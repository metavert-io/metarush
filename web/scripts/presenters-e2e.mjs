#!/usr/bin/env node
// End-to-end check of presenters (D273–D277) in a real browser, against a daemon whose D-ID endpoint is the fake
// API (internal/testutil/fakedid): no account, no real key, nothing made by a real model. Run it through
// scripts/presenters-e2e.sh, which sets up the installation, the footage, the fake and the seeded presenter.
//
//   Settings → Integrations: a Presenters section with D-ID connected
//   Generate → Presenter: a test clip of the draft presenter, priced first and made; the presenter approved; a
//     clip read by it with green behind it, priced first and made, landing in Sources marked as an AI presenter;
//     a clip read by one of HeyGen's stock presenters, priced with its licence
//   a new presenter begun: D-ID's consent words to read on camera, with the person's name put in
//   the project's Content Settings: No presenters; the panel then refuses a presenter clip and says why
//   the presenter retired in a styled confirm: deleted at D-ID
//   no key in any page or the browser's storage; no native dialog
//
// Screenshots go to PR_SHOTS (outside the repository) for review.
import { execFileSync } from 'node:child_process';
import { mkdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5510').replace(/\/$/, '');
const missing = ['METARUSH_MINT', 'PR_PROJECT', 'PR_LINE', 'PR_KEY', 'PR_SHOTS'].filter((k) => !process.env[k]);
if (missing.length) {
  console.error(`set ${missing.join(', ')} (scripts/presenters-e2e.sh does)`);
  process.exit(2);
}
const KEYS = [process.env.PR_KEY, process.env.PR_HEYGEN_KEY].filter(Boolean).map((f) => readFileSync(f, 'utf8').trim());
const P = process.env.PR_PROJECT;
const LINE = process.env.PR_LINE;
const SHOTS = process.env.PR_SHOTS;
mkdirSync(SHOTS, { recursive: true });

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
}

function mintLink() {
  const [bin, ...pre] = process.env.METARUSH_MINT.split(' ');
  const out = execFileSync(bin, pre, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return `${base}/auth${new URL(m[0]).search}`;
}

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

/** Waits until the project's newest presenter clip has ended (settled, and deleted at D-ID). */
async function finished(page, after = 0) {
  return waitFor(async () => {
    const l = await api(page, 'ai.list', { projectId: P, kind: 'presenter', limit: 5 });
    const w = l.work[0];
    return l.work.length > after && w && ['done', 'failed', 'refused', 'canceled'].includes(w.state) && w.settled && (!w.ticket || w.forgotten) ? w : null;
  }, 'the presenter clip to be made');
}

async function shot(page, name) {
  await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: false });
}

async function presenterKind(panel) {
  await panel.locator('[data-generate-kinds] [data-value="presenter"]').click();
  await panel.locator('[data-ai-work="presenter"]').waitFor();
}

async function main() {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await ctx.newPage();
  const problems = [];
  page.on('dialog', (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  const bodies = [];
  page.on('response', async (r) => {
    if (r.url().includes('/api/')) {
      try {
        bodies.push(await r.text());
      } catch {
        /* streamed */
      }
    }
  });

  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });

  // ---------- Settings → Integrations ----------
  await page.goto(`${base}/settings/integrations`);
  const section = page.locator('[data-integrations-section="presenter"]');
  await section.waitFor({ timeout: 20_000 });
  const secText = (await section.textContent()) ?? '';
  check('Settings → Integrations has Presenters, with D-ID', /Presenters/.test(secText) && /D-ID/.test(secText), secText.slice(0, 120));
  await shot(page, '01-integrations');

  // ---------- a test clip of the draft presenter ----------
  await page.goto(`${base}/p/${P}?tab=sources&generate=1`);
  const panel = page.locator('[data-generate-panel]');
  await panel.waitFor();
  await presenterKind(panel);
  const who = panel.locator('[data-presenter-who]');
  await who.locator('option[value="ada"]').waitFor({ state: 'attached', timeout: 20_000 });
  await who.selectOption('ada');
  await panel.locator('[data-presenter-clip]').selectOption(LINE);
  await panel.locator('[data-ai-estimate]').waitFor({ timeout: 20_000 });
  await panel.locator('[data-presenter-go]:not([disabled])').waitFor({ timeout: 20_000 });
  const label = (await panel.locator('[data-presenter-go]').textContent()) ?? '';
  check('the test clip is priced first, the price on the button', /Make a test clip \(about \$0\.\d\d\)/.test(label), label);
  await shot(page, '02-test-priced');
  await panel.locator('[data-presenter-go]').click();
  const test = await finished(page);
  check('the test clip is made at D-ID and deleted there after the download', test.state === 'done' && !!test.forgotten?.ok, `${test.state} ${test.outputs[0]?.title}`);

  // ---------- approve it ----------
  const ada = panel.locator('[data-presenter-name="ada"]');
  await ada.locator('[data-presenter-approve]').click();
  await page.locator('[data-presenter-name="ada"][data-presenter="approved"]').waitFor({ timeout: 20_000 });
  check('the presenter is approved after its test clip', true);

  // ---------- a clip, green behind ----------
  await who.selectOption('ada');
  await panel.locator('[data-presenter-clip]').selectOption(LINE);
  await panel.getByLabel('Behind the presenter').selectOption('green');
  await panel.locator('[data-presenter-go]:not([disabled])').waitFor({ timeout: 20_000 });
  await shot(page, '03-clip-priced');
  await panel.locator('[data-presenter-go]').click();
  const clip = await finished(page, 1);
  check('a clip read by the presenter, green behind it', clip.state === 'done' && clip.options?.background === '#00b140', `${clip.state} ${clip.options?.background}`);
  const assetId = clip.outputs[0]?.id;
  const asset = await api(page, 'asset.show', { id: assetId }).catch(() => null);
  const src = asset?.asset?.source;
  check('it lands in Sources marked as an AI presenter', src?.generated?.kind === 'presenter' && src?.platform === 'generated', JSON.stringify(src?.generated ?? {}).slice(0, 160));
  await panel.locator('[data-ai-work-list] [data-ai-work-item="done"]').first().waitFor({ timeout: 20_000 });
  await shot(page, '04-clip-made');

  // ---------- a stock presenter (HeyGen's, licensed for client work) ----------
  await who.locator('option[value="stock:lk_amber_studio"]').waitFor({ state: 'attached', timeout: 20_000 });
  await who.selectOption('stock:lk_amber_studio');
  await panel.locator('[data-presenter-clip]').selectOption(LINE);
  await panel.locator('[data-presenter-go]:not([disabled])').waitFor({ timeout: 20_000 });
  const stockText = (await panel.locator('[data-ai-estimate]').textContent()) ?? '';
  check('a stock presenter is priced with its licence', /HeyGen/.test(stockText) && /client work/.test(stockText), stockText.slice(0, 160));
  await shot(page, '04b-stock-priced');
  await panel.locator('[data-presenter-go]').click();
  const stockClip = await finished(page, 2);
  check('a clip read by a stock presenter at HeyGen', stockClip.state === 'done' && stockClip.provider === 'heygen' && !!stockClip.options?.stock, `${stockClip.state} ${stockClip.provider}`);

  // ---------- a new presenter begun ----------
  await panel.locator('[data-presenter-person]').selectOption({ label: 'Ada Lovelace' });
  await panel.locator('[data-presenter-start]').click();
  const words = panel.locator('[data-presenter="waiting"] [data-presenter-words]').first();
  await words.waitFor({ timeout: 20_000 });
  const w = (await words.textContent()) ?? '';
  check('D-ID’s consent words are shown, with the person’s name put in', /I, Ada Lovelace confirm/.test(w) && !/\[user name\]/.test(w), w.slice(0, 140));
  await shot(page, '05-begun');

  // ---------- Content Settings: No presenters ----------
  await page.goto(`${base}/p/${P}?tab=settings&part=aiServices`);
  const part = page.locator('[data-ai-services-part]');
  await part.waitFor({ timeout: 20_000 });
  await part.locator('[data-ai-no-presenters]').check();
  await part.getByLabel('Why (shown when something is refused)').fill("The client's contract");
  await part.locator('[data-ai-rules-save]').click();
  await page.getByText('Saved: no presenters').first().waitFor({ timeout: 30_000 });
  const rules = await api(page, 'ai.rules', { projectId: P });
  check('Content Settings rule presenters out', (rules.rules.forbidKinds ?? []).includes('presenter'), rules.summary);
  await shot(page, '06-no-presenters');
  await page.goto(`${base}/p/${P}?tab=sources&generate=1`);
  await panel.waitFor();
  await presenterKind(panel);
  await who.locator('option[value="ada"]').waitFor({ state: 'attached', timeout: 20_000 });
  await who.selectOption('ada');
  await panel.locator('[data-presenter-clip]').selectOption(LINE);
  await panel.getByText("don't allow presenters").first().waitFor({ timeout: 20_000 });
  check('the panel refuses a presenter clip, and says why', await panel.locator('[data-presenter-go]').isDisabled());
  await shot(page, '07-refused');

  // ---------- retire it, in a styled confirm ----------
  await panel.locator('[data-presenter-name="ada"] [data-presenter-retire]').click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Retire it' }).click(); // a danger confirm is an alertdialog
  const retired = await waitFor(async () => {
    const v = await api(page, 'presenter.show', { id: 'ada' });
    return v.state === 'retired' && v.removed ? v : null;
  }, 'the presenter to be retired and deleted at D-ID');
  check('retired, and deleted at D-ID', !!retired.removed?.ok, retired.summary);
  await shot(page, '08-retired');

  // ---------- no key anywhere ----------
  const storage = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }) + document.cookie);
  const html = await page.content();
  const leaked = KEYS.some((k) => storage.includes(k) || html.includes(k) || bodies.some((b) => b.includes(k)));
  check('no key in a page, a response or the browser storage', !leaked);
  check('no native dialogs', problems.length === 0, problems.join('; '));

  await browser.close();
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed; screenshots in ${SHOTS}`);
  process.exit(failed.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
