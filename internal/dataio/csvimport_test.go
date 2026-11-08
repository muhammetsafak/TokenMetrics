package dataio

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

func importFile(t *testing.T, s *store.Store, name string) (ImportResult, error) {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return ImportCSV(context.Background(), s, f, CSVImportOptions{DeviceID: "dev-local"})
}

func TestImportCSVValid(t *testing.T) {
	s := openStore(t)
	res, err := importFile(t, s, "valid.csv")
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}
	if res.Rows != 4 || res.Added != 4 || res.ErrorCount != 0 {
		t.Fatalf("result = %+v", res)
	}
	tot := mustTotals(t, s, store.Filter{Sources: []model.Source{model.SourceCSVImport}})
	if tot.RecordCount != 4 || tot.InputTokens != 3300 || tot.OutputTokens != 1300 ||
		tot.CacheReadTokens != 200 || tot.CacheWrite5mTokens != 150 {
		t.Errorf("totals = %+v", tot)
	}
	if tot.CostReportedRecords != 2 || tot.ReportedCostUSD < 1.5122 || tot.ReportedCostUSD > 1.5124 {
		t.Errorf("cost totals = %+v", tot)
	}

	// Offset timestamps are normalized to UTC; quoted commas survive.
	var found bool
	err = s.EachUsageRecord(context.Background(), store.Filter{Services: []string{"openai"}}, func(r model.UsageRecord) error {
		found = true
		want := time.Date(2026, 3, 1, 8, 30, 0, 0, time.UTC)
		if !r.Timestamp.Equal(want) || r.Project != "my, project" || r.DeviceID != "dev-local" ||
			r.Source != model.SourceCSVImport || r.ReportedCostUSD != nil {
			t.Errorf("openai row = %+v", r)
		}
		return nil
	})
	if err != nil || !found {
		t.Fatalf("openai row: found=%v err=%v", found, err)
	}
}

func TestImportCSVIdempotent(t *testing.T) {
	s := openStore(t)
	first, err := importFile(t, s, "valid.csv")
	if err != nil {
		t.Fatal(err)
	}
	before := mustTotals(t, s, store.Filter{})

	second, err := importFile(t, s, "valid.csv")
	if err != nil {
		t.Fatal(err)
	}
	if second.Added != 0 || second.Updated != 0 || second.Unchanged != first.Added {
		t.Errorf("second import = %+v", second)
	}
	if after := mustTotals(t, s, store.Filter{}); after != before {
		t.Errorf("totals changed on re-import: %+v -> %+v", before, after)
	}
	if before.RecordCount != 4 {
		t.Errorf("RecordCount = %d, want 4", before.RecordCount)
	}
}

func TestImportCSVSemicolonAndBOM(t *testing.T) {
	s := openStore(t)
	res, err := importFile(t, s, "semicolon_bom.csv")
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}
	if res.Added != 2 || res.ErrorCount != 0 {
		t.Fatalf("result = %+v", res)
	}
	var proj string
	_ = s.EachUsageRecord(context.Background(), store.Filter{Services: []string{"openai"}}, func(r model.UsageRecord) error {
		proj = r.Project
		return nil
	})
	if proj != "a;b" {
		t.Errorf("project = %q, want a;b", proj)
	}

	// The same data with commas hashes to the same ids: no new rows.
	comma := "timestamp,service,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,project,session,cost_usd\n" +
		"2026-03-01T10:00:00Z,anthropic,claude-sonnet,1000,500,200,100,app,s1,0.0123\n"
	res, err = ImportCSV(context.Background(), s, strings.NewReader(comma), CSVImportOptions{DeviceID: "dev-local"})
	if err != nil || res.Added != 0 || res.Unchanged != 1 {
		t.Errorf("comma re-import: %+v, %v", res, err)
	}
}

func TestImportCSVRowErrors(t *testing.T) {
	s := openStore(t)
	res, err := importFile(t, s, "errors.csv")
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}
	if res.Rows != 9 || res.Added != 2 || res.ErrorCount != 7 {
		t.Fatalf("result = %+v", res)
	}
	want := map[int]string{
		3:  "timestamp",
		4:  "input_tokens must not be negative",
		6:  "service is required",
		7:  "at least one token",
		8:  "not a whole number",
		9:  "expected 10 columns, found 5",
		10: "cost_usd must not be negative",
	}
	if len(res.Errors) != len(want) {
		t.Fatalf("errors = %+v", res.Errors)
	}
	for _, e := range res.Errors {
		sub, ok := want[e.Line]
		if !ok {
			t.Errorf("unexpected error on line %d: %s", e.Line, e.Reason)
			continue
		}
		if !strings.Contains(e.Reason, sub) {
			t.Errorf("line %d reason = %q, want it to contain %q", e.Line, e.Reason, sub)
		}
	}
	if n := mustTotals(t, s, store.Filter{}).RecordCount; n != 2 {
		t.Errorf("stored %d rows, want only the 2 valid ones", n)
	}
}

func TestImportCSVMalformedQuote(t *testing.T) {
	s := openStore(t)
	data := "timestamp,service,model,input_tokens\n" +
		"2026-03-01T10:00:00Z,ac\"me,m1,5\n" +
		"2026-03-01T11:00:00Z,acme,m1,6\n"
	res, err := ImportCSV(context.Background(), s, strings.NewReader(data), CSVImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.ErrorCount != 1 || res.Errors[0].Line != 2 {
		t.Errorf("result = %+v", res)
	}
}

func TestImportCSVBadHeader(t *testing.T) {
	s := openStore(t)
	cases := map[string]string{
		"missing required": "timestamp,service,input_tokens\n2026-03-01,a,1\n",
		"unknown column":   "timestamp,service,model,input_token\n2026-03-01,a,m,1\n",
		"duplicate":        "timestamp,service,model,model\n2026-03-01,a,m,m\n",
		"no header":        "2026-03-01T10:00:00Z,anthropic,claude,10\n",
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ImportCSV(context.Background(), s, strings.NewReader(data), CSVImportOptions{})
			if !errors.Is(err, ErrInvalidHeader) {
				t.Errorf("err = %v, want ErrInvalidHeader", err)
			}
		})
	}
	if _, err := ImportCSV(context.Background(), s, strings.NewReader(" \n"), CSVImportOptions{}); !errors.Is(err, ErrEmptyFile) {
		t.Errorf("empty: err = %v, want ErrEmptyFile", err)
	}
	if n := mustTotals(t, s, store.Filter{}).RecordCount; n != 0 {
		t.Errorf("bad files stored %d rows", n)
	}
}

func TestImportCSVHeaderOrderAndCase(t *testing.T) {
	s := openStore(t)
	data := "Model, SERVICE ,Timestamp,output_tokens\nm1,acme,2026-03-01T10:00:00Z,9\n"
	res, err := ImportCSV(context.Background(), s, strings.NewReader(data), CSVImportOptions{})
	if err != nil || res.Added != 1 {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	if tot := mustTotals(t, s, store.Filter{}); tot.OutputTokens != 9 {
		t.Errorf("totals = %+v", tot)
	}
}

func TestImportCSVSizeLimit(t *testing.T) {
	s := openStore(t)
	var b bytes.Buffer
	b.WriteString("timestamp,service,model,input_tokens\n")
	b.Write(bytes.Repeat([]byte("x"), MaxCSVImportBytes))
	_, err := ImportCSV(context.Background(), s, &b, CSVImportOptions{})
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("err = %v, want ErrFileTooLarge", err)
	}
}

func TestImportCSVLimits(t *testing.T) {
	s := openStore(t)
	data := "timestamp,service,model,input_tokens,cost_usd\n" +
		"2026-03-01T10:00:00Z,a,m,99999999999999999999,\n" + // overflows int64
		"2026-03-01T10:00:00Z,a,m,2000000000000000,\n" + // above maxTokens
		"2026-03-01T10:00:00Z,a,m,1,NaN\n" +
		"2026-03-01T10:00:00Z,a,m,1,1e12\n" +
		"2999-01-01T10:00:00Z,a,m,1,\n" // far future
	res, err := ImportCSV(context.Background(), s, strings.NewReader(data), CSVImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.ErrorCount != 5 {
		t.Errorf("result = %+v", res)
	}
}

func TestImportCSVTextFormulaPrefixRoundTrips(t *testing.T) {
	s := openStore(t)
	data := "timestamp,service,model,input_tokens,project\n" +
		"2026-03-01T10:00:00Z,acme,m1,5,'=SUM(A1)\n" +
		"2026-03-01T10:00:00Z,acme,m1,5,'plain\n"
	if _, err := ImportCSV(context.Background(), s, strings.NewReader(data), CSVImportOptions{}); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	_ = s.EachUsageRecord(context.Background(), store.Filter{}, func(r model.UsageRecord) error {
		got[r.Project] = true
		return nil
	})
	if !got["=SUM(A1)"] || !got["'plain"] || len(got) != 2 {
		t.Errorf("projects = %v", got)
	}
}

// Files written before the total_only_tokens column existed (the 10-column
// header of valid.csv) stay valid, import as split records, and keep the row
// ids they always had, so re-importing an old file after an upgrade adds nothing.
func TestImportCSVLegacyHeaderStaysValid(t *testing.T) {
	s := openStore(t)
	res, err := importFile(t, s, "valid.csv")
	if err != nil || res.Added != 4 || res.ErrorCount != 0 {
		t.Fatalf("legacy import: %+v, %v", res, err)
	}
	tot := mustTotals(t, s, store.Filter{})
	if tot.TotalOnlyRecords != 0 || tot.TotalOnlyTokens != 0 {
		t.Fatalf("legacy rows became total-only: %+v", tot)
	}
	// The id is the SHA-256 of the normalized first row, computed outside this
	// code from the rule in docs/csv-format.md.
	const firstRowID = "b48d5d9b4c26bf11a5ce2fb0cb7a24817c2e0577e192ad59a85d03ab53245438"
	if _, found, err := s.GetUsageRecord(context.Background(), model.SourceCSVImport, firstRowID); err != nil || !found {
		t.Fatalf("legacy row id changed: found=%v err=%v", found, err)
	}
	if res, err := importFile(t, s, "valid.csv"); err != nil || res.Added != 0 || res.Updated != 0 {
		t.Fatalf("second legacy import: %+v, %v", res, err)
	}
}

func TestImportCSVTotalOnlyColumn(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	// The column is optional per row too: empty means 0.
	data := "Timestamp;SERVICE;model;input_tokens;total_only_tokens\n" +
		"2026-03-01T10:00:00Z;openai;gpt-5.5;;9000\n" +
		"2026-03-01T11:00:00Z;openai;gpt-5.5;100;\n" +
		"2026-03-01T12:00:00Z;openai;gpt-5.5;100;50\n" + // mixed: rejected
		"2026-03-01T13:00:00Z;openai;gpt-5.5;;-1\n" // negative: rejected
	res, err := ImportCSV(ctx, s, strings.NewReader(data), CSVImportOptions{DeviceID: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows != 4 || res.Added != 2 || res.ErrorCount != 2 {
		t.Fatalf("result = %+v", res)
	}
	var totalOnly, split int
	_ = s.EachUsageRecord(ctx, store.Filter{}, func(r model.UsageRecord) error {
		if r.TotalOnly {
			totalOnly++
			if r.TotalOnlyTokens != 9000 || r.InputTokens != 0 || r.Source != model.SourceCSVImport || r.TotalTokens() != 9000 {
				t.Errorf("total-only row = %+v", r)
			}
		} else {
			split++
		}
		return nil
	})
	if totalOnly != 1 || split != 1 {
		t.Fatalf("total-only rows %d, split rows %d; want 1 and 1", totalOnly, split)
	}
	// The same file again changes nothing (the row hash covers the new column).
	if res, _ := ImportCSV(ctx, s, strings.NewReader(data), CSVImportOptions{DeviceID: "d"}); res.Added != 0 || res.Updated != 0 {
		t.Fatalf("second import = %+v", res)
	}
	// A different count is a different row.
	other := "timestamp,service,model,total_only_tokens\n2026-03-01T10:00:00Z,openai,gpt-5.5,9001\n"
	if res, _ := ImportCSV(ctx, s, strings.NewReader(other), CSVImportOptions{}); res.Added != 1 {
		t.Fatalf("changed count = %+v, want Added 1", res)
	}
}

func TestImportCSVTotalOnlyFile(t *testing.T) {
	s := openStore(t)
	res, err := importFile(t, s, "total_only.csv")
	if err != nil || res.Added != 1 || res.ErrorCount != 0 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if tot := mustTotals(t, s, store.Filter{}); tot.TotalOnlyTokens != 9000 || tot.TotalTokens() != 9000 {
		t.Fatalf("totals = %+v", tot)
	}
}

// The example in docs/csv-format.md must import as written.
func TestDocsExampleImports(t *testing.T) {
	doc, err := os.ReadFile("../../docs/csv-format.md")
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(doc), "## Example\n\n```\n")
	if !ok {
		t.Fatal("example block not found in docs/csv-format.md")
	}
	example, _, _ := strings.Cut(after, "```")
	s := openStore(t)
	res, err := ImportCSV(context.Background(), s, strings.NewReader(example), CSVImportOptions{DeviceID: "d"})
	if err != nil || res.Rows != 4 || res.Added != 4 || res.ErrorCount != 0 {
		t.Fatalf("doc example: %+v, %v", res, err)
	}
	if tot := mustTotals(t, s, store.Filter{}); tot.TotalOnlyTokens != 1_500_000 || tot.TotalOnlyRecords != 1 {
		t.Fatalf("doc example totals = %+v", tot)
	}
}
