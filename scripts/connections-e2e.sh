#!/usr/bin/env bash
# connections-e2e.sh — built-in connections in a real browser (D303–D305), on a throwaway
# installation that talks to nothing outside this computer. Two clips are added from the command
# line; an FCPXML 1.11 timeline from "another editor" plays them with a dissolve, a third clip the
# Library doesn't have, a chapter marker and a marker. The browser run
# (web/scripts/connections-e2e.mjs) drops the timeline on Home's Add box: the dialog reads it,
# shows the footage it found and asks about the one it didn't, lists what can't be carried over;
# the clip is left out, the project chosen and the cut made, through the edit check, landing on the
# new version; its page shows Post to YouTube held (not approved, no final render, YouTube not set
# up on this computer), in plain words. Then the command line agrees (the new cut, its chapters).
# Desktop and a 390 px phone, dark and light, with screenshots; no native browser dialog, no
# sideways scrolling, plain words.
#
#   scripts/connections-e2e.sh                  # needs scripts/dev-mongo.sh running and ffmpeg
#   KEEP=1 scripts/connections-e2e.sh           # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/connections-e2e.sh     # set it up and leave the dashboard running
#   METARUSH_CONNECTIONS_PORT=5570 SKIP_BUILD=1 CONNECTIONS_SHOTS=<folder> scripts/connections-e2e.sh
#
# Port: the dashboard on 5570 (5570–5579 are this run's). The throwaway database is dropped on
# exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_CONNECTIONS_PORT:-5570}"
BASE="${METARUSH_CONNECTIONS_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/connections-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-connections$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SRV=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, dashboard http://127.0.0.1:$PORT (pid $SRV)"
    return
  fi
  if [ -n "$SRV" ]; then
    kill "$SRV" 2>/dev/null || true
    for _ in $(seq 1 60); do kill -0 "$SRV" 2>/dev/null || break; sleep 0.25; done
  fi
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
command -v ffmpeg >/dev/null || { echo "ffmpeg is needed (the clips are made with it)" >&2; exit 1; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Connections test" --media filesystem --port "$PORT" >/dev/null
printf 'METARUSH_MONGODB_URI=%s\n' "$URI" > "$H/.env"; chmod 600 "$H/.env"

step "two clips in the Library"
F="$BASE/footage"; mkdir -p "$F"
ffmpeg -loglevel error -f lavfi -i "testsrc2=size=640x360:rate=24000/1001:duration=12" -f lavfi -i "sine=frequency=440:duration=12" -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest "$F/Interview.mov"
ffmpeg -loglevel error -f lavfi -i "testsrc=size=640x360:rate=24000/1001:duration=8" -f lavfi -i "sine=frequency=660:duration=8" -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest "$F/Wide shot.mov"
MR project create "Garden walk" --json | J '"project: " + d["data"]["project"]["name"]'
MR add "$F/Interview.mov" "$F/Wide shot.mov" --project "Garden walk"

step "a timeline from another editor: FCPXML 1.11, 23.976 fps, from 01:00:00:00"
T="$BASE/Rough cut.fcpxml"
cat > "$T" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE fcpxml>
<fcpxml version="1.11">
  <resources>
    <format id="r1" name="FFVideoFormat640x360p2398" frameDuration="1001/24000s" width="640" height="360"/>
    <asset id="r2" name="Interview" start="0s" duration="288288/24000s" hasVideo="1" format="r1" hasAudio="1" audioSources="1" audioChannels="2" audioRate="48000">
      <media-rep kind="original-media" src="file:///Volumes/Shoot/Interview.mov"/>
    </asset>
    <asset id="r3" name="Wide shot" start="0s" duration="192192/24000s" hasVideo="1" format="r1" hasAudio="1" audioSources="1" audioChannels="2" audioRate="48000">
      <media-rep kind="original-media" src="file:///Volumes/Shoot/Wide%20shot.mov"/>
    </asset>
    <asset id="r4" name="Missing broll" start="0s" duration="240240/24000s" hasVideo="1" format="r1">
      <media-rep kind="original-media" src="file:///Volumes/Shoot/Missing%20broll.mov"/>
    </asset>
    <effect id="r5" name="Cross Dissolve" uid="FxPlug:4731E73A-8DAC-4113-9A30-AE85B1761265"/>
  </resources>
  <library>
    <event name="Shoot">
      <project name="Rough cut">
        <sequence format="r1" duration="360360/24000s" tcStart="3600s" tcFormat="NDF" audioLayout="stereo" audioRate="48k">
          <spine>
            <asset-clip ref="r2" offset="3600s" name="Interview" start="24024/24000s" duration="120120/24000s" format="r1" tcFormat="NDF" audioRole="dialogue">
              <chapter-marker start="48048/24000s" duration="1001/24000s" value="The garden" posterOffset="0s"/>
            </asset-clip>
            <transition name="Cross Dissolve" offset="86508108/24000s" duration="24024/24000s">
              <filter-video ref="r5" name="Cross Dissolve"/>
            </transition>
            <asset-clip ref="r3" offset="86520120/24000s" name="Wide shot" start="24024/24000s" duration="96096/24000s" format="r1" tcFormat="NDF" audioRole="dialogue">
              <marker start="48048/24000s" duration="1001/24000s" value="Brighter here"/>
            </asset-clip>
            <asset-clip ref="r4" offset="86616216/24000s" name="Missing broll" start="0s" duration="72072/24000s" format="r1" tcFormat="NDF"/>
            <asset-clip ref="r2" offset="86688288/24000s" name="Interview" start="144144/24000s" duration="72072/24000s" format="r1" tcFormat="NDF" audioRole="dialogue"/>
          </spine>
        </sequence>
      </project>
    </event>
  </library>
</fcpxml>
EOF
MR import timeline "$T" --project "Garden walk" --read --json | J '"read: %s (%d questions)" % (d["data"]["summary"], d["data"]["questions"])'

step "the dashboard on 127.0.0.1:$PORT"
export METARUSH_DEV_ORIGINS=""
e2e_bg "$BASE/serve.log" ./bin/metarush --home "$H" serve; SRV=$E2E_BG
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open; the timeline is $T"
  KEEP=1
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" CONNECTIONS_TIMELINE="$T" \
CONNECTIONS_SHOTS="${CONNECTIONS_SHOTS:-$BASE/screenshots}" node web/scripts/connections-e2e.mjs

step "the command line agrees with what the browser did"
cuts="$(MR project show "Garden walk" --json | J '[c["name"] for c in d["data"]["cuts"]]')"
echo "cuts: $cuts"
case "$cuts" in *"Rough cut"*) ;; *) echo "FAIL the command line doesn't list the imported cut" >&2; exit 1 ;; esac
chapters="$(MR deliver notes show "Garden walk" --branch rough-cut --json | J '[c["title"] for c in (d["data"].get("notes") or {}).get("chapters", [])]')"
echo "chapters: $chapters"
case "$chapters" in *"The garden"*) ;; *) echo "FAIL the chapter isn't in the version's delivery notes" >&2; exit 1 ;; esac
exit 0
