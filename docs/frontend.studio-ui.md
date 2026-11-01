# Frontend — Studio, Runs, Chat sections

State as of 2026-11-01. Feature-level reference for the pages built on the base app
(see `docs/frontend.md` for stack/router/API-layer/auth). All data fetching goes through
`src/api/*`; shared UI from `src/components/*`; forms bound via `useDirtyDraft` with
debounced autosave to `PUT /studio/{kind}/{id}/draft` and `VersionBar` for publishing.

## Settings — LLM providers (`/settings`, plan §3.3)

Two-pane: provider list (enabled-first, search, adapter Badge responses/chat/anthropic/none —
`none` cannot be enabled, plan §3.2) + detail pane. Detail = Enable switch, API key
(write-only; `has_key` shows "Configured", env-var fallback hint from models.dev `env`),
base-URL vars (one Input per `${VAR}` in the `api` template), rpm limit (default 150),
Save (single PUT), "Test connection" (`POST …/test` — tests the *saved* credentials),
"Refresh catalog". Draft model: one `useDirtyDraft` per provider detail (remount via `key`).
No deprecated-provider toggle (models.dev flags models, not providers).

## Model sets (`/studio/model-sets`, plan §3.4)

List (name, `default` Badge on slug `default`, strategy, entry count) + editor: strategy
Segmented `single|fallback|fanout` with hints, capability filters ("Requires vision" /
"Requires tool use" — ephemeral UI state, not saved), ordered entries with per-entry model
Select (enabled catalog models, capability-filtered; excluded-but-selected entries stay with
a warning Badge), reasoning-effort Select from the model's `reasoning_options`, max-output
tokens. Identity (name/slug/description) is read-only — no update endpoint in the plan.
Editor re-syncs `useDirtyDraft` snapshot when the server draft converges (VersionBar saves
externally). Fanout shows an info Callout.

## Agents (`/studio/agents`, plan §4.3–4.4)

Two-column editor: form (2fr) + Try-it (1fr).
- Prompts are plain markdown Textareas (mono); the engine auto-appends `## Rules/## Skills/## Output`.
  Variable chips (`catalog.prompt_variables`, e.g. `{{brief}}`) insert at the caret of the
  last-focused textarea.
- Tools/skills/rules are checkbox groups from the catalog.
- Output contract: Segmented `artifact|critique|custom`; custom renders a fields-row editor
  (name/type/description/required/enum) that compiles to a JSON Schema `fields_schema`
  (`{type:'object',properties,required}`) and re-parses on restore.
- "Show assembled prompt" → `POST /studio/agents/{id}/preview` modal (system + input panes).
- Try-it: JSON sample input → `POST /studio/agents/{id}/try` (SSE) transcript — text deltas,
  reasoning collapsed, tool rows collapsible, auto-scroll with scroll-lock, Stop via
  AbortController. **Draft is flushed before try/preview** (autosave debounce would otherwise
  hit the backend late).

## Pipelines (`/studio/pipelines`, plan §4.5)

React Flow v12 graph editor. One generic RF node type `wc-pipeline` (RF reserves
`input`/`output`/`default`); the pipeline node type rides in `data.nodeType`. Display label
lives at reserved `config.label` (filtered out of generated SchemaForms).
- Wire rules: port-type compatibility (catalog `port_types`; optional `compatible_with`-style
  extras on a port type are honored, else equality), Input/Output endpoint exclusivity, one
  wire per non-`multiple` input, duplicate-wire rejection, **at most one loop edge** (Gate
  `continue` only, dashed+animated) and **DAG over non-loop edges** (client-side DFS check;
  full validation server-side via `POST /pipelines/{id}/validate`, issues rendered on nodes).
- Node config panel: SchemaForm from the node type's `config_schema`; Agent nodes add
  agent/version-pin/model-set-override (mapped into schema-declared property names when present);
  Input nodes get a form-fields editor (`{key,label,type}`) consumed by the run-start modal.
- Edge ids are deterministic `from:port->to:port`; positions persist via `onNodeDragStop`;
  untrusted bodies pass through `sanitizeBody` before conversion.
- Canvas themed via `--xy-*` CSS vars → design tokens (no raw hex).

## Skills & Rules (`/studio/skills`, `/studio/rules`, plan §4.6)

- Skills: file tree (add/rename/delete; `SKILL.md` protected from delete *and* rename) +
  full-size Textarea (mono for non-`.md`); tab inserts two spaces; mount hint `/skills/<slug>/`.
  Run-script has no endpoint yet (`TODO` in `SkillsEditor.tsx`).
- Rules: group Cards with name Input, rule rows (severity `info|warning|error` Select,
  auto-grow Textarea, validator Select of `catalog.validator_rule_codes` + "Custom…" sentinel;
  stored codes missing from the catalog render as custom). Empty custom text is omitted from
  the body.

## Runs (`/runs`, plan §5)

- List: status Badges (pulsing while running), pipeline+version, iterations, best score, cost.
- Start modal: published pipeline Select, test-run Switch, dynamic input form from the
  **pinned version's** Input-node `config.fields` (image fields submit `{artifact_id}` —
  no uploads yet); `POST /runs` → navigate to detail.
- Detail: header (status, times, usage/cost chips, Cancel), live read-only graph
  (`RunGraph.tsx`, self-contained RF instance), iteration tabs + "Best (1)" + Compare mode
  (two iterations side by side), per-iteration shots grid + lightbox, HTML artifact
  preview/source toggle (sandboxed iframe), JSON critique Cards, SVG score step-line
  (`ScoreChart.tsx`, no chart lib), node transcripts Accordion (status/model/duration/tokens/
  output/error/pinned versions/copyable session chip).
- Live state: SSE events are merged into the `Run` object via a pure `applyRunEvent` reducer
  (`useAsync.setData`); `iteration_completed`/terminal events trigger a debounced full
  `getRun` refresh; stream drops retry 3× then show a "Live updates lost" Callout.
  Artifacts discovered live (event-carried ids) are classified by MIME sniffing
  (`fetchArtifact`) until the next refresh.
- Cross-run compare is intentionally not implemented.

## Chat (`/chat`, plan §6)

`?agent=<slug>` (default `critic`); admin picker over `listDefs('agent')` published defs,
falling back to a mono slug Input. Left session rail (new/delete/select) + thread + composer
(Enter send / Shift+Enter newline, Send↔Stop). `AgentRunEvent` mapping: `text_delta` appends
per-segment (tool calls split segments so commentary→tool→answer order is preserved),
`reasoning_delta` → collapsed "Thinking…" row, tool_call events → collapsible rows (history
`tool_result` blocks folded in by `tool_call_id`), `run_failed` → Callout + Retry. Busy
session (409) → warning Callout with cancel action. Stop = `cancelAgentRun` + abort
(client disconnect also cancels server-side). Session titles are local-only (no update
endpoint). Switching session/agent mid-stream aborts and discards the local partial.

## Smoke tooling

`tmp/frontend-smoke.mjs` (all routes, stubbed API, screenshots), `tmp/pipelines-smoke*.cjs`
(pipeline editor interactions). Playwright chromium binary path is hardcoded in those scripts.
