#!/usr/bin/env bash
# End-to-end run of the dashboard's Voiceover tab in a real browser, on a
# throwaway installation: a fresh home and local test database, real fixture
# footage, the fake ElevenLabs API (no real account or key) and Chromium's fake
# microphone fed with a spoken script. It drives every way to add a voiceover
# (write, import, record, AI voice), a fit fix, the timeline's narration lane
# (drag, arrow keys and ⌥-drag, saved in the background, undo), the mixer (changes
# heard in the plan) and takes A/B, checks that scrolling never puts the preview over
# the script cards or the mixer, and saves screenshots (desktop and phone, dark and
# light).
#
#   scripts/voiceover-e2e.sh               # needs scripts/dev-mongo.sh running and tests/fixtures/gen
#   KEEP=1 scripts/voiceover-e2e.sh        # keep the home, database and servers for a look afterwards
#   METARUSH_VO_PORT=4981 METARUSH_VO_FAKE_PORT=4982 SKIP_BUILD=1 scripts/voiceover-e2e.sh
#
# The throwaway database is dropped on exit through store.DropTestDatabase
# (scripts/droptestdb refuses anything but local metarush-test-*).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_VO_PORT:-4981}"; FAKE="${METARUSH_VO_FAKE_PORT:-4982}"
H="${METARUSH_VO_HOME:-$ROOT/.agent-home/vo-e2e-$(date +%s)}"; DB="metarush-test-vo$(date +%s)"
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
. scripts/lib/e2e.sh && e2e_run "$DB" "$H"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { METARUSH_ELEVENLABS_BASE_URL="http://127.0.0.1:$FAKE" ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
[ -f tests/fixtures/gen/interview-a.mov ] || scripts/make-fixtures.sh
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation with the fake ElevenLabs API"
mkdir -p "$H"
# The home sits in this checkout's ignored .agent-home, hence --allow-unsafe-secret-path.
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --allow-unsafe-secret-path --db-name "$DB" --workspace-name "Voiceover test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
KEYFILE="$H/state/fake-elevenlabs.key"; mkdir -p "$H/state"
# A fake key made at run time (the fake API accepts only this one).
python3 -I -c "import secrets;print('sk'+'_'+secrets.token_hex(20))" > "$KEYFILE"; chmod 600 "$KEYFILE"
go build -o "$H/fakeeleven" ./internal/testutil/fakeeleven/cmd/fakeeleven
"$H/fakeeleven" -addr "127.0.0.1:$FAKE" -key-file "$KEYFILE" > "$H/fakeeleven.log" 2>&1 & FK=$!

step "footage, a cut with music, and a spoken script for the fake microphone"
P=$(MR project create "Garden story" --tone warm --json | J 'd["data"]["project"]["id"]')
MR ingest tests/fixtures/gen/interview-a.mov tests/fixtures/gen/broll-c.mp4 tests/fixtures/gen/music-bed.m4a --project "$P" --wait --json \
  | J '"ingested=%d jobs=%s" % (d["data"]["ingest"]["ingested"], sorted({j["state"] for j in d["data"]["jobs"]}))'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="interview-a.mov"][0]')
B=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="broll-c.mp4"][0]')
M=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="music-bed.m4a"][0]')
cat > "$H/cut.json" <<EOF
{"projectId":"$P","instruction":"first cut","rationale":"A short garden story.",
 "spec":{"schemaVersion":1,"title":"Garden story","output":{"preset":"landscape-1080p","fps":30,"platform":"youtube"},
  "clips":[{"id":"kitchen","label":"Kitchen","assetId":"$A","in":0.4,"out":9.0},
           {"id":"garden","label":"Garden","assetId":"$B","in":0,"out":6,"framing":{"mode":"fit"}},
           {"id":"payoff","label":"Payoff","assetId":"$A","in":18.0,"out":26.0}],
  "audio":[{"id":"bed","role":"music","assetId":"$M","in":0,"out":22,"at":0,"gainDb":-8,"rights":{"license":"CC0","source":"generated test fixture"}}]}}
EOF
TOK=$(MR edit check --plan "$H/cut.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$H/cut.json" --confirm "$TOK" --json | J '"cut r%d" % d["data"]["revision"]["number"]'
# The fake microphone: the script the browser test writes, read by macOS say after 2.5 s
# of silence (the recorder's 3-2-1 count-in), then a short pause; the browser loops it.
SCRIPT_LINES=("Every morning starts in the kitchen." "Then the garden wakes up." "And by noon, everything is green.")
printf '%s [[slnc 600]] ' "${SCRIPT_LINES[@]}" > "$H/read.txt"
say -v Samantha -r 165 -f "$H/read.txt" -o "$H/read.aiff"
ffmpeg -hide_banner -loglevel error -y -f lavfi -i anullsrc=r=48000:cl=mono -i "$H/read.aiff" -filter_complex "[0]atrim=0:2.5[s];[1]aformat=sample_rates=48000:channel_layouts=mono[r];[s][r]concat=n=2:v=0:a=1,apad=pad_dur=2" "$H/fake-mic.wav"
# A narration recording the person "already has" (for the import path).
say -v Daniel -r 160 -o "$H/narration-import.aiff" "Every morning starts in the kitchen. Then the garden wakes up. And by noon, everything is green."
ffmpeg -hide_banner -loglevel error -y -i "$H/narration-import.aiff" -ac 1 -ar 48000 "$H/My narration.wav"

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_ELEVENLABS_BASE_URL="http://127.0.0.1:$FAKE" METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$H/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  echo "fake ElevenLabs key file: $KEYFILE · fake microphone: $H/fake-mic.wav"
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" \
METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
VO_PROJECT="Garden story" VO_KEY_FILE="$KEYFILE" VO_FAKE_URL="http://127.0.0.1:$FAKE" VO_FAKE_MIC="$H/fake-mic.wav" VO_IMPORT_FILE="$H/My narration.wav" \
VO_SHOTS="${VO_SHOTS:-$H/screenshots}" node web/scripts/voiceover-e2e.mjs
exit 0
