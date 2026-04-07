#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BACKEND_PORT="${BACKEND_PORT:-8080}"
FRONTEND_PORT="${FRONTEND_PORT:-5173}"
BACKEND_ADDR="${BACKEND_ADDR:-0.0.0.0:${BACKEND_PORT}}"
REDIS_ADDR="${REDIS_ADDR:-127.0.0.1:6379}"

BACKEND_LOG="${BACKEND_LOG:-/tmp/go-agent-backend.log}"
FRONTEND_LOG="${FRONTEND_LOG:-/tmp/go-agent-web.log}"

kill_port() {
  local port="$1"
  # macOS: lsof -ti tcp:PORT
  local pids
  pids="$(lsof -ti "tcp:${port}" 2>/dev/null || true)"
  if [[ -z "${pids}" ]]; then
    return 0
  fi
  echo "[restart] killing processes on port ${port}: ${pids}"
  # Try graceful first.
  kill ${pids} 2>/dev/null || true
  sleep 1
  pids="$(lsof -ti "tcp:${port}" 2>/dev/null || true)"
  if [[ -n "${pids}" ]]; then
    echo "[restart] force killing processes on port ${port}: ${pids}"
    kill -9 ${pids} 2>/dev/null || true
  fi
}

wait_port() {
  local host="$1"
  local port="$2"
  local max_try="${3:-40}"
  local i
  for ((i=1; i<=max_try; i++)); do
    if python3 - "${host}" "${port}" <<'PY' >/dev/null 2>&1
import socket, sys
host = sys.argv[1]
port = int(sys.argv[2])
s = socket.socket()
s.settimeout(0.3)
try:
    s.connect((host, port))
    sys.exit(0)
except Exception:
    sys.exit(1)
finally:
    s.close()
PY
    then
      return 0
    fi
    sleep 0.25
  done
  return 1
}

echo "[restart] stopping old servers (by ports)"
kill_port "${BACKEND_PORT}"
kill_port "${FRONTEND_PORT}"

echo "[restart] starting backend (go run ./cmd/server) [后台模式，详细日志在 ${BACKEND_LOG}]"
cd "${ROOT_DIR}"
rm -f "${BACKEND_LOG}"
go run ./cmd/server --addr "${BACKEND_ADDR}" --redis-addr "${REDIS_ADDR}" > "${BACKEND_LOG}" 2>&1 &
BACKEND_PID=$!
echo "[restart] backend pid: ${BACKEND_PID}, log: ${BACKEND_LOG}"

echo "[restart] starting frontend (npm run dev in ./web) [后台模式，详细日志在 ${FRONTEND_LOG}]"
cd "${ROOT_DIR}/web"
if [[ ! -d "node_modules" ]]; then
  npm install
fi
rm -f "${FRONTEND_LOG}"
npm run dev -- --host 0.0.0.0 --port "${FRONTEND_PORT}" > "${FRONTEND_LOG}" 2>&1 &
FRONTEND_PID=$!
echo "[restart] frontend pid: ${FRONTEND_PID}, log: ${FRONTEND_LOG}"

echo "[restart] waiting for ports to open..."
if wait_port "127.0.0.1" "${BACKEND_PORT}"; then
  echo "[restart] backend is listening on :${BACKEND_PORT}"
else
  echo "[restart] backend did not open :${BACKEND_PORT} within timeout. Check ${BACKEND_LOG}"
fi

if wait_port "127.0.0.1" "${FRONTEND_PORT}"; then
  echo "[restart] frontend is listening on :${FRONTEND_PORT}"
else
  echo "[restart] frontend did not open :${FRONTEND_PORT} within timeout. Check ${FRONTEND_LOG}"
fi

echo "[restart] done. You can visit:"
echo "  - http://localhost:${FRONTEND_PORT}"
echo "  - backend: http://${BACKEND_ADDR}"

