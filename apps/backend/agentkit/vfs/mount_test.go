package vfs_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs/memory"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs/vfstest"
)

func seeded(t *testing.T, files map[string][]byte) *memory.FS {
	t.Helper()
	m := memory.New(vfs.Limits{})
	for p, data := range files {
		if err := m.Write(context.Background(), p, data); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}
	return m
}

func mustMount(t *testing.T, points ...vfs.MountPoint) *vfs.Mount {
	t.Helper()
	m, err := vfs.NewMount(points...)
	if err != nil {
		t.Fatalf("NewMount: %v", err)
	}
	return m
}

func names(infos []vfs.FileInfo) []string {
	out := []string{}
	for _, fi := range infos {
		if fi.IsDir {
			out = append(out, fi.Path+"/")
		} else {
			out = append(out, fi.Path)
		}
	}
	return out
}

// sub exposes directory dir of fsys as the root of a file system (test adapter used to run the
// conformance suite against a file system mounted below "/").
type sub struct {
	fs  vfs.FS
	dir string
}

func (s sub) in(p string) (string, error) {
	c, err := vfs.Clean(p)
	if err != nil {
		return "", err
	}
	if c == "/" {
		return s.dir, nil
	}
	return s.dir + c, nil
}

func (s sub) out(p string) string {
	if p == s.dir {
		return "/"
	}
	return strings.TrimPrefix(p, s.dir)
}

func (s sub) Stat(ctx context.Context, p string) (vfs.FileInfo, error) {
	q, err := s.in(p)
	if err != nil {
		return vfs.FileInfo{}, err
	}
	fi, err := s.fs.Stat(ctx, q)
	fi.Path = s.out(fi.Path)
	return fi, err
}

func (s sub) Read(ctx context.Context, p string) ([]byte, error) {
	q, err := s.in(p)
	if err != nil {
		return nil, err
	}
	return s.fs.Read(ctx, q)
}

func (s sub) Write(ctx context.Context, p string, data []byte) error {
	q, err := s.in(p)
	if err != nil {
		return err
	}
	return s.fs.Write(ctx, q, data)
}

func (s sub) Delete(ctx context.Context, p string) error {
	q, err := s.in(p)
	if err != nil {
		return err
	}
	return s.fs.Delete(ctx, q)
}

func (s sub) List(ctx context.Context, p string, rec bool) ([]vfs.FileInfo, error) {
	q, err := s.in(p)
	if err != nil {
		return nil, err
	}
	infos, err := s.fs.List(ctx, q, rec)
	for i := range infos {
		infos[i].Path = s.out(infos[i].Path)
	}
	return infos, err
}

func TestMountConformanceRoot(t *testing.T) {
	vfstest.Run(t, func(t *testing.T, l vfs.Limits) vfs.FS {
		return mustMount(t, vfs.MountPoint{Prefix: "/", FS: memory.New(l)})
	})
}

func TestMountConformanceNested(t *testing.T) {
	vfstest.Run(t, func(t *testing.T, l vfs.Limits) vfs.FS {
		skills := seeded(t, map[string][]byte{"/a/SKILL.md": []byte("skill")})
		m := mustMount(t,
			vfs.MountPoint{Prefix: "/work", FS: memory.New(l)},
			vfs.MountPoint{Prefix: "/skills", FS: skills, ReadOnly: true},
		)
		return sub{fs: m, dir: "/work"}
	})
}

func TestReadOnlyConformance(t *testing.T) {
	t.Run("Wrapper", func(t *testing.T) {
		vfstest.RunReadOnly(t, func(t *testing.T, files map[string][]byte) vfs.FS {
			return vfs.ReadOnly(seeded(t, files))
		})
	})
	t.Run("ReadOnlyRootMount", func(t *testing.T) {
		vfstest.RunReadOnly(t, func(t *testing.T, files map[string][]byte) vfs.FS {
			return mustMount(t, vfs.MountPoint{Prefix: "/", FS: seeded(t, files), ReadOnly: true})
		})
	})
	t.Run("ReadOnlyNestedMount", func(t *testing.T) {
		vfstest.RunReadOnly(t, func(t *testing.T, files map[string][]byte) vfs.FS {
			return sub{fs: mustMount(t, vfs.MountPoint{Prefix: "/ro/x", FS: seeded(t, files), ReadOnly: true}), dir: "/ro/x"}
		})
	})
}

func TestReadOnlyIdempotent(t *testing.T) {
	m := memory.New(vfs.Limits{})
	ro := vfs.ReadOnly(m)
	if vfs.ReadOnly(ro) != ro {
		t.Fatal("ReadOnly wrapped twice")
	}
}

func TestNewMountErrors(t *testing.T) {
	m := memory.New(vfs.Limits{})
	bad := [][]vfs.MountPoint{
		{{Prefix: "relative", FS: m}},
		{{Prefix: "/a/../b", FS: m}},
		{{Prefix: "/a", FS: nil}},
		{{Prefix: "/a", FS: m}, {Prefix: "/a/", FS: m}},
		{{Prefix: "/", FS: m}, {Prefix: "//", FS: m}},
	}
	for i, points := range bad {
		if _, err := vfs.NewMount(points...); err == nil {
			t.Errorf("case %d: NewMount accepted %+v", i, points)
		}
	}
}

func TestMountEmpty(t *testing.T) {
	ctx := context.Background()
	m := mustMount(t)
	if fi, err := m.Stat(ctx, "/"); err != nil || !fi.IsDir {
		t.Fatalf("Stat(/) = %+v, %v", fi, err)
	}
	if got, err := m.List(ctx, "/", true); err != nil || len(got) != 0 {
		t.Fatalf("List(/) = %v, %v", got, err)
	}
	if _, err := m.Read(ctx, "/x"); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("Read err = %v", err)
	}
	if err := m.Write(ctx, "/x", nil); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("Write err = %v", err)
	}
}

func TestMountWithoutRoot(t *testing.T) {
	ctx := context.Background()
	work := memory.New(vfs.Limits{})
	skills := seeded(t, map[string][]byte{"/a/SKILL.md": []byte("skill a"), "/b/SKILL.md": []byte("skill b")})
	m := mustMount(t,
		vfs.MountPoint{Prefix: "/work", FS: work},
		vfs.MountPoint{Prefix: "/skills", FS: skills, ReadOnly: true},
	)

	if got, err := m.List(ctx, "/", false); err != nil || !slices.Equal(names(got), []string{"/skills/", "/work/"}) {
		t.Fatalf("List(/) = %v, %v (empty /work must still appear)", names(got), err)
	}
	if got, err := m.List(ctx, "/work", false); err != nil || len(got) != 0 {
		t.Fatalf("List(/work) = %v, %v", names(got), err)
	}
	for _, p := range []string{"/work", "/skills", "/"} {
		if fi, err := m.Stat(ctx, p); err != nil || !fi.IsDir || fi.Path != p {
			t.Errorf("Stat(%s) = %+v, %v", p, fi, err)
		}
		if _, err := m.Read(ctx, p); !errors.Is(err, vfs.ErrIsDir) {
			t.Errorf("Read(%s) err = %v, want ErrIsDir", p, err)
		}
		if err := m.Write(ctx, p, []byte("x")); !errors.Is(err, vfs.ErrIsDir) {
			t.Errorf("Write(%s) err = %v, want ErrIsDir", p, err)
		}
		if err := m.Delete(ctx, p); !errors.Is(err, vfs.ErrIsDir) {
			t.Errorf("Delete(%s) err = %v, want ErrIsDir", p, err)
		}
	}

	if err := m.Write(ctx, "/work/notes/x.md", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if data, err := work.Read(ctx, "/notes/x.md"); err != nil || string(data) != "x" {
		t.Fatalf("mounted FS sees %q, %v; want the file at /notes/x.md", data, err)
	}
	fi, err := m.Stat(ctx, "/skills/a/SKILL.md")
	if err != nil || fi.Path != "/skills/a/SKILL.md" || fi.Size != 7 {
		t.Fatalf("Stat(skill) = %+v, %v", fi, err)
	}
	if data, err := m.Read(ctx, "/skills/b/SKILL.md"); err != nil || string(data) != "skill b" {
		t.Fatalf("Read(skill) = %q, %v", data, err)
	}
	for _, p := range []string{"/skills/a/SKILL.md", "/skills/new.md"} {
		if err := m.Write(ctx, p, []byte("x")); !errors.Is(err, vfs.ErrReadOnly) || !strings.Contains(err.Error(), "/skills is mounted read-only") {
			t.Errorf("Write(%s) err = %v, want ErrReadOnly", p, err)
		}
		if err := m.Delete(ctx, p); !errors.Is(err, vfs.ErrReadOnly) {
			t.Errorf("Delete(%s) err = %v, want ErrReadOnly", p, err)
		}
	}

	// Outside every mount.
	if _, err := m.Read(ctx, "/other.txt"); !errors.Is(err, vfs.ErrNotFound) {
		t.Errorf("Read outside err = %v", err)
	}
	if _, err := m.Stat(ctx, "/other"); !errors.Is(err, vfs.ErrNotFound) {
		t.Errorf("Stat outside err = %v", err)
	}
	if _, err := m.List(ctx, "/other", false); !errors.Is(err, vfs.ErrNotFound) {
		t.Errorf("List outside err = %v", err)
	}
	if err := m.Write(ctx, "/other.txt", nil); !errors.Is(err, vfs.ErrReadOnly) || !strings.Contains(err.Error(), "no file system is mounted") {
		t.Errorf("Write outside err = %v", err)
	}
	if err := m.Delete(ctx, "/other.txt"); !errors.Is(err, vfs.ErrReadOnly) {
		t.Errorf("Delete outside err = %v", err)
	}
	if _, err := m.Read(ctx, "/work/../x"); !errors.Is(err, vfs.ErrInvalidPath) {
		t.Errorf("invalid path err = %v", err)
	}

	got, err := m.List(ctx, "/", true)
	if err != nil || !slices.Equal(names(got), []string{"/skills/a/SKILL.md", "/skills/b/SKILL.md", "/work/notes/x.md"}) {
		t.Fatalf("List(/, true) = %v, %v", names(got), err)
	}
	got, err = m.List(ctx, "/skills", false)
	if err != nil || !slices.Equal(names(got), []string{"/skills/a/", "/skills/b/"}) {
		t.Fatalf("List(/skills) = %v, %v", names(got), err)
	}
	got, err = m.List(ctx, "/work/notes", true)
	if err != nil || !slices.Equal(names(got), []string{"/work/notes/x.md"}) {
		t.Fatalf("List(/work/notes) = %v, %v", names(got), err)
	}
	if _, err := m.List(ctx, "/work/notes/x.md", false); !errors.Is(err, vfs.ErrNotDir) {
		t.Fatalf("List(file) err = %v", err)
	}

	globbed, err := vfs.Glob(ctx, m, "/**/SKILL.md", 0)
	if err != nil || !slices.Equal(names(globbed), []string{"/skills/a/SKILL.md", "/skills/b/SKILL.md"}) {
		t.Fatalf("Glob across mounts = %v, %v", names(globbed), err)
	}
}

func TestMountNestedShadowing(t *testing.T) {
	ctx := context.Background()
	root := seeded(t, map[string][]byte{
		"/readme.md":         []byte("root"),
		"/work/shadowed.txt": []byte("hidden by the /work mount"),
		"/deep":              []byte("a file where a mount needs a directory"),
	})
	work := seeded(t, map[string][]byte{"/a.txt": []byte("work a")})
	inner := seeded(t, map[string][]byte{"/z.txt": []byte("z")})
	m := mustMount(t,
		vfs.MountPoint{Prefix: "/deep/er/mnt", FS: inner},
		vfs.MountPoint{Prefix: "/", FS: root},
		vfs.MountPoint{Prefix: "/work", FS: work},
	)
	cases := []struct {
		dir       string
		recursive bool
		want      []string
	}{
		{"/", false, []string{"/deep/", "/readme.md", "/work/"}},
		{"/", true, []string{"/deep/er/mnt/z.txt", "/readme.md", "/work/a.txt"}},
		{"/deep", false, []string{"/deep/er/"}},
		{"/deep/er", false, []string{"/deep/er/mnt/"}},
		{"/deep/er/mnt", false, []string{"/deep/er/mnt/z.txt"}},
		{"/deep", true, []string{"/deep/er/mnt/z.txt"}},
		{"/work", false, []string{"/work/a.txt"}},
	}
	for _, tc := range cases {
		got, err := m.List(ctx, tc.dir, tc.recursive)
		if err != nil || !slices.Equal(names(got), tc.want) {
			t.Errorf("List(%s, %v) = %v, %v; want %v", tc.dir, tc.recursive, names(got), err, tc.want)
		}
	}
	if _, err := m.Read(ctx, "/work/shadowed.txt"); !errors.Is(err, vfs.ErrNotFound) {
		t.Errorf("shadowed file readable: %v", err)
	}
	if fi, err := m.Stat(ctx, "/deep"); err != nil || !fi.IsDir {
		t.Errorf("Stat(/deep) = %+v, %v; want the mount ancestor directory", fi, err)
	}
	if _, err := m.Read(ctx, "/deep"); !errors.Is(err, vfs.ErrIsDir) {
		t.Errorf("Read(/deep) err = %v", err)
	}
	if err := m.Write(ctx, "/deep/er", nil); !errors.Is(err, vfs.ErrIsDir) {
		t.Errorf("Write(/deep/er) err = %v", err)
	}
	if err := m.Write(ctx, "/new.md", []byte("n")); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Read(ctx, "/new.md"); err != nil {
		t.Errorf("write did not reach the root FS: %v", err)
	}
}

func TestMountSameFSTwice(t *testing.T) {
	ctx := context.Background()
	shared := memory.New(vfs.Limits{})
	m := mustMount(t,
		vfs.MountPoint{Prefix: "/rw", FS: shared},
		vfs.MountPoint{Prefix: "/ro", FS: shared, ReadOnly: true},
	)
	if err := m.Write(ctx, "/rw/f", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if data, err := m.Read(ctx, "/ro/f"); err != nil || string(data) != "v" {
		t.Fatalf("Read via read-only mount = %q, %v", data, err)
	}
	if err := m.Delete(ctx, "/ro/f"); !errors.Is(err, vfs.ErrReadOnly) {
		t.Fatalf("Delete via read-only mount err = %v", err)
	}
}
