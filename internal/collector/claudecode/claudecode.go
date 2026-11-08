// Package claudecode collects token usage from Claude Code's local session
// logs (~/.claude/projects/**/*.jsonl).
package claudecode

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/muhammetsafak/TokenMetrics/internal/collector"
	"github.com/muhammetsafak/TokenMetrics/internal/ingest"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

const (
	// SettingRootPath is the settings key that overrides the log root.
	SettingRootPath = "sources.claude_code.path"
	// EnvConfigDir is Claude Code's own config directory variable.
	EnvConfigDir = "CLAUDE_CONFIG_DIR"

	// maxWarningsPerFile caps warning spam from one damaged file.
	maxWarningsPerFile = 20
)

// batchSize is how many distinct records are buffered before they are
// written. Writing in chunks keeps memory flat on large first scans; upserts
// make the chunking invisible to the stored totals. A variable so tests can
// shrink it.
var batchSize = 2000

// Options configures a Collector. The zero value uses the real environment.
type Options struct {
	// Root, when set, is the log directory. The settings key still wins.
	Root string
	// Getenv and UserHomeDir exist for tests; they default to the os package.
	Getenv      func(string) string
	UserHomeDir func() (string, error)
}

// Collector implements collector.Collector for Claude Code.
type Collector struct {
	opts Options
}

var _ collector.Collector = (*Collector)(nil)

// New returns a Claude Code collector.
func New(opts Options) *Collector {
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.UserHomeDir == nil {
		opts.UserHomeDir = os.UserHomeDir
	}
	return &Collector{opts: opts}
}

// Name implements collector.Collector.
func (c *Collector) Name() model.Source { return model.SourceClaudeCode }

// DefaultRoot returns the log directory without any settings override:
// Options.Root, else $CLAUDE_CONFIG_DIR/projects, else ~/.claude/projects.
func (c *Collector) DefaultRoot() (string, error) {
	if c.opts.Root != "" {
		return c.opts.Root, nil
	}
	if dir := c.opts.Getenv(EnvConfigDir); dir != "" {
		return filepath.Join(dir, "projects"), nil
	}
	home, err := c.opts.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("claudecode: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

func (c *Collector) root(ctx context.Context, deps collector.Deps) (string, error) {
	var p string
	found, err := deps.Store.GetSetting(ctx, SettingRootPath, &p)
	if err != nil {
		return "", err
	}
	if found && p != "" {
		return p, nil
	}
	return c.DefaultRoot()
}

// Collect implements collector.Collector. It reads only the bytes appended to
// each log since the previous run. A missing root directory (Claude Code not
// installed) is not an error.
func (c *Collector) Collect(ctx context.Context, deps collector.Deps) (collector.Result, error) {
	var res collector.Result
	if deps.Store == nil {
		return res, errors.New("claudecode: nil store")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	root, err := c.root(ctx, deps)
	if err != nil {
		return res, err
	}
	files, walkWarnings, err := ingest.WalkJSONL(ctx, root)
	res.Warnings = append(res.Warnings, walkWarnings...)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			logger.Debug("claude code log directory not found", "root", root)
			return res, nil
		}
		return res, fmt.Errorf("claudecode: scan %s: %w", root, err)
	}

	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if err := c.collectFile(ctx, deps, path, &res); err != nil {
			return res, err
		}
		res.Files++
	}
	logger.Debug("claude code scan finished",
		"files", res.Files, "added", res.Added, "updated", res.Updated, "warnings", len(res.Warnings))
	return res, nil
}

// collectFile ingests one log file. A returned error is fatal for the run
// (store failure or cancellation); problems local to the file become warnings.
func (c *Collector) collectFile(ctx context.Context, deps collector.Deps, path string, res *collector.Result) error {
	prev, found, err := deps.Store.GetIngestState(ctx, model.SourceClaudeCode, path)
	if err != nil {
		return err
	}
	prevPtr := &prev
	if !found {
		prevPtr = nil
	}

	var (
		pending  []model.UsageRecord
		index    = map[string]int{} // ExternalID -> position in pending
		warnings int
		fatal    error
	)
	warn := func(msg string) {
		warnings++
		if warnings <= maxWarningsPerFile {
			res.Warnings = append(res.Warnings, msg)
		}
	}
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		added, updated, err := deps.Store.InsertBatch(ctx, pending)
		if err != nil {
			return err
		}
		res.Added += added
		res.Updated += updated
		pending = pending[:0]
		clear(index)
		return nil
	}

	fr, err := ingest.ReadFile(ctx, path, prevPtr, ingest.Options{}, func(line []byte, offset int64) error {
		rec, ok, perr := parseLine(line, deps.DeviceID)
		if perr != nil {
			warn(fmt.Sprintf("%s: offset %d: %v", path, offset, perr))
			return nil
		}
		if !ok {
			return nil
		}
		// A message repeats its usage on every content-block line; the last
		// occurrence carries the final values.
		if i, dup := index[rec.ExternalID]; dup {
			pending[i] = rec
			return nil
		}
		// Flush only when a new message id starts, so the content-block
		// lines of one message are never split across two writes.
		if len(pending) >= batchSize {
			if ferr := flush(); ferr != nil {
				fatal = ferr
				return ferr
			}
		}
		index[rec.ExternalID] = len(pending)
		pending = append(pending, rec)
		return nil
	})
	if fatal != nil {
		return fatal
	}
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		// Unreadable, vanished or non-regular file: skip it, keep going.
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", path, err))
		return nil
	}
	if err := flush(); err != nil {
		return err
	}
	for _, w := range fr.Warnings {
		warn(w)
	}
	if warnings > maxWarningsPerFile {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("%s: %d more warnings suppressed", path, warnings-maxWarningsPerFile))
	}

	// Nothing changed since the last run: skip the write, it would only
	// churn updated_at (and the WAL) once per file per scan.
	if found && prev.Offset == fr.State.Offset && prev.Size == fr.State.Size && prev.ModTime.Equal(fr.State.ModTime) {
		return nil
	}
	st := fr.State
	st.Source = model.SourceClaudeCode
	return deps.Store.SetIngestState(ctx, st)
}
