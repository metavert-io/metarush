#!/usr/bin/env node
// End-to-end check of resources, the Add box and the Sources "…" menu in the
// dashboard (D79–D84), against a seeded daemon whose downloader is the fake:
//   A  the Add box on a project's Sources tab: empty, then compact; dropping a
//      file anywhere says "Drop to add to <project>" and adds it to that
//      project; a pasted link imports into the project
//   B  dragging Inbox clips onto project cards: a card arms only after a short
//      hover (or with the pointer well inside it), exactly one lights up, the
//      pointer label says "Add to <project>", the toast offers Undo and Move…,
//      and several selected clips go to one project together
//   C  the Sources "…" menu: Move to another project…, Remove from this project
//      (back in the Inbox, with Undo)
//   D  Delete…: refused with where it's used while a cut uses it, greyed out
//      while another project has it, and the styled confirmation otherwise
//   E  Where it's used, for a clip
//   a cut inside a cut from the dashboard: "From another project" → Brand →
//   "Play it at the end" gives fernbrook a first cut that plays Brand's cut
//   and screenshots (desktop and phone, dark and light) of the Resources tab,
//   the brand kit, a linked folder, the project link picker with a subset, the
//   nested cut on the timeline and the Script view with its resource pins.
//
// Seed: the projects "Test project" (a cut with a nested cut from "Brand" and a
// corner logo, a linked folder), "fernbrook" (empty) and "Brand", footage in the
// Inbox, and the brand kit (see docs/resources.md, "Trying it").
//   METARUSH_YTDLP=<fake yt-dlp> bin/metarush --home <test home> serve
//   METARUSH_DASHBOARD=http://127.0.0.1:<port> METARUSH_MINT="bin/metarush --home <test home> dashboard --print-link" \
//   DROP_FILE=<a small unique .mp4> SHOTS_DIR=<dir> node web/scripts/resources-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:4845').replace(/\/$/, '');
if (!process.env.METARUSH_MINT || !process.env.DROP_FILE) {
  console.error('set METARUSH_MINT, METARUSH_DASHBOARD and DROP_FILE (see the header of this script)');
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

async function shot(page, name) {
  await new Promise((r) => setTimeout(r, 350)); // let dialogs and toasts finish appearing
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: false });
}

async function waitFor(fn, ms, every = 500) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > end) return null;
    await new Promise((r) => setTimeout(r, every));
  }
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function newPage(browser, { theme = 'dark', phone = false, tall = false } = {}) {
  const ctx = await browser.newContext({
    viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: tall ? 1500 : 900 },
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
  await page.goto(mintLink());
  await page.waitForURL(`${base}/**`);
  return page;
}

const projectId = async (page, name) => (await api(page, 'project.list', {})).projects.find((p) => p.name === name)?.id;

const browser = await chromium.launch();
try {
  const page = await newPage(browser);
  const test = await projectId(page, 'Test project');
  const fernbrook = await projectId(page, 'fernbrook');
  const brand = await projectId(page, 'Brand');
  check('seeded projects', !!(test && fernbrook && brand));

  /* ---------- A: the Add box on Sources ---------- */
  await page.goto(`${base}/p/${fernbrook}?tab=sources`);
  const box = page.locator('[data-addbox]').first();
  await box.waitFor();
  check('A: an empty project shows the Add box on Sources', (await box.textContent()).includes('Add to fernbrook'));
  check('A: it offers files, a folder, a link and other projects',
    (await box.getByRole('button', { name: 'Choose files' }).count()) === 1 && (await box.getByRole('button', { name: 'Choose a folder' }).count()) === 1 &&
      (await box.getByRole('button', { name: 'From another project' }).count()) === 1 && (await box.locator('form[aria-label="Import from a link"]').count()) === 1);
  await shot(page, 'A-sources-empty-desktop-dark');

  // Drag a file over the page: the overlay says where it goes; dropping adds it to fernbrook.
  const bytes = readFileSync(process.env.DROP_FILE).toString('base64');
  const dropName = path.basename(process.env.DROP_FILE);
  await page.evaluate(([b64, name]) => {
    const raw = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    const dt = new DataTransfer();
    dt.items.add(new File([raw], name, { type: 'video/mp4' }));
    window.__mrDrop = dt;
    window.dispatchEvent(new DragEvent('dragenter', { dataTransfer: dt, bubbles: true, cancelable: true }));
    window.dispatchEvent(new DragEvent('dragover', { dataTransfer: dt, bubbles: true, cancelable: true }));
  }, [bytes, dropName]);
  const overlay = page.getByTestId('drop-title');
  await overlay.waitFor({ timeout: 5000 });
  check('A: dragging over the project says "Drop to add to fernbrook"', (await overlay.textContent()).trim() === 'Drop to add to fernbrook');
  await shot(page, 'A-drop-overlay-desktop-dark');
  await page.evaluate(() => window.dispatchEvent(new DragEvent('drop', { dataTransfer: window.__mrDrop, bubbles: true, cancelable: true })));
  const dropped = await waitFor(async () => (await api(page, 'project.show', { id: fernbrook })).assets.find((a) => a.name === dropName), 60000, 1000);
  check('A: the dropped file is in fernbrook', !!dropped);

  // A pasted link imports into this project (the box's link field; ⌘V anywhere does the same).
  const vid = `ccres${Date.now().toString(36).slice(-6)}`.slice(0, 11);
  const link = page.locator('[data-addbox] form[aria-label="Import from a link"] input').first();
  await link.fill(`https://www.youtube.com/watch?v=${vid}`);
  await link.press('Enter');
  const imported = await waitFor(async () => (await api(page, 'project.show', { id: fernbrook })).assets.find((a) => a.source?.url?.includes(vid)), 90000, 1500);
  check('A: a pasted link imports into fernbrook', !!imported, imported ? imported.title : 'not imported');
  const vid2 = `ccrep${Date.now().toString(36).slice(-6)}`.slice(0, 11);
  await page.locator('body').click({ position: { x: 5, y: 300 } });
  await page.evaluate((u) => {
    const dt = new DataTransfer();
    dt.setData('text/plain', u);
    document.body.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
  }, `https://www.youtube.com/watch?v=${vid2}`);
  const pasted = await waitFor(async () => (await api(page, 'project.show', { id: fernbrook })).assets.find((a) => a.source?.url?.includes(vid2)), 90000, 1500);
  check('A: ⌘V anywhere on the project imports into it too', !!pasted);
  await page.reload();
  await page.locator('[data-addbox]').first().waitFor();
  check('A: with sources the Add box is a compact bar', (await page.locator('[data-addbox]').first().textContent()).includes('Drop files or folders anywhere here'));
  await shot(page, 'A-sources-compact-desktop-dark');

  /* ---------- B: safe drags onto projects ---------- */
  // A tall window, so the project cards and the Inbox are both on screen.
  const home = await newPage(browser, { tall: true });
  await home.goto(`${base}/`);
  {
  const page = home;
  const cards = page.locator('[data-drop-project]');
  await cards.first().waitFor();
  const inboxItems = page.locator('#inbox [role="listitem"]');
  await inboxItems.first().waitFor();
  const inboxBefore = await inboxItems.count();
  const cardA = page.locator(`[data-drop-project="${test}"]`);
  const cardB = page.locator(`[data-drop-project="${fernbrook}"]`);
  const a = await cardA.boundingBox();
  const b = await cardB.boundingBox();
  const item = inboxItems.first();
  const title = (await item.locator('[data-testid="asset-title"], h3, button').first().textContent())?.trim();
  const grip = await item.locator('[draggable="true"]').first().boundingBox();
  await page.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2);
  await page.mouse.down();
  await page.mouse.move(grip.x + grip.width / 2 + 20, grip.y + grip.height / 2 + 20, { steps: 4 });
  // Brush the edge of the first card on the way: it must not light up.
  await page.mouse.move(a.x + a.width - 6, a.y + a.height / 2, { steps: 8 });
  await sleep(80);
  const brushed = await page.locator('[data-drop-armed]').count();
  // Settle well inside the second card.
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 8 });
  await sleep(350);
  await page.mouse.move(b.x + b.width / 2 + 2, b.y + b.height / 2 + 2, { steps: 2 });
  await sleep(120);
  const armed = await page.locator('[data-drop-armed]').evaluateAll((els) => els.map((e) => e.getAttribute('data-drop-project')));
  const label = await page.getByTestId('drag-label').textContent().catch(() => '');
  check('B: brushing past a card does not arm it', brushed === 0, `armed while brushing: ${brushed}`);
  check('B: exactly one card lights up, the one under the pointer', armed.length === 1 && armed[0] === fernbrook, JSON.stringify(armed));
  check('B: the pointer says "Add to fernbrook"', label.trim() === 'Add to fernbrook', label);
  await shot(page, 'B-drag-armed-desktop-dark');
  await page.mouse.up();
  const toast = page.getByRole('status').filter({ hasText: 'to fernbrook' }).last();
  await toast.waitFor({ timeout: 8000 });
  check('B: the toast says where it went, with Undo and Move…',
    (await toast.textContent()).includes('Added') && (await toast.getByRole('button', { name: 'Undo' }).count()) === 1 && (await toast.getByRole('button', { name: 'Move…' }).count()) === 1,
    (await toast.textContent()).slice(0, 80));
  await shot(page, 'B-toast-desktop-dark');
  await toast.getByRole('button', { name: 'Undo' }).click();
  const undone = await waitFor(async () => (await api(page, 'asset.list', { unassigned: true, limit: 500 })).assets.filter((x) => x.kind !== 'image').length === inboxBefore, 8000);
  check('B: Undo puts it back in the Inbox', !!undone);

  // Drop again, then Move… to another project with the picker.
  const dragTo = async (card) => {
    const it = page.locator('#inbox [role="listitem"]').first();
    const g = await it.locator('[draggable="true"]').first().boundingBox();
    const c = await card.boundingBox();
    await page.mouse.move(g.x + g.width / 2, g.y + g.height / 2);
    await page.mouse.down();
    await page.mouse.move(g.x + g.width / 2 + 15, g.y + g.height / 2 + 15, { steps: 4 });
    await page.mouse.move(c.x + c.width / 2, c.y + c.height / 2, { steps: 10 });
    await sleep(320);
    await page.mouse.move(c.x + c.width / 2 + 2, c.y + c.height / 2 + 1, { steps: 2 });
    await sleep(100);
    await page.mouse.up();
  };
  const countOf = async (id) => (await api(page, 'project.show', { id })).assets.filter((x) => !x.from).length;
  const [testBefore, fernbrookBefore] = [await countOf(test), await countOf(fernbrook)];
  await dragTo(cardA);
  const toast2 = page.getByRole('status').filter({ hasText: 'to Test project' }).last();
  await toast2.waitFor({ timeout: 8000 });
  const added = await waitFor(async () => (await countOf(test)) === testBefore + 1, 5000);
  await toast2.getByRole('button', { name: 'Move…' }).click();
  const picker = page.locator('input[role="combobox"]');
  await picker.waitFor();
  await picker.fill('fer');
  await shot(page, 'B-move-picker-desktop-dark');
  await picker.press('Enter');
  const moved = await waitFor(async () => (await countOf(test)) === testBefore && (await countOf(fernbrook)) === fernbrookBefore + 1, 10000);
  check('B: Move… sends it to the project picked by typing', !!added && !!moved, `test ${testBefore}→${await countOf(test)}, fernbrook ${fernbrookBefore}→${await countOf(fernbrook)}`);

  // Several selected clips go to one project together.
  await page.reload();
  await page.locator('#inbox [role="listitem"]').first().waitFor();
  const boxes = page.locator('#inbox [role="checkbox"]');
  const n = await boxes.count();
  if (n >= 2) {
    await page.locator('#inbox [role="listitem"]').nth(0).hover();
    await boxes.nth(0).click();
    await boxes.nth(1).click();
    const two = (await api(page, 'asset.list', { unassigned: true, limit: 500 })).assets.filter((x) => x.kind !== 'image').length;
    await dragTo(cardB);
    const both = await waitFor(async () => (await api(page, 'asset.list', { unassigned: true, limit: 500 })).assets.filter((x) => x.kind !== 'image').length === two - 2, 10000);
    check('B: two selected clips land in one project together', !!both);
  } else {
    check('B: two selected clips land in one project together', false, `only ${n} Inbox clip(s) to select`);
  }
  await home.context().close();
  }

  /* ---------- C, D, E: the Sources "…" menu ---------- */
  await page.goto(`${base}/p/${test}?tab=sources`);
  await page.locator('[role="listitem"]').first().waitFor();
  const menuFor = (t) => page.getByRole('button', { name: `More for ${t}` }).first();
  // E: where a clip the cut uses is used.
  const used = (await api(page, 'project.show', { id: test })).assets.find((x) => x.name === 'interview-a.mov');
  await menuFor(used.title).click();
  await shot(page, 'C-menu-desktop-dark');
  await page.getByRole('menuitem', { name: "Where it's used" }).click();
  const where = page.getByRole('dialog');
  await where.getByRole('list', { name: "Where it's used" }).waitFor();
  const whereText = await where.textContent();
  check("E: Where it's used lists the project and the cut's version", whereText.includes('In Test project') && /Used in Test project's cut \(version \d/.test(whereText), whereText.slice(0, 160));
  await shot(page, 'E-where-desktop-dark');
  await page.keyboard.press('Escape');
  // D: refused while the cut uses it, saying where.
  await menuFor(used.title).click();
  await page.getByRole('menuitem', { name: 'Delete…' }).click();
  const refusal = page.getByRole('dialog');
  await refusal.waitFor();
  const refusalText = await refusal.textContent();
  check('D: Delete… is refused while a cut uses it, saying where', refusalText.includes("can't be deleted yet") && /Used in Test project's cut \(version \d.*remove it from the cut first/.test(refusalText), refusalText.slice(0, 200));
  await shot(page, 'D-refused-desktop-dark');
  await refusal.getByRole('button', { name: 'OK' }).click();
  // C: Move to another project… (the menu, keyboard-accessible).
  const broll = (await api(page, 'project.show', { id: test })).assets.find((x) => x.name === 'broll-c.mp4');
  await menuFor(broll.title).focus();
  await page.keyboard.press('Enter');
  await page.getByRole('menuitem', { name: 'Move to another project…' }).waitFor();
  await page.keyboard.press('Enter');
  const mp = page.locator('input[role="combobox"]');
  await mp.waitFor();
  await mp.fill('fernbrook');
  await mp.press('Enter');
  const movedBroll = await waitFor(async () => (await api(page, 'project.show', { id: fernbrook })).assets.some((x) => x.id === broll.id), 8000);
  check('C: Move to another project… (keyboard) moves it', !!movedBroll);
  // D: greyed out while another project has it (it is in fernbrook now; add it back here too).
  await api(page, 'asset.link', { assetId: broll.id, projectId: test });
  await page.reload();
  await page.locator('[role="listitem"]').first().waitFor();
  await menuFor(broll.title).click();
  const del = page.getByRole('menuitem', { name: 'Delete…' });
  check('D: Delete… is greyed out while another project has it', (await del.getAttribute('aria-disabled')) === 'true' && ((await del.getAttribute('title')) ?? '').includes('Also in fernbrook'), await del.getAttribute('title'));
  await shot(page, 'D-greyed-desktop-dark');
  await page.keyboard.press('Escape');
  // C: Remove from this project: back in the Inbox (it is still in fernbrook, so it stays there), with Undo.
  await menuFor(broll.title).click();
  await page.getByRole('menuitem', { name: 'Remove from this project' }).click();
  const rmToast = page.getByRole('status').filter({ hasText: 'from Test project' }).last();
  await rmToast.waitFor({ timeout: 8000 });
  check('C: Remove from this project says so, with Undo', (await rmToast.textContent()).includes('Removed') && (await rmToast.getByRole('button', { name: 'Undo' }).count()) === 1);
  await rmToast.getByRole('button', { name: 'Undo' }).click();
  const back = await waitFor(async () => (await api(page, 'project.show', { id: test })).assets.some((x) => x.id === broll.id), 8000);
  check('C: Undo puts it back', !!back);
  // C+D in fernbrook: the dropped file is only in fernbrook → Remove sends it to the Inbox; Delete… removes it for good.
  await page.goto(`${base}/p/${fernbrook}?tab=sources`);
  await page.locator('[role="listitem"]').first().waitFor();
  const droppedNow = (await api(page, 'project.show', { id: fernbrook })).assets.find((x) => x.name === dropName);
  await menuFor(droppedNow.title).click();
  await page.getByRole('menuitem', { name: 'Remove from this project' }).click();
  const inboxToast = page.getByRole('status').filter({ hasText: "back in the Inbox" }).last();
  check('C: removing a clip no project has says it is back in the Inbox', await inboxToast.waitFor({ timeout: 8000 }).then(() => true, () => false));
  await api(page, 'asset.link', { assetId: droppedNow.id, projectId: fernbrook });
  await page.reload();
  await page.locator('[role="listitem"]').first().waitFor();
  await menuFor(droppedNow.title).click();
  await page.getByRole('menuitem', { name: 'Delete…' }).click();
  const confirm = page.getByRole('alertdialog').or(page.getByRole('dialog')).first();
  await confirm.waitFor();
  const confirmText = await confirm.textContent();
  check('D: the styled confirmation says exactly what goes',
    confirmText.includes(`Permanently delete “${droppedNow.title}”?`) &&
      confirmText.includes("This removes the video, its preview, transcript and speaker data from storage for every machine. It can't be undone."),
    confirmText.slice(0, 220));
  await shot(page, 'D-confirm-desktop-dark');
  await confirm.getByRole('button', { name: 'Delete forever' }).click();
  const gone = await waitFor(async () => {
    try {
      await api(page, 'asset.show', { id: droppedNow.id });
      return false;
    } catch {
      return true;
    }
  }, 8000);
  check('D: the clip is deleted for good', !!gone);

  /* ---------- a cut inside a cut, from the dashboard: fernbrook plays Brand's cut ---------- */
  await page.goto(`${base}/p/${fernbrook}?tab=sources`);
  await page.locator('[data-addbox]').first().getByRole('button', { name: 'From another project' }).click();
  const pickBrand = page.locator('input[role="combobox"]');
  await pickBrand.waitFor();
  await pickBrand.pressSequentially('Brand', { delay: 40 });
  await sleep(200);
  await pickBrand.press('Enter');
  const playIt = page.getByRole('button', { name: 'Play it at the end' });
  const offered = await playIt.waitFor({ timeout: 15000 }).then(() => true, () => false);
  check("Nest: Brand's cut is offered with its version", offered && (await page.getByRole('region', { name: "Brand's cut" }).textContent()).includes('version'));
  if (offered) {
    await shot(page, 'nest-offer-desktop-dark');
    await playIt.click();
    const nestToast = page.getByRole('status').filter({ hasText: "Brand's cut plays in fernbrook now" }).last();
    check("Nest: the toast says Brand's cut plays in fernbrook", await nestToast.waitFor({ timeout: 10000 }).then(() => true, () => false));
    await page.goto(`${base}/p/${fernbrook}?tab=cut`);
    const block = page.locator('[data-nested]').first();
    check("Nest: fernbrook's timeline shows Brand's cut as one block", await block.waitFor({ timeout: 15000 }).then(() => true, () => false));
    await sleep(400);
    await shot(page, 'nest-timeline-desktop-dark');
  }

  /* ---------- screens: desktop and phone, dark and light ---------- */
  const screens = [
    ['resources-tab', `/p/${test}?tab=resources`, '[aria-label="Resources"]'],
    ['brand-kit', '/settings/brand', '[data-testid="brand-preview"]'],
    ['sources-addbox', `/p/${fernbrook}?tab=sources`, '[data-addbox]'],
    ['nested-timeline', `/p/${test}?tab=cut`, '[data-nested]'],
    ['script-pins', `/p/${test}?tab=script`, '[data-script-view]'],
    ['home-addbox', '/', '[data-addbox]'],
  ];
  for (const theme of ['dark', 'light']) {
    for (const phone of [false, true]) {
      const p = await newPage(browser, { theme, phone });
      const tag = `${phone ? 'phone' : 'desktop'}-${theme}`;
      for (const [name, url, sel] of screens) {
        await p.goto(`${base}${url}`);
        const ok = await p.locator(sel).first().waitFor({ timeout: 15000 }).then(() => true, () => false);
        if (name === 'nested-timeline' && ok) await p.locator('[data-nested] [role="button"]').first().click().catch(() => {});
        if (name === 'script-pins' && ok) {
          const txt = await p.locator(sel).first().textContent();
          if (theme === 'dark' && !phone)
            check('Script view shows the resource and cut pins, and the nested clip', txt.includes('Logos, pictures and fonts') && txt.includes('Cuts from other projects') && txt.includes('A cut from another project'), txt.slice(0, 80));
        }
        if (theme === 'dark' && !phone) check(`screen ${name} renders`, ok);
        await sleep(400);
        await shot(p, `${name}-${tag}`);
        if (name === 'resources-tab' && ok) {
          // The linked folder and a resource's details.
          await p.locator('[aria-label="Linked folders"]').first().scrollIntoViewIfNeeded().catch(() => {});
          await shot(p, `linked-folder-${tag}`);
          await p.locator('[aria-label="Resources"] button').first().click();
          await p.getByRole('dialog').waitFor();
          await sleep(300);
          await shot(p, `resource-details-${tag}`);
          await p.keyboard.press('Escape');
          // The project link picker with a subset.
          await p.getByRole('button', { name: 'From another project' }).first().click();
          const pick = p.locator('input[role="combobox"]');
          await pick.waitFor();
          await pick.pressSequentially('fernbrook', { delay: 40 });
          await sleep(200);
          await pick.press('Enter');
          const some = p.getByRole('radio', { name: /Only some of it/ });
          if (!(await some.waitFor({ timeout: 15000 }).then(() => true, () => false))) {
            await shot(p, `project-link-FAILED-${tag}`);
            check('the project link picker opens for fernbrook', false, tag);
            continue;
          }
          await some.click();
          await sleep(300);
          await shot(p, `project-link-subset-${tag}`);
          if (theme === 'dark' && !phone) check('the project link picker offers everything (default), a subset, or picked items', (await p.getByRole('radio').count()) === 3);
          await p.keyboard.press('Escape');
        }
        if (name === 'sources-addbox' && ok) {
          // D and E on this device too.
          const t2 = (await api(p, 'project.show', { id: test })).assets.find((x) => x.name === 'interview-a.mov');
          await p.goto(`${base}/p/${test}?tab=sources`);
          await p.locator('[role="listitem"]').first().waitFor();
          await p.getByRole('button', { name: `More for ${t2.title}` }).first().click();
          await shot(p, `C-menu-${tag}`);
          await p.getByRole('menuitem', { name: "Where it's used" }).click();
          await p.getByRole('dialog').getByRole('list', { name: "Where it's used" }).waitFor();
          await shot(p, `E-where-${tag}`);
          await p.keyboard.press('Escape');
          await p.getByRole('button', { name: `More for ${t2.title}` }).first().click();
          await p.getByRole('menuitem', { name: 'Delete…' }).click();
          await p.getByRole('dialog').waitFor();
          await shot(p, `D-refused-${tag}`);
          await p.getByRole('button', { name: 'OK' }).click();
        }
      }
      await p.context().close();
    }
  }
} finally {
  await browser.close();
}
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
process.exit(failed.length ? 1 : 0);
