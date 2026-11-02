# backend.sandbox — JS sandbox (`run_js`)

**State as of 2026-11-02.** This file describes the code as it is now, not how it got here.

Untrusted JavaScript runs in QuickJS-NG compiled to WebAssembly, executed by wazero (pure Go, no cgo).
Everything is in the `agentkit` module (root `apps/backend/agentkit`):

| Path | Purpose |
|---|---|
| `sandbox/jsrun/jsrun.go` | `Config`, `Runner`, `Request`, `Result`, `Error` kinds, `Run`, `Warmup`, failure classification, security-model doc comment |
| `sandbox/jsrun/prelude.js` | Embedded JS run before every script: strips engine globals, installs `console`/`print`/`fs`, `serialize` for results |
| `sandbox/jsrun/host.go` | Go side of the bridge: capped output sink, `__wc_log`, `__wc_fs` (vfs ops → Node-style errors) |
| `sandbox/jsrun/tool.go` | `NewTool(runner, fsys)` → the `run_js` agent tool; `FormatResult` |
| `sandbox/jsrun/internal/qjs/` | **Vendored and patched** `github.com/fastschema/qjs` v0.0.6 (MIT); `qjs.wasm` is rebuilt by `build/build.sh`; see `VENDOR.md` there |

**Not wired into the app yet.** No agent registers `run_js`, there is no HTTP endpoint (the SkillsEditor "run
script" button has no backend), and nothing calls `Warmup` at startup.

## API

```go
r := jsrun.New(jsrun.Config{})              // zero fields = defaults; safe for concurrent use
res, err := r.Run(ctx, jsrun.Request{
    Code:     "fs.glob('/work/**/*.md').length", // classic script, strict mode, top-level await
    Filename: "script.js",                       // shown in stack traces (default script.js, ≤128 bytes)
    Input:    json.RawMessage(`{"k":1}`),        // optional → global `input`
    FS:       vfs.ReadOnly(ws),                  // optional → global `fs`; nil = no fs object
    Timeout:  2 * time.Second,                   // can only SHORTEN Config.Timeout
})
t := jsrun.NewTool(r, fsys)                 // tool "run_js", input {"code": "..."}
```

- **Result**: `Value json.RawMessage` (nil when the result is undefined or the script failed), `Output string`,
  `OutputTruncated bool` (set only when bytes were actually dropped), `Error *Error{Kind, Message, Stack}`,
  `Duration`.
- **Result value**: the value of the last expression. A promise is awaited.
- **Go errors**: `Run` returns one only in these cases:
  - `ErrInvalidRequest` (wrapped): empty code, code over MaxCodeSize, NUL in code, invalid UTF-8, bad filename,
    invalid `Input` JSON.
  - `ctx.Err()`: the caller's ctx ended, either while waiting for a slot or during the run.
  - The engine failed to start.

  Every script failure is reported in `Result.Error` instead.
- **Error kinds**:

  | Kind | Meaning |
  |---|---|
  | `exception` | Thrown error or rejected promise, including SyntaxError. Has `Stack`. |
  | `timeout` | The run hit its deadline. Message: "did not finish within …". |
  | `memory` | The QuickJS heap hit `MemoryLimit`, or the wasm memory cap was reached. |
  | `crash` | Genuine engine failure: a wasm trap or `InternalError: invalid opcode`. Not expected in practice; deep recursion is an ordinary `exception`. |
  | `exit` | The script called `std.exit` via `import("qjs:std")`. |
  | `result` | The result could not be serialized, or its JSON is larger than `MaxResult`. |

  When the context is done, classification checks the parent first: parent ctx done → Go `ctx.Err()`; otherwise
  the run's own deadline → `timeout`. A deadline wins over the exception it caused (for example an uncaught
  `ETIMEDOUT` from `fs`).
- **`FormatResult`** (the tool output) renders, in order:
  1. `console:\n…` and `[console output truncated at N bytes]`, only when there is output.
  2. One of `result: <json>`, `result: undefined`, or `error (<kind>): <msg>` followed by the stack.
  3. `(<duration>)`.

  An invalid tool input becomes a tool error with the `jsrun: invalid request: ` prefix stripped.
- The tool description is built from the effective Config limits. It states the recursion limits. The fs section is
  included only when `fsys != nil`.

## Limits (`Config`, zero = default)

| Field | Default | Notes |
|---|---|---|
| `Timeout` | 5s | Wall clock, engine startup included. |
| `MemoryLimit` | 64 MiB | QuickJS heap. Clamped to `MaxMemoryLimit` = 96 MiB. |
| `MaxOutput` | 32 KiB | Console output. The rest is dropped, and the cut never splits a UTF-8 character. |
| `MaxResult` | 32 KiB | JSON result. Larger → `result` error. The length is checked before copying out of the guest. |
| `MaxCodeSize` | 256 KiB | Source size. |
| `MaxConcurrent` | 4 | Semaphore. Extra runs wait, and a ctx that ends while waiting returns `ctx.Err()`. |
| `StackLimit` | 32 KiB | Engine stack for JS (≈100 nested calls). Clamped to [32 KiB, 128 KiB]; the minimum is what the prelude needs to serialize a 64-level result. Passed as `qjs.Option.MaxStackSize`. |

- The wasm linear memory is hard-capped at 2048 pages (128 MiB) per instance. `init` sets `qjs.MaxMemoryPages`.
- **Recursion**: sized for util-style code. Exceeding `StackLimit` throws a catchable
  `RangeError: Maximum call stack size exceeded` (→ `exception` if uncaught). The script can catch it and continue,
  and the runner is unaffected. The limit is in bytes, so the depth depends on the frame. Measured (2026-11-02):

  | Pattern | 32 KiB (default) | 128 KiB (max) |
  |---|---|---|
  | plain `f()` | 117 | 478 |
  | function with 3 args + locals | 100 | 407 |
  | tree walk (`for…of` over children) | 95 | 387 |
  | method / getter | 105 | 428 |
  | async/await | 57 | 233 |
  | `.map(f)` callback | 50 | 203 |
  | `.forEach(() => f())` | 35 | 143 |
  | `sort` comparator | 21 | 84 |
  | nested parens / arrays in source | 87 | 366 |
  | nested arrow functions in source | 56 | 237 |
  | `JSON.parse` nesting | ~1900 | 5000+ |

  At 32 KiB these still work: a 60k-element spread or `apply`, sorting 50k elements, `Promise.all` over 2000
  promises, regex replace with callbacks, 30-level `input`, 64-level results, and `console.log` of a 16-level
  object from 60 calls deep. The tool description gives the approximate depth (`approxDepth`: limit/330 calls,
  limit/650 through callbacks). A runaway recursion run allocates ~4 MiB of Go memory, about the same as a
  trivial run (~3.3 MiB).
  Why a rebuild was needed: upstream's `qjs.wasm` compiled QuickJS' stack check out for WASI and had a 64 KiB
  stack above static data, so overflow corrupted the instance at ~200 frames. Our build (`internal/qjs/build/`)
  patches the check back in and links a 256 KiB stack at the bottom of memory (see "Vendored qjs" below).
- **Timings**: the first instance in a process takes ~300 ms (wasm compile). Later runs take ~8 ms because they
  reuse the shared compilation cache. `Runner.Warmup(ctx)` pays the compile cost up front.

## Security model

- **A fresh wazero runtime and QuickJS instance for every run**, discarded afterwards. The engine is treated as
  untrusted, so nothing is shared between runs (globals, prototypes, memory corruption).
- **The guest has no host access**: no WASI preopens (no host files), env, args, stdin or sockets.
  `qjs:std`/`qjs:os` can still be reached through `import()`, but they can only:
  - read clocks;
  - sleep, bounded by the deadline;
  - exit the instance (→ `exit` kind).
- **Deadline**: wazero `CloseOnContextDone` aborts busy loops. WASI sleeps and timers wake up when the ctx is done.
  Pending timers keep a run alive until they fire or the deadline hits.
- **Host surface**: only `__wc_log` and (with `FS`) `__wc_fs`. The prelude captures and deletes both. All traffic
  is JSON text in both directions, because the bridge passes C strings and a raw NUL would truncate them.
- **The prelude removes these globals**: `std`, `os`, `bjson`, `gc`, `scriptArgs`, `QJS_PROXY_VALUE`, and the
  engine's own `print`/`console`. It then redefines `console` and `print`.
- **Filesystem**: `fs` sees only the `vfs.FS` it is given. That FS enforces its own path rules, size limits,
  quotas and read-only mounts. Wrap it with `vfs.ReadOnly` to forbid writes.

## Script-side API

- **`console`**:
  - `log`/`info`/`debug`/`warn`/`error`/`dir`/`assert`/`trace`/`table`. Each call prints one line.
  - `warn` and `error` lines get a `[warn] ` / `[error] ` prefix.
  - Objects print as JSON, nested up to 16 levels. An `Error` prints as `String(e)` plus its stack.
  - `console.assert(false, …)` prints `[error] Assertion failed: …`.
  - `print` is the same as `console.log`.
  - Once the output is full, further calls are skipped.
- **`input`**: `Request.Input` parsed into a value.
- **Result serialization** (`toPlain`, depth cap 64):
  - bigint → `"123n"`; NaN and Infinity → strings; functions → `"[Function: name]"`.
  - Map → object; Set and typed arrays → arrays; Error → `{name, message}`.
  - Cycles → `"[Circular]"`; anything deeper than the cap → `"[Object]"`/`"[Array]"`.
  - `toJSON` is honored.
  - The walk uses loops, not callbacks, to spare the stack.
- **`fs`** (frozen object). Paths without a leading `/` are treated as absolute:

  | Call | Returns |
  |---|---|
  | `read(path)` | string. Binary content → `EBINARY`. |
  | `write(path, text)` | Text must be a string, otherwise TypeError. |
  | `readJSON(path)` | Parsed value. Bad JSON → SyntaxError `fs.readJSON <path>: …`. |
  | `writeJSON(path, v, indent=2)` | Writes the JSON plus a trailing newline. |
  | `list(dir="/", {recursive})` | `[{path, isDir, size, version?, modTime?}]` (`modTime` is RFC3339Nano UTC) |
  | `glob(pattern)` | `[path]`, doublestar via `vfs.Glob`. More than 10000 matches → `E2BIG`. |
  | `stat(path)` | Info, or `null` when missing. |
  | `exists(path)` | bool |
  | `delete(path)` | Deletes a single file. |

  Failures throw an `Error` with a `.code` and a Node-style message such as
  `ENOENT: no such file or directory, read '/work/x.md'`.

  | Code | vfs error / cause |
  |---|---|
  | `ENOENT` | `ErrNotFound` |
  | `EISDIR` | `ErrIsDir` |
  | `ENOTDIR` | `ErrNotDir` |
  | `EROFS` | `ErrReadOnly` |
  | `EFBIG` | `ErrTooLarge` |
  | `EDQUOT` | `ErrQuota` |
  | `EINVAL` | `ErrInvalidPath`, `ErrBadPattern`, or an unknown op |
  | `ETIMEDOUT` | Deadline reached |
  | `ECANCELED` | Run canceled |
  | `EIO` | Any other error |

## Vendored qjs (`internal/qjs`)

The package is vendored because upstream cannot be configured as a sandbox. The full list is in `VENDOR.md`, and
every patched spot carries a `vendor patch` comment. Summary:

- `runtime.go`:
  - No preopens, env, args or stdin. Upstream mounted the host CWD read-write.
  - ctx-aware nanosleep. Upstream used `time.Sleep`, which outlived deadlines.
  - A process-wide `RuntimeConfig` with a shared compilation cache, `CloseOnContextDone` and the memory-page cap.
  - One wazero runtime per instance.
  - `Close` never calls into the guest.
  - `Pool` removed.
- `options.go`: sandbox-hostile options removed. stdout/stderr default to `io.Discard`. `EvalOption.Free` really
  frees its C buffers.
- **Leak fixes**:
  - `context.go`: `NewString`/`ParseJSON` free their C buffer.
  - `value.go`: `String` calls `JS_FreeCString`.

  Upstream leaked guest memory on every string crossing the bridge.
- Leaked bridge memory is not counted by QuickJS `MemoryLimit`, only by wasm linear memory. The leak tests
  therefore measure `rt.Mem().Size()`, using the test hook `testHookMemory` in `jsrun.go`.
- **Host-function rule**: `this.Args()` returns clones, and every host function must `Free()` them. `hostArgs` in
  `host.go` does this. Without it, 100 calls with 256 KiB arguments leak about 27 MB.
- **`qjs.wasm` is rebuilt** by `build/build.sh` (zig 0.15.x, ~6 s, byte-for-byte reproducible). Inputs are pinned
  and checksummed: quickjs-ng `d01ca449` (v0.10.1), the fastschema/qjs `v0.0.6` C glue and export list, and wasi-libc
  `wasi-sdk-21` dlmalloc. Differences from upstream's binary:
  - `build/quickjs-wasi-stack.patch`: stack limit = `max(stack_top - MaxStackSize, __stack_low + STACK_RESERVE)`,
    with default stack size 0 (= as deep as the stack allows). `qjs.Option.MaxStackSize` works, but can only lower it.
  - `STACK_SIZE` 256 KiB at the bottom of linear memory (stack-first), of which `STACK_RESERVE` 128 KiB is kept for C
    code that runs between checks, so JS gets at most 128 KiB (= `jsrun.MaxStackLimit`). The stack is part of every
    instance's memory: 1 MiB cost ~1.1 MiB more Go allocation per run. Full-depth recursion costs ~30 MiB of
    transient Go memory per MiB of guest stack (wazero's native stack, which traps at ~50 MB).
  - WASI reactor: `runtime.go` starts the module with `_initialize`, and `Option.StartFunctionName` was removed.
  - dlmalloc is linked first, replacing zig's emmalloc (~20% slower, ~25% more memory). Speed and memory match
    upstream's binary. No `wasm-opt` pass.
- **Known engine quirks**, documented rather than fixed:
  - When the completion value is an `Error` object, it is reported as an exception (qjs `normalizeJsValue`).
  - Integer-like object keys sort first in results.

## Tests

From `apps/backend/agentkit` run `go test -race ./sandbox/...`. This takes ~25 s with `-race` and ~7 s without.

- `jsrun_test.go` covers:
  - values, depth caps, globals, no host access;
  - console, output truncation, result limit;
  - exceptions, timeouts (loops, sleeps, timers, never-settling promises), memory, recursion (default depth ~100, `StackLimit` raise and clamps, RangeError for every
    pattern, catchable, runner stays usable), exit;
  - input, invalid requests, caller ctx;
  - concurrency;
  - host-call leak (`TestHostCallsDoNotLeak`);
  - warmup.
- `fs_test.go`: read/write, error codes, read-only and quota, absent `fs`, fs calls honoring the deadline.
- `tool_test.go`: tool spec, end-to-end output, `FormatResult`.
- `internal/qjs/bridge_test.go`: `TestStackLimit` (stack patch: RangeError, `MaxStackSize` applies) and
  `TestBridgeStringsDoNotLeak`. Each fails when its patch is reverted.
