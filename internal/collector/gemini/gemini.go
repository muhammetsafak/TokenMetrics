// Package gemini collects token usage from the Gemini CLI's local session
// recordings (~/.gemini/tmp/<project>/chats/**/*.jsonl, plus legacy *.json).
//
// Only chat recordings are read. The prompt history next to them
// (tmp/<project>/logs.json) carries no token usage and is never opened.
package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/muhammetsafak/TokenMetrics/internal/collector"
	"github.com/muhammetsafak/TokenMetrics/internal/ingest"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

const (
	// SettingRootPath is the settings key that overrides the log root
	// (the directory that holds the per-project folders, i.e. ~/.gemini/tmp).
	SettingRootPath = "sources.gemini_cli.path"
	// EnvCLIHome is Gemini CLI's own home override; the CLI keeps its data
	// in <GEMINI_CLI_HOME>/.gemini.
	EnvCLIHome = "GEMINI_CLI_HOME"

	chatsDir        = "chats"
	projectRootFile = ".project_root"

	// maxLegacyFileBytes bounds a legacy .json session, which is read whole.
	maxLegacyFileBytes = 64 << 20
	// maxProjectRootBytes bounds the .project_root file.
	maxProjectRootBytes = 4 << 10

	// maxWarningsPerFile caps warning spam from one damaged file.
	maxWarningsPerFile = 20
)

// batchSize is how many distinct records are buffered before they are
// written. A variable so tests can shrink it.
var batchSize = 2000

// Options configures a Collector. The zero value uses the real environment.
type Options struct {
	// Root, when set, is the tmp directory. The settings key still wins.
	Root string
	// Getenv and UserHomeDir exist for tests; they default to the os package.
	Getenv      func(string) string
	UserHomeDir func() (string, error)
}

// Collector implements collector.Collector for the Gemini CLI.
type Collector struct {
	opts Options
}

var _ collector.Collector = (*Collector)(nil)

// New returns a Gemini CLI collector.
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
func (c *Collector) Name() model.Source { return model.SourceGeminiCLI }

// DefaultRoot returns the tmp directory without any settings override:
// Options.Root, else $GEMINI_CLI_HOME/.gemini/tmp, else ~/.gemini/tmp.
func (c *Collector) DefaultRoot() (string, error) {
	if c.opts.Root != "" {
		return c.opts.Root, nil
	}
	home := c.opts.Getenv(EnvCLIHome)
	if home == "" {
		var err error
		if home, err = c.opts.UserHomeDir(); err != nil {
			return "", fmt.Errorf("gemini: resolve home dir: %w", err)
		}
	}
	return filepath.Join(home, ".gemini", "tmp"), nil
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

// sessionFile is one session recording to ingest.
type sessionFile struct {
	path    string
	project string
	legacy  bool // a single-object .json file instead of JSONL
}

// Collect implements collector.Collector. It reads only the bytes appended to
// each recording since the previous run. A missing root directory (Gemini CLI
// not installed) is not an error. When projects exist but no recording does,
// a warning says that no token data is available.
func (c *Collector) Collect(ctx context.Context, deps collector.Deps) (collector.Result, error) {
	var res collector.Result
	if deps.Store == nil {
		return res, errors.New("gemini: nil store")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	root, err := c.root(ctx, deps)
	if err != nil {
		return res, err
	}
	files, projects, err := discover(ctx, root, &res)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			logger.Debug("gemini tmp directory not found", "root", root)
			return res, nil
		}
		return res, fmt.Errorf("gemini: scan %s: %w", root, err)
	}
	if len(files) == 0 && projects > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"gemini: no token data: %d project folder(s) under %s hold no chat recordings (chats/session-*.jsonl)",
			projects, root))
	}

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if f.legacy {
			err = c.collectLegacy(ctx, deps, f, &res)
		} else {
			err = c.collectJSONL(ctx, deps, f, &res)
		}
		if err != nil {
			return res, err
		}
		res.Files++
	}
	logger.Debug("gemini scan finished",
		"files", res.Files, "added", res.Added, "updated", res.Updated, "warnings", len(res.Warnings))
	return res, nil
}

// discover lists the session recordings under root. Project folders and
// chats directories that are symlinks are not followed. It also returns how
// many project folders were seen.
func discover(ctx context.Context, root string, res *collector.Result) (files []sessionFile, projects int, err error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, 0, err
	}
	for _, e := range entries {
		if cerr := ctx.Err(); cerr != nil {
			return nil, 0, cerr
		}
		if !e.IsDir() { // false for symlinks as well
			continue
		}
		projects++
		projDir := filepath.Join(root, e.Name())
		chats := filepath.Join(projDir, chatsDir)
		if fi, lerr := os.Lstat(chats); lerr != nil || !fi.IsDir() {
			continue
		}
		project := readProjectRoot(filepath.Join(projDir, projectRootFile), e.Name())

		jsonl, warnings, werr := ingest.WalkJSONL(ctx, chats)
		res.Warnings = append(res.Warnings, warnings...)
		if werr != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, 0, cerr
			}
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", chats, werr))
		}
		for _, p := range jsonl {
			files = append(files, sessionFile{path: p, project: project})
		}

		top, rerr := os.ReadDir(chats)
		if rerr != nil {
			continue
		}
		for _, f := range top {
			if f.Type().IsRegular() && strings.HasSuffix(f.Name(), ".json") {
				files = append(files, sessionFile{path: filepath.Join(chats, f.Name()), project: project, legacy: true})
			}
		}
	}
	return files, projects, nil
}

// readProjectRoot returns the project path Gemini CLI records in
// <project folder>/.project_root, or fallback (the folder name) when the file
// is missing, unreadable or empty.
func readProjectRoot(path, fallback string) string {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return fallback
	}
	f, err := os.Open(path)
	if err != nil {
		return fallback
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxProjectRootBytes))
	if err != nil {
		return fallback
	}
	if p := strings.TrimSpace(string(b)); p != "" {
		return p
	}
	return fallback
}

// fallbackSessionID is used until a metadata record names the session.
func fallbackSessionID(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// newContext starts the file context. Recordings nested below the chats
// directory (chats/<parent session>/<id>.jsonl) belong to sub-agents.
func newContext(f sessionFile) *fileContext {
	fc := &fileContext{sessionID: fallbackSessionID(f.path), project: f.project}
	if filepath.Base(filepath.Dir(f.path)) != chatsDir {
		fc.sidechain = true
	}
	return fc
}

// stopPriming ends the head read early; it is not a failure.
var stopPriming = errors.New("gemini: head read done")

// primeContext rebuilds the file context from the first lines of the file,
// which a resumed read skips: the metadata record that names the session and
// its kind sits at the top and the ingest state does not carry it.
func primeContext(ctx context.Context, path string, limit int64, fc *fileContext) {
	_, _ = ingest.ReadFile(ctx, path, nil, ingest.Options{}, func(line []byte, offset int64) error {
		if offset >= limit || fc.metaSeen {
			return stopPriming
		}
		if !interesting(line) {
			return nil
		}
		var l sessionLine
		if json.Unmarshal(line, &l) == nil {
			fc.observe(l)
		}
		return nil
	})
}

// batcher buffers records, keeps the last version of each external id and
// writes them in chunks.
type batcher struct {
	deps    collector.Deps
	res     *collector.Result
	pending []model.UsageRecord
	index   map[string]int
}

func newBatcher(deps collector.Deps, res *collector.Result) *batcher {
	return &batcher{deps: deps, res: res, index: map[string]int{}}
}

func (b *batcher) add(ctx context.Context, rec model.UsageRecord) error {
	if i, dup := b.index[rec.ExternalID]; dup {
		b.pending[i] = rec
		return nil
	}
	if len(b.pending) >= batchSize {
		if err := b.flush(ctx); err != nil {
			return err
		}
	}
	b.index[rec.ExternalID] = len(b.pending)
	b.pending = append(b.pending, rec)
	return nil
}

func (b *batcher) flush(ctx context.Context) error {
	if len(b.pending) == 0 {
		return nil
	}
	added, updated, err := b.deps.Store.InsertBatch(ctx, b.pending)
	if err != nil {
		return err
	}
	b.res.Added += added
	b.res.Updated += updated
	b.pending = b.pending[:0]
	clear(b.index)
	return nil
}

// warner caps the warnings of one file.
type warner struct {
	res  *collector.Result
	path string
	n    int
}

func (w *warner) warn(msg string) {
	w.n++
	if w.n <= maxWarningsPerFile {
		w.res.Warnings = append(w.res.Warnings, msg)
	}
}

func (w *warner) finish() {
	if w.n > maxWarningsPerFile {
		w.res.Warnings = append(w.res.Warnings,
			fmt.Sprintf("%s: %d more warnings suppressed", w.path, w.n-maxWarningsPerFile))
	}
}

// noTokenData emits the "no token data" warning when a scan saw model
// messages in a file but none of them carried usage. The last message is not
// counted when it has no tokens: it may be a response still in flight, whose
// tokens arrive with a re-append of the same message.
func noTokenData(w *warner, path string, seen map[string]bool, lastID string) {
	n := len(seen)
	for _, hasTokens := range seen {
		if hasTokens {
			return
		}
	}
	if _, ok := seen[lastID]; ok {
		n-- // known tokenless here: the loop above returned otherwise
	}
	if n > 0 {
		w.warn(fmt.Sprintf("%s: no token data in %d model message(s)", path, n))
	}
}

// collectJSONL ingests one JSONL session recording. A returned error is
// fatal for the run (store failure or cancellation); problems local to the
// file become warnings.
func (c *Collector) collectJSONL(ctx context.Context, deps collector.Deps, f sessionFile, res *collector.Result) error {
	prev, found, err := deps.Store.GetIngestState(ctx, model.SourceGeminiCLI, f.path)
	if err != nil {
		return err
	}
	prevPtr := &prev
	if !found {
		prevPtr = nil
	}

	var (
		fc     = newContext(f)
		primed bool
		b      = newBatcher(deps, res)
		w      = &warner{res: res, path: f.path}
		seen   = map[string]bool{} // model message id -> carries tokens
		lastID string              // the model message seen last
		fatal  error
	)
	fr, err := ingest.ReadFile(ctx, f.path, prevPtr, ingest.Options{}, func(line []byte, offset int64) error {
		if !primed {
			primed = true
			if offset > 0 {
				primeContext(ctx, f.path, offset, fc)
			}
		}
		results, perr := parseLine(line, fc, deps.DeviceID)
		if perr != nil {
			w.warn(fmt.Sprintf("%s: offset %d: %v", f.path, offset, perr))
		}
		for _, r := range results {
			seen[r.ID] = seen[r.ID] || r.Rec != nil
			lastID = r.ID
			if r.Rec == nil {
				continue
			}
			if aerr := b.add(ctx, *r.Rec); aerr != nil {
				fatal = aerr
				return aerr
			}
		}
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
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", f.path, err))
		return nil
	}
	if err := b.flush(ctx); err != nil {
		return err
	}
	for _, msg := range fr.Warnings {
		w.warn(msg)
	}
	// Only a read of the whole file can tell that it has no token data: a
	// resumed read may start between a message and the re-append that
	// carries its tokens (Gemini CLI writes them in two steps).
	if prevPtr == nil || fr.Reset {
		noTokenData(w, f.path, seen, lastID)
	}
	w.finish()

	// Nothing changed since the last run: skip the write, it would only
	// churn updated_at (and the WAL) once per file per scan.
	if found && prev.Offset == fr.State.Offset && prev.Size == fr.State.Size && prev.ModTime.Equal(fr.State.ModTime) {
		return nil
	}
	st := fr.State
	st.Source = model.SourceGeminiCLI
	return deps.Store.SetIngestState(ctx, st)
}

// collectLegacy ingests a legacy session, one JSON object holding every
// message that Gemini CLI rewrote on each save. It is read whole, and only
// when its size or mtime changed.
func (c *Collector) collectLegacy(ctx context.Context, deps collector.Deps, f sessionFile, res *collector.Result) error {
	w := &warner{res: res, path: f.path}
	prev, found, err := deps.Store.GetIngestState(ctx, model.SourceGeminiCLI, f.path)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(f.path)
	if err != nil || !fi.Mode().IsRegular() {
		w.warn(fmt.Sprintf("%s: not a readable regular file", f.path))
		return nil
	}
	if fi.Size() > maxLegacyFileBytes {
		w.warn(fmt.Sprintf("%s: skipped, larger than %d bytes", f.path, maxLegacyFileBytes))
		return nil
	}
	st := store.IngestState{Path: f.path, Offset: fi.Size(), Size: fi.Size(), ModTime: fi.ModTime().UTC()}
	if found && prev.Size == st.Size && prev.ModTime.Equal(st.ModTime) {
		return nil
	}

	file, err := os.Open(f.path)
	if err != nil {
		w.warn(fmt.Sprintf("%s: %v", f.path, err))
		return nil
	}
	// Read through the handle, bounded again: the file may have grown.
	data, err := io.ReadAll(io.LimitReader(file, maxLegacyFileBytes+1))
	file.Close()
	if err != nil {
		w.warn(fmt.Sprintf("%s: %v", f.path, err))
		return nil
	}
	if len(data) > maxLegacyFileBytes {
		w.warn(fmt.Sprintf("%s: skipped, larger than %d bytes", f.path, maxLegacyFileBytes))
		return nil
	}

	var l sessionLine
	if err := json.Unmarshal(data, &l); err != nil {
		w.warn(fmt.Sprintf("%s: invalid JSON: %v", f.path, scrub(err)))
		return nil
	}
	fc := newContext(f)
	results, perr := fc.handle(l, deps.DeviceID)
	if perr != nil {
		w.warn(fmt.Sprintf("%s: %v", f.path, perr))
	}
	b := newBatcher(deps, res)
	seen := map[string]bool{}
	lastID := ""
	for _, r := range results {
		seen[r.ID] = seen[r.ID] || r.Rec != nil
		lastID = r.ID
		if r.Rec == nil {
			continue
		}
		if err := b.add(ctx, *r.Rec); err != nil {
			return err
		}
	}
	if err := b.flush(ctx); err != nil {
		return err
	}
	if !found { // a rewritten legacy file may hold a message still awaiting its tokens
		noTokenData(w, f.path, seen, lastID)
	}

	st.Source = model.SourceGeminiCLI
	return deps.Store.SetIngestState(ctx, st)
}
