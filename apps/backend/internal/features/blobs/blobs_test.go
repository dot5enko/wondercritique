package blobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := dbx.Open(":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbx.Close(db) })
	if err := db.AutoMigrate(&Blob{}); err != nil {
		t.Fatal(err)
	}
	return NewStore(db)
}

func TestPutGetRoundtrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	data := []byte("PNG-ish payload \x00\x01\x02")

	sha, err := s.Put(ctx, data, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)
	if sha != hex.EncodeToString(want[:]) {
		t.Fatalf("sha = %s, want %s", sha, hex.EncodeToString(want[:]))
	}

	b, err := s.Get(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}
	if string(b.Data) != string(data) {
		t.Fatal("payload mismatch")
	}
	if b.MIME != "image/png" || b.Size != int64(len(data)) {
		t.Fatalf("meta: mime=%q size=%d", b.MIME, b.Size)
	}
	if b.CreatedAt.IsZero() {
		t.Error("created_at not set")
	}

	exists, err := s.Exists(ctx, sha)
	if err != nil || !exists {
		t.Fatalf("exists = %v, %v", exists, err)
	}
}

func TestPutIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	data := []byte("same bytes")

	sha1, err := s.Put(ctx, data, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	sha2, err := s.Put(ctx, data, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if sha1 != sha2 {
		t.Fatal("same content must hash the same")
	}
	var n int64
	if err := s.db.Model(&Blob{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("row count = %d, want 1", n)
	}
}

func TestGetNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	missing := strings.Repeat("a", 64)

	if _, err := s.Get(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get err = %v, want ErrNotFound", err)
	}
	if _, err := s.DataURL(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DataURL err = %v, want ErrNotFound", err)
	}
	exists, err := s.Exists(ctx, missing)
	if err != nil || exists {
		t.Fatalf("exists = %v, %v", exists, err)
	}
}

func TestInvalidSHA(t *testing.T) {
	s := newTestStore(t)
	for _, bad := range []string{"", "zz", strings.Repeat("a", 63), strings.Repeat("g", 64), "sha/../../etc"} {
		if _, err := s.Get(context.Background(), bad); !errors.Is(err, ErrInvalidSHA) {
			t.Errorf("Get(%q) err = %v, want ErrInvalidSHA", bad, err)
		}
	}
}

func TestPutInvalidMime(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put(context.Background(), []byte("x"), "not a mime!!"); err == nil {
		t.Fatal("want mime error")
	}
}

func TestPutDefaultMime(t *testing.T) {
	s := newTestStore(t)
	sha, err := s.Put(context.Background(), []byte("x"), "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Get(context.Background(), sha)
	if err != nil {
		t.Fatal(err)
	}
	if b.MIME != "application/octet-stream" {
		t.Fatalf("mime = %q", b.MIME)
	}
}

func TestDataURL(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	data := []byte("hello blob")
	sha, err := s.Put(ctx, data, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.DataURL(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "data:text/plain;base64,") {
		t.Fatalf("url = %q", u)
	}
}

func TestURLParseURL(t *testing.T) {
	sha := strings.Repeat("0123456789abcdef", 4)
	u := URL(sha)
	if u != "wcblob://"+sha {
		t.Fatalf("URL = %q", u)
	}
	got, err := ParseURL(u)
	if err != nil {
		t.Fatal(err)
	}
	if got != sha {
		t.Fatalf("parsed = %q", got)
	}
	for _, bad := range []string{"", "wcblob://", "wcblob://nothex", "https://" + sha, "wcblob://short"} {
		if _, err := ParseURL(bad); !errors.Is(err, ErrInvalidSHA) {
			t.Errorf("ParseURL(%q) err = %v, want ErrInvalidSHA", bad, err)
		}
	}
}
