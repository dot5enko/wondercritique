package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs/sqlite"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs/vfstest"

	_ "modernc.org/sqlite"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "vfs.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newStore(t *testing.T, db *sql.DB, opts ...sqlite.Option) *sqlite.Store {
	t.Helper()
	s, err := sqlite.New(db, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestConformance(t *testing.T) {
	vfstest.Run(t, func(t *testing.T, l vfs.Limits) vfs.FS {
		return newStore(t, openDB(t)).Workspace("ws", l)
	})
}

// Several workspaces share one table: each must behave as an isolated FS.
func TestConformanceSharedTable(t *testing.T) {
	db := openDB(t)
	s := newStore(t, db)
	noise := s.Workspace("noise", vfs.Limits{})
	for p, data := range vfstest.Fixture() {
		if err := noise.Write(context.Background(), p, data); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	vfstest.Run(t, func(t *testing.T, l vfs.Limits) vfs.FS {
		n++
		return s.Workspace(fmt.Sprintf("ws-%d", n), l)
	})
}

func TestReadOnlyConformance(t *testing.T) {
	vfstest.RunReadOnly(t, func(t *testing.T, files map[string][]byte) vfs.FS {
		ws := newStore(t, openDB(t)).Workspace("ro", vfs.Limits{})
		for p, data := range files {
			if err := ws.Write(context.Background(), p, data); err != nil {
				t.Fatal(err)
			}
		}
		return vfs.ReadOnly(ws)
	})
}

func TestSchemaIdempotentAndPrefix(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	newStore(t, db, sqlite.WithTablePrefix("custom_"))
	s := newStore(t, db, sqlite.WithTablePrefix("custom_")) // second migration is a no-op
	if err := s.Workspace("w", vfs.Limits{}).Write(ctx, "/f", []byte("x")); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM custom_files WHERE workspace_id = 'w'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("custom_files rows = %d, %v", n, err)
	}
	if _, err := sqlite.New(nil); err == nil {
		t.Fatal("New(nil) succeeded")
	}
}

func TestListUsesIndex(t *testing.T) {
	db := openDB(t)
	newStore(t, db)
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT path, size FROM vfs_files
		WHERE workspace_id = ? AND path >= ? AND path < ? ORDER BY path`, "w", "/a/", "/a0")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, "; ")
	if !strings.Contains(joined, "SEARCH") || !strings.Contains(joined, "INDEX") || !strings.Contains(joined, "path>") {
		t.Fatalf("prefix listing does not use the primary key index: %s", joined)
	}
	if strings.Contains(joined, "TEMP B-TREE") {
		t.Fatalf("prefix listing sorts in a temp b-tree: %s", joined)
	}
}

func TestWorkspaceIsolationAndLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, openDB(t))
	a := s.Workspace("a", vfs.Limits{MaxFiles: 2})
	b := s.Workspace("b", vfs.Limits{MaxFiles: 2})
	for _, p := range []string{"/x", "/dir/y"} {
		if err := a.Write(ctx, p, []byte("a"+p)); err != nil {
			t.Fatal(err)
		}
	}
	// Quotas are per workspace.
	if err := b.Write(ctx, "/x", []byte("b")); err != nil {
		t.Fatalf("b write: %v", err)
	}
	if data, _ := a.Read(ctx, "/x"); string(data) != "a/x" {
		t.Fatalf("a sees %q", data)
	}

	if err := s.CopyWorkspace(ctx, "a", "b"); !errors.Is(err, sqlite.ErrWorkspaceNotEmpty) {
		t.Fatalf("copy onto non-empty err = %v", err)
	}
	if err := s.CopyWorkspace(ctx, "a", "a"); err == nil {
		t.Fatal("copy onto itself succeeded")
	}
	if err := a.Write(ctx, "/x", []byte("a/x v2")); err != nil {
		t.Fatal(err)
	}
	if err := s.CopyWorkspace(ctx, "a", "c"); err != nil {
		t.Fatalf("CopyWorkspace: %v", err)
	}
	c := s.Workspace("c", vfs.Limits{})
	src, _ := a.Stat(ctx, "/x")
	dst, err := c.Stat(ctx, "/x")
	if err != nil || dst != src {
		t.Fatalf("copied Stat = %+v, %v; want %+v", dst, err, src)
	}
	if data, _ := c.Read(ctx, "/dir/y"); string(data) != "a/dir/y" {
		t.Fatalf("copied content = %q", data)
	}
	if err := c.Write(ctx, "/x", []byte("changed in c")); err != nil {
		t.Fatal(err)
	}
	if data, _ := a.Read(ctx, "/x"); string(data) != "a/x v2" {
		t.Fatalf("copy is not independent: a sees %q", data)
	}
	if err := s.CopyWorkspace(ctx, "empty", "d"); err != nil {
		t.Fatalf("copy of empty workspace: %v", err)
	}

	if err := s.DeleteWorkspace(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.List(ctx, "/", true); err != nil || len(got) != 0 {
		t.Fatalf("a after DeleteWorkspace = %v, %v", got, err)
	}
	if got, _ := c.List(ctx, "/", true); len(got) != 2 {
		t.Fatalf("DeleteWorkspace touched c: %v", got)
	}
	if err := s.DeleteWorkspace(ctx, "never-existed"); err != nil {
		t.Fatalf("DeleteWorkspace(missing): %v", err)
	}
}
