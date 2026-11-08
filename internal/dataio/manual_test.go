package dataio

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

func TestAddManual(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	in := baseRecord()
	in.Source = model.SourceClaudeCode // must be overridden
	in.ExternalID = "forced"           // must be overridden
	in.Service = "  acme  "
	got, err := AddManual(ctx, s, "dev-1", in)
	if err != nil {
		t.Fatalf("AddManual: %v", err)
	}
	if got.Source != model.SourceManual || got.DeviceID != "dev-1" || got.Service != "acme" {
		t.Errorf("unexpected record: %+v", got)
	}
	if got.ExternalID == "" || got.ExternalID == "forced" {
		t.Errorf("ExternalID = %q, want a fresh UUID", got.ExternalID)
	}
	stored, ok, err := s.GetUsageRecord(ctx, model.SourceManual, got.ExternalID)
	if err != nil || !ok {
		t.Fatalf("GetUsageRecord: ok=%v err=%v", ok, err)
	}
	if stored.InputTokens != 100 || stored.OutputTokens != 50 {
		t.Errorf("stored = %+v", stored)
	}

	other, err := AddManual(ctx, s, "dev-1", in)
	if err != nil {
		t.Fatal(err)
	}
	if other.ExternalID == got.ExternalID {
		t.Error("two manual records share an external id")
	}
	if n := mustTotals(t, s, storeFilter()).RecordCount; n != 2 {
		t.Errorf("RecordCount = %d, want 2", n)
	}
}

func TestAddManualValidation(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	cases := map[string]func(*model.UsageRecord){
		"no timestamp":     func(r *model.UsageRecord) { r.Timestamp = time.Time{} },
		"no service":       func(r *model.UsageRecord) { r.Service = "  " },
		"no model":         func(r *model.UsageRecord) { r.Model = "" },
		"no tokens":        func(r *model.UsageRecord) { r.InputTokens, r.OutputTokens = 0, 0 },
		"negative input":   func(r *model.UsageRecord) { r.InputTokens = -1 },
		"negative cache":   func(r *model.UsageRecord) { r.CacheReadTokens = -5 },
		"negative cost":    func(r *model.UsageRecord) { r.ReportedCostUSD = f64(-0.1) },
		"huge counter":     func(r *model.UsageRecord) { r.InputTokens = maxTokens + 1 },
		"future timestamp": func(r *model.UsageRecord) { r.Timestamp = time.Now().Add(72 * time.Hour) },
		"pre-1970":         func(r *model.UsageRecord) { r.Timestamp = time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC) },
		"overlong service": func(r *model.UsageRecord) { r.Service = strings.Repeat("x", maxTextLen+1) },
		"overlong project": func(r *model.UsageRecord) { r.Project = strings.Repeat("é", maxTextLen+1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := baseRecord()
			mutate(&r)
			if _, err := AddManual(ctx, s, "dev", r); err == nil {
				t.Error("AddManual accepted an invalid record")
			}
		})
	}
	if n := mustTotals(t, s, storeFilter()).RecordCount; n != 0 {
		t.Errorf("invalid records were stored: %d", n)
	}

	// Only cache/thinking tokens still count as "has tokens".
	r := baseRecord()
	r.InputTokens, r.OutputTokens, r.CacheWrite1hTokens = 0, 0, 7
	if _, err := AddManual(ctx, s, "dev", r); err != nil {
		t.Errorf("cache-write-only record rejected: %v", err)
	}
}

func TestUpdateAndDeleteManualOnlyTouchManual(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	m, err := AddManual(ctx, s, "dev-a", baseRecord())
	if err != nil {
		t.Fatal(err)
	}

	// A collector record must be immune to update and delete.
	collected := baseRecord()
	collected.Source = model.SourceClaudeCode
	collected.ExternalID = "msg-1"
	collected.DeviceID = "dev-a"
	if _, _, err := s.InsertBatch(ctx, []model.UsageRecord{collected}); err != nil {
		t.Fatal(err)
	}

	upd := m
	upd.InputTokens = 999
	upd.Model = "acme-2"
	upd.DeviceID = "tampered"
	got, err := UpdateManual(ctx, s, upd)
	if err != nil {
		t.Fatalf("UpdateManual: %v", err)
	}
	if got.DeviceID != "dev-a" {
		t.Errorf("DeviceID = %q, want original dev-a", got.DeviceID)
	}
	stored, _, _ := s.GetUsageRecord(ctx, model.SourceManual, m.ExternalID)
	if stored.InputTokens != 999 || stored.Model != "acme-2" || stored.DeviceID != "dev-a" {
		t.Errorf("stored after update = %+v", stored)
	}

	// Same external id but a collector record: must not be found / changed.
	bad := collected
	bad.InputTokens = 1
	if _, err := UpdateManual(ctx, s, bad); !errors.Is(err, ErrNotManual) {
		t.Errorf("update with source=claude_code: err = %v, want ErrNotManual", err)
	}
	bad.Source = ""
	if _, err := UpdateManual(ctx, s, bad); !errors.Is(err, ErrManualNotFound) {
		t.Errorf("update of a collector id: err = %v, want ErrManualNotFound", err)
	}
	if err := DeleteManual(ctx, s, "msg-1"); !errors.Is(err, ErrManualNotFound) {
		t.Errorf("delete of a collector id: err = %v, want ErrManualNotFound", err)
	}
	c, ok, _ := s.GetUsageRecord(ctx, model.SourceClaudeCode, "msg-1")
	if !ok || c.InputTokens != collected.InputTokens {
		t.Errorf("collector record changed: ok=%v %+v", ok, c)
	}

	// Invalid update leaves the record alone.
	inv := upd
	inv.InputTokens, inv.OutputTokens = 0, 0
	if _, err := UpdateManual(ctx, s, inv); err == nil {
		t.Error("update to a record without tokens succeeded")
	}
	stored, _, _ = s.GetUsageRecord(ctx, model.SourceManual, m.ExternalID)
	if stored.InputTokens != 999 {
		t.Errorf("failed update changed the record: %+v", stored)
	}

	if err := DeleteManual(ctx, s, m.ExternalID); err != nil {
		t.Fatalf("DeleteManual: %v", err)
	}
	if err := DeleteManual(ctx, s, m.ExternalID); !errors.Is(err, ErrManualNotFound) {
		t.Errorf("second delete: err = %v, want ErrManualNotFound", err)
	}
	if _, err := UpdateManual(ctx, s, upd); !errors.Is(err, ErrManualNotFound) {
		t.Errorf("update after delete: err = %v, want ErrManualNotFound", err)
	}
}
