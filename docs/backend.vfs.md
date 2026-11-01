# backend.vfs — virtual file system & file tools

**State as of 2026-11-01.** Current state only; this file describes the code as it is, not its history.

Everything lives in the `agentkit` module (root `apps/backend/agentkit`), Go workspace `go.work`:

| Package | Path | Purpose |
|---|---|---|
| `vfs` | `agentkit/vfs` | Contract: `FS` interface, `FileInfo`, errors, `Clean`, `Limits`, search helpers, wrappers |
| `vfs/memory` | `agentkit/vfs/memory` | In-process implementation (tests, ephemeral workspaces) |
| `vfs/sqlite` | `agentkit/vfs/sqlite` | Persistent, workspace-scoped implementation on any SQLite `database/sql` driver |
| `vfs/vfstest` | `agentkit/vfs/vfstest` | Conformance suite every implementation runs against |
| `tools/fstools` | `agentkit/tools/fstools` | The agent tools: `read_file`, `write_file`, `edit_file`, `glob`, `grep` |

## Contract (`vfs.FS`)

```go
type FS interface {
    Stat(ctx, path) (FileInfo, error)
    Read(ctx, path) ([]byte, error)
    Write(ctx, path, data) error
    Delete(ctx, path) error
    List(ctx, dir string, recursive bool) ([]FileInfo, error)
}
```

Implementations must be safe for concurrent use. The rules below are enforced by every
implementation and checked by the `vfstest` suite:

- **Paths** are absolute, slash-separated keys (`/work/notes.md`); directories are **implicit** —
  a directory exists while at least one file lives under it; `/` always exists. A file and a
  directory can never share a path: `Write` to a directory path → `ErrIsDir`, `Write` under an
  existing file path → `ErrNotDir`.
- `Clean` validates/normalizes: absolute, valid UTF-8, no NUL/control characters, no `\`, no `.` or
  `..` segments, ≤ `MaxPathLen` (512) bytes, each segment ≤ `MaxSegmentLen` (255) bytes; repeated
  and trailing slashes collapse. Anything else → `ErrInvalidPath` (wrapped `*pathError`).
- `Stat` of a file returns its `FileInfo`; of an implicit directory `{Path, IsDir: true}` (size 0,
  version 0, no digest/modtime); otherwise `ErrNotFound`.
- `Read`: directory → `ErrIsDir`, missing → `ErrNotFound`. `Write` creates or replaces atomically
  (parents implicit) and bumps `Version`. `Delete`: single file only; directory → `ErrIsDir`,
  missing → `ErrNotFound`.
- `List(dir, false)` = direct children (files + implicit subdirectories); `List(dir, true)` = every
  file below `dir` (no directory entries). Sorted by path. Missing dir → `ErrNotFound`, file →
  `ErrNotDir`.
- Read-only file systems return `ErrReadOnly` from `Write`/`Delete`. Size/quota violations return
  `ErrTooLarge`/`ErrQuota` and leave the FS unchanged.

`FileInfo`: `Path`, `IsDir`, `Size`, `SHA256` (hex digest of content), `Version` (starts at 1,
incremented on every write), `ModTime` — the last three files only.

Sentinel errors (match with `errors.Is`, wrap with `%w`):
`ErrNotFound`, `ErrInvalidPath`, `ErrIsDir`, `ErrNotDir`, `ErrReadOnly`, `ErrTooLarge`, `ErrQuota`,
plus `ErrBadPattern` from the search helpers.

Helpers: `Dir`, `Base`, `IsWithin` (path containment), `Parents` (ancestors excluding `/`),
`Children` (derive direct children from a flat file list), `SortInfos`.

## Limits

```go
type Limits struct {
    MaxFileSize  int64 // per file; default 1 MiB  (DefaultMaxFileSize)
    MaxTotalSize int64 // per workspace; default 20 MiB (DefaultMaxTotalSize)
    MaxFiles     int   // per workspace; default 500 (DefaultMaxFiles)
}
```

Zero fields mean "default" (`WithDefaults`). Checks: `CheckFileSize(size)` → `ErrTooLarge`;
`CheckQuota(usage, size, replaces, oldSize)` → `ErrQuota` (replacing never changes the file count;
only the size delta counts). Implementations run check + write atomically.

## Implementations

### `vfs/memory`

`memory.New(limits) *FS` — empty in-process FS. Content is copied on `Write` and `Read`; guarded by
an `RWMutex`; quota/limit checks happen under the write lock. `Usage()` reports current files/bytes.
Meant for tests, ephemeral workspaces, and startup content (wrap with `vfs.ReadOnly`).

### `vfs/sqlite`

`sqlite.New(db, opts...)` builds a `Store` over any `database/sql` SQLite driver
(modernc, glebarez, mattn). It creates its table idempotently. One table holds **all** workspaces:

```sql
CREATE TABLE IF NOT EXISTS vfs_files (
    workspace_id TEXT NOT NULL,
    path         TEXT NOT NULL,
    size         INTEGER NOT NULL,
    sha256       TEXT NOT NULL,
    version      INTEGER NOT NULL,
    mod_time     INTEGER NOT NULL,   -- Unix nanos, UTC
    data         BLOB NOT NULL,
    PRIMARY KEY (workspace_id, path)
)
```

- Table name = `<prefix>files`, prefix default `vfs_` (`WithTablePrefix`).
- `data` is the last column on purpose: SQLite reads columns in order, so metadata-only queries
  (List, quota sums) never touch overflow pages of large blobs. The PK index serves exact lookups
  and the path range scans (`bounds(dir)` → `[dir+"/", dir+"0")` in binary collation, which also
  sidesteps LIKE wildcards in paths).
- `Store.Workspace(id, limits) vfs.FS` — workspaces are implicit (unknown id = empty workspace).
  `Store.DeleteWorkspace(ctx, id)`, `Store.CopyWorkspace(ctx, src, dst)` (dst must be empty →
  `ErrWorkspaceNotEmpty`; preserves content/version/modtime; limits not checked).
- Concurrency: a store-wide mutex serializes writes; each write (dir check, parent check, quota
  check, upsert) runs in one transaction; `List` reads in one transaction for a consistent
  snapshot. `Write` version starts at 1, `version = version + 1` on replace.

### `vfs.ReadOnly`

`ReadOnly(fsys) FS` — a view whose `Write`/`Delete` return `ErrReadOnly` (after path validation, so
invalid paths still yield `ErrInvalidPath`); reads pass through unchanged. Idempotent (wrapping an
already read-only FS returns it).

### `vfs.Mount`

`NewMount(points ...MountPoint) (*Mount, error)` composes several FSs into one tree.
`MountPoint{Prefix, FS, ReadOnly}`: `Prefix` is the outer directory the FS appears under (`/work`,
`/skills`, or `/` for a root FS); the inner FS sees paths **relative** to the prefix (with
`/skills`, outer `/skills/a/SKILL.md` is `/a/SKILL.md` inside). `ReadOnly` makes writes under the
prefix fail with `ErrReadOnly` regardless of the inner FS.

- **Longest-prefix routing**: points are sorted by descending prefix length; a path is served by
  the first (longest) prefix containing it (`route`). Nested mounts are allowed (`/` plus
  `/work/shared`); the inner mount shadows the outer FS under its prefix.
- **Always-present directories**: `/`, every prefix, and all their ancestors exist as directories
  (`synth`) — `Stat` reports dirs, `Read`/`Write`/`Delete` on them → `ErrIsDir`, `List` of their
  parent includes them. Outer files colliding with such a directory are hidden.
- **List merges**: non-recursive shows the routed FS's children plus the mount points (or their
  first segment) directly below `dir`; recursive returns every visible file across all mounts with
  paths re-prefixed into the outer tree (`visible` drops entries another mount shadows).
- **Uncovered paths** (no `/` mount): `Stat`/`Read`/`List` → `ErrNotFound`, `Write`/`Delete` →
  `ErrReadOnly` (nothing is mounted there).
- `NewMount` fails on an invalid prefix, nil FS, or duplicate prefix; zero points give a tree where
  only `/` exists. Errors from mounted FSs pass through unchanged.

## Search helpers (`vfs/search.go`)

### `Glob(ctx, fsys, pattern, limit) ([]FileInfo, error)`

Absolute doublestar pattern (`github.com/bmatcuk/doublestar/v4`) matched against cleaned paths:
`*`/`?` within one segment, `**` across segments, `[classes]`, `{alt,ernatives}`, backslash escapes.
Only files are returned (never directories), sorted by path, at most `limit` entries
(`<= 0` = unlimited; ask for `limit+1` to detect truncation). A relative or malformed pattern →
`ErrBadPattern` (with details). The pattern is split at its literal prefix
(`doublestar.SplitPattern`), so only that directory is listed — anchored patterns stay cheap; a
missing base directory yields no matches, not an error.

### `Grep(ctx, fsys, GrepOptions) (matches []GrepMatch, truncated bool, err error)`

`GrepOptions`: `Pattern` (RE2, required), `Glob` (file filter, default `/**`), `IgnoreCase`,
`Context` (clamped to `0..MaxGrepContext` = 5), `MaxMatches` (`<= 0` unlimited).

- Files are selected by `Glob`, then read and matched line by line (`SplitLines`); matches are
  ordered by path, then line. `GrepMatch` = `{Path, Line (1-based), Text, Before, After}`.
- **Binary files are skipped** (see `IsText`: valid UTF-8 without NUL); files deleted mid-search
  are skipped. `truncated=true` when `MaxMatches` was hit. Malformed pattern/glob → `ErrBadPattern`.
- `IsText(data)`, `SplitLines(text)` (splits on `\n`, drops trailing `\r`, final newline does not
  create an extra line) are exported for tools.

## File tools (`tools/fstools`)

`fstools.New(fsys vfs.FS, opts Options) []tool.Tool` returns the tools in the order
`read_file, write_file, edit_file, glob, grep`; with `opts.ReadOnly` only
`read_file, glob, grep`. Tools hold no state; the FS decides what is readable/writable. Every tool
is exercised through `tool.Tool.Call(ctx, json.RawMessage)` (strict input decoding: unknown fields
rejected).

`Options` (zero = default): `ReadOnly`; `MaxReadLines` **2000**; `MaxLineLength` **2000**
(characters shown per line by `read_file`/`grep`; longer lines are cut with
`… [line truncated: N characters]`); `MaxGlobResults` **500**; `MaxGrepMatches` **200**.

| Tool | Input | Output |
|---|---|---|
| `read_file` | `path` (required), `offset` (1-based line, default 1), `limit` (≤ line limit) | Lines as `<6-wide number>\t<line>` (prefix not part of the content); footer `(lines A–B of N; use offset=X to read more)` / `…; end of file`; empty file → `(path is empty)` |
| `write_file` | `path`, `content` (required; `""` = empty file) | JSON `{"path","bytes","version"}` (version starts at 1) |
| `edit_file` | `path`, `old_string` (required, exact match), `new_string` (required, `""` deletes), `replace_all` (default false) | JSON `{"path","replacements","version"}` |
| `glob` | `pattern` (required; no leading `/` → relative to `/`) | One `path (N bytes)` per line, sorted; none → `No files match P.`; over cap → `(showing the first N matches; more files match, use a more specific pattern)` |
| `grep` | `pattern` (required, RE2), `glob` (default all files; a name-only glob like `*.md` matches at any depth), `ignore_case`, `context` (0–5) | `path:line: text` for matches, `path-line- text` for context lines, `--` separates non-adjacent groups (and files, when context > 0); none → `No lines match "P" in files matching G.`; over cap → `(stopped after N matching lines; …)` |

Behavior notes:

- `read_file`: past-the-end offsets are an error (says how many lines the file has); negative
  offset/limit rejected; binary files rejected (`binary file (N bytes)`); directory → advice to use
  `glob`.
- `edit_file`: `old_string` must occur **exactly once** unless `replace_all` — otherwise errors
  saying the count and what to do; zero matches re-read advice, plus hints when the input looks
  like it still has `read_file` line-number prefixes or matches modulo whitespace; identical
  old/new rejected; binary files rejected; CRLF files adapt a `\n`-based old/new to `\r\n`.
- `grep`: `context` is clamped to `vfs.MaxGrepContext` (5); bad regex/glob →
  `invalid pattern or glob: …`; binary files skipped.
- **Error translation**: vfs errors are rewritten for the model — `ErrNotFound` suggests a `glob`
  pattern for the parent directory, `ErrIsDir`/`ErrNotDir` suggest a corrected path,
  `ErrReadOnly`/`ErrTooLarge`/`ErrQuota` say what to do instead; `context.Canceled`/
  `DeadlineExceeded` pass through untouched.

## Tests

- `vfstest.Run(t, newFS)` — conformance suite (invalid paths, implicit dirs, file/dir conflicts,
  list shapes, limits incl. default limits, concurrent writers/quota, context cancellation);
  `vfstest.RunReadOnly` — read-only view; `vfstest.Fixture()` — the shared listing fixture.
- `vfs`, `vfs/memory`, `vfs/sqlite`, `tools/fstools` all have test files; run with
  `go test -race ./vfs/... ./tools/...` from `apps/backend/agentkit`.
