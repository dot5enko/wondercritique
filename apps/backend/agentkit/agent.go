// Package agentkit is an embeddable LLM agent runtime: a streaming tool loop over a
// provider-neutral model interface with pluggable persistence.
package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/store"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/store/memory"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/tool"
)

// Defaults applied by New.
const (
	DefaultName        = "agent"
	DefaultMaxSteps    = 20
	DefaultToolTimeout = 2 * time.Minute
)

var (
	// ErrSessionBusy is returned when a session already has an active run.
	ErrSessionBusy = errors.New("agentkit: session has an active run")
	// ErrMaxSteps is returned when a run exceeds Config.MaxSteps model calls.
	ErrMaxSteps = errors.New("agentkit: max steps exceeded")
	// ErrNotFound is returned for unknown sessions (alias of store.ErrNotFound).
	ErrNotFound = store.ErrNotFound
)

// Config configures an Agent.
type Config struct {
	Name     string       // identifies the agent's sessions in the store; default "agent"
	Provider llm.Provider // required
	Model    string       // empty lets the provider choose its default
	System   string
	Tools    *tool.Set
	Store    store.Store // default: in-memory store

	MaxSteps        int           // max model calls per run; default 20
	ToolTimeout     time.Duration // per tool call; default 2m
	MaxOutputTokens int
	ReasoningEffort string
	Extra           map[string]any // passed to llm.Request.Extra

	ContextPolicy ContextPolicy // default: PassThrough
	Hooks         Hooks
}

// Hooks customize the run loop. All hooks are called from the run's goroutine.
type Hooks struct {
	// BeforeToolCall runs before each tool call. A non-nil error vetoes the call; the error text is
	// returned to the model as an error tool result. The hook may modify call.Input.
	BeforeToolCall func(ctx context.Context, call *ToolCall) error
	// BeforeRequest may adjust each model request (e.g. set SafetyID from session metadata).
	// A non-nil error fails the run.
	BeforeRequest func(ctx context.Context, sess *store.Session, req *llm.Request) error
	// OnEvent observes every event, before it is yielded to the caller.
	OnEvent func(Event)
}

// ToolCall describes a tool invocation requested by the model. Tools can retrieve it with CallFromContext.
type ToolCall struct {
	ID        string
	Name      string
	Input     json.RawMessage
	SessionID string
	RunID     string
	Step      int
}

// Agent runs conversations against a model with tools. It is safe for concurrent use.
type Agent struct {
	cfg    Config
	mu     sync.Mutex
	active map[string]context.CancelFunc // session ID -> cancel of the active run
}

// New validates cfg, applies defaults and returns an Agent.
func New(cfg Config) (*Agent, error) {
	if cfg.Provider == nil {
		return nil, errors.New("agentkit: Config.Provider is required")
	}
	if cfg.MaxSteps < 0 || cfg.ToolTimeout < 0 {
		return nil, errors.New("agentkit: MaxSteps and ToolTimeout must not be negative")
	}
	if cfg.Name == "" {
		cfg.Name = DefaultName
	}
	if cfg.Store == nil {
		cfg.Store = memory.New()
	}
	if cfg.Tools == nil {
		cfg.Tools = tool.NewSet()
	}
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = DefaultMaxSteps
	}
	if cfg.ToolTimeout == 0 {
		cfg.ToolTimeout = DefaultToolTimeout
	}
	if cfg.ContextPolicy == nil {
		cfg.ContextPolicy = PassThrough()
	}
	return &Agent{cfg: cfg, active: map[string]context.CancelFunc{}}, nil
}

// Name returns the agent name.
func (a *Agent) Name() string { return a.cfg.Name }

// Config returns the effective configuration (with defaults applied).
func (a *Agent) Config() Config { return a.cfg }

// Store returns the agent's store.
func (a *Agent) Store() store.Store { return a.cfg.Store }

// Recover marks runs left running by a previous process as interrupted. Call it once at startup.
func (a *Agent) Recover(ctx context.Context) (int, error) { return a.cfg.Store.RecoverInterrupted(ctx) }

// Busy reports whether the session has an active run.
func (a *Agent) Busy(sessionID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.active[sessionID]
	return ok
}

// Cancel cancels the active run of a session, reporting whether one was running.
func (a *Agent) Cancel(sessionID string) bool {
	a.mu.Lock()
	cancel, ok := a.active[sessionID]
	a.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (a *Agent) acquire(sessionID string, cancel context.CancelFunc) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.active[sessionID]; ok {
		return false
	}
	a.active[sessionID] = cancel
	return true
}

func (a *Agent) release(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.active, sessionID)
}

type callKey struct{}

// CallFromContext returns the tool call being executed, when ctx is a tool's context.
func CallFromContext(ctx context.Context) (*ToolCall, bool) {
	c, ok := ctx.Value(callKey{}).(*ToolCall)
	return c, ok
}
