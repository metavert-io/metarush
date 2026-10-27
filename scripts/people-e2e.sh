#!/usr/bin/env bash
# End-to-end run of asking who people are (D155–D158) in a real browser, on a
# throwaway installation (needs a Mac: the on-screen index uses Apple Vision):
# the speaker-view call fixture (scripts/make-call-fixtures.sh) in a project
# whose cut uses its first minutes, interview-b (someone else's voice) and
# visual-a (one of the call's faces in another clip). web/scripts/people-e2e.mjs
# checks the grid of face questions, one click for the names the call shows,
# the voice comparison dialog fixing an earlier wrong voice in place, and the
# faces a name was learned from; answers for many at once (D189) on a second copy
# of the call in another project ("None of these are …", "Not … in this clip",
# "Skip all in this project"); screenshots on desktop and phone, both themes.
#
#   scripts/people-e2e.sh                    # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/people-e2e.sh             # keep the home, database and server for a look afterwards
#   METARUSH_PEOPLE_PORT=5363 SKIP_BUILD=1 PEOPLE_SHOTS=<folder> scripts/people-e2e.sh
#   METARUSH_FACE_MODEL=<sface.onnx>         # a copy of the face model, so nothing is downloaded
#
# The throwaway database is dropped on exit through store.DropTestDatabase
# (scripts/droptestdb refuses anything but local metarush-test-*).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_PEOPLE_PORT:-5362}"
BASE="$(mktemp -d "${TMPDIR:-/tmp}/metarush-people-e2e.XXXXXX")"; H="$BASE/home"; DB="metarush-test-ppl$(date +%s)"
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
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
export METARUSH_VISION_CACHE="$BASE/vision-cache"
scripts/make-fixtures.sh >/dev/null
scripts/make-call-fixtures.sh >/dev/null
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation"
mkdir -p "$H"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "People test" --media filesystem --port "$PORT" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
if [ -n "${METARUSH_FACE_MODEL:-}" ]; then mkdir -p "$H/components/faces" && cp "$METARUSH_FACE_MODEL" "$H/components/faces/"; fi
if ! MR footage visuals status --json | J 'd["data"]["here"]["supported"]' | grep -q True; then
  echo "the on-screen index needs a Mac: nothing to check here"; exit 0
fi

step "a call in a project whose cut uses its first minutes; someone else's voice; one of the call's faces elsewhere"
P=$(MR project create "Garden club" --json | J 'd["data"]["project"]["id"]')
MR ingest tests/fixtures/gen/call/garden-call.mp4 --project "$P" --wait --json | J '"the call: %s" % sorted({"%s %s" % (j["type"], j["state"]) for j in d["data"]["jobs"]})'
CALL=$(MR asset list "$P" --json | J 'd["data"]["assets"][0]["id"]')
MR footage speakers hint "$CALL" --count 3 --wait --json >/dev/null
MR ingest tests/fixtures/gen/interview-b.mp4 tests/fixtures/gen/visual-a.mp4 --wait --json >/dev/null
id_of() { MR asset list --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="'"$1"'"][0]'; }
OTHER=$(id_of interview-b.mp4); LOOK=$(id_of visual-a.mp4)
cat > "$BASE/cut.json" <<EOF
{"projectId":"$P","instruction":"the opening","spec":{"schemaVersion":1,"title":"Garden club","output":{"preset":"landscape-1080p","fps":30},
  "clips":[{"id":"open","assetId":"$CALL","in":0,"out":20}]}}
EOF
TOK=$(MR edit check --plan "$BASE/cut.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$BASE/cut.json" --confirm "$TOK" --json | J '"cut version %d" % d["data"]["revision"]["number"]'
# A second copy of the call (other bytes, the same people and name tiles) in another project: two
# questions suggest each name, for the answers for many at once (D189).
P2=$(MR project create "Second club" --json | J 'd["data"]["project"]["id"]')
ffmpeg -nostdin -loglevel error -y -i tests/fixtures/gen/call/garden-call.mp4 -c:v libx264 -crf 21 -preset veryfast -c:a copy "$BASE/garden-call-again.mp4"
MR ingest "$BASE/garden-call-again.mp4" --project "$P2" --wait --json | J '"the copy: %s" % sorted({"%s %s" % (j["type"], j["state"]) for j in d["data"]["jobs"]})'
COPY=$(MR asset list "$P2" --json | J 'd["data"]["assets"][0]["id"]')
MR todos groups --json | J '"questions: %s" % [(g["question"], g["context"]["appearances"], g["context"].get("inCut", False)) for g in d["data"]["groups"]]'

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve --no-worker > "$BASE/serve.log" 2>&1 & SRV=$!  # the binary itself (not MR), so $SRV is the server
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done
if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1; exit 0
fi

step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
PEOPLE_MR="./bin/metarush --home $H" PEOPLE_CALL="$CALL" PEOPLE_OTHER="$OTHER" PEOPLE_LOOK="$LOOK" PEOPLE_COPY="$COPY" PEOPLE_P2="$P2" \
PEOPLE_SHOTS="${PEOPLE_SHOTS:-$ROOT/tests/out/people-shots}" node web/scripts/people-e2e.mjs || { echo "--- dashboard log"; tail -40 "$BASE/serve.log"; exit 1; }
exit 0
