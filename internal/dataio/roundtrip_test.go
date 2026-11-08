package dataio

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

// fixture mixes sources, devices, cache write buckets, nil and zero costs, and
// text that looks like a spreadsheet formula.
func fixture() []model.UsageRecord {
	ts := func(h int) time.Time { return time.Date(2026, 3, 1, h, 0, 0, 123_000_000, time.UTC) }
	return []model.UsageRecord{
		{Source: model.SourceClaudeCode, ExternalID: "msg-1", DeviceID: "dev-a", Timestamp: ts(9),
			Service: "anthropic", Model: "claude-sonnet", Project: "/work/app", Session: "s1",
			InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 200,
			CacheWrite5mTokens: 100, CacheWrite1hTokens: 40, ThinkingTokens: 30,
			RequestCount: 1, IsSidechain: true},
		{Source: model.SourceCodexCLI, ExternalID: "msg-2", DeviceID: "dev-a", Timestamp: ts(10),
			Service: "openai", Model: "gpt-5", Project: "=cmd|' /C calc'!A0", Session: "+1",
			InputTokens: 2000, OutputTokens: 800, RequestCount: 1, ReportedCostUSD: f64(0.25)},
		{Source: model.SourceManual, ExternalID: "m-1", DeviceID: "dev-a", Timestamp: ts(11),
			Service: "acme, inc", Model: "-model", Project: "@proj", Session: "line\nbreak \"q\"",
			InputTokens: 5, ReportedCostUSD: f64(0)},
		{Source: model.SourceAnthropicAPI, ExternalID: "api-1", DeviceID: "dev-b", Timestamp: ts(12),
			Service: "anthropic", Model: "claude-opus", InputTokens: 7, OutputTokens: 3, RequestCount: 2},
	}
}

func seed(t *testing.T, s *store.Store, recs []model.UsageRecord) {
	t.Helper()
	if _, _, err := s.InsertBatch(context.Background(), recs); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := openStore(t)
	seed(t, src, fixture())

	var buf bytes.Buffer
	if err := ExportJSON(ctx, src, &buf, store.Filter{}, "dev-a"); err != nil {
		t.Fatalf("ExportJSON: %v", err)
	}
	var doc struct {
		SchemaVersion int               `json:"schema_version"`
		DeviceID      string            `json:"device_id"`
		Records       []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("export is not valid JSON: %v", err)
	}
	if doc.SchemaVersion != ExportSchemaVersion || doc.DeviceID != "dev-a" || len(doc.Records) != 4 {
		t.Fatalf("export header = %+v", doc)
	}
	if !strings.Contains(buf.String(), `"device_id":"dev-b"`) {
		t.Error("per-record device_id missing from export")
	}

	dst := openStore(t)
	res, err := ImportJSON(ctx, dst, bytes.NewReader(buf.Bytes()), "dev-importer")
	if err != nil {
		t.Fatalf("ImportJSON: %v", err)
	}
	if res.Rows != 4 || res.Added != 4 || res.ErrorCount != 0 {
		t.Fatalf("result = %+v", res)
	}
	if a, b := mustTotals(t, src, store.Filter{}), mustTotals(t, dst, store.Filter{}); a != b {
		t.Errorf("Totals differ after round trip:\n src %+v\n dst %+v", a, b)
	}

	// Identity of each record (source, external id, device) is preserved.
	for _, want := range fixture() {
		got, ok, err := dst.GetUsageRecord(ctx, want.Source, want.ExternalID)
		if err != nil || !ok {
			t.Fatalf("%s/%s missing after import (err %v)", want.Source, want.ExternalID, err)
		}
		if got.DeviceID != want.DeviceID || !got.Timestamp.Equal(want.Timestamp) ||
			got.CacheWrite1hTokens != want.CacheWrite1hTokens || got.IsSidechain != want.IsSidechain ||
			got.Project != want.Project || got.Session != want.Session ||
			(got.ReportedCostUSD == nil) != (want.ReportedCostUSD == nil) {
			t.Errorf("record %s differs:\n got  %+v\n want %+v", want.ExternalID, got, want)
		}
	}

	// Importing the same file again changes nothing.
	res, err = ImportJSON(ctx, dst, bytes.NewReader(buf.Bytes()), "dev-importer")
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.Updated != 0 || res.Unchanged != 4 {
		t.Errorf("re-import = %+v", res)
	}
	if n := mustTotals(t, dst, store.Filter{}).RecordCount; n != 4 {
		t.Errorf("RecordCount = %d, want 4", n)
	}
}

func TestJSONMergesDevices(t *testing.T) {
	ctx := context.Background()
	all := fixture()
	devA, devB := openStore(t), openStore(t)
	seed(t, devA, all[:2])
	seed(t, devB, all[2:])

	var buf bytes.Buffer
	if err := ExportJSON(ctx, devB, &buf, store.Filter{}, "dev-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportJSON(ctx, devA, &buf, "dev-a"); err != nil {
		t.Fatal(err)
	}
	merged := openStore(t)
	seed(t, merged, all)
	if a, b := mustTotals(t, devA, store.Filter{}), mustTotals(t, merged, store.Filter{}); a != b {
		t.Errorf("merged totals differ:\n got  %+v\n want %+v", a, b)
	}
}

func TestExportJSONFilter(t *testing.T) {
	ctx := context.Background()
	src := openStore(t)
	seed(t, src, fixture())

	f := store.Filter{
		From:    time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		To:      time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Sources: []model.Source{model.SourceCodexCLI, model.SourceManual, model.SourceAnthropicAPI},
	}
	var buf bytes.Buffer
	if err := ExportJSON(ctx, src, &buf, f, "dev-a"); err != nil {
		t.Fatal(err)
	}
	dst := openStore(t)
	if _, err := ImportJSON(ctx, dst, &buf, "dev"); err != nil {
		t.Fatal(err)
	}
	if a, b := mustTotals(t, src, f), mustTotals(t, dst, store.Filter{}); a != b || a.RecordCount != 2 {
		t.Errorf("filtered export mismatch:\n src %+v\n dst %+v", a, b)
	}
}

func TestExportEmpty(t *testing.T) {
	ctx := context.Background()
	src := openStore(t)
	var buf bytes.Buffer
	if err := ExportJSON(ctx, src, &buf, store.Filter{}, "d"); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(buf.Bytes()) {
		t.Fatalf("empty export is not valid JSON: %s", buf.String())
	}
	res, err := ImportJSON(ctx, openStore(t), &buf, "d")
	if err != nil || res.Rows != 0 {
		t.Errorf("empty import: %+v, %v", res, err)
	}
}

func TestImportJSONSchemaAndShape(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	rec := `{"source":"manual","external_id":"x","device_id":"d","timestamp":"2026-03-01T10:00:00Z","service":"a","model":"m","input_tokens":1}`

	reject := map[string]struct {
		doc  string
		want error
	}{
		"future version": {`{"schema_version":2,"records":[` + rec + `]}`, ErrUnsupportedSchema},
		"version zero":   {`{"schema_version":0,"records":[]}`, ErrUnsupportedSchema},
		"no version":     {`{"records":[` + rec + `]}`, ErrInvalidExport},
		"version after":  {`{"records":[` + rec + `],"schema_version":1}`, ErrInvalidExport},
		"no records":     {`{"schema_version":1}`, ErrInvalidExport},
		"not an object":  {`[1,2]`, ErrInvalidExport},
		"garbage":        {`nope`, ErrInvalidExport},
		"truncated":      {`{"schema_version":1,"records":[` + rec, ErrInvalidExport},
	}
	for name, c := range reject {
		t.Run(name, func(t *testing.T) {
			_, err := ImportJSON(ctx, s, strings.NewReader(c.doc), "d")
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
	// Records are only written once a whole batch was read, so a rejected
	// (including truncated) small file stores nothing.
	if n := mustTotals(t, s, store.Filter{}).RecordCount; n != 0 {
		t.Errorf("rejected files stored %d records", n)
	}

	// Unknown extra keys are skipped; bad records are reported by position.
	doc := `{"schema_version":1,"note":{"a":[1,2]},"records":[` + rec + `,
		{"source":"nope","external_id":"y","timestamp":"2026-03-01T10:00:00Z","service":"a","model":"m","input_tokens":1},
		{"source":"manual","external_id":"z","timestamp":"2026-03-01T10:00:00Z","service":"a","model":"m","input_tokens":-1},
		{"source":"manual","external_id":"w","timestamp":"garbage","service":"a","model":"m","input_tokens":1},
		{"source":"manual","external_id":"v","timestamp":"2026-03-01T10:00:00Z","service":"a","model":"m","input_tokens":"many"}]}`
	res, err := ImportJSON(ctx, s, strings.NewReader(doc), "local")
	if err != nil {
		t.Fatalf("ImportJSON: %v", err)
	}
	if res.Rows != 5 || res.Added != 1 || res.ErrorCount != 4 {
		t.Fatalf("result = %+v", res)
	}
	for i, e := range res.Errors {
		if e.Line != i+2 {
			t.Errorf("error %d on record %d, want %d", i, e.Line, i+2)
		}
	}
}

func TestImportJSONDeviceDefault(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	doc := `{"schema_version":1,"records":[{"source":"manual","external_id":"x","timestamp":"2026-03-01T10:00:00Z","service":"a","model":"m","input_tokens":1}]}`
	if _, err := ImportJSON(ctx, s, strings.NewReader(doc), "local-dev"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.GetUsageRecord(ctx, model.SourceManual, "x")
	if got.DeviceID != "local-dev" {
		t.Errorf("DeviceID = %q, want local-dev", got.DeviceID)
	}
}

func TestExportExcludesSettings(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seed(t, s, fixture())
	if err := s.SetSetting(ctx, "admin_api_key", "sk-secret-123"); err != nil {
		t.Fatal(err)
	}
	for name, export := range map[string]func(*bytes.Buffer) error{
		"json": func(b *bytes.Buffer) error { return ExportJSON(ctx, s, b, store.Filter{}, "d") },
		"csv":  func(b *bytes.Buffer) error { return ExportCSV(ctx, s, b, store.Filter{}) },
	} {
		var buf bytes.Buffer
		if err := export(&buf); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(buf.String(), "sk-secret-123") || strings.Contains(buf.String(), "admin_api_key") {
			t.Errorf("%s export leaks settings", name)
		}
	}
}

func TestCSVRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := openStore(t)
	seed(t, src, fixture())

	var buf bytes.Buffer
	if err := ExportCSV(ctx, src, &buf, store.Filter{}); err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}
	dst := openStore(t)
	res, err := ImportCSV(ctx, dst, bytes.NewReader(buf.Bytes()), CSVImportOptions{DeviceID: "d"})
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}
	if res.Rows != 4 || res.Added != 4 || res.ErrorCount != 0 {
		t.Fatalf("result = %+v\nCSV:\n%s", res, buf.String())
	}

	a, b := mustTotals(t, src, store.Filter{}), mustTotals(t, dst, store.Filter{})
	// The CSV schema folds both cache write buckets into one column and has no
	// thinking/request columns; everything it does carry must survive.
	if a.RecordCount != b.RecordCount || a.InputTokens != b.InputTokens ||
		a.OutputTokens != b.OutputTokens || a.CacheReadTokens != b.CacheReadTokens ||
		a.CacheWrite5mTokens+a.CacheWrite1hTokens != b.CacheWrite5mTokens+b.CacheWrite1hTokens ||
		a.ReportedCostUSD != b.ReportedCostUSD || a.CostReportedRecords != b.CostReportedRecords {
		t.Errorf("CSV round trip changed totals:\n src %+v\n dst %+v", a, b)
	}

	// Text, including the formula-looking cells, comes back unchanged.
	texts := map[string]bool{}
	_ = dst.EachUsageRecord(ctx, store.Filter{}, func(r model.UsageRecord) error {
		texts[r.Project+"|"+r.Session+"|"+r.Service+"|"+r.Model] = true
		return nil
	})
	for _, want := range fixture() {
		k := want.Project + "|" + want.Session + "|" + want.Service + "|" + want.Model
		if !texts[k] {
			t.Errorf("text of %s lost in CSV round trip: %q", want.ExternalID, k)
		}
	}

	// Exporting the CSV import and importing again is stable.
	var buf2 bytes.Buffer
	if err := ExportCSV(ctx, dst, &buf2, store.Filter{}); err != nil {
		t.Fatal(err)
	}
	res, err = ImportCSV(ctx, dst, &buf2, CSVImportOptions{DeviceID: "d"})
	if err != nil || res.Added != 0 || res.Updated != 0 {
		t.Errorf("second CSV generation: %+v, %v", res, err)
	}
}

func TestCSVExportFormulaEscaping(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seed(t, s, fixture())

	var buf bytes.Buffer
	if err := ExportCSV(ctx, s, &buf, store.Filter{}); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("export is not valid CSV: %v", err)
	}
	if strings.Join(rows[0], ",") != strings.Join(csvColumns, ",") {
		t.Errorf("header = %v", rows[0])
	}
	for _, row := range rows[1:] {
		for i, cell := range row {
			// Numeric columns (3..6, 9, 10) are never text; all other cells must
			// not begin with a formula trigger.
			if i >= 3 && i <= 6 || i >= 9 || cell == "" {
				continue
			}
			if isFormulaTrigger(cell[0]) {
				t.Errorf("cell %q (column %s) can run as a formula", cell, rows[0][i])
			}
		}
	}
	// Spot-check the exact escaped values of the nasty rows.
	text := map[string]bool{}
	for _, row := range rows[1:] {
		text[row[2]+"|"+row[7]+"|"+row[8]] = true
	}
	for _, want := range []string{
		"gpt-5|'=cmd|' /C calc'!A0|'+1",
		"'-model|'@proj|line\nbreak \"q\"",
	} {
		if !text[want] {
			t.Errorf("missing escaped row %q in %v", want, text)
		}
	}
}

func TestEscapeFormula(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"plain":   "plain",
		"=1+1":    "'=1+1",
		"+1":      "'+1",
		"-1":      "'-1",
		"@SUM":    "'@SUM",
		"\tx":     "'\tx",
		"\rx":     "'\rx",
		"a=b":     "a=b",
		"'quoted": "'quoted",
		"'=x":     "''=x",
		"''=x":    "'''=x",
		"'":       "'",
	}
	for in, want := range cases {
		if got := escapeFormula(in); got != want {
			t.Errorf("escapeFormula(%q) = %q, want %q", in, got, want)
		}
		if back := unescapeFormula(escapeFormula(in)); back != in {
			t.Errorf("unescape(escape(%q)) = %q", in, back)
		}
	}
}

// A record a collector could store (long working directory, odd clock) must
// survive export and import on another device.
func TestJSONRoundTripKeepsWhatStoreAccepts(t *testing.T) {
	ctx := context.Background()
	long := "/" + strings.Repeat("deep/", 400) // 2001 characters, over the entry limit
	recs := []model.UsageRecord{
		{Source: model.SourceClaudeCode, ExternalID: strings.Repeat("i", 900), DeviceID: "dev-a",
			Timestamp: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC),
			Service:   "anthropic", Model: "claude-sonnet", Project: long, Session: " padded ",
			InputTokens: 1},
		{Source: model.SourceClaudeCode, ExternalID: "future", DeviceID: "dev-a",
			Timestamp: time.Now().Add(10 * 24 * time.Hour).UTC().Truncate(time.Millisecond),
			Service:   "anthropic", Model: "claude-sonnet", InputTokens: 2},
	}
	src := openStore(t)
	seed(t, src, recs)

	var buf bytes.Buffer
	if err := ExportJSON(ctx, src, &buf, store.Filter{}, "dev-a"); err != nil {
		t.Fatal(err)
	}
	dst := openStore(t)
	res, err := ImportJSON(ctx, dst, &buf, "dev-b")
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 || res.ErrorCount != 0 {
		t.Fatalf("result = %+v", res)
	}
	if a, b := mustTotals(t, src, store.Filter{}), mustTotals(t, dst, store.Filter{}); a != b {
		t.Errorf("Totals differ:\n src %+v\n dst %+v", a, b)
	}
	for _, want := range recs {
		got, ok, _ := dst.GetUsageRecord(ctx, want.Source, want.ExternalID)
		if !ok || got.Project != want.Project || got.Session != want.Session {
			t.Errorf("record %.20s changed by import: %+v", want.ExternalID, got)
		}
	}

	// The wider limits are still limits.
	huge := strings.Repeat("x", maxImportTextLen+1)
	doc := `{"schema_version":1,"records":[{"source":"manual","external_id":"x","timestamp":"2026-03-01T10:00:00Z","service":"a","model":"m","project":"` + huge + `","input_tokens":1}]}`
	res, err = ImportJSON(ctx, dst, strings.NewReader(doc), "d")
	if err != nil || res.Added != 0 || res.ErrorCount != 1 {
		t.Errorf("oversized text: %+v, %v", res, err)
	}
}

// Documents the known limitation from docs/csv-format.md: a CSV export has no
// record identity, so importing it into the store it came from doubles it.
func TestCSVReimportIntoSameStoreDuplicates(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seed(t, s, fixture())

	var buf bytes.Buffer
	if err := ExportCSV(ctx, s, &buf, store.Filter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportCSV(ctx, s, &buf, CSVImportOptions{DeviceID: "d"}); err != nil {
		t.Fatal(err)
	}
	if n := mustTotals(t, s, store.Filter{}).RecordCount; n != 8 {
		t.Errorf("RecordCount = %d, want 8 (4 originals + 4 csv_import copies)", n)
	}
}

func totalOnlyFixture() []model.UsageRecord {
	ts := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	return []model.UsageRecord{
		{Source: model.SourceCodexCLI, ExternalID: "c:1", DeviceID: "dev-a", Timestamp: ts,
			Service: "openai", Model: "gpt-5.5", Session: "s1", RequestCount: 1,
			TotalOnly: true, TotalOnlyTokens: 1_500_000},
		{Source: model.SourceClaudeCode, ExternalID: "m:1", DeviceID: "dev-a", Timestamp: ts.Add(time.Hour),
			Service: "anthropic", Model: "claude-sonnet", Session: "s2", RequestCount: 1,
			InputTokens: 100, OutputTokens: 50},
	}
}

func TestCSVRoundTripKeepsTotalOnlyTokens(t *testing.T) {
	ctx := context.Background()
	src := openStore(t)
	seed(t, src, totalOnlyFixture())

	var buf bytes.Buffer
	if err := ExportCSV(ctx, src, &buf, store.Filter{}); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(buf.Bytes())).ReadAll()
	if err != nil || len(rows) != 3 {
		t.Fatalf("export: %v, %d rows", err, len(rows))
	}
	col := len(csvColumns) - 1
	if rows[0][col] != "total_only_tokens" || rows[1][col] != "1500000" || rows[2][col] != "0" {
		t.Fatalf("total_only_tokens column = %q / %q / %q", rows[0][col], rows[1][col], rows[2][col])
	}

	dst := openStore(t)
	res, err := ImportCSV(ctx, dst, &buf, CSVImportOptions{DeviceID: "d"})
	if err != nil || res.Added != 2 || res.ErrorCount != 0 {
		t.Fatalf("import = %+v, %v", res, err)
	}
	a, b := mustTotals(t, src, store.Filter{}), mustTotals(t, dst, store.Filter{})
	if b.TotalOnlyTokens != 1_500_000 || b.TotalOnlyRecords != 1 || b.TotalTokens() != a.TotalTokens() ||
		b.InputTokens != a.InputTokens || b.OutputTokens != a.OutputTokens {
		t.Fatalf("CSV round trip changed totals:\n src %+v\n dst %+v", a, b)
	}
}

func TestJSONRoundTripKeepsTotalOnlyRecords(t *testing.T) {
	ctx := context.Background()
	src := openStore(t)
	seed(t, src, totalOnlyFixture())

	var buf bytes.Buffer
	if err := ExportJSON(ctx, src, &buf, store.Filter{}, "dev-a"); err != nil {
		t.Fatal(err)
	}
	dst := openStore(t)
	if res, err := ImportJSON(ctx, dst, &buf, "dev-b"); err != nil || res.Added != 2 || res.ErrorCount != 0 {
		t.Fatalf("import = %+v, %v", res, err)
	}
	got, found, err := dst.GetUsageRecord(ctx, model.SourceCodexCLI, "c:1")
	if err != nil || !found || !got.TotalOnly || got.TotalOnlyTokens != 1_500_000 {
		t.Fatalf("record = %+v found=%v err=%v", got, found, err)
	}
}
