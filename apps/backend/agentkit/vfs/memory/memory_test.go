package memory_test

import (
	"context"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs/memory"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs/vfstest"
)

func TestConformance(t *testing.T) {
	vfstest.Run(t, func(_ *testing.T, l vfs.Limits) vfs.FS { return memory.New(l) })
}

func TestUsage(t *testing.T) {
	ctx := context.Background()
	m := memory.New(vfs.Limits{})
	_ = m.Write(ctx, "/a", []byte("12345"))
	_ = m.Write(ctx, "/b/c", []byte("12"))
	_ = m.Write(ctx, "/a", []byte("1"))
	if u := m.Usage(); u.Files != 2 || u.Bytes != 3 {
		t.Fatalf("Usage = %+v, want 2 files / 3 bytes", u)
	}
	_ = m.Delete(ctx, "/b/c")
	if u := m.Usage(); u.Files != 1 || u.Bytes != 1 {
		t.Fatalf("Usage after delete = %+v", u)
	}
}
