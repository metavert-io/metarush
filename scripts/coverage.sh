#!/usr/bin/env bash
# scripts/coverage.sh — MetaRush's coverage in one place (D195, docs/testing.md).
#
#   scripts/coverage.sh --test               # run the Go tests with coverage, then report and check
#   scripts/coverage.sh                      # report and check what is already in .coverage/
#   scripts/coverage.sh --update-baseline    # … and record today's numbers in coverage-baseline.json
#   scripts/coverage.sh --no-check           # report only
#
# Inputs, in .coverage/ (METARUSH_COVERAGE_DIR moves it):
#   go-unit.raw.cov   go test -coverpkg=./internal/... -coverprofile (--test makes it)
#   e2e/<run>/        counters from browser runs and acceptance under COVERAGE=1 (scripts/lib/e2e.sh)
#   web-e2e/<run>/    the dashboard's pages in those browser runs (web/scripts/coverage/collect.mjs)
#   ../web/coverage/lcov.info   the dashboard's unit tests (npm run test:coverage -w web)
# Outputs, in .coverage/:
#   go-unit.cov, go-e2e.cov, go-all.cov   merged profiles (each block once); what CI sends to Codecov
#   web-e2e.lcov                          the dashboard's source the browser runs reached
#   table-unit.txt, table-all.txt         statements, covered and percent per package
# The ratchet compares go-unit.cov with coverage-baseline.json: a package of 100 statements or more
# that drops more than 1 point fails (exit 1). Browser-run coverage is reported, not ratcheted:
# which suites run differs between machines. --update-baseline records this run; a few paths are
# reached in one run and not the next (timing, Mac-only helpers), so a baseline from several runs is
# steadier: go run ./scripts/covreport baseline -o coverage-baseline.json run1.cov run2.cov (each
# package at its lowest; how today's was recorded).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
OUT="${METARUSH_COVERAGE_DIR:-$ROOT/.coverage}"
TEST=""; CHECK=1; UPDATE=""
for a in "$@"; do
  case "$a" in
    --test) TEST=1 ;;
    --no-check) CHECK="" ;;
    --update-baseline) UPDATE=1 ;;
    -h | --help) sed -n '2,22p' "$0"; exit 0 ;;
    *) echo "unknown option $a (see --help)" >&2; exit 2 ;;
  esac
done
mkdir -p "$OUT"
COV="$OUT/covreport"
go build -o "$COV" ./scripts/covreport

status=0
if [ -n "$TEST" ]; then
  # 45 minutes: under load internal/ops has come close to 30 (Oct 2026).
  # Tests that need the generated footage skip without it, which would read as
  # lost coverage in a fresh worktree: make it first (quick when up to date).
  scripts/make-fixtures.sh >/dev/null || echo "couldn't make the test footage: the tests that need it will skip" >&2
  echo "== Go tests with coverage (-p ${GO_TEST_P:-2}; the slowest packages take minutes)"
  nice -n 10 go test -p "${GO_TEST_P:-2}" -timeout 45m -coverpkg=./internal/... -coverprofile="$OUT/go-unit.raw.cov" ./... > "$OUT/go-test.log" 2>&1 || status=$?
  grep -E '^(FAIL|ok|---)' "$OUT/go-test.log" | grep -v '^ok' || true
  [ "$status" = 0 ] || echo "the Go tests failed (exit $status): $OUT/go-test.log"
fi

profiles=()
if [ -s "$OUT/go-unit.raw.cov" ]; then
  "$COV" merge -o "$OUT/go-unit.cov" "$OUT/go-unit.raw.cov"
  profiles+=("$OUT/go-unit.cov")
fi
# Counters from runs under COVERAGE=1 (only folders that have any).
e2e=()
if [ -d "$OUT/e2e" ]; then
  for d in "$OUT"/e2e/*/; do
    ls "$d"covcounters.* >/dev/null 2>&1 && e2e+=("${d%/}")
  done
fi
if [ "${#e2e[@]}" -gt 0 ]; then
  dirs="$(IFS=,; echo "${e2e[*]}")"
  go tool covdata textfmt -i="$dirs" -o "$OUT/go-e2e.raw.cov"
  "$COV" merge -o "$OUT/go-e2e.cov" "$OUT/go-e2e.raw.cov"
  profiles+=("$OUT/go-e2e.cov")
  echo "== browser runs and acceptance: ${#e2e[@]} run(s) with counters"
  "$COV" table "$OUT/go-e2e.cov" | tail -1
fi
if [ "${#profiles[@]}" -eq 0 ]; then
  echo "no coverage to report in $OUT (run with --test, or browser runs with COVERAGE=1)" >&2
  exit 1
fi
"$COV" merge -o "$OUT/go-all.cov" "${profiles[@]}"
"$COV" table "$OUT/go-all.cov" > "$OUT/table-all.txt"
if [ -s "$OUT/go-unit.cov" ]; then
  "$COV" table "$OUT/go-unit.cov" > "$OUT/table-unit.txt"
  echo "== Go tests: $(tail -1 "$OUT/table-unit.txt" | sed -E 's/^total +//')"
fi
echo "== everything measured: $(tail -1 "$OUT/table-all.txt" | sed -E 's/^total +//')"
echo "   per package: $OUT/table-all.txt"
if [ -f web/coverage/lcov.info ]; then
  awk -F: '/^LF:/ {lf += $2} /^LH:/ {lh += $2} END {if (lf) printf "== dashboard unit tests: %d of %d lines (%.1f%%)\n", lh, lf, 100 * lh / lf}' web/coverage/lcov.info
fi
# The dashboard's pages in browser runs under COVERAGE=1 (web/scripts/coverage/collect.mjs).
web_e2e=()
if [ -d "$OUT/web-e2e" ]; then
  for d in "$OUT"/web-e2e/*/; do
    ls "$d"page-*.json >/dev/null 2>&1 && web_e2e+=("${d%/}")
  done
fi
if [ "${#web_e2e[@]}" -gt 0 ]; then
  printf '== '
  node web/scripts/coverage/report.mjs --out "$OUT/web-e2e.lcov" "${web_e2e[@]}" || status=1
fi

if [ -n "$UPDATE" ]; then
  [ -s "$OUT/go-unit.cov" ] || { echo "no Go test profile to record (run with --test)" >&2; exit 1; }
  "$COV" baseline -o coverage-baseline.json "$OUT/go-unit.cov"
  echo "== coverage-baseline.json recorded ($(grep -c '"statements"' coverage-baseline.json) entries)"
elif [ -n "$CHECK" ] && [ -s "$OUT/go-unit.cov" ]; then
  echo "== the ratchet (coverage-baseline.json)"
  "$COV" check -baseline coverage-baseline.json "$OUT/go-unit.cov" || status=1
fi
exit "$status"
