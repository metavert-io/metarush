#!/usr/bin/env bash
# ship-gate-e2e.sh — the ship gate and promises made to the client in a real browser
# (D298–D302), on a throwaway installation that talks to nothing outside this computer. A new
# workspace holds versions back. A short cut of an interview, captioned, with two promises (one
# it keeps, "Captions on every video", and one it breaks, "Under 3 seconds") and a sign-off;
# its final render is made and checked on this computer, so the quality check measures the
# promises on the real file. Delivery refuses it from the CLI (E_HELD). The browser run
# (web/scripts/ship-gate-e2e.mjs) opens the version's page — Held by …, the promise not kept
# and its measure, the sign-off ticked in one click, Deliver anyway… with a note in the styled
# dialog — then Settings → General's switch, the project's Before it goes out part and promises
# in its rules, and the receipt's Sent out anyway. Then the CLI delivers it and the notes file
# says who sent it out anyway and why. Desktop and 390 px phone, dark and light, screenshots; no
# native browser dialog, no sideways scrolling, plain words.
#
#   scripts/ship-gate-e2e.sh                  # needs scripts/dev-mongo.sh running, ffmpeg (and macOS `say` for the fixtures)
#   KEEP=1 scripts/ship-gate-e2e.sh           # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/ship-gate-e2e.sh     # set it up and leave the dashboard running
#   METARUSH_SHIPGATE_PORT=5561 SKIP_BUILD=1 SHIPGATE_SHOTS=<folder> scripts/ship-gate-e2e.sh
#
# Ports 5560–5569 are this run's (the dashboard on 5560). The throwaway database is dropped on
# exit through scripts/droptestdb (store.DropTestDatabase). Fakes only: nothing goes to a paid service.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_SHIPGATE_PORT:-5560}"
BASE="${METARUSH_SHIPGATE_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/ship-gate-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-shipgate$(date +%s)"
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
[ -f tests/fixtures/gen/interview-a.mov ] || scripts/make-fixtures.sh >/dev/null
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  nice -n 10 npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation: a new workspace holds versions back"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Ship gate test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
MR render follow off --json >/dev/null # D187: no review copies by themselves
MR deliver gate --json | J '"the gate: on=%s decided=%s" % (d["data"]["on"], d["data"]["decided"])'
MR deliver gate --json | J 'd["data"]["on"] and d["data"]["decided"] or sys.exit("a new workspace should hold versions back")' >/dev/null

step "an interview, a captioned cut, two promises and a sign-off"
P=$(MR project create "Client spot" --json | J 'd["data"]["project"]["id"]')
MR ingest tests/fixtures/gen/interview-a.mov --project "$P" --wait --json | J '"added=%d" % d["data"]["ingest"]["ingested"]'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="interview-a.mov"][0]')
cat > "$BASE/plan.json" <<EOF
{"projectId":"$P","instruction":"The opening, captioned","spec":{"schemaVersion":1,"title":"The opening",
 "output":{"preset":"landscape-1080p","fps":30,"platform":"youtube"},
 "clips":[{"id":"start","assetId":"$A","in":0,"out":4.5}],
 "captions":{"enabled":true,"style":"clean","position":"lower","maxWordsPerLine":5}}}
EOF
TOK=$(MR edit check --plan "$BASE/plan.json" --json | J 'd["data"]["confirmToken"]')
R1=$(MR edit apply --plan "$BASE/plan.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
echo "version 1: $R1"
MR project rules add "$P" "Captions on every video" --promise --json | J '"promise: %s → %s" % (d["data"]["rule"]["text"], d["data"]["rule"]["measure"])'
MR project rules add "$P" "Under 3 seconds" --promise --json | J '"promise: %s → %s" % (d["data"]["rule"]["text"], d["data"]["rule"]["measure"])'
MR project rules add "$P" "Warm and friendly" --promise --json >/dev/null 2>&1 && { echo "an unmeasurable promise was kept"; exit 1; } || echo "an unmeasurable promise stays a sign-off: refused"
MR cut signoffs add "$P" "Lena OKs the final version" --json | J '"sign-offs waiting: %d" % d["data"]["open"]'

step "its final render, made and checked on this computer (the promises measured on the file)"
REND=$(nice -n 10 ./bin/metarush --home "$H" render submit --revision "$R1" --quality final --preset landscape-1080p --wait --json | J 'd["data"]["render"]["id"]')
echo "final render: $REND"
nice -n 10 ./bin/metarush --home "$H" qa run "$REND" --wait --json > "$BASE/qa.json"
python3 -I - "$BASE/qa.json" <<'PY'
import json, sys
rep = json.load(open(sys.argv[1]))["data"]["report"]
ps = {p["text"]: p for p in rep.get("promises", [])}
for t, p in ps.items():
    print("  %-26s %-11s %s" % (t, p["status"], p["measured"]))
assert ps["Under 3 seconds"]["status"] == "broken", "the 4.5 s cut breaks “Under 3 seconds”"
assert ps["Captions on every video"]["status"] in ("kept", "notApplicable"), ps["Captions on every video"]
assert any(f["kind"] == "promise_broken" and f["severity"] == "error" for f in rep["findings"]), "a broken promise is a problem"
PY

step "delivery refuses it while the check's problem and the sign-off hold it"
MR deliver ready "$P" --json | J '"%s: %s" % (d["data"]["headline"], [h["kind"] for h in d["data"]["holds"]])'
set +e
MR deliver --revision "$R1" --out "$BASE/held" --allow-draft --json > "$BASE/held.json"; CODE=$?
set -e
J '"refused (exit %s): %s %s" % ('"$CODE"', d["error"]["code"], d["error"]["message"])' < "$BASE/held.json"
[ "$CODE" = "4" ] && J 'd["error"]["code"] == "E_HELD" or sys.exit("not held")' < "$BASE/held.json" >/dev/null || { echo "delivery should refuse it (E_HELD, exit 4)"; exit 1; }
[ ! -e "$BASE/held" ] || [ -z "$(ls -A "$BASE/held")" ] || { echo "a held delivery wrote files"; exit 1; }

if [ "${NO_BROWSER:-}" = "1" ]; then
  METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

step "the browser run"
SHOTS="${SHIPGATE_SHOTS:-$BASE/screenshots}"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
SHIPGATE_PROJECT="$P" SHIPGATE_REVISION="$R1" SHIPGATE_SHOTS="$SHOTS" SHIPGATE_CLI="./bin/metarush --home $H" node web/scripts/ship-gate-e2e.mjs

step "the CLI agrees, and delivery says who sent it out anyway"
MR deliver ready "$P" --json | J '"%s (%s)" % (d["data"]["headline"], d["data"]["state"])'
MR deliver ready "$P" --json | J 'd["data"]["state"] == "sent-anyway" or sys.exit("not sent anyway")' >/dev/null
OUT="$BASE/deliveries"
MR deliver --revision "$R1" --out "$OUT" --json | J '"delivered %d file(s); sent anyway by %s" % (len(d["data"]["files"]), d["data"]["files"][0]["sentAnyway"][0]["by"]["userName"])'
NOTES=$(ls "$OUT"/*.notes.md)
grep -q "## Sent out anyway" "$NOTES" || { echo "the notes file doesn't say it was sent out anyway"; exit 1; }
grep -q "The client needs it today" "$NOTES" || { echo "the note isn't in the notes file"; exit 1; }
python3 -I -c 'import json,glob,sys; sc=json.load(open([f for f in glob.glob(sys.argv[1]+"/*.json")][0])); g=sc["gate"]; assert g["on"] and g["sentAnyway"] and any(p["status"]=="broken" for p in g["promises"]), g; print("the record: held by %s; sent anyway" % [h["kind"] for h in g["held"]])' "$OUT"
# A timeline into that delivery folder waits too, unless covered: this version was sent out anyway.
MR export timeline "$R1" --format otio --out "$OUT/opening.otio" --json | J '"a timeline into the delivery folder: %s" % d["data"]["file"].rsplit("/",1)[-1]'
MR project receipt "$P" --json | J '"\n".join(d["data"]["lines"])'
echo "screenshots: $SHOTS"
exit 0
