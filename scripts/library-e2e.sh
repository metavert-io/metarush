#!/usr/bin/env bash
# End-to-end run of the Library (D96–D98) in a real browser, on a throwaway
# installation: a fresh home and local test database, real fixture footage in
# the Inbox and a project whose cut uses one clip. It drives the Library's
# views, the "…" menu (Archive, Add to collection…, Move to Trash), the
# collection picker with create-new, a project using a collection, the Trash
# (Restore, Empty Trash…, Delete forever… and the styled warning with sizes),
# Settings → Storage → Trash and deleting a project (D196: Archived projects,
# Restore, Delete for good, Clear out archived projects), and saves screenshots
# (desktop and phone, dark and light).
#
#   scripts/library-e2e.sh                # needs scripts/dev-mongo.sh running and tests/fixtures/gen
#   KEEP=1 scripts/library-e2e.sh         # keep the home, database and server for a look afterwards
#   METARUSH_LIBRARY_PORT=5113 SKIP_BUILD=1 scripts/library-e2e.sh
#
# The throwaway database is dropped on exit through store.DropTestDatabase
# (scripts/droptestdb refuses anything but local metarush-test-*).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_LIBRARY_PORT:-5112}"
H="${METARUSH_LIBRARY_HOME:-$ROOT/.agent-home/library-e2e-$(date +%s)}"; DB="metarush-test-lib$(date +%s)"
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
. scripts/lib/e2e.sh && e2e_run "$DB" "$H"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
[ -f tests/fixtures/gen/vfr.mp4 ] || scripts/make-fixtures.sh
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation"
mkdir -p "$H"
# The home sits in this checkout's ignored .agent-home, hence --allow-unsafe-secret-path.
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --allow-unsafe-secret-path --db-name "$DB" --workspace-name "Library test" --media filesystem --port "$PORT" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"

step "four clips in the Inbox, and a project whose cut uses a fifth"
MR ingest tests/fixtures/gen/vfr.mp4 tests/fixtures/gen/broll-c.mp4 tests/fixtures/gen/visual-a.mp4 tests/fixtures/gen/long-silence.mp4 --no-transcribe --wait --json \
  | J '"inbox: ingested=%d" % d["data"]["ingest"]["ingested"]'
P=$(MR project create "Spring promo" --json | J 'd["data"]["project"]["id"]')
MR ingest tests/fixtures/gen/phone-rotated.mov --project "$P" --no-transcribe --wait --json >/dev/null
id_of() { MR asset list --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="'"$1"'"][0]'; }
MR asset update "$(id_of vfr.mp4)" --title "Waves at dawn" >/dev/null
MR asset update "$(id_of broll-c.mp4)" --title "Kitchen chat" >/dev/null
MR asset update "$(id_of visual-a.mp4)" --title "Harbour pan" >/dev/null
MR asset update "$(id_of long-silence.mp4)" --title "Blurry take" >/dev/null
U=$(id_of phone-rotated.mov); MR asset update "$U" --title "Interview" >/dev/null
cat > "$H/cut.json" <<EOF
{"projectId":"$P","instruction":"first cut","spec":{"schemaVersion":1,"title":"Spring promo","output":{"preset":"vertical-1080p","fps":30,"platform":"shorts"},
  "clips":[{"id":"open","assetId":"$U","in":0,"out":2}]}}
EOF
TOK=$(MR edit check --plan "$H/cut.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$H/cut.json" --confirm "$TOK" --json | J '"cut r%d" % d["data"]["revision"]["number"]'

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve --no-worker > "$H/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" \
METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
LIBRARY_PROJECT="$P" SHOTS_DIR="${SHOTS_DIR:-$H/screenshots}" node web/scripts/library-e2e.mjs
exit 0
