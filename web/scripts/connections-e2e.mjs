#!/usr/bin/env node
// End-to-end check of built-in connections (D303–D305) in the dashboard, against a seeded
// throwaway daemon that reaches nothing outside this computer (scripts/connections-e2e.sh does
// all of it):
//   A  a timeline from another editor dropped on Home's Add box opens the import dialog instead of
//      an upload: what it is (FCPXML 1.11, its clips, how long), the footage it found in the
//      Library (by name and length), a question for the clip it didn't find, the chapter, what
//      can't be carried over
//   B  the question answered (leave it out), the project chosen, Make the cut: the new version's
//      page, with the cut's clips
//   C  Post to YouTube on that page: what holds it (not approved, no final render, not set up on
//      this computer), in plain words; nothing can be posted
//   D  phone dark and light: the dialog and the version page, no sideways scrolling
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5570 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   CONNECTIONS_TIMELINE=<file.fcpxml> CONNECTIONS_SHOTS=<dir> node web/scripts/connections-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5570').replace(/\/$/, '');
const timeline = process.env.CONNECTIONS_TIMELINE ?? '';
if (!process.env.METARUSH_MINT || !timeline) {
  console.error('set METARUSH_MINT, METARUSH_DASHBOARD and CONNECTIONS_TIMELINE (see the header of this script)');
  process.exit(2);
}
const SHOTS = process.env.CONNECTIONS_SHOTS ?? '';
if (SHOTS) mkdirSync(SHOTS, { recursive: true });

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail && !ok ? ` - ${detail}` : detail ? ` (${String(detail).slice(0, 200)})` : ''}`);
}

function mintLink() {
  const out = execSync(process.env.METARUSH_MINT, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return `${base}/auth${new URL(m[0]).search}`;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function shot(page, name) {
  await sleep(400);
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: false });
}

async function newPage(browser, { theme = 'dark', phone = false } = {}) {
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
  const page = await ctx.newPage();
  page.on('dialog', (d) => {
    check('no native browser dialog', false, `${d.type()}: ${d.message()}`);
    void d.dismiss();
  });
  await page.goto(mintLink());
  await page.waitForURL(`${base}/**`);
  return page;
}

// The text a person reads in an element, their own words marked as theirs.
async function readText(page, selector) {
  return page.evaluate(
    ([sel, open, close]) => {
      const root = document.querySelector(sel);
      if (!root) return '';
      const clone = root.cloneNode(true);
      for (const el of clone.querySelectorAll('[data-user-content]')) {
        el.textContent = open + el.textContent + close;
      }
      for (const el of clone.querySelectorAll('details:not([open]) > :not(summary), code')) el.remove();
      return clone.textContent ?? '';
    },
    [selector, THEIRS_OPEN, THEIRS_CLOSE],
  );
}

async function plain(page, selector, what) {
  const text = await readText(page, selector);
  const problems = checkShown(text);
  check(`${what} reads plainly`, problems.length === 0, problems.map((p) => p.message ?? String(p)).join('; '));
}

async function noSideways(page, what) {
  const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  check(`${what}: no sideways scrolling`, wide <= 1, `${wide}px`);
}

async function dropTimeline(page) {
  await page.goto(`${base}/`);
  const input = page.getByTestId('addbox-files').first();
  await input.waitFor({ state: 'attached', timeout: 15_000 });
  await input.setInputFiles(timeline);
  const dialog = page.getByTestId('timeline-import');
  await dialog.waitFor({ timeout: 15_000 });
  await page.getByText(/FCPXML 1\.11/).first().waitFor({ timeout: 20_000 });
  return dialog;
}

const browser = await chromium.launch();
try {
  // A: the dialog reads the timeline.
  const page = await newPage(browser);
  const dialog = await dropTimeline(page);
  const text = (await dialog.textContent()) ?? '';
  check('A: a timeline opens the import dialog, not an upload', text.includes('Rough cut') && /FCPXML 1\.11: \d+ clips/.test(text), text.slice(0, 200));
  check('A: the footage found by name and length', /Interview/.test(text) && /same name and length/.test(text));
  check('A: a question for the clip it did not find', /“Missing broll” isn't in the Library/.test(text));
  check('A: the chapter it brings', /0:01 .*The garden|The garden/.test(text));
  check('A: Make the cut waits for the answer and a project', await page.getByTestId('timeline-import-make').isDisabled());
  await shot(page, 'a-dialog');
  await plain(page, '[data-testid="timeline-import"]', 'A: the dialog');

  // B: answer, choose the project, make the cut.
  await dialog.getByRole('button', { name: 'Leave it out' }).first().click();
  await page.getByText('Left out of the cut').first().waitFor({ timeout: 15_000 });
  const picker = dialog.getByRole('combobox');
  await picker.fill('Garden');
  await page.getByRole('option', { name: /Garden walk/ }).first().click();
  await page.getByText(/What can't be carried over/).first().waitFor({ timeout: 15_000 });
  const lost = (await dialog.textContent()) ?? '';
  check('B: leaving it out is listed as not carried over', /Missing broll.*was left out/.test(lost), lost.slice(0, 300));
  await shot(page, 'b-answered');
  const make = page.getByTestId('timeline-import-make');
  await make.waitFor();
  check('B: Make the cut is ready', !(await make.isDisabled()));
  await make.click();
  await page.waitForURL(/\/p\/prj_[0-9a-z]+.*rev=rev_/, { timeout: 30_000 });
  check('B: the new version opens', true, page.url());
  await page.getByText(/Imported from FCPXML 1\.11|Rough cut/).first().waitFor({ timeout: 20_000 });
  await shot(page, 'b-version');

  // C: Post to YouTube is held, plainly.
  const rev = new URL(page.url()).searchParams.get('rev');
  const project = page.url().match(/\/p\/(prj_[0-9a-z]+)/)?.[1];
  await page.goto(`${base}/p/${project}/r/${rev}`);
  const post = page.getByTestId('post-to-youtube');
  await post.waitFor({ timeout: 20_000 });
  await post.getByRole('button', { name: /Look at what would be posted/ }).click();
  await post.getByText(/can't be posted to YouTube yet/).waitFor({ timeout: 20_000 });
  const held = (await post.textContent()) ?? '';
  check('C: held until approved', held.includes("isn't approved yet"), held.slice(0, 300));
  check('C: held without a final render', held.includes('has no final render yet'));
  check('C: held until YouTube is set up here', held.includes("YouTube isn't set up on this computer yet"));
  check('C: nothing can be posted', (await post.getByTestId('post-to-youtube-send').count()) === 0);
  await shot(page, 'c-held');
  await plain(page, '[data-testid="post-to-youtube"]', 'C: Post to YouTube');

  // D: phone, light and dark.
  for (const theme of ['light', 'dark']) {
    const phone = await newPage(browser, { theme, phone: true });
    await dropTimeline(phone);
    await noSideways(phone, `D: the dialog on a phone (${theme})`);
    await shot(phone, `d-dialog-phone-${theme}`);
    await phone.goto(`${base}/p/${project}/r/${rev}`);
    await phone.getByTestId('post-to-youtube').waitFor({ timeout: 20_000 });
    await noSideways(phone, `D: the version page on a phone (${theme})`);
    await shot(phone, `d-version-phone-${theme}`);
    await phone.context().close();
  }
} finally {
  await browser.close();
}
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length} of ${results.length} checks passed`);
process.exit(failed.length ? 1 : 0);
