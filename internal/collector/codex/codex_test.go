package codex

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/muhammetsafak/TokenMetrics/internal/collector"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

const (
	testDevice = "device-test"
	sessionA   = "aaaaaaaa-0000-4000-8000-000000000001"
	sessionB   = "bbbbbbbb-0000-4000-8000-000000000002"
)

// setup copies the fixtures into a temp root (tests mutate them) and opens a
// fresh store. The real ~/.codex is never touched: Root is always explicit.
func setup(t *testing.T) (root string, c *Collector, deps collector.Deps) {
	t.Helper()
	root = t.TempDir()
	copyTree(t, filepath.Join("testdata", "sessions"), root)

	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return root, New(Options{Root: root}), collector.Deps{Store: s, DeviceID: testDevice}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := os.MkdirAll(to, 0o700); err != nil {
				t.Fatal(err)
			}
			copyTree(t, from, to)
			continue
		}
		b, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func rolloutPath(t *testing.T, root, session string) string {
	t.Helper()
	var found string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(d.Name(), session) {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no rollout for session %s under %s", session, root)
	}
	return found
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func collect(t *testing.T, c *Collector, deps collector.Deps) collector.Result {
	t.Helper()
	res, err := c.Collect(context.Background(), deps)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return res
}

func tokenCountLine(ts string, total, last [5]int64) string {
	usage := func(u [5]int64) map[string]int64 {
		return map[string]int64{
			"input_tokens": u[0], "cached_input_tokens": u[1], "output_tokens": u[2],
			"reasoning_output_tokens": u[3], "total_tokens": u[4],
		}
	}
	b, _ := json.Marshal(map[string]any{
		"timestamp": ts, "type": "event_msg",
		"payload": map[string]any{
			"type": "token_count",
			"info": map[string]any{"total_token_usage": usage(total), "last_token_usage": usage(last)},
		},
	})
	return string(b) + "\n"
}

// referenceCumulativeTotal is the independent reference: the final cumulative
// total_tokens of each rollout, which is what the sum of every response's
// usage must add up to. It never looks at last_token_usage, so a collector
// that summed the running totals (double counting) would not match it.
func referenceCumulativeTotal(t *testing.T, root string) int64 {
	t.Helper()
	var sum int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		var final int64
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var line struct {
				Payload struct {
					Type string `json:"type"`
					Info *struct {
						Total struct {
							TotalTokens int64 `json:"total_tokens"`
						} `json:"total_token_usage"`
					} `json:"info"`
				} `json:"payload"`
			}
			if json.Unmarshal(sc.Bytes(), &line) != nil || line.Payload.Type != "token_count" || line.Payload.Info == nil {
				continue
			}
			final = line.Payload.Info.Total.TotalTokens
		}
		sum += final
		return sc.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func totals(t *testing.T, s *store.Store, f store.Filter) store.Totals {
	t.Helper()
	tt, err := s.Totals(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

func TestName(t *testing.T) {
	if got := New(Options{}).Name(); got != model.SourceCodexCLI {
		t.Fatalf("Name = %q", got)
	}
}

func TestGoldenTotals(t *testing.T) {
	root, c, deps := setup(t)
	res := collect(t, c, deps)

	// A: 2 responses; B: 1 token_count-only response plus 2 usage records
	// whose sibling token_count events must not count again.
	if res.Added != 5 || res.Updated != 0 || res.Files != 2 {
		t.Fatalf("result = %+v, want Added 5 Updated 0 Files 2", res)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}

	all := totals(t, deps.Store, store.Filter{})
	if want := referenceCumulativeTotal(t, root); want != 4560 || all.TotalTokens() != want {
		t.Fatalf("total tokens = %d, reference = %d (want 4560)", all.TotalTokens(), want)
	}

	rows, err := deps.Store.Breakdown(context.Background(), store.Filter{}, store.DimModel)
	if err != nil {
		t.Fatal(err)
	}
	type want struct{ in, out, cacheRead, cw5m, thinking, req int64 }
	exp := map[string]want{
		"gpt-5-codex": {in: 400, out: 200, cacheRead: 600, thinking: 50, req: 1},
		"gpt-5.1":     {in: 800, out: 300, cacheRead: 1200, thinking: 70, req: 1},
		"gpt-5.2":     {in: 700, out: 160, cacheRead: 140, cw5m: 60, thinking: 5, req: 3},
	}
	if len(rows) != len(exp) {
		t.Fatalf("got %d models, want %d: %+v", len(rows), len(exp), rows)
	}
	for _, r := range rows {
		w, ok := exp[r.Key]
		got := want{r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWrite5mTokens, r.ThinkingTokens, r.RequestCount}
		if !ok || got != w || r.CacheWrite1hTokens != 0 {
			t.Errorf("model %q = %+v, want %+v", r.Key, got, w)
		}
	}

	ctx := context.Background()
	rec, found, err := deps.Store.GetUsageRecord(ctx, model.SourceCodexCLI, sessionA+":c:3500.3000.500")
	if err != nil || !found {
		t.Fatalf("second A record: found=%v err=%v", found, err)
	}
	if rec.Service != model.ServiceOpenAI || rec.Session != sessionA || rec.Project != "/work/alpha/sub" ||
		rec.DeviceID != testDevice || rec.IsSidechain || rec.Model != "gpt-5.1" ||
		!rec.Timestamp.Equal(time.Date(2026, 11, 1, 10, 5, 20, 0, time.UTC)) {
		t.Errorf("record = %+v", rec)
	}
	rec, found, err = deps.Store.GetUsageRecord(ctx, model.SourceCodexCLI, "r:resp_001")
	if err != nil || !found {
		t.Fatalf("usage record: found=%v err=%v", found, err)
	}
	if !rec.IsSidechain || rec.Session != sessionB || rec.InputTokens != 0 ||
		rec.CacheReadTokens != 40 || rec.CacheWrite5mTokens != 60 || rec.OutputTokens != 10 || rec.ThinkingTokens != 5 {
		t.Errorf("usage record = %+v", rec)
	}
}

func TestSecondScanIsNoop(t *testing.T) {
	_, c, deps := setup(t)
	collect(t, c, deps)
	before := totals(t, deps.Store, store.Filter{})

	res := collect(t, c, deps)
	if res.Added != 0 || res.Updated != 0 || len(res.Warnings) != 0 {
		t.Fatalf("second scan = %+v, want nothing new", res)
	}
	if after := totals(t, deps.Store, store.Filter{}); after != before {
		t.Fatalf("totals changed: %+v -> %+v", before, after)
	}
}

func TestRescanWithoutStateIsIdempotent(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)
	before := totals(t, deps.Store, store.Filter{})

	// Pretend the saved offsets were lost: everything is read again.
	for _, s := range []string{sessionA, sessionB} {
		err := deps.Store.SetIngestState(context.Background(), store.IngestState{
			Source: model.SourceCodexCLI, Path: rolloutPath(t, root, s),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	res := collect(t, c, deps)
	if res.Added != 0 || res.Updated != 0 {
		t.Fatalf("rescan = %+v, want no new or changed records", res)
	}
	if after := totals(t, deps.Store, store.Filter{}); after != before {
		t.Fatalf("totals changed: %+v -> %+v", before, after)
	}
}

func TestAppendUsesContextFromEarlierLines(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)

	a, b := rolloutPath(t, root, sessionA), rolloutPath(t, root, sessionB)
	// A: a new response; its model, project and session are only named by
	// lines the resumed read does not see again.
	appendTo(t, a, tokenCountLine("2026-11-01T10:10:00.000Z",
		[5]int64{4000, 2000, 700, 150, 4700}, [5]int64{1000, 200, 200, 30, 1200}))
	// B has usage records, so a stray token_count must be ignored even when
	// the records are in the part that is not read again.
	appendTo(t, b, tokenCountLine("2026-11-01T11:10:00.000Z",
		[5]int64{1000, 140, 170, 5, 1170}, [5]int64{100, 0, 10, 0, 110}))

	// A fresh collector has no cached context, so the lines before the
	// resumed offset are the only source of the model and project.
	res := collect(t, New(Options{Root: root}), deps)
	if res.Added != 1 || res.Updated != 0 || len(res.Warnings) != 0 {
		t.Fatalf("append scan = %+v, want exactly one new record", res)
	}
	rec, found, err := deps.Store.GetUsageRecord(context.Background(), model.SourceCodexCLI, sessionA+":c:4700.4000.700")
	if err != nil || !found {
		t.Fatalf("appended record: found=%v err=%v", found, err)
	}
	if rec.Model != "gpt-5.1" || rec.Project != "/work/alpha/sub" || rec.Session != sessionA ||
		rec.InputTokens != 800 || rec.CacheReadTokens != 200 || rec.OutputTokens != 200 {
		t.Errorf("appended record = %+v", rec)
	}
	if got := totals(t, deps.Store, store.Filter{}).TotalTokens(); got != 4560+1200 {
		t.Fatalf("total tokens = %d, want %d", got, 4560+1200)
	}
}

// TestIdleScanDoesNotEraseCachedContext: a scan that reads nothing (the file
// did not change) must not replace the cached session context with an empty
// one, or the next appended response is stored without model and project.
func TestIdleScanDoesNotEraseCachedContext(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)
	// Second scan: no file changed, no line is read.
	if res := collect(t, c, deps); res.Added != 0 || res.Updated != 0 {
		t.Fatalf("idle scan = %+v", res)
	}

	a := rolloutPath(t, root, sessionA)
	appendTo(t, a, tokenCountLine("2026-11-01T10:10:00.000Z",
		[5]int64{4000, 2000, 700, 150, 4700}, [5]int64{1000, 200, 200, 30, 1200}))
	// Same Collector: the cache from the first scan is what is used.
	if res := collect(t, c, deps); res.Added != 1 {
		t.Fatalf("append scan = %+v, want one new record", res)
	}
	rec, found, err := deps.Store.GetUsageRecord(context.Background(), model.SourceCodexCLI, sessionA+":c:4700.4000.700")
	if err != nil || !found {
		t.Fatalf("appended record: found=%v err=%v", found, err)
	}
	if rec.Model != "gpt-5.1" || rec.Project != "/work/alpha/sub" || rec.Session != sessionA {
		t.Errorf("appended record = %+v, want model gpt-5.1 and project /work/alpha/sub", rec)
	}
}

func TestPartialLastLineProcessedLater(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)
	a := rolloutPath(t, root, sessionA)

	line := tokenCountLine("2026-11-01T10:20:00.000Z",
		[5]int64{3100, 1800, 600, 120, 3700}, [5]int64{100, 0, 100, 0, 200})
	appendTo(t, a, line[:len(line)/2])
	if res := collect(t, c, deps); res.Added != 0 || len(res.Warnings) != 0 {
		t.Fatalf("partial line scan = %+v, want nothing", res)
	}
	appendTo(t, a, line[len(line)/2:])
	if res := collect(t, c, deps); res.Added != 1 || len(res.Warnings) != 0 {
		t.Fatalf("completed line scan = %+v, want one record", res)
	}
}

func TestCorruptLineWarnsAndScanContinues(t *testing.T) {
	root, c, deps := setup(t)
	a := rolloutPath(t, root, sessionA)
	appendTo(t, a, `{"timestamp":"2026-11-01T10:30:00.000Z","type":"event_msg","payload":{"type":"token_count" SECRET-PROMPT`+"\n")

	res := collect(t, c, deps)
	if res.Added != 5 {
		t.Fatalf("Added = %d, want 5 (the corrupt line must not stop the scan)", res.Added)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "syntax error") {
		t.Fatalf("warnings = %v, want one syntax error warning", res.Warnings)
	}
	if strings.Contains(res.Warnings[0], "SECRET-PROMPT") {
		t.Fatalf("warning leaks line content: %s", res.Warnings[0])
	}
}

func TestRolloutWithoutSessionMetaUsesFileName(t *testing.T) {
	root, c, deps := setup(t)
	id := "cccccccc-0000-4000-8000-000000000003"
	p := filepath.Join(root, "rollout-2026-11-02T09-00-00-"+id+".jsonl")
	if err := os.WriteFile(p, []byte(tokenCountLine("2026-11-02T09:00:10.000Z",
		[5]int64{50, 0, 5, 0, 55}, [5]int64{50, 0, 5, 0, 55})), 0o600); err != nil {
		t.Fatal(err)
	}
	collect(t, c, deps)
	rec, found, err := deps.Store.GetUsageRecord(context.Background(), model.SourceCodexCLI, id+":c:55.50.5")
	if err != nil || !found {
		t.Fatalf("record: found=%v err=%v", found, err)
	}
	if rec.Model != unknownModel || rec.Session != id || rec.InputTokens != 50 {
		t.Errorf("record = %+v", rec)
	}
}

func TestMalformedCountsAreClamped(t *testing.T) {
	root, c, deps := setup(t)
	id := "dddddddd-0000-4000-8000-000000000004"
	p := filepath.Join(root, "rollout-2026-11-02T10-00-00-"+id+".jsonl")
	// cached larger than input must not produce negative input tokens.
	if err := os.WriteFile(p, []byte(tokenCountLine("2026-11-02T10:00:10.000Z",
		[5]int64{10, 99, 5, 0, 15}, [5]int64{10, 99, 5, 0, 15})), 0o600); err != nil {
		t.Fatal(err)
	}
	res := collect(t, c, deps)
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings: %v", res.Warnings)
	}
	rec, found, err := deps.Store.GetUsageRecord(context.Background(), model.SourceCodexCLI, id+":c:15.10.5")
	if err != nil || !found {
		t.Fatalf("record: found=%v err=%v", found, err)
	}
	if rec.InputTokens != 0 || rec.CacheReadTokens != 10 {
		t.Errorf("record = %+v, want input 0 cache_read 10", rec)
	}
}

// zstdBytes compresses data the way Codex compresses a cold rollout.
func zstdBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll(data, nil)
}

// resolved returns p with symlinks resolved, which is how the collector names
// files (the temp dir is a symlink on macOS).
func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func writeZst(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, zstdBytes(t, data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCompressedRolloutIsRead(t *testing.T) {
	root, c, deps := setup(t)
	base := collect(t, c, deps)
	id := "eeeeeeee-0000-4000-8000-000000000005"
	p := filepath.Join(root, "rollout-2026-10-01T00-00-00-"+id+".jsonl.zst")
	writeZst(t, p, []byte(tokenCountLine("2026-10-01T00:00:10.000Z",
		[5]int64{100, 20, 10, 0, 110}, [5]int64{100, 20, 10, 0, 110})))

	res := collect(t, c, deps)
	if res.Added != 1 || res.Files != base.Files+1 || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v, want one added record and no warnings", res)
	}
	rec, found, err := deps.Store.GetUsageRecord(context.Background(), model.SourceCodexCLI, id+":c:110.100.10")
	if err != nil || !found {
		t.Fatalf("record: found=%v err=%v", found, err)
	}
	// The session falls back to the thread id in the compressed file name.
	if rec.Session != id || rec.InputTokens != 80 || rec.CacheReadTokens != 20 || rec.OutputTokens != 10 {
		t.Errorf("record = %+v", rec)
	}

	// Unchanged compressed file: no re-read, no write.
	p = resolved(t, p)
	st1, _, _ := deps.Store.GetIngestState(context.Background(), model.SourceCodexCLI, p)
	res = collect(t, c, deps)
	if res.Added != 0 || res.Updated != 0 || len(res.Warnings) != 0 {
		t.Fatalf("second scan = %+v", res)
	}
	st2, found, _ := deps.Store.GetIngestState(context.Background(), model.SourceCodexCLI, p)
	if !found || st1 != st2 {
		t.Errorf("ingest state moved: %+v -> %+v", st1, st2)
	}
	if st2.Size == 0 || st2.Offset == 0 {
		t.Errorf("ingest state = %+v, want compressed size and decompressed offset", st2)
	}
}

func TestChangedCompressedRolloutIsReadFromTheStart(t *testing.T) {
	root, c, deps := setup(t)
	id := "eeeeeeee-0000-4000-8000-000000000005"
	p := filepath.Join(root, "rollout-2026-10-01T00-00-00-"+id+".jsonl.zst")
	l1 := tokenCountLine("2026-10-01T00:00:10.000Z", [5]int64{100, 0, 10, 0, 110}, [5]int64{100, 0, 10, 0, 110})
	l2 := tokenCountLine("2026-10-01T00:00:20.000Z", [5]int64{250, 0, 30, 0, 280}, [5]int64{150, 0, 20, 0, 170})
	writeZst(t, p, []byte(l1))
	collect(t, c, deps)

	writeZst(t, p, []byte(l1+l2))
	res := collect(t, c, deps)
	if res.Added != 1 || res.Updated != 0 {
		t.Fatalf("after rewrite = %+v, want only the new record", res)
	}
}

// TestPlainRolloutCompressedLaterIsNotCounted is the Codex lifecycle: a
// rollout is ingested as .jsonl, later replaced by the same content as
// .jsonl.zst. The identity keys do not depend on the file name, so the
// records must not be added a second time.
func TestPlainRolloutCompressedLaterIsNotCounted(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)
	before := totals(t, deps.Store, store.Filter{})

	for _, sess := range []string{sessionA, sessionB} {
		plain := rolloutPath(t, root, sess)
		data, err := os.ReadFile(plain)
		if err != nil {
			t.Fatal(err)
		}
		writeZst(t, plain+".zst", data)
		if err := os.Remove(plain); err != nil {
			t.Fatal(err)
		}
	}
	res := collect(t, c, deps)
	if res.Added != 0 || res.Updated != 0 || len(res.Warnings) != 0 {
		t.Fatalf("scan after compression = %+v, want nothing new", res)
	}
	if res.Files != 2 {
		t.Errorf("files = %d, want the 2 compressed rollouts", res.Files)
	}
	if after := totals(t, deps.Store, store.Filter{}); after != before {
		t.Fatalf("totals changed: %+v -> %+v", before, after)
	}
}

func TestCompressedFixtureAloneMatchesPlain(t *testing.T) {
	root, c, deps := setup(t)
	for _, sess := range []string{sessionA, sessionB} {
		plain := rolloutPath(t, root, sess)
		data, err := os.ReadFile(plain)
		if err != nil {
			t.Fatal(err)
		}
		writeZst(t, plain+".zst", data)
		if err := os.Remove(plain); err != nil {
			t.Fatal(err)
		}
	}
	res := collect(t, c, deps)
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings: %v", res.Warnings)
	}

	root2, c2, deps2 := setup(t)
	_ = root2
	collect(t, c2, deps2)
	if got, want := totals(t, deps.Store, store.Filter{}), totals(t, deps2.Store, store.Filter{}); got != want {
		t.Fatalf("compressed totals %+v, plain totals %+v", got, want)
	}
}

func TestCorruptCompressedRolloutWarnsAndScanContinues(t *testing.T) {
	root, c, deps := setup(t)
	bad := filepath.Join(root, "rollout-2026-10-01T00-00-00-eeeeeeee-0000-4000-8000-000000000005.jsonl.zst")
	if err := os.WriteFile(bad, []byte("this is not a zstd stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A truncated stream: valid header, missing tail.
	good := zstdBytes(t, []byte(strings.Repeat(tokenCountLine("2026-10-01T00:00:10.000Z",
		[5]int64{100, 0, 10, 0, 110}, [5]int64{100, 0, 10, 0, 110}), 50)))
	cut := filepath.Join(root, "rollout-2026-10-01T00-00-01-ffffffff-0000-4000-8000-000000000006.jsonl.zst")
	if err := os.WriteFile(cut, good[:len(good)-6], 0o600); err != nil {
		t.Fatal(err)
	}

	res := collect(t, c, deps)
	if res.Added != 5 {
		t.Fatalf("added = %d, want the 5 records of the readable fixtures", res.Added)
	}
	if len(res.Warnings) < 2 {
		t.Fatalf("warnings = %v, want one per damaged file", res.Warnings)
	}
	for _, p := range []string{resolved(t, bad), resolved(t, cut)} {
		n := 0
		for _, w := range res.Warnings {
			if strings.Contains(w, p) {
				n++
			}
			if strings.Contains(w, "not supported") {
				t.Errorf("stale warning: %s", w)
			}
		}
		if n != 1 {
			t.Errorf("%d warnings mention %s, want 1: %v", n, p, res.Warnings)
		}
		// No state is saved, so the file is tried again next time.
		if _, found, _ := deps.Store.GetIngestState(context.Background(), model.SourceCodexCLI, p); found {
			t.Errorf("ingest state saved for damaged file %s", p)
		}
	}
}

// TestDamagedCompressedRolloutIsNotRetriedUntilItChanges: a compressed rollout
// is immutable, so a decode failure repeats until the file changes. It is
// reported once, not decoded again on every scan, and read once it is replaced.
func TestDamagedCompressedRolloutIsNotRetriedUntilItChanges(t *testing.T) {
	root, c, deps := setup(t)
	id := "eeeeeeee-0000-4000-8000-000000000005"
	p := filepath.Join(root, "rollout-2026-10-01T00-00-00-"+id+".jsonl.zst")
	if err := os.WriteFile(p, []byte("this is not a zstd stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := collect(t, c, deps); len(res.Warnings) != 1 {
		t.Fatalf("first scan warnings = %v, want 1", res.Warnings)
	}
	if res := collect(t, c, deps); len(res.Warnings) != 0 {
		t.Fatalf("second scan warnings = %v, want none for the known-bad file", res.Warnings)
	}

	line := tokenCountLine("2026-10-01T00:00:10.000Z", [5]int64{100, 0, 10, 0, 110}, [5]int64{100, 0, 10, 0, 110})
	writeZst(t, p, []byte(line))
	res := collect(t, c, deps)
	if res.Added != 1 || len(res.Warnings) != 0 {
		t.Fatalf("scan after repair = %+v, want the record read and no warning", res)
	}

	// Gone from disk: the marker goes with it.
	if err := os.WriteFile(p, []byte("bad again"), 0o600); err != nil {
		t.Fatal(err)
	}
	collect(t, c, deps)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	collect(t, c, deps)
	if len(c.failed) != 0 {
		t.Errorf("failed markers = %v, want none after the file is gone", c.failed)
	}
}

func TestMissingRootIsNotAnError(t *testing.T) {
	_, _, deps := setup(t)
	c := New(Options{Root: filepath.Join(t.TempDir(), "does-not-exist")})
	res := collect(t, c, deps)
	if res.Added != 0 || res.Files != 0 || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v, want empty", res)
	}
}

func TestRootResolution(t *testing.T) {
	home := func() (string, error) { return filepath.Join("h", "me"), nil }
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	got, err := New(Options{Getenv: env(nil), UserHomeDir: home}).DefaultRoot()
	if err != nil || got != filepath.Join("h", "me", ".codex", "sessions") {
		t.Errorf("home default = %q, %v", got, err)
	}
	got, _ = New(Options{Getenv: env(map[string]string{EnvCodexHome: "ch"}), UserHomeDir: home}).DefaultRoot()
	if got != filepath.Join("ch", "sessions") {
		t.Errorf("CODEX_HOME = %q", got)
	}
	got, _ = New(Options{Root: "explicit", Getenv: env(map[string]string{EnvCodexHome: "ch"}), UserHomeDir: home}).DefaultRoot()
	if got != "explicit" {
		t.Errorf("Options.Root = %q", got)
	}

	// The settings key beats everything and is what Collect scans.
	root, _, deps := setup(t)
	if err := deps.Store.SetSetting(context.Background(), SettingRootPath, root); err != nil {
		t.Fatal(err)
	}
	c := New(Options{Root: filepath.Join(t.TempDir(), "elsewhere")})
	if res := collect(t, c, deps); res.Added != 5 {
		t.Fatalf("settings override ignored: %+v", res)
	}
}

func TestCachedContextSparesPrefixRead(t *testing.T) {
	root, c, deps := setup(t)
	primes := 0
	orig := primeFn
	primeFn = func(ctx context.Context, path string, limit int64, sc *sessionContext) {
		primes++
		orig(ctx, path, limit, sc)
	}
	t.Cleanup(func() { primeFn = orig })

	collect(t, c, deps)
	a := rolloutPath(t, root, sessionA)

	appendTo(t, a, tokenCountLine("2026-11-01T10:10:00.000Z",
		[5]int64{4000, 2000, 700, 150, 4700}, [5]int64{1000, 200, 200, 30, 1200}))
	if res := collect(t, c, deps); res.Added != 1 {
		t.Fatalf("scan = %+v, want one new record", res)
	}
	if primes != 0 {
		t.Fatalf("prefix was read %d time(s); the cached context should have been used", primes)
	}
	rec, found, err := deps.Store.GetUsageRecord(context.Background(), model.SourceCodexCLI, sessionA+":c:4700.4000.700")
	if err != nil || !found || rec.Model != "gpt-5.1" || rec.Project != "/work/alpha/sub" {
		t.Fatalf("cached-context record = %+v found=%v err=%v", rec, found, err)
	}

	// A new process (empty cache) falls back to reading the prefix.
	appendTo(t, a, tokenCountLine("2026-11-01T10:11:00.000Z",
		[5]int64{5000, 2400, 800, 170, 5800}, [5]int64{1000, 400, 100, 20, 1100}))
	if res := collect(t, New(Options{Root: root}), deps); res.Added != 1 {
		t.Fatalf("scan = %+v, want one new record", res)
	}
	if primes != 1 {
		t.Fatalf("prefix reads = %d, want 1", primes)
	}
	rec, found, err = deps.Store.GetUsageRecord(context.Background(), model.SourceCodexCLI, sessionA+":c:5800.5000.800")
	if err != nil || !found || rec.Model != "gpt-5.1" || rec.Project != "/work/alpha/sub" {
		t.Fatalf("primed-context record = %+v found=%v err=%v", rec, found, err)
	}
}

// writeFork writes a sub-agent rollout the way Codex does: the parent's
// token_usage_record lines are copied and re-stamped with the time of the
// copy, followed by the child's own response.
func writeFork(t *testing.T, root, parent string) (childPath string) {
	t.Helper()
	const childID = "cccccccc-0000-4000-8000-000000000003"
	b, err := os.ReadFile(parent)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"timestamp":"2026-11-03T09:00:00.000Z","type":"session_meta","payload":{"id":"` + childID + `","forked_from_id":"` + sessionB + `","cwd":"/work/child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"` + sessionB + `","depth":1}}}}}`,
		`{"timestamp":"2026-11-03T09:00:00.050Z","type":"turn_context","payload":{"cwd":"/work/child","model":"gpt-5.9"}}`,
	}
	for i, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.Contains(l, "token_usage_record") {
			continue
		}
		var env map[string]json.RawMessage
		if err := json.Unmarshal([]byte(l), &env); err != nil {
			t.Fatal(err)
		}
		env["timestamp"] = json.RawMessage(fmt.Sprintf("\"2026-11-03T09:00:00.%03dZ\"", 100+i))
		out, _ := json.Marshal(env)
		lines = append(lines, string(out))
	}
	lines = append(lines, `{"timestamp":"2026-11-03T09:05:00.000Z","type":"token_usage_record","payload":{"response_id":"resp_003","usage":{"input_tokens":70,"cached_input_tokens":0,"output_tokens":30,"reasoning_output_tokens":0,"total_tokens":100}}}`)
	childPath = filepath.Join(root, "rollout-2026-11-03T09-00-00-"+childID+".jsonl")
	if err := os.WriteFile(childPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return childPath
}

func TestForkedRolloutKeepsOriginalResponses(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)
	before := totals(t, deps.Store, store.Filter{})

	writeFork(t, root, rolloutPath(t, root, sessionB))
	res := collect(t, c, deps)
	if res.Added != 1 || res.Updated != 0 || len(res.Warnings) != 0 {
		t.Fatalf("fork scan = %+v, want only the child's own response", res)
	}
	if got := totals(t, deps.Store, store.Filter{}).TotalTokens(); got != before.TotalTokens()+100 {
		t.Fatalf("total tokens = %d, want %d", got, before.TotalTokens()+100)
	}
	for _, id := range []string{"r:resp_001", "r:resp_002"} {
		rec, found, err := deps.Store.GetUsageRecord(context.Background(), model.SourceCodexCLI, id)
		if err != nil || !found {
			t.Fatalf("%s: found=%v err=%v", id, found, err)
		}
		if rec.Session != sessionB || rec.Project != "/work/beta" || rec.Model != "gpt-5.2" ||
			rec.Timestamp.Day() != 1 || rec.Timestamp.Month() != time.November {
			t.Errorf("%s moved by the fork copy: %+v", id, rec)
		}
	}
	rec, found, err := deps.Store.GetUsageRecord(context.Background(), model.SourceCodexCLI, "r:resp_003")
	if err != nil || !found || rec.Session != "cccccccc-0000-4000-8000-000000000003" || rec.Model != "gpt-5.9" || rec.Project != "/work/child" {
		t.Fatalf("child's own record = %+v found=%v err=%v", rec, found, err)
	}

	// Scanning again, and re-reading the fork from the start, changes nothing.
	if res := collect(t, c, deps); res.Added != 0 || res.Updated != 0 {
		t.Fatalf("second scan = %+v", res)
	}
}

func TestForkScannedBeforeParentIsCorrectedByTheOriginal(t *testing.T) {
	// Only the fork exists at first, e.g. the parent was not scanned yet.
	src, _, _ := setup(t)
	root := t.TempDir()
	writeFork(t, root, rolloutPath(t, src, sessionB))
	_, c, deps := setup(t)
	c = New(Options{Root: root})
	if res := collect(t, c, deps); res.Added != 3 {
		t.Fatalf("fork scan = %+v, want 3 records", res)
	}

	// The parent shows up: its earlier timestamps replace the copies.
	b, err := os.ReadFile(rolloutPath(t, src, sessionB))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.Base(rolloutPath(t, src, sessionB))), b, 0o600); err != nil {
		t.Fatal(err)
	}
	res := collect(t, c, deps)
	if res.Updated != 2 {
		t.Fatalf("parent scan = %+v, want the two copied responses corrected", res)
	}
	for _, id := range []string{"r:resp_001", "r:resp_002"} {
		rec, found, err := deps.Store.GetUsageRecord(context.Background(), model.SourceCodexCLI, id)
		if err != nil || !found || rec.Session != sessionB || rec.Project != "/work/beta" || rec.Timestamp.Day() != 1 {
			t.Errorf("%s = %+v found=%v err=%v, want the original", id, rec, found, err)
		}
	}
}

func TestContextCacheForgetsDeletedRollouts(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)
	if len(c.cache) != 2 {
		t.Fatalf("cache entries = %d, want 2", len(c.cache))
	}
	if err := os.Remove(rolloutPath(t, root, sessionB)); err != nil {
		t.Fatal(err)
	}
	collect(t, c, deps)
	if len(c.cache) != 1 {
		t.Fatalf("cache entries after delete = %d, want 1", len(c.cache))
	}
}

// setupTotalOnly is setup for the fixture that mimics the logs of newer Codex
// versions: no turn_context lines, the model in session_meta provenance or in
// thread_settings_applied, and last_token_usage holding only total_tokens.
func setupTotalOnly(t *testing.T) (root string, c *Collector, deps collector.Deps) {
	t.Helper()
	root = t.TempDir()
	copyTree(t, filepath.Join("testdata", "total_only", "sessions"), root)
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return root, New(Options{Root: root}), collector.Deps{Store: s, DeviceID: testDevice}
}

func TestTotalOnlyEventsAreStoredWithoutSplit(t *testing.T) {
	_, c, deps := setupTotalOnly(t)
	res := collect(t, c, deps)

	// c: 2 distinct responses (the repeated and the all-zero event add none);
	// d: 1; e: 1 split + 1 total-only.
	if res.Added != 5 || res.Updated != 0 || res.Files != 3 || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v, want Added 5 Updated 0 Files 3 and no warnings", res)
	}

	all := totals(t, deps.Store, store.Filter{})
	if all.TotalTokens() != 5100 || all.TotalOnlyTokens != 4600 || all.TotalOnlyRecords != 4 ||
		all.InputTokens != 400 || all.OutputTokens != 100 || all.RecordCount != 5 {
		t.Fatalf("totals = %+v, want total 5100, total-only 4600 in 4 records, input 400, output 100", all)
	}

	rows, err := deps.Store.Breakdown(context.Background(), store.Filter{}, store.DimModel)
	if err != nil {
		t.Fatal(err)
	}
	type want struct{ total, totalOnly, records int64 }
	exp := map[string]want{
		"gpt-test-meta":     {3500, 3500, 2}, // model from session_meta provenance
		"gpt-test-settings": {700, 700, 1},   // thread settings beat provenance
		"gpt-test-turn":     {900, 400, 1},   // turn_context beats both
	}
	if len(rows) != len(exp) {
		t.Fatalf("got %d models, want %d: %+v", len(rows), len(exp), rows)
	}
	for _, r := range rows {
		w, ok := exp[r.Key]
		if !ok || r.TotalTokens() != w.total || r.TotalOnlyTokens != w.totalOnly || r.TotalOnlyRecords != w.records {
			t.Errorf("model %q = total %d, total-only %d in %d records; want %+v",
				r.Key, r.TotalTokens(), r.TotalOnlyTokens, r.TotalOnlyRecords, w)
		}
	}

	rec, found, err := deps.Store.GetUsageRecord(context.Background(), model.SourceCodexCLI, "cccccccc-0000-4000-8000-000000000003:c:3500.0.0")
	if err != nil || !found {
		t.Fatalf("total-only record: found=%v err=%v", found, err)
	}
	if !rec.TotalOnly || rec.TotalOnlyTokens != 2500 || rec.InputTokens != 0 || rec.OutputTokens != 0 ||
		rec.CacheReadTokens != 0 || rec.RequestCount != 1 || rec.Model != "gpt-test-meta" || rec.ReportedCostUSD != nil {
		t.Errorf("record = %+v", rec)
	}
}

func TestTotalOnlySecondScanIsNoop(t *testing.T) {
	root, c, deps := setupTotalOnly(t)
	collect(t, c, deps)
	before := totals(t, deps.Store, store.Filter{})
	if res := collect(t, c, deps); res.Added != 0 || res.Updated != 0 {
		t.Fatalf("second scan = %+v, want nothing added or updated", res)
	}
	// Forget the incremental state: a full re-read must not double count.
	for _, sess := range []string{"cccccccc-0000-4000-8000-000000000003", "dddddddd-0000-4000-8000-000000000004", "eeeeeeee-0000-4000-8000-000000000005"} {
		err := deps.Store.SetIngestState(context.Background(), store.IngestState{
			Source: model.SourceCodexCLI, Path: rolloutPath(t, root, sess),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if res := collect(t, c, deps); res.Added != 0 || res.Updated != 0 {
		t.Fatalf("rescan without state = %+v, want nothing added or updated", res)
	}
	if after := totals(t, deps.Store, store.Filter{}); after != before {
		t.Fatalf("totals changed: %+v -> %+v", before, after)
	}
}

func TestModelFallbackChain(t *testing.T) {
	meta := func(model string) string {
		bi := ""
		if model != "" {
			bi = `,"base_instructions":{"provenance":{"model":"` + model + `"}}`
		}
		return `{"timestamp":"2026-11-02T09:00:00.000Z","type":"session_meta","payload":{"id":"s1","cwd":"/w"` + bi + `}}`
	}
	settings := `{"timestamp":"2026-11-02T09:00:01.000Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"from-settings"}}}`
	turn := `{"timestamp":"2026-11-02T09:00:02.000Z","type":"turn_context","payload":{"model":"from-turn"}}`
	usage := `{"timestamp":"2026-11-02T09:00:03.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":10},"last_token_usage":{"total_tokens":10}}}}`

	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"turn_context first", []string{meta("from-meta"), settings, turn, usage}, "from-turn"},
		{"settings over provenance", []string{meta("from-meta"), settings, usage}, "from-settings"},
		{"provenance only", []string{meta("from-meta"), usage}, "from-meta"},
		{"nothing names a model", []string{meta(""), usage}, "unknown"},
		{"base_instructions is a plain string", []string{
			`{"timestamp":"2026-11-02T09:00:00.000Z","type":"session_meta","payload":{"id":"s1","base_instructions":"text only"}}`, usage}, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &sessionContext{}
			var got []model.UsageRecord
			for _, l := range tc.lines {
				rec, ok, err := parseLine([]byte(l), ctx, testDevice)
				if err != nil {
					t.Fatal(err)
				}
				if ok {
					got = append(got, rec)
				}
			}
			if len(got) != 1 || got[0].Model != tc.want || !got[0].TotalOnly || got[0].TotalOnlyTokens != 10 {
				t.Fatalf("records = %+v, want one total-only record of model %q", got, tc.want)
			}
		})
	}
}

func TestAllZeroUsageIsStillSkipped(t *testing.T) {
	ctx := &sessionContext{sessionID: "s1"}
	line := `{"timestamp":"2026-11-02T09:00:03.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":0},"last_token_usage":{"total_tokens":0}}}}`
	if _, ok, err := parseLine([]byte(line), ctx, testDevice); ok || err != nil {
		t.Fatalf("all-zero usage: ok=%v err=%v, want skipped without error", ok, err)
	}
}

// TestUpgradeFromSchema1RereadsScannedRollouts is the upgrade path of an
// installation that scanned Codex logs with a build that skipped total-only
// entries: the database is at schema 1, the rollouts are recorded as read to
// their end, and the stored rows are missing. After the new build migrates the
// database, a scan must bring the entries in.
func TestUpgradeFromSchema1RereadsScannedRollouts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	copyTree(t, filepath.Join("testdata", "total_only", "sessions"), root)
	dbPath := filepath.Join(t.TempDir(), "t.db")

	// A split row the old build did store, under the model it could find.
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	deps := collector.Deps{Store: s, DeviceID: testDevice}
	c := New(Options{Root: root})
	if res := collect(t, c, deps); res.Added != 5 {
		t.Fatalf("setup scan = %+v, want Added 5", res)
	}
	want := totals(t, s, store.Filter{})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Rewind the database to what the old build left: schema 1 (no total-only
	// columns), no total-only rows, every rollout recorded as read to its end.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DELETE FROM usage_records WHERE total_only = 1`,
		`ALTER TABLE usage_records DROP COLUMN total_only_tokens`,
		`ALTER TABLE usage_records DROP COLUMN total_only`,
		`DELETE FROM schema_version WHERE version = 2`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var recorded, atEnd int
	if err := raw.QueryRow(`SELECT COUNT(*), COALESCE(SUM("offset" > 0 AND "offset" = size), 0) FROM ingest_state WHERE source = 'codex_cli'`).Scan(&recorded, &atEnd); err != nil {
		t.Fatal(err)
	}
	if recorded != 3 || atEnd != 3 {
		t.Fatalf("precondition: %d codex files recorded, %d at their end; want 3 and 3", recorded, atEnd)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// The new build opens the database (migration 2) and scans.
	s, err = store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	deps = collector.Deps{Store: s, DeviceID: testDevice}
	res := collect(t, c, deps)
	if res.Added != 4 || res.Updated != 0 {
		t.Fatalf("scan after upgrade = %+v, want the 4 missing total-only records added", res)
	}
	if got := totals(t, s, store.Filter{}); got != want {
		t.Fatalf("totals after upgrade = %+v, want %+v", got, want)
	}
	if res := collect(t, c, deps); res.Added != 0 || res.Updated != 0 {
		t.Fatalf("second scan = %+v, want nothing", res)
	}
}
