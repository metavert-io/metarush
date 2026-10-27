#!/usr/bin/env bash
# End-to-end run of generative video (D123–D127) in a real browser, on a throwaway installation: a fresh home
# (gen-…), a local test database, fixture footage and the fake Gemini API (internal/testutil/fakevideogen: no Google
# account, no real key, nothing made by a real model). The browser run drives Settings → Integrations (the key
# flow: a wrong key refused, the right one checked and saved, shown masked, never in the page, a response or the
# browser's storage), the generative-video settings, Generate a shot (priced first, made, then Variations), a shot
# continued from a frame, consent and a look on the People page (refused without consent first), the Script's
# Generated line, estimates that open and Settings → Preferences with a fake `claude` asking for a shot (D208:
# its card waits with estimates on, runs by itself under Auto with estimates off), and saves screenshots for review.
#
#   scripts/generate-e2e.sh                 # needs scripts/dev-mongo.sh running and tests/fixtures/gen
#   KEEP=1 scripts/generate-e2e.sh          # keep the home, database and servers for a look afterwards
#   NO_BROWSER=1 scripts/generate-e2e.sh    # set it up and print how to sign in
#   METARUSH_GEN_PORT=5141 METARUSH_GEN_FAKE_PORT=5142 METARUSH_GEN_HOME=/tmp/gen-x SKIP_BUILD=1 scripts/generate-e2e.sh
#
# The throwaway database is dropped on exit through store.DropTestDatabase
# (scripts/droptestdb refuses anything but local metarush-test-*).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_GEN_PORT:-5141}"; FAKE="${METARUSH_GEN_FAKE_PORT:-5142}"
# Outside any repository: the key file and .env must never sit where git could commit them.
H="${METARUSH_GEN_HOME:-$(mktemp -d -t gen-e2e)/home}"; DB="metarush-test-gen$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SRV=""; FK=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, dashboard http://127.0.0.1:$PORT (pids $SRV $FK)"
    return
  fi
  [ -n "$SRV" ] && kill "$SRV" 2>/dev/null || true
  [ -n "$FK" ] && kill "$FK" 2>/dev/null || true
  go run ./scripts/droptestdb "$URI" "$DB" || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$(dirname "$H")"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
[ -f tests/fixtures/gen/interview-a.mov ] && [ -f tests/fixtures/gen/visual-a.mp4 ] || scripts/make-fixtures.sh
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation that talks only to the fake Gemini API"
mkdir -p "$H"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Generate test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
# The base URL goes in the home's .env before any key exists, so every process of this home (the CLI, the
# dashboard and its worker) sends requests to the fake, never to Google.
printf 'METARUSH_MONGODB_URI=%s\nMETARUSH_GEMINI_BASE_URL=http://127.0.0.1:%s\n' "$URI" "$FAKE" > "$H/.env"; chmod 600 "$H/.env"
KEYFILE="$H/state/fake-gemini.key"; mkdir -p "$H/state"
# A fake key made at run time (the fake API accepts only this one).
python3 -I -c "import secrets;print('gk-'+secrets.token_hex(20))" > "$KEYFILE"; chmod 600 "$KEYFILE"
go build -o "$H/fakevideogen" ./internal/testutil/fakevideogen/cmd/fakevideogen
"$H/fakevideogen" -addr "127.0.0.1:$FAKE" -key-file "$KEYFILE" > "$H/fakevideogen.log" 2>&1 & FK=$!

step "footage: an interview, other shots and a clip with faces; someone named Ada"
P=$(MR project create "Pond film" --json | J 'd["data"]["project"]["id"]')
MR ingest tests/fixtures/gen/interview-a.mov tests/fixtures/gen/broll-c.mp4 tests/fixtures/gen/visual-a.mp4 --project "$P" --wait --json \
  | J '"added=%d jobs=%s" % (d["data"]["ingest"]["ingested"], sorted({j["state"] for j in d["data"]["jobs"]}))'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="interview-a.mov"][0]')
B=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="broll-c.mp4"][0]')
V=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="visual-a.mp4"][0]')
FACES=0
if MR footage visuals status --json | J 'd["data"]["here"]["supported"]' | grep -q True; then
  MR footage visuals "$V" --json >/dev/null || true
  # The fixture's two faces (public-domain portraits) stand in for one person, "Ada": consent is recorded and a
  # look is made, but nothing is ever generated from them, not even by the fake.
  FACE1=$(MR footage faces "$V" --json | J '([f["id"] for f in d["data"]["faces"]] + ["", ""])[0]' || true)
  FACE2=$(MR footage faces "$V" --json | J '([f["id"] for f in d["data"]["faces"]] + ["", ""])[1]' || true)
  if [ -n "$FACE1" ]; then
    MR footage faces name "$FACE1" "Ada" --new-person --json | J '"named a face: %s" % d["data"]["done"]'
    [ -n "$FACE2" ] && MR footage faces name "$FACE2" "Ada" --json | J '"named a face: %s" % d["data"]["done"]'
    FACES=1
  fi
fi
# A character look is made from pictures that show no person: a dog, and the same dog mirrored.
ffmpeg -nostdin -loglevel error -y -i tests/fixtures/images/dog.jpg -vf hflip "$H/dog-mirrored.jpg"
MR resources add tests/fixtures/images/dog.jpg --workspace --name captain-dog-1 --title "Captain Dog, left" --json | J 'd["data"]["files"][0]["message"]'
MR resources add "$H/dog-mirrored.jpg" --workspace --name captain-dog-2 --title "Captain Dog, right" --json | J 'd["data"]["files"][0]["message"]'
cat > "$H/cut.json" <<EOF
{"projectId":"$P","instruction":"first cut","rationale":"A short pond story.",
 "spec":{"schemaVersion":1,"title":"Pond film","output":{"preset":"landscape-1080p","fps":30,"platform":"youtube"},
  "clips":[{"id":"kitchen","label":"Kitchen","assetId":"$A","in":0.4,"out":9.0},
           {"id":"garden","label":"Garden","assetId":"$B","in":0,"out":6,"framing":{"mode":"fit"}}]}}
EOF
TOK=$(MR edit check --plan "$H/cut.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$H/cut.json" --confirm "$TOK" --json | J '"cut r%d" % d["data"]["revision"]["number"]'

step "a fake claude that asks for a shot (D208: estimates and Auto)"
go build -o "$H/claude" ./internal/assistant/fakeclaude/cmd/fakeclaude
cat > "$H/claude.json" <<EOF
{"turns":[
 {"match":"while estimates come first","steps":[
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush generate shot \"a paper boat by the reeds\" --project $P --seconds 3 --resolution 360p --json"}}},
  {"text":"I asked for a 3-second shot of the paper boat."}]},
 {"match":"with estimates off","steps":[
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush generate shot \"a paper boat under the bridge\" --project $P --seconds 3 --resolution 360p --json"}}},
  {"text":"I asked for another 3-second shot."}]},
 {"match":"Done by itself","steps":[{"text":"The shot is on its way."}]}
]}
EOF

step "the dashboard on 127.0.0.1:$PORT (with its worker, which makes the shots)"
METARUSH_CLAUDE="$H/claude" METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$H/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  echo "fake Gemini key file: $KEYFILE"
  KEEP=1
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" \
METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
GEN_PROJECT="$P" GEN_CLIP="$A" GEN_FACES="$FACES" GEN_KEY_FILE="$KEYFILE" GEN_FAKE_URL="http://127.0.0.1:$FAKE" \
GEN_SHOTS="${GEN_SHOTS:-$H/screenshots}" GEN_MR="./bin/metarush --home $H" node web/scripts/generate-e2e.mjs
exit 0
