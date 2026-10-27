#!/usr/bin/env bash
# recorder-e2e.sh — Record your screen (D258–D262) in a real browser, on a throwaway
# installation: Chromium's fake camera and microphone (the microphone fed with beeps at
# irregular times, asked for by its device ID: asked for the default, Chromium opens the
# computer's real input) and a fake screen (getDisplayMedia answered by an animated canvas
# that says it is a window). The browser run (web/scripts/recorder-e2e.mjs):
#   opens the recorder from a project's Add box; a microphone that is refused and one that
#   never answers are said plainly (the deadline); shares the screen, picks the camera, the
#   layout and a pasted script; records with a countdown, brings a line in early, pauses
#   and resumes, stops; checks the three tracks became footage of the same length with the
#   pause left out of each, the voice's place on the clock agrees with lining the files up
#   by their sound, and a cut starts with the camera in a corner; closes a tab mid-recording
#   and keeps what it sent from the next page; stops by itself when screen sharing ends.
#   Desktop and 390 px phone, dark and light, screenshots; no native dialog, no sideways
#   scrolling.
#
#   scripts/recorder-e2e.sh                   # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/recorder-e2e.sh            # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/recorder-e2e.sh      # set it up and leave the dashboard running
#   METARUSH_RECORDER_PORT=5480 SKIP_BUILD=1 RECORDER_SHOTS=<folder> scripts/recorder-e2e.sh
#
# Ports: the dashboard on 5480 (5480–5489 are this run's). The throwaway database is dropped
# on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_RECORDER_PORT:-5480}"
T="${TMPDIR:-/tmp}"; T="${T%/}"
BASE="${METARUSH_RECORDER_DIR:-$(mktemp -d "$T/mr-recorder-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-recorder$(date +%s)"
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
  [ -z "${METARUSH_RECORDER_DIR:-}" ] && rm -rf "$BASE" || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation"
mkdir -p "$H" "$BASE/media"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Recorder test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
P=$(MR project create "Launch video" --json | J 'd["data"]["project"]["id"]')
echo "project $P"

step "the fake microphone's sound: beeps at irregular times (they line the files up by sound)"
python3 -I - "$BASE/mic.wav" <<'EOF'
import array, math, sys, wave
rate, secs = 48000, 40
beeps = []
t, k = 0.7, 1
while t < secs - 1:
    beeps.append(t)
    k = (k * 7 + 3) % 11
    t += 0.45 + k * 0.17
s = array.array('h', bytes(2 * rate * secs))
for b in beeps:
    i0 = int(b * rate)
    for j in range(int(0.08 * rate)):
        s[i0 + j] = int(0.5 * 32767 * math.sin(2 * math.pi * 1000 * j / rate))
w = wave.open(sys.argv[1], 'wb')
w.setnchannels(1); w.setsampwidth(2); w.setframerate(rate); w.writeframes(s.tobytes()); w.close()
print("%d beeps" % len(beeps))
EOF

step "the dashboard on 127.0.0.1:$PORT (no worker: nothing here needs one)"
METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve --no-worker --no-watch > "$H/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" \
METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
RECORDER_PROJECT="$P" RECORDER_MIC="$BASE/mic.wav" \
RECORDER_SHOTS="${RECORDER_SHOTS:-$BASE/screenshots}" node web/scripts/recorder-e2e.mjs

step "the CLI sees the recordings"
MR recording list --project "$P"
MR recording list --project "$P" --json | J '"%d recordings, %d footage" % (len(d["data"]["recordings"]), sum(1 for r in d["data"]["recordings"] if r["recording"]["state"]=="ready"))'
exit 0
