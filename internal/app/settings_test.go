package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/collector"
	"github.com/muhammetsafak/TokenMetrics/internal/dataio"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/secrets"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

// A made-up key shaped like a real Admin key. It must never show up in any
// value a binding returns.
const fakeAdminKey = "sk-ant-admin01-FAKEKEYfakekey0123456789ABCDEFWXYZ"

type fakeKeys struct {
	m      map[string]string
	setErr error
	getErr error
}

func newFakeKeys() *fakeKeys { return &fakeKeys{m: map[string]string{}} }

func (f *fakeKeys) Set(p, k string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.m[p] = k
	return nil
}

func (f *fakeKeys) Get(p string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.m[p]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return v, nil
}

func (f *fakeKeys) Delete(p string) error { delete(f.m, p); return nil }

// settingsService is a Service whose key storage is fk and whose registry
// holds a rooted fake for every scanned source.
func settingsService(t *testing.T, fk *fakeKeys) (*Service, *store.Store) {
	t.Helper()
	st := openStore(t)
	reg := collector.NewRegistry()
	for _, src := range []model.Source{
		model.SourceClaudeCode, model.SourceCodexCLI, model.SourceGeminiCLI,
		model.SourceAnthropicAPI, model.SourceOpenAIAPI,
	} {
		if err := reg.Register(&rootedCollector{fakeCollector: fakeCollector{name: src}, root: filepath.Join(t.TempDir(), "default-root")}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Config{
		Store: st, DeviceID: "dev", Registry: reg,
		Now: func() time.Time { return testNow }, Location: testLoc,
		Keys: fk,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

type rootedCollector struct {
	fakeCollector
	root string
}

func (r *rootedCollector) DefaultRoot() (string, error) { return r.root, nil }

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAdminKeyNeverReturned(t *testing.T) {
	ctx := context.Background()
	fk := newFakeKeys()
	s, _ := settingsService(t, fk)

	var outputs []string
	got, err := s.SetAdminKey(secrets.ProviderAnthropic, "  "+fakeAdminKey+"\n")
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, mustJSON(t, got))
	if fk.m[secrets.ProviderAnthropic] != fakeAdminKey {
		t.Fatal("the trimmed key was not stored")
	}
	if !got.Configured || !strings.HasSuffix(got.Masked, fakeAdminKey[len(fakeAdminKey)-4:]) || got.Masked == fakeAdminKey {
		t.Fatalf("masked state = %+v", got)
	}

	outputs = append(outputs, mustJSON(t, s.AdminKeys()))
	rows, err := s.SourceSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, mustJSON(t, rows))
	meta, err := s.Meta(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, mustJSON(t, meta))
	// A scan reports through ScanStep too.
	sum, err := s.Rescan(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, mustJSON(t, sum))

	// Errors never echo the key, even when the storage layer does.
	fk.setErr = errors.New("storage refused " + fakeAdminKey)
	_, err = s.SetAdminKey(secrets.ProviderOpenAI, fakeAdminKey)
	if err == nil {
		t.Fatal("storage error was swallowed")
	}
	outputs = append(outputs, err.Error())
	fk.setErr = nil
	_, err = s.SetAdminKey(secrets.ProviderOpenAI, "two words "+fakeAdminKey)
	if err == nil {
		t.Fatal("a key with spaces was accepted")
	}
	outputs = append(outputs, err.Error())

	for i, o := range outputs {
		if strings.Contains(o, fakeAdminKey) || strings.Contains(o, fakeAdminKey[:len(fakeAdminKey)-4]) {
			t.Errorf("output %d contains the key: %s", i, o)
		}
	}

	// The source list knows that a key exists without carrying it.
	for _, r := range rows {
		if r.ID == "anthropic_api" && (!r.HasKey || !r.Active) {
			t.Errorf("anthropic_api row = %+v", r)
		}
		if r.ID == "openai_api" && r.HasKey {
			t.Errorf("openai_api row claims a key: %+v", r)
		}
	}

	after, err := s.DeleteAdminKey(secrets.ProviderAnthropic)
	if err != nil || after.Configured || after.Masked != "" {
		t.Fatalf("delete = %+v, %v", after, err)
	}
}

func TestAdminKeyValidation(t *testing.T) {
	s, _ := settingsService(t, newFakeKeys())
	if _, err := s.SetAdminKey("google", "x"); err == nil {
		t.Error("unknown provider accepted")
	}
	if _, err := s.SetAdminKey(secrets.ProviderOpenAI, "   "); err == nil {
		t.Error("empty key accepted")
	}
	if _, err := s.DeleteAdminKey("nope"); err == nil {
		t.Error("unknown provider accepted by delete")
	}
}

func TestAdminKeysKeychainUnavailable(t *testing.T) {
	fk := newFakeKeys()
	fk.getErr = secrets.ErrKeychainUnavailable
	fk.setErr = secrets.ErrKeychainUnavailable
	s, _ := settingsService(t, fk)

	got := s.AdminKeys()
	if got.KeychainError == "" || len(got.Keys) != 2 {
		t.Fatalf("AdminKeys = %+v", got)
	}
	for _, k := range got.Keys {
		if k.Configured || k.Error == "" {
			t.Errorf("key state = %+v", k)
		}
	}
	_, err := s.SetAdminKey(secrets.ProviderAnthropic, fakeAdminKey)
	if err == nil || !strings.Contains(err.Error(), "anahtar deposuna erişilemiyor") {
		t.Fatalf("SetAdminKey error = %v", err)
	}
}

func TestSourceSettings(t *testing.T) {
	ctx := context.Background()
	s, st := settingsService(t, newFakeKeys())

	find := func() SourceSettingDTO {
		t.Helper()
		rows, err := s.SourceSettings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(sources) {
			t.Fatalf("got %d rows", len(rows))
		}
		for _, r := range rows {
			if r.ID == "claude_code" {
				return r
			}
		}
		t.Fatal("claude_code missing")
		return SourceSettingDTO{}
	}

	r := find()
	if !r.Enabled || !r.Active || !r.PathEditable || r.PathOverride != "" || r.DefaultPath == "" || r.EffectivePath != r.DefaultPath || r.PathExists {
		t.Fatalf("initial row = %+v", r)
	}

	if err := s.SetSourceEnabled(ctx, "claude_code", false); err != nil {
		t.Fatal(err)
	}
	if r := find(); r.Enabled || r.Active {
		t.Fatalf("after disable = %+v", r)
	}
	if err := s.SetSourceEnabled(ctx, "manual", false); err == nil {
		t.Error("a manual source cannot be switched")
	}
	if err := s.SetSourceEnabled(ctx, "bogus", false); err == nil {
		t.Error("unknown source accepted")
	}

	dir := t.TempDir()
	if err := s.SetSourcePath(ctx, "claude_code", dir); err != nil {
		t.Fatal(err)
	}
	var stored string
	if found, _ := st.GetSetting(ctx, "sources.claude_code.path", &stored); !found || stored != dir {
		t.Fatalf("stored path = %q (found %v), want %q", stored, found, dir)
	}
	if r := find(); r.PathOverride != dir || r.EffectivePath != dir || !r.PathExists {
		t.Fatalf("after set path = %+v", r)
	}
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSourcePath(ctx, "claude_code", file); err == nil {
		t.Error("a file was accepted as folder")
	}
	if err := s.SetSourcePath(ctx, "claude_code", filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing folder was accepted")
	}
	if err := s.SetSourcePath(ctx, "anthropic_api", dir); err == nil {
		t.Error("an API source has no folder")
	}
	if err := s.ResetSourcePath(ctx, "claude_code"); err != nil {
		t.Fatal(err)
	}
	if r := find(); r.PathOverride != "" || r.EffectivePath != r.DefaultPath {
		t.Fatalf("after reset = %+v", r)
	}
}

func TestSourceSettingsLastScan(t *testing.T) {
	ctx := context.Background()
	s, _ := settingsService(t, newFakeKeys())
	if _, err := s.Rescan(ctx, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := s.SourceSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		switch r.ID {
		case "claude_code":
			if r.LastStatus != StatusDone || r.LastAdded != 1 || r.LastScanMs == 0 {
				t.Errorf("claude_code = %+v", r)
			}
		case "anthropic_api":
			if r.LastStatus != StatusSkipped {
				t.Errorf("anthropic_api = %+v", r)
			}
		case "manual":
			if r.LastStatus != "" {
				t.Errorf("manual = %+v", r)
			}
		}
	}
}

func f64(v float64) *float64 { return &v }

func TestPrices(t *testing.T) {
	ctx := context.Background()
	s, _ := settingsService(t, newFakeKeys())

	table, err := s.Prices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Rows) < 10 || table.AsOf == "" || table.LongPromptThreshold <= 0 {
		t.Fatalf("table = %d rows, asOf %q", len(table.Rows), table.AsOf)
	}
	row := func(name string) PriceRowDTO {
		t.Helper()
		tb, err := s.Prices(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range tb.Rows {
			if r.Model == name {
				return r
			}
		}
		t.Fatalf("model %q not in table", name)
		return PriceRowDTO{}
	}

	base := row("claude-haiku-5-5")
	if base.Custom || base.Overridden || base.Default == nil || base.Effective == nil || base.Override != nil || base.LongPrompt == nil {
		t.Fatalf("base row = %+v", base)
	}

	if err := s.SetPriceOverride(ctx, PriceOverrideDTO{Model: " Claude-Haiku-5-5 ", Input: f64(0.25)}); err != nil {
		t.Fatal(err)
	}
	o := row("claude-haiku-5-5")
	if !o.Overridden || o.Override == nil || *o.Override.Input != 0.25 || o.Override.Output != nil {
		t.Fatalf("override row = %+v", o)
	}
	if o.Effective.Input != 0.25 || o.Effective.Output != base.Effective.Output || o.Default.Input != base.Default.Input {
		t.Fatalf("effective = %+v default = %+v", o.Effective, o.Default)
	}
	if o.LongPrompt == nil || o.LongPrompt.Input != 0.25 {
		t.Fatalf("long prompt tier = %+v", o.LongPrompt)
	}

	// A model that is not in the table becomes a custom row.
	if err := s.SetPriceOverride(ctx, PriceOverrideDTO{Model: "my-model", Input: f64(1), Output: f64(2)}); err != nil {
		t.Fatal(err)
	}
	c := row("my-model")
	if !c.Custom || c.Default != nil || c.Effective == nil || c.Effective.Output != 2 {
		t.Fatalf("custom row = %+v", c)
	}
	// An input-only custom override leaves the model unpriced.
	if err := s.SetPriceOverride(ctx, PriceOverrideDTO{Model: "half-model", Input: f64(1)}); err != nil {
		t.Fatal(err)
	}
	if h := row("half-model"); !h.Custom || h.Effective != nil {
		t.Fatalf("half custom row = %+v", h)
	}

	for _, bad := range []PriceOverrideDTO{
		{Model: "", Input: f64(1)},
		{Model: "x"},
		{Model: "x", Input: f64(-1)},
		{Model: "x", Output: f64(2e6)},
		{Model: strings.Repeat("a", 300), Input: f64(1)},
	} {
		if err := s.SetPriceOverride(ctx, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}

	if err := s.ResetPriceOverride(ctx, "claude-haiku-5-5"); err != nil {
		t.Fatal(err)
	}
	if r := row("claude-haiku-5-5"); r.Overridden || r.Override != nil || r.Effective.Input != base.Effective.Input {
		t.Fatalf("after reset = %+v", r)
	}
	if err := s.ResetPriceOverride(ctx, "my-model"); err != nil {
		t.Fatal(err)
	}
	tb, _ := s.Prices(ctx)
	for _, r := range tb.Rows {
		if r.Model == "my-model" {
			t.Error("custom row survived its reset")
		}
	}
}

func TestLimitSettings(t *testing.T) {
	ctx := context.Background()
	s, _ := settingsService(t, newFakeKeys())

	got, err := s.LimitSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Limits) != 0 || got.WeeklyStart.Weekday != 1 || got.WeeklyStart.Hour != 0 {
		t.Fatalf("defaults = %+v", got)
	}

	tok, err := s.SaveLimit(ctx, LimitSettingDTO{Service: " anthropic ", Kind: "5h", Metric: "total_tokens", Limit: 1_500_000})
	if err != nil || tok.ID == 0 || tok.Service != "anthropic" || tok.Limit != 1_500_000 {
		t.Fatalf("token limit = %+v, %v", tok, err)
	}
	usd, err := s.SaveLimit(ctx, LimitSettingDTO{Kind: "weekly", Metric: "cost_usd", Limit: 12.5})
	if err != nil || usd.Limit != 12.5 {
		t.Fatalf("cost limit = %+v, %v", usd, err)
	}
	// One limit per (service, kind): saving again replaces.
	again, err := s.SaveLimit(ctx, LimitSettingDTO{Service: "anthropic", Kind: "5h", Metric: "output_tokens", Limit: 900})
	if err != nil || again.ID != tok.ID {
		t.Fatalf("replace = %+v, %v", again, err)
	}
	got, _ = s.LimitSettings(ctx)
	if len(got.Limits) != 2 {
		t.Fatalf("limits = %+v", got.Limits)
	}
	// What the dashboard evaluates matches what was entered.
	dash, err := s.Limits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range dash.Limits {
		if l.Metric == "cost_usd" && l.Limit != 12.5 {
			t.Errorf("dashboard limit = %v, want 12.5", l.Limit)
		}
	}

	for _, bad := range []LimitSettingDTO{
		{Kind: "daily", Metric: "total_tokens", Limit: 1},
		{Kind: "5h", Metric: "bogus", Limit: 1},
		{Kind: "5h", Metric: "total_tokens", Limit: 0},
		{Kind: "5h", Metric: "total_tokens", Limit: 1e16},
		{Kind: "5h", Metric: "cost_usd", Limit: 0.001},
	} {
		if _, err := s.SaveLimit(ctx, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}

	if err := s.DeleteLimit(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.LimitSettings(ctx); len(got.Limits) != 1 {
		t.Fatalf("after delete = %+v", got.Limits)
	}

	ws, err := s.SetWeeklyStart(ctx, WeeklyStartDTO{Weekday: 3, Hour: 9, Timezone: "Europe/Istanbul"})
	if err != nil || ws.Weekday != 3 {
		t.Fatalf("weekly start = %+v, %v", ws, err)
	}
	if got, _ = s.LimitSettings(ctx); got.WeeklyStart != ws {
		t.Fatalf("weekly start read back = %+v", got.WeeklyStart)
	}
	for _, bad := range []WeeklyStartDTO{{Weekday: 7}, {Hour: 24}, {Weekday: 1, Timezone: "Mars/Olympus"}} {
		if _, err := s.SetWeeklyStart(ctx, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	// The failed saves left the good value in place.
	if got, _ = s.LimitSettings(ctx); got.WeeklyStart != ws {
		t.Fatalf("weekly start changed by a failed save: %+v", got.WeeklyStart)
	}
}

func TestBudgets(t *testing.T) {
	ctx := context.Background()
	s, _ := settingsService(t, newFakeKeys())

	list, err := s.Budgets(ctx)
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty list = %#v, %v", list, err)
	}
	tok := int64(5_000_000)
	b, err := s.SaveBudget(ctx, BudgetDTO{Scope: "service", ScopeValue: " anthropic ", Period: "monthly", ThresholdTokens: &tok, ThresholdUSD: f64(50), Enabled: true})
	if err != nil || b.ID == 0 || b.ScopeValue != "anthropic" {
		t.Fatalf("create = %+v, %v", b, err)
	}
	total, err := s.SaveBudget(ctx, BudgetDTO{Scope: "total", ScopeValue: "ignored", Period: "daily", ThresholdUSD: f64(5)})
	if err != nil || total.ScopeValue != "" || total.Enabled {
		t.Fatalf("total budget = %+v, %v", total, err)
	}

	b.Enabled = false
	b.ThresholdTokens = nil
	up, err := s.SaveBudget(ctx, b)
	if err != nil || up.ID != b.ID || up.ThresholdTokens != nil {
		t.Fatalf("update = %+v, %v", up, err)
	}
	list, _ = s.Budgets(ctx)
	if len(list) != 2 || list[0].Enabled || list[0].ThresholdTokens != nil || *list[0].ThresholdUSD != 50 {
		t.Fatalf("list = %+v", list)
	}

	zero := int64(0)
	for _, bad := range []BudgetDTO{
		{Scope: "service", Period: "daily", ThresholdUSD: f64(1)},
		{Scope: "galaxy", Period: "daily", ThresholdUSD: f64(1)},
		{Scope: "total", Period: "hourly", ThresholdUSD: f64(1)},
		{Scope: "total", Period: "daily"},
		{Scope: "total", Period: "daily", ThresholdTokens: &zero},
		{Scope: "total", Period: "daily", ThresholdUSD: f64(0)},
		{ID: 999, Scope: "total", Period: "daily", ThresholdUSD: f64(1)},
	} {
		if _, err := s.SaveBudget(ctx, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if _, err := s.SaveBudget(ctx, BudgetDTO{ID: 999, Scope: "total", Period: "daily", ThresholdUSD: f64(1)}); !errors.Is(err, ErrBudgetNotFound) {
		t.Errorf("missing budget error = %v", err)
	}

	if err := s.DeleteBudget(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.Budgets(ctx); len(list) != 1 || list[0].ID != total.ID {
		t.Fatalf("after delete = %+v", list)
	}
}

func manualDTOAt(h int) ManualRecordDTO {
	return ManualRecordDTO{
		TimestampMs: day(0, h).UnixMilli(), Service: "anthropic", Model: "claude-sonnet-5",
		InputTokens: 1000, OutputTokens: 500,
	}
}

func TestManualRecords(t *testing.T) {
	ctx := context.Background()
	s, st := settingsService(t, newFakeKeys())

	added, err := s.AddManualRecord(ctx, manualDTOAt(9))
	if err != nil || added.ExternalID == "" {
		t.Fatalf("add = %+v, %v", added, err)
	}
	second := manualDTOAt(11)
	second.Project = "p"
	if _, err := s.AddManualRecord(ctx, second); err != nil {
		t.Fatal(err)
	}
	// A collector record is not part of the manual list.
	insert(t, st, rec("c1", day(0, 10), model.SourceClaudeCode, "anthropic", "m", "p", "s", 1, 1, 0))

	list, err := s.ManualRecords(ctx)
	if err != nil || list.Total != 2 || len(list.Rows) != 2 || list.Truncated {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list.Rows[0].TimestampMs < list.Rows[1].TimestampMs {
		t.Error("list is not newest first")
	}

	added.OutputTokens = 777
	added.CostUSD = f64(0.5)
	up, err := s.UpdateManualRecord(ctx, added)
	if err != nil || up.OutputTokens != 777 || up.CostUSD == nil {
		t.Fatalf("update = %+v, %v", up, err)
	}
	// The collector record cannot be changed through the manual path.
	if _, err := s.UpdateManualRecord(ctx, ManualRecordDTO{ExternalID: "c1", TimestampMs: day(0, 10).UnixMilli(), Service: "a", Model: "m", InputTokens: 1}); err == nil {
		t.Error("a collector record was updated")
	}
	if err := s.DeleteManualRecord(ctx, "c1"); err == nil || !strings.Contains(err.Error(), "bulunamadı") {
		t.Errorf("delete collector record error = %v", err)
	}

	bad := manualDTOAt(9)
	bad.InputTokens, bad.OutputTokens = 0, 0
	if _, err := s.AddManualRecord(ctx, bad); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("zero tokens error = %v", err)
	}
	bad = manualDTOAt(9)
	bad.Model = " "
	if _, err := s.AddManualRecord(ctx, bad); err == nil || !strings.Contains(err.Error(), "Model") {
		t.Errorf("empty model error = %v", err)
	}

	if err := s.DeleteManualRecord(ctx, added.ExternalID); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.ManualRecords(ctx); list.Total != 1 {
		t.Fatalf("after delete = %+v", list)
	}
}

func totalsOf(t *testing.T, s *Service) store.Totals {
	t.Helper()
	tot, err := s.st.Totals(context.Background(), store.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	return tot
}

func TestImportCSVFile(t *testing.T) {
	ctx := context.Background()
	s, _ := settingsService(t, newFakeKeys())

	res, err := s.ImportCSVFile(ctx, filepath.Join("..", "dataio", "testdata", "errors.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if res.File != "errors.csv" || res.Format != FormatCSV || res.Added < 1 || res.ErrorCount < 1 || len(res.Errors) != res.ErrorCount {
		t.Fatalf("result = %+v", res)
	}
	if res.Errors[0].Line < 2 || res.Errors[0].Reason == "" {
		t.Errorf("first error = %+v", res.Errors[0])
	}
	if got := totalsOf(t, s).RecordCount; got != int64(res.Added) {
		t.Errorf("stored %d records, result says %d", got, res.Added)
	}
	// The same file again changes nothing.
	again, err := s.ImportCSVFile(ctx, filepath.Join("..", "dataio", "testdata", "errors.csv"))
	if err != nil || again.Added != 0 || again.Unchanged != res.Added {
		t.Fatalf("second import = %+v, %v", again, err)
	}

	if _, err := s.ImportCSVFile(ctx, filepath.Join(t.TempDir())); err == nil {
		t.Error("a directory was imported")
	}
	empty := filepath.Join(t.TempDir(), "empty.csv")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportCSVFile(ctx, empty); err == nil || !strings.Contains(err.Error(), "boş") {
		t.Errorf("empty file error = %v", err)
	}
	notJSON := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(notJSON, []byte(`{"hello":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportJSONFile(ctx, notJSON); err == nil {
		t.Error("a foreign JSON file was imported")
	}
}

func TestExportImportJSONRoundTrip(t *testing.T) {
	ctx := context.Background()
	src, st := settingsService(t, newFakeKeys())
	insert(t, st,
		rec("a", day(-1, 10), model.SourceClaudeCode, "anthropic", "claude-sonnet-5", "/work/app", "s1", 100, 50, 25),
		rec("b", day(0, 9), model.SourceCodexCLI, "openai", "gpt-5.5", "/work/app", "s2", 300, 70, 0),
	)
	if _, err := src.AddManualRecord(ctx, manualDTOAt(12)); err != nil {
		t.Fatal(err)
	}
	want := totalsOf(t, src)

	path := filepath.Join(t.TempDir(), "export")
	res, err := src.ExportFile(ctx, path, FormatJSON, FilterDTO{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Records != 3 || res.Path != path+".json" {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatal(err)
	}

	// An empty database, as after pointing TOKENMETRICS_DATA_DIR at a new dir.
	dst, _ := settingsService(t, newFakeKeys())
	imp, err := dst.ImportJSONFile(ctx, res.Path)
	if err != nil || imp.Added != 3 || imp.ErrorCount != 0 {
		t.Fatalf("import = %+v, %v", imp, err)
	}
	if got := totalsOf(t, dst); got != want {
		t.Fatalf("totals after import = %+v, want %+v", got, want)
	}
	// Importing again is safe: nothing is counted twice.
	imp, err = dst.ImportJSONFile(ctx, res.Path)
	if err != nil || imp.Added != 0 || imp.Updated != 0 {
		t.Fatalf("second import = %+v, %v", imp, err)
	}
	if got := totalsOf(t, dst); got != want {
		t.Fatalf("totals after second import = %+v", got)
	}
	// Settings and keys are not in an export.
	raw, _ := os.ReadFile(res.Path)
	if strings.Contains(string(raw), "sources.") || strings.Contains(string(raw), "api_key") {
		t.Error("export carries settings")
	}
}

func TestExportFilterAndCSV(t *testing.T) {
	ctx := context.Background()
	s, st := settingsService(t, newFakeKeys())
	insert(t, st,
		rec("a", day(-10, 10), model.SourceClaudeCode, "anthropic", "claude-sonnet-5", "p", "s1", 100, 50, 25),
		rec("b", day(0, 9), model.SourceClaudeCode, "anthropic", "claude-sonnet-5", "p", "s2", 300, 70, 0),
	)
	res, err := s.ExportFile(ctx, filepath.Join(t.TempDir(), "recent.csv"), FormatCSV, FilterDTO{FromMs: day(-1, 0).UnixMilli()})
	if err != nil || res.Records != 1 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	raw, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(raw)), "\n"); lines != 1 { // header + 1 row
		t.Errorf("csv has %d data lines:\n%s", lines, raw)
	}

	if _, err := s.ExportFile(ctx, filepath.Join(t.TempDir(), "x.xml"), "xml", FilterDTO{}); err == nil {
		t.Error("unknown format accepted")
	}
	if _, err := s.ExportFile(ctx, filepath.Join(t.TempDir(), "no-dir", "x.csv"), FormatCSV, FilterDTO{}); err == nil {
		t.Error("an unwritable path was accepted")
	}
	if _, err := s.ExportFile(ctx, filepath.Join(t.TempDir(), "x.csv"), FormatCSV, FilterDTO{FromMs: 10, ToMs: 5}); err == nil {
		t.Error("an empty range was accepted")
	}
}

// TestAcceptanceAcrossDataDirs follows the manual acceptance steps with the
// real Open path: a hand-entered record shows up in the summary, a sample CSV
// is imported with its bad rows listed, and a JSON export imported into a
// database in an empty TOKENMETRICS_DATA_DIR gives the same totals.
func TestAcceptanceAcrossDataDirs(t *testing.T) {
	ctx := context.Background()
	open := func() *Service {
		t.Helper()
		t.Setenv("TOKENMETRICS_DATA_DIR", t.TempDir())
		s, err := Open(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}

	a := open()
	now := time.Now()
	if _, err := a.AddManualRecord(ctx, ManualRecordDTO{TimestampMs: now.Add(-time.Minute).UnixMilli(), Service: "anthropic", Model: "claude-sonnet-5", InputTokens: 1000, OutputTokens: 500}); err != nil {
		t.Fatal(err)
	}
	sum, err := a.Summary(ctx, FilterDTO{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Selected.TotalTokens != 1500 || sum.Selected.Records != 1 {
		t.Fatalf("summary after manual entry = %+v", sum.Selected)
	}

	res, err := a.ImportCSVFile(ctx, filepath.Join("..", "dataio", "testdata", "errors.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCount == 0 || res.Added == 0 || len(res.Errors) != res.ErrorCount {
		t.Fatalf("csv import = %+v", res)
	}
	t.Logf("errors.csv: %d rows, %d added, %d rejected: %+v", res.Rows, res.Added, res.ErrorCount, res.Errors)

	file := filepath.Join(t.TempDir(), "export.json")
	if _, err := a.ExportFile(ctx, file, FormatJSON, FilterDTO{}); err != nil {
		t.Fatal(err)
	}
	want, err := a.Summary(ctx, FilterDTO{})
	if err != nil {
		t.Fatal(err)
	}
	wantAll := totalsOf(t, a)

	b := open() // a new, empty data dir
	if got := totalsOf(t, b); got.RecordCount != 0 {
		t.Fatalf("new data dir is not empty: %+v", got)
	}
	if _, err := b.ImportJSONFile(ctx, file); err != nil {
		t.Fatal(err)
	}
	if got := totalsOf(t, b); got != wantAll {
		t.Fatalf("totals after import = %+v, want %+v", got, wantAll)
	}
	got, err := b.Summary(ctx, FilterDTO{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Selected.TotalTokens != want.Selected.TotalTokens {
		t.Fatalf("summary after import = %+v, want %+v", got.Selected, want.Selected)
	}
}

func TestSaveLimitEditByID(t *testing.T) {
	ctx := context.Background()
	s, _ := settingsService(t, newFakeKeys())
	five, _ := s.SaveLimit(ctx, LimitSettingDTO{Service: "anthropic", Kind: "5h", Metric: "total_tokens", Limit: 1_000_000})
	week, _ := s.SaveLimit(ctx, LimitSettingDTO{Service: "anthropic", Kind: "weekly", Metric: "total_tokens", Limit: 20_000_000})

	// Editing the value keeps the limit.
	up, err := s.SaveLimit(ctx, LimitSettingDTO{ID: five.ID, Service: "anthropic", Kind: "5h", Metric: "output_tokens", Limit: 500})
	if err != nil || up.ID != five.ID || up.Limit != 500 || up.Metric != "output_tokens" {
		t.Fatalf("edit = %+v, %v", up, err)
	}
	// Moving it onto another limit's key is refused and changes nothing.
	if _, err := s.SaveLimit(ctx, LimitSettingDTO{ID: five.ID, Service: "anthropic", Kind: "weekly", Metric: "total_tokens", Limit: 1}); err == nil {
		t.Fatal("an edit changed the window")
	}
	if _, err := s.SaveLimit(ctx, LimitSettingDTO{ID: five.ID, Service: "openai", Kind: "5h", Metric: "total_tokens", Limit: 1}); err == nil {
		t.Fatal("an edit changed the service")
	}
	if _, err := s.SaveLimit(ctx, LimitSettingDTO{ID: 9999, Service: "anthropic", Kind: "5h", Metric: "total_tokens", Limit: 1}); err == nil {
		t.Fatal("a missing limit was edited")
	}
	got, _ := s.LimitSettings(ctx)
	if len(got.Limits) != 2 {
		t.Fatalf("limits = %+v", got.Limits)
	}
	for _, l := range got.Limits {
		if l.ID == week.ID && l.Limit != 20_000_000 {
			t.Errorf("weekly limit was overwritten: %+v", l)
		}
	}
}

func TestExportKeepsExistingFile(t *testing.T) {
	ctx := context.Background()
	s, st := settingsService(t, newFakeKeys())
	insert(t, st, rec("a", day(0, 9), model.SourceClaudeCode, "anthropic", "m", "p", "s", 1, 1, 0))
	dir := t.TempDir()

	// An extension added to a bare name never replaces an existing file.
	existing := filepath.Join(dir, "backup.json")
	if err := os.WriteFile(existing, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExportFile(ctx, filepath.Join(dir, "backup"), FormatJSON, FilterDTO{}); err == nil {
		t.Fatal("export replaced a file the dialog never asked about")
	}
	if b, _ := os.ReadFile(existing); string(b) != "keep me" {
		t.Fatalf("existing file changed: %q", b)
	}

	// A failed export leaves the file it was meant to replace, and no temp file.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExportFile(cctx, target, FormatJSON, FilterDTO{}); err == nil {
		t.Fatal("canceled export succeeded")
	}
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Fatalf("target changed by a failed export: %q", b)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tokenmetrics-export-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	// An explicit name replaces the old file with the finished export.
	if _, err := s.ExportFile(ctx, target, FormatJSON, FilterDTO{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); !strings.Contains(string(b), "schema_version") {
		t.Fatalf("target not replaced: %q", b)
	}
}

func TestImportTooLargeMessage(t *testing.T) {
	csv := importError(dataio.ErrFileTooLarge, FormatCSV).Error()
	js := importError(dataio.ErrFileTooLarge, FormatJSON).Error()
	if !strings.Contains(csv, "CSV") || !strings.Contains(csv, "50") || !strings.Contains(js, "JSON") || !strings.Contains(js, "512") {
		t.Fatalf("messages: %q / %q", csv, js)
	}
}
