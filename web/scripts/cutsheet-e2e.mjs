#!/usr/bin/env node
// Drives the studio's Script tab (the cut sheet: Formatted, Markdown and Edit)
// end to end in a real browser and saves screenshots (desktop and mobile, dark
// and light) to web/.screenshots/cutsheet/ (or SCREENSHOTS_DIR).
//
// It CHANGES the project (it applies a new revision), so point it only at a
// throwaway installation. There is deliberately no default URL.
//
//   METARUSH_DASHBOARD=http://127.0.0.1:4891 \
//   METARUSH_MINT="./.agent-home/mr dashboard --print-link --no-open" \
//   PROJECT="Builder story" node web/scripts/cutsheet-e2e.mjs
//
// The project needs at least one revision whose first clip line ends in a time,
// and footage named in the sheet's settings.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const outDir = process.env.SCREENSHOTS_DIR ? path.resolve(process.env.SCREENSHOTS_DIR) : path.join(here, '..', '.screenshots', 'cutsheet');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
if (!base) {
  console.error('Set METARUSH_DASHBOARD to a throwaway dashboard (this script applies a revision).');
  process.exit(2);
}

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
      const r = await fetch(`/api/ops/${op}`, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' }, body: JSON.stringify(input) });
      const j = await r.json();
      if (!j.ok) throw new Error(`${op}: ${j.error?.code} ${j.error?.message}`);
      return j.data;
    },
    [op, input],
  );
}

const problems = [];
const shots = [];
let step = 0;

function check(ok, what) {
  if (!ok) throw new Error(`check failed: ${what}`);
  console.log(`  ok  ${what}`);
}

async function shot(page, name) {
  const file = path.join(outDir, `${String(++step).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file, fullPage: true });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
  shots.push(path.relative(process.cwd(), file));
}

async function context(browser, state, theme, mobile) {
  const ctx = await browser.newContext({
    viewport: mobile ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: mobile ? 2 : 1,
    storageState: state,
    colorScheme: theme,
    reducedMotion: 'reduce',
  });
  await ctx.addInitScript((t) => {
    try {
      localStorage.setItem('metarush.theme', t);
      for (const k of Object.keys(localStorage)) if (k.startsWith('metarush.cutsheet.draft.')) localStorage.removeItem(k);
    } catch {}
  }, theme);
  const page = await ctx.newPage();
  page.on('console', (m) => m.type() === 'error' && problems.push(`console.error ${page.url()}: ${m.text()}`));
  page.on('pageerror', (e) => problems.push(`pageerror ${page.url()}: ${e.message}`));
  return { ctx, page };
}

/** End of the logical line (End stops at each wrap point on narrow screens). */
async function lineEnd(page) {
  for (let i = 0; i < 6; i++) await page.keyboard.press('End');
}

/** The editor line that starts with `prefix` (CodeMirror renders one .cm-line per line). */
const line = (page, prefix) => page.locator('.cm-line', { hasText: prefix }).first();

/**
 * The editor line that starts with `prefix`, scrolled into view first: CodeMirror
 * only renders the lines near the viewport, and the settings at the top (narration,
 * mix) can push the clips below it, especially on a phone.
 */
async function reveal(page, prefix) {
  await page.evaluate(() => {
    const s = document.querySelector('.cm-scroller');
    if (s) s.scrollTop = 0;
  });
  for (let i = 0; i < 60 && (await line(page, prefix).count()) === 0; i++) {
    await page.evaluate(() => {
      const s = document.querySelector('.cm-scroller');
      if (s) s.scrollTop += 200;
      window.scrollBy(0, 200);
    });
    await page.waitForTimeout(100);
  }
  const l = line(page, prefix);
  await l.scrollIntoViewIfNeeded();
  return l;
}

async function waitStatus(page, re, timeout = 15_000) {
  await page.locator('section[aria-label="Cut sheet"] [aria-live="polite"]').filter({ hasText: re }).waitFor({ timeout });
}

async function openSheet(page, projectId, extra = '') {
  await page.goto(`${base}/p/${projectId}?tab=script&view=edit${extra}`);
  await page.locator('.cm-content').waitFor({ timeout: 20_000 });
  await page.waitForTimeout(900);
}

/** The Script tab's Formatted view (the default). */
async function openScript(page, projectId, extra = '') {
  await page.goto(`${base}/p/${projectId}?tab=script${extra}`);
  await page.locator('[data-script-view] [data-clip]').first().waitFor({ timeout: 20_000 });
  await page.waitForTimeout(700);
}

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
  const before = await api(ap, 'project.show', { id: project.id });
  const headBefore = before.revisions.find((r) => r.id === before.headRevisionId);
  await auth.close();
  console.log(`project ${project.name}: latest is r${headBefore?.number}`);

  // Desktop, dark: edit, break a line, fix it, apply.
  {
    const { ctx, page } = await context(browser, state, 'dark', false);
    await openSheet(page, project.id);
    await waitStatus(page, /no changes|ready|problem/);
    check(await page.getByRole('tab', { name: 'Script' }).getAttribute('aria-selected') === 'true', 'the Script tab is open');
    check(await page.getByRole('radio', { name: 'Edit' }).getAttribute('aria-checked') === 'true', 'the Edit view is chosen');
    check((await page.locator('.cm-cs-time').count()) > 3 && (await page.locator('.cm-cs-clip').count()) > 1, 'times and clip names are marked in the editor');
    await shot(page, 'open-desktop-dark');

    // A broken line shows an inline error with a plain message.
    const clip1 = await reveal(page, '1. ');
    const original = (await clip1.innerText()).trim();
    await clip1.click();
    await lineEnd(page);
    await page.keyboard.press('Enter');
    await page.keyboard.type('2. oops: nowhere 0:01 → 0:02');
    await waitStatus(page, /problem/);
    await page.locator('.cm-lintRange-error').first().waitFor({ timeout: 10_000 });
    const problemsPanel = page.getByRole('region', { name: 'Problems' });
    check(/no footage called "nowhere"/.test(await problemsPanel.innerText()), 'a broken line is reported in plain words');
    check((await page.getByRole('button', { name: 'Apply changes' }).isDisabled()) === true, 'Apply is off while there are problems');
    await shot(page, 'broken-line-desktop-dark');
    await page.locator('.cm-lintRange-error').first().hover();
    await page.locator('.cm-tooltip-lint').waitFor({ timeout: 5_000 }).catch(() => {});
    await shot(page, 'broken-line-tooltip-desktop-dark');
    for (let i = 0; i < 4; i++) await page.keyboard.press(process.platform === 'darwin' ? 'Meta+z' : 'Control+z');
    await page.waitForTimeout(300);
    check((await clip1.innerText()).trim() === original && (await line(page, '2. oops').count()) === 0, 'undo removes the broken line');

    // Change the first clip's out time: half a second shorter, or 1.5 s longer
    // when the clip is already short (so repeated runs keep making a change).
    const m = original.match(/(\d+:\d{2}(?:\.\d+)?) → (\d+:\d{2}(?:\.\d+)?)/);
    if (!m) throw new Error(`first clip line has no start and end times: ${original}`);
    const secs = (s) => {
      const [mm, ss] = s.split(':');
      return Number(mm) * 60 + Number(ss);
    };
    const clock = (t) => {
      const s = (t % 60).toFixed(1).replace(/\.0$/, '');
      return `${Math.floor(t / 60)}:${s.padStart(s.includes('.') ? 4 : 2, '0')}`;
    };
    const out = m[2];
    const newOut = clock(secs(out) - secs(m[1]) > 1.5 ? secs(out) - 0.5 : secs(out) + 1.5);
    await clip1.click();
    await lineEnd(page);
    const lineText = (await clip1.innerText()).trim();
    const tail = lineText.slice(lineText.indexOf(out) + out.length);
    for (let i = 0; i < tail.length; i++) await page.keyboard.press('ArrowLeft');
    for (let i = 0; i < out.length; i++) await page.keyboard.press('Backspace');
    await page.keyboard.type(newOut);
    await waitStatus(page, /ready/);
    const changes = await page.getByRole('region', { name: 'What changes' }).innerText();
    check(changes.includes(`ends at ${newOut}`), `the change list says the clip now ends at ${newOut}`);
    await shot(page, 'edited-desktop-dark');

    await page.getByRole('button', { name: 'Apply changes' }).click();
    const dlg = page.getByRole('dialog');
    await dlg.waitFor();
    await dlg.getByLabel('What did you change? (optional)').fill('Change where the first clip ends');
    await shot(page, 'apply-dialog-desktop-dark');
    await dlg.getByRole('button', { name: /^Save as r\d+/ }).click();
    await page.getByText(/^Saved as r\d+/).waitFor({ timeout: 20_000 });
    await page.waitForTimeout(1200);
    await waitStatus(page, /no changes/);
    const after = await api(page, 'project.show', { id: project.id });
    const headAfter = after.revisions.find((r) => r.id === after.headRevisionId);
    check(headAfter.number === (headBefore?.number ?? 0) + 1, `a new revision r${headAfter.number} exists`);
    check(headAfter.instruction === 'Change where the first clip ends', 'the revision records what was changed');
    check((await line(page, 'base:').innerText()).includes(headAfter.id), 'the editor now holds the new revision');
    check((await page.locator('select').first().inputValue()) === headAfter.id, 'the version picker shows the new latest');
    await shot(page, 'applied-desktop-dark');

    // An older revision opens read-only.
    await page.locator('select').first().selectOption(before.headRevisionId);
    await waitStatus(page, /read only/);
    check((await page.locator('.cm-content').getAttribute('contenteditable')) === 'false', 'an older version cannot be edited');
    check((await page.getByRole('button', { name: 'Edit the latest' }).isVisible()) === true, 'it offers to edit the latest instead');
    await shot(page, 'read-only-desktop-dark');

    // Compare two revisions as cut sheets.
    await page.getByRole('button', { name: 'Compare versions' }).click();
    await page.locator('[role="table"][aria-label="Changed lines"]').waitFor({ timeout: 15_000 });
    check((await page.locator('[role="row"].bg-ok-soft').count()) > 0 && (await page.locator('[role="row"].bg-danger-soft').count()) > 0, 'the diff shows removed and added lines');
    check((await page.getByRole('region', { name: 'What changed' }).innerText()).includes(newOut), 'the comparison says what changed in words');
    await shot(page, 'compare-desktop-dark');

    // Formatted: the cut read like a script.
    await openScript(page, project.id);
    check(await page.getByRole('radio', { name: 'Formatted' }).getAttribute('aria-checked') === 'true', 'Formatted is the default view');
    const sheet = (await api(page, 'cut.show', { projectId: project.id })).sheet;
    const clipLines = sheet.split('\n').filter((l) => /^\d+\. /.test(l)).length;
    check((await page.locator('[data-script-view] [data-clip]').count()) === clipLines, `every clip line is a clip row (${clipLines})`);
    check((await page.locator('[data-script-view] [data-as-written]').count()) === 0, 'nothing in the script is left unrecognized');
    const summaryText = await page.getByRole('region', { name: 'About this cut' }).innerText();
    check(!/ast_|prj_|rev_|sha256:/.test(summaryText), 'the summary shows no IDs or hashes');
    await page.getByRole('region', { name: 'About this cut' }).getByText('Details').click();
    check(/ast_/.test(await page.locator('[data-settings-text]').innerText()), 'Details shows the settings as written');
    await page.getByRole('region', { name: 'About this cut' }).getByText('Details').click();
    if (await page.locator('[data-script-view] [data-dialogue]').count()) check((await page.locator('[data-script-view] [data-dialogue]').first().innerText()).length > 3, 'clip rows show what is said');
    await shot(page, 'script-formatted-desktop-dark');

    // An older version, by link (?rev=).
    await openScript(page, project.id, `&rev=${before.headRevisionId}`);
    check((await page.locator('select').first().inputValue()) === before.headRevisionId, 'the version picker shows the older version');
    check((await page.getByRole('button', { name: 'Show the latest' }).isVisible()) === true, 'an older version offers the latest');
    await shot(page, 'script-older-desktop-dark');

    // Markdown: the same sheet as text, read only.
    await page.getByRole('radio', { name: 'Markdown' }).click();
    await page.locator('.cm-content').waitFor({ timeout: 15_000 });
    check((await page.locator('.cm-content').getAttribute('contenteditable')) === 'false', 'the Markdown view is read only');
    check((await page.locator('.cm-cs-time').count()) > 3, 'the Markdown view is highlighted');
    await shot(page, 'script-markdown-desktop-dark');

    // The old tab name still opens the script.
    await page.goto(`${base}/p/${project.id}?tab=sheet`);
    await page.locator('[data-script-view]').waitFor({ timeout: 15_000 });
    check(await page.getByRole('tab', { name: 'Script' }).getAttribute('aria-selected') === 'true', 'the old tab name (?tab=sheet) opens the Script tab');

    // A clip plays the cut from there; "View script" comes back.
    await openScript(page, project.id);
    const second = page.locator('[data-script-view] [data-clip]').nth(1);
    const clipId = (await second.count()) ? await second.getAttribute('data-clip') : await page.locator('[data-script-view] [data-clip]').first().getAttribute('data-clip');
    await page.locator(`[data-script-view] [data-clip="${clipId}"] [data-play-clip]`).click();
    await page.waitForURL((u) => u.searchParams.get('tab') === 'cut', { timeout: 10_000 });
    await page.getByRole('region', { name: 'Editorial treatment' }).waitFor({ timeout: 15_000 });
    await page.waitForFunction(() => !new URL(location.href).searchParams.get('clip'), null, { timeout: 15_000 });
    await page.waitForTimeout(800);
    const specClip = (await api(page, 'cut.show', { projectId: project.id, format: 'json' })).spec.clips.find((c) => c.id === clipId);
    const pressed = await page.locator('button[aria-pressed="true"][aria-label^="Clip "]').first().getAttribute('aria-label');
    check(pressed?.startsWith(`Clip ${specClip?.label || clipId},`), `the cut opens at clip ${clipId} (${pressed})`);
    await shot(page, 'script-play-clip-desktop-dark');
    await page.locator('[data-view-script]').first().click();
    await page.locator('[data-script-view]').waitFor({ timeout: 15_000 });
    check(true, 'View script on the current cut opens the script');
    await page.goto(`${base}/p/${project.id}?tab=history`);
    await page.locator('[data-view-script]').first().waitFor({ timeout: 15_000 });
    check((await page.locator('[data-view-script]').count()) === before.revisions.length + 1, 'every version in Revisions has View script');
    await ctx.close();
  }

  // The other themes and widths for the same screens (read-only checks).
  for (const [theme, mobile] of [
    ['light', false],
    ['dark', true],
    ['light', true],
  ]) {
    const tag = `${mobile ? 'mobile' : 'desktop'}-${theme}`;
    const { ctx, page } = await context(browser, state, theme, mobile);
    await openSheet(page, project.id);
    await waitStatus(page, /no changes|ready|problem/);
    await shot(page, `open-${tag}`);
    const clip1 = await reveal(page, '1. ');
    await clip1.click();
    await lineEnd(page);
    await page.keyboard.press('Enter');
    await page.keyboard.type('2. oops: nowhere 0:01 → 0:02');
    await waitStatus(page, /problem/);
    await page.locator('.cm-lintRange-error').first().waitFor({ timeout: 10_000 });
    await shot(page, `broken-line-${tag}`);
    await openScript(page, project.id);
    await shot(page, `script-formatted-${tag}`);
    await page.getByRole('radio', { name: 'Markdown' }).click();
    await page.locator('.cm-content').waitFor({ timeout: 15_000 });
    await page.waitForTimeout(500);
    await shot(page, `script-markdown-${tag}`);
    await page.goto(`${base}/p/${project.id}?tab=script&mode=compare`);
    await page.locator('[role="table"][aria-label="Changed lines"]').waitFor({ timeout: 15_000 });
    await page.waitForTimeout(600);
    await shot(page, `compare-${tag}`);
    await ctx.close();
  }

  await browser.close();
  console.log(`\n${shots.length} screenshots:`);
  for (const s of shots) console.log(`  ${s}`);
  if (problems.length) {
    console.log(`\n${problems.length} problem(s):`);
    for (const p of problems) console.log(`  ${p}`);
    process.exitCode = 1;
  } else console.log('\nNo console errors, page errors or horizontal overflow.');
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
