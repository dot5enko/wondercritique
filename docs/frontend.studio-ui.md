# Frontend — Studio, Runs, Design chat sections

State as of 2026-11-04 (design chat rebuilt as a full-screen canvas with movable/resizable layout + selection toolbar; runs served by the engine, `docs/backend.engine.md`). Feature-level reference for the pages built on the base app
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
  render as `RunGroup`s stacked top→bottom in a world container (flex) until the user moves or
  arranges a group — then every group gets a canvas position (**free layout**: absolute + `translate`,
  new messages placed below everything in a layout effect). See "Canvas layout" below.
  - Floating chrome: top-left back + chat title, top-right zoom − / % (click = 100%; text written
    imperatively from `onView`, no React state) / + / fit all, under it the **selection toolbar**
    while something is selected; bottom `Composer`. Empty new chat → centered explainer card with 3 example prompts.
  - Auto-fit: a ResizeObserver on the content + every group (rAF-coalesced) keeps the **latest
    group** fitted (insets top 72 / bottom 150) until the user pans/zooms/drags (`autoFit` ref);
    re-armed after each send and on chat switch. `fitRect` ignores changes < 2% zoom / 6px so
    frames measuring themselves don't restart the animation. Fit all = union of group rects.
    Keys: `+`/`-` zoom, `Shift+1` fit all, `Esc` clears selection.
  - Overscroll disabled on `<html>` while mounted; viewport meta has `viewport-fit=cover,
    interactive-widget=resizes-content`; chrome uses `env(safe-area-inset-*)`.
- **`useViewport`** (`canvas/useViewport.ts`): view `{x,y,k}` in a ref, applied imperatively as a
  2D `translate() scale()` on the world (no React render per frame) + dotted background; `onView`
  callback per change. While moving, the world has `will-change: transform` (composite only);
  **settle** (140 ms after the last change, 260 ms after an animated one) drops it so iframes
  re-raster crisply, and only then updates `--inv-k` (1/k, rounded) — it restyles/relayouts the
  zoom-independent chrome, so never per frame. One handler owns every pointer on the viewport
  (`touch-none`): first pointer down on a `[data-handle="move"|"resize"]` → `onHandle({kind,
  target (nearest data-sel), phase start|move|end|cancel, dx, dy})` (world units; extra fingers
  ignored meanwhile); otherwise 1-pointer drag = pan, 2 pointers = pinch zoom around the midpoint
  + pan, wheel = pan, ctrl/⌘+wheel (incl. trackpad pinch) = zoom at cursor, Safari `gesture*`
  events prevented. Tap → `onTap({target, additive})`; long press (touch/pen, 450 ms, cancelled
  by >8px movement or a 2nd finger, `vibrate(8)`) or `contextmenu` (right click / Android) →
  `onMenu`. Targets = nearest `[data-sel]` (`c:<artifactId>` component, `g:<runId>` group);
  `[data-canvas-ui]` subtrees (group header buttons) keep their own clicks. `rectOf` measures via
  offset chains + `data-dx`/`data-dy` (translate of moved elements; transform/animation-proof);
  `fitRect`, `zoomBy`, `setView` (optional 220 ms transition). Zoom 5%–400%.
- **`RunGroup`** (`canvas/RunGroup.tsx`, `memo`): `<section data-sel="g:<run>" data-group>`
  transparent rounded block (`bg-ink/[0.035]`, outline when selected — outline widths use `--inv-k`
  so they stay ~3px on screen; `absolute w-max` + translate in the free layout; move-handle pill on
  its top edge). Header = index + message text + "about 1.2, 2.1" (labels of the run's
  `context_artifact_ids`) + `RunProgress` while running + error line + Stop / Try again /
  Reconnect / Details buttons; header and component labels use CSS `zoom: min(4, max(1, --inv-k))`
  (`CHROME_ZOOM`) so they stay readable when zoomed out. Live data via `useRunLive` (getRun + SSE
  + debounced refetch after step/iteration/terminal events).
  `componentsOf(run)`: `results` (position order) → else completed `best_artifact_id` → else the
  latest iteration's html artifacts as **drafts** (dimmed, "draft" tag). Reports components and
  status up (`onComponents`, `onStatus`). Components container: wrap (default, `max-w-[5400px]`),
  `row` (nowrap) or `col` per `layout.dir`; gaps x 64 / y 80. Moved components are translated; a
  rAF + ResizeObserver layout effect pads the container right/bottom so the block encloses them.
- **`ComponentFrame`** (`canvas/ComponentFrame.tsx`, `memo`): `[data-sel="c:<id>"]`, label `i.j`
  (message i, component j) + score — **absolutely positioned above the box** (`bottom-full`, 28px
  reserved by `mt-7`, grows upward when zoomed out) so zooming never changes the layout — then the
  box `[data-frame-box]` (size = manual size ?? measured) with the HTML in an iframe
  `sandbox="allow-scripts"` (opaque origin), `pointer-events-none`, inside a `contain: strict`
  clipper. HTML cached per artifact id for the session (`loadComponentHtml`). An injected script
  postMessages `{__wcFrame, w, h}` (parent checks `e.source`; measures at most once per rAF, posts
  only on change): rendered at 1280px first; if the visible non-full-width boxes span < 75% the
  frame shrinks once to that width (+ up to 48px margins each side, min 320); height = content
  bottom (not `scrollHeight`, which never drops below the frame), clamped 64–6000, followed via
  ResizeObserver + delayed re-measures.
  - **Lazy boot**: the iframe is created only once an IntersectionObserver (screen root,
    `rootMargin 50%`; sees the canvas transform) reports the frame near the screen, then through a
    module-level boot queue (`BOOT_CONCURRENCY = 2`; slot freed on the first size report or after
    1.5 s), so many heavy hero blocks don't parse/lay out at once. Mounted frames stay mounted.
  - Handles (`FrameHandles`, also used by groups): pill straddling the top edge (move) and grip in
    the bottom-right corner (resize; components only), `CHROME_ZOOM`-sized, visible while the
    element itself is selected (a selected group doesn't show its components' handles) and on
    hover with fine pointers (`group-hover/frame|run`).
- **Selection**: ordered keys (`c:`/`g:`). Tap = select only it (tap again = deselect), Shift/⌘/Ctrl
  or "Select more" mode (banner in the composer with Done) = toggle; tap on empty canvas clears
  (not in select mode). Long press on an unselected target selects it (adds in select mode);
  starting a handle drag selects too. Selection shows as removable chips in the composer ("1.2",
  "Group 3"); sending expands groups to their component ids → `startRun({input:{[field]: text},
  thread_id?, context_artifact_ids})` (max 12; see `docs/backend.engine.md` "Selected
  components"). `field` = default pipeline's first non-image input field (admin-only lookup) else
  `brief`. Sending is allowed while other runs are still going (each message is its own group);
  the first message of `/chat/new` navigates (replace) to `/chat/<thread>`.
- **Context menu** (`canvas/ContextMenu.tsx`, fixed at the press point, clamped/flipped, touch rows
  `pointer-coarse:h-11`): with a selection — quick requests that **just send a message** about the
  selection (Generate variants, Improve the design, Adapt for mobile, Dark version —
  `QUICK_ACTIONS`), Describe a change… (focuses the composer), Select more / Done selecting,
  Arrange vertically / horizontally / Reset position & size, Open preview + Copy HTML (single
  component), Run details (drawer with `RunDetailView`), Clear selection. On empty canvas: Fit
  everything, Go to latest, New message, Reset layout (when anything is customised).
- **Selection toolbar** (`canvas/SelectionBar.tsx`): fixed vertical icon bar under the zoom
  controls while the selection is non-empty (count badge; scrolls when taller than
  `100dvh-240px`): Arrange vertically / horizontally, the 4 quick actions, Open preview (single
  component), Run details (single), Reset position & size, Clear, More (opens the context menu
  next to it). Same `MenuAction` objects as the menu.
- **Composer** (`canvas/Composer.tsx`): floating bottom-center card, chips row, auto-growing textarea
  at 16px (no iOS focus zoom), Enter sends / Shift+Enter newline (IME-safe), Send button.
- Render cost: `RunGroup`/`ComponentFrame` are memoized and the page's callbacks to them are
  stable (`retry` via a ref), so typing in the composer or selection changes don't re-render frames.
- Smoke (2026-11-04): `tmp/canvas-smoke/smoke.mjs <dist> <out>` — playwright-core (installed in
  `tmp/canvas-smoke`) drives `/usr/bin/google-chrome` headless as a touch tablet (820×1180), serving
  `apps/frontend/dist` and a stubbed `/api/v1` purely through `context.route` (no dev server, no
  backend); touch gestures via CDP `Input.dispatchTouchEvent`. 31 checks: list, legacy redirect,
  frame shrink/height, context refs, tap select, long-press menu, select-more, pinch zoom, pan,
  quick action payload, group selection expansion, handles (hidden unselected, move/resize stored,
  group grows), toolbar, arrange (group vertical, groups horizontal), reset, group move → free
  layout, new chat, zero console errors. (The stub returns run id `R3` for every POST, so later
  sends overlap on screen — stub artifact.)

### Canvas layout (2026-11-04)

`canvas/layout.ts` `useCanvasLayout(threadId)` — user layout on top of the flex layout, per thread
in localStorage `wc-canvas-layout:<threadId>` (removed when empty; a `/chat/new` layout carries
over to the created thread):
- `pos[key]` = CSS `translate`. `c:<id>`: offset from the component's flow place (clamped so it
  stays right/below its group's origin; zero offsets dropped). `g:<run>`: absolute canvas position
  (kept even at 0,0). Any `g:` entry ⇒ free layout; the first group move/arrange stores every
  group's current place first (`groupPlaces`), so nothing jumps; groups then never shift when
  another changes height.
- `size[id]` = manual frame size from the resize handle (240–2560 × 64–6000, overrides measuring).
- `dir[runId]` = `row` | `col` flow of a group's components (default wrap).
- Drags are imperative (page `onHandle`: `translate` + `data-dx/dy`, or box width/height;
  `data-dragging` → z-30) and committed to the layout on release (`cancel` restores).
- **Arrange** (`arrange(dir)` in the page): one group → its `dir`, its component offsets cleared;
  several groups → lined up (`lineUp`: current visual order, from the top-left-most corner, gap
  160); ≥ 2 components of a group → lined up inside it (gap 64 row / 80 col); all of a group's
  components → its `dir`. Disabled unless one of these applies.
- Reset position & size: components → offset + size; groups → `dir` + their components (the
  group's canvas place stays). Empty-canvas "Reset layout" clears everything (back to the stack).

**Layout width (2026-11-02).** `pages/shell.tsx` wraps the Outlet of all settings/studio pages in a `max-w-[1100px]` centered column; only the pipeline graph editor (`/studio/pipelines?id=`) is full-bleed. Pages must not set their own wider max-width. `Modal` sizes: sm/md/lg/xl (xl = `max-w-5xl`).

## Candidate tools UI (updated 2026-11-02)

- Agent editor (`AgentForm.tsx`): "Candidate tools" card appears when generate_components /
  pick_best / critique_component are enabled; edits `tool_config` (agent pickers from
  `catalog.agents`, max count). Model-set strategy is single|fallback only.
- Canvas: every entry of `run.results` is its own component on the canvas (see Design canvas).
