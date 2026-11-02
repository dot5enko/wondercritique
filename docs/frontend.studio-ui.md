# Frontend — Studio, Runs, Chat sections

State as of 2026-11-02. Feature-level reference for the pages built on the base app
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

Two-column editor: form (2fr) + Try-it (1fr). The form is already grouped into titled cards in
product order: Identity · Model set · Prompts (system + input template) · Tools · Skills & rules ·
Output contract · Limits.
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
- List page: intro copy ("agents are the workers a pipeline calls…"), a "Chat" quick action per
  published agent (→ `/agent-chat?agent=<slug>`), and a backend-unavailable Callout on
  404/501/503/network.

## Pipelines (`/studio/pipelines`, plan §4.5)

React Flow v12 graph editor. One generic RF node type `wc-pipeline` (RF reserves
`input`/`output`/`default`); the pipeline node type rides in `data.nodeType`. Display label
lives at reserved `config.label` (filtered out of generated SchemaForms).

- **Design loop template** (`flow.ts::designLoopTemplate`): new pipelines start from
  Input(brief) → Generator → Render → Vision critic + Code critic → Merge → Gate → Output with
  the Gate `continue` loop edge back to the Generator's feedback port; agents preselect by slug
  (`component-generator`, `vision-critic`, `code-critic`) when published defs exist. Port names
  come from the catalog; wires whose ports don't exist are skipped (Validate flags them). Also
  insertable from the empty-canvas state in the editor. Requires the catalog; falls back to a
  blank graph.
- **Onboarding**: "Add node" menu items carry one-line explanations (catalog description or
  built-in copy per node type); a dismissible (localStorage `wc_pipeline_help_v1`) help strip
  over the canvas explains wiring/loop/publish; the config panel shows a When/Gets/Sends box
  derived from the current wires (loop targets read "when the Gate says continue") and Gate nodes
  get an explainer Callout.
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
- Backend-unavailable (404/501/503/network) → "studio backend isn't available yet" Callout on
  the list page and the editor instead of a generic error.

## Skills & Rules (`/studio/skills`, `/studio/rules`, plan §4.6)

- Skills: file tree (add/rename/delete; `SKILL.md` protected from delete *and* rename) +
  full-size Textarea (mono for non-`.md`); tab inserts two spaces; mount hint `/skills/<slug>/`.
  Run-script has no endpoint yet (`TODO` in `SkillsEditor.tsx`).
- Rules: group Cards with name Input, rule rows (severity `info|warning|error` Select,
  auto-grow Textarea, validator Select of `catalog.validator_rule_codes` + "Custom…" sentinel;
  stored codes missing from the catalog render as custom). Empty custom text is omitted from
  the body.

## Runs (plan §5)

The **runs list page was removed** (2026-11-02): history lives in the design-chat rail, `/runs`
redirects to `/chat`. What remains under `src/pages/runs/`:

- `detail.tsx` — `RunDetailView({runId})` (the full run UI: header with status/usage/cancel,
  live read-only graph, iteration tabs + Best + Compare mode, per-iteration shots/critiques/
  score chart, node transcripts) + a thin `RunDetailPage` wrapper for `/runs/:id` (back link →
  `/chat`). Exports `applyRunEvent` (pure SSE-event → Run reducer) for the chat.
- `NewRunModal.tsx` — now also exports `parseInputFields` (Input-node `config.fields` parser
  used by the chat composer). Still reachable from the old entry points.
- Shared bits used by the chat: `ArtifactViews` (`useArtifactContent`, `HtmlArtifactCard`,
  `ArtifactCritiqueCard`, `ShotsGrid`), `RunGraph`, `ScoreChart`, `StepsAccordion`, `shared`
  (`RunStatusBadge`, `nodeLabel`, formatters, critique heuristics).
- `RunGraph` defensively defaults missing node `position` to `{x:0,y:0}` (a body without
  positions crashed React Flow's store init).

## Design chat (`/chat`, 2026-11-02)

Every chat = pipeline runs (`src/pages/chat/`): the user describes what they want designed →
a run starts (generator → render → critic → gate → regenerate while the score is low) → the
thread streams each iteration live.

- `index.tsx` — rail + timeline + composer. Threads are grouped client-side from
  `listRuns({limit:100})` (`thread_id ?? id`); `?thread=<id>` deep links, `?run=<id>` opens the
  details drawer. New chats pick a published pipeline (preselect: last choice in localStorage
  `wc_chat_pipeline` → slug `default` → first; admin-only studio API → 403 hides the picker and
  the run starts on the default published pipeline). The message maps to the Input node's first
  non-image field (fallback `brief`); other required fields render as an inline form. Follow-up
  messages send `thread_id` (+ new input) as a new run in the same thread. Composer: Enter send /
  Shift+Enter newline, Send↔Stop (`cancelRun` on the thread's last run), disabled while any run in
  the thread is non-terminal (statuses relay up from mounted RunCards). Empty state: explainer +
  3 clickable example prompts. 404/501/503/network → "runs backend isn't available yet" Callout.
- `rail.tsx` — thread summaries (`groupThreads`): title = first run's `input.brief` (fallback:
  date), latest-run status dot, run count, relative time.
- `RunCard.tsx` — one run as a chat turn: header (status, pipeline name/version, Stop, Details);
  per-iteration sections with a score badge, "Score N — critic requested changes" separator
  before iterations > 1, inline artifacts (HTML preview card, image shots grid, critique cards,
  ordered by the pipeline body's node order), StepsAccordion for transcripts; live via
  `streamRunEvents` + `applyRunEvent` + debounced `getRun` refresh (3× retry then "Live updates
  lost"); final banner shows the best artifact (iframe/image) with best iteration + score;
  failed/cancelled → Callout with Try-again (parent re-sends the previous message).
- Drawer: `RunDetailView` in a fixed right-side panel (Esc/backdrop/X close), `?run=` param.
- Smoke: `tmp/chat-smoke.mjs` (stubbed API, screenshots in `tmp/chat-smoke/`).

## Agent chat (`/agent-chat`, plan §6)

The former `/chat` page moved verbatim to `src/pages/agent-chat/`.
`?agent=<slug>` (default `critic`); admin picker over `listDefs('agent')` published defs,
falling back to a mono slug Input. Left session rail (new/delete/select) + thread + composer
(Enter send / Shift+Enter newline, Send↔Stop). Messages span the full container width
(`max-w-3xl` column): user messages stay a right-aligned accent-soft bubble that grows to
100% when long; assistant text is a borderless full-width block rendered as markdown via
shared `components/Markdown.tsx` (GFM tables/lists/strikethrough; user input and tool
output stay plain text). `AgentRunEvent` mapping: `text_delta` appends per-segment (tool
calls split segments so commentary→tool→answer order is preserved),
`reasoning_delta` → collapsed "Thinking…" row, tool_call events → collapsible rows (history
`tool_result` blocks folded in by `tool_call_id`), `run_failed` → Callout + Retry. Busy
session (409) → warning Callout with cancel action. Stop = `cancelAgentRun` + abort
(client disconnect also cancels server-side). Session titles are local-only (no update
endpoint). Switching session/agent mid-stream aborts and discards the local partial.
Reachable from the agents list ("Chat" action on published agents).
