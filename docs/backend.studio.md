# Backend — studio (definitions, catalog, providers, agent runtime)

**State as of 2026-11-02.** Phases 3–5 of `plan.critique-engine.md` implemented. Working contract
was `tmp/studio-backend-spec.md`; this file is the durable reference now. Package layout:

| Package | Purpose |
|---|---|
| `internal/features/studio` | definition CRUD + immutable versions, typed bodies, per-kind validation, catalog, defaults seeding, export/import |
| `internal/features/llm` | models.dev catalog sync, enabled-provider state, AES-GCM keys, admin settings API, provider factory + RPM limiter |
| `internal/features/agentrun` | prompt assembly, agent build/cache, session-scoped VFS tools, preview/try/chat mount |
| `internal/models/{studio,llm}.go` | GORM models (`studio_defs`, `studio_def_versions`, `llm_catalogs`, `llm_providers`) |
| `agentkit/llm/{responses,chat,anthropic}` | provider adapters (see `docs/backend.agentkit-adapters.md` not needed — summarized below) |

Verify: `go build ./... && go vet ./... && go test <packages>` from `apps/backend` (the render
package's tests fail in this environment: root + Chrome sandbox — pre-existing). Frontend
`npm run typecheck` passes against these shapes.

## Definitions (studio)

- `StudioDef` (kind, slug unique per kind, name, description, draft JSON, draft_updated_at,
  latest_version, archived) + `StudioDefVersion` (immutable body, note, published_by, published_at).
  Publish bumps `latest_version`, clears the draft, inserts the version — one tx (`Store.Publish`).
- `Store` API: `List/Published/Get/GetBySlug/Versions/Version/LatestPublished/Create/PutDraft/
  Publish/Restore`. `DecodeBody[T]` decodes strictly (unknown fields rejected).

### HTTP (all admin; mounted at `/api/v1`)

| Route | Notes |
|---|---|
| `GET /studio/{kind}` | `{items: [StudioDef...], next_cursor: null}`, oldest first, `?limit`/`?offset` |
| `POST /studio/{kind}` | `{slug, name, description?, draft}` → 201; slug `^[a-z0-9][a-z0-9-]{0,99}$`; dup slug → 409 |
| `GET /studio/{kind}/{id}` | def; 404 otherwise |
| `PUT /studio/{kind}/{id}/draft` | body IS the typed body; validated (pipelines: structural decode only) → refreshed def |
| `POST /studio/{kind}/{id}/publish` | `{note}`; 400 "no draft to publish"; full validation → StudioVersion |
| `GET /studio/{kind}/{id}/versions[/{v}]` | versions newest first |
| `POST /studio/{kind}/{id}/restore/{version}` | copies body into draft → def |
| `GET /studio/catalog` | live catalog (below) |
| `GET /studio/export` / `POST /studio/import` | JSON bundle `{version:1, exported_at, defs:[{kind,slug,name,description,body,version?}]}` |
| `POST /studio/agents/{id}/preview` | `{input}` → `{system, input}` (no model call; draft-first) |
| `POST /studio/agents/{id}/try` | `{input}` → SSE run stream (ephemeral in-memory session) |
| `POST /studio/pipelines/{id}/validate` | `{ok, issues}` (draft, else latest published) |

Kinds: `model_set | agent | pipeline | skill | rule` — SINGULAR in CRUD paths; action routes use
the plural forms the frontend calls. The kind string is the path segment verbatim.

### Validation rules (validate.go)

- model_set: strategy ∈ single|fallback|fanout; ≥1 entry; provider_id+model_id non-empty;
  `reasoning_effort` ∈ minimal|low|medium|high|xhigh; `max_output_tokens` ≥ 0. No catalog lookup
  at save time (runtime enforces enablement).
- agent: model_set_id must exist (any state); contract type artifact|critique|custom; custom needs
  a JSON object schema with non-empty `properties`; limits ≥ 0; tools ⊆ catalog tool names;
  skill_ids/rule_ids must reference existing defs (checked in the controller against the store).
- skill: ≥1 file; exactly one root-level `SKILL.md`; paths relative with vfs.Clean semantics (no
  leading `/`, no `.`/`..`, no empty segments); unique; ≤ 512 KiB per file.
- rule: ≥1 group; group name non-empty; ≥1 rule per group; text non-empty; severity ∈
  info|warning|error; validator_code free-form.
- pipeline: full validation runs on publish and on `/validate` (`ValidatePipelineBody(ctx, body,
  *CatalogData) []PipelineIssue`, issues `{node_id, message, severity: error|warning}`): unique
  node ids, ≥1 node, edges reference existing nodes+ports, port-type compatibility, at most one
  loop edge and only from a Gate `continue` output (checked even when ports don't resolve), the
  non-loop graph must be a DAG (DFS), agent nodes warn on missing/unknown `agent_id`.

### Catalog (`GET /studio/catalog`, catalog.go)

`CatalogData{port_types, node_types, tools, models, model_sets, skills, rules, prompt_sections,
prompt_variables, validator_rule_codes}` (+ unexported `Agents` used by pipeline validation).
Node types: input, agent (generic superset ports), validate, render (width 1280/dpr 2 defaults),
merge, gate (continue/done outputs), output (strategy best|last). Tools are exactly what the
runtime wires: `read_file, write_file, edit_file, glob, grep` (fs), `run_js` (sandbox),
`validate_component`, `submit_artifact/submit_critique/submit_fields` (terminal). Models come from
`llm.Service.EnabledModels()`. Pickers (`model_sets`, `skills`, `rules`) list ALL defs of the kind,
drafts included. `validator_rule_codes` = `component.Codes()` (new accessor in
`internal/features/component/codes.go`).

### Defaults seeding (defaults.go, fx start hook)

When `studio_defs` is empty: model_set `default` (single meta/muse-spark-1.3-contributor), agents
`component-generator` (artifact, fs+run_js+validate), `vision-critic` (critique),
`code-critic` (critique), `critic` (critique, no tools), pipeline `default` (design loop:
Input → Generator → Render → Vision+Code critics → Merge → Gate → Output, gate.continue loop edge
back to generator.feedback, merge.critique → output.critique), rules `component-format` (cleanups/
structure/CSS groups referencing validator codes). All published, note "seeded",
published_by "system". Idempotent by emptiness.

## Providers (`internal/features/llm`)

- `LLMCatalog` single row (`llm_catalogs`): fetched_at, etag, raw api.json body.
- `LLMProvider` (`llm_providers`): provider_id unique, enabled, api_key_enc (AES-256-GCM, key =
  sha256("wondercritique/secrets/"+SECRETS_KEY); dev fallback derives from JWT_SECRET), base_url_vars
  (JSON), rpm (0 = unlimited).
- Sync: startup hook syncs when the snapshot is missing or older than 24h; network failure is a
  warning, never fatal. `Refresh` force-fetches (If-None-Match/ETag; 304 keeps the body). Prod
  refuses to start without SECRETS_KEY ≥ 32 bytes (startup hook, not config).
- Adapter resolution (`ResolveAdapter(npm, shape, id)`): `@ai-sdk/openai` or per-model
  `shape: responses` → responses; `@ai-sdk/openai-compatible`/`shape: completions`/openrouter/groq/
  xai/deepinfra/togetherai/cerebras/mistral/perplexity → chat; `@ai-sdk/anthropic` → anthropic;
  anything else → none (listed, cannot be enabled). Models of another type than chat are dropped;
  a model whose per-model shape resolves to a DIFFERENT adapter than its provider is dropped too
  (the provider builds one adapter for all its models). The snapshot's provider order is preserved
  (streaming JSON decode of the top-level object).
- Prices: models.dev `cost` (USD per 1M tokens: input/output/cache_read) is kept per model
  (`CatalogModel.Cost`); `Service.ModelCost(provider, model)` → `*ModelCost` (nil = unknown) with
  `Estimate(input, cached, output)`; the engine uses it for run cost and gate budgets.
- Base URL: provider `api` template with `${VAR}` placeholders filled from base_url_vars (missing
  var → unavailable); known defaults for first-party providers without `api` (openai/anthropic/meta).
- Key resolution: stored key → models.dev `env` var → legacy `MODEL_API_KEY` (meta only). Keys are
  never returned (`has_key` only).
- HTTP `/settings/providers` (admin): `GET` → `{synced_at?, catalog[], state[]}` (state: enabled,
  has_key, base_url_vars, rpm_limit null when unlimited); `PUT /{id}` `{enabled, api_key?, 
  base_url_vars, rpm_limit?}` (unknown → 404, none adapter → 400, negative rpm → 400; omit/empty
  api_key keeps the stored one) → updated state; `POST /{id}/test` → `{ok, detail?}` (1-token
  request against the provider's first model; 30s timeout); `POST /catalog/refresh`.
- `Service`: `SyncedAt()`, `Refresh(ctx)`, `Catalog()/CatalogEntry()`, `EnabledModels() []ModelInfo`
  (enabled providers only), `Provider(ctx, providerID) (llm.Provider, error)` (NotFound/Unavailable
  on unknown, none-adapter, disabled, keyless, missing base-url var; wrapped with the per-provider
  RPM limiter `rate.Every(time.Minute/rpm)`, burst 1), `TestProvider(ctx, id) (bool, string)`.
- Adapter construction (factory.go): `responses.New(responses.Config{APIKey, BaseURL, Name: providerID})`,
  same for `chat`/`anthropic`. DefaultModel stays empty — the runtime sends explicit model ids.
  The adapter `Name` (= provider id) is stamped on reasoning blocks, so replay works per provider.

## Agent runtime (`internal/features/agentrun`)

- **Prompt assembly** (prompt.go, exported): `AssembleSystem(system, ruleGroups, skills, contract)`
  appends `## Rules` (`### {group}` + `- {text}`), `## Skills` (`### {slug}` + SKILL.md + note that
  extra files are mounted read-only at `/skills/{slug}/`) and `## Output` per contract (artifact →
  submit_artifact; critique → submit_critique with `{strengths, issues[{severity,code?,message,
  suggestion?}], score 0-10}`; custom → field list derived from fields_schema + submit_fields).
  Empty sections are omitted. `BuildInput(template, samples, preview)` substitutes `{{name}}`
  (value in samples → substituted; missing KNOWN variable → ""; unknown placeholder → verbatim);
  empty template → `## {Key}` sections; image values (`data:image/...`, `wcblob://`) become
  `llm.ImageBlock`s after the text (in preview: `[image: {name}]`).
- **Body resolution**: Try/preview use the agent's DRAFT when present, else latest published; chat
  mount uses latest published only (404 unpublished). Model set: latest published body, fallback
  draft; single/fanout → first buildable entry, fallback walks the order on provider-build failure.
  (Parallel fanout is an engine concern, out of scope — noted in code.)
- **Agent construction** (runtime.go): one `*agentkit.Agent` per (def id, published version)
  cached under a mutex (`builds` map, key `defID|version`, version 0 = draft build). Config:
  Name = slug (or `try:{id}`), Provider from the llm Service, Model = entry.model_id, assembled
  System, tools per allowlist, shared sqlite store (chat) or in-memory store (try),
  MaxSteps = limits.max_steps (default 20), MaxOutputTokens/ReasoningEffort from entry params,
  Hooks.BeforeRequest = sha256[:8] of session metadata.owner as SafetyID + `wcblob://` images
  resolved to data: URLs via blobs.Store.
- **Tools** (tools.go): fstools (`read_file`…`grep`) and `run_js` (shared jsrun.Runner, Warmup at
  startup) operate on a session-scoped VFS: `sessionFS` resolves
  `agentkit.CallFromContext(ctx).SessionID` → per-session mount: `/work` rw (vfs/sqlite workspace
  `session:{id}`) + `/skills/{slug}/...` ro + `/rules/rules.md` ro (agent's rules as markdown).
  Mounts are cached per session (FIFO cap 256); the material (skills/rules content) is recorded per
  agent name at build time — try sessions register their ephemeral session id → name mapping.
  `validate_component` wraps `component.Validate` (JSON report + formatted text). Terminal submit
  tools are built from the output contract and wrapped in `tool.AsTerminal` (Workstream B
  semantics: the tool's output string becomes the run's final answer).
- **Endpoints** (controller.go): preview (draft-first), try (SSE framed exactly like
  `agentkit/httpapi`: `event: <type>` + JSON data, 15s heartbeats, final `event: done` with `{}`,
  client disconnect cancels), `/studio/pipelines/{id}/validate`, chat mount `/agents/{slug}`
  (auth required, NOT admin; unpublished → 404; provider unconfigured → 503 `unavailable`; serves
  `httpapi.New(agent, Options{SessionOwner: auth.UserID, Logger})` with the chi RoutePath →
  req.URL.Path rewrite before serving).

## Frontend contract reconciliation

`apps/frontend/src/api/{studio,settings,agents}.ts` match the implemented shapes (verified by
`npm run typecheck`); runs.ts is served by the engine (`docs/backend.engine.md`). Frontend "backend missing" callouts
for studio/settings pages can now be pointed at the real API.
