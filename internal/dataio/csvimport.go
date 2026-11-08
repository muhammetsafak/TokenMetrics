package dataio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

// MaxCSVImportBytes is the largest CSV file ImportCSV accepts (50 MiB).
const MaxCSVImportBytes = 50 << 20

// CSV column names, in export order. See docs/csv-format.md.
const (
	colTimestamp        = "timestamp"
	colService          = "service"
	colModel            = "model"
	colInputTokens      = "input_tokens"
	colOutputTokens     = "output_tokens"
	colCacheReadTokens  = "cache_read_tokens"
	colCacheWriteTokens = "cache_write_tokens"
	colProject          = "project"
	colSession          = "session"
	colCostUSD          = "cost_usd"
	// colTotalOnlyTokens is optional on import (files written before it
	// existed have none) and always written on export.
	colTotalOnlyTokens = "total_only_tokens"
)

var csvColumns = []string{
	colTimestamp, colService, colModel, colInputTokens, colOutputTokens,
	colCacheReadTokens, colCacheWriteTokens, colProject, colSession, colCostUSD,
	colTotalOnlyTokens,
}

// Errors returned by ImportCSV for whole-file problems. Problems with single
// rows are reported in ImportResult.Errors instead.
var (
	ErrFileTooLarge  = errors.New("dataio: file is too large")
	ErrEmptyFile     = errors.New("dataio: file is empty")
	ErrInvalidHeader = errors.New("dataio: invalid CSV header")
)

// CSVImportOptions configures ImportCSV.
type CSVImportOptions struct {
	// DeviceID is stored on every imported record (the importing device).
	DeviceID string
}

// ImportCSV reads a usage CSV (see docs/csv-format.md) and stores every valid
// row with Source = csv_import. Invalid rows are skipped and listed, with
// their line numbers, in the result. The delimiter (',' or ';') is detected
// from the header line and a UTF-8 BOM is tolerated.
//
// Importing a CSV that ExportCSV wrote from this same store doubles the
// totals (the CSV has no record identity); see ExportCSV.
//
// A record's ExternalID is the SHA-256 of its normalized row, so importing the
// same file again changes nothing. Whole-file problems (too large, empty, bad
// header) return an error and write nothing.
func ImportCSV(ctx context.Context, st *store.Store, r io.Reader, opts CSVImportOptions) (ImportResult, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxCSVImportBytes+1))
	if err != nil {
		return ImportResult{}, fmt.Errorf("dataio: read csv: %w", err)
	}
	if len(data) > MaxCSVImportBytes {
		return ImportResult{}, fmt.Errorf("%w (limit is %d MiB)", ErrFileTooLarge, MaxCSVImportBytes>>20)
	}
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if len(bytes.TrimSpace(data)) == 0 {
		return ImportResult{}, ErrEmptyFile
	}

	cr := csv.NewReader(bytes.NewReader(data))
	cr.Comma = detectDelimiter(data)
	cr.FieldsPerRecord = -1 // the column count is checked per row for a precise message
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return ImportResult{}, fmt.Errorf("%w: %v", ErrInvalidHeader, err)
	}
	idx, err := parseHeader(header)
	if err != nil {
		return ImportResult{}, err
	}

	var res ImportResult
	var records []model.UsageRecord
	for {
		if err := ctx.Err(); err != nil {
			return ImportResult{}, err
		}
		row, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				res.Rows++
				res.addError(pe.StartLine, csvParseReason(pe))
				continue
			}
			return ImportResult{}, fmt.Errorf("dataio: read csv: %w", err)
		}
		line, _ := cr.FieldPos(0)
		res.Rows++
		rec, err := parseRow(row, idx, opts.DeviceID)
		if err != nil {
			res.addError(line, err.Error())
			continue
		}
		records = append(records, rec)
	}

	if len(records) > 0 {
		added, updated, err := st.InsertBatch(ctx, records)
		if err != nil {
			return ImportResult{}, err
		}
		res.Added, res.Updated = added, updated
		res.Unchanged = len(records) - added - updated
	}
	return res, nil
}

// detectDelimiter picks ',' or ';' by counting both outside quotes on the
// header line. A tie (or neither) falls back to ','.
func detectDelimiter(data []byte) rune {
	line := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		line = data[:i]
	}
	var commas, semis int
	inQuotes := false
	for _, b := range line {
		switch {
		case b == '"':
			inQuotes = !inQuotes
		case inQuotes:
		case b == ',':
			commas++
		case b == ';':
			semis++
		}
	}
	if semis > commas {
		return ';'
	}
	return ','
}

// parseHeader maps the schema columns to their positions. Column order is
// free and names are case-insensitive. The required columns must be present;
// unknown or repeated columns are rejected so a typo cannot silently drop data.
func parseHeader(header []string) (map[string]int, error) {
	known := make(map[string]bool, len(csvColumns))
	for _, c := range csvColumns {
		known[c] = true
	}
	idx := make(map[string]int, len(header))
	for i, h := range header {
		name := strings.ToLower(strings.TrimSpace(h))
		if !known[name] {
			return nil, fmt.Errorf("%w: unknown column %q", ErrInvalidHeader, h)
		}
		if _, dup := idx[name]; dup {
			return nil, fmt.Errorf("%w: column %q appears twice", ErrInvalidHeader, name)
		}
		idx[name] = i
	}
	for _, c := range []string{colTimestamp, colService, colModel} {
		if _, ok := idx[c]; !ok {
			return nil, fmt.Errorf("%w: required column %q is missing", ErrInvalidHeader, c)
		}
	}
	return idx, nil
}

func csvParseReason(pe *csv.ParseError) string {
	if pe.Err != nil {
		return "malformed CSV: " + pe.Err.Error()
	}
	return "malformed CSV"
}

// parseRow converts one data row into a record, or explains why it cannot.
func parseRow(row []string, idx map[string]int, deviceID string) (model.UsageRecord, error) {
	if len(row) != len(idx) {
		return model.UsageRecord{}, fmt.Errorf("expected %d columns, found %d", len(idx), len(row))
	}
	get := func(col string) string {
		i, ok := idx[col]
		if !ok {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	text := func(col string) string { return unescapeFormula(get(col)) }

	ts, err := parseTimestamp(get(colTimestamp))
	if err != nil {
		return model.UsageRecord{}, err
	}
	rec := model.UsageRecord{
		Source:    model.SourceCSVImport,
		DeviceID:  deviceID,
		Timestamp: ts,
		Service:   text(colService),
		Model:     text(colModel),
		Project:   text(colProject),
		Session:   text(colSession),
	}
	for _, f := range []struct {
		col string
		dst *int64
	}{
		{colInputTokens, &rec.InputTokens},
		{colOutputTokens, &rec.OutputTokens},
		{colCacheReadTokens, &rec.CacheReadTokens},
		// The CSV schema has one cache write column; it maps to the 5-minute
		// bucket (the 5m/1h split exists only in the JSON export).
		{colCacheWriteTokens, &rec.CacheWrite5mTokens},
	} {
		n, err := parseCount(f.col, get(f.col))
		if err != nil {
			return model.UsageRecord{}, err
		}
		*f.dst = n
	}
	// A total-only row carries a combined token count and no split (see
	// model.UsageRecord.TotalOnly); mixing both in one row would double count.
	totalOnly, err := parseCount(colTotalOnlyTokens, get(colTotalOnlyTokens))
	if err != nil {
		return model.UsageRecord{}, err
	}
	if totalOnly > 0 {
		if hasTokens(&rec) {
			return model.UsageRecord{}, fmt.Errorf("%s cannot be combined with other token columns", colTotalOnlyTokens)
		}
		rec.TotalOnly, rec.TotalOnlyTokens = true, totalOnly
	}
	if s := get(colCostUSD); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return model.UsageRecord{}, fmt.Errorf("%s %q is not a number", colCostUSD, s)
		}
		if v < 0 {
			return model.UsageRecord{}, fmt.Errorf("%s must not be negative", colCostUSD)
		}
		rec.ReportedCostUSD = &v
	}

	if rec.Service == "" {
		return model.UsageRecord{}, errors.New("service is required")
	}
	if rec.Model == "" {
		return model.UsageRecord{}, errors.New("model is required")
	}
	if !hasTokens(&rec) {
		return model.UsageRecord{}, errors.New("at least one token column must be greater than zero")
	}
	rec.ExternalID = rowHash(rec)
	if err := validateCommon(rec, entryLimits); err != nil {
		return model.UsageRecord{}, err
	}
	return rec, nil
}

// timestampLayouts are tried in order. Layouts without a zone are read as UTC.
var timestampLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseTimestamp(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("timestamp is required")
	}
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Truncate(time.Millisecond), nil
		}
	}
	return time.Time{}, fmt.Errorf("timestamp %q is not RFC 3339 (e.g. 2026-03-01T10:00:00Z)", s)
}

// parseCount parses a non-negative integer; an empty cell means 0.
func parseCount(col, s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && errors.Is(ne.Err, strconv.ErrRange) {
			return 0, fmt.Errorf("%s %q is out of range", col, s)
		}
		return 0, fmt.Errorf("%s %q is not a whole number", col, s)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s must not be negative", col)
	}
	return n, nil
}

// rowHash is the external id of a CSV record: the SHA-256 of the normalized
// row. Normalized means trimmed text, the timestamp as unix milliseconds,
// canonical numbers, and an unambiguous separator between fields.
func rowHash(r model.UsageRecord) string {
	cost := ""
	if r.ReportedCostUSD != nil {
		cost = strconv.FormatFloat(*r.ReportedCostUSD, 'g', -1, 64)
	}
	parts := []string{
		strconv.FormatInt(r.Timestamp.UnixMilli(), 10),
		r.Service, r.Model,
		strconv.FormatInt(r.InputTokens, 10),
		strconv.FormatInt(r.OutputTokens, 10),
		strconv.FormatInt(r.CacheReadTokens, 10),
		strconv.FormatInt(r.CacheWrite5mTokens, 10),
		r.Project, r.Session, cost,
	}
	// Appended only when set, so the id of every row without it (all rows of
	// files written before the column existed) stays what it was.
	if r.TotalOnly {
		parts = append(parts, "total_only", strconv.FormatInt(r.TotalOnlyTokens, 10))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

// isFormulaTrigger reports whether a spreadsheet would treat a cell starting
// with c as a formula.
func isFormulaTrigger(c byte) bool {
	return c == '=' || c == '+' || c == '-' || c == '@' || c == '\t' || c == '\r'
}

// needsEscape reports whether text must be quoted on export: it starts with a
// formula trigger, possibly behind quote characters (so that a value that
// already looks like an escaped cell survives the round trip).
func needsEscape(s string) bool {
	i := 0
	for i < len(s) && s[i] == '\'' {
		i++
	}
	return i < len(s) && isFormulaTrigger(s[i])
}

// escapeFormula neutralizes text that a spreadsheet would run as a formula
// by prefixing it with a single quote (OWASP CSV injection guidance).
func escapeFormula(s string) string {
	if needsEscape(s) {
		return "'" + s
	}
	return s
}

// unescapeFormula undoes escapeFormula so export followed by import returns
// the original text.
func unescapeFormula(s string) string {
	if len(s) > 1 && s[0] == '\'' && needsEscape(s[1:]) {
		return s[1:]
	}
	return s
}
