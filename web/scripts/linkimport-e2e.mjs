#!/usr/bin/env node
// End-to-end check of link imports in the dashboard and the browser extension,
// against a daemon whose downloader is the fake (no network):
//   paste a link in the Inbox · the activity bar shows looking up, downloading
//   (with a percent) and adding, also after a reload · the clip says where it
//   came from · ⌘V anywhere imports · a playlist is refused with "Import the
//   first 5" · a bot check offers "Use my browser's sign-in" (styled dialog,
//   never a native one) and then works · "Import the first 5" imports the
//   playlist video by video and empties the field · Settings shows the downloader and the
//   extension · the extension, loaded in Chromium, pairs from its popup and
//   saves a video from a (routed) TikTok page into the inbox, and refuses a
//   copy-protected one.
//
//   go build -o /tmp/mr-fake/yt-dlp ./internal/testutil/fakeytdlp/cmd/fake-yt-dlp
//   METARUSH_YTDLP=/tmp/mr-fake/yt-dlp bin/metarush --home <test home> serve      (port from its config)
//   METARUSH_DASHBOARD=http://127.0.0.1:<port> \
//   METARUSH_MINT="bin/metarush --home <test home> dashboard --print-link --no-open" node web/scripts/linkimport-e2e.mjs
import { execSync } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:4845').replace(/\/$/, '');
if (!process.env.METARUSH_MINT) {
  console.error('set METARUSH_MINT and METARUSH_DASHBOARD (see the header of this script)');
  process.exit(2);
}
const SHOTS = process.env.SHOTS_DIR ?? '';

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

async function shot(page, name) {
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: false });
}

async function waitFor(fn, ms, every = 500) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) return null;
    await new Promise((r) => setTimeout(r, every));
  }
}

const uniq = Date.now().toString(36).slice(-5);
// YouTube IDs are 11 characters; the fake decides what happens from words in them.
const id11 = (prefix) => (prefix + uniq + 'xxxxxxxxxxx').slice(0, 11);

async function dashboard(problems) {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await ctx.newPage();
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('dialog', (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
  await page.goto(`${base}/`);
  const field = page.getByLabel('Or paste a link');
  await field.waitFor({ timeout: 15_000 });
  check('the Inbox has a "Paste a link" field', true);

  // 1. Paste a link: a slow fake download so the stages can be watched.
  const slow = `https://www.youtube.com/watch?v=${id11('slow')}`;
  await field.fill(slow);
  await page.getByRole('button', { name: 'Import', exact: true }).click();
  await page.getByText('Importing the video from YouTube').first().waitFor({ timeout: 15_000 });
  check('a toast says the video is being imported', true);
  await page.getByRole('button', { name: /^Activity/ }).first().click();
  const drawer = page.locator('#mr-activity-drawer');
  const seenStages = new Set();
  const downloading = await waitFor(async () => {
    const txt = (await drawer.innerText().catch(() => '')) || '';
    for (const s of ['Looking up the video', 'Downloading', 'Adding to MetaRush', 'Waiting to start']) if (txt.includes(s)) seenStages.add(s);
    return /Downloading/.test(txt) ? txt : null;
  }, 30_000, 250);
  check('the activity bar shows "Downloading" with a percent', !!downloading && /\d+%/.test(downloading), [...seenStages].join(', '));
  await shot(page, '1-downloading');
  // A reload keeps showing it (server state, not page state).
  await page.reload();
  await page.getByRole('button', { name: /^Activity/ }).first().click();
  const afterReload = await waitFor(async () => {
    const t = (await drawer.innerText().catch(() => '')) || '';
    return t.includes('Fake video') || t.includes('YouTube video') || /Downloading|Adding to MetaRush|Ready|Processing/.test(t) ? t : null;
  }, 15_000);
  check('the import is still in the activity bar after a reload', !!afterReload);
  const done = await waitFor(async () => {
    const list = await api(page, 'asset.list', { unassigned: true, limit: 500 });
    return list.assets.find((a) => a.source?.url?.endsWith(slow.split('v=')[1]));
  }, 90_000, 1000);
  check('the video arrives in the inbox with its original title', !!done && done.title.startsWith('Fake video'), done?.title);
  check('the clip keeps where it came from', !!done?.source && done.source.handle === '@fakechannel' && done.source.platform === 'youtube', JSON.stringify(done?.source ?? {}));
  await page.keyboard.press('Escape');

  // 2. The clip's info: "From YouTube · @handle · date · open original".
  await page.goto(`${base}/?asset=${done?.id}`);
  const credit = page.getByText(/From YouTube · @fakechannel · 12 Mar 2024/).first();
  await credit.waitFor({ timeout: 15_000 }).catch(() => undefined);
  check('the clip info says "From YouTube · @handle · 12 Mar 2024"', await credit.isVisible());
  const orig = page.getByRole('link', { name: /open original/ }).first();
  check('"open original" links to the post in a new tab', (await orig.getAttribute('href'))?.includes('youtube.com/watch') && (await orig.getAttribute('target')) === '_blank');
  await shot(page, '2-clip-credit');
  await page.keyboard.press('Escape');

  // 3. ⌘V anywhere outside a text box imports.
  await page.goto(`${base}/`);
  await page.locator('main').first().click({ position: { x: 5, y: 5 } }).catch(() => undefined);
  const tt = `https://www.tiktok.com/@fakecreator/video/70000000000${Date.now() % 100000000}`;
  await page.evaluate((text) => {
    const dt = new DataTransfer();
    dt.setData('text/plain', text);
    document.body.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true }));
  }, tt);
  await page.getByText('Importing the video from TikTok').first().waitFor({ timeout: 15_000 }).catch(() => undefined);
  check('⌘V with a link imports it', await page.getByText('Importing the video from TikTok').first().isVisible());

  // 4. A playlist imports nothing by default, and offers the first 5.
  const playlistLink = 'https://www.youtube.com/playlist?list=PLfake' + uniq + 'abcdef';
  await field.fill(playlistLink);
  await page.getByRole('button', { name: 'Import', exact: true }).click();
  const pl = page.getByText('That link is a playlist, so nothing was imported').first();
  await pl.waitFor({ timeout: 15_000 }).catch(() => undefined);
  check('a playlist is refused plainly', await pl.isVisible());
  check('…with "Import the first 5"', await page.getByRole('button', { name: 'Import the first 5' }).first().isVisible());
  await shot(page, '3-playlist');

  // 5. A bot check: "Use my browser's sign-in" in a styled dialog, then it works.
  const bot = `https://www.youtube.com/watch?v=${id11('bot-check')}`;
  await field.fill(bot);
  await page.getByRole('button', { name: 'Import', exact: true }).click();
  await page.getByRole('button', { name: /^Activity/ }).first().click();
  const signInBtn = drawer.getByRole('button', { name: "Use my browser's sign-in" }).first();
  await signInBtn.waitFor({ timeout: 45_000 }).catch(() => undefined);
  const attention = (await drawer.innerText().catch(() => '')) || '';
  check('a bot check is explained in plain words', attention.includes("YouTube wants to check that you're not a bot"), attention.slice(0, 200));
  check('…and offers "Use my browser\'s sign-in"', await signInBtn.isVisible());
  await shot(page, '4-bot-check');
  await signInBtn.click();
  const dlg = page.getByRole('dialog', { name: "Use your browser's sign-in" });
  await dlg.waitFor({ timeout: 10_000 });
  check('the sign-in choice is a styled dialog', await dlg.isVisible());
  check('…that says MetaRush never keeps cookies', (await dlg.innerText()).includes('never copies or keeps your cookies'));
  await dlg.getByRole('radio', { name: 'Brave' }).click();
  await shot(page, '5-sign-in-dialog');
  await dlg.getByRole('button', { name: 'Download with Brave' }).click();
  const signed = await waitFor(async () => {
    const list = await api(page, 'asset.list', { unassigned: true, limit: 500 });
    return list.assets.find((a) => a.source?.videoId === bot.split('v=')[1]);
  }, 90_000, 1000);
  check('with the browser sign-in the import works', !!signed && signed.source.signedIn === true);
  const tools = await api(page, 'ingest.url.tools');
  check('the browser is remembered on this machine as a suggestion', tools.browser === 'brave', tools.browser);

  // 5b. "Import the first 5" from the playlist toast: the field empties and the list is imported video by video.
  await page.goto(`${base}/`);
  await field.fill(playlistLink);
  await page.getByRole('button', { name: 'Import', exact: true }).click();
  const firstFive = page.getByRole('button', { name: 'Import the first 5' }).first();
  await firstFive.waitFor({ timeout: 15_000 }).catch(() => undefined);
  await firstFive.click();
  const listToast = page.getByText('Importing the first 5 videos of the playlist').first();
  await listToast.waitFor({ timeout: 15_000 }).catch(() => undefined);
  check('"Import the first 5" starts the import and says so', await listToast.isVisible());
  check('…and the link field is empty again', (await field.inputValue()) === '', await field.inputValue());
  const expanded = await waitFor(async () => {
    const s = await api(page, 'activity.summary');
    return s.items.find((i) => i.category === 'import' && i.url?.includes(`PLfake${uniq}`) && i.state === 'done');
  }, 60_000, 1000);
  check('the playlist is split into one import per video', !!expanded && /Found 3 videos/.test(expanded.stageLabel), expanded?.stageLabel);

  // 6. Settings: the downloader and the extension.
  await page.goto(`${base}/settings/footage#link-imports`);
  const li = page.locator('#link-imports');
  await li.getByText(/Downloader: yt-dlp/).waitFor({ timeout: 15_000 }).catch(() => undefined);
  check('Settings shows the downloader', await li.getByText(/Downloader: yt-dlp 2026\.08\.19/).isVisible());
  await shot(page, '6-settings');
  await page.goto(`${base}/settings/extension`); // its own section (D191)
  const ext = page.locator('#browser-extension');
  await ext.getByText(/browser-extension$/).first().waitFor({ timeout: 15_000 }).catch(() => undefined);
  check('Settings shows the extension folder', (await ext.innerText()).includes('browser-extension'));
  await shot(page, '6-settings-extension');
  const folder = (await api(page, 'extension.status')).folder;
  await browser.close();
  return { folder };
}

async function extension(folder, problems) {
  // A local file server for the "TikTok" video the routed page plays.
  const clip = path.join(mkdtempSync(path.join(tmpdir(), 'mr-ext-')), 'clip.mp4');
  execSync(`ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=320x568:rate=25:duration=2 -f lavfi -i sine=frequency=550:duration=2 -metadata comment=${uniq} -c:v mpeg4 -c:a aac -shortest "${clip}"`);
  const bytes = readFileSync(clip);
  const srv = createServer((req, res) => {
    res.writeHead(200, { 'Content-Type': 'video/mp4', 'Content-Length': bytes.length, 'Access-Control-Allow-Origin': '*' });
    res.end(bytes);
  });
  await new Promise((r) => srv.listen(0, '127.0.0.1', r));
  const mediaURL = `http://localhost:${srv.address().port}/v/clip.mp4`;

  const userDir = mkdtempSync(path.join(tmpdir(), 'mr-ext-profile-'));
  const ctx = await chromium.launchPersistentContext(userDir, {
    headless: true,
    channel: 'chromium',
    args: [`--disable-extensions-except=${folder}`, `--load-extension=${folder}`],
  });
  try {
    let [sw] = ctx.serviceWorkers();
    if (!sw) sw = await ctx.waitForEvent('serviceworker', { timeout: 15_000 });
    const extId = new URL(sw.url()).host;
    check('the extension loads in Chromium', !!extId, extId);

    // Pair from the popup with a code made in the dashboard.
    const dash = await ctx.newPage();
    await dash.goto(mintLink());
    await dash.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
    const code = (await api(dash, 'extension.pair')).code;
    const popup = await ctx.newPage();
    popup.on('pageerror', (e) => problems.push(`popup: ${e.message}`));
    await popup.goto(`chrome-extension://${extId}/popup.html`);
    await popup.getByLabel('Pairing code').waitFor({ timeout: 10_000 });
    await popup.locator('details summary').click();
    await popup.getByLabel('Address').fill(base);
    await popup.getByLabel('Pairing code').fill(code);
    await popup.getByRole('button', { name: 'Pair' }).click();
    await popup.getByText(/Paired with MetaRush on/).waitFor({ timeout: 15_000 }).catch(() => undefined);
    check('the popup pairs with a one-time code', await popup.getByText(/Paired with MetaRush on/).isVisible());
    if (SHOTS) await popup.screenshot({ path: path.join(SHOTS, '7-popup-paired.png') });
    const status = await api(dash, 'extension.status');
    check('the dashboard lists the paired browser', status.browsers.length >= 1, status.browsers.map((b) => b.name).join(', '));

    // A routed "TikTok" page that plays a plain file: "Save from this browser".
    const vid = `7${Date.now()}`.slice(0, 19);
    const pageURL = `https://www.tiktok.com/@fakecreator/video/${vid}`;
    await ctx.route('https://www.tiktok.com/**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'text/html',
        body: `<!doctype html><html><head><title>A test TikTok</title><meta property="og:description" content="A test clip from the extension check"></head>
          <body><video id="v" src="${mediaURL}" width="320" height="568" muted controls></video><span data-e2e="browse-username">Fake Creator</span></body></html>`,
      }),
    );
    const tik = await ctx.newPage();
    tik.on('pageerror', (e) => problems.push(`tiktok page: ${e.message}`));
    await tik.goto(pageURL);
    await tik.evaluate(() => document.getElementById('v').play().catch(() => undefined));
    const send = tik.getByRole('button', { name: 'Send this video to MetaRush' });
    await send.waitFor({ timeout: 15_000 }).catch(() => undefined);
    check('the page gets a "Send to MetaRush" button', await send.isVisible());
    await send.click();
    const saveBtn = tik.getByRole('button', { name: /Save from this browser/ });
    await saveBtn.waitFor({ timeout: 10_000 });
    check('the panel offers "Save from this browser" for a plain file', (await saveBtn.innerText()).includes('with your sign-in'));
    if (SHOTS) await tik.screenshot({ path: path.join(SHOTS, '8-extension-panel.png') });
    await saveBtn.click();
    await tik.getByText(/Sent to MetaRush/).waitFor({ timeout: 30_000 }).catch(() => undefined);
    check('the video is sent to MetaRush', await tik.getByText(/Sent to MetaRush/).isVisible());
    const arrived = await waitFor(async () => {
      const list = await api(dash, 'asset.list', { unassigned: true, limit: 500 });
      return list.assets.find((a) => a.source?.videoId === vid);
    }, 60_000, 1000);
    check('it lands in the inbox with the page as its source', !!arrived && arrived.source.method === 'browser' && arrived.source.handle === '@fakecreator', JSON.stringify(arrived?.source ?? {}));

    // A copy-protected page: refused, never captured.
    const vid2 = `8${Date.now()}`.slice(0, 19);
    const drm = await ctx.newPage();
    await drm.goto(`https://www.tiktok.com/@fakecreator/video/${vid2}`);
    await drm.evaluate(async () => {
      const v = document.getElementById('v');
      const access = await navigator.requestMediaKeySystemAccess('org.w3.clearkey', [{ initDataTypes: ['keyids'], videoCapabilities: [{ contentType: 'video/mp4; codecs="avc1.42E01E"' }] }]);
      await v.setMediaKeys(await access.createMediaKeys());
    });
    await drm.getByRole('button', { name: 'Send this video to MetaRush' }).click();
    const refuse = drm.getByText(/copy-protected \(DRM\)/).first();
    await refuse.waitFor({ timeout: 10_000 }).catch(() => undefined);
    check('copy-protected video is refused', await refuse.isVisible());
    check('…and "Save from this browser" is off', await drm.getByRole('button', { name: /Save from this browser/ }).isDisabled());
    if (SHOTS) await drm.screenshot({ path: path.join(SHOTS, '9-extension-drm.png') });
  } finally {
    await ctx.close();
    srv.close();
    rmSync(userDir, { recursive: true, force: true });
  }
}

async function main() {
  const problems = [];
  const { folder } = await dashboard(problems);
  await extension(folder, problems);
  check('no native dialogs or page errors', problems.length === 0, problems.join(' | '));
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
  if (failed.length) process.exitCode = 1;
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
