#!/usr/bin/env bash
# content-e2e.sh — Content Settings in a real browser (D192, D193), on a
# throwaway installation: "fernbrook" (the first project, so its settings are the
# default) with a logo, a brand and a spelling; "Client" with a short cut;
# "Third". The browser opens the old addresses (/settings/brand, Settings →
# Footage → Spelling), each project's Settings tab, gives Client its own
# settings (a copy), changes its brand and takes the offer for its latest cut,
# has Third follow Client, moves "Use for new projects", and adds a house rule.
# Then the parts of D198–D200: Client's logos for dark and light backgrounds
# (uploaded right there, offered for the latest cut, a title card that shows
# the one made for its background), a house style (offered for the latest
# cut; a new cut of "Fourth", which uses the default, starts with it) and a
# house sign-off (read-only for Third, ticked there on its own).
# Screenshots: desktop dark and light, phone.
#
#   scripts/content-e2e.sh                  # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/content-e2e.sh           # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/content-e2e.sh     # set it up and leave the dashboard running
#   METARUSH_E2E_PORT=5375 SKIP_BUILD=1 CONTENT_SHOTS=<folder> scripts/content-e2e.sh
#
# The throwaway database is dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_E2E_PORT:-5374}"
BASE="${METARUSH_E2E_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/content-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-content$(date +%s)"
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
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Content test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"

step "fernbrook (the default's settings): a logo, its brand and a spelling"
mkdir -p "$BASE/media"
E=$(MR project create "fernbrook" --json | J 'd["data"]["project"]["id"]')
printf '<svg xmlns="http://www.w3.org/2000/svg" width="240" height="120" viewBox="0 0 240 120"><rect x="6" y="6" width="108" height="108" rx="28" fill="#2E7D50"/><circle cx="60" cy="60" r="26" fill="#F5C842"/><rect x="128" y="44" width="104" height="32" rx="6" fill="#1B1B1F"/></svg>\n' > "$BASE/media/fernbrook-logo.svg"
MR add "$BASE/media/fernbrook-logo.svg" --project "$E" --json >/dev/null
MR brand set --logo fernbrook-logo --json | J '"brand: " + d["data"]["message"]'
MR transcript spelling add FernBrook --heard-as "Fern Brook" --json | J 'd["data"]["summary"]'
MR content show --project "$E" --json | J 'd["data"]["summary"]'

step "Client (with a short cut), Third and Fourth use the default"
C=$(MR project create "Client" --json | J 'd["data"]["project"]["id"]')
T=$(MR project create "Third" --json | J 'd["data"]["project"]["id"]')
F=$(MR project create "Fourth" --json | J 'd["data"]["project"]["id"]')
ffmpeg -v error -y -f lavfi -i "color=c=0x2a6f4e:s=640x360:r=30:d=4" -f lavfi -i "sine=frequency=330:sample_rate=48000:duration=4" \
  -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest "$BASE/media/client-talk.mp4"
MR ingest "$BASE/media/client-talk.mp4" --project "$C" --no-transcribe --wait --json | J '"added=%d" % d["data"]["ingest"]["ingested"]'
A=$(MR asset list "$C" --json | J '[a["id"] for a in d["data"]["assets"]][0]')
cat > "$BASE/plan.json" <<EOF
{"projectId":"$C","instruction":"A first cut","spec":{"schemaVersion":1,"title":"Client intro","output":{"preset":"landscape-1080p","fps":30},
 "clips":[{"id":"talk","assetId":"$A","in":0,"out":3.5}]}}
EOF
TOK=$(MR edit check --plan "$BASE/plan.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$BASE/plan.json" --confirm "$TOK" --json | J '"version %d of Client (its brand: fernbrook'"'"'s)" % d["data"]["revision"]["number"]'
MR content show --project "$C" --json | J 'd["data"]["summary"]'

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run: each project's Settings tab"
SHOTS="${CONTENT_SHOTS:-$BASE/screenshots}"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
CONTENT_FERNBROOK="$E" CONTENT_CLIENT="$C" CONTENT_THIRD="$T" CONTENT_FOURTH="$F" CONTENT_ASSET="$A" CONTENT_SHOTS="$SHOTS" node web/scripts/content-e2e.mjs

step "what the browser changed, from the CLI"
MR content show --project "$C"
MR content show --project "$T"
MR content rules list --project "$T"
MR content style show --project "$T"
MR content signoffs list --project "$T"
MR brand show --project "$T"
exit 0
