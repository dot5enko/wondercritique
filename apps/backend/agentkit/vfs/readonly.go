package vfs

import "context"

// ReadOnly returns a view of fsys whose Write and Delete fail with ErrReadOnly (after path
// validation, so invalid paths still yield ErrInvalidPath). Reads pass through unchanged.
func ReadOnly(fsys FS) FS {
	if ro, ok := fsys.(readOnly); ok {
		return ro
	}
	return readOnly{fsys}
}

type readOnly struct{ fs FS }

func (r readOnly) Stat(ctx context.Context, path string) (FileInfo, error) {
	return r.fs.Stat(ctx, path)
}

func (r readOnly) Read(ctx context.Context, path string) ([]byte, error) {
	return r.fs.Read(ctx, path)
}

func (r readOnly) List(ctx context.Context, dir string, recursive bool) ([]FileInfo, error) {
	return r.fs.List(ctx, dir, recursive)
}

func (r readOnly) Write(_ context.Context, path string, _ []byte) error {
	if _, err := Clean(path); err != nil {
		return err
	}
	return ErrReadOnly
}

func (r readOnly) Delete(_ context.Context, path string) error {
	if _, err := Clean(path); err != nil {
		return err
	}
	return ErrReadOnly
}
