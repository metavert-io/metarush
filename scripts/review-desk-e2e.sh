#!/usr/bin/env bash
# review-desk-e2e.sh — the review desk in a real browser (D248–D251), on a
# throwaway installation: a project with a first cut, and two fake AI apps
# (scripts/mcp-agents.py over the official MCP Python SDK) connected to
# `metarush mcp serve --propose` as Codex and Cursor. Codex proposes a shorter
# first clip (a draft version beside the cut), Cursor a house rule; both are
# refused approving. In the browser a person finds both in Awaiting your
# review, opens Codex's draft (what changes, why, checks, cost, what's
# waiting), says Not quite with a note — Codex hears it on its next call and
# makes a new draft in the old one's place — approves that one (it goes into
# the cut, approved) and Cursor's house rule (made as the person), and the
# review copy MetaRush queues by itself for the draft's version shows as being
# made (no worker here: nothing renders). Screenshots: desktop dark and light,
# phone.
#
#   scripts/review-desk-e2e.sh               # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/review-desk-e2e.sh        # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/review-desk-e2e.sh  # set it up and leave the dashboard running
#   METARUSH_E2E_PORT=5450 SKIP_BUILD=1 METARUSH_E2E_DIR=<empty folder> REVIEW_SHOTS=<folder> scripts/review-desk-e2e.sh
#
# Ports 5450–5459 belong to this run. The MCP Python SDK is set up in a
# throwaway virtual environment (METARUSH_MCP_VENV reuses one). The throwaway
# database is dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_E2E_PORT:-5450}"
BASE="${METARUSH_E2E_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/review-desk-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-reviewdesk$(date +%s)"
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
  [ -z "${METARUSH_E2E_DIR:-}" ] && rm -rf "$BASE" || true
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

step "a throwaway installation"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Review desk test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"

step "a project with a first cut"
mkdir -p "$BASE/media"
ffmpeg -v error -y -f lavfi -i "testsrc2=size=640x360:rate=30:duration=6" -f lavfi -i "sine=frequency=330:sample_rate=48000:duration=6" \
  -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest "$BASE/media/garden-walk.mp4"
P=$(MR project create "Garden walk" --json | J 'd["data"]["project"]["id"]')
MR ingest "$BASE/media/garden-walk.mp4" --project "$P" --no-transcribe --wait --json | J '"added=%d" % d["data"]["ingest"]["ingested"]'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"]][0]')
cat > "$BASE/plan.json" <<EOF
{"projectId":"$P","instruction":"A first cut","spec":{"schemaVersion":1,"title":"Garden walk","output":{"preset":"landscape-1080p","fps":30},
 "clips":[{"id":"walk","assetId":"$A","in":0,"out":5}]}}
EOF
TOK=$(MR edit check --plan "$BASE/plan.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$BASE/plan.json" --confirm "$TOK" --json | J '"version %d of the garden walk" % d["data"]["revision"]["number"]'

step "two AI apps that may only propose: Codex and Cursor"
export METARUSH_MCP_VENV="${METARUSH_MCP_VENV:-$BASE/venv}"  # the MCP Python SDK, set up once for every phase
AGENTS=(scripts/mcp-agents.sh --home "$H" --project "$P")
"${AGENTS[@]}" --phase propose | tee "$BASE/agents.json"
MR draft list --all
MR revision waiting

step "the dashboard on 127.0.0.1:$PORT (no worker: review copies are queued, nothing renders)"
METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve --no-worker --no-watch > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run: the review desk"
SHOTS="${REVIEW_SHOTS:-$BASE/screenshots}"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
REVIEW_PROJECT="$P" REVIEW_SHOTS="$SHOTS" REVIEW_AGENTS="${AGENTS[*]}" node web/scripts/review-desk-e2e.mjs

step "what the browser decided, from the command line"
MR draft list --all
MR revision list "$P"
MR revision waiting
exit 0
