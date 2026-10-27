#!/usr/bin/env bash
# End-to-end run of presenters (D273–D277) in a real browser, on a throwaway installation: a fresh home
# (presenters-…), a local test database, a little made-up footage and the fake D-ID and HeyGen APIs
# (internal/testutil/fakedid, fakeheygen: no account, no real key, nothing made by a real model). Ada Lovelace, her consent
# (recorded, covering a presenter, naming D-ID), her approved look and her presenter waiting for its test clip are
# seeded (scripts/presenterseed): recording someone on camera can't be played by a browser. The browser run checks
# Settings → Integrations' Presenters, then in the Generate panel: a test clip priced and made, the presenter
# approved, a clip read by it with green behind it (MetaRush's own sound under D-ID's picture, marked as an AI
# presenter), a clip read by one of HeyGen's stock presenters (licensed for client work), a new presenter begun (D-ID's words to read shown), the project's Content Settings ruling presenters
# out (then refused in the panel), the presenter retired in a styled confirm and deleted at the fake; no key in a
# page or the browser's storage. Screenshots are saved for review.
#
#   scripts/presenters-e2e.sh              # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/presenters-e2e.sh       # keep the home, database and servers for a look afterwards
#   NO_BROWSER=1 scripts/presenters-e2e.sh # set it up and print how to sign in
#
# Ports 5510 (dashboard), 5512 (fake D-ID) and 5513 (fake HeyGen), within 5510–5519. The throwaway database is dropped on exit through
# store.DropTestDatabase (scripts/droptestdb refuses anything but local metarush-test-*).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_PRESENTERS_PORT:-5510}"; DFAKE="${METARUSH_PRESENTERS_DID_PORT:-5512}"; HFAKE="${METARUSH_PRESENTERS_HEYGEN_PORT:-5513}"
# Outside any repository: the keys and .env must never sit where git could commit them.
H="${METARUSH_PRESENTERS_HOME:-$(mktemp -d -t presenters-e2e)/home}"; DB="metarush-test-prs$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SRV=""; DF=""; HF=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, dashboard http://127.0.0.1:$PORT (pids $SRV $DF $HF)"
    return
  fi
  for p in $SRV $DF $HF; do kill "$p" 2>/dev/null || true; done
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

step "a throwaway installation that talks only to the fake D-ID and HeyGen APIs"
mkdir -p "$H/state"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Presenters test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
# A fake key made at run time (the fake accepts only it); every process of this home reads it, and the base URL,
# from the home's .env — requests never go to D-ID.
DKEY="$H/state/fake-did.key"; HKEY="$H/state/fake-heygen.key"
python3 -I -c "import secrets;print('ZGlk'+secrets.token_hex(20))" > "$DKEY"
python3 -I -c "import secrets;print('hg'+secrets.token_hex(20))" > "$HKEY"
chmod 600 "$DKEY" "$HKEY"
{
  printf 'METARUSH_MONGODB_URI=%s\n' "$URI"
  printf 'METARUSH_D_ID_BASE_URL=http://127.0.0.1:%s\nMETARUSH_HEYGEN_BASE_URL=http://127.0.0.1:%s\n' "$DFAKE" "$HFAKE"
  printf 'METARUSH_D_ID_API_KEY=%s\nMETARUSH_HEYGEN_API_KEY=%s\n' "$(cat "$DKEY")" "$(cat "$HKEY")"
} > "$H/.env"; chmod 600 "$H/.env"
go build -o "$H/fakedid" ./internal/testutil/fakedid/cmd/fakedid
"$H/fakedid" -addr "127.0.0.1:$DFAKE" -key-file "$DKEY" -presenter "avt_e2eada=Ada Lovelace [mr-e2e]" > "$H/fakedid.log" 2>&1 & DF=$!
go build -o "$H/fakeheygen" ./internal/testutil/fakeheygen/cmd/fakeheygen
"$H/fakeheygen" -addr "127.0.0.1:$HFAKE" -key-file "$HKEY" > "$H/fakeheygen.log" 2>&1 & HF=$!

step "footage: Ada talking (a tone, in place of speech), and a line of narration"
ffmpeg -nostdin -loglevel error -y -f lavfi -i "testsrc2=size=320x180:rate=24:duration=6" -f lavfi -i "sine=frequency=330:duration=6" \
  -shortest -pix_fmt yuv420p "$H/ada-says.mp4"
ffmpeg -nostdin -loglevel error -y -f lavfi -i "sine=frequency=440:duration=4" "$H/line.wav"
P=$(MR project create "Garden film" --json | J 'd["data"]["project"]["id"]')
MR ingest "$H/ada-says.mp4" --project "$P" --wait --json | J '"added=%d" % d["data"]["ingest"]["ingested"]'
MR ingest "$H/line.wav" --project "$P" --wait --json | J '"added=%d" % d["data"]["ingest"]["ingested"]'
SAYS=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"].startswith("ada-says")][0]')
LINE=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"].startswith("line")][0]')
MR content own --project "$P" --json >/dev/null
go run ./scripts/presenterseed "$URI" "$DB" "$SAYS" avt_e2eada

step "the CLI: presenters, stock presenters and D-ID's training terms, with --json"
MR presenter list --json | J '"presenters: %s" % ", ".join("%s (%s)" % (p["name"], p["state"]) for p in d["data"]["presenters"])'
MR presenter stock --service d-id --json | J '"d-id stock offered: %s" % d["data"]["services"][0]["offered"]'
MR presenter opt-out d-id --json | J '"d-id trains: %s" % d["data"]["trains"]'
MR ai estimate presenter --presenter stock:x --model d-id/express --clip "$LINE" --project "$P" --json >/dev/null 2>&1 && { echo "a D-ID stock presenter was priced"; exit 1; } || echo "D-ID's stock presenters refused (their licence is unstated)"

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
PR_PROJECT="$P" PR_LINE="$LINE" PR_KEY="$DKEY" PR_HEYGEN_KEY="$HKEY" PR_SHOTS="${PR_SHOTS:-$H/screenshots}" node web/scripts/presenters-e2e.mjs || { echo "--- dashboard log"; tail -40 "$H/serve.log"; echo "--- fake D-ID log"; tail -20 "$H/fakedid.log"; exit 1; }
exit 0
