package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

// TestEachMatchesGet guards the duplicated column list: a record with every
// field set must come back identical from EachUsageRecord and GetUsageRecord.
func TestEachMatchesGet(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	cost := 1.25
	full := model.UsageRecord{
		Source: model.SourceCodexCLI, ExternalID: "full-1", DeviceID: "dev-9",
		Timestamp: time.Date(2026, 4, 2, 3, 4, 5, 6_000_000, time.UTC),
		Service:   "openai", Model: "gpt-5", Project: "/p", Session: "s",
		InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWrite5mTokens: 4,
		CacheWrite1hTokens: 5, ThinkingTokens: 6, RequestCount: 7, IsSidechain: true,
		ReportedCostUSD: &cost,
	}
	if _, _, err := s.InsertBatch(ctx, []model.UsageRecord{full, rec("other")}); err != nil {
		t.Fatal(err)
	}
	want, ok, err := s.GetUsageRecord(ctx, full.Source, full.ExternalID)
	if err != nil || !ok {
		t.Fatalf("GetUsageRecord: ok=%v err=%v", ok, err)
	}
	var got *model.UsageRecord
	err = s.EachUsageRecord(ctx, Filter{Sources: []model.Source{full.Source}}, func(r model.UsageRecord) error {
		got = &r
		return nil
	})
	if err != nil || got == nil {
		t.Fatalf("EachUsageRecord: got=%v err=%v", got, err)
	}
	if !reflect.DeepEqual(*got, want) || !reflect.DeepEqual(want, full) {
		t.Errorf("readers disagree:\n each %+v\n get  %+v\n put  %+v", *got, want, full)
	}
}
