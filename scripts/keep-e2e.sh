#!/usr/bin/env bash
# keep-e2e.sh — "Keep this?" cards, attachments and what a project keeps in a
# real browser (D166, docs/keeping.md), on a throwaway installation: a project
# with a short cut made from a generated clip whose picture switches just after
# the clip starts (a split-second flash, D161), and a fake `claude` that, told
# about a garden club, asks to keep a rule, a check, a sign-off and the brief.
# The browser attaches a logo through the message box and types a link (MetaRush's
# own cards), keeps and sets aside cards (a rule card asks where it goes, D193),
# opens the project's Settings tab, adds and ticks things there, trims the flash and approves the cut past the
# sign-off reminder. Screenshots: desktop light and dark, phone.
#
#   scripts/keep-e2e.sh                     # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/keep-e2e.sh              # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/keep-e2e.sh        # set it up and leave the dashboard running
#   METARUSH_E2E_PORT=5373 SKIP_BUILD=1 METARUSH_E2E_DIR=<empty folder> KEEP_SHOTS=<folder> scripts/keep-e2e.sh
#
# The throwaway database is dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_E2E_PORT:-5372}"
BASE="${METARUSH_E2E_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/keep-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-keep$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SRV=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, dashboard http://127.0.0.1:$PORT (pid $SRV)"
    return
  fi
  if [ -n "$SRV" ]; then
    kill "$SRV" 2>/dev/null || true
    # Let it finish writing its activity log first: a late write would make the database again.
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

step "a throwaway installation with a fake claude"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Keep test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
go build -o "$BASE/claude" ./internal/assistant/fakeclaude/cmd/fakeclaude

step "a project with a short cut: a clip whose picture switches 0.15 s after it starts"
mkdir -p "$BASE/media"
# 3 s of one picture, then 3 s of another (a call app switching to the other person), with a tone.
# The two differ in brightness, as two people's pictures do: the switch is found to the frame.
ffmpeg -v error -y -f lavfi -i "color=c=0x101828:s=640x360:r=30:d=3" -f lavfi -i "color=c=0xf5d061:s=640x360:r=30:d=3" \
  -f lavfi -i "sine=frequency=330:sample_rate=48000:duration=6" \
  -filter_complex "[0:v][1:v]concat=n=2:v=1:a=0[v]" -map "[v]" -map 2:a -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest "$BASE/media/garden-call.mp4"
P=$(MR project create "Garden club" --json | J 'd["data"]["project"]["id"]')
MR ingest "$BASE/media/garden-call.mp4" --project "$P" --no-transcribe --wait --json | J '"added=%d jobs=%s" % (d["data"]["ingest"]["ingested"], sorted({j["state"] for j in d["data"]["jobs"]}))'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"]][0]')
MR asset update "$A" --title "Garden call" --json >/dev/null
cat > "$BASE/plan.json" <<EOF
{"projectId":"$P","instruction":"A first cut","spec":{"schemaVersion":1,"title":"Garden club","output":{"preset":"landscape-1080p","fps":30},
 "clips":[{"id":"answer","assetId":"$A","in":2.85,"out":5.6}]}}
EOF
TOK=$(MR edit check --plan "$BASE/plan.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$BASE/plan.json" --confirm "$TOK" --json | J '"version %d of the garden club" % d["data"]["revision"]["number"]'

# The fake claude: told about the garden club, it notes what is worth keeping (each
# `metarush keep …` is refused for it, so each becomes a "Keep this?" card).
cat > "$BASE/claude.json" <<EOF
{"turns":[{"match":"garden club","costUsd":0.002,"steps":[
  {"text":"I'll note what you told me so the next conversation knows it too."},
  {"tool":{"name":"Bash","input":{"command":"metarush project show $P --json"},"result":"{\"ok\":true,\"data\":{}}"}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush keep rule 'First names only on screen' --project $P --json"}}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush keep check 'No split-second flashes of the wrong person at clip edges' --project $P --json"}}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush keep signoff 'Lena OKs the final version' --project $P --json"}}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush keep brief --project $P --audience 'Members and neighbours' --deliverable 'on the website' --deliverable 'social shorts' --json"}}},
  {"text":"I've asked to keep the rule, the check, Lena's sign-off and where the video goes."}
]}]}
EOF

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_CLAUDE="$BASE/claude" METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

# The switch is found by the daemon's worker after the clip's analysis: wait for it (a minute at most).
for _ in $(seq 1 120); do
  [ "$(MR footage switches "$A" --json | J 'd["data"]["state"]')" = "ready" ] && break
  sleep 0.5
done
MR footage switches "$A" --json | J '"switches: " + d["data"]["summary"]'

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run: keep cards, attachments, what the project keeps"
SHOTS="${KEEP_SHOTS:-$BASE/screenshots}"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
KEEP_PROJECT="$P" KEEP_ASSET="$A" KEEP_SHOTS="$SHOTS" node web/scripts/keep-e2e.mjs

step "what the browser kept, from the CLI"
MR keep list --project "$P"
MR project rules "$P"
MR cut signoffs "$P"
exit 0
