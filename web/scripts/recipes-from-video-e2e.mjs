#!/usr/bin/env node
// End-to-end check of Start from a video you like (D398–D404) in the dashboard, against a
// seeded throwaway daemon whose downloader is a fake that hands over the fixture
// reference-short.mp4 (scripts/start-from-video-e2e.sh does all of it):
//   A  the Recipes tab's Add box takes a link: it comes in as a reference, the box follows it
//      while MetaRush looks (its worker, for real), then shows what the video does — its
//      sections with times as tests/fixtures/truth/reference-short.json has them, captions,
//      words on screen and music — and the draft, never current
//   B  the draft's sheet says where it came from and holds none of the video's words: no line
//      it says, no caption, title or end card's words; its check passes
//   C  Open the draft: its page says what it was drafted from; a person makes it current
//   D  What the video does opens the clip on its What it does tab with the reference's note;
//      This is mine… asks in a styled question (cancelled: it stays a reference); a cut that
//      plays it is refused in plain words, with how to say it's yours
//   E  Home's Start from finds the same draft again (Drafted earlier)
//   F  Claude's card in the panel names the video and what it does; Start the draft makes a
//      second draft whose runs make new cuts in the project
//   G  on a phone, a video file chosen in the Add box comes in as a reference and is drafted
//      too; desktop light, phone dark and light, no sideways scrolling
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5760 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   FROM_VIDEO_PROJECT=prj_… FROM_VIDEO_FILE=<a video file> FROM_VIDEO_CLAUDE_SCRIPT=<the fake claude's script> \
//   FROM_VIDEO_SHOTS=<dir> node web/scripts/recipes-from-video-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkPanel, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5760').replace(/\/$/, '');
const project = process.env.FROM_VIDEO_PROJECT;
const chosenFile = process.env.FROM_VIDEO_FILE;
const claudeScript = process.env.FROM_VIDEO_CLAUDE_SCRIPT;
if (!process.env.METARUSH_MINT || !project || !chosenFile || !claudeScript) {
  console.error('set METARUSH_MINT, METARUSH_DASHBOARD, FROM_VIDEO_PROJECT, FROM_VIDEO_FILE and FROM_VIDEO_CLAUDE_SCRIPT (see the header of this script)');
  process.exit(2);
}
const SHOTS = process.env.FROM_VIDEO_SHOTS ?? '';
if (SHOTS) mkdirSync(SHOTS, { recursive: true });
const truth = JSON.parse(readFileSync(path.join(repo, 'tests/fixtures/truth/reference-short.json'), 'utf8'));
const LINK = 'https://www.youtube.com/watch?v=refshort001';
const onMac = process.platform === 'darwin'; // what's on screen is read on a Mac (Apple Vision)

const results = [];
const problems = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail && !ok ? ` - ${detail}` : detail ? ` (${String(detail).slice(0, 160)})` : ''}`);
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function waitFor(fn, ms, every = 500) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > end) return null;
    await sleep(every);
  }
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
      if (!j.ok) throw Object.assign(new Error(`${op}: ${j.error?.code} ${j.error?.message}`), { error: j.error });
      return j.data;
    },
    [op, input],
  );
}

async function newPage(browser, { theme = 'dark', phone = false, state } = {}) {
  const ctx = await browser.newContext({
    viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 940 },
    deviceScaleFactor: phone ? 2 : 1,
    hasTouch: phone,
    isMobile: phone,
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
  page.on('dialog', (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  if (!state) {
    await page.goto(mintLink());
    await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  }
  return { ctx, page };
}

async function settle(page) {
  await page.waitForFunction(() => [...document.images].every((i) => i.complete), null, { timeout: 10_000 }).catch(() => undefined);
  await sleep(400);
}
let shotN = 0;
async function shot(page, name) {
  await settle(page);
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, `${String(++shotN).padStart(2, '0')}-${name}.png`) });
}
async function noSideways(page, what) {
  const w = await page.evaluate(() => document.documentElement.scrollWidth);
  check(`${what}: no sideways scrolling`, w <= 392, `scrollWidth ${w}`);
}

/** A part of the page as a person reads it, people's own words in curly quotes (as plainwords.mjs expects). */
async function readText(loc) {
  return loc.evaluate(
    (root, [open, close]) => {
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
    [THEIRS_OPEN, THEIRS_CLOSE],
  );
}
async function plainIn(loc, what, checker = checkShown) {
  const text = await readText(loc);
  const unplain = text.split('\n').flatMap((l) => checker(l).map((f) => `${f.kind} ${JSON.stringify(f.text)}`));
  check(`${what} reads plainly`, text.length > 0 && unplain.length === 0, unplain.join(' | ') || `${text.length} characters`);
}

async function openRecipes(page) {
  await page.goto(`${base}/library/recipes`);
  await page.locator('[data-library-section="recipes"]').waitFor({ timeout: 20_000 });
  await settle(page);
}

/** The video's own words: four-word runs of every line it says, its captions, title and end card. */
function theirPhrases() {
  const words = (s) => s.toLowerCase().replace(/[^a-z' ]+/g, ' ').split(/\s+/).filter(Boolean);
  const out = new Set();
  for (const l of truth.lines) {
    const w = words(l.text);
    for (let i = 0; i + 4 <= w.length; i++) out.add(w.slice(i, i + 4).join(' '));
  }
  for (const c of truth.captions) if (words(c.text).length > 1) out.add(words(c.text).join(' '));
  out.add(words(truth.title.text).join(' '));
  out.add(words(truth.end_card.text).join(' '));
  return [...out];
}

const browser = await chromium.launch({ args: ['--autoplay-policy=no-user-gesture-required'] });
let refId = '';
let draftName = '';
try {
  const { ctx, page } = await newPage(browser);
  const state = await ctx.storageState();
  await api(page, 'assistant.mode.set', { mode: 'ask' });

  /* ---------- A: a link in the Recipes tab's Add box ---------- */
  await openRecipes(page);
  const box = page.locator('[data-start-from-video]').first();
  await box.waitFor({ timeout: 20_000 });
  await plainIn(box, 'A: the Add box');
  await shot(page, 'A-add-box-desktop-dark');
  await box.locator('[data-start-from-video-link]').fill(LINK);
  await box.locator('[data-start-from-video-go]').click();
  const stage = () => box.locator('[data-start-from-video-result]').getAttribute('data-start-from-video-result', { timeout: 1000 });
  const looking = await waitFor(async () => ((await stage()) === 'looking' ? true : null), 120_000, 400);
  check('A: the box follows the import, then MetaRush looking at the video', !!looking);
  if (looking) {
    await plainIn(box.locator('[data-start-from-video-result]'), 'A: the box while MetaRush looks');
    await shot(page, 'A-looking-desktop-dark');
  }
  const assets = (await api(page, 'asset.list', {})).assets;
  const ref = assets.find((a) => a.source?.url && a.reference);
  refId = ref?.id ?? '';
  check('A: the video came in as a reference, from the link', !!ref && /refshort001/.test(ref.source.url), JSON.stringify(ref ? { id: ref.id, reference: ref.reference, url: ref.source?.url } : assets.map((a) => a.name)));
  const drafted = await waitFor(async () => ((await box.locator('[data-start-from-video-drafted]').count()) === 1 ? true : null), 900_000, 2000);
  const failedText = drafted ? '' : await box.innerText().catch(() => '');
  check('A: once MetaRush has looked, the box shows the draft', !!drafted, failedText.slice(0, 300));
  if (!drafted) throw new Error('no draft');
  await shot(page, 'A-drafted-desktop-dark');
  const draftedText = await box.locator('[data-start-from-video-drafted]').innerText();
  check('A: the box says the draft it followed is made (not “earlier”)', /Drafted: /.test(draftedText) && !/Drafted earlier/.test(draftedText), draftedText.slice(0, 160));
  const shown = box.locator('[data-what-it-does]');
  const sectionNames = await shown.locator('[data-what-section]').evaluateAll((els) => els.map((e) => e.getAttribute('data-what-section')));
  const wantNames = truth.sections.map((s) => s.name);
  check('A: what the video does: its sections as the fixture has them', JSON.stringify(sectionNames) === JSON.stringify(wantNames), `${sectionNames.join(', ')} vs ${wantNames.join(', ')}`);
  const what = await api(page, 'footage.whatItDoes', { assetId: refId });
  const card = what.card;
  const off = truth.sections
    .map((w, i) => ({ w, s: card.sections[i] }))
    .filter(({ w, s }) => !s || Math.abs(s.from - w.from_s) > 0.6 || Math.abs(s.to - w.to_s) > 0.6)
    .map(({ w, s }) => `${w.name} ${s ? `${s.from}–${s.to}` : 'missing'} (want ${w.from_s}–${w.to_s})`);
  check('A: each section at its time (within 0.6 s)', off.length === 0, off.join('; ') || card.sections.map((s) => `${s.name} ${s.from}–${s.to}`).join(', '));
  check('A: its length and shape', card.ready && Math.abs(card.length - truth.duration_s) < 0.3 && card.shape === 'vertical', `${card.length} s ${card.shape} ready=${card.ready}`);
  check('A: who speaks and the pacing', card.speakers.length === 1 && card.pacing.words >= 30 && card.pacing.shots === truth.cuts_s.length + 1, JSON.stringify({ speakers: card.speakers.length, words: card.pacing.words, shots: card.pacing.shots }));
  check('A: music under the voices', card.sound.bed === 'sound' && card.sound.below >= 8 && card.sound.below <= 30, JSON.stringify(card.sound));
  check('A: the end card after the last words', !!card.end && Math.abs(card.end.from - truth.end_card.from_s) < 0.6, JSON.stringify(card.end));
  if (onMac) {
    check('A: burned-in captions, read on screen', card.captions.looked && card.captions.burned, JSON.stringify(card.captions));
    check('A: the title and the end card’s words on screen', (card.graphics ?? []).some((g) => g.kind === 'title') && (card.graphics ?? []).some((g) => g.kind === 'end-card'), JSON.stringify((card.graphics ?? []).map((g) => `${g.kind}:${g.text}`)));
    check('A: the box says what is on screen', (await shown.locator('[data-what-graphics]').count()) === 1);
  }
  await plainIn(box.locator('[data-start-from-video-result]'), 'A: what the video does and the draft');
  const rows = (await api(page, 'recipe.list', {})).recipes;
  const draft = rows.find((r) => r.draft >= 1 && /Like @fakechannel/.test(r.title));
  draftName = draft?.name ?? '';
  check('A: a draft, never current', !!draft && draft.current === 0, JSON.stringify(rows.map((r) => [r.name, r.title, r.current, r.draft])));

  /* ---------- B: the draft's sheet ---------- */
  const show = await api(page, 'recipe.show', { recipe: draftName });
  const co = await api(page, 'recipe.checkout', { recipe: draftName });
  const sheet = co.sheet;
  check('B: the sheet says where it came from', new RegExp(`^reference: clip ${refId} · link "${LINK.replace(/[?.]/g, '\\$&')}"`, 'm').test(sheet), sheet.split('\n').find((l) => l.startsWith('reference:')) ?? 'no reference line');
  const lowered = sheet.toLowerCase().replace(/[^a-z' \n]+/g, ' ').replace(/ +/g, ' ');
  const theirs = theirPhrases().filter((p) => lowered.includes(p));
  check('B: none of the video’s words are in the draft', theirs.length === 0, theirs.join(' | '));
  check('B: its sections mirror the video’s, with lengths', (show.sections ?? []).length === truth.sections.length, JSON.stringify((show.sections ?? []).map((s) => s.name)));
  check('B: its check passes (each run gives the footage)', show.version.ok === true, JSON.stringify(show.version.check?.problems ?? []).slice(0, 200));

  /* ---------- C: the draft's page; a person makes it current ---------- */
  await box.locator('[data-open-draft]').click();
  const recipePage = page.locator(`[data-recipe-page="${draftName}"]`);
  await recipePage.waitFor({ timeout: 20_000 });
  const from = recipePage.locator('[data-drafted-from]');
  check('C: the page says what it was drafted from', (await from.count()) === 1 && /Drafted from what a video someone liked does/.test(await from.innerText()));
  await plainIn(recipePage, 'C: the draft’s page');
  await shot(page, 'C-draft-page-desktop-dark');
  await recipePage.locator('[data-make-current="1"]').first().click();
  const mc = page.locator('[data-mr-dialog="open"]').last();
  await mc.waitFor({ timeout: 15_000 });
  await mc.getByRole('button', { name: 'Make it current' }).click();
  const current = await waitFor(async () => ((await api(page, 'recipe.show', { recipe: draftName })).recipe.current === 1 ? true : null), 20_000);
  check('C: a person makes it current', !!current);

  /* ---------- D: the clip's page; a cut can't play it ---------- */
  await page.goto(`${base}/library/recipes?recipe=${draftName}`);
  await recipePage.waitFor({ timeout: 20_000 });
  await recipePage.locator('[data-drafted-from] a', { hasText: 'What the video does' }).click();
  const doesPanel = page.locator('[data-what-it-does]').first();
  await doesPanel.waitFor({ timeout: 30_000 });
  const note = page.locator('[data-reference-note]').first();
  check('D: the clip opens on What it does, with the reference’s note', (await note.count()) === 1 && /Someone else’s video, kept to learn from/.test(await note.innerText()));
  await plainIn(page.locator('[data-reference-note]').first(), 'D: the reference’s note');
  await plainIn(doesPanel, 'D: what the video does on its page');
  await doesPanel.scrollIntoViewIfNeeded();
  await shot(page, 'D-clip-what-it-does-desktop-dark');
  await note.locator('[data-reference-mine]').click();
  const mine = page.locator('[data-mr-dialog="open"]').last();
  await mine.waitFor({ timeout: 10_000 });
  check('D: This is mine… asks in a styled question', /Is this your own video\?/.test(await mine.innerText()));
  await shot(page, 'D-this-is-mine-desktop-dark');
  await mine.getByRole('button', { name: 'Cancel' }).click();
  check('D: cancelled, it stays a reference', !!(await api(page, 'asset.show', { id: refId })).asset.reference);
  const asks = (await api(page, 'todos.list', { assetId: refId })).todos;
  check('D: nobody in someone else’s video is asked about (no voice or face questions)', asks.length === 0, JSON.stringify(asks.map((t) => `${t.kind}: ${t.question}`)));
  const tried = await api(page, 'edit.check', {
    plan: { projectId: project, instruction: 'try the liked video', spec: { schemaVersion: 1, title: 'Try', output: { preset: 'vertical-1080p', fps: 30 }, clips: [{ id: 'liked', assetId: refId, in: 0, out: 3 }] } },
  });
  const refused = (tried.errors ?? []).find((e) => e.code === 'reference_footage');
  check('D: a cut that plays it is refused, with how to say it’s yours', !tried.valid && !!refused && /someone else's video, kept to learn from/.test(refused.message) && refused.command === `metarush footage reference ${refId} --mine`, JSON.stringify(refused ?? tried.errors));
  if (refused) check('D: the refusal reads plainly', checkShown(refused.message).length === 0, JSON.stringify(checkShown(refused.message)));

  /* ---------- E: Home's Start from finds the same draft ---------- */
  await page.goto(`${base}/`);
  const homeBox = page.locator('[data-start-from-recipe] [data-start-from-video]');
  await homeBox.waitFor({ timeout: 20_000 });
  await homeBox.locator('[data-start-from-video-link]').fill(LINK);
  await homeBox.locator('[data-start-from-video-go]').click();
  await homeBox.locator('[data-start-from-video-drafted]').waitFor({ timeout: 60_000 });
  check('E: Home’s Start from finds the draft made from it earlier', /Drafted earlier/.test(await homeBox.innerText()));
  await shot(page, 'E-home-drafted-earlier-desktop-dark');

  /* ---------- F: Claude's card in the panel ---------- */
  const script = JSON.parse(readFileSync(claudeScript, 'utf8'));
  script.turns = [
    {
      match: 'Make one like the video I liked',
      steps: [
        { tool: { name: 'Bash', input: { command: `metarush footage what-it-does ${refId} --json` }, result: '{"ok":true,"data":{}}' } },
        { tool: { name: 'Bash', denied: true, input: { command: `metarush recipe from-video ${refId} --project ${project} --name like-it-in-shop-tips --json` } } },
        { text: 'Starting from that video is on a card for you.' },
      ],
    },
    ...(script.turns ?? []),
  ];
  writeFileSync(claudeScript, JSON.stringify(script));
  const asked = await api(page, 'assistant.ask', { prompt: 'Make one like the video I liked', projectId: project });
  const conv = asked.conversation.id;
  const cv = await waitFor(async () => {
    const c = await api(page, 'assistant.show', { id: conv, events: false });
    const last = c.turns[c.turns.length - 1];
    return c.state === 'idle' && last && /succeeded|failed/.test(last.state) && (c.queue ?? []).length === 0 ? c : null;
  }, 90_000, 300);
  const fv = cv?.actions.find((a) => a.kind === 'recipe from-video');
  check('F: Claude’s ask is a card, never Auto', !!fv && fv.op === 'recipe.fromVideo' && fv.state === 'pending' && /^Start a recipe from “/.test(fv.title), fv ? `${fv.title} ${fv.state}` : JSON.stringify(cv?.actions.map((a) => a.title)));
  await page.goto(`${base}/p/${project}?ask=${conv}`);
  const c1 = page.locator(`article[data-card-id="${fv.id}"]`);
  await c1.waitFor({ timeout: 30_000 });
  await c1.scrollIntoViewIfNeeded();
  check('F: the card names the video and what comes of it', (await c1.locator('[data-recipe-card-kind="fromvideo"] [data-card-video]').count()) === 1 && /kept to learn from/.test(await c1.innerText()) && /Nothing of the video goes into the draft/.test(await c1.innerText()));
  await c1.locator('[data-card-does-toggle]').click();
  const doesLines = await c1.locator('[data-card-does] li').count();
  check('F: what the video does opens on the card', doesLines >= 3, `${doesLines} lines`);
  await plainIn(c1, 'F: the card', checkPanel);
  await shot(page, 'F-panel-card-desktop-dark');
  await c1.getByRole('button', { name: 'Start the draft', exact: true }).click();
  await c1.getByText('Done', { exact: true }).waitFor({ timeout: 60_000 });
  const second = await api(page, 'recipe.show', { recipe: 'like-it-in-shop-tips' }).catch(() => null);
  check('F: a second draft, whose runs make new cuts in the project', !!second && second.recipe.current === 0 && second.version.state === 'draft' && second.doc.lands?.projectId === project, JSON.stringify(second ? { cur: second.recipe.current, state: second.version.state, lands: second.lands } : null).slice(0, 200));
  await shot(page, 'F-panel-card-done-desktop-dark');

  /* ---------- G: a phone chooses a video file; desktop light ---------- */
  {
    const { ctx: pctx, page: phone } = await newPage(browser, { theme: 'dark', phone: true, state });
    await openRecipes(phone);
    const pbox = phone.locator('[data-start-from-video]').first();
    await pbox.waitFor({ timeout: 20_000 });
    await shot(phone, 'G-add-box-phone-dark');
    await noSideways(phone, 'G phone: the Recipes tab with the Add box');
    await pbox.locator('[data-start-from-video-file]').setInputFiles(chosenFile);
    const going = await waitFor(async () => ((await pbox.locator('[data-start-from-video-result="looking"], [data-start-from-video-result="drafted"]').count()) === 1 ? true : null), 180_000, 500);
    check('G: a video file chosen on a phone comes in, and MetaRush looks at it', !!going, (await pbox.innerText().catch(() => '')).slice(0, 200));
    await shot(phone, 'G-file-looking-phone-dark');
    await noSideways(phone, 'G phone: the box while MetaRush looks');
    const chosen = (await api(phone, 'asset.list', {})).assets.find((a) => a.name === path.basename(chosenFile));
    check('G: the chosen file is a reference', !!chosen?.reference, JSON.stringify(chosen ? { id: chosen.id, reference: chosen.reference } : null));
    await pctx.close();
    if (chosen) {
      const made = await waitFor(async () => {
        const w = await api(page, 'footage.whatItDoes', { assetId: chosen.id });
        if (w.clip.startFrom?.state === 'failed') throw new Error(w.clip.startFrom.problem);
        return w.clip.startFrom?.state === 'made' ? w : null;
      }, 900_000, 3000);
      const r = made ? await api(page, 'recipe.show', { recipe: made.clip.startFrom.recipe }) : null;
      // A file has no link or channel: the draft's title is MetaRush's own words, never the clip's (D444).
      check('G: its draft is made too, titled in MetaRush’s words', !!r && r.recipe.title === 'Like a 26-second vertical video you liked' && r.recipe.current === 0 && !r.doc.reference?.url,
        JSON.stringify(r ? { title: r.recipe.title, clip: made.clip.title, current: r.recipe.current, reference: r.doc.reference } : made));
    }
  }
  {
    const { ctx: lctx, page: light } = await newPage(browser, { theme: 'light', phone: false, state });
    await light.goto(`${base}/?asset=${refId}&panel=does`);
    await light.locator('[data-what-it-does]').first().waitFor({ timeout: 30_000 });
    await light.locator('[data-what-it-does]').first().scrollIntoViewIfNeeded();
    await shot(light, 'G-clip-what-it-does-desktop-light');
    await openRecipes(light);
    await shot(light, 'G-recipes-desktop-light');
    await lctx.close();
    const { ctx: plctx, page: plight } = await newPage(browser, { theme: 'light', phone: true, state });
    await plight.goto(`${base}/?asset=${refId}&panel=does`);
    await plight.locator('[data-what-it-does]').first().waitFor({ timeout: 30_000 });
    await plight.locator('[data-what-it-does]').first().scrollIntoViewIfNeeded();
    await shot(plight, 'G-clip-what-it-does-phone-light');
    await noSideways(plight, 'G phone light: the clip’s What it does');
    await plight.goto(`${base}/library/recipes?recipe=${draftName}`);
    await plight.locator(`[data-recipe-page="${draftName}"]`).waitFor({ timeout: 20_000 });
    await shot(plight, 'G-draft-page-phone-light');
    await noSideways(plight, 'G phone light: the draft’s page');
    await plctx.close();
  }
  await ctx.close();
} catch (e) {
  check('the run finished', false, String(e?.stack ?? e));
} finally {
  await browser.close();
}
check('no native dialogs or page errors', problems.length === 0, problems.join(' | '));
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed${SHOTS ? `; screenshots in ${SHOTS}` : ''}`);
process.exit(failed.length ? 1 : 0);
