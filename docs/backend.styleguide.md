# Backend — style guides (vision-generated, pinned per chat thread)

**State as of 2026-11-05.** Package `internal/features/styleguide` (`styleguide.Module`: model,
`Service`, controller), model `internal/models/styleguide.go`. A style guide is a markdown
document a vision model writes from 1–4 sample images. A chat thread picks one with its
**first** message; it can't change afterwards. Every agent of the thread (the chat agent and
its nested gen_components / candidate workers) gets it in the **system prompt** and as a
read-only file **`/styleguide/STYLEGUIDE.md`**.

Verify: from `apps/backend`, `go test -race ./internal/features/styleguide
./internal/features/agentrun -run Styleguide ./internal/features/engine -run Styleguide`.

## Data model

`Styleguide` (`styleguides`, embeds `models.Base` → soft delete): `OwnerID`, `Name` (≤ 200,
"" until generated when the user gave none), `Notes` (user hints, ≤ 4000), `Images`
(`[]string` JSON, `wcblob://` refs in upload order), `Markdown` (≤ 100 000), `Status`
(`generating | ready | failed`), `Error`.

`runs.styleguide_id` (`models.Run.StyleguideID`) pins it per thread — see Engine below.

## HTTP (`/api/v1`, `auth.Required`; owner-scoped, admins can read/act on any by id)

| Route | Notes |
|---|---|
| `GET /styleguides` | `{items: [View]}` — the caller's own, newest first (≤ 200) |
| `POST /styleguides` | **multipart/form-data**: `images` (1–4 files, ≤ 8 MB each, sniffed PNG/JPEG/WebP/GIF), `name?`, `notes?` → **201** View in `generating`. Body cap 4×8 MB + 1 MB (413). JSON would hit `httpx.Decode`'s 1 MB limit, hence multipart |
| `GET /styleguides/{id}` | View; 404 for other users |
| `PATCH /styleguides/{id}` | `{name?, markdown?}` (absent = keep; empty → 400). A markdown edit sets `ready` (hand-written guide works even after a failed generation). 409 while generating |
| `DELETE /styleguides/{id}` | 204, soft delete. Threads that picked it keep following it (runtime lookups are `Unscoped`) |
| `POST /styleguides/{id}/regenerate` | regenerate from the stored images; 409 while generating |
| `GET /styleguides/{id}/images/{n}` | sample n (0-based) with its MIME, `nosniff`, `private, max-age=86400` |

View = `{id, name (fallback "Style guide"), status, error, notes, markdown, image_count,
created_at, updated_at}`.

## Generation (`service.go`)

- Async: `Create`/`Regenerate` launch a goroutine under the service's root context (10 min
  timeout); the client polls. fx `OnStart` → `Recover` marks leftover `generating` rows
  `failed` ("the server restarted…"); `OnStop` cancels and waits (10 s).
- Runs the seeded studio agent **`styleguide-generator`** (`studio.StyleguideAgentSlug`,
  `studio/defaults_styleguide.go`, ensured on every start like canvas-chat; uses model set
  `default`; editable in the studio) through `agentrun.Runtime.RunNode` (interface `Runner`,
  faked in tests): samples `images` (the `[]string` refs → image blocks), `name` (or "pick a
  short fitting name"), `notes` (or "(none)") — always set, since unknown placeholders stay
  verbatim in `BuildInput`. Contract `chat`, no tools; the system prompt dictates sections
  (Colors table, Typography, Spacing and layout, Shape and depth, Components, Imagery and icons,
  Do and don't, Design tokens `:root{…}` css block), markdown only.
- Result: `CleanMarkdown` (trim, unwrap a fence around the whole answer) → empty = failed;
  name = the user's, else `NameOf` (first `# ` heading minus " style guide"/" styleguide"/
  " design system"). Errors → `failed` + message (apperr message, timeout / shutdown text).

## Engine (pinning, `engine/start.go` `resolveStyleguide`)

- `POST /runs` body `styleguide_id`. New thread: must be a **ready** guide the caller owns
  (admins: any) → else 400 field `styleguide_id`. Follow-up (`thread_id`): inherits the
  thread head's `styleguide_id`; a different non-empty id → 400 "the style guide of a chat
  can't be changed"; the same id or none is fine.
- `styleguide_id` is on `GET /runs` items, `GET /runs/{id}`, and `GET /threads` items (head run).
- Agent nodes and pick_best judges pass `NodeRequest.StyleguideID`.

## Agent runtime (`agentrun/styleguide.go`)

- `NodeRequest.StyleguideID` → session metadata `styleguide_id` and `nodeScope.styleguide`;
  `toolEnv.styleguide(ctx)` passes it to every nested `NodeRequest` (gen_components,
  generate_components, pick_best, critique_component).
- `beforeRequest` (every model request of a session with the key) appends
  `StyleguideSystem`: `## Style guide` + "follow it; deviate only when the user explicitly
  asks" + `<styleguide>…</styleguide>` (cut at 40 000 chars with a pointer to the file).
  Built agents stay cached — the guide is per session, not per build.
- `sessionMount` adds `/styleguide` (memory FS, read-only) with `STYLEGUIDE.md`.
- Lookup: `Runtime.gdb` (the gorm DB from `NewRuntime`), `Unscoped`, missing id → no guide.
