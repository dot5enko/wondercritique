# Deploy — Docker Compose

State as of 2026-11-05. A one-command local/self-hosted stack, independent of the Ansible
deploy (`docs/deploy.ansible.md`). Images verified to build (`docker compose build`); the stack
was not started from app work.

## Files

| Path | Purpose |
|---|---|
| `docker-compose.yml` | services `backend` + `frontend`, named volume `data` |
| `.env.example` (root) | compose interpolation vars (`PORT`, `APP_ENV`, secrets, admin emails, LLM keys); copy to `.env` (gitignored) — everything has a default |
| `apps/backend/Dockerfile` | build context = **repo root** (needs `go.work` + `agentkit`); `golang:1.27-trixie` → `CGO_ENABLED=0` static build; runtime `debian:trixie-slim` + `chromium` + Noto/Liberation/DejaVu fonts + curl |
| `.dockerignore` (root) | excludes `.git`, `tmp`, `docs`, `deploy`, `apps/frontend`, binaries, `.env`, `*.db` |
| `apps/frontend/Dockerfile` | context `apps/frontend`; `node:24-alpine` `npm ci` + build with `VITE_API_BASE_URL=""` (same origin) → `nginx:1.29-alpine` |
| `apps/frontend/nginx.conf` | SPA fallback, immutable `/assets/`, `/api/` → `http://backend:8080` with SSE settings (buffering off, 300 s timeouts) |

## Behaviour

- Only the frontend publishes a port (`${PORT:-8080}:80`); the backend is internal. Same-origin
  API → `CORS_ORIGINS` not needed.
- Backend runs as uid 10001 (`app`, HOME `/home/app` — Chrome needs a writable HOME), `DB_PATH=/data/app.db`
  (blobs live in the same SQLite), `RENDER_CHROME_PATH=/usr/bin/chromium`. Image HEALTHCHECK curls
  `/readyz`; frontend `depends_on` it with `service_healthy`.
- `APP_ENV` defaults to `dev`: without `RESEND_API_KEY` login codes are logged (`docker compose logs backend`)
  and shown on the login page with a "Local mode" notice (don't expose such a stack publicly), empty
  `JWT_SECRET` → per-process secret. `prod` needs `JWT_SECRET`, `SECRETS_KEY`, `RESEND_API_KEY`, `MAIL_FROM`.
- Chrome sandbox: backend has `security_opt: seccomp=unconfined, apparmor=unconfined` so Chromium's
  user-namespace sandbox works inside the container (Docker's default profiles block it) instead of
  using `RENDER_CHROME_NO_SANDBOX=true` (still overridable via env as a fallback).
- LLM keys passed through: `META_MODEL_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` (models.dev
  env names); others can be set in Settings → Providers.
