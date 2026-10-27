#!/usr/bin/env node
// Live takes and script versions end to end (D85, D86), in a real browser with
// Chromium's fake microphone fed with known speech (run it through
// scripts/voiceover-live-e2e.sh, which builds the throwaway installation):
//
//   1. A pasted script is used as-is (the original kept exactly).
//   2. "Fit it to the video with Claude" (a fake claude): the card's version side
//      by side with the person's, a line kept as written, the version used; the
//      two versions switch; the original survives a reload and the cut sheet.
//   3. A live take from the playhead: the teleprompter cues each pinned line about
//      2 s before its moment; the take lands where it was spoken (measured
//      independently: the page taps the same microphone and logs the playhead);
//      each line's start point moves to where it was said; the take is cut only in
//      the pauses; "Snap lines to their planned spots" and back.
//   4. Re-recording one line (a second browser whose microphone says line 2 again)
//      replaces only that line; both takes stay and A/B still works.
// Screenshots (desktop and phone, dark and light) go to LIVE_SHOTS.
//
// It CHANGES the project, so point it only at a throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { useFakeMicByID } from './fake-mic.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'LIVE_PROJECT', 'LIVE_TAKE_A', 'LIVE_TAKE_B', 'LIVE_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/voiceover-live-e2e.sh does)`);
    process.exit(2);
  }
}
const shotsDir = process.env.LIVE_SHOTS;
mkdirSync(shotsDir, { recursive: true });
const TOL = 0.15; // seconds: a take lands where it was spoken within this

const results = [];
const problems = [];
const measured = [];
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

/*
 * The independent witness: the page's own tap on the microphone stream (an
 * analyser in its own audio context, read every 10 ms) and the playhead the
 * recorder shows (data-playhead), both on performance.now(). Nothing of
 * MetaRush's timing code is used to say when a line was spoken.
 */
async function witness(ctx) {
  await ctx.addInitScript(() => {
    const md = navigator.mediaDevices;
    if (!md) return;
    const real = md.getUserMedia.bind(md);
    md.getUserMedia = async (c) => {
      const s = await real(c);
      if (c && c.audio) {
        try {
          const actx = new AudioContext();
          const an = actx.createAnalyser();
          an.fftSize = 512;
          actx.createMediaStreamSource(s).connect(an);
          void actx.resume();
          const buf = new Float32Array(512);
          window.__tap = window.__tap ?? [];
          window.__ph = window.__ph ?? [];
          setInterval(() => {
            const now = performance.now();
            an.getFloatTimeDomainData(buf);
            let ss = 0;
            for (const v of buf) ss += v * v;
            window.__tap.push([now, Math.sqrt(ss / buf.length)]);
            const el = document.querySelector('[data-mini-timeline]');
            const rec = document.querySelector('[data-mr-recorder]');
            if (el && rec?.getAttribute('data-phase') === 'recording') {
              const cur = document.querySelector('[data-prompt-current]')?.getAttribute('data-prompt-current') ?? '';
              const nx = document.querySelector('[data-prompt-next]');
              window.__ph.push([now, Number(el.getAttribute('data-playhead')), cur, nx?.getAttribute('data-prompt-next') ?? '', nx?.getAttribute('data-countdown') ?? '']);
            }
            if (window.__tap.length > 200000) window.__tap.splice(0, 50000);
          }, 10);
        } catch {
          /* the witness is the test's own */
        }
      }
      return s;
    };
  });
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
    permissions: ['microphone'],
    ...(state ? { storageState: state } : {}),
  });
  await setTheme(theme)(ctx);
  await useFakeMicByID(ctx);
  await witness(ctx);
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
  const file = path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
}

const fakeMic = (wav) => ['--autoplay-policy=no-user-gesture-required', '--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', `--use-file-for-fake-audio-capture=${wav}`];

/** Where each stretch of sound starts after half a second of quiet: [perf ms or seconds]. */
function onsets(series, thr, gap) {
  const out = [];
  let quietSince = -Infinity;
  let loud = false;
  for (const [t, v] of series) {
    if (v > thr) {
      if (!loud && t - quietSince >= gap) out.push(t);
      loud = true;
    } else {
      if (loud) quietSince = t;
      loud = false;
    }
  }
  return out;
}

/** Where sound started on the microphone, by the witness, as timeline seconds (the playhead the recorder showed then). */
async function spokenOnTimeline(page) {
  const ph = await page.evaluate(() => window.__ph);
  const tap = await page.evaluate(() => window.__tap);
  const offsets = ph.filter((x, i) => i > 0 && x[1] !== ph[i - 1][1]).map((x) => x[1] - x[0] / 1000);
  offsets.sort((a, b) => a - b);
  const toTimeline = (perf) => perf / 1000 + offsets[Math.floor(offsets.length / 2)];
  return { ph, spoken: onsets(tap, 0.02, 500).filter((t) => t >= ph[0][0] && t <= ph.at(-1)[0]).map(toTimeline) };
}

/** Where the plan plays a moment of a take (seconds into the file), or NaN. */
function playedAt(plan, assetId, x) {
  const e = plan.audio.find((a) => a.role === 'voiceover' && a.assetId === assetId && x >= a.trimBefore / plan.fps - 0.05 && x < (a.trimBefore + a.durationInFrames) / plan.fps);
  return e ? e.from / plan.fps + (x - e.trimBefore / plan.fps) : NaN;
}

/** The decoded take: its sound every 10 ms (seconds into the file) and its length. */
async function takeEnvelope(page, assetId) {
  return page.evaluate(async (id) => {
    const r = await fetch(`/media/assets/${id}/original`, { credentials: 'same-origin' });
    const ac = new OfflineAudioContext(1, 48000, 48000);
    const buf = await ac.decodeAudioData(await r.arrayBuffer());
    const d = buf.getChannelData(0);
    const win = Math.round(buf.sampleRate * 0.01);
    const env = [];
    for (let i = 0; i + win <= d.length; i += win) {
      let s = 0;
      for (let j = i; j < i + win; j++) s += d[j] * d[j];
      env.push([i / buf.sampleRate, Math.sqrt(s / win)]);
    }
    return { env, duration: buf.duration };
  }, assetId);
}

async function main() {
  const browser = await chromium.launch({ args: fakeMic(process.env.LIVE_TAKE_A) });
  const { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
  const projects = (await api(page, 'project.list')).projects;
  const p = projects.find((x) => x.name === process.env.LIVE_PROJECT);
  if (!p) throw new Error(`no project ${process.env.LIVE_PROJECT}`);
  const head = async () => (await api(page, 'project.show', { id: p.id })).headRevisionId;
  const narration = async () => api(page, 'voiceover.show', { revisionId: await head() });
  const studio = `${base}/p/${p.id}`;
  const state = await ctx.storageState();

  // 1. A pasted script, used as-is.
  const SCRIPT = 'Every morning starts in the kitchen.\n\nThen the garden wakes up.\n\n  And by noon, everything is green.\n';
  await page.goto(`${studio}?tab=voiceover&mode=write`);
  const box = page.getByRole('textbox', { name: 'Script' });
  await box.waitFor();
  await box.fill(SCRIPT);
  await page.locator('[data-script-choice]').waitFor();
  await shot(page, 'script-choice');
  check('as-is is the default choice', (await page.getByRole('radio', { name: /Use my script as-is/ }).getAttribute('aria-checked')) === 'true');
  await page.getByRole('button', { name: 'Use my script' }).click();
  await page.getByRole('list', { name: 'Narration lines' }).waitFor({ timeout: 20_000 });
  let n = await narration();
  check(
    'as-is: three lines, no timing guessed, the original kept exactly',
    n.beats.length === 3 && n.beats.every((b) => b.anchor.kind === 'after') && n.script.original === SCRIPT && n.script.active === 'original',
    JSON.stringify(n.beats.map((b) => b.placement)),
  );

  // 2. Fit it to the video with Claude: paste again, choose fit, review the card's diff.
  await page.getByRole('button', { name: 'Paste a script' }).click();
  const dlg = page.getByRole('dialog', { name: 'Paste a script' });
  await dlg.getByRole('textbox', { name: 'Script' }).fill(SCRIPT);
  await dlg.getByRole('radio', { name: /Fit it to the video with Claude/ }).click();
  await dlg.getByRole('button', { name: /fit it with Claude/i }).click();
  const review = page.locator('[data-fit-review="proposal"]');
  await review.waitFor({ timeout: 60_000 });
  // The assistant panel opened on the conversation; close it to look at the tab.
  await page.keyboard.press('Escape').catch(() => undefined);
  await review.scrollIntoViewIfNeeded();
  await shot(page, 'fit-review-desktop-dark');
  const line2 = review.locator('[data-fit-line="line-2"]');
  const delWords = await line2.locator('[data-side="mine"] mark[data-word="del"]').allInnerTexts();
  check('the diff highlights what changed, with a reason per change', delWords.join(' ') === 'Then' && /Trimmed so it lands/.test(await line2.innerText()), `removed: ${delWords.join(' ')}`);
  check('timing-only changes are shown as such', (await review.locator('[data-fit-line="line-1"]').getAttribute('data-change')) === 'timing');
  // Keep line 3 as written; use Claude's version for the rest.
  await review.locator('[data-fit-line="line-3"]').getByRole('radio', { name: 'Mine' }).click();
  await review.locator('[data-fit-use]').click();
  await review.waitFor({ state: 'detached', timeout: 30_000 });
  n = await narration();
  const fittedOK = n.script.active === 'fitted' && n.beats[1].text === 'The garden wakes up.' && n.beats[0].anchor.clipId === 'kitchen' && n.beats[2].anchor.kind === 'after' && n.script.original === SCRIPT;
  check('per-line choice: Claude\'s lines 1–2 in use, line 3 kept as written, the original untouched', fittedOK, JSON.stringify(n.beats.map((b) => [b.text, b.placement])));

  // Both versions switch at any time, and the original survives a reload and the cut sheet.
  const sw = page.locator('[data-version-switch]');
  await sw.getByRole('radio', { name: 'Yours, as written' }).click();
  await page.locator('[data-version-switch="original"]').waitFor({ timeout: 20_000 });
  n = await narration();
  check('switch to the original', n.script.active === 'original' && n.beats[1].text === 'Then the garden wakes up.');
  await sw.getByRole('radio', { name: 'Fitted by Claude' }).click();
  await page.locator('[data-version-switch="fitted"]').waitFor({ timeout: 20_000 });
  await page.reload();
  await page.locator('[data-version-switch="fitted"]').waitFor({ timeout: 20_000 });
  n = await narration();
  const sheet = (await api(page, 'cut.show', { projectId: p.id })).sheet;
  const checked = await api(page, 'cut.check', { sheet });
  check(
    'after a reload: the fitted version in use, the original kept; the cut sheet holds it exactly and reads back unchanged',
    n.script.active === 'fitted' && n.script.original === SCRIPT && sheet.includes('### Original script') && sheet.includes(SCRIPT) && checked.valid && checked.noChanges,
    `sheet round trip: valid ${checked.valid}, no changes ${checked.noChanges}`,
  );
  await page.locator('[data-version-switch]').getByRole('button', { name: 'Compare' }).click();
  await page.locator('[data-fit-review="cut"]').waitFor();
  await shot(page, 'compare-versions-desktop-dark');
  await page.getByRole('button', { name: 'Close the comparison' }).click();

  // 3. A live take from the playhead. The pins before it: Claude's (lines 1–2) and line 3 after line 2.
  const before = await narration();
  await page.getByRole('button', { name: 'Record narration' }).click();
  const rec = page.getByRole('dialog', { name: 'Record narration' });
  await rec.getByRole('meter', { name: 'Microphone level' }).waitFor({ timeout: 20_000 });
  check('the recorder opens in live mode', (await rec.getByRole('radio', { name: /^Live/ }).getAttribute('aria-checked')) === 'true');
  await rec.locator('[data-mini-timeline]').waitFor();
  await shot(page, 'live-setup-desktop-dark');
  await page.evaluate(() => {
    window.__tap = [];
    window.__ph = [];
  });
  await rec.getByRole('button', { name: /^Record from/ }).click();
  await rec.locator('[data-mr-recorder][data-phase="recording"]').or(page.locator('[data-mr-recorder][data-phase="recording"]')).waitFor({ timeout: 15_000 });
  // Line 2 is pinned to the garden shot (Claude's fit): wait for its countdown, then let all three lines be read.
  await page.waitForFunction(() => document.querySelector('[data-prompt-next="line-2"]')?.getAttribute('data-countdown'), null, { timeout: 30_000 }).catch(() => undefined);
  await shot(page, 'live-recording-countdown');
  await page.waitForFunction(() => (window.__ph ?? []).at(-1)?.[1] >= 15.5, null, { timeout: 40_000 });
  await rec.getByRole('button', { name: /^Stop/ }).click();
  await page.locator('[data-take-review="new"]').waitFor({ timeout: 120_000 });
  await shot(page, 'live-review-desktop-dark');

  // The teleprompter cued line 2 about two seconds before its pin and switched to it on time.
  const pin2 = before.beats[1].start;
  const ph = await page.evaluate(() => window.__ph);
  const cueAt = ph.find((x) => x[3] === 'line-2' && x[4] !== '')?.[1];
  const onAt = ph.find((x) => x[2] === 'line-2')?.[1];
  check(
    'teleprompter: line 2 counts down ~2 s before its pin and is the line to say on it',
    cueAt !== undefined && onAt !== undefined && Math.abs(pin2 - 2 - cueAt) <= 0.2 && Math.abs(onAt - pin2) <= 0.15,
    `pin ${pin2.toFixed(2)} s, countdown from ${cueAt?.toFixed(2)} s, current from ${onAt?.toFixed(2)} s`,
  );

  // Keep it.
  await rec.getByRole('button', { name: 'Keep it' }).click();
  await page.locator('[data-take-review="kept"]').waitFor({ timeout: 30_000 });
  // The recorder said how the take went; the bell keeps its notice (read) instead of saying it again.
  await page.waitForTimeout(3500);
  const notices = await page.getByText(/^Your take is placed/).count();
  const bell = (await api(page, 'notification.list', { limit: 10 })).notifications.filter((x) => /^Your take is placed/.test(x.title));
  check('one message for the take (the bell keeps its notice, read, without a second toast)', notices === 0 && bell.length >= 1 && bell.every((x) => x.read), `${bell.length} notice(s), read: ${bell.map((x) => x.read).join(',')}`);
  const after = await narration();
  const plan = await api(page, 'revision.plan', { id: after.revisionId });
  const asset = after.beats[0].take?.assetId;
  check('every line plays a piece of the one take', after.beats.every((b) => b.take?.assetId === asset && b.take?.source === 'recorded'), JSON.stringify(after.beats.map((b) => b.takeLabel)));

  // Where the lines were spoken, by the witness: sound onsets on the page clock → the playhead.
  const spoken = (await spokenOnTimeline(page)).spoken.slice(0, 3);
  // Where MetaRush plays them: the take's own sound onsets mapped through the plan's narration element(s).
  const env = await takeEnvelope(page, asset);
  const fileOnsets = onsets(env.env, 0.02, 0.5).slice(0, 3);
  const elems = plan.audio.filter((a) => a.role === 'voiceover');
  const played = fileOnsets.map((x) => playedAt(plan, asset, x));
  const placeErr = spoken.map((s, i) => played[i] - s);
  measured.push(...placeErr.map((e, i) => `line ${i + 1}: played ${e >= 0 ? '+' : ''}${e.toFixed(3)} s from where it was spoken`));
  check(
    `placement: the take plays where it was spoken (within ±${TOL} s)`,
    spoken.length === 3 && placeErr.every((e) => Math.abs(e) <= TOL),
    placeErr.map((e) => e.toFixed(3)).join(', '),
  );
  const pinErr = after.beats.map((b, i) => b.start - spoken[i]);
  measured.push(...pinErr.map((e, i) => `line ${i + 1}: start point ${e.toFixed(3)} s from the spoken onset`));
  check(
    `pins: each line now starts where it was said (within ±${TOL} s)`,
    pinErr.every((e) => Math.abs(e) <= TOL) && after.beats.every((b) => b.anchor.kind === 'clip' || b.anchor.kind === 'moment'),
    after.beats.map((b, i) => `${b.placement} (${pinErr[i].toFixed(3)})`).join(' | '),
  );
  // Cut only in the pauses: around every cut between lines the take is silent.
  const quietAt = (t) => Math.max(...env.env.filter(([x]) => Math.abs(x - t) <= 0.02).map(([, v]) => v));
  const cuts = after.beats.slice(1).map((b) => b.take.in);
  check(
    'splitting: no line is cut inside a word (silence at every cut)',
    cuts.every((c, i) => quietAt(c) < 0.01 && Math.abs(after.beats[i].take.out - c) < 1e-6),
    cuts.map((c) => `${c.toFixed(2)} s: ${quietAt(c).toFixed(4)}`).join(', '),
  );
  check('the read plays as one piece until an edit moves a shot', elems.length === 1 && (elems[0].beatIds ?? []).length === 3);

  // Snap lines to their planned spots, then back.
  await rec.getByRole('button', { name: 'Snap lines to their planned spots' }).click();
  await rec.getByRole('button', { name: 'Back to where I said them' }).waitFor({ timeout: 30_000 });
  const snapped = await narration();
  check(
    'Snap lines to their planned spots: back to Claude\'s pins',
    snapped.beats[0].anchor.clipId === 'kitchen' && Math.abs((snapped.beats[0].anchor.offset ?? 0) - 0.4) < 1e-6 && snapped.beats[1].anchor.clipId === 'garden' && snapped.beats[2].anchor.kind === 'after',
    snapped.beats.map((b) => b.placement).join(' | '),
  );
  await shot(page, 'live-snapped');
  await rec.getByRole('button', { name: 'Back to where I said them' }).click();
  await rec.getByRole('button', { name: 'Snap lines to their planned spots' }).waitFor({ timeout: 30_000 });
  const back = await narration();
  check('and back where they were said', back.beats.every((b, i) => JSON.stringify(b.anchor) === JSON.stringify(after.beats[i].anchor)));
  await rec.getByRole('button', { name: 'Done' }).click();
  await rec.waitFor({ state: 'detached', timeout: 30_000 });
  await ctx.close();
  await browser.close();

  // 4. Re-record only line 2 (a second browser: its microphone says line 2 again).
  const browser2 = await chromium.launch({ args: fakeMic(process.env.LIVE_TAKE_B) });
  const b2 = await newPage(browser2, { state });
  const pg = b2.page;
  await pg.goto(`${studio}?tab=voiceover`);
  await pg.getByRole('list', { name: 'Narration lines' }).waitFor({ timeout: 20_000 });
  await pg.getByRole('button', { name: 'Record narration' }).click();
  const rec2 = pg.getByRole('dialog', { name: 'Record narration' });
  await rec2.getByRole('meter', { name: 'Microphone level' }).waitFor({ timeout: 20_000 });
  await rec2.getByRole('combobox', { name: /Re-record one line/ }).selectOption({ label: 'only line 2' });
  await pg.evaluate(() => {
    window.__tap = [];
    window.__ph = [];
  });
  await rec2.getByRole('button', { name: 'Record line 2 again' }).click();
  await pg.locator('[data-take-review="new"]').waitFor({ timeout: 120_000 });
  const spoken2 = (await spokenOnTimeline(pg)).spoken[0];
  const states = await pg.locator('[data-placed-line]').evaluateAll((els) => els.map((e) => `${e.getAttribute('data-placed-line')}:${e.getAttribute('data-state')}`));
  check('punch-in review: the new line 2 between lines 1 and 3, which stay as they were', states.join(' ') === 'line-1:kept line-2:placed line-3:kept', states.join(' '));
  await shot(pg, 'punch-in-review');
  await rec2.getByRole('button', { name: 'Keep it' }).click();
  await pg.locator('[data-take-review="kept"]').waitFor({ timeout: 30_000 });
  const punched = await api(pg, 'voiceover.show', { revisionId: (await api(pg, 'project.show', { id: p.id })).headRevisionId });
  const l1 = punched.beats[0].take;
  const l3 = punched.beats[2].take;
  check(
    'punch-in: only line 2 is replaced (lines 1 and 3 keep their audio and places)',
    punched.beats[1].take.assetId !== asset &&
      l1.assetId === asset &&
      l1.in === back.beats[0].take.in &&
      JSON.stringify(punched.beats[0].anchor) === JSON.stringify(back.beats[0].anchor) &&
      JSON.stringify(l3) === JSON.stringify(back.beats[2].take) &&
      JSON.stringify(punched.beats[2].anchor) === JSON.stringify(back.beats[2].anchor),
    `line 1 now ends at ${l1.out.toFixed(2)} s of the take (was ${back.beats[0].take.out.toFixed(2)})`,
  );
  // The new line 2 plays where it was said; line 1 keeps its words (its sound stops in silence).
  const asset2 = punched.beats[1].take.assetId;
  const plan2 = await api(pg, 'revision.plan', { id: punched.revisionId });
  const env2 = await takeEnvelope(pg, asset2);
  const err2 = playedAt(plan2, asset2, onsets(env2.env, 0.02, 0.5)[0]) - spoken2;
  const pinErr2 = punched.beats[1].start - spoken2;
  measured.push(`re-recorded line 2: played ${err2 >= 0 ? '+' : ''}${err2.toFixed(3)} s from where it was spoken, start point ${pinErr2.toFixed(3)} s from the onset`);
  check(`punch-in: the new line 2 plays where it was said (within ±${TOL} s)`, Math.abs(err2) <= TOL && Math.abs(pinErr2) <= TOL, `${err2.toFixed(3)}, start ${pinErr2.toFixed(3)}`);
  check('punch-in: line 1 keeps its words (it stops in silence)', quietAt(l1.out) < 0.01, `${l1.out.toFixed(2)} s: ${quietAt(l1.out).toFixed(4)}`);
  await rec2.getByRole('button', { name: 'Done' }).click();
  await rec2.waitFor({ state: 'detached', timeout: 30_000 });
  // All takes are kept: line 2 has both; the first one goes back where it was said.
  await pg.getByRole('listitem', { name: 'Line 2' }).getByRole('button', { name: /takes$/ }).click();
  const takesDlg = pg.getByRole('dialog', { name: 'Takes for line 2' });
  await takesDlg.getByRole('button', { name: 'Use this take' }).first().waitFor({ timeout: 20_000 });
  const ts = await api(pg, 'voiceover.takes', { revisionId: punched.revisionId, beat: 'line-2' });
  check('both takes of line 2 are kept', ts.takes.length === 2, ts.takes.map((t) => t.summary).join(' | '));
  await takesDlg.getByRole('button', { name: 'Use this take' }).first().click();
  await pg.waitForTimeout(1500);
  const ab = await api(pg, 'voiceover.show', { revisionId: (await api(pg, 'project.show', { id: p.id })).headRevisionId });
  check('A/B: the first take of line 2 plays again, where it was said', ab.beats[1].take.assetId === asset && JSON.stringify(ab.beats[1].anchor) === JSON.stringify(back.beats[1].anchor));
  await pg.keyboard.press('Escape');
  await b2.ctx.close();

  // 5. Screenshots: the recorder, the review and the comparison on a phone and in light.
  for (const theme of ['dark', 'light']) {
    for (const mobile of [false, true]) {
      const tag = `${mobile ? 'phone' : 'desktop'}-${theme}`;
      const v = await newPage(browser2, { theme, mobile, state });
      await v.page.goto(`${studio}?tab=voiceover`);
      await v.page.locator('[data-version-switch]').waitFor({ timeout: 20_000 });
      await shot(v.page, `voiceover-tab-${tag}`);
      await v.page.locator('[data-version-switch]').getByRole('button', { name: 'Compare' }).click();
      await v.page.locator('[data-fit-review="cut"]').waitFor();
      await v.page.locator('[data-fit-review="cut"]').scrollIntoViewIfNeeded();
      await shot(v.page, `compare-${tag}`);
      await v.page.getByRole('button', { name: 'Close the comparison' }).click();
      await v.page.getByRole('button', { name: 'Record narration' }).click();
      await v.page.getByRole('dialog', { name: 'Record narration' }).locator('[data-mini-timeline]').waitFor({ timeout: 20_000 });
      await shot(v.page, `recorder-live-${tag}`);
      await v.ctx.close();
    }
  }
  await browser2.close();

  check('no native dialogs, page errors or horizontal overflow', problems.length === 0, problems.join(' | '));
  const failed = results.filter((r) => !r.ok);
  console.log(`\nmeasured:\n  ${measured.join('\n  ')}`);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed; screenshots in ${shotsDir}`);
  if (failed.length) process.exitCode = 1;
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
