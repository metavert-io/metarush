#!/usr/bin/env node
// The Claude Code panel's navigator, lightbox, combined approvals, question
// cards, safe steps that run by themselves and the notes when their work
// finishes (D140–D144), in a real browser. Run it through scripts/cards-e2e.sh:
// a throwaway installation with a short project, a page served on this
// computer and a fake `claude` that, asked to make a lower bar and use the
// page, looks at three stills, writes a motion graphic, asks to check it, to
// bring the page in and to leave two notes, and asks a question. It checks:
//   1. the check of the graphic and the snapshot of the typed link start by
//      themselves (no click), say so, and Claude hears once their work is done;
//   2. the message box counts the cards and questions waiting and ‹ › jumps to
//      each (scrolled into view, highlighted); the conversation list and the top
//      bar show the count;
//   3. a still and the graphic's test frames open large: ‹ › and the arrow keys
//      step through them, the preview plays, Esc closes the lightbox, not the panel;
//   4. the two notes are one card: Approve all decides both and Claude hears once;
//   5. the question card takes ticks and words, and the answer is the person's reply;
//   6. the cut Claude writes next saves and renders its review copy in one click,
//      and Claude hears when the copy and its quality check are done;
//   7. everything the panel shows reads plainly (checkPanel: no file names,
//      version codes, IDs, milliseconds or tool words); toasts keep clear of the
//      message box (beside the panel, or at the top on a phone); no native
//      dialogs, page errors or horizontal overflow. Screenshots: desktop dark and
//      light, phone.
// It CHANGES the project, so point it only at a throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { checkPanel } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'PANEL_PROJECT', 'PANEL_PAGE', 'PANEL_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/cards-e2e.sh does)`);
    process.exit(2);
  }
}
const projectId = process.env.PANEL_PROJECT;
const otherProject = process.env.PANEL_OTHER_PROJECT ?? '';
const pageURL = process.env.PANEL_PAGE;
const shotsDir = process.env.PANEL_SHOTS;
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

async function newPage(browser, { theme = 'dark', mobile = false, state } = {}) {
  const ctx = await browser.newContext({
    viewport: mobile ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: mobile ? 2 : 1,
    colorScheme: theme,
    reducedMotion: 'reduce',
    hasTouch: mobile,
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

async function until(what, f, { timeout = 60_000, every = 500 } = {}) {
  const end = Date.now() + timeout;
  let last;
  while (Date.now() < end) {
    last = await f();
    if (last) return last;
    await new Promise((r) => setTimeout(r, every));
  }
  throw new Error(`timed out waiting for ${what}`);
}

/** The panel's visible text, people's own words (<Theirs>) read inside curly quotes. */
async function panelText(page) {
  return page.evaluate(() => {
    const panel = document.querySelector('aside[role="dialog"], [role="dialog"][aria-modal="true"]');
    return panel ? panel.innerText : '';
  });
}

function plainProblems(text) {
  return text
    .split('\n')
    .flatMap((l) => checkPanel(l).map((f) => `${f.kind} ${JSON.stringify(f.text)} in ${JSON.stringify(l.trim().slice(0, 120))}`));
}

const notes = (cv) => (cv.turns ?? []).filter((t) => t.note);

/** Where toasts go while the panel is open, and where the message box is. */
async function toastPlace(page) {
  return page.evaluate(() => {
    const el = document.querySelector('[data-toasts]');
    const label = [...document.querySelectorAll('label')].find((l) => l.textContent.trim() === 'Message to Claude');
    const box = label ? document.getElementById(label.htmlFor)?.getBoundingClientRect() : undefined;
    if (!el || !box) return null;
    const s = getComputedStyle(el);
    return {
      panel: document.body.dataset.askPanel ?? '',
      top: s.top,
      toastsRight: window.innerWidth - parseFloat(s.paddingRight),
      composerLeft: Math.round(box.left),
    };
  });
}

/** Toasts (a render finished elsewhere…) sit over the panel's corner for a few seconds: let them go first. */
async function noToasts(page) {
  await page
    .waitForFunction(() => document.querySelectorAll('[aria-live="polite"][aria-relevant="additions"] [role="status"]').length === 0, null, { timeout: 20_000 })
    .catch(() => {});
}

async function main() {
  const browser = await chromium.launch();
  const { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const state = await ctx.storageState();
  const settings = await api(page, 'assistant.settings');
  check('safe steps run by themselves by default', settings.safeSteps === true);
  // Usage and memory (D181): tokens, not dollars; the defaults; the section saves and its toggles stick.
  check(
    'a conversation is limited in tokens (50M), summarized at 200K, cached an hour, news held',
    settings.tokenLimitMillions === 50 && settings.compact && settings.compactAtTokens === 200000 && settings.autoCompactTokens === 300000 && settings.cacheHour && settings.holdNews === false,
    JSON.stringify({ l: settings.tokenLimitMillions, c: settings.compactAtTokens, a: settings.autoCompactTokens, h: settings.cacheHour, n: settings.holdNews }),
  );
  const home = page.url();
  await page.goto(new URL('/settings/claude#claude-usage', page.url()).toString());
  const usage = page.locator('[data-usage-settings]');
  await usage.waitFor({ timeout: 15_000 });
  check('Settings has its own usage and memory section', (await usage.locator('[data-usage-field]').count()) === 3 && (await usage.locator('[data-usage-toggle]').count()) === 3);
  await usage.locator('[data-usage-field="limit"]').fill('80');
  await usage.getByRole('button', { name: 'Save' }).click();
  await usage.getByText('Saved. New messages use it.').waitFor({ timeout: 10_000 });
  await usage.locator('[data-usage-toggle="cacheHour"]').uncheck();
  const changed = await until('the usage settings saved', async () => {
    const st = await api(page, 'assistant.settings');
    return st.tokenLimitMillions === 80 && st.cacheHour === false ? st : null;
  });
  check('the limit and the cache setting save from Settings', !!changed);
  await shot(page, 'settings-usage-desktop-dark');
  await api(page, 'assistant.settings.set', { tokenLimitMillions: 0, cacheHour: true });
  await page.goto(home);

  // Ask Claude (the fake): it looks, writes a graphic and asks for things.
  const asked = await api(page, 'assistant.ask', { prompt: `Make a lower bar, look at the opening, and use our page ${pageURL}`, projectId });
  const convId = asked.conversation.id;
  let cv = await until('the cards', async () => {
    const c = await api(page, 'assistant.show', { id: convId, events: false });
    return c.state === 'idle' && c.actions.length >= 5 ? c : null;
  });
  const byOp = (c, op) => c.actions.filter((a) => a.op === op);
  const checkCard = byOp(cv, 'motion.check')[0];
  const fetchCard = byOp(cv, 'resources.fetch')[0];
  const comments = byOp(cv, 'comment.add');
  const question = cv.actions.find((a) => a.question);
  check('the graphic check started by itself', checkCard?.auto === true && ['running', 'done'].includes(checkCard.state), `${checkCard?.state}`);
  // Started by itself; its snapshot may still be on its way (it's watched: see "the snapshot came in" below).
  check('the typed page is brought in by itself', fetchCard?.auto === true && ['running', 'done'].includes(fetchCard.state), `${fetchCard?.state} ${fetchCard?.work?.state ?? '(no work yet)'}`);
  check('the two notes and the question wait for the person', comments.length === 2 && comments.every((a) => a.state === 'pending') && question?.state === 'pending');
  check('three things wait (counted on the conversation)', cv.pendingActions === 3, `${cv.pendingActions}`);
  check('Claude was not woken when the work started', notes(cv).length === 0, `${notes(cv).length} notes`);

  // The panel.
  await page.goto(`${base}/p/${projectId}?ask=${convId}`);
  await page.locator('[data-navigator]').waitFor({ timeout: 30_000 });
  await page.waitForTimeout(600);
  const navText = await page.locator('[data-navigator]').innerText();
  check('the message box says 3 things wait', /3 things wait for you/.test(navText), navText.replace(/\s+/g, ' '));
  const topBadge = await page.locator('[data-assistant-waiting]').first().innerText().catch(() => '');
  const status = await page.evaluate(async () => (await (await fetch('/api/assistant', { credentials: 'same-origin' })).json()).data);
  check('the top bar counts what waits for the person', Number(topBadge) === Math.min(status.waiting, 9) && status.waiting >= 3, `badge ${topBadge}, waiting ${status.waiting}`);
  await shot(page, 'panel-navigator-desktop-dark');

  // ‹ › jump to each, highlighted and in view.
  const visited = [];
  for (let i = 0; i < 4; i++) {
    await noToasts(page);
    await page.getByRole('button', { name: 'Next one waiting for you' }).click();
    await page.waitForTimeout(250);
    const flashed = await page.evaluate(() => {
      const el = document.querySelector('.mr-flash');
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return { id: el.getAttribute('data-waiting'), visible: r.top < window.innerHeight && r.bottom > 0 };
    });
    visited.push(flashed);
  }
  const ids = new Set(visited.slice(0, 3).map((v) => v?.id));
  check('› visits each waiting card once, in view, then wraps', ids.size === 3 && visited.every((v) => v?.visible) && visited[3]?.id === visited[0]?.id, JSON.stringify(visited));
  await noToasts(page);
  await page.getByRole('button', { name: 'Previous one waiting for you' }).click();
  await page.waitForTimeout(250);
  check('‹ goes back', (await page.evaluate(() => document.querySelector('.mr-flash')?.getAttribute('data-waiting'))) === visited[2]?.id);

  // The conversation list carries the same count.
  await page.getByRole('button', { name: /^All conversations/ }).click();
  const row = page.locator(`[data-waiting-count="3"]`);
  const listed = await row
    .first()
    .waitFor({ timeout: 15_000 })
    .then(() => true)
    .catch(() => false);
  check('the conversation list says 3 wait', listed, listed ? (await row.first().innerText()).trim() : 'not shown');
  await shot(page, 'conversation-list-desktop-dark');
  await page.getByRole('button', { name: 'Back to the conversation' }).first().click();

  // A still opens large; ‹ › and the arrow keys step; Esc closes it, not the panel.
  const thumbs = page.locator('[aria-label="Pictures Claude looked at"] [data-thumb]');
  check('the stills Claude looked at are one step with 3 thumbnails', (await thumbs.count()) === 3 && (await page.getByText('Looked at 3 stills from', { exact: false }).count()) === 1);
  await thumbs.first().click();
  const box = page.locator('[data-mr-lightbox]');
  await box.waitFor({ timeout: 10_000 });
  const first = await box.innerText();
  await page.keyboard.press('ArrowRight');
  const second = await box.innerText();
  await box.getByRole('button', { name: 'Next (→)' }).click();
  const third = await box.innerText();
  check('the lightbox steps through the stills', /1 of 3/.test(first) && /2 of 3/.test(second) && /3 of 3/.test(third), [first, second, third].map((s) => s.replace(/\s+/g, ' ')).join(' / '));
  await shot(page, 'lightbox-desktop-dark');
  await page.keyboard.press('Escape');
  await box.waitFor({ state: 'detached', timeout: 5000 });
  check('Esc closes the lightbox and the panel stays', (await page.locator('aside[role="dialog"]').count()) === 1);

  // The safe steps finish; Claude hears once (or once per batch close together).
  const heard = (c, re) => notes(c).some((t) => re.test(t.shown ?? ''));
  cv = await until(
    'the graphic check and the snapshot to finish, and Claude to hear about both',
    async () => {
      const c = await api(page, 'assistant.show', { id: convId, events: false });
      const ck = c.actions.find((a) => a.op === 'motion.check');
      const fe = c.actions.find((a) => a.op === 'resources.fetch');
      return ck?.state === 'done' && fe?.work?.state !== 'running' && heard(c, /the check/) && heard(c, /is in the resources/) && c.state === 'idle' && !(c.queue ?? []).length
        ? c
        : null;
    },
    { timeout: 300_000, every: 1000 },
  );
  const checked = cv.actions.find((a) => a.op === 'motion.check');
  const fetched = cv.actions.find((a) => a.op === 'resources.fetch');
  check('the check drew test frames and a preview', (checked.frames ?? []).length >= 2 && !!checked.preview, `${checked.result} · ${(checked.frames ?? []).length} frames`);
  check('the snapshot came in', fetched.work?.state === 'finished', JSON.stringify(fetched.work));
  const finishNotes = notes(cv);
  check('Claude heard about them when they finished, in at most two notes', finishNotes.length <= 2 && finishNotes.every((t) => !/waiting to start|Getting a snapshot/.test(t.shown ?? '')),
    finishNotes.map((t) => t.shown).join(' | '));
  await page.waitForTimeout(800);
  const motionCard = page.locator('article[aria-label^="Claude asks: Check the graphic"]');
  await motionCard.scrollIntoViewIfNeeded();
  check('the card says it started by itself', (await motionCard.getByText('Started by itself', { exact: false }).count()) === 1);
  await motionCard.locator('[data-thumb]').first().click();
  await box.waitFor({ timeout: 10_000 });
  const frames = (checked.frames ?? []).length;
  for (let i = 0; i < frames; i++) await page.keyboard.press('ArrowRight');
  const onPreview = await box.locator('video').count();
  check('the test frames step to the preview, which plays large', onPreview === 1, `${frames} frames`);
  await shot(page, 'lightbox-preview-desktop-dark');
  await page.keyboard.press('Escape');
  await box.waitFor({ state: 'detached', timeout: 5000 });

  // The two notes are one card: Approve all decides both, Claude hears once.
  const combined = page.locator('[data-combined]');
  await combined.scrollIntoViewIfNeeded();
  check('the two notes are one combined card', (await combined.locator('li[data-waiting]').count()) === 2);
  await shot(page, 'combined-card-desktop-dark');
  await noToasts(page);
  await combined.getByRole('button', { name: 'Approve all' }).click();
  const added = (c) => notes(c).filter((t) => (t.shown ?? '').includes('Added the note'));
  cv = await until('both notes added and Claude told', async () => {
    const c = await api(page, 'assistant.show', { id: convId, events: false });
    return c.actions.filter((a) => a.op === 'comment.add').every((a) => a.state === 'done') && added(c).length >= 1 && c.state === 'idle' ? c : null;
  });
  // One message for the two notes (it may also carry anything else Claude hadn't heard yet).
  const approveNotes = added(cv);
  check('Approve all is one message to Claude, a line each for the person',
    approveNotes.length === 1 && (approveNotes[0].shown ?? '').split('\n').filter((l) => l === 'Added the note').length === 2, approveNotes.map((t) => t.shown).join(' | '));

  // The question: ticks and words; the answer is the person's reply.
  const q = page.locator('[data-question]');
  await q.scrollIntoViewIfNeeded();
  await q.getByLabel('The opening').check();
  await q.getByLabel('The end').check();
  await q.getByLabel('Or in your own words').fill('Keep it small.');
  await shot(page, 'question-card-desktop-dark');
  await noToasts(page);
  await q.getByRole('button', { name: 'Send answer' }).click();
  cv = await until('the answer and the cut Claude writes next', async () => {
    const c = await api(page, 'assistant.show', { id: convId, events: false });
    return c.state === 'idle' && c.actions.some((a) => a.op === 'cut.apply') ? c : null;
  });
  const answerTurn = cv.turns.find((t) => t.answerTo === question.id);
  check('the answer went to Claude as the person’s reply', !!answerTurn && !answerTurn.note && answerTurn.prompt === 'The opening; The end. Keep it small.', answerTurn?.prompt);
  await page.waitForTimeout(800);
  check('the thread shows it as their answer', (await page.getByText('Your answer to “Which parts should get the name bar?”').count()) === 1);

  // Save and render a review copy, in one click; Claude hears when it's checked.
  const save = page.locator('article[aria-label^="Claude asks: Save the cut"]');
  await save.scrollIntoViewIfNeeded();
  check('the save card offers save and render first', (await save.getByRole('button', { name: 'Save and render a review copy' }).count()) === 1 && (await save.getByRole('button', { name: 'Save only' }).count()) === 1);
  const notesBefore = notes(cv).length;
  await noToasts(page);
  await save.getByRole('button', { name: 'Save and render a review copy' }).click();
  await save.getByText('Working on the review copy of version 2', { exact: false }).waitFor({ timeout: 30_000 });
  await shot(page, 'save-and-render-desktop-dark');
  cv = await until(
    'the review copy and its quality check',
    async () => {
      const c = await api(page, 'assistant.show', { id: convId, events: false });
      const s = c.actions.find((a) => a.op === 'cut.apply');
      return s?.work?.state && s.work.state !== 'running' && notes(c).length === notesBefore + 1 && c.state === 'idle' ? c : null;
    },
    { timeout: 600_000, every: 2000 },
  );
  const saveNote = notes(cv)[notes(cv).length - 1];
  check('one message once the copy is checked', /^Saved as version 2\. The review copy of version 2 is ready/.test(saveNote.shown ?? ''), saveNote.shown);
  const renders = (await api(page, 'render.list', { projectId })).renders;
  check('the review copy is of version 2', renders.some((r) => r.revisionNumber === 2 && r.quality === 'review' && r.state === 'available'));

  // 8. One project per conversation (D185): the header names it; messages and decided cards say when (D186).
  await page.waitForTimeout(600);
  const headerProject = (await page.locator(`[data-header-project="${projectId}"]`).first().innerText().catch(() => '')).trim();
  check('the header names the conversation’s project', headerProject === 'In “Short test”', headerProject);
  check('the header has a labelled New conversation button', (await page.locator('[data-new-conversation]').innerText()).trim() === 'New conversation');
  const times = await page.locator('[data-message-time]').allInnerTexts();
  check('messages say when, readably', times.length >= 4 && times.every((t) => /^(Today|Yesterday) \d{1,2}:\d{2}/.test(t.trim())), `${times.length}: ${times.slice(0, 3).join(' | ')}`);
  const decidedNote = (await page.locator('article[data-decided-state="done"], article [data-decided-state="done"]').first().innerText().catch(() => '')).trim();
  const decided = await page.locator('[data-decided-state="done"]').allInnerTexts();
  check('a decided card says who and when', decided.some((t) => /^Done by you · Today \d{1,2}:\d{2}/.test(t.trim())), decided.slice(0, 3).join(' | ') || decidedNote);

  // 9. Code in a reply folds away behind See code (D186).
  await api(page, 'assistant.continue', { id: convId, prompt: 'Show me the settings', projectId });
  await until('the reply with code', async () => {
    const c = await api(page, 'assistant.show', { id: convId, events: false });
    return c.state === 'idle' && (c.turns ?? []).some((t) => t.prompt === 'Show me the settings' && t.state === 'succeeded') ? c : null;
  });
  const fold = page.locator('[data-code-fold]').last();
  await fold.waitFor({ timeout: 30_000 });
  await fold.scrollIntoViewIfNeeded();
  const folded = (await fold.innerText()).trim();
  const codeShown = await page.locator('pre code').filter({ hasText: '"width"' }).count();
  check('long code folds away behind See code (N lines)', folded === 'See code (9 lines)' && codeShown === 0, `${folded} · ${codeShown} shown`);
  await shot(page, 'code-folded-desktop-dark');
  await fold.click();
  await page.locator('pre code').filter({ hasText: '"width"' }).first().waitFor({ timeout: 5000 });
  check('See code opens it', (await page.locator('[data-code-fold]').last().innerText()).trim() === 'Hide the code');
  await page.locator('[data-code-fold]').last().click();

  // 10. Not quite… (D186): a note on the card goes to Claude at once, as your message.
  await api(page, 'assistant.continue', { id: convId, prompt: 'One more note', projectId });
  cv = await until('the new card', async () => {
    const c = await api(page, 'assistant.show', { id: convId, events: false });
    return c.state === 'idle' && c.actions.some((a) => a.op === 'comment.add' && a.state === 'pending') ? c : null;
  });
  const asked1 = cv.actions.find((a) => a.op === 'comment.add' && a.state === 'pending');
  const waitingCard = page.locator(`article[data-waiting="${asked1.id}"]`);
  await waitingCard.waitFor({ timeout: 30_000 });
  await waitingCard.scrollIntoViewIfNeeded();
  await noToasts(page);
  await waitingCard.locator('[data-not-quite]').click();
  await waitingCard.locator('[data-not-quite-box] textarea').fill('Only the first ten seconds');
  await shot(page, 'card-not-quite-open-desktop-dark');
  await waitingCard.locator('[data-not-quite-send]').click();
  cv = await until('the note to reach Claude', async () => {
    const c = await api(page, 'assistant.show', { id: convId, events: false });
    const a = c.actions.find((x) => x.id === asked1.id);
    return a?.state === 'not_quite' && (c.turns ?? []).some((t) => (t.prompt ?? '').startsWith('Not quite:') && t.state === 'succeeded') && c.state === 'idle' ? c : null;
  });
  const nq = cv.turns.find((t) => (t.prompt ?? '').startsWith('Not quite:'));
  check('Not quite went to Claude at once, as your message',
    !nq.note && nq.prompt === 'Not quite: Add a note to “Short test”. Only the first ten seconds. Make a new version with that change.' && nq.shown === 'Not quite: Add a note to “Short test”. Only the first ten seconds.',
    `${nq.prompt} / ${nq.shown}`);
  await page.waitForTimeout(600);
  const nqText = (await page.locator('[data-decided-state="not_quite"]').first().innerText().catch(() => '')).replace(/\s+/g, ' ').trim();
  check('the card says you said not quite, with the note and when', /^You said not quite: “Only the first ten seconds” · Today \d/.test(nqText), nqText);

  // 11. Auto (D186): chosen by the message box, it runs a change that can be undone by itself.
  await noToasts(page);
  await page.locator('[data-auto-mode]').click();
  await page.locator('[data-auto-menu]').waitFor({ timeout: 5000 });
  await page.locator('[data-auto-option="auto"]').check();
  await until('Auto on', async () => ((await api(page, 'assistant.mode')).mode === 'auto' ? true : null));
  await page.waitForTimeout(500);
  check('the switch says Auto', (await page.locator('[data-auto-mode]').getAttribute('data-auto-mode')) === 'auto');
  await shot(page, 'panel-auto-on-desktop-dark');
  await page.keyboard.press('Escape');
  check('Esc closes the switch, not the panel', (await page.locator('[data-auto-menu]').count()) === 0 && (await page.locator('aside[role="dialog"]').count()) === 1);
  await api(page, 'assistant.continue', { id: convId, prompt: 'Note while on Auto', projectId });
  cv = await until('Auto to run the note', async () => {
    const c = await api(page, 'assistant.show', { id: convId, events: false });
    const a = c.actions.find((x) => x.op === 'comment.add' && x.autoMode);
    return a?.state === 'done' && c.state === 'idle' && !(c.queue ?? []).length ? c : null;
  });
  const ranAuto = cv.actions.find((x) => x.op === 'comment.add' && x.autoMode);
  check('with Auto on, the note ran by itself as you', ranAuto.auto && ranAuto.result === 'Added the note', ranAuto.result);
  await page.waitForTimeout(800);
  const autoLine = (await page.locator('[data-auto-done]').last().innerText().catch(() => '')).replace(/\s+/g, ' ').trim();
  check('the card says it was done by itself under Auto', /^Done by itself — Auto is on\. Change · Today \d/.test(autoLine), autoLine);
  await page.locator('[data-auto-done]').last().scrollIntoViewIfNeeded();
  await shot(page, 'card-done-by-itself-desktop-dark');
  await api(page, 'assistant.mode.set', { mode: 'ask' });
  // A card left waiting, for the light screenshots of Not quite.
  await api(page, 'assistant.continue', { id: convId, prompt: 'A last note', projectId });
  await until('the last card', async () => {
    const c = await api(page, 'assistant.show', { id: convId, events: false });
    return c.state === 'idle' && c.actions.some((a) => a.op === 'comment.add' && a.state === 'pending') ? c : null;
  });

  // 12. The full-screen list says each conversation's project (D185).
  await noToasts(page);
  await page.getByRole('button', { name: 'Full screen' }).click();
  const rowProjects = await page.locator('aside[aria-label="Conversations"] [data-row-project]').allInnerTexts();
  check('the full-screen list says each row’s project', rowProjects.length >= 1 && rowProjects.every((t) => /Short test|Garden show|Whole workspace/.test(t)), rowProjects.join(' | '));
  await page.keyboard.press('Escape');
  await page.locator('aside[role="dialog"]').waitFor({ timeout: 5000 });

  // Everything shown reads plainly.
  await page.waitForTimeout(1000);
  const unplain = plainProblems(await panelText(page));
  check('the panel reads plainly (checkPanel)', unplain.length === 0, [...new Set(unplain)].slice(0, 8).join(' | '));
  const place = await toastPlace(page);
  check('toasts appear beside the panel, never over the message box', place?.panel === 'docked' && place.toastsRight <= place.composerLeft, JSON.stringify(place));
  await shot(page, 'panel-done-desktop-dark');

  // 13. Opening another project switches the panel to that project (D185); coming back brings this one back.
  if (otherProject) {
    const panelState = () => page.evaluate(() => JSON.parse(sessionStorage.getItem('mr.assistant.panel') || '{}'));
    await page.goto(`${base}/p/${otherProject}`);
    await page.locator(`[data-header-project="${otherProject}"]`).waitFor({ timeout: 30_000 });
    const there = await panelState();
    check('opening another project switches the panel to that project', there.conversationId !== convId && there.projectKey === otherProject, JSON.stringify(there));
    await shot(page, 'panel-other-project-desktop-dark');
    await page.goto(`${base}/p/${projectId}`);
    await page.locator(`[data-header-project="${projectId}"]`).waitFor({ timeout: 30_000 });
    const back = await panelState();
    check('back in the first project, its conversation is open again', back.conversationId === convId && back.projectKey === projectId, JSON.stringify(back));
    await page.goto(`${base}/library`);
    await page.locator(`[data-header-project="${projectId}"]`).waitFor({ timeout: 30_000 });
    check('a page outside any project keeps the conversation (and its project)', (await panelState()).conversationId === convId);
  }
  await ctx.close();

  for (const theme of ['dark', 'light']) {
    for (const mobile of [false, true]) {
      if (!mobile && theme === 'dark') continue;
      const tag = `${mobile ? 'phone' : 'desktop'}-${theme}`;
      const v = await newPage(browser, { theme, mobile, state });
      await v.page.goto(`${base}/p/${projectId}?ask=${convId}`);
      await v.page.locator('[aria-label="Conversation"]').waitFor({ timeout: 30_000 });
      await shot(v.page, `panel-${tag}`);
      if (!mobile) {
        // The header, a card with Not quite open, and the Ask first / Auto switch (D185, D186).
        const last = v.page.locator('article[data-waiting]').last();
        await last.scrollIntoViewIfNeeded();
        await last.locator('[data-not-quite]').click();
        await last.locator('[data-not-quite-box] textarea').fill('Warmer, and only at the end');
        await shot(v.page, `card-not-quite-open-${tag}`);
        await last.getByRole('button', { name: 'Cancel' }).click();
        await v.page.locator('[data-auto-mode]').click();
        await v.page.locator('[data-auto-menu]').waitFor({ timeout: 5000 });
        await shot(v.page, `panel-auto-switch-${tag}`);
        await v.page.keyboard.press('Escape');
      }
      const t = v.page.locator('[aria-label="Pictures Claude looked at"] [data-thumb]').first();
      await t.scrollIntoViewIfNeeded();
      await t.click();
      await v.page.locator('[data-mr-lightbox]').waitFor({ timeout: 10_000 });
      if (mobile) {
        const b = await v.page.locator('[data-mr-lightbox] img').boundingBox();
        check(`the lightbox fits a phone (${theme})`, !!b && b.width <= 390 && b.x >= 0, JSON.stringify(b));
        if (theme === 'dark') {
          const place = await toastPlace(v.page);
          check('on a phone, toasts appear at the top, away from the message box', place?.top === '0px', JSON.stringify(place));
        }
      }
      await shot(v.page, `lightbox-${tag}`);
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
