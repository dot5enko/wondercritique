# Deploy — Ansible

State as of 2026-10-31. Location: `deploy/ansible/`. Owned by a separate deploy
tool; app work only authors the files, playbooks are never run from app work.

## Layout

```
deploy/ansible/
├── ansible.cfg
├── inventory.yml              # single host wonder-critique-api = localhost, connection=local
├── playbook.yml               # one play: backend + frontend + nginx roles
├── group_vars/all/
│   ├── vars.yml               # non-secret vars (domains, CORS)
│   └── vault.yml(.example)    # encrypted secrets (gitignored); JWT_SECRET etc.
└── roles/
    ├── backend/               # build Go API, systemd service
    ├── frontend/              # build SPA, install static files + its own vhost
    └── nginx/                 # API vhost + proxy to backend
```

## Roles

### backend (tag: `backend`)

- system user `wonder-critique-backend`; binary built from
  `{{ playbook_dir }}/../../apps/backend` (`CGO_ENABLED=0 go build -trimpath
  -ldflags "-s -w"`, mirror of Taskfile) → `/opt/wonder-critique/bin/server`
- env: `/etc/wonder-critique/backend.env` (mode 0640, group = service),
  unit `/etc/systemd/system/wonder-critique-backend.service`
- state (sqlite) in `/var/lib/wonder-critique`; binds `127.0.0.1:11111`
  (`backend_port`, role default); probes `/readyz` after start

### frontend (tag: `frontend`) — added 2026-10-31

- no systemd service — nginx serves static files from disk
- ensures nginx installed, then `npm ci --no-audit --no-fund` +
  `npm run build` in `{{ playbook_dir }}/../../apps/frontend` (Node must be on
  the box); requires committed `package-lock.json` for `npm ci`
- wipes `/opt/wonder-critique/frontend` and copies `dist/` in (fresh copy every
  deploy, so stale hashed bundles never accumulate; docroot briefly empty
  mid-deploy); file modes come from build output, no explicit `mode=`
- vhost `/etc/nginx/conf.d/wonder-critique-frontend.conf` (from
  `wonder-critique-frontend.conf.j2`): plain HTTP (Cloudflare terminates TLS),
  `try_files $uri /index.html` SPA fallback, `no-cache` on `index.html`,
  `immutable` 1-y cache on `/assets/`, gzip; handler `Reload nginx for frontend`
  (name deliberately differs from nginx role's `Reload nginx` — handler names
  are per-play and would shadow each other)
- health check: `GET http://127.0.0.1/` with `Host: {{ vault_frontend_domain }}`
  must return 200
- knobs (`roles/frontend/defaults/main.yml`): `frontend_install_dir`,
  `frontend_nginx_conf_path`, `frontend_assets_path`, `frontend_asset_max_age`,
  `frontend_source_dir`

### nginx (tag: `nginx`)

- installs nginx package; API vhost
  `/etc/nginx/conf.d/wonder-critique-api.conf` proxies everything to
  `127.0.0.1:{{ backend_port }}` (SSE-friendly: buffering off, 300s timeouts,
  passes CF client-IP headers through for chi RealIP); `nginx -t` then reload
- the frontend vhost lives in the frontend role, not here

## Vars (group_vars/all/vars.yml + vault.yml)

- `backend_cors_origins: []` — rendered into backend `CORS_ORIGINS`;
  frontend origin goes here for cross-origin API calls
- vault (all `vault_`-prefixed; empty placeholders live in vars.yml, real
  values in vault.yml): `vault_jwt_secret` (>= 32 bytes, asserted in
  pre_tasks), resend key, mail-from, model API key, and since 2026-10-31 both
  vhost domains — `vault_backend_domain` (wonder-critique-api.cldfn.com),
  `vault_frontend_domain` (wonder-critique.cldfn.com); pre_tasks assert both
  non-empty so a missing vault entry fails fast instead of rendering
  `server_name ""`

## Playbook conventions (tag discovery — keep when editing)

- single play `Deploy wonder-critique (backend + frontend + nginx)`, host group
  `wonder-critique-api`, `become: true`
- every deployable play/task carries explicit literal tags; never templated
  tags; roles tagged at `roles:` level only, not inside roles
- secrets guard pre_task tagged `always`
- per-tag descriptions parsed from comment lines shaped like
  `ansible-playbook playbook.yml --tags <tag> # <description>`
- run: `ansible-playbook playbook.yml --ask-vault-pass` (`--tags
  backend|frontend|nginx|deploy` for pieces)

## TLS / DNS

Cloudflare (proxied records) terminates TLS; origin nginx listens plain HTTP:80
only, no 443. Backend API behind `wonder-critique-api.cldfn.com` (zone
`cldfn.com`); frontend at `wonder-critique.cldfn.com` — both proxied records
already point at this machine and forward `Host:` intact.
