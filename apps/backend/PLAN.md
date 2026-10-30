# Backend architecture plan

Go 1.27. Repo root: `/root/projects/wonder-critique`. This file is the shared spec for all implementation work.

## 1. Decisions

| Concern | Choice |
|---|---|
| Module paths | `github.com/dot5enko/wonder-critique/apps/backend`, `github.com/dot5enko/wonder-critique/apps/backend/agentkit` (separate module), joined by root `go.work` |
| Router | `github.com/go-chi/chi/v5` |
| DI / lifecycle | `go.uber.org/fx` |
| ORM | **GORM** (`gorm.io/gorm`) with pure-Go SQLite driver `github.com/glebarez/sqlite` (modernc under the hood, no CGO) |
| Schema | GORM `AutoMigrate` for the draft; every feature registers its models through an fx group (`dbx.AsModel(&models.X{})`). Swap for versioned migrations later. |
| Validation | `github.com/go-playground/validator/v10` |
| Config | `github.com/caarlos0/env/v11`, optional `.env` via `github.com/joho/godotenv` |
| Logging | `log/slog` |
| IDs | UUIDv7 strings (`github.com/google/uuid`) |
| Auth | JWT (`github.com/golang-jwt/jwt/v5`, HS256) + pluggable login providers; default provider = 6-digit email code sent via Resend (`github.com/resend/resend-go/v2`) |
| LLM | Only Meta Model API (Muse Spark), contributor models by default. Hand-written client over `net/http` for the **Responses API**, stateless encrypted reasoning replay |
| Agent tool schemas | `github.com/invopop/jsonschema` |

SQLite pragmas on open: `journal_mode(WAL)`, `foreign_keys(ON)`, `busy_timeout(5000)`, `synchronous(NORMAL)`
(glebarez DSN: `file.db?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)`).

## 2. Layout

```
go.work                              # use ./apps/backend ./apps/backend/agentkit
apps/backend/
  go.mod
  cmd/server/main.go                 # fx.New(app.Module).Run()
  internal/
    app/app.go                       # composition root: list of fx modules
    config/                          # Config struct (env), fx provider
    platform/
      logger/                        # *slog.Logger provider
      dbx/                           # *gorm.DB provider, pragmas, AutoMigrate of fx group "models",
                                     # tx-in-context helpers, OnStop close
      httpx/                         # router, server lifecycle, middleware, JSON helpers,
                                     # error rendering, Controller + fx group helpers
      apperr/                        # domain error types
    crud/                            # generic CRUD (see §4)
    models/                          # shared Base model + app models
    features/
      health/                        # /healthz, /readyz
      auth/                          # JWT, providers, email-code provider, Resend mailer (see §5)
      messages/                      # example CRUD feature
      agents/                        # wires agentkit into the app (see §6)
  agentkit/                          # separate module (see §6)
```

## 3. Layering & shared contracts

```
controller (HTTP: decode, validate, map errors) -> service (business logic, tx) -> gorm (via dbx.Conn(ctx))
```

- Controllers never touch GORM directly; services never touch `net/http`.
- `apperr`: `NotFound`, `Invalid` (with field map), `Unauthorized`, `Forbidden`, `Conflict`, `TooManyRequests`, `Internal`.
  `httpx` maps them to status codes and one JSON shape: `{"error":{"code":"not_found","message":"...","fields":{...}}}`.
- Transactions: `dbx.Tx(ctx, db, func(ctx) error)` stores the tx in ctx; `dbx.Conn(ctx, db) *gorm.DB` returns the tx if present,
  else `db.WithContext(ctx)`. All repos/services use `dbx.Conn`.
- `httpx.Controller interface{ Routes(r chi.Router) }`; `httpx.AsController(ctor)` annotates a constructor into fx group
  `"controllers"`. The router mounts all controllers under `/api/v1` with RequestID, RealIP, slog logger, Recoverer, Timeout.
- `httpx` typed adapters: `httpx.Handle(func(w, r) error)` (central error rendering), `httpx.Decode[T](r)` (JSON + validation),
  `httpx.JSON(w, status, v)`.
- `models.Base`: `ID string (primaryKey, uuidv7 set in BeforeCreate)`, `CreatedAt`, `UpdatedAt`, `DeletedAt gorm.DeletedAt (index)`.

## 4. Generic CRUD (`internal/crud`)

```go
func (c *Controller) Routes(r chi.Router) {
    api := c.crud.On(r) // *crud.API: chi.Router + crud deps (db, validator, logger)

    crud.NewForType[models.Message](api, "/messages")       // all ops, zero config

    res := crud.NewForType[models.Thread](api, "/threads",
        crud.Use(auth.Required),                             // all ops
        crud.UseFor(crud.OpDelete, auth.Role("admin")),      // single op
        crud.Only(crud.OpList, crud.OpGet, crud.OpCreate),
        crud.Scope[models.Thread](func(ctx context.Context, q *gorm.DB) *gorm.DB {
            return q.Where("owner_id = ?", auth.UserID(ctx))
        }),
        crud.WithHooks(crud.Hooks[models.Thread]{
            BeforeCreate: func(ctx context.Context, t *models.Thread) error { t.OwnerID = auth.UserID(ctx); return nil },
        }),
    )
    res.Route(func(r chi.Router) { r.Post("/{id}/archive", c.archive) }) // extra endpoints, same prefix + middleware
    _ = res.Service // *crud.Service[T], reusable from custom services
}
```

`crud.NewService[T](db, opts...)` builds the service without HTTP.

| Op | Route | Behaviour |
|---|---|---|
| List | `GET /prefix` | `?limit=` (default 50, max 200), `?cursor=` (keyset on id, UUIDv7 ⇒ time ordered) or `?offset=`, `?sort=-created_at,title`, `?filter[field]=v`, `?filter[field][op]=v` (ops: eq, ne, gt, gte, lt, lte, in (comma list), like). Response `{"items":[...],"next_cursor":"..."}` |
| Get | `GET /prefix/{id}` | 404 if missing or outside `Scope` |
| Create | `POST /prefix` | only `create`-tagged fields are taken from the body; validation; 201 |
| Update | `PATCH /prefix/{id}` | partial: only keys present in body AND `update`-tagged; validation of the merged entity |
| Delete | `DELETE /prefix/{id}` | 204; soft delete automatically when model has `gorm.DeletedAt` |

Field policy via `crud:"create,update,filter,sort"` struct tags; `id/created_at/updated_at/deleted_at` always readonly,
filterable and sortable (`id`, `created_at`, `updated_at`). Untagged fields are server-set only. Unknown filter/sort keys → 400.
Column names resolved through GORM's schema parser (`schema.Parse`), metadata cached per type.

Options: `Use`, `UseFor`, `Only`, `Scope[T]`, `WithHooks[T]` (BeforeCreate, AfterCreate, BeforeUpdate(ctx, existing, patch map), AfterUpdate,
BeforeDelete, AfterDelete), `Authorize[T](func(ctx, op, *T) error)`, `MaxLimit`, `DefaultSort`.

## 5. Auth (`internal/features/auth`)

Models: `User{Base; Email (unique, lowercased); Name; Role}`, `Identity{Base; UserID; Provider; Subject}` (unique provider+subject),
`AuthCode{Base; Email; CodeHash; ExpiresAt; Attempts; ConsumedAt}`, `RefreshToken{Base; UserID; TokenHash (unique); ExpiresAt; RevokedAt; ReplacedBy}`.

Pluggable providers:

```go
type Provider interface {
    Name() string                                                         // "email_code", later "google", ...
    Start(ctx context.Context, req json.RawMessage) (StartResult, error)  // e.g. send code
    Verify(ctx context.Context, req json.RawMessage) (Identity, error)    // returns provider+subject+email+name
}
```

Registered via fx group `"auth_providers"` (`auth.AsProvider(ctor)`).

Email-code provider: 6-digit code from `crypto/rand`, stored as HMAC-SHA256(secret, email+code), TTL 10 min, max 5 attempts,
resend cooldown 60 s per email, constant-time compare, previous unconsumed codes invalidated on new send.
Delivery via `Mailer` interface: `ResendMailer` (when `RESEND_API_KEY` is set) else `LogMailer` (logs the code; dev only).

JWT: HS256 access token (15 min, claims `sub`, `role`, `exp`, `iat`, `iss`), opaque refresh token (32 random bytes, stored hashed,
30 days, rotation on use, reuse of a rotated token revokes the whole chain).

Endpoints (`/api/v1/auth`):
`POST /{provider}/start`, `POST /{provider}/verify` → `{access_token, refresh_token, expires_in, user}`,
`POST /refresh`, `POST /logout`, `GET /me` (requires auth).

Middleware/helpers: `auth.Required`, `auth.Optional`, `auth.Role(roles...)`, `auth.UserID(ctx)`, `auth.ClaimsFrom(ctx)`.
Config: `JWT_SECRET` (required, ≥32 bytes outside dev), `JWT_ISSUER`, `RESEND_API_KEY`, `MAIL_FROM`.

## 6. Agent SDK (`apps/backend/agentkit`, separate module)

Embeddable library: no fx/chi in the core packages; SQLite persistence; streaming-first; testable without network.

```
agentkit/
  agent.go, run.go, session.go, events.go   # Agent, Config, New, Run loop, sessions, events
  llm/                                      # neutral types + Provider interface
  llm/meta/                                 # Meta Model API (Responses API) provider
  llm/fake/                                 # scripted provider for tests
  tool/                                     # Tool interface, typed tool.New[In,Out], Set
  store/                                    # Store interface + shared types
  store/sqlite/                             # database/sql impl (tables agent_*), own schema bootstrap
  store/memory/                             # in-memory impl
  httpapi/                                  # optional http.Handler (sessions + SSE runs)
  fxagent/                                  # optional fx module
```

### Neutral LLM types (`llm`)

```go
type Role string // system, user, assistant, tool
type BlockType string // text, image, tool_call, tool_result, reasoning
type Block struct {
    Type       BlockType
    Text       string          // text
    Phase      string          // assistant text: "commentary" when it precedes a tool call (Meta requires replay of it)
    ToolCallID, ToolName string
    Input      json.RawMessage // tool_call args
    Output     string          // tool_result
    IsError    bool
    ImageURL   string          // image (data: URL or https)
    Opaque     json.RawMessage // provider payload that must be replayed verbatim (e.g. Meta reasoning item w/ encrypted_content)
    Provider   string          // which provider produced Opaque
}
type Message struct { Role Role; Blocks []Block }
type ToolSpec struct { Name, Description string; Schema json.RawMessage }
type Request struct {
    Model string; System string; Messages []Message; Tools []ToolSpec
    MaxOutputTokens int; ReasoningEffort string; SafetyID string; Extra map[string]any
}
type Usage struct { InputTokens, CachedInputTokens, OutputTokens, ReasoningTokens int }
type StreamEvent struct { // Type: text_delta, reasoning_summary_delta, tool_call_start, tool_call_delta, message_done, usage
    Type string; Delta string; ToolCallID, ToolName string; Message *Message; Usage *Usage; StopReason string
}
type Provider interface {
    Name() string
    Stream(ctx context.Context, req *Request) iter.Seq2[StreamEvent, error]
}
```

### Meta provider (`llm/meta`)

- Base URL `https://api.meta.ai/v1`, bearer `MODEL_API_KEY`. Default model `muse-spark-1.3-contributor`
  (also `muse-spark-1.2-contributor`; standard `muse-spark-1.3` etc. allowed by config). Contributor mode: Meta may train on content; 150 RPM.
- `POST /v1/responses`, `stream: true`, `store: false`, `include: ["reasoning.encrypted_content"]`, `instructions` = system prompt,
  `tools` as `{"type":"function","name","description","parameters"}`, `reasoning.effort` optional (never "none").
- Input mapping: user text → `{"role":"user","content":[{"type":"input_text"}]}`; assistant text → `output_text` (+ `phase` preserved);
  reasoning blocks → replay `Opaque` verbatim (must include `summary` and `encrypted_content`); tool_call → `function_call{call_id,name,arguments}`;
  tool_result → `function_call_output{call_id,output}`.
- Structure rules the adapter must uphold: every reasoning item followed by an assistant message or function_call before the next user
  message (insert a minimal assistant message if needed); function_call/function_call_output pairing; call_id 1–64 chars; assistant text
  preceding a function_call carries `phase:"commentary"`.
- SSE events consumed: `response.output_text.delta`, `response.output_item.added/done` (message, function_call, reasoning),
  `response.function_call_arguments.delta/done`, `response.reasoning_summary_text.delta`, `response.completed` (usage),
  `response.failed` / `error`.
- Retries on 429/5xx before the first byte: exponential backoff with jitter, honour `Retry-After`.
- Docs: https://dev.meta.ai/docs/protocols/responses, https://dev.meta.ai/docs/tool-calling, https://dev.meta.ai/docs/api-reference

### Tools

```go
type Tool interface {
    Spec() llm.ToolSpec
    Call(ctx context.Context, input json.RawMessage) (string, error)
}
func New[In, Out any](name, description string, fn func(ctx context.Context, in In) (Out, error)) Tool // schema from In; Out JSON-encoded (string passthrough)
func NewSet(tools ...Tool) *Set
```

### Embedding API

```go
a, err := agentkit.New(agentkit.Config{
    Name: "critic", Provider: meta.New(meta.Config{APIKey: key}), Model: "muse-spark-1.3-contributor",
    System: "...", Tools: tool.NewSet(t1, t2), Store: sqlitestore.New(sqlDB), MaxSteps: 20,
    Hooks: agentkit.Hooks{BeforeToolCall: func(ctx, *agentkit.ToolCall) error {...}, OnEvent: func(agentkit.Event) {...}},
})
sess, _ := a.NewSession(ctx, agentkit.SessionOptions{Title: "...", Metadata: map[string]any{"user_id": uid}})
for ev, err := range a.Run(ctx, sess.ID, agentkit.UserText("...")) { ... }
res, err := a.RunSync(ctx, sess.ID, agentkit.UserText("..."))
sub := agentkit.AsTool(otherAgent, "critic", "Critiques a text") // agents as tools
```

Events: `run_started`, `text_delta`, `reasoning_delta`, `tool_call_started`, `tool_call_finished`, `step_finished` (usage),
`run_finished`, `run_failed`.

Run loop: persist user message → stream model → persist assistant message → execute tool calls in parallel (per-call timeout,
panic recovery, tool errors become `tool_result{IsError:true}`; `BeforeToolCall` may veto) → persist results → repeat until no tool
calls, `MaxSteps`, or ctx cancel. One active run per session (`ErrSessionBusy`). Runs left `running` are marked `interrupted` by
`Store.RecoverInterrupted` at startup. `ContextPolicy` interface decides what history is sent (default: all; drop reasoning of
settled turns when over budget).

### Store

```go
type Store interface {
    CreateSession(ctx, *Session) error
    GetSession(ctx, id string) (*Session, error)
    ListSessions(ctx, ListOptions) ([]Session, error)
    DeleteSession(ctx, id string) error
    AppendMessages(ctx, sessionID, runID string, msgs ...llm.Message) error
    History(ctx, sessionID string) ([]StoredMessage, error)
    SaveRun(ctx, *Run) error
    SaveToolCall(ctx, *ToolCallRecord) error
    RecoverInterrupted(ctx) (int, error)
}
```

SQLite store takes a `*sql.DB` (the backend passes `gormDB.DB()`), creates its own tables idempotently:
`agent_sessions(id, agent, title, metadata JSON, created_at, updated_at)`,
`agent_messages(id, session_id, run_id, seq, role, blocks JSON, created_at)`,
`agent_runs(id, session_id, agent, model, status, error, input_tokens, cached_input_tokens, output_tokens, reasoning_tokens, started_at, finished_at)`,
`agent_tool_calls(id, run_id, session_id, call_id, name, input JSON, output, is_error, duration_ms, created_at)`.
Tests use `modernc.org/sqlite`.

### httpapi

`httpapi.New(agent, opts)` → `http.Handler`: `POST /sessions`, `GET /sessions`, `GET /sessions/{id}` (with messages),
`DELETE /sessions/{id}`, `POST /sessions/{id}/runs` (SSE: `event: <type>\ndata: <json>`), `POST /sessions/{id}/cancel`.
`Options.SessionOwner func(r) string` lets the host app scope sessions to the authenticated user (stored in session metadata).

### Backend wiring (`features/agents`)

Config `MODEL_API_KEY`, `AGENT_MODEL` (default `muse-spark-1.3-contributor`). Provides a demo `critic` agent, mounts
`httpapi` at `/api/v1/agents/critic` behind `auth.Required`, owner = `auth.UserID`. If `MODEL_API_KEY` is empty the routes return 503.

## 7. Testing

- `crud`: in-memory SQLite + `httptest` per op; filter/sort whitelist; scope; soft delete; partial PATCH.
- `auth`: code flow with a capturing mailer, attempt limit, cooldown, expiry, refresh rotation & reuse detection, middleware.
- `agentkit`: fake provider scripted tool loop on memory + sqlite stores; meta adapter against `httptest` SSE fixtures;
  live test behind `//go:build live` using `MODEL_API_KEY`.
- `fx.ValidateApp` test for the composition root.

## 8. Tooling

`Taskfile.yml` at `apps/backend`: `dev`, `run`, `test`, `lint`, `build`. `.env.example`. `.gitignore`.
