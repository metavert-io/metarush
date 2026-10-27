#!/usr/bin/env node
// The preview plays a voice's clearer copy (variant) at the renderer's level:
// the mono compensation (web/src/player/compensate.ts, D64) looks at the file
// actually heard — a mono camera recording plays from a stereo proxy (nothing
// to do) but its clearer copy from a mono file (−3 dB, like the renderer).
//
//   node --experimental-strip-types web/scripts/sound-test.mjs
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { compensateMono, MONO_PREVIEW_GAIN } = await import(path.join(here, '..', 'src', 'player', 'compensate.ts'));

let failed = 0;
let checked = 0;
const eq = (name, got, want) => {
  checked++;
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    failed++;
    console.error(`FAIL ${name}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
};
const near = (a, b) => Math.abs(a - b) < 1e-9;

const env = [{ f: 0, g: 1 }, { f: 89, g: 1 }];
const clip = (variant) => ({
  id: 'talk', assetId: 'ast_rec', from: 0, durationInFrames: 90, volume: 1, gain: env,
  audio: { from: 0, durationInFrames: 90, trimBefore: 0, blendInFrames: 0, blendOutFrames: 0, gain: env, ...(variant ? { variant } : {}) },
});
const plan = (variant, sources) => ({
  clips: [clip(variant)],
  audio: [{ id: 'dlg', assetId: 'ast_dlg', gain: env, gainDb: 0, ...(variant ? { variant: { assetId: 'ast_dlg_copy', trimBefore: 0 } } : {}) }],
  sources,
  // The camera recording plays from its stereo proxy; the copies are mono WAVs.
  sourceChannels: { ast_rec: 2, ast_dlg: 2, ast_copy: 1, ast_dlg_copy: 1 },
});

// Without a copy: stereo proxies play as they are.
const plain = compensateMono(plan(null, { ast_rec: 'r', ast_dlg: 'd' }));
eq('recording from a stereo proxy: unchanged', plain.clips[0].audio.gain[0].g, 1);
// With the copy: its mono file plays −3 dB, like the renderer's equal-power spread.
const withCopy = compensateMono(plan({ assetId: 'ast_copy', trimBefore: 0 }, { ast_rec: 'r', ast_dlg: 'd', ast_copy: 'c', ast_dlg_copy: 'dc' }));
eq('a clip sound playing its mono copy is compensated', near(withCopy.clips[0].audio.gain[0].g, MONO_PREVIEW_GAIN), true);
eq('a dialogue track playing its mono copy is compensated', near(withCopy.audio[0].gain[0].g, MONO_PREVIEW_GAIN), true);
// A copy the plan has no file for: the recording plays (the preview falls back), unchanged.
const missing = compensateMono(plan({ assetId: 'ast_copy', trimBefore: 0 }, { ast_rec: 'r', ast_dlg: 'd' }));
eq('a copy without a file falls back to the recording', missing.clips[0].audio.gain[0].g, 1);

console.log(`${failed ? 'FAIL' : 'ok  '} clearer copies in the preview: ${checked - failed}/${checked} checks`);
if (failed) process.exit(1);
