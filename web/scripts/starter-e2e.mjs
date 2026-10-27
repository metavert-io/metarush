#!/usr/bin/env node
// End-to-end check of the starter pack (D283–D287) in the dashboard, against a seeded
// throwaway daemon that reaches nothing outside this computer (scripts/starter-e2e.sh
// does all of it). The ComfyUI ability runs for real — in the sandbox, through its run's
// proxy, to the one local service an owner agreed to — against a fake ComfyUI this
// script serves on this computer:
//   A  Library → Abilities lists the starter abilities that passed their check; they wait
//      for an owner, and the two not yet runnable aren't there
//   B  ComfyUI's page says the one thing on this computer it reaches; Approve asks in a
//      styled question that agrees to it too ("Approve, with ComfyUI")
//   C  Set up on this computer
//   D  Run…: the plan says nothing leaves this computer, the words go to ComfyUI here, and
//      a person starts it; the run is done and brought the picture back, made with the ability
//   E  Open media's page: no key, only words leave, anyone may start it; Pexels' page: its
//      key goes only to api.pexels.com
//   F  phone, dark and light: the list and ComfyUI's page, no sideways scrolling
// No native browser dialog may appear; what is shown reads plainly (web/scripts/plainwords.mjs).
//
//   METARUSH_DASHBOARD=http://127.0.0.1:5530 METARUSH_MINT="bin/metarush --home <home> dashboard --print-link --no-open" \
//   COMFY_PORT=5531 STARTER_SHOTS=<dir> node web/scripts/starter-e2e.mjs
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:5530').replace(/\/$/, '');
const COMFY_PORT = Number(process.env.COMFY_PORT ?? 5531);
if (!process.env.METARUSH_MINT) {
  console.error('set METARUSH_MINT and METARUSH_DASHBOARD (see the header of this script)');
  process.exit(2);
}
const SHOTS = process.env.STARTER_SHOTS ?? '';
if (SHOTS) mkdirSync(SHOTS, { recursive: true });

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail && !ok ? ` - ${detail}` : detail ? ` (${String(detail).slice(0, 160)})` : ''}`);
}

// ---- a fake ComfyUI on this computer (its HTTP API, nothing else) ----
const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAACAAAAASCAIAAAC1qksFAAAED0lEQVR4nA3Noe52UADA4e9y/kmSJEmSJBMEk056JwgmnWSCYNJJvwmCvekkEwSTTjJBMOlcyfc+N/D8+0twEtwEL8FPCBLChCghTkgT8gSR8EkoE+oEmdAkdAlDgkogYUr4JuiEJWFLOBJMwplwJ7wJNuHfX4aT4WZ4GX5GkBFmRBlxRpqRZ4iMT0aZUWfIjCajyxgyVAYZU8Y3Q2csGVvGkWEyzow7482w2S8QOAJX4Al8QSAIBZEgFqSCXCAEH0EpqAVS0Ag6wSBQAgST4CvQgkWwCQ6BEZyCW/AKrPgFBU6BW+AV+AVBQVgQFcQFaUFeIAo+BWVBXSALmoKuYChQBRRMBd8CXbAUbAVHgSk4C+6Ct8AWv6DCqXArvAq/IqgIK6KKuCKtyCtExaeirKgrZEVT0VUMFaqCiqniW6Erloqt4qgwFWfFXfFW2OoXSByJK/EkviSQhJJIEktSSS4Rko+klNQSKWkknWSQKAmSSfKVaMki2SSHxEhOyS15JVb+ghanxW3xWvyWoCVsiVrilrQlbxEtn5aypW6RLU1L1zK0qBZappZvi25ZWraWo8W0nC13y9ti21/Q4/S4PV6P3xP0hD1RT9yT9uQ9oufTU/bUPbKn6el6hh7VQ8/U8+3RPUvP1nP0mJ6z5+55e2z/CxSOwlV4Cl8RKEJFpIgVqSJXCMVHUSpqhVQ0ik4xKJQCxaT4KrRiUWyKQ2EUp+JWvAqrfsGIM+KOeCP+SDASjkQj8Ug6ko+Ikc9IOVKPyJFmpBsZRtQII9PId0SPLCPbyDFiRs6Re+QdseMvmHFm3Blvxp8JZsKZaCaeSWfyGTHzmSln6hk508x0M8OMmmFmmvnO6JllZps5ZszMOXPPvDN2/gUaR+NqPI2vCTShJtLEmlSTa4Tmoyk1tUZqGk2nGTRKg2bSfDVas2g2zaExmlNza16N1b9gxVlxV7wVfyVYCVeilXglXclXxMpnpVypV+RKs9KtDCtqhZVp5buiV5aVbeVYMSvnyr3yrtj1F+w4O+6Ot+PvBDvhTrQT76Q7+Y7Y+eyUO/WO3Gl2up1hR+2wM+18d/TOsrPtHDtm59y5d94du/8Cg2NwDZ7BNwSG0BAZYkNqyA3C8DGUhtogDY2hMwwGZcAwGb4GbVgMm+EwGMNpuA2vwZpfcOFcuBfehX8RXIQX0UV8kV7kF+Lic1Fe1BfyornoLoYLdcHFdPG90BfLxXZxXJiL8+K+eC/s9QsenAf3wXvwH4KH8CF6iB/Sh/xBPHweyof6QT40D93D8KAeeJgevg/6YXnYHo4H83A+3A/vg31+gcWxuBbP4lsCS2iJLLElteQWYflYSkttkZbG0lkGi7JgmSxfi7Ysls1yWIzltNyW12It/wHs4/buiCh3PAAAAABJRU5ErkJggg==',
  'base64',
);
const prompts = [];
const comfy = http.createServer((req, res) => {
  const u = new URL(req.url, `http://127.0.0.1:${COMFY_PORT}`);
  const json = (v, code = 200) => {
    res.writeHead(code, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(v));
  };
  if (u.pathname === '/system_stats') return json({ system: { os: process.platform, comfyui_version: '0.3.0-fake' }, devices: [] });
  if (u.pathname === '/object_info/CheckpointLoaderSimple') return json({ CheckpointLoaderSimple: { input: { required: { ckpt_name: [['example-model.safetensors'], {}] } } } });
  if (u.pathname === '/prompt' && req.method === 'POST') {
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
      try {
        prompts.push(JSON.parse(body));
      } catch {
        prompts.push(null);
      }
      json({ prompt_id: `p-${prompts.length}`, number: prompts.length, node_errors: {} });
    });
    return undefined;
  }
  const h = u.pathname.match(/^\/history\/(p-\d+)$/);
  if (h) return json({ [h[1]]: { status: { status_str: 'success', completed: true }, outputs: { 9: { images: [{ filename: 'metarush_00001_.png', subfolder: '', type: 'output' }] } } } });
  if (u.pathname === '/view') {
    res.writeHead(200, { 'Content-Type': 'image/png' });
    return res.end(PNG);
  }
  return json({ error: 'not found' }, 404);
});
await new Promise((r) => comfy.listen(COMFY_PORT, '127.0.0.1', r));

// ---- the dashboard ----
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
  await sleep(400);
}

async function shot(page, name) {
  await settle(page);
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: false });
}

async function waitFor(fn, ms, every = 500) {
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

async function plainIn(page, selector, what) {
  const text = await readText(page, selector);
  const unplain = text.split('\n').flatMap((l) => checkShown(l).map((f) => `${f.kind} ${JSON.stringify(f.text)}`));
  check(`${what} reads plainly`, text.length > 0 && unplain.length === 0, unplain.join(' | ') || `${text.length} characters`);
  return text;
}

async function noSideways(page, what) {
  const w = await page.evaluate(() => document.documentElement.scrollWidth);
  check(`${what}: no sideways scrolling`, w <= 392, `scrollWidth ${w}`);
}

async function openAbilities(page, query = '') {
  await page.goto(`${base}/library/abilities${query}`);
  await page.locator('[data-library-section="abilities"]').waitFor({ timeout: 20_000 });
  await settle(page);
}

const browser = await chromium.launch();
try {
  const page = await newPage(browser, { theme: 'light' });

  // A — the list.
  await openAbilities(page);
  await page.locator('[data-ability-list]').waitFor({ timeout: 20_000 });
  const listed = await api(page, 'ability.list', {});
  const names = listed.abilities.map((r) => r.name).sort();
  check('A the starter abilities that passed their check are listed', ['comfyui-local', 'freesound-effects', 'open-media', 'pexels-stock'].every((n) => names.includes(n)), names.join(', '));
  check('A the two not yet runnable aren’t', !names.includes('content-credentials') && !names.includes('davinci-resolve'), names.join(', '));
  check('A they wait for an owner', listed.waiting === names.length, `${listed.waiting} waiting`);
  await plainIn(page, '[data-ability-list]', 'A the list');
  await shot(page, 'A-list-light');

  // B — ComfyUI's page and its approval with the one local service.
  await openAbilities(page, '?ability=comfyui-local');
  await page.locator('[data-approval-page]').waitFor({ timeout: 20_000 });
  const mayDo = await plainIn(page, '[data-approval-page]', 'B what ComfyUI may do');
  check('B it names the one thing on this computer it reaches', mayDo.includes(`ComfyUI on this computer (port ${COMFY_PORT})`) && mayDo.includes('outside MetaRush'), mayDo.slice(0, 300));
  check('B nothing leaves this computer through MetaRush', mayDo.includes(`Nothing leaves this computer through MetaRush; what you give it goes to ComfyUI on this computer (port ${COMFY_PORT})`));
  await shot(page, 'B-comfyui-waiting');
  await page.locator('[data-decide]').getByRole('button', { name: 'Approve this version' }).click();
  const dlg = page.getByRole('dialog');
  await dlg.waitFor({ timeout: 10_000 });
  check('B the question says approving agrees to ComfyUI too', (await dlg.locator('[data-local-service]').count()) === 1);
  const listening = await dlg.locator('[data-listening-now]').innerText().catch(() => '');
  check('B the question shows the program listening now, the script it runs', listening.includes('starter-e2e.mjs') && listening.includes(`port ${COMFY_PORT}`), listening.slice(0, 200));
  await plainIn(page, '[role="dialog"]', 'B the approval question');
  await shot(page, 'B-approve-question');
  await dlg.getByRole('button', { name: 'Approve, with ComfyUI' }).click();
  await dlg.waitFor({ state: 'detached', timeout: 15_000 });
  const approved = await waitFor(async () => {
    const s = await api(page, 'ability.show', { ref: 'comfyui-local' });
    return s.ability.state === 'approved' ? s : null;
  }, 15_000);
  check('B ComfyUI is approved with its port', approved?.ability?.localPort === COMFY_PORT, JSON.stringify(approved?.ability?.localPort));

  // C — set it up.
  await openAbilities(page, '?ability=comfyui-local');
  await page.locator('[data-setup]').click();
  await page.locator('[data-setup-form]').waitFor({ timeout: 10_000 });
  await page.getByRole('dialog').getByRole('button', { name: 'Set it up' }).click();
  await page.getByRole('dialog').waitFor({ state: 'detached', timeout: 15_000 });
  const ready = await waitFor(async () => (await api(page, 'ability.show', { ref: 'comfyui-local' })).here.ready, 15_000);
  check('C on this computer, everything a run needs is here', !!ready);

  // D — run it.
  await openAbilities(page, '?ability=comfyui-local');
  await page.locator('[data-run-button]').click();
  await page.locator('[data-run-form]').waitFor({ timeout: 10_000 });
  await page.locator('[data-input="words"]').fill('a lighthouse at dawn');
  await page.locator('[data-input="seed"]').fill('7');
  await page.locator('[data-look]').click();
  await page.locator('[data-plan]').waitFor({ timeout: 20_000 });
  const plan = await plainIn(page, '[data-plan]', 'D the plan');
  check('D the plan: nothing leaves, the words go to ComfyUI here, a person starts it',
    plan.includes(`goes to ComfyUI on this computer (port ${COMFY_PORT})`) && plan.includes('It costs nothing.') && plan.includes('A person starts each run'), plan.slice(0, 300));
  await shot(page, 'D-plan');
  await page.locator('[data-start]').click();
  const started = await page.waitForURL(/run=abr_/, { timeout: 30_000 }).then(() => true).catch(() => false);
  if (!started) {
    await shot(page, 'D-not-started');
    const said = await page.getByRole('dialog').innerText().catch(() => '');
    throw new Error(`the run didn't start: ${said.slice(-600)}`);
  }
  const runId = new URL(page.url()).searchParams.get('run');
  const done = await waitFor(async () => {
    const r = (await api(page, 'ability.runs', { id: runId })).run;
    return r.state === 'done' || r.state === 'failed' ? r : null;
  }, 120_000, 1000);
  check('D the run is done', done?.state === 'done', JSON.stringify(done ?? {}).slice(0, 300));
  check('D ComfyUI got one graph with the words', prompts.length === 1 && JSON.stringify(prompts[0]).includes('a lighthouse at dawn'), `${prompts.length} graphs`);
  const runs = await api(page, 'ability.runs', { id: runId });
  const hosts = [...new Set(runs.requests.map((q) => `${q.host}${q.refused ? ' (refused)' : ''}`))];
  check('D every request went to ComfyUI on this computer, none refused', hosts.length === 1 && hosts[0] === '127.0.0.1', hosts.join(', '));
  await page.locator(`[data-run="${runId}"] [data-outputs]`).waitFor({ timeout: 20_000 });
  const runText = await plainIn(page, `[data-run="${runId}"]`, 'D the run');
  check('D it brought the picture back', runText.includes('added'), runText.slice(0, 200));
  check('D the picture says its licence', runText.includes('the model’s licence applies') || runText.includes("the model's licence applies"), runText.slice(0, 300));
  await shot(page, 'D-run-done');
  const pic = done?.outputs?.[0]?.resourceId ? await api(page, 'resources.show', { ref: done.outputs[0].resourceId }).catch(() => null) : null;
  const made = JSON.stringify(pic ?? {});
  check('D the picture says how it was made', made.includes('"kind":"ability"') && made.includes('comfyui-local@'), made.slice(0, 200));

  // E — open media and Pexels: what they may do.
  await openAbilities(page, '?ability=open-media');
  await page.locator('[data-approval-page]').waitFor({ timeout: 20_000 });
  const om = await plainIn(page, '[data-approval-page]', 'E what open media may do');
  check('E open media: no key, only words leave, anyone may start it', om.includes('Keys') && om.includes('None.') && om.includes('Words you give it leave this computer') && om.includes('Anyone may run it'), om.slice(0, 300));
  await shot(page, 'E-open-media');
  await openAbilities(page, '?ability=pexels-stock');
  await page.locator('[data-approval-page]').waitFor({ timeout: 20_000 });
  const px = await plainIn(page, '[data-approval-page]', 'E what Pexels may do');
  check('E Pexels: its key goes only to api.pexels.com, a person starts each run', px.includes('only for api.pexels.com') && px.includes('A person starts each run'), px.slice(0, 300));
  await page.context().close();

  // F — phone, dark and light.
  for (const theme of ['dark', 'light']) {
    const p = await newPage(browser, { theme, phone: true });
    await openAbilities(p);
    await p.locator('[data-ability-list]').waitFor({ timeout: 20_000 });
    await shot(p, `F-list-phone-${theme}`);
    await noSideways(p, `F ${theme}: the list`);
    await openAbilities(p, '?ability=comfyui-local');
    await p.locator('[data-approval-page]').waitFor({ timeout: 20_000 });
    await shot(p, `F-comfyui-phone-${theme}`);
    await noSideways(p, `F ${theme}: ComfyUI's page`);
    await p.context().close();
  }
} catch (e) {
  check('the run finished', false, String(e?.stack ?? e));
} finally {
  await browser.close();
  comfy.close();
}
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed${SHOTS ? `; screenshots in ${SHOTS}` : ''}`);
process.exit(failed.length ? 1 : 0);
