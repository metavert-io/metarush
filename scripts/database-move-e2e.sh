#!/usr/bin/env bash
# database-move-e2e.sh — moving the workspace's database from the dashboard (D334–D338,
# docs/database-move.md) in a real browser, on two throwaway installations of one workspace
# ("Studio Mac" and "Edit laptop") on one test database: the first plans the move to another,
# empty test database from Settings → Storage → Move the database (checked first, confirmed in a
# styled dialog), both dashboards start again holding (changes refused, every page says so),
# the copy is checked and the first switches; the second is told the workspace moved, refuses
# changes and switches with its own address. A project then written straight into the old database
# (a computer that slept through the move, scripts/latewrite) is found by the studio's next look and
# shown on its card, and carried over after a styled dialog (D352–D358). The command line then
# agrees, and refuses Claude Code and an address on the command line. Screenshots in DATABASE_SHOTS.
#
#   scripts/database-move-e2e.sh                     # needs scripts/dev-mongo.sh running and python3
#   KEEP=1 SKIP_BUILD=1 scripts/database-move-e2e.sh
#
# Ports (this run's own, 5690–5699): the studio's dashboard 5690, the laptop's 5691. Both
# throwaway databases are dropped on exit.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT_A="${METARUSH_DATABASE_MOVE_PORT:-5690}"; PORT_B=$((PORT_A + 1))
TMPBASE="${TMPDIR:-/tmp}"; TMPBASE="${TMPBASE%/}"
BASE="${METARUSH_DATABASE_MOVE_DIR:-$(mktemp -d "$TMPBASE/database-move-e2e-XXXXXX")}"
mkdir -p "$BASE"
HA="$BASE/studio"; HB="$BASE/laptop"; USERHOME="$BASE/user"
DB="metarush-test-dbmove$(date +%s)"
NEWDB="${DB}new"
URI="$(scripts/dev-mongo.sh start)"
MR_BIN="${MR_BIN:-$ROOT/bin/metarush}"
SA=""; SB=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: homes $HA and $HB, databases $DB and $NEWDB, dashboards http://127.0.0.1:$PORT_A and http://127.0.0.1:$PORT_B"
    return
  fi
  for p in $SA $SB; do
    kill "$p" 2>/dev/null || true
    for _ in $(seq 1 60); do kill -0 "$p" 2>/dev/null || break; sleep 0.25; done
  done
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
  go run ./scripts/droptestdb "$URI" "$NEWDB" >/dev/null || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its databases, removes its folder when it passes
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
for p in "$PORT_A" "$PORT_B"; do
  if nc -z 127.0.0.1 "$p" >/dev/null 2>&1; then die "port $p is in use (an earlier run left something running? scripts/lib/e2e.sh list)"; fi
done

if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary ($(scripts/version.sh))"
  npm run build -w web >/dev/null
  go build -ldflags "$(scripts/version.sh --ldflags)" -o "$MR_BIN" ./cmd/metarush
fi
go build -o "$BASE/latewrite" ./scripts/latewrite   # a computer that slept through the move

mkdir -p "$USERHOME"
export METARUSH_NO_PAID_SERVICES=1 METARUSH_DEV_ORIGINS=""
unset METARUSH_MONGODB_URI METARUSH_DATABASE_NAME METARUSH_HOME CLAUDE_CONFIG_DIR CLAUDECODE METARUSH_AGENT METARUSH_VIA
A() { HOME="$USERHOME" "$MR_BIN" --home "$HA" "$@"; }
B() { HOME="$USERHOME" "$MR_BIN" --home "$HB" "$@"; }

step "two computers of one workspace, on one database"
METARUSH_MONGODB_URI="$URI" A init --yes --test-db --secrets env --db-name "$DB" --workspace-name "FernBrook Studio" --media filesystem \
  --media-root "$BASE/media" --media-shared --port "$PORT_A" --installation-name "Studio Mac" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$HA/.env"; chmod 600 "$HA/.env"
WS=$(A workspace show --json | J 'd["data"]["workspace"]["id"]')
METARUSH_MONGODB_URI="$URI" B init --yes --test-db --secrets env --db-name "$DB" --join "$WS" --media filesystem \
  --media-root "$BASE/media" --media-shared --port "$PORT_B" --installation-name "Edit laptop" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$HB/.env"; chmod 600 "$HB/.env"
A project create "Spring launch" --json >/dev/null
A project create "Call with Sam" --json >/dev/null

step "the command line refuses an address on it, and Claude Code"
if A database plan --to "$URI" --json >"$BASE/argv.json" 2>&1; then die "an address on the command line was taken"; fi
[ "$(J 'd["error"]["code"]' <"$BASE/argv.json")" = E_INVALID_INPUT ] || { cat "$BASE/argv.json"; die "an address on the command line wasn't refused plainly"; }
if grep -q "127.0.0.1:27018" "$BASE/argv.json"; then die "the refusal repeated the address"; fi
if CLAUDECODE=1 A database move --to here --json >"$BASE/claude.json" 2>&1; then die "Claude Code moved the database"; fi
[ "$(J 'd["error"]["code"]' <"$BASE/claude.json")" = E_HUMAN_ONLY ] || { cat "$BASE/claude.json"; die "Claude Code's move wasn't refused as people-only"; }
echo "an address as --to: refused; Claude Code moving: refused"

step "both dashboards (no workers: nothing else runs meanwhile)"
e2e_bg "$BASE/studio.log" env HOME="$USERHOME" "$MR_BIN" --home "$HA" serve --no-worker; SA=$E2E_BG
e2e_bg "$BASE/laptop.log" env HOME="$USERHOME" "$MR_BIN" --home "$HB" serve --no-worker; SB=$E2E_BG
for port in "$PORT_A" "$PORT_B"; do
  for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$port/api/health" >/dev/null && break; sleep 0.25; done
  curl -sf "http://127.0.0.1:$port/api/health" >/dev/null || { tail -40 "$BASE/studio.log" "$BASE/laptop.log"; die "a dashboard didn't start"; }
done

step "the browser run"
METARUSH_DASHBOARD_A="http://127.0.0.1:$PORT_A" METARUSH_MINT_A="env HOME=$USERHOME $MR_BIN --home $HA dashboard --print-link --no-open" \
METARUSH_DASHBOARD_B="http://127.0.0.1:$PORT_B" METARUSH_MINT_B="env HOME=$USERHOME $MR_BIN --home $HB dashboard --print-link --no-open" \
DATABASE_ADDRESS="$URI" DATABASE_NEW="$NEWDB" DATABASE_OLD="$DB" DATABASE_LATE_WRITER="$BASE/latewrite" \
DATABASE_SHOTS="${DATABASE_SHOTS:-$BASE/screenshots}" node web/scripts/database-move-e2e.mjs || {
  echo "--- studio dashboard log"; tail -60 "$BASE/studio.log"; echo "--- laptop dashboard log"; tail -60 "$BASE/laptop.log"
  for f in "$HA"/state/database-move/*.log; do [ -f "$f" ] && { echo "--- $f"; cat "$f"; }; done
  exit 1
}

step "afterwards: both computers use the new database; the old one is kept, marked as moved"
for M in A B; do
  NAME=$($M database move-status --json | J 'd["data"]["this"]["name"]')
  [ "$NAME" = "$NEWDB" ] || die "$M still uses $NAME"
  $M database move-status
done
grep -q "$URI" "$HA/config.yaml" "$HB/config.yaml" && die "an address is in config.yaml" || true

step "what was written into the old database late: the command line agrees"
A database late-writes --no-look
[ "$(A database late-writes --no-look --json | J 'd["data"]["found"][0]["state"]')" = carried ] || die "the late write isn't carried over"
[ "$(B database late-writes --json | J 'd["data"]["looked"]')" = False ] || die "the laptop looked at the old database (only the studio keeps its address)"
if CLAUDECODE=1 A database late-writes leave 1 --json >"$BASE/late-claude.json" 2>&1; then die "Claude Code decided a late write"; fi
[ "$(J 'd["error"]["code"]' <"$BASE/late-claude.json")" = E_HUMAN_ONLY ] || { cat "$BASE/late-claude.json"; die "Claude Code's decision wasn't refused as people-only"; }
echo "the late write: carried over; the laptop doesn't look; Claude Code deciding: refused"
for f in "$HA"/state/database-move/*.log; do [ -f "$f" ] && { echo "--- the studio's move log"; cat "$f"; } ; done
grep -h "holding" "$BASE/studio.log" "$BASE/laptop.log" | head -4 || true
echo "database-move-e2e: passed"
exit 0
