package fstools_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tool"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tools/fstools"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs/memory"
)

// env drives the tools through tool.Tool.Call, the same entry point agents use.
type env struct {
	t      *testing.T
	fsys   vfs.FS
	names  []string // tool names in registration order
	byName map[string]tool.Tool
}

func newEnv(t *testing.T, opts fstools.Options) *env {
	t.Helper()
	fsys := memory.New(vfs.Limits{})
	tools := fstools.New(fsys, opts)
	e := &env{t: t, fsys: fsys, byName: map[string]tool.Tool{}}
	for _, tl := range tools {
		name := tl.Spec().Name
		e.names = append(e.names, name)
		e.byName[name] = tl
	}
	return e
}

func (e *env) seed(files map[string]string) {
	e.t.Helper()
	for p, content := range files {
		if err := e.fsys.Write(context.Background(), p, []byte(content)); err != nil {
			e.t.Fatalf("seed %s: %v", p, err)
		}
	}
}

func (e *env) call(name, input string) (string, error) {
	e.t.Helper()
	tl, ok := e.byName[name]
	if !ok {
		e.t.Fatalf("tool %q is not registered; tools: %v", name, e.names)
	}
	return tl.Call(context.Background(), json.RawMessage(input))
}

func (e *env) mustCall(name, input string) string {
	e.t.Helper()
	out, err := e.call(name, input)
	if err != nil {
		e.t.Fatalf("%s(%s): unexpected error: %v", name, input, err)
	}
	return out
}

// mustFail asserts the call fails with an error mentioning wantSubstring.
func (e *env) mustFail(name, input, wantSubstring string) {
	e.t.Helper()
	out, err := e.call(name, input)
	if err == nil {
		e.t.Fatalf("%s(%s) = %q, want an error mentioning %q", name, input, out, wantSubstring)
	}
	if !strings.Contains(err.Error(), wantSubstring) {
		e.t.Fatalf("%s(%s) error = %q, want it to mention %q", name, input, err, wantSubstring)
	}
}

func (e *env) content(p string) string {
	e.t.Helper()
	data, err := e.fsys.Read(context.Background(), p)
	if err != nil {
		e.t.Fatalf("read %s back from the FS: %v", p, err)
	}
	return string(data)
}

func TestToolSet(t *testing.T) {
	full := newEnv(t, fstools.Options{})
	if want := []string{"read_file", "write_file", "edit_file", "glob", "grep"}; !slices.Equal(full.names, want) {
		t.Errorf("tool order = %v, want %v", full.names, want)
	}
	ro := newEnv(t, fstools.Options{ReadOnly: true})
	if want := []string{"read_file", "glob", "grep"}; !slices.Equal(ro.names, want) {
		t.Errorf("read-only tool order = %v, want %v", ro.names, want)
	}
	// The read-only set still works.
	ro.seed(map[string]string{"/a.md": "alpha\n"})
	if out := ro.mustCall(fstools.ReadFile, `{"path":"/a.md"}`); out != "     1\talpha" {
		t.Errorf("read-only read_file = %q", out)
	}
	if out := ro.mustCall(fstools.Glob, `{"pattern":"/*.md"}`); out != "/a.md (6 bytes)" {
		t.Errorf("read-only glob = %q", out)
	}
	if out := ro.mustCall(fstools.Grep, `{"pattern":"alpha"}`); out != "/a.md:1: alpha" {
		t.Errorf("read-only grep = %q", out)
	}
}

func TestReadFile(t *testing.T) {
	e := newEnv(t, fstools.Options{})
	e.seed(map[string]string{
		"/work/notes.md": "alpha\nbravo\ncharlie\ndelta\necho\n",
		"/work/empty.md": "",
		"/work/bin.dat":  "a\x00b",
	})

	// Whole file, numbered lines, no footer when everything fits.
	got := e.mustCall(fstools.ReadFile, `{"path":"/work/notes.md"}`)
	want := "     1\talpha\n     2\tbravo\n     3\tcharlie\n     4\tdelta\n     5\techo"
	if got != want {
		t.Errorf("read =\n%q\nwant\n%q", got, want)
	}

	// offset/limit window with the "read more" footer.
	got = e.mustCall(fstools.ReadFile, `{"path":"/work/notes.md","offset":3,"limit":2}`)
	want = "     3\tcharlie\n     4\tdelta\n(lines 3–4 of 5; use offset=5 to read more)"
	if got != want {
		t.Errorf("read offset=3 limit=2 =\n%q\nwant\n%q", got, want)
	}

	// A window reaching the end says so.
	got = e.mustCall(fstools.ReadFile, `{"path":"/work/notes.md","offset":4}`)
	want = "     4\tdelta\n     5\techo\n(lines 4–5 of 5; end of file)"
	if got != want {
		t.Errorf("read offset=4 =\n%q\nwant\n%q", got, want)
	}

	e.mustFail(fstools.ReadFile, `{"path":"/work/notes.md","offset":6}`, "offset 6 is past the end of /work/notes.md, which has 5 lines")
	e.mustFail(fstools.ReadFile, `{"path":"/work/notes.md","offset":-1}`, "offset and limit must be positive")
	e.mustFail(fstools.ReadFile, `{"path":"/work/notes.md","limit":-1}`, "offset and limit must be positive")

	if got := e.mustCall(fstools.ReadFile, `{"path":"/work/empty.md"}`); got != "(/work/empty.md is empty)" {
		t.Errorf("read of empty file = %q", got)
	}
	e.mustFail(fstools.ReadFile, `{"path":"/work/empty.md","offset":2}`, "offset 2 is past the end of /work/empty.md: the file is empty")

	// Errors carry advice.
	e.mustFail(fstools.ReadFile, `{"path":"/work/bin.dat"}`, "binary file (3 bytes)")
	e.mustFail(fstools.ReadFile, `{"path":"/work"}`, "is a directory, not a file")
	e.mustFail(fstools.ReadFile, `{"path":"/missing.md"}`, "file not found: /missing.md")
	e.mustFail(fstools.ReadFile, `{"path":"relative.md"}`, "must be absolute")
}

func TestReadFileTruncatesLongLines(t *testing.T) {
	e := newEnv(t, fstools.Options{MaxLineLength: 10})
	e.seed(map[string]string{"/f.txt": "1234567890123456789012345\nshort\n"})
	got := e.mustCall(fstools.ReadFile, `{"path":"/f.txt"}`)
	want := "     1\t1234567890… [line truncated: 25 characters]\n     2\tshort"
	if got != want {
		t.Errorf("read =\n%q\nwant\n%q", got, want)
	}
}

func TestReadFileLineLimit(t *testing.T) {
	e := newEnv(t, fstools.Options{MaxReadLines: 2})
	e.seed(map[string]string{"/f.txt": "one\ntwo\nthree\nfour\n"})
	got := e.mustCall(fstools.ReadFile, `{"path":"/f.txt"}`)
	want := "     1\tone\n     2\ttwo\n(lines 1–2 of 4; use offset=3 to read more)"
	if got != want {
		t.Errorf("read =\n%q\nwant\n%q", got, want)
	}
	// limit cannot exceed the per-call line limit.
	got = e.mustCall(fstools.ReadFile, `{"path":"/f.txt","offset":3,"limit":100}`)
	want = "     3\tthree\n     4\tfour\n(lines 3–4 of 4; end of file)"
	if got != want {
		t.Errorf("read offset=3 limit=100 =\n%q\nwant\n%q", got, want)
	}
}

type writeResult struct {
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
	Version int64  `json:"version"`
}

func TestWriteFile(t *testing.T) {
	e := newEnv(t, fstools.Options{})

	got := e.mustCall(fstools.WriteFile, `{"path":"/work/report.md","content":"hello\nworld\n"}`)
	var w writeResult
	if err := json.Unmarshal([]byte(got), &w); err != nil {
		t.Fatalf("write output is not JSON: %v (%q)", err, got)
	}
	if w.Path != "/work/report.md" || w.Bytes != 12 || w.Version != 1 {
		t.Errorf("write output = %+v", w)
	}
	if c := e.content("/work/report.md"); c != "hello\nworld\n" {
		t.Errorf("content = %q", c)
	}

	// Replacing bumps the version.
	got = e.mustCall(fstools.WriteFile, `{"path":"/work/report.md","content":"bye\n"}`)
	if err := json.Unmarshal([]byte(got), &w); err != nil {
		t.Fatalf("write output is not JSON: %v", err)
	}
	if w.Bytes != 4 || w.Version != 2 {
		t.Errorf("second write output = %+v", w)
	}
	if fi, err := e.fsys.Stat(context.Background(), "/work/report.md"); err != nil || fi.Version != 2 || fi.Size != 4 {
		t.Errorf("Stat after write = %+v, %v", fi, err)
	}

	// Parents are implicit; empty content is allowed explicitly.
	e.mustCall(fstools.WriteFile, `{"path":"/deep/nested/dir/file.txt","content":""}`)
	if fi, err := e.fsys.Stat(context.Background(), "/deep/nested/dir/file.txt"); err != nil || fi.Size != 0 {
		t.Errorf("Stat of empty file = %+v, %v", fi, err)
	}

	e.mustFail(fstools.WriteFile, `{"path":"/x.txt"}`, "content is required")
	e.mustFail(fstools.WriteFile, `{"path":"relative","content":"x"}`, "must be absolute")
}

func TestEditFile(t *testing.T) {
	e := newEnv(t, fstools.Options{})
	e.seed(map[string]string{"/work/app.go": "package main\n\nfunc main() {}\n"})

	got := e.mustCall(fstools.EditFile, `{"path":"/work/app.go","old_string":"func main() {}","new_string":"func main() { run() }"}`)
	var out struct {
		Path         string `json:"path"`
		Replacements int    `json:"replacements"`
		Version      int64  `json:"version"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("edit output is not JSON: %v (%q)", err, got)
	}
	if out.Path != "/work/app.go" || out.Replacements != 1 || out.Version != 2 {
		t.Errorf("edit output = %+v", out)
	}
	if c := e.content("/work/app.go"); c != "package main\n\nfunc main() { run() }\n" {
		t.Errorf("content after edit = %q", c)
	}

	// Deleting with an empty new_string.
	e.seed(map[string]string{"/work/hello.txt": "hello world\n"})
	e.mustCall(fstools.EditFile, `{"path":"/work/hello.txt","old_string":" world","new_string":""}`)
	if c := e.content("/work/hello.txt"); c != "hello\n" {
		t.Errorf("content after delete-edit = %q", c)
	}
}

func TestEditFileErrors(t *testing.T) {
	e := newEnv(t, fstools.Options{})
	e.seed(map[string]string{
		"/work/app.go":  "package main\n\nfunc main() {}\n",
		"/work/dup.txt": "foo bar foo\n",
	})

	// Zero matches: the file is unchanged.
	e.mustFail(fstools.EditFile, `{"path":"/work/app.go","old_string":"does not exist","new_string":"x"}`, "was not found in /work/app.go")
	if c := e.content("/work/app.go"); c != "package main\n\nfunc main() {}\n" {
		t.Errorf("failed edit changed the file: %q", c)
	}
	// The hint about line-number prefixes fires.
	e.mustFail(fstools.EditFile, `{"path":"/work/app.go","old_string":"     1\tpackage main","new_string":"x"}`, "line-number prefixes")

	// Multiple matches without replace_all.
	e.mustFail(fstools.EditFile, `{"path":"/work/dup.txt","old_string":"foo","new_string":"x"}`, "occurs 2 times in /work/dup.txt")
	if c := e.content("/work/dup.txt"); c != "foo bar foo\n" {
		t.Errorf("ambiguous edit changed the file: %q", c)
	}

	e.mustFail(fstools.EditFile, `{"path":"/work/app.go","old_string":"","new_string":"x"}`, "old_string must not be empty")
	e.mustFail(fstools.EditFile, `{"path":"/work/app.go","old_string":"main"}`, "new_string is required")
	e.mustFail(fstools.EditFile, `{"path":"/work/app.go","old_string":"main","new_string":"main"}`, "identical")
	e.mustFail(fstools.EditFile, `{"path":"/missing.go","old_string":"x","new_string":"y"}`, "file not found: /missing.go")
	e.mustFail(fstools.EditFile, `{"path":"relative","old_string":"x","new_string":"y"}`, "must be absolute")
}

func TestEditFileReplaceAll(t *testing.T) {
	e := newEnv(t, fstools.Options{})
	e.seed(map[string]string{"/work/dup.txt": "foo bar foo\n"})
	got := e.mustCall(fstools.EditFile, `{"path":"/work/dup.txt","old_string":"foo","new_string":"x","replace_all":true}`)
	if !strings.Contains(got, `"replacements":2`) || !strings.Contains(got, `"version":2`) {
		t.Errorf("replace_all output = %s", got)
	}
	if c := e.content("/work/dup.txt"); c != "x bar x\n" {
		t.Errorf("content after replace_all = %q", c)
	}
}

func TestGlob(t *testing.T) {
	e := newEnv(t, fstools.Options{})
	e.seed(map[string]string{
		"/work/a.md":     "a",
		"/work/b.txt":    "b",
		"/work/sub/c.md": "cc",
		"/root.md":       "r",
	})

	got := e.mustCall(fstools.Glob, `{"pattern":"/work/**/*.md"}`)
	want := "/work/a.md (1 bytes)\n/work/sub/c.md (2 bytes)"
	if got != want {
		t.Errorf("glob =\n%q\nwant\n%q", got, want)
	}

	// A pattern without a leading / is taken relative to /.
	if got := e.mustCall(fstools.Glob, `{"pattern":"work/*.txt"}`); got != "/work/b.txt (1 bytes)" {
		t.Errorf("relative glob = %q", got)
	}

	if got := e.mustCall(fstools.Glob, `{"pattern":"/nothing/**"}`); got != "No files match /nothing/**." {
		t.Errorf("empty glob = %q", got)
	}

	e.mustFail(fstools.Glob, `{"pattern":"/[a"}`, "invalid glob pattern")
}

func TestGlobCap(t *testing.T) {
	e := newEnv(t, fstools.Options{MaxGlobResults: 2})
	e.seed(map[string]string{"/f1.md": "1", "/f2.md": "2", "/f3.md": "3"})
	got := e.mustCall(fstools.Glob, `{"pattern":"/*.md"}`)
	want := "/f1.md (1 bytes)\n/f2.md (1 bytes)\n(showing the first 2 matches; more files match, use a more specific pattern)"
	if got != want {
		t.Errorf("capped glob =\n%q\nwant\n%q", got, want)
	}
}

func TestGrep(t *testing.T) {
	e := newEnv(t, fstools.Options{})
	e.seed(map[string]string{
		"/docs/intro.md": "first\nsecond\nthird\nfourth\nfifth\n",
		"/code/main.go":  "func third() {}\n",
		"/docs/bin.md":   "third\x00hidden", // binary: skipped
	})

	// Matches are ordered by path, then line; the binary file is skipped.
	got := e.mustCall(fstools.Grep, `{"pattern":"third"}`)
	want := "/code/main.go:1: func third() {}\n/docs/intro.md:3: third"
	if got != want {
		t.Errorf("grep =\n%q\nwant\n%q", got, want)
	}

	// Context lines use "-", matches ":", "--" separates non-adjacent groups and files.
	got = e.mustCall(fstools.Grep, `{"pattern":"third","context":1}`)
	want = "/code/main.go:1: func third() {}\n--\n" +
		"/docs/intro.md-2- second\n/docs/intro.md:3: third\n/docs/intro.md-4- fourth"
	if got != want {
		t.Errorf("grep with context =\n%q\nwant\n%q", got, want)
	}

	// ignore_case.
	wantBasic := "/code/main.go:1: func third() {}\n/docs/intro.md:3: third"
	if got := e.mustCall(fstools.Grep, `{"pattern":"THIRD","ignore_case":true}`); got != wantBasic {
		t.Errorf("ignore_case grep = %q, want %q", got, wantBasic)
	}

	// A glob without "/" matches file names at any depth.
	if got := e.mustCall(fstools.Grep, `{"pattern":"third","glob":"*.md"}`); got != "/docs/intro.md:3: third" {
		t.Errorf("glob-filtered grep = %q", got)
	}
	// An absolute glob restricts to a subtree.
	if got := e.mustCall(fstools.Grep, `{"pattern":"third","glob":"/docs/**"}`); got != "/docs/intro.md:3: third" {
		t.Errorf("subtree grep = %q", got)
	}

	if got := e.mustCall(fstools.Grep, `{"pattern":"zzz"}`); got != `No lines match "zzz" in files matching /**.` {
		t.Errorf("empty grep = %q", got)
	}
	e.mustFail(fstools.Grep, `{"pattern":"[unclosed"}`, "invalid pattern or glob")
	e.mustFail(fstools.Grep, `{"pattern":""}`, "pattern is required")
}

func TestGrepCap(t *testing.T) {
	e := newEnv(t, fstools.Options{MaxGrepMatches: 2})
	e.seed(map[string]string{"/many.txt": "hit\nhit\nhit\nhit\n"})
	got := e.mustCall(fstools.Grep, `{"pattern":"hit"}`)
	want := "/many.txt:1: hit\n/many.txt:2: hit\n(stopped after 2 matching lines; more lines match, narrow the pattern or the glob)"
	if got != want {
		t.Errorf("capped grep =\n%q\nwant\n%q", got, want)
	}
}

func TestGrepTruncatesLongLines(t *testing.T) {
	e := newEnv(t, fstools.Options{MaxLineLength: 10})
	e.seed(map[string]string{"/f.txt": "TODO 12345678901234567890\n"})
	got := e.mustCall(fstools.Grep, `{"pattern":"TODO"}`)
	want := "/f.txt:1: TODO 12345… [line truncated: 25 characters]"
	if got != want {
		t.Errorf("grep =\n%q\nwant\n%q", got, want)
	}
}
