#!/usr/bin/env node
// End-to-end check of the Library (D96–D98) in the dashboard, against a seeded
// throwaway daemon (scripts/library-e2e.sh does all of it):
//   A  the Library's views with counts (Inbox, Collections, Archived, In projects, Trash)
//   B  the "…" menu: Add to collection…, Archive, Move to Trash, Delete…; Archive
//      and Take out of the Archive (with Undo in the toast)
//   C  the collection picker: type a new name → "Create “B-roll”"; an existing one
//      by typing part of it; the collection's shelf and its own view
//   D  "Use in a project…": the project sees everything on the collection
//   E  several selected Inbox clips go to the Trash together; Restore
//   F  Empty Trash… and Delete forever…: the styled warning says how much goes
//      and that it can't be undone; Cancel keeps everything
//   G  Move to Trash is refused for a clip the project's cut uses (it says why)
//   H  a search covers every view, the Trash included
//   I  Settings → Storage → Trash: turning on "Empty the Trash after 30 days" asks first
//   K  deleting a project (D196): "Delete project…" in its "…" menu asks first and
//      moves it to Archived projects (Home links there); a link to it says it is
//      archived, with Restore; Delete for good warns with what goes (versions)
//      and what moves (its footage back to the Inbox), Cancel focused; Clear out
//      archived projects takes them all
// plus screenshots of each on desktop and phone, dark and light. No native
// browser dialog may appear.
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5112 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   LIBRARY_PROJECT=prj_… SHOTS_DIR=<dir> node web/scripts/library-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5112').replace(/\/$/, '');
const project = process.env.LIBRARY_PROJECT;
if (!process.env.METARUSH_MINT || !project) {
  console.error('set METARUSH_MINT, METARUSH_DASHBOARD and LIBRARY_PROJECT (see the header of this script)');
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

async function settle(page) {
  await page.waitForFunction(() => [...document.images].every((i) => i.complete), null, { timeout: 10_000 }).catch(() => undefined);
  await sleep(350); // dialogs, menus and toasts finish appearing
}

async function shot(page, name) {
  await settle(page);
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: false });
}

async function waitFor(fn, ms, every = 400) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > end) return null;
    await sleep(every);
  }
}

async function newPage(browser, { theme = 'dark', phone = false } = {}) {
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
  await page.goto(mintLink());
  await page.waitForURL(`${base}/**`);
  return page;
}

const lib = (page, input) => api(page, 'library.list', input);
const tab = (page, name) => page.getByRole('tab', { name: new RegExp(`^${name}`) });
const menuFor = (page, title) => page.getByRole('button', { name: `More for ${title}` }).first();
const toastWith = (page, text) => page.getByRole('status').filter({ hasText: text }).last();
async function openLibrary(page, query = '') {
  await page.goto(`${base}/library${query}`);
  await page.getByRole('tablist', { name: 'Library views' }).waitFor();
  await settle(page);
}

const browser = await chromium.launch();
try {
  const page = await newPage(browser);
  const before = await lib(page, {});
  const inbox = before.items.map((i) => i.title).sort();
  check('seeded: four clips wait in the Inbox', inbox.length === 4, inbox.join(', '));
  const [first, second, third, fourth] = ['Waves at dawn', 'Kitchen chat', 'Harbour pan', 'Blurry take'];

  /* ---------- A: the views, with counts ---------- */
  await openLibrary(page);
  const tabsText = await page.getByRole('tablist', { name: 'Library views' }).textContent();
  check('A: the views are Inbox, Collections, Archived, In projects and Trash, with counts', /Inbox\s*4/.test(tabsText) && /Collections\s*0/.test(tabsText) && /Archived\s*0/.test(tabsText) && /In projects\s*1/.test(tabsText) && /Trash\s*0/.test(tabsText), tabsText);
  check('A: the Library is in the top bar', (await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Library' }).count()) === 1);
  await shot(page, 'A-inbox-desktop-dark');

  /* ---------- B: the "…" menu, Archive and back ---------- */
  await menuFor(page, first).click();
  const items = await page.getByRole('menuitem').allTextContents();
  check('B: the menu offers Add to collection…, Archive, Move to Trash and Delete…',
    ['Add to collection…', 'Archive', 'Move to Trash', 'Delete…'].every((l) => items.some((t) => t.startsWith(l))), items.join(' | '));
  await shot(page, 'B-menu-desktop-dark');
  await page.getByRole('menuitem', { name: /^Archive/ }).click();
  const archived = toastWith(page, `Archived “${first}”.`);
  check('B: Archive says so, with Undo', await archived.waitFor({ timeout: 8000 }).then(async () => (await archived.getByRole('button', { name: 'Undo' }).count()) === 1, () => false));
  const counted = await waitFor(async () => /Archived\s*1/.test(await page.getByRole('tablist', { name: 'Library views' }).textContent()), 8000);
  check('B: it leaves the Inbox for the Archive', !!counted && (await lib(page, {})).items.every((i) => i.title !== first));
  await tab(page, 'Archived').click();
  await page.getByRole('button', { name: `More for ${first}` }).first().waitFor();
  await shot(page, 'B-archived-desktop-dark');
  await menuFor(page, first).click();
  await page.getByRole('menuitem', { name: /^Take out of the Archive/ }).click();
  check('B: Take out of the Archive puts it back in the Inbox',
    !!(await waitFor(async () => (await lib(page, {})).items.some((i) => i.title === first), 8000)));

  /* ---------- C: the collection picker ---------- */
  await openLibrary(page);
  await menuFor(page, second).click();
  await page.getByRole('menuitem', { name: /^Add to collection/ }).click();
  const picker = page.getByRole('combobox');
  await picker.waitFor();
  await picker.fill('B-roll');
  const create = page.getByRole('option', { name: /Create “B-roll”/ });
  check('C: typing a new name offers to create it', (await create.count()) === 1);
  await shot(page, 'C-picker-create-desktop-dark');
  await picker.press('Enter');
  const added = toastWith(page, 'Created the B-roll collection.');
  check('C: Enter creates the collection and puts the clip on it', await added.waitFor({ timeout: 8000 }).then(() => true, () => false));
  await menuFor(page, first).click();
  await page.getByRole('menuitem', { name: /^Add to collection/ }).click();
  await page.getByRole('combobox').fill('b-ro');
  const existing = page.getByRole('option', { name: /^B-roll/ }).first();
  check('C: typing part of a name finds the collection', await existing.waitFor({ timeout: 8000 }).then(() => true, () => false));
  await page.getByRole('combobox').fill('b-roll');
  check('C: its whole name offers no second one to create', (await page.getByRole('option', { name: /Create/ }).count()) === 0);
  await existing.click();
  check('C: it is put on the existing collection', await toastWith(page, 'Added “Waves at dawn” to B-roll.').waitFor({ timeout: 8000 }).then(() => true, () => false));
  await tab(page, 'Collections').click();
  const shelf = page.locator('[data-collection="B-roll"]');
  await shelf.waitFor();
  check('C: the collection is a shelf with its count', ((await shelf.textContent()) ?? '').includes('2 clips'));
  await shot(page, 'C-shelves-desktop-dark');
  await shelf.click();
  await page.locator('[data-collection-header]').waitFor();
  check('C: its own view lists its clips', (await page.getByRole('button', { name: `More for ${second}` }).count()) === 1 && (await page.getByRole('button', { name: `More for ${first}` }).count()) === 1);
  await shot(page, 'C-collection-desktop-dark');

  /* ---------- D: a project uses everything on the collection ---------- */
  await page.getByRole('button', { name: 'Use in a project…' }).click();
  const pp = page.getByRole('combobox');
  await pp.waitFor();
  await pp.fill('Spring');
  await page.getByRole('option', { name: /Spring promo/ }).first().waitFor();
  await pp.press('Enter');
  check('D: the project uses the collection', await toastWith(page, 'uses everything on the B-roll collection').waitFor({ timeout: 8000 }).then(() => true, () => false));
  const shown = await api(page, 'project.show', { id: project });
  const fromShelf = shown.assets.filter((a) => a.fromCollection && a.from === 'B-roll').map((a) => a.title).sort();
  check('D: the project sees both clips, from B-roll (nothing copied)', fromShelf.length === 2, fromShelf.join(', '));
  await page.goto(`${base}/p/${project}?tab=sources`);
  await page.getByText('from B-roll').first().waitFor({ timeout: 10_000 }).catch(() => undefined);
  check('D: its Sources say “from B-roll”', (await page.getByText('from B-roll').count()) >= 1);
  await shot(page, 'D-project-sources-desktop-dark');

  /* ---------- G: a clip the cut uses can't go to the Trash ---------- */
  const used = shown.assets.find((a) => !a.from);
  await menuFor(page, used.title).click();
  await page.getByRole('menuitem', { name: /^Move to Trash/ }).click();
  const refused = page.getByRole('alert').filter({ hasText: 'uses it' }).last();
  check('G: Move to Trash is refused while the cut uses the clip, saying why', await refused.waitFor({ timeout: 8000 }).then(() => true, () => false));
  await shot(page, 'G-refused-desktop-dark');

  /* ---------- E: several at once to the Trash; Restore ---------- */
  await openLibrary(page);
  await page.getByRole('checkbox', { name: `Select ${third}` }).click();
  await page.getByRole('checkbox', { name: `Select ${fourth}` }).click();
  const bar = page.getByRole('region', { name: 'Selected clips' });
  await bar.waitFor();
  const barText = await bar.textContent();
  check('E: selected clips can be added to a collection, archived or moved to the Trash together',
    ['Add to project…', 'Add to collection…', 'Archive', 'Move to Trash'].every((l) => barText.includes(l)), barText);
  await shot(page, 'E-selected-desktop-dark');
  await bar.getByRole('button', { name: 'Move to Trash' }).click();
  check('E: both go to the Trash at once', await toastWith(page, 'Moved 2 clips to the Trash.').waitFor({ timeout: 8000 }).then(() => true, () => false));
  await tab(page, 'Trash').click();
  await page.locator('[data-trash-header]').filter({ hasText: 'in the Trash' }).waitFor();
  const header = await page.locator('[data-trash-header]').textContent();
  check('E: the Trash says how much is there and that nothing is deleted on its own', header.includes('2 clips in the Trash') && header.includes('Never emptied on its own'), header);
  check('E: each clip says where it came from', ((await page.locator('[data-where]').first().textContent()) ?? '').includes('From the Inbox'));
  await shot(page, 'E-trash-desktop-dark');
  await page.getByRole('listitem').filter({ hasText: fourth }).getByRole('button', { name: 'Restore' }).click();
  check('E: Restore puts one back', !!(await waitFor(async () => (await lib(page, { view: 'trash' })).total === 1, 8000)));
  check('E: back in the Inbox', (await lib(page, {})).items.some((i) => i.title === fourth));

  /* ---------- F: the permanent-delete warning ---------- */
  await openLibrary(page, '?view=trash');
  await page.getByRole('button', { name: 'Empty Trash…' }).click();
  const warn = page.getByRole('dialog');
  await warn.waitFor();
  const warnText = await warn.textContent();
  check('F: Empty Trash… asks first, with the size, and says it can\'t be undone', /Permanently delete “.+” \(\d+(\.\d)? (KB|MB)\)\?/.test(warnText) && warnText.includes("This can't be undone."), warnText.slice(0, 200));
  check('F: Cancel has the focus (Enter does not delete)', await page.evaluate(() => document.activeElement?.textContent?.trim() === 'Cancel'));
  await shot(page, 'F-warning-desktop-dark');
  await warn.getByRole('button', { name: 'Cancel' }).click();
  check('F: Cancel keeps it', (await lib(page, { view: 'trash' })).total === 1);

  /* ---------- J: the Inbox on Home files clips the same way ---------- */
  await page.goto(`${base}/`);
  const home = page.locator('#inbox');
  await home.waitFor();
  const homeClip = (await lib(page, {})).items[0];
  await home.getByRole('button', { name: `More for ${homeClip.title}` }).click();
  const homeItems = await page.getByRole('menuitem').allTextContents();
  check('J: the Inbox on Home has the same menu (Add to collection…, Archive, Move to Trash)',
    ['Add to collection…', 'Archive', 'Move to Trash'].every((l) => homeItems.some((t) => t.startsWith(l))), homeItems.join(' | '));
  await page.keyboard.press('Escape');
  await home.getByRole('checkbox', { name: `Select ${homeClip.title}` }).click();
  const homeBar = page.getByRole('region', { name: 'Selected clips' });
  await homeBar.waitFor();
  const homeBarText = await homeBar.textContent();
  check('J: selected Inbox clips can be put on a collection, archived or moved to the Trash', ['Add to collection…', 'Archive', 'Move to Trash'].every((l) => homeBarText.includes(l)), homeBarText);
  check('J: the Inbox links to the Library', (await home.getByRole('link', { name: 'Library' }).count()) === 1);
  await shot(page, 'J-home-inbox-desktop-dark');
  await homeBar.getByRole('button', { name: /Clear/ }).click();

  /* ---------- H: one search across every view ---------- */
  await openLibrary(page);
  await page.locator('#library-search').fill('harbour');
  const allTab = await waitFor(async () => ((await tab(page, 'All').count()) === 1 ? tab(page, 'All') : null), 8000);
  // The All tab shows at once; its counts arrive with the search's answer.
  const searchCounted = await waitFor(async () => /Trash\s*1/.test((await page.getByRole('tablist', { name: 'Library views' }).textContent()) ?? ''), 8000);
  check('H: a search shows All, with matches counted in each view', !!allTab && !!searchCounted);
  check('H: it finds the clip in the Trash', await page.locator('[data-summary]').filter({ hasText: '1 in the Trash' }).waitFor({ timeout: 8000 }).then(() => true, () => false));
  await shot(page, 'H-search-desktop-dark');

  /* ---------- I: Settings → Storage → Trash ---------- */
  await page.goto(`${base}/settings/storage#trash`);
  const card = page.locator('#trash');
  await card.waitFor();
  await card.getByRole('radio', { name: 'After 30 days' }).click();
  const ask = page.getByRole('alertdialog').or(page.getByRole('dialog')).first();
  await ask.waitFor();
  check('I: turning on emptying asks first', ((await ask.textContent()) ?? '').includes('deleted for good 30 days after'));
  await shot(page, 'I-settings-confirm-desktop-dark');
  await ask.getByRole('button', { name: 'Turn on' }).click();
  check('I: it is on, for the whole workspace', !!(await waitFor(async () => ((await card.locator('[data-trash-setting]').textContent()) ?? '').includes('30 days after'), 8000)));
  await shot(page, 'I-settings-on-desktop-dark');
  await openLibrary(page, '?view=trash');
  check('I: the Trash says when it empties itself', ((await page.locator('[data-trash-header]').textContent()) ?? '').includes('Emptied automatically after 30 days'));
  await page.goto(`${base}/settings/storage#trash`);
  await page.locator('#trash').getByRole('radio', { name: 'Never on its own' }).click();
  check('I: turning it off again', !!(await waitFor(async () => (await api(page, 'trash.settings', {})).emptyAfterDays === 0, 8000)));

  /* ---------- J: when each clip was added, readable at a glance (D188) ---------- */
  // checkDates sees an "Added Today 3:04 PM" on every tile of the grid and every
  // row of the list, the exact date on hover, on any screen width.
  async function checkDates(p, tag) {
    await openLibrary(p);
    await p.getByRole('radio', { name: 'Grid with large thumbnails' }).click();
    await settle(p);
    const tiles = p.getByRole('list', { name: /./ }).first().getByRole('listitem');
    const n = await tiles.count();
    const grid = await p.locator('[role="listitem"] [data-stamp]').evaluateAll((els) => els.filter((e) => e.offsetParent !== null).map((e) => ({ text: e.textContent?.trim() ?? '', title: e.getAttribute('title') ?? '' })));
    check(`J ${tag}: every grid tile says when it was added`, n > 0 && grid.length >= n && grid.every((d) => /^Added (Today|Yesterday|\w{3}, )/.test(d.text) && /\d{4}/.test(d.title)), JSON.stringify(grid.slice(0, 2)));
    await shot(p, `J-grid-dates-${tag}`);
    await p.getByRole('radio', { name: 'Compact list' }).click();
    await settle(p);
    const list = await p.locator('[role="listitem"] [data-stamp]').evaluateAll((els) => els.filter((e) => e.offsetParent !== null).map((e) => e.textContent?.trim() ?? ''));
    check(`J ${tag}: every list row shows its added date`, list.length >= n && list.every((t) => /(Today|Yesterday|\w{3}, )/.test(t)), JSON.stringify(list.slice(0, 2)));
    await shot(p, `J-list-dates-${tag}`);
    if (tag.startsWith('phone')) {
      const scrollW = await p.evaluate(() => document.documentElement.scrollWidth);
      check(`J ${tag}: the list's dates fit without sideways scrolling`, scrollW <= 392, `scrollWidth ${scrollW}`);
    }
    await p.getByRole('radio', { name: 'Grid with large thumbnails' }).click();
  }
  await checkDates(page, 'desktop-dark');

  /* ---------- screenshots: desktop light, phone dark and light ---------- */
  for (const [theme, phone] of [['light', false], ['dark', true], ['light', true]]) {
    const tag = `${phone ? 'phone' : 'desktop'}-${theme}`;
    const p = await newPage(browser, { theme, phone });
    await checkDates(p, tag);
    await openLibrary(p);
    await shot(p, `A-inbox-${tag}`);
    await menuFor(p, (await lib(p, {})).items[0].title).click();
    if (phone) {
      const box = await p.getByRole('menu').boundingBox();
      check(`phone ${theme}: the "…" menu stays on the screen`, !!box && box.x >= 0 && box.x + box.width <= 390, JSON.stringify(box));
    }
    await shot(p, `B-menu-${tag}`);
    await p.getByRole('menuitem', { name: /^Add to collection/ }).click();
    await p.getByRole('combobox').fill('Fam');
    await shot(p, `C-picker-create-${tag}`);
    await p.keyboard.press('Escape');
    await openLibrary(p, '?view=collections');
    await shot(p, `C-shelves-${tag}`);
    await p.locator('[data-collection="B-roll"]').click();
    await p.locator('[data-collection-header]').waitFor();
    await shot(p, `C-collection-${tag}`);
    await openLibrary(p, '?view=trash');
    await shot(p, `E-trash-${tag}`);
    await p.getByRole('button', { name: 'Empty Trash…' }).click();
    await p.getByRole('dialog').waitFor();
    await shot(p, `F-warning-${tag}`);
    await p.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();
    if (phone) {
      const scrollW = await p.evaluate(() => document.documentElement.scrollWidth);
      check(`phone ${theme}: no sideways scrolling in the Library`, scrollW <= 392, `scrollWidth ${scrollW}`);
      check(`phone ${theme}: the Library is in the top bar`, (await p.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Library' }).isVisible()));
    }
    await p.goto(`${base}/settings/storage#trash`);
    await p.locator('#trash').scrollIntoViewIfNeeded();
    await shot(p, `I-settings-${tag}`);
    await p.context().close();
  }

  /* ---------- F (again): Delete forever… deletes, after the warning ---------- */
  await openLibrary(page, '?view=trash');
  const last = (await lib(page, { view: 'trash' })).items[0];
  await menuFor(page, last.title).click();
  await page.getByRole('menuitem', { name: /^Delete forever/ }).click();
  const confirm = page.getByRole('dialog');
  await confirm.waitFor();
  check('F: Delete forever… shows the same warning', ((await confirm.textContent()) ?? '').includes(`Permanently delete “${last.title}”`));
  await confirm.getByRole('button', { name: 'Delete forever' }).click();
  check('F: the clip is deleted for good', await toastWith(page, 'for good').waitFor({ timeout: 10_000 }).then(() => true, () => false));
  const gone = await waitFor(async () => {
    try {
      await api(page, 'asset.show', { id: last.id });
      return false;
    } catch {
      return true;
    }
  }, 8000);
  check('F: nothing of it is left', !!gone);
  await shot(page, 'F-deleted-desktop-dark');

  /* ---------- K: deleting a project (D196): archived, restored, deleted for good ---------- */
  const spare = (await lib(page, {})).items[0]; // the clip left in the Inbox
  const old = await api(page, 'project.create', { name: 'Old promo' });
  const oldId = old.project.id;
  await api(page, 'asset.link', { assetId: spare.id, projectId: oldId });
  const plan = { projectId: oldId, instruction: 'first cut', spec: { schemaVersion: 1, title: 'Old promo', output: { preset: 'vertical-1080p', fps: 30, platform: 'shorts' }, clips: [{ id: 'a', assetId: spare.id, in: 0, out: 2 }] } };
  const chk = await api(page, 'edit.check', { plan });
  await api(page, 'edit.apply', { plan, confirmToken: chk.confirmToken });
  await page.goto(`${base}/p/${oldId}`);
  await page.getByRole('button', { name: 'More for Old promo' }).click();
  await page.getByRole('menuitem', { name: /^Delete project/ }).click();
  const askDelete = page.getByRole('alertdialog');
  await askDelete.waitFor();
  check('K: Delete project asks first: it moves to Archived projects and can be restored', ((await askDelete.textContent()) ?? '').includes('It moves to Archived projects; you can restore it.'));
  await shot(page, 'K-delete-confirm-desktop-dark');
  await askDelete.getByRole('button', { name: 'Delete project' }).click();
  const deleted = await toastWith(page, 'Deleted “Old promo”').waitFor({ timeout: 8000 }).then(() => true, () => false);
  check('K: deleted, back on Home, with Undo', deleted && new URL(page.url()).pathname === '/' && (await toastWith(page, 'Deleted “Old promo”').getByRole('button', { name: 'Undo' }).count()) === 1);
  check("K: gone from Home's projects", !(await api(page, 'project.list', {})).projects.some((pr) => pr.id === oldId));
  const archivedLink = page.locator('[data-archived-link]');
  check('K: Home links to Archived projects, with the count', await archivedLink.waitFor({ timeout: 8000 }).then(async () => /Archived projects\s*1/.test((await archivedLink.textContent()) ?? ''), () => false));
  await archivedLink.click();
  await page.locator('[data-archived-projects]').waitFor();
  const oldRow = page.locator('[data-archived-row="Old promo"]');
  await oldRow.waitFor({ timeout: 8000 }).catch(() => undefined);
  check('K: Archived projects lists it, saying when it was deleted', ((await oldRow.textContent()) ?? '').includes('Deleted'));
  check('K: its footage stays in the Library, labelled', (await lib(page, { view: 'projects' })).items.some((i) => i.id === spare.id && i.where.includes('Old promo (archived)')));
  await shot(page, 'K-archived-desktop-dark');
  await page.goto(`${base}/p/${oldId}`);
  await page.locator('[data-archived-project]').waitFor();
  check('K: a link to it lands on a page that says it is archived', ((await page.locator('[data-archived-project]').textContent()) ?? '').includes('is archived'));
  await shot(page, 'K-archived-notice-desktop-dark');
  await page.locator('[data-archived-project]').getByRole('button', { name: 'Restore' }).click();
  const restored = await toastWith(page, 'Restored “Old promo”').waitFor({ timeout: 8000 }).then(() => true, () => false);
  check('K: Restore brings it back', restored && (await api(page, 'project.list', {})).projects.some((pr) => pr.id === oldId));
  await api(page, 'project.delete', { id: oldId });
  await page.goto(`${base}/projects/archived`);
  await page.getByRole('button', { name: 'Delete Old promo for good' }).click();
  const purgeWarn = page.locator('[data-purge-confirm]');
  await purgeWarn.waitFor();
  const purgeText = (await purgeWarn.textContent()) ?? '';
  check('K: Delete for good lists what goes and what moves', purgeText.includes('1 version of its cut') && purgeText.includes('1 clip only it had goes back to the Inbox') && purgeText.includes("This can't be undone."), purgeText.slice(0, 300));
  check('K: Cancel has the focus (Enter does not delete)', await page.evaluate(() => document.activeElement?.textContent?.trim() === 'Cancel'));
  await shot(page, 'K-purge-confirm-desktop-dark');
  await purgeWarn.getByRole('button', { name: 'Delete for good' }).click();
  check('K: cleared out for good', await toastWith(page, 'Cleared out “Old promo” for good').waitFor({ timeout: 15_000 }).then(() => true, () => false));
  check('K: its footage is back in the Inbox, not deleted', (await lib(page, {})).items.some((i) => i.id === spare.id));
  check('K: the project is gone', await api(page, 'project.show', { id: oldId }).then(() => false, () => true));
  for (const name of ['Spare one', 'Spare two']) {
    const pr = await api(page, 'project.create', { name });
    await api(page, 'project.delete', { id: pr.project.id });
  }
  await page.goto(`${base}/projects/archived`);
  await page.locator('[data-archived-row="Spare two"]').waitFor({ timeout: 8000 });
  await page.getByRole('button', { name: 'Clear out archived projects…' }).click();
  await page.locator('[data-purge-confirm]').waitFor();
  check('K: Clear out archived projects asks for both', ((await page.getByRole('dialog').textContent()) ?? '').includes('Clear out 2 archived projects for good?'));
  await shot(page, 'K-clear-out-desktop-dark');
  for (const [theme, phone] of [['light', false], ['dark', true], ['light', true]]) {
    const tag = `${phone ? 'phone' : 'desktop'}-${theme}`;
    const pg = await newPage(browser, { theme, phone });
    await pg.goto(`${base}/projects/archived`);
    await pg.locator('[data-archived-row]').first().waitFor({ timeout: 10_000 });
    await shot(pg, `K-archived-${tag}`);
    await pg.getByRole('button', { name: 'Clear out archived projects…' }).click();
    await pg.locator('[data-purge-confirm]').waitFor();
    await shot(pg, `K-clear-out-${tag}`);
    await pg.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();
    if (phone) {
      const scrollW = await pg.evaluate(() => document.documentElement.scrollWidth);
      check(`K phone ${theme}: no sideways scrolling in Archived projects`, scrollW <= 392, `scrollWidth ${scrollW}`);
    }
    await pg.context().close();
  }
  await page.locator('[data-purge-confirm]').getByRole('button', { name: 'Delete for good' }).click();
  check('K: both cleared out', !!(await waitFor(async () => (await api(page, 'project.list', { archivedOnly: true })).projects.length === 0, 15_000)));
  await page.getByText('No archived projects').waitFor({ timeout: 8000 }).catch(() => undefined);
  await shot(page, 'K-archived-empty-desktop-dark');
} catch (e) {
  check('the run finished', false, String(e?.stack ?? e));
} finally {
  await browser.close();
}
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed${SHOTS ? `; screenshots in ${SHOTS}` : ''}`);
process.exit(failed.length ? 1 : 0);
