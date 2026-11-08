package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func writeZstFile(t *testing.T, path, content string) {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	if err := os.WriteFile(path, enc.EncodeAll([]byte(content), nil), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIsCompressed(t *testing.T) {
	for p, want := range map[string]bool{
		"a/rollout-1.jsonl.zst": true,
		"a/ROLLOUT-1.JSONL.ZST": true,
		"a/rollout-1.jsonl":     false,
		"a/rollout-1.zst":       false,
		"a/rollout-1.json.zst":  false,
	} {
		if got := IsCompressed(p); got != want {
			t.Errorf("IsCompressed(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestReadFileCompressed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl.zst")
	writeZstFile(t, path, "one\ntwo\r\n\nthree\npartial")

	var offsets []int64
	var lines []string
	res, err := ReadFile(context.Background(), path, nil, Options{}, func(line []byte, off int64) error {
		lines = append(lines, string(line))
		offsets = append(offsets, off)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"one", "two", "three"}; !equalLines(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	if offsets[0] != 0 || offsets[1] != 4 || offsets[2] != 10 {
		t.Errorf("offsets = %v, want [0 4 10] (decompressed)", offsets)
	}
	fi, _ := os.Stat(path)
	if res.State.Offset != 16 || res.State.Size != fi.Size() || res.Reset {
		t.Errorf("state = %+v (reset %v), want offset 16, compressed size %d", res.State, res.Reset, fi.Size())
	}
}

func TestReadFileCompressedUnchangedIsSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl.zst")
	writeZstFile(t, path, "one\ntwo\n")
	_, first := readAll(t, path, nil, Options{})

	lines, res := readAll(t, path, &first.State, Options{})
	if len(lines) != 0 || res.Reset || res.State.Offset != first.State.Offset {
		t.Fatalf("lines=%q reset=%v state=%+v, want no read and the saved offset", lines, res.Reset, res.State)
	}
}

func TestReadFileCompressedChangedIsReadFromStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl.zst")
	writeZstFile(t, path, "one\ntwo\n")
	_, first := readAll(t, path, nil, Options{})

	writeZstFile(t, path, "one\ntwo\nthree\n")
	// Same mtime is not guaranteed to differ; size always does here.
	lines, res := readAll(t, path, &first.State, Options{})
	if want := []string{"one", "two", "three"}; !equalLines(lines, want) || !res.Reset {
		t.Fatalf("lines=%q reset=%v, want a full re-read", lines, res.Reset)
	}

	// Same size, newer mtime: still re-read.
	writeZstFile(t, path, "ONE\ntwo\nthree\n")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	lines, _ = readAll(t, path, &res.State, Options{})
	if len(lines) != 3 || lines[0] != "ONE" {
		t.Fatalf("lines = %q, want a re-read after an mtime change", lines)
	}
}

func TestReadFileCompressedSizeCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl.zst")
	body := strings.Repeat("0123456789\n", 1000) // 11000 bytes
	writeZstFile(t, path, body)

	_, err := ReadFile(context.Background(), path, nil, Options{MaxDecompressedBytes: 5000},
		func([]byte, int64) error { return nil })
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}

	// Exactly at the cap is fine.
	lines, res := readAll(t, path, nil, Options{MaxDecompressedBytes: 11000})
	if len(lines) != 1000 || res.State.Offset != 11000 {
		t.Fatalf("at the cap: %d lines, offset %d", len(lines), res.State.Offset)
	}
	// One byte under is not.
	_, err = ReadFile(context.Background(), path, nil, Options{MaxDecompressedBytes: 10999},
		func([]byte, int64) error { return nil })
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestReadFileCompressedDamaged(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage.jsonl.zst")
	writeFile(t, garbage, "definitely not zstd")
	empty := filepath.Join(dir, "empty.jsonl.zst")
	writeFile(t, empty, "")
	cut := filepath.Join(dir, "cut.jsonl.zst")
	writeZstFile(t, cut, strings.Repeat("line\n", 5000))
	b, _ := os.ReadFile(cut)
	if err := os.WriteFile(cut, b[:len(b)-5], 0o600); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{garbage, cut} {
		_, err := ReadFile(context.Background(), p, nil, Options{}, func([]byte, int64) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "zstd") {
			t.Errorf("%s: err = %v, want a zstd error", filepath.Base(p), err)
		}
	}
	// A zero-byte file holds no frame: nothing to read, not an error.
	if lines, res := readAll(t, empty, nil, Options{}); len(lines) != 0 || res.State.Offset != 0 {
		t.Errorf("empty file: lines=%q state=%+v", lines, res.State)
	}
}

func TestReadFileCompressedCallbackError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl.zst")
	writeZstFile(t, path, "one\ntwo\n")
	boom := errors.New("boom")
	_, err := ReadFile(context.Background(), path, nil, Options{}, func([]byte, int64) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the callback error", err)
	}
}

func TestWalkJSONLWithCompressed(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a.jsonl", "b.jsonl.zst", "c.zst", "d.json", "sub/e.jsonl.zst"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, p, "x")
	}
	resolved, _ := filepath.EvalSymlinks(root)

	files, _, err := WalkJSONLWithCompressed(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(resolved, "a.jsonl"),
		filepath.Join(resolved, "b.jsonl.zst"),
		filepath.Join(resolved, "sub", "e.jsonl.zst"),
	}
	if !equalLines(files, want) {
		t.Fatalf("files = %q, want %q", files, want)
	}

	// The plain walk used by the other collectors never lists them.
	files, _, err = WalkJSONL(context.Background(), root)
	if err != nil || !equalLines(files, []string{filepath.Join(resolved, "a.jsonl")}) {
		t.Fatalf("WalkJSONL = %q, %v", files, err)
	}
}

func TestIsLogFileCompressed(t *testing.T) {
	if !isLogFile("x/rollout-1.jsonl.zst") || isLogFile("x/other.zst") {
		t.Error("isLogFile must accept .jsonl.zst and nothing else with .zst")
	}
}
