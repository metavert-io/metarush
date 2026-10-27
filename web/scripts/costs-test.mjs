#!/usr/bin/env node
// Estimates of what outside AI services cost (D208): the dashboard says an
// amount the way Go's costs.Money does (the shared cases in
// internal/costs/testdata/money.json), a part made before is free, and the
// parts group by step, service, model and what they make, in order.
//
//   node --experimental-strip-types web/scripts/costs-test.mjs
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { groupParts, money, partPrice } = await import(path.join(here, '..', 'src', 'lib', 'costs.ts'));
const { cases } = JSON.parse(readFileSync(path.join(here, '..', '..', 'internal', 'costs', 'testdata', 'money.json'), 'utf8'));

let failed = 0;
const check = (name, ok, got) => {
  if (!ok) {
    failed++;
    console.error(`✗ ${name}${got !== undefined ? `: ${JSON.stringify(got)}` : ''}`);
  }
};

for (const c of cases) check(`money(${c.usd}) = ${c.money}`, money(c.usd) === c.money, money(c.usd));
check('a part made before is free', partPrice({ usd: 0.4, free: true }) === 'free (made before)', partPrice({ usd: 0.4, free: true }));
check('a part under a cent', partPrice({ usd: 0.004 }) === 'less than a cent', partPrice({ usd: 0.004 }));

const part = (o) => ({ amount: 1, unit: 'credits', units: '1 credit', usd: 0.01, ...o });
const groups = groupParts([
  part({ service: 'ElevenLabs', model: 'Eleven Multilingual v2', what: 'the AI voice (Ada)', source: 'Line 1: “Hello”', rate: '$0.08 per 1,000 credits' }),
  part({ service: 'Google Gemini', model: 'Gemini Omni 1.1 Flash', what: 'a generated shot', source: '“A paper boat”' }),
  part({ service: 'ElevenLabs', model: 'Eleven Multilingual v2', what: 'the AI voice (Ada)', source: 'Line 2: “Again”', rate: '$0.08 per 1,000 credits' }),
  part({ step: 'Step 3: the end card', service: 'ElevenLabs', model: 'Eleven Multilingual v2', what: 'the AI voice (Ada)', source: 'Line 1: “Bye”' }),
]);
check(
  'parts group by step, service, model and what, in order',
  groups.length === 3 && groups[0].parts.length === 2 && groups[0].service === 'ElevenLabs' && groups[1].service === 'Google Gemini' && groups[2].step === 'Step 3: the end card',
  groups.map((g) => `${g.step ?? ''}|${g.service}|${g.parts.length}`),
);
check('a group says its price once', groups[0].rates.length === 1 && groups[1].rates.length === 0, groups.map((g) => g.rates));

if (failed) {
  console.error(`${failed} estimate check(s) failed`);
  process.exit(1);
}
console.log(`estimates: ${cases.length + 4} checks passed`);
