#!/usr/bin/env bash
# End-to-end run of the dashboard's Sound tab in a real browser, on a throwaway
# installation: a fresh home and local test database, the noisy two-person
# fixture (transcribed and diarized here, the voices named), the fake
# ElevenLabs API (no real account or key) and the clearer-voice components.
# It drives a person's level over their own speech, a clearer voice (clean,
# studio when it is set up here, cloud priced first and refused over budget),
# the A/B switches and strength, music dropped in (fitted, ending on a beat),
# its level and getting quieter when someone talks, timing by hand and on the
# timeline's music lane (drag, and a file dropped on the lane), checks the page
# never shows "LUFS", "ducking", "de-ess" or "DNSMOS", and saves screenshots
# (desktop and phone, dark and light).
#
#   scripts/sound-e2e.sh               # needs scripts/dev-mongo.sh running; makes the sound fixtures (macOS say)
#   KEEP=1 scripts/sound-e2e.sh        # keep the home, database and servers for a look afterwards
#   METARUSH_SOUND_PORT=4963 METARUSH_SOUND_FAKE_PORT=4964 SKIP_BUILD=1 scripts/sound-e2e.sh
#   METARUSH_TEST_SOUND_HOME=<home>    # a home whose components (clean, studio) are used, never downloaded
#
# The throwaway database is dropped on exit through store.DropTestDatabase
# (scripts/droptestdb refuses anything but local metarush-test-*).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_SOUND_PORT:-4963}"; FAKE="${METARUSH_SOUND_FAKE_PORT:-4964}"
H="${METARUSH_SOUND_E2E_HOME:-$ROOT/.agent-home/sound-e2e-$(date +%s)}"; DB="metarush-test-sound$(date +%s)"
COMPONENTS="${METARUSH_TEST_SOUND_HOME:-$ROOT/.agent-home/sound}/components"
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
scripts/make-sound-fixtures.sh >/dev/null
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation with the fake ElevenLabs API"
mkdir -p "$H"
# The home sits in this checkout's ignored .agent-home, hence --allow-unsafe-secret-path.
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --allow-unsafe-secret-path --db-name "$DB" --workspace-name "Sound test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
if [ -d "$COMPONENTS" ]; then
  ln -s "$COMPONENTS" "$H/components"
  echo "clearer-voice components from $COMPONENTS"
else
  echo "no components at $COMPONENTS: clean sets itself up on first use (a download)"
fi
KEYFILE="$H/state/fake-elevenlabs.key"; mkdir -p "$H/state"
# A fake key made at run time (the fake API accepts only this one).
python3 -I -c "import secrets;print('sk'+'_'+secrets.token_hex(20))" > "$KEYFILE"; chmod 600 "$KEYFILE"
go build -o "$H/fakeeleven" ./internal/testutil/fakeeleven/cmd/fakeeleven
"$H/fakeeleven" -addr "127.0.0.1:$FAKE" -key-file "$KEYFILE" > "$H/fakeeleven.log" 2>&1 & FK=$!

step "the two-person recording, transcribed and the voices named, and a cut"
GEN=tests/fixtures/gen/sound
P=$(MR project create "Garden show" --tone warm --json | J 'd["data"]["project"]["id"]')
MR ingest "$GEN/noisy-two-speakers.mov" --project "$P" --wait --json \
  | J '"ingested=%d jobs=%s" % (d["data"]["ingest"]["ingested"], sorted({j["state"] for j in d["data"]["jobs"]}))'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="noisy-two-speakers.mov"][0]')
# Whoever speaks first is Samantha (the fixture's turns: tests/fixtures/truth/sound-two-speakers.json).
VOICES=$(MR footage transcript "$A" --json | J '" ".join(dict.fromkeys(s.get("speaker","") for s in d["data"]["segments"] if s.get("speaker")))')
set -- $VOICES
[ "$#" -ge 2 ] || { echo "diarization found $# voice(s): $VOICES"; exit 1; }
MR footage speakers rename "$A" "$1" "Samantha" --new-person >/dev/null
MR footage speakers rename "$A" "$2" "Daniel" --new-person >/dev/null
DUR=$(MR asset show "$A" --json | J 'round(d["data"]["asset"]["media"]["duration"]-0.05,2)')
cat > "$H/cut.json" <<EOF
{"projectId":"$P","instruction":"first cut","rationale":"The garden show talk.",
 "spec":{"schemaVersion":1,"title":"Garden show","output":{"preset":"landscape-1080p","fps":30,"platform":"youtube"},
  "clips":[{"id":"talk","label":"The talk","assetId":"$A","in":0,"out":$DUR}]}}
EOF
TOK=$(MR edit check --plan "$H/cut.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$H/cut.json" --confirm "$TOK" --json | J '"cut r%d" % d["data"]["revision"]["number"]'
# The music the person drops in (40 s at 120 bpm, the first beat at 0.25 s), under a friendly name.
cp "$GEN/music-beat.m4a" "$H/Garden bed.m4a"
# A second, shorter bed (other bytes: an identical file would be caught as a duplicate before upload).
ffmpeg -hide_banner -loglevel error -y -i "$GEN/music-beat.m4a" -t 12 -c:a aac -b:a 128k "$H/Second bed.m4a"

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_ELEVENLABS_BASE_URL="http://127.0.0.1:$FAKE" METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$H/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  echo "fake ElevenLabs key file: $KEYFILE"
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" \
METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
METARUSH_CLI="./bin/metarush --home $H" \
SOUND_PROJECT="Garden show" SOUND_KEY_FILE="$KEYFILE" SOUND_FAKE_URL="http://127.0.0.1:$FAKE" \
SOUND_MUSIC="$H/Garden bed.m4a" SOUND_MUSIC2="$H/Second bed.m4a" \
SOUND_SHOTS="${SOUND_SHOTS:-$H/screenshots}" node web/scripts/sound-e2e.mjs
exit 0
