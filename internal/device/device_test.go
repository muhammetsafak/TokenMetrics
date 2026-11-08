package device

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestLoadOrCreatePersists(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("not a uuid: %q", first)
	}
	second, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("device id changed: %q -> %q", first, second)
	}
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != first+"\n" {
		t.Fatalf("file content %q", got)
	}
}

func TestLoadOrCreateDifferentDirsDiffer(t *testing.T) {
	a, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two data dirs produced the same id")
	}
}

func TestLoadOrCreateRegeneratesEmptyOrCorruptFile(t *testing.T) {
	for name, content := range map[string]string{"empty": "", "garbage": "garbage", "truncated": "1234abcd-"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, FileName)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			id, err := LoadOrCreate(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := uuid.Parse(id); err != nil {
				t.Fatalf("not a uuid: %q", id)
			}
			again, err := LoadOrCreate(dir)
			if err != nil || again != id {
				t.Fatalf("second call = %q, %v; want %q", again, err, id)
			}
		})
	}
}

func TestLoadOrCreateLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if _, err := LoadOrCreate(dir); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != FileName {
		t.Fatalf("data dir contains %v, want only %s", entries, FileName)
	}
}

func TestLoadOrCreateConcurrent(t *testing.T) {
	dir := t.TempDir()
	const n = 16
	ids := make([]string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			id, err := LoadOrCreate(dir)
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = id
		}(i)
	}
	close(start)
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("concurrent callers disagree: %v", ids)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("leftover files in data dir: %v", entries)
	}
}
