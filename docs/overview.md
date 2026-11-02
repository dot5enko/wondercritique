# Repository overview

State as of 2026-10-31.

## Layout

```
apps/
  backend/     Go API server (see docs/backend*.md; pipeline engine: docs/backend.engine.md)
  frontend/    Vite + TS + React SPA (see docs/frontend.md)
deploy/ansible/  Ansible: backend + frontend + nginx roles (see docs/deploy.ansible.md)
go.work        Go workspace: apps/backend + apps/backend/agentkit
localrun.sh    Run backend + frontend dev servers together (see below)
```

- Go 1.27.2 workspace (`go.work`), backend has its own `Taskfile.yml` for dev/build/test/lint
- Frontend is npm-based, independent of the Go workspace
- Deploy is Ansible (`deploy/ansible`), run by a separate deploy tool — app work only
  authors the files, never runs playbooks
- `.env` files exist per app (backend has `.env.example`); secrets in env files are not trusted/used
  for anything but running the apps
- Temp/work files go to `./tmp` inside the repo (not system /tmp)

## localrun.sh (2026-11-01)

Root-level dev runner: starts backend (`apps/backend`, :8080) and frontend
(`apps/frontend`, :5173) in parallel with `[backend]`/`[frontend]` log prefixes.
Ctrl+C (SIGINT/SIGTERM) kills both process groups (`set -m` + `kill -- -PGID`; needed
because npm detaches vite and `go run` spawns a child server).

- Does **not** parse `.env` itself: the backend loads `apps/backend/.env` on startup
  (godotenv — real env wins, then `.env`), so bash never reproduces dotenv quoting.
  `env_get` only *peeks* at single keys for preflight hints.
- Backend runner: `task dev` when `task` is installed **and** `task --list` reports a
  `dev` task; otherwise falls back to `go run ./cmd/server`.
- Preflight: creates `apps/backend/.env` from `.env.example` when missing; warns when
  port 8080/5173 is already listening; warns when no Chrome/Chromium is found (checks
  `RENDER_CHROME_PATH`, well-known macOS app paths, Playwright caches under
  `~/Library/Caches/ms-playwright` and `~/.cache/ms-playwright`, and PATH) — rendering
  is lazy, so this is a hint, not a startup failure.
- Verified 2026-11-01: start + `/healthz` 200 + frontend 200, `.env` auto-create, both
  backend fallbacks (no `task`, and `task` without a `dev` task), busy-port warning,
  Chrome-missing warning, and clean group shutdown on Ctrl+C (no leftover processes).
