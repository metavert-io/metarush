#!/usr/bin/env node
// Signing in with an organization's account in a real browser (D425–D434,
// docs/accounts.md). Run it through scripts/sso-e2e.sh: a throwaway
// installation where the owner is signed in with a one-time link, and a
// stand-in sign-in provider on this computer whose page asks who signs in.
// It checks that:
//   1. Settings → People shows the organization sign-in "Not set up", and the
//      method wizard sends the owner to it;
//   2. the owner sets it up (another provider: its name, address, the app's
//      ID and secret in the one-time form, an allowed domain); the secret
//      never comes back; the addresses to register are shown with Copy;
//   3. the test sign-in opens in a tab of its own, goes to the provider and
//      back to 127.0.0.1 (never localhost), and says it worked; Settings sees
//      it and offers the switch; the switch (a styled confirm) makes it how
//      people sign in;
//   3b. the owner sets up another app with its own secret: it waits for its
//      test, and steps 4–6 sign people in with the one in use meanwhile;
//   4. an invited person follows the invite link, joins with the provider and
//      lands signed in as themselves; the link doesn't work twice;
//   5. a person of the allowed domain joins by themselves, as a member;
//   6. a stranger is turned away with the email they used, in plain words;
//   6b. the owner forgets the waiting app (a styled confirm);
//   7. the owner signs out and signs in again through the provider;
//   7b. two sign-ins started in two tabs of one browser both finish;
//   8. the pages read plainly; no native dialogs, page errors or horizontal
//      overflow; screenshots of the sign-in page, the Settings part and the
//      refusal, dark and light, desktop and phone.
// It CHANGES the installation's sign-in: point it only at a throwaway one.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'SSO_PROVIDER', 'SSO_CLIENT_ID', 'FAKEOIDC_SECRET', 'SSO_SECRET_B', 'SSO_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/sso-e2e.sh does)`);
    process.exit(2);
  }
}
const provider = process.env.SSO_PROVIDER.replace(/\/$/, '');
const secret = process.env.FAKEOIDC_SECRET;
const secretB = process.env.SSO_SECRET_B;
const shotsDir = process.env.SSO_SHOTS;
mkdirSync(shotsDir, { recursive: true });
const port = new URL(base).port;
const localBase = `http://127.0.0.1:${port}`; // where sign-ins on this computer come back (the provider's registered address; never localhost)

// Made-up people (example domains only).
const OWNER = 'sam@example.com';
const INVITED = 'maya@example.com';
const DOMAIN_PERSON = 'carol@example.org';
const STRANGER = 'bob@example.net';

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
  watch(page);
  return { ctx, page };
}

function watch(page) {
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) problems.push(`console: ${m.text()}`);
  });
  page.on('dialog', (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
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
  const bad = checkShown(text).map((f) => `${f.kind}: ${f.text}`);
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

/** The stand-in provider's page: who signs in. */
async function atProvider(page, email, { verified = true } = {}) {
  await page.waitForURL((u) => u.toString().startsWith(provider), { timeout: 20_000 });
  await page.locator('[data-fake-form]').waitFor({ timeout: 10_000 });
  await page.locator('[data-fake-email]').fill(email);
  await page.locator('[data-fake-name]').fill(email.split('@')[0].replace(/^./, (c) => c.toUpperCase()));
  if (!verified) await page.locator('[data-fake-verified]').uncheck();
  await page.locator('[data-fake-continue]').click();
}

async function session(page) {
  return page.evaluate(async () => {
    const r = await fetch('/api/session');
    return r.ok ? (await r.json()).data : null;
  });
}

/** Signs in through the sign-in page's button as email; returns where the browser lands. */
async function signInThroughPage(page, email, opts) {
  await page.goto(`${base}/signin`);
  const btn = page.locator('[data-sso-signin]');
  await btn.waitFor({ timeout: 15_000 });
  await btn.click();
  await atProvider(page, email, opts);
  await page.waitForURL((u) => !u.toString().startsWith(provider), { timeout: 20_000 });
  await page.waitForLoadState('networkidle').catch(() => undefined);
  return page.url();
}

async function main() {
  const browser = await chromium.launch();
  const owner = await newPage(browser);
  const { page } = owner;

  // 1. Not set up; the wizard sends the owner to its part.
  await page.goto(mintLink());
  await page.goto(`${base}/settings/people`);
  const part = page.locator('[data-sso-section]');
  await part.waitFor({ timeout: 20_000 });
  check('Settings → People has the organization sign-in, not set up', /Not set up/.test(await part.innerText()));
  await page.locator('[data-method-change]').click();
  await page.locator('[data-method-option="sso"]').click();
  await page.locator('[data-method-next]').click();
  await page.locator('[data-method-sso-go]').click();
  await page.waitForTimeout(400);
  check('the method wizard sends the owner to the setup', (await page.locator('[role="dialog"]').count()) === 0);

  // 2. The setup: another provider, the app's secret in the one-time form, an allowed domain.
  await page.locator('[data-sso-setup-button]').click();
  const form = page.locator('[data-sso-form]');
  await form.waitFor({ timeout: 10_000 });
  await form.getByRole('radio', { name: 'Another provider' }).click().catch(async () => form.getByText('Another provider').click());
  await form.locator('[data-sso-label]').fill('Test provider');
  await form.locator('[data-sso-issuer]').fill(provider);
  await form.locator('[data-sso-client-id]').fill(process.env.SSO_CLIENT_ID);
  await form.locator('[data-sso-secret]').fill(secret);
  await form.locator('[data-sso-domain]').fill('example.org');
  await shot(page, 'setup-form-desktop-dark');
  await form.locator('[data-sso-save]').click();
  await waitFor(async () => (await page.locator('[role="dialog"]').count()) === 0, 15_000);
  await waitFor(async () => /Set up, not in use/.test(await part.innerText()), 15_000);
  const partText = await part.innerText();
  check('the setup is kept and waits for its test', /Set up, not in use/.test(partText) && /Test provider/.test(partText), partText.slice(0, 160));
  check('the app secret never comes back to the browser', !(await page.content()).includes(secret));
  const callbacks = await page.locator('[data-sso-callback]').evaluateAll((els) => els.map((e) => e.value));
  check('the address to register is shown, at 127.0.0.1', callbacks.includes(`${localBase}/auth/sso/callback`) && !callbacks.some((c) => c.includes('localhost')), callbacks.join(', '));
  await plain(page, '[data-sso-section]', 'the organization sign-in part');
  await shot(page, 'people-sign-in-part-desktop-dark');

  // 3. The test sign-in in a tab of its own, then the switch.
  const [tab] = await Promise.all([page.context().waitForEvent('page'), page.locator('[data-sso-test-button]').click()]);
  watch(tab);
  await atProvider(tab, OWNER);
  await tab.waitForURL((u) => u.toString().startsWith(`${localBase}/auth/sso/callback`), { timeout: 20_000 });
  const testText = await tab.locator('main').innerText().catch(() => '');
  check('the test sign-in worked, as the owner', /test sign-in worked/i.test(testText) && testText.includes(OWNER), testText.slice(0, 200));
  await shot(tab, 'test-result');
  await tab.close();
  const sw = page.locator('[data-sso-switch]');
  const offered = await waitFor(async () => (await sw.count()) === 1 && (await sw.isEnabled()), 20_000);
  check('Settings sees the test and offers the switch', !!offered);
  await sw.click();
  const ask = page.getByRole('dialog').filter({ hasText: 'from now on' });
  await ask.waitFor({ timeout: 10_000 });
  await ask.getByRole('button', { name: /^Switch to/ }).click();
  const inUse = await waitFor(async () => /In use/.test(await part.innerText()), 15_000);
  check('switched: people sign in with the provider', !!inUse);
  await shot(page, 'people-sign-in-in-use-desktop-dark');

  // 3b. Another app with its own secret: it waits; the one in use goes on.
  await page.locator('[data-sso-setup-button]').click();
  const change = page.locator('[data-sso-form]');
  await change.waitFor({ timeout: 10_000 });
  const keepHelp = await change.innerText();
  check('the form offers to keep this app’s secret', /has this app’s secret/.test(keepHelp), keepHelp.slice(0, 300));
  await change.locator('[data-sso-client-id]').fill(`${process.env.SSO_CLIENT_ID}-b`);
  const newHelp = await change.innerText();
  check('another app asks for its own secret', !/has this app’s secret/.test(newHelp) && /This app’s secret/.test(newHelp));
  await change.locator('[data-sso-secret]').fill(secretB);
  await change.locator('[data-sso-save]').click();
  await waitFor(async () => (await page.locator('[role="dialog"]').count()) === 0, 15_000);
  const waiting = await waitFor(async () => {
    const t = await part.innerText();
    return /waits for its test/.test(t) && /keep using the one they have/.test(t) ? t : undefined;
  }, 15_000);
  check('the new app waits; sign-ins keep the secret in use', !!waiting, (waiting ?? (await part.innerText())).slice(0, 300));
  check('the second secret never comes back to the browser', !(await page.content()).includes(secretB));
  check('the waiting app can be forgotten', (await page.locator('[data-sso-discard]').count()) === 1);
  await plain(page, '[data-sso-section]', 'the part with an app waiting');
  await shot(page, 'people-sign-in-another-app-waiting-desktop-dark');

  // 4. An invited person joins with the invite link.
  await page.locator('[data-invite-button]').click();
  const inv = page.locator('[data-invite-dialog]');
  await inv.waitFor({ timeout: 10_000 });
  check('the invite dialog says they join with the provider', /sign in with Test provider/.test(await inv.innerText()));
  await inv.locator('[data-invite-email]').fill(INVITED);
  await inv.locator('[data-invite-submit]').click();
  const linkBox = page.locator('[data-link]');
  await linkBox.waitFor({ timeout: 10_000 });
  const link = await linkBox.inputValue();
  check('the invite link says how they join', /join by signing in with Test provider/.test(await page.locator('[data-link-summary]').innerText()));
  await page.getByRole('button', { name: 'Done' }).click();
  const maya = await newPage(browser, { theme: 'light' });
  await maya.page.goto(`${base}${new URL(link).pathname}`);
  const join = maya.page.locator('[data-welcome-sso] [data-sso-signin]');
  await join.waitFor({ timeout: 15_000 });
  await plain(maya.page, 'main, body', 'the invite page');
  await shot(maya.page, 'welcome-join-light');
  await join.click();
  await atProvider(maya.page, INVITED);
  await maya.page.waitForURL((u) => u.toString().startsWith(localBase) && !u.pathname.startsWith('/auth/'), { timeout: 20_000 });
  const ms = await waitFor(() => session(maya.page), 10_000);
  check('the invited person lands signed in as themselves', ms?.user?.email === INVITED && ms?.access?.signedInWith === 'sso' && !ms?.owner, JSON.stringify(ms?.user ?? null));
  const again = await newPage(browser);
  await again.page.goto(`${base}${new URL(link).pathname}`);
  await again.page.locator('[data-link-gone]').waitFor({ timeout: 15_000 }).catch(() => undefined);
  check("the invite link doesn't work twice", (await again.page.locator('[data-link-gone]').count()) === 1);
  await again.ctx.close();
  await maya.ctx.close();

  // 5. A person of the allowed domain joins by themselves.
  const carol = await newPage(browser);
  const landed = await signInThroughPage(carol.page, DOMAIN_PERSON);
  const cs = await waitFor(() => session(carol.page), 10_000);
  check('a person of the allowed domain joins as a member', cs?.user?.email === DOMAIN_PERSON && !cs?.owner, `${landed} ${JSON.stringify(cs?.user ?? null)}`);
  await carol.ctx.close();

  // 6. A stranger is turned away, in plain words, with the email they used.
  for (const [theme, mobile] of [
    ['dark', false],
    ['light', true],
  ]) {
    const bob = await newPage(browser, { theme, mobile });
    const at = await signInThroughPage(bob.page, STRANGER);
    const why = bob.page.locator('[data-sso-problem]');
    await why.waitFor({ timeout: 15_000 }).catch(() => undefined);
    const words = (await why.count()) ? await why.innerText() : '';
    check(`a stranger is turned away plainly (${theme}${mobile ? ', phone' : ''})`, at.includes('/signin') && words.includes(STRANGER) && /invite you/.test(words), words || at);
    if (!mobile) await plain(bob.page, 'main, body', 'the refusal');
    await shot(bob.page, `signin-refused-${mobile ? 'phone' : 'desktop'}-${theme}`);
    check('…and is not signed in', !(await session(bob.page)));
    await bob.ctx.close();
  }

  // 6b. The owner forgets the waiting app.
  await page.locator('[data-sso-discard]').click();
  const forget = page.getByRole('dialog').filter({ hasText: 'Forget the new setup' });
  await forget.waitFor({ timeout: 10_000 });
  await plain(page, '[role="dialog"]', 'the forget dialog');
  await forget.getByRole('button', { name: 'Forget it' }).click();
  const forgotten = await waitFor(async () => (await page.locator('[data-sso-discard]').count()) === 0 && !/waits for its test/.test(await part.innerText()), 15_000);
  check('the waiting app is forgotten; the one in use stays', !!forgotten && /In use/.test(await part.innerText()));

  // 7. The owner signs out and back in through the provider.
  const back = await newPage(browser, { theme: 'light' });
  await back.page.goto(`${base}/signin`);
  await back.page.locator('[data-sso-signin]').waitFor({ timeout: 15_000 });
  await plain(back.page, 'main, body', 'the sign-in page');
  await shot(back.page, 'signin-desktop-light');
  await signInThroughPage(back.page, OWNER);
  const os = await waitFor(() => session(back.page), 10_000);
  check('the owner signs in again through the provider', os?.user?.email === OWNER && os?.owner === true && os?.access?.provider === 'Test provider', JSON.stringify(os ?? null));
  // 7b. Two sign-ins started in two tabs of one browser: the first tab's
  // answer comes back first and still finishes.
  const tabs = await newPage(browser);
  const tab2 = await tabs.ctx.newPage();
  watch(tab2);
  for (const p of [tabs.page, tab2]) {
    await p.goto(`${base}/signin`);
    await p.locator('[data-sso-signin]').waitFor({ timeout: 15_000 });
    await p.locator('[data-sso-signin]').click();
    await p.waitForURL((u) => u.toString().startsWith(provider), { timeout: 20_000 });
  }
  await atProvider(tabs.page, INVITED);
  await tabs.page.waitForURL((u) => !u.toString().startsWith(provider), { timeout: 20_000 });
  await tabs.page.waitForLoadState('networkidle').catch(() => undefined);
  const firstTab = await tabs.page.locator('body').innerText();
  const ts = await waitFor(() => session(tabs.page), 10_000);
  check('the first of two tabs finishes its sign-in', ts?.user?.email === INVITED && !/wasn.t started in this browser/.test(firstTab), firstTab.slice(0, 200));
  await atProvider(tab2, INVITED);
  await tab2.waitForURL((u) => !u.toString().startsWith(provider), { timeout: 20_000 });
  const secondTab = await tab2.locator('body').innerText();
  check('…and so does the second', !/wasn.t started in this browser/.test(secondTab) && !tab2.url().includes('/signin'), `${tab2.url()} ${secondTab.slice(0, 160)}`);
  await tabs.ctx.close();

  const phone = await newPage(browser, { mobile: true });
  await phone.page.goto(`${base}/signin`);
  await phone.page.locator('[data-sso-signin]').waitFor({ timeout: 15_000 });
  await shot(phone.page, 'signin-phone-dark');
  await phone.ctx.close();
  await back.ctx.close();
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
