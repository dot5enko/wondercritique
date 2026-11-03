# Frontend — Studio, Runs, Design chat sections

State as of 2026-11-04 (design chat rebuilt as a full-screen canvas; runs served by the engine, `docs/backend.engine.md`). Feature-level reference for the pages built on the base app
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

Single-column editor (generic "Agent" page header with a "Try it" button that opens a large modal, `TryItPanel.tsx`; run state survives closing the modal). The form is already grouped into titled cards in
product order: Identity · Model set · Prompts (system + input template) · Tools · Skills & rules ·
Output contract · Limits.
Cards use the borderless compact variant (`plain` prop on `Card`/`CardHeader`/`CardBody`/`IdentityCard`:
no borders/dividers, `px-3` padding, `space-y-2` between cards) — updated 2026-11-02.
- Prompts are plain markdown Textareas (mono); the engine auto-appends `## Rules/## Skills/## Output`.
  Variable chips (`catalog.prompt_variables`, e.g. `{{brief}}`) insert at the caret of the
  last-focused textarea.
- Tools/skills/rules are checkbox groups from the catalog.
- Output contract: Segmented `artifact|critique|custom`; custom renders a fields-row editor
  (name/type/description/required/enum) that compiles to a JSON Schema `fields_schema`
  (`{type:'object',properties,required}`) and re-parses on restore.
- "Show assembled prompt" → `POST /studio/agents/{id}/preview` modal (system + input panes).
- Try-it (modal): JSON sample input → `POST /studio/agents/{id}/try` (SSE) transcript — text deltas,
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

The **runs list page was removed** (2026-11-02): history lives in the chat list, `/runs`
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

## Design canvas (`/chat`, `/chat/:threadId`, 2026-11-04)

Replaces the timeline/floating-chat page (the old `RunCard.tsx` / `DesignStage.tsx` in
`pages/chat/` are no longer routed — dead code kept for now; `RunProgress.tsx` and `rail.tsx`
(`groupThreads`) are still used). Full screen, no Shell/sidebar, touch-first (tablet/phone).

- **Chat list** (`pages/chat/index.tsx`): header (app name, Settings icon for admins → studio,
  logout, primary "New chat" → `/chat/new`) + one list of threads from `useThreads()` (status dot,
  title = first brief, relative time, message count) → `/chat/<id>`. Nothing else.
- **Canvas page** (`pages/chat/canvas/index.tsx`): one chat. Model: **message = run = group**,
  **result artifact = component**. Runs of the thread (`listRuns filter[thread_id]`, oldest first)
  render as `RunGroup`s stacked top→bottom in a world container; DOM flex layout does the placement
  (no stored positions, no dragging of elements).
  - Floating chrome: top-left back + chat title, top-right zoom − / % (click = 100%) / + / fit all;
    bottom `Composer`. Empty new chat → centered explainer card with 3 example prompts.
  - Auto-fit: a ResizeObserver on the content keeps the **latest group** fitted (insets top 72 /
    bottom 150) until the user pans/zooms (`autoFit` ref); re-armed after each send and on chat
    switch. Keys: `+`/`-` zoom, `Shift+1` fit all, `Esc` clears selection.
  - Overscroll disabled on `<html>` while mounted; viewport meta has `viewport-fit=cover,
    interactive-widget=resizes-content`; chrome uses `env(safe-area-inset-*)`.
- **`useViewport`** (`canvas/useViewport.ts`): view `{x,y,k}` in a ref, applied imperatively as a
  2D `translate() scale()` on the world (no React render per frame; 2D so iframes re-raster
  crisply); also sets `--inv-k` (1/k) on the world and moves the dotted background. One handler
  owns every pointer on the viewport (`touch-none`): 1-pointer drag = pan (no element dragging),
  2 pointers = pinch zoom around the midpoint + pan, wheel = pan, ctrl/⌘+wheel (incl. trackpad
  pinch) = zoom at cursor, Safari `gesture*` events prevented. Tap → `onTap({target, additive})`;
  long press (touch/pen, 450 ms, cancelled by >8px movement or a 2nd finger, `vibrate(8)`) or
  `contextmenu` (right click / Android) → `onMenu`. Targets = nearest `[data-sel]` (`c:<artifactId>`
  component, `g:<runId>` group); `[data-canvas-ui]` subtrees (group header buttons) keep their own
  clicks. `rectOf` measures via offset chains (transform/animation-proof); `fitRect`, `zoomBy`,
  `setView` (optional 220 ms transition). Zoom 5%–400%.
- **`RunGroup`** (`canvas/RunGroup.tsx`): `<section data-sel="g:<run>" data-group>` transparent
  rounded block (`bg-ink/[0.035]`, outline when selected — outline widths use `--inv-k` so they
  stay ~3px on screen). Header = index + message text + "about 1.2, 2.1" (labels of the run's
  `context_artifact_ids`) + `RunProgress` while running + error line + Stop / Try again /
  Reconnect / Details buttons; header and component labels use CSS `zoom: min(4, max(1, --inv-k))`
  (`CHROME_ZOOM`) so they stay readable when zoomed out. Live data via `useRunLive` (getRun + SSE
  + debounced refetch after step/iteration/terminal events — moved out of the old RunCard).
  `componentsOf(run)`: `results` (position order) → else completed `best_artifact_id` → else the
  latest iteration's html artifacts as **drafts** (dimmed, "draft" tag). Reports components and
  status up (`onComponents`, `onStatus`).
- **`ComponentFrame`** (`canvas/ComponentFrame.tsx`): `[data-sel="c:<id>"]`, label `i.j` (message i,
  component j) + score, then the HTML in an iframe `sandbox="allow-scripts"` (opaque origin) with
  `pointer-events-none`. HTML cached per artifact id for the session (`loadComponentHtml`). An
  injected script postMessages `{__wcFrame, w, h}` (parent checks `e.source`): rendered at 1280px
  first; if the visible non-full-width boxes span < 75% the frame shrinks once to that width
  (+ up to 48px margins each side, min 320); height = content bottom (not `scrollHeight`, which
  never drops below the frame), clamped 64–6000, followed via ResizeObserver + delayed re-measures.
- **Selection**: ordered keys (`c:`/`g:`). Tap = select only it (tap again = deselect), Shift/⌘/Ctrl
  or "Select more" mode (banner in the composer with Done) = toggle; tap on empty canvas clears
  (not in select mode). Long press on an unselected target selects it (adds in select mode).
  Selection shows as removable chips in the composer ("1.2", "Group 3"); sending expands groups to
  their component ids → `startRun({input:{[field]: text}, thread_id?, context_artifact_ids})`
  (max 12; see `docs/backend.engine.md` "Selected components"). `field` = default pipeline's first
  non-image input field (admin-only lookup) else `brief`. Sending is allowed while other runs are
  still going (each message is its own group); the first message of `/chat/new` navigates
  (replace) to `/chat/<thread>`.
- **Context menu** (`canvas/ContextMenu.tsx`, fixed at the press point, clamped/flipped, touch rows
  `pointer-coarse:h-11`): with a selection — quick requests that **just send a message** about the
  selection (Generate variants, Improve the design, Adapt for mobile, Dark version — `QUICK_ACTIONS`),
  Describe a change… (focuses the composer), Select more / Done selecting, Open preview + Copy HTML
  (single component), Run details (drawer with `RunDetailView`), Clear selection. On empty canvas:
  Fit everything, Go to latest, New message.
- **Composer** (`canvas/Composer.tsx`): floating bottom-center card, chips row, auto-growing textarea
  at 16px (no iOS focus zoom), Enter sends / Shift+Enter newline (IME-safe), Send button.
- Smoke (2026-11-04): `tmp/canvas-smoke/smoke.mjs <dist> <out>` — playwright-core (installed in
  `tmp/canvas-smoke`) drives `/usr/bin/google-chrome` headless as a touch tablet (820×1180), serving
  `apps/frontend/dist` and a stubbed `/api/v1` purely through `context.route` (no dev server, no
  backend); touch gestures via CDP `Input.dispatchTouchEvent`. Covers list, legacy redirect, frame
  shrink/height, context refs, tap select, long-press menu, select-more, pinch zoom, pan, quick
  action payload, group selection expansion, new chat, zero console errors.

**Layout width (2026-11-02).** `pages/shell.tsx` wraps the Outlet of all settings/studio pages in a `max-w-[1100px]` centered column; only the pipeline graph editor (`/studio/pipelines?id=`) is full-bleed. Pages must not set their own wider max-width. `Modal` sizes: sm/md/lg/xl (xl = `max-w-5xl`).

## Candidate tools UI (updated 2026-11-02)

- Agent editor (`AgentForm.tsx`): "Candidate tools" card appears when generate_components /
  pick_best / critique_component are enabled; edits `tool_config` (agent pickers from
  `catalog.agents`, max count). Model-set strategy is single|fallback only.
- Canvas: every entry of `run.results` is its own component on the canvas (see Design canvas).
