// Package fstools provides the generic file tools agents use on a vfs.FS: read_file,
// write_file, edit_file, glob and grep. Outputs and error messages are written for the model:
// errors from the file system are translated into messages that say what went wrong and what to
// do instead.
package fstools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tool"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
)

// Tool names.
const (
	ReadFile  = "read_file"
	WriteFile = "write_file"
	EditFile  = "edit_file"
	Glob      = "glob"
	Grep      = "grep"
)

// Default limits.
const (
	DefaultMaxReadLines   = 2000
	DefaultMaxLineLength  = 2000
	DefaultMaxGlobResults = 500
	DefaultMaxGrepMatches = 200
)

// Options configures the tools. Zero values take the defaults.
type Options struct {
	ReadOnly       bool // only read_file, glob and grep
	MaxReadLines   int  // lines returned per read_file call (default 2000)
	MaxLineLength  int  // characters shown per line by read_file and grep; longer lines are cut (default 2000)
	MaxGlobResults int  // paths returned per glob call (default 500)
	MaxGrepMatches int  // matching lines returned per grep call (default 200)
}

func (o Options) withDefaults() Options {
	if o.MaxReadLines <= 0 {
		o.MaxReadLines = DefaultMaxReadLines
	}
	if o.MaxLineLength <= 0 {
		o.MaxLineLength = DefaultMaxLineLength
	}
	if o.MaxGlobResults <= 0 {
		o.MaxGlobResults = DefaultMaxGlobResults
	}
	if o.MaxGrepMatches <= 0 {
		o.MaxGrepMatches = DefaultMaxGrepMatches
	}
	return o
}

// New returns the file tools operating on fsys, in the order read_file, write_file, edit_file,
// glob, grep (write_file and edit_file are omitted when opts.ReadOnly is set). The tools hold no
// state; fsys decides what is readable and writable.
func New(fsys vfs.FS, opts Options) []tool.Tool {
	t := &tools{fs: fsys, o: opts.withDefaults()}
	read := tool.New(ReadFile, fmt.Sprintf(readDesc, t.o.MaxReadLines, t.o.MaxLineLength), t.read)
	glob := tool.New(Glob, fmt.Sprintf(globDesc, t.o.MaxGlobResults), t.glob)
	grep := tool.New(Grep, fmt.Sprintf(grepDesc, vfs.MaxGrepContext, t.o.MaxGrepMatches, t.o.MaxLineLength), t.grep)
	if opts.ReadOnly {
		return []tool.Tool{read, glob, grep}
	}
	return []tool.Tool{
		read,
		tool.New(WriteFile, writeDesc, t.write),
		tool.New(EditFile, editDesc, t.edit),
		glob,
		grep,
	}
}

const readDesc = `Read a text file. Returns its lines numbered from 1 in the form "<number>\t<line>" (the number and tab are not part of the file content). At most %d lines are returned per call: for longer files use offset (1-based line to start at) and limit, as indicated by the footer that says which lines were shown. Lines longer than %d characters are cut and marked as truncated. Paths are absolute, e.g. /work/notes.md. Binary files cannot be read; for a directory use glob to list its files.`

const writeDesc = `Create a file or replace its entire content. Parent directories are created implicitly. Paths are absolute, e.g. /work/report.md. To change part of an existing file prefer edit_file. Returns JSON {"path", "bytes", "version"}; version starts at 1 and increases by one on every write to the file.`

const editDesc = `Replace an exact substring in an existing text file. old_string must match the file content exactly, character for character, including whitespace, indentation and line breaks; copy it from read_file output without the line-number prefix. By default old_string must occur exactly once: include enough surrounding text to make it unique, or set replace_all to true to replace every occurrence. new_string may be empty to delete old_string. Returns JSON {"path", "replacements", "version"}.`

const globDesc = `Find files by path pattern. Patterns are absolute: * matches any characters within one path segment, ** matches any number of directories, ? matches one character, [abc] a character class, {a,b} alternatives. Examples: /work/*.md, /skills/**/SKILL.md, /**/*.{html,css}. A pattern without a leading / is taken relative to /. Returns the matching file paths, sorted, one per line with their size in bytes, at most %d. Only files are listed (directories exist implicitly while they contain files).`

const grepDesc = `Search file contents with a regular expression (RE2 syntax as in Go and ripgrep: no lookaround or backreferences; escape ( ) [ ] { } . * + ? | ^ $ \ with a backslash to match them literally). Lines are matched one at a time. Output: "path:line: text" for matching lines and, with context, "path-line- text" for surrounding lines; "--" separates non-adjacent groups. glob restricts which files are searched (glob tool syntax; a glob without "/", such as *.md, matches file names at any depth). context adds up to %d lines before and after each match. Binary files are skipped. At most %d matching lines are returned; lines longer than %d characters are cut.`

type tools struct {
	fs vfs.FS
	o  Options
}

// --- read_file ---

type readInput struct {
	Path   string `json:"path" jsonschema_description:"Absolute path of the file, e.g. /work/notes.md."`
	Offset int    `json:"offset,omitempty" jsonschema:"minimum=1" jsonschema_description:"1-based line number to start reading from (default 1)."`
	Limit  int    `json:"limit,omitempty" jsonschema:"minimum=1" jsonschema_description:"Maximum number of lines to return (default and maximum: the per-call line limit from the tool description)."`
}

func (t *tools) read(ctx context.Context, in readInput) (string, error) {
	p, err := vfs.Clean(in.Path)
	if err != nil {
		return "", explain("read", in.Path, err, t.fs)
	}
	if in.Offset < 0 || in.Limit < 0 {
		return "", errors.New("offset and limit must be positive (offset is a 1-based line number)")
	}
	data, err := t.fs.Read(ctx, p)
	if err != nil {
		return "", explain("read", p, err, t.fs)
	}
	if !vfs.IsText(data) {
		return "", fmt.Errorf("cannot read %s: binary file (%d bytes); read_file only shows UTF-8 text", p, len(data))
	}
	lines := vfs.SplitLines(string(data))
	start := max(in.Offset, 1)
	if len(lines) == 0 {
		if start > 1 {
			return "", fmt.Errorf("offset %d is past the end of %s: the file is empty", start, p)
		}
		return fmt.Sprintf("(%s is empty)", p), nil
	}
	if start > len(lines) {
		return "", fmt.Errorf("offset %d is past the end of %s, which has %d lines", start, p, len(lines))
	}
	limit := t.o.MaxReadLines
	if in.Limit > 0 && in.Limit < limit {
		limit = in.Limit
	}
	end := min(start-1+limit, len(lines)) // last line shown, 1-based
	var b strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i, t.cut(lines[i-1]))
	}
	switch {
	case end < len(lines):
		fmt.Fprintf(&b, "(lines %d–%d of %d; use offset=%d to read more)", start, end, len(lines), end+1)
	case start > 1:
		fmt.Fprintf(&b, "(lines %d–%d of %d; end of file)", start, end, len(lines))
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// cut shortens a line to MaxLineLength characters.
func (t *tools) cut(line string) string {
	if len(line) <= t.o.MaxLineLength { // fast path: bytes >= characters
		return line
	}
	n := utf8.RuneCountInString(line)
	if n <= t.o.MaxLineLength {
		return line
	}
	i, count := 0, 0
	for i = range line {
		if count == t.o.MaxLineLength {
			break
		}
		count++
	}
	return fmt.Sprintf("%s… [line truncated: %d characters]", line[:i], n)
}

// --- write_file ---

type writeInput struct {
	Path    string  `json:"path" jsonschema_description:"Absolute path of the file to create or replace, e.g. /work/report.md."`
	Content *string `json:"content" jsonschema_description:"The complete new content of the file."`
}

type writeOutput struct {
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
	Version int64  `json:"version"`
}

func (t *tools) write(ctx context.Context, in writeInput) (writeOutput, error) {
	p, err := vfs.Clean(in.Path)
	if err != nil {
		return writeOutput{}, explain("write", in.Path, err, t.fs)
	}
	if in.Content == nil {
		return writeOutput{}, errors.New(`content is required (use "" to create an empty file)`)
	}
	if err := t.fs.Write(ctx, p, []byte(*in.Content)); err != nil {
		return writeOutput{}, explain("write", p, err, t.fs)
	}
	return writeOutput{Path: p, Bytes: len(*in.Content), Version: t.version(ctx, p)}, nil
}

// version returns the current version of a file just written (0 if it cannot be determined; the
// write itself succeeded, so this is not an error).
func (t *tools) version(ctx context.Context, p string) int64 {
	fi, err := t.fs.Stat(ctx, p)
	if err != nil {
		return 0
	}
	return fi.Version
}

// --- edit_file ---

type editInput struct {
	Path       string  `json:"path" jsonschema_description:"Absolute path of the file to edit."`
	OldString  string  `json:"old_string" jsonschema_description:"Exact text to replace, copied from the file (without read_file line-number prefixes)."`
	NewString  *string `json:"new_string" jsonschema_description:"Replacement text; empty to delete old_string."`
	ReplaceAll bool    `json:"replace_all,omitempty" jsonschema_description:"Replace every occurrence instead of requiring exactly one (default false)."`
}

type editOutput struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
	Version      int64  `json:"version"`
}

var lineNumberPrefix = regexp.MustCompile(`(?m)^ *\d+\t`)

func (t *tools) edit(ctx context.Context, in editInput) (editOutput, error) {
	p, err := vfs.Clean(in.Path)
	if err != nil {
		return editOutput{}, explain("edit", in.Path, err, t.fs)
	}
	if in.OldString == "" {
		return editOutput{}, errors.New("old_string must not be empty; to create a file or replace its whole content use write_file")
	}
	if in.NewString == nil {
		return editOutput{}, errors.New(`new_string is required (use "" to delete old_string)`)
	}
	oldS, newS := in.OldString, *in.NewString
	if oldS == newS {
		return editOutput{}, errors.New("old_string and new_string are identical; there is nothing to change")
	}
	data, err := t.fs.Read(ctx, p)
	if err != nil {
		return editOutput{}, explain("edit", p, err, t.fs)
	}
	if !vfs.IsText(data) {
		return editOutput{}, fmt.Errorf("cannot edit %s: binary file (%d bytes); edit_file only works on UTF-8 text", p, len(data))
	}
	content := string(data)
	n := strings.Count(content, oldS)
	if n == 0 && strings.Contains(content, "\r\n") && strings.Contains(oldS, "\n") && !strings.Contains(oldS, "\r") {
		// read_file hides CRLF line endings; adapt the edit to the file's convention.
		crlf := strings.ReplaceAll(oldS, "\n", "\r\n")
		if m := strings.Count(content, crlf); m > 0 {
			oldS, newS, n = crlf, strings.ReplaceAll(newS, "\n", "\r\n"), m
		}
	}
	switch {
	case n == 0:
		msg := fmt.Sprintf("old_string was not found in %s. It must match the file exactly, including whitespace, indentation and line breaks. Re-read the file with read_file and copy the text exactly, without the line-number prefixes.", p)
		if lineNumberPrefix.MatchString(oldS) {
			msg += " Your old_string looks like it contains read_file line-number prefixes (digits followed by a tab); remove them."
		} else if strings.Contains(collapseSpace(content), collapseSpace(oldS)) {
			msg += " Text matching old_string up to whitespace exists: check spaces vs tabs, indentation and line breaks."
		}
		return editOutput{}, errors.New(msg)
	case n > 1 && !in.ReplaceAll:
		return editOutput{}, fmt.Errorf("old_string occurs %d times in %s. Include more surrounding text in old_string so it matches exactly one place, or set replace_all to true to replace all %d occurrences.", n, p, n)
	}
	limit := 1
	if in.ReplaceAll {
		limit = -1
	}
	if err := t.fs.Write(ctx, p, []byte(strings.Replace(content, oldS, newS, limit))); err != nil {
		return editOutput{}, explain("edit", p, err, t.fs)
	}
	if !in.ReplaceAll {
		n = 1
	}
	return editOutput{Path: p, Replacements: n, Version: t.version(ctx, p)}, nil
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// --- glob ---

type globInput struct {
	Pattern string `json:"pattern" jsonschema_description:"Absolute glob pattern, e.g. /work/**/*.md."`
}

func (t *tools) glob(ctx context.Context, in globInput) (string, error) {
	pattern := strings.TrimSpace(in.Pattern)
	if pattern == "" {
		return "", errors.New("pattern is required, e.g. /work/**/*.md or /**/* for every file")
	}
	if !strings.HasPrefix(pattern, "/") {
		pattern = "/" + pattern
	}
	infos, err := vfs.Glob(ctx, t.fs, pattern, t.o.MaxGlobResults+1)
	if err != nil {
		return "", explainSearch("glob pattern", err, t.fs)
	}
	if len(infos) == 0 {
		return fmt.Sprintf("No files match %s.", pattern), nil
	}
	truncated := len(infos) > t.o.MaxGlobResults
	if truncated {
		infos = infos[:t.o.MaxGlobResults]
	}
	var b strings.Builder
	for _, fi := range infos {
		fmt.Fprintf(&b, "%s (%d bytes)\n", fi.Path, fi.Size)
	}
	if truncated {
		fmt.Fprintf(&b, "(showing the first %d matches; more files match, use a more specific pattern)", t.o.MaxGlobResults)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// --- grep ---

type grepInput struct {
	Pattern    string `json:"pattern" jsonschema_description:"RE2 regular expression to search for, e.g. TODO|FIXME or func \\w+\\(."`
	Glob       string `json:"glob,omitempty" jsonschema_description:"Only search files matching this glob, e.g. /work/**/*.md or *.css (default: all files)."`
	IgnoreCase bool   `json:"ignore_case,omitempty" jsonschema_description:"Case-insensitive search (default false)."`
	Context    int    `json:"context,omitempty" jsonschema:"minimum=0,maximum=5" jsonschema_description:"Lines of context to show before and after each match (0-5, default 0)."`
}

func (t *tools) grep(ctx context.Context, in grepInput) (string, error) {
	if in.Pattern == "" {
		return "", errors.New("pattern is required (an RE2 regular expression, e.g. TODO|FIXME)")
	}
	glob := strings.TrimSpace(in.Glob)
	switch {
	case glob == "":
		glob = "/**"
	case !strings.Contains(glob, "/"):
		glob = "/**/" + glob
	case !strings.HasPrefix(glob, "/"):
		glob = "/" + glob
	}
	n := min(max(in.Context, 0), vfs.MaxGrepContext)
	matches, truncated, err := vfs.Grep(ctx, t.fs, vfs.GrepOptions{
		Pattern:    in.Pattern,
		Glob:       glob,
		IgnoreCase: in.IgnoreCase,
		Context:    n,
		MaxMatches: t.o.MaxGrepMatches,
	})
	if err != nil {
		return "", explainSearch("pattern or glob", err, t.fs)
	}
	if len(matches) == 0 {
		return fmt.Sprintf("No lines match %q in files matching %s.", in.Pattern, glob), nil
	}

	type line struct {
		text  string
		match bool
	}
	var b strings.Builder
	for start := 0; start < len(matches); {
		path := matches[start].Path
		end := start
		lines := map[int]line{}
		for ; end < len(matches) && matches[end].Path == path; end++ {
			m := matches[end]
			for i, s := range m.Before {
				if no := m.Line - len(m.Before) + i; !lines[no].match {
					lines[no] = line{text: s}
				}
			}
			for i, s := range m.After {
				if no := m.Line + 1 + i; !lines[no].match {
					lines[no] = line{text: s}
				}
			}
			lines[m.Line] = line{text: m.Text, match: true}
		}
		nums := make([]int, 0, len(lines))
		for no := range lines {
			nums = append(nums, no)
		}
		sort.Ints(nums)
		if start > 0 && n > 0 {
			b.WriteString("--\n")
		}
		for i, no := range nums {
			if i > 0 && no != nums[i-1]+1 {
				b.WriteString("--\n")
			}
			sep := "-"
			if lines[no].match {
				sep = ":"
			}
			fmt.Fprintf(&b, "%s%s%d%s %s\n", path, sep, no, sep, t.cut(lines[no].text))
		}
		start = end
	}
	if truncated {
		fmt.Fprintf(&b, "(stopped after %d matching lines; more lines match, narrow the pattern or the glob)", t.o.MaxGrepMatches)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// --- error translation ---

func detail(err error) string { return strings.TrimPrefix(err.Error(), "vfs: ") }

// explain turns a vfs error for path into a message telling the model what to do.
func explain(op, path string, err error, fsys vfs.FS) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	writing := op == "write" || op == "edit"
	switch {
	case errors.Is(err, vfs.ErrInvalidPath):
		return fmt.Errorf(`%s. Paths are absolute and "/"-separated, e.g. /work/notes.md, without "." or ".." segments or backslashes`, detail(err))
	case errors.Is(err, vfs.ErrNotFound):
		return fmt.Errorf("file not found: %s. Check the path; use glob (e.g. pattern %q) to see which files exist", path, globHint(vfs.Dir(path)))
	case errors.Is(err, vfs.ErrIsDir):
		if writing {
			return fmt.Errorf("cannot %s %s: it is a directory. Choose a file path inside it, e.g. %s", op, path, joinPath(path, "file.md"))
		}
		return fmt.Errorf("%s is a directory, not a file. Use glob with pattern %q to list its files", path, globHint(path))
	case errors.Is(err, vfs.ErrNotDir):
		return fmt.Errorf("cannot %s %s: %s is a file, so it cannot contain other files. Choose a different path", op, path, fileAncestor(fsys, path))
	case errors.Is(err, vfs.ErrReadOnly):
		return fmt.Errorf("cannot %s %s: %s. Files here can be read but not changed; write to a writable location instead", op, path, detail(err))
	case errors.Is(err, vfs.ErrTooLarge):
		return fmt.Errorf("cannot %s %s: %s. Split the content into several smaller files", op, path, detail(err))
	case errors.Is(err, vfs.ErrQuota):
		return fmt.Errorf("cannot %s %s: %s. Write less, or shorten files you no longer need", op, path, detail(err))
	}
	return fmt.Errorf("cannot %s %s: %v", op, path, err)
}

func explainSearch(what string, err error, fsys vfs.FS) error {
	if errors.Is(err, vfs.ErrBadPattern) {
		return fmt.Errorf("invalid %s: %s", what, strings.TrimPrefix(detail(err), "bad pattern: "))
	}
	return explain("search", "files", err, fsys)
}

func globHint(dir string) string {
	if dir == "/" {
		return "/**"
	}
	return dir + "/**"
}

func joinPath(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

// fileAncestor returns the ancestor of path that is a file (the reason for ErrNotDir).
func fileAncestor(fsys vfs.FS, path string) string {
	for _, d := range vfs.Parents(path) {
		if fi, err := fsys.Stat(context.Background(), d); err == nil && !fi.IsDir {
			return d
		}
	}
	return "a parent path"
}
