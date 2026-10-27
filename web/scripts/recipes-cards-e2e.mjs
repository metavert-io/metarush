#!/usr/bin/env node
// Claude's recipe cards in the panel (D211), in a real browser, with a fake `claude`
// that asks for runs, a save, a change and making a version current. Two scenarios:
//
//   priced (scripts/recipes-e2e.sh, after web/scripts/recipes-e2e.mjs; fakes for ElevenLabs and Gemini):
//     P1 a run card shows its plan — where the video goes, what this run gives each input, each step and
//        who does it — and its estimate, which opens to ElevenLabs and the voice; it is never combined
//     P2 Not quite… sends it back with a note; Claude's new card has the note's headline
//     P3 under Auto with estimates off, a new run card still waits for a person; nothing starts
//     P4 Run asks in a styled question (one agreement covers the run) and starts it with exactly the
//        estimate agreed; the card follows the run until its sample waits for a person (Claude hears)
//     P5 a save card (a draft: what changes each run and what stays), a change card (what it changes)
//     P6 a make-current card says what the version changes; the styled question says it again
//     P7 desktop light, phone dark and light, no sideways scrolling
//   free (scripts/cards-e2e.sh; no paid service):
//     F1 a save card saves a draft; F2 making it current (no current version yet) asks first;
//     F3 a run with nothing to pay asks only whether to start it, and starts
// No native browser dialog; every card reads plainly (web/scripts/plainwords.mjs).
//
//   METARUSH_DASHBOARD=… METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   RECIPES_CARDS_SCENARIO=priced|free RECIPES_PROJECT=prj_… RECIPES_CARDS_SHOTS=<dir> node web/scripts/recipes-cards-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { checkPanel } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const scenario = process.env.RECIPES_CARDS_SCENARIO ?? 'priced';
const project = process.env.RECIPES_PROJECT;
const SHOTS = process.env.RECIPES_CARDS_SHOTS ?? '';
if (!process.env.METARUSH_MINT || !base || !project) {
  console.error('set METARUSH_DASHBOARD, METARUSH_MINT and RECIPES_PROJECT (see the header of this script)');
  process.exit(2);
}
if (SHOTS) mkdirSync(SHOTS, { recursive: true });

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

/** Asks Claude (the fake) and waits until it has answered and its cards are there. */
async function ask(page, convId, prompt) {
  const before = convId ? (await api(page, 'assistant.show', { id: convId, events: false })).turns.length : 0;
  const out = convId ? await api(page, 'assistant.continue', { id: convId, prompt, projectId: project }) : await api(page, 'assistant.ask', { prompt, projectId: project });
  const id = out.conversation.id;
  const cv = await waitFor(async () => {
    const c = await api(page, 'assistant.show', { id, events: false });
    const last = c.turns[c.turns.length - 1];
    return c.state === 'idle' && c.turns.length > before && last && /succeeded|failed/.test(last.state) && (c.queue ?? []).length === 0 ? c : null;
  }, 60_000, 300);
  if (!cv) throw new Error(`Claude never finished: ${prompt}`);
  return cv;
}

/** A card by its ID (cards of runs can have the same title). */
const cardOf = (page, id) => page.locator(`article[data-card-id="${id}"]`);

async function readable(loc) {
  return (await loc.innerText()).split('\n').flatMap((l) => checkPanel(l).map((f) => `${f.kind} ${f.text} in ${JSON.stringify(l.slice(0, 120))}`));
}

const browser = await chromium.launch();
try {
  const { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const state = await ctx.storageState();
  // Ask first, estimates on (a person's own choice; the earlier run may have turned them off).
  await api(page, 'assistant.mode.set', { mode: 'ask' });
  await api(page, 'account.preferences.set', { estimates: true });

  if (scenario === 'priced') {
    /* ---------- P1: a run card with its plan and estimate ---------- */
    let cv = await ask(page, '', 'Make the garden story again for the summer');
    const conv = cv.id;
    const first = cv.actions.find((a) => a.kind === 'recipe run');
    // The recipe makes a generated shot, an expensive kind (D294): the run makes a sample of it first.
    check('P1: Claude’s run is a card, people only, with the sample’s estimate in the title',
      !!first && first.op === 'recipe.run' && first.humanOnly && first.state === 'pending' && /^Run “Garden story” \(a sample first: (about \$\d+\.\d\d|less than a cent)\)$/.test(first.title) && !!first.estimate,
      first ? `${first.title} ${first.state}` : JSON.stringify(cv.actions.map((a) => a.title)));
    await page.goto(`${base}/p/${project}?ask=${conv}`);
    const c1 = cardOf(page, first.id);
    await c1.waitFor({ timeout: 30_000 });
    await c1.scrollIntoViewIfNeeded();
    const c1Text = (await c1.innerText()).replace(/\s+/g, ' ');
    check('P1: the plan: where the video goes and what this run gives', /Makes a new project, “Garden story \d+”/.test(c1Text) && (await c1.locator('[data-card-input][data-given="yes"]').count()) >= 2 && /Summer is here/.test(c1Text) && /a yellow paper boat at sunset/.test(c1Text), c1Text.slice(0, 300));
    check('P1: never combined with other cards', (await page.locator('[data-combined]').count()) === 0);
    const stepsOpen = await c1.locator('[data-run-steps]').count();
    if (!stepsOpen) await c1.locator('[data-card-steps-toggle]').click();
    check('P1: each step and who does it', (await c1.locator('[data-step]').count()) >= 5 && (await c1.locator('[data-step-who="metarush"]').count()) >= 4);
    await c1.locator('[data-cost-toggle]').click();
    const parts = (await c1.locator('[data-cost-parts]').innerText()).replace(/\s+/g, ' ');
    check('P1: the estimate opens to the service and what the shot shows', /Google Gemini/.test(parts) && /a yellow paper boat at sunset/.test(parts), parts.slice(0, 200));
    const unplain = await readable(c1);
    check('P1: the card reads plainly', unplain.length === 0, unplain.join(' | '));
    check('P1: its button says the price', /^Run it \((about \$|less than a cent)/.test((await c1.getByRole('button', { name: /^Run it/ }).innerText()).trim()));
    await shot(page, 'P1-run-card-desktop-dark');

    /* ---------- P2: Not quite… ---------- */
    await c1.locator('[data-not-quite]').click();
    await c1.locator('[data-not-quite-box] textarea').fill('Use the headline Summer days');
    await shot(page, 'P2-not-quite-desktop-dark');
    await c1.locator('[data-not-quite-send]').click();
    await c1.getByText('Not quite', { exact: true }).first().waitFor({ timeout: 15_000 });
    cv = await waitFor(async () => {
      const c = await api(page, 'assistant.show', { id: conv, events: false });
      return c.state === 'idle' && c.actions.filter((a) => a.kind === 'recipe run').length >= 2 ? c : null;
    }, 60_000, 400);
    const second = cv?.actions.filter((a) => a.kind === 'recipe run')[1];
    const firstNow = cv?.actions.find((a) => a.id === first.id);
    check('P2: Not quite sent it back; Claude’s new card has the new headline',
      firstNow?.state === 'not_quite' && !!second && second.state === 'pending' && (second.recipe?.inputs ?? []).some((i) => /Summer days/.test(i.words)), JSON.stringify(second?.recipe?.inputs ?? []).slice(0, 200));

    /* ---------- P3: Auto with estimates off never starts a run ---------- */
    const runsBefore = (await api(page, 'recipe.runs', { recipe: 'garden-story' })).runs.length;
    await api(page, 'assistant.mode.set', { mode: 'auto' });
    await api(page, 'account.preferences.set', { estimates: false });
    cv = await ask(page, conv, 'One more on Auto, in the autumn');
    const third = cv.actions.filter((a) => a.kind === 'recipe run')[2];
    await sleep(3000);
    const thirdNow = (await api(page, 'assistant.show', { id: conv, events: false })).actions.find((a) => a.id === third?.id);
    const runsAfter = (await api(page, 'recipe.runs', { recipe: 'garden-story' })).runs.length;
    check('P3: under Auto with estimates off, the run card still waits for a person; nothing started',
      !!thirdNow && thirdNow.state === 'pending' && !thirdNow.auto && runsAfter === runsBefore, `${thirdNow?.state} auto=${thirdNow?.auto} runs ${runsBefore}→${runsAfter}`);
    await page.reload();
    const c3 = cardOf(page, third.id);
    await c3.waitFor({ timeout: 30_000 });
    check('P3: the card says it needs you, not that it runs by itself', (await c3.getByText('Needs you', { exact: true }).count()) === 1 && (await c3.getByText(/Running by itself|Done by itself/).count()) === 0);
    await shot(page, 'P3-auto-still-asks-desktop-dark');
    await c3.getByRole('button', { name: 'Not now', exact: true }).click();
    await api(page, 'assistant.mode.set', { mode: 'ask' });
    await api(page, 'account.preferences.set', { estimates: true });

    /* ---------- P4: Run: the agreement, the run, the card's work ---------- */
    await page.reload();
    const c2 = cardOf(page, second.id);
    await c2.waitFor({ timeout: 30_000 });
    await c2.scrollIntoViewIfNeeded();
    await c2.getByRole('button', { name: /^Run it/ }).click();
    const agree = page.locator('[data-mr-dialog="open"]');
    await agree.waitFor({ timeout: 10_000 });
    const agreeText = (await agree.innerText()).replace(/\s+/g, ' ');
    check('P4: Run asks in a styled question: one agreement covers the run', /Spend (about \$\d+\.\d\d|less than a cent) on this run\?/.test(agreeText) && /one agreement covers the whole run/i.test(agreeText) && /Budgets, today’s limits/.test(agreeText), agreeText.slice(0, 260));
    await shot(page, 'P4-agree-desktop-dark');
    await agree.getByRole('button', { name: 'Agree and run' }).click();
    await c2.getByText('Done', { exact: true }).or(c2.getByText('Working on it', { exact: true })).first().waitFor({ timeout: 60_000 });
    const decided = (await api(page, 'assistant.show', { id: conv, events: false })).actions.find((a) => a.id === second.id);
    const runId = decided?.work?.runId;
    const run = runId ? (await api(page, 'recipe.runs', { run: runId })).run : null;
    check('P4: it started with exactly the estimate agreed, the card’s work is the run',
      decided?.state === 'done' && /^Started run \d+ of “Garden story”/.test(decided.result ?? '') && !!run && Math.abs((run.agreed?.upTo ?? -1) - second.estimate.usd) < 0.0006 && !run.agreed?.skipped && decided.work?.kind === 'run',
      `${decided?.result} | agreed ${JSON.stringify(run?.agreed)}`);
    await shot(page, 'P4-run-started-desktop-dark');
    const ended = await waitFor(async () => {
      const a = (await api(page, 'assistant.show', { id: conv, events: false })).actions.find((x) => x.id === second.id);
      return a?.work && a.work.state !== 'running' ? a.work : null;
    }, 420_000, 3000);
    check('P4: when the sample is ready, the card says so (and Claude hears): a person watches and approves it', !!ended && /waiting for you: watch and listen to the sample, then approve it/.test(ended.outcome ?? ''), ended ? `${ended.state}: ${ended.outcome}` : 'still running');
    await page.reload();
    await cardOf(page, second.id).waitFor({ timeout: 30_000 });
    await cardOf(page, second.id).scrollIntoViewIfNeeded();
    await shot(page, 'P4-run-ready-desktop-dark');

    /* ---------- P5: a save (a draft) and a change ---------- */
    cv = await ask(page, conv, 'Save it for kids and make the ending calmer');
    const save = cv.actions.find((a) => a.kind === 'recipe save');
    const change = cv.actions.find((a) => a.kind === 'recipe update');
    await page.reload();
    const cs = cardOf(page, save.id);
    await cs.waitFor({ timeout: 30_000 });
    await cs.scrollIntoViewIfNeeded();
    const csText = (await cs.innerText()).replace(/\s+/g, ' ');
    check('P5: the save card: what changes each run and what stays, saved as a draft',
      save.title === 'Save “Garden story for kids” as a recipe' && (await cs.locator('[data-card-list="varies"] li').count()) >= 1 && /Stays the same/.test(csText) && /Saved as a draft/.test(csText), csText.slice(0, 260));
    await shot(page, 'P5-save-card-desktop-dark');
    await cs.getByRole('button', { name: 'Save the draft', exact: true }).click();
    await cs.getByText('Done', { exact: true }).waitFor({ timeout: 60_000 });
    const kids = (await api(page, 'recipe.list', {})).recipes.find((r) => r.title === 'Garden story for kids');
    check('P5: saved as a draft (a person makes it current)', !!kids && kids.current === 0 && kids.draft === 1, JSON.stringify(kids ?? {}).slice(0, 160));
    const cc = cardOf(page, change.id);
    await cc.scrollIntoViewIfNeeded();
    const ccText = (await cc.innerText()).replace(/\s+/g, ' ');
    check('P5: the change card says what it changes, with Claude’s note', /New hint: “End on the garden\.”/.test(ccText) && /a calmer ending/.test(ccText) && (await cc.locator('[data-difference-part="hints"]').count()) === 1, ccText.slice(0, 260));
    const ccPlain = await readable(cc);
    check('P5: the change card reads plainly', ccPlain.length === 0, ccPlain.join(' | '));
    await cc.getByRole('button', { name: 'Save the draft', exact: true }).click();
    await cc.getByText('Done', { exact: true }).waitFor({ timeout: 60_000 });
    const show = await api(page, 'recipe.show', { recipe: 'garden-story', version: 'draft' });
    check('P5: a draft of the recipe, with the hint', show.version.state === 'draft' && show.hints.includes('End on the garden.'), `version ${show.version.number} ${show.version.state}`);

    /* ---------- P6: making it current ---------- */
    cv = await ask(page, conv, 'Make the new version current');
    const pub = cv.actions.find((a) => a.kind === 'recipe publish');
    await page.reload();
    const cp = cardOf(page, pub.id);
    await cp.waitFor({ timeout: 30_000 });
    await cp.scrollIntoViewIfNeeded();
    const cpText = (await cp.innerText()).replace(/\s+/g, ' ');
    check('P6: making a version current is a people-only card saying what it changes',
      pub.humanOnly && /^Make version \d+ of “Garden story” current$/.test(pub.title) && /Compared with version \d+, the one runs make now/.test(cpText) && /End on the garden/.test(cpText), cpText.slice(0, 260));
    await shot(page, 'P6-make-current-card-desktop-dark');
    await cp.getByRole('button', { name: 'Make it current', exact: true }).click();
    const dlg = page.locator('[data-mr-dialog="open"]');
    await dlg.waitFor({ timeout: 10_000 });
    check('P6: the styled question says what it changes', /End on the garden/.test(await dlg.innerText()));
    await shot(page, 'P6-make-current-question-desktop-dark');
    await dlg.getByRole('button', { name: 'Make it current' }).click();
    await cp.getByText('Done', { exact: true }).waitFor({ timeout: 60_000 });
    const now = await api(page, 'recipe.show', { recipe: 'garden-story' });
    check('P6: the version is current', now.version.state === 'current' && now.hints.includes('End on the garden.'), `version ${now.version.number}`);
    await ctx.close();

    /* ---------- P7: light, phone dark and light ---------- */
    for (const [theme, phone] of [['light', false], ['dark', true], ['light', true]]) {
      const tag = `${phone ? 'phone' : 'desktop'}-${theme}`;
      const v = await newPage(browser, { theme, phone, state });
      await v.page.goto(`${base}/p/${project}?ask=${conv}`);
      const c = cardOf(v.page, second.id);
      await c.waitFor({ timeout: 30_000 });
      await c.scrollIntoViewIfNeeded();
      await shot(v.page, `P7-run-card-${tag}`);
      if (phone) await noSideways(v.page, `P7 ${tag}: the panel with the cards`);
      await v.ctx.close();
    }
  } else {
    /* ---------- F1: a save card saves a draft ---------- */
    let cv = await ask(page, '', 'Save the short as a recipe');
    const conv = cv.id;
    const save = cv.actions.find((a) => a.kind === 'recipe save');
    await page.goto(`${base}/p/${project}?ask=${conv}`);
    const cs = cardOf(page, save.id);
    await cs.waitFor({ timeout: 30_000 });
    check('F1: the save card shows what changes and what stays', (await cs.locator('[data-card-proposal]').count()) === 1 && /Saved as a draft/.test(await cs.innerText()));
    const plain1 = await readable(cs);
    check('F1: the save card reads plainly', plain1.length === 0, plain1.join(' | '));
    await shot(page, 'F1-save-card-desktop-dark');
    await cs.getByRole('button', { name: 'Save the draft', exact: true }).click();
    await cs.getByText('Done', { exact: true }).waitFor({ timeout: 60_000 });
    const r = (await api(page, 'recipe.list', {})).recipes.find((x) => x.name === 'short-test');
    check('F1: a draft, not current', !!r && r.current === 0, JSON.stringify(r ?? {}).slice(0, 160));

    /* ---------- F2: making it current ---------- */
    cv = await ask(page, conv, 'Make the recipe current');
    const pub = cv.actions.find((a) => a.kind === 'recipe publish');
    await page.reload();
    const cp = cardOf(page, pub.id);
    await cp.waitFor({ timeout: 30_000 });
    await cp.scrollIntoViewIfNeeded();
    check('F2: no version is current yet: runs will make this one', /No version is current yet/.test(await cp.innerText()));
    await cp.getByRole('button', { name: 'Make it current', exact: true }).click();
    await page.locator('[data-mr-dialog="open"]').getByRole('button', { name: 'Make it current' }).click();
    await cp.getByText('Done', { exact: true }).waitFor({ timeout: 60_000 });

    /* ---------- F3: a run with nothing to pay ---------- */
    cv = await ask(page, conv, 'Run the recipe');
    const run = cv.actions.find((a) => a.kind === 'recipe run');
    check('F3: a run with nothing to pay: no estimate, people only', !!run && run.state === 'pending' && run.humanOnly && !run.estimate && run.title === 'Run “Short test”', run ? `${run.title} ${run.state} ${run.unavailable ?? ''}` : 'none');
    await page.reload();
    const cr = cardOf(page, run.id);
    await cr.waitFor({ timeout: 30_000 });
    await cr.scrollIntoViewIfNeeded();
    await shot(page, 'F3-run-card-desktop-dark');
    await cr.getByRole('button', { name: 'Run it', exact: true }).click();
    const q = page.locator('[data-mr-dialog="open"]');
    await q.waitFor({ timeout: 10_000 });
    const qText = (await q.innerText()).replace(/\s+/g, ' ');
    check('F3: it asks only whether to start it', /Run “Short test”\?/.test(qText) && /No outside AI service is paid for/.test(qText) && !/Spend/.test(qText), qText.slice(0, 200));
    await q.getByRole('button', { name: 'Run it' }).click();
    await cr.getByText('Done', { exact: true }).or(cr.getByText('Working on it', { exact: true })).first().waitFor({ timeout: 60_000 });
    const decided = (await api(page, 'assistant.show', { id: conv, events: false })).actions.find((a) => a.id === run.id);
    check('F3: it started; the run is the card’s work', decided?.state === 'done' && /^Started run 1 of “Short test”/.test(decided.result ?? '') && decided.work?.kind === 'run', decided?.result);
    // The daemon's engine takes the run on from there (never inside the click, where it would race the engine).
    await sleep(8000);
    const going = decided?.work?.runId ? (await api(page, 'recipe.runs', { run: decided.work.runId })).run : null;
    check('F3: the run goes on (it did not fail at the start)', !!going && going.state !== 'failed' && going.state !== 'stopped', going ? `${going.state}: ${going.stateLabel} ${going.problem ?? ''}` : 'none');
    await shot(page, 'F3-run-started-desktop-dark');
    await ctx.close();
    const v = await newPage(browser, { theme: 'light', phone: true, state });
    await v.page.goto(`${base}/p/${project}?ask=${conv}`);
    await cardOf(v.page, run.id).waitFor({ timeout: 30_000 });
    await shot(v.page, 'F3-cards-phone-light');
    await noSideways(v.page, 'F3 phone light: the panel with the cards');
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
