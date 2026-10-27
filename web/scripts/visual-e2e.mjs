#!/usr/bin/env node
// End-to-end check of visual understanding in the dashboard, against a daemon
// whose workspace has a clip with a face whose voice is named (and a second clip
// where that face is still a question):
//   the clip's On screen tab (the readout, the named face, "Not …?"), its frame
//   grid (one frame per shot), visual search results in the search palette
//   ("dog", "exit"), the People page's face confirmation ("Is this …?"), and the
//   face question on the To-dos page; screenshots on desktop and phone, dark and
//   light. Finally it answers one face question with Yes and checks the readout.
//
//   METARUSH_DASHBOARD=http://127.0.0.1:4921 \
//   METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   VISUAL_PROJECT=prj_… VISUAL_ASSET=ast_… (named face) VISUAL_ASKED=ast_… (face asked about) \
//   VISUAL_PERSON=Samantha VISUAL_SHOTS=<folder> node scripts/visual-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:4845').replace(/\/$/, '');
const { VISUAL_PROJECT: project, VISUAL_ASSET: asset, VISUAL_ASKED: asked, VISUAL_PERSON: person = 'Samantha' } = process.env;
const shots = process.env.VISUAL_SHOTS ?? path.join(repo, 'tests', 'out', 'visual-shots');
if (!process.env.METARUSH_MINT || !project || !asset || !asked) {
  console.error('set METARUSH_MINT, VISUAL_PROJECT, VISUAL_ASSET and VISUAL_ASKED (see the header of this script)');
  process.exit(2);
}
mkdirSync(shots, { recursive: true });

const results = [];
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

const sources = (id, panel) => `${base}/p/${project}?tab=sources&asset=${id}&panel=${panel}`;

/** Waits until pictures (frames, face crops) have loaded, so screenshots show them. */
async function settle(page) {
  await page.waitForFunction(() => [...document.images].every((i) => i.complete), null, { timeout: 15_000 }).catch(() => undefined);
  await page.waitForFunction(() => !document.querySelector('[data-face-tile].animate-pulse'), null, { timeout: 15_000 }).catch(() => undefined);
  await page.waitForTimeout(300);
}

async function pass(browser, { width, height, phone }, theme, problems) {
  const tag = `${phone ? 'phone' : 'desktop'}-${theme}`;
  const ctx = await browser.newContext({ viewport: { width, height }, deviceScaleFactor: phone ? 2 : 1, isMobile: !!phone, hasTouch: !!phone });
  await ctx.addInitScript((t) => {
    try {
      localStorage.setItem('metarush.theme', t);
      localStorage.setItem('metarush.sourcePanel', JSON.stringify('transcript'));
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

  // 1. The clip's On screen tab: the readout, line by line, with the named face.
  await page.goto(sources(asset, 'onscreen'));
  await page.locator('[data-onscreen-line]').first().waitFor({ timeout: 30_000 });
  const lines = await page.locator('[data-onscreen-line]').allInnerTexts();
  check(`${tag}: readout lines`, lines.length >= 4 && /close-up/.test(lines[0]) && lines[0].includes(`1 face (${person})`), lines[0]?.replace(/\s+/g, ' '));
  check(`${tag}: on-screen text listed`, (await page.getByText('“EXIT”').count()) > 0);
  check(`${tag}: named face with a correction`, (await page.getByRole('button', { name: `Not ${person}?` }).count()) > 0);
  await page.locator('[data-face-tile]').first().waitFor();
  await settle(page);
  await page.screenshot({ path: path.join(shots, `onscreen-${tag}.png`), fullPage: !phone });
  if (phone) {
    await page.locator('[data-onscreen]').scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(shots, `onscreen-panel-${tag}.png`) });
  }

  // 2. The frame grid: one frame per shot, labeled.
  await page.goto(sources(asset, 'sheet'));
  await page.locator('[data-frame-tile]').first().waitFor({ timeout: 30_000 });
  const tiles = await page.locator('[data-frame-tile]').count();
  const shotsN = await page.locator('[data-frame-tile="shot"]').count();
  check(`${tag}: smart frame grid`, tiles >= 8 && shotsN === 5, `${tiles} frames, ${shotsN} shots`);
  await settle(page);
  if (!phone) await page.screenshot({ path: path.join(shots, `framegrid-${tag}.png`), fullPage: true });

  // 3. Visual search in the palette.
  await page.goto(`${base}/`);
  await page.locator('body').click({ position: { x: 5, y: 5 } }).catch(() => undefined);
  await page.keyboard.press('/');
  const box = page.getByRole('combobox');
  await box.waitFor();
  await box.fill('dog');
  const seen = page.getByRole('option', { name: /^Seen: dog/ });
  await seen.first().waitFor({ timeout: 15_000 });
  check(`${tag}: search "dog" finds what's on screen`, (await page.getByText('On screen', { exact: true }).count()) > 0);
  await settle(page);
  await page.screenshot({ path: path.join(shots, `search-dog-${tag}.png`) });
  await box.fill('exit');
  await page.getByRole('option', { name: /^Text on screen: EXIT/ }).first().waitFor({ timeout: 15_000 });
  check(`${tag}: search "exit" finds on-screen text`, true);
  await seen.first().waitFor({ state: 'detached' }).catch(() => undefined);
  await page.keyboard.press('Enter');
  await page.waitForURL((u) => u.searchParams.get('panel') === 'onscreen', { timeout: 15_000 });
  check(`${tag}: a result opens the clip's On screen tab`, page.url().includes(asset));
  await page.keyboard.press('Escape');

  // 4. People: the face confirmation.
  await page.goto(`${base}/library/people`);
  await page.getByRole('button', { name: new RegExp(`^${person}`) }).first().click();
  await page.locator('[data-person-faces]').waitFor({ timeout: 20_000 });
  const askedTiles = await page.locator('[data-face-question]').count();
  const confirmed = await page.locator('[data-face-confirmed]').count();
  check(`${tag}: People shows "Is this ${person}?" and confirmed faces`, askedTiles === 1 && confirmed >= 1, `${askedTiles} asked, ${confirmed} confirmed`);
  await page.locator('[data-person-faces]').scrollIntoViewIfNeeded();
  await settle(page);
  await page.screenshot({ path: path.join(shots, `people-faces-${tag}.png`), fullPage: !phone });

  // 5. To-dos: the face question (one per person per clip, D155) with the face.
  await page.goto(`${base}/todos?kind=faces`);
  await page.locator('[data-todo-kind="face_group"]').first().waitFor({ timeout: 20_000 });
  // The faces asked about (the name picker's closed list also has small faces of people you named).
  const faceTile = page.locator('[data-todo-kind="face_group"] [data-face-tile]:visible');
  await faceTile.first().waitFor({ timeout: 15_000 }).catch(() => undefined);
  check(`${tag}: the to-do shows the face`, (await faceTile.count()) >= 1, `${await faceTile.count()} visible`);
  await settle(page);
  await page.screenshot({ path: path.join(shots, `todos-face-${tag}.png`) });
  return { ctx, page };
}

async function main() {
  const browser = await chromium.launch();
  const problems = [];
  const views = [
    { width: 1440, height: 1000 },
    { width: 390, height: 844, phone: true },
  ];
  let last;
  for (const v of views) {
    for (const theme of ['dark', 'light']) {
      const r = await pass(browser, v, theme, problems);
      if (last) await last.ctx.close();
      last = r;
    }
  }
  // 6. Answer the open question with Yes (People page), then the readout names the face.
  const { page } = last;
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(`${base}/library/people`);
  await page.getByRole('button', { name: new RegExp(`^${person}`) }).first().click();
  await page.locator('[data-face-question] >> role=button[name="Yes"]').click();
  await page.getByText(`That face is ${person} now`).first().waitFor({ timeout: 15_000 });
  await page.goto(sources(asked, 'onscreen'));
  await page.locator('[data-onscreen-line]').first().waitFor({ timeout: 30_000 });
  // The face asked about is wherever it is in the clip (one question per person per clip, D155), not always first.
  const named = (await page.locator('[data-onscreen-line]').allInnerTexts()).find((l) => l.includes(`(${person})`));
  check('Yes on the People page names the face in its clip', !!named, (named ?? 'no line names them').replace(/\s+/g, ' '));
  await browser.close();
  for (const p of problems) console.log(`FAIL ${p}`);
  const failed = results.filter((r) => !r.ok).length + problems.length;
  console.log(`\n${results.length - results.filter((r) => !r.ok).length}/${results.length} checks passed${problems.length ? `, ${problems.length} page problems` : ''}; screenshots in ${shots}`);
  process.exit(failed ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
