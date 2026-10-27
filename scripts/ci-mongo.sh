#!/usr/bin/env bash
# scripts/ci-mongo.sh — the test MongoDB in CI (D194): mongo:8.0 in Docker as a one-member replica set
# on 127.0.0.1:27018, set up like scripts/dev-mongo.sh's. A standalone server has no change streams
# (live updates); many test databases at once need many open files, idle ones closed quickly, a capped
# cache, and no diagnostic capture (mongod aborts when it can't open that file). Without these, the
# Go jobs' mongod went away part-way through a run.
#
#   scripts/ci-mongo.sh start    # start it and wait until it takes writes; says why when it can't
#   scripts/ci-mongo.sh report   # after a failure: whether it still runs, how it ended, what it said, memory and disk
set -euo pipefail
NAME="${METARUSH_CI_MONGO_NAME:-mongo}"
IMAGE="${METARUSH_CI_MONGO_IMAGE:-mongo:8.0}"
PORT="${METARUSH_DEV_MONGO_PORT:-27018}"

running() { [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" = true ]; }
# ask <expression>: what mongosh in the container prints for it (its last line), nothing when it can't.
ask() { docker exec "$NAME" mongosh --quiet --eval "$1" 2>/dev/null | tail -n 1 || true; }

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
  echo "== memory and disk"
  free -m || true
  df -h / /tmp 2>/dev/null || true
  echo "== the kernel's out-of-memory killer"
  sudo -n dmesg 2>/dev/null | grep -i -E 'out of memory|killed process|oom-kill' | tail -n 10 || echo "(nothing found, or dmesg can't be read)"
}

case "${1:-}" in
  start)
    docker run -d --name "$NAME" -p "127.0.0.1:$PORT:27017" --ulimit nofile=200000:200000 "$IMAGE" \
      --replSet rs0 --bind_ip_all --wiredTigerCacheSizeGB 2 \
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
    ask "rs.initiate({_id:'rs0',members:[{_id:0,host:'127.0.0.1:27017'}]}).ok" >/dev/null
    for _ in $(seq 1 120); do
      if [ "$(ask 'db.hello().isWritablePrimary')" = true ]; then
        echo "MongoDB takes writes on 127.0.0.1:$PORT (replica set rs0); open files: $(docker exec "$NAME" sh -c 'echo "$(ulimit -Sn) soft, $(ulimit -Hn) hard"')"
        exit 0
      fi
      running || break
      sleep 1
    done
    echo "MongoDB didn't become the replica set's primary:" >&2
    report
    exit 1
    ;;
  report) report ;;
  *)
    echo "usage: $0 start|report" >&2
    exit 2
    ;;
esac
