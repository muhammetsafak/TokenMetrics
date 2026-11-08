package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDataDirEnvOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "nested", "data")
	t.Setenv(EnvDataDir, want)

	got, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("DataDir = %q, want %q", got, want)
	}
	info, err := os.Stat(got)
	if err != nil || !info.IsDir() {
		t.Fatalf("data dir not created: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("perm = %v, want 0700", info.Mode().Perm())
	}
}

func TestDBPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDataDir, dir)

	got, err := DBPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "tokenmetrics.db"); got != want {
		t.Fatalf("DBPath = %q, want %q", got, want)
	}
}

func TestDataDirDefaultUsesUserConfigDir(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv(EnvDataDir, "")
	// os.UserConfigDir reads these per platform; point them all at a temp dir
	// so the test never touches the real data directory.
	t.Setenv("AppData", cfg)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", cfg)

	got, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user config dir: %v", err)
	}
	if want := filepath.Join(base, "TokenMetrics"); got != want {
		t.Fatalf("DataDir = %q, want %q", got, want)
	}
}
