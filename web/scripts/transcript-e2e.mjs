#!/usr/bin/env node
// Fixing transcripts in a real browser (D145–D149). Run it through
// scripts/transcript-e2e.sh: a throwaway installation with an interview and a
// short clip of names (both transcribed on this computer), a cut with
// captions, and a fake `claude`. It checks that:
//   1. the spelling list corrects a transcript that already exists (the name said
//      “Katelyn” and written another way → “Kaytlin”, through how it is heard), the timing kept;
//   2. a word retyped in the cut's transcript keeps its time and shows as fixed;
//      who says a stretch can be changed ("this part is Lena");
//   3. find and replace lists every place with a play button first, joins two
//      words into one over the time both took, and changes only the ticked places;
//   4. a clip's history undoes a fix, and an undone fix doesn't wait for a version;
//   5. the fixes wait for a new version (the banner), which one click makes with
//      a review copy; the old version keeps its words (its plan is unchanged);
//   6. clicking a caption in the review player fixes its words; the caption
//      check lists the fixed words and a name not on the list; the captions
//      file has the fixed words without a new render;
//   7. Claude's changes are cards (the find is a read): the change everywhere
//      it is said, the spelling list, the new version; confirming them works;
//   8. the Script view and the clip's own transcript offer the same fixes;
//   9. no native dialogs, page errors or horizontal overflow; plain words.
// Screenshots: desktop and phone, dark and light, in TRANSCRIPT_SHOTS.
// It CHANGES the project, so point it only at a throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'TRANSCRIPT_PROJECT', 'TRANSCRIPT_INTERVIEW', 'TRANSCRIPT_NAMES', 'TRANSCRIPT_REVISION', 'TRANSCRIPT_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/transcript-e2e.sh does)`);
    process.exit(2);
  }
}
// The interview's words the run picks, clicks and fixes are ones its fixture says (the ground truth
// scripts/make-fixtures.sh writes), never what one recognizer happened to hear.
const truthText = readFileSync(path.join(here, '..', '..', 'tests', 'fixtures', 'truth', 'interview-a.txt'), 'utf8');
for (const w of ['started', 'garage', 'absolutely', 'doing', 'three motors', 'build', 'nobody']) {
  if (!new RegExp(`\\b${w}\\b`).test(truthText)) {
    console.error(`the interview fixture no longer says “${w}” (tests/fixtures/truth/interview-a.txt): pick words it says`);
    process.exit(1);
  }
}
const projectId = process.env.TRANSCRIPT_PROJECT;
const interview = process.env.TRANSCRIPT_INTERVIEW;
const names = process.env.TRANSCRIPT_NAMES;
const rev1 = process.env.TRANSCRIPT_REVISION;
const shotsDir = process.env.TRANSCRIPT_SHOTS;
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
    acceptDownloads: true,
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
  await page.waitForTimeout(400);
  const file = path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
}

/** Plain-words findings in what is on screen (people's own words aside). */
async function unplainIn(locator) {
  const texts = await locator.allInnerTexts();
  return texts.flatMap((t) => t.split('\n')).flatMap((l) => checkShown(l).map((f) => `${f.kind} ${f.text} in ${JSON.stringify(l.slice(0, 120))}`));
}

const wordsOf = (tx) => tx.segments.flatMap((s) => s.words ?? []);
const textOf = (tx) => tx.segments.map((s) => s.text).join(' ');
const count = (s, re) => (s.match(re) ?? []).length;
const core = (w) => w.replace(/^[^\p{L}\p{N}]+|[^\p{L}\p{N}]+$/gu, '');
const dialogOpen = (page) => page.locator('[data-mr-dialog="open"]');
async function closeDialog(page) {
  await page.keyboard.press('Escape');
  await dialogOpen(page).waitFor({ state: 'detached', timeout: 10_000 });
}
async function until(fn, ms = 20_000, step = 250) {
  const end = Date.now() + ms;
  let v;
  while (Date.now() < end) {
    v = await fn();
    if (v) return v;
    await new Promise((r) => setTimeout(r, step));
  }
  return v;
}

// A card on its own, or a row of the combined card when Claude asked for several things in one turn (D140–D144);
// once decided it shows how it went as a card of its own.
const card = (page, title) => page.locator(`[aria-label="Claude asks: ${title.replace(/"/g, '\\"')}"]`).last();

async function confirmCard(page, title, button) {
  const c = card(page, title);
  await c.scrollIntoViewIfNeeded();
  await c.getByRole('button', { name: button, exact: true }).click();
  await c.getByText('Done', { exact: true }).or(c.getByText('Did not work', { exact: true })).waitFor({ timeout: 120_000 });
  return c;
}

async function main() {
  const browser = await chromium.launch();
  const { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const state = await ctx.storageState();
  const tx = (assetId) => api(page, 'footage.transcript', { assetId, words: true });

  // What the computer heard, before anyone fixed it.
  const namesBefore = await tx(names);
  const nw = wordsOf(namesBefore);
  const mapleAt = nw.findIndex((w, i) => core(w.w) === 'Maple' && core(nw[i + 1]?.w ?? '') === 'Grove');
  const maple = nw[mapleAt];
  const grove = nw[mapleAt + 1];
  // "Katelyn" is written another way than the list's "Kaytlin" ("Caitlin", "Kaitlyn"…): the words between these.
  const between = (a, b) => {
    const i = nw.findIndex((w) => core(w.w).toLowerCase() === a);
    const j = nw.findIndex((w, k) => k > i && core(w.w).toLowerCase() === b);
    return i >= 0 && j > i + 1 ? nw.slice(i + 1, j) : [];
  };
  const kaytlin = [between('met', 'at'), between('ask', 'about'), between('and', 'will')];
  const heardAs = [...new Set(kaytlin.map((ws) => ws.map((w) => core(w.w)).join(' ')))];
  const norm = (x) => x.toLowerCase().replace(/[^\p{L}\p{N}]/gu, '');
  const otherwise = heardAs.filter((h) => norm(h) !== 'kaytlin'); // what the list can't find by itself
  check('the names clip was transcribed (as the computer heard it)', maple && grove && kaytlin.every((ws) => ws.length > 0), `${textOf(namesBefore)} (heard “${heardAs.join('”, “')}”)`);
  check('the transcriber wrote the name another way, so only “Also heard as” can correct it', otherwise.length > 0, heardAs.join(', '));
  const interviewBefore = await tx(interview);
  const iw = wordsOf(interviewBefore);
  const garage = iw.find((w) => core(w.w) === 'garage');
  const nobody = iw.find((w) => core(w.w) === 'nobody');
  check('the interview was transcribed', garage && nobody && count(textOf(interviewBefore), /\bbuild\b/g) === 2, textOf(interviewBefore).slice(0, 160));
  const plan1 = await api(page, 'revision.plan', { id: rev1 });
  const captions1 = JSON.stringify(plan1.captions);
  const lines1 = plan1.captions.lines.map((l) => l.text).join(' / ');
  check('version 1 has captions of what the computer heard', /garage/.test(lines1) && /Maple Grove/.test(lines1) && !/Kaytlin/.test(lines1), lines1);

  // 1. The spelling list, from the studio header (the project's Settings tab, its
  // Content Settings, D193); it fixes the transcripts there already are.
  await page.goto(`${base}/p/${projectId}?tab=cut`);
  await page.locator('[data-spelling-open]').click();
  const sp = page.locator('[data-content-part="spelling"] [data-spelling-list]');
  await sp.waitFor({ timeout: 20_000 });
  check('the header’s Spelling opens the Settings tab', /tab=settings/.test(page.url()) && /part=spelling/.test(page.url()), page.url());
  await sp.locator('[data-spelling-word]').fill('Kaytlin');
  await sp.getByLabel(/Also heard as/).fill(otherwise.join(', '));
  await shot(page, 'spelling-list-add-desktop-dark');
  await sp.locator('[data-spelling-add]').click();
  await sp.locator('[data-spelling-entry="Kaytlin"]').waitFor({ timeout: 30_000 });
  const namesSpelled = await until(async () => {
    const t = await tx(names);
    return count(textOf(t), /\bKaytlin\b/g) === 3 ? t : null;
  });
  const ns = namesSpelled ? wordsOf(namesSpelled).filter((w) => core(w.w) === 'Kaytlin') : [];
  check(
    'the spelling list corrected the clip: “Kaytlin” three times, each where it was said',
    ns.length === 3 && ns.every((w, i) => w.f === kaytlin[i][0].f && w.t === kaytlin[i][kaytlin[i].length - 1].t && w.x),
    namesSpelled ? textOf(namesSpelled) : 'not corrected',
  );
  const spUnplain = await unplainIn(sp);
  check('the spelling list reads plainly', spUnplain.length === 0, spUnplain.join(' | '));
  await shot(page, 'spelling-list-desktop-dark');
  await page.goto(`${base}/p/${projectId}?tab=cut`);

  // 2. A word retyped in the cut's transcript (double-click it).
  await page.getByRole('tablist', { name: 'Beside the player' }).getByRole('tab', { name: 'Transcript' }).click();
  const box = page.locator('[data-cut-transcript]');
  await box.locator('[data-wi]').first().waitFor({ timeout: 30_000 });
  const word = (scope, re) => scope.locator('[data-wi]').filter({ hasText: re }).first();
  await word(box, /^garage,?\s*$/).dblclick();
  const ed = page.locator('[data-word-selection] [data-fix-words]');
  await ed.waitFor();
  check('double-clicking a word opens its fix with the word in it', (await ed.locator('[data-fix-words-input]').inputValue()).trim() === garage.w.trim());
  await ed.locator('[data-fix-words-input]').fill('workshop,');
  await shot(page, 'fix-a-word-desktop-dark');
  await ed.locator('[data-fix-words-save]').click();
  await box.locator('[data-wi][data-fixed]').filter({ hasText: /^workshop,?\s*$/ }).first().waitFor({ timeout: 20_000 });
  await ed.waitFor({ state: 'detached', timeout: 10_000 });
  const ws = wordsOf(await tx(interview)).find((w) => core(w.w) === 'workshop');
  check('the retyped word keeps its time and is marked as written by a person', ws && ws.f === garage.f && ws.t === garage.t && ws.x, JSON.stringify(ws));
  const toast = page.locator('[role="status"]').filter({ has: page.getByRole('button', { name: 'Undo' }) }).filter({ hasText: /workshop/ });
  const toastText = await toast.first().innerText({ timeout: 5000 }).catch(() => '');
  check('a toast says what changed, with an undo', /workshop/.test(toastText) && /Undo/.test(toastText), toastText.replace(/\s+/g, ' '));
  await shot(page, 'fixed-word-desktop-dark');

  // "This part is Lena": who says a stretch.
  const start = box.locator('[data-clip-words="start"]');
  await word(start, /^absolutely\s*$/).click();
  await word(start, /^doing\.?\s*$/).click({ modifiers: ['Shift'] });
  await page.locator('[data-word-selection] [data-who-says-open]').click();
  const who = page.locator('[data-stretch-speaker]');
  await who.getByRole('combobox').fill('Lena');
  await shot(page, 'who-says-this-desktop-dark');
  await who.getByRole('option', { name: /Someone new/ }).click();
  await who.waitFor({ state: 'detached', timeout: 90_000 });
  const afterWho = await tx(interview);
  const segName = (t, re) => {
    const names = new Map(t.speakers.map((v) => [v.label, v.displayName]));
    return t.segments.filter((sg) => re.test(sg.text)).map((sg) => sg.speakerName || names.get(sg.speaker) || '');
  };
  const stretch = segName(afterWho, /^absolutely/);
  check('that stretch is Lena now (its own line)', stretch.length === 1 && stretch[0] === 'Lena', `${stretch} / ${segName(afterWho, /started/)}`);

  // 3. Find and replace: a join, then only the ticked places.
  await page.locator('[data-find-replace-open]').click();
  let fr = dialogOpen(page).locator('[data-find-replace]');
  await fr.locator('[data-find-input]').fill('Maple Grove');
  await fr.locator('[data-replace-input]').fill('Maplegrove');
  await fr.locator('[data-find-hit]').first().waitFor({ timeout: 20_000 });
  await fr.locator('[data-find-hit]').getByText('Maplegrove', { exact: true }).first().waitFor({ timeout: 10_000 });
  const hit = fr.locator('[data-find-hit]').first();
  check('every place is listed with the words around it and a play button, before anything changes', (await fr.locator('[data-find-hit]').count()) === 1 && (await hit.getByRole('button').count()) >= 1 && /on Elmwood/.test(await hit.innerText()), (await hit.innerText()).replace(/\s+/g, ' '));
  const frUnplain = await unplainIn(fr);
  check('find and replace reads plainly', frUnplain.length === 0, frUnplain.join(' | '));
  await shot(page, 'find-replace-join-desktop-dark');
  await fr.locator('[data-replace-apply]').click();
  await fr.waitFor({ state: 'detached', timeout: 20_000 });
  const hh = wordsOf(await tx(names)).find((w) => core(w.w) === 'Maplegrove');
  check('“Maple Grove” → “Maplegrove”: one word over the time both took', hh && hh.f === maple.f && hh.t === grove.t && hh.x, JSON.stringify(hh));

  await page.locator('[data-find-replace-open]').click();
  fr = dialogOpen(page).locator('[data-find-replace]');
  await fr.locator('[data-find-input]').fill('build');
  await fr.locator('[data-replace-input]').fill('make');
  await until(async () => (await fr.locator('[data-find-hit]').count()) === 2, 20_000);
  check('“build” is said twice (whole words: not in “building”)', (await fr.locator('[data-find-hit]').count()) === 2);
  await fr.locator('[data-find-hit]').nth(1).locator('input[type="checkbox"]').uncheck();
  const label = (await fr.locator('[data-replace-apply]').innerText()).trim();
  check('unticking a place leaves it alone', label === 'Change 1 place' && /Only the 1 ticked place will change\./.test(await fr.innerText()), label);
  await shot(page, 'find-replace-ticks-desktop-dark');
  await fr.locator('[data-replace-apply]').click();
  await fr.waitFor({ state: 'detached', timeout: 20_000 });
  const afterBuild = textOf(await tx(interview));
  check('only the ticked place changed', count(afterBuild, /\bmake\b/g) === 1 && count(afterBuild, /\bbuild\b/g) === 1 && /would you make if/.test(afterBuild), afterBuild.slice(0, 200));

  // 4. The clip's history: undo the "make".
  await box.locator('[data-clip-words="question"] [data-clip-history]').click();
  const hist = dialogOpen(page).locator('[data-transcript-history]');
  await hist.locator('[data-history-entry]').first().waitFor({ timeout: 20_000 });
  const entries = await hist.locator('[data-history-entry]').allInnerTexts();
  check('the history lists every fix with who and when', entries.length >= 3 && entries.some((t) => /workshop/.test(t)) && entries.some((t) => /Lena/.test(t)) && entries.some((t) => /make/.test(t)), entries.map((t) => t.replace(/\s+/g, ' ')).join(' | '));
  const hUnplain = await unplainIn(hist);
  check('the history reads plainly', hUnplain.length === 0, hUnplain.join(' | '));
  await shot(page, 'history-desktop-dark');
  await hist.locator('[data-history-entry]').filter({ hasText: /make/ }).first().locator('[data-history-undo]').click();
  const undone = await until(async () => {
    const t = textOf(await tx(interview));
    return count(t, /\bbuild\b/g) === 2 && count(t, /\bmake\b/g) === 0 ? t : null;
  });
  check('undo puts “build” back', !!undone);
  await hist.locator('[data-history-entry]').filter({ hasText: /Who says/ }).first().locator('[data-history-undo]').click();
  const unsaid = await until(async () => {
    const t = await tx(interview);
    return segName(t, /./).includes('Lena') ? null : t;
  }, 60_000);
  check('undoing who says the stretch gives every word back to the voice that had it', !!unsaid, unsaid ? '' : segName(await tx(interview), /./).join(', '));
  await until(async () => (await hist.locator('[data-history-entry]').filter({ hasText: /Who says/ }).locator('[data-history-undo]').count()) === 0, 20_000);
  await shot(page, 'history-undone-desktop-dark');
  await closeDialog(page);

  // 5. The fixes wait for a new version; one click makes it, and a review copy.
  await page.reload();
  const banner = page.locator('[data-pending-fixes]');
  await banner.waitFor({ timeout: 20_000 });
  const pend = await api(page, 'transcript.pending', { projectId });
  check('the banner says how many text fixes wait for a new version', pend.count === 5 && (await banner.innerText()).includes(pend.summary), `${pend.count}: ${pend.summary}`);
  check('undone fixes do not wait for a version', !pend.places.some((p) => p.kind === 'speaker' || /\bmake\b/.test(`${p.afterText} ${p.beforeText}`)), pend.places.map((p) => `${p.kind} ${p.beforeText}→${p.afterText}`).join(', '));
  await banner.getByRole('button', { name: 'See them' }).click();
  await banner.locator('[data-pending-places] li').first().waitFor();
  const bUnplain = await unplainIn(banner);
  check('the banner reads plainly', bUnplain.length === 0, bUnplain.join(' | '));
  await shot(page, 'pending-fixes-desktop-dark');
  const rendersBefore = (await api(page, 'project.show', { id: projectId })).renders.length;
  await banner.locator('[data-pending-apply]').click();
  await banner.waitFor({ state: 'detached', timeout: 60_000 });
  const revs = (await api(page, 'revision.list', { projectId })).revisions;
  const head = revs.find((r) => r.isHead);
  check('one new version for all of them', revs.length === 2 && head?.number === 2, `${revs.length} versions`);
  const shown = await until(async () => {
    const d = await api(page, 'project.show', { id: projectId });
    return d.renders.length > rendersBefore ? d : null;
  }, 20_000) ?? (await api(page, 'project.show', { id: projectId }));
  check('…and one review copy of it, nothing else rendering', shown.renders.length === rendersBefore + 1 && shown.renders.some((r) => r.revisionId === head?.id && r.quality === 'review'), shown.renders.map((r) => `${r.revisionId === head?.id ? 'v2' : 'v1'} ${r.quality} ${r.state}`).join(', '));
  const plan2 = await api(page, 'revision.plan', { id: head.id });
  const lines2 = plan2.captions.lines.map((l) => l.text).join(' / ');
  check('the new version has the fixed words in its captions', /workshop/.test(lines2) && /Maplegrove/.test(lines2) && count(lines2, /Kaytlin/g) >= 1 && !/garage|Maple Grove|Caitlin|Katelyn/.test(lines2), lines2);
  check('version 1 keeps the words it was made with', JSON.stringify((await api(page, 'revision.plan', { id: rev1 })).captions) === captions1);

  // 6. Version 1's review page: click a caption to fix it.
  await page.goto(`${base}/p/${projectId}/r/${rev1}?clip=question`);
  const surface = page.locator('[data-fix-captions]');
  await surface.waitFor({ timeout: 30_000 });
  await page.waitForTimeout(3000); // the page seeks to the clip once the plan is in
  check('the review player says captions can be clicked', (await page.getByText('Click a caption to fix its words.').count()) === 1);
  const target = surface.locator('[data-caption-line]').filter({ hasText: /nobody/ });
  let clicked = false;
  for (let i = 0; i < 2 && !clicked; i++) {
    if (!(await target.count())) {
      const b = await surface.boundingBox();
      if (b) await page.mouse.click(b.x + b.width / 2, b.y + b.height * 0.3); // play
      await until(async () => (await target.count()) > 0, 15_000, 50);
    }
    if (await target.count()) {
      try {
        await target.first().click({ timeout: 2000 });
      } catch {
        await target.first().dispatchEvent('pointerdown');
      }
      clicked = await page.locator('[data-caption-fix]').waitFor({ timeout: 5000 }).then(() => true, () => false);
    }
  }
  check('clicking a caption opens its fix', clicked);
  if (clicked) {
    const fix = page.locator('[data-caption-fix]');
    const input = fix.locator('[data-fix-words-input]');
    await input.waitFor({ timeout: 10_000 });
    const before = await input.inputValue();
    check('the fix shows the words of that caption', /nobody/.test(before) && /This version says/.test(await fix.innerText()), before);
    await input.fill(before.replace(/\bnobody\b/, 'no one'));
    await shot(page, 'caption-fix-desktop-dark');
    await fix.locator('[data-fix-words-save]').click();
    await fix.waitFor({ state: 'detached', timeout: 20_000 });
    const split = wordsOf(await tx(interview));
    const k = split.findIndex((w, i) => core(w.w) === 'no' && core(split[i + 1]?.w ?? '') === 'one');
    check('“nobody” → “no one”: two words over the time it took', k >= 0 && split[k].f === nobody.f && split[k + 1].t === nobody.t, k >= 0 ? JSON.stringify(split.slice(k, k + 2)) : 'not split');
  }

  // The caption check: the words fixed since this version, and a name to look at.
  await page.locator('[data-caption-check-open]').click();
  const cc = dialogOpen(page).locator('[data-caption-check]');
  await cc.locator('[data-caption-issue]').first().waitFor({ timeout: 20_000 });
  const fixedItems = await cc.locator('[data-caption-issue="fixed"]').allInnerTexts();
  const nameItems = await cc.locator('[data-caption-issue="name"]').allInnerTexts();
  const has = (a, b) => fixedItems.some((t) => t.includes(`“${a}`) && t.includes(`“${b}`));
  check('the caption check lists the words fixed since this version', has('garage', 'workshop') && has('Maple Grove', 'Maplegrove') && otherwise.every((h) => has(h, 'Kaytlin')) && has('nobody', 'no one'), fixedItems.map((t) => t.replace(/\s+/g, ' ')).join(' | '));
  check('…and a name that is not on the spelling list', nameItems.some((t) => /“Elmwood” appears once/.test(t)), nameItems.join(' | '));
  const ccUnplain = await unplainIn(cc);
  check('the caption check reads plainly', ccUnplain.length === 0, ccUnplain.join(' | '));
  await shot(page, 'caption-check-desktop-dark');
  await closeDialog(page);

  // The captions file: fixed words without a new render.
  const [dl] = await Promise.all([page.waitForEvent('download', { timeout: 20_000 }), page.locator('[data-captions-file]').click()]);
  const srtFile = path.join(shotsDir, 'version-1.srt');
  await dl.saveAs(srtFile);
  const srt = readFileSync(srtFile, 'utf8');
  check('the captions file has every fix, without a new render', /^1\r?\n00:00:0\d,\d{3} --> /.test(srt) && /workshop/.test(srt) && /Maplegrove/.test(srt) && /Kaytlin/.test(srt) && /no one/.test(srt), srt.slice(0, 120).replace(/\n/g, '⏎'));
  const rendersNow = (await api(page, 'project.show', { id: projectId })).renders.length;
  check('nothing rendered by itself', rendersNow === rendersBefore + 1, `${rendersNow} renders`);

  // 7. Claude's changes as cards.
  const asked = await api(page, 'assistant.ask', { prompt: 'Numbers as digits please, and Elmwood is spelled right', projectId });
  const convId = asked.conversation.id;
  const cv = await until(async () => {
    const c = await api(page, 'assistant.show', { id: convId, events: false });
    return c.state === 'idle' && c.actions.length >= 3 ? c : null;
  }, 60_000);
  const kinds = (cv?.actions ?? []).map((a) => `${a.kind}: ${a.title}`);
  check('Claude asked for 3 changes, each a card (its look was a read)', cv?.actions.length === 3, kinds.join(' | '));
  const replace = cv?.actions.find((a) => a.kind === 'transcript replace');
  const spell = cv?.actions.find((a) => a.kind === 'transcript spelling add');
  const make = cv?.actions.find((a) => a.kind === 'transcript pending apply');
  check('the change card counts the places and videos', replace?.title === 'Change “three” to “3” in 1 place in 1 video' && replace.details.some((d) => /“three” motors/.test(d)), replace?.title);
  check('the spelling card names the list', spell?.title === "Add “Elmwood” to the spelling list of “Garden story”'s settings", spell?.title);
  check('the new-version card counts the fixes', /^Make version 3 of “Garden story” with (1 text fix|\d+ text fixes), and a review copy$/.test(make?.title ?? ''), make?.title);
  check('nothing changed before anyone confirmed', /\bthree motors\b/.test(textOf(await tx(interview))));
  await page.goto(`${base}/p/${projectId}?ask=${convId}`);
  await page.locator('article[aria-label^="Claude asks"]').first().waitFor({ timeout: 30_000 });
  await page.waitForTimeout(800);
  const cardsUnplain = await unplainIn(page.locator('article[aria-label^="Claude asks"]'));
  check('every card reads plainly', cardsUnplain.length === 0, cardsUnplain.join(' | '));
  await shot(page, 'cards-desktop-dark');
  for (const [a, button] of [[replace, 'Change them'], [spell, 'Add it'], [make, 'Make the version']]) {
    if (!a) continue;
    const c = await confirmCard(page, a.title, button);
    check(`confirmed: ${a.title}`, (await c.getByText('Done', { exact: true }).count()) === 1, (await c.innerText()).replace(/\s+/g, ' ').slice(0, 200));
  }
  check('the change is in the transcript', /\b3 motors\b/.test(textOf(await tx(interview))));
  const lists = await api(page, 'transcript.spelling.list', { projectId });
  check('the spelling list has both words', ['Kaytlin', 'Elmwood'].every((w) => lists.entries.some((e) => e.word === w)), lists.entries.map((e) => e.word).join(', '));
  const revs3 = (await api(page, 'revision.list', { projectId })).revisions;
  check('the card made version 3', revs3.length === 3 && revs3.find((r) => r.isHead)?.number === 3, `${revs3.length} versions`);
  await shot(page, 'cards-done-desktop-dark');
  await page.keyboard.press('Escape');
  await page.locator('aside[role="dialog"]').waitFor({ state: 'detached', timeout: 10_000 }).catch(() => {});

  // The old address of the workspace's list (Settings → Footage → Spelling list)
  // opens the default's Content Settings (D193): this project's, the first one.
  await page.goto(`${base}/settings/footage#spelling`);
  const wsl = page.locator('[data-content-part="spelling"] [data-spelling-list]');
  await wsl.waitFor({ timeout: 20_000 });
  check('Settings → Footage → Spelling opens the default’s Content Settings', page.url().includes(`/p/${projectId}`) && /tab=settings/.test(page.url()), page.url());
  await wsl.scrollIntoViewIfNeeded();
  await wsl.locator('[data-spelling-word]').fill('Maplegrove');
  await wsl.locator('[data-spelling-add]').click();
  await wsl.locator('[data-spelling-entry="Maplegrove"]').waitFor({ timeout: 20_000 });
  await shot(page, 'settings-spelling-desktop-dark');
  const both = await api(page, 'transcript.spelling.list', {});
  check("the default's list (footage in no project uses it) is this project's", both.entries.some((e) => e.word === 'Maplegrove') && both.entries.some((e) => e.word === 'Kaytlin'), both.entries.map((e) => e.word).join(', '));

  // 8. The Script view and the clip's own transcript offer the same fixes.
  await page.goto(`${base}/p/${projectId}?tab=script`);
  await page.locator('[data-script-fix]').first().click();
  const cw = dialogOpen(page).locator('[data-clip-words-fix]');
  await cw.waitFor({ timeout: 20_000 });
  await shot(page, 'script-view-fix-desktop-dark');
  await closeDialog(page);
  await page.goto(`${base}/p/${projectId}?tab=sources&asset=${names}`);
  const fixedInSource = page.locator('[data-wi][data-fixed]').filter({ hasText: /^Kaytlin/ });
  await fixedInSource.first().waitFor({ timeout: 30_000 });
  check("the clip's own transcript shows the fixed words", (await fixedInSource.count()) === 3);
  await shot(page, 'source-transcript-desktop-dark');
  await ctx.close();

  // Phone and light.
  for (const theme of ['dark', 'light']) {
    for (const mobile of [false, true]) {
      if (!mobile && theme === 'dark') continue;
      const tag = `${mobile ? 'phone' : 'desktop'}-${theme}`;
      const v = await newPage(browser, { theme, mobile, state });
      await v.page.goto(`${base}/p/${projectId}?tab=cut`);
      await v.page.getByRole('tablist', { name: 'Beside the player' }).getByRole('tab', { name: 'Transcript' }).click();
      const vb = v.page.locator('[data-cut-transcript]');
      await vb.locator('[data-wi]').first().waitFor({ timeout: 30_000 });
      await word(vb, /^workshop,?\s*$/).scrollIntoViewIfNeeded();
      await word(vb, /^workshop,?\s*$/).dblclick();
      await v.page.locator('[data-word-selection] [data-fix-words]').waitFor();
      await shot(v.page, `fix-a-word-${tag}`);
      await v.page.keyboard.press('Escape');
      await v.page.locator('[data-find-replace-open]').click();
      const vf = dialogOpen(v.page).locator('[data-find-replace]');
      await vf.locator('[data-find-input]').fill('Kaytlin');
      await vf.locator('[data-replace-input]').fill('Kaytlyn');
      await vf.locator('[data-find-hit]').first().waitFor({ timeout: 20_000 });
      await shot(v.page, `find-replace-${tag}`);
      await closeDialog(v.page);
      await v.page.locator('[data-spelling-open]').click();
      await v.page.locator('[data-content-part="spelling"] [data-spelling-entry="Kaytlin"]').waitFor({ timeout: 20_000 });
      await v.page.locator('[data-content-part="spelling"]').scrollIntoViewIfNeeded();
      await shot(v.page, `spelling-list-${tag}`);
      await v.page.goto(`${base}/p/${projectId}/r/${rev1}`);
      await v.page.locator('[data-caption-tools]').waitFor({ timeout: 30_000 });
      await v.page.locator('[data-caption-check-open]').click();
      await dialogOpen(v.page).locator('[data-caption-issue]').first().waitFor({ timeout: 20_000 });
      await shot(v.page, `caption-check-${tag}`);
      await v.ctx.close();
    }
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
