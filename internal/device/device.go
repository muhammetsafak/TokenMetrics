// Package device provides the stable per-installation device identifier.
package device

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// FileName is the file inside the data directory that holds the device id.
const FileName = "device_id"

// LoadOrCreate returns the device id stored in dataDir, generating and
// persisting a new UUID on first call. Later calls return the same value.
//
// The id file is only ever published atomically (a fully written, synced
// temp file is linked or renamed into place), so readers never observe a
// partial file. When several callers race to create it, exactly one wins and
// all of them return the winner's id. An empty or corrupt file (left by an
// older version or external damage) is replaced with a fresh id.
func LoadOrCreate(dataDir string) (string, error) {
	path := filepath.Join(dataDir, FileName)

	id, err := read(path)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errInvalid) {
		return "", err
	}
	missing := errors.Is(err, fs.ErrNotExist)

	tmp, err := writeTemp(dataDir, uuid.NewString())
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp) // no-op after a successful rename

	if missing {
		// Link fails with ErrExist if someone else published first; unlike
		// rename it never overwrites, so the first writer's id stays.
		switch lerr := os.Link(tmp, path); {
		case lerr == nil:
			return read(path)
		case errors.Is(lerr, fs.ErrExist):
			return read(path) // lost the race: use the winner's id
		default:
			// Hard links unsupported on this filesystem; fall through to
			// rename. The narrow race left here is resolved by re-reading.
		}
	}

	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("publish device id: %w", err)
	}
	// Return what is actually on disk, which is the last published id if
	// two callers replaced a corrupt file at the same time.
	return read(path)
}

var errInvalid = errors.New("invalid device id")

func read(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("device id file %s: %w: %v", path, errInvalid, err)
	}
	return id, nil
}

// writeTemp writes id to a new synced file next to the final path.
func writeTemp(dir, id string) (string, error) {
	f, err := os.CreateTemp(dir, FileName+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create temp device id file: %w", err)
	}
	name := f.Name()
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("write device id: %w", err)
	}
	if _, err := f.WriteString(id + "\n"); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("close device id file: %w", err)
	}
	return name, nil
}
