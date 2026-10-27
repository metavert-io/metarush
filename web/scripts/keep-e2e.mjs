#!/usr/bin/env node
// "Keep this?" cards, attachments and what a project keeps, in a real browser
// (D166, docs/keeping.md). Run it through scripts/keep-e2e.sh: a throwaway
// installation with a project ("Garden club") whose cut opens on a split-second
// of another picture, and a fake `claude` that, told about the garden club, asks
// to keep a rule, a check, a sign-off and the brief. It checks that:
//   1. the message box takes pictures and PDFs from the paperclip, a drop and a
//      paste, shows each as a chip (with a remove ×), refuses other files in plain
//      words, and a drop on it doesn't add footage to the project;
//   2. the sent message shows its picture and PDF; the link typed, the picture and
//      the PDF become MetaRush's own "Keep this?" cards beside Claude's four;
//   3. every keep card stands on its own (never in a combined card), reads plainly,
//      and the navigator by the message box counts and jumps to them;
//   4. Keep keeps (the rule, the sign-off, the brief, the logo) and Not now sets
//      aside for good ("Not kept; MetaRush won't ask again");
//   5. the project's Settings tab ("What it keeps" before D193; ?tab=memory still
//      opens it) lists, under This project, the rules (what the quality check
//      measures), the sign-offs, where the video goes (with shapes) and what was
//      offered; adding a rule and a place, "Keep after all" and ticking work;
//   6. the studio offers "Trim the flashes" for the split-second flash and the fix
//      saves a version without it; approving reminds of the open sign-off once
//      ("Approve anyway" / "Not yet") and the toast repeats the reminder;
//   7. no native dialogs, page errors or horizontal overflow; 16px gutters on a phone.
// Screenshots: the panel and the project tab, desktop dark and light, and phone
// (390px), in KEEP_SHOTS. It CHANGES the project: point it only at a throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { crc32, deflateSync } from 'node:zlib';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkPanel, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'KEEP_PROJECT', 'KEEP_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/keep-e2e.sh does)`);
    process.exit(2);
  }
}
const projectId = process.env.KEEP_PROJECT;
const shotsDir = process.env.KEEP_SHOTS;
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

const shots = [];
let shotN = 0;
async function shot(page, name) {
  await page.waitForTimeout(450);
  const file = path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file });
  shots.push(file);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
}

/** The text a person reads in an element, people's own words (<Theirs>) in curly quotes. */
async function readText(page, selector) {
  return page.evaluate(
    ([sel, open, close]) => {
      const root = document.querySelector(sel);
      if (!root) return '';
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
    [selector, THEIRS_OPEN, THEIRS_CLOSE],
  );
}

function unplain(text, rules = checkShown) {
  return text
    .split('\n')
    .flatMap((l) => rules(l).map((f) => `${f.kind} ${JSON.stringify(f.text)} in ${JSON.stringify(l.trim().slice(0, 120))}`));
}

async function until(fn, ms = 30_000, step = 250) {
  const end = Date.now() + ms;
  let v;
  while (Date.now() < end) {
    v = await fn();
    if (v) return v;
    await new Promise((r) => setTimeout(r, step));
  }
  return v;
}

/* ---------- Files to attach, made here ---------- */

/** A PNG (RGBA) drawn by pixel(x, y). */
function png(w, h, pixel) {
  const row = w * 4 + 1;
  const raw = Buffer.alloc(row * h);
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const [r, g, b, a] = pixel(x, y);
      const o = y * row + 1 + x * 4;
      raw[o] = r;
      raw[o + 1] = g;
      raw[o + 2] = b;
      raw[o + 3] = a;
    }
  }
  const chunk = (type, data) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const td = Buffer.concat([Buffer.from(type, 'latin1'), data]);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(crc32(td) >>> 0);
    return Buffer.concat([len, td, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // RGBA
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', ihdr), chunk('IDAT', deflateSync(raw)), chunk('IEND', Buffer.alloc(0))]);
}

// The garden club's logo: a green leaf disc with a sun in it, on a clear background.
const logo = png(240, 160, (x, y) => {
  const d = Math.hypot(x - 120, y - 80);
  if (d < 26) return [245, 200, 66, 255];
  if (d < 70) return [46, 125, 80, 255];
  return [0, 0, 0, 0];
});
const screenshotPng = png(64, 40, (x) => (x < 32 ? [40, 40, 48, 255] : [220, 220, 228, 255]));
const pdf = Buffer.from('%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n2 0 obj << /Type /Pages /Kids [] /Count 0 >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n', 'latin1');

/** Drops (or pastes) a file onto an element the way a browser does, with a DataTransfer. */
async function dropFile(page, selector, file, how = 'drop') {
  await page.evaluate(
    async ([sel, name, type, b64, how]) => {
      const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
      const dt = new DataTransfer();
      dt.items.add(new File([bytes], name, { type }));
      const el = document.querySelector(sel);
      if (!el) throw new Error(`no ${sel}`);
      if (how === 'paste') {
        el.dispatchEvent(new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: dt }));
        return;
      }
      for (const t of ['dragenter', 'dragover', 'drop']) el.dispatchEvent(new DragEvent(t, { bubbles: true, cancelable: true, dataTransfer: dt }));
    },
    [selector, file.name, file.mimeType, file.buffer.toString('base64'), how],
  );
}

const keepCard = (page, kind, title) => page.locator(title ? `article[data-keep="${kind}"][aria-label$="${title.replace(/"/g, '\\"')}"]` : `article[data-keep="${kind}"]`);

async function decide(page, card, yes) {
  await card.scrollIntoViewIfNeeded();
  await card.locator(yes ? '[data-keep-yes]' : '[data-keep-no]').click();
  await until(async () => {
    const s = await card.getAttribute('data-keep-state');
    return s === 'done' || s === 'dismissed' || s === 'failed';
  }, 60_000);
  return { state: await card.getAttribute('data-keep-state'), text: (await card.innerText()).replace(/\s+/g, ' ') };
}

async function main() {
  const browser = await chromium.launch();
  const { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const state = await ctx.storageState();

  /* ---------- 1. The message box ---------- */
  await page.goto(`${base}/p/${projectId}`);
  await page.getByRole('button', { name: /^Ask Claude Code/ }).click();
  const box = page.locator('[data-composer]');
  await box.waitFor({ timeout: 20_000 });
  await box.locator('[data-attach-input]').setInputFiles([{ name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('plain words') }]);
  const refused = await box.locator('[data-attach-note]').innerText().catch(() => '');
  check('a file that is not a picture or a PDF is refused in plain words', /“notes\.txt” isn't a picture or a PDF/.test(refused) && checkShown(refused).length === 0, refused);
  await box.locator('[data-attach-input]').setInputFiles([{ name: 'garden-logo.png', mimeType: 'image/png', buffer: logo }]);
  await dropFile(page, '[data-composer]', { name: 'club-flyer.pdf', mimeType: 'application/pdf', buffer: pdf });
  await dropFile(page, '[data-composer] textarea', { name: 'screenshot.png', mimeType: 'image/png', buffer: screenshotPng }, 'paste');
  await until(async () => (await box.locator('[data-attachment="ready"]').count()) === 3, 30_000);
  const chips = await box.locator('[data-attachment]').allInnerTexts();
  check('the paperclip, a drop and a paste each attach a file (a chip with its name)', chips.length === 3 && chips.some((t) => t.includes('garden-logo.png')) && chips.some((t) => t.includes('club-flyer.pdf')) && chips.some((t) => t.includes('screenshot.png')), chips.join(' | ').replace(/\s+/g, ' '));
  const thumbs = await box.locator('[data-attachment] img').evaluateAll((els) => els.map((e) => e.naturalWidth));
  check('picture chips show the picture before it is sent', thumbs.length === 2 && thumbs.every((w) => w > 0), JSON.stringify(thumbs));
  const dropToast = await page.locator('[data-toasts]').innerText();
  check("a drop on the message box doesn't add footage to the project", !/Adding|Uploading/.test(dropToast), dropToast.replace(/\s+/g, ' '));
  await box.locator('[data-attachment]', { hasText: 'screenshot.png' }).getByRole('button', { name: /^Remove/ }).click();
  check('the × takes a file off the message', (await box.locator('[data-attachment]').count()) === 2);
  await box.locator('textarea').fill('Make a short video about our garden club for the website. Our site is acme-garden.org.');
  await shot(page, 'composer-with-files-desktop-dark');
  await box.getByRole('button', { name: 'Send message' }).click();

  /* ---------- 2. The cards ---------- */
  const convId = await until(async () => (await api(page, 'assistant.list', { projectId })).conversations?.[0]?.id, 20_000);
  let cv;
  await until(async () => {
    cv = await api(page, 'assistant.show', { id: convId, events: false });
    return cv.state === 'idle' && (cv.actions ?? []).filter((a) => a.keeping).length >= 7;
  }, 90_000);
  const keeps = (cv.actions ?? []).filter((a) => a.keeping);
  check('Claude asked to keep 4 things and MetaRush noticed 3 (the link, the picture, the PDF)', keeps.length === 7 && keeps.filter((a) => a.keeping.from === 'metarush').length === 3, keeps.map((a) => `${a.keeping.kind}/${a.keeping.from}: ${a.title}`).join(' | '));
  check('the turn keeps its two files', (cv.turns?.[0]?.attachments ?? []).length === 2, JSON.stringify(cv.turns?.[0]?.attachments?.map((f) => f.name)));

  await page.locator('article[data-keep]').first().waitFor({ timeout: 30_000 });
  await page.waitForTimeout(800);
  const bubblePic = page.locator('[data-message-files] [data-message-file="picture"] img');
  await until(async () => (await bubblePic.evaluateAll((els) => els.map((e) => e.naturalWidth))).some((w) => w > 0), 15_000);
  check('the message shows its picture and its PDF', (await bubblePic.count()) === 1 && (await page.locator('[data-message-files] [data-message-file="pdf"]').count()) === 1);
  const cards = await page.locator('article[data-keep]').count();
  const inCombined = await page.locator('[data-combined] [data-keep], [data-combined] li[data-waiting]').count();
  check('each keep card stands on its own (none in a combined card)', cards === 7 && inCombined === 0, `${cards} cards, ${inCombined} combined`);
  const nav = page.locator('[data-navigator]');
  check('the navigator counts the keep cards', /7 things wait for you/.test(await nav.innerText().catch(() => '')), await nav.innerText().catch(() => 'no navigator'));
  await nav.getByRole('button', { name: 'Next one waiting for you' }).click();
  await page.waitForTimeout(300);
  const landed = await page.evaluate(() => document.querySelector('.mr-flash')?.getAttribute('data-keep') ?? '');
  check('the navigator jumps to a keep card', landed !== '', landed);
  const panelText = await readText(page, 'aside[role="dialog"]');
  const unplainCards = unplain(panelText, checkPanel);
  check('the panel reads plainly (people’s words aside)', unplainCards.length === 0, unplainCards.join(' | '));
  const logoCard = keepCard(page, 'picture', 'Keep this logo in the sources and the brand kit?');
  await logoCard.scrollIntoViewIfNeeded();
  const logoImg = await logoCard.locator('img').evaluate((e) => e.naturalWidth).catch(() => 0);
  check('the logo card shows the picture', logoImg > 0 && (await logoCard.innerText()).includes('garden-logo.png'));
  const pdfCard = (await keepCard(page, 'picture', "Keep this PDF in the project's sources?").innerText()).replace(/\s+/g, ' ');
  check('the PDF card names the file', pdfCard.includes('club-flyer.pdf'), pdfCard);
  await shot(page, 'panel-keep-cards-desktop-dark');
  await page.locator('[data-message-files]').first().scrollIntoViewIfNeeded();
  await shot(page, 'panel-message-files-desktop-dark');

  // The panel with the cards waiting, in light and on a phone.
  for (const [theme, mobile, tag] of [
    ['light', false, 'desktop-light'],
    ['dark', true, 'phone-dark'],
    ['light', true, 'phone-light'],
  ]) {
    const v = await newPage(browser, { theme, mobile, state });
    await v.page.goto(`${base}/p/${projectId}?ask=${convId}`);
    await v.page.locator('article[data-keep]').first().waitFor({ timeout: 30_000 });
    await v.page.waitForTimeout(600);
    await shot(v.page, `panel-keep-cards-${tag}`);
    await keepCard(v.page, 'picture', 'Keep this logo in the sources and the brand kit?').scrollIntoViewIfNeeded();
    await shot(v.page, `panel-logo-card-${tag}`);
    await v.page.locator('[data-message-files]').first().scrollIntoViewIfNeeded();
    await shot(v.page, `panel-message-files-${tag}`);
    if (mobile) {
      const wide = await v.page.evaluate(() => {
        const s = document.querySelector('aside[role="dialog"] .overflow-y-auto');
        return s ? s.scrollWidth - s.clientWidth : -1;
      });
      check(`the panel fits a phone (${theme})`, wide === 0, `${wide}px wider than the panel`);
    }
    await v.ctx.close();
  }

  /* ---------- 3. Keep and Not now ---------- */
  // A rule asks where it goes (D193): just this project, or every project using its settings.
  const whereButtons = keepCard(page, 'rule').locator('[data-keep-where]');
  const whereLabels = (await whereButtons.allInnerTexts()).map((t) => t.trim());
  check(
    'the rule card asks where to keep it',
    whereLabels.length === 2 && whereLabels[0] === 'Just this project' && /^Every project using/.test(whereLabels[1]),
    whereLabels.join(' | '),
  );
  // A sign-off asks where too (D200); kept for every project using the settings it is a house sign-off.
  const soCard = keepCard(page, 'signoff');
  const soWhere = (await soCard.locator('[data-keep-where]').allInnerTexts()).map((t) => t.trim());
  check(
    'the sign-off card asks where to keep it',
    soWhere.length === 2 && soWhere[0] === 'Just this project' && /^Every project using/.test(soWhere[1]),
    soWhere.join(' | '),
  );
  await soCard.scrollIntoViewIfNeeded();
  await soCard.locator('[data-keep-where="settings"]').click();
  await until(async () => ['done', 'failed'].includes((await soCard.getAttribute('data-keep-state')) ?? ''), 60_000);
  const soDone = (await soCard.innerText()).replace(/\s+/g, ' ');
  check('Keep for every project: a house sign-off', (await soCard.getAttribute('data-keep-state')) === 'done' && /house sign-off/.test(soDone), soDone.slice(0, 220));
  for (const [kind, title, yes] of [
    ['rule', '', true],
    ['brief', '', true],
    ['picture', 'Keep this logo in the sources and the brand kit?', true],
    ['source', '', false],
    ['check', '', false],
    ['picture', "Keep this PDF in the project's sources?", false],
  ]) {
    const c = keepCard(page, kind, title);
    const out = await decide(page, c, yes);
    const want = yes ? 'done' : 'dismissed';
    check(`${yes ? 'Keep' : 'Not now'}: ${title || kind}`, out.state === want && (yes ? !/Did not work/.test(out.text) : /Not kept; MetaRush won't ask again/.test(out.text)), out.text.slice(0, 220));
  }
  check('nothing waits any more (the navigator is gone)', (await page.locator('[data-navigator]').count()) === 0);
  await shot(page, 'panel-keep-cards-decided-desktop-dark');
  const after = unplain(await readText(page, 'aside[role="dialog"]'), checkPanel);
  check('the decided cards read plainly', after.length === 0, after.join(' | '));

  const rules = (await api(page, 'project.rules.list', { projectId })).rules;
  check('the rule is kept, measured by the quality check', rules.length === 1 && rules[0].text === 'First names only on screen' && rules[0].measure === 'first-names', JSON.stringify(rules.map((r) => [r.text, r.measure])));
  const so = await api(page, 'cut.signoffs.list', { projectId });
  check('the sign-off is on the cut, still waiting', so.signoffs.length === 1 && so.open === 1, so.summary);
  const house = await api(page, 'content.signoffs.list', { projectId });
  check(
    '… as a house sign-off of the project’s settings (not one of its own)',
    so.signoffs[0]?.house === true && house.signoffs.length === 1 && house.signoffs[0].text === 'Lena OKs the final version',
    JSON.stringify({ house: so.signoffs[0]?.house, settings: house.signoffs.map((s) => s.text) }),
  );
  const show = await api(page, 'project.show', { id: projectId });
  const places = (show.project.brief.deliverables ?? []).map((d) => `${d.name}:${d.preset}`);
  check('the brief says where the video goes, each with its shape', places.includes('on the website:landscape-1080p') && places.includes('social shorts:vertical-1080p'), places.join(', '));
  const res = (await api(page, 'resources.list', { projectId })).resources ?? [];
  check('the logo is in the sources', res.some((r) => /garden-logo/.test(r.name) || /garden-logo/.test(r.title ?? '')), res.map((r) => r.name).join(', '));
  const items = (await api(page, 'keep.list', { projectId })).items;
  const by = (s) => items.filter((i) => i.state === s).map((i) => i.kind).sort().join(',');
  check('what was offered is remembered: 4 kept, 3 set aside', by('kept') === 'brief,picture,rule,signoff' && by('declined') === 'check,picture,source', `kept ${by('kept')}; set aside ${by('declined')}`);

  /* ---------- 4. The studio: the flash at the clip's start ---------- */
  // Close the panel first (it reopens after a reload while it was open).
  await page.locator('aside[role="dialog"]').getByRole('button', { name: 'Close (Esc)' }).first().click();
  await page.locator('aside[role="dialog"]').waitFor({ state: 'detached', timeout: 10_000 }).catch(() => {});
  await page.goto(`${base}/p/${projectId}`);
  const fix = page.locator('[data-call-fix="flashes"]');
  await fix.waitFor({ timeout: 30_000 }).catch(() => {});
  const fixText = (await fix.innerText().catch(() => '')).replace(/\s+/g, ' ');
  check('the studio offers to trim the split-second flash', /opens with a split-second of another picture/.test(fixText) && /Trim the flashes/.test(fixText) && unplain(fixText).length === 0, fixText);
  await fix.scrollIntoViewIfNeeded().catch(() => {});
  await shot(page, 'studio-trim-flashes-desktop-dark');
  const headBefore = (await api(page, 'project.show', { id: projectId })).headRevisionId;
  await fix.locator('[data-trim-flashes]').click().catch(() => {});
  await page.locator('[data-toasts]', { hasText: 'Trimmed the flashes' }).waitFor({ timeout: 30_000 }).catch(() => {});
  const headAfter = await until(async () => {
    const h = (await api(page, 'project.show', { id: projectId })).headRevisionId;
    return h !== headBefore ? h : '';
  }, 30_000);
  const again = await api(page, 'cut.flashes', { projectId, dryRun: true });
  check('trimming saves a version without the flash', !!headAfter && again.noChange === true, again.summary);
  await until(async () => (await page.locator('[data-call-fix="flashes"]').count()) === 0, 20_000);
  check('the offer goes once the flash is gone', (await page.locator('[data-call-fix="flashes"]').count()) === 0);

  /* ---------- 5. Approving reminds of the sign-off (once, never a lock) ---------- */
  const head = (await api(page, 'project.show', { id: projectId })).headRevisionId;
  await page.goto(`${base}/p/${projectId}/r/${head}`);
  const approveBtn = page.getByRole('button', { name: 'Approve', exact: true });
  await approveBtn.waitFor({ timeout: 30_000 });
  const line = (await page.locator('[data-signoffs-line]').innerText().catch(() => '')).replace(/\s+/g, ' ');
  check('the latest version shows the sign-off still waiting', /Still waiting for a sign-off: “Lena OKs the final version”/.test(line) && unplain(line).length === 0, line);
  await approveBtn.click();
  const dlg = page.locator('[data-mr-dialog="open"]');
  await dlg.waitFor({ timeout: 10_000 });
  const dlgText = (await dlg.innerText()).replace(/\s+/g, ' ');
  check('approving says which sign-off is still waiting', /Still waiting for a sign-off: “Lena OKs the final version”\. Approve anyway\?/.test(dlgText) && /Approve anyway/.test(dlgText) && /Not yet/.test(dlgText), dlgText.slice(0, 260));
  await shot(page, 'approve-reminder-desktop-dark');
  await dlg.getByRole('button', { name: 'Not yet' }).click();
  await dlg.waitFor({ state: 'detached', timeout: 10_000 });
  const stillDraft = (await api(page, 'revision.show', { id: head })).summary.approval.state;
  check('"Not yet" approves nothing', stillDraft === 'draft', stillDraft);
  await approveBtn.click();
  await dlg.waitFor({ timeout: 10_000 });
  await dlg.locator('button[type="submit"]').click();
  const toast = page.locator('[data-toasts]', { hasText: 'approved' });
  await toast.waitFor({ timeout: 20_000 });
  const toastText = (await toast.innerText()).replace(/\s+/g, ' ');
  check('"Approve anyway" approves, and the toast repeats the reminder', /approved/.test(toastText) && /Still waiting for a sign-off/.test(toastText), toastText);
  const approved = (await api(page, 'revision.show', { id: head })).summary.approval.state;
  check('the version is approved', approved === 'approved', approved);

  /* ---------- 6. What the project keeps ---------- */
  // An older link (?tab=memory) opens the Settings tab (D193).
  await page.goto(`${base}/p/${projectId}?tab=memory`);
  const mem = page.locator('[data-project-memory]');
  await mem.waitFor({ timeout: 20_000 });
  await page.waitForTimeout(500);
  const memText = await readText(page, '[data-project-memory]');
  check(
    'the tab lists the rules, the sign-offs, where it goes and what was offered',
    /First names only on screen/.test(memText) &&
      /the quality check measures it: names on screen are first names only/.test(memText) &&
      /Lena OKs the final version/.test(memText) &&
      /Still waiting/.test(memText) &&
      (await mem.locator('[data-deliverable="landscape-1080p"]').count()) === 1 &&
      (await mem.locator('[data-deliverable="vertical-1080p"]').count()) === 1 &&
      (await mem.locator('[data-offered="kept"] li').count()) === 4 &&
      (await mem.locator('[data-offered="declined"] li').count()) === 3,
    memText.replace(/\s+/g, ' ').slice(0, 300),
  );
  await shot(page, 'project-keeps-desktop-dark');
  const tabLabel = await page.getByRole('tab', { name: /^Settings/ }).innerText();
  const selected = await page.getByRole('tab', { name: /^Settings/ }).getAttribute('aria-selected');
  check('the project page has the Settings tab, and ?tab=memory opens it', /Settings/.test(tabLabel) && selected === 'true', `${tabLabel} ${selected}`);
  check('the Settings tab holds Content Settings above This project', (await page.locator('[data-project-settings] [data-content-settings]').count()) === 1, '');

  await mem.locator('[data-rule-add] input[aria-label]').first().fill('Captions at the top');
  await mem.locator('[data-rule-add]').getByRole('button', { name: 'Add' }).click();
  await until(async () => (await mem.locator('[data-rule]').count()) === 2, 15_000);
  const rulesText = await mem.locator('[data-memory="rules"]').innerText();
  check('a rule added here says what the quality check measures', /Captions at the top/.test(rulesText) && /measures it: captions at the top/.test(rulesText), rulesText.replace(/\s+/g, ' '));

  await mem.locator('[data-deliverable-add] input').fill('Instagram reels');
  await mem.locator('[data-deliverable-add]').getByRole('button', { name: 'Add' }).click();
  await until(async () => (await mem.locator('[data-deliverable]').count()) === 3, 15_000);
  const placeRow = (await mem.locator('[data-deliverable]', { hasText: 'Instagram reels' }).innerText().catch(() => '')).replace(/\s+/g, ' ');
  check('a place added here gets its shape', /Instagram reels/.test(placeRow) && /Vertical/.test(placeRow), placeRow);

  await mem.locator('[data-offered="declined"] [data-offered-item="check"] [data-keep-after-all]').click();
  const keptToast = page.locator('[data-toasts] [role="status"], [data-toasts] [role="alert"]').last();
  await keptToast.waitFor({ timeout: 15_000 }).catch(() => {});
  const keptToastText = (await keptToast.innerText().catch(() => '')).replace(/\s+/g, ' ');
  await until(async () => (await mem.locator('[data-rule="check"]').count()) === 1, 15_000);
  const checkRow = (await mem.locator('[data-rule="check"]').innerText().catch(() => '')).replace(/\s+/g, ' ');
  check('"Keep after all" keeps a check that was set aside', /No split-second flashes/.test(checkRow) && /Checked in every quality check: no split-second flashes at clip edges/.test(checkRow), checkRow);
  check('… and says so', /^Kept/.test(keptToastText), keptToastText);
  await until(async () => (await mem.locator('[data-offered="kept"] [data-offered-item="check"]').count()) === 1, 15_000);
  const offered = (await api(page, 'keep.list', { projectId })).items;
  check('… and the offer now reads as kept', offered.find((i) => i.kind === 'check')?.state === 'kept' && (await mem.locator('[data-offered="kept"] [data-offered-item="check"]').count()) === 1, offered.map((i) => `${i.kind}:${i.state}`).join(', '));

  await mem.locator('[data-signoff="open"] input[type="checkbox"]').check();
  check('a tick shows at once', (await mem.locator('[data-signoff="done"]').count()) === 1);
  // Then the server's answer says who gave the OK.
  const soRow = await until(async () => {
    const t = (await mem.locator('[data-signoff="done"]').innerText().catch(() => '')).replace(/\s+/g, ' ');
    return /OK'd by/.test(t) ? t : '';
  }, 15_000);
  check('ticking a sign-off says who OK’d it', /OK'd by/.test(soRow ?? ''), soRow);
  check('everyone has OK’d it now', (await api(page, 'cut.signoffs.list', { projectId })).open === 0);
  const memPlain = unplain(await readText(page, '[data-project-memory]'));
  check('the tab reads plainly (people’s words aside)', memPlain.length === 0, memPlain.join(' | '));
  await shot(page, 'project-keeps-changed-desktop-dark');
  await ctx.close();

  for (const [theme, mobile, tag] of [
    ['light', false, 'desktop-light'],
    ['dark', true, 'phone-dark'],
    ['light', true, 'phone-light'],
  ]) {
    const v = await newPage(browser, { theme, mobile, state });
    await v.page.goto(`${base}/p/${projectId}?tab=settings`);
    await v.page.locator('[data-project-memory]').waitFor({ timeout: 20_000 });
    await v.page.waitForTimeout(600);
    await shot(v.page, `project-keeps-${tag}`);
    if (mobile) {
      const box = await v.page.locator('[data-project-memory]').boundingBox();
      check(`16px gutters on a phone (${theme})`, !!box && box.x >= 15.5 && box.x + box.width <= 390 - 15.5, box ? `left ${box.x}, right ${390 - box.x - box.width}` : 'not found');
      await v.page.locator('[data-memory="offered"]').scrollIntoViewIfNeeded();
      await shot(v.page, `project-keeps-offered-${tag}`);
    }
    await v.ctx.close();
  }
  await browser.close();

  check('no native dialogs, page errors or horizontal overflow', problems.length === 0, problems.join(' | '));
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed; ${shots.length} screenshots in ${shotsDir}`);
  if (failed.length) process.exitCode = 1;
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
