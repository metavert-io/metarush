#!/usr/bin/env node
// End-to-end check of MetaRush's components in the dashboard (D324–D327), against a throwaway
// daemon set up with only the required components on a computer whose programs it can't see and
// whose Homebrew is a stand-in (scripts/components-e2e.sh does all of it):
//   A  Home's Getting started: what isn't ready says what adds it and offers Add it; More you can
//      add lists the other components not here, the suggested ones first
//   B  Settings → Components: what MetaRush needs to run (here), what can be added (Suggested,
//      sizes, what each unlocks), the command behind Details only
//   C  PDF pages added from Settings through a styled confirm with its size: it turns ready
//   D  the renderer's confirm says Remotion's licence (cancelled: it isn't added here)
//   E  FFmpeg added from Getting started's Add it: Editing turns ready
//   F  phone dark and light: Settings → Components and Getting started, no sideways scrolling
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5650 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   COMPONENTS_SHOTS=<dir> node web/scripts/components-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5650').replace(/\/$/, '');
if (!process.env.METARUSH_MINT) {
  console.error('set METARUSH_MINT and METARUSH_DASHBOARD (see the header of this script)');
  process.exit(2);
}
const SHOTS = process.env.COMPONENTS_SHOTS ?? '';
if (SHOTS) mkdirSync(SHOTS, { recursive: true });

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail && !ok ? ` - ${detail}` : detail ? ` (${String(detail).slice(0, 160)})` : ''}`);
}

function mintLink(next = '') {
  const out = execSync(process.env.METARUSH_MINT, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
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

async function openSettings(page) {
  await page.goto(`${base}/settings/components`);
  await page.locator('[data-components-page] [data-components-required]').waitFor({ timeout: 20_000 });
  await sleep(300);
}

async function openHome(page) {
  await page.goto(`${base}/?welcome=1`);
  await page.locator('[data-getting-started] [data-no-keys]').waitFor({ timeout: 20_000 });
  await sleep(300);
}

const browser = await chromium.launch();
try {
  const page = await newPage(browser, { theme: 'light', next: '/?welcome=1' });

  // A — Getting started.
  await openHome(page);
  const editing = page.locator('[data-works="editing"]');
  check('A Editing is not ready yet', (await editing.getAttribute('data-ready')) === 'no');
  check('A Editing offers Add it for FFmpeg', (await editing.locator('[data-add-component="ffmpeg"]').count()) === 1);
  const renders = page.locator('[data-works="renders"]');
  check('A Finished videos offer Add it for the renderer', (await renders.locator('[data-add-component="renderer"]').count()) === 1, await renders.innerText());
  const more = page.locator('[data-more-components]');
  check('A More you can add lists other components', (await more.count()) === 1 && (await more.locator('[data-more-component]').count()) > 0, (await more.innerText().catch(() => '')).slice(0, 200));
  check('A More you can add leaves out what is offered above', (await more.locator('[data-more-component="ffmpeg"]').count()) === 0);
  await plainIn(page, '[data-getting-started]', 'A Getting started');
  await shot(page, 'A-getting-started');

  // B — Settings → Components.
  await openSettings(page);
  const required = page.locator('[data-components-required] [data-component]');
  check('B what MetaRush needs to run: three, all here', (await required.count()) === 3 && (await page.locator('[data-components-required] [data-ready="no"]').count()) === 0);
  const missing = page.locator('[data-components-missing] [data-component]');
  const names = await missing.evaluateAll((els) => els.map((e) => e.getAttribute('data-component')));
  check('B what can be added: FFmpeg, the renderer, transcription, PDF pages', ['ffmpeg', 'renderer', 'transcription', 'pdf-pages'].every((n) => names.includes(n)), names.join(', '));
  check('B the suggested ones come first', names.indexOf('ffmpeg') < names.indexOf('other-languages') && names.indexOf('renderer') < names.indexOf('ai-models'), names.join(', '));
  const rendererRow = await page.locator('[data-component="renderer"]').innerText();
  check('B the renderer says what it unlocks, its size and Suggested', /finished videos/i.test(rendererRow) && /about 400 MB/.test(rendererRow) && /Suggested/.test(rendererRow), rendererRow);
  await plainIn(page, '[data-components-page]', 'B Settings → Components');
  await shot(page, 'B-settings-components');

  // C — PDF pages added from Settings.
  await page.locator('[data-add-component="pdf-pages"]').click();
  const confirm = page.getByRole('dialog');
  await confirm.waitFor({ timeout: 10_000 });
  const confirmText = await confirm.innerText();
  check('C the confirm says what it adds, its size and time', /PDF/.test(confirmText) && /about 30 MB/.test(confirmText) && /Homebrew installs it/.test(confirmText), confirmText);
  await shot(page, 'C-confirm-pdf-pages');
  await confirm.getByRole('button', { name: 'Add it' }).click();
  const pdfReady = await waitFor(async () => (await page.locator('[data-component="pdf-pages"][data-ready="yes"]').count()) === 1, 30_000);
  check('C PDF pages are added and shown as here', !!pdfReady);
  const pdfRow = await page.locator('[data-component="pdf-pages"]').innerText();
  check('C Homebrew’s copy has no Remove (MetaRush didn’t put it there)', (await page.locator('[data-remove-component="pdf-pages"]').count()) === 0, pdfRow);
  await shot(page, 'C-pdf-pages-added');

  // D — the renderer's confirm says Remotion's licence.
  await page.locator('[data-add-component="renderer"]').click();
  await confirm.waitFor({ timeout: 10_000 });
  const lic = await confirm.locator('[data-licence]').innerText().catch(() => '');
  check('D the renderer’s confirm says Remotion’s licence', /Remotion Company License/.test(lic) && /3 people/.test(lic), lic);
  await shot(page, 'D-confirm-renderer');
  await confirm.getByRole('button', { name: 'Cancel' }).click();
  await confirm.waitFor({ state: 'detached', timeout: 10_000 });
  const stillMissing = await api(page, 'components.list', { name: 'renderer' });
  check('D cancelled: the renderer isn’t added', !stillMissing.components[0].ready && stillMissing.components[0].status !== 'adding');

  // E — FFmpeg from Getting started.
  await openHome(page);
  await page.locator('[data-works="editing"] [data-add-component="ffmpeg"]').click();
  await confirm.waitFor({ timeout: 10_000 });
  await confirm.getByRole('button', { name: 'Add it' }).click();
  const editingReady = await waitFor(async () => {
    await page.reload();
    await page.locator('[data-getting-started] [data-no-keys]').waitFor({ timeout: 10_000 });
    return (await page.locator('[data-works="editing"]').getAttribute('data-ready')) === 'yes';
  }, 40_000, 1500);
  check('E FFmpeg added from Getting started: Editing works', !!editingReady);
  const st = await api(page, 'setup.status', { refresh: true });
  check('E Getting started no longer offers FFmpeg', !st.components.some((c) => c.name === 'ffmpeg'), st.components.map((c) => c.name).join(', '));
  await shot(page, 'E-getting-started-after');

  // F — phone, dark and light.
  for (const theme of ['dark', 'light']) {
    const p = await newPage(browser, { theme, phone: true });
    await openSettings(p);
    await noSideways(p, `F Settings → Components on a phone (${theme})`);
    await shot(p, `F-components-phone-${theme}`);
    await openHome(p);
    await noSideways(p, `F Getting started on a phone (${theme})`);
    await shot(p, `F-getting-started-phone-${theme}`);
    await p.context().close();
  }
} finally {
  await browser.close();
}

const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
process.exit(failed.length ? 1 : 0);
