#!/usr/bin/env node
// Runs the Script view's reader (web/src/screens/studio/cutsheet/script.ts) on
// the cut sheets the Go parser is tested with (internal/cutsheet/testdata):
// every clip, transition, narration line, graphic, sound and caption there is
// recognized (nothing falls back to plain Markdown), clip lines line up with
// the spec's clips, and the derived lines read as who says what.
//
//   node --experimental-strip-types web/scripts/script-test.mjs
import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { parseScript, parseSpeech, splitParts, withFields, sheetSeconds, templateName, motionUse, motionPins, motionState } = await import(path.join(here, '..', 'src', 'screens', 'studio', 'cutsheet', 'script.ts'));
const dir = path.join(here, '..', '..', 'internal', 'cutsheet', 'testdata');
let failed = 0;
let checked = 0;
const fail = (msg) => {
  failed++;
  console.error('FAIL', msg);
};
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

for (const f of readdirSync(dir).filter((n) => n.endsWith('.cut.md'))) {
  const text = readFileSync(path.join(dir, f), 'utf8');
  const spec = JSON.parse(readFileSync(path.join(dir, f.replace(/\.cut\.md$/, '.spec.json')), 'utf8'));
  const doc = parseScript(text);
  checked++;
  if (!doc.settings.length) fail(`${f}: no settings read`);
  if (spec.title && doc.title !== spec.title) fail(`${f}: title ${JSON.stringify(doc.title)}, want ${JSON.stringify(spec.title)}`);
  for (const s of doc.sections) {
    if (s.kind === 'other') fail(`${f}: unknown section ${s.title}`);
    for (const it of s.items) if (it.kind === 'markdown') fail(`${f}: ${s.title} has unrecognized text: ${JSON.stringify(it.text.slice(0, 80))}`);
  }
  const clips = doc.sections.find((s) => s.kind === 'clips')?.items.filter((i) => i.kind === 'clip') ?? [];
  if (clips.length !== (spec.clips ?? []).length) fail(`${f}: ${clips.length} clip lines, spec has ${(spec.clips ?? []).length}`);
  clips.forEach((c, i) => {
    const sc = spec.clips[i];
    if (sc && c.name && c.name !== sc.id) fail(`${f}: clip ${i + 1} is ${c.name}, spec says ${sc.id}`);
    if (!c.footage || !c.range) fail(`${f}: clip ${i + 1} footage/range missing: ${JSON.stringify(c)}`);
    const [a, b] = c.range.split(/\s*(?:→|->)\s*/);
    if (sc && sheetSeconds(a ?? '') !== undefined && Math.abs(sheetSeconds(a) - sc.in) > 0.0005) fail(`${f}: clip ${c.name} starts ${a}, spec ${sc.in}`);
    if (sc && sheetSeconds(b ?? '') !== undefined && Math.abs(sheetSeconds(b) - sc.out) > 0.0005) fail(`${f}: clip ${c.name} ends ${b}, spec ${sc.out}`);
  });
  const graphics = doc.sections.find((s) => s.kind === 'graphics')?.items.filter((i) => i.kind === 'bullet') ?? [];
  if (graphics.length !== (spec.overlays ?? []).length) fail(`${f}: ${graphics.length} graphics, spec has ${(spec.overlays ?? []).length}`);
  const pins = motionPins(doc);
  graphics.forEach((g, i) => {
    const w = g.settings.find((p) => p.startsWith('with '));
    if (w && !withFields(w)) fail(`${f}: graphic ${g.name} fields not read: ${w}`);
    // A project motion graphic: the line names it and its version, the settings pin the full version.
    const m = spec.overlays?.[i]?.motion;
    const use = motionUse(g.head);
    if (m && (!use || use.graphic !== m.name || !m.hash.startsWith(use.version) || pins.get(m.name)?.hash !== m.hash)) fail(`${f}: motion graphic ${g.name} read as ${JSON.stringify(use)}, pins ${JSON.stringify([...pins])}`);
    if (!m && use) fail(`${f}: template line ${g.name} read as a motion graphic`);
  });
  const sound = doc.sections.find((s) => s.kind === 'sound')?.items.filter((i) => i.kind === 'bullet') ?? [];
  if (sound.length !== (spec.audio ?? []).length) fail(`${f}: ${sound.length} sounds, spec has ${(spec.audio ?? []).length}`);
  const vo = doc.sections.find((s) => s.kind === 'voiceover')?.items.filter((i) => i.kind === 'bullet') ?? [];
  const beats = spec.voiceover?.beats ?? [];
  if (vo.length !== beats.length) fail(`${f}: ${vo.length} narration lines, spec has ${beats.length}`);
  vo.forEach((l, i) => {
    if (beats[i] && l.words !== beats[i].text) fail(`${f}: narration ${i + 1} words ${JSON.stringify(l.words)}, want ${JSON.stringify(beats[i].text)}`);
  });
}

// The examples in docs/cut-sheet.md (the complete sheet and the narration section).
{
  const md = readFileSync(path.join(here, '..', '..', 'docs', 'cut-sheet.md'), 'utf8');
  const blocks = [...md.matchAll(/```markdown\n([\s\S]*?)```/g)].map((m) => m[1]);
  const full = parseScript(blocks[0]);
  checked++;
  const vo = full.sections.find((s) => s.kind === 'voiceover');
  const lines = vo?.items.filter((i) => i.kind === 'bullet') ?? [];
  if (!same(lines.map((l) => l.words), ['Nobody watched her first robot fail.', 'So she built it again.'])) fail(`doc example narration: ${JSON.stringify(lines.map((l) => l.words))}`);
  if (lines[0]?.settings[0] !== '0.4 s after twice starts') fail(`doc example start point: ${JSON.stringify(lines[0]?.settings)}`);
  if (!lines[0]?.labeled.some((x) => x.label === 'Delivery' && x.text === 'quiet, a little amused')) fail(`doc example delivery: ${JSON.stringify(lines[0]?.labeled)}`);
  if (lines[0]?.derived[0] !== 'starts at 0:06.8 · fits (2.3 of 3 s)') fail(`doc example placement: ${JSON.stringify(lines[0]?.derived)}`);
  const hook = full.sections.find((s) => s.kind === 'clips')?.items.find((i) => i.kind === 'clip');
  if (!same(hook?.speech, [{ speaker: 'Ada', text: '“When I started building my …” … “… robot I had no idea.”' }])) fail(`doc example speech: ${JSON.stringify(hook?.speech)}`);
  if (hook?.why !== 'Opens on the confession; the doubt is the hook.' || hook?.evidence.length !== 2 || hook?.notes[0] !== "The director likes the breath before 'I had no idea'. Keep it.") fail(`doc example clip body: ${JSON.stringify(hook)}`);
  const narration = parseScript(`---\ncut: 1\n---\n\n${blocks.find((b) => b.startsWith('## Voiceover'))}`);
  checked++;
  const ns = narration.sections[0];
  if (!ns?.recording?.startsWith('narration-take')) fail(`narration recording: ${JSON.stringify(ns?.recording)}`);
  const nl = ns?.items.filter((i) => i.kind === 'bullet') ?? [];
  if (nl.length !== 5 || nl[1]?.name !== undefined || nl[1]?.words !== 'So we built it twice.' || !nl[0]?.labeled.some((x) => x.label === 'Take')) fail(`narration lines: ${JSON.stringify(nl.map((l) => [l.name, l.words, l.settings]))}`);
  if (ns?.items.some((i) => i.kind === 'markdown')) fail('narration example has unrecognized text');
}

// Who says what in a derived line.
const speech = [
  ['Ada: “So what would you build?” Speaker 2: “A robot.”', [{ speaker: 'Ada', text: '“So what would you build?”' }, { speaker: 'Speaker 2', text: '“A robot.”' }]],
  ['“When I started building my …” … “… robot I had no idea.”', [{ speaker: '', text: '“When I started building my …” … “… robot I had no idea.”' }]],
  ['Maya Lindqvist: “Welcome in.”', [{ speaker: 'Maya Lindqvist', text: '“Welcome in.”' }]],
  ['Speaker 1: “So tell me how the …” … Speaker 2: “… if they could buy one.”', [{ speaker: 'Speaker 1', text: '“So tell me how the …”' }, { speaker: 'Speaker 2', text: '“… if they could buy one.”' }]],
  ['José Núñez: “Hola.” … Dr. O\'Neil: “Hi.”', [{ speaker: 'José Núñez', text: '“Hola.”' }, { speaker: "Dr. O'Neil", text: '“Hi.”' }]],
];
for (const [line, want] of speech) {
  const got = parseSpeech(line);
  if (!same(got, want)) fail(`speech ${line}: got ${JSON.stringify(got)}`);
}
// Phrases after the picture transition are kept as written (split edits).
const parts = splitParts('cut · Ada\'s voice starts 0.4 s early · label "a · b" · {x: [1, 2]}');
if (!same(parts, ['cut', "Ada's voice starts 0.4 s early", 'label "a · b"', '{x: [1, 2]}'])) fail(`parts: ${JSON.stringify(parts)}`);
if (templateName('lower-third.clean') !== 'Lower third, clean') fail(`template name: ${templateName('lower-third.clean')}`);
if (templateName('slide.code') !== 'Code slide' || templateName('slide.screenshot') !== 'Screenshot slide') fail(`slide names: ${templateName('slide.code')}, ${templateName('slide.screenshot')}`);
// Motion graphics: the other spellings of a line, and the approval note after a pinned version.
{
  const v = 'a1b2c3d4'.repeat(8);
  const sheet = `---\ncut: 1\ntitle: x\nmotion:\n  count-up: sha256:${v}   # approved by Jean-Luc · 18 Oct 2026\n  route-map: sha256:${'f'.repeat(64)}   # waiting for a person to approve it\n---\n\n## Graphics\n\n` +
    `- subs: motion count-up@a1b2c3d4 at 0:02 for 4 s · with target 1000, label "subscribers"\n- motion: route-map@ffffffff at 0:12 for 4 s\n- map: route-map@${'f'.repeat(64)} at 0:20 for 3 s\n`;
  const doc = parseScript(sheet);
  checked++;
  const lines = doc.sections[0]?.items ?? [];
  const uses = lines.map((l) => (l.kind === 'bullet' ? motionUse(l.head) : undefined));
  if (!same(uses.map((u) => u && [u.graphic, u.version.slice(0, 8), u.when.join(' ')]), [['count-up', 'a1b2c3d4', 'at 0:02 for 4 s'], ['route-map', 'ffffffff', 'at 0:12 for 4 s'], ['route-map', 'ffffffff', 'at 0:20 for 3 s']])) fail(`motion lines: ${JSON.stringify(uses)}`);
  const pins = motionPins(doc);
  if (pins.get('count-up')?.hash !== v || pins.get('count-up')?.note !== 'approved by Jean-Luc · 18 Oct 2026') fail(`motion pin: ${JSON.stringify([...pins])}`);
  if (!same(['approved by Jean-Luc · 18 Oct 2026', 'waiting for a person to approve it', 'approval withdrawn', 'turned down', 'not checked in this workspace', undefined].map(motionState), ['approved', 'waiting', 'blocked', 'blocked', 'blocked', 'unknown'])) fail('motion states');
  if (motionUse('lower-third.clean at 0:01 for 3 s') || motionUse('motion at 0:01')) fail('a template line read as a motion graphic');
}
// Unknown text stays visible as Markdown.
const odd = parseScript('---\ncut: 1\ntitle: x\n---\n\n## Clips\n\nSomething new here\n\n1. a: cam 0:01 → 0:02\n\n## Extras\n\n- a list\n');
const clipsSec = odd.sections[0];
if (!clipsSec.items.some((i) => i.kind === 'markdown' && i.text.includes('Something new'))) fail('unrecognized clip text is not kept');
if (odd.sections[1].kind !== 'other' || odd.sections[1].items[0]?.kind !== 'markdown') fail('unknown section is not Markdown');

if (failed) {
  console.error(`script: ${failed} failures. A new cut sheet construct? Teach web/src/screens/studio/cutsheet/script.ts (and ScriptView.tsx) to show it.`);
  process.exit(1);
}
console.log(`script: ${checked} cut sheets read like the Go parser reads them`);
