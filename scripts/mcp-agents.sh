#!/usr/bin/env bash
# Fake AI apps on the review desk (D248–D251): scripted MCP clients, driven by
# the official MCP Python SDK, that connect to `metarush mcp serve --propose`
# as Codex and Cursor do (each names itself in clientInfo), read and check,
# propose a draft version and a house rule, and are refused approving,
# deciding, rendering, spending and changing through a reading tool
# (scripts/mcp-agents.py). --phase news checks that codex hears a person's
# decision once, on its next call.
#
# The SDK lives in a throwaway virtual environment (METARUSH_MCP_VENV reuses
# one); the first run needs the network. It needs a test installation: pass
# --home <folder>. Without --project it makes one with a first cut from a
# four-second clip it draws with ffmpeg (added without transcripts).
# Usage: scripts/mcp-agents.sh --home <folder> [--project <project>] [--phase propose|news] [--expect <words>]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
HOME_ARG="" PROJECT="" REST=()
while [ $# -gt 0 ]; do
  case "$1" in
    --home) HOME_ARG="$2"; shift 2 ;;
    --project) PROJECT="$2"; shift 2 ;;
    *) REST+=("$1"); shift ;;
  esac
done
[ -n "$HOME_ARG" ] || { echo "pass --home <a test installation>" >&2; exit 2; }
WORK="$(mktemp -d "${TMPDIR:-/tmp}/mr-agents-XXXXXX")"
trap 'rc="$(exit_status $?)"; rm -rf "$WORK"; exit "$rc"' EXIT
VENV="${METARUSH_MCP_VENV:-$WORK/venv}"
if [ ! -x "$VENV/bin/python" ]; then
  echo "setting up the MCP Python SDK in a temporary virtual environment…"
  if command -v uv >/dev/null; then
    uv venv -q --python 3.12 "$VENV"
    uv pip install -q --python "$VENV/bin/python" mcp
  else
    python3 -m venv "$VENV"
    "$VENV/bin/pip" install -q mcp
  fi
fi
[ -x bin/metarush ] || go build -o bin/metarush ./cmd/metarush
MR() { ./bin/metarush --home "$HOME_ARG" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
if [ -z "$PROJECT" ]; then
  ffmpeg -loglevel error -f lavfi -i testsrc2=size=640x360:rate=30:duration=4 -f lavfi -i sine=frequency=440:duration=4 \
    -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest "$WORK/garden-walk.mp4"
  PROJECT=$(MR project create "Agents check $(date +%s)" --json | J 'd["data"]["project"]["id"]')
  MR ingest "$WORK/garden-walk.mp4" --project "$PROJECT" --no-transcribe --wait --json >/dev/null
  CLIP=$(MR asset list "$PROJECT" --json | J 'd["data"]["assets"][0]["id"]')
  python3 -I -c 'import json,sys; json.dump({"projectId":sys.argv[2],"instruction":"first cut","spec":{"schemaVersion":1,"title":"Garden walk","output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"walk","assetId":sys.argv[3],"in":0,"out":3.5}]}}, open(sys.argv[1],"w"))' \
    "$WORK/first.plan.json" "$PROJECT" "$CLIP"
  TOK=$(MR edit check --plan "$WORK/first.plan.json" --json | J 'd["data"]["confirmToken"]')
  MR edit apply --plan "$WORK/first.plan.json" --confirm "$TOK" --json >/dev/null
fi
"$VENV/bin/python" -I scripts/mcp-agents.py --cmd "$ROOT/bin/metarush" --home "$HOME_ARG" --project "$PROJECT" "${REST[@]+"${REST[@]}"}"
exit 0
