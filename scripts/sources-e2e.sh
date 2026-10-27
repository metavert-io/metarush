#!/usr/bin/env bash
# End-to-end run of descriptive names, the info card, fixes and Generate video in the drop box (D150–D154) in a real
# browser, on a throwaway installation: a fresh home, a local test database, made-up footage (a shaky camera clip with
# a camera's own file name, a voice memo with a hum, an interview whose voice gets a name after its first look) and the
# fake Gemini API (internal/testutil/fakevideogen: no Google account, no real key). The browser run reads a clip's info
# card, renames it, ticks Steady the camera and waits for the fixed copy (made by the dashboard's worker), looks at
# before and after, uses the copy in the cut as a new version, unticks it, takes MetaRush's better names (Use all),
# names the cut and a version, fixes a picture, and generates a shot from Home's drop box that starts from a photo;
# screenshots (desktop light and dark, a phone) go to the screenshots folder for review.
#
#   scripts/sources-e2e.sh                  # needs scripts/dev-mongo.sh running and tests/fixtures/gen
#   KEEP=1 scripts/sources-e2e.sh           # keep the home, database and servers for a look afterwards
#   METARUSH_STABILIZER_ZIP=<the pinned archive> scripts/sources-e2e.sh   # steady with vid.stab without downloading
#   METARUSH_SRC_PORT=5355 METARUSH_SRC_FAKE_PORT=5356 SKIP_BUILD=1 scripts/sources-e2e.sh
#
# The throwaway database is dropped on exit through store.DropTestDatabase
# (scripts/droptestdb refuses anything but local metarush-test-*).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_SRC_PORT:-5355}"; FAKE="${METARUSH_SRC_FAKE_PORT:-5356}"
# Outside any repository: the key file and .env must never sit where git could commit them.
H="${METARUSH_SRC_HOME:-$(mktemp -d -t sources-e2e)/home}"; DB="metarush-test-src$(date +%s)"
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
# Its own binary next to the home (a run of acceptance.sh may be using bin/metarush meanwhile).
BIN="${METARUSH_BIN:-$(dirname "$H")/metarush}"
MR() { "$BIN" --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
# Builds of the on-screen helper (macOS) stay in this run's folder, not the user's cache.
export METARUSH_VISION_CACHE="$(dirname "$H")/vision-cache"
# The better steadying comes from a pinned download this run never fetches: without a copy given, ffmpeg's basic method.
export METARUSH_STABILIZER_ZIP="${METARUSH_STABILIZER_ZIP:-$(dirname "$H")/no-stabilizer-download.zip}"
[ -f tests/fixtures/gen/interview-a.mov ] || scripts/make-fixtures.sh
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o "$BIN" ./cmd/metarush
elif [ ! -x "$BIN" ]; then
  # SKIP_BUILD (scripts/check.sh): the binary already built, copied so a run of acceptance.sh keeps bin/metarush.
  cp -c bin/metarush "$BIN" 2>/dev/null || cp bin/metarush "$BIN"
fi

step "a throwaway installation that talks only to the fake Gemini API"
mkdir -p "$H"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Sources test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
printf 'METARUSH_MONGODB_URI=%s\nMETARUSH_GEMINI_BASE_URL=http://127.0.0.1:%s\n' "$URI" "$FAKE" > "$H/.env"; chmod 600 "$H/.env"
KEYFILE="$H/state/fake-gemini.key"; mkdir -p "$H/state"
python3 -I -c "import secrets;print('gk-'+secrets.token_hex(20))" > "$KEYFILE"; chmod 600 "$KEYFILE"
go build -o "$H/fakevideogen" ./internal/testutil/fakevideogen/cmd/fakevideogen
"$H/fakevideogen" -addr "127.0.0.1:$FAKE" -key-file "$KEYFILE" > "$H/fakevideogen.log" 2>&1 & FK=$!
for _ in $(seq 1 240); do curl -s -o /dev/null "http://127.0.0.1:$FAKE/__calls" && break; sleep 0.25; done
MR providers set gemini --key-stdin --json < "$KEYFILE" | J '"the fake Gemini key: %s" % d["data"]["lastTest"]["message"]'

step "footage: a shaky camera clip, a voice memo with a hum, an interview"
SRCF="$(dirname "$H")/sources"; mkdir -p "$SRCF"
ffmpeg -nostdin -loglevel error -y -f lavfi -i "mandelbrot=s=1440x820:end_pts=1" -frames:v 1 "$SRCF/scene.png"
ffmpeg -nostdin -loglevel error -y -loop 1 -framerate 30 -i "$SRCF/scene.png" -f lavfi -i "sine=frequency=330:duration=8" -t 8 \
  -vf "crop=1280:720:x='80+26*sin(n*1.9)*cos(n*0.7)':y='50+20*sin(n*2.3+1)',format=yuv420p" -c:v libx264 -crf 18 -c:a aac -shortest \
  -metadata creation_time=2026-10-12T15:40:00Z "$SRCF/IMG_4821.MOV"
ffmpeg -nostdin -loglevel error -y -f lavfi -i "aevalsrc=0.25*sin(2*PI*220*t)*(0.6+0.4*sin(2*PI*3*t))+0.08*sin(2*PI*60*t)+0.05*sin(2*PI*120*t)+0.03*sin(2*PI*180*t):s=48000:d=10" \
  -c:a pcm_s16le "$SRCF/kitchen-voice.wav"
# A call recorded in speaker view whose two webcams shake (D190): Ada's picture, then a hard switch to Ben's.
for who in "ada:beach.jpg:300" "ben:portrait.jpg:420"; do
  IFS=: read -r name photo hz <<<"$who"
  ffmpeg -nostdin -loglevel error -y -loop 1 -framerate 30 -t 5 -i "tests/fixtures/images/$photo" -f lavfi -i "sine=frequency=$hz:duration=5" -t 5 \
    -vf "scale=1440:820:force_original_aspect_ratio=increase,crop=1440:820,crop=1280:720:x='80+26*sin(n*1.7)*cos(n*0.6)':y='50+20*sin(n*2.1+1)',format=yuv420p" \
    -c:v libx264 -crf 18 -c:a aac -shortest "$SRCF/$name-cam.mp4"
done
ffmpeg -nostdin -loglevel error -y -i "$SRCF/ada-cam.mp4" -i "$SRCF/ben-cam.mp4" -filter_complex "[0:v][0:a][1:v][1:a]concat=n=2:v=1:a=1[v][a]" -map "[v]" -map "[a]" \
  -c:v libx264 -crf 18 -c:a aac "$SRCF/zoom garden call.mp4"
P=$(MR project create "Garden film" --goal "an awareness video for the garden open day" --json | J 'd["data"]["project"]["id"]')
MR ingest "$SRCF/IMG_4821.MOV" "$SRCF/kitchen-voice.wav" tests/fixtures/gen/interview-a.mov "$SRCF/zoom garden call.mp4" --project "$P" --wait --json \
  | J '"added=%d jobs=%s" % (d["data"]["ingest"]["ingested"], sorted({j["state"] for j in d["data"]["jobs"]}))'
SH=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="IMG_4821.MOV"][0]')
VM=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="kitchen-voice.wav"][0]')
IA=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="interview-a.mov"][0]')
CL=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="zoom garden call.mp4"][0]')
# Every first analysis done (on a Mac, what's on screen too), so later facts become suggestions, not renames.
QJ=$(MR job list --state queued --json | J '" ".join(j["id"] for j in d["data"]["jobs"] if j["type"] != "inspect")')
[ -n "$QJ" ] && MR job wait $QJ --json >/dev/null || true
MR asset fixes "$SH" --look --wait --json | J '"the shaky clip: %s" % [r["why"] for r in d["data"]["view"]["fixes"] if r["fits"]]'
MR asset fixes "$VM" --look --wait --json | J '"the voice memo: %s" % [r["why"] for r in d["data"]["view"]["fixes"] if r["fits"]]'
MR footage switches "$CL" --json | J '"the call switches at %s" % [s["at"] for s in d["data"].get("switches") or []]' || true
MR asset fixes "$CL" --look --wait --json | J '"the call (%s): %s" % (d["data"]["view"]["kind"], [(r["id"], r["fits"], r.get("measured")) for r in d["data"]["view"]["fixes"]])'
# A voice named after the first look: MetaRush's better name for the interview waits as a suggestion.
MR footage speakers rename "$IA" "Speaker 1" "Samantha" --json >/dev/null
MR asset names --project "$P" --json | J '"better names waiting: %s" % [(r["title"], r["suggestion"]) for r in d["data"]["names"]]'
cat > "$H/cut.json" <<EOF
{"projectId":"$P","instruction":"first cut of the walk","rationale":"The walk, then the interview.",
 "spec":{"schemaVersion":1,"title":"garden main v2","output":{"preset":"landscape-1080p","fps":30},
  "clips":[{"id":"walk","assetId":"$SH","in":0.5,"out":7.5},{"id":"talk","assetId":"$IA","in":0.4,"out":6}]}}
EOF
TOK=$(MR edit check --plan "$H/cut.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$H/cut.json" --confirm "$TOK" --json | J '"cut: version %d" % d["data"]["revision"]["number"]'
# One generated shot in this project, then another project: "the one used last" is the project of the last shot.
MR generate shot "a watering can on a garden table" --project "$P" --seconds 3 --resolution 360p --max-cost 0.5 --wait --json </dev/null | J '"a shot made earlier: %s" % d["data"]["generations"][0]["state"]'
MR project create "Other film" --json >/dev/null

step "the dashboard on 127.0.0.1:$PORT (with its worker, which makes the fixed copies)"
METARUSH_DEV_ORIGINS="" "$BIN" --home "$H" serve > "$H/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" \
METARUSH_MINT="$BIN --home $H dashboard --print-link --no-open" \
SRC_PROJECT="$P" SRC_SHAKY="$SH" SRC_VOICE="$VM" SRC_INTERVIEW="$IA" SRC_CALL="$CL" SRC_PHOTO="$ROOT/tests/fixtures/images/dog.jpg" \
SRC_SHOTS="${SRC_SHOTS:-$(dirname "$H")/screenshots}" node web/scripts/sources-e2e.mjs
exit 0
