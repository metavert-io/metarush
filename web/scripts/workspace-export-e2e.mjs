#!/usr/bin/env node
// End-to-end check of exporting a workspace from the dashboard and opening a restored copy (D329–D333), against
// throwaway daemons that scripts/workspace-export-e2e.sh sets up:
//   MODE=export (the original):
//     A  Settings → Storage → Export the workspace: what an archive holds, the restore said to be a terminal step
//     B  Check what it would hold: the workspace's name, records, what stays out on purpose, nothing written
//     C  Export… asks in a styled dialog; the archive is made in the background and the list says it is ready,
//        with where it is behind Details (printed for the script as ARCHIVE=<path>)
//     D  phone, dark and light: the card without sideways scrolling
//   MODE=copy (the copy restored from the archive in a fresh home):
//     E  the dashboard opens on the copy: the workspace's name, its project, and the restore in the Log
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
//
//   MODE=export METARUSH_DASHBOARD=http://127.0.0.1:5670 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   EXPORT_SHOTS=<dir> node web/scripts/workspace-export-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5670').replace(/\/$/, '');
const MODE = process.env.MODE ?? 'export';
if (!process.env.METARUSH_MINT) {
  console.error('set METARUSH_MINT and METARUSH_DASHBOARD (see the header of this script)');
  process.exit(2);
}
const SHOTS = process.env.EXPORT_SHOTS ?? '';
if (SHOTS) mkdirSync(SHOTS, { recursive: true });

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail && !ok ? ` - ${detail}` : detail ? ` (${String(detail).slice(0, 160)})` : ''}`);
}

function mintLink(next = '') {
  const out = execSync(process.env.METARUSH_MINT, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  const u = new URL(m[0]);
  if (next) u.searchParams.set('next', next);
  return `${base}/auth${u.search}`;
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
      return r.json();
    },
    [op, input],
  );
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function shot(page, name) {
  await sleep(400);
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: false });
}

async function waitFor(fn, ms, every = 500) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > end) return null;
    await sleep(every);
  }
}

async function newPage(browser, { theme = 'dark', phone = false, next = '' } = {}) {
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
  await page.goto(mintLink(next));
  await page.waitForURL(`${base}/**`);
  return page;
}

async function readText(page, selector) {
  return page.evaluate(
    ([sel, open, close]) => {
      const root = document.querySelector(sel);
      if (!root) return '';
      const added = [];
      for (const el of root.querySelectorAll('[data-user-content]')) {
        if (el.parentElement?.closest('[data-user-content]') || !el.textContent?.trim()) continue;
        const a = document.createTextNode(open);
        const b = document.createTextNode(close);
        el.prepend(a);
        el.append(b);
        added.push(a, b);
      }
      // What is behind a closed Details isn't read as plain copy.
      const hidden = [...root.querySelectorAll('details:not([open]) > :not(summary)')];
      for (const h of hidden) h.setAttribute('data-e2e-hidden', h.style.display), (h.style.display = 'none');
      const t = root.innerText;
      for (const h of hidden) (h.style.display = h.getAttribute('data-e2e-hidden') ?? ''), h.removeAttribute('data-e2e-hidden');
      for (const n of added) n.remove();
      return t;
    },
    [selector, THEIRS_OPEN, THEIRS_CLOSE],
  );
}

async function plainIn(page, selector, what) {
  const text = await readText(page, selector);
  const unplain = text.split('\n').flatMap((l) => checkShown(l).map((f) => `${f.kind} ${JSON.stringify(f.text)}`));
  check(`${what} reads plainly`, text.length > 0 && unplain.length === 0, unplain.join(' | ') || `${text.length} characters`);
  return text;
}

async function noSideways(page, what) {
  const [w, widest] = await page.evaluate(() => {
    let far = null;
    for (const el of document.querySelectorAll('body *')) {
      const r = el.getBoundingClientRect();
      if (r.right > window.innerWidth + 1 && (!far || r.right > far.right)) far = { right: Math.round(r.right), tag: el.tagName, text: (el.textContent ?? '').trim().slice(0, 60) };
    }
    return [document.documentElement.scrollWidth, far];
  });
  check(`${what}: no sideways scrolling`, w <= 392, `scrollWidth ${w}${widest ? `; widest ${widest.tag} at ${widest.right}: ${widest.text}` : ''}`);
}

async function openCard(page) {
  await page.goto(`${base}/settings/storage#export`);
  await page.locator('[data-workspace-export] [data-export-holds]').waitFor({ timeout: 20_000 });
  await page.locator('[data-workspace-export]').scrollIntoViewIfNeeded();
  await sleep(300);
}

const browser = await chromium.launch();
try {
  if (MODE === 'export') {
    const page = await newPage(browser, { theme: 'light', next: '/settings/storage' });

    // A — the card.
    await openCard(page);
    const card = page.locator('[data-workspace-export]');
    const holds = await card.locator('[data-export-holds]').innerText();
    check('A it says what an archive holds and how to keep it', /consent records/.test(holds) && /Passwords, keys/.test(holds) && /keep it as carefully/.test(holds), holds);
    const note = await card.locator('[data-restore-note]').innerText();
    check('A restoring is a terminal step, never here', /in a terminal/.test(note) && /never here/.test(note) && /never deletes anything/.test(note), note);
    const before = await api(page, 'workspace.exports', {});
    check('A no exports yet', before.ok && before.data.exports.length === 0, JSON.stringify(before).slice(0, 200));
    await plainIn(page, '[data-workspace-export]', 'A the export card');
    await shot(page, 'A-export-card');

    // B — the plan.
    await card.locator('[data-export-check]').click();
    const plan = card.locator('[data-export-plan]');
    await plan.waitFor({ timeout: 30_000 });
    const planText = await plan.innerText();
    check('B the plan names the workspace and counts its records', /FernBrook Studio/.test(planText) && /\d+ records?/.test(planText), planText);
    check('B the plan says what stays out on purpose', /Stays out on purpose/.test(planText) || /credentials/.test(planText), planText);
    const stillNone = await api(page, 'workspace.exports', {});
    check('B checking wrote nothing', stillNone.data.exports.length === 0);
    await plainIn(page, '[data-export-plan]', 'B the plan');
    await shot(page, 'B-plan');

    // C — Export….
    await card.locator('[data-export-start]').click();
    const confirm = page.getByRole('dialog');
    await confirm.waitFor({ timeout: 10_000 });
    const confirmText = await confirm.innerText();
    check('C a styled confirm says what it holds and where it goes', /Export the workspace\?/.test(confirmText) && /consent records/.test(confirmText) && /MetaRush’s own folder/.test(confirmText), confirmText);
    await shot(page, 'C-confirm');
    await confirm.getByRole('button', { name: 'Export' }).click();
    await confirm.waitFor({ state: 'detached', timeout: 10_000 });
    const ready = await waitFor(async () => (await card.locator('[data-export-row="done"]').count()) === 1, 120_000);
    check('C the archive is made in the background and said to be ready', !!ready, await card.innerText().catch(() => ''));
    const says = await card.locator('[data-export-row="done"] [data-export-says]').innerText().catch(() => '');
    check('C it says what the archive holds', /The archive is ready: \d/.test(says) && /in all/.test(says), says);
    await plainIn(page, '[data-export-list]', 'C the exports');
    await shot(page, 'C-ready');
    const list = await api(page, 'workspace.exports', {});
    const rec = list.data?.exports?.[0];
    check('C one archive, a .tar in MetaRush’s archives folder', !!rec && rec.state === 'done' && /\/archives\/[^/]+\.tar$/.test(rec.path), JSON.stringify(rec ?? {}).slice(0, 200));
    if (rec) console.log(`ARCHIVE=${rec.path}`);
    // A path from the browser is refused.
    const named = await api(page, 'workspace.export', { to: '/tmp/somewhere-else' });
    check('C a path the browser names is refused', !named.ok && named.error?.code === 'E_INVALID_INPUT', JSON.stringify(named).slice(0, 200));

    // D — phone, dark and light.
    for (const theme of ['dark', 'light']) {
      const p = await newPage(browser, { theme, phone: true, next: '/settings/storage' });
      await openCard(p);
      await noSideways(p, `D the export card on a phone (${theme})`);
      await shot(p, `D-export-phone-${theme}`);
      await p.context().close();
    }
  } else {
    // E — the restored copy.
    const page = await newPage(browser, { theme: 'dark', next: '/settings/general' });
    await page.goto(`${base}/settings/general`);
    const ws = await waitFor(async () => {
      const t = await page.locator('main').innerText();
      return /FernBrook Studio/.test(t) ? t : null;
    }, 30_000);
    check('E the copy opens: Settings → General names the workspace', !!ws);
    const projects = await api(page, 'project.list', {});
    check('E the copy has its project', projects.ok && projects.data.projects.some((p) => p.name === 'Spring launch'), JSON.stringify(projects).slice(0, 200));
    await page.goto(`${base}/`);
    const home = await waitFor(async () => {
      const t = await page.locator('main').innerText();
      return /Spring launch/.test(t) ? t : null;
    }, 30_000);
    check('E Home shows the project', !!home);
    await shot(page, 'E-copy-home');
    const log = await api(page, 'log.list', {});
    const restored = log.ok && log.data.entries.some((e) => e.op?.name === 'workspace.restore');
    check('E the Log has the restore', restored, JSON.stringify(log.data?.entries?.slice(0, 3) ?? log).slice(0, 300));
    await page.goto(`${base}/settings/log`);
    const logText = await waitFor(async () => {
      const t = await page.locator('main').innerText();
      return /Restored the workspace from an export/i.test(t) ? t : null;
    }, 30_000);
    check('E the Log page says it in words', !!logText);
    await shot(page, 'E-copy-log');
  }
} finally {
  await browser.close();
}

const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
process.exit(failed.length ? 1 : 0);
