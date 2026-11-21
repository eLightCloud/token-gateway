#!/usr/bin/env bash
set -euo pipefail

cd -- "$(dirname -- "$0")/../.."
container="token-gateway-log-bench-$$"
container_id=''
cleanup() { if [ -n "$container_id" ]; then docker stop "$container_id" >/dev/null 2>&1 || true; fi; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

container_options=()
if [ -n "${LOG_BENCH_CPUSET:-}" ]; then container_options+=(--cpuset-cpus "$LOG_BENCH_CPUSET"); fi
if [ "${LOG_BENCH_DIAGNOSTIC_TMPFS:-false}" = true ]; then
  if [ "${1:-}" != --full-chain ]; then
    echo 'The in-memory PostgreSQL diagnostic requires --full-chain.' >&2
    exit 2
  fi
  container_options+=(--tmpfs '/var/lib/postgresql:rw,size=1536m')
fi
container_id="$(docker run --rm -d --name "$container" --cpus=1 --memory=2g --shm-size=256m \
  ${container_options[@]+"${container_options[@]}"} \
  -p 127.0.0.1::5432 -e POSTGRES_HOST_AUTH_METHOD=trust postgres:18.4-alpine \
  -c cluster_name=token-gateway-log-bench -c track_io_timing=on -c track_wal_io_timing=on)"
for _ in {1..30}; do
  if docker exec "$container" pg_isready -h 127.0.0.1 -q; then break; fi
  sleep 1
done
docker exec "$container" pg_isready -h 127.0.0.1 -q
host_port="$(docker port "$container" 5432)"
fixture=log_read_bench_fixture
rows="${LOG_BENCH_ROWS:-350000}"
writes="${LOG_BENCH_WRITES:-1200}"
reads="${LOG_BENCH_READS:-20}"
export LOG_SQL_DSN='' PERSONAL_LOG_CURSOR_ENABLED=true LOG_READ_FLEET_BUDGET_KEY=''
export LOG_READ_MAX_CONCURRENT="${LOG_READ_MAX_CONCURRENT:-2}"
export LOG_READ_MAX_HEAVY="${LOG_READ_MAX_HEAVY:-1}"
export LOG_READ_STARTS_PER_SECOND="${LOG_READ_STARTS_PER_SECOND:-5}"
export SQL_MAX_OPEN_CONNS="${SQL_MAX_OPEN_CONNS:-16}"
export SQL_MAX_IDLE_CONNS="${SQL_MAX_IDLE_CONNS:-8}"

cpu_counters() {
  docker exec "$container" sh -c 'if [ -r /sys/fs/cgroup/cpu.stat ]; then cat /sys/fs/cgroup/cpu.stat; fi' |
    awk -v label="$1" '$1 == "nr_throttled" || $1 == "throttled_usec" { print "CPU", label, $0 }'
}

if [ "${1:-}" = --full-chain ]; then
  shift
  if [ "$#" -ne 0 ]; then
    echo 'Configure full-chain rounds through LOG_BENCH_* environment variables.' >&2
    exit 2
  fi
  # shellcheck source=scripts/log-read-benchmark/full-chain.sh
  source scripts/log-read-benchmark/full-chain.sh
fi

docker exec "$container" psql -U postgres -Atc 'SELECT version()'
docker exec "$container" createdb -U postgres "$fixture"
export SQL_DSN="postgres://postgres@${host_port}/${fixture}?sslmode=disable&application_name=log-read-bench"
go run ./scripts/log-read-benchmark -seed "$rows"

if [ "$#" -eq 0 ]; then set -- pure mixed mixed pure; fi
round=0
for mode in "$@"; do
  round=$((round+1))
  database="log_read_bench_${round}"
  docker exec "$container" createdb -U postgres -T "$fixture" "$database"
  docker exec "$container" psql -U postgres -d "$database" -qc CHECKPOINT
  export SQL_DSN="postgres://postgres@${host_port}/${database}?sslmode=disable&application_name=log-read-bench"
  printf 'ROUND %s mode=%s rows=%s writes=%s reads_per_second=%s admission_starts_per_second=%s\n' \
    "$round" "$mode" "$rows" "$writes" "$reads" "$LOG_READ_STARTS_PER_SECOND"
  cpu_counters before
  go run ./scripts/log-read-benchmark -writes "$writes" -reads "$reads" -mode "$mode"
  cpu_counters after
  docker exec "$container" dropdb -U postgres "$database"
done
