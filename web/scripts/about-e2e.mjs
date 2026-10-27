#!/usr/bin/env node
// Settings → General (About MetaRush, D130) in a real browser. Run it through
// scripts/about-e2e.sh: a workspace with this computer ("Studio Mac") and an
// older one ("Mac mini", a build that says 0.1.0). It checks that:
//   1. About heads Settings → General (what /settings opens) with the version
//      and its day ("MetaRush 0.2.0 · 20 Oct 2026"), a build past a release
//      saying so in plain words, the exact build only behind Details;
//   2. both computers are listed with the version they run, the mini flagged
//      older in plain words, with how to update it behind Details;
//   3. what's new comes from the changelog built into this version ("What's
//      new in this version", or "Coming in the next version" in a build past
//      a release), a few headlines first, Show all, one opened to read;
//   4. the section speaks plainly, no native dialogs, page errors or
//      horizontal overflow. Screenshots: desktop dark and light, phone dark
//      and light, in ABOUT_SHOTS.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
for (const k of ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'ABOUT_SHOTS']) {
  if (!process.env[k]) {
    console.error('set METARUSH_DASHBOARD, METARUSH_MINT and ABOUT_SHOTS (scripts/about-e2e.sh does)');
    process.exit(2);
  }
}
const base = process.env.METARUSH_DASHBOARD.replace(/\/$/, '');
const shotsDir = process.env.ABOUT_SHOTS;
mkdirSync(shotsDir, { recursive: true });

const results = [];
const problems = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
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
    if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) problems.push(`${label}: console: ${m.text()}`);
  });
  page.on('dialog', (d) => {
    problems.push(`${label}: native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  return { ctx, page };
}

let shotN = 0;
async function shot(page, name, { full = false } = {}) {
  await page.waitForTimeout(400);
  await page.screenshot({ path: path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`), fullPage: full });
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

/** The About section's words as people read them (closed Details stay out, people's own words marked theirs). */
async function aboutText(page) {
  return page.locator('[data-about]').evaluate((sec) => {
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

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
function dayOf(d) {
  if (!d.dev && d.releaseDate) {
    const t = new Date(`${d.releaseDate}T00:00:00Z`);
    return `${t.getUTCDate()} ${MONTHS[t.getUTCMonth()]} ${t.getUTCFullYear()}`;
  }
  const t = new Date(d.date);
  return Number.isFinite(t.getTime()) ? `${t.getDate()} ${MONTHS[t.getMonth()]} ${t.getFullYear()}` : '';
}

async function main() {
  const browser = await chromium.launch();
  const { ctx, page } = await newPage(browser, { label: 'desktop dark' });
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const state = await ctx.storageState();
  const v = await api(page, 'version.show');
  console.log(`version.show: ${v.version} (release ${v.release}, dev ${v.dev}), ${v.computers.length} computers, ${v.whatsNew.map((s) => `${s.heading}: ${s.entries.length}`).join('; ')}`);

  await page.goto(`${base}/settings`);
  const about = page.locator('[data-about]');
  await about.locator('[data-about-version]').waitFor({ timeout: 20_000 });
  const after = await page.evaluate(() => {
    const a = document.querySelector('[data-about]');
    // Every other Settings section (the workspace and storage cards, remote access, the watched folder…) comes after it.
    return [...document.querySelectorAll('section.panel, section[id]')]
      .filter((s) => s !== a && !a.contains(s))
      .map((s) => [s.id || s.querySelector('h2')?.textContent?.trim() || '?', !!(a.compareDocumentPosition(s) & Node.DOCUMENT_POSITION_FOLLOWING)]);
  });
  check('About heads Settings → General (what /settings opens)', after.length >= 2 && after.every(([, f]) => f) && (await page.locator('[data-settings="general"]').count()) === 1, after.filter(([, f]) => !f).map(([n]) => n).join(', '));
  const head = (await about.locator('[data-about-version]').innerText()).trim();
  check('it says the version', head === `MetaRush ${v.release}`, head);
  const day = dayOf(v);
  const aboutAll = await about.innerText();
  check("and the version's day", day !== '' && aboutAll.includes(day), day);
  const devNote = aboutAll.includes("Includes changes that aren't in a release yet.");
  check(v.dev ? 'a build past a release says so in plain words' : 'a release build says nothing about newer changes', devNote === v.dev);
  check('the exact build is only behind Details', !aboutAll.includes(v.commit) && (v.dev ? !aboutAll.includes(v.version) || v.version === v.release : true), v.version);
  await about.locator('summary', { hasText: /^Details$/ }).first().click();
  const opened = await about.innerText();
  check('Details has the exact version and the commit', opened.includes(v.version) && opened.includes(v.commit));
  await about.locator('summary', { hasText: /^Details$/ }).first().click();

  // The computers.
  const rows = about.locator('[data-about-computers] > li');
  check('both computers are listed', (await rows.count()) === 2, String(await rows.count()));
  const me = await rows.nth(0).innerText();
  check('this computer first, with its version', me.includes('Studio Mac') && me.includes('this computer') && me.includes(`MetaRush ${v.release}`), me.replace(/\s+/g, ' '));
  const mini = about.locator('[data-about-computer="older"]');
  const miniText = (await mini.count()) ? await mini.innerText() : '';
  check('the mini is flagged older', miniText.includes('Mac mini') && miniText.includes('MetaRush 0.1.0') && miniText.includes('Older'), miniText.replace(/\s+/g, ' '));
  const note = (await about.locator('[data-about-older]').count()) ? await about.locator('[data-about-older]').innerText() : '';
  check('in plain words, with what to do', note.includes('Mac mini runs an older MetaRush than this computer. Update MetaRush there to get what\'s new.'), note.replace(/\s+/g, ' '));

  // What's new.
  const sections = about.locator('[data-whats-new]');
  const firstTitle = (await sections.first().locator('h3').innerText()).trim();
  // The heading About.tsx gives the first section: changes not released yet, this version's, or (in a
  // build past a release with nothing new in the changelog yet, right after a release) the release's.
  const first = v.whatsNew[0];
  const wantTitle = !first.release ? 'Coming in the next version' : first.release === v.release && !v.dev ? "What's new in this version" : `What's new in ${first.release}`;
  check(`what's new: "${wantTitle}" first`, firstTitle.startsWith(wantTitle), firstTitle);
  const s0 = v.whatsNew[0];
  const plainCount = s0.entries.filter((e) => e.headline).length;
  const heads = sections.first().locator('[data-change] > details > summary');
  check('a few headlines first', (await heads.count()) === Math.min(6, plainCount), `${await heads.count()} of ${plainCount}`);
  const firstHead = (await heads.first().innerText()).trim();
  check('the newest change first, as the changelog says it', firstHead === s0.entries.find((e) => e.headline).headline, firstHead);
  await shot(page, 'about-desktop-dark');
  if (plainCount > 6) {
    await sections.first().getByRole('button', { name: `Show all ${plainCount} changes` }).click();
    check('Show all lists every plain change', (await heads.count()) === plainCount, String(await heads.count()));
  }
  await heads.first().click();
  const body = sections.first().locator('details[open]').first();
  check('a change opens to say more', (await body.innerText()).length > firstHead.length + 20);
  await shot(page, 'about-desktop-dark-open');
  const tech = s0.entries.filter((e) => !e.headline).length;
  if (tech) {
    const label = await sections.first().locator('summary', { hasText: 'mostly for developers' }).innerText();
    check('technical changes wait behind one Details', label.includes(`${tech} more change`), label);
  }
  await heads.first().click(); // close it again for the plain-words read
  const text = await aboutText(page);
  const found = checkShown(text);
  check('About speaks plainly', found.length === 0, found.map((f) => `${f.kind} ${f.text}`).join('; '));
  // An older link to About lands on General (D184).
  await page.goto(`${base}/settings#about`);
  await page.locator('[data-about] [data-about-version]').waitFor({ timeout: 20_000 });
  check('an older link (/settings#about) opens Settings → General', new URL(page.url()).pathname === '/settings/general', page.url());
  await ctx.close();

  for (const [theme, mobile] of [['light', false], ['dark', true], ['light', true]]) {
    const label = `${mobile ? 'phone' : 'desktop'} ${theme}`;
    const p = await newPage(browser, { theme, mobile, state, label });
    await p.page.goto(`${base}/settings`);
    await p.page.locator('[data-about] [data-about-version]').waitFor({ timeout: 20_000 });
    await shot(p.page, `about-${label.replace(' ', '-')}`);
    if (mobile) {
      await p.page.locator('[data-about-computers]').scrollIntoViewIfNeeded();
      await shot(p.page, `about-${label.replace(' ', '-')}-computers`);
      await p.page.locator('[data-whats-new]').first().scrollIntoViewIfNeeded();
      await shot(p.page, `about-${label.replace(' ', '-')}-whats-new`);
    }
    await p.ctx.close();
  }

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
