// Package jsrun runs untrusted JavaScript in a sandbox: QuickJS-NG compiled to WebAssembly,
// executed by wazero (pure Go, no cgo).
//
// # Security model
//
// The JS engine itself is treated as untrusted. Every Run gets a fresh wazero runtime and a
// fresh QuickJS instance that are discarded afterwards, so nothing (globals, prototypes, memory
// corruption) leaks between runs. What a script can reach is exactly:
//
//   - CPU until the run's deadline: the wasm is aborted when the context is done (infinite
//     loops included), and sleeps/timers wake up early.
//   - Memory up to Config.MemoryLimit for the JS heap and a hard cap on the wasm linear memory.
//   - The host functions this package installs: console output (capped at Config.MaxOutput)
//     and, when Request.FS is set, the fs API backed by that vfs.FS (which enforces its own
//     paths, limits and read-only rules).
//
// The guest has no WASI preopens (no host files), no environment, no arguments, no stdin and no
// sockets. QuickJS' qjs:std / qjs:os modules stay reachable through dynamic import(), but with
// no file system, network or exec support they can only sleep (bounded by the deadline), read
// clocks or exit the instance.
//
// Recursion: the engine (rebuilt with a stack-limit patch, see internal/qjs/build) throws an
// ordinary, catchable RangeError "Maximum call stack size exceeded" when a script uses more
// than Config.StackLimit of stack. The default 32 KiB fits about 100 nested calls of a typical
// function, 50 through callbacks such as Array.prototype.map, 20 through sort comparators.
// ErrorKind "crash" is left for genuine engine failures (wasm traps, corrupted bytecode).
package jsrun

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tetratelabs/wazero/sys"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/sandbox/jsrun/internal/qjs"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
)

//go:embed prelude.js
var prelude string

// Defaults and hard caps for Config.
const (
	DefaultTimeout       = 5 * time.Second
	DefaultMemoryLimit   = 64 << 20
	MaxMemoryLimit       = 96 << 20
	DefaultMaxOutput     = 32 << 10
	DefaultMaxResult     = 32 << 10
	DefaultMaxCodeSize   = 256 << 10
	DefaultMaxConcurrent = 4
	DefaultStackLimit    = 32 << 10
	MinStackLimit        = 32 << 10  // the prelude needs it to serialize a 64-level result
	MaxStackLimit        = 128 << 10 // the engine's whole JS stack (see internal/qjs/build)

	// wasmMemoryPages is the hard cap on each instance's wasm linear memory (64 KiB pages):
	// 128 MiB, which leaves room above MaxMemoryLimit for the engine's own data, stack and
	// allocator overhead.
	wasmMemoryPages = 2048

	defaultFilename = "script.js"
	maxFilenameLen  = 128
)

func init() {
	// Read once, when the first runtime is created.
	qjs.MaxMemoryPages = wasmMemoryPages
}

// testHookMemory, when set (tests only), receives the wasm memory size of each instance after
// its script finished.
var testHookMemory func(bytes uint32)

// ErrInvalidRequest is returned (wrapped) by Run for requests it refuses to execute.
var ErrInvalidRequest = errors.New("jsrun: invalid request")

// Config bounds every run. Zero fields mean the default.
type Config struct {
	// Timeout is the wall-clock budget of one run, including engine startup (default 5s).
	Timeout time.Duration
	// MemoryLimit caps the QuickJS heap in bytes (default 64 MiB, at most MaxMemoryLimit).
	MemoryLimit int
	// MaxOutput caps captured console output in bytes (default 32 KiB); the rest is dropped.
	MaxOutput int
	// MaxResult caps the JSON-encoded result in bytes (default 32 KiB); larger results fail.
	MaxResult int
	// MaxCodeSize caps the script source in bytes (default 256 KiB).
	MaxCodeSize int
	// MaxConcurrent caps runs executing at once (default 4); further runs wait for a slot.
	MaxConcurrent int
	// StackLimit caps the engine stack available to JS code, in bytes (default and minimum
	// 32 KiB, at most MaxStackLimit = 128 KiB). Going deeper throws RangeError "Maximum call
	// stack size exceeded". 32 KiB fits about 100 nested calls of a typical function, 50 through
	// callbacks such as Array.prototype.map; see approxDepth.
	StackLimit int
}

// WithDefaults returns c with zero fields replaced by defaults and limits clamped.
func (c Config) WithDefaults() Config {
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MemoryLimit <= 0 {
		c.MemoryLimit = DefaultMemoryLimit
	}
	c.MemoryLimit = min(c.MemoryLimit, MaxMemoryLimit)
	if c.MaxOutput <= 0 {
		c.MaxOutput = DefaultMaxOutput
	}
	if c.MaxResult <= 0 {
		c.MaxResult = DefaultMaxResult
	}
	if c.MaxCodeSize <= 0 {
		c.MaxCodeSize = DefaultMaxCodeSize
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = DefaultMaxConcurrent
	}
	if c.StackLimit <= 0 {
		c.StackLimit = DefaultStackLimit
	}
	c.StackLimit = min(max(c.StackLimit, MinStackLimit), MaxStackLimit)
	return c
}

// approxDepth estimates how many nested calls fit in a stack limit, from measured per-frame
// costs: ~330 bytes for a typical function (a few arguments and locals), ~650 when each level
// also goes through a native callback such as Array.prototype.map. Used for messages only.
func approxDepth(stackLimit int) (calls, viaCallbacks int) {
	round := func(n int) int { return (n + 5) / 10 * 10 }
	return round(stackLimit / 330), round(stackLimit / 650)
}

// Request is one script execution.
type Request struct {
	// Code is the script source. It runs as a classic script in strict mode with top-level
	// await; the value of its last expression is the result (a promise is awaited).
	Code string
	// Filename names the script in stack traces (default "script.js").
	Filename string
	// Input, when set, is parsed and exposed to the script as the global `input`.
	Input json.RawMessage
	// FS, when set, is exposed to the script as the global `fs`. Wrap it with vfs.ReadOnly to
	// forbid writes.
	FS vfs.FS
	// Timeout shortens Config.Timeout for this run (0 or larger values = Config.Timeout).
	Timeout time.Duration
}

// ErrorKind classifies why a script failed.
type ErrorKind string

const (
	// KindException: the script threw (or a returned promise rejected), including syntax errors.
	KindException ErrorKind = "exception"
	// KindTimeout: the run hit its deadline.
	KindTimeout ErrorKind = "timeout"
	// KindMemory: the script ran out of memory.
	KindMemory ErrorKind = "memory"
	// KindCrash: the engine trapped, most likely from recursion that is too deep.
	KindCrash ErrorKind = "crash"
	// KindExit: the script exited the engine (std.exit via import("qjs:std")).
	KindExit ErrorKind = "exit"
	// KindResult: the result could not be serialized or is larger than Config.MaxResult.
	KindResult ErrorKind = "result"
)

// Error describes a failed script.
type Error struct {
	Kind    ErrorKind `json:"kind"`
	Message string    `json:"message"`
	Stack   string    `json:"stack,omitempty"`
}

func (e *Error) Error() string { return string(e.Kind) + ": " + e.Message }

// Result is the outcome of a run that executed. Script failures are reported in Error, not as a
// Go error.
type Result struct {
	// Value is the JSON encoding of the result; nil when it is undefined or the script failed.
	Value json.RawMessage `json:"value,omitempty"`
	// Output is the captured console output (each call on its own line; warn/error prefixed).
	Output string `json:"output"`
	// OutputTruncated reports that output beyond Config.MaxOutput was dropped.
	OutputTruncated bool          `json:"output_truncated,omitempty"`
	Error           *Error        `json:"error,omitempty"`
	Duration        time.Duration `json:"duration"`
}

// Runner executes scripts. It is safe for concurrent use; the zero value is not usable, call New.
type Runner struct {
	cfg Config
	sem chan struct{}
}

// New returns a Runner with cfg (zero fields = defaults).
func New(cfg Config) *Runner {
	cfg = cfg.WithDefaults()
	return &Runner{cfg: cfg, sem: make(chan struct{}, cfg.MaxConcurrent)}
}

// Config returns the effective configuration.
func (r *Runner) Config() Config { return r.cfg }

// Warmup creates and discards one engine instance so the wasm module is compiled before the
// first real run (the first instance in a process takes a few hundred milliseconds; later ones
// a few milliseconds).
func (r *Runner) Warmup(ctx context.Context) error {
	rt, err := qjs.New(qjs.Option{Context: ctx})
	if err != nil {
		return fmt.Errorf("jsrun: warmup: %w", err)
	}
	rt.Close()
	return nil
}

// Run executes req. It returns a Go error only for an invalid request (ErrInvalidRequest), when
// ctx is done before or during the run (ctx.Err()), or when the engine cannot start. Everything
// the script does wrong — exceptions, timeouts, memory, crashes, oversized results — is reported
// in Result.Error.
func (r *Runner) Run(ctx context.Context, req Request) (*Result, error) {
	if err := r.validate(&req); err != nil {
		return nil, err
	}

	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	timeout := r.cfg.Timeout
	if req.Timeout > 0 && req.Timeout < timeout {
		timeout = req.Timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ex := &execution{
		cfg:     r.cfg,
		req:     req,
		parent:  ctx,
		ctx:     runCtx,
		timeout: timeout,
		out:     &output{max: r.cfg.MaxOutput},
	}
	start := time.Now()
	res, err := ex.run()
	if err != nil {
		return nil, err
	}
	res.Duration = time.Since(start)
	return res, nil
}

func (r *Runner) validate(req *Request) error {
	if strings.TrimSpace(req.Code) == "" {
		return fmt.Errorf("%w: code is empty", ErrInvalidRequest)
	}
	if len(req.Code) > r.cfg.MaxCodeSize {
		return fmt.Errorf("%w: code is %d bytes, over the %d-byte limit", ErrInvalidRequest, len(req.Code), r.cfg.MaxCodeSize)
	}
	if strings.IndexByte(req.Code, 0) >= 0 {
		return fmt.Errorf("%w: code contains a NUL character", ErrInvalidRequest)
	}
	if !utf8.ValidString(req.Code) {
		return fmt.Errorf("%w: code is not valid UTF-8", ErrInvalidRequest)
	}
	if req.Filename == "" {
		req.Filename = defaultFilename
	}
	if len(req.Filename) > maxFilenameLen || !utf8.ValidString(req.Filename) ||
		strings.ContainsFunc(req.Filename, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
		return fmt.Errorf("%w: bad filename", ErrInvalidRequest)
	}
	if len(req.Input) > 0 && !json.Valid(req.Input) {
		return fmt.Errorf("%w: input is not valid JSON", ErrInvalidRequest)
	}
	return nil
}

// execution is the state of one run.
type execution struct {
	cfg     Config
	req     Request
	parent  context.Context // the caller's context
	ctx     context.Context // parent + the run's timeout; bounds the engine and host calls
	timeout time.Duration
	out     *output
}

func (ex *execution) run() (res *Result, err error) {
	rt, err := qjs.New(qjs.Option{
		Context:      ex.ctx,
		MemoryLimit:  ex.cfg.MemoryLimit,
		MaxStackSize: ex.cfg.StackLimit,
		Stdout:       ex.out,
		Stderr:       ex.out,
	})
	if err != nil {
		// A deadline or cancellation during startup surfaces here too.
		if res, cerr := ex.contextFailure(); res != nil || cerr != nil {
			return res, cerr
		}
		return nil, fmt.Errorf("jsrun: start engine: %w", err)
	}
	defer rt.Close()

	defer func() {
		if p := recover(); p != nil {
			res, err = ex.classifyPanic(p)
		}
	}()

	value, scriptErr := ex.execute(rt)
	if testHookMemory != nil {
		testHookMemory(rt.Mem().Size())
	}
	if scriptErr != nil {
		// An exception can be a consequence of the deadline (a host call failing with
		// ETIMEDOUT that the script did not catch); report the cause.
		if res, cerr := ex.contextFailure(); res != nil || cerr != nil {
			return res, cerr
		}
	}
	res = ex.result()
	res.Value, res.Error = value, scriptErr
	return res, nil
}

// execute installs the host API, runs the script and serializes its result. JS failures are
// returned as *Error; engine aborts (deadline, traps, exit) panic and are classified by run.
func (ex *execution) execute(rt *qjs.Runtime) (json.RawMessage, *Error) {
	jsctx := rt.Context()
	jsctx.SetFunc("__wc_log", ex.hostLog)
	if ex.req.FS != nil {
		h := &fsHost{ctx: ex.ctx, fsys: ex.req.FS}
		jsctx.SetFunc("__wc_fs", h.call)
	}
	if len(ex.req.Input) > 0 {
		jsctx.Global().SetPropertyStr("input", jsctx.ParseJSON(string(ex.req.Input)))
	}

	helpers, err := rt.Eval("<prelude>", qjs.Code(prelude))
	if err != nil {
		panic(fmt.Errorf("jsrun: prelude failed: %w", err))
	}
	serialize := helpers.GetPropertyStr("serialize")

	value, err := rt.Eval(ex.req.Filename, qjs.Code(ex.req.Code), qjs.FlagAsync())
	if err == nil && value.IsPromise() {
		value, err = value.Await()
	}
	if err != nil {
		return nil, ex.scriptError(err)
	}

	encoded, err := jsctx.Invoke(serialize, helpers, value)
	if err != nil {
		e := ex.scriptError(err)
		if e.Kind == KindException {
			e.Kind = KindResult
			e.Message = "could not serialize the result: " + e.Message
		}
		return nil, e
	}
	if encoded.IsUndefined() {
		return nil, nil
	}
	// The JS length (UTF-16 units) never exceeds the UTF-8 byte length, so this rejects
	// oversized results before copying them out of the guest.
	if n := encoded.GetPropertyStr("length").Int64(); n > int64(ex.cfg.MaxResult) {
		return nil, ex.resultTooLarge(n)
	}
	text := encoded.String()
	if len(text) > ex.cfg.MaxResult {
		return nil, ex.resultTooLarge(int64(len(text)))
	}
	return json.RawMessage(text), nil
}

func (ex *execution) resultTooLarge(n int64) *Error {
	return &Error{
		Kind: KindResult,
		Message: fmt.Sprintf("the result is at least %d bytes of JSON, over the %d-byte limit; return a smaller value (or print a summary)",
			n, ex.cfg.MaxResult),
	}
}

// scriptError converts an error returned by the engine for a JS exception.
func (ex *execution) scriptError(err error) *Error {
	msg, stack := splitException(err.Error())
	switch {
	case strings.HasPrefix(msg, "InternalError: out of memory"):
		return ex.memoryError()
	case strings.HasPrefix(msg, "InternalError: invalid opcode"): // corrupted bytecode
		return crashError(msg)
	}
	return &Error{Kind: KindException, Message: msg, Stack: stack}
}

func (ex *execution) memoryError() *Error {
	return &Error{
		Kind:    KindMemory,
		Message: fmt.Sprintf("out of memory: the script exceeded the %d MiB memory limit", ex.cfg.MemoryLimit>>20),
	}
}

func crashError(detail string) *Error {
	return &Error{
		Kind: KindCrash,
		Message: "the JavaScript engine crashed (an internal engine failure, not a JS exception); " +
			"the run was aborted. Retry with a simpler script, e.g. splitting the work into smaller steps",
		Stack: detail,
	}
}

// splitException splits "<message>\n<stack>" as produced by the engine; QuickJS stack lines
// start with "    at ".
func splitException(s string) (msg, stack string) {
	s = strings.TrimRight(s, "\n")
	if i := strings.Index(s, "\n    at "); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// contextFailure reports what a done run context means: the caller's context ended (Go error)
// or the run hit its own deadline (timeout result). Both nil when the context is still live.
func (ex *execution) contextFailure() (*Result, error) {
	if err := ex.parent.Err(); err != nil {
		return nil, err
	}
	if ex.ctx.Err() == nil {
		return nil, nil
	}
	res := ex.result()
	res.Error = &Error{
		Kind: KindTimeout,
		Message: fmt.Sprintf("the script did not finish within %s (an endless loop, a promise or timer that never settles, or too much work)",
			ex.timeout),
	}
	return res, nil
}

func (ex *execution) classifyPanic(p any) (*Result, error) {
	if res, err := ex.contextFailure(); res != nil || err != nil {
		return res, err
	}
	perr, ok := p.(error)
	if !ok {
		perr = fmt.Errorf("%v", p)
	}
	res := ex.result()
	var exit *sys.ExitError
	switch msg := perr.Error(); {
	case errors.As(perr, &exit):
		res.Error = &Error{Kind: KindExit, Message: fmt.Sprintf("the script exited the engine with code %d", exit.ExitCode())}
	case strings.Contains(msg, "out of memory"):
		res.Error = ex.memoryError()
	default:
		res.Error = crashError(firstLine(msg))
	}
	return res, nil
}

func (ex *execution) result() *Result {
	out, truncated := ex.out.text()
	return &Result{Output: out, OutputTruncated: truncated}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
