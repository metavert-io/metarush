#!/usr/bin/env node
// Links someone may open (D444, web/src/lib/weblink.ts): "Open the original"
// and the panel's cards show an http or https address with a host and no
// sign-in in it, and nothing else — links come from files people import too.
//
//   node --experimental-strip-types web/scripts/weblink-test.mjs
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { webLink } = await import(path.join(here, '..', 'src', 'lib', 'weblink.ts'));

let failed = 0;
const check = (name, ok, got) => {
  if (!ok) {
    failed++;
    console.error(`✗ ${name}${got !== undefined ? `: ${JSON.stringify(got)}` : ''}`);
  }
};
for (const [u, want] of [
  ['https://www.youtube.com/watch?v=refshort001', 'https://www.youtube.com/watch?v=refshort001'],
  ['http://vimeo.com/123', 'http://vimeo.com/123'],
  ['  https://www.tiktok.com/@a/video/1  ', 'https://www.tiktok.com/@a/video/1'],
  ['https://youtube.com@evil.example/', undefined],
  ['https://user:pass@example.com/', undefined],
  ['javascript:alert(1)', undefined],
  ['javascript://%0aalert(1)', undefined],
  ['data:text/html,hi', undefined],
  ['file:///etc/passwd', undefined],
  ['//example.com/x', undefined],
  ['not a link', undefined],
  ['', undefined],
  [undefined, undefined],
]) {
  check(`webLink(${JSON.stringify(u)})`, webLink(u) === want, webLink(u));
}
if (failed) {
  console.error(`${failed} link check(s) failed`);
  process.exit(1);
}
console.log('links someone may open ✓');
