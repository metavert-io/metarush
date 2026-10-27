#!/usr/bin/env bash
# End-to-end run of remote access over Tailscale (D128, D129) in a real
# browser, on a throwaway installation: a fresh home and local test database,
# a fake `tailscale` (never the real one: nothing on the tailnet changes) and
# fake-serve, an HTTPS stand-in for `tailscale serve` with a certificate for a
# made-up tailnet name that hands requests to MetaRush on 127.0.0.1 the way
# serve does. Chromium resolves that name to this computer and trusts the
# certificate's key.
#
# It turns remote access on with the real CLI in a pseudo-terminal (people
# only, with its question answered "y"), --apply included, and makes Tailscale
# accounts the sign-in method (D183: a Tailscale identity counts only then),
# then checks in the browser: a listed Tailscale account is signed in by itself; one not on the
# list gets the plain page; headers pretending to be Tailscale's are refused
# from a connection that isn't loopback (a second MetaRush on the link-local
# address of lo0) and on this computer's own address; the microphone records
# through the HTTPS address; host-only actions say "on <machine>"; a computer
# without a listed account gets the plain "sign in to Tailscale" page, and a
# one-time link made on this computer is refused at the public address (D183:
# those links sign in only the owner, on this computer); and a Claude Code
# conversation (a fake claude) two people share (D179): one started by Sam
# shows in Maya's project list with who started it, she continues it, and
# each sees who said and decided what. Screenshots go to REMOTE_SHOTS
# (default .agent-home/remote-shots).
#
#   scripts/remote-e2e.sh            # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/remote-e2e.sh     # keep the homes, database and servers for a look afterwards
#   SKIP_BUILD=1 METARUSH_REMOTE_PORT=5150 scripts/remote-e2e.sh
#
# Ports: PORT (MetaRush), PORT+1 (fake serve), PORT+3 (the second MetaRush, on
# fe80::1%lo0), PORT+4 (the hop that keeps spoofed headers). The throwaway
# database is dropped on exit through store.DropTestDatabase.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_REMOTE_PORT:-5150}"; SERVE=$((PORT + 1)); BPORT=$((PORT + 3)); SPOOF=$((PORT + 4))
NAME="studio-mac.tail0000.ts.net"
STAMP="$(date +%s)"
H="${METARUSH_REMOTE_HOME:-$ROOT/.agent-home/remote-e2e-$STAMP}"; HB="$H-b"; DB="metarush-test-remote$STAMP"
SHOTS="${REMOTE_SHOTS:-$ROOT/.agent-home/remote-shots}"
URI="$(scripts/dev-mongo.sh start)"
PIDS=()
cleanup() {
  local code="$1"
  if [ "${KEEP:-}" = "1" ]; then
    echo "kept: homes $H $HB, database $DB, https://$NAME:$SERVE (pids ${PIDS[*]:-})"
    return
  fi
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  sleep 1 # let them close the database before it is dropped
  go run ./scripts/droptestdb "$URI" "$DB" || true
  # A failed run keeps its homes and logs for a look; a home you named is never removed.
  if [ "$code" = "0" ] && [ -z "${METARUSH_REMOTE_HOME:-}" ]; then rm -rf "$H" "$HB" "$H-watched"; fi
}
trap 'cleanup $?' EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$H" "$HB" "$H-watched"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
# A person at a terminal: the CLI's people-only questions answered "y".
AS_PERSON() { printf 'y\ny\ny\n' | script -q /dev/null "$@" | tr -d '\r'; }
wait_up() { local u="$1"; shift; for _ in $(seq 1 120); do curl -sf "$@" "$u" >/dev/null 2>&1 && return 0; sleep 0.25; done; echo "not answering: $u" >&2; return 1; }

[ -f tests/fixtures/gen/interview-a.mov ] || scripts/make-fixtures.sh
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation whose tailscale is the fake"
mkdir -p "$H" "$SHOTS"
go build -o "$H/tailscale" ./internal/testutil/faketailscale/cmd/fake-tailscale
go build -o "$H/fake-serve" ./internal/testutil/fakeserve/cmd/fake-serve
cat > "$H/tailscale-state.json" <<EOF
{"backendState":"Running","dnsName":"$NAME.","hostName":"Sam's MacBook","login":"sam@example.com","displayName":"Sam Lee","certs":true}
EOF
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --allow-unsafe-secret-path --db-name "$DB" --workspace-name "Remote test" \
  --media filesystem --port "$PORT" --video-dir "$ROOT/video" --user-name "Sam Lee" --user-email sam@example.com >/dev/null
# This run checks the notes Claude is told at once (D142); holding them for the person's next message (D181,
# the default) has its own test (TestNewsWaitsForTheNextMessage).
grep -q '^assistant:' "$H/config.yaml" || printf 'assistant:\n  holdNews: false\n' >> "$H/config.yaml"
cat > "$H/.env" <<EOF
METARUSH_MONGODB_URI=$URI
METARUSH_TAILSCALE=$H/tailscale
METARUSH_FAKE_TAILSCALE_STATE=$H/tailscale-state.json
METARUSH_EXPORTS_DIR=$H/exports
EOF
chmod 600 "$H/.env"

step "remote status before: off, and what it needs"
MR remote status

step "people-only: refused without a terminal"
if MR remote enable --allow maya@example.com >/dev/null 2>&1; then echo "remote enable ran without a person" >&2; exit 1; fi
echo "refused without a person: ok"

step "remote enable --apply as a person (pseudo-terminal, answering yes)"
AS_PERSON ./bin/metarush --home "$H" remote enable --url "https://$NAME:$SERVE" --allow maya@example.com --machine-name "Sam's MacBook" --apply
MR remote status --json | J '"on=%s ready=%s url=%s people=%s" % (d["data"]["on"], d["data"]["ready"], d["data"]["url"], [p["login"] for p in d["data"]["people"]])'
MR remote status --json | python3 -I -c 'import json,sys;d=json.load(sys.stdin)["data"];assert d["on"] and d["ready"] and d["serve"]["active"], d;print("ready: ok")'

step "Tailscale accounts as the sign-in method (D183: a Tailscale identity counts only with it), as a person"
AS_PERSON ./bin/metarush --home "$H" auth method tailscale --login sam@example.com | tee "$H/auth-method.txt"
grep -q "People sign in with their Tailscale accounts." "$H/auth-method.txt" || { echo "auth method tailscale didn't say it is set" >&2; exit 1; }
MR auth status --json | python3 -I -c 'import json,sys;d=json.load(sys.stdin)["data"];assert d["method"]=="tailscale", d;print("method: tailscale: ok")'
MR remote status --json | J '"who signs in with Tailscale after choosing the method: %s" % sorted(p["login"] for p in d["data"]["people"])'
# Choosing the method rebuilds the list from accounts with a Tailscale login, so an account let in with
# --allow that hasn't signed in yet (no account, like Maya here) drops off it: let her in again.
AS_PERSON ./bin/metarush --home "$H" remote allow maya@example.com >/dev/null
MR remote status --json | python3 -I -c '
import json,sys
d=json.load(sys.stdin)["data"]
logins=sorted(p["login"] for p in d["people"])
assert logins==["maya@example.com","sam@example.com"], logins
print("who signs in with Tailscale: %s: ok" % logins)'

step "footage, a cut and a script to record"
P=$(MR project create "Tailnet story" --json | J 'd["data"]["project"]["id"]')
MR ingest tests/fixtures/gen/interview-a.mov --project "$P" --wait --json | J '"ingested=%d" % d["data"]["ingest"]["ingested"]'
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="interview-a.mov"][0]')
cat > "$H/cut.json" <<EOF
{"projectId":"$P","instruction":"first cut","rationale":"A short story.",
 "spec":{"schemaVersion":1,"title":"Tailnet story","output":{"preset":"landscape-1080p","fps":30,"platform":"youtube"},
  "clips":[{"id":"open","label":"Opening","assetId":"$A","in":0.4,"out":9.0},{"id":"close","label":"Close","assetId":"$A","in":18.0,"out":26.0}]}}
EOF
TOK=$(MR edit check --plan "$H/cut.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$H/cut.json" --confirm "$TOK" --json | J '"cut r%d" % d["data"]["revision"]["number"]'
printf 'Every morning starts in the kitchen.\n\nThen the garden wakes up.\n' > "$H/script.txt"
STOK=$(MR voiceover script import "$H/script.txt" --project "$P" --json | J 'd["data"]["check"]["confirmToken"]')
MR voiceover script import "$H/script.txt" --project "$P" --confirm "$STOK" --json >/dev/null
MR watch set "$H-watched" --create --json >/dev/null # outside MetaRush's own folder
# A spoken line for Chromium's fake microphone.
say -v Samantha -r 165 -o "$H/read.aiff" "Every morning starts in the kitchen." 2>/dev/null || true
if [ -f "$H/read.aiff" ]; then
  ffmpeg -hide_banner -loglevel error -y -f lavfi -i anullsrc=r=48000:cl=mono -i "$H/read.aiff" -filter_complex "[0]atrim=0:2.5[s];[1]aformat=sample_rates=48000:channel_layouts=mono[r];[s][r]concat=n=2:v=0:a=1,apad=pad_dur=4" "$H/fake-mic.wav"
else
  ffmpeg -hide_banner -loglevel error -y -f lavfi -i "sine=frequency=330:duration=8" -ar 48000 -ac 1 "$H/fake-mic.wav"
fi

step "a fake claude for a conversation two people share (D179)"
# Sam asks for a slower opening; Maya, continuing his conversation, asks to
# keep the kitchen shot: each message ends with a note Claude asks to leave (a card).
go build -o "$H/claude" ./internal/assistant/fakeclaude/cmd/fakeclaude
cat > "$H/claude.json" <<EOF
{"turns":[
  {"match":"a slower opening","steps":[
    {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush comment add \"A slower opening\" --project $P --json"}}},
    {"text":"I put your note on a card."}]},
  {"match":"keep the kitchen shot","steps":[
    {"tool":{"name":"Bash","denied":true,"input":{"command":"metarush comment add \"Keep the kitchen shot\" --project $P --json"}}},
    {"text":"Your note is on a card too."}]},
  {"match":"Done from the dashboard","steps":[{"text":"Noted."}]}
]}
EOF

step "MetaRush on 127.0.0.1:$PORT, and a second one on the link-local address of lo0"
METARUSH_CLAUDE="$H/claude" ./bin/metarush --home "$H" serve > "$H/daemon.log" 2>&1 & PIDS+=($!) # the binary itself, so cleanup's kill reaches it
wait_up "http://127.0.0.1:$PORT/api/health"
B_URL=""
if ifconfig lo0 2>/dev/null | grep -q 'fe80::1%lo0'; then
  mkdir -p "$HB"
  cp "$H/config.yaml" "$H/.env" "$HB/" && chmod 600 "$HB/.env"
  python3 -I - "$HB/config.yaml" "$BPORT" <<'PY'
import re, sys
p, port = sys.argv[1], sys.argv[2]
s = open(p).read()
s = re.sub(r'(\n\s+host:)\s*\S+', r'\1 "fe80::1%lo0"', s, count=1)
s = re.sub(r'(\n\s+port:)\s*\d+', r'\1 ' + port, s, count=1)
open(p, 'w').write(s)
PY
  ./bin/metarush --home "$HB" serve --no-worker --no-watch > "$HB/daemon.log" 2>&1 & PIDS+=($!)
  wait_up "http://[fe80::1%25lo0]:$BPORT/api/health" -H "Host: 127.0.0.1:$BPORT" # a Host it answers to
  B_URL="http://[fe80::1%25lo0]:$BPORT"
else
  echo "no fe80::1 on lo0 here: the non-loopback browser check is skipped (the Go test covers it where it can)"
fi

step "fake tailscale serve: https://$NAME:$SERVE"
SPOOF_ARGS=()
[ -n "$B_URL" ] && SPOOF_ARGS=(-spoof-listen "127.0.0.1:$SPOOF" -spoof-backend "$B_URL" -spoof-from "fe80::1%lo0" -spoof-host "$NAME:$SERVE")
"$H/fake-serve" -name "$NAME" -listen "127.0.0.1:$SERVE" -backend "http://127.0.0.1:$PORT" -spki-file "$H/spki.txt" \
  -user "maya=maya@example.com=Maya Lindqvist=100.101.102.104" -user "bob=bob@example.com=Bob Stranger=100.101.102.105" \
  ${SPOOF_ARGS[@]+"${SPOOF_ARGS[@]}"} > "$H/fake-serve.log" 2>&1 & PIDS+=($!)
for _ in $(seq 1 40); do [ -s "$H/spki.txt" ] && break; sleep 0.25; done

step "one-time links for another computer are retired (D183): they sign in only the owner, on this computer"
if MR dashboard --print-link --remote --no-open > "$H/remote-link.txt" 2>&1; then
  cat "$H/remote-link.txt"; echo "dashboard --remote still printed a link" >&2; exit 1
fi
grep -q "One-time sign-in links work only on this computer now." "$H/remote-link.txt" || { cat "$H/remote-link.txt"; echo "dashboard --remote failed without saying why" >&2; exit 1; }
echo "dashboard --remote refuses, and says why: ok"

step "the browser"
REMOTE_URL="https://$NAME:$SERVE" REMOTE_NAME="$NAME" REMOTE_SPKI="$(cat "$H/spki.txt")" REMOTE_SPOOF="$([ -n "$B_URL" ] && echo "https://$NAME:$SPOOF" || true)" \
  REMOTE_LOCAL="http://127.0.0.1:$PORT" REMOTE_PROJECT="$P" REMOTE_MINT="./bin/metarush --home $H dashboard --print-link --no-open" REMOTE_SHOTS="$SHOTS" \
  REMOTE_FAKE_MIC="$H/fake-mic.wav" node web/scripts/remote-e2e.mjs

step "the command line agrees (D179): a project's list is everyone's, --mine is yours; the thread names who said and decided what"
MR assistant conversations list --project "$P" --json | python3 -I -c '
import json,sys
d=json.load(sys.stdin)["data"]
assert d["allUsers"] and len(d["conversations"])==1 and d["conversations"][0]["mine"], d
print("--project: everyone'"'"'s conversations in it: ok")'
MR assistant conversations list --project "$P" --mine --json | python3 -I -c '
import json,sys
d=json.load(sys.stdin)["data"]
assert not d["allUsers"] and len(d["conversations"])==1, d
print("--mine: just yours: ok")'
if MR assistant conversations list --project "$P" --mine --all-users >/dev/null 2>&1; then echo "--mine with --all-users was taken" >&2; exit 1; fi
CONV=$(MR assistant conversations list --project "$P" --json | J 'd["data"]["conversations"][0]["id"]')
MR assistant conversations show "$CONV" > "$H/shared-thread.txt"
if grep -q "── Maya Lindqvist" "$H/shared-thread.txt" && grep -q "done by Maya Lindqvist" "$H/shared-thread.txt"; then
  echo "the printed thread names who said and decided what: ok"
else
  cat "$H/shared-thread.txt"; echo "the printed thread doesn't name Maya" >&2; exit 1
fi

step "the audit trail names the person"
MR audit --limit 500 --json | python3 -I -c '
import json,sys
ev=json.load(sys.stdin)["data"]["events"]
names={(e.get("action"), (e.get("actor") or {}).get("userName")) for e in ev}
want=[("remote.signin","Maya Lindqvist"),("remote.enable","Sam Lee"),("assistant.ask","Sam Lee"),("assistant.turn","Maya Lindqvist"),("assistant.action","Maya Lindqvist")]
missing=[w for w in want if w not in names]
print("audit:", "ok" if not missing else "missing %s" % missing)
sys.exit(1 if missing else 0)'

step "turn it off again (as a person, --apply stops the fake serve)"
AS_PERSON ./bin/metarush --home "$H" remote disable --apply
MR remote status --json | python3 -I -c 'import json,sys;d=json.load(sys.stdin)["data"];assert not d["on"] and not d["serve"]["active"], d;print("off: ok")'
code=$(curl -s -o /dev/null -w '%{http_code}' --resolve "$NAME:$SERVE:127.0.0.1" -k "https://$NAME:$SERVE/api/health" -H 'X-Fake-Tailnet-User: maya')
[ "$code" = "403" ] || { echo "after disable, the address answered $code" >&2; exit 1; }
echo "after disable the public address is turned away: ok"
echo
echo "remote access end to end: all good (screenshots in $SHOTS)"
exit 0
