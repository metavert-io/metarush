#!/usr/bin/env node
// End-to-end check of the motion graphics pages (D68) against a running
// daemon that has at least one checked motion graphic waiting for a person
// (run `metarush motion check <folder>` first):
//   the list and the review page in the dark and light themes, on a desktop
//   and a phone (no sideways scrolling, frames and preview load, the source
//   is there) · approving with the styled confirm dialog · withdrawing the
//   approval with the styled prompt · no native dialogs, no console errors.
//
//   METARUSH_DASHBOARD=http://127.0.0.1:4801 \
//   METARUSH_MINT="bin/metarush --home <test home> dashboard --print-link --no-open" \
//   [SHOTS=<folder for screenshots>] node web/scripts/motion-e2e.mjs
import { execSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:4845').replace(/\/$/, '');
const shots = process.env.SHOTS;
if (!process.env.METARUSH_MINT) {
  console.error('set METARUSH_MINT (see the header of this script)');
  process.exit(2);
}
if (shots) fs.mkdirSync(shots, { recursive: true });

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

const viewports = { desktop: { width: 1440, height: 900 }, phone: { width: 390, height: 844 } };

async function newPage(browser, theme, size) {
  const ctx = await browser.newContext({ viewport: viewports[size], deviceScaleFactor: size === 'phone' ? 2 : 1, colorScheme: theme });
  await ctx.addInitScript((t) => {
    try {
      localStorage.setItem('metarush.theme', t);
    } catch {
      // ignore
    }
  }, theme);
  const page = await ctx.newPage();
  const problems = [];
  page.on('dialog', async (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    await d.dismiss();
  });
  page.on('console', (m) => {
    if (m.type() === 'error' && !/favicon/.test(m.text())) problems.push(`console error: ${m.text()}`);
  });
  page.on('pageerror', (e) => problems.push(`page error: ${e.message}`));
  await page.goto(mintLink());
  await page.waitForLoadState('networkidle');
  return { ctx, page, problems };
}

const noSideways = (page) => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1);

async function shot(page, name) {
  if (shots) await page.screenshot({ path: path.join(shots, `${name}.png`), fullPage: true });
}

const browser = await chromium.launch();
try {
  // Find a version waiting for a person.
  const probe = await newPage(browser, 'dark', 'desktop');
  const list = await api(probe.page, 'motion.list', {});
  const waiting = list.components.find((c) => c.state === 'waiting');
  await probe.ctx.close();
  if (!waiting) throw new Error('no motion graphic is waiting for a person; run metarush motion check first');
  check('a version waits for a person', true, waiting.name);

  for (const theme of ['dark', 'light']) {
    for (const size of ['desktop', 'phone']) {
      const tag = `${theme}-${size}`;
      const { ctx, page, problems } = await newPage(browser, theme, size);
      const htmlTheme = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
      check(`${tag}: theme applied`, htmlTheme === theme, htmlTheme ?? 'none');

      await page.goto(`${base}/library/motion`);
      await page.locator('[data-library-tab="motion"][aria-current="page"]').waitFor();
      await page.getByText(waiting.name, { exact: true }).first().waitFor();
      check(`${tag}: Motion is a tab of the Library, with what waits on the Library link`, (await page.getByRole('navigation', { name: 'Primary' }).locator('[data-motion-badge]').count()) === 1);
      check(`${tag}: the list shows ${waiting.name} as waiting`, await page.getByText('Waiting for a person').first().isVisible());
      check(`${tag}: the list doesn't scroll sideways`, await noSideways(page));
      await shot(page, `motion-list-${tag}`);

      await page.goto(`${base}/motion/${waiting.hash}`);
      await page.getByRole('heading', { name: waiting.name }).waitFor();
      const img = page.getByRole('img', { name: new RegExp(`Test frames of ${waiting.name}`) });
      await img.waitFor();
      const loaded = await img.evaluate((el) => el.complete && el.naturalWidth > 0);
      check(`${tag}: the test frames load`, loaded);
      check(`${tag}: there's a preview video`, (await page.locator('video').count()) === 1);
      for (const h of ['Test frames', 'What it does', 'What it uses', 'Source']) {
        check(`${tag}: section ${h}`, await page.getByRole('heading', { name: h }).first().isVisible());
      }
      // After an earlier approved version, the page opens on what changed; the whole file is one click away.
      const onlyChanged = page.getByLabel('Only what changed');
      if ((await onlyChanged.count()) && (await onlyChanged.isChecked())) {
        check(`${tag}: what changed since the approved version is marked`, (await page.locator('[data-change="added"], [data-change="removed"]').count()) > 0);
        await onlyChanged.uncheck();
      }
      check(`${tag}: the source is shown`, await page.getByText('export const schema').first().isVisible());
      check(`${tag}: Approve and Turn down are offered`, (await page.getByRole('button', { name: 'Approve' }).isVisible()) && (await page.getByRole('button', { name: 'Turn down' }).isVisible()));
      check(`${tag}: the review page doesn't scroll sideways`, await noSideways(page));
      const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
      check(`${tag}: page background follows the theme`, theme === 'dark' ? bg !== 'rgb(242, 241, 238)' : bg !== 'rgb(13, 12, 11)', bg);
      await shot(page, `motion-review-${tag}`);
      check(`${tag}: no native dialogs or console errors`, problems.length === 0, problems.join(' | '));
      await ctx.close();
    }
  }

  // Deciding: the styled confirm and prompt, then the state everywhere.
  const { ctx, page, problems } = await newPage(browser, 'dark', 'desktop');
  await page.goto(`${base}/motion/${waiting.hash}`);
  await page.getByRole('button', { name: 'Approve' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.waitFor();
  check('approving asks in a styled dialog', (await dialog.getByText(`Approve ${waiting.name}?`).isVisible()) && (await dialog.getByText('needs your approval').isVisible()));
  await shot(page, 'motion-approve-dialog');
  await dialog.getByRole('button', { name: 'Approve' }).click();
  await page.getByText(`Approved ${waiting.name}@`).first().waitFor({ timeout: 15_000 });
  await page.getByRole('button', { name: 'Withdraw approval' }).waitFor();
  const after = await api(page, 'motion.show', { ref: waiting.hash });
  check('approved by a person', after.row.state === 'approved' && after.component.decisions?.length >= 1, after.row.stateLabel);
  await page.getByRole('button', { name: 'Withdraw approval' }).click();
  const prompt = page.getByRole('alertdialog').or(page.getByRole('dialog'));
  await prompt.waitFor();
  await prompt.getByRole('textbox').fill('the colors are off');
  await shot(page, 'motion-withdraw-dialog');
  await prompt.getByRole('button', { name: 'Withdraw approval' }).click();
  await page.getByText(`Withdrew approval of ${waiting.name}@`).first().waitFor({ timeout: 15_000 });
  const revoked = await api(page, 'motion.show', { ref: waiting.hash });
  check('approval withdrawn, with the note', revoked.row.state === 'revoked' && revoked.component.decisions?.at(-1)?.note === 'the colors are off', revoked.row.stateLabel);
  await page.getByRole('button', { name: 'Approve' }).waitFor();
  await shot(page, 'motion-review-withdrawn');
  check('deciding: no native dialogs or console errors', problems.length === 0, problems.join(' | '));
  await ctx.close();
} finally {
  await browser.close();
}

const failed = results.filter((r) => !r.ok);
console.log(failed.length ? `${failed.length} check(s) failed` : `all ${results.length} checks passed`);
process.exit(failed.length ? 1 : 0);
