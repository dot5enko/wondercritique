package qjs

import (
	"context"
	"strings"
	"testing"
)

// TestStackLimit guards build/quickjs-wasi-stack.patch: upstream's WASI build had no stack check,
// so runaway recursion overran the shadow stack instead of throwing. MaxStackSize must take
// effect, and 0 must still stop well before the end of the stack.
func TestStackLimit(t *testing.T) {
	depth := func(maxStack int) int64 {
		t.Helper()
		rt, err := New(Option{Context: context.Background(), MaxStackSize: maxStack})
		if err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		v, err := rt.Eval("stack.js", Code(`
			let d = 0; function f() { d++; f() }
			let err; try { f() } catch (e) { err = String(e) }
			if (err !== "RangeError: Maximum call stack size exceeded") throw new Error("got " + err);
			d`))
		if err != nil {
			t.Fatal(err)
		}
		defer v.Free()
		return v.Int64()
	}
	full, small := depth(0), depth(64<<10)
	if full < 2000 || small < 50 || small*4 > full {
		t.Fatalf("depth with MaxStackSize 0 = %d, with 64 KiB = %d: limit not applied", full, small)
	}
}

// TestBridgeStringsDoNotLeak guards the vendor patches in Value.String and Context.NewString:
// upstream leaked every string crossing the bridge. The leaked memory is not counted by
// QuickJS' MemoryLimit, only by the wasm linear memory, so growth is measured there.
func TestBridgeStringsDoNotLeak(t *testing.T) {
	const rounds, size = 100, 256 << 10 // 25 MiB each way if leaking

	rt, err := New(Option{Context: context.Background(), MemoryLimit: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	big := strings.Repeat("z", size)
	received := 0
	free := func(this *This) {
		for _, a := range this.Args() { // Args are clones owned by the callee.
			a.Free()
		}
	}
	rt.Context().SetFunc("send", func(this *This) (*Value, error) {
		defer free(this)
		received += len(this.Args()[0].String())
		return nil, nil
	})
	rt.Context().SetFunc("recv", func(this *This) (*Value, error) {
		defer free(this)
		return this.Context().NewString(big), nil
	})

	before := rt.Mem().Size()
	v, err := rt.Eval("bridge.js", Code(`
		const big = "x".repeat(256 * 1024);
		let got = 0;
		for (let i = 0; i < 100; i++) { send(big + i); got += recv().length }
		got`))
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Int64(); got != rounds*size || received < rounds*size {
		t.Fatalf("got %d / received %d bytes, want %d", got, received, rounds*size)
	}
	if growth := rt.Mem().Size() - before; growth > 8<<20 {
		t.Fatalf("wasm memory grew by %d KiB over %d round trips of %d KiB: bridge strings leak",
			growth>>10, rounds, size>>10)
	}
}
