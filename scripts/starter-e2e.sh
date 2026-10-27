#!/usr/bin/env bash
# starter-e2e.sh — the starter pack of abilities (D283–D287) in a real browser, on a
# throwaway installation that talks to nothing outside this computer. Each starter
# ability is checked from its folder in abilities/ (its test runs once in the sandbox with
# the network off); the two not yet runnable are refused by the check, as their notes
# say. The ComfyUI ability is checked as a version whose one local service is a fake
# ComfyUI this run serves on this computer (web/scripts/starter-e2e.mjs starts it); the
# browser run approves it with that service, sets it up, runs it — in the sandbox,
# through its run's proxy, to that port — and follows the run to the picture; then
# reads what open media and Pexels may do. Desktop and 390 px phone, dark and light,
# with screenshots; no native browser dialog, no sideways scrolling, plain words.
#
#   scripts/starter-e2e.sh                  # needs scripts/dev-mongo.sh running, a sandbox and python3
#   KEEP=1 scripts/starter-e2e.sh           # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/starter-e2e.sh     # set it up and leave the dashboard running
#   METARUSH_STARTER_PORT=5530 SKIP_BUILD=1 STARTER_SHOTS=<folder> scripts/starter-e2e.sh
#
# Ports: the dashboard on 5530, the fake ComfyUI on 5531 (5530–5539 are this run's). The
# throwaway database is dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_STARTER_PORT:-5530}"
COMFY="$((PORT + 1))"
BASE="${METARUSH_STARTER_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/starter-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-starter$(date +%s)"
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
  # The folder goes through e2e_run: removed when the run passed, kept a day when it didn't.
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
command -v python3 >/dev/null || { echo "python3 is needed (the starter abilities are Python scripts)" >&2; exit 1; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Starter test" --media filesystem --port "$PORT" >/dev/null
printf 'METARUSH_MONGODB_URI=%s\n' "$URI" > "$H/.env"; chmod 600 "$H/.env"

step "the starter abilities, each checked once in the sandbox with the network off"
for a in pexels-stock open-media freesound-effects; do
  MR ability check "abilities/$a" --json | J '"%s: %s" % (d["data"]["name"], d["data"]["message"])'
done
if command -v ffmpeg >/dev/null && command -v whisper-cli >/dev/null; then
  MR ability check abilities/english-captions --json | J '"english-captions: %s" % d["data"]["message"]'
else
  echo "english-captions: skipped (no ffmpeg or whisper-cli on this computer)"
fi
# ComfyUI as a version whose one local service is this run's fake.
mkdir -p "$BASE/abilities"
cp -R abilities/comfyui-local "$BASE/abilities/"
sed "s/^  port: 8188$/  port: $COMFY/" abilities/comfyui-local/ability.yaml > "$BASE/abilities/comfyui-local/ability.yaml"
grep -q "^  port: $COMFY\$" "$BASE/abilities/comfyui-local/ability.yaml" || { echo "the ComfyUI manifest's port wasn't changed" >&2; exit 1; }
MR ability check "$BASE/abilities/comfyui-local" --json | J '"comfyui-local: %s" % d["data"]["message"]'
for a in content-credentials davinci-resolve; do
  MR ability check "abilities/$a" --json | J '"%s: %s (%s)" % ("'"$a"'", d["data"]["message"], d["data"]["problems"][0]["text"]) if not d["data"]["ok"] else sys.exit("'"$a"' passed its check, but it is marked not yet runnable")'
done

step "the dashboard on 127.0.0.1:$PORT (with its worker, which runs the abilities)"
export METARUSH_DEV_ORIGINS=""
e2e_bg "$BASE/serve.log" ./bin/metarush --home "$H" serve; SRV=$E2E_BG
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run (it serves the fake ComfyUI on 127.0.0.1:$COMFY)"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
COMFY_PORT="$COMFY" STARTER_SHOTS="${STARTER_SHOTS:-$BASE/screenshots}" node web/scripts/starter-e2e.mjs

step "what the browser did, from the CLI"
MR ability list
MR ability runs comfyui-local
exit 0
