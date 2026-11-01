#!/usr/bin/env bash
# Run backend (Go API, :8080) and frontend (Vite dev server, :5173) together.
# Ctrl+C stops both.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND="$ROOT/apps/backend"
FRONTEND="$ROOT/apps/frontend"

PIDS=()

# Kill each service's process group (including npm/vite and go run children).
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

run_backend() {
  cd "$BACKEND"
  if command -v task >/dev/null 2>&1; then
    # task dev loads .env via the Taskfile
    task dev
  else
    # fallback: load .env manually and run from source
    if [ -f .env ]; then
      while IFS='=' read -r key value; do
        case "$key" in ''|\#*) continue ;; esac
        export "$key=$value"
      done < .env
    fi
    go run ./cmd/server
  fi
}

echo "Starting backend (http://localhost:8080) and frontend (http://localhost:5173)..."

if [ ! -f "$BACKEND/.env" ] && [ -f "$BACKEND/.env.example" ]; then
  echo "No apps/backend/.env found — creating one from .env.example"
  cp "$BACKEND/.env.example" "$BACKEND/.env"
fi

if [ ! -d "$FRONTEND/node_modules" ]; then
  echo "node_modules missing, running npm install..."
  (cd "$FRONTEND" && npm install)
fi

run_backend 2>&1 | prefix backend 34 &
PIDS+=($!)

(cd "$FRONTEND" && npm run dev -- --host) 2>&1 | prefix frontend 32 &
PIDS+=($!)

wait