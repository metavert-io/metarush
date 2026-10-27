#!/usr/bin/env bash
# catalogue-e2e.sh — the menu of abilities in a real browser (D268–D272), on a throwaway
# installation that talks to nothing outside this computer. One ability is checked from the CLI
# (it runs once in the sandbox with the network off) and waits for an owner. The browser run
# (web/scripts/catalogue-e2e.mjs) opens Library → Abilities: what MetaRush does built in, group by
# group, beside the added ability, each saying whether it works on this computer now or what it
# needs first (no keys here: the AI services say which key); the filters; a built-in ability's
# page; the added one approved in the styled question and set up on this computer, after which
# it works here. Then the command line agrees: `catalogue list --json` and `ops find`. Desktop
# and 390 px phone, dark and light, with screenshots; no native browser dialog, no sideways
# scrolling, plain words.
#
#   scripts/catalogue-e2e.sh                  # needs scripts/dev-mongo.sh running, a sandbox and python3
#   KEEP=1 scripts/catalogue-e2e.sh           # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/catalogue-e2e.sh     # set it up and leave the dashboard running
#   METARUSH_CATALOGUE_PORT=5500 SKIP_BUILD=1 CATALOGUE_SHOTS=<folder> scripts/catalogue-e2e.sh
#
# Port: the dashboard on 5500 (5500–5509 are this run's). The throwaway database is dropped on
# exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_CATALOGUE_PORT:-5500}"
BASE="${METARUSH_CATALOGUE_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/catalogue-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-catalogue$(date +%s)"
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
  [ -z "${METARUSH_CATALOGUE_DIR:-}" ] && rm -rf "$BASE" || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
command -v python3 >/dev/null || { echo "python3 is needed (the ability is a Python script)" >&2; exit 1; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Catalogue test" --media filesystem --port "$PORT" >/dev/null
printf 'METARUSH_MONGODB_URI=%s\n' "$URI" > "$H/.env"; chmod 600 "$H/.env"

step "one ability added to the workspace, checked once in the sandbox with the network off"
A="$BASE/abilities/title-card"; mkdir -p "$A"
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
inputs = json.load(open(os.environ["METARUSH_INPUTS"]))
words = inputs["words"].replace("<", "").replace("&", "and")
svg = '<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720"><rect width="1280" height="720" fill="#264653"/>' \
      '<text x="640" y="380" font-size="72" text-anchor="middle" fill="#f4f1de">' + words + '</text></svg>'
open(os.path.join(os.environ["METARUSH_OUTPUT"], "card.svg"), "w").write(svg)
EOF
MR ability check "$A" --json | J '"Title card: %s" % d["data"]["message"]'

step "the menu from the command line, before the browser"
MR catalogue list --json | J '"%d built in, %d added; %d work here: %s" % (d["data"]["builtIn"], d["data"]["added"], d["data"]["worksNow"], d["data"]["message"])'

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
CATALOGUE_SHOTS="${CATALOGUE_SHOTS:-$BASE/screenshots}" node web/scripts/catalogue-e2e.mjs

step "the command line agrees with what the browser did"
works="$(MR catalogue list --added --json | J '[i["worksHere"] for i in d["data"]["items"] if i["name"] == "title-card"]')"
echo "title card works here: $works"
[ "$works" = "[True]" ] || { echo "FAIL the command line doesn't say the title card works here" >&2; exit 1; }
found="$(MR ops find a title card picture --json | J '[h["name"] for h in d["data"]["hits"]][:3]')"
echo "find: $found"
case "$found" in *title-card*) ;; *) echo "FAIL find doesn't list the title card" >&2; exit 1 ;; esac
MR catalogue show steady-footage
exit 0
