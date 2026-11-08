package dataio

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

// ExportSchemaVersion is the version written to (and required from) JSON
// exports. Bump it for any change a reader of the old version would misread.
const ExportSchemaVersion = 1

// csvTimestampLayout is the CSV export timestamp: RFC 3339, UTC, milliseconds.
const csvTimestampLayout = "2006-01-02T15:04:05.000Z"

// exportHeader is the fixed start of a JSON export; the records follow.
type exportHeader struct {
	SchemaVersion int       `json:"schema_version"`
	ExportedAt    time.Time `json:"exported_at"`
	DeviceID      string    `json:"device_id"`
}

// ExportJSON writes the records matching f as one JSON document:
//
//	{"schema_version":1,"exported_at":...,"device_id":"<this device>","records":[...]}
//
// Every record carries all its fields including its own device_id, so another
// device can merge it with ImportJSON. Only usage records are exported:
// settings and secrets are never part of an export. The output is streamed.
func ExportJSON(ctx context.Context, st *store.Store, w io.Writer, f store.Filter, deviceID string) error {
	hdr, err := json.Marshal(exportHeader{
		SchemaVersion: ExportSchemaVersion,
		ExportedAt:    time.Now().UTC().Truncate(time.Second),
		DeviceID:      deviceID,
	})
	if err != nil {
		return err
	}
	// hdr is {...}; reopen it so "records" lands in the same object.
	if _, err := w.Write(append(hdr[:len(hdr)-1], []byte(`,"records":[`)...)); err != nil {
		return fmt.Errorf("dataio: write export: %w", err)
	}

	first := true
	err = st.EachUsageRecord(ctx, f, func(r model.UsageRecord) error {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if !first {
			if _, err := w.Write([]byte{','}); err != nil {
				return err
			}
		}
		first = false
		_, err = w.Write(b)
		return err
	})
	if err != nil {
		return fmt.Errorf("dataio: write export: %w", err)
	}
	if _, err := w.Write([]byte("]}\n")); err != nil {
		return fmt.Errorf("dataio: write export: %w", err)
	}
	return nil
}

// ExportCSV writes the records matching f in the import schema (see
// docs/csv-format.md), delimiter ','. cache_write_tokens is the sum of the
// 5-minute and 1-hour cache write tokens; total_only_tokens holds the combined
// count of a total-only record (no input/output split, see
// model.UsageRecord.TotalOnly), 0 for every other record; thinking tokens, request counts,
// source, device and the sidechain flag are not part of the CSV schema, use
// ExportJSON for a lossless copy. Text cells that a spreadsheet would run as a
// formula (starting with =, +, -, @, tab or CR) are prefixed with a single
// quote; ImportCSV removes that prefix again.
//
// Do not feed this CSV back into a store that already holds the exported
// records: rows carry no source or external id, so ImportCSV stores them as new
// csv_import records and every total doubles. Use ExportJSON/ImportJSON to
// move or merge data.
func ExportCSV(ctx context.Context, st *store.Store, w io.Writer, f store.Filter) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvColumns); err != nil {
		return fmt.Errorf("dataio: write csv: %w", err)
	}
	err := st.EachUsageRecord(ctx, f, func(r model.UsageRecord) error {
		cost := ""
		if r.ReportedCostUSD != nil {
			cost = strconv.FormatFloat(*r.ReportedCostUSD, 'f', -1, 64)
		}
		return cw.Write([]string{
			r.Timestamp.UTC().Format(csvTimestampLayout),
			escapeFormula(r.Service),
			escapeFormula(r.Model),
			strconv.FormatInt(r.InputTokens, 10),
			strconv.FormatInt(r.OutputTokens, 10),
			strconv.FormatInt(r.CacheReadTokens, 10),
			strconv.FormatInt(r.CacheWrite5mTokens+r.CacheWrite1hTokens, 10),
			escapeFormula(r.Project),
			escapeFormula(r.Session),
			cost,
			strconv.FormatInt(r.TotalOnlyTokens, 10),
		})
	})
	if err != nil {
		return fmt.Errorf("dataio: write csv: %w", err)
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("dataio: write csv: %w", err)
	}
	return nil
}
