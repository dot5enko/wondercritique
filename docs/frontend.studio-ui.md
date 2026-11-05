# Frontend — Studio, Runs, Design chat sections

State as of 2026-11-05 (vision mode, essentials-only toolbar, "Arrange to fill space"; design chat = full-screen canvas over the thread's shared file tree — folders = groups, server-stored layout, see `docs/backend.canvas.md`; runs served by the engine, `docs/backend.engine.md`). Feature-level reference for the pages built on the base app
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
- Output contract: Segmented `artifact|critique|custom|chat` (chat: hint line — plain-text reply,
  gen_components results); custom renders a fields-row editor
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
  (2026-11-02); agents run inside pipelines, and the design chat runs the `canvas-chat` agent
  directly (agent-mode runs, 2026-11-04).

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

- **Chat list** (`pages/chat/index.tsx`, updated 2026-11-05): header (app name, Settings icon for
  admins → studio, logout) + **style-guide pills** (below) + search `Input` (250 ms debounce, clear X / Esc) + one list card whose
  **first row is a full-width "New chat"** (→ `/chat/new`), then the chats (status dot, title,
  relative time, message count) → `/chat/<id>`. Data: `usePagedThreads(search)` (`threads.tsx`) —
  `GET /threads` chunks of `THREADS_PAGE` (20), a new search restarts from offset 0 (superseded
  requests aborted), appended chunks are de-duplicated by id; a centered rounded-full **"Load
  more" pill** under the card while `next_offset != null` (spinner while loading, "Couldn't load —
  retry" after a failed chunk). Search matches in titles are wrapped in `<mark>` `bg-yellow-200`
  (`Highlight`, case-insensitive regex). Empty search → `No chats match “…”`. The list page is not
  under a `ThreadsProvider` any more; it refetches on every mount. Chat rows show the chat's style
  guide name (palette icon) after the time when `styleguide_id` resolves in the user's list.
- **Style guides** (`pages/chat/styleguides.tsx`, `api/styleguides.ts`, 2026-11-05; backend
  `docs/backend.styleguide.md`): `useStyleguides()` lists `GET /styleguides` and re-polls every 3 s
  while any guide is `generating` (`upsert`/`remove` for optimistic updates). `StyleguidePills`
  (horizontal scroll row): dashed "+ Style guide" → `CreateStyleguideModal` (drop / choose ≤ 4
  images with thumbnails; the picker is a `<label>` around an `sr-only` `accept="image/*"` input —
  a `.click()` on a `hidden` input didn't open it on tablets; on `pointer-coarse` devices the empty
  state swaps the "Drop or choose…" text for a button-styled "Choose images" span inside the label; picked files are copied before the
  input reset, `isImageFile` also takes empty-`type` files by extension (mobile pickers), and
  `pages/chat/sampleImage.ts` `prepareSampleImage` (2026-11-05) re-encodes anything that isn't a
  ≤ 1.5 MB PNG/JPEG/WebP (or any GIF) — HEIC, big photos / screenshots — to a JPEG ≤ 2048 px long
  edge on the device (spinner tiles while preparing; Generate / close wait), keeping the request
  under the host nginx's 10 MB `client_max_body_size`; optional name + notes → multipart `createStyleguide` via
  `apiPost(FormData)` — `api/client.ts` `send` passes FormData through without a JSON
  Content-Type); one pill per guide: tap a ready one → `/chat/new?styleguide=<id>`; a generating one
  shows a spinner ("Generating…" until named), a failed one is red; the "…" (and taps on non-ready
  ones) opens `StyleguideModal` (sample thumbnails via `styleguideImageURL` object URLs, notes,
  generating / failed callouts, rendered markdown; Edit / "Write by hand" → name + markdown
  textarea → PATCH; Regenerate; Delete with `ConfirmDialog`; "Start a chat").
- **Canvas page** (`pages/chat/canvas/index.tsx`): one chat. Model: **folder = group**, **file =
  component** — the canvas is a view of `GET /threads/{id}/canvas` (`api/canvas.ts`), the same
  files the agents read under `/canvas`. Data: the canvas (files + layout) **and** the thread's
  runs (`listRuns filter[thread_id]`, oldest first; message numbers, briefs, pending cards; the
  chat title = `threadTitle` of the first run's brief — the canvas no longer uses `useThreads`). The
  canvas reloads when a run reaches a terminal state (a pending card reports it via `onStatus` and
  the listed status was non-terminal) and after every canvas operation.
  `canvas/tree.ts` `buildTree(files, pendingRuns)` builds the folders (implicit, from paths;
  `first`/`firstRun` = earliest file below; subfolders oldest first, virtual ones last);
  `topGroups` = the top-level folders (+ a "Top level" group for files directly under `/`). Top
  groups stack top→bottom in a world container (flex) until the user moves or arranges one — then
  every top group gets a canvas position (**free layout**: absolute + `translate`, new groups
  placed below everything in a layout effect). See "Canvas layout" below.
  - Floating chrome: top-left back + chat title — the title collapses to just the back arrow
    `TITLE_COLLAPSE_MS` (3.5 s) after the chat opens (max-width/opacity/margin transition, 300 ms;
    hover re-expands with fine pointers); the floating **selection toolbar** while something is
    selected; bottom `Composer`. Zoom − / % / + / fit buttons exist but are hidden
    (`SHOW_ZOOM_CONTROLS = false`; pinch / ctrl+wheel / keys cover it — kept for a desktop toggle). Empty new chat → centered explainer card with 3 example prompts.
  - Auto-fit: a ResizeObserver on the content + every group (rAF-coalesced) keeps the **latest
    group** fitted (insets top 72 / bottom 150) until the user pans/zooms/drags (`autoFit` ref);
    re-armed after each send and on chat switch. `fitRect` ignores changes < 2% zoom / 6px so
    frames measuring themselves don't restart the animation. Fit all = union of group rects.
    Keys: `+`/`-` zoom, `Shift+1` (by `e.code === 'Digit1'`) fit all, `Esc` clears selection.
  - Overscroll disabled on `<html>` while mounted; viewport meta has `viewport-fit=cover,
    interactive-widget=resizes-content`; chrome uses `env(safe-area-inset-*)`.
- **`useViewport`** (`canvas/useViewport.ts`): view `{x,y,k}` in a ref, applied imperatively as a
  2D `translate() scale()` on the world (no React render per frame); the dotted background is
  plain texture — constant 24px spacing and dot size at any zoom, only its position follows the
  pan (`x % 24`); `onView` callback per change. While moving, the world has `will-change: transform` (composite only);
  **settle** (140 ms after the last change, 260 ms after an animated one) drops it so iframes
  re-raster crisply, and only then updates `--inv-k` (1/k, rounded) — it restyles/relayouts the
  zoom-independent chrome, so never per frame. `onMotion(moving, view)`: `true` once when a
  motion starts, `false` at every settle (the page forwards it to `setCanvasMotion`, see
  ComponentFrame "Freezing"). One handler owns every pointer on the viewport
  (`touch-none`): first pointer down on a `[data-handle="move"|"resize"]` → `onHandle({kind,
  target (nearest data-sel), phase start|move|end|cancel, dx, dy})` (world units; extra fingers
  ignored meanwhile); otherwise 1-pointer drag = pan, 2 pointers = pinch zoom around the midpoint
  + pan, wheel = pan, ctrl/⌘+wheel (incl. trackpad pinch) = zoom at cursor, Safari `gesture*`
  events prevented. Tap → `onTap({target, additive})`; long press (touch/pen, 450 ms, cancelled
  by >8px movement or a 2nd finger, `vibrate(8)`) or `contextmenu` (right click / Android) →
  `onMenu`; the press that opened the menu never ends as a tap (`contextmenu` also marks it
  moved — Android can fire it before the timer, macOS ctrl+click is a left press; 2026-11-05).
  Targets = nearest `[data-sel]` (`c:<file path>` component, `g:<folder path>` group);
  `[data-canvas-ui]` subtrees (group header buttons) keep their own clicks. `rectOf` measures via
  offset chains + `data-dx`/`data-dy` (translate of moved elements; transform/animation-proof);
  `fitRect`, `zoomBy`, `setView` (optional 220 ms transition). Zoom 5%–400%.
- **`FolderGroup`** (`canvas/FolderGroup.tsx`, `memo`, recursive): `<section data-sel="g:<folder>">`
  with `data-group=<path>` (top-level) or `data-subgroup=<path>` (nested); transparent rounded
  block (`isolate`; top + free layout: `absolute top-0 left-0 w-max` + translate; nested: in its
  parent's flow, translated by its `g:` offset like a component; move-handle pill on its top edge).
  Its look is a separate `aria-hidden` background div (`-z-10`, `bg-ink/[0.035]`, dashed border for
  virtual folders, outline when selected — widths use `--inv-k`) that extends left / up around
  items moved past the block's edge. Header = folder name (`folderLabel`: last segment + `/`,
  root = "Top level"; full path as tooltip; "new folder" tag when virtual) — nothing else since
  2026-11-05 (the originating message + the agent's reply moved to the "Show message & reply"
  modal, see Context menu); `CHROME_ZOOM` (`zoom: min(4, max(1, --inv-k))`) keeps
  it readable zoomed out. Items container (wrap default `max-w-[5400px]`, `row`, `col` per
  `layout.dir[path]`; gaps x 64 / y 80): the folder's files (`ComponentFrame`, label =
  `componentLabel` (file name without `.html`, `tree.ts`) + score), then pending cards, then
  subfolders (nested `FolderGroup`s). A rAF + ResizeObserver
  layout effect pads the container right/bottom and moves the background left/top around moved
  items (no layout shift). Everything the groups need comes in one memoized `GroupEnv` (selection
  set, layout, `messageOf`, `onRetry`, `onStatus`).
- **`PendingCard`** (in FolderGroup.tsx): a run that is running / failed / cancelled / interrupted
  (`statuses[id] ?? listed status` ≠ completed) **or a completed reply-only message** (listed
  `reply`, no file of the run on the canvas) shows as a dashed card `[data-pending=<run>]`
  inside the group of `run.canvas_folder` (virtual folders are created in the tree when it doesn't
  exist yet): message number + brief, "about <file names>" (`run.context_paths`), the reply
  (`Reply`: markdown, `line-clamp-4`, tap toggles full text, `data-canvas-ui`), `RunProgress` while running, error line, Stop / Try again (failed, cancelled,
  interrupted) / Reconnect, and the latest iteration's html artifacts as drafts
  (`useRunLive` + `componentsOf`; gen_components results appear here live; frames without a
  selection key). Completed runs with files show nothing extra — their files are in the tree.
- **`ComponentFrame`** (`canvas/ComponentFrame.tsx`, `memo`): `id` = artifact id (loaded/cached,
  immutable), `selKey` = `data-sel` (`c:<path>`; none for drafts), label = file name + score —
  **absolutely positioned above the box** (`bottom-full`, 28px
  reserved by `mt-7`, grows upward when zoomed out) so zooming never changes the layout — then the
  box `[data-frame-box]` (= measured × `scale`) holding `[data-frame-doc]` (the document at its
  measured size, `data-w/h`, CSS `scale()` from the top-left) with the HTML in an iframe
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
  - **Freezing** (2026-11-04): a cross-origin iframe can't be snapshotted to a bitmap client-side,
    so instead live documents stop producing frames when they aren't worth watching — then the
    compositor only moves tiles it already has. Frozen = canvas moving (module-level
    `setCanvasMotion(moving, k)` from `useViewport` `onMotion`) **or** off screen (a second,
    permanent IntersectionObserver, `rootMargin 10%`) **or** zoom < `LIVE_MIN_ZOOM` (0.2). Each
    mounted frame registers a `sync` in `liveFrames` that posts `{__wcFreeze: 1|0}` only on change
    (re-sent on iframe `load` and on every size report). The injected script (same `<script>` as
    the measurer) on freeze pauses `document.getAnimations()` that are running (CSS animations /
    transitions / WAAPI) and playing `<video>`s, and holds `requestAnimationFrame` callbacks
    (wrapped global rAF / cAF, negative ids while frozen); thaw replays all of them. Measuring uses
    the native rAF so off-screen frames still size the layout. Not covered: `setInterval` /
    `setTimeout` driven updates.
  - Handles (`FrameHandles`, also used by groups): pill straddling the top edge (move) and grip in
    the bottom-right corner (resize = **uniform scale** of the whole component, components only), `CHROME_ZOOM`-sized, visible while the
    element itself is selected (a selected group doesn't show its components' handles) and on
    hover with fine pointers (`group-hover/frame|run`).
- **Selection**: ordered keys (`c:<file>`/`g:<folder>`). Tap = select only it (tap again =
  deselect), Shift/⌘/Ctrl or "Select more" mode (banner in the composer with Done) = toggle; tap on
  empty canvas clears (not in select mode). Long press on an unselected target selects it (adds in
  select mode); starting a handle drag selects too. Chips in the composer show file names /
  `folder/`. Sending → `startRun({agent: 'canvas-chat', input:{brief: text}, thread_id?,
  context_paths})` — the chat talks to the `canvas-chat` agent, not a pipeline
  (`docs/backend.chat-agent.md`): the selected
  paths (folders as folder paths — the server expands them; the root group `g:/` sends its direct
  files), client-side cap 12 files after expansion (error in the composer). Sending is allowed while
  other runs are going; the first message of `/chat/new` navigates (replace) to `/chat/<thread>`.
  Try again resends the brief with the run's `context_paths` that still exist (and its `vision`).
- **Vision mode** (2026-11-05): a toggle (`vision` state, Eye icon; menu + toolbar `visionAction`,
  disabled with no components or > `MAX_VISION_FILES` = 6, hint `max 6`) that arms the **next
  message** — composer banner "Vision mode — the agent sees a rendered preview of the selection"
  with an X, placeholder "Ask about how it looks…". `send(message, paths, look)` adds `vision: true`
  to `startRun` (quick actions sent while armed carry it too); off after sending and whenever the
  selection empties. The server renders the selection and gives the agent the screenshots +
  a `<meta>` note (`docs/backend.chat-agent.md` "Vision mode"); the brief shown on the canvas stays
  the user's text.
- **Context menu** (`canvas/ContextMenu.tsx`, fixed at the press point, clamped/flipped, touch rows
  `pointer-coarse:h-11`; `MenuAction.active` = a toggle → `menuitemcheckbox` + accent; scrollable —
  `max-h-[calc(100dvh-16px)]` + `overflow-y-auto`, so a long menu never runs off the window height): with a
  selection — Vision mode + quick requests that **just send a message** about the
  selection (Generate variants, Improve the design, Adapt for mobile, Dark version —
  `QUICK_ACTIONS`), Describe a change… (focuses the composer), Select more / Done selecting,
  Arrange to fill space / vertically / horizontally (only when arranging applies), Reset position & size, **Rename
  folder…** (one non-root group: prompt → `moveCanvasPath(folder, parent/name)`),
  **Move to folder…** (components only: prompt with a folder path → one move per file), Delete from
  canvas, **Show message & reply** (one group / component whose origin run is in the thread:
  component → `file.run_id`, group → `node.firstRun`; opens `MessageModal` — "Message N", the
  brief, the agent's reply as markdown), Open preview (`PreviewPanel`, 2026-11-05: fixed full-screen
  panel sliding in from the right, `translate-x-full` ↔ `0` transition, always mounted and keeps the
  last component while sliding out; big X top-left or Escape slides it back; iframe fills the rest)
  + Copy HTML (single component), **Download
  HTML / Download PNG @2x** (see Downloads), Clear selection. Chips, menu title and preview title
  use `componentLabel` (no `.html`). (No run-details drawer /
  Details buttons on the canvas since 2026-11-05 — agent + tools runs made them obsolete; the
  full `/runs/:id` page still exists.) Opened from the toolbar's More, the menu gets a last
  section "Toolbar: make horizontal / vertical" (`BarControls` passed by `onMore`). On empty canvas: Fit
  everything, Go to latest, New message, Reset layout (when anything is customised).
- **Selection toolbar** (`canvas/SelectionBar.tsx`, essentials only, updated 2026-11-05 — no count
  badge): floating icon
  bar while the selection is non-empty. Grip (drag anywhere; double-click = back to the default
  top-right place), Vision mode (pressed = `aria-pressed`, accent), Describe a
  change…, Open preview (single component) | Delete from canvas, Clear, orientation toggle
  ("Make horizontal" / "Make vertical"), More (opens the context
  menu beside it — everything else: quick actions, arrange, downloads, reset, rename / move; the
  orientation toggle is duplicated there as "Toolbar: make horizontal / vertical" via `BarControls` passed by `onMore`). Fixed
  thickness (46px, 54px on coarse pointers = border + p-1 + button), only the long axis scrolls
  (scrollbar hidden), position clamped into the window on render / resize; place + orientation in
  localStorage `wc-canvas-toolbar` (all chats). Same `MenuAction` objects as the menu.
- **Canvas operations** (`operate`: flush the pending layout save → call → reload the canvas;
  failures show as a toast): **Delete** → `deleteCanvasPaths(selection paths)`; the toast offers
  **Undo** (6 s) → `restoreCanvasOp(op_id)` (409 when a path is taken again → error toast). The
  server records a note for the next run; nothing is sent with the message (the old client
  `<meta>` mechanism is gone). Rename / Move as above (the server remaps layout keys).
- **Downloads** (2026-11-05): `exportSelection(format)` → `exportCanvas(thread, paths, 'html'|'png')`
  (`api/canvas.ts`, `POST /threads/{id}/canvas/export`, `docs/backend.canvas.md` "Export"; paths
  as for delete — the root group sends its direct files) → `saveFile` (`api/client.ts`: temporary
  `<a download>`, name from `Content-Disposition` via `apiPostFile`). One selected component →
  "Download HTML" / "Download PNG @2x" save that file (`tiers.html` / `tiers@2x.png`); groups or
  several items → the labels say "(zip)" and the server zips. PNG is disabled above
  `MAX_EXPORT_PNG` (24) components (hint `max 24`); both actions are disabled while one runs
  (toast "Rendering PNG…" / "Preparing download…", errors as a toast).
- **Composer** (`canvas/Composer.tsx`, restyled 2026-11-04): floating bottom-center card —
  rounded-3xl pill (`p-1.5`, `focus-within:border-accent/60`), chips row (small rounded-full
  chips), auto-growing textarea at 16px (no iOS focus zoom), Enter sends / Shift+Enter newline
  (IME-safe), circular primary Send `IconButton` (`size="lg"` = square 36px, `rounded-full!`,
  ArrowUp / Spinner while sending). Note: `cx` doesn't merge classes, so overriding the base
  `rounded-md` needs the `!` important suffix (Tailwind v4 orders same-property utilities itself).
  (2026-11-04). Optional `accessory` row above the chips = the chat's **style guide**
  (2026-11-05): a new chat shows `StyleguidePicker` (pill "No style guide" / guide name, opens a
  list **upwards** with None + guides, non-ready ones disabled; preselected from `?styleguide=`)
  when the user has guides; the first message sends `styleguide_id`. An existing chat shows
  `LockedStyleguide` (name + lock, from `runs[0].styleguide_id`); follow-ups never send the id
  (the server inherits it). No guide on an existing chat → no row.
- Render cost: `FolderGroup`/`PendingCard`/`ComponentFrame` are memoized and the page's callbacks
  are stable (`retry` via a ref), so typing in the composer doesn't re-render groups and selection /
  layout changes don't re-render frames.
- Auto-fit "latest": the top group holding the newest running message's card, else the newest file.
- Smoke (2026-11-05): `node tmp/canvas-smoke/smoke.mjs <dist> tmp/canvas-smoke/out` — build a
  same-origin bundle for it first (`VITE_API_BASE_URL= npx vite build --outDir
  ../../tmp/canvas-smoke/dist`; `apps/frontend/dist` may be a production build pointing at the real
  API). Stubs `GET /threads` for the list page; actions moved off the toolbar go through `viaMore`
  (More → menu item); checks toolbar essentials, vision mode (banner, `vision: true` posted with the
  selection, off after send) and pack (`dir` saved, ≥ 2 columns, no overlap, block wraps), and
  style guides (12 checks, 2026-11-05: R1 has guide `S1`; pills + chat-row name, new-chat picker,
  locked pill + no id on follow-ups, multipart upload from the create modal, generating pill →
  polled ready "Sunset", details modal sample image, pill → `/chat/new?styleguide=S2` → first
  message posts `styleguide_id`, picker → None posts none). Known
  stale checks after the label / header changes of 2026-11-05 (header origin text, `.html` chip
  labels, header taps that now land on a component). Runs —
  playwright-core (installed in `tmp/canvas-smoke`) drives `/usr/bin/google-chrome` headless as a
  touch tablet (820×1180), serving `dist` and a stubbed `/api/v1` purely through `context.route`
  (no dev server, no backend): runs (each POST → running run `R<n>` with `canvas_folder
  /new-r<n>`) and a mutable canvas (`GET/PUT /threads/R1/canvas…`, delete/restore/move/export) with
  `/pricing/{card-one,card-two}.html` (R1 is an agent-mode run with a reply), nested `/pricing/dark/card-one.html`, `/hero/wide-hero.html`
  and a running run targeting `/hero/taller`. Touch via CDP `Input.dispatchTouchEvent`; prompts
  answered through `page.on('dialog')`; fit via `Shift+Digit1`. 63 checks: groups = folders incl.
  nested, folder header origin, file labels, pending card in its virtual target folder, frame
  shrink, tap / long-press / select-more, pinch, pan, freezing (card fixtures carry a CSS
  animation + rAF counter `__ticks`: off-screen frozen, settled running, mid-pan frozen, resumes
  after settle), quick action posts `context_paths` + `agent: canvas-chat`, the agent reply in the group header, group →
  folder path, handles (move both ways, background grows, scale), debounced layout PUT with path
  keys, arrange (folder col, groups row), reset, Reset layout, free layout, toolbar, delete → server
  + undo → restore, nested folder delete, downloads (component → `card-two@2x.png`, group →
  `hero.zip` with the folder path), rename folder → move, move to folder → new group, no
  `<meta>`, title collapse, new chat, zero console errors.

### Canvas layout (2026-11-04)

`canvas/layout.ts` `useCanvasLayout(threadId)` — user layout on top of the flex layout, keyed by
canvas paths, stored on the server (`PUT /threads/{id}/canvas/layout`, debounced
`SAVE_DEBOUNCE_MS` = 500, flushed on thread switch / unmount / `pagehide` with `keepalive`).
`adopt(server)` takes the server copy (page load, after an operation — the server remaps keys on
move) unless local edits are unsaved; `flush()` saves now. A `/chat/new` layout carries over to the
created thread. Only the toolbar place stays in localStorage.
- `pos[key]` = CSS `translate`. `c:<file>`: offset from the component's flow place, any direction
  (zero offsets dropped). `g:<folder>`: a top group's absolute canvas position (kept even at 0,0);
  a nested group's offset in its parent. Any top `g:` entry ⇒ free layout; the first top-group
  move/arrange stores every top group's current place first (`groupPlaces`), so nothing jumps.
- `scale[file]` = uniform scale from the resize handle (0.25–4, the corner follows the axis dragged
  further; ≈1 dropped).
- `dir[folder]` = `row` | `col` | `pack` flow of a folder's items (default wrap). **`pack`** (2026-11-05,
  "Arrange to fill space"): `FolderGroup`'s layout effect places the items absolutely
  (`packItems`: `width: max-content`, top-left from `packRects`, label margin counted) and sizes
  the items box to the packed block (+ the padding around moved items); re-packed on every item
  resize (the first fit runs synchronously in the layout effect — no flash of the flex flow);
  `unpackItems` restores the flex flow when `dir` changes.
- `packRects(boxes, gap, aspect)` (`layout.ts`): skyline packing (tallest first, each box where its
  top is highest, leftmost on ties) tried at block widths of the 1…n widest boxes side by side +
  √(area·aspect); keeps the one with the smallest bounding box × (1 + |ln(shape / aspect)|).
  `PACK_ASPECT` = 1.6 (landscape, so a few items on a portrait tablet still use several columns),
  `PACK_GAP` = 64.
- Drags are imperative (page `onHandle`: `translate` + `data-dx/dy`, or box size + doc `scale()`;
  `data-dragging` → z-30) and committed to the layout on release (`cancel` restores).
- **Arrange** (`arrange(dir)`, dir = `pack` | `col` | `row`): one group → its `dir`, its items'
  offsets cleared; several top groups → lined up (`lineUp`: current visual order, from the
  top-left-most corner, gap 160; `pack` → offsets from `packRects` of their current rects); ≥ 2
  items (components / nested groups) of one folder → lined up / packed inside it (gap 64 row and
  pack / 80 col); all of a folder's items → its `dir`. Offered only when one of these applies.
- Reset position & size: components → offset + scale; groups → `dir` + their items (the group's
  own place stays). Empty-canvas "Reset layout" clears everything (back to the stack).

**Layout width (2026-11-02).** `pages/shell.tsx` wraps the Outlet of all settings/studio pages in a `max-w-[1100px]` centered column; only the pipeline graph editor (`/studio/pipelines?id=`) is full-bleed. Pages must not set their own wider max-width. `Modal` sizes: sm/md/lg/xl (xl = `max-w-5xl`).

## Candidate tools UI (updated 2026-11-02)

- Agent editor (`AgentForm.tsx`): "Candidate tools" card appears when generate_components /
  gen_components / pick_best / critique_component are enabled; edits `tool_config` (agent pickers
  from `catalog.agents`, max count; gen_components: generator default `component-generator`,
  max 8). Model-set strategy is single|fallback only.
- Run detail shows a "Reply" card (markdown) for agent-mode runs; `applyRunEvent` merges
  `run_completed.reply`.
- Canvas: every result of a completed run becomes its own file on the canvas (engine commit, see
  `docs/backend.engine.md` "Canvas").
