#!/usr/bin/env node
// Every job type MetaRush has is named on the dashboard (D396): the types
// internal/jobs/queue.go declares each have a label in lib/format.ts, so a
// worker card never shows "Render-shape" or another raw type.
//
//   node --experimental-strip-types web/scripts/job-labels-test.mjs
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { jobLabel, JOB_TYPES_NAMED } = await import(path.join(here, '..', 'src', 'lib', 'format.ts'));
const queue = readFileSync(path.join(here, '..', '..', 'internal', 'jobs', 'queue.go'), 'utf8');
const types = [...queue.matchAll(/^\s*Type\w+\s*=\s*"([^"]+)"/gm)].map((m) => m[1]);

let failed = 0;
if (types.length < 20) {
  failed++;
  console.error(`✗ read only ${types.length} job types from queue.go`);
}
for (const t of types) {
  if (!JOB_TYPES_NAMED.includes(t)) {
    failed++;
    console.error(`✗ ${t} has no label on the dashboard (lib/format.ts JOB_LABELS): it shows as “${jobLabel(t)}”`);
  }
}
if (jobLabel('render-shape') !== 'Render') {
  failed++;
  console.error(`✗ a copy in another shape is a Render, not “${jobLabel('render-shape')}”`);
}
if (failed) process.exit(1);
console.log(`job labels: all ${types.length} job types are named`);
