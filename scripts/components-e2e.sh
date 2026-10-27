#!/usr/bin/env bash
# components-e2e.sh — MetaRush with only what it needs, and its optional components added from the
# dashboard (D324–D327), on a throwaway installation that sees none of this computer's programs
# (METARUSH_TOOL_DIRS names one empty folder) and whose Homebrew is a stand-in
# (internal/testutil/fakebrew: nothing is downloaded or installed). `metarush setup --yes
# --no-components` sets it up with only the required components; the command line says what is
# missing; the dashboard starts; then web/scripts/components-e2e.mjs opens Home's Getting started
# (Add it beside what isn't ready, More you can add) and Settings → Components (what is needed, what
# can be added, Suggested, sizes), adds PDF pages from Settings and FFmpeg from Getting started
# through styled confirms, sees the renderer's confirm say Remotion's licence, and checks plain
# words, no native dialogs and phone widths. The command line then agrees.
#
#   scripts/components-e2e.sh                  # needs scripts/dev-mongo.sh running and python3
#   KEEP=1 SKIP_BUILD=1 scripts/components-e2e.sh
#
# Ports (this run's own, 5650–5659): the dashboard 5650. The throwaway database is dropped on exit.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_COMPONENTS_PORT:-5650}"
TMPBASE="${TMPDIR:-/tmp}"; TMPBASE="${TMPBASE%/}"
BASE="${METARUSH_COMPONENTS_DIR:-$(mktemp -d "$TMPBASE/components-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; USERHOME="$BASE/user"; BIN="$BASE/bin"
DB="metarush-test-components$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
MR_BIN="${MR_BIN:-$ROOT/bin/metarush}"
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
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
if (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null; then die "port $PORT is in use (an earlier run left something running? scripts/lib/e2e.sh list)"; fi

if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary ($(scripts/version.sh))"
  npm run build -w web >/dev/null
  go build -ldflags "$(scripts/version.sh --ldflags)" -o "$MR_BIN" ./cmd/metarush
fi

step "a bare computer: no programs MetaRush can find, and a stand-in Homebrew"
mkdir -p "$BIN" "$USERHOME"
go run ./internal/testutil/fakebrew/cmd/fake-brew "$BASE/brew" "$BIN" "$BASE/brew.log"
export METARUSH_TOOL_DIRS="$BIN" METARUSH_BREW="$BASE/brew" METARUSH_NO_PAID_SERVICES=1 METARUSH_DEV_ORIGINS=""
unset METARUSH_MONGODB_URI METARUSH_HOME CLAUDE_CONFIG_DIR CLAUDECODE METARUSH_AGENT METARUSH_COMPONENTS_BASE
MR() { HOME="$USERHOME" "$MR_BIN" --home "$H" "$@"; }

step "metarush setup --yes --no-components (only what MetaRush needs)"
MR setup --yes --json --no-components --database elsewhere --db-uri "$URI" --db-name "$DB" --test-db --secrets env --service=false --skills=false \
  --port "$PORT" --workspace-name "Components test" --user-name "Sam Lee" --user-email "sam@example.com" >"$BASE/setup.json" || { cat "$BASE/setup.json"; die "setup failed"; }
J '"chosen %s, left out %s" % (d["data"]["components"]["chosen"], ", ".join(d["data"]["components"]["leftOut"]))' <"$BASE/setup.json"
[ "$(J '"renderer" in d["data"]["components"]["leftOut"] and not d["data"]["components"]["added"]' <"$BASE/setup.json")" = True ] || die "setup added components"
J '"\n".join("  %s %s: %s" % ("ready" if w["ready"] else "not yet", w["name"], w.get("next") or w["says"]) for w in d["data"]["whatWorks"]["noKeys"])' <"$BASE/setup.json"

step "metarush components: only the required ones are here"
MR components
[ "$(MR components --json | J 'all(c["ready"] for c in d["data"]["components"] if c["required"]) and not any(c["ready"] for c in d["data"]["components"] if c["name"] in ("ffmpeg","renderer","transcription","pdf-pages"))')" = True ] || die "the command line doesn't say only the required components are here"
# Adding is a person's: a command with no terminal (as an agent would run it) is refused.
if MR components add pdf-pages </dev/null >/dev/null 2>&1; then die "components add ran without a person"; fi
echo "components add without a person: refused"

step "the dashboard on 127.0.0.1:$PORT, with only the required components"
e2e_bg "$BASE/serve.log" env HOME="$USERHOME" "$MR_BIN" --home "$H" serve; SRV=$E2E_BG
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done
curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null || { tail -40 "$BASE/serve.log"; die "the dashboard didn't start"; }
if grep -qi "Setting up the renderer" "$BASE/serve.log"; then die "the renderer set itself up although it was left out"; fi

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: HOME=$USERHOME $MR_BIN --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="env HOME=$USERHOME $MR_BIN --home $H dashboard --print-link --no-open" \
  COMPONENTS_SHOTS="${COMPONENTS_SHOTS:-$BASE/shots}" node web/scripts/components-e2e.mjs || { echo "--- dashboard log"; tail -40 "$BASE/serve.log"; exit 1; }

step "the command line agrees with what the browser did"
MR components --json | J '"\n".join("  %s: %s" % (c["name"], c["status"]) for c in d["data"]["components"] if c["name"] in ("ffmpeg","pdf-pages","renderer"))'
[ "$(MR components --json | J '{c["name"]: c["ready"] for c in d["data"]["components"]}["pdf-pages"] and {c["name"]: c["ready"] for c in d["data"]["components"]}["ffmpeg"]')" = True ] || die "PDF pages and FFmpeg aren't ready"
grep -q "install --formula poppler" "$BASE/brew.log" && grep -q "install --formula ffmpeg" "$BASE/brew.log" || die "the stand-in Homebrew wasn't asked for poppler and ffmpeg"
echo "Homebrew was asked: $(tr '\n' ';' <"$BASE/brew.log")"
echo; echo "components-e2e: passed"
exit 0
