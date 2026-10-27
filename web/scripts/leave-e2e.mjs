#!/usr/bin/env node
// End-to-end check of What may leave and ability files (D288–D292) in the dashboard,
// against a seeded throwaway daemon that reaches nothing outside this computer
// (scripts/leave-e2e.sh does all of it):
//   A  Acme's Settings tab has What may leave: nothing set, anything may leave
//   B  a person keeps Acme's words, pictures and faces from Claude and its voices from AI
//      services, with a note, and saves; the rules say so, with who set them
//   C  Claude Code in a terminal can't read Acme's words (E_KEPT_HERE, the rule in words)
//   D  Acme's Receipt: nothing of it left this computer; what was kept here is listed
//   E  Library → Abilities: an ability file from another workspace comes in as a draft
//      (a dry run, then a styled question); its page says where it came from and how to
//      check it here; it isn't approved
//   F  Export to a file… downloads the ability again
//   G  desktop dark, phone dark and light: the part and the receipt, no sideways scrolling
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5540 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   METARUSH_READ_AS_CLAUDE="bin/metarush --home <home> transcript find hello --project <id> --json" \
//   LEAVE_PROJECT=<id> LEAVE_FILE=<ability file> LEAVE_SHOTS=<dir> node web/scripts/leave-e2e.mjs
import { execSync, spawnSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5540').replace(/\/$/, '');
const project = process.env.LEAVE_PROJECT ?? '';
const abilityFile = process.env.LEAVE_FILE ?? '';
if (!process.env.METARUSH_MINT || !project || !abilityFile) {
  console.error('set METARUSH_MINT, METARUSH_DASHBOARD, LEAVE_PROJECT and LEAVE_FILE (see the header of this script)');
  process.exit(2);
}
const SHOTS = process.env.LEAVE_SHOTS ?? '';
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

async function shot(page, name) {
  await sleep(400);
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: false });
}

async function waitFor(fn, ms, every = 400) {
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
    acceptDownloads: true,
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

async function openPart(page) {
  await page.goto(`${base}/p/${project}?tab=settings&part=leave`);
  const part = page.locator('[data-content-part="leave"] [data-leave-part]');
  await part.waitFor({ timeout: 20_000 });
  await part.scrollIntoViewIfNeeded();
  return part;
}

async function openReceipt(page) {
  await page.goto(`${base}/p/${project}?tab=receipt`);
  const left = page.locator('[data-receipt-left]');
  await left.waitFor({ timeout: 20_000 });
  await left.scrollIntoViewIfNeeded();
  return left;
}

const browser = await chromium.launch();
try {
  const page = await newPage(browser, { theme: 'dark' });

  // A — the part, nothing set.
  const part = await openPart(page);
  const summary = await page.locator('[data-content-part="leave"]').innerText();
  check('A What may leave is in Acme’s Content Settings', summary.includes('What may leave') && summary.includes('Anything MetaRush offers may leave'), summary.slice(0, 120));
  check('A Claude never gets footage or voices (no box to tick)', (await part.locator('[data-leave-cell="claude:footage"]').count()) === 0);
  await plainIn(page, '[data-content-part="leave"]', 'A the part');
  await shot(page, 'A-part-dark');

  // B — keep words, pictures and faces from Claude, voices from AI services.
  for (const cell of ['claude:words', 'claude:pictures', 'claude:faces', 'ai:voices']) {
    await part.locator(`[data-leave-cell="${cell}"]`).uncheck();
  }
  await part.locator('[data-leave-note]').fill('The client’s contract');
  await part.locator('[data-leave-save]').click();
  const saved = await waitFor(async () => {
    const r = await api(page, 'content.leave', { projectId: project });
    return r.rules.places?.claude?.keep?.includes('words') && r.rules.places?.ai?.keep?.includes('voices') ? r : null;
  }, 15_000);
  check('B the rules are saved', !!saved, JSON.stringify(saved?.rules ?? {}));
  check('B they say who set them', !!saved?.setBy);
  await page.locator('[data-toasts]').getByText('Saved').first().waitFor({ timeout: 10_000 }).catch(() => undefined);
  const after = await waitFor(async () => {
    const t = await page.locator('[data-content-part="leave"]').innerText();
    return t.includes('to Claude') ? t : null;
  }, 10_000);
  check('B the part’s summary says it', !!after, after?.slice(0, 160));
  await shot(page, 'B-part-saved-dark');

  // C — Claude Code in a terminal can't read Acme's words.
  if (process.env.METARUSH_READ_AS_CLAUDE) {
    const [cmd, ...args] = process.env.METARUSH_READ_AS_CLAUDE.split(' ');
    const r = spawnSync(cmd, args, { cwd: repo, encoding: 'utf8', env: { ...process.env, CLAUDECODE: '1' } });
    const said = `${r.stdout}${r.stderr}`;
    check('C Claude Code in a terminal is refused Acme’s words', said.includes('E_KEPT_HERE') && said.includes('keep words on this computer'), said.slice(0, 200));
  }

  // D — the receipt.
  const left = await openReceipt(page);
  const leftText = await left.innerText();
  check('D the receipt says nothing of Acme left this computer', leftText.includes('Nothing of “Acme” left this computer.'), leftText.slice(0, 160));
  check('D it lists what was kept here', (await left.locator('[data-receipt-left-refused] li').count()) >= 1, leftText.slice(0, 300));
  await plainIn(page, '[data-receipt-left]', 'D what left');
  await shot(page, 'D-receipt-dark');

  // E — bring in an ability file from another workspace.
  await page.goto(`${base}/library/abilities`);
  await page.locator('[data-library-section="abilities"]').waitFor({ timeout: 20_000 });
  const picker = page.locator('[data-ability-import-file]');
  await picker.setInputFiles(abilityFile);
  const ask = page.getByRole('dialog');
  await ask.waitFor({ timeout: 15_000 });
  const asked = await ask.innerText();
  check('E it asks first, saying it comes in as a draft', asked.includes('never approved or set up') && asked.includes('Partner studio'), asked.slice(0, 200));
  await shot(page, 'E-import-question-dark');
  await ask.getByRole('button', { name: 'Bring it in' }).click();
  await page.locator('[data-ability-imported]').waitFor({ timeout: 20_000 });
  const shown = await api(page, 'ability.show', { ref: 'title-card' });
  check('E it came in imported, not approved', shown.ability.state === 'imported' && !shown.ability.decisions.length, shown.ability.state);
  const imported = await page.locator('[data-ability-imported]').innerText();
  check('E its page says where it came from and that it isn’t approved here', imported.includes('Partner studio') && imported.includes('Information only'), imported.slice(0, 200));
  await plainIn(page, '[data-ability-imported]', 'E the imported version');
  await shot(page, 'E-imported-dark');

  // F — export it again.
  const [download] = await Promise.all([page.waitForEvent('download', { timeout: 15_000 }), page.locator('[data-ability-export]').click()]);
  check('F Export to a file… downloads the ability', download.suggestedFilename() === 'title-card.ability.zip', download.suggestedFilename());
  await page.context().close();

  // G — phone, dark and light.
  for (const theme of ['dark', 'light']) {
    const p = await newPage(browser, { theme, phone: true });
    await openPart(p);
    await shot(p, `G-part-phone-${theme}`);
    await noSideways(p, `G ${theme}: What may leave`);
    await openReceipt(p);
    await shot(p, `G-receipt-phone-${theme}`);
    await noSideways(p, `G ${theme}: the receipt`);
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
