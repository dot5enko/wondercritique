# Frontend — Studio, Runs, Design chat sections

State as of 2026-11-02 (runs/chat now served by the real engine, `docs/backend.engine.md`). Feature-level reference for the pages built on the base app
(see `docs/frontend.md` for stack/router/API-layer/auth). All data fetching goes through
`src/api/*`; shared UI from `src/components/*`; forms bound via `useDirtyDraft` with
debounced autosave to `PUT /studio/{kind}/{id}/draft` and `VersionBar` for publishing.

**Editor loading (2026-11-02).** Publish clears the server draft, so every studio editor (pipelines,
agents, model sets, skills, rules) loads via `api/studio.ts::getEditableDef`: when `draft == null`
and a version exists, the latest published body becomes the starting draft. A clean editor never
writes a draft (autosave fires only when dirty; pipeline Validate saves only when dirty — the
backend validates draft-else-published). Before this, editors opened published defs as empty
templates and any interaction saved that junk as the draft.

## Providers — LLM providers (`/settings/providers`, plan §3.3)

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
tokens. Identity: name/description editable through the shared `IdentityCard` (explicit "Save identity" button → `PATCH /studio/{kind}/{id}`, independent of draft/publish); slug read-only.
Editor re-syncs `useDirtyDraft` snapshot when the server draft converges (VersionBar saves
externally). Fanout shows an info Callout. Create modal (slug/name/description) posts an empty
draft `{strategy: single, entries: []}` — the backend accepts empty drafts (2026-11-02) — then opens
the editor; while `entries` is empty the editor passes `publishBlocked` to `VersionBar` (generic
optional prop: disables Publish and shows the reason inline), mirroring the backend's publish rule.

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
- List page **Used in** column (`getAgentUsage`: first pipeline name + `+N` badge, tooltip lists all;
  "Unused" otherwise) and a trash IconButton per row (disabled with tooltip while used or usage still
  loading). Delete opens the danger `ConfirmDialog`; a 409 from the backend (race) is shown inside the
  dialog and usage reloads (2026-11-02).
- List page: intro copy ("agents are the workers a pipeline calls…") and a backend-unavailable
  Callout on 404/501/503/network. No per-agent chat action — standalone agent chat was removed
  (2026-11-02); agents run only inside pipelines.

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
  The list's "Nodes" column reads `draft?.nodes` → shows 0 for published defs without a draft.
- Node config panel: SchemaForm from the node type's `config_schema`; Agent nodes add
  agent/version-pin/model-set-override (mapped into schema-declared property names when present);
  Input nodes get a form-fields editor (`{key,label,type}`) consumed by the run-start modal.
- Edge ids are deterministic `from:port->to:port`; positions persist via `onNodeDragStop`;
  untrusted bodies pass through `sanitizeBody` before conversion.
- Canvas themed via `--xy-*` CSS vars → design tokens (no raw hex).
- Backend-unreachable (404/501/503/network) → "studio backend can't be reached" Callout on
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
- `usePipelineBody(run)` (`runs/usePipelineBody.ts`) supplies graph/labels/node types: it prefers the
  run's own `pipeline_body` snapshot (available to non-admins, exact for draft test runs) and only
  falls back to the studio API (`getVersion`/`getDef`) for runs without one.
- Scores are 0–10 (`GOOD_SCORE = 8` in `shared.tsx` = the engine gate's default target; scores at or
  above it get the success tone).

## Design chat (`/chat`, 2026-11-02)

Every chat = pipeline runs (`src/pages/chat/`): the user describes what they want designed →
a run starts (generator → render → critic → gate → regenerate while the score is low) → the
thread streams each iteration live.

- `index.tsx` — timeline + composer only (no own rail column; the thread list lives in the Shell
  sidebar). Header row only for an open thread (title + run count). Threads come from
  `useThreads()` (`threads.tsx`, grouped client-side from `listRuns({limit:100})` by
  `thread_id ?? id`); `?thread=<id>` deep links, `?run=<id>` opens the details drawer.
  Switching thread clears a stale send error. New chats pick a published pipeline (preselect: last choice in localStorage
  `wc_chat_pipeline` → slug `default` → first; admin-only studio API → 403 hides the picker and
  the run starts on the default published pipeline). The message maps to the Input node's first
  non-image field (fallback `brief`); other required fields render as an inline form. Follow-up
  messages send `thread_id` (+ new input) as a new run in the same thread. Composer: Enter send /
  Shift+Enter newline, Send↔Stop (`cancelRun` on the thread's last run), disabled while any run in
  the thread is non-terminal (statuses relay up from mounted RunCards). Empty state: explainer +
  3 clickable example prompts. 404/501/503/network → "runs backend can't be reached" Callout.
- `rail.tsx` — `groupThreads` + `ThreadList` (rendered by the Shell sidebar): title = first run's
  `input.brief` (fallback: date), latest-run status dot, run count, relative time.
- `threads.tsx` — `ThreadsProvider`/`useThreads()` shared thread query (Shell ↔ chat page).
- `RunCard.tsx` — one run as a chat turn: header (status, pipeline name/version, Stop, Details);
  per-iteration sections with a score badge, "Score N — critic requested changes" separator
  before iterations > 1, inline artifacts (HTML preview card, image shots grid, critique cards,
  ordered by the pipeline body's node order), StepsAccordion for transcripts; live via
  `streamRunEvents` + `applyRunEvent` + debounced `getRun` refresh (3× retry then "Live updates
  lost"); final banner shows the best artifact (iframe/image) with best iteration + score;
  failed/cancelled → Callout with Try-again (parent re-sends the previous message).
- Drawer: `RunDetailView` in a fixed right-side panel (Esc/backdrop/X close), `?run=` param.
- Smoke: `tmp/chat-smoke.mjs` (stubbed API, screenshots in `tmp/chat-smoke/`; also covers the
  chats↔settings sidebar switch. Its only console errors are 404s for studio/settings endpoints
  the stub doesn't serve).
