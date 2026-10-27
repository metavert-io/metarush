#!/usr/bin/env bash
# delivery-e2e.sh — delivery notes, captions in other languages, whose Claude
# answers and the receipt in a real browser (D237–D241), on a throwaway
# installation: an interview transcribed on this computer, a cut with
# captions, and a fake `claude` signed in to a Claude Max account that writes
# the delivery notes and translates the captions into Spanish (each a card the
# person keeps). Then the version's page (delivery notes kept and edited by
# hand, the Spanish captions marked machine-translated, the version's cost at
# cost), the project's Receipt tab, Settings → Claude Code's "Whose Claude
# answers" and the conversation saying who answered. Screenshots: desktop and
# phone, dark and light. Needs ffmpeg (and macOS `say` for the fixtures).
#
#   scripts/delivery-e2e.sh                 # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/delivery-e2e.sh          # keep the home, database and dashboard afterwards
#   METARUSH_E2E_PORT=5411 SKIP_BUILD=1 METARUSH_E2E_DIR=<empty folder> scripts/delivery-e2e.sh
#
# Ports 5410–5419 are this run's. The throwaway database is dropped on exit through
# scripts/droptestdb (store.DropTestDatabase). Fakes only: nothing is sent to a paid service.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_E2E_PORT:-5410}"
BASE="${METARUSH_E2E_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/delivery-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-delivery$(date +%s)"
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
[ -f tests/fixtures/gen/interview-a.mov ] || scripts/make-fixtures.sh >/dev/null
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation with a fake claude signed in to a Claude Max account"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Delivery test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
grep -q '^assistant:' "$H/config.yaml" || printf 'assistant:\n  holdNews: false\n' >> "$H/config.yaml"
go build -o "$BASE/claude" ./internal/assistant/fakeclaude/cmd/fakeclaude
echo '{"loggedIn": true, "authMethod": "claude.ai", "email": "host@example.com", "subscriptionType": "max"}' > "$BASE/claude.auth.json"

# No review copies by themselves (D187): this run renders nothing.
MR render follow off --json >/dev/null

step "an interview, transcribed on this computer; a cut with captions"
P=$(MR project create "Garden story" --json | J 'd["data"]["project"]["id"]')
MR ingest tests/fixtures/gen/interview-a.mov --project "$P" --wait --json | J '"added=%d jobs=%s" % (d["data"]["ingest"]["ingested"], sorted({j["state"] for j in d["data"]["jobs"]}))'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="interview-a.mov"][0]')
MR asset update "$A" --title "Interview" --json >/dev/null
cat > "$BASE/plan.json" <<EOF
{"projectId":"$P","instruction":"The garage and the question, captioned","spec":{"schemaVersion":1,"title":"The question",
 "output":{"preset":"landscape-1080p","fps":30,"platform":"youtube"},
 "clips":[{"id":"start","assetId":"$A","in":0,"out":6.6},{"id":"question","assetId":"$A","in":16.2,"out":20.9}],
 "captions":{"enabled":true,"style":"clean","position":"lower","maxWordsPerLine":5}}}
EOF
TOK=$(MR edit check --plan "$BASE/plan.json" --json | J 'd["data"]["confirmToken"]')
R1=$(MR edit apply --plan "$BASE/plan.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
echo "version 1: $R1"
MR captions lines "$P" --to es --json > "$BASE/lines.json"
J '"lines to translate: %d" % len(d["data"]["lines"])' < "$BASE/lines.json"

# The fake claude: asked for delivery notes, it writes them; asked to translate
# the captions, it writes the Spanish lines (stand-ins: "ES " before each line).
# Keeping either is refused for it, so each becomes a card.
python3 -I - "$BASE/lines.json" "$BASE/claude.json" "$P" <<'PY'
import json, sys
lines = json.load(open(sys.argv[1]))["data"]["lines"]
p = sys.argv[3]
es = {"language": "es", "lines": [{"n": l["n"], "source": l["source"], "text": "ES " + l["source"]} for l in lines]}
notes = "# The question in the garage\n\n## Other titles\n- What we built\n- A garage and a question\n\n## Description\nAn interview about building things in a garage.\n\n## Chapters\n0:00 The garage\n0:06 The question\n\n## Show notes\nWho speaks, and what about.\n"
script = {"turns": [
  {"match": "delivery notes", "costUsd": 0.002, "steps": [
    {"tool": {"name": "Bash", "input": {"command": f"metarush captions file {p} --version-words --format vtt --json"}, "result": "{\"ok\":true,\"data\":{}}"}},
    {"write": {"name": "notes.md", "content": notes}},
    {"tool": {"name": "Bash", "denied": True, "input": {"command": f"metarush deliver notes set {p} --from {{{{proposals}}}}/notes.md --by claude --json"}}},
    {"text": "I wrote a title, two others to choose from, a description, chapters and show notes; they're on a card for you to keep."}]},
  {"match": "Translate", "costUsd": 0.002, "steps": [
    {"tool": {"name": "Bash", "input": {"command": f"metarush captions lines {p} --to es --missing --json"}, "result": "{\"ok\":true,\"data\":{}}"}},
    {"write": {"name": "es.json", "content": json.dumps(es, ensure_ascii=False)}},
    {"tool": {"name": "Bash", "denied": True, "input": {"command": f"metarush captions translate {p} --to es --from {{{{proposals}}}}/es.json --json"}}},
    {"text": "The Spanish captions are on a card, marked as machine-translated."}]}]}
json.dump(script, open(sys.argv[2], "w"))
PY

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_CLAUDE="$BASE/claude" METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run"
SHOTS="${DELIVERY_SHOTS:-$BASE/screenshots}"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
DELIVERY_PROJECT="$P" DELIVERY_REVISION="$R1" DELIVERY_SHOTS="$SHOTS" node web/scripts/delivery-e2e.mjs

step "what the browser did, from the CLI"
MR deliver notes show "$P"
MR captions languages "$P"
MR project receipt "$P"
MR assistant spending --project "$P"
echo "screenshots: $SHOTS"
exit 0
