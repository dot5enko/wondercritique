// Package dbx provides the GORM database, model registration and transaction helpers.
package dbx

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"go.uber.org/fx"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/config"
)

// Module provides *gorm.DB and auto-migrates every model registered with AsModel.
var Module = fx.Module("dbx",
	fx.Provide(New),
	fx.Invoke(migrate),
)

const pragmas = "_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_txlock=immediate"

// AsModel registers a model (e.g. &models.User{}) for AutoMigrate.
func AsModel(m any) fx.Option {
	return fx.Provide(fx.Annotate(func() any { return m }, fx.ResultTags(`group:"models"`)))
}

// New opens the database at cfg.DBPath and closes it when the app stops.
func New(lc fx.Lifecycle, cfg *config.Config, log *slog.Logger) (*gorm.DB, error) {
	db, err := Open(cfg.DBPath, log)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.StopHook(func() error { return Close(db) }))
	return db, nil
}

// Open opens a SQLite database at path (a file path, ":memory:" or a "file:" URI) with the standard
// pragmas, creating the parent directory of plain file paths.
func Open(path string, log *slog.Logger) (*gorm.DB, error) {
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("dbx: create db dir: %w", err)
		}
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	db, err := gorm.Open(sqlite.Open(path+sep+pragmas), &gorm.Config{
		Logger: logger.NewSlogLogger(log.With("component", "gorm"), logger.Config{
			LogLevel:                  logger.Warn,
			SlowThreshold:             200 * time.Millisecond,
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("dbx: open %s: %w", path, err)
	}
	return db, nil
}

// SQLDB returns the underlying *sql.DB.
func SQLDB(db *gorm.DB) (*sql.DB, error) { return db.DB() }

// Close closes the underlying connection pool.
func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

type migrateParams struct {
	fx.In
	DB     *gorm.DB
	Models []any `group:"models"`
}

func migrate(p migrateParams) error {
	if len(p.Models) == 0 {
		return nil
	}
	if err := p.DB.AutoMigrate(p.Models...); err != nil {
		return fmt.Errorf("dbx: automigrate: %w", err)
	}
	return nil
}
