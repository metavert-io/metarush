#!/usr/bin/env bash
# scripts/ci-go-packages.sh <group>: the Go packages one CI job tests (D194, .github/workflows/ci.yml).
# The suite runs as four jobs side by side: internal/assistant and internal/ops each alone (the
# slowest on a Mac: many test databases each), "heavy" (a fixed list) and "rest" (everything else,
# computed, so a new package is never left out). heavy and rest are balanced by the package times CI
# measured on ubuntu (22 Oct 2026, seconds): fixes 116, qa 92, sound 36, cli 32, voiceover 31,
# generate 29 (heavy: 336) against tests/e2e 97, api 66, worker 21 and ~90 small ones (rest: ~320).
#
#   scripts/ci-go-packages.sh assistant|ops|heavy|rest   # import paths, one per line
#   scripts/ci-go-packages.sh --all                      # every group with its packages (a check)
set -euo pipefail
cd "$(dirname "$0")/.."
M="github.com/metavert-io/metarush"
HEAVY="internal/fixes internal/qa internal/sound internal/cli internal/voiceover internal/generate"

group() {
  local p args=()
  case "$1" in
    assistant) echo "$M/internal/assistant" ;;
    ops) echo "$M/internal/ops" ;;
    heavy) for p in $HEAVY; do echo "$M/$p"; done ;;
    rest)
      args=(-e "$M/internal/assistant" -e "$M/internal/ops")
      for p in $HEAVY; do args+=(-e "$M/$p"); done
      go list ./... | grep -v -x "${args[@]}"
      ;;
    *) echo "usage: scripts/ci-go-packages.sh assistant|ops|heavy|rest|--all" >&2; return 2 ;;
  esac
}

if [ "${1:-}" = "--all" ]; then
  total=0
  for g in assistant ops heavy rest; do
    n="$(group "$g" | wc -l | tr -d ' ')"
    total=$((total + n))
    echo "$g: $n package(s)"
  done
  all="$(go list ./... | wc -l | tr -d ' ')"
  echo "together: $total of $all"
  [ "$total" = "$all" ] || { echo "the groups don't cover every package exactly once" >&2; exit 1; }
  exit 0
fi
group "${1:-}"
