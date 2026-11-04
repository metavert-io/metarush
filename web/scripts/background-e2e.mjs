#!/usr/bin/env node
// Replacing a clip's background in a real browser (D505–D514). Run it through
// scripts/background-e2e.sh: a throwaway installation where "Studio" has a first
// cut of one clip (a bright figure over a dark blue room) and a stand-in person
// finder. It checks that:
//   1. the Edit tab offers Replace the background… on the clip;
//   2. the dialog shows a still of the clip with the colour chosen behind the
//      figure, made on this computer;
//   3. replacing it starts the clip's cut-out, shows its progress, then saves
//      the change as a new version (the clip's background: the colour, its
//      cut-out pinned) and says so;
//   4. the preview draws the colour behind the cut-out, played from the
//      clip's own moment;
//   5. Take the background out saves a version without it, and Undo puts it
//      back;
//   6. no native dialogs, page errors or horizontal overflow; the light
//      theme and a phone (390 px) fit.
// Screenshots in BACKGROUND_SHOTS. It CHANGES the project: point it only at a
// throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'BACKGROUND_PROJECT', 'BACKGROUND_CLIP', 'BACKGROUND_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/background-e2e.sh does)`);
    process.exit(2);
  }
}
const project = process.env.BACKGROUND_PROJECT;
const shotsDir = process.env.BACKGROUND_SHOTS;
mkdirSync(shotsDir, { recursive: true });

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
async function shot(page, name) {
  await page.waitForTimeout(500);
  await page.screenshot({ path: path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`) });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
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

/** The background of the latest version's clip. */
async function headBackground(page) {
  const list = (await api(page, 'revision.list', { projectId: project })).revisions;
  const h = list.find((r) => r.isHead) ?? list[0];
  const r = await api(page, 'revision.show', { id: h.id });
  const spec = r.revision?.spec ?? r.spec;
  return { number: h.number, background: spec.clips[0]?.background };
}

async function openEdit(page) {
  await page.goto(`${base}/p/${project}?tab=cut`);
  const button = page.locator('[data-replace-background]');
  await button.waitFor({ timeout: 60_000 });
  // The Claude panel stays open across pages once opened: close it.
  const close = page.getByRole('button', { name: 'Close (Esc)' });
  if (await close.count()) await close.first().click();
  return button;
}

/** What the preview draws: the colour behind the people and the cut-out's video. */
async function previewDraws(page) {
  return page.evaluate(() => {
    const els = [...document.querySelectorAll('div')];
    const colour = els.some((e) => getComputedStyle(e).backgroundColor === 'rgb(0, 177, 64)' && e.getBoundingClientRect().width > 100);
    const cutOut = [...document.querySelectorAll('video')].some((v) => (v.currentSrc || v.src || '').includes('/media/cutouts/'));
    return { colour, cutOut };
  });
}

async function main() {
  const browser = await chromium.launch();
  const { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 30_000 });
  const state = await ctx.storageState();

  // 1. The Edit tab offers it.
  const button = await openEdit(page);
  check('the Edit tab offers Replace the background… on the clip', (await button.innerText()).includes('Replace the background'));
  await shot(page, 'edit-desktop-dark');

  // 2. The dialog and its still.
  await button.click();
  const dlg = page.locator('[data-background-dialog]');
  await dlg.waitFor({ timeout: 10_000 });
  await dlg.getByRole('radio', { name: 'Colour' }).click();
  await dlg.locator('input[type="color"]').fill('#00b140');
  const still = await until(async () => (await dlg.locator('img[data-background-still]').getAttribute('data-background-still')) === 'shown', 90_000, 500);
  check('the dialog shows a still of the clip with the colour behind the figure', !!still);
  await shot(page, 'dialog-colour');

  // 3. The cut-out is made, then the change saved.
  await dlg.getByRole('button', { name: 'Replace the background' }).click();
  const progress = await until(async () => (await dlg.locator('[data-cutout-progress]').count()) > 0 || (await dlg.count()) === 0, 30_000, 200);
  check('replacing it starts the clip\'s cut-out (its progress shows)', !!progress);
  if ((await dlg.count()) > 0 && (await dlg.locator('[data-cutout-progress]').count()) > 0) await shot(page, 'dialog-making');
  await dlg.waitFor({ state: 'detached', timeout: 240_000 });
  const toast = await until(async () => (await page.getByText('Background replaced').count()) > 0, 10_000);
  const saved = await until(async () => {
    const h = await headBackground(page);
    return h.background?.with === 'colour' && h.background?.colour === '#00b140' && /^[0-9a-f]{64}$/.test(h.background?.matte ?? '') ? h : null;
  }, 20_000);
  check('the change is saved as a new version with the cut-out pinned, and the dashboard says so', !!saved && !!toast, saved ? `version ${saved.number}` : '');

  // 4. The preview draws it.
  const draws = await until(async () => {
    const d = await previewDraws(page);
    return d.colour && d.cutOut ? d : null;
  }, 30_000, 500);
  check('the preview draws the colour behind the cut-out', !!draws);
  check('the clip says what is behind its people', (await page.locator('[data-replace-background]').innerText()).includes('the colour #00b140'));
  await shot(page, 'replaced-desktop-dark');

  // 5. Out, then Undo.
  await page.locator('[data-background-off]').click();
  const out = await until(async () => ((await headBackground(page)).background ? null : true), 20_000);
  check('Take the background out saves a version without it', !!out);
  const undo = page.getByRole('button', { name: 'Undo' });
  await undo.first().waitFor({ timeout: 10_000 });
  await undo.first().click();
  const back = await until(async () => ((await headBackground(page)).background?.colour === '#00b140' ? true : null), 20_000);
  check('Undo puts it back', !!back);
  check('no native dialogs, page errors or horizontal overflow (desktop)', problems.length === 0, problems.join(' | '));
  await ctx.close();

  // 6. Light and phone.
  for (const [theme, mobile] of [['light', false], ['dark', true]]) {
    const tag = `${mobile ? 'phone' : 'desktop'}-${theme}`;
    const v = await newPage(browser, { theme, mobile, state });
    const b = await openEdit(v.page);
    await b.scrollIntoViewIfNeeded();
    await b.click();
    await v.page.locator('[data-background-dialog]').waitFor({ timeout: 10_000 });
    await shot(v.page, `dialog-${tag}`);
    await v.ctx.close();
  }
  await browser.close();
  check('no native dialogs, page errors or horizontal overflow', problems.length === 0, problems.join(' | '));
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed; screenshots in ${shotsDir}`);
  if (failed.length) process.exitCode = 1;
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
