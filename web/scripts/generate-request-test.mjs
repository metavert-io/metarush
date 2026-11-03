#!/usr/bin/env node
// The Generate panel's request for a shot (D385): it names a shape only when
// the person chose one, so MetaRush makes the shot in the shape of the cut it
// is for; the panel shows the shape MetaRush will make before the price is
// agreed, and offers the cut's when the person chose another.
//
//   node --experimental-strip-types web/scripts/generate-request-test.mjs
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { shotRequest, shownAspect, chosenAgainstCut, frameRef, agreedRequest } = await import(path.join(here, '..', 'src', 'components', 'generate', 'shot.ts'));

let failed = 0;
const check = (name, ok, got) => {
  if (!ok) {
    failed++;
    console.error(`✗ ${name}${got !== undefined ? `: ${JSON.stringify(got)}` : ''}`);
  }
};

const base = { projectId: 'prj_00000000000000000000000000', prompt: '  a paper boat  ', seconds: 6, aspect: '', quality: 'draft', resolution: '720p', takes: '1' };

// Nobody chose a shape: none is sent, so the server makes it in the cut's.
const auto = shotRequest(base);
check('no shape is sent when none was chosen', !('aspectRatio' in auto), auto);
check('the prompt is trimmed', auto.prompt === 'a paper boat', auto.prompt);
check('takes are a number', auto.takes === 1, auto.takes);
check('nothing extra is sent', JSON.stringify(Object.keys(auto).sort()) === JSON.stringify(['projectId', 'prompt', 'quality', 'resolution', 'seconds', 'takes']), Object.keys(auto));

// A person chose: exactly that shape is sent.
check('a chosen tall shape is sent', shotRequest({ ...base, aspect: '9:16' }).aspectRatio === '9:16');
check('a chosen wide shape is sent', shotRequest({ ...base, aspect: '16:9' }).aspectRatio === '16:9');

// A look only without a start frame; frames as references.
check('a look without frames', shotRequest({ ...base, look: 'ada' }).look === 'ada');
const framed = shotRequest({ ...base, look: 'ada', start: { assetId: 'ast_1', at: 2.5 }, end: { picture: 'logo' } });
check('frames replace the look', !('look' in framed) && framed.from.assetId === 'ast_1' && framed.from.at === 2.5 && framed.to.picture === 'logo', framed);
check('a start frame alone', !('to' in shotRequest({ ...base, end: { picture: 'x' } })));
check('frameRef of a picture', JSON.stringify(frameRef({ picture: 'p', assetId: 'ast_x' })) === '{"picture":"p"}');
check('no people only when said', shotRequest({ ...base, noPeople: true }).noPeople === true && !('noPeople' in shotRequest(base)));

// The price is agreed for the shape the estimate showed (D394): both are sent.
const agreed = agreedRequest(auto, { usd: 0.13, aspectRatio: '9:16' });
check('the agreed price carries the shape it was shown for', agreed.maxUsd === 0.13 && agreed.aspectRatio === '9:16' && agreed.prompt === 'a paper boat', agreed);
check('the estimate only priced: the request it came from has no shape', !('aspectRatio' in auto));
check('a chosen shape stays the one sent', agreedRequest(shotRequest({ ...base, aspect: '16:9' }), { usd: 0.2, aspectRatio: '16:9' }).aspectRatio === '16:9');
check('no shape in the estimate: none invented', !('aspectRatio' in agreedRequest(auto, { usd: 0.1, aspectRatio: '' })));

// What the panel shows: the server's shape until the person chooses.
check('shows the shape MetaRush will make', shownAspect('', { aspectRatio: '9:16' }) === '9:16');
check('shows the person’s choice', shownAspect('16:9', { aspectRatio: '9:16' }) === '16:9');
check('shows nothing before the price', shownAspect('', null) === '');
check('a shape the cut would not get is noticed', chosenAgainstCut('16:9', { fitsCut: '9:16' }) === true);
check('the cut’s own shape is not', chosenAgainstCut('9:16', { fitsCut: '9:16' }) === false);
check('nothing chosen is never against the cut', chosenAgainstCut('', { fitsCut: '9:16' }) === false);
check('a project that says nothing', chosenAgainstCut('16:9', {}) === false);

if (failed) {
  console.error(`${failed} generate request check(s) failed`);
  process.exit(1);
}
console.log('generate request: all checks passed');
