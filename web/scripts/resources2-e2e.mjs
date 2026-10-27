#!/usr/bin/env node
// End-to-end check of resources part 2 in the dashboard and the browser
// extension (D104–D108):
//   A  pasting a GitHub repository in a project's Add box brings it in at its
//      commit: a tile, and an overview (commit, files, languages, README)
//   B  pasting a web page brings in a dated snapshot: the pictures on a
//      computer, on a phone and of the whole page
//   C  slides: a cut that uses the slide templates previews them in the
//      studio (their words and pictures in the player), and the Script view
//      spells them
//   D  the browser extension's "Send this page": a stub page goes to the
//      workspace library as the person sees it (a picture of the tab and its
//      text)
//   and screenshots (desktop and phone, dark and light) of the Resources tab
//   with a repository and a page snapshot, their details, and the slides.
//
// Seed (see docs/resources.md, "Trying it"): a project (SLIDES_PROJECT,
// default "Widget tour") with footage and a cut that uses slide.title,
// slide.code, slide.architecture and slide.screenshot; a daemon that fetches
// the fixture repository and may take snapshots of this computer:
//   scripts/make-fixture-repo.sh <dir>
//   METARUSH_GIT_BASE=<dir> METARUSH_PAGES_ALLOW_LOCAL=1 bin/metarush --home <test home> serve
//   (tests/fixtures/site, with a preview.png and an icon.png, served at SITE_URL)
//   METARUSH_DASHBOARD=http://127.0.0.1:<port> METARUSH_MINT="bin/metarush --home <test home> dashboard --print-link" \
//   SITE_URL=http://127.0.0.1:<port> SHOTS_DIR=<dir> node web/scripts/resources2-e2e.mjs
import { execSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:4845').replace(/\/$/, '');
const SITE = (process.env.SITE_URL ?? '').replace(/\/$/, '');
const SLIDES_PROJECT = process.env.SLIDES_PROJECT ?? 'Widget tour';
if (!process.env.METARUSH_MINT || !SITE) {
  console.error('set METARUSH_MINT, METARUSH_DASHBOARD and SITE_URL (see the header of this script)');
  process.exit(2);
}
const SHOTS = process.env.SHOTS_DIR ?? '';
if (SHOTS) mkdirSync(SHOTS, { recursive: true });

const results = [];
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

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function shot(page, name) {
  await sleep(400); // let dialogs, toasts and pictures finish appearing
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

async function newPage(browser, problems, { theme = 'dark', phone = false } = {}) {
  const ctx = await browser.newContext({
    viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 900 },
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
  page.on('pageerror', (e) => problems.push(`${theme}${phone ? ' phone' : ''}: ${e.message}`));
  await page.goto(mintLink());
  await page.waitForURL(`${base}/**`);
  return page;
}

const projectId = async (page, name) => (await api(page, 'project.list', {})).projects.find((p) => p.name === name)?.id;

/** Pastes a link into the Add box's link field and presses Enter. */
async function pasteLink(page, url) {
  const link = page.locator('[data-addbox] form[aria-label="Import from a link"] input').first();
  await link.fill(url);
  await link.press('Enter');
}

/** Opens a resource's details from the grid by its title. */
async function openResource(page, title) {
  await page.locator('ul[aria-label="Resources"] button').filter({ hasText: title }).first().click();
  const dlg = page.getByRole('dialog').last();
  await dlg.waitFor({ timeout: 10000 });
  return dlg;
}

/**
 * Moves the studio's playhead to a time by clicking the ruler, measured on the
 * picture lane (the first clip starts at 0, the last one ends at total).
 */
async function seek(page, seconds, total) {
  const ruler = await page.locator('[data-ruler]').first().boundingBox();
  const clips = page.locator('[data-timeline-editor] [data-clip]');
  const first = await clips.first().boundingBox();
  const last = await clips.last().boundingBox();
  const x0 = first.x;
  const x1 = last.x + last.width;
  await page.mouse.click(x0 + ((x1 - x0) * seconds) / total, ruler.y + ruler.height / 2);
}

const browser = await chromium.launch();
const problems = [];
let launchId = '';
try {
  const page = await newPage(browser, problems);

  /* ---------- A: a repository pasted in the Add box ---------- */
  const launch = await api(page, 'project.create', { name: `Launch video ${Date.now().toString(36).slice(-4)}` });
  launchId = launch.project.id;
  await page.goto(`${base}/p/${launchId}?tab=resources`);
  await page.locator('[data-addbox]').first().waitFor();
  await pasteLink(page, 'https://github.com/acme/widget');
  const toast = page.getByRole('status').filter({ hasText: /Getting acme\/widget/ }).first();
  check('A: pasting a repository says MetaRush is getting it', await toast.waitFor({ timeout: 15000 }).then(() => true, () => false));
  const repoRow = await waitFor(async () => (await api(page, 'resources.list', { projectId: launchId })).resources.find((r) => r.kind === 'repo'), 120000, 1000);
  check('A: the repository arrives in the project, pinned at a commit', !!repoRow && repoRow.repo?.commit?.length === 40 && repoRow.origin === 'project',
    repoRow ? `${repoRow.name} @ ${repoRow.repo?.commit?.slice(0, 8)}, ${repoRow.repo?.files} files, origin ${repoRow.origin}` : 'not there');
  await page.reload();
  const tile = page.locator('[data-repo-tile]').first();
  check('A: the Resources tab shows the repository as a tile', await tile.waitFor({ timeout: 15000 }).then(() => true, () => false));
  if (repoRow) {
    const dlg = await openResource(page, repoRow.title);
    // The overview loads with the details (resources.show).
    const text =
      (await waitFor(async () => {
        const t = (await dlg.textContent()) ?? '';
        return /README/.test(t) ? t : null;
      }, 15000, 300)) ?? ((await dlg.textContent()) ?? '');
    check('A: its overview shows the commit, the files, the languages and the README',
      text.includes(repoRow.repo.commit.slice(0, 8)) && /files/.test(text) && /Go/.test(text) && /README/.test(text), text.replace(/\s+/g, ' ').slice(0, 160));
    await shot(page, 'A-repo-details-desktop-dark');
    await page.keyboard.press('Escape');
  }

  /* ---------- B: a web page pasted in the Add box ---------- */
  await pasteLink(page, `${SITE}/pricing.html`);
  const pageRow = await waitFor(async () => (await api(page, 'resources.list', { projectId: launchId })).resources.find((r) => r.kind === 'website' && r.origin === 'project' && r.site?.url?.startsWith(`${SITE}/pricing`)), 180000, 1500);
  check('B: the page arrives as a dated snapshot', !!pageRow && !!pageRow.site?.takenAt, pageRow ? `${pageRow.name}: ${pageRow.title} (${pageRow.site?.takenAt?.slice(0, 10)})` : 'not there');
  if (pageRow) {
    await page.reload();
    const dlg = await openResource(page, pageRow.title);
    const which = dlg.getByRole('group', { name: 'Which picture' });
    await which.waitFor({ timeout: 15000 }).catch(() => undefined);
    const buttons = await which.getByRole('button').count();
    check('B: its details offer the computer, phone and whole-page pictures', buttons === 3, `${buttons} pictures`);
    await which.getByRole('button').nth(1).click();
    const phonePic = await waitFor(
      () => dlg.locator('[data-snapshot-picture="mobile"] img').first().evaluate((img) => (img.complete && img.naturalWidth > 0 ? `${img.naturalWidth}x${img.naturalHeight}` : null)),
      15000,
      300,
    );
    check('B: the phone picture shows', !!phonePic, phonePic ?? 'not loaded');
    await shot(page, 'B-page-details-desktop-dark');
    await page.keyboard.press('Escape');
  }
  await shot(page, 'AB-resources-desktop-dark');

  /* ---------- C: slides in the studio ---------- */
  const sp = await projectId(page, SLIDES_PROJECT);
  check(`C: the seeded project "${SLIDES_PROJECT}" is there`, !!sp);
  let slides = [];
  let total = 0;
  if (sp) {
    const head = (await api(page, 'revision.list', { projectId: sp })).revisions[0];
    const rev = await api(page, 'revision.show', { id: head.id });
    total = rev.spec.clips.reduce((s, c) => s + (c.out - c.in), 0);
    slides = rev.spec.overlays.filter((o) => o.template?.startsWith('slide.'));
    check('C: its cut uses slides', slides.length >= 3, slides.map((o) => o.template).join(', '));
    await page.goto(`${base}/p/${sp}?tab=cut`);
    await page.locator('[data-player-surface]').first().waitFor({ timeout: 20000 });
    await page.locator('[data-ruler]').first().waitFor({ timeout: 20000 });
    await sleep(1500);
    for (const o of slides) {
      const params = o.params ?? {};
      await seek(page, o.at + Math.min(o.duration * 0.6, o.duration - 0.4), total);
      const words = params.title ?? params.text ?? '';
      const drawn = await waitFor(async () => {
        const surface = page.locator('[data-player-surface]').first();
        const text = (await surface.textContent()) ?? '';
        const pics = await surface.locator('img').evaluateAll((imgs) => imgs.filter((i) => i.complete && i.naturalWidth > 0).length);
        return (!words || text.includes(words)) && (o.template === 'slide.title' || o.template === 'slide.bullets' || pics > 0) ? { text, pics } : null;
      }, 15000, 400);
      check(`C: the preview draws ${o.template}`, !!drawn, drawn ? `“${words}”, ${drawn.pics} picture(s)` : `looked for “${words}”`);
      await shot(page, `C-${o.template.replace('.', '-')}-desktop-dark`);
    }
    await page.goto(`${base}/p/${sp}?tab=script`);
    const script = page.locator('[data-script-view]').first();
    await script.waitFor({ timeout: 15000 });
    await sleep(800);
    const sheet = (await script.textContent()) ?? '';
    const at = sheet.indexOf('Graphics');
    check('C: the Script view names the slides and pins their pictures', sheet.includes('Code slide') && sheet.includes('Screenshot slide') && sheet.includes('main-loop'),
      sheet.slice(Math.max(0, at), at + 160).replace(/\s+/g, ' '));
    await shot(page, 'C-script-desktop-dark');
  }

  /* ---------- D: the extension's "Send this page" ---------- */
  const { folder } = await api(page, 'extension.status');
  // A test copy that may capture a tab without the toolbar click (Playwright
  // can't press the browser's toolbar button, which is what grants activeTab).
  const extDir = mkdtempSync(path.join(tmpdir(), 'mr-ext-pages-'));
  cpSync(folder, extDir, { recursive: true });
  const manifest = JSON.parse(readFileSync(path.join(extDir, 'manifest.json'), 'utf8'));
  manifest.host_permissions = [...manifest.host_permissions, '<all_urls>'];
  writeFileSync(path.join(extDir, 'manifest.json'), JSON.stringify(manifest, null, 2));
  const userDir = mkdtempSync(path.join(tmpdir(), 'mr-ext-profile-'));
  const ctx = await chromium.launchPersistentContext(userDir, { headless: true, channel: 'chromium', args: [`--disable-extensions-except=${extDir}`, `--load-extension=${extDir}`] });
  try {
    let [sw] = ctx.serviceWorkers();
    if (!sw) sw = await ctx.waitForEvent('serviceworker', { timeout: 15000 });
    const extId = new URL(sw.url()).host;
    const dash = await ctx.newPage();
    await dash.goto(mintLink());
    await dash.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15000 });
    const code = (await api(dash, 'extension.pair')).code;
    const popup = await ctx.newPage();
    popup.on('pageerror', (e) => problems.push(`popup: ${e.message}`));
    await popup.goto(`chrome-extension://${extId}/popup.html`);
    await popup.getByLabel('Pairing code').waitFor({ timeout: 10000 });
    await popup.locator('details summary').click();
    await popup.getByLabel('Address').fill(base);
    await popup.getByLabel('Pairing code').fill(code);
    await popup.getByRole('button', { name: 'Pair' }).click();
    check('D: the extension pairs', await popup.getByText(/Paired with MetaRush on/).waitFor({ timeout: 15000 }).then(() => true, () => false));
    // A signed-in-looking stub page, in its own window so it is that window's visible tab.
    await ctx.route('https://acme.example/**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'text/html',
        body: `<!doctype html><html lang="en"><head><title>Acme pricing</title><meta name="description" content="Plans for every team">
          <meta name="theme-color" content="#2563EB"></head><body style="margin:0;font:18px/1.5 system-ui,sans-serif;background:#fff;color:#0f172a">
          <header style="background:#2563EB;color:#fff;padding:20px 48px;font-size:26px;font-weight:700">Acme <span style="float:right;font-size:16px">Signed in as Ada</span></header>
          <main style="padding:48px"><h1>Pricing</h1><p>Start free. Teams from $12 a month.</p></main></body></html>`,
      }),
    );
    const stub = await ctx.newPage();
    const stubURL = `https://acme.example/pricing/${Date.now().toString(36)}`; // a page this check hasn't sent before
    await stub.goto(stubURL);
    await stub.getByRole('heading', { name: 'Pricing' }).waitFor({ timeout: 15000 });
    // Its tab, brought to the front of its window (what captureVisibleTab takes).
    const tabId = await popup.evaluate(async () => {
      const [t] = await chrome.tabs.query({ url: 'https://acme.example/*' });
      await chrome.tabs.update(t.id, { active: true });
      return t.id;
    });
    // The popup sends "the tab it was opened on"; opened as a page here, that is the stub's tab.
    await popup.evaluate((id) => {
      const query = chrome.tabs.query.bind(chrome.tabs);
      chrome.tabs.query = async (info) => (info && info.active ? [await chrome.tabs.get(id)] : query(info));
    }, tabId);
    await popup.evaluate(() => document.getElementById('page-btn').click());
    const result = popup.locator('#page-result');
    await result.waitFor({ state: 'visible', timeout: 30000 }).catch(() => undefined);
    const said = ((await result.textContent().catch(() => '')) ?? '').trim();
    check('D: Send this page says where the page went', /Sent to MetaRush/.test(said), said);
    if (SHOTS) {
      await popup.bringToFront();
      await popup.screenshot({ path: path.join(SHOTS, 'D-popup-sent.png') });
    }
    const sent = await waitFor(async () => (await api(dash, 'resources.list', {})).resources.find((r) => r.kind === 'website' && r.site?.via === 'extension' && r.site?.url === stubURL), 20000, 500);
    check('D: it is in the workspace library as the person saw it, signed in', !!sent && sent.site.signedIn === true && sent.title === 'Acme pricing', sent ? `${sent.name}: ${sent.title}, theme ${sent.site.themeColors?.[0]}` : 'not there');
    if (sent) {
      const pic = await api(dash, 'resources.show', { ref: sent.id });
      const v = pic.resource.versions.find((x) => x.sha256 === pic.row.current);
      check('D: with a picture of the tab and the page text', (v?.width ?? 0) >= 300 && /Teams from \$12/.test(v?.text ?? ''), `${v?.width}x${v?.height}, “${(v?.text ?? '').slice(0, 40)}”`);
    }
  } finally {
    await ctx.close();
    rmSync(userDir, { recursive: true, force: true });
    rmSync(extDir, { recursive: true, force: true });
  }

  /* ---------- screens: desktop and phone, dark and light ---------- */
  for (const theme of ['dark', 'light']) {
    for (const phone of [false, true]) {
      const p = await newPage(browser, problems, { theme, phone });
      const tag = `${phone ? 'phone' : 'desktop'}-${theme}`;
      await p.goto(`${base}/p/${launchId}?tab=resources`);
      const grid = await p.locator('ul[aria-label="Resources"]').first().waitFor({ timeout: 15000 }).then(() => true, () => false);
      if (theme === 'dark' && !phone) check('screens: the Resources tab lists the repository and the page', grid);
      await shot(p, `E-resources-${tag}`);
      if (repoRow) {
        await openResource(p, repoRow.title).catch(() => null);
        await shot(p, `E-repo-${tag}`);
        await p.keyboard.press('Escape');
      }
      if (pageRow) {
        await openResource(p, pageRow.title).catch(() => null);
        await shot(p, `E-page-${tag}`);
        await p.keyboard.press('Escape');
      }
      if (sp && slides.length) {
        await p.goto(`${base}/p/${sp}?tab=cut`);
        const ready = await p.locator('[data-ruler]').first().waitFor({ timeout: 20000 }).then(() => true, () => false);
        const code = slides.find((o) => o.template === 'slide.code') ?? slides[0];
        if (ready) {
          await sleep(1200);
          await seek(p, code.at + code.duration * 0.6, total).catch(() => undefined);
          await sleep(1500);
        }
        await shot(p, `E-slides-${tag}`);
      }
      await p.context().close();
    }
  }
} finally {
  await browser.close();
}

check('no page errors', problems.length === 0, problems.slice(0, 5).join(' | '));
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed${SHOTS ? `; screenshots in ${SHOTS}` : ''}`);
process.exit(failed.length ? 1 : 0);
