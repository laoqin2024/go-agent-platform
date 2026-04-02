#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"

BACKEND_ADDR="${BACKEND_ADDR:-0.0.0.0:8080}"
REDIS_ADDR="${REDIS_ADDR:-127.0.0.1:6379}"

WEB_DIR="${ROOT_DIR}/web"

cleanup() {
  if [[ -n "${BACKEND_PID:-}" ]]; then
    kill "${BACKEND_PID}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

echo "[run] starting backend: go run ./cmd/server --addr ${BACKEND_ADDR}"
cd "${ROOT_DIR}"
go run ./cmd/server --addr "${BACKEND_ADDR}" --redis-addr "${REDIS_ADDR}" >/tmp/go-agent-backend.log 2>&1 &
BACKEND_PID=$!

echo "[run] starting frontend: npm run dev (web)"
cd "${WEB_DIR}"
if [[ ! -d "node_modules" ]]; then
  npm install
fi

# Do NOT default VITE_API_BASE_URL to localhost here.
# Keeping it empty allows Vite's `/api` + `/ws` proxy to forward to the Go backend,
# which is required for LAN clients.
if [[ -n "${VITE_API_BASE_URL:-}" ]]; then
  export VITE_API_BASE_URL
else
  unset VITE_API_BASE_URL || true
fi

npm run dev -- --host 0.0.0.0 --port 5173

