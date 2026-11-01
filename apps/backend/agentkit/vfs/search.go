package vfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
)

// ErrBadPattern is returned (wrapped, with details) by Glob and Grep for malformed patterns.
var ErrBadPattern = errors.New("vfs: bad pattern")

// MaxGrepContext is the largest number of context lines Grep returns around a match.
const MaxGrepContext = 5

// Glob returns the files (never directories) whose path matches pattern, sorted by path, at most
// limit entries (limit <= 0: no limit; ask for limit+1 to detect truncation).
//
// Pattern is an absolute doublestar pattern matched against cleaned paths: "*" and "?" stay within
// one path segment, "**" spans any number of segments, plus [classes] and {alt,ernatives};
// backslash escapes a meta character. Repeated and trailing slashes are ignored. Only the
// directory named by the pattern's literal prefix is listed ("/work/**/*.md" lists /work), so
// anchored patterns stay cheap. A missing base directory yields no matches, not an error.
func Glob(ctx context.Context, fsys FS, pattern string, limit int) ([]FileInfo, error) {
	pattern, err := normalizePattern(pattern)
	if err != nil {
		return nil, err
	}
	base, rest := doublestar.SplitPattern(pattern)
	if rest == "" {
		return []FileInfo{}, nil // the pattern names "/", which is not a file
	}
	if base, err = Clean(base); err != nil {
		return nil, fmt.Errorf("%w: %q: %w", ErrBadPattern, pattern, err)
	}
	recursive := strings.Contains(rest, "/") || strings.Contains(rest, "**")
	infos, err := fsys.List(ctx, base, recursive)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotDir) {
		return []FileInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []FileInfo{}
	for _, fi := range infos {
		if fi.IsDir || !doublestar.MatchUnvalidated(pattern, fi.Path) {
			continue
		}
		out = append(out, fi)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func normalizePattern(pattern string) (string, error) {
	if !strings.HasPrefix(pattern, "/") {
		return "", fmt.Errorf("%w: %s: must be absolute (start with /)", ErrBadPattern, quote(pattern))
	}
	for strings.Contains(pattern, "//") {
		pattern = strings.ReplaceAll(pattern, "//", "/")
	}
	if len(pattern) > 1 {
		pattern = strings.TrimSuffix(pattern, "/")
	}
	if !doublestar.ValidatePattern(pattern) {
		return "", fmt.Errorf("%w: %s: unbalanced brackets/braces or a trailing backslash", ErrBadPattern, quote(pattern))
	}
	return pattern, nil
}

// GrepOptions configures Grep.
type GrepOptions struct {
	Pattern    string // RE2 regular expression matched against each line (required)
	Glob       string // absolute Glob pattern selecting the files to search (default "/**": all files)
	IgnoreCase bool   // case-insensitive matching
	Context    int    // lines of context before and after each match, clamped to 0..MaxGrepContext
	MaxMatches int    // stop after this many matching lines (<= 0: no limit)
}

// GrepMatch is one matching line. Before holds the lines Line-len(Before) … Line-1 and After the
// lines Line+1 … Line+len(After); context windows of nearby matches may overlap.
type GrepMatch struct {
	Path   string   `json:"path"`
	Line   int      `json:"line"` // 1-based
	Text   string   `json:"text"`
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
}

// Grep searches the text files selected by opts.Glob line by line (see SplitLines) for
// opts.Pattern. Matches are ordered by path, then line. Binary files (not valid UTF-8 or
// containing NUL, see IsText) are skipped, as are files deleted while searching. truncated
// reports that more matches existed beyond opts.MaxMatches.
func Grep(ctx context.Context, fsys FS, opts GrepOptions) (matches []GrepMatch, truncated bool, err error) {
	if opts.Pattern == "" {
		return nil, false, fmt.Errorf("%w: empty regular expression", ErrBadPattern)
	}
	expr := opts.Pattern
	if opts.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrBadPattern, err)
	}
	glob := opts.Glob
	if glob == "" {
		glob = "/**"
	}
	files, err := Glob(ctx, fsys, glob, 0)
	if err != nil {
		return nil, false, err
	}
	n := min(max(opts.Context, 0), MaxGrepContext)
	matches = []GrepMatch{}
	for _, fi := range files {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		data, err := fsys.Read(ctx, fi.Path)
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrIsDir) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if !IsText(data) {
			continue
		}
		lines := SplitLines(string(data))
		for i, line := range lines {
			if !re.MatchString(line) {
				continue
			}
			if opts.MaxMatches > 0 && len(matches) >= opts.MaxMatches {
				return matches, true, nil
			}
			m := GrepMatch{Path: fi.Path, Line: i + 1, Text: line}
			if n > 0 {
				m.Before = lines[max(i-n, 0):i]
				m.After = lines[i+1 : min(i+1+n, len(lines))]
			}
			matches = append(matches, m)
		}
	}
	return matches, false, nil
}

// IsText reports whether data is treated as text: valid UTF-8 without NUL bytes.
func IsText(data []byte) bool {
	return utf8.Valid(data) && bytes.IndexByte(data, 0) < 0
}

// SplitLines splits text into lines the way Grep numbers them: on "\n", dropping a trailing "\r"
// from each line; a final newline does not start an extra line, and empty text has no lines.
func SplitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}
