#!/usr/bin/env node
// Which browsers show a cut-out's see-through parts (D510): the one rule the
// renderer's preview and the dashboard's notes read (video/src/clips/
// seethrough.ts). Safari plays VP9 without its alpha channel, and every
// browser on an iPhone or iPad is Safari's engine whatever it is called — an
// iPad's also when it says it is a Mac.
//
//   node --experimental-strip-types web/scripts/seethrough-test.mjs
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { seeThroughIn } = await import(path.join(here, '..', '..', 'video', 'src', 'clips', 'seethrough.ts'));

const cases = [
  ['Chrome on a Mac', 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36', 0, true],
  ['Edge on Windows', 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.0.0', 0, true],
  ['Firefox on Linux', 'Mozilla/5.0 (X11; Linux x86_64; rv:144.0) Gecko/20100101 Firefox/144.0', 0, true],
  ['Chrome on Android', 'Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36', 5, true],
  ['Safari on a Mac', 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.1 Safari/605.1.15', 0, false],
  ['Safari on an iPhone', 'Mozilla/5.0 (iPhone; CPU iPhone OS 26_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.1 Mobile/15E148 Safari/604.1', 5, false],
  ['Chrome on an iPhone', 'Mozilla/5.0 (iPhone; CPU iPhone OS 26_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/141.0.7390.41 Mobile/15E148 Safari/604.1', 5, false],
  ['Firefox on an iPhone', 'Mozilla/5.0 (iPhone; CPU iPhone OS 26_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/144.0 Mobile/15E148 Safari/605.1.15', 5, false],
  ['Edge on an iPhone', 'Mozilla/5.0 (iPhone; CPU iPhone OS 26_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 EdgiOS/141.0.3537.57 Mobile/15E148 Safari/605.1.15', 5, false],
  ['Chrome on an iPad', 'Mozilla/5.0 (iPad; CPU OS 26_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/141.0.7390.41 Mobile/15E148 Safari/604.1', 5, false],
  ['Safari on an iPad, as a Mac', 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.1 Safari/605.1.15', 5, false],
  ['Chrome on an iPad, as a Mac', 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/141.0.7390.41 Safari/604.1', 5, false],
];

let failed = 0;
for (const [name, ua, touch, want] of cases) {
  const got = seeThroughIn(ua, touch);
  if (got !== want) {
    failed++;
    console.error(`FAIL ${name}: ${got}, want ${want}`);
  }
}
if (failed) process.exit(1);
console.log(`see-through video: ${cases.length} browsers ok`);
