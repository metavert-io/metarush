#!/usr/bin/env node
// End-to-end check of the menu of abilities (D268–D272) in Library → Abilities, against a seeded
// throwaway daemon that reaches nothing outside this computer (scripts/catalogue-e2e.sh does all
// of it):
//   A  the menu: a line saying what works here, the six built-in groups, each card saying "Works
//      here" or what it needs first — the AI services need a key, as this computer has none —
//      and the added ability beside them, waiting for an owner
//   B  the filters: Works here leaves out what needs a key; Built in and Added each show theirs
//   C  a built-in ability's page: what it needs, what it costs, who starts it; its name and the
//      operation it runs behind Details; an AI service's says what leaves this computer
//   D  the added ability approved (a styled question) and set up on this computer: the menu then
//      says it works here
//   E  phone dark and light: the menu and a page, no sideways scrolling
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5500 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   CATALOGUE_SHOTS=<dir> node web/scripts/catalogue-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5500').replace(/\/$/, '');
if (!process.env.METARUSH_MINT) {
  console.error('set METARUSH_MINT and METARUSH_DASHBOARD (see the header of this script)');
  process.exit(2);
}
const SHOTS = process.env.CATALOGUE_SHOTS ?? '';
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
  const [w, widest] = await page.evaluate(() => {
    let far = null;
    for (const el of document.querySelectorAll('body *')) {
      const r = el.getBoundingClientRect();
      if (r.right > window.innerWidth + 1 && (!far || r.right > far.right)) far = { right: Math.round(r.right), tag: el.tagName, text: (el.textContent ?? '').trim().slice(0, 60) };
    }
    return [document.documentElement.scrollWidth, far];
  });
  check(`${what}: no sideways scrolling`, w <= 392, `scrollWidth ${w}${widest ? `; widest ${widest.tag} at ${widest.right}: ${widest.text}` : ''}`);
}

async function openMenu(page, query = '') {
  await page.goto(`${base}/library/abilities${query}`);
  await page.locator('[data-library-section="abilities"]').waitFor({ timeout: 20_000 });
  await settle(page);
}

const browser = await chromium.launch();
try {
  const page = await newPage(browser, { theme: 'light' });

  // A — the menu.
  await openMenu(page);
  await page.locator('[data-menu-summary]').waitFor({ timeout: 20_000 });
  const summary = await page.locator('[data-menu-summary]').innerText();
  check('A a line says what works here', /built in and 1 added/.test(summary) && /work on/.test(summary), summary);
  const groups = await page.locator('[data-menu-group]').evaluateAll((els) => els.map((e) => e.getAttribute('data-menu-group')));
  check('A the six built-in groups, then what was added', ['understand', 'fix', 'make', 'bring', 'edit', 'deliver', 'added'].every((g) => groups.includes(g)) && groups[groups.length - 1] === 'added', groups.join(', '));
  const narration = page.locator('[data-builtin="ai-narration"]');
  const narrationText = await narration.innerText();
  check('A AI narration needs a key this computer doesn’t have', (await narration.getAttribute('data-works')) === 'no' && /Needs an ElevenLabs key/.test(narrationText) && /Priced first/.test(narrationText), narrationText);
  const works = await page.locator('[data-builtin][data-works="yes"]').count();
  check('A most of what is built in works here with no keys', works >= 30, `${works} work here`);
  const card = page.locator('[data-ability="title-card"]');
  const cardText = await card.innerText();
  check('A the added ability sits beside them, waiting for an owner', /Needs an owner.s approval/.test(cardText) && (await card.getAttribute('data-works')) === 'no', cardText);
  await plainIn(page, '[data-abilities-page]', 'A the menu');
  await shot(page, 'A-menu-light');

  // B — the filters.
  await page.getByRole('button', { name: 'Works here', exact: true }).click();
  await settle(page);
  check('B Works here leaves out what needs a key', (await page.locator('[data-builtin="ai-narration"]').count()) === 0 && (await page.locator('[data-builtin][data-works="no"]').count()) === 0);
  check('B … and the added ability that waits', (await page.locator('[data-ability="title-card"]').count()) === 0);
  await page.getByRole('button', { name: 'Added', exact: true }).click();
  await settle(page);
  check('B Added shows only what this workspace added', (await page.locator('[data-builtin]').count()) === 0 && (await page.locator('[data-ability="title-card"]').count()) === 1);
  await page.getByRole('button', { name: 'Built in', exact: true }).click();
  await settle(page);
  check('B Built in shows only MetaRush’s own', (await page.locator('[data-ability]').count()) === 0 && (await page.locator('[data-builtin]').count()) >= 45);

  // C — a built-in ability's page.
  await openMenu(page);
  await page.locator('[data-builtin="steady-footage"]').click();
  await page.locator('[data-builtin-page="steady-footage"]').waitFor({ timeout: 20_000 });
  await page.getByRole('heading', { name: 'Steady shaky footage' }).waitFor({ timeout: 20_000 });
  const steady = await plainIn(page, '[data-builtin-page]', 'C Steady shaky footage');
  check('C it says what it needs and costs, and who starts it', /What it needs/.test(steady) && /It costs nothing\./.test(steady) && /Who starts it/.test(steady), steady.slice(0, 300));
  await page.getByText('Details', { exact: true }).click();
  const details = await page.locator('[data-builtin-page]').innerText();
  check('C its name and the operation it runs are behind Details', details.includes('metarush/steady-footage') && details.includes('asset.fixes.set'), details.slice(-300));
  await shot(page, 'C-built-in');
  await openMenu(page, '?builtin=ai-pictures');
  await page.locator('[data-builtin-page="ai-pictures"]').getByText(/Needs .* key/).first().waitFor({ timeout: 20_000 });
  const pics = await page.locator('[data-builtin-page]').innerText();
  check('C an AI service says which key it needs and that it is priced first', /key/.test(pics) && /priced first/i.test(pics), pics.slice(0, 300));
  check('C … and what leaves this computer', /What leaves/.test(pics) && /leave this computer/.test(pics), pics.slice(0, 400));
  check('C … and never that it costs nothing', !/costs nothing/.test(pics));

  // D — approve and set up the added ability; then it works here.
  await openMenu(page, '?ability=title-card');
  await page.locator('[data-approval-page]').waitFor({ timeout: 20_000 });
  await page.locator('[data-decide]').getByRole('button', { name: 'Approve this version' }).click();
  const dlg = page.getByRole('dialog');
  await dlg.waitFor({ timeout: 10_000 });
  await dlg.getByRole('button', { name: 'Approve', exact: true }).click();
  await dlg.waitFor({ state: 'detached', timeout: 15_000 });
  const approved = await waitFor(async () => (await api(page, 'ability.show', { ref: 'title-card' })).ability.state === 'approved', 15_000);
  check('D an owner approved it in the styled question', !!approved);
  await openMenu(page);
  const needsSetup = await page.locator('[data-ability="title-card"]').innerText();
  check('D approved, it needs setting up on this computer', /Needs setting up on this computer/.test(needsSetup), needsSetup);
  await openMenu(page, '?ability=title-card');
  await page.locator('[data-setup]').click();
  await page.locator('[data-setup-form]').waitFor({ timeout: 10_000 });
  await page.getByRole('dialog').getByRole('button', { name: 'Set it up' }).click();
  await page.getByRole('dialog').waitFor({ state: 'detached', timeout: 15_000 });
  const ready = await waitFor(async () => {
    const m = await api(page, 'catalogue.list', { kind: 'added' });
    return m.items.find((i) => i.name === 'title-card')?.worksHere ? m : null;
  }, 15_000);
  check('D set up, it works here', !!ready);
  await openMenu(page);
  await page.getByRole('button', { name: 'Works here', exact: true }).click();
  await settle(page);
  const nowWorks = page.locator('[data-ability="title-card"]');
  check('D the menu lists it under Works here', (await nowWorks.count()) === 1 && /Works here/.test(await nowWorks.innerText()));
  await shot(page, 'D-works-here');

  // E — phone, dark and light.
  for (const theme of ['dark', 'light']) {
    const p = await newPage(browser, { theme, phone: true });
    await openMenu(p);
    await p.locator('[data-menu-summary]').waitFor({ timeout: 20_000 });
    await noSideways(p, `E the menu on a phone (${theme})`);
    await shot(p, `E-menu-phone-${theme}`);
    await openMenu(p, '?builtin=whats-on-screen');
    await p.locator('[data-builtin-page="whats-on-screen"]').getByText('What it needs').waitFor({ timeout: 20_000 });
    await noSideways(p, `E a built-in ability on a phone (${theme})`);
    await shot(p, `E-built-in-phone-${theme}`);
    await p.context().close();
  }
} finally {
  await browser.close();
}

const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
process.exit(failed.length ? 1 : 0);
