#!/usr/bin/env node
// Checks that the dashboard's word rules (src/lib/words.ts: which words a clip
// plays, the stretch that removes a selection, the removeWords ops for a set of
// removals) match Go's (internal/cutsheet/words.go) for every case in the
// golden file Go writes. Run by `go test ./internal/cutsheet`
// (TestWordRemovalsGolden) and `npm run test:editops -w web`.
//
//   node web/scripts/words-parity.mjs [internal/cutsheet/testdata/word-removals.golden.json]
import { mkdtempSync, readFileSync, realpathSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { build } from 'esbuild';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const goldenArg = process.argv[2] ?? 'internal/cutsheet/testdata/word-removals.golden.json';
const golden = path.isAbsolute(goldenArg) ? goldenArg : path.resolve(process.cwd(), goldenArg);

// The real path (macOS's temp folder is behind a link), so the bundle's source map points at
// the sources from where Node loads it.
const dir = realpathSync(mkdtempSync(path.join(tmpdir(), 'words-')));
try {
  const out = path.join(dir, 'words.mjs');
  await build({
    entryPoints: [path.join(repo, 'web/src/lib/words.ts')],
    bundle: true,
    format: 'esm',
    platform: 'node',
    outfile: out,
    logLevel: 'error',
    // Coverage (npm run test:coverage -w web) is counted in src/lib/words.ts, not the bundle.
    sourcemap: 'inline',
    alias: { '@metarush/schema/schemas/transitions.json': path.join(repo, 'schema/transitions.json') },
  });
  const { removalFor, removalOps } = await import(pathToFileURL(out).href);
  const { cases } = JSON.parse(readFileSync(golden, 'utf8'));
  const norm = (v) => {
    const sort = (x) =>
      Array.isArray(x)
        ? x.map(sort)
        : x && typeof x === 'object'
          ? Object.fromEntries(
              Object.keys(x)
                .sort()
                .map((k) => [k, sort(x[k])]),
            )
          : x;
    return JSON.stringify(sort(JSON.parse(JSON.stringify(v ?? null))));
  };
  const fields = ['clipId', 'clip', 'words', 'wordsFrom', 'wordsTo', 'from', 'to', 'at', 'seconds'];
  const pick = (r) => (r ? Object.fromEntries(fields.map((k) => [k, r[k]])) : null);
  const opsOf = (spec, rs) => {
    try {
      return { ops: removalOps(spec, rs) };
    } catch (e) {
      return { error: e };
    }
  };
  let failed = 0;
  let checks = 0;
  const fail = (msg) => {
    failed++;
    if (failed <= 20) console.error(`✗ ${msg}`);
  };
  for (const c of cases) {
    const tx = (id) => c.transcripts[id];
    c.selections.forEach((s, i) => {
      checks++;
      const want = norm(pick(c.removals[i]));
      const have = norm(pick(removalFor(c.spec, tx, s.clipId, s.from, s.to)));
      if (want !== have) fail(`${c.name}, selection ${i + 1}\n  go:  ${want}\n  web: ${have}`);
    });
    const sets = [
      ['removals', c.removals.filter(Boolean), c.ops, c.error],
      ['cleanups', c.cleanups, c.cleanupOps, c.cleanupError],
    ];
    for (const [what, rs, wantOps, wantErr] of sets) {
      checks++;
      const got = opsOf(c.spec, rs);
      if (wantErr) {
        if (!got.error) fail(`${c.name} (${what}): Go refused the ops, the dashboard did not`);
        continue;
      }
      if (got.error) {
        fail(`${c.name} (${what}): the dashboard refused the ops (${got.error.message})`);
        continue;
      }
      if (norm(got.ops) !== norm(wantOps)) fail(`${c.name} (${what})\n  go:  ${norm(wantOps)}\n  web: ${norm(got.ops)}`);
    }
  }
  if (failed) {
    console.error(`${failed} of ${checks} checks differ`);
    process.exit(1);
  }
  console.log(`✓ ${checks} checks in ${cases.length} cases: the dashboard's word rules match Go`);
} finally {
  rmSync(dir, { recursive: true, force: true });
}
