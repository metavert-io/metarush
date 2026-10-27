#!/usr/bin/env bash
# storage-e2e.sh — moving the media from the dashboard (D112) in a real browser,
# on two throwaway installations of one workspace ("Studio Mac" and "Edit
# laptop") that share a folder: the first moves the media to a new folder from
# Settings → Storage (checked first, confirmed in a styled dialog, shown in the
# activity bar, the dashboard starting again with the new storage), then the
# second is told it still uses the old storage and switches too. Nothing is
# ever deleted: the old folder keeps every file. Screenshots in STORAGE_SHOTS.
#
#   scripts/storage-e2e.sh                     # needs scripts/dev-mongo.sh running
#   KEEP=1 METARUSH_E2E_DIR=<empty folder> SKIP_BUILD=1 scripts/storage-e2e.sh
#   METARUSH_E2E_PORT=4926 (the second dashboard uses the next port)
#
# The throwaway database is dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT_A="${METARUSH_E2E_PORT:-4926}"; PORT_B=$((PORT_A + 1))
TMPBASE="${TMPDIR:-/tmp}"; TMPBASE="${TMPBASE%/}"  # macOS TMPDIR ends in "/": a doubled slash made the paths differ
BASE="${METARUSH_E2E_DIR:-$(mktemp -d "$TMPBASE/catchup-storage-XXXXXX")}"
mkdir -p "$BASE"
HA="$BASE/studio"; HB="$BASE/laptop"
OLD="$BASE/Shared media"; NEW="$BASE/New media"
DB="metarush-test-storage$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SA=""; SB=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: homes $HA and $HB, database $DB, dashboards http://127.0.0.1:$PORT_A (pid $SA) and http://127.0.0.1:$PORT_B (pid $SB)"
    return
  fi
  [ -n "$SA" ] && kill "$SA" 2>/dev/null || true
  [ -n "$SB" ] && kill "$SB" 2>/dev/null || true
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
A() { ./bin/metarush --home "$HA" "$@"; }
B() { ./bin/metarush --home "$HB" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
files() { find "$1" -type f 2>/dev/null | wc -l | tr -d ' '; }
[ -f tests/fixtures/gen/broll-c.mp4 ] || scripts/make-fixtures.sh >/dev/null
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "two machines of one workspace, sharing a folder"
METARUSH_MONGODB_URI="$URI" A init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Storage test" --media filesystem \
  --media-root "$OLD" --media-shared --port "$PORT_A" --installation-name "Studio Mac" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$HA/.env"; chmod 600 "$HA/.env"
WS=$(A workspace show --json | J 'd["data"]["workspace"]["id"]')
METARUSH_MONGODB_URI="$URI" B init --yes --test-db --secrets env --db-name "$DB" --join "$WS" --media filesystem \
  --media-root "$OLD" --media-shared --port "$PORT_B" --installation-name "Edit laptop" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$HB/.env"; chmod 600 "$HB/.env"

step "footage in the shared folder"
P=$(A project create "Moving day" --json | J 'd["data"]["project"]["id"]')
A ingest tests/fixtures/gen/broll-c.mp4 tests/fixtures/gen/music-bed.m4a --project "$P" --json | J '"added=%d" % d["data"]["ingest"]["ingested"]'
BEFORE=$(files "$OLD")
echo "files in the old folder: $BEFORE"
[ "$BEFORE" -ge 2 ] || { echo "no footage in the old folder"; exit 1; }

step "both dashboards (no workers: nothing else runs meanwhile)"
# The binary itself in the background (not the A/B functions: a function would run
# in a subshell whose PID is not the dashboard's, and the dashboard would outlive it).
METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$HA" serve --no-worker > "$BASE/studio.log" 2>&1 & SA=$!
METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$HB" serve --no-worker > "$BASE/laptop.log" 2>&1 & SB=$!
for port in "$PORT_A" "$PORT_B"; do
  for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$port/api/health" >/dev/null && break; sleep 0.25; done
done

step "the browser run"
SHOTS="${STORAGE_SHOTS:-$BASE/screenshots}"
METARUSH_DASHBOARD_A="http://127.0.0.1:$PORT_A" METARUSH_MINT_A="./bin/metarush --home $HA dashboard --print-link --no-open" \
METARUSH_DASHBOARD_B="http://127.0.0.1:$PORT_B" METARUSH_MINT_B="./bin/metarush --home $HB dashboard --print-link --no-open" \
STORAGE_NEW="$NEW" STORAGE_SHOTS="$SHOTS" node web/scripts/storage-e2e.mjs || {
  echo "--- studio dashboard log"; tail -40 "$BASE/studio.log"; echo "--- laptop dashboard log"; tail -40 "$BASE/laptop.log"
  for f in "$HA"/state/storage-move/dashboard/*.log "$HB"/state/storage-move/dashboard/*.log; do [ -f "$f" ] && { echo "--- $f"; cat "$f"; }; done
  exit 1
}

step "afterwards: both machines use the new folder; the old one keeps every file"
for M in A B; do
  LOC=$($M storage status --json | J 'd["data"]["this"]["locator"]')
  [ "$LOC" = "fs:$NEW" ] || { echo "$M still uses $LOC"; exit 1; }
  $M storage status
done
AFTER=$(files "$OLD"); COPIED=$(files "$NEW")
echo "old folder: $AFTER files (was $BEFORE); new folder: $COPIED files"
[ "$AFTER" = "$BEFORE" ] || { echo "the old folder changed"; exit 1; }
[ "$COPIED" -ge "$BEFORE" ] || { echo "the new folder is missing files"; exit 1; }
for f in "$HA"/state/storage-move/dashboard/*.log "$HB"/state/storage-move/dashboard/*.log; do echo "--- $(basename "$(dirname "$(dirname "$(dirname "$(dirname "$f")")")")") move log"; cat "$f"; done
echo "storage e2e: ok"
exit 0
