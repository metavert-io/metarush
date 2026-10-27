#!/usr/bin/env node
// End-to-end check of Settings → Integrations against a daemon whose ElevenLabs
// endpoint is the fake server (internal/testutil/fakeeleven/cmd/fakeeleven), so no
// real account or key is involved:
//   add a key (check, then save) · the key never comes back in any response, the
//   page or browser storage · choose a default voice · play and stop a sample ·
//   change how the voice sounds · favorite a voice · set a project's spending
//   limit · remove the key with the styled confirm dialog.
//
//   METARUSH_DASHBOARD=http://127.0.0.1:4881 \
//   METARUSH_MINT="bin/metarush --home <test home> dashboard --print-link" \
//   INTEGRATIONS_KEY_FILE=<file with the fake server's key> node scripts/integrations-e2e.mjs
import { execSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:4845').replace(/\/$/, '');
const keyFile = process.env.INTEGRATIONS_KEY_FILE;
if (!keyFile || !process.env.METARUSH_MINT) {
  console.error('set INTEGRATIONS_KEY_FILE and METARUSH_MINT (see the header of this script)');
  process.exit(2);
}
const KEY = readFileSync(keyFile, 'utf8').trim();

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

async function toast(page, text) {
  await page.getByText(text).first().waitFor({ timeout: 15_000 });
}

async function main() {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await ctx.newPage();
  const leaks = [];
  const problems = [];
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

  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
  await page.goto(`${base}/settings/integrations`);
  await page.getByRole('heading', { name: 'Integrations', level: 1 }).waitFor();

  // Start from "not set up": remove a key left by an earlier run (styled confirm, never window.confirm).
  page.on('dialog', (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  const card = page.locator('section[data-provider="elevenlabs"]');
  if (await card.getByRole('button', { name: 'Remove' }).isVisible()) {
    await card.getByRole('button', { name: 'Remove' }).click();
    const confirm = page.getByRole('alertdialog');
    await confirm.getByRole('button', { name: 'Remove key' }).click();
    await card.getByText('Not set up').waitFor({ timeout: 15_000 });
  }
  check('starts without a key', await card.getByText('Not set up').isVisible());

  // Add the key: a wrong key is refused, the right one checks out, then saves.
  await card.getByRole('button', { name: 'Add key' }).click();
  let dialog = page.getByRole('dialog');
  const field = dialog.getByLabel('API key');
  check('key field hides what is typed', (await field.getAttribute('type')) === 'password');
  await field.fill('sk_wrong_' + 'x'.repeat(24));
  await dialog.getByRole('button', { name: 'Check key' }).click();
  await dialog.getByText('did not accept this key').waitFor({ timeout: 15_000 });
  check('a wrong key is refused before saving', await dialog.getByRole('button', { name: 'Save key' }).isHidden());
  await field.fill(KEY);
  await dialog.getByRole('button', { name: 'Check key' }).click();
  await dialog.getByText('The key works').waitFor({ timeout: 15_000 });
  await dialog.getByRole('button', { name: 'Save key' }).click();
  await toast(page, 'ElevenLabs is connected');
  await card.getByText('Connected', { exact: true }).waitFor();
  const masked = (await card.getByText(/^••••/).first().textContent())?.trim();
  check('shows only the last four characters', masked === `••••${KEY.slice(-4)}`, masked);

  // Voice: choose a default, play a sample and stop it, favorite one.
  const voices = page.getByRole('list', { name: 'Your voices' });
  await voices.waitFor();
  // Pick a voice that is not the default yet (an earlier run may have chosen one).
  const before = await api(page, 'provider.status', { name: 'elevenlabs' });
  const name = ['Cleo', 'Bram', 'Ada'].find((n) => n !== before.workspace?.voiceName);
  const row = voices.getByRole('listitem').filter({ hasText: name });
  await row.getByRole('button', { name: 'Use as default' }).click();
  await toast(page, `${name} is now the default voice`);
  let st = await api(page, 'provider.status', { name: 'elevenlabs' });
  check('default voice saved for the workspace', st.workspace?.voiceName === name, st.workspace?.voiceName);

  const other = voices.getByRole('listitem').filter({ hasText: 'Bram' });
  await other.getByRole('button', { name: 'Play a sample of Bram' }).click();
  await other.getByText('Playing sample').waitFor({ timeout: 10_000 });
  check('a sample plays through the daemon', true);
  await other.getByRole('button', { name: 'Stop the sample of Bram' }).click();
  await other.getByText('Playing sample').waitFor({ state: 'hidden', timeout: 5_000 });
  check('the sample stops', true);

  const fav = row.getByRole('button', { name: /favorites$/ });
  const wasFav = (await fav.getAttribute('aria-pressed')) === 'true';
  await fav.click();
  await toast(page, wasFav ? `${name} removed from favorites` : `${name} added to favorites`);
  const vs = await api(page, 'voiceover.voices', { provider: 'elevenlabs' });
  check('favorite saved', !!vs.voices.find((v) => v.name === name)?.favorite === !wasFav);

  // How it sounds: speed up with the keyboard (10%, or 5% if an earlier run saved 10%), then save.
  const speed = page.getByRole('slider', { name: 'Speed' });
  const target = Math.abs((st.workspace?.voice?.speed ?? 1) - 1.1) < 1e-6 ? 1.05 : 1.1;
  await speed.focus();
  await page.keyboard.press('Home');
  for (let i = 0; i < Math.round((target - 0.7) / 0.01); i++) await page.keyboard.press('ArrowRight');
  await page.getByText(`${Math.round((target - 1) * 100)}% faster`).first().waitFor();
  await page.getByRole('button', { name: 'Save changes' }).click();
  await toast(page, 'Voice settings saved');
  st = await api(page, 'provider.status', { name: 'elevenlabs' });
  check('speed saved', Math.abs((st.workspace?.voice?.speed ?? 0) - target) < 1e-6, String(st.workspace?.voice?.speed));
  check('effective settings come from the workspace', st.effective.from.speed === 'workspace');

  // One project: a spending limit.
  const projects = (await api(page, 'project.list')).projects;
  if (projects.length) {
    const p = projects[projects.length - 1];
    await page.getByLabel('Project', { exact: true }).selectOption(p.id);
    const limit = page.getByLabel(/Spending limit/);
    const prev = (await api(page, 'provider.status', { name: 'elevenlabs', projectId: p.id })).budget?.limitCredits;
    const want = prev === 5000 ? 6000 : 5000;
    await limit.fill(String(want));
    await page.getByRole('button', { name: 'Save limit' }).click();
    await toast(page, `${p.name} may spend up to ${want.toLocaleString('en-US')} credits`);
    st = await api(page, 'provider.status', { name: 'elevenlabs', projectId: p.id });
    check('project spending limit saved', st.budget?.limitCredits === want, JSON.stringify(st.budget));
  } else check('project spending limit saved', false, 'no project to test with');

  // The key never comes back.
  const html = await page.content();
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
  check('no response contained the key', leaks.length === 0, leaks.join(', '));
  check('the page never shows the key', !html.includes(KEY));
  check('browser storage never holds the key', !stored.includes(KEY));

  // Remove with the styled confirm; cancelling first keeps it.
  await card.getByRole('button', { name: 'Remove' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Cancel' }).click();
  check('cancel keeps the key', await card.getByText('Connected', { exact: true }).isVisible());
  await card.getByRole('button', { name: 'Remove' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Remove key' }).click();
  await card.getByText('Not set up').waitFor({ timeout: 15_000 });
  const list = await api(page, 'provider.list');
  check('key removed from this computer', list.providers.find((p) => p.name === 'elevenlabs')?.configured === false);
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
