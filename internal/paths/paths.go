// Package paths resolves where TokenMetrics keeps its local data.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// EnvDataDir overrides the data directory (used by tests and verification).
	EnvDataDir = "TOKENMETRICS_DATA_DIR"

	appDirName = "TokenMetrics"
	// DBFileName is the SQLite database file name inside the data directory.
	DBFileName = "tokenmetrics.db"
)

// DataDir returns the data directory, creating it (mode 0700) if needed.
// It is $TOKENMETRICS_DATA_DIR when set, otherwise os.UserConfigDir()/TokenMetrics.
func DataDir() (string, error) {
	dir := os.Getenv(EnvDataDir)
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve user config dir: %w", err)
		}
		dir = filepath.Join(base, appDirName)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve data dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create data dir: %w", err)
	}
	return dir, nil
}

// DBPath returns the full path of the SQLite database, creating the data
// directory if needed.
func DBPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DBFileName), nil
}
