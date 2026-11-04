package canvas

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

// FS is a read-only vfs view of a thread's live canvas files, derived from the
// table on every call (no caching). Agents mount it at /canvas.
func (s *Store) FS(thread string) vfs.FS { return &threadFS{s: s, thread: thread} }

type threadFS struct {
	s      *Store
	thread string
}

type fsEntry struct {
	info vfs.FileInfo
	sha  string
}

// entries lists the live files with their blob digests, sorted by path.
func (f *threadFS) entries(ctx context.Context) ([]fsEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var rows []struct {
		Path      string
		Size      int64
		CreatedAt time.Time
		BlobSHA   string
	}
	err := f.s.conn(ctx).Model(&models.CanvasFile{}).
		Select("canvas_files.path, canvas_files.size, canvas_files.created_at, artifacts.blob_sha").
		Joins("JOIN artifacts ON artifacts.id = canvas_files.artifact_id").
		Where("canvas_files.thread_id = ? AND canvas_files.deleted_at IS NULL", f.thread).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("canvas fs: %w", err)
	}
	out := make([]fsEntry, len(rows))
	for i, r := range rows {
		out[i] = fsEntry{
			info: vfs.FileInfo{Path: r.Path, Size: r.Size, SHA256: r.BlobSHA, Version: 1, ModTime: r.CreatedAt.UTC()},
			sha:  r.BlobSHA,
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].info.Path < out[j].info.Path })
	return out, nil
}

// lookup classifies a cleaned path: a file entry, a directory, or neither.
func lookup(entries []fsEntry, p string) (*fsEntry, bool) {
	if p == "/" {
		return nil, true
	}
	dir := false
	for i := range entries {
		e := &entries[i]
		if e.info.Path == p {
			return e, false
		}
		if strings.HasPrefix(e.info.Path, p+"/") {
			dir = true
		}
	}
	return nil, dir
}

func (f *threadFS) Stat(ctx context.Context, path string) (vfs.FileInfo, error) {
	p, err := vfs.Clean(path)
	if err != nil {
		return vfs.FileInfo{}, err
	}
	entries, err := f.entries(ctx)
	if err != nil {
		return vfs.FileInfo{}, err
	}
	e, dir := lookup(entries, p)
	switch {
	case e != nil:
		return e.info, nil
	case dir:
		return vfs.FileInfo{Path: p, IsDir: true}, nil
	}
	return vfs.FileInfo{}, vfs.ErrNotFound
}

func (f *threadFS) Read(ctx context.Context, path string) ([]byte, error) {
	p, err := vfs.Clean(path)
	if err != nil {
		return nil, err
	}
	entries, err := f.entries(ctx)
	if err != nil {
		return nil, err
	}
	e, dir := lookup(entries, p)
	switch {
	case e != nil:
		b, err := f.s.blobs.Get(ctx, e.sha)
		if errors.Is(err, blobs.ErrNotFound) {
			return nil, vfs.ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), b.Data...), nil
	case dir:
		return nil, vfs.ErrIsDir
	}
	return nil, vfs.ErrNotFound
}

func (f *threadFS) Write(_ context.Context, path string, _ []byte) error {
	if _, err := vfs.Clean(path); err != nil {
		return err
	}
	return vfs.ErrReadOnly
}

func (f *threadFS) Delete(_ context.Context, path string) error {
	if _, err := vfs.Clean(path); err != nil {
		return err
	}
	return vfs.ErrReadOnly
}

func (f *threadFS) List(ctx context.Context, dir string, recursive bool) ([]vfs.FileInfo, error) {
	p, err := vfs.Clean(dir)
	if err != nil {
		return nil, err
	}
	entries, err := f.entries(ctx)
	if err != nil {
		return nil, err
	}
	e, isDir := lookup(entries, p)
	if e != nil {
		return nil, vfs.ErrNotDir
	}
	if !isDir {
		return nil, vfs.ErrNotFound
	}
	var below []vfs.FileInfo
	for _, e := range entries {
		if vfs.IsWithin(e.info.Path, p) && e.info.Path != p {
			below = append(below, e.info)
		}
	}
	if recursive {
		if below == nil {
			below = []vfs.FileInfo{}
		}
		return below, nil
	}
	return vfs.Children(p, below), nil
}
