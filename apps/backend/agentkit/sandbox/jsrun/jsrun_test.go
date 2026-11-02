package jsrun

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs/memory"
)

func run(t *testing.T, r *Runner, req Request) *Result {
	t.Helper()
	res, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func runCode(t *testing.T, code string) *Result {
	t.Helper()
	return run(t, New(Config{}), Request{Code: code})
}

func wantValue(t *testing.T, res *Result, want string) {
	t.Helper()
	if res.Error != nil {
		t.Fatalf("unexpected error: %+v (output %q)", res.Error, res.Output)
	}
	if string(res.Value) != want {
		t.Fatalf("value = %s, want %s", res.Value, want)
	}
}

func wantError(t *testing.T, res *Result, kind ErrorKind, msgPart string) {
	t.Helper()
	if res.Error == nil {
		t.Fatalf("want %s error, got value %s (output %q)", kind, res.Value, res.Output)
	}
	if res.Error.Kind != kind || !strings.Contains(res.Error.Message, msgPart) {
		t.Fatalf("error = %+v, want kind %s containing %q", res.Error, kind, msgPart)
	}
	if res.Value != nil {
		t.Fatalf("value = %s, want none on error", res.Value)
	}
}

func TestValues(t *testing.T) {
	r := New(Config{})
	cases := []struct{ code, want string }{
		{`1 + 2`, `3`},
		{`"a" + "b"`, `"ab"`},
		{`({a: [1, 2, {b: null}], s: "x\u0000y"})`, `{"a":[1,2,{"b":null}],"s":"x\u0000y"}`},
		{`[undefined, () => 1, function named() {}, Symbol("s")]`, `[null,"[Function (anonymous)]","[Function: named]","Symbol(s)"]`},
		{`({u: undefined, n: NaN, i: -Infinity, b: 12n})`, `{"n":"NaN","i":"-Infinity","b":"12n"}`},
		{`new Map([["k", 1], ["z", new Set([1, 2])]])`, `{"k":1,"z":[1,2]}`},
		{`[new TypeError("bad")]`, `[{"name":"TypeError","message":"bad"}]`},
		{`new Uint8Array([1, 2, 3])`, `[1,2,3]`},
		{`new Date(0)`, `"1970-01-01T00:00:00.000Z"`},
		{`const o = {name: "o"}; o.self = o; o.list = [o]; o`, `{"name":"o","self":"[Circular]","list":["[Circular]"]}`},
		{`const shared = {v: 1}; ({a: shared, b: shared})`, `{"a":{"v":1},"b":{"v":1}}`},
		{`null`, `null`},
		{`let x = 1; x + 41`, `42`},
		{`const v = await new Promise(r => setTimeout(() => r(7), 5)); v * 6`, `42`},
		{`async function main() { await null; return {ok: true} } main()`, `{"ok":true}`},
		{`"é€😀"`, `"é€😀"`},
	}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			wantValue(t, run(t, r, Request{Code: c.code}), c.want)
		})
	}

	for _, code := range []string{`let x = 1;`, `undefined`, `let y = await null;`} {
		res := run(t, r, Request{Code: code})
		if res.Error != nil || res.Value != nil {
			t.Fatalf("%s: got value %s error %+v, want no value", code, res.Value, res.Error)
		}
	}

	// Engine quirk: an Error object as the completion value is reported as an exception.
	wantError(t, run(t, r, Request{Code: `new TypeError("bad")`}), KindException, "TypeError: bad")
}

func TestDeepValueIsCapped(t *testing.T) {
	res := runCode(t, `let v = {}; const root = v; for (let i = 0; i < 1000; i++) { v.c = {}; v = v.c } root`)
	if res.Error != nil {
		t.Fatalf("unexpected error: %+v", res.Error)
	}
	if !strings.Contains(string(res.Value), `"[Object]"`) || strings.Count(string(res.Value), `"c"`) != 64 {
		t.Fatalf("value not capped at depth 64: %.200s", res.Value)
	}

	// Console arguments are capped at depth 16, so logging deep inside user recursion leaves
	// most of the engine's stack to the user's code.
	res = runCode(t, `let v = {}; const root = v; for (let i = 0; i < 1000; i++) { v.c = {}; v = v.c }
		function deep(n) { if (n === 0) { console.log(root); return 0 } return 1 + deep(n - 1) }
		deep(60)`)
	wantValue(t, res, `60`)
	if !strings.Contains(res.Output, `"[Object]"`) || strings.Count(res.Output, `"c"`) != 16 {
		t.Fatalf("console output not capped at depth 16: %.200s", res.Output)
	}
}

func TestSandboxGlobals(t *testing.T) {
	res := runCode(t, `[typeof std, typeof os, typeof bjson, typeof gc, typeof scriptArgs, typeof __wc_log,
		typeof __wc_fs, typeof QJS_PROXY_VALUE, typeof fs, typeof input, typeof console.log, print === console.log]`)
	wantValue(t, res, `["undefined","undefined","undefined","undefined","undefined","undefined","undefined","undefined","undefined","undefined","function",true]`)
}

func TestNoHostAccess(t *testing.T) {
	// qjs:std / qjs:os stay reachable via import(), but the guest has no preopened directories,
	// no environment and no exec.
	res := runCode(t, `
		const std = await import("qjs:std");
		const os = await import("qjs:os");
		[std.loadFile("/etc/passwd"), std.loadFile("go.mod"), std.getenv("HOME"), std.getenv("PATH"),
		 os.readdir("/")[1], os.open("/x.txt", os.O_WRONLY | os.O_CREAT, 0o644) < 0,
		 typeof os.exec, typeof std.urlGet]`)
	wantValue(t, res, `[null,null,null,null,44,true,"undefined","undefined"]`)
}

func TestConsole(t *testing.T) {
	res := runCode(t, `
		console.log("hello", 1, {a: [1, "x"]}, [1, 2], null, undefined, 3n, true);
		console.info("info"); console.debug("debug");
		console.warn("careful"); console.error(new RangeError("nope"));
		print("printed");
		console.assert(1 === 2, "math");
		console.assert(true, "silent");
		"done"`)
	wantValue(t, res, `"done"`)
	want := strings.Join([]string{
		`hello 1 {"a":[1,"x"]} [1,2] null undefined 3n true`,
		`info`,
		`debug`,
		`[warn] careful`,
		`[error] RangeError: nope`,
		`    at <eval> (script.js:4:46)`,
		`printed`,
		`[error] Assertion failed: math`,
		``,
	}, "\n")
	if res.Output != want {
		t.Fatalf("output:\n%s\nwant:\n%s", res.Output, want)
	}
	if res.OutputTruncated {
		t.Fatal("output reported truncated")
	}
}

func TestOutputTruncation(t *testing.T) {
	r := New(Config{MaxOutput: 100})
	res := run(t, r, Request{Code: `for (let i = 0; i < 100000; i++) console.log("line " + i); "end"`})
	wantValue(t, res, `"end"`)
	if !res.OutputTruncated || len(res.Output) != 100 || !strings.HasPrefix(res.Output, "line 0\nline 1\n") {
		t.Fatalf("output = %q (%d bytes), truncated %v", res.Output, len(res.Output), res.OutputTruncated)
	}

	// A multi-byte character cut by the cap is dropped entirely.
	r = New(Config{MaxOutput: 10})
	res = run(t, r, Request{Code: `console.log("aaaaaaaa€"); 1`})
	if res.Output != "aaaaaaaa" || !res.OutputTruncated {
		t.Fatalf("output = %q, truncated %v", res.Output, res.OutputTruncated)
	}

	// Exactly filling the cap is not truncation; the next line is.
	r = New(Config{MaxOutput: 4})
	res = run(t, r, Request{Code: `console.log("abc"); 1`})
	if res.Output != "abc\n" || res.OutputTruncated {
		t.Fatalf("output = %q, truncated %v", res.Output, res.OutputTruncated)
	}
	res = run(t, r, Request{Code: `console.log("abc"); console.log("d"); 1`})
	if res.Output != "abc\n" || !res.OutputTruncated {
		t.Fatalf("output = %q, truncated %v", res.Output, res.OutputTruncated)
	}
}

func TestResultLimit(t *testing.T) {
	r := New(Config{MaxResult: 64})
	wantValue(t, run(t, r, Request{Code: `"x".repeat(62)`}), `"`+strings.Repeat("x", 62)+`"`)
	wantError(t, run(t, r, Request{Code: `"x".repeat(63)`}), KindResult, "over the 64-byte limit")
	wantError(t, run(t, r, Request{Code: `"x".repeat(1e7)`}), KindResult, "over the 64-byte limit")
	// Multi-byte: 30 × 3 bytes = 90 bytes although only 32 UTF-16 units.
	wantError(t, run(t, r, Request{Code: `"€".repeat(30)`}), KindResult, "over the 64-byte limit")

	res := runCode(t, `({get boom() { throw new Error("getter failed") }})`)
	wantError(t, res, KindResult, "could not serialize the result: Error: getter failed")
}

func TestExceptions(t *testing.T) {
	res := runCode(t, `console.log("before"); function f() { throw new TypeError("bad thing") }
f()`)
	wantError(t, res, KindException, "TypeError: bad thing")
	if res.Error.Message != "TypeError: bad thing" || !strings.Contains(res.Error.Stack, "at f (script.js:1:") {
		t.Fatalf("error = %+v", res.Error)
	}
	if res.Output != "before\n" {
		t.Fatalf("output = %q, want console output kept on error", res.Output)
	}

	wantError(t, runCode(t, `let = ;`), KindException, "SyntaxError")
	wantError(t, runCode(t, `await Promise.reject(new Error("boom"))`), KindException, "Error: boom")
	wantError(t, runCode(t, `async function f() { throw new RangeError("late") } f()`), KindException, "RangeError: late")
	wantError(t, runCode(t, `throw "plain string"`), KindException, "plain string")
	wantError(t, runCode(t, `undeclared = 1`), KindException, "ReferenceError")

	res = run(t, New(Config{}), Request{Code: "\n\nnull.x", Filename: "skill/check.js"})
	wantError(t, res, KindException, "TypeError")
	if !strings.Contains(res.Error.Stack, "skill/check.js:3") {
		t.Fatalf("stack = %q, want custom filename", res.Error.Stack)
	}
}

func TestTimeouts(t *testing.T) {
	r := New(Config{Timeout: 300 * time.Millisecond})
	for _, code := range []string{
		`for (;;) {}`,
		`console.log("spin"); while (true) { Math.random() }`,
		`await new Promise(() => {})`,
		`await new Promise(r => setTimeout(r, 60000))`,
		`setTimeout(() => {}, 60000); 1`,
		`const os = await import("qjs:os"); os.sleep(60000); 1`,
		`async function f() { for (;;) await null } f()`,
	} {
		t.Run(code, func(t *testing.T) {
			start := time.Now()
			res := run(t, r, Request{Code: code})
			wantError(t, res, KindTimeout, "did not finish within 300ms")
			if el := time.Since(start); el > 2*time.Second {
				t.Fatalf("took %v to stop", el)
			}
		})
	}

	res := run(t, r, Request{Code: `console.log("partial"); for (;;) {}`})
	if res.Output != "partial\n" {
		t.Fatalf("output = %q, want output before the timeout kept", res.Output)
	}

	// Request.Timeout can only shorten the configured timeout.
	res = run(t, r, Request{Code: `for (;;) {}`, Timeout: 50 * time.Millisecond})
	wantError(t, res, KindTimeout, "within 50ms")
	res = run(t, r, Request{Code: `for (;;) {}`, Timeout: time.Hour})
	wantError(t, res, KindTimeout, "within 300ms")
}

func TestMemoryLimit(t *testing.T) {
	r := New(Config{MemoryLimit: 16 << 20})
	wantError(t, run(t, r, Request{Code: `const a = []; for (;;) a.push(new ArrayBuffer(1 << 20))`}), KindMemory, "16 MiB")
	wantError(t, run(t, r, Request{Code: `let s = "x"; for (;;) s = s + s`}), KindMemory, "16 MiB")

	// A caught OOM leaves the script running.
	res := run(t, r, Request{Code: `let n = 0; try { const a = []; for (;;) { a.push(new ArrayBuffer(1 << 20)); n++ } } catch (e) { String(e) }`})
	wantValue(t, res, `"InternalError: out of memory"`)

	// Config clamps the limit.
	if got := New(Config{MemoryLimit: 1 << 30}).Config().MemoryLimit; got != MaxMemoryLimit {
		t.Fatalf("MemoryLimit = %d, want clamp to %d", got, MaxMemoryLimit)
	}
}

func TestRecursion(t *testing.T) {
	r := New(Config{})
	// The default limit fits util-sized recursion, directly and through native callbacks...
	wantValue(t, run(t, r, Request{Code: `function f(n) { return n === 0 ? 0 : 1 + f(n - 1) } f(90)`}), `90`)
	wantValue(t, run(t, r, Request{Code: `function f(n) { return n === 0 ? 0 : [n - 1].map(f)[0] + 1 } f(40)`}), `40`)
	wantValue(t, run(t, r, Request{Code: `function f(n) { return n === 0 ? 0 : [n].map(() => 1 + f(n - 1))[0] } f(25)`}), `25`)
	// ...and not much more.
	const depth = `let d = 0; function r() { d++; r() } try { r() } catch (e) {} d`
	res := run(t, r, Request{Code: depth})
	if res.Error != nil || len(res.Value) == 0 {
		t.Fatalf("depth probe: %+v", res.Error)
	}
	var d int
	if err := json.Unmarshal(res.Value, &d); err != nil || d < 90 || d > 150 {
		t.Fatalf("default max depth = %s, want ~100", res.Value)
	}
	// StackLimit raises it, up to MaxStackLimit, and is clamped at both ends.
	wantValue(t, run(t, New(Config{StackLimit: MaxStackLimit}), Request{Code: `function f(n) { return n === 0 ? 0 : 1 + f(n - 1) } f(300)`}), `300`)
	for in, want := range map[int]int{0: DefaultStackLimit, 1: MinStackLimit, 1 << 30: MaxStackLimit} {
		if got := New(Config{StackLimit: in}).Config().StackLimit; got != want {
			t.Fatalf("StackLimit %d → %d, want %d", in, got, want)
		}
	}

	// Runaway recursion throws an ordinary RangeError, however it recurses.
	const overflow = "RangeError: Maximum call stack size exceeded"
	for _, code := range []string{
		`function r() { r() } r()`,
		`const r = (a) => a.map(() => r(a)); r([1])`,
		`function r() { [2, 1].sort(() => { r(); return 0 }) } r()`,
		`const o = { get x() { return o.x } }; o.x`,
		`const p = new Proxy({}, { get(t, k) { return p[k] } }); p.x`,
		`const o = { toString() { return String(o) } }; String(o)`,
		`async function r() { await r() } await r()`,
		`JSON.parse("[".repeat(100000))`,
		`eval("(".repeat(100000) + "1" + ")".repeat(100000))`,
	} {
		wantError(t, run(t, r, Request{Code: code}), KindException, overflow)
	}

	// It can be caught, and the script carries on.
	res = run(t, r, Request{Code: `let d = 0; function r() { d++; r() }
		let msg; try { r() } catch (e) { msg = String(e) }
		[d > 50, msg, [1, 2, 3].map(x => x * 2)]`})
	wantValue(t, res, `[true,"`+overflow+`",[2,4,6]]`)

	// The runner stays usable afterwards.
	wantValue(t, run(t, r, Request{Code: `1`}), `1`)
}

func TestExit(t *testing.T) {
	res := runCode(t, `console.log("bye"); const std = await import("qjs:std"); std.exit(3)`)
	wantError(t, res, KindExit, "code 3")
	if res.Output != "bye\n" {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestInput(t *testing.T) {
	r := New(Config{})
	res := run(t, r, Request{Code: `input.items.map(x => x * 2)`, Input: json.RawMessage(`{"items":[1,2,3]}`)})
	wantValue(t, res, `[2,4,6]`)
	res = run(t, r, Request{Code: `input`, Input: json.RawMessage(`"nul \u0000 inside"`)})
	wantValue(t, res, `"nul \u0000 inside"`)
	if _, err := r.Run(context.Background(), Request{Code: `1`, Input: json.RawMessage(`{bad`)}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestInvalidRequests(t *testing.T) {
	r := New(Config{MaxCodeSize: 32})
	for name, req := range map[string]Request{
		"empty":    {Code: "  \n"},
		"too big":  {Code: strings.Repeat("1;", 20)},
		"nul":      {Code: "1\x00"},
		"utf8":     {Code: "'\xff'"},
		"filename": {Code: "1", Filename: "a\nb"},
	} {
		if _, err := r.Run(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

func TestCallerContext(t *testing.T) {
	r := New(Config{Timeout: 10 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Run(ctx, Request{Code: `1`}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	if _, err := r.Run(ctx, Request{Code: `for (;;) {}`}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("took %v to stop after cancel", el)
	}

	// A caller deadline is the caller's error, not a script timeout.
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := r.Run(ctx, Request{Code: `for (;;) {}`}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestConcurrency(t *testing.T) {
	r := New(Config{MaxConcurrent: 2, Timeout: 2 * time.Second})

	// Both slots busy: a third run waits, and gives up with its context.
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { <-release; cancel() }()
			_, _ = r.Run(ctx, Request{Code: `for (;;) {}`})
		})
	}
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := r.Run(ctx, Request{Code: `1`}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want to wait for a slot and time out", err)
	}
	close(release)
	wg.Wait()

	// Many parallel runs, each isolated from the others.
	var mu sync.Mutex
	errs := []string{}
	for i := range 8 {
		wg.Go(func() {
			res, err := r.Run(context.Background(), Request{
				Code:  `globalThis.counter = (globalThis.counter || 0) + 1; [input, counter]`,
				Input: json.RawMessage(strings.Repeat("1", i+1)),
			})
			mu.Lock()
			defer mu.Unlock()
			want := `[` + strings.Repeat("1", i+1) + `,1]`
			if err != nil || res.Error != nil || string(res.Value) != want {
				errs = append(errs, want)
			}
		})
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("runs failed or shared state: %v", errs)
	}
}

func TestHostCallsDoNotLeak(t *testing.T) {
	// Host-call arguments are clones the callee must free; leaked strings grow the wasm memory
	// (QuickJS' MemoryLimit does not see them). 100 × 256 KiB round trips would add ~50 MiB.
	var memSize uint32
	testHookMemory = func(n uint32) { memSize = n }
	defer func() { testHookMemory = nil }()

	fsys := memory.New(vfs.Limits{})
	r := New(Config{Timeout: 30 * time.Second})
	res := run(t, r, Request{FS: fsys, Code: `
		const big = "x".repeat(256 * 1024);
		for (let i = 0; i < 100; i++) {
			fs.write("/big.txt", big + i);
			if (fs.read("/big.txt").length !== big.length + String(i).length) throw new Error("mismatch");
			console.log(big.slice(0, 10));
		}
		"ok"`})
	wantValue(t, res, `"ok"`)
	if memSize == 0 || memSize > 16<<20 {
		t.Fatalf("wasm memory after the run = %d KiB, want < 16 MiB (host strings leak?)", memSize>>10)
	}
	t.Logf("wasm memory after 100 round trips of 256 KiB: %d KiB", memSize>>10)
}

func TestWarmup(t *testing.T) {
	if err := New(Config{}).Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
}
