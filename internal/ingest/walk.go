package ingest

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// WalkJSONL returns the *.jsonl files under root in lexical order.
//
// Symlinks inside the tree are never followed (neither files nor
// directories). root itself may be a symlink; it is resolved first. A
// directory that cannot be read is skipped and reported in warnings. A root
// that does not exist yields an error wrapping fs.ErrNotExist.
func WalkJSONL(ctx context.Context, root string) (files, warnings []string, err error) {
	return WalkMatching(ctx, root, isPlainJSONL)
}

// WalkJSONLWithCompressed is WalkJSONL that also returns the zstd-compressed
// *.jsonl.zst files (see CompressedSuffix).
func WalkJSONLWithCompressed(ctx context.Context, root string) (files, warnings []string, err error) {
	return WalkMatching(ctx, root, func(path string) bool {
		return isPlainJSONL(path) || IsCompressed(path)
	})
}

func isPlainJSONL(path string) bool { return strings.EqualFold(filepath.Ext(path), ".jsonl") }

// WalkMatching returns the regular files under root for which match reports
// true, in lexical order, with the same symlink and error handling as
// WalkJSONL.
func WalkMatching(ctx context.Context, root string, match func(path string) bool) (files, warnings []string, err error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("ingest: %s is not a directory", root)
	}

	w := &walker{ctx: ctx, root: resolved, match: match}
	if err := filepath.WalkDir(resolved, w.visit); err != nil {
		return w.files, w.warnings, err
	}
	return w.files, w.warnings, nil
}

type walker struct {
	ctx      context.Context
	root     string
	match    func(path string) bool
	files    []string
	warnings []string
}

// visit is the filepath.WalkDirFunc behind WalkJSONL.
func (w *walker) visit(path string, d fs.DirEntry, werr error) error {
	if cerr := w.ctx.Err(); cerr != nil {
		return cerr
	}
	if werr != nil {
		if path == w.root {
			return werr
		}
		w.warnings = append(w.warnings, fmt.Sprintf("%s: %v", path, werr))
		if d != nil && d.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
	if d.Type()&fs.ModeSymlink != 0 || d.IsDir() {
		return nil
	}
	if d.Type().IsRegular() && w.match(path) {
		w.files = append(w.files, path)
	}
	return nil
}
