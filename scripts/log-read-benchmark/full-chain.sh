# shellcheck shell=bash
# shellcheck disable=SC2154
# Loaded by run.sh after it creates and verifies the private PostgreSQL cluster.
work="$(mktemp -d "${TMPDIR:-/tmp}/token-gateway-log-chain.XXXXXX")"
work="$(cd "$work" && pwd -P)"
echo "RESULT_DIR $work"
vm_client="${LOG_BENCH_VM_CLIENT:-false}"
if [ "$vm_client" = true ]; then
  # Keep the gateway outside the database's CPU quota while eliminating the
  # desktop host-to-VM port forward from every SQL round trip.
  CGO_ENABLED=0 GOOS=linux GOARCH="$(docker info --format '{{.Architecture}}' | sed 's/^aarch64$/arm64/;s/^x86_64$/amd64/')" \
    go build -o "$work/benchmark" ./scripts/log-read-benchmark
  host_port=127.0.0.1:5432
else
  go build -o "$work/benchmark" ./scripts/log-read-benchmark
fi

writes="${LOG_BENCH_WRITES:-1200}"
rates="${LOG_BENCH_RATES:-50 25 10}"
baseline_gate="${LOG_BENCH_BASELINE_SPREAD_PERCENT:-20}"
release_gate=5
diagnostic_tmpfs="${LOG_BENCH_DIAGNOSTIC_TMPFS:-false}"
faults_only="${LOG_BENCH_FAULTS_ONLY:-false}"
if [ "$faults_only" = true ]; then rows=0; fi
export LOG_ALLOW_DELETION=false
export PERSONAL_LOG_CURSOR_ENABLED=false
export SQL_DSN LOG_SQL_DSN

fixture_dsn() {
  printf 'postgres://postgres@%s/%s?sslmode=disable&application_name=log-read-bench' "$host_port" "$1"
}

clone_fixture() {
  docker exec "$container" createdb -U postgres -T "$fixture" "$1" &&
    docker exec "$container" psql -U postgres -d "$1" -qc CHECKPOINT
}

chain_run() {
  local name="$1"
  shift
  local result=0
  if [ "$vm_client" = true ]; then
    docker run --rm --network "container:$container" --entrypoint /bench/benchmark \
      --mount "type=bind,src=$work,dst=/bench,readonly" \
      -e SQL_DSN -e LOG_SQL_DSN -e LOG_ALLOW_DELETION -e PERSONAL_LOG_CURSOR_ENABLED \
      -e SQL_MAX_OPEN_CONNS -e SQL_MAX_IDLE_CONNS -e LOG_READ_MAX_CONCURRENT \
      -e LOG_READ_MAX_HEAVY -e LOG_READ_STARTS_PER_SECOND -e LOG_READ_FLEET_BUDGET_KEY \
      postgres:18.4-alpine "$@" >"$work/$name.log" 2>&1 || result=$?
  else
    "$work/benchmark" "$@" >"$work/$name.log" 2>&1 || result=$?
  fi
  rg '^CHAIN_|^RESERVATION ' "$work/$name.log" || true
  if [ "$result" -ne 0 ]; then
    awk '!/^CHAIN_|^RESERVATION /' "$work/$name.log" | tail -25 >&2
  fi
  return "$result"
}

load_round() {
  local rate="$1" mode="$2" name="$3" database="log_read_bench_$3"
  local variant="${4:-standard}" fixture_channels=1 async_commit=false channel_lock=false
  local SQL_MAX_OPEN_CONNS="$SQL_MAX_OPEN_CONNS" SQL_MAX_IDLE_CONNS="$SQL_MAX_IDLE_CONNS"
  export SQL_MAX_OPEN_CONNS SQL_MAX_IDLE_CONNS
  clone_fixture "$database" || return
  case "$variant" in
    pool6) SQL_MAX_OPEN_CONNS=6; SQL_MAX_IDLE_CONNS=6 ;;
    channels8) fixture_channels=8 ;;
    channel_lock) channel_lock=true ;;
    async)
      async_commit=true
      docker exec "$container" psql -v ON_ERROR_STOP=1 -U postgres -qc "ALTER DATABASE $database SET synchronous_commit=off" || return ;;
  esac
  SQL_DSN="$(fixture_dsn "$database")"
  case "$mode" in
    mixed|page) export PERSONAL_LOG_CURSOR_ENABLED=true ;;
    *) export PERSONAL_LOG_CURSOR_ENABLED=false ;;
  esac
  echo "ROUND $name rate=$rate mode=$mode variant=$variant rows=$rows writes=$writes reads=$reads"
  cpu_counters before
  local result=0
  chain_run "$name" -chain load -rate "$rate" -mode "$mode" -writes "$writes" -reads "$reads" \
    -keyed="${LOG_BENCH_KEYED_WRITES:-false}" -fixture-channels="$fixture_channels" \
    -diagnostic-async-commit="$async_commit" -diagnostic-channel-lock="$channel_lock" || result=$?
  cpu_counters after
  docker exec "$container" dropdb -U postgres "$database" || return
  return "$result"
}

docker exec "$container" psql -U postgres -Atc 'SELECT version()'
docker exec "$container" createdb -U postgres "$fixture"
SQL_DSN="$(fixture_dsn "$fixture")"
chain_run seed -chain seed -seed "$rows"

if [ "${LOG_BENCH_WRITE_DIAGNOSTICS:-false}" != false ]; then
  # Interleave the control and one-variable changes. This path never runs
  # queries or contributes to release gates, including its WAL negative control.
  sample=0
  case "$LOG_BENCH_WRITE_DIAGNOSTICS" in
    true) variants='standard pool6 channels8 async async channels8 pool6 standard' ;;
    lock) variants='standard channel_lock standard' ;;
    *) echo 'Unknown write diagnostics profile.' >&2; exit 2 ;;
  esac
  for variant in $variants; do
    sample=$((sample+1))
    load_round 50 pure "write_${sample}_$variant" "$variant"
  done
  exit 0
fi

if [ "$diagnostic_tmpfs" != true ]; then
  name=log_read_bench_client_retry
  clone_fixture "$name"
  SQL_DSN="$(fixture_dsn "$name")"
  chain_run client-retry -chain client-retry
  chain_run client-replay -chain client-replay

  for phase in fault journal-gap; do
    name="log_read_bench_${phase//-/_}"
    clone_fixture "$name"
    docker exec "$container" createdb -U postgres -T "$fixture" "${name}_logs"
    SQL_DSN="$(fixture_dsn "$name")"
    LOG_SQL_DSN="$(fixture_dsn "${name}_logs")"
    chain_run "$phase" -chain "$phase"
    if [ "$phase" = fault ]; then chain_run recover -chain recover; fi
  done
  LOG_SQL_DSN=''
fi
if [ "$faults_only" = true ]; then exit 0; fi

status=0
stable_rate=0
for rate in $rates; do
  sample_ok=true
  for sample in 1 2 3; do
    if ! load_round "$rate" pure "pure_${rate}_$sample"; then
      sample_ok=false
      status=1
    fi
  done
  if ! awk -v gate="$baseline_gate" '
    /CHAIN_ACCOUNTING/ { accounted++ }
    /CHAIN_LOAD/ {
      for (i=1; i<=NF; i++) if ($i ~ /^p99_ns=/) {
        split($i, pair, "="); sample=pair[2]+0
        if (!n || sample<low) low=sample
        if (!n || sample>high) high=sample
        n++
      }
    }
    END {
      if (n != 3 || accounted != 3 || low <= 0) exit 1
      spread=(high/low-1)*100
    printf "CHAIN_BASELINE rounds=%d p99_min_ns=%d p99_max_ns=%d spread_pct=%.2f gate_pct=%g\n", n,low,high,spread,gate
      if (spread > gate) exit 1
    }
  ' "$work"/pure_"${rate}"_*.log; then
    continue
  fi
  if [ "$sample_ok" = true ]; then
    stable_rate="$rate"
    break
  fi
done

if [ "$stable_rate" -eq 50 ] && [ "$status" -eq 0 ] && [ "$diagnostic_tmpfs" != true ]; then
  load_round "$stable_rate" legacy "legacy_$stable_rate" || status=1
  load_round "$stable_rate" page "cursor_$stable_rate" || status=1
  for sample in 1 2; do
    load_round "$stable_rate" mixed "mixed_${stable_rate}_$sample" || status=1
  done
  load_round "$stable_rate" pure "verify_pure_$stable_rate" || status=1
  if ! awk -v gate="$release_gate" '
    /CHAIN_ACCOUNTING/ { accounted++ }
    /CHAIN_LOAD/ {
      for (i=1; i<=NF; i++) if ($i ~ /^p99_ns=/) {
        split($i, pair, "="); sample=pair[2]+0
        if ($2=="mode=pure") {
          if (!pure || sample<low) low=sample
          if (!pure || sample>high) high=sample
          pure++
        } else {
          modes++
          if (sample>read_high) read_high=sample
        }
      }
    }
    END {
      if (pure!=4 || modes!=4 || accounted!=8 || low<=0) exit 1
      spread=(high/low-1)*100
      regression=(read_high/low-1)*100
      printf "CHAIN_COMPARISON pure_rounds=%d query_rounds=%d pure_spread_pct=%.2f worst_query_vs_fastest_pure_pct=%.2f gate_pct=%g\n",pure,modes,spread,regression,gate
      if (spread>gate || regression>gate) exit 1
    }
  ' "$work"/pure_"${stable_rate}"_*.log "$work"/{legacy,cursor,verify_pure}_"${stable_rate}".log "$work"/mixed_"${stable_rate}"_*.log; then
    status=1
  fi
else
  echo "CHAIN_BASELINE target_rate=50 stable_rate=$stable_rate target_verified=false comparison_skipped=true"
  status=1
fi

export PERSONAL_LOG_CURSOR_ENABLED=false
if [ "$diagnostic_tmpfs" = true ]; then
  echo 'CHAIN_STORAGE diagnostic_tmpfs=true durability_verified=false release_eligible=false'
fi
exit "$status"
