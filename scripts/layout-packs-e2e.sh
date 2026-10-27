#!/usr/bin/env bash
# layout-packs-e2e.sh — layout packs in a real browser (D263–D267), on a
# throwaway installation with a fake `claude`: "Acme" (the first project, so
# its settings are the default) with a logo and colours; "Client" (uses the
# default) whose first cut names roles — a title card, a lower third and an end
# screen — that MetaRush's own pack fills. A motion graphic for Acme's title
# card is checked from the command line, as Claude Code in a terminal would
# (it waits for a person). The browser run changes the pack in Settings →
# Layout pack (each role previewed in the house style), takes the offer for
# Client's latest cut, asks the fake Claude for a pack in the panel (a card
# only a person decides: kept as the proposed pack while its graphic waits),
# approves the graphic under Motion, accepts the pack and puts it on the
# latest cut. Screenshots: desktop dark and light, phone. Fakes only: nothing
# paid, no real Claude.
#
#   scripts/layout-packs-e2e.sh                  # needs scripts/dev-mongo.sh running
#   KEEP=1 scripts/layout-packs-e2e.sh           # keep the home, database and dashboard afterwards
#   NO_BROWSER=1 scripts/layout-packs-e2e.sh     # set it up and leave the dashboard running
#   METARUSH_E2E_PORT=5490 SKIP_BUILD=1 PACKS_SHOTS=<folder> scripts/layout-packs-e2e.sh
#
# Ports 5490–5499 (this stream's). The throwaway database is dropped on exit
# through scripts/droptestdb (store.DropTestDatabase).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew
PORT="${METARUSH_E2E_PORT:-5490}"
BASE="${METARUSH_E2E_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/layout-packs-e2e-XXXXXX")}"
mkdir -p "$BASE"
H="$BASE/home"; DB="metarush-test-packs$(date +%s)"
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
if [ "${SKIP_BUILD:-}" != "1" ]; then
  step "build the dashboard and the binary"
  npm run build -w web >/dev/null
  go build -o bin/metarush ./cmd/metarush
fi

step "a throwaway installation with a fake claude"
METARUSH_MONGODB_URI="$URI" MR init --yes --test-db --secrets env --db-name "$DB" --workspace-name "Packs test" --media filesystem --port "$PORT" --video-dir "$ROOT/video" >/dev/null
echo "METARUSH_MONGODB_URI=$URI" > "$H/.env"; chmod 600 "$H/.env"
grep -q '^assistant:' "$H/config.yaml" || printf 'assistant:\n  holdNews: false\n' >> "$H/config.yaml"
go build -o "$BASE/claude" ./internal/assistant/fakeclaude/cmd/fakeclaude

step "Acme (the default's settings): a logo and its colours"
mkdir -p "$BASE/media"
A=$(MR project create "Acme" --json | J 'd["data"]["project"]["id"]')
printf '<svg xmlns="http://www.w3.org/2000/svg" width="240" height="120" viewBox="0 0 240 120"><rect x="6" y="6" width="108" height="108" rx="28" fill="#F2A900"/><rect x="128" y="44" width="104" height="32" rx="6" fill="#F7F5F0"/></svg>\n' > "$BASE/media/acme-logo.svg"
MR add "$BASE/media/acme-logo.svg" --project "$A" --json >/dev/null
MR brand set --logo acme-logo --background "#13294B" --accent "#F2A900" --primary "#F7F5F0" --json | J '"brand: " + d["data"]["message"]'

step "Client uses the default; its first cut names roles"
C=$(MR project create "Client" --json | J 'd["data"]["project"]["id"]')
ffmpeg -v error -y -f lavfi -i "color=c=0x2a6f4e:s=1280x720:r=30:d=12" -f lavfi -i "sine=frequency=330:sample_rate=48000:duration=12" \
  -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest "$BASE/media/client-talk.mp4"
MR ingest "$BASE/media/client-talk.mp4" --project "$C" --no-transcribe --wait --json | J '"added=%d" % d["data"]["ingest"]["ingested"]'
CA=$(MR asset list "$C" --json | J '[a["id"] for a in d["data"]["assets"]][0]')
cat > "$BASE/plan.json" <<EOF
{"projectId":"$C","instruction":"A first cut","spec":{"schemaVersion":1,"title":"Spring launch","output":{"preset":"landscape-1080p","fps":30},
 "clips":[{"id":"talk","assetId":"$CA","in":0,"out":12}],
 "overlays":[{"id":"intro","role":"title-card","at":0,"duration":3,"params":{"title":"Spring launch"}},
  {"id":"ada","role":"lower-third","at":3.5,"duration":3,"params":{"name":"Ada Lovelace","title":"Engineer"}},
  {"id":"end","role":"end-screen","at":7,"duration":0,"params":{"headline":"Thanks for watching"}}]}}
EOF
TOK=$(MR edit check --plan "$BASE/plan.json" --json | J 'd["data"]["confirmToken"]')
MR edit apply --plan "$BASE/plan.json" --confirm "$TOK" --json | J '"version %d of Client" % d["data"]["revision"]["number"]'
MR cut show "$C" --json | J '"\n".join(l for l in d["data"]["sheet"].splitlines() if l.startswith("- "))'
MR content pack show --project "$C"

step "a motion graphic for Acme's title card, checked from the command line (it waits for a person)"
G="$BASE/acme/motion/acme-title"; mkdir -p "$G"
cat > "$G/index.tsx" <<'TSX'
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { z } from 'zod';
import { makeOverlayClock, rgba, type MotionMeta, type MotionProps } from '@metarush/motion';

export const schema = z.object({
  title: z.string().min(1).max(80).describe('The title'),
  subtitle: z.string().max(120).optional().describe('A line under it'),
  wipe: z.enum(['left', 'right']).optional().describe('Where the colour comes in from'),
});

export const meta: MotionMeta = {
  summary: "Acme's title card: a band in the brand's accent sweeps in and the title rises over the brand's background.",
  region: 'full',
  minDuration: 2,
  maxDuration: 6,
};

export default function AcmeTitle({ params, durationInFrames, brand, layout }: MotionProps<typeof schema>) {
  const frame = useCurrentFrame();
  const { content, unit: u, fps } = layout;
  const c = makeOverlayClock(frame, durationInFrames, fps, 1.2);
  const sweep = c.enter(0, 0.6);
  const rise = c.enter(0.3, 0.7);
  const fromLeft = params.wipe !== 'right';
  return (
    <AbsoluteFill style={{ pointerEvents: 'none', backgroundColor: brand.background, opacity: c.exit }}>
      <div style={{ position: 'absolute', top: content.top + content.height * 0.62, height: 18 * u, width: content.width * sweep,
        left: fromLeft ? content.left : content.left + content.width * (1 - sweep), backgroundColor: brand.accent }} />
      <div style={{ position: 'absolute', left: content.left, top: content.top, width: content.width, height: content.height * 0.6,
        display: 'flex', flexDirection: 'column', justifyContent: 'flex-end', fontFamily: brand.fontFamily, color: brand.text,
        opacity: rise, transform: `translateY(${(1 - rise) * 24 * u}px)` }}>
        <div style={{ fontSize: 120 * u, fontWeight: 700, lineHeight: 1.05 }}>{params.title}</div>
        {params.subtitle ? <div style={{ fontSize: 44 * u, color: rgba(brand.text, 0.8), marginTop: 18 * u }}>{params.subtitle}</div> : null}
      </div>
    </AbsoluteFill>
  );
}
TSX
printf '{ "title": "Spring launch", "subtitle": "From Acme", "wipe": "left" }\n' > "$G/preview.json"
MR motion check "$G" --project "$A" --json > "$BASE/check.json"; J '"check: " + d["data"]["summary"]' < "$BASE/check.json"
HASH=$(J 'd["data"]["hash"]' < "$BASE/check.json")

# The fake claude: asked for a layout pack, it writes the pack (its title card
# the graphic checked above) and proposes it; the refusal becomes a card.
python3 -I - "$BASE/claude.json" "$A" "$HASH" <<'PY'
import json, sys
path, project, h = sys.argv[1:4]
pack = {"name": "Acme spring", "note": "Acme's spring look", "pieces": {
    "title-card": {"motion": {"name": "acme-title", "hash": h}, "fields": {"wipe": "left"}, "seconds": 4},
    "lower-third": {"template": "lower-third.cinematic", "fields": {"align": "center"}},
    "end-screen": {"template": "end.screen", "fields": {"slots": "two-videos"}, "logo": True}},
    "layouts": {"pip": {"shape": "circle", "frame": "accent"}, "sideBySide": {"gap": "wide", "backdrop": "brand"}}}
def bash(cmd, denied=True, result=None):
    t = {"name": "Bash", "denied": denied, "input": {"command": cmd}}
    if result is not None:
        t["result"] = result
    return {"tool": t}
json.dump({"turns": [
    {"match": "layout pack", "costUsd": 0.002, "steps": [
        {"text": "I'll look at Acme's pack and brand first."},
        bash("metarush content pack show --project " + project + " --json", denied=False, result="{\"ok\":true,\"data\":{}}"),
        {"write": {"name": "pack.json", "content": json.dumps(pack, indent=2)}},
        bash("metarush content pack propose --project " + project + " --file {{proposals}}/pack.json --note \"Acme's spring look\" --json"),
        {"text": "Acme's pack is on a card: its title card is the motion graphic acme-title, which needs your approval first."}]}],
    "default": {"steps": [{"text": "Noted."}]}}, open(path, "w"))
PY

step "the dashboard on 127.0.0.1:$PORT"
METARUSH_CLAUDE="$BASE/claude" METARUSH_DEV_ORIGINS="" ./bin/metarush --home "$H" serve > "$BASE/serve.log" 2>&1 & SRV=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null && break; sleep 0.25; done

if [ "${NO_BROWSER:-}" = "1" ]; then
  echo "ready: http://127.0.0.1:$PORT — sign in with: ./bin/metarush --home $H dashboard --print-link --no-open"
  KEEP=1
  exit 0
fi
step "the browser run: Settings → Layout pack, Claude's pack, approving, accepting"
SHOTS="${PACKS_SHOTS:-$BASE/screenshots}"
METARUSH_DASHBOARD="http://127.0.0.1:$PORT" METARUSH_MINT="./bin/metarush --home $H dashboard --print-link --no-open" \
PACKS_ACME="$A" PACKS_CLIENT="$C" PACKS_MOTION="$HASH" PACKS_SHOTS="$SHOTS" node web/scripts/layout-packs-e2e.mjs

step "what the browser changed, from the CLI"
MR content pack show --project "$C"
MR cut show "$C" --json | J '"\n".join(l for l in d["data"]["sheet"].splitlines() if l.startswith("- "))'
exit 0
