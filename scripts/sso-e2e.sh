#!/usr/bin/env bash
# sso-e2e.sh — signing in with an organization's account in a real browser
# (D425–D434, docs/accounts.md) on a throwaway installation, against a stand-in
# sign-in provider on this computer (internal/testutil/fakeoidc/cmd: keys and the
# app's secret made for this run; never a real provider). The browser run
# (web/scripts/sso-e2e.mjs): the owner sets it up in Settings → People (the
# provider, the app, its secret in the one-time form, an allowed domain), sees
# the addresses to register (this computer's at 127.0.0.1), runs the test
# sign-in in a tab of its own, switches; sets up another app with its own
# secret, which waits while people keep signing in with the one in use: an
# invited person joins with the invite link, a person of the allowed domain
# joins by themselves, a stranger is turned away in plain words; the owner
# forgets the waiting app, signs in again through the provider, and two
# sign-ins started in two tabs of one browser both finish. Then the CLI shows
# what the browser did.
# Screenshots go to SSO_SHOTS.
#
#   scripts/sso-e2e.sh                                  # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/sso-e2e.sh                           # keep the database and the servers running afterwards
#   SKIP_BUILD=1 scripts/sso-e2e.sh                     # use the binary and dashboard as they are
#   METARUSH_SSO_PORT=5790 METARUSH_BIN=<binary> SSO_SHOTS=<folder> scripts/sso-e2e.sh
#
# Ports: PORT (MetaRush), PORT+1 (the stand-in provider). The throwaway database
# is dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_SSO_PORT:-5790}"; IDP=$((PORT + 1))
BIN="${METARUSH_BIN:-$ROOT/bin/metarush}"
T="${TMPDIR:-/tmp}"; T="${T%/}"
BASE="$(mktemp -d "$T/sso-e2e-XXXXXX")"
H="$BASE/home"; DB="metarush-test-sso$(date +%s)"
SHOTS="${SSO_SHOTS:-$BASE/screenshots}"
URI="$(scripts/dev-mongo.sh start)"
SRV=""; FAKE=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, dashboard http://127.0.0.1:$PORT (pid $SRV), provider http://127.0.0.1:$IDP (pid $FAKE)"
    return
  fi
  for p in "$SRV" "$FAKE"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  if [ -n "$SRV" ]; then
    for _ in $(seq 1 60); do kill -0 "$SRV" 2>/dev/null || break; sleep 0.25; done
  fi
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { "$BIN" --home "$H" "$@"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }

for p in "$PORT" "$IDP"; do
  if curl -s -o /dev/null "http://127.0.0.1:$p/" 2>/dev/null; then
    echo "something already answers on 127.0.0.1:$p; choose another port with METARUSH_SSO_PORT" >&2
    exit 1
  fi
done
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o "$BIN" ./cmd/metarush
fi
go build -o "$BASE/fakeoidc" ./internal/testutil/fakeoidc/cmd

step "a stand-in sign-in provider on 127.0.0.1:$IDP (its app's secret made for this run)"
# The secret is made here and handed over in the environment: never in a file or an argument.
FAKEOIDC_SECRET="$(openssl rand -hex 20 2>/dev/null || python3 -I -c 'import secrets; print(secrets.token_hex(20))')"
# The second app's secret (it only waits; the provider never sees it).
SSO_SECRET_B="$(openssl rand -hex 20 2>/dev/null || python3 -I -c 'import secrets; print(secrets.token_hex(20))')"
export FAKEOIDC_SECRET SSO_SECRET_B
"$BASE/fakeoidc" -listen "127.0.0.1:$IDP" -client-id metarush-e2e -redirects "http://127.0.0.1:$PORT/auth/sso/callback" > "$BASE/fakeoidc.log" 2>&1 & FAKE=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$IDP/.well-known/openid-configuration" >/dev/null && break; sleep 0.25; done
curl -sf "http://127.0.0.1:$IDP/.well-known/openid-configuration" >/dev/null || { echo "the stand-in provider didn't start; see $BASE/fakeoidc.log" >&2; exit 1; }

step "a throwaway installation: just the owner, on this computer"
# Made-up owner and computer names (never this computer's git name and email).
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Sign-in test" --media filesystem --port "$PORT" \
  --user-name "Sam Lee" --user-email sam@example.com --installation-name "Studio Mac" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
MR auth sso status

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_DEV_ORIGINS="" "$BIN" --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 120); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done
curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null || { echo "the dashboard didn't start; see $BASE/serve.log" >&2; tail -20 "$BASE/serve.log" >&2; exit 1; }

step "the browser run: set it up, test, switch, another app waits, invite, join, refuse, forget, sign in, two tabs"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="$BIN --home $H dashboard --print-link --no-open" \
SSO_PROVIDER="http://127.0.0.1:$IDP" SSO_CLIENT_ID=metarush-e2e SSO_SHOTS="$SHOTS" node web/scripts/sso-e2e.mjs

step "what the browser did, from the CLI"
MR auth sso status
MR auth status
MR users list
# The client secret stayed on this computer's own secrets file, nowhere a person reads.
for s in "$FAKEOIDC_SECRET" "$SSO_SECRET_B"; do
  if grep -rqs -- "$s" "$BASE/serve.log" "$H/config.yaml"; then echo "an app's secret was written where it shouldn't be" >&2; exit 1; fi
done
# The forgotten app's secret went with it.
if grep -qs -- "$SSO_SECRET_B" "$H/.env"; then echo "the forgotten app's secret stayed" >&2; exit 1; fi
exit 0
