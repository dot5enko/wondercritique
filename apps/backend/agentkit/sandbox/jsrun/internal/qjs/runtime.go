package qjs

import (
	"context"
	_ "embed"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	wsp1 "github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed qjs.wasm
var wasmBytes []byte

// MaxMemoryPages caps the wasm linear memory of every runtime, in 64 KiB pages (default 4096 =
// 256 MiB). QuickJS' own MemoryLimit only counts the JS heap; this is the hard ceiling for
// everything inside the instance. It is read when the shared runtime config is first built, so
// it must be set before the first New call.
var MaxMemoryPages uint32 = 4096

var (
	sharedConfig   wazero.RuntimeConfig
	sharedConfigMu sync.Mutex
)

// runtimeConfig returns the process-wide runtime config. All runtimes share one in-memory
// compilation cache, so the QuickJS module is machine-compiled once per process and every later
// CompileModule only decodes it. (vendor patch: upstream compiled once in a throwaway runtime and
// instantiated that CompiledModule in other runtimes, and froze CloseOnContextDone at the value of
// the first caller.)
func runtimeConfig(cacheDir string) (wazero.RuntimeConfig, error) {
	sharedConfigMu.Lock()
	defer sharedConfigMu.Unlock()

	if sharedConfig != nil {
		return sharedConfig, nil
	}

	var cache wazero.CompilationCache
	if cacheDir == "" {
		cache = wazero.NewCompilationCache()
	} else {
		var err error
		if cache, err = wazero.NewCompilationCacheWithDir(cacheDir); err != nil {
			return nil, fmt.Errorf("failed to create compilation cache with dir %s: %w", cacheDir, err)
		}
	}

	sharedConfig = wazero.NewRuntimeConfig().
		WithCompilationCache(cache).
		// Always on: a done context aborts running wasm (infinite loops included).
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(MaxMemoryPages)

	return sharedConfig, nil
}

// Runtime wraps a QuickJS WebAssembly runtime with memory management.
type Runtime struct {
	wrt      wazero.Runtime
	module   api.Module
	malloc   api.Function
	free     api.Function
	mem      *Mem
	option   Option
	handle   *Handle
	context  *Context
	registry *ProxyRegistry
}

// New creates a QuickJS runtime with optional configuration.
//
// Vendor patches (sandboxing): the guest gets no preopened directories (upstream mounted the
// process working directory read-write at "/"), no environment, no arguments and no stdin; sleeps
// (WASI poll_oneoff clock waits, used by os.sleep and the timer loop) end when Option.Context is
// done instead of blocking the goroutine for the requested duration.
func New(options ...Option) (runtime *Runtime, err error) {
	defer func() {
		rerr := AnyToError(recover())
		if rerr != nil {
			if runtime != nil {
				runtime.Close()
			}
			runtime = nil
			err = fmt.Errorf("failed to create QJS runtime: %w", rerr)
		}
	}()

	proxyRegistry := NewProxyRegistry()

	option, err := getRuntimeOption(proxyRegistry, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get runtime options: %w", err)
	}

	cfg, err := runtimeConfig(option.CacheDir)
	if err != nil {
		return nil, err
	}

	ctx := option.Context
	runtime = &Runtime{
		option:   option,
		context:  &Context{Context: ctx},
		registry: proxyRegistry,
	}

	runtime.wrt = wazero.NewRuntimeWithConfig(ctx, cfg)

	if _, err := wsp1.Instantiate(ctx, runtime.wrt); err != nil {
		runtime.Close()
		return nil, fmt.Errorf("failed to instantiate WASI: %w", err)
	}

	if _, err := runtime.wrt.NewHostModuleBuilder("env").
		NewFunctionBuilder().
		WithFunc(option.ProxyFunction).
		Export("jsFunctionProxy").
		Instantiate(ctx); err != nil {
		runtime.Close()
		return nil, fmt.Errorf("failed to setup host module: %w", err)
	}

	compiled, err := runtime.wrt.CompileModule(ctx, wasmBytes)
	if err != nil {
		runtime.Close()
		return nil, fmt.Errorf("failed to compile qjs module: %w", err)
	}

	if runtime.module, err = runtime.wrt.InstantiateModule(
		ctx,
		compiled,
		wazero.NewModuleConfig().
			// vendor patch: qjs.wasm is built as a WASI reactor (see build/); _initialize runs
			// the C constructors once.
			WithStartFunctions("_initialize").
			WithSysWalltime().
			WithSysNanotime().
			WithNanosleep(func(ns int64) { sleepCtx(ctx, ns) }).
			WithStdout(option.Stdout).
			WithStderr(option.Stderr),
	); err != nil {
		runtime.Close()
		return nil, fmt.Errorf("failed to instantiate module: %w", err)
	}

	runtime.initializeRuntime()

	return runtime, nil
}

// sleepCtx sleeps for ns nanoseconds or until ctx is done, whichever comes first.
func sleepCtx(ctx context.Context, ns int64) {
	if ns <= 0 {
		return
	}

	t := time.NewTimer(time.Duration(ns))
	defer t.Stop()

	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func (r *Runtime) Raw() uint64 {
	return r.handle.raw
}

// Mem returns the WebAssembly memory interface for this runtime.
func (r *Runtime) Mem() *Mem {
	return r.mem
}

// String returns a string representation of the runtime.
func (r *Runtime) String() string {
	return fmt.Sprintf("QJSRuntime: %p", r)
}

// Close shuts the runtime down and releases everything it holds. It never calls into the guest
// (vendor patch: upstream freed the QuickJS runtime first, which panics after the module was
// aborted, and never closed the wazero runtime): closing the wazero runtime discards the whole
// instance, including all QuickJS memory. Safe to call more than once.
func (r *Runtime) Close() {
	if r == nil {
		return
	}

	if r.wrt != nil {
		_ = r.wrt.Close(context.Background())
		r.wrt = nil
	}

	if r.registry != nil {
		r.registry.Clear()
		r.registry = nil
	}

	r.module = nil
	r.handle = nil
	r.context = nil
	r.malloc = nil
	r.free = nil
	r.mem = nil
}

// Load executes a JavaScript file in the runtime's context.
func (r *Runtime) Load(file string, flags ...EvalOptionFunc) (*Value, error) {
	return r.context.Load(file, flags...)
}

// Eval executes JavaScript code in the runtime's context.
func (r *Runtime) Eval(file string, flags ...EvalOptionFunc) (*Value, error) {
	return r.context.Eval(file, flags...)
}

// Compile compiles JavaScript code to bytecode without executing it.
func (r *Runtime) Compile(file string, flags ...EvalOptionFunc) ([]byte, error) {
	return r.context.Compile(file, flags...)
}

// Context returns the JavaScript execution context for this runtime.
func (r *Runtime) Context() *Context {
	return r.context
}

// Call invokes a WebAssembly function by name with the given arguments.
func (r *Runtime) Call(name string, args ...uint64) *Handle {
	return NewHandle(r, r.call(name, args...))
}

// CallUnPack calls a WebAssembly function and unpacks the returned pointer.
func (r *Runtime) CallUnPack(name string, args ...uint64) (uint32, uint32) {
	return r.mem.UnpackPtr(r.Call(name, args...).raw)
}

// Malloc allocates memory in the WebAssembly linear memory and return a pointer to it.
func (r *Runtime) Malloc(size uint64) uint64 {
	ptrs, err := r.malloc.Call(r.context, size)
	if err != nil {
		panic(fmt.Errorf("failed to allocate memory: %w", err))
	}

	return ptrs[0]
}

// FreeHandle releases memory allocated in WebAssembly linear memory.
func (r *Runtime) FreeHandle(ptr uint64) {
	if _, err := r.free.Call(r.context, ptr); err != nil {
		panic(fmt.Errorf("failed to free memory: %w", err))
	}
}

// FreeJsValue frees a JavaScript value in the QuickJS runtime.
func (r *Runtime) FreeJsValue(val uint64) {
	r.Call("QJS_FreeValue", r.context.Raw(), val)
}

// NewBytesHandle creates a handle for byte data in WebAssembly memory.
func (r *Runtime) NewBytesHandle(b []byte) *Handle {
	if len(b) == 0 {
		return nil
	}

	ptr := r.Malloc(uint64(len(b)))
	r.mem.MustWrite(uint32(ptr), b)

	return NewHandle(r, ptr)
}

// NewStringHandle creates a handle for string data with null termination.
func (r *Runtime) NewStringHandle(v string) *Handle {
	// Allocate len+1 for null terminator
	ptr := r.Malloc(uint64(len(v) + 1))

	// Write string data
	r.mem.MustWrite(uint32(ptr), []byte(v))
	// Add null terminator
	r.mem.MustWrite(uint32(ptr)+uint32(len(v)), []byte{0})

	return NewHandle(r, ptr)
}

// initializeRuntime sets up the runtime components after module instantiation.
func (r *Runtime) initializeRuntime() {
	r.malloc = r.module.ExportedFunction("malloc")
	r.free = r.module.ExportedFunction("free")
	r.mem = &Mem{mem: r.module.Memory()}
	r.handle = r.Call(
		"New_QJS",
		uint64(r.option.MemoryLimit),
		uint64(r.option.MaxStackSize),
		0, // max_execution_time is ignored by the guest; time limits come from Option.Context
		uint64(r.option.GCThreshold),
	)

	r.context.handle = r.Call("QJS_GetContext", r.handle.raw)
	r.context.runtime = r
}

func (r *Runtime) call(name string, args ...uint64) uint64 {
	fn := r.module.ExportedFunction(name)
	if fn == nil {
		panic(fmt.Errorf("WASM function %s not found", name))
	}

	results, err := fn.Call(r.context, args...)
	if err != nil {
		stack := debug.Stack()
		panic(fmt.Errorf("failed to call %s: %w\nstack: %s", name, err, stack))
	}

	if len(results) == 0 {
		return 0
	}

	return results[0]
}
