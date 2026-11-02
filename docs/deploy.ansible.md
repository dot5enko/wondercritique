# Deploy — Ansible

State as of 2026-11-02. Location: `deploy/ansible/`. Owned by a separate deploy
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
- headless Chrome for the renderer (added 2026-11-01, see
  `docs/backend.render.md`):
  - browser install is Debian-family only — **Ubuntu** gets Google Chrome
    stable from Google's apt repo (`dl.google.com/linux/chrome/deb`, signing
    key dearmored into `/etc/apt/keyrings/`, amd64 asserted; the Ubuntu
    `chromium` package is snap-only and cannot run under systemd), **Debian**
    gets the distro `chromium` deb; any other distro logs a warning and you
    must set `backend_render_chrome_path` yourself. The role is the single
    owner of the Chrome source: it removes the legacy
    `/etc/apt/keyrings/google-chrome.asc` keyring and deletes any other
    `sources.list[.d]` entry for `dl.google.com/linux/chrome/deb` before
    writing `/etc/apt/sources.list.d/google-chrome.list`
    (`signed-by=/etc/apt/keyrings/google-chrome.gpg`), so a stale `.deb`
    install or older role revision cannot leave apt with `Conflicting values
    set for option Signed-By` (fixed 2026-11-02); the dearmor step re-runs
    when the upstream key changes or the `.gpg` is missing
  - fonts: `fonts-noto-core`, `fonts-noto-color-emoji`, `fonts-liberation`,
    `fonts-dejavu-core` (plus `ca-certificates`, `gnupg` for the Chrome repo)
  - `{{ backend_state_dir }}/tmp` is created owned by the service user; the
    unit sets `HOME={{ backend_state_dir }}` and `TMPDIR=…/tmp` because
    `ProtectSystem=strict` makes the real `/tmp` read-only (Chrome needs a
    writable HOME, profile and temp dir; chromedp's profile dir follows
    `TMPDIR`)
  - systemd hardening adjusted for Chrome's sandbox (unit comments carry the
    rationale): `RestrictNamespaces=true` → allow-list `user mnt pid net`
    (Chrome enters user/mount/PID/network namespaces; cgroup, ipc, uts, time
    stay closed), `AF_NETLINK` added to `RestrictAddressFamilies` (Chrome's
    network service watches link/route changes). Reviewed and kept as-is:
    `NoNewPrivileges` + `RestrictSUIDSGID` (Chrome falls back to the
    user-namespace sandbox, no setuid helper), `PrivateDevices` (chromedp
    passes `--disable-dev-shm-usage`, so no `/dev/shm` needed), `ProtectHome`
    (Chrome installs under `/opt`/`/usr`), `ProtectProc`/`ProcSubset=pid`
    (Chrome only needs `/proc/self` and `/proc/<pid>`)
  - Ubuntu 24.04+ / Debian 13 (AppArmor 4) restrict unprivileged user
    namespaces host-wide; the role stats
    `/proc/sys/kernel/apparmor_restrict_unprivileged_userns` and, where it
    exists, writes `/etc/sysctl.d/60-wonder-critique-render.conf`
    (`kernel.apparmor_restrict_unprivileged_userns = 0`) and applies it via
    the `Apply render sysctl` handler — which runs in the pre-start handler
    flush, so the kernel allows Chrome's sandbox before the service starts
  - env knobs rendered into `backend.env`: `RENDER_CHROME_PATH`,
    `RENDER_CONCURRENCY`, `RENDER_TIMEOUT`, `RENDER_ALLOWED_HOSTS`,
    `RENDER_CHROME_NO_SANDBOX` from the `backend_render_*` role defaults
    (never set `backend_render_chrome_no_sandbox: true` in production)

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
- renderer knobs (`roles/backend/defaults/main.yml`, added 2026-11-01):
  `backend_render_chrome_path` (empty = auto-detect),
  `backend_render_concurrency` (2), `backend_render_timeout` (`20s`),
  `backend_render_allowed_hosts` (extra hosts fragments may load from,
  Google Fonts always allowed), `backend_render_chrome_no_sandbox`
  (false — keep it false)
- vault (all `vault_`-prefixed; empty placeholders live in vars.yml, real
  values in vault.yml): `vault_jwt_secret` (>= 32 bytes, asserted in
  pre_tasks), `vault_secrets_key` (>= 32 bytes, asserted in pre_tasks;
  rendered into backend `SECRETS_KEY` — AES-GCM key for stored provider API
  keys, prod refuses to start without it since 2026-11-02), resend key, mail-from, model API key, and since 2026-10-31 both
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
