package gemini

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
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
// fresh store. The real ~/.gemini is never touched: Root is always explicit.
func setup(t *testing.T) (root string, c *Collector, deps collector.Deps) {
	t.Helper()
	root = t.TempDir()
	copyTree(t, filepath.Join("testdata", "tmp"), root)

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

func mainSession(root string) string {
	return filepath.Join(root, "proj-a", "chats", "session-2026-11-01T10-00-sessa001.jsonl")
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

func totals(t *testing.T, s *store.Store) store.Totals {
	t.Helper()
	tt, err := s.Totals(context.Background(), store.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

func geminiLine(id, ts string, tok *tokens, mdl string) string {
	b, _ := json.Marshal(map[string]any{
		"id": id, "timestamp": ts, "type": "gemini", "content": "...", "tokens": tok, "model": mdl,
	})
	return string(b) + "\n"
}

// referenceTotal is the independent reference: the sum of the reported
// `total` of every model message that carries tokens, one per message id
// (last occurrence wins), read with generic map decoding. A collector that
// counted a re-appended message twice, or lost the tool or thought tokens,
// would not match it.
func referenceTotal(t *testing.T, root string) (sum int64, messages int) {
	t.Helper()
	last := map[string]int64{}
	take := func(session string, m map[string]any) {
		if m["type"] != "gemini" {
			return
		}
		tok, _ := m["tokens"].(map[string]any)
		if tok == nil {
			return
		}
		id, _ := m["id"].(string)
		total, _ := tok["total"].(float64)
		last[session+":"+id] = int64(total)
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch {
		case strings.HasSuffix(p, ".jsonl"):
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			session := ""
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				var m map[string]any
				if json.Unmarshal(sc.Bytes(), &m) != nil {
					continue
				}
				if s, ok := m["sessionId"].(string); ok && session == "" {
					session = s
				}
				take(session, m)
			}
			return sc.Err()
		case strings.HasSuffix(p, ".json") && filepath.Base(filepath.Dir(p)) == "chats":
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				return err
			}
			session, _ := m["sessionId"].(string)
			msgs, _ := m["messages"].([]any)
			for _, x := range msgs {
				if mm, ok := x.(map[string]any); ok {
					take(session, mm)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range last {
		sum += v
	}
	return sum, len(last)
}

func TestName(t *testing.T) {
	if got := New(Options{}).Name(); got != model.SourceGeminiCLI {
		t.Fatalf("Name = %q", got)
	}
}

func TestGoldenTotals(t *testing.T) {
	root, c, deps := setup(t)
	res := collect(t, c, deps)

	// g1 (re-appended with its tokens), g2 (rewound but spent), the
	// sub-agent message and the legacy-file message. g3 has no tokens.
	if res.Added != 4 || res.Updated != 0 || res.Files != 3 {
		t.Fatalf("result = %+v, want Added 4 Updated 0 Files 3", res)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}

	sum, n := referenceTotal(t, root)
	all := totals(t, deps.Store)
	if sum != 4335 || n != 4 || all.TotalTokens() != sum || all.RecordCount != int64(n) {
		t.Fatalf("stored total = %d over %d records; reference = %d over %d (want 4335 over 4)",
			all.TotalTokens(), all.RecordCount, sum, n)
	}

	rows, err := deps.Store.Breakdown(context.Background(), store.Filter{}, store.DimModel)
	if err != nil {
		t.Fatal(err)
	}
	type want struct{ in, out, cacheRead, thinking, req int64 }
	exp := map[string]want{
		"gemini-3-pro":   {in: 650, out: 300, cacheRead: 400, thinking: 100, req: 1}, // input 1000-400+tool 50; output 200+thoughts 100
		"gemini-3-flash": {in: 2100, out: 310, req: 2},
		"gemini-2.5-pro": {in: 400, out: 75, cacheRead: 100, thinking: 25, req: 1},
	}
	if len(rows) != len(exp) {
		t.Fatalf("got %d models, want %d: %+v", len(rows), len(exp), rows)
	}
	for _, r := range rows {
		w, ok := exp[r.Key]
		got := want{r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.ThinkingTokens, r.RequestCount}
		if !ok || got != w || r.CacheWrite5mTokens != 0 || r.CacheWrite1hTokens != 0 {
			t.Errorf("model %q = %+v, want %+v", r.Key, got, w)
		}
	}

	ctx := context.Background()
	rec, found, err := deps.Store.GetUsageRecord(ctx, model.SourceGeminiCLI, "sess-a-0001:g1")
	if err != nil || !found {
		t.Fatalf("g1: found=%v err=%v", found, err)
	}
	if rec.Service != model.ServiceGoogle || rec.Session != "sess-a-0001" || rec.Project != "/work/alpha" ||
		rec.DeviceID != testDevice || rec.IsSidechain || !rec.Timestamp.Equal(time.Date(2026, 11, 1, 10, 0, 5, 0, time.UTC)) {
		t.Errorf("g1 = %+v", rec)
	}
	rec, found, err = deps.Store.GetUsageRecord(ctx, model.SourceGeminiCLI, "agent-9:s1")
	if err != nil || !found || !rec.IsSidechain || rec.Project != "/work/alpha" {
		t.Errorf("sub-agent record = %+v found=%v err=%v", rec, found, err)
	}
	// proj-b has no .project_root: the folder name stands in.
	rec, found, err = deps.Store.GetUsageRecord(ctx, model.SourceGeminiCLI, "sess-b-0001:lg1")
	if err != nil || !found || rec.Project != "proj-b" || rec.IsSidechain {
		t.Errorf("legacy record = %+v found=%v err=%v", rec, found, err)
	}
	if _, found, _ := deps.Store.GetUsageRecord(ctx, model.SourceGeminiCLI, "sess-a-0001:g3"); found {
		t.Error("g3 has no tokens and must not be stored")
	}
}

func TestSecondScanIsNoop(t *testing.T) {
	_, c, deps := setup(t)
	collect(t, c, deps)
	before := totals(t, deps.Store)

	res := collect(t, c, deps)
	if res.Added != 0 || res.Updated != 0 || len(res.Warnings) != 0 {
		t.Fatalf("second scan = %+v, want nothing new", res)
	}
	if after := totals(t, deps.Store); after != before {
		t.Fatalf("totals changed: %+v -> %+v", before, after)
	}
}

func TestRescanWithoutStateIsIdempotent(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)
	before := totals(t, deps.Store)

	for _, p := range []string{
		mainSession(root),
		filepath.Join(root, "proj-a", "chats", "sess-a-0001", "agent-9.jsonl"),
	} {
		err := deps.Store.SetIngestState(context.Background(), store.IngestState{Source: model.SourceGeminiCLI, Path: p})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Legacy file: a state with a different size forces a full re-read.
	err := deps.Store.SetIngestState(context.Background(), store.IngestState{
		Source: model.SourceGeminiCLI, Path: filepath.Join(root, "proj-b", "chats", "session-2026-10-01T09-00-legacy01.json"), Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := collect(t, c, deps)
	if res.Added != 0 || res.Updated != 0 {
		t.Fatalf("rescan = %+v, want no new or changed records", res)
	}
	if after := totals(t, deps.Store); after != before {
		t.Fatalf("totals changed: %+v -> %+v", before, after)
	}
}

func TestAppendUsesSessionFromFirstLine(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)

	// g3 gets its tokens later: the same id is appended again.
	appendTo(t, mainSession(root), geminiLine("g3", "2026-11-01T10:02:00.000Z",
		&tokens{Input: 700, Output: 70, Cached: 200, Total: 770}, "gemini-3-flash"))
	appendTo(t, mainSession(root), geminiLine("g4", "2026-11-01T10:03:00.000Z",
		&tokens{Input: 10, Output: 5, Total: 15}, "gemini-3-flash"))

	res := collect(t, c, deps)
	if res.Added != 2 || res.Updated != 0 || len(res.Warnings) != 0 {
		t.Fatalf("append scan = %+v, want exactly two new records", res)
	}
	// The session id lives only in the first line, which was not read again.
	rec, found, err := deps.Store.GetUsageRecord(context.Background(), model.SourceGeminiCLI, "sess-a-0001:g3")
	if err != nil || !found {
		t.Fatalf("g3: found=%v err=%v", found, err)
	}
	if rec.Session != "sess-a-0001" || rec.Project != "/work/alpha" || rec.InputTokens != 500 || rec.CacheReadTokens != 200 {
		t.Errorf("g3 = %+v", rec)
	}
	if got := totals(t, deps.Store).TotalTokens(); got != 4335+770+15 {
		t.Fatalf("total tokens = %d, want %d", got, 4335+770+15)
	}
}

func TestChangedValueUpdatesInsteadOfDoubleCounting(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)
	// The same message id re-appended with different usage: last wins.
	appendTo(t, mainSession(root), geminiLine("g2", "2026-11-01T10:01:00.000Z",
		&tokens{Input: 2500, Output: 300, Total: 2800}, "gemini-3-flash"))
	res := collect(t, c, deps)
	if res.Added != 0 || res.Updated != 1 {
		t.Fatalf("result = %+v, want Updated 1", res)
	}
	if got := totals(t, deps.Store).TotalTokens(); got != 4335-2300+2800 {
		t.Fatalf("total tokens = %d, want %d", got, 4335-2300+2800)
	}
}

func TestLegacyFileIsReadAgainWhenItChanges(t *testing.T) {
	root, c, deps := setup(t)
	collect(t, c, deps)

	p := filepath.Join(root, "proj-b", "chats", "session-2026-10-01T09-00-legacy01.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// Gemini CLI rewrites the whole file on each save: a new message appears.
	extra := `{"id":"lg2","timestamp":"2026-10-01T09:06:00.000Z","type":"gemini","content":"...","tokens":{"input":40,"output":4,"cached":0,"thoughts":0,"tool":0,"total":44},"model":"gemini-2.5-pro"}`
	updated := strings.Replace(string(b), `"messages": [`, `"messages": [`+extra+`,`, 1)
	if err := os.WriteFile(p, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	res := collect(t, c, deps)
	if res.Added != 1 || res.Updated != 0 {
		t.Fatalf("result = %+v, want only the new message to be added", res)
	}
}

func TestCorruptLineWarnsAndScanContinues(t *testing.T) {
	root, c, deps := setup(t)
	appendTo(t, mainSession(root), `{"id":"g9","type":"gemini","tokens":{"input":1 SECRET-PROMPT`+"\n")

	res := collect(t, c, deps)
	if res.Added != 4 {
		t.Fatalf("Added = %d, want 4 (the corrupt line must not stop the scan)", res.Added)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "syntax error") {
		t.Fatalf("warnings = %v, want one syntax error warning", res.Warnings)
	}
	if strings.Contains(res.Warnings[0], "SECRET-PROMPT") {
		t.Fatalf("warning leaks line content: %s", res.Warnings[0])
	}
}

func TestRecordingWithoutTokensWarns(t *testing.T) {
	root := t.TempDir()
	chats := filepath.Join(root, "proj-x", "chats")
	if err := os.MkdirAll(chats, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"sessionId":"sess-x","projectHash":"proj-x","kind":"main"}` + "\n" +
		geminiLine("x1", "2026-11-01T10:00:00.000Z", nil, "gemini-3-pro") +
		geminiLine("x2", "2026-11-01T10:00:01.000Z", &tokens{}, "gemini-3-pro") +
		geminiLine("x3", "2026-11-01T10:00:02.000Z", nil, "gemini-3-pro") // in flight: not counted
	if err := os.WriteFile(filepath.Join(chats, "session-x.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, deps := setup(t)
	res := collect(t, New(Options{Root: root}), deps)
	if res.Added != 0 || res.Files != 1 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "no token data in 2 model message(s)") {
		t.Fatalf("warnings = %v, want a no-token-data warning", res.Warnings)
	}
}

func TestProjectsWithoutRecordingsWarn(t *testing.T) {
	root := t.TempDir()
	// What this repo's author's machine looks like: projects with only logs.json.
	for _, p := range []string{"one", "two"} {
		if err := os.MkdirAll(filepath.Join(root, p, "chats"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p, "logs.json"), []byte("[]"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, deps := setup(t)
	res := collect(t, New(Options{Root: root}), deps)
	if res.Added != 0 || res.Files != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "no token data") ||
		!strings.Contains(res.Warnings[0], "2 project folder(s)") {
		t.Fatalf("warnings = %v, want one no-token-data warning", res.Warnings)
	}
}

func TestMissingOrEmptyRootIsQuiet(t *testing.T) {
	_, _, deps := setup(t)
	for name, root := range map[string]string{
		"missing": filepath.Join(t.TempDir(), "does-not-exist"),
		"empty":   t.TempDir(),
	} {
		res := collect(t, New(Options{Root: root}), deps)
		if res.Added != 0 || res.Files != 0 || len(res.Warnings) != 0 {
			t.Errorf("%s root: result = %+v, want empty", name, res)
		}
	}
}

func TestLogsJSONIsNeverRead(t *testing.T) {
	root := t.TempDir()
	chats := filepath.Join(root, "p", "chats")
	if err := os.MkdirAll(chats, 0o700); err != nil {
		t.Fatal(err)
	}
	// A logs.json that looks like a recording must not be ingested.
	bait := `{"sessionId":"s","messages":[{"id":"m","timestamp":"2026-11-01T10:00:00.000Z","type":"gemini","tokens":{"input":9,"output":9,"total":18},"model":"x"}]}`
	if err := os.WriteFile(filepath.Join(root, "p", "logs.json"), []byte(bait), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, deps := setup(t)
	if res := collect(t, New(Options{Root: root}), deps); res.Added != 0 {
		t.Fatalf("logs.json was ingested: %+v", res)
	}
}

func TestRootResolution(t *testing.T) {
	home := func() (string, error) { return filepath.Join("h", "me"), nil }
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	got, err := New(Options{Getenv: env(nil), UserHomeDir: home}).DefaultRoot()
	if err != nil || got != filepath.Join("h", "me", ".gemini", "tmp") {
		t.Errorf("home default = %q, %v", got, err)
	}
	got, _ = New(Options{Getenv: env(map[string]string{EnvCLIHome: "gh"}), UserHomeDir: home}).DefaultRoot()
	if got != filepath.Join("gh", ".gemini", "tmp") {
		t.Errorf("GEMINI_CLI_HOME = %q", got)
	}
	got, _ = New(Options{Root: "explicit", Getenv: env(map[string]string{EnvCLIHome: "gh"}), UserHomeDir: home}).DefaultRoot()
	if got != "explicit" {
		t.Errorf("Options.Root = %q", got)
	}

	// The settings key beats everything and is what Collect scans.
	root, _, deps := setup(t)
	if err := deps.Store.SetSetting(context.Background(), SettingRootPath, root); err != nil {
		t.Fatal(err)
	}
	c := New(Options{Root: filepath.Join(t.TempDir(), "elsewhere")})
	if res := collect(t, c, deps); res.Added != 4 {
		t.Fatalf("settings override ignored: %+v", res)
	}
}

func TestNoTokenDataWarningOnlyForFullRead(t *testing.T) {
	root := t.TempDir()
	chats := filepath.Join(root, "proj-x", "chats")
	if err := os.MkdirAll(chats, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(chats, "session-x.jsonl")
	content := `{"sessionId":"sess-x","projectHash":"proj-x","kind":"main"}` + "\n" +
		geminiLine("x1", "2026-11-01T10:00:00.000Z", nil, "gemini-3-pro")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, deps := setup(t)
	c := New(Options{Root: root})
	// The only message may be a response still in flight: no warning yet.
	if res := collect(t, c, deps); len(res.Warnings) != 0 {
		t.Fatalf("first read warnings = %v, want none for a lone in-flight message", res.Warnings)
	}

	// A new message is written, tokens still to come; then its re-append
	// arrives. A read that resumes in between must not warn.
	appendTo(t, p, geminiLine("x2", "2026-11-01T10:01:00.000Z", nil, "gemini-3-pro"))
	if res := collect(t, c, deps); len(res.Warnings) != 0 || res.Added != 0 {
		t.Fatalf("resumed read = %+v, want no warning", res)
	}
	appendTo(t, p, geminiLine("x2", "2026-11-01T10:01:00.000Z", &tokens{Input: 5, Output: 1, Total: 6}, "gemini-3-pro"))
	if res := collect(t, c, deps); len(res.Warnings) != 0 || res.Added != 1 {
		t.Fatalf("completed message = %+v, want Added 1 and no warning", res)
	}
}
