# Backend — studio build-out plan (2026-11-02)

Working plan for the studio-backend build (`plan.critique-engine.md` phases 3–5 + §6). The
detailed per-workstream contract lived in `tmp/studio-backend-spec.md`; the implemented state is
documented in `docs/backend.studio.md` and summarized per workstream below. This file tracks the
plan, cross-workstream contracts and status — it reflects the current state only.

## Status

| Workstream | Scope | Owned files | Status |
|---|---|---|---|
| Foundation | studio models/bodies/store, fx wiring, config | `internal/models/studio.go`, `internal/features/studio/{store,bodies,module,controller}.go`, stub modules for llm/agentrun | merged |
| A — provider adapters | agentkit LLM adapters: `responses` (extracted from `llm/meta`), `chat`, `anthropic` | `agentkit/llm/{responses,chat,anthropic}/**`, `agentkit/llm/internal/wire/**`, `agentkit/llm/meta/*` (preset refactor) | **done 2026-11-02** |
| B — agentkit terminal tools | `tool.Terminal` + `tool.AsTerminal`; run loop finishes on a terminal tool | `agentkit/tool/**`, `agentkit/run.go`, `agentkit/terminal_test.go` | **done 2026-11-02** |
| C — providers feature | models.dev catalog sync, admin settings API, AES-GCM keys, provider factory + RPM limiter | `internal/features/llm/**`, `internal/models/llm.go` | **done 2026-11-02** |
| D — studio core | CRUD + versions + restore, export/import, catalog, per-kind validation, defaults seeding | `internal/features/studio/**` (controller.go replaced, + validate/catalog/defaults) | **done 2026-11-02** |
| E — agent runtime | prompt assembly, agent build/cache, session VFS tools, preview/try/chat mount | `internal/features/agentrun/**` | **done 2026-11-02** |
| Integration | remaining wiring, full build/test pass, frontend typecheck, DB updates | cross-cutting | **done 2026-11-02** |

Verification (2026-11-02): from `apps/backend`, `go build ./... && go vet ./...` clean;
`go test ./...` green except `internal/features/render` (pre-existing: root + Chrome sandbox).
From `apps/backend/agentkit`, `go build/vet/test ./...` all green. Frontend `npm run typecheck`
green. `fx.ValidateApp(app.Module)` green. Nothing committed (per instructions).

## Shared rules

- Module path `github.com/dot5enko/wondercritique/apps/backend`; agentkit is its own module
  joined by go.work. Go 1.27. Errors via `apperr`; HTTP via `httpx.Handle/JSON/Decode[T]`;
  auth via `auth.Middleware.Required`/`auth.Role("admin")`; DB via `dbx` (GORM SQLite).
- Never start the server or touch `deploy/ansible` — deploys belong to a separate tool.
- Studio CRUD paths are SINGULAR, action routes are plural (frontend
  `apps/frontend/src/api/{studio,settings,runs,agents}.ts` is authoritative for HTTP shapes).

## Cross-workstream contracts (as implemented)

- **Terminal tools (B → E, D)**: `tool.Terminal` / `tool.AsTerminal(t)` — exact semantics below.
- **Adapter constructors (A → C, E)**: `responses.New(responses.Config{...})`,
  `chat.New(chat.Config{...})`, `anthropic.New(anthropic.Config{...})` — Config carries
  APIKey/BaseURL/DefaultModel/HTTPClient/MaxRetries/UserAgent (plus an optional `Name` provider
  identity used on reasoning blocks and error prefixes; the providers factory sets it to the
  models.dev provider id). All satisfy `llm.Provider`, stream deltas and finish with exactly one
  `StreamMessageDone`. Shared HTTP/SSE plumbing lives in `agentkit/llm/internal/wire`
  (APIError with Retry-After handling, retry-with-backoff before the first stream byte, SSE event
  splitting). `llm/meta` is a thin preset over `responses` (its existing tests pass unchanged).
- **Provider service (C → D, E)**: `llm.Service` — `SyncedAt()`, `Refresh(ctx)`, `EnabledModels()
  []ModelInfo`, `Provider(ctx, providerID) (llm.Provider, error)` (NotFound/Unavailable on unknown,
  unsupported, disabled or keyless; wrapped with the per-provider RPM limiter), `TestProvider(ctx,
  providerID) (bool, string)`. Adapter `Name` = provider id (reasoning-block identity).
- **Pipeline validation (D → E)**: `studio.ValidatePipelineBody(ctx, body, *CatalogData)
  []PipelineIssue` (`PipelineIssue{NodeID, Message, Severity}`); `studio.BuildCatalogData(ctx,
  store, llmService)` builds the catalog for external callers (the agentrun validate route).
- **studio.Store (foundation → D, E)**: `List/Published/Get/GetBySlug/Versions/Version/
  LatestPublished/Create/PutDraft/Publish/Restore`; publish bumps `latest_version`, clears
  the draft and inserts the immutable version in one tx.
- **Catalog tool list (D defines, E wires)**: `read_file`, `write_file`, `edit_file`, `glob`,
  `grep` (flags `["fs"]`), `run_js` (`["sandbox"]`), `validate_component` (`["validate"]`),
  `submit_artifact`, `submit_critique`, `submit_fields` (`["terminal"]`).

## Workstream A — implementation notes (2026-11-02)

- `agentkit/llm/responses`: the Responses-API implementation extracted from `llm/meta`
  (streaming, tool calls, encrypted reasoning replay, retries with backoff on 429/5xx before the
  first stream byte, `Retry-After`). Spec'd Config fields plus `Name` (provider identity stamped
  on reasoning blocks / used in error prefixes; default `responses`). `BuildInput`/`BuildInputAs`
  exported. BaseURL defaults to `https://api.meta.ai/v1` per spec.
- `agentkit/llm/chat`: OpenAI Chat Completions — SSE with `stream_options.include_usage`, tool
  calls assembled from deltas incl. parallel calls, `reasoning_content` mapped to reasoning blocks
  (Opaque `{"reasoning_content": ...}`, replayed back as the assistant field; foreign-provider
  reasoning skipped), `reasoning_effort` + `max_tokens` mapped, finish_reason → stop reason
  (tool_calls/length), in-stream `{"error":...}` → `chat.StreamError`.
- `agentkit/llm/anthropic`: Messages API — SSE `message_start/content_block_*/message_delta/
  message_stop/ping`, thinking blocks → reasoning with `{"thinking","signature"}` Opaque (replayed
  as thinking content), tool_use with `input_json_delta` assembly (inline block input as fallback),
  `x-api-key` + `anthropic-version: 2023-06-01` headers, `max_tokens` required (default 8192),
  ReasoningEffort → extended-thinking budget (minimal 1024…xhigh 32768; budget+1024 floors
  max_tokens), image data: URLs → base64 source, https → url source, usage merged from
  message_start + message_delta.
- `agentkit/llm/internal/wire`: shared APIError/parse/retry/backoff/SSE-drain helpers.
- Tests: httptest wire-format/streaming/error/retry suites for all three; `llm/meta` tests
  unchanged and green.

## Terminal tool semantics (implemented, agentkit)

As implemented 2026-11-02 in `agentkit/tool/terminal.go` + `agentkit/run.go`:

- `tool.Terminal` is `interface { Tool; IsTerminal() bool }`; `tool.AsTerminal(t Tool) Tool`
  returns a delegating wrapper whose `IsTerminal()` is true. A tool may also implement the
  interface directly.
- In the run loop, after a step's tool results are collected AND persisted (assistant
  tool_call message and tool_result message are stored as usual), if any executed tool call
  in the step reached a terminal tool, the run finishes as `completed` in that same step:
  `Result.Text` and the `run_finished` event carry that tool's output string, usage
  accumulates as usual, and NO further model call happens (a scripted next turn stays
  unconsumed).
- The error flag is irrelevant: when a terminal tool call fails, the error text is what the
  run records as the tool result output, so that text becomes the final answer and the run
  still completes (it never fails because of a terminal tool's error).
- Calls that never executed do not end the run: unknown tools and calls vetoed by
  `BeforeToolCall` keep the loop going. If several terminal calls execute in one step, the
  first in call order wins.
- Cancellation still wins: a consumer break, `Agent.Cancel` or parent-context cancellation
  during/around the terminal step fails/cancels the run as usual (the tool result stays
  persisted so call/result pairs remain intact); an abandoned run is never reported completed.
- `MaxSteps` semantics unchanged: non-terminal loops still end with `ErrMaxSteps`; terminal
  runs count the model steps they used (`Result.Steps`).
- Tests: `agentkit/terminal_test.go` (fake provider, patterned on `agent_test.go`): terminal
  tool ends the run with one model step, direct interface implementation, non-terminal tool
  still loops back to the model, failed terminal call completes with the error text, vetoed
  terminal call continues, cancellation during a terminal call, max-steps unchanged.

## Workstream C — implementation notes (2026-11-02)

See `docs/backend.studio.md` § Providers. Notable decisions beyond the spec:
- `llm_providers.base_url_vars` uses the GORM json serializer; PUT with `base_url_vars: null`
  keeps the stored vars.
- Per-model `shape` overrides that disagree with the provider adapter drop the model from the
  registry (single adapter per provider).
- `EnabledModels()` reads enablement with a detached context (catalog endpoint convenience).
- `TestProvider` maps catalog/enablement/key problems to `(false, detail)`; the controller
  answers 404/400 for unknown/none-adapter before testing.

## Workstream D — implementation notes (2026-11-02)

See `docs/backend.studio.md`. Notable decisions:
- `PUT /{kind}/{id}/draft` takes the raw typed body (validated per kind; agent cross-refs are
  checked against the store at draft save too).
- Publish validates fully: pipelines go through `ValidatePipelineBody` with a live catalog;
  failures surface as 400 `invalid` with the joined issues.
- Import: per-entry errors don't abort (`{imported, skipped, errors:[{slug,error}]}`);
  `skipped` counts malformed/unknown-kind/empty entries; duplicate slugs within a bundle are
  per-entry errors. Existing defs are published as a new version (note "imported") AND their
  draft is overwritten. Bundle entries that reference other defs by id (agent → model_set)
  only import into a database where those ids exist — cross-database imports of agent/pipeline
  bundles need id remapping (future work).
- `SKILL.md` is required exactly once at the skill root; nested `sub/SKILL.md` files are
  ordinary files.
- Seeded pipeline wires `gate:done → output:html` and `merge:critique → output:critique`
  (port-type compatible; the plan's diagram implied gate→output critique which the port types
  forbid).

## Workstream E — implementation notes (2026-11-02)

See `docs/backend.studio.md` § Agent runtime. Notable decisions:
- Prompt placeholder rule: value present in samples → substituted (images become blocks);
  missing KNOWN catalog variable → ""; unknown placeholder → left verbatim (spec's two clauses
  both honored).
- Rules/skills load at build time from latest published body, falling back to the draft
  (consistent with model sets). Missing/deleted refs are tolerated silently.
- Try runs build a fresh agent per request over an in-memory store (name `try:{id}`, version 0
  key, not cached); the ephemeral session id is registered so the session VFS still mounts.
- Chat mount caches one agent per (def id, version) in-process; a new published version is
  picked up on the next build (cache key includes the version).
- `/studio/pipelines/{id}/validate` validates the draft (else latest published) and answers
  `{ok, issues}`.
