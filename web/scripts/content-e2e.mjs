#!/usr/bin/env node
// Content Settings in a real browser (D192, D193). Run it through
// scripts/content-e2e.sh: a throwaway installation with three projects —
// "fernbrook" (the first, whose settings are the default, with a brand and a
// spelling), "Client" (with a cut) and "Third" — that use the default. It checks that:
//   1. /settings/brand (and Settings → Footage → Spelling) open the default's
//      Content Settings: fernbrook's Settings tab; Brand is no Settings section;
//   2. fernbrook's Settings tab: its own settings, "Use for new projects" ticked,
//      "Also used by 3 projects", Brand, Spelling, Narration voice, House rules,
//      House style and House sign-offs;
//   3. Client uses the default: read-only ("From fernbrook — edit them there…"), no
//      upload buttons or spelling form;
//   4. "Its own" asks Start empty / a copy of fernbrook's; a copy keeps fernbrook's
//      spelling; changing its brand offers "Use the new brand on the latest
//      cut", which saves a new version;
//   5. Third follows Client (through the picker), and Client says "Also used by 1 project";
//   6. "Use for new projects" on Client moves the default from fernbrook;
//   7. a house rule added in Client's settings reaches Third (read-only there);
//   8. logo files (D198): Client's logos for dark and light backgrounds are
//      uploaded right there, followers see them read-only, the change is
//      offered for the latest cut, and a title card shows the logo made for
//      its background — the one for dark backgrounds, then, on a light
//      background, the one for light backgrounds;
//   9. house style (D199): captions on and large, a half-height name bar that
//      never animates, an end card; offered for Client's latest cut (a new
//      version that follows it); Fourth (using the default) starts its first
//      cut with it; Third shows it read-only;
//  10. house sign-offs (D200): added in Client's settings, read-only for Third,
//      shown in Third's Sign-offs from Client's settings and ticked there
//      only (Client's stays open);
//  11. no native dialogs, page errors or horizontal overflow; a phone (390 px) fits.
// Screenshots: desktop dark and light, phone, in CONTENT_SHOTS. It CHANGES the
// projects: point it only at a throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'CONTENT_FERNBROOK', 'CONTENT_CLIENT', 'CONTENT_THIRD', 'CONTENT_FOURTH', 'CONTENT_ASSET', 'CONTENT_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/content-e2e.sh does)`);
    process.exit(2);
  }
}
const fernbrook = process.env.CONTENT_FERNBROOK;
const client = process.env.CONTENT_CLIENT;
const third = process.env.CONTENT_THIRD;
const fourth = process.env.CONTENT_FOURTH;
const asset = process.env.CONTENT_ASSET;
const shotsDir = process.env.CONTENT_SHOTS;
mkdirSync(shotsDir, { recursive: true });
const logoDir = path.join(shotsDir, '..', 'logo-files');
mkdirSync(logoDir, { recursive: true });

/** A small logo file: a rounded square and a bar, in two colours. */
function logoFile(name, fill, mark) {
  const p = path.join(logoDir, name);
  writeFileSync(
    p,
    `<svg xmlns="http://www.w3.org/2000/svg" width="240" height="120" viewBox="0 0 240 120"><rect x="6" y="6" width="108" height="108" rx="28" fill="${fill}"/><rect x="128" y="44" width="104" height="32" rx="6" fill="${mark}"/></svg>\n`,
  );
  return p;
}

/** The latest version of a project's cut. */
async function head(page, projectId) {
  const list = (await api(page, 'revision.list', { projectId })).revisions;
  return list.find((r) => r.isHead) ?? list[0];
}

/** Waits until the cut's latest version is newer than number (the save has moved the head). */
async function newHead(page, projectId, number) {
  return until(async () => {
    const h = await head(page, projectId);
    return h && h.number > number ? h : null;
  }, 30_000);
}

/** A checked change through the same ops the dashboard uses. */
async function applyOps(page, projectId, ops, instruction) {
  const base = await head(page, projectId);
  const plan = { baseRevisionId: base.id, instruction, ops };
  const chk = await api(page, 'edit.check', { plan });
  if (!chk.valid) throw new Error(`edit.check: ${JSON.stringify(chk.errors)}`);
  return api(page, 'edit.apply', { plan, confirmToken: chk.confirmToken });
}

/** The logo a graphic shows in the latest version's frame-by-frame plan. */
async function planLogo(page, projectId, id) {
  const plan = await api(page, 'revision.plan', { id: (await head(page, projectId)).id });
  const o = plan.overlays.find((x) => x.id === id);
  return { logo: o?.params?.logo, background: plan.brand?.background };
}

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

let shotN = 0;
async function shot(page, name, { full = false } = {}) {
  await page.waitForTimeout(450);
  await page.screenshot({ path: path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`), fullPage: full });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
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

async function until(fn, ms = 30_000, step = 250) {
  const end = Date.now() + ms;
  let v;
  while (Date.now() < end) {
    v = await fn();
    if (v) return v;
    await new Promise((r) => setTimeout(r, step));
  }
  return v;
}

const settings = (id) => `${base}/p/${id}?tab=settings`;

async function main() {
  const browser = await chromium.launch();
  const { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const state = await ctx.storageState();

  /* ---------- 1. Old addresses open the default's ---------- */
  await page.goto(`${base}/settings/brand`);
  await page.waitForURL((u) => u.pathname === `/p/${fernbrook}`, { timeout: 20_000 });
  check('/settings/brand opens the default’s Content Settings (fernbrook’s Settings tab)', /tab=settings/.test(page.url()) && /part=brand/.test(page.url()), page.url());
  await page.goto(`${base}/settings/footage#spelling`);
  await page.waitForURL((u) => u.pathname === `/p/${fernbrook}`, { timeout: 20_000 });
  check('Settings → Footage → Spelling opens it too, at Spelling', /part=spelling/.test(page.url()), page.url());
  await page.goto(`${base}/settings/general`);
  await page.locator('[data-settings]').waitFor({ timeout: 20_000 });
  check('Brand is no longer a Settings section', (await page.locator('a[href="/settings/brand"]').count()) === 0);

  /* ---------- 2. fernbrook: its own, the default ---------- */
  await page.goto(settings(fernbrook));
  const cs = page.locator('[data-content-settings]');
  await cs.waitFor({ timeout: 20_000 });
  await page.locator('[data-content-part="spelling"] [data-spelling-entry="FernBrook"]').waitFor({ timeout: 20_000 });
  check('fernbrook uses its own settings', (await cs.getAttribute('data-uses')) === 'own' && (await cs.getAttribute('data-read-only')) === null);
  check('“Use for new projects” is ticked on fernbrook', await page.locator('[data-use-for-new] input').isChecked());
  const used = (await page.locator('[data-also-used-by]').innerText().catch(() => '')).replace(/\s+/g, ' ');
  check('“Also used by 3 projects” (Client, Third and Fourth use the default)', /Also used by 3 projects/.test(used), used);
  for (const p of ['brand', 'spelling', 'voice', 'rules', 'style', 'signoffs']) check(`the ${p} part is there`, (await page.locator(`[data-content-part="${p}"]`).count()) === 1);
  check('the Settings tab also has This project', (await page.locator('[data-project-settings] [data-project-memory]').count()) === 1);
  const csText = await readText(page, '[data-content-settings]');
  const unplain = csText.split('\n').flatMap((l) => checkShown(l).map((f) => `${f.kind} ${JSON.stringify(f.text)}`));
  check('Content Settings read plainly', unplain.length === 0, unplain.join(' | '));
  await shot(page, 'fernbrook-settings-desktop-dark');

  /* ---------- 3. Client uses the default: read-only ---------- */
  await page.goto(settings(client));
  await cs.waitFor({ timeout: 20_000 });
  await page.locator('[data-content-from]').waitFor({ timeout: 20_000 });
  const from = (await page.locator('[data-content-from]').innerText()).replace(/\s+/g, ' ');
  check('a follower shows “From fernbrook — edit them there, or give this project its own”', /From .?fernbrook.? — edit them there, or give this project its own/.test(from), from);
  check('a follower’s brand has no upload buttons', (await page.locator('[data-brand-page] button', { hasText: /^Upload/ }).count()) === 0);
  check('a follower’s spelling list has no form', (await page.locator('[data-content-part="spelling"] [data-spelling-word]').count()) === 0 && (await page.locator('[data-content-part="spelling"] [data-spelling-entry="FernBrook"]').count()) === 1);
  await shot(page, 'client-follows-default-desktop-dark');

  /* ---------- 4. Its own, a copy; a new brand offered for the latest cut ---------- */
  await page.locator('[data-uses-option="own"]').click();
  await page.locator('[data-own="copy"]').click();
  await until(async () => (await cs.getAttribute('data-uses')) === 'own', 20_000);
  check('Client has its own settings now', (await cs.getAttribute('data-uses')) === 'own' && (await page.locator('[data-content-from]').count()) === 0);
  check('…a copy of fernbrook’s (its spelling came along)', (await page.locator('[data-content-part="spelling"] [data-spelling-entry="FernBrook"]').count()) === 1);
  const before = (await api(page, 'revision.list', { projectId: client })).revisions.length;
  await page.locator('[data-brand-page] button', { hasText: /^Fraunces$/ }).click();
  const offer = page.locator(`[data-brand-offer="${client}"]`);
  await offer.waitFor({ timeout: 20_000 });
  check('a brand change offers “Use the new brand on the latest cut”', /Use the new brand on the latest cut/.test(await page.locator('[data-brand-offers]').innerText()));
  await shot(page, 'client-brand-offer-desktop-dark');
  await offer.click();
  const after = await until(async () => {
    const n = (await api(page, 'revision.list', { projectId: client })).revisions.length;
    return n > before ? n : 0;
  }, 30_000);
  check('the offer saved a new version of the latest cut', after === before + 1, `${before} → ${after}`);
  const kit = await api(page, 'brand.show', { projectId: client });
  check('Client’s brand is its own (fernbrook’s is unchanged)', kit.kit.font === 'Fraunces' && (await api(page, 'brand.show', { projectId: fernbrook })).kit.font !== 'Fraunces');

  /* ---------- 5. Third follows Client ---------- */
  await page.goto(settings(third));
  await cs.waitFor({ timeout: 20_000 });
  await page.locator('[data-follow-select]').selectOption(client);
  await until(async () => (await cs.getAttribute('data-uses')) === 'project', 20_000);
  const thirdFrom = (await page.locator('[data-content-from]').innerText().catch(() => '')).replace(/\s+/g, ' ');
  check('Third follows Client', /From .?Client/.test(thirdFrom), thirdFrom);
  await page.goto(settings(client));
  await cs.waitFor({ timeout: 20_000 });
  const used2 = (await page.locator('[data-also-used-by]').innerText().catch(() => '')).replace(/\s+/g, ' ');
  check('Client: “Also used by 1 project” (Third)', /Also used by 1 project/.test(used2) && /Third/.test(used2), used2);

  /* ---------- 6. Use for new projects moves the default ---------- */
  await page.locator('[data-use-for-new] input').check();
  await until(async () => (await api(page, 'content.show', { projectId: client })).isDefault, 20_000);
  const fernbrookNow = await api(page, 'content.show', { projectId: fernbrook });
  check('“Use for new projects” on Client moves the default from fernbrook', !fernbrookNow.isDefault && fernbrookNow.default?.id === client, JSON.stringify(fernbrookNow.default));

  /* ---------- 7. A house rule reaches the follower ---------- */
  const rules = page.locator('[data-content-part="rules"]');
  await rules.locator('[data-house-rule-add] input[aria-label]').fill('The name bar never animates');
  await rules.locator('[data-house-rule-add]').getByRole('button', { name: 'Add' }).click();
  await rules.locator('[data-house-rule="The name bar never animates"]').waitFor({ timeout: 20_000 });
  await page.goto(settings(third));
  await page.locator('[data-content-part="rules"] [data-house-rule="The name bar never animates"]').waitFor({ timeout: 20_000 });
  check('Third reads Client’s house rule, read-only', (await page.locator('[data-house-rule-add]').count()) === 0);
  await shot(page, 'third-follows-client-desktop-dark', { full: true });

  /* ---------- 8. Logo files (D198) ---------- */
  await page.goto(settings(client));
  await cs.waitFor({ timeout: 20_000 });
  const brandPart = page.locator('[data-content-part="brand"]');
  await brandPart.locator('[data-brand-logos]').waitFor({ timeout: 20_000 });
  const whichLogos = await brandPart.locator('[data-brand-logo]').evaluateAll((els) => els.map((e) => e.getAttribute('data-brand-logo')));
  check('the brand shows its five named logos', whichLogos.join(',') === 'main,dark,light,icon,wordmark', whichLogos.join(','));
  check('the main logo is the one the brand had', (await brandPart.locator('[data-brand-logo="main"][data-has-logo="yes"]').count()) === 1);
  const before8 = (await head(page, client)).number;
  for (const [which, file] of [
    ['dark', logoFile('client-white.svg', '#F7F5F0', '#E0A458')],
    ['light', logoFile('client-ink.svg', '#14161C', '#2E7D50')],
  ]) {
    const [chooser] = await Promise.all([page.waitForEvent('filechooser'), brandPart.locator(`[data-logo-upload="${which}"]`).click()]);
    await chooser.setFiles(file);
    await brandPart.locator(`[data-brand-logo="${which}"][data-has-logo="yes"]`).waitFor({ timeout: 45_000 });
  }
  const shown8 = await api(page, 'brand.show', { projectId: client });
  const named = Object.fromEntries(shown8.logos.map((l) => [l.which, l.resource?.title ?? '']));
  check('uploaded right there: the logos for dark and light backgrounds', /client-white/.test(named.dark) && /client-ink/.test(named.light), JSON.stringify(named));
  await brandPart.locator('[data-brand-logos]').scrollIntoViewIfNeeded();
  await shot(page, 'client-logos-desktop-dark');
  const logoOffer = page.locator(`[data-brand-offer="${client}"]`);
  await logoOffer.waitFor({ timeout: 20_000 });
  check('a logo change offers “Use the new brand on the latest cut”', (await logoOffer.count()) === 1);
  await logoOffer.click();
  const head8 = await newHead(page, client, before8);
  const spec8 = (await api(page, 'revision.show', { id: head8.id })).spec;
  check(
    'the latest cut pins the logos and names them in its brand',
    !!spec8.resources?.['logo-dark'] && !!spec8.resources?.['logo-light'] && spec8.brand?.logo === 'logo' && spec8.brand?.logoDark === 'logo-dark' && spec8.brand?.logoLight === 'logo-light',
    JSON.stringify({ pins: Object.keys(spec8.resources ?? {}), brand: spec8.brand }),
  );
  await applyOps(page, client, [{ op: 'addOverlay', overlay: { id: 'title', template: 'title.card', at: 0, duration: 1.4, params: { title: 'Client intro', logo: 'logo' } } }], 'A title card with the logo');
  const dark = await planLogo(page, client, 'title');
  check('a title card on a dark background shows the logo for dark backgrounds', dark.logo === 'logo-dark', JSON.stringify(dark));
  const beforeLight = (await head(page, client)).number;
  await api(page, 'brand.set', { projectId: client, background: '#F7F5F0', text: '#141414' });
  await api(page, 'brand.apply', { projectId: client, instruction: 'Use the brand kit' });
  await newHead(page, client, beforeLight);
  const light = await planLogo(page, client, 'title');
  check('on a light background, the logo for light backgrounds', light.logo === 'logo-light', JSON.stringify(light));
  await page.goto(settings(third));
  await cs.waitFor({ timeout: 20_000 });
  await page.locator('[data-content-part="brand"] [data-brand-logo="dark"][data-has-logo="yes"]').waitFor({ timeout: 20_000 });
  check('a follower sees the logos read-only', (await page.locator('[data-content-part="brand"] [data-logo-upload]').count()) === 0);

  /* ---------- 9. House style (D199) ---------- */
  await page.goto(settings(client));
  await cs.waitFor({ timeout: 20_000 });
  const stylePart = page.locator('[data-content-part="style"]');
  await stylePart.locator('[data-house-style]').waitFor({ timeout: 20_000 });
  const lines = stylePart.locator('[data-style-lines]');
  const styled = async (re) => until(async () => re.test(await lines.innerText().catch(() => '')), 20_000);
  await stylePart.locator('[data-style-field="captions"]').selectOption('on');
  await styled(/Captions: on/);
  const styleOffer = page.locator(`[data-style-offer="${client}"]`);
  await styleOffer.waitFor({ timeout: 20_000 });
  check('a style change offers “Use the house style on the latest cut”', /Use the house style on the latest cut/.test(await page.locator('[data-style-offers]').innerText()));
  await stylePart.locator('[data-style-field="captionSize"]').selectOption('large');
  await styled(/large/);
  await stylePart.locator('[data-style-field="nameBarHeight"]').selectOption('half');
  await styled(/half as tall/);
  await stylePart.locator('[data-style-field="nameBarAnimate"]').selectOption('never');
  await styled(/never animates/);
  await stylePart.locator('[data-style-field="endCard"]').selectOption('endcard.cta');
  await stylePart.locator('[data-style-card="end"] [data-style-card-title]').fill('Volunteer with us');
  await stylePart.locator('[data-style-card-save="end"]').click();
  await styled(/Volunteer with us/);
  const words = await lines.innerText();
  check('the house style in plain words', /Captions: on, large/.test(words) && /about half as tall/.test(words) && /never animates/.test(words) && /End card/.test(words), words.replace(/\s+/g, ' '));
  await stylePart.scrollIntoViewIfNeeded();
  await shot(page, 'client-house-style-desktop-dark');
  const before9 = (await head(page, client)).number;
  await page.locator(`[data-style-offer="${client}"]`).click();
  const head9 = await newHead(page, client, before9);
  const spec9 = (await api(page, 'revision.show', { id: head9.id })).spec;
  check(
    'the offer saved a version that follows the house style',
    spec9.captions?.enabled === true && spec9.captions?.size === 'large' && (spec9.overlays ?? []).some((o) => o.template === 'endcard.cta'),
    JSON.stringify({ captions: spec9.captions, overlays: (spec9.overlays ?? []).map((o) => o.template) }),
  );
  // A new cut of a project that uses the default (Client's settings now) starts with it.
  await api(page, 'asset.link', { assetId: asset, projectId: fourth });
  const plan4 = {
    projectId: fourth,
    instruction: 'A first cut',
    spec: { schemaVersion: 1, title: 'Fourth', output: { preset: 'landscape-1080p', fps: 30 }, clips: [{ id: 'talk', assetId: asset, in: 0, out: 3.5 }],
      overlays: [{ id: 'names', template: 'name.bar', span: {}, at: 0, duration: 0, params: { name: 'Ada' } }] },
  };
  const chk4 = await api(page, 'edit.check', { plan: plan4 });
  const new4 = await api(page, 'edit.apply', { plan: plan4, confirmToken: chk4.confirmToken });
  const spec4 = (await api(page, 'revision.show', { id: new4.revision.id })).spec;
  const bar4 = (spec4.overlays ?? []).find((o) => o.id === 'names');
  check(
    'a new cut of a project using the settings starts with the house style',
    spec4.captions?.enabled === true && spec4.captions?.size === 'large' && bar4?.params?.height === 'half' && bar4?.params?.animate === 'never',
    JSON.stringify({ captions: spec4.captions, bar: bar4?.params }),
  );
  await page.goto(settings(third));
  await cs.waitFor({ timeout: 20_000 });
  await page.locator('[data-content-part="style"] [data-style-lines]').waitFor({ timeout: 20_000 });
  check('a follower reads the house style, read-only', (await page.locator('[data-content-part="style"] [data-style-field]').count()) === 0);

  /* ---------- 10. House sign-offs (D200) ---------- */
  await page.goto(settings(client));
  await cs.waitFor({ timeout: 20_000 });
  const soPart = page.locator('[data-content-part="signoffs"]');
  await soPart.locator('[data-house-signoff-add] input').fill('Legal OKs it before export');
  await soPart.locator('[data-house-signoff-add]').getByRole('button', { name: 'Add' }).click();
  await soPart.locator('[data-house-signoff="Legal OKs it before export"]').waitFor({ timeout: 20_000 });
  await soPart.scrollIntoViewIfNeeded();
  await shot(page, 'client-house-signoffs-desktop-dark');
  await page.goto(settings(third));
  await cs.waitFor({ timeout: 20_000 });
  await page.locator('[data-content-part="signoffs"] [data-house-signoff="Legal OKs it before export"]').waitFor({ timeout: 20_000 });
  check('a follower reads the house sign-offs, read-only', (await page.locator('[data-house-signoff-add]').count()) === 0);
  const houseRow = page.locator('[data-project-memory] [data-signoff-house]');
  await houseRow.first().waitFor({ timeout: 20_000 });
  const rowText = (await houseRow.first().innerText()).replace(/\s+/g, ' ');
  check('Third’s Sign-offs show it, from Client’s settings', /Legal OKs it before export/.test(rowText) && /from Client/.test(rowText), rowText);
  await houseRow.first().locator('input[type="checkbox"]').check();
  await until(async () => (await api(page, 'cut.signoffs.list', { projectId: third })).open === 0, 20_000);
  const clientOpen = (await api(page, 'cut.signoffs.list', { projectId: client })).open;
  check('ticked in Third only: Client still waits for it', clientOpen === 1, `Client open: ${clientOpen}`);
  await page.locator('[data-project-memory] [data-signoff-house]').first().scrollIntoViewIfNeeded();
  await shot(page, 'third-house-signoff-ticked-desktop-dark');
  await ctx.close();

  /* ---------- 11. Light, and a phone ---------- */
  for (const [theme, mobile, tag] of [
    ['light', false, 'desktop-light'],
    ['dark', true, 'phone-dark'],
    ['light', true, 'phone-light'],
  ]) {
    const v = await newPage(browser, { theme, mobile, state });
    for (const [id, name] of [
      [client, 'client'],
      [third, 'third'],
    ]) {
      await v.page.goto(settings(id));
      await v.page.locator('[data-content-settings]').waitFor({ timeout: 20_000 });
      await v.page.waitForTimeout(600);
      await shot(v.page, `${name}-settings-${tag}`);
      if (mobile) {
        const box = await v.page.locator('[data-content-settings]').boundingBox();
        check(`16px gutters on a phone (${name}, ${theme})`, !!box && box.x >= 15.5 && box.x + box.width <= 390 - 15.5, box ? `left ${box.x}, right ${390 - box.x - box.width}` : 'not found');
        await v.page.locator('[data-content-part="spelling"]').scrollIntoViewIfNeeded();
        await shot(v.page, `${name}-settings-spelling-${tag}`);
      }
      for (const part of ['brand', 'style', 'signoffs']) {
        const el = v.page.locator(part === 'brand' ? '[data-brand-logos]' : `[data-content-part="${part}"]`);
        await el.scrollIntoViewIfNeeded();
        await shot(v.page, `${name}-settings-${part === 'brand' ? 'logos' : part}-${tag}`);
      }
    }
    await v.ctx.close();
  }
  await browser.close();

  check('no native dialogs, page errors or horizontal overflow', problems.length === 0, problems.join(' | '));
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed; screenshots in ${shotsDir}`);
  process.exit(failed.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
