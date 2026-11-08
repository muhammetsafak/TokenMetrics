// Package collector defines the interface every usage source implements.
package collector

import (
	"context"
	"log/slog"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

// Deps are the shared services handed to a collector on each run.
type Deps struct {
	Store    *store.Store
	DeviceID string // stamped on every record the collector writes
	Logger   *slog.Logger
}

// Result summarizes one Collect run.
type Result struct {
	Added    int      // new records written
	Updated  int      // existing records whose values changed
	Files    int      // source files (or API pages) processed
	Warnings []string // non-fatal problems, e.g. unparseable lines
}

// Collector reads usage from one source and writes it to the store.
// Implementations must be idempotent: running Collect twice over the same
// data must not change the stored totals (see docs/design.md).
type Collector interface {
	// Name returns the source this collector produces records for.
	Name() model.Source
	// Collect gathers new usage and stores it. It should honor ctx
	// cancellation and report partial progress in Result even on error.
	Collect(ctx context.Context, deps Deps) (Result, error)
}
