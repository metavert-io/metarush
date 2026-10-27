#!/usr/bin/env node
// "An 8-second shot", not "a 8-second shot": the dashboard's article before a
// number (aOrAn in web/src/lib/format.ts) follows the same cases as Go's
// (plainwords.AOrAn, internal/plainwords/testdata/articles.json), and the
// Generate panel's price line says it that way (describe in
// web/src/components/generate/describe.ts, bundled with esbuild because it
// imports the rule).
//
//   node --experimental-strip-types web/scripts/article-test.mjs
import { mkdtempSync, readFileSync, realpathSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { build } from 'esbuild';

const here = path.dirname(fileURLToPath(import.meta.url));
const { aOrAn } = await import(path.join(here, '..', 'src', 'lib', 'format.ts'));
const cases = JSON.parse(readFileSync(path.join(here, '..', '..', 'internal', 'plainwords', 'testdata', 'articles.json'), 'utf8'));

let failed = 0;
let checked = 0;
const eq = (name, got, want) => {
  checked++;
  if (got !== want) {
    failed++;
    console.error(`FAIL ${name}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
};

for (const n of cases.an) eq(`aOrAn(${n})`, aOrAn(n), 'an');
for (const n of cases.a) eq(`aOrAn(${n})`, aOrAn(n), 'a');
// Numbers Go never sends: a fraction follows its whole number; no number at all takes "a" (and never loops).
for (const [n, want] of [[8.5, 'an'], [6.5, 'a'], [Infinity, 'a'], [NaN, 'a'], [-8, 'a']]) eq(`aOrAn(${n})`, aOrAn(n), want);

// The real path (macOS's temp folder is behind a link), so the bundle's source map points at the sources.
const dir = realpathSync(mkdtempSync(path.join(tmpdir(), 'article-')));
try {
  const out = path.join(dir, 'describe.mjs');
  // Coverage (npm run test:coverage -w web) is counted in the sources, not the bundle.
  await build({ entryPoints: [path.join(here, '..', 'src', 'components', 'generate', 'describe.ts')], bundle: true, format: 'esm', platform: 'node', outfile: out, logLevel: 'error', sourcemap: 'inline' });
  const { describe } = await import(pathToFileURL(out).href);
  const est = (over) => ({ operation: 'shot', seconds: 6, takes: 1, quality: 'draft', resolution: '720p', aspectRatio: '16:9', providerTitle: 'Google Gemini', ...over });
  eq('a 6-second shot', describe(est({})), 'a 6-second shot, draft (720p), wide, made with Google Gemini');
  eq('an 8-second shot', describe(est({ seconds: 8 })), 'an 8-second shot, draft (720p), wide, made with Google Gemini');
  eq('an 11-second shot', describe(est({ seconds: 11, quality: 'final', resolution: '4k', aspectRatio: '9:16' })), 'an 11-second shot, final quality (4K), tall, made with Google Gemini');
  eq('takes of an 18-second shot', describe(est({ seconds: 18, takes: 2 })), '2 takes of an 18-second shot, draft (720p), wide, made with Google Gemini');
  eq('more seconds of a clip', describe(est({ operation: 'extend', seconds: 8 })), '8 more seconds, draft (720p), wide, made with Google Gemini');
  eq('a changed copy', describe(est({ operation: 'edit', seconds: 8 })), 'a changed copy of the clip, draft (720p), wide, made with Google Gemini');
} finally {
  rmSync(dir, { recursive: true, force: true });
}

console.log(`${failed ? 'FAIL' : 'ok  '} a or an before a number: ${checked - failed}/${checked} checks agree with Go`);
if (failed) process.exit(1);
