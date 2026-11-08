package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/collector"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

const testDevice = "device-test"

// setup copies the fixtures into a temp root (tests mutate them) and opens a
// fresh store. The real ~/.claude is never touched: Root is always explicit.
func setup(t *testing.T) (root string, c *Collector, deps collector.Deps) {
	t.Helper()
	root = t.TempDir()
	copyTree(t, "testdata", root)

	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
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

// expected is the reference aggregate, computed by a deliberately simple and
// independent reader: generic map decoding, one entry per message id.
type expected struct {
	in, out, cacheRead, c5m, c1h, thinking, requests int64
}

func num(m map[string]any, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func referenceTotals(t *testing.T, root string) map[string]expected {
	t.Helper()
	type entry struct {
		model string
		e     expected
	}
	byID := map[string]entry{}

	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var line map[string]any
			if json.Unmarshal(sc.Bytes(), &line) != nil || line["type"] != "assistant" {
				continue
			}
			msg, _ := line["message"].(map[string]any)
			usage, _ := msg["usage"].(map[string]any)
			if usage == nil || msg["model"] == "<synthetic>" {
				continue
			}
			id := str(msg, "id")
			if id == "" {
				id = str(line, "requestId")
			}
			if id == "" {
				id = str(line, "uuid")
			}
			e := expected{
				in:        num(usage, "input_tokens"),
				out:       num(usage, "output_tokens"),
				cacheRead: num(usage, "cache_read_input_tokens"),
				requests:  1,
			}
			if cc, ok := usage["cache_creation"].(map[string]any); ok &&
				num(cc, "ephemeral_5m_input_tokens")+num(cc, "ephemeral_1h_input_tokens") > 0 {
				e.c5m = num(cc, "ephemeral_5m_input_tokens")
				e.c1h = num(cc, "ephemeral_1h_input_tokens")
			} else {
				e.c5m = num(usage, "cache_creation_input_tokens")
			}
			if d, ok := usage["output_tokens_details"].(map[string]any); ok {
				e.thinking = num(d, "thinking_tokens")
			}
			byID[id] = entry{model: str(msg, "model"), e: e} // last line per id wins
		}
		return sc.Err()
	})
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]expected{}
	for _, en := range byID {
		a := out[en.model]
		a.in += en.e.in
		a.out += en.e.out
		a.cacheRead += en.e.cacheRead
		a.c5m += en.e.c5m
		a.c1h += en.e.c1h
		a.thinking += en.e.thinking
		a.requests += en.e.requests
		out[en.model] = a
	}
	return out
}

func storedTotals(t *testing.T, s *store.Store) map[string]expected {
	t.Helper()
	rows, err := s.Breakdown(context.Background(), store.Filter{}, store.DimModel)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]expected{}
	for _, r := range rows {
		out[r.Key] = expected{
			in: r.InputTokens, out: r.OutputTokens, cacheRead: r.CacheReadTokens,
			c5m: r.CacheWrite5mTokens, c1h: r.CacheWrite1hTokens,
			thinking: r.ThinkingTokens, requests: r.RequestCount,
		}
	}
	return out
}

func TestName(t *testing.T) {
	if got := New(Options{}).Name(); got != model.SourceClaudeCode {
		t.Fatalf("Name = %q", got)
	}
}

func TestGoldenTotals(t *testing.T) {
	root, c, deps := setup(t)
	res := collect(t, c, deps)

	want := referenceTotals(t, root)
	got := storedTotals(t, deps.Store)

	// Hand-computed values from the fixtures guard against the reference
	// reader and the parser sharing a misreading.
	hand := map[string]expected{
		"claude-sonnet-4-5": {in: 16, out: 65, cacheRead: 109, c5m: 30, c1h: 10, thinking: 20, requests: 3},
		"claude-opus-4-1":   {in: 4, out: 9, cacheRead: 3, c5m: 31, requests: 2},
		"claude-haiku-4-5":  {in: 25, out: 45, cacheRead: 2, c1h: 12, requests: 2},
	}
	if len(want) != len(hand) || len(got) != len(hand) {
		t.Fatalf("models: reference %v, stored %v, hand %v", want, got, hand)
	}
	for m, h := range hand {
		if want[m] != h {
			t.Errorf("%s: reference %+v != hand %+v", m, want[m], h)
		}
		if got[m] != want[m] {
			t.Errorf("%s: stored %+v != reference %+v", m, got[m], want[m])
		}
	}

	// 7 distinct messages; the cross-file duplicate (msg_A2) is not new.
	if res.Added != 7 {
		t.Errorf("Added = %d, want 7", res.Added)
	}
	if res.Updated != 0 {
		t.Errorf("Updated = %d, want 0", res.Updated)
	}
	if res.Files != 2 {
		t.Errorf("Files = %d, want 2", res.Files)
	}
}

func TestRecordFields(t *testing.T) {
	_, c, deps := setup(t)
	collect(t, c, deps)
	ctx := context.Background()

	r, found, err := deps.Store.GetUsageRecord(ctx, model.SourceClaudeCode, "msg_A1")
	if err != nil || !found {
		t.Fatalf("msg_A1: found=%v err=%v", found, err)
	}
	if r.Service != model.ServiceAnthropic || r.Project != "/work/alpha" || r.Session != "sess-a" ||
		r.DeviceID != testDevice || r.RequestCount != 1 || r.IsSidechain {
		t.Errorf("msg_A1 = %+v", r)
	}
	if r.OutputTokens != 50 || r.ThinkingTokens != 20 {
		t.Errorf("last block must win: %+v", r)
	}
	if !r.Timestamp.Equal(time.Date(2026, 3, 1, 10, 0, 1, 900_000_000, time.UTC)) {
		t.Errorf("timestamp = %v (last block expected)", r.Timestamp)
	}

	// No cache_creation breakdown: the total goes to 5m.
	r, _, _ = deps.Store.GetUsageRecord(ctx, model.SourceClaudeCode, "msg_A2")
	if r.CacheWrite5mTokens != 25 || r.CacheWrite1hTokens != 0 {
		t.Errorf("msg_A2 cache = %d/%d", r.CacheWrite5mTokens, r.CacheWrite1hTokens)
	}

	// Fallback ids: requestId, then uuid.
	if _, ok, _ := deps.Store.GetUsageRecord(ctx, model.SourceClaudeCode, "req_A3"); !ok {
		t.Error("requestId fallback missing")
	}
	if _, ok, _ := deps.Store.GetUsageRecord(ctx, model.SourceClaudeCode, "a-0007"); !ok {
		t.Error("uuid fallback missing")
	}

	// Sidechain flag and project from the nested file.
	r, _, _ = deps.Store.GetUsageRecord(ctx, model.SourceClaudeCode, "msg_B1")
	if !r.IsSidechain || r.Project != "/work/beta" {
		t.Errorf("msg_B1 = %+v", r)
	}

	// Synthetic and usage-less entries produce nothing.
	for _, id := range []string{"msg_S1", "msg_NOUSAGE", "msg_BROKEN"} {
		if _, ok, _ := deps.Store.GetUsageRecord(ctx, model.SourceClaudeCode, id); ok {
			t.Errorf("%s must not be stored", id)
		}
	}
}

func TestSecondScanIsNoop(t *testing.T) {
	_, c, deps := setup(t)
	collect(t, c, deps)
	before := storedTotals(t, deps.Store)

	res := collect(t, c, deps)
	if res.Added != 0 || res.Updated != 0 {
		t.Fatalf("second scan: Added=%d Updated=%d", res.Added, res.Updated)
	}
	if after := storedTotals(t, deps.Store); len(after) != len(before) {
		t.Fatalf("totals changed: %v -> %v", before, after)
	}

	states, err := deps.Store.ListIngestStates(context.Background(), model.SourceClaudeCode)
	if err != nil || len(states) != 2 {
		t.Fatalf("states = %d, err = %v", len(states), err)
	}
	for _, st := range states {
		info, err := os.Stat(st.Path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Offset != info.Size() {
			t.Errorf("%s: offset %d, size %d", st.Path, st.Offset, info.Size())
		}
	}
}

// Even without the saved offsets (e.g. the state table was cleared) a full
// re-read must leave the stored data unchanged.
func TestRescanWithoutStateIsIdempotent(t *testing.T) {
	_, c, deps := setup(t)
	collect(t, c, deps)
	before := storedTotals(t, deps.Store)

	states, _ := deps.Store.ListIngestStates(context.Background(), model.SourceClaudeCode)
	for _, st := range states {
		st.Offset, st.Size = 0, 0
		st.ModTime = time.Time{}
		// Offset 0 + size 0 means "shorter than the file": a full re-read.
		if err := deps.Store.SetIngestState(context.Background(), st); err != nil {
			t.Fatal(err)
		}
	}
	res := collect(t, c, deps)
	if res.Added != 0 || res.Updated != 0 {
		t.Fatalf("rescan: Added=%d Updated=%d", res.Added, res.Updated)
	}
	after := storedTotals(t, deps.Store)
	for m, v := range before {
		if after[m] != v {
			t.Errorf("%s: %+v -> %+v", m, v, after[m])
		}
	}
}

func assistantLine(id string, in, out int) string {
	return `{"type":"assistant","uuid":"x-` + id + `","timestamp":"2026-03-03T09:00:00.000Z","sessionId":"sess-a","cwd":"/work/alpha",` +
		`"message":{"id":"` + id + `","model":"claude-sonnet-4-5","usage":{"input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) + `}}}` + "\n"
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestAppendReadsOnlyNewLines(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)
	path := filepath.Join(root, "session-a.jsonl")

	appendTo(t, path, assistantLine("msg_N1", 1, 2)+assistantLine("msg_N2", 3, 4))
	res := collect(t, c, deps)
	if res.Added != 2 || res.Updated != 0 {
		t.Fatalf("Added=%d Updated=%d, want 2/0", res.Added, res.Updated)
	}
	// The fixture's corrupt line would warn again if the file were re-read
	// from the start instead of from the saved offset.
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings = %q: already-consumed lines were read again", res.Warnings)
	}

	// A later line for an existing message carries its final usage.
	appendTo(t, path, assistantLine("msg_N1", 1, 20))
	res = collect(t, c, deps)
	if res.Added != 0 || res.Updated != 1 {
		t.Fatalf("Added=%d Updated=%d, want 0/1", res.Added, res.Updated)
	}
	r, _, _ := deps.Store.GetUsageRecord(context.Background(), model.SourceClaudeCode, "msg_N1")
	if r.OutputTokens != 20 {
		t.Fatalf("OutputTokens = %d, want 20", r.OutputTokens)
	}
}

func TestPartialLastLineProcessedLater(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)
	path := filepath.Join(root, "session-a.jsonl")

	full := assistantLine("msg_P1", 5, 6)
	half := len(full) / 2
	appendTo(t, path, full[:half])
	res := collect(t, c, deps)
	if res.Added != 0 || len(res.Warnings) != 0 {
		t.Fatalf("partial line: Added=%d warnings=%q", res.Added, res.Warnings)
	}
	if _, ok, _ := deps.Store.GetUsageRecord(context.Background(), model.SourceClaudeCode, "msg_P1"); ok {
		t.Fatal("partial line must not be stored")
	}

	appendTo(t, path, full[half:])
	res = collect(t, c, deps)
	if res.Added != 1 || res.Updated != 0 || len(res.Warnings) != 0 {
		t.Fatalf("completed line: Added=%d Updated=%d warnings=%q", res.Added, res.Updated, res.Warnings)
	}
	r, ok, _ := deps.Store.GetUsageRecord(context.Background(), model.SourceClaudeCode, "msg_P1")
	if !ok || r.InputTokens != 5 || r.OutputTokens != 6 {
		t.Fatalf("msg_P1 = %+v ok=%v", r, ok)
	}
}

func TestTruncatedFileReadFromStart(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)
	path := filepath.Join(root, "session-a.jsonl")

	// Log rewritten to something shorter than what was read before.
	if err := os.WriteFile(path, []byte(assistantLine("msg_T1", 7, 8)), 0o600); err != nil {
		t.Fatal(err)
	}
	res := collect(t, c, deps)
	if res.Added != 1 || res.Updated != 0 {
		t.Fatalf("Added=%d Updated=%d, want 1/0", res.Added, res.Updated)
	}
	if _, ok, _ := deps.Store.GetUsageRecord(context.Background(), model.SourceClaudeCode, "msg_T1"); !ok {
		t.Fatal("msg_T1 missing")
	}
	// Earlier data is kept even though the log no longer has it.
	if _, ok, _ := deps.Store.GetUsageRecord(context.Background(), model.SourceClaudeCode, "msg_A1"); !ok {
		t.Fatal("msg_A1 must survive log truncation")
	}
}

func TestCorruptLineWarnsAndScanContinues(t *testing.T) {
	_, c, deps := setup(t)
	res := collect(t, c, deps)

	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly 1", res.Warnings)
	}
	w := res.Warnings[0]
	if !strings.Contains(w, "session-a.jsonl") || !strings.Contains(w, "invalid JSON") {
		t.Errorf("warning = %q", w)
	}
	// The lines after the corrupt one were still ingested.
	if _, ok, _ := deps.Store.GetUsageRecord(context.Background(), model.SourceClaudeCode, "msg_A4"); !ok {
		t.Error("record after the corrupt line is missing")
	}
	// The corrupt line is not retried: a second scan is silent.
	if res2 := collect(t, c, deps); len(res2.Warnings) != 0 {
		t.Errorf("second scan warnings = %q", res2.Warnings)
	}
}

func TestInvalidUsageLinesWarn(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)
	path := filepath.Join(root, "session-a.jsonl")

	appendTo(t, path,
		// negative counter
		`{"type":"assistant","uuid":"n1","timestamp":"2026-03-03T09:00:00Z","message":{"id":"msg_NEG","model":"m","usage":{"input_tokens":-1}}}`+"\n"+
			// bad timestamp
			`{"type":"assistant","uuid":"n2","timestamp":"yesterday","message":{"id":"msg_TS","model":"m","usage":{"input_tokens":1}}}`+"\n"+
			// no id of any kind
			`{"type":"assistant","timestamp":"2026-03-03T09:00:00Z","message":{"model":"m","usage":{"input_tokens":1}}}`+"\n"+
			// wrong type for a counter
			`{"type":"assistant","uuid":"n3","timestamp":"2026-03-03T09:00:00Z","message":{"id":"msg_STR","model":"m","usage":{"input_tokens":"many"}}}`+"\n"+
			assistantLine("msg_OK", 1, 1))
	res := collect(t, c, deps)
	if res.Added != 1 || len(res.Warnings) != 4 {
		t.Fatalf("Added=%d warnings=%q, want 1 and 4", res.Added, res.Warnings)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "many") {
			t.Errorf("warning leaks line content: %q", w)
		}
	}
}

func TestWarningsAreCapped(t *testing.T) {
	root, c, deps := setup(t)
	path := filepath.Join(root, "bad.jsonl")
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("not json\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	res := collect(t, c, deps)
	// 1 (session-a corrupt) + 20 kept + 1 summary for bad.jsonl.
	if len(res.Warnings) != 22 {
		t.Fatalf("warnings = %d, want 22: %q", len(res.Warnings), res.Warnings)
	}
	suppressed := 0
	for _, w := range res.Warnings {
		if strings.Contains(w, "bad.jsonl") && strings.Contains(w, "30 more warnings suppressed") {
			suppressed++
		}
	}
	if suppressed != 1 {
		t.Errorf("summary warning count = %d, warnings: %q", suppressed, res.Warnings)
	}
}

func TestMissingRootIsNotAnError(t *testing.T) {
	_, _, deps := setup(t)
	c := New(Options{Root: filepath.Join(t.TempDir(), "does-not-exist")})
	res, err := c.Collect(context.Background(), deps)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.Added != 0 || res.Files != 0 {
		t.Fatalf("res = %+v", res)
	}
}

func TestContextCanceled(t *testing.T) {
	_, c, deps := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Collect(ctx, deps); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestNilStore(t *testing.T) {
	if _, err := New(Options{}).Collect(context.Background(), collector.Deps{}); err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestRootResolution(t *testing.T) {
	home := filepath.Join("home", "u")
	env := map[string]string{}
	c := New(Options{
		Getenv:      func(k string) string { return env[k] },
		UserHomeDir: func() (string, error) { return home, nil },
	})

	got, err := c.DefaultRoot()
	if err != nil || got != filepath.Join(home, ".claude", "projects") {
		t.Fatalf("default = %q, %v", got, err)
	}

	env[EnvConfigDir] = filepath.Join("cfg", "claude")
	if got, _ = c.DefaultRoot(); got != filepath.Join("cfg", "claude", "projects") {
		t.Fatalf("env = %q", got)
	}

	withRoot := New(Options{Root: "explicit", Getenv: c.opts.Getenv, UserHomeDir: c.opts.UserHomeDir})
	if got, _ = withRoot.DefaultRoot(); got != "explicit" {
		t.Fatalf("opts root = %q", got)
	}

	failing := New(Options{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
	})
	if _, err := failing.DefaultRoot(); err == nil {
		t.Fatal("expected error when home cannot be resolved")
	}
}

func TestSettingOverridesRoot(t *testing.T) {
	_, c, deps := setup(t)

	// The configured Options.Root has data; the setting points elsewhere.
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "only.jsonl"), []byte(assistantLine("msg_ONLY", 1, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := deps.Store.SetSetting(context.Background(), SettingRootPath, other); err != nil {
		t.Fatal(err)
	}
	res := collect(t, c, deps)
	if res.Files != 1 || res.Added != 1 {
		t.Fatalf("Files=%d Added=%d, want 1/1", res.Files, res.Added)
	}
	if _, ok, _ := deps.Store.GetUsageRecord(context.Background(), model.SourceClaudeCode, "msg_A1"); ok {
		t.Fatal("setting must replace the default root")
	}

	// An empty setting falls back to the default root.
	if err := deps.Store.SetSetting(context.Background(), SettingRootPath, ""); err != nil {
		t.Fatal(err)
	}
	res = collect(t, c, deps)
	if res.Files != 2 {
		t.Fatalf("Files = %d, want 2 after clearing the setting", res.Files)
	}
}

func TestCollectorInterfaceViaRegistry(t *testing.T) {
	reg := collector.NewRegistry()
	if err := reg.Register(New(Options{Root: t.TempDir()})); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get(model.SourceClaudeCode); !ok {
		t.Fatal("not registered")
	}
}

// Unchanged files must not rewrite their ingest state on every scan.
func TestUnchangedFileStateNotRewritten(t *testing.T) {
	_, c, deps := setup(t)
	collect(t, c, deps)
	ctx := context.Background()
	before, _ := deps.Store.ListIngestStates(ctx, model.SourceClaudeCode)

	time.Sleep(5 * time.Millisecond) // UpdatedAt has millisecond resolution
	collect(t, c, deps)
	after, _ := deps.Store.ListIngestStates(ctx, model.SourceClaudeCode)
	if len(before) != 2 || len(after) != 2 {
		t.Fatalf("states: %d, %d", len(before), len(after))
	}
	for i := range before {
		if !before[i].UpdatedAt.Equal(after[i].UpdatedAt) {
			t.Errorf("%s: state rewritten for an unchanged file", before[i].Path)
		}
	}
}

// A message whose content-block lines straddle the batch size must still be
// written once, so the first scan reports no spurious updates.
func TestBatchBoundaryDoesNotSplitMessages(t *testing.T) {
	old := batchSize
	batchSize = 2
	t.Cleanup(func() { batchSize = old })

	root, c, deps := setup(t)
	var sb strings.Builder
	for _, id := range []string{"b1", "b2", "b3", "b4", "b5"} {
		for blk := 1; blk <= 3; blk++ {
			sb.WriteString(assistantLine("msg_"+id, 1, blk))
		}
	}
	if err := os.WriteFile(filepath.Join(root, "zz-batch.jsonl"), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	res := collect(t, c, deps)
	if res.Updated != 0 {
		t.Fatalf("Updated = %d, want 0", res.Updated)
	}
	if res.Added != 7+5 {
		t.Fatalf("Added = %d, want 12", res.Added)
	}
	r, _, _ := deps.Store.GetUsageRecord(context.Background(), model.SourceClaudeCode, "msg_b3")
	if r.OutputTokens != 3 {
		t.Fatalf("msg_b3 output = %d, want 3 (last block)", r.OutputTokens)
	}
}
