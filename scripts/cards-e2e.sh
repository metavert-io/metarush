#!/usr/bin/env bash
# cards-e2e.sh — the Claude Code panel's conversation and sound cards in a real
# browser (D111), on a throwaway installation: a Riverside call imported as a
# conversation, cut and looked over, a music bed, and a fake `claude` that asks
# for the changes. Then the dashboard's plain-words pass over the same
# workspace, whose clips carry Riverside's own file names (people's words, D113),
# and Claude's recipe cards with nothing to pay (D211, web/scripts/recipes-cards-e2e.mjs:
# the short saved as a draft, made current, run). Screenshots: desktop and phone,
# dark and light.
#
#   scripts/cards-e2e.sh                   # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/cards-e2e.sh            # keep the home, database and dashboard afterwards
#   METARUSH_E2E_PORT=4923 SKIP_BUILD=1 METARUSH_E2E_DIR=<empty folder> scripts/cards-e2e.sh
#
# The throwaway database is dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_E2E_PORT:-4923}"
BASE="${METARUSH_E2E_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/catchup-cards-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-cards$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SRV=""
WEB=""
cleanup() {
  [ -n "$WEB" ] && kill "$WEB" 2>/dev/null || true
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
[ -f tests/fixtures/gen/music-bed.m4a ] || scripts/make-fixtures.sh >/dev/null
scripts/make-conversation-fixtures.sh >/dev/null
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation with a fake claude"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Cards test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
# This run checks the notes Claude is told at once (D142); holding them for the person's next message (D181,
# the default) has its own test (TestNewsWaitsForTheNextMessage).
grep -q '^assistant:' "$H/config.yaml" || printf 'assistant:\n  holdNews: false\n' >> "$H/config.yaml"
go build -o "$BASE/claude" ./internal/assistant/fakeclaude/cmd/fakeclaude

step "a call from Riverside: lined up, cut and looked over; a music bed"
P=$(MR project create "Garden show" --tone warm --json | J 'd["data"]["project"]["id"]')
RS=tests/fixtures/gen/conversation/riverside
MR ingest "$RS"/riverside_maya_raw-video-cfr_garden_show_0001.mp4 "$RS"/riverside_sam_raw-video-cfr_garden_show_0002.mp4 tests/fixtures/gen/music-bed.m4a \
  --project "$P" --wait --json | J '"added=%d jobs=%s" % (d["data"]["ingest"]["ingested"], sorted({j["state"] for j in d["data"]["jobs"]}))'
MR conversation import "$RS" --project "$P" --name "Garden call" --json | J '"conversation: " + d["data"]["summary"]'
MR conversation cut "Garden call" --project "$P" --style podcast --apply --json > "$BASE/cut.json"; J '"cut: " + d["data"]["result"]["summary"]' < "$BASE/cut.json"
MR conversation cleanup "Garden call" --json | J '"clean-up: " + d["data"]["summary"]'
MUSIC=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="music-bed.m4a"][0]')
MR asset update "$MUSIC" --title "Music bed" --json >/dev/null

# The fake claude: asked to clean up the call and fix the sound, it reads, then asks
# for the changes (each refused for it, so each becomes a card).
cat > "$BASE/claude.json" <<EOF
{"turns":[{"match":"Clean up the call","costUsd":0.003,"steps":[
  {"text":"I'll look at the call and the sound first."},
  {"tool":{"name":"Bash","input":{"command":"metarush conversation show \"Garden call\" --project $P --json"},"result":"{\"ok\":true,\"data\":{}}"}},
  {"tool":{"name":"Bash","input":{"command":"metarush conversation moments \"Garden call\" --count 3 --json"},"result":"{\"ok\":true,\"data\":{}}"}},
  {"tool":{"name":"Bash","input":{"command":"metarush sound show --project $P --json"},"result":"{\"ok\":true,\"data\":{}}"}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush conversation cut \"Garden call\" --project $P --style reactions --json"}}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush conversation decide \"Garden call\" open --accept --json"}}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush conversation angle --project $P --at 0:31 --member Sam --why 'His answer about the bucket.' --json"}}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush conversation short \"Garden call\" --from 0:24 --to 0:36 --json"}}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush sound levels --project $P --speaker Sam --gain 3 --json"}}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush sound enhance Maya --project $P --json"}}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush sound enhance Sam --project $P --mode studio --json"}}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush sound enhance Maya --project $P --mode cloud --json"}}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush sound music add \"Music bed\" --project $P --json"}}},
  {"text":"I've put the changes on cards for you: the reaction-heavy cut, the clean-up, Sam's angle at 0:31, a short, Sam a little louder, clearer voices and the music bed."}
]}]}
EOF

step "for the panel run (D140–D144): a short project, three stills, a page on this computer, a graphic"
P2=$(MR project create "Short test" --json | J 'd["data"]["project"]["id"]')
CLIP=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"].startswith("riverside_maya")][0]')
MR asset link "$CLIP" --project "$P2" --json >/dev/null
python3 -I -c 'import json,sys; json.dump({"projectId":sys.argv[2],"instruction":"first cut","spec":{"schemaVersion":1,"title":"Short","output":{"preset":"vertical-1080p","fps":30},"clips":[{"id":"open","assetId":sys.argv[3],"in":1,"out":5}]}}, open(sys.argv[1],"w"))' \
  "$BASE/short.plan.json" "$P2" "$CLIP"
TOK=$(MR edit check --plan "$BASE/short.plan.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$BASE/short.plan.json" --confirm "$TOK" --json | J '"version %d of the short project" % d["data"]["revision"]["number"]'
MR cut show "$P2" --json > "$BASE/short-sheet.json"
MR footage frames "$CLIP" --at 1.5,2.5,3.5 --json > "$BASE/frames.json"
PAGE_PORT="${METARUSH_E2E_PAGE_PORT:-$((PORT + 1))}"
mkdir -p "$BASE/site"
printf '<!doctype html><meta charset="utf-8"><title>Acme garden club</title><h1>Acme garden club</h1><p>Our logo, our colours, our story.</p>\n' > "$BASE/site/brand.html"
python3 -I -m http.server "$PAGE_PORT" --bind 127.0.0.1 --directory "$BASE/site" > "$BASE/site.log" 2>&1 & WEB=$!
# The fake claude for the panel run: it looks at the stills (and the rest of a long
# result), writes a graphic, asks to check it, to bring the page in and to leave two
# notes, and asks a question; answered, it writes a tighter cut to save.
python3 -I - "$BASE/claude.json" "$BASE/frames.json" "$BASE/short-sheet.json" video/render/fixtures/motion/route-map "$P2" "http://127.0.0.1:$PAGE_PORT/brand.html" "$HOME" <<'PY'
import json, os, sys
script_path, frames_path, sheet_path, motion_dir, project, page, home = sys.argv[1:8]
script = json.load(open(script_path))
frames = [f["path"] for f in json.load(open(frames_path))["data"]["frames"]]
sheet = json.load(open(sheet_path))["data"]["sheet"]
shorter = sheet.replace("0:01 → 0:05", "0:01 → 0:04", 1)
assert shorter != sheet, sheet
def bash(cmd, denied=True):
    return {"tool": {"name": "Bash", "denied": denied, "input": {"command": cmd}}}
steps = [{"text": "I'll look at the opening first."}]
steps += [{"tool": {"name": "Read", "input": {"file_path": p}, "result": "[image]"}} for p in frames]
steps.append({"tool": {"name": "Read", "input": {"file_path": os.path.join(home, ".claude/projects/-panel/s1/tool-results/toolu_01PanelE2eSpill.txt")}, "result": "…"}})
for name in sorted(os.listdir(motion_dir)):
    steps.append({"write": {"name": "motion/lower-bar/" + name, "content": open(os.path.join(motion_dir, name)).read()}})
steps += [
    bash("metarush motion check {{proposals}}/motion/lower-bar --project " + project + " --json"),
    bash("metarush resources add " + page + " --project " + project + " --json"),
    bash('metarush comment add "The opening works." --project ' + project + " --json"),
    bash('metarush comment add "Try the name bar next." --project ' + project + " --json"),
    {"write": {"name": "parts.question.json", "content": json.dumps({"question": "Which parts should get the name bar?",
        "choices": ["The opening", "The middle", "The end"], "multiple": True, "other": True,
        "why": "The bar covers the names the call app shows."})}},
    {"text": "I'm checking the graphic and bringing your page in; two notes and a question are on cards for you."},
]
script["turns"] += [
    {"match": "brand.html", "costUsd": 0.002, "steps": steps},
    {"match": "Done by itself", "steps": [{"text": "The graphic passed its check and your page is in."}]},
    {"match": "This message answers your question card", "steps": [
        {"write": {"name": "tighter.cut.md", "content": shorter}},
        {"text": "Here is a tighter opening for you to save."}]},
    # D185/D186: a reply with code to fold away, a card to send back with Not quite, one Auto runs, one left waiting.
    {"match": "Show me the settings", "steps": [{"text": "Here they are:\n\n```json\n{\n  \"width\": 1080,\n  \"height\": 1920,\n  \"fps\": 30,\n  \"captions\": true,\n  \"music\": \"calm\",\n  \"length\": 30,\n  \"shape\": \"vertical\"\n}\n```\n\nThat's the whole shape of the short."}]},
    {"match": "One more note", "steps": [bash('metarush comment add "Shorter intro." --project ' + project + " --json"), {"text": "A note for you."}]},
    {"match": "Not quite:", "steps": [{"text": "I'll make a new one with that change."}]},
    {"match": "Note while on Auto", "steps": [bash('metarush comment add "Warmer music." --project ' + project + " --json"), {"text": "Leaving a note."}]},
    {"match": "A last note", "steps": [bash('metarush comment add "Brighter colours." --project ' + project + " --json"), {"text": "One more note for you."}]},
    # D211: the short saved as a recipe (a draft), made current, and a run with nothing to pay.
    {"match": "Save the short as a recipe", "steps": [bash("metarush recipe save --from " + project + " --name short-test --title 'Short test' --hint 'Open on the hook.' --json"), {"text": "It's on a card: it saves a draft."}]},
    {"match": "Make the recipe current", "steps": [bash("metarush recipe publish short-test --json"), {"text": "It's on a card for you."}]},
    {"match": "Run the recipe", "steps": [bash("metarush recipe run short-test --note 'a test run' --json"), {"text": "A run is on a card."}]},
]
json.dump(script, open(script_path, "w"))
PY

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_PAGES_ALLOW_LOCAL=1 METARUSH_CLAUDE="$BASE/claude" METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run: the cards"
SHOTS="${CARDS_SHOTS:-$BASE/screenshots}"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
CARDS_PROJECT="$P" CARDS_SHOTS="$SHOTS" node web/scripts/cards-e2e.mjs

step "the browser run: the panel (navigator, lightbox, combined approvals, questions, safe steps, finish notes)"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
PANEL_PROJECT="$P2" PANEL_OTHER_PROJECT="$P" PANEL_PAGE="http://127.0.0.1:$PAGE_PORT/brand.html" PANEL_SHOTS="$SHOTS/panel" node web/scripts/panel-e2e.mjs

step "the plain-words pass over the same workspace, the panel's conversation included"
PANEL_CONV=$(MR assistant conversations list --project "$P2" --json | J 'd["data"]["conversations"][0]["id"]')
LINK=$(./bin/metarush --home "$H" dashboard --print-link --no-open | grep -o 'http[^ ]*auth?t=[a-f0-9]*' | head -1)
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_SIGNIN_URL="$LINK" PLAIN_SHOTS="$SHOTS/plain" PLAIN_CONVERSATION="$PANEL_CONV" PLAIN_CONVERSATION_PROJECT="$P2" \
  node web/scripts/plain-words-e2e.mjs

step "the browser run: Claude's recipe cards with nothing to pay (D211): a save as a draft, making it current, a run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
RECIPES_CARDS_SCENARIO=free RECIPES_PROJECT="$P2" RECIPES_CARDS_SHOTS="$SHOTS/recipes" node web/scripts/recipes-cards-e2e.mjs
exit 0
