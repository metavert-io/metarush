#!/usr/bin/env bash
# scripts/motion-shapes-e2e.sh — a motion graphic drawn in another shape after a person approved it (D377, D381):
# a throwaway home and database, the route-map fixture checked vertical, then the browser run
# (web/scripts/motion-shapes-e2e.mjs): a person approves it in the dashboard; a non-person draws it again
# widescreen from a terminal; the frames the person approved stay; a widescreen cut is noted; the person
# approves the new shape on the graphic's page; the note clears. Ports 5740–5749.
#
#   scripts/motion-shapes-e2e.sh                 # needs scripts/dev-mongo.sh running and the video package installed
#   KEEP=1 scripts/motion-shapes-e2e.sh          # keep the home, database and server for a look afterwards
#   SKIP_BUILD=1 METARUSH_MOTION_SHAPES_PORT=5741 scripts/motion-shapes-e2e.sh
#
# The throwaway database is dropped on exit through store.DropTestDatabase
# (scripts/droptestdb refuses anything but local metarush-test-*).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_MOTION_SHAPES_PORT:-5740}"
H="$(mktemp -d "${TMPDIR:-/tmp}/motion-shapes-e2e-XXXXXX")/home"; DB="metarush-test-mshapes$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SRV=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, dashboard http://127.0.0.1:$PORT (pid $SRV)"
    return
  fi
  [ -n "$SRV" ] && kill "$SRV" 2>/dev/null || true
  go run ./scripts/droptestdb "$URI" "$DB" || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$(dirname "$H")"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation"
mkdir -p "$H"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Motion shapes test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
printf 'METARUSH_MONGODB_URI=%s\n' "$URI" > "$H/.env"; chmod 600 "$H/.env"

step "a project and the route-map graphic, checked vertical"
P=$(MR project create "Shapes" --json | J 'd["data"]["project"]["id"]')
G="$(dirname "$H")/motion/route-map"; mkdir -p "$(dirname "$G")"; cp -R video/render/fixtures/motion/route-map "$G"
VERSION=$(MR motion check "$G" --project "$P" --shape vertical --no-preview --json | J 'd["data"]["version"]')
echo "checked $VERSION"

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$H/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" \
METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
MS_MR="./bin/metarush --home $H" MS_PROJECT="$P" MS_VERSION="$VERSION" \
SHOTS="${SHOTS:-$(dirname "$H")/screenshots}" node web/scripts/motion-shapes-e2e.mjs
exit 0
