#!/usr/bin/env node
// Playing through a database outage (D180), in the dashboard, against a seeded
// throwaway installation (scripts/playback-e2e.sh does all of it). This run
// starts the dashboard behind a link to the test MongoDB that it can cut —
// live connections dropped mid-reply, new ones left unanswered, mongod never
// touched — and checks:
//   A  in an outage a clip MetaRush remembers streams from memory, and one it
//      doesn't is refused quickly in plain words;
//   B  the source viewer of a clip the server can't send says "MetaRush can't
//      reach its database right now; it keeps trying" (no endless "Buffering…"),
//      and plays once the database is back;
//   C  the cut preview stalled in the middle of the cut (the second clip's file
//      held back) says "Reconnecting…", and goes on from the same time once
//      the file comes;
//   D  a cut MetaRush got ready plays through an outage, the clip not reached
//      yet included;
//   E  the version page lists its review copy, with when it was made, and plays
//      it in its player, through an outage too (D187);
//   F  the Edit tab shows the latest version's review copy by default with a
//      switch to the live preview; a checked graphic (approved here as a person)
//      shows as its still in the live preview, labelled (desktop dark/light,
//      phone, no sideways scrolling);
// with screenshots. No native browser dialog may appear.
//
//   METARUSH_BIN=bin/metarush METARUSH_PLAYBACK_HOME=<home> METARUSH_DASHBOARD=http://127.0.0.1:5480 \
//   METARUSH_DB_URI=<test mongodb uri> METARUSH_DB_LINK_PORT=5482 PLAYBACK_PROJECT=prj_… PLAYBACK_REVISION=rev_… \
//   PLAYBACK_HARBOUR=ast_… PLAYBACK_KITCHEN=ast_… PLAYBACK_WAVES=ast_… PLAYBACK_REVIEW_COPY=rnd_… PLAYBACK_MOTION_HASH=<sha256> \
//   SHOTS_DIR=<dir> node web/scripts/playback-e2e.mjs
import { execFileSync, spawn } from 'node:child_process';
import { mkdirSync, openSync } from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { chromium } from 'playwright';

const env = process.env;
for (const k of ['METARUSH_BIN', 'METARUSH_PLAYBACK_HOME', 'METARUSH_DB_URI', 'PLAYBACK_PROJECT', 'PLAYBACK_REVISION', 'PLAYBACK_HARBOUR', 'PLAYBACK_KITCHEN', 'PLAYBACK_WAVES', 'PLAYBACK_REVIEW_COPY', 'PLAYBACK_MOTION_HASH']) {
  if (!env[k]) {
    console.error(`set ${k} (see the header of this script)`);
    process.exit(2);
  }
}
const base = (env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5480').replace(/\/$/, '');
const home = env.METARUSH_PLAYBACK_HOME;
const ids = { harbour: env.PLAYBACK_HARBOUR, kitchen: env.PLAYBACK_KITCHEN, waves: env.PLAYBACK_WAVES };
const SHOTS = env.SHOTS_DIR ?? '';
if (SHOTS) mkdirSync(SHOTS, { recursive: true });
const DATABASE_LINE = "MetaRush can't reach its database right now; it keeps trying.";

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function waitFor(fn, ms, every = 250) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > end) return null;
    await sleep(every);
  }
}

/* ---------- the database link ---------- */

// Between the dashboard and the test MongoDB: cut() drops every live connection
// and holds new ones without an answer; restore() lets them through again.
function dbLink(port, upstream) {
  let cut = false;
  const live = new Set();
  const held = new Set();
  const server = net.createServer((c) => {
    c.on('error', () => undefined);
    if (cut) {
      held.add(c);
      c.on('close', () => held.delete(c));
      return;
    }
    const up = net.connect(upstream.port, upstream.host);
    const pair = { c, up };
    live.add(pair);
    const end = () => {
      c.destroy();
      up.destroy();
      live.delete(pair);
    };
    up.on('error', end);
    c.on('close', end);
    up.on('close', end);
    c.pipe(up);
    up.pipe(c);
  });
  return {
    listen: () => new Promise((resolve, reject) => server.once('error', reject).listen(port, '127.0.0.1', resolve)),
    cut() {
      cut = true;
      for (const p of live) {
        p.c.destroy();
        p.up.destroy();
      }
      live.clear();
    },
    restore() {
      cut = false;
      for (const c of held) c.destroy();
      held.clear();
    },
    close() {
      this.restore();
      for (const p of live) {
        p.c.destroy();
        p.up.destroy();
      }
      server.close();
    },
  };
}

const real = new URL(env.METARUSH_DB_URI);
const linkPort = Number(env.METARUSH_DB_LINK_PORT ?? 5482);
const link = dbLink(linkPort, { host: real.hostname, port: Number(real.port || 27017) });
await link.listen();
const viaLink = new URL(env.METARUSH_DB_URI);
viaLink.host = `127.0.0.1:${linkPort}`;

/* ---------- the dashboard, behind the link ---------- */

const log = openSync(path.join(home, 'serve.log'), 'a');
const daemon = spawn(env.METARUSH_BIN, ['--home', home, 'serve', '--no-worker'], {
  env: { ...env, METARUSH_MONGODB_URI: viaLink.toString(), METARUSH_DEV_ORIGINS: '' },
  stdio: ['ignore', log, log],
});
const up = await waitFor(async () => (await fetch(`${base}/api/health`)).ok, 30_000);
if (!up) {
  console.error('the dashboard did not start; see serve.log in the home');
  daemon.kill();
  link.close();
  process.exit(1);
}

function mintLink() {
  // The terminal's own MetaRush reaches the database directly (the home's .env).
  const out = execFileSync(env.METARUSH_BIN, ['--home', home, 'dashboard', '--print-link', '--no-open'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return `${base}/auth${new URL(m[0]).search}`;
}

async function shot(page, name) {
  await sleep(300);
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: false });
}

/** One byte range of a media file, asked by the page itself (its cookie), with the time it took. */
async function mediaFetch(page, url, range) {
  return page.evaluate(
    async ([url, range]) => {
      const t0 = performance.now();
      const r = await fetch(url, { headers: { Range: range }, cache: 'no-store', credentials: 'same-origin' });
      const ms = Math.round(performance.now() - t0);
      const record = r.headers.get('X-MetaRush-Record');
      let error = null;
      if ((r.headers.get('Content-Type') ?? '').includes('application/json')) error = (await r.json()).error ?? null;
      else await r.arrayBuffer();
      return { status: r.status, record, ms, error };
    },
    [url, range],
  );
}

// Requests for a clip's files can be held back (a stalled connection) until released.
function holdable() {
  let holding = false;
  const waiting = [];
  return {
    get holding() {
      return holding;
    },
    hold() {
      holding = true;
    },
    release() {
      holding = false;
      waiting.splice(0).forEach((r) => r());
    },
    async route(route, onlyMedia) {
      if (holding && (!onlyMedia || route.request().resourceType() === 'media')) await new Promise((r) => waiting.push(r));
      await route.continue().catch(() => undefined); // a request the player gave up on meanwhile
    },
  };
}

const playerTime = (page) =>
  page.evaluate(() => {
    const label = [...document.querySelectorAll('[data-player-surface] div')].map((d) => d.textContent ?? '').find((t) => /^\d+:\d\d \/ \d+:\d\d$/.test(t.trim()));
    if (!label) return null;
    const [m, s] = label.trim().split(' / ')[0].split(':').map(Number);
    return m * 60 + s;
  });
const troubleText = (page) => page.evaluate(() => document.querySelector('[data-player-trouble]')?.textContent?.trim() ?? null);
const bufferingShown = (page) => page.evaluate(() => [...document.querySelectorAll('[role="status"]')].some((n) => /Buffering/.test(n.textContent ?? '')));
const videoTime = (page) => page.evaluate(() => document.querySelector('[data-player-surface] video')?.currentTime ?? null);

/** The version page plays its review copy by default (D187): switch to the live preview. */
async function liveVersionPreview(page) {
  const live = page.locator('[data-preview-switch]').getByRole('radio', { name: /^Live preview/ });
  if (await live.waitFor({ timeout: 15_000 }).then(() => true, () => false)) await live.click();
}

/** An op called from the page, as the dashboard calls it (a person's session). */
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

/** Another signed-in page, in a theme, on a desktop or a 390 px phone. */
async function themedPage(theme, phone) {
  const ctx = await browser.newContext({
    viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 940 },
    deviceScaleFactor: phone ? 2 : 1,
    hasTouch: phone,
    isMobile: phone,
  });
  await ctx.addInitScript((t) => {
    try {
      localStorage.setItem('metarush.theme', t);
    } catch {
      /* private mode */
    }
  }, theme);
  const p = await ctx.newPage();
  p.on('dialog', (d) => {
    check('no native browser dialog', false, `${d.type()}: ${d.message()}`);
    void d.dismiss();
  });
  await p.goto(mintLink());
  await p.waitForURL(`${base}/**`);
  return p;
}

const browser = await chromium.launch({ args: ['--autoplay-policy=no-user-gesture-required'] });
const pageErrors = [];
try {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 940 } });
  const page = await ctx.newPage();
  page.on('dialog', (d) => {
    check('no native browser dialog', false, `${d.type()}: ${d.message()}`);
    void d.dismiss();
  });
  page.on('pageerror', (e) => pageErrors.push(String(e?.message ?? e)));
  // The Inbox clip's files reach MetaRush only once the database is cut, so
  // MetaRush never saw that clip while its database answered (routing also
  // turns the browser's cache off: every request reaches the dashboard).
  let cutDone;
  const databaseCut = new Promise((r) => (cutDone = r));
  await page.route(`**/media/assets/${ids.waves}/**`, async (route) => {
    await databaseCut;
    await route.continue().catch(() => undefined);
  });
  const kitchen = holdable();
  await page.route(`**/media/assets/${ids.kitchen}/**`, (route) => kitchen.route(route, true));

  await page.goto(mintLink());
  await page.waitForURL(`${base}/**`);
  await page.goto(`${base}/library`);
  await page.getByRole('tablist', { name: 'Library views' }).waitFor();
  await waitFor(() => page.evaluate((id) => [...document.images].some((i) => i.src.includes(`/media/assets/${id}/thumbnail`) && i.complete && i.naturalWidth > 0), ids.harbour), 15_000);

  /* ---------- A: what MetaRush remembers streams; the rest is refused quickly ---------- */
  const proxy = (id) => `/media/assets/${id}/proxy`;
  const before = await mediaFetch(page, proxy(ids.harbour), 'bytes=0-99');
  check('A: with the database answering, the clip comes from it', before.status === 206 && before.record === null, JSON.stringify(before));
  link.cut();
  cutDone();
  const remembered = await mediaFetch(page, proxy(ids.harbour), 'bytes=100-199');
  check('A: in the outage a clip MetaRush remembers still streams, from memory', remembered.status === 206 && remembered.record === 'memory', JSON.stringify(remembered));
  const unknown = await mediaFetch(page, proxy('ast_neverseen000000000000'), 'bytes=0-99');
  check(
    'A: one it never saw is refused in plain words, quickly',
    unknown.status === 503 && unknown.error?.code === 'E_DATABASE' && /can't reach its database right now/.test(unknown.error?.message ?? '') && unknown.ms < 5000,
    `${unknown.status} ${unknown.error?.message} in ${unknown.ms} ms`,
  );

  /* ---------- B: the source viewer says so and plays once the database is back ---------- */
  await page.getByText('Waves at dawn', { exact: true }).first().click();
  const viewer = page.getByRole('dialog');
  await viewer.waitFor();
  const said = await waitFor(async () => (await troubleText(page)) === DATABASE_LINE, 15_000);
  check('B: the source viewer says the database is out of reach', !!said, String(await troubleText(page)));
  check('B: no endless "Buffering…"', !(await bufferingShown(page)));
  await viewer.getByRole('button', { name: 'Play (Space)' }).click();
  check('B: Play pressed meanwhile is kept for when it is back', (await viewer.getByRole('button', { name: 'Pause (Space)' }).count()) === 1);
  await shot(page, 'B-source-viewer-database');
  await sleep(2500);
  check('B: it keeps saying so while the database is out', (await troubleText(page)) === DATABASE_LINE, String(await troubleText(page)));
  link.restore();
  const played = await waitFor(
    () => page.evaluate(() => {
      const v = document.querySelector('[role="dialog"] video');
      return !!v && !v.paused && v.currentTime > 0.5 && !document.querySelector('[data-player-trouble]');
    }),
    45_000,
  );
  check('B: once the database is back the clip plays, and the line goes', !!played, `trouble: ${await troubleText(page)}`);
  await shot(page, 'B-source-viewer-playing');
  await page.keyboard.press('Escape');
  await viewer.waitFor({ state: 'hidden' }).catch(() => undefined);

  /* ---------- C: a stall in the middle of the cut ---------- */
  await page.goto(`${base}/p/${env.PLAYBACK_PROJECT}/r/${env.PLAYBACK_REVISION}`);
  await liveVersionPreview(page); // the version has a review copy (D187): C and D are about the live preview
  const play = page.getByRole('button', { name: 'Play video' }).first();
  await play.waitFor({ timeout: 30_000 });
  await waitFor(async () => !(await page.getByText(/Preparing preview/).count()), 30_000);
  kitchen.hold(); // the second clip's file doesn't come
  await page.locator('[data-player-surface]').first().hover();
  await play.click();
  const stalledAt = await waitFor(async () => {
    const t = await playerTime(page);
    return t !== null && t >= 2 ? t : null;
  }, 15_000);
  check('C: the preview plays up to the second clip', stalledAt !== null, `at ${stalledAt} s`);
  const reconnecting = await waitFor(async () => (await troubleText(page)) === 'Reconnecting…', 20_000);
  check('C: stalled, it says "Reconnecting…" instead of "Buffering…"', !!reconnecting && !(await bufferingShown(page)), String(await troubleText(page)));
  await shot(page, 'C-preview-reconnecting');
  const whileStalled = await playerTime(page);
  check('C: it stays where it stalled meanwhile', whileStalled !== null && whileStalled >= 2 && whileStalled <= 3, `at ${whileStalled} s`);
  kitchen.release();
  const resumed = await waitFor(async () => {
    const t = await playerTime(page);
    return t !== null && t >= 3 && !(await troubleText(page)) ? t : null;
  }, 30_000);
  check('C: once the file comes it goes on from the same time, not from the start', resumed !== null, `at ${resumed} s, trouble: ${await troubleText(page)}`);
  await shot(page, 'C-preview-resumed');

  /* ---------- D: a cut MetaRush got ready plays through an outage ---------- */
  await page.goto(`${base}/p/${env.PLAYBACK_PROJECT}/r/${env.PLAYBACK_REVISION}`);
  await liveVersionPreview(page);
  await page.getByRole('button', { name: 'Play video' }).first().waitFor({ timeout: 30_000 });
  await waitFor(async () => !(await page.getByText(/Preparing preview/).count()), 30_000);
  const answers = [];
  const onResponse = (r) => {
    if (r.url().includes('/media/assets/')) answers.push({ url: new URL(r.url()).pathname, status: r.status(), record: r.headers()['x-metarush-record'] ?? null });
  };
  page.on('response', onResponse);
  link.cut();
  await page.locator('[data-player-surface]').first().hover();
  await page.getByRole('button', { name: 'Play video' }).first().click();
  let troubled = null;
  const through = await waitFor(async () => {
    troubled = troubled ?? (await troubleText(page));
    const t = await playerTime(page);
    return t !== null && t >= 4 ? t : null;
  }, 25_000);
  check('D: the cut plays past its second clip with the database out', through !== null && !troubled, `at ${through} s, trouble: ${troubled}`);
  const second = answers.filter((a) => a.url.startsWith(`/media/assets/${ids.kitchen}/`));
  check(
    'D: the clip it hadn\'t reached came from what MetaRush remembered',
    second.length > 0 && second.every((a) => a.status < 300 && a.record === 'memory'),
    JSON.stringify(second.slice(0, 3)),
  );
  page.off('response', onResponse);
  await shot(page, 'D-preview-through-outage');
  link.restore();

  /* ---------- E: the version page lists its review copy and plays it (D187) ---------- */
  await page.unroute(`**/media/assets/${ids.kitchen}/**`);
  await page.goto(`${base}/p/${env.PLAYBACK_PROJECT}/r/${env.PLAYBACK_REVISION}`);
  const renders = page.locator('[data-version-renders]');
  await renders.waitFor({ timeout: 30_000 });
  const copyRow = ((await renders.locator(`[data-version-render="${env.PLAYBACK_REVIEW_COPY}"]`).textContent()) ?? '').trim();
  check('E: the version page lists its review copy, with when it was made', /^Review copy/.test(copyRow) && /Today \d{1,2}:\d\d/.test(copyRow), copyRow);
  const chosen = ((await page.locator('[data-preview-switch] [aria-checked="true"]').textContent()) ?? '').trim();
  check('E: its player plays the review copy by default, the switch says which and when', /^Review copy \(Today \d/.test(chosen), chosen);
  const startedCopy = await waitFor(
    () => page.evaluate(() => {
      const v = document.querySelector('[data-player-surface] video');
      if (!v) return false;
      if (v.paused) void v.play().catch(() => undefined);
      return v.currentTime > 0.6;
    }),
    20_000,
  );
  check('E: the review copy plays in the player (same-origin, from MetaRush)', !!startedCopy);
  link.cut();
  const copyAt = await videoTime(page);
  const onward = await waitFor(async () => {
    const t = await videoTime(page);
    return t !== null && copyAt !== null && (t >= copyAt + 0.8 || (await page.evaluate(() => !!document.querySelector('[data-player-surface] video')?.ended))) && !(await troubleText(page));
  }, 15_000);
  check('E: it plays on with the database out', !!onward, `from ${copyAt} s, trouble: ${await troubleText(page)}`);
  link.restore();
  await shot(page, 'E-version-renders-desktop-dark');

  /* ---------- F: the Edit tab: the review copy by default, the switch, stills of graphics ---------- */
  const editViews = async (p, tag) => {
    await p.goto(`${base}/p/${env.PLAYBACK_PROJECT}?tab=cut`);
    const sw = p.locator('[data-preview-switch]');
    await sw.waitFor({ timeout: 30_000 });
    check(`F ${tag}: the tab is called Edit`, (await p.getByRole('tab', { name: /^Edit/ }).getAttribute('aria-selected')) === 'true');
    const on = ((await sw.locator('[aria-checked="true"]').textContent()) ?? '').trim();
    check(`F ${tag}: the latest version's review copy is shown by default`, /^Review copy \(version 1, Today \d/.test(on), on);
    check(`F ${tag}: it plays the real video`, !!(await waitFor(() => p.evaluate(() => !!document.querySelector('[data-player-surface] video')), 10_000)));
    await shot(p, `F-edit-review-copy-${tag}`);
    if (tag.startsWith('phone')) {
      const scrollW = await p.evaluate(() => document.documentElement.scrollWidth);
      check(`F ${tag}: no sideways scrolling`, scrollW <= 392, `scrollWidth ${scrollW}`);
    }
    await sw.getByRole('radio', { name: /^Live preview/ }).click();
    check(`F ${tag}: Live preview switches to the cut drawn in the browser`, !!(await waitFor(async () => (await p.getByRole('button', { name: 'Play video' }).count()) > 0, 15_000)));
    // The version page of the same version, its renders listed.
    await p.goto(`${base}/p/${env.PLAYBACK_PROJECT}/r/${env.PLAYBACK_REVISION}`);
    await p.locator('[data-version-renders]').waitFor({ timeout: 20_000 });
    await p.locator('[data-version-renders]').scrollIntoViewIfNeeded();
    await shot(p, `E-version-renders-${tag}`);
  };
  await editViews(page, 'desktop-dark');
  for (const [theme, phone] of [['light', false], ['dark', true], ['light', true]]) {
    const p = await themedPage(theme, phone);
    await editViews(p, `${phone ? 'phone' : 'desktop'}-${theme}`);
    await p.context().close();
  }

  // A checked motion graphic, approved by a person (here, in the dashboard), in a new version.
  await api(page, 'motion.approve', { ref: env.PLAYBACK_MOTION_HASH, note: 'looked at the frames' });
  const plan = { projectId: env.PLAYBACK_PROJECT, baseRevisionId: env.PLAYBACK_REVISION, instruction: 'add the route',
    ops: [{ op: 'addOverlay', overlay: { id: 'route', motion: { name: 'route-map', hash: env.PLAYBACK_MOTION_HASH }, at: 0.5, duration: 3, params: { from: 'Lisbon', to: 'Porto' } } }] };
  const checked = await api(page, 'edit.check', { plan });
  check('F: a version with the approved graphic checks', !!checked.valid, JSON.stringify(checked.errors ?? []));
  await api(page, 'edit.apply', { plan, confirmToken: checked.confirmToken });
  const stillViews = async (p, tag) => {
    await p.goto(`${base}/p/${env.PLAYBACK_PROJECT}?tab=cut`);
    const playBtn = p.getByRole('button', { name: 'Play video' }).first();
    await playBtn.waitFor({ timeout: 30_000 });
    const older = ((await p.locator('[data-watch-older]').first().locator('xpath=..').textContent().catch(() => '')) ?? '').trim();
    check(`F ${tag}: the latest version has no review copy yet: live preview, and the older copy offered`, /review copy is of version 1, not the latest/.test(older), older);
    await waitFor(async () => !(await p.getByText(/Preparing preview/).count()), 30_000);
    await p.locator('[data-player-surface]').first().hover();
    await playBtn.click();
    await waitFor(async () => {
      const t = await playerTime(p);
      return t !== null && t >= 2;
    }, 15_000);
    await p.getByRole('button', { name: 'Pause video' }).first().click().catch(() => undefined);
    const still = await waitFor(
      () => p.evaluate((hash) => {
        const box = document.querySelector('[data-motion-still]');
        const img = box && [...box.querySelectorAll('img')].find((i) => i.style.display !== 'none');
        return img && img.complete && img.naturalWidth > 0 && img.src.includes(`/media/motion/${hash}/still-`) ? { label: box.textContent?.trim(), src: new URL(img.src).pathname } : null;
      }, env.PLAYBACK_MOTION_HASH),
      15_000,
    );
    check(`F ${tag}: the graphic shows as its still, in its place, labelled`, !!still && still.label === 'still — moves in renders', JSON.stringify(still));
    check(`F ${tag}: no placeholder box for a checked graphic`, (await p.getByText('shows in renders', { exact: true }).count()) === 0);
    await shot(p, `F-still-in-place-${tag}`);
  };
  await stillViews(page, 'desktop-dark');
  for (const [theme, phone] of [['light', false], ['dark', true], ['light', true]]) {
    const p = await themedPage(theme, phone);
    await stillViews(p, `${phone ? 'phone' : 'desktop'}-${theme}`);
    await p.context().close();
  }

  // The daemon queues the latest version's review copy by itself once the cut
  // has been left alone for a minute; the Edit tab says it is on its way (and,
  // with no machine that renders online here, why it waits).
  await page.goto(`${base}/p/${env.PLAYBACK_PROJECT}?tab=cut`);
  const making = await waitFor(async () => ((await page.locator('[data-copy-making]').first().textContent().catch(() => '')) ?? '').trim() || null, 120_000, 2000);
  check('F: about a minute after the last change, a review copy of the latest version is on its way by itself, and the Edit tab says so', /^Making a review copy of version 2…/.test(making ?? ''), making);
  await shot(page, 'F-making-review-copy-desktop-dark');
  const follow = await api(page, 'render.follow.show', {});
  check('F: render.follow.show lists it', follow.on && follow.making.length === 1 && follow.making[0].revisionNumber === 2 && follow.making[0].follow, JSON.stringify(follow.making.map((r) => [r.revisionNumber, r.state])));
} catch (e) {
  check('the run finished', false, String(e?.stack ?? e));
} finally {
  await browser.close();
  daemon.kill('SIGTERM');
  link.close();
}
const unexpected = pageErrors.filter((m) => !/MediaPlaybackError|The browser threw an error while playing the video/.test(m));
if (unexpected.length) console.log(`page errors: ${unexpected.join(' | ')}`);
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed${SHOTS ? `; screenshots in ${SHOTS}` : ''}`);
process.exit(failed.length ? 1 : 0);
