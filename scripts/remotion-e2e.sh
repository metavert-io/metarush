#!/usr/bin/env bash
# End-to-end run of Remotion sources (D131–D138) in a real browser, on a throwaway installation: a fresh home
# (remotion-e2e-…), a local test database and the small fixture project (tests/fixtures/remotion/fixture-film,
# brought in from a folder with a .env, coding-assistant instructions and a render in out/ next to it). The
# browser run (web/scripts/remotion-e2e.mjs) reviews and approves the first version as a person (styled dialogs
# only), puts a composition in the cut, reviews an edit Claude would make (a new headline: words, before/after
# stills) and approves it, follows a clip back to its project, and tries the Add box with a folder and a GitHub
# address; pictures as settings (D209): its picture made a setting (a version approved), new art uploaded with
# Replace, checked and used (the cut's clip made again with it), the CLI's list and --original; Tier 3 (D173–D176): a
# second project that builds itself (scripts/make-refresh-fixture.sh, a git repository with a local bare remote) is
# refreshed from its own steps in the panel (steps, a fake key, outputs, started in the styled dialog, its log with
# the key hidden) and its approved changes sent back as a new branch; screenshots on a desktop (light and dark) and a
# phone go to a folder for review.
#
#   scripts/remotion-e2e.sh                  # needs scripts/dev-mongo.sh running; downloads the fixture's packages
#   KEEP=1 scripts/remotion-e2e.sh           # keep the home, database and server for a look afterwards
#   NO_BROWSER=1 scripts/remotion-e2e.sh     # set it up and print how to sign in
#   METARUSH_REMOTION_E2E_PORT=5315 METARUSH_REMOTION_E2E_HOME=/tmp/rs-x SKIP_BUILD=1 scripts/remotion-e2e.sh
#   METARUSH_REMOTION_BROWSER_SEED=<folder with chrome-headless-shell>   # reuse a browser instead of downloading one
#
# The throwaway database is dropped on exit through store.DropTestDatabase
# (scripts/droptestdb refuses anything but local metarush-test-*).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_REMOTION_E2E_PORT:-5315}"
H="${METARUSH_REMOTION_E2E_HOME:-$(mktemp -d "${TMPDIR:-/tmp}/remotion-e2e-XXXXXX")/home}"; DB="metarush-test-remotion$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SRV=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, dashboard http://127.0.0.1:$PORT (pid $SRV)"
    return
  fi
  [ -n "$SRV" ] && kill "$SRV" 2>/dev/null || true
  go run ./scripts/droptestdb "$URI" "$DB" || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$(dirname "$H")"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation"
mkdir -p "$H"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Remotion test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
printf 'METARUSH_MONGODB_URI=%s\n' "$URI" > "$H/.env"; chmod 600 "$H/.env"

step "the fixture project as a folder on this computer, with what must never come in"
RF="$(dirname "$H")/fixture-film"; rm -rf "$RF"; cp -R tests/fixtures/remotion/fixture-film "$RF"; mkdir -p "$RF/out"
ffmpeg -nostdin -loglevel error -y -f lavfi -i "color=c=0x14141c:s=640x360:r=30:d=2" -f lavfi -i "sine=frequency=440:duration=2" -shortest -c:v libx264 -pix_fmt yuv420p -c:a aac "$RF/out/Title.mp4"
printf 'MUSIC_SERVICE_TOKEN=made-up-for-this-run\n' > "$RF/.env"
printf 'Run npm run build after every change.\n' > "$RF/CLAUDE.md"
P=$(MR project create "Remotion film" --json | J 'd["data"]["project"]["id"]')
MR import remotion "$RF" --project "$P" --wait --json | J '"brought in: %s" % d["data"]["result"]["message"]'

step "new art for the title's picture (pictures as settings, D209)"
PIC="$(dirname "$H")/new-title-art.png"
ffmpeg -nostdin -loglevel error -y -f lavfi -i "color=c=0xe0458a:s=192x192" -frames:v 1 "$PIC"

step "a project that builds itself, with a remote to send changes back to (Tier 3), and a made-up key"
RR="$(dirname "$H")/refresh-film"; RREMOTE="$(dirname "$H")/refresh-remote.git"
RORIG=$(scripts/make-refresh-fixture.sh "$RR" "$RREMOTE")
# (brought in by the browser run when it gets to Tier 3, so the earlier checks see one project)
printf 'METARUSH_ELEVENLABS_API_KEY=sk_%s\n' "$(printf 'f00d%.0s' 1 2 3 4 5 6 7 8 9 10)" >> "$H/.env"

step "the dashboard on 127.0.0.1:$PORT (with its worker, which renders the clips)"
METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$H/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" \
METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
RS_PROJECT="$P" RS_FOLDER="$RF" RS_EDIT="$(dirname "$H")/rs-edit" RS_MR="./bin/metarush --home $H" RS_REFRESH="$RR" RS_REMOTE="$RREMOTE" RS_ORIGINAL="$RORIG" RS_PICTURE="$PIC" \
RS_SHOTS="${RS_SHOTS:-$(dirname "$H")/screenshots}" node web/scripts/remotion-e2e.mjs
exit 0
