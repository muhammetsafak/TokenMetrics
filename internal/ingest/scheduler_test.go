package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testDebounce = 60 * time.Millisecond

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// never asserts that cond stays false for d.
func never(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			t.Fatalf("unexpected: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startScheduler(t *testing.T, cfg Config, tasks ...Task) *Scheduler {
	t.Helper()
	if cfg.Debounce == 0 {
		cfg.Debounce = testDebounce
	}
	s := NewScheduler(cfg)
	for _, task := range tasks {
		if err := s.Add(task); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func rootsOf(dirs ...string) func(context.Context) []string {
	return func(context.Context) []string { return dirs }
}

func TestSchedulerDebouncesFileBurst(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "project")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	startScheduler(t, Config{}, Task{
		Name:  "files",
		Roots: rootsOf(root),
		Run:   func(context.Context) error { runs.Add(1); return nil },
	})

	log := filepath.Join(sub, "a.jsonl")
	for i := 0; i < 8; i++ {
		appendTo(t, log, "{}\n")
	}
	waitFor(t, "the first run", func() bool { return runs.Load() >= 1 })
	// The whole burst fell into one debounce window: no second run follows.
	never(t, "a second run for one burst", 4*testDebounce, func() bool { return runs.Load() > 1 })

	// A later change starts a new window and runs again.
	appendTo(t, log, "{}\n")
	waitFor(t, "the run for a later change", func() bool { return runs.Load() == 2 })
}

func TestSchedulerIgnoresOtherFiles(t *testing.T) {
	root := t.TempDir()
	var runs atomic.Int32
	startScheduler(t, Config{}, Task{
		Name:  "files",
		Roots: rootsOf(root),
		Run:   func(context.Context) error { runs.Add(1); return nil },
	})
	appendTo(t, filepath.Join(root, "notes.txt"), "x")
	never(t, "a run for a non-log file", 5*testDebounce, func() bool { return runs.Load() > 0 })
}

func TestSchedulerWatchesNewDirectories(t *testing.T) {
	root := t.TempDir()
	var runs atomic.Int32
	startScheduler(t, Config{}, Task{
		Name:  "files",
		Roots: rootsOf(root),
		Run:   func(context.Context) error { runs.Add(1); return nil },
	})

	sub := filepath.Join(root, "2026", "11")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a run after the directory appeared", func() bool { return runs.Load() >= 1 })
	// The directory created above must now be watched: a write inside it
	// triggers a run without any help from a periodic tick.
	time.Sleep(3 * testDebounce)
	base := runs.Load()
	appendTo(t, filepath.Join(sub, "rollout.jsonl"), "{}\n")
	waitFor(t, "a run for a file in the new directory", func() bool { return runs.Load() > base })
}

func TestSchedulerNeverOverlapsAndRunsOnceMore(t *testing.T) {
	var (
		running, maxRunning, runs atomic.Int32
		release                   = make(chan struct{})
		entered                   = make(chan struct{}, 8)
	)
	s := startScheduler(t, Config{}, Task{
		Name: "job",
		Run: func(ctx context.Context) error {
			n := running.Add(1)
			for {
				m := maxRunning.Load()
				if n <= m || maxRunning.CompareAndSwap(m, n) {
					break
				}
			}
			runs.Add(1)
			entered <- struct{}{}
			<-release
			running.Add(-1)
			return nil
		},
	})

	if !s.Trigger("job") {
		t.Fatal("Trigger reported an unknown task")
	}
	<-entered // run 1 is inside Run now
	for i := 0; i < 20; i++ {
		s.Trigger("job") // all of these collapse into one follow-up
	}
	release <- struct{}{} // finish run 1
	<-entered             // run 2 starts
	release <- struct{}{}
	never(t, "a third run", 4*testDebounce, func() bool { return runs.Load() > 2 })

	if got := maxRunning.Load(); got != 1 {
		t.Fatalf("task ran %d times at once, want 1", got)
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("runs = %d, want 2 (one in flight + one follow-up)", got)
	}
}

func TestSchedulerTriggerUnknownTask(t *testing.T) {
	s := startScheduler(t, Config{}, Task{Name: "a", Run: func(context.Context) error { return nil }})
	if s.Trigger("nope") {
		t.Fatal("Trigger accepted an unknown task")
	}
}

func TestSchedulerRetriesAfterBusy(t *testing.T) {
	var calls atomic.Int32
	s := startScheduler(t, Config{RetryDelay: 20 * time.Millisecond}, Task{
		Name: "job",
		Run: func(context.Context) error {
			if calls.Add(1) < 3 {
				return ErrBusy
			}
			return nil
		},
	})
	s.Trigger("job")
	waitFor(t, "the third attempt", func() bool { return calls.Load() >= 3 })
	never(t, "more attempts after success", 5*testDebounce, func() bool { return calls.Load() > 3 })
}

func TestSchedulerPeriodicRun(t *testing.T) {
	var runs atomic.Int32
	startScheduler(t, Config{}, Task{
		Name:     "job",
		Interval: func(context.Context) time.Duration { return 25 * time.Millisecond },
		Run:      func(context.Context) error { runs.Add(1); return nil },
	})
	waitFor(t, "three periodic runs", func() bool { return runs.Load() >= 3 })
}

func TestSchedulerPeriodicRunPicksUpLateRoot(t *testing.T) {
	// The root does not exist at start (the tool is not installed yet). The
	// periodic run re-evaluates the roots, after which file events work.
	parent := t.TempDir()
	root := filepath.Join(parent, "logs")
	var runs atomic.Int32
	startScheduler(t, Config{}, Task{
		Name:     "job",
		Roots:    rootsOf(root),
		Interval: func(context.Context) time.Duration { return 40 * time.Millisecond },
		Run:      func(context.Context) error { runs.Add(1); return nil },
	})
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // a few periodic syncs
	base := runs.Load()
	// With a one-minute-long gap to the next tick being irrelevant here, the
	// write itself must trigger within a few debounces.
	appendTo(t, filepath.Join(root, "x.jsonl"), "{}\n")
	waitFor(t, "a run for the late root", func() bool { return runs.Load() > base })
}

func TestSchedulerSurvivesPanicAndError(t *testing.T) {
	var calls atomic.Int32
	s := startScheduler(t, Config{}, Task{
		Name: "job",
		Run: func(context.Context) error {
			switch calls.Add(1) {
			case 1:
				panic("boom")
			case 2:
				return errors.New("failed")
			}
			return nil
		},
	})
	for want := int32(1); want <= 3; want++ {
		s.Trigger("job")
		waitFor(t, "the next run", func() bool { return calls.Load() >= want })
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSchedulerStopWaitsForRunAndCancelsContext(t *testing.T) {
	started := make(chan struct{})
	var finished atomic.Bool
	s := NewScheduler(Config{Debounce: testDebounce})
	if err := s.Add(Task{
		Name: "job",
		Run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			finished.Store(true)
			return ctx.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Trigger("job")
	<-started
	s.Stop()
	if !finished.Load() {
		t.Fatal("Stop returned before the running task ended")
	}
}

func TestSchedulerAddValidation(t *testing.T) {
	s := NewScheduler(Config{})
	run := func(context.Context) error { return nil }
	if err := s.Add(Task{Run: run}); err == nil {
		t.Error("nameless task accepted")
	}
	if err := s.Add(Task{Name: "a"}); err == nil {
		t.Error("task without Run accepted")
	}
	if err := s.Add(Task{Name: "a", Run: run}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(Task{Name: "a", Run: run}); err == nil {
		t.Error("duplicate accepted")
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if err := s.Add(Task{Name: "b", Run: run}); err == nil {
		t.Error("task added after Start")
	}
	if err := s.Start(context.Background()); err == nil {
		t.Error("second Start accepted")
	}
}

func TestSchedulerWatchLimitLeavesRestToPeriodicRun(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a", "b", "c", "d"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	runs := 0
	startScheduler(t, Config{MaxWatchDirs: 2}, Task{
		Name:  "job",
		Roots: rootsOf(root),
		Run:   func(context.Context) error { mu.Lock(); runs++; mu.Unlock(); return nil },
	})
	// Nothing to assert beyond "no crash and no hang": the limit only caps the
	// number of watches, and watching root itself still works.
	appendTo(t, filepath.Join(root, "top.jsonl"), "{}\n")
	waitFor(t, "a run for a watched directory", func() bool { mu.Lock(); defer mu.Unlock(); return runs > 0 })
}

func TestSchedulerWatchFileLimit(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a", "b", "c"} {
		dir := filepath.Join(root, d)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"1.jsonl", "2.jsonl"} {
			appendTo(t, filepath.Join(dir, f), "{}\n")
		}
	}
	s := NewScheduler(Config{MaxWatchFiles: 3})
	r := newRunner(Task{Name: "t", Roots: rootsOf(root), Run: func(context.Context) error { return nil }}, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws := newWatchSet(ctx, r)
	defer ws.close()
	ws.sync(ctx)

	ws.mu.Lock()
	defer ws.mu.Unlock()
	// The root (no files) and one directory (2 files) fit; a second directory
	// would make 4 > 3.
	if len(ws.dir) != 2 || ws.files != 2 {
		t.Fatalf("watched %d dirs with %d files, want 2 dirs and 2 files: %v", len(ws.dir), ws.files, ws.dir)
	}
	for d := range ws.dir {
		ws.dropLocked(d)
	}
	if ws.files != 0 {
		t.Fatalf("file count after dropping everything = %d", ws.files)
	}
}

func TestSchedulerNoFileLimitByDefaultOffKqueue(t *testing.T) {
	if kqueuePlatform() {
		t.Skip("kqueue platforms cap files by default")
	}
	if got := NewScheduler(Config{}).cfg.MaxWatchFiles; got != 0 {
		t.Fatalf("default MaxWatchFiles = %d, want 0 (no cap)", got)
	}
}

func TestSchedulerResyncsRootsWhileEventsKeepComingIn(t *testing.T) {
	root := t.TempDir()
	var rootCalls, runs atomic.Int32
	startScheduler(t, Config{Debounce: 10 * time.Millisecond}, Task{
		Name: "job",
		Roots: func(context.Context) []string {
			rootCalls.Add(1)
			return []string{root}
		},
		// Runs happen every ~10ms because of the writes below, so the timer
		// never fires; the roots must be re-read anyway.
		Interval: func(context.Context) time.Duration { return 100 * time.Millisecond },
		Run:      func(context.Context) error { runs.Add(1); return nil },
	})
	base := rootCalls.Load() // the initial read at Start
	log := filepath.Join(root, "a.jsonl")
	end := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(end) {
		appendTo(t, log, "{}\n")
		time.Sleep(5 * time.Millisecond)
	}
	if runs.Load() < 10 {
		t.Fatalf("only %d runs; the test needs a busy task", runs.Load())
	}
	if got := rootCalls.Load() - base; got < 2 {
		t.Fatalf("roots re-read %d times during 700ms of events with a 100ms interval, want >= 2", got)
	}
}

func TestSchedulerFileLimitKeepsRootAndNewestDirectories(t *testing.T) {
	root := t.TempDir()
	base := time.Now().Add(-time.Hour)
	// Names sort the opposite way of their age, so neither lexical order nor
	// map order can pass by accident: "a" is the newest.
	for i, d := range []string{"d-old", "c", "b", "a-new"} {
		dir := filepath.Join(root, d)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(dir, "x.jsonl")
		appendTo(t, f, "{}\n")
		mt := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(f, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	for run := 0; run < 5; run++ { // map order varies between runs
		s := NewScheduler(Config{MaxWatchFiles: 2})
		r := newRunner(Task{Name: "t", Roots: rootsOf(root), Run: func(context.Context) error { return nil }}, s)
		ctx, cancel := context.WithCancel(context.Background())
		ws := newWatchSet(ctx, r)
		ws.sync(ctx)
		ws.mu.Lock()
		var got []string
		for d := range ws.dir {
			got = append(got, filepath.Base(d))
		}
		ws.mu.Unlock()
		ws.close()
		cancel()
		resolvedRoot, _ := filepath.EvalSymlinks(root)
		hasRoot, hasNew, others := false, false, 0
		for _, g := range got {
			switch g {
			case filepath.Base(resolvedRoot):
				hasRoot = true
			case "a-new":
				hasNew = true
			default:
				others++
			}
		}
		// Limit 2 files: the root (0 files) and the two newest directories.
		if !hasRoot || !hasNew || others != 1 {
			t.Fatalf("run %d: watched %v, want the root, a-new and one more", run, got)
		}
		for _, g := range got {
			if g == "d-old" {
				t.Fatalf("run %d: the oldest directory was watched: %v", run, got)
			}
		}
	}
}
