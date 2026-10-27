#!/usr/bin/env node
// End-to-end check of Remotion sources (D131–D138) in a real browser. Run it through scripts/remotion-e2e.sh,
// which makes the installation and brings in the fixture project (version 1, waiting for a person).
//
//   Sources: the project's Remotion panel (pictures of its compositions, where it came from, waiting for you)
//   the first version reviewed (what was left out, its settings) and approved as a person, in the styled dialog
//   Use in the cut: the composition rendered in the jail and put in the cut
//   an edit like Claude's (CLI: checkout, a new headline, check): the review shows the words that changed and
//     before/after stills; approving it moves the cut's clip to the new version
//   a clip's header says which project, composition and version it was made from, and leads back to it
//   the Add box: a folder path brings a Remotion project in; a GitHub address asks whether it is one
//   desktop light and dark, a phone: no sideways scrolling, pictures load, no native dialogs, no console errors
//   pictures as settings (D209): the title's picture offered, made a setting (version 4, approved by a person), new
//     art uploaded with Replace, checked (stills, the new picture seen) and used — the cut's Title clip made again
//     with it, no new version — then the CLI's list and putting the project's own back (checked only)
//   Tier 3: a project that builds itself refreshed from its own steps in its panel (scripts ticked, a fake key under
//     its name, an output added, started in the styled dialog), its log with the key hidden, the new version
//     reviewed and approved; its changes sent back as a new branch of its local remote (main untouched)
//
// Screenshots go to RS_SHOTS (outside the repository) for review.
import { execFileSync } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5315').replace(/\/$/, '');
const missing = ['METARUSH_MINT', 'RS_PROJECT', 'RS_FOLDER', 'RS_EDIT', 'RS_MR', 'RS_SHOTS'].filter((k) => !process.env[k]);
if (missing.length) {
  console.error(`set ${missing.join(', ')} (scripts/remotion-e2e.sh does)`);
  process.exit(2);
}
const P = process.env.RS_PROJECT;
const SHOTS = process.env.RS_SHOTS;
const NAME = 'fixture-film';
mkdirSync(SHOTS, { recursive: true });

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
}

function cli(args) {
  const [bin, ...pre] = process.env.RS_MR.split(' ');
  const out = execFileSync(bin, [...pre, ...args, '--json'], { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 64 << 20 });
  return JSON.parse(out);
}

function mintLink() {
  const [bin, ...pre] = process.env.METARUSH_MINT.split(' ');
  const out = execFileSync(bin, pre, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return `${base}/auth${new URL(m[0]).search}`;
}

async function api(page, name, input = {}) {
  return page.evaluate(
    async ([name, input]) => {
      const r = await fetch(`/api/ops/${name}`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' },
        body: JSON.stringify(input),
      });
      const j = await r.json();
      if (!j.ok) throw new Error(`${name}: ${j.error?.code} ${j.error?.message}`);
      return j.data;
    },
    [name, input],
  );
}

async function waitFor(fn, what, timeout = 240_000) {
  const end = Date.now() + timeout;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 1000));
  }
}

const viewports = { desktop: { width: 1440, height: 1000 }, phone: { width: 390, height: 844 } };

async function newPage(browser, theme, size) {
  const ctx = await browser.newContext({ viewport: viewports[size], deviceScaleFactor: size === 'phone' ? 2 : 1, isMobile: size === 'phone', hasTouch: size === 'phone', colorScheme: theme });
  await ctx.addInitScript((t) => {
    try {
      localStorage.setItem('metarush.theme', t);
    } catch {
      // ignore
    }
  }, theme);
  const page = await ctx.newPage();
  const problems = [];
  page.on('dialog', async (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    await d.dismiss();
  });
  page.on('console', (m) => {
    if (m.type() === 'error' && !/favicon/.test(m.text())) problems.push(`console error: ${m.text()}`);
  });
  page.on('pageerror', (e) => problems.push(`page error: ${e.message}`));
  await page.goto(mintLink());
  await page.waitForLoadState('networkidle');
  return { ctx, page, problems };
}

const noSideways = (page) => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1);
const shot = async (page, name) => {
  const file = path.join(SHOTS, `${name}.png`);
  await page.waitForTimeout(500); // let fades and the toast settle
  await page.screenshot({ path: file, fullPage: true });
  console.log(`     screenshot ${file}`);
};
/** Every picture in the locator has loaded (and none is broken). */
async function picturesLoad(loc) {
  const imgs = loc.locator('img');
  const n = await imgs.count();
  for (let i = 0; i < n; i++) await imgs.nth(i).scrollIntoViewIfNeeded();
  return waitFor(
    async () => {
      const states = await imgs.evaluateAll((els) => els.map((e) => e.complete && e.naturalWidth > 0));
      return states.length > 0 && states.every(Boolean) ? states.length : 0;
    },
    'the pictures to load',
    30_000,
  ).catch(() => 0);
}
const sources = (page) => page.locator('[data-remotion-sources]');
const card = (page) => page.locator(`[data-remotion-source="${NAME}"]`);
async function openSources(page) {
  await page.goto(`${base}/p/${P}?tab=sources`);
  await sources(page).getByRole('heading', { name: 'Remotion projects' }).waitFor({ timeout: 30_000 });
}
async function confirmIn(page, title, button) {
  const dialog = page.getByRole('dialog').or(page.getByRole('alertdialog')).filter({ hasText: title });
  await dialog.waitFor({ timeout: 10_000 });
  await dialog.getByRole('button', { name: button, exact: true }).click();
  await dialog.waitFor({ state: 'detached', timeout: 10_000 });
}

const browser = await chromium.launch();
try {
  // ---- version 1: looked at and approved by a person ----
  {
    const { ctx, page, problems } = await newPage(browser, 'light', 'desktop');
    await openSources(page);
    const c = card(page);
    check('the Remotion panel lists the project', await c.isVisible());
    check('version 1 waits for a person', await c.getByText('Version 1: waiting for a person').first().isVisible());
    check('it says where it came from', await c.getByText(/From the folder/).first().isVisible());
    const pics = await picturesLoad(c);
    check('pictures of all three compositions load (drawn in the jail, served as images)', pics === 3, `${pics} loaded`);
    check('no composition can be used before approval', (await c.getByRole('button', { name: 'Use in the cut' }).count()) === 0);
    check('the video it already had is offered as footage', await c.getByRole('button', { name: /Title \(fixture-film\)/ }).isVisible());
    await shot(page, 'sources-waiting-desktop-light');

    // The version waiting for a person is a question on the To-dos page and in the bell (D172).
    await page.goto(`${base}/todos?kind=approval`);
    const todo = page.locator('[data-todo-kind="remotion_approval"]').first();
    await todo.waitFor({ timeout: 20_000 });
    check('To-dos asks whether the Remotion project can go into cuts', /Can the Remotion project “fixture-film” go into cuts\?/.test((await todo.textContent()) ?? ''));
    check('…with a way to look at it first', await todo.locator('[data-todo-look]').isVisible());
    await shot(page, 'todos-remotion-desktop-light');
    const bell = await api(page, 'notification.list', { limit: 20 });
    check('the bell tells the person who brought it in', bell.notifications.some((n) => n.kind === 'remotion_waiting'));
    await todo.locator('[data-todo-look]').click();
    await card(page).waitFor({ timeout: 20_000 });
    check('…which opens its review under Sources', new URL(page.url()).searchParams.get('remotion')?.startsWith('rms_') === true);
    await openSources(page);

    await c.getByRole('button', { name: 'Review version 1' }).click();
    const review = c.locator('[data-remotion-review]');
    await review.waitFor();
    check('the review says what was left out (secrets, coding-assistant files, the render)', (await review.getByText(/holds secrets/).count()) === 1 && (await review.getByText(/coding assistants/).count()) === 1);
    check('the review notes the project settings MetaRush handles its own way', await review.getByText('Its own render settings').isVisible());
    check('the URL opens this review', new URL(page.url()).searchParams.get('remotion')?.startsWith('rms_') === true);
    await shot(page, 'review-v1-desktop-light');
    await review.getByRole('button', { name: 'Approve', exact: true }).click();
    await confirmIn(page, 'Approve version 1', 'Approve');
    await c.getByText('Version 1: approved').first().waitFor({ timeout: 20_000 });
    check('approved in the styled dialog: the card says so', true);

    // ---- a composition into the cut ----
    const title = c.locator('li').filter({ has: page.getByText('Title', { exact: true }) }).first();
    await title.getByRole('button', { name: 'Use in the cut' }).click();
    await page.getByText(/Rendering “Title”|is in the cut/).first().waitFor({ timeout: 20_000 });
    const head = await waitFor(async () => {
      const s = await api(page, 'project.show', { id: P });
      return s.headRevisionId ?? null;
    }, 'the cut with the Title clip');
    check('Use in the cut made the clip and put it in a new cut', !!head);
    check('no native dialogs or console errors (version 1)', problems.length === 0, problems.join(' | '));
    await ctx.close();
  }

  // ---- an edit like Claude's, made in the terminal ----
  const co = cli(['remotion', 'checkout', NAME, process.env.RS_EDIT, '--project', P]);
  check('checkout for editing', !!co.data?.dir, co.data?.message);
  const story = path.join(process.env.RS_EDIT, 'src', 'story.ts');
  writeFileSync(story, readFileSync(story, 'utf8').replace('Hello from the fixture film', 'Hello from version two'));
  const ck = cli(['remotion', 'check', process.env.RS_EDIT, '-m', 'a new headline', '--project', P]);
  check('the check of version 2 passes and waits for a person', ck.data.ok && ck.data.versionNumber === 2, ck.data.summary);

  // ---- version 2 reviewed: words and before/after stills; then approved ----
  for (const [theme, size] of [
    ['light', 'desktop'],
    ['dark', 'desktop'],
    ['dark', 'phone'],
  ]) {
    const tag = `${size}-${theme}`;
    const { ctx, page, problems } = await newPage(browser, theme, size);
    await openSources(page);
    const c = card(page);
    const htmlTheme = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
    check(`${tag}: theme applied`, htmlTheme === theme, htmlTheme ?? 'none');
    check(`${tag}: version 2 waits for you`, await c.getByText(/Version 2 waits for you/).isVisible());
    check(`${tag}: the Sources tab doesn't scroll sideways`, await noSideways(page));
    await shot(page, `sources-${tag}`);
    await c.getByRole('button', { name: 'Review version 2' }).click();
    const review = c.locator('[data-remotion-review]');
    await review.waitFor();
    check(`${tag}: the words that changed`, (await review.getByText('“Hello from the fixture film”', { exact: true }).isVisible()) && (await review.getByText('“Hello from version two”', { exact: true }).isVisible()));
    const stills = await picturesLoad(review);
    check(`${tag}: before and after stills load`, stills >= 2, `${stills} pictures`);
    check(`${tag}: the review doesn't scroll sideways`, await noSideways(page));
    const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    check(`${tag}: page background follows the theme`, theme === 'dark' ? bg !== 'rgb(242, 241, 238)' : bg !== 'rgb(13, 12, 11)', bg);
    await review.scrollIntoViewIfNeeded();
    await shot(page, `review-v2-${tag}`);
    if (tag === 'phone-dark') {
      await review.getByRole('button', { name: 'Approve', exact: true }).click();
      await confirmIn(page, 'Approve version 2', 'Approve');
      await page.getByText(/Approved version 2/).first().waitFor({ timeout: 20_000 });
      check(`${tag}: approved on a phone, in the styled dialog`, true);
    }
    check(`${tag}: no native dialogs or console errors`, problems.length === 0, problems.join(' | '));
    await ctx.close();
  }

  // ---- the cut moves to version 2; the clip says where it came from ----
  {
    const { ctx, page, problems } = await newPage(browser, 'light', 'desktop');
    const clip = await waitFor(async () => {
      const l = await api(page, 'remotion.list', { projectId: P });
      const src = l.sources.find((s) => s.name === NAME);
      const c = src?.clips.find((x) => x.kind === 'render' && x.composition === 'Title' && x.versionNumber === 2);
      if (!c) return null;
      const cut = await api(page, 'cut.show', { projectId: P });
      return cut.sheet.includes('Title, version 2') ? c : null;
    }, 'the cut to move to version 2');
    check('approving version 2 moved the cut to it', !!clip);
    await page.goto(`${base}/p/${P}?tab=sources&asset=${clip.assetId}`);
    const credit = page.getByText(/Made from the Remotion project/).first();
    await credit.waitFor({ timeout: 20_000 });
    check('the clip says which project, composition and version it was made from', /fixture-film.*Title, version 2/.test((await credit.textContent()) ?? ''));
    await shot(page, 'clip-credit-desktop-light');
    await page.getByRole('button', { name: 'see the project' }).click();
    const back = card(page).locator('[data-remotion-review]');
    await back.waitFor({ timeout: 20_000 });
    check('…and leads back to the project, open on that version', await back.getByRole('heading', { name: 'Version 2' }).isVisible());

    // ---- the Add box: a folder, and a GitHub address ----
    await openSources(page);
    const field = page.getByLabel('Paste a link or a folder').first();
    await field.fill(process.env.RS_FOLDER);
    await field.press('Enter');
    await page.getByText(/Bringing in the Remotion project from the folder fixture-film/).first().waitFor({ timeout: 20_000 });
    check('the Add box takes a Remotion project’s folder', true);
    await field.fill('https://github.com/example-org/teaser-film');
    await field.press('Enter');
    const ask = page.getByRole('dialog').filter({ hasText: 'Is this a Remotion project?' });
    await ask.waitFor({ timeout: 15_000 });
    check('a GitHub address in Sources asks whether it is a Remotion project', true);
    await shot(page, 'add-github-question-desktop-light');
    await page.keyboard.press('Escape');
    await ask.waitFor({ state: 'detached', timeout: 10_000 });
    check('…and closing the question brings nothing in (the address stays in the field)', (await field.inputValue()) === 'https://github.com/example-org/teaser-film');
    check('no native dialogs or console errors (the cut, the clip, the Add box)', problems.length === 0, problems.join(' | '));
    await ctx.close();
  }

  // ---- Tier 2 (D167–D172): scenes and sounds, words as settings, sound as tracks, a scene as a motion graphic ----
  const jobDone = (page, id) => waitFor(async () => {
    const j = await api(page, 'job.show', { id });
    return ['succeeded', 'failed', 'canceled'].includes(j.state) ? j : null;
  }, 'the job to finish', 600_000);
  {
    const { ctx, page, problems } = await newPage(browser, 'light', 'desktop');
    await openSources(page);
    const c = card(page);
    await c.locator('[data-remotion-panel="scenes"]').click();
    const map = c.locator('[data-remotion-map="done"]');
    await map.waitFor({ timeout: 600_000 });
    check('Scenes and sounds: the map is made (in the jail) and shown', true);
    await map.getByRole('button', { name: 'Film', exact: true }).click();
    const scenes = map.locator('[data-remotion-scene]');
    check('Film shows its two scenes', (await scenes.count()) === 2 && /Opening/.test((await scenes.nth(0).textContent()) ?? '') && /End card/.test((await scenes.nth(1).textContent()) ?? ''));
    check('…with the words on screen', await map.getByText('“Made with the fixture theme”').first().isVisible());
    check('…and its music and sound', await map.getByText(/1 piece of music, 1 sound effect/).isVisible());
    const stills = await picturesLoad(map);
    check('a picture of each scene loads', stills >= 2, `${stills} pictures`);
    check('the scenes don\'t scroll sideways', await noSideways(page));
    await shot(page, 'scenes-desktop-light');
    // Its sound as the cut's tracks.
    await map.getByRole('button', { name: 'Use in the cut, its sounds as tracks' }).click();
    const tracks = await waitFor(async () => {
      const s = await api(page, 'project.show', { id: P });
      const r = s.headRevisionId ? await api(page, 'revision.show', { id: s.headRevisionId }) : null;
      const roles = new Set((r?.spec.audio ?? []).map((a) => a.role));
      return roles.has('music') && roles.has('sfx') ? [...roles] : null;
    }, 'its music and sound in the cut', 600_000);
    check('Use in the cut, its sounds as tracks: music and the sound effect are the cut\'s tracks', !!tracks, tracks?.join(', '));
    // Words as settings: the offer, the change approved from To-dos.
    await c.locator('[data-remotion-panel="words"]').click();
    const offer = c.locator('[data-remotion-words="offer"]');
    await offer.waitFor({ timeout: 20_000 });
    await shot(page, 'words-offer-desktop-light');
    await offer.getByRole('button', { name: 'Make its words settings' }).click();
    const v3 = await waitFor(async () => {
      const l = await api(page, 'remotion.list', { projectId: P });
      const src = l.sources.find((s) => s.name === NAME);
      return src && src.headNumber === 3 && src.headState === 'waiting' ? src : null;
    }, 'the version that makes its words settings', 600_000);
    check('Make its words settings: version 3 waits for a person', !!v3);
    await page.goto(`${base}/todos?kind=approval`);
    const todo = page.locator('[data-todo-kind="remotion_approval"]').filter({ hasText: 'version 3' }).first();
    await todo.waitFor({ timeout: 20_000 });
    await shot(page, 'todos-remotion-v3-desktop-light');
    await todo.getByRole('button', { name: /Approve this version/ }).click();
    await waitFor(async () => {
      const l = await api(page, 'remotion.list', { projectId: P });
      return l.sources.find((s) => s.name === NAME)?.approvedNumber === 3;
    }, 'version 3 approved from To-dos');
    check('approved from the To-dos page', true);
    await openSources(page);
    await card(page).locator('[data-remotion-panel="words"]').click();
    const form = card(page).locator('[data-remotion-words="settings"]');
    await form.waitFor({ timeout: 20_000 });
    const field = form.locator('[data-word-setting="END.tagline"] textarea');
    check('the words are a form now', (await field.inputValue()) === 'Made with the fixture theme');
    await field.fill('A new tagline, as a setting');
    await form.getByRole('button', { name: 'Check the new words' }).click();
    await form.locator('[data-words-check]').first().waitFor({ timeout: 600_000 });
    check('Check the new words: drawn and checked, with pictures', (await picturesLoad(form.locator('[data-words-check]').first())) >= 1);
    await shot(page, 'words-check-desktop-light');
    await form.getByRole('button', { name: 'Use the new words' }).click();
    const changed = await waitFor(async () => {
      const w = await api(page, 'remotion.words', { source: NAME, projectId: P });
      return w.number === 1 ? w : null;
    }, 'the change of words', 600_000);
    check('Use the new words: the setting changed, no new version', changed.settings.find((s) => s.key === 'END.tagline')?.text === 'A new tagline, as a setting' && changed.versionNumber === 3);
    // A scene as a motion graphic.
    await card(page).locator('[data-remotion-panel="scenes"]').click();
    const map3 = card(page).locator('[data-remotion-map="done"]');
    await map3.waitFor({ timeout: 600_000 });
    await map3.getByRole('button', { name: 'Film', exact: true }).click();
    await map3.locator('[data-remotion-scene="2"]').getByRole('button', { name: 'Make it a motion graphic' }).click();
    const lifted = await waitFor(async () => {
      const m = await api(page, 'motion.list', {});
      const g = m.components.find((x) => x.name === 'end-card');
      const lib = m.components.find((x) => x.name === 'fixture-film-theme');
      return g && lib ? { g, lib } : null;
    }, 'the lifted end card and its library', 600_000);
    check('Make it a motion graphic: the end card and the shared theme are checked and wait for a person', lifted.g.state === 'waiting' && lifted.lib.kind === 'library' && (lifted.g.libraries ?? []).includes('fixture-film-theme'));
    void jobDone;
    check('no native dialogs or console errors (Tier 2, desktop)', problems.length === 0, problems.join(' | '));
    await ctx.close();
  }
  // ---- Pictures as settings (D209): made settings once, then new art without a new version of the code ----
  if (process.env.RS_PICTURE) {
    const { ctx, page, problems } = await newPage(browser, 'light', 'desktop');
    await openSources(page);
    await card(page).locator('[data-remotion-panel="pictures"]').click();
    const offer = card(page).locator('[data-remotion-pictures="offer"]');
    await offer.waitFor({ timeout: 20_000 });
    check('Pictures: the picture its code draws is offered', (await offer.locator('[data-picture-candidate="PICTURE"]').count()) === 1);
    check('…shown as the project has it', (await picturesLoad(offer)) >= 1);
    await shot(page, 'pictures-offer-desktop-light');
    await offer.getByRole('button', { name: 'Make its pictures settings' }).click();
    const v4 = await waitFor(async () => {
      const l = await api(page, 'remotion.list', { projectId: P });
      const src = l.sources.find((s) => s.name === NAME);
      return src && src.headNumber === 4 && src.headState === 'waiting' ? src : null;
    }, 'the version that makes its pictures settings', 600_000);
    check('Make its pictures settings: version 4 waits for a person', !!v4);
    // The version is recorded first, then checked (in the jail): approve it only once its check is done.
    const shown = await waitFor(async () => {
      const x = await api(page, 'remotion.show', { ref: `${NAME}@4`, projectId: P });
      return x.version.check ? x : null;
    }, 'the check of version 4', 600_000);
    check('…its check passed, the picture wrapped in its code', shown.version.check?.ok === true && (shown.version.pictures ?? []).some((p) => p.name === 'PICTURE' && p.setting), shown.version.check?.summary);
    await openSources(page);
    await card(page).getByRole('button', { name: 'Review version 4' }).click();
    const review = card(page).locator('[data-remotion-review]');
    await review.waitFor();
    await review.getByRole('button', { name: 'Approve', exact: true }).click();
    await confirmIn(page, 'Approve version 4', 'Approve');
    await waitFor(async () => (await api(page, 'remotion.list', { projectId: P })).sources.find((s) => s.name === NAME)?.approvedNumber === 4, 'version 4 approved');
    check('version 4 approved by a person', true);
    // Its map now (the pages after this one show its scenes).
    await waitFor(async () => (await api(page, 'remotion.map', { ref: `${NAME}@4`, projectId: P })).state === 'done', 'the map of version 4', 600_000);
    await openSources(page);
    await card(page).locator('[data-remotion-panel="pictures"]').click();
    const form = card(page).locator('[data-remotion-pictures="settings"]');
    await form.waitFor({ timeout: 20_000 });
    const pic = form.locator('[data-picture-setting="PICTURE"]');
    check('the picture is a setting, with Replace', await pic.getByRole('button', { name: 'Replace…' }).isVisible());
    check("…and the project's own picture loads", (await picturesLoad(pic)) >= 1);
    await pic.getByRole('button', { name: 'Replace…' }).click();
    const picker = page.locator('[data-picture-picker="PICTURE"]');
    await picker.waitFor({ timeout: 10_000 });
    await shot(page, 'pictures-replace-desktop-light');
    await picker.locator('[data-picture-upload="PICTURE"]').setInputFiles(process.env.RS_PICTURE);
    await pic.locator('[data-picture-now] img').waitFor({ timeout: 120_000 });
    check("Replace…: the uploaded picture is ready to use (it went to the project's resources)", (await picturesLoad(pic)) >= 2);
    await form.getByRole('button', { name: 'Check the new pictures' }).click();
    await form.locator('[data-pictures-check]').first().waitFor({ timeout: 600_000 });
    const chk = await api(page, 'remotion.pictures', { source: NAME, projectId: P });
    check('Check the new pictures: drawn with no network, the new picture seen, nothing changed yet',
      chk.pending?.check?.ok === true && chk.number === 0 && !(chk.pending.check.problems ?? []).some((p) => p.code === 'picture_not_seen'), chk.pending?.summary);
    check('…with before/after stills', (await picturesLoad(form.locator('[data-pictures-check]').first())) >= 2);
    await shot(page, 'pictures-check-desktop-light');
    await form.getByRole('button', { name: 'Use the new pictures' }).click();
    const used = await waitFor(async () => {
      const w = await api(page, 'remotion.pictures', { source: NAME, projectId: P });
      return w.number === 1 ? w : null;
    }, 'the change of pictures', 600_000);
    check('Use the new pictures: replaced, no new version of its code', used.pictures.find((p) => p.name === 'PICTURE')?.changed === true && used.versionNumber === 4);
    const moved = await waitFor(async () => {
      const l = await api(page, 'remotion.list', { projectId: P });
      const made = l.sources.find((s) => s.name === NAME)?.clips.find((x) => x.kind === 'render' && x.composition === 'Title' && x.picturesNumber === 1);
      if (!made) return null;
      const s = await api(page, 'project.show', { id: P });
      const r = s.headRevisionId ? await api(page, 'revision.show', { id: s.headRevisionId }) : null;
      return r?.spec.clips.some((x) => x.assetId === made.assetId) ? made : null;
    }, "the cut's Title clip made again with the new picture", 900_000);
    check("…the latest cut's Title clip was made again with the new picture", !!moved);
    await openSources(page);
    await card(page).locator('[data-remotion-panel="pictures"]').click();
    await card(page).locator('[data-remotion-pictures="settings"] [data-picture-changed]').first().waitFor({ timeout: 20_000 });
    await shot(page, 'pictures-replaced-desktop-light');
    // The same from the terminal: the list, and the project's own picture back (only checked).
    const list = cli(['remotion', 'pictures', 'list', NAME, '--project', P]);
    check('remotion pictures list (CLI): the picture and what replaces it', list.data?.pictures?.find((p) => p.name === 'PICTURE')?.current?.sha256?.length === 64);
    const back = cli(['remotion', 'pictures', 'set', NAME, '--original', 'PICTURE', '--check', '--project', P]);
    check('remotion pictures set --original --check (CLI): drawn and checked, nothing changed', back.data?.result?.ok === true && back.data?.result?.checkOnly === true, back.data?.result?.message);
    check('no native dialogs or console errors (pictures, desktop)', problems.length === 0, problems.join(' | '));
    await ctx.close();
  }

  for (const [theme, size] of [
    ['dark', 'desktop'],
    ['dark', 'phone'],
  ]) {
    const tag = `${size}-${theme}`;
    const { ctx, page, problems } = await newPage(browser, theme, size);
    await openSources(page);
    const c = card(page);
    await c.locator('[data-remotion-panel="scenes"]').click();
    const map = c.locator('[data-remotion-map="done"]');
    await map.waitFor({ timeout: 120_000 });
    await map.getByRole('button', { name: 'Film', exact: true }).click();
    check(`${tag}: the scenes don't scroll sideways`, await noSideways(page));
    await picturesLoad(map);
    await shot(page, `scenes-${tag}`);
    await c.locator('[data-remotion-panel="words"]').click();
    await c.locator('[data-remotion-words="settings"]').waitFor({ timeout: 20_000 });
    check(`${tag}: the words form doesn't scroll sideways`, await noSideways(page));
    await shot(page, `words-${tag}`);
    if (process.env.RS_PICTURE) {
      await c.locator('[data-remotion-panel="pictures"]').click();
      const form = c.locator('[data-remotion-pictures="settings"]');
      await form.waitFor({ timeout: 20_000 });
      check(`${tag}: the pictures (the project's own and the new one) load`, (await picturesLoad(form.locator('[data-picture-setting="PICTURE"]'))) >= 2);
      check(`${tag}: the pictures don't scroll sideways`, await noSideways(page));
      await shot(page, `pictures-${tag}`);
    }
    await page.goto(`${base}/library/motion`);
    const lib = page.getByText('Shared library').first();
    await lib.waitFor({ timeout: 20_000 });
    check(`${tag}: Motion graphics shows the lifted end card and its shared library`, (await page.getByText('end-card', { exact: true }).count()) >= 1);
    await shot(page, `motion-library-${tag}`);
    check(`${tag}: no native dialogs or console errors`, problems.length === 0, problems.join(' | '));
    await ctx.close();
  }

  // ---- Tier 3 (D173–D176): a refresh from its own steps, then its changes sent back ----
  const REFRESH = 'refresh-film';
  const rcard = (page) => page.locator(`[data-remotion-source="${REFRESH}"]`);
  const fakeKey = 'sk_' + 'f00d'.repeat(10);
  if (process.env.RS_REMOTE) {
    const imp = cli(['import', 'remotion', process.env.RS_REFRESH, '--project', P, '--name', REFRESH, '--wait']);
    check('a project that builds itself is brought in (a git repository with a remote)', !!imp.data?.result?.version, imp.data?.result?.message);
    const { ctx, page, problems } = await newPage(browser, 'light', 'desktop');
    await api(page, 'remotion.approve', { ref: `${REFRESH}@1`, projectId: P, keepCuts: true });
    await openSources(page);
    const c = rcard(page);
    await c.locator('[data-remotion-panel="refresh"]').click();
    const panel = c.locator('[data-remotion-refresh]');
    await panel.waitFor({ timeout: 30_000 });
    check('Refresh from its own steps: the panel lists its scripts', (await panel.locator('[data-refresh-script]').count()) >= 3);
    check('…a script that renders is marked', await panel.getByText('It renders videos; MetaRush makes clips itself.').first().isVisible());
    check('…nothing starts before steps are picked', await panel.locator('[data-refresh-start]').isDisabled());
    await panel.locator('[data-refresh-script="make-data"]').check();
    await panel.locator('[data-refresh-script="voice"]').check();
    await panel.locator('[data-refresh-key="elevenlabs"]').check();
    await panel.getByLabel('Another place it may write').fill('src/generated/');
    await panel.getByLabel('Another place it may write').press('Enter');
    await waitFor(async () => !(await panel.locator('[data-refresh-start]').isDisabled()), 'the plan to be ready', 30_000);
    const text = (await panel.textContent()) ?? '';
    check('…the plan says the steps, the key under its name and where it may write', /npm run make-data/.test(text) && /ELEVENLABS_API_KEY/.test(text) && /src\/generated\//.test(text));
    check('…how it is kept apart, and that only a person starts it', /sandbox|bubblewrap/.test(text) && /Only a person starts one/.test(text));
    check('…and never shows the key', !text.includes('f00df00d'));
    check('the refresh panel doesn\'t scroll sideways', await noSideways(page));
    await panel.scrollIntoViewIfNeeded();
    await shot(page, 'refresh-plan-desktop-light');
    await panel.locator('[data-refresh-start]').click();
    await confirmIn(page, `Refresh “${REFRESH}” from its own steps?`, 'Start the refresh');
    const run = await waitFor(async () => {
      const r = await api(page, 'remotion.refresh.runs', { source: REFRESH, projectId: P });
      const x = r.refreshes[0];
      return x && ['done', 'failed', 'canceled'].includes(x.state) ? x : null;
    }, 'the refresh to finish', 900_000);
    check('started in the styled dialog: the refresh made version 2, checked and waiting for you', run.state === 'done' && run.versionNumber === 2, run.summary || run.error);
    check('…a file written elsewhere was left out', run.leftOut.some((l) => l.path === 'notes.txt'));
    const runRow = panel.locator('[data-refresh-run="1"]');
    await runRow.waitFor({ timeout: 30_000 });
    await runRow.getByRole('button', { name: 'What it printed' }).click();
    const log = runRow.locator('[data-refresh-log]');
    await log.waitFor({ timeout: 20_000 });
    const logText = (await log.textContent()) ?? '';
    check('what it printed is kept, with the key hidden', /made the data/.test(logText) && /\[redacted\]/.test(logText) && !logText.includes(fakeKey));
    await shot(page, 'refresh-done-desktop-light');
    await runRow.getByRole('button', { name: 'Review version 2' }).click();
    const review = c.locator('[data-remotion-review]');
    await review.waitFor({ timeout: 20_000 });
    check('Review version 2: made by a refresh, with before/after stills', /made by a refresh/i.test((await review.textContent()) ?? '') && (await picturesLoad(review)) >= 2);
    await review.getByRole('button', { name: 'Approve', exact: true }).click();
    await confirmIn(page, 'Approve version 2', 'Approve');
    await c.getByText('Version 2: approved').first().waitFor({ timeout: 30_000 });
    check('the refreshed version approved by a person', true);
    // Its changes back to its remote, as a new branch.
    await c.locator('[data-remotion-panel="push"]').click();
    const push = c.locator('[data-remotion-push]');
    await push.waitFor({ timeout: 30_000 });
    await waitFor(async () => !(await push.locator('[data-push-send]').isDisabled()), 'the push plan', 60_000);
    const ptext = (await push.textContent()) ?? '';
    check('Send changes back: the changes in plain words, the new branch', /metarush\/refresh-film-v2/.test(ptext) && /src\/generated\/stats\.ts/.test(ptext) && !/notes\.txt/.test(ptext));
    check('the push panel doesn\'t scroll sideways', await noSideways(page));
    await push.scrollIntoViewIfNeeded();
    await shot(page, 'push-plan-desktop-light');
    await push.locator('[data-push-send]').click();
    await confirmIn(page, `Send version 2 of “${REFRESH}” back?`, 'Send them back');
    const sent = await waitFor(async () => {
      const l = await api(page, 'remotion.push.list', { source: REFRESH, projectId: P });
      const x = l.pushes[0];
      return x && ['done', 'failed'].includes(x.state) ? x : null;
    }, 'the changes to be sent', 300_000);
    check('sent in the styled dialog', sent.state === 'done', sent.error);
    const tip = execFileSync('git', ['--git-dir', process.env.RS_REMOTE, 'rev-parse', 'refs/heads/metarush/refresh-film-v2', 'refs/heads/main'], { encoding: 'utf8' }).trim().split('\n');
    check('…the remote has the new branch, and its main branch is where it was', tip[0] === sent.commit && tip[1] === process.env.RS_ORIGINAL, tip.join(' '));
    await push.locator('[data-push-history]').waitFor({ timeout: 20_000 });
    await shot(page, 'push-done-desktop-light');
    check('no native dialogs or console errors (Tier 3, desktop)', problems.length === 0, problems.join(' | '));
    await ctx.close();
  }
  if (process.env.RS_REMOTE) {
    for (const [theme, size] of [
      ['dark', 'desktop'],
      ['dark', 'phone'],
      ['light', 'phone'],
    ]) {
      const tag = `${size}-${theme}`;
      const { ctx, page, problems } = await newPage(browser, theme, size);
      await openSources(page);
      const c = rcard(page);
      await c.locator('[data-remotion-panel="refresh"]').click();
      const panel = c.locator('[data-remotion-refresh]');
      await panel.waitFor({ timeout: 30_000 });
      await panel.locator('[data-refresh-run="1"]').waitFor({ timeout: 30_000 });
      check(`${tag}: the refresh panel doesn't scroll sideways`, await noSideways(page));
      await panel.scrollIntoViewIfNeeded();
      await shot(page, `refresh-${tag}`);
      await c.locator('[data-remotion-panel="push"]').click();
      const push = c.locator('[data-remotion-push]');
      await push.locator('[data-push-history]').waitFor({ timeout: 30_000 });
      check(`${tag}: the push panel doesn't scroll sideways`, await noSideways(page));
      await push.scrollIntoViewIfNeeded();
      await shot(page, `push-${tag}`);
      check(`${tag}: no native dialogs or console errors (Tier 3)`, problems.length === 0, problems.join(' | '));
      await ctx.close();
    }
  }
} catch (e) {
  check('the run finished', false, e instanceof Error ? e.message : String(e));
} finally {
  await browser.close();
}
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed; screenshots in ${SHOTS}`);
process.exit(failed.length ? 1 : 0);
