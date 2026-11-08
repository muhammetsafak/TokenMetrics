package model

import (
	"testing"
	"time"
)

func totalOnlyRecord() UsageRecord {
	return UsageRecord{
		Source: SourceCodexCLI, ExternalID: "x", Timestamp: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC),
		Model: "m", RequestCount: 1, TotalOnly: true, TotalOnlyTokens: 900,
	}
}

func TestTotalOnlyRecordValidationAndTotal(t *testing.T) {
	r := totalOnlyRecord()
	if err := r.Validate(); err != nil {
		t.Fatalf("valid total-only record rejected: %v", err)
	}
	if r.TotalTokens() != 900 {
		t.Fatalf("TotalTokens = %d, want 900", r.TotalTokens())
	}

	bad := map[string]func(*UsageRecord){
		"no count":           func(r *UsageRecord) { r.TotalOnlyTokens = 0 },
		"negative count":     func(r *UsageRecord) { r.TotalOnlyTokens = -1 },
		"count without flag": func(r *UsageRecord) { r.TotalOnly = false },
		"split input":        func(r *UsageRecord) { r.InputTokens = 1 },
		"split output":       func(r *UsageRecord) { r.OutputTokens = 1 },
		"split cache read":   func(r *UsageRecord) { r.CacheReadTokens = 1 },
		"split cache write":  func(r *UsageRecord) { r.CacheWrite5mTokens = 1 },
	}
	for name, mutate := range bad {
		r := totalOnlyRecord()
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, r)
		}
	}
}
