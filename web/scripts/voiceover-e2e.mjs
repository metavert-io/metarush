#!/usr/bin/env node
// Drives the studio's Voiceover tab end to end in a real browser (run it through
// scripts/voiceover-e2e.sh, which builds a throwaway installation for it):
//   Add voiceover from the Edit tab · write a script (pasted, split into
//   lines) · a fit warning fixed with one click · a line dragged on the timeline's
//   narration lane (tied to the shot it lands on, saved in the background, undone;
//   moved with the arrow keys; ⌥ pins it to the clock) · a line placed
//   "when someone says…" · Ask Claude (the panel opens with the request) · record
//   lines with the microphone (count-in, teleprompter, retake keeps both) · takes
//   A/B and clean-up · the mixer (a change heard in the plan, then saved) · the AI
//   voice against the fake ElevenLabs API (no key → Settings; price first; make;
//   make again) · import a recording · a denied microphone explained · scrolling never
//   puts the preview over the script cards or the mixer (right after recording, then on a
//   desktop, a smaller desktop window and a phone).
// Screenshots (desktop and phone, dark and light) go to VO_SHOTS.
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
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'VO_PROJECT', 'VO_KEY_FILE', 'VO_FAKE_URL', 'VO_FAKE_MIC', 'VO_IMPORT_FILE', 'VO_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/voiceover-e2e.sh does)`);
    process.exit(2);
  }
}
const shotsDir = process.env.VO_SHOTS;
mkdirSync(shotsDir, { recursive: true });

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
 * The microphone. Chromium's fake device (--use-fake-device-for-media-stream with
 * --use-file-for-fake-audio-capture) is used when it works. On macOS a headless
 * Chromium waits forever for the system's microphone permission, so there the
 * same WAV is played into the page through Web Audio by a stand-in for
 * getUserMedia: everything after the device (level meter, capture, WAV, upload,
 * placement) runs for real. window.__voDenyMic makes it refuse, as a person can.
 */
const fakeMicFlags = ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', `--use-file-for-fake-audio-capture=${process.env.VO_FAKE_MIC}`];
async function chromiumMicWorks() {
  const b = await chromium.launch({ args: fakeMicFlags });
  try {
    const ctx = await b.newContext({ permissions: ['microphone'] });
    const p = await ctx.newPage();
    await p.goto(`${base}/api/health`);
    return await p.evaluate(async () => {
      try {
        const s = await Promise.race([navigator.mediaDevices.getUserMedia({ audio: true }), new Promise((_, rej) => setTimeout(() => rej(new Error('timeout')), 4000))]);
        s.getTracks().forEach((t) => t.stop());
        return true;
      } catch {
        return false;
      }
    });
  } catch {
    return false;
  } finally {
    await Promise.race([b.close(), new Promise((r) => setTimeout(r, 3000))]);
  }
}

async function useStandInMic(ctx) {
  await ctx.route('**/__vo-fake-mic.wav', (route) => route.fulfill({ path: process.env.VO_FAKE_MIC, contentType: 'audio/wav' }));
  await ctx.addInitScript(() => {
    const md = navigator.mediaDevices;
    if (!md) return;
    const real = md.getUserMedia.bind(md);
    md.getUserMedia = async (c) => {
      if (!c || !c.audio) return real(c);
      if (window.__voDenyMic) throw new DOMException('Permission denied', 'NotAllowedError');
      const actx = new AudioContext({ sampleRate: 48000 });
      const buf = await actx.decodeAudioData(await (await fetch('/__vo-fake-mic.wav')).arrayBuffer());
      const src = actx.createBufferSource();
      src.buffer = buf;
      src.loop = true;
      const dst = actx.createMediaStreamDestination();
      src.connect(dst);
      src.start();
      void actx.resume();
      return dst.stream;
    };
    md.enumerateDevices = async () => [{ kind: 'audioinput', deviceId: 'test-mic', label: 'Test microphone', groupId: 'test', toJSON() { return this; } }];
  });
}

// With Chromium's fake device the fake UI always allows the microphone, so a
// person refusing it is played by wrapping getUserMedia the same way.
async function useDeniableMic(ctx) {
  await ctx.addInitScript(() => {
    const md = navigator.mediaDevices;
    if (!md) return;
    const real = md.getUserMedia.bind(md);
    md.getUserMedia = async (c) => {
      if (c && c.audio && window.__voDenyMic) throw new DOMException('Permission denied', 'NotAllowedError');
      return real(c);
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

// A smaller desktop window: a landscape cut stacks the preview above the panel below 1280 px.
const LAPTOP = { width: 1180, height: 800 };

async function newPage(browser, { theme = 'dark', mobile = false, viewport, standIn, state } = {}) {
  const ctx = await browser.newContext({
    viewport: viewport ?? (mobile ? { width: 390, height: 844 } : { width: 1440, height: 900 }),
    deviceScaleFactor: mobile ? 2 : 1,
    colorScheme: theme,
    reducedMotion: 'reduce',
    permissions: ['microphone'],
    ...(state ? { storageState: state } : {}),
  });
  await setTheme(theme)(ctx);
  if (standIn) await useStandInMic(ctx);
  else await useDeniableMic(ctx);
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

const toast = (page, text) => page.getByText(text).first().waitFor({ timeout: 30_000 });

/*
 * The preview next to the lines and the mixer. On a window too narrow for two columns it once stayed
 * pinned over them while the page scrolled. previewOverlap scrolls the page top to bottom and, at
 * every step, measures the preview's picture against each script card and mixer section on screen;
 * it returns the worst overlap (px²) and ends at the bottom of the page.
 */
async function previewOverlap(page) {
  await page.locator('[data-vo-preview] [data-player-surface]').waitFor({ timeout: 20_000 });
  // The picture takes the cut's shape once its plan is in (a video in the player).
  await page.locator('[data-vo-preview] [data-player-surface] video').first().waitFor({ timeout: 20_000 }).catch(() => undefined);
  const max = await page.evaluate(() => document.documentElement.scrollHeight - window.innerHeight);
  let worst = { area: 0, what: '', y: 0 };
  for (let y = 0; ; y = Math.min(max, y + 60)) {
    await page.evaluate((to) => window.scrollTo(0, to), y);
    await page.waitForTimeout(40);
    const m = await page.evaluate(() => {
      const f = document.querySelector('[data-vo-preview] [data-player-surface] > div')?.getBoundingClientRect();
      let area = 0;
      let what = '';
      for (const el of document.querySelectorAll('ol[aria-label="Narration lines"] > li, section[aria-labelledby^="mix-"]')) {
        if (!f || !el.getClientRects().length) continue;
        const b = el.getBoundingClientRect();
        const w = Math.min(f.right, b.right) - Math.max(f.left, b.left);
        const h = Math.min(f.bottom, b.bottom, window.innerHeight) - Math.max(f.top, b.top, 0);
        if (w > 0 && h > 0 && w * h > area) {
          area = Math.round(w * h);
          what = el.getAttribute('aria-label') || document.getElementById(el.getAttribute('aria-labelledby') ?? '')?.textContent || '';
        }
      }
      return { area, what };
    });
    if (m.area > worst.area) worst = { ...m, y };
    if (y >= max) break;
  }
  return worst;
}

/**
 * How the preview's column behaves: beside the panel it stays in view (sticky below the top bar and
 * the section tabs, opaque, under menus and dialogs); above it (and always on a phone) it scrolls
 * with the page.
 */
async function previewColumn(page, mobile) {
  const c = await page.evaluate(() => {
    const col = document.querySelector('[data-vo-preview]');
    const panel = col?.nextElementSibling;
    if (!col || !panel) return null;
    const cs = getComputedStyle(col);
    const tabs = document.querySelector('[role="tablist"][aria-label="Project sections"]')?.parentElement;
    // Where the top bar and the section tabs end once the page scrolls (both stick).
    const chrome = Math.max(document.querySelector('header')?.getBoundingClientRect().height ?? 0, tabs ? parseFloat(getComputedStyle(tabs).top) + tabs.getBoundingClientRect().height : 0);
    const bg = cs.backgroundColor.match(/[\d.]+/g)?.map(Number) ?? [];
    return {
      beside: col.getBoundingClientRect().right <= panel.getBoundingClientRect().left + 1,
      position: cs.position,
      top: parseFloat(cs.top),
      z: cs.zIndex === 'auto' ? null : Number(cs.zIndex),
      opaque: bg.length === 3 || bg[3] === 1,
      chrome: Math.round(chrome),
    };
  });
  if (!c) return { ok: false, how: 'no preview found' };
  const pinned = c.position === 'sticky' || c.position === 'fixed';
  if (c.beside && !mobile)
    return {
      ok: c.position === 'sticky' && c.top >= c.chrome && c.z !== null && c.z > 0 && c.z < 20 && c.opaque,
      how: `sits beside the panel, ${c.position} at ${c.top}px (the section tabs end at ${c.chrome}px), z-index ${c.z ?? 'auto'} (menus: 20), ${c.opaque ? 'opaque' : 'see-through'}`,
    };
  return { ok: !c.beside && !pinned, how: `sits ${c.beside ? 'beside' : 'above'} the panel and ${pinned ? 'stays pinned' : 'scrolls with the page'} (${c.position})` };
}

const overlapText = (o, what) => (o.area ? `${o.area}px² over ${o.what || what} at scroll ${o.y}` : `never over the ${what}`);

/** Something in flight: the activity answer says a file is being processed, so the strip under the top bar shows. */
async function busyActivity(ctx) {
  await ctx.route('**/api/ops/activity.summary', async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    if (body?.ok && body.data) {
      body.data.counts = { ...body.data.counts, active: Math.max(1, body.data.counts?.active ?? 0), processing: Math.max(1, body.data.counts?.processing ?? 0) };
      body.data.headline = 'Processing 1 file';
    }
    await route.fulfill({ response: res, json: body });
  });
}

/**
 * The section tabs once the page has scrolled (both the top bar and the tabs stick): the bar's real
 * height, the tabs' place, which tabs the bar covers (what is drawn at each tab's center), and where
 * the preview sticks (beside the panel) compared with the tabs' bottom.
 */
async function tabsUnderBar(page) {
  // The top bar is the page's first header; it grows once the strip shows.
  await page.waitForFunction(() => !!document.querySelector('header [data-activity-strip]'), null, { timeout: 15_000 }).catch(() => undefined);
  await page.evaluate(() => window.scrollTo(0, Math.min(600, document.documentElement.scrollHeight - window.innerHeight)));
  await page.waitForTimeout(300);
  return page.evaluate(() => {
    const bar = document.querySelector('header')?.getBoundingClientRect();
    const tabs = document.querySelector('[data-studio-tabs]')?.getBoundingClientRect();
    const covered = [];
    for (const t of document.querySelectorAll('[data-studio-tabs] [role="tab"]')) {
      const r = t.getBoundingClientRect();
      // Scrolled out of the strip of tabs, or only a sliver of it showing at the edge (the strip scrolls).
      if (Math.min(r.right, window.innerWidth) - Math.max(r.left, 0) < 24) continue;
      const x = Math.min(window.innerWidth - 1, Math.max(0, (Math.max(r.left, 0) + Math.min(r.right, window.innerWidth)) / 2));
      for (const y of [r.top + 2, (r.top + r.bottom) / 2, r.bottom - 2]) {
        const hit = document.elementFromPoint(x, y);
        if (!hit || !t.contains(hit)) {
          covered.push(`${t.textContent?.trim()} at ${Math.round(y)}px`);
          break;
        }
      }
    }
    const prev = document.querySelector('[data-vo-preview]');
    const sticky = prev && getComputedStyle(prev).position === 'sticky' ? Math.round(prev.getBoundingClientRect().top) : null;
    const strip = !!document.querySelector('header [data-activity-strip]');
    const ok = !!bar && !!tabs && strip && tabs.top >= bar.bottom - 0.5 && covered.length === 0 && (sticky === null || sticky >= tabs.bottom - 0.5);
    return { ok, bar: Math.round(bar?.height ?? 0), strip, tabsTop: Math.round(tabs?.top ?? 0), tabsBottom: Math.round(tabs?.bottom ?? 0), covered, preview: sticky };
  });
}

async function main() {
  const flagsMic = await chromiumMicWorks();
  console.log(`microphone: ${flagsMic ? "Chromium's fake device" : 'stand-in through Web Audio (fake device capture is blocked here)'}`);
  const browser = await chromium.launch({ args: ['--autoplay-policy=no-user-gesture-required', ...(flagsMic ? fakeMicFlags : [])] });
  const { ctx, page } = await newPage(browser, { standIn: !flagsMic });
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
  const projects = (await api(page, 'project.list')).projects;
  const p = projects.find((x) => x.name === process.env.VO_PROJECT);
  if (!p) throw new Error(`no project ${process.env.VO_PROJECT}`);
  const head = async () => (await api(page, 'project.show', { id: p.id })).headRevisionId;
  const narration = async () => api(page, 'voiceover.show', { revisionId: await head() });
  const studio = `${base}/p/${p.id}`;

  // 1. Add voiceover from the Edit tab (the timeline's empty narration lane): the chooser, for the whole video.
  await page.goto(`${studio}?tab=cut`);
  const lane = page.locator('[data-narration-lane]');
  await lane.getByRole('button', { name: 'Add voiceover' }).click();
  const chooser = page.getByRole('dialog', { name: 'Add voiceover' });
  await chooser.waitFor();
  check('the chooser offers five ways', (await chooser.getByRole('button', { name: /Write my own script|Ask Claude|Record it now|AI voice/ }).count()) === 4 && (await chooser.getByLabel(/Use a recording I have/).count()) === 1);
  await shot(page, 'chooser');
  await chooser.getByRole('button', { name: /Write my own script/ }).click();
  await page.waitForURL(/tab=voiceover/);

  // 2. Write: a pasted script becomes lines; the long last line does not fit.
  const script = page.getByRole('textbox', { name: 'Script' });
  await script.waitFor();
  // Start points written in the script (markup) place lines even when it is used as-is (D86).
  await script.fill(
    '[@kitchen+0.3] Every morning starts in the kitchen.\n\n[@garden+0.3] (warm) Then the garden wakes up.\n\n[@payoff+0.3] And by noon, everything is green, and every single leaf, branch and flower in the whole wide garden is shining brightly in the warm sun.',
  );
  check('a pasted script is used as-is unless you ask Claude to fit it', (await page.getByRole('radio', { name: /Use my script as-is/ }).getAttribute('aria-checked')) === 'true');
  await page.getByRole('button', { name: 'Use my script' }).click();
  await toast(page, '3 narration lines read from the script');
  const lines = page.getByRole('list', { name: 'Narration lines' });
  await lines.waitFor();
  let n = await narration();
  check('write: three lines, the delivery note kept', n.beats.length === 3 && n.beats[1].delivery === 'warm', JSON.stringify(n.beats.map((b) => b.placement)));
  check('lines start conversationally (on shots)', n.beats.every((b) => b.anchor.kind === 'clip' || b.anchor.kind === 'after'));

  // 3. Fit: line 3 runs over; one click fixes it.
  const card3 = lines.getByRole('listitem', { name: 'Line 3' });
  const warn = card3.getByRole('group', { name: 'Line 3 does not fit' });
  await warn.waitFor();
  const warnText = await warn.innerText();
  check('fit warning in plain words, never negative', /before the video ends|before the next line|before line \d starts/.test(warnText) && !/-\d/.test(warnText), warnText.split('\n')[0]);
  await warn.scrollIntoViewIfNeeded();
  await shot(page, 'script-fit-warning');
  const startEarlier = warn.getByRole('button', { name: /^Start it .* earlier/ });
  await startEarlier.click();
  await toast(page, 'Fixed:');
  n = await narration();
  const f3 = n.fit.find((f) => f.beatId === n.beats[2].id);
  check('fit fix applied in one click', f3 && f3.status !== 'over', f3?.message);

  // 4. The narration lane on the timeline (Edit tab): drag line 2 later and it is tied to
  //    the shot it lands on, saved in the background as a timeline edit; undo puts it back; the
  //    arrow keys move it too; ⌥ pins it to the clock.
  await page.goto(`${studio}?tab=cut`);
  await lane.getByRole('listitem').first().waitFor({ timeout: 20_000 });
  const bar = () => lane.getByRole('listitem', { name: /^Narration line 2:/ });
  await bar().scrollIntoViewIfNeeded();
  const before2 = n.beats[1];
  const until = async (pred) => {
    for (let i = 0; i < 40; i++) {
      n = await narration();
      if (pred(n)) return true;
      await page.waitForTimeout(500);
    }
    return false;
  };
  let box = await bar().boundingBox();
  await page.mouse.move(box.x + 6, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + 40, box.y + box.height / 2, { steps: 5 });
  await page.mouse.move(box.x + 70, box.y + box.height / 2, { steps: 5 });
  await shot(page, 'timeline-lane-drag');
  await page.mouse.up();
  const dragged = await until((x) => x.beats[1].anchor.kind === 'clip' && x.beats[1].start > before2.start + 0.3);
  check('timeline: a dragged line is tied to the shot it lands on, saved in the background', dragged, `${n.beats[1].placement} (${before2.start} → ${n.beats[1].start})`);
  const lineSave = await api(page, 'revision.show', { id: await head() });
  check('timeline: the line move is saved as a timeline edit', lineSave.summary.origin === 'timeline:studio', lineSave.summary.origin);
  await page.keyboard.press(`${process.platform === 'darwin' ? 'Meta' : 'Control'}+z`);
  const undone = await until((x) => Math.abs(x.beats[1].start - before2.start) < 0.05);
  check('timeline: undo puts the line back', undone, `${n.beats[1].placement} (${n.beats[1].start})`);
  await bar().focus();
  await page.keyboard.press('Shift+ArrowRight');
  const nudged = await until((x) => x.beats[1].start > before2.start + 0.8);
  check('timeline: the arrow keys move a line too', nudged, n.beats[1].placement);
  box = await bar().boundingBox();
  await page.keyboard.down('Alt');
  await page.mouse.move(box.x + 6, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + 30, box.y + box.height / 2, { steps: 4 });
  await page.mouse.up();
  await page.keyboard.up('Alt');
  const pinned = await until((x) => x.beats[1].anchor.kind === 'time');
  check('timeline: ⌥-drag pins the line to the clock', pinned, n.beats[1].placement);
  await page.goto(`${studio}?tab=voiceover`);
  await lines.waitFor();

  // 5. Placement in words: line 1 starts when someone says a word.
  const card1 = lines.getByRole('listitem', { name: 'Line 1' });
  await card1.getByRole('button', { name: 'Change' }).click();
  const placer = card1.getByRole('form', { name: 'Where line 1 starts' });
  await placer.getByRole('radio', { name: 'When someone says…' }).click();
  const words = placer.getByRole('listbox', { name: 'Words in this shot' });
  await words.waitFor({ timeout: 15_000 });
  await words.getByRole('option').nth(3).click();
  await shot(page, 'placement-moment');
  await placer.getByRole('button', { name: 'Save start point' }).click();
  await toast(page, 'Start point saved');
  n = await narration();
  check('line placed at a spoken moment', n.beats[0].anchor.kind === 'moment' && /says/.test(n.beats[0].placement), n.beats[0].placement);

  // 6. Ask Claude: the panel opens with the request in its input (nothing is sent).
  await page.getByRole('button', { name: 'Add voiceover' }).click();
  await page.getByRole('dialog', { name: 'Add voiceover' }).getByRole('button', { name: /Ask Claude to suggest one/ }).click();
  let asked = false;
  for (let i = 0; i < 30 && !asked; i++) {
    asked = await page.evaluate(() => [...document.querySelectorAll('textarea')].some((t) => /Suggest a voiceover script/.test(t.value)));
    if (!asked) await page.waitForTimeout(300);
  }
  check('Ask Claude opens the panel with the request ready', asked);
  await page.keyboard.press('Escape');
  await page.goto(`${studio}?tab=voiceover`);
  await lines.waitFor();

  // 7. Record lines 1 and 2 (line 1 twice: a retake keeps both takes).
  await page.getByRole('button', { name: 'Record narration' }).click();
  const rec = page.getByRole('dialog', { name: 'Record narration' });
  await rec.getByRole('meter', { name: 'Microphone level' }).waitFor({ timeout: 20_000 });
  // Live takes are the default (scripts/voiceover-live-e2e.sh tests them); this run keeps the second mode working.
  check('the recorder opens in live mode', (await rec.getByRole('radio', { name: /^Live/ }).getAttribute('aria-checked')) === 'true');
  await rec.getByRole('radio', { name: 'One line at a time' }).click();
  const recordTake = async (secs) => {
    await rec.getByRole('button', { name: /^(Record line|Try again)/ }).click();
    await rec.getByRole('status', { name: /Recording in/ }).waitFor();
    await rec.getByRole('button', { name: /^Stop/ }).waitFor({ timeout: 10_000 });
    await page.waitForTimeout(secs * 1000);
    await rec.getByRole('button', { name: /^Stop/ }).click();
    await rec.getByRole('button', { name: /^Keep it/ }).waitFor();
  };
  await recordTake(3.2);
  await shot(page, 'recorder-review');
  await recordTake(2.6); // a retake
  await rec.getByRole('list', { name: 'Takes recorded now' }).getByText('Saved').first().waitFor({ timeout: 30_000 });
  check('the retake keeps both takes', (await rec.getByRole('list', { name: 'Takes recorded now' }).getByRole('listitem').count()) === 2);
  await rec.getByRole('button', { name: /^Keep it/ }).click();
  await rec.getByText(/^Line 2/).waitFor();
  await recordTake(3);
  await shot(page, 'recorder-line-2');
  await rec.getByRole('button', { name: /^Keep it/ }).click();
  await rec.getByRole('button', { name: 'Close the recorder' }).click();
  await rec.waitFor({ state: 'detached', timeout: 60_000 });
  n = await narration();
  check('recorded takes are on lines 1 and 2', n.beats[0].take?.source === 'recorded' && n.beats[1].take?.source === 'recorded' && n.beats[0].takeCount === 2, JSON.stringify(n.beats.map((b) => b.takeLabel)));
  // Right after recording, the same page scrolled: the preview never covers the lines, here and in a
  // smaller window (where it once stayed pinned over them).
  const afterRec = [];
  for (const vp of [{ width: 1440, height: 900 }, LAPTOP]) {
    await page.setViewportSize(vp);
    const o = await previewOverlap(page);
    const col = await previewColumn(page, false);
    afterRec.push({ ok: o.area === 0 && col.ok, text: `${vp.width} px: ${overlapText(o, 'lines')}; ${col.how}` });
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.evaluate(() => window.scrollTo(0, 0));
  check('right after recording, scrolling never puts the preview over the lines', afterRec.every((r) => r.ok), afterRec.map((r) => r.text).join(' | '));

  // 8. Takes A/B: the other take of line 1, then clean-up and back to the original.
  const takesFor1 = n.beats[0].take?.takeId;
  await lines.getByRole('listitem', { name: 'Line 1' }).getByRole('button', { name: 'More for line 1' }).click();
  await page.getByRole('menuitem', { name: 'Takes and clean-up' }).click();
  const tk = page.getByRole('dialog', { name: 'Takes for line 1' });
  await tk.getByRole('list', { name: 'Takes for line 1' }).waitFor();
  await tk.getByRole('button', { name: 'Play take 1' }).click();
  await tk.getByRole('button', { name: 'Stop take 1' }).waitFor({ timeout: 10_000 });
  await tk.getByRole('button', { name: 'Use this take' }).click();
  await toast(page, 'now plays take');
  n = await narration();
  check('A/B: the other take now plays', n.beats[0].take?.takeId && n.beats[0].take.takeId !== takesFor1);
  await tk.getByRole('button', { name: 'Clean up the sound' }).click();
  await toast(page, 'Cleaned up');
  // The tab says how it went; the bell keeps the notice (read) instead of saying it again.
  await page.waitForTimeout(2500);
  check('one message for work the tab ran (no second notice from the bell)', (await page.getByText(/^Narration cleaned up/).count()) === 0);
  await tk.getByRole('button', { name: 'Play the cleaned up version' }).waitFor({ timeout: 30_000 });
  await shot(page, 'takes-ab');
  await tk.getByRole('button', { name: 'Use the original version' }).click();
  await toast(page, 'plays the original recording');
  n = await narration();
  check('A/B: original and cleaned-up versions switch', !!n.beats[0].take?.processed && !n.beats[0].take?.useProcessed);
  await page.keyboard.press('Escape');

  // 9. The mixer: music 6 dB quieter is heard in the preview's plan at once, then saved.
  const planBefore = await api(page, 'revision.plan', { id: await head() });
  // An element's gain at one of its own frames (keyframes interpolate linearly, as in Remotion).
  const gainAt = (g, f) => {
    if (!g?.length) return 1;
    if (f <= g[0].f) return g[0].g;
    for (let i = 1; i < g.length; i++) if (f <= g[i].f) return g[i - 1].g + ((g[i].g - g[i - 1].g) * (f - g[i - 1].f)) / Math.max(1, g[i].f - g[i - 1].f);
    return g[g.length - 1].g;
  };
  // Music at its frame 30; narration in the middle of its first element.
  const env = (plan, pred) => {
    const a = plan.audio.find(pred);
    if (!a) return null;
    return gainAt(a.gain, a.role === 'music' ? 30 : Math.floor(a.durationInFrames / 2));
  };
  // The mixer moved to its own Sound tab (the Voiceover tab links to it).
  await page.getByRole('button', { name: 'Sound tab' }).click();
  await page.waitForURL((u) => u.searchParams.get('tab') === 'sound');
  const music = page.getByRole('slider', { name: 'Music', exact: true });
  await music.waitFor();
  const previews = [];
  page.on('response', async (r) => {
    if (r.url().endsWith('/api/ops/sound.preview')) previews.push(await r.json().catch(() => null));
  });
  await music.focus();
  for (let i = 0; i < 12; i++) await page.keyboard.press('ArrowLeft'); // 0.5 dB steps → −6 dB
  await page.getByText('You are hearing your changes in the preview').waitFor();
  await page.getByText('Playing your sound changes (not saved yet)').waitFor();
  for (let i = 0; i < 30 && !previews.at(-1)?.data; i++) await page.waitForTimeout(200);
  const pv = previews.at(-1)?.data?.plan;
  const ratio = pv ? env(pv, (a) => a.role === 'music') / env(planBefore, (a) => a.role === 'music') : NaN;
  const narrRatio = pv ? (env(pv, (a) => a.role === 'voiceover') ?? 1) / (env(planBefore, (a) => a.role === 'voiceover') ?? 1) : NaN;
  check('mixer change heard in the preview plan (music 6 dB down against the narration)', Math.abs(20 * Math.log10(ratio / narrRatio) + 6) < 0.6, `${(20 * Math.log10(ratio / narrRatio)).toFixed(2)} dB`);
  await page.getByRole('switch', { name: 'Music gets quieter when someone talks' }).waitFor();
  await shot(page, 'mixer');
  await page.getByRole('button', { name: 'Save sound' }).click();
  await toast(page, 'Sound saved');
  const saved = await api(page, 'revision.show', { id: await head() });
  const planAfter = await api(page, 'revision.plan', { id: await head() });
  check('mixer saved through sound.levels', saved.spec.mix?.music?.gainDb === -6, JSON.stringify(saved.spec.mix?.music));
  check('saved mix plays like its preview', Math.abs(env(planAfter, (a) => a.role === 'music') - env(pv, (a) => a.role === 'music')) < 1e-4);

  // 10. AI voice: without a key a plain link to Settings; with the (fake) key, price first, then make.
  await page.getByRole('tab', { name: 'Voiceover', exact: true }).click();
  await page.getByRole('tab', { name: /AI voice/ }).click();
  await page.getByRole('link', { name: /Open Settings → Integrations/ }).waitFor();
  check('without a key: a plain link to Settings → Integrations', true);
  await shot(page, 'ai-no-key');
  // The person adds the key on this computer (here: the fake API's key, checked against it).
  execSync(`${process.env.METARUSH_MINT.replace(/ dashboard .*$/, '')} providers set elevenlabs --key-stdin --allow-unsafe-secret-path < "${process.env.VO_KEY_FILE}"`, {
    cwd: repo,
    stdio: 'ignore',
    env: { ...process.env, METARUSH_ELEVENLABS_BASE_URL: process.env.VO_FAKE_URL },
  });
  await page.reload();
  await page.getByRole('tab', { name: /AI voice/ }).click();
  const voicesList = page.getByRole('list', { name: 'Voices to choose from' });
  await voicesList.waitFor({ timeout: 20_000 });
  await voicesList.getByRole('listitem').filter({ hasText: 'Ada' }).getByRole('button', { name: 'Play a sample of Ada' }).click();
  await voicesList.getByText('Playing sample').waitFor({ timeout: 10_000 });
  await voicesList.getByRole('listitem').filter({ hasText: 'Ada' }).getByRole('button', { name: 'Use this voice' }).click();
  await toast(page, 'now uses Ada');
  // The price is an estimate in dollars that opens to each line's voice, model, characters and credits (D208).
  const make = page.getByRole('button', { name: /^Make the AI voice for 1 line \((about \$\d+\.\d\d|less than a cent)\)$/ });
  await make.waitFor({ timeout: 20_000 });
  check('the price shows before anything is made, as an estimate', /\((about \$\d+\.\d\d|less than a cent)\)/.test(await make.innerText()));
  const priceBox = page.locator('section[aria-labelledby="ai-price-h"]');
  await priceBox.locator('[data-cost-toggle]').click();
  const voiceParts = priceBox.locator('[data-cost-part]');
  await voiceParts.first().waitFor();
  const voicePartsText = (await priceBox.locator('[data-cost-parts]').textContent()) ?? '';
  check(
    'the estimate opens to the line, the voice and model, characters and credits',
    (await voiceParts.count()) === 1 && /ElevenLabs/.test(voicePartsText) && /Ada/.test(voicePartsText) && /Line 3: “/.test(voicePartsText) && /characters/.test(voicePartsText) && /per 1,000 credits/.test(voicePartsText),
    voicePartsText.replace(/\s+/g, ' ').slice(0, 240),
  );
  await shot(page, 'ai-priced');
  await make.click();
  await toast(page, 'Generated 1 line with ElevenLabs');
  n = await narration();
  check('AI voice made for the line without sound', n.beats[2].take?.source === 'tts' && n.beats[2].aiVoice);
  await page.getByRole('list', { name: 'AI voice, line by line' }).getByRole('listitem').filter({ hasText: 'And by noon' }).getByRole('button', { name: 'Make again' }).click();
  const again = page.getByRole('dialog', { name: /Make line 3 again/ });
  await again.waitFor();
  check(
    'making a line again says what it costs, as an estimate that is open',
    /costs (about \$\d+\.\d\d|less than a cent) \(\d+ credits\)/.test(await again.innerText()) && (await again.locator('[data-cost-part]').count()) === 1,
    (await again.innerText()).replace(/\s+/g, ' ').slice(0, 200),
  );
  await shot(page, 'ai-make-again-price', again);
  await again.getByRole('button', { name: 'Make it again' }).click();
  // The first make's toast can still be showing, so wait for the second take itself.
  let aiTakes = 0;
  for (let i = 0; i < 60 && aiTakes < 2; i++) {
    const takes3 = await api(page, 'voiceover.takes', { revisionId: await head(), beat: n.beats[2].id });
    aiTakes = takes3.takes.filter((t) => t.source === 'tts').length;
    if (aiTakes < 2) await page.waitForTimeout(500);
  }
  check('a remade line keeps its old take too', aiTakes === 2, `${aiTakes} AI takes`);
  // Estimates off (Settings → Preferences, D208): making a line again doesn't ask about the price first.
  await api(page, 'account.preferences.set', { estimates: false });
  await page.reload();
  await page.getByRole('tab', { name: /AI voice/ }).click();
  await page.getByRole('list', { name: 'AI voice, line by line' }).getByRole('listitem').filter({ hasText: 'And by noon' }).getByRole('button', { name: 'Make again' }).click();
  for (let i = 0; i < 60 && aiTakes < 3; i++) {
    const takes3 = await api(page, 'voiceover.takes', { revisionId: await head(), beat: n.beats[2].id });
    aiTakes = takes3.takes.filter((t) => t.source === 'tts').length;
    if (aiTakes < 3) await page.waitForTimeout(500);
  }
  check('estimates off: a line is made again without the price question', aiTakes === 3 && (await page.getByRole('dialog', { name: /Make line 3 again/ }).count()) === 0, `${aiTakes} AI takes`);
  await api(page, 'account.preferences.set', { estimates: true });
  check('estimates back on', (await api(page, 'account.preferences')).estimates === true);
  // The voice model, in plain words, for this project.
  await page.getByText('How it sounds', { exact: true }).click();
  await page.getByLabel('Voice model').selectOption({ label: 'Eleven Flash v2.5' });
  await page.getByText(/credits? per character/).first().waitFor();
  await shot(page, 'ai-how-it-sounds');
  // Saved into the project's Content Settings (D193), not a per-tab setting.
  await page.locator('section', { hasText: 'How it sounds' }).getByRole('button', { name: 'Save', exact: true }).first().click();
  await toast(page, 'Voice settings saved');
  const pst = await api(page, 'provider.status', { name: 'elevenlabs', projectId: p.id });
  check('the voice model is chosen in the project\'s Content Settings', pst.effective.modelId === 'eleven_flash_v2_5' && pst.effective.from.model === 'settings', `${pst.effective.modelName} from ${pst.effective.from.model}`);

  // 11. Import: a recording the person has, matched to the lines.
  await page.getByRole('button', { name: 'Add voiceover' }).click();
  const chooser2 = page.getByRole('dialog', { name: 'Add voiceover' });
  await chooser2.getByLabel(/Use a recording I have/).setInputFiles(process.env.VO_IMPORT_FILE);
  const flow = page.getByRole('region', { name: 'Adding your recording' });
  await flow.waitFor();
  await shot(page, 'import-progress');
  await flow.getByRole('button', { name: 'Done' }).waitFor({ timeout: 240_000 });
  n = await narration();
  check('import: the recording is placed on the lines', n.beats.filter((b) => b.take?.source === 'imported').length >= 2, JSON.stringify(n.beats.map((b) => b.takeLabel)));
  await flow.getByRole('button', { name: 'Done' }).click();

  // 12. A refused microphone is explained in plain words.
  await page.evaluate(() => {
    window.__voDenyMic = true;
  });
  if (flagsMic) await ctx.clearPermissions();
  await page.getByRole('button', { name: 'Record narration' }).click();
  const help = page.getByRole('dialog', { name: 'Record narration' }).getByRole('alert');
  await help.waitFor({ timeout: 15_000 });
  check('denied microphone: plain help', /not allowed to use your microphone/.test(await help.innerText()));
  await shot(page, 'mic-denied');
  await help.getByRole('button', { name: 'Close' }).click();
  await page.evaluate(() => {
    window.__voDenyMic = false;
  });

  // 13. Screenshots on desktop and phone, dark and light. The finished uploads
  // (the takes and the imported file) are cleared first, as the tray does by
  // itself a minute later, so the shots show the tab itself.
  const tray = page.getByRole('region', { name: 'Uploads' });
  if (await tray.count()) await tray.getByRole('button', { name: 'Clear', exact: true }).click().catch(() => undefined);
  const state = await ctx.storageState();
  // On a phone the panel sits below the player: bring its tabs to the top first.
  const toPanel = async (p, mobile) => {
    if (!mobile) return;
    await p.getByRole('tab', { name: /Lines/ }).evaluate((el) => {
      el.scrollIntoView({ block: 'start' });
      window.scrollBy(0, -130);
    });
  };
  // The Sound tab has no section tabs of its own: bring the mixer's first part to the top.
  const toMixer = async (p, mobile) => {
    if (!mobile) return;
    await p.locator('#mix-levels-h').evaluate((el) => {
      el.scrollIntoView({ block: 'start' });
      window.scrollBy(0, -130);
    });
  };
  for (const theme of ['dark', 'light']) {
    for (const mobile of [false, true]) {
      const tag = `${mobile ? 'phone' : 'desktop'}-${theme}`;
      const v = await newPage(browser, { theme, mobile, standIn: !flagsMic, state });
      await v.page.goto(`${studio}?tab=voiceover`);
      await v.page.getByRole('list', { name: 'Narration lines' }).waitFor({ timeout: 20_000 });
      await toPanel(v.page, mobile);
      await shot(v.page, `lines-${tag}`);
      await v.page.getByRole('tab', { name: 'Sound', exact: true }).click();
      await v.page.getByRole('slider', { name: 'Music', exact: true }).waitFor();
      await toMixer(v.page, mobile);
      await shot(v.page, `mixer-${tag}`);
      await v.page.getByRole('tab', { name: 'Voiceover', exact: true }).click();
      await v.page.getByRole('list', { name: 'Narration lines' }).waitFor({ timeout: 20_000 });
      await v.page.getByRole('tab', { name: /AI voice/ }).click();
      await v.page.getByText('What it costs').waitFor();
      await toPanel(v.page, mobile);
      await shot(v.page, `ai-${tag}`);
      await v.page.getByRole('button', { name: 'Record narration' }).click();
      await v.page.getByRole('dialog', { name: 'Record narration' }).getByRole('meter').waitFor({ timeout: 20_000 });
      await shot(v.page, `recorder-${tag}`);
      await v.page.getByRole('button', { name: 'Close the recorder' }).click();
      await v.page.goto(`${studio}?tab=cut`);
      const vlane = v.page.locator('[data-narration-lane]');
      await vlane.getByRole('listitem').first().waitFor({ timeout: 20_000 });
      await v.page.locator('[data-timeline-editor]').evaluate((el) => {
        el.scrollIntoView({ block: 'center' });
      });
      await shot(v.page, `timeline-${tag}`);
      await v.page.getByRole('tab', { name: 'Review' }).click();
      await v.page.getByRole('button', { name: 'Add voiceover' }).click();
      await v.page.getByRole('dialog', { name: 'Add voiceover' }).waitFor();
      await shot(v.page, `chooser-${tag}`);
      await v.ctx.close();
    }
  }

  // 14. Scrolling never puts the preview over the script cards or the mixer: on a desktop (side by
  //     side, the preview stays in view in its own column), a smaller desktop window (the landscape
  //     cut stacks the preview above the panel: it scrolls away) and a phone, dark and light. The
  //     shots show the bottom of each panel.
  for (const theme of ['dark', 'light']) {
    for (const size of ['desktop', 'laptop', 'phone']) {
      const tag = `${size}-${theme}`;
      const mobile = size === 'phone';
      const v = await newPage(browser, { theme, mobile, viewport: size === 'laptop' ? LAPTOP : undefined, standIn: !flagsMic, state });
      await v.page.goto(`${studio}?tab=voiceover`);
      await v.page.getByRole('list', { name: 'Narration lines' }).waitFor({ timeout: 20_000 });
      const overLines = await previewOverlap(v.page);
      await shot(v.page, `scrolled-lines-${tag}`);
      await v.page.getByRole('tab', { name: 'Sound', exact: true }).click();
      await v.page.getByRole('slider', { name: 'Music', exact: true }).waitFor();
      const overMixer = await previewOverlap(v.page);
      await shot(v.page, `scrolled-mixer-${tag}`);
      const col = await previewColumn(v.page, mobile);
      check(
        `${tag}: scrolling never puts the preview over the script cards or the mixer`,
        overLines.area === 0 && overMixer.area === 0 && col.ok,
        `${overlapText(overLines, 'script cards')}, ${overlapText(overMixer, 'mixer')}; the preview ${col.how}`,
      );
      await v.ctx.close();
    }
  }

  // 15. While the activity strip shows (here: a file being processed, put into the activity answer),
  //     the top bar is taller. The section tabs stick below its real height, fully visible, and the
  //     preview sticks below the tabs: on a desktop and a phone.
  for (const size of ['desktop', 'phone']) {
    const mobile = size === 'phone';
    const v = await newPage(browser, { mobile, standIn: !flagsMic, state });
    await busyActivity(v.ctx);
    await v.page.goto(`${studio}?tab=voiceover`);
    await v.page.getByRole('list', { name: 'Narration lines' }).waitFor({ timeout: 20_000 });
    const t = await tabsUnderBar(v.page);
    await shot(v.page, `tabs-with-activity-${size}`);
    check(
      `${size}: with the activity strip showing, the section tabs stay fully visible below the top bar`,
      t.ok,
      `top bar ${t.bar}px tall (the strip ${t.strip ? 'shows' : 'does not show'}); tabs ${t.tabsTop}–${t.tabsBottom}px, ${t.covered.length ? `covered: ${t.covered.join(', ')}` : 'every tab uncovered'}${t.preview !== null ? `; the preview sticks at ${t.preview}px` : ''}`,
    );
    await v.ctx.close();
  }

  check('no native dialogs, page errors or horizontal overflow', problems.length === 0, problems.join(' | '));
  await browser.close();
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed; screenshots in ${shotsDir}`);
  if (failed.length) process.exitCode = 1;
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
