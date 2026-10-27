#!/usr/bin/env bash
# End-to-end run of AI services (D224–D228) in a real browser, on a throwaway installation: a fresh home
# (ai-…), a local test database, a little made-up footage and the fake Gemini and ElevenLabs APIs
# (internal/testutil/fakevideogen, fakeeleven: no account, no real key, nothing made by a real model). The browser
# run drives the Generate panel's other kinds — a picture priced with its model and the better one offered, music
# with the alternative picked and made, a sound effect, a cloud transcript of a clip — the AI work list, the
# project's Content Settings ruling a service out (then refused in the panel), and checks that no key ever reaches
# the page or the browser's storage. Screenshots are saved for review.
#
#   scripts/ai-services-e2e.sh              # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/ai-services-e2e.sh       # keep the home, database and servers for a look afterwards
#   NO_BROWSER=1 scripts/ai-services-e2e.sh # set it up and print how to sign in
#
# Ports 5400–5402 (dashboard, fake Gemini, fake ElevenLabs). The throwaway database is dropped on exit through
# store.DropTestDatabase (scripts/droptestdb refuses anything but local metarush-test-*).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_AI_PORT:-5400}"; GFAKE="${METARUSH_AI_GEMINI_PORT:-5401}"; XFAKE="${METARUSH_AI_ELEVEN_PORT:-5402}"
# Outside any repository: the keys and .env must never sit where git could commit them.
H="${METARUSH_AI_HOME:-$(mktemp -d "${TMPDIR:-/tmp}/ai-e2e-XXXXXX")/home}"; DB="metarush-test-ai$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SRV=""; GF=""; XF=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, dashboard http://127.0.0.1:$PORT (pids $SRV $GF $XF)"
    return
  fi
  for p in $SRV $GF $XF; do kill "$p" 2>/dev/null || true; done
  go run ./scripts/droptestdb "$URI" "$DB" || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$(dirname "$H")"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation that talks only to the fake Gemini and ElevenLabs APIs"
mkdir -p "$H/state"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "AI services test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
# Fake keys made at run time (each fake accepts only its own); every process of this home reads them, and the
# base URLs, from the home's .env — requests never go to Google or ElevenLabs.
GKEY="$H/state/fake-gemini.key"; XKEY="$H/state/fake-eleven.key"
python3 -I -c "import secrets;print('gk-'+secrets.token_hex(20))" > "$GKEY"
python3 -I -c "import secrets;print('xk-'+secrets.token_hex(20))" > "$XKEY"
chmod 600 "$GKEY" "$XKEY"
{
  printf 'METARUSH_MONGODB_URI=%s\n' "$URI"
  printf 'METARUSH_GEMINI_BASE_URL=http://127.0.0.1:%s\nMETARUSH_ELEVENLABS_BASE_URL=http://127.0.0.1:%s\n' "$GFAKE" "$XFAKE"
  printf 'METARUSH_GEMINI_API_KEY=%s\nMETARUSH_ELEVENLABS_API_KEY=%s\n' "$(cat "$GKEY")" "$(cat "$XKEY")"
} > "$H/.env"; chmod 600 "$H/.env"
go build -o "$H/fakevideogen" ./internal/testutil/fakevideogen/cmd/fakevideogen
go build -o "$H/fakeeleven" ./internal/testutil/fakeeleven/cmd/fakeeleven
"$H/fakevideogen" -addr "127.0.0.1:$GFAKE" -key-file "$GKEY" > "$H/fakevideogen.log" 2>&1 & GF=$!
"$H/fakeeleven" -addr "127.0.0.1:$XFAKE" -key-file "$XKEY" > "$H/fakeeleven.log" 2>&1 & XF=$!

step "footage: a short interview (a tone, in place of speech)"
ffmpeg -nostdin -loglevel error -y -f lavfi -i "testsrc2=size=320x180:rate=24:duration=6" -f lavfi -i "sine=frequency=330:duration=6" \
  -shortest -pix_fmt yuv420p "$H/interview.mp4"
P=$(MR project create "Garden film" --json | J 'd["data"]["project"]["id"]')
MR ingest "$H/interview.mp4" --project "$P" --wait --json | J '"added=%d" % d["data"]["ingest"]["ingested"]'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"]][0]')
MR content own --project "$P" --json >/dev/null

step "the CLI: models and an estimate, with --json"
MR ai models --kind music --json | J '"music models: %s" % ", ".join(m["id"] for m in d["data"]["kinds"][0]["models"])'
MR ai estimate picture "a watercolour kite" --project "$P" --json | J '"picture: %s, %s" % (d["data"]["cost"]["about"], d["data"]["modelTitle"])'

step "the dashboard on 127.0.0.1:$PORT (with its worker, which makes everything)"
METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$H/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" \
METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
AI_PROJECT="$P" AI_CLIP="$A" AI_GEMINI_URL="http://127.0.0.1:$GFAKE" AI_ELEVEN_URL="http://127.0.0.1:$XFAKE" \
AI_KEYS="$GKEY:$XKEY" AI_SHOTS="${AI_SHOTS:-$H/screenshots}" AI_MR="./bin/metarush --home $H" node web/scripts/ai-services-e2e.mjs
exit 0
