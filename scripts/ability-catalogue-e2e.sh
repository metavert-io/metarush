#!/usr/bin/env bash
# ability-catalogue-e2e.sh — community catalogues of abilities in a real browser (D480–D487), on a
# throwaway installation that talks to nothing outside this computer. A fixture GitHub repository
# (scripts/make-fixture-repo.sh … ability, through METARUSH_GIT_BASE) holds the "Title card" ability;
# the catalogue's index is made from a checkout of it by the generator (metarush ability catalogues
# make), with a second listing added by hand whose archive lies, and served over HTTPS on this
# computer (scripts/catalogueserve, a certificate authority made for the run). The browser run
# (web/scripts/ability-catalogue-e2e.mjs): an owner adds the catalogue under Settings → Abilities (a
# look first, a styled question); Library → Abilities → Browse catalogues lists what it lists with
# "listed, not checked by MetaRush"; Bring it in… fetches Title card from the repository's commit and
# lands it as a draft; a terminal checks it here (its test in the sandbox, network off); the owner
# approves it in the browser; the lying listing is refused in plain words; browse says Title card is
# here; the owner removes the catalogue. Desktop and 390 px phone, dark and light, with screenshots;
# no native browser dialog, no sideways scrolling, plain words.
#
#   scripts/ability-catalogue-e2e.sh                  # needs scripts/dev-mongo.sh running, git, a sandbox and python3
#   KEEP=1 scripts/ability-catalogue-e2e.sh           # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/ability-catalogue-e2e.sh     # set it up and leave the dashboard running
#   METARUSH_CATALOGUE_E2E_PORT=5810 SKIP_BUILD=1 CATALOGUE_SHOTS=<folder> scripts/ability-catalogue-e2e.sh
#
# Ports: the dashboard on 5810, the catalogue on 5811 (5810–5819 are this run's). The throwaway
# database is dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_CATALOGUE_E2E_PORT:-5810}"
CPORT=$((PORT + 1))
BASE="${METARUSH_CATALOGUE_E2E_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/ability-catalogue-e2e-XXXXXX")}"
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
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys
d=json.load(sys.stdin)
try: print($1)
except (KeyError,IndexError,TypeError): sys.exit('unexpected answer: '+json.dumps(d)[:3000])"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
command -v python3 >/dev/null || { echo "python3 is needed (the ability is a Python script)" >&2; exit 1; }
command -v git >/dev/null || { echo "git is needed (the catalogue's ability is in a repository)" >&2; exit 1; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Catalogue test" --media filesystem --port "$PORT" >/dev/null
printf 'METARUSH_MONGODB_URI=%s\n' "$URI" > "$H/.env"; chmod 600 "$H/.env"

step "the fixture repository on GitHub (a bare repository here) with the Title card ability"
GIT="$BASE/git"
bash scripts/make-fixture-repo.sh "$GIT" >/dev/null
bash scripts/make-fixture-repo.sh "$GIT" ability >/dev/null
export METARUSH_GIT_BASE="$GIT"
CHECKOUT="$BASE/widget"
git -c protocol.file.allow=always -c core.hooksPath=/dev/null clone -q "$GIT/acme/widget.git" "$CHECKOUT"

step "the catalogue's index, made by the generator from the checkout"
SITE="$BASE/site"; mkdir -p "$SITE"
MR ability catalogues make "$CHECKOUT/abilities" --out "$SITE/index.json" --name acme --title "Acme abilities" \
  --about "Abilities for small video teams." --repository https://github.com/acme/widget --maintainer acme --json \
  | J '"listed: %s, left out: %s" % ([l["name"] for l in d["data"]["listed"]], d["data"]["leftOut"])'
# A second listing whose archive lies: MetaRush must refuse it when someone brings it in.
python3 -I - "$SITE/index.json" <<'PY'
import copy, json, sys
p = sys.argv[1]
ix = json.load(open(p))
e = copy.deepcopy(ix["abilities"][0])
e["name"], e["title"], e["archive"] = "copy-card", "Copy card", "0" * 64
e["does"] = "Says it is a title card, but its files are another version."
ix["abilities"].append(e)
json.dump(ix, open(p, "w"), indent=2)
PY

step "the catalogue on https://127.0.0.1:$CPORT (a certificate authority made for this run)"
CA="$BASE/catalogue-ca.pem"
e2e_bg "$BASE/catalogue.log" go run ./scripts/catalogueserve "$SITE" "$CPORT" "$CA"
for _ in $(seq 1 120); do [ -s "$CA" ] && curl -sf --cacert "$CA" "https://127.0.0.1:$CPORT/index.json" >/dev/null && break; sleep 0.5; done
curl -sf --cacert "$CA" "https://127.0.0.1:$CPORT/index.json" >/dev/null || { echo "the catalogue server didn't start" >&2; cat "$BASE/catalogue.log" >&2; exit 1; }
export METARUSH_CATALOGUE_CA="$CA"

step "the dashboard on 127.0.0.1:$PORT"
export METARUSH_DEV_ORIGINS=""
e2e_bg "$BASE/serve.log" ./bin/metarush --home "$H" serve; SRV=$E2E_BG
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  echo "the catalogue's address: https://127.0.0.1:$CPORT/index.json"
  KEEP=1
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
METARUSH_CHECK="./bin/metarush --home $H ability check --version" CATALOGUE_URL="https://127.0.0.1:$CPORT/index.json" \
CATALOGUE_SHOTS="${CATALOGUE_SHOTS:-$BASE/screenshots}" node web/scripts/ability-catalogue-e2e.mjs

step "what the browser did, from the command line"
MR ability list
MR ability catalogues list
MR log list --since 1h --json | J '"in the log: %s" % sorted({e["text"] for e in d["data"]["entries"] if "catalogue" in e["text"]})[:8]'
exit 0
