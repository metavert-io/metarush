#!/usr/bin/env bash
# scripts/check.sh — the checks before a commit, before a merge and before a release, in one command
# (D194, docs/testing.md). Every step's output goes to a log in a folder it prints; a table at the end
# says what passed, what failed and how long each took. Exit status 1 when any step failed.
#
#   scripts/check.sh --quick     static checks, the dashboard's unit tests, and the Go tests of the
#                                packages this branch changed (minutes)
#   scripts/check.sh --full      everything before a release: the dashboard (types, build, unit tests),
#                                the build, schema stable, gofmt, a Linux build, vet, skills, secret scan,
#                                motion tests, the render self-test, the Go suite with coverage and the
#                                ratchet (scripts/coverage.sh), acceptance, and every browser run one at a
#                                time, with a sweep of leftovers before and after (an hour or more)
#   scripts/check.sh --release   --full, then the publish check (scripts/publish-check.sh) and
#                                scripts/release.sh --snapshot
#
# Options: --keep-going (default) runs every step even after a failure; --stop stops at the first.
# Environment: GO_TEST_P (default 2) for the Go tests; COVERAGE=1 also measures the browser runs and
# acceptance (coverage builds, D195); ONLY_E2E="about storage" runs only those browser runs (--full);
# SKIP_E2E="remote sound" leaves those out. Needs scripts/dev-mongo.sh (started when not running).
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew

MODE=""; STOP=""
for a in "$@"; do
  case "$a" in
    --quick | --full | --release) MODE="${a#--}" ;;
    --stop) STOP=1 ;;
    --keep-going) STOP="" ;;
    -h | --help) sed -n '2,19p' "$0"; exit 0 ;;
    *) echo "unknown option $a (see --help)" >&2; exit 2 ;;
  esac
done
[ -n "$MODE" ] || { sed -n '2,19p' "$0"; exit 2; }

STAMP="$(date +%Y%m%d-%H%M%S)"
LOGS="${METARUSH_CHECK_LOGS:-$ROOT/.agent-home/check}/$STAMP-$MODE"
mkdir -p "$LOGS"
SUMMARY="$LOGS/summary.txt"
: > "$SUMMARY"
FAILED=0; N=0
echo "scripts/check.sh --$MODE: logs in $LOGS"

# step "<name>" <command…>: runs one step, its output in its own log.
step() {
  local name="$1" log start end took result
  shift
  N=$((N + 1))
  log="$LOGS/$(printf '%02d' "$N")-$(printf '%s' "$name" | tr -c 'A-Za-z0-9._-' '-').log"
  printf '%-44s ' "$name"
  start="$(date +%s)"
  if "$@" > "$log" 2>&1; then result="ok"; else result="FAILED"; FAILED=$((FAILED + 1)); fi
  # A script that an unset variable stopped can still end with status 0 (macOS's bash 3.2 with an
  # EXIT trap, scripts/lib/status.sh): its log says so, and that is a failure whatever the status.
  if [ "$result" = "ok" ] && grep -Eq ': line [0-9]+: [^ ]+: unbound variable$' "$log"; then
    result="FAILED"; FAILED=$((FAILED + 1))
    echo "check.sh: a script stopped on an unset variable (above), so this step failed" >> "$log"
  fi
  end="$(date +%s)"; took=$((end - start))
  printf '%-7s %5ss\n' "$result" "$took"
  printf '%s|%s|%s|%s\n' "$name" "$result" "$took" "$log" >> "$SUMMARY"
  if [ "$result" = "FAILED" ]; then
    tail -15 "$log" | sed 's/^/    | /'
    [ -n "$STOP" ] && finish
  fi
  return 0
}

finish() {
  echo
  printf '%-44s %-7s %7s  %s\n' "step" "result" "time" "log"
  while IFS='|' read -r name result took log; do
    printf '%-44s %-7s %6ss  %s\n' "$name" "$result" "$took" "${log#"$LOGS"/}"
  done < "$SUMMARY"
  echo
  if [ "$FAILED" -gt 0 ]; then
    echo "$FAILED step(s) failed. Logs: $LOGS"
    exit 1
  fi
  echo "every step passed. Logs: $LOGS"
  exit 0
}

# --- the steps ------------------------------------------------------------------------------------

gofmt_clean() {
  local out
  out="$(gofmt -l cmd internal scripts tests tools ./*.go 2>&1)"
  [ -z "$out" ] || { echo "not gofmt-formatted:"; echo "$out"; return 1; }
  echo "gofmt: clean"
}

go_vet() { nice -n 10 go vet ./...; }

linux_build() { GOOS=linux GOARCH=amd64 CGO_ENABLED=0 nice -n 10 go build -o /dev/null ./...; }

secret_scan() { scripts/secret-scan.sh; }

shell_syntax() {
  local f bad=0
  for f in scripts/*.sh scripts/lib/*.sh bootstrap.sh start.sh; do
    bash -n "$f" || { echo "syntax: $f"; bad=1; }
  done
  [ "$bad" = 0 ] && echo "every script parses"
}

e2e_lib_test() { scripts/lib/e2e-test.sh; }
voices_lib_test() { scripts/lib/voices-test.sh; }

web_typecheck() { npm run typecheck; }

web_unit() {
  if [ "${COVERAGE:-}" = "1" ]; then npm run test:coverage -w web; else npm test -w web; fi
}

web_build() {
  # Coverage builds keep source maps beside the bundles (D195).
  if [ "${COVERAGE:-}" = "1" ]; then METARUSH_WEB_SOURCEMAP=1 npm run build -w web; else npm run build -w web; fi
}

# COVERAGE=1: the Go tests, acceptance and the browser runs together (scripts/coverage.sh).
coverage_report() { scripts/coverage.sh --no-check; }

# The packages this branch changed (since it left main, plus what isn't committed yet).
changed_packages() {
  local base files dirs d
  base="$(git merge-base HEAD main 2>/dev/null || git rev-parse HEAD~1 2>/dev/null || true)"
  files="$( { [ -n "$base" ] && git diff --name-only "$base"...HEAD; git diff --name-only HEAD; git ls-files --others --exclude-standard; } 2>/dev/null | grep '\.go$' | sort -u)"
  dirs="$(printf '%s\n' "$files" | sed -n 's#/[^/]*$##p' | grep -v '/testdata' | sort -u)"
  for d in $dirs; do [ -d "$d" ] && go list "./$d" 2>/dev/null; done | sort -u
}

go_changed() {
  local pkgs
  pkgs="$(changed_packages)"
  if [ -z "$pkgs" ]; then
    echo "no Go package changed on this branch"
    return 0
  fi
  echo "changed packages:"; printf '  %s\n' $pkgs
  # 45 minutes: under load internal/ops has come close to 30 (Oct 2026).
  # shellcheck disable=SC2086
  nice -n 10 go test -p "${GO_TEST_P:-2}" -timeout 45m $pkgs
}

build_binary() {
  local ld
  ld="$(scripts/version.sh --ldflags)"
  if [ "${COVERAGE:-}" = "1" ]; then
    go build -cover -covermode=atomic -coverpkg=./internal/...,./cmd/... -ldflags "$ld" -o bin/metarush ./cmd/metarush
  else
    go build -ldflags "$ld" -o bin/metarush ./cmd/metarush
  fi
  nice -n 10 go build ./...
  ./bin/metarush version
}

schema_stable() {
  local before after
  before="$(git status --porcelain -- schema | sort; find schema -type f -exec shasum {} + | sort)"
  ./bin/metarush schema export --dir schema && npm run gen:types >/dev/null || return 1
  after="$(git status --porcelain -- schema | sort; find schema -type f -exec shasum {} + | sort)"
  if [ "$before" != "$after" ]; then
    echo "the generated schema was out of date and has been regenerated: review and commit schema/"
    git status --short -- schema
    return 1
  fi
  echo "the generated schema matches the Go types"
}

skills_test() { ./bin/metarush skills test; }

motion_test() { npm run test:motion -w video; }

render_selftest() { (cd video && nice -n 10 npm run render:selftest); }

go_suite_with_coverage() { GO_TEST_P="${GO_TEST_P:-2}" scripts/coverage.sh --test; }

sweep() {
  go run ./scripts/sweeptestdbs --drop
  scripts/lib/e2e.sh sweep
}

acceptance() { SKIP_BUILD=1 nice -n 10 scripts/acceptance.sh; }

# Every browser run, one at a time (they use fixed ports), with the build already made.
browser_run() { SKIP_BUILD=1 nice -n 10 "scripts/$1-e2e.sh"; }

# Nothing personal or private in the tree or the history (docs/publish-check.md).
publish_check() { scripts/publish-check.sh; }

release_snapshot() { scripts/release.sh --snapshot; }

# --- the modes ------------------------------------------------------------------------------------

scripts/dev-mongo.sh start > /dev/null || { echo "the test MongoDB couldn't be started (scripts/dev-mongo.sh)"; exit 1; }

case "$MODE" in
  quick)
    step "gofmt" gofmt_clean
    step "go vet" go_vet
    step "shell scripts parse" shell_syntax
    step "secret scan" secret_scan
    step "dashboard: types" web_typecheck
    step "dashboard: unit tests" web_unit
    step "Go tests of changed packages" go_changed
    ;;
  full | release)
    if [ "${COVERAGE:-}" = "1" ]; then
      # Only this run's counters (earlier runs' would mix in).
      rm -rf "${METARUSH_COVERAGE_DIR:-$ROOT/.coverage}/e2e" "${METARUSH_COVERAGE_DIR:-$ROOT/.coverage}/web-e2e"
    fi
    step "sweep before (test databases, kept runs)" sweep
    step "dashboard: types" web_typecheck
    step "dashboard: build" web_build
    step "dashboard: unit tests" web_unit
    step "build (bin/metarush, ./...)" build_binary
    step "schema stable" schema_stable
    step "gofmt" gofmt_clean
    step "Linux build" linux_build
    step "go vet" go_vet
    step "shell scripts parse" shell_syntax
    step "skills test" skills_test
    step "secret scan" secret_scan
    step "motion tests" motion_test
    step "render self-test" render_selftest
    step "e2e library" e2e_lib_test
    step "fixture voices" voices_lib_test
    step "Go suite + coverage ratchet" go_suite_with_coverage
    step "acceptance" acceptance
    for f in scripts/*-e2e.sh; do
      name="$(basename "$f" -e2e.sh)"
      case " ${SKIP_E2E:-} " in *" $name "*) continue ;; esac
      if [ -n "${ONLY_E2E:-}" ]; then case " $ONLY_E2E " in *" $name "*) ;; *) continue ;; esac; fi
      step "browser run: $name" browser_run "$name"
    done
    step "sweep after" sweep
    [ "${COVERAGE:-}" = "1" ] && step "coverage: Go tests, acceptance and browser runs" coverage_report
    if [ "$MODE" = "release" ]; then
      step "publish check" publish_check
      step "release dry run (--snapshot)" release_snapshot
    fi
    ;;
esac
finish
