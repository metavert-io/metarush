#!/usr/bin/env bash
# recipes-e2e.sh — the Library's Recipes tab in a real browser (D201–D208), on a
# throwaway installation that talks only to fakes: a fake ElevenLabs
# (internal/testutil/fakeeleven) and a fake Gemini (internal/testutil/fakevideogen),
# no real service, no real key. A project "Garden story" has a cut that worked: a
# generated shot of a paper boat, a moment of footage, an end card with a headline
# and two narration lines in an AI voice, approved, with a review copy. The browser
# run (web/scripts/recipes-e2e.mjs):
#   saves the project as a recipe from its "…" menu (the proposal: what changes each
#   run and what stays; the shot's words moved to change; a project per run), finds
#   it in the Library's Recipes tab and opens its page; plans a run with a new
#   headline, another voice and new words for the shot, opens the estimate ("about
#   $0.13", what it draws on) and agrees to it in the styled question; watches the
#   run go, wait for a person (a budget set meanwhile on the run's new project) and
#   go on; follows the links to the new project and version, which say "Made with
#   … run 1"; turns estimates off in Settings → Preferences and runs it again with
#   another voice (no money question, only the line that budgets still apply); and
#   checks the plan's steps say who does each (new footage: Claude chooses the
#   moments). To-dos, the bell and the activity bar show the waiting run. Then (D212)
#   two runs side by side, what a draft version changed, and Start from a recipe on
#   Home. Desktop and 390 px phone, dark and light, with screenshots; no native browser
#   dialog, no sideways scrolling, plain words. Last, Claude's recipe cards in the panel
#   (D211, web/scripts/recipes-cards-e2e.mjs) with a fake claude: a run with its plan and
#   estimate that Auto never starts, Not quite…, the agreement, a save, a change and
#   making a version current. Then Tier 3 (web/scripts/recipes-tier3-e2e.mjs): two runs whose
#   boat a person shortened become a suggested change (D217) saved as a draft; planning by
#   itself (D218) turned on, a clip joining the project makes a plan and rings the bell, and no
#   run starts though Auto is on and estimates are off; Claude's cards to keep a run's first
#   picks, save a recipe sheet and save the suggested changes.
#
#   scripts/recipes-e2e.sh                  # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/recipes-e2e.sh           # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/recipes-e2e.sh     # set it up and leave the dashboard running
#   METARUSH_RECIPES_PORT=5390 SKIP_BUILD=1 RECIPES_SHOTS=<folder> scripts/recipes-e2e.sh
#
# Ports: the dashboard on 5390, the fake ElevenLabs on 5391, the fake Gemini on 5392
# (METARUSH_RECIPES_PORT moves all three). The throwaway database is dropped on exit
# through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_RECIPES_PORT:-5390}"; EFAKE=$((PORT + 1)); GFAKE=$((PORT + 2))
BASE="${METARUSH_RECIPES_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/recipes-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-recipes$(date +%s)"
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
  [ -z "${METARUSH_RECIPES_DIR:-}" ] && rm -rf "$BASE" || true
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

step "a throwaway installation that talks only to the fake ElevenLabs and the fake Gemini"
mkdir -p "$H/state" "$BASE/media"
# --video-dir: renders (the review copies) use this checkout's renderer.
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Recipes test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
# The fakes' addresses go in the home's .env before any key exists, so every process of this home (the CLI, the
# dashboard and its worker) sends requests to them, never to ElevenLabs or Google. Keys made at run time: each fake
# accepts only its own.
python3 -I -c "import secrets;print('sk'+'_'+secrets.token_hex(20))" > "$H/state/fake-eleven.key"
python3 -I -c "import secrets;print('gk-'+secrets.token_hex(20))" > "$H/state/fake-gemini.key"
chmod 600 "$H/state/fake-eleven.key" "$H/state/fake-gemini.key"
{
  printf 'METARUSH_MONGODB_URI=%s\n' "$URI"
  printf 'METARUSH_ELEVENLABS_BASE_URL=http://127.0.0.1:%s\n' "$EFAKE"
  printf 'METARUSH_GEMINI_BASE_URL=http://127.0.0.1:%s\n' "$GFAKE"
} > "$H/.env"; chmod 600 "$H/.env"
go build -o "$BASE/fakeeleven" ./internal/testutil/fakeeleven/cmd/fakeeleven
go build -o "$BASE/fakevideogen" ./internal/testutil/fakevideogen/cmd/fakevideogen
"$BASE/fakeeleven" -addr "127.0.0.1:$EFAKE" -key-file "$H/state/fake-eleven.key" > "$BASE/fakeeleven.log" 2>&1 &
"$BASE/fakevideogen" -addr "127.0.0.1:$GFAKE" -key-file "$H/state/fake-gemini.key" > "$BASE/fakevideogen.log" 2>&1 &
for port in "$EFAKE" "$GFAKE"; do
  for _ in $(seq 1 240); do curl -s -o /dev/null "http://127.0.0.1:$port/__calls" && break; sleep 0.25; done
  curl -s -o /dev/null "http://127.0.0.1:$port/__calls" || { echo "the fake on port $port never answered (see $BASE/fake*.log)" >&2; exit 1; }
done
MR providers set elevenlabs --key-stdin --json < "$H/state/fake-eleven.key" | J '"ElevenLabs (the fake): %s" % d["data"]["masked"]'
MR providers set gemini --key-stdin --json < "$H/state/fake-gemini.key" | J '"Google Gemini (the fake): %s" % d["data"]["masked"]'

step "Garden story: footage, a generated shot of a paper boat, more footage in the Library"
P=$(MR project create "Garden story" --json | J 'd["data"]["project"]["id"]')
clip() { # name, picture, tone (Hz), seconds
  ffmpeg -hide_banner -loglevel error -nostdin -y -f lavfi -i "$2=size=640x360:rate=30:duration=$4" -f lavfi -i "sine=frequency=$3:duration=$4:sample_rate=48000" \
    -c:v libx264 -preset veryfast -pix_fmt yuv420p -c:a aac -b:a 96k -shortest "$BASE/media/$1.mp4"
}
clip "garden-walk" testsrc2 440 10
clip "kitchen-chat" smptebars 550 10
MR ingest "$BASE/media/garden-walk.mp4" --project "$P" --no-transcribe --wait --json | J '"in the project: added %d" % d["data"]["ingest"]["ingested"]'
MR ingest "$BASE/media/kitchen-chat.mp4" --no-transcribe --wait --json | J '"in the Inbox: added %d" % d["data"]["ingest"]["ingested"]'
id_of() { MR asset list --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="'"$1"'"][0]'; }
WALK=$(id_of garden-walk.mp4); KITCHEN=$(id_of kitchen-chat.mp4)
MR asset update "$WALK" --title "Garden walk" >/dev/null
MR asset update "$KITCHEN" --title "Kitchen chat" >/dev/null
GID=$(MR generate shot "a red paper boat on a pond" --project "$P" --seconds 3 --resolution 360p --max-cost 0.5 --wait --json </dev/null | J 'd["data"]["generations"][0]["id"]')
BOAT=$(MR generate show "$GID" --json | J 'd["data"]["clips"][0]["assetId"]')
MR asset update "$BOAT" --title "Paper boat" >/dev/null
echo "the shot: made by the fake Gemini"

step "the cut that worked: the boat, a moment of the walk, an end card, two narration lines (the browser run gives them the AI voice)"
MR providers settings elevenlabs --voice Ada --json >/dev/null  # the narration voice of the default Content Settings
cat > "$BASE/cut.json" <<EOF
{"projectId":"$P","instruction":"a garden story to make again","spec":{"schemaVersion":1,"title":"Garden story","output":{"preset":"landscape-1080p","fps":30},
 "clips":[{"id":"boat","assetId":"$BOAT","in":0,"out":3,"label":"the boat"},{"id":"walk","assetId":"$WALK","in":1,"out":5,"label":"the walk"}],
 "overlays":[{"id":"end","template":"endcard.cta","at":4.5,"duration":2.5,"params":{"headline":"Grow with us","cta":"Visit the garden"}}],
 "voiceover":{"beats":[{"id":"line-1","text":"Meet the paper boat on our pond.","anchor":{"kind":"clip","clipId":"boat","offset":0.2}},
  {"id":"line-2","text":"Then walk the garden with us.","anchor":{"kind":"after"}}]}}}
EOF
TOK=$(MR edit check --plan "$BASE/cut.json" --json | J 'd["data"]["confirmToken"]')
R1=$(MR edit apply --plan "$BASE/cut.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
echo "the cut: version 1"

step "a fake claude for the panel's recipe cards (D211): it asks for runs, a save, a change and making a version current"
go build -o "$BASE/claude" ./internal/assistant/fakeclaude/cmd/fakeclaude
cat > "$BASE/claude.json" <<EOF
{"default":{"steps":[{"text":"Noted."}]},
 "turns":[
  {"match":"Make the garden story again for the summer","steps":[
    {"tool":{"name":"Bash","input":{"command":"metarush recipe show garden-story --json"},"result":"{\"ok\":true,\"data\":{}}"}},
    {"tool":{"name":"Bash","input":{"command":"metarush recipe plan garden-story --input 'headline=Summer is here' --input 'boat-prompt=a yellow paper boat at sunset' --json"},"result":"{\"ok\":true,\"data\":{\"ready\":true,\"estimate\":{\"about\":\"about \$0.13\"}}}"}},
    {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush recipe run garden-story --input 'headline=Summer is here' --input 'boat-prompt=a yellow paper boat at sunset' --note 'the summer one' --max-cost 50 --json"}}},
    {"text":"I planned it with your summer headline and a yellow boat at sunset: it's on a card for you to start."}]},
  {"match":"Not quite:","steps":[
    {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush recipe run garden-story --input 'headline=Summer days' --input 'boat-prompt=a yellow paper boat at sunset' --json"}}},
    {"text":"Here it is with that headline."}]},
  {"match":"One more on Auto","steps":[
    {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush recipe run garden-story --input headline=Autumn --input voice=Bram --json"}}},
    {"text":"Another run is on a card."}]},
  {"match":"Save it for kids","steps":[
    {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush recipe save --from $P --title 'Garden story for kids' --hint 'Keep it gentle.' --json"}}},
    {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush recipe update garden-story --hint 'End on the garden.' --note 'a calmer ending' --json"}}},
    {"text":"A save and a change are on cards."}]},
  {"match":"Make the new version current","steps":[
    {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush recipe publish garden-story --json"}}},
    {"text":"It's on a card for you."}]}
 ]}
EOF

step "the dashboard on 127.0.0.1:$PORT (with its worker, which makes the shots, the AI voice and the review copies)"
# Planning by itself (D218) looks, and new files settle, every 2 s here (a minute and two minutes by default).
METARUSH_RECIPE_TRIGGER_SECONDS=2 METARUSH_CLAUDE="$BASE/claude" METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
RECIPES_PROJECT="$P" RECIPES_CUT="$R1" RECIPES_WALK="$WALK" RECIPES_KITCHEN="$KITCHEN" RECIPES_BOAT="$BOAT" \
RECIPES_GEMINI_FAKE="http://127.0.0.1:$GFAKE" RECIPES_ELEVEN_FAKE="http://127.0.0.1:$EFAKE" \
RECIPES_SHOTS="${RECIPES_SHOTS:-$BASE/screenshots}" node web/scripts/recipes-e2e.mjs

step "the browser run: Claude's recipe cards in the panel (D211)"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
RECIPES_CARDS_SCENARIO=priced RECIPES_PROJECT="$P" RECIPES_CARDS_SHOTS="${RECIPES_SHOTS:-$BASE/screenshots}/cards" node web/scripts/recipes-cards-e2e.mjs

step "the browser run: Recipes Tier 3 — suggested changes (D217), planning by itself (D218), Claude's picks, sheet and suggestions cards"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
RECIPES_PROJECT="$P" RECIPES_KITCHEN="$KITCHEN" RECIPES_CLAUDE_SCRIPT="$BASE/claude.json" \
RECIPES_TIER3_SHOTS="${RECIPES_SHOTS:-$BASE/screenshots}/tier3" node web/scripts/recipes-tier3-e2e.mjs

step "what the browser did, from the CLI"
MR recipe list
MR recipe runs garden-story
MR recipe diff garden-story --to current --from 1
MR recipe suggestions garden-story
MR recipe triggers
MR log list --since 1h --channel recipe --json | J '"done by the runs (the recipe channel): %s" % sorted({e["text"] for e in d["data"]["entries"]})[:8]'
exit 0
