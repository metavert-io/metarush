#!/usr/bin/env node
// End-to-end check of asking who people are (D155–D158) against a daemon
// whose workspace has a speaker-view call (scripts/make-call-fixtures.sh) in
// a project whose cut uses its first minutes, another clip with someone else's
// voice and a clip with one of the call's faces:
//   the To-dos page shows one question per person as a grid (sample faces,
//   "Seen N times", "In your cut", the names the video shows as evidence, the
//   keys); one click uses the names shown in the video; a voice named after a
//   wrong earlier voice opens the comparison dialog (this voice next to what
//   the name was learned from, the odd one marked), "Not …" fixes the earlier
//   mistake in place and the warning goes away; "Not …?" on a face named by
//   its looks shows the faces that name was learned from; answers for many at
//   once (D189) on a second copy of the call in another project: "None of
//   these are …" offered for names two questions suggest, "Not … in this
//   clip" from a tile, "Skip all in this project". Screenshots on desktop and
//   phone, dark and light.
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5362 \
//   METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   PEOPLE_MR="bin/metarush --home <home>" PEOPLE_CALL=ast_… PEOPLE_OTHER=ast_… PEOPLE_LOOK=ast_… \
//   PEOPLE_SHOTS=<folder> node scripts/people-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5362').replace(/\/$/, '');
const { PEOPLE_MR: mr, PEOPLE_CALL: call, PEOPLE_OTHER: other, PEOPLE_LOOK: lookClip, PEOPLE_COPY: copy, PEOPLE_P2: p2 } = process.env;
const shots = process.env.PEOPLE_SHOTS ?? path.join(repo, 'tests', 'out', 'people-shots');
if (!process.env.METARUSH_MINT || !mr || !call || !other || !lookClip || !copy || !p2) {
  console.error('set METARUSH_MINT, PEOPLE_MR, PEOPLE_CALL, PEOPLE_OTHER, PEOPLE_LOOK, PEOPLE_COPY and PEOPLE_P2 (see the header of this script)');
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
const ADA = 'Ada Park';

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
}
function cli(args) {
  const out = execSync(`${mr} ${args} --json`, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  return JSON.parse(out).data;
}
function mintLink() {
  const out = execSync(process.env.METARUSH_MINT, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return `${base}/auth${new URL(m[0]).search}`;
}
async function settle(page) {
  await page.waitForFunction(() => [...document.images].every((i) => i.complete), null, { timeout: 15_000 }).catch(() => undefined);
  await page.waitForFunction(() => !document.querySelector('[data-face-tile].animate-pulse'), null, { timeout: 20_000 }).catch(() => undefined);
  await page.waitForTimeout(400);
}
async function open(browser, v, theme, problems) {
  const tag = `${v.phone ? 'phone' : 'desktop'}-${theme}`;
  const ctx = await browser.newContext({ viewport: { width: v.width, height: v.height }, deviceScaleFactor: v.phone ? 2 : 1, isMobile: !!v.phone, hasTouch: !!v.phone });
  await ctx.addInitScript((t) => {
    try {
      localStorage.setItem('metarush.theme', t);
    } catch {
      // storage unavailable
    }
  }, theme);
  const page = await ctx.newPage();
  page.on('pageerror', (e) => problems.push(`${tag} pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) problems.push(`${tag} console: ${m.text()}`);
  });
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'));
  return { ctx, page, tag };
}

const browser = await chromium.launch();
const problems = [];
const views = [
  { width: 1440, height: 1000 },
  { width: 390, height: 844, phone: true },
];

// 1. The grid: one question per person, with their faces and why.
for (const v of views) {
  for (const theme of ['dark', 'light']) {
    const { ctx, page, tag } = await open(browser, v, theme, problems);
    await page.goto(`${base}/todos`);
    await page.locator('[data-face-grid]').waitFor({ timeout: 30_000 });
    const tiles = page.locator(`[data-todo-kind="face_group"][data-asset="${call}"]`);
    const n = await tiles.count();
    check(`${tag}: one question per person in the call`, n === 3, `${n} tiles`);
    // visual-a's talking head is asked about on its own: one question per person per clip.
    const elsewhere = page.locator(`[data-todo-kind="face_group"][data-asset="${lookClip}"]`);
    check(`${tag}: the face that talks in another clip is its own question there`, (await elsewhere.count()) === 1 && (await elsewhere.getByText(/^Seen once in/).count()) === 1);
    let faces = 0;
    let seen = 0;
    let shown = 0;
    for (let i = 0; i < n; i++) {
      const t = tiles.nth(i);
      faces += (await t.locator('[data-face-tile]').count()) >= 4 ? 1 : 0;
      seen += (await t.getByText(/^Seen \d+ times/).count()) > 0 ? 1 : 0;
      shown += (await t.locator('[data-evidence="shown"]').count()) > 0 ? 1 : 0;
    }
    check(`${tag}: each tile shows 4–6 of the person's faces and how often they're seen`, faces === n && seen === n, `${faces} with faces, ${seen} with "Seen N times"`);
    check(`${tag}: the name the video shows under each face is the evidence`, shown === n);
    check(`${tag}: the faces in the cut are marked`, (await page.getByText('In your cut').count()) >= 1);
    check(`${tag}: one click for the names shown in the video`, (await page.locator(`[data-accept-shown="${call}"]`).count()) === 1);
    // Keyboard: j moves to the next question.
    if (!v.phone) {
      await page.locator('body').click({ position: { x: 5, y: 5 } });
      const before = await page.locator('[data-todo-kind="face_group"][aria-current="true"]').getAttribute('data-todo-index').catch(() => null);
      await page.keyboard.press('j');
      await page.waitForTimeout(200);
      const after = await page.locator('[data-todo-kind="face_group"][aria-current="true"]').getAttribute('data-todo-index').catch(() => null);
      check(`${tag}: j moves to the next question`, after !== null && after !== before, `${before} → ${after}`);
    }
    await settle(page);
    await page.locator('[data-face-grid]').scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(shots, `grid-${tag}.png`), fullPage: !v.phone });
    if (v.phone) {
      await tiles.first().scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(shots, `grid-tile-${tag}.png`) });
    }
    await ctx.close();
  }
}

// 1b. Answers for many at once (D189), on the second copy of the call (the
// phone only looks; the desktop then answers).
for (const v of [views[1], views[0]]) {
  const theme = 'dark';
  const { ctx, page, tag } = await open(browser, v, theme, problems);
  await page.goto(`${base}/todos`);
  await page.locator('[data-face-grid]').waitFor({ timeout: 30_000 });
  const none = page.locator('[data-none-of]');
  await none.first().waitFor({ timeout: 20_000 }).catch(() => undefined);
  const noneText = (await none.allTextContents()).join(' | ');
  check(`${tag}: "None of these are …" for the names both calls suggest`, (await none.count()) >= 3 && /None of these are Ada Park \(2\)/.test(noneText), noneText);
  const tiles = page.locator(`[data-todo-kind="face_group"][data-asset="${copy}"]`);
  const tile = tiles.first();
  check(`${tag}: each tile offers "Not … in this clip" and "Skip all in this clip", and can be selected`,
    (await tile.locator('[data-not-here]').count()) === 1 && (await tile.locator('[data-skip-clip]').count()) === 1 && (await tile.locator('[data-select-face]').count()) === 1);
  check(`${tag}: select all`, (await page.locator('[data-select-all-faces]').count()) === 1);
  await settle(page);
  await page.locator('[data-face-grid]').scrollIntoViewIfNeeded();
  await page.screenshot({ path: path.join(shots, `bulk-${tag}.png`), fullPage: !v.phone });
  if (v.phone) {
    await tile.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(shots, `bulk-tile-${tag}.png`) });
    await ctx.close();
    continue;
  }
  // "Not Ben Okafor in this clip", from his tile in the copy (the keyboard: c on the active tile).
  await page.goto(`${base}/todos?asset=${copy}`);
  const ben = page.locator(`[data-todo-kind="face_group"][data-asset="${copy}"]`).filter({ has: page.locator('[data-not-here="Ben Okafor"]') }).first();
  await ben.waitFor({ timeout: 20_000 });
  await ben.click({ position: { x: 20, y: 140 } });
  await page.keyboard.press('c');
  await page.getByText(/^Noted: the person asked about isn't Ben Okafor\./).first().waitFor({ timeout: 30_000 });
  await page.waitForFunction((a) => !document.querySelector(`[data-asset="${a}"] [data-not-here="Ben Okafor"]`) || ![...document.querySelectorAll(`[data-asset="${a}"] h2`)].some((h) => h.textContent === 'Is this Ben Okafor?'), copy, { timeout: 20_000 }).catch(() => undefined);
  const asks = await page.locator(`[data-asset="${copy}"] h2`).allTextContents();
  check(`${tag}: not Ben Okafor in this clip: he isn't asked about there any more`, !asks.includes('Is this Ben Okafor?'), asks.join(' | '));
  const callAsks = cli(`todos groups --asset ${call}`).groups.map((g) => g.question);
  check('…and the first call still asks about him', callAsks.includes('Is this Ben Okafor?'), callAsks.join(' | '));
  // "Skip all in this project": a styled confirm (never the browser's own), then nothing left there.
  await page.goto(`${base}/todos?project=${p2}`);
  await page.locator('[data-skip-project]').click();
  const dlg = page.getByRole('dialog');
  await dlg.waitFor({ timeout: 10_000 });
  await page.screenshot({ path: path.join(shots, `skip-project-confirm-${tag}.png`) });
  await dlg.getByRole('button', { name: 'Skip them all' }).click();
  await page.getByText(/^Skipped \d+ questions? about faces in “Second club”/).first().waitFor({ timeout: 30_000 });
  check(`${tag}: skip all in this project`, cli(`todos groups --project ${p2}`).groups.length === 0);
  check('…the first call keeps its three questions', cli(`todos groups --asset ${call}`).groups.length === 3);
  await ctx.close();
}

// 2. One click: the names shown in the video, for everyone in the clip.
{
  const { ctx, page, tag } = await open(browser, views[0], 'light', problems);
  await page.goto(`${base}/todos`);
  await page.locator(`[data-accept-shown="${call}"]`).click();
  await page.getByText(/^Named 3 people in “.+” from the names the video shows/).first().waitFor({ timeout: 30_000 });
  await page.waitForFunction(() => document.querySelectorAll('[data-todo-kind="face_group"]').length === 0, null, { timeout: 20_000 }).catch(() => undefined);
  check(`${tag}: one click names everyone the call shows`, (await page.locator('[data-todo-kind="face_group"]').count()) === 0);
  const fs = cli(`footage faces ${call}`).faces;
  const named = fs.filter((f) => f.name);
  check('the faces of the call are named', named.length >= fs.length - 2, `${named.length} of ${fs.length}: ${[...new Set(named.map((f) => f.name))].join(', ')}`);
  await settle(page);
  await page.screenshot({ path: path.join(shots, `after-accept-${tag}.png`), fullPage: true });
  await ctx.close();
}

// 3. "Is this really Ada Park?": hear what her voice was learned from, fix it in place.
cli(`footage speakers rename ${other} "Speaker 1" "${ADA}"`); // a wrong voice named Ada Park first
// The phone only looks (the question stays open); the desktop then fixes it.
for (const v of [views[1], views[0]]) {
  const theme = v.phone ? 'dark' : 'light';
  const { ctx, page, tag } = await open(browser, v, theme, problems);
  await page.goto(`${base}/todos?asset=${call}&kind=speakers`);
  const btn = page.getByRole('button', { name: new RegExp(`^${ADA} \\(on screen while this voice talks\\)`) }).first();
  await btn.waitFor({ timeout: 30_000 });
  await btn.click();
  const dlg = page.locator('[data-compare-voices]');
  await dlg.waitFor({ timeout: 20_000 });
  await dlg.locator('[data-learned-voice]').first().waitFor({ timeout: 20_000 });
  check(`${tag}: the warning, with this voice to hear`, (await dlg.locator('[data-pitch-warning]').count()) === 1 && (await dlg.locator('[aria-label="This voice"] [data-sample-tile]').count()) >= 1);
  const odd = dlg.locator('[data-learned-voice][data-odd]');
  check(`${tag}: the voice ${ADA}'s voice was learned from is there, marked as unlike this one`, (await odd.count()) === 1, (await odd.innerText().catch(() => '')).replace(/\s+/g, ' '));
  await settle(page);
  await page.screenshot({ path: path.join(shots, `compare-voices-${tag}.png`) });
  if (v.phone) {
    // On the phone, only look; the fix is made once, on the desktop.
    await dlg.getByRole('button', { name: 'No, let me pick another name' }).click();
    await ctx.close();
    continue;
  }
  await odd.getByRole('button', { name: `Not ${ADA}` }).click();
  const picker = dlg.getByRole('combobox');
  await picker.fill('Rex Hale');
  await page.getByRole('option', { name: /Add someone new/ }).first().click();
  await dlg.locator('[data-warning-gone]').waitFor({ timeout: 30_000 });
  check(`${tag}: taking the wrong voice back ends the warning in place`, (await dlg.locator('[data-pitch-warning]').count()) === 0 && (await dlg.locator('[data-learned-voice]').count()) === 0);
  await settle(page);
  await page.screenshot({ path: path.join(shots, `compare-voices-fixed-${tag}.png`) });
  await dlg.getByRole('button', { name: `Yes, it's ${ADA}` }).click();
  await page.getByText(`That voice is ${ADA} now.`).first().waitFor({ timeout: 30_000 });
  const voices = cli(`footage speakers ${call}`).speakers.filter((s) => s.name === ADA);
  const others = cli(`footage speakers ${other}`).speakers.filter((s) => s.name === 'Rex Hale');
  check('the answer that raised the warning is finished, and the earlier mistake is fixed', voices.length === 1 && others.length === 1);
  await ctx.close();
}

// 4. "Not Ada Park?" on a face named by its looks: the faces her face was learned from.
for (const v of views) {
  const theme = v.phone ? 'light' : 'dark';
  const { ctx, page, tag } = await open(browser, v, theme, problems);
  await page.goto(`${base}/library/people`);
  await page.getByRole('button', { name: new RegExp(`^${ADA}`) }).first().click();
  const rec = page.locator('[data-face-recognized]').first();
  await rec.waitFor({ timeout: 30_000 });
  await rec.getByRole('button', { name: `Not ${ADA}?` }).click();
  const dlg = page.locator('[data-compare-faces]');
  await dlg.waitFor({ timeout: 20_000 });
  await dlg.locator('[data-learned-face]').first().waitFor({ timeout: 20_000 });
  const lf = await dlg.locator('[data-learned-face]').first().innerText();
  check(`${tag}: the faces ${ADA}'s face was learned from, how alike each is`, /faces, one answer/.test(lf) && /% like this face/.test(lf), lf.replace(/\s+/g, ' '));
  await settle(page);
  await page.screenshot({ path: path.join(shots, `compare-faces-${tag}.png`) });
  await dlg.getByRole('button', { name: 'Cancel' }).click();
  await ctx.close();
}

await browser.close();
for (const p of problems) console.log(`FAIL ${p}`);
const failed = results.filter((r) => !r.ok).length + problems.length;
console.log(`\n${results.length - results.filter((r) => !r.ok).length}/${results.length} checks passed${problems.length ? `, ${problems.length} page problems` : ''}; screenshots in ${shots}`);
process.exit(failed ? 1 : 0);
