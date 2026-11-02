# Frontend

State as of 2026-11-02 (app shell + router + API layer + auth + all studio/runs/chat sections).

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
| `src/components/` | shared app components: `Modal`, `ConfirmDialog`, `SchemaForm` (JSON-Schema subset + `ui:*` hints), `VersionBar` (save-draft/publish/versions/diff/restore — calls `api/studio` itself), `Diff` (`diffJson`/`diffText` + `DiffView`), `PageHeader`, `ListPage`, `Errors` (`Loadable`, `ErrorState`, `ForbiddenState`, `NotFoundState`), `Markdown` (react-markdown + GFM; wrapper-level Tailwind styling, no raw HTML, links target _blank), `format` (relative/absolute time); barrel `index.ts` |
| `src/hooks/` | `useAsync` (abortable, stale-safe), `useDebouncedCallback`, `useDirtyDraft` (dirty tracking + 800 ms debounced draft autosave) |
| `src/pages/` | one dir per route section (below); pages are lazy-loaded default exports |
| `src/pages/showcase/` | the old design-system showcase, preserved at `/showcase` (dev-only) |

## Routes

| Route | Guard | Page |
|---|---|---|
| `/login` | public | email-code login (start → verify, resend countdown from 60 s / `Retry-After`) |
| `/` | auth | redirect → `/runs` |
| `/runs`, `/runs/:id` | auth | runs list / run detail (`src/pages/runs/`) |
| `/chat` | auth | chat with published agents (`src/pages/chat/`, `?agent=<slug>` default `critic`, `?id=<sessionId>` deep links into a session and survives a refresh) |
| `/studio/pipelines` | admin | list + `?id=` graph editor (`src/pages/studio/pipelines/`) |
| `/studio/agents` | admin | list + `?id=` editor + Try-it panel |
| `/studio/model-sets` | admin | list + `?id=` editor |
| `/studio/skills`, `/studio/rules` | admin | lists + `?id=` editors |
| `/settings` | admin | LLM providers (two-pane list/detail) |

Shell layout (`src/pages/shell.tsx`): viewport frame + Sidebar — Workspace (Runs, Chat), Studio
section (admin), Admin (Settings); footer = Avatar + name/email + role Badge + logout. Non-admins
get hidden nav items and a Forbidden page on admin routes.

Studio pages switch list↔editor via the `?id=` search param inside their `index.tsx`
(no nested routes). Draft editing uses `useDirtyDraft` (debounced autosave `PUT …/draft`) +
`VersionBar` for publish/versions/diff/restore.

## API contract status (important)

The studio/settings/runs **backend does not exist yet** (plan phases 4–6). The `src/api/*` modules
are the authoritative frontend contract typed from `plan.critique-engine.md` §3.3–§5 + §4.7; open
points are marked `ASSUMPTION` comments in `studio.ts`/`settings.ts`/`runs.ts` (path segment ==
kind string, list envelope `{items, next_cursor}`, version/restore shapes, export/import bundle,
SSE event payloads). When the backend lands, reconcile only those marked spots. `agents.ts` matches
the existing agentkit httpapi surface (docs/backend.md).

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

- **(2026-11-02) Studio + Settings are invisible in practice**: nav hides them unless `user.role === 'admin'`
  (`pages/shell.tsx`), and the `ADMIN_EMAILS` bootstrap (plan §7) is not implemented in the backend — every
  login is role `user`. Even as admin the pages would error: studio/runs/settings endpoints don't exist yet.

- Identity (name/slug/description) of studio defs is fixed at creation — no `PUT /{kind}/{id}`
  endpoint in the plan; editors render identity read-only. Backend decision pending.
- Skill "run script in the sandbox" has no endpoint yet (`TODO` in `SkillsEditor.tsx`).
- Chat session titles are local-only (no title-update endpoint; first message truncates to 60 chars).
- Cross-run compare (`?compare=`) not implemented (iterations-compare is).
- Deprecated-provider toggle in Settings skipped (models.dev marks models, not providers, deprecated).
- `docs/llm.models-catalog.md` is the catalog format reference for the Settings page.
