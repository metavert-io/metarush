#!/usr/bin/env bash
# background-e2e.sh — replacing a clip's background in a real browser (D505–D514; D514 for the run itself), on a
# throwaway installation.
#
# "Studio" has a first cut of one clip: a bright figure moving over a dark blue room, with a tone. The person
# finder is a stand-in for MetaRush's on-screen helper (protocol 3's segment command: the bright parts of each
# frame are "the people"; nothing else), so the run needs a Mac — Apple Vision's model is a Mac's — but no real
# model, and nothing is downloaded or sent. The browser run opens the Edit tab, chooses Replace the background…
# on the clip (a colour), sees the still, starts the cut-out and watches it being made, gets the change saved as a
# new version, finds the preview drawing the colour behind the cut-out, and takes the background out again (Undo
# puts it back). From the command line it then renders a review copy and checks its frames: the colour behind the
# figure, the figure where the clip has it. Screenshots: desktop dark and light, phone.
#
#   scripts/background-e2e.sh                     # needs scripts/dev-mongo.sh running; a Mac
#   KEEP=1 scripts/background-e2e.sh              # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/background-e2e.sh        # set it up and leave the dashboard running
#   METARUSH_E2E_PORT=5830 SKIP_BUILD=1 BACKGROUND_SHOTS=<folder> scripts/background-e2e.sh
#
# Ports 5830–5839 (this stream's). The throwaway database is dropped on exit through scripts/droptestdb.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
if [ "$(uname -s)" != "Darwin" ]; then
  echo "background-e2e: Apple Vision's model is a Mac's (MODNet is measured and tested in go test): skipped here"
  exit 0
fi
PORT="${METARUSH_E2E_PORT:-5830}"
BASE="${METARUSH_E2E_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/background-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-background$(date +%s)"
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
die() { echo "FAIL: $*" >&2; exit 1; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation with a stand-in person finder"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Background test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
mkdir -p "$BASE/helper"
cat > "$BASE/helper/metarush-vision" <<'PY'
#!/usr/bin/env python3
# A stand-in for MetaRush's on-screen helper (protocol 3): only `segment`, whose
# "people" are each frame's bright parts. Frames come in as BGRA, mattes go out
# as one grey byte a pixel, one frame at a time.
import sys
args = sys.argv[1:]
if args[:1] == ["--version"]:
    print("metarush-vision 3 macOS 26.5")
    sys.exit(0)
if args[:1] != ["segment"]:
    sys.exit(2)
opts = dict(zip(args[1::2], args[2::2]))
w, h = int(opts["--width"]), int(opts["--height"])
keep = bytes(255 if v > 160 else 0 for v in range(256))
n = w * h * 4
src, out = sys.stdin.buffer, sys.stdout.buffer
while True:
    frame = src.read(n)
    if len(frame) < n:
        break
    out.write(frame[1::4].translate(keep))
    out.flush()
PY
chmod 755 "$BASE/helper/metarush-vision"
python3 -I - "$H/config.yaml" "$BASE/helper/metarush-vision" <<'PY'
import sys
path, helper = sys.argv[1:3]
lines = open(path).read().splitlines()
out, done = [], False
for i, l in enumerate(lines):
    out.append(l)
    if l.rstrip() == "tools:" and not done:
        nxt = lines[i + 1] if i + 1 < len(lines) else ""
        pad = nxt[: len(nxt) - len(nxt.lstrip())] or "    "  # the file's own indentation
        out.append(pad + "vision: " + helper)
        done = True
if not done:
    out += ["tools:", "    vision: " + helper]
open(path, "w").write("\n".join(out) + "\n")
PY

step "Studio: a first cut of one clip, and a picture"
mkdir -p "$BASE/media"
P=$(MR project create "Studio" --json | J 'd["data"]["project"]["id"]')
ffmpeg -v error -y -f lavfi -i "color=c=0x1d3a6e:s=1280x720:r=30:d=6[bg];color=c=0xf0e6d8:s=220x420:r=30:d=6[p];[bg][p]overlay=x='420+160*sin(t)':y=180[out0]" \
  -f lavfi -i "sine=frequency=330:sample_rate=48000:duration=6" -c:v libx264 -pix_fmt yuv420p -g 30 -c:a aac -shortest "$BASE/media/host.mp4"
MR ingest "$BASE/media/host.mp4" --project "$P" --no-transcribe --wait --json | J '"added=%d" % d["data"]["ingest"]["ingested"]'
CL=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"]][0]')
ffmpeg -v error -y -f lavfi -i "color=c=0xFFD400:s=640x360" -frames:v 1 "$BASE/media/sunny.png"
MR add "$BASE/media/sunny.png" --project "$P" --json >/dev/null
cat > "$BASE/plan.json" <<EOF
{"projectId":"$P","instruction":"A first cut","spec":{"schemaVersion":1,"title":"The host","output":{"preset":"landscape-1080p","fps":30},
 "clips":[{"id":"host","assetId":"$CL","in":0.5,"out":5.5}]}}
EOF
TOK=$(MR edit check --plan "$BASE/plan.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$BASE/plan.json" --confirm "$TOK" --json | J '"version %d of Studio" % d["data"]["revision"]["number"]'
MR footage cut-out "$CL" --dry-run --json | J '"cut-out: " + d["data"]["summary"]'

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_SCREEN_INDEX=open-models METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run: Replace the background…, the cut-out made, the preview, taking it out"
SHOTS="${BACKGROUND_SHOTS:-$BASE/screenshots}"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
BACKGROUND_PROJECT="$P" BACKGROUND_CLIP="$CL" BACKGROUND_SHOTS="$SHOTS" node web/scripts/background-e2e.mjs

step "the command line: out and in again (the cut-out reused), then a review copy"
MR footage cut-outs "$CL"
TOK=$(MR cut background "$P" --clip host --off --json | J 'd["data"]["check"]["confirmToken"]')
MR cut background "$P" --clip host --off --confirm "$TOK" --json | J '"saved: " + d["data"]["summary"]'
MR cut background "$P" --clip host --with colour --colour '#00b140' --json > "$BASE/bg.json"
[ "$(J 'd["data"]["waiting"]' < "$BASE/bg.json")" = "False" ] || die "the cut-out wasn't reused"
TOK=$(J 'd["data"]["check"]["confirmToken"]' < "$BASE/bg.json")
MR cut background "$P" --clip host --with colour --colour '#00b140' --confirm "$TOK" --json | J '"saved: " + d["data"]["summary"]'
REV=$(MR revision list "$P" --json | J '[r["id"] for r in d["data"]["revisions"] if r.get("isHead")][0]')
RD=$(MR render submit --revision "$REV" --quality review --no-qa --wait --json | J 'd["data"]["render"]["id"]')
OUT="$BASE/review.mp4"
MR render fetch "$RD" --out "$OUT" >/dev/null
[ -s "$OUT" ] || die "no review copy to look at"
python3 -I - "$OUT" <<'PY'
import subprocess, sys
path = sys.argv[1]
def frame(n):
    r = subprocess.run(["ffmpeg", "-v", "error", "-i", path, "-vf", "select=eq(n\\,%d),scale=320:180" % n, "-frames:v", "1",
                        "-f", "rawvideo", "-pix_fmt", "rgb24", "-"], capture_output=True, check=True)
    return r.stdout
bad = []
for n in (15, 75, 135):
    px = frame(n)
    pixels = [px[i:i + 3] for i in range(0, len(px), 3)]
    green = sum(1 for p in pixels if p[0] < 60 and p[1] > 130 and p[2] < 110) / len(pixels)
    light = sum(1 for p in pixels if min(p) > 190) / len(pixels)
    blue = sum(1 for p in pixels if p[2] > p[1] + 25 and p[2] > 80) / len(pixels)
    print("frame %d: chroma green %.0f%%, the figure %.0f%%, the clip's own blue %.1f%%" % (n, green * 100, light * 100, blue * 100))
    if green < 0.5 or light < 0.04 or blue > 0.01:
        bad.append(n)
if bad:
    print("FAIL: the review copy doesn't show the colour behind the figure at frames %s" % bad)
    sys.exit(1)
print("the review copy shows the colour behind the figure")
PY
exit 0
