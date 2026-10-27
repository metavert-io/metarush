#!/usr/bin/env node
// Drives the studio timeline (project → Edit) end to end in a real
// browser: reorder, add footage from the sources, remove (drag off), trim,
// roll, split, delete a marked stretch, undo/redo across saves, the transition
// picker, a keyboard-only flow, a conflict with another machine (Keep mine),
// editing by transcript (select words and delete them, Remove ums and
// restarts, cutting words out from the footage's transcript) and a phone
// layout. Every change is checked against the server (the saved
// version's cut), and screenshots (desktop and phone, dark and light) go to
// web/.screenshots/timeline/.
//
// It CHANGES the project (it saves many versions), so point it only at a
// throwaway installation. There is deliberately no default URL.
//
//   METARUSH_DASHBOARD=http://127.0.0.1:4922 \
//   METARUSH_MINT="./bin/metarush --home .agent-home dashboard --print-link --no-open" \
//   PROJECT="Builder story" node web/scripts/timeline-e2e.mjs
//
// The project needs a cut with at least four clips and footage with proxies.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const outDir = path.join(here, '..', '.screenshots', 'timeline');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
if (!base) {
  console.error('Set METARUSH_DASHBOARD to a throwaway dashboard (this script saves versions).');
  process.exit(2);
}
const only = process.env.STEPS ? new Set(process.env.STEPS.split(',')) : null;

function mintLink() {
  if (process.env.METARUSH_SIGNIN_URL) return process.env.METARUSH_SIGNIN_URL;
  const cmd = process.env.METARUSH_MINT;
  if (!cmd) throw new Error('set METARUSH_SIGNIN_URL or METARUSH_MINT');
  const out = execSync(cmd, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in the output of: ${cmd}\n${out}`);
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

const problems = [];
let step = 0;
const passed = [];

function check(ok, what) {
  if (!ok) throw new Error(`check failed: ${what}`);
  console.log(`  ok  ${what}`);
  passed.push(what);
}

async function shot(page, name, opts = {}) {
  mkdirSync(outDir, { recursive: true });
  const file = path.join(outDir, `${String(++step).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file, fullPage: opts.full ?? false });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal page overflow of ${overflow}px on ${name}`);
}

async function context(browser, state, theme, mobile) {
  const ctx = await browser.newContext({
    viewport: mobile ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: mobile ? 2 : 1,
    storageState: state,
    colorScheme: theme,
    hasTouch: mobile,
    isMobile: mobile,
  });
  await ctx.addInitScript((t) => {
    try {
      localStorage.setItem('metarush.theme', t);
      for (const k of Object.keys(localStorage)) if (k.startsWith('metarush.timeline.draft.')) localStorage.removeItem(k);
    } catch {}
  }, theme);
  const page = await ctx.newPage();
  page.on('console', (m) => m.type() === 'error' && !/Failed to load resource/.test(m.text()) && problems.push(`console.error: ${m.text()}`));
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  return { ctx, page };
}

/** The latest saved version: number and spec. */
async function head(page, projectId) {
  const p = await api(page, 'project.show', { id: projectId });
  const r = await api(page, 'revision.show', { id: p.headRevisionId });
  return { id: r.summary.id, number: r.summary.number, spec: r.spec, summary: r.summary, duration: r.summary.duration };
}

/** Waits until a version newer than n is saved (the timeline saves on its own). */
async function newerThan(page, projectId, n, timeout = 20_000, timeline = true) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeout) {
    const h = await head(page, projectId);
    if (h.number > n) {
      if (!timeline) return h;
      await page
        .locator('[data-save-state="saved"]')
        .waitFor({ timeout: 10_000 })
        .catch(() => {});
      return h;
    }
    await page.waitForTimeout(250);
  }
  throw new Error(
    `no version after r${n} within ${timeout} ms (save state: ${await page
      .locator('[data-save-state]')
      .getAttribute('data-save-state')
      .catch(() => '?')})`,
  );
}

const order = (spec) => spec.clips.map((c) => c.id).join(',');
const box = async (page, sel) => {
  await page
    .locator(sel)
    .first()
    .scrollIntoViewIfNeeded()
    .catch(() => {});
  const b = await page.locator(sel).first().boundingBox();
  if (!b) throw new Error(`no box for ${sel}`);
  return b;
};

async function dragMouse(page, from, to, opts = {}) {
  if (opts.shift) await page.keyboard.down('Shift');
  if (opts.alt) await page.keyboard.down('Alt');
  await page.mouse.move(from.x, from.y);
  await page.mouse.down();
  const steps = opts.steps ?? 12;
  for (let i = 1; i <= steps; i++) await page.mouse.move(from.x + ((to.x - from.x) * i) / steps, from.y + ((to.y - from.y) * i) / steps);
  if (opts.beforeUp) await opts.beforeUp();
  await page.mouse.up();
  if (opts.alt) await page.keyboard.up('Alt');
  if (opts.shift) await page.keyboard.up('Shift');
}

async function openEditor(page, projectId) {
  await page.goto(`${base}/p/${projectId}`);
  await page.locator('[data-timeline-editor] [data-clip]').first().waitFor({ timeout: 30_000 });
  await page.waitForTimeout(1200);
  await page.locator('[data-timeline-editor]').scrollIntoViewIfNeeded();
}

const want = (name) => !only || only.has(name);

async function main() {
  mkdirSync(outDir, { recursive: true });
  const browser = await chromium.launch();
  const auth = await browser.newContext();
  const ap = await auth.newPage();
  await ap.goto(mintLink());
  await ap.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
  const state = await auth.storageState();
  const projects = (await api(ap, 'project.list')).projects;
  const wanted = process.env.PROJECT;
  const project = projects.find((p) => !wanted || p.id === wanted || p.name.toLowerCase() === wanted.toLowerCase());
  if (!project) throw new Error('no project found');
  const P = project.id;

  const { ctx, page } = await context(browser, state, 'dark', false);
  await openEditor(page, P);
  let h = await head(page, P);
  check(h.spec.clips.length >= 4, `the cut has ${h.spec.clips.length} clips to edit`);
  await shot(page, 'editor-dark', { full: true });

  // 1. Reorder: drag the last clip to the start.
  if (want('reorder')) {
    const ids = h.spec.clips.map((c) => c.id);
    const last = ids[ids.length - 1];
    const lb = await box(page, `[data-clip="${last}"]`);
    const fb = await box(page, `[data-clip="${ids[0]}"]`);
    await dragMouse(
      page,
      { x: lb.x + lb.width / 2, y: lb.y + lb.height / 2 },
      { x: fb.x + lb.width / 2 - 12, y: fb.y + fb.height / 2 },
      {
        beforeUp: async () => shot(page, 'reorder-dragging'),
      },
    );
    const r = await newerThan(page, P, h.number);
    check(r.spec.clips[0].id === last, `reorder: ${last} is now first (${order(r.spec)})`);
    check(r.summary.origin === 'timeline:studio' && /^Moved 1 clip/.test(r.summary.instruction ?? ''), `saved from the timeline with a plain summary (“${r.summary.instruction}”)`);
    h = r;
    // Undo walks back through the save, redo forward again.
    await page.locator('[data-timeline-editor]').click({ position: { x: 5, y: 5 } });
    await page.keyboard.press('Meta+z');
    const u = await newerThan(page, P, h.number);
    check(u.spec.clips.map((c) => c.id).join(',') === ids.join(','), 'undo (⌘Z) after the save puts the clip back');
    await page.keyboard.press('Meta+Shift+z');
    const rd = await newerThan(page, P, u.number);
    check(rd.spec.clips[0].id === last, 'redo (⇧⌘Z) moves it again');
    h = rd;
  }

  // 2. Trim: drag the end of the second clip 1 s to the left (snapping off).
  if (want('trim')) {
    const c = h.spec.clips[1];
    const hb = await box(page, `[data-clip-edge="${c.id}"][data-edge="end"]`);
    const pps = await page.evaluate(() => {
      const ticks = [...document.querySelectorAll('[data-ruler] > div')].map((d) => d.getBoundingClientRect().left);
      return ticks.length > 1 ? ticks[1] - ticks[0] : 50;
    });
    const tickStep = await page.evaluate(() => {
      const labels = [...document.querySelectorAll('[data-ruler] span')].map((s) => s.textContent);
      const parse = (t) => t.split(':').reduce((a, v) => a * 60 + Number(v), 0);
      return labels.length > 1 ? parse(labels[1]) - parse(labels[0]) : 1;
    });
    const pxPerSec = pps / tickStep;
    await page.locator(`[data-clip="${c.id}"]`).click();
    const hb2 = await box(page, `[data-clip-edge="${c.id}"][data-edge="end"]`);
    void hb;
    await dragMouse(
      page,
      { x: hb2.x + hb2.width / 2, y: hb2.y + hb2.height / 2 },
      { x: hb2.x + hb2.width / 2 - pxPerSec, y: hb2.y + hb2.height / 2 },
      {
        shift: true,
        beforeUp: async () => shot(page, 'trim-dragging'),
      },
    );
    const r = await newerThan(page, P, h.number);
    const nc = r.spec.clips.find((x) => x.id === c.id);
    check(nc && nc.out < c.out - 0.7 && nc.out > c.out - 1.3, `trim: ${c.id} ends ${(c.out - nc.out).toFixed(2)} s earlier`);
    check(r.duration < h.duration - 0.7, `ripple: the cut got shorter (${h.duration.toFixed(2)} → ${r.duration.toFixed(2)} s)`);
    h = r;
  }

  // 3. Roll: ⌥-drag the shared edge between clips 2 and 3; the cut keeps its length.
  if (want('roll')) {
    const a = h.spec.clips[1];
    const b = h.spec.clips[2];
    await page.locator(`[data-clip="${a.id}"]`).click();
    const hb = await box(page, `[data-clip-edge="${a.id}"][data-edge="end"]`);
    await dragMouse(page, { x: hb.x + hb.width / 2, y: hb.y + hb.height / 2 }, { x: hb.x + hb.width / 2 + 30, y: hb.y + hb.height / 2 }, { alt: true, shift: true });
    const r = await newerThan(page, P, h.number);
    const na = r.spec.clips.find((x) => x.id === a.id);
    const nb = r.spec.clips.find((x) => x.id === b.id);
    check(na.out > a.out && nb.in > b.in, `roll: ${a.id} ends later and ${b.id} starts later (${a.out}→${na.out}, ${b.in}→${nb.in})`);
    check(Math.abs(r.duration - h.duration) < 0.05, `roll keeps the length (${h.duration.toFixed(2)} → ${r.duration.toFixed(2)} s)`);
    h = r;
  }

  // 4. Split at the playhead (S).
  if (want('split')) {
    const c = h.spec.clips[0];
    const cb = await box(page, `[data-clip="${c.id}"]`);
    const ruler = await box(page, '[data-ruler]');
    await page.mouse.click(cb.x + cb.width * 0.5, ruler.y + ruler.height / 2);
    await page.keyboard.press('s');
    const r = await newerThan(page, P, h.number);
    check(r.spec.clips.length === h.spec.clips.length + 1 && r.spec.clips[1].in === r.spec.clips[0].out, `split: ${c.id} is two clips (${order(r.spec)})`);
    check(Math.abs(r.duration - h.duration) < 0.05, 'a split keeps the length');
    h = r;
  }

  // 5. Mark a stretch on the ruler and cut it out (Delete), across a cut.
  if (want('range')) {
    const b0 = await box(page, `[data-clip="${h.spec.clips[1].id}"]`);
    const b1 = await box(page, `[data-clip="${h.spec.clips[2].id}"]`);
    const ruler = await box(page, '[data-ruler]');
    await dragMouse(page, { x: b0.x + b0.width * 0.6, y: ruler.y + 12 }, { x: b1.x + b1.width * 0.3, y: ruler.y + 12 }, { shift: true });
    await shot(page, 'range-marked');
    await page.keyboard.press('Delete');
    const r = await newerThan(page, P, h.number);
    check(r.duration < h.duration - 0.2, `range: cut out ${(h.duration - r.duration).toFixed(2)} s across two clips`);
    check(r.spec.clips.some((c) => c.id === h.spec.clips[1].id) && r.spec.clips.some((c) => c.id === h.spec.clips[2].id), 'both clips around the stretch are still there, trimmed');
    h = r;
  }

  // 6. Remove a clip by dragging it off the timeline; the toast offers Undo.
  if (want('remove')) {
    const c = h.spec.clips[h.spec.clips.length - 1];
    const cb = await box(page, `[data-clip="${c.id}"]`);
    await dragMouse(
      page,
      { x: cb.x + cb.width / 2, y: cb.y + cb.height / 2 },
      { x: cb.x + cb.width / 2, y: cb.y - 160 },
      {
        beforeUp: async () => shot(page, 'remove-dragging'),
      },
    );
    await page
      .getByText(/^Removed /)
      .first()
      .waitFor({ timeout: 5000 });
    check(true, 'removing shows a toast with Undo');
    const r = await newerThan(page, P, h.number);
    check(!r.spec.clips.some((x) => x.id === c.id), `remove: ${c.id} is gone`);
    h = r;
  }

  // 7. Add footage: drag a source from the “Add footage” panel onto the timeline.
  if (want('add')) {
    await page.getByRole('tab', { name: /Add footage/ }).click();
    const card = page.locator('button[draggable="true"]').first();
    await card.waitFor({ timeout: 10_000 });
    const target = await box(page, `[data-clip="${h.spec.clips[0].id}"]`);
    await card.dragTo(page.locator('[data-timeline-editor] [data-ruler]'), { targetPosition: { x: Math.max(4, target.x - (await box(page, '[data-ruler]')).x + target.width + 3), y: 10 } });
    const r = await newerThan(page, P, h.number);
    check(r.spec.clips.length === h.spec.clips.length + 1, `add: a clip was dragged in (${order(r.spec)})`);
    h = r;
    // The keyboard and touch alternative: open it, mark a part, “Add at the end”.
    await card.click();
    await page.getByRole('button', { name: 'Add at the end' }).click();
    const r2 = await newerThan(page, P, h.number);
    check(r2.spec.clips.length === h.spec.clips.length + 1, 'add: “Add at the end” adds the footage too');
    h = r2;
    await shot(page, 'add-footage-panel', { full: true });
    await page.getByRole('tab', { name: 'Clip' }).click();
  }

  // 8. The transition picker: the marker between two clips.
  if (want('transition')) {
    const into = h.spec.clips[2].id;
    await page.locator(`[data-cut="${into}"]`).click();
    const dlg = page.getByRole('dialog');
    await dlg.waitFor({ timeout: 10_000 });
    await dlg.getByRole('radio', { name: /Fade through black/ }).click();
    await dlg
      .getByText(/half a second|three quarters of a second|a second/)
      .first()
      .waitFor({ timeout: 5000 });
    await dlg.getByRole('radio', { name: /Hear the next clip a moment early/ }).click();
    await page.waitForTimeout(1800);
    await shot(page, 'transition-picker');
    await dlg.getByRole('button', { name: /^Use / }).click();
    await dlg.waitFor({ state: 'detached', timeout: 15_000 });
    const r = await newerThan(page, P, h.number);
    const tr = r.spec.clips.find((c) => c.id === into)?.transitionIn;
    check(tr?.type === 'dip-to-black' && tr.audio?.mode === 'lead', `transition: fade through black, the next voice early (${JSON.stringify(tr)})`);
    check(/^cut-transition:/.test(r.summary.origin ?? ''), 'the picker saves through cut.transition');
    h = r;
  }

  // 9. Keyboard only: select, move, trim an edge, split, mark and cut out.
  if (want('keyboard')) {
    const c = h.spec.clips[1];
    await page.locator(`[data-clip="${c.id}"]`).focus();
    await page.keyboard.press('Enter');
    await page.keyboard.press('Meta+ArrowRight');
    let r = await newerThan(page, P, h.number);
    check(r.spec.clips[2].id === c.id, `keyboard: ${mod()}→ moved ${c.id} one place later`);
    h = r;
    await page.locator(`[data-clip-edge="${c.id}"][data-edge="end"]`).focus();
    for (let i = 0; i < 6; i++) await page.keyboard.press('ArrowLeft');
    r = await newerThan(page, P, h.number);
    const nc = r.spec.clips.find((x) => x.id === c.id);
    check(Math.abs(c.out - nc.out - 0.2) < 0.02, `keyboard: ← on the end handle trimmed 6 frames (${(c.out - nc.out).toFixed(3)} s)`);
    h = r;
    await page.locator(`[data-clip="${h.spec.clips[0].id}"]`).focus();
    await page.keyboard.press('Home');
    for (let i = 0; i < 2; i++) await page.keyboard.press('Shift+ArrowRight');
    await page.keyboard.press('s');
    r = await newerThan(page, P, h.number);
    check(r.spec.clips.length === h.spec.clips.length + 1, 'keyboard: S split at the playhead (2 s in)');
    h = r;
    await page.keyboard.press('i');
    await page.keyboard.press('Shift+ArrowRight');
    await page.keyboard.press('o');
    await page.keyboard.press('Delete');
    r = await newerThan(page, P, h.number);
    check(Math.abs(h.duration - r.duration - 1) < 0.1, `keyboard: I, O and Delete cut out 1 s (${(h.duration - r.duration).toFixed(2)} s)`);
    h = r;
    await page.keyboard.press('?');
    await page.getByRole('dialog', { name: /shortcuts/i }).waitFor({ timeout: 5000 });
    await page.waitForTimeout(600); // let it finish opening
    await shot(page, 'shortcuts');
    await page.keyboard.press('Escape');
  }

  // 10. Someone else changes the same clip meanwhile: a plain message, Keep mine.
  if (want('conflict')) {
    const c = h.spec.clips[0];
    await page.locator(`[data-clip="${c.id}"]`).click();
    const hb = await box(page, `[data-clip-edge="${c.id}"][data-edge="end"]`);
    await dragMouse(page, { x: hb.x + hb.width / 2, y: hb.y + hb.height / 2 }, { x: hb.x + hb.width / 2 - 25, y: hb.y + hb.height / 2 }, { shift: true });
    const mine = await page.evaluate(() => 0);
    void mine;
    // Another machine trims the same clip before the background save (2 s).
    await api(page, 'cut.trim', { projectId: P, clip: c.id, end: '-0.1' });
    await page.getByRole('button', { name: 'Keep mine' }).waitFor({ timeout: 15_000 });
    await shot(page, 'conflict');
    const before = await head(page, P);
    await page.getByRole('button', { name: 'Keep mine' }).click();
    const r = await newerThan(page, P, before.number);
    const nc = r.spec.clips.find((x) => x.id === c.id);
    check(nc.out < before.spec.clips.find((x) => x.id === c.id).out, `conflict: Keep mine saved my trim over theirs (${nc.out})`);
    h = r;
  }

  // 11. Edit by transcript: select words in the cut's transcript and delete them.
  if (want('words')) {
    await page.getByRole('tab', { name: 'Transcript' }).click();
    await page.locator('[data-cut-transcript] [data-wi]').first().waitFor({ timeout: 30_000 });
    h = await head(page, P);
    const clipId = await page.evaluate(() => {
      for (const s of document.querySelectorAll('[data-cut-transcript] [data-clip-words]')) if (s.querySelectorAll('[data-wi]').length >= 6) return s.getAttribute('data-clip-words');
      return null;
    });
    check(!!clipId, `the cut's transcript shows what ${clipId} says`);
    const ws = page.locator(`[data-clip-words="${clipId}"] [data-wi]`);
    const said = `${(await ws.nth(2).textContent()).trim()} … ${(await ws.nth(4).textContent()).trim()}`;
    await ws.nth(2).click();
    await ws.nth(4).click({ modifiers: ['Shift'] });
    await page.locator('[data-word-selection]').waitFor();
    check((await page.locator('[data-cut-transcript] [data-selected]').count()) === 3, 'Shift-click selects three words');
    await shot(page, 'words-selected');
    await page.keyboard.press('Delete');
    const r = await newerThan(page, P, h.number);
    check(r.duration < h.duration - 0.2, `Delete cut “${said}” out (${h.duration.toFixed(2)} → ${r.duration.toFixed(2)} s)`);
    check(/took out words/i.test(r.summary.instruction ?? '') && r.summary.origin === 'timeline:studio', `saved from the timeline with a plain summary (“${r.summary.instruction}”)`);
    await shot(page, 'words-after');
    await page.locator('[data-cut-transcript] [role="region"]').focus();
    await page.keyboard.press('Meta+z');
    const u = await newerThan(page, P, r.number);
    check(Math.abs(u.duration - h.duration) < 0.05, `undo (⌘Z) in the transcript puts the words back (${u.duration.toFixed(2)} s)`);
    h = u;
    // Keyboard: Shift+→ extends a selection from the clicked word.
    await page.locator(`[data-clip-words="${clipId}"] [data-wi]`).nth(1).click();
    await page.keyboard.press('Shift+ArrowRight');
    await page.keyboard.press('Shift+ArrowRight');
    check((await page.locator('[data-cut-transcript] [data-selected]').count()) === 3, 'Shift+→ extends the selection word by word');
    await page.keyboard.press('Escape');
    check((await page.locator('[data-cut-transcript] [data-selected]').count()) === 0, 'Escape clears the selection');
    // Who speaks: the chip opens "who says this?", and selecting words still works.
    const chip = page.locator('[data-cut-transcript] [data-line-chip]').first();
    if (await chip.count()) {
      await chip.click();
      await page.locator('[data-cut-transcript] [data-line-fix]').waitFor();
      await shot(page, 'words-speaker-chip');
      await page.locator('[data-cut-transcript] [data-line-fix] button[aria-label="Close"]').click();
      await page.locator(`[data-clip-words="${clipId}"] [data-wi]`).nth(1).click();
      check((await page.locator('[data-cut-transcript] [data-selected]').count()) === 1, 'speaker chips and word selection work side by side');
      await page.keyboard.press('Escape');
    }
  }

  // 12. Remove ums and restarts: listen to a suggestion, take one out, keep one.
  if (want('cleanup')) {
    await page.getByRole('tab', { name: 'Transcript' }).click();
    await page.locator('[data-cleanup-open]').click();
    const review = page.locator('[data-cleanup-review]');
    await review.waitFor({ timeout: 30_000 });
    const kind = await review.getAttribute('data-cleanup-review');
    if (kind === 'review') {
      const n = await page.locator('[aria-label="All suggestions"] li').count();
      await page.waitForTimeout(2000); // the preview plays the suggestion taken out
      await shot(page, 'cleanup-review');
      check(n >= 1 && (await page.locator('[data-cleanup-preview="without"]').count()) === 1, `${n} suggestions to listen to, each played taken out`);
      const before = await head(page, P);
      const first = (await page.locator('[data-cleanup-words] del').textContent()).trim();
      await page.locator('[data-cleanup-take]').click();
      if (n > 1) {
        await page.getByRole('button', { name: 'As it is now' }).click();
        await page.waitForTimeout(600);
        await page.locator('[data-cleanup-keep]').click();
      }
      await page.locator('[data-cleanup-apply]').click();
      const r = await newerThan(page, P, before.number);
      check(r.duration < before.duration - 0.1, `took out “${first}” (${before.duration.toFixed(2)} → ${r.duration.toFixed(2)} s)`);
      h = r;
      await shot(page, 'cleanup-after');
    } else {
      check(kind === 'none', `no ums or restarts to suggest (${kind})`);
      await page.getByRole('button', { name: 'Done' }).click();
    }
  }

  // 13. In the footage: select words in a clip's transcript, cut them out of the video, undo.
  if (want('source-words')) {
    // A clip of the cut whose footage has words in the part it plays.
    let used;
    for (const c of h.spec.clips) {
      if (c.muted || c.out - c.in < 3) continue;
      const tx = await api(page, 'footage.transcript', { assetId: c.assetId, words: true });
      const said = tx.segments.flatMap((s) => s.words ?? []).filter((w) => w.f > c.in + 0.5 && w.f < c.out - 1);
      if (said.length >= 3) {
        used = c;
        break;
      }
    }
    check(!!used, `${used?.id} plays footage with words in it`);
    await page.goto(`${base}/p/${P}?tab=sources&asset=${used.assetId}&panel=transcript&t=${used.in + 1}`);
    const words = page.locator('[data-wi]');
    await words.first().waitFor({ timeout: 30_000 });
    // Words the cut plays: from just after the clip starts.
    const pick = await page.evaluate(
      ([from, to]) => {
        const titles = [...document.querySelectorAll('[data-wi]')].map((el) => el.getAttribute('title') ?? '');
        const t = (s) => s.split(',')[0].split(':').reduce((a, v) => a * 60 + Number(v), 0);
        const inside = titles.map((s, i) => [i, t(s)]).filter(([, s]) => s > from + 0.5 && s < to - 1);
        return inside.length >= 3 ? [inside[0][0], inside[2][0]] : null;
      },
      [used.in, used.out],
    );
    check(!!pick, 'the footage transcript has words the cut plays');
    const a = page.locator(`[data-wi="${pick[0]}"]`);
    const b = page.locator(`[data-wi="${pick[1]}"]`);
    await a.scrollIntoViewIfNeeded();
    const ab = await a.boundingBox();
    const bb = await b.boundingBox();
    await dragMouse(page, { x: ab.x + 2, y: ab.y + ab.height / 2 }, { x: bb.x + bb.width - 2, y: bb.y + bb.height / 2 });
    await page.locator('[data-word-selection]').waitFor();
    check((await page.locator('[data-selected]').count()) === 3, 'dragging across words selects them');
    await shot(page, 'source-words-selected');
    const before = await head(page, P);
    await page.locator('[data-cut-words]').click();
    const r = await newerThan(page, P, before.number, 20_000, false);
    check(r.duration < before.duration - 0.2 && r.summary.origin === 'cut-remove-words:studio', `cut out of the video from the footage (“${r.summary.instruction}”)`);
    await shot(page, 'source-words-cut');
    await page.getByRole('button', { name: 'Undo' }).click();
    const u = await newerThan(page, P, r.number, 20_000, false);
    check(Math.abs(u.duration - before.duration) < 0.05, `Undo puts them back (“${u.summary.instruction}”)`);
    h = u;
    // Words the cut doesn't play can be marked instead.
    await page.locator(`[data-wi="${pick[0]}"]`).click();
    await page.locator('[data-mark-words]').click();
    await page.waitForTimeout(300);
    await shot(page, 'source-words-marked');
    await page.goto(`${base}/p/${P}`);
    await page.locator('[data-timeline-editor] [data-clip]').first().waitFor({ timeout: 30_000 });
  }

  await shot(page, 'editor-after', { full: true });

  // Revisions: a person's hand edits are grouped into one row that opens.
  if (want('history')) {
    await page.goto(`${base}/p/${P}?tab=history`);
    const run = page.getByRole('button', { name: /changes? by hand/ }).first();
    await run.waitFor({ timeout: 20_000 });
    check((await run.getAttribute('aria-expanded')) === 'false', `hand edits are grouped (“${(await run.textContent()).trim()}”)`);
    await run.click();
    await shot(page, 'history-hand-edits', { full: true });
  }
  await ctx.close();

  // Light theme and the phone layout.
  for (const [theme, mobile] of [
    ['light', false],
    ['dark', true],
    ['light', true],
  ]) {
    const { ctx: c2, page: p2 } = await context(browser, state, theme, mobile);
    await openEditor(p2, P);
    await shot(p2, `editor-${mobile ? 'phone' : 'desktop'}-${theme}`, { full: true });
    if (mobile && want('phone')) {
      // The longest clip: a finger needs room to tap it.
      const clips = (await head(p2, P)).spec.clips;
      const first = clips.reduce((a, c) => ((c.out - c.in) / (c.speed || 1) > (a.out - a.in) / (a.speed || 1) ? c : a), clips[0]);
      await p2.locator(`[data-clip="${first.id}"]`).tap();
      const handle = await box(p2, `[data-clip-edge="${first.id}"][data-edge="end"]`);
      check(handle.width >= 44, `phone: the trim handle is ${Math.round(handle.width)} px wide`);
      const before = await head(p2, P);
      await p2.getByRole('button', { name: /^Split/ }).first().tap();
      const r = await newerThan(p2, P, before.number);
      check(r.spec.clips.length === before.spec.clips.length + 1, 'phone: Split works from the toolbar');
      await shot(p2, `phone-${theme}-selected`, { full: false });
    }
    await c2.close();
  }

  await browser.close();
  console.log(`\n${passed.length} checks passed; screenshots in ${path.relative(process.cwd(), outDir)}`);
  if (problems.length) {
    console.log('\nproblems:');
    for (const p of problems) console.log('  - ' + p);
    process.exit(1);
  }
}

function mod() {
  return process.platform === 'darwin' ? '⌘' : 'Ctrl';
}

main().catch((e) => {
  console.error(e);
  if (problems.length) console.error('problems:\n' + problems.map((p) => '  - ' + p).join('\n'));
  process.exit(1);
});
