#!/usr/bin/env bash
# Checks `metarush mcp serve` (D95, D222, docs/mcp.md) with the official MCP
# Python SDK, the client library many MCP clients use: the core (find,
# describe, run), one toolset (review unless --toolset names another) and
# every operation as a tool — handshake, tool lists and schemas, reads,
# refusals, and (with --changes) the two-step confirmation and a propose-only
# connection saving a draft (D248).
#
# The SDK lives in a throwaway virtual environment, never a MetaRush
# dependency; the first run needs the network. METARUSH_MCP_VENV reuses one.
# It needs a working installation: pass --home <folder> (a test installation
# for --changes, which creates two projects).
# Usage: scripts/mcp-check.sh [--home <folder>] [--changes] [--toolset review|edit|library|admin]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
WORK="$(mktemp -d)"
trap 'rc="$(exit_status $?)"; rm -rf "$WORK"; exit "$rc"' EXIT
VENV="${METARUSH_MCP_VENV:-$WORK/venv}"
if [ ! -x "$VENV/bin/python" ]; then
  echo "setting up the MCP Python SDK in a temporary virtual environment…"
  if command -v uv >/dev/null; then
    uv venv -q --python 3.12 "$VENV"
    uv pip install -q --python "$VENV/bin/python" mcp jsonschema
  else
    python3 -m venv "$VENV"
    "$VENV/bin/pip" install -q mcp jsonschema
  fi
fi
[ -x bin/metarush ] || go build -o bin/metarush ./cmd/metarush
"$VENV/bin/python" -I scripts/mcp-check.py --cmd "$ROOT/bin/metarush" "$@"
exit 0
