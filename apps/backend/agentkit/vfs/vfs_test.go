package vfs_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
)

func TestClean(t *testing.T) {
	ok := []struct{ in, want string }{
		{"/", "/"},
		{"//", "/"},
		{"///", "/"},
		{"/a", "/a"},
		{"/a/", "/a"},
		{"//a//b///c/", "/a/b/c"},
		{"/a b/c-d_e.f", "/a b/c-d_e.f"},
		{"/...", "/..."},
		{"/.hidden/..x/x..", "/.hidden/..x/x.."},
		{"/100%_done", "/100%_done"},
		{"/ünïcødé/日本語", "/ünïcødé/日本語"},
		{"/" + strings.Repeat("s", vfs.MaxSegmentLen), "/" + strings.Repeat("s", vfs.MaxSegmentLen)},
		{"/" + strings.Repeat("a/", 255), "/" + strings.TrimSuffix(strings.Repeat("a/", 255), "/")}, // 511 bytes
	}
	for _, tc := range ok {
		got, err := vfs.Clean(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("Clean(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	bad := []string{
		"",
		"a",
		"a/b",
		" /a",
		"/.",
		"/..",
		"/a/.",
		"/a/..",
		"/a/../b",
		"/./a",
		`/a\b`,
		`\a`,
		"/a\x00",
		"/a\tb",
		"/a\nb",
		"/a\rb",
		"/a\x7f",
		"/a\u0085b", // C1 control
		"/a\xff",
		"/a\xc3", // truncated rune
		"/" + strings.Repeat("s", vfs.MaxSegmentLen+1),
		"/" + strings.Repeat("a/", 256), // 513 bytes
	}
	for _, in := range bad {
		got, err := vfs.Clean(in)
		if !errors.Is(err, vfs.ErrInvalidPath) {
			t.Errorf("Clean(%q) = %q, %v; want ErrInvalidPath", in, got, err)
		}
	}
}

func TestCleanErrorMessage(t *testing.T) {
	_, err := vfs.Clean("rel")
	if err == nil || !strings.Contains(err.Error(), `"rel"`) || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("err = %v", err)
	}
	// Long paths are shortened on a rune boundary; control characters are escaped.
	_, err = vfs.Clean("/" + strings.Repeat("é", 300))
	if err == nil || !utf8.ValidString(err.Error()) || !strings.Contains(err.Error(), "…") {
		t.Fatalf("long path err = %q", err)
	}
	_, err = vfs.Clean("/a\x00b")
	if err == nil || strings.ContainsRune(err.Error(), 0) || !strings.Contains(err.Error(), `\x00`) {
		t.Fatalf("NUL err = %q", err)
	}
	_, err = vfs.Clean("/\xff")
	if err == nil || !utf8.ValidString(err.Error()) {
		t.Fatalf("invalid UTF-8 err = %q", err)
	}
}

func TestDirBase(t *testing.T) {
	cases := []struct{ in, dir, base string }{
		{"/", "/", ""},
		{"/a", "/", "a"},
		{"/a/b", "/a", "b"},
		{"/a/b/c.txt", "/a/b", "c.txt"},
		{"/.hidden", "/", ".hidden"},
	}
	for _, tc := range cases {
		if got := vfs.Dir(tc.in); got != tc.dir {
			t.Errorf("Dir(%q) = %q, want %q", tc.in, got, tc.dir)
		}
		if got := vfs.Base(tc.in); got != tc.base {
			t.Errorf("Base(%q) = %q, want %q", tc.in, got, tc.base)
		}
	}
}

func TestIsWithin(t *testing.T) {
	cases := []struct {
		p, dir string
		want   bool
	}{
		{"/", "/", true},
		{"/a", "/", true},
		{"/a/b", "/", true},
		{"/a", "/a", true},
		{"/a/b", "/a", true},
		{"/a/b/c", "/a", true},
		{"/ab", "/a", false},
		{"/a.txt", "/a", false},
		{"/a", "/a/b", false},
		{"/", "/a", false},
		{"/b/a", "/a", false},
	}
	for _, tc := range cases {
		if got := vfs.IsWithin(tc.p, tc.dir); got != tc.want {
			t.Errorf("IsWithin(%q, %q) = %v, want %v", tc.p, tc.dir, got, tc.want)
		}
	}
}

func TestLimitsWithDefaults(t *testing.T) {
	l := vfs.Limits{}.WithDefaults()
	if l.MaxFileSize != vfs.DefaultMaxFileSize || l.MaxTotalSize != vfs.DefaultMaxTotalSize || l.MaxFiles != vfs.DefaultMaxFiles {
		t.Fatalf("defaults = %+v", l)
	}
	l = vfs.Limits{MaxFileSize: 1, MaxTotalSize: -1, MaxFiles: 3}.WithDefaults()
	if l.MaxFileSize != 1 || l.MaxTotalSize != vfs.DefaultMaxTotalSize || l.MaxFiles != 3 {
		t.Fatalf("partial defaults = %+v", l)
	}
}

func TestCheckQuota(t *testing.T) {
	l := vfs.Limits{MaxFiles: 2, MaxTotalSize: 10}
	cases := []struct {
		u        vfs.Usage
		size     int64
		replaces bool
		old      int64
		quota    bool
	}{
		{vfs.Usage{Files: 1, Bytes: 5}, 5, false, 0, false},
		{vfs.Usage{Files: 2, Bytes: 5}, 1, false, 0, true}, // file count
		{vfs.Usage{Files: 2, Bytes: 5}, 1, true, 1, false}, // replacing keeps the count
		{vfs.Usage{Files: 1, Bytes: 5}, 6, false, 0, true}, // total
		{vfs.Usage{Files: 2, Bytes: 10}, 8, true, 8, false},
		{vfs.Usage{Files: 2, Bytes: 10}, 9, true, 8, true},
	}
	for i, tc := range cases {
		err := l.CheckQuota(tc.u, tc.size, tc.replaces, tc.old)
		if got := errors.Is(err, vfs.ErrQuota); got != tc.quota || (err != nil && !got) {
			t.Errorf("case %d: err = %v, want quota=%v", i, err, tc.quota)
		}
	}
	if err := (vfs.Limits{MaxFileSize: 3}).CheckFileSize(4); !errors.Is(err, vfs.ErrTooLarge) || !strings.Contains(err.Error(), "limit is 3") {
		t.Errorf("CheckFileSize err = %v", err)
	}
}

func TestParentsAndChildren(t *testing.T) {
	if got := vfs.Parents("/a/b/c"); !slices.Equal(got, []string{"/a", "/a/b"}) {
		t.Errorf("Parents = %q", got)
	}
	if got := vfs.Parents("/a"); len(got) != 0 {
		t.Errorf("Parents(/a) = %q", got)
	}
	files := []vfs.FileInfo{{Path: "/d/x"}, {Path: "/d/s/y"}, {Path: "/d/s/z"}, {Path: "/d/a"}, {Path: "/e/q"}, {Path: "/d.txt"}}
	got := vfs.Children("/d", files)
	var names []string
	for _, fi := range got {
		names = append(names, fi.Path+map[bool]string{true: "/"}[fi.IsDir])
	}
	if !slices.Equal(names, []string{"/d/a", "/d/s/", "/d/x"}) {
		t.Errorf("Children(/d) = %q", names)
	}
	if got := vfs.Children("/", files); len(got) != 3 { // /d/, /d.txt, /e/
		t.Errorf("Children(/) = %+v", got)
	}
}

func TestSplitLinesIsText(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"\n", []string{""}},
		{"a", []string{"a"}},
		{"a\n", []string{"a"}},
		{"a\nb", []string{"a", "b"}},
		{"a\r\nb\r\n", []string{"a", "b"}},
		{"a\n\nb\n\n", []string{"a", "", "b", ""}},
	}
	for _, tc := range cases {
		if got := vfs.SplitLines(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("SplitLines(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if !vfs.IsText([]byte("héllo\n")) || !vfs.IsText(nil) || vfs.IsText([]byte("a\x00b")) || vfs.IsText([]byte{0xff}) {
		t.Error("IsText misclassifies")
	}
}
