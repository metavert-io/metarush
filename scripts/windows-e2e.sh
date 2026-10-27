#!/usr/bin/env bash
# windows-e2e.sh — the dashboard as a Windows 11 person uses it (D247): from Edge or Chrome on Windows, against
# MetaRush running on a Mac or Linux computer (docs/windows.md). It can't run Windows here, so it plays one: a
# Windows browser's identity (user agent, Win32, 1366×768 at 125% scaling) in Chromium, and in Microsoft Edge when
# it is installed (Playwright's msedge channel). web/scripts/windows-e2e.mjs checks that:
#   1. a file dropped with a Windows path for a name (C:\Users\…\clip.mp4) arrives under its own name, accents kept;
#   2. Windows' own files (Thumbs.db, desktop.ini, a Recycle Bin) are left out of a dropped or chosen folder;
#   3. a clip whose name Windows can't keep (Q&A: what's next?.mp4) downloads under a name it can, and an accented
#      name arrives whole;
#   4. text pasted with Windows line endings is stored with plain ones (delivery notes, chapters read line by line);
#   5. shortcuts say Ctrl, not ⌘; no page scrolls sideways at 1366×768 and 125%.
# The real Windows 11 check is a person's (docs/windows.md, "Checking it on Windows").
#
#   scripts/windows-e2e.sh                     # needs scripts/dev-mongo.sh running
#   KEEP=1 SKIP_BUILD=1 METARUSH_E2E_PORT=5440 scripts/windows-e2e.sh
#
# Ports 5440–5449 (D247). The throwaway database is dropped on exit through scripts/droptestdb.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_E2E_PORT:-5440}"
TMPBASE="${TMPDIR:-/tmp}"; TMPBASE="${TMPBASE%/}"
BASE="${METARUSH_E2E_DIR:-$(mktemp -d "$TMPBASE/windows-e2e-XXXXXX")}"
mkdir -p "$BASE"
HA="$BASE/home"; MEDIA="$BASE/media"
DB="metarush-test-windows$(date +%s)"
MR_BIN="${MR_BIN:-$ROOT/bin/metarush}"
URI="$(scripts/dev-mongo.sh start)"
SA=""
cleanup() {
  local code="$1"
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $HA, database $DB, dashboard http://127.0.0.1:$PORT (pid $SA)"
    return
  fi
  [ -n "$SA" ] && kill "$SA" 2>/dev/null || true
  sleep 1
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
  if [ "$code" = "0" ] && [ -z "${METARUSH_E2E_DIR:-}" ]; then rm -rf "$BASE"; fi
}
trap 'cleanup $?' EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
A() { "$MR_BIN" --home "$HA" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }

if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -ldflags "$(scripts/version.sh --ldflags)" -o "$MR_BIN" ./cmd/metarush
fi

step "tiny clips (each its own bytes: MetaRush keeps one copy of the same file), and a folder as Windows leaves it"
CLIPS="$BASE/clips"; mkdir -p "$CLIPS"
clip() { ffmpeg -nostdin -hide_banner -loglevel error -y -f lavfi -i "testsrc2=size=640x360:rate=30:duration=2" \
  -f lavfi -i "sine=frequency=$2:duration=2" -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest "$1"; }
for f in 330 440 550 660 990 1100 1210 1320; do clip "$CLIPS/$f.mp4" "$f"; done
FOLDER="$BASE/Trip"
mkdir -p "$FOLDER/\$RECYCLE.BIN"
clip "$FOLDER/ferry-dock.mp4" 770
clip "$FOLDER/\$RECYCLE.BIN/old-take.mp4" 880
printf 'thumbs' > "$FOLDER/Thumbs.db"
printf '[.ShellClassInfo]\r\nIconResource=C:\\Windows\\System32\\imageres.dll,-3\r\n' > "$FOLDER/desktop.ini"
ls -A "$FOLDER"

step "a throwaway installation, the dashboard (no worker)"
METARUSH_MONGODB_URI="$URI" A init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Windows test" --media filesystem \
  --media-root "$MEDIA" --port "$PORT" --installation-name "Studio Mac" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$HA/.env"; chmod 600 "$HA/.env"
PROJECT=$(A project create "Harbour film" --json | J 'd["data"]["project"]["id"]')
METARUSH_DEV_ORIGINS="" "$MR_BIN" --home "$HA" serve --no-worker > "$BASE/daemon.log" 2>&1 & SA=$!
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done
echo "/api/health: $(curl -sf "http://127.0.0.1:$PORT/api/health")"

step "the browser run"
SHOTS="${WINDOWS_SHOTS:-$ROOT/.agent-home/windows-shots}"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="$MR_BIN --home $HA dashboard --print-link --no-open" WINDOWS_SHOTS="$SHOTS" \
  CLIPS="$CLIPS" FOLDER="$FOLDER" PROJECT="$PROJECT" \
  node web/scripts/windows-e2e.mjs || { echo "--- dashboard log"; tail -40 "$BASE/daemon.log"; exit 1; }
echo "screenshots: $SHOTS"
exit 0
