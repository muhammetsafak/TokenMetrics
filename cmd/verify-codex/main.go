// Command verify-codex cross-checks the Codex CLI totals TokenMetrics has
// stored against the raw rollout logs.
//
// It reads every rollout-*.jsonl under the Codex sessions directory itself and
// sums tokens per model. The reader here is deliberately independent from
// internal/collector/codex: it shares no code with it, so a bug in the
// application's parser shows up as a difference. The result is compared with
// the usage_records rows of the application database, model by model. The exit
// status is 1 when anything differs.
//
// Identity rule used for de-duplication (the same rule the application is
// meant to follow, see docs/design.md): a token_usage_record line is keyed by
// "r:<response_id>"; a token_count event, which carries no response id, by the
// session id plus the cumulative counters after it. When the same key appears
// more than once (re-emitted events, forked rollouts that copy their parent's
// history) the earliest record wins.
//
// The raw side also sums total_tokens: some Codex builds report only
// total_tokens and leave the input/output split at zero. The application stores
// such an entry as a "total-only" record (its count in total_only_tokens, the
// split columns 0, no cost computed). The tool applies the same rule on its own:
// an entry whose counters are all zero except total_tokens goes to the
// "total_only" column, and the "total" column compares total_tokens (raw) with
// the application's input + output + cache + total_only columns.
//
// The Codex directory is only ever read. The application database is opened
// read-only, except in -scan mode, which first runs the application's own scan
// twice into the data directory and therefore requires TOKENMETRICS_DATA_DIR
// to be set to a scratch directory (so the real database is never touched):
//
//	TOKENMETRICS_DATA_DIR=$(mktemp -d) go run ./cmd/verify-codex -scan
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
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/muhammetsafak/TokenMetrics/internal/app"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/paths"
)

const (
	sourceName   = "codex_cli"
	unknownModel = "unknown"
	envCodexHome = "CODEX_HOME"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "verify-codex:", err)
		if errors.Is(err, errInconclusive) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run() error {
	var (
		codexDir = flag.String("codex-dir", "", "Codex home directory (default $CODEX_HOME, else ~/.codex)")
		dbFlag   = flag.String("db", "", "application database (default: tokenmetrics.db in the data directory)")
		scan     = flag.Bool("scan", false, "run the application's Codex CLI scan twice into $"+paths.EnvDataDir+" first")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root, err := sessionsDir(*codexDir)
	if err != nil {
		return err
	}
	fmt.Println("codex logs  :", root)

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

// sessionsDir resolves the directory that holds the rollout files.
func sessionsDir(flagValue string) (string, error) {
	dir := flagValue
	if dir == "" {
		dir = os.Getenv(envCodexHome)
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		dir = filepath.Join(home, ".codex")
	}
	return filepath.Join(dir, "sessions"), nil
}

// dataDir mirrors the application's data directory rule without creating
// anything.
func dataDir() (string, error) {
	dir := os.Getenv(paths.EnvDataDir)
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve user config dir: %w", err)
		}
		dir = filepath.Join(base, "TokenMetrics")
	}
	return filepath.Abs(dir)
}

func defaultDBPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, paths.DBFileName), nil
}

// scanTwice runs the application's scan for the Codex CLI twice. The first
// run fills the database; the second must find nothing new (see docs/design.md).
func scanTwice(ctx context.Context, root string) (ok bool, err error) {
	if os.Getenv(paths.EnvDataDir) == "" {
		return false, fmt.Errorf("-scan writes to the application database: set %s to a scratch directory first", paths.EnvDataDir)
	}
	// Never write into the real database, even if the variable points at it.
	scratch, err := dataDir()
	if err != nil {
		return false, err
	}
	if base, cerr := os.UserConfigDir(); cerr == nil && scratch == filepath.Join(base, "TokenMetrics") {
		return false, fmt.Errorf("-scan refuses to run against the real data directory %s", scratch)
	}
	// The application reads CODEX_HOME itself; keep it in step with the
	// directory this tool compares against.
	if err := os.Setenv(envCodexHome, filepath.Dir(root)); err != nil {
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
		sum := svc.RunScanSources(ctx, id, []model.Source{model.SourceCodexCLI}, nil)
		if sum.Canceled {
			return false, ctx.Err()
		}
		var files, warnings int
		var stepErr string
		for _, st := range sum.Steps {
			if st.Source != string(model.SourceCodexCLI) {
				continue
			}
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
			fmt.Println("FAIL: the second scan must find nothing (added=0 updated=0) but changed records; a re-scan is not idempotent, or a live Codex session wrote in between (run again)")
			ok = false
		}
	}
	return ok, nil
}

// ---- totals ----------------------------------------------------------------

// totals are the token sums of one model.
type totals struct {
	Requests   int64
	Input      int64 // input excluding cached and cache-write tokens
	Output     int64
	CacheRead  int64
	CacheWrite int64
	TotalOnly  int64 // tokens of entries that carry only total_tokens
	Total      int64 // input + output + cache + total-only columns (app) or total_tokens (raw)
}

func (t *totals) add(o totals) {
	t.Requests += o.Requests
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
	t.TotalOnly += o.TotalOnly
	t.Total += o.Total
}

func (t totals) sub(o totals) totals {
	return totals{
		Requests:   t.Requests - o.Requests,
		Input:      t.Input - o.Input,
		Output:     t.Output - o.Output,
		CacheRead:  t.CacheRead - o.CacheRead,
		CacheWrite: t.CacheWrite - o.CacheWrite,
		TotalOnly:  t.TotalOnly - o.TotalOnly,
		Total:      t.Total - o.Total,
	}
}

func (t totals) zero() bool { return t == totals{} }

// negative reports whether any column is below zero.
func (t totals) negative() bool {
	return t.Requests < 0 || t.Input < 0 || t.Output < 0 || t.CacheRead < 0 || t.CacheWrite < 0 || t.TotalOnly < 0 || t.Total < 0
}

// fileState is what the application recorded about one log file.
type fileState struct {
	Offset int64
	Size   int64
}

// ---- database side ---------------------------------------------------------

// readDB reads the per-model sums and the file states of the Codex source. The
// database is opened read-only.
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
		       SUM(cache_read_tokens), SUM(cache_write_5m_tokens + cache_write_1h_tokens),
		       SUM(total_only_tokens)
		FROM usage_records WHERE source = ? GROUP BY model`, sourceName)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("read usage_records: %w", err)
	}
	defer rows.Close()
	sums := map[string]totals{}
	for rows.Next() {
		var name string
		var t totals
		if err := rows.Scan(&name, &t.Requests, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite, &t.TotalOnly); err != nil {
			return nil, nil, time.Time{}, err
		}
		t.Total = t.Input + t.Output + t.CacheRead + t.CacheWrite + t.TotalOnly
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
	Files          int // rollout *.jsonl files found
	Compressed     int // of Files, how many are *.jsonl.zst
	Compared       int // files read up to the recorded size
	NotScanned     int // newer than the application's last read; skipped
	Unread         int // older than the application's last read, yet never recorded by it
	Vanished       int // known to the application but gone from disk
	Changed        int // smaller than when the application read them
	OffsetMismatch int // application offset differs from the last complete line
	WithTurnCtx    int // files with at least one turn_context line
	WithRecords    int // files with at least one token_usage_record line
	CountEvents    int // token_count events with a last_token_usage
	RecordLines    int // token_usage_record lines
	ZeroEvents     int // usage entries whose counters are all zero
	TotalOnly      int // usage entries with total_tokens but no input/output split
	Anomalies      int // cached + cache-write larger than input
	UniqueKeys     int
	Repeated       int // keys seen more than once
	ConflictingDup int // keys repeated with different values
	CrossSession   int // identical (timestamp, counters) pairs under different sessions
	ModelSources   map[string]int
}

func (s logStats) print() {
	fmt.Printf("log files   : %d found (%d of them compressed), %d compared, %d not scanned yet, %d vanished, %d smaller than scanned\n",
		s.Files, s.Compressed, s.Compared, s.NotScanned, s.Vanished, s.Changed)
	fmt.Printf("line kinds  : %d token_count events, %d token_usage_record lines; %d files with turn_context, %d with token_usage_record\n",
		s.CountEvents, s.RecordLines, s.WithTurnCtx, s.WithRecords)
	fmt.Printf("usage keys  : %d unique, %d repeated (%d with different values), %d identical across sessions\n",
		s.UniqueKeys, s.Repeated, s.ConflictingDup, s.CrossSession)
	fmt.Printf("shape       : %d entries all-zero, %d total_tokens-only (no input/output split), %d cache > input\n",
		s.ZeroEvents, s.TotalOnly, s.Anomalies)
	if len(s.ModelSources) > 0 {
		names := make([]string, 0, len(s.ModelSources))
		for n := range s.ModelSources {
			names = append(names, n)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, n := range names {
			parts = append(parts, fmt.Sprintf("%s=%d", n, s.ModelSources[n]))
		}
		fmt.Printf("model from  : %s (files)\n", strings.Join(parts, ", "))
	}
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

// usage mirrors the counters of a Codex usage object. input_tokens includes
// the cached and cache-write tokens; output_tokens includes the reasoning
// tokens.
type usage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
	Total      int64 `json:"total_tokens"`
}

func (u usage) zero() bool { return u == usage{} }

// totalOnly reports whether total_tokens is the only counter set.
func (u usage) totalOnly() bool {
	return u.Total > 0 && u.Input == 0 && u.Cached == 0 && u.CacheWrite == 0 && u.Output == 0 && u.Reasoning == 0
}

type line struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type payload struct {
	Type string `json:"type"`
	// session_meta
	ID   string `json:"id"`
	Base *struct {
		Provenance *struct {
			Model string `json:"model"`
		} `json:"provenance"`
	} `json:"base_instructions"`
	// turn_context and thread_settings_applied
	Model    string `json:"model"`
	Settings *struct {
		Model string `json:"model"`
	} `json:"thread_settings"`
	// event_msg token_count
	Info *struct {
		Total *usage `json:"total_token_usage"`
		Last  *usage `json:"last_token_usage"`
	} `json:"info"`
	// token_usage_record
	ResponseID string `json:"response_id"`
	Usage      *usage `json:"usage"`
}

// candidate is one usage entry before de-duplication.
type candidate struct {
	ts    time.Time
	order string // file path + zero-padded line number: tie-breaker
	model string
	u     usage
	sess  string
}

// readLogs de-duplicates the usage entries of all rollouts by identity key and
// returns the per-model sums. Files are visited in lexical path order; for a
// repeated key the earliest timestamp wins (ties: earlier file, earlier line).
func readLogs(ctx context.Context, root string, states map[string]fileState, excuseAfter time.Time) (map[string]totals, logStats, error) {
	st := logStats{ModelSources: map[string]int{}}
	files, err := listRollouts(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, st, fmt.Errorf("codex session directory not found: %s", root)
		}
		return nil, st, err
	}
	st.Files = len(files)
	for _, p := range files {
		if isCompressed(p) {
			st.Compressed++
		}
	}

	onDisk := make(map[string]struct{}, len(files))
	byKey := map[string]candidate{}
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
		// A compressed rollout is immutable and read whole; its recorded size
		// is the compressed size, so any difference means it was replaced
		// after the application read it.
		stale := false
		if isCompressed(path) {
			if fi.Size() != limit {
				st.Changed++
				stale = true
			}
		} else if fi.Size() < limit {
			st.Changed++
			limit = fi.Size()
		}
		consumed, nb, err := readFile(ctx, path, limit, byKey, &st, buf)
		if err != nil {
			return nil, st, fmt.Errorf("%s: %w", path, err)
		}
		buf = nb
		st.Compared++
		if consumed != state.Offset && !stale {
			st.OffsetMismatch++
		}
	}
	for p := range states {
		if _, ok := onDisk[p]; !ok {
			st.Vanished++
		}
	}

	st.UniqueKeys = len(byKey)
	type tsKey struct {
		ts string
		u  usage
	}
	sessions := map[tsKey]string{}
	sums := map[string]totals{}
	for _, c := range byKey {
		k := tsKey{c.ts.Format(time.RFC3339Nano), c.u}
		if other, ok := sessions[k]; ok && other != c.sess {
			st.CrossSession++
		}
		sessions[k] = c.sess

		cached := clamp(c.u.Cached, c.u.Input)
		written := clamp(c.u.CacheWrite, c.u.Input-cached)
		if c.u.Cached+c.u.CacheWrite > c.u.Input {
			st.Anomalies++
		}
		var totalOnly int64
		if c.u.totalOnly() {
			totalOnly = c.u.Total
		}
		t := sums[c.model]
		t.add(totals{
			Requests:   1,
			Input:      c.u.Input - cached - written,
			Output:     c.u.Output,
			CacheRead:  cached,
			CacheWrite: written,
			TotalOnly:  totalOnly,
			Total:      c.u.Total,
		})
		sums[c.model] = t
	}
	return sums, st, nil
}

func clamp(v, hi int64) int64 { return min(max(v, 0), max(hi, 0)) }

// isCompressed reports whether path is a zstd-compressed rollout.
func isCompressed(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".jsonl.zst")
}

// listRollouts returns the regular rollout files (*.jsonl and *.jsonl.zst)
// under root, symlinks excluded, in lexical order.
func listRollouts(root string) (files []string, err error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
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
		if !d.Type().IsRegular() {
			return nil
		}
		if name := strings.ToLower(d.Name()); strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".jsonl.zst") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// Substrings a line must contain to be worth decoding; rollout lines can be
// megabytes of tool output.
var markers = [][]byte{
	[]byte(`"session_meta"`),
	[]byte(`"turn_context"`),
	[]byte(`"thread_settings_applied"`),
	[]byte(`"token_usage_record"`),
	[]byte(`"token_count"`),
}

func interesting(b []byte) bool {
	for _, m := range markers {
		if bytes.Contains(b, m) {
			return true
		}
	}
	return false
}

// fileCtx is what one rollout says about itself.
type fileCtx struct {
	session    string
	metaSeen   bool
	metaModel  string // session_meta provenance model
	threadMod  string // thread_settings_applied model
	turnModel  string // latest turn_context model
	hasRecords bool
	hasTurnCtx bool
}

func (c *fileCtx) model() (name, from string) {
	switch {
	case c.turnModel != "":
		return c.turnModel, "turn_context"
	case c.threadMod != "":
		return c.threadMod, "thread_settings"
	case c.metaModel != "":
		return c.metaModel, "session_meta"
	}
	return unknownModel, "none"
}

// maxDecompressed caps what one compressed rollout may expand to.
const maxDecompressed = 2 << 30

// errTooLarge reports a compressed rollout that expands beyond maxDecompressed.
var errTooLarge = errors.New("decompressed size exceeds the limit")

// capReader fails with errTooLarge once more than left bytes would be read.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		n, err := c.r.Read(one[:])
		if n > 0 {
			return 0, errTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// readFile folds the usage entries found in the first limit bytes of path into
// byKey. Only complete (newline-terminated) lines count. It returns the offset
// just after the last complete line. A compressed rollout is decoded in full
// (limit is ignored) and the offset counts decompressed bytes.
//
// Newer Codex versions write both a token_usage_record (exact, with the
// response id) and a token_count event for every response. From the first
// token_usage_record of a file on, its token_count events are therefore
// redundant; token_count events before it are genuine responses of their own.
func readFile(ctx context.Context, path string, limit int64, byKey map[string]candidate, st *logStats, buf []byte) (int64, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, buf, err
	}
	defer f.Close()

	var (
		fc       = fileCtx{session: fallbackSession(path)}
		counts   []candidate // token_count entries, key resolved later
		countTot []usage
		records  = map[string]candidate{}
		lineNo   int
		offset   int64
	)
	var src io.Reader = io.LimitReader(f, limit)
	if isCompressed(path) {
		dec, err := zstd.NewReader(f,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxMemory(256<<20),
			zstd.WithDecoderMaxWindow(128<<20))
		if err != nil {
			return 0, buf, fmt.Errorf("zstd: %w", err)
		}
		defer dec.Close()
		src = &capReader{r: dec, left: maxDecompressed}
	}
	r := bufio.NewReaderSize(src, 256<<10)
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
			break
		}
		offset += int64(len(buf))
		lineNo++
		if err := ctx.Err(); err != nil {
			return offset, buf, err
		}
		if !interesting(buf) {
			continue
		}
		var l line
		if json.Unmarshal(buf, &l) != nil {
			continue
		}
		var p payload
		if json.Unmarshal(l.Payload, &p) != nil {
			continue
		}
		order := fmt.Sprintf("%s#%09d", path, lineNo)
		switch l.Type {
		case "session_meta":
			if fc.metaSeen {
				continue // a fork repeats its parent's header
			}
			fc.metaSeen = true
			if p.ID != "" {
				fc.session = p.ID
			}
			if p.Base != nil && p.Base.Provenance != nil {
				fc.metaModel = p.Base.Provenance.Model
			}
		case "turn_context":
			fc.hasTurnCtx = true
			if p.Model != "" {
				fc.turnModel = p.Model
			}
		case "token_usage_record":
			fc.hasRecords = true
			st.RecordLines++
			if p.Usage == nil || p.ResponseID == "" {
				continue
			}
			ts, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
			name, _ := fc.model()
			c := candidate{ts: ts, order: order, model: name, u: *p.Usage, sess: fc.session}
			key := "r:" + p.ResponseID
			if prev, dup := records[key]; !dup || earlier(c, prev) {
				records[key] = c
			}
		case "event_msg":
			switch p.Type {
			case "thread_settings_applied":
				if p.Settings != nil && p.Settings.Model != "" {
					fc.threadMod = p.Settings.Model
				}
			case "token_count":
				if p.Info == nil || p.Info.Last == nil {
					continue
				}
				st.CountEvents++
				if fc.hasRecords {
					continue // the record before it is exact and already keyed
				}
				ts, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
				name, _ := fc.model()
				counts = append(counts, candidate{ts: ts, order: order, model: name, u: *p.Info.Last, sess: fc.session})
				var tot usage
				if p.Info.Total != nil {
					tot = *p.Info.Total
				}
				countTot = append(countTot, tot)
			}
		}
	}

	if fc.hasTurnCtx {
		st.WithTurnCtx++
	}
	if fc.hasRecords {
		st.WithRecords++
	}
	_, from := fc.model()
	st.ModelSources[from]++

	fold := func(key string, c candidate) {
		if c.u.zero() {
			st.ZeroEvents++
			return
		}
		if c.u.totalOnly() {
			st.TotalOnly++
		}
		if prev, dup := byKey[key]; dup {
			st.Repeated++
			if prev.u != c.u || prev.model != c.model {
				st.ConflictingDup++
			}
			if !earlier(c, prev) {
				return
			}
		}
		byKey[key] = c
	}
	for key, c := range records {
		fold(key, c)
	}
	for i, c := range counts {
		t := countTot[i]
		fold(fmt.Sprintf("%s:c:%d.%d.%d", c.sess, t.Total, t.Input, t.Output), c)
	}
	return offset, buf, nil
}

// rolloutFile matches rollout-<YYYY-MM-DDThh-mm-ss>-<thread id>[_<suffix>].jsonl[.zst].
var rolloutFile = regexp.MustCompile(`^rollout-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-([^_.]+)(?:_[^.]*)?\.jsonl(?:\.zst)?$`)

// fallbackSession names a rollout's session until its session_meta line does:
// the thread id in the file name, else the file name without extension.
func fallbackSession(path string) string {
	base := filepath.Base(path)
	if m := rolloutFile.FindStringSubmatch(base); m != nil {
		return m[1]
	}
	base = strings.TrimSuffix(base, ".zst")
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func earlier(a, b candidate) bool {
	if !a.ts.Equal(b.ts) {
		return a.ts.Before(b.ts)
	}
	return a.order < b.order
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
	fmt.Fprintln(w, "model\tview\trequests\tinput\toutput\tcache_read\tcache_write\ttotal_only\ttotal\t")
	var rawTotal, dbTotal totals
	mismatched := 0
	shortfall := false // some column of the application is below the raw logs
	line := func(model, view string, t totals) {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t\n", model, view, t.Requests, t.Input, t.Output, t.CacheRead, t.CacheWrite, t.TotalOnly, t.Total)
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
