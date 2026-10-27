#!/usr/bin/env node
// Drives the studio's Sound tab end to end in a real browser (run it through
// scripts/sound-e2e.sh, which builds a throwaway installation for it):
//   a person's level over their own speech (heard in the preview plan, saved) ·
//   a clearer voice made on this computer (scores in plain words, the copy in
//   the plan) · the A/B: every voice as recorded in the preview, and a person's
//   clearer voice off and on · strength · studio quality (when it is set up
//   here) · the cloud priced first against the fake ElevenLabs API, refused over
//   the project's budget · music dropped in (fitted to the video, ending on a
//   beat), its level and getting quieter while someone talks (heard, saved),
//   timed by hand · the timeline's music lane (dragged; a file dropped on it) ·
//   no "LUFS", "ducking", "de-ess" or "DNSMOS" anywhere on the page.
// Screenshots (desktop and phone, dark and light) go to SOUND_SHOTS.
//
// It CHANGES the project, so point it only at a throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'METARUSH_CLI', 'SOUND_PROJECT', 'SOUND_KEY_FILE', 'SOUND_FAKE_URL', 'SOUND_MUSIC', 'SOUND_MUSIC2', 'SOUND_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/sound-e2e.sh does)`);
    process.exit(2);
  }
}
const shotsDir = process.env.SOUND_SHOTS;
mkdirSync(shotsDir, { recursive: true });
const ONLY = process.env.SOUND_ONLY ? new Set(process.env.SOUND_ONLY.split(',')) : null;
const want = (k) => !ONLY || ONLY.has(k);

const results = [];
const problems = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
}

function mintLink() {
  const out = execSync(process.env.METARUSH_MINT, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return `${base}/auth${new URL(m[0]).search}`;
}

function cli(args, input) {
  return execSync(`${process.env.METARUSH_CLI} ${args}`, {
    cwd: repo,
    encoding: 'utf8',
    input,
    stdio: [input === undefined ? 'ignore' : 'pipe', 'pipe', 'pipe'],
    env: { ...process.env, METARUSH_ELEVENLABS_BASE_URL: process.env.SOUND_FAKE_URL },
  });
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

const setTheme = (t) => (ctx) =>
  ctx.addInitScript((theme) => {
    try {
      localStorage.setItem('metarush.theme', theme);
    } catch {
      /* private mode */
    }
  }, t);

async function newPage(browser, { theme = 'dark', mobile = false, state } = {}) {
  const ctx = await browser.newContext({
    viewport: mobile ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: mobile ? 2 : 1,
    colorScheme: theme,
    reducedMotion: 'reduce',
    ...(state ? { storageState: state } : {}),
  });
  await setTheme(theme)(ctx);
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
async function shot(page, name, { full = false } = {}) {
  await page.waitForTimeout(400);
  const file = path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file, fullPage: full });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
  return file;
}

const toast = (page, text, timeout = 60_000) => page.getByText(text).first().waitFor({ timeout });

/** Polls until pred() is true (or throws after timeout with what). */
async function until(pred, what, timeout = 120_000) {
  const end = Date.now() + timeout;
  for (;;) {
    if (await pred()) return;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 500));
  }
}

/** Waits for a voice's work to finish: its toast, or the problem the row shows. */
async function made(page, row, title, timeout = 300_000) {
  const ok = page.getByText(title).first();
  const bad = row.getByRole('alert').first();
  await Promise.race([ok.waitFor({ timeout }), bad.waitFor({ timeout })]);
  if (await bad.isVisible().catch(() => false)) throw new Error(`${title}: ${await bad.textContent()}`);
}

/** The linear gain of an envelope at element frame f (AudioLayer's envelopeGain). */
function gainAt(env, f) {
  if (!env?.length) return 1;
  if (f <= env[0].f) return env[0].g;
  for (let i = 1; i < env.length; i++) {
    if (f <= env[i].f) {
      const a = env[i - 1];
      const b = env[i];
      return b.f === a.f ? b.g : a.g + ((b.g - a.g) * (f - a.f)) / (b.f - a.f);
    }
  }
  return env[env.length - 1].g;
}
const db = (r) => 20 * Math.log10(r);

/** Words the Sound tab must never show (the plain-words rule). */
const JARGON = /\b(LUFS|ducking|de-?ess(er|ing)?|DNSMOS)\b/i;
async function jargon(page) {
  const text = await page.evaluate(() => document.body.innerText);
  const m = text.match(JARGON);
  return m ? m[0] : null;
}

async function main() {
  const browser = await chromium.launch({ args: ['--autoplay-policy=no-user-gesture-required'] });
  const { ctx, page } = await newPage(browser);
  current = page;
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
  const state = await ctx.storageState();
  const projects = (await api(page, 'project.list')).projects;
  const p = projects.find((x) => x.name === process.env.SOUND_PROJECT);
  if (!p) throw new Error(`no project ${process.env.SOUND_PROJECT}`);
  const head = async () => (await api(page, 'project.show', { id: p.id })).headRevisionId;
  const spec = async () => (await api(page, 'revision.show', { id: await head() })).spec;
  const plan = async () => api(page, 'revision.plan', { id: await head() });
  const studio = `${base}/p/${p.id}`;
  const previews = [];
  page.on('response', async (r) => {
    if (r.url().endsWith('/api/ops/sound.preview')) previews.push(await r.json().catch(() => null));
  });
  const lastPreview = async () => {
    for (let i = 0; i < 40 && !previews.at(-1)?.data; i++) await page.waitForTimeout(200);
    return previews.at(-1)?.data;
  };

  // 1. The Sound tab: the parts, each person under the camera sound, clearer voices, music.
  await page.goto(`${studio}?tab=sound`);
  await page.getByRole('heading', { name: 'How loud each part is' }).waitFor({ timeout: 20_000 });
  const people = page.getByRole('list', { name: 'Each person, over their own speech' });
  await people.waitFor();
  check('each person has a level of their own', (await people.getByRole('slider', { name: 'Samantha' }).count()) === 1 && (await people.getByRole('slider', { name: 'Daniel' }).count()) === 1);
  await page.getByRole('heading', { name: 'Clearer voices' }).waitFor();
  await page.getByRole('heading', { name: 'Music', exact: true }).waitFor();
  await shot(page, 'sound-tab');
  check('no jargon on the Sound tab', !(await jargon(page)), (await jargon(page)) ?? '');

  // 2. Samantha 6 dB louder over her own speech: heard in the preview, then saved.
  const before = await plan();
  const sam = people.getByRole('slider', { name: 'Samantha' });
  await sam.focus();
  for (let i = 0; i < 12; i++) await page.keyboard.press('ArrowRight'); // 0.5 dB steps
  await page.getByText('You are hearing your changes in the preview').waitFor();
  const pv = await lastPreview();
  const transcript = (await api(page, 'footage.transcript', { assetId: before.clips[0].assetId })).segments;
  const turn = (who) => transcript.find((s) => s.speakerName === who || s.speaker === who);
  const at = (pl, sec) => {
    const a = pl.clips[0].audio ?? { from: pl.clips[0].from, gain: pl.clips[0].gain };
    return gainAt(a.gain, Math.round(sec * pl.fps) - a.from);
  };
  const sT = turn('Samantha');
  const dT = turn('Daniel');
  const mid = (s) => (s.from + s.to) / 2;
  const heard = pv ? db(at(pv.plan, mid(sT)) / at(pv.plan, mid(dT))) - db(at(before, mid(sT)) / at(before, mid(dT))) : NaN;
  check('Samantha +6 dB is heard over her speech only (preview plan)', Math.abs(heard - 6) < 0.6, `${heard.toFixed(2)} dB between her turn and Daniel's`);
  await page.getByRole('button', { name: 'Save sound' }).click();
  await toast(page, 'Sound saved');
  const s1 = await spec();
  check('saved through sound.levels', s1.mix?.voices?.find((v) => v.name === 'Samantha')?.gainDb === 6, JSON.stringify(s1.mix?.voices));

  // 3. A clearer voice for Samantha on this computer: scores in plain words, the copy in the plan.
  const samRow = page.locator('[data-voice="Samantha"]');
  await samRow.getByRole('button', { name: 'Make it clearer' }).click();
  await made(page, samRow, 'Samantha sounds clearer');
  await samRow.getByText(/Voice clarity score \d\.\d → \d\.\d out of 5/).waitFor({ timeout: 20_000 });
  const scoreText = await samRow.getByText(/Voice clarity score/).first().textContent();
  check('her score is said plainly', /clearer/.test(scoreText ?? ''), scoreText ?? '');
  const pl1 = await plan();
  const copy = pl1.clips[0].audio?.variant?.assetId;
  check('the clip plays her clearer copy', !!copy && !!pl1.sources[copy], JSON.stringify(pl1.clips[0].audio?.variant));
  await samRow.getByText('Details').click();
  await samRow.getByText('The background').waitFor();
  await shot(page, 'clearer-samantha');
  check('no jargon with the scores open', !(await jargon(page)), (await jargon(page)) ?? '');

  // 4. A/B in the preview: every voice as recorded (nothing saved), then back.
  const headBeforeAB = await head();
  previews.length = 0;
  await page.getByRole('switch', { name: 'Hear every voice as recorded' }).click();
  const ab = await lastPreview();
  check('A/B: the preview plays the recording', !!ab && !ab.plan.clips[0].audio?.variant, JSON.stringify(ab?.plan.clips[0].audio?.variant ?? null));
  await page.getByRole('switch', { name: 'Hear every voice as recorded' }).click();
  check('A/B saved nothing', (await head()) === headBeforeAB && !!(await plan()).clips[0].audio?.variant);

  // 5. Her clearer voice off (the original plays; the copy is kept) and on again: no new work.
  await samRow.getByRole('switch', { name: 'Clearer voice on' }).click();
  await toast(page, 'Samantha plays as recorded');
  check('off: the original plays', !(await plan()).clips[0].audio?.variant);
  await samRow.getByRole('switch', { name: 'Clearer voice on' }).click();
  await toast(page, 'Samantha plays the clearer voice');
  check('on again: the same copy', (await plan()).clips[0].audio?.variant?.assetId === copy);

  // 6. Strength 60%: a new copy with that blend.
  const strength = samRow.getByRole('slider', { name: 'How much of the clearer voice you hear' });
  await strength.focus();
  for (let i = 0; i < 8; i++) await page.keyboard.press('ArrowLeft'); // 5% steps
  await samRow.getByRole('button', { name: 'Make it again with these settings' }).click();
  await until(async () => (await spec()).mix?.voices?.find((v) => v.name === 'Samantha')?.enhance?.strength === 0.6, 'the 60% copy');
  const s2 = await spec();
  const v2 = s2.mix?.voices?.find((v) => v.name === 'Samantha');
  check('strength 60% saved and a new copy made', v2?.enhance?.strength === 0.6 && (await plan()).clips[0].audio?.variant?.assetId !== copy, JSON.stringify(v2));

  // 7. Studio quality for Daniel, when it is set up on this computer.
  const danRow = page.locator('[data-voice="Daniel"]');
  await danRow.getByRole('radio', { name: 'Studio' }).click();
  const studioReady = (await danRow.getByRole('button', { name: 'Set it up' }).count()) === 0;
  if (studioReady && want('studio')) {
    await danRow.getByRole('button', { name: 'Make it clearer' }).click();
    await made(page, danRow, 'Daniel sounds clearer');
    await danRow.getByText(/Voice clarity score \d\.\d → \d\.\d out of 5/).waitFor({ timeout: 20_000 });
    const t = await danRow.getByText(/Voice clarity score/).first().textContent();
    check('studio for Daniel: his score in plain words', /clearer/.test(t ?? ''), t ?? '');
    await shot(page, 'studio-daniel');
  } else {
    check('studio not set up here: a one-time setup is offered', !studioReady || !want('studio'));
    await shot(page, 'studio-setup');
  }

  // 8. The cloud: no key → Settings; with the fake key, the price first; over the budget, refused.
  await danRow.getByRole('radio', { name: 'Cloud' }).click();
  await danRow.getByRole('link', { name: /Open Settings → Integrations/ }).waitFor();
  check('cloud without a key: a plain link to Settings', true);
  cli('providers set elevenlabs --key-stdin --allow-unsafe-secret-path', readFileSync(process.env.SOUND_KEY_FILE, 'utf8'));
  await page.reload();
  await page.getByRole('heading', { name: 'Clearer voices' }).waitFor({ timeout: 20_000 });
  await danRow.getByRole('radio', { name: 'Cloud' }).click();
  const isoBefore = await fakeIsolations();
  await danRow.getByRole('button', { name: /^Make it (clearer|again with these settings)$/ }).click();
  const price = page.getByRole('dialog', { name: /Make Daniel’s voice clearer with ElevenLabs/ });
  await price.waitFor();
  const priceText = await price.textContent();
  check('cloud: the price comes first, in credits', /ElevenLabs credits/.test(priceText ?? '') && (await fakeIsolations()) === isoBefore, priceText?.slice(0, 160) ?? '');
  await shot(page, 'cloud-price');
  await price.getByRole('button', { name: 'Cancel' }).click();
  check('cloud: cancelling sends nothing', (await fakeIsolations()) === isoBefore);
  // Only a person sets a budget (D241): through the dashboard, as the person signed in.
  await api(page, 'provider.settings', { name: 'elevenlabs', projectId: p.id, budgetCredits: 10 });
  await danRow.getByRole('button', { name: /^Make it (clearer|again with these settings)$/ }).click();
  const refused = page.getByRole('alertdialog', { name: 'This can’t be made in the cloud right now' });
  await refused.waitFor();
  const refusedText = await refused.textContent();
  check('cloud over the budget: refused in plain words, nothing sent', /budget/.test(refusedText ?? '') && (await fakeIsolations()) === isoBefore, refusedText?.slice(0, 160) ?? '');
  await refused.getByRole('button', { name: /OK|Close/ }).first().click();
  await api(page, 'provider.settings', { name: 'elevenlabs', projectId: p.id, budgetCredits: 0 });
  await danRow.getByRole('button', { name: /^Make it (clearer|again with these settings)$/ }).click();
  await price.waitFor();
  await price.getByRole('button', { name: /Spend about/ }).click();
  let seen = '';
  await until(async () => {
    const n = await fakeIsolations();
    const v = (await spec()).mix?.voices?.find((x) => x.name === 'Daniel');
    seen = `isolations ${n} (before ${isoBefore}), Daniel ${JSON.stringify(v)}`;
    return n > isoBefore && v?.enhance?.mode === 'cloud';
  }, 'the cloud voice').catch((e) => {
    throw new Error(`${e.message}: ${seen}`);
  });
  check('cloud: made once the person said yes (one request per stretch of his speech)', (await fakeIsolations()) >= isoBefore + 1, `${(await fakeIsolations()) - isoBefore} requests`);

  // 9. Music: a file dropped in fits the video, ending on a beat; the rights question.
  await page.getByLabel('Choose a music file').setInputFiles(process.env.SOUND_MUSIC);
  await toast(page, 'Music added', 120_000);
  const track = page.locator('[data-music-track]').first();
  await track.waitFor();
  const placement = await track.textContent();
  check('the music fits the video and ends on a beat', /on a beat/.test(placement ?? ''), placement?.slice(0, 160) ?? '');
  check('where it comes from is asked', /Nobody has said where this music comes from/.test(placement ?? ''));
  const pm = await plan();
  const bed = pm.audio.find((a) => a.role === 'music');
  check('in the plan: from the start, ending on a beat near the end', !!bed && bed.from === 0 && bed.endsOnBeat === true && pm.durationInFrames - (bed.from + bed.durationInFrames) < 45, JSON.stringify(bed && { from: bed.from, dur: bed.durationInFrames, endsOnBeat: bed.endsOnBeat, total: pm.durationInFrames }));
  await shot(page, 'music');

  // 10. Its level and getting quieter while someone talks: heard, then saved.
  previews.length = 0;
  const lvl = track.getByRole('slider', { name: /Level of/ });
  await lvl.focus();
  for (let i = 0; i < 8; i++) await page.keyboard.press('ArrowLeft'); // −4 dB
  const pvm = await lastPreview();
  const bedPv = pvm?.plan.audio.find((a) => a.role === 'music');
  const t0 = Math.round(mid(dT) * pm.fps); // while Daniel talks
  const lvlDb = bedPv ? db(gainAt(bedPv.gain, t0) / gainAt(bed.gain, t0)) : NaN;
  check('music level −4 dB heard in the preview', Math.abs(lvlDb + 4) < 0.6, `${lvlDb.toFixed(2)} dB`);
  // How far the music dips between its fades (the loudest against the quietest moment).
  const dip = (a) => {
    const from = (a.fadeInFrames ?? 0) + 1;
    const to = a.durationInFrames - (a.fadeOutFrames ?? 0) - 1;
    let lo = Infinity;
    let hi = 0;
    for (let f = from; f <= to; f++) {
      const g = gainAt(a.gain, f);
      lo = Math.min(lo, g);
      hi = Math.max(hi, g);
    }
    return db(lo / hi);
  };
  previews.length = 0;
  await track.getByRole('switch', { name: 'Gets quieter when someone talks' }).click();
  const pvd = await lastPreview();
  const bedOff = pvd?.plan.audio.find((a) => a.role === 'music');
  check('it got quieter while people talked, and in the preview now it does not', !!bedOff && dip(bed) < -6 && Math.abs(dip(bedOff)) < 1, `dip ${dip(bed).toFixed(1)} dB before, ${bedOff ? dip(bedOff).toFixed(1) : '?'} dB now`);
  await page.getByRole('button', { name: 'Save sound' }).click();
  await toast(page, 'Sound saved');
  const s3 = await spec();
  const m3 = s3.audio.find((a) => a.role === 'music');
  check('music level and switch saved', m3?.gainDb === -16 && m3?.duck === false, JSON.stringify(m3 && { gainDb: m3.gainDb, duck: m3.duck }));
  const savedBed = (await plan()).audio.find((a) => a.role === 'music');
  check('saved: no dip while people talk, and it plays like its preview', !!savedBed && !!bedOff && Math.abs(dip(savedBed)) < 0.5 && Math.abs(db(gainAt(savedBed.gain, t0) / gainAt(bedOff.gain, t0))) < 0.1, savedBed && bedOff ? `dip ${dip(savedBed).toFixed(1)} dB, ${db(gainAt(savedBed.gain, t0) / gainAt(bedOff.gain, t0)).toFixed(2)} dB from the preview` : '');

  // 11. Placed by hand: from 0:02 in the video.
  await track.getByRole('radio', { name: 'Placed by hand' }).click();
  await until(async () => (await spec()).audio.find((a) => a.role === 'music')?.fit !== 'auto', 'the music placed by hand');
  const startField = track.getByRole('textbox', { name: 'Starts in the video at' });
  await startField.fill('0:02.0');
  await track.getByRole('button', { name: 'Place it' }).click();
  await until(async () => (await plan()).audio.find((a) => a.role === 'music')?.from === 60, 'the music at 0:02').catch(() => undefined);
  const pm2 = await plan();
  const bed2 = pm2.audio.find((a) => a.role === 'music');
  check('by hand: it starts 2 s into the video', bed2?.from === 60, JSON.stringify(bed2 && { from: bed2.from }));

  // 12. The timeline's music lane: drag it 1 s later; drop a file on the lane.
  await page.goto(`${studio}?tab=cut`);
  const laneEl = page.locator('[data-music-lane]');
  await laneEl.waitFor({ timeout: 20_000 });
  const block = laneEl.locator('[data-music-block]').first();
  await block.waitFor();
  await laneEl.evaluate((el) => el.scrollIntoView({ block: 'center' }));
  const pps = await page.evaluate(() => {
    const r = document.querySelector('[data-music-block]')?.getBoundingClientRect();
    return r ? r.width : 0;
  });
  const box = await block.boundingBox();
  const durSec = (bed2.durationInFrames ?? 0) / pm2.fps;
  const perSec = pps / durSec;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width / 2 + perSec * 0.5, box.y + box.height / 2, { steps: 4 });
  await page.mouse.move(box.x + box.width / 2 + perSec, box.y + box.height / 2, { steps: 4 });
  await page.mouse.up();
  await toast(page, /Moved “/);
  const s4 = await spec();
  const m4 = s4.audio.find((a) => a.role === 'music');
  check('dragged on the lane: about 1 s later', !!m4 && Math.abs(m4.at - 3) < 0.25, JSON.stringify(m4 && { at: m4.at }));
  await shot(page, 'music-lane');
  const buf = readFileSync(process.env.SOUND_MUSIC2).toString('base64');
  await laneEl.evaluate(
    (el, [b64, name]) => {
      const bin = atob(b64);
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      const dt = new DataTransfer();
      dt.items.add(new File([bytes], name, { type: 'audio/mp4' }));
      el.dispatchEvent(new DragEvent('dragover', { bubbles: true, cancelable: true, dataTransfer: dt }));
      el.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: dt }));
    },
    [buf, path.basename(process.env.SOUND_MUSIC2)],
  );
  // A file the workspace already has is asked about first (here it is new).
  await Promise.race([toast(page, /as music/, 120_000), page.getByRole('dialog', { name: 'Already in MetaRush' }).waitFor({ timeout: 120_000 })]);
  if (await page.getByRole('dialog', { name: 'Already in MetaRush' }).isVisible().catch(() => false)) {
    await page.getByRole('button', { name: 'Skip' }).click();
    await toast(page, /as music/, 120_000);
  }
  const s5 = await spec();
  check('a file dropped on the lane is music under the video, fitted', s5.audio.filter((a) => a.role === 'music').length === 2 && s5.audio.some((a) => a.role === 'music' && a.fit === 'auto'));

  // 13. Desktop and phone, dark and light.
  for (const theme of ['dark', 'light']) {
    for (const mobile of [false, true]) {
      const tag = `${mobile ? 'phone' : 'desktop'}-${theme}`;
      const v = await newPage(browser, { theme, mobile, state });
      await v.page.goto(`${studio}?tab=sound`);
      await v.page.getByRole('heading', { name: 'Clearer voices' }).waitFor({ timeout: 20_000 });
      await shot(v.page, `sound-${tag}`);
      await v.page.locator('[data-sound-voices]').evaluate((el) => el.scrollIntoView({ block: 'start' }));
      await v.page.evaluate(() => window.scrollBy(0, -120));
      await shot(v.page, `voices-${tag}`);
      await v.page.locator('[data-sound-music]').evaluate((el) => el.scrollIntoView({ block: 'start' }));
      await v.page.evaluate(() => window.scrollBy(0, -120));
      await shot(v.page, `music-${tag}`);
      check(`${tag}: no jargon`, !(await jargon(v.page)), (await jargon(v.page)) ?? '');
      await v.page.goto(`${studio}?tab=cut`);
      await v.page.locator('[data-music-lane]').waitFor({ timeout: 20_000 });
      await v.page.locator('[data-music-lane]').evaluate((el) => el.scrollIntoView({ block: 'center' }));
      await shot(v.page, `lane-${tag}`);
      await v.ctx.close();
    }
  }

  // 14. The top bar fits at common laptop and desktop widths: every button on screen.
  {
    const v = await newPage(browser, { theme: 'dark', state });
    await v.page.goto(`${studio}?tab=sound`);
    await v.page.getByRole('heading', { name: 'Clearer voices' }).waitFor({ timeout: 20_000 });
    const off = [];
    for (const width of [1024, 1280, 1366, 1440, 1536, 1920]) {
      await v.page.setViewportSize({ width, height: 900 });
      await v.page.waitForTimeout(250);
      const r = await v.page.evaluate(() => {
        const w = document.documentElement.clientWidth;
        const bar = document.querySelector('header');
        const outside = bar
          ? [...bar.querySelectorAll('a, button')]
              .filter((el) => {
                const b = el.getBoundingClientRect();
                return b.width > 0 && b.right > w + 0.5;
              })
              .map((el) => el.getAttribute('aria-label') || el.textContent.trim())
          : [];
        return { over: document.documentElement.scrollWidth - w, outside };
      });
      if (r.over > 0 || r.outside.length) off.push(`${width}px: ${r.over}px too wide${r.outside.length ? `, off screen: ${r.outside.join(', ')}` : ''}`);
    }
    check('the top bar fits from 1024 to 1920 px wide', off.length === 0, off.join(' | '));
    await v.ctx.close();
  }

  check('no native dialogs, page errors or horizontal overflow', problems.length === 0, problems.join(' | '));
  await browser.close();
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed; screenshots in ${shotsDir}`);
  if (failed.length) process.exitCode = 1;
}

/** Voice isolations the fake ElevenLabs API has answered (from its log of calls). */
async function fakeIsolations() {
  const r = await fetch(`${process.env.SOUND_FAKE_URL}/__calls`).catch(() => null);
  if (!r || !r.ok) return -1;
  const j = await r.json().catch(() => ({}));
  return j.isolation ?? 0;
}

let current = null; // the page in use, for a picture when something fails

main().catch(async (e) => {
  console.error(e);
  if (current) {
    await current.screenshot({ path: path.join(shotsDir, 'failure.png'), fullPage: true }).catch(() => undefined);
    const alerts = await current.getByRole('alert').allTextContents().catch(() => []);
    if (alerts.length) console.error(`alerts on the page: ${alerts.join(' | ')}`);
    console.error(`picture of the page: ${path.join(shotsDir, 'failure.png')}`);
  }
  process.exit(1);
});
