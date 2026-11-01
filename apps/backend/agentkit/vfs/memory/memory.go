// Package memory implements vfs.FS in process memory. It is meant for tests, ephemeral
// workspaces and read-only content built at startup (wrap it with vfs.ReadOnly).
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
)

// FS is an in-memory vfs.FS. It is safe for concurrent use; reads share a lock, writes are
// serialized. Content is copied on Write and Read, so callers may reuse their buffers.
type FS struct {
	limits vfs.Limits

	mu    sync.RWMutex
	files map[string]*file
	dirs  map[string]int // implicit directory → number of files below it (excluding "/")
	total int64
}

type file struct {
	data []byte
	info vfs.FileInfo
}

var _ vfs.FS = (*FS)(nil)

// New returns an empty file system enforcing limits (zero fields take the vfs defaults).
func New(limits vfs.Limits) *FS {
	return &FS{limits: limits.WithDefaults(), files: map[string]*file{}, dirs: map[string]int{}}
}

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

// Stat implements vfs.FS.
func (m *FS) Stat(ctx context.Context, path string) (vfs.FileInfo, error) {
	p, err := prepare(ctx, path)
	if err != nil {
		return vfs.FileInfo{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if f, ok := m.files[p]; ok {
		return f.info, nil
	}
	if p == "/" || m.dirs[p] > 0 {
		return vfs.FileInfo{Path: p, IsDir: true}, nil
	}
	return vfs.FileInfo{}, vfs.ErrNotFound
}

// Read implements vfs.FS.
func (m *FS) Read(ctx context.Context, path string) ([]byte, error) {
	p, err := prepare(ctx, path)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if f, ok := m.files[p]; ok {
		return append([]byte{}, f.data...), nil
	}
	if p == "/" || m.dirs[p] > 0 {
		return nil, vfs.ErrIsDir
	}
	return nil, vfs.ErrNotFound
}

// Write implements vfs.FS.
func (m *FS) Write(ctx context.Context, path string, data []byte) error {
	p, err := prepare(ctx, path)
	if err != nil {
		return err
	}
	if p == "/" {
		return vfs.ErrIsDir
	}
	size := int64(len(data))
	if err := m.limits.CheckFileSize(size); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	content := append([]byte{}, data...)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dirs[p] > 0 {
		return vfs.ErrIsDir
	}
	parents := vfs.Parents(p)
	for _, d := range parents {
		if _, ok := m.files[d]; ok {
			return vfs.ErrNotDir
		}
	}
	old, exists := m.files[p]
	var oldSize, version int64
	if exists {
		oldSize, version = old.info.Size, old.info.Version
	}
	if err := m.limits.CheckQuota(vfs.Usage{Files: len(m.files), Bytes: m.total}, size, exists, oldSize); err != nil {
		return err
	}
	m.files[p] = &file{data: content, info: vfs.FileInfo{
		Path:    p,
		Size:    size,
		SHA256:  hex.EncodeToString(sum[:]),
		Version: version + 1,
		ModTime: time.Now().UTC(),
	}}
	m.total += size - oldSize
	if !exists {
		for _, d := range parents {
			m.dirs[d]++
		}
	}
	return nil
}

// Delete implements vfs.FS.
func (m *FS) Delete(ctx context.Context, path string) error {
	p, err := prepare(ctx, path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[p]
	if !ok {
		if p == "/" || m.dirs[p] > 0 {
			return vfs.ErrIsDir
		}
		return vfs.ErrNotFound
	}
	delete(m.files, p)
	m.total -= f.info.Size
	for _, d := range vfs.Parents(p) {
		if m.dirs[d]--; m.dirs[d] <= 0 {
			delete(m.dirs, d)
		}
	}
	return nil
}

// List implements vfs.FS.
func (m *FS) List(ctx context.Context, dir string, recursive bool) ([]vfs.FileInfo, error) {
	d, err := prepare(ctx, dir)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.files[d]; ok {
		return nil, vfs.ErrNotDir
	}
	if d != "/" && m.dirs[d] == 0 {
		return nil, vfs.ErrNotFound
	}
	prefix := d + "/"
	if d == "/" {
		prefix = "/"
	}
	below := make([]vfs.FileInfo, 0, len(m.files))
	for p, f := range m.files {
		if strings.HasPrefix(p, prefix) {
			below = append(below, f.info)
		}
	}
	if !recursive {
		return vfs.Children(d, below), nil
	}
	vfs.SortInfos(below)
	return below, nil
}

// Usage returns the current number of files and their total size.
func (m *FS) Usage() vfs.Usage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return vfs.Usage{Files: len(m.files), Bytes: m.total}
}
