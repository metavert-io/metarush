#!/usr/bin/env node
// End-to-end checks of the usability batch through the real dashboard, with
// screenshots (desktop + mobile, dark + light) in web/.screenshots/ux-*.png:
// duplicate dialog and Skip, a throttled upload with reload and resume at the
// server's offset, battery-paused transcription and "Process now", the
// activity bar and drawer, the notification bell, inline title editing, grid
// and list views with drag-to-project and Undo, the watched folder strip and
// dialog, server search into the transcript, job details (render with a
// player, Go to, failed job with Retry and collapsed Details, ?job= reload),
// and a throttled multi-clip preview check for backward media-time jumps.
//
// Run it against a throwaway installation (filesystem media store, a
// metarush-test-* database) whose daemon was started with
// METARUSH_FAKE_BATTERY=18,discharging:
//
//   METARUSH_DASHBOARD=http://127.0.0.1:4851 METARUSH_MINT="bin/metarush --home H dashboard --print-link --no-open" \
//   METARUSH_CLI="bin/metarush --home H" node web/scripts/usability-e2e.mjs
//
// Env: STEPS (comma list), FIXTURES (default tests/fixtures/gen), SCRATCH (temp dir for copies).
import { execSync } from 'node:child_process';
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const webDir = path.resolve(here, '..');
const repo = path.resolve(webDir, '..');
const outDir = path.join(webDir, '.screenshots');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:4851').replace(/\/$/, '');
const fixtures = process.env.FIXTURES ?? path.join(repo, 'tests/fixtures/gen');
const cli = process.env.METARUSH_CLI;
const only = process.env.STEPS ? new Set(process.env.STEPS.split(',')) : null;
const scratch = process.env.SCRATCH ?? mkdtempSync(path.join(os.tmpdir(), 'mr-ux-'));
mkdirSync(outDir, { recursive: true });

const results = [];
const check = (name, ok, detail = '') => {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? `  (${detail})` : ''}`);
};

function mintLink() {
  const cmd = process.env.METARUSH_MINT;
  const out = execSync(cmd, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return `${base}/auth${new URL(m[0]).search}`;
}
const run = (args) => execSync(`${cli} ${args}`, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
const runJSON = (args) => JSON.parse(run(`${args} --json`)).data;

async function api(page, op, input = {}) {
  return page.evaluate(
    async ([op, input]) => {
      const r = await fetch(`/api/ops/${op}`, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' }, body: JSON.stringify(input) });
      const j = await r.json();
      if (!j.ok) throw new Error(`${op}: ${j.error?.code} ${j.error?.message}`);
      return j.data;
    },
    [op, input],
  );
}

async function waitFor(fn, { timeout = 60_000, every = 500, what = 'condition' } = {}) {
  const end = Date.now() + timeout;
  let last;
  while (Date.now() < end) {
    try {
      last = await fn();
      if (last) return last;
    } catch (e) {
      last = e;
    }
    await new Promise((r) => setTimeout(r, every));
  }
  throw new Error(`timed out waiting for ${what}${last instanceof Error ? `: ${last.message}` : ''}`);
}

async function setTheme(page, theme) {
  await page.evaluate((t) => {
    try {
      localStorage.setItem('metarush.theme', t);
    } catch {}
    document.documentElement.setAttribute('data-theme', t);
  }, theme);
}

/** Screenshot in the current theme/viewport; `variants` re-shoots in the other theme. */
async function shot(page, name, { full = false, clip } = {}) {
  await page.waitForTimeout(450);
  const file = path.join(outDir, `ux-${name}.png`);
  await page.screenshot({ path: file, fullPage: full, ...(clip ? { clip } : {}) });
  console.log(`      screenshot ${path.relative(repo, file)}`);
  return file;
}

async function bothThemes(page, name, fn = async () => {}) {
  for (const t of ['dark', 'light']) {
    await setTheme(page, t);
    await fn();
    await shot(page, `${name}-${t}`);
  }
  await setTheme(page, 'dark');
}

const step = (name) => !only || only.has(name);

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 });
const page = await ctx.newPage();
const consoleErrors = [];
page.on('console', (m) => m.type() === 'error' && consoleErrors.push(m.text()));
page.on('pageerror', (e) => consoleErrors.push(`uncaught: ${e.message}`));
await page.goto(mintLink());
await page.waitForURL(`${base}/`);
await page.waitForSelector('header');

const fixture = (n) => path.join(fixtures, n);
const copy = (n, as) => {
  const p = path.join(scratch, as);
  copyFileSync(fixture(n), p);
  return p;
};
const fileInput = () => page.locator('input[type=file]').first();

try {
  /* ---------- mark ---------- */
  if (step('mark')) {
    await bothThemes(page, 'header', async () => {
      await page.goto(`${base}/`);
      await page.waitForSelector('header');
    });
    const hdr = await page.locator('header').boundingBox();
    await shot(page, 'header-mark', { clip: { x: 0, y: 0, width: 520, height: hdr?.height ?? 56 } });
    check('logo renders in the header', (await page.locator('header svg rect[fill="#e0a458"]').count()) > 0);
  }

  /* ---------- duplicates within one drop: dialog, Skip, two assets ---------- */
  if (step('duplicates')) {
    await page.goto(`${base}/`);
    await fileInput().setInputFiles([fixture('interview-a.mov'), copy('interview-a.mov', 'interview-a 2.mov'), fixture('broll-c.mp4')]);
    const dlg = page.getByRole('dialog').filter({ hasText: 'already in MetaRush' });
    await dlg.waitFor({ timeout: 30_000 });
    await shot(page, 'duplicate-batch-dark');
    await setTheme(page, 'light');
    await shot(page, 'duplicate-batch-light');
    await setTheme(page, 'dark');
    check('duplicate dialog names the copy and its original', (await dlg.textContent()).includes('interview-a 2.mov') && (await dlg.textContent()).includes('in this drop'));
    await dlg.getByRole('button', { name: 'Skip' }).click();
    await page.getByRole('region', { name: 'Uploads' }).waitFor();
    await shot(page, 'tray-uploading-dark');
    const assets = await waitFor(
      async () => {
        const a = await api(page, 'asset.list', { unassigned: true });
        return a.assets.length >= 2 ? a.assets : null;
      },
      { timeout: 120_000, what: 'two inbox assets' },
    );
    await page.waitForTimeout(2500);
    const again = await api(page, 'asset.list', { unassigned: true });
    check('Skip stores the duplicate once: exactly 2 assets', again.assets.length === 2, `${again.assets.length} assets`);
    check('asset keeps the original name, not the copy', assets.some((a) => a.name === 'interview-a.mov') && !assets.some((a) => a.name === 'interview-a 2.mov'));

    // A file identical to a stored asset: dialog names the asset and where it is.
    await fileInput().setInputFiles([copy('broll-c.mp4', 'broll copy.mp4')]);
    const dlg2 = page.getByRole('dialog').filter({ hasText: 'Already in MetaRush' });
    await dlg2.waitFor({ timeout: 30_000 });
    await bothThemes(page, 'duplicate-existing');
    check('duplicate of a stored asset names its title and place', /identical to .*broll-c.*\(in Inbox\)/s.test(await dlg2.textContent()));
    await dlg2.getByRole('button', { name: 'Keep both' }).click();
    const kept = await waitFor(
      async () => {
        const a = await api(page, 'asset.list', { unassigned: true });
        const b = a.assets.find((x) => x.name === 'broll-c.mp4');
        return b?.aliases?.includes('broll copy.mp4') ? b : null;
      },
      { timeout: 20_000, what: 'alias recorded' },
    );
    check('Keep both records an alias, still one asset', !!kept && (await api(page, 'asset.list', { unassigned: true })).assets.length === 2);
  }

  /* ---------- battery: paused transcriptions, activity bar, Process now ---------- */
  if (step('battery')) {
    await page.goto(`${base}/`);
    const strip = page.getByRole('button', { name: /Activity:/ });
    await waitFor(async () => /paused: battery at 18%/i.test((await strip.textContent().catch(() => '')) ?? ''), { timeout: 60_000, what: 'battery reason in the activity bar' });
    check('activity bar says why transcriptions wait', true);
    await bothThemes(page, 'activity-paused');
    await page.getByRole('button', { name: 'Show activity details' }).click();
    await page.locator('#mr-activity-drawer').waitFor();
    await bothThemes(page, 'activity-drawer');
    // Inline chips on the asset cards say the same.
    check('asset chip shows transcript paused with the battery level', (await page.getByText(/Transcript paused \(battery 18%\)/).count()) > 0);
    const paused = (await api(page, 'job.list', { type: 'transcribe', state: 'queued' })).jobs.map((x) => x.id);
    await page.locator('#mr-activity-drawer').getByRole('button', { name: 'Process now' }).first().click();
    const ran = await waitFor(
      async () => {
        const mine = (await api(page, 'job.list', { type: 'transcribe' })).jobs.filter((x) => paused.includes(x.id));
        return mine.length && mine.every((x) => x.allowOnBattery && x.state !== 'queued') ? mine : null;
      },
      { timeout: 60_000, what: 'transcription to start on battery' },
    );
    check('Process now runs paused transcriptions on battery', ran.length > 0, ran.map((j) => j.state).join(','));
    await page.keyboard.press('Escape');
  }

  /* ---------- notifications ---------- */
  if (step('notify')) {
    await page.goto(`${base}/`);
    const bell = page.getByRole('button', { name: /^Notifications/ });
    await waitFor(async () => /unread/.test((await bell.getAttribute('aria-label')) ?? ''), { timeout: 240_000, every: 2000, what: 'an unread notification' });
    const before = await bell.getAttribute('aria-label');
    await page.reload();
    await page.waitForSelector('header');
    // The list loads after the header renders (slower when the machine is busy): give it a moment.
    const label = () => page.getByRole('button', { name: /^Notifications/ }).getAttribute('aria-label');
    const after = await waitFor(async () => (/unread/.test((await label()) ?? '') ? label() : null), { timeout: 10_000, every: 200, what: 'the unread badge' }).catch(() => label());
    check('unread badge survives a reload', /unread/.test(after ?? ''), `${before} -> ${after}`);
    await page.getByRole('button', { name: /^Notifications/ }).click();
    await page.getByRole('dialog', { name: 'Notifications' }).waitFor();
    await bothThemes(page, 'bell');
    const list = await api(page, 'notification.list', {});
    check('one "file ready" per asset', new Set(list.notifications.filter((n) => n.kind === 'file_ready').map((n) => n.groupKey)).size === list.notifications.filter((n) => n.kind === 'file_ready').length);
    await page.keyboard.press('Escape');
  }

  /* ---------- titles ---------- */
  if (step('titles')) {
    await page.goto(`${base}/`);
    const card = page.getByRole('listitem').filter({ hasText: 'interview-a' }).first();
    await card.hover();
    await card.getByRole('button', { name: /^Rename/ }).click();
    const input = page.getByRole('textbox', { name: /^Title for/ });
    await input.fill('Robot garage story');
    await shot(page, 'title-editing-dark');
    await input.press('Enter');
    await page.getByText('Robot garage story').first().waitFor();
    const a = (await api(page, 'asset.list', { unassigned: true })).assets.find((x) => x.name === 'interview-a.mov');
    check('title saved, filename unchanged', a?.title === 'Robot garage story' && a?.name === 'interview-a.mov');
    const s = await api(page, 'search', { query: 'robot garage' });
    check('search finds the new title', s.videos.some((v) => v.title === 'Robot garage story'));
    const audit = await api(page, 'audit.list', { targetId: a.id });
    check('rename is audited', audit.events.some((e) => e.action === 'asset.update'));
    await shot(page, 'title-saved-dark');
  }

  /* ---------- grid / list and drag to project ---------- */
  if (step('drag')) {
    const existing = (await api(page, 'project.list', {})).projects.find((p) => p.name === 'Garage film');
    const proj = existing ? { project: existing } : await api(page, 'project.create', { name: 'Garage film', brief: { goal: 'A short story about building robots' } });
    await page.goto(`${base}/`);
    await page.getByRole('heading', { name: 'Garage film' }).first().waitFor().catch(() => undefined);
    for (const v of ['grid', 'list']) {
      await page.getByRole('radio', { name: v === 'grid' ? 'Grid with large thumbnails' : 'Compact list' }).click();
      await page.locator('#inbox').evaluate((el) => el.scrollIntoView({ block: 'center' }));
      await bothThemes(page, `inbox-${v}`);
    }
    const stored = await page.evaluate(() => localStorage.getItem('metarush.view.inbox'));
    check('layout choice is remembered', stored === '"list"', stored ?? 'null');
    // Select both clips (x on the highlighted item, and a checkbox).
    await page.getByRole('checkbox', { name: /^Select/ }).first().click();
    await page.getByRole('checkbox', { name: /^Select/ }).nth(1).click();
    await page.getByRole('region', { name: 'Selected clips' }).waitFor();
    // Drag with the mouse onto the project card, screenshot mid-drag. (A
    // finished upload tray folds away by itself; clear it so it cannot cover the row.)
    const tray = page.getByRole('region', { name: 'Uploads' });
    if (await tray.count()) await tray.getByRole('button', { name: 'Clear' }).click().catch(() => undefined);
    const src = page.getByRole('list', { name: /inbox/i }).getByRole('listitem').filter({ hasText: 'Robot garage story' }).first();
    const dst = page.locator(`[data-drop-project="${proj.project.id}"]`);
    await src.scrollIntoViewIfNeeded();
    const s = await src.boundingBox();
    const d = await dst.boundingBox();
    await page.mouse.move(s.x + 40, s.y + s.height / 2);
    await page.mouse.down();
    await page.mouse.move(s.x + 80, s.y + s.height / 2 - 20, { steps: 5 });
    await page.mouse.move(d.x + d.width / 2, d.y + d.height / 2, { steps: 12 });
    await page.waitForTimeout(300);
    await shot(page, 'drag-in-progress-dark');
    check('drop targets light up during a drag', (await page.locator('.mr-drop-over, .mr-drop-target').count()) > 0);
    await page.mouse.up();
    const toast = page.getByText(/Added 2 clips to Garage film/);
    await toast.waitFor({ timeout: 10_000 });
    await shot(page, 'drop-undo-toast-dark');
    const linked = await api(page, 'project.show', { id: proj.project.id });
    check('drop adds both selected clips to the project', linked.assets.length === 2);
    await page.getByRole('button', { name: 'Undo' }).click();
    await waitFor(async () => (await api(page, 'project.show', { id: proj.project.id })).assets.length === 0, { timeout: 10_000, what: 'undo' });
    check('Undo removes them again', true);
    // Keyboard alternative: picker.
    await page.getByRole('radio', { name: 'Grid with large thumbnails' }).click();
  }

  /* ---------- watched folder ---------- */
  if (step('watch')) {
    await page.goto(`${base}/`);
    const showInbox = () => page.locator('#inbox').evaluate((el) => el.scrollIntoView({ block: 'start' }));
    const fresh = (await api(page, 'watch.status', {})).state === 'not_configured';
    if (fresh) {
      await page.getByText('Add footage automatically.').waitFor();
      await bothThemes(page, 'watch-not-configured', showInbox);
      await page.getByRole('button', { name: 'Set up watched folder' }).click();
    } else {
      await bothThemes(page, 'watch-off', showInbox);
      await page.locator('#inbox').getByRole('button', { name: /^(Change|Set up|Configure|Turn on)/ }).first().click();
    }
    await page.getByRole('dialog', { name: 'Watched folder' }).waitFor();
    await bothThemes(page, 'watch-dialog');
    await page.keyboard.press('Escape');
    const dir = path.join(scratch, 'Watched Inbox');
    mkdirSync(dir, { recursive: true });
    run(`watch set "${dir}" --stable-seconds 2`);
    await page.reload();
    await page.getByText(/^Watching$/).first().waitFor({ timeout: 20_000 }).catch(() => undefined);
    await waitFor(async () => (await api(page, 'watch.status', {})).state === 'watching', { timeout: 20_000, what: 'watching' });
    check('CLI change hot-reloads the daemon watcher', true);
    await page.reload();
    await page.waitForTimeout(1500);
    await bothThemes(page, 'watch-watching', showInbox);
    rmSync(dir, { recursive: true, force: true });
    await waitFor(async () => (await api(page, 'watch.status', {})).state === 'missing', { timeout: 20_000, what: 'missing' });
    await page.reload();
    await page.waitForTimeout(1500);
    await bothThemes(page, 'watch-missing', showInbox);
    await page.goto(`${base}/settings/footage#watched-folder`);
    await page.waitForTimeout(1200);
    await bothThemes(page, 'settings-watch');
    run('watch off');
  }

  /* ---------- search into the transcript ---------- */
  if (step('search')) {
    await waitFor(
      async () => {
        const s = await api(page, 'search', { query: 'neighbor' });
        return s.moments.length ? s : null;
      },
      { timeout: 240_000, every: 3000, what: 'a transcript containing "neighbor"' },
    );
    await page.goto(`${base}/`);
    await page.getByRole('button', { name: /^Search/ }).first().waitFor();
    await page.keyboard.press('/');
    await page.getByRole('combobox').fill('neighb');
    await page.getByText('Footage moments').waitFor({ timeout: 15_000 });
    await bothThemes(page, 'search');
    const opt = page.getByRole('option').first();
    await opt.click();
    // An inbox clip opens in a dialog; a clip in a project opens in its Sources tab.
    await page.locator('[data-hit]').first().waitFor({ timeout: 20_000 });
    await page.waitForTimeout(1500);
    const hit = await page.locator('[data-hit]').first().textContent();
    check('opening a moment highlights the word in the transcript', /neighb/i.test(hit ?? ''), hit ?? '');
    const t = await page.evaluate(() => document.querySelector('video')?.currentTime ?? -1);
    check('the player is at the moment', t > 5, `${t.toFixed(2)}s`);
    await shot(page, 'search-opened-dark');
    await page.keyboard.press('Escape');
  }

  /* ---------- job details ---------- */
  if (step('jobs')) {
    const assets = (await api(page, 'asset.list', {})).assets;
    const A = assets.find((a) => a.name === 'interview-a.mov');
    const B = assets.find((a) => a.name === 'broll-c.mp4');
    const proj = (await api(page, 'project.list', {})).projects.find((p) => p.name === 'Garage film') ?? (await api(page, 'project.create', { name: 'Garage film' })).project;
    for (const a of [A, B]) await api(page, 'asset.link', { assetId: a.id, projectId: proj.id });
    const plan = {
      projectId: proj.id,
      instruction: 'Open on the question, cut to the b-roll, end on the answer.',
      rationale: 'The question is the hook.',
      spec: { schemaVersion: 1, title: 'Garage cut', output: { preset: 'vertical-1080p', fps: 30, platform: 'shorts' },
        clips: [{ id: 'q', assetId: A.id, in: 11.6, out: 18.4 }, { id: 'b', assetId: B.id, in: 0, out: 2.5, muted: true }, { id: 'a', assetId: A.id, in: 20.8, out: 26.2 }] },
    };
    const head = (await api(page, 'project.show', { id: proj.id })).project.branches?.main?.head;
    if (head) plan.baseRevisionId = head; // a re-run builds on the cut it made last time
    writeFileSync(path.join(scratch, 'plan.json'), JSON.stringify(plan));
    const tok = runJSON(`edit check --plan "${path.join(scratch, 'plan.json')}"`).confirmToken;
    const rev = runJSON(`edit apply --plan "${path.join(scratch, 'plan.json')}" --confirm ${tok}`).revision;
    const sub = await api(page, 'render.submit', { revisionId: rev.id, quality: 'review' });
    await waitFor(async () => (await api(page, 'render.status', { id: sub.render.id })).render.state === 'available', { timeout: 300_000, every: 3000, what: 'the review render' });
    await page.goto(`${base}/renders?tab=jobs`);
    await page.locator(`[data-job="${sub.job.id}"]`).click({ position: { x: 24, y: 16 } }); // the label cell, not the project link
    const dlg = page.getByRole('dialog', { name: 'Job details' });
    await dlg.waitFor();
    await dlg.locator('video').waitFor();
    const playable = await waitFor(async () => (await dlg.locator('video').evaluate((v) => v.readyState)) >= 1, { timeout: 20_000, what: 'video metadata' });
    check('finished render opens with a playable video', !!playable);
    await bothThemes(page, 'job-render');
    await page.reload();
    await page.getByRole('dialog', { name: 'Job details' }).waitFor({ timeout: 15_000 });
    check('?job= survives a reload', page.url().includes(`job=${sub.job.id}`));
    await page.getByRole('dialog', { name: 'Job details' }).getByRole('button', { name: /Go to the render/ }).click();
    await page.waitForURL(/tab=renders/);
    await page.locator(`[data-render="${sub.render.id}"][data-active]`).waitFor({ timeout: 10_000 });
    check('Go to lands on the highlighted render', true);
    await shot(page, 'job-goto-render-dark');
    // A failed job (inserted into this throwaway test database).
    const db = process.env.TEST_DB;
    const uri = process.env.TEST_URI;
    if (db && uri && db.startsWith('metarush-test-')) {
      const ws = (await api(page, 'workspace.show', {})).workspace.id;
      const id = `job_01failedtestjob0000000000`.slice(0, 30);
      execSync(
        `mongosh --quiet "${uri}" --eval 'db.getSiblingDB("${db}").jobs.insertOne({_id:"${id}",workspaceId:"${ws}",type:"transcribe",schemaVersion:1,state:"failed",priority:6,heavy:true,assetId:"${A.id}",payload:{assetId:"${A.id}"},idempotencyKey:"test-failed",attemptCount:3,maxAttempts:3,attempts:[{worker:"ins_x",workerName:"studio-mac",startedAt:new Date(Date.now()-90000),endedAt:new Date(Date.now()-80000),outcome:"retry",error:"sherpa-onnx-offline failed: exit status 1: model file not found"},{worker:"ins_x",workerName:"studio-mac",startedAt:new Date(Date.now()-40000),endedAt:new Date(Date.now()-30000),outcome:"failed",error:"sherpa-onnx-offline failed: exit status 1: model file not found"}],nextRunAt:new Date(),cancelRequested:false,progress:{pct:0},error:"sherpa-onnx-offline failed: exit status 1: model file not found",createdBy:{userId:"usr_x",installationId:"ins_x",via:"cli"},createdAt:new Date(Date.now()-100000),updatedAt:new Date(),finishedAt:new Date(Date.now()-30000)})'`,
        { stdio: 'ignore' },
      );
      await page.goto(`${base}/renders?tab=jobs&job=${id}`);
      const fd = page.getByRole('dialog', { name: 'Job details' });
      await fd.waitFor();
      await fd.getByRole('button', { name: 'Retry' }).waitFor();
      const open = await fd.locator('details').evaluate((d) => d.open);
      check('failed job shows Retry with Details collapsed', !open);
      await bothThemes(page, 'job-failed');
    }
  }

  /* ---------- upload reload + resume at the server offset ---------- */
  if (step('resume')) {
    const big = copy('interview-b.mp4', 'resume-test.mp4');
    const cdp = await ctx.newCDPSession(page);
    await page.goto(`${base}/`);
    await cdp.send('Network.enable');
    await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 20, downloadThroughput: -1, uploadThroughput: 400 * 1024 });
    await fileInput().setInputFiles([big]);
    await waitFor(async () => {
      const l = await page.evaluate(async () => (await (await fetch('/api/uploads', { credentials: 'same-origin' })).json()).data.uploads);
      const u = l.find((x) => x.name === 'resume-test.mp4');
      return u && u.offset > 1_000_000 ? u : null;
    }, { timeout: 60_000, what: 'some bytes on the server' });
    await shot(page, 'tray-mid-upload-dark');
    await setTheme(page, 'light');
    await shot(page, 'tray-mid-upload-light');
    await setTheme(page, 'dark');
    await page.reload();
    // The tray and the home page's in-progress list both say it.
    await page.getByText(/Interrupted at \d+%/).first().waitFor({ timeout: 15_000 });
    check('after a reload the tray shows the upload as interrupted', true);
    await shot(page, 'tray-interrupted-dark');
    const before = await page.evaluate(async () => (await (await fetch('/api/uploads', { credentials: 'same-origin' })).json()).data.uploads.find((x) => x.name === 'resume-test.mp4').offset);
    await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
    const firstPatch = page.waitForRequest((r) => r.method() === 'PATCH' && r.url().includes('/api/uploads/'));
    await fileInput().setInputFiles([big]);
    const req = await firstPatch;
    const off = Number(req.headers()['upload-offset']);
    check('dropping the same file again resumes at the server offset', off === before && off > 0, `server had ${before}, resumed at ${off}`);
    await waitFor(async () => {
      const a = await api(page, 'asset.list', {});
      return a.assets.some((x) => x.name === 'resume-test.mp4') ? true : null;
    }, { timeout: 120_000, what: 'the resumed upload to finish' });
    check('resumed upload finished', true);
  }

  /* ---------- preview: no backward media-time jumps under throttling ---------- */
  if (step('preview')) {
    const proj = (await api(page, 'project.list', {})).projects.find((p) => p.name === 'Garage film');
    if (proj) {
      const cdp = await ctx.newCDPSession(page);
      await cdp.send('Network.enable');
      await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 80, downloadThroughput: (6 * 1024 * 1024) / 8, uploadThroughput: -1 });
      await page.goto(`${base}/p/${proj.id}?tab=cut`);
      const player = page.locator('[data-player-surface]').first();
      await player.waitFor({ timeout: 30_000 });
      await page.waitForTimeout(3000);
      await shot(page, 'preview-dark');
      await player.click();
      // Sample every element's currentTime; a backward jump while playing (beyond a frame) means a re-seek heard as a repeat.
      const jumps = await page.evaluate(async () => {
        const out = [];
        const last = new Map();
        const t0 = performance.now();
        while (performance.now() - t0 < 14_000) {
          for (const v of document.querySelectorAll('video, audio')) {
            const prev = last.get(v);
            if (!v.paused && prev !== undefined && v.currentTime < prev - 0.08) out.push({ src: v.currentSrc.slice(-40), from: prev, to: v.currentTime });
            last.set(v, v.currentTime);
          }
          await new Promise((r) => setTimeout(r, 100));
        }
        return out;
      });
      check('throttled multi-clip preview plays without backward media jumps', jumps.length === 0, JSON.stringify(jumps).slice(0, 300));
      await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
    }
  }

  /* ---------- mobile ---------- */
  if (step('mobile')) {
    const m = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
    const p = await m.newPage();
    await p.goto(mintLink());
    await p.waitForSelector('header');
    for (const t of ['dark', 'light']) {
      await setTheme(p, t);
      await p.goto(`${base}/`);
      await p.waitForTimeout(1500);
      await p.screenshot({ path: path.join(outDir, `ux-mobile-home-${t}.png`) });
      await p.getByRole('button', { name: /^Notifications/ }).click();
      await p.waitForTimeout(500);
      await p.screenshot({ path: path.join(outDir, `ux-mobile-bell-${t}.png`) });
      await p.keyboard.press('Escape');
      await p.getByRole('button', { name: /^Activity/ }).first().click();
      await p.waitForTimeout(600);
      await p.screenshot({ path: path.join(outDir, `ux-mobile-activity-${t}.png`) });
      await p.keyboard.press('Escape');
      await p.getByRole('radio', { name: 'Compact list' }).click().catch(() => undefined);
      await p.waitForTimeout(500);
      await p.locator('#inbox').screenshot({ path: path.join(outDir, `ux-mobile-inbox-list-${t}.png`) }).catch(() => undefined);
      await p.goto(`${base}/settings/footage#watched-folder`);
      await p.waitForTimeout(1200);
      await p.screenshot({ path: path.join(outDir, `ux-mobile-settings-${t}.png`) });
    }
    console.log('      screenshots ux-mobile-*.png');
    await m.close();
  }
} catch (e) {
  check('script finished', false, e instanceof Error ? e.message : String(e));
  await page.screenshot({ path: path.join(outDir, 'ux-failure.png') }).catch(() => undefined);
} finally {
  await browser.close();
}

const failed = results.filter((r) => !r.ok);
if (consoleErrors.length) console.log(`console errors:\n  ${consoleErrors.slice(0, 10).join('\n  ')}`);
console.log(`\n${results.length - failed.length} passed, ${failed.length} failed`);
process.exit(failed.length ? 1 : 0);
