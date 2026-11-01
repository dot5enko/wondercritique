package vfs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// MountPoint attaches a file system at a directory of a Mount.
type MountPoint struct {
	// Prefix is the absolute directory the file system appears under ("/work", "/skills", or "/"
	// for a root file system). It is normalized with Clean.
	Prefix string
	// FS is the mounted file system. It sees paths relative to Prefix: with Prefix "/skills", the
	// outer path /skills/a/SKILL.md is /a/SKILL.md in FS.
	FS FS
	// ReadOnly makes Write and Delete under Prefix fail with ErrReadOnly (FS itself may be
	// writable; the same FS can be mounted writable elsewhere).
	ReadOnly bool
}

// Mount composes several file systems into one tree. It implements FS and is safe for
// concurrent use if the mounted file systems are.
//
// Routing: a path is served by the mount point with the longest Prefix containing it. Nested
// mounts are allowed (e.g. "/" and "/work/shared"); the inner mount shadows whatever the outer
// file system stores under its prefix.
//
// Mount points and their ancestors are directories that always exist, even when the mounted
// file system is empty or nothing else lives there: Stat reports them as directories, List of
// their parent includes them, Read/Write/Delete on them fail with ErrIsDir. Files of an outer
// file system whose path collides with such a directory are hidden.
//
// List merges mounts: List(dir, false) shows the direct children of dir from the routed file
// system plus the mount points (or their first path segment) directly below dir; List(dir, true)
// returns every visible file below dir across all mounts, with paths re-prefixed into the outer
// tree.
//
// Paths not covered by any mount point (possible when there is no "/" mount) and that are not
// ancestors of a mount point do not exist: Stat, Read and List fail with ErrNotFound, Write and
// Delete with ErrReadOnly (there is nothing to write to). Errors returned by mounted file
// systems are passed through unchanged.
type Mount struct {
	points []MountPoint    // sorted by descending prefix length (longest-prefix routing)
	synth  map[string]bool // "/", every mount prefix and all their ancestors: always directories
}

var _ FS = (*Mount)(nil)

// NewMount builds a Mount. It fails if a prefix is invalid, a file system is nil or two mount
// points share a prefix. Zero mount points give an empty tree where only "/" exists.
func NewMount(points ...MountPoint) (*Mount, error) {
	m := &Mount{synth: map[string]bool{"/": true}}
	for _, mp := range points {
		p, err := Clean(mp.Prefix)
		if err != nil {
			return nil, fmt.Errorf("vfs: mount prefix: %w", err)
		}
		if mp.FS == nil {
			return nil, fmt.Errorf("vfs: mount %s: nil FS", p)
		}
		if slices.ContainsFunc(m.points, func(o MountPoint) bool { return o.Prefix == p }) {
			return nil, fmt.Errorf("vfs: mount %s: duplicate prefix", p)
		}
		mp.Prefix = p
		m.points = append(m.points, mp)
		m.synth[p] = true
		for _, d := range Parents(p) {
			m.synth[d] = true
		}
	}
	slices.SortStableFunc(m.points, func(a, b MountPoint) int { return len(b.Prefix) - len(a.Prefix) })
	return m, nil
}

// route returns the index of the mount point serving cleaned path p (-1 if none) and p relative
// to that mount point.
func (m *Mount) route(p string) (int, string) {
	for i, mp := range m.points {
		if IsWithin(p, mp.Prefix) {
			switch {
			case mp.Prefix == "/":
				return i, p
			case p == mp.Prefix:
				return i, "/"
			default:
				return i, p[len(mp.Prefix):]
			}
		}
	}
	return -1, ""
}

func outerPath(prefix, inner string) string {
	switch {
	case prefix == "/":
		return inner
	case inner == "/":
		return prefix
	default:
		return prefix + inner
	}
}

// visible re-prefixes entries listed by mount i and drops those another mount shadows or that
// collide with an always-present directory.
func (m *Mount) visible(i int, infos []FileInfo, out []FileInfo) []FileInfo {
	prefix := m.points[i].Prefix
	for _, fi := range infos {
		fi.Path = outerPath(prefix, fi.Path)
		if m.synth[fi.Path] {
			continue
		}
		if owner, _ := m.route(fi.Path); owner != i {
			continue
		}
		out = append(out, fi)
	}
	return out
}

func (m *Mount) prepare(ctx context.Context, path string) (string, error) {
	p, err := Clean(path)
	if err != nil {
		return "", err
	}
	return p, ctx.Err()
}

// Stat implements FS.
func (m *Mount) Stat(ctx context.Context, path string) (FileInfo, error) {
	p, err := m.prepare(ctx, path)
	if err != nil {
		return FileInfo{}, err
	}
	if m.synth[p] {
		return FileInfo{Path: p, IsDir: true}, nil
	}
	i, rel := m.route(p)
	if i < 0 {
		return FileInfo{}, ErrNotFound
	}
	fi, err := m.points[i].FS.Stat(ctx, rel)
	if err != nil {
		return FileInfo{}, err
	}
	fi.Path = p
	return fi, nil
}

// Read implements FS.
func (m *Mount) Read(ctx context.Context, path string) ([]byte, error) {
	p, err := m.prepare(ctx, path)
	if err != nil {
		return nil, err
	}
	if m.synth[p] {
		return nil, ErrIsDir
	}
	i, rel := m.route(p)
	if i < 0 {
		return nil, ErrNotFound
	}
	return m.points[i].FS.Read(ctx, rel)
}

// writable routes a path for Write/Delete.
func (m *Mount) writable(ctx context.Context, path string) (FS, string, error) {
	p, err := m.prepare(ctx, path)
	if err != nil {
		return nil, "", err
	}
	if m.synth[p] {
		return nil, "", ErrIsDir
	}
	i, rel := m.route(p)
	if i < 0 {
		return nil, "", fmt.Errorf("%w: no file system is mounted at %s", ErrReadOnly, p)
	}
	if m.points[i].ReadOnly {
		return nil, "", fmt.Errorf("%w: %s is mounted read-only", ErrReadOnly, m.points[i].Prefix)
	}
	return m.points[i].FS, rel, nil
}

// Write implements FS.
func (m *Mount) Write(ctx context.Context, path string, data []byte) error {
	fsys, rel, err := m.writable(ctx, path)
	if err != nil {
		return err
	}
	return fsys.Write(ctx, rel, data)
}

// Delete implements FS.
func (m *Mount) Delete(ctx context.Context, path string) error {
	fsys, rel, err := m.writable(ctx, path)
	if err != nil {
		return err
	}
	return fsys.Delete(ctx, rel)
}

// List implements FS.
func (m *Mount) List(ctx context.Context, dir string, recursive bool) ([]FileInfo, error) {
	d, err := m.prepare(ctx, dir)
	if err != nil {
		return nil, err
	}
	i, rel := m.route(d)
	if !m.synth[d] {
		if i < 0 {
			return nil, ErrNotFound
		}
		infos, err := m.points[i].FS.List(ctx, rel, recursive)
		if err != nil {
			return nil, err
		}
		return m.visible(i, infos, make([]FileInfo, 0, len(infos))), nil
	}

	// d always exists: merge what the routed file system has there with the mounts below d.
	out := []FileInfo{}
	if i >= 0 {
		infos, err := m.points[i].FS.List(ctx, rel, recursive)
		switch {
		case err == nil:
			out = m.visible(i, infos, out)
		case !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrNotDir):
			return nil, err
		}
	}
	seen := map[string]bool{}
	for j, mp := range m.points {
		if j == i || !IsWithin(mp.Prefix, d) {
			continue
		}
		if recursive {
			infos, err := mp.FS.List(ctx, "/", true)
			if err != nil {
				return nil, err
			}
			out = m.visible(j, infos, out)
			continue
		}
		rest := strings.TrimPrefix(mp.Prefix, d)
		if d != "/" {
			rest = rest[1:] // drop the separator
		}
		if k := strings.IndexByte(rest, '/'); k >= 0 {
			rest = rest[:k]
		}
		child := outerPath(d, "/"+rest)
		if !seen[child] {
			seen[child] = true
			out = append(out, FileInfo{Path: child, IsDir: true})
		}
	}
	SortInfos(out)
	return out, nil
}
