#!/usr/bin/env bash
set -euo pipefail

# 开发环境：只启动前端 Vite（后端单独用 dev-backend.sh）
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FRONTEND_PORT="${FRONTEND_PORT:-5173}"
FRONTEND_LOG="${FRONTEND_LOG:-/tmp/go-agent-web.log}"

kill_port() {
  local port="$1"
  local pids
  # macOS: lsof -ti tcp:PORT
  pids="$(lsof -ti "tcp:${port}" 2>/dev/null || true)"
  if [[ -z "${pids}" ]]; then
    return 0
  fi
  echo "[frontend-dev] killing processes on port ${port}: ${pids}"
  kill ${pids} 2>/dev/null || true
  sleep 0.5
  pids="$(lsof -ti "tcp:${port}" 2>/dev/null || true)"
  if [[ -n "${pids}" ]]; then
    echo "[frontend-dev] force killing processes on port ${port}: ${pids}"
    kill -9 ${pids} 2>/dev/null || true
  fi
}

cd "${ROOT_DIR}/web"

if [[ ! -d "node_modules" ]]; then
  echo "[frontend-dev] node_modules 不存在，执行 npm install（仅首次会较慢）"
  npm install
fi

echo "[frontend-dev] ensuring port ${FRONTEND_PORT} is free..."
kill_port "${FRONTEND_PORT}"

echo "[frontend-dev] 运行：npm run dev -- --host 0.0.0.0 --port ${FRONTEND_PORT}"
echo "[frontend-dev] 日志文件：${FRONTEND_LOG}"
echo "[frontend-dev] (按 Ctrl+C 停止前端服务)"

rm -f "${FRONTEND_LOG}"

# 前台运行，方便实时看到 HMR / 接口错误
npm run dev -- --host 0.0.0.0 --port "${FRONTEND_PORT}" 2>&1 | tee "${FRONTEND_LOG}"

