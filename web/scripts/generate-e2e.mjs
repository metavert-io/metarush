#!/usr/bin/env node
// End-to-end check of generative video (D123–D127) in a real browser, against a daemon whose Gemini endpoint is
// the fake API (internal/testutil/fakevideogen): no Google account, no real key, nothing made by a real model.
// Run it through scripts/generate-e2e.sh, which sets up the installation, the footage and the fake.
//
//   before a key: the Generate panel says to add one and makes nothing
//   Settings → Integrations: a wrong key refused, the right one checked and saved, shown masked; the key never
//     in a response, the page or browser storage; draft quality, today's limit and a project's budget
//   Generate a shot: priced first (the button carries the price), made, marked AI-generated in Sources;
//     Variations (new takes, never a replacement); how it was made, in the clip's header
//   Continue this shot from a frame of real footage, only once the person says it shows nobody
//   Consent: a prompt naming someone, or a frame showing them, is refused without their consent; consent for
//     a public figure, or someone else's without a recording, is refused; then their own consent is recorded
//   Looks: a person's look is made (never used: nothing is generated from a real face, not even by the fake);
//     a character look from pictures gets test shots, is approved and makes a shot
//   a key whose Google project is on the free tier: the shot ends at once, says why, costs nothing (D178)
//   the Script's Generated line; withdrawing consent withdraws the person's look
//   estimates (D208): the estimate opens to its parts; Claude's shot card waits under Auto while estimates come
//     first; Settings → Preferences turns them off: the Generate button doesn't wait for the price and the card
//     runs by itself (said on the card), within the budgets; back on
//   the key removed
//
// Screenshots go to GEN_SHOTS (outside the repository) for review.
import { execFileSync } from 'node:child_process';
import { mkdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5141').replace(/\/$/, '');
const missing = ['METARUSH_MINT', 'GEN_PROJECT', 'GEN_CLIP', 'GEN_KEY_FILE', 'GEN_FAKE_URL', 'GEN_SHOTS', 'GEN_MR'].filter((k) => !process.env[k]);
if (missing.length) {
  console.error(`set ${missing.join(', ')} (scripts/generate-e2e.sh does)`);
  process.exit(2);
}
const KEY = readFileSync(process.env.GEN_KEY_FILE, 'utf8').trim();
const P = process.env.GEN_PROJECT;
const CLIP = process.env.GEN_CLIP;
const FACES = process.env.GEN_FACES === '1';
const SHOTS = process.env.GEN_SHOTS;
const FAKE = process.env.GEN_FAKE_URL.replace(/\/$/, '');
mkdirSync(SHOTS, { recursive: true });

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
}

function cli(args) {
  const [bin, ...pre] = process.env.GEN_MR.split(' ');
  const out = execFileSync(bin, [...pre, ...args, '--json'], { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  return JSON.parse(out);
}

function mintLink() {
  const [bin, ...pre] = process.env.METARUSH_MINT.split(' ');
  const out = execFileSync(bin, pre, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return `${base}/auth${new URL(m[0]).search}`;
}

/** Calls an op the way the dashboard does; returns {ok, data} or {ok:false, code, message}. */
async function op(page, name, input = {}) {
  return page.evaluate(
    async ([name, input]) => {
      const r = await fetch(`/api/ops/${name}`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' },
        body: JSON.stringify(input),
      });
      const j = await r.json();
      return j.ok ? { ok: true, data: j.data } : { ok: false, code: j.error?.code, message: j.error?.message };
    },
    [name, input],
  );
}
async function api(page, name, input = {}) {
  const r = await op(page, name, input);
  if (!r.ok) throw new Error(`${name}: ${r.code} ${r.message}`);
  return r.data;
}

async function waitFor(fn, what, timeout = 90_000) {
  const end = Date.now() + timeout;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 750));
  }
}

// Ended, with its money settled and (when Google had it) deleted at Google: the job's last steps.
const ended = (g) => ['done', 'failed', 'refused', 'canceled'].includes(g.state) && !!g.settled && (!g.ticket || !!g.forgotten);

/** Waits until every generation of the project (or the given IDs) has ended; returns them. */
async function settled(page, ids, what) {
  return waitFor(async () => {
    const l = await api(page, 'generate.list', { projectId: P, limit: 50 });
    const gs = l.generations.filter((g) => ids.includes(g.id));
    return gs.length === ids.length && gs.every(ended) ? gs : null;
  }, what);
}

async function toast(page, text) {
  await page.getByText(text).first().waitFor({ timeout: 20_000 });
}

async function shot(page, name, locator, around = 0) {
  const file = path.join(SHOTS, `${name}.png`);
  if (locator && around) {
    // The part of the page around it (rows have no box of their own).
    await locator.scrollIntoViewIfNeeded();
    const b = await locator.boundingBox();
    const vp = page.viewportSize();
    const y = Math.max(0, b.y - around);
    await page.screenshot({ path: file, clip: { x: 0, y, width: vp.width, height: Math.min(vp.height - y, b.height + 2 * around) } });
  } else if (locator) await locator.screenshot({ path: file });
  else await page.screenshot({ path: file, fullPage: true });
  console.log(`     screenshot ${file}`);
}

async function enabled(locator, what, timeout = 20_000) {
  await waitFor(async () => (await locator.isVisible()) && (await locator.isEnabled()), what, timeout);
}

async function fakeCalls() {
  const r = await fetch(`${FAKE}/__calls`);
  return r.json();
}

async function main() {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await ctx.newPage();
  const leaks = [];
  const sentKey = new Set();
  const problems = [];
  page.on('request', (r) => {
    const body = r.postData();
    if (body && body.includes(KEY)) sentKey.add(new URL(r.url()).pathname);
  });
  page.on('response', async (r) => {
    try {
      const body = await r.body();
      if (body.includes(Buffer.from(KEY))) leaks.push(r.url());
    } catch {
      // redirects and aborted media have no body
    }
  });
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) problems.push(`console: ${m.text()}`);
  });
  page.on('dialog', (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });

  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });

  // ---------- before a key: nothing can be made ----------
  await page.goto(`${base}/p/${P}?tab=sources&generate=1`);
  let panel = page.locator('[data-generate-panel]');
  await panel.waitFor();
  await panel.getByText('Add your Google Gemini key on this computer first').waitFor({ timeout: 15_000 });
  await panel.locator('[data-generate-prompt]').fill('a red paper boat drifting on a pond');
  await panel.locator('[data-estimate-problem]').first().waitFor({ timeout: 15_000 });
  check('without a key the panel says to add one and makes nothing', await panel.locator('[data-generate-go]').isDisabled(), await panel.locator('[data-estimate-problem]').first().textContent());

  // ---------- Settings → Integrations: the key ----------
  await page.goto(`${base}/settings/integrations`);
  await page.getByRole('heading', { name: 'Integrations', level: 1 }).waitFor();
  const section = page.locator('[data-integrations-section="video"]');
  const card = page.locator('section[data-provider="gemini"]');
  await card.waitFor();
  check('generative video has its own section', await section.getByRole('heading', { name: 'Generative video' }).isVisible());
  if (await card.getByRole('button', { name: 'Remove' }).isVisible()) {
    await card.getByRole('button', { name: 'Remove' }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Remove key' }).click();
    await card.getByText('Not set up').waitFor({ timeout: 15_000 });
  }
  check('starts without a key', await card.getByText('Not set up').isVisible());
  await card.getByRole('button', { name: 'Add key' }).click();
  const dialog = page.getByRole('dialog');
  const field = dialog.getByLabel('API key');
  check('the key field hides what is typed', (await field.getAttribute('type')) === 'password');
  await field.fill('gk-wrong-' + 'x'.repeat(32));
  await dialog.getByRole('button', { name: 'Check key' }).click();
  await dialog.getByText('did not accept this key').waitFor({ timeout: 15_000 });
  check('a wrong key is refused before saving', await dialog.getByRole('button', { name: 'Save key' }).isHidden());
  await field.fill(KEY);
  await dialog.getByRole('button', { name: 'Check key' }).click();
  await dialog.getByText('The key works').waitFor({ timeout: 15_000 });
  // Listing the models can't show a free-tier project, so the check promises no video (D178).
  check(
    'the check names the model and what Google needs, and promises no video',
    (await dialog.getByText(/Google makes video with Gemini Omni 1\.1 Flash only for projects with billing turned on/).isVisible()) &&
      !(await dialog.getByText(/can make videos?/).isVisible()),
  );
  await shot(page, 'settings-key-checked', dialog);
  await dialog.getByRole('button', { name: 'Save key' }).click();
  await toast(page, 'Google Gemini is connected');
  await card.getByText('Connected', { exact: true }).waitFor();
  const masked = (await card.getByText(/^••••/).first().textContent())?.trim();
  check('shows only the last four characters', masked === `••••${KEY.slice(-4)}`, masked);

  // ---------- what new shots use, and what they may cost ----------
  const gs = page.locator('[data-generative-settings]');
  await gs.waitFor({ timeout: 15_000 });
  check('lists the model with its prices', (await gs.getByText('Gemini Omni 1.1 Flash').first().isVisible()) && (await gs.getByText(/360p \$0\.03 a second/).isVisible()));
  await gs.getByRole('radiogroup', { name: 'Draft quality' }).getByRole('radio', { name: /^360p/ }).click();
  await toast(page, 'Drafts are 360p now');
  await gs.getByLabel(/Today's limit for the whole team/).fill('12');
  await gs.getByRole('button', { name: 'Save limit' }).click();
  await toast(page, 'The team may spend up to $12.00 a day on generated video');
  await gs.getByLabel("A project's budget").selectOption(P);
  await gs.getByLabel('Budget in US dollars (0 for no limit)').fill('5');
  await gs.getByRole('button', { name: 'Save budget' }).click();
  await toast(page, 'This project may spend up to $5.00 on generated video');
  let st = await api(page, 'generate.settings', { projectId: P });
  check(
    'draft quality, today’s limit and the project budget saved',
    st.draftResolution === '360p' && st.dailyCapUsd === 12 && st.projectBudgetUsd === 5,
    JSON.stringify({ draft: st.draftResolution, cap: st.dailyCapUsd, budget: st.projectBudgetUsd }),
  );
  await page.mouse.move(0, 0);
  await page.waitForTimeout(4500); // let the toasts go
  await shot(page, 'settings');

  // ---------- Generate a shot from words ----------
  await page.goto(`${base}/p/${P}?tab=sources`);
  await page.locator('[data-open-generate]').click();
  panel = page.locator('[data-generate-panel]');
  await panel.waitFor();
  await panel.locator('[data-generate-prompt]').fill('a red paper boat drifting on a still pond at dawn');
  await panel.locator('input[type=range]').focus();
  await page.keyboard.press('Home');
  await panel.getByText('Length: 3 seconds').waitFor();
  const go = panel.locator('[data-generate-go]');
  await waitFor(async () => /^Generate \(about \$0\.\d\d\)$/.test(((await go.textContent()) ?? '').trim()) && (await go.isEnabled()), 'the price on the button', 20_000);
  const summary = (await panel.locator('[data-estimate-summary]').textContent()) ?? '';
  check('the price comes first', /About \$0\.\d\d for a 3-second shot, draft \(360p\), wide, made with Google Gemini/.test(summary), summary);
  check('the button carries the price', /Generate \(about \$0\.1\d\)/.test((await go.textContent()) ?? ''), (await go.textContent()) ?? '');
  await shot(page, 'generate-panel', panel);
  const callsBefore = (await fakeCalls()).create ?? 0;
  await go.click();
  await toast(page, 'Asked for the shot');
  let list = await api(page, 'generate.list', { projectId: P, limit: 50 });
  const firstId = list.generations[0]?.id;
  const [first] = await settled(page, [firstId], 'the first shot');
  check('the shot was made', first.state === 'done' && first.clips.length === 1, first.summary);
  await panel.locator('li[data-generation="done"]').first().waitFor({ timeout: 20_000 });
  const clipId = first.clips[0]?.assetId;
  await page.locator('[data-ai-badge]').first().waitFor({ timeout: 20_000 });
  check('the new clip is marked AI-generated in Sources', (await page.locator('[data-ai-badge]').count()) >= 1);
  check('the AI-generated filter appears', await page.getByRole('group', { name: 'Filter by kind' }).getByRole('button', { name: 'AI-generated' }).isVisible());
  let calls = await fakeCalls();
  check('Google was asked once, and kept nothing afterwards', (calls.create ?? 0) - callsBefore === 1 && calls.interactionsKept === 0 && calls.filesKept === 0, JSON.stringify(calls));
  check('the key never travelled in an address', (calls.problems ?? []).length === 0, (calls.problems ?? []).join(' | '));

  // ---------- Variations: new takes, never a replacement ----------
  await panel.locator('li[data-generation="done"]').first().getByRole('button', { name: 'Variations' }).click();
  const vd = page.getByRole('dialog');
  await vd.getByRole('radiogroup', { name: 'How many takes' }).getByRole('radio', { name: '2' }).click();
  await vd.locator('[data-estimate-summary]').getByText(/for 2 takes of a 3-second shot/).waitFor({ timeout: 20_000 });
  const make = vd.getByRole('button', { name: /^Make it \(about \$/ });
  await enabled(make, 'the price of the variations');
  await shot(page, 'variations-dialog', vd);
  await make.click();
  await vd.waitFor({ state: 'hidden', timeout: 20_000 });
  await toast(page, 'Asked Google Gemini for 2 takes');
  list = await api(page, 'generate.list', { projectId: P, limit: 50 });
  const takes = list.generations.filter((g) => g.groupId === first.groupId && g.id !== first.id).map((g) => g.id);
  const variations = takes.length ? await settled(page, takes, 'the variations') : [];
  check(
    'Variations made two more takes of the same shot',
    variations.length === 2 && variations.every((g) => g.state === 'done' && g.parentId === first.id),
    variations.map((g) => `take ${g.take} ${g.state}`).join(', ') || 'none',
  );
  const firstAgain = (await api(page, 'generate.show', { id: first.id })).clips[0]?.assetId;
  check('the first take is still there, unchanged', firstAgain === clipId);

  // ---------- how it was made, in the clip's header ----------
  await page.goto(`${base}/p/${P}?tab=sources&asset=${clipId}`);
  const info = page.locator('[data-generated-info]');
  await info.locator('[data-generated-prompt]').waitFor({ timeout: 20_000 });
  check('the clip header says how it was made', /Made with Google Gemini · 3 seconds, 360p/.test((await info.textContent()) ?? ''), ((await info.textContent()) ?? '').slice(0, 120));
  await info.getByText('How it was made').click();
  await info.getByText('Deleted at Google').waitFor();
  check('how it was made says it was deleted at Google', /Deleted at Googleyes/.test(((await info.textContent()) ?? '').replace(/\s+/g, '')) || /Deleted at Google\s*yes/.test((await info.textContent()) ?? ''));
  await shot(page, 'clip-how-it-was-made', info);

  // ---------- Continue this shot from real footage (no people) ----------
  await page.goto(`${base}/p/${P}?tab=sources&asset=${CLIP}&t=2`);
  await page.locator('[data-frame-actions]').getByRole('button', { name: 'Continue this shot' }).click();
  panel = page.locator('[data-generate-panel]');
  await panel.locator('[data-generate-frames]').waitFor();
  await panel.locator('[data-generate-prompt]').fill('the camera keeps drifting slowly to the right');
  await panel.locator('[data-estimate-summary]').waitFor({ timeout: 20_000 });
  const cant = await panel.locator('[data-estimate-problem]').count();
  if (cant > 0) {
    check('a frame nobody has looked at needs “shows no people” first', await panel.locator('[data-generate-go]').isDisabled(), await panel.locator('[data-estimate-problem]').first().textContent());
    await panel.locator('[data-no-people]').check();
  }
  await enabled(panel.locator('[data-generate-go]'), 'the continue price');
  await shot(page, 'continue-shot', panel);
  await panel.locator('[data-generate-go]').click();
  await toast(page, 'Asked for the shot');
  list = await api(page, 'generate.list', { projectId: P, limit: 50 });
  const [cont] = await settled(page, [list.generations[0].id], 'the continued shot');
  check('a shot continued from a frame of footage was made', cont.state === 'done' && cont.params.task !== 'text-to-video', `${cont.state} ${cont.params.task}`);

  // ---------- Consent: refused first ----------
  const ada = (await api(page, 'people.list')).people.find((p) => p.name === 'Ada');
  if (!FACES || !ada) {
    check('consent and person looks (needs the faces index on a Mac)', true, 'skipped: no faces here');
  } else {
    await page.goto(`${base}/p/${P}?tab=sources&generate=1`);
    panel = page.locator('[data-generate-panel]');
    await panel.locator('[data-generate-prompt]').fill('Ada waves from a paper boat');
    await panel.locator('[data-estimate-problem]').first().waitFor({ timeout: 20_000 });
    const why = (await panel.locator('[data-estimate-problem]').first().textContent()) ?? '';
    check('a prompt naming someone is refused without their look and consent', why.includes('names Ada') && (await panel.locator('[data-generate-go]').isDisabled()), why);
    await shot(page, 'consent-refused', panel);
    let r = await op(page, 'generate.shot', { projectId: P, prompt: 'Ada waves from a paper boat', seconds: 3, resolution: '360p', maxUsd: 1 });
    check('…and the server refuses it too', !r.ok && r.code === 'E_CONSENT_REQUIRED', `${r.code}`);
    const adaFace = (await api(page, 'people.show', { id: ada.id })).faces[0];
    r = await op(page, 'generate.shot', { projectId: P, prompt: 'the camera pulls back', seconds: 3, resolution: '360p', from: { assetId: adaFace.assetId, at: adaFace.at }, noPeople: true, maxUsd: 1 });
    check('a frame showing someone is refused without their consent, even when said to show nobody', !r.ok && r.code === 'E_CONSENT_REQUIRED', `${r.code}: ${r.message}`);
    r = await op(page, 'looks.create', { person: ada.id, title: 'Ada', description: 'Ada, short dark hair', stills: [{ assetId: adaFace.assetId, at: adaFace.at }] });
    check('a person’s look is refused without their consent', !r.ok && r.code === 'E_CONSENT_REQUIRED', `${r.code}`);

    // ---------- Consent: recorded on the People page ----------
    await page.goto(`${base}/library/people?person=${ada.id}`);
    const likeness = page.locator('[data-likeness]');
    await likeness.waitFor({ timeout: 20_000 });
    check('no look can be made before consent', (await likeness.locator('[data-make-look]').count()) === 0);
    await likeness.locator('[data-record-consent]').click();
    const cd = page.getByRole('dialog');
    const form = cd.locator('[data-consent-form]');
    await form.getByRole('radio', { name: 'Someone else' }).click();
    await form.getByLabel(/Ada's consent, in their words/).fill('I, Ada, agree to my likeness being used in generated video for this project.');
    await form.locator('[data-adult]').check();
    await form.getByRole('button', { name: 'Record consent' }).click();
    await form.getByText('only uses it with their own recorded consent').waitFor({ timeout: 15_000 });
    check('someone else’s consent needs their recording', true);
    await form.getByLabel(/is a public figure/).check();
    await form.getByRole('button', { name: 'Record consent' }).click();
    await form.getByText("doesn't make the likeness of public figures").waitFor({ timeout: 15_000 });
    check('a public figure is refused, even with consent', true);
    await form.getByLabel(/is a public figure/).uncheck();
    await form.getByRole('radio', { name: 'This is me (Ada)' }).click();
    await form.getByLabel('Your consent, in your own words').fill('I, Ada, agree to MetaRush making videos with my likeness for my own projects.');
    await form.locator('[data-adult]').uncheck();
    check('consent can’t be saved until the person is said to be an adult', await form.getByRole('button', { name: 'Record consent' }).isDisabled());
    await form.locator('[data-adult]').check();
    await shot(page, 'consent-form', cd);
    await form.getByRole('button', { name: 'Record consent' }).click();
    await toast(page, 'Consent recorded');
    const consents = (await api(page, 'consent.list', { person: ada.id })).consents;
    check('consent recorded, by whom and when', consents.length === 1 && consents[0].status === 'active', consents[0]?.summary);

    // ---------- a person's look: made, never used ----------
    await likeness.locator('[data-make-look]').click();
    const ld = page.getByRole('dialog');
    const lform = ld.locator('[data-look-form]');
    await lform.getByLabel(/Who they are and how they look/).fill('Ada, in her forties, short hair, in a dark jacket, calm and friendly');
    await shot(page, 'look-person-form', ld);
    await lform.getByRole('button', { name: 'Make the look' }).click();
    await toast(page, 'Look made');
    const personLook = (await api(page, 'looks.list', { person: ada.id, all: true })).looks[0];
    check('a person’s look starts as not approved', personLook?.state === 'draft' && personLook.stills.length >= 1, `${personLook?.state}, ${personLook?.stills.length} stills`);
    await page.locator(`article[data-look="${personLook.name}"]`).waitFor({ timeout: 15_000 });
    await shot(page, 'consent-and-look', likeness);
  }

  // ---------- a character look from pictures: test shots, approve, use ----------
  await page.goto(`${base}/settings/integrations#looks`);
  await page.getByRole('heading', { name: 'Looks and consent', level: 2 }).waitFor();
  await page.getByRole('button', { name: 'New character look' }).click();
  const chd = page.getByRole('dialog');
  const chf = chd.locator('[data-look-form]');
  await chf.getByLabel('Name of the look').fill('Captain Dog');
  await chf.getByLabel(/Who they are and how they look/).fill('A cheerful cattle dog with a blue speckled coat and alert ears, the captain of a paper boat');
  await chf.getByRole('group', { name: 'Pictures' }).getByRole('button', { name: /Captain Dog, left/ }).click();
  await chf.getByRole('group', { name: 'Pictures' }).getByRole('button', { name: /Captain Dog, right/ }).click();
  check('a character look needs “these pictures show no real person”', await chf.getByRole('button', { name: 'Make the look' }).isDisabled());
  await chf.getByLabel('These pictures show no real person.').check();
  await shot(page, 'look-character-form', chd);
  await chf.getByRole('button', { name: 'Make the look' }).click();
  await toast(page, 'Look made');
  const dog = (await api(page, 'looks.list', { all: true })).looks.find((l) => l.title === 'Captain Dog');
  const dogCard = page.locator(`article[data-look="${dog.name}"]`);
  await dogCard.waitFor({ timeout: 15_000 });
  await dogCard.getByRole('button', { name: 'Make test shots' }).click();
  const td = page.getByRole('dialog');
  const makeThem = td.getByRole('button', { name: /^Make them \(about \$/ });
  await enabled(makeThem, 'the price of the test shots');
  await makeThem.click();
  await toast(page, 'Test shots asked for');
  const tests = await waitFor(async () => {
    const s = await api(page, 'looks.show', { look: dog.id });
    return (s.testShots ?? []).length >= 2 && s.testShots.every(ended) ? s.testShots : null;
  }, 'the test shots');
  check('two test shots were made for the test sheet', tests.filter((t) => t.state === 'done').length === 2, tests.map((t) => t.state).join(', '));
  await dogCard.locator('[data-test-sheet] img').first().waitFor({ timeout: 20_000 });
  await dogCard.getByRole('button', { name: 'Approve' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Approve look' }).click();
  await toast(page, 'Approved the look');
  check('the look is approved', (await api(page, 'looks.show', { look: dog.id })).state === 'approved');
  await page.waitForTimeout(4500);
  await shot(page, 'looks-page');

  await page.goto(`${base}/p/${P}?tab=sources&generate=1`);
  panel = page.locator('[data-generate-panel]');
  await panel.getByLabel('Who appears').selectOption(dog.name);
  await panel.locator('[data-generate-prompt]').fill('stands at the bow of a red paper boat drifting on a pond');
  await enabled(panel.locator('[data-generate-go]'), 'the price with the look');
  await shot(page, 'generate-with-look', panel);
  await panel.locator('[data-generate-go]').click();
  await toast(page, 'Asked for the shot');
  list = await api(page, 'generate.list', { projectId: P, limit: 50 });
  const [withLook] = await settled(page, [list.generations[0].id], 'the shot with the look');
  check('a shot with the character look was made', withLook.state === 'done' && withLook.look?.name === dog.name, `${withLook.state} ${withLook.look?.name}`);

  // ---------- a key whose Google project is on the free tier: said at once, nothing charged (D178) ----------
  // The fake answers this request's status checks as Google did on 2026-10-20 (a 429 "limit: 0 … on Free Tier").
  await page.goto(`${base}/p/${P}?tab=sources&generate=1`);
  panel = page.locator('[data-generate-panel]');
  await panel.locator('[data-generate-prompt]').fill('[fake:free-tier] a paper boat at dusk');
  await enabled(panel.locator('[data-generate-go]'), 'the price of the free-tier shot');
  await panel.locator('[data-generate-go]').click();
  await toast(page, 'Asked for the shot');
  list = await api(page, 'generate.list', { projectId: P, limit: 50 });
  const [free] = await settled(page, [list.generations[0].id], 'the free-tier shot');
  check(
    'a free-tier key: the shot ends at once and says why, nothing charged, deleted at Google',
    free.state === 'failed' && /free tier, which can't make video/.test(free.outcome?.message ?? '') && free.settled?.usd === 0 && !!free.forgotten?.ok,
    `${free.state}: ${free.outcome?.message}`,
  );
  const failedRow = panel.locator('li[data-generation="failed"]').first();
  await failedRow.waitFor({ timeout: 20_000 });
  check('the panel says what to do', /Turn on billing for the project in Google AI Studio/.test((await failedRow.textContent()) ?? ''), ((await failedRow.textContent()) ?? '').slice(0, 160));
  await shot(page, 'free-tier', failedRow, 40);

  // ---------- the Script's Generated line ----------
  const dry = cli(['cut', 'add', P, clipId, '--dry-run']);
  cli(['cut', 'add', P, clipId, '--confirm', dry.data.check.confirmToken]);
  await page.goto(`${base}/p/${P}?tab=script`);
  const line = page.locator('[data-generated-line]').first();
  await line.waitFor({ timeout: 20_000 });
  check('the Script marks the generated clip', /Generated\s*with Google Gemini/.test((await line.textContent()) ?? ''), (await line.textContent()) ?? '');
  await line.getByText('Prompt').click();
  check('the prompt is behind Details', await line.getByText('a red paper boat drifting on a still pond at dawn').isVisible());
  await shot(page, 'script-generated', line, 160);

  // ---------- withdrawing consent withdraws the person's look ----------
  if (FACES && ada) {
    await page.goto(`${base}/library/people?person=${ada.id}`);
    const likeness = page.locator('[data-likeness]');
    await likeness.getByRole('button', { name: 'Withdraw consent' }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Withdraw consent' }).click();
    await toast(page, 'Withdrew');
    const looks = (await api(page, 'looks.list', { person: ada.id, all: true })).looks;
    check('withdrawing consent withdraws the looks that rely on it', looks.length > 0 && looks.every((l) => l.state === 'revoked'), looks.map((l) => l.state).join(', '));
  }

  // ---------- money: reserved, then settled ----------
  st = await api(page, 'generate.settings', { projectId: P });
  const today = st.budget.today;
  check('nothing is still set aside once every shot ended', today.heldUsd === 0 && today.spentUsd > 0, JSON.stringify(today));
  calls = await fakeCalls();
  check('nothing was left at Google', calls.interactionsKept === 0 && calls.filesKept === 0, JSON.stringify({ kept: calls.interactionsKept, files: calls.filesKept }));

  // ---------- estimates (D208): the estimate opens; Preferences; Auto with estimates off ----------
  await page.goto(`${base}/p/${P}?tab=sources&generate=1`);
  panel = page.locator('[data-generate-panel]');
  await panel.locator('[data-generate-prompt]').fill('a paper boat drifting past the reeds');
  await enabled(panel.locator('[data-generate-go]'), 'the price');
  check('estimates come first by default', (await panel.locator('[data-generate-go]').getAttribute('data-estimates-first')) === 'on');
  await panel.locator('[data-cost-toggle]').click();
  const partsBox = panel.locator('[data-cost-parts]');
  await partsBox.waitFor();
  const partsText = (await partsBox.textContent()) ?? '';
  check(
    'the estimate opens to what it draws on: the video, what is sent, the thinking allowance',
    (await partsBox.locator('[data-cost-part]').count()) === 3 && /Gemini Omni 1\.1 Flash/.test(partsText) && /\d+ seconds of video at 360p/.test(partsText) && /What is sent: the prompt/.test(partsText) && /prices of/.test(partsText),
    partsText.replace(/\s+/g, ' ').slice(0, 240),
  );
  await shot(page, 'estimate-open', panel);
  // Auto on, estimates on: Claude's shot card waits for the person, with its estimate.
  await page.goto(`${base}/settings/preferences`);
  const prefsPage = page.locator('[data-preferences]');
  await prefsPage.waitFor();
  await prefsPage.locator('[data-pref-mode="auto"]').check();
  await waitFor(async () => (await api(page, 'assistant.mode')).mode === 'auto', 'Auto on', 20_000);
  const pf = await api(page, 'account.preferences');
  check('Preferences: estimates on by default, Auto chosen there', pf.estimates && pf.estimatesYours && pf.assistantMode === 'auto', JSON.stringify(pf));
  const askOne = await api(page, 'assistant.ask', { prompt: 'Make a paper boat shot while estimates come first', projectId: P });
  const waitingCard = await waitFor(async () => {
    const cv = await api(page, 'assistant.show', { id: askOne.conversation.id, events: false });
    return cv.state === 'idle' && (cv.actions ?? []).length ? cv.actions[0] : null;
  }, 'the shot card');
  check(
    "under Auto with estimates on, Claude's priced card waits, with its estimate",
    waitingCard.op === 'generate.shot' && waitingCard.state === 'pending' && !waitingCard.autoPriced && /^Make a 3-second shot \(about \$0\.\d\d\)$/.test(waitingCard.title) && (waitingCard.estimate?.parts ?? []).length === 3,
    `${waitingCard.title} ${waitingCard.state}`,
  );
  // Estimates off, in Settings → Preferences.
  await page.goto(`${base}/settings/preferences`);
  await prefsPage.waitFor();
  await shot(page, 'preferences-on');
  await prefsPage.getByLabel('Estimate costs of outside AI services before edits').uncheck();
  await waitFor(async () => (await api(page, 'account.preferences')).estimates === false, 'estimates off', 20_000);
  await page.reload();
  await prefsPage.waitFor();
  check('the choice is kept', !(await prefsPage.getByLabel('Estimate costs of outside AI services before edits').isChecked()));
  await shot(page, 'preferences-off');
  // The Generate button doesn't wait for the price.
  await page.goto(`${base}/p/${P}?tab=sources&generate=1`);
  panel = page.locator('[data-generate-panel]');
  await panel.locator('[data-generate-prompt]').fill('a paper boat in the rain');
  const goNow = panel.locator('[data-generate-go]');
  check('estimates off: Generate can be pressed before the price is in', (await goNow.getAttribute('data-estimates-first')) === 'off' && (await goNow.isEnabled()));
  let before = (await api(page, 'generate.list', { projectId: P, limit: 50 })).generations.length;
  await goNow.click();
  await toast(page, 'Asked for the shot');
  list = await api(page, 'generate.list', { projectId: P, limit: 50 });
  const [quick] = await settled(page, [list.generations[0].id], 'the shot without the price step');
  check('the shot was made without the estimate step, within the budget', list.generations.length === before + 1 && quick.state === 'done', `${quick.state}`);
  // Claude's shot card runs by itself under Auto, and says why.
  before = list.generations.length;
  const askTwo = await api(page, 'assistant.ask', { prompt: 'Make another paper boat shot with estimates off', projectId: P });
  const ranCard = await waitFor(async () => {
    const cv = await api(page, 'assistant.show', { id: askTwo.conversation.id, events: false });
    const a = (cv.actions ?? [])[0]; // no cards yet: the list is left out
    return a && a.state !== 'pending' && a.state !== 'running' ? a : null;
  }, 'the card that runs by itself');
  check("under Auto with estimates off, Claude's priced card ran by itself", ranCard.state === 'done' && ranCard.autoPriced && ranCard.autoMode, `${ranCard.state} ${ranCard.result ?? ''}`);
  list = await api(page, 'generate.list', { projectId: P, limit: 50 });
  check('…and asked for the shot', list.generations.length === before + 1, `${list.generations.length - before} new`);
  await settled(page, [list.generations[0].id], "the card's shot");
  await page.goto(`${base}/p/${P}?ask=${askTwo.conversation.id}`);
  const autoLine = page.locator('[data-auto-priced]').first();
  await autoLine.waitFor({ timeout: 30_000 });
  check('the card says it ran by itself because estimates are off', /Auto is on, and your estimates are off/.test((await autoLine.textContent()) ?? ''), (await autoLine.textContent()) ?? '');
  await shot(page, 'card-auto-priced', page.locator('[data-auto-priced]').first().locator('xpath=ancestor::article[1]'));
  // Close the panel (it stays open across pages, over them on a narrow window).
  await page.getByRole('button', { name: 'Close (Esc)' }).click();
  await page.locator('[data-auto-priced]').first().waitFor({ state: 'detached', timeout: 10_000 }).catch(() => undefined);
  // Back on: the step comes back.
  await page.goto(`${base}/settings/preferences`);
  await prefsPage.waitFor();
  await prefsPage.getByLabel('Estimate costs of outside AI services before edits').check();
  await prefsPage.locator('[data-pref-mode="ask"]').check();
  await waitFor(async () => {
    const p = await api(page, 'account.preferences');
    return p.estimates && p.assistantMode === 'ask';
  }, 'estimates on and Ask first again', 20_000);
  const refused = await op(page, 'generate.shot', { projectId: P, prompt: 'a paper boat at noon', seconds: 3, resolution: '360p' });
  check('estimates on again: a shot without the agreed price is refused', !refused.ok && refused.code === 'E_CONFIRM_REQUIRED', `${refused.code} ${refused.message}`);

  // ---------- the key never comes back; removing it ----------
  await page.goto(`${base}/settings/integrations`);
  await card.waitFor();
  const html = await page.content();
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
  check('no response contained the key', leaks.length === 0, leaks.join(', '));
  check('the page never shows the key', !html.includes(KEY));
  check('browser storage never holds the key', !stored.includes(KEY));
  check('the key was sent only to check and save it', [...sentKey].every((p) => /\/api\/ops\/provider\.(test|set)$/.test(p)), [...sentKey].join(', '));
  await card.getByRole('button', { name: 'Remove' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Cancel' }).click();
  check('cancel keeps the key', await card.getByText('Connected', { exact: true }).isVisible());
  await card.getByRole('button', { name: 'Remove' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Remove key' }).click();
  await card.getByText('Not set up').waitFor({ timeout: 15_000 });
  const providers = await api(page, 'provider.list');
  check('key removed from this computer', providers.providers.find((p) => p.name === 'gemini')?.configured === false);
  check('no native dialogs or page errors', problems.length === 0, problems.join(' | '));

  await browser.close();
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
  if (failed.length) process.exitCode = 1;
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
