# Backend — canvas (shared file tree of a chat thread)

**State as of 2026-11-04.** Package `internal/features/canvas` (`canvas.Module`: models, `NewStore`,
controller), models in `internal/models/canvas.go`. The shared state between the user and the
agents of a chat thread is its **set of component files**: a tree of HTML files (folders = groups)
over immutable html artifacts. The canvas UI is a view of it plus a view-only layout; agents read
the same files through a read-only `/canvas` mount. The package must not import `engine` or
`agentrun` (both depend on it). Deps: `*gorm.DB`, `*blobs.Store`.

Verify: from `apps/backend`, `go test -race ./internal/features/canvas` (temp-file SQLite).

## Decisions

- Group = folder, per thread; folders nest. A component = one `.html` file.
- **Files are never modified in place**: a change is a new file in a new subfolder next to it
  (`/pricing/tiers.html` → "make it dark" → `/pricing/dark/tiers.html`); variants are new files too.
- Layout (positions, scale, flow) is stored server-side per thread; agents never see it.
- Old threads (before 2026-11-04) were not migrated: they simply have an empty canvas.
- User operations (delete, restore, move/rename) become **notes** handed to the **next** run of
  the thread, once (replaces the old client `<meta>` mechanism).

## Data model

- `CanvasFile` (`canvas_files`): `ID`, `CreatedAt`, `UpdatedAt`, `ThreadID`, `Path`
  (`/pricing/dark/tiers.html`), `ArtifactID` (html artifact), `RunID` (producer), `SourcePath`
  (the file it is a new version of, "" = new design), `Score`, `Size` (blob bytes), `DeletedAt
  *time.Time` + `DeleteOp` (soft delete grouped by operation id). It does **not** embed
  `models.Base` (that carries `gorm.DeletedAt`); every query filters `deleted_at IS NULL` itself.
  Partial unique index `idx_canvas_live (thread_id, path) WHERE deleted_at IS NULL` (gorm `where:`
  index option) + checks inside the write transactions.
- `CanvasState` (`canvas_states`, PK `thread_id`): `OwnerID`, `Layout` (JSON: `pos` map
  `"g:<folder>"|"c:<file>" → {x,y}`, `scale` file → number, `dir` folder → `row|col`), `Notes`
  (JSON `[{id, text, at}]`, pending = not yet delivered), `UpdatedAt`. Saved with `db.Save`
  (update, else insert).

## Path rules (`paths.go`)

- Absolute; segments lowercase `[a-z0-9-]` (no leading/trailing hyphen), ≤ 48 chars; files end
  in `.html` (stem follows the same rule); at most `MaxDepth` = 4 folder levels; folders are
  implicit. `/` is the root folder (files may live directly under it).
- `CleanFolder` / `CleanFile` validate strictly (`ErrBadPath`); `NormalizeFolder` /
  `NormalizeFile` / `NormalizeName` slug every segment (agent hints, move targets: `"Pricing/Dark
  Theme"` → `/pricing/dark-theme`, `"Tiers Card.HTML"` → `tiers-card.html`).
- `Slug(s)`: NFKD + drop combining marks (é → e), lowercase, non-alnum → `-`, collapsed, ≤ 48
  chars, fallback `design`. `BriefSlug(brief)`: same, stop words dropped ("make it dark" → `dark`,
  "Give me 5 buttons" → `5-buttons`), first 4 words.
- Uniqueness: `uniqueFile` appends `-2`, `-3`, … to the stem, `uniqueFolder` to the last segment.

## Store (`store.go`)

| Method | Notes |
|---|---|
| `Access(ctx, thread, Viewer)` | owner of the thread's first run; 404 when no runs or not the owner (admins see all) |
| `Files(ctx, thread)` | live files sorted by path |
| `PathsOfRun(ctx, runID)` | live paths a run committed (`[]` when none) |
| `Content(ctx, file)` | HTML bytes via the artifact's blob |
| `Resolve(ctx, thread, paths, field)` | selection → live files; a folder expands recursively (`/` = everything); dedupe; unknown → 400 field error on `field` |
| `Commit(ctx, thread, runID, Dest{Folder, Fresh}, items)` | inserts results as new files on the ctx transaction (`dbx.Conn`); `Fresh` makes the folder unique (default destinations), otherwise files join an existing folder and only names are uniquified; `Size` from the blob |
| `Delete(ctx, thread, owner, paths)` | files or folders (prefix) soft-deleted as one op (`op_id`); one note: `deleted /hero/ (2 components: /hero/a.html, /hero/b.html); /cta/c.html` (≤ 5 files listed); unknown path → 404 |
| `Restore(ctx, thread, owner, opID)` | un-deletes the op; 409 if a path is live again; removes the op's note while pending, else appends `restored …` |
| `Move(ctx, thread, owner, from, to)` | file → file or folder → folder (rename = move); `to` normalized; 409 when a target file exists or a folder target exists; 400 for moving into itself / depth > 4; rewrites `path`, `source_path`s pointing at moved files and the layout keys (`c:`/`g:`/scale/dir) in one tx; note `moved /a.html → /cta/a.html` / `moved folder /a/ → /b/ (N components)` |
| `State` / `PutLayout(ctx, thread, owner, layout)` | last write wins; keys must name paths of the thread (live or deleted; stale keys dropped), non-finite numbers dropped, scale clamped 0.25–4, dir ∈ row/col; > 256 KB or > 2000 entries per map → 400 |
| `TakeNotes(ctx, thread)` | returns and clears pending notes (engine, inside the run-creation tx) |
| `FS(thread) vfs.FS` | read-only adapter (below) |

User operations run in their own `dbx.Tx`; `Commit`/`TakeNotes` use the caller's tx from ctx.

## Read-only FS (`fs.go`)

`Store.FS(thread)` implements `vfs.FS` over the live rows (joined with `artifacts.blob_sha`),
recomputed on every call: implicit directories, `Stat`/`Read`/`List` per the vfs contract,
`Version` 1, `SHA256` = blob sha, `ModTime` = `CreatedAt`; `Write`/`Delete` → `ErrReadOnly` (after
`vfs.Clean`). Passes `vfstest.RunReadOnly` (rows seeded directly) and works with `fstools`
(`glob`, `read_file`). Agents see it at `/canvas` (see `docs/backend.studio.md` "Tools").

## HTTP (`controller.go`, `/api/v1`, authenticated, owner-scoped via `Access`)

| Route | Body → Response |
|---|---|
| `GET /threads/{id}/canvas` | `{thread_id, files: [{path, artifact_id, run_id, source_path, score, size, created_at}], layout: {pos, scale, dir}, pending_notes: [{id, text, at}]}` — empty arrays/maps, never null |
| `PUT /threads/{id}/canvas/layout` | `{layout}` → `{layout}` (the stored, filtered copy) |
| `POST /threads/{id}/canvas/delete` | `{paths: [...]}` (1–200) → `{op_id, deleted: [...]}` |
| `POST /threads/{id}/canvas/restore` | `{op_id}` → `{restored: [...]}` |
| `POST /threads/{id}/canvas/move` | `{from, to}` → `{moved: [{from, to}]}` |

Content keeps using `GET /artifacts/{id}/content` (engine, sandboxed CSP).

## Engine side (summary — details in `docs/backend.engine.md`)

`POST /runs` `context_paths` → `Resolve`; default destination folder on `run.canvas_folder`;
`TakeNotes` in the run-creation tx (`run.canvas_notes`); the canvas context seeds iteration 1;
a completed run's results are committed in `finish` (same tx as `result_artifacts`).

## Out of scope / later

Drag-and-drop between folders; agent tools that delete/move files; version history UI
(`source_path` links versions); cleanup of old threads; multi-device live sync (the client reloads
on run end and after its own operations).
