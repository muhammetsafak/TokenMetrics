// Package ingest holds the file-based helpers shared by the collectors that
// read JSONL logs: an incremental line reader and a directory walker.
package ingest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

// DefaultMaxLineBytes is the longest line ReadFile will hand to the callback.
// Longer lines are skipped with a warning.
const DefaultMaxLineBytes = 16 << 20

// CompressedSuffix ends the name of a zstd-compressed JSONL file, as Codex
// writes cold rollouts.
const CompressedSuffix = ".jsonl" + CompressedExt

// CompressedExt is the extension a compressed file name ends in.
const CompressedExt = ".zst"

// DefaultMaxDecompressedBytes caps how much a single compressed file may
// expand to; a larger stream is refused with ErrTooLarge (decompression bomb).
const DefaultMaxDecompressedBytes int64 = 2 << 30

// Decoder limits: the window is what a hostile frame can make the decoder
// allocate before any output is produced.
const (
	maxDecoderMemory = 256 << 20
	maxDecoderWindow = 128 << 20
)

// ErrTooLarge is returned when a compressed file expands beyond the cap.
var ErrTooLarge = errors.New("ingest: decompressed size exceeds the limit")

// DecodeError reports a compressed file that is not a readable zstd stream
// (damaged, truncated, or declaring a window above the decoder limit). It is
// permanent for that version of the file, unlike an I/O error.
type DecodeError struct{ Err error }

func (e *DecodeError) Error() string { return "zstd: " + e.Err.Error() }
func (e *DecodeError) Unwrap() error { return e.Err }

// ErrNotRegular is returned for paths that are symlinks or not regular files.
var ErrNotRegular = errors.New("ingest: not a regular file")

// Options tunes ReadFile.
type Options struct {
	// MaxLineBytes is the longest accepted line, newline included.
	// Zero means DefaultMaxLineBytes.
	MaxLineBytes int
	// MaxDecompressedBytes caps the decompressed size of a compressed file.
	// Zero means DefaultMaxDecompressedBytes. Ignored for plain files.
	MaxDecompressedBytes int64
}

// IsCompressed reports whether path names a zstd-compressed JSONL file.
func IsCompressed(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), CompressedSuffix)
}

// LineFunc receives one complete line without its line terminator. line is
// only valid until the function returns. offset is the byte offset of the
// line's first byte in the file. Returning an error aborts the read.
type LineFunc func(line []byte, offset int64) error

// FileResult is the outcome of one ReadFile call.
type FileResult struct {
	// State is the position to persist once the lines have been handled
	// successfully. Its Source and UpdatedAt fields are left for the caller.
	State store.IngestState
	// Reset reports that the saved state was discarded and the file was read
	// from the start (it shrank or its mtime went backwards).
	Reset bool
	// Lines is the number of lines handed to the callback.
	Lines int
	// Warnings lists non-fatal problems, e.g. lines that were too long.
	Warnings []string
}

// ReadFile reads the lines of path that were not consumed yet.
//
// prev is the previously saved state, or nil when the file was never read.
// Only complete lines (ending in '\n') are consumed: a trailing partial line
// is neither passed to fn nor counted in the returned offset, so it is read
// again once the writer finishes it. The file is read from the start when its
// size dropped below the saved offset or size, or when its mtime moved
// backwards. Symlinks and non-regular files are refused with ErrNotRegular.
//
// A path ending in CompressedSuffix is decoded as a zstd stream. A compressed
// file has no byte offset to resume from and is treated as immutable: when its
// size and mtime equal the saved state nothing is read; otherwise it is read
// from the start. The saved Offset then counts decompressed bytes (up to the
// end of the last complete line) and Size counts compressed bytes. Streams
// that expand beyond Options.MaxDecompressedBytes fail with ErrTooLarge.
//
// ReadFile never writes the state; the caller persists FileResult.State after
// the records produced by fn are safely stored.
func ReadFile(ctx context.Context, path string, prev *store.IngestState, opts Options, fn LineFunc) (FileResult, error) {
	maxLine := opts.MaxLineBytes
	if maxLine <= 0 {
		maxLine = DefaultMaxLineBytes
	}

	li, err := os.Lstat(path)
	if err != nil {
		return FileResult{}, err
	}
	if !li.Mode().IsRegular() {
		return FileResult{}, fmt.Errorf("%w: %s", ErrNotRegular, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return FileResult{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return FileResult{}, err
	}
	if !info.Mode().IsRegular() {
		return FileResult{}, fmt.Errorf("%w: %s", ErrNotRegular, path)
	}

	size := info.Size()
	mtime := info.ModTime().UTC()
	res := FileResult{State: store.IngestState{Path: path, Size: size, ModTime: mtime}}

	if IsCompressed(path) {
		return readCompressed(ctx, f, prev, opts, fn, res, maxLine)
	}

	var start int64
	if prev != nil {
		shrunk := size < prev.Offset || size < prev.Size
		backwards := !prev.ModTime.IsZero() && mtime.Before(prev.ModTime)
		if shrunk || backwards {
			res.Reset = true
		} else {
			start = prev.Offset
		}
	}
	res.State.Offset = start
	if start >= size {
		return res, nil
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return FileResult{}, err
	}

	// Reading at most up to the size seen above keeps Offset <= Size even if
	// the file keeps growing while we read.
	r := bufio.NewReaderSize(io.LimitReader(f, size-start), 64<<10)
	return scanLines(ctx, path, r, start, maxLine, fn, res)
}

// readCompressed reads a zstd-compressed JSONL file in full, or not at all
// when it is unchanged since prev.
func readCompressed(ctx context.Context, f *os.File, prev *store.IngestState, opts Options, fn LineFunc, res FileResult, maxLine int) (FileResult, error) {
	if prev != nil {
		if prev.Size == res.State.Size && prev.ModTime.Equal(res.State.ModTime) {
			res.State.Offset = prev.Offset
			return res, nil
		}
		res.Reset = true
	}
	limit := opts.MaxDecompressedBytes
	if limit <= 0 {
		limit = DefaultMaxDecompressedBytes
	}
	dec, err := zstd.NewReader(f,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(maxDecoderMemory),
		zstd.WithDecoderMaxWindow(maxDecoderWindow))
	if err != nil {
		return FileResult{}, &DecodeError{Err: err}
	}
	defer dec.Close()

	r := bufio.NewReaderSize(&capReader{r: dec, left: limit}, 64<<10)
	return scanLines(ctx, res.State.Path, r, 0, maxLine, fn, res)
}

// capReader caps the bytes read from a decoder: it fails with ErrTooLarge once
// more than left bytes would be produced, and labels decoder errors.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		// Exactly at the cap: only more data is an error, not a clean end.
		var one [1]byte
		n, err := c.r.Read(one[:])
		if n > 0 {
			return 0, ErrTooLarge
		}
		return 0, c.label(err)
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, c.label(err)
}

func (c *capReader) label(err error) error {
	if err == nil || err == io.EOF {
		return err
	}
	return &DecodeError{Err: err}
}

// scanLines hands the complete lines of r to fn. start is the file offset of
// r's first byte; res carries the state being built and gets Offset, Lines and
// Warnings filled in.
func scanLines(ctx context.Context, path string, r *bufio.Reader, start int64, maxLine int, fn LineFunc, res FileResult) (FileResult, error) {
	res.State.Offset = start
	var (
		buf         []byte // line being assembled across buffer refills
		discarding  bool   // current line exceeded maxLine; drop until newline
		pending     int64  // bytes of the current line consumed so far
		lineStart   = start
		consumedEnd = start
	)
	for {
		if err := ctx.Err(); err != nil {
			return FileResult{}, err
		}
		chunk, rerr := r.ReadSlice('\n')
		pending += int64(len(chunk))

		if !discarding {
			if len(buf)+len(chunk) > maxLine {
				discarding = true
				buf = nil
			} else if rerr != nil || len(buf) > 0 {
				buf = append(buf, chunk...)
			}
		}

		switch {
		case rerr == nil:
			if discarding {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("%s: line at offset %d exceeds %d bytes, skipped", path, lineStart, maxLine))
			} else {
				line := buf
				if len(line) == 0 {
					line = chunk
				}
				line = bytes.TrimSpace(line)
				if len(line) > 0 {
					if err := fn(line, lineStart); err != nil {
						return FileResult{}, err
					}
					res.Lines++
				}
			}
			consumedEnd += pending
			lineStart = consumedEnd
			pending = 0
			buf = buf[:0]
			discarding = false
		case errors.Is(rerr, bufio.ErrBufferFull):
			// keep accumulating
		case errors.Is(rerr, io.EOF):
			// Trailing bytes without a newline are not consumed.
			res.State.Offset = consumedEnd
			return res, nil
		default:
			return FileResult{}, rerr
		}
	}
}
