// Package vfstest is a conformance suite for vfs.FS implementations.
package vfstest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
)

// Run executes the conformance suite for a writable file system. newFS must return a fresh,
// empty file system enforcing limits (zero fields mean the vfs defaults) for each call.
func Run(t *testing.T, newFS func(t *testing.T, limits vfs.Limits) vfs.FS) {
	tests := []struct {
		name string
		fn   func(*testing.T, func(vfs.Limits) vfs.FS)
	}{
		{"InvalidPaths", testInvalidPaths},
		{"Root", testRoot},
		{"WriteReadStat", testWriteReadStat},
		{"BuffersAreCopied", testBuffersAreCopied},
		{"Missing", testMissing},
		{"Delete", testDelete},
		{"ImplicitDirs", testImplicitDirs},
		{"FileDirConflicts", testFileDirConflicts},
		{"List", testList},
		{"MaxFileSize", testMaxFileSize},
		{"MaxFiles", testMaxFiles},
		{"MaxTotalSize", testMaxTotalSize},
		{"DefaultLimits", testDefaultLimits},
		{"ConcurrentWriters", testConcurrentWriters},
		{"ConcurrentQuota", testConcurrentQuota},
		{"ContextCanceled", testContextCanceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, func(l vfs.Limits) vfs.FS { return newFS(t, l) })
		})
	}
}

// RunReadOnly executes the conformance suite for a read-only file system. newFS must return a
// read-only file system containing exactly files (path → content).
func RunReadOnly(t *testing.T, newFS func(t *testing.T, files map[string][]byte) vfs.FS) {
	t.Run("Reads", func(t *testing.T) {
		checkFixture(t, newFS(t, Fixture()))
	})
	t.Run("WritesRejected", func(t *testing.T) {
		ctx := context.Background()
		fsys := newFS(t, Fixture())
		before := snapshot(t, fsys)
		for _, p := range []string{"/a.txt", "/new.txt", "/dir/new/x.txt", "/dir/b.txt/x"} {
			if err := fsys.Write(ctx, p, []byte("x")); !errors.Is(err, vfs.ErrReadOnly) {
				t.Errorf("Write(%s) err = %v, want ErrReadOnly", p, err)
			}
		}
		for _, p := range []string{"/a.txt", "/missing.txt"} {
			if err := fsys.Delete(ctx, p); !errors.Is(err, vfs.ErrReadOnly) {
				t.Errorf("Delete(%s) err = %v, want ErrReadOnly", p, err)
			}
		}
		// Directories may report either error.
		for _, p := range []string{"/", "/dir"} {
			if err := fsys.Write(ctx, p, nil); !errors.Is(err, vfs.ErrReadOnly) && !errors.Is(err, vfs.ErrIsDir) {
				t.Errorf("Write(%s) err = %v, want ErrReadOnly or ErrIsDir", p, err)
			}
			if err := fsys.Delete(ctx, p); !errors.Is(err, vfs.ErrReadOnly) && !errors.Is(err, vfs.ErrIsDir) {
				t.Errorf("Delete(%s) err = %v, want ErrReadOnly or ErrIsDir", p, err)
			}
		}
		for _, p := range invalidPaths() {
			if err := fsys.Write(ctx, p, nil); !errors.Is(err, vfs.ErrInvalidPath) {
				t.Errorf("Write(%q) err = %v, want ErrInvalidPath", p, err)
			}
			if err := fsys.Delete(ctx, p); !errors.Is(err, vfs.ErrInvalidPath) {
				t.Errorf("Delete(%q) err = %v, want ErrInvalidPath", p, err)
			}
		}
		if after := snapshot(t, fsys); !slices.Equal(before, after) {
			t.Errorf("read-only FS changed:\nbefore %v\nafter  %v", before, after)
		}
	})
}

// Fixture returns the files used by the listing checks: nested directories, names that are
// prefixes of each other, LIKE wildcards and non-ASCII characters.
func Fixture() map[string][]byte {
	return map[string][]byte{
		"/a.txt":                []byte("alpha\n"),
		"/dir/b.txt":            []byte("bravo"),
		"/dir/sub/c.txt":        []byte("charlie\n"),
		"/dir/sub/deeper/d.md":  []byte("# delta\n"),
		"/dir.txt":              []byte("dir-file"),
		"/dir0/e.txt":           []byte("echo"),
		"/dirx/f.txt":           []byte(""),
		"/odd/100%_done.txt":    []byte("percent"),
		"/odd/x_y/g.txt":        []byte("underscore"),
		"/odd/xay/h.txt":        []byte("wildcard twin"),
		"/odd/x%y/i.txt":        []byte("percent dir"),
		"/unicodé/ñ.txt":        []byte("ñ"),
		"/with space/file name": []byte("spaces"),
	}
}

func ctxb() context.Context { return context.Background() }

func mustWrite(t *testing.T, fsys vfs.FS, p string, data []byte) {
	t.Helper()
	if err := fsys.Write(ctxb(), p, data); err != nil {
		t.Fatalf("Write(%s): %v", p, err)
	}
}

func mustStat(t *testing.T, fsys vfs.FS, p string) vfs.FileInfo {
	t.Helper()
	fi, err := fsys.Stat(ctxb(), p)
	if err != nil {
		t.Fatalf("Stat(%s): %v", p, err)
	}
	return fi
}

func writeFixture(t *testing.T, fsys vfs.FS) {
	t.Helper()
	for p, data := range Fixture() {
		mustWrite(t, fsys, p, data)
	}
}

// snapshot returns a comparable description of every file (path, size, version, digest).
func snapshot(t *testing.T, fsys vfs.FS) []string {
	t.Helper()
	infos, err := fsys.List(ctxb(), "/", true)
	if err != nil {
		t.Fatalf("List(/, true): %v", err)
	}
	out := make([]string, 0, len(infos))
	for _, fi := range infos {
		out = append(out, fmt.Sprintf("%s:%d:%d:%s", fi.Path, fi.Size, fi.Version, fi.SHA256))
	}
	return out
}

func paths(infos []vfs.FileInfo) []string {
	out := make([]string, 0, len(infos))
	for _, fi := range infos {
		p := fi.Path
		if fi.IsDir {
			p += "/"
		}
		out = append(out, p)
	}
	return out
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func invalidPaths() []string {
	return []string{
		"",
		"relative",
		"a/b",
		"/a/../b",
		"/a/./b",
		"/..",
		`/a\b`,
		"/a\x00b",
		"/a\nb",
		"/a\x7fb",
		"/bad\xffutf8",
		"/" + strings.Repeat("a", vfs.MaxSegmentLen+1),
		strings.Repeat("/abcdefg", vfs.MaxPathLen/8+1),
	}
}

func testInvalidPaths(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{})
	mustWrite(t, fsys, "/ok.txt", []byte("ok"))
	ctx := ctxb()
	for _, p := range invalidPaths() {
		if _, err := fsys.Stat(ctx, p); !errors.Is(err, vfs.ErrInvalidPath) {
			t.Errorf("Stat(%q) err = %v, want ErrInvalidPath", p, err)
		}
		if _, err := fsys.Read(ctx, p); !errors.Is(err, vfs.ErrInvalidPath) {
			t.Errorf("Read(%q) err = %v, want ErrInvalidPath", p, err)
		}
		if err := fsys.Write(ctx, p, []byte("x")); !errors.Is(err, vfs.ErrInvalidPath) {
			t.Errorf("Write(%q) err = %v, want ErrInvalidPath", p, err)
		}
		if err := fsys.Delete(ctx, p); !errors.Is(err, vfs.ErrInvalidPath) {
			t.Errorf("Delete(%q) err = %v, want ErrInvalidPath", p, err)
		}
		if _, err := fsys.List(ctx, p, false); !errors.Is(err, vfs.ErrInvalidPath) {
			t.Errorf("List(%q) err = %v, want ErrInvalidPath", p, err)
		}
	}
	if got := snapshot(t, fsys); len(got) != 1 {
		t.Fatalf("invalid writes changed the FS: %v", got)
	}
	// Messy but valid paths are normalized.
	mustWrite(t, fsys, "//messy///path/", []byte("m"))
	if fi := mustStat(t, fsys, "/messy/path"); fi.Path != "/messy/path" {
		t.Fatalf("Stat path = %q, want /messy/path", fi.Path)
	}
	if data, err := fsys.Read(ctx, "/messy//path//"); err != nil || string(data) != "m" {
		t.Fatalf("Read(messy) = %q, %v", data, err)
	}
	if fi := mustStat(t, fsys, "/messy/"); !fi.IsDir || fi.Path != "/messy" {
		t.Fatalf("Stat(/messy/) = %+v", fi)
	}
}

func testRoot(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{})
	ctx := ctxb()
	if fi, err := fsys.Stat(ctx, "/"); err != nil || !fi.IsDir || fi.Path != "/" {
		t.Fatalf("Stat(/) = %+v, %v", fi, err)
	}
	if got, err := fsys.List(ctx, "/", false); err != nil || len(got) != 0 {
		t.Fatalf("List(/) on empty FS = %v, %v", got, err)
	}
	if got, err := fsys.List(ctx, "/", true); err != nil || len(got) != 0 {
		t.Fatalf("List(/, true) on empty FS = %v, %v", got, err)
	}
	if _, err := fsys.Read(ctx, "/"); !errors.Is(err, vfs.ErrIsDir) {
		t.Fatalf("Read(/) err = %v, want ErrIsDir", err)
	}
	if err := fsys.Write(ctx, "/", []byte("x")); !errors.Is(err, vfs.ErrIsDir) {
		t.Fatalf("Write(/) err = %v, want ErrIsDir", err)
	}
	if err := fsys.Delete(ctx, "/"); !errors.Is(err, vfs.ErrIsDir) {
		t.Fatalf("Delete(/) err = %v, want ErrIsDir", err)
	}
}

func testWriteReadStat(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{})
	ctx := ctxb()
	start := time.Now().Add(-time.Second)
	mustWrite(t, fsys, "/notes/a.md", []byte("hello"))
	data, err := fsys.Read(ctx, "/notes/a.md")
	if err != nil || string(data) != "hello" {
		t.Fatalf("Read = %q, %v", data, err)
	}
	fi := mustStat(t, fsys, "/notes/a.md")
	if fi.Path != "/notes/a.md" || fi.IsDir || fi.Size != 5 || fi.Version != 1 || fi.SHA256 != digest([]byte("hello")) {
		t.Fatalf("Stat = %+v", fi)
	}
	if fi.ModTime.Before(start) || fi.ModTime.After(time.Now().Add(time.Second)) {
		t.Fatalf("ModTime = %v, want about now", fi.ModTime)
	}

	mustWrite(t, fsys, "/notes/a.md", []byte("hello, world"))
	fi2 := mustStat(t, fsys, "/notes/a.md")
	if fi2.Version != 2 || fi2.Size != 12 || fi2.SHA256 != digest([]byte("hello, world")) {
		t.Fatalf("Stat after replace = %+v", fi2)
	}
	if fi2.ModTime.Before(fi.ModTime) {
		t.Fatalf("ModTime went backwards: %v < %v", fi2.ModTime, fi.ModTime)
	}
	mustWrite(t, fsys, "/notes/a.md", []byte("hello, world")) // same content still bumps the version
	if v := mustStat(t, fsys, "/notes/a.md").Version; v != 3 {
		t.Fatalf("Version after identical write = %d, want 3", v)
	}

	mustWrite(t, fsys, "/empty", nil)
	if data, err := fsys.Read(ctx, "/empty"); err != nil || len(data) != 0 {
		t.Fatalf("Read(empty) = %q, %v", data, err)
	}
	if fi := mustStat(t, fsys, "/empty"); fi.Size != 0 || fi.Version != 1 || fi.SHA256 != digest(nil) {
		t.Fatalf("Stat(empty) = %+v", fi)
	}

	bin := []byte{0, 1, 2, 0xff, 0xfe, '\n', 0}
	mustWrite(t, fsys, "/bin/blob", bin)
	if data, err := fsys.Read(ctx, "/bin/blob"); err != nil || !bytes.Equal(data, bin) {
		t.Fatalf("Read(binary) = %v, %v", data, err)
	}
}

func testBuffersAreCopied(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{})
	buf := []byte("original")
	mustWrite(t, fsys, "/f", buf)
	copy(buf, "MUTATED!")
	data, err := fsys.Read(ctxb(), "/f")
	if err != nil || string(data) != "original" {
		t.Fatalf("Read after mutating the written buffer = %q, %v", data, err)
	}
	copy(data, "MUTATED!")
	again, _ := fsys.Read(ctxb(), "/f")
	if string(again) != "original" {
		t.Fatalf("Read after mutating a read buffer = %q", again)
	}
}

func testMissing(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{})
	mustWrite(t, fsys, "/dir/file.txt", []byte("x"))
	ctx := ctxb()
	for _, p := range []string{"/nope", "/dir/nope", "/nope/deeper", "/dir/file.txt/below", "/dir/file"} {
		if _, err := fsys.Stat(ctx, p); !errors.Is(err, vfs.ErrNotFound) {
			t.Errorf("Stat(%s) err = %v, want ErrNotFound", p, err)
		}
		if _, err := fsys.Read(ctx, p); !errors.Is(err, vfs.ErrNotFound) {
			t.Errorf("Read(%s) err = %v, want ErrNotFound", p, err)
		}
		if err := fsys.Delete(ctx, p); !errors.Is(err, vfs.ErrNotFound) {
			t.Errorf("Delete(%s) err = %v, want ErrNotFound", p, err)
		}
		if _, err := fsys.List(ctx, p, false); !errors.Is(err, vfs.ErrNotFound) {
			t.Errorf("List(%s) err = %v, want ErrNotFound", p, err)
		}
		if _, err := fsys.List(ctx, p, true); !errors.Is(err, vfs.ErrNotFound) {
			t.Errorf("List(%s, true) err = %v, want ErrNotFound", p, err)
		}
	}
}

func testDelete(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{})
	ctx := ctxb()
	mustWrite(t, fsys, "/x.txt", []byte("one"))
	mustWrite(t, fsys, "/x.txt", []byte("two"))
	mustWrite(t, fsys, "/y.txt", []byte("keep"))
	if err := fsys.Delete(ctx, "/x.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := fsys.Read(ctx, "/x.txt"); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("Read after Delete err = %v, want ErrNotFound", err)
	}
	if err := fsys.Delete(ctx, "/x.txt"); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("second Delete err = %v, want ErrNotFound", err)
	}
	mustWrite(t, fsys, "/x.txt", []byte("three"))
	if fi := mustStat(t, fsys, "/x.txt"); fi.Version != 1 {
		t.Fatalf("recreated file Version = %d, want 1", fi.Version)
	}
	if data, _ := fsys.Read(ctx, "/y.txt"); string(data) != "keep" {
		t.Fatalf("unrelated file changed: %q", data)
	}
}

func testImplicitDirs(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{})
	ctx := ctxb()
	mustWrite(t, fsys, "/d/e/f.txt", []byte("f"))
	for _, p := range []string{"/d", "/d/e"} {
		fi := mustStat(t, fsys, p)
		if !fi.IsDir || fi.Path != p || fi.Size != 0 || fi.Version != 0 || fi.SHA256 != "" || !fi.ModTime.IsZero() {
			t.Fatalf("Stat(%s) = %+v, want a bare directory entry", p, fi)
		}
	}
	got, err := fsys.List(ctx, "/", false)
	if err != nil || !slices.Equal(paths(got), []string{"/d/"}) {
		t.Fatalf("List(/) = %v, %v", paths(got), err)
	}
	got, err = fsys.List(ctx, "/d", false)
	if err != nil || !slices.Equal(paths(got), []string{"/d/e/"}) {
		t.Fatalf("List(/d) = %v, %v", paths(got), err)
	}
	mustWrite(t, fsys, "/d/g.txt", []byte("g"))
	if err := fsys.Delete(ctx, "/d/e/f.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Stat(ctx, "/d/e"); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("Stat(/d/e) after its last file was deleted err = %v, want ErrNotFound", err)
	}
	if _, err := fsys.List(ctx, "/d/e", false); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("List(/d/e) after its last file was deleted err = %v, want ErrNotFound", err)
	}
	if fi := mustStat(t, fsys, "/d"); !fi.IsDir {
		t.Fatalf("Stat(/d) = %+v, want dir (still holds g.txt)", fi)
	}
	if err := fsys.Delete(ctx, "/d/g.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Stat(ctx, "/d"); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("Stat(/d) after emptying err = %v, want ErrNotFound", err)
	}
	if got, err := fsys.List(ctx, "/", false); err != nil || len(got) != 0 {
		t.Fatalf("List(/) after emptying = %v, %v", paths(got), err)
	}
	// A former directory path can become a file.
	mustWrite(t, fsys, "/d", []byte("now a file"))
	if fi := mustStat(t, fsys, "/d"); fi.IsDir || fi.Version != 1 {
		t.Fatalf("Stat(/d) = %+v, want file", fi)
	}
}

func testFileDirConflicts(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{})
	ctx := ctxb()
	mustWrite(t, fsys, "/d/e/f.txt", []byte("f"))
	before := snapshot(t, fsys)
	for _, p := range []string{"/d", "/d/e"} {
		if err := fsys.Write(ctx, p, []byte("x")); !errors.Is(err, vfs.ErrIsDir) {
			t.Errorf("Write(%s) err = %v, want ErrIsDir", p, err)
		}
		if _, err := fsys.Read(ctx, p); !errors.Is(err, vfs.ErrIsDir) {
			t.Errorf("Read(%s) err = %v, want ErrIsDir", p, err)
		}
		if err := fsys.Delete(ctx, p); !errors.Is(err, vfs.ErrIsDir) {
			t.Errorf("Delete(%s) err = %v, want ErrIsDir", p, err)
		}
	}
	for _, p := range []string{"/d/e/f.txt/g", "/d/e/f.txt/g/h"} {
		if err := fsys.Write(ctx, p, []byte("x")); !errors.Is(err, vfs.ErrNotDir) {
			t.Errorf("Write(%s) err = %v, want ErrNotDir", p, err)
		}
	}
	for _, rec := range []bool{false, true} {
		if _, err := fsys.List(ctx, "/d/e/f.txt", rec); !errors.Is(err, vfs.ErrNotDir) {
			t.Errorf("List(file, %v) err = %v, want ErrNotDir", rec, err)
		}
	}
	if after := snapshot(t, fsys); !slices.Equal(before, after) {
		t.Fatalf("failed writes changed the FS:\nbefore %v\nafter  %v", before, after)
	}
	if got, _ := fsys.List(ctx, "/d/e", false); !slices.Equal(paths(got), []string{"/d/e/f.txt"}) {
		t.Fatalf("List(/d/e) = %v", paths(got))
	}
}

func testList(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{})
	writeFixture(t, fsys)
	checkFixture(t, fsys)
}

// checkFixture verifies Stat, Read and List against a file system holding exactly Fixture().
func checkFixture(t *testing.T, fsys vfs.FS) {
	t.Helper()
	ctx := ctxb()
	files := Fixture()
	for p, want := range files {
		data, err := fsys.Read(ctx, p)
		if err != nil || !bytes.Equal(data, want) {
			t.Errorf("Read(%s) = %q, %v; want %q", p, data, err, want)
		}
		fi, err := fsys.Stat(ctx, p)
		if err != nil || fi.Path != p || fi.IsDir || fi.Size != int64(len(want)) || fi.SHA256 != digest(want) || fi.Version < 1 || fi.ModTime.IsZero() {
			t.Errorf("Stat(%s) = %+v, %v", p, fi, err)
		}
	}
	cases := []struct {
		dir       string
		recursive bool
		want      []string
	}{
		{"/", false, []string{"/a.txt", "/dir/", "/dir.txt", "/dir0/", "/dirx/", "/odd/", "/unicodé/", "/with space/"}},
		{"/dir", false, []string{"/dir/b.txt", "/dir/sub/"}},
		{"/dir/sub", false, []string{"/dir/sub/c.txt", "/dir/sub/deeper/"}},
		{"/dir", true, []string{"/dir/b.txt", "/dir/sub/c.txt", "/dir/sub/deeper/d.md"}},
		{"/dir/", true, []string{"/dir/b.txt", "/dir/sub/c.txt", "/dir/sub/deeper/d.md"}},
		{"/odd", false, []string{"/odd/100%_done.txt", "/odd/x%y/", "/odd/x_y/", "/odd/xay/"}},
		{"/odd/x_y", true, []string{"/odd/x_y/g.txt"}},
		{"/odd/x%y", false, []string{"/odd/x%y/i.txt"}},
		{"/unicodé", false, []string{"/unicodé/ñ.txt"}},
		{"/with space", true, []string{"/with space/file name"}},
	}
	all := make([]string, 0, len(files))
	for p := range files {
		all = append(all, p)
	}
	slices.Sort(all)
	cases = append(cases, struct {
		dir       string
		recursive bool
		want      []string
	}{"/", true, all})
	for _, tc := range cases {
		got, err := fsys.List(ctx, tc.dir, tc.recursive)
		if err != nil {
			t.Errorf("List(%s, %v): %v", tc.dir, tc.recursive, err)
			continue
		}
		if !slices.Equal(paths(got), tc.want) {
			t.Errorf("List(%s, %v) =\n  %q\nwant\n  %q", tc.dir, tc.recursive, paths(got), tc.want)
		}
		for _, fi := range got {
			if !fi.IsDir && (fi.Size != int64(len(files[fi.Path])) || fi.Version < 1 || fi.SHA256 == "") {
				t.Errorf("List(%s) entry %+v lacks file metadata", tc.dir, fi)
			}
		}
	}
	for _, p := range []string{"/di", "/odd/x", "/odd/x_", "/dir/sub/deeper/d"} {
		if _, err := fsys.List(ctx, p, false); !errors.Is(err, vfs.ErrNotFound) {
			t.Errorf("List(%s) err = %v, want ErrNotFound", p, err)
		}
		if _, err := fsys.Stat(ctx, p); !errors.Is(err, vfs.ErrNotFound) {
			t.Errorf("Stat(%s) err = %v, want ErrNotFound", p, err)
		}
	}
	if fi, err := fsys.Stat(ctx, "/odd/x_y"); err != nil || !fi.IsDir {
		t.Errorf("Stat(/odd/x_y) = %+v, %v", fi, err)
	}
}

func testMaxFileSize(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{MaxFileSize: 10})
	ctx := ctxb()
	mustWrite(t, fsys, "/ok", bytes.Repeat([]byte("a"), 10))
	before := snapshot(t, fsys)
	if err := fsys.Write(ctx, "/big", bytes.Repeat([]byte("a"), 11)); !errors.Is(err, vfs.ErrTooLarge) {
		t.Fatalf("Write(11 bytes) err = %v, want ErrTooLarge", err)
	}
	if err := fsys.Write(ctx, "/ok", bytes.Repeat([]byte("b"), 11)); !errors.Is(err, vfs.ErrTooLarge) {
		t.Fatalf("replace with 11 bytes err = %v, want ErrTooLarge", err)
	}
	if after := snapshot(t, fsys); !slices.Equal(before, after) {
		t.Fatalf("rejected writes changed the FS:\nbefore %v\nafter  %v", before, after)
	}
	if data, _ := fsys.Read(ctx, "/ok"); string(data) != "aaaaaaaaaa" {
		t.Fatalf("content after rejected replace = %q", data)
	}
}

func testMaxFiles(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{MaxFiles: 2})
	ctx := ctxb()
	mustWrite(t, fsys, "/1", []byte("1"))
	mustWrite(t, fsys, "/sub/2", []byte("2"))
	before := snapshot(t, fsys)
	if err := fsys.Write(ctx, "/3", []byte("3")); !errors.Is(err, vfs.ErrQuota) {
		t.Fatalf("third file err = %v, want ErrQuota", err)
	}
	if err := fsys.Write(ctx, "/other/3", []byte("3")); !errors.Is(err, vfs.ErrQuota) {
		t.Fatalf("third file in a new dir err = %v, want ErrQuota", err)
	}
	if after := snapshot(t, fsys); !slices.Equal(before, after) {
		t.Fatalf("rejected writes changed the FS:\nbefore %v\nafter  %v", before, after)
	}
	if _, err := fsys.Stat(ctx, "/other"); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("rejected write left an implicit dir: %v", err)
	}
	mustWrite(t, fsys, "/1", []byte("replaced")) // replacing does not add a file
	if err := fsys.Delete(ctx, "/1"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, fsys, "/3", []byte("3"))
}

func testMaxTotalSize(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{MaxTotalSize: 10, MaxFileSize: 10})
	ctx := ctxb()
	mustWrite(t, fsys, "/six", []byte("666666"))
	mustWrite(t, fsys, "/four", []byte("4444")) // total 10
	before := snapshot(t, fsys)
	if err := fsys.Write(ctx, "/one", []byte("1")); !errors.Is(err, vfs.ErrQuota) {
		t.Fatalf("write past total err = %v, want ErrQuota", err)
	}
	if err := fsys.Write(ctx, "/six", []byte("7777777")); !errors.Is(err, vfs.ErrQuota) {
		t.Fatalf("growing replace err = %v, want ErrQuota", err)
	}
	if after := snapshot(t, fsys); !slices.Equal(before, after) {
		t.Fatalf("rejected writes changed the FS:\nbefore %v\nafter  %v", before, after)
	}
	mustWrite(t, fsys, "/six", []byte("666666")) // same size: delta 0
	mustWrite(t, fsys, "/six", []byte("55555"))  // shrink: total 9
	mustWrite(t, fsys, "/one", []byte("1"))      // total 10
	if err := fsys.Delete(ctx, "/four"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, fsys, "/four2", []byte("4444")) // freed space is reusable
}

func testDefaultLimits(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{})
	mustWrite(t, fsys, "/max", make([]byte, vfs.DefaultMaxFileSize))
	if err := fsys.Write(ctxb(), "/over", make([]byte, vfs.DefaultMaxFileSize+1)); !errors.Is(err, vfs.ErrTooLarge) {
		t.Fatalf("default MaxFileSize not enforced: %v", err)
	}
}

func testConcurrentWriters(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{})
	ctx := ctxb()
	const workers, writes = 8, 10
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range writes {
				if err := fsys.Write(ctx, fmt.Sprintf("/w%d/f%d", w, i), []byte("x")); err != nil {
					t.Errorf("Write: %v", err)
				}
				if err := fsys.Write(ctx, "/shared", fmt.Appendf(nil, "%d-%d", w, i)); err != nil {
					t.Errorf("Write(shared): %v", err)
				}
				if _, err := fsys.List(ctx, "/", true); err != nil {
					t.Errorf("List: %v", err)
				}
				if _, err := fsys.Read(ctx, "/shared"); err != nil {
					t.Errorf("Read(shared): %v", err)
				}
			}
		})
	}
	wg.Wait()
	if fi := mustStat(t, fsys, "/shared"); fi.Version != workers*writes {
		t.Fatalf("shared Version = %d, want %d (lost updates)", fi.Version, workers*writes)
	}
	all, err := fsys.List(ctx, "/", true)
	if err != nil || len(all) != workers*writes+1 {
		t.Fatalf("List(/, true) = %d entries, %v; want %d", len(all), err, workers*writes+1)
	}
}

func testConcurrentQuota(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{MaxFiles: 10})
	var ok, quota atomic.Int32
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			err := fsys.Write(ctxb(), fmt.Sprintf("/f%02d", i), []byte("x"))
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, vfs.ErrQuota):
				quota.Add(1)
			default:
				t.Errorf("Write: %v", err)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 10 || quota.Load() != 30 {
		t.Fatalf("succeeded %d, quota errors %d; want 10 and 30", ok.Load(), quota.Load())
	}
	if all, _ := fsys.List(ctxb(), "/", true); len(all) != 10 {
		t.Fatalf("files = %d, want 10", len(all))
	}
}

func testContextCanceled(t *testing.T, newFS func(vfs.Limits) vfs.FS) {
	fsys := newFS(vfs.Limits{})
	mustWrite(t, fsys, "/f", []byte("x"))
	ctx, cancel := context.WithCancel(ctxb())
	cancel()
	if err := fsys.Write(ctx, "/g", []byte("y")); !errors.Is(err, context.Canceled) {
		t.Errorf("Write err = %v, want context.Canceled", err)
	}
	if _, err := fsys.Stat(ctxb(), "/g"); !errors.Is(err, vfs.ErrNotFound) {
		t.Errorf("canceled Write created the file: %v", err)
	}
	if err := fsys.Delete(ctx, "/f"); !errors.Is(err, context.Canceled) {
		t.Errorf("Delete err = %v, want context.Canceled", err)
	}
	if _, err := fsys.Read(ctx, "/f"); !errors.Is(err, context.Canceled) {
		t.Errorf("Read err = %v, want context.Canceled", err)
	}
	if _, err := fsys.Stat(ctx, "/f"); !errors.Is(err, context.Canceled) {
		t.Errorf("Stat err = %v, want context.Canceled", err)
	}
	if _, err := fsys.List(ctx, "/", true); !errors.Is(err, context.Canceled) {
		t.Errorf("List err = %v, want context.Canceled", err)
	}
	if data, err := fsys.Read(ctxb(), "/f"); err != nil || string(data) != "x" {
		t.Errorf("file after canceled ops = %q, %v", data, err)
	}
}
