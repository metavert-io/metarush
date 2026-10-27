#!/usr/bin/env bash
# Makes the refresh fixture (D173–D176): a copy of tests/fixtures/remotion/fixture-film that builds itself — its
# script "make-data" writes the number its title shows (src/generated/stats.ts) and a picture, "voice" makes a fake
# voice line with a fake key (ELEVENLABS_API_KEY; no network), "render" stands for a project's own render — as a git
# repository whose remote is a local bare repository (file://), so changes can be sent back without GitHub.
#
#   scripts/make-refresh-fixture.sh <folder> <bare remote>     prints the original commit
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIR="$1"; REMOTE="$2"
rm -rf "$DIR" "$REMOTE"; mkdir -p "$(dirname "$DIR")" "$(dirname "$REMOTE")"
cp -R "$ROOT/tests/fixtures/remotion/fixture-film" "$DIR"
mkdir -p "$DIR/src/generated" "$DIR/scripts"
printf 'export const VISITORS = 7;\n' > "$DIR/src/generated/stats.ts"
perl -0pi -e 's/import \{ HEADLINE, SUBTITLE \} from "\.\/story";/import { HEADLINE, SUBTITLE } from ".\/story";\nimport { VISITORS } from ".\/generated\/stats";/; s/\{SUBTITLE\}<\/div>/{SUBTITLE} · {VISITORS} visitors<\/div>/' "$DIR/src/Title.tsx"
cat > "$DIR/scripts/make-data.mjs" <<'JS'
import fs from 'node:fs';
fs.writeFileSync('src/generated/stats.ts', 'export const VISITORS = 42;\n');
fs.mkdirSync('public/refresh', { recursive: true });
fs.writeFileSync('public/refresh/chart.png', Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64'));
fs.writeFileSync('notes.txt', 'written outside the places this refresh may write\n');
console.log('made the data: 42 visitors');
JS
cat > "$DIR/scripts/fake-voice.mjs" <<'JS'
import fs from 'node:fs';
const key = process.env.ELEVENLABS_API_KEY;
if (!key) { console.error('ELEVENLABS_API_KEY is not set'); process.exit(1); }
console.log('the fake voice uses key ' + key);
const n = 800, b = Buffer.alloc(44 + n * 2);
b.write('RIFF', 0); b.writeUInt32LE(36 + n * 2, 4); b.write('WAVE', 8); b.write('fmt ', 12); b.writeUInt32LE(16, 16); b.writeUInt16LE(1, 20); b.writeUInt16LE(1, 22);
b.writeUInt32LE(8000, 24); b.writeUInt32LE(16000, 28); b.writeUInt16LE(2, 32); b.writeUInt16LE(16, 34); b.write('data', 36); b.writeUInt32LE(n * 2, 40);
for (let i = 0; i < n; i++) b.writeInt16LE(Math.round(8000 * Math.sin(i / 4)), 44 + i * 2);
fs.mkdirSync('public/voice', { recursive: true });
fs.writeFileSync('public/voice/line.wav', b);
fs.writeFileSync('src/generated/voice.json', JSON.stringify({ words: ['Forty', 'two', 'visitors'] }) + '\n');
JS
python3 -I - "$DIR/package.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p))
d["scripts"].update({"make-data": "node scripts/make-data.mjs", "voice": "node scripts/fake-voice.mjs", "render": "remotion render Title out/title.mp4"})
json.dump(d, open(p, "w"), indent=2)
PY
printf "Instructions for another project's assistant.\n" > "$DIR/CLAUDE.md"
G=(git -c init.defaultBranch=main -c user.name=Origin -c user.email=origin@example.invalid -c commit.gpgsign=false -c core.hooksPath=/dev/null)
"${G[@]}" -C "$DIR" init --quiet
"${G[@]}" -C "$DIR" add --all
"${G[@]}" -C "$DIR" commit --quiet -m "the original"
"${G[@]}" clone --quiet --bare "$DIR" "$REMOTE"
"${G[@]}" -C "$DIR" remote add origin "file://$REMOTE"
"${G[@]}" -C "$DIR" rev-parse HEAD
