package sqlite_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store/sqlite"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store/storetest"

	_ "modernc.org/sqlite"
)

func newStore(t *testing.T) store.Store {
	t.Helper()
	s, _ := newStoreWithPrefix(t, "agent_")
	return s
}

func newStoreWithPrefix(t *testing.T, prefix string) (store.Store, *sql.DB) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "test.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := sqlite.New(db, sqlite.WithTablePrefix(prefix))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, db
}

func TestConformance(t *testing.T) {
	storetest.Run(t, newStore)
}

func TestTablePrefix(t *testing.T) {
	s, db := newStoreWithPrefix(t, "custom_")
	sess := &store.Session{Agent: "a"}
	if err := s.CreateSession(t.Context(), sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM custom_sessions`).Scan(&n); err != nil {
		t.Fatalf("query custom table: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	if _, err := s.GetSession(t.Context(), sess.ID); err != nil {
		t.Fatalf("GetSession: %v", err)
	}
}
