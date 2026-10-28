#!/usr/bin/env node
// End-to-end check of handing a computer's role to another computer (D344–D348), against the throwaway daemons
// scripts/handover-e2e.sh sets up — the old computer "Studio Mac" (METARUSH_DASHBOARD_A) and the new one
// "Edit mini" (METARUSH_DASHBOARD_B):
//   MODE=plan (the old computer, before anything changed):
//     A  Settings → Storage → Hand over this computer's role: what it is, a computer that's gone behind Details
//     B  Hand over this computer's role… then the new computer: the dry run names the conversation (its session is
//        here), the watched folder, the keys by name, the steps only a person takes (whose Claude account answers
//        changes), and the commands for a terminal only behind Details; nothing changed
//     C  phone, dark and light: the card without sideways scrolling
//   MODE=moving (the old computer, after the handover file was made):
//     D  the card follows the handover (waiting to be received) with Abandon…; the panel says the conversation is
//        moving and offers no message box
//   MODE=new (the new computer, after it received the file):
//     E  the conversation is here and goes on here; this computer is the host (no strip)
//   MODE=client (the old computer, once it stepped down):
//     F  every page says this computer is no longer the host; the card offers clean, its command behind Details
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const MODE = process.env.MODE ?? 'plan';
const onB = MODE === 'new';
const base = ((onB ? process.env.METARUSH_DASHBOARD_B : process.env.METARUSH_DASHBOARD_A) ?? '').replace(/\/$/, '');
const MINT = onB ? process.env.METARUSH_MINT_B : process.env.METARUSH_MINT_A;
const CONV = process.env.HANDOVER_CONVERSATION ?? '';
if (!base || !MINT) {
  console.error('set METARUSH_DASHBOARD_A/B and METARUSH_MINT_A/B (see scripts/handover-e2e.sh)');
  process.exit(2);
}
const SHOTS = process.env.HANDOVER_SHOTS ?? '';
if (SHOTS) mkdirSync(SHOTS, { recursive: true });

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail && !ok ? ` - ${detail}` : detail ? ` (${String(detail).slice(0, 160)})` : ''}`);
}

function mintLink(next = '') {
  const out = execSync(MINT, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  const u = new URL(m[0]);
  if (next) u.searchParams.set('next', next);
  return `${base}/auth${u.search}`;
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
      return r.json();
    },
    [op, input],
  );
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function shot(page, name) {
  await sleep(400);
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

async function newPage(browser, { theme = 'dark', phone = false, next = '' } = {}) {
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
  await page.goto(mintLink(next));
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
      // What is behind a closed Details isn't read as plain copy.
      const hidden = [...root.querySelectorAll('details:not([open]) > :not(summary)')];
      for (const h of hidden) h.setAttribute('data-e2e-hidden', h.style.display), (h.style.display = 'none');
      const t = root.innerText;
      for (const h of hidden) (h.style.display = h.getAttribute('data-e2e-hidden') ?? ''), h.removeAttribute('data-e2e-hidden');
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

async function openCard(page) {
  await page.goto(`${base}/settings/storage#handover`);
  await page.locator('[data-handover] [data-handover-status]').waitFor({ state: 'attached', timeout: 20_000 });
  await page.locator('[data-handover]').scrollIntoViewIfNeeded();
  await sleep(300);
}

const browser = await chromium.launch();
try {
  if (MODE === 'plan') {
    const page = await newPage(browser, { theme: 'light', next: '/settings/storage' });
    // A — the card.
    await openCard(page);
    const card = page.locator('[data-handover]');
    const intro = await card.innerText();
    check('A the card says what moves and that keys never travel', /conversations in Claude’s panel/.test(intro) && /Keys never travel/.test(intro) && /Nothing is deleted here/.test(intro), intro);
    const before = await api(page, 'machine.handover.status', {});
    check('A this computer is the host, nothing in progress', before.ok && before.data.role.role === 'host' && before.data.inProgress.length === 0, JSON.stringify(before).slice(0, 200));
    check('A no strip on a host', (await page.locator('[data-handover-strip]').count()) === 0);
    await plainIn(page, '[data-handover]', 'A the handover card');
    await shot(page, 'A-card');

    // B — the dry run.
    await card.locator('[data-handover-open]').click();
    const plan = card.locator('[data-handover-plan]');
    await plan.waitFor({ timeout: 20_000 });
    await plan.locator('[data-handover-to]').selectOption({ label: 'Edit mini' });
    const ready = await waitFor(async () => ((await plan.locator('[data-handover-ready="yes"]').count()) === 1 ? true : null), 20_000);
    check('B the dry run for “Edit mini” is ready', !!ready, await plan.innerText().catch(() => ''));
    const text = await plan.innerText();
    check('B it names the conversation, whose session is here', (await plan.locator('[data-handover-conversation="here"]').count()) === 1 && /Hello there/.test(text), text);
    check('B it names the watched folder and what happens to it', /The watched folder \(“inbox-studio” here\)/.test(text) && /say where it is on “Edit mini”/.test(text), text);
    check('B it names the keys by name only', /Storage and database sign-in/.test(text) && /never copied|Each computer has its own/.test(text), text);
    check('B whose Claude account answers changes, said plainly', /the Claude account signed in there answers these conversations/.test(text), text);
    check('B the background role is named', /background/i.test(text), text);
    const closed = await plan.locator('details:not([open]) [data-handover-next], [data-handover-next] details:not([open])').count();
    check('B the commands wait behind Details', closed >= 1);
    await plainIn(page, '[data-handover-plan]', 'B the dry run');
    const still = await api(page, 'machine.handover.status', {});
    check('B the dry run changed nothing', still.ok && still.data.inProgress.length === 0 && still.data.role.role === 'host');
    const remote = await api(page, 'machine.handover.export', { to: 'Edit mini' });
    check('B the handover file is never made from the browser', !remote.ok && ['E_UNAUTHORIZED', 'E_NOT_FOUND'].includes(remote.error?.code), JSON.stringify(remote).slice(0, 200));
    await shot(page, 'B-plan');

    // C — phone, dark and light.
    for (const theme of ['dark', 'light']) {
      const p = await newPage(browser, { theme, phone: true, next: '/settings/storage' });
      await openCard(p);
      await p.locator('[data-handover-open]').click();
      await p.locator('[data-handover-to]').selectOption({ label: 'Edit mini' });
      await p.locator('[data-handover-ready="yes"]').waitFor({ timeout: 20_000 });
      await noSideways(p, `C the dry run on a phone (${theme})`);
      await shot(p, `C-plan-phone-${theme}`);
      await p.context().close();
    }
  } else if (MODE === 'moving') {
    // D — after the handover file was made.
    const page = await newPage(browser, { theme: 'dark', next: '/settings/storage' });
    await openCard(page);
    const card = page.locator('[data-handover]');
    const row = await waitFor(async () => ((await card.locator('[data-handover-row="exported"]').count()) === 1 ? card.locator('[data-handover-row="exported"]').innerText() : null), 20_000);
    check('D the card follows the handover: waiting to be received', !!row && /Studio Mac/.test(row) && /Edit mini/.test(row) && /Waiting to be received/.test(row), row ?? '');
    check('D Abandon… is offered before it is received', (await card.locator('[data-handover-abandon]').count()) === 1);
    check('D no second handover is offered meanwhile', (await card.locator('[data-handover-open]').count()) === 0);
    await plainIn(page, '[data-handover]', 'D the card while handing over');
    await shot(page, 'D-moving');
    const conv = await api(page, 'assistant.show', { id: CONV, events: false });
    check('D the conversation is marked as moving', conv.ok && conv.data.handingOver?.toName === 'Edit mini' && conv.data.resumable === false, JSON.stringify(conv).slice(0, 300));
    const sent = await api(page, 'assistant.continue', { id: CONV, prompt: 'One more thing' });
    check('D the old computer refuses a message to it', !sent.ok && sent.error?.details?.reason === 'handing_over', JSON.stringify(sent).slice(0, 300));
  } else if (MODE === 'new') {
    // E — the new computer.
    const page = await newPage(browser, { theme: 'dark', next: '/settings/storage' });
    const conv = await api(page, 'assistant.show', { id: CONV, events: false });
    check('E the conversation is held here and takes messages', conv.ok && conv.data.here && conv.data.resumable && conv.data.moved?.fromName === 'Studio Mac', JSON.stringify(conv).slice(0, 300));
    const st = await api(page, 'machine.handover.status', {});
    check('E this computer is the host', st.ok && st.data.role.role === 'host', JSON.stringify(st).slice(0, 200));
    await openCard(page);
    check('E no strip on the new host', (await page.locator('[data-handover-strip]').count()) === 0);
    await plainIn(page, '[data-handover]', 'E the card on the new computer');
    await shot(page, 'E-new-host');
  } else {
    // F — the old computer, stepped down.
    const page = await newPage(browser, { theme: 'light', next: '/' });
    const strip = await waitFor(async () => ((await page.locator('[data-handover-strip="client"]').count()) === 1 ? page.locator('[data-handover-strip="client"]').innerText() : null), 30_000);
    check('F every page says this computer is no longer the host', !!strip && /no longer the host/.test(strip) && /Edit mini/.test(strip) && /client/.test(strip), strip ?? '');
    await plainIn(page, '[data-handover-strip]', 'F the strip');
    await shot(page, 'F-strip');
    await openCard(page);
    const card = page.locator('[data-handover]');
    const clean = await waitFor(async () => ((await card.locator('[data-handover-clean]').count()) === 1 ? card.locator('[data-handover-clean]').innerText() : null), 20_000);
    check('F clean is offered, never done by itself', !!clean && /never by themselves/.test(clean), clean ?? '');
    check('F its command waits behind Details', (await card.locator('[data-handover-clean] details:not([open])').count()) === 1);
    await plainIn(page, '[data-handover]', 'F the card on a client');
    await shot(page, 'F-client-card');
    for (const theme of ['dark']) {
      const p = await newPage(browser, { theme, phone: true, next: '/settings/storage' });
      await openCard(p);
      await noSideways(p, `F the card on a phone (${theme})`);
      await p.context().close();
    }
  }
} finally {
  await browser.close();
}
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed (${MODE})`);
process.exit(failed.length ? 1 : 0);
