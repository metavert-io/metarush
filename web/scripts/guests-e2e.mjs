#!/usr/bin/env node
// Guests in a real browser (docs/guests.md). Run it through scripts/guests-e2e.sh: a throwaway installation with
// "Spring promo", its version 1 and that version's review copy kept on this computer; the owner is signed in with a
// one-time link. It checks that:
//   1. Settings → People → Guests says guests are off and nothing is reachable from the internet; the owner turns
//      guests on and the guest listener answers at its own address on this computer;
//   2. on version 1's page, Share with a guest… (beside Share for review…) says what the guest sees, shares the
//      version with Dana as a commenter, and makes a one-time sign-in link to the guest address;
//   3. a second browser, with no MetaRush sign-in, opens the link: the page signs in only when its button is
//      pressed; it lists what was shared, plays the review copy from the guest listener (ranges), and keeps a comment
//      pinned to a time; it reaches nothing but the guest address, and its only cookie is the guest's own;
//   4. the owner sees the comment on the version, marked as a guest's, and what the guest did; takes everything
//      back (a styled confirm);
//   5. the guest is out at once: the next request is refused and the page asks them to sign in again;
//   6. the pages read plainly; no native dialogs, page errors or horizontal overflow; screenshots of the guest page
//      (desktop and phone, dark and light), the share dialog and Settings → People → Guests.
// It CHANGES the installation (guests on, a share): point it only at a throwaway one.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const need = ['METARUSH_DASHBOARD', 'METARUSH_GUESTS', 'METARUSH_MINT', 'GUESTS_PROJECT', 'GUESTS_REVISION', 'GUESTS_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/guests-e2e.sh does)`);
    process.exit(2);
  }
}
const base = process.env.METARUSH_DASHBOARD.replace(/\/$/, '');
const guestBase = process.env.METARUSH_GUESTS.replace(/\/$/, '');
const project = process.env.GUESTS_PROJECT;
const revision = process.env.GUESTS_REVISION;
const shotsDir = process.env.GUESTS_SHOTS;
mkdirSync(shotsDir, { recursive: true });

// A made-up guest (an example domain only).
const GUEST = 'dana@client.example';

const results = [];
const problems = [];
let probing = false;
function check(name, ok, detail = '') {
  results.push({ name, ok: !!ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
}

function mintLink() {
  const out = execSync(process.env.METARUSH_MINT, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return `${base}/auth${new URL(m[0]).search}`;
}

async function newPage(browser, { theme = 'dark', mobile = false } = {}) {
  const ctx = await browser.newContext({
    viewport: mobile ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: mobile ? 2 : 1,
    colorScheme: theme,
    reducedMotion: 'reduce',
  });
  await ctx.addInitScript((t) => {
    try {
      localStorage.setItem('metarush.theme', t);
    } catch {
      /* private mode */
    }
  }, theme);
  const page = await ctx.newPage();
  const seen = watch(page);
  return { ctx, page, seen };
}

function watch(page) {
  const seen = { requests: [], media: [] };
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('console', (m) => {
    // The run's own probe of the dashboard from the guest page is refused by the page's policy, as it should be.
    if (probing && /Content Security Policy|api\/session/.test(m.text())) return;
    if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) problems.push(`console: ${m.text()}`);
  });
  page.on('dialog', (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  page.on('request', (r) => seen.requests.push(r.url()));
  page.on('response', (r) => {
    if (r.url().includes('/guest/media/')) seen.media.push({ status: r.status(), type: r.headers()['content-type'] ?? '' });
    if (r.status() >= 500) problems.push(`${r.status()} from ${r.url().replace(/[?#].*/, '')}`);
  });
  return seen;
}

const shots = [];
let shotN = 0;
async function shot(page, name) {
  await page.waitForTimeout(400);
  const file = path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file, fullPage: false });
  shots.push(file);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
}

async function readText(page, selector) {
  return page.evaluate(
    ([sel, open, close]) => {
      const root = document.querySelector(sel);
      if (!root) return '';
      const clone = root.cloneNode(true);
      clone.querySelectorAll('[data-user-content]').forEach((el) => {
        el.textContent = `${open}${el.textContent}${close}`;
      });
      clone.querySelectorAll('details:not([open]) > :not(summary)').forEach((el) => el.remove());
      document.body.appendChild(clone);
      clone.style.position = 'absolute';
      clone.style.left = '-99999px';
      const t = clone.innerText;
      clone.remove();
      return t;
    },
    [selector, THEIRS_OPEN, THEIRS_CLOSE],
  );
}

async function plain(page, selector, what) {
  const text = await readText(page, selector);
  // The guest address is shown on purpose (it is the link people open).
  const bad = checkShown(text)
    .filter((f) => !(f.text ?? '').includes('127.0.0.1'))
    .map((f) => `${f.kind}: ${f.text}`);
  check(`${what} reads plainly`, text.length > 0 && bad.length === 0, bad.join(' | '));
}

async function waitFor(fn, ms = 15_000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    const v = await fn().catch(() => undefined);
    if (v) return v;
    await new Promise((r) => setTimeout(r, 250));
  }
  return undefined;
}

async function main() {
  const browser = await chromium.launch();
  const owner = await newPage(browser);
  const { page } = owner;
  await page.goto(mintLink());

  // 1. Settings → People → Guests: off, then on.
  await page.goto(`${base}/settings/people#guests`);
  const part = page.locator('[data-guests-settings]');
  await part.waitFor({ timeout: 20_000 });
  const off = await waitFor(async () => /Guests are off/.test(await part.innerText()), 20_000);
  check('Settings → People → Guests says guests are off', !!off);
  check('…and that nothing is reachable from the internet yet', /Nothing is reachable from the internet yet/.test(await part.innerText()));
  await part.locator('[role="radiogroup"][aria-label="Let guests in"] [data-value="on"]').click();
  const on = await waitFor(async () => /This computer answers them at/.test(await part.innerText()) && (await part.locator('text=Answering').count()) > 0, 20_000);
  check('turning guests on starts the guest listener', !!on, (await part.innerText()).slice(0, 300));
  const health = await waitFor(async () => {
    const r = await fetch(`${guestBase}/guest/api/health`).catch(() => null);
    return r && r.ok;
  }, 10_000);
  check('the guest address answers', !!health);
  await plain(page, '[data-guests-settings]', 'Settings → People → Guests');
  await shot(page, 'settings-guests-dark');

  // 2. Share version 1 with a guest, from its page.
  await page.goto(`${base}/p/${project}/r/${revision}`);
  const open = page.locator('[data-guest-share-open]');
  await open.waitFor({ timeout: 20_000 });
  check('Share with a guest… sits beside Share for review…', (await page.locator('[data-share-open]').count()) === 1);
  await open.click();
  const form = page.locator('[data-guest-form]');
  await form.waitFor({ timeout: 10_000 });
  await form.locator('[data-guest-email]').fill(GUEST);
  await form.locator('[data-guest-name]').fill('Dana');
  await form.locator('[data-guest-role="commenter"]').check();
  const planned = await waitFor(async () => (await form.locator('[data-guest-leaves] li').count()) > 0, 15_000);
  check('the form says what the guest sees before anything is shared', !!planned, (await form.locator('[data-guest-leaves]').innerText()).slice(0, 200));
  await plain(page, '[data-guest-form]', 'the share dialog');
  await shot(page, 'share-dialog');
  await form.locator('[data-guest-share]').click();
  const sharedPart = page.locator('[data-guest-shared]');
  await sharedPart.waitFor({ timeout: 15_000 });
  check('it is shared', /Shared version 1 of “Spring promo” with Dana/.test(await sharedPart.innerText()), (await sharedPart.innerText()).slice(0, 200));
  await sharedPart.locator('[data-guest-make-link]').click();
  const linkPart = page.locator('[data-guest-link-made]');
  await linkPart.waitFor({ timeout: 15_000 });
  const link = (await linkPart.locator('[data-guest-link-url]').innerText()).trim();
  check('the sign-in link opens the guest address', link.startsWith(`${guestBase}/welcome#`) && link.length > guestBase.length + 40, link.replace(/#.*/, '#…'));
  check('…and says it opens on this computer only for now', /opens on this computer only/.test(await linkPart.innerText()));
  await linkPart.locator('[data-guest-link-done]').click();
  const row = page.locator('[data-guest-row="dana@client.example"]');
  await row.waitFor({ timeout: 15_000 });
  check('the version lists Dana as a guest who can see it', /Watches and comments/.test(await row.innerText()));

  // 3. The guest, in another browser with no MetaRush sign-in.
  const guest = await newPage(browser, { theme: 'light' });
  const g = guest.page;
  await g.goto(link);
  const cont = g.locator('[data-guest-link-button]');
  await cont.waitFor({ timeout: 15_000 });
  const before = await guest.ctx.cookies();
  check('opening the link signs nothing in until its button is pressed', before.length === 0, JSON.stringify(before));
  check('the link’s secret leaves the address bar as the page opens', !g.url().includes('#'), g.url().replace(/#.*/, '#…'));
  await shot(g, 'guest-welcome-light');
  await cont.click();
  const shares = g.locator('[data-guest-shares]');
  await shares.waitFor({ state: 'visible', timeout: 15_000 });
  check('the guest sees what was shared with them', /Spring promo/.test(await shares.innerText()) && (await g.locator('[data-guest-version]').count()) === 1);
  check('…and who they are signed in as', (await g.locator('[data-guest-me]').innerText()).includes(GUEST));
  check('the link is gone from the address bar', !g.url().includes('#'), g.url());
  await plain(g, 'main', 'the guest page');
  await shot(g, 'guest-shared-light');
  await g.locator('[data-guest-open]').first().click();
  await g.locator('[data-guest-watch]').waitFor({ state: 'visible', timeout: 15_000 });
  const loaded = await g.waitForFunction(() => document.querySelector('[data-guest-video]')?.readyState >= 1, null, { timeout: 30_000 }).then(
    () => true,
    () => false,
  );
  check('the review copy loads from the guest listener', loaded && guest.seen.media.some((m) => (m.status === 200 || m.status === 206) && m.type.startsWith('video/mp4')), JSON.stringify(guest.seen.media));
  await g.evaluate(() => {
    const v = document.querySelector('[data-guest-video]');
    v.currentTime = 2;
  });
  await g.waitForTimeout(500);
  await g.locator('[data-guest-comment-text]').fill('The ending feels rushed: hold the last shot longer.');
  await g.locator('[data-guest-comment-send]').click();
  const kept = await waitFor(async () => (await g.locator('[data-guest-comment]').count()) === 1, 15_000);
  const listed = kept ? await g.locator('[data-guest-comments]').innerText() : '';
  check('the comment is kept, pinned to a time', !!kept && /0:02/.test(listed) && listed.includes('hold the last shot longer'), listed.slice(0, 160));
  await shot(g, 'guest-watch-light');
  const elsewhere = guest.seen.requests.filter((u) => !u.startsWith(`${guestBase}/`) && !u.startsWith('data:') && !u.startsWith('blob:'));
  check('the guest page reaches only the guest address (never the dashboard)', elsewhere.length === 0, elsewhere.join(' '));
  const cookies = await guest.ctx.cookies();
  check(
    'its only cookie is the guest one: HttpOnly, SameSite=Strict, this computer only',
    cookies.length === 1 && cookies[0].name === 'mr_guest' && cookies[0].httpOnly && cookies[0].sameSite === 'Strict' && cookies[0].domain === '127.0.0.1',
    JSON.stringify(cookies.map((c) => ({ name: c.name, httpOnly: c.httpOnly, sameSite: c.sameSite, domain: c.domain }))),
  );
  probing = true;
  const dash = await g.evaluate(async (u) => {
    try {
      const r = await fetch(`${u}/api/session`, { credentials: 'include' });
      return r.status;
    } catch {
      return 0;
    }
  }, base);
  await g.waitForTimeout(300);
  probing = false;
  check('the guest page can’t reach the dashboard (its policy refuses it)', dash !== 200, String(dash));
  // On a phone, dark.
  const phone = await newPage(browser, { theme: 'dark', mobile: true });
  await phone.ctx.addCookies(cookies.map((c) => ({ ...c })));
  await phone.page.goto(`${guestBase}/`);
  await phone.page.locator('[data-guest-shares]').waitFor({ state: 'visible', timeout: 15_000 });
  await shot(phone.page, 'guest-shared-phone-dark');
  await phone.ctx.close();

  // 4. The owner: the comment on the version, what the guest did; takes it all back.
  await page.goto(`${base}/p/${project}/r/${revision}`);
  const seenComment = await waitFor(async () => (await page.locator('body').innerText()).includes('hold the last shot longer'), 20_000);
  check('the owner sees the guest’s comment on the version', !!seenComment);
  const bodyText = await page.locator('body').innerText();
  check('…marked as a guest’s', /Dana \(guest\)/.test(bodyText));
  check('…its words shown as theirs', (await page.locator('[data-user-content]', { hasText: 'hold the last shot longer' }).count()) > 0);
  await page.goto(`${base}/settings/people#guests`);
  const act = page.locator('[data-guests-activity]');
  await act.waitFor({ timeout: 20_000 });
  const did = await waitFor(async () => /commented on version 1 of “Spring promo”/.test(await act.innerText()), 20_000);
  check('what the guest did is listed for the owner, in plain words', !!did, (await act.innerText()).slice(0, 300));
  await plain(page, '[data-guests-settings]', 'Settings → People → Guests with a guest');
  await shot(page, 'settings-guests-activity');
  await page.locator('[data-guest="dana@client.example"] [data-guest-revoke-all]').click();
  const confirmBox = page.locator('[role="alertdialog"]');
  await confirmBox.waitFor({ timeout: 10_000 });
  await confirmBox.locator('button[type="submit"]').click();
  const gone = await waitFor(async () => (await page.locator('[data-guest="dana@client.example"]').count()) === 0, 15_000);
  check('taking everything back clears Dana from who has what', !!gone);

  // 5. The guest is out at once.
  const status = await g.evaluate(async () => (await fetch('/guest/api/shared', { credentials: 'same-origin' })).status);
  check('the guest’s next request is refused', status === 401, String(status));
  await g.reload();
  const signin = g.locator('[data-guest-signin]');
  const out = await signin.waitFor({ state: 'visible', timeout: 15_000 }).then(
    () => true,
    () => false,
  );
  check('the guest page asks them to sign in again', out);
  await plain(g, 'main', 'the signed-out guest page');
  await shot(g, 'guest-signed-out-light');
  await guest.ctx.close();
  await owner.ctx.close();
  await browser.close();

  check('no native dialogs, page errors or horizontal overflow', problems.length === 0, problems.join(' | '));
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed; ${shots.length} screenshots in ${shotsDir}`);
  if (failed.length) process.exitCode = 1;
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
