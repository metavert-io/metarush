#!/usr/bin/env node
// The review desk in a real browser (D248–D251). Run it through
// scripts/review-desk-e2e.sh: a throwaway installation with a project
// ("Garden walk") whose first cut Codex proposed to shorten (a draft version
// beside the cut) and where Cursor proposed a house rule — both fake AI apps
// on `metarush mcp serve --propose` (scripts/mcp-agents.py). It checks that:
//   1. Awaiting your review on the Studio page lists both drafts, each named
//      for its app, the version as "not in the cut yet";
//   2. a draft's page says what changes, why (the app's words), the checks,
//      the cost and what's waiting, with Approve, Not quite… and Turn down;
//   3. Not quite takes an inline note (never a native dialog) that Codex hears
//      on its next call, once; its new draft takes the old one's place;
//   4. approving the new draft (a styled confirm) puts its version in the cut,
//      approved by the person; approving Cursor's makes the house rule;
//   5. the version's page says it was the app's draft only while it waited;
//      the review copy MetaRush queues by itself for it shows as being made;
//   6. the page can be copied and downloaded as Markdown;
//   7. no native dialogs, page errors or horizontal overflow; plain words;
//      16px gutters on a phone.
// It CHANGES the project: point it only at a throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'REVIEW_PROJECT', 'REVIEW_SHOTS', 'REVIEW_AGENTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/review-desk-e2e.sh does)`);
    process.exit(2);
  }
}
const projectId = process.env.REVIEW_PROJECT;
const shotsDir = process.env.REVIEW_SHOTS;
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

/** Runs a fake AI app's phase (scripts/mcp-agents.sh); its last line is JSON. */
function agents(args) {
  const out = execSync(`${process.env.REVIEW_AGENTS} ${args}`, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'] });
  return JSON.parse(out.trim().split('\n').pop());
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

const shots = [];
let shotN = 0;
async function shot(page, name) {
  await page.waitForTimeout(450);
  const file = path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file, fullPage: true });
  shots.push(file);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
}

/** The text a person reads in an element, people's own words (<Theirs>) in curly quotes. */
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
      const t = root.innerText;
      for (const n of added) n.remove();
      return t;
    },
    [selector, THEIRS_OPEN, THEIRS_CLOSE],
  );
}

function unplain(text) {
  return text
    .split('\n')
    .flatMap((l) => checkShown(l).map((f) => `${f.kind} ${JSON.stringify(f.text)} in ${JSON.stringify(l.trim().slice(0, 120))}`));
}

async function until(fn, ms = 30_000, step = 250) {
  const end = Date.now() + ms;
  let v;
  while (Date.now() < end) {
    v = await fn();
    if (v) return v;
    await new Promise((r) => setTimeout(r, step));
  }
  return v;
}

async function toastText(page) {
  const t = page.locator('[data-toasts] [role="status"], [data-toasts] [role="alert"]').last();
  await t.waitFor({ timeout: 15_000 }).catch(() => {});
  return (await t.innerText().catch(() => '')).replace(/\s+/g, ' ');
}

async function main() {
  const browser = await chromium.launch();
  const { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const state = await ctx.storageState();

  // 1. The Studio page's Awaiting your review.
  await page.goto(`${base}/`);
  const desk = page.locator('[data-awaiting-review]');
  await desk.waitFor({ timeout: 20_000 });
  await until(async () => (await desk.locator('[data-waiting-draft]').count()) >= 2, 20_000);
  const deskText = await readText(page, '[data-awaiting-review]');
  check('Awaiting your review lists both apps’ drafts', /Codex's draft/.test(deskText) && /Cursor's draft/.test(deskText), deskText.replace(/\s+/g, ' ').slice(0, 300));
  check('… the version as not in the cut yet', /not in the cut yet/.test(deskText));
  check('… and Cursor’s house rule by its words', /The name bar never animates/.test(deskText));
  const deskPlain = unplain(deskText);
  check('the list reads plainly', deskPlain.length === 0, deskPlain.join(' | '));
  await shot(page, 'awaiting-review-desktop-dark');

  // 2. Codex's draft page.
  const drafts = (await api(page, 'draft.list', { all: true, state: 'waiting' })).drafts;
  const codex = drafts.find((d) => d.agent === 'codex');
  const cursor = drafts.find((d) => d.agent === 'cursor');
  check('the apps’ drafts are waiting', !!codex && !!cursor && codex.kind === 'version' && cursor.kind === 'change', drafts.map((d) => `${d.agent}:${d.kind}`).join(', '));
  await desk.locator(`[data-waiting-draft="${codex.id}"] a`).click();
  await page.waitForURL(new RegExp(`/review/${codex.id}$`), { timeout: 15_000 });
  const pageSel = '[data-review-page]';
  await page.locator(pageSel).waitFor({ timeout: 15_000 });
  const pageText = await readText(page, pageSel);
  for (const [what, re] of [
    ['what changes', /What changes[\s\S]*shorter/],
    ['why, in the app’s words', /Why[\s\S]*The opening drags/],
    ['the checks', /Checks/],
    ['the cost', /These changes cost nothing to make/],
    ['what’s waiting', /What's waiting[\s\S]*A person's OK/],
    ['who made it', /Codex, through MetaRush's tools for AI apps/],
  ])
    check(`the draft’s page says ${what}`, re.test(pageText));
  check('… and offers Approve, Not quite and Turn down', (await page.locator('[data-approve]').count()) === 1 && (await page.locator('[data-not-quite]').count()) === 1 && (await page.locator('[data-turn-down]').count()) === 1);
  const pagePlain = unplain(pageText);
  check('the draft’s page reads plainly', pagePlain.length === 0, pagePlain.join(' | '));
  await shot(page, 'draft-page-desktop-dark');

  // 3. Not quite, inline; Codex hears it once and makes a new draft.
  await page.locator('[data-not-quite]').click();
  await page.locator('[data-not-quite-note]').fill('Keep more of the opening: take half a second off, not a whole one.');
  await page.locator('[data-send-not-quite]').click();
  const nqToast = await toastText(page);
  check('Not quite says Codex hears it', /Codex hears what should change/.test(nqToast), nqToast);
  await until(async () => (await page.locator('[data-review-page][data-draft-state="not_quite"]').count()) === 1, 15_000);
  check('the page shows the decision', /said Not quite/.test(await readText(page, pageSel)));
  const news = agents(`--phase news --expect 'Keep more of the opening'`);
  check('Codex hears the note on its next call, once', news.failures.length === 0, news.failures.join(' | '));
  const redo = agents('--phase redo');
  check('Codex’s new draft takes the old one’s place', redo.failures.length === 0 && !!redo.codex, redo.failures.join(' | '));

  // 4. Approve the new draft and Cursor's house rule.
  await page.goto(`${base}/`);
  await until(async () => (await page.locator(`[data-waiting-draft="${redo.codex}"]`).count()) === 1, 20_000);
  check('the old draft left the desk', (await page.locator(`[data-waiting-draft="${codex.id}"]`).count()) === 0);
  await page.locator(`[data-waiting-draft="${redo.codex}"] a`).click();
  await page.locator('[data-review-page][data-draft-state="waiting"]').waitFor({ timeout: 15_000 });
  await page.locator('[data-approve]').click();
  const confirm = page.locator('[role="dialog"], [role="alertdialog"]').last();
  await confirm.waitFor({ timeout: 10_000 });
  const confirmText = (await confirm.innerText()).replace(/\s+/g, ' ');
  check('approving asks first, in a styled dialog', /Put Codex's draft in the cut\?/.test(confirmText) && /latest version of the cut, approved by you/.test(confirmText), confirmText);
  await confirm.getByRole('button', { name: 'Approve and use it' }).click();
  const okToast = await toastText(page);
  check('approving says the version is in the cut', /is in the cut and approved/.test(okToast), okToast);
  await until(async () => (await page.locator('[data-review-page][data-draft-state="approved"]').count()) === 1, 15_000);
  const approved = (await api(page, 'draft.list', { all: true, state: 'approved' })).drafts.find((d) => d.id === redo.codex);
  const show = await api(page, 'project.show', { id: projectId });
  check('its version is the cut’s latest, approved by the person', !!approved && show.headRevisionId === approved.revision.id && show.revisions.find((r) => r.id === approved.revision.id)?.approval.state === 'approved',
    `${show.headRevisionId} vs ${approved?.revision?.id}`);
  await shot(page, 'draft-approved-desktop-dark');

  await page.goto(`${base}/review/${cursor.id}`);
  await page.locator('[data-review-page][data-draft-state="waiting"]').waitFor({ timeout: 15_000 });
  check('Cursor’s draft says where it changes', /Content Settings/.test(await readText(page, pageSel)));
  await page.locator('[data-approve]').click();
  const confirm2 = page.locator('[role="dialog"], [role="alertdialog"]').last();
  await confirm2.waitFor({ timeout: 10_000 });
  await confirm2.getByRole('button', { name: 'Approve and make it' }).click();
  await until(async () => (await page.locator('[data-review-page][data-draft-state="approved"]').count()) === 1, 15_000);
  const rules = await api(page, 'content.rules.list', { projectId });
  check('approving Cursor’s draft made the house rule', rules.rules.some((r) => r.text === 'The name bar never animates'), JSON.stringify(rules.rules.map((r) => r.text)));
  await page.goto(`${base}/`);
  await desk.waitFor({ timeout: 15_000 });
  await until(async () => (await desk.locator('[data-waiting-draft]').count()) === 0, 15_000);
  check('the desk holds no drafts now', (await desk.locator('[data-waiting-draft]').count()) === 0);

  // 5. The version's page: no "draft" banner once it is in the cut; the review copy queued by itself.
  await page.goto(`${base}/p/${projectId}/r/${approved.revision.id}`);
  await page.waitForTimeout(800);
  check('the version’s page no longer calls it a draft', (await page.locator('[data-draft-banner]').count()) === 0);
  await page.goto(`${base}/review/${redo.codex}`);
  const queued = await until(
    async () => {
      await page.reload();
      await page.locator('[data-review-page]').waitFor({ timeout: 15_000 });
      return (await page.locator('[data-review-copy="queued"]').count()) === 1;
    },
    150_000,
    10_000,
  );
  check('MetaRush queued the draft’s review copy by itself (being made)', queued, await page.locator('[data-review-copy]').getAttribute('data-review-copy').catch(() => ''));

  // 6. Copy and download as Markdown.
  check('the page can be downloaded as Markdown', (await page.locator('[data-download-page]').count()) === 1);
  const md = await api(page, 'review.packet', { id: redo.codex, format: 'markdown' });
  check('the Markdown page has every part', ['## What changes', '## Why', '## Checks', '## Cost', "## What's waiting", '## Decision'].every((h) => md.markdown.includes(h)), md.markdown.slice(0, 200));
  await ctx.close();

  // 7. Light and phone.
  for (const [theme, mobile, tag] of [
    ['light', false, 'desktop-light'],
    ['dark', true, 'phone-dark'],
    ['light', true, 'phone-light'],
  ]) {
    const v = await newPage(browser, { theme, mobile, state });
    await v.page.goto(`${base}/review/${redo.codex}`);
    await v.page.locator('[data-review-page]').waitFor({ timeout: 20_000 });
    await shot(v.page, `draft-page-${tag}`);
    if (mobile) {
      const box = await v.page.locator('[data-review-page]').boundingBox();
      check(`16px gutters on a phone (${theme})`, !!box && box.x >= 15.5 && box.x + box.width <= 390 - 15.5, box ? `left ${box.x}, right ${390 - box.x - box.width}` : 'not found');
    }
    await v.ctx.close();
  }
  await browser.close();

  check('no native dialogs, page errors or horizontal overflow', problems.length === 0, problems.join(' | '));
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed; ${shots.length} screenshots in ${shotsDir}`);
  if (failed.length) process.exitCode = 1;
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
