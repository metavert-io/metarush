#!/usr/bin/env bash
# accounts-e2e.sh — accounts and sign-in in a real browser (D183, docs/accounts.md)
# on a throwaway installation: a fresh home and local test database where the
# owner opens the dashboard with a one-time link and nobody else can get in yet.
# The browser run (web/scripts/accounts-e2e.mjs) checks the top bar's one menu
# (D184: a menu button while it is just the owner, the person's picture once
# people sign in), has the owner choose "Email and password" in Settings →
# People, set and remove a picture (cropped in a styled dialog; a file that isn't
# a picture refused), invite maya@example.com, follow the link in a new
# browser (name and password), sign out and in again, try a wrong password, and
# use a reset link from the owner; the old password stops working; she pairs her
# own browser extension and sees only hers, the owner sees everyone's (D197).
# Then the CLI shows what the browser did. Screenshots of /signin, /welcome,
# Settings → People, Settings → Claude Code, Settings → Browser extension (Your
# browsers), Library → People, the top bar and the open menu (desktop and phone,
# dark and light) go to ACCOUNTS_SHOTS.
#
#   scripts/accounts-e2e.sh                       # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/accounts-e2e.sh                # keep the database and the dashboard running afterwards
#   NO_BROWSER=1 scripts/accounts-e2e.sh          # set it up and leave the dashboard running
#   SKIP_BUILD=1 scripts/accounts-e2e.sh          # use the binary and dashboard as they are
#   METARUSH_E2E_PORT=4927 METARUSH_BIN=<binary> METARUSH_E2E_DIR=<empty folder> ACCOUNTS_SHOTS=<folder> scripts/accounts-e2e.sh
#
# The throwaway database is dropped on exit through scripts/droptestdb
# (store.DropTestDatabase); the installation's home is kept for a look (its path
# is printed) — remove it when done.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_E2E_PORT:-4925}"
BIN="${METARUSH_BIN:-$ROOT/bin/metarush}"
T="${TMPDIR:-/tmp}"; T="${T%/}"
BASE="${METARUSH_E2E_DIR:-$(mktemp -d "$T/accounts-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-accounts$(date +%s)"
SHOTS="${ACCOUNTS_SHOTS:-$BASE/screenshots}"
URI="$(scripts/dev-mongo.sh start)"
SRV=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, dashboard http://127.0.0.1:$PORT (pid $SRV)"
    return
  fi
  if [ -n "$SRV" ]; then
    kill "$SRV" 2>/dev/null || true
    # Let it finish writing its activity log first: a late write would make the database again.
    for _ in $(seq 1 60); do kill -0 "$SRV" 2>/dev/null || break; sleep 0.25; done
  fi
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
  echo "kept for a look: $BASE (home, server log, screenshots); the database $DB is dropped"
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { "$BIN" --home "$H" "$@"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }

if curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1; then
  echo "something already answers on 127.0.0.1:$PORT; choose another port with METARUSH_E2E_PORT" >&2
  exit 1
fi
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o "$BIN" ./cmd/metarush
fi

step "a throwaway installation: just the owner, on this computer"
# Made-up owner and computer names (never this computer's git name and email).
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Accounts test" --media filesystem --port "$PORT" \
  --user-name "Sam Lee" --user-email sam@example.com --installation-name "Studio Mac" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
MR auth status

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_DEV_ORIGINS="" "$BIN" --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 120); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done
curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null || { echo "the dashboard didn't start; see $BASE/serve.log" >&2; tail -20 "$BASE/serve.log" >&2; exit 1; }

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: $BIN --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi

step "the browser run: choose the sign-in, invite, join, sign in and out, reset"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="$BIN --home $H dashboard --print-link --no-open" \
ACCOUNTS_SHOTS="$SHOTS" node web/scripts/accounts-e2e.mjs

step "what the browser did, from the CLI"
MR auth status
MR users list
exit 0
