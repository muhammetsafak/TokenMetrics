// Command verify-claude cross-checks the Claude Code totals TokenMetrics has
// stored against the raw session logs.
//
// It reads every ~/.claude/projects/**/*.jsonl itself, de-duplicates the
// assistant entries by message id and sums tokens per model. The parser here
// is deliberately independent from internal/collector/claudecode: it shares no
// code with it, so a bug in the application's parser shows up as a difference.
// The result is compared with the usage_records rows of the application
// database, model by model. The exit status is 1 when anything differs.
//
// Claude's directory is only ever read. The application database is opened
// read-only, except in -scan mode, which first runs the application's own
// scan twice into the data directory and therefore requires
// TOKENMETRICS_DATA_DIR to be set (so the real database is never touched by
// accident):
//
//	TOKENMETRICS_DATA_DIR=$(mktemp -d) go run ./cmd/verify-claude -scan
//
// Logs of a live session keep growing while this tool runs. To compare like
// with like, each file is read up to the size the application recorded when it
// last read the file (ingest_state). Only files created after the
// application's last read are left out (and reported); an older file the
// application never recorded, or one that shrank, is a failure. When files the
// application had read are gone from disk their records remain in the database
// (see docs/design.md), so a difference is reported as INCONCLUSIVE (exit status 2).
package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/muhammetsafak/TokenMetrics/internal/app"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/paths"
)

const (
	sourceName   = "claude_code"
	unknownModel = "unknown"
	synthetic    = "<synthetic>"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "verify-claude:", err)
		if errors.Is(err, errInconclusive) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run() error {
	var (
		claudeDir = flag.String("claude-dir", "", "Claude config directory (default $CLAUDE_CONFIG_DIR, else ~/.claude)")
		dbFlag    = flag.String("db", "", "application database (default: tokenmetrics.db in the data directory)")
		scan      = flag.Bool("scan", false, "run the application's Claude Code scan twice into $"+paths.EnvDataDir+" first")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root, err := projectsDir(*claudeDir)
	if err != nil {
		return err
	}
	fmt.Println("claude logs :", root)

	failed := false
	var scanStart time.Time
	if *scan {
		scanStart = time.Now()
		ok, err := scanTwice(ctx, root)
		if err != nil {
			return err
		}
		failed = !ok
	}

	dbPath := *dbFlag
	if dbPath == "" {
		if dbPath, err = defaultDBPath(); err != nil {
			return err
		}
	}
	fmt.Println("database    :", dbPath)

	appRows, states, lastScan, err := readDB(ctx, dbPath)
	if err != nil {
		return err
	}
	// Files created after this instant (a live session) may legitimately be
	// unknown to the application. With -scan it is the start of our own scan.
	// Against an existing database it is the newest ingest_state update, which
	// is when the application last saw a file change, not when it last scanned;
	// a zero value (empty ingest_state) excuses nothing. Use -scan for a strict
	// check.
	excuseAfter := lastScan
	if !scanStart.IsZero() {
		excuseAfter = scanStart
	}
	raw, stats, err := readLogs(ctx, root, states, excuseAfter)
	if err != nil {
		return err
	}
	stats.print()

	switch result := report(raw, appRows, stats); {
	case failed || result == resultMismatch:
		return errors.New("verification failed")
	case result == resultInconclusive:
		return errInconclusive
	}
	return nil
}

// errInconclusive is returned when the comparison cannot say either way; it
// exits with status 2 instead of 1.
var errInconclusive = errors.New("verification inconclusive")

// projectsDir resolves the directory that holds the project logs.
func projectsDir(flagValue string) (string, error) {
	dir := flagValue
	if dir == "" {
		dir = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, "projects"), nil
}

// defaultDBPath mirrors the application's data directory rule without
// creating anything.
func defaultDBPath() (string, error) {
	dir := os.Getenv(paths.EnvDataDir)
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve user config dir: %w", err)
		}
		dir = filepath.Join(base, "TokenMetrics")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, paths.DBFileName), nil
}

// scanTwice runs the application's scan for Claude Code twice. The first run
// fills the database; the second must find nothing new (see docs/design.md).
func scanTwice(ctx context.Context, root string) (ok bool, err error) {
	if os.Getenv(paths.EnvDataDir) == "" {
		return false, fmt.Errorf("-scan writes to the application database: set %s to a scratch directory first", paths.EnvDataDir)
	}
	// The application reads CLAUDE_CONFIG_DIR itself; keep it in step with the
	// directory this tool compares against.
	if err := os.Setenv("CLAUDE_CONFIG_DIR", filepath.Dir(root)); err != nil {
		return false, err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svc, err := app.Open(ctx, logger)
	if err != nil {
		return false, fmt.Errorf("open application service: %w", err)
	}
	defer svc.Close()

	ok = true
	for i, label := range []string{"first scan ", "second scan"} {
		id, started := svc.BeginScan()
		if !started {
			return false, errors.New("a scan is already running")
		}
		sum := svc.RunScanSources(ctx, id, []model.Source{model.SourceClaudeCode}, nil)
		if sum.Canceled {
			return false, ctx.Err()
		}
		var files, warnings int
		var stepErr string
		for _, st := range sum.Steps {
			files += st.Files
			warnings += len(st.Warnings)
			if st.Status == app.StatusError {
				stepErr = st.Error
			}
		}
		fmt.Printf("%s: files=%d added=%d updated=%d warnings=%d\n", label, files, sum.Added, sum.Updated, warnings)
		if stepErr != "" {
			return false, fmt.Errorf("%s failed: %s", strings.TrimSpace(label), stepErr)
		}
		if i == 1 && (sum.Added != 0 || sum.Updated != 0) {
			fmt.Println("FAIL: the second scan must find nothing (added=0 updated=0) but changed records; a re-scan is not idempotent, or a live Claude Code session wrote in between (run again)")
			ok = false
		}
	}
	return ok, nil
}

// ---- database side ---------------------------------------------------------

// totals are the token sums of one model.
type totals struct {
	Requests   int64
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64 // 5m + 1h
}

func (t *totals) add(o totals) {
	t.Requests += o.Requests
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
}

func (t totals) sub(o totals) totals {
	return totals{
		Requests:   t.Requests - o.Requests,
		Input:      t.Input - o.Input,
		Output:     t.Output - o.Output,
		CacheRead:  t.CacheRead - o.CacheRead,
		CacheWrite: t.CacheWrite - o.CacheWrite,
	}
}

func (t totals) zero() bool { return t == totals{} }

// negative reports whether any column is below zero.
func (t totals) negative() bool {
	return t.Requests < 0 || t.Input < 0 || t.Output < 0 || t.CacheRead < 0 || t.CacheWrite < 0
}

// fileState is what the application recorded about one log file.
type fileState struct {
	Offset int64
	Size   int64
}

// readDB reads the per-model sums and the file states of the Claude Code
// source. The database is opened read-only.
func readDB(ctx context.Context, path string) (map[string]totals, map[string]fileState, time.Time, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("database not found (has the application scanned yet? try -scan): %w", err)
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
		SELECT model, SUM(request_count), SUM(input_tokens), SUM(output_tokens),
		       SUM(cache_read_tokens), SUM(cache_write_5m_tokens + cache_write_1h_tokens)
		FROM usage_records WHERE source = ? GROUP BY model`, sourceName)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("read usage_records: %w", err)
	}
	defer rows.Close()
	sums := map[string]totals{}
	for rows.Next() {
		var name string
		var t totals
		if err := rows.Scan(&name, &t.Requests, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite); err != nil {
			return nil, nil, time.Time{}, err
		}
		sums[name] = t
	}
	if err := rows.Err(); err != nil {
		return nil, nil, time.Time{}, err
	}

	srows, err := db.QueryContext(ctx, `SELECT path, "offset", size, updated_at FROM ingest_state WHERE source = ?`, sourceName)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("read ingest_state: %w", err)
	}
	defer srows.Close()
	states := map[string]fileState{}
	var lastMs int64 // newest updated_at: when the application last read a file
	for srows.Next() {
		var p string
		var s fileState
		var updated int64
		if err := srows.Scan(&p, &s.Offset, &s.Size, &updated); err != nil {
			return nil, nil, time.Time{}, err
		}
		states[p] = s
		lastMs = max(lastMs, updated)
	}
	var last time.Time // zero when nothing was recorded
	if lastMs > 0 {
		last = time.UnixMilli(lastMs)
	}
	return sums, states, last, srows.Err()
}

// ---- raw log side ----------------------------------------------------------

type logStats struct {
	Files          int // *.jsonl files found
	Compared       int // files read up to the recorded size
	NotScanned     int // newer than the application's last read; skipped
	Unread         int // older than the application's last read, yet never recorded by it
	Vanished       int // known to the application but gone from disk
	Changed        int // smaller than when the application read them
	OffsetMismatch int // application offset differs from the last complete line
	UsageLines     int // assistant lines with usage
	NoID           int // usage lines without any id; cannot be counted
	UniqueIDs      int
	ConflictingDup int // ids repeated with different values
}

func (s logStats) print() {
	fmt.Printf("log files   : %d found, %d compared, %d not scanned yet, %d vanished, %d smaller than scanned\n",
		s.Files, s.Compared, s.NotScanned, s.Vanished, s.Changed)
	fmt.Printf("usage lines : %d (%d unique message ids, %d ids repeated with different values, %d without id)\n",
		s.UsageLines, s.UniqueIDs, s.ConflictingDup, s.NoID)
	if s.OffsetMismatch > 0 {
		fmt.Printf("WARN: %d files where the stored offset is not the end of the last complete line\n", s.OffsetMismatch)
	}
	if s.Unread > 0 {
		fmt.Printf("WARN: %d log files are older than the application's last read but were never recorded by it\n", s.Unread)
	}
	if s.Changed > 0 {
		fmt.Printf("WARN: %d log files are smaller than when the application read them\n", s.Changed)
	}
}

type usageLine struct {
	Type      string `json:"type"`
	UUID      string `json:"uuid"`
	RequestID string `json:"requestId"`
	Message   *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			Breakdown     *struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

type entry struct {
	model string
	t     totals
}

// readLogs de-duplicates the assistant entries of all logs by message id and
// returns the per-model sums. Files are visited in lexical path order and the
// last occurrence of an id wins, like repeated content-block lines of one
// message do.
func readLogs(ctx context.Context, root string, states map[string]fileState, excuseAfter time.Time) (map[string]totals, logStats, error) {
	var st logStats
	files, err := listJSONL(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, st, fmt.Errorf("claude log directory not found: %s", root)
		}
		return nil, st, err
	}
	st.Files = len(files)

	onDisk := make(map[string]struct{}, len(files))
	byID := map[string]entry{}
	var buf []byte
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return nil, st, err
		}
		onDisk[path] = struct{}{}
		state, known := states[path]
		if !known {
			// A file the application never recorded is only excusable when it
			// appeared after excuseAfter (a live session). Anything older, or
			// any file at all when excuseAfter is zero, was missed by the
			// application's file discovery.
			if fi, err := os.Stat(path); err == nil && !excuseAfter.IsZero() && fi.ModTime().After(excuseAfter) {
				st.NotScanned++
			} else {
				st.Unread++
			}
			continue
		}
		limit := state.Size
		fi, err := os.Stat(path)
		if err != nil {
			st.Vanished++
			continue
		}
		if fi.Size() < limit {
			st.Changed++
			limit = fi.Size()
		}
		consumed, nb, err := readFile(ctx, path, limit, byID, &st, buf)
		if err != nil {
			return nil, st, fmt.Errorf("%s: %w", path, err)
		}
		buf = nb
		st.Compared++
		if consumed != state.Offset {
			st.OffsetMismatch++
		}
	}
	for p := range states {
		if _, ok := onDisk[p]; !ok {
			st.Vanished++
		}
	}

	st.UniqueIDs = len(byID)
	sums := map[string]totals{}
	for _, e := range byID {
		t := sums[e.model]
		t.add(e.t)
		sums[e.model] = t
	}
	return sums, st, nil
}

// listJSONL returns the regular *.jsonl files under root, symlinks excluded,
// in lexical order.
func listJSONL(root string) ([]string, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	var files []string
	err = filepath.WalkDir(resolved, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if p == resolved {
				return werr
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && strings.EqualFold(filepath.Ext(p), ".jsonl") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// readFile folds the usage lines found in the first limit bytes of path into
// byID. Only complete (newline-terminated) lines count. It returns the offset
// just after the last complete line.
func readFile(ctx context.Context, path string, limit int64, byID map[string]entry, st *logStats, buf []byte) (int64, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, buf, err
	}
	defer f.Close()

	r := bufio.NewReaderSize(io.LimitReader(f, limit), 256<<10)
	var offset int64
	usageKey := []byte(`"usage"`)
	for {
		buf = buf[:0]
		complete := false
		for {
			chunk, rerr := r.ReadSlice('\n')
			buf = append(buf, chunk...)
			if rerr == nil {
				complete = true
				break
			}
			if errors.Is(rerr, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(rerr, io.EOF) {
				break
			}
			return offset, buf, rerr
		}
		if !complete {
			return offset, buf, nil
		}
		offset += int64(len(buf))
		if err := ctx.Err(); err != nil {
			return offset, buf, err
		}
		if !bytes.Contains(buf, usageKey) {
			continue
		}
		var l usageLine
		if json.Unmarshal(buf, &l) != nil {
			continue
		}
		if l.Type != "assistant" || l.Message == nil || l.Message.Usage == nil || l.Message.Model == synthetic {
			continue
		}
		st.UsageLines++
		id := l.Message.ID
		if id == "" {
			id = l.RequestID
		}
		if id == "" {
			id = l.UUID
		}
		if id == "" {
			st.NoID++
			continue
		}
		u := l.Message.Usage
		cacheWrite := u.CacheCreation
		if cacheWrite == 0 && u.Breakdown != nil {
			cacheWrite = u.Breakdown.Ephemeral5m + u.Breakdown.Ephemeral1h
		}
		name := l.Message.Model
		if name == "" {
			name = unknownModel
		}
		e := entry{model: name, t: totals{
			Requests: 1, Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: cacheWrite,
		}}
		if prev, dup := byID[id]; dup && prev != e {
			st.ConflictingDup++
		}
		byID[id] = e
	}
}

// ---- comparison ------------------------------------------------------------

type result int

const (
	resultMatch result = iota
	resultMismatch
	// resultInconclusive: the totals differ, but only because log files the
	// application had read are gone from disk. Their records stay in the
	// database (see docs/design.md), so the two sides cannot be compared.
	resultInconclusive
)

// report prints the per-model comparison and classifies the outcome.
func report(raw, db map[string]totals, st logStats) result {
	names := map[string]struct{}{}
	for n := range raw {
		names[n] = struct{}{}
	}
	for n := range db {
		names[n] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight|tabwriter.Debug)
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(w, "model\tview\trequests\tinput\toutput\tcache_read\tcache_write\t")
	var rawTotal, dbTotal totals
	mismatched := 0
	shortfall := false // some column of the application is below the raw logs
	line := func(model, view string, t totals) {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t\n", model, view, t.Requests, t.Input, t.Output, t.CacheRead, t.CacheWrite)
	}
	for _, n := range sorted {
		r, d := raw[n], db[n]
		rawTotal.add(r)
		dbTotal.add(d)
		diff := d.sub(r)
		line(n, "raw", r)
		line("", "app", d)
		line("", "diff", diff)
		if diff.negative() {
			shortfall = true
		}
		if !diff.zero() {
			mismatched++
		}
	}
	line("TOTAL", "raw", rawTotal)
	line("", "app", dbTotal)
	line("", "diff", dbTotal.sub(rawTotal))
	w.Flush()

	fmt.Println()
	// Files that were skipped or shrank make the comparison incomplete: they
	// are failures, never silently excluded.
	incomplete := st.OffsetMismatch + st.Unread + st.Changed
	if st.Files > 0 && st.Compared == 0 {
		incomplete++ // nothing was compared; that is never a match
	}
	shortfall = shortfall || dbTotal.sub(rawTotal).negative()
	differs := mismatched > 0 || !dbTotal.sub(rawTotal).zero()
	switch {
	case !differs && incomplete == 0:
		fmt.Printf("RESULT: MATCH. %d models, difference 0 in every column.\n", len(sorted))
		return resultMatch
	// Records of deleted logs stay in the database, so vanished files can only
	// make the application higher than the logs. Any column where it is lower
	// is a real undercount.
	case differs && !shortfall && incomplete == 0 && st.Vanished > 0:
		fmt.Printf("RESULT: INCONCLUSIVE. %d log files the application read are gone from disk; their records stay in the database (see docs/design.md), so the totals cannot be compared.\n", st.Vanished)
		fmt.Println("Use -scan with a scratch data directory to compare like with like.")
		return resultInconclusive
	}
	fmt.Printf("RESULT: MISMATCH. %d of %d models differ, %d offset mismatches, %d unread files, %d shrunken files.\n",
		mismatched, len(sorted), st.OffsetMismatch, st.Unread, st.Changed)
	if st.Files > 0 && st.Compared == 0 {
		fmt.Printf("Nothing was compared: %d log files found, none recorded by the application.\n", st.Files)
	}
	return resultMismatch
}
