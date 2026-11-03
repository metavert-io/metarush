#!/usr/bin/env bash
# start-from-video-e2e.sh — Start from a video you like in a real browser (D398–D404), on
# a throwaway installation with no network: a fake downloader (internal/testutil/fakeytdlp)
# hands over the fixture tests/fixtures/gen/reference-short.mp4 for any link
# (METARUSH_FAKE_YTDLP_MEDIA), and a fake claude asks for the panel's card. The daemon's
# worker looks at the video for real on this computer: its words and voices, cuts, pauses
# and loudness, and on a Mac what's on screen. The browser run
# (web/scripts/recipes-from-video-e2e.mjs):
#   pastes a link in the Recipes tab's Add box: it comes in as a reference, the box follows
#   it while MetaRush looks, then shows what the video does (its sections with times,
#   captions, words on screen, music) and the draft; the draft's sheet holds none of the
#   video's words and says where it came from; the draft's page says what it was drafted
#   from, and a person makes it current; the clip's page opens on What it does with the
#   reference's note (This is mine… asks in a styled question, cancelled here); a cut that
#   plays it is refused in plain words; Home's Start from finds the same draft again;
#   Claude's card in the panel names the video and what it does, and a person starts a
#   second draft from it; the person's own video, chosen again, stays theirs (never a
#   reference); a video file chosen on a phone comes in as a reference too.
#   Desktop dark and light, 390 px phone dark and light, with screenshots; no native
#   browser dialog, no sideways scrolling, plain words.
#
#   scripts/start-from-video-e2e.sh                  # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/start-from-video-e2e.sh           # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/start-from-video-e2e.sh     # set it up and leave the dashboard running
#   SKIP_BUILD=1 FROM_VIDEO_SHOTS=<folder> scripts/start-from-video-e2e.sh
#
# Ports (5760–5769): the dashboard on 5760 (METARUSH_FROM_VIDEO_PORT moves it). The
# throwaway database is dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_FROM_VIDEO_PORT:-5760}"
BASE="${METARUSH_FROM_VIDEO_DIR:-$(mktemp -d "${TMPDIR%/}/start-from-video-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-fromvideo$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SRV=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, dashboard http://127.0.0.1:$PORT (pid $SRV)"
    return
  fi
  if [ -n "$SRV" ]; then
    kill "$SRV" 2>/dev/null || true
    for _ in $(seq 1 60); do kill -0 "$SRV" 2>/dev/null || break; sleep 0.25; done
  fi
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi
step "the fixture: a 26-second vertical video with a hook, three tips, burned-in captions, a title, music and an end card"
scripts/make-fixtures.sh >/dev/null
VIDEO="$ROOT/tests/fixtures/gen/reference-short.mp4"
[ -f "$VIDEO" ] || { echo "no $VIDEO (scripts/make-fixtures.sh)" >&2; exit 1; }

step "a throwaway installation with a fake downloader and a fake claude"
mkdir -p "$H/state" "$BASE/fake-yt-dlp" "$BASE/files"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Start from a video test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
# The links the browser pastes: the daemon's worker runs the fake, which hands over the fixture (never the network).
go build -o "$BASE/fake-yt-dlp/yt-dlp" ./internal/testutil/fakeytdlp/cmd/fake-yt-dlp
export METARUSH_YTDLP="$BASE/fake-yt-dlp/yt-dlp" METARUSH_FAKE_YTDLP_MEDIA="$VIDEO"
# The video file a person chooses: the same video, its own bytes (another clip).
ffmpeg -hide_banner -loglevel error -nostdin -y -i "$VIDEO" -map 0 -c copy -metadata comment="chosen in the browser" "$BASE/files/their-tips.mp4"
# The person's own video, in the Library already: chosen again, it stays theirs (never a reference).
ffmpeg -hide_banner -loglevel error -nostdin -y -f lavfi -i "testsrc2=size=640x360:rate=30:duration=3" -f lavfi -i "sine=frequency=330:duration=3" \
  -shortest -c:v libx264 -preset veryfast -pix_fmt yuv420p -c:a aac "$BASE/files/our-own.mp4"
go build -o "$BASE/claude" ./internal/assistant/fakeclaude/cmd/fakeclaude
echo '{"default":{"steps":[{"text":"Noted."}]},"turns":[]}' > "$BASE/claude.json"  # the browser run adds its turns
P=$(MR project create "Shop tips" --json | J 'd["data"]["project"]["id"]')
MR ingest "$BASE/files/our-own.mp4" --no-transcribe --wait --json | J '"their own video, in the Library: added %d" % d["data"]["ingest"]["ingested"]'

step "the dashboard on 127.0.0.1:$PORT (with its worker: the imports and the looking)"
METARUSH_CLAUDE="$BASE/claude" METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
FROM_VIDEO_PROJECT="$P" FROM_VIDEO_FILE="$BASE/files/their-tips.mp4" FROM_VIDEO_OWN_FILE="$BASE/files/our-own.mp4" FROM_VIDEO_CLAUDE_SCRIPT="$BASE/claude.json" \
FROM_VIDEO_SHOTS="${FROM_VIDEO_SHOTS:-$BASE/screenshots}" node web/scripts/recipes-from-video-e2e.mjs

step "what the browser did, from the CLI"
MR recipe list
REF=$(MR asset list --json | J '[a["id"] for a in d["data"]["assets"] if a.get("reference") and (a.get("source") or {}).get("url")][0]')
MR footage what-it-does "$REF"
MR log list --since 1h --json | J '"in the activity log: %s" % sorted({e["text"] for e in d["data"]["entries"] if "video" in e["text"].lower() or "reference" in e["text"].lower()})[:8]'
exit 0
