#!/usr/bin/env node
// End-to-end check of AI services (D224–D228) in a real browser, against a daemon whose Gemini and ElevenLabs
// endpoints are the fake APIs (internal/testutil/fakevideogen, fakeeleven): no account, no real key, nothing made by
// a real model. Run it through scripts/ai-services-e2e.sh, which sets up the installation, the footage and the fakes.
//
//   Generate → Picture: priced first with the model and why, a better model offered; made; lands in the resources
//     library, listed under the panel
//   Generate → Music: an alternative picked from the estimate (priced again, "the model you chose"); made, a clip
//     in Sources
//   Generate → Sound effect and Transcript (a clip written down in the cloud, by ElevenLabs): made
//   the project's Settings → Content Settings → AI services: ElevenLabs ruled out with a note; the panel then
//     refuses a sound effect and says why
//   no key in any page or the browser's storage; no native dialog
//
// Screenshots go to AI_SHOTS (outside the repository) for review.
import { execFileSync } from 'node:child_process';
import { mkdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5400').replace(/\/$/, '');
const missing = ['METARUSH_MINT', 'AI_PROJECT', 'AI_CLIP', 'AI_GEMINI_URL', 'AI_ELEVEN_URL', 'AI_KEYS', 'AI_SHOTS', 'AI_MR'].filter((k) => !process.env[k]);
if (missing.length) {
  console.error(`set ${missing.join(', ')} (scripts/ai-services-e2e.sh does)`);
  process.exit(2);
}
const KEYS = process.env.AI_KEYS.split(':').map((f) => readFileSync(f, 'utf8').trim());
const P = process.env.AI_PROJECT;
const SHOTS = process.env.AI_SHOTS;
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

/** Waits until the project's newest work of a kind has ended (settled, and cleaned up at the provider). */
async function finished(page, kind) {
  return waitFor(async () => {
    const l = await api(page, 'ai.list', { projectId: P, kind, limit: 5 });
    const w = l.work[0];
    return w && ['done', 'failed', 'refused', 'canceled'].includes(w.state) && w.settled && (!w.ticket || w.forgotten) ? w : null;
  }, `the ${kind} to be made`);
}

async function shot(page, name) {
  await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: false });
}

async function calls(url) {
  const r = await fetch(`${url}/__calls`);
  return r.json();
}

/** Chooses a kind in the panel's "What to make". */
async function kind(panel, value) {
  await panel.locator(`[data-generate-kinds] [data-value="${value}"]`).click();
  await panel.locator(`[data-ai-work="${value}"]`).waitFor();
}

/** Waits for a priced estimate (no pricing in progress) and returns the panel's estimate box. */
async function priced(panel) {
  const est = panel.locator('[data-ai-estimate]');
  await est.waitFor({ timeout: 20_000 });
  await panel.locator('[data-ai-go]:not([disabled])').waitFor({ timeout: 20_000 });
  return est;
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

  // ---------- a picture ----------
  await page.goto(`${base}/p/${P}?tab=sources&generate=1`);
  const panel = page.locator('[data-generate-panel]');
  await panel.waitFor();
  await kind(panel, 'picture');
  await panel.locator('[data-ai-prompt]').fill('A watercolour kite over a green hill');
  let est = await priced(panel);
  const text = (await est.textContent()) ?? '';
  check('a picture is priced with the model and why', /Google Gemini \(Nano Banana 2\.1\), MetaRush’s usual model/.test(text), text.slice(0, 160));
  check('a better model is offered', (await panel.locator('[data-ai-alternatives]').textContent())?.includes('Better') ?? false);
  check('the button carries the price', /Make it \(about \$0\.0/.test((await panel.locator('[data-ai-go]').textContent()) ?? ''));
  await shot(page, '01-picture-priced');
  await panel.locator('[data-ai-go]').click();
  const pic = await finished(page, 'picture');
  check('the picture is made and kept in the resources library', pic.state === 'done' && pic.outputs[0]?.kind === 'picture', `${pic.state} ${pic.outputs[0]?.title}`);
  await panel.locator('[data-ai-work-list] [data-ai-work-item="done"]').first().waitFor({ timeout: 20_000 });
  await shot(page, '02-picture-made');

  // ---------- music: the alternative picked ----------
  await kind(panel, 'music');
  await panel.locator('[data-ai-prompt]').fill('A calm acoustic guitar piece for a cooking video');
  est = await priced(panel);
  const alt = panel.locator('[data-ai-alternatives] button').first();
  const altText = (await alt.textContent()) ?? '';
  await alt.click();
  await waitFor(async () => /the model you chose/.test((await panel.locator('[data-ai-estimate]').textContent()) ?? ''), 'the estimate for the chosen model', 20_000);
  check('picking an alternative prices it again as your choice', true, altText);
  await shot(page, '03-music-alternative');
  await panel.locator('[data-ai-go]:not([disabled])').click();
  const music = await finished(page, 'music');
  check('the music is made and lands in Sources', music.state === 'done' && music.outputs[0]?.kind === 'clip', `${music.state} ${music.modelTitle}`);

  // ---------- a sound effect ----------
  await kind(panel, 'sound-effect');
  await panel.locator('[data-ai-prompt]').fill('A wooden door creaks open slowly');
  await priced(panel);
  await panel.locator('[data-ai-go]').click();
  const sfx = await finished(page, 'sound-effect');
  check('a sound effect is made; ElevenLabs keeps it, and that is said', sfx.state === 'done' && !!sfx.forgotten?.kept, sfx.forgotten?.detail ?? '');

  // ---------- a cloud transcript ----------
  await kind(panel, 'transcription');
  await panel.locator('[data-ai-clip]').selectOption(process.env.AI_CLIP);
  await priced(panel);
  await panel.locator('[data-ai-go]').click();
  const tr = await finished(page, 'transcription');
  check('a clip is written down in the cloud', tr.state === 'done' && tr.outputs[0]?.kind === 'transcript', tr.outputs[0]?.title ?? '');
  await shot(page, '04-work-list');

  // ---------- Content Settings rule ElevenLabs out ----------
  await page.goto(`${base}/p/${P}?tab=settings&part=aiServices`);
  const part = page.locator('[data-ai-services-part]');
  await part.waitFor({ timeout: 20_000 });
  await part.locator('[data-ai-allow="elevenlabs"]').uncheck();
  await part.getByLabel('Why (shown when something is refused)').fill("The client's contract");
  await part.locator('[data-ai-rules-save]').click();
  // The toast's body is the op's summary: only a finished save says it.
  await page.getByText('Saved: never ElevenLabs').first().waitFor({ timeout: 30_000 });
  const rules = await api(page, 'ai.rules', { projectId: P });
  check('Content Settings rule ElevenLabs out', (rules.rules.forbid ?? []).includes('elevenlabs'), rules.summary);
  await shot(page, '05-content-settings');
  await page.goto(`${base}/p/${P}?tab=sources&generate=1`);
  await panel.waitFor();
  await kind(panel, 'sound-effect');
  await panel.locator('[data-ai-prompt]').fill('Rain on a tin roof');
  const refusal = panel.getByText("don't allow ElevenLabs");
  await refusal.first().waitFor({ timeout: 20_000 });
  check('the panel refuses what Content Settings rule out, and says why', await panel.locator('[data-ai-go]').isDisabled());
  await shot(page, '06-refused');

  // ---------- the fakes saw what was asked; no key anywhere ----------
  const g = await calls(process.env.AI_GEMINI_URL);
  const x = await calls(process.env.AI_ELEVEN_URL);
  check('Google was asked for the picture and the music', (g.generate ?? 0) >= 2, JSON.stringify({ generate: g.generate }));
  check('ElevenLabs was asked for the sound effect, and nothing after it was ruled out', (x['sound-effect'] ?? 0) === 1, JSON.stringify({ music: x.music, sfx: x['sound-effect'] }));
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
