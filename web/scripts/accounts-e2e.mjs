#!/usr/bin/env node
// Accounts and sign-in in a real browser (D183, docs/accounts.md). Run it
// through scripts/accounts-e2e.sh: a throwaway installation where the owner is
// signed in with a one-time link and nobody else can get in yet. It checks that:
//   1. Settings → People (Users) shows "Just me on this computer", and inviting is off
//      until a sign-in method is chosen;
//   2. the owner's wizard (a styled dialog) chooses Email and password with
//      their email and a password, showing the length rule and the match check
//      as they type; inviting turns on;
//   3. inviting maya@example.com shows the link once (with Copy and expiry);
//   4. in a new browser the invite page says "Join MetaRush on …", takes a name
//      and a password, and lands signed in (her name in the top bar); the link
//      doesn't work a second time;
//   5. she signs out (back at the sign-in page) and signs in again there; a
//      wrong password says so plainly;
//   6. the owner makes a reset link for her; a fresh browser follows it,
//      chooses a new password and lands signed in; her earlier browser is sent
//      to the sign-in page; the old password no longer works, the new one does;
//      changing it in Settings → People → Your account keeps that browser signed
//      in (her name stays in the top bar, after a reload too) and the password
//      before no longer works;
//   7. the activity log names people and offers a filter by person;
//   8. the sign-in, welcome and Users screens read plainly; no native dialogs,
//      page errors or horizontal overflow; 16px gutters on a phone;
//   9. the top bar's one menu (D184): a menu button with the Settings sections
//      and light/dark (no sign-out) while it is just me; the person's picture
//      (initials without one) with their name, Your account, the sections,
//      light/dark and Sign out once people sign in; older Settings addresses
//      land in their section;
//  10. pictures (D184): a picture chosen in Settings → People → Your account is
//      cropped in a styled dialog and shows in the menu button and the people
//      list; removing it brings the initials back; a file that isn't a picture
//      is refused in plain words;
//  11. owner-only settings (D191): Settings → Browser extension is a section of
//      its own (older addresses land there); a member signed in at this
//      computer's own address sees Claude Code, Footage, Storage, Integrations,
//      Log and Remote access with every control disabled and one line naming
//      the owner, and the server refuses her change with the owner named;
//  12. browser extension pairings are a person's (D197): the member pairs her
//      own browser (her code, "what it sends is yours"), sees only hers in Your
//      browsers and removes it; the owner sees everyone's, with whose each is,
//      and can remove any; the server refuses her removing the owner's.
// Screenshots of /signin, /welcome, Settings → People, Settings → Claude Code,
// Library → People, the top bar (menu button and picture) and the open menu at
// desktop and phone width, dark and light, in ACCOUNTS_SHOTS. It CHANGES the
// installation's sign-in: point it only at a throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'ACCOUNTS_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/accounts-e2e.sh does)`);
    process.exit(2);
  }
}
const shotsDir = process.env.ACCOUNTS_SHOTS;
mkdirSync(shotsDir, { recursive: true });

// Made-up people and passwords for a throwaway installation (none holds a
// person's name or email, which the server refuses).
const OWNER_EMAIL = 'owner@example.com';
const OWNER_PW = 'violet harbor lantern 42';
const J_EMAIL = 'maya@example.com';
const J_NAME = 'Maya';
const J_PW = 'maple orbit sunrise 17';
const J_PW2 = 'copper fjord meadow 93';
const J_PW3 = 'amber canyon willow 58';

const results = [];
const problems = [];
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

/** A link MetaRush made (it names this computer's address), on the address the browser uses. */
function onBase(link) {
  const u = new URL(link);
  return `${base}${u.pathname}${u.search}`;
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

const shots = [];
let shotN = 0;
async function shot(page, name) {
  await page.waitForTimeout(450);
  const file = path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file });
  shots.push(file);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
}

/** The top bar alone (it must fit the width: nothing scrolls sideways). */
async function shotTop(page, name) {
  await page.waitForTimeout(450);
  const box = await page.locator('header').first().boundingBox();
  const file = path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file, clip: { x: 0, y: 0, width: page.viewportSize().width, height: Math.ceil((box?.height ?? 57) + 1) } });
  shots.push(file);
  const overflow = await page.evaluate(() => {
    const h = document.querySelector('header > div');
    return Math.max(document.documentElement.scrollWidth - document.documentElement.clientWidth, h ? h.scrollWidth - h.clientWidth : 0);
  });
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px in the top bar on ${name}`);
}

/** A small PNG drawn in the page (a gradient and a circle), as bytes. */
async function makePicture(page, w = 400, h = 300) {
  const b64 = await page.evaluate(
    ([w, h]) => {
      const c = document.createElement('canvas');
      c.width = w;
      c.height = h;
      const x = c.getContext('2d');
      const g = x.createLinearGradient(0, 0, w, h);
      g.addColorStop(0, '#2b6cb0');
      g.addColorStop(1, '#e0a458');
      x.fillStyle = g;
      x.fillRect(0, 0, w, h);
      x.fillStyle = '#fff';
      x.beginPath();
      x.arc(w / 2, h / 2, Math.min(w, h) / 4, 0, Math.PI * 2);
      x.fill();
      const u = c.toDataURL('image/png');
      return u.slice(u.indexOf(',') + 1);
    },
    [w, h],
  );
  return Buffer.from(b64, 'base64');
}

/** The open menu's settings sections, in order. */
const SECTION_IDS = ['general', 'people', 'preferences', 'claude', 'footage', 'extension', 'storage', 'components', 'integrations', 'log', 'cli']; // Brand is a project's Content Settings now (D193); Preferences after People (D208); Components after Storage (D324)

/** The text a person reads in an element, people's own words (<Theirs>) in curly quotes. */
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

function unplain(text) {
  return checkShown(text).map((f) => `${f.kind}: ${f.text}`);
}

async function waitFor(fn, ms = 15_000, step = 250) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) return v;
    await new Promise((r) => setTimeout(r, step));
  }
}

/** The name in the top bar (the account button), once the app has loaded. */
async function headerName(page) {
  const el = page.locator('[data-account-name]');
  try {
    await el.waitFor({ state: 'attached', timeout: 20_000 });
  } catch {
    return '';
  }
  return ((await el.textContent()) ?? '').trim();
}

async function pathOf(page) {
  return new URL(page.url()).pathname;
}

/** Sign in on /signin; returns the plain error line, or '' when it signed in. */
async function signInWith(page, email, password) {
  await page.goto(`${base}/signin`);
  const form = page.locator('[data-signin-form]');
  await form.waitFor({ timeout: 20_000 });
  await form.locator('input[name="email"]').fill(email);
  await form.locator('input[name="password"]').fill(password);
  await form.locator('button[type="submit"]').click();
  const outcome = await waitFor(async () => {
    if ((await pathOf(page)) !== '/signin') return 'in';
    if (await page.locator('[data-signin-error]').count()) return 'error';
    return '';
  }, 20_000);
  if (outcome === 'error') return ((await page.locator('[data-signin-error]').textContent()) ?? '').trim();
  return outcome === 'in' ? '' : 'nothing happened';
}

/** Follow an invite or reset link in this page and choose a password (and a name, for an invite). */
async function acceptWelcome(page, link, { name, password }) {
  await page.goto(onBase(link));
  const form = page.locator('[data-welcome-form]');
  await form.waitFor({ timeout: 20_000 });
  if (name !== undefined) await form.locator('[data-welcome-name]').fill(name);
  await form.locator('[data-new-password]').fill(password);
  await form.locator('[data-password-again]').fill(password);
  await form.locator('button[type="submit"]').click();
  return waitFor(async () => !(await pathOf(page)).startsWith('/welcome/') || (await page.locator('[data-welcome-error]').count()) > 0, 20_000);
}

async function openUsers(page) {
  // A full load (going to the same address with only a different #part wouldn't reload).
  await page.goto('about:blank');
  await page.goto(`${base}/settings/people#users`);
  await page.locator('[data-users-section] [data-method-card]').waitFor({ timeout: 30_000 });
  await page.locator('[data-users-section]').scrollIntoViewIfNeeded();
}

/** What the extension does: pair with a code from its own origin (Node's fetch can set Origin; a page can't). */
async function extPair(code, origin) {
  const r = await fetch(`${base}/ext/pair`, {
    method: 'POST',
    headers: { Origin: origin, 'X-MetaRush-Client': 'extension', 'Content-Type': 'application/json' },
    body: JSON.stringify({ code, name: 'Chrome', version: '0.3.0' }),
  });
  const j = await r.json().catch(() => ({}));
  return { ok: r.ok && j.ok, status: r.status, data: j.data, error: j.error };
}

async function extHello(token, origin) {
  const r = await fetch(`${base}/ext/hello`, {
    method: 'POST',
    headers: { Origin: origin, 'X-MetaRush-Client': 'extension', 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: '{}',
  });
  return { status: r.status };
}

async function main() {
  const browser = await chromium.launch();
  const VARIANTS = [
    ['dark', false, 'desktop-dark'],
    ['light', false, 'desktop-light'],
    ['dark', true, 'phone-dark'],
    ['light', true, 'phone-light'],
  ];

  // 1. The owner, signed in with a one-time link, opens Settings → People (Users).
  const owner = await newPage(browser);
  await owner.page.goto(mintLink());
  await owner.page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  await openUsers(owner.page);
  const page = owner.page;
  const users = page.locator('[data-users-section]');
  check('Settings → People (Users) opens for the owner', await users.isVisible());
  check('the method starts as just me on this computer', (await users.locator('[data-method-card]').getAttribute('data-method')) === 'single');
  check('inviting is off until a sign-in method is chosen', await users.locator('[data-invite-button]').isDisabled(), (await users.innerText()).includes('Choose how people sign in first') ? 'says why' : 'no reason shown');
  check('no account menu in the top bar while it is just me', (await page.locator('[data-account-button]').count()) === 0);

  // 9. The one menu while it is just me: a menu button, the sections, light/dark, no sign-out.
  {
    const menuBtn = page.locator('[data-account-menu]');
    await page.locator('[data-account-menu][data-menu-kind="menu"]').waitFor({ timeout: 15_000 });
    check('just me: the menu is a menu button (no picture)', (await menuBtn.getAttribute('data-menu-kind')) === 'menu' && (await menuBtn.locator('img, [data-initials]').count()) === 0);
    check('no Settings gear or separate light/dark button in the top bar', (await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Settings' }).count()) === 0 && (await page.getByRole('button', { name: /Switch to (light|dark) mode/ }).count()) === 0);
    const primary = await page.getByRole('navigation', { name: 'Primary' }).getByRole('link').evaluateAll((els) => els.map((e) => e.getAttribute('aria-label')));
    check('the top bar has Studio, Library, Renders and To-dos', JSON.stringify(primary) === JSON.stringify(['Studio', 'Library', 'Renders', 'To-dos']), JSON.stringify(primary));
    await menuBtn.click();
    const menu = page.getByRole('menu');
    await menu.waitFor({ timeout: 10_000 });
    const ids = await menu.locator('[role="menuitem"]').evaluateAll((els) => els.map((e) => [...e.attributes].find((a) => a.name.startsWith('data-menu-settings-'))?.name.slice('data-menu-settings-'.length)).filter(Boolean));
    check('the menu lists the Settings sections', JSON.stringify(ids) === JSON.stringify(SECTION_IDS), JSON.stringify(ids));
    check('…with light/dark and no Sign out', (await menu.locator('[data-menu-theme]').count()) === 1 && (await menu.locator('[data-signout]').count()) === 0 && (await menu.locator('[data-menu-your-account]').count()) === 0);
    await shot(page, 'menu-just-me-desktop-dark');
    await menu.locator('[data-menu-theme]').click();
    check('light/dark in the menu switches the theme', (await page.evaluate(() => document.documentElement.getAttribute('data-theme'))) === 'light');
    await menu.locator('[data-menu-theme]').click();
    check('…and back', (await page.evaluate(() => document.documentElement.getAttribute('data-theme'))) === 'dark');
    await menu.locator('[data-menu-settings-claude]').click();
    await page.waitForURL((u) => u.pathname === '/settings/claude', { timeout: 10_000 }).catch(() => undefined);
    await page.locator('#claude-code').waitFor({ timeout: 15_000 }).catch(() => undefined);
    check('a section in the menu opens its page', (await pathOf(page)) === '/settings/claude' && (await page.locator('#claude-code').count()) === 1, page.url());
    // Older addresses land in their section, replacing the old one in the history.
    await page.goto(`${base}/settings#remote-access`);
    await page.waitForURL((u) => u.pathname === '/settings/people', { timeout: 10_000 }).catch(() => undefined);
    check('an older link (/settings#remote-access) opens Settings → People there', (await pathOf(page)) === '/settings/people' && new URL(page.url()).hash === '#remote-access', page.url());
    await page.goto(`${base}/people`);
    await page.waitForURL((u) => u.pathname === '/library/people', { timeout: 10_000 }).catch(() => undefined);
    check('/people opens Library → People', (await pathOf(page)) === '/library/people', page.url());
    // The browser extension has its own section (D191); its older addresses land there.
    for (const from of ['/settings#extension', '/settings#browser-extension', '/settings/footage#browser-extension', '/settings/footage#extension']) {
      await page.goto('about:blank');
      await page.goto(`${base}${from}`);
      await page.waitForURL((u) => u.pathname === '/settings/extension', { timeout: 10_000 }).catch(() => undefined);
      check(`${from} opens Settings → Browser extension`, (await pathOf(page)) === '/settings/extension', page.url());
    }
    // Settings → Preferences (D208): its addresses, and light or dark moved there from General.
    for (const [from, hash] of [['/settings#preferences', ''], ['/settings/general#appearance', '#appearance'], ['/preferences', ''], ['/settings#appearance', '#appearance']]) {
      await page.goto('about:blank');
      await page.goto(`${base}${from}`);
      await page.waitForURL((u) => u.pathname === '/settings/preferences', { timeout: 10_000 }).catch(() => undefined);
      check(`${from} opens Settings → Preferences${hash}`, (await pathOf(page)) === '/settings/preferences' && new URL(page.url()).hash === hash, page.url());
    }
    {
      const prefs = page.locator('[data-preferences]');
      await prefs.locator('#estimates input[type=checkbox]').first().waitFor({ timeout: 20_000 }).catch(() => undefined);
      check(
        'Preferences: estimates, Claude’s changes and light or dark; General no longer has light or dark',
        (await prefs.locator('#estimates').count()) === 1 && (await prefs.locator('#assistant-mode').count()) === 1 && (await prefs.locator('#appearance').count()) === 1,
      );
      check('estimates come first by default, and the owner (just me) may ask everyone', (await prefs.getByLabel('Estimate costs of outside AI services before edits').isChecked()) && (await prefs.getByLabel('Ask everyone for estimates').isEnabled()) && (await prefs.locator('[data-owners-line]').count()) === 0);
      await prefs.locator('#appearance').getByRole('radio', { name: 'light' }).click();
      check('light or dark is switched here', (await page.evaluate(() => document.documentElement.getAttribute('data-theme'))) === 'light');
      await prefs.locator('#appearance').getByRole('radio', { name: 'dark' }).click();
      const prefsPlain = unplain(await readText(page, '[data-preferences]'));
      check('Settings → Preferences reads plainly', prefsPlain.length === 0, prefsPlain.join(' | '));
      await shot(page, 'preferences-just-me-desktop-dark');
      await page.goto(`${base}/settings/general`);
      await page.locator('[data-settings="general"]').waitFor({ timeout: 10_000 }).catch(() => undefined);
      check('General has no light or dark of its own now', (await page.locator('#appearance').count()) === 0);
    }
    await page.goto(`${base}/settings/extension`);
    await page.locator('#browser-extension').waitFor({ timeout: 20_000 }).catch(() => undefined);
    const pairBtn = page.locator('#browser-extension').getByRole('button', { name: 'Pair a browser' });
    check('the owner (just me) can pair a browser, with no owners line', (await pairBtn.count()) === 1 && (await pairBtn.isEnabled()) && (await page.locator('[data-owners-line]').count()) === 0);
    check('Settings → Browser extension is in the side menu after Footage', JSON.stringify(await page.locator('[data-settings-link]').evaluateAll((els) => els.map((e) => e.getAttribute('data-settings-link')))) === JSON.stringify(SECTION_IDS));
    const ownerSingle = await owner.ctx.storageState();
    for (const [theme, mobile, tag] of VARIANTS) {
      const v = await newPage(browser, { theme, mobile, state: ownerSingle });
      await v.page.goto(`${base}/`);
      await v.page.locator('[data-account-menu][data-menu-kind="menu"]').waitFor({ timeout: 20_000 });
      if (mobile) {
        const links = v.page.getByRole('navigation', { name: 'Primary' }).getByRole('link');
        const shown = await links.evaluateAll((els) => els.filter((e) => e.getBoundingClientRect().width > 0).length);
        const inView = await v.page.locator('[data-account-menu]').evaluate((el) => el.getBoundingClientRect().right <= window.innerWidth);
        check(`phone ${theme}: the four places are icons and the menu fits`, shown === 4 && inView, `${shown} links`);
      }
      await shotTop(v.page, `topbar-just-me-${tag}`);
      await v.page.locator('[data-account-menu]').click();
      await v.page.getByRole('menu').waitFor({ timeout: 10_000 });
      await shot(v.page, `menu-just-me-${tag}`);
      await v.ctx.close();
    }
    await openUsers(page);
  }

  // 2. The wizard: Email and password, with the owner's email and a password.
  await users.locator('[data-method-change]').click();
  const wizard = page.locator('[data-method-wizard]');
  await wizard.waitFor({ timeout: 10_000 });
  check('the wizard opens in a styled dialog', (await page.locator('[data-mr-dialog="open"] [data-method-wizard]').count()) === 1);
  await wizard.locator('[data-method-option="password"]').click();
  await wizard.locator('[data-method-next]').click();
  await page.locator('[data-method-wizard][data-step="details"]').waitFor({ timeout: 10_000 });
  const details = page.locator('[data-method-wizard][data-step="details"]');
  await details.locator('[data-method-email]').fill(OWNER_EMAIL);
  await details.locator('[data-new-password]').fill('short one');
  const lengthRule = await details.innerText();
  check('the wizard shows the length rule as you type', /at least 12 characters/i.test(lengthRule));
  await details.locator('[data-new-password]').fill(OWNER_PW);
  await details.locator('[data-password-again]').fill(OWNER_PW + 'x');
  check('it says when the two passwords differ', /don't match/i.test(await details.innerText()));
  await details.locator('[data-password-again]').fill(OWNER_PW);
  const wizardPlain = unplain(await readText(page, '[data-method-wizard]'));
  check('the wizard reads plainly', wizardPlain.length === 0, wizardPlain.join(' | '));
  await shot(page, 'method-wizard-desktop-dark');
  await details.locator('[data-method-save]').click();
  const methodSet = await waitFor(async () => (await users.locator('[data-method-card]').getAttribute('data-method')) === 'password', 15_000);
  check('choosing Email and password sets it', !!methodSet, await users.locator('[data-method-summary]').innerText().catch(() => ''));
  check('the wizard closes', (await page.locator('[data-method-wizard]').count()) === 0);
  check('inviting turns on', await waitFor(async () => !(await users.locator('[data-invite-button]').isDisabled()), 10_000));
  check('the owner is still signed in afterwards', (await headerName(page)).length > 0, await headerName(page));
  await page.locator('[data-account-menu][data-menu-kind="account"]').waitFor({ timeout: 15_000 }).catch(() => undefined);
  check('with sign-in on, the menu button is the person (initials: no picture yet)', (await page.locator('[data-account-menu][data-menu-kind="account"] [data-initials]').count()) === 1);

  // 10. Pictures: a file that isn't one is refused; a picture is cropped, shown, then removed.
  {
    await page.goto(`${base}/settings/people#your-account`);
    const mine = page.locator('[data-your-account]');
    await mine.locator('[data-avatar-input]').waitFor({ state: 'attached', timeout: 20_000 });
    await mine.locator('[data-avatar-input]').setInputFiles({ name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('not a picture\n') });
    const err = mine.locator('[data-avatar-error]');
    await err.waitFor({ timeout: 10_000 }).catch(() => undefined);
    const errText = (await err.count()) ? (await err.innerText()).trim() : '';
    check('a file that isn\'t a picture is refused in plain words', errText === "That isn't a picture MetaRush can use. Choose a PNG, JPEG or WebP file.", errText);
    check('…and nothing opens', (await page.locator('[data-avatar-crop]').count()) === 0);
    await mine.locator('[data-avatar-input]').setInputFiles({ name: 'me.png', mimeType: 'image/png', buffer: await makePicture(page) });
    const crop = page.locator('[data-mr-dialog="open"] [data-avatar-crop]');
    const cropped = await crop.waitFor({ timeout: 10_000 }).then(() => true).catch(() => false);
    check('choosing a picture opens the crop step in a styled dialog', cropped);
    if (cropped) {
      const area = crop.getByRole('img', { name: 'Your picture, to place and zoom' });
      const box = await area.boundingBox();
      if (box) {
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        await page.mouse.down();
        await page.mouse.move(box.x + box.width / 2 + 40, box.y + box.height / 2 + 10, { steps: 5 });
        await page.mouse.up();
      }
      await crop.locator('[data-avatar-zoom]').focus();
      for (let i = 0; i < 25; i++) await page.keyboard.press('ArrowRight');
      check('the crop zooms', Number(await crop.locator('[data-avatar-zoom]').inputValue()) > 1.1, await crop.locator('[data-avatar-zoom]').inputValue());
      await shot(page, 'avatar-crop-desktop-dark');
      const cropPlain = unplain(await readText(page, '[data-avatar-crop]'));
      check('the crop step reads plainly', cropPlain.length === 0, cropPlain.join(' | '));
      await crop.locator('[data-avatar-crop-save]').click();
    }
    const img = page.locator('[data-account-menu] img[data-avatar]');
    const shownInMenu = await img.waitFor({ timeout: 15_000 }).then(() => true).catch(() => false);
    const src = shownInMenu ? (await img.getAttribute('src')) ?? '' : '';
    const loaded = shownInMenu ? await img.evaluate((el) => new Promise((r) => (el.complete ? r(el.naturalWidth > 0) : (el.onload = () => r(el.naturalWidth > 0), el.onerror = () => r(false))))) : false;
    check('the picture shows in the menu button', shownInMenu && /^\/media\/avatars\/[^/?]+\?v=[0-9a-f]+$/.test(src) && loaded, src);
    check('…and in Your account', (await mine.locator('img[data-your-avatar]').count()) === 1);
    const rowImg = page.locator(`[data-person="${OWNER_EMAIL}"] img[data-person-avatar]`);
    check('…and in the people list', await rowImg.waitFor({ timeout: 15_000 }).then(() => true).catch(() => false));
    check('the error from before is gone', (await mine.locator('[data-avatar-error]').count()) === 0);
    await mine.scrollIntoViewIfNeeded();
    await shot(page, 'avatar-set-desktop-dark');

    // The top bar with the picture, the open menu, and the pages that moved, in every look.
    const withPicture = await owner.ctx.storageState();
    for (const [theme, mobile, tag] of VARIANTS) {
      const v = await newPage(browser, { theme, mobile, state: withPicture });
      await v.page.goto(`${base}/`);
      await v.page.locator('[data-account-menu] img[data-avatar]').waitFor({ timeout: 20_000 });
      await shotTop(v.page, `topbar-picture-${tag}`);
      await v.page.locator('[data-account-menu]').click();
      const m = v.page.getByRole('menu');
      await m.waitFor({ timeout: 10_000 });
      if (tag === 'desktop-dark') {
        const t = await m.innerText();
        check('signed in: the menu says who and offers Your account, the sections, light/dark and Sign out', t.includes(OWNER_EMAIL) && (await m.locator('[data-menu-your-account]').count()) === 1 && (await m.locator('[data-menu-settings-integrations]').count()) === 1 && (await m.locator('[data-menu-theme]').count()) === 1 && (await m.locator('[data-signout]').count()) === 1, t.replace(/\s+/g, ' ').slice(0, 160));
        const menuPlain = unplain(await readText(v.page, '[role="menu"]'));
        check('the menu reads plainly', menuPlain.length === 0, menuPlain.join(' | '));
      }
      await shot(v.page, `menu-picture-${tag}`);
      if (mobile) {
        const mb = await m.boundingBox();
        check(`phone ${theme}: the open menu stays on the screen`, !!mb && mb.x >= 0 && mb.x + mb.width <= 390, JSON.stringify(mb));
      }
      await v.page.keyboard.press('Escape');
      for (const [where, ready, name] of [
        ['/settings/people', '[data-users-section] [data-method-card]', 'settings-people'],
        ['/settings/claude', '#claude-code', 'settings-claude'],
        ['/library/people', '[data-people-page]', 'library-people'],
      ]) {
        await v.page.goto(`${base}${where}`);
        await v.page.locator(ready).first().waitFor({ timeout: 20_000 });
        if (mobile && where.startsWith('/settings')) {
          check(`phone ${theme}: ${where} has the section dropdown, no side menu`, (await v.page.locator('[data-settings-select]').isVisible()) && !(await v.page.locator('[data-settings-menu]').isVisible()));
        }
        if (!mobile && tag === 'desktop-dark' && where.startsWith('/settings')) {
          check(`${where}: the side menu marks the section`, (await v.page.locator(`[data-settings-link="${where.split('/')[2]}"][aria-current="page"]`).count()) === 1);
        }
        await shot(v.page, `${name}-${tag}`);
      }
      await v.ctx.close();
    }

    await page.goto(`${base}/settings/people#your-account`);
    await mine.locator('[data-avatar-remove]').waitFor({ timeout: 20_000 });
    await mine.locator('[data-avatar-remove]').click();
    const back = await waitFor(async () => (await page.locator('[data-account-menu] [data-initials]').count()) === 1 && (await page.locator('[data-account-menu] img').count()) === 0, 15_000);
    check('removing the picture brings the initials back in the menu button', !!back);
    const rowBack = await waitFor(async () => (await page.locator(`[data-person="${OWNER_EMAIL}"] img[data-person-avatar]`).count()) === 0 && (await page.locator(`[data-person="${OWNER_EMAIL}"] [data-initials]`).count()) === 1, 15_000);
    check('…and in the people list', !!rowBack);
    check('…and Remove picture goes away', (await mine.locator('[data-avatar-remove]').count()) === 0);
    await openUsers(page);
  }

  // 3. Invite Maya: the link is shown once.
  await users.locator('[data-invite-button]').click();
  const inviteForm = page.locator('[data-invite-dialog]');
  await inviteForm.waitFor({ timeout: 10_000 });
  await inviteForm.locator('[data-invite-email]').fill(J_EMAIL);
  await inviteForm.locator('[data-invite-submit]').click();
  const linkDialog = page.locator('[data-link-dialog]');
  await linkDialog.waitFor({ timeout: 15_000 });
  const inviteLink = await linkDialog.locator('[data-link]').inputValue();
  const linkText = await linkDialog.innerText();
  check('the invite shows its link', /\/welcome\/[A-Za-z0-9_-]{20,}/.test(inviteLink), inviteLink.replace(/welcome\/.*/, 'welcome/…'));
  check('the link dialog has Copy, the expiry and "shown only now"', /Copy/.test(linkText) && /until/.test(linkText) && /Shown only now/.test(linkText));
  const linkPlain = unplain(await readText(page, '[data-link-dialog]'));
  check('the link dialog reads plainly', linkPlain.length === 0, linkPlain.join(' | '));
  await shot(page, 'invite-link-desktop-dark');
  await linkDialog.getByRole('button', { name: 'Done' }).click();
  const jRow = users.locator(`[data-person="${J_EMAIL}"]`);
  await jRow.waitFor({ timeout: 10_000 });
  check('Maya is listed as invited', (await jRow.getAttribute('data-person-state')) === 'invited', (await jRow.innerText()).replace(/\s+/g, ' '));

  // Settings → People (Users) in every look (the owner's sign-in, copied).
  const ownerState = await owner.ctx.storageState();
  const usersPlain = unplain(await readText(page, '[data-users-section]'));
  check('Settings → People reads plainly (people’s words aside)', usersPlain.length === 0, usersPlain.join(' | '));
  await page.locator('[data-users-section]').scrollIntoViewIfNeeded();
  await shot(page, 'settings-users-desktop-dark');
  for (const [theme, mobile, tag] of VARIANTS.slice(1)) {
    const v = await newPage(browser, { theme, mobile, state: ownerState });
    await openUsers(v.page);
    await v.page.waitForTimeout(500);
    await v.page.locator('[data-users-section]').evaluate((el) => el.scrollIntoView({ block: 'start' }));
    await shot(v.page, `settings-users-${tag}`);
    if (mobile) {
      const box = await v.page.locator('[data-users-section]').boundingBox();
      check(`16px gutters for Users on a phone (${theme})`, !!box && box.x >= 15.5 && box.x + box.width <= 390 - 15.5, box ? `left ${box.x}, right ${390 - box.x - box.width}` : 'not found');
    }
    // Settings → Preferences in every look (D208).
    await v.page.goto(`${base}/settings/preferences`);
    await v.page.locator('[data-preferences] #estimates input[type=checkbox]').first().waitFor({ timeout: 20_000 }).catch(() => undefined);
    await shot(v.page, `preferences-${tag}`);
    if (mobile) {
      const box = await v.page.locator('[data-preferences]').boundingBox();
      check(`16px gutters for Preferences on a phone (${theme})`, !!box && box.x >= 15.5 && box.x + box.width <= 390 - 15.5, box ? `left ${box.x}, right ${390 - box.x - box.width}` : 'not found');
    }
    await v.ctx.close();
  }

  // The welcome page in every look, before it is used (looking doesn't use it up).
  for (const [theme, mobile, tag] of VARIANTS) {
    const v = await newPage(browser, { theme, mobile });
    await v.page.goto(onBase(inviteLink));
    await v.page.locator('[data-welcome-form]').waitFor({ timeout: 20_000 });
    if (tag === 'desktop-dark') {
      const t = await v.page.locator('h1').innerText();
      check('the invite page says "Join MetaRush on …"', /^Join MetaRush on /.test(t), t);
      check('it shows her email, not to be changed', (await v.page.locator('[data-welcome-form] input[name="email"]').inputValue()) === J_EMAIL && (await v.page.locator('[data-welcome-form] input[name="email"]').getAttribute('readonly')) !== null);
      const wPlain = unplain(await readText(v.page, '#root'));
      check('the invite page reads plainly', wPlain.length === 0, wPlain.join(' | '));
    }
    await shot(v.page, `welcome-invite-${tag}`);
    await v.ctx.close();
  }

  // 4. Maya, in a new browser: joins with a name and a password.
  const maya = await newPage(browser);
  await maya.page.goto(onBase(inviteLink));
  await maya.page.locator('[data-welcome-form]').waitFor({ timeout: 20_000 });
  await maya.page.locator('[data-welcome-name]').fill(J_NAME);
  await maya.page.locator('[data-new-password]').fill(J_PW);
  await maya.page.locator('[data-password-again]').fill(J_PW.slice(0, -1));
  check('the welcome page says when the passwords differ', /don't match/i.test(await maya.page.locator('[data-welcome-form]').innerText()));
  await acceptWelcome(maya.page, inviteLink, { name: J_NAME, password: J_PW });
  check('joining lands on the studio', (await pathOf(maya.page)) === '/', maya.page.url());
  check('the top bar names her', (await headerName(maya.page)) === J_NAME, await headerName(maya.page));
  {
    const v = await newPage(browser);
    await v.page.goto(onBase(inviteLink));
    const gone = await waitFor(async () => (await v.page.locator('[data-link-gone]').count()) > 0, 15_000);
    check('the invite link works only once', !!gone, gone ? '' : await v.page.locator('body').innerText());
    await v.ctx.close();
  }

  // 5. She signs out, signs in again; a wrong password is said plainly.
  await maya.page.locator('[data-account-menu]').click();
  await maya.page.locator('[data-signout]').click();
  await maya.page.waitForURL((u) => u.pathname === '/signin', { timeout: 15_000 }).catch(() => undefined);
  await maya.page.locator('[data-signin-form]').waitFor({ timeout: 15_000 }).catch(() => undefined);
  check('signing out goes to the sign-in page', (await pathOf(maya.page)) === '/signin' && (await maya.page.locator('[data-signin-form]').count()) === 1, maya.page.url());
  const again = await signInWith(maya.page, J_EMAIL, J_PW);
  check('she signs in again at /signin', again === '' && (await pathOf(maya.page)) === '/', again);
  check('…and the top bar names her', (await headerName(maya.page)) === J_NAME, await headerName(maya.page));
  {
    const v = await newPage(browser);
    const err = await signInWith(v.page, J_EMAIL, 'not the right password 7');
    check('a wrong password says so plainly', /That email and password don't match/.test(err), err);
    check('…in plain words', unplain(err).length === 0, unplain(err).join(' | '));
    await v.ctx.close();
  }

  // The sign-in page in every look.
  for (const [theme, mobile, tag] of VARIANTS) {
    const v = await newPage(browser, { theme, mobile });
    await v.page.goto(`${base}/signin`);
    await v.page.locator('[data-signin-form]').waitFor({ timeout: 20_000 });
    if (tag === 'desktop-dark') {
      const sPlain = unplain(await readText(v.page, '#root'));
      check('the sign-in page reads plainly', sPlain.length === 0, sPlain.join(' | '));
      check('the password box is a password box with its autocomplete names', (await v.page.locator('input[name="password"]').getAttribute('type')) === 'password' && (await v.page.locator('input[name="password"]').getAttribute('autocomplete')) === 'current-password' && (await v.page.locator('input[name="email"]').getAttribute('autocomplete')) === 'username');
    }
    await shot(v.page, `signin-${tag}`);
    await v.ctx.close();
  }
  {
    // Not signed in, on an app page: sent to /signin, and back after signing in.
    const v = await newPage(browser);
    await v.page.goto(`${base}/settings#users`);
    await v.page.waitForURL((u) => u.pathname === '/signin', { timeout: 20_000 }).catch(() => undefined);
    const nextParam = new URL(v.page.url()).searchParams.get('next');
    check('an app page without a sign-in goes to /signin with where it was', (await pathOf(v.page)) === '/signin' && nextParam === '/settings#users', v.page.url());
    const form = v.page.locator('[data-signin-form]');
    await form.waitFor({ timeout: 20_000 });
    await form.locator('input[name="email"]').fill(J_EMAIL);
    await form.locator('input[name="password"]').fill(J_PW);
    await form.locator('button[type="submit"]').click();
    await waitFor(async () => (await pathOf(v.page)) !== '/signin', 20_000);
    await waitFor(async () => (await pathOf(v.page)) === '/settings/people', 10_000);
    const back = new URL(v.page.url());
    check('…and comes back there after signing in (the section it lives in now)', back.pathname === '/settings/people' && back.hash === '#users', v.page.url());
    await v.page.goto(`${base}/signin?next=${encodeURIComponent('/settings/log')}`);
    const shown = await waitFor(async () => (await v.page.getByText("You're signed in").count()) > 0, 10_000);
    check('/signin when signed in says so instead of looping', !!shown);
    await v.page.goto(`${base}/signin?next=${encodeURIComponent('//example.com/x')}`);
    await v.page.getByRole('button', { name: 'Open MetaRush' }).click();
    await waitFor(async () => (await pathOf(v.page)) !== '/signin', 10_000);
    check('a next address on another site is ignored', new URL(v.page.url()).origin === base && (await pathOf(v.page)) === '/', v.page.url());
    await v.ctx.close();
  }

  // 6. The owner makes a reset link; a fresh browser chooses a new password.
  await openUsers(page);
  const row = users.locator(`[data-person="${J_EMAIL}"]`);
  await waitFor(async () => (await row.getAttribute('data-person-state')) === 'active', 10_000);
  check('Maya can sign in now (listed as such)', (await row.getAttribute('data-person-state')) === 'active', (await row.innerText()).replace(/\s+/g, ' '));
  await row.locator('button[aria-haspopup="menu"]').click();
  await page.getByRole('menuitem', { name: /Make a reset link/ }).click();
  await page.locator('[data-link-dialog][data-link-kind="reset"]').waitFor({ timeout: 15_000 });
  const resetLink = await page.locator('[data-link-dialog] [data-link]').inputValue();
  check('the owner gets a reset link for her', /\/welcome\//.test(resetLink));
  await page.locator('[data-link-dialog]').getByRole('button', { name: 'Done' }).click();

  for (const [theme, mobile, tag] of [VARIANTS[0], VARIANTS[3]]) {
    const v = await newPage(browser, { theme, mobile });
    await v.page.goto(onBase(resetLink));
    await v.page.locator('[data-welcome-form][data-welcome-kind="reset"]').waitFor({ timeout: 20_000 });
    if (tag === 'desktop-dark') check('the reset page says "Choose a new password"', (await v.page.locator('h1').innerText()) === 'Choose a new password');
    await shot(v.page, `welcome-reset-${tag}`);
    await v.ctx.close();
  }
  const fresh = await newPage(browser);
  await acceptWelcome(fresh.page, resetLink, { password: J_PW2 });
  check('the reset lands signed in', (await pathOf(fresh.page)) === '/' && (await headerName(fresh.page)) === J_NAME, fresh.page.url());
  await fresh.ctx.close();

  // Her earlier browser was signed out by the reset: it is sent to /signin.
  await maya.page.goto(`${base}/renders`);
  await maya.page.waitForURL((u) => u.pathname === '/signin', { timeout: 20_000 }).catch(() => undefined);
  check('the reset signed out her other browser (sent to /signin)', (await pathOf(maya.page)) === '/signin', maya.page.url());
  await maya.ctx.close();
  {
    const v = await newPage(browser);
    const old = await signInWith(v.page, J_EMAIL, J_PW);
    check('the old password no longer works', /don't match/.test(old), old || 'it signed in');
    const ok = await signInWith(v.page, J_EMAIL, J_PW2);
    check('the new password works', ok === '' && (await headerName(v.page)) === J_NAME, ok);

    // She changes it herself in Settings → People → Your account: this browser stays signed in.
    await v.page.goto(`${base}/settings/people#your-account`);
    const mine = v.page.locator('[data-your-account]');
    await mine.locator('[data-change-password]').waitFor({ timeout: 30_000 });
    await mine.locator('[data-change-password]').click();
    const dlg = v.page.locator('[data-change-password-dialog]');
    await dlg.waitFor({ timeout: 10_000 });
    await dlg.locator('input[autocomplete="current-password"]').fill(J_PW2);
    await dlg.locator('[data-new-password]').fill(J_PW3);
    await dlg.locator('[data-password-again]').fill(J_PW3);
    await shot(v.page, 'change-password-desktop-dark');
    await dlg.locator('button[type="submit"]').click();
    const closed = await dlg.waitFor({ state: 'detached', timeout: 15_000 }).then(() => true).catch(() => false);
    check('changing the password in Your account works', closed, closed ? '' : (await dlg.innerText().catch(() => '')).replace(/\s+/g, ' '));
    // The dashboard asks for the session again right after the change: give it time to answer.
    await v.page.waitForTimeout(2500);
    check('…and this browser stays signed in (her name stays in the top bar)', (await pathOf(v.page)) === '/settings/people' && (await headerName(v.page)) === J_NAME, `${v.page.url()} ${await headerName(v.page)}`);
    await v.page.reload();
    await v.page.locator('[data-users-section]').waitFor({ timeout: 30_000 }).catch(() => undefined);
    check('…also after a reload', (await pathOf(v.page)) === '/settings/people' && (await headerName(v.page)) === J_NAME, `${v.page.url()} ${await headerName(v.page)}`);
    await v.ctx.close();

    const w = await newPage(browser);
    const before = await signInWith(w.page, J_EMAIL, J_PW2);
    check('the password before the change no longer works', /don't match/.test(before), before || 'it signed in');
    const changed = await signInWith(w.page, J_EMAIL, J_PW3);
    check('the changed password works', changed === '' && (await headerName(w.page)) === J_NAME, changed);
    await w.ctx.close();
  }

  // 11. A member's view (D191), at this computer's own address: every owner-only section shows its settings with
  //     the controls disabled and one line naming the owner; the server refuses a change too; installing the
  //     extension stays open.
  {
    const OWNER_NAME = 'Sam Lee';
    const member = await newPage(browser);
    const signed = await signInWith(member.page, J_EMAIL, J_PW3);
    check("Maya (a member) signs in at this computer's own address", signed === '', signed);
    const sess = await member.page.evaluate(async () => (await (await fetch('/api/session')).json()).data);
    check('her session: not an owner, the owner named', sess.owner === false && JSON.stringify(sess.owners) === JSON.stringify([OWNER_NAME]), JSON.stringify({ owner: sess.owner, owners: sess.owners }));
    const sections = [
      ['/settings/claude', '#claude-code'],
      ['/settings/footage', '#watched-folder'],
      ['/settings/storage', '#machine'],
      ['/settings/integrations', '[data-integrations]'],
      ['/settings/log', '#log'],
      ['/settings/people#remote-access', '[data-remote-access]'],
    ];
    for (const [where, ready] of sections) {
      await member.page.goto('about:blank');
      await member.page.goto(`${base}${where}`);
      await member.page.locator(ready).first().waitFor({ timeout: 20_000 }).catch(() => undefined);
      const lines = member.page.locator('[data-owners-line]');
      await lines.first().waitFor({ timeout: 15_000 }).catch(() => undefined);
      const texts = (await lines.allInnerTexts()).map((t) => t.replace(/\s+/g, ' ').trim());
      check(`${where}: one line, "Only an owner can change this. Owner: ${OWNER_NAME}."`, texts.length === 1 && texts[0] === `Only an owner can change this. Owner: ${OWNER_NAME}.`, JSON.stringify(texts));
      // Some parts are still loading their settings: wait until their controls are there.
      await waitFor(async () => (await member.page.locator('fieldset[data-owner-only] :is(button, input, select, textarea)').count()) > 0, 10_000);
      const fieldsets = await member.page.locator('fieldset[data-owner-only]').count();
      const greyed = await member.page.locator('fieldset[data-owner-only] :is(button, input, select, textarea)').count();
      const enabled = await member.page.locator('fieldset[data-owner-only] :is(button, input, select, textarea):not(:disabled)').count();
      check(`${where}: every owner-only control is disabled`, fieldsets > 0 && enabled === 0, `${fieldsets} parts, ${greyed} controls, ${enabled} still enabled`);
      const p = unplain(await readText(member.page, '[data-owners-line]'));
      check(`${where}: the owners line reads plainly`, p.length === 0, p.join(' | '));
    }
    // Settings → Preferences (D208): her own estimates are hers; asking everyone for them is an owner's.
    {
      const prefsOf = async (p) => p.evaluate(async () => (await (await fetch('/api/ops/account.preferences', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' }, body: '{}' })).json()).data);
      await member.page.goto('about:blank');
      await member.page.goto(`${base}/settings/preferences`);
      const mp = member.page.locator('[data-preferences]');
      await mp.locator('#estimates input[type=checkbox]').first().waitFor({ timeout: 20_000 }).catch(() => undefined);
      const herBox = mp.getByLabel('Estimate costs of outside AI services before edits');
      const allBox = mp.getByLabel('Ask everyone for estimates');
      const ownersLine = (await mp.locator('[data-owners-line]').allInnerTexts()).map((t) => t.replace(/\s+/g, ' ').trim());
      check(
        'a member changes her own estimates; asking everyone is greyed, with the owners line',
        (await herBox.isEnabled()) && (await allBox.isDisabled()) && ownersLine.length === 1 && ownersLine[0] === `Only an owner can change this. Owner: ${OWNER_NAME}.`,
        JSON.stringify(ownersLine),
      );
      await herBox.uncheck();
      await waitFor(async () => (await prefsOf(member.page)).estimates === false, 10_000);
      check("her estimates are off; the owner's are still on", (await prefsOf(member.page)).estimates === false && (await prefsOf(page)).estimates === true);
      const askAll = await member.page.evaluate(async () => {
        const r = await fetch('/api/ops/account.preferences.set', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' }, body: JSON.stringify({ requireEstimates: true }) });
        return { status: r.status, body: await r.json() };
      });
      check('the server refuses a member asking everyone for estimates', askAll.status === 401 && askAll.body.error?.details?.reason === 'owner_only', JSON.stringify(askAll).slice(0, 200));
      // The owner asks everyone: hers come first again, and the page says who asked.
      await page.goto('about:blank');
      await page.goto(`${base}/settings/preferences`);
      const op = page.locator('[data-preferences]');
      await op.locator('#estimates input[type=checkbox]').first().waitFor({ timeout: 20_000 }).catch(() => undefined);
      await op.getByLabel('Ask everyone for estimates').check();
      await waitFor(async () => (await prefsOf(page)).estimatesRequired === true, 10_000);
      await member.page.reload();
      await mp.locator('[data-estimates-required]').waitFor({ timeout: 15_000 }).catch(() => undefined);
      const required = (await mp.locator('[data-estimates-required]').innerText().catch(() => '')).replace(/\s+/g, ' ');
      check(
        'an owner asked everyone: her estimates come first again, greyed, saying who asked',
        (await herBox.isChecked()) && (await herBox.isDisabled()) && required.includes(`${OWNER_NAME} asks everyone for estimates`) && (await prefsOf(member.page)).estimates === true,
        required,
      );
      await shot(member.page, 'member-preferences-required-desktop-dark');
      await op.getByLabel('Ask everyone for estimates').uncheck();
      await waitFor(async () => (await prefsOf(page)).estimatesRequired === false, 10_000);
      check('the owners stop asking: her own choice (off) counts again', (await prefsOf(member.page)).estimates === false);
      await member.page.reload();
      await herBox.waitFor({ timeout: 15_000 }).catch(() => undefined);
      await herBox.check();
      await waitFor(async () => (await prefsOf(member.page)).estimates === true, 10_000);
    }
    // 12. Settings → Browser extension: a pairing is its person's (D197). The member pairs her own browser.
    await member.page.goto('about:blank');
    await member.page.goto(`${base}/settings/extension`);
    const ext = member.page.locator('#browser-extension');
    await ext.getByRole('button', { name: 'Pair a browser' }).waitFor({ timeout: 20_000 }).catch(() => undefined);
    check('a member can pair her own browser (no owners line)', await ext.getByRole('button', { name: 'Pair a browser' }).isEnabled().catch(() => false) && (await member.page.locator('[data-owners-line]').count()) === 0);
    check('…and copy where the extension is, to install it', await ext.getByRole('button', { name: 'Copy its location' }).isEnabled().catch(() => false));
    await ext.getByRole('button', { name: 'Pair a browser' }).click();
    const codeBox = member.page.getByLabel('Pairing code');
    await codeBox.waitFor({ timeout: 15_000 }).catch(() => undefined);
    const herCode = (await codeBox.innerText().catch(() => '')).trim();
    const pairText = await member.page.getByRole('dialog').innerText().catch(() => '');
    check('her code says what that browser sends is hers', /^[A-Z0-9]{4}-[A-Z0-9]{4}$/.test(herCode) && pairText.includes(`What that browser sends is yours (${J_NAME})`), `${herCode} ${pairText.replace(/\s+/g, ' ').slice(0, 160)}`);
    await shot(member.page, 'member-pair-code-desktop-dark');
    await member.page.getByRole('dialog').getByRole('button', { name: 'Done' }).click();
    // The extension pairs with her code (as the extension does: its own origin and header).
    const herBrowser = await extPair(herCode, 'chrome-extension://cccccccccccccccccccccccccccccccc');
    check('her browser is paired for her', herBrowser.ok && herBrowser.data?.for === J_NAME, JSON.stringify({ status: herBrowser.status, for: herBrowser.data?.for, error: herBrowser.error?.message }));
    // The owner pairs one of theirs too (the owner's own code).
    const ownerCode = await page.evaluate(async () => (await (await fetch('/api/ops/extension.pair', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' }, body: '{}' })).json()).data.code);
    const ownersBrowser = await extPair(ownerCode, 'chrome-extension://dddddddddddddddddddddddddddddddd');
    check("the owner's browser is paired for the owner", ownersBrowser.ok && ownersBrowser.data?.for === 'Sam Lee', JSON.stringify({ status: ownersBrowser.status, for: ownersBrowser.data?.for, error: ownersBrowser.error?.message }));
    await member.page.reload();
    const mine = member.page.locator('[data-your-browsers]');
    await mine.locator('[data-browser]').first().waitFor({ timeout: 15_000 }).catch(() => undefined);
    check('Your browsers: hers only; no Everyone\'s browsers for a member', (await mine.locator('[data-browser]').count()) === 1 && (await member.page.locator('[data-everyones-browsers]').count()) === 0);
    const herStatus = await member.page.evaluate(async () => (await (await fetch('/api/ops/extension.status', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' }, body: '{}' })).json()).data);
    check('her status lists only her browser', herStatus.browsers.length === 1 && herStatus.everyone.length === 0 && herStatus.owner === false, JSON.stringify({ b: herStatus.browsers.length, e: herStatus.everyone.length }));
    const removeOwners = await member.page.evaluate(async (id) => {
      const r = await fetch('/api/ops/extension.revoke', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' }, body: JSON.stringify({ id }) });
      return { status: r.status, body: await r.json() };
    }, ownersBrowser.data?.id);
    check("the server refuses her removing the owner's browser", removeOwners.status === 401 && removeOwners.body.error?.details?.reason === 'owner_only', JSON.stringify(removeOwners).slice(0, 200));
    await shot(member.page, 'member-your-browsers-desktop-dark');
    // The owner sees everyone's, with whose each is.
    await page.goto('about:blank');
    await page.goto(`${base}/settings/extension`);
    const all = page.locator('[data-everyones-browsers]');
    await all.waitFor({ timeout: 15_000 }).catch(() => undefined);
    check("the owner sees Everyone's browsers, with hers named", (await all.locator('[data-browser]').count()) === 1 && (await all.innerText().catch(() => '')).includes(J_NAME) && (await page.locator('[data-your-browsers] [data-browser]').count()) === 1);
    await shot(page, 'owner-everyones-browsers-desktop-dark');
    // The server says no too, naming the owner, whatever the page shows.
    const refused = await member.page.evaluate(async () => {
      const r = await fetch('/api/ops/trash.configure', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' }, body: JSON.stringify({ emptyAfterDays: 30 }) });
      return { status: r.status, body: await r.json() };
    });
    check('the server refuses a member\'s change: owner_only, the owner named', refused.status === 401 && refused.body.error?.details?.reason === 'owner_only' && JSON.stringify(refused.body.error?.details?.owners) === JSON.stringify([OWNER_NAME]) && refused.body.error?.message === 'Only an owner of this MetaRush can change that.', JSON.stringify(refused));
    // A member's view of Claude Code and Browser extension, desktop and phone.
    const memberState = await member.ctx.storageState();
    await member.ctx.close();
    for (const [theme, mobile, tag] of VARIANTS) {
      const v = await newPage(browser, { theme, mobile, state: memberState });
      for (const [where, ready, name] of [
        ['/settings/claude', '#claude-code', 'member-claude'],
        ['/settings/extension', '#browser-extension', 'member-your-browsers'],
      ]) {
        await v.page.goto(`${base}${where}`);
        await v.page.locator(ready).first().waitFor({ timeout: 20_000 }).catch(() => undefined);
        if (name === 'member-claude') await v.page.locator('[data-owners-line]').first().waitFor({ timeout: 15_000 }).catch(() => undefined);
        else await v.page.locator('[data-your-browsers] [data-browser]').first().waitFor({ timeout: 15_000 }).catch(() => undefined);
        await shot(v.page, `${name}-${tag}`);
      }
      await v.ctx.close();
    }
    // She removes her own browser (a styled confirm).
    const again = await newPage(browser, { state: memberState });
    await again.page.goto(`${base}/settings/extension`);
    const hers = again.page.locator('[data-your-browsers] [data-browser]');
    await hers.first().waitFor({ timeout: 15_000 }).catch(() => undefined);
    await hers.first().getByRole('button', { name: /^Remove/ }).click();
    const ask = again.page.getByRole('alertdialog');
    await ask.waitFor({ timeout: 10_000 }).catch(() => undefined);
    await ask.getByRole('button', { name: 'Remove' }).click();
    const gone = await waitFor(async () => (await again.page.locator('[data-your-browsers] [data-browser]').count()) === 0, 10_000);
    check('she removes her own browser', !!gone);
    const after = await extHello(herBrowser.data?.token, 'chrome-extension://cccccccccccccccccccccccccccccccc');
    check('…and it stops working at once', after.status === 401, String(after.status));
    await again.ctx.close();
  }

  // 7. The activity log names people and filters by person.
  await page.goto(`${base}/log?range=hour`);
  await page.locator('[data-entry]').first().waitFor({ timeout: 20_000 }).catch(() => undefined);
  check('/log opens Settings → Log, keeping what it asked for', (await pathOf(page)) === '/settings/log' && new URL(page.url()).searchParams.get('range') === 'hour', page.url());
  const personSelect = page.locator('[data-log-person]');
  check('the log offers a filter by person', (await personSelect.count()) === 1 && (await personSelect.locator('option', { hasText: J_NAME }).count()) === 1);
  const named = await page.locator('[data-entry-person]').count();
  check('log entries name who did them', named > 0, `${named} entries named`);
  if ((await personSelect.count()) === 1) {
    const mayaId = await personSelect.locator('option', { hasText: J_NAME }).getAttribute('value');
    await personSelect.selectOption(mayaId ?? '');
    await page.waitForTimeout(600);
    const others = await page.evaluate((id) => [...document.querySelectorAll('[data-entry-person]')].filter((el) => el.getAttribute('data-entry-person') !== id).length, mayaId);
    check('filtering by Maya shows only her entries', others === 0, `${others} entries of others shown`);
  }
  await shot(page, 'log-by-person-desktop-dark');
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
