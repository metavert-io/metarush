#!/usr/bin/env bash
# workspace-export-e2e.sh — exporting a workspace from the dashboard and restoring it in a fresh home (D329–D333,
# docs/workspace-export.md). A throwaway installation is set up with a workspace, a project and a picture; its
# dashboard runs on 5670; web/scripts/workspace-export-e2e.mjs (MODE=export) opens Settings → Storage → Export the
# workspace (what an archive holds, the restore said to be a terminal step), checks what it would hold, exports
# through the styled confirm and waits for the archive, refuses a path named by the browser, and looks at a phone in
# both themes. The command line then agrees, refuses Claude Code, plans and restores the archive into a fresh home
# with `metarush setup --restore` (a person at a terminal: script(1), the way remote-e2e.sh plays one) and an empty
# database, and the browser (MODE=copy) opens the dashboard on the restored copy on 5671: the workspace, its
# project, the restore in the Log, and the copy's paused background deletions, lifted with "This copy is now the
# workspace…".
#
#   scripts/workspace-export-e2e.sh                  # needs scripts/dev-mongo.sh running and python3
#   KEEP=1 SKIP_BUILD=1 scripts/workspace-export-e2e.sh
#
# Ports (this run's own, 5670–5679): the original's dashboard 5670, the copy's 5671. Both throwaway databases are
# dropped on exit.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_EXPORT_PORT:-5670}"
COPY_PORT="$((PORT + 1))"
TMPBASE="${TMPDIR:-/tmp}"; TMPBASE="${TMPBASE%/}"
BASE="${METARUSH_EXPORT_DIR:-$(mktemp -d "$TMPBASE/workspace-export-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; HB="$BASE/copy-home"; USERHOME="$BASE/user"
DB="metarush-test-wsexport$(date +%s)"
DB2="${DB}copy"
URI="$(scripts/dev-mongo.sh start)"
MR_BIN="${MR_BIN:-$ROOT/bin/metarush}"
SRV=""; SRV2=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: homes $H and $HB, databases $DB and $DB2, dashboards http://127.0.0.1:$PORT and http://127.0.0.1:$COPY_PORT"
    return
  fi
  for p in $SRV $SRV2; do
    kill "$p" 2>/dev/null || true
    for _ in $(seq 1 60); do kill -0 "$p" 2>/dev/null || break; sleep 0.25; done
  done
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
  go run ./scripts/droptestdb "$URI" "$DB2" >/dev/null || true
  [ -z "${METARUSH_EXPORT_DIR:-}" ] && rm -rf "$BASE" || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its databases, removes its folder when it passes
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
for p in "$PORT" "$COPY_PORT"; do
  if nc -z 127.0.0.1 "$p" >/dev/null 2>&1; then die "port $p is in use (an earlier run left something running? scripts/lib/e2e.sh list)"; fi
done

if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary ($(scripts/version.sh))"
  npm run build -w web >/dev/null
  go build -ldflags "$(scripts/version.sh --ldflags)" -o "$MR_BIN" ./cmd/metarush
fi

mkdir -p "$USERHOME"
export METARUSH_NO_PAID_SERVICES=1 METARUSH_DEV_ORIGINS=""
unset METARUSH_MONGODB_URI METARUSH_HOME CLAUDE_CONFIG_DIR CLAUDECODE METARUSH_AGENT METARUSH_VIA
MR() { HOME="$USERHOME" "$MR_BIN" --home "$H" "$@"; }
MRB() { HOME="$USERHOME" "$MR_BIN" --home "$HB" "$@"; }
# A person at a terminal (exporting and restoring are a person's): script(1) gives the command one.
AS_PERSON() { script -q /dev/null "$@" </dev/null | tr -d '\r' | sed '1s/^[^{]*//'; }

step "the original: a workspace with a project and a picture"
MR setup --yes --json --no-components --database elsewhere --db-uri "$URI" --db-name "$DB" --test-db --secrets env --service=false --skills=false \
  --port "$PORT" --workspace-name "FernBrook Studio" --user-name "Maya Lin" --user-email "maya@example.com" >"$BASE/setup.json" || { cat "$BASE/setup.json"; die "setup failed"; }
PRJ="$(MR project create "Spring launch" --json | J 'd["data"]["project"]["id"]')"
MR add "$ROOT/tests/fixtures/images/dog.jpg" --project "$PRJ" --json >"$BASE/add.json" || { cat "$BASE/add.json"; die "adding a picture failed"; }
echo "project $PRJ with a picture"

step "the dashboard on 127.0.0.1:$PORT"
e2e_bg "$BASE/serve.log" env HOME="$USERHOME" "$MR_BIN" --home "$H" serve; SRV=$E2E_BG
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done
curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null || { tail -40 "$BASE/serve.log"; die "the dashboard didn't start"; }

step "the browser: Settings → Storage → Export the workspace"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="env HOME=$USERHOME $MR_BIN --home $H dashboard --print-link --no-open" MODE=export \
  EXPORT_SHOTS="${EXPORT_SHOTS:-$BASE/shots}" node web/scripts/workspace-export-e2e.mjs | tee "$BASE/browser.log" || { echo "--- dashboard log"; tail -40 "$BASE/serve.log"; exit 1; }
ARCHIVE="$(sed -n 's/^ARCHIVE=//p' "$BASE/browser.log" | tail -1)"
[ -n "$ARCHIVE" ] && [ -f "$ARCHIVE" ] || die "the browser run named no archive"
[ "$(stat -f '%Lp' "$ARCHIVE" 2>/dev/null || stat -c '%a' "$ARCHIVE")" = "600" ] || die "the archive isn't private (0600)"
echo "archive: $ARCHIVE ($(du -h "$ARCHIVE" | cut -f1))"

step "the command line agrees, and refuses Claude Code"
AS_PERSON env HOME="$USERHOME" "$MR_BIN" --home "$H" workspace exports --json >"$BASE/exports.json"
[ "$(J 'd["data"]["exports"][0]["state"]' <"$BASE/exports.json")" = done ] || { cat "$BASE/exports.json"; die "workspace exports doesn't list the archive"; }
if CLAUDECODE=1 MR workspace export --to "$BASE/by-claude" --json >"$BASE/claude.json" 2>&1; then die "Claude Code exported the workspace"; fi
[ "$(J 'd["error"]["code"]' <"$BASE/claude.json")" = E_HUMAN_ONLY ] || { cat "$BASE/claude.json"; die "Claude Code's export wasn't refused as people-only"; }
echo "Claude Code exporting: refused"

step "plan the restore into an empty database, then set a fresh home up from the archive"
AS_PERSON env HOME="$USERHOME" ARCHIVE_COPY_URI="$URI" "$MR_BIN" --home "$HB" workspace restore "$ARCHIVE" --plan \
  --db-uri-env ARCHIVE_COPY_URI --db-name "$DB2" --storage folder --media-root "$H/media" --json >"$BASE/plan.json"
J '"plan: %s records, %s of %s footage files found, every file checks out: %s" % (d["data"]["documents"], d["data"]["media"]["present"], d["data"]["media"]["files"], d["data"]["verified"])' <"$BASE/plan.json"
[ "$(J 'd["data"]["plan"] and d["data"]["verified"] and d["data"]["media"]["missing"] == 0' <"$BASE/plan.json")" = True ] || { cat "$BASE/plan.json"; die "the restore's plan"; }
AS_PERSON env HOME="$USERHOME" "$MR_BIN" --home "$HB" setup --yes --json --no-components --database elsewhere --db-uri "$URI" --db-name "$DB2" --test-db \
  --secrets env --service=false --skills=false --port "$COPY_PORT" --storage folder --media-root "$H/media" --restore "$ARCHIVE" >"$BASE/restore.json"
J '"restored: %s" % d["data"]["restored"]["says"]' <"$BASE/restore.json" || { cat "$BASE/restore.json"; die "setup --restore failed"; }
[ "$(J 'd["data"]["workspace"]["name"]' <"$BASE/restore.json")" = "FernBrook Studio" ] || die "the copy isn't the workspace"
[ "$(J 'd["data"]["user"]["name"]' <"$BASE/restore.json")" = "Maya Lin" ] || die "the copy's computer doesn't work as the owner"

step "the dashboard on the copy, 127.0.0.1:$COPY_PORT"
e2e_bg "$BASE/serve-copy.log" env HOME="$USERHOME" "$MR_BIN" --home "$HB" serve; SRV2=$E2E_BG
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$COPY_PORT/api/health" >/dev/null && break; sleep 0.25; done
curl -sf "http://127.0.0.1:$COPY_PORT/api/health" >/dev/null || { tail -40 "$BASE/serve-copy.log"; die "the copy's dashboard didn't start"; }
METARUSH_DASHBOARD="http://127.0.0.1:$COPY_PORT" METARUSH_MINT="env HOME=$USERHOME $MR_BIN --home $HB dashboard --print-link --no-open" MODE=copy \
  EXPORT_SHOTS="${EXPORT_SHOTS:-$BASE/shots}" node web/scripts/workspace-export-e2e.mjs || { echo "--- copy's dashboard log"; tail -40 "$BASE/serve-copy.log"; exit 1; }

echo; echo "workspace-export-e2e: passed"
exit 0
