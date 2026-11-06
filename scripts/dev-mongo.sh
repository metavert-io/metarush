#!/usr/bin/env bash
# Starts (idempotently) a local single-node MongoDB replica set for tests and
# offline development: 127.0.0.1:27018, data in .dev/mongo. Replica set mode is
# required for change streams (live dashboard updates) and transactions.
#
# --memory (or METARUSH_DEV_MONGO_MEMORY=1), macOS only: the data lives on a RAM disk instead, for test runs. On a
# Mac every collection and index a test database makes or drops waits for a full flush of the disk, so a test
# database takes seconds to set up on disk and a fraction of one in memory (internal/ops: 2.6 s → 0.23 s each, the
# package 3.3× faster). The RAM disk (/Volumes/metarush-test-mongo-<port>, METARUSH_DEV_MONGO_MEMORY_GB, default 4:
# at most that much memory, all of it given back on stop) is this port's server while it is attached; `stop` ejects
# it and everything on it is gone. Never keep a dev installation's database there (start.sh refuses, and so does
# --memory while the dev daemon runs).
#
# The log grows with every collection and index a test database makes and drops (~0.3 MB a test database; one
# reached 50 GB): `start` has mongod begin a new log once it passes METARUSH_DEV_MONGO_LOG_MB (256) and keeps one
# older log (none past four times that).
# Usage: scripts/dev-mongo.sh [start [--memory]|stop|status|uri]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT="${METARUSH_DEV_MONGO_PORT:-27018}"
DISK_DATA="$ROOT/.dev/mongo"
RAM_MOUNT="/Volumes/metarush-test-mongo-$PORT"
RAM_MARK="$RAM_MOUNT/.metarush-ram-disk"
# Shared by every checkout of this computer's user: one start or stop of the port at a time (the file stays).
LOCK="/tmp/metarush-dev-mongo-$(id -u)-$PORT.lock"
LOG_MB="${METARUSH_DEV_MONGO_LOG_MB:-256}"
DEV_DAEMON_PORT=4841 # start.sh's
MONGOD="$(command -v mongod || true)"
[ -z "$MONGOD" ] && [ -x "$HOME/.local/mongodb/bin/mongod" ] && MONGOD="$HOME/.local/mongodb/bin/mongod"
MONGOSH="$(command -v mongosh || true)"
URI="mongodb://127.0.0.1:${PORT}/?replicaSet=rs0&directConnection=true"

running() { nc -z 127.0.0.1 "$PORT" >/dev/null 2>&1; }
# This port's RAM disk is attached: while it is, it holds this port's server.
on_ram() { [ -f "$RAM_MARK" ]; }
data_dir() { if on_ram; then echo "$RAM_MOUNT/mongo"; else echo "$DISK_DATA"; fi; }
# The data folder's mongod runs (its pid file names a mongod, not a process that got the number since).
alive() {
  local pid
  [ -f "$1/mongod.pid" ] || return 1
  pid="$(cat "$1/mongod.pid")"
  kill -0 "$pid" 2>/dev/null || return 1
  case "$(ps -p "$pid" -o comm= 2>/dev/null)" in *mongod*) return 0 ;; esac
  return 1
}
ram_used() { df -P "$RAM_MOUNT" | awk 'NR == 2 { sub("%", "", $5); print $5 }'; }

# A lock the kernel holds on descriptor 9 until this script ends, however it ends (nothing is left behind); mongod
# gets the descriptor closed. flock (Linux) and lockf (macOS) both lock a descriptor; with neither, no lock.
lock() {
  local got=1
  exec 9>>"$LOCK"
  if command -v flock >/dev/null 2>&1; then
    if flock -w 120 9; then got=0; fi
  elif command -v lockf >/dev/null 2>&1; then
    if lockf -s -t 120 9; then got=0; fi
  else
    got=0
  fi
  [ "$got" = 0 ] && return 0
  echo "another scripts/dev-mongo.sh has been starting or stopping port $PORT for 2 minutes ($LOCK)" >&2; exit 1
}

# The disk image mounted at exactly RAM_MOUNT, only when it is a RAM disk: its /dev/diskN. Ejecting goes by this,
# never by a remembered number (macOS gives numbers out again). awk reads to the end (no broken pipe for hdiutil);
# older hdiutil pads its columns with spaces.
ram_device() {
  { hdiutil info 2>/dev/null || true; } | awk -F'\t' -v m="$RAM_MOUNT" '
    function trim(s) { sub(/ +$/, "", s); return s }
    /^image-path/ { ram = ($0 ~ /: ram:\/\//); dev = "" }
    trim($1) ~ /^\/dev\/disk[0-9]+$/ && dev == "" { dev = trim($1) }
    trim($NF) == m && ram && dev != "" && found == "" { found = dev }
    END { if (found != "") print found }'
}

# Disk image helpers may outlive this script: never with the lock (descriptor 9).
eject() { diskutil eject "$1" >/dev/null 2>&1 9>&- || hdiutil detach "$1" >/dev/null 2>&1 9>&-; }

make_ram_disk() {
  on_ram && return 0
  [ -e "$RAM_MOUNT" ] && { echo "$RAM_MOUNT exists but isn't this script's RAM disk: eject it or choose another METARUSH_DEV_MONGO_PORT" >&2; exit 1; }
  local gb="${METARUSH_DEV_MONGO_MEMORY_GB:-4}" dev
  case "$gb" in '' | *[!0-9]* | 0) echo "METARUSH_DEV_MONGO_MEMORY_GB must be a whole number of GB" >&2; exit 2 ;; esac
  gb=$((10#$gb)) # 08 is eight, not a bad octal number
  # 512-byte sectors. diskutil image is the newer way; older macOS has only hdiutil.
  dev="$(diskutil image attach --noMount "ram://$((gb * 2097152))" 2>/dev/null 9>&- ||
    hdiutil attach -nomount "ram://$((gb * 2097152))" 2>/dev/null 9>&- || true)"
  dev="$(echo "$dev" | awk '/^\/dev\/disk[0-9]+/ { print $1; exit }')"
  [ -n "$dev" ] || { echo "couldn't make a ${gb} GB RAM disk" >&2; exit 1; }
  # Mounted where it should be, and nothing else there: a volume of the same name puts it at "… 1".
  if ! diskutil erasevolume APFS "${RAM_MOUNT#/Volumes/}" "$dev" >/dev/null 9>&- || [ "$(ram_device)" != "$dev" ]; then
    eject "$dev" || true; echo "couldn't set up the RAM disk at $RAM_MOUNT" >&2; exit 1
  fi
  echo "a RAM disk made by scripts/dev-mongo.sh for the test MongoDB on port $PORT" > "$RAM_MARK"
}

# Only once its mongod has stopped: what was on it is gone.
drop_ram_disk() {
  local dev
  dev="$(ram_device)"
  [ -n "$dev" ] || { echo "$RAM_MOUNT isn't a RAM disk: left as it is" >&2; return 1; }
  eject "$dev" || { echo "the RAM disk $RAM_MOUNT is still attached (diskutil eject $dev)" >&2; return 1; }
}

# Older logs: the newest stays unless it is huge (the first rotation of a log that grew for weeks), the rest go.
prune_logs() {
  local f n=0 huge=$((LOG_MB * 4 * 1048576))
  { ls -td "$1"/mongod.log.* 2>/dev/null || true; } | while IFS= read -r f; do
    [ -f "$f" ] || continue
    n=$((n + 1))
    if [ "$n" -gt 1 ] || [ "$(wc -c < "$f")" -gt "$huge" ]; then rm -f -- "$f"; fi
  done
}

# A running server's log past LOG_MB: mongod renames it and starts a new one.
rotate_log() {
  local log="$1/mongod.log"
  [ -f "$log" ] && [ -n "$MONGOSH" ] || return 0
  [ "$(($(wc -c < "$log") / 1048576))" -ge "$LOG_MB" ] || return 0
  "$MONGOSH" --quiet "$URI" --eval 'db.adminCommand({ logRotate: 1 }).ok' >/dev/null 2>&1 || return 0
  prune_logs "$1"
}

memory=0
case "${METARUSH_DEV_MONGO_MEMORY:-}" in 1 | true | yes) memory=1 ;; esac
case "${1:-start} ${2:-}" in
  "start --memory") memory=1 ;;
  "start " | "stop " | "status " | "uri ") ;;
  *) echo "usage: $0 [start [--memory]|stop|status|uri]" >&2; exit 2 ;;
esac
case "$LOG_MB" in '' | *[!0-9]*) echo "METARUSH_DEV_MONGO_LOG_MB must be a whole number of MB" >&2; exit 2 ;; esac
LOG_MB=$((10#$LOG_MB))

case "${1:-start}" in
  uri) echo "$URI" ;;
  status)
    running || { echo "not running"; exit 1; }
    if on_ram && alive "$RAM_MOUNT/mongo"; then
      echo "running on $PORT, in memory ($(ram_used)% of the RAM disk used)"
    else
      echo "running on $PORT"
    fi ;;
  stop)
    lock
    DATA="$(data_dir)"
    # Clean shutdown only (SIGTERM, then wait); never SIGKILL — an unclean stop means a long recovery next start.
    if alive "$DATA"; then
      pid="$(cat "$DATA/mongod.pid")"; kill "$pid" 2>/dev/null || true
      for _ in $(seq 1 120); do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
    fi
    if on_ram; then
      if alive "$DATA"; then
        echo "mongod (pid $(cat "$DATA/mongod.pid")) is still stopping; the RAM disk stays until it has" >&2; exit 1
      fi
      drop_ram_disk
    fi
    echo "stopped" ;;
  start)
    if [ "$memory" = 1 ] && [ "$(uname -s)" != Darwin ]; then
      echo "--memory is for macOS (elsewhere the disk's flushes cost far less): start without it" >&2; exit 2
    fi
    if running; then
      DATA="$(data_dir)"
      if on_ram && alive "$DATA"; then
        [ "$(ram_used)" -lt 80 ] || echo "the in-memory test MongoDB is $(ram_used)% full: scripts/dev-mongo.sh stop && scripts/dev-mongo.sh start --memory starts it empty" >&2
      elif [ "$memory" = 1 ]; then
        echo "the test MongoDB on $PORT keeps its data on disk; to move it into memory: scripts/dev-mongo.sh stop && scripts/dev-mongo.sh start --memory" >&2
      fi
      alive "$DATA" && rotate_log "$DATA"
      echo "$URI"; exit 0
    fi
    lock
    # Another start may have finished while this one waited.
    running && { echo "$URI"; exit 0; }
    # A mongod that is still recovering after an unclean stop does not accept connections yet; never start a
    # second one on the same port (and never kill it): wait for it instead.
    DATA="$(data_dir)"
    if alive "$DATA"; then
      echo "mongod (pid $(cat "$DATA/mongod.pid")) is starting up; waiting…" >&2
      for _ in $(seq 1 240); do running && { echo "$URI"; exit 0; }; sleep 0.5; done
      echo "mongod is still not accepting connections after 2 minutes; see $DATA/mongod.log" >&2; exit 1
    fi
    [ -z "$MONGOD" ] && { echo "mongod not found — run ./bootstrap.sh or install MongoDB, or point METARUSH_MONGODB_URI at an existing server" >&2; exit 1; }
    if [ "$memory" = 1 ] && ! on_ram; then
      # A running dev installation would carry on with an empty database in memory, and lose what it writes.
      if [ "$PORT" = 27018 ] && nc -z 127.0.0.1 "$DEV_DAEMON_PORT" >/dev/null 2>&1; then
        echo "a development MetaRush (start.sh) runs on 127.0.0.1:$DEV_DAEMON_PORT and would keep its database in memory: stop it first" >&2; exit 1
      fi
      make_ram_disk
      DATA="$(data_dir)"
    fi
    mkdir -p "$DATA"
    # macOS starts many processes with a 256-file soft limit; many test databases need far more
    # (mongod aborts when it cannot open its diagnostics file).
    # Parallel test runs create and drop thousands of collections, and WiredTiger keeps idle files open: allow
    # many handles, close idle ones quickly, and turn off diagnostic capture (it aborts mongod when it cannot open
    # its own file, which is pointless for a throwaway test server).
    ulimit -n 200000 2>/dev/null || ulimit -n 65536 2>/dev/null || ulimit -n "$(ulimit -Hn)" 2>/dev/null || true
    # Test data is small: cap the cache (the default is half of RAM). Without --logappend mongod renames the old
    # log as it starts.
    "$MONGOD" --dbpath "$DATA" --bind_ip 127.0.0.1 --port "$PORT" --replSet rs0 --wiredTigerCacheSizeGB 2 \
      --wiredTigerEngineConfigString "file_manager=(close_idle_time=30,close_scan_interval=10,close_handle_minimum=500)" \
      --setParameter diagnosticDataCollectionEnabled=false \
      --fork --logpath "$DATA/mongod.log" --pidfilepath "$DATA/mongod.pid" >/dev/null 9>&-
    prune_logs "$DATA"
    for _ in $(seq 1 240); do running && break; sleep 0.5; done
    if [ -n "$MONGOSH" ]; then
      "$MONGOSH" --quiet "mongodb://127.0.0.1:${PORT}/?directConnection=true" --eval \
        "try { rs.status().ok } catch (e) { rs.initiate({_id:'rs0', members:[{_id:0, host:'127.0.0.1:${PORT}'}]}).ok }" >/dev/null
      for _ in $(seq 1 40); do
        "$MONGOSH" --quiet "$URI" --eval 'db.hello().isWritablePrimary' 2>/dev/null | grep -q true && break; sleep 0.25
      done
    fi
    echo "$URI" ;;
esac
