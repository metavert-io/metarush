#!/usr/bin/env node
// Content Credentials in delivery in a real browser (D415–D424). Run it through
// scripts/content-credentials-e2e.sh: a throwaway installation with an approved-to-be
// version whose final render is checked, and a c2patool (the stand-in, or the real
// one). It checks that:
//   1. Settings → Delivery says Content Credentials are off, and an owner makes them
//      required in one click; this computer says it can't sign them yet;
//   2. the version's page is held for them, with Set them up, which opens
//      Settings → Delivery;
//   3. Make a test certificate… asks in the styled dialog and says what a test
//      certificate is; this computer then signs, and Try it on a sample checks out;
//   4. the version's page is ready, and Deliver… delivers it into a new folder,
//      saying the video carries Content Credentials;
//   5. the Receipt tab lists the signed video and the project's Settings tab has the
//      Content Credentials part;
//   6. no native dialogs, page errors or horizontal overflow; plain words.
// Screenshots: desktop and phone, dark and light, in CREDENTIALS_SHOTS.
// It CHANGES the workspace, so point it only at a throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'CREDENTIALS_PROJECT', 'CREDENTIALS_REVISION', 'CREDENTIALS_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/content-credentials-e2e.sh does)`);
    process.exit(2);
  }
}
const projectId = process.env.CREDENTIALS_PROJECT;
const rev1 = process.env.CREDENTIALS_REVISION;
const shotsDir = process.env.CREDENTIALS_SHOTS;
mkdirSync(shotsDir, { recursive: true });
const ORG = 'Example Studio';

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

/** The text of the newest toast once one says something matching re. */
async function toastSaying(page, re, timeout = 60_000) {
  const toast = page.locator('[data-toasts] [role="status"], [data-toasts] [role="alert"]').filter({ hasText: re });
  await toast.first().waitFor({ timeout });
  return toast.first().innerText();
}

async function main() {
  const browser = await chromium.launch();
  let { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const state = await ctx.storageState();

  // Approving is a person's: the person signed in here approves the version.
  await api(page, 'revision.approve', { id: rev1 });

  // 1. Settings → Delivery: off until an owner turns them on; required in one click.
  await page.goto(`${base}/settings/delivery#content-credentials`);
  const settings = page.locator('[data-content-credentials-settings]');
  await settings.waitFor({ timeout: 30_000 });
  const summary = settings.locator('[data-credentials-summary]');
  check('Settings → Delivery: off until an owner turns them on', /without Content Credentials/.test(await summary.innerText()), await summary.innerText());
  await shot(page, 'settings-off-desktop-dark');
  await settings.getByRole('radio', { name: 'Required' }).click();
  await page.waitForFunction(() => /can.t sign waits/.test(document.querySelector('[data-credentials-summary]')?.textContent ?? ''), null, { timeout: 30_000 });
  check('Required, chosen by the owner', (await settings.getByRole('radio', { name: 'Required' }).getAttribute('aria-checked')) === 'true');
  const hereCard = settings.locator('[data-credentials-here]');
  const notYet = await hereCard.locator('[data-credentials-says]').innerText();
  check("this computer says it can't sign them yet, and why", (await hereCard.getAttribute('data-ready')) === 'no' && /no signing certificate is set up/.test(notYet), notYet);

  // 2. The version's page is held for them, and Set them up opens Settings → Delivery.
  await page.goto(`${base}/p/${projectId}/r/${rev1}`);
  const gate = page.locator('[data-ship-gate]');
  await gate.locator('[data-ship-headline]').waitFor({ timeout: 30_000 });
  await page.waitForFunction(() => document.querySelector('[data-ship-gate] [data-hold="credentials"]') !== null, null, { timeout: 30_000 });
  const held = await gate.locator('[data-hold="credentials"]').innerText();
  check('the version is held: its videos must carry Content Credentials', (await gate.getAttribute('data-state')) === 'held' && /must carry Content Credentials/.test(held) && /Set them up/.test(held), held);
  check('nothing else holds it', (await gate.locator('[data-hold]').count()) === 1, await gate.locator('[data-ship-headline]').innerText());
  const gateFindings = await unplainIn(gate);
  check('the hold reads plainly', gateFindings.length === 0, gateFindings.join(' | '));
  await gate.scrollIntoViewIfNeeded();
  await shot(page, 'held-desktop-dark');
  await gate.locator('[data-hold="credentials"]').getByRole('link', { name: 'Set them up' }).click();
  await page.waitForURL((u) => u.pathname === '/settings/delivery', { timeout: 20_000 });
  check('Set them up opens Settings → Delivery', page.url().includes('/settings/delivery'), page.url());

  // 3. A test certificate, made in the styled dialog; then a sample, signed and checked.
  await settings.waitFor({ timeout: 30_000 });
  await settings.locator('[data-credentials-make-test]').click();
  const dialog = page.getByRole('dialog');
  await dialog.waitFor({ timeout: 10_000 });
  const dialogText = await dialog.innerText();
  check('Make a test certificate asks in a styled dialog and says what it is', /verifiers show the signer as unknown/.test(dialogText) && /never for a client/.test(dialogText), dialogText.slice(0, 300));
  const dialogFindings = await unplainIn(dialog);
  check('the dialog reads plainly', dialogFindings.length === 0, dialogFindings.join(' | '));
  await shot(page, 'test-certificate-dialog-desktop-dark');
  await dialog.locator('input').fill(ORG);
  await dialog.getByRole('button', { name: 'Make it', exact: true }).click();
  await page.waitForFunction(() => document.querySelector('[data-credentials-here]')?.getAttribute('data-ready') === 'yes', null, { timeout: 60_000 });
  const signs = await hereCard.locator('[data-credentials-says]').innerText();
  check('this computer now signs, with a test certificate it names as one', /signs with a test certificate/.test(signs) && /not for a client/.test(signs), signs);
  check('the card marks the test certificate', (await hereCard.getByText('Test certificate', { exact: true }).count()) === 1);
  await settings.locator('[data-credentials-try]').click();
  const tried = await toastSaying(page, /sample was signed as|don.t check out/); // (setting it up said "A sample was signed and checked.")
  check('Try it on a sample: signed and checked', /its Content Credentials check out/.test(tried) && /test certificate/.test(tried), tried);
  const settingsFindings = await unplainIn(settings);
  check('Settings → Delivery reads plainly', settingsFindings.length === 0, settingsFindings.join(' | '));
  await settings.scrollIntoViewIfNeeded();
  await shot(page, 'settings-ready-desktop-dark');

  // 4. Ready to deliver; Deliver… delivers it, signed.
  await page.goto(`${base}/p/${projectId}/r/${rev1}`);
  await page.waitForFunction(() => document.querySelector('[data-ship-gate]')?.getAttribute('data-state') === 'ready', null, { timeout: 30_000 });
  check('nothing holds it now', (await gate.locator('[data-hold]').count()) === 0, await gate.locator('[data-ship-headline]').innerText());
  await gate.locator('[data-ship-deliver]').click();
  const deliver = page.getByRole('dialog');
  await deliver.waitFor({ timeout: 10_000 });
  check('Deliver… says what it does before it does it', /Content Credentials where they are on/.test(await deliver.innerText()) && /Nothing is uploaded/.test(await deliver.innerText()));
  await shot(page, 'deliver-dialog-desktop-dark');
  await deliver.locator('[data-deliver-go]').click();
  await deliver.locator('[data-deliver-done]').waitFor({ timeout: 180_000 });
  const doneText = await deliver.innerText();
  const signed = deliver.locator('[data-deliver-credentials="signed"]');
  check('the delivered video carries Content Credentials, signed and checked', (await signed.count()) === 1 && /carries Content Credentials/.test(await signed.innerText()), doneText.slice(0, 400));
  check('the delivery says so in a sentence', /carries Content Credentials/.test(doneText), doneText.slice(0, 300));
  const doneFindings = await unplainIn(deliver.locator('[data-deliver-done] p, [data-deliver-credentials]'));
  check('what delivery says reads plainly', doneFindings.length === 0, doneFindings.join(' | '));
  await shot(page, 'delivered-desktop-dark');
  await deliver.getByRole('button', { name: 'Done', exact: true }).click();
  await deliver.waitFor({ state: 'detached', timeout: 10_000 }).catch(() => {});
  const ready = await api(page, 'credentials.status', { projectId });
  check('the status says required, and this computer is ready', ready.mode === 'required' && ready.here.ready && ready.here.certificate?.test === true);

  // 5. The receipt and the project's own part.
  await page.goto(`${base}/p/${projectId}?tab=receipt`);
  const rc = page.locator('[data-receipt-signed]');
  await rc.waitFor({ timeout: 30_000 });
  const rcText = await rc.innerText();
  check('the receipt lists the signed video', /Version 1/.test(rcText) && /test certificate/.test(rcText) && /Checked/.test(rcText) && (await rc.locator('[data-receipt-signed-file]').count()) === 1, rcText.slice(0, 300));
  const rcFindings = await unplainIn(rc);
  check('the receipt reads plainly', rcFindings.length === 0, rcFindings.join(' | '));
  await rc.scrollIntoViewIfNeeded();
  await shot(page, 'receipt-desktop-dark');
  await page.goto(`${base}/p/${projectId}?tab=settings`);
  const part = page.locator('[data-credentials-part]');
  await part.waitFor({ timeout: 30_000 }).catch(() => {});
  check("the project's Content Settings have Content Credentials", (await part.count()) === 1);
  if ((await part.count()) === 1) {
    await part.scrollIntoViewIfNeeded();
    const partFindings = await unplainIn(part);
    check('the part reads plainly', partFindings.length === 0, partFindings.join(' | '));
  }
  await shot(page, 'project-part-desktop-dark');
  await ctx.close();

  // Phone and light.
  for (const theme of ['dark', 'light']) {
    for (const mobile of [false, true]) {
      if (!mobile && theme === 'dark') continue;
      const tag = `${mobile ? 'phone' : 'desktop'}-${theme}`;
      const v = await newPage(browser, { theme, mobile, state });
      await v.page.goto(`${base}/settings/delivery#content-credentials`);
      await v.page.locator('[data-content-credentials-settings]').waitFor({ timeout: 30_000 });
      await v.page.locator('[data-content-credentials-settings]').scrollIntoViewIfNeeded();
      await shot(v.page, `settings-${tag}`);
      await v.page.goto(`${base}/p/${projectId}/r/${rev1}`);
      await v.page.locator('[data-ship-gate][data-state="ready"]').waitFor({ timeout: 30_000 });
      await v.page.locator('[data-ship-gate]').scrollIntoViewIfNeeded();
      await shot(v.page, `version-${tag}`);
      await v.page.goto(`${base}/p/${projectId}?tab=receipt`);
      await v.page.locator('[data-receipt-signed]').waitFor({ timeout: 30_000 });
      await v.page.locator('[data-receipt-signed]').scrollIntoViewIfNeeded();
      await shot(v.page, `receipt-${tag}`);
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
