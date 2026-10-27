#!/usr/bin/env bash
# abilities-e2e.sh — Library → Abilities in a real browser (D230–D235), on a throwaway
# installation that talks to nothing outside this computer. Two abilities are checked
# from the CLI (each runs once in the sandbox with the network off): "Title card", a
# local one that makes a picture from words, and "Stock photos", which would reach one
# made-up host with a key and costs money per photo. The browser run
# (web/scripts/abilities-e2e.mjs) finds them in the Library, To-dos and the bell; reads
# what Title card may do and approves it in the styled question; sets it up on this
# computer; runs it from Run… (the plan first: nothing leaves this computer); follows
# the run to its picture; sees Stock photos' key, the places it reaches and its price,
# adds a made-up key in the setup dialog (typed, never shown); takes Title card's
# approval back. Desktop and 390 px phone, dark and light, with screenshots; no native
# browser dialog, no sideways scrolling, plain words.
#
#   scripts/abilities-e2e.sh                  # needs scripts/dev-mongo.sh running, a sandbox and python3
#   KEEP=1 scripts/abilities-e2e.sh           # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/abilities-e2e.sh     # set it up and leave the dashboard running
#   METARUSH_ABILITIES_PORT=5420 SKIP_BUILD=1 ABILITIES_SHOTS=<folder> scripts/abilities-e2e.sh
#
# Port: the dashboard on 5420 (5420–5429 are this run's). The throwaway database is
# dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_ABILITIES_PORT:-5420}"
BASE="${METARUSH_ABILITIES_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/abilities-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-abilities$(date +%s)"
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
J() { python3 -I -c "import json,sys
d=json.load(sys.stdin)
try: print($1)
except (KeyError,IndexError,TypeError): sys.exit('unexpected answer: '+json.dumps(d)[:3000])"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
command -v python3 >/dev/null || { echo "python3 is needed (the abilities are Python scripts)" >&2; exit 1; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Abilities test" --media filesystem --port "$PORT" >/dev/null
printf 'METARUSH_MONGODB_URI=%s\n' "$URI" > "$H/.env"; chmod 600 "$H/.env"

step "two abilities, each checked once in the sandbox with the network off"
A="$BASE/abilities"; mkdir -p "$A/title-card" "$A/stock-photos"
cat > "$A/title-card/ability.yaml" <<'EOF'
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
cat > "$A/title-card/make.py" <<'EOF'
import json, os
inputs = json.load(open(os.environ["METARUSH_INPUTS"]))
words = inputs["words"].replace("<", "").replace("&", "and")
svg = '<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720"><rect width="1280" height="720" fill="#264653"/>' \
      '<text x="640" y="380" font-size="72" text-anchor="middle" fill="#f4f1de">' + words + '</text></svg>'
out = os.environ["METARUSH_OUTPUT"]
open(os.path.join(out, "card.svg"), "w").write(svg)
json.dump({"files": [{"path": "card.svg", "title": "Card: " + words, "licence": "CC0"}]}, open(os.path.join(out, "result.json"), "w"))
print("made a card for", words)
EOF
echo "Keep the words short: a card reads best with five or fewer." > "$A/title-card/notes.md"
cat > "$A/stock-photos/ability.yaml" <<'EOF'
ability: 1
name: stock-photos
title: Stock photos
does: Finds a stock photo that matches a few words.
runs: python3 fetch.py
inputs:
  words: {kind: words, max: 100, says: What the photo shows}
outputs: [picture]
cost: {service: Example Photos, usd: 0.02, per: photo, as-of: 2026-10-01}
keys: [{name: example-photos, env: PHOTOS_KEY, hosts: [api.photos.example]}]
hosts: [api.photos.example]
sends: [words]
needs: {programs: [python3]}
licence: Apache-2.0
search: [stock, photo, picture]
test: {inputs: {words: test}}
EOF
cat > "$A/stock-photos/fetch.py" <<'EOF'
import os, sys
if os.environ.get("METARUSH_TEST"):
    print("checked offline"); sys.exit(0)
sys.exit("this example never runs in the browser test")
EOF
MR ability check "$A/title-card" --json | J '"Title card: %s" % d["data"]["message"]'
MR ability check "$A/stock-photos" --json | J '"Stock photos: %s" % d["data"]["message"]'

step "the dashboard on 127.0.0.1:$PORT (with its worker, which runs the abilities)"
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
ABILITIES_SHOTS="${ABILITIES_SHOTS:-$BASE/screenshots}" node web/scripts/abilities-e2e.mjs

step "what the browser did, from the CLI"
MR ability list
MR ability runs
MR log list --since 1h --json | J '"in the log: %s" % sorted({e["text"] for e in d["data"]["entries"] if "abilit" in e["text"]})[:8]'
exit 0
