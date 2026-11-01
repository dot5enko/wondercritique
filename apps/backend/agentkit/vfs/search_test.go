package vfs_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs/vfstest"
)

// listSpy records List calls.
type listSpy struct {
	vfs.FS
	mu    sync.Mutex
	calls []string
}

func (s *listSpy) List(ctx context.Context, dir string, rec bool) ([]vfs.FileInfo, error) {
	s.mu.Lock()
	s.calls = append(s.calls, fmt.Sprintf("%s:%v", dir, rec))
	s.mu.Unlock()
	return s.FS.List(ctx, dir, rec)
}

func TestGlob(t *testing.T) {
	ctx := context.Background()
	fsys := seeded(t, vfstest.Fixture())
	cases := []struct {
		pattern string
		want    []string
	}{
		{"/*.txt", []string{"/a.txt", "/dir.txt"}},
		{"/dir/*", []string{"/dir/b.txt"}},
		{"/dir/**", []string{"/dir/b.txt", "/dir/sub/c.txt", "/dir/sub/deeper/d.md"}},
		{"/dir/**/*.md", []string{"/dir/sub/deeper/d.md"}},
		{"/**/*.md", []string{"/dir/sub/deeper/d.md"}},
		{"/**/c.txt", []string{"/dir/sub/c.txt"}},
		{"/{a,dir}.txt", []string{"/a.txt", "/dir.txt"}},
		{"/odd/x[_%]y/*", []string{"/odd/x%y/i.txt", "/odd/x_y/g.txt"}},
		{"/odd/100%_done.txt", []string{"/odd/100%_done.txt"}},
		{"/dir/sub/c.txt", []string{"/dir/sub/c.txt"}},
		{"//dir//sub//", nil}, // a directory never matches
		{"/dir?/*", []string{"/dir0/e.txt", "/dirx/f.txt"}},
		{"/unicodé/*", []string{"/unicodé/ñ.txt"}},
		{"/with space/*", []string{"/with space/file name"}},
		{"/missing/**", nil},
		{"/a.txt/**", nil},
		{"/", nil},
	}
	for _, tc := range cases {
		got, err := vfs.Glob(ctx, fsys, tc.pattern, 0)
		if err != nil {
			t.Errorf("Glob(%q): %v", tc.pattern, err)
			continue
		}
		if !slices.Equal(names(got), append([]string{}, tc.want...)) {
			t.Errorf("Glob(%q) = %q, want %q", tc.pattern, names(got), tc.want)
		}
	}
	all, _ := vfs.Glob(ctx, fsys, "/**", 0)
	if len(all) != len(vfstest.Fixture()) {
		t.Errorf("Glob(/**) = %d files, want %d", len(all), len(vfstest.Fixture()))
	}
	if got, _ := vfs.Glob(ctx, fsys, "/**", 3); len(got) != 3 || got[0].Path != all[0].Path {
		t.Errorf("Glob limit = %v", names(got))
	}
	for _, bad := range []string{"", "*.txt", "dir/*", "/[a", "/{a,b", `/a\`, "/a/../*"} {
		if _, err := vfs.Glob(ctx, fsys, bad, 0); !errors.Is(err, vfs.ErrBadPattern) {
			t.Errorf("Glob(%q) err = %v, want ErrBadPattern", bad, err)
		}
	}
}

func TestGlobListsFromLiteralPrefix(t *testing.T) {
	ctx := context.Background()
	spy := &listSpy{FS: seeded(t, vfstest.Fixture())}
	for _, p := range []string{"/dir/sub/*.txt", "/dir/**/*.md", "/odd/x_y/g.txt", "/*"} {
		if _, err := vfs.Glob(ctx, spy, p, 0); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"/dir/sub:false", "/dir:true", "/odd/x_y:false", "/:false"}
	if !slices.Equal(spy.calls, want) {
		t.Fatalf("List calls = %q, want %q", spy.calls, want)
	}
}

func TestGrep(t *testing.T) {
	ctx := context.Background()
	fsys := seeded(t, map[string][]byte{
		"/notes/a.md":   []byte("one\nTODO: two\nthree\nfour\nfive\nsix TODO\nseven\n"),
		"/notes/b.txt":  []byte("todo lower\r\nnothing\r\n"),
		"/notes/bin":    []byte("TODO\x00binary"),
		"/notes/latin1": []byte("TODO \xe9t\xe9"),
		"/src/main.go":  []byte("package main // TODO\n"),
	})
	matches, truncated, err := vfs.Grep(ctx, fsys, vfs.GrepOptions{Pattern: "TODO"})
	if err != nil || truncated {
		t.Fatalf("Grep = %v, %v", truncated, err)
	}
	var got []string
	for _, m := range matches {
		got = append(got, fmt.Sprintf("%s:%d:%s", m.Path, m.Line, m.Text))
	}
	want := []string{"/notes/a.md:2:TODO: two", "/notes/a.md:6:six TODO", "/src/main.go:1:package main // TODO"}
	if !slices.Equal(got, want) {
		t.Fatalf("Grep = %q, want %q (binary and non-UTF-8 files skipped)", got, want)
	}

	matches, _, _ = vfs.Grep(ctx, fsys, vfs.GrepOptions{Pattern: "todo", IgnoreCase: true, Glob: "/notes/*.txt"})
	if len(matches) != 1 || matches[0].Text != "todo lower" || matches[0].Line != 1 {
		t.Fatalf("ignore-case + glob = %+v", matches)
	}

	matches, _, _ = vfs.Grep(ctx, fsys, vfs.GrepOptions{Pattern: "^TODO|TODO$", Glob: "/notes/a.md", Context: 2})
	if len(matches) != 2 {
		t.Fatalf("context matches = %+v", matches)
	}
	if m := matches[0]; !slices.Equal(m.Before, []string{"one"}) || !slices.Equal(m.After, []string{"three", "four"}) {
		t.Fatalf("context of first match = %+v", m)
	}
	if m := matches[1]; !slices.Equal(m.Before, []string{"four", "five"}) || !slices.Equal(m.After, []string{"seven"}) {
		t.Fatalf("context of second match = %+v", m)
	}
	matches, _, _ = vfs.Grep(ctx, fsys, vfs.GrepOptions{Pattern: "six", Glob: "/notes/a.md", Context: 99})
	if len(matches) != 1 || len(matches[0].Before) != vfs.MaxGrepContext {
		t.Fatalf("context not clamped: %+v", matches)
	}

	matches, truncated, err = vfs.Grep(ctx, fsys, vfs.GrepOptions{Pattern: "TODO", MaxMatches: 2})
	if err != nil || !truncated || len(matches) != 2 {
		t.Fatalf("MaxMatches: %d matches, truncated=%v, %v", len(matches), truncated, err)
	}
	if _, truncated, _ = vfs.Grep(ctx, fsys, vfs.GrepOptions{Pattern: "TODO", MaxMatches: 3}); truncated {
		t.Fatal("exactly MaxMatches matches reported as truncated")
	}

	for _, opts := range []vfs.GrepOptions{{Pattern: ""}, {Pattern: "("}, {Pattern: "x", Glob: "rel"}} {
		if _, _, err := vfs.Grep(ctx, fsys, opts); !errors.Is(err, vfs.ErrBadPattern) {
			t.Errorf("Grep(%+v) err = %v, want ErrBadPattern", opts, err)
		}
	}
	if m, _, err := vfs.Grep(ctx, fsys, vfs.GrepOptions{Pattern: "x", Glob: "/nothing/**"}); err != nil || len(m) != 0 {
		t.Errorf("Grep on no files = %v, %v", m, err)
	}

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := vfs.Grep(cctx, fsys, vfs.GrepOptions{Pattern: "x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled Grep err = %v", err)
	}
}
