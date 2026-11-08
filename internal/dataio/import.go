package dataio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

// MaxJSONImportBytes is the largest JSON export ImportJSON accepts (512 MiB).
// JSON exports are bigger than CSV ones, so the limit is higher than
// MaxCSVImportBytes.
const MaxJSONImportBytes = 512 << 20

// maxReportedErrors caps ImportResult.Errors; ErrorCount keeps the true count.
const maxReportedErrors = 1000

// importBatchSize is how many JSON records go into one transaction.
const importBatchSize = 1000

// Errors returned by ImportJSON for whole-file problems.
var (
	ErrUnsupportedSchema = errors.New("dataio: unsupported export schema_version")
	ErrInvalidExport     = errors.New("dataio: not a valid TokenMetrics JSON export")
)

// RowError describes one rejected row. Line is the 1-based line number in a
// CSV file, or the 1-based position of the record in a JSON export.
type RowError struct {
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// ImportResult summarizes an import.
type ImportResult struct {
	Rows      int // data rows (CSV) or records (JSON) read
	Added     int // new records stored
	Updated   int // existing records whose values changed
	Unchanged int // valid rows that matched an existing record exactly
	// Errors lists the rejected rows (at most 1000); ErrorCount is the full count.
	Errors     []RowError
	ErrorCount int
}

func (r *ImportResult) addError(line int, reason string) {
	r.ErrorCount++
	if len(r.Errors) < maxReportedErrors {
		r.Errors = append(r.Errors, RowError{Line: line, Reason: reason})
	}
}

// ImportJSON reads a JSON export written by ExportJSON and merges it into the
// store. Source, ExternalID and DeviceID of every record are kept, so data
// from another device joins this one and importing the same file again changes
// nothing. A record that already exists is overwritten (last write wins, as
// everywhere in the store). Records without a device id get deviceID.
//
// schema_version must come before the records (ExportJSON writes it first) and
// must be known; otherwise nothing is imported. Invalid records are skipped
// and listed in the result. Records are written in batches, so a failure
// halfway leaves earlier batches stored; running the import again is safe.
func ImportJSON(ctx context.Context, st *store.Store, r io.Reader, deviceID string) (ImportResult, error) {
	lr := &limitedReader{r: r, left: MaxJSONImportBytes}
	dec := json.NewDecoder(lr)

	var res ImportResult
	if err := expectDelim(dec, '{'); err != nil {
		return res, tooLargeOr(lr, err)
	}
	haveVersion, haveRecords := false, false
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return res, jsonReadErr(lr, err)
		}
		key, _ := keyTok.(string)
		switch key {
		case "schema_version":
			var v int
			if err := dec.Decode(&v); err != nil {
				return res, jsonReadErr(lr, err)
			}
			if v != ExportSchemaVersion {
				return res, fmt.Errorf("%w: %d (this version reads %d)", ErrUnsupportedSchema, v, ExportSchemaVersion)
			}
			haveVersion = true
		case "records":
			if !haveVersion {
				return res, fmt.Errorf("%w: schema_version must come before records", ErrInvalidExport)
			}
			if haveRecords {
				return res, fmt.Errorf("%w: records appears twice", ErrInvalidExport)
			}
			haveRecords = true
			if err := importRecords(ctx, st, dec, lr, deviceID, &res); err != nil {
				return res, err
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return res, jsonReadErr(lr, err)
			}
		}
	}
	if !haveVersion {
		return res, fmt.Errorf("%w: schema_version is missing", ErrInvalidExport)
	}
	if !haveRecords {
		return res, fmt.Errorf("%w: records is missing", ErrInvalidExport)
	}
	return res, nil
}

func importRecords(ctx context.Context, st *store.Store, dec *json.Decoder, lr *limitedReader, deviceID string, res *ImportResult) error {
	if err := expectDelim(dec, '['); err != nil {
		return tooLargeOr(lr, err)
	}
	var batch []model.UsageRecord
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		added, updated, err := st.InsertBatch(ctx, batch)
		if err != nil {
			return err
		}
		res.Added += added
		res.Updated += updated
		res.Unchanged += len(batch) - added - updated
		batch = batch[:0]
		return nil
	}

	pos := 0
	for dec.More() {
		if err := ctx.Err(); err != nil {
			return err
		}
		pos++
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return jsonReadErr(lr, err)
		}
		res.Rows++
		var rec model.UsageRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			res.addError(pos, "malformed record: "+err.Error())
			continue
		}
		if rec.DeviceID == "" {
			rec.DeviceID = deviceID
		}
		if err := validateCommon(rec, importLimits); err != nil {
			res.addError(pos, err.Error())
			continue
		}
		batch = append(batch, rec)
		if len(batch) >= importBatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	// Check the closing bracket before the last flush: on truncated input
	// dec.More reports false at EOF, and the final batch must not be stored.
	if err := expectDelim(dec, ']'); err != nil {
		return tooLargeOr(lr, err)
	}
	return flush()
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidExport, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("%w: expected %q", ErrInvalidExport, string(want))
	}
	return nil
}

func jsonReadErr(lr *limitedReader, err error) error {
	return tooLargeOr(lr, fmt.Errorf("%w: %v", ErrInvalidExport, err))
}

func tooLargeOr(lr *limitedReader, err error) error {
	if lr.exceeded {
		return fmt.Errorf("%w (limit is %d MiB)", ErrFileTooLarge, MaxJSONImportBytes>>20)
	}
	return err
}

// limitedReader fails (instead of silently truncating) once more than left
// bytes have been read, so an oversized file is reported as such.
type limitedReader struct {
	r        io.Reader
	left     int64
	exceeded bool
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		// Only an error if the file actually continues.
		var one [1]byte
		n, err := l.r.Read(one[:])
		if n > 0 {
			l.exceeded = true
			return 0, ErrFileTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}
