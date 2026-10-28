#!/usr/bin/env bash
# handover-e2e.sh — handing a computer's role to another computer (D344–D348, docs/handover.md). Two throwaway
# computers of one workspace on one database: "Studio Mac" (dashboard 5710) and "Edit mini" (5711), each with its own
# Claude Code folder (CLAUDE_CONFIG_DIR) and a fake `claude` that keeps sessions where Claude Code does. Studio Mac
# has a conversation in Claude's panel and a watched folder. The browser (web/scripts/handover-e2e.mjs) checks the dry
# run in Settings → Storage; the command line refuses Claude Code and makes the handover file at a person's terminal
# (script(1), the passphrase on standard input, never an argument); the browser checks the old computer while the
# conversation is moving; Edit mini receives the file (the session put where Claude Code looks there, the watched
# folder set where the person says) and the conversation goes on there in the same session; Studio Mac steps down by
# itself and starts again as a client, which the browser checks on every page; clean removes its copies.
#
#   scripts/handover-e2e.sh                  # needs scripts/dev-mongo.sh running, python3 and macOS's script(1)
#   KEEP=1 SKIP_BUILD=1 scripts/handover-e2e.sh
#
# Ports (this run's own, 5710–5719): Studio Mac's dashboard 5710, Edit mini's 5711. The throwaway database is dropped
# on exit.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT_A="${METARUSH_HANDOVER_PORT:-5710}"; PORT_B=$((PORT_A + 1))
TMPBASE="${TMPDIR:-/tmp}"; TMPBASE="${TMPBASE%/}"
BASE="${METARUSH_HANDOVER_DIR:-$(mktemp -d "$TMPBASE/handover-e2e-XXXXXX")}"
mkdir -p "$BASE"
HA="$BASE/studio"; HB="$BASE/mini"; USERHOME="$BASE/user"
CA="$BASE/claude-studio"; CB="$BASE/claude-mini"
DB="metarush-test-handover$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
MR_BIN="${MR_BIN:-$ROOT/bin/metarush}"
SA=""; SB=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: homes $HA and $HB, database $DB, dashboards http://127.0.0.1:$PORT_A and http://127.0.0.1:$PORT_B"
    return
  fi
  for p in $SA $SB; do
    kill "$p" 2>/dev/null || true
    for _ in $(seq 1 60); do kill -0 "$p" 2>/dev/null || break; sleep 0.25; done
  done
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
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
go build -o "$BASE/claude" ./internal/assistant/fakeclaude/cmd/fakeclaude
# The fake keeps each session where Claude Code does (under CLAUDE_CONFIG_DIR) and says how much it remembers.
printf '%s\n' '{"sessions":"claude","default":{"steps":[{"text":"I remember {{remembered}} messages."}],"costUsd":0.001}}' >"$BASE/claude.json"

mkdir -p "$USERHOME" "$CA" "$CB" "$BASE/inbox-mini"
export METARUSH_NO_PAID_SERVICES=1 METARUSH_DEV_ORIGINS=""
unset METARUSH_MONGODB_URI METARUSH_DATABASE_NAME METARUSH_HOME CLAUDE_CONFIG_DIR CLAUDECODE METARUSH_AGENT METARUSH_VIA
A() { HOME="$USERHOME" CLAUDE_CONFIG_DIR="$CA" METARUSH_CLAUDE="$BASE/claude" "$MR_BIN" --home "$HA" "$@"; }
B() { HOME="$USERHOME" CLAUDE_CONFIG_DIR="$CB" METARUSH_CLAUDE="$BASE/claude" "$MR_BIN" --home "$HB" "$@"; }
# A person at a terminal (a handover is a person's): script(1) gives the command one; the passphrase comes on
# standard input from the environment (printf is the shell's own, so it is in no program's arguments).
export HANDOVER_PASS="correct horse battery staple, for the handover run"
AS_PERSON() { script -q /dev/null "$@" </dev/null | tr -d '\r' | sed '1s/^[^{]*//'; }

step "two computers of one workspace, on one database"
METARUSH_MONGODB_URI="$URI" A init --yes --test-db --secrets env --db-name "$DB" --workspace-name "FernBrook Studio" --media filesystem \
  --media-root "$BASE/media" --media-shared --port "$PORT_A" --installation-name "Studio Mac" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$HA/.env"; chmod 600 "$HA/.env"
WS=$(A workspace show --json | J 'd["data"]["workspace"]["id"]')
METARUSH_MONGODB_URI="$URI" B init --yes --test-db --secrets env --db-name "$DB" --join "$WS" --media filesystem \
  --media-root "$BASE/media" --media-shared --port "$PORT_B" --installation-name "Edit mini" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$HB/.env"; chmod 600 "$HB/.env"
A watch set "$BASE/inbox-studio" --create --json >/dev/null

step "both dashboards, each with its own Claude Code folder"
e2e_bg "$BASE/studio.log" env HOME="$USERHOME" CLAUDE_CONFIG_DIR="$CA" METARUSH_CLAUDE="$BASE/claude" "$MR_BIN" --home "$HA" serve --no-worker; SA=$E2E_BG
e2e_bg "$BASE/mini.log" env HOME="$USERHOME" CLAUDE_CONFIG_DIR="$CB" METARUSH_CLAUDE="$BASE/claude" "$MR_BIN" --home "$HB" serve --no-worker; SB=$E2E_BG
for port in "$PORT_A" "$PORT_B"; do
  for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$port/api/health" >/dev/null && break; sleep 0.25; done
  curl -sf "http://127.0.0.1:$port/api/health" >/dev/null || { tail -40 "$BASE/studio.log" "$BASE/mini.log"; die "a dashboard didn't start"; }
done

step "a conversation in Claude's panel on Studio Mac"
A assistant ask "Hello there, let's plan the teaser." --wait --json >"$BASE/ask.log" || { tail -20 "$BASE/ask.log"; die "the first message failed"; }
CONV=$(A assistant conversations list --json | J 'd["data"]["conversations"][0]["id"]')
A assistant continue "$CONV" "Make it thirty seconds." --wait --json >"$BASE/continue.log" || { tail -20 "$BASE/continue.log"; die "the second message failed"; }
SID=$(A assistant conversations show "$CONV" --json | J 'd["data"]["claudeSessionId"]')
OLD_SESSION=$(find "$CA/projects" -name "$SID.jsonl" | head -1)
[ -n "$OLD_SESSION" ] || die "the fake kept no session where Claude Code would"
echo "conversation $CONV, session at $OLD_SESSION ($(wc -l <"$OLD_SESSION" | tr -d ' ') lines)"

step "the browser: Settings → Storage → Hand over this computer's role (the dry run)"
BROWSER_ENV=(METARUSH_DASHBOARD_A="http://127.0.0.1:$PORT_A" METARUSH_MINT_A="env HOME=$USERHOME $MR_BIN --home $HA dashboard --print-link --no-open"
  METARUSH_DASHBOARD_B="http://127.0.0.1:$PORT_B" METARUSH_MINT_B="env HOME=$USERHOME $MR_BIN --home $HB dashboard --print-link --no-open"
  HANDOVER_CONVERSATION="$CONV" HANDOVER_SHOTS="${HANDOVER_SHOTS:-$BASE/shots}")
browser() { env "${BROWSER_ENV[@]}" MODE="$1" node web/scripts/handover-e2e.mjs || { echo "--- studio log"; tail -40 "$BASE/studio.log"; echo "--- mini log"; tail -40 "$BASE/mini.log"; exit 1; }; }
browser plan

step "the command line: the dry run agrees, Claude Code is refused, a person makes the handover file"
A machine handover plan --to "Edit mini" --json >"$BASE/plan.json"
[ "$(J 'd["data"]["ready"] and d["data"]["conversations"][0]["session"] == "here"' <"$BASE/plan.json")" = True ] || { cat "$BASE/plan.json"; die "the plan isn't ready"; }
if CLAUDECODE=1 A machine handover export --to "Edit mini" --out "$BASE/by-claude.age" --json >"$BASE/by-claude.json" 2>&1; then die "Claude Code made a handover file"; fi
[ "$(J 'd["error"]["code"]' <"$BASE/by-claude.json")" = E_HUMAN_ONLY ] || { cat "$BASE/by-claude.json"; die "Claude Code's export wasn't refused as people-only"; }
echo "Claude Code making the handover file: refused"
AS_PERSON env HOME="$USERHOME" CLAUDE_CONFIG_DIR="$CA" MR_BIN="$MR_BIN" HA="$HA" OUT="$BASE/teaser.age" \
  sh -c 'printf "%s\n" "$HANDOVER_PASS" | "$MR_BIN" --home "$HA" machine handover export --to "Edit mini" --out "$OUT" --passphrase-stdin --json' >"$BASE/export.json"
J '"exported: %s" % d["data"]["summary"]' <"$BASE/export.json" || { cat "$BASE/export.json"; die "the export failed"; }
[ "$(J 'd["data"]["encrypted"] and d["data"]["sessions"] == 1' <"$BASE/export.json")" = True ] || die "the file isn't protected, or lacks the session"
[ "$(stat -f '%Lp' "$BASE/teaser.age" 2>/dev/null || stat -c '%a' "$BASE/teaser.age")" = "600" ] || die "the handover file isn't private (0600)"
[ "$(head -c 21 "$BASE/teaser.age")" = "age-encryption.org/v1" ] || die "the handover file isn't an age file"

step "the browser: Studio Mac while the conversation is moving"
browser moving
if A assistant continue "$CONV" "One more thing" --wait --json >"$BASE/refused.json" 2>&1; then die "Studio Mac continued a conversation it is handing over"; fi

step "Edit mini receives the file (the folder set where the person says)"
AS_PERSON env HOME="$USERHOME" CLAUDE_CONFIG_DIR="$CB" MR_BIN="$MR_BIN" HB="$HB" FILE="$BASE/teaser.age" FROM="$BASE/inbox-studio" TO="$BASE/inbox-mini" \
  sh -c 'printf "%s\n" "$HANDOVER_PASS" | "$MR_BIN" --home "$HB" machine handover receive "$FILE" --folder "$FROM=$TO" --passphrase-stdin --json' >"$BASE/receive.json"
J '"received: %s" % d["data"]["summary"]' <"$BASE/receive.json" || { cat "$BASE/receive.json"; die "the receive failed"; }
[ "$(J 'd["data"]["moved"]["conversations"] == 1 and d["data"]["moved"]["folders"] == 1' <"$BASE/receive.json")" = True ] || die "receive moved the wrong things"
NEW_SESSION=$(find "$CB/projects" -name "$SID.jsonl" | head -1)
[ -n "$NEW_SESSION" ] || die "the session wasn't put where Claude Code looks on Edit mini"
case "$(B watch status --json | J 'd["data"]["dir"]')" in */inbox-mini) ;; *) die "the watched folder isn't set on Edit mini" ;; esac

step "the conversation goes on on Edit mini, in the same session"
B assistant continue "$CONV" "Where were we?" --wait --json >"$BASE/goes-on.log" || { tail -20 "$BASE/goes-on.log"; die "the conversation didn't go on"; }
REPLY=$(B assistant conversations show "$CONV" --json | J '[e["text"] for e in d["data"]["events"] if e["kind"] == "text"][-1]')
[ "$REPLY" = "I remember 2 messages." ] || die "Claude Code on Edit mini didn't resume the session (it said: $REPLY)"
grep -q "moved to another computer" "$NEW_SESSION" || die "Claude wasn't told where the files are now"
echo "Edit mini: $REPLY"
browser new

step "Studio Mac steps down by itself and starts again as a client"
ROLE=""
for _ in $(seq 1 90); do
  ROLE=$(A machine handover status --json 2>/dev/null | J 'd["data"]["role"]["role"]' 2>/dev/null || true)
  [ "$ROLE" = client ] && break
  sleep 1
done
[ "$ROLE" = client ] || { tail -40 "$BASE/studio.log"; die "Studio Mac didn't step down"; }
for _ in $(seq 1 80); do grep -q "as a client" "$BASE/studio.log" && curl -sf "http://127.0.0.1:$PORT_A/api/health" >/dev/null && break; sleep 0.25; done
grep -q "as a client" "$BASE/studio.log" || { tail -40 "$BASE/studio.log"; die "Studio Mac's dashboard didn't start again as a client"; }
browser client

step "clean, at a person's terminal: Studio Mac's copies go, Edit mini's stay"
AS_PERSON env HOME="$USERHOME" CLAUDE_CONFIG_DIR="$CA" "$MR_BIN" --home "$HA" machine handover clean --yes --json >"$BASE/clean.json"
[ "$(J 'd["data"]["removed"]' <"$BASE/clean.json")" = True ] || { cat "$BASE/clean.json"; die "clean"; }
[ ! -e "$OLD_SESSION" ] || die "Studio Mac's copy of the session stayed"
[ -e "$NEW_SESSION" ] || die "clean touched Edit mini's session"

step "the passphrase is nowhere MetaRush writes"
A log list --json >"$BASE/log-a.json" 2>/dev/null || true
B log list --json >"$BASE/log-b.json" 2>/dev/null || true
if grep -rqF "$HANDOVER_PASS" "$HA" "$HB" "$BASE"/*.log "$BASE"/*.json; then die "the passphrase was written somewhere"; fi
echo "not in either home, the logs or the activity log"

echo; echo "handover-e2e: passed"
exit 0
