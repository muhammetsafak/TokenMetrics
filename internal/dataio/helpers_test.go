package dataio

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustTotals(t *testing.T, s *store.Store, f store.Filter) store.Totals {
	t.Helper()
	tot, err := s.Totals(context.Background(), f)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	return tot
}

func f64(v float64) *float64 { return &v }

func baseRecord() model.UsageRecord {
	return model.UsageRecord{
		Timestamp:    time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		Service:      "acme",
		Model:        "acme-1",
		InputTokens:  100,
		OutputTokens: 50,
	}
}

func storeFilter() store.Filter { return store.Filter{} }
