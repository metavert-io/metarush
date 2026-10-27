#!/usr/bin/env node
// The dashboard's first run (D256) in a real browser, right after `metarush setup`. Run it through
// scripts/install-e2e.sh (a fresh home with a database on this computer). It checks that:
//   1. the link setup opens (/?welcome=1) lands on Home with Getting started above the Add box;
//   2. it lists what works free on this computer (transcripts, editing, finished videos, the check of every
//      finished video, a stand-in voice), each ready or saying what it needs, and what a key adds (generated
//      shots, AI voices) with the way to Settings → Integrations;
//   3. Claude Code's state and where the projects are kept ("a database on this computer") are said, the same
//      words setup.status gives;
//   4. Getting started stays on Home while the workspace is empty, leaves once a project exists, and comes back
//      with ?welcome=1, which can then be closed;
//   5. plain words, no native dialogs, page errors or horizontal overflow; desktop and phone, dark and light.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
for (const k of ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'METARUSH_MR', 'INSTALL_SHOTS']) {
  if (!process.env[k]) {
    console.error('set METARUSH_DASHBOARD, METARUSH_MINT, METARUSH_MR and INSTALL_SHOTS (scripts/install-e2e.sh does)');
    process.exit(2);
  }
}
const base = process.env.METARUSH_DASHBOARD.replace(/\/$/, '');
const shotsDir = process.env.INSTALL_SHOTS;
mkdirSync(shotsDir, { recursive: true });

const results = [];
const problems = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
}

function mintLink(next) {
  const out = execSync(`${process.env.METARUSH_MINT} --next '${next}'`, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?\S+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return `${base}/auth${new URL(m[0]).search}`;
}

function mr(args) {
  return JSON.parse(execSync(`${process.env.METARUSH_MR} ${args} --json`, { cwd: repo, encoding: 'utf8' })).data;
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

async function newPage(browser, { theme = 'dark', mobile = false, state, label } = {}) {
  const ctx = await browser.newContext({
    viewport: mobile ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: mobile ? 2 : 1,
    colorScheme: theme,
    reducedMotion: 'reduce',
    ...(state ? { storageState: state } : {}),
  });
  await ctx.addInitScript((t) => {
    try {
      localStorage.setItem('metarush.theme', t);
    } catch {
      /* private mode */
    }
  }, theme);
  const page = await ctx.newPage();
  page.on('pageerror', (e) => problems.push(`${label}: pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) problems.push(`${label}: console: ${m.text()}`);
  });
  page.on('dialog', (d) => {
    problems.push(`${label}: native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  return { ctx, page };
}

let shotN = 0;
async function shot(page, name, { full = false } = {}) {
  await page.waitForTimeout(400);
  await page.screenshot({ path: path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`), fullPage: full });
  const overflow = await page.evaluate(() => {
    const w = document.documentElement.clientWidth;
    const by = [...document.querySelectorAll('body *')]
      .filter((el) => el.getBoundingClientRect().right > w + 1 && getComputedStyle(el).position !== 'fixed')
      .map((el) => `${el.tagName.toLowerCase()}.${String(el.className).slice(0, 60)} "${(el.textContent ?? '').trim().slice(0, 40)}"`)
      .slice(-3);
    return { px: document.documentElement.scrollWidth - w, by };
  });
  if (overflow.px > 0) problems.push(`horizontal overflow of ${overflow.px}px on ${name} (${overflow.by.join(' | ')})`);
}

async function main() {
  const browser = await chromium.launch();
  const { ctx, page } = await newPage(browser, { label: 'desktop dark' });
  await page.goto(mintLink('/?welcome=1'));
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  check('setup\'s link lands on Home with ?welcome=1', new URL(page.url()).pathname === '/' && new URL(page.url()).searchParams.get('welcome') === '1', page.url());
  const state = await ctx.storageState();
  const st = await api(page, 'setup.status');
  console.log(`setup.status: firstRun ${st.firstRun}; ${st.noKeys.map((x) => `${x.key}:${x.ready ? 'ready' : 'not yet'}`).join(', ')}; keys ${st.withKeys.map((x) => x.key).join(', ')}; claude ready ${st.claude.ready}; database local ${st.database.local}`);
  check('an empty workspace is a first run', st.firstRun === true);

  const gs = page.locator('[data-getting-started]');
  await gs.waitFor({ timeout: 20_000 });
  await gs.locator('[data-no-keys] [data-works]').first().waitFor({ timeout: 20_000 });
  const order = await page.evaluate(() => {
    const g = document.querySelector('[data-getting-started]');
    const add = document.querySelector('[data-add-box], [data-addbox], input[type=file]');
    return add ? !!(g.compareDocumentPosition(add) & Node.DOCUMENT_POSITION_FOLLOWING) : true;
  });
  check('Getting started is first on Home', order);
  const free = await gs.locator('[data-no-keys] [data-works]').evaluateAll((els) => els.map((e) => [e.getAttribute('data-works'), e.getAttribute('data-ready'), e.innerText]));
  const freeKeys = free.map(([k]) => k);
  check('what works with no keys: transcripts, editing, finished videos, checks, a stand-in voice', ['transcripts', 'editing', 'renders', 'checks', 'scratch-voice'].every((k) => freeKeys.includes(k)), freeKeys.join(', '));
  check('each says what it does, or what it needs', free.every(([, ready, text]) => text.split('\n').length >= 2 && (ready === 'yes' || text.split('\n').length >= 3)), free.map(([k, r]) => `${k}:${r}`).join(' '));
  check('the same as setup.status says', free.every(([k, r]) => st.noKeys.find((x) => x.key === k)?.ready === (r === 'yes')));
  const keyed = await gs.locator('[data-with-keys] [data-works]').evaluateAll((els) => els.map((e) => [e.getAttribute('data-works'), e.getAttribute('data-ready'), e.innerText]));
  check('what a key adds: generated shots and AI voices', ['generated-shots', 'ai-voices'].every((k) => keyed.some(([x]) => x === k)), keyed.map(([k]) => k).join(', '));
  const toKeys = gs.locator('[data-with-keys] a[href="/settings/integrations"]');
  check('each key not set up leads to Settings → Integrations', (await toKeys.count()) === keyed.filter(([, r]) => r === 'no').length, String(await toKeys.count()));
  const claude = (await gs.locator('[data-claude-code]').innerText()).trim();
  check("Claude Code's state, in setup.status's words", claude.startsWith(st.claude.says), claude);
  const dbLine = (await gs.locator('[data-database]').innerText()).trim();
  check('the projects are kept in a database on this computer', dbLine.includes('database on this computer') && st.database.local, dbLine);
  const text = await gs.innerText();
  const unplain = checkShown(text);
  check('Getting started speaks plainly', unplain.length === 0, unplain.map((f) => `${f.kind}: ${f.text}`).join('; '));
  // What isn't ready says what to do; the exact command is behind Details.
  const notReady = st.noKeys.filter((x) => !x.ready && x.command);
  const details = gs.locator('[data-no-keys] [data-command-details]');
  check('the commands for what is not ready are behind Details', (await details.count()) === notReady.length, `${await details.count()} of ${notReady.length}`);
  if (notReady.length) {
    await details.first().locator('summary').click();
    const cmd = (await details.first().locator('code').innerText()).trim();
    check('Details opens to the command', cmd === notReady[0].command, cmd);
    await details.first().locator('summary').click();
  }
  await shot(page, 'getting-started-desktop-dark');

  // Opened on its own (no ?welcome): still there while nothing is in the workspace.
  await page.goto(`${base}/`);
  await gs.waitFor({ timeout: 20_000 });
  check('without ?welcome it stays while the workspace is empty', await gs.isVisible());

  // Light, and phone widths.
  for (const [theme, mobile, name] of [
    ['light', false, 'desktop-light'],
    ['dark', true, 'phone-dark'],
    ['light', true, 'phone-light'],
  ]) {
    const p = await newPage(browser, { theme, mobile, state, label: name });
    await p.page.goto(`${base}/?welcome=1`);
    await p.page.locator('[data-getting-started] [data-works]').first().waitFor({ timeout: 20_000 });
    await shot(p.page, `getting-started-${name}`, { full: mobile });
    await p.ctx.close();
  }

  // A project: no longer a first run, so Home goes back to normal; ?welcome=1 brings it back, with Close.
  const made = mr(`project create "First video"`);
  check('a project was made from the command line', !!made?.project?.id);
  await page.goto(`${base}/`);
  await page.waitForTimeout(1500);
  const st2 = await api(page, 'setup.status');
  check('a workspace with a project is no longer a first run', st2.firstRun === false);
  check('Getting started leaves Home', (await gs.count()) === 0);
  await page.goto(`${base}/?welcome=1`);
  await gs.waitFor({ timeout: 20_000 });
  const close = gs.getByRole('button', { name: 'Close getting started' });
  check('with ?welcome=1 it is back, with Close', await close.isVisible());
  await close.click();
  await page.waitForTimeout(500);
  check('Close puts it away', (await gs.count()) === 0 && !new URL(page.url()).searchParams.has('welcome'), page.url());
  await shot(page, 'home-after-close-desktop-dark');

  await browser.close();
  for (const p of problems) check('no problem in the browser', false, p);
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
  process.exit(failed.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
