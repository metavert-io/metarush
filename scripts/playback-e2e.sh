#!/usr/bin/env bash
# Playing through a database outage (D180) in a real browser, on a throwaway
# installation: a fresh home and local test database, three short clips (two
# of them in a cut) and the dashboard. The browser run (web/scripts/
# playback-e2e.mjs) starts the dashboard behind a link to the test MongoDB
# that it cuts and restores — mongod itself keeps running — and checks that:
#   A  in an outage, a clip MetaRush remembers still streams (from memory), and
#      one it doesn't is refused at once, in plain words;
#   B  the source viewer of a clip the server can't send says "MetaRush can't
#      reach its database right now; it keeps trying" and plays once it can;
#   C  the cut preview stalled in the middle of the cut says "Reconnecting…"
#      instead of an endless "Buffering…", and goes on from the same time;
#   D  a cut MetaRush got ready plays through an outage, clips not reached yet too;
#   E  the version page lists its review copy (with when it was made) and plays
#      it in its player, through an outage too (D187);
#   F  the Edit tab shows the review copy of the latest version by default, with
#      a switch to the live preview; a checked motion graphic shows as its still
#      in the live preview, labelled; phone width without sideways scrolling;
# with screenshots. No native browser dialog may appear.
#
#   scripts/playback-e2e.sh                     # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/playback-e2e.sh              # keep the home and the database afterwards
#   METARUSH_PLAYBACK_PORT=5480 METARUSH_PLAYBACK_DB_PORT=5482 SKIP_BUILD=1 scripts/playback-e2e.sh
#
# The throwaway database is dropped on exit through store.DropTestDatabase
# (scripts/droptestdb refuses anything but local metarush-test-*).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_PLAYBACK_PORT:-5480}"; DBPORT="${METARUSH_PLAYBACK_DB_PORT:-5482}"
H="${METARUSH_PLAYBACK_HOME:-$(mktemp -d)/home}"; DB="metarush-test-play$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB"
    return
  fi
  go run ./scripts/droptestdb "$URI" "$DB" || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$(dirname "$H")"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys
d=json.load(sys.stdin)
try: print($1)
except (KeyError,IndexError,TypeError): sys.exit('unexpected answer: '+json.dumps(d)[:3000])"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation"
mkdir -p "$H"
# --allow-unsafe-secret-path: a home given inside this checkout (METARUSH_PLAYBACK_HOME) is allowed for a test.
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --allow-unsafe-secret-path --db-name "$DB" --workspace-name "Playback test" --media filesystem --port "$PORT" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"

step "three short clips: two in a cut, one in the Inbox"
CLIPS="$H/clips"; mkdir -p "$CLIPS"
make_clip() { # name, picture source, tone (Hz)
  ffmpeg -hide_banner -loglevel error -nostdin -y -f lavfi -i "$2=size=640x360:rate=30:duration=6" -f lavfi -i "sine=frequency=$3:duration=6:sample_rate=48000" \
    -c:v libx264 -preset veryfast -pix_fmt yuv420p -g 30 -c:a aac -b:a 96k -shortest "$CLIPS/$1.mp4"
}
make_clip harbour testsrc2 440; make_clip kitchen smptebars 550; make_clip waves rgbtestsrc 660
P=$(MR project create "Spring promo" --json | J 'd["data"]["project"]["id"]')
MR ingest "$CLIPS/harbour.mp4" "$CLIPS/kitchen.mp4" --project "$P" --no-transcribe --wait --json | J '"in the project: ingested=%d" % d["data"]["ingest"]["ingested"]'
MR ingest "$CLIPS/waves.mp4" --no-transcribe --wait --json | J '"in the Inbox: ingested=%d" % d["data"]["ingest"]["ingested"]'
id_of() { MR asset list --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="'"$1"'"][0]'; }
HARBOUR=$(id_of harbour.mp4); KITCHEN=$(id_of kitchen.mp4); WAVES=$(id_of waves.mp4)
MR asset update "$HARBOUR" --title "Harbour walk" >/dev/null
MR asset update "$KITCHEN" --title "Kitchen chat" >/dev/null
MR asset update "$WAVES" --title "Waves at dawn" >/dev/null
cat > "$H/cut.json" <<EOF
{"projectId":"$P","instruction":"two clips","spec":{"schemaVersion":1,"title":"Spring promo","output":{"preset":"landscape-1080p","fps":30,"platform":"youtube"},
  "clips":[{"id":"open","assetId":"$HARBOUR","in":0,"out":2.5},{"id":"close","assetId":"$KITCHEN","in":0,"out":3}]}}
EOF
TOK=$(MR edit check --plan "$H/cut.json" --json | J 'd["data"]["confirmToken"]')
R=$(MR edit apply --plan "$H/cut.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
echo "cut saved"

# D187: a review copy of the cut (rendered here, from this checkout's renderer)
# and a checked motion graphic (its test frames are the live preview's stills;
# the browser run approves it as a person and puts it in a new version).
step "a review copy of the cut, and a checked motion graphic"
DEV=(env METARUSH_VIDEO_DIR="$ROOT/video" METARUSH_DEV_RENDERER=1)
RC=$("${DEV[@]}" ./bin/metarush --home "$H" render submit --revision "$R" --quality review --no-qa --wait --json | J 'd["data"]["render"]["id"]')
echo "review copy ready"
MF="$H/motion-project"; mkdir -p "$MF/motion"; cp -R video/render/fixtures/motion/route-map "$MF/motion/route-map"
MH=$("${DEV[@]}" ./bin/metarush --home "$H" motion check "$MF/motion/route-map" --project "$P" --no-preview --json | J 'd["data"]["hash"]')
echo "motion graphic checked"

step "the browser run (it starts the dashboard on 127.0.0.1:$PORT behind a database link on $DBPORT)"
METARUSH_BIN="$ROOT/bin/metarush" METARUSH_PLAYBACK_HOME="$H" METARUSH_DASHBOARD="http://127.0.0.1:$PORT" \
METARUSH_DB_URI="$URI" METARUSH_DB_LINK_PORT="$DBPORT" \
PLAYBACK_PROJECT="$P" PLAYBACK_REVISION="$R" PLAYBACK_HARBOUR="$HARBOUR" PLAYBACK_KITCHEN="$KITCHEN" PLAYBACK_WAVES="$WAVES" \
PLAYBACK_REVIEW_COPY="$RC" PLAYBACK_MOTION_HASH="$MH" \
SHOTS_DIR="${SHOTS_DIR:-$H/screenshots}" node web/scripts/playback-e2e.mjs
exit 0
