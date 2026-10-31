# Frontend

State as of 2026-10-31.

## Location & stack

`apps/frontend` — bare Vite + TypeScript + React scaffold, nothing else (no router, no state
management, no CSS framework, no linter). Created from the official `create-vite` react-ts
template (create-vite 9.3.3), trimmed down to essentials.

- Vite 8 (`vite` ^8.3.5), React 19 (`react` ^19.3.1), TypeScript ~7.0.2, `@vitejs/plugin-react` ^6.1.2
- npm for package management; `package-lock.json` committed
- Not part of `go.work` (Go workspace only covers `apps/backend*`)

## Layout

Standard create-vite structure:

| Path | Purpose |
|---|---|
| `index.html` | entry HTML, mounts `#root`, loads `/src/main.tsx` |
| `src/main.tsx` | `createRoot` + `StrictMode`, imports `./index.css` |
| `src/App.tsx` | root component (currently just an `<h1>`) |
| `src/index.css` | minimal global styles |
| `vite.config.ts` | react plugin only, no proxy/server config yet |
| `tsconfig.json` | project references → `tsconfig.app.json` (app code, `src/`) + `tsconfig.node.json` (`vite.config.ts`) |

## Commands

| Command | What it does |
|---|---|
| `npm run dev` | Vite dev server with HMR (default http://localhost:5173) |
| `npm run build` | `tsc -b` type-check, then production build to `dist/` |
| `npm run preview` | serve the production build |

Verified working 2026-10-31: `npm install` clean, `npm run build` passes (type-check + build).

## Deployment (2026-10-31)

The `frontend` Ansible role (`deploy/ansible/roles/frontend`, see
`docs/deploy.ansible.md`) builds on the box (`npm ci` + `npm run build`) and
installs `dist/` to `/opt/wonder-critique/frontend`, served by an nginx vhost
at `vault_frontend_domain` (vault.yml, example `wonder-critique.cldfn.com`) with SPA fallback
(`try_files $uri /index.html`), no-cache on index.html and immutable caching
for `/assets/`. No systemd service — static files only. Deploy tag: `frontend`.

## Not yet decided / done

- No dev-time proxy to the Go backend (`apps/backend`, :8080) in `vite.config.ts`
- No API client / fetch layer yet (see backend auth endpoints in `docs/backend.auth.md` once researched)
- No lint/format tooling (backend uses golangci-lint; frontend has none by design so far)
- Frontend calls the API cross-origin on `vault_backend_domain`; the origin must be
  listed in `backend_cors_origins` (currently empty)
- Root `.gitignore` has no frontend entries; `apps/frontend/.gitignore` (from template) covers
  `node_modules`, `dist`, logs
