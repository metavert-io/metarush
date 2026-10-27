#!/usr/bin/env node
// End-to-end check of faces recognized across clips (D114), against a daemon
// whose workspace has a clip with a face a person named and another clip
// where MetaRush recognized that face by its looks:
//   the person's page shows the recognized faces (dashed, "Recognized, N% alike",
//   That's … / Not …?) next to the confirmed one; the other clip's On screen tab
//   shows "recognized by their face … Not confirmed yet." with That's … and
//   Not …?; the readout says "looks like …"; finally "Not …?" takes one name
//   back and the face is no longer named. Screenshots on desktop and phone,
//   dark and light.
//
//   METARUSH_DASHBOARD=http://127.0.0.1:4896 \
//   METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   FACES_PROJECT=prj_… FACES_ASSET=ast_… (the clip with recognized faces) FACES_PERSON=Samantha \
//   FACES_SHOTS=<folder> node scripts/faces-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:4845').replace(/\/$/, '');
const { FACES_PROJECT: project, FACES_ASSET: asset, FACES_PERSON: person = 'Samantha' } = process.env;
const shots = process.env.FACES_SHOTS ?? path.join(repo, 'tests', 'out', 'faces-shots');
if (!process.env.METARUSH_MINT || !project || !asset) {
  console.error('set METARUSH_MINT, FACES_PROJECT and FACES_ASSET (see the header of this script)');
  process.exit(2);
}
mkdirSync(shots, { recursive: true });

const results = [];
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
const sources = (id, panel) => `${base}/p/${project}?tab=sources&asset=${id}&panel=${panel}`;

async function settle(page) {
  await page.waitForFunction(() => [...document.images].every((i) => i.complete), null, { timeout: 15_000 }).catch(() => undefined);
  await page.waitForFunction(() => !document.querySelector('[data-face-tile].animate-pulse'), null, { timeout: 15_000 }).catch(() => undefined);
  await page.waitForTimeout(300);
}

const browser = await chromium.launch();
const problems = [];
const views = [
  { width: 1440, height: 1000, phone: false },
  { width: 390, height: 844, phone: true },
];
let last = null;
for (const v of views) {
  for (const theme of ['dark', 'light']) {
    const tag = `${v.phone ? 'phone' : 'desktop'}-${theme}`;
    const ctx = await browser.newContext({ viewport: { width: v.width, height: v.height }, deviceScaleFactor: v.phone ? 2 : 1, isMobile: v.phone, hasTouch: v.phone });
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

    // 1. The person's page: confirmed and recognized faces.
    await page.goto(`${base}/library/people`);
    await page.getByRole('button', { name: new RegExp(`^${person}`) }).first().click();
    await page.locator('[data-person-faces]').waitFor({ timeout: 20_000 });
    const recognized = page.locator('[data-face-recognized]');
    const nRec = await recognized.count();
    const nConf = await page.locator('[data-face-confirmed]').count();
    check(`${tag}: ${person}'s page shows faces recognized by looks and the confirmed one`, nRec >= 1 && nConf >= 1, `${nRec} recognized, ${nConf} confirmed`);
    check(`${tag}: a recognized face says how alike and offers That's / Not …?`,
      (await recognized.first().getByText(/Recognized, \d+% alike/).count()) === 1 &&
        (await recognized.first().getByRole('button', { name: `That's ${person}` }).count()) === 1 &&
        (await recognized.first().getByRole('button', { name: `Not ${person}?` }).count()) === 1);
    await page.locator('[data-person-faces]').scrollIntoViewIfNeeded();
    await settle(page);
    await page.screenshot({ path: path.join(shots, `people-recognized-${tag}.png`), fullPage: !v.phone });

    // 2. The other clip's On screen tab: the face named by its looks.
    await page.goto(sources(asset, 'onscreen'));
    await page.locator('[data-onscreen-line]').first().waitFor({ timeout: 30_000 });
    const lines = await page.locator('[data-onscreen-line]').allInnerTexts();
    check(`${tag}: the readout says who it looks like`, lines.some((l) => l.includes(`looks like ${person}`)), lines[0]?.replace(/\s+/g, ' '));
    const row = page.locator('[data-face-recognized="looks"]').first();
    await row.waitFor({ timeout: 15_000 });
    check(`${tag}: the face row says it isn't confirmed yet`, (await row.getByText(/recognized by their face, \d+% alike\. Not confirmed yet\./).count()) === 1);
    check(`${tag}: That's ${person} and Not ${person}? on the row`, (await row.getByRole('button', { name: `That's ${person}` }).count()) === 1 && (await row.getByRole('button', { name: `Not ${person}?` }).count()) === 1);
    await settle(page);
    await page.screenshot({ path: path.join(shots, `onscreen-recognized-${tag}.png`), fullPage: !v.phone });
    if (v.phone) {
      await page.locator('[data-onscreen]').scrollIntoViewIfNeeded();
      await row.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(shots, `onscreen-recognized-row-${tag}.png`) });
    }
    last = { ctx, page, tag };
    if (!(v.phone && theme === 'light')) await ctx.close();
  }
}

// 3. "Not …?" takes one name back (once, at the end).
if (last) {
  const { ctx, page, tag } = last;
  const before = await page.locator('[data-face-recognized="looks"]').count();
  await page.locator('[data-face-recognized="looks"]').first().getByRole('button', { name: `Not ${person}?` }).click();
  // The faces her face was learned from (D158), then "Not …" for this one.
  const dlg = page.locator('[data-compare-faces]');
  await dlg.waitFor({ timeout: 15_000 });
  await dlg.locator('[data-learned-face]').first().waitFor({ timeout: 15_000 }).catch(() => undefined);
  check(`${tag}: "Not ${person}?" shows the faces ${person}'s face was learned from`, (await dlg.locator('[data-learned-face]').count()) >= 1);
  await dlg.getByRole('button', { name: `This face isn't ${person}`, exact: true }).click();
  await page.getByText(`Noted: that face is not ${person}.`).first().waitFor({ timeout: 15_000 });
  await page.waitForFunction((n) => document.querySelectorAll('[data-face-recognized="looks"]').length < n, before, { timeout: 15_000 }).catch(() => undefined);
  const after = await page.locator('[data-face-recognized="looks"]').count();
  check(`${tag}: "Not ${person}?" takes that name back`, after === before - 1, `${before} → ${after}`);
  await settle(page);
  await page.screenshot({ path: path.join(shots, `onscreen-after-not-${tag}.png`) });
  await ctx.close();
}
await browser.close();
for (const p of problems) console.log(`FAIL ${p}`);
const failed = results.filter((r) => !r.ok).length + problems.length;
console.log(`\n${results.length - results.filter((r) => !r.ok).length}/${results.length} checks passed${problems.length ? `, ${problems.length} page problems` : ''}; screenshots in ${shots}`);
process.exit(failed ? 1 : 0);
