#!/usr/bin/env bash
# outside-reviewers-e2e.sh — Share for review (D313–D317) in a real browser, on a throwaway installation whose
# media is a private S3-compatible bucket on this computer (internal/testutil/fakebucket: it checks every signed
# link's signature, the headers it signs and its expiry, like a cloud bucket; nothing leaves this computer and no
# cloud account is involved). "Spring promo" has a short spoken clip, transcribed here, a cut and a real review
# copy; "Acme" uses its own Content Settings, whose What may leave keeps footage from people outside the workspace.
#
# In the browser a person opens version 1's page, Share for review… says what leaves, makes a link for Dana with
# the words and a download; a second browser with no MetaRush sign-in opens the link (only the bucket's address is
# reached), plays the video, presses a line of the script and leaves a comment pinned to a time; Look for comments
# brings it to the version as "Dana (outside)" and rings the bell; the receipt lists the link; Acme's version is
# refused with the rule in words; taking the link back makes it stop opening; a second link stops opening once the
# bucket's clock passes its signed time. Desktop and 390 px phone, dark and light, with screenshots; no native
# browser dialog, no sideways scrolling, plain words.
#
#   scripts/outside-reviewers-e2e.sh                 # needs scripts/dev-mongo.sh running, ffmpeg, say (macOS) and python3
#   KEEP=1 scripts/outside-reviewers-e2e.sh          # keep the home, database, bucket and dashboard afterwards
#   NO_BROWSER=1 scripts/outside-reviewers-e2e.sh    # set it up and leave the dashboard running
#   METARUSH_SHARE_PORT=5590 SKIP_BUILD=1 SHARE_SHOTS=<folder> scripts/outside-reviewers-e2e.sh
#
# Ports 5590–5599 belong to this run (the dashboard on 5590, the bucket on 5591). The throwaway database is
# dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. scripts/lib/status.sh
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_SHARE_PORT:-5590}"; BPORT=$((PORT + 1))
BASE="${METARUSH_SHARE_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/outside-reviewers-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-share$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SRV=""; FB=""
# The run's exit status is kept by scripts/lib/e2e.sh, which calls this first.
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, bucket http://127.0.0.1:$BPORT (pid $FB), dashboard http://127.0.0.1:$PORT (pid $SRV)"
    return
  fi
  for p in "$SRV" "$FB"; do
    [ -n "$p" ] || continue
    kill "$p" 2>/dev/null || true
    for _ in $(seq 1 60); do kill -0 "$p" 2>/dev/null || break; sleep 0.25; done
  done
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
  # The folder goes with scripts/lib/e2e.sh: removed when the run passed, kept a day when it failed.
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
for t in ffmpeg python3 say; do command -v "$t" >/dev/null || { echo "$t is needed" >&2; exit 1; }; done
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi
go build -o "$BASE/fakebucket" ./internal/testutil/fakebucket/cmd/fakebucket

step "a private bucket on 127.0.0.1:$BPORT (a key made for this run)"
KEYID="e2e$(date +%s)"
python3 -I -c 'import secrets;print(secrets.token_hex(24))' > "$BASE/bucket.secret"; chmod 600 "$BASE/bucket.secret"
e2e_bg "$BASE/fakebucket.log" "$BASE/fakebucket" -addr "127.0.0.1:$BPORT" -bucket media -key-id "$KEYID" -secret-file "$BASE/bucket.secret"; FB=$E2E_BG
for _ in $(seq 1 40); do curl -s -o /dev/null "http://127.0.0.1:$BPORT/" && break; sleep 0.25; done

step "a throwaway installation whose media is that bucket"
mkdir -p "$H"
METARUSH_MONGODB_URI="$URI" METARUSH_S3_SECRET_ACCESS_KEY="$(cat "$BASE/bucket.secret")" MR init --yes --test-db --secrets env --db-name "$DB" \
  --workspace-name "Studio" --media s3 --s3-preset custom --s3-endpoint "http://127.0.0.1:$BPORT" --s3-region us-east-1 --s3-bucket media \
  --s3-prefix team --s3-access-key-id "$KEYID" --s3-path-style --port "$PORT" >/dev/null
# No review copies by themselves (D187): this run renders one, when it asks.
MR render follow off --json >/dev/null

step "a short spoken clip, transcribed on this computer; a cut of it"
CLIPS="$BASE/clips"; mkdir -p "$CLIPS"
say -v Samantha -o "$CLIPS/voice.aiff" -- "Welcome to the garden. This spring we planted tomatoes, beans and sunflowers. Tell us what you think of the ending."
ffmpeg -hide_banner -loglevel error -nostdin -y -f lavfi -i "testsrc2=size=1280x720:rate=30:duration=9" -i "$CLIPS/voice.aiff" \
  -c:v libx264 -preset veryfast -pix_fmt yuv420p -g 30 -c:a aac -b:a 128k -shortest "$CLIPS/garden.mp4"
P=$(MR project create "Spring promo" --json | J 'd["data"]["project"]["id"]')
MR ingest "$CLIPS/garden.mp4" --project "$P" --wait --json | J '"added=%d" % d["data"]["ingest"]["ingested"]'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="garden.mp4"][0]')
MR asset update "$A" --title "Garden walk" --json >/dev/null
MR footage transcript "$A" --json | J '"heard: " + " ".join(s["text"] for s in d["data"]["segments"])'
DUR=$(MR asset show "$A" --json | J 'round(min(8.5, d["data"]["asset"]["media"]["duration"]), 2)')
cat > "$BASE/cut.json" <<EOF
{"projectId":"$P","instruction":"the garden, whole","spec":{"schemaVersion":1,"title":"The garden",
 "output":{"preset":"landscape-1080p","fps":30,"platform":"youtube"},"clips":[{"id":"walk","assetId":"$A","in":0,"out":$DUR}]}}
EOF
TOK=$(MR edit check --plan "$BASE/cut.json" --json | J 'd["data"]["confirmToken"]')
R=$(MR edit apply --plan "$BASE/cut.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
echo "version 1: $R"

step "a review copy of it (rendered here, from this checkout's renderer)"
DEV=(env METARUSH_VIDEO_DIR="$ROOT/video" METARUSH_DEV_RENDERER=1)
"${DEV[@]}" ./bin/metarush --home "$H" render submit --revision "$R" --quality review --no-qa --wait --json | J '"review copy: %s" % d["data"]["render"]["state"]'

step "Acme: its own Content Settings and its own clip (the browser run keeps its footage from people outside the workspace)"
ACME=$(MR project create "Acme" --json | J 'd["data"]["project"]["id"]')
MR content own --project "$ACME" >/dev/null
ffmpeg -hide_banner -loglevel error -nostdin -y -f lavfi -i "smptebars=size=1280x720:rate=30:duration=4" -f lavfi -i "sine=frequency=440:duration=4:sample_rate=48000" \
  -c:v libx264 -preset veryfast -pix_fmt yuv420p -g 30 -c:a aac -b:a 96k -shortest "$CLIPS/acme.mp4"
MR ingest "$CLIPS/acme.mp4" --project "$ACME" --no-transcribe --wait --json | J '"added=%d" % d["data"]["ingest"]["ingested"]'
AA=$(MR asset list "$ACME" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="acme.mp4"][0]')
sed -e "s/$P/$ACME/" -e "s/$A/$AA/" -e "s/\"out\":$DUR/\"out\":3/" "$BASE/cut.json" > "$BASE/acme.json"
TOK=$(MR edit check --plan "$BASE/acme.json" --json | J 'd["data"]["confirmToken"]')
RA=$(MR edit apply --plan "$BASE/acme.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
echo "Acme's version 1: $RA"

step "the dashboard on 127.0.0.1:$PORT"
export METARUSH_DEV_ORIGINS=""
e2e_bg "$BASE/serve.log" ./bin/metarush --home "$H" serve --no-watch; SRV=$E2E_BG
for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
SHARE_BUCKET="http://127.0.0.1:$BPORT" SHARE_PROJECT="$P" SHARE_REVISION="$R" SHARE_ACME="$ACME" SHARE_ACME_REVISION="$RA" \
SHARE_SHOTS="${SHARE_SHOTS:-$BASE/screenshots}" node web/scripts/outside-reviewers-e2e.mjs

step "what the browser did, from the CLI"
MR share list "$P" --all
MR comment list --project "$P"
MR project leaves "$P"
exit 0
