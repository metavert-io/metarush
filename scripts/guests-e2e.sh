#!/usr/bin/env bash
# guests-e2e.sh — guests in a real browser (docs/guests.md), on a throwaway installation whose media is kept on this
# computer. "Spring promo" has a short clip, a cut and a review copy (an MP4 made here; scripts/guestseed keeps it as the
# version's review copy). In the browser the owner turns guests on in Settings → People → Guests (the guest listener
# answers on 127.0.0.1 at its own port), shares version 1 from its page with Share with a guest… as a commenter, and
# makes a one-time sign-in link; a second browser with no MetaRush sign-in opens the link at the guest address,
# signs in with its button, sees what was shared, watches the review copy (served by the guest listener, with
# ranges) and leaves a comment pinned to a time; it reaches nothing but the guest address. The owner sees the comment
# on the version, marked as a guest's, and what the guest did; takes everything back; the guest is out at once.
# Desktop and 390 px phone, dark and light, with screenshots; no native browser dialog, no sideways scrolling, plain
# words. Nothing is reachable from the internet: both addresses are this computer's own.
#
#   scripts/guests-e2e.sh                 # needs scripts/dev-mongo.sh running, ffmpeg and python3
#   KEEP=1 scripts/guests-e2e.sh          # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/guests-e2e.sh    # set it up and leave the dashboard running
#   METARUSH_GUESTS_PORT=5820 SKIP_BUILD=1 GUESTS_SHOTS=<folder> scripts/guests-e2e.sh
#
# Ports 5820–5829 belong to this run (the dashboard on 5820, the guest listener on 5821). The throwaway database is
# dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. scripts/lib/status.sh
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_GUESTS_PORT:-5820}"; GPORT=$((PORT + 1))
BASE="${METARUSH_GUESTS_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/guests-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-guests$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SRV=""
# The run's exit status is kept by scripts/lib/e2e.sh, which calls this first.
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, dashboard http://127.0.0.1:$PORT (pid $SRV), guests http://127.0.0.1:$GPORT"
    return
  fi
  if [ -n "$SRV" ]; then
    kill "$SRV" 2>/dev/null || true
    for _ in $(seq 1 60); do kill -0 "$SRV" 2>/dev/null || break; sleep 0.25; done
  fi
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
  # The folder goes with scripts/lib/e2e.sh: removed when the run passed, kept a day when it failed.
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
for t in ffmpeg python3; do command -v "$t" >/dev/null || { echo "$t is needed" >&2; exit 1; }; done
for p in "$PORT" "$GPORT"; do
  if curl -s -o /dev/null "http://127.0.0.1:$p/" 2>/dev/null; then
    echo "something already answers on 127.0.0.1:$p; choose another port with METARUSH_GUESTS_PORT" >&2
    exit 1
  fi
done
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation, its media in a folder on this computer"
mkdir -p "$H"
# Made-up owner and computer names (never this computer's git name and email).
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Studio" --media filesystem --port "$PORT" \
  --user-name "Sam Lee" --user-email sam@example.com --installation-name "Studio Mac" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
# The guest listener's port is this run's own; it stays off until the owner turns guests on.
printf 'guests:\n    port: %s\n' "$GPORT" >> "$H/config.yaml"
# No review copies by themselves (D187): the run gives the version one.
MR render follow off --json >/dev/null

step "a short clip, a cut of it and its review copy"
CLIPS="$BASE/clips"; mkdir -p "$CLIPS"
ffmpeg -hide_banner -loglevel error -nostdin -y -f lavfi -i "testsrc2=size=1280x720:rate=30:duration=6" -f lavfi -i "sine=frequency=440:duration=6:sample_rate=48000" \
  -c:v libx264 -preset veryfast -pix_fmt yuv420p -g 30 -c:a aac -b:a 96k -shortest -movflags +faststart "$CLIPS/garden.mp4"
P=$(MR project create "Spring promo" --json | J 'd["data"]["project"]["id"]')
MR ingest "$CLIPS/garden.mp4" --project "$P" --no-transcribe --wait --json | J '"added=%d" % d["data"]["ingest"]["ingested"]'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="garden.mp4"][0]')
cat > "$BASE/cut.json" <<EOF
{"projectId":"$P","instruction":"the garden, whole","spec":{"schemaVersion":1,"title":"The garden",
 "output":{"preset":"landscape-1080p","fps":30,"platform":"youtube"},"clips":[{"id":"walk","assetId":"$A","in":0,"out":5}]}}
EOF
TOK=$(MR edit check --plan "$BASE/cut.json" --json | J 'd["data"]["confirmToken"]')
R=$(MR edit apply --plan "$BASE/cut.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
echo "version 1: $R"
go run ./scripts/guestseed "$URI" "$DB" "$H/media" "$R" "$CLIPS/garden.mp4"
MR guests status

step "the dashboard on 127.0.0.1:$PORT"
export METARUSH_DEV_ORIGINS=""
e2e_bg "$BASE/serve.log" ./bin/metarush --home "$H" serve --no-watch; SRV=$E2E_BG
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done
curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null || { echo "the dashboard didn't start; see $BASE/serve.log" >&2; tail -20 "$BASE/serve.log" >&2; exit 1; }
if curl -s -o /dev/null "http://127.0.0.1:$GPORT/" 2>/dev/null; then
  echo "the guest listener answers before anyone turned guests on" >&2
  exit 1
fi

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_GUESTS="http://127.0.0.1:$GPORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
GUESTS_PROJECT="$P" GUESTS_REVISION="$R" GUESTS_SHOTS="${GUESTS_SHOTS:-$BASE/screenshots}" node web/scripts/guests-e2e.mjs

step "what the browser did, from the CLI"
MR guests status
MR guests list --all
MR guests activity --limit 20
MR comment list --project "$P"
# The guest listener stopped answering the guest at once; it never listened beyond this computer.
if lsof -nP -iTCP:"$GPORT" -sTCP:LISTEN 2>/dev/null | awk 'NR>1{print $9}' | grep -v '^127\.0\.0\.1:' | grep -q .; then
  echo "the guest listener listened beyond 127.0.0.1" >&2
  exit 1
fi
exit 0
