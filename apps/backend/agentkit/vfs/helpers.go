package vfs

import (
	"fmt"
	"slices"
	"strings"
)

// Usage is the current size of a workspace, used for quota checks.
type Usage struct {
	Files int   // number of files
	Bytes int64 // total size of all files
}

// CheckFileSize returns an error wrapping ErrTooLarge if a file of size bytes exceeds
// l.MaxFileSize (after applying defaults).
func (l Limits) CheckFileSize(size int64) error {
	l = l.WithDefaults()
	if size > l.MaxFileSize {
		return fmt.Errorf("%w: %d bytes, the limit is %d bytes per file", ErrTooLarge, size, l.MaxFileSize)
	}
	return nil
}

// CheckQuota returns an error wrapping ErrQuota if writing size bytes would push the workspace
// past l.MaxFiles or l.MaxTotalSize (after applying defaults). u is the usage before the write;
// replaces reports whether the write replaces an existing file of oldSize bytes (replacing never
// changes the file count and only the size delta counts towards the total). Implementations must
// run the check and the write atomically.
func (l Limits) CheckQuota(u Usage, size int64, replaces bool, oldSize int64) error {
	l = l.WithDefaults()
	if !replaces && u.Files >= l.MaxFiles {
		return fmt.Errorf("%w: the workspace already holds %d files (limit %d)", ErrQuota, u.Files, l.MaxFiles)
	}
	total := u.Bytes + size
	if replaces {
		total -= oldSize
	}
	if total > l.MaxTotalSize {
		return fmt.Errorf("%w: total size would be %d bytes (limit %d)", ErrQuota, total, l.MaxTotalSize)
	}
	return nil
}

// Parents returns the ancestor directories of cleaned path p, outermost first, excluding "/"
// (Parents("/a/b/c") = ["/a", "/a/b"]).
func Parents(clean string) []string {
	var out []string
	for i := 1; i < len(clean); i++ {
		if clean[i] == '/' {
			out = append(out, clean[:i])
		}
	}
	return out
}

// Children derives the direct children of cleaned directory dir from files, a list of file
// entries (no directories) lying below dir, as returned by a recursive List. Files directly in
// dir are returned as is; deeper files collapse into one implicit directory entry per
// subdirectory. The result is sorted by path. Entries outside dir are ignored.
//
// It is a helper for FS implementations that store files in a flat key space.
func Children(dir string, files []FileInfo) []FileInfo {
	prefix := dir + "/"
	if dir == "/" {
		prefix = "/"
	}
	out := make([]FileInfo, 0, len(files))
	seen := map[string]bool{}
	for _, f := range files {
		rel, ok := strings.CutPrefix(f.Path, prefix)
		if !ok || rel == "" {
			continue
		}
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			sub := prefix + rel[:i]
			if !seen[sub] {
				seen[sub] = true
				out = append(out, FileInfo{Path: sub, IsDir: true})
			}
			continue
		}
		out = append(out, f)
	}
	SortInfos(out)
	return out
}

// SortInfos sorts entries by path (byte-wise, the order List returns).
func SortInfos(infos []FileInfo) {
	slices.SortFunc(infos, func(a, b FileInfo) int { return strings.Compare(a.Path, b.Path) })
}
