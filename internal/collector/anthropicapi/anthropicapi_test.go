package anthropicapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/collector"
	"github.com/muhammetsafak/TokenMetrics/internal/httpx"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/secrets"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
	"github.com/zalando/go-keyring"
)

const fakeKey = "sk-ant-admin01-FAKEKEY-do-not-use-0123456789"

var testNow = time.Date(2026, 11, 6, 15, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fakeAPI is an httptest server whose behavior each test scripts.
type fakeAPI struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	handler  func(w http.ResponseWriter, r *http.Request, n int)
}

func newFakeAPI(t *testing.T, h func(w http.ResponseWriter, r *http.Request, n int)) *fakeAPI {
	t.Helper()
	f := &fakeAPI{handler: h}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Clone(context.Background()))
		n := len(f.requests)
		f.mu.Unlock()
		f.handler(w, r, n)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAPI) reqs() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.requests...)
}

type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.d = append(s.d, d)
	s.mu.Unlock()
	return ctx.Err()
}

func newCollector(api *fakeAPI, sl *sleeps, opts ...Option) *Collector {
	base := []Option{
		WithBaseURL(api.URL),
		WithKeyFunc(func() (string, error) { return fakeKey, nil }),
		WithClock(func() time.Time { return testNow }),
		WithHTTPConfig(httpx.Config{Sleep: sl.sleep, BaseDelay: time.Millisecond}),
	}
	return New(append(base, opts...)...)
}

func pagedUsage(t *testing.T, page2 string) func(http.ResponseWriter, *http.Request, int) {
	p1, p2 := fixture(t, "usage_page1.json"), fixture(t, page2)
	return func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path != usagePath:
			http.NotFound(w, r)
		case r.URL.Query().Get("page") == "":
			w.Write(p1)
		default:
			w.Write(p2)
		}
	}
}

func run(t *testing.T, c *Collector, s *store.Store, logs *bytes.Buffer) (collector.Result, error) {
	t.Helper()
	var logger *slog.Logger
	if logs != nil {
		logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return c.Collect(context.Background(), collector.Deps{Store: s, DeviceID: "dev-1", Logger: logger})
}

func TestPaginationCollectsAllPages(t *testing.T) {
	api := newFakeAPI(t, pagedUsage(t, "usage_page2.json"))
	s := openStore(t)
	res, err := run(t, newCollector(api, &sleeps{}), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 4 || res.Updated != 0 || res.Files != 2 {
		t.Fatalf("result = %+v, want Added 4, Updated 0, Files 2", res)
	}

	reqs := api.reqs()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	first := reqs[0]
	if first.Header.Get("x-api-key") != fakeKey || first.Header.Get("anthropic-version") != APIVersion {
		t.Errorf("auth headers wrong: %v", first.Header)
	}
	q := first.URL.Query()
	if q.Get("bucket_width") != "1d" || q.Get("limit") != "31" ||
		strings.Join(q["group_by[]"], ",") != "model,workspace_id" {
		t.Errorf("query = %v", q)
	}
	if want := testNow.Add(-90 * 24 * time.Hour).Truncate(24 * time.Hour).Format(time.RFC3339); q.Get("starting_at") != want {
		t.Errorf("starting_at = %q, want %q (90 days back, day aligned)", q.Get("starting_at"), want)
	}
	if reqs[1].URL.Query().Get("page") != "page_MjAyNi0xMS0wNlQwMDowMDowMFo=" {
		t.Errorf("second request page = %q", reqs[1].URL.Query().Get("page"))
	}

	tot, err := s.Totals(context.Background(), store.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if tot.RecordCount != 4 || tot.InputTokens != 1000+300+700+40 || tot.OutputTokens != 500+100+350+20 ||
		tot.CacheReadTokens != 270 || tot.CacheWrite5mTokens != 57 || tot.CacheWrite1hTokens != 10 {
		t.Fatalf("totals = %+v", tot)
	}

	rec, ok, err := s.GetUsageRecord(context.Background(), model.SourceAnthropicAPI,
		"anthropic:2026-11-04T00:00:00Z:claude-sonnet-5-5:")
	if err != nil || !ok {
		t.Fatalf("record missing: %v %v", ok, err)
	}
	if rec.Service != model.ServiceAnthropic || rec.DeviceID != "dev-1" || rec.Project != "" ||
		rec.CacheWrite5mTokens != 50 || rec.CacheWrite1hTokens != 10 || rec.ReportedCostUSD != nil ||
		!rec.Timestamp.Equal(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("record = %+v", rec)
	}
}

func TestSecondSyncUpdatesSameBucket(t *testing.T) {
	var mu sync.Mutex
	page2 := "usage_page2.json"
	p1 := pagedUsage(t, "usage_page2.json")
	p2 := pagedUsage(t, "usage_page2_updated.json")
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		mu.Lock()
		cur := page2
		mu.Unlock()
		if cur == "usage_page2.json" {
			p1(w, r, n)
		} else {
			p2(w, r, n)
		}
	})
	s := openStore(t)
	c := newCollector(api, &sleeps{})

	if res, err := run(t, c, s, nil); err != nil || res.Added != 4 {
		t.Fatalf("first sync: %+v %v", res, err)
	}
	// Re-running over identical data changes nothing.
	if res, err := run(t, c, s, nil); err != nil || res.Added != 0 || res.Updated != 0 {
		t.Fatalf("idempotent sync: %+v %v", res, err)
	}

	// The newest bucket (2026-11-06) now reports more usage.
	mu.Lock()
	page2 = "usage_page2_updated.json"
	mu.Unlock()
	res, err := run(t, c, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.Updated != 1 {
		t.Fatalf("third sync = %+v, want Added 0, Updated 1", res)
	}

	tot, _ := s.Totals(context.Background(), store.Filter{})
	if tot.RecordCount != 4 {
		t.Fatalf("RecordCount = %d, want 4 (no duplicates)", tot.RecordCount)
	}
	if tot.InputTokens != 1000+300+700+400 || tot.OutputTokens != 500+100+350+200 {
		t.Fatalf("totals = %+v", tot)
	}

	// Later syncs start at the newest bucket of the previous sync.
	reqs := api.reqs()
	last := reqs[len(reqs)-2] // first request of the third sync
	if got := last.URL.Query().Get("starting_at"); got != "2026-11-06T00:00:00Z" {
		t.Errorf("incremental starting_at = %q, want 2026-11-06T00:00:00Z", got)
	}
}

func TestRetryAfterHonoredOn429(t *testing.T) {
	good := pagedUsage(t, "usage_page2.json")
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		good(w, r, n)
	})
	sl := &sleeps{}
	res, err := run(t, newCollector(api, sl), openStore(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 4 {
		t.Fatalf("result = %+v", res)
	}
	if len(sl.d) != 1 || sl.d[0] != 3*time.Second {
		t.Fatalf("sleeps = %v, want [3s]", sl.d)
	}
}

func TestCostIsAttachedAndAddsCostOnlyRecords(t *testing.T) {
	usage := pagedUsage(t, "usage_page2.json")
	cost := fixture(t, "cost_page1.json")
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == costPath {
			if r.URL.Query().Get("bucket_width") != "1d" ||
				strings.Join(r.URL.Query()["group_by[]"], ",") != "workspace_id,description" {
				t.Errorf("cost query = %v", r.URL.Query())
			}
			w.Write(cost)
			return
		}
		usage(w, r, n)
	})
	s := openStore(t)
	res, err := run(t, newCollector(api, &sleeps{}, WithCost(true)), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 5 || res.Files != 3 {
		t.Fatalf("result = %+v, want Added 5 (4 usage + 1 cost-only), Files 3", res)
	}

	rec, ok, _ := s.GetUsageRecord(context.Background(), model.SourceAnthropicAPI,
		"anthropic:2026-11-05T00:00:00Z:claude-opus-5-5:wrkspc_01ABC")
	if !ok || rec.ReportedCostUSD == nil || math.Abs(*rec.ReportedCostUSD-3.505) > 1e-9 {
		t.Fatalf("opus 11-05 record = %+v", rec)
	}
	other, _, _ := s.GetUsageRecord(context.Background(), model.SourceAnthropicAPI,
		"anthropic:2026-11-04T00:00:00Z:claude-opus-5-5:wrkspc_01ABC")
	if other.ReportedCostUSD != nil {
		t.Fatalf("day without a cost row must keep nil cost: %+v", other)
	}
	ws, ok, _ := s.GetUsageRecord(context.Background(), model.SourceAnthropicAPI,
		"anthropic:2026-11-05T00:00:00Z:web_search::cost")
	if !ok || ws.ReportedCostUSD == nil || math.Abs(*ws.ReportedCostUSD-0.30) > 1e-9 || ws.TotalTokens() != 0 {
		t.Fatalf("web search cost-only record = %+v ok=%v", ws, ok)
	}

	// Syncing again with cost must not duplicate anything.
	res, err = run(t, newCollector(api, &sleeps{}, WithCost(true)), s, nil)
	if err != nil || res.Added != 0 {
		t.Fatalf("second sync = %+v %v", res, err)
	}
	if tot, _ := s.Totals(context.Background(), store.Filter{}); tot.RecordCount != 5 {
		t.Fatalf("RecordCount = %d, want 5", tot.RecordCount)
	}
}

func TestCostFailureKeepsUsageAndRetriesLater(t *testing.T) {
	usage := pagedUsage(t, "usage_page2.json")
	cost := fixture(t, "cost_page1.json")
	costOK := false
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == costPath {
			if !costOK {
				http.Error(w, "boom "+r.Header.Get("x-api-key"), http.StatusBadRequest)
				return
			}
			w.Write(cost)
			return
		}
		usage(w, r, n)
	})
	s := openStore(t)
	var logs bytes.Buffer
	c := newCollector(api, &sleeps{}, WithCost(true))
	res, err := run(t, c, s, &logs)
	if err != nil {
		t.Fatalf("cost failure must be non-fatal: %v", err)
	}
	if res.Added != 4 || len(res.Warnings) != 1 {
		t.Fatalf("result = %+v", res)
	}
	assertNoKey(t, res, nil, logs.String())

	// Usage was not held back by the failing cost report: the recovery sync
	// reads usage from the newest bucket, while the cost report (which never
	// succeeded) still starts from the default lookback.
	costOK = true
	res, err = run(t, c, s, nil)
	if err != nil || res.Added != 1 || res.Updated != 1 {
		t.Fatalf("recovery sync = %+v %v", res, err)
	}
	reqs := api.reqs()
	if got := reqs[len(reqs)-3].URL.Query().Get("starting_at"); got != "2026-11-06T00:00:00Z" {
		t.Errorf("usage restarted at %q, want the newest bucket", got)
	}
	if got := reqs[len(reqs)-1].URL.Query().Get("starting_at"); !strings.HasPrefix(got, "2026-08-0") {
		t.Errorf("cost started at %q, want the default lookback", got)
	}
}

func assertNoKey(t *testing.T, res collector.Result, err error, logs string) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), fakeKey) {
		t.Errorf("error leaks key: %v", err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, fakeKey) {
			t.Errorf("warning leaks key: %s", w)
		}
	}
	if strings.Contains(logs, fakeKey) {
		t.Errorf("log leaks key: %s", logs)
	}
}

func TestKeyNeverAppearsInErrorsWarningsOrLogs(t *testing.T) {
	cases := map[string]func(w http.ResponseWriter, r *http.Request, n int){
		"401 echoing the key": func(w http.ResponseWriter, r *http.Request, _ int) {
			http.Error(w, `{"error":"invalid x-api-key `+r.Header.Get("x-api-key")+`"}`, http.StatusUnauthorized)
		},
		"500 echoing the key": func(w http.ResponseWriter, r *http.Request, _ int) {
			http.Error(w, "oops "+r.Header.Get("x-api-key"), http.StatusInternalServerError)
		},
		"invalid json": func(w http.ResponseWriter, r *http.Request, _ int) {
			w.Write([]byte("not json " + r.Header.Get("x-api-key")))
		},
		"has_more without cursor": func(w http.ResponseWriter, r *http.Request, _ int) {
			w.Write([]byte(`{"data":[],"has_more":true,"next_page":null}`))
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			api := newFakeAPI(t, h)
			var logs bytes.Buffer
			s := openStore(t)
			res, err := run(t, newCollector(api, &sleeps{}, WithCost(true)), s, &logs)
			if err == nil {
				t.Fatal("expected an error")
			}
			assertNoKey(t, res, err, logs.String())
			if tot, _ := s.Totals(context.Background(), store.Filter{}); tot.RecordCount != 0 {
				t.Errorf("records stored on failure: %+v", tot)
			}
		})
	}

	// A panic-free path with a transport failure (closed server).
	api := newFakeAPI(t, func(http.ResponseWriter, *http.Request, int) {})
	api.Close()
	var logs bytes.Buffer
	res, err := run(t, newCollector(api, &sleeps{}), openStore(t), &logs)
	if err == nil {
		t.Fatal("expected transport error")
	}
	assertNoKey(t, res, err, logs.String())
}

func TestPartialPagesAreStoredButWatermarkNotAdvanced(t *testing.T) {
	p1 := fixture(t, "usage_page1.json")
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.URL.Query().Get("page") == "" {
			w.Write(p1)
			return
		}
		http.Error(w, "nope", http.StatusBadRequest)
	})
	s := openStore(t)
	res, err := run(t, newCollector(api, &sleeps{}), s, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if res.Added != 3 || res.Files != 1 {
		t.Fatalf("partial progress not reported: %+v", res)
	}
	var last int64
	if found, _ := s.GetSetting(context.Background(), SettingLastBucket, &last); found {
		t.Fatal("watermark advanced after a failed sync")
	}
}

func TestMissingKeyIsSkippedWithWarning(t *testing.T) {
	keyring.MockInit()
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		t.Error("API must not be called without a key")
	})
	c := New(WithBaseURL(api.URL)) // default keychain lookup
	res, err := run(t, c, openStore(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || res.Added != 0 {
		t.Fatalf("result = %+v", res)
	}

	// With a key in the (mock) keychain the default lookup is used.
	if err := secrets.Set(secrets.ProviderAnthropic, fakeKey); err != nil {
		t.Fatal(err)
	}
	api2 := newFakeAPI(t, pagedUsage(t, "usage_page2.json"))
	res, err = run(t, New(WithBaseURL(api2.URL)), openStore(t), nil)
	if err != nil || res.Added != 4 {
		t.Fatalf("keychain-backed sync = %+v %v", res, err)
	}
	if got := api2.reqs()[0].Header.Get("x-api-key"); got != fakeKey {
		t.Errorf("x-api-key = %q", got)
	}
}

func TestKeychainErrorIsReturned(t *testing.T) {
	c := New(WithKeyFunc(func() (string, error) { return "", secrets.ErrKeychainUnavailable }))
	_, err := run(t, c, openStore(t), nil)
	if !errors.Is(err, secrets.ErrKeychainUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestContextCancellation(t *testing.T) {
	api := newFakeAPI(t, pagedUsage(t, "usage_page2.json"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newCollector(api, &sleeps{}).Collect(ctx, collector.Deps{Store: openStore(t)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestNewClientValidation(t *testing.T) {
	for _, u := range []string{"http://api.example.com", "ftp://x", "://bad", "https://"} {
		if _, err := NewClient(u, fakeKey, nil); err == nil {
			t.Errorf("NewClient(%q) accepted", u)
		}
	}
	for _, u := range []string{"", "https://api.example.com", "http://127.0.0.1:8080", "http://localhost:1"} {
		if _, err := NewClient(u, fakeKey, nil); err != nil {
			t.Errorf("NewClient(%q) = %v", u, err)
		}
	}
	if _, err := NewClient("", " ", nil); err == nil {
		t.Error("empty key accepted")
	}
}

func TestUnsupportedBucketWidth(t *testing.T) {
	api := newFakeAPI(t, func(http.ResponseWriter, *http.Request, int) {})
	_, err := run(t, newCollector(api, &sleeps{}, WithBucketWidth("1w")), openStore(t), nil)
	if err == nil || !strings.Contains(err.Error(), "bucket width") {
		t.Fatalf("err = %v", err)
	}
}

func costHandler(t *testing.T, usage func(http.ResponseWriter, *http.Request, int), costOK *bool) func(http.ResponseWriter, *http.Request, int) {
	cost := fixture(t, "cost_page1.json")
	return func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == costPath {
			if !*costOK {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Write(cost)
			return
		}
		usage(w, r, n)
	}
}

func costOf(t *testing.T, s *store.Store, id string) *float64 {
	t.Helper()
	rec, ok, err := s.GetUsageRecord(context.Background(), model.SourceAnthropicAPI, id)
	if err != nil || !ok {
		t.Fatalf("record %s: ok=%v err=%v", id, ok, err)
	}
	return rec.ReportedCostUSD
}

const (
	opusDay2ID = "anthropic:2026-11-05T00:00:00Z:claude-opus-5-5:wrkspc_01ABC"
	searchID   = "anthropic:2026-11-05T00:00:00Z:web_search::cost"
)

func TestCostErrorKeepsStoredCostAndDoesNotFreezeUsage(t *testing.T) {
	costOK := true
	api := newFakeAPI(t, costHandler(t, pagedUsage(t, "usage_page2.json"), &costOK))
	s := openStore(t)
	c := newCollector(api, &sleeps{}, WithCost(true))

	if _, err := run(t, c, s, nil); err != nil {
		t.Fatal(err)
	}
	// A permanent cost error (HTTP 403) must neither erase the stored cost nor
	// keep usage from advancing.
	costOK = false
	for i := 0; i < 2; i++ {
		res, err := run(t, c, s, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Added != 0 || res.Updated != 0 || len(res.Warnings) != 1 {
			t.Fatalf("failing-cost sync %d = %+v", i, res)
		}
		if v := costOf(t, s, opusDay2ID); v == nil || math.Abs(*v-3.505) > 1e-9 {
			t.Fatalf("stored cost lost after cost failure: %v", v)
		}
		if v := costOf(t, s, searchID); v == nil {
			t.Fatal("cost-only record lost")
		}
	}
	reqs := api.reqs()
	usageStart, costStart := reqs[len(reqs)-3], reqs[len(reqs)-1]
	if usageStart.URL.Path != usagePath || costStart.URL.Path != costPath {
		t.Fatalf("unexpected request order: %s ... %s", usageStart.URL.Path, costStart.URL.Path)
	}
	if got := usageStart.URL.Query().Get("starting_at"); got != "2026-11-06T00:00:00Z" {
		t.Errorf("usage starting_at = %q: usage must keep advancing", got)
	}
	if got := costStart.URL.Query().Get("starting_at"); got != "2026-11-05T00:00:00Z" {
		t.Errorf("cost starting_at = %q: the cost position must not move on failure", got)
	}
}

func TestDisablingCostKeepsStoredCost(t *testing.T) {
	costOK := true
	api := newFakeAPI(t, costHandler(t, pagedUsage(t, "usage_page2.json"), &costOK))
	s := openStore(t)
	if _, err := run(t, newCollector(api, &sleeps{}, WithCost(true)), s, nil); err != nil {
		t.Fatal(err)
	}
	res, err := run(t, newCollector(api, &sleeps{}), s, nil)
	if err != nil || res.Updated != 0 {
		t.Fatalf("sync without cost = %+v %v", res, err)
	}
	if v := costOf(t, s, opusDay2ID); v == nil || math.Abs(*v-3.505) > 1e-9 {
		t.Fatalf("stored cost lost: %v", v)
	}
}

func TestCostForOlderDayAttachesToStoredRecord(t *testing.T) {
	costOK := true
	var onlyNewest bool
	full := pagedUsage(t, "usage_page2.json")
	newest := fixture(t, "usage_page2.json")
	api := newFakeAPI(t, costHandler(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if onlyNewest {
			w.Write(newest)
			return
		}
		full(w, r, n)
	}, &costOK))
	s := openStore(t)
	if res, err := run(t, newCollector(api, &sleeps{}), s, nil); err != nil || res.Added != 4 {
		t.Fatalf("first sync = %+v %v", res, err)
	}

	// Cost is switched on later: the cost window (default lookback) reaches
	// days the usage window (newest bucket) no longer covers.
	onlyNewest = true
	res, err := run(t, newCollector(api, &sleeps{}, WithCost(true)), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Updated != 1 {
		t.Fatalf("result = %+v, want Added 1 (web search) and Updated 1 (stored opus record)", res)
	}
	if v := costOf(t, s, opusDay2ID); v == nil || math.Abs(*v-3.505) > 1e-9 {
		t.Fatalf("cost not attached to the stored usage record: %v", v)
	}
	if tot, _ := s.Totals(context.Background(), store.Filter{}); tot.RecordCount != 5 || tot.InputTokens != 1000+300+700+40 {
		t.Fatalf("totals = %+v: tokens must be untouched, no duplicate rows", tot)
	}
}

func TestCostOnlyRecordIsNotCountedTwiceWhenUsageArrivesLater(t *testing.T) {
	costOK := true
	usageLate := true
	normal := pagedUsage(t, "usage_page2.json")
	api := newFakeAPI(t, costHandler(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if usageLate {
			w.Write([]byte(`{"data":[],"has_more":false,"next_page":null}`))
			return
		}
		normal(w, r, n)
	}, &costOK))
	s := openStore(t)
	c := newCollector(api, &sleeps{}, WithCost(true))

	// Cost is reported before any usage row exists: two cost-only records.
	res, err := run(t, c, s, nil)
	if err != nil || res.Added != 2 {
		t.Fatalf("first sync = %+v %v", res, err)
	}
	const total = 3.505 + 0.30
	if tot, _ := s.Totals(context.Background(), store.Filter{}); math.Abs(tot.ReportedCostUSD-total) > 1e-9 {
		t.Fatalf("cost after first sync = %v, want %v", tot.ReportedCostUSD, total)
	}

	// The usage row arrives: the cost moves onto it and the cost-only record
	// is reset, so the day's cost is still counted once.
	usageLate = false
	if _, err := run(t, c, s, nil); err != nil {
		t.Fatal(err)
	}
	tot, _ := s.Totals(context.Background(), store.Filter{})
	if math.Abs(tot.ReportedCostUSD-total) > 1e-9 {
		t.Fatalf("cost after usage arrived = %v, want %v (double counted?)", tot.ReportedCostUSD, total)
	}
	if v := costOf(t, s, opusDay2ID); v == nil || math.Abs(*v-3.505) > 1e-9 {
		t.Fatalf("usage record cost = %v", v)
	}
	if v := costOf(t, s, opusDay2ID+":cost"); v != nil {
		t.Fatalf("stale cost-only record still carries %v", *v)
	}

	// And it is stable from now on.
	if res, err := run(t, c, s, nil); err != nil || res.Added != 0 || res.Updated != 0 {
		t.Fatalf("steady-state sync = %+v %v", res, err)
	}
}

func TestCancelDuringCostWritesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	usage := pagedUsage(t, "usage_page2.json")
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == costPath {
			cancel()
			time.Sleep(50 * time.Millisecond)
			return
		}
		usage(w, r, n)
	})
	s := openStore(t)
	_, err := newCollector(api, &sleeps{}, WithCost(true)).Collect(ctx, collector.Deps{Store: s})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if tot, _ := s.Totals(context.Background(), store.Filter{}); tot.RecordCount != 0 {
		t.Fatalf("records written by a canceled run: %+v", tot)
	}
	var last int64
	if found, _ := s.GetSetting(context.Background(), SettingLastBucket, &last); found {
		t.Fatal("sync position moved on a canceled run")
	}
}

func newClientFor(t *testing.T, api *fakeAPI) *Client {
	t.Helper()
	c, err := NewClient(api.URL, fakeKey, httpx.New(httpx.Config{Redact: []string{fakeKey}}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRepeatedCursorStopsPagination(t *testing.T) {
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Write([]byte(`{"data":[],"has_more":true,"next_page":"same"}`))
	})
	_, pages, err := newClientFor(t, api).FetchUsage(context.Background(), testNow, Width1d, nil)
	if err == nil || !strings.Contains(err.Error(), "repeated next_page") {
		t.Fatalf("err = %v", err)
	}
	if pages != 2 || len(api.reqs()) != 2 {
		t.Fatalf("pages=%d requests=%d, want 2 and 2", pages, len(api.reqs()))
	}
	if got := api.reqs()[1].URL.Query().Get("page"); got != "same" {
		t.Errorf("cursor was not sent as page: %q", got)
	}
}

func TestMaxPagesStopsPagination(t *testing.T) {
	old := maxPages
	maxPages = 3
	t.Cleanup(func() { maxPages = old })

	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request, n int) {
		fmt.Fprintf(w, `{"data":[],"has_more":true,"next_page":"cursor-%d"}`, n)
	})
	c := newClientFor(t, api)
	_, pages, err := c.FetchUsage(context.Background(), testNow, Width1d, nil)
	if err == nil || !strings.Contains(err.Error(), "giving up") {
		t.Fatalf("usage err = %v", err)
	}
	if pages != 3 || len(api.reqs()) != 3 {
		t.Fatalf("pages=%d requests=%d, want 3 and 3", pages, len(api.reqs()))
	}
	if _, _, err := c.FetchCost(context.Background(), testNow, nil); err == nil || !strings.Contains(err.Error(), "giving up") {
		t.Fatalf("cost err = %v", err)
	}
}

func TestMalformedKeyIsRejectedWithoutEchoingIt(t *testing.T) {
	for _, k := range []string{"sk-ant-admin01-abc\n", "sk-ant admin", "sk-\tkey", "sk-ключ"} {
		_, err := NewClient("", k, nil)
		if err == nil {
			t.Errorf("key %q accepted", k)
			continue
		}
		if strings.Contains(err.Error(), "sk-") {
			t.Errorf("error echoes the key: %v", err)
		}
	}
}
