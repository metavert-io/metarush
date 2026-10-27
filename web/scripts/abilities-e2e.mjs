#!/usr/bin/env node
// End-to-end check of Library → Abilities (D230–D235) in the dashboard, against a
// seeded throwaway daemon that reaches nothing outside this computer
// (scripts/abilities-e2e.sh does all of it):
//   A  the Abilities tab sits in the Library after Recipes; both abilities wait for an owner
//   B  To-dos ("Ready to approve") and the bell ask about them
//   C  Title card's page: what it may do reads plainly; Approve asks in a styled question;
//      it is approved
//   D  Set up on this computer: the program it runs; on this computer, everything is ready
//   E  Run…: the words, the plan (nothing leaves this computer, it costs nothing), Run; the
//      run's panel follows it to "done" and names the picture it brought back, which says
//      how it was made
//   F  Stock photos: its key goes only to its one place and isn't on this computer; its
//      price; a made-up key typed in the setup dialog shows masked, never in full
//   G  Take back the approval of Title card (a styled question with a reason)
//   H  desktop light, phone dark and light: the list and a page, no sideways scrolling
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5420 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   ABILITIES_SHOTS=<dir> node web/scripts/abilities-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5420').replace(/\/$/, '');
if (!process.env.METARUSH_MINT) {
  console.error('set METARUSH_MINT and METARUSH_DASHBOARD (see the header of this script)');
  process.exit(2);
}
const SHOTS = process.env.ABILITIES_SHOTS ?? '';
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
  return text;
}

async function noSideways(page, what) {
  const w = await page.evaluate(() => document.documentElement.scrollWidth);
  check(`${what}: no sideways scrolling`, w <= 392, `scrollWidth ${w}`);
}

async function openAbilities(page, query = '') {
  await page.goto(`${base}/library/abilities${query}`);
  await page.locator('[data-library-section="abilities"]').waitFor({ timeout: 20_000 });
  await settle(page);
}

const browser = await chromium.launch();
try {
  const page = await newPage(browser, { theme: 'light' });

  // A — the tab and the list.
  await page.goto(`${base}/library/recipes`);
  const tab = page.locator('[data-library-tab="abilities"]');
  check('A the Abilities tab is in the Library', !!(await tab.waitFor({ timeout: 20_000 }).then(() => true).catch(() => false)));
  await tab.click();
  await page.locator('[data-library-section="abilities"] [data-ability-list]').waitFor({ timeout: 20_000 });
  const cards = await page.locator('[data-ability]').count();
  check('A both abilities are listed', cards === 2, `${cards} cards`);
  const waitingChip = await page.getByRole('button', { name: /Waiting for an owner/ }).innerText();
  check('A two versions wait for an owner', /2/.test(waitingChip), waitingChip);
  await plainIn(page, '[data-ability-list]', 'A the list');
  await shot(page, 'A-list-light');

  // B — To-dos and the bell.
  await page.goto(`${base}/todos`);
  const todo = await waitFor(async () => ((await page.getByText('Can the ability “Title card” be used?').count()) > 0 ? true : null), 20_000);
  check('B To-dos asks whether Title card can be used', !!todo);
  const bell = await api(page, 'notification.list', {}).catch(() => null);
  const rung = (bell?.notifications ?? bell?.items ?? []).filter((n) => n.kind === 'ability_waiting').length;
  check('B the bell told the owner about both', rung === 2, `${rung} notifications`);

  // C — Title card's page and its approval.
  await openAbilities(page, '?ability=title-card');
  await page.locator('[data-approval-page]').waitFor({ timeout: 20_000 });
  const mayDo = await plainIn(page, '[data-approval-page]', 'C what Title card may do');
  check('C it says nothing leaves this computer', mayDo.includes('Nothing leaves this computer.'), mayDo.slice(0, 200));
  check('C it says its licence and cost', mayDo.includes('MIT') && mayDo.includes('Free'));
  await shot(page, 'C-title-card-waiting');
  await page.locator('[data-decide]').getByRole('button', { name: 'Approve this version' }).click();
  const dlg = page.getByRole('dialog');
  await dlg.waitFor({ timeout: 10_000 });
  await shot(page, 'C-approve-question');
  await dlg.getByRole('button', { name: 'Approve', exact: true }).click();
  await dlg.waitFor({ state: 'detached', timeout: 15_000 });
  const approved = await waitFor(async () => (await api(page, 'ability.show', { ref: 'title-card' })).ability.state === 'approved', 15_000);
  check('C Title card is approved', !!approved);
  await page.getByText('approved', { exact: true }).first().waitFor({ timeout: 10_000 });

  // D — set it up on this computer.
  await page.locator('[data-setup]').click();
  await page.locator('[data-setup-form]').waitFor({ timeout: 10_000 });
  await plainIn(page, '[data-setup-form]', 'D the setup dialog');
  await shot(page, 'D-setup');
  await page.getByRole('dialog').getByRole('button', { name: 'Set it up' }).click();
  await page.getByRole('dialog').waitFor({ state: 'detached', timeout: 15_000 });
  const ready = await waitFor(async () => (await api(page, 'ability.show', { ref: 'title-card' })).here.ready, 15_000);
  check('D on this computer, everything a run needs is here', !!ready);

  // E — run it.
  await openAbilities(page, '?ability=title-card');
  await page.locator('[data-run-button]').click();
  await page.locator('[data-run-form]').waitFor({ timeout: 10_000 });
  await page.locator('[data-input="words"]').fill('Spring launch');
  await page.locator('[data-look]').click();
  await page.locator('[data-plan]').waitFor({ timeout: 20_000 });
  const plan = await plainIn(page, '[data-plan]', 'E the plan');
  check('E the plan: nothing leaves, it costs nothing', plan.includes('Nothing leaves this computer.') && plan.includes('It costs nothing.'), plan.slice(0, 200));
  await shot(page, 'E-plan');
  await page.locator('[data-start]').click();
  await page.waitForURL(/run=abr_/, { timeout: 15_000 });
  const runId = new URL(page.url()).searchParams.get('run');
  const done = await waitFor(async () => {
    const r = (await api(page, 'ability.runs', { id: runId })).run;
    return r.state === 'done' || r.state === 'failed' ? r : null;
  }, 90_000, 1000);
  check('E the run is done', done?.state === 'done', JSON.stringify(done ?? {}).slice(0, 300));
  await page.locator(`[data-run="${runId}"] [data-outputs]`).waitFor({ timeout: 20_000 });
  const runText = await plainIn(page, `[data-run="${runId}"]`, 'E the run');
  check('E it brought the picture back', runText.includes('Card: Spring launch') && runText.includes('added'), runText.slice(0, 200));
  await shot(page, 'E-run-done');
  const pic = done?.outputs?.[0]?.resourceId ? await api(page, 'resources.show', { ref: done.outputs[0].resourceId }).catch(() => null) : null;
  const made = JSON.stringify(pic ?? {});
  check('E the picture says how it was made', made.includes('"kind":"ability"') && made.includes('title-card@'), made.slice(0, 200));

  // F — Stock photos: its key, its places, its price; a key typed once.
  await openAbilities(page, '?ability=stock-photos');
  await page.locator('[data-approval-page]').waitFor({ timeout: 20_000 });
  const stock = await plainIn(page, '[data-approval-page]', 'F what Stock photos may do');
  check('F its key goes only to its one place, and isn’t here yet', stock.includes('only for api.photos.example') && stock.includes('Not on this computer yet'), stock.slice(0, 300));
  check('F its price, with the day it was read', stock.includes('per photo') && stock.includes('1 Oct 2026'));
  check('F only words leave, only for its place', stock.includes('Words you give it leave this computer, only for api.photos.example.'));
  await shot(page, 'F-stock-photos');
  await page.locator('[data-setup]').click();
  await page.locator('[data-setup-form] [data-key="example-photos"]').waitFor({ timeout: 10_000 });
  const fakeKey = `photos-${Math.random().toString(16).slice(2)}${Math.random().toString(16).slice(2)}`;
  await page.locator('[data-key="example-photos"]').fill(fakeKey);
  check('F the key is typed hidden', (await page.locator('[data-key="example-photos"]').getAttribute('type')) === 'password');
  await page.getByRole('dialog').getByRole('button', { name: 'Set it up' }).click();
  await page.getByRole('dialog').waitFor({ state: 'detached', timeout: 15_000 });
  const keyed = await waitFor(async () => {
    const s = await api(page, 'ability.show', { ref: 'stock-photos' });
    return s.page.keys[0]?.configured ? s : null;
  }, 15_000);
  check('F the key is on this computer, masked', !!keyed && keyed.page.keys[0].masked.endsWith(fakeKey.slice(-4)) && !JSON.stringify(keyed).includes(fakeKey));
  await openAbilities(page, '?ability=stock-photos');
  const shown = await page.locator('[data-approval-page]').innerText();
  check('F the page never shows the key', !shown.includes(fakeKey) && shown.includes('On this computer'));

  // G — take the approval back.
  await openAbilities(page, '?ability=title-card');
  await page.locator('[data-decide]').getByRole('button', { name: 'Take back the approval' }).click();
  const g = page.getByRole('dialog');
  await g.waitFor({ timeout: 10_000 });
  await g.locator('textarea').fill('Testing the way back');
  await g.getByRole('button', { name: 'Take back' }).click();
  await g.waitFor({ state: 'detached', timeout: 15_000 });
  const revoked = await waitFor(async () => (await api(page, 'ability.show', { ref: 'title-card' })).ability.state === 'revoked', 15_000);
  check('G the approval is taken back', !!revoked);
  await page.getByText('approval taken back').first().waitFor({ timeout: 10_000 });
  await page.context().close();

  // H — phone, dark and light.
  for (const theme of ['dark', 'light']) {
    const p = await newPage(browser, { theme, phone: true });
    await openAbilities(p);
    await p.locator('[data-ability-list]').waitFor({ timeout: 20_000 });
    await shot(p, `H-list-phone-${theme}`);
    await noSideways(p, `H ${theme}: the list`);
    await openAbilities(p, '?ability=stock-photos');
    await p.locator('[data-approval-page]').waitFor({ timeout: 20_000 });
    await shot(p, `H-page-phone-${theme}`);
    await noSideways(p, `H ${theme}: an ability's page`);
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
