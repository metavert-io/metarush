#!/usr/bin/env node
// Record your screen end to end (D258–D262), in a real browser with Chromium's fake
// camera and microphone and a fake screen (run it through scripts/recorder-e2e.sh,
// which builds the throwaway installation):
//
//   1. The recorder opens from a project's Add box with the microphone and its level;
//      the screen (a window), the camera, the layout and a pasted script are chosen.
//   2. A recording with a countdown: the teleprompter shows the lines, Space brings the
//      next one in, a pause stops the clock; stopped, it is sent and becomes footage.
//   3. The three tracks are footage of the same length with the pause left out of each;
//      the voice's place on the clock agrees with lining the files up by their sound;
//      a cut starts with the camera in a corner.
//   4. A tab closed mid-recording: the next page offers to send the rest and keep it.
//   5. Screen sharing ending stops the recording by itself and keeps it.
//   6. A refused microphone and one that never answers are said plainly.
// Screenshots (desktop and phone, dark and light) go to RECORDER_SHOTS.
//
// It CHANGES the project, so point it only at a throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'RECORDER_PROJECT', 'RECORDER_MIC', 'RECORDER_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/recorder-e2e.sh does)`);
    process.exit(2);
  }
}
const P = process.env.RECORDER_PROJECT;
const shotsDir = process.env.RECORDER_SHOTS;
mkdirSync(shotsDir, { recursive: true });
const results = [];
const problems = [];
function check(name, ok, detail = '') {
  results.push({ name, ok });
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
 * The microphone: Chromium's fake device, asked for by its ID. Asked for the default,
 * Chromium's audio service opens the computer's real default input to resolve it, even
 * with the fake device (a virtual input that never answers hangs it); the same rule as
 * the other browser runs' fake-mic helper.
 */
const fakeMicByID = (ctx) =>
  ctx.addInitScript(() => {
    const md = navigator.mediaDevices;
    if (!md?.getUserMedia) return;
    const real = md.getUserMedia.bind(md);
    md.getUserMedia = async (c) => {
      const audio = c?.audio;
      if (audio === true || (audio && typeof audio === 'object' && !audio.deviceId)) {
        const devices = await md.enumerateDevices();
        const fake = devices.find((d) => d.kind === 'audioinput' && d.deviceId !== 'default' && /^Fake Audio Input/.test(d.label));
        if (fake) c = { ...c, audio: { ...(audio === true ? {} : audio), deviceId: { exact: fake.deviceId } } };
      }
      return real(c);
    };
  });

/* The screen: an animated canvas that says it is a window (headless Chromium has no screen to share). */
const fakeScreen = (ctx) =>
  ctx.addInitScript(() => {
    const md = navigator.mediaDevices;
    if (!md) return;
    md.getDisplayMedia = async () => {
      const c = document.createElement('canvas');
      c.width = 1280;
      c.height = 720;
      const g = c.getContext('2d');
      let n = 0;
      const draw = () => {
        g.fillStyle = '#1d3557';
        g.fillRect(0, 0, 1280, 720);
        g.fillStyle = '#f1faee';
        g.font = '64px sans-serif';
        g.fillText(`A product demo, frame ${n++}`, 80, 200);
        g.fillRect((n * 9) % 1200, 400, 80, 80);
      };
      draw();
      setInterval(draw, 33);
      const s = c.captureStream(30);
      const v = s.getVideoTracks()[0];
      const settings = v.getSettings.bind(v);
      v.getSettings = () => ({ ...settings(), displaySurface: 'window', width: 1280, height: 720, frameRate: 30 });
      Object.defineProperty(v, 'label', { value: 'Docs: a fake window' });
      window.__screenTrack = v;
      return s;
    };
  });

const setTheme = (t) => (ctx) =>
  ctx.addInitScript((theme) => {
    try {
      localStorage.setItem('metarush.theme', theme);
    } catch {
      /* private mode */
    }
  }, t);

async function newPage(browser, { theme = 'dark', mobile = false, state, mic } = {}) {
  const ctx = await browser.newContext({
    viewport: mobile ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: mobile ? 2 : 1,
    colorScheme: theme,
    reducedMotion: 'reduce',
    permissions: ['microphone', 'camera'],
    ...(state ? { storageState: state } : {}),
  });
  await setTheme(theme)(ctx);
  await fakeScreen(ctx);
  if (mic) await mic(ctx);
  else await fakeMicByID(ctx);
  const page = await watch(await ctx.newPage());
  return { ctx, page };
}

function watch(page) {
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) problems.push(`console: ${m.text()}`);
  });
  page.on('dialog', (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  return page;
}

let shotN = 0;
async function shot(page, name) {
  await page.waitForTimeout(400);
  await page.screenshot({ path: path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`) });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
}

const recorder = (page) => page.locator('[data-mr-screen-recorder]');
const phase = (page) => recorder(page).getAttribute('data-phase');
const elapsed = async (page) => Number(await page.locator('[data-elapsed]').getAttribute('data-elapsed'));

async function openRecorder(page) {
  await page.goto(`${base}/record?project=${P}`);
  await recorder(page).getByRole('meter', { name: 'Microphone level' }).waitFor({ timeout: 25_000 });
}

async function waitFor(fn, ms, what) {
  const until = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > until) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 500));
  }
}

const args = ['--autoplay-policy=no-user-gesture-required', '--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', `--use-file-for-fake-audio-capture=${process.env.RECORDER_MIC}`];

try {
  const browser = await chromium.launch({ args });
  const { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
  const state = await ctx.storageState();

  // 1. From the project's Add box: the sources, the layout and a script.
  await page.goto(`${base}/p/${P}?tab=sources`);
  await page.locator('[data-record-screen]').first().click();
  await page.waitForURL(/\/record\?project=/, { timeout: 10_000 });
  await recorder(page).getByRole('meter', { name: 'Microphone level' }).waitFor({ timeout: 25_000 });
  check('the recorder opens from the Add box with the microphone and its level', true);
  await recorder(page).locator('[data-choose-screen]').click();
  await recorder(page).locator('[data-surface="window"]').waitFor({ timeout: 10_000 });
  check('a window is shared, and the page says the teleprompter stays out of it', await recorder(page).getByText('this window and the teleprompter stay out of it').isVisible());
  const camValues = await recorder(page).locator('[data-camera-picker] option').evaluateAll((os) => os.map((o) => o.value));
  await recorder(page).locator('[data-camera-picker]').selectOption(camValues.find((v) => v));
  await recorder(page).locator('[data-source="camera"] video').waitFor({ timeout: 15_000 });
  check('the fake camera shows in its bubble', true);
  await recorder(page).getByRole('radio', { name: 'Camera in a corner' }).click();
  await recorder(page).getByRole('radio', { name: 'Paste a script' }).click();
  await recorder(page).locator('[data-script]').fill('Welcome to the demo of the new dashboard.\n\nHere is where the recordings land.\n\nAnd that is how you record your screen.');
  check('the pasted script is three lines', (await recorder(page).locator('[data-script-lines]').getAttribute('data-script-lines')) === '3');
  await recorder(page).locator('[data-recording-name]').fill('Product demo');
  await shot(page, 'setup-desktop-dark');
  // The teleprompter stays in this page here (the floating window is checked in 4).
  const float = recorder(page).getByLabel('Float it above other windows while recording');
  if (await float.count()) await float.uncheck();

  // 2. Recording: countdown, the teleprompter, a line brought in early, a pause, stop.
  await recorder(page).locator('[data-record]').click();
  await page.locator('[data-countdown]').waitFor({ timeout: 5000 });
  check('a 3-2-1 countdown before recording', true);
  await waitFor(async () => (await phase(page)) === 'recording', 15_000, 'recording');
  await page.waitForTimeout(1600);
  check('the teleprompter shows the first line once recording', (await page.locator('[data-screen-prompter]').getAttribute('data-screen-prompter')) === '0');
  await shot(page, 'recording-desktop-dark');
  await page.keyboard.press('Space');
  await page.waitForTimeout(300);
  check('Space brings the next line in', (await page.locator('[data-screen-prompter]').getAttribute('data-screen-prompter')) === '1');
  await page.locator('[data-pause]').click();
  const e1 = await elapsed(page);
  await page.waitForTimeout(1500);
  const e2 = await elapsed(page);
  check('pausing stops the clock (and the teleprompter)', e2 - e1 < 0.3, `${e1} → ${e2}`);
  await page.locator('[data-pause]').click();
  await page.waitForTimeout(3500);
  const recorded = await elapsed(page);
  await page.locator('[data-stop]').click();
  await page.locator('[data-done]').waitFor({ timeout: 120_000 });
  check('stopped, it is sent and added to the Library', (await page.locator('[data-done]').getAttribute('data-done')) === 'ready');
  await shot(page, 'done-desktop-dark');

  // 3. The footage, its clock and a cut.
  const list = await api(page, 'recording.list', { projectId: P });
  const r = list.recordings[0].recording;
  const ids = Object.fromEntries(r.tracks.map((t) => [t.id, t.assetId]));
  check('three tracks became footage', ['screen', 'camera', 'voice'].every((k) => ids[k]), JSON.stringify(ids));
  check('the pause is recorded on the clock', r.pauses?.length === 1 && Math.abs(r.pauses[0].length - 1.5) < 0.4, JSON.stringify(r.pauses));
  const durs = {};
  for (const [k, id] of Object.entries(ids)) durs[k] = (await api(page, 'asset.show', { id })).asset.media.duration;
  const spread = Math.max(...Object.values(durs)) - Math.min(...Object.values(durs));
  check('the files are the same length, the pause left out of each', spread < 0.4 && Math.abs(durs.voice - recorded) < 0.6, `${JSON.stringify(durs)}, recorded ${recorded.toFixed(2)} s`);
  const voice = r.tracks.find((t) => t.id === 'voice');
  check('the voice has its measured place on the clock', Math.abs(voice.start) < 0.5 && r.clock?.method === 'clock', `start ${voice.start} s, spread ${r.clock?.spread} ms`);
  // The recording keeps its clock; its conversation was lined up again by the sound the
  // pictures carry (the microphone's copy): the two must agree.
  const cnv = (await api(page, 'conversation.show', { conversation: r.links[0]?.conversationId })).conversation;
  const at = (id) => cnv.members.find((m) => m.id === id).start;
  const clock = (id) => r.tracks.find((t) => t.id === id).start;
  const soundGap = at('voice') - at('camera');
  const clockGap = clock('voice') - clock('camera');
  check('MetaRush checked the clock by the sound the pictures carry', cnv.method === 'audio', `${cnv.method}, ${cnv.sure}`);
  check('the clock agrees with the sound for the camera (within 0.1 s)', Math.abs(clockGap - soundGap) <= 0.1, `clock ${clockGap.toFixed(3)} s, sound ${soundGap.toFixed(3)} s`);
  const screenSound = at('voice') - at('screen');
  check('and for the screen', Math.abs(clock('voice') - clock('screen') - screenSound) <= 0.1, `sound ${screenSound.toFixed(3)} s`);
  await page.locator('[data-done] [data-start-cut="pip"]').click();
  await page.waitForURL(new RegExp(`/p/${P}`), { timeout: 30_000 });
  const head = (await api(page, 'project.show', { id: P })).headRevisionId;
  const spec = (await api(page, 'revision.show', { id: head })).spec;
  const sh = spec.clips[0]?.shots?.[0];
  check('a cut starts with the screen and the camera in a corner', sh?.layout === 'pip' && sh.members?.join(',') === 'screen,camera' && !!spec.conversations?.call, JSON.stringify(sh));
  await shot(page, 'cut-desktop-dark');

  // 4. A tab closed mid-recording: the next page sends the rest and keeps it.
  const p2 = watch(await ctx.newPage());
  await openRecorder(p2);
  await recorder(p2).locator('[data-choose-screen]').click();
  await recorder(p2).locator('[data-surface="window"]').waitFor({ timeout: 10_000 });
  await recorder(p2).locator('[data-recording-name]').fill('Closed tab');
  await recorder(p2).getByRole('radio', { name: 'Paste a script' }).click();
  await recorder(p2).locator('[data-script]').fill('One line to read.\n\nAnd another.');
  const floats = (await recorder(p2).getByLabel('Float it above other windows while recording').count()) > 0;
  await recorder(p2).locator('[data-record]').click();
  await waitFor(async () => (await phase(p2)) === 'recording', 15_000, 'recording');
  if (floats) check('the teleprompter floats in its own window when asked', await p2.getByText('The teleprompter floats in its own window.').isVisible());
  await p2.waitForTimeout(4500);
  const open = (await api(page, 'recording.list', { projectId: P })).recordings.find((x) => x.recording.name === 'Closed tab');
  await p2.close();
  const p3 = watch(await ctx.newPage());
  // Someone else who used this browser left a recording here: it is theirs, never offered to this person.
  await p3.goto(`${base}/library`);
  const others = 'rcd_01someoneelsesrecording0';
  await p3.evaluate(
    (id) =>
      new Promise((resolve, reject) => {
        const req = indexedDB.open('metarush-recorder', 1);
        req.onupgradeneeded = () => {
          req.result.createObjectStore('pieces', { keyPath: ['rec', 'track', 'offset'] });
          req.result.createObjectStore('recordings', { keyPath: 'id' });
        };
        req.onsuccess = () => {
          const tx = req.result.transaction('recordings', 'readwrite');
          tx.objectStore('recordings').put({ id, userId: 'usr_01someoneelse00000000000', name: 'Someone else’s', tracks: [], stopped: false, startedAt: 0 });
          tx.oncomplete = () => (req.result.close(), resolve(null));
          tx.onerror = () => reject(tx.error);
        };
        req.onerror = () => reject(req.error);
      }),
    others,
  );
  await openRecorder(p3);
  await p3.locator(`[data-unfinished-recording="${open.recording.id}"]`).waitFor({ timeout: 15_000 });
  check('the next page says a recording didn’t finish', true);
  check('a recording someone else left in this browser is not offered', (await p3.locator(`[data-unfinished-recording="${others}"]`).count()) === 0);
  await shot(p3, 'unfinished-desktop-dark');
  await p3.locator(`[data-unfinished-recording="${open.recording.id}"] [data-keep-unfinished]`).click();
  const kept = await waitFor(
    async () => {
      const x = (await api(p3, 'recording.show', { recording: open.recording.id })).recording;
      return x.state === 'ready' ? x : null;
    },
    90_000,
    'the kept recording',
  );
  const keptDur = (await api(p3, 'asset.show', { id: kept.tracks.find((t) => t.id === 'voice').assetId })).asset.media.duration;
  check('what the closed tab recorded is kept as footage', kept.ended === 'kept' && keptDur > 3 && kept.tracks.every((t) => t.assetId), `voice ${keptDur.toFixed(2)} s, ended ${kept.ended}`);

  // 5. Screen sharing ends: the recording stops by itself and is kept.
  await openRecorder(p3);
  await recorder(p3).locator('[data-choose-screen]').click();
  await recorder(p3).locator('[data-surface="window"]').waitFor({ timeout: 10_000 });
  await recorder(p3).locator('[data-recording-name]').fill('Sharing ended');
  await recorder(p3).locator('[data-record]').click();
  await waitFor(async () => (await phase(p3)) === 'recording', 15_000, 'recording');
  await p3.waitForTimeout(2500);
  await p3.evaluate(() => window.__screenTrack.dispatchEvent(new Event('ended')));
  await p3.locator('[data-done]').waitFor({ timeout: 90_000 });
  check('screen sharing ending stops it and keeps it, said plainly', await p3.getByText('Screen sharing ended, so the recording stopped').isVisible());
  await p3.close();

  // 6. A refused microphone, and one that never answers (the deadline).
  const refuse = (c) =>
    c.addInitScript(() => {
      const md = navigator.mediaDevices;
      const real = md.getUserMedia.bind(md);
      md.getUserMedia = async (x) => {
        if (x?.audio) throw new DOMException('Permission denied', 'NotAllowedError');
        return real(x);
      };
    });
  const silent = (c) =>
    c.addInitScript(() => {
      const md = navigator.mediaDevices;
      const real = md.getUserMedia.bind(md);
      md.getUserMedia = (x) => (x?.audio ? new Promise(() => {}) : real(x));
    });
  const r1 = await newPage(browser, { state, mic: refuse });
  await r1.page.goto(`${base}/record?project=${P}`);
  await r1.page.locator('[data-source="microphone"] [data-problem="denied"]').waitFor({ timeout: 15_000 });
  check('a refused microphone is said plainly, with what to do', await r1.page.getByText("MetaRush isn't allowed to use the microphone yet.").isVisible());
  await shot(r1.page, 'mic-refused-desktop-dark');
  await r1.ctx.close();
  const r2 = await newPage(browser, { state, mic: silent, theme: 'light' });
  await r2.page.goto(`${base}/record?project=${P}`);
  const t0 = Date.now();
  await r2.page.locator('[data-source="microphone"] [data-problem="noanswer"]').waitFor({ timeout: 20_000 });
  check('a microphone that never answers is given up on after the deadline, with a picker', (await r2.page.locator('[data-mic-picker]').count()) === 1, `${((Date.now() - t0) / 1000).toFixed(1)} s`);
  await shot(r2.page, 'mic-noanswer-desktop-light');
  await r2.ctx.close();

  // Phone, light: the setup fits.
  const ph = await newPage(browser, { state, mobile: true, theme: 'light' });
  await openRecorder(ph.page);
  await shot(ph.page, 'setup-phone-light');
  await ph.ctx.close();
  await browser.close();
} catch (e) {
  check('the run finished', false, e instanceof Error ? e.stack : String(e));
}

for (const p of problems) check('no page problem', false, p);
const failed = results.filter((r) => !r.ok).length;
console.log(`\nrecorder-e2e: ${results.length - failed}/${results.length} checks passed; screenshots in ${shotsDir}`);
process.exit(failed ? 1 : 0);
