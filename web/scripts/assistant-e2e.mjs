#!/usr/bin/env node
// End-to-end check of the Claude Code panel through the real UI. One
// conversation of three messages on a project:
//   1. a read-only look (contact sheets, so the thread shows thumbnails);
//   2. a change, with a page reload while Claude works (the stream picks up
//      again), then the plan applied from its card, then full screen;
//   3. a bigger request finished through action cards and a render card.
// Then a screenshot matrix (dark/light × desktop/mobile) of the drawer's
// empty state, the conversation, the conversation list, full screen and
// Settings → Claude Code, plus the single top-bar entry point on Home, a
// project and a revision. Screenshots land in web/.screenshots/assistant/.
//
// With a real `claude` this spends Claude usage (a few cents API-equivalent
// with a small model; capped by assistant.maxBudgetUsd). Point tools.claude at
// a fake to test the UI for free.
//
//   METARUSH_DASHBOARD=http://127.0.0.1:4862 \
//   METARUSH_MINT="bin/metarush --home <home> dashboard --print-link" \
//     node web/scripts/assistant-e2e.mjs
//
// Env: PROJECT (name; default the first), TURN1, TURN2, TURN3 (the messages),
//      CONVERSATION=<asc_…> (skip the conversation, only take the matrix of it),
//      RUN_TIMEOUT_MS (per message; default 6 minutes), SCREENSHOTS_DIR.
import { execSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const outDir = process.env.SCREENSHOTS_DIR ?? path.join(path.resolve(here, '..'), '.screenshots', 'assistant');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://localhost:4840').replace(/\/$/, '');
const runTimeout = Number(process.env.RUN_TIMEOUT_MS ?? 6 * 60_000);
const turn1 = process.env.TURN1 ?? "What's in this project? Look at a contact sheet of each clip and tell me briefly what you see.";
const turn2 = process.env.TURN2 ?? 'Tighten the b-roll in the current cut';
const problems = [];
const log = (...a) => console.log(new Date().toISOString().slice(11, 19), ...a);

function mintLink() {
  if (process.env.METARUSH_SIGNIN_URL) return process.env.METARUSH_SIGNIN_URL;
  const cmd = process.env.METARUSH_MINT ?? 'metarush dashboard --print-link --no-open';
  const out = execSync(cmd, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in the output of: ${cmd}\n${out}`);
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
      if (!j.ok) throw new Error(`${name}: ${j.error?.code} ${j.error?.message}`);
      return j.data;
    },
    [name, input],
  );
}

async function shot(page, name) {
  await page.waitForTimeout(400);
  const file = path.join(outDir, `${name}.png`);
  await page.screenshot({ path: file });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow ${overflow}px on ${name}`);
  log('screenshot', path.relative(process.cwd(), file));
}

function watch(page) {
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('console', (m) => m.type() === 'error' && !/Failed to load resource/.test(m.text()) && problems.push(`console.error: ${m.text()}`));
}

const confirmDialog = (page) => page.locator('[data-mr-dialog="open"] button[type="submit"]');

async function send(page, text) {
  const ta = page.getByLabel('Message to Claude');
  await ta.click();
  await ta.fill(text);
  await page.keyboard.press('ControlOrMeta+Enter');
}

async function waitIdle(page, id, label) {
  const start = Date.now();
  for (;;) {
    const c = await op(page, 'assistant.show', { id, events: false });
    const last = c.turns?.[c.turns.length - 1];
    if (c.state === 'idle' && !c.queue?.length && last && last.state !== 'running') {
      log(`${label}: ${last.state} after ${Math.round((Date.now() - start) / 1000)}s (${last.steps} steps)`);
      return c;
    }
    if (Date.now() - start > runTimeout) throw new Error(`${label}: still working after ${runTimeout / 1000}s`);
    await page.waitForTimeout(1500);
  }
}

async function scrollThreadToEnd(page) {
  await page.evaluate(() => {
    for (const el of document.querySelectorAll('[role="dialog"] .overflow-y-auto')) el.scrollTop = el.scrollHeight;
  });
  await page.waitForTimeout(500);
}

/** The conversation: three messages, a reload mid-reply, cards applied by a person. */
async function converse(ctx, project) {
  const page = await ctx.newPage();
  watch(page);
  await page.goto(`${base}/p/${project.id}`);
  await page.waitForLoadState('networkidle').catch(() => {});
  const entries = await page.getByRole('button', { name: /^Ask Claude Code/ }).count();
  if (entries !== 1) problems.push(`${entries} Ask Claude Code buttons on the project page (want exactly 1)`);
  await page.getByRole('button', { name: /^Ask Claude Code/ }).first().click();
  await page.getByText('Things to try').waitFor({ timeout: 30_000 });
  await shot(page, 'run-1-empty');

  await send(page, turn1);
  let id;
  for (let i = 0; i < 60 && !id; i++) {
    await page.waitForTimeout(250);
    id = await page.evaluate(() => {
      try {
        return JSON.parse(sessionStorage.getItem('mr.assistant.panel') || '{}').conversationId;
      } catch {
        return undefined;
      }
    });
  }
  if (!id) throw new Error('the conversation did not start');
  log('conversation', id);
  for (let i = 0; i < 120; i++) {
    if ((await page.locator('.mr-cursor').count()) > 0 || (await page.locator('ul[aria-label$="steps"] li').count()) >= 3) break;
    await page.waitForTimeout(500);
  }
  await shot(page, 'run-2-streaming');
  await waitIdle(page, id, 'message 1');
  await page.waitForTimeout(1000);
  await shot(page, 'run-3-reply');

  await send(page, turn2);
  for (let i = 0; i < 80; i++) {
    const c = await op(page, 'assistant.show', { id, events: true });
    if (c.state === 'working' && (c.events ?? []).filter((e) => e.turn === 2).length >= 3) break;
    await page.waitForTimeout(500);
  }
  log('reloading while Claude works');
  await page.reload();
  await page.getByLabel('Message to Claude').waitFor({ timeout: 30_000 });
  await page.waitForTimeout(1500);
  await shot(page, 'run-4-after-reload');
  await waitIdle(page, id, 'message 2');
  await page.waitForTimeout(1500);

  // The plan is either a plan card ("Apply plan") or, when Claude asked to apply
  // it, an action card ("Apply") that stands for it.
  const c2 = await op(page, 'assistant.show', { id, events: false });
  const asked = (c2.actions ?? []).find((a) => a.state === 'pending' && a.op === 'edit.apply');
  const apply = page.getByRole('button', { name: 'Apply plan' }).first();
  if (asked) {
    const article = page.locator(`article[aria-label="Claude asks: ${asked.title.replace(/"/g, '\\"')}"]`).last();
    await article.scrollIntoViewIfNeeded();
    await shot(page, 'run-5-plan-card');
    await article.getByRole('button', { name: 'Apply', exact: true }).click();
    await page.waitForTimeout(400);
    if (await confirmDialog(page).count()) await confirmDialog(page).first().click();
    await article.getByText('Done').first().waitFor({ timeout: 30_000 });
    await page.waitForTimeout(800);
    await shot(page, 'run-6-plan-applied');
    await waitIdle(page, id, 'after applying');
  } else if (await apply.count()) {
    await apply.scrollIntoViewIfNeeded();
    await shot(page, 'run-5-plan-card');
    await apply.click();
    await confirmDialog(page).click();
    await page.getByText(/Saved as version \d/).first().waitFor({ timeout: 30_000 });
    await page.waitForTimeout(800);
    await shot(page, 'run-6-plan-applied');
  } else {
    problems.push('message 2 produced no plan to apply');
  }

  await page.getByRole('button', { name: 'Full screen' }).click();
  await page.waitForTimeout(2000);
  await shot(page, 'run-7-fullscreen');

  const turn3 = process.env.TURN3 ?? `Make a 15-second vertical cut of ${project.name} and render a review copy`;
  const before = (await op(page, 'assistant.show', { id, events: false })).turns.length;
  await send(page, turn3);
  for (let round = 0; round < 4; round++) {
    let card = null;
    for (let i = 0; i < runTimeout / 1000; i++) {
      const c = await op(page, 'assistant.show', { id, events: false });
      const pending = (c.actions ?? []).filter((a) => a.state === 'pending' && a.op && a.turn > before);
      if (pending.length && c.state === 'idle') {
        card = pending[0];
        break;
      }
      if (c.state === 'idle' && !pending.length && i > 4) break;
      await page.waitForTimeout(1000);
    }
    if (!card) break;
    log('action card:', card.title);
    const article = page.locator(`article[aria-label="Claude asks: ${card.title.replace(/"/g, '\\"')}"]`).last();
    await article.scrollIntoViewIfNeeded();
    await shot(page, `run-8-action-${round + 1}`);
    await article.getByRole('button').filter({ hasNotText: 'Not now' }).filter({ hasNotText: 'Details' }).filter({ hasNotText: 'Show' }).first().click();
    await page.waitForTimeout(400);
    if (await confirmDialog(page).count()) await confirmDialog(page).first().click();
    await page.waitForTimeout(2500);
    await waitIdle(page, id, `after card ${round + 1}`);
  }

  const renderBtn = page.locator('article[aria-label^="Render request"]').getByRole('button', { name: /^Render (review|final)/ }).last();
  if (await renderBtn.count()) {
    await renderBtn.scrollIntoViewIfNeeded();
    await shot(page, 'run-9-render-card');
    await renderBtn.click();
    await page.waitForTimeout(400);
    await confirmDialog(page).click();
    await page.getByText(/Rendering version \d|Already rendered/).first().waitFor({ timeout: 30_000 });
    const start = Date.now();
    for (;;) {
      const props = await op(page, 'assistant.proposals', { id });
      const states = props.turns.flatMap((t) => t.renders).map((r) => r.renderState).filter(Boolean);
      if (states.includes('available') || states.includes('failed') || Date.now() - start > 5 * 60_000) {
        log('render:', states.join(', ') || 'none', `after ${Math.round((Date.now() - start) / 1000)}s`);
        break;
      }
      await page.waitForTimeout(3000);
    }
  } else {
    log('no render card in message 3');
  }
  await page.waitForTimeout(1500);
  await scrollThreadToEnd(page);
  await shot(page, 'run-10-done');

  const final = await op(page, 'assistant.show', { id, events: true });
  writeFileSync(path.join(outDir, 'conversation.json'), JSON.stringify(final, null, 2));
  log(
    'turns:',
    final.turns.map((t) => `${t.n}:${t.state}${t.note ? '(note)' : ''}`).join(' '),
    '| cards:',
    (final.actions ?? []).map((a) => `${a.kind}=${a.state}`).join(' ') || 'none',
  );
  await page.close();
  return id;
}

/** Opens the panel in a given state (the panel restores it from sessionStorage on load). */
async function openPanel(page, url, state) {
  await page.goto(url);
  // Let the app finish mounting first: it saves its own panel state on mount.
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(800);
  await page.evaluate((s) => sessionStorage.setItem('mr.assistant.panel', JSON.stringify(s)), { view: 'thread', fullscreen: false, open: true, ...state });
  await page.reload();
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.getByLabel('Message to Claude').or(page.getByRole('button', { name: 'Check again' })).first().waitFor({ timeout: 20_000 });
  await page.waitForTimeout(1200);
}

async function matrix(browser, storageState, project, id, revisionId) {
  for (const scheme of ['dark', 'light']) {
    for (const vp of [
      { name: 'desktop', width: 1440, height: 900, scale: 2 },
      { name: 'mobile', width: 390, height: 844, scale: 3 },
    ]) {
      const ctx = await browser.newContext({ viewport: { width: vp.width, height: vp.height }, deviceScaleFactor: vp.scale, colorScheme: scheme, storageState });
      await ctx.addInitScript((t) => {
        try {
          localStorage.setItem('metarush.theme', t);
        } catch {}
      }, scheme);
      const page = await ctx.newPage();
      watch(page);
      const p = `${scheme}-${vp.name}`;
      await openPanel(page, `${base}/p/${project.id}`, { conversationId: undefined });
      await shot(page, `${p}-empty`);
      await openPanel(page, `${base}/p/${project.id}`, { conversationId: id });
      await scrollThreadToEnd(page);
      await shot(page, `${p}-conversation`);
      await page.getByRole('button', { name: 'All conversations' }).click();
      await page.getByPlaceholder('Search conversations').waitFor({ timeout: 5_000 });
      await shot(page, `${p}-list`);
      await openPanel(page, `${base}/p/${project.id}`, { conversationId: id, fullscreen: true });
      await scrollThreadToEnd(page);
      await shot(page, `${p}-fullscreen`);
      await page.evaluate(() => sessionStorage.removeItem('mr.assistant.panel'));
      await page.goto(`${base}/settings/claude#claude-code`);
      await page.locator('#claude-code').waitFor({ timeout: 15_000 });
      await page.waitForTimeout(1200);
      await page.locator('#claude-code').scrollIntoViewIfNeeded();
      await shot(page, `${p}-settings`);
      await ctx.close();
    }
  }
  // Exactly one way in: the top-bar button, on every screen.
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1, colorScheme: 'dark', storageState });
  const page = await ctx.newPage();
  watch(page);
  // A notification or activity link (?ask=<conversation>) opens that conversation.
  if (id) {
    await page.goto(`${base}/p/${project.id}?ask=${id}`);
    await page.getByLabel('Message to Claude').waitFor({ timeout: 20_000 });
    await page.waitForTimeout(1200);
    const opened = await page.evaluate(() => {
      try {
        return JSON.parse(sessionStorage.getItem('mr.assistant.panel') || '{}').conversationId;
      } catch {
        return undefined;
      }
    });
    if (opened !== id) problems.push(`?ask= opened ${opened ?? 'nothing'}, want ${id}`);
    await shot(page, 'entry-deep-link');
    await page.evaluate(() => sessionStorage.removeItem('mr.assistant.panel'));
  }
  for (const [name, url] of [
    ['entry-home', `${base}/`],
    ['entry-project', `${base}/p/${project.id}`],
    ['entry-revision', `${base}/p/${project.id}/r/${revisionId}`],
  ]) {
    await page.goto(url);
    await page.waitForLoadState('networkidle').catch(() => {});
    await page.waitForTimeout(800);
    const n = await page.getByRole('button', { name: /Ask Claude/ }).count();
    log(`${name}: ${n} Ask Claude Code button(s)`);
    if (n !== 1) problems.push(`${name}: ${n} Ask Claude Code buttons (want exactly 1)`);
    await shot(page, name);
  }
  await ctx.close();
}

async function main() {
  mkdirSync(outDir, { recursive: true });
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2, colorScheme: 'dark' });
  await ctx.addInitScript(() => {
    try {
      localStorage.setItem('metarush.theme', 'dark');
    } catch {}
  });
  const signin = await ctx.newPage();
  await signin.goto(mintLink());
  await signin.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
  const projects = (await op(signin, 'project.list')).projects;
  const project = process.env.PROJECT ? projects.find((p) => p.name === process.env.PROJECT) : projects[0];
  if (!project) throw new Error('no project in this installation');
  const status = await op(signin, 'assistant.status');
  log(`Claude Code: ${status.available ? `${status.claudeVersion}, ${status.billingLabel}` : `not available (${status.reason})`}`);
  await signin.close();

  const id = process.env.CONVERSATION || (status.available ? await converse(ctx, project) : undefined);
  const storageState = await ctx.storageState();
  const page = await ctx.newPage();
  await page.goto(`${base}/`);
  const show = await op(page, 'project.show', { id: project.id });
  await page.close();
  await matrix(browser, storageState, project, id, show.headRevisionId ?? show.revisions?.[0]?.id);
  await browser.close();
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
