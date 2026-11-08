// Package dataio moves usage data in and out of the store by hand: manual
// entries, CSV import, and JSON/CSV export plus JSON import (the building
// block for merging data from several devices).
//
// Nothing here ever reads or writes secrets or settings; only usage records.
package dataio

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

// Sanity limits for hand-typed and CSV data. They keep typos and foreign files
// from overflowing sums or bloating rows.
const (
	maxTextLen    = 256            // service, model, project, session
	maxExternalID = 512            // external id (CSV ids are 64 hex characters)
	maxFutureSkew = 24 * time.Hour // timestamps further ahead than this are rejected
)

// Limits that hold for every source: they only guard int64 sums and floats.
const (
	maxTokens  = int64(1e15) // per counter; keeps int64 sums far from overflow
	maxCostUSD = 1e9         // per record
)

// Wider limits for JSON imports. The records come from this app's own store on
// another device, where collectors write paths and ids unchecked, so anything
// a collector could store must be accepted back (a deep working directory
// easily exceeds maxTextLen). Clock skew between devices is not an error, so
// there is no future check.
const (
	maxImportTextLen    = 32768 // roughly the longest path Windows allows
	maxImportExternalID = 4096
)

// limits are the per-entry-path bounds validateCommon applies.
type limits struct {
	textLen    int
	externalID int
	futureSkew time.Duration // 0 disables the future-timestamp check
}

var (
	entryLimits  = limits{textLen: maxTextLen, externalID: maxExternalID, futureSkew: maxFutureSkew}
	importLimits = limits{textLen: maxImportTextLen, externalID: maxImportExternalID}
)

// Errors returned by the manual-entry functions.
var (
	// ErrManualNotFound means there is no manual record with that id. Records
	// from other sources can never be updated or deleted here.
	ErrManualNotFound = errors.New("dataio: manual record not found")
	// ErrNotManual means the record passed in claims a source other than manual.
	ErrNotManual = errors.New("dataio: only manual records can be changed")
)

// AddManual stores a hand-entered record. Source is forced to manual and
// ExternalID to a fresh UUID; DeviceID is set to deviceID. Required: a
// timestamp, service and model, and at least one token counter above zero.
// The stored record is returned.
func AddManual(ctx context.Context, st *store.Store, deviceID string, rec model.UsageRecord) (model.UsageRecord, error) {
	rec.Source = model.SourceManual
	rec.ExternalID = uuid.NewString()
	rec.DeviceID = deviceID
	rec.Timestamp = rec.Timestamp.UTC().Truncate(time.Millisecond)
	if err := validateManual(&rec); err != nil {
		return model.UsageRecord{}, err
	}
	if _, _, err := st.InsertBatch(ctx, []model.UsageRecord{rec}); err != nil {
		return model.UsageRecord{}, err
	}
	return rec, nil
}

// UpdateManual replaces the values of the manual record with rec.ExternalID.
// The original DeviceID is kept. It returns ErrManualNotFound when no manual
// record has that id (which includes ids of records from other sources) and
// ErrNotManual when rec.Source is set to anything but manual.
func UpdateManual(ctx context.Context, st *store.Store, rec model.UsageRecord) (model.UsageRecord, error) {
	if rec.Source != "" && rec.Source != model.SourceManual {
		return model.UsageRecord{}, ErrNotManual
	}
	if rec.ExternalID == "" {
		return model.UsageRecord{}, errors.New("dataio: external id is required")
	}
	old, found, err := st.GetUsageRecord(ctx, model.SourceManual, rec.ExternalID)
	if err != nil {
		return model.UsageRecord{}, err
	}
	if !found {
		return model.UsageRecord{}, ErrManualNotFound
	}
	rec.Source = model.SourceManual
	rec.DeviceID = old.DeviceID
	rec.Timestamp = rec.Timestamp.UTC().Truncate(time.Millisecond)
	if err := validateManual(&rec); err != nil {
		return model.UsageRecord{}, err
	}
	ok, err := st.UpdateManualRecord(ctx, rec)
	if err != nil {
		return model.UsageRecord{}, err
	}
	if !ok { // deleted between the lookup and the update
		return model.UsageRecord{}, ErrManualNotFound
	}
	return rec, nil
}

// DeleteManual removes the manual record with the given id. It returns
// ErrManualNotFound when there is none; records from other sources are never
// deleted.
func DeleteManual(ctx context.Context, st *store.Store, externalID string) error {
	ok, err := st.DeleteManualRecord(ctx, externalID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrManualNotFound
	}
	return nil
}

// validateManual trims the text fields and enforces the manual-entry rules.
func validateManual(r *model.UsageRecord) error {
	trimText(r)
	if r.Timestamp.IsZero() {
		return errors.New("timestamp is required")
	}
	if r.Service == "" {
		return errors.New("service is required")
	}
	if r.Model == "" {
		return errors.New("model is required")
	}
	if !hasTokens(r) {
		return errors.New("at least one token field must be greater than zero")
	}
	return validateCommon(*r, entryLimits)
}

func hasTokens(r *model.UsageRecord) bool {
	return r.InputTokens > 0 || r.OutputTokens > 0 || r.CacheReadTokens > 0 ||
		r.CacheWrite5mTokens > 0 || r.CacheWrite1hTokens > 0 || r.ThinkingTokens > 0 ||
		r.TotalOnlyTokens > 0
}

func trimText(r *model.UsageRecord) {
	r.Service = strings.TrimSpace(r.Service)
	r.Model = strings.TrimSpace(r.Model)
	r.Project = strings.TrimSpace(r.Project)
	r.Session = strings.TrimSpace(r.Session)
}

// validateCommon applies the store invariants plus the given limits. It is the
// single gate every record from manual entry, CSV and JSON import passes.
func validateCommon(r model.UsageRecord, lim limits) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if lim.futureSkew > 0 && r.Timestamp.After(time.Now().Add(lim.futureSkew)) {
		return errors.New("timestamp is in the future")
	}
	for _, f := range []struct{ name, v string }{
		{"service", r.Service}, {"model", r.Model},
		{"project", r.Project}, {"session", r.Session},
	} {
		if utf8.RuneCountInString(f.v) > lim.textLen {
			return fmt.Errorf("%s is longer than %d characters", f.name, lim.textLen)
		}
		if !utf8.ValidString(f.v) {
			return fmt.Errorf("%s is not valid UTF-8", f.name)
		}
	}
	if len(r.ExternalID) > lim.externalID {
		return fmt.Errorf("external id is longer than %d bytes", lim.externalID)
	}
	for _, c := range []struct {
		name string
		v    int64
	}{
		{"input_tokens", r.InputTokens}, {"output_tokens", r.OutputTokens},
		{"cache_read_tokens", r.CacheReadTokens},
		{"cache_write_5m_tokens", r.CacheWrite5mTokens},
		{"cache_write_1h_tokens", r.CacheWrite1hTokens},
		{"thinking_tokens", r.ThinkingTokens}, {"request_count", r.RequestCount},
		{"total_only_tokens", r.TotalOnlyTokens},
	} {
		if c.v > maxTokens {
			return fmt.Errorf("%s is larger than %d", c.name, maxTokens)
		}
	}
	if r.ReportedCostUSD != nil {
		v := *r.ReportedCostUSD
		if math.IsNaN(v) || math.IsInf(v, 0) || v > maxCostUSD {
			return errors.New("reported_cost_usd is not a sane number")
		}
	}
	return nil
}
