#!/usr/bin/env node
// End-to-end check of the starter recipes and a sample first (D293–D297) in the dashboard,
// against a seeded throwaway daemon that talks only to a fake ElevenLabs
// (scripts/starter-recipes-e2e.sh does all of it):
//   A  the Library's Recipes tab offers the two starter recipes (what each makes, what a run
//      gives it); Add the starter recipes brings them in as drafts, never current
//   B  a person makes "A call becomes an episode with shorts" current (Make current…)
//   C  Run with… the call recording: the plan asks for a sample first — one stretch of the
//      call made clearer in the cloud — priced apart from the whole run; Skip the sample…
//      asks why (cancelled); Make the sample first agrees to the sample's price only
//   D  the run's page shows the sample with its review copy; To-dos offer no quiet
//      approval (Watch the sample); Approve the sample plans the rest, which asks for its
//      own agreement; Go on agrees to it
//   E  the run waits for Claude (kept as Claude would), makes the voices clearer and its
//      review copy, and finishes within what was agreed; the receipt says which cost was
//      the sample's; Home's Start from a recipe lists the starter
//   F  desktop light, phone dark and light: the starters, the run dialog, the run's page,
//      no sideways scrolling
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5550 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   STARTERS_CALL=ast_… STARTERS_SHOTS=<dir> node web/scripts/recipes-starters-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5550').replace(/\/$/, '');
const callClip = process.env.STARTERS_CALL;
if (!process.env.METARUSH_MINT || !callClip) {
  console.error('set METARUSH_MINT, METARUSH_DASHBOARD and STARTERS_CALL (see the header of this script)');
  process.exit(2);
}
const SHOTS = process.env.STARTERS_SHOTS ?? '';
if (SHOTS) mkdirSync(SHOTS, { recursive: true });

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail && !ok ? ` - ${detail}` : detail ? ` (${String(detail).slice(0, 160)})` : ''}`);
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

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function settle(page) {
  await page.waitForFunction(() => [...document.images].every((i) => i.complete), null, { timeout: 10_000 }).catch(() => undefined);
  await sleep(400);
}

async function shot(page, name) {
  await settle(page);
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

async function newPage(browser, { theme = 'dark', phone = false } = {}) {
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
  await page.goto(mintLink());
  await page.waitForURL(`${base}/**`);
  return page;
}

/** A part of the page as a person reads it, people's own words in curly quotes (as plainwords.mjs expects). */
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

async function plainIn(page, selector, what) {
  const text = await readText(page, selector);
  const unplain = text.split('\n').flatMap((l) => checkShown(l).map((f) => `${f.kind} ${JSON.stringify(f.text)}`));
  check(`${what} reads plainly`, text.length > 0 && unplain.length === 0, unplain.join(' | ') || `${text.length} characters`);
}

async function noSideways(page, what) {
  const w = await page.evaluate(() => document.documentElement.scrollWidth);
  check(`${what}: no sideways scrolling`, w <= 392, `scrollWidth ${w}`);
}

const runOf = async (page, id) => (await api(page, 'recipe.runs', { run: id })).run;

async function openRecipes(page, query = '') {
  await page.goto(`${base}/library/recipes${query}`);
  await page.locator('[data-library-section="recipes"]').waitFor({ timeout: 20_000 });
  await settle(page);
}

/** Confirms the styled question that is open (its confirm button by name). */
async function confirmDialog(page, name) {
  const d = page.locator('[data-mr-dialog="open"]').last(); // the top one (a question over the run dialog)
  await d.waitFor({ timeout: 15_000 });
  const text = await d.innerText();
  await d.getByRole('button', { name }).click();
  return text;
}

const browser = await chromium.launch({ args: ['--autoplay-policy=no-user-gesture-required'] });
let runId = '';
try {
  const page = await newPage(browser);

  /* ---------- A: the starters, added as drafts ---------- */
  await openRecipes(page);
  const starters = page.locator('[data-starter-recipes]');
  await starters.waitFor({ timeout: 20_000 });
  check('A: two starter recipes are offered', (await starters.locator('[data-starter]').count()) === 2 && /A call becomes an episode with shorts/.test(await starters.innerText()) && /A release tag becomes a release video/.test(await starters.innerText()));
  await plainIn(page, '[data-starter-recipes]', 'A: the starter recipes');
  await shot(page, 'A-starters-desktop-dark');
  {
    const phone = await newPage(browser, { theme: 'light', phone: true });
    await openRecipes(phone);
    await phone.locator('[data-starter-recipes]').waitFor({ timeout: 20_000 });
    await shot(phone, 'A-starters-phone-light');
    await noSideways(phone, 'A phone: the starter recipes');
    await phone.context().close();
  }
  await starters.locator('[data-add-starters]').click();
  await page.locator('[data-recipe-card="call-episode"]').waitFor({ timeout: 60_000 });
  await page.locator('[data-recipe-card="release-video"]').waitFor({ timeout: 20_000 });
  // The cards can show while the second one is still coming in: wait for both drafts.
  const added =
    (await waitFor(async () => {
      const rows = (await api(page, 'recipe.list')).recipes.filter((r) => r.name === 'call-episode' || r.name === 'release-video');
      return rows.length === 2 && rows.every((r) => r.draft === 1) ? rows : null;
    }, 30_000)) ?? (await api(page, 'recipe.list')).recipes.filter((r) => r.name === 'call-episode' || r.name === 'release-video');
  check('A: both came in as drafts, neither current', added.length === 2 && added.every((r) => r.current === 0 && r.draft === 1), JSON.stringify(added.map((r) => [r.name, r.current, r.draft])));
  const gone = await waitFor(async () => ((await page.locator('[data-starter-recipes]').count()) === 0 ? true : null), 15_000);
  const states = (await api(page, 'recipe.starters')).starters.map((s) => `${s.name}:${s.state}`);
  check('A: the starters strip is gone once both are here', !!gone && states.every((s) => s.endsWith(':here')), states.join(', '));
  check('A: the cards say they are drafts', /Draft only/.test(await page.locator('[data-recipe-card="call-episode"]').innerText()));
  await shot(page, 'A-added-desktop-dark');

  /* ---------- B: a person makes it current ---------- */
  await openRecipes(page, '?recipe=call-episode');
  await page.locator('[data-recipe-page="call-episode"]').waitFor({ timeout: 20_000 });
  await page.locator('[data-make-current="1"]').first().click();
  const mc = await confirmDialog(page, 'Make it current');
  check('B: Make current… asks first', /Runs make it from now on/.test(mc));
  const current = await waitFor(async () => ((await api(page, 'recipe.show', { recipe: 'call-episode' })).recipe.current === 1 ? true : null), 20_000);
  check('B: version 1 is current', !!current);
  await plainIn(page, '[data-recipe-page]', 'B: the starter’s page');

  /* ---------- C: Run with… the call: a sample first ---------- */
  await openRecipes(page, '?recipe=call-episode');
  await page.locator('[data-recipe-page] [data-run-with="call-episode"]').click();
  const dlg = page.locator('[data-run-dialog]');
  await dlg.waitFor({ timeout: 20_000 });
  await dlg.locator('[data-input-choose="call"]').click();
  const picker = page.locator('[data-clip-picker]');
  await picker.waitFor({ timeout: 15_000 });
  await picker.locator('[data-pick-clip="The garden call"]').click();
  await picker.locator('[data-picker-use]').click();
  await dlg.locator('[data-run-input="episode"] input').fill('Episode 7: the garden');
  const sampled = await waitFor(async () => ((await dlg.locator('[data-plan-sample]').count()) === 1 && (await dlg.getAttribute('data-plan-ready')) === 'yes' ? true : null), 60_000);
  check('C: the plan asks for a sample first', !!sampled);
  const sampleText = await dlg.locator('[data-plan-sample]').innerText();
  check('C: the sample is one stretch of the call made clearer in the cloud, priced apart', /clean-up in the cloud/.test(sampleText) && /in the whole run/.test(sampleText), sampleText);
  const startLabel = (await dlg.locator('[data-run-start]').innerText()).trim();
  check('C: Run says it makes the sample first, at its price', /^Make the sample first \(about \$/.test(startLabel), startLabel);
  check('C: the steps start with the sample and your approval', (await dlg.locator('[data-step="sample"]').count()) === 1 && (await dlg.locator('[data-step="approve"]').count()) === 1);
  await plainIn(page, '[data-run-dialog]', 'C: the run dialog');
  await shot(page, 'C-run-dialog-sample-desktop-dark');
  await dlg.locator('[data-run-skip-sample]').click();
  const why = page.locator('[data-mr-dialog="open"]').last();
  await why.waitFor({ timeout: 10_000 });
  check('C: Skip the sample… asks why', /Say why you go without the sample/.test(await why.innerText()));
  await shot(page, 'C-skip-asks-why-desktop-dark');
  await why.getByRole('button', { name: 'Cancel' }).click();
  await dlg.locator('[data-run-start]').click();
  const agree = await confirmDialog(page, 'Make the sample');
  check('C: the styled question agrees to the sample only', /on a sample first/.test(agree) && /You agree now only to the sample/.test(agree), agree.slice(0, 200));
  await page.waitForURL(/\/library\/recipes\?run=rrn_/, { timeout: 30_000 });
  runId = new URL(page.url()).searchParams.get('run') ?? '';
  let run = await runOf(page, runId);
  check('C: the run starts with its sample, the sample’s price agreed', run.sample?.state === 'making' || run.sample?.state === 'waiting', JSON.stringify(run.sample?.state));
  check('C: what is agreed is the sample’s price', Math.abs((run.agreed?.upTo ?? 0) - (run.sample?.estimate?.usd ?? -1)) < 0.0015, JSON.stringify(run.agreed));

  /* ---------- D: the sample, watched and approved ---------- */
  const runPage = page.locator(`[data-run-page="${runId}"]`);
  await runPage.locator('[data-run-waits="sample"]').waitFor({ timeout: 300_000 });
  check('D: the run’s page shows the sample with its review copy', (await runPage.locator('[data-run-sample-video]').count()) === 1);
  await plainIn(page, '[data-run-waits="sample"]', 'D: the sample waiting');
  await shot(page, 'D-sample-waits-desktop-dark');
  const todos = await api(page, 'todos.list', { kind: 'recipe_run' });
  const td = todos.todos.find((t) => t.context?.recipe?.runId === runId);
  check('D: its to-do offers no quiet approval', !!td && td.suggestions.length === 1 && td.suggestions[0].value === 'stop', JSON.stringify(td?.suggestions));
  await page.goto(`${base}/todos`);
  const todoCard = page.locator(`[data-run-todo="${runId}"]`);
  await todoCard.waitFor({ timeout: 20_000 });
  check('D: To-dos say Watch the sample, with no Go on', (await todoCard.locator('[data-run-go-on]').count()) === 0 && /Watch the sample/.test(await todoCard.innerText()));
  await shot(page, 'D-todos-desktop-dark');
  {
    const phone = await newPage(browser, { theme: 'dark', phone: true });
    await phone.goto(`${base}/library/recipes?run=${runId}`);
    await phone.locator('[data-run-waits="sample"]').waitFor({ timeout: 20_000 });
    await shot(phone, 'D-sample-waits-phone-dark');
    await noSideways(phone, 'D phone: the sample waiting');
    await phone.context().close();
  }
  await page.goto(`${base}/library/recipes?run=${runId}`);
  await runPage.locator('[data-run-sample-approve]').click();
  const ok = await confirmDialog(page, 'Approve the sample');
  check('D: approving says the rest is planned then', /The rest of the run is planned then/.test(ok));
  await runPage.locator('[data-run-waits="person"]').waitFor({ timeout: 30_000 });
  run = await runOf(page, runId);
  check('D: the rest is planned and asks for its own agreement', run.sample?.state === 'approved' && run.waitingUsd > 0 && /the rest of the run costs/.test(run.waiting), JSON.stringify({ s: run.sample?.state, w: run.waiting }));
  await shot(page, 'D-rest-asks-desktop-dark');
  await runPage.locator('[data-run-go-on]').click();
  await confirmDialog(page, /Agree to/);

  /* ---------- E: the rest of the run ---------- */
  const forClaude = await waitFor(async () => {
    const r = await runOf(page, runId);
    return r.state === 'waiting' && r.waitingFor === 'claude' ? r : null;
  }, 120_000, 1500);
  check('E: the run waits for Claude to choose the moments', !!forClaude, JSON.stringify((await runOf(page, runId)).waiting));
  await api(page, 'recipe.picks', { run: runId, keep: true });
  const done = await waitFor(async () => {
    const r = await runOf(page, runId);
    if (r.state === 'failed' || r.state === 'stopped') throw new Error(`the run ${r.state}: ${r.problem}`);
    return r.state === 'done' ? r : null;
  }, 600_000, 2000);
  check('E: the run is ready for review', !!done, JSON.stringify((await runOf(page, runId)).stateLabel));
  if (done) {
    check('E: it spent within what was agreed', done.spent.usd <= done.agreed.upTo + 0.001, JSON.stringify({ spent: done.spent, agreed: done.agreed.upTo }));
    const rc = await api(page, 'project.receipt', { projectId: done.projectId });
    check('E: the receipt says which cost was the sample’s', rc.samples?.length === 1 && rc.samples[0].state === 'approved' && rc.lines.some((l) => /A sample came first in run 1/.test(l)), JSON.stringify(rc.samples));
    await page.goto(`${base}/library/recipes?run=${runId}`);
    await runPage.locator('[data-run-sample="approved"]').waitFor({ timeout: 20_000 });
    await shot(page, 'E-run-done-desktop-dark');
  }
  await page.goto(`${base}/`);
  const start = page.locator('[data-start-from-recipe]');
  await start.locator('[data-start-recipe="call-episode"]').waitFor({ timeout: 20_000 });
  check('E: Home’s Start from a recipe lists the starter', true);
  await shot(page, 'E-home-desktop-dark');

  /* ---------- F: light ---------- */
  const light = await newPage(browser, { theme: 'light' });
  await light.goto(`${base}/library/recipes?run=${runId}`);
  await light.locator(`[data-run-page="${runId}"]`).waitFor({ timeout: 20_000 });
  await shot(light, 'F-run-desktop-light');
  await light.context().close();
} catch (e) {
  check('the run finished', false, String(e?.stack ?? e));
} finally {
  await browser.close();
}
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed${SHOTS ? `; screenshots in ${SHOTS}` : ''}`);
process.exit(failed.length ? 1 : 0);
