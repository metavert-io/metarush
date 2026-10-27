#!/usr/bin/env bash
# leave-e2e.sh — What may leave and ability files in a real browser (D288–D292), on
# throwaway installations that talk to nothing outside this computer. Workspace A has
# two projects: Base (the default's settings, nothing set) and Acme (its own). In the
# browser a person opens Acme's Settings tab, keeps its words, pictures and faces from
# Claude and its voices from AI services in What may leave, with a note, and saves;
# Claude Code in a terminal (CLAUDECODE=1) then can't search Acme's words (the CLI
# shows E_KEPT_HERE); Acme's Receipt says nothing of it left this computer and lists
# what was kept here. Workspace B checks an ability (once in the sandbox, network off)
# and exports it; in A's Library → Abilities a person brings the file in: a draft,
# never approved or set up here, its page saying where it came from and how to check
# it; Export to a file… downloads it again. Desktop and 390 px phone, dark and light,
# with screenshots; no native browser dialog, no sideways scrolling, plain words.
#
#   scripts/leave-e2e.sh                   # needs scripts/dev-mongo.sh running, a sandbox and python3
#   KEEP=1 scripts/leave-e2e.sh            # keep the homes, databases and dashboard afterwards
#   NO_BROWSER=1 scripts/leave-e2e.sh      # set it up and leave the dashboard running
#   METARUSH_LEAVE_PORT=5540 SKIP_BUILD=1 LEAVE_SHOTS=<folder> scripts/leave-e2e.sh
#
# Ports 5540–5549 belong to this run (A's dashboard on 5540; B never serves). The
# throwaway databases are dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. scripts/lib/status.sh
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_LEAVE_PORT:-5540}"
BASE="${METARUSH_LEAVE_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/leave-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; HB="$BASE/home-b"; STAMP="$(date +%s)"
DB="metarush-test-leave$STAMP"; DBB="metarush-test-leaveb$STAMP"
URI="$(scripts/dev-mongo.sh start)"
SRV=""
# The run's exit status is kept by scripts/lib/e2e.sh, which calls this first.
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: homes $H and $HB, databases $DB and $DBB, dashboard http://127.0.0.1:$PORT (pid $SRV)"
    return
  fi
  if [ -n "$SRV" ]; then
    kill "$SRV" 2>/dev/null || true
    for _ in $(seq 1 60); do kill -0 "$SRV" 2>/dev/null || break; sleep 0.25; done
  fi
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
  go run ./scripts/droptestdb "$URI" "$DBB" >/dev/null || true
  [ -z "${METARUSH_LEAVE_DIR:-}" ] && rm -rf "$BASE" || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
MRB() { ./bin/metarush --home "$HB" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
command -v python3 >/dev/null || { echo "python3 is needed (the ability is a Python script)" >&2; exit 1; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "two throwaway installations"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Studio" --media filesystem --port "$PORT" >/dev/null
printf 'METARUSH_MONGODB_URI=%s\n' "$URI" > "$H/.env"; chmod 600 "$H/.env"
METARUSH_MONGODB_URI="$URI" MRB init --yes --test-db --secrets env --db-name "$DBB" --workspace-name "Partner studio" --media filesystem --port "$((PORT + 1))" >/dev/null
printf 'METARUSH_MONGODB_URI=%s\n' "$URI" > "$HB/.env"; chmod 600 "$HB/.env"

step "projects: Base (the default's settings) and Acme (its own)"
MR project create Base >/dev/null
ACME="$(MR project create Acme --json | J 'd["data"]["project"]["id"]')"
MR content own --project "$ACME" >/dev/null
MR content leave --project "$ACME"

step "workspace B checks an ability and exports it"
A="$BASE/ability"; mkdir -p "$A"
cat > "$A/ability.yaml" <<'EOF'
ability: 1
name: title-card
title: Title card
does: Makes a plain title card picture from a few words.
runs: python3 make.py
inputs:
  words: {kind: words, max: 80, says: The words on the card}
outputs: [picture]
cost: free
needs: {programs: [python3]}
licence: MIT
search: [title, card, picture]
test: {inputs: {words: Hello}}
EOF
cat > "$A/make.py" <<'EOF'
import json, os
words = json.load(open(os.environ["METARUSH_INPUTS"]))["words"].replace("<", "")
svg = '<svg xmlns="http://www.w3.org/2000/svg" width="640" height="360"><rect width="640" height="360" fill="#264653"/><text x="320" y="190" font-size="40" text-anchor="middle" fill="#f4f1de">' + words + '</text></svg>'
open(os.path.join(os.environ["METARUSH_OUTPUT"], "card.svg"), "w").write(svg)
json.dump({"files": [{"path": "card.svg", "title": "Card: " + words, "licence": "CC0"}]}, open(os.path.join(os.environ["METARUSH_OUTPUT"], "result.json"), "w"))
EOF
echo "Short words read best." > "$A/notes.md"
MRB ability check "$A" --json | J '"B: %s" % d["data"]["message"]'
FILE="$BASE/title-card.ability.zip"
MRB ability export title-card "$FILE" --json | J '"B: %s" % d["data"]["message"]'

step "the dashboard on 127.0.0.1:$PORT"
export METARUSH_DEV_ORIGINS=""
e2e_bg "$BASE/serve.log" ./bin/metarush --home "$H" serve; SRV=$E2E_BG
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
METARUSH_READ_AS_CLAUDE="./bin/metarush --home $H transcript find hello --project $ACME --json" \
LEAVE_PROJECT="$ACME" LEAVE_FILE="$FILE" LEAVE_SHOTS="${LEAVE_SHOTS:-$BASE/screenshots}" node web/scripts/leave-e2e.mjs

step "what the browser did, from the CLI"
MR content leave --project "$ACME"
MR project leaves "$ACME"
MR ability list
exit 0
