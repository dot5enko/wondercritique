# Repository overview

State as of 2026-10-31.

## Layout

```
apps/
  backend/     Go API server (see docs/backend*.md)
  frontend/    Vite + TS + React SPA (see docs/frontend.md)
deploy/ansible/  Ansible: backend + frontend + nginx roles (see docs/deploy.ansible.md)
go.work        Go workspace: apps/backend + apps/backend/agentkit
```

- Go 1.27.2 workspace (`go.work`), backend has its own `Taskfile.yml` for dev/build/test/lint
- Frontend is npm-based, independent of the Go workspace
- Deploy is Ansible (`deploy/ansible`), run by a separate deploy tool — app work only
  authors the files, never runs playbooks
- `.env` files exist per app (backend has `.env.example`); secrets in env files are not trusted/used
  for anything but running the apps
- Temp/work files go to `./tmp` inside the repo (not system /tmp)
