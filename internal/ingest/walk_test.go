package ingest

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWalkJSONL(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, p, "{}\n")
	}
	mk("b.jsonl")
	mk("a/one.jsonl")
	mk("a/deep/two.JSONL")
	mk("a/notes.txt")
	mk("a/data.json")

	files, warnings, err := WalkJSONL(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %q", warnings)
	}
	resolved, _ := filepath.EvalSymlinks(root)
	want := []string{
		filepath.Join(resolved, "a", "deep", "two.JSONL"),
		filepath.Join(resolved, "a", "one.jsonl"),
		filepath.Join(resolved, "b.jsonl"),
	}
	if len(files) != len(want) {
		t.Fatalf("files = %q, want %q", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("files = %q, want %q", files, want)
		}
	}
}

func TestWalkJSONLSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.jsonl"), "{}\n")
	writeFile(t, filepath.Join(root, "real.jsonl"), "{}\n")

	if err := os.Symlink(filepath.Join(outside, "secret.jsonl"), filepath.Join(root, "link.jsonl")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	files, _, err := WalkJSONL(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "real.jsonl" {
		t.Fatalf("files = %q, want only real.jsonl", files)
	}
}

func TestWalkJSONLRootSymlinkIsResolved(t *testing.T) {
	target := t.TempDir()
	writeFile(t, filepath.Join(target, "a.jsonl"), "{}\n")
	link := filepath.Join(t.TempDir(), "projects")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	files, _, err := WalkJSONL(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %q", files)
	}
}

func TestWalkJSONLMissingRoot(t *testing.T) {
	_, _, err := WalkJSONL(context.Background(), filepath.Join(t.TempDir(), "nope"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want not-exist", err)
	}
}

func TestWalkJSONLRootIsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.jsonl")
	writeFile(t, p, "{}\n")
	if _, _, err := WalkJSONL(context.Background(), p); err == nil {
		t.Fatal("expected error for a file root")
	}
}

func TestWalkJSONLContextCanceled(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.jsonl"), "{}\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := WalkJSONL(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestWalkerVisitUnreadableDir exercises the error branch directly, so it runs
// on every platform without needing real permission denials.
func TestWalkerVisitUnreadableDir(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "locked")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sub)
	if err != nil {
		t.Fatal(err)
	}
	dir := fs.FileInfoToDirEntry(info)
	denied := errors.New("permission denied")

	w := &walker{ctx: context.Background(), root: root}
	if err := w.visit(sub, dir, denied); !errors.Is(err, fs.SkipDir) {
		t.Fatalf("dir error: got %v, want SkipDir", err)
	}
	if len(w.warnings) != 1 || !strings.Contains(w.warnings[0], "permission denied") {
		t.Fatalf("warnings = %q", w.warnings)
	}

	// An unreadable file is skipped with a warning, not fatal.
	f := filepath.Join(root, "x.jsonl")
	writeFile(t, f, "{}\n")
	finfo, _ := os.Stat(f)
	if err := w.visit(f, fs.FileInfoToDirEntry(finfo), denied); err != nil {
		t.Fatalf("file error: got %v, want nil", err)
	}
	if len(w.warnings) != 2 {
		t.Fatalf("warnings = %q", w.warnings)
	}

	// An error on the root itself is fatal.
	if err := w.visit(root, nil, denied); !errors.Is(err, denied) {
		t.Fatalf("root error: got %v", err)
	}
}

// TestWalkJSONLUnreadableDir uses a real permission denial where the platform
// supports it: the locked directory is reported, its sibling is still listed.
func TestWalkJSONLUnreadableDir(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("chmod-based denial is not reliable here")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(locked, "hidden.jsonl"), "{}\n")
	writeFile(t, filepath.Join(root, "ok.jsonl"), "{}\n")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	files, warnings, err := WalkJSONL(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "ok.jsonl" {
		t.Fatalf("files = %q", files)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %q", warnings)
	}
}
