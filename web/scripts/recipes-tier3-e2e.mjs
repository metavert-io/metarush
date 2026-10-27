#!/usr/bin/env node
// Recipes Tier 3 in the dashboard (D217, D218) and the panel's last recipe cards, in a real browser, on the workspace
// scripts/recipes-e2e.sh left behind (the "Garden story" recipe, its runs, a fake claude, the fakes for ElevenLabs and
// Gemini; the dashboard started with METARUSH_RECIPE_TRIGGER_SECONDS=2 so planning by itself looks every 2 s):
//   S  suggested changes (D217): two runs whose boat a person shortened, then approved → "You shortened … in both runs"
//      on the recipe's page, with what a draft would change; leaving it out asks to tick one; Save as a draft opens the
//      draft (a person makes it current; nothing changed for runs)
//   T  planning by itself (D218): the footage input takes the project's footage; New footage on → a clip joins the
//      project → once it settles, a plan and the bell ("the run is planned"), and no run starts though the person has
//      Auto on and estimates off; the bell's link opens Run with… on the recipe's page; Set aside; every week; off
//   C  Claude's cards (D211 completed): keep a waiting run's first picks (the run goes on), save a recipe sheet Claude
//      wrote as a draft (what it changes), save the suggested changes as a draft — none by itself, each plain
//   J  phone dark and light: the recipe's page with its suggestions and planning, no sideways scrolling
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5390 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   RECIPES_PROJECT=prj_… RECIPES_KITCHEN=ast_… RECIPES_CLAUDE_SCRIPT=<home>/../claude.json RECIPES_TIER3_SHOTS=<dir> \
//   node web/scripts/recipes-tier3-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkPanel, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const project = process.env.RECIPES_PROJECT;
const kitchen = process.env.RECIPES_KITCHEN;
const claudeScript = process.env.RECIPES_CLAUDE_SCRIPT;
const SHOTS = process.env.RECIPES_TIER3_SHOTS ?? '';
if (!process.env.METARUSH_MINT || !base || !project || !kitchen || !claudeScript) {
  console.error('set METARUSH_DASHBOARD, METARUSH_MINT, RECIPES_PROJECT, RECIPES_KITCHEN and RECIPES_CLAUDE_SCRIPT (see the header of this script)');
  process.exit(2);
}
if (SHOTS) mkdirSync(SHOTS, { recursive: true });
const RECIPE = 'garden-story';

const results = [];
const problems = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail && !ok ? ` - ${detail}` : detail ? ` (${String(detail).slice(0, 160)})` : ''}`);
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function waitFor(fn, ms, every = 500) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > end) return null;
    await sleep(every);
  }
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

async function newPage(browser, { theme = 'dark', phone = false, state } = {}) {
  const ctx = await browser.newContext({
    viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 940 },
    deviceScaleFactor: phone ? 2 : 1,
    hasTouch: phone,
    isMobile: phone,
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
  page.on('dialog', (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  return { ctx, page };
}

let shotN = 0;
async function shot(page, name) {
  await page.waitForTimeout(450);
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, `${String(++shotN).padStart(2, '0')}-${name}.png`) });
}
async function noSideways(page, what) {
  const w = await page.evaluate(() => document.documentElement.scrollWidth);
  check(`${what}: no sideways scrolling`, w <= 392, `scrollWidth ${w}`);
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
async function cardPlain(loc) {
  return (await loc.innerText()).split('\n').flatMap((l) => checkPanel(l).map((f) => `${f.kind} ${f.text} in ${JSON.stringify(l.slice(0, 120))}`));
}

const runOf = async (page, id) => (await api(page, 'recipe.runs', { run: id })).run;
const doneRuns = async (page) => (await api(page, 'recipe.runs', { recipe: RECIPE, limit: 50 })).runs;

/** Plans and starts a run as the person (agreeing to its estimate), then waits until it is done or waits. */
async function startRun(page, inputs = []) {
  const plan = await api(page, 'recipe.plan', { recipe: RECIPE, inputs });
  if (!plan.ready) throw new Error(`the run can't start: ${plan.problems.join(' ')}`);
  const maxUsd = plan.askForEstimate && plan.estimate.usd > 0 ? plan.estimate.usd : undefined;
  const out = await api(page, 'recipe.run', { recipe: RECIPE, inputs, fingerprint: plan.fingerprint, ...(maxUsd !== undefined ? { maxUsd } : {}) });
  return out.run.id;
}

/** Asks Claude (the fake) in a new conversation and waits until it has answered and its cards are there. */
async function ask(page, prompt) {
  const out = await api(page, 'assistant.ask', { prompt, projectId: project });
  const id = out.conversation.id;
  const cv = await waitFor(async () => {
    const c = await api(page, 'assistant.show', { id, events: false });
    const last = c.turns[c.turns.length - 1];
    return c.state === 'idle' && last && /succeeded|failed/.test(last.state) && (c.queue ?? []).length === 0 ? c : null;
  }, 60_000, 300);
  if (!cv) throw new Error(`Claude never finished: ${prompt}`);
  return cv;
}
const cardOf = (page, id) => page.locator(`article[data-card-id="${id}"]`);

/**
 * Adds turns to the front of the fake claude's script: a new conversation reads them. The fake plays the first turn
 * whose words are in the message, and a message carries what happened in the project so far (earlier prompts too),
 * so each turn goes in just before the conversation that asks for it.
 */
function addTurns(turns) {
  const script = JSON.parse(readFileSync(claudeScript, 'utf8'));
  script.turns = [...turns, ...(script.turns ?? [])];
  writeFileSync(claudeScript, JSON.stringify(script));
}
const denied = (command) => ({ tool: { name: 'Bash', denied: true, input: { command } } });

const browser = await chromium.launch();
try {
  const { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const state = await ctx.storageState();
  await api(page, 'assistant.mode.set', { mode: 'ask' });
  await api(page, 'account.preferences.set', { estimates: true });

  /* ---------- S: suggested changes (D217) ---------- */
  let made = (await doneRuns(page)).filter((r) => r.state === 'done' && !r.repeats);
  while (made.length < 2) {
    const id = await startRun(page);
    const done = await waitFor(async () => ((await runOf(page, id)).state === 'done' ? true : null), 420_000, 3000);
    if (!done) throw new Error(`run ${id} never finished: ${(await runOf(page, id)).stateLabel}`);
    made = (await doneRuns(page)).filter((r) => r.state === 'done' && !r.repeats);
  }
  for (const brief of made.slice(0, 2)) {
    // A person shortens the boat (the section that stays the same: unlocked on purpose), then approves it.
    const run = await runOf(page, brief.id);
    const plan = { baseRevisionId: run.revision, branch: run.branch, instruction: 'a shorter boat', ops: [{ op: 'updateClip', id: 'boat', set: { out: 2 } }] };
    const chk = await api(page, 'edit.check', { plan, allowUnlock: true });
    if (!chk.valid) throw new Error(`the shorter boat: ${JSON.stringify(chk.errors)}`);
    const applied = await api(page, 'edit.apply', { plan, confirmToken: chk.confirmToken, allowUnlock: true });
    await api(page, 'revision.approve', { id: applied.revision.id, note: 'shorter boat' });
  }
  await page.goto(`${base}/library/recipes?recipe=${RECIPE}`);
  const sug = page.locator('[data-recipe-suggestions]');
  await sug.waitFor({ timeout: 30_000 });
  const boat = sug.locator('[data-suggestion^="length:"]').first();
  const shown = await waitFor(async () => ((await boat.count()) ? (await boat.innerText()).replace(/\s+/g, ' ') : null), 20_000);
  check('S: the recipe’s page suggests what people kept changing, with why', !!shown && /You shortened “[^”]+” in (both runs|\d of \d runs): from 3 s to about 2 s\./.test(shown), shown ?? (await sug.innerText()));
  const draftText = (await sug.locator('[data-suggestions-draft]').innerText().catch(() => '')).replace(/\s+/g, ' ');
  check('S: and what a draft would change', /A draft from version \d+ would change/.test(draftText) && /plays|now/.test(draftText), draftText.slice(0, 200));
  await plainIn(page, '[data-recipe-suggestions]', 'S: the suggested changes');
  await sug.scrollIntoViewIfNeeded();
  await shot(page, 'S-suggestions-desktop-dark');
  await boat.locator('input[type="checkbox"]').uncheck();
  const tick = await waitFor(async () => /Tick at least one/.test(await sug.innerText()) && (await sug.locator('[data-save-suggestions]').isDisabled()), 10_000);
  check('S: leaving every suggestion out asks to tick one', !!tick);
  await boat.locator('input[type="checkbox"]').check();
  const before = await api(page, 'recipe.show', { recipe: RECIPE });
  await sug.locator('[data-save-suggestions]').click();
  await page.waitForURL(/[?&]version=\d+/, { timeout: 30_000 });
  const v = Number(new URL(page.url()).searchParams.get('version'));
  const after = await api(page, 'recipe.show', { recipe: RECIPE, version: String(v) });
  check('S: Save as a draft opens the draft; runs keep making the current version', after.version.state === 'draft' && after.recipe.current === before.recipe.current && /Suggested from runs/.test(after.version.note ?? ''), `version ${v} ${after.version.state}, current ${after.recipe.current}`);
  // The banner comes with the draft's page, a moment after its address (a slower computer takes longer).
  const makeCurrent = page.locator(`[data-version-banner] [data-make-current="${v}"]`);
  await makeCurrent.first().waitFor({ timeout: 20_000 }).catch(() => {});
  check('S: the draft’s banner offers Make current… (a person’s)', (await makeCurrent.count()) === 1);
  await shot(page, 'S-draft-desktop-dark');

  /* ---------- T: planning by itself (D218) ---------- */
  const cur = await api(page, 'recipe.show', { recipe: RECIPE, version: 'current' });
  const inputs = cur.doc.inputs.map((i) => (i.kind === 'footage' ? { ...i, from: { project } } : i));
  const upd = await api(page, 'recipe.update', { recipe: RECIPE, version: 'current', patch: { inputs }, note: 'footage from the project' });
  await api(page, 'recipe.publish', { recipe: RECIPE, version: String(upd.version.number) });
  await page.goto(`${base}/library/recipes?recipe=${RECIPE}`);
  const trig = page.locator('[data-recipe-triggers]');
  await trig.waitFor({ timeout: 30_000 });
  check('T: off at first', (await trig.getAttribute('data-triggers-on')) === 'no');
  await trig.scrollIntoViewIfNeeded();
  await trig.locator('[data-trigger-new-files]').check();
  const onNow = await waitFor(async () => (await trig.getAttribute('data-triggers-on')) === 'yes' && (await trig.locator('[data-trigger-new-files="on"]').count()) === 1, 15_000);
  const who = (await trig.locator('[data-triggers-who]').innerText().catch(() => '')).replace(/\s+/g, ' ');
  check('T: New footage on: it tells you, and says which computer looks', !!onNow && /^Tells /.test(who) && /looks/.test(who), who);
  await plainIn(page, '[data-recipe-triggers]', 'T: planning by itself');
  await shot(page, 'T-triggers-on-desktop-dark');
  // Auto on, estimates off: planning by itself still never starts a run.
  await api(page, 'assistant.mode.set', { mode: 'auto' });
  await api(page, 'account.preferences.set', { estimates: false });
  const runsBefore = (await doneRuns(page)).length;
  await api(page, 'asset.link', { assetId: kitchen, projectId: project });
  const planned = await waitFor(async () => (await api(page, 'recipe.triggers', { recipe: RECIPE })).recipes[0].prepared, 90_000, 1000);
  check('T: new footage, once settled: a plan', !!planned && planned.why === 'new-files' && /^New footage in “Garden story” \(\d clips?\)$/.test(planned.words), JSON.stringify(planned ?? {}).slice(0, 200));
  const bell = (await api(page, 'notification.list', { limit: 30 })).notifications.find((n) => n.kind === 'recipe_planned');
  check('T: the bell rings: the run is planned', !!bell && /for “Garden story”: the run is planned, /.test(bell.title) && /planned=1/.test(bell.link ?? ''), bell ? `${bell.title} → ${bell.link}` : 'none');
  await sleep(3000);
  check('T: no run started (Auto on, estimates off)', (await doneRuns(page)).length === runsBefore, `${runsBefore} → ${(await doneRuns(page)).length}`);
  await page.getByRole('button', { name: /^Notifications/ }).click();
  await page.getByRole('dialog', { name: 'Notifications' }).getByText(/the run is planned/).first().waitFor({ timeout: 10_000 });
  await shot(page, 'T-bell-desktop-dark');
  await page.keyboard.press('Escape');
  await page.goto(`${base}${bell.link}`);
  const dlg = page.locator('[data-run-dialog]');
  await dlg.waitFor({ timeout: 30_000 });
  check('T: the bell’s link opens Run with… on the recipe', true);
  await shot(page, 'T-run-dialog-desktop-dark');
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();
  const banner = page.locator('[data-planned-run]');
  await banner.waitFor({ timeout: 15_000 });
  check('T: the plan waits at the top with Run… and Set aside', (await banner.locator('[data-run-planned]').count()) === 1 && /nothing starts until a person runs it/.test(await banner.innerText()));
  check('T: the link opened it once (the address no longer asks)', !new URL(page.url()).searchParams.get('planned'));
  await plainIn(page, '[data-planned-run]', 'T: the plan made by itself');
  await shot(page, 'T-planned-banner-desktop-dark');
  await banner.locator('[data-set-aside]').click();
  const gone = await waitFor(async () => (await page.locator('[data-planned-run]').count()) === 0, 15_000);
  check('T: Set aside drops it; nothing had started', !!gone && (await doneRuns(page)).length === runsBefore);
  await trig.locator('[data-trigger-weekly]').check();
  const weekly = await waitFor(async () => (await trig.locator('[data-trigger-weekly="on"]').count()) === 1 && /Every Monday at 09:00; the next plan/.test(await trig.innerText()), 15_000);
  check('T: every week: Monday at 09:00, with the next time', !!weekly, (await trig.innerText()).slice(0, 300));
  await shot(page, 'T-weekly-desktop-dark');
  await trig.locator('[data-trigger-weekly]').uncheck();
  await waitFor(async () => (await trig.locator('[data-trigger-weekly="off"]').count()) === 1, 15_000);
  await trig.locator('[data-trigger-new-files]').uncheck();
  const off = await waitFor(async () => (await trig.getAttribute('data-triggers-on')) === 'no', 15_000);
  check('T: both off: it doesn’t plan by itself any more', !!off);
  await api(page, 'assistant.mode.set', { mode: 'ask' });
  await api(page, 'account.preferences.set', { estimates: true });

  /* ---------- C: Claude's picks, sheet and suggestions cards ---------- */
  const waitingId = await startRun(page, [{ name: 'footage', items: [kitchen] }]);
  const waits = await waitFor(async () => {
    const r = await runOf(page, waitingId);
    return r.state === 'waiting' && r.waitingFor === 'claude' ? r : null;
  }, 120_000, 1500);
  if (!waits) throw new Error(`the run never waited for Claude: ${(await runOf(page, waitingId)).stateLabel}`);
  const co = await api(page, 'recipe.checkout', { recipe: RECIPE });
  const hint = 'End by the pond.';
  const sheet = co.sheet.includes('\n## Hints\n') ? co.sheet.replace('\n## Hints\n', `\n## Hints\n- ${hint}\n`) : co.sheet.replace('## The cut', `## Hints\n- ${hint}\n\n## The cut`);
  addTurns([{ match: 'Keep the picks of the new run', steps: [denied(`metarush recipe picks ${waits.ref} --keep --json`), { text: 'The picks are on a card for you.' }] }]);
  let cv = await ask(page, 'Keep the picks of the new run');
  const picks = cv.actions.find((a) => a.kind === 'recipe picks');
  check('C: keeping the first picks is a card that waits for you', !!picks && picks.op === 'recipe.picks' && picks.state === 'pending' && /^Keep MetaRush's first picks in run \d+ of “Garden story”$/.test(picks.title), picks ? `${picks.title} ${picks.state}` : JSON.stringify(cv.actions.map((a) => a.title)));
  await page.goto(`${base}/p/${project}?ask=${cv.id}`);
  const cp = cardOf(page, picks.id);
  await cp.waitFor({ timeout: 30_000 });
  await cp.scrollIntoViewIfNeeded();
  check('C: the picks card lists the run’s picks', (await cp.locator('[data-card-pick]').count()) >= 1 && /goes on by itself/.test(await cp.innerText()));
  const pp = await cardPlain(cp);
  check('C: the picks card reads plainly', pp.length === 0, pp.join(' | '));
  await shot(page, 'C-picks-card-desktop-dark');
  await cp.getByRole('button', { name: 'Keep them', exact: true }).click();
  await cp.getByText('Done', { exact: true }).or(cp.getByText('Working on it', { exact: true })).first().waitFor({ timeout: 60_000 });
  const goesOn = await waitFor(async () => {
    const r = await runOf(page, waitingId);
    return !(r.state === 'waiting' && r.waitingFor === 'claude') ? r : null;
  }, 30_000, 1000);
  check('C: kept: the run goes on', !!goesOn, goesOn ? goesOn.stateLabel : 'still waiting');
  addTurns([
    {
      match: 'Tidy the recipe sheet',
      steps: [{ write: { name: 'garden.recipe.md', content: sheet } }, denied(`metarush recipe check {{proposals}}/garden.recipe.md --save --note 'a calmer ending' --json`), { text: 'The sheet is on a card.' }],
    },
  ]);
  cv = await ask(page, 'Tidy the recipe sheet');
  const sh = cv.actions.find((a) => a.kind === 'recipe check');
  if (!sh) throw new Error(`no sheet card: ${JSON.stringify(cv.actions.map((a) => a.title))}`);
  await page.goto(`${base}/p/${project}?ask=${cv.id}`);
  const cs = cardOf(page, sh.id);
  await cs.waitFor({ timeout: 30_000 });
  await cs.scrollIntoViewIfNeeded();
  const csText = (await cs.innerText()).replace(/\s+/g, ' ');
  check('C: the sheet card says what it changes', sh.op === 'recipe.write' && /New hint: “End by the pond\.”/.test(csText) && /a calmer ending/.test(csText), csText.slice(0, 260));
  const sp = await cardPlain(cs);
  check('C: the sheet card reads plainly', sp.length === 0, sp.join(' | '));
  await shot(page, 'C-sheet-card-desktop-dark');
  await cs.getByRole('button', { name: 'Save the draft', exact: true }).click();
  await cs.getByText('Done', { exact: true }).waitFor({ timeout: 60_000 });
  const withHint = await api(page, 'recipe.show', { recipe: RECIPE, version: 'draft' });
  check('C: saved as a draft with the hint', withHint.version.state === 'draft' && withHint.hints.includes(hint), `version ${withHint.version.number}`);
  addTurns([{ match: 'Save what people keep changing', steps: [denied(`metarush recipe suggestions ${RECIPE} --save --json`), { text: 'The suggested changes are on a card.' }] }]);
  cv = await ask(page, 'Save what people keep changing');
  const sg = cv.actions.find((a) => a.kind === 'recipe suggestions');
  if (!sg) throw new Error(`no suggestions card: ${JSON.stringify(cv.actions.map((a) => a.title))}`);
  await page.goto(`${base}/p/${project}?ask=${cv.id}`);
  const cg = cardOf(page, sg.id);
  await cg.waitFor({ timeout: 30_000 });
  await cg.scrollIntoViewIfNeeded();
  const cgText = (await cg.innerText()).replace(/\s+/g, ' ');
  check('C: the suggestions card gives the reasons and what the draft changes', sg.op === 'recipe.suggestions.save' && /You shortened/.test(cgText) && /A draft from version/.test(cgText), cgText.slice(0, 260));
  const gp = await cardPlain(cg);
  check('C: the suggestions card reads plainly', gp.length === 0, gp.join(' | '));
  await shot(page, 'C-suggestions-card-desktop-dark');
  await cg.getByRole('button', { name: 'Save the draft', exact: true }).click();
  await cg.getByText('Done', { exact: true }).waitFor({ timeout: 60_000 });
  const cur2 = await api(page, 'recipe.show', { recipe: RECIPE });
  check('C: still a draft: runs make the current version', cur2.recipe.draft > cur2.recipe.current, `current ${cur2.recipe.current}, draft ${cur2.recipe.draft}`);
  await ctx.close();

  /* ---------- J: phone, dark and light ---------- */
  for (const theme of ['dark', 'light']) {
    const v = await newPage(browser, { theme, phone: true, state });
    await v.page.goto(`${base}/library/recipes?recipe=${RECIPE}`);
    await v.page.locator('[data-recipe-triggers]').waitFor({ timeout: 30_000 });
    await v.page.locator('[data-recipe-suggestions]').scrollIntoViewIfNeeded();
    await shot(v.page, `J-suggestions-phone-${theme}`);
    await v.page.locator('[data-recipe-triggers]').scrollIntoViewIfNeeded();
    await shot(v.page, `J-triggers-phone-${theme}`);
    await noSideways(v.page, `J phone ${theme}: the recipe’s page`);
    await v.ctx.close();
  }
} catch (e) {
  check('the run finished', false, String(e?.stack ?? e));
} finally {
  await browser.close();
}
check('no native dialogs or page errors', problems.length === 0, problems.join(' | '));
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed${SHOTS ? `; screenshots in ${SHOTS}` : ''}`);
process.exit(failed.length ? 1 : 0);
