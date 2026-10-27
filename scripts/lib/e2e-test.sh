#!/usr/bin/env bash
# scripts/lib/e2e-test.sh — checks scripts/lib/e2e.sh with stand-in runs (D194): a run that
# passes, fails, keeps everything (KEEP=1), is killed outright, is stopped with Ctrl-C, and one
# whose folder a person named. Each starts a stand-in server with a child, and one child that is
# left without its parent. No browser; with mongosh it also checks the run's database is dropped.
# Runs in a private temp folder and run list, so real runs are never touched. ~40 s.
#
#   scripts/lib/e2e-test.sh
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
LIB="$ROOT/scripts/lib/e2e.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/e2e-lib-test-XXXXXX")"
WORK="$(cd "$WORK" && pwd -P)"
export TMPDIR="$WORK/tmp/" METARUSH_E2E_RUNS="$WORK/runs"
mkdir -p "$WORK/tmp"
# The database part needs the test MongoDB and mongosh; without them it is skipped (CI's static job).
URI=""; MONGOSH=""
if nc -z 127.0.0.1 "${METARUSH_DEV_MONGO_PORT:-27018}" >/dev/null 2>&1; then
  URI="$("$ROOT/scripts/dev-mongo.sh" uri)"
  MONGOSH="$(command -v mongosh || true)"
fi
[ -n "$MONGOSH" ] || echo "(no test MongoDB with mongosh here: the database checks are skipped)"
failures=0
check() { if eval "$2"; then echo "PASS  $1"; else echo "FAIL  $1"; failures=$((failures + 1)); fi; }
trap 'pkill -f "e2e-lib-test-marker-$$" 2>/dev/null; rm -rf "$WORK"' EXIT

# A stand-in run: a server (a shell with a child), and a child whose parent ends after 3 s (it is
# left without its parent, the way a renderer's browser can be). Its database and folder look like
# a real run's. $1 = what happens at the end: pass | fail | keep | wait.
cat > "$WORK/run.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
cd "$ROOT"
MARK="e2e-lib-test-marker-$$"
DB="metarush-test-e2elib\$(date +%s)\$RANDOM"
BASE="\${METARUSH_E2E_DIR:-\$(mktemp -d "\${TMPDIR%/}/stand-in-XXXXXX")}"
HOMEDIR="\$BASE/home"
mkdir -p "\$HOMEDIR"
printf 'database:\n  name: %s\n' "\$DB" > "\$HOMEDIR/config.yaml"
URI="$URI"
SRV=""
cleanup() {
  echo "own cleanup ran (\$1)"
  [ "\${KEEP:-}" = "1" ] && return
  [ -n "\$SRV" ] && kill "\$SRV" 2>/dev/null
}
trap 'cleanup \$?' EXIT
. scripts/lib/e2e.sh && e2e_run "\$DB" "\$BASE"
echo "base=\$BASE db=\$DB state=\$E2E_STATE"
echo "services: keys=\$(env | grep -c '^METARUSH_[A-Z_]*_API_KEY=') refused=\${METARUSH_NO_PAID_SERVICES:-} gemini=\${METARUSH_GEMINI_BASE_URL:-unset} eleven=\${METARUSH_ELEVENLABS_BASE_URL:-unset} did=\${METARUSH_D_ID_BASE_URL:-unset}"
[ -n "$MONGOSH" ] && "$MONGOSH" --quiet "$URI" --eval "db.getSiblingDB('\$DB').t.insertOne({a:1})" >/dev/null
bash -c "exec -a \$MARK-server sleep 300" &
SRV=\$!
# A sleep left without its parent: the parent ends once the guard has seen the sleep.
bash -c 'sleep 300 & echo \$! >"\$1/orphan"; until [ -f "\$1/seen" ]; do sleep 0.1; done' _ "\$BASE" &
bash -c "exec -a \$MARK-child bash -c 'sleep 300 & echo \\\$! >\"\$BASE/grandchild\"; wait'" &
CHILD=\$!
# The guard lists what the run started every 2 s, later on a loaded machine: wait until it has
# seen these (two minutes at most), never a fixed time.
guard_saw() {
  local p ok
  for _ in \$(seq 1 1200); do
    ok=1
    for p in "\$@"; do { [ -n "\$p" ] && grep -q "^\$p|" "\$E2E_STATE/procs" 2>/dev/null; } || ok=0; done
    [ "\$ok" = 1 ] && return 0
    sleep 0.1
  done
}
for _ in \$(seq 1 1200); do [ -s "\$BASE/orphan" ] && [ -s "\$BASE/grandchild" ] && break; sleep 0.1; done
guard_saw "\$SRV" "\$CHILD" "\$(cat "\$BASE/orphan")" "\$(cat "\$BASE/grandchild")"
echo "sleeps=\$(cat "\$BASE/orphan") \$(cat "\$BASE/grandchild")"
touch "\$BASE/seen"
case "\${1:-pass}" in
  pass) exit 0 ;;
  fail) exit 3 ;;
  keep) KEEP=1; exit 0 ;;
  unset) echo "\$NOT_SET_ANYWHERE" ;; # set -u stops it: macOS's bash 3.2 then says 0 to the EXIT trap
  wait)
    sleep 300 &
    LAST=\$!
    echo "sleeps=\$LAST"
    guard_saw "\$LAST"
    touch "\$BASE/ready" # everything it started is on its guard's list
    wait "\$LAST" ;;
esac
exit 0
EOF
chmod +x "$WORK/run.sh"

running() { pgrep -f "e2e-lib-test-marker-$$" >/dev/null || pgrep -f "^sleep 300$" -P 1 >/dev/null; }
mine() { pgrep -f "e2e-lib-test-marker-$$" | wc -l | tr -d ' '; }
field() { sed -n -E "s/.*$1=([^ ]*).*/\\1/p" "$2" | head -1; }
dbthere() { [ -n "$MONGOSH" ] && "$MONGOSH" --quiet "$URI" --eval "db.adminCommand({listDatabases:1,nameOnly:true}).databases.some(d=>d.name=='$1')" 2>/dev/null | grep -q true; }
# The run's plain sleeps (the one left without its parent among them) by process ID: other runs
# of this test, the full check's included, start sleeps just like them at the same time.
sleeps_running() {
  local p
  for p in $(sed -n -E 's/^sleeps=//p' "$1" 2>/dev/null); do
    [ "$(ps -o stat= -o command= -p "$p" 2>/dev/null | awk '$1 !~ /Z/ {$1=""; sub(/^ /, ""); print}')" = "sleep 300" ] && return 0
  done
  return 1
}
# A loaded machine is slow, never wrong: wait for what a check looks at, with a generous deadline,
# instead of sleeping a fixed time. eventually <seconds> <command…>
eventually() {
  local end=$((SECONDS + $1))
  shift
  until "$@"; do
    [ "$SECONDS" -ge "$end" ] && return 1
    sleep 0.2
  done
}
all_stopped() { [ "$(mine)" = 0 ] && ! sleeps_running "$1"; } # all_stopped <the run's output>
none_of_mine() { [ "$(mine)" = 0 ]; }
started() { grep -q "^base=" "$1" 2>/dev/null; } # the run is under way (e2e_run is done)
ready() { [ -n "$1" ] && [ -f "$1/ready" ]; }    # its guard has seen everything it started

echo "== a run that passes"
# Keys and a real provider address in the shell (made-up values): the run never sees them.
METARUSH_GEMINI_API_KEY="made-up-$$" METARUSH_FAKE_API_KEY="made-up-$$" METARUSH_ELEVENLABS_BASE_URL="https://api.elevenlabs.io" \
  METARUSH_D_ID_BASE_URL="https://api.d-id.com" METARUSH_GEMINI_BASE_URL="http://127.0.0.1:5401" "$WORK/run.sh" pass > "$WORK/pass.out" 2>&1; code=$?
base="$(field base "$WORK/pass.out")"; db="$(field db "$WORK/pass.out")"; state="$(field state "$WORK/pass.out")"
check "it passed and its own cleanup ran first" '[ "$code" = 0 ] && grep -q "own cleanup ran (0)" "$WORK/pass.out"'
check "no AI service key from the shell reached it, a real address was dropped, a fake's kept, real services refused" \
  'grep -q "^services: keys=0 refused=1 gemini=http://127.0.0.1:5401 eleven=unset did=unset$" "$WORK/pass.out"'
check "everything it started was stopped (the one left without its parent too)" 'eventually 60 all_stopped "$WORK/pass.out"'
check "its folder was removed" '[ -n "$base" ] && [ ! -e "$base" ]'
check "its record is gone" '[ ! -e "$state" ]'
[ -n "$MONGOSH" ] && check "its database was dropped" '! dbthere "$db"'

echo "== a run that fails"
"$WORK/run.sh" fail > "$WORK/fail.out" 2>&1; code=$?
base="$(field base "$WORK/fail.out")"; db="$(field db "$WORK/fail.out")"; state="$(field state "$WORK/fail.out")"
check "it failed with its own status" '[ "$code" = 3 ] && grep -q "own cleanup ran (3)" "$WORK/fail.out"'
check "everything it started was stopped" 'eventually 60 all_stopped "$WORK/fail.out"'
check "its folder is kept for a look, and it says where" '[ -d "$base" ] && grep -q "kept for a look: $base" "$WORK/fail.out"'
check "its record says it failed" 'grep -q "^failed " "$state/result"'
[ -n "$MONGOSH" ] && check "its database was dropped" '! dbthere "$db"'
fail_base="$base"; fail_state="$state"

echo "== a run that an unset variable stops (set -u)"
"$WORK/run.sh" unset > "$WORK/unset.out" 2>&1; code=$?
base="$(field base "$WORK/unset.out")"; state="$(field state "$WORK/unset.out")"
check "it failed, though bash $(bash -c 'echo $BASH_VERSION') may report 0 into its exit handler" '[ "$code" != 0 ] && grep -q "unbound variable" "$WORK/unset.out" && grep -q "own cleanup ran (1)" "$WORK/unset.out"'
check "and said why" 'grep -q "ended without reaching an exit" "$WORK/unset.out"'
check "everything it started was stopped" 'eventually 60 all_stopped "$WORK/unset.out"'
check "its folder is kept for a look, and its record says it failed" '[ -d "$base" ] && grep -q "^failed " "$state/result"'

echo "== KEEP=1 keeps everything running"
"$WORK/run.sh" keep > "$WORK/keep.out" 2>&1
base="$(field base "$WORK/keep.out")"; db="$(field db "$WORK/keep.out")"; state="$(field state "$WORK/keep.out")"
check "its programs still run" '[ "$(mine)" -ge 2 ]'
check "its folder and record are kept" '[ -d "$base" ] && grep -q "^kept " "$state/result"'
[ -n "$MONGOSH" ] && check "its database is kept" 'dbthere "$db"'
bash "$LIB" sweep > "$WORK/sweep1.out" 2>&1
check "a sweep within the day leaves it alone" '[ "$(mine)" -ge 2 ] && [ -d "$base" ] && [ -d "$fail_base" ]'
bash "$LIB" sweep --now > "$WORK/sweep2.out" 2>&1
check "sweep --now stops its programs and removes its folder and record" 'eventually 60 none_of_mine && [ ! -e "$base" ] && [ ! -e "$state" ]'
[ -n "$MONGOSH" ] && check "and drops its database" '! dbthere "$db"'
check "sweep --now also removes the failed run's kept folder" '[ ! -e "$fail_base" ] && [ ! -e "$fail_state" ]'

echo "== a run killed outright: its guard cleans up"
"$WORK/run.sh" wait > "$WORK/kill.out" 2>&1 &
pid=$!
eventually 120 started "$WORK/kill.out"
base="$(field base "$WORK/kill.out")"; db="$(field db "$WORK/kill.out")"; state="$(field state "$WORK/kill.out")"
eventually 120 ready "$base"
kill -KILL "$pid"
{ wait "$pid"; } 2>/dev/null
# The guard notices within 2 s, stops what the run started (up to 10 s each before KILL), drops
# its database and records it: minutes at most on a loaded machine.
eventually 180 test -f "$state/result"
check "its guard stopped what it started" 'eventually 60 all_stopped "$WORK/kill.out"'
check "and recorded it, keeping the folder for a look" 'grep -q "^crashed " "$state/result" && [ -d "$base" ]'
[ -n "$MONGOSH" ] && check "and dropped its database" '! dbthere "$db"'
bash "$LIB" sweep --now >/dev/null 2>&1
check "sweep --now removes its folder" '[ ! -e "$base" ] && [ ! -e "$state" ]'

echo "== Ctrl-C"
set -m   # its own process group, like a terminal's foreground job
# With Ctrl-C's signal as a terminal delivers it: a test started in the background (a script's
# `&`, a detached check) passes SIGINT on ignored, and bash can't trap a signal ignored on entry.
python3 -I -c 'import os, signal, sys; signal.signal(signal.SIGINT, signal.SIG_DFL); os.execv(sys.argv[1], sys.argv[1:])' \
  "$WORK/run.sh" wait > "$WORK/int.out" 2>&1 &
pid=$!
set +m
eventually 120 started "$WORK/int.out"
base="$(field base "$WORK/int.out")"; state="$(field state "$WORK/int.out")"
eventually 120 ready "$base"
kill -INT -- "-$pid"; wait "$pid"; code=$?
check "it ends as interrupted, its own cleanup and ours ran" '[ "$code" = 130 ] && grep -q "own cleanup ran (130)" "$WORK/int.out" && eventually 60 all_stopped "$WORK/int.out"'
check "its folder is kept (it didn't pass)" '[ -d "$base" ] && grep -q "^failed " "$state/result"'
bash "$LIB" sweep --now >/dev/null 2>&1

echo "== a folder a person named is never removed"
named="$WORK/tmp/named-by-you"
mkdir -p "$named"
METARUSH_E2E_DIR="$named" "$WORK/run.sh" pass > "$WORK/named.out" 2>&1
check "the run passed and the named folder is still there" '[ -d "$named/home" ] && grep -q "kept (named by you" "$WORK/named.out"'

echo "== every script with an EXIT trap reports an unset variable as a failure"
# On macOS's bash 3.2, `set -eu` with an EXIT trap turns a stop on an unset variable into status
# 0 unless the script uses scripts/lib/status.sh (directly or through e2e.sh) and ends with `exit 0`.
bad=""
for f in "$ROOT"/scripts/*.sh "$ROOT"/bootstrap.sh; do
  grep -Eq '^[[:space:]]*(.*;[[:space:]]*)?trap .*EXIT' "$f" || continue
  grep -Eq '^set -[a-z]*e' "$f" || continue
  grep -Eq 'scripts/lib/(status|e2e)\.sh|lib/status\.sh' "$f" && [ "$(grep -v '^[[:space:]]*$' "$f" | tail -1)" = "exit 0" ] || bad="$bad $(basename "$f")"
done
check "they use scripts/lib/status.sh and end with \`exit 0\`" '[ -z "$bad" ]' 
[ -n "$bad" ] && echo "      not yet:$bad"

echo
[ "$failures" = 0 ] && echo "scripts/lib/e2e.sh: all checks passed" || echo "scripts/lib/e2e.sh: $failures check(s) failed"
[ "$failures" = 0 ]
