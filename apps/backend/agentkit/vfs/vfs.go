// Package vfs defines a virtual file system for agents: a flat, workspace-scoped key space of files
// addressed by absolute slash-separated paths. Directories are implicit (derived from file paths).
//
// Tools and sandboxes depend only on the FS interface; implementations (memory, SQLite, mounts,
// read-only views) live in subpackages or wrappers and enforce the same path rules and limits.
package vfs

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Errors returned by FS implementations. Wrap them with fmt.Errorf("...: %w", ErrX) when adding
// context; callers match with errors.Is.
var (
	ErrNotFound    = errors.New("vfs: file not found")
	ErrInvalidPath = errors.New("vfs: invalid path")
	ErrIsDir       = errors.New("vfs: path is a directory")
	ErrNotDir      = errors.New("vfs: path is not a directory")
	ErrReadOnly    = errors.New("vfs: read-only file system")
	ErrTooLarge    = errors.New("vfs: file too large")
	ErrQuota       = errors.New("vfs: workspace quota exceeded")
)

// Path limits enforced by Clean.
const (
	MaxPathLen    = 512
	MaxSegmentLen = 255
)

// FileInfo describes a file or an implicit directory.
type FileInfo struct {
	Path    string    `json:"path"`
	IsDir   bool      `json:"is_dir,omitempty"`
	Size    int64     `json:"size"`              // files only
	SHA256  string    `json:"sha256,omitempty"`  // hex digest of the content, files only
	Version int64     `json:"version,omitempty"` // starts at 1, incremented on every write, files only
	ModTime time.Time `json:"mod_time,omitzero"` // files only
}

// FS is a virtual file system. Implementations must be safe for concurrent use.
//
// Semantics every implementation follows (checked by the vfstest conformance suite):
//   - All paths go through Clean; an invalid path yields ErrInvalidPath.
//   - Directories are implicit: a directory exists while at least one file lives under it. "/" always
//     exists. A file and a directory can never share a path (Write to a directory path → ErrIsDir,
//     Write under an existing file path → ErrNotDir).
//   - Stat of a file returns its FileInfo; of an implicit directory {Path, IsDir: true}; otherwise
//     ErrNotFound.
//   - Read of a directory → ErrIsDir; of a missing file → ErrNotFound.
//   - Write creates or replaces a file atomically (parents are implicit) and bumps Version.
//   - Delete removes a single file; a directory → ErrIsDir; missing → ErrNotFound.
//   - List(dir, false) returns the direct children of dir (files and implicit subdirectories);
//     List(dir, true) returns every file below dir (no directory entries). Results are sorted by
//     path. A missing directory → ErrNotFound; a file → ErrNotDir.
//   - Read-only file systems return ErrReadOnly from Write and Delete.
//   - Size/quota violations return ErrTooLarge / ErrQuota and leave the FS unchanged.
type FS interface {
	Stat(ctx context.Context, path string) (FileInfo, error)
	Read(ctx context.Context, path string) ([]byte, error)
	Write(ctx context.Context, path string, data []byte) error
	Delete(ctx context.Context, path string) error
	List(ctx context.Context, dir string, recursive bool) ([]FileInfo, error)
}

// Limits bound a workspace. Zero values mean "use the default".
type Limits struct {
	MaxFileSize  int64 // bytes per file; default 1 MiB
	MaxTotalSize int64 // bytes per workspace; default 20 MiB
	MaxFiles     int   // files per workspace; default 500
}

// Default limits.
const (
	DefaultMaxFileSize  int64 = 1 << 20
	DefaultMaxTotalSize int64 = 20 << 20
	DefaultMaxFiles           = 500
)

// WithDefaults returns l with zero fields replaced by the defaults.
func (l Limits) WithDefaults() Limits {
	if l.MaxFileSize <= 0 {
		l.MaxFileSize = DefaultMaxFileSize
	}
	if l.MaxTotalSize <= 0 {
		l.MaxTotalSize = DefaultMaxTotalSize
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultMaxFiles
	}
	return l
}

// Clean validates and normalizes a path: it must be absolute ("/..."), valid UTF-8, without NUL,
// control characters or backslashes, without "." or ".." segments, within MaxPathLen/MaxSegmentLen.
// Repeated and trailing slashes are removed. "/" is returned as is.
func Clean(p string) (string, error) {
	if p == "" || p[0] != '/' {
		return "", invalid(p, "must be absolute (start with /)")
	}
	if len(p) > MaxPathLen {
		return "", invalid(p, "too long")
	}
	if !utf8.ValidString(p) {
		return "", invalid(p, "not valid UTF-8")
	}
	for _, r := range p {
		if r == '\\' || unicode.IsControl(r) {
			return "", invalid(p, "contains a backslash or control character")
		}
	}
	segs := strings.Split(p, "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		switch s {
		case "":
			continue
		case ".", "..":
			return "", invalid(p, `"." and ".." segments are not allowed`)
		}
		if len(s) > MaxSegmentLen {
			return "", invalid(p, "path segment too long")
		}
		out = append(out, s)
	}
	return "/" + strings.Join(out, "/"), nil
}

// Dir returns the parent directory of a cleaned path ("/" for top-level entries and for "/").
func Dir(clean string) string {
	i := strings.LastIndexByte(clean, '/')
	if i <= 0 {
		return "/"
	}
	return clean[:i]
}

// Base returns the last segment of a cleaned path ("" for "/").
func Base(clean string) string {
	return clean[strings.LastIndexByte(clean, '/')+1:]
}

// IsWithin reports whether cleaned path p equals dir or lies below it.
func IsWithin(p, dir string) bool {
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}

type pathError struct {
	path, reason string
}

func (e *pathError) Error() string { return "vfs: invalid path " + quote(e.path) + ": " + e.reason }
func (e *pathError) Unwrap() error { return ErrInvalidPath }

func invalid(p, reason string) error { return &pathError{path: p, reason: reason} }

// quote shortens s to about 80 bytes (on a rune boundary) and quotes it Go-style, escaping control
// characters and invalid UTF-8 so error messages stay printable.
func quote(s string) string {
	if len(s) > 80 {
		cut := 80
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return strconv.Quote(s)
}
