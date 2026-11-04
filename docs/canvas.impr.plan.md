# Plan — canvas = shared file tree of the thread

**Status: plan, not implemented (written 2026-11-04).** The executing agent should delete this file when
done and move its lasting content into the research docs (see "Docs to update").

Read first (research database, per `AGENTS.md`): `docs/frontend.studio-ui.md` (section "Design canvas"
and "Canvas layout" — the current client), `docs/backend.engine.md` (runs, selection context,
results), `docs/backend.studio.md` ("Tools", session mounts), `docs/backend.vfs.md` (`vfs.FS`
contract, `fstools`, `vfs.Mount`). Rules that apply: use `./tmp` for scratch files, never start the
app/binaries, never deploy or touch ansible, test only through `go test` / `npm run build` / the
headless smoke script, update the docs when done.

## 1. Goal and decisions (agreed with the user)

The **shared state between the human and the chat is the set of component files of a thread**. The
canvas is only a view of it (plus a view-only layout); the agents read the same files through their
tools.

| # | Decision |
|---|---|
| D1 | **Group = folder**, per thread. Folders nest (subfolders = nested groups). A component = one `.html` file. A group is no longer "one message". |
| D2 | **Files are never modified in place.** A change to a component is a **new file in a new subfolder** next to it (Stitch-style "copy the file, change the copy"), e.g. `/pricing/tiers.html` → "make it dark" → `/pricing/dark/tiers.html`. Variants are new files too. |
| D3 | **Layout (positions, scale, row/col flow) is stored on the server, permanently**, per thread. The chat/agents never see positions. |
| D4 | **Old threads are ignored.** No data migration. Do **not** delete anything from the database yourself (the user cleans the dev DB on their own); threads without files simply show an empty canvas. |
| D5 | The agent must not be surprised by vanished files: user operations (delete, move/rename) are recorded as **notes** on the server and handed to the **next** run of the thread, once. This replaces the client-side `<meta>` hack. |
| D6 | The agent sees the **folder structure** (paths) — grouping matters to it; positions do not. |

### Why a metadata table over artifacts (not a `vfs/sqlite` workspace)

Content stays in immutable **artifacts** (blob store, already produced by every run); the tree is a
table of `(thread, path) → artifact`. This gives for free: immutability (D2), soft delete + undo,
provenance (which run / message made a file, its critique score, "Run details"), previews through
the existing `GET /artifacts/{id}/content`, and no write contention between running agents and the
user. Agents get the tree as a **read-only `vfs.FS` adapter**, so `read_file` / `glob` / `grep`
work unchanged. All writes go through the engine (results of a run) or the canvas API (user
operations).

## 2. Data model (`internal/models/canvas.go`, registered with `dbx.AsModel` in the canvas module)

```go
// CanvasFile is one component file of a thread's canvas.
type CanvasFile struct {
    Base                                   // ID, CreatedAt, UpdatedAt
    ThreadID   string   `gorm:"index:idx_canvas_thread_path;size:64"`
    Path       string   `gorm:"index:idx_canvas_thread_path;size:512"` // "/pricing/dark/tiers.html"
    ArtifactID string   `gorm:"size:64"`   // html artifact holding the content (immutable)
    RunID      string   `gorm:"size:64"`   // run that produced it
    SourcePath string   `gorm:"size:512"`  // the file it is a new version of ("" = new design)
    Score      *float64
    Size       int64
    DeletedAt  *time.Time `gorm:"index"`   // soft delete (undo); NOT gorm.DeletedAt — queries filter explicitly
    DeleteOp   string     `gorm:"size:64"` // id of the delete operation (restore groups)
}

// CanvasState is the per-thread view state + pending notes for the agent.
type CanvasState struct {
    ThreadID  string `gorm:"primaryKey;size:64"`
    OwnerID   string `gorm:"index;size:64"`
    Layout    CanvasLayout `gorm:"serializer:json"`
    Notes     []CanvasNote `gorm:"serializer:json"` // not yet delivered to a run
    UpdatedAt time.Time
}

type CanvasLayout struct {
    Pos   map[string]Point   `json:"pos"`   // "g:<folder>" absolute (free layout) | "c:<file>" offset
    Scale map[string]float64 `json:"scale"` // file path → uniform scale
    Dir   map[string]string  `json:"dir"`   // folder path → "row" | "col"
}
type Point struct{ X, Y float64 }
type CanvasNote struct {
    ID   string    `json:"id"`   // = the operation id (delete op / move op)
    Text string    `json:"text"` // "deleted /hero/ (2 components)", "moved /a.html → /cta/a.html"
    At   time.Time `json:"at"`
}
```

Uniqueness: at most one **live** (`deleted_at IS NULL`) row per `(thread_id, path)` — enforce in the
store inside the write transaction (SQLite partial unique index is fine too:
`CREATE UNIQUE INDEX … ON canvas_files(thread_id, path) WHERE deleted_at IS NULL` via a dbx hook if
the repo has one; otherwise check-in-tx).

`models.Run` changes:
- replace `ContextArtifactIDs []string` with `ContextPaths []string \`gorm:"serializer:json"\`` (the
  files the message was about; display "about tiers.html");
- add `CanvasFolder string` (default destination computed at start, final folder after commit);
- add `CanvasNotes []string \`gorm:"serializer:json"\`` (notes delivered to this run — audit/UI).

Drop `context_artifact_ids` everywhere (API, engine, frontend). Old rows keep the column; nothing
reads it.

### Path rules (`canvas.CleanPath`, built on `vfs.Clean`)

- Absolute, no trailing slash for files; files must end in `.html`; folders are implicit (a folder
  exists while a live file is under it).
- Segments: lowercase `[a-z0-9-]`, ≤ 48 chars (`Slug` helper: lowercase, ASCII-fold, non-alnum → `-`,
  collapse, trim, fallback `design`); file stem the same + `.html`.
- Max depth: 4 folder levels; segments starting with `.` are reserved.
- `UniquePath(thread, p)`: if taken, append `-2`, `-3`, … to the stem (files) / last segment (folders).

## 3. Backend — new package `internal/features/canvas`

Must not import `engine` or `agentrun` (both import it). Deps: `*gorm.DB`, `*blobs.Store`, logger.

### 3.1 Store (`store.go`)

- `Files(ctx, thread) ([]models.CanvasFile, error)` — live files, sorted by path.
- `Commit(ctx, tx, thread, runID string, items []CommitItem) ([]models.CanvasFile, error)` —
  `CommitItem{Folder, Name, ArtifactID, SourcePath, Score, Size}`; cleans/slugs, `UniquePath`,
  inserts. Used by the engine inside its finish transaction.
- `Delete(ctx, thread, paths []string) (opID string, deleted []string, err)` — each path = a file or
  a folder (prefix `folder + "/"`); soft-delete, same `DeleteOp`; appends one note
  ("deleted /hero/ (2 components: /hero/a.html, /hero/b.html)" — list up to 5 files).
- `Restore(ctx, thread, opID)` — un-delete the op's rows; 409 (`apperr.Conflict`) if a path is live
  again; removes the op's note if still pending, else appends "restored …".
- `Move(ctx, thread, from, to string)` — file→file or folder→folder (rename = move); validates, 409 on
  conflict, rewrites `path` (prefix replace for folders) and `SourcePath`s pointing into it, remaps
  layout keys (`c:` / `g:` / scale / dir) in the same transaction, appends a note.
- `State(ctx, thread)` / `PutLayout(ctx, thread, owner, layout)` — validates keys (must be paths of
  this thread, live or not — stale keys are dropped), caps JSON size (256 KB) and map sizes (2000).
- `TakeNotes(ctx, tx, thread) ([]CanvasNote, error)` — returns and clears pending notes (run start).
- `ThreadOwner(ctx, thread)` helper or reuse the engine's thread check (owner = owner of the
  thread's runs; admins see all) — put a small `Access(ctx, thread, viewer)` here, querying `runs`.

### 3.2 Read-only FS for agents (`fs.go`)

`canvas.FS(store, thread) vfs.FS`:
- `Stat`, `Read` (artifact blob), `List` (direct children with implicit dirs / recursive files), derived
  from `Files()` on every call (no caching; cheap at ≤ hundreds of rows).
- `Write` / `Delete` → `vfs.ErrReadOnly`. Use `vfs.Clean` semantics; implement the conformance
  expectations of `vfstest.RunReadOnly` where they apply (seed rows directly in the test).
- File sizes/digests from `CanvasFile.Size` / artifact `BlobSHA`; `Version` = 1; `ModTime` = CreatedAt.

### 3.3 HTTP (`controller.go`, `httpx.AsController`, all authenticated, owner-scoped like runs)

| Route | Body → Response |
|---|---|
| `GET /threads/{id}/canvas` | → `{thread_id, files: [{path, artifact_id, run_id, source_path, score, size, created_at}], layout: {pos, scale, dir}, pending_notes: [{id, text, at}]}` (empty lists for a thread without files; 404 when the caller can't see the thread) |
| `PUT /threads/{id}/canvas/layout` | `{layout}` → `{layout}`; last write wins |
| `POST /threads/{id}/canvas/delete` | `{paths: [...]}` → `{op_id, deleted: [...]}` |
| `POST /threads/{id}/canvas/restore` | `{op_id}` → `{restored: [...]}` |
| `POST /threads/{id}/canvas/move` | `{from, to}` → `{moved: [{from, to}]}` |

Content keeps using `GET /artifacts/{id}/content` (already sandboxed with CSP).
Module `canvas.Module` (models, `fx.Provide(NewStore)`, controller) added to `internal/app/app.go`
under feature modules, before `engine`.

## 4. Backend — engine integration (`internal/features/engine`)

### 4.1 Start (`start.go`)

- `StartParams.ContextIDs` → `ContextPaths []string` (POST /runs `context_paths`, ≤ 12, each a live
  file **or folder** of `thread_id`; folders expand to their files recursively; dedupe; > 12 files
  after expansion → 400 field error `context_paths`). Requires `thread_id` (400 otherwise).
- Default destination `run.CanvasFolder` (§4.4 rules), computed here from the brief + selection.
- In the run-creation transaction: `notes := canvas.TakeNotes(tx, thread)`; store their texts on
  `run.CanvasNotes`.
- Replace `threadContext` + `selectionContext` with **`canvasContext`** (new file `context.go`) →
  the iteration-1 loop seed (`portVal.Text`), same delivery as today (reaches agents whose template
  uses the loop port, e.g. `{{feedback}}`; also expose the same text as prompt variable `canvas` in
  `nodeRun.samples()` so templates can place it explicitly — check `nr.samples()` in `nodes.go`):

```
## Canvas
This chat's design canvas is a tree of HTML component files (folders = groups). You can read them
under /canvas with your file tools when you have them; you cannot change them.

### Files
/pricing/
  tiers.html  — score 8.6, message 1
  dark/
    tiers.html — message 2
/buttons/ … (≤ 200 lines, then "… and N more files")

### Changes the user made since the last message        ← only when notes exist
- deleted /hero/ (2 components: /hero/a.html, /hero/b.html)
- moved /buttons/cta.html → /cta/cta.html
These are no longer part of the design: don't build on, reference or recreate them unless asked.

### Selected components                                  ← only with context_paths
The new brief is about these files. Start from their HTML.
#### /pricing/tiers.html
```html
…
```

### Latest result                                        ← only without a selection
(the files committed by the thread's latest completed run, ≤ 3, same HTML budget)

### Where your result goes
Never modify existing files. Your result becomes NEW files in the folder "/pricing/dark-theme"
(a new subfolder next to the selected component). You may choose a better folder and file names:
call submit_artifact with `folder` (a new subfolder for changes, a new top-level folder for a new
design) and `names` (one per component; keep the original file name when you make a new version of
one component).
```

  Budgets: total HTML ≈ 60 000 chars (≥ 2 000 per file), as `selectionContext` does today.

### 4.2 Destination hints from the agent (`agentrun`)

- `submit_artifact` (tools.go `submitTool`) gains optional `folder string` and `names []string`
  (descriptions: "Canvas folder for the result, e.g. /pricing/dark — a NEW subfolder when changing
  an existing component" / "File names, one per component, e.g. tiers.html"). When either is set, the
  tool output is always the JSON envelope `{"components": [...], "folder": "...", "names": [...]}`
  (also for one component); without them keep today's outputs.
- `parseComponentSet` / `NodeResult.parse`: accept the envelope with ≥ 1 item when `folder` or
  `names` is present; `NodeResult` gets `Folder string`, `Names []string`.
- Engine Agent node (`nodes.go`, artifact contract branch): store the hint on the produced candidate
  (`candidate.dest`) and on the iteration (`iterState.dest`); `iterRecord` keeps it, so
  `selectFinal` → `st.final.dest`. Multi-candidate (`count>1`, `generate_components`, `pick_best`):
  the hint of the orchestrating agent's final submit wins; worker hints are ignored.

### 4.3 Commit on finish (`engine.go` `finish`, `exec.go` `results`)

For `RunCompleted` with results, in **one transaction** with `result_artifacts`:
`canvas.Commit(tx, run.ThreadID, run.ID, items)` where item i = result artifact i, folder =
`final.dest.Folder` (cleaned; must not be an existing **file** path; may be an existing folder — the
names are then made unique) else `run.CanvasFolder`; name = `dest.Names[i]` else default (§4.4);
`SourcePath` = the selected file when exactly one file was selected; `Score` = result score.
Update `run.CanvasFolder` to the folder actually used. Add `canvas_paths: [...]` to the
`run_completed` event payload and to `RunDetail`. Failed/cancelled runs commit nothing (their drafts
stay run artifacts, shown by the client as before).

### 4.4 Defaults

- Folder: exactly one folder `F` holds all selected files → `F + "/" + Slug(brief, 3–5 words)`;
  otherwise (no selection / several folders) → `"/" + Slug(brief)`. Unique at commit time.
- Names: one selected file and one result → the selected file's name; otherwise
  `Slug(brief)-<n>.html` (`<n>` from 1; a single result → `Slug(brief).html`).

### 4.5 Agent mount (`agentrun`)

- `NodeRequest.ThreadID` (new); engine sets it from `run.ThreadID` for agent and judge nodes; put it
  in session metadata (`Metadata: {"owner", "thread_id"}`) and in `nodeScope` so nested runs built in
  `tools_candidates.go` (`generate_components`, `pick_best`, `critique_component`) pass it on.
- `sessionMount` (tools.go): when the session metadata has `thread_id`, add
  `vfs.MountPoint{Prefix: "/canvas", FS: canvas.FS(store, thread), ReadOnly: true}`. The Runtime
  gets the canvas store via fx (`agentrun` → `canvas` dependency). Ephemeral try sessions: no
  thread, no mount.
- Update the seeded `component-generator` guidance (studio seeds) with one paragraph about
  `/canvas`, D2 and `folder`/`names`; existing DB agents are not migrated (note in docs).

## 5. Frontend (`apps/frontend/src`)

### 5.1 API

- New `api/canvas.ts`: types `CanvasFile`, `CanvasLayout`, `CanvasNote`; `getCanvas(thread)`,
  `putCanvasLayout`, `deleteCanvasPaths`, `restoreCanvasOp`, `moveCanvasPath`.
- `api/runs.ts`: `context_artifact_ids` → `context_paths`; add `canvas_folder`, `canvas_notes`,
  `canvas_paths` (detail + `run_completed`).

### 5.2 Canvas page (`pages/chat/canvas/`)

- Data: `getCanvas(thread)` (files + layout) **and** the thread's runs (`listRuns`, as today) for
  messages in progress. Reload the canvas when any run reaches a terminal state (`onStatus`) and
  after every canvas operation.
- Tree → view: build folders from file paths. Top-level folders (and a "root" group for files directly
  under `/`) are the canvas groups; subfolders render as **nested group blocks** inside their parent
  (recursive `FolderGroup`, replacing `RunGroup`). Inside a folder: its files first (flow as today:
  wrap / `dir` row / col), then its subfolders.
- Folder header: folder name (last segment; full path as tooltip), "from message N: <brief>" (run of
  the folder's earliest file, from `run_id`), Details button (that run). Component label = file name
  (+ score); "draft" only for in-progress cards.
- **Runs in progress / failed**: a pending card (brief, `RunProgress`, drafts = latest iteration html
  artifacts as now via `useRunLive`/`componentsOf`, Stop / Try again / Reconnect / Details) shown
  inside the group of `run.canvas_folder` (a temporary group block when that folder doesn't exist
  yet). Completed runs show nothing extra — their files are in the tree. Keep `useRunLive`.
- `ComponentFrame`: keyed by `artifact_id` (immutable) — loading/caching stay as they are.
- Selection keys: `c:<filePath>`, `g:<folderPath>`; send `context_paths` (folder keys as folder
  paths — the server expands); chips show file / folder names. Max 12 files client-side check.
- Delete: `deleteCanvasPaths(selection paths)` → reload; toast **Undo** → `restoreCanvasOp(op_id)`.
  Remove the client `<meta>` mechanism entirely (`withMeta`, `META_RE`, `removed` / `pending` in the
  layout hook, the meta stripping in `briefOf`).
- New actions (toolbar + context menu): **Rename folder** (prompt → `move` folder), **Move to
  folder…** for components (prompt with folder path → `move`). Drag-and-drop between groups is out of
  scope.
- Layout: `useCanvasLayout` loads `layout` from `getCanvas`, keeps the same API (`setPos`, `setScale`,
  `setDir`, `reset`) and persists with `putCanvasLayout`, debounced 500 ms (flush on unmount /
  `pagehide`); localStorage only for the toolbar place (`wc-canvas-toolbar`, unchanged). Keys are
  paths: `g:<folder>` (free layout positions of top-level groups; nested groups use offsets like
  components), `c:<file>`. Server remaps keys on move.
- Labels / "about …": `run.context_paths` → file names.
- Keep everything from the last iteration (handles, drag any direction, uniform scale, arrange
  actions only when applicable, floating toolbar, collapsing title, hidden zoom controls, constant
  background dots, perf work).

## 6. Tests

Backend (from `apps/backend`: `go test -race ./internal/features/canvas ./internal/features/engine ./internal/features/agentrun`):
- canvas store: path cleaning/slugs/uniquify; commit; delete file + folder (one op, one note);
  restore (+ conflict 409, note removed); move file / folder (paths, `source_path`, layout keys
  remapped, conflict); notes taken once; layout validation/caps; access (owner vs other user vs admin).
- canvas FS: Stat/Read/List of implicit dirs, deleted files invisible, writes `ErrReadOnly`;
  `fstools` `glob` / `read_file` against it.
- engine (fake NodeRunner as in `engine_test.go`): completed run commits its results at the default
  folder; the agent's `folder`/`names` hint wins; multi-result names; selection → seed contains tree,
  selected HTML, "Where your result goes"; notes appear in the next run's seed exactly once and on
  `run.canvas_notes`; `context_paths` validation (unknown path, foreign thread, > 12, folder
  expansion); failed run commits nothing. Replace `TestSelectedComponentsSeedTheRun`.
- agentrun: session with `thread_id` metadata sees `/canvas/...` read-only through `sessionFS`;
  `submit_artifact` envelope with folder/names parses into `NodeResult.Folder/Names`.

Frontend: `npm run build` (tsc + vite) in `apps/frontend`. Update `tmp/canvas-smoke/smoke.mjs`: stub
`GET/PUT /threads/:id/canvas…` (+ delete/restore/move) in the route handler, files with nested
folders; checks: groups = folders incl. a nested subfolder, file labels, pending card of a running
run in its target folder, selection sends `context_paths`, delete → hidden + undo → restore call,
rename folder → move call, layout change → debounced PUT with path keys, plus the existing checks.
Run: `node tmp/canvas-smoke/smoke.mjs apps/frontend/dist tmp/canvas-smoke/out` (headless Chrome via
playwright-core, no servers).

## 7. Order of work

1. `models/canvas.go` + `canvas` store + FS + tests.
2. `agentrun`: `ThreadID` → metadata/scope, `/canvas` mount, `submit_artifact` folder/names + tests.
3. `engine`: `context_paths`, default folder, notes, `canvasContext`, dest hints, commit in `finish`,
   event payload + tests; remove `context_artifact_ids`, `threadContext`, `selectionContext`.
4. Canvas HTTP controller + module wiring (`app.go`) + tests.
5. Frontend API + canvas page rewrite to the tree; remove client meta/removed; server layout.
6. Smoke script, docs, delete this plan.

## 8. Docs to update when done

- New `docs/backend.canvas.md` (data model, path rules, store ops, FS adapter, HTTP, notes).
- `docs/backend.engine.md`: replace "Follow-ups" / "Selected components" with the canvas context,
  commit, `context_paths`, `canvas_folder` / `canvas_notes` / `canvas_paths`.
- `docs/backend.studio.md` "Tools": `/canvas` mount, `thread_id` metadata, submit `folder`/`names`.
- `docs/frontend.studio-ui.md`: Design canvas + Canvas layout sections (groups = folders, pending
  cards, server layout, delete/undo/rename/move, no `<meta>`), smoke coverage.
- `docs/backend.md` feature list: the canvas module.

## 9. Out of scope / later

Drag-and-drop of components between folders; agent tools that delete/move files; version history UI
(`source_path` already links versions); cleanup of old threads (user does it manually, D4);
real-time multi-device sync (reload on run end / own operations is enough for now).
