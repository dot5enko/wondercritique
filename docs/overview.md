# Repository overview

State as of 2026-10-31.

## Layout

```
apps/
  backend/     Go API server (see docs/backend*.md)
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

## localrun.sh (2026-10-31)

Root-level dev runner: starts backend (`apps/backend`, :8080) and frontend
(`apps/frontend`, :5173) in parallel with `[backend]`/`[frontend]` log prefixes.
Ctrl+C (or TERM) kills both process groups (`set -m` + `kill -- -PGID`; needed
because npm detaches vite and `go run` spawns a child server).

- Backend: uses `task dev` when `task` is installed (Taskfile loads `.env`);
  otherwise falls back to `go run ./cmd/server` after parsing `.env`
  line-by-line (plain `source` breaks on the unquoted `MAIL_FROM=... <...>` value).
- Creates `apps/backend/.env` from `.env.example` if missing (backend fails to
  start without it, e.g. mailer requires `MAIL_FROM` when `RESEND_API_KEY` is
  set — a stray exported `RESEND_API_KEY` in the shell breaks startup without `.env`).
- Frontend: runs `npm install` first if `node_modules` is missing, then `npm run dev -- --host`.
