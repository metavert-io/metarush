#!/usr/bin/env bash
# Tier A acceptance run: a fresh installation home, a fresh local database, real
# fixture footage → ingest → evidence → speakers (and the catch-up after an upgrade) → plan → check → apply → revise → render →
# cut sheets (round trip of every revision, an edit with quoted words) → editing verbs (trim,
# split, move, words out, a locked clip refused, a preview that saves nothing) → sound (voices made
# clearer and measured, music fitted to the video, quieter when someone talks, rendered and checked) →
# dashboard (sign-in, compare, rationale, download) → deliver → a timeline for DaVinci Resolve (OpenTimelineIO,
# footage beside it; METARUSH_OTIO_VENV also reads it with OpenTimelineIO) → an MCP client (tools, a read,
# people-only and read-only refusals, all logged) → resources (a logo, the brand kit, a nested cut) → a fixture
# repository, a code slide, a diagram and a local site's snapshot in a cut of slides (rendered, credited, refreshed)
# → the Library (archive, a collection a project uses, the two-step Trash) → a project motion graphic
# (check, refused until a person approves it, rendered, QA, withdrawn and refused) → a conversation from an online call
# (two recordings lined up by their sound, cut automatically, cleaned up, rendered, QA) → on a Mac, a face named once
# recognized in another recording, a crop that follows a walk, and the render checked for faces cut off and captions
# over faces or the footage's text → on a Mac, a call in speaker view in a workspace of its own (one question per
# person, the names it shows accepted in one click, nothing learned before, a wrong voice taken back from what a name
# was learned from) → generated video (a fake Gemini: priced first, made only with the agreed price,
# how it was made kept, deleted at the provider, marked AI-generated in the cut sheet, QA and the delivery; no one's likeness
# without consent) → a Remotion project from a folder (secrets and other tools' settings left out, its render as
# footage, compositions read and drawn with no network, clips only from an approved version, an edit checked with
# before/after stills and approved into the cut, a change that reaches the internet refused, approval withdrawn) →
# its map (scenes, words with where they are written, sounds, stills), its sound as the cut's tracks, its words as
# settings (the change approved from To-dos, a headline changed and checked), a scene lifted into a motion graphic with
# the shared theme and rendered, a theme change approved once moving the cut → a refresh from the project's own steps
# (planned first, started by a person, kept apart, its outputs a version a person approves) and its changes sent back
# as a new branch of its own remote →
# text fixes (a spelling list on a new transcript, a word retyped and two words joined keeping their times and the
# cut sheet's quotes, one new version and its review copy for every fix while the old version renders unchanged,
# captions files with the fixes) → a speaker-view call recording (switches to the frame, flashes at clip edges flagged
# and trimmed at the switch, the printed names covered by one name bar for the whole video that changes on the exact
# frame, QA) → a motion graphic's test frames in a widescreen cut's shape → "Keep this?" cards in the Claude panel (a
# fake claude: a link the person typed, a logo they attached, a rule and a sign-off; asked once; Not now; the site's
# logo and colours as the project's brand kit; the sign-off reminded at approval) → sources (a descriptive name at
# import, a rename that keeps the file name, a shaky clip steadied and a humming voice cleaned, measured before and
# after, the fixed copy used from then on and in a new version, the saved version unchanged, the original back on
# untick) → the activity log (the run in plain words, a bug report without secrets) → cleanup of the throwaway DB.
# Usage: scripts/acceptance.sh   (needs scripts/dev-mongo.sh running and tests/fixtures/gen)
# The throwaway database is dropped on exit, also after a failure, through
# store.DropTestDatabase (scripts/droptestdb refuses anything but local metarush-test-*).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
H="$(mktemp -d)/home"; DB="metarush-test-acc$(date +%s)"; PORT="${METARUSH_ACCEPTANCE_PORT:-4849}"
URI="$(scripts/dev-mongo.sh start)"
SRV=""; WEB=""; GEN=""; ELV=""; DBC=""
cleanup() {
  [ -n "$SRV" ] && kill "$SRV" 2>/dev/null || true
  [ -n "$WEB" ] && kill "$WEB" 2>/dev/null || true
  [ -n "$GEN" ] && kill "$GEN" 2>/dev/null || true
  [ -n "$ELV" ] && kill "$ELV" 2>/dev/null || true
  go run ./scripts/droptestdb "$URI" "$DB" || true
  [ -z "$DBC" ] || go run ./scripts/droptestdb "$URI" "$DBC" || true
}
trap cleanup EXIT
. scripts/lib/e2e.sh && e2e_run "$DB" "$(dirname "$H")"  # D194: stops what the run started, drops its database, removes its folder when it passes
MR() { ./bin/metarush --home "$H" "$@"; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
# CHECK runs Python statements on a command's JSON (d), with asserts that fail the run.
CHECK() { python3 -I -c "import json,sys;d=json.load(sys.stdin)
$1"; }
# Builds of the on-screen helper (macOS) stay in this run's folder, not the user's cache.
export METARUSH_VISION_CACHE="$(dirname "$H")/vision-cache"
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
scripts/make-fixtures.sh  # quick when the test videos are up to date; makes the missing ones (visual-b arrived later)
go build -o bin/metarush ./cmd/metarush

step "1. init: database and media store chosen separately; doctor"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Acceptance" --media filesystem --port $PORT >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
# A fake ElevenLabs (internal/testutil/fakeeleven) for the AI voice of the Recipes step: its address goes in the home's
# .env before the dashboard starts, so no voice in this run reaches ElevenLabs; it takes only the made-up key the
# Remotion refresh step puts in the .env later.
EF="${METARUSH_ACCEPTANCE_ELEVEN_PORT:-$((PORT + 4))}"
FAKEKEY="sk_$(printf 'f00d%.0s' 1 2 3 4 5 6 7 8 9 10)"
printf 'METARUSH_ELEVENLABS_BASE_URL=http://127.0.0.1:%s\n' "$EF" >> "$H/.env"
mkdir -p "$H/state"; printf '%s\n' "$FAKEKEY" > "$H/state/fake-eleven.key"; chmod 600 "$H/state/fake-eleven.key"
go build -o "$H/fakeeleven" ./internal/testutil/fakeeleven/cmd/fakeeleven
"$H/fakeeleven" -addr "127.0.0.1:$EF" -key-file "$H/state/fake-eleven.key" > "$H/fakeeleven.log" 2>&1 & ELV=$!
# A fake rclone (internal/testutil/fakerclone) for the Google Drive step (D220), set before anything runs — the CLI and
# the dashboard both use it — so this run never reaches a real rclone or a Google account: its "Google Drive" gdrive is
# a folder of this run.
FRC="$(dirname "$H")/fake-rclone"; DRIVE="$(dirname "$H")/drive"; mkdir -p "$FRC" "$DRIVE/Garden/Art"
go build -o "$FRC/rclone" ./internal/testutil/fakerclone/cmd/fake-rclone
printf '{"remotes":[{"name":"gdrive","type":"drive","root":"%s"}]}\n' "$DRIVE" > "$FRC/state.json"
export METARUSH_RCLONE="$FRC/rclone" METARUSH_FAKE_RCLONE_STATE="$FRC/state.json" METARUSH_FAKE_RCLONE_LOG="$FRC/calls.log"
MR doctor --json | J '"doctor ok=%s, failing=%s" % (d["data"]["ok"], [c["name"] for c in d["data"]["checks"] if c["status"]=="fail"])'

step "renderer: this version's own, set up in <home>/renderer/<version> (renders never use the checkout)"
MR renderer status --json | J '"before: %s (%s)" % (d["data"]["state"], d["data"]["id"])'
MR renderer install --json | J '"set up: %s, Remotion %s, browser %s" % (d["data"]["state"], d["data"]["remotion"], d["data"]["browser"])'
RID=$(MR renderer status --json | J 'd["data"]["id"]'); RDIR=$(MR renderer status --json | J 'd["data"]["videoDir"]')
case "$RDIR" in "$H/renderer/$RID/video") echo "renders run in <home>/renderer/$RID" ;; *) echo "unexpected renderer folder $RDIR"; exit 1 ;; esac

step "4. ingest two real files (+ a fake .mp4), originals unchanged"
P=$(MR project create "Acceptance film" --tone intimate --json | J 'd["data"]["project"]["id"]')
SUM_BEFORE=$(shasum -a 256 tests/fixtures/gen/interview-a.mov tests/fixtures/gen/broll-c.mp4)
MR ingest tests/fixtures/gen/interview-a.mov tests/fixtures/gen/broll-c.mp4 tests/fixtures/gen/not-a-video.mp4 --project "$P" --wait --json \
  | J '"ingested=%d skipped=%d failed=%d jobs=%s" % (d["data"]["ingest"]["ingested"], d["data"]["ingest"]["skipped"], d["data"]["ingest"]["failed"], sorted({j["state"] for j in d["data"]["jobs"]}))'
[ "$SUM_BEFORE" = "$(shasum -a 256 tests/fixtures/gen/interview-a.mov tests/fixtures/gen/broll-c.mp4)" ] && echo "originals unchanged (sha256)"
A=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="interview-a.mov"][0]')
B=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="broll-c.mp4"][0]')
MR footage moments "$P" --json | J '"pauses found: %s" % [(m["from"], m["to"]) for m in d["data"]["moments"] if m["kind"]=="pause"]'

step "link imports: a YouTube link through the same pipeline, its source kept; a playlist imports nothing (fake downloader, no network)"
FAKE="$(dirname "$H")/fake-yt-dlp"; mkdir -p "$FAKE"; go build -o "$FAKE/yt-dlp" ./internal/testutil/fakeytdlp/cmd/fake-yt-dlp
export METARUSH_YTDLP="$FAKE/yt-dlp"
MR import --check-tools --json | J '"downloader: %s (ready=%s)" % (d["data"]["summary"], d["data"]["ready"])'
MR import "https://www.youtube.com/watch?v=ccaccept001" --project "$P" --wait --json \
  | J '"imported %r (%s); its processing: %s" % (d["data"]["imports"][0]["result"]["title"], d["data"]["imports"][0]["result"]["status"], sorted({j["state"] for j in d["data"]["jobs"]}))'
MR asset list "$P" --json | J '"source kept: %s" % ["%s %s %s %s (%s)" % (a["source"]["platform"], a["source"]["handle"], a["source"]["uploadDate"], a["source"]["license"], a["source"]["tool"]) for a in d["data"]["assets"] if a.get("source")]'
MR import "https://www.youtube.com/watch?v=ccaccept001" --project "$P" --json | J '"the same link again: %s" % d["data"]["imports"][0]["status"]'
MR import "https://www.youtube.com/playlist?list=PLacceptance01" --json | J '"a playlist: %s (%s)" % (d["data"]["imports"][0]["status"], d["data"]["imports"][0]["code"])'
MR import "https://www.youtube.com/watch?v=drmaccept01" --wait --json | J '"copy-protected: %s" % d["data"]["imports"][0]["payload"]["advice"]["code"]' || true
unset METARUSH_YTDLP

step "speakers: voices found for every transcript; naming one is remembered; questions instead of guesses"
MR footage speakers list "$A" --json | J '"%s (status %s, %d voice(s), samples %s)" % (d["data"]["summary"], d["data"]["status"]["state"], len(d["data"]["speakers"]), [len(v["samples"]) for v in d["data"]["speakers"]])'
MR todos list --asset "$A" --json | J '"open questions for interview-a: %s" % [t["question"] for t in d["data"]["todos"]]'
MR footage speakers rename "$A" "Speaker 1" "Samantha" --json | J '"named: %s" % d["data"]["summary"]'
MR footage transcript "$A" --speaker Samantha --json | J '"Samantha says %d segment(s), first: %r" % (len(d["data"]["segments"]), d["data"]["segments"][0]["text"][:40] if d["data"]["segments"] else "")'
MR people list --json | J '"people: %s" % [(p["name"], p["recordings"]) for p in d["data"]["people"]]'

step "catch-up: a clip from before speaker detection gets voices and questions after the upgrade"
# The clip is imported the way an older version did: transcribed, no speaker detection (no diarization models).
MODELS="${METARUSH_MODELS_DIR:-$HOME/.local/share/metarush-models}"; OLDMODELS="$(mktemp -d)"
for m in "$MODELS"/*; do [ "$(basename "$m")" = diarization ] || ln -s "$m" "$OLDMODELS/"; done
METARUSH_MODELS_DIR="$OLDMODELS" MR ingest tests/fixtures/gen/interview-b.mp4 --project "$P" --wait --json >/dev/null
OLDCLIP=$(MR asset list "$P" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="interview-b.mp4"][0]')
MR footage speakers list "$OLDCLIP" --json | J '"before: speaker detection %s, %d voice(s)" % (d["data"]["status"]["state"], len(d["data"]["speakers"]))'
MR migrate --json | J '"migrate, then the catch-up: %s" % ["%s: %s" % (o["name"], o.get("summary") or ("done" if o["ran"] else "nothing to do")) for o in d["data"]["catchUp"]]'
MR footage speakers detect "$OLDCLIP" --wait --json | J '"after: %s" % [s["summary"] for s in d["data"]["speakers"]]'
MR todos list --asset "$OLDCLIP" --json | J '"questions about it now: %s" % [t["question"] for t in d["data"]["todos"]]'
MR migrate --json | J '"migrate again: %d step(s) ran (each runs once per version)" % len([o for o in d["data"]["catchUp"] if o["ran"]])'

step "what's on screen: cuts, a pan and someone walking in found; a smart contact sheet; on a Mac, text on screen read and searchable"
# visual-a: hard cuts at 6, 14, 21 and 25 s, a slow pan 6–14 s, a person walking in at 16–17.5 s, a sign reading EXIT
# at 21–25 s (tests/fixtures/truth/visual-a.json). Its own project, so the steps below see the same footage as before.
# The face model is downloaded the first time faces are fingerprinted (checked against its SHA-256, D114); a copy at
# METARUSH_FACE_MODEL is used instead, so the run works offline.
if [ -n "${METARUSH_FACE_MODEL:-}" ]; then mkdir -p "$H/components/faces" && cp "$METARUSH_FACE_MODEL" "$H/components/faces/"; fi
PV=$(MR project create "Acceptance visuals" --json | J 'd["data"]["project"]["id"]')
MR ingest tests/fixtures/gen/visual-a.mp4 --project "$PV" --wait --json | J '"visual-a processed: %s" % sorted({"%s %s" % (j["type"], j["state"]) for j in d["data"]["jobs"]})'
VA=$(MR asset list "$PV" --json | J 'd["data"]["assets"][0]["id"]')
MR footage moments "$PV" --json | CHECK '
ms = [m for m in d["data"]["moments"] if m["kind"] == "visual"]
cuts = sorted(round(m["from"], 2) for m in ms if m["change"] == "cut")
slow = [(m["from"], m["to"]) for m in ms if m["change"] == "gradual"]
moving = [(m["from"], m["to"]) for m in ms if m["change"] == "motion"]
print("cuts at %s; slow change %s; movement %s" % (cuts, slow, moving))
assert cuts == [6.0, 14.0, 21.0, 25.0], "cuts"
assert any(f <= 6.5 and t >= 12 for f, t in slow), "the pan"
assert any(15 <= f <= 17.5 for f, t in moving), "someone walking in"
assert all(m.get("frames") for m in ms), "frames to look at"'
MR footage sheet "$VA" --json | CHECK '
import os
s = d["data"]
shots = sorted({t["shot"] for t in s["tiles"]})
print("smart sheet: %d frames over shots %s (%s)" % (len(s["tiles"]), shots, os.path.basename(s["path"])))
assert s["mode"] == "smart" and os.path.getsize(s["path"]) > 10000, "a smart sheet image"
assert shots == [1, 2, 3, 4, 5], "a frame of every shot"
print("first frames: %s" % [t["caption"] for t in s["tiles"][:2]])
assert any(t["shot"] == 1 and "“" in t["caption"] for t in s["tiles"]), "the words said over the first shot"'
if MR footage visuals status --json | J 'd["data"]["here"]["supported"]' | grep -q True; then
  MR footage visuals "$VA" --json | CHECK '
v = d["data"]
print("\n".join(l["line"] for l in v["lines"]))
assert any(t["text"] == "EXIT" and 20.5 <= t["from"] <= 21.5 for t in v["text"]), "the sign"
assert any(l["shot"] == "close-up" and l["from"] == 0 for l in v["lines"]), "the close-up"
assert any("dog" in l["things"] for l in v["lines"] if l["from"] >= 25), "the dog"'
  MR search "exit sign" --json | CHECK '
hits = [h for h in d["data"]["visuals"] if h["matchedIn"] == "text"]
print("search \"exit sign\": %s" % ["%s %s %r" % (h["assetTitle"], h["timecode"], h["text"]) for h in hits])
assert hits and hits[0]["text"] == "EXIT", "on-screen text is searchable"'
  MR doctor --json | J '"doctor on the helper: %s" % [c["detail"] for c in d["data"]["checks"] if c["name"] == "helper"]'
else
  echo "on-screen index: not on this machine (Apple Vision needs a Mac); the step above is the part every machine does"
fi

step "faces across clips, framing that follows movement, what's on screen in the render (D114–D116; needs a Mac)"
# visual-b (tests/fixtures/truth/visual-b.json): visual-a's talking head walks across the room (0–8 s) and sits low in
# the picture (14–20 s), the person who walks into visual-a stands at the far right edge above a sign (8–14 s), and
# someone else closes it (20–24 s). Other photos of the same two people, so recognizing them is not a copy match.
if MR footage visuals status --json | J 'd["data"]["here"]["supported"]' | grep -q True; then
  MR ingest tests/fixtures/gen/visual-b.mp4 --project "$PV" --wait --json | J '"visual-b processed: %s" % sorted({"%s %s" % (j["type"], j["state"]) for j in d["data"]["jobs"]})'
  VB=$(MR asset list "$PV" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="visual-b.mp4"][0]')
  MR doctor --json | J '"doctor on face recognition: %s" % [c["detail"] for c in d["data"]["checks"] if c["name"] == "face recognition"]'
  MR footage faces "$VB" --json | CHECK '
fs = d["data"]["faces"]
print("before anyone is named: %s" % d["data"]["summary"])
assert not any(f.get("recognized") for f in fs), "nothing is named by its looks before a person names someone"'
  FA=$(MR footage faces "$VA" --json | J '[f["id"] for f in d["data"]["faces"] if f["from"] < 1][0]')
  MR footage faces name "$FA" "Samantha" --json | J '"a person names the talking head of visual-a (her voice is Samantha'"'"'s): %s" % d["data"]["done"]'
  MR footage faces "$VB" --json | CHECK '
fs = d["data"]["faces"]
for f in fs:
    print("  %5.1f–%5.1f  %s" % (f["from"], f["to"], (f.get("recognized") or {}).get("name") and "looks like %s (%d%%)" % (f["recognized"]["name"], round(f["recognized"]["score"] * 100)) or (f.get("maybe") or {}).get("name") and "asked: %s" % f["maybe"]["name"] or "no name"))
rec = [f for f in fs if f.get("recognized")]
assert any(f["from"] < 1 and f["recognized"]["name"] == "Samantha" for f in rec), "her walk is recognized"
assert any(13.5 <= f["from"] < 15 and f["recognized"]["name"] == "Samantha" for f in rec), "and her face low in the picture"
assert not any(f["from"] >= 19.5 and (f.get("recognized") or (f.get("maybe") or {}).get("by") in ("looks", "both")) for f in fs), "the stranger does not look like her (her voice heard over it may still be asked about)"
assert not any(7.5 <= f["from"] < 13.5 and f.get("recognized") for f in fs), "the other person is not her"'
  MR footage visuals "$VB" --json | J '"the readout: %s" % [l["line"] for l in d["data"]["lines"]][:2]'
  MR people list --json | J '"people: %s" % [(p["name"]) for p in d["data"]["people"]]'
  FB=$(MR footage faces "$VB" --json | J '[f["id"] for f in d["data"]["faces"] if f.get("recognized") and f["from"] >= 13.5][0]')
  MR footage faces reject "$FB" --json | J '"\"Not Samantha?\" on one of them: %s" % d["data"]["done"]'
  MR footage faces "$VB" --json | CHECK '
fs = d["data"]["faces"]
low = [f for f in fs if 13.5 <= f["from"] < 15][0]
assert not low.get("recognized") and (low.get("maybe") or {}).get("name") != "Samantha", "never Samantha again for that face"
assert any(f["from"] < 1 and f.get("recognized") for f in fs), "the others stay"
print("taken back; the walk stays recognized")'

  # A vertical cut of visual-b with captions, the crop held in the middle: QA sees the face cut off at the edge,
  # the captions over the sign and over her face low in the picture, each with a fix.
  cat > "$H/vb-plan.json" <<EOF
{"projectId":"$PV","instruction":"a vertical cut of the walk with captions",
 "spec":{"schemaVersion":1,"title":"Walk","output":{"preset":"vertical-1080p","fps":30,"platform":"shorts","framing":"center"},
  "clips":[{"id":"walk","assetId":"$VB","in":0,"out":7.8},{"id":"edge","assetId":"$VB","in":8.2,"out":13.8},{"id":"low","assetId":"$VB","in":14.2,"out":19.8}],
  "captions":{"enabled":true,"style":"clean","position":"bottom","maxWordsPerLine":5}}}
EOF
  TOK=$(MR edit check --plan "$H/vb-plan.json" --json | J 'd["data"]["confirmToken"]')
  RV1=$(MR edit apply --plan "$H/vb-plan.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]'); echo "walk r1 $RV1 (crops keep the middle)"
  MR revision plan "$RV1" --json | J '"r1 framing: %s" % [(c["id"], c["framing"].get("from", "middle"), len(c["framing"].get("path") or [])) for c in d["data"]["clips"]]'
  RENDV=$(MR render submit --revision "$RV1" --quality review --wait --json | J 'd["data"]["render"]["id"]')
  MR qa run "$RENDV" --wait --json > "$H/vb-qa.json"
  CHECK '
on = [f for f in d["data"]["report"]["findings"] if f["kind"] in ("face_cut_off", "faces_covered", "graphic_covers_face", "text_under_captions")]
for f in on:
    print("  %s %s (%s): %s → %s" % (f["timecode"], f["kind"], f["basis"], f["message"], (f.get("fix") or {}).get("summary")))
kinds = {f["kind"] for f in on}
assert "face_cut_off" in kinds, "the face at the edge is cut off by the middle crop"
assert "text_under_captions" in kinds or "faces_covered" in kinds, "captions over the sign or her face"
assert all(f.get("fix") for f in on), "every one has a fix"
check = [c for c in d["data"]["report"]["checks"] if c["name"] == "on_screen"][0]
print("check: %s (%s) %s" % (check["status"], check["basis"], check["detail"]))' < "$H/vb-qa.json"
  # The person chooses to let the crops follow movement (the edit op the fix suggests, as a command): a new version.
  MR cut framing "$PV" follow --dry-run --json | J '"cut framing follow (checked): %s" % d["data"]["summary"]'
  TOK=$(MR cut framing "$PV" follow --dry-run --json | J 'd["data"]["check"]["confirmToken"]')
  RV2=$(MR cut framing "$PV" follow --confirm "$TOK" --json | J 'd["data"]["applied"]["revision"]["id"]'); echo "walk r2 $RV2 (crops follow)"
  MR revision plan "$RV1" --json | J '"r1 unchanged: %s" % [(c["id"], c["framing"].get("from", "middle")) for c in d["data"]["clips"]]'
  MR revision plan "$RV2" --json | CHECK '
cs = d["data"]["clips"]
print("r2 framing: %s" % [(c["id"], c["framing"].get("from"), len(c["framing"].get("path") or [])) for c in cs])
walk = cs[0]["framing"]
assert walk.get("from") == "follow" and len(walk["path"]) > 3, "the walk is followed"
xs = [k["x"] for k in walk["path"]]
assert xs[-1] > xs[0] + 0.2, "left to right"'
  MR cut show "$PV" --json | J '"the cut sheet says: %s" % [l.strip() for l in d["data"]["sheet"].splitlines() if l.strip().startswith("framing:")]'
  RENDV2=$(MR render submit --revision "$RV2" --quality review --wait --json | J 'd["data"]["render"]["id"]')
  MR qa run "$RENDV2" --wait --json | CHECK '
on = [f for f in d["data"]["report"]["findings"] if f["kind"] in ("face_cut_off", "faces_covered", "graphic_covers_face", "text_under_captions")]
print("following movement, the render shows: %s" % (["%s %s" % (f["timecode"], f["kind"]) for f in on] or "no face cut off"))
assert not any(f["kind"] == "face_cut_off" for f in on), "no face is cut off once the crop follows"'
else
  echo "faces, following and the on-screen checks: not on this machine (Apple Vision needs a Mac)"
fi

step "who is on screen: one question per person, the names a call shows in one click, nothing learned before; a wrong voice fixed from what it was learned from (D155–D158; needs a Mac)"
# The call fixture (tests/fixtures/truth/call.json): three people in speaker view, the picture switching at every turn
# (so every appearance is a new face on screen), each name printed under the picture, a stretch in gallery view. In a
# workspace of its own (another throwaway database), like a team that just recorded a call: nobody named yet, and the
# people it adds don't change what the steps below see.
if MR footage visuals status --json | J 'd["data"]["here"]["supported"]' | grep -q True; then
  scripts/make-call-fixtures.sh >/dev/null
  HC="$(dirname "$H")/home-call"; DBC="${DB}call"
  MC() { ./bin/metarush --home "$HC" "$@"; }
  METARUSH_MONGODB_URI="$URI" MC init --yes --test-db --secrets env --db-name "$DBC" --workspace-name "Garden club" --media filesystem --port $((PORT + 1)) >/dev/null
  echo "METARUSH_MONGODB_URI=$URI" > "$HC/.env"; chmod 600 "$HC/.env"
  if [ -d "$H/components/faces" ]; then mkdir -p "$HC/components" && cp -R "$H/components/faces" "$HC/components/"; fi
  PC=$(MC project create "Garden club" --json | J 'd["data"]["project"]["id"]')
  MC ingest tests/fixtures/gen/call/garden-call.mp4 --project "$PC" --wait --json | J '"the call processed: %s" % sorted({"%s %s" % (j["type"], j["state"]) for j in d["data"]["jobs"]})'
  CALL=$(MC asset list "$PC" --json | J 'd["data"]["assets"][0]["id"]')
  cat > "$HC/cut.json" <<EOF
{"projectId":"$PC","instruction":"the opening","spec":{"schemaVersion":1,"title":"Garden club","output":{"preset":"landscape-1080p","fps":30},
  "clips":[{"id":"open","assetId":"$CALL","in":0,"out":20}]}}
EOF
  TOK=$(MC edit check --plan "$HC/cut.json" --json | J 'd["data"]["confirmToken"]')
  MC edit apply --plan "$HC/cut.json" --confirm "$TOK" --json | J '"a cut of the first 20 seconds: version %d" % d["data"]["revision"]["number"]'
  MC todos groups --project "$PC" --json | CHECK '
truth = json.load(open("tests/fixtures/truth/call.json"))
gs = d["data"]["groups"]
seen = sum(g["context"]["appearances"] for g in gs)
print("%d appearances of 3 people on screen, %d questions (earlier versions asked once per appearance):" % (seen, len(gs)))
for g in gs:
    c = g["context"]
    print("  %s  seen %d times%s · %s" % (g["question"], c["appearances"], " · in your cut" if c.get("inCut") else "", " · ".join(e["text"] for e in c.get("evidence", []))))
assert len(gs) == 3, "one question per person"
assert sorted(g["context"].get("shown") for g in gs) == ["Ada Park", "Ben Okafor", "Cleo Marsh"], "the names the call shows are the suggestions"
assert seen >= truth["speaker_view_appearances"], "every appearance is in one of the questions"
assert [g["context"].get("inCut", False) for g in gs] == sorted([g["context"].get("inCut", False) for g in gs], reverse=True), "the people in the cut first"
assert {g["context"]["shown"] for g in gs if g["context"].get("inCut")} == {"Ada Park", "Ben Okafor"}, "the cut shows Ada and Ben"
assert len(d["data"]["shown"]) == 1 and len(d["data"]["shown"][0]["names"]) == 3, "one click for the clip"
print(d["data"]["shown"][0]["summary"])'
  MC people list --json | CHECK '
assert not d["data"]["people"], "nobody is added from what the video shows before a person accepts"
print("before accepting: nobody added")'
  MC footage faces "$CALL" --json | CHECK '
assert not [f for f in d["data"]["faces"] if f.get("name") or f.get("recognized")], "no face named or recognized"
print("before accepting: no face named")'
  MC todos accept-shown "$CALL" --json | J '"one click: %s" % d["data"]["summary"]'
  MC footage faces "$CALL" --json | CHECK '
truth = json.load(open("tests/fixtures/truth/call.json"))
def views(t):  # what the picture shows at t, a frame either side
    return {v["view"] for v in truth["views"] if v["from_s"] - 0.05 <= t < v["to_s"] + 0.05}
fs = d["data"]["faces"]
named = [f for f in fs if f.get("name")]
wrong = [(f["at"], f["name"], sorted(views(f["at"]))) for f in named if f["name"] not in views(f["at"]) and "gallery" not in views(f["at"])]
print("named %d of %d faces: %s" % (len(named), len(fs), sorted({f["name"] for f in named})))
assert len(named) == len(fs), "the faces of everyone in the call are named"
assert not wrong, "no wrong name: %s" % wrong'
  MC footage speakers "$CALL" --json | CHECK '
assert not [s for s in d["data"]["speakers"] if s.get("personId")], "faces never name a voice"
print("voices still unnamed (a face never names a voice): %s" % [s["label"] for s in d["data"]["speakers"]])'
  # A wrong voice learned first: someone else's voice (interview-b) named Ada Park by mistake.
  MC ingest tests/fixtures/gen/interview-b.mp4 --wait --json >/dev/null
  OTHER=$(MC asset list --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="interview-b.mp4"][0]')
  MC footage speakers rename "$OTHER" "Speaker 1" "Ada Park" --json | J '"by mistake: %s" % d["data"]["summary"]'
  ONSCREEN="Ada Park (on screen while this voice talks)"
  ADAQ=$(MC todos list --asset "$CALL" --json | J '[t["id"] for t in d["data"]["todos"] if any(s["label"] == "'"$ONSCREEN"'" for s in t["suggestions"])][0]')
  ADAV=$(MC todos show "$ADAQ" --json | J 'd["data"]["todo"]["context"]["label"]')
  adan() { MC todos show "$ADAQ" --json | J '[i + 1 for i, s in enumerate(d["data"]["todo"]["suggestions"]) if s["label"] == "'"$ONSCREEN"'"][0]'; }
  MC todos answer "$ADAQ" --suggestion "$(adan)" --json > "$HC/ada-answer.json" || true
  CHECK '
e = d["error"]
print("her voice named Ada Park: %s" % e["message"])
assert e["code"] == "E_CONFIRM_REQUIRED" and e["details"]["reason"] == "voices_differ" and e["details"]["speaker"], "the warning, with what it compares"' < "$HC/ada-answer.json"
  MC people learned "Ada Park" --asset "$CALL" --voice "$ADAV" --json | CHECK '
v = d["data"]
print(v["summary"])
assert len(v["voices"]) == 1 and v["voices"][0]["odd"] and v["still"], "the wrong voice her name was learned from, marked"'
  MC people unlearn "Ada Park" --asset "$OTHER" --voice "Speaker 1" --as "Rex Hale" --new-person --against-asset "$CALL" --against-voice "$ADAV" --json | CHECK '
v = d["data"]
print(v["done"]); print(v["summary"])
assert not v["still"] and not v["voices"], "taken back: the warning is gone"'
  MC todos answer "$ADAQ" --suggestion "$(adan)" --json | J '"now: %s" % d["data"]["done"]'
  MC people learned "Ada Park" --json | CHECK '
v = d["data"]
print("Ada Park is learned from %d voice(s) and %s" % (len(v["voices"]), ["%d faces in %s" % (f["faces"], f["assetTitle"]) for f in v["faces"]]))
assert len(v["voices"]) == 1 and v["voices"][0]["assetTitle"] == "garden-call", "her own voice only"'
  go run ./scripts/droptestdb "$URI" "$DBC" >/dev/null && DBC="" && rm -rf "$HC"
else
  echo "who is on screen: not on this machine (Apple Vision needs a Mac)"
fi

step "7. grounded plan → check → apply (immutable revision) → render with a Remotion overlay"
cat > "$H/plan.json" <<EOF
{"projectId":"$P","instruction":"Open on the neighbor's question with the pause before it; vertical; captions.",
 "rationale":"The question is the hook; the pause before it is the tension.",
 "spec":{"schemaVersion":1,"title":"Acceptance cut","output":{"preset":"vertical-1080p","fps":30,"platform":"shorts"},
  "clips":[{"id":"hook","assetId":"$A","in":11.6,"out":18.4,"framing":{"mode":"fill","focusX":0.5},"rationale":"setup, pause, question",
            "evidence":[{"kind":"transcript","assetId":"$A","from":16.1,"to":18.3,"quote":"What would you build if nobody was watching?"}]},
           {"id":"broll","assetId":"$B","in":0,"out":2.5,"muted":true,"transitionIn":{"type":"crossfade","duration":0.4}},
           {"id":"payoff","assetId":"$A","in":20.8,"out":26.2,"locked":true}],
  "captions":{"enabled":true,"style":"emphasis","position":"bottom","maxWordsPerLine":4},
  "overlays":[{"id":"who","template":"lower-third.clean","at":0.5,"duration":3,"params":{"name":"A builder","title":"Teacher"}}]}}
EOF
TOK=$(MR edit check --plan "$H/plan.json" --json | J 'd["data"]["confirmToken"]')
R1=$(MR edit apply --plan "$H/plan.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]'); echo "r1 $R1"
GOALQ=$(MR todos list --project "$P" --kind brief_missing --json | J '[t["id"] for t in d["data"]["todos"] if t["context"]["field"]=="goal"][0]')
MR todos answer "$GOALQ" "A short story for the neighbors" --json | J '"brief question answered: %s" % d["data"]["done"]'
MR todos count --json | J '"open questions left: %d (by kind %s)" % (d["data"]["total"], d["data"]["byKind"])'
printf '{"baseRevisionId":"%s","instruction":"tighten the b-roll","ops":[{"op":"updateClip","id":"broll","set":{"out":1.8}}]}' "$R1" > "$H/ops.json"
TOK=$(MR edit check --plan "$H/ops.json" --json | J 'd["data"]["confirmToken"]')
R2=$(MR edit apply --plan "$H/ops.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]'); echo "r2 $R2"
REND=$(MR render submit --revision "$R2" --quality review --wait --json | J 'd["data"]["render"]["id"]')
MR render status "$REND" --json | J '"render %s: %s %sx%s %.2fs sha256=%s…" % (d["data"]["render"]["id"], d["data"]["render"]["state"], d["data"]["render"]["output"]["width"], d["data"]["render"]["output"]["height"], d["data"]["render"]["output"]["duration"], d["data"]["render"]["output"]["sha256"][:12])'
MR render status "$REND" --json | J '"drawn by renderer %s (this version: %s)" % (d["data"]["render"]["rendererId"], d["data"]["render"]["rendererId"] == "'"$RID"'")'

step "QA: decode the actual render (black/frozen vs. planned dips, loudness, silence vs. speech, layout) + frames"
MR qa run "$REND" --wait --json | J '"qa %s: %s — %s; contact sheet %s" % (d["data"]["report"]["id"], d["data"]["report"]["verdict"], ["%s/%s@%s" % (f["kind"], f["severity"], f["timecode"]) for f in d["data"]["report"]["findings"]], d["data"]["report"]["contactSheet"]["path"])'

step "split edits: the question's voice carries over the b-roll (L-cut); a locked ending refuses a transition"
cat > "$H/split.json" <<EOF
{"baseRevisionId":"$R2","instruction":"let the question carry over the b-roll","ops":[{"op":"setTransition","id":"broll","transition":{"type":"cut","audio":{"mode":"carry","duration":0.4}}}]}
EOF
MR edit check --plan "$H/split.json" --json | J '"check: valid=%s, warnings=%s" % (d["data"]["valid"], [w["message"] for w in d["data"]["warnings"]])'
TOK=$(MR edit check --plan "$H/split.json" --json | J 'd["data"]["confirmToken"]')
R3=$(MR edit apply --plan "$H/split.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]'); echo "r3 $R3"
MR revision plan "$R3" --json | J '"the hook picture ends at frame %d, its sound at %d (%s, blend %d frames); the b-roll sound: %s" % (d["data"]["clips"][0]["from"]+d["data"]["clips"][0]["durationInFrames"], d["data"]["clips"][0]["audio"]["from"]+d["data"]["clips"][0]["audio"]["durationInFrames"], d["data"]["clips"][1]["transitionIn"]["audio"]["result"], d["data"]["clips"][1]["transitionIn"]["audio"]["blendFrames"], "muted" if d["data"]["clips"][1]["muted"] else "plays")'
printf '{"baseRevisionId":"%s","ops":[{"op":"setTransition","id":"payoff","transition":{"type":"crossfade","duration":0.5}}]}' "$R3" > "$H/locked.json"
MR edit check --plan "$H/locked.json" --json | J '"a dissolve into the locked ending → %s" % d["error"]["code"]' || true
MR edit transitions --json | J '"%d transitions: %s; styles: %s" % (len(d["data"]["transitions"]), ", ".join(t["name"] for t in d["data"]["transitions"]), ", ".join(x["name"] for x in d["data"]["styles"]))'
MR cut show "$P" --json | J '"the cut sheet says: %s" % [l for l in d["data"]["sheet"].splitlines() if l.startswith("↳")]'
MR cut transition "$P" broll keep --auto --dry-run --json | J '"one cut by command: %s (valid=%s)" % (d["data"]["summary"], d["data"]["check"]["valid"])'

step "10. stale full-spec plan is rejected (no silent last-write-wins)"
python3 -I -c "import json;p=json.load(open('$H/plan.json'));p['baseRevisionId']='$R1';del p['projectId'];json.dump(p,open('$H/stale.json','w'))"
MR edit check --plan "$H/stale.json" --json | J '"stale plan → %s" % d["error"]["code"]' || true

step "cut sheets: every revision reads back unchanged; an edit with quoted words saves a new revision"
for R in "$R1" "$R2"; do
  MR cut show "$P" --revision "$R" --out "$H/r.cut.md" >/dev/null
  MR cut check "$H/r.cut.md" --json | J '"r%d sheet reads back as the same cut: %s" % (d["data"]["baseNumber"], d["data"]["valid"] and d["data"]["noChanges"])'
done
MR cut show "$P" --out "$H/edit.cut.md" >/dev/null
python3 -I -c "import sys;L=open(sys.argv[1]).read().split(chr(10));f=[l.strip() for l in L if 'interview-a.mov' in l and '#' in l][0];i=[k for k,l in enumerate(L) if l.startswith('1. hook')][0];print('sheet names who speaks:', f.split('#')[1].strip(), '|', L[i+1].strip())" "$H/edit.cut.md"
python3 -I -c "import re,sys;p=sys.argv[1];s=open(p).read();t=re.sub(r'^(1\. hook: \S+ \S+) → \S+', r'\1 → \"nobody was watching\"', s, count=1, flags=re.M);assert t!=s;open(p,'w').write(t)" "$H/edit.cut.md"
MR cut check "$H/edit.cut.md" -m "end the hook on the question" --json > "$H/edit.check.json"
J '"quoted words: %s; changes: %s" % ([(a["phrase"], a["time"]) for a in d["data"]["anchors"]], d["data"]["changes"])' < "$H/edit.check.json"
TOK=$(J 'd["data"]["confirmToken"]' < "$H/edit.check.json")
MR cut apply "$H/edit.cut.md" --confirm "$TOK" -m "end the hook on the question" --json | J '"r%d saved from the sheet (rebased=%s)" % (d["data"]["revision"]["number"], d["data"]["rebased"])'

step "editing verbs: a small change by command, shown first, saved with its token; words out by quoting them"
MR cut trim "$P" broll --end -0.3 --json > "$H/trim.json"
J '"trim, shown first: %s (valid=%s)" % (d["data"]["summary"], d["data"]["check"]["valid"])' < "$H/trim.json"
TOK=$(J 'd["data"]["check"]["confirmToken"]' < "$H/trim.json")
MR cut trim "$P" broll --end -0.3 --confirm "$TOK" --json | J '"saved r%d: %s" % (d["data"]["applied"]["revision"]["number"], d["data"]["summary"])'
MR cut split "$P" hook '"What would you build"' --dry-run --json | J '"split, not saved: %s" % d["data"]["summary"]'
MR cut move "$P" broll start --dry-run --json | J '"move, not saved: %s" % d["data"]["summary"]'
MR cut remove-words "$P" "one simple" --json > "$H/words.json"
J '"remove words: %s" % d["data"]["summary"]' < "$H/words.json"
TOK=$(J 'd["data"]["check"]["confirmToken"]' < "$H/words.json")
MR cut remove-words "$P" "one simple" --confirm "$TOK" --json | J '"saved r%d: the cut went from %.2f s to %.2f s" % (d["data"]["applied"]["revision"]["number"], d["data"]["durationBefore"], d["data"]["duration"])'
MR cut remove-words "$P" --ums --json | J '"ums and restarts: %s" % ([r["words"] for r in d["data"].get("removals", [])] or d["data"]["summary"])'
MR cut trim "$P" payoff --end -0.5 --json | J '"trimming the locked ending → %s" % d["error"]["code"]' || true
printf '{"baseRevisionId":"%s","ops":[{"op":"splitClip","id":"hook","at":13}]}' "$(MR project show "$P" --json | J 'd["data"]["headRevisionId"]')" > "$H/preview.json"
MR edit preview --plan "$H/preview.json" --json | J '"edit preview, nothing saved: valid=%s, %s" % (d["data"]["valid"], d["data"]["summary"])'

step "sound: voices made clearer (clean) and measured; music fitted to the video, ending on a phrase (or a bar line or a beat); quieter when someone talks; rendered and checked"
# The clearer-voice components: a home that has them (never downloaded per run), else clean sets itself up on first use.
SOUNDH="${METARUSH_TEST_SOUND_HOME:-$ROOT/.agent-home/sound}"
if [ -d "$SOUNDH/components/voice-cleaner" ]; then ln -s "$SOUNDH/components" "$H/components"; echo "clearer-voice components from $SOUNDH"; fi
MR sound enhance everyone --project "$P" --mode clean --estimate --json | J '"estimate: %s" % d["data"]["summary"]'
MR sound enhance everyone --project "$P" --mode clean --apply --json \
  | J '"clearer: %s (%d copy, applied as version %s)" % (d["data"]["result"]["summary"].split(" It is in")[0], len(d["data"]["result"]["copies"]), d["data"]["result"]["applied"]["number"])'
MR sound enhance everyone --project "$P" --mode clean --json | J '"the same again: made before, %s" % d["data"]["result"]["copies"][0]["reused"]'
MR sound music add tests/fixtures/gen/music-bed.m4a --project "$P" --apply --json \
  | J '"music: %s (about %.0f beats a minute)" % (d["data"]["result"]["placement"], d["data"]["result"]["tempo"])'
MR sound music set music --project "$P" --level -16 --duck on --duck-depth 14 --apply --json | J '"music level: %s" % d["data"]["result"]["summary"].split(" It is in")[0]'
HEADS=$(MR project show "$P" --json | J 'd["data"]["headRevisionId"]')
MR revision plan "$HEADS" --json | J '"in the plan: the clip sound plays its clearer copy %s; the music ends on %s" % ([c["id"] for c in d["data"]["clips"] if (c.get("audio") or {}).get("variant")], [a.get("endsOn") or ("a beat" if a.get("endsOnBeat") else "the end of the video") for a in d["data"]["audio"] if a["role"]=="music"])'
MR sound show --project "$P" --json | J '"sound now: %s" % d["data"]["summary"]'
RENDS=$(MR render submit --revision "$HEADS" --quality review --wait --json | J 'd["data"]["render"]["id"]')
MR qa run "$RENDS" --wait --json | J '"qa %s, about the voices: %s" % (d["data"]["report"]["verdict"], [f["message"] for f in d["data"]["report"]["findings"] if f["kind"]=="clearer_voice"])'

step "8. dashboard: sign in, open revision, compare, rationale + source IDs, download"
# The dashboard's Claude Code panel talks to a fake claude (its script is written by the Keep this? step), and may
# take snapshots of the fixture site on this computer (tests only).
FAKEC="$(dirname "$H")/fake-claude/claude"; mkdir -p "$(dirname "$FAKEC")"; go build -o "$FAKEC" ./internal/assistant/fakeclaude/cmd/fakeclaude
echo '{"turns":[]}' > "$FAKEC.json"
METARUSH_CLAUDE="$FAKEC" METARUSH_PAGES_ALLOW_LOCAL=1 METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve --no-worker > "$H/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 40); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done
LINK=$(MR dashboard --print-link --no-open --json | J 'd["data"]["url"]'); CJ="$H/cookies.txt"
curl -sf -c "$CJ" -o /dev/null "$LINK" && echo "signed in"
api() { curl -sf -b "$CJ" -H 'X-MetaRush-Client: dashboard' -H 'Content-Type: application/json' -X POST "http://127.0.0.1:$PORT/api/ops/$1" -d "$2"; }
api revision.show "{\"id\":\"$R2\"}" | J '"revision r%d rationale=%r clip sources=%s" % (d["data"]["summary"]["number"], d["data"]["spec"]["clips"][0]["rationale"], [c["assetId"] for c in d["data"]["spec"]["clips"]])'
api revision.compare "{\"left\":\"$R1\",\"right\":\"$R2\"}" | J '"compare: %s" % d["data"]["diff"]["summary"]'
curl -sf -b "$CJ" -o "$H/download.mp4" -D "$H/headers.txt" "http://127.0.0.1:$PORT/media/renders/$REND?download=1"
grep -i "content-disposition" "$H/headers.txt" | tr -d '\r'
ffprobe -v error -show_entries format=duration -of csv=p=0 "$H/download.mp4" | xargs -I{} echo "downloaded MP4 decodes, duration {}s"
api revision.approve "{\"id\":\"$R2\",\"note\":\"looks good\"}" | J '"approved via dashboard: %s" % d["data"]["approval"]["state"]'
MR revision approve "$R2" --json </dev/null | J '"CLI approve without a person → %s" % d["error"]["code"]' || true

step "deliver: approved revision → verified copy + JSON sidecar; draft refused; never overwrites"
MR deliver --revision "$R1" --quality review --out "$H/deliveries" --json | J '"draft r1 → %s" % d["error"]["code"]' || true
MR deliver --revision "$R2" --quality review --out "$H/deliveries" --json | J '"delivered %s (sha256 %s…, %s)" % (d["data"]["files"][0]["path"], d["data"]["files"][0]["sha256"][:12], d["data"]["files"][0]["approval"])'
MR deliver --revision "$R2" --quality review --out "$H/deliveries" --json | J '"again → %s" % d["error"]["code"]' || true

step "timeline export: the version as OpenTimelineIO for DaVinci Resolve, its footage beside it under readable names"
MR export timeline "$R2" --format otio --out "$H/handoff/cut" --media "$H/handoff/Footage" --json > "$H/otio.json"
J '"%s Footage: %s" % (d["data"]["summary"], sorted("%s (%s)" % (m["name"], m["how"]) for m in d["data"]["media"]))' < "$H/otio.json"
python3 -I - "$H/handoff/cut.otio" <<'PY'
import json, os, sys, urllib.parse
t = json.load(open(sys.argv[1]))
assert t["OTIO_SCHEMA"].startswith("Timeline."), t["OTIO_SCHEMA"]
tracks = t["tracks"]["children"]
clips = [c for tr in tracks for c in tr["children"] if c["OTIO_SCHEMA"].startswith("Clip.")]
refs = [r for c in clips for r in [c.get("media_reference")] + list(c.get("media_references", {}).values()) if r and r.get("target_url")]
missing = [r["target_url"] for r in refs if not os.path.exists(urllib.parse.unquote(urllib.parse.urlparse(r["target_url"]).path))]
print("OTIO: %d tracks (%s), %d clips, every clip's footage in the folder: %s" % (len(tracks), ", ".join(sorted({tr["kind"] for tr in tracks})), len(clips), not missing and bool(refs)))
sys.exit(1 if missing or not clips else 0)
PY
if [ -n "${METARUSH_OTIO_VENV:-}" ]; then
  "$METARUSH_OTIO_VENV/bin/python" -I -c "import opentimelineio as otio,sys;t=otio.adapters.read_from_file(sys.argv[1]);print('OpenTimelineIO reads it: %d tracks, %.2f s' % (len(t.tracks), t.duration().to_seconds()))" "$H/handoff/cut.otio"
fi

step "MCP: a client lists the tools (read-only), reads a version, and is refused what only a person may do"
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"acceptance","version":"1"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"revision_show","arguments":{"id":"'"$R2"'"}}}' \
  '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"revision_approve","arguments":{"id":"'"$R2"'"}}}' \
  '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"project_create","arguments":{"name":"not on a read-only connection"}}}' \
  | MR mcp serve 2>/dev/null | python3 -I -c '
import json, sys
got = {m["id"]: m for m in map(json.loads, sys.stdin) if "id" in m}
init, tools = got[1]["result"], got[2]["result"]["tools"]
names = {t["name"] for t in tools}
read, approve, create = (got[i]["result"] for i in (3, 4, 5))
print("MCP %s: %d tools, all read-only: %s; people-only offered: %s" % (init["protocolVersion"], len(tools), all(t["annotations"]["readOnlyHint"] for t in tools), sorted(names & {"revision_approve", "asset_delete", "storage_migrate"}) or "none"))
print("revision_show: version %s; revision_approve: %s; project_create: %s" % (read["structuredContent"]["summary"]["number"], approve["structuredContent"]["error"]["code"], create["structuredContent"]["error"]["code"]))
ok = not read["isError"] and approve["isError"] and create["isError"] and "revision_approve" not in names
sys.exit(0 if ok else 1)'
MR log list --since 2h --channel mcp --json | J '"logged on the mcp channel: %s" % sorted({e["text"] for e in d["data"]["entries"]})'
step "resources: a logo, the brand kit, a title card with the logo, rendered; another project's cut nested and rendered; credits"
RF="$H/resource-files"; mkdir -p "$RF"
printf '<svg xmlns="http://www.w3.org/2000/svg" width="480" height="160" viewBox="0 0 480 160"><script>alert(1)</script><rect x="8" y="8" width="144" height="144" rx="36" fill="#E2552D"/><circle cx="80" cy="80" r="38" fill="#FFF4E8"/><rect x="176" y="56" width="280" height="48" rx="8" fill="#1B1B1F"/></svg>\n' > "$RF/acme-logo.svg"
MR add "$RF/acme-logo.svg" --project "$P" --json | J '"added: %s (the SVG was cleaned: %s)" % ([f["message"] for f in d["data"]["paths"]["files"]], [c for f in d["data"]["paths"]["files"] for c in f.get("changes", [])])'
MR resources list --project "$P" --json | J '"resources: %s; suggested tags %s" % ([r["name"] + " (" + r["kind"] + ")" for r in d["data"]["resources"]], [t["tag"] for r in d["data"]["resources"] for t in r.get("autoTags", [])])'
MR brand set --logo acme-logo --json | J '"brand kit: logo %s, colours %s" % (d["data"]["logo"]["name"], [d["data"]["brand"].get(k) for k in ("primary", "accent", "background")])'
BP=$(MR project create "Acceptance brand" --json | J 'd["data"]["project"]["id"]')
MR asset link "$B" --project "$BP" --json >/dev/null
cat > "$H/brand-plan.json" <<EOF
{"projectId":"$BP","instruction":"A brand intro with our logo on the title card",
 "spec":{"schemaVersion":1,"title":"Brand intro","output":{"preset":"vertical-1080p","fps":30,"platform":"shorts"},
  "clips":[{"id":"intro","assetId":"$B","in":0,"out":3,"framing":{"mode":"fill","focusX":0.5}}],
  "overlays":[{"id":"title","template":"title.card","at":0.2,"duration":2.4,"params":{"title":"Acme","subtitle":"Made by hand","logo":"logo"}}]}}
EOF
MR edit check --plan "$H/brand-plan.json" --json | J '"brand cut check (a new cut starts with the kit, its logo pinned as logo): valid=%s" % d["data"]["valid"]'
TOK=$(MR edit check --plan "$H/brand-plan.json" --json | J 'd["data"]["confirmToken"]')
BR1=$(MR edit apply --plan "$H/brand-plan.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
MR cut show "$BP" --json | J '"brand cut sheet: %s" % [l.strip() for l in d["data"]["sheet"].splitlines() if l.strip().startswith(("logo:", "- title:"))]'
BREND=$(MR render submit --revision "$BR1" --quality review --no-qa --wait --json | J 'd["data"]["render"]["id"]')
MR render status "$BREND" --json | J '"title card with the logo rendered: %s %.2fs" % (d["data"]["render"]["state"], d["data"]["render"]["output"]["duration"])'
MR project link "$P" "$BP" --json | J '"%s" % d["data"]["message"]'
NT=$(MR cut nest "$P" "$BP" --dry-run --json | J 'd["data"]["check"]["confirmToken"]')
MR cut nest "$P" "$BP" --confirm "$NT" --json | J '"nested: %s (%s)" % (d["data"]["summary"], d["data"]["applied"]["revision"]["number"])'
NR=$(MR revision list "$P" --json | J 'd["data"]["revisions"][0]["id"]')
MR cut show "$P" --json | J '"the sheet: %s" % [l.strip() for l in d["data"]["sheet"].splitlines() if "cut from" in l or l.strip().startswith("acceptance-brand:")]'
MR revision plan "$NR" --json | J '"plan: %d frames, nested %s" % (d["data"]["durationInFrames"], [(v["projectName"], v["number"], v["plan"]["durationInFrames"]) for v in d["data"].get("nested", {}).values()])'
NREND=$(MR render submit --revision "$NR" --quality review --wait --json | J 'd["data"]["render"]["id"]')
MR render status "$NREND" --json | J '"nested render: %s %.2fs" % (d["data"]["render"]["state"], d["data"]["render"]["output"]["duration"])'
MR qa run "$NREND" --wait --json | J '"QA of the nested render: %s; notes %s" % (d["data"]["report"]["verdict"], [f["kind"] for f in d["data"]["report"]["findings"] if f["severity"] == "info"])'
# The render queued its own quality check; the terminal that waited for the render left it alone and
# `qa run --wait` ran that same job: one check, finished, none failed or cut off.
MR job list --type qa --json | NREND="$NREND" J '"the automatic quality check of the nested render: %s" % [(j["state"], j["attemptCount"]) for j in d["data"]["jobs"] if j["payload"].get("renderId") == __import__("os").environ["NREND"]]'
api revision.approve "{\"id\":\"$NR\",\"note\":\"with the brand intro\"}" >/dev/null
MR deliver --revision "$NR" --quality review --out "$H/deliveries-nested" --json >/dev/null
J '"sidecar: resources %s, nested cuts %s, credits %s" % ([r["name"] + "@" + r["sha256"][:8] + (" in " + r["in"] if r.get("in") else "") for r in d.get("resources", [])], [(n["projectName"], n["revisionNumber"]) for n in d.get("nestedCuts", [])], [c["credit"][:60] for c in d.get("credits", [])])' < "$(ls "$H"/deliveries-nested/*.json | head -1)"
MR asset where "$A" --json | J '"where interview-a is used: %s" % [u["text"] for u in d["data"]["uses"]]'
MR asset delete "$A" --forever --json </dev/null | J '"deleting it from a script → %s" % d["error"]["code"]' || true

step "repositories, web pages and slides: a fixture repository at its commit, a code slide and a diagram from it, a local site's snapshot, slides in a cut, rendered and delivered with credits"
# The repository is built here (scripts/make-fixture-repo.sh) and fetched as https://github.com/acme/widget
# through METARUSH_GIT_BASE: no network. The site is tests/fixtures/site served on loopback; snapshots of
# this computer are refused unless METARUSH_PAGES_ALLOW_LOCAL=1 (tests only).
GB="$(dirname "$H")/git-fixtures"; scripts/make-fixture-repo.sh "$GB" >/dev/null
export METARUSH_GIT_BASE="$GB"
SP=$(MR project create "Acceptance slides" --json | J 'd["data"]["project"]["id"]')
MR asset link "$B" --project "$SP" --json >/dev/null
MR resources add https://github.com/acme/widget --project "$SP" --wait --json | CHECK '
rs = d["data"].get("resources", []); js = d["data"].get("jobs", [])
assert [r["kind"] for r in rs] == ["repo"], [(j["state"], j.get("error")) for j in js]
print("brought in: %s (%s)" % ([r["name"] + " (" + r["kind"] + ")" for r in rs], [j["result"]["message"] for j in js]))'
MR resources show widget --project "$SP" --json | CHECK '
r = d["data"]["repo"]
assert len(r["commit"]) == 40 and r["files"] > 0 and r["skipped"] >= 3, r
print("pinned at %s: %d text files kept, %d left out (%s); licence %s; languages %s; history from %s" % (r["commit"][:8], r["files"], r["skipped"], "; ".join(r["skippedWhy"]), r["license"], [l["name"] for l in r["languages"]], r["history"]))'
RP=$(MR resources path widget --project "$SP" --json | J 'd["data"]["path"]')
[ -f "$RP/internal/queue/queue.go" ] && [ ! -w "$RP/internal/queue/queue.go" ] && [ ! -e "$RP/assets/logo.png" ] && echo "a read-only copy to read (no pictures in it): $(ls "$RP" | tr '\n' ' ')"
MR resources make code widget --project "$SP" --file internal/queue/queue.go --lines 46-63 --highlight 52-55 --name main-loop --json | CHECK '
o = d["data"]
assert o["provenance"].startswith("github.com/acme/widget@") and o["provenance"].endswith(":internal/queue/queue.go:46-63"), o["provenance"]
print("code slide %s from %s" % (o["resource"]["name"], o["provenance"]))'
printf 'flowchart LR\n  cli[metarush CLI] --> q[(Job queue)]\n  q --> w[Worker]\n  w --> s[(Media store)]\n' > "$H/architecture.mmd"
MR resources make diagram widget --project "$SP" --from "$H/architecture.mmd" --name architecture --json | J '"diagram %s (%s): %s" % (d["data"]["resource"]["name"], d["data"]["resource"]["kind"], d["data"]["message"])'
SITE="$(dirname "$H")/site"; cp -R tests/fixtures/site "$SITE"
ffmpeg -hide_banner -loglevel error -f lavfi -i color=c=0xE2552D:s=1200x630 -frames:v 1 "$SITE/preview.png"
ffmpeg -hide_banner -loglevel error -f lavfi -i color=c=0x1B1B1F:s=64x64 -frames:v 1 "$SITE/icon.png"
SITE_PORT=$((PORT + 2))
python3 -I -m http.server "$SITE_PORT" --bind 127.0.0.1 --directory "$SITE" >/dev/null 2>&1 & WEB=$!
for _ in $(seq 1 40); do curl -sf "http://127.0.0.1:$SITE_PORT/" >/dev/null && break; sleep 0.25; done
MR resources add "http://127.0.0.1:$SITE_PORT/" --project "$SP" --json | J '"a page on this computer → %s: %s" % (d["error"]["code"], d["error"]["message"])' || true
METARUSH_PAGES_ALLOW_LOCAL=1 MR resources add "http://127.0.0.1:$SITE_PORT/" --project "$SP" --depth 1 --max-pages 3 --wait --json | J '"snapshots: %s" % [r["name"] for r in d["data"]["resources"]]'
MR resources show r-127-0-0-1 --project "$SP" --json | CHECK '
row = d["data"]["row"]; v = [x for x in d["data"]["resource"]["versions"] if x["sha256"] == row["current"]][0]
assert [p["label"] for p in v["pages"]] == ["desktop", "mobile", "full"], v["pages"]
assert v["site"]["preview"] and v["site"]["icon"] and v["site"]["themeColors"][0] == "#E2552D", v["site"]
print("page %r on %s: pictures %s, theme colour %s, preview picture and icon kept, its words: %r" % (v["site"]["title"], v["site"]["takenAt"][:10], ["%s %dx%d" % (p["label"], p["width"], p["height"]) for p in v["pages"]], v["site"]["themeColors"][0], v["text"][:50]))'
{ kill "$WEB" && wait "$WEB"; } 2>/dev/null || true
cat > "$H/slides-plan.json" <<EOF
{"projectId":"$SP","instruction":"A short tour of Widget",
 "spec":{"schemaVersion":1,"title":"Widget tour","output":{"preset":"landscape-1080p","fps":30,"platform":"youtube"},
  "clips":[{"id":"a","assetId":"$B","in":0,"out":7},{"id":"b","assetId":"$B","in":3,"out":10}],
  "overlays":[{"id":"intro","template":"slide.title","at":0,"duration":2.5,"params":{"title":"How Widget works","kicker":"Part 1","subtitle":"A job queue in one binary"}},
   {"id":"why","template":"slide.bullets","at":2.5,"duration":3,"params":{"title":"Why it is fast","bullets":["Everything runs on your computer","Files are stored once","Renders are locked down"]}}]}}
EOF
TOK=$(MR edit check --plan "$H/slides-plan.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$H/slides-plan.json" --confirm "$TOK" --json >/dev/null
MR cut show "$SP" --out "$H/slides.cut.md" >/dev/null
python3 -I - "$H/slides.cut.md" <<'PY'
import sys
p = sys.argv[1]; s = open(p).read()
add = ('- loop: slide.code at 0:05.5 for 3 s · with title "The worker loop", code "main-loop", caption "internal/queue/queue.go"\n'
       '- arch: slide.architecture at 0:08.5 for 2.5 s · with title "How it fits together", diagram "architecture"\n'
       '- home: slide.screenshot at 0:11 for 2.5 s · with image "r-127-0-0-1#3", caption "The whole homepage"\n')
i = s.index("## Graphics\n"); j = s.find("\n## ", i + 1); j = len(s) if j < 0 else j + 1
block = s[i:j].rstrip("\n") + "\n" + add + "\n"
open(p, "w").write(s[:i] + block + s[j:])
PY
MR cut check "$H/slides.cut.md" -m "slides from the repository and the homepage" --json > "$H/slides.check.json"
CHECK '
c = d["data"]
assert c["valid"], [p["message"] for p in c["problems"]]
pinned = sorted({w["message"].split(" ")[1] for w in c["problems"] if w["code"] == "resource_pinned"})
assert pinned == ["architecture", "main-loop", "r-127-0-0-1"], pinned
print("slides in the sheet: valid=%s; pinned when checked: %s; changes: %s" % (c["valid"], pinned, c["changes"]))' < "$H/slides.check.json"
TOK=$(J 'd["data"]["confirmToken"]' < "$H/slides.check.json")
SR=$(MR cut apply "$H/slides.cut.md" --confirm "$TOK" -m "slides from the repository and the homepage" --json | J 'd["data"]["revision"]["id"]')
MR cut show "$SP" --json | J '"the sheet: %s" % [l.strip()[:72] for l in d["data"]["sheet"].splitlines() if "slide." in l or l.strip().startswith(("main-loop:", "architecture:", "r-127-0-0-1:"))]'
SREND=$(MR render submit --revision "$SR" --quality review --wait --json | J 'd["data"]["render"]["id"]')
MR render status "$SREND" --json | J '"slides render: %s %.2fs" % (d["data"]["render"]["state"], d["data"]["render"]["output"]["duration"])'
MR qa run "$SREND" --wait --json | J '"QA of the slides: %s; notes %s" % (d["data"]["report"]["verdict"], [f["message"][:90] for f in d["data"]["report"]["findings"] if f["severity"] == "info"])'
api revision.approve "{\"id\":\"$SR\",\"note\":\"the tour\"}" >/dev/null
MR deliver --revision "$SR" --quality review --out "$H/deliveries-slides" --json >/dev/null
CHECK '
creds = [c["credit"] for c in d.get("credits", [])]
assert any(c.startswith("Includes code from github.com/acme/widget@") and ":internal/queue/queue.go:46-63" in c for c in creds), creds
assert any(c.startswith("Includes a snapshot of") for c in creds), creds
print("sidecar credits:\n  " + "\n  ".join(creds))
print("provenance: %s" % [(r["name"], r.get("provenance", "")) for r in d.get("resources", [])])' < "$(ls "$H"/deliveries-slides/*.json | head -1)"
SF="$H/slide-frames"; mkdir -p "$SF"; SV="$(ls "$H"/deliveries-slides/*.mp4 | head -1)"
for t in 1.2 4 7 9.7 12.6; do ffmpeg -hide_banner -loglevel error -ss "$t" -i "$SV" -frames:v 1 "$SF/at-$t.png"; done
echo "slide frames to look at: $(ls "$SF" | tr '\n' ' ')(in $SF)"
scripts/make-fixture-repo.sh "$GB" second >/dev/null
MR resources refresh widget --project "$SP" --wait --json | J '"refreshed: %s; %s" % (d["data"]["message"], [j["state"] for j in d["data"].get("jobs", [])])'
MR resources show main-loop --project "$SP" --json | CHECK '
prov = d["data"]["row"]["provenance"]
assert prov.endswith(":internal/queue/queue.go:47-64"), prov
print("the code slide followed its lines to the new commit: %s (%d versions)" % (prov, len(d["data"]["resource"]["versions"])))'
MR cut show "$SP" --json | J '"the cut keeps its pins until an update: %s" % [l.strip()[:80] for l in d["data"]["sheet"].splitlines() if l.strip().startswith("main-loop:")]'
# Art kept in git (D216): the pictures in a folder of the repository, each a picture of its own.
scripts/make-fixture-repo.sh "$GB" art >/dev/null
MR resources refresh widget --project "$SP" --wait --json >/dev/null
MR resources pictures widget --project "$SP" --keep art/characters --wait --json | CHECK '
p = d["data"]
assert p["kept"] == 5 and [x["path"].split("/")[-1] for x in p["pictures"]] == ["alice.png", "bob.jpg", "carol.svg", "hero", "dot.gif"], p
print("pictures kept from art/characters: %s; left out: %s" % ([x["name"] for x in p["pictures"]], "; ".join(p.get("skippedWhy", []))))'
MR resources show widget-alice --project "$SP" --json | CHECK '
prov = d["data"]["row"]["provenance"]
assert prov.startswith("github.com/acme/widget@") and prov.endswith(":art/characters/alice.png"), prov
print("a picture from the repository says where it came from: %s" % prov)'
unset METARUSH_GIT_BASE
step "the Library: archive → restore; a collection a project uses; the Trash in two steps (deleting for good is for people)"
# apiAny returns the answer of a refused call too (api fails on an error).
apiAny() { curl -s -b "$CJ" -H 'X-MetaRush-Client: dashboard' -H 'Content-Type: application/json' -X POST "http://127.0.0.1:$PORT/api/ops/$1" -d "$2"; }
MR ingest tests/fixtures/gen/long-silence.mp4 tests/fixtures/gen/phone-rotated.mov tests/fixtures/gen/vfr.mp4 --wait --json >/dev/null
inbox_id() { MR library --json | J '[i["id"] for i in d["data"]["items"] if i["name"]=="'"$1"'"][0]'; }
LS=$(inbox_id long-silence.mp4); PR=$(inbox_id phone-rotated.mov); VF=$(inbox_id vfr.mp4)
MR library --json | CHECK '
c = d["data"]["counts"]
print("Inbox %d · Collections %d · Archived %d · In projects %d · Trash %d" % (c["inbox"], c["collections"], c["archived"], c["projects"], c["trash"]))
assert c["inbox"] >= 3, "three new clips wait in the Inbox"'
MR asset archive "$LS" --json | J '"archive: %s" % d["data"]["message"]'
MR library archived --json | CHECK '
w = [i["where"] for i in d["data"]["items"] if i["name"] == "long-silence.mp4"]
print("in the Archive: %s" % w)
assert w == ["In the Archive"]'
MR library --json | CHECK 'assert all(i["name"] != "long-silence.mp4" for i in d["data"]["items"]), "out of the Inbox"'
MR asset unarchive "$LS" --json | J '"unarchive: %s" % d["data"]["message"]'
MR collection create "Acceptance B-roll" --json | J 'd["data"]["message"]'
MR collection add "acceptance b-roll" "$PR" "$VF" --json | J '"%s (%s)" % (d["data"]["message"], d["data"]["collection"]["text"])'
MR project link "$P" "Acceptance B-roll" --json | CHECK '
l = d["data"]["link"]
print(d["data"]["message"])
assert l["sourceKind"] == "collection" and l["counts"]["footage"] == 2, "the project uses everything on the collection"'
MR project show "$P" --json | CHECK '
seen = sorted(a["name"] for a in d["data"]["assets"] if a.get("fromCollection"))
print("the project sees, from the collection: %s" % seen)
assert seen == ["phone-rotated.mov", "vfr.mp4"]'
MR collection list --json | J '"collections: %s" % ["%s: %s" % (c["name"], c["text"]) for c in d["data"]["collections"]]'
MR asset trash "$LS" --json | J '"trash: %s" % d["data"]["message"]'
MR trash list --json | J '"the Trash: %s" % d["data"]["summary"]'
MR asset restore "$LS" --json | J '"restore: %s" % d["data"]["message"]'
# A clip the cut uses can't go to the Trash, and can't be deleted for good.
MR asset trash "$A" --project "$P" --json | J '"moving a clip the cut uses to the Trash → %s" % d["error"]["code"]' || true
apiAny asset.delete "{\"assetId\":\"$A\",\"projectId\":\"$P\",\"forever\":true}" | CHECK '
print("deleting it for good, as a person → %s: %s" % (d["error"]["code"], d["error"]["message"][:110]))
assert d["error"]["code"] == "E_CONFLICT" and "cut" in d["error"]["message"], "refused because a cut uses it"'
# Emptying the Trash: refused for a script, allowed for a person (the dashboard), with the size first.
MR asset trash "$LS" --json >/dev/null
MR trash empty --json </dev/null | J '"empty the Trash from a script → %s" % d["error"]["code"]' || true
MR trash settings --empty-after 30 --json </dev/null | J '"turn on emptying after 30 days from a script → %s" % d["error"]["code"]' || true
api trash.empty '{"dryRun":true}' | J '"the warning: %s" % d["data"]["question"]'
api trash.empty '{}' | CHECK '
print("emptied by a person: %s" % d["data"]["message"])
assert d["data"]["deleted"] and d["data"]["count"] == 1'
MR trash list --json | CHECK '
print("the Trash now: %s" % d["data"]["summary"])
assert d["data"]["count"] == 0'
MR trash settings --json | J '"emptying on its own: %s" % d["data"]["summary"]'

step "motion graphics: a project's own component is checked, refused until a person approves it, rendered, then withdrawn"
MF="$H/motion-project"; mkdir -p "$MF/motion"; cp -R video/render/fixtures/motion/route-map "$MF/motion/route-map"
MR motion check "$MF/motion/route-map" --no-preview --json > "$H/motion.json"
J '"motion check: passed=%s, %s, %d test frames, imports %s" % (d["data"]["ok"], d["data"]["state"], len(d["data"]["frames"]), sorted({i["specifier"] for i in d["data"]["imports"]}))' < "$H/motion.json"
MV=$(J 'd["data"]["version"]' < "$H/motion.json"); MH=$(J 'd["data"]["hash"]' < "$H/motion.json")
mkdir -p "$MF/motion/sneaky" && printf "import { AbsoluteFill } from 'remotion';\nimport { readFileSync } from 'node:fs';\nimport { z } from 'zod';\nexport const schema = z.object({});\nexport const meta = { summary: 'x', region: 'center', minDuration: 1, maxDuration: 2 };\nexport default function S() { return <AbsoluteFill>{String(readFileSync('/etc/hosts'))}</AbsoluteFill>; }\n" > "$MF/motion/sneaky/index.tsx"
MR motion check "$MF/motion/sneaky" --no-preview --json | J '"a component reading files → passed=%s: %s line %s: %s" % (d["data"]["ok"], d["data"]["problems"][0]["file"], d["data"]["problems"][0]["line"], d["data"]["problems"][0]["message"][:60])'
HEAD=$(MR revision list "$P" --json | J 'd["data"]["revisions"][0]["id"]')
printf '{"baseRevisionId":"%s","instruction":"add the route","ops":[{"op":"addOverlay","overlay":{"id":"route","motion":{"name":"route-map","hash":"%s"},"at":0.5,"duration":3,"params":{"from":"Lisbon","to":"Porto"}}}]}' "$HEAD" "$MH" > "$H/motion-plan.json"
MR edit check --plan "$H/motion-plan.json" --json | J '"before a person approves it, a cut using it → valid=%s %s" % (d["data"]["valid"], [e["code"] for e in d["data"]["errors"]])'
MR motion approve "$MV" --json </dev/null | J '"CLI approve without a person → %s" % d["error"]["code"]' || true
api motion.approve "{\"ref\":\"$MV\",\"note\":\"reviewed the frames\"}" | J '"approved as a person in the dashboard: %s" % d["data"]["row"]["state"]'
TOK=$(MR edit check --plan "$H/motion-plan.json" --json | J 'd["data"]["confirmToken"]')
RM=$(MR edit apply --plan "$H/motion-plan.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
MR cut show "$P" --revision "$RM" --json | J '"the cut sheet says: %s" % [l.strip() for l in d["data"]["sheet"].splitlines() if "motion " in l or l.strip().startswith("route-map:")]'
MREND=$(MR render submit --revision "$RM" --quality review --no-qa --wait --json | J 'd["data"]["render"]["id"]')
MR render status "$MREND" --json | J '"motion render: %s %.2fs, drawn by %s with renderer %s" % (d["data"]["render"]["state"], d["data"]["render"]["output"]["duration"], {k: v[:8] for k, v in d["data"]["render"]["templateVersions"].items() if k.startswith("motion:")}, d["data"]["render"]["rendererId"])'
MR qa run "$MREND" --wait --json | J '"QA of the motion render: %s, layout findings %s" % (d["data"]["report"]["verdict"], [f["kind"] for f in d["data"]["report"]["findings"] if f["kind"] in ("overlay_overlap","caption_overlay_stack","captions_hidden","platform_safe_area")])'
api motion.revoke "{\"ref\":\"$MV\",\"note\":\"wrong colors\"}" | J '"withdrawn in the dashboard: %s" % d["data"]["row"]["state"]'
wd=$(MR render submit --revision "$RM" --quality final --no-qa --wait --json || true)
echo "$wd" | J '"a new render after withdrawal → %s: %s" % (d["error"]["code"], d["error"]["message"][:110])'
# Refused up front with the plain reason, not queued to fail in the worker.
echo "$wd" | J '"" if d.get("error", {}).get("details", {}).get("reason") == "motion_not_approved" else sys.exit("a render using a withdrawn graphic was not refused up front")'

step "conversations: one call's two recordings lined up by their sound, cut automatically, cleaned up, rendered and checked (D99–D103)"
# Maya and Sam on a call, each recorded on their own machine: Sam's file starts 1.234 s in,
# his webcam freezes at 0:30 and his sound drops out at 0:40 (tests/fixtures/truth/conversation.json).
scripts/make-conversation-fixtures.sh >/dev/null
PC=$(MR project create "Acceptance call" --json | J 'd["data"]["project"]["id"]')
MR ingest tests/fixtures/gen/conversation/maya.mov tests/fixtures/gen/conversation/sam.mov --project "$PC" --wait --json \
  | J '"the call recordings processed: %s" % sorted({"%s %s" % (j["type"], j["state"]) for j in d["data"]["jobs"]})'
CJ=$(MR asset list "$PC" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="maya.mov"][0]')
CS=$(MR asset list "$PC" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="sam.mov"][0]')
MR conversation create --project "$PC" --members "$CJ" "$CS" --names "Maya,Sam" --name "Garden call" --json | CHECK '
v = d["data"]["conversation"]
sam = [m for m in v["members"] if m["name"] == "Sam"][0]
print("lined up by their sound: Sam starts %.3f s in (truth 1.234), %s, %s" % (sam["start"], sam["method"], sam["sure"]))
assert abs(sam["start"] - 1.234) < 1 / 30, "within a frame of the truth"'
MR conversation cut "Garden call" --project "$PC" --style podcast --apply --json | CHECK '
o = d["data"]["result"]
print("automatic cut: %s (%d reasons)" % (o["summary"], len(o["why"])))
assert o["applied"]["number"] >= 1 and len(o["why"]) >= 6, "a new version with a reason per shot"'
MR conversation cleanup "Garden call" --json | CHECK '
kinds = sorted({s["kind"] for s in d["data"]["suggestions"]})
print("clean-up suggestions: %s" % ["%s %s" % (s["kind"], s["when"]) for s in d["data"]["suggestions"]])
assert {"call-talk", "dead-air", "freeze", "dropout"} <= set(kinds), "the call talk, the long pause, the freeze and the dropout"'
CH=$(MR project show "$PC" --json | J 'd["data"]["headRevisionId"]')
CREND=$(MR render submit --revision "$CH" --quality review --no-qa --wait --json | J 'd["data"]["render"]["id"]')
MR qa run "$CREND" --wait --no-speech-check --json | CHECK '
r = d["data"]["report"]
checks = {c["name"]: c["status"] for c in r["checks"] if c["name"].startswith("conversation")}
people = r["measurements"]["conversations"][0]["people"]
print("quality check of the conversation render: %s; %s" % (checks, ["%s %.1f LUFS" % (p["name"], p["loudness"]) for p in people if p.get("loudness") is not None]))
print("  %s" % [f["message"] for f in r["findings"] if f["kind"].startswith("conversation")])
assert checks.get("conversation_voices") == "pass", "both people heard about as loud"
assert checks.get("conversation_picture") == "pass", "nobody alone on screen too long"
assert any(f["kind"] == "conversation_freeze" for f in r["findings"]), "the frozen webcam on screen"'

step "generated video: priced first, made only with the agreed price, how it was made kept, marked AI-generated in the cut, the quality check and the delivery; no one's likeness without consent (D123–D127)"
# A fake Gemini API (internal/testutil/fakevideogen): no Google account, no real key. Its address goes in the home's
# .env before any key exists, so nothing in this run can reach Google.
GF="${METARUSH_ACCEPTANCE_GEN_PORT:-$((PORT + 3))}"
CJ="$H/cookies.txt" # the dashboard session from step 8 (the conversations step reused the name)
printf 'METARUSH_GEMINI_BASE_URL=http://127.0.0.1:%s\n' "$GF" >> "$H/.env"
python3 -I -c "import secrets;print('gk-'+secrets.token_hex(20))" > "$H/state/fake-gemini.key"; chmod 600 "$H/state/fake-gemini.key"
go build -o "$H/fakevideogen" ./internal/testutil/fakevideogen/cmd/fakevideogen
"$H/fakevideogen" -addr "127.0.0.1:$GF" -key-file "$H/state/fake-gemini.key" > "$H/fakevideogen.log" 2>&1 & GEN=$!
# Up to a minute (a loaded machine starts it slowly), and say so when it never answers.
for _ in $(seq 1 240); do curl -s -o /dev/null "http://127.0.0.1:$GF/__calls" && break; sleep 0.25; done
curl -s -o /dev/null "http://127.0.0.1:$GF/__calls" || { echo "the fake Gemini API on port $GF never answered (see $H/fakevideogen.log)" >&2; exit 1; }
MR providers set gemini --key-stdin --json < "$H/state/fake-gemini.key" | J '"the key, checked and saved on this computer (shown as %s): %s" % (d["data"]["masked"], d["data"]["lastTest"]["message"])'
PG=$(MR project create "Acceptance generated" --json | J 'd["data"]["project"]["id"]')
MR generate estimate "a red paper boat drifting on a pond" --project "$PG" --seconds 3 --resolution 360p --json | CHECK '
e = d["data"]
print("priced first: %s" % e["summary"])
assert e["allowed"] and 0.05 < e["usd"] < 0.5, "a 3-second draft costs cents"'
MR generate shot "a red paper boat drifting on a pond" --project "$PG" --seconds 3 --resolution 360p --json </dev/null | J '"without an agreed price → %s: %s" % (d["error"]["code"], d["error"]["message"])' || true
api generate.configure "{\"projectId\":\"$PG\",\"budgetUsd\":0.05}" | J '"a person set the project budget to $%.2f" % d["data"]["projectBudgetUsd"]'
MR generate shot "a red paper boat drifting on a pond" --project "$PG" --seconds 3 --resolution 360p --max-cost 0.5 --json </dev/null | J '"over the project budget → %s: %s" % (d["error"]["code"], d["error"]["message"])' || true
api generate.configure "{\"projectId\":\"$PG\",\"budgetUsd\":5}" >/dev/null
GID=$(MR generate shot "a red paper boat drifting on a pond" --project "$PG" --seconds 3 --resolution 360p --max-cost 0.5 --wait --json </dev/null | J 'd["data"]["generations"][0]["id"]')
MR generate show "$GID" --json | CHECK '
g = d["data"]
print("made: %s Charged $%.3f (%s), deleted at the provider: %s" % (g["summary"], g["outcome"]["actual"]["usd"], g["outcome"]["actual"]["basis"], g["forgotten"]["ok"]))
assert g["state"] == "done" and g["clips"] and g["forgotten"]["ok"] and g["settled"], "made, paid and deleted at the provider"
assert g["prompt"] == "a red paper boat drifting on a pond" and g["priceTable"] and len(g["requestHash"]) == 64, "how it was made"'
GA=$(MR generate show "$GID" --json | J 'd["data"]["clips"][0]["assetId"]')
MR generate settings --project "$PG" --json | CHECK '
b = d["data"]["budget"]
print("money: the project spent $%.3f, $%.3f still set aside" % (b["project"]["spentUsd"], b["project"]["heldUsd"]))
assert b["project"]["heldUsd"] == 0 and b["project"]["spentUsd"] > 0, "set aside, then settled"'
curl -s "http://127.0.0.1:$GF/__calls" | CHECK '
print("the fake Gemini saw: %s" % {k: d.get(k) for k in ("create", "get", "delete", "interactionsKept", "filesKept")})
assert d["interactionsKept"] == 0 and d["filesKept"] == 0 and not d["problems"], "nothing left at the provider; the key never in an address"'
# A key whose Google project is on the free tier (D178): Google accepts the request, then answers every status check
# with a 429 "limit: 0 … on Free Tier" (as on 2026-10-20). The first such answer ends it, plainly and at no cost.
MR generate shot "[fake:free-tier] a paper boat at dusk" --project "$PG" --seconds 3 --resolution 360p --max-cost 0.5 --wait --json </dev/null > "$H/free-tier.json" || true
CHECK < "$H/free-tier.json" '
g = d["data"]["generations"][0]
print("a free-tier key: %s Charged $%.2f (%s); asked Google to delete it: %s" % (g["summary"], g["settled"]["usd"], g["outcome"]["actual"]["basis"], g["forgotten"]["ok"]))
print("  %s" % g["outcome"]["hint"])
assert g["state"] == "failed" and "free tier" in g["outcome"]["message"] and g["settled"]["usd"] == 0, "the free tier ends it at once, at no cost"
assert "https://ai.dev/rate-limit" in g["outcome"]["hint"] and g["forgotten"]["ok"], "Google page behind the hint; nothing kept at Google"'
MR generate settings --project "$PG" --json | CHECK '
assert d["data"]["budget"]["project"]["heldUsd"] == 0, "nothing still set aside for it"'
MR generate estimate "Samantha waves from a paper boat" --project "$PG" --seconds 3 --resolution 360p --json | J '"a prompt naming Samantha → allowed=%s: %s" % (d["data"]["allowed"], d["data"]["consent"]["message"])'
MR generate shot "Samantha waves from a paper boat" --project "$PG" --seconds 3 --resolution 360p --max-cost 0.5 --json </dev/null | J '"…and making it → %s" % d["error"]["code"]' || true
MR consent record --person Samantha --self --adult --statement "I agree to MetaRush making videos with my likeness." --json </dev/null | J '"consent recorded by a script → %s" % d["error"]["code"]' || true
cat > "$H/gplan.json" <<EOF
{"projectId":"$PG","instruction":"open on the generated shot","rationale":"An establishing shot of the pond.",
 "spec":{"schemaVersion":1,"title":"Generated opening","output":{"preset":"landscape-1080p","fps":30,"platform":"youtube"},
  "clips":[{"id":"boat","assetId":"$GA","in":0,"out":3}]}}
EOF
TOK=$(MR edit check --plan "$H/gplan.json" --json | J 'd["data"]["confirmToken"]')
RG=$(MR edit apply --plan "$H/gplan.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
MR cut show "$PG" --json | J '"the cut sheet says: %s" % [l.split("# ")[-1] for l in d["data"]["sheet"].splitlines() if "AI-generated" in l]'
GREND=$(MR render submit --revision "$RG" --quality review --no-qa --wait --json | J 'd["data"]["render"]["id"]')
MR qa run "$GREND" --wait --no-speech-check --json | CHECK '
f = [x["message"] for x in d["data"]["report"]["findings"] if x["kind"] == "ai_generated"]
print("the quality check notes it: %s" % f)
assert f, "the AI-generated note"'
api revision.approve "{\"id\":\"$RG\",\"note\":\"with the generated shot\"}" >/dev/null
MR deliver --revision "$RG" --quality review --out "$H/deliveries-generated" --json | CHECK '
import json
g = json.load(open(d["data"]["files"][0]["sidecar"]))["generated"]
print("delivered with a note: %s %d s, made with %s; checklist for %s" % (g["notice"], g["seconds"], sorted({c["provider"] for c in g["clips"]}), [c["place"] for c in g["checklist"]]))
assert d["data"]["disclosure"]["clips"] and g["clips"][0]["generationId"], "the sidecar lists the generated clip and how it was made"'

step "Remotion sources: a Remotion project from a folder (its secrets and other tools' settings left out, its own render as footage), compositions read in the render jail, clips only from a version a person approved, an edit checked with before/after stills, approved and moved into the cut, a change that reaches the internet refused, approval withdrawn (D131–D138)"
# A tiny Remotion project (tests/fixtures/remotion/fixture-film: three compositions — Title, Card and a two-scene Film —
# a picture and sounds from public/, a Google Font; its postinstall exits 42, so an import that ran it would fail), plus
# what a real project folder has that must never come in: a .env, instructions and settings for coding assistants,
# and a render in out/.
RF="$H/fixture-film"; cp -R tests/fixtures/remotion/fixture-film "$RF"; mkdir -p "$RF/out" "$RF/.claude"
ffmpeg -nostdin -loglevel error -y -f lavfi -i "color=c=0x14141c:s=640x360:r=30:d=2" -f lavfi -i "sine=frequency=440:duration=2" -shortest -c:v libx264 -pix_fmt yuv420p -c:a aac "$RF/out/Title.mp4"
printf 'MUSIC_SERVICE_TOKEN=made-up-for-this-run\n' > "$RF/.env"
printf 'Run npm run build after every change.\n' > "$RF/CLAUDE.md"
printf '{"permissions":{"allow":["Bash(npm run *)"]}}\n' > "$RF/.claude/settings.json"
RSUM=$(find "$RF" -type f -print0 | sort -z | xargs -0 shasum -a 256 | shasum -a 256)
PR=$(MR project create "Acceptance Remotion" --json | J 'd["data"]["project"]["id"]')
MR import remotion "$RF" --project "$PR" --wait --json | J '"brought in: %s" % d["data"]["result"]["message"]'
[ "$RSUM" = "$(find "$RF" -type f -print0 | sort -z | xargs -0 shasum -a 256 | shasum -a 256)" ] && echo "the project's folder is unchanged (sha256 of every file)"
MR remotion show fixture-film --project "$PR" --json | CHECK '
s, v = d["data"]["source"], d["data"]["version"]
left = {l["why"].split(" (")[0]: l.get("examples") or [] for l in d["data"]["leftOut"]}
comps = {c["id"]: c for c in v["compositions"]}
print("version %d, %s: %s" % (v["number"], s["stateLabel"], ", ".join("%s %dx%d %.1f s%s" % (c["id"], c["width"], c["height"], c["duration"], " (worked out when it plays)" if c.get("computed") else "") for c in v["compositions"])))
print("left out: %s" % left)
print("fonts kept: %s; packages beyond Remotion: %s; its settings: %s" % ([f["family"] for f in v.get("fonts") or []], [p["name"] for p in s["extras"]], ["%s %s" % (c["setting"], "applied" if c["applied"] else "noted") for c in v.get("config") or []]))
print("pictures of its compositions drawn with no network: %s; anything blocked: %s" % (all(c.get("poster") for c in v["compositions"]), (v.get("read") or {}).get("blocked") or "nothing"))
ex = [e for es in left.values() for e in es]
assert set(comps) == {"Title", "Card", "Film"} and abs(comps["Title"]["duration"] - 2) < 0.01 and comps["Card"]["computed"] and abs(comps["Film"]["duration"] - 3) < 0.01, "three compositions, one computed"
assert ".env" in ex and "CLAUDE.md" in ex and any(e.startswith(".claude") for e in ex), "secrets and coding-assistant files left out"
assert not any(f == ".env" or f == "CLAUDE.md" or f.startswith((".claude", "out/", "node_modules/")) for f in d["data"]["files"]), "none of them copied"
assert [f["family"] for f in v["fonts"]] == ["Anton"] and all(c.get("poster") for c in v["compositions"])
assert len([c for c in s["clips"] if c["kind"] == "existing"]) == 1, "its render in out/ is footage"
assert s["state"] == "waiting" and not s.get("approved")'
MR remotion use fixture-film Title --project "$PR" --json | J '"before a person approves it, using a composition → %s: %s" % (d["error"]["code"], d["error"]["message"])' || true
MR remotion approve fixture-film@1 --project "$PR" --json </dev/null | J '"CLI approve without a person → %s" % d["error"]["code"]' || true
api remotion.approve "{\"ref\":\"fixture-film@1\",\"projectId\":\"$PR\",\"note\":\"looked at both compositions\"}" | J '"approved as a person in the dashboard: %s" % d["data"]["summary"]'
MR remotion use fixture-film Title --project "$PR" --wait --json | J '"in the cut: %s" % d["data"]["result"]["message"]'
MR remotion render fixture-film Card --project "$PR" --props '{"headline":"Made with props","seconds":1}' --wait --json | J '"a composition with its own props: %s" % d["data"]["result"]["message"]'
MR remotion list --project "$PR" --json | CHECK '
s = d["data"]["sources"][0]
made = {(c["composition"], c.get("props") or ""): c for c in s["clips"] if c["kind"] == "render"}
print("clips made from it: %s" % ["%s v%d %.2f s%s" % (c["composition"], c["versionNumber"], c["duration"], " with its own props" if c.get("props") else "") for c in made.values()])
assert abs(made[("Title", "")]["duration"] - 2) < 0.05 and any(k[0] == "Card" and abs(c["duration"] - 1) < 0.05 for k, c in made.items())'
TA=$(MR remotion list --project "$PR" --json | J '[c["assetId"] for c in d["data"]["sources"][0]["clips"] if c["kind"] == "render" and c["composition"] == "Title"][0]')
MR asset show "$TA" --json | CHECK '
a = d["data"]["asset"] if "asset" in d["data"] else d["data"]
m, src = a["media"], a["source"]
print("the Title clip: %dx%d, %.2f s, sound: %s; made from %s %s version %d" % (m["width"], m["height"], m["duration"], m["hasAudio"], src["platform"], src["remotion"]["composition"], src["remotion"]["versionNumber"]))
assert (m["width"], m["height"]) == (640, 360) and abs(m["duration"] - 2) < 0.05 and m["hasAudio"], "its size, length and its <Audio>"'
MR remotion checkout fixture-film "$H/rf-edit" --project "$PR" --json | J '"to edit: %s" % d["data"]["message"]'
perl -pi -e 's/Hello from the fixture film/Hello from version two/' "$H/rf-edit/src/story.ts"
MR remotion check "$H/rf-edit" -m "a new headline" --project "$PR" --json | CHECK '
c = d["data"]
print("the check of version %d: %s" % (c["versionNumber"], c["summary"]))
print("  words: %s; looks different: %s; before/after stills: %d" % (["%s → %s" % (w.get("before"), w.get("after")) for w in c["words"]], c["changed"], len(c["stills"])))
assert c["ok"] and c["changed"] == ["Title"] and c["stills"] and c["words"][0]["after"] == "Hello from version two"'
MR remotion approve fixture-film@2 --project "$PR" --json </dev/null | J '"CLI approve without a person → %s" % d["error"]["code"]' || true
api remotion.approve "{\"ref\":\"fixture-film@2\",\"projectId\":\"$PR\"}" | J '"approved version 2: %s %s" % (d["data"]["summary"], [u["message"] for u in d["data"].get("updates") or []])'
RJ=$(MR job list --type remotion-render --state queued --json | J '" ".join(j["id"] for j in d["data"]["jobs"])')
if [ -n "$RJ" ]; then MR job wait $RJ --json >/dev/null; fi
MR cut show "$PR" --json | CHECK '
lines = [l.strip() for l in d["data"]["sheet"].splitlines() if "made from" in l]
print("the cut sheet says: %s" % lines)
assert any("Title, version 2" in l for l in lines), "the cut moved to version 2"'
RH=$(MR project show "$PR" --json | J 'd["data"]["headRevisionId"]')
RREND=$(MR render submit --revision "$RH" --quality review --no-qa --wait --json | J 'd["data"]["render"]["id"]')
MR qa run "$RREND" --wait --no-speech-check --json | CHECK '
r = d["data"]["report"]
print("the cut with the Remotion clip, rendered and checked: %s; %s" % (r["verdict"], [f["kind"] for f in r["findings"]][:6]))
assert not any(f["kind"] in ("black_frames", "frozen_frames", "silence") and f.get("severity") == "error" for f in r["findings"])'
perl -0pi -e 's/(export const Title: React.FC = \(\) => \{\n)/$1  if (typeof window !== "undefined") fetch("https:\/\/example.com\/pixel.gif").catch(() => undefined);\n/' "$H/rf-edit/src/Title.tsx"
MR remotion check "$H/rf-edit" -m "a tracking pixel" --project "$PR" --json | CHECK '
c = d["data"]
print("a change that reaches the internet → passed=%s at %s: %s" % (c["ok"], c.get("stage"), [p["message"] for p in c["problems"] if p["code"] == "network"]))
assert not c["ok"] and c.get("stage") == "network"'
apiAny remotion.approve "{\"ref\":\"fixture-film@3\",\"projectId\":\"$PR\"}" | J '"a person approving it anyway → %s: %s" % (d["error"]["code"], d["error"]["message"])'
api remotion.revoke "{\"ref\":\"fixture-film@2\",\"projectId\":\"$PR\",\"note\":\"the old headline was right\"}" | J '"withdrawn in the dashboard: %s" % d["data"]["summary"]'
MR render submit --revision "$RH" --quality final --no-qa --wait --json | J '"a new render of the cut after withdrawal → %s: %s" % (d["error"]["code"], d["error"]["message"])' || true
MR remotion update fixture-film --project "$PR" --json | J '"the cut back on the approved version: %s" % d["data"]["message"]'

step "Remotion sources (the map, words and lifting): the project map, a version waiting asks a person, the music and sound as the cut's tracks, words as settings, a scene lifted into a motion graphic with the shared theme, a theme change approved once (D167–D172)"
# The fixture's Film: two scenes (a Series) with music under them and a sound at the end card; its words in src/story.ts.
waitq() { local q; q=$(MR job list --state queued --json | J '" ".join(j["id"] for j in d["data"]["jobs"] if j["type"] in ("remotion-render", "remotion-task"))'); if [ -n "$q" ]; then MR job wait $q --json >/dev/null; fi; }
MR remotion map fixture-film --project "$PR" --stills --json | CHECK '
import os
m = d["data"]["map"]
film = [c for c in m["compositions"] if c["id"] == "Film"][0]
print("the map of version %d: %s" % (m["number"], "; ".join("%s: %s" % (c["id"], ", ".join("%s %.1f–%.1f s" % (s["name"], s["from"], s["to"]) for s in c["scenes"])) for c in m["compositions"])))
print("  Film says %s" % ["%s (%s:%d)" % (w["text"], w.get("file"), w.get("line") or 0) for s in film["scenes"] for w in s["words"]])
print("  its sounds: %s; its length is set at %s:%s" % (["%s, %s from %.1f s at %d%%" % (s["file"], s["role"], s["from"], round(s["volume"] * 100)) for s in film["sounds"]], film["length"].get("file"), film["length"].get("line")))
print("  a still per scene: %d, Title shows %s" % (len(d["data"]["stills"]), [p["file"] for c in m["compositions"] if c["id"] == "Title" for s in c["scenes"] for p in s["pictures"]]))
names = [s["name"] for s in film["scenes"]]
assert names == ["Opening", "End card"] and abs(film["scenes"][1]["from"] - 1.5) < 0.01 and abs(film["scenes"][1]["to"] - 3) < 0.01, names
assert any(w["text"] == "Made with the fixture theme" and w.get("file") == "src/story.ts" for s in film["scenes"] for w in s["words"]), "words with where they are written"
roles = {s["file"]: s["role"] for s in film["sounds"]}
assert roles.get("music/bed.wav") == "music" and roles.get("tone.wav") == "effect", roles
assert len(d["data"]["stills"]) >= 4 and all(os.path.exists(s["path"]) for s in d["data"]["stills"])'
MR remotion use fixture-film Film --project "$PR" --sound layers --wait --json | J '"Film in the cut, its sound as tracks: %s" % d["data"]["result"]["message"]'
waitq
RL=$(MR project show "$PR" --json | J 'd["data"]["headRevisionId"]')
MR revision show "$RL" --json | CHECK '
s = d["data"]["spec"]
tracks = [(a["role"], round(a.get("at", 0), 2)) for a in s.get("audio") or []]
print("the cut now: %s clips, tracks %s" % (len(s["clips"]), tracks))
roles = {a["role"] for a in s.get("audio") or []}
assert "music" in roles and "sfx" in roles, tracks'
MR remotion words fixture-film --project "$PR" --json | J '"its words: %s" % d["data"]["message"]'
MR remotion words setup fixture-film --project "$PR" --json | J '"making them settings: %s" % d["data"]["result"]["message"]'
MR todos list --kind remotion_approval --json | CHECK '
t = [x for x in d["data"]["todos"] if x["state"] == "open"]
print("To-dos: %s" % [x["question"] for x in t])
assert len(t) == 1 and t[0]["humanOnly"] and t[0]["context"]["remotion"]["number"] >= 2'
MR notification list --json | J '"the bell: %s" % [n["title"] for n in d["data"]["notifications"] if n["kind"] == "remotion_waiting"][:2]'
TD=$(MR todos list --kind remotion_approval --json | J '[x["id"] for x in d["data"]["todos"] if x["state"] == "open"][0]')
MR todos answer "$TD" --suggestion 1 --json </dev/null | J '"Claude answering it → %s" % d["error"]["code"]' || true
api todos.answer "{\"id\":\"$TD\",\"suggestion\":1}" | J '"a person approves it from To-dos: %s" % d["data"]["done"]'
waitq
MR remotion words set fixture-film END.tagline="Lifted, reused and changed" --check --project "$PR" --json | CHECK '
r = d["data"]["result"]
print("checking new words: %s" % r["message"])
assert r.get("ok", True)'
MR remotion words set fixture-film END.tagline="Lifted, reused and changed" --project "$PR" --json | J '"the new words: %s" % d["data"]["result"]["message"]'
waitq
MR remotion words fixture-film --project "$PR" --json | CHECK '
w = d["data"]
row = [s for s in w["settings"] if s["key"] == "END.tagline"][0]
print("END.tagline is now %r (the code says %r); changes: %d" % (row["text"], row["code"], w["number"]))
assert row["changed"] and row["text"] == "Lifted, reused and changed" and w["number"] == 1'
MR remotion list --project "$PR" --json | CHECK '
clips = [c for c in d["data"]["sources"][0]["clips"] if c["kind"] == "render" and c["composition"] == "Film"]
print("Film clips: %s" % ["v%d words %s sound %s" % (c["versionNumber"], c.get("wordsNumber") or 0, c.get("sound") or "mixed") for c in clips])
assert any((c.get("wordsNumber") or 0) == 1 and c.get("sound") == "layers" for c in clips), "the cut clip made again with the new words, its sound still as tracks"'
MR remotion lift fixture-film Film --scene 2 --name fixture-end --project "$PR" --json | J '"lifted: %s" % (d["data"]["result"]["message"] if d.get("ok") else d["error"])'
GV=$(MR motion show fixture-end --json | J 'd["data"]["row"]["version"]'); GH=$(MR motion show fixture-end --json | J 'd["data"]["row"]["hash"]')
LV=$(MR motion show fixture-film-theme --json | J 'd["data"]["row"]["version"]'); LH=$(MR motion show fixture-film-theme --json | J 'd["data"]["row"]["hash"]')
MR motion show fixture-end --json | CHECK '
r = d["data"]["row"]
print("the graphic: %s; draws with %s; %s" % (r["summary"], r.get("libraries"), r["stateLabel"]))
assert r.get("libraries") == ["fixture-film-theme"] and r["state"] == "waiting"'
MR motion show fixture-film-theme --json | J '"the shared library: %s (%s)" % (d["data"]["row"]["summary"], d["data"]["row"].get("kind"))'
api motion.approve "{\"ref\":\"$LV\",\"note\":\"the theme\"}" | J '"a person approves the library: %s" % d["data"]["summary"]'
api motion.approve "{\"ref\":\"$GV\",\"note\":\"the end card\"}" | J '"and the graphic: %s" % d["data"]["summary"]'
FONT=$(MR resources list --project "$PR" --kind font --json | J 'json.dumps([{"id": r["id"], "sha256": r["current"]} for r in d["data"]["resources"] if r["name"] == "anton"][0])')
RH=$(MR project show "$PR" --json | J 'd["data"]["headRevisionId"]')
printf '{"baseRevisionId":"%s","instruction":"the lifted end card","ops":[{"op":"setResource","id":"anton","resource":%s},{"op":"addOverlay","overlay":{"id":"endcard","motion":{"name":"fixture-end","hash":"%s","libs":{"fixture-film-theme":"%s"}},"at":0.5,"duration":2,"params":{"tagline":"Lifted out of the film"}}}]}' "$RH" "$FONT" "$GH" "$LH" > "$H/lift-plan.json"
TOK=$(MR edit check --plan "$H/lift-plan.json" --json | J 'd["data"]["confirmToken"]')
RLIFT=$(MR edit apply --plan "$H/lift-plan.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
MR cut show "$PR" --revision "$RLIFT" --json | J '"the cut sheet says: %s" % [l.strip() for l in d["data"]["sheet"].splitlines() if "fixture-end" in l]'
MR render submit --revision "$RLIFT" --quality review --no-qa --wait --json | CHECK '
r = d["data"]["render"]
tv = {k: v[:8] for k, v in r["templateVersions"].items() if k.startswith("motion:")}
print("rendered with the lifted graphic and its library: %s %.2f s, %s" % (r["state"], r["output"]["duration"], tv))
assert r["state"] == "available" and "motion:fixture-film-theme" in tv and "motion:fixture-end" in tv'
# A theme change: one new version of the library, one approval, and the cut moves to it.
MR remotion checkout fixture-film "$H/rf-theme" --project "$PR" --json >/dev/null
perl -pi -e 's/accent: "#E0A458"/accent: "#5FB4E0"/' "$H/rf-theme/src/theme.ts"
MR remotion check "$H/rf-theme" -m "a blue accent" --project "$PR" --json | J '"the theme changed: %s" % d["data"]["summary"]'
VN=$(MR remotion show fixture-film --project "$PR" --json | J 'd["data"]["source"]["headNumber"]')
api remotion.approve "{\"ref\":\"fixture-film@$VN\",\"projectId\":\"$PR\",\"keepCuts\":true}" | J '"approved version %s: %s" % ("'"$VN"'", d["data"]["summary"])'
MR remotion lift fixture-film Film --scene 2 --name fixture-end --project "$PR" --json | J '"lifted again: %s" % (d["data"]["result"]["message"] if d.get("ok") else d["error"])'
MR motion list --json | CHECK '
rows = {(r["name"], r["state"]) for r in d["data"]["components"]}
print("graphics now: %s" % sorted(rows))
assert ("fixture-film-theme", "waiting") in rows and not any(n == "fixture-end" and s == "waiting" for n, s in rows), "only the library needs a person: the end card itself is unchanged"'
LV2=$(MR motion list --json | J '[r["version"] for r in d["data"]["components"] if r["name"] == "fixture-film-theme" and r["state"] == "waiting"][0]')
api motion.approve "{\"ref\":\"$LV2\",\"note\":\"blue\"}" | CHECK '
o = d["data"]
print("approving the new theme once: %s" % o["summary"])
assert any(m["projectId"] == "'"$PR"'" for m in o.get("moved") or []), "the latest cut moved to it"'
MR cut show "$PR" --json | J '"the cut sheet now: %s" % [l.strip() for l in d["data"]["sheet"].splitlines() if "fixture-end" in l]'

step "Remotion sources (refreshing and sending changes back): a refresh from the project's own steps (planned first, started by a person, kept apart, its outputs a version a person approves) and the changes sent back to its repository as a new branch (D173–D176)"
# A copy of the fixture film that builds itself (scripts/make-refresh-fixture.sh): "make-data" writes the number its
# title shows and a picture, "voice" makes a fake voice line with a fake key (no network). It is a git repository
# with a remote (a bare one, file://), so its changes can go back.
RR="$(dirname "$H")/refresh-film"; RREMOTE="$(dirname "$H")/refresh-remote.git"
RORIG=$(scripts/make-refresh-fixture.sh "$RR" "$RREMOTE")
MR import remotion "$RR" --project "$PR" --name refresh-film --wait --json | J '"brought in: %s" % d["data"]["result"]["message"]'
api remotion.approve "{\"ref\":\"refresh-film@1\",\"projectId\":\"$PR\",\"keepCuts\":true}" | J '"a person approves version 1: %s" % d["data"]["summary"]'
# A made-up key on this computer (Settings → Integrations; here the test home's .env).
FAKEKEY="sk_$(printf 'f00d%.0s' 1 2 3 4 5 6 7 8 9 10)"; printf 'METARUSH_ELEVENLABS_API_KEY=%s\n' "$FAKEKEY" >> "$H/.env"
RFLAGS=(--step "npm run make-data" --step "npm run voice" --key elevenlabs=ELEVENLABS_API_KEY --output public/ --output src/generated/)
MR remotion refresh plan refresh-film --project "$PR" "${RFLAGS[@]}" --json > "$H/refresh-plan.json"
CHECK '
p = d["data"]
print("the plan: %s" % p["message"])
print("  steps: %s" % [s["run"] + " → " + (s.get("command") or "") for s in p["steps"]])
print("  keys: %s; where it may write: %s; places it may reach: %s" % (["%s as %s (%s)" % (k["title"], k["env"], k["masked"]) for k in p["keys"]], p["outputs"], [h["host"] for h in p["hosts"]]))
print("  kept apart: %s" % p["apart"])
assert p["ready"] and [s["run"] for s in p["steps"]] == ["npm run make-data", "npm run voice"] and p["keys"][0]["read"] and p["keys"][0]["masked"].startswith("••••")
assert any(s["name"] == "render" and s.get("makesVideos") for s in p["scripts"]), "a script that renders is marked"
assert "'"$FAKEKEY"'" not in json.dumps(p), "the plan never shows the key"' < "$H/refresh-plan.json"
RPLAN=$(J 'd["data"]["fingerprint"]' < "$H/refresh-plan.json")
MR remotion refresh start refresh-film --project "$PR" "${RFLAGS[@]}" --json </dev/null | J '"starting it from a script, without a person → %s" % d["error"]["code"]' || true
apiAny remotion.refresh.start "{\"source\":\"refresh-film\",\"projectId\":\"$PR\",\"steps\":[\"npm run make-data\"],\"outputs\":[\"public/\"],\"plan\":\"$RPLAN\"}" | J '"a person starting a plan they did not see → %s" % d["error"]["code"]'
RSTART=$(api remotion.refresh.start "{\"source\":\"refresh-film\",\"projectId\":\"$PR\",\"steps\":[\"npm run make-data\",\"npm run voice\"],\"keys\":[{\"provider\":\"elevenlabs\",\"env\":\"ELEVENLABS_API_KEY\"}],\"outputs\":[\"public/\",\"src/generated/\"],\"plan\":\"$RPLAN\"}")
echo "$RSTART" | J '"a person starts it in the dashboard: %s" % d["data"]["message"]'
MR job wait "$(echo "$RSTART" | J 'd["data"]["jobId"]')" --json >/dev/null || true
MR remotion refresh runs refresh-film --project "$PR" --json | CHECK '
r = d["data"]["refreshes"][0]
print("refresh %d: %s — %s" % (r["number"], r["stateLabel"], r.get("summary") or r.get("error")))
print("  steps: %s; left out: %s" % (["%s %s" % (s["run"], "ok" if s["ok"] else "failed") for s in r["stepRuns"]], ["%s (%s)" % (l["path"], l["why"]) for l in r["leftOut"]]))
assert r["state"] == "done" and r["versionNumber"] == 2 and all(s["ok"] for s in r["stepRuns"]), r
assert any(l["path"] == "notes.txt" for l in r["leftOut"]), "a file written outside its places is left out"'
MR remotion refresh log refresh-film 1 --project "$PR" > "$H/refresh-log.txt"
python3 -I -c "import sys;t=open(sys.argv[1]).read();print('what it printed, key hidden: %s' % [l for l in t.splitlines() if 'key' in l]);sys.exit(not ('[redacted]' in t and sys.argv[2] not in t and 'made the data' in t))" "$H/refresh-log.txt" "$FAKEKEY"
MR remotion show refresh-film@2 --project "$PR" --json | CHECK '
v = d["data"]["version"]
print("version 2, %s: %s" % (v["kind"], (v.get("check") or {}).get("summary")))
print("  before/after stills: %d" % len(d["data"]["stills"]))
assert v["kind"] == "refresh" and v["state"] == "waiting" and (v.get("check") or {}).get("ok"), v.get("check")
assert "Title" in ((v.get("check") or {}).get("changed") or []), "the title shows the new number"
files = d["data"]["files"]
assert "src/generated/voice.json" in files and "notes.txt" not in files'
MR todos list --kind remotion_approval --json | J '"To-dos: %s" % [t["question"] for t in d["data"]["todos"] if t["state"] == "open"][:3]'
api remotion.approve "{\"ref\":\"refresh-film@2\",\"projectId\":\"$PR\",\"keepCuts\":true}" | J '"a person approves the refreshed version: %s" % d["data"]["summary"]'
# Sending the changes back: a plan in plain words, then a new branch on the project's own remote (never main, never forced).
MR remotion push plan refresh-film --project "$PR" --json > "$H/push-plan.json"
CHECK '
p = d["data"]
print("sending back: %s" % p["message"])
print("  changes: %s" % ["%s %s" % (c["change"], c["path"]) for c in p["changes"]])
assert p["ready"] and p["target"]["kind"] == "git" and p["branch"] == "metarush/refresh-film-v2", p
assert {c["path"] for c in p["changes"]} >= {"src/generated/stats.ts", "src/generated/voice.json", "public/refresh/chart.png"} and not any(c["path"] == "notes.txt" for c in p["changes"])' < "$H/push-plan.json"
apiAny remotion.push.plan "{\"source\":\"refresh-film\",\"projectId\":\"$PR\",\"branch\":\"main\"}" | J '"to the main branch → %s" % d["data"]["problems"]'
MR remotion push start refresh-film --project "$PR" --json </dev/null | J '"sending from a script, without a person → %s" % d["error"]["code"]' || true
PPLAN=$(J 'd["data"]["fingerprint"]' < "$H/push-plan.json")
PSTART=$(api remotion.push.start "{\"source\":\"refresh-film\",\"projectId\":\"$PR\",\"plan\":\"$PPLAN\"}")
echo "$PSTART" | J '"a person sends it: %s" % d["data"]["message"]'
MR job wait "$(echo "$PSTART" | J 'd["data"]["jobId"]')" --json >/dev/null || true
MR remotion push list refresh-film --project "$PR" --json | CHECK '
x = d["data"]["pushes"][0]
print("sent: version %d to %s as %s (%s)" % (x["versionNumber"], x["target"]["where"], x["branch"], x["stateLabel"]))
assert x["state"] == "done" and len(x["commit"]) == 40, x'
RTREE=$(git --git-dir="$RREMOTE" ls-tree -r --name-only refs/heads/metarush/refresh-film-v2)
python3 -I -c "import sys;t=sys.argv[1].split();print('the branch holds: %s' % [f for f in t if f.startswith(('src/generated', 'public/refresh', 'CLAUDE'))]);sys.exit(not ('src/generated/voice.json' in t and 'CLAUDE.md' in t and 'notes.txt' not in t))" "$RTREE"
[ "$(git --git-dir="$RREMOTE" rev-parse refs/heads/main)" = "$RORIG" ] && echo "main is where it was; the branch's parent is the original commit: $([ "$(git --git-dir="$RREMOTE" rev-parse 'refs/heads/metarush/refresh-film-v2^')" = "$RORIG" ] && echo yes)"
[ -z "$(git -C "$RR" status --porcelain)" ] && echo "the project's own folder is unchanged"

step "text fixes: a spelling list on a new transcript; a word retyped and two words joined, keeping their times and the cut sheet's quotes; the fixes wait for one new version and its review copy while the old version renders unchanged; the captions file follows the fixes (D145–D149)"
TF="$(dirname "$H")/text-fixes"; mkdir -p "$TF"
# A clip of names made with the Mac's voice (like the fixtures): the transcriber writes "Katelyn" as "Caitlin" (or
# another usual spelling), never as the spelling list's "Kaytlin": only how it is heard can fix it.
say -v Samantha -o "$TF/names.aiff" -- "We met Katelyn at the Maple Grove on Elmwood last week. Ask Katelyn about the garden, and Katelyn will tell you everything."
ffmpeg -hide_banner -loglevel error -y -f lavfi -i "color=c=0x2a6f4e:s=640x360:r=30" -i "$TF/names.aiff" -shortest -c:v libx264 -pix_fmt yuv420p -c:a aac -ar 48000 "$TF/names.mp4"
PT=$(MR project create "Acceptance text fixes" --json | J 'd["data"]["project"]["id"]')
# Its spelling list is its own (Content Settings, D193): new projects use the default's.
MR content own --project "$PT" --json | J '"Content Settings: " + d["data"]["changed"]'
MR transcript spelling add Kaytlin --project "$PT" --heard-as Caitlin --heard-as Kaitlyn --heard-as Katelyn --json | J '"the spelling list first: " + d["data"]["summary"]'
MR ingest "$TF/names.mp4" --project "$PT" --wait --json | J '"added=%d jobs=%s" % (d["data"]["ingest"]["ingested"], sorted({j["state"] for j in d["data"]["jobs"]}))'
NA=$(MR asset list "$PT" --json | J 'd["data"]["assets"][0]["id"]')
MR job list --asset "$NA" --type transcribe --json | J '"the transcriber was told to expect %s word(s); %s place(s) corrected after" % (d["data"]["jobs"][0]["result"].get("hints"), d["data"]["jobs"][0]["result"].get("spelled", 0))'
MR footage transcript "$NA" --words --json > "$TF/t0.json"
CHECK '
t = " ".join(s["text"] for s in d["data"]["segments"])
print("the new transcript says: %s" % t)
assert t.count("Kaytlin") == 3 and not any(x in t for x in ("Caitlin", "Kaitlyn", "Katelyn")), "the spelling list on a new transcript"' < "$TF/t0.json"
read -r HF HT TW EN < <(python3 -I -c '
import json, sys
ws = [w for s in json.load(open(sys.argv[1]))["data"]["segments"] for w in s["words"]]
core = lambda w: w["w"].strip(".,!?")
i = [k for k in range(len(ws) - 1) if core(ws[k]) == "Maple" and core(ws[k + 1]) == "Grove"][0]
t = [w for w in ws if core(w) == "tell"][0]
print(ws[i]["f"], ws[i + 1]["t"], (t["f"] + t["t"]) / 2, round(ws[-1]["t"] + 0.05, 3))' "$TF/t0.json")
cat > "$TF/plan.json" <<EOF
{"projectId":"$PT","instruction":"Where they met, then what Kaytlin knows; captioned","spec":{"schemaVersion":1,"title":"Meeting Kaytlin",
 "output":{"preset":"vertical-1080p","fps":30,"platform":"youtube"},
 "clips":[{"id":"met","assetId":"$NA","in":0,"out":$HT},{"id":"garden","assetId":"$NA","in":$HT,"out":$EN}],
 "captions":{"enabled":true,"style":"clean","position":"lower","maxWordsPerLine":5}}}
EOF
TOK=$(MR edit check --plan "$TF/plan.json" --json | J 'd["data"]["confirmToken"]')
RT1=$(MR edit apply --plan "$TF/plan.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
MR revision plan "$RT1" --json > "$TF/r1-before.json"
J '"version 1 captions: %s" % " / ".join(l["text"] for l in d["data"]["captions"]["lines"])' < "$TF/r1-before.json"
MR transcript fix "$NA" --at "$TW" "show" --project "$PT" --json | J '"a word retyped: " + d["data"]["summary"]'
MR transcript find "Maple Grove" --project "$PT" --with Maplegrove --json | J '"looked first: " + d["data"]["summary"]'
MR transcript replace "Maple Grove" "Maplegrove" --project "$PT" --json | J '"find and replace: " + d["data"]["summary"]'
MR footage transcript "$NA" --words --json | CHECK '
ws = [w for s in d["data"]["segments"] for w in s["words"]]
h = [w for w in ws if w["w"].strip(".,") == "Maplegrove"][0]
s = [w for w in ws if w["w"].strip(".,") == "show"][0]
print("“Maplegrove” is said %.3f–%.3f s (“Maple” began at '"$HF"', “Grove” ended at '"$HT"'); “show” %.3f–%.3f s" % (h["f"], h["t"], s["f"], s["t"]))
assert abs(h["f"] - '"$HF"') < 0.0005 and abs(h["t"] - '"$HT"') < 0.0005 and h.get("x"), "a join keeps the time both words took"
assert s["f"] < '"$TW"' < s["t"] and s.get("x"), "a retyped word keeps its time"'
MR cut show "$PT" --out "$TF/fixed.cut.md" >/dev/null
python3 -I -c "import re,sys;p=sys.argv[1];s=open(p).read();t=re.sub(r'^(1\. met: \S+ \S+) → \S+', r'\1 → \"Maplegrove\"', s, count=1, flags=re.M);assert t!=s;open(p,'w').write(t)" "$TF/fixed.cut.md"
MR cut check "$TF/fixed.cut.md" --json | CHECK '
c = d["data"]
print("a cut sheet quoting the joined word: %s; changes: %s" % ([(a["phrase"], a["time"]) for a in c["anchors"]], c["changes"] or "none"))
assert c["valid"] and c["noChanges"], "a quote of the joined word lands where the two words ended"'
MR render list --project "$PT" --json | J '"renders so far: %d (nothing renders by itself)" % len(d["data"]["renders"])'
MR transcript pending list "$PT" --json | CHECK '
p = d["data"]
print("waiting for a new version: %s" % p["summary"])
assert p["count"] == 2, "the retyped word and the join wait (the spelling list wrote the transcript before the version)"'
MR transcript pending apply "$PT" --json > "$TF/apply.json"
J '"one click: " + d["data"]["summary"]' < "$TF/apply.json"
RT2=$(J 'd["data"]["revision"]["id"]' < "$TF/apply.json"); RD2=$(J 'd["data"]["renderId"]' < "$TF/apply.json")
MR job wait "$(MR render status "$RD2" --json | J 'd["data"]["render"]["jobId"]')" --json >/dev/null
MR revision plan "$RT1" --json > "$TF/r1-after.json"
python3 -I -c "import json,sys;a,b=(json.load(open(f))['data']['captions'] for f in sys.argv[1:3]);print('version 1 keeps its words after the fixes: %s' % (a == b));sys.exit(a != b)" "$TF/r1-before.json" "$TF/r1-after.json"
MR revision plan "$RT2" --json | CHECK '
t = " / ".join(l["text"] for l in d["data"]["captions"]["lines"])
print("version 2 captions: %s" % t)
assert "Maplegrove" in t and "show you" in t and "Maple Grove" not in t and "tell" not in t, "the fixes are in the new version"'
RD1=$(MR render submit --revision "$RT1" --quality review --wait --json | J 'd["data"]["render"]["id"]')
MR render list --project "$PT" --json | J '"renders now: %s" % sorted("version %d %s" % (r["revisionNumber"], r["quality"]) for r in d["data"]["renders"])'
MR qa run "$RD1" --wait --json | CHECK '
f = [x["message"] for x in d["data"]["report"]["findings"] if x["kind"] == "caption_fixed"]
print("version 1 rendered unchanged; its quality check says: %s" % f)
assert len(f) == 2 and any("Maple Grove" in x for x in f) and any("tell" in x for x in f), "the old version keeps its words and says so"'
MR qa run "$RD2" --wait --json | CHECK '
r = d["data"]["report"]
print("version 2: %s" % [x["message"] for x in r["findings"] if x["kind"].startswith("caption_")] or "the captions have every fix")
assert not [x for x in r["findings"] if x["kind"] == "caption_fixed"], "the new version has every fix"'
MR captions file "$PT" --version r1 --format srt > "$TF/v1-now.srt"; MR captions file "$PT" --version r1 --version-words > "$TF/v1-own.srt"
python3 -I -c "import sys;now,own=(open(f).read() for f in sys.argv[1:3]);print('version 1 captions file: with today\'s words says Maplegrove=%s; with its own words says Maple Grove=%s' % ('Maplegrove' in now, 'Maple Grove' in own));sys.exit(not ('Maplegrove' in now and 'Maple Grove' in own))" "$TF/v1-now.srt" "$TF/v1-own.srt"

step "call recordings: a speaker-view call's picture switches found to the frame; the flashes at clip edges flagged and trimmed at the switch; the printed names covered by one name bar for the whole video that changes on the exact frame (D160–D163)"
# call-speaker-view: the picture switches at exactly 6, 12 and 18 s, with the call app's name tile at the bottom
# left (tests/fixtures/truth/call-speaker-view.json). Printed names need the on-screen index (a Mac).
PCALL=$(MR project create "Acceptance call recording" --json | J 'd["data"]["project"]["id"]')
MR project rules add "$PCALL" "First names only" --json | J '"a rule: %s (the quality check measures: %s)" % (d["data"]["rule"]["text"], d["data"]["rule"]["measure"])'
MR ingest tests/fixtures/gen/call-speaker-view.mp4 --project "$PCALL" --wait --json | J '"the call processed: %s" % sorted({"%s %s" % (j["type"], j["state"]) for j in d["data"]["jobs"]})'
CALL=$(MR asset list "$PCALL" --json | J 'd["data"]["assets"][0]["id"]')
MR footage find-switches "$CALL" --wait --json > "$H/switches.json"
CHECK '
s = d["data"]
ats = [round(x["at"], 3) for x in s["switches"]]
assert ats == [6.0, 12.0, 18.0], ("switches to the frame", ats)
print("switches: %s (precision %s); shots: %s; printed names: %s" % (ats, s.get("precision", 0), [(round(x["from"], 2), x.get("person", ""), x.get("by", "")) for x in s["shots"]], s.get("names")))' < "$H/switches.json"
NAMES=$(J '"yes" if d["data"].get("names") else "no"' < "$H/switches.json")
if [ "$NAMES" = "yes" ]; then
  MR footage visuals "$CALL" --json | CHECK '
b = sorted({round(s["from"], 3) for s in d["data"]["shots"]} | {round(s["to"], 3) for s in d["data"]["shots"]})
assert 6.0 in b and 12.0 in b and 18.0 in b, ("the on-screen readout follows the switches", b)
print("what is on screen, shot by shot on the switches: %s" % [l["line"] for l in d["data"]["lines"]][:4])'
fi
cat > "$H/call-plan.json" <<EOF
{"projectId":"$PCALL","instruction":"The call, three answers","spec":{"schemaVersion":1,"title":"Call","output":{"preset":"landscape-1080p","fps":30},
 "clips":[{"id":"hello","assetId":"$CALL","in":0.5,"out":5.9},{"id":"reply","assetId":"$CALL","in":5.8,"out":11.8},{"id":"again","assetId":"$CALL","in":12.1,"out":18.25}]}}
EOF
MR edit check --plan "$H/call-plan.json" --json | CHECK '
w = [x["message"] for x in d["data"]["warnings"] if x["code"] == "edge_flash"]
assert len(w) == 2, w
print("the edit check: %s" % w)'
TOK=$(MR edit check --plan "$H/call-plan.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$H/call-plan.json" --confirm "$TOK" --json >/dev/null
TOK=$(MR cut flashes "$PCALL" --dry-run --json | J 'd["data"]["check"]["confirmToken"]')
MR cut flashes "$PCALL" --confirm "$TOK" --json | CHECK '
o = d["data"]
assert o.get("saved") and len(o["flashes"]) == 2, o.get("summary")
print("trim the flashes: %s" % o["summary"])'
RC1=$(MR revision list "$PCALL" --json | J 'd["data"]["revisions"][0]["id"]')
MR revision show "$RC1" --json | CHECK '
c = {x["id"]: x for x in d["data"]["spec"]["clips"]}
assert 6.0 <= c["reply"]["in"] < 6.0 + 1/30 and 18.0 - 2/30 < c["again"]["out"] <= 18.0, (c["reply"]["in"], c["again"]["out"])
print("trimmed at the switch: reply starts at %.3f s, again ends at %.3f s of the footage" % (c["reply"]["in"], c["again"]["out"]))'
if [ "$NAMES" = "yes" ]; then
  TOK=$(MR cut names "$PCALL" --dry-run --json | J 'd["data"]["check"]["confirmToken"]')
  MR cut names "$PCALL" --confirm "$TOK" --json | J '"cover the printed names: %s" % d["data"]["summary"]'
  RC2=$(MR revision list "$PCALL" --json | J 'd["data"]["revisions"][0]["id"]')
  MR revision plan "$RC2" --json | SW="$H/switches.json" CHECK '
import os
sw = json.load(open(os.environ["SW"]))["data"]
p = d["data"]
clips = {c["id"]: c for c in p["clips"]}
bars = [o for o in p["overlays"] if o["template"] == "name.bar"]
assert len(bars) == 1, bars
bar = bars[0]
segs = [(s["from"], s["params"]["name"]) for s in bar["segments"]]
want = [0, clips["reply"]["from"] - bar["from"], clips["again"]["from"] - bar["from"]]
assert [f for f, _ in segs] == want, ("the name changes on the frame the picture switches", segs, want)
assert all(" " not in n for _, n in segs), ("first names only", segs)
cover = bar["params"]["cover"]; box = sw["names"]["box"]
assert cover["y"] <= box["y"] + 0.005 and cover["y"] + cover["h"] >= box["y"] + box["h"] - 0.005 and cover["x"] <= box["x"] + 0.005, (cover, box)
print("the name bar lasts the whole video (frames %d–%d), changes at frames %s: %s; it covers the printed names (%s)" % (bar["from"], bar["from"] + bar["durationInFrames"], want, [n for _, n in segs], cover))'
  MR cut show "$PCALL" --out "$H/call.cut.md" --json >/dev/null
  grep -E "name.bar for the whole video|Change: at " "$H/call.cut.md" | sed 's/^/  sheet: /'
  MR cut check "$H/call.cut.md" --json | J '"the sheet reads back unchanged: valid=%s, changes=%s" % (d["data"]["valid"], d["data"]["changes"] or "none")'
  CREND=$(MR render submit --revision "$RC2" --quality review --wait --json | J 'd["data"]["render"]["id"]')
  MR qa run "$CREND" --wait --json | CHECK '
r = d["data"]["report"]
bad = [f["kind"] for f in r["findings"] if f["kind"] in ("edge_flash", "printed_names", "name_bar_mismatch", "rule_broken")]
assert not bad, [f["message"] for f in r["findings"] if f["kind"] in bad]
print("QA of the call: %s; call footage and the rules: no flashes, no printed names showing, the right names, first names only" % r["verdict"])'
else
  echo "printed names need the on-screen index (a Mac): the name bar part is skipped here"
fi

step "test frames in the video's shape: a motion graphic used in a widescreen cut is drawn widescreen (D164)"
MR motion check "$MF/motion/route-map" --project "$SP" --no-preview --json | CHECK '
fr = d["data"]["frames"]
shapes = sorted({f.get("shape", "") for f in fr})
assert shapes == ["widescreen"], shapes
import struct
w, h = struct.unpack(">II", open(fr[0]["path"], "rb").read()[16:24])
assert w > h, (w, h)
print("the slides project (widescreen) → test frames %s, %dx%d" % (shapes, w, h))'

step "Keep this? cards: a link the person typed, a logo they attached, a rule and a sign-off Claude heard — each asked once and kept in the project; Not now is never asked again; the site's logo and colours become the brand kit; a reminder of the sign-off at approval (D165, D166)"
KP=$(MR project create "Acceptance keep" --json | J 'd["data"]["project"]["id"]')
MR asset link "$B" --project "$KP" --json >/dev/null
printf '{"projectId":"%s","instruction":"first cut","spec":{"schemaVersion":1,"title":"Keep","output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"open","assetId":"%s","in":0,"out":3}]}}' "$KP" "$B" > "$H/keep-plan.json"
TOK=$(MR edit check --plan "$H/keep-plan.json" --json | J 'd["data"]["confirmToken"]')
KR1=$(MR edit apply --plan "$H/keep-plan.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
python3 -I -m http.server "$SITE_PORT" --bind 127.0.0.1 --directory "$SITE" >/dev/null 2>&1 & WEB=$!
for _ in $(seq 1 40); do curl -sf "http://127.0.0.1:$SITE_PORT/" >/dev/null && break; sleep 0.25; done
ffmpeg -hide_banner -loglevel error -y -f lavfi -i color=c=0x1D4E89:s=240x120 -frames:v 1 "$RF/fernbrook-logo.png"
python3 -I - "$FAKEC.json" "$KP" <<'PY'
import json, sys
path, project = sys.argv[1:3]
p = " --project " + project + " --json"
def denied(cmd):
    return {"tool": {"name": "Bash", "denied": True, "input": {"command": cmd}}}
json.dump({"turns": [
    {"match": "open day video", "costUsd": 0.002, "steps": [
        {"text": "I'll keep what you told me for the project."},
        denied("metarush keep rule 'First names only on screen'" + p),
        denied("metarush keep signoff 'Lena OKs the final version'" + p),
        {"text": "I asked to keep the rule and the sign-off."}]},
    {"match": "Once more", "steps": [
        denied("metarush keep rule 'first names only, on screen'" + p),
        {"text": "That rule is kept already."}]},
]}, open(path, "w"))
PY
CONV=$(MR assistant ask "We are making an open day video for a garden club. Their site is http://127.0.0.1:$SITE_PORT/ and this is their logo." --project "$KP" --attach "$RF/fernbrook-logo.png" --json | J 'd["data"]["conversation"]["id"]')
waitcards() { for _ in $(seq 1 120); do n=$(MR assistant conversations show "$CONV" --json | J 'len(d["data"].get("actions") or []) if d["data"]["state"] == "idle" else -1'); [ "$n" -ge "$1" ] && return 0; sleep 0.5; done; echo "the cards did not come"; return 1; }
waitcards 4
MR assistant conversations show "$CONV" --json > "$H/keep-conv.json"
CHECK '
cards = d["data"]["actions"]
for c in cards:
    print("  card: %r — %s (from %s)" % (c["title"], c["keeping"]["does"], c["keeping"]["from"]))
kinds = sorted((c["kind"], c["keeping"]["kind"]) for c in cards)
assert kinds == [("keep", "picture"), ("keep", "source"), ("keep rule", "rule"), ("keep signoff", "signoff")], kinds
assert all(c["state"] == "pending" and c["op"] == "keep.add" for c in cards)' < "$H/keep-conv.json"
card() { J '[c["id"] for c in d["data"]["actions"] if c["keeping"]["kind"] == "'"$1"'"][0]' < "$H/keep-conv.json"; }
for k in rule signoff source; do
  api assistant.act "{\"id\":\"$CONV\",\"actionId\":\"$(card $k)\",\"quiet\":true}" | J '"Keep → %s: %s" % (d["data"]["action"]["state"], d["data"]["action"]["result"])'
done
api assistant.act "{\"id\":\"$CONV\",\"actionId\":\"$(card picture)\",\"dismiss\":true,\"quiet\":true}" | J '"Not now → %s" % d["data"]["action"]["state"]'
MR keep list --project "$KP" --json | CHECK '
st = sorted((i["kind"], i["state"]) for i in d["data"]["items"])
assert st == [("picture", "declined"), ("rule", "kept"), ("signoff", "kept"), ("source", "kept")], st
print("kept in the project: %s" % d["data"]["summary"])'
MR project rules list "$KP" --json | J '"rules: %s" % [(r["text"], r["measure"]) for r in d["data"]["rules"]]'
KJOBS=$(MR assistant conversations show "$CONV" --json | J '" ".join(j for c in d["data"]["actions"] if c["keeping"]["kind"] == "source" for j in (c.get("work") or {}).get("jobIds", []))')
METARUSH_PAGES_ALLOW_LOCAL=1 MR job wait $KJOBS --json >/dev/null
waitcards 5
MR assistant conversations show "$CONV" --json > "$H/keep-conv.json"
CHECK '
new = [c for c in d["data"]["actions"] if c["keeping"]["kind"] == "picture" and c["state"] == "pending"]
assert len(new) == 1 and new[0]["keeping"].get("colors") and new[0]["image"].startswith("/media/resources/"), new
print("the snapshot found the site'"'"'s logo: %r with colours %s" % (new[0]["title"], new[0]["keeping"]["colors"]))' < "$H/keep-conv.json"
SITECARD=$(J '[c["id"] for c in d["data"]["actions"] if c["keeping"]["kind"] == "picture" and c["state"] == "pending"][0]' < "$H/keep-conv.json")
api assistant.act "{\"id\":\"$CONV\",\"actionId\":\"$SITECARD\",\"quiet\":true}" | J '"Keep the logo → %s: %s" % (d["data"]["action"]["state"], d["data"]["action"]["result"])'
MR brand show --project "$KP" --json | J '"the project'"'"'s brand kit: logo %s, colours %s, font %s" % ((d["data"].get("logo") or {}).get("name"), [d["data"]["brand"].get(k) for k in ("primary", "accent", "text", "background")], d["data"]["brand"].get("font"))'
{ kill "$WEB" && wait "$WEB"; } 2>/dev/null || true
MR assistant continue "$CONV" "Once more" --project "$KP" --json >/dev/null
for _ in $(seq 1 120); do [ "$(MR assistant conversations show "$CONV" --json | J 'd["data"]["state"] + str(d["data"]["turnCount"])')" = "idle2" ] && break; sleep 0.5; done
sleep 1
MR assistant conversations show "$CONV" --raw --json | CHECK '
cv = d["data"]
second = [c for c in cv["actions"] if c["turn"] == 2]
assert not second, ("the same rule is not asked again", [c["title"] for c in second])
sent = cv["turns"][1]["sent"]
assert "This project'"'"'s rules" in sent and "First names only on screen" in sent and "Already offered to keep" in sent, sent
print("asked once: no card for the same rule; Claude reads the rules and what was offered: %r" % [l for l in sent.splitlines() if l.startswith("- This project")][0][:110])'
api revision.approve "{\"id\":\"$KR1\"}" | CHECK '
r = d["data"]
assert r.get("reminder", "").startswith("Still waiting for a sign-off"), r
print("approved, with a reminder: %s" % r["reminder"])'
step "sources: a descriptive name at import, renaming keeps the file name; fixes on a shaky clip and a humming voice measured before and after, the fixed copy used from then on, a saved version unchanged, the original back on untick (D150–D153)"
# The better steadying (vid.stab) comes from a pinned download, which this run never fetches: give it a copy with
# METARUSH_STABILIZER_ZIP (the archive docs/sources.md pins), else MetaRush steadies with ffmpeg's basic method and says so.
export METARUSH_STABILIZER_ZIP="${METARUSH_STABILIZER_ZIP:-$H/no-stabilizer-download.zip}"
SRCF="$(dirname "$H")/sources"; mkdir -p "$SRCF"
# A hand-held camera clip (a camera's own file name, the day it was recorded in its tags) and a voice memo with a hum.
ffmpeg -nostdin -loglevel error -y -f lavfi -i "mandelbrot=s=1440x820:end_pts=1" -frames:v 1 "$SRCF/scene.png"
ffmpeg -nostdin -loglevel error -y -loop 1 -framerate 30 -i "$SRCF/scene.png" -f lavfi -i "sine=frequency=330:duration=8" -t 8 \
  -vf "crop=1280:720:x='80+26*sin(n*1.9)*cos(n*0.7)':y='50+20*sin(n*2.3+1)',format=yuv420p" -c:v libx264 -crf 18 -c:a aac -shortest \
  -metadata creation_time=2026-10-12T15:40:00Z "$SRCF/IMG_4821.MOV"
ffmpeg -nostdin -loglevel error -y -f lavfi -i "aevalsrc=0.25*sin(2*PI*220*t)*(0.6+0.4*sin(2*PI*3*t))+0.08*sin(2*PI*60*t)+0.05*sin(2*PI*120*t)+0.03*sin(2*PI*180*t):s=48000:d=10" \
  -c:a pcm_s16le "$SRCF/kitchen-voice.wav"
PS=$(MR project create "Acceptance sources" --json | J 'd["data"]["project"]["id"]')
MR ingest "$SRCF/IMG_4821.MOV" "$SRCF/kitchen-voice.wav" --project "$PS" --wait --json >/dev/null
SH=$(MR asset list "$PS" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="IMG_4821.MOV"][0]')
VM=$(MR asset list "$PS" --json | J '[a["id"] for a in d["data"]["assets"] if a["name"]=="kitchen-voice.wav"][0]')
MR asset list "$PS" --json | CHECK '
a = {x["name"]: x for x in d["data"]["assets"]}
cam, voice = a["IMG_4821.MOV"], a["kitchen-voice.wav"]
print("named at import: %r (by %s); a recording MetaRush can tell nothing about keeps its file name: %r" % (cam["title"], cam.get("titleBy"), voice["title"]))
assert cam["title"].startswith("Video from 12 Oct 2026") and cam.get("titleBy") == "metarush", "a camera file is named by the day it was recorded"
assert voice["title"] == "kitchen-voice"'
MR asset update "$VM" --title "Kitchen voice memo" --json >/dev/null
MR asset card "$VM" --json | CHECK '
c = d["data"]
print("renamed: %r, its file %r stays on the card (%s)" % (c["title"], c["fileName"], c["namedWords"]))
assert c["title"] == "Kitchen voice memo" and c["fileName"] == "kitchen-voice.wav" and c["titleBy"] == "person"'
# MetaRush measures each clip once its first look is done (a worker's inspect job); here, measured in this terminal.
MR asset fixes "$SH" --look --wait --json >/dev/null
MR asset fixes "$VM" --look --wait --json >/dev/null
MR asset fixes "$SH" --json | CHECK '
v = d["data"]
f = {r["id"]: r for r in v["fixes"]}
print("%s: %s; marked: %s" % (v["title"], v["kindWords"], ["%s (%s; %s)" % (r["label"], r["why"], r["measured"]) for r in v["fixes"] if r["fits"]]))
assert v["kind"] == "camera" and f["steady"]["fits"], "the shake is found"'
MR asset fixes "$VM" --json | CHECK '
v = d["data"]
f = {r["id"]: r for r in v["fixes"]}
print("%s: %s; marked: %s" % (v["title"], v["kindWords"], ["%s (%s; %s)" % (r["label"], r["why"], r["measured"]) for r in v["fixes"] if r["fits"]]))
assert v["kind"] == "voice" and f["hum"]["fits"], "the hum is found"'
# A cut that plays the shaky clip, saved before any fix.
cat > "$H/splan.json" <<EOF
{"projectId":"$PS","instruction":"the walk","note":"first cut of the walk",
 "spec":{"schemaVersion":1,"title":"Walk","output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"walk","assetId":"$SH","in":0.5,"out":7.5}]}}
EOF
TOK=$(MR edit check --plan "$H/splan.json" --json | J 'd["data"]["confirmToken"]')
SV1=$(MR edit apply --plan "$H/splan.json" --confirm "$TOK" --json | J 'd["data"]["revision"]["id"]')
SV1HASH=$(MR revision show "$SV1" --json | J 'd["data"]["summary"]["specHash"]')
MR asset fixes "$SH" --tick steady --wait --json | CHECK '
v = d["data"]
m = {x["fix"]: x for x in v["copy"]["measures"]}["steady"]
print("steadied: %s (%s); notes: %s" % (m["words"], v["usingWords"], v["copy"].get("notes") or "none"))
assert v["using"] == "fixed" and m["after"] < m["before"] / 2, "much steadier, and the clip uses the fixed copy"'
MR asset fixes "$VM" --tick hum --wait --json | CHECK '
v = d["data"]
m = {x["fix"]: x for x in v["copy"]["measures"]}["hum"]
print("hum removed: %s (%s)" % (m["words"], v["usingWords"]))
assert v["using"] == "fixed" and m["after"] < m["before"] - 10'
FIXED=$(MR asset fixes "$SH" --json | J 'd["data"]["copy"]["sha256"]')
MR asset show "$SH" --json | CHECK '
a = d["data"]["asset"] if "asset" in d["data"] else d["data"]
print("the original is kept (%s…); what plays now is the fixed copy (%s…), with its own preview copy: %s" % (a["sha256"][:12], a["fixes"]["copy"]["sha256"][:12], "proxy" in (a.get("variants") or {})))
assert a["fixes"]["copy"]["sha256"] == "'"$FIXED"'" and a["sha256"] != "'"$FIXED"'"'
MR job list --json | J '"looked at again with the fixed copy: %s" % sorted({j["type"] for j in d["data"]["jobs"] if (j.get("payload") or {}).get("reason") == "fixed"})'
# The version saved before the fix plays the original; the card offers the copy as a new version.
MR revision show "$SV1" --json | CHECK '
s = d["data"]["spec"]
print("version 1 (saved before the fix) pins: %s" % (s.get("fixed") or "nothing: it plays the original"))
assert not s.get("fixed") and d["data"]["summary"]["specHash"] == "'"$SV1HASH"'"'
MR asset card "$SH" --json | J '"the card offers: %s" % [c["offer"] for c in d["data"]["fixes"]["cuts"] if c.get("offer")]'
UTOK=$(MR cut use-fixed "$PS" "$SH" --json | J 'd["data"]["check"]["confirmToken"]')
SV2=$(MR cut use-fixed "$PS" "$SH" --confirm "$UTOK" --json | J 'd["data"]["applied"]["revision"]["id"]')
MR cut show "$PS" --json | J '"version 2 (%s): %s" % (d["data"].get("revision", {}).get("note", "") if isinstance(d["data"].get("revision"), dict) else "", [l.strip() for l in d["data"]["sheet"].splitlines() if "sha256:'"$FIXED"'" in l])'
MR revision plan "$SV2" --json | CHECK '
p = d["data"]["plan"] if "plan" in d["data"] else d["data"]
print("its frame-by-frame plan plays the fixed copy: %s" % (p.get("fixed") or {}).get("'"$SH"'", "")[:12])
assert (p.get("fixed") or {}).get("'"$SH"'") == "'"$FIXED"'"'
SREND=$(MR render submit --revision "$SV2" --quality review --no-qa --wait --json | J 'd["data"]["render"]["id"]')
MR render status "$SREND" --json | J '"rendered with the fixed copy: %s" % d["data"]["render"]["state"]'
# Untick: the clip plays its original again; version 2 still plays the copy it was saved with, version 1 the original.
MR asset fixes "$SH" --none --json | CHECK '
v = d["data"]
print("unticked: %s" % v["usingWords"])
assert v["using"] == "original"'
MR revision show "$SV2" --json | J '"version 2 still pins: %s…" % d["data"]["spec"]["fixed"]["'"$SH"'"]["sha256"][:12]'
MR revision show "$SV1" --json | J '"version 1 unchanged: %s" % (d["data"]["summary"]["specHash"] == "'"$SV1HASH"'")'
MR asset fixes "$SH" --tick steady --json | J '"ticked again: %s (the copy is reused, nothing made twice)" % d["data"]["usingWords"]'

step "Recipes: a cut of the fixture film with a picture folder and an AI voice saved as a recipe (a draft from a script, made current by a person); a run planned first and started by a person (a new cut, its review copy and quality check, Awaiting your review); run again with a new picture in the folder and another voice — what changes and its estimate (in dollars, opening to each line; agreed once), a new version of the same cut; the runs compared; the recipe as its sheet, read back exactly, a hint added and saved as a draft; new footage with MetaRush's first picks, kept; the call's episode with a pack of shorts — changed in its sheet, one plan and estimate, a short's stretch changed, each short its own project, reviewed with the episode; the recipe channel in the log (D201–D208, D213–D215)"
for _ in $(seq 1 120); do curl -s -o /dev/null "http://127.0.0.1:$EF/__calls" && break; sleep 0.25; done
curl -s -o /dev/null "http://127.0.0.1:$EF/__calls" || { echo "the fake ElevenLabs on port $EF never answered (see $H/fakeeleven.log)" >&2; exit 1; }
PREC=$(MR project create "Acceptance recipe" --json | J 'd["data"]["project"]["id"]')
MR asset link "$A" --project "$PREC" --json >/dev/null
ART="$(dirname "$H")/character-art"; mkdir -p "$ART"
ffmpeg -hide_banner -loglevel error -y -f lavfi -i color=c=red:s=320x240 -frames:v 1 "$ART/hero.png"
# A linked folder takes a file once it has been still for a few seconds: these were saved a minute ago.
BACKDATE='import os,sys,time; t=time.time()-60; [os.utime(p,(t,t)) for p in sys.argv[1:]]'
python3 -I -c "$BACKDATE" "$ART/hero.png"
MR resources link "$ART" --project "$PREC" --json | J '"a picture folder: %s" % d["data"]["message"]'
read -r HERO HEROSHA < <(MR resources list --project "$PREC" --json | J '" ".join([(r["id"], r["current"]) for r in d["data"]["resources"] if r["name"].startswith("hero")][0])')
api provider.settings '{"name":"elevenlabs","voice":"Ada"}' >/dev/null  # the narration voice of the default Content Settings
cat > "$H/recipe-cut.json" <<JSON
{"plan":{"projectId":"$PREC","instruction":"a garden story to make again","spec":{"schemaVersion":1,"title":"Garden story","output":{"preset":"vertical-1080p","fps":30},
 "clips":[{"id":"hook","assetId":"$A","in":1,"out":4,"label":"the hook"},{"id":"answer","assetId":"$A","in":5,"out":8,"label":"the answer"}],
 "overlays":[{"id":"end","template":"endcard.cta","at":4,"duration":2,"params":{"headline":"Grow with us","cta":"Visit","image":"hero"}}],
 "resources":{"hero":{"id":"$HERO","sha256":"$HEROSHA"}},
 "voiceover":{"beats":[{"id":"l1","text":"Meet the garden.","anchor":{"kind":"clip","clipId":"hook","offset":0.2}},{"id":"l2","text":"Grow with us.","anchor":{"kind":"after"}}]}}}}
JSON
RC1=$(api edit.apply "$(cat "$H/recipe-cut.json")" | J 'd["data"]["revision"]["id"]')
RC2=$(api voiceover.tts "{\"revisionId\":\"$RC1\",\"apply\":true}" | J 'd["data"]["applied"]["revisionId"]')
api revision.approve "{\"id\":\"$RC2\",\"note\":\"make this again\"}" >/dev/null
MR recipe propose --from "$PREC" --json | CHECK '
p = d["data"]
keys = {i["key"]: i["varies"] for i in p["proposal"]}
print("proposed — changes each run: %s; stays: %s" % (sorted(k for k, v in keys.items() if v), sorted(k for k, v in keys.items() if not v)))
assert keys.get("footage") and keys.get("character-art") and keys.get("voice") and keys.get("headline"), keys
assert keys.get("cta") is False and p["check"]["ok"], p["check"]'
MR recipe save --from "$PREC" --hint "Open on a face." --json </dev/null | J '"saved from a script: %s" % d["data"]["message"]'
MR recipe publish garden-story --json </dev/null | J '"making it current from a script → %s" % d["error"]["code"]' || true
api recipe.publish '{"recipe":"garden-story"}' | J '"a person makes it current: %s" % d["data"]["message"]'
MR recipe show garden-story --json | J 'd["data"]["readable"]'
MR recipe plan garden-story --json > "$H/recipe-plan.json"
CHECK '
p = d["data"]
print("plan: %s (steps: %s)" % (p["summary"], ", ".join(s["label"] for s in p["steps"])))
assert p["ready"] and p["estimate"]["about"] == "nothing" and not p["estimate"]["parts"] and p["askForEstimate"] and not p["claude"], p' < "$H/recipe-plan.json"
FP=$(J 'd["data"]["fingerprint"]' < "$H/recipe-plan.json")
apiAny recipe.run "{\"recipe\":\"garden-story\",\"fingerprint\":\"0000000000000000000000000000000000\"}" | J '"another plan than the one shown → %s" % d["error"]["code"]'
api recipe.run "{\"recipe\":\"garden-story\",\"fingerprint\":\"$FP\"}" | J '"a person starts it: %s" % d["data"]["message"]'
MR recipe runs --run garden-story#1 --wait --json | CHECK '
r = d["data"]["run"]
print("run 1: %s — version %d of the cut “%s” in “%s”, the quality check %s, spent %s" % (r["stateLabel"], r["revisionNumber"], r["branch"], r["projectName"], r["qa"], r["spentWords"]))
assert r["state"] == "done" and r["branch"] == "garden-story-1", r'
ffmpeg -hide_banner -loglevel error -y -f lavfi -i color=c=green:s=320x240 -frames:v 1 "$ART/villain.png"
python3 -I -c "$BACKDATE" "$ART/villain.png"
MR resources folder character-art --rescan --json | J '"new art in the folder: %s" % d["data"]["message"]'
MR recipe plan --again garden-story#1 --input voice=Bram --note "a warmer voice" --json > "$H/recipe-again.json"
CHECK '
p = d["data"]
print("again: %s" % p["summary"])
print("  what changes: %s" % "; ".join(p["changes"]))
e = p["estimate"]
print("  estimate: %s — %s" % (e["about"], ["%s — %s, %s: %s, %s" % (l["step"], l["service"], l["model"], l["source"], l["units"]) for l in e["parts"]]))
assert p["ready"] and e["usd"] > 0 and len(e["parts"]) == 2 and all(l["service"] == "ElevenLabs" and l["step"] == "Make the AI voice" for l in e["parts"]), e
assert p["askForEstimate"] and p["repeats"]["ref"] == "garden-story#1"
assert any("picture" in c for c in p["changes"]) and any("Bram" in c for c in p["changes"]), p["changes"]' < "$H/recipe-again.json"
FP2=$(J 'd["data"]["fingerprint"]' < "$H/recipe-again.json"); USD2=$(J 'd["data"]["estimate"]["usd"]' < "$H/recipe-again.json")
apiAny recipe.again "{\"run\":\"garden-story#1\",\"inputs\":[{\"name\":\"voice\",\"text\":\"Bram\"}],\"note\":\"a warmer voice\",\"fingerprint\":\"$FP2\"}" | J '"without agreeing to the estimate → %s" % d["error"]["code"]'
api recipe.again "{\"run\":\"garden-story#1\",\"inputs\":[{\"name\":\"voice\",\"text\":\"Bram\"}],\"note\":\"a warmer voice\",\"fingerprint\":\"$FP2\",\"maxUsd\":$USD2}" | J '"a person agrees to %s and starts it: %s" % (d["data"]["run"]["agreedWords"], d["data"]["message"])'
MR recipe runs --run garden-story#2 --wait --json > "$H/recipe-run2.json"
CHECK '
r = d["data"]["run"]
print("run 2: %s — version %d of the same cut, spent %s" % (r["stateLabel"], r["revisionNumber"], r["spentWords"]))
assert r["state"] == "done" and r["branch"] == "garden-story-1" and r["spent"]["credits"] > 0 and r["spent"]["usd"] > 0, r' < "$H/recipe-run2.json"
MR revision show "$(J 'd["data"]["run"]["revision"]' < "$H/recipe-run2.json")" --json | CHECK '
s = d["data"]["spec"]
voices = sorted({(b.get("voice") or {}).get("name", "") for b in s["voiceover"]["beats"]})
print("the new version: the end card shows %s; the lines are read by %s" % (s["resources"]["hero"]["id"][:8] + "…", voices))
assert voices == ["Bram"] and all(b.get("take") for b in s["voiceover"]["beats"])'
curl -s "http://127.0.0.1:$EF/__calls" | J '"AI voice lines made by the fake ElevenLabs: %d" % d.get("tts", 0)'
MR recipe compare garden-story#1 garden-story#2 --json | CHECK '
c = d["data"]
changed = sorted(r["name"] for r in c["inputs"] if r["changed"])
print("compared: %s changed; %s; the cut: %s" % (changed, c["cost"], c["cut"]["changes"][:3] if c.get("cut") else "—"))
assert changed == ["character-art", "voice"], changed'
# The recipe sheet (D213): the recipe as Markdown that reads back to the same version; a hint added in it and saved as
# a draft from a script (only a person makes a version current).
MR recipe checkout garden-story "$H/garden-story.recipe.md" --json | J '"the recipe as its sheet: %s (version %d)" % (d["data"]["written"].split("/")[-1], d["data"]["version"]["number"])'
MR recipe check "$H/garden-story.recipe.md" --json | CHECK '
c = d["data"]
print("read back: %s" % c["message"])
assert c.get("valid") and c.get("same") and c["check"]["ok"], c'
python3 -I -c 'import sys; p = sys.argv[1]; s = open(p, encoding="utf-8").read(); open(p, "w", encoding="utf-8").write(s.replace("- Open on a face.\n", "- Open on a face.\n- End on the garden, never on black.\n", 1))' "$H/garden-story.recipe.md"
MR recipe check "$H/garden-story.recipe.md" --json | CHECK '
c = d["data"]
print("the edited sheet: %s %s" % (c["message"], c.get("changes")))
assert c.get("valid") and not c.get("same") and c.get("changes") == ["Added the hint “End on the garden, never on black.”."], c'
MR recipe check "$H/garden-story.recipe.md" --save --note "end on the garden" --json </dev/null | CHECK '
w = d["data"]
print("saved from a script: %s" % w["message"])
assert w["version"]["state"] == "draft" and w["version"]["number"] == 2 and not w.get("new"), w["version"]'
# First picks (D215): new footage for the chosen section — MetaRush picks its moments first, each with why; the run
# waits until they are kept or changed ("Claude" keeps them here) and goes on.
MR recipe plan garden-story --input footage="$A,$B" --json > "$H/recipe-picks.json"
CHECK '
p = d["data"]
print("with new footage: %s" % [s["detail"] for s in p["steps"] if s["id"] == "claude"])
assert p["ready"] and p["claude"], p' < "$H/recipe-picks.json"
FP3=$(J 'd["data"]["fingerprint"]' < "$H/recipe-picks.json")
api recipe.run "{\"recipe\":\"garden-story\",\"inputs\":[{\"name\":\"footage\",\"items\":[\"$A\",\"$B\"]}],\"fingerprint\":\"$FP3\"}" | J '"a person starts it: %s" % d["data"]["message"]'
MR recipe runs --run garden-story#3 --wait --json | CHECK '
r = d["data"]["run"]
print("run 3: %s" % r["stateLabel"])
for p in r.get("picks") or []:
    print("  %s %d: %s of “%s” — %s (%s) %s" % (p["part"], p["n"], p["when"], p.get("footageTitle", ""), p["state"], ", ".join(p.get("why") or []), p.get("words", "")[:60]))
assert r["state"] == "waiting" and r["waitingFor"] == "claude" and any(p["state"] == "first" for p in r.get("picks") or []), r'
MR recipe picks garden-story#3 --keep --json | J '"kept: %s" % d["data"]["message"]'
MR recipe runs --run garden-story#3 --wait --json | CHECK '
r = d["data"]["run"]
print("run 3: %s — version %d of “%s”" % (r["stateLabel"], r["revisionNumber"], r["branch"]))
assert r["state"] == "done" and all(p["state"] == "kept" for p in r["picks"]), r'
# A pack (D214): the call's episode saved as a recipe with its shorts (a person, in the dashboard), the shorts made
# shorter in the recipe's sheet (a draft a person makes current), one plan and one estimate for the episode and its
# shorts, the shorts' first picks kept or changed, each short made as its own project with its review copy and
# quality check and waiting for review with the episode; a short's project lists the run that made it.
MR recipe propose --from "$PC" --json | CHECK '
p = [i for i in d["data"]["proposal"] if i["kind"] == "pack"]
print("offered for a cut that plays a conversation: %s" % [i["what"] for i in p])
assert p and p[0]["key"] == "shorts" and not p[0]["varies"], d["data"]["proposal"]'
api recipe.save "{\"from\":\"$PC\",\"name\":\"garden-call\",\"pack\":[\"shorts\"]}" | J '"saved by a person: %s" % d["data"]["message"]'
MR recipe checkout garden-call "$H/garden-call.recipe.md" --json >/dev/null
python3 -I -c 'import sys; p = sys.argv[1]; s = open(p, encoding="utf-8").read(); old = "- shorts: shorts · count 1–3 · length 15–60 s"; assert old in s, s; open(p, "w", encoding="utf-8").write(s.replace(old, "- shorts: shorts · count 2 · length 5–20 s · named \"{title}: short {k}\"", 1))' "$H/garden-call.recipe.md"
MR recipe check "$H/garden-call.recipe.md" --save --note "two shorter shorts" --json </dev/null | J '"Claude changes the shorts in the sheet: %s %s" % (d["data"]["message"], d["data"]["changes"])'
api recipe.publish '{"recipe":"garden-call"}' | J '"a person makes it current: %s" % d["data"]["message"]'
MR recipe plan garden-call --json > "$H/recipe-pack.json"
CHECK '
p = d["data"]
print("plan: %s (estimate %s)" % (p["summary"], p["estimate"]["about"]))
for o in p.get("pack") or []:
    print("  short %d: %.1f–%.1f s “%s” (%s)" % (o["n"], o["from"], o["to"], o.get("first", ""), ", ".join(o.get("why") or [])))
assert p["ready"] and p.get("pack") and any(s["id"] == "pack" for s in p["steps"]) and p["claude"], p' < "$H/recipe-pack.json"
FP4=$(J 'd["data"]["fingerprint"]' < "$H/recipe-pack.json")
api recipe.run "{\"recipe\":\"garden-call\",\"fingerprint\":\"$FP4\"}" | J '"a person starts it: %s" % d["data"]["message"]'
MR recipe runs --run garden-call#1 --wait --json | J '"run 1: %s" % d["data"]["run"]["stateLabel"]'
MR recipe picks garden-call#1 --part shorts --n 2 --from 36.3 --to 44.3 --why "the roof garden" --keep --json | J '"Claude keeps a short and changes another: %s" % d["data"]["message"]'
MR recipe runs --run garden-call#1 --wait --json > "$H/recipe-pack-run.json"
CHECK '
r = d["data"]["run"]
print("run 1: %s — the episode version %d (the quality check %s)" % (r["stateLabel"], r["revisionNumber"], r.get("qa")))
for s in r.get("shorts") or []:
    print("  short %d: “%s” — %s" % (s["n"], s.get("project", ""), s["state"]))
assert r["state"] == "done" and len(r["shorts"]) == 2 and all(s["state"] == "ready" for s in r["shorts"]), r' < "$H/recipe-pack-run.json"
SHORTR=$(J 'd["data"]["run"]["outputs"][1]["revision"]' < "$H/recipe-pack-run.json")
SHORTP=$(J 'd["data"]["run"]["outputs"][1]["projectId"]' < "$H/recipe-pack-run.json")
MR revision show "$SHORTR" --json | CHECK '
v = d["data"]
s = v["summary"]
print("a short: version %d, origin %s, %s" % (s["number"], s.get("origin"), v["spec"]["output"]["preset"]))
assert s.get("origin", "").startswith("recipe:") and v["spec"]["output"]["preset"] == "vertical-1080p", s.get("origin")'
MR recipe runs --project "$SHORTP" --json | J '"a short'"'"'s project lists the run that made it: %s" % [r["ref"] for r in d["data"]["runs"]]'
MR log list --since 2h --channel recipe --json | J '"done by the runs (the recipe channel): %s" % sorted({e["text"] for e in d["data"]["entries"]})[:6]'

step "Recipes between workspaces (D219): garden-story exported to a file (its pictures, the footage its cut plays, its sheet) and brought into another workspace — a dry run first, then a draft with its things in a project of its own, planned there; the same file again brings nothing; a file holding anything else refused. A Google Drive folder as an input without syncing it (D220): the checkup, the input pointed at the folder (a draft a person makes current), planned (the folder listed, one file to fetch), run (only that file fetched through the fake rclone, kept by its content with its Drive ID)"
MR recipe export garden-story "$H/garden-story.recipe.zip" --json | CHECK '
x = d["data"]
print("exported: %s (left out: %s)" % (x["message"], [l["what"] for l in x["leftOut"]]))
assert x["pictures"] >= 1 and x["footage"] >= 1, x'
python3 -I -c 'import sys, zipfile; n = zipfile.ZipFile(sys.argv[1]).namelist(); print("in the file: %s, %s and %d files named by their SHA-256" % (n[0], n[1], len(n) - 2)); assert n[0] == "recipe.json" and n[1] == "garden-story.recipe.md"' "$H/garden-story.recipe.zip"
HX="$(dirname "$H")/home-other"; DBC="${DB}other"
MX() { ./bin/metarush --home "$HX" "$@"; }
METARUSH_MONGODB_URI="$URI" MX init --yes --test-db --secrets env --db-name "$DBC" --workspace-name "Another team" --media filesystem --port $((PORT + 2)) >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$HX/.env"; chmod 600 "$HX/.env"
MX recipe import "$H/garden-story.recipe.zip" --dry-run --json | CHECK '
x = d["data"]
print("a dry run in the other workspace: %s" % x["message"])
assert x["dryRun"] and x["new"] and x["newProject"], x'
MX recipe import "$H/garden-story.recipe.zip" --json | CHECK '
x = d["data"]
print("brought in: %s" % x["message"])
assert x["new"] and x["version"]["state"] == "draft" and x["version"]["imported"] and x["pictures"] >= 1 and x["footage"] >= 1, x'
MX recipe show garden-story --json | J '"its page there: %s" % d["data"]["imported"]["summary"]'
MX recipe plan garden-story --version 1 --json | CHECK '
p = d["data"]
print("planned there: %s" % p["summary"])
assert p["ready"], p["problems"]'
MX recipe import "$H/garden-story.recipe.zip" --json | J '"the same file again: %s" % d["data"]["message"]'
cp "$H/garden-story.recipe.zip" "$H/odd.recipe.zip"
python3 -I -c 'import sys, zipfile; zipfile.ZipFile(sys.argv[1], "a").writestr("run.sh", "#!/bin/sh\n")' "$H/odd.recipe.zip"
MX recipe import "$H/odd.recipe.zip" --json | J '"a file holding something else → %s: %s" % (d["error"]["code"], d["error"]["message"])' || true
go run ./scripts/droptestdb "$URI" "$DBC" >/dev/null && DBC="" && rm -rf "$HX"
ffmpeg -hide_banner -loglevel error -y -f lavfi -i color=c=green:s=320x240 -frames:v 1 "$DRIVE/Garden/Art/villain.png"
MR doctor --json | J '"the checkup: %s" % [c["detail"] for c in d["data"]["checks"] if c["area"] == "google drive"]'
MR recipe update garden-story --from-drive character-art=gdrive:Garden/Art --note "art from the shared Drive" --json | J '"a draft that takes its pictures from Google Drive: %s" % d["data"]["message"]'
api recipe.publish '{"recipe":"garden-story"}' | J '"a person makes it current: %s" % d["data"]["message"]'
MR recipe plan garden-story --json > "$H/recipe-drive.json"
CHECK '
p = d["data"]
art = [i for i in p["inputs"] if i["name"] == "character-art"][0]
print("planned: %s; %s" % (art["words"], p["steps"][0]["detail"]))
assert p["ready"] and art["drive"] == "gdrive:Garden/Art" and not art["items"][0].get("id") and "Fetches 1 file" in p["steps"][0]["detail"], p' < "$H/recipe-drive.json"
[ ! -s "$METARUSH_FAKE_RCLONE_LOG" ] || ! grep -q copyid "$METARUSH_FAKE_RCLONE_LOG" || { echo "planning fetched a file" >&2; exit 1; }
FPD=$(J 'd["data"]["fingerprint"]' < "$H/recipe-drive.json")
api recipe.run "{\"recipe\":\"garden-story\",\"fingerprint\":\"$FPD\"}" | J '"a person starts it: %s" % d["data"]["message"]'
RUND=$(MR recipe runs garden-story --json | J 'd["data"]["runs"][0]["ref"]')
MR recipe runs --run "$RUND" --wait --json | CHECK '
r = d["data"]["run"]
it = [i for i in r["inputs"] if i["name"] == "character-art"][0]["items"][0]
print("the run: %s; the picture “%s” fetched from Google Drive (Drive ID %s…, kept by its SHA-256 %s…)" % (r["stateLabel"], it["title"], it["drive"]["id"][:10], it["sha256"][:12]))
assert r["state"] == "done" and it.get("id") and it["drive"]["fetched"] and it["sha256"], r'
echo "files rclone fetched: $(grep -c copyid "$METARUSH_FAKE_RCLONE_LOG")"

step "log: the run told in plain words; a bug report without secrets"
sleep 2 # the daemon writes its entries in the background (within a second)
MR log list --since 2h --limit 500 --json | J '"\n".join("  " + t for t in reversed([e["text"] for e in d["data"]["entries"]]) if t.startswith(("Imported footage", "Made a review copy", "Checked the review copy", "Saved a new version", "Approved version", "Exported finished videos")))'
MR log list --since 2h --limit 500 --json | J '"story complete (import, render, QA): %s" % all(any(e["text"].startswith(p) for e in d["data"]["entries"]) for p in ("Imported footage", "Made a review copy", "Checked the review copy"))'
MR log list --since 2h --problems --json | J '"problems logged: %s" % [e["text"] for e in d["data"]["entries"]]'
MR log list --since 2h --channel dashboard --json | J '"done in the dashboard: %s" % [e["text"] for e in d["data"]["entries"]]'
MR log bundle --since 2h --out "$H/bug-report.zip" --json | J '"bug report: %d entries, %d failed jobs" % (d["data"]["entries"], d["data"]["failedJobs"])'
python3 -I - "$H/bug-report.zip" "$H/state/confirm.key" <<'PY'
import base64, binascii, re, sys, zipfile
z = zipfile.ZipFile(sys.argv[1])
key = open(sys.argv[2], "rb").read()
forms = [binascii.hexlify(key).decode(), base64.b64encode(key).decode()]
shapes = re.compile(r"sk_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|tsec_[A-Za-z0-9]{16,}|mongodb(\+srv)?://[^/\s:@]+:[^@\s]+@|PRIVATE KEY-----")
bad = [n for n in z.namelist() for t in [z.read(n).decode("utf-8", "replace")] if shapes.search(t) or any(f in t for f in forms)]
print("bug report files: %s; secrets found: %s" % (sorted(z.namelist()), bad or "none"))
sys.exit(1 if bad else 0)
PY

step "renderer removed: renders refuse with a clear prompt to set it up again (never a silent reinstall)"
rm -rf "$H/renderer/$RID"
MR render submit --revision "$R2" --quality final --no-qa --wait --json | J '"a render after deleting the renderer → %s: %s %s" % (d["error"]["code"], d["error"]["message"], d["error"]["hint"])' || true
MR doctor --json | J '"doctor: %s" % ["%s (%s)" % (c["detail"], c.get("fix", "")) for c in d["data"]["checks"] if c["area"] == "renderer"]'

step "cleanup (throwaway database only)"
echo "home kept for inspection: $H"
exit 0
