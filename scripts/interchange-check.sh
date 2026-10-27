#!/usr/bin/env bash
# Validates the timeline export (D92, docs/timeline-export.md) with the official
# OpenTimelineIO library and the formats' DTDs: the Go tests write a fixture
# timeline in all three formats, scripts/interchange-check.py reads each back
# (OpenTimelineIO, its fcp_xml and fcpx_xml adapters), round-trips the .otio and
# checks FCPXML 1.8 and Final Cut Pro 7 XML against their DTDs with xmllint.
# Then OpenTimelineIO writes the fixture in each format it writes and MetaRush's
# timeline readers (D303, docs/timeline-import.md) read those files back.
#
# OpenTimelineIO lives in a throwaway virtual environment, never a MetaRush
# dependency; its versions and hashes are pinned (scripts/interchange-requirements.txt). The first run needs the network (packages, DTDs); pass
# METARUSH_OTIO_VENV and METARUSH_INTERCHANGE_DTD to reuse them.
# Usage: scripts/interchange-check.sh [export.otio|export.fcpxml|export.xml ...]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
WORK="$(mktemp -d)"
trap 'rc="$(exit_status $?)"; rm -rf "$WORK"; exit "$rc"' EXIT
VENV="${METARUSH_OTIO_VENV:-$WORK/venv}"
DTD="${METARUSH_INTERCHANGE_DTD:-$WORK/dtd}"
if [ ! -x "$VENV/bin/python" ]; then
  # Python 3.12 with OpenTimelineIO and its adapters at pinned versions and
  # hashes (scripts/interchange-requirements.txt, D303): uv when present (it
  # brings a managed Python 3.12 if none is installed), else a Python 3.12 on
  # PATH, else uv itself pinned by hash (scripts/interchange-uv.txt) into the
  # temporary folder first.
  echo "setting up OpenTimelineIO in a temporary virtual environment…"
  UV="$(command -v uv || true)"
  if [ -z "$UV" ] && ! command -v python3.12 >/dev/null; then
    python3 -m venv "$WORK/uv"
    "$WORK/uv/bin/pip" install -q --require-hashes --only-binary :all: -r scripts/interchange-uv.txt
    UV="$WORK/uv/bin/uv"
    export UV_PYTHON_INSTALL_DIR="$WORK/python" UV_CACHE_DIR="$WORK/uv-cache"
  fi
  if [ -n "$UV" ]; then
    "$UV" venv -q --python 3.12 "$VENV"
    "$UV" pip install -q --python "$VENV/bin/python" --require-hashes --only-binary :all: -r scripts/interchange-requirements.txt
  else
    python3.12 -m venv "$VENV"
    "$VENV/bin/pip" install -q --require-hashes --only-binary :all: -r scripts/interchange-requirements.txt
  fi
fi
mkdir -p "$WORK/out"
INTERCHANGE_OUT="$WORK/out" go test -count=1 -run 'TestOTIOStructure|TestFCPXMLStructure|TestXMEMLStructure|TestNestedCut|TestClearerVoicesExportTheCopy' ./internal/interchange/ >/dev/null
"$VENV/bin/python" -I scripts/interchange-check.py --fixture "$WORK/out" --dtd-dir "$DTD" --other-out "$WORK/other" "$@"
# MetaRush's readers (D303) read back what OpenTimelineIO wrote.
INTERCHANGE_OTHER="$WORK/other" go test -count=1 -run 'TestReadOtherWriters' ./internal/interchange/
echo "MetaRush's timeline readers read OpenTimelineIO's files back"
exit 0
