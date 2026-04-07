#!/usr/bin/env bash
set -euo pipefail

# 开发环境：只启动 Go 后端（前端单独用 dev-frontend.sh）
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_PORT="${BACKEND_PORT:-8080}"
BACKEND_ADDR="${BACKEND_ADDR:-0.0.0.0:${BACKEND_PORT}}"
REDIS_ADDR="${REDIS_ADDR:-127.0.0.1:6379}"
BACKEND_LOG="${BACKEND_LOG:-/tmp/go-agent-backend.log}"

kill_port() {
  local port="$1"
  local pids
  # macOS: lsof -ti tcp:PORT
  pids="$(lsof -ti "tcp:${port}" 2>/dev/null || true)"
  if [[ -z "${pids}" ]]; then
    return 0
  fi
  echo "[backend-dev] killing processes on port ${port}: ${pids}"
  kill ${pids} 2>/dev/null || true
  sleep 0.5
  pids="$(lsof -ti "tcp:${port}" 2>/dev/null || true)"
  if [[ -n "${pids}" ]]; then
    echo "[backend-dev] force killing processes on port ${port}: ${pids}"
    kill -9 ${pids} 2>/dev/null || true
  fi
}

echo "[backend-dev] cd ${ROOT_DIR}"
cd "${ROOT_DIR}"

echo "[backend-dev] ensuring port ${BACKEND_PORT} is free..."
kill_port "${BACKEND_PORT}"

echo "[backend-dev] building & running: go run ./cmd/server --addr \"${BACKEND_ADDR}\" --redis-addr \"${REDIS_ADDR}\""
echo "[backend-dev] log file: ${BACKEND_LOG}"
echo "[backend-dev] (按 Ctrl+C 停止后端服务)"

rm -f "${BACKEND_LOG}"

# 前台运行，stdout/stderr 同时写入控制台和日志文件，方便实时调试
go run ./cmd/server --addr "${BACKEND_ADDR}" --redis-addr "${REDIS_ADDR}" 2>&1 | tee "${BACKEND_LOG}"

