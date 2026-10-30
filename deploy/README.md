# Deploying wonder-critique API on the local machine (localhost)

Ansible setup that builds the Go API from `apps/backend`, installs it as a
systemd service, and puts an nginx vhost (`wonder-critique-api.cldfn.com`) in
front of it.

Layout:

```
deploy/ansible/
├── ansible.cfg
├── inventory.yml              # localhost, connection=local
├── playbook.yml
├── group_vars/all/
│   ├── vars.yml               # domain, CORS origins
│   ├── vault.yml.example      # copy to vault.yml, edit, encrypt
│   └── vault.yml              # ansible-vault encrypted secrets (gitignored)
└── roles/
    ├── backend/               # build + systemd service
    │   ├── defaults/main.yml  # ports, paths, app env
    │   ├── tasks/main.yml     # build, env file, unit, enable, health check
    │   ├── handlers/main.yml
    │   └── templates/
    │       ├── backend.env.j2
    │       └── wonder-critique-backend.service.j2
    └── nginx/                 # vhost + proxy
        ├── defaults/main.yml
        ├── tasks/main.yml
        ├── handlers/main.yml
        └── templates/wonder-critique-api.conf.j2
```

## 1. Configure secrets

```
cd deploy/ansible
cp group_vars/all/vault.yml.example group_vars/all/vault.yml
$EDITOR group_vars/all/vault.yml    # set JWT_SECRET (>= 32 bytes), keys...
ansible-vault encrypt group_vars/all/vault.yml
```

Optional (group_vars/all/vars.yml):

- `backend_cors_origins` — list of frontend origins, e.g.
  `["https://wonder-critique.com"]`

## 2. DNS (Cloudflare)

`wonder-critique-api.cldfn.com` is a proxied (orange cloud) record in the
Cloudflare zone `cldfn.com`; Cloudflare terminates TLS at the edge and forwards
to this machine's public IP over HTTP:80. The nginx vhost matches on
`server_name`, so Cloudflare forwarding `Host:` intact is all that's required.
Locally you can test with
`curl -H "Host: wonder-critique-api.cldfn.com" http://127.0.0.1/healthz`.

## 3. Run

```
cd deploy/ansible
ansible-playbook playbook.yml --ask-vault-pass
```

Tags (also selectable from the app's Tags tab; descriptions come from the
comment lines at the top of `playbook.yml`):

- `deploy` — the whole play (build + install backend, configure nginx);
- `backend` — build, install and run the Go API as a systemd service;
- `nginx` — install and validate the nginx vhost for the API.

```
ansible-playbook playbook.yml --ask-vault-pass --tags backend   # backend only
ansible-playbook playbook.yml --ask-vault-pass --tags nginx     # nginx only
ansible-playbook playbook.yml --ask-vault-pass --tags deploy    # everything
```

Conventions the tag discovery relies on (keep them when editing):

- every deployable play/task carries explicit literal tags — comma string or
  list both fine, never templated (a `tags: "{{ var }}"` form is skipped by
  discovery);
- roles are tagged at the `roles:` level (`- role: backend, tags: [backend]`),
  not with tags inside the role;
- the secrets guard pre-task is tagged `always` so it runs under any tag
  selection; discovery drops `always`/`never` from the visible tag list;
- per-tag descriptions are parsed from playbook comment lines shaped like
  `ansible-playbook playbook.yml --tags <tag> ... # <description>`.

The playbook:

1. builds `apps/backend` (`CGO_ENABLED=0 go build`, mirroring Taskfile) and
   installs the binary to `/opt/wonder-critique/bin/server`;
2. writes env to `/etc/wonder-critique/backend.env` and the unit to
   `/etc/systemd/system/wonder-critique-backend.service` (dedicated system
   user, sqlite state in `/var/lib/wonder-critique`, bind only 127.0.0.1);
3. installs the nginx vhost to `/etc/nginx/conf.d/wonder-critique-api.conf`
   and validates with `nginx -t` before reloading;
4. verifies `http://127.0.0.1:8080/readyz` and the proxied vhost.

## 4. TLS

None at the origin: Cloudflare terminates TLS in front (`wonder-critique-api.cldfn.com`
is proxied through Cloudflare) and talks to this machine over HTTP:80. The nginx
vhost intentionally has no 443 listener. Client IP reaches the backend intact
because the proxy passes Cloudflare's `True-Client-IP` / `CF-Connecting-IP`
through, which chi's RealIP middleware consumes.

If Cloudflare's SSL mode is "Full (strict)" later, upload a Cloudflare Origin
Certificate (issued from the Cloudflare dashboard) on a 443 listener — but the
default expectation here is plain HTTP back to Cloudflare.

## Day-2

```
systemctl status wonder-critique-backend
journalctl -u wonder-critique-backend -f
systemctl restart wonder-critique-backend
cd deploy/ansible && ansible-playbook playbook.yml --ask-vault-pass  # re-deploy
# pick pieces: add --tags backend|nginx|deploy

The cached tag list is refreshed on page refresh in the app; after editing
tags in `playbook.yml`, refresh the Tags tab so discovery re-runs.
```
