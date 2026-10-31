# Backend

## Auth

Package `internal/features/auth` (`auth.Module`). Login goes through pluggable providers (the built-in one is
`email_code`) and returns a short-lived JWT access token plus a rotating opaque refresh token.

### Endpoints (`/api/v1/auth`)

| Method | Path | Body | Response |
|---|---|---|---|
| POST | `/{provider}/start` | provider specific; `email_code`: `{"email"}` | `email_code`: `{"sent":true,"expires_in":600}` |
| POST | `/{provider}/verify` | provider specific; `email_code`: `{"email","code"}` | token pair (below) |
| POST | `/refresh` | `{"refresh_token"}` | new token pair; the old refresh token is now invalid |
| POST | `/logout` | `{"refresh_token"}` | 204, revokes that refresh token |
| GET | `/me` | (`Authorization: Bearer <access>`) | the user |

Token pair: `{"access_token","refresh_token","token_type":"Bearer","expires_in":900,"user":{"id","email","name","role",...}}`.

Errors use the shared shape `{"error":{"code","message","fields"}}`. Unknown provider → 404; bad or expired code →
401 `invalid or expired code` (the same message every time, so it doesn't reveal whether the email exists); resend
within 60 s or per-IP rate limit → 429 with a `Retry-After` header; mail delivery failure → 503.

```sh
curl -XPOST localhost:8080/api/v1/auth/email_code/start  -d '{"email":"me@example.com"}'
# dev: the code is printed in the server log
curl -XPOST localhost:8080/api/v1/auth/email_code/verify -d '{"email":"me@example.com","code":"123456"}'
```

### Rules

- **Email code**: 6 digits from `crypto/rand`, stored as HMAC-SHA256 (key derived from `JWT_SECRET`), valid 10 min,
  5 verify attempts, 60 s resend cooldown per email; a new code invalidates earlier ones. Emails are trimmed and
  lowercased.
- **Users**: verify looks up the identity (provider + subject). If there is none, it links to the user with the same
  email, or creates that user (role `user`). Soft-deleted users get 403.
- **Access token**: HS256 only, 15 min, claims `sub` (user id), `role`, `iss`, `iat`, `exp`, `jti`; issuer is checked,
  30 s clock leeway.
- **Refresh token**: 32 random bytes (base64url), stored as SHA-256, valid 30 days, rotated on every use. Presenting an
  already rotated token counts as theft: all of that user's active refresh tokens are revoked and the call returns 401.
  A token revoked by logout just returns 401.
- **Rate limit**: per client IP (after `RealIP`) on `start`/`verify`: burst 20, then one request per 3 s. It is in
  memory and per process.

### Protecting routes

Inject `*auth.Middleware`; the context helpers are package-level:

```go
type Controller struct{ auth *auth.Middleware }

func NewController(m *auth.Middleware) *Controller { return &Controller{auth: m} }

func (c *Controller) Routes(r chi.Router) {
    r.With(c.auth.Required).Get("/things", list)                          // 401 without a valid token
    r.With(c.auth.Required, auth.Role("admin")).Delete("/things/{id}", del) // 403 for other roles
    r.With(c.auth.Optional).Get("/public", pub)                           // anonymous OK; an invalid token is still 401
}

func list(w http.ResponseWriter, r *http.Request) {
    uid := auth.UserID(r.Context())        // "" when anonymous
    claims, ok := auth.ClaimsFrom(r.Context()) // *auth.Claims{Role, RegisteredClaims}
    _, _, _ = uid, claims, ok
}
```

In tests or background jobs, use `auth.WithClaims(ctx, &auth.Claims{...})` to act as a user.

### Adding a provider

```go
type googleProvider struct{ /* deps */ }

func newGoogleProvider(cfg *config.Config) *googleProvider { return &googleProvider{} }

func (p *googleProvider) Name() string { return "google" } // → /auth/google/start|verify

func (p *googleProvider) Start(ctx context.Context, raw json.RawMessage) (auth.StartResult, error) {
    return auth.StartResult{"redirect_url": "..."}, nil
}

func (p *googleProvider) Verify(ctx context.Context, raw json.RawMessage) (auth.Identity, error) {
    req, err := auth.DecodeRequest[struct {
        Code string `json:"code" validate:"required"`
    }](raw)
    if err != nil {
        return auth.Identity{}, err
    }
    // exchange req.Code, check the ID token...
    return auth.Identity{Provider: "google", Subject: googleSub, Email: verifiedEmail, Name: name}, nil
}

// in an fx module:
auth.AsProvider(newGoogleProvider)
```

Only return emails the provider has **verified**, because users are linked by email. Return `*apperr.Error` values
for client errors. Provider names must be unique; startup fails on a duplicate.

### Configuration

| Variable | Notes |
|---|---|
| `JWT_SECRET` | ≥ 32 bytes, required in prod. Empty in dev → a random secret per process (logged warning; tokens don't survive restarts). Separate JWT and code-HMAC keys are derived from it. |
| `JWT_ISSUER` | `iss` claim, default `wonder-critique`. |
| `RESEND_API_KEY` | Sends email via Resend. If empty, dev logs emails (including codes) and prod refuses to start. |
| `MAIL_FROM` | Sender, required when `RESEND_API_KEY` is set, e.g. `Wonder Critique <noreply@example.com>`. |

Tests can `fx.Decorate` `auth.Clock`, `mailer.Mailer` and `auth.RateLimit` (see `auth_test.go`).

## Adding a CRUD resource

Package `internal/crud` (`crud.Module`, provides `*crud.Registry`) mounts REST endpoints for any GORM model that
embeds `models.Base`. `internal/features/messages` is the reference example.

1. Model in `internal/models`, with field policy in `crud` tags (json names are what clients use):

   ```go
   type Message struct {
       models.Base
       ThreadID string `json:"thread_id" crud:"create,filter"      validate:"required"`
       Role     string `json:"role"      crud:"create,filter,sort" validate:"oneof=user assistant system"`
       Body     string `json:"body"      crud:"create,update"      validate:"required,max=20000"`
       OwnerID  string `json:"owner_id"` // no crud tag: server-set only
   }
   ```

   `create`/`update` = writable in POST/PATCH bodies, `filter`/`sort` = usable in `filter[...]`/`sort`. `id`,
   `created_at`, `updated_at` are always filterable and sortable and never writable; `deleted_at` is hidden.
   Columns come from GORM's schema parser; metadata is cached per type. Bad tags panic at startup.

2. Feature module: register the model for migration and a controller.

   ```go
   var Module = fx.Module("messages", dbx.AsModel(&models.Message{}), httpx.AsController(NewController))

   type Controller struct{ crud *crud.Registry }
   func NewController(reg *crud.Registry) *Controller { return &Controller{crud: reg} }

   func (c *Controller) Routes(r chi.Router) {
       api := c.crud.On(r)
       res := crud.NewForType[models.Message](api, "/messages" /*, options... */)
       res.Route(func(r chi.Router) { r.Post("/{id}/archive", c.archive) }) // same prefix + Use middleware
       _ = res.Service // *crud.Service[models.Message] for custom logic
   }
   ```

3. Add `messages.Module` to `internal/app/app.go` under `// feature modules`.

### Endpoints

| Op | Route | Behaviour |
|---|---|---|
| `OpList` | `GET /prefix` | `{"items":[...],"next_cursor":"..."}` (see below) |
| `OpGet` | `GET /prefix/{id}` | 404 if missing, soft-deleted or outside `Scope` |
| `OpCreate` | `POST /prefix` | 201. Unknown or non-`create` keys → 400 with `fields`; `BeforeCreate` hooks run before validation |
| `OpUpdate` | `PATCH /prefix/{id}` | Partial: only keys present in the body are written, zero values included (`{"count":0}` works). Non-`update` keys → 400. The merged entity is validated |
| `OpDelete` | `DELETE /prefix/{id}` | 204; soft delete (`models.Base` has `gorm.DeletedAt`) |

Mutations run in `dbx.Tx`; hooks get the tx through ctx (`dbx.Conn(ctx, db)`), and a hook error rolls back.

### Listing

- `limit` (default 50, clamped to `MaxLimit`, default 200), `offset`, `cursor`, `sort`, `filter[field]=v`,
  `filter[field][op]=v` with ops `eq ne gt gte lt lte in like` (`in` takes a comma list; `like` is SQL LIKE on text
  fields, the client supplies `%`; timestamps are RFC 3339). Unknown or non-whitelisted filter/sort keys → 400.
- `sort=-created_at,role`: comma list, `-` = descending. The id is always appended as a tie-break in the
  direction of the last key. Default: `-created_at` (i.e. `-created_at,-id`), or `DefaultSort(...)`.
- **Cursor paging** (keyset) works when the effective sort is `created_at` or `id` (either direction). The response has
  `next_cursor` while more rows exist; pass it back as `?cursor=`. Cursors are opaque, stay valid if their row is
  deleted, and only resolve within the caller's `Scope`.
- **Any other sort uses offset paging**: pass `?offset=`; `next_cursor` is never returned, and `?cursor=` is a 400.
  `offset` together with `cursor` is a 400.

### Options

Generic and non-generic options go in one list; a generic option for the wrong model panics at registration.

```go
crud.NewForType[models.Thread](api, "/threads",
    crud.Use(authMW.Required),                        // middleware on all routes (incl. Resource.Route)
    crud.UseFor(crud.OpDelete, requireAdmin),         // middleware on one op
    crud.Only(crud.OpList, crud.OpGet, crud.OpCreate), // mount only these ops (others → 405)
    crud.Scope[models.Thread](func(ctx context.Context, q *gorm.DB) *gorm.DB {
        return q.Where("owner_id = ?", auth.UserID(ctx)) // applied to every op
    }),
    crud.WithHooks(crud.Hooks[models.Thread]{
        BeforeCreate: func(ctx context.Context, t *models.Thread) error { t.OwnerID = auth.UserID(ctx); return nil },
        // AfterCreate, BeforeUpdate(ctx, existing, patch map[string]any), AfterUpdate, BeforeDelete, AfterDelete
    }),
    crud.Authorize[models.Thread](func(ctx context.Context, op crud.Op, t *models.Thread) error {
        return nil // t is nil for OpList; return apperr.Forbidden(...) to deny
    }),
    crud.MaxLimit(100),
    crud.DefaultSort("-updated_at"),
)
```

- Order: create = `BeforeCreate` → validate → `Authorize` → insert → `AfterCreate`; update = load in scope →
  `Authorize` → `BeforeUpdate` → apply patch → validate → write → `AfterUpdate`; delete = load in scope → `Authorize`
  → `BeforeDelete` → delete → `AfterDelete`.
- `BeforeUpdate`'s patch is keyed by json name with decoded Go values; it may change or add keys, including untagged
  server-side fields (not `id`).
- `crud.NewService[T](db, validator, opts...)` builds the service without HTTP (`Use`/`UseFor`/`Only` are ignored;
  a nil validator uses the shared one). Methods: `List`, `Get`, `Create`, `Update`, `Delete`, and `Query(ctx)` for
  the scoped base query.

## Messages

Package `internal/features/messages` (`messages.Module`). A worked example of the CRUD layer: `GET/POST /api/v1/messages`,
`GET/PATCH/DELETE /api/v1/messages/{id}`, all behind `auth.Required`, scoped with `owner_id = <authenticated user>`
(set by a `BeforeCreate` hook; the `owner_id` field is not writable). List supports `filter[thread_id]` and `sort=role`.

## Agents (`/api/v1/agents/critic`)

Package `internal/features/agents` (`agents.Module`) wires the `agentkit` SDK into the app: a `critic` agent using
the Meta Model API, mounted from `agentkit/httpapi` behind `auth.Required`. Sessions are scoped to the authenticated
user (`metadata.owner`, derived from the access token; other users' sessions 404). Without `MODEL_API_KEY` the routes
answer 503 `unavailable` instead of failing at startup.

## agentkit (separate module)

`agentkit` is an embeddable agent SDK with its own `go.mod` (joined by the root `go.work`); it has no fx or chi
dependency in its core. See `agentkit/README.md` for the embedding API.

- `agentkit/llm` — provider-neutral messages (text, image, tool calls, tool results, reasoning with opaque
  provider payloads) and the streaming `Provider` interface.
- `agentkit/llm/meta` — Meta Model API via the Responses API: `store:false`, stateless encrypted reasoning replay
  (the model's chain of thought travels with the history, nothing is stored at Meta), streamed tool-call arguments,
  retries with backoff on 429/5xx before the stream starts. Default model `muse-spark-1.3-contributor`
  (**contributor mode: heavily discounted tokens; in exchange Meta may use request content and responses to train
  future models**, and the limit is 150 requests/min). Standard-mode models can be selected via `AGENT_MODEL`.
- `agentkit/tool` — typed tools: `tool.New(name, desc, func(ctx, in In) (Out, error))` derives the JSON schema
  from `In`.
- `agentkit/store` + `store/sqlite` / `store/memory` — persistence for sessions, messages, runs and tool calls,
  sharing the app's SQLite file (tables prefixed `agent_`).
- `agentkit/httpapi` — HTTP API over an agent: session CRUD and runs streamed as SSE.
- `agentkit/fxagent` — fx wiring (`fxagent.Provide("critic", ctor)`, `fxagent.SQLiteStore()`).

### Agent HTTP endpoints (mounted at `/api/v1/agents/critic`)

| Method | Path | Notes |
|---|---|---|
| POST | `/sessions` | `{"title","metadata"}` |
| GET | `/sessions` | `?limit`, `?offset` |
| GET | `/sessions/{id}` | session messages |
| DELETE | `/sessions/{id}` | 204 |
| POST | `/sessions/{id}/runs` | `{"input":"text"}`; SSE stream of run events (final `event: done`); `?stream=false` returns the JSON result |
| POST | `/sessions/{id}/cancel` | 204; cancels the active run |

A busy session answers 409. The run loop persists every message and tool call; a run interrupted by a process
crash is marked `interrupted` on next start.
