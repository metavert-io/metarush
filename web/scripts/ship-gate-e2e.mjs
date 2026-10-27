#!/usr/bin/env node
// The ship gate and promises made to the client in a real browser (D298–D302).
// Run it through scripts/ship-gate-e2e.sh: a throwaway installation whose new
// workspace holds versions back, a captioned cut with two promises (one kept,
// one broken) and a sign-off, its final render checked on this computer.
// It checks that:
//   1. Settings → General → Before it goes out says versions are held back;
//   2. the version's page says Held by …: the quality check's problem (the
//      promise not kept, with its measure and the step to fix it) and the
//      sign-off, and the promises as measured;
//   3. Tick it clears the sign-off in one click (a person) and Deliver anyway…
//      takes a note in the styled dialog: the page says it was sent out anyway,
//      by whom and why;
//   4. the project's Settings tab shows Before it goes out and the promises in
//      its rules; the Receipt tab lists what went out anyway;
//   5. no native dialogs, page errors or horizontal overflow; plain words.
// Screenshots: desktop and phone, dark and light, in SHIPGATE_SHOTS.
// It CHANGES the project, so point it only at a throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'SHIPGATE_PROJECT', 'SHIPGATE_REVISION', 'SHIPGATE_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/ship-gate-e2e.sh does)`);
    process.exit(2);
  }
}
const projectId = process.env.SHIPGATE_PROJECT;
const rev1 = process.env.SHIPGATE_REVISION;
const shotsDir = process.env.SHIPGATE_SHOTS;
mkdirSync(shotsDir, { recursive: true });
const NOTE = 'The client needs it today; the next version will be shorter.';

const results = [];
const problems = [];
function check(name, ok, detail = '') {
  results.push({ name, ok: !!ok, detail });
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

async function newPage(browser, { theme = 'dark', mobile = false, state } = {}) {
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
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) problems.push(`console: ${m.text()}`);
  });
  page.on('dialog', (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  return { ctx, page };
}

let shotN = 0;
async function shot(page, name) {
  await page.waitForTimeout(400);
  const file = path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file, fullPage: false });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
}

/** Plain-words findings in what is on screen (people's own words aside). */
async function unplainIn(locator) {
  const texts = await locator.allInnerTexts();
  return texts.flatMap((t) => t.split('\n')).flatMap((l) => checkShown(l).map((f) => `${f.kind} ${f.text} in ${JSON.stringify(l.slice(0, 120))}`));
}

async function main() {
  const browser = await chromium.launch();
  let { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const state = await ctx.storageState();

  // 1. The workspace's switch.
  await page.goto(`${base}/settings/general#ship-gate`);
  const sw = page.locator('[data-ship-gate-settings]');
  await sw.waitFor({ timeout: 30_000 });
  await sw.scrollIntoViewIfNeeded();
  const pressed = await sw.getByRole('radio', { name: 'Hold versions back' }).getAttribute('aria-checked').catch(() => null);
  const swText = await sw.innerText();
  check('Settings → General: a new workspace holds versions back', pressed === 'true' || /wait for the quality check/.test(swText), swText.slice(0, 200));
  await shot(page, 'settings-desktop-dark');

  // 2. The version's page: approved by a person first (approving is theirs).
  await api(page, 'revision.approve', { id: rev1 });
  await page.goto(`${base}/p/${projectId}/r/${rev1}`);
  const gate = page.locator('[data-ship-gate]');
  await gate.waitFor({ timeout: 30_000 });
  await gate.locator('[data-ship-headline]').waitFor();
  const headline = await gate.locator('[data-ship-headline]').innerText();
  check('the version is held by two things', /^Held by 2 things$/.test(headline) && (await gate.getAttribute('data-state')) === 'held', headline);
  const problemsText = await gate.locator('[data-hold="qa-errors"]').innerText();
  check("the quality check's problem is the promise not kept, with its measure", /“Under 3 seconds” isn't kept: it lasts/.test(problemsText) && /Promise/.test(problemsText), problemsText.slice(0, 300));
  check('the sign-off holds it too', (await gate.locator('[data-hold="signoff"]').count()) === 1);
  const promisesText = await gate.locator('[data-ship-promises]').innerText();
  check('the promises as measured: one not kept, one kept', /Not kept/.test(promisesText) && /Kept/.test(promisesText) && /Captions on every video/.test(promisesText), promisesText.slice(0, 300));
  const gateFindings = await unplainIn(gate);
  check('the gate reads plainly', gateFindings.length === 0, gateFindings.join(' | '));
  await gate.scrollIntoViewIfNeeded();
  await shot(page, 'held-desktop-dark');

  // 3. Tick the sign-off in one click; then deliver anyway with a note.
  await gate.locator('[data-hold="signoff"] [data-hold-path="cut.signoffs.done"]').click();
  await page.waitForFunction(() => document.querySelectorAll('[data-ship-gate] [data-hold="signoff"]').length === 0, null, { timeout: 30_000 });
  const after = await gate.locator('[data-ship-headline]').innerText();
  check('ticking the sign-off clears that hold', /^Held by 1 thing$/.test(after), after);
  await gate.locator('[data-ship-anyway]').click();
  const dialog = page.getByRole('dialog');
  await dialog.waitFor({ timeout: 10_000 });
  const dialogText = await dialog.innerText();
  check('Deliver anyway asks in a styled dialog what holds it and why', /isn't kept/.test(dialogText) && /shows on the receipt/.test(dialogText), dialogText.slice(0, 300));
  await shot(page, 'anyway-dialog-desktop-dark');
  await dialog.locator('textarea').fill(NOTE);
  await dialog.getByRole('button', { name: 'Deliver anyway', exact: true }).click();
  await page.waitForFunction(() => document.querySelector('[data-ship-gate]')?.getAttribute('data-state') === 'sent-anyway', null, { timeout: 30_000 });
  const sent = await gate.innerText();
  check('the page says it was sent out anyway, by whom and why', /sent out anyway by/i.test(sent) && /'s note/.test(sent) && sent.includes(NOTE), sent.slice(0, 300));
  const ready = await api(page, 'delivery.ready', { projectId, revision: rev1 });
  check('the gate agrees: ready, sent out anyway, the note kept with who and when', ready.ready && ready.state === 'sent-anyway' && ready.override?.note === NOTE && !!ready.override?.by?.userId);
  await shot(page, 'sent-anyway-desktop-dark');

  // 4. The project's Settings tab and the Receipt tab.
  await page.goto(`${base}/p/${projectId}?tab=settings`);
  const part = page.locator('[data-gate-part]');
  await part.waitFor({ timeout: 30_000 }).catch(() => {});
  check("the project's Content Settings have Before it goes out", (await part.count()) === 1);
  const promiseRules = page.locator('[data-rule="promise"]');
  check("the project's rules mark its promises", (await promiseRules.count()) === 2, `${await promiseRules.count()} promises`);
  await promiseRules.first().scrollIntoViewIfNeeded().catch(() => {});
  await shot(page, 'settings-tab-desktop-dark');
  await page.goto(`${base}/p/${projectId}?tab=receipt`);
  const rc = page.locator('[data-receipt-sent-anyway]');
  await rc.waitFor({ timeout: 30_000 });
  const rcText = await rc.innerText();
  check('the receipt lists what went out anyway, with the note and what held it', rcText.includes(NOTE) && /Version 1/.test(rcText) && /quality check found 1 problem/.test(rcText), rcText.slice(0, 300));
  const rcFindings = await unplainIn(rc);
  check('the receipt reads plainly', rcFindings.length === 0, rcFindings.join(' | '));
  await rc.scrollIntoViewIfNeeded();
  await shot(page, 'receipt-desktop-dark');
  await ctx.close();

  // Phone and light.
  for (const theme of ['dark', 'light']) {
    for (const mobile of [false, true]) {
      if (!mobile && theme === 'dark') continue;
      const tag = `${mobile ? 'phone' : 'desktop'}-${theme}`;
      const v = await newPage(browser, { theme, mobile, state });
      await v.page.goto(`${base}/p/${projectId}/r/${rev1}`);
      await v.page.locator('[data-ship-gate][data-state="sent-anyway"]').waitFor({ timeout: 30_000 });
      await v.page.locator('[data-ship-gate]').scrollIntoViewIfNeeded();
      await shot(v.page, `version-${tag}`);
      await v.page.goto(`${base}/settings/general#ship-gate`);
      await v.page.locator('[data-ship-gate-settings]').waitFor({ timeout: 30_000 });
      await v.page.locator('[data-ship-gate-settings]').scrollIntoViewIfNeeded();
      await shot(v.page, `settings-${tag}`);
      await v.ctx.close();
    }
  }
  await browser.close();

  check('no native dialogs, page errors or horizontal overflow', problems.length === 0, problems.join(' | '));
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed; screenshots in ${shotsDir}`);
  if (failed.length) process.exitCode = 1;
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
