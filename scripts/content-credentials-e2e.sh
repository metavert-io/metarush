#!/usr/bin/env bash
# content-credentials-e2e.sh — Content Credentials in delivery in a real browser (D415–D424), on a throwaway
# installation that talks to nothing outside this computer. A short cut of a test clip gets its final render
# here and its quality check. Delivery from the CLI with Content Credentials off carries none. The browser run
# (web/scripts/content-credentials-e2e.mjs) approves the version as a person, makes Content Credentials
# required in Settings → Delivery, sees the version held for them ("Set them up"), turns the gate off and sees
# it held still, sends it out without them with a note (Deliver anyway…), turns the gate on again, makes a test
# certificate in the styled dialog, tries it on a sample, delivers from the version page (Deliver…) and reads
# what the video carries; with the stand-in it then makes signing fail (nothing written, the version held for
# the failed signing) and lets a sample that works free it; then the receipt's Content Credentials and the
# project's own part. Then the script reads both delivered folders back — the signed file's record, notes and
# Content Credentials (c2patool), the manifest naming nothing of the project, and the one sent without them with
# its note — and looks for the private key everywhere it must never be: the log, the dashboard's output, the
# records and the home's state. Desktop and 390 px phone, dark and light, screenshots; no native browser
# dialog, no sideways scrolling, plain words.
#
#   scripts/content-credentials-e2e.sh                        # needs scripts/dev-mongo.sh running, ffmpeg and python3
#   C2PATOOL=/path/to/c2patool scripts/content-credentials-e2e.sh   # the real c2patool (0.28 or newer) instead of the stand-in
#   KEEP=1 scripts/content-credentials-e2e.sh                 # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/content-credentials-e2e.sh           # set it up and leave the dashboard running
#   METARUSH_CREDENTIALS_PORT=5781 SKIP_BUILD=1 CREDENTIALS_SHOTS=<folder> scripts/content-credentials-e2e.sh
#
# Ports 5780–5789 are this run's (the dashboard on 5780). The throwaway database is dropped on exit through
# scripts/droptestdb (store.DropTestDatabase). The stand-in c2patool (internal/testutil/fakec2patool) signs
# through MetaRush's own signer like the real one; certificates and keys are made while the run goes.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_CREDENTIALS_PORT:-5780}"
BASE="${METARUSH_CREDENTIALS_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/content-credentials-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-credentials$(date +%s)"
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
J() { python3 -I -c "import json,sys
d=json.load(sys.stdin)
try: print($1)
except (KeyError,IndexError,TypeError): sys.exit('unexpected answer: '+json.dumps(d)[:3000])"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
for t in ffmpeg python3; do command -v "$t" >/dev/null || { echo "$t is needed" >&2; exit 1; }; done
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  nice -n 10 npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi
FAKE_STATE=""
if [ -n "${C2PATOOL:-}" ]; then
  TOOL="$C2PATOOL"
  echo "the real c2patool: $("$TOOL" --version)"
else
  # MetaRush runs c2patool with an environment built from nothing, so the stand-in's state file (the browser run
  # makes it fail and work again) is named by a wrapper.
  go build -o "$BASE/bin/fake-c2patool" ./internal/testutil/fakec2patool/cmd/fake-c2patool
  FAKE_STATE="$BASE/c2patool-state.json"; echo '{}' > "$FAKE_STATE"
  printf '#!/bin/sh\nexec /usr/bin/env METARUSH_FAKE_C2PATOOL_STATE=%s %s "$@"\n' "'$FAKE_STATE'" "'$BASE/bin/fake-c2patool'" > "$BASE/bin/c2patool"
  chmod 755 "$BASE/bin/c2patool"
  TOOL="$BASE/bin/c2patool"
  echo "the stand-in c2patool"
fi
# Every MetaRush of this run (the CLI and the dashboard) uses it. (The dashboard alone is told where Deliver… writes:
# a METARUSH_…_DIR in this script's environment would count as a folder a person named, which the run never removes.)
export METARUSH_C2PATOOL="$TOOL"

step "a throwaway installation"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Credentials test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
MR render follow off --json >/dev/null # D187: no review copies by themselves
MR credentials --json | J '"Content Credentials: %s — %s" % (d["data"]["mode"], d["data"]["here"]["says"])'
MR credentials --json | J 'd["data"]["mode"] == "off" or sys.exit("they should be off until an owner turns them on")' >/dev/null

step "a short clip, a cut and its final render, checked"
CLIPS="$BASE/clips"; mkdir -p "$CLIPS"
ffmpeg -hide_banner -loglevel error -nostdin -y -f lavfi -i "testsrc2=size=640x360:rate=30:duration=4" -f lavfi -i "sine=frequency=440:duration=4:sample_rate=48000" \
  -c:v libx264 -preset veryfast -pix_fmt yuv420p -g 30 -c:a aac -b:a 96k -shortest "$CLIPS/garden-walk.mp4"
P=$(MR project create "Acme spring launch" --json | J 'd["data"]["project"]["id"]')
MR ingest "$CLIPS/garden-walk.mp4" --project "$P" --no-transcribe --wait --json | J '"added=%d" % d["data"]["ingest"]["ingested"]'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="garden-walk.mp4"][0]')
cat > "$BASE/plan.json" <<EOF
{"projectId":"$P","instruction":"The garden","spec":{"schemaVersion":1,"title":"The garden",
 "output":{"preset":"landscape-1080p","fps":30,"platform":"youtube"},
 "clips":[{"id":"open","assetId":"$A","in":0,"out":3}]}}
EOF
TOK=$(MR edit check --plan "$BASE/plan.json" --json | J 'd["data"]["confirmToken"]')
R1=$(MR edit apply --plan "$BASE/plan.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
echo "version 1: $R1"
REND=$(nice -n 10 ./bin/metarush --home "$H" render submit --revision "$R1" --quality final --preset landscape-1080p --wait --json | J 'd["data"]["render"]["id"]')
echo "final render: $REND"
nice -n 10 ./bin/metarush --home "$H" qa run "$REND" --wait --json | J '"quality check: %s (%d problems, %d worth a look)" % (d["data"]["report"]["verdict"], d["data"]["report"]["counts"]["errors"], d["data"]["report"]["counts"]["warnings"])'

step "with Content Credentials off, a delivery carries none"
set +e
MR deliver --revision "$R1" --out "$BASE/deliveries/off" --allow-draft --json > "$BASE/off.json"; CODE=$?
set -e
J '"delivered %d file(s), Content Credentials: %s" % (len(d["data"]["files"]), d["data"]["files"][0].get("contentCredentials"))' < "$BASE/off.json" || { cat "$BASE/off.json"; exit 1; }
[ "$CODE" = "0" ] && J 'd["data"]["files"][0].get("contentCredentials") is None or sys.exit("off, but the video carries Content Credentials")' < "$BASE/off.json" >/dev/null

if [ "${NO_BROWSER:-}" = "1" ]; then
  METARUSH_EXPORTS_DIR="$BASE/exports" METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_EXPORTS_DIR="$BASE/exports" METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

step "the browser run"
SHOTS="${CREDENTIALS_SHOTS:-$BASE/screenshots}"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
CREDENTIALS_PROJECT="$P" CREDENTIALS_REVISION="$R1" CREDENTIALS_SHOTS="$SHOTS" CREDENTIALS_FAKE_STATE="$FAKE_STATE" \
node web/scripts/content-credentials-e2e.mjs

step "what the dashboard delivered: one folder sent without them with a note, one signed (a signing that failed wrote none)"
python3 -I - "$BASE/exports" > "$BASE/folders.txt" <<'PY'
import glob, json, os, sys
root = sys.argv[1]
C2PA = bytes.fromhex("d8fec3d61b0e483c92975828877ec481")
signed, anyway = [], []
folders = [f for f in sorted(os.listdir(root)) if not f.startswith(".")]
assert len(folders) == 2, ("two delivered folders, not", folders)
for f in folders:
    videos = glob.glob(os.path.join(root, f, "*.mp4"))
    assert len(videos) == 1, (f, videos)
    c = json.load(open(videos[0][:-4] + ".json")).get("contentCredentials") or {}
    (signed if c.get("signed") else anyway).append(videos[0])
assert len(signed) == 1 and len(anyway) == 1, ("one signed, one sent without them", signed, anyway)
v = anyway[0]
c = json.load(open(v[:-4] + ".json"))["contentCredentials"]
assert c.get("required") and not c.get("signed") and "with a note" in c.get("problem", ""), c
assert C2PA not in open(v, "rb").read(), "the video sent without them carries Content Credentials"
notes = open(v[:-4] + ".notes.md").read()
assert "## Sent out anyway" in notes and "without Content Credentials" in notes, notes[-800:]
assert "The client takes this one without Content Credentials." in notes, "the note isn't in the notes file"
print(signed[0])
print("sent without them, with a note: %s" % os.path.basename(os.path.dirname(v)), file=sys.stderr)
PY
VIDEO=$(head -1 "$BASE/folders.txt"); FOLDER=$(dirname "$VIDEO")
echo "signed: $(basename "$FOLDER")"
RECORD="${VIDEO%.mp4}.json"; NOTES="${VIDEO%.mp4}.notes.md"
[ ! -e "$H/state/private/content-credentials-failed.json" ] || { echo "a failed signing is still remembered after a sample worked"; exit 1; }
python3 -I - "$VIDEO" "$RECORD" <<'PY'
import hashlib, json, sys
video, record = sys.argv[1], sys.argv[2]
sc = json.load(open(record))
c = sc["contentCredentials"]
assert c["signed"] and c["verified"] and c["testCertificate"], c
assert len(c["manifestHash"]) == 64 and c["signer"], c
assert c["sourceType"].endswith("/composite"), c["sourceType"]  # nothing AI in this video
h = hashlib.sha256(open(video, "rb").read()).hexdigest()
assert c["sha256"] == h, ("the record's hash isn't the delivered file's", c["sha256"], h)
assert "carries Content Credentials" in c["says"], c["says"]
# The manifest store (the MP4's top-level C2PA uuid box) names nothing of the project.
C2PA = bytes.fromhex("d8fec3d61b0e483c92975828877ec481")
data = open(video, "rb").read()
i, store = 0, None
while i + 8 <= len(data):
    size = int.from_bytes(data[i:i+4], "big"); kind = data[i+4:i+8]; head = 8
    if size == 1:
        size = int.from_bytes(data[i+8:i+16], "big"); head = 16
    elif size == 0:
        size = len(data) - i
    if kind == b"uuid" and data[i+head:i+head+16] == C2PA:
        store = data[i+head+16:i+size]
    if size < head:
        break
    i += size
assert store is not None, "no Content Credentials box in the delivered file"
low = store.lower()
for never in (b"acme", b"spring", b"garden", b"credentials test"):
    assert never not in low, "the manifest says %r" % never
print("record: signed by %s (test certificate), checked, %d parts, the file's hash matches; the manifest names nothing of the project" % (c["signer"], c["parts"]))
PY
grep -q "## Content Credentials" "$NOTES" || { echo "the notes file has no Content Credentials section"; exit 1; }
grep -q "This video carries Content Credentials" "$NOTES" || { echo "the notes file doesn't say the video carries Content Credentials"; exit 1; }
MR credentials check "$VIDEO" --json > "$BASE/check.json"
J '"c2patool reads it back: %s" % d["data"]["summary"]' < "$BASE/check.json"
python3 -I - "$BASE/check.json" "$RECORD" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))["data"]
rec = json.load(open(sys.argv[2]))["contentCredentials"]
assert c["carries"] and c["verified"] and c["ours"], c
assert c["manifestHash"] == rec["manifestHash"], ("the manifest's hash differs from the record's", c["manifestHash"], rec["manifestHash"])
PY
if [ -n "${C2PATOOL:-}" ]; then
  # The real c2patool's own report: MetaRush's actions and the video's source type.
  ( cd "$BASE" && "$TOOL" "$VIDEO" ) > "$BASE/report.json"
  python3 -I - "$BASE/report.json" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
m = r["manifests"][r["active_manifest"]]
acts = [a for x in m["assertions"] if x["label"].startswith("c2pa.actions") for a in x["data"]["actions"]]
assert acts[0]["action"] == "c2pa.created" and acts[0]["digitalSourceType"].endswith("/composite"), acts[0]
assert acts[-1]["action"] == "c2pa.edited", acts[-1]
assert m["title"] == "Video", m["title"]
assert r.get("validation_state") in ("Valid", "Trusted"), r.get("validation_state")
print("the real c2patool: %s, %d actions, title %r, claim generator %s" % (r["validation_state"], len(acts), m["title"], m.get("claim_generator_info", [{}])[0].get("name")))
PY
fi

step "the CLI delivers it signed too, and the receipt lists both"
MR deliver --revision "$R1" --out "$BASE/deliveries/cli" --json | J '"delivered: %s" % d["data"]["files"][0]["contentCredentials"]["says"]'
MR deliver --revision "$R1" --out "$BASE/deliveries/cli2" --json | J 'd["data"]["files"][0]["contentCredentials"]["signed"] or sys.exit("not signed")' >/dev/null
MR project receipt "$P" --json | J '"\n".join(l for l in d["data"]["lines"] if "Content Credentials" in l)'
MR project receipt "$P" --json | J 'len(d["data"]["signed"]) == 3 or sys.exit("the receipt lists %d signed files, not 3" % len(d["data"]["signed"]))' >/dev/null
MR project receipt "$P" --json | J 'any("must carry Content Credentials" in h and x["wentOut"] for x in d["data"]["sentAnyway"] for h in x["held"]) or sys.exit("the receipt doesn'"'"'t list the version sent without them: %s" % d["data"]["sentAnyway"])' >/dev/null

step "the private key is nowhere it must never be"
MR log export --out "$BASE/log.jsonl" >/dev/null
MR credentials --json > "$BASE/status.json"
python3 -I - "$H" "$BASE" <<'PY'
import os, sys
home, base = sys.argv[1], sys.argv[2]
key = None
for line in open(os.path.join(home, ".env")):
    if line.startswith("METARUSH_CONTENT_CREDENTIALS_SECRET="):
        key = line.split("=", 1)[1].strip().strip('"')
assert key, "the key isn't in the home's secrets file (--secrets env)"
needles = [key, key[48:64], key[64:90]]  # the whole key, and its secret part as a PEM file's lines would carry it
looked = 0
for root, dirs, files in os.walk(base):
    dirs[:] = [d for d in dirs if d not in ("clips", "bin", "renderer", "node_modules")]
    for f in files:
        p = os.path.join(root, f)
        if p == os.path.join(home, ".env") or os.path.getsize(p) > 64 << 20:
            continue
        b = open(p, "rb").read()
        looked += 1
        for n in needles:
            assert n.encode() not in b, "the key is in %s" % p
        assert b"PRIVATE KEY" not in b, "a private key is in %s" % p
print("the key is only in the home's secrets file: %d files looked through (the log, the status, the records, the home's state, the dashboard's output)" % looked)
PY
echo "screenshots: $SHOTS"
exit 0
