#!/usr/bin/env bash
# install-e2e.sh — the five-minute install (D253–D256) on a fresh, throwaway home: `metarush setup` with the suggested
# answers sets up a database on this computer (a stand-in for MongoDB's download, served from this computer: an
# archive made from a copy of the development mongod), storage in a folder, the dashboard's port and Claude Code's
# skills (into a throwaway home folder); then it is run again (nothing changes), `metarush database status` and doctor
# check it, and web/scripts/install-e2e.mjs opens the dashboard's first run in a real browser (Getting started: what
# works with no keys, what a key adds, Claude Code, the database), closes it, sees it leave once a project exists,
# and checks plain words and phone widths. At the end `metarush stop` stops MetaRush and its database.
#
#   scripts/install-e2e.sh                      # needs a mongod to copy (~/.local/mongodb, METARUSH_TEST_MONGOD or PATH)
#   KEEP=1 SKIP_BUILD=1 scripts/install-e2e.sh
#
# Ports (this run's own, 5460–5469): the dashboard 5460, the database on this computer 5461, the stand-in download
# 5462. No LaunchAgent is ever installed (--service=false), nothing goes to the keychain (--secrets env), and the
# person's own Claude Code folder is never touched (HOME is the run's own folder).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT=5460; DB_PORT=5461; DL_PORT=5462
TMPBASE="${TMPDIR:-/tmp}"; TMPBASE="${TMPBASE%/}"
BASE="${METARUSH_E2E_DIR:-$(mktemp -d "$TMPBASE/install-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; USERHOME="$BASE/user"; SERVE="$BASE/serve"
DB="metarush-test-install$(date +%s)"
MR_BIN="${MR_BIN:-$ROOT/bin/metarush}"
REAL_HOME="$HOME"
SA=""; DL=""
cleanup() {
  local code="$1"
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, dashboard http://127.0.0.1:$PORT (pid $SA), the database on 127.0.0.1:$DB_PORT"
    return
  fi
  [ -n "$SA" ] && kill "$SA" 2>/dev/null || true
  [ -n "$DL" ] && kill "$DL" 2>/dev/null || true
  sleep 1
  # The database on this computer is MetaRush's own: `metarush stop` asks it to shut down cleanly.
  if [ -f "$H/config.yaml" ]; then HOME="$USERHOME" "$MR_BIN" --home "$H" stop >/dev/null 2>&1 || true; fi
  if [ -f "$H/database/mongod.pid" ]; then
    pid="$(cat "$H/database/mongod.pid")"
    if kill -0 "$pid" 2>/dev/null && ps -p "$pid" -o comm= | grep -q mongod; then kill -TERM "$pid"; for _ in $(seq 1 60); do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done; fi
  fi
  if [ "$code" = "0" ] && [ -z "${METARUSH_E2E_DIR:-}" ]; then rm -rf "$BASE"; fi
}
trap 'cleanup $?' EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { HOME="$USERHOME" "$MR_BIN" --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }

for p in $PORT $DB_PORT $DL_PORT; do
  if (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then die "port $p is in use (an earlier run left something running? scripts/lib/e2e.sh list)"; fi
done
MONGOD="${METARUSH_TEST_MONGOD:-}"
[ -n "$MONGOD" ] || { [ -x "$REAL_HOME/.local/mongodb/bin/mongod" ] && MONGOD="$REAL_HOME/.local/mongodb/bin/mongod"; } || true
[ -n "$MONGOD" ] || MONGOD="$(command -v mongod || true)"
[ -x "$MONGOD" ] || die "no mongod to copy for the stand-in download (set METARUSH_TEST_MONGOD)"

if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary ($(scripts/version.sh))"
  npm run build -w web >/dev/null
  go build -ldflags "$(scripts/version.sh --ldflags)" -o "$MR_BIN" ./cmd/metarush
fi

step "a stand-in for MongoDB's download, served on 127.0.0.1:$DL_PORT"
VER="$("$MONGOD" --version | sed -n 's/^db version v\([0-9.]*\).*/\1/p')"
[ -n "$VER" ] || die "couldn't read the mongod's version"
SRC="$BASE/archive/mongodb-test-$VER"
mkdir -p "$SRC/bin" "$SERVE" "$USERHOME"
cp "$MONGOD" "$SRC/bin/mongod"                      # a copy: the original is never changed
LIC="$(dirname "$(dirname "$MONGOD")")/LICENSE-Community.txt"
[ -f "$LIC" ] && cp "$LIC" "$SRC/" || echo "Server Side Public License" >"$SRC/LICENSE-Community.txt"
(cd "$BASE/archive" && tar -cf - "mongodb-test-$VER" | gzip -1 >"$SERVE/mongodb.tgz")
SHA="$(shasum -a 256 "$SERVE/mongodb.tgz" | awk '{print $1}')"
printf '{"version":"%s","url":"http://127.0.0.1:%s/mongodb.tgz","sha256":"%s"}\n' "$VER" "$DL_PORT" "$SHA" >"$BASE/pin.json"
e2e_bg "$BASE/download.log" python3 -I -m http.server "$DL_PORT" --bind 127.0.0.1 --directory "$SERVE"; DL=$E2E_BG
for _ in $(seq 1 40); do curl -sf -o /dev/null "http://127.0.0.1:$DL_PORT/" && break; sleep 0.25; done
echo "MongoDB $VER, $(du -h "$SERVE/mongodb.tgz" | awk '{print $1}'), SHA-256 ${SHA:0:12}…"
export METARUSH_MONGODB_PIN="$BASE/pin.json"
unset METARUSH_MONGODB_URI METARUSH_HOME CLAUDE_CONFIG_DIR

step "metarush setup --yes --json (the suggested answers, unattended)"
MR setup --yes --json --no-components --secrets env --db-name "$DB" --test-db --database-port "$DB_PORT" --port "$PORT" --service=false \
  --workspace-name "Install test" --user-name "Sam Lee" --user-email "sam@example.com" >"$BASE/setup.json" || { cat "$BASE/setup.json"; die "setup failed"; }
J '"fresh %s; database here %s, running %s, port %s, MongoDB %s; storage %s; skills %s (%s files); service wanted %s; next %s" % (d["data"]["fresh"], d["data"]["database"]["local"], d["data"]["database"]["running"], d["data"]["database"]["port"], d["data"]["database"]["version"], d["data"]["storage"]["kind"], d["data"]["skills"]["installed"], d["data"]["skills"]["files"], d["data"]["service"]["wanted"], d["data"]["next"])' <"$BASE/setup.json"
[ "$(J 'd["data"]["fresh"] and d["data"]["database"]["local"] and d["data"]["database"]["running"] and d["data"]["database"]["port"]=='"$DB_PORT"' and d["data"]["storage"]["kind"]=="folder" and d["data"]["skills"]["installed"]' <"$BASE/setup.json")" = True ] || die "setup's result isn't what was asked for"
J '"\n".join("  %s %s: %s" % ("ready" if w["ready"] else "not yet", w["name"], w["says"]) for w in d["data"]["whatWorks"]["noKeys"] + d["data"]["whatWorks"]["withKeys"])' <"$BASE/setup.json"
[ -f "$USERHOME/.claude/skills/video-render/SKILL.md" ] || die "the skills aren't in Claude Code's folder"
grep -q "mongodb://" "$H/config.yaml" && die "config.yaml holds the database address"
grep -q "^METARUSH_MONGODB_URI=" "$H/.env" || die "the home's .env has no database address"
grep -q "metarush-admin" "$H/.env" && die "the home's .env holds the administrator's address"
[ "$(stat -f %Lp "$H/database/admin.secret" 2>/dev/null || stat -c %a "$H/database/admin.secret")" = 600 ] || die "the administrator's address isn't in its own private file"
grep -q "metarush-admin" "$H/config.yaml" && die "config.yaml names the database's administrator"
# Without a sign-in the database refuses everything (--auth): doctor's check connects without one.
[ "$(MR doctor --json | J 'any(c["name"] == "sign-in required" and c["status"] == "ok" for c in d["data"]["checks"])')" = True ] || die "the database answers without a sign-in"
echo "a connection without a sign-in is refused"
[ "$(stat -f %Lp "$H/database/keyfile" 2>/dev/null || stat -c %a "$H/database/keyfile")" = 600 ] || die "the database's key file isn't private"

step "metarush setup again (nothing changes), database status, doctor"
MR setup --yes --json --no-components --service=false >"$BASE/again.json"
[ "$(J 'd["data"]["fresh"]' <"$BASE/again.json")" = False ] || die "setup again made something new"
[ "$(J 'd["data"]["workspace"]["id"]' <"$BASE/again.json")" = "$(J 'd["data"]["workspace"]["id"]' <"$BASE/setup.json")" ] || die "setup again changed the workspace"
MR setup --yes --no-components --service=false | sed -n '1,12p'
MR database status
MR database status --json | J '"database status: running %s, version %s, port %s, %s bytes" % (d["data"]["status"]["running"], d["data"]["status"]["version"], d["data"]["status"]["port"], d["data"]["status"]["dataBytes"])'
MR doctor --json | J '"\n".join("  doctor %s %s: %s" % (c["status"], c["area"], c["detail"]) for c in d["data"]["checks"] if c["area"] in ("database", "claude code"))'

step "the dashboard (no worker), and the first run in a browser"
e2e_bg "$BASE/dashboard.log" env HOME="$USERHOME" METARUSH_DEV_ORIGINS="" "$MR_BIN" --home "$H" serve --no-worker; SA=$E2E_BG
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done
curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null || { tail -40 "$BASE/dashboard.log"; die "the dashboard didn't start"; }
SHOTS="${INSTALL_SHOTS:-$BASE/shots}"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="env HOME=$USERHOME $MR_BIN --home $H dashboard --print-link --no-open" \
  METARUSH_MR="env HOME=$USERHOME $MR_BIN --home $H" INSTALL_SHOTS="$SHOTS" \
  node web/scripts/install-e2e.mjs || { echo "--- dashboard log"; tail -40 "$BASE/dashboard.log"; exit 1; }
echo "screenshots: $SHOTS"

step "metarush stop: MetaRush and its database"
kill "$SA" 2>/dev/null || true
for _ in $(seq 1 40); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null || break; sleep 0.25; done
SA=""
MR stop --json | J '"stopped %s, database stopped %s" % (d["data"]["stopped"], d["data"]["databaseStopped"])'
if (exec 3<>"/dev/tcp/127.0.0.1/$DB_PORT") 2>/dev/null; then die "the database still answers after metarush stop"; fi
echo; echo "install-e2e: passed"
exit 0
