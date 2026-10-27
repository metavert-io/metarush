#!/usr/bin/env node
// Delivery notes, captions in other languages, whose Claude answers and the
// receipt in a real browser (D237–D241). Run it through scripts/delivery-e2e.sh:
// a throwaway installation with an interview (transcribed on this computer), a
// cut with captions, and a fake `claude` signed in to a Claude Max account.
// It checks that:
//   1. Settings → Claude Code says whose Claude answers and under which terms;
//   2. Claude's delivery notes and Spanish captions are cards (reading is its
//      own), the conversation says who answered, and keeping them works;
//   3. the version's page shows the notes (and a person edits them by hand), the
//      Spanish captions marked machine-translated (the file says so), its cost at
//      cost, and "Ask Claude to write them" opens the panel with the request;
//   4. the project's Receipt tab lists the conversation and whose account answered;
//   5. no native dialogs, page errors or horizontal overflow; plain words.
// Screenshots: desktop and phone, dark and light, in DELIVERY_SHOTS.
// It CHANGES the project, so point it only at a throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { checkPanel, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'DELIVERY_PROJECT', 'DELIVERY_REVISION', 'DELIVERY_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/delivery-e2e.sh does)`);
    process.exit(2);
  }
}
const projectId = process.env.DELIVERY_PROJECT;
const rev1 = process.env.DELIVERY_REVISION;
const shotsDir = process.env.DELIVERY_SHOTS;
mkdirSync(shotsDir, { recursive: true });

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
    acceptDownloads: true,
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

const card = (page, title) => page.locator(`[aria-label="Claude asks: ${title.replace(/"/g, '\\"')}"]`).last();

async function keepCard(page, title) {
  const c = card(page, title);
  await c.scrollIntoViewIfNeeded();
  await c.getByRole('button', { name: 'Keep them', exact: true }).click();
  await c.getByText('Done', { exact: true }).or(c.getByText('Did not work', { exact: true })).waitFor({ timeout: 60_000 });
  return c;
}

async function untilIdle(page, convId, actions) {
  let cv;
  for (let i = 0; i < 240; i++) {
    cv = await api(page, 'assistant.show', { id: convId, events: false });
    if (cv.state === 'idle' && cv.actions.length >= actions) break;
    await page.waitForTimeout(250);
  }
  return cv;
}

async function main() {
  const browser = await chromium.launch();
  let { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const state = await ctx.storageState();

  // 1. Whose Claude answers, in Settings → Claude Code.
  await page.goto(`${base}/settings/claude`);
  const acct = page.locator('[data-claude-account]');
  await acct.getByText(/host@example\.com/).waitFor({ timeout: 30_000 });
  const acctText = (await acct.innerText()).replace(/\s+/g, ' ');
  check("Settings → Claude Code says whose Claude answers and its terms", /host@example\.com \(Claude Max\)/.test(acctText) && /Anthropic's consumer terms/.test(acctText) && /privacy settings/.test(acctText), acctText.slice(0, 240));
  check('it reads plainly', (await unplainIn(acct)).length === 0, (await unplainIn(acct)).join(' | '));
  await shot(page, 'whose-claude-desktop-dark');

  // 2. Claude writes the delivery notes and translates the captions: two cards.
  const asked = await api(page, 'assistant.ask', { prompt: 'Write the delivery notes for version 1', projectId });
  const convId = asked.conversation.id;
  let cv = await untilIdle(page, convId, 1);
  await api(page, 'assistant.continue', { id: convId, prompt: 'Translate the captions into Spanish', projectId });
  cv = await untilIdle(page, convId, 2);
  const notesCard = cv.actions.find((a) => a.op === 'delivery.notes.set');
  const esCard = cv.actions.find((a) => a.op === 'captions.translate');
  check('the delivery notes are a card', /^Keep the delivery notes for version 1 of “Garden story”: a title, 2 other titles, a description, 2 chapters and show notes/.test(notesCard?.title ?? ''), notesCard?.title);
  check('the Spanish captions are a card, marked machine-translated', /^Keep the Spanish captions for version 1 of “Garden story”: \d+ lines?, machine-translated by Claude$/.test(esCard?.title ?? ''), esCard?.title);
  check('the conversation records whose Claude answered', /host@example\.com \(Claude Max\)/.test(cv.account?.who ?? '') && cv.account?.terms === 'consumer', JSON.stringify(cv.account));
  check('nothing was kept before anyone said so', !(await api(page, 'delivery.notes', { projectId })).notes && (await api(page, 'captions.languages', { projectId })).languages.length === 0);

  await page.goto(`${base}/p/${projectId}?ask=${convId}`);
  const who = page.locator('[data-who-answers]').first();
  await who.waitFor({ timeout: 30_000 });
  check('the conversation says who answers', /Answered by Claude Code on .*host@example\.com \(Claude Max\) · Anthropic's consumer terms/.test(await who.innerText()), await who.innerText());
  const texts = await page.locator('[aria-label^="Claude asks:"]').allInnerTexts();
  const unplain = texts.flatMap((t) => t.split('\n')).flatMap((l) => checkPanel(l).map((f) => `${f.kind} ${f.text} in ${JSON.stringify(l)}`));
  check('the cards read plainly', unplain.length === 0, unplain.join(' | '));
  await shot(page, 'cards-desktop-dark');
  await keepCard(page, notesCard.title);
  await keepCard(page, esCard.title);
  const notes = await api(page, 'delivery.notes', { projectId });
  check('keeping the notes card keeps them, written by Claude', notes.notes?.title === 'The question in the garage' && notes.notes?.writtenBy === 'claude' && notes.chapters.length === 2, JSON.stringify(notes.notes)?.slice(0, 200));
  const langs = await api(page, 'captions.languages', { projectId });
  check('keeping the translation card keeps the Spanish captions', langs.languages.length === 1 && langs.languages[0].language === 'es' && langs.languages[0].machine && langs.languages[0].missing === 0, JSON.stringify(langs.languages));

  // 3. The version's page (a page of its own: the panel stays closed).
  await ctx.close();
  ({ ctx, page } = await newPage(browser, { state }));
  await page.goto(`${base}/p/${projectId}/r/${rev1}`);
  const dn = page.locator('[data-delivery-notes]');
  await dn.locator('[data-notes-shown]').waitFor({ timeout: 30_000 });
  const dnText = (await dn.innerText()).replace(/\s+/g, ' ');
  check("the version's page shows the delivery notes", /The question in the garage/.test(dnText) && /Written by Claude/.test(dnText) && /0:06 The question/.test(dnText) && /at least three/.test(dnText), dnText.slice(0, 240));
  const cl = page.locator('[data-caption-language="es"]');
  await cl.waitFor({ timeout: 30_000 });
  check('the Spanish captions are listed, machine-translated', /Spanish/.test(await cl.innerText()) && /Machine-translated/.test(await cl.innerText()), await cl.innerText());
  const vttButton = cl.getByRole('button', { name: '.vtt', exact: true });
  check('the Spanish captions download as .srt and .vtt', (await vttButton.count()) === 1 && (await cl.getByRole('button', { name: '.srt', exact: true }).count()) === 1);
  try {
    const [download] = await Promise.all([page.waitForEvent('download', { timeout: 30_000 }), vttButton.click()]);
    const vtt = readFileSync(await download.path(), 'utf8');
    check('the Spanish file says it was machine-translated', vtt.startsWith('WEBVTT\n\nNOTE Machine-translated from English by Claude') && /\nES /.test(vtt) && /\.es\.vtt$/.test(download.suggestedFilename()), `${download.suggestedFilename()}: ${vtt.slice(0, 120)}`);
  } catch (e) {
    const toasts = await page.locator('[data-toasts]').innerText().catch(() => '');
    check('the Spanish file downloads', false, `${e.message.split('\n')[0]}; toasts: ${toasts.replace(/\s+/g, ' ')}`);
  }
  const cost = page.locator('[data-version-cost]');
  await cost.waitFor({ timeout: 30_000 });
  check("the version's cost at cost", /costs nothing at cost/.test(await cost.innerText()), await cost.innerText());
  const pageFindings = [...(await unplainIn(dn)), ...(await unplainIn(page.locator('[data-caption-languages]'))), ...(await unplainIn(cost))];
  check("the version's page reads plainly", pageFindings.length === 0, pageFindings.join(' | '));
  await dn.scrollIntoViewIfNeeded();
  await shot(page, 'version-notes-desktop-dark');

  // A person edits them by hand.
  await dn.locator('[data-notes-edit]').click();
  const form = page.locator('[data-mr-dialog="open"] [data-notes-form]');
  await form.waitFor({ timeout: 10_000 });
  await form.getByLabel('Title', { exact: true }).fill('A garage, a question');
  await form.getByLabel(/^Chapters/).fill('0:00 The garage\n0:06 The question\n1.5 not a time');
  await form.locator('[data-notes-save]').click();
  const err = form.getByRole('alert');
  await err.waitFor({ timeout: 10_000 });
  check('a chapter without a time is said in the form', /can't read the chapter “1\.5 not a time”/.test(await err.innerText()), await err.innerText());
  await form.getByLabel(/^Chapters/).fill('0:00 The garage\n0:06 The question');
  await shot(page, 'notes-form-desktop-dark');
  await form.locator('[data-notes-save]').click();
  await form.waitFor({ state: 'detached', timeout: 20_000 });
  await dn.getByText('A garage, a question').waitFor({ timeout: 20_000 });
  check('edited by hand: written by a person', /Written by a person/.test(await dn.innerText()));

  // "Ask Claude to write them" opens the panel with the request.
  await dn.locator('[data-notes-ask]').click();
  const box = page.locator('textarea').filter({ hasText: /delivery notes/ }).or(page.locator('textarea[aria-label]'));
  await page.waitForTimeout(800);
  const asksText = await page.evaluate(() => [...document.querySelectorAll('textarea')].map((t) => t.value).join(' | '));
  check('"Ask Claude for new ones" opens the panel with the request', /Write the delivery notes for version 1/.test(asksText), asksText.slice(0, 160));
  void box;

  // 4. The Receipt tab (again a page of its own).
  await ctx.close();
  ({ ctx, page } = await newPage(browser, { state }));
  await page.goto(`${base}/p/${projectId}?tab=receipt`);
  const rc = page.locator('[data-receipt]');
  await rc.locator('[data-receipt-lines]').waitFor({ timeout: 30_000 });
  const rcText = (await rc.innerText()).replace(/\s+/g, ' ');
  check('the receipt: nothing sent to outside AI services, nothing spent', /sent nothing to outside AI services/.test(rcText) && /nothing/.test(await rc.locator('[data-receipt-project-cost]').innerText()), rcText.slice(0, 200));
  check("the receipt lists Claude's conversation and whose account answered", (await rc.locator('[data-receipt-claude]').count()) === 1 && /host@example\.com \(Claude Max\)/.test(rcText) && /on the plan/.test(rcText), rcText.slice(0, 400));
  const rcFindings = await unplainIn(rc);
  check('the receipt reads plainly', rcFindings.length === 0, rcFindings.join(' | '));
  await shot(page, 'receipt-desktop-dark');
  await ctx.close();

  // Phone and light.
  for (const theme of ['dark', 'light']) {
    for (const mobile of [false, true]) {
      if (!mobile && theme === 'dark') continue;
      const tag = `${mobile ? 'phone' : 'desktop'}-${theme}`;
      const v = await newPage(browser, { theme, mobile, state });
      await v.page.goto(`${base}/p/${projectId}/r/${rev1}`);
      await v.page.locator('[data-delivery-notes] [data-notes-shown]').waitFor({ timeout: 30_000 });
      await v.page.locator('[data-delivery-notes]').scrollIntoViewIfNeeded();
      await shot(v.page, `version-notes-${tag}`);
      await v.page.locator('[data-caption-languages]').scrollIntoViewIfNeeded();
      await shot(v.page, `caption-languages-${tag}`);
      await v.page.goto(`${base}/p/${projectId}?tab=receipt`);
      await v.page.locator('[data-receipt-lines]').waitFor({ timeout: 30_000 });
      await shot(v.page, `receipt-${tag}`);
      await v.page.goto(`${base}/settings/claude`);
      await v.page.locator('[data-claude-account]').getByText(/host@example\.com/).waitFor({ timeout: 30_000 });
      await shot(v.page, `whose-claude-${tag}`);
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
