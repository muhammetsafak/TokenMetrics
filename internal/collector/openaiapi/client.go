// Package openaiapi collects organization usage from the OpenAI Organization
// Usage API. It needs an Admin key, which is read from the OS keychain on
// every run and never logged.
//
// Endpoints (reference: https://platform.openai.com/docs/api-reference/usage,
// schema: https://github.com/openai/openai-openapi/blob/master/openapi.yaml):
//   - GET /v1/organization/usage/completions
//     https://platform.openai.com/docs/api-reference/usage/completions
//   - GET /v1/organization/costs
//     https://platform.openai.com/docs/api-reference/usage/costs
package openaiapi

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
	DefaultBaseURL = "https://api.openai.com"

	usagePath = "/v1/organization/usage/completions"
	costPath  = "/v1/organization/costs"
)

// maxPages bounds pagination so a misbehaving server cannot loop forever. It
// is a variable only so tests can lower it.
var maxPages = 1000

// Bucket widths supported by this collector.
const (
	Width1d = "1d"
	Width1h = "1h"
)

// maxBuckets is the documented per-page bucket limit for each width.
var maxBuckets = map[string]int{Width1d: 31, Width1h: 168}

// costMaxBuckets is the documented per-page bucket limit of the costs endpoint.
const costMaxBuckets = 180

// UsageResult is one row of a completions usage bucket. With group_by=model
// and group_by=project_id the Model and ProjectID fields are populated.
type UsageResult struct {
	Model     *string `json:"model"`
	ProjectID *string `json:"project_id"`

	InputTokens              int64  `json:"input_tokens"` // includes cached and cache-write tokens
	InputCachedTokens        int64  `json:"input_cached_tokens"`
	InputCacheWriteTokens    int64  `json:"input_cache_write_tokens"`
	InputCacheWrite30mTokens *int64 `json:"input_cache_write_30m_tokens"`
	InputCacheWrite12hTokens int64  `json:"input_cache_write_12h_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	NumModelRequests         int64  `json:"num_model_requests"`
}

// UsageBucket is one time bucket; times are unix seconds.
type UsageBucket struct {
	StartTime int64         `json:"start_time"`
	EndTime   int64         `json:"end_time"`
	Results   []UsageResult `json:"results"`
}

// CostResult is one row of a daily cost bucket.
type CostResult struct {
	Amount struct {
		Value    float64 `json:"value"`
		Currency string  `json:"currency"`
	} `json:"amount"`
	LineItem  *string `json:"line_item"`
	ProjectID *string `json:"project_id"`
}

// CostBucket is one daily bucket of the costs endpoint.
type CostBucket struct {
	StartTime int64        `json:"start_time"`
	EndTime   int64        `json:"end_time"`
	Results   []CostResult `json:"results"`
}

type page[T any] struct {
	Data     []T     `json:"data"`
	HasMore  bool    `json:"has_more"`
	NextPage *string `json:"next_page"`
}

// Client calls the Organization API. It holds the admin key only in memory.
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
		return nil, errors.New("openaiapi: admin key is empty")
	}
	for _, r := range adminKey {
		if r < 0x21 || r > 0x7e {
			// Never echo the key; only say what is wrong with it.
			return nil, errors.New("openaiapi: admin key contains whitespace or non-printable characters")
		}
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return nil, errors.New("openaiapi: invalid base URL")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return nil, errors.New("openaiapi: plain http is only allowed for loopback hosts")
		}
	default:
		return nil, errors.New("openaiapi: base URL must use https")
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
	h.Set("Authorization", "Bearer "+c.adminKey)
	h.Set("Accept", "application/json")
	return h
}

// FetchUsage returns every completions usage bucket starting at or after
// start, oldest first, following next_page until exhausted. On error it
// returns the buckets and page count collected so far alongside the error.
func (c *Client) FetchUsage(ctx context.Context, start time.Time, width string, groupBy []string) ([]UsageBucket, int, error) {
	limit, ok := maxBuckets[width]
	if !ok {
		return nil, 0, fmt.Errorf("openaiapi: unsupported bucket width %q", width)
	}
	q := url.Values{}
	q.Set("start_time", strconv.FormatInt(start.UTC().Unix(), 10))
	q.Set("bucket_width", width)
	q.Set("limit", strconv.Itoa(limit))
	for _, g := range groupBy {
		q.Add("group_by", g)
	}
	return fetchAll[UsageBucket](ctx, c, usagePath, q)
}

// FetchCost returns the daily cost buckets starting at or after start.
func (c *Client) FetchCost(ctx context.Context, start time.Time, groupBy []string) ([]CostBucket, int, error) {
	q := url.Values{}
	q.Set("start_time", strconv.FormatInt(start.UTC().Unix(), 10))
	q.Set("bucket_width", Width1d)
	q.Set("limit", strconv.Itoa(costMaxBuckets))
	for _, g := range groupBy {
		q.Add("group_by", g)
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
			return all, pages, fmt.Errorf("openaiapi: more than %d pages, giving up", maxPages)
		}
		body, err := c.http.Get(ctx, c.baseURL+path+"?"+q.Encode(), c.header())
		if err != nil {
			return all, pages, fmt.Errorf("openaiapi: GET %s: %w", path, err)
		}
		var p page[T]
		if err := json.Unmarshal(body, &p); err != nil {
			return all, pages, fmt.Errorf("openaiapi: decode %s response: %w", path, err)
		}
		pages++
		all = append(all, p.Data...)

		if !p.HasMore {
			return all, pages, nil
		}
		if p.NextPage == nil || *p.NextPage == "" {
			return all, pages, fmt.Errorf("openaiapi: %s reported has_more without next_page", path)
		}
		if seen[*p.NextPage] {
			return all, pages, fmt.Errorf("openaiapi: %s returned a repeated next_page cursor", path)
		}
		seen[*p.NextPage] = true
		q.Set("page", *p.NextPage)
	}
}
