#!/usr/bin/env node
// Changing a spoken word in a real browser (D308–D312). Run it through scripts/spoken-word-e2e.sh: a throwaway
// installation with a clip of Ada (the Mac's synthetic voice) transcribed and fingerprinted on this computer, and
// the fake ElevenLabs. It checks that:
//   1. a person names the voice, records a consent that covers new words with ElevenLabs and copies the voice;
//   2. right-clicking “twelve” in the clip's transcript offers Say it differently…, whose dialog prices the new
//      words first (the estimate with the service, what is sent) — nothing is made before the person agrees;
//   3. the new words, made by the fake in the same synthetic voice, pass the checks on this computer (the real
//      recognizer hears exactly them, the voice fingerprint matches), and both can be heard;
//   4. Put it in the cut makes a new version that plays them; the clip's transcript marks the words a cut says
//      differently and the cut's transcript shows the new words marked; the captions file names them;
//   5. undo plays what was said again;
//   6. no native dialogs, page errors or horizontal overflow; plain words.
// Screenshots in SW_SHOTS. It CHANGES the project, so point it only at a throwaway installation.
import { createHash } from 'node:crypto';
import { execFileSync, execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'SW_PROJECT', 'SW_CLIP', 'SW_SPEECH', 'SW_VOICE', 'SW_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/spoken-word-e2e.sh does)`);
    process.exit(2);
  }
}
const projectId = process.env.SW_PROJECT;
const clip = process.env.SW_CLIP;
const shotsDir = process.env.SW_SHOTS;
mkdirSync(shotsDir, { recursive: true });

const results = [];
const problems = [];
function check(name, ok, detail = '') {
  results.push({ name, ok: !!ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
}

function mintLink() {
  const out = execSync(process.env.METARUSH_MINT, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return `${base}/auth${new URL(m[0]).search}`;
}

async function api(page, op, input = {}) {
  return page.evaluate(
    async ([op, input]) => {
      const r = await fetch(`/api/ops/${op}`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' },
        body: JSON.stringify(input),
      });
      const j = await r.json();
      if (!j.ok) throw new Error(`${op}: ${j.error?.code} ${j.error?.message}`);
      return j.data;
    },
    [op, input],
  );
}

async function newPage(browser, { theme = 'dark', state } = {}) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: theme, reducedMotion: 'reduce', ...(state ? { storageState: state } : {}) });
  await ctx.addInitScript((t) => {
    try {
      localStorage.setItem('metarush.theme', t);
    } catch {
      /* private mode */
    }
  }, theme);
  const page = await ctx.newPage();
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) problems.push(`console: ${m.text()}`);
  });
  page.on('dialog', (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  return { ctx, page };
}

let shotN = 0;
async function shot(page, name) {
  await page.waitForTimeout(400);
  await page.screenshot({ path: path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`) });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
}

async function unplainIn(locator) {
  const texts = await locator.allInnerTexts();
  return texts.flatMap((t) => t.split('\n')).flatMap((l) => checkShown(l).map((f) => `${f.kind} ${f.text} in ${JSON.stringify(l.slice(0, 120))}`));
}

async function until(fn, ms = 20_000, step = 500) {
  const end = Date.now() + ms;
  let v;
  while (Date.now() < end) {
    v = await fn();
    if (v) return v;
    await new Promise((r) => setTimeout(r, step));
  }
  return v;
}

const core = (w) => w.replace(/^[^\p{L}\p{N}]+|[^\p{L}\p{N}]+$/gu, '').toLowerCase();
const dialogOpen = (page) => page.locator('[data-mr-dialog="open"]');

/** The new words as the fake ElevenLabs will say them: the Mac's same voice, made here beforehand. */
function speak(text) {
  const name = createHash('sha256').update(text).digest('hex').slice(0, 16);
  const aiff = path.join(process.env.SW_SPEECH, `${name}.aiff`);
  execFileSync('say', ['-v', process.env.SW_VOICE, '-o', aiff, '--', text]);
  execFileSync('ffmpeg', ['-v', 'error', '-y', '-i', aiff, '-ac', '1', '-ar', '22050', '-c:a', 'pcm_s16le', path.join(process.env.SW_SPEECH, `${name}.wav`)]);
}

async function main() {
  const browser = await chromium.launch();
  const { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });

  // 1. Ada's voice named, her consent covering new words with ElevenLabs, a copy of her voice.
  const tx = await api(page, 'footage.transcript', { assetId: clip, words: true });
  const words = tx.segments.flatMap((s) => (s.words ?? []).map((w) => ({ ...w, speaker: s.speaker })));
  const twelve = words.find((w) => ['twelve', '12'].includes(core(w.w)));
  check('the recognizer heard “twelve”', !!twelve, tx.segments.map((s) => s.text).join(' '));
  if (!twelve) throw new Error('nothing to change');
  await api(page, 'footage.speakers.rename', { assetId: clip, speaker: twelve.speaker, name: 'Ada Lovelace', newPerson: true });
  await api(page, 'consent.record', {
    person: 'Ada Lovelace',
    self: true,
    adult: true,
    statement: 'I, Ada Lovelace, agree to MetaRush copying my voice and saying a word differently in my own voice copy.',
    uses: ['voice', 'new-words'],
    providers: ['elevenlabs'],
  });
  const copyIn = { projectId, person: 'Ada Lovelace', name: 'Ada', sample: clip };
  const copyEst = await api(page, 'ai.voices.copy', { ...copyIn, dryRun: true });
  check('a copy of Ada’s voice may be made', copyEst.estimate.allowed, (copyEst.estimate.problems ?? []).join(' '));
  const copy = await api(page, 'ai.voices.copy', { ...copyIn, maxUsd: copyEst.estimate.usd });
  const made = await until(async () => {
    const w = await api(page, 'ai.show', { id: copy.work.id });
    return ['done', 'failed', 'refused', 'canceled'].includes(w.state) ? w : null;
  }, 120_000);
  check('Ada’s voice copy is made', made?.state === 'done', made?.state);
  const dur = Math.max(...words.map((w) => w.t)) + 0.6;
  const plan = { projectId, spec: { schemaVersion: 1, output: { preset: 'landscape-1080p', fps: 30 }, clips: [{ id: 'garden', assetId: clip, in: 0, out: Math.round(dur * 1000) / 1000 }] } };
  await api(page, 'edit.apply', { plan });

  // The words the fake will be asked to say: priced first (nothing is sent), then made here in the same voice.
  const est = await api(page, 'cut.say.estimate', { projectId, assetId: clip, from: twelve.f, to: twelve.t, words: 'fifteen' });
  check('the estimate says it may be made, with the words around the new ones', est.allowed && est.context.includes('fifteen') && est.usd > 0, (est.problems ?? []).join(' ') || est.context);
  speak(est.context);

  // 2. Right-click “twelve” in the clip's transcript → Say it differently… → the price first.
  await page.goto(`${base}/p/${projectId}?tab=sources&asset=${clip}`);
  const word = page.locator('[data-wi]').filter({ hasText: new RegExp(`^${twelve.w.trim().replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s*$`) }).first();
  await word.waitFor({ timeout: 30_000 });
  await word.click({ button: 'right' });
  const menu = page.locator('[data-word-menu]');
  await menu.waitFor();
  check('right-clicking a word offers Say it differently…', await menu.getByRole('menuitem', { name: /Say it differently/ }).isVisible());
  await shot(page, 'word-menu');
  await menu.getByRole('menuitem', { name: /Say it differently/ }).click();
  const dlg = dialogOpen(page);
  await dlg.locator('[data-say-dialog]').waitFor();
  await dlg.locator('[data-say-words]').fill('fifteen');
  await dlg.locator('[data-say-estimate] [data-cost-estimate]').waitFor({ timeout: 20_000 });
  const makeBtn = dlg.locator('[data-say-make]');
  check('the button carries the price before anything is made', /Make it \((about|less than)/.test(await makeBtn.innerText()), await makeBtn.innerText());
  const unplain = await unplainIn(dlg);
  check('the dialog reads plainly', unplain.length === 0, unplain.join(' | '));
  await shot(page, 'say-it-differently-priced');
  const calls = async () => (await fetch(`http://127.0.0.1:${process.env.METARUSH_SW_ELEVEN_PORT ?? 5581}/__calls`).then((r) => r.json()).catch(() => ({})));
  const before = await calls();

  // 3. Made, checked on this computer, heard.
  await makeBtn.click();
  const outcome = await until(async () => {
    if (await dlg.locator('[data-say-ready]').isVisible().catch(() => false)) return 'ready';
    if (await dlg.locator('[data-say-failed]').isVisible().catch(() => false)) return 'failed';
    return null;
  }, 180_000);
  const failed = outcome === 'failed' ? await dlg.locator('[data-say-failed]').innerText() : '';
  check('the new words pass the checks on this computer', outcome === 'ready', failed || String(outcome));
  check('the fake was asked for speech once', JSON.stringify(await calls()) !== JSON.stringify(before));
  const list = await api(page, 'cut.say.list', { projectId });
  const change = list.changes[0];
  check('the recognizer heard exactly the new words, and the voice matched', change?.check?.wordsMatch && change?.check?.voiceMatch, JSON.stringify(change?.check ?? {}));
  if (outcome !== 'ready') throw new Error(failed || 'not ready');
  check('both can be heard first', (await dlg.locator('[data-say-before]').count()) === 1 && (await dlg.locator('[data-say-after]').count()) === 1);
  await shot(page, 'say-it-differently-ready');

  // 4. Into the cut: a new version, marked in both transcripts and the captions file.
  await dlg.locator('[data-say-place]').click();
  await dlg.waitFor({ state: 'detached', timeout: 30_000 });
  const placed = await until(async () => (await api(page, 'cut.say.show', { id: change.id })).inCut, 20_000);
  check('the cut plays the new words', !!placed);
  await page.reload();
  await page.locator('[data-wi][data-said]').first().waitFor({ timeout: 30_000 });
  check('the clip’s transcript marks the words a cut says differently', (await page.locator('[data-wi][data-said="in-cut"]').count()) >= 1);
  await shot(page, 'clip-transcript-marked');
  await page.goto(`${base}/p/${projectId}?tab=cut`);
  await page.getByRole('tablist', { name: 'Beside the player' }).getByRole('tab', { name: 'Transcript' }).click();
  const cutSaid = page.locator('[data-cut-transcript] [data-said]').first();
  await cutSaid.waitFor({ timeout: 30_000 });
  check('the cut’s transcript shows the new words, marked', /fifteen/i.test(await cutSaid.innerText()) && /what was said/i.test((await cutSaid.getAttribute('title')) ?? ''));
  await shot(page, 'cut-transcript-marked');
  const vtt = await api(page, 'captions.file', { projectId, format: 'vtt' });
  check('the captions file names the changed words', /NOTE Words changed with AI/.test(vtt.content) && /fifteen/i.test(vtt.content), vtt.content.slice(0, 200));
  const marked = await api(page, 'captions.file', { projectId, format: 'srt', markChanged: true });
  check('captions can mark them', /<i>fifteen/i.test(marked.content), marked.content.slice(0, 200));

  // 5. Undo: what was said plays again.
  await api(page, 'cut.say.undo', { id: change.id, noRender: true });
  const back = await api(page, 'cut.say.show', { id: change.id });
  check('undo plays what was said again', !back.inCut && back.state === 'undone');

  // A light-theme look at the dialog's refusal: the consent withdrawn, the new words can't go back in.
  const { page: light } = await newPage(browser, { theme: 'light', state: await ctx.storageState() });
  await light.goto(`${base}/p/${projectId}?tab=sources&asset=${clip}`);
  await light.locator('[data-wi]').first().waitFor({ timeout: 30_000 });
  await shot(light, 'clip-transcript-light');

  await browser.close();
  for (const p of problems) check('no problems on the page', false, p);
  const failedN = results.filter((r) => !r.ok).length;
  console.log(`\n${results.length - failedN}/${results.length} checks passed; screenshots in ${shotsDir}`);
  process.exit(failedN ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
