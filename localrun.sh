#!/usr/bin/env bash
# Run backend (Go API, :8080) and frontend (Vite dev server, :5173) together.
# Ctrl+C (SIGINT/SIGTERM) stops both process groups.
#
# The backend loads apps/backend/.env itself (godotenv: real shell env wins,
# then .env). This script does NOT parse .env — it only peeks at a few keys for
# preflight hints, so bash never has to reproduce dotenv quoting rules.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND="$ROOT/apps/backend"
FRONTEND="$ROOT/apps/frontend"

PIDS=()

cleanup() {
  trap - INT TERM
  echo
  echo "Shutting down..."
  for pid in "${PIDS[@]:-}"; do
    kill -- "-$pid" 2>/dev/null || true
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  exit 0
}
trap cleanup INT TERM

# Each background job gets its own process group so cleanup can kill it whole.
set -m

prefix() { # label color
  while IFS= read -r line; do
    printf '\033[%sm[%s]\033[0m %s\n' "$2" "$1" "$line"
  done
}

# Peek at one key from a dotenv file without executing or interpreting it.
# Handles the common forms only (KEY=value, optional single/double quotes, no
# inline comments); good enough for hints, never for exporting.
env_get() { # file key
  [ -f "$1" ] || return 1
  local line
  line="$(grep -E "^${2}=" "$1" | tail -n 1)" || return 1
  [ -n "$line" ] || return 1
  line="${line#*=}"
  line="${line%\"}"; line="${line#\"}"
  line="${line%\'}"; line="${line#\'}"
  printf '%s' "$line"
}

port_busy() { # port -> 0 when something is listening
  lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
}

echo "Starting backend (http://localhost:8080) and frontend (http://localhost:5173)..."

# --- preflight --------------------------------------------------------------

if [ ! -f "$BACKEND/.env" ] && [ -f "$BACKEND/.env.example" ]; then
  echo "No apps/backend/.env found — creating one from .env.example"
  cp "$BACKEND/.env.example" "$BACKEND/.env"
fi

if [ ! -d "$FRONTEND/node_modules" ]; then
  echo "node_modules missing, running npm install..."
  (cd "$FRONTEND" && npm install)
fi

for p in 8080 5173; do
  if port_busy "$p"; then
    echo "warning: port $p is already in use — the corresponding service may fail to start" >&2
  fi
done

chrome_path="$(env_get "$BACKEND/.env" RENDER_CHROME_PATH || true)"
if [ -z "${chrome_path:-}" ]; then
  # Auto-detection inside the backend covers PATH + well-known locations
  # (incl. Playwright caches); warn only when we can see there is nothing.
  for c in \
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
    "/Applications/Chromium.app/Contents/MacOS/Chromium" \
    "$HOME/.cache/ms-playwright" \
    "$HOME/Library/Caches/ms-playwright"; do
    if [ -e "$c" ]; then
      chrome_path="found"; break
    fi
  done
  if [ -z "$chrome_path" ] && ! command -v google-chrome >/dev/null 2>&1 \
     && ! command -v chromium >/dev/null 2>&1 && ! command -v chrome >/dev/null 2>&1; then
    echo "warning: no Chrome/Chromium found — rendering will fail until one is installed" >&2
    echo "         (e.g. brew install --cask google-chrome, or set RENDER_CHROME_PATH in apps/backend/.env)" >&2
  fi
fi

# --- services ---------------------------------------------------------------

run_backend() {
  cd "$BACKEND"
  if command -v task >/dev/null 2>&1 && task --list 2>/dev/null | grep -qE '(^|[[:space:]])dev([[:space:]]|$)'; then
    # task loads .env via the Taskfile
    task dev
  else
    go run ./cmd/server
  fi
}

run_frontend() {
  cd "$FRONTEND"
  npm run dev -- --host
}

run_backend 2>&1 | prefix backend 34 &
PIDS+=($!)

run_frontend 2>&1 | prefix frontend 32 &
PIDS+=($!)

wait
