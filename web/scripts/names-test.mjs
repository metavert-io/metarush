#!/usr/bin/env node
// Runs the person picker's matching (web/src/lib/names.ts) against the table
// the Go matcher is tested with (internal/names/testdata/cases.json), so the
// dashboard and the CLI rank and resolve names the same way.
//
//   node --experimental-strip-types web/scripts/names-test.mjs
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { rank, resolve, close } = await import(path.join(here, '..', 'src', 'lib', 'names.ts'));
const table = JSON.parse(readFileSync(path.join(here, '..', '..', 'internal', 'names', 'testdata', 'cases.json'), 'utf8'));
const people = table.people.map((name) => ({ id: name, name }));
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
let failed = 0;
const fail = (msg) => {
  failed++;
  console.error('FAIL', msg);
};

for (const c of table.rank) {
  const hits = rank(c.query, people);
  const names = hits.map((h) => h.candidate.name);
  const kinds = hits.map((h) => h.kind);
  if (!same(names, c.names) || !same(kinds, c.kinds)) fail(`rank ${JSON.stringify(c.query)}: got ${JSON.stringify(names)} ${JSON.stringify(kinds)}, want ${JSON.stringify(c.names)} ${JSON.stringify(c.kinds)}`);
  for (const h of hits) for (const [a, b] of h.marks) if (!(a >= 0 && b > a && b <= h.candidate.name.length)) fail(`rank ${c.query}: bad mark ${a}-${b} on ${h.candidate.name}`);
}
for (const c of table.resolve) {
  const r = resolve(c.query, people);
  if (c.found) {
    if (r.found?.name !== c.found) fail(`resolve ${JSON.stringify(c.query)}: got ${JSON.stringify(r)}, want ${c.found}`);
  } else if (c.ambiguous) {
    if (r.found || !same((r.ambiguous ?? []).map((x) => x.name), c.ambiguous)) fail(`resolve ${JSON.stringify(c.query)}: got ${JSON.stringify(r)}, want ambiguous ${JSON.stringify(c.ambiguous)}`);
  } else if (r.found || r.ambiguous || !same(r.suggest.map((x) => x.name), c.suggest ?? [])) {
    fail(`resolve ${JSON.stringify(c.query)}: got ${JSON.stringify(r)}, want suggestions ${JSON.stringify(c.suggest)}`);
  }
}
for (const c of table.close) {
  const got = close(c.query, people).map((x) => x.name);
  if (!same(got, c.names)) fail(`close ${JSON.stringify(c.query)}: got ${JSON.stringify(got)}, want ${JSON.stringify(c.names)}`);
}
// Highlights land on the matched letters of the original name (accents kept).
const mark = (q, name) => {
  const h = rank(q, [{ id: name, name }])[0];
  return h ? h.marks.map(([a, b]) => name.slice(a, b)) : null;
};
for (const [q, name, want] of [
  ['hart', 'Theo Hartley', ['Hart']],
  ['zoe', 'Zoë Ibáñez', ['Zoë']],
  ['jean luc', 'Jean-Luc Ferrand', ['Jean-Luc']],
  ['may lind', 'Maya Lindqvist', ['May', 'Lind']],
  ['ovel', 'Ada Lovelace', ['ovel']],
]) {
  const got = mark(q, name);
  if (!same(got, want)) fail(`marks ${q} in ${name}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
}
const total = table.rank.length + table.resolve.length + table.close.length + 5;
if (failed) {
  console.error(`${failed} of ${total} name-matching cases failed`);
  process.exit(1);
}
console.log(`names: ${total} cases agree with the Go matcher`);
