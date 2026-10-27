#!/usr/bin/env node
// Captures every dashboard screen at desktop (1440x900) and mobile (390x844),
// in dark and light, into web/.screenshots/. Runs against the Vite dev server
// (or any dashboard URL) backed by a real MetaRush daemon.
//
//   npm run screenshots -w web
//
// Env:
//   METARUSH_DASHBOARD   base URL (default http://localhost:4840)
//   METARUSH_SIGNIN_URL  a one-time sign-in link; if unset, one is minted with
//   METARUSH_MINT        the command that prints it (default: ../.dev/mr dashboard --print-link --dev)
//   SCREENS              comma-separated subset of screen names
//   SCREENSHOT_PROVIDER_KEY  key typed into the Integrations "Add key" dialog (point the
//                        daemon at the fake ElevenLabs server: internal/testutil/fakeeleven/cmd)
//   THEMES / VIEWPORTS   subsets, e.g. THEMES=dark VIEWPORTS=desktop
import { execSync } from 'node:child_process';
import { existsSync, mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const webDir = path.resolve(here, '..');
const repo = path.resolve(webDir, '..');
const outDir = path.join(webDir, '.screenshots');
const base = (process.env.METARUSH_DASHBOARD ?? 'http://localhost:4840').replace(/\/$/, '');
const only = process.env.SCREENS ? new Set(process.env.SCREENS.split(',')) : null;
const themes = (process.env.THEMES ?? 'dark,light').split(',');
const viewports = Object.entries({ desktop: { width: 1440, height: 900 }, mobile: { width: 390, height: 844 } }).filter(([k]) =>
  (process.env.VIEWPORTS ?? 'desktop,mobile').split(',').includes(k),
);

function mintLink() {
  if (process.env.METARUSH_SIGNIN_URL) return process.env.METARUSH_SIGNIN_URL;
  const cmd = process.env.METARUSH_MINT ?? (existsSync(path.join(repo, '.dev/mr')) ? `${path.join(repo, '.dev/mr')} dashboard --print-link --dev` : 'metarush dashboard --print-link');
  const out = execSync(cmd, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`could not find a sign-in link in the output of: ${cmd}\n${out}`);
  // Keep the link on the dashboard origin we are testing.
  const u = new URL(m[0]);
  return `${base}/auth${u.search}`;
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

async function settle(page) {
  await page.waitForLoadState('networkidle', { timeout: 15_000 }).catch(() => {});
  // Wait until no skeletons or spinners remain (bounded), then let motion finish.
  await page
    .waitForFunction(() => document.querySelectorAll('.skeleton, [aria-busy="true"]').length === 0, null, { timeout: 8_000 })
    .catch(() => {});
  await page.waitForTimeout(700);
}

const consoleProblems = [];

// The Claude Code panel opens from the one top-bar button; its dialog is named
// after the conversation, so the helpers key on the composer and header buttons.
async function openAssistant(page) {
  await page.getByRole('button', { name: /^Ask Claude Code/ }).first().click();
  await page.getByLabel('Message to Claude').or(page.getByRole('button', { name: 'Check again' })).first().waitFor({ timeout: 10_000 });
  return true;
}

async function openAssistantHistory(page) {
  const btn = page.getByRole('button', { name: 'All conversations' });
  if (await btn.isVisible().catch(() => false)) await btn.click();
  await page.getByPlaceholder('Search conversations').waitFor({ timeout: 5_000 }).catch(() => {});
}

// Settings → Integrations: pick a project (the oldest) so its voice and spending limit show.
async function chooseProject(page) {
  const sel = page.getByLabel('Project', { exact: true });
  if (!(await sel.count())) return;
  const values = await sel.locator('option').evaluateAll((os) => os.map((o) => o.value).filter(Boolean));
  if (values.length) await sel.selectOption(values[values.length - 1]);
}

// Settings → Integrations: play a voice sample and capture it while it plays.
async function playVoiceSample(page) {
  const play = page.getByRole('button', { name: /^Play a sample of / });
  await play.first().waitFor({ timeout: 10_000 });
  await play.nth((await play.count()) > 2 ? 2 : 0).click();
  await page.getByText('Playing sample').first().waitFor({ timeout: 10_000 });
}

// Settings → Integrations: the add/replace key dialog after checking a key.
async function checkKeyInDialog(page) {
  const open = page.getByRole('button', { name: /^(Add key|Replace key)$/ }).first();
  await open.click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('API key').fill(process.env.SCREENSHOT_PROVIDER_KEY ?? 'example-key-not-real-000000');
  await dialog.getByRole('button', { name: 'Check key' }).click();
  await dialog.getByText(/The key works|did not accept|Could not reach/).first().waitFor({ timeout: 10_000 }).catch(() => {});
}

async function openLatestRun(page) {
  await openAssistantHistory(page);
  const first = page.locator('aside[role="dialog"] ul li > button').first();
  if (await first.isVisible().catch(() => false)) await first.click();
}

async function main() {
  mkdirSync(outDir, { recursive: true });
  const browser = await chromium.launch();
  const link = mintLink();

  // Sign in once; reuse the HttpOnly session cookie for every context.
  const auth = await browser.newContext();
  const ap = await auth.newPage();
  await ap.goto(link);
  await ap.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
  const state = await auth.storageState();

  const projects = (await api(ap, 'project.list')).projects;
  const project = projects[0];
  let show = null;
  let revs = [];
  if (project) {
    show = await api(ap, 'project.show', { id: project.id });
    revs = show.revisions;
  }
  const head = show?.headRevisionId;
  const firstVideo = show?.assets.find((a) => a.kind === 'video' && a.transcript?.words) ?? show?.assets[0];
  const audio = show?.assets.find((a) => a.kind === 'audio');
  await auth.close();

  const screens = [
    { name: 'home', path: '/' },
    project && { name: 'studio-cut', path: `/p/${project.id}` },
    project && { name: 'studio-sources', path: `/p/${project.id}?tab=sources` },
    firstVideo && { name: 'studio-source-viewer', path: `/p/${project.id}?tab=sources&asset=${firstVideo.id}&t=12.5` },
    audio && { name: 'studio-audio-source', path: `/p/${project.id}?tab=sources&asset=${audio.id}` },
    project && { name: 'studio-renders', path: `/p/${project.id}?tab=renders` },
    project && { name: 'studio-history', path: `/p/${project.id}?tab=history` },
    head && revs.length > 1 && { name: 'revision', path: `/p/${project.id}/r/${revs[revs.length - 1].id}` },
    revs.length > 1 && { name: 'compare', path: `/p/${project.id}/compare?left=${revs[revs.length - 1].id}&right=${revs[0].id}` },
    // The Claude Code panel: empty state, the conversation list, and the latest conversation.
    project && { name: 'assistant', path: `/p/${project.id}`, act: openAssistant },
    project && { name: 'assistant-history', path: `/p/${project.id}`, act: async (page) => (await openAssistant(page)) && openAssistantHistory(page) },
    project && { name: 'assistant-run', path: `/p/${project.id}`, act: async (page) => (await openAssistant(page)) && openLatestRun(page) },
    { name: 'queue-jobs', path: '/renders?tab=jobs' },
    { name: 'queue-renders', path: '/renders?tab=renders' },
    { name: 'queue-machines', path: '/renders?tab=machines' },
    { name: 'library', path: '/library' },
    { name: 'library-people', path: '/library/people' },
    { name: 'library-motion', path: '/library/motion' },
    // Settings, one page per section (D184).
    { name: 'settings', path: '/settings' },
    { name: 'settings-people', path: '/settings/people' },
    { name: 'settings-preferences', path: '/settings/preferences' },
    { name: 'settings-claude', path: '/settings/claude' },
    { name: 'settings-footage', path: '/settings/footage' },
    { name: 'settings-extension', path: '/settings/extension' },
    { name: 'settings-storage', path: '/settings/storage' },
    { name: 'settings-brand', path: '/settings/brand' },
    { name: 'settings-log', path: '/settings/log' },
    { name: 'integrations', path: '/settings/integrations', act: chooseProject },
    // Captured the moment the sample starts playing (no settle: it is a few seconds long).
    { name: 'integrations-preview', path: '/settings/integrations', act: playVoiceSample, settleAfter: false },
    { name: 'integrations-key', path: '/settings/integrations', act: checkKeyInDialog },
    { name: 'not-found', path: '/nope' },
    { name: 'signin', path: '/', mode: 'signedOut' },
    { name: 'daemon-down', path: '/', mode: 'offline' },
  ].filter(Boolean);

  const shots = [];
  for (const theme of themes) {
    for (const [vpName, viewport] of viewports) {
      for (const mode of ['signedIn', 'signedOut', 'offline']) {
        const signedOut = mode !== 'signedIn';
        const list = screens.filter((s) => (s.mode ?? 'signedIn') === mode && (!only || only.has(s.name)));
        if (list.length === 0) continue;
        const ctx = await browser.newContext({
          viewport,
          deviceScaleFactor: vpName === 'mobile' ? 2 : 1,
          storageState: signedOut ? undefined : state,
          colorScheme: theme,
          reducedMotion: 'reduce',
        });
        await ctx.addInitScript((t) => {
          try {
            localStorage.setItem('metarush.theme', t);
          } catch {}
        }, theme);
        const page = await ctx.newPage();
        // Simulate a stopped daemon: every API call fails at the network level.
        if (mode === 'offline') await page.route((u) => u.pathname.startsWith('/api/'), (r) => r.abort('connectionrefused'));
        page.on('console', (m) => {
          if (m.type() !== 'error') return;
          // Signed out, the session probe is *expected* to answer 401.
          if (signedOut && /status of 401|ERR_CONNECTION_REFUSED|ERR_FAILED/.test(m.text())) return;
          consoleProblems.push(`[${theme}/${vpName}] console.error ${page.url()}: ${m.text()}`);
        });
        page.on('pageerror', (e) => consoleProblems.push(`[${theme}/${vpName}] pageerror ${page.url()}: ${e.message}`));
        for (const s of list) {
          await page.goto(base + s.path);
          if (mode !== 'signedIn') await page.getByRole('heading', { level: 1 }).first().waitFor({ timeout: 10_000 }).catch(() => {});
          await settle(page);
          if (s.act) {
            await s.act(page);
            if (s.settleAfter !== false) await settle(page);
          }
          const file = path.join(outDir, `${s.name}-${vpName}-${theme}.png`);
          await page.screenshot({ path: file, fullPage: true });
          const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
          if (overflow > 0) consoleProblems.push(`[${theme}/${vpName}] horizontal overflow of ${overflow}px on ${s.name}`);
          shots.push(path.relative(webDir, file));
        }
        await ctx.close();
      }
    }
  }
  await browser.close();
  console.log(`Captured ${shots.length} screenshots in ${path.relative(process.cwd(), outDir) || outDir}`);
  for (const s of shots) console.log(`  ${s}`);
  if (consoleProblems.length) {
    console.log(`\n${consoleProblems.length} problem(s):`);
    for (const p of consoleProblems) console.log(`  ${p}`);
    process.exitCode = 1;
  } else console.log('\nNo console errors, page errors or horizontal overflow.');
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
