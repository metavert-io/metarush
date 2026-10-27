#!/usr/bin/env bash
# Starts (idempotently) a local single-node MongoDB replica set for tests and
# offline development: 127.0.0.1:27018, data in .dev/mongo. Replica set mode is
# required for change streams (live dashboard updates) and transactions.
# Usage: scripts/dev-mongo.sh [start|stop|status|uri]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT="${METARUSH_DEV_MONGO_PORT:-27018}"
DATA="$ROOT/.dev/mongo"
MONGOD="$(command -v mongod || true)"
[ -z "$MONGOD" ] && [ -x "$HOME/.local/mongodb/bin/mongod" ] && MONGOD="$HOME/.local/mongodb/bin/mongod"
MONGOSH="$(command -v mongosh || true)"
URI="mongodb://127.0.0.1:${PORT}/?replicaSet=rs0&directConnection=true"

running() { nc -z 127.0.0.1 "$PORT" >/dev/null 2>&1; }

case "${1:-start}" in
  uri) echo "$URI" ;;
  status) running && echo "running on $PORT" || { echo "not running"; exit 1; } ;;
  stop)
    # Clean shutdown only (SIGTERM, then wait); never SIGKILL — an unclean stop means a long recovery next start.
    if [ -f "$DATA/mongod.pid" ]; then
      pid="$(cat "$DATA/mongod.pid")"; kill "$pid" 2>/dev/null || true
      for _ in $(seq 1 120); do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
    fi
    echo "stopped" ;;
  start)
    if running; then echo "$URI"; exit 0; fi
    # A mongod that is still recovering after an unclean stop does not accept connections yet; never start a
    # second one on the same data (and never kill it): wait for it instead.
    if [ -f "$DATA/mongod.pid" ] && kill -0 "$(cat "$DATA/mongod.pid")" 2>/dev/null; then
      echo "mongod (pid $(cat "$DATA/mongod.pid")) is starting up; waiting…" >&2
      for _ in $(seq 1 240); do running && { echo "$URI"; exit 0; }; sleep 0.5; done
      echo "mongod is still not accepting connections after 2 minutes; see $DATA/mongod.log" >&2; exit 1
    fi
    [ -z "$MONGOD" ] && { echo "mongod not found — run ./bootstrap.sh or install MongoDB, or point METARUSH_MONGODB_URI at an existing server" >&2; exit 1; }
    mkdir -p "$DATA"
    # macOS starts many processes with a 256-file soft limit; many test databases need far more
    # (mongod aborts when it cannot open its diagnostics file).
    # Parallel test runs create and drop thousands of collections, and WiredTiger keeps idle files open: allow
    # many handles, close idle ones quickly, and turn off diagnostic capture (it aborts mongod when it cannot open
    # its own file, which is pointless for a throwaway test server).
    ulimit -n 200000 2>/dev/null || ulimit -n 65536 2>/dev/null || ulimit -n "$(ulimit -Hn)" 2>/dev/null || true
    # Test data is small: cap the cache (the default is half of RAM).
    "$MONGOD" --dbpath "$DATA" --bind_ip 127.0.0.1 --port "$PORT" --replSet rs0 --wiredTigerCacheSizeGB 2 \
      --wiredTigerEngineConfigString "file_manager=(close_idle_time=30,close_scan_interval=10,close_handle_minimum=500)" \
      --setParameter diagnosticDataCollectionEnabled=false \
      --fork --logpath "$DATA/mongod.log" --pidfilepath "$DATA/mongod.pid" >/dev/null
    for _ in $(seq 1 240); do running && break; sleep 0.5; done
    if [ -n "$MONGOSH" ]; then
      "$MONGOSH" --quiet "mongodb://127.0.0.1:${PORT}/?directConnection=true" --eval \
        "try { rs.status().ok } catch (e) { rs.initiate({_id:'rs0', members:[{_id:0, host:'127.0.0.1:${PORT}'}]}).ok }" >/dev/null
      for _ in $(seq 1 40); do
        "$MONGOSH" --quiet "$URI" --eval 'db.hello().isWritablePrimary' 2>/dev/null | grep -q true && break; sleep 0.25
      done
    fi
    echo "$URI" ;;
  *) echo "usage: $0 [start|stop|status|uri]" >&2; exit 2 ;;
esac
