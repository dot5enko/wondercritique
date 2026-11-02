# Frontend

State as of 2026-11-02 (chat-first restructure: design chat as the main screen, run list removed,
pipeline editor onboarding). Base stack unchanged.

## Location & stack

`apps/frontend` — Vite + TypeScript + React 19 + Tailwind v4 design system (`src/ui/`, see
`docs/frontend.design-system.md`). npm; `package-lock.json` committed; not part of `go.work`.

- Vite 8, React 19, TypeScript ~7.0.2, `@vitejs/plugin-react`
- Extra runtime deps: `lucide-react`, `react-router` v7 (createBrowserRouter, data router),
  `@xyflow/react` v12 (pipeline editor + read-only run graph), `react-markdown` +
  `remark-gfm` (chat/agent text via `components/Markdown.tsx`), self-hosted fonts.
- No global state library, no chart lib (score chart is hand-rolled SVG), no linter (by design so far).

## Layout

| Path | Purpose |
|---|---|
| `src/main.tsx` | `createRoot` + StrictMode; fonts + `ui/styles.css` |
| `src/App.tsx` | router root: `createBrowserRouter` + `RouterProvider` |
| `src/ui/` | design system (untouched by app code; app never edits it) |
| `src/api/` | typed API layer — `client` (fetch wrapper, ApiError `{"error":{code,message,fields}}`, single-flight refresh, session in localStorage `wc_access`/`wc_refresh`/`wc_user`), `sse` (fetch-based SSE for POST+GET with Authorization), `auth`, `studio`, `settings`, `runs`, `agents`, `types`, barrel |
| `src/features/auth.tsx` | `AuthProvider`/`useAuth`, `RequireAuth`/`RequireAdmin` guards, boot hydration via `GET /auth/me` |
| `src/components/` | shared app components: `Modal`, `ConfirmDialog`, `SchemaForm` (JSON-Schema subset + `ui:*` hints), `VersionBar` (save-draft/publish/versions/diff/restore — calls `api/studio` itself), `Diff` (`diffJson`/`diffText` + `DiffView`), `PageHeader`, `ListPage`, `Errors` (`Loadable`, `ErrorState`, `ForbiddenState`, `NotFoundState`, `isBackendMissing` — true for ApiError 404/501/503 or a network TypeError, used to show "backend isn't available yet" states), `Markdown`, `format`; barrel `index.ts` |
| `src/hooks/` | `useAsync` (abortable, stale-safe), `useDebouncedCallback`, `useDirtyDraft` (dirty tracking + 800 ms debounced draft autosave) |
| `src/pages/` | one dir per route section (below); pages are lazy-loaded default exports |
| `src/pages/showcase/` | the old design-system showcase, preserved at `/showcase` (dev-only) |

## Routes

| Route | Guard | Page |
|---|---|---|
| `/login` | public | email-code login (start → verify, resend countdown from 60 s / `Retry-After`) |
| `/` | auth | redirect → `/chat` |
| `/chat` | auth | **Design chat** — every chat is a pipeline run (`src/pages/chat/`, see `docs/frontend.studio-ui.md`) |
| `/runs/:id` | auth | full run detail (`RunDetailView` from `src/pages/runs/detail.tsx`; also embedded in the chat drawer). `/runs` redirects to `/chat` — the run list was removed; history lives in the chat rail |
| `/studio/pipelines` | admin | list + `?id=` graph editor (`src/pages/studio/pipelines/`) |
| `/studio/agents` | admin | list + `?id=` editor + Try-it panel |
| `/studio/model-sets` | admin | list + `?id=` editor |
| `/studio/skills`, `/studio/rules` | admin | lists + `?id=` editors |
| `/settings/providers` | admin | LLM providers (two-pane list/detail); `/settings` redirects here |

Shell layout (`src/pages/shell.tsx`, 2026-11-02): viewport frame + **one** sidebar column (`w-60`),
always exactly one level deep, two modes:
- **Chats mode** (default, every non-settings route): header "Wonder Critique" + New-chat
  IconButton (→ `/chat`); body = the design-chat thread list (`ThreadList` from
  `pages/chat/rail.tsx`, click → `/chat?thread=<id>`); footer = a single "Settings" NavItem
  (admin only, → `/studio/pipelines`) + user footer (Avatar, name/email, role Badge, logout).
- **Settings mode** (admin and path `/studio/*` or `/settings*`): header "Settings", "Back to
  chats", then a flat list: Pipelines, Agents, Models (`/studio/model-sets`), Providers
  (`/settings/providers`), Skills, Rules.
The thread list is shared via `ThreadsProvider`/`useThreads()` (`pages/chat/threads.tsx`,
mounted by Shell, one `listRuns({limit:100})` query) — the chat page reads and reloads it.
Non-admins never see Settings and get a Forbidden page on admin routes. There is deliberately
**no standalone agent chat** (removed 2026-11-02): agents are only used through pipelines; the
agent editor's Try-it panel is the only direct way to call one.

Studio pages switch list↔editor via the `?id=` search param inside their `index.tsx`
(no nested routes). Draft editing uses `useDirtyDraft` (debounced autosave `PUT …/draft`) +
`VersionBar` for publish/versions/diff/restore.

## API contract status (important)

**Studio + settings backend implemented (2026-11-02, plan phases 3–5)** — `docs/backend.studio.md`
is the backend reference. Reconciled and verified by `npm run typecheck`:
- `studio.ts` — generic CRUD (SINGULAR kind paths), list envelope `{items, next_cursor}`,
  versions/restore, export/import bundle, catalog (`StudioCatalog` matches `studio.CatalogData`),
  preview/try (`/studio/agents/{id}/preview|try`, try = SSE with the same event mapping as chat),
  `/studio/pipelines/{id}/validate` → `{ok, issues}`. The remaining `ASSUMPTION` comments in
  `studio.ts` are resolved in the implemented shapes (draft/versions shapes match; `next_cursor`
  is always `null` for now — list offset/limit supported).
- `settings.ts` — `GET/PUT /settings/providers/{id}`, `/test`, `/catalog/refresh` implemented
  (`synced_at`, `model_count`, `has_key` only, `rpm_limit: null` = unlimited).
- `agents.ts` — the generic chat mount `/agents/{slug}/…` serves every published agent
  (agentkit httpapi surface, unchanged).
- `runs.ts` — **implemented by the engine (2026-11-02, plan phase 6; `docs/backend.engine.md`)**:
  `POST /runs` → 201 `{id, thread_id, status}`, `GET /runs` (offset paging, `filter[thread_id]`),
  `GET /runs/{id}` (+ `pipeline_body` snapshot), `/runs/{id}/events` SSE (history replay, closes
  after the terminal event), `/runs/{id}/cancel`, `/artifacts/{id}/content`. Critique scores run
  0–10. Typecheck passes against these shapes; the chat has not been exercised against a live model
  in this environment (backend flow is covered by engine tests with fake agents/renderer).

## Commands

| Command | What it does |
|---|---|
| `npm run dev` | Vite dev server (:5173), `/api` proxied to `http://localhost:8080` (changeOrigin, ws off) |
| `npm run typecheck` | `tsc -b` |
| `npm run build` | type-check + production build to `dist/` |
| `npm run preview` | serve the production build |

Verified 2026-11-01: typecheck ✓, build ✓ (per-section code-split chunks), full smoke over all 16
routes with stubbed API — zero console/page errors (`tmp/frontend-smoke.mjs`; pipeline editor and
run detail additionally smoke-tested by their agents with stubbed API, `tmp/pipelines-smoke*.cjs`).

## Deployment

Unchanged from 2026-10-31: `frontend` Ansible role builds on the box and serves `dist/` via nginx
vhost with SPA fallback (see `docs/deploy.ansible.md`). Production API is cross-origin on
`vault_backend_domain` (base URL override `VITE_API_BASE_URL`); the origin must stay in
`backend_cors_origins`.

## Known gaps / TODO

- Studio/settings backends exist (2026-11-02); the studio pages can drop their "backend isn't
  available yet" callouts (`isBackendMissing`) once wired to the real API (the callouts now read
  "…can't be reached"). The runs engine exists (phase 6) — the design chat runs end to end.
- Identity: name/description of studio defs are editable via `components/IdentityCard` (agent and
  model-set editors; `PATCH /studio/{kind}/{id}`); slug is immutable. Skills/rules/pipelines editors
  have no identity card yet (2026-11-02).
- Skill "run script in the sandbox" has no endpoint yet (`TODO` in `SkillsEditor.tsx`).
- Design chat thread titles use `input.brief` of the first run (list items carry `input`; fallback: date).
- Cross-run compare (`?compare=`) not implemented (iterations-compare is).
- Deprecated-provider toggle in Settings skipped (models.dev marks models, not providers, deprecated).
- `docs/llm.models-catalog.md` is the catalog format reference for the Settings page (now parsed
  server-side; adapter resolution + base-URL vars live in `internal/features/llm`).
- Import bundles that cross-reference defs by id (agent → model_set_id) only import cleanly into
  a database containing those ids; cross-database import needs id remapping (future work).

## Verification (2026-11-02)

typecheck ✓, build ✓, `tmp/chat-smoke.mjs` (design chat: two-iteration run, drawer, follow-up
message, zero console errors) ✓, `tmp/studio-smoke.cjs` (template create → 8 nodes + loop edge +
9 wires, help strip dismiss persists, palette hints, When/Gets/Sends panel, agents list) ✓. Both
stub `/api/v1` with Playwright routes against the vite dev server (`SMOKE_BASE` overrides the
port — the dev server may land on 5174/5175 when 5173 is busy). Note: the real studio API paths
are SINGULAR (`/studio/pipeline`, `/studio/agent`) — smoke stubs must match.
