# agentkit

Embeddable Go LLM agent runtime: streaming tool loop, provider-neutral model types, typed tools,
pluggable persistence. No framework dependencies in the core packages.

| Package | Purpose |
|---|---|
| `agentkit` | `Agent`, `Config`, run loop, sessions, events, hooks, `AsTool` |
| `llm` | neutral `Message`/`Block`/`Request`/`StreamEvent` types, `Provider` interface |
| `llm/fake` | scripted provider for tests |
| `tool` | `Tool` interface, typed `tool.New[In, Out]`, `tool.Set` |
| `store` | `Store` interface + `Session`, `StoredMessage`, `Run`, `ToolCallRecord` |
| `store/memory` | in-memory store (default) |
| `store/storetest` | conformance suite for `Store` implementations |

## Usage

```go
type weatherIn struct {
    City string `json:"city" jsonschema:"description=City name"`
}

weather := tool.New("weather", "Current weather for a city",
    func(ctx context.Context, in weatherIn) (map[string]any, error) {
        return map[string]any{"city": in.City, "temp_c": 21}, nil
    })

a, err := agentkit.New(agentkit.Config{
    Name:     "assistant",
    Provider: provider, // any llm.Provider
    Model:    "muse-spark-1.3-contributor",
    System:   "You are helpful.",
    Tools:    tool.NewSet(weather),
    Store:    memory.New(), // default when nil
    MaxSteps: 20,            // default
    Hooks: agentkit.Hooks{
        BeforeToolCall: func(ctx context.Context, c *agentkit.ToolCall) error { return nil }, // error = veto
        OnEvent:        func(ev agentkit.Event) { slog.Debug("agent", "type", ev.Type) },
    },
})
_, _ = a.Recover(ctx) // once at startup: mark runs left "running" as "interrupted"

sess, _ := a.NewSession(ctx, agentkit.SessionOptions{Title: "chat", Metadata: map[string]any{"user_id": uid}})

// Streaming: the run starts when iterated; breaking out cancels it.
for ev, err := range a.Run(ctx, sess.ID, agentkit.UserText("Weather in Kyiv?")) {
    if err != nil { // last event is run_failed (Status failed or cancelled)
        return err
    }
    if ev.Type == agentkit.EventTextDelta {
        fmt.Print(ev.Delta)
    }
}

// Blocking:
res, err := a.RunSync(ctx, sess.ID, agentkit.UserText("And tomorrow?"))
fmt.Println(res.Text, res.Usage)

// Agents as tools ({"prompt": string} in, final text out; each call gets a fresh session):
orchestrator, _ := agentkit.New(agentkit.Config{Provider: provider, Tools: tool.NewSet(agentkit.AsTool(a, "assistant", "Answers questions"))})
```

Events: `run_started`, `text_delta`, `reasoning_delta`, `tool_call_started`, `tool_call_finished`,
`step_finished` (assistant message + step usage), `run_finished` (final text + total usage), `run_failed`.

Loop semantics: one active run per session (`ErrSessionBusy`; `Agent.Cancel(sessionID)` stops it);
tool calls of a step run in parallel with `ToolTimeout` and panic recovery; tool errors, unknown tools and
vetoed calls become `tool_result` blocks with `IsError`; every message is persisted as it is produced;
runs end `completed`, `failed` (incl. `ErrMaxSteps`) or `cancelled`. `ContextPolicy` chooses the history
sent each step (`PassThrough` by default, `DropSettledReasoning(budget)` available).

## Providers

A provider streams deltas and must finish with one `llm.StreamMessageDone` event carrying the complete
assistant `*llm.Message` (reasoning blocks with `Opaque`, tool calls with full `Input`), `Usage` and
`StopReason`. Reasoning `Opaque` payloads are persisted and replayed verbatim on later turns.

### Meta Model API (`llm/meta`)

```go
p := meta.New(meta.Config{APIKey: os.Getenv("MODEL_API_KEY")}) // base URL https://api.meta.ai/v1
```

Uses the Responses API with `stream:true`, `store:false` and stateless encrypted reasoning replay
(`include:["reasoning.encrypted_content"]`): the model's chain of thought comes back encrypted and is replayed
verbatim on later turns, so nothing is stored at Meta. Default model `muse-spark-1.3-contributor`
(**contributor mode: heavily discounted tokens; in exchange Meta may use request content and responses to train
future models**, 150 requests/min). `muse-spark-1.3`, `muse-spark-1.2`, `muse-spark-1.1` and `muse-wonder-1.0`
are available in standard mode. Tool calls stream their arguments; retries with exponential backoff + jitter
apply to 429/5xx before the first stream byte, honouring `Retry-After`. A live smoke test runs with
`MODEL_API_KEY=... go test -tags live -run TestLive ./llm/meta/`.

## Storage

### SQLite (`store/sqlite`)

```go
import sqlitestore "github.com/dot5enko/wonder-critique/apps/backend/agentkit/store/sqlite"

s, err := sqlitestore.New(sqlDB) // any SQLite database/sql driver; tables prefixed agent_
s, err := sqlitestore.New(sqlDB, sqlitestore.WithTablePrefix("custom_"))
```

Schema is created idempotently; the store serializes writes so per-session sequence numbers stay contiguous.
It passes the conformance suite in `store/storetest` (use it for any custom implementation).

## HTTP API (`httpapi`)

`httpapi.New(agent, httpapi.Options{...}) http.Handler` exposes one agent; mount it under any prefix
(it uses relative ServeMux patterns, so `http.StripPrefix` and chi `Mount` both work).

| Method | Path | Notes |
|---|---|---|
| POST | `/sessions` | `{"title","metadata"}` → 201 |
| GET | `/sessions` | `?limit`, `?offset` |
| GET | `/sessions/{id}` | the session's messages |
| DELETE | `/sessions/{id}` | 204 |
| POST | `/sessions/{id}/runs` | `{"input":"text"}` or `{"messages":[...]}`; default is SSE (`event: <type>` + `data: <json>`, heartbeat comments, final `event: done`); `?stream=false` returns the JSON result |
| POST | `/sessions/{id}/cancel` | 204; cancels the active run |

With `Options.SessionOwner` every session carries `metadata.owner` and lists/access are scoped to it; an error
from the function yields 401, another owner's session 404. A busy session answers 409 `busy`. Client
disconnects cancel the run.

## fx (`fxagent`)

```go
fxagent.Provide("critic", func(s store.Store) (*agentkit.Agent, error) { ... }), // registers name:"critic"
fxagent.SQLiteStore(), // store.Store from an injected *sql.DB
// consume: fx.Annotate(f, fx.ParamTags(`name:"critic"`))
```

On start each agent recovers runs that were left running by a previous process.

## Testing

```go
p := fake.New(
    fake.ToolCalls(fake.Call("c1", "weather", map[string]string{"city": "Kyiv"})),
    fake.Text("It is 21°C."),
)
// ... run the agent, then inspect p.Requests()
```

Store implementations run the conformance suite:

```go
func TestConformance(t *testing.T) {
    storetest.Run(t, func(t *testing.T) store.Store { return mystore.New(...) })
}
```
