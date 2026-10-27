#!/usr/bin/env node
// End-to-end check of how crops are framed (D73, D115): under the player of the
// Edit tab, a cut whose latest version doesn't say how to frame its crops
// offers Keep faces in frame and Follow movement; pressing Keep faces in frame
// saves a new version that does (the toast's Undo saves one that keeps the
// middle again, and pressing it again turns faces back on); Follow movement
// saves a version whose crops follow the subject, and Hold still goes back to
// faces. Screenshots on desktop and phone, dark and light.
//
//   METARUSH_DASHBOARD=http://127.0.0.1:4922 \
//   METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   FRAMING_PROJECT=prj_… FRAMING_SHOTS=<folder> node scripts/framing-e2e.mjs
//
// The project needs a cut whose latest version has no output.framing (a cut made
// before the setting, or one saved with a setOutput op without it).
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:4845').replace(/\/$/, '');
const project = process.env.FRAMING_PROJECT;
const shots = process.env.FRAMING_SHOTS ?? path.join(repo, 'tests', 'out', 'framing-shots');
if (!process.env.METARUSH_MINT || !project) {
  console.error('set METARUSH_MINT and FRAMING_PROJECT (see the header of this script)');
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

const browser = await chromium.launch();
const problems = [];
const views = [
  { width: 1440, height: 1000, phone: false },
  { width: 390, height: 844, phone: true },
];
let first = true;
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
    await page.goto(mintLink());
    await page.waitForURL((u) => !u.pathname.startsWith('/auth'));
    await page.goto(`${base}/p/${project}`);
    const ctl = page.locator('[data-faces-in-frame]');
    await ctl.waitFor({ timeout: 30_000 });
    if (first) {
      first = false;
      // Start from a version that doesn't keep faces in frame.
      if ((await ctl.getAttribute('data-faces-in-frame')) === 'on') {
        await ctl.getByRole('button', { name: 'Turn off' }).click();
        await page.locator('[data-faces-in-frame="off"]').waitFor({ timeout: 15_000 });
      }
      check(`${tag}: a cut without the setting offers it`, (await ctl.getByRole('button', { name: 'Keep faces in frame' }).count()) === 1);
      await ctl.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(shots, `framing-off-${tag}.png`) });
      await ctl.getByRole('button', { name: 'Keep faces in frame' }).click();
      const toast = page.getByText('Crops keep faces in frame').first();
      await toast.waitFor({ timeout: 15_000 });
      await page.locator('[data-faces-in-frame="on"]').waitFor({ timeout: 15_000 });
      check(`${tag}: pressing it saves a version that keeps faces in frame`, true);
      // Undo (the toast's) saves a version that keeps the middle again; pressing again turns faces on.
      await page.getByRole('status').filter({ hasText: 'Crops keep faces in frame' }).getByRole('button', { name: 'Undo' }).click();
      await page.getByText('Framing put back').first().waitFor({ timeout: 15_000 });
      await page.locator('[data-faces-in-frame="off"]').waitFor({ timeout: 15_000 });
      check(`${tag}: Undo puts the framing back`, true);
      await page.locator('[data-faces-in-frame] >> role=button[name="Keep faces in frame"]').click();
      await page.locator('[data-faces-in-frame="on"]').waitFor({ timeout: 15_000 });
      // Follow movement (D115): a new version whose crops follow the subject; Hold still goes back to faces.
      check(`${tag}: faces in frame offers following movement`, (await ctl.getByRole('button', { name: 'Follow movement' }).count()) === 1);
      await ctl.getByRole('button', { name: 'Follow movement' }).click();
      await page.getByText('Crops follow movement').first().waitFor({ timeout: 15_000 });
      await page.locator('[data-crop-framing="follow"]').waitFor({ timeout: 15_000 });
      check(`${tag}: Follow movement saves a version whose crops follow the subject`, (await page.getByText('Crops follow movement').count()) > 0);
      await ctl.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(shots, `framing-follow-${tag}.png`) });
      check(`${tag}: following offers Hold still and Turn off`, (await ctl.getByRole('button', { name: 'Hold still' }).count()) === 1 && (await ctl.getByRole('button', { name: 'Turn off' }).count()) === 1);
      await ctl.getByRole('button', { name: 'Hold still' }).click();
      await page.locator('[data-crop-framing="faces"]').waitFor({ timeout: 15_000 });
      check(`${tag}: Hold still keeps faces in frame, in one place`, true);
    }
    check(`${tag}: the latest version keeps faces in frame`, (await ctl.getAttribute('data-faces-in-frame')) === 'on' && (await page.getByText('Faces kept in frame').count()) > 0);
    await ctl.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(shots, `framing-on-${tag}.png`) });
    await ctx.close();
  }
}
await browser.close();
for (const p of problems) console.log(`FAIL ${p}`);
const failed = results.filter((r) => !r.ok).length + problems.length;
console.log(`\n${results.length - results.filter((r) => !r.ok).length}/${results.length} checks passed${problems.length ? `, ${problems.length} page problems` : ''}; screenshots in ${shots}`);
process.exit(failed ? 1 : 0);
