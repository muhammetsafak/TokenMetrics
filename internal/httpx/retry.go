// Package httpx is a small HTTP GET client for the provider usage APIs: it
// applies a per-attempt timeout, retries 429/5xx and network errors with
// exponential backoff (honoring Retry-After), stops on context cancellation,
// caps the response body size and keeps secrets out of every error message.
package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Defaults applied by New for zero Config fields.
const (
	DefaultTimeout      = 30 * time.Second
	DefaultMaxAttempts  = 5
	DefaultBaseDelay    = 500 * time.Millisecond
	DefaultMaxDelay     = 60 * time.Second
	DefaultMaxBodyBytes = 32 << 20 // 32 MiB

	// errorBodyLimit bounds how much of an error response body is kept in a
	// StatusError.
	errorBodyLimit = 512
	// errorReadLimit is how much of an error body is read before redaction and
	// truncation; it is far larger than errorBodyLimit so that dropping
	// leading whitespace or replacing a secret cannot shift anything that the
	// read window cut off into the displayed part.
	errorReadLimit = 8192
)

// ErrBodyTooLarge is returned when a response body exceeds Config.MaxBodyBytes.
var ErrBodyTooLarge = errors.New("httpx: response body too large")

// StatusError is a non-2xx response that was not (or no longer) retried.
// It never contains request headers; Body is a truncated, redacted snippet.
type StatusError struct {
	StatusCode int
	Body       string
	// RetryAfter is the server's Retry-After hint, zero if absent.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("httpx: unexpected status %d", e.StatusCode)
	if e.RetryAfter > 0 {
		msg += fmt.Sprintf(" (retry after %s)", e.RetryAfter)
	}
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// Config configures a Client. Zero values select the defaults above.
type Config struct {
	// HTTPClient performs the requests. The default never follows redirects,
	// because a redirect would forward credential headers such as x-api-key.
	HTTPClient *http.Client
	// Timeout bounds each attempt, including reading the body.
	Timeout time.Duration
	// MaxAttempts is the total number of attempts (first try included).
	MaxAttempts int
	// BaseDelay is the first backoff step; it doubles per retry up to MaxDelay.
	BaseDelay time.Duration
	// MaxDelay caps a backoff step and the longest Retry-After that is waited
	// for. A larger Retry-After makes the request fail with a StatusError.
	MaxDelay time.Duration
	// MaxBodyBytes caps the response body size.
	MaxBodyBytes int64
	// UserAgent is sent on every request if non-empty.
	UserAgent string
	// Redact lists secret strings that are scrubbed from every error message.
	Redact []string
	// Sleep waits for d or until ctx is done. Tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now returns the current time (for HTTP-date Retry-After). Tests replace it.
	Now func() time.Time
}

// Client is safe for concurrent use.
type Client struct {
	cfg Config
}

// New returns a Client with defaults filled in.
func New(cfg Config) *Client {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = DefaultBaseDelay
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = DefaultMaxDelay
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	// Drop empty secrets: replacing "" would corrupt every message.
	var redact []string
	for _, s := range cfg.Redact {
		if s != "" {
			redact = append(redact, s)
		}
	}
	cfg.Redact = redact
	return &Client{cfg: cfg}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// redactedError carries a scrubbed message while keeping the original error
// available to errors.Is / errors.As.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// errorBody builds the displayable snippet of an error response. The order
// matters: (1) if the read window (or a failed read) cut the body off, drop a trailing fragment
// that is a prefix of a secret, because a fragment cannot be recognized by the
// redaction; (2) redact every occurrence of every secret in the whole buffer;
// (3) trim and only then truncate to errorBodyLimit. After step 2 no secret
// text is left, so the later steps, which may shift or cut the string
// anywhere, cannot expose a part of one.
func (c *Client) errorBody(raw []byte, cutOff bool) string {
	if cutOff {
		raw = raw[:len(raw)-c.partialSecretSuffix(raw)]
	}
	return truncate(strings.TrimSpace(c.scrub(string(raw))), errorBodyLimit)
}

// partialSecretSuffix returns the length of the longest suffix of buf that is
// a proper prefix of one of the secrets (0 if there is none).
func (c *Client) partialSecretSuffix(buf []byte) int {
	best := 0
	for _, secret := range c.cfg.Redact {
		for n := min(len(secret)-1, len(buf)); n > best; n-- {
			if bytes.HasSuffix(buf, []byte(secret[:n])) {
				best = n
				break
			}
		}
	}
	return best
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

func (c *Client) scrub(s string) string {
	for _, secret := range c.cfg.Redact {
		s = strings.ReplaceAll(s, secret, "[REDACTED]")
	}
	return s
}

// Get performs a GET and returns the response body of a 2xx answer. header
// may carry credentials; it is never included in errors or logs.
func (c *Client) Get(ctx context.Context, rawURL string, header http.Header) ([]byte, error) {
	target := describeURL(rawURL)
	var lastErr error
	for attempt := 0; attempt < c.cfg.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, status, retryAfter, err := c.once(ctx, rawURL, header)
		if err == nil {
			return body, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !retryable(status, err) || attempt == c.cfg.MaxAttempts-1 {
			break
		}
		delay := c.backoff(attempt)
		if retryAfter > 0 {
			if retryAfter > c.cfg.MaxDelay {
				break // waiting longer than we are willing to; surface it
			}
			delay = retryAfter
		}
		if err := c.cfg.Sleep(ctx, delay); err != nil {
			return nil, err
		}
	}

	var se *StatusError
	if errors.As(lastErr, &se) {
		return nil, lastErr
	}
	return nil, &redactedError{msg: c.scrub(fmt.Sprintf("httpx: GET %s: %v", target, lastErr)), err: lastErr}
}

// once makes a single attempt. status is 0 for transport failures.
func (c *Client) once(ctx context.Context, rawURL string, header http.Header) (body []byte, status int, retryAfter time.Duration, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, 0, &redactedError{msg: c.scrub(fmt.Sprintf("httpx: build request: %v", err)), err: errBuild}
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if c.cfg.UserAgent != "" && req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.cfg.UserAgent)
	}

	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, errorReadLimit))
		ra := c.parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, resp.StatusCode, ra, &StatusError{
			StatusCode: resp.StatusCode,
			// A read that failed midway ends the data as abruptly as the read
			// window does, so it takes the same fragment-dropping path.
			Body:       c.errorBody(raw, readErr != nil || len(raw) == errorReadLimit),
			RetryAfter: ra,
		}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBodyBytes+1))
	if err != nil {
		return nil, resp.StatusCode, 0, err
	}
	if int64(len(data)) > c.cfg.MaxBodyBytes {
		return nil, resp.StatusCode, 0, ErrBodyTooLarge
	}
	return data, resp.StatusCode, 0, nil
}

var errBuild = errors.New("httpx: invalid request")

// retryable reports whether a failed attempt is worth repeating.
func retryable(status int, err error) bool {
	switch {
	case errors.Is(err, ErrBodyTooLarge), errors.Is(err, errBuild):
		return false
	case status == 0 && strings.Contains(err.Error(), "invalid header field"):
		return false // a malformed credential will not fix itself
	case status == http.StatusTooManyRequests:
		return true
	case status == http.StatusInternalServerError, status == http.StatusBadGateway,
		status == http.StatusServiceUnavailable, status == http.StatusGatewayTimeout,
		status == 529: // Anthropic "overloaded"
		return true
	case status != 0:
		return false
	}
	// Transport failure (connection reset, timeout, truncated body). The
	// caller has already ruled out context cancellation.
	return true
}

// backoff returns the wait before retry number attempt+1: exponential with
// "equal jitter" (half fixed, half random), capped at MaxDelay.
func (c *Client) backoff(attempt int) time.Duration {
	d := c.cfg.BaseDelay
	for i := 0; i < attempt && d < c.cfg.MaxDelay; i++ {
		d *= 2
	}
	if d > c.cfg.MaxDelay {
		d = c.cfg.MaxDelay
	}
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// parseRetryAfter understands both "seconds" and HTTP-date forms.
func (c *Client) parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		const maxSecs = int64(24 * time.Hour / time.Second)
		if secs > maxSecs {
			secs = maxSecs
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(c.cfg.Now()); d > 0 {
			return d
		}
	}
	return 0
}

// describeURL returns scheme-less host+path of rawURL for error messages; the
// query string and any userinfo are dropped.
func describeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "<url>"
	}
	return u.Host + u.Path
}
