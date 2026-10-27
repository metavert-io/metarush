#!/usr/bin/env node
// Tests for the static read of Remotion sources (analyze.mjs, D132): no
// browser, no project code run. `npm run test:motion -w video` runs it.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { PICTURES_DIR, placePictures } from './serve-pictures.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ANALYZE = path.join(HERE, 'analyze.mjs');
const FIXTURE = path.resolve(HERE, '..', '..', '..', 'tests', 'fixtures', 'remotion', 'fixture-film');

// Every folder a test makes is removed when the run ends, passed or not.
const made = [];
process.on('exit', () => {
  for (const d of made) fs.rmSync(d, { recursive: true, force: true });
});
const tempDir = (prefix) => {
  const d = fs.mkdtempSync(path.join(os.tmpdir(), prefix));
  made.push(d);
  return d;
};

const read = (dir) => JSON.parse(execFileSync(process.execPath, [ANALYZE, 'read', '--work', dir], { encoding: 'utf8' }));
let failed = 0;
const test = (name, fn) => {
  try {
    fn();
    console.log(`ok   ${name}`);
  } catch (e) {
    failed++;
    console.log(`FAIL ${name}\n     ${e.message}`);
  }
};
const project = (files) => {
  const dir = tempDir('metarush-source-test-');
  for (const [rel, text] of Object.entries(files)) {
    fs.mkdirSync(path.dirname(path.join(dir, rel)), { recursive: true });
    fs.writeFileSync(path.join(dir, rel), text);
  }
  return dir;
};

test('the fixture film: entry, compositions, constants, computed lengths, fonts, public files, settings', () => {
  if (!fs.existsSync(FIXTURE)) throw new Error('fixture missing: ' + FIXTURE);
  const r = read(FIXTURE);
  assert.equal(r.entry, 'src/index.ts');
  const byId = Object.fromEntries(r.compositions.map((c) => [c.id, c]));
  assert.deepEqual(Object.keys(byId).sort(), ['Card', 'Film', 'Title']);
  assert.equal(byId.Film.durationInFrames, 90, 'FILM_LENGTH: a sum of constants');
  assert.equal(byId.Title.durationInFrames, 60, 'durationInFrames={2 * FPS} with FPS an imported constant');
  assert.equal(byId.Title.width, 640);
  assert.equal(byId.Title.computed, false);
  assert.equal(byId.Card.computed, true, 'calculateMetadata makes the length computed');
  assert.equal(byId.Card.folder, 'Cards');
  assert.equal(JSON.parse(byId.Card.defaultProps).seconds, 1.5);
  assert.deepEqual(byId.Title.files, ['src/Title.tsx', 'src/story.ts', 'src/theme.ts']);
  assert.deepEqual(r.fonts, [{ module: '@remotion/google-fonts/Anton', family: 'Anton' }]);
  assert.deepEqual(r.staticFiles, ['music/bed.wav', 'picture.png', 'tone.wav']);
  // Its words are kept in one file of constants (D168), and which constants each composition reads.
  assert.deepEqual(r.copy.map((c) => [c.file, c.consts]), [['src/story.ts', ['HEADLINE', 'SUBTITLE', 'OPENING', 'END']]]);
  assert.deepEqual(byId.Film.words, ['END.cta', 'END.tagline', 'OPENING.line', 'OPENING.note']);
  assert.deepEqual(byId.Title.words, ['HEADLINE', 'SUBTITLE']);
  assert.equal(byId.Film.componentName, 'Film');
  assert.deepEqual(r.config.map((c) => c.setting), ['setVideoImageFormat', 'setRspack', 'setOverwriteOutput']);
  assert.equal(r.problems.length, 0);
});

const run = (cmd, dir, extra = []) => JSON.parse(execFileSync(process.execPath, [ANALYZE, cmd, '--work', dir, ...extra], { encoding: 'utf8' }));

/** A copy of the fixture to change (and a stand-in for its Google Font package: the lift reads the font's data, never its code). */
const fixtureCopy = () => {
  const dir = tempDir('metarush-source-fixture-');
  fs.cpSync(FIXTURE, dir, { recursive: true });
  const esm = path.join(dir, 'node_modules', '@remotion', 'google-fonts', 'dist', 'esm');
  fs.mkdirSync(esm, { recursive: true });
  fs.writeFileSync(
    path.join(esm, 'Anton.mjs'),
    'var getInfo = () => ({\n  fontFamily: "Anton",\n  unicodeRanges: { latin: "U+0000-00FF" },\n  fonts: { normal: { "400": { latin: "https://fonts.gstatic.com/s/anton/v25/1Ptgg87LROyAm3Kz-C8.woff2" } } }\n});\nexport { getInfo };\n',
  );
  return dir;
};

test('the map: scenes of a Series with their lengths and where they are written, sounds with their levels (D167)', () => {
  const m = run('map', FIXTURE);
  const film = m.scenes.filter((s) => s.file === 'src/Film.tsx' && s.kind === 'series');
  assert.deepEqual(film.map((s) => [s.name, s.child, s.duration.frames, s.duration.text]), [['Opening', 'Opening', 45, 'FILM_SCENES.opening'], ['End card', 'EndCard', 45, 'FILM_SCENES.end']]);
  const sounds = m.files['src/Film.tsx'].sounds.map((s) => [s.file, s.volume]);
  assert.deepEqual(sounds, [['music/bed.wav', '0.3'], ['tone.wav', '0.8']]);
  const later = m.scenes.find((s) => s.file === 'src/Film.tsx' && s.kind === 'sequence');
  assert.equal(later.from.frames, 51, 'from={FILM_SCENES.opening + 6}');
  assert.equal(m.compositions.find((c) => c.id === 'Film').durationAt.text, 'FILM_LENGTH');
});

test('words as settings: the one-time change wraps the copy constants and writes the helper (D168)', () => {
  const dir = fixtureCopy();
  const out = run('words-setup', dir);
  assert.equal(out.type, 'words-setup');
  const story = out.files.find((f) => f.path === 'src/story.ts');
  const helper = out.files.find((f) => f.path === 'src/metarush-words.ts');
  assert.ok(story && helper, 'the copy file and the helper');
  assert.match(story.text, /export const END = words\("END", \{/);
  assert.match(story.text, /export const HEADLINE = words\("HEADLINE", "Hello from the fixture film"\)/);
  assert.match(story.text, /^import \{ words \} from "\.\/metarush-words";/m);
  assert.match(helper.text, /MetaRush words as settings: /);
  for (const f of out.files) fs.writeFileSync(path.join(dir, f.path), f.text);
  const r = read(dir);
  const keys = r.settings.map((s) => s.key);
  for (const k of ['HEADLINE', 'END.tagline', 'END.cta', 'OPENING.line']) assert.ok(keys.includes(k), `setting ${k}`);
  assert.equal(r.settings.find((s) => s.key === 'END.tagline').text, 'Made with the fixture theme');
  assert.ok(r.wordsHelper, 'the helper is recognized');
});

test('sending changes back: the words helper comes out again, with the settings written in only when asked (D176)', () => {
  const dir = fixtureCopy();
  const original = fs.readFileSync(path.join(dir, 'src/story.ts'), 'utf8');
  for (const f of run('words-setup', dir).files) fs.writeFileSync(path.join(dir, f.path), f.text);
  // Plain: the code's own words, exactly the file it was.
  const plain = run('words-bake', dir);
  assert.equal(plain.type, 'words-bake');
  assert.deepEqual(plain.removed, ['src/metarush-words.ts']);
  assert.deepEqual(plain.problems, []);
  assert.equal(plain.files.find((f) => f.path === 'src/story.ts').text, original, 'unwrapped back to the file it was');
  // With the settings: their words in the strings at their keys.
  const opts = path.join(dir, 'bake.json');
  fs.writeFileSync(opts, JSON.stringify({ values: { 'END.tagline': 'Sent back with "quotes"', HEADLINE: 'A new headline' } }));
  const withWords = run('words-bake', dir, ['--options', opts]).files.find((f) => f.path === 'src/story.ts').text;
  assert.match(withWords, /export const HEADLINE = "A new headline";/);
  assert.match(withWords, /tagline: "Sent back with \\"quotes\\""/);
  assert.match(withWords, /cta: "Watch the next one"/, 'words without a setting stay as written');
  assert.doesNotMatch(withWords, /words\(|metarush-words/);
});

test('pictures as settings: found, wrapped in picture("NAME", …), read back as settings, and taken out exactly (D209)', () => {
  const dir = fixtureCopy();
  const original = fs.readFileSync(path.join(dir, 'src/Title.tsx'), 'utf8');
  const before = read(dir);
  assert.deepEqual(before.pictures, [{ name: 'PICTURE', file: 'public/picture.png', setting: false, uses: [{ file: 'src/Title.tsx', line: 12, how: 'static', wrapped: false }] }]);
  assert.equal(before.picturesHelper, '');
  assert.deepEqual(before.compositions.find((c) => c.id === 'Title').pictures, ['PICTURE']);
  assert.deepEqual(before.compositions.find((c) => c.id === 'Film').pictures, []);
  const out = run('pictures-setup', dir);
  assert.equal(out.type, 'pictures-setup');
  assert.equal(out.helper, 'src/metarush-pictures.ts');
  const title = out.files.find((f) => f.path === 'src/Title.tsx').text;
  assert.match(title, /<Img src=\{picture\("PICTURE", staticFile\("picture\.png"\)\)\}/);
  assert.match(title, /^import \{ picture \} from "\.\/metarush-pictures";$/m);
  assert.match(title, /<Audio src=\{staticFile\("tone\.wav"\)\}/, 'sounds stay as they are');
  assert.match(out.files.find((f) => f.path === 'src/metarush-pictures.ts').text, /MetaRush pictures as settings: /);
  for (const f of out.files) fs.writeFileSync(path.join(dir, f.path), f.text);
  const after = read(dir);
  assert.equal(after.picturesHelper, 'src/metarush-pictures.ts');
  assert.deepEqual(after.pictures.map((p) => [p.name, p.file, p.setting, p.uses.map((u) => u.wrapped)]), [['PICTURE', 'public/picture.png', true, [true]]]);
  assert.deepEqual(after.compositions.find((c) => c.id === 'Title').pictures, ['PICTURE']);
  assert.deepEqual(run('pictures-setup', dir).files, [], 'a second setup has nothing left to wrap');
  // Its name is code, not words on screen (the map).
  assert.ok(!run('map', dir).files['src/Title.tsx'].words.some((w) => w.text === 'PICTURE'));
  const baked = run('bake', dir);
  assert.equal(baked.type, 'bake');
  assert.deepEqual(baked.removed, ['src/metarush-pictures.ts']);
  assert.equal(baked.files.find((f) => f.path === 'src/Title.tsx').text, original, 'unwrapped back to the file it was');
});

test('pictures as settings: imported pictures, clashing names, references worked out as it plays, and both helpers out at once', () => {
  const dir = project({
    'package.json': '{}',
    'public/hero.png': 'x',
    'public/art/hero.png': 'x',
    'src/art/logo-dark.svg': '<svg xmlns="http://www.w3.org/2000/svg"/>',
    'src/index.ts': "import { registerRoot } from 'remotion';\nimport { Root } from './Root';\nregisterRoot(Root);\n",
    'src/Root.tsx': "import React from 'react';\nimport { Composition } from 'remotion';\nimport { Scene } from './Scene';\nexport const Root = () => <Composition id=\"Scene\" component={Scene} durationInFrames={30} fps={30} width={100} height={100} />;\n",
    'src/story.ts': 'export const TITLE = "A film with pictures";\nexport const END = { line: "The end of it" };\n',
    'src/Scene.tsx':
      "import React from 'react';\n" +
      "import * as R from 'remotion';\n" +
      "import { Img, staticFile } from 'remotion';\n" +
      "import logo from './art/logo-dark.svg';\n" +
      "import { TITLE } from './story';\n" +
      '\n' +
      'const picture = 1; // the project has its own name "picture"\n' +
      'export const Scene = ({ who }: { who: string }) => (\n' +
      '  <>\n' +
      '    <Img src={staticFile("hero.png")} />\n' +
      '    <Img src={R.staticFile("/art/hero.png")} />\n' +
      '    <Img src={logo} />\n' +
      '    <Img src={staticFile(`people/${who}.png`)} />\n' +
      '    <Img src={staticFile("missing.png")} />\n' +
      '    <Img src={staticFile("../outside.png")} />\n' +
      '    <div>{TITLE} {picture}</div>\n' +
      '  </>\n' +
      ');\n',
    'src/Early.tsx': "export const early = () => banner;\nimport banner from './art/logo-dark.svg';\nimport { Img } from 'remotion';\nexport const E = () => <Img src={banner} />;\n",
  });
  const originals = Object.fromEntries(['src/Scene.tsx', 'src/story.ts'].map((f) => [f, fs.readFileSync(path.join(dir, f), 'utf8')]));
  const r = read(dir);
  assert.deepEqual(r.pictures.map((p) => [p.name, p.file, p.uses.length]), [['HERO', 'public/hero.png', 1], ['ART_HERO', 'public/art/hero.png', 1], ['LOGO_DARK', 'src/art/logo-dark.svg', 2]].sort((a, b) => a[1].localeCompare(b[1])));
  const early = r.pictures.find((p) => p.name === 'LOGO_DARK').uses.find((u) => u.file === 'src/Early.tsx');
  assert.equal(early.why, 'the code uses it before its imports end');
  assert.deepEqual(r.picturesLeft.map((l) => l.why).sort(), ['it names a file outside the project', 'its file is worked out as the video plays', 'the project has no file public/missing.png']);
  const out = run('pictures-setup', dir);
  assert.deepEqual(out.skipped.map((s) => [s.file, s.name]), [['src/Early.tsx', 'LOGO_DARK']], 'a reference that can\'t be wrapped safely stays code');
  const scene = out.files.find((f) => f.path === 'src/Scene.tsx').text;
  assert.match(scene, /^import logoAsWritten from '\.\/art\/logo-dark\.svg';$/m);
  assert.match(scene, /^import \{ picture as metarushPicture \} from "\.\/metarush-pictures";\nconst logo = metarushPicture\("LOGO_DARK", logoAsWritten\);$/m);
  assert.match(scene, /metarushPicture\("HERO", staticFile\("hero\.png"\)\)/);
  assert.match(scene, /metarushPicture\("ART_HERO", R\.staticFile\("\/art\/hero\.png"\)\)/);
  assert.match(scene, /<Img src=\{staticFile\(`people\/\$\{who\}\.png`\)\} \/>/, 'worked out as it plays: left as it was');
  for (const f of out.files) fs.writeFileSync(path.join(dir, f.path), f.text);
  const r2 = read(dir);
  assert.deepEqual(r2.pictures.filter((p) => p.setting).map((p) => p.name).sort(), ['ART_HERO', 'HERO', 'LOGO_DARK']);
  assert.deepEqual(r2.compositions.find((c) => c.id === 'Scene').pictures, ['ART_HERO', 'HERO', 'LOGO_DARK']);
  // Its words as settings too; sending changes back takes both helpers out.
  for (const f of run('words-setup', dir).files) fs.writeFileSync(path.join(dir, f.path), f.text);
  assert.ok(read(dir).wordsHelper);
  const baked = run('bake', dir);
  assert.deepEqual(baked.removed.sort(), ['src/metarush-pictures.ts', 'src/metarush-words.ts']);
  for (const [f, text] of Object.entries(originals)) assert.equal(baked.files.find((x) => x.path === f).text, text, `${f} as it was`);
  assert.deepEqual(baked.problems, []);
});

test('replaced pictures are served with the bundle by their hash, checked first (D209)', () => {
  const bundle = tempDir('metarush-bundle-');
  const src = path.join(tempDir('metarush-cache-'), 'object');
  fs.writeFileSync(src, 'a picture');
  const sha = crypto.createHash('sha256').update('a picture').digest('hex');
  assert.equal(placePictures(bundle, { [`${sha}.png`]: src }), 1);
  assert.equal(fs.readFileSync(path.join(bundle, PICTURES_DIR, `${sha}.png`), 'utf8'), 'a picture');
  assert.equal(placePictures(bundle, { [`${sha}.png`]: src }), 0, 'already there: kept');
  assert.throws(() => placePictures(bundle, { '../escape.png': src }), /unusual name/);
  assert.throws(() => placePictures(bundle, { [`${'0'.repeat(64)}.png`]: src }), /isn't the file it should be/);
  assert.throws(() => placePictures(bundle, { [`${sha}.html`]: src }), /unusual name/, 'pictures only');
});

test('lifting a scene: the scene becomes a graphic, what it shares a library, its Google Font a resource (D171)', () => {
  const dir = fixtureCopy();
  const opts = path.join(dir, 'lift.json');
  const font = 's/anton/v25/1Ptgg87LROyAm3Kz-C8.woff2';
  fs.writeFileSync(opts, JSON.stringify({ component: 'EndCard', from: 'src/scenes/EndCard.tsx', library: 'film-theme', name: 'end-card', source: 'Fixture film', fonts: { [font]: 'anton' }, width: 640, height: 360, seconds: 1.5 }));
  const r = run('lift', dir, ['--options', opts]);
  assert.equal(r.ok, true, JSON.stringify(r.problems));
  assert.deepEqual(r.params, ['tagline', 'cta']);
  assert.deepEqual(r.preview, { tagline: 'Made with the fixture theme', cta: 'Watch the next one' });
  assert.deepEqual(r.graphic.files.map((f) => f.path).sort(), ['EndCard.tsx', 'index.tsx', 'motion.json', 'preview.json']);
  const scene = r.graphic.files.find((f) => f.path === 'EndCard.tsx').text;
  assert.match(scene, /from "@metarush\/lib\/film-theme";/);
  assert.doesNotMatch(scene, /from "\.\.\//, 'no import leaves the graphic');
  const lib = Object.fromEntries(r.library.files.map((f) => [f.path, f.text]));
  assert.deepEqual(Object.keys(lib).sort(), ['components/Pill.tsx', 'index.tsx', 'library.json', 'metarush-fonts.tsx', 'story.ts', 'theme.ts']);
  assert.match(lib['theme.ts'], /const anton = \{ fontFamily: "Anton" \};/);
  assert.doesNotMatch(lib['theme.ts'], /google-fonts/);
  assert.match(lib['metarush-fonts.tsx'], /useFont\("anton", "Anton"/);
  assert.equal(JSON.parse(lib['library.json']).typescript.noUncheckedIndexedAccess, false, 'checked the way the project checked it');
  assert.deepEqual(r.fonts.map((f) => [f.path, f.family, f.weights]), [[font, 'Anton', [400]]]);
  assert.match(r.graphic.files.find((f) => f.path === 'index.tsx').text, /<Sequence width=\{W\} height=\{H\} layout="none"/);
});

test('lifting refuses scenes that play sounds or draw the project files', () => {
  const dir = fixtureCopy();
  const opts = path.join(dir, 'lift.json');
  fs.writeFileSync(opts, JSON.stringify({ composition: 'Title', library: 'film-theme' }));
  const r = run('lift', dir, ['--options', opts]);
  assert.equal(r.ok, false);
  assert.deepEqual([...new Set(r.problems.map((p) => p.code))].sort(), ['media', 'project_files']);
  assert.ok(r.problems.every((p) => p.file === 'src/Title.tsx' && p.line > 0));
});

test('inline loaders and imports from web addresses are refused (the bundler never runs them)', () => {
  const dir = project({
    'package.json': '{}',
    'src/index.ts': "import { registerRoot } from 'remotion';\nimport { R } from './Root';\nregisterRoot(R);\n",
    'src/Root.tsx': "import raw from '!!raw-loader!./notes.txt';\nimport x from 'https://cdn.example/x.js';\nexport const R = () => null;\n",
  });
  const r = read(dir);
  assert.deepEqual(r.problems.map((p) => p.code).sort(), ['inline_loader', 'remote_import']);
  assert.ok(r.problems.every((p) => p.severity === 'error' && p.file === 'src/Root.tsx' && p.line > 0));
});

test('computed ids, dynamic imports and a missing entry are reported', () => {
  const dir = project({
    'package.json': '{}',
    'src/Root.tsx': "import { Composition } from 'remotion';\nconst id = 'x' + 1;\nexport const R = () => <Composition id={id} component={() => null} durationInFrames={10} fps={30} width={100} height={100} />;\nexport const later = () => import('lodash');\n",
  });
  const r = read(dir);
  const codes = r.problems.map((p) => p.code).sort();
  assert.deepEqual(codes, ['composition_id', 'dynamic_import', 'no_entry']);
});

test('the read never runs project code', () => {
  const marker = path.join(os.tmpdir(), `metarush-ran-${process.pid}`);
  const dir = project({
    'package.json': '{}',
    'src/index.ts': `import fs from 'node:fs';\nfs.writeFileSync(${JSON.stringify(marker)}, 'ran');\nimport { registerRoot } from 'remotion';\nregisterRoot(() => null);\n`,
    'remotion.config.ts': `require('node:fs').writeFileSync(${JSON.stringify(marker)}, 'ran');\n`,
  });
  read(dir);
  assert.equal(fs.existsSync(marker), false);
});

if (failed) {
  console.log(`${failed} failed`);
  process.exit(1);
}
console.log('remotion source read: all passed');
