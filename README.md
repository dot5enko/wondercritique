# Wonder Critique

An AI design studio for UI components. Describe what you need in a chat, and AI agents generate HTML
components. A headless browser renders them, critic models review the screenshots and layout
metrics, and the agents keep improving the components until they pass a quality bar. Results land
on a pan/zoom canvas, where you can organise them, refine them and download them.

## Features

- **Design chat on a canvas**: each chat is a full-screen canvas of generated components. Select
  components to give an agent context, and move, delete or restore them.
- **Critique loop**: the stages are generate → validate → render (light and dark) → critique →
  gate. The loop repeats until the score is good enough, and the run graph updates live.
- **Studio** (admin): visual editors for pipelines, agents, model sets, skills and rules. Drafts
  and versions are kept, and you can publish them.
- **Any LLM provider**: providers come from the [models.dev](https://models.dev) catalog
  (OpenAI, Anthropic, Meta and others). Configure them in Settings → Providers.
- **Sandboxed tooling**: agents work in a virtual file system and run JS in a WebAssembly sandbox.
  Components are rendered in an isolated Chromium.
- **Passwordless login**: users sign in with a one-time code sent by email.

Stack: Go (chi, fx, GORM/SQLite) · React 19 + Vite + Tailwind · Chromium for rendering.

## Run with Docker Compose

```sh
cp .env.example .env        # optional: set ADMIN_EMAILS, JWT_SECRET, LLM keys
docker compose up -d --build
```

Open http://localhost:8080. To sign in, enter your email address. In dev mode the login code is
printed in the backend logs:

```sh
docker compose logs -f backend
```

Data (SQLite) is stored in the `data` volume. Stop the stack with `docker compose down`. To also
wipe the data, run `docker compose down -v`.

## Run locally

Requirements: Go 1.27+, Node 22+, and Chrome or Chromium (needed for rendering).

```sh
./localrun.sh
```

This starts the backend on http://localhost:8080 and the frontend on http://localhost:5173.
On the first run, it creates `apps/backend/.env` from `.env.example`. Edit that file to set
`ADMIN_EMAILS` and your keys. Login codes are printed in the backend output.

To start the two parts separately:

```sh
cd apps/backend && go run ./cmd/server     # or: task dev
cd apps/frontend && npm install && npm run dev
```

## Configuration

| Variable | Purpose |
|---|---|
| `ADMIN_EMAILS` | Comma-separated list of users who get admin access (Studio, Settings) |
| `JWT_SECRET`, `SECRETS_KEY` | Each must be at least 32 bytes. Required when `APP_ENV=prod` |
| `RESEND_API_KEY`, `MAIL_FROM` | Send login codes by email. Required in prod |
| `*_API_KEY` | LLM provider keys. You can also add them in Settings → Providers |

To generate a secret, run `openssl rand -base64 48`. The full list of backend variables is in
`apps/backend/.env.example`.
