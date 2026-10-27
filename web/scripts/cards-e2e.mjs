#!/usr/bin/env node
// The Claude Code panel's conversation and sound cards in a real browser (D111).
// Run it through scripts/cards-e2e.sh: a throwaway installation with a Riverside
// call (lined up, cut, looked over), a music bed and a fake `claude` that, asked
// to clean up the call and fix the sound, asks for the changes. It checks that:
//   1. every card is there, in plain words (checkShown: people's own words are
//      theirs), with the right button;
//   2. the studio card shows its one-time setup first, and asking for it opens a
//      styled confirmation that says so (cancelled here: nothing is downloaded);
//   3. the cloud card can't be confirmed without a key on this computer, and says why;
//   4. confirming the reaction-heavy cut, the clean-up, Sam's angle and level and
//      the music bed puts each in the cut as a new version, and the short makes a
//      project of its own;
//   5. no native dialogs, page errors or horizontal overflow. Screenshots: desktop
//      and phone, dark and light, in CARDS_SHOTS.
// It CHANGES the project, so point it only at a throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { checkPanel } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'CARDS_PROJECT', 'CARDS_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/cards-e2e.sh does)`);
    process.exit(2);
  }
}
const projectId = process.env.CARDS_PROJECT;
const shotsDir = process.env.CARDS_SHOTS;
mkdirSync(shotsDir, { recursive: true });

const results = [];
const problems = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
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
  await page.screenshot({ path: file });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
}

// A card on its own, or a row of the combined card several cards of one reply make (D144).
const card = (page, title) => page.locator(`[aria-label="Claude asks: ${title.replace(/"/g, '\\"')}"]`).last();

async function confirmCard(page, title, button, { dialog = false } = {}) {
  const c = card(page, title);
  await c.scrollIntoViewIfNeeded();
  await c.getByRole('button', { name: button, exact: true }).click();
  if (dialog) await page.locator('[data-mr-dialog="open"] button[type="submit"]').click();
  await c.getByText('Done', { exact: true }).or(c.getByText('Did not work', { exact: true })).waitFor({ timeout: 120_000 });
  return c;
}

async function main() {
  const browser = await chromium.launch();
  const { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const state = await ctx.storageState();

  // Ask Claude (the fake), and wait for its cards.
  const asked = await api(page, 'assistant.ask', { prompt: 'Clean up the call and fix the sound', projectId });
  const convId = asked.conversation.id;
  let cv;
  for (let i = 0; i < 240; i++) {
    cv = await api(page, 'assistant.show', { id: convId, events: false });
    if (cv.state === 'idle' && cv.actions.length >= 9) break;
    await page.waitForTimeout(250);
  }
  const titles = cv.actions.map((a) => a.title);
  check('Claude asked for 9 changes, each a card', cv.actions.length === 9, titles.join(' | '));
  const byOp = (op, i = 0) => cv.actions.filter((a) => a.op === op)[i];
  const cut = cv.actions.find((a) => a.kind === 'conversation cut');
  const decide = cv.actions.find((a) => a.kind === 'conversation decide');
  const angle = cv.actions.find((a) => a.kind === 'conversation angle');
  const short = cv.actions.find((a) => a.kind === 'conversation short');
  const level = cv.actions.find((a) => a.kind === 'sound levels');
  const enhances = cv.actions.filter((a) => a.kind === 'sound enhance');
  const [clean, studio, cloud] = enhances;
  const music = cv.actions.find((a) => a.kind === 'sound music add');
  check('the cut card names the style and says it replaces the cut', cut?.title === 'Make an automatic cut of “Garden call”: reaction-heavy' && cut.details.some((d) => d.startsWith('Replaces the current cut')), cut?.title);
  check('the clean-up card takes every open suggestion', /^Accept all \d+ clean-up suggestions for “Garden call”$/.test(decide?.title ?? ''), decide?.title);
  check('the angle card names the person and the moment', angle?.title === 'Show Sam from 0:31.0 in the conversation', angle?.title);
  check("the short card names its stretch and its project", /^Make a short from 0:24\.0–0:36\.0 of “Garden call”$/.test(short?.title ?? '') && short.details[0].startsWith('A new project, “Garden call'), short?.title);
  check("a person's level", level?.title === 'Make Sam louder' && level.details.includes('Only while Sam speaks; nobody else changes'), level?.title);
  check('clean: on this computer, free', clean?.title === "Make Maya's voice clearer (on this computer)" && clean.state === 'pending', clean?.title);
  check('studio: its one-time setup first, the person decides', studio?.humanOnly === true && (studio.setUp ?? []).join() === 'studio' && /^First sets up studio quality on this computer: a one-time download of about 1\.8 GB/.test(studio.details[0]), studio?.details?.[0]);
  check('cloud: priced first, as an estimate in dollars; no key here, so it says where to add one and cannot be confirmed',
    /^Make Maya's voice clearer with ElevenLabs \((about \$\d+\.\d\d|less than a cent)\)$/.test(cloud?.title ?? '') && cloud?.state === 'unavailable' &&
      /^ElevenLabs isn't set up on this computer yet\. Add your key in Settings → Integrations, then ask again\.$/.test(cloud.unavailable ?? ''), `${cloud?.title} / ${cloud?.unavailable}`);
  check('cloud: the estimate draws on the Voice Isolator, stretch by stretch (D208)',
    (cloud?.estimate?.parts ?? []).length > 0 && cloud.estimate.parts.every((p) => p.service === 'ElevenLabs' && p.model === 'Voice Isolator' && /credits/.test(p.units) && /^“/.test(p.source ?? '')) &&
      (cloud?.title ?? '').includes(`(${cloud?.estimate?.about})`), JSON.stringify(cloud?.estimate)?.slice(0, 240));
  check('clean and studio are free on this computer: no estimate', !clean?.estimate && !studio?.estimate);
  check('music: the bed under the video', music?.title === 'Put “Music bed” under the video', music?.title);
  check('nothing changed before anyone confirmed', (await api(page, 'revision.list', { projectId })).revisions.length === 1);

  // The panel shows them.
  await page.goto(`${base}/p/${projectId}?ask=${convId}`);
  await page.locator('[data-combined]').first().waitFor({ timeout: 30_000 });
  await page.waitForTimeout(800);
  // The eight changes waiting for a click are one combined card (D144); the cloud one can't be done from here.
  const rows = await page.locator('[data-combined] li[data-waiting]').count();
  const alone = await page.locator('article[aria-label^="Claude asks:"]').count();
  check('the panel shows every card: 8 in one combined card, the one it can\'t do on its own', rows === 8 && alone === 1, `${rows} rows, ${alone} on their own`);
  const texts = await page.locator('[aria-label^="Claude asks:"]').allInnerTexts();
  const unplain = texts.flatMap((t) => t.split('\n')).flatMap((l) => checkPanel(l).map((f) => `${f.kind} ${f.text} in ${JSON.stringify(l)}`));
  check('every card reads plainly', unplain.length === 0, unplain.join(' | '));
  await shot(page, 'cards-desktop-dark');
  const studioCard = card(page, studio.title);
  await studioCard.scrollIntoViewIfNeeded();
  check('the studio card says the setup comes first', (await studioCard.getByText('One-time setup first').count()) === 1);
  await studioCard.getByRole('button', { name: 'Set up and make it clearer', exact: true }).click();
  const dlg = page.locator('[data-mr-dialog="open"]');
  await dlg.waitFor({ timeout: 10_000 });
  const dlgText = await dlg.innerText();
  check('asking for studio opens a styled confirmation with the 1.8 GB notice', /1\.8 GB/.test(dlgText) && /Make Sam's voice clearer in studio quality\?/.test(dlgText), dlgText.replace(/\s+/g, ' ').slice(0, 200));
  await shot(page, 'studio-confirm-desktop-dark');
  await dlg.getByRole('button', { name: 'Cancel' }).click();
  await dlg.waitFor({ state: 'detached', timeout: 10_000 });
  check('cancelling leaves the studio card waiting', !!(await studioCard.getAttribute('data-waiting')));
  const cloudCard = card(page, cloud.title);
  check('the cloud card has no button', (await cloudCard.getByRole('button', { name: 'Make it clearer' }).count()) === 0 && (await cloudCard.getByText('Not from here').count()) === 1);
  // The estimate on the card opens to what it draws on (D208).
  await cloudCard.scrollIntoViewIfNeeded();
  const total = (await cloudCard.locator('[data-cost-total]').innerText().catch(() => '')).trim();
  await cloudCard.locator('[data-cost-toggle]').click();
  await cloudCard.locator('[data-cost-parts]').waitFor({ timeout: 10_000 });
  const partsText = (await cloudCard.locator('[data-cost-parts]').innerText()).replace(/\s+/g, ' ');
  check('the cloud card: "Estimate: about $…" opens to the stretches, Voice Isolator, minutes and credits',
    total === cloud.estimate.about && /Voice Isolator/.test(partsText) && /credits/.test(partsText) && /ElevenLabs' prices of/.test(partsText) && (await cloudCard.locator('[data-cost-part]').count()) === cloud.estimate.parts.length,
    `${total} | ${partsText.slice(0, 200)}`);
  const costPlain = partsText.split(' · ').flatMap((l) => checkPanel(l).map((f) => `${f.kind} ${f.text}`));
  check('the estimate reads plainly', costPlain.length === 0, costPlain.join(' | '));
  await shot(page, 'cloud-estimate-open-desktop-dark');

  // Confirm them in the order a person might: the new cut first, then the rest on top of it.
  for (const [a, button] of [[cut, 'Make the cut'], [decide, 'Accept'], [angle, 'Change it'], [level, 'Change it'], [music, 'Add the music']]) {
    const c = await confirmCard(page, a.title, button);
    const done = (await c.getByText('Done', { exact: true }).count()) === 1;
    check(`confirmed: ${a.title}`, done, (await c.innerText()).replace(/\s+/g, ' ').slice(0, 240));
  }
  const revs = (await api(page, 'revision.list', { projectId })).revisions;
  check('each change is a new version', revs.length === 6, `${revs.length} versions`);
  const head = await api(page, 'revision.show', { id: revs.find((r) => r.isHead)?.id ?? revs[0].id });
  const spec = head.revision?.spec ?? head.spec;
  const shots = spec.clips.flatMap((c) => c.shots ?? []);
  check("the cut is reaction-heavy, with Sam's angle at 0:31", shots.some((s) => Math.abs(s.at - 31) < 0.01 && s.member === 'sam'), `${shots.length} shots`);
  check('Sam is louder over his own speech', (spec.mix?.voices ?? []).some((v) => v.name === 'Sam' && v.gainDb === 3));
  check('the music bed is under the video', (spec.audio ?? []).some((t) => t.role === 'music'));
  const conv = (await api(page, 'conversation.show', { conversation: 'Garden call' })).conversation;
  check('the conversation remembers the style; its suggestions are accepted', conv.style === 'reactions' && conv.suggestions.every((s) => s.state !== 'open'), `${conv.style} ${conv.suggestions.map((s) => s.state).join(',')}`);
  const s = await confirmCard(page, short.title, 'Make the short');
  const link = await s.getByRole('button', { name: 'Open' }).count();
  check('the short is a project of its own', link === 1 && (await api(page, 'project.list', {})).projects.some((p) => p.name.startsWith('Garden call:')));
  // What Claude is told after each card shows in the thread too, without the IDs it carries for Claude.
  await page.waitForTimeout(1500);
  const notes = await page.locator('[data-note]').allInnerTexts();
  const noteProblems = notes.flatMap((n) => checkPanel(n).map((f) => `${f.kind} ${f.text} in ${JSON.stringify(n.slice(0, 120))}`));
  check("the thread's outcome rows read plainly", notes.length >= 6 && noteProblems.length === 0, noteProblems.join(' | ') || `${notes.length} rows`);
  await shot(page, 'cards-done-desktop-dark');
  await ctx.close();

  // The panel with its cards on a phone and in light.
  for (const theme of ['dark', 'light']) {
    for (const mobile of [false, true]) {
      if (!mobile && theme === 'dark') continue;
      const tag = `${mobile ? 'phone' : 'desktop'}-${theme}`;
      const v = await newPage(browser, { theme, mobile, state });
      await v.page.goto(`${base}/p/${projectId}?ask=${convId}`);
      await v.page.locator('[aria-label^="Claude asks:"]').first().waitFor({ timeout: 30_000 });
      await shot(v.page, `cards-${tag}`);
      await card(v.page, studio.title).scrollIntoViewIfNeeded();
      await shot(v.page, `studio-card-${tag}`);
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
