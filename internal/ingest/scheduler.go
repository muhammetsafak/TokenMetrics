package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Defaults of a Scheduler.
const (
	// DefaultDebounce is how long the scheduler waits after the first file
	// event before it runs the task, so a burst of writes costs one run.
	DefaultDebounce = 2 * time.Second
	// DefaultRetryDelay is how long a task waits after it reported ErrBusy.
	DefaultRetryDelay = 3 * time.Second
	// DefaultMaxWatchDirs bounds the directories one task watches. The rest
	// is left to the periodic run.
	DefaultMaxWatchDirs = 2048
	// DefaultMaxWatchFiles bounds the files that sit in the watched
	// directories on platforms whose watcher spends one file descriptor per
	// file (kqueue: macOS and the BSDs). With thousands of logs it would
	// otherwise starve the process of descriptors.
	DefaultMaxWatchFiles = 1000
)

// kqueuePlatform reports whether fsnotify watches with kqueue here.
func kqueuePlatform() bool {
	switch runtime.GOOS {
	case "darwin", "freebsd", "openbsd", "netbsd", "dragonfly":
		return true
	}
	return false
}

// ErrBusy is returned by Task.Run when the work could not start because
// something else holds the resource (for example another scan). The scheduler
// retries after Config.RetryDelay.
var ErrBusy = errors.New("ingest: busy")

// Task is one unit of background work: a collector run that is triggered by
// changes below Roots and, as a backstop, by a timer.
type Task struct {
	// Name identifies the task (Trigger, log lines). Must be unique.
	Name string
	// Roots lists the directories to watch recursively. It is evaluated at
	// start and again before every periodic run, so a path that changes in
	// the settings or appears later is picked up. Nil: no file watching.
	Roots func(ctx context.Context) []string
	// Interval is the period of the backstop run. It is evaluated after every
	// run. Nil or <= 0: no periodic run.
	Interval func(ctx context.Context) time.Duration
	// Run does the work. It is never called concurrently with itself.
	Run func(ctx context.Context) error
}

// Config configures a Scheduler. The zero value uses the defaults.
type Config struct {
	Debounce     time.Duration
	RetryDelay   time.Duration
	MaxWatchDirs int
	// MaxWatchFiles caps the files in watched directories. 0: the default
	// (DefaultMaxWatchFiles on kqueue platforms, no cap elsewhere); < 0: no
	// cap. A directory that would exceed it is not watched; the periodic run
	// covers it.
	MaxWatchFiles int
	Logger        *slog.Logger
}

// Scheduler runs Tasks in the background. Per task it guarantees that Run
// never overlaps with itself and that a burst of triggers while it runs ends
// in exactly one more run. Different tasks run independently of each other;
// whoever owns the shared resource (the scan slot of the app service) answers
// ErrBusy to serialize them.
type Scheduler struct {
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	runners map[string]*runner
	order   []string
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewScheduler returns a Scheduler.
func NewScheduler(cfg Config) *Scheduler {
	if cfg.Debounce <= 0 {
		cfg.Debounce = DefaultDebounce
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = DefaultRetryDelay
	}
	if cfg.MaxWatchDirs <= 0 {
		cfg.MaxWatchDirs = DefaultMaxWatchDirs
	}
	if cfg.MaxWatchFiles == 0 && kqueuePlatform() {
		cfg.MaxWatchFiles = DefaultMaxWatchFiles
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Scheduler{cfg: cfg, log: log, runners: map[string]*runner{}}
}

// Add registers a task. It fails after Start, for a nameless or duplicate
// task, and for a task without Run.
func (s *Scheduler) Add(t Task) error {
	if t.Name == "" || t.Run == nil {
		return errors.New("ingest: task needs a name and a Run function")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("ingest: cannot add a task to a running scheduler")
	}
	if _, dup := s.runners[t.Name]; dup {
		return fmt.Errorf("ingest: duplicate task %q", t.Name)
	}
	s.runners[t.Name] = newRunner(t, s)
	s.order = append(s.order, t.Name)
	return nil
}

// Start sets up the file watches and launches the tasks. They stop when ctx
// ends or Stop is called. It does not run any task: the first run happens on
// the first event, the first timer tick or Trigger.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("ingest: scheduler already started")
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	for _, name := range s.order {
		r := s.runners[name]
		// The watches exist when Start returns, so a change made right after
		// it is not missed.
		w := newWatchSet(ctx, r)
		w.sync(ctx)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer w.close()
			r.loop(ctx, w)
		}()
	}
	return nil
}

// Stop cancels the tasks and waits for them, including a Run in progress.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// Trigger asks the task to run as soon as possible (without the debounce
// delay). It returns false for an unknown task. A trigger that arrives while
// the task runs makes it run once more afterwards.
func (s *Scheduler) Trigger(name string) bool {
	s.mu.Lock()
	r := s.runners[name]
	s.mu.Unlock()
	if r == nil {
		return false
	}
	signal(r.now)
	return true
}

// signal does a non-blocking send on a one-slot channel: pending requests
// collapse into one.
func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

type runner struct {
	task Task
	s    *Scheduler

	events chan struct{} // a watched file changed: run after the debounce
	now    chan struct{} // run right away
}

func newRunner(t Task, s *Scheduler) *runner {
	return &runner{task: t, s: s, events: make(chan struct{}, 1), now: make(chan struct{}, 1)}
}

func (r *runner) loop(ctx context.Context, w *watchSet) {
	// A nil channel never fires, so the timers are plain variables.
	var (
		debounce, retry, periodic *time.Timer
		debounceC, retryC, perC   <-chan time.Time
	)
	stop := func(t *time.Timer) {
		if t != nil {
			t.Stop()
		}
	}
	defer func() { stop(debounce); stop(retry); stop(periodic) }()

	armPeriodic := func() {
		stop(periodic)
		perC = nil
		if r.task.Interval == nil {
			return
		}
		if d := r.task.Interval(ctx); d > 0 {
			periodic = time.NewTimer(d)
			perC = periodic.C
		}
	}
	armPeriodic()

	lastSync := time.Now()
	run := func(resync bool) {
		// Anything that was pending is satisfied by this run. Events that
		// arrive while it runs are not lost: they sit in the channels.
		stop(debounce)
		debounceC = nil
		stop(retry)
		retryC = nil
		// The roots are re-read every Interval even when events keep the
		// task busy, which would otherwise keep pushing the timer back.
		if !resync && r.task.Interval != nil {
			if d := r.task.Interval(ctx); d > 0 && time.Since(lastSync) >= d {
				resync = true
			}
		}
		if resync {
			w.sync(ctx)
			lastSync = time.Now()
		}
		err := r.safeRun(ctx)
		switch {
		case err == nil, ctx.Err() != nil:
		case errors.Is(err, ErrBusy):
			retry = time.NewTimer(r.s.cfg.RetryDelay)
			retryC = retry.C
		default:
			r.s.log.Warn("background task failed", "task", r.task.Name, "error", err)
		}
		armPeriodic()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.events:
			// Leading-edge debounce: the timer is not pushed back by later
			// events, so a log that is written continuously is still ingested
			// every Debounce.
			if debounceC == nil {
				debounce = time.NewTimer(r.s.cfg.Debounce)
				debounceC = debounce.C
			}
		case <-debounceC:
			run(false)
		case <-r.now:
			run(false)
		case <-retryC:
			run(false)
		case <-perC:
			run(true)
		}
	}
}

// safeRun turns a panic of the task into an error.
func (r *runner) safeRun(ctx context.Context) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("task %s panicked: %v", r.task.Name, p)
		}
	}()
	return r.task.Run(ctx)
}

// watchSet keeps the fsnotify watches of one task. The loop goroutine
// (sync) and the event goroutine (pump) both change it, hence the mutex.
type watchSet struct {
	r   *runner
	w   *fsnotify.Watcher
	max int
	wg  sync.WaitGroup

	maxFiles int // 0: no cap

	mu     sync.Mutex
	dir    map[string]int  // watched directories -> files counted against maxFiles
	files  int             // sum of the counts in dir
	warned map[string]bool // suppresses repeated warnings about one problem
}

func newWatchSet(ctx context.Context, r *runner) *watchSet {
	ws := &watchSet{r: r, dir: map[string]int{}, max: r.s.cfg.MaxWatchDirs, warned: map[string]bool{}}
	if r.s.cfg.MaxWatchFiles > 0 {
		ws.maxFiles = r.s.cfg.MaxWatchFiles
	}
	if r.task.Roots == nil {
		return ws
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		r.s.log.Warn("file watching unavailable; relying on periodic runs", "task", r.task.Name, "error", err)
		return ws
	}
	ws.w = w
	ws.wg.Add(1)
	go func() {
		defer ws.wg.Done()
		ws.pump(ctx)
	}()
	return ws
}

func (ws *watchSet) close() {
	if ws.w != nil {
		_ = ws.w.Close()
		ws.wg.Wait()
	}
}

// warnOnce logs once per key. The caller holds ws.mu.
func (ws *watchSet) warnOnce(key, msg string, args ...any) {
	if ws.warned[key] {
		return
	}
	ws.warned[key] = true
	ws.r.s.log.Warn(msg, args...)
}

// pump turns file system events into run requests and keeps the watches of
// new and removed directories current.
func (ws *watchSet) pump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ws.w.Events:
			if !ok {
				return
			}
			if ev.Op == fsnotify.Chmod {
				continue
			}
			if ev.Op.Has(fsnotify.Remove) || ev.Op.Has(fsnotify.Rename) {
				ws.forget(ev.Name)
			}
			if ev.Op.Has(fsnotify.Create) {
				// A new directory (a new project or day) needs its own watch,
				// and files may already be in it: their events were missed.
				if info, err := os.Lstat(ev.Name); err == nil && info.IsDir() {
					ws.addTree(ev.Name)
					signal(ws.r.events)
				}
			}
			if isLogFile(ev.Name) {
				signal(ws.r.events)
			}
		case err, ok := <-ws.w.Errors:
			if !ok {
				return
			}
			// Typically an overflowed event queue: events were lost, so run.
			ws.r.s.log.Debug("file watcher error", "task", ws.r.task.Name, "error", err)
			signal(ws.r.events)
		}
	}
}

// forget drops a removed or renamed directory from the bookkeeping (fsnotify
// already dropped the watch) so a directory created again is watched again.
func (ws *watchSet) forget(path string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.dropLocked(path)
}

// dropLocked forgets a watched directory. The caller holds ws.mu.
func (ws *watchSet) dropLocked(dir string) {
	if n, ok := ws.dir[dir]; ok {
		ws.files -= n
		delete(ws.dir, dir)
	}
}

// sync reconciles the watches with the task's current roots.
func (ws *watchSet) sync(ctx context.Context) {
	if ws.w == nil || ws.r.task.Roots == nil {
		return
	}
	want := map[string]struct{}{}
	isRoot := map[string]bool{}
	for _, root := range ws.r.task.Roots(ctx) {
		if root != "" {
			if resolved, ok := ws.collect(root, want); ok {
				isRoot[resolved] = true
			}
		}
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	for d := range ws.dir {
		if _, ok := want[d]; !ok {
			_ = ws.w.Remove(d)
			ws.dropLocked(d)
		}
	}
	for _, d := range ws.priority(want, isRoot) {
		ws.addLocked(d)
	}
}

// priority orders the directories to watch. The caps (directories, and files
// on kqueue platforms) cut the list off, so the order decides what is left
// out: the roots come first (a root that is not watched loses the Create
// events of every new project), then the directories with the most recently
// written file, which are the ones in use. Without a cap everything is
// watched and the order is irrelevant.
func (ws *watchSet) priority(want map[string]struct{}, isRoot map[string]bool) []string {
	dirs := make([]string, 0, len(want))
	for d := range want {
		dirs = append(dirs, d)
	}
	capped := ws.maxFiles > 0 || len(dirs) >= ws.max
	newest := map[string]time.Time{}
	if capped {
		for _, d := range dirs {
			if !isRoot[d] {
				newest[d] = newestEntry(d)
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		a, b := dirs[i], dirs[j]
		if isRoot[a] != isRoot[b] {
			return isRoot[a]
		}
		if !newest[a].Equal(newest[b]) {
			return newest[a].After(newest[b])
		}
		return a < b // deterministic
	})
	return dirs
}

// newestEntry returns the latest modification time of the files in dir, or
// of dir itself when it holds none.
func newestEntry(dir string) time.Time {
	var newest time.Time
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
	}
	if newest.IsZero() {
		if info, err := os.Stat(dir); err == nil {
			newest = info.ModTime()
		}
	}
	return newest
}

// addTree watches path and the directories below it.
func (ws *watchSet) addTree(path string) {
	found := map[string]struct{}{}
	ws.collect(path, found)
	ws.mu.Lock()
	defer ws.mu.Unlock()
	for d := range found {
		ws.addLocked(d)
	}
}

// collect gathers root and the directories below it into into (symlinks are
// never followed), up to the watch limit.
func (ws *watchSet) collect(root string, into map[string]struct{}) (resolved string, ok bool) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false // not installed (yet)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", false
	}
	_ = filepath.WalkDir(resolved, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if len(into) >= ws.max {
			return fs.SkipAll
		}
		into[path] = struct{}{}
		return nil
	})
	return resolved, true
}

// addLocked starts watching dir. The caller holds ws.mu.
func (ws *watchSet) addLocked(dir string) {
	if _, ok := ws.dir[dir]; ok {
		return
	}
	if len(ws.dir) >= ws.max {
		ws.warnOnce("max", "too many directories to watch; the rest is left to the periodic run",
			"task", ws.r.task.Name, "limit", ws.max)
		return
	}
	n := 0
	if ws.maxFiles > 0 {
		n = countFiles(dir)
		if ws.files+n > ws.maxFiles {
			ws.warnOnce("files", "too many files to watch; the rest is left to the periodic run",
				"task", ws.r.task.Name, "limit", ws.maxFiles)
			return
		}
	}
	if err := ws.w.Add(dir); err != nil {
		ws.warnOnce("add:"+dir, "cannot watch directory; relying on periodic runs",
			"task", ws.r.task.Name, "dir", dir, "error", err)
		return
	}
	ws.dir[dir] = n
	ws.files += n
}

// countFiles counts the entries of dir that are not directories.
func countFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

// isLogFile reports whether a path looks like a usage log (JSONL, or the
// single-object JSON recordings some CLIs write, or a compressed JSONL).
func isLogFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jsonl", ".json":
		return true
	}
	return IsCompressed(path)
}
