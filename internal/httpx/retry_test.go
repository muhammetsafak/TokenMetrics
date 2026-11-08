package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type sleepRecorder struct{ delays []time.Duration }

func (s *sleepRecorder) sleep(ctx context.Context, d time.Duration) error {
	s.delays = append(s.delays, d)
	return ctx.Err()
}

func newTestClient(rec *sleepRecorder, cfg Config) *Client {
	if rec != nil {
		cfg.Sleep = rec.sleep
	}
	return New(cfg)
}

func TestGetSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "yes" || r.Header.Get("User-Agent") != "ua/1" {
			t.Errorf("headers = %v", r.Header)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(nil, Config{UserAgent: "ua/1"})
	body, err := c.Get(context.Background(), srv.URL, http.Header{"X-Test": {"yes"}})
	if err != nil || string(body) != `{"ok":true}` {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestRetryAfterIsHonored(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("fine"))
	}))
	defer srv.Close()

	rec := &sleepRecorder{}
	c := newTestClient(rec, Config{})
	body, err := c.Get(context.Background(), srv.URL, nil)
	if err != nil || string(body) != "fine" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if len(rec.delays) != 1 || rec.delays[0] != 7*time.Second {
		t.Fatalf("delays = %v, want exactly [7s]", rec.delays)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", now.Add(9*time.Second).Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("fine"))
	}))
	defer srv.Close()

	rec := &sleepRecorder{}
	c := newTestClient(rec, Config{Now: func() time.Time { return now }})
	if _, err := c.Get(context.Background(), srv.URL, nil); err != nil {
		t.Fatal(err)
	}
	if len(rec.delays) != 1 || rec.delays[0] != 9*time.Second {
		t.Fatalf("delays = %v, want [9s]", rec.delays)
	}
}

func TestRetryAfterBeyondMaxDelayFailsFast(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	rec := &sleepRecorder{}
	c := newTestClient(rec, Config{MaxDelay: time.Minute})
	_, err := c.Get(context.Background(), srv.URL, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 429 || se.RetryAfter != time.Hour {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 || len(rec.delays) != 0 {
		t.Fatalf("calls=%d delays=%v", calls.Load(), rec.delays)
	}
}

func TestExponentialBackoffOn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	rec := &sleepRecorder{}
	c := newTestClient(rec, Config{MaxAttempts: 4, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second})
	_, err := c.Get(context.Background(), srv.URL, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 503 {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 4 || len(rec.delays) != 3 {
		t.Fatalf("calls=%d delays=%v", calls.Load(), rec.delays)
	}
	// Equal jitter: delay n is within [base*2^n / 2, base*2^n].
	for i, d := range rec.delays {
		full := 100 * time.Millisecond << i
		if d < full/2 || d > full {
			t.Errorf("delay[%d] = %v, want within [%v, %v]", i, d, full/2, full)
		}
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer srv.Close()

	rec := &sleepRecorder{}
	c := newTestClient(rec, Config{})
	_, err := c.Get(context.Background(), srv.URL, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 401 {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestContextCancelStopsRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := New(Config{
		Sleep: func(ctx context.Context, d time.Duration) error {
			cancel()
			return ctx.Err()
		},
	})
	_, err := c.Get(ctx, srv.URL, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(Config{}).Get(ctx, "http://127.0.0.1:1/", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestPerAttemptTimeoutIsRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		w.Write([]byte("late but fine"))
	}))
	defer srv.Close()

	rec := &sleepRecorder{}
	c := newTestClient(rec, Config{Timeout: 100 * time.Millisecond})
	body, err := c.Get(context.Background(), srv.URL, nil)
	if err != nil || string(body) != "late but fine" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestBodyTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 1000)))
	}))
	defer srv.Close()

	c := newTestClient(&sleepRecorder{}, Config{MaxBodyBytes: 999})
	if _, err := c.Get(context.Background(), srv.URL, nil); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
	c = newTestClient(&sleepRecorder{}, Config{MaxBodyBytes: 1000})
	if body, err := c.Get(context.Background(), srv.URL, nil); err != nil || len(body) != 1000 {
		t.Fatalf("len=%d err=%v", len(body), err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var targetHit atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit.Add(1)
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()

	c := newTestClient(&sleepRecorder{}, Config{})
	_, err := c.Get(context.Background(), srv.URL, http.Header{"X-Api-Key": {"secret"}})
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusFound {
		t.Fatalf("err = %v", err)
	}
	if targetHit.Load() != 0 {
		t.Fatal("redirect target was contacted")
	}
}

func TestErrorsNeverContainSecrets(t *testing.T) {
	const secret = "sk-admin-FAKESECRET-1234567890"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A misbehaving server echoing the credential back.
		http.Error(w, "invalid key "+r.Header.Get("X-Api-Key"), http.StatusForbidden)
	}))
	defer srv.Close()

	c := newTestClient(&sleepRecorder{}, Config{Redact: []string{secret, ""}})
	_, err := c.Get(context.Background(), srv.URL, http.Header{"X-Api-Key": {secret}})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks secret: %v", err)
	}
	var se *StatusError
	if !errors.As(err, &se) || !strings.Contains(se.Body, "[REDACTED]") {
		t.Fatalf("body not redacted: %+v", se)
	}

	// Transport failure: the closed server's error must not carry headers.
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	c = newTestClient(&sleepRecorder{}, Config{MaxAttempts: 2, Redact: []string{secret}})
	_, err = c.Get(context.Background(), url+"/?token="+secret, http.Header{"X-Api-Key": {secret}})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error = %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	c := New(Config{})
	for in, want := range map[string]time.Duration{
		"":                0,
		"abc":             0,
		"0":               0,
		"-3":              0,
		"3":               3 * time.Second,
		"999999999999999": 24 * time.Hour,
	} {
		if got := c.parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestErrorBodyKeyStraddlingTruncationIsFullyRedacted(t *testing.T) {
	const secret = "sk-admin-FAKESECRET-abcdefghijklmnopqrstuvwxyz0123456789"
	// The key starts a few bytes before the 512-byte display limit, so a
	// truncate-then-redact order would leave a fragment of it behind.
	for _, offset := range []int{400, 480, 500, 511, 512, 530} {
		body := strings.Repeat("a", offset) + secret + strings.Repeat("b", 2000)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, body, http.StatusBadGateway)
		}))
		c := newTestClient(&sleepRecorder{}, Config{MaxAttempts: 1, Redact: []string{secret}})
		_, err := c.Get(context.Background(), srv.URL, nil)
		srv.Close()
		var se *StatusError
		if !errors.As(err, &se) {
			t.Fatalf("offset %d: err = %v", offset, err)
		}
		if len(se.Body) > errorBodyLimit {
			t.Errorf("offset %d: body length %d exceeds limit", offset, len(se.Body))
		}
		for n := 8; n <= len(secret); n++ {
			if strings.Contains(se.Body, secret[:n]) || strings.Contains(err.Error(), secret[:n]) {
				t.Fatalf("offset %d: fragment %q of the key leaked", offset, secret[:n])
			}
		}
	}
}

func TestInvalidHeaderIsNotRetried(t *testing.T) {
	rec := &sleepRecorder{}
	c := newTestClient(rec, Config{})
	_, err := c.Get(context.Background(), "http://127.0.0.1:1/", http.Header{"X-Api-Key": {"bad\nkey"}})
	if err == nil {
		t.Fatal("expected error")
	}
	if len(rec.delays) != 0 {
		t.Fatalf("malformed header was retried: %v", rec.delays)
	}
	if strings.Contains(err.Error(), "bad") {
		t.Fatalf("error leaks header value: %v", err)
	}
}

// leakedFragment returns a piece of secret (8+ characters) found in s, or "".
func leakedFragment(s, secret string) string {
	const minLen = 8
	for i := 0; i+minLen <= len(secret); i++ {
		for j := i + minLen; j <= len(secret); j++ {
			if strings.Contains(s, secret[i:j]) {
				return secret[i:j]
			}
		}
	}
	return ""
}

func TestErrorBodyRedactionSurvivesShiftingEdgeCases(t *testing.T) {
	const secret = "sk-admin-FAKESECRET-abcdefghijklmnopqrstuvwxyz0123456789"
	bodies := map[string]string{
		// Leading whitespace is trimmed after the read window cut the key off.
		"leading whitespace, key cut by the read window": strings.Repeat(" ", errorReadLimit-3-20) + "abc" + secret,
		"leading newlines, key cut by the read window":   strings.Repeat("\n", errorReadLimit-3-40) + "abc" + secret,
		// A first match shrinks the string and used to shift the second one.
		"key reflected twice":                      secret + strings.Repeat("a", 520-len(secret)) + secret + strings.Repeat("b", 3000),
		"second reflection cut by the read window": secret + strings.Repeat("a", errorReadLimit-len(secret)-20) + secret + "tail",
		"reflection right after the display limit": strings.Repeat("a", errorBodyLimit-10) + secret + strings.Repeat("b", 100),
		"key only partly inside the read window":   strings.Repeat("a", errorReadLimit-1) + secret,
		"key as the whole body":                    secret,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, body, http.StatusBadGateway)
			}))
			defer srv.Close()
			c := newTestClient(&sleepRecorder{}, Config{MaxAttempts: 1, Redact: []string{secret}})
			_, err := c.Get(context.Background(), srv.URL, nil)
			var se *StatusError
			if !errors.As(err, &se) {
				t.Fatalf("err = %v", err)
			}
			if len(se.Body) > errorBodyLimit {
				t.Errorf("body length %d exceeds limit", len(se.Body))
			}
			if frag := leakedFragment(se.Body, secret); frag != "" {
				t.Fatalf("fragment %q of the key leaked in %q", frag, se.Body)
			}
			if frag := leakedFragment(err.Error(), secret); frag != "" {
				t.Fatalf("fragment %q of the key leaked in the error message", frag)
			}
		})
	}
}

func TestPartialSecretSuffix(t *testing.T) {
	c := New(Config{Redact: []string{"sk-admin-SECRET", "other-KEY"}})
	for in, want := range map[string]int{
		"":                          0,
		"nothing here":              0,
		"text sk-admin-SEC":         12,
		"text sk-admin-SECRET":      0, // complete match: handled by scrub, not dropped
		"text other-":               6,
		"ends with s":               1,
		"sk-admin-SECRET and other": 5,
	} {
		if got := c.partialSecretSuffix([]byte(in)); got != want {
			t.Errorf("partialSecretSuffix(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestErrorBodyCutByReadFailureDropsKeyFragment(t *testing.T) {
	const secret = "sk-admin-FAKESECRET-abcdefghijklmnopqrstuvwxyz0123456789"
	// The server announces a long body, sends part of the key and aborts the
	// connection, so reading the body fails with an unexpected EOF.
	for name, sent := range map[string]string{
		"fragment right after text":         "upstream said: " + secret[:25],
		"fragment after leading whitespace": strings.Repeat(" ", 200) + secret[:40],
		"complete key then a fragment":      secret + " and again " + secret[:30],
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "5000")
				w.WriteHeader(http.StatusBadGateway)
				w.Write([]byte(sent))
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			}))
			defer srv.Close()

			c := newTestClient(&sleepRecorder{}, Config{MaxAttempts: 1, Redact: []string{secret}})
			_, err := c.Get(context.Background(), srv.URL, nil)
			var se *StatusError
			if !errors.As(err, &se) || se.StatusCode != http.StatusBadGateway {
				t.Fatalf("err = %v, want a 502 StatusError", err)
			}
			if frag := leakedFragment(se.Body, secret); frag != "" {
				t.Fatalf("fragment %q of the key leaked in %q", frag, se.Body)
			}
			if frag := leakedFragment(err.Error(), secret); frag != "" {
				t.Fatalf("fragment %q leaked in the error message", frag)
			}
		})
	}
}
