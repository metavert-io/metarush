#!/usr/bin/env bash
# starter-recipes-e2e.sh — the starter recipes and a sample first in a real browser
# (D293–D297), on a throwaway installation that talks only to a fake ElevenLabs
# (internal/testutil/fakeeleven): no real service, no real key. A project "Calls"
# holds a call recording (the generated call of scripts/make-conversation-fixtures.sh,
# "The garden call"). The browser run (web/scripts/recipes-starters-e2e.mjs):
#   finds the starter recipes on the Library's Recipes tab and adds them (each comes in
#   as a draft, never current); makes "A call becomes an episode with shorts" current;
#   plans a run with the call recording: the plan asks for a sample first (one stretch of
#   the call made clearer in the cloud), its price apart from the whole run's; Skip the
#   sample… asks why (cancelled here); Make the sample first agrees to the sample's price
#   only; the run's page shows the sample — its review copy to watch and listen to —
#   while To-dos offer no quiet approval; Approve the sample plans the rest, which asks
#   for its own agreement (Go on); the run waits for Claude (kept as Claude would), makes
#   the voices clearer, its review copy and finishes; the project's receipt says which
#   cost was the sample's; Home's Start from a recipe lists it. Desktop dark, light and a
#   390 px phone, with screenshots; no native browser dialog, no sideways scrolling, plain
#   words.
#
#   scripts/starter-recipes-e2e.sh                  # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/starter-recipes-e2e.sh           # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/starter-recipes-e2e.sh     # set it up and leave the dashboard running
#   SKIP_BUILD=1 STARTERS_SHOTS=<folder> scripts/starter-recipes-e2e.sh
#
# Ports (5550–5559): the dashboard on 5550, the fake ElevenLabs on 5551
# (METARUSH_STARTERS_PORT moves both). The throwaway database is dropped on exit
# through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_STARTERS_PORT:-5550}"; EFAKE=$((PORT + 1))
BASE="${METARUSH_STARTERS_DIR:-$(mktemp -d "${TMPDIR%/}/starter-recipes-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-starters$(date +%s)"
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
CALL="$ROOT/tests/fixtures/gen/conversation/gallery.mp4"
if [ ! -f "$CALL" ]; then
  step "the call recording (a generated call: macOS say and ImageMagick)"
  scripts/make-conversation-fixtures.sh >/dev/null
fi

step "a throwaway installation that talks only to the fake ElevenLabs"
mkdir -p "$H/state"
# --video-dir: renders (the review copies) use this checkout's renderer.
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Starters test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
# The fake's address goes in the home's .env before any key exists, so every process of this home sends
# requests to it, never to ElevenLabs. A key made at run time: the fake accepts only its own.
python3 -I -c "import secrets;print('sk'+'_'+secrets.token_hex(20))" > "$H/state/fake-eleven.key"
chmod 600 "$H/state/fake-eleven.key"
{
  printf 'METARUSH_MONGODB_URI=%s\n' "$URI"
  printf 'METARUSH_ELEVENLABS_BASE_URL=http://127.0.0.1:%s\n' "$EFAKE"
} > "$H/.env"; chmod 600 "$H/.env"
go build -o "$BASE/fakeeleven" ./internal/testutil/fakeeleven/cmd/fakeeleven
"$BASE/fakeeleven" -addr "127.0.0.1:$EFAKE" -key-file "$H/state/fake-eleven.key" > "$BASE/fakeeleven.log" 2>&1 &
for _ in $(seq 1 240); do curl -s -o /dev/null "http://127.0.0.1:$EFAKE/__calls" && break; sleep 0.25; done
curl -s -o /dev/null "http://127.0.0.1:$EFAKE/__calls" || { echo "the fake ElevenLabs never answered (see $BASE/fakeeleven.log)" >&2; exit 1; }
MR providers set elevenlabs --key-stdin --json < "$H/state/fake-eleven.key" | J '"ElevenLabs (the fake): %s" % d["data"]["masked"]'

step "Calls: a call recording"
P=$(MR project create "Calls" --json | J 'd["data"]["project"]["id"]')
MR providers settings elevenlabs --voice Ada --json >/dev/null  # the narration voice of the default Content Settings
MR ingest "$CALL" --project "$P" --no-transcribe --wait --json | J '"in the project: added %d" % d["data"]["ingest"]["ingested"]'
GC=$(MR asset list --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="gallery.mp4"][0]')
MR asset update "$GC" --title "The garden call" >/dev/null
MR recipe starters

step "the dashboard on 127.0.0.1:$PORT (with its worker: the clearer voices and the review copies)"
METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
STARTERS_CALL="$GC" STARTERS_ELEVEN_FAKE="http://127.0.0.1:$EFAKE" \
STARTERS_SHOTS="${STARTERS_SHOTS:-$BASE/screenshots}" node web/scripts/recipes-starters-e2e.mjs

step "what the browser did, from the CLI"
MR recipe starters
MR recipe runs call-episode
MR log list --since 1h --channel recipe --json | J '"done by the run (the recipe channel): %s" % sorted({e["text"] for e in d["data"]["entries"]})[:8]'
exit 0
