package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func rec(id string) model.UsageRecord {
	return model.UsageRecord{
		Source:       model.SourceClaudeCode,
		ExternalID:   id,
		DeviceID:     "dev-1",
		Timestamp:    time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		Service:      model.ServiceAnthropic,
		Model:        "claude-sonnet",
		Project:      "/work/app",
		Session:      "sess-1",
		InputTokens:  100,
		OutputTokens: 50,
		RequestCount: 1,
	}
}

func TestOpenCreatesSchema(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	v, err := s.SchemaVersion(ctx)
	if err != nil || v != 2 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}
	for _, tbl := range []string{
		"usage_records", "ingest_state", "settings", "price_overrides",
		"limit_windows", "budgets", "notifications_sent", "schema_version",
	} {
		var n int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing (n=%d, err=%v)", tbl, n, err)
		}
	}
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v; want wal", mode, err)
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d, %v; want 1", fk, err)
	}
}

func TestMigrateSecondRunIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var appliedAt int64
	if err := s1.db.QueryRow(`SELECT MAX(applied_at) FROM schema_version`).Scan(&appliedAt); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s1.InsertBatch(ctx, []model.UsageRecord{rec("a")}); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	time.Sleep(5 * time.Millisecond)
	s2, err := Open(ctx, path) // runs migrateUp again
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	var rows int
	var appliedAt2 int64
	if err := s2.db.QueryRow(`SELECT COUNT(*), MAX(applied_at) FROM schema_version`).Scan(&rows, &appliedAt2); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || appliedAt2 != appliedAt {
		t.Fatalf("second Open changed schema_version: rows=%d applied_at %d -> %d", rows, appliedAt, appliedAt2)
	}
	if _, ok, err := s2.GetUsageRecord(ctx, model.SourceClaudeCode, "a"); err != nil || !ok {
		t.Fatalf("existing data lost after re-open: ok=%v err=%v", ok, err)
	}
	// Direct re-run is a no-op as well.
	if err := migrateUp(ctx, s2.db); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateDownAndUp(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := migrateDown(ctx, s.db, 0); err != nil {
		t.Fatal(err)
	}
	if v, _ := schemaVersion(ctx, s.db); v != 0 {
		t.Fatalf("version after down = %d", v)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'usage_records'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("usage_records still exists after down (n=%d, err=%v)", n, err)
	}
	if err := migrateUp(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	if v, _ := schemaVersion(ctx, s.db); v != 2 {
		t.Fatalf("version after re-up = %d", v)
	}
}

func TestLoadMigrations(t *testing.T) {
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) == 0 {
		t.Fatal("no migrations embedded")
	}
	for _, m := range migs {
		if m.down == "" {
			t.Errorf("migration %04d has no down script", m.version)
		}
	}
}

func TestInsertBatchDedupes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	r := rec("msg-1")
	added, updated, err := s.InsertBatch(ctx, []model.UsageRecord{r})
	if err != nil || added != 1 || updated != 0 {
		t.Fatalf("first insert: added=%d updated=%d err=%v", added, updated, err)
	}

	// Same record again: one row, nothing added, nothing updated.
	added, updated, err = s.InsertBatch(ctx, []model.UsageRecord{r})
	if err != nil || added != 0 || updated != 0 {
		t.Fatalf("identical re-insert: added=%d updated=%d err=%v", added, updated, err)
	}
	tot, err := s.Totals(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if tot.RecordCount != 1 || tot.InputTokens != 100 || tot.OutputTokens != 50 {
		t.Fatalf("double counting: %+v", tot)
	}
}

func TestInsertBatchLastValueWins(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	cost := 0.25
	r := rec("msg-1")
	if _, _, err := s.InsertBatch(ctx, []model.UsageRecord{r}); err != nil {
		t.Fatal(err)
	}

	r.OutputTokens = 80 // streaming message: later line has the final count
	r.ReportedCostUSD = &cost
	r.IsSidechain = true
	added, updated, err := s.InsertBatch(ctx, []model.UsageRecord{r})
	if err != nil || added != 0 || updated != 1 {
		t.Fatalf("changed re-insert: added=%d updated=%d err=%v", added, updated, err)
	}

	got, ok, err := s.GetUsageRecord(ctx, r.Source, r.ExternalID)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.OutputTokens != 80 || !got.IsSidechain || got.ReportedCostUSD == nil || *got.ReportedCostUSD != 0.25 {
		t.Fatalf("last value did not win: %+v", got)
	}

	// Going back to a nil cost counts as a change too.
	r.ReportedCostUSD = nil
	_, updated, err = s.InsertBatch(ctx, []model.UsageRecord{r})
	if err != nil || updated != 1 {
		t.Fatalf("nil cost update: updated=%d err=%v", updated, err)
	}
	got, _, _ = s.GetUsageRecord(ctx, r.Source, r.ExternalID)
	if got.ReportedCostUSD != nil {
		t.Fatalf("cost should be nil, got %v", *got.ReportedCostUSD)
	}
}

func TestInsertBatchDuplicatesWithinBatchAndSources(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	a := rec("same-id")
	b := rec("same-id")
	b.OutputTokens = 70
	other := rec("same-id")
	other.Source = model.SourceCodexCLI // same external id, different source = different row

	added, updated, err := s.InsertBatch(ctx, []model.UsageRecord{a, b, other})
	if err != nil || added != 2 || updated != 1 {
		t.Fatalf("added=%d updated=%d err=%v", added, updated, err)
	}
	tot, _ := s.Totals(ctx, Filter{})
	if tot.RecordCount != 2 || tot.OutputTokens != 70+50 {
		t.Fatalf("totals: %+v", tot)
	}
}

func TestInsertBatchValidationWritesNothing(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	bad := rec("bad")
	bad.InputTokens = -1
	noID := rec("")
	badSource := rec("x")
	badSource.Source = "nope"
	noTime := rec("t")
	noTime.Timestamp = time.Time{}

	for name, b := range map[string]model.UsageRecord{
		"negative tokens": bad, "empty id": noID, "bad source": badSource, "zero time": noTime,
	} {
		added, updated, err := s.InsertBatch(ctx, []model.UsageRecord{rec("good"), b})
		if err == nil || added != 0 || updated != 0 {
			t.Errorf("%s: added=%d updated=%d err=%v", name, added, updated, err)
		}
	}
	tot, _ := s.Totals(ctx, Filter{})
	if tot.RecordCount != 0 {
		t.Fatalf("invalid batch wrote %d rows", tot.RecordCount)
	}
}

func TestInsertBatchEmpty(t *testing.T) {
	s := openTestStore(t)
	added, updated, err := s.InsertBatch(context.Background(), nil)
	if err != nil || added != 0 || updated != 0 {
		t.Fatalf("added=%d updated=%d err=%v", added, updated, err)
	}
}

func TestInsertBatchConcurrentWriters(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			batch := make([]model.UsageRecord, 0, 20)
			for i := 0; i < 20; i++ {
				batch = append(batch, rec("msg-"+string(rune('a'+i))))
			}
			if _, _, err := s.InsertBatch(ctx, batch); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	tot, err := s.Totals(ctx, Filter{})
	if err != nil || tot.RecordCount != 20 {
		t.Fatalf("RecordCount = %d, err=%v; want 20", tot.RecordCount, err)
	}
}

func TestGetUsageRecordRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	r := rec("rt")
	r.CacheReadTokens, r.CacheWrite5mTokens, r.CacheWrite1hTokens, r.ThinkingTokens = 1, 2, 3, 4
	r.Timestamp = time.Date(2026, 3, 1, 10, 0, 0, 123_000_000, time.FixedZone("x", 3*3600))
	if _, _, err := s.InsertBatch(ctx, []model.UsageRecord{r}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetUsageRecord(ctx, r.Source, r.ExternalID)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !got.Timestamp.Equal(r.Timestamp) || got.Timestamp.Location() != time.UTC {
		t.Fatalf("timestamp %v, want %v in UTC", got.Timestamp, r.Timestamp)
	}
	got.Timestamp = r.Timestamp
	if got != r {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, r)
	}
	if _, ok, _ := s.GetUsageRecord(ctx, r.Source, "missing"); ok {
		t.Fatal("missing record reported as found")
	}
}

func TestIngestState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, ok, err := s.GetIngestState(ctx, model.SourceClaudeCode, "/a.jsonl"); err != nil || ok {
		t.Fatalf("unknown state: ok=%v err=%v", ok, err)
	}
	mtime := time.Date(2026, 3, 1, 10, 0, 0, 123456789, time.UTC)
	st := IngestState{Source: model.SourceClaudeCode, Path: "/a.jsonl", Offset: 10, Size: 20, ModTime: mtime}
	if err := s.SetIngestState(ctx, st); err != nil {
		t.Fatal(err)
	}
	st.Offset, st.Size = 30, 40
	if err := s.SetIngestState(ctx, st); err != nil { // replace
		t.Fatal(err)
	}
	if err := s.SetIngestState(ctx, IngestState{Source: model.SourceClaudeCode, Path: "/b.jsonl"}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.GetIngestState(ctx, model.SourceClaudeCode, "/a.jsonl")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Offset != 30 || got.Size != 40 || !got.ModTime.Equal(mtime) || got.UpdatedAt.IsZero() {
		t.Fatalf("state: %+v", got)
	}
	list, err := s.ListIngestStates(ctx, model.SourceClaudeCode)
	if err != nil || len(list) != 2 || list[0].Path != "/a.jsonl" || list[1].Path != "/b.jsonl" {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	if other, _ := s.ListIngestStates(ctx, model.SourceCodexCLI); len(other) != 0 {
		t.Fatalf("other source leaked: %+v", other)
	}
	if err := s.SetIngestState(ctx, IngestState{Source: "bogus", Path: "/x"}); err == nil {
		t.Fatal("invalid source accepted")
	}
}

func TestSettings(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	type conf struct {
		Theme string   `json:"theme"`
		Tags  []string `json:"tags"`
	}
	var got conf
	if ok, err := s.GetSetting(ctx, "ui", &got); err != nil || ok {
		t.Fatalf("unset key: ok=%v err=%v", ok, err)
	}
	if err := s.SetSetting(ctx, "ui", conf{Theme: "dark", Tags: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "ui", conf{Theme: "light"}); err != nil { // overwrite
		t.Fatal(err)
	}
	if ok, err := s.GetSetting(ctx, "ui", &got); err != nil || !ok || got.Theme != "light" || got.Tags != nil {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
	var n int
	if err := s.SetSetting(ctx, "count", 42); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.GetSetting(ctx, "count", &n); err != nil || !ok || n != 42 {
		t.Fatalf("n=%d ok=%v err=%v", n, ok, err)
	}
	if err := s.DeleteSetting(ctx, "count"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.GetSetting(ctx, "count", &n); ok {
		t.Fatal("deleted setting still present")
	}
	if err := s.SetSetting(ctx, "", 1); err == nil {
		t.Fatal("empty key accepted")
	}
}

func TestSchemaConstraints(t *testing.T) {
	s := openTestStore(t)

	bad := []string{
		`INSERT INTO limit_windows (service, kind, token_limit, metric, updated_at) VALUES ('anthropic', 'daily', 1, 'tokens', 0)`,
		`INSERT INTO budgets (scope, period, threshold_tokens) VALUES ('project', 'daily', 1)`,
		`INSERT INTO budgets (scope, period) VALUES ('total', 'daily')`,                              // no threshold
		`INSERT INTO notifications_sent (budget_id, period_key, sent_at) VALUES (999, '2026-03', 0)`, // FK
		`INSERT INTO settings (key, value) VALUES ('k', 'not json')`,
	}
	for _, q := range bad {
		if _, err := s.db.Exec(q); err == nil {
			t.Errorf("constraint not enforced: %s", q)
		}
	}

	if _, err := s.db.Exec(`INSERT INTO budgets (scope, period, threshold_usd) VALUES ('total', 'monthly', 10)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO notifications_sent (budget_id, period_key, sent_at) VALUES (1, '2026-03', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO notifications_sent (budget_id, period_key, sent_at) VALUES (1, '2026-03', 1)`); err == nil {
		t.Error("duplicate (budget_id, period_key) accepted")
	}
	if _, err := s.db.Exec(`DELETE FROM budgets WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM notifications_sent`).Scan(&n); err != nil || n != 0 {
		t.Errorf("cascade delete failed: n=%d err=%v", n, err)
	}
}

func TestOpenRejectsBadPath(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(ctx, ""); err == nil {
		t.Error("empty path accepted")
	}
	if _, err := Open(ctx, filepath.Join(t.TempDir(), "a?b.db")); err == nil {
		t.Error("path with '?' accepted")
	}
	if _, err := Open(ctx, filepath.Join(t.TempDir(), "missing-dir", "x", "t.db")); err == nil {
		t.Error("nonexistent parent directory accepted")
	}
}

func TestIngestStateZeroModTime(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.SetIngestState(ctx, IngestState{Source: model.SourceClaudeCode, Path: "/z.jsonl", Offset: 1}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetIngestState(ctx, model.SourceClaudeCode, "/z.jsonl")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !got.ModTime.IsZero() {
		t.Fatalf("zero ModTime came back as %v", got.ModTime)
	}
	list, err := s.ListIngestStates(ctx, model.SourceClaudeCode)
	if err != nil || len(list) != 1 || !list[0].ModTime.IsZero() {
		t.Fatalf("list: %+v err=%v", list, err)
	}
}

func TestMigrateTotalOnlyKeepsExistingRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Step back to schema 1 (before the total-only columns), write a row the
	// way an old build would, then migrate up again.
	if err := migrateDown(ctx, s.db, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO usage_records (source, external_id, ts, input_tokens, request_count)
		VALUES ('claude_code', 'old', 1000, 77, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateUp(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetUsageRecord(ctx, model.SourceClaudeCode, "old")
	if err != nil || !found {
		t.Fatalf("old row lost: found=%v err=%v", found, err)
	}
	if got.InputTokens != 77 || got.TotalOnly || got.TotalOnlyTokens != 0 {
		t.Fatalf("old row = %+v, want input 77 and not total-only", got)
	}
}

func totalOnlyRec(id string, tokens int64) model.UsageRecord {
	r := rec(id)
	r.Source = model.SourceCodexCLI
	r.Model = "gpt-test"
	r.InputTokens, r.OutputTokens = 0, 0
	r.TotalOnly, r.TotalOnlyTokens = true, tokens
	return r
}

func TestTotalOnlyRecordRoundTripAndAggregation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	split := rec("split") // 100 in + 50 out, claude-sonnet
	split.Source = model.SourceCodexCLI
	split.Model = "gpt-test"
	if _, _, err := s.InsertBatch(ctx, []model.UsageRecord{totalOnlyRec("a", 1000), totalOnlyRec("b", 500), split}); err != nil {
		t.Fatal(err)
	}

	got, found, err := s.GetUsageRecord(ctx, model.SourceCodexCLI, "a")
	if err != nil || !found || !got.TotalOnly || got.TotalOnlyTokens != 1000 || got.InputTokens != 0 {
		t.Fatalf("Get = %+v found=%v err=%v", got, found, err)
	}
	var each []model.UsageRecord
	if err := s.EachUsageRecord(ctx, Filter{}, func(r model.UsageRecord) error { each = append(each, r); return nil }); err != nil {
		t.Fatal(err)
	}
	var sawA bool
	for _, r := range each {
		if r.ExternalID == "a" {
			sawA = r.TotalOnly && r.TotalOnlyTokens == 1000
		}
	}
	if !sawA {
		t.Fatalf("EachUsageRecord lost the total-only fields: %+v", each)
	}

	// Upsert replaces the count; an identical write changes nothing.
	if added, updated, err := s.InsertBatch(ctx, []model.UsageRecord{totalOnlyRec("a", 1000)}); err != nil || added != 0 || updated != 0 {
		t.Fatalf("identical rewrite: added=%d updated=%d err=%v", added, updated, err)
	}
	if added, updated, err := s.InsertBatch(ctx, []model.UsageRecord{totalOnlyRec("a", 1200)}); err != nil || added != 0 || updated != 1 {
		t.Fatalf("changed rewrite: added=%d updated=%d err=%v", added, updated, err)
	}

	tot, err := s.Totals(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if tot.TotalOnlyTokens != 1700 || tot.TotalOnlyRecords != 2 || tot.InputTokens != 100 || tot.OutputTokens != 50 || tot.TotalTokens() != 1850 {
		t.Fatalf("totals = %+v", tot)
	}
	rows, err := s.Breakdown(ctx, Filter{}, DimModel)
	if err != nil || len(rows) != 1 || rows[0].TotalTokens() != 1850 || rows[0].TotalOnlyRecords != 2 {
		t.Fatalf("breakdown = %+v err=%v", rows, err)
	}
	series, err := s.TimeSeries(ctx, Filter{}, BucketDay, nil)
	if err != nil || len(series) == 0 || series[0].TotalTokens() != 1850 || series[0].TotalOnlyTokens != 1700 {
		t.Fatalf("time series = %+v err=%v", series, err)
	}

	// Cost inputs: only the split record is priced; total-only records are
	// counted separately and never land in the priced counters.
	ci, err := s.CostInputs(ctx, Filter{}, 100_000)
	if err != nil || len(ci) != 1 {
		t.Fatalf("cost inputs = %+v err=%v", ci, err)
	}
	c := ci[0]
	if c.UnreportedRecords != 1 || c.InputTokens != 100 || c.OutputTokens != 50 || c.TotalOnlyRecords != 2 || c.TotalOnlyTokens != 1700 {
		t.Fatalf("cost input = %+v", c)
	}
}

func TestUpdateManualRecordKeepsTotalOnlyColumns(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	r := rec("m1")
	r.Source = model.SourceManual
	if _, _, err := s.InsertBatch(ctx, []model.UsageRecord{r}); err != nil {
		t.Fatal(err)
	}
	r.InputTokens = 5
	if found, err := s.UpdateManualRecord(ctx, r); err != nil || !found {
		t.Fatalf("UpdateManualRecord: found=%v err=%v", found, err)
	}
	got, _, _ := s.GetUsageRecord(ctx, model.SourceManual, "m1")
	if got.InputTokens != 5 || got.TotalOnly {
		t.Fatalf("record = %+v", got)
	}
}

func TestMigration2ClearsOnlyCodexReadPositions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := migrateDown(ctx, s.db, 1); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{"codex_cli", "claude_code"} {
		if _, err := s.db.Exec(`INSERT INTO ingest_state (source, path, "offset", size, mtime, updated_at)
			VALUES (?, '/x', 10, 10, 1, 1)`, src); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateUp(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	for src, wantRows := range map[string]int{"codex_cli": 0, "claude_code": 1} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ingest_state WHERE source = ?`, src).Scan(&n); err != nil || n != wantRows {
			t.Errorf("ingest_state rows for %s = %d (err %v), want %d", src, n, err, wantRows)
		}
	}
}

func TestMigrateDownRemovesTotalOnlyRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, _, err := s.InsertBatch(ctx, []model.UsageRecord{totalOnlyRec("a", 100), rec("b")}); err != nil {
		t.Fatal(err)
	}
	if err := migrateDown(ctx, s.db, 1); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows after down = %d (err %v), want 1 (the split record)", n, err)
	}
}
