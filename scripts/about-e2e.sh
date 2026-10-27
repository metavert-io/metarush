#!/usr/bin/env bash
# about-e2e.sh — Settings → General (About MetaRush, D130) in a real browser, and the
# version on every surface: two throwaway installations of one workspace,
# "Studio Mac" (this checkout's build, stamped by scripts/version.sh) and
# "Mac mini" (a build that says it is 0.1.0, as an older computer would). It
# prints `metarush version` (plain, --json, --details) and /api/health, then
# web/scripts/about-e2e.mjs checks the About section: the version and its day,
# the exact build behind Details, both computers with the mini flagged older in
# plain words, what's new from the built-in changelog (show all, open one),
# plain words, no native dialogs or overflow. Screenshots (desktop dark and
# light, phone dark and light) go to ABOUT_SHOTS (default .agent-home/about-shots).
#
#   scripts/about-e2e.sh                       # needs scripts/dev-mongo.sh running
#   KEEP=1 SKIP_BUILD=1 METARUSH_E2E_PORT=4986 scripts/about-e2e.sh
#   MR_BIN=<another build> SKIP_BUILD=1 scripts/about-e2e.sh   # e.g. one built at a release tag
#
# The throwaway database is dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_E2E_PORT:-4986}"; PORT_B=$((PORT + 1))
TMPBASE="${TMPDIR:-/tmp}"; TMPBASE="${TMPBASE%/}"
BASE="${METARUSH_E2E_DIR:-$(mktemp -d "$TMPBASE/about-e2e-XXXXXX")}"
mkdir -p "$BASE"
HA="$BASE/studio"; HB="$BASE/mini"; MEDIA="$BASE/media"
DB="metarush-test-about$(date +%s)"
MR_BIN="${MR_BIN:-$ROOT/bin/metarush}"; OLD_BIN="$BASE/metarush-0.1.0"
URI="$(scripts/dev-mongo.sh start)"
SA=""
cleanup() {
  local code="$1"
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: homes $HA and $HB, database $DB, dashboard http://127.0.0.1:$PORT (pid $SA)"
    return
  fi
  [ -n "$SA" ] && kill "$SA" 2>/dev/null || true
  sleep 1
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
  # A failed run keeps its folder for a look; a folder you named is never removed.
  if [ "$code" = "0" ] && [ -z "${METARUSH_E2E_DIR:-}" ]; then rm -rf "$BASE"; fi
}
trap 'cleanup $?' EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
A() { "$MR_BIN" --home "$HA" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }

PKG=github.com/metavert-io/metarush/internal/version
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary (stamped: $(scripts/version.sh))"
  npm run build -w web >/dev/null
  go build -ldflags "$(scripts/version.sh --ldflags)" -o "$MR_BIN" ./cmd/metarush
fi
step "an older computer's build: it says it is 0.1.0"
go build -ldflags "-X $PKG.Version=0.1.0 -X $PKG.Commit=0a1b2c3 -X $PKG.Date=2026-10-17T09:00:00Z" -o "$OLD_BIN" ./cmd/metarush
"$OLD_BIN" version | head -1

step "two computers of one workspace"
METARUSH_MONGODB_URI="$URI" A init --yes --test-db --secrets env --db-name "$DB" --workspace-name "About test" --media filesystem \
  --media-root "$MEDIA" --media-shared --port "$PORT" --installation-name "Studio Mac" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$HA/.env"; chmod 600 "$HA/.env"
WS=$(A workspace show --json | J 'd["data"]["workspace"]["id"]')
METARUSH_MONGODB_URI="$URI" "$OLD_BIN" --home "$HB" init --yes --test-db --secrets env --db-name "$DB" --join "$WS" --media filesystem \
  --media-root "$MEDIA" --media-shared --port "$PORT_B" --installation-name "Mac mini" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$HB/.env"; chmod 600 "$HB/.env"

step "metarush version (plain, --json, --details)"
A version
A version --json | J '"--json: version %s, release %s, dev %s, devCount %s, commit %s, built %s" % (d["data"]["version"], d["data"]["release"], d["data"]["dev"], d["data"].get("devCount"), d["data"]["commit"], d["data"]["date"])'
A version --details | sed -n '1,12p'
A version --details --json | J '"--details --json: %d computers (%s), %d older; what is new: %s" % (len(d["data"]["computers"]), ", ".join("%s %s %s" % (c["name"], c.get("version", "?"), c["standing"]) for c in d["data"]["computers"]), d["data"]["older"], "; ".join("%s (%d)" % (s["heading"], len(s["entries"])) for s in d["data"]["whatsNew"]))'

step "the dashboard (no worker)"
METARUSH_DEV_ORIGINS="" "$MR_BIN" --home "$HA" serve --no-worker > "$BASE/studio.log" 2>&1 & SA=$!
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done
echo "/api/health: $(curl -sf "http://127.0.0.1:$PORT/api/health")"

step "the browser run"
SHOTS="${ABOUT_SHOTS:-$ROOT/.agent-home/about-shots}"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="$MR_BIN --home $HA dashboard --print-link --no-open" ABOUT_SHOTS="$SHOTS" \
  node web/scripts/about-e2e.mjs || { echo "--- dashboard log"; tail -40 "$BASE/studio.log"; exit 1; }
echo "screenshots: $SHOTS"
exit 0
