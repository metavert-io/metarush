#!/usr/bin/env node
// Hidden characters come out of words by one rule on every surface: the
// dashboard's (cleanCount in web/src/lib/hidden.ts) follows the same cases as
// Go's (plainwords.Clean, internal/plainwords/testdata/hidden.json).
//
//   node --experimental-strip-types web/scripts/hidden-test.mjs
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { cleanCount, shownText, hiddenTitle } = await import(path.join(here, '..', 'src', 'lib', 'hidden.ts'));
const data = JSON.parse(readFileSync(path.join(here, '..', '..', 'internal', 'plainwords', 'testdata', 'hidden.json'), 'utf8'));

let failed = 0;
let checked = 0;
const eq = (name, got, want) => {
  checked++;
  if (got !== want) {
    failed++;
    console.error(`FAIL ${name}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
};

if (data.cases.length < 20) {
  console.error(`only ${data.cases.length} cases`);
  process.exit(1);
}
for (const c of data.cases) {
  const [out, n] = cleanCount(c.in);
  eq(`cleanCount(${JSON.stringify(c.in)}) words`, out, c.out);
  eq(`cleanCount(${JSON.stringify(c.in)}) count`, n, c.removed);
  eq(`shownText(${JSON.stringify(c.in)})`, shownText(c.in), c.out);
  eq(`cleaning ${JSON.stringify(c.in)} twice`, cleanCount(out)[1], 0);
}
// Half of a pair on its own isn't text (JSON's decoders disagree about it, so it is only here).
eq('a lone high half', shownText('a\ud83db'), 'ab');
eq('a lone low half', shownText('\ude0aend'), 'end');
eq('one character', hiddenTitle(1).includes('a hidden character'), true);
eq('several', hiddenTitle(3).includes('3 hidden characters'), true);

if (failed) {
  console.error(`hidden-test: ${failed} of ${checked} checks failed`);
  process.exit(1);
}
console.log(`hidden-test: ${checked} checks passed`);
