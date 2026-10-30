#!/usr/bin/env node
// Moving the workspace's database from the dashboard (D334–D338) in a real
// browser. Run it through scripts/database-move-e2e.sh: two throwaway
// installations of one workspace on one database, each with its own
// dashboard. It checks that:
//   1. Settings → Storage → "Move the database" checks the new database first
//      (what moves, how long, what each computer needs, what waits; nothing
//      changes), then asks in a styled dialog;
//   2. while it moves every page says changes wait, and a change is refused
//      plainly; the dashboard starts again by itself (holding, then switched)
//      and the card says how it went;
//   3. on the other computer the page says the workspace moved, a change is
//      refused ("switch this computer too"), and switching with its own
//      address (typed once, never shown again) brings it to the new database;
//   4. a project written straight into the old database after the move (a
//      computer that slept through it) is found by the studio's next look,
//      shown on its card in plain words with the bell, compared, and carried
//      over after a styled dialog (D352–D358);
//   5. no native dialogs, page errors or horizontal overflow; the cards in
//      plain words. Screenshots: desktop dark and light, phone, in
//      DATABASE_SHOTS.
// It CHANGES both installations, so point it only at throwaway ones.
import { execFileSync, execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const need = ['METARUSH_DASHBOARD_A', 'METARUSH_MINT_A', 'METARUSH_DASHBOARD_B', 'METARUSH_MINT_B', 'DATABASE_ADDRESS', 'DATABASE_NEW', 'DATABASE_OLD', 'DATABASE_LATE_WRITER', 'DATABASE_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/database-move-e2e.sh does)`);
    process.exit(2);
  }
}
const address = process.env.DATABASE_ADDRESS;
const newName = process.env.DATABASE_NEW;
const shotsDir = process.env.DATABASE_SHOTS;
mkdirSync(shotsDir, { recursive: true });

const results = [];
const problems = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
}

function mintLink(which) {
  const base = process.env[`METARUSH_DASHBOARD_${which}`].replace(/\/$/, '');
  const out = execSync(process.env[`METARUSH_MINT_${which}`], { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return `${base}/auth${new URL(m[0]).search}`;
}

/** Calls an operation the way the dashboard does; returns {ok, data, error}. */
async function op(page, name, input = {}) {
  return page.evaluate(
    async ([name, input]) => {
      const r = await fetch(`/api/ops/${name}`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' },
        body: JSON.stringify(input),
      });
      return r.json();
    },
    [name, input],
  );
}

// While a dashboard starts again (holding for the move, then with the new
// database) its requests fail for a moment: expected, so not problems.
const restarting = /ERR_CONNECTION_REFUSED|ERR_EMPTY_RESPONSE|ERR_CONNECTION_RESET|Failed to load resource|status of 50[234]|EventSource|net::ERR/;

async function newPage(browser, { theme = 'dark', mobile = false, state, label } = {}) {
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
  page.on('pageerror', (e) => problems.push(`${label}: pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/status of 4\d\d/.test(m.text()) && !restarting.test(m.text())) problems.push(`${label}: console: ${m.text()}`);
  });
  page.on('dialog', (d) => {
    problems.push(`${label}: native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  return { ctx, page };
}

let shotN = 0;
async function shot(page, name) {
  await page.waitForTimeout(400);
  const file = path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file, fullPage: false });
  const overflow = await page.evaluate(() => {
    const w = document.documentElement.clientWidth;
    const by = [...document.querySelectorAll('body *')]
      .filter((el) => el.getBoundingClientRect().right > w + 1 && getComputedStyle(el).position !== 'fixed')
      .map((el) => `${el.tagName.toLowerCase()}.${String(el.className).slice(0, 60)} "${(el.textContent ?? '').trim().slice(0, 40)}"`)
      .slice(-3);
    return { px: document.documentElement.scrollWidth - w, by };
  });
  if (overflow.px > 0) problems.push(`horizontal overflow of ${overflow.px}px on ${name} (${overflow.by.join(' | ')})`);
}

/** A part of the page's words as people read them, people's own words marked as theirs. */
async function shownText(page, selector) {
  return page.locator(selector).first().evaluate((sec) => {
    const added = [];
    for (const el of sec.querySelectorAll('[data-user-content]')) {
      if (el.parentElement?.closest('[data-user-content]') || !el.textContent?.trim()) continue;
      const a = document.createTextNode('“');
      const b = document.createTextNode('”');
      el.prepend(a);
      el.append(b);
      added.push(a, b);
    }
    const text = sec.innerText;
    for (const n of added) n.remove();
    return text;
  });
}

async function plain(page, label, selector = 'section#database') {
  const text = await shownText(page, selector);
  const found = checkShown(text);
  check(`${label}: in plain words`, found.length === 0, found.map((f) => `${f.kind} ${f.text}`).join('; '));
  return text;
}

/** Opens a page of a dashboard that may be starting again (holding, or switched). */
async function open(page, url) {
  const end = Date.now() + 90_000;
  for (;;) {
    try {
      await page.goto(url);
      return;
    } catch (e) {
      if (Date.now() > end || !restarting.test(String(e))) throw e;
      await page.waitForTimeout(1000);
    }
  }
}

/** Waits until the dashboard answers database.move.status with what `done` wants (it restarts on the way). */
async function waitStatus(page, which, done, seconds = 180) {
  const end = Date.now() + seconds * 1000;
  let last = '';
  while (Date.now() < end) {
    try {
      const r = await op(page, 'database.move.status');
      if (r.ok) {
        last = `${r.data.summary} / ${r.data.run?.state ?? '-'} / ${r.data.this.name}`;
        if (done(r.data)) return r.data;
      } else {
        last = `${r.error?.code} ${r.error?.message}`;
      }
    } catch (e) {
      last = String(e); // restarting
    }
    await page.waitForTimeout(1000);
  }
  throw new Error(`${which}: still waiting: ${last}`);
}

async function main() {
  const browser = await chromium.launch();

  /* ---------- Studio Mac plans and moves the database ---------- */
  const { ctx: ctxA, page: a } = await newPage(browser, { label: 'studio' });
  await a.goto(mintLink('A'));
  await a.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const stateA = await ctxA.storageState();
  await a.goto(`${process.env.METARUSH_DASHBOARD_A}/settings/storage#database`);
  const sec = a.locator('section#database');
  await sec.getByRole('button', { name: 'Move the database to another place' }).waitFor({ timeout: 30_000 });
  await sec.scrollIntoViewIfNeeded();
  await plain(a, 'the card');
  await shot(a, 'database-before-desktop-dark');

  await sec.getByRole('button', { name: 'Move the database to another place' }).click();
  await sec.getByRole('radio', { name: /Another MongoDB/ }).click();
  await sec.getByLabel('Address', { exact: true }).fill(address);
  await sec.getByLabel('Database name').fill(newName);
  await sec.getByRole('button', { name: 'Check the move' }).click();
  const planBox = sec.locator('[data-database-plan]');
  await planBox.waitFor({ timeout: 60_000 });
  const planText = await planBox.innerText();
  check('the check says what moving would copy', /Moving would copy the workspace's database, [\d,]+ records/.test(planText), planText.split('\n')[0]);
  check('the check is ready', (await planBox.getAttribute('data-database-plan')) === 'ready');
  check('the check lists the other computer and what it needs', /Edit laptop.*own sign-in/.test(planText.replace(/\n/g, ' ')));
  check('the check says what waits meanwhile', planText.includes('changes wait on every computer'));
  check('the check says the old database is kept', planText.includes('nothing is ever deleted'));
  check('the typed address is gone from the form', (await sec.locator('[data-database-address]').count()) === 0 || (await sec.locator('[data-database-address]').inputValue()) === '');
  const html = await a.content();
  check('the address is nowhere in the page', !html.includes(address));
  const still = await op(a, 'database.move.status');
  check('checking changed nothing', still.ok && still.data.state === '' && still.data.this.name !== newName, still.data?.summary);
  await plain(a, 'the plan');
  await shot(a, 'database-plan-desktop-dark');

  await sec.getByRole('button', { name: 'Move the database', exact: true }).click();
  const dlg = a.locator('[data-mr-dialog="open"]');
  await dlg.waitFor();
  const dlgText = await dlg.innerText();
  check('a styled dialog asks first', dlgText.includes('Move the database to') && dlgText.includes('Changes pause on every computer'), dlgText.split('\n')[0]);
  await shot(a, 'database-confirm-desktop-dark');
  await dlg.locator('button[type="submit"]').click();

  /* ---------- while it moves ---------- */
  const { ctx: ctxB, page: b } = await newPage(browser, { label: 'laptop' });
  await open(b, mintLink('B'));
  await b.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const stateB = await ctxB.storageState();
  // The laptop refuses changes from the moment the move starts (being moved,
  // then moved), and says so on every page.
  const frozen = await waitStatus(b, 'laptop', (s) => s.frozen, 60);
  check('the laptop knows the workspace is being moved', frozen.frozen, frozen.summary);
  const refused = await op(b, 'project.create', { name: 'Made during the move' });
  check(
    'a change on the laptop is refused plainly',
    !refused.ok && refused.error.code === 'E_MOVING' && /being moved to another database|moved to another database/.test(refused.error.message),
    refused.error?.message,
  );
  await open(b, `${process.env.METARUSH_DASHBOARD_B}/`);
  await b.locator('[data-database-strip]').waitFor({ timeout: 30_000 });
  check('every page says so on the laptop', true);
  await plain(b, 'the strip', '[data-database-strip]');

  const doneA = await waitStatus(a, 'studio', (s) => s.run?.state === 'done' && s.this.name === newName && !s.frozen, 240);
  check('the move finished and this computer uses the new database', doneA.this.name === newName, doneA.run.summary);
  check('the summary says the old database is kept', /old database is kept, marked as moved, and nothing was deleted/.test(doneA.run.summary));
  await open(a, `${process.env.METARUSH_DASHBOARD_A}/settings/storage#database`);
  await sec.locator('[data-database-state="done"]').waitFor({ timeout: 60_000 });
  await sec.scrollIntoViewIfNeeded();
  await plain(a, 'after the move');
  await shot(a, 'database-done-desktop-dark');
  const made = await op(a, 'project.create', { name: 'Summer after the move' });
  check('a change on the studio works in the new database', made.ok, made.error?.message);

  /* ---------- something written into the old database late ---------- */
  const late = sec.locator('[data-database-late]');
  await late.waitFor({ timeout: 30_000 });
  check('the card says MetaRush looks at the old database again', /looks at the old database|Nothing was written/.test(await late.innerText()), (await late.innerText()).split('\n')[1]);
  // A computer that slept through the move: one project lands in the old database now.
  execFileSync(process.env.DATABASE_LATE_WRITER, [address, process.env.DATABASE_OLD, 'Written after the move'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  await late.getByRole('button', { name: 'Look again now' }).click();
  const item = sec.locator('[data-database-late-item="waiting"]');
  await item.waitFor({ timeout: 60_000 });
  const itemText = await item.innerText();
  check('the late write is on the card in plain words', itemText.includes('A project was added to the old database after the move'), itemText.split('\n').slice(0, 3).join(' '));
  check('the card says it waits for a person', (await late.getAttribute('data-database-late')) === 'waiting');
  check('the card says how long its copy is kept', /keeps its copies for 30 days/.test(await late.innerText()) && /Its copy is kept until/.test(itemText));
  await plain(a, 'the late write');
  await late.scrollIntoViewIfNeeded();
  await shot(a, 'database-late-write-desktop-dark');
  const bell = await op(a, 'notification.list', {});
  check('owners hear of it once, on the bell', bell.ok && bell.data.notifications.filter((n) => n.kind === 'late_writes').length === 1);
  await item.getByRole('button', { name: 'Compare' }).click();
  await item.locator('[data-database-late-both]').waitFor({ timeout: 20_000 });
  check('both versions side by side', (await item.locator('[data-database-late-both]').innerText()).includes('Written after the move'));
  await item.getByRole('button', { name: 'Carry it over' }).click();
  const lateDlg = a.locator('[data-mr-dialog="open"]');
  await lateDlg.waitFor();
  check('a styled dialog asks before carrying it over', (await lateDlg.innerText()).includes('Carry it over?'));
  await lateDlg.locator('button[type="submit"]').click();
  await sec.locator('[data-database-late-item="carried"]').waitFor({ timeout: 30_000 });
  const projects = await op(a, 'project.list', {});
  check('the project written late is in the new database now', projects.ok && projects.data.projects.some((p) => p.name === 'Written after the move'));
  await shot(a, 'database-late-carried-desktop-dark');
  {
    const { ctx, page } = await newPage(browser, { theme: 'light', state: stateA, label: 'studio light' });
    await open(page, `${process.env.METARUSH_DASHBOARD_A}/settings/storage#database`);
    await page.locator('section#database [data-database-state="done"]').waitFor({ timeout: 30_000 });
    await page.locator('section#database').scrollIntoViewIfNeeded();
    await shot(page, 'database-done-desktop-light');
    await ctx.close();
  }

  /* ---------- the laptop switches too ---------- */
  const moved = await waitStatus(b, 'laptop', (s) => s.state === 'moved' && s.switchMeToo, 60);
  check('the laptop is told to switch', moved.switchMeToo && moved.canSwitch && moved.steps.length > 0, moved.summary);
  const stillRefused = await op(b, 'project.create', { name: 'Lost' });
  check('a change on the laptop is still refused', !stillRefused.ok && stillRefused.error.code === 'E_MOVING' && stillRefused.error.message.includes('switch this computer too'), stillRefused.error?.message);
  await open(b, `${process.env.METARUSH_DASHBOARD_B}/settings/storage#database`);
  const secB = b.locator('section#database');
  const prompt = secB.locator('[data-database-switch-me-too]');
  await prompt.waitFor({ timeout: 30_000 });
  check('the card says the workspace moved', (await prompt.innerText()).includes('switch this computer too'));
  await b.locator('[data-database-strip="moved"]').waitFor({ timeout: 10_000 });
  await plain(b, 'switch me too');
  await secB.scrollIntoViewIfNeeded();
  await shot(b, 'database-switch-me-too-desktop-dark');
  {
    const { ctx, page } = await newPage(browser, { mobile: true, state: stateB, label: 'laptop phone' });
    await open(page, `${process.env.METARUSH_DASHBOARD_B}/settings/storage#database`);
    await page.locator('section#database [data-database-switch-me-too]').waitFor({ timeout: 30_000 });
    await page.locator('section#database [data-database-switch-me-too]').scrollIntoViewIfNeeded();
    await shot(page, 'database-switch-me-too-phone-dark');
    await ctx.close();
  }
  const form = secB.locator('[data-database-switch-form]');
  check('the switch is filled in with the new database’s name', (await form.getByLabel('Database name').inputValue()) === newName);
  await form.getByLabel('This computer’s address for the new database').fill(address);
  await form.getByRole('button', { name: 'Switch this computer' }).click();
  const switched = await waitStatus(b, 'laptop', (s) => s.this.name === newName && !s.frozen, 120);
  check('the laptop switched to the new database', switched.this.name === newName, switched.summary);
  await open(b, `${process.env.METARUSH_DASHBOARD_B}/settings/storage#database`);
  await b.locator('section#database').waitFor({ timeout: 30_000 });
  await b.waitForTimeout(1500);
  check('the strip is gone on the laptop', (await b.locator('[data-database-strip]').count()) === 0);
  const madeB = await op(b, 'project.create', { name: 'Autumn on the laptop' });
  check('a change on the laptop works after switching', madeB.ok, madeB.error?.message);
  await plain(b, 'after switching');
  await b.locator('section#database').scrollIntoViewIfNeeded();
  await shot(b, 'database-switched-desktop-dark');

  await browser.close();
  for (const p of problems) check(p, false);
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
  process.exit(failed.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
