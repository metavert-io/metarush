#!/usr/bin/env node
// Checks that the dashboard's working copy of the clip ops and moveBeat
// (src/lib/editops.ts) makes the same cuts, and puts narration lines in the
// same places, as Go (internal/editspec) for every case in the golden
// file Go writes. Run by `go test ./internal/editspec` (TestStudioOpsGolden) and
// `npm run test:editops -w web`.
//
//   node web/scripts/editops-parity.mjs [internal/editspec/testdata/studio-ops.golden.json]
import { mkdtempSync, readFileSync, realpathSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { build } from 'esbuild';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const goldenArg = process.argv[2] ?? 'internal/editspec/testdata/studio-ops.golden.json';
const golden = path.isAbsolute(goldenArg) ? goldenArg : path.resolve(process.cwd(), goldenArg);

// The real path (macOS's temp folder is behind a link), so the bundle's source map points at
// the sources from where Node loads it.
const dir = realpathSync(mkdtempSync(path.join(tmpdir(), 'editops-')));
try {
  const out = path.join(dir, 'editops.mjs');
  await build({
    entryPoints: [path.join(repo, 'web/src/lib/editops.ts')],
    bundle: true,
    format: 'esm',
    platform: 'node',
    outfile: out,
    logLevel: 'error',
    // Coverage (npm run test:coverage -w web) is counted in src/lib/editops.ts, not the bundle.
    sourcemap: 'inline',
    alias: { '@metarush/schema/schemas/transitions.json': path.join(repo, 'schema/transitions.json') },
  });
  const { applyOps, OpError } = await import(pathToFileURL(out).href);
  const { cases } = JSON.parse(readFileSync(golden, 'utf8'));
  // Canonical JSON: keys sorted at every level (field order is not meaning).
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
    return sort(JSON.parse(JSON.stringify(v)));
  };
  let failed = 0;
  for (const c of cases) {
    let got;
    let threw = null;
    let gotBeats;
    try {
      const out = applyOps(c.spec, c.ops);
      got = out.clips;
      gotBeats = out.voiceover?.beats;
    } catch (e) {
      threw = e;
    }
    if (c.error) {
      if (!threw) {
        failed++;
        console.error(`✗ ${c.name}: Go refused it, the dashboard did not`);
      }
      continue;
    }
    if (threw) {
      failed++;
      console.error(`✗ ${c.name}: the dashboard refused it (${threw instanceof OpError ? threw.message : threw})`);
      continue;
    }
    const want = JSON.stringify(norm(c.clips));
    const have = JSON.stringify(norm(got));
    if (want !== have) {
      failed++;
      console.error(`✗ ${c.name}\n  go:  ${want}\n  web: ${have}`);
      continue;
    }
    if (c.beats || gotBeats) {
      const wantB = JSON.stringify(norm(c.beats ?? []));
      const haveB = JSON.stringify(norm(gotBeats ?? []));
      if (wantB !== haveB) {
        failed++;
        console.error(`✗ ${c.name} (narration lines)\n  go:  ${wantB}\n  web: ${haveB}`);
      }
    }
  }
  if (failed) {
    console.error(`${failed} of ${cases.length} cases differ`);
    process.exit(1);
  }
  console.log(`✓ ${cases.length} cases: the dashboard's working copy matches Go`);
} finally {
  rmSync(dir, { recursive: true, force: true });
}
