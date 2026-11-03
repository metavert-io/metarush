#!/usr/bin/env node
// The one shape rule (D376): the dashboard's twin (web/src/lib/shape.ts) says
// every shape, size, ratio and preset the way Go's editspec.ShapeOf and
// Output.Shape do — the shared cases in internal/editspec/testdata/shapes.json
// — and a frame is shown at its own ratio (frameAspect), never a fixed box.
//
//   node --experimental-strip-types web/scripts/shape-test.mjs
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { shapeOf, sizeShape, outputShape, presetFor, frameAspect, shapeWord, shapeMaxWidth } = await import(path.join(here, '..', 'src', 'lib', 'shape.ts'));
const v = JSON.parse(readFileSync(path.join(here, '..', '..', 'internal', 'editspec', 'testdata', 'shapes.json'), 'utf8'));

let failed = 0;
const check = (name, ok, got) => {
  if (!ok) {
    failed++;
    console.error(`✗ ${name}${got !== undefined ? `: ${JSON.stringify(got)}` : ''}`);
  }
};

check('there are cases', v.sizes.length > 10 && v.outputs.length > 4);
for (const c of v.sizes) {
  const want = c.shape || undefined;
  check(`shapeOf(${c.width}, ${c.height}) = ${want}`, shapeOf(c.width, c.height) === want, shapeOf(c.width, c.height));
  const s = sizeShape(c.width, c.height);
  if (!want) {
    check(`sizeShape(${c.width}, ${c.height}) is nothing`, s === undefined, s);
    continue;
  }
  check(`sizeShape(${c.width}, ${c.height})`, s && s.shape === c.shape && s.word === c.word && s.ratio === c.ratio && s.aspect === c.aspect && s.width === c.width && s.height === c.height, s);
  check(`shapeWord(${c.shape}) = ${c.word}`, shapeWord(c.shape) === c.word, shapeWord(c.shape));
}
for (const c of v.outputs) {
  const s = outputShape(c.output);
  if (!c.shape) check(`outputShape(${JSON.stringify(c.output)}) is nothing`, s === undefined, s);
  else check(`outputShape(${JSON.stringify(c.output)})`, s && s.shape === c.shape && s.width === c.width && s.height === c.height, s);
}
for (const [shape, preset] of Object.entries(v.presets)) check(`presetFor(${shape}) = ${preset}`, presetFor(shape) === preset, presetFor(shape));

// A frame at its own ratio: its size, else what the picture turned out to be, else the box while it loads.
check('a frame with its size', frameAspect({ width: 1080, height: 1350 }) === '1080 / 1350', frameAspect({ width: 1080, height: 1350 }));
check('a frame learned once loaded', frameAspect({}, { width: 720, height: 1280 }) === '720 / 1280', frameAspect({}, { width: 720, height: 1280 }));
check('its own size wins over the picture', frameAspect({ width: 1920, height: 1080 }, { width: 10, height: 10 }) === '1920 / 1080');
check('nothing known: the box while it loads', frameAspect(undefined, null) === '16 / 9' && frameAspect(null, null, '1 / 1') === '1 / 1');
check('tall boxes are narrower', shapeMaxWidth({ width: 1080, height: 1920 }, 'w', 't', 's') === 't' && shapeMaxWidth({ width: 1080, height: 1080 }, 'w', 't', 's') === 's' && shapeMaxWidth(undefined, 'w', 't', 's') === 'w');

if (failed) {
  console.error(`${failed} shape case(s) failed`);
  process.exit(1);
}
console.log(`shapes: ${v.sizes.length + v.outputs.length} shared cases agree with Go`);
