#!/usr/bin/env bash
# End-to-end run of live takes and script versions (D85, D86) in a real browser,
# on a throwaway installation: a fresh home and local test database, real fixture
# footage, Chromium's fake microphone fed with known speech (macOS say, lines at
# known places with silence between them) and a fake `claude` that fits the
# script. It checks that a live take lands where it was spoken (independently of
# MetaRush: the page taps the same microphone and logs the playhead), that each
# line's start point moves to where it was said, that no line is cut inside a
# word, that "Snap lines to their planned spots" works, that a re-recorded line
# replaces only its stretch, that the teleprompter cues pinned lines on time, and
# the fit flow: the diff, a line kept, both versions switchable, the original
# kept through reload and the cut sheet. Screenshots: desktop and phone, dark and
# light.
#
#   scripts/voiceover-live-e2e.sh            # needs scripts/dev-mongo.sh running and tests/fixtures/gen
#   KEEP=1 scripts/voiceover-live-e2e.sh     # keep the home, database and server for a look afterwards
#   METARUSH_LIVE_PORT=4803 SKIP_BUILD=1 scripts/voiceover-live-e2e.sh
#
# The throwaway database is dropped on exit through store.DropTestDatabase.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_LIVE_PORT:-4803}"
H="${METARUSH_LIVE_HOME:-$ROOT/.agent-home/live-e2e-$(date +%s)}"; DB="metarush-test-live$(date +%s)"
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
[ -f tests/fixtures/gen/interview-a.mov ] || scripts/make-fixtures.sh
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation with a fake claude"
mkdir -p "$H"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --allow-unsafe-secret-path --db-name "$DB" --workspace-name "Live takes test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
go build -o "$H/claude" ./internal/assistant/fakeclaude/cmd/fakeclaude

step "footage and a cut"
P=$(MR project create "Garden story" --tone warm --json | J 'd["data"]["project"]["id"]')
MR ingest tests/fixtures/gen/interview-a.mov tests/fixtures/gen/broll-c.mp4 --project "$P" --wait --json \
  | J '"ingested=%d jobs=%s" % (d["data"]["ingest"]["ingested"], sorted({j["state"] for j in d["data"]["jobs"]}))'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="interview-a.mov"][0]')
B=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="broll-c.mp4"][0]')
cat > "$H/cut.json" <<EOF
{"projectId":"$P","instruction":"first cut","rationale":"A short garden story.",
 "spec":{"schemaVersion":1,"title":"Garden story","output":{"preset":"landscape-1080p","fps":30,"platform":"youtube"},
  "clips":[{"id":"kitchen","label":"Kitchen","assetId":"$A","in":0.4,"out":9.0},
           {"id":"garden","label":"Garden","assetId":"$B","in":0,"out":6,"framing":{"mode":"fit"}},
           {"id":"payoff","label":"Payoff","assetId":"$A","in":18.0,"out":26.0}]}}
EOF
TOK=$(MR edit check --plan "$H/cut.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$H/cut.json" --confirm "$TOK" --json | J '"cut r%d" % d["data"]["revision"]["number"]'

step "known speech for the fake microphone"
# Take A: the three lines, each after a known silence; then a long silence (the
# fake device loops the file; the test is over long before it comes round).
LINES=("Every morning starts in the kitchen." "Then the garden wakes up." "And by noon, everything is green.")
printf '%s [[slnc 1600]] ' "${LINES[@]}" > "$H/read-a.txt"
say -v Samantha -r 165 -f "$H/read-a.txt" -o "$H/read-a.aiff"
ffmpeg -hide_banner -loglevel error -y -f lavfi -i anullsrc=r=48000:cl=mono -i "$H/read-a.aiff" -filter_complex "[0]atrim=0:6.0[s];[1]aformat=sample_rates=48000:channel_layouts=mono[r];[s][r]concat=n=2:v=0:a=1,apad=pad_dur=70" -ac 1 -ar 48000 -c:a pcm_s16le "$H/take-a.wav"
# Take B: line 2 read again (another voice), for a re-recorded stretch.
say -v Daniel -r 160 -o "$H/read-b.aiff" "${LINES[1]}"
ffmpeg -hide_banner -loglevel error -y -f lavfi -i anullsrc=r=48000:cl=mono -i "$H/read-b.aiff" -filter_complex "[0]atrim=0:5.0[s];[1]aformat=sample_rates=48000:channel_layouts=mono[r];[s][r]concat=n=2:v=0:a=1,apad=pad_dur=70" -ac 1 -ar 48000 -c:a pcm_s16le "$H/take-b.wav"

# The fake claude: asked to fit the script, it writes a fitted version (line 1 tied to
# the kitchen shot, line 2 trimmed and tied to the garden shot, line 3 to the payoff)
# and asks to use it; the card shows both versions side by side.
cat > "$H/claude.json" <<'EOF'
{"turns":[{"match":"Fit the narration script","costUsd":0.002,"steps":[
  {"text":"I'll look at the shots and fit your lines to them."},
  {"tool":{"name":"Bash","input":{"command":"metarush voiceover script show --revision {{head}} --json"},"result":"{\"ok\":true,\"data\":{}}"}},
  {"write":{"name":"fitted-script.json","content":"{\"note\":\"Each line now starts on the shot it describes; line 2 is shorter so it lands with the garden.\",\"lines\":[{\"id\":\"line-1\",\"text\":\"Every morning starts in the kitchen.\",\"anchor\":{\"kind\":\"clip\",\"clipId\":\"kitchen\",\"offset\":0.4},\"reason\":\"Starts just after the kitchen shot opens.\"},{\"id\":\"line-2\",\"text\":\"The garden wakes up.\",\"at\":\"0:09.4\",\"reason\":\"Trimmed so it lands right as the garden appears.\"},{\"id\":\"line-3\",\"text\":\"And by noon, everything is green.\",\"anchor\":{\"kind\":\"clip\",\"clipId\":\"payoff\",\"offset\":0.5},\"reason\":\"Tied to the payoff shot.\"}]}"}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush voiceover script fit --propose {{proposals}}/fitted-script.json --revision {{head}} --json"}}},
  {"text":"I've put the fitted version on a card: review it line by line. Your original script stays as you wrote it."}
]}]}
EOF

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_CLAUDE="$H/claude" METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$H/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" \
METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
LIVE_PROJECT="Garden story" LIVE_TAKE_A="$H/take-a.wav" LIVE_TAKE_B="$H/take-b.wav" \
LIVE_SHOTS="${LIVE_SHOTS:-$H/screenshots}" node web/scripts/voiceover-live-e2e.mjs
exit 0
