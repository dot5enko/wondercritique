// Package sqlite implements workspace-scoped vfs.FS instances on top of any database/sql driver
// speaking SQLite (modernc.org/sqlite, glebarez/sqlite, mattn/go-sqlite3).
//
// All workspaces live in one table (<prefix>files, default prefix "vfs_") keyed by
// (workspace_id, path). A Store is safe for concurrent use: writes are serialized through a
// mutex and every write (including its limit checks) runs in a single transaction.
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
)

// ErrWorkspaceNotEmpty is returned by CopyWorkspace when the destination already has files.
var ErrWorkspaceNotEmpty = errors.New("vfs/sqlite: destination workspace is not empty")

// Option configures the store.
type Option func(*Store)

// WithTablePrefix changes the table name prefix (default "vfs_"; the table is <prefix>files).
func WithTablePrefix(p string) Option { return func(s *Store) { s.table = p + "files" } }

// Store persists the files of many workspaces in one SQLite table.
type Store struct {
	db    *sql.DB
	table string

	mu sync.Mutex // serializes writes (quota checks and the write itself must not interleave)
}

// New opens the store on db and creates its table if needed (idempotent).
func New(db *sql.DB, opts ...Option) (*Store, error) {
	if db == nil {
		return nil, errors.New("vfs/sqlite: nil *sql.DB")
	}
	s := &Store{db: db, table: "vfs_files"}
	for _, o := range opts {
		o(s)
	}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("vfs/sqlite: migrate: %w", err)
	}
	return s, nil
}

// The data column is last on purpose: SQLite reads record columns in order, so metadata-only
// queries (List, quota sums) never touch the overflow pages of large blobs. The primary key's
// index on (workspace_id, path) serves exact lookups and the path range scans used for prefix
// listing.
func (s *Store) migrate() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS ` + s.table + ` (
		workspace_id TEXT NOT NULL,
		path TEXT NOT NULL,
		size INTEGER NOT NULL,
		sha256 TEXT NOT NULL,
		version INTEGER NOT NULL,
		mod_time INTEGER NOT NULL,
		data BLOB NOT NULL,
		PRIMARY KEY (workspace_id, path)
	)`)
	return err
}

// Workspace returns the file system of workspace id, enforcing limits (zero fields take the vfs
// defaults). Workspaces exist implicitly: an unknown id is an empty workspace.
func (s *Store) Workspace(id string, limits vfs.Limits) vfs.FS {
	return &workspace{s: s, id: id, limits: limits.WithDefaults()}
}

// DeleteWorkspace removes every file of workspace id. Deleting an empty workspace is a no-op.
func (s *Store) DeleteWorkspace(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM `+s.table+` WHERE workspace_id = ?`, id)
	return err
}

// CopyWorkspace copies every file of workspace src into workspace dst, preserving content,
// versions and modification times. dst must be empty (ErrWorkspaceNotEmpty otherwise) and differ
// from src. Limits are not checked: the copy has exactly the size of the source.
func (s *Store) CopyWorkspace(ctx context.Context, src, dst string) error {
	if src == dst {
		return errors.New("vfs/sqlite: cannot copy a workspace onto itself")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tx(ctx, func(tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM `+s.table+` WHERE workspace_id = ? LIMIT 1`, dst).Scan(&one)
		switch {
		case err == nil:
			return ErrWorkspaceNotEmpty
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO `+s.table+` (workspace_id, path, size, sha256, version, mod_time, data)
			SELECT ?, path, size, sha256, version, mod_time, data FROM `+s.table+` WHERE workspace_id = ?`, dst, src)
		return err
	})
}

func (s *Store) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type workspace struct {
	s      *Store
	id     string
	limits vfs.Limits
}

var _ vfs.FS = (*workspace)(nil)

func prepare(ctx context.Context, path string) (string, error) {
	p, err := vfs.Clean(path)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return p, nil
}

// bounds returns the half-open range [lo, hi) of paths strictly below cleaned directory dir.
// '0' is the byte after '/', so the range covers exactly dir+"/..." in binary collation; a range
// query also sidesteps LIKE wildcards ('%', '_') appearing in paths.
func bounds(dir string) (lo, hi string) {
	if dir == "/" {
		return "/", "0"
	}
	return dir + "/", dir + "0"
}

const infoCols = `path, size, sha256, version, mod_time`

func scanInfo(row interface{ Scan(...any) error }) (vfs.FileInfo, error) {
	var (
		fi  vfs.FileInfo
		mod int64
	)
	if err := row.Scan(&fi.Path, &fi.Size, &fi.SHA256, &fi.Version, &mod); err != nil {
		return vfs.FileInfo{}, err
	}
	fi.ModTime = time.Unix(0, mod).UTC()
	return fi, nil
}

func (w *workspace) isDir(ctx context.Context, q querier, p string) (bool, error) {
	if p == "/" {
		return true, nil
	}
	lo, hi := bounds(p)
	var one int
	err := q.QueryRowContext(ctx,
		`SELECT 1 FROM `+w.s.table+` WHERE workspace_id = ? AND path >= ? AND path < ? LIMIT 1`, w.id, lo, hi,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (w *workspace) stat(ctx context.Context, q querier, p string) (vfs.FileInfo, bool, error) {
	fi, err := scanInfo(q.QueryRowContext(ctx,
		`SELECT `+infoCols+` FROM `+w.s.table+` WHERE workspace_id = ? AND path = ?`, w.id, p))
	if errors.Is(err, sql.ErrNoRows) {
		return vfs.FileInfo{}, false, nil
	}
	return fi, err == nil, err
}

// Stat implements vfs.FS.
func (w *workspace) Stat(ctx context.Context, path string) (vfs.FileInfo, error) {
	p, err := prepare(ctx, path)
	if err != nil {
		return vfs.FileInfo{}, err
	}
	fi, ok, err := w.stat(ctx, w.s.db, p)
	if err != nil || ok {
		return fi, err
	}
	dir, err := w.isDir(ctx, w.s.db, p)
	switch {
	case err != nil:
		return vfs.FileInfo{}, err
	case dir:
		return vfs.FileInfo{Path: p, IsDir: true}, nil
	}
	return vfs.FileInfo{}, vfs.ErrNotFound
}

// Read implements vfs.FS.
func (w *workspace) Read(ctx context.Context, path string) ([]byte, error) {
	p, err := prepare(ctx, path)
	if err != nil {
		return nil, err
	}
	var data []byte
	err = w.s.db.QueryRowContext(ctx,
		`SELECT data FROM `+w.s.table+` WHERE workspace_id = ? AND path = ?`, w.id, p).Scan(&data)
	if err == nil {
		if data == nil {
			data = []byte{}
		}
		return data, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	dir, err := w.isDir(ctx, w.s.db, p)
	switch {
	case err != nil:
		return nil, err
	case dir:
		return nil, vfs.ErrIsDir
	}
	return nil, vfs.ErrNotFound
}

// Write implements vfs.FS.
func (w *workspace) Write(ctx context.Context, path string, data []byte) error {
	p, err := prepare(ctx, path)
	if err != nil {
		return err
	}
	if p == "/" {
		return vfs.ErrIsDir
	}
	size := int64(len(data))
	if err := w.limits.CheckFileSize(size); err != nil {
		return err
	}
	if data == nil {
		data = []byte{} // the column is NOT NULL; a nil slice would bind as NULL
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	now := time.Now().UTC().UnixNano()

	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	return w.s.tx(ctx, func(tx *sql.Tx) error {
		if dir, err := w.isDir(ctx, tx, p); err != nil {
			return err
		} else if dir {
			return vfs.ErrIsDir
		}
		if parents := vfs.Parents(p); len(parents) > 0 {
			q := `SELECT 1 FROM ` + w.s.table + ` WHERE workspace_id = ? AND path IN (?` + strings.Repeat(",?", len(parents)-1) + `) LIMIT 1`
			args := []any{w.id}
			for _, d := range parents {
				args = append(args, d)
			}
			var one int
			err := tx.QueryRowContext(ctx, q, args...).Scan(&one)
			if err == nil {
				return vfs.ErrNotDir
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		old, exists, err := w.stat(ctx, tx, p)
		if err != nil {
			return err
		}
		var u vfs.Usage
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*), COALESCE(SUM(size), 0) FROM `+w.s.table+` WHERE workspace_id = ?`, w.id,
		).Scan(&u.Files, &u.Bytes); err != nil {
			return err
		}
		if err := w.limits.CheckQuota(u, size, exists, old.Size); err != nil {
			return err
		}
		if exists {
			_, err = tx.ExecContext(ctx, `UPDATE `+w.s.table+`
				SET size = ?, sha256 = ?, version = version + 1, mod_time = ?, data = ?
				WHERE workspace_id = ? AND path = ?`, size, digest, now, data, w.id, p)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO `+w.s.table+`
				(workspace_id, path, size, sha256, version, mod_time, data) VALUES (?, ?, ?, ?, 1, ?, ?)`,
				w.id, p, size, digest, now, data)
		}
		return err
	})
}

// Delete implements vfs.FS.
func (w *workspace) Delete(ctx context.Context, path string) error {
	p, err := prepare(ctx, path)
	if err != nil {
		return err
	}
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	res, err := w.s.db.ExecContext(ctx,
		`DELETE FROM `+w.s.table+` WHERE workspace_id = ? AND path = ?`, w.id, p)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		return nil
	}
	dir, err := w.isDir(ctx, w.s.db, p)
	switch {
	case err != nil:
		return err
	case dir:
		return vfs.ErrIsDir
	}
	return vfs.ErrNotFound
}

// List implements vfs.FS.
func (w *workspace) List(ctx context.Context, dir string, recursive bool) ([]vfs.FileInfo, error) {
	d, err := prepare(ctx, dir)
	if err != nil {
		return nil, err
	}
	// Read in one transaction so the listing is a consistent snapshot.
	var out []vfs.FileInfo
	err = w.s.tx(ctx, func(tx *sql.Tx) error {
		if _, isFile, err := w.stat(ctx, tx, d); err != nil {
			return err
		} else if isFile {
			return vfs.ErrNotDir
		}
		lo, hi := bounds(d)
		rows, err := tx.QueryContext(ctx, `SELECT `+infoCols+` FROM `+w.s.table+`
			WHERE workspace_id = ? AND path >= ? AND path < ? ORDER BY path`, w.id, lo, hi)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			fi, err := scanInfo(rows)
			if err != nil {
				return err
			}
			out = append(out, fi)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 && d != "/" {
		return nil, vfs.ErrNotFound
	}
	if !recursive {
		return vfs.Children(d, out), nil
	}
	if out == nil {
		out = []vfs.FileInfo{}
	}
	return out, nil
}
