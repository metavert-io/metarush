#!/usr/bin/env node
// End-to-end check of community catalogues of abilities (D480–D487) in the dashboard, against a
// throwaway daemon that reaches nothing outside this computer (scripts/ability-catalogue-e2e.sh does
// all of it):
//   A  Settings → Abilities: no catalogue yet; an owner adds one by its address — a look first (what
//      it lists, who keeps it), a styled question, then it is listed
//   B  Library → Abilities → Browse catalogues: what it lists, each with its publisher, licence, what
//      it does and needs, and the note that MetaRush didn't check it; finding by words
//   C  Bring it in… on Title card: MetaRush fetches the repository's commit, a styled question says
//      what comes in; it lands as a draft on its page, from the catalogue
//   D  a terminal checks it here (its test in the sandbox, the network off); the owner approves it
//   E  the listing whose archive lies is refused in plain words; nothing comes in
//   F  browse says Title card is here, approved; then the catalogue takes it back: after Read again
//      the owners' question is on To-dos, its page says so, and taking back its approval works
//   G  phone, dark and light: browse and Settings → Abilities, no sideways scrolling
//   H  the owner removes the catalogue (a styled question); browse says there is none
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5810 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   METARUSH_CHECK="bin/metarush --home <home> ability check --version" CATALOGUE_URL=https://127.0.0.1:5811/index.json \
//   CATALOGUE_INDEX=<the served index.json> CATALOGUE_SHOTS=<dir> node web/scripts/ability-catalogue-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5810').replace(/\/$/, '');
const catalogueURL = process.env.CATALOGUE_URL ?? '';
const catalogueIndex = process.env.CATALOGUE_INDEX ?? '';
if (!process.env.METARUSH_MINT || !process.env.METARUSH_CHECK || !catalogueURL || !catalogueIndex) {
  console.error('set METARUSH_MINT, METARUSH_CHECK, CATALOGUE_URL, CATALOGUE_INDEX and METARUSH_DASHBOARD (see the header of this script)');
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
      // What a Details disclosure holds is for people who asked for it.
      const hidden = [];
      for (const d of root.querySelectorAll('details:not([open])')) {
        for (const c of d.children) {
          if (c.tagName !== 'SUMMARY') {
            hidden.push([c, c.style.display]);
            c.style.display = 'none';
          }
        }
      }
      const t = root.innerText;
      for (const n of added) n.remove();
      for (const [c, v] of hidden) c.style.display = v;
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

async function toast(page) {
  const t = page.locator('[data-toasts] [role="status"], [data-toasts] [role="alert"]').last();
  await t.waitFor({ timeout: 60_000 });
  return t.innerText();
}

async function openBrowse(page, query = '') {
  await page.goto(`${base}/library/abilities?browse=1${query}`);
  await page.locator('[data-browse-catalogues]').waitFor({ timeout: 20_000 });
  await page.locator('[data-browse-summary], [data-browse-empty], [data-browse-catalogues] [role="alert"]').first().waitFor({ timeout: 30_000 });
  await settle(page);
}

const browser = await chromium.launch();
try {
  const page = await newPage(browser, { theme: 'light' });

  // A — Settings → Abilities: none yet; an owner adds the catalogue.
  await page.goto(`${base}/settings/abilities`);
  await page.locator('[data-catalogues-settings]').waitFor({ timeout: 20_000 });
  await page.locator('[data-no-catalogues]').waitFor({ timeout: 20_000 });
  check('A no catalogue is trusted by default', true);
  await plainIn(page, '[data-catalogues-settings]', 'A Settings → Abilities');
  await shot(page, 'A-settings-empty');
  await page.locator('[data-catalogue-url]').fill(catalogueURL);
  await page.locator('[data-add-catalogue]').getByRole('button', { name: 'Add…' }).click();
  const add = page.getByRole('dialog');
  await add.waitFor({ timeout: 30_000 });
  const look = await plainIn(page, '[role="dialog"]', 'A the look before trusting it');
  check('A the look says what it lists and who keeps it', look.includes('2 abilities') && look.includes('acme') && look.includes('Listed, not checked by MetaRush'), look.slice(0, 300));
  await shot(page, 'A-add-question');
  await add.getByRole('button', { name: 'Trust it' }).click();
  await add.waitFor({ state: 'detached', timeout: 15_000 });
  await page.locator('[data-catalogue-item="acme"]').waitFor({ timeout: 20_000 });
  const listed = await plainIn(page, '[data-catalogue-list]', 'A the trusted catalogue');
  check('A it is listed, read just now', listed.includes('Acme abilities') && listed.includes('2 abilities listed'), listed.slice(0, 200));
  await shot(page, 'A-settings-trusted');

  // B — Browse catalogues.
  await page.goto(`${base}/library/abilities`);
  await page.locator('[data-browse-catalogues-link]').click();
  await page.locator('[data-listings]').waitFor({ timeout: 30_000 });
  const cards = await page.locator('[data-listing]').count();
  check('B both listings show', cards === 2, `${cards} listings`);
  const browse = await plainIn(page, '[data-browse-catalogues]', 'B browsing');
  check('B the note: listed, not checked by MetaRush', browse.includes('Listed, not checked by MetaRush'));
  const card = await readText(page, '[data-listing="title-card"]');
  check('B a listing says its publisher, licence, what it reaches and needs', card.includes('github.com/acme') && card.includes('MIT') && card.includes('No network') && card.includes('python3'),
    card.slice(0, 300));
  await shot(page, 'B-browse');
  await page.locator('[data-browse-search]').fill('copy');
  await page.locator('[data-browse-search]').press('Enter');
  const found = await waitFor(async () => ((await page.locator('[data-listing]').count()) === 1 ? true : null), 15_000);
  check('B finding by words keeps one', !!found && (await page.locator('[data-listing="copy-card"]').count()) === 1);

  // C — Bring it in… on Title card.
  await openBrowse(page);
  await page.locator('[data-bring-in="title-card"]').click();
  const q = page.getByRole('dialog');
  await q.waitFor({ timeout: 120_000 });
  const preview = await plainIn(page, '[role="dialog"]', 'C the question before it comes in');
  check('C it says what comes in and that it is checked first', preview.includes('a new ability') && preview.includes('never approved or set up here') && preview.includes('Nothing leaves this computer'),
    preview.slice(0, 300));
  await shot(page, 'C-bring-in-question');
  await q.getByRole('button', { name: 'Bring it in' }).click();
  await q.waitFor({ state: 'detached', timeout: 15_000 });
  await page.waitForURL(/ability=title-card/, { timeout: 120_000 });
  await page.locator('[data-ability-imported]').waitFor({ timeout: 20_000 });
  const imported = await plainIn(page, '[data-ability-imported]', 'C its page: where it came from');
  check('C it came from the catalogue, as a draft', imported.includes('Brought in from a catalogue') && imported.includes('Acme abilities'), imported.slice(0, 300));
  const v = await api(page, 'ability.show', { ref: 'title-card' });
  check('C imported, never approved or set up', v.ability.state === 'imported' && !v.here.setUp && v.ability.imported?.catalogue?.key === 'acme', v.ability.state);
  await shot(page, 'C-imported-page');

  // D — checked at a terminal here, then approved by the owner in the browser.
  const out = execSync(`${process.env.METARUSH_CHECK} title-card@${v.ability.hash} --json`, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const checked = JSON.parse(out);
  check('D a terminal checks it here: it passes and waits for an owner', checked.ok && checked.data.ok && checked.data.state === 'waiting', out.slice(0, 300));
  await page.goto(`${base}/library/abilities?ability=title-card`);
  await page.locator('[data-decide]').getByRole('button', { name: 'Approve this version' }).waitFor({ timeout: 20_000 });
  await page.locator('[data-decide]').getByRole('button', { name: 'Approve this version' }).click();
  const ap = page.getByRole('dialog');
  await ap.waitFor({ timeout: 10_000 });
  await ap.getByRole('button', { name: 'Approve', exact: true }).click();
  await ap.waitFor({ state: 'detached', timeout: 15_000 });
  const approved = await waitFor(async () => (await api(page, 'ability.show', { ref: 'title-card' })).ability.state === 'approved', 15_000);
  check('D the owner approved it', !!approved);
  await shot(page, 'D-approved');

  // E — the lying listing is refused.
  await openBrowse(page);
  await page.locator('[data-bring-in="copy-card"]').click();
  const refused = await toast(page);
  check('E a listing whose archive lies is refused in plain words', /aren.t the version/.test(refused) && checkShown(refused).length === 0, refused.slice(0, 300));
  const none = await api(page, 'ability.list', {});
  check('E nothing came in', !none.abilities.some((r) => r.name === 'copy-card'), JSON.stringify(none.abilities.map((r) => r.name)));
  await shot(page, 'E-refused');

  // F — browse says it is here.
  await openBrowse(page);
  const hereText = await readText(page, '[data-listing="title-card"]');
  check('F Title card is here, approved', hereText.includes('Here: version 1, approved'), hereText.slice(0, 200));

  // F — the catalogue takes that version back: Read again, and the owners are asked.
  const ix = JSON.parse(readFileSync(catalogueIndex, 'utf8'));
  ix.version += 1;
  ix.revoked = [{ archive: v.ability.hash, name: 'title-card', why: 'It sent words elsewhere.' }];
  writeFileSync(catalogueIndex, JSON.stringify(ix, null, 2));
  await page.goto(`${base}/settings/abilities`);
  await page.locator('[data-catalogue-item="acme"] [data-catalogue-read]').click();
  const asked = await waitFor(async () => {
    const l = await api(page, 'todos.list', { kind: 'ability_taken_back' });
    return l.todos?.length ? l.todos[0] : null;
  }, 20_000);
  check('F the owners are asked about the version taken back', !!asked && asked.humanOnly, JSON.stringify(asked ?? {}).slice(0, 200));
  await page.goto(`${base}/todos?kind=takenBack`);
  const question = page.locator('[data-todo-kind="ability_taken_back"]').first();
  await question.waitFor({ timeout: 20_000 });
  const qText = await plainIn(page, '[data-todo-kind="ability_taken_back"]', 'F the question');
  check('F it says which version and why, in the catalogue’s words', qText.includes('took back version 1') && qText.includes('It sent words elsewhere'), qText.slice(0, 300));
  await shot(page, 'F-taken-back-question');
  await question.getByRole('button', { name: /Take its approval back/ }).click();
  const revokedNow = await waitFor(async () => (await api(page, 'ability.show', { ref: 'title-card@' + v.ability.hash })).ability.state === 'revoked', 15_000);
  check('F an owner takes back its approval from To-dos', !!revokedNow);
  await page.goto(`${base}/library/abilities?ability=title-card`);
  await page.locator('[data-taken-back]').waitFor({ timeout: 20_000 });
  const banner = await readText(page, '[data-taken-back]');
  check('F its page says the catalogue took it back', banner.includes('It sent words elsewhere.'), banner.slice(0, 200));
  await shot(page, 'F-taken-back-page');
  await page.context().close();

  // G — phone, dark and light.
  for (const theme of ['dark', 'light']) {
    const p = await newPage(browser, { theme, phone: true });
    await openBrowse(p);
    await p.locator('[data-listings]').waitFor({ timeout: 20_000 });
    await shot(p, `G-browse-phone-${theme}`);
    await noSideways(p, `G ${theme}: browsing`);
    await p.goto(`${base}/settings/abilities`);
    await p.locator('[data-catalogue-item="acme"]').waitFor({ timeout: 20_000 });
    await shot(p, `G-settings-phone-${theme}`);
    await noSideways(p, `G ${theme}: Settings → Abilities`);
    await p.context().close();
  }

  // H — the owner removes the catalogue.
  const last = await newPage(browser, { theme: 'dark' });
  await last.goto(`${base}/settings/abilities`);
  await last.locator('[data-catalogue-item="acme"] [data-catalogue-remove]').click();
  const rm = last.getByRole('alertdialog');
  await rm.waitFor({ timeout: 10_000 });
  await rm.getByRole('button', { name: 'Stop trusting it' }).click();
  await rm.waitFor({ state: 'detached', timeout: 15_000 });
  await last.locator('[data-no-catalogues]').waitFor({ timeout: 20_000 });
  check('H the catalogue is removed', true);
  const kept = await api(last, 'ability.show', { ref: 'title-card' });
  check('H what came in from it stays (its approval taken back, as the owner chose)', kept.ability.state === 'revoked', kept.ability.state);
  await openBrowse(last);
  const empty = await readText(last, '[data-browse-catalogues]');
  check('H browse says there is no catalogue', empty.includes('No catalogue yet'), empty.slice(0, 200));
  await shot(last, 'H-removed');
  await last.context().close();
} catch (e) {
  check('the run finished', false, String(e?.stack ?? e));
} finally {
  await browser.close();
}
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed${SHOTS ? `; screenshots in ${SHOTS}` : ''}`);
process.exit(failed.length ? 1 : 0);
