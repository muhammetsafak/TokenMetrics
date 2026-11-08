// Package codex collects token usage from the OpenAI Codex CLI's local
// rollout files (~/.codex/sessions/**/rollout-*.jsonl, and the zstd-compressed
// rollout-*.jsonl.zst that Codex turns cold rollouts into).
package codex

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/collector"
	"github.com/muhammetsafak/TokenMetrics/internal/ingest"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

const (
	// SettingRootPath is the settings key that overrides the log root.
	SettingRootPath = "sources.codex_cli.path"
	// EnvCodexHome is Codex's own home directory variable.
	EnvCodexHome = "CODEX_HOME"

	// maxWarningsPerFile caps warning spam from one damaged file.
	maxWarningsPerFile = 20
)

// batchSize is how many distinct records are buffered before they are
// written. A variable so tests can shrink it.
var batchSize = 2000

// rolloutName extracts the thread id from rollout-<YYYY-MM-DDThh-mm-ss>-<id>.jsonl
// (or .jsonl.zst). Reverted threads append "_<rollout id>" to the thread id.
var rolloutName = regexp.MustCompile(`^rollout-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-([^_.]+)(?:_[^.]+)?\.jsonl(?:\.zst)?$`)

// Options configures a Collector. The zero value uses the real environment.
type Options struct {
	// Root, when set, is the sessions directory. The settings key still wins.
	Root string
	// Getenv and UserHomeDir exist for tests; they default to the os package.
	Getenv      func(string) string
	UserHomeDir func() (string, error)
}

// Collector implements collector.Collector for the Codex CLI.
type Collector struct {
	opts Options

	mu    sync.Mutex
	cache map[string]cachedContext // rollout path -> context at a saved offset
	// failed remembers the compressed rollouts that could not be decoded, by
	// the size and mtime they had. A compressed file is immutable, so reading
	// it again cannot succeed until it changes; this spares re-decoding it
	// (up to the size cap) and repeating its warning on every scan.
	failed map[string]fileSig
}

// fileSig identifies one version of a file.
type fileSig struct {
	size  int64
	mtime time.Time
}

var _ collector.Collector = (*Collector)(nil)

// New returns a Codex CLI collector.
func New(opts Options) *Collector {
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.UserHomeDir == nil {
		opts.UserHomeDir = os.UserHomeDir
	}
	return &Collector{opts: opts, cache: map[string]cachedContext{}, failed: map[string]fileSig{}}
}

// Name implements collector.Collector.
func (c *Collector) Name() model.Source { return model.SourceCodexCLI }

// DefaultRoot returns the sessions directory without any settings override:
// Options.Root, else $CODEX_HOME/sessions, else ~/.codex/sessions.
func (c *Collector) DefaultRoot() (string, error) {
	if c.opts.Root != "" {
		return c.opts.Root, nil
	}
	if dir := c.opts.Getenv(EnvCodexHome); dir != "" {
		return filepath.Join(dir, "sessions"), nil
	}
	home, err := c.opts.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("codex: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".codex", "sessions"), nil
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
// each rollout since the previous run; a compressed rollout is read whole,
// and only when its size or mtime changed (see ingest.ReadFile). A missing
// root directory (Codex not installed) is not an error.
func (c *Collector) Collect(ctx context.Context, deps collector.Deps) (collector.Result, error) {
	var res collector.Result
	if deps.Store == nil {
		return res, errors.New("codex: nil store")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	root, err := c.root(ctx, deps)
	if err != nil {
		return res, err
	}
	files, walkWarnings, err := ingest.WalkJSONLWithCompressed(ctx, root)
	res.Warnings = append(res.Warnings, walkWarnings...)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			logger.Debug("codex session directory not found", "root", root)
			return res, nil
		}
		return res, fmt.Errorf("codex: scan %s: %w", root, err)
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
	c.prune(files)
	logger.Debug("codex scan finished",
		"files", res.Files, "added", res.Added, "updated", res.Updated, "warnings", len(res.Warnings))
	return res, nil
}

// cachedContext is a session context as it stood when the file was read up
// to offset. Reusing it spares re-reading the whole file prefix on every scan
// of a growing rollout; it is only valid for a read that resumes at offset.
type cachedContext struct {
	offset int64
	ctx    sessionContext
}

func (c *Collector) cached(path string, offset int64) (sessionContext, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[path]
	return e.ctx, ok && e.offset == offset
}

// prune drops the cache entries of rollouts that are no longer listed.
func (c *Collector) prune(live []string) {
	keep := make(map[string]struct{}, len(live))
	for _, p := range live {
		keep[p] = struct{}{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for p := range c.cache {
		if _, ok := keep[p]; !ok {
			delete(c.cache, p)
		}
	}
	for p := range c.failed {
		if _, ok := keep[p]; !ok {
			delete(c.failed, p)
		}
	}
}

// knownBad reports whether path is a compressed rollout that already failed
// to decode in exactly this version, and returns the version it has now.
func (c *Collector) knownBad(path string) (sig fileSig, bad bool) {
	if !ingest.IsCompressed(path) {
		return fileSig{}, false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fileSig{}, false
	}
	sig = fileSig{size: fi.Size(), mtime: fi.ModTime()}
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, ok := c.failed[path]
	return sig, ok && prev.size == sig.size && prev.mtime.Equal(sig.mtime)
}

func (c *Collector) markBad(path string, sig fileSig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failed[path] = fileSig{size: sig.size, mtime: sig.mtime}
}

func (c *Collector) remember(path string, offset int64, sc sessionContext) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[path] = cachedContext{offset: offset, ctx: sc}
}

// primeFn rebuilds the context from the file prefix; a variable so tests can
// count the calls.
var primeFn = primeContext

// stopPriming ends the prefix scan early; it is not a failure.
var stopPriming = errors.New("codex: prefix scan done")

// primeContext rebuilds the session context from the part of the file that a
// resumed read skips, i.e. the lines before limit. The model and working
// directory live in session_meta / turn_context lines written long before the
// appended usage lines, and the ingest state does not carry them.
func primeContext(ctx context.Context, path string, limit int64, sc *sessionContext) {
	_, _ = ingest.ReadFile(ctx, path, nil, ingest.Options{}, func(line []byte, offset int64) error {
		if offset >= limit {
			return stopPriming
		}
		if !interesting(line) {
			return nil
		}
		// Prefix problems were already reported when the lines were first
		// read; here only the context matters, and usage lines never
		// produce records, they only mark hasUsageRecords.
		_, _, _ = parseLine(line, sc, "")
		return nil
	})
}

// fallbackSessionID is used until a session_meta line names the session.
func fallbackSessionID(path string) string {
	base := filepath.Base(path)
	if m := rolloutName.FindStringSubmatch(base); m != nil {
		return m[1]
	}
	if ingest.IsCompressed(base) {
		base = base[:len(base)-len(ingest.CompressedExt)]
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// collectFile ingests one rollout file. A returned error is fatal for the run
// (store failure or cancellation); problems local to the file become warnings.
func (c *Collector) collectFile(ctx context.Context, deps collector.Deps, path string, res *collector.Result) error {
	sig, bad := c.knownBad(path)
	if bad {
		return nil // already reported; it cannot decode until it changes
	}
	prev, found, err := deps.Store.GetIngestState(ctx, model.SourceCodexCLI, path)
	if err != nil {
		return err
	}
	prevPtr := &prev
	if !found {
		prevPtr = nil
	}

	var (
		sc       = &sessionContext{sessionID: fallbackSessionID(path)}
		primed   bool
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
		if !primed {
			primed = true
			if offset > 0 {
				if cached, ok := c.cached(path, offset); ok {
					*sc = cached
				} else {
					primeFn(ctx, path, offset, sc)
				}
			}
		}
		rec, ok, perr := parseLine(line, sc, deps.DeviceID)
		if perr != nil {
			warn(fmt.Sprintf("%s: offset %d: %v", path, offset, perr))
			return nil
		}
		if !ok {
			return nil
		}
		isResponse := strings.HasPrefix(rec.ExternalID, responseKeyPrefix)
		if i, dup := index[rec.ExternalID]; dup {
			// A response is immutable and the earliest sighting is the
			// original: see keepOriginal. Other keys: the last line wins.
			if !isResponse || rec.Timestamp.UnixMilli() < pending[i].Timestamp.UnixMilli() {
				pending[i] = rec
			}
			return nil
		}
		if isResponse {
			existing, exists, gerr := deps.Store.GetUsageRecord(ctx, model.SourceCodexCLI, rec.ExternalID)
			if gerr != nil {
				fatal = gerr
				return gerr
			}
			if exists && keepOriginal(existing, rec) {
				return nil
			}
		}
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
		var de *ingest.DecodeError
		if sig != (fileSig{}) && (errors.As(err, &de) || errors.Is(err, ingest.ErrTooLarge)) {
			c.markBad(path, sig)
		}
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
	if !(found && prev.Offset == fr.State.Offset && prev.Size == fr.State.Size && prev.ModTime.Equal(fr.State.ModTime)) {
		st := fr.State
		st.Source = model.SourceCodexCLI
		if err := deps.Store.SetIngestState(ctx, st); err != nil {
			return err
		}
	}
	// Remember the context only when lines were read: without them sc is the
	// bare fallback, and caching it at this offset would make the next resumed
	// read skip priming and store its records without model and project. A
	// compressed rollout is never resumed.
	if primed && !ingest.IsCompressed(path) {
		c.remember(path, fr.State.Offset, *sc)
	}
	return nil
}

// keepOriginal reports whether the stored response record is the original and
// the newly parsed one must be dropped. Codex copies the parent's rollout
// items, token_usage_record included, into forked and sub-agent rollouts and
// stamps every copied line with the time of the copy. Left to the store's
// last-write-wins, a copy would move the response to the fork's date, session
// and project. The earliest timestamp is the original, whichever file is
// scanned first; equal timestamps are a re-read of the same line.
func keepOriginal(existing, rec model.UsageRecord) bool {
	return existing.Timestamp.UnixMilli() <= rec.Timestamp.UnixMilli()
}
