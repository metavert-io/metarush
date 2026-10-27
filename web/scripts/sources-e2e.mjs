#!/usr/bin/env node
// End-to-end check of descriptive names, the info card, fixes and Generate video in the drop box (D150–D154) in a
// real browser, against a daemon whose Gemini endpoint is the fake API. Run it through scripts/sources-e2e.sh, which
// sets up the installation, the footage and the fake.
//
//   the info card: the clip's name (MetaRush's, from the day it was recorded) and its original file, where it came
//     from, what MetaRush found, where it's used, and its fixes with the ones that fit marked
//   renaming in place keeps the file name on the card
//   Steady the camera: ticked, the fixed copy made by the dashboard's worker, before and after, "Use the steadier
//     picture in …" saves a new version, unticking goes back to the original while that version keeps the copy
//   Steady the camera for a call recording (D190): offered and marked, measured between the picture's switches, made
//     one stretch between switches at a time
//   Use all: MetaRush's better names; the cut's name (MetaRush's suggestion) and a version's note
//   a picture's fixes in Resources
//   Generate video from Home's drop box: the project used last chosen, a shot that starts from a photo
//   screenshots: desktop light and dark, a phone; no page errors, no native dialogs, no sideways scrolling on a phone
import { execFileSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5355').replace(/\/$/, '');
const missing = ['METARUSH_MINT', 'SRC_PROJECT', 'SRC_SHAKY', 'SRC_VOICE', 'SRC_INTERVIEW', 'SRC_CALL', 'SRC_PHOTO', 'SRC_SHOTS'].filter((k) => !process.env[k]);
if (missing.length) {
  console.error(`set ${missing.join(', ')} (scripts/sources-e2e.sh does)`);
  process.exit(2);
}
const P = process.env.SRC_PROJECT;
const SHAKY = process.env.SRC_SHAKY;
const CALL = process.env.SRC_CALL;
const SHOTS = process.env.SRC_SHOTS;
mkdirSync(SHOTS, { recursive: true });

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
}

function mintLink() {
  const [bin, ...pre] = process.env.METARUSH_MINT.split(' ');
  const out = execFileSync(bin, pre, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return `${base}/auth${new URL(m[0]).search}`;
}

async function op(page, name, input = {}) {
  return page.evaluate(
    async ([name, input]) => {
      const r = await fetch(`/api/ops/${name}`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' },
        body: JSON.stringify(input),
      });
      const j = await r.json();
      return j.ok ? { ok: true, data: j.data } : { ok: false, code: j.error?.code, message: j.error?.message };
    },
    [name, input],
  );
}
async function api(page, name, input = {}) {
  const r = await op(page, name, input);
  if (!r.ok) throw new Error(`${name}: ${r.code} ${r.message}`);
  return r.data;
}

async function waitFor(fn, what, timeout = 90_000) {
  const end = Date.now() + timeout;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 750));
  }
}

async function toast(page, text) {
  await page.getByText(text).first().waitFor({ timeout: 30_000 });
}

/** Lets the toasts go (they sit over the card's corner). */
async function quiet(page) {
  await page.mouse.move(0, 0);
  await page.waitForTimeout(4500);
}

async function shot(page, name, locator) {
  const file = path.join(SHOTS, `${name}.png`);
  if (locator) await locator.screenshot({ path: file });
  else await page.screenshot({ path: file, fullPage: true });
  console.log(`     screenshot ${file}`);
}

/** The dashboard's own theme setting (it doesn't follow the system's). */
async function theme(page, t) {
  await page.evaluate((t) => {
    localStorage.setItem('metarush.theme', t);
    document.documentElement.setAttribute('data-theme', t);
  }, t);
  await page.waitForTimeout(300);
}

async function head(page) {
  const show = await api(page, 'project.show', { id: P });
  return api(page, 'revision.show', { id: show.headRevisionId });
}

async function main() {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 }, colorScheme: 'light' });
  const page = await ctx.newPage();
  const problems = [];
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) problems.push(`console: ${m.text()}`);
  });
  page.on('dialog', (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  // The shot's request and answer, with times (a loaded machine can be slow to answer).
  page.on('request', (r) => {
    if (r.url().includes('/api/ops/generate.shot')) console.log(`     ${new Date().toISOString()} asked: ${(r.postData() ?? '').slice(0, 160)}`);
  });
  page.on('response', (r) => {
    if (r.url().includes('/api/ops/generate.shot')) console.log(`     ${new Date().toISOString()} answered: ${r.status()}`);
  });
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
  await theme(page, 'light');
  // SRC_ONLY=generate goes straight to Generate video (a second run against a kept installation).
  const only = process.env.SRC_ONLY ?? '';

  if (!only) {
  // ---------- the info card ----------
  await page.goto(`${base}/p/${P}?tab=sources&asset=${SHAKY}`);
  const card = page.locator('[data-info-card]');
  await card.waitFor({ timeout: 30_000 });
  const cardText = (await card.textContent()) ?? '';
  check('named from the day it was recorded', /Video from 12 Oct 2026/.test(cardText), cardText.slice(0, 120));
  check('the original file is on the card', (await card.locator('[data-file-name]').textContent())?.includes('IMG_4821.MOV') ?? false);
  check('where it came from and what MetaRush found', /Where it came from/.test(cardText) && /What MetaRush found/.test(cardText) && /Recorded 12 Oct 2026/.test(cardText));
  check('where it is used', /Where it.s used/.test(cardText) && /Garden film/.test(cardText));
  const steadyRow = card.locator('[data-fix="steady"]');
  check('Steady the camera is marked, with why', (await steadyRow.getAttribute('data-fits')) === 'true' && /The camera shakes/.test((await steadyRow.textContent()) ?? ''), (await steadyRow.textContent()) ?? '');
  await shot(page, 'info-card-desktop-light', card);

  // ---------- renaming keeps the file name ----------
  await page.locator('section[aria-label^="Clip "] h2').first().click();
  const titleInput = page.getByLabel('Title for IMG_4821.MOV');
  await titleInput.fill('Garden walk');
  await titleInput.press('Enter');
  await waitFor(async () => /Garden walk/.test((await card.textContent()) ?? ''), 'the card to show the new name', 20_000);
  check('renamed in place; the file name stays on the card', /Named by a person/.test((await card.textContent()) ?? '') && ((await card.locator('[data-file-name]').textContent()) ?? '').includes('IMG_4821.MOV'));

  // ---------- Steady the camera: a fixed copy, before and after, a new version ----------
  await steadyRow.locator('input[type=checkbox]').check();
  await toast(page, 'Making the fixed copy');
  const fixes = card.locator('[data-fixes-panel]');
  await waitFor(async () => (await fixes.getAttribute('data-fix-state')) === 'ready' && /Uses its fixed copy/.test((await fixes.locator('[data-fix-summary]').textContent()) ?? ''), 'the fixed copy', 240_000);
  const after = (await steadyRow.textContent()) ?? '';
  check('the steadied copy is measured before and after', /→/.test(after), after);
  await card.locator('[data-before-after]').click();
  const ba = page.getByRole('dialog');
  await ba.locator('video').first().waitFor();
  check('before and after side by side', (await ba.locator('video').count()) === 2);
  await shot(page, 'before-after', ba);
  await page.keyboard.press('Escape');
  const offer = card.locator('[data-fix-cuts] [data-use-fixed]');
  await offer.waitFor({ timeout: 20_000 });
  const offerText = (await offer.textContent()) ?? '';
  check('the cut is offered the steadier picture', /^Use the steadier picture in “/.test(offerText.trim()), offerText);
  const before = await head(page);
  await offer.click();
  await toast(page, /Saved as version 2/);
  const v2 = await head(page);
  check('a new version plays the fixed copy; the saved one is unchanged', v2.summary.number === 2 && !!v2.spec.fixed?.[SHAKY]?.sha256 && !before.spec.fixed, JSON.stringify(v2.spec.fixed ?? {}));
  await quiet(page);
  await shot(page, 'fixes-after-steady', fixes);

  // Untick: the original again; version 2 keeps its copy.
  await steadyRow.locator('input[type=checkbox]').uncheck();
  await waitFor(async () => /Uses the original\./.test((await fixes.locator('[data-fix-summary]').textContent()) ?? ''), 'the original again', 30_000);
  const v2again = await api(page, 'revision.show', { id: v2.summary.id });
  check('unticking goes back to the original; version 2 keeps the copy', v2again.spec.fixed?.[SHAKY]?.sha256 === v2.spec.fixed[SHAKY].sha256);

  // ---------- Steady the camera for a call recording, one picture at a time (D190) ----------
  await page.goto(`${base}/p/${P}?tab=sources&asset=${CALL}`);
  const callCard = page.locator('[data-info-card]');
  await callCard.waitFor({ timeout: 30_000 });
  const callFixes = callCard.locator('[data-fixes-panel]');
  const callSteady = callCard.locator('[data-fix="steady"]');
  await callSteady.waitFor({ timeout: 20_000 });
  const callRow = (await callSteady.textContent()) ?? '';
  check('a call recording is offered Steady the camera, marked', (await callSteady.getAttribute('data-fits')) === 'true' && /The camera shakes/.test(callRow), callRow);
  check('measured between the picture switches', /between the picture.s switches/.test(callRow), callRow);
  await callSteady.locator('input[type=checkbox]').check();
  await toast(page, 'Making the fixed copy');
  await waitFor(async () => (await callFixes.getAttribute('data-fix-state')) === 'ready' && /Uses its fixed copy/.test((await callFixes.locator('[data-fix-summary]').textContent()) ?? ''), 'the steady call', 240_000);
  const callView = await api(page, 'asset.fixes', { id: CALL });
  check('steadied one stretch between switches at a time', (callView.copy?.notes ?? []).some((n) => /each stretch between switches was steadied on its own/.test(n)), JSON.stringify(callView.copy?.notes ?? []));
  check('the call is steadier, measured between the switches', /→/.test((await callSteady.textContent()) ?? '') && !!callView.copy, (await callSteady.textContent()) ?? '');
  await quiet(page);
  await shot(page, 'call-steadied-desktop-light', callCard);

  // ---------- dark ----------
  await theme(page, 'dark');
  await quiet(page);
  await shot(page, 'info-card-desktop-dark', card);
  await theme(page, 'light');

  // ---------- Use all: MetaRush's better names ----------
  await page.goto(`${base}/p/${P}?tab=sources`);
  const better = page.locator('[data-better-names]');
  await better.waitFor({ timeout: 30_000 });
  await better.getByRole('button', { name: 'See them' }).click();
  const proposed = (await better.textContent()) ?? '';
  check('better names wait for a click', /becomes/.test(proposed) && /Samantha/.test(proposed), proposed.slice(0, 160));
  await shot(page, 'better-names', better);
  await better.locator('[data-use-all-names]').click();
  await toast(page, /Renamed 1 source/);
  const ia = (await api(page, 'asset.card', { id: process.env.SRC_INTERVIEW })).title;
  check('Use all renamed the interview', /Samantha/.test(ia), ia);
  check('the Add box offers Generate video', await page.locator('[data-addbox] [data-generate-video]').isVisible());

  // ---------- the cut's name and a version's note ----------
  await page.goto(`${base}/p/${P}?tab=history`);
  const names = page.locator('[data-cut-names]');
  await names.waitFor({ timeout: 20_000 });
  const suggested = (await names.textContent()) ?? '';
  check('the cut is named by its purpose, not “garden main v2”', /Awareness video/.test(suggested) && !/garden main v2/.test(suggested), suggested.slice(0, 120));
  await names.locator('[data-use-cut-name]').click();
  await toast(page, 'The cut is called “Awareness video” now');
  await page.locator('[data-version-note]').first().click();
  const prompt = page.getByRole('dialog');
  await prompt.getByRole('textbox').fill('steadier walk');
  await prompt.getByRole('button', { name: /OK|Save|Done/ }).click();
  await page.getByText('steadier walk').first().waitFor({ timeout: 20_000 });
  check('a version carries its note', true);
  await shot(page, 'history-names');
  }

  // ---------- Generate video from Home's drop box, starting from a photo ----------
  await page.goto(`${base}/`);
  await page.locator('[data-addbox] [data-generate-video]').first().click();
  const which = page.locator('[data-which-project]');
  await which.waitFor();
  const usual = (await which.locator('[data-usual-project]').textContent()) ?? '';
  check('Home asks which project, the one used last chosen', /Garden film/.test(usual), usual);
  await shot(page, 'generate-which-project', page.getByRole('dialog'));
  await which.locator('[data-usual-project]').click();
  await page.waitForURL((u) => u.pathname === `/p/${P}` && u.searchParams.get('generate') === '1', { timeout: 15_000 });
  const panel = page.locator('[data-generate-panel]');
  await panel.waitFor();
  await panel.locator('[data-generate-prompt]').fill('the dog runs across the garden lawn');
  await panel.locator('[data-start-file]').setInputFiles(process.env.SRC_PHOTO);
  await panel.locator('[data-generate-frames]').waitFor({ timeout: 60_000 });
  check('the shot starts on the photo', /Starts on/.test((await panel.locator('[data-generate-frames]').textContent()) ?? ''));
  await panel.locator('[data-no-people]').check();
  const go = panel.locator('[data-generate-go]');
  await waitFor(async () => /^Generate \(about \$\d+\.\d\d\)$/.test(((await go.textContent()) ?? '').trim()) && (await go.isEnabled()), 'the price on the button', 30_000);
  await shot(page, 'generate-from-photo', panel);
  const toasted = page.getByText('Asked for the shot').first().waitFor({ timeout: 180_000 }).then(() => true, () => false);
  await go.click();
  // The request itself, or a refusal in the panel (a loaded machine may take a while to answer).
  const asked = await waitFor(async () => {
    const alert = panel.locator('[role=alert]');
    if (await alert.count()) return `refused: ${(await alert.first().textContent()) ?? ''}`;
    const l = await api(page, 'generate.list', { projectId: P, limit: 5 });
    return l.generations.some((g) => g.prompt === 'the dog runs across the garden lawn') ? 'ok' : null;
  }, 'the shot to be asked for', 180_000);
  check('the shot is asked for', asked === 'ok', asked);
  if (asked !== 'ok') throw new Error(asked);
  check('the panel says it asked', await toasted);
  const made = await waitFor(async () => {
    const l = await api(page, 'generate.list', { projectId: P, limit: 5 });
    const g = l.generations.find((x) => x.prompt === 'the dog runs across the garden lawn');
    return g && g.state === 'done' ? g : null;
  }, 'the shot from the photo', 180_000);
  const madeWith = await api(page, 'generate.show', { id: made.id });
  check('made from the photo, as its first frame', (madeWith.references ?? []).some((r) => r.role === 'start-frame' && r.resourceId), JSON.stringify((madeWith.references ?? []).map((r) => r.role)));

  // ---------- a picture's fixes in Resources ----------
  await page.goto(`${base}/p/${P}?tab=resources`);
  await page.getByRole('list', { name: 'Resources' }).getByRole('button').filter({ hasText: 'dog' }).first().click();
  const details = page.getByRole('dialog');
  const pf = details.locator('[data-fixes-panel]');
  await pf.waitFor({ timeout: 20_000 });
  await pf.locator('[data-fix="larger"] input[type=checkbox]').check();
  await waitFor(async () => (await pf.getAttribute('data-fix-state')) === 'ready', 'the larger picture', 120_000);
  check('a picture is made larger as a new version', /larger/i.test((await pf.textContent()) ?? ''));
  await shot(page, 'picture-fixes', details);
  await page.keyboard.press('Escape');

  // ---------- a phone ----------
  const phone = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  const pp = await phone.newPage();
  pp.on('pageerror', (e) => problems.push(`phone pageerror: ${e.message}`));
  await pp.goto(mintLink());
  await pp.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
  await theme(pp, 'light');
  await pp.goto(`${base}/p/${P}?tab=sources&asset=${SHAKY}`);
  await pp.locator('[data-info-card]').waitFor({ timeout: 30_000 });
  const wide = await pp.evaluate(() => document.scrollingElement.scrollWidth - window.innerWidth);
  check('no sideways scrolling on a phone', wide <= 1, `${wide}px`);
  await pp.locator('[data-info-card]').scrollIntoViewIfNeeded();
  await shot(pp, 'info-card-phone', pp.locator('[data-info-card]'));
  await phone.close();

  check('no page errors, no native dialogs', problems.length === 0, problems.join(' | '));
  await browser.close();
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
  process.exit(failed.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
