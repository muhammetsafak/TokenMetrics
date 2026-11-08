// Package anthropicapi collects organization usage from the Anthropic Admin
// API (Usage & Cost). It needs an Admin API key (sk-ant-admin...), which is
// read from the OS keychain on every run and never logged.
//
// Endpoints (https://platform.claude.com/docs/en/manage-claude/usage-cost-api):
//   - GET /v1/organizations/usage_report/messages
//     https://platform.claude.com/docs/en/api/beta/organization/usage_report/retrieve_messages
//   - GET /v1/organizations/cost_report
//     https://platform.claude.com/docs/en/api/beta/organization/cost_report/retrieve
package anthropicapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/httpx"
)

const (
	// DefaultBaseURL is the production API host.
	DefaultBaseURL = "https://api.anthropic.com"
	// APIVersion is sent in the anthropic-version header.
	APIVersion = "2023-06-01"

	usagePath = "/v1/organizations/usage_report/messages"
	costPath  = "/v1/organizations/cost_report"
)

// maxPages bounds pagination so a misbehaving server cannot loop forever. It
// is a variable only so tests can lower it.
var maxPages = 1000

// Bucket widths accepted by the usage report. The cost report only supports 1d.
const (
	Width1d = "1d"
	Width1h = "1h"
	Width1m = "1m"
)

// maxBuckets is the documented per-page bucket limit for each width.
var maxBuckets = map[string]int{Width1d: 31, Width1h: 168, Width1m: 1440}

// UsageResult is one row of a usage bucket. With group_by=model and
// group_by=workspace_id the Model and WorkspaceID fields are populated.
type UsageResult struct {
	Model       *string `json:"model"`
	WorkspaceID *string `json:"workspace_id"`

	UncachedInputTokens  int64 `json:"uncached_input_tokens"`
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CacheCreation        struct {
		Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
		Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
	} `json:"cache_creation"`
}

// UsageBucket is one time bucket of the usage report.
type UsageBucket struct {
	StartingAt time.Time     `json:"starting_at"`
	EndingAt   time.Time     `json:"ending_at"`
	Results    []UsageResult `json:"results"`
}

// CostResult is one row of a cost bucket. Amount is a decimal string in cents.
type CostResult struct {
	Amount      string  `json:"amount"`
	Currency    string  `json:"currency"`
	CostType    *string `json:"cost_type"`
	Model       *string `json:"model"`
	WorkspaceID *string `json:"workspace_id"`
}

// CostBucket is one daily bucket of the cost report.
type CostBucket struct {
	StartingAt time.Time    `json:"starting_at"`
	EndingAt   time.Time    `json:"ending_at"`
	Results    []CostResult `json:"results"`
}

type page[T any] struct {
	Data     []T     `json:"data"`
	HasMore  bool    `json:"has_more"`
	NextPage *string `json:"next_page"`
}

// Client calls the Admin API. It holds the admin key only in memory.
type Client struct {
	baseURL  string
	adminKey string
	http     *httpx.Client
}

// NewClient validates baseURL (https, or http on a loopback host for tests)
// and returns a client. h should have been built with the key in its Redact
// list; see Collector.
func NewClient(baseURL, adminKey string, h *httpx.Client) (*Client, error) {
	if strings.TrimSpace(adminKey) == "" {
		return nil, errors.New("anthropicapi: admin key is empty")
	}
	for _, r := range adminKey {
		if r < 0x21 || r > 0x7e {
			// Never echo the key; only say what is wrong with it.
			return nil, errors.New("anthropicapi: admin key contains whitespace or non-printable characters")
		}
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return nil, errors.New("anthropicapi: invalid base URL")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return nil, errors.New("anthropicapi: plain http is only allowed for loopback hosts")
		}
	default:
		return nil, errors.New("anthropicapi: base URL must use https")
	}
	if h == nil {
		h = httpx.New(httpx.Config{Redact: []string{adminKey}})
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), adminKey: adminKey, http: h}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Client) header() http.Header {
	h := http.Header{}
	h.Set("x-api-key", c.adminKey)
	h.Set("anthropic-version", APIVersion)
	h.Set("Accept", "application/json")
	return h
}

// FetchUsage returns every usage bucket starting at or after start, oldest
// first, following next_page until the report is exhausted. On error it
// returns the buckets and page count collected so far alongside the error.
func (c *Client) FetchUsage(ctx context.Context, start time.Time, width string, groupBy []string) ([]UsageBucket, int, error) {
	limit, ok := maxBuckets[width]
	if !ok {
		return nil, 0, fmt.Errorf("anthropicapi: unsupported bucket width %q", width)
	}
	q := url.Values{}
	q.Set("starting_at", start.UTC().Format(time.RFC3339))
	q.Set("bucket_width", width)
	q.Set("limit", strconv.Itoa(limit))
	for _, g := range groupBy {
		q.Add("group_by[]", g)
	}
	return fetchAll[UsageBucket](ctx, c, usagePath, q)
}

// FetchCost returns the daily cost buckets starting at or after start.
func (c *Client) FetchCost(ctx context.Context, start time.Time, groupBy []string) ([]CostBucket, int, error) {
	q := url.Values{}
	q.Set("starting_at", start.UTC().Format(time.RFC3339))
	q.Set("bucket_width", Width1d)
	q.Set("limit", "31")
	for _, g := range groupBy {
		q.Add("group_by[]", g)
	}
	return fetchAll[CostBucket](ctx, c, costPath, q)
}

func fetchAll[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, int, error) {
	var (
		all   []T
		pages int
		seen  = map[string]bool{}
	)
	for {
		if pages >= maxPages {
			return all, pages, fmt.Errorf("anthropicapi: more than %d pages, giving up", maxPages)
		}
		body, err := c.http.Get(ctx, c.baseURL+path+"?"+q.Encode(), c.header())
		if err != nil {
			return all, pages, fmt.Errorf("anthropicapi: GET %s: %w", path, err)
		}
		var p page[T]
		if err := json.Unmarshal(body, &p); err != nil {
			return all, pages, fmt.Errorf("anthropicapi: decode %s response: %w", path, err)
		}
		pages++
		all = append(all, p.Data...)

		if !p.HasMore {
			return all, pages, nil
		}
		if p.NextPage == nil || *p.NextPage == "" {
			return all, pages, fmt.Errorf("anthropicapi: %s reported has_more without next_page", path)
		}
		if seen[*p.NextPage] {
			return all, pages, fmt.Errorf("anthropicapi: %s returned a repeated next_page cursor", path)
		}
		seen[*p.NextPage] = true
		q.Set("page", *p.NextPage)
	}
}
