#!/usr/bin/env bash
# scripts/lib/e2e.sh — what every browser run (scripts/*-e2e.sh) and scripts/acceptance.sh
# share (D194, docs/testing.md): it keeps track of what a run starts and cleans up after it.
#
# In a script, right after its own `trap … EXIT` (one line, so merges stay simple):
#
#   . scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"      # the run's database, then the folders it makes
#
# - On exit, after the script's own cleanup, everything the run started that still runs is
#   stopped (daemons, workers, fake servers and their children, Remotion's Chromium too:
#   TERM, then KILL after 10 s; never a mongod), then the run's databases are dropped (the one
#   named, and any metarush-test-* a config.yaml in its folders names; only through
#   scripts/droptestdb), and its folders are removed when the run passed. A failed run keeps its
#   folders and says where.
# - KEEP=1 keeps everything, still running, for a look; a later run stops and removes it a day
#   later (or `scripts/lib/e2e.sh sweep --now`).
# - A guard (this file run as `guard`, in the background, in its own process group) keeps the
#   list of what the run started, so a run killed outright (no exit handler) is still cleaned up:
#   its programs stopped and its databases dropped; its folders stay for a day.
# - At the start of every run, and by hand with `sweep`, what earlier runs left is cleaned up:
#   kept folders older than a day, only when a config.yaml in them names a metarush-test-
#   database, and only folders under the temp folder or the checkout's .agent-home.
# - A folder you named (METARUSH_HOME or any METARUSH_…_DIR / METARUSH_…_HOME) is never removed.
# - Never a paid AI service (D224): every provider key in the environment (METARUSH_*_API_KEY) is
#   dropped, a provider address that isn't on this computer is dropped too, and
#   METARUSH_NO_PAID_SERVICES=1 makes every program the run starts refuse the real services'
#   addresses, as under `go test`. A script that uses fakes sets their addresses after e2e_run (in
#   the environment, or in the home's .env, which the environment would otherwise override).
# - COVERAGE=1: `go build` in the script makes coverage builds (D195) and every program the run
#   starts writes its counters to .coverage/e2e/<run> (METARUSH_COVERAGE_DIR moves .coverage);
#   scripts/coverage.sh merges them.
#
# By hand:
#   scripts/lib/e2e.sh sweep [--now]   # clean up after runs that are gone (--now: kept ones too)
#   scripts/lib/e2e.sh list            # the runs it knows about
#
# New scripts can start background programs with `e2e_bg <log> <command…>` (their own process
# group, stopped with everything they started). Works with macOS's bash 3.2.
#
# A script ends with `exit 0`: on macOS's bash 3.2 a run that an unset variable stops (`set -u`)
# reaches its exit handler with status 0, so the handler counts a run that ended without `exit`
# as failed (scripts/lib/status.sh).

E2E_LIB="${BASH_SOURCE[0]}"
. "$(dirname "$E2E_LIB")/status.sh"
E2E_LIB_ROOT="$(cd "$(dirname "$E2E_LIB")/../.." && pwd)"
E2E_RUNS="${METARUSH_E2E_RUNS:-${TMPDIR:-/tmp}}"
E2E_RUNS="${E2E_RUNS%/}"
[ -n "${METARUSH_E2E_RUNS:-}" ] || E2E_RUNS="$E2E_RUNS/metarush-e2e-runs"
E2E_KEEP_SECONDS=$((24 * 3600))
E2E_MODULE="github.com/metavert-io/metarush"

_e2e_say() { printf '[e2e] %s\n' "$*" >&2; }

_e2e_mtime() { stat -c %Y "$1" 2>/dev/null || stat -f %m "$1" 2>/dev/null || echo 0; }

# _e2e_procs <pid> [skip]: one line per program under pid (not pid itself, nor skip and what is
# under it): "pid|start time|program". The start time tells a program from a later one that got
# the same process ID. A mongod is never listed (the shared test database server).
_e2e_procs() {
  ps -A -o pid= -o ppid= -o lstart= -o comm= 2>/dev/null | awk -v root="$1" -v skip="${2:-0}" '
    {
      pid = $1; ppid = $2; start = $3 " " $4 " " $5 " " $6 " " $7
      comm = $0
      sub(/^[ \t]*[0-9]+[ \t]+[0-9]+[ \t]+[^ \t]+[ \t]+[^ \t]+[ \t]+[^ \t]+[ \t]+[^ \t]+[ \t]+[^ \t]+[ \t]+/, "", comm)
      info[pid] = pid "|" start "|" comm; prog[pid] = comm; kids[ppid] = kids[ppid] " " pid
    }
    END {
      n = 0; q[++n] = root; seen[root] = 1
      for (i = 1; i <= n; i++) {
        m = split(kids[q[i]], ks, " ")
        for (j = 1; j <= m; j++) {
          k = ks[j]
          if (k == "" || (k in seen) || k == skip) continue
          seen[k] = 1; q[++n] = k
          if (prog[k] ~ /(^|\/)mongod$/) continue
          print info[k]
        }
      }
    }'
}

# _e2e_running <pid>: a process with this ID runs (an ended one waiting to be reaped doesn't).
_e2e_running() {
  local st
  st="$(ps -o stat= -p "$1" 2>/dev/null)" || return 1
  case "$st" in "" | *Z*) return 1 ;; esac
  return 0
}

# _e2e_alive <"pid|start|program">: that very program still runs.
_e2e_alive() {
  local pid="${1%%|*}" rest now
  rest="${1#*|}"
  _e2e_running "$pid" || return 1
  now="$(ps -o lstart= -o comm= -p "$pid" 2>/dev/null | tr -s ' \t' '  ' | sed -e 's/^ //' -e 's/ $//')"
  [ -n "$now" ] && [ "$now" = "$(printf '%s %s' "${rest%%|*}" "${rest#*|}" | tr -s ' \t' '  ')" ]
}

# _e2e_stop <file>: stops the programs listed in it that still run: TERM, up to 10 s for them to
# go, then KILL.
_e2e_stop() {
  local f="$1" line pids="" pid left i n=0 names=""
  [ -s "$f" ] || return 0
  while IFS= read -r line; do
    if [ -n "$line" ] && _e2e_alive "$line" && kill -TERM "${line%%|*}" 2>/dev/null; then
      pids="$pids ${line%%|*}"
      names="$names $(basename "${line##*|}")"
      n=$((n + 1))
    fi
  done < "$f"
  [ "$n" -gt 0 ] || return 0
  for i in $(seq 1 40); do
    left=""
    for pid in $pids; do _e2e_running "$pid" && left="$left $pid"; done
    pids="$left"
    [ -n "$pids" ] || break
    sleep 0.25
  done
  if [ -n "$pids" ]; then
    while IFS= read -r line; do
      case " $pids " in *" ${line%%|*} "*) _e2e_alive "$line" && kill -KILL "${line%%|*}" 2>/dev/null ;; esac
    done < "$f"
  fi
  _e2e_say "stopped $n program(s) the run had started and left running:$names"
  return 0
}

# _e2e_named <folder>: a folder a person named (METARUSH_HOME, METARUSH_…_DIR or METARUSH_…_HOME:
# the folder itself, or anything inside it).
_e2e_named() {
  local f="${1%/}" v rv x
  while IFS= read -r v; do
    [ -n "$v" ] || continue
    v="${v%/}"
    rv="$(cd "$v" 2>/dev/null && pwd -P || printf '%s' "$v")"
    for x in "$v" "$rv"; do
      case "$x/" in "$f"/*) return 0 ;; esac
    done
  done <<EOF
$(env | sed -n -E 's/^METARUSH_([A-Z0-9_]*_)?(DIR|HOME)=(.*)$/\3/p')
EOF
  return 1
}

# _e2e_removable <folder>: a run's own folder that may go: not named by a person, under the temp
# folder or the checkout's .agent-home (never those themselves), holding a home whose config.yaml
# names a metarush-test- database.
_e2e_removable() {
  local f="$1" real tmp
  [ -d "$f" ] || return 1
  real="$(cd "$f" 2>/dev/null && pwd -P)" || return 1
  _e2e_named "$real" && return 1
  _e2e_named "$f" && return 1
  tmp="$(cd "${TMPDIR:-/tmp}" 2>/dev/null && pwd -P || echo /tmp)"
  case "$real/" in
    "$tmp"/?*/ | /tmp/?*/ | /private/tmp/?*/ | /private/var/folders/?*/T/?*/ | "$E2E_LIB_ROOT"/.agent-home/?*/) ;;
    *) return 1 ;;
  esac
  find "$real" -maxdepth 3 -name config.yaml -type f -exec grep -l 'metarush-test-' {} + 2>/dev/null | grep -q .
}

# _e2e_dbs <db> <folder…>: the databases a run used: the one it named and any metarush-test-*
# a config.yaml in its folders names.
_e2e_dbs() {
  local db="$1" f
  shift
  {
    [ -n "$db" ] && printf '%s\n' "$db"
    for f in "$@"; do
      [ -d "$f" ] && find "$f" -maxdepth 3 -name config.yaml -type f -exec grep -ho 'metarush-test-[A-Za-z0-9_-]*' {} + 2>/dev/null
    done
  } | sort -u
}

# _e2e_drop <uri> <db…>: drops test databases through scripts/droptestdb (store.DropTestDatabase:
# only local metarush-test-*).
_e2e_drop() {
  local uri="$1" db hp
  shift
  [ -n "$uri" ] || return 0
  # Nothing to drop when the database server isn't there (a quick look, not a long connect).
  hp="${uri#*://}"; hp="${hp#*@}"; hp="${hp%%/*}"; hp="${hp%%\?*}"; hp="${hp%%,*}"
  case "$hp" in *:*) ;; *) hp="$hp:27017" ;; esac
  if ! (exec 3<>"/dev/tcp/${hp%:*}/${hp##*:}") 2>/dev/null; then
    [ "$#" -gt 0 ] && _e2e_say "no database server at ${hp}: nothing dropped"
    return 0
  fi
  for db in "$@"; do
    case "$db" in metarush-test-?*) ;; *) continue ;; esac
    (cd "$E2E_LIB_ROOT" && command go run ./scripts/droptestdb "$uri" "$db" >/dev/null 2>&1) || true
  done
  return 0
}

# _e2e_write_run <state>: the run's record (read back by the guard, the sweep and list).
_e2e_write_run() {
  local state="$1" f
  {
    printf 'R_SCRIPT=%q\nR_PID=%q\nR_STARTED=%q\nR_DB=%q\nR_URI=%q\nR_FOLDERS=(' \
      "$E2E_SCRIPT" "$$" "$E2E_STARTED" "${E2E_DB:-}" "${E2E_URI:-}"
    for f in ${E2E_FOLDERS[@]+"${E2E_FOLDERS[@]}"}; do printf ' %q' "$f"; done
    printf ' )\n'
  } > "$state/run"
}

# _e2e_no_paid_services: nothing a run starts can reach a paid AI service (D224), even a coverage
# build (not under `go test`) or a script that forgot its fakes: keys from the shell are dropped,
# addresses that aren't on this computer are dropped, and the programs refuse real addresses.
_e2e_no_paid_services() {
  local v val
  for v in $(compgen -e); do
    case "$v" in
      METARUSH_*_API_KEY) unset "$v" ;;
      METARUSH_GEMINI_BASE_URL | METARUSH_ELEVENLABS_BASE_URL | METARUSH_D_ID_BASE_URL | METARUSH_HEYGEN_BASE_URL)
        val="${!v}"
        case "$val" in
          http://127.0.0.1:* | http://localhost:* | https://127.0.0.1:* | https://localhost:*) ;;
          *) unset "$v" ;;
        esac
        ;;
    esac
  done
  export METARUSH_NO_PAID_SERVICES=1
}

# e2e_run <database> [folder…]: called once by a script, right after its own EXIT trap.
e2e_run() {
  E2E_DB="${1:-}"
  shift || true
  E2E_FOLDERS=("$@")
  E2E_SCRIPT="$(basename "$0")"
  E2E_STARTED="$(date +%s)"
  E2E_URI="${URI:-}"
  [ -n "$E2E_URI" ] || E2E_URI="$("$E2E_LIB_ROOT/scripts/dev-mongo.sh" uri 2>/dev/null || true)"
  E2E_ID="${E2E_SCRIPT%.sh}-$$-$E2E_STARTED"
  E2E_STATE="$E2E_RUNS/$E2E_ID"
  E2E_GUARD=""
  # Earlier runs first (never this one: it isn't recorded yet).
  _e2e_sweep "" || true
  mkdir -p "$E2E_STATE"
  _e2e_write_run "$E2E_STATE"
  _e2e_no_paid_services
  if [ "${COVERAGE:-}" = "1" ]; then
    E2E_COVERAGE="${METARUSH_COVERAGE_DIR:-$E2E_LIB_ROOT/.coverage}/e2e/$E2E_ID"
    mkdir -p "$E2E_COVERAGE"
    export GOCOVERDIR="$E2E_COVERAGE"
    # The script's own `go build`s make coverage builds; the main packages are instrumented too,
    # or a binary writes nothing (internal/coverflush).
    go() {
      if [ "${1:-}" = "build" ]; then
        shift
        command go build -cover -covermode=atomic -coverpkg="$E2E_MODULE/internal/...,$E2E_MODULE/cmd/..." "$@"
      else
        command go "$@"
      fi
    }
    # The dashboard: a build with source maps beside its bundles, and every page the browser run's
    # script opens records what it ran (web/scripts/coverage/collect.mjs).
    export METARUSH_WEB_SOURCEMAP=1
    export METARUSH_WEB_COVERAGE_DIR="${METARUSH_COVERAGE_DIR:-$E2E_LIB_ROOT/.coverage}/web-e2e/$E2E_ID"
    node() {
      case "${1:-}" in
        web/scripts/*e2e*.mjs | */web/scripts/*e2e*.mjs) command node --import "$E2E_LIB_ROOT/web/scripts/coverage/collect.mjs" "$@" ;;
        *) command node "$@" ;;
      esac
    }
    _e2e_say "coverage: every program this run starts writes to $E2E_COVERAGE (the dashboard's pages to $METARUSH_WEB_COVERAGE_DIR)"
  fi
  # The script's own EXIT trap runs first, then ours; a signal ends the run through them.
  local prev
  prev="$(trap -p EXIT)"
  E2E_PREV_EXIT=""
  if [ -n "$prev" ]; then
    eval "set -- $prev"
    E2E_PREV_EXIT="${3:-}"
  fi
  trap '_e2e_exit $?' EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  # The guard, in its own process group: stopping the run's group doesn't stop it.
  set -m
  bash "$E2E_LIB" guard "$$" "$E2E_STATE" >/dev/null 2>>"$E2E_STATE/guard.log" </dev/null &
  E2E_GUARD=$!
  set +m
}

# e2e_bg <log> <command…>: starts a program in the background in its own process group (it and
# everything it starts stop with the run). Its process ID is in E2E_BG.
e2e_bg() {
  local log="$1"
  shift
  set -m
  "$@" >"$log" 2>&1 </dev/null &
  E2E_BG=$!
  set +m
  echo "$E2E_BG" >>"$E2E_STATE/groups"
}

_e2e_exit() {
  local code
  code="$(exit_status "$1")"
  set +e
  if [ "$code" != "$1" ]; then
    _e2e_say "the run ended without reaching an exit (an unset variable stopped it, see above; or the script lacks its final \`exit 0\`): it failed"
  fi
  trap - HUP INT TERM
  if [ -n "${E2E_PREV_EXIT:-}" ]; then
    # The script's cleanup, with $? as the run's exit status.
    eval "${E2E_PREV_EXIT//\$\?/$code}"
  fi
  _e2e_finish "$code"
  exit "$code"
}

# _e2e_finish <exit status>: what every run does last. Interrupted (a second Ctrl-C), the guard
# finishes the job.
_e2e_finish() {
  local code="$1" state="${E2E_STATE:-}" list f kept="" removed="" g
  [ -n "$state" ] && [ -d "$state" ] || return 0
  list="$state/stop"
  { _e2e_procs "$$" "${E2E_GUARD:-0}"; [ -f "$state/procs" ] && cat "$state/procs"; } | sort -u >"$list"
  if [ "${KEEP:-}" = "1" ]; then
    cp "$list" "$state/procs"
    printf 'kept %s\n' "$(date +%s)" >"$state/result"
    : >"$state/done"
    _e2e_stop_guard
    _e2e_say "KEEP=1: everything is kept and still runs; a browser run a day from now stops and removes it (now: scripts/lib/e2e.sh sweep --now)"
    return 0
  fi
  if [ -f "$state/groups" ]; then
    while IFS= read -r g; do kill -TERM -- "-$g" 2>/dev/null; done <"$state/groups"
  fi
  _e2e_stop "$list"
  # The databases, once nothing can write to them any more.
  _e2e_drop "${E2E_URI:-}" $(_e2e_dbs "${E2E_DB:-}" ${E2E_FOLDERS[@]+"${E2E_FOLDERS[@]}"})
  : >"$state/done"
  _e2e_stop_guard
  if [ "$code" = "0" ]; then
    for f in ${E2E_FOLDERS[@]+"${E2E_FOLDERS[@]}"}; do
      if _e2e_removable "$f"; then
        rm -rf "$f" && removed="$removed $f"
      elif [ -d "$f" ]; then
        kept="$kept $f"
      fi
    done
    [ -n "$removed" ] && _e2e_say "the run passed: removed$removed"
    [ -n "$kept" ] && _e2e_say "kept (named by you, or not a run's folder):$kept"
    rm -rf "$state"
  else
    printf 'failed %s\n' "$(date +%s)" >"$state/result"
    for f in ${E2E_FOLDERS[@]+"${E2E_FOLDERS[@]}"}; do [ -d "$f" ] && kept="$kept $f"; done
    [ -n "$kept" ] && _e2e_say "the run failed (exit $code): kept for a look:$kept (a browser run a day from now removes it)"
  fi
  return 0
}

_e2e_stop_guard() {
  local i
  [ -n "${E2E_GUARD:-}" ] || return 0
  kill -TERM "$E2E_GUARD" 2>/dev/null || return 0
  for i in $(seq 1 20); do
    kill -0 "$E2E_GUARD" 2>/dev/null || return 0
    sleep 0.1
  done
  return 0
}

# _e2e_guard <run's pid> <state>: keeps the list of what the run started; if the run ends
# without its exit handler (killed outright), stops it all and drops the run's databases.
_e2e_guard() {
  local main="$1" state="$2" line
  trap '' HUP INT
  trap 'exit 0' TERM
  while _e2e_running "$main"; do
    [ -f "$state/done" ] && exit 0
    {
      _e2e_procs "$main" "$$"
      # Programs that left the tree (their parent ended) stay listed while they run.
      if [ -f "$state/procs" ]; then
        while IFS= read -r line; do
          [ -n "$line" ] && _e2e_alive "$line" && printf '%s\n' "$line"
        done <"$state/procs"
      fi
    } | sort -u >"$state/procs.new" 2>/dev/null && mv "$state/procs.new" "$state/procs" 2>/dev/null
    sleep 2 &
    wait $! 2>/dev/null
  done
  [ -f "$state/done" ] && exit 0
  [ -f "$state/run" ] || exit 0
  # Killed outright: nobody else will clean up.
  echo "$(date): the run (process $main) ended without its exit handler: stopping what it started"
  _e2e_stop "$state/procs"
  (
    R_FOLDERS=()
    . "$state/run"
    _e2e_drop "$R_URI" $(_e2e_dbs "$R_DB" ${R_FOLDERS[@]+"${R_FOLDERS[@]}"})
  )
  printf 'crashed %s\n' "$(date +%s)" >"$state/result"
}

# _e2e_sweep [--now]: cleans up after runs that are gone. A kept run (KEEP=1) and kept folders
# wait a day, unless --now. Quiet when there is nothing to do.
_e2e_sweep() {
  local all="${1:-}" d now
  [ -d "$E2E_RUNS" ] || return 0
  now="$(date +%s)"
  for d in "$E2E_RUNS"/*/; do
    d="${d%/}"
    [ -f "$d/run" ] || continue
    (
      set +eu
      R_FOLDERS=()
      . "$d/run"
      # Still running?
      if kill -0 "$R_PID" 2>/dev/null && ps -o command= -p "$R_PID" 2>/dev/null | grep -qF "$R_SCRIPT"; then
        exit 0
      fi
      result="" ended=""
      [ -f "$d/result" ] && read -r result ended <"$d/result"
      [ -n "$ended" ] || ended="$(_e2e_mtime "$d/procs")"
      [ "$ended" != "0" ] || ended="$(_e2e_mtime "$d/run")"
      age=$((now - ended))
      due=""
      if [ "$age" -ge "$E2E_KEEP_SECONDS" ] || [ "$all" = "--now" ]; then due=1; fi
      # Its programs and databases: a kept run's once due; a run nobody cleaned up after (its
      # guard was stopped too) at once.
      if [ -z "$result" ] || { [ "$result" = "kept" ] && [ -n "$due" ]; }; then
        _e2e_stop "$d/procs"
        _e2e_drop "$R_URI" $(_e2e_dbs "$R_DB" ${R_FOLDERS[@]+"${R_FOLDERS[@]}"})
        _e2e_say "cleaned up after $R_SCRIPT (process $R_PID, ${result:-ended without cleaning up})"
        [ -n "$result" ] || printf 'crashed %s\n' "$now" >"$d/result"
      fi
      [ -n "$due" ] || exit 0
      left=""
      for f in ${R_FOLDERS[@]+"${R_FOLDERS[@]}"}; do
        if _e2e_removable "$f"; then
          rm -rf "$f" && _e2e_say "removed $f (kept from $R_SCRIPT, ${result:-crashed}, $((age / 3600)) h ago)"
        elif [ -d "$f" ] && ! _e2e_named "$f"; then
          left="$left $f"
        fi
      done
      [ -n "$left" ] && _e2e_say "left alone (not a run's folder):$left"
      rm -rf "$d"
    )
  done
  return 0
}

_e2e_list() {
  local d
  [ -d "$E2E_RUNS" ] || { echo "no runs recorded ($E2E_RUNS)"; return 0; }
  for d in "$E2E_RUNS"/*/; do
    d="${d%/}"
    [ -f "$d/run" ] || continue
    (
      set +eu
      R_FOLDERS=()
      . "$d/run"
      result="running"
      [ -f "$d/result" ] && read -r result _ <"$d/result"
      if [ "$result" = "running" ] && ! kill -0 "$R_PID" 2>/dev/null; then result="ended without cleaning up"; fi
      printf '%s: %s (process %s), started %s: %s\n  database %s; folders:%s\n' "$(basename "$d")" "$R_SCRIPT" "$R_PID" \
        "$(date -r "$R_STARTED" 2>/dev/null || date -d "@$R_STARTED" 2>/dev/null)" "$result" "${R_DB:-none}" \
        "$(printf ' %s' ${R_FOLDERS[@]+"${R_FOLDERS[@]}"})"
    )
  done
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  # Run as a command.
  case "${1:-}" in
    sweep) _e2e_sweep "${2:-}" ;;
    list) _e2e_list ;;
    guard) _e2e_guard "$2" "$3" ;;
    *)
      echo "usage: scripts/lib/e2e.sh sweep [--now] | list   (scripts source it: . scripts/lib/e2e.sh && e2e_run \"\$DB\" <folder…>)" >&2
      exit 2
      ;;
  esac
fi
