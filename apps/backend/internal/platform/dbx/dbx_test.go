package dbx_test

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/models"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/dbx"
)

type item struct {
	models.Base
	Name string
}

func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := dbx.Open(filepath.Join(t.TempDir(), "nested", "test.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbx.Close(db) })
	if err := db.AutoMigrate(&item{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func count(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&item{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPragmas(t *testing.T) {
	db := openDB(t)
	var mode string
	var fk int
	db.Raw("PRAGMA journal_mode").Scan(&mode)
	db.Raw("PRAGMA foreign_keys").Scan(&fk)
	if mode != "wal" || fk != 1 {
		t.Fatalf("journal_mode=%q foreign_keys=%d", mode, fk)
	}
}

func TestBaseAssignsID(t *testing.T) {
	db := openDB(t)
	it := item{Name: "a"}
	if err := db.Create(&it).Error; err != nil {
		t.Fatal(err)
	}
	if len(it.ID) != 36 || it.CreatedAt.IsZero() {
		t.Fatalf("got %+v", it)
	}
}

func TestTxCommitAndRollback(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	if dbx.InTx(ctx) {
		t.Fatal("unexpected tx")
	}
	err := dbx.Tx(ctx, db, func(ctx context.Context) error {
		if !dbx.InTx(ctx) {
			t.Fatal("tx missing from ctx")
		}
		return dbx.Conn(ctx, db).Create(&item{Name: "committed"}).Error
	})
	if err != nil {
		t.Fatal(err)
	}

	boom := errors.New("boom")
	err = dbx.Tx(ctx, db, func(ctx context.Context) error {
		if err := dbx.Conn(ctx, db).Create(&item{Name: "rolled back"}).Error; err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n := count(t, db); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
}

func TestTxNestedSavepoint(t *testing.T) {
	db := openDB(t)
	err := dbx.Tx(context.Background(), db, func(ctx context.Context) error {
		if err := dbx.Conn(ctx, db).Create(&item{Name: "outer"}).Error; err != nil {
			return err
		}
		_ = dbx.Tx(ctx, db, func(ctx context.Context) error {
			if err := dbx.Conn(ctx, db).Create(&item{Name: "inner"}).Error; err != nil {
				return err
			}
			return errors.New("inner fails")
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	db.Model(&item{}).Pluck("name", &names)
	if len(names) != 1 || names[0] != "outer" {
		t.Fatalf("names = %v", names)
	}
}

func TestConnWithoutTx(t *testing.T) {
	db := openDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := dbx.Conn(ctx, db).Create(&item{Name: "x"}).Error; err == nil {
		t.Fatal("expected cancelled ctx to fail the query")
	}
}
