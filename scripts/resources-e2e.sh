#!/usr/bin/env bash
# resources-e2e.sh — resources, the Add box, safe drags onto projects and the Sources "…" menu (D79–D84) in a real
# browser, on a throwaway installation: a fresh home, a local test database and the fake downloader
# (internal/testutil/fakeytdlp: no network). The seed is what web/scripts/resources-e2e.mjs expects (docs/resources.md,
# "Trying it"): the projects "Test project" (interview-a and broll-c, a cut with a corner logo and Brand's cut nested
# at the end, a linked folder, the link to Brand), "fernbrook" (empty) and "Brand" (a cut of its own), four clips in
# the Inbox, and the brand kit with a logo. The browser run drops a file and pastes links into fernbrook, drags Inbox
# clips onto project cards (Undo, Move…, two at once), uses the Sources "…" menu (Where it's used, Move to another
# project…, Remove from this project, Delete…), nests Brand's cut from the dashboard and saves screenshots (desktop and
# phone, dark and light).
#
#   scripts/resources-e2e.sh                  # needs scripts/dev-mongo.sh running and tests/fixtures/gen
#   KEEP=1 SKIP_BUILD=1 scripts/resources-e2e.sh
#   NO_BROWSER=1 scripts/resources-e2e.sh     # only the seeded installation, kept running for a look
#
# Ports (this run's own, 5660–5669): the dashboard 5660. The throwaway database is dropped on exit.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_RESOURCES_PORT:-5660}"
TMPBASE="${TMPDIR:-/tmp}"; TMPBASE="${TMPBASE%/}"
# Outside any repository: the home's .env must never sit where git could commit it.
BASE="${METARUSH_RESOURCES_DIR:-$(mktemp -d "$TMPBASE/resources-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-res$(date +%s)"
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
# Its own binary next to the home (a run of acceptance.sh may be using bin/metarush meanwhile).
BIN="${METARUSH_BIN:-$BASE/metarush}"
MR() { "$BIN" --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }
if (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null; then die "port $PORT is in use (an earlier run left something running? scripts/lib/e2e.sh list)"; fi
# Builds of the on-screen helper (macOS) stay in this run's folder, not the user's cache.
export METARUSH_VISION_CACHE="$BASE/vision-cache"
[ -f tests/fixtures/gen/interview-a.mov ] || scripts/make-fixtures.sh
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -ldflags "$(scripts/version.sh --ldflags)" -o "$BIN" ./cmd/metarush
elif [ ! -x "$BIN" ]; then
  # SKIP_BUILD (scripts/check.sh): the binary already built, copied so a run of acceptance.sh keeps bin/metarush.
  cp -c bin/metarush "$BIN" 2>/dev/null || cp bin/metarush "$BIN"
fi
# The fake downloader, for the links the browser pastes (the daemon's worker runs the imports).
mkdir -p "$BASE/fake-yt-dlp"
go build -o "$BASE/fake-yt-dlp/yt-dlp" ./internal/testutil/fakeytdlp/cmd/fake-yt-dlp
export METARUSH_YTDLP="$BASE/fake-yt-dlp/yt-dlp"

step "a throwaway installation"
mkdir -p "$H"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Resources test" --media filesystem --port "$PORT" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"

step "the projects, four clips in the Inbox, and Test project's footage"
T=$(MR project create "Test project" --json | J 'd["data"]["project"]["id"]')
F=$(MR project create "fernbrook" --json | J 'd["data"]["project"]["id"]')
B=$(MR project create "Brand" --json | J 'd["data"]["project"]["id"]')
MR ingest tests/fixtures/gen/vfr.mp4 tests/fixtures/gen/visual-a.mp4 tests/fixtures/gen/visual-b.mp4 tests/fixtures/gen/long-silence.mp4 --no-transcribe --wait --json \
  | J '"inbox: ingested=%d" % d["data"]["ingest"]["ingested"]'
MR ingest tests/fixtures/gen/interview-a.mov tests/fixtures/gen/broll-c.mp4 --project "$T" --no-transcribe --wait --json \
  | J '"Test project: ingested=%d" % d["data"]["ingest"]["ingested"]'
MR ingest tests/fixtures/gen/phone-rotated.mov --project "$B" --no-transcribe --wait --json | J '"Brand: ingested=%d" % d["data"]["ingest"]["ingested"]'
id_of() { MR asset list --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="'"$1"'"][0]'; }
IA=$(id_of interview-a.mov); PR=$(id_of phone-rotated.mov)

step "a logo, the brand kit and a linked folder"
RF="$BASE/files"; mkdir -p "$RF" "$BASE/Brand art"
printf '<svg xmlns="http://www.w3.org/2000/svg" width="480" height="160" viewBox="0 0 480 160"><rect x="8" y="8" width="144" height="144" rx="36" fill="#E2552D"/><circle cx="80" cy="80" r="38" fill="#FFF4E8"/><rect x="176" y="56" width="280" height="48" rx="8" fill="#1B1B1F"/></svg>\n' > "$RF/acme-logo.svg"
MR add "$RF/acme-logo.svg" --project "$T" --json | J '"added: %s" % [f["message"] for f in d["data"]["paths"]["files"]]'
MR brand set --logo acme-logo --json | J '"brand kit: logo %s, colours %s" % (d["data"]["logo"]["name"], [d["data"]["brand"].get(k) for k in ("primary", "accent", "background")])'
ffmpeg -nostdin -loglevel error -y -f lavfi -i "color=c=0x1D4E89:s=640x360" -frames:v 1 "$BASE/Brand art/backdrop.png"
MR add "$BASE/Brand art" --project "$T" --json | J '"linked: %s" % [f["message"] for f in d["data"]["paths"].get("folders", [])]'

step "Brand's cut, and Test project's cut: a corner logo, then Brand's cut nested at the end"
cat > "$BASE/brand-cut.json" <<EOF
{"projectId":"$B","instruction":"the brand intro",
 "spec":{"schemaVersion":1,"title":"Brand intro","output":{"preset":"landscape-1080p","fps":30},
  "clips":[{"id":"intro","assetId":"$PR","in":0,"out":2}]}}
EOF
TOK=$(MR edit check --plan "$BASE/brand-cut.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$BASE/brand-cut.json" --confirm "$TOK" --json | J '"Brand: version %d" % d["data"]["revision"]["number"]'
cat > "$BASE/test-cut.json" <<EOF
{"projectId":"$T","instruction":"first cut","rationale":"The interview with our logo in the corner.",
 "spec":{"schemaVersion":1,"title":"Test cut","output":{"preset":"landscape-1080p","fps":30},
  "clips":[{"id":"talk","assetId":"$IA","in":0,"out":4}],
  "overlays":[{"id":"corner","template":"logo.corner","at":0,"duration":4,"params":{"logo":"logo"}}]}}
EOF
TOK=$(MR edit check --plan "$BASE/test-cut.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$BASE/test-cut.json" --confirm "$TOK" --json | J '"Test project: version %d" % d["data"]["revision"]["number"]'
MR project link "$T" "$B" --json | J 'd["data"]["message"]'
NT=$(MR cut nest "$T" "$B" --dry-run --json | J 'd["data"]["check"]["confirmToken"]')
MR cut nest "$T" "$B" --confirm "$NT" --json | J '"nested: %s (version %d)" % (d["data"]["summary"], d["data"]["applied"]["revision"]["number"])'

step "the dashboard on 127.0.0.1:$PORT (with its worker, which runs the link imports)"
e2e_bg "$H/serve.log" env METARUSH_DEV_ORIGINS="" "$BIN" --home "$H" serve; SRV=$E2E_BG
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done
curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null || { tail -40 "$H/serve.log"; die "the dashboard didn't start"; }

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: $BIN --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run"
# A small clip no one has yet (a noise seed of its own), for the drop onto fernbrook.
DROP="$BASE/files/dropped-$(date +%s)-$$.mp4"
ffmpeg -nostdin -loglevel error -y -f lavfi -i "color=c=0x2E7D32:s=320x180:r=30:d=2" -f lavfi -i "sine=frequency=440:duration=2" \
  -vf "noise=alls=40:allf=t+u:all_seed=$((RANDOM * 32768 + RANDOM)),format=yuv420p" -c:v libx264 -c:a aac -shortest "$DROP"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="$BIN --home $H dashboard --print-link --no-open" \
  DROP_FILE="$DROP" SHOTS_DIR="${SHOTS_DIR:-$BASE/screenshots}" node web/scripts/resources-e2e.mjs || { echo "--- dashboard log"; tail -40 "$H/serve.log"; exit 1; }
echo; echo "resources-e2e: passed"
exit 0
