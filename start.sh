#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

BACKEND_PORT="${BACKEND_PORT:-3200}"
FRONTEND_PORT="${FRONTEND_PORT:-3201}"
FRONTEND_HOST="${FRONTEND_HOST:-0.0.0.0}"
PORT_KILL_GRACE_SECONDS="${PORT_KILL_GRACE_SECONDS:-10}"
LOG_ROOT="${LOG_ROOT:-${ROOT_DIR}/logs/dev}"
BACKEND_APP_LOG_DIR="${BACKEND_APP_LOG_DIR:-${ROOT_DIR}/logs/backend}"
BACKEND_LOG="${LOG_ROOT}/backend.log"
FRONTEND_LOG="${LOG_ROOT}/frontend.log"

START_DEPS="${START_DEPS:-1}"
POSTGRES_CONTAINER="new-api-dev-pg"

BACKEND_PID=""
FRONTEND_PID=""
TAIL_PID=""

usage() {
  cat <<'EOF'
Usage:
  ./start.sh

Environment:
  BACKEND_PORT=3200              Backend port.
  FRONTEND_PORT=3201             Frontend dev-server port.
  PORT_KILL_GRACE_SECONDS=10     Seconds to wait before force-killing port listeners.
  LOG_ROOT=./logs/dev            Combined dev log directory.
  START_DEPS=1                   Start the Compose PostgreSQL (default); new-api runs locally.
  POSTGRES_PORT=54329            Compose PostgreSQL host port (configure in .env).

Examples:
  ./start.sh
  START_DEPS=1 ./start.sh
  BACKEND_PORT=3202 FRONTEND_PORT=3203 ./start.sh
EOF
}

log() {
  printf '[start] %s\n' "$*"
}

fail() {
  printf '[start] ERROR: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

stop_port_listener() {
  local port="$1"
  local label="$2"
  local -a pids=()

  while IFS= read -r pid; do
    if [[ -n "${pid}" ]]; then
      pids+=("${pid}")
    fi
  done < <(lsof -tiTCP:"${port}" -sTCP:LISTEN 2>/dev/null || true)

  if (( ${#pids[@]} == 0 )); then
    return
  fi

  log "stopping ${label} port ${port} listeners: ${pids[*]}"
  kill "${pids[@]}" 2>/dev/null || true

  for _ in $(seq 1 "${PORT_KILL_GRACE_SECONDS}"); do
    local listener_running=0
    for pid in "${pids[@]}"; do
      if kill -0 "${pid}" 2>/dev/null; then
        listener_running=1
        break
      fi
    done
    if (( listener_running == 0 )); then
      return
    fi
    sleep 1
  done

  log "force stopping ${label} port ${port} listeners"
  for pid in "${pids[@]}"; do
    if kill -0 "${pid}" 2>/dev/null; then
      kill -KILL "${pid}" 2>/dev/null || true
    fi
  done

  for _ in $(seq 1 20); do
    local listener_running=0
    for pid in "${pids[@]}"; do
      if kill -0 "${pid}" 2>/dev/null; then
        listener_running=1
        break
      fi
    done
    if (( listener_running == 0 )); then
      return
    fi
    sleep 0.1
  done

  fail "unable to release ${label} port ${port}"
}

ensure_embed_assets() {
  mkdir -p "${ROOT_DIR}/web/default/dist" "${ROOT_DIR}/web/classic/dist"

  if [[ ! -f "${ROOT_DIR}/web/default/dist/index.html" ]] ||
    grep -Eq '<body>use frontend dev server</body>|<!-- start.sh dev redirect -->' \
      "${ROOT_DIR}/web/default/dist/index.html"; then
    cat >"${ROOT_DIR}/web/default/dist/index.html" <<EOF
<!doctype html>
<!-- start.sh dev redirect -->
<html>
  <head>
    <meta charset="UTF-8" />
    <title>Opening frontend dev server</title>
    <script>
      window.location.replace(
        window.location.protocol + '//' + window.location.hostname + ':${FRONTEND_PORT}' +
          window.location.pathname + window.location.search + window.location.hash
      )
    </script>
  </head>
  <body>Opening frontend dev server...</body>
</html>
EOF
  fi

  if [[ ! -f "${ROOT_DIR}/web/classic/dist/index.html" ]] ||
    grep -Eq '<body>use frontend dev server</body>|<!-- start.sh dev redirect -->' \
      "${ROOT_DIR}/web/classic/dist/index.html"; then
    cat >"${ROOT_DIR}/web/classic/dist/index.html" <<EOF
<!doctype html>
<!-- start.sh dev redirect -->
<html>
  <head>
    <meta charset="UTF-8" />
    <title>Opening frontend dev server</title>
    <script>
      window.location.replace(
        window.location.protocol + '//' + window.location.hostname + ':${FRONTEND_PORT}' +
          window.location.pathname + window.location.search + window.location.hash
      )
    </script>
  </head>
  <body>Opening frontend dev server...</body>
</html>
EOF
  fi
}

ensure_frontend_dependencies() {
  if [[ ! -d "${ROOT_DIR}/web/node_modules" && ! -d "${ROOT_DIR}/web/default/node_modules" ]]; then
    log "installing frontend dependencies"
    (cd "${ROOT_DIR}/web" && bun install)
  fi
}

start_deps_once() {
  require_command docker
  bash "${ROOT_DIR}/scripts/start-local-postgres.sh"
}

start_backend() {
  : >"${BACKEND_LOG}"
  log "backend log: ${BACKEND_LOG}"
  (
    cd "${ROOT_DIR}"
    local_postgres_dsn="$(bun run "${ROOT_DIR}/scripts/local-postgres-dsn.ts")"
    export SQL_DSN="${local_postgres_dsn}"
    export SQLITE_PATH="${SQLITE_PATH:-one-api.db?_busy_timeout=30000}"
    export TZ="${TZ:-Asia/Shanghai}"
    export ERROR_LOG_ENABLED="${ERROR_LOG_ENABLED:-true}"
    export BATCH_UPDATE_ENABLED="${BATCH_UPDATE_ENABLED:-true}"
    export PORT="${BACKEND_PORT}"
    exec go run . --port "${BACKEND_PORT}" --log-dir "${BACKEND_APP_LOG_DIR}"
  ) >"${BACKEND_LOG}" 2>&1 &
  BACKEND_PID="$!"
}

start_frontend() {
  : >"${FRONTEND_LOG}"
  log "frontend log: ${FRONTEND_LOG}"
  (
    cd "${ROOT_DIR}/web"
    export VITE_REACT_APP_SERVER_URL="${VITE_REACT_APP_SERVER_URL:-http://localhost:${BACKEND_PORT}}"
    exec bun run dev -- --host "${FRONTEND_HOST}" --port "${FRONTEND_PORT}" --strict-port
  ) >"${FRONTEND_LOG}" 2>&1 &
  FRONTEND_PID="$!"
}

start_log_monitor() {
  tail -n +1 -F "${BACKEND_LOG}" "${FRONTEND_LOG}" &
  TAIL_PID="$!"
}

cleanup() {
  local code=$?
  trap - EXIT INT TERM

  if [[ -n "${TAIL_PID}" ]]; then
    kill "${TAIL_PID}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${BACKEND_PID}" ]]; then
    kill "${BACKEND_PID}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${FRONTEND_PID}" ]]; then
    kill "${FRONTEND_PID}" >/dev/null 2>&1 || true
  fi

  wait "${TAIL_PID:-}" >/dev/null 2>&1 || true
  wait "${BACKEND_PID:-}" >/dev/null 2>&1 || true
  wait "${FRONTEND_PID:-}" >/dev/null 2>&1 || true

  if [[ "${START_DEPS}" == "1" ]]; then
    log "dependency containers were left running: ${POSTGRES_CONTAINER}"
  fi

  exit "${code}"
}

wait_for_processes() {
  while :; do
    if ! kill -0 "${BACKEND_PID}" >/dev/null 2>&1; then
      wait "${BACKEND_PID}" || return $?
      return 0
    fi

    if ! kill -0 "${FRONTEND_PID}" >/dev/null 2>&1; then
      wait "${FRONTEND_PID}" || return $?
      return 0
    fi

    sleep 1
  done
}

main() {
  if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
  fi

  require_command go
  require_command bun
  require_command lsof

  stop_port_listener "${BACKEND_PORT}" "backend"
  stop_port_listener "${FRONTEND_PORT}" "frontend"

  mkdir -p "${LOG_ROOT}" "${BACKEND_APP_LOG_DIR}"
  ensure_embed_assets
  ensure_frontend_dependencies

  if [[ "${START_DEPS}" == "1" ]]; then
    start_deps_once
  fi

  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  start_backend
  start_frontend
  start_log_monitor

  log "frontend UI: http://localhost:${FRONTEND_PORT}"
  log "backend API: http://localhost:${BACKEND_PORT}"
  log "press Ctrl+C to stop backend/frontend"

  wait_for_processes
}

main "$@"
