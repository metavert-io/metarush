#!/usr/bin/env bash
# spoken-word-e2e.sh — changing a spoken word in a real browser (D308–D312), on a throwaway installation: a
# clip of a person (the Mac's synthetic voice) transcribed and fingerprinted on this computer, their voice named,
# their consent covering new words with ElevenLabs (the fake: no account, no real key), a copy of their voice, a
# cut; then in the dashboard a right-click on “twelve” → Say it differently…, the price first, the new words made
# by the fake ElevenLabs in the same synthetic voice (made here beforehand with `say`) and checked by the real
# recognizer and voice fingerprints, heard, put in the cut, marked in both transcripts, named in the captions
# file, and undone. Screenshots are saved. Needs macOS `say` and ffmpeg, and the recognizer and speaker detection
# set up on this computer.
#
#   scripts/spoken-word-e2e.sh              # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/spoken-word-e2e.sh       # keep the home, database and servers for a look afterwards
#   SKIP_BUILD=1 scripts/spoken-word-e2e.sh
#
# Ports 5580 (dashboard) and 5581 (fake ElevenLabs). The throwaway database is dropped on exit through
# scripts/droptestdb (store.DropTestDatabase refuses anything but local metarush-test-*).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_SW_PORT:-5580}"; XFAKE="${METARUSH_SW_ELEVEN_PORT:-5581}"
BASE="$(mktemp -d "${TMPDIR%/}/spoken-word-e2e-XXXXXX")"
H="$BASE/home"; DB="metarush-test-spoken$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SRV=""; XF=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, dashboard http://127.0.0.1:$PORT (pids $SRV $XF)"
    return
  fi
  for p in $SRV $XF; do kill "$p" 2>/dev/null || true; done
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
command -v say >/dev/null || { echo "needs macOS say"; exit 1; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation that talks only to the fake ElevenLabs"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Spoken word test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
mkdir -p "$H/state" "$BASE/speech" "$BASE/media"
XKEY="$H/state/fake-eleven.key"
python3 -I -c "import secrets;print('xk-'+secrets.token_hex(20))" > "$XKEY"
chmod 600 "$XKEY"
{
  printf 'METARUSH_MONGODB_URI=%s\n' "$URI"
  printf 'METARUSH_ELEVENLABS_BASE_URL=http://127.0.0.1:%s\n' "$XFAKE"
  printf 'METARUSH_ELEVENLABS_API_KEY=%s\n' "$(cat "$XKEY")"
} > "$H/.env"; chmod 600 "$H/.env"
go build -o "$BASE/fakeeleven" ./internal/testutil/fakeeleven/cmd/fakeeleven
"$BASE/fakeeleven" -addr "127.0.0.1:$XFAKE" -key-file "$XKEY" -speech-dir "$BASE/speech" > "$BASE/fakeeleven.log" 2>&1 & XF=$!

step "a clip of Ada (the Mac's voice), transcribed and fingerprinted on this computer"
VOICE="${SW_VOICE:-Samantha}"
say -v "$VOICE" -o "$BASE/media/garden.aiff" -- "This spring we planted twelve rows of beans along the old fence. They came up in a week, and the neighbours noticed. By the summer we had more than we could eat, so we gave baskets of them away at the market every Saturday morning."
ffmpeg -v error -y -f lavfi -i "color=c=0x2a6f4e:s=640x360:r=30" -i "$BASE/media/garden.aiff" -shortest -c:v libx264 -pix_fmt yuv420p -c:a aac -ar 48000 "$BASE/media/garden.mp4"
P=$(MR project create "Garden story" --json | J 'd["data"]["project"]["id"]')
MR ingest "$BASE/media/garden.mp4" --project "$P" --wait --json | J '"added=%d jobs=%s" % (d["data"]["ingest"]["ingested"], sorted({j["state"] for j in d["data"]["jobs"]}))'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"]][0]')
MR footage transcript "$A" --json | J '"heard: " + " ".join(s["text"] for s in d["data"]["segments"])'

step "the dashboard on 127.0.0.1:$PORT (with its worker, which makes and checks everything)"
METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" \
METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
SW_PROJECT="$P" SW_CLIP="$A" SW_SPEECH="$BASE/speech" SW_VOICE="$VOICE" SW_SHOTS="${SW_SHOTS:-$BASE/screenshots}" \
node web/scripts/spoken-word-e2e.mjs

step "the CLI, with --json"
MR cut say list "$P" --json | J '"changes: %s" % [(c["state"], c["inCut"]) for c in d["data"]["changes"]]'
exit 0
