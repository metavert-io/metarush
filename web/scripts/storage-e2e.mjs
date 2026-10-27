#!/usr/bin/env node
// Moving the media from the dashboard (D112) in a real browser. Run it through
// scripts/storage-e2e.sh: two throwaway installations of one workspace that
// share a folder, each with its own dashboard. It checks that:
//   1. Settings → Storage → "Move the media to another storage" checks first
//      (nothing is copied or created), then asks in a styled dialog;
//   2. the move shows in the activity bar, the dashboard starts again with the
//      new storage by itself and the page says how it went, in plain words;
//   3. on the other computer, the menu button (D184) carries a dot and the page says
//      "This computer still uses the old storage", with the form filled in;
//      switching it copies nothing new and switches it;
//   4. no native dialogs, page errors or horizontal overflow. Screenshots:
//      desktop dark and light, phone, in STORAGE_SHOTS.
// It CHANGES both installations, so point it only at throwaway ones.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const need = ['METARUSH_DASHBOARD_A', 'METARUSH_MINT_A', 'METARUSH_DASHBOARD_B', 'METARUSH_MINT_B', 'STORAGE_NEW', 'STORAGE_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/storage-e2e.sh does)`);
    process.exit(2);
  }
}
const newRoot = process.env.STORAGE_NEW;
const newName = `the shared folder ${path.basename(newRoot)}`;
const shotsDir = process.env.STORAGE_SHOTS;
mkdirSync(shotsDir, { recursive: true });

const results = [];
const problems = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
}

function mintLink(which) {
  const base = process.env[`METARUSH_DASHBOARD_${which}`].replace(/\/$/, '');
  const out = execSync(process.env[`METARUSH_MINT_${which}`], { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
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

// While a dashboard starts again with the new storage, its requests fail for a
// moment: that is expected, so those console lines are not problems.
const restarting = /ERR_CONNECTION_REFUSED|ERR_EMPTY_RESPONSE|ERR_CONNECTION_RESET|Failed to load resource|status of 50[234]|EventSource/;

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
    if (m.type() === 'error' && !/status of 4\d\d/.test(m.text()) && !restarting.test(m.text())) problems.push(`${label}: console: ${m.text()}`);
  });
  page.on('dialog', (d) => {
    problems.push(`${label}: native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  return { ctx, page };
}

let shotN = 0;
async function shot(page, name) {
  await page.waitForTimeout(400);
  const file = path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file, fullPage: false });
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

/** The section's words as people read them, people's own words marked as theirs. */
async function sectionText(page) {
  return page.locator('section#storage').evaluate((sec) => {
    const added = [];
    for (const el of sec.querySelectorAll('[data-user-content]')) {
      if (el.parentElement?.closest('[data-user-content]') || !el.textContent?.trim()) continue;
      const a = document.createTextNode('“');
      const b = document.createTextNode('”');
      el.prepend(a);
      el.append(b);
      added.push(a, b);
    }
    const text = sec.innerText; // closed Details stay out, as on screen
    for (const n of added) n.remove();
    return text;
  });
}

async function plain(page, label) {
  const text = await sectionText(page);
  const found = checkShown(text);
  check(`${label}: in plain words`, found.length === 0, found.map((f) => `${f.kind} ${f.text}`).join('; '));
  return text;
}

/** Waits for the storage section to say how the move went (the dashboard restarts on the way). */
async function waitDone(page, which) {
  const end = Date.now() + 150_000;
  let last = '';
  while (Date.now() < end) {
    try {
      const st = await api(page, 'storage.status');
      last = `${st.summary} / ${st.move?.state}`;
      if (!st.moving && st.move && st.move.state !== 'starting' && st.move.state !== 'running') return st;
    } catch (e) {
      last = String(e); // restarting
    }
    await page.waitForTimeout(1000);
  }
  throw new Error(`${which}: the move did not finish: ${last}`);
}

async function main() {
  const browser = await chromium.launch();

  /* ---------- Studio Mac moves the media ---------- */
  const { ctx: ctxA, page: a } = await newPage(browser, { label: 'studio' });
  await a.goto(mintLink('A'));
  await a.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const stateA = await ctxA.storageState();
  await a.goto(`${process.env.METARUSH_DASHBOARD_A}/settings/storage#storage`);
  const sec = a.locator('section#storage');
  await sec.getByRole('button', { name: 'Move the media to another storage' }).waitFor();
  await sec.scrollIntoViewIfNeeded();
  await plain(a, 'before');
  await shot(a, 'storage-before-desktop-dark');

  await sec.getByRole('button', { name: 'Move the media to another storage' }).click();
  await sec.getByLabel('Folder', { exact: true }).fill(newRoot);
  await sec.getByRole('checkbox', { name: /Every computer of the workspace reaches this folder/ }).check();
  await sec.getByRole('button', { name: 'Check what it would copy' }).click();
  const planBox = sec.locator('[data-storage-plan]');
  await planBox.waitFor({ timeout: 60_000 });
  const planText = await planBox.innerText();
  check('the check says what moving would copy', new RegExp(`Moving to ${newName} would copy \\d+ files`).test(planText), planText.split('\n')[0]);
  check('the check mentions the other computer', /Other machines in this workspace \(Edit laptop\) keep using/.test(planText));
  const created = await a.evaluate(async () => (await (await fetch('/api/status', { credentials: 'same-origin' })).json()).data.mediaStore);
  check('checking moved nothing', !created.includes('New media'), created);
  await plain(a, 'the plan');
  await shot(a, 'storage-plan-desktop-dark');

  await sec.getByRole('button', { name: 'Move the media', exact: true }).click();
  const dlg = a.locator('[data-mr-dialog="open"]');
  await dlg.waitFor();
  const dlgText = await dlg.innerText();
  check('a styled dialog asks first', dlgText.includes(`Move the media to ${newName}?`) && dlgText.includes('Nothing is deleted'), dlgText.split('\n')[0]);
  await shot(a, 'storage-confirm-desktop-dark');
  await dlg.locator('button[type="submit"]').click();

  // The activity bar shows it (it may already be done: two files are quick).
  let seen = false;
  for (let i = 0; i < 40 && !seen; i++) {
    try {
      const s = await api(a, 'activity.summary');
      seen = (s.items ?? []).some((it) => it.category === 'storage');
    } catch {
      /* restarting */
    }
    if (!seen) await a.waitForTimeout(250);
  }
  check('the activity bar shows the move', seen);
  const doneA = await waitDone(a, 'studio');
  check('the move finished', doneA.move.state === 'done' && /^Moved \d+ files/.test(doneA.move.summary), doneA.move.summary);
  check('this computer now uses the new folder', doneA.this.locator === `fs:${newRoot}` && !doneA.switchMeToo, doneA.this.locator);
  // The page follows by itself once the dashboard is back.
  await sec.locator('[data-move-state="done"]').waitFor({ timeout: 60_000 });
  await a.getByText(`Right now this computer keeps the media in`).first().waitFor();
  const after = await plain(a, 'after the move');
  check('the page says where the media is now', after.includes(`Right now this computer keeps the media in “${newName}”`), after.split('\n').slice(0, 3).join(' / '));
  await sec.scrollIntoViewIfNeeded();
  await shot(a, 'storage-done-desktop-dark');
  {
    const { ctx, page } = await newPage(browser, { theme: 'light', state: stateA, label: 'studio light' });
    await page.goto(`${process.env.METARUSH_DASHBOARD_A}/settings/storage#storage`);
    await page.locator('section#storage [data-move-state="done"]').waitFor({ timeout: 30_000 });
    await page.locator('section#storage').scrollIntoViewIfNeeded();
    await shot(page, 'storage-done-desktop-light');
    await ctx.close();
  }

  /* ---------- Edit laptop switches too ---------- */
  const { ctx: ctxB, page: b } = await newPage(browser, { label: 'laptop' });
  await b.goto(mintLink('B'));
  await b.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const stateB = await ctxB.storageState();
  await b.locator('[data-account-menu] [data-storage-badge]').waitFor({ timeout: 30_000 });
  check('the menu button carries a dot on the other computer', true);
  await b.locator('[data-account-menu]').click();
  check('…and so does Storage in the menu', (await b.locator('[data-menu-settings-storage] [data-storage-badge]').count()) === 1);
  await b.keyboard.press('Escape');
  await b.goto(`${process.env.METARUSH_DASHBOARD_B}/settings/storage#storage`);
  const secB = b.locator('section#storage');
  const prompt = secB.locator('[data-switch-me-too]');
  await prompt.waitFor({ timeout: 30_000 });
  const promptText = await prompt.innerText();
  check('the other computer is told it still uses the old storage', promptText.startsWith('This computer still uses the old storage') && promptText.includes(newName), promptText.split('\n')[0]);
  await plain(b, 'switch me too');
  await secB.scrollIntoViewIfNeeded();
  await shot(b, 'switch-me-too-desktop-dark');
  {
    const { ctx, page } = await newPage(browser, { mobile: true, state: stateB, label: 'laptop phone' });
    await page.goto(`${process.env.METARUSH_DASHBOARD_B}/settings/storage#storage`);
    await page.locator('section#storage [data-switch-me-too]').waitFor({ timeout: 30_000 });
    await page.locator('section#storage [data-switch-me-too]').scrollIntoViewIfNeeded();
    await shot(page, 'switch-me-too-phone-dark');
    await ctx.close();
  }

  await prompt.getByRole('button', { name: 'Switch this computer too' }).click();
  const folder = secB.getByLabel('Folder', { exact: true });
  check('the form is filled in from the new storage', (await folder.inputValue()) === newRoot && (await secB.getByRole('checkbox', { name: /Every computer/ }).isChecked()));
  await secB.getByRole('button', { name: 'Check what it would copy' }).click();
  const planB = secB.locator('[data-storage-plan]');
  await planB.waitFor({ timeout: 60_000 });
  const planBText = await planB.innerText();
  check('switching copies nothing new', planBText.startsWith(`Everything is in ${newName} already`), planBText.split('\n')[0]);
  await shot(b, 'switch-plan-desktop-dark');
  await secB.getByRole('button', { name: 'Switch this computer', exact: true }).click();
  const dlgB = b.locator('[data-mr-dialog="open"]');
  await dlgB.waitFor();
  check('a styled dialog asks first (switch)', (await dlgB.innerText()).includes(`Switch this computer to ${newName}?`));
  await dlgB.locator('button[type="submit"]').click();
  const doneB = await waitDone(b, 'laptop');
  check('the other computer switched', doneB.move.state === 'done' && doneB.this.locator === `fs:${newRoot}` && !doneB.switchMeToo, `${doneB.move.summary} (${doneB.this.locator})`);
  await secB.locator('[data-move-state="done"]').waitFor({ timeout: 60_000 });
  await b.reload();
  await b.locator('section#storage').waitFor();
  await b.waitForTimeout(1500);
  check('the dot is gone', (await b.locator('[data-storage-badge]').count()) === 0);
  await plain(b, 'after switching');
  await b.locator('section#storage').scrollIntoViewIfNeeded();
  await shot(b, 'switched-desktop-dark');

  await browser.close();
  for (const p of problems) check(p, false);
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
  process.exit(failed.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
