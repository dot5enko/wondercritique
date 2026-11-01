# Wondercritique — critique engine plan

Drafted 2026-11-01; revised 2026-11-01 (visual Studio, models.dev registry, decisions settled — see §11).

## 0. Baseline

Exists: agentkit (tool loop, typed tools, SQLite store, Meta Responses-API provider with image input, httpapi,
fx), auth (roles, `auth.Role("admin")`), CRUD registry, SQLite via GORM, a hardcoded demo `critic` agent.
Frontend: React 19 + Tailwind design system, no router, no API layer yet.

agentkit constraints (docs/backend.md): `Config` fixed per `*Agent` but cheap to build → one agent per node run;
tools return strings only → images go through messages; a run ends only without tool calls → terminal tools.

## 1. Architecture

```
 Settings UI ─► Providers: models.dev catalog → enable providers (keys, base URL vars, rate limit)
 Studio UI   ─► Definitions (DB, drafts + immutable versions): model sets · agents · pipelines (graph) ·
                skills · rules — forms driven by GET /studio/catalog
 Runs UI     ─► POST /runs ─► Engine: pins versions, runs the graph per iteration
                  Agent node    ─► agentkit.Agent (provider adapter picked from models.dev metadata)
                                   tools: fs.* (VFS) · run_js (wasm) · validate_component · submit_*
                  Validate node ─► component validator (Go)
                  Render node   ─► Chromium on the same host ─► PNG + metrics ─► blobs
                  Merge / Gate / Output nodes
                  events ─► SSE (live graph)
 Chat API    ─► /api/v1/agents/{slug}/… any published Studio agent through agentkit/httpapi (replaces demo critic)

 Agent workspace (VFS, SQLite): /work rw · /skills ro · /rules ro · /renders ro · /history ro
```

## 2. Building blocks

- **VFS** `agentkit/vfs`: `Stat/Read/Write/Delete/List`; cleaned absolute paths, quotas; memory + sqlite impls,
  `Mount` overlay, `ReadOnly`, conformance suite. Mutable scratch; iteration outputs snapshotted to `artifacts`.
- **FS tools** `agentkit/tools/fstools`: `read_file`, `write_file`, `edit_file` (exact-match), `glob`, `grep`.
- **JS sandbox** `agentkit/sandbox/jsrun`: QuickJS-NG on wazero (`fastschema/qjs`), `run_js`; memory/time/output
  caps, no fs/net, host API = VFS + console. Spike first.
- **Component validator** `internal/features/component`: rule codes from the platform guide (+ `on*=`,
  `javascript:`), post-render `stage-height`/`no-stage-wrapper`. Tool, submit gate, Validate node, critic report.
- **Renderer** `internal/features/render`: chromedp + headless Chromium **on the backend host**; renderer-owned
  canvas (reset, centering, 220 px stage); CSP `script-src 'none'`, page JS off; network allowlist (Google Fonts
  + chosen hosts) + private-IP block (SSRF); fresh context per render; timeouts; Chromium sandbox kept.
  Ansible role installs Chromium + fonts (authored only).
- **Images into models**: `wcblob://<sha>` image blocks resolved to `data:` URLs in `BeforeRequest`.
- **agentkit extensions** (approved): terminal tools, provider adapters + registry, image tool results (later),
  per-step usage for budgets.

## 3. LLM providers from models.dev

See docs/llm.models-catalog.md for the catalog format.

### 3.1 Catalog sync

- Fetch `https://models.dev/api.json` at startup (if the snapshot is older than 24 h), daily, and on "Refresh" in
  the UI. Store the raw snapshot in `llm_catalog(fetched_at, etag, body)` so startup works offline; parse into an
  in-memory registry.
- Keep chat models only (`type` absent or `chat`); `deprecated` hidden by default (toggle to show).
- Capabilities from the catalog: vision (`modalities.input` ∋ `image`), tools (`tool_call`), reasoning options,
  context/output limits, cost (used for run cost estimates and budgets).

### 3.2 Adapters (agentkit `llm/*`)

Wire protocol chosen from `npm` (+ per-model `provider.{npm, api, shape}` override):

| Adapter | Covers | Notes |
|---|---|---|
| `responses` (OpenAI Responses API) | `@ai-sdk/openai`, `shape: responses` → openai, **meta**, gateways | generalize the existing `llm/meta` provider (base URL + key); Meta = preset |
| `chat` (OpenAI Chat Completions) | `@ai-sdk/openai-compatible` (196 providers), `shape: completions`, openrouter, groq, xai, deepinfra, togetherai, cerebras, mistral, perplexity | base URL from `api`, known defaults for SDK-specific providers without `api` |
| `anthropic` (Messages API) | `@ai-sdk/anthropic` providers | |
| `google` (Gemini) | `@ai-sdk/google` | later |

Providers with no adapter are listed but cannot be enabled. `${VAR}` placeholders in `api` become fields in the
provider settings.

### 3.3 Enabled providers (admin settings, not versioned)

`llm_providers(provider_id, enabled, api_key_enc, base_url_vars, rpm_limit, updated_at)`
- Admin picks providers from the catalog, enters the key (or uses the env var named by models.dev `env`, e.g.
  `META_MODEL_API_KEY`), "Test connection" (tiny request).
- API key storage: AES-GCM in DB with a new `SECRETS_KEY` env (see §11 open item); env var fallback.
- Per-provider rate limiter (`rpm_limit`, Meta default 150).

### 3.4 Model sets (Studio kind `model_set`, versioned)

A named, ordered list of models picked **only from enabled providers**, filtered by required capabilities, with:
strategy `single | fallback | fanout`, per-entry params (reasoning effort from the model's `reasoning_options`,
max output tokens). Agents and Agent nodes pick a model set; the UI only offers sets that satisfy the agent's
needs (vision, tools).

Default (production too): set `default` = `meta/muse-spark-1.3-contributor` (contributor mode on by default).

## 4. Studio — visual configuration

### 4.1 Storage

```
studio_defs          id, kind, slug, name, description, draft (json), draft_updated_at, latest_version, archived
studio_def_versions  def_id, version, body (json), note, published_by, published_at      -- immutable
```

`kind` ∈ `model_set | agent | pipeline | skill | rule`. Draft (autosave) → publish (immutable version + note) →
history, diff, restore-as-draft. Runs pin versions; Studio test runs may use drafts. Bodies are typed Go structs
validated on save/publish. Defaults inserted from Go code once on an empty DB, then ordinary editable
definitions; JSON bundle export/import for dev → prod.

Access: all `/studio` and provider settings routes behind `auth.Role("admin")`.

### 4.2 Catalog — `GET /studio/catalog`

Node types (typed ports, config JSON schema), port types, tools (descriptions, flags), enabled models with
capabilities, prompt sections/variables, validator rule codes. Settings forms are generated from schemas; the
pipeline graph is the only custom editor.

### 4.3 Prompts — plain markdown

- System prompt and input template are **plain textareas** (design-system `Textarea`), written as markdown.
- No template language required: the engine appends standard sections automatically:
  `## Rules` (attached rules), `## Skills` (name + description + `/skills/<slug>/SKILL.md`), `## Output`
  (submit contract). The input message is built from the wired input ports as markdown sections
  (`## Brief`, `## Previous critique`, `## Current file`) + images.
- Optional `{{port_name}}` placeholders for full control over where an input lands (the available names are
  listed next to the textarea).
- "Show assembled prompt" button: backend returns the final system + input text for sample data.

### 4.4 Agent editor

Identity · model set · system prompt · input template · tools checklist · skills/rules multi-select · output
contract (artifact / critique / custom fields → JSON schema → submit tool) · limits · **Try it** (one-off run on
sample input or a past artifact, streamed transcript) · history/diff/publish.

### 4.5 Pipeline editor — node graph (`@xyflow/react`)

| Node | Inputs → Outputs | Config |
|---|---|---|
| Input | → brief (text), references (images) | form fields shown when starting a run |
| Agent | ports from the agent's input/output contract | agent + version pin, model set override |
| Validate | html → report | rule set |
| Render | html → images, metrics | variants (width, background, color scheme, DPR) |
| Merge | critique×N → critique | weights, issue dedupe |
| Gate | critique → continue / done | score ≥ X, max iterations, no-improvement patience, budget; advanced `expr-lang` |
| Output | html, critique → | best by score / last |

Wires only connect compatible port types. Gate `continue` is a dashed **loop edge** back to e.g. the generator's
`feedback` port (previous iteration's value, empty on iteration 1); otherwise the graph must be a DAG (validated
server-side, errors shown on nodes). Ready nodes run in parallel.

```
Input ─brief─► Generator ─html─► Render ─images─► Vision critic ─┐
                  ▲          └──html──────────► Code critic ───┴─► Merge ─► Gate ─done─► Output
                  └──────────────── feedback (loop) ◄──────────────────────────┘ continue
```

### 4.6 Skills, rules

Skills: file tree + textarea per file (SKILL.md, references, `*.js`), "run script" in the sandbox; a version =
manifest of blob hashes; mounted at `/skills/<slug>/`. Rules: grouped list, text, severity, optional validator code.

### 4.7 API (`/api/v1/studio`, admin)

```
GET    /catalog
GET    /{kind}            POST /{kind}
GET    /{kind}/{id}       PUT  /{kind}/{id}/draft      POST /{kind}/{id}/publish {note}
GET    /{kind}/{id}/versions[/{v}]                     POST /{kind}/{id}/restore/{v}
POST   /agents/{id}/preview      POST /agents/{id}/try (SSE)      POST /pipelines/{id}/validate
GET    /export   POST /import
/api/v1/settings/providers: GET catalog+state, PUT /{provider} (enable, key, vars, rpm), POST /{provider}/test,
                            POST /catalog/refresh
```

## 5. Engine and runs

- Tables: `runs` (owner, pipeline_version_id, input, status, iteration, best_artifact_id, usage, cost,
  workspace_id, is_test, error), `run_steps` (run, iteration, node_id, status, pinned versions, model,
  agent_session_id, output, usage, error, times), `artifacts`, `blobs`. Transcripts in `agent_*` tables.
- Scheduler: per iteration run ready nodes in parallel, persist each step, gate loops/finishes, keep the best
  iteration; worker pool; per-provider rate limiters; token/cost budget from models.dev prices.
- Crash → `interrupted`; later resume from the last completed step.
- API: `POST /runs`, `GET /runs`, `GET /runs/{id}`, `GET /runs/{id}/events` (SSE), `POST /runs/{id}/cancel`,
  `GET /artifacts/{id}/content`.
- Runs UI: start (pipeline + Input form) → live read-only graph → iterations with shots side by side, critiques,
  score chart, per-node transcripts → compare runs/iterations.

## 6. Chat with any agent (replaces the demo critic)

Remove the hardcoded `critic` (`internal/features/agents`, `MODEL_API_KEY`/`AGENT_MODEL` config). Mount
`/api/v1/agents/{slug}/…` (existing httpapi surface: sessions, runs SSE, cancel): resolves the published agent,
builds and caches one `*agentkit.Agent` per agent version, sessions owner-scoped as today. The old critic becomes
a default Studio agent you can edit or extend.

## 7. Admin bootstrap

`ADMIN_EMAILS` env (comma list), set to `sergiydotsenko@gmail.com` in `.env.example` and Ansible vars. At
startup each address is upserted as a user with role `admin` (created if missing, promoted if present). Login
stays the existing email-code flow (identity links by email).

## 8. Built-in defaults

Model set `default` (muse-spark-1.3-contributor) · agents `component-generator`, `vision-critic`, `code-critic`,
`critic` (chat) · starter pipeline (§4.5) · component-format rules from the platform guide.

## 9. Security

Tools only, per-agent allowlist, audit; per-run VFS with RO mounts and quotas; JS in wasm without fs/net, with
caps; HTML validated before render, CSP + JS off + network allowlist + private-IP block; API keys encrypted at
rest, never returned by the API (masked); admin-only Studio/settings; per-run budget, max iterations/steps,
per-provider rate limits.

## 10. Phases

1. Foundations, no LLM: VFS, fstools, validator, renderer + SSRF tests, blobs.
2. Sandbox spike → `run_js`.
3. agentkit: terminal tools, adapters (`responses` from `meta`, `chat`, `anthropic`), registry, blob images.
4. Providers: models.dev sync, enabled providers, key storage; admin bootstrap.
5. Studio backend: defs/versions, typed bodies, catalog, prompt assembly, defaults; chat-with-any-agent.
6. Engine: graph scheduler, loop edges, gate, runs API + SSE (testable with defaults before the UI).
7. Frontend base: router, API client, auth.
8. Settings + Studio UI: providers → model sets → agent editor → pipeline graph → skills/rules.
9. Runs UI.
10. Later: eval briefs × pipeline versions, image tool results, resume, fanout, Gemini adapter.

## 11. Decisions (2026-11-01)

| # | Decision |
|---|---|
| Pipeline editor | node graph |
| Prompts | plain markdown textareas, auto-appended sections, no code editor |
| Models | models.dev catalog → enabled providers → model sets per agent/node |
| Renderer | Chromium on the same host |
| agentkit changes | approved |
| Default model | contributor mode, also in production |
| Demo critic | replaced by configurable Studio agents + generic chat endpoint |
| Studio access | admin role; bootstrap admin sergiydotsenko@gmail.com |
| Defaults / env transfer | Go seed once + JSON export/import |
| API keys | UI-entered, AES-GCM encrypted in DB with `SECRETS_KEY` (required in prod), models.dev `env` var fallback |

## 12. Execution log

- 2026-11-01: contract `agentkit/vfs/vfs.go` written; deps pre-added (agentkit: qjs, doublestar; backend:
  chromedp, cdproto, x/net, tdewolff/parse). Phase 1+2 delegated to 4 parallel subagents: VFS+fstools,
  component validator, renderer+blobs+ansible, JS sandbox.
- 2026-11-01: phases 7–9 (frontend) delegated to subagents in two waves: base (router, API layer typed
  from §4.7/§5, auth, shell, dev proxy) + shared components (SchemaForm, VersionBar/Diff, Modal,
  ListPage, useDirtyDraft), then 7 parallel section agents (settings providers, model sets, agent editor,
  pipeline graph editor, skills+rules, runs UI, chat). Verified: typecheck + build + full-route smoke
  with stubbed API (zero app errors). Backend contracts pending reconciliation where marked `ASSUMPTION`
  in `src/api/*` — see docs/frontend.md + docs/frontend.studio-ui.md.
- 2026-11-01: phase 2 done: `agentkit/sandbox/jsrun` + `run_js` tool (qjs vendored and patched: no host FS,
  ctx-aware sleep, bridge leak fixes). Tests pass with `-race`. Not wired into agents yet. See docs/backend.sandbox.md.
- 2026-11-02: `qjs.wasm` rebuilt from pinned sources (`internal/qjs/build/build.sh`, zig) with QuickJS' stack check
  patched back in and a 1 MiB stack: deep recursion now throws a catchable RangeError (~3300 plain frames) instead
  of crashing the engine. Speed and memory are the same as upstream's binary.
