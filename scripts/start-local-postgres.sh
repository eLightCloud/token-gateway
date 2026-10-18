#!/usr/bin/env bash

set -Eeuo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose_file="${project_root}/docker-compose.dev.yml"

docker compose --project-directory "${project_root}" -f "${compose_file}" up -d postgres

for _ in {1..60}; do
  if docker compose --project-directory "${project_root}" -f "${compose_file}" exec -T postgres \
    sh -c 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >/dev/null 2>&1; then
    exit 0
  fi
  sleep 1
done

printf '%s\n' 'Local Compose PostgreSQL did not become ready' >&2
exit 1
