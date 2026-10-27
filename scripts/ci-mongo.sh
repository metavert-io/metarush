#!/usr/bin/env bash
# scripts/ci-mongo.sh — the test MongoDB in CI (D194): MongoDB in Docker as a one-member replica set
# on 127.0.0.1:27018, set up like scripts/dev-mongo.sh's. A standalone server has no change streams
# (live updates); many test databases at once need many open files, idle ones closed quickly, a capped
# cache, and no diagnostic capture (mongod aborts when it can't open that file). Without these, the
# Go jobs' mongod went away part-way through a run.
#
# MongoDB 8 can't run on Linux kernels 6.19 through 7.0.13: 8.0.21 and later refuse to start, older
# 8.0 crashes within a minute (its memory allocator and the kernel's rseq change; MongoDB's
# SERVER-121912 and SERVER-125742). 7.0 runs there. GitHub's newer ubuntu-24.04 images have kernel
# 7.0.0, so on those the test server is 7.0, and 8.0 (the dev one's and MetaRush's own) everywhere else.
#
# It runs on the host's network: every connection goes straight to mongod, as on a Mac, not through
# Docker's port-forwarding process (a test's new connection once waited 8 s for an answer while the
# test's open ones were answered).
#
#   scripts/ci-mongo.sh start    # start it and wait until it takes writes; says why when it can't
#   scripts/ci-mongo.sh watch    # in the background: how long a new connection's first answer takes, the load,
#                                # mongod's CPU and memory, TCP connections, every 2 s ($DIAG/watch.log)
#   scripts/ci-mongo.sh report   # after a failure: how it ended, what it said, its slow moments; its whole log in $DIAG
#   METARUSH_CI_MONGO_IMAGE=mongo:7.0 scripts/ci-mongo.sh start   # a version of your choosing
set -euo pipefail
NAME="${METARUSH_CI_MONGO_NAME:-mongo}"
PORT="${METARUSH_DEV_MONGO_PORT:-27018}"
DIAG="${METARUSH_CI_MONGO_DIAG:-${RUNNER_TEMP:-/tmp}/mongo}" # the whole log and the watch's samples (CI keeps them after a failure)

# mongo8_runs_here: not a kernel from 6.19 through 7.0.13.
mongo8_runs_here() {
  local major minor patch v
  IFS=.- read -r major minor patch _ <<<"$(uname -r)"
  v=$((10#${major:-0} * 1000000 + 10#${minor:-0} * 1000 + 10#${patch:-0}))
  [ "$v" -lt 6019000 ] || [ "$v" -gt 7000013 ]
}
WHY=""
if [ -n "${METARUSH_CI_MONGO_IMAGE:-}" ]; then
  IMAGE="$METARUSH_CI_MONGO_IMAGE"
elif mongo8_runs_here; then
  IMAGE="mongo:8.0"
else
  IMAGE="mongo:7.0"
  WHY=" (MongoDB 8 can't run on this computer's Linux kernel, $(uname -r))"
fi

running() { [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" = true ]; }
# ask <expression>: what mongosh in the container prints for it (its last line), nothing when it can't.
ask() { docker exec "$NAME" mongosh --port "$PORT" --quiet --eval "$1" 2>/dev/null | tail -n 1 || true; }

# watch: samples until the job ends (both loops run detached). A new connection each time, as a test's
# app opens one: its first answer is what timed out.
watch() {
  mkdir -p "$DIAG"
  nohup docker exec "$NAME" mongosh --port "$PORT" --quiet --eval "
    const uri = 'mongodb://127.0.0.1:$PORT/?directConnection=true&serverSelectionTimeoutMS=120000&connectTimeoutMS=120000';
    for (let i = 0; ; i++) {
      const t0 = Date.now();
      let said = '';
      try {
        const c = new Mongo(uri);
        const a = c.getDB('admin');
        a.runCommand({ hello: 1 });
        const ms = Date.now() - t0;
        if (i % 15 === 0) {
          const st = a.runCommand({ serverStatus: 1, repl: 0, metrics: 0, locks: 0 });
          const wt = st.wiredTiger || {};
          said = ' connections ' + st.connections.current + ', cache ' + Math.round(((wt.cache || {})['bytes currently in the cache'] || 0) / 1048576) + ' MB, checkpoint running ' + ((wt.transaction || {})['transaction checkpoint currently running'] || 0) + ', files open ' + ((wt.connection || {})['files currently open'] || 0);
        }
        c.close();
        print(new Date().toISOString() + ' new connection ' + ms + ' ms' + said);
      } catch (e) {
        print(new Date().toISOString() + ' new connection FAILED after ' + (Date.now() - t0) + ' ms: ' + String(e).slice(0, 200));
      }
      sleep(2000);
    }" >"$DIAG/watch.log" 2>&1 &
  local pid
  pid="$(docker inspect -f '{{.State.Pid}}' "$NAME")"
  nohup bash -c '
    pid="$1"; prev=""; prevt=""
    while sleep 2; do
      read -r -a st <"/proc/$pid/stat" 2>/dev/null || break
      cpu=$((st[13] + st[14])); now=$(date +%s%N)
      pct="-"; [ -n "$prev" ] && pct=$(( (cpu - prev) * 1000000000 / (now - prevt) ))
      prev=$cpu; prevt=$now
      rss=$(awk "/VmRSS/ {print int(\$2/1024)}" "/proc/$pid/status")
      tcp=$(ss -tan state time-wait 2>/dev/null | wc -l)
      est=$(ss -tan state established "( sport = :$2 )" 2>/dev/null | wc -l)
      echo "$(date -u +%FT%TZ) load $(cut -d" " -f1-3 /proc/loadavg), mongod cpu ${pct}% rss ${rss} MB, connections to it $est, time-wait $tcp"
    done' _ "$pid" "$PORT" >"$DIAG/host.log" 2>&1 &
  echo "watching MongoDB: $DIAG/watch.log, $DIAG/host.log"
}

report() {
  echo "== MongoDB's container"
  docker ps -a --filter "name=^${NAME}\$" --format '{{.Names}} {{.Image}} {{.Status}}' || true
  docker inspect -f 'running={{.State.Running}} status={{.State.Status}} exit={{.State.ExitCode}} oom-killed={{.State.OOMKilled}} error={{.State.Error}} started={{.State.StartedAt}} finished={{.State.FinishedAt}}' "$NAME" 2>&1 || true
  if running; then
    echo "open files: $(docker exec "$NAME" sh -c 'echo "$(ulimit -Sn) soft, $(ulimit -Hn) hard"' 2>&1)"
  fi
  echo "== what mongod warned about, and its errors"
  docker logs "$NAME" 2>&1 | grep -E '"s":"(W|E|F)"' | tail -n 40 || true
  echo "== its last lines"
  docker logs --tail 15 "$NAME" 2>&1 || true
  mkdir -p "$DIAG"
  docker logs "$NAME" >"$DIAG/mongod.log" 2>&1 || true
  echo "== operations that took a second or more (all of them: $DIAG/mongod.log)"
  grep -E '"durationMillis":[0-9]{4,}' "$DIAG/mongod.log" | tail -n 30 || true
  echo "== checkpoints and storage it called slow"
  grep -i -E 'checkpoint has|running for [0-9]|"msg":"Slow|stalled|took too long' "$DIAG/mongod.log" | grep -v -E '"durationMillis"' | tail -n 20 || true
  if [ -s "$DIAG/watch.log" ]; then
    echo "== a new connection's first answer: slowest, and every one over a second"
    grep -o 'new connection [0-9]* ms' "$DIAG/watch.log" | awk '{print $3}' | sort -n | tail -n 1 | sed 's/^/slowest: /;s/$/ ms/'
    grep -E 'new connection ([0-9]{4,} ms|FAILED)' "$DIAG/watch.log" | tail -n 30 || true
  fi
  if [ -s "$DIAG/host.log" ]; then
    echo "== the computer when it was slowest (load, mongod's CPU and memory, connections)"
    sort -t' ' -k3,3 -rn "$DIAG/host.log" | head -n 5 || true
  fi
  echo "== memory and disk"
  free -m || true
  df -h / /tmp 2>/dev/null || true
  echo "== the kernel's out-of-memory killer"
  sudo -n dmesg 2>/dev/null | grep -i -E 'out of memory|killed process|oom-kill' | tail -n 10 || echo "(nothing found, or dmesg can't be read)"
}

case "${1:-}" in
  start)
    docker run -d --name "$NAME" --network host --ulimit nofile=200000:200000 "$IMAGE" \
      --replSet rs0 --bind_ip 127.0.0.1 --port "$PORT" --wiredTigerCacheSizeGB 2 \
      --wiredTigerEngineConfigString "file_manager=(close_idle_time=30,close_scan_interval=10,close_handle_minimum=500)" \
      --setParameter diagnosticDataCollectionEnabled=false >/dev/null
    up=""
    for _ in $(seq 1 120); do
      running || break
      [ "$(ask 'db.runCommand({ping:1}).ok')" = 1 ] && { up=1; break; }
      sleep 1
    done
    if [ -z "$up" ]; then
      echo "MongoDB didn't start (it stopped, or didn't answer in two minutes):" >&2
      report
      exit 1
    fi
    ask "rs.initiate({_id:'rs0',members:[{_id:0,host:'127.0.0.1:$PORT'}]}).ok" >/dev/null
    for _ in $(seq 1 120); do
      if [ "$(ask 'db.hello().isWritablePrimary')" = true ]; then
        echo "MongoDB $(ask 'db.version()')$WHY takes writes on 127.0.0.1:$PORT (replica set rs0); open files: $(docker exec "$NAME" sh -c 'echo "$(ulimit -Sn) soft, $(ulimit -Hn) hard"')"
        exit 0
      fi
      running || break
      sleep 1
    done
    echo "MongoDB didn't become the replica set's primary:" >&2
    report
    exit 1
    ;;
  watch) watch ;;
  report) report ;;
  *)
    echo "usage: $0 start|watch|report" >&2
    exit 2
    ;;
esac
