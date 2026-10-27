#!/usr/bin/env node
// End-to-end check of the Library's Recipes tab (D201–D208) in the dashboard, against a
// seeded throwaway daemon that talks only to a fake ElevenLabs and a fake Gemini
// (scripts/recipes-e2e.sh does all of it):
//   A  the Recipes tab sits in the Library next to People and Motion; empty at first
//   B  "Save as a recipe…" in the project's "…" menu shows the proposal — what changes
//      each run and what stays, with why — the shot's words move to "Changes each run",
//      runs make a project each, a hint; saving opens the recipe's page
//   C  the gallery: the card with its example, what changes, Run with…
//   D  the recipe's page: version 1 current, the inputs, sections and rules in plain words
//   E  Run with…: a new headline, another voice and new words for the shot; the plan's
//      steps say who does each; the estimate "about $0.13" opens to Google Gemini and
//      ElevenLabs; Run asks in a styled question and agrees to exactly the estimate
//   F  the run's page follows the run live; a budget set meanwhile on the run's new
//      project makes it wait for a person: the page, To-dos, the bell and the activity
//      bar say so; Go on (after the budget is raised) lets it finish
//   G  the links to the new project, the version and the review copy; "Made with the
//      “Garden story” recipe, run 1" on the project and on the version
//   H  estimates off (Settings → Preferences): Run again… asks no money question, only
//      the line that budgets still apply, and starts without one
//   I  new footage: the plan says Claude chooses the moments (who does each)
//   K  (D212) two runs side by side: their review copies play together, the sound from one,
//      what changed between them below
//   L  (D212) a draft's versions row says what it changed; a draft shows what it changes from
//      the current version; Make current… says so before you decide
//   M  (D212) Studio's Home: Start from a recipe opens Run with…
//   J  desktop light, phone dark and light: the gallery, a recipe, a run and the dialog, two runs
//      side by side, Home, no sideways scrolling
// Claude's recipe cards in the panel (D211) are web/scripts/recipes-cards-e2e.mjs, run next.
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5390 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   RECIPES_PROJECT=prj_… RECIPES_CUT=rev_… RECIPES_GEMINI_FAKE=http://127.0.0.1:5392 RECIPES_SHOTS=<dir> node web/scripts/recipes-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5390').replace(/\/$/, '');
const project = process.env.RECIPES_PROJECT;
const cut = process.env.RECIPES_CUT;
const geminiFake = process.env.RECIPES_GEMINI_FAKE ?? '';
if (!process.env.METARUSH_MINT || !project || !cut) {
  console.error('set METARUSH_MINT, METARUSH_DASHBOARD, RECIPES_PROJECT and RECIPES_CUT (see the header of this script)');
  process.exit(2);
}
const SHOTS = process.env.RECIPES_SHOTS ?? '';
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
  await sleep(400); // dialogs, menus and toasts finish appearing
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

/** The run dialog's plan, once it reflects the form (the plan's summary changes with it). */
async function planShows(page, fn, ms = 30_000) {
  return waitFor(async () => {
    const d = page.locator('[data-run-dialog]');
    if ((await d.getAttribute('aria-busy')) === 'true') return null;
    const summary = (await d.locator('[data-plan-summary]').textContent({ timeout: 1000 })) ?? '';
    return fn(summary, d) ? summary : null;
  }, ms);
}

const browser = await chromium.launch({ args: ['--autoplay-policy=no-user-gesture-required'] });
let runId = '';
try {
  const page = await newPage(browser);

  /* ---------- the cut that worked: its AI voice, its review copy, approved (as a person) ---------- */
  const tts = await api(page, 'voiceover.tts', { revisionId: cut, apply: true });
  const version = tts.applied?.revisionId;
  check('the cut’s two lines in the AI voice (the fake ElevenLabs)', !!version, JSON.stringify(tts.applied ?? tts.summary));
  const example = await api(page, 'render.submit', { revisionId: version, quality: 'review', noQA: true });
  const rendered = await waitFor(async () => {
    const st = (await api(page, 'render.status', { id: example.render.id })).render.state;
    if (st === 'failed' || st === 'canceled') throw new Error(`the review copy ${st}`);
    return st === 'available';
  }, 240_000, 2000);
  check('its review copy is made (the example a recipe shows)', !!rendered);
  await api(page, 'revision.approve', { id: version, note: 'make this again' });

  /* ---------- A: the tab, empty at first ---------- */
  await page.goto(`${base}/library`);
  const tabLink = page.locator('[data-library-tab="recipes"]');
  await tabLink.waitFor({ timeout: 20_000 });
  check('A: the Library has a Recipes tab after People and Motion', (await page.locator('[data-library-tab]').allTextContents()).map((t) => t.trim()).join(',').includes('Recipes'));
  await tabLink.click();
  await page.waitForURL(`${base}/library/recipes`);
  await page.locator('[data-recipes-gallery]').getByText('No recipes yet', { exact: true }).waitFor({ timeout: 15_000 });
  check('A: no recipes yet, and it says how to save one', ((await page.locator('[data-recipes-gallery]').textContent()) ?? '').includes('Save as a recipe…'));
  await shot(page, 'A-empty-desktop-dark');

  /* ---------- B: Save as a recipe… ---------- */
  await page.goto(`${base}/p/${project}`);
  await page.getByRole('button', { name: 'More for Garden story' }).click();
  await page.getByRole('menuitem', { name: /Save as a recipe/ }).click();
  const save = page.locator('[data-save-dialog]');
  await save.waitFor({ timeout: 40_000 });
  const vary = (await save.locator('[data-proposal-list="vary"]').innerText()).replace(/\s+/g, ' ');
  const stay = (await save.locator('[data-proposal-list="stay"]').innerText()).replace(/\s+/g, ' ');
  check('B: the proposal: the headline, the AI voice and the footage change each run', /Grow with us/.test(vary) && /Ada/.test(vary) && /footage/i.test(vary), vary);
  check('B: the shot, the call to action and the narration words stay, each saying why', /Paper boat/.test(stay) && /Visit the garden/.test(stay) && /Narration line 1/.test(stay) && /unless you vary it/.test(stay), stay);
  await shot(page, 'B-save-proposal-desktop-dark');
  await save.locator('[data-move="boat-prompt"]').click();
  const moved = await waitFor(async () => (await save.locator('[data-proposal-list="vary"] [data-proposal-item="boat-prompt"]').count()) === 1, 20_000);
  check('B: the shot’s words move to “Changes each run”', !!moved);
  await save.getByRole('radio', { name: 'A new project' }).click();
  const each = await waitFor(async () => /a new project each time/.test((await save.textContent()) ?? ''), 20_000);
  check('B: runs make a project each', !!each);
  await save.locator('textarea').fill('Open on the boat.');
  await sleep(600);
  await plainIn(page, '[data-save-dialog]', 'B: the proposal');
  await shot(page, 'B-save-moved-desktop-dark');
  await save.locator('[data-save-recipe]').click();
  await page.waitForURL(/\/library\/recipes\?recipe=garden-story/, { timeout: 40_000 });
  check('B: saving opens the recipe’s page', true, page.url());

  /* ---------- D: its page ---------- */
  const rp = page.locator('[data-recipe-page="garden-story"]');
  await rp.waitFor({ timeout: 20_000 });
  const rpText = (await rp.innerText()).replace(/\s+/g, ' ');
  check('D: version 1 is current (a person saved it)', (await rp.locator('[data-recipe-version="1"][data-version-state="current"]').count()) === 1, rpText.slice(0, 200));
  check('D: it lands in a new project each time', /a new project each time/.test(rpText));
  check('D: its inputs, in plain words: the headline, the AI voice, the shot’s words', (await rp.locator('[data-recipe-input]').count()) >= 4 && /The headline on the end card/i.test(rpText) && /The AI voice/.test(rpText) && /words the shot/.test(rpText), rpText.slice(0, 400));
  check('D: its sections: the boat the same every run, Claude chooses the moments of the walk', /The same every run/.test(rpText) && /Claude chooses/.test(rpText));
  check('D: the hint for Claude', /Open on the boat\./.test(rpText));
  check('D: its example plays', (await rp.locator('video').count()) === 1);
  await plainIn(page, '[data-recipe-page]', 'D: the recipe’s page');
  await shot(page, 'D-recipe-desktop-dark');

  /* ---------- C: the gallery ---------- */
  await openRecipes(page);
  const card = page.locator('[data-recipe-card="garden-story"]');
  await card.waitFor({ timeout: 20_000 });
  const cardText = (await card.innerText()).replace(/\s+/g, ' ');
  check('C: the card: its name, what it makes, where runs go, what changes, no runs yet', /Garden story/.test(cardText) && /a new project each time/.test(cardText) && /Changes each run/.test(cardText) && /No runs yet/.test(cardText), cardText);
  check('C: the card shows its example (a still of the video it was saved from)', /Example/.test(cardText) && (await card.locator('img[src*="/media/renders/"]').count()) <= 1);
  await plainIn(page, '[data-recipes-gallery]', 'C: the gallery');
  await shot(page, 'C-gallery-desktop-dark');

  /* ---------- E: Run with… ---------- */
  await card.locator('[data-run-with="garden-story"]').click();
  const dlg = page.locator('[data-run-dialog]');
  await dlg.waitFor({ timeout: 20_000 });
  await planShows(page, (s) => /costs/.test(s));
  await dlg.locator('[data-run-input="headline"] input').fill('Spring is here');
  const voiceSelect = dlg.locator('[data-run-input="voice"] select');
  if (await voiceSelect.count()) await voiceSelect.selectOption('Bram');
  else await dlg.locator('[data-run-input="voice"] input').fill('Bram');
  await dlg.locator('[data-run-input="boat-prompt"] textarea').fill('a blue paper boat on a pond');
  const priced = await waitFor(async () => {
    const changed = await dlg.locator('[data-run-input][data-changed="yes"]').count();
    const total = (await dlg.locator('[data-plan-estimate] [data-cost-total]').textContent({ timeout: 1000 })) ?? '';
    const steps = await dlg.locator('[data-step]').allTextContents();
    return changed === 3 && /about \$0\.1\d/.test(total) && steps.some((s) => s.includes('Make the generated shots')) && steps.some((s) => s.includes('Make the AI voice')) ? total : null;
  }, 40_000);
  check('E: the plan prices the new shot and the voice: about $0.13', !!priced, priced ?? '');
  const steps = await dlg.locator('[data-step]').allTextContents();
  check('E: the plan’s steps say who does each (MetaRush)', (await dlg.locator('[data-step-who="metarush"]').count()) >= 5, steps.join(' | '));
  check('E: the plan says where the video goes: a new project', /a new project, “Garden story 1”/.test((await dlg.locator('[data-plan-summary]').textContent()) ?? ''));
  await dlg.locator('[data-plan-estimate] [data-cost-toggle]').click();
  const parts = (await dlg.locator('[data-plan-estimate] [data-cost-parts]').innerText()).replace(/\s+/g, ' ');
  check('E: the estimate opens to the services, models and sources', /Google Gemini/.test(parts) && /ElevenLabs/.test(parts) && /Bram/.test(parts) && /a blue paper boat on a pond/.test(parts) && /Make the generated shots/.test(parts), parts.slice(0, 300));
  // A generated shot is an expensive kind (D294): the run offers a sample of it first, priced on its own.
  check('E: a sample of the shot is offered first, with its own price', (await dlg.locator('[data-run-start]').getAttribute('data-sample-first')) === 'yes' &&
    /Make the sample first \(about \$/.test((await dlg.locator('[data-run-start]').textContent()) ?? '') && (await dlg.locator('[data-plan-sample]').count()) === 1);
  await plainIn(page, '[data-run-dialog]', 'E: the run dialog');
  await shot(page, 'E-run-dialog-desktop-dark');
  const est = (await api(page, 'recipe.plan', { recipe: 'garden-story', inputs: [{ name: 'headline', text: 'Spring is here' }, { name: 'voice', text: 'Bram' }, { name: 'boat-prompt', text: 'a blue paper boat on a pond' }] })).estimate;
  // The fake Gemini makes the next shot take a few looks: a window to change the budget of the run's project.
  if (geminiFake) await fetch(`${geminiFake}/__script`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify([{ kind: 'delay', n: 5 }]) });
  // This run checks the whole run, so it goes without the sample (the starter recipes' run checks the sample).
  await dlg.locator('[data-run-skip-sample]').click();
  const why = page.getByRole('dialog', { name: 'Run it without the sample?' });
  await why.getByLabel('Why').fill('We made this shot style last week');
  await why.getByRole('button', { name: 'Go without the sample' }).click();
  const agree = page.getByRole('dialog').filter({ hasText: 'Spend about $0.1' });
  await agree.waitFor({ timeout: 10_000 });
  const agreeText = (await agree.innerText()).replace(/\s+/g, ' ');
  check('E: Run asks in a styled question, naming what is paid for', /One agreement covers the whole run/.test(agreeText) && /make the generated shots/.test(agreeText) && /Budgets, today’s limits/.test(agreeText), agreeText);
  await shot(page, 'E-agree-desktop-dark');
  await agree.getByRole('button', { name: 'Agree and run' }).click();
  await page.waitForURL(/\/library\/recipes\?run=/, { timeout: 30_000 });
  runId = new URL(page.url()).searchParams.get('run') ?? '';
  const started = await runOf(page, runId);
  check('E: it started with exactly the estimate agreed', !!started.agreed && Math.abs(started.agreed.upTo - est.usd) < 0.0006 && !started.agreed.skipped, JSON.stringify(started.agreed));

  /* ---------- F: the run, live; it waits for a person; Go on ---------- */
  const runPage = page.locator(`[data-run-page="${runId}"]`);
  await runPage.waitFor({ timeout: 20_000 });
  const withProject = await waitFor(async () => (await runOf(page, runId)).projectId, 20_000, 250);
  check('F: the run made its project', !!withProject, String(withProject));
  // Meanwhile someone sets the new project's AI voice budget too low: the run will wait for a person.
  if (withProject) await api(page, 'provider.settings', { name: 'elevenlabs', projectId: withProject, budgetCredits: 1 });
  const goingLive = await waitFor(async () => (await runPage.locator('[data-step="shots"][data-step-state="running"]').count()) === 1, 20_000, 300);
  check('F: the page follows the run: making the shot', !!goingLive);
  await shot(page, 'F-run-going-desktop-dark');
  const waiting = await waitFor(async () => ((await runPage.getAttribute('data-run-state')) === 'waiting' && (await runPage.getAttribute('data-waiting-for')) === 'person' ? true : null), 120_000, 1000);
  check('F: the shot is made, then the run waits for a person (the budget)', !!waiting, `${await runPage.getAttribute('data-run-state')}`);
  const waitText = (await runPage.locator('[data-run-waits="person"]').innerText().catch(() => '')).replace(/\s+/g, ' ');
  check('F: it says what it waits for, plainly', /budget/.test(waitText) && /let the run go on/.test(waitText), waitText);
  check('F: the shot’s step is done, the voice waits', (await runPage.locator('[data-step="shots"][data-step-state="done"]').count()) === 1 && (await runPage.locator('[data-step="voice"][data-step-state="waiting"]').count()) === 1);
  await plainIn(page, '[data-run-page]', 'F: the run’s page');
  await shot(page, 'F-run-waits-desktop-dark');
  // To-dos, the bell and the activity bar say so.
  await page.goto(`${base}/todos`);
  const todo = page.locator('[data-todo-kind="recipe_run"]');
  await todo.waitFor({ timeout: 20_000 });
  const todoText = (await todo.innerText()).replace(/\s+/g, ' ');
  check('F: To-dos ask: Go on or Stop this run', /Run 1 of “Garden story”/.test(todoText) && (await todo.locator('[data-run-go-on]').count()) === 1 && (await todo.locator('[data-run-stop]').count()) === 1, todoText);
  await shot(page, 'F-todos-desktop-dark');
  const bell = (await api(page, 'notification.list', { limit: 20 })).notifications.find((n) => n.kind === 'recipe_run');
  check('F: the bell rings: the run waits', !!bell && /waits/.test(bell.title), bell ? bell.title : 'none');
  await page.getByRole('button', { name: /^Notifications/ }).click();
  await page.getByRole('dialog', { name: 'Notifications' }).getByText(/Run 1 of .Garden story. waits/).waitFor({ timeout: 10_000 });
  await shot(page, 'F-bell-desktop-dark');
  await page.keyboard.press('Escape');
  if (await page.getByRole('dialog', { name: 'Notifications' }).isVisible().catch(() => false)) await page.getByRole('button', { name: /^Notifications/ }).click();
  const act = await api(page, 'activity.summary', {});
  const item = act.items.find((i) => i.category === 'recipe');
  check('F: the activity bar has the run (Counts.recipes)', act.counts.recipes >= 1 && !!item && item.state === 'waiting', JSON.stringify({ recipes: act.counts.recipes, state: item?.state, stage: item?.stageLabel }));
  await page.locator('[data-activity-button]').click();
  await page.locator('#mr-activity-drawer').getByText(/Run 1 of .Garden story./).first().waitFor({ timeout: 10_000 });
  await shot(page, 'F-activity-desktop-dark');
  await page.keyboard.press('Escape');
  // The person raises the budget, then lets the run go on from its page.
  await api(page, 'provider.settings', { name: 'elevenlabs', projectId: withProject, budgetCredits: 0 });
  await page.goto(`${base}/library/recipes?run=${runId}`);
  await runPage.locator('[data-run-go-on]').click();
  const wentOn = await waitFor(async () => ((await runPage.getAttribute('data-run-state')) !== 'waiting' ? true : null), 20_000, 300);
  check('F: Go on: it carries on', !!wentOn);
  const done = await waitFor(async () => ((await runPage.getAttribute('data-run-state')) === 'done' ? true : null), 300_000, 2000);
  const finished = await runOf(page, runId);
  check('F: it is ready for review (the voice made, a review copy and its quality check)', !!done && finished.state === 'done' && !!finished.render, `${finished.state}: ${finished.stateLabel}`);
  check('F: every step done or not needed', finished.steps.every((s) => s.state === 'done' || s.state === 'skipped'), finished.steps.map((s) => `${s.id}:${s.state}`).join(' '));
  check('F: it spent what the services charged, within what was agreed', finished.spent.usd > 0 && finished.spent.usd <= finished.agreed.upTo + 0.001, `${finished.spentWords} of ${finished.agreedWords}`);
  await shot(page, 'F-run-done-desktop-dark');

  /* ---------- G: the new project and version, "Made with…" ---------- */
  check('G: links to its project, the version and the review copy', (await runPage.locator('[data-run-project]').count()) === 1 && (await runPage.locator('[data-run-version]').count()) === 1 && (await runPage.locator('[data-run-review]').count()) === 1);
  await runPage.locator('[data-run-project]').click();
  await page.waitForURL(`${base}/p/${finished.projectId}`);
  const mw = page.locator(`[data-made-with="${finished.ref}"]`);
  await mw.waitFor({ timeout: 20_000 });
  check('G: the project says “Made with the “Garden story” recipe, run 1”', /Made with the “Garden story” recipe, run 1/.test((await mw.innerText()).replace(/\s+/g, ' ')));
  await shot(page, 'G-project-made-with-desktop-dark');
  await page.goto(`${base}/library/recipes?run=${runId}`);
  await runPage.locator('[data-run-version]').click();
  await page.waitForURL(new RegExp(`/p/${finished.projectId}/r/`));
  await page.locator(`[data-made-with="${finished.ref}"]`).waitFor({ timeout: 20_000 });
  check('G: the version says so too, with Run again…', (await page.locator('[data-made-with-again]').count()) === 1);
  const madeVia = ((await page.locator('main').innerText().catch(() => '')) || '').includes('a recipe run');
  check('G: the version was saved “via a recipe run”', madeVia);
  await shot(page, 'G-version-made-with-desktop-dark');

  /* ---------- H: estimates off → no money question ---------- */
  await page.goto(`${base}/settings/preferences`);
  const mine = page.locator('[data-estimates-mine]');
  await mine.waitFor({ timeout: 20_000 });
  await mine.uncheck();
  const off = await waitFor(async () => (await page.locator('[data-estimates-mine="off"]').count()) === 1, 15_000);
  check('H: estimates off in Settings → Preferences', !!off);
  await page.goBack();
  await page.locator('[data-made-with-again]').click();
  const again = page.locator('[data-run-dialog]');
  await again.waitFor({ timeout: 20_000 });
  await planShows(page, (s) => /again/.test(s));
  const vs = again.locator('[data-run-input="voice"] select');
  if (await vs.count()) await vs.selectOption('Cleo');
  else await again.locator('[data-run-input="voice"] input').fill('Cleo');
  const noMoney = await waitFor(async () => {
    const planned = (await again.locator('[data-run-input="voice"] [data-input-planned]').textContent({ timeout: 1000 })) ?? '';
    return (await again.locator('[data-plan-no-estimate]').count()) === 1 && /Cleo/.test(planned) ? true : null; // the plan has the new voice
  }, 30_000);
  check('H: no money question, only the line that budgets still apply', !!noMoney && (await again.locator('[data-plan-estimate]').count()) === 0 && /budgets/.test((await again.locator('[data-plan-no-estimate]').textContent()) ?? ''));
  check('H: the plan says what changes and what is used again', (await again.locator('[data-plan-changes]').count()) === 1 && /Cleo/.test((await again.locator('[data-plan-changes]').textContent()) ?? ''));
  check('H: the button just runs it', ((await again.locator('[data-run-start]').textContent()) ?? '').trim() === 'Run it' && (await again.locator('[data-run-start]').getAttribute('data-asks-money')) === 'no');
  await plainIn(page, '[data-run-dialog]', 'H: the run dialog without estimates');
  await shot(page, 'H-again-no-estimate-desktop-dark');
  await again.locator('[data-run-start]').click();
  await page.waitForURL(/\/library\/recipes\?run=/, { timeout: 30_000 });
  check('H: it started without a money question', (await page.getByRole('dialog').filter({ hasText: 'Spend' }).count()) === 0);
  const run2Id = new URL(page.url()).searchParams.get('run') ?? '';
  const run2 = await runOf(page, run2Id);
  check('H: run 2 makes a new version of the same cut, with no amount agreed', run2.number === 2 && run2.branch === finished.branch && run2.projectId === finished.projectId && !!run2.agreed?.skipped && /estimates are off/.test(run2.agreedWords), JSON.stringify({ n: run2.number, agreed: run2.agreedWords }));
  const run2Page = page.locator(`[data-run-page="${run2Id}"]`);
  await run2Page.waitFor({ timeout: 20_000 });
  check('H: its page says no estimate was asked for', /no estimate asked for/.test((await run2Page.locator('[data-run-agreed]').textContent()) ?? ''));
  await shot(page, 'H-run2-desktop-dark');

  /* ---------- I: new footage: Claude chooses the moments ---------- */
  await openRecipes(page, '?recipe=garden-story');
  await page.locator('[data-recipe-page] [data-run-with="garden-story"]').click();
  const fresh = page.locator('[data-run-dialog]');
  await fresh.waitFor({ timeout: 20_000 });
  await fresh.locator('[data-input-choose="footage"]').click();
  const picker = page.locator('[data-clip-picker]');
  await picker.waitFor({ timeout: 15_000 });
  await picker.locator('[data-pick-clip="Kitchen chat"]').click();
  await shot(page, 'I-clip-picker-desktop-dark');
  await picker.locator('[data-picker-use]').click();
  const claudeStep = await waitFor(async () => (await fresh.locator('[data-step="claude"] [data-step-who="claude"]').count()) === 1, 30_000);
  check('I: new footage: the plan says Claude chooses the moments', !!claudeStep, (await fresh.locator('[data-step]').allTextContents()).join(' | '));
  await shot(page, 'I-run-dialog-claude-desktop-dark');
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();

  // Run 2 finishes too (no money asked: within budgets and limits).
  const run2Done = await waitFor(async () => ((await runOf(page, run2Id)).state === 'done' ? true : null), 300_000, 2000);
  check('H: run 2 is ready for review', !!run2Done);

  /* ---------- K (D212): two runs side by side ---------- */
  await openRecipes(page, '?recipe=garden-story');
  const runsList = page.locator('[data-recipe-runs]');
  await runsList.locator('[data-run-row]').nth(1).waitFor({ timeout: 20_000 });
  await runsList.getByRole('checkbox', { name: 'Compare run 2' }).check();
  await runsList.getByRole('checkbox', { name: 'Compare run 1' }).check();
  await runsList.locator('[data-compare-runs-button]').click();
  await page.waitForURL(/\/library\/recipes\?compare=rrn_[0-9a-z]+&with=rrn_/, { timeout: 20_000 });
  const sbs = page.locator('[data-side-by-side]');
  await sbs.waitFor({ timeout: 20_000 });
  check('K: side by side: run 1 on the left, run 2 on the right', (await sbs.getAttribute('data-left')) === '1' && (await sbs.getAttribute('data-right')) === '2');
  check('K: both review copies are there to play', (await sbs.locator('[data-side-copy][data-copy-ready="yes"] video').count()) === 2);
  await sbs.locator('[data-play-both]').click();
  const together = await waitFor(async () => {
    const t = await sbs.locator('video').evaluateAll((vs) => vs.map((v) => ({ t: v.currentTime, paused: v.paused, muted: v.muted })));
    return t.length === 2 && t.every((v) => !v.paused && v.t > 0.6) ? t : null;
  }, 15_000, 300);
  check('K: Play both plays them together, the sound from one', !!together && Math.abs(together[0].t - together[1].t) < 0.6 && together.filter((v) => !v.muted).length === 1, JSON.stringify(together));
  await sbs.locator('[data-sound-from="2"]').click();
  const swapped = await sbs.locator('video').evaluateAll((vs) => vs.map((v) => v.muted));
  check('K: the sound comes from the other one on request', swapped[0] === true && swapped[1] === false, JSON.stringify(swapped));
  await shot(page, 'K-side-by-side-desktop-dark');
  await sbs.locator('[data-play-both]').click();
  const changedRows = await sbs.locator('[data-compare-input][data-changed="yes"]').allTextContents();
  check('K: below them, what changed: the AI voice', changedRows.some((r) => /AI voice/.test(r)), changedRows.join(' | '));
  await plainIn(page, '[data-side-by-side]', 'K: two runs side by side');

  /* ---------- L (D212): what each version changed, in plain words ---------- */
  const draft = await api(page, 'recipe.update', { recipe: 'garden-story', addHints: ['End on the garden at dusk.'], note: 'a quieter ending' });
  check('L: a draft of the recipe (version 2)', draft.version.number === 2 && draft.version.state === 'draft', draft.message);
  await openRecipes(page, '?recipe=garden-story');
  const versions = page.locator('[data-recipe-versions]');
  await versions.locator('[data-recipe-version="2"]').waitFor({ timeout: 20_000 });
  check('L: version 1 is the first one', /The first version/.test((await versions.locator('[data-recipe-version="1"]').innerText()) ?? ''));
  await versions.locator('[data-version-changes-toggle="2"]').click();
  const changes2 = versions.locator('[data-version-changes="2"]');
  await changes2.waitFor({ timeout: 15_000 });
  const v2Text = (await changes2.innerText()).replace(/\s+/g, ' ');
  check('L: version 2 says what it changed from version 1', /Version 2 changes 1 thing from version 1/.test(v2Text) && /New hint: “End on the garden at dusk\.”/.test(v2Text), v2Text);
  await plainIn(page, '[data-recipe-versions]', 'L: the versions');
  await shot(page, 'L-versions-desktop-dark');
  await openRecipes(page, '?recipe=garden-story&version=2');
  const banner = page.locator('[data-version-banner]');
  await banner.locator('[data-against-current]').waitFor({ timeout: 15_000 });
  check('L: a draft shows what it changes from the current version', /End on the garden at dusk/.test(await banner.innerText()));
  await banner.locator('[data-make-current="2"]').click();
  const mc = page.locator('[data-make-current-dialog]');
  await mc.waitFor({ timeout: 15_000 });
  check('L: Make current… says what it changes before you decide', /Compared with version 1/.test(await mc.innerText()) && /End on the garden at dusk/.test(await mc.innerText()));
  await shot(page, 'L-make-current-desktop-dark');
  await page.locator('[data-mr-dialog="open"]').getByRole('button', { name: 'Cancel' }).click();

  /* ---------- M (D212): Start from a recipe on Studio's Home ---------- */
  await page.goto(`${base}/`);
  const start = page.locator('[data-start-from-recipe]');
  await start.locator('[data-start-recipe="garden-story"]').waitFor({ timeout: 20_000 });
  check('M: Home lists the recipe with Run with…', (await start.locator('[data-start-run-with="garden-story"]').count()) === 1 && /Garden story/.test(await start.innerText()));
  await start.locator('[data-start-run-with="garden-story"]').click();
  await page.locator('[data-run-dialog]').waitFor({ timeout: 20_000 });
  check('M: Run with… opens the run dialog from Home', true);
  await shot(page, 'M-home-run-dialog-desktop-dark');
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();
  await plainIn(page, '[data-start-from-recipe]', 'M: Start from a recipe');
  const compareUrl = (await api(page, 'recipe.compare', { left: runId, right: run2Id })).url;

  /* ---------- J: light, phone dark and light ---------- */
  for (const [theme, phone] of [['light', false], ['dark', true], ['light', true]]) {
    const tag = `${phone ? 'phone' : 'desktop'}-${theme}`;
    const p = await newPage(browser, { theme, phone });
    await openRecipes(p);
    await p.locator('[data-recipe-card="garden-story"]').waitFor({ timeout: 20_000 });
    await shot(p, `J-gallery-${tag}`);
    if (phone) await noSideways(p, `J ${tag}: the gallery`);
    await openRecipes(p, '?recipe=garden-story');
    await p.locator('[data-recipe-page]').waitFor({ timeout: 20_000 });
    await shot(p, `J-recipe-${tag}`);
    if (phone) await noSideways(p, `J ${tag}: the recipe’s page`);
    await p.goto(`${base}/library/recipes?run=${runId}`);
    await p.locator(`[data-run-page="${runId}"]`).waitFor({ timeout: 20_000 });
    await shot(p, `J-run-${tag}`);
    if (phone) await noSideways(p, `J ${tag}: the run’s page`);
    await p.goto(`${base}/library/recipes?recipe=garden-story`);
    await p.locator('[data-recipe-page] [data-run-with="garden-story"]').click();
    await p.locator('[data-run-dialog][data-plan-ready="yes"]').waitFor({ timeout: 30_000 });
    await shot(p, `J-run-dialog-${tag}`);
    if (phone) await noSideways(p, `J ${tag}: the run dialog`);
    await p.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();
    await p.goto(`${base}${new URL(compareUrl).pathname}${new URL(compareUrl).search}`);
    await p.locator('[data-side-by-side-players]').waitFor({ timeout: 20_000 });
    await shot(p, `J-side-by-side-${tag}`);
    if (phone) await noSideways(p, `J ${tag}: two runs side by side`);
    await p.goto(`${base}/`);
    await p.locator('[data-start-recipe="garden-story"]').waitFor({ timeout: 20_000 });
    await p.locator('[data-start-from-recipe]').scrollIntoViewIfNeeded();
    await shot(p, `J-home-start-${tag}`);
    if (phone) await noSideways(p, `J ${tag}: Home with Start from a recipe`);
    if (phone) {
      await p.goto(`${base}/p/${project}`);
      await p.getByRole('button', { name: 'More for Garden story' }).click();
      await p.getByRole('menuitem', { name: /Save as a recipe/ }).click();
      await p.locator('[data-save-dialog]').waitFor({ timeout: 40_000 });
      await shot(p, `J-save-${tag}`);
      await noSideways(p, `J ${tag}: Save as a recipe`);
    }
    await p.context().close();
  }
} catch (e) {
  check('the run finished', false, String(e?.stack ?? e));
} finally {
  await browser.close();
}
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed${SHOTS ? `; screenshots in ${SHOTS}` : ''}`);
process.exit(failed.length ? 1 : 0);
