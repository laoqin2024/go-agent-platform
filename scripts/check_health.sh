#!/usr/bin/env bash
set -euo pipefail

# check_health.sh - Quick self-check for Go Agent Server in production
#
# Usage:
#   ./scripts/check_health.sh [BASE_URL] [REDIS_ADDR]
#
# Examples:
#   ./scripts/check_health.sh http://127.0.0.1:8080 127.0.0.1:6379
#   ./scripts/check_health.sh https://server.example.com:8443 10.0.0.10:6379

BASE_URL="${1:-http://127.0.0.1:8080}"
REDIS_ADDR="${2:-127.0.0.1:6379}"

say() { printf '%s\n' "$*"; }
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || fail "missing dependency: $1"
}

need curl
need redis-cli

say "[1/4] HTTP healthz: ${BASE_URL}/healthz"
curl -fsS "${BASE_URL}/healthz" >/dev/null && say "OK"

say "[2/4] Redis PING: ${REDIS_ADDR}"
redis-cli -a "${REDIS_PASSWORD:-}" -h "${REDIS_ADDR%:*}" -p "${REDIS_ADDR##*:}" PING >/dev/null && say "OK"

say "[3/4] Swagger UI (optional): ${BASE_URL}/swagger/index.html"
if curl -fsS "${BASE_URL}/swagger/index.html" >/dev/null; then
  say "OK"
else
  say "WARN: swagger not reachable (may be disabled/restricted in production)"
fi

say "[4/4] Agent version API (optional): ${BASE_URL}/api/v1/agent/version?os=linux&arch=amd64&device_id=healthcheck-0"
if curl -fsS "${BASE_URL}/api/v1/agent/version?os=linux&arch=amd64&device_id=healthcheck-0" >/dev/null; then
  say "OK"
else
  say "WARN: agent/version not available (config.yaml missing/invalid, or rollout rules block this device_id)"
fi

say "DONE: basic checks passed"

