#!/usr/bin/env node
// The dashboard as a Windows 11 person uses it (D247). Run it through
// scripts/windows-e2e.sh: a throwaway installation with the project "Harbour
// film", a tiny clip (CLIP) and a folder as Windows leaves it (FOLDER: a clip,
// Thumbs.db, desktop.ini, a $RECYCLE.BIN). In Chromium with a Windows
// browser's identity, and again in Microsoft Edge when it is installed, it
// checks that:
//   1. a file dropped with a Windows path for a name arrives under its own name;
//   2. Windows' own files are left out of a dropped or chosen folder;
//   3. a name Windows can't keep downloads under one it can; an accented one whole;
//   4. text pasted with Windows line endings is stored with plain ones;
//   5. shortcuts say Ctrl; no page scrolls sideways at 1366×768 and 125%;
//   6. no native dialogs or page errors. Screenshots in WINDOWS_SHOTS.
import { execSync } from 'node:child_process';
import { mkdirSync, readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
for (const k of ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'WINDOWS_SHOTS', 'CLIPS', 'FOLDER', 'PROJECT']) {
  if (!process.env[k]) {
    console.error('set METARUSH_DASHBOARD, METARUSH_MINT, WINDOWS_SHOTS, CLIPS, FOLDER and PROJECT (scripts/windows-e2e.sh does)');
    process.exit(2);
  }
}
const base = process.env.METARUSH_DASHBOARD.replace(/\/$/, '');
const shotsDir = process.env.WINDOWS_SHOTS;
const project = process.env.PROJECT;
// Tiny clips, each its own bytes (MetaRush keeps one copy of the same file): one per drop and browser.
const clipFiles = readdirSync(process.env.CLIPS).filter((f) => f.endsWith('.mp4')).sort();
let nextClip = 0;
const freshClip = () => readFileSync(path.join(process.env.CLIPS, clipFiles[nextClip++ % clipFiles.length])).toString('base64');
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

async function waitFor(fn, ms = 30000, every = 500) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn().catch(() => undefined);
    if (v) return v;
    if (Date.now() > end) return undefined;
    await new Promise((r) => setTimeout(r, every));
  }
}

// What Edge and Chrome on Windows 11 say they are.
const WINDOWS_UA = {
  chromium: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36',
  msedge: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.0.0',
};

async function windowsPage(browser, who) {
  // A common Windows laptop: 1366×768 at 125% scaling (the CSS viewport is smaller still).
  const ctx = await browser.newContext({
    viewport: { width: 1093, height: 614 },
    deviceScaleFactor: 1.25,
    userAgent: WINDOWS_UA[who],
    colorScheme: 'light',
    reducedMotion: 'reduce',
    acceptDownloads: true,
  });
  await ctx.addInitScript(() => {
    Object.defineProperty(Navigator.prototype, 'platform', { get: () => 'Win32' });
  });
  const page = await ctx.newPage();
  page.on('pageerror', (e) => problems.push(`${who}: pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) problems.push(`${who}: console: ${m.text()}`);
  });
  page.on('dialog', (d) => {
    problems.push(`${who}: native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  return { ctx, page };
}

let shotN = 0;
async function shot(page, who, name) {
  await page.waitForTimeout(500);
  await page.screenshot({ path: path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${who}-${name}.png`) });
  const overflow = await page.evaluate(() => {
    const w = document.documentElement.clientWidth;
    const by = [...document.querySelectorAll('body *')]
      .filter((el) => el.getBoundingClientRect().right > w + 1 && getComputedStyle(el).position !== 'fixed')
      .map((el) => `${el.tagName.toLowerCase()}.${String(el.className).slice(0, 50)}`)
      .slice(-3);
    return { px: document.documentElement.scrollWidth - w, by };
  });
  check(`${who}: ${name} doesn't scroll sideways at 1366×768 and 125%`, overflow.px <= 0, overflow.px > 0 ? `${overflow.px}px (${overflow.by.join(' | ')})` : '');
}

/** Drops files on the page the way a browser does (a DataTransfer of Files). */
async function drop(page, files) {
  await page.evaluate(
    (files) => {
      const dt = new DataTransfer();
      for (const f of files) {
        const bytes = f.b64 ? Uint8Array.from(atob(f.b64), (c) => c.charCodeAt(0)) : new TextEncoder().encode('x');
        dt.items.add(new File([bytes], f.name, { type: f.b64 ? 'video/mp4' : '' }));
      }
      for (const t of ['dragenter', 'dragover', 'drop']) window.dispatchEvent(new DragEvent(t, { dataTransfer: dt, bubbles: true, cancelable: true }));
    },
    files.map((f) => ({ name: f.name, b64: f.video ? freshClip() : '' })),
  );
}

const assetNames = async (page) => (await api(page, 'project.show', { id: project })).assets.map((a) => a.name);

async function run(browser, who) {
  const tag = `${who}-${Date.now().toString(36).slice(-4)}`;
  const { ctx, page } = await windowsPage(browser, who);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  await page.waitForLoadState('networkidle');
  check(`${who}: the dashboard believes it is on Windows`, await page.evaluate(() => navigator.platform === 'Win32' && /Windows NT/.test(navigator.userAgent)));
  await shot(page, who, 'home');

  // 1. A Windows path as a file's name: only its last part is kept, accents and all.
  await page.goto(`${base}/p/${project}?tab=sources`);
  await page.waitForLoadState('networkidle');
  const accented = `Hafen – Café (take 2) ${tag}.mp4`;
  await drop(page, [{ name: `C:\\Users\\Ana\\Videos\\${accented}`, video: true }]);
  const got = await waitFor(async () => (await assetNames(page)).includes(accented), 60000, 1000);
  check(`${who}: a file dropped as C:\\Users\\…\\clip.mp4 arrives under its own name`, !!got, got ? '' : (await assetNames(page)).join(', '));

  // 2. Windows' own files in a drop are left out.
  const ferry = `ferry ${tag}.mp4`;
  await drop(page, [{ name: 'Thumbs.db' }, { name: 'desktop.ini' }, { name: 'DESKTOP.INI' }, { name: ferry, video: true }]);
  await waitFor(async () => (await assetNames(page)).includes(ferry), 60000, 1000);
  const names = await assetNames(page);
  const uploads = await page.evaluate(async () => (await (await fetch('/api/uploads', { credentials: 'same-origin' })).json()).data?.uploads ?? []);
  check(`${who}: a drop with Thumbs.db and desktop.ini adds only the clip`,
    names.includes(ferry) && !names.some((n) => /thumbs\.db|desktop\.ini/i.test(n)) && !uploads.some((u) => /thumbs\.db|desktop\.ini/i.test(u.name)),
    names.join(', '));

  // ... and from a folder chosen with Choose a folder (Explorer's folder picker).
  const folderInput = page.locator('[data-addbox] input[type=file]').nth(1);
  let folderOK = false;
  let folderNote = '';
  if ((await folderInput.count()) === 1) {
    try {
      await folderInput.setInputFiles(process.env.FOLDER);
      folderOK = !!(await waitFor(async () => (await assetNames(page)).includes('ferry-dock.mp4'), 60000, 1000));
      const after = await assetNames(page);
      folderOK = folderOK && !after.some((n) => /thumbs\.db|desktop\.ini|old-take/i.test(n));
      folderNote = after.join(', ');
    } catch (e) {
      folderNote = `this Playwright can't choose a folder: ${e.message.split('\n')[0]}`;
      folderOK = true;
    }
  } else {
    folderNote = 'no folder picker in the add box';
  }
  check(`${who}: a chosen folder adds its clip, not Thumbs.db, desktop.ini or the Recycle Bin`, folderOK, folderNote);
  await shot(page, who, 'sources');

  // 3. Downloads under names Windows keeps.
  const odd = `Q&A: what's next? ${tag}.mp4`;
  await drop(page, [{ name: odd, video: true }]);
  // The original is served once the clip is ready.
  const ready = (name) => waitFor(async () => (await api(page, 'project.show', { id: project })).assets.find((a) => a.name === name && a.state === 'available'), 60000, 1000);
  const oddAsset = await ready(odd);
  const accentedAsset = await ready(accented);
  const download = async (assetId) => {
    const [d] = await Promise.all([
      page.waitForEvent('download', { timeout: 20000 }),
      page.evaluate((id) => {
        const a = document.createElement('a');
        a.href = `/media/assets/${id}/original?download=1`;
        document.body.appendChild(a);
        a.click();
        a.remove();
      }, assetId),
    ]);
    return d.suggestedFilename();
  };
  if (oddAsset) {
    const name = await download(oddAsset.id);
    check(`${who}: “Q&A: what's next?.mp4” downloads as “Q&A- what's next-.mp4”`, name === `Q&A- what's next- ${tag}.mp4`, name);
  } else {
    check(`${who}: the clip with a name Windows can't keep arrived`, false);
  }
  if (accentedAsset) {
    const name = await download(accentedAsset.id);
    check(`${who}: an accented name downloads whole`, name === accented, name);
  }

  // 4. Windows line endings in pasted text: delivery notes on a version.
  const clipId = (await api(page, 'project.show', { id: project })).assets.find((a) => a.name === ferry)?.id;
  // The project's main cut (the first browser makes it; another one adds a version to it).
  const proj = await api(page, 'project.show', { id: project });
  const head = proj.project.branches?.main?.head;
  const saved = await api(page, 'edit.apply', {
    plan: head
      ? { baseRevisionId: head, origin: 'timeline:studio', instruction: 'Another clip for the Windows run', ops: [{ op: 'insertClip', clip: { id: `ferry-${tag}`, assetId: clipId, in: 0, out: 2 } }] }
      : { projectId: project, origin: 'timeline:studio', instruction: 'A cut for the Windows run',
        spec: { schemaVersion: 1, output: { preset: 'landscape-1080p', fps: 30 }, clips: [{ id: 'ferry', assetId: clipId, in: 0, out: 2 }] } },
  });
  const rev = saved.revision.id;
  await page.goto(`${base}/p/${project}/r/${rev}`);
  await page.locator('[data-notes-edit]').click();
  const form = page.locator('[data-notes-form]');
  await form.waitFor({ timeout: 10000 });
  await form.locator('input').first().fill('Harbour at dawn');
  const areas = form.locator('textarea');
  await areas.nth(1).fill('The ferry comes in.\r\nThe dock wakes up.\r\n');
  await areas.nth(2).fill('0:00 Arrival\r\n0:01 The dock\r\n');
  await shot(page, who, 'delivery-notes');
  await form.locator('[data-notes-save]').click();
  const notes = await waitFor(async () => (await api(page, 'delivery.notes', { projectId: project, revision: rev }))?.notes, 15000);
  check(`${who}: a description pasted with Windows line endings is stored with plain ones`,
    !!notes && !notes.description.includes('\r') && notes.description.includes('\n'), JSON.stringify(notes?.description));
  check(`${who}: chapters pasted with Windows line endings are read line by line`,
    !!notes && notes.chapters?.length === 2 && notes.chapters[1].title === 'The dock', JSON.stringify(notes?.chapters));

  // 5. Shortcuts say Ctrl on Windows.
  await page.goto(`${base}/p/${project}?tab=cut`);
  await page.waitForLoadState('networkidle');
  await page.locator('body').click({ position: { x: 5, y: 5 } });
  await page.keyboard.type('?');
  const sheet = await waitFor(async () => {
    const d = page.getByRole('dialog', { name: 'Timeline shortcuts' });
    return (await d.count()) ? d.innerText() : undefined;
  }, 8000);
  check(`${who}: the timeline's shortcut sheet says Ctrl, not ⌘`, !!sheet && /Ctrl/.test(sheet) && !/⌘|⌥/.test(sheet), sheet ? '' : 'the sheet did not open');
  if (sheet) {
    await shot(page, who, 'shortcuts');
    await page.keyboard.press('Escape');
  }
  await shot(page, who, 'edit');
  await page.goto(`${base}/settings/general`);
  await page.waitForLoadState('networkidle');
  await shot(page, who, 'settings');
  await ctx.close();
}

async function main() {
  const browsers = [['chromium', () => chromium.launch()]];
  let edge = null;
  try {
    edge = await chromium.launch({ channel: 'msedge' });
  } catch (e) {
    console.log(`skip msedge: Microsoft Edge isn't installed on this computer (${e.message.split('\n')[0].slice(0, 120)}). Check it on Windows 11: docs/windows.md.`);
  }
  for (const [who, launch] of browsers) {
    const b = await launch();
    try {
      await run(b, who);
    } finally {
      await b.close();
    }
  }
  if (edge) {
    try {
      await run(edge, 'msedge');
    } finally {
      await edge.close();
    }
  }
  for (const p of problems) check(p, false);
  const failed = results.filter((r) => !r.ok);
  console.log(`\nwindows: ${results.length - failed.length} of ${results.length} checks pass${edge ? ' (Chromium and Edge)' : ' (Chromium; Edge not installed)'}`);
  process.exit(failed.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
