# Vendored: github.com/fastschema/qjs v0.0.6

QuickJS-NG compiled to WASI (`qjs.wasm`) plus its Go glue, MIT licensed (see `LICENSE`).
Go files and `LICENSE` copied from the module cache on 2026-11-01. `qjs.wasm` is rebuilt here
(2026-11-02) from pinned sources by `build/build.sh`; upstream's binary is not used.

Vendored instead of imported because upstream's `New` cannot be configured to be a sandbox:

1. It mounted the host working directory read-write at `/` in the guest, so `qjs:std` / `qjs:os`
   (always registered by the wasm, also reachable via `import()`) could read and write host files.
2. WASI sleeps used `time.Sleep`, so `os.sleep(1e9)` or a long timer pinned a goroutine and the
   instance's memory past any deadline.
3. `CloseOnContextDone` and the compiled module were frozen process-wide by the first caller, and
   `Close` panicked on an aborted instance and never closed the wazero runtime.
4. Every string crossing the Go/JS boundary leaked guest memory (see the `context.go`/`value.go`/
   `options.go` patches), so a script calling host functions in a loop ran out of memory.
5. Upstream's `qjs.wasm` had no stack-overflow check (compiled out for WASI) and a 64 KiB shadow
   stack above static data, so recursion past ~200 frames corrupted the instance or trapped
   instead of throwing `RangeError`.

## Go patches (every patched spot carries a `vendor patch` comment)

- `runtime.go`: no FS config (no preopens), no env/args/stdin; `WithNanosleep` returns when
  `Option.Context` is done; a process-wide `RuntimeConfig` with a shared compilation cache,
  `WithCloseOnContextDone(true)` and `WithMemoryLimitPages(MaxMemoryPages)`; each runtime compiles
  (decode only, machine code comes from the cache) and instantiates in its own wazero runtime;
  the module is started with `_initialize` (WASI reactor); `Close` closes the wazero runtime
  without calling into the guest; `Pool` removed.
- `options.go`: removed `CWD`, `CloseOnContextDone`, `DisableBuildCache`, `QuickJSWasmBytes`,
  `MaxExecutionTime`, `StartFunctionName`; `MaxStackSize` documented for the rebuilt binary;
  stdout/stderr default to `io.Discard`; `EvalOption.Free` frees the C buffers with `free`
  (upstream passed them to `JS_FreeValue`, a no-op for raw pointers).
- `context.go`: `NewString` and `ParseJSON` free their temporary C buffer.
- `value.go`: `String` releases the `JS_ToCString` result with `JS_FreeCString`.

## qjs.wasm (`build/`)

`build/build.sh` (needs zig 0.15.x; ~6 s) downloads checksummed sources, applies
`build/quickjs-wasi-stack.patch` and writes `qjs.wasm`. The output is reproducible (byte-identical
rebuilds). Sources: quickjs-ng commit `d01ca449` (v0.10.1, upstream's submodule), the C glue and
export list from fastschema/qjs `v0.0.6` (`qjswasm/`), dlmalloc from wasi-libc `wasi-sdk-21`.

Differences from upstream's build:

- The patch turns QuickJS' stack limit on for WASI: the limit is
  `max(stack_top - MaxStackSize, __stack_low + STACK_RESERVE)`, and the default stack size is 0
  (= as deep as the stack allows). Overflow throws `RangeError: Maximum call stack size exceeded`.
- 1 MiB shadow stack (`STACK_SIZE`) at the bottom of linear memory (zig links with
  `--stack-first`), 128 KiB of it reserved for C code between two checks (`STACK_RESERVE`).
  Measured depth: ~3370 plain calls, ~1430 via `map`, ~590 via `sort` comparators. Do not grow the
  stack without re-measuring: wazero's native stack (Go heap, trap at ~50 MB) grows ~30 MiB per
  MiB of guest stack at full depth.
- WASI reactor (`-mexec-model=reactor`): exports `_initialize`, no `_start`.
- wasi-libc's dlmalloc is the first object linked, replacing zig's default emmalloc (which was
  ~20% slower and used ~25% more memory on allocation-heavy scripts). Speed and memory match
  upstream's binary.
- No `wasm-opt` pass (benchmarks match without it).

## Upgrading

Go side: diff upstream against these files, re-apply the patches above, copy the other files.
wasm: bump the pins and checksums in `build/build.sh`, re-check that the patch applies and that
the export list in upstream's `qjswasm.cmake` still matches the Go glue, rebuild. Then run
`go test -race ./sandbox/...` (`TestStackLimit` and `TestRecursion` cover the stack patch).
