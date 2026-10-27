#!/usr/bin/env node
// End-to-end check of Share for review (D313–D317) against a seeded throwaway daemon whose media is a private
// bucket on this computer (scripts/outside-reviewers-e2e.sh does all of it):
//   A  version 1's page: Shared for review, the form says what leaves, a link for Dana with the words and a
//      download; the link shows once; the list says it works and until when
//   B  the reviewer: another browser with no MetaRush sign-in opens the link; it reaches only the bucket's address
//      (no request to the dashboard, no policy broken); the video plays, a line of the script plays from there; a
//      comment pinned to a time is sent; phone, dark and light
//   C  Look for comments brings it in: on the version as "Dana (outside)", pinned, her words shown as hers; the bell
//   D  the receipt lists the link under people outside the workspace
//   E  Acme keeps its footage from people outside the workspace: its version's form refuses, with the rule in words
//   F  Take back (a styled question): the link no longer opens, the video neither
//   G  a second link stops opening at the bucket once its clock passes the link's signed time
//   H  Settings → Storage → Review links; phone, dark and light; no sideways scrolling
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5590 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   SHARE_BUCKET=http://127.0.0.1:5591 SHARE_PROJECT=<id> SHARE_REVISION=<id> SHARE_ACME=<id> SHARE_ACME_REVISION=<id> \
//   SHARE_SHOTS=<dir> node web/scripts/outside-reviewers-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5590').replace(/\/$/, '');
const bucket = (process.env.SHARE_BUCKET ?? 'http://127.0.0.1:5591').replace(/\/$/, '');
const project = process.env.SHARE_PROJECT ?? '';
const revision = process.env.SHARE_REVISION ?? '';
const acme = process.env.SHARE_ACME ?? '';
const acmeRevision = process.env.SHARE_ACME_REVISION ?? '';
if (!process.env.METARUSH_MINT || !project || !revision || !acme || !acmeRevision) {
  console.error('set METARUSH_MINT, METARUSH_DASHBOARD, SHARE_BUCKET, SHARE_PROJECT, SHARE_REVISION, SHARE_ACME and SHARE_ACME_REVISION (see the header)');
  process.exit(2);
}
const SHOTS = process.env.SHARE_SHOTS ?? '';
if (SHOTS) mkdirSync(SHOTS, { recursive: true });

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail && !ok ? ` - ${detail}` : detail ? ` (${String(detail).slice(0, 160)})` : ''}`);
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

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

function contextOptions(theme, phone) {
  return {
    viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 940 },
    deviceScaleFactor: phone ? 2 : 1,
    hasTouch: phone,
    isMobile: phone,
    acceptDownloads: true,
    colorScheme: theme,
  };
}

async function dashboard(browser, { theme = 'dark', phone = false } = {}) {
  const ctx = await browser.newContext(contextOptions(theme, phone));
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

/** The reviewer: a browser that never signed in to MetaRush, and what it asked for. */
async function reviewer(browser, { theme = 'light', phone = false } = {}) {
  const ctx = await browser.newContext(contextOptions(theme, phone));
  const page = await ctx.newPage();
  const seen = { requests: [], problems: [] };
  page.on('request', (r) => seen.requests.push(r.url()));
  page.on('console', (m) => {
    if (m.type() === 'error') seen.problems.push(m.text());
  });
  page.on('pageerror', (e) => seen.problems.push(String(e)));
  page.on('dialog', (d) => {
    check('no native browser dialog on the review page', false, `${d.type()}: ${d.message()}`);
    void d.dismiss();
  });
  return { ctx, page, seen };
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

async function plainIn(page, selector, what, skip = () => false) {
  const text = await readText(page, selector);
  const unplain = text
    .split('\n')
    .filter((l) => !skip(l))
    .flatMap((l) => checkShown(l).map((f) => `${f.kind} ${JSON.stringify(f.text)}`));
  check(`${what} reads plainly`, text.length > 0 && unplain.length === 0, unplain.join(' | ') || `${text.length} characters`);
  return text;
}

async function noSideways(page, what) {
  const w = await page.evaluate(() => document.documentElement.scrollWidth);
  check(`${what}: no sideways scrolling`, w <= 392, `scrollWidth ${w}`);
}

async function versionPage(page, p, r) {
  await page.goto(`${base}/p/${p}/r/${r}`);
  const part = page.locator('[data-share]');
  await part.waitFor({ timeout: 30_000 });
  await part.scrollIntoViewIfNeeded();
  return part;
}

/** Fills the form and makes the link; returns the link the form shows. */
async function makeLink(page, { forWhom, words = true, download = false, days }) {
  await page.locator('[data-share-open]').click();
  const form = page.locator('[data-share-form]');
  await form.waitFor({ timeout: 10_000 });
  await page.locator('[data-share-leaves] li').first().waitFor({ timeout: 15_000 });
  if (forWhom) await form.locator('[data-share-for]').fill(forWhom);
  if (!words) await form.locator('[data-share-words]').uncheck();
  if (download) await form.locator('[data-share-download]').check();
  if (days) await form.locator('[data-share-days]').selectOption(String(days));
  await form.locator('[data-share-message]').fill('Is the ending right?');
  const ready = await page.waitForFunction(() => !document.querySelector('[data-share-make]')?.hasAttribute('disabled'), null, { timeout: 15_000 }).then(
    () => true,
    () => false,
  );
  if (!ready) {
    const why = (await page.locator('[data-share-refused]').innerText().catch(() => '')) || (await form.innerText().catch(() => ''));
    throw new Error(`Make the link stays off: ${why.slice(0, 400)}`);
  }
  return form;
}

const browser = await chromium.launch();
let link = '';
let second = '';
try {
  const page = await dashboard(browser, { theme: 'dark' });

  // A — version 1's page: what leaves, then the link.
  const part = await versionPage(page, project, revision);
  check('A the version page has Shared for review', (await part.innerText()).includes('Shared for review'));
  const form = await makeLink(page, { forWhom: 'Dana', download: true });
  const leaves =
    (await waitFor(async () => {
      const t = await page.locator('[data-share-leaves]').innerText();
      return t.includes('download') ? t : null;
    }, 15_000)) ?? (await page.locator('[data-share-leaves]').innerText());
  check(
    'A the form says what leaves before anything is made',
    leaves.includes('review copy') && leaves.includes('what is said') && leaves.includes('download') && leaves.includes('until'),
    leaves.slice(0, 200),
  );
  await plainIn(page, '[data-share-form]', 'A the form');
  await shot(page, 'A-form-dark');
  await form.locator('[data-share-make]').click();
  const url = page.locator('[data-share-url]');
  await url.waitFor({ timeout: 60_000 });
  link = (await url.innerText()).trim();
  check('A the link is the bucket’s, signed', link.startsWith(`${bucket}/`) && link.includes('X-Amz-Signature=') && !link.includes(base), link.slice(0, 80));
  await shot(page, 'A-made-dark');
  await page.locator('[data-share-done]').click();
  const row = page.locator('[data-share-link]').first();
  await row.waitFor({ timeout: 10_000 });
  const rowText = await row.innerText();
  check('A the list says it works, for Dana, until when', rowText.includes('Dana') && rowText.includes('Works until'), rowText.slice(0, 160));
  await plainIn(page, '[data-share]', 'A the version’s links');

  // B — the reviewer.
  {
    const r = await reviewer(browser, { theme: 'light' });
    const res = await r.page.goto(link);
    check('B the link opens without signing in', res?.status() === 200, String(res?.status()));
    await r.page.locator('#title').waitFor({ timeout: 10_000 });
    const title = await r.page.locator('#title').innerText();
    check('B the page has the title', title.includes('The garden'), title);
    check('B a version nobody approved is marked as a draft for review', await r.page.locator('#draft').isVisible());
    const meta = await r.page.waitForFunction(() => document.getElementById('video')?.readyState >= 1, null, { timeout: 30_000 }).then(
      () => true,
      () => false,
    );
    check('B the video loads from the bucket', meta);
    const lines = r.page.locator('#script .line');
    const n = await lines.count();
    check('B the Script view shows what is said', n > 0 && (await r.page.locator('#script').innerText()).toLowerCase().includes('garden'), `${n} lines`);
    if (n > 1) {
      await lines.nth(1).click();
      const t = await r.page.evaluate(() => document.getElementById('video').currentTime);
      check('B pressing a line plays from there', t > 0.2, `at ${t}`);
    }
    check('B the name is filled in for Dana', (await r.page.locator('#name').inputValue()) === 'Dana');
    check('B the download is offered', await r.page.locator('#download').isVisible());
    await r.page.locator('#text').fill('The ending feels rushed: hold the last shot longer.');
    await r.page.locator('#send').click();
    const sent = await waitFor(async () => ((await r.page.locator('#status').innerText()).startsWith('Sent') ? true : null), 20_000);
    check('B the comment is sent', !!sent, await r.page.locator('#status').innerText());
    const listed = await r.page.locator('#list').innerText();
    check('B the comment is listed, pinned to a time', listed.includes('Dana (you)') && /\d:\d\d/.test(listed), listed.slice(0, 120));
    check('B a mark sits under the video', (await r.page.locator('#marks .mark').count()) === 1);
    const elsewhere = r.seen.requests.filter((u) => !u.startsWith(`${bucket}/`) && !u.startsWith('data:') && !u.startsWith('blob:'));
    check('B the page reaches only the bucket (never the dashboard)', elsewhere.length === 0, elsewhere.join(' '));
    check('B nothing on the page broke its policy', r.seen.problems.length === 0, r.seen.problems.join(' | '));
    const cookies = await r.ctx.cookies();
    check('B no cookies', cookies.length === 0, JSON.stringify(cookies));
    await plainIn(r.page, 'body', 'B the review page', (l) => l.includes('http'));
    await shot(r.page, 'B-review-light');
    // Phone, dark.
    const ph = await reviewer(browser, { theme: 'dark', phone: true });
    await ph.page.goto(link);
    await ph.page.locator('#title').waitFor({ timeout: 10_000 });
    await ph.page.waitForFunction(() => document.querySelectorAll('#list li').length > 0, null, { timeout: 15_000 });
    const phoneList = await ph.page.locator('#list').innerText();
    check('B another reviewer sees Dana’s comment', phoneList.includes('Dana') && !phoneList.includes('(you)'), phoneList.slice(0, 100));
    await noSideways(ph.page, 'B the review page on a phone');
    await shot(ph.page, 'B-review-phone-dark');
    await ph.ctx.close();
    await r.ctx.close();
  }

  // C — the comment comes back.
  await versionPage(page, project, revision);
  await page.locator('[data-share-look]').first().click();
  const back = await waitFor(async () => {
    const c = await api(page, 'comment.list', { projectId: project, revisionId: revision });
    return c.comments.find((x) => x.outside) ?? null;
  }, 30_000);
  check(
    'C the comment came back as the version’s, from outside',
    !!back && back.author.userName === 'Dana (outside)' && back.revisionId === revision && typeof back.at === 'number' && back.body.includes('hold the last shot'),
    JSON.stringify(back ?? {}).slice(0, 200),
  );
  await page.reload();
  const shown = page.locator('[data-comment-outside]').first();
  const ok = await shown.waitFor({ timeout: 20_000 }).then(
    () => true,
    () => false,
  );
  check('C the version page shows it, marked from outside', ok && (await shown.innerText()).includes('Dana (outside)'));
  const note = await waitFor(async () => {
    const bell = await api(page, 'notification.list', {});
    return (bell.notifications ?? []).find((x) => x.kind === 'outside_comments') ?? null;
  }, 15_000);
  check('C the bell says a comment came from outside', !!note && note.title.includes('from outside'), note?.title);
  await page.locator('[data-comment-outside]').first().scrollIntoViewIfNeeded();
  await shot(page, 'C-comment-dark');

  // D — the receipt.
  await page.goto(`${base}/p/${project}?tab=receipt`);
  const left = page.locator('[data-receipt-left-place="reviewers"]');
  const has = await left.waitFor({ timeout: 20_000 }).then(
    () => true,
    () => false,
  );
  const leftText = has ? await left.innerText() : '';
  check('D the receipt lists the link', has && leftText.includes('Dana') && leftText.includes('footage'), leftText.slice(0, 160));
  if (has) await left.scrollIntoViewIfNeeded();
  await shot(page, 'D-receipt-dark');

  // E — Acme keeps its footage from people outside the workspace.
  await api(page, 'content.leave.set', { projectId: acme, keep: { reviewers: ['footage'] }, note: 'The client’s contract' });
  await versionPage(page, acme, acmeRevision);
  await page.locator('[data-share-open]').click();
  const refused = page.locator('[data-share-refused]');
  await refused.waitFor({ timeout: 15_000 });
  const why = await refused.innerText();
  check('E Acme’s link is refused with the rule in words', why.includes('keep footage on this computer') && why.includes('people outside the workspace'), why.slice(0, 200));
  check('E Make the link can’t be pressed', await page.locator('[data-share-make]').isDisabled());
  await plainIn(page, '[data-share-form]', 'E the refusal');
  await shot(page, 'E-refused-dark');
  await page.keyboard.press('Escape');

  // F — take the link back.
  await versionPage(page, project, revision);
  await page.locator('[data-share-revoke]').first().click();
  await page.getByRole('button', { name: 'Take it back' }).click();
  await page.locator('[data-toasts]').getByText('Link taken back').first().waitFor({ timeout: 30_000 });
  const ended = await waitFor(async () => {
    const l = await api(page, 'share.list', { projectId: project, all: true });
    return l.links.find((x) => x.link.state === 'revoked') ?? null;
  }, 20_000);
  check('F the link is taken back', !!ended);
  {
    const r = await reviewer(browser);
    const res = await r.page.goto(link).catch(() => null);
    check('F the link no longer opens', !res || res.status() !== 200, String(res?.status()));
    await r.ctx.close();
  }

  // G — a second link, past its time at the bucket.
  {
    const f2 = await makeLink(page, { forWhom: 'Sam', words: false, days: 1 });
    await f2.locator('[data-share-make]').click();
    await page.locator('[data-share-url]').waitFor({ timeout: 60_000 });
    second = (await page.locator('[data-share-url]').innerText()).trim();
    await page.locator('[data-share-done]').click();
    const r = await reviewer(browser);
    const before = await r.page.goto(second);
    check('G the second link opens', before?.status() === 200, String(before?.status()));
    const moved = await fetch(`${bucket}/__fakebucket/ahead?by=25h`, { method: 'POST' });
    check('G the bucket’s clock moves a day ahead', moved.ok);
    const after = await r.page.goto(second);
    const body = (await after?.text()) ?? '';
    check('G past its signed time the bucket refuses it', after?.status() === 403 && body.includes('expired'), `${after?.status()} ${body.slice(0, 120)}`);
    await r.ctx.close();
  }

  // H — Settings → Storage → Review links; phone and light.
  await page.goto(`${base}/settings/storage#review-links`);
  const settings = page.locator('[data-review-link-settings]');
  await settings.waitFor({ timeout: 15_000 });
  await settings.scrollIntoViewIfNeeded();
  check('H Settings → Storage → Review links says who may send them', (await settings.innerText()).includes('Anyone in the workspace can share'));
  await plainIn(page, '[data-review-link-settings]', 'H the settings');
  await shot(page, 'H-settings-dark');
  for (const theme of ['light', 'dark']) {
    const p2 = await dashboard(browser, { theme, phone: true });
    await versionPage(p2, project, revision);
    await noSideways(p2, `H the version page on a phone (${theme})`);
    await p2.locator('[data-share]').scrollIntoViewIfNeeded();
    await shot(p2, `H-version-phone-${theme}`);
    await p2.locator('[data-share-open]').click();
    await p2.locator('[data-share-form]').waitFor({ timeout: 10_000 });
    await noSideways(p2, `H the form on a phone (${theme})`);
    await shot(p2, `H-form-phone-${theme}`);
    await p2.context().close();
  }
} catch (e) {
  check('the run finished', false, e?.stack ?? String(e));
} finally {
  await browser.close();
}

const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed${SHOTS ? `; screenshots in ${SHOTS}` : ''}`);
process.exit(failed.length ? 1 : 0);
