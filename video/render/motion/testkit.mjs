// Test-only helpers: archives for the self-tests. In production the archive
// and its hash come from Go (internal/motion); this mirrors that encoding for
// plain source text (sorted paths, LF), which is all the fixtures use.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { sha256 } from './archive.mjs';

const readTree = (dir, base = dir, out = {}) => {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    if (e.name.startsWith('.')) continue;
    const p = path.join(dir, e.name);
    if (e.isDirectory()) readTree(p, base, out);
    else out[path.relative(base, p).split(path.sep).join('/')] = fs.readFileSync(p, 'utf8').replace(/\r\n?/g, '\n');
  }
  return out;
};

/** files: {path: text} or a folder. Returns {hash, file, bytes}. */
export const makeArchive = (files, outDir = fs.mkdtempSync(path.join(os.tmpdir(), 'metarush-motion-archive-'))) => {
  const map = typeof files === 'string' ? readTree(files) : files;
  const list = Object.keys(map)
    .sort()
    .map((p) => ({ path: p, text: map[p] }));
  const bytes = Buffer.from(JSON.stringify({ format: 'metarush.motion/1', files: list }), 'utf8');
  const hash = sha256(bytes);
  const file = path.join(outDir, `${hash}.json`);
  fs.mkdirSync(outDir, { recursive: true });
  fs.writeFileSync(file, bytes);
  return { hash, file, bytes };
};

/** A minimal valid component around a body (for refusal and limit tests). */
export const component = ({ imports = '', body = 'return null;', top = '', region = 'center' } = {}) => ({
  'index.tsx': `import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { z } from 'zod';
import type { MotionMeta, MotionProps } from '@metarush/motion';
${imports}
export const schema = z.object({ label: z.string().max(40) });
export const meta: MotionMeta = { summary: 'A test graphic.', region: '${region}', minDuration: 1, maxDuration: 4 };
${top}
export default function Graphic({ params }: MotionProps<typeof schema>) {
  const frame = useCurrentFrame();
  ${body}
}
`,
  'preview.json': '{"label": "Test"}',
});
