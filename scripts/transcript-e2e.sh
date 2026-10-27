#!/usr/bin/env bash
# transcript-e2e.sh — fixing transcripts in a real browser (D145–D149), on a
# throwaway installation: an interview and a clip of names (made with the Mac's
# voice) transcribed on this computer, a cut with captions, then in the
# dashboard the spelling list correcting a transcript, a word retyped, who says
# a stretch, find and replace (a join; only the ticked places), a clip's
# history with undo, the text fixes waiting for a new version (one click: one
# version and a review copy), a caption fixed from the review player, the
# caption check, a captions file, Claude Code panel cards (a fake `claude`:
# numbers as digits, a name on the spelling list, the new version), the
# old Settings → Footage spelling address opening the default's Content Settings (D193), the Script view and a clip's own
# transcript. Screenshots: desktop and phone, dark and light. Needs macOS `say`
# and ffmpeg.
#
#   scripts/transcript-e2e.sh                 # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/transcript-e2e.sh          # keep the home, database and dashboard afterwards
#   METARUSH_E2E_PORT=4929 SKIP_BUILD=1 METARUSH_E2E_DIR=<empty folder> scripts/transcript-e2e.sh
#
# The throwaway database is dropped on exit through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_E2E_PORT:-4929}"
BASE="${METARUSH_E2E_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/transcript-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-transcript$(date +%s)"
URI="$(scripts/dev-mongo.sh start)"
SRV=""
cleanup() {
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: home $H, database $DB, dashboard http://127.0.0.1:$PORT (pid $SRV)"
    return
  fi
  [ -n "$SRV" ] && kill "$SRV" 2>/dev/null || true
  go run ./scripts/droptestdb "$URI" "$DB" >/dev/null || true
  [ -z "${METARUSH_E2E_DIR:-}" ] && rm -rf "$BASE" || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$BASE"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
scripts/make-fixtures.sh >/dev/null
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation with a fake claude"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Transcript test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
go build -o "$BASE/claude" ./internal/assistant/fakeclaude/cmd/fakeclaude

step "an interview and a clip of names, transcribed on this computer; a cut with captions"
# The names clip is made here with the Mac's voice (like the fixtures): the transcriber writes "Katelyn" another
# way than the spelling list's "Kaytlin" (so only how it is heard can fix it), and "Maple Grove", which find and
# replace then joins.
mkdir -p "$BASE/media"
say -v Samantha -o "$BASE/media/names.aiff" -- "We met Katelyn at the Maple Grove on Elmwood last week. Ask Katelyn about the garden, and Katelyn will tell you everything."
ffmpeg -v error -y -f lavfi -i "color=c=0x2a6f4e:s=640x360:r=30" -i "$BASE/media/names.aiff" -shortest -c:v libx264 -pix_fmt yuv420p -c:a aac -ar 48000 "$BASE/media/names.mp4"
P=$(MR project create "Garden story" --json | J 'd["data"]["project"]["id"]')
MR ingest tests/fixtures/gen/interview-a.mov "$BASE/media/names.mp4" --project "$P" --wait --json | J '"added=%d jobs=%s" % (d["data"]["ingest"]["ingested"], sorted({j["state"] for j in d["data"]["jobs"]}))'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="interview-a.mov"][0]')
N=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="names.mp4"][0]')
NEND=$(MR asset list "$P" --json | J '"%.2f" % ([a["duration"] for a in d["data"]["assets"] if a["name"]=="names.mp4"][0] - 0.05)')
MR asset update "$A" --title "Interview" --json >/dev/null
MR asset update "$N" --title "Names" --json >/dev/null
MR footage transcript "$N" --json | J '"the names clip, as heard: " + " ".join(s["text"] for s in d["data"]["segments"])'
# The interview's clips end on words its ground truth guarantees (tests/fixtures/truth/interview-a.*), where this
# computer heard them: "start" runs to the end of the first sentence ("… what I was doing."), "question" from just
# before the pause ends to the end of "What would you build if nobody was watching?". A Mac without the fixture's
# voice says them at other times, and fixed times would cut them off.
CLIPS=$(MR footage transcript "$A" --words --json | python3 -I -c '
import json, re, sys
d = json.load(sys.stdin)["data"]
text = open(sys.argv[1]).read()
truth = json.load(open(sys.argv[2]))
words = [w for s in d["segments"] for w in (s.get("words") or [])]
core = lambda w: re.sub(r"^\W+|\W+$", "", w).lower()
sentences = [s.strip() for s in re.findall(r"[^.?!]+[.?!]", text)]
question = next(s for s in sentences if s.startswith(truth["pause_before_word"]))
def end_of(sentence, after):
    last = core(sentence.split()[-1])
    for i, w in enumerate(words):
        if core(w["w"]) == last and w["f"] >= after:
            nxt = words[i + 1]["f"] if i + 1 < len(words) else w["t"] + 1
            return min(w["t"] + 0.3, (w["t"] + nxt) / 2)
    sys.exit("the interview was heard without “%s”, the last word of “%s”: %s" % (last, sentence, " ".join(s["text"] for s in d["segments"])))
q_in = truth["pause_end_s"] - 0.36
print("%.2f %.2f %.2f" % (end_of(sentences[0], 0), q_in, end_of(question, q_in)))
' tests/fixtures/truth/interview-a.txt tests/fixtures/truth/interview-a.json)
read -r START_OUT Q_IN Q_OUT <<< "$CLIPS"
echo "the interview's clips: 0–${START_OUT} s and ${Q_IN}–${Q_OUT} s"
cat > "$BASE/plan.json" <<EOF
{"projectId":"$P","instruction":"The garage, the question and the garden, captioned","spec":{"schemaVersion":1,"title":"The question",
 "output":{"preset":"vertical-1080p","fps":30,"platform":"youtube"},
 "clips":[{"id":"start","assetId":"$A","in":0,"out":$START_OUT},{"id":"question","assetId":"$A","in":$Q_IN,"out":$Q_OUT},{"id":"names","assetId":"$N","in":0,"out":$NEND}],
 "captions":{"enabled":true,"style":"clean","position":"lower","maxWordsPerLine":5}}}
EOF
TOK=$(MR edit check --plan "$BASE/plan.json" --json | J 'd["data"]["confirmToken"]')
R1=$(MR edit apply --plan "$BASE/plan.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
echo "version 1: $R1"

# The fake claude: asked for numbers as digits and told a name is right, it looks, then asks for the
# change, the spelling list and the new version (each refused for it, so each becomes a card).
cat > "$BASE/claude.json" <<EOF
{"turns":[{"match":"Numbers as digits","costUsd":0.002,"steps":[
  {"text":"I'll find where the numbers are said first."},
  {"tool":{"name":"Bash","input":{"command":"metarush transcript find 'three' --project $P --json"},"result":"{\"ok\":true,\"data\":{}}"}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush transcript replace 'three' '3' --project $P --json"}}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush transcript spelling add Elmwood --project $P --json"}}},
  {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush transcript pending apply $P --json"}}},
  {"text":"I've put them on cards: the number as a digit, Elmwood on the spelling list, and one new version with every text fix."}
]}]}
EOF

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_CLAUDE="$BASE/claude" METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run: fixing the transcript"
SHOTS="${TRANSCRIPT_SHOTS:-$BASE/screenshots}"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
TRANSCRIPT_PROJECT="$P" TRANSCRIPT_INTERVIEW="$A" TRANSCRIPT_NAMES="$N" TRANSCRIPT_REVISION="$R1" TRANSCRIPT_SHOTS="$SHOTS" node web/scripts/transcript-e2e.mjs

step "what the browser did, from the CLI"
MR transcript history "$A"
MR transcript history "$N"
MR transcript spelling list --project "$P"
MR transcript pending list "$P"
exit 0
