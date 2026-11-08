package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

// readAll runs ReadFile and returns the lines it delivered.
func readAll(t *testing.T, path string, prev *store.IngestState, opts Options) ([]string, FileResult) {
	t.Helper()
	var lines []string
	res, err := ReadFile(context.Background(), path, prev, opts, func(line []byte, _ int64) error {
		lines = append(lines, string(line))
		return nil
	})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return lines, res
}

func equalLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestReadFileFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeFile(t, path, "one\ntwo\r\n\n  \nthree\n")

	lines, res := readAll(t, path, nil, Options{})
	if want := []string{"one", "two", "three"}; !equalLines(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	if res.Reset || res.Lines != 3 {
		t.Fatalf("Reset=%v Lines=%d", res.Reset, res.Lines)
	}
	info, _ := os.Stat(path)
	if res.State.Offset != info.Size() || res.State.Size != info.Size() {
		t.Fatalf("state = %+v, size %d", res.State, info.Size())
	}
	if res.State.Path != path || res.State.ModTime.IsZero() {
		t.Fatalf("state = %+v", res.State)
	}
}

func TestReadFileOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeFile(t, path, "ab\ncd\n")
	var offsets []int64
	_, err := ReadFile(context.Background(), path, nil, Options{}, func(_ []byte, off int64) error {
		offsets = append(offsets, off)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 3 {
		t.Fatalf("offsets = %v", offsets)
	}
}

func TestReadFileIncrementalAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeFile(t, path, "one\ntwo\n")
	_, first := readAll(t, path, nil, Options{})

	// Nothing new: no lines, state unchanged.
	lines, again := readAll(t, path, &first.State, Options{})
	if len(lines) != 0 || again.State.Offset != first.State.Offset {
		t.Fatalf("lines=%q state=%+v", lines, again.State)
	}

	appendFile(t, path, "three\nfour\n")
	lines, second := readAll(t, path, &again.State, Options{})
	if want := []string{"three", "four"}; !equalLines(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	if second.Reset {
		t.Fatal("unexpected reset")
	}
}

func TestReadFilePartialTrailingLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeFile(t, path, "one\ntwo\nthr")

	lines, res := readAll(t, path, nil, Options{})
	if want := []string{"one", "two"}; !equalLines(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	if res.State.Offset != int64(len("one\ntwo\n")) {
		t.Fatalf("offset = %d, want %d", res.State.Offset, len("one\ntwo\n"))
	}
	if res.State.Size != int64(len("one\ntwo\nthr")) {
		t.Fatalf("size = %d", res.State.Size)
	}

	// Still incomplete: re-reads nothing, offset stays.
	lines, res2 := readAll(t, path, &res.State, Options{})
	if len(lines) != 0 || res2.State.Offset != res.State.Offset {
		t.Fatalf("lines=%q offset=%d", lines, res2.State.Offset)
	}

	appendFile(t, path, "ee\nfour\n")
	lines, res3 := readAll(t, path, &res2.State, Options{})
	if want := []string{"three", "four"}; !equalLines(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	info, _ := os.Stat(path)
	if res3.State.Offset != info.Size() {
		t.Fatalf("offset = %d, want %d", res3.State.Offset, info.Size())
	}
}

func TestReadFileTruncateResets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeFile(t, path, "one\ntwo\nthree\n")
	_, first := readAll(t, path, nil, Options{})

	writeFile(t, path, "x\ny\n")
	lines, res := readAll(t, path, &first.State, Options{})
	if !res.Reset {
		t.Fatal("expected Reset")
	}
	if want := []string{"x", "y"}; !equalLines(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	if res.State.Offset != 4 {
		t.Fatalf("offset = %d", res.State.Offset)
	}
}

func TestReadFileSizeBelowSavedSizeResets(t *testing.T) {
	// Saved size includes a partial tail; a file that ends up smaller than
	// that was rewritten even if it is still larger than the offset.
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeFile(t, path, "one\ntwo\nthr")
	_, first := readAll(t, path, nil, Options{})

	writeFile(t, path, "one\ntwo\nX")
	lines, res := readAll(t, path, &first.State, Options{})
	if !res.Reset {
		t.Fatal("expected Reset")
	}
	if want := []string{"one", "two"}; !equalLines(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
}

func TestReadFileMtimeBackwardsResets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeFile(t, path, "one\ntwo\n")
	_, first := readAll(t, path, nil, Options{})

	appendFile(t, path, "three\n")
	old := first.State.ModTime.Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	lines, res := readAll(t, path, &first.State, Options{})
	if !res.Reset {
		t.Fatal("expected Reset")
	}
	if want := []string{"one", "two", "three"}; !equalLines(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
}

func TestReadFileZeroSavedModTimeDoesNotReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeFile(t, path, "one\ntwo\n")
	_, first := readAll(t, path, nil, Options{})
	st := first.State
	st.ModTime = time.Time{}
	lines, res := readAll(t, path, &st, Options{})
	if res.Reset || len(lines) != 0 {
		t.Fatalf("Reset=%v lines=%q", res.Reset, lines)
	}
}

func TestReadFileOversizedLineSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	long := strings.Repeat("x", 300_000) // spans several bufio refills
	writeFile(t, path, "ok1\n"+long+"\nok2\n")

	lines, res := readAll(t, path, nil, Options{MaxLineBytes: 1024})
	if want := []string{"ok1", "ok2"}; !equalLines(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "exceeds 1024 bytes") {
		t.Fatalf("warnings = %q", res.Warnings)
	}
	info, _ := os.Stat(path)
	if res.State.Offset != info.Size() {
		t.Fatalf("offset = %d, want %d", res.State.Offset, info.Size())
	}
}

func TestReadFileLineJustUnderLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	line := strings.Repeat("y", 200_000)
	writeFile(t, path, line+"\n")
	lines, res := readAll(t, path, nil, Options{MaxLineBytes: 300_000})
	if len(lines) != 1 || lines[0] != line || len(res.Warnings) != 0 {
		t.Fatalf("got %d lines, warnings %q", len(lines), res.Warnings)
	}
}

func TestReadFileHandlerError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeFile(t, path, "one\ntwo\n")
	boom := errors.New("boom")
	_, err := ReadFile(context.Background(), path, nil, Options{}, func([]byte, int64) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestReadFileContextCanceled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeFile(t, path, "one\ntwo\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ReadFile(ctx, path, nil, Options{}, func([]byte, int64) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestReadFileMissing(t *testing.T) {
	_, err := ReadFile(context.Background(), filepath.Join(t.TempDir(), "nope.jsonl"), nil, Options{},
		func([]byte, int64) error { return nil })
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want not-exist", err)
	}
}

func TestReadFileRefusesNonRegular(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadFile(context.Background(), dir, nil, Options{}, func([]byte, int64) error { return nil })
	if !errors.Is(err, ErrNotRegular) {
		t.Fatalf("dir: err = %v, want ErrNotRegular", err)
	}

	target := filepath.Join(dir, "real.jsonl")
	writeFile(t, target, "one\n")
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	_, err = ReadFile(context.Background(), link, nil, Options{}, func([]byte, int64) error { return nil })
	if !errors.Is(err, ErrNotRegular) {
		t.Fatalf("symlink: err = %v, want ErrNotRegular", err)
	}
}

// TestReadFileStatePersistsThroughStore round-trips the state through the
// real store, as the collectors do.
func TestReadFileStatePersistsThroughStore(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeFile(t, path, "one\ntwo\nthr")
	_, res := readAll(t, path, nil, Options{})
	st := res.State
	st.Source = model.SourceClaudeCode
	if err := s.SetIngestState(ctx, st); err != nil {
		t.Fatal(err)
	}

	saved, found, err := s.GetIngestState(ctx, model.SourceClaudeCode, path)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	appendFile(t, path, "ee\n")
	lines, res2 := readAll(t, path, &saved, Options{})
	if want := []string{"three"}; !equalLines(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	if res2.Reset {
		t.Fatal("state round trip caused a reset (mtime precision?)")
	}
}
