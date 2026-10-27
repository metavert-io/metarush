#!/usr/bin/env node
// A smoke pass over the dashboard's main pages with the plain-words guard:
// every page's visible text — the dashboard's own copy and the words the server
// sends (activity rows, quality checks, to-dos, job advice, edit problems) — is
// checked with the rules the Go side uses (internal/plainwords/rules.json).
// Closed Details disclosures and the cut sheet's text views are not visible
// text, so they are not checked; people's own words (titles, file names, names:
// <Theirs> in the dashboard, curly quotes in the server's sentences) are only
// checked for IDs (D113). Screenshots go to $PLAIN_SHOTS (if set).
//
//   METARUSH_DASHBOARD=http://127.0.0.1:4961 METARUSH_SIGNIN_URL=<one-time link> \
//     node web/scripts/plain-words-e2e.mjs
//
// Env: METARUSH_DASHBOARD (base URL), METARUSH_SIGNIN_URL (a link from
// `metarush dashboard --print-link`), PLAIN_SHOTS (folder for screenshots).
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { chromium } from 'playwright';
import { THEIRS_CLOSE, THEIRS_OPEN, checkPanel, checkShown } from './plainwords.mjs';

const base = (process.env.METARUSH_DASHBOARD ?? 'http://127.0.0.1:4961').replace(/\/$/, '');
const signin = process.env.METARUSH_SIGNIN_URL;
if (!signin) {
  console.error('METARUSH_SIGNIN_URL is required (metarush dashboard --print-link)');
  process.exit(2);
}
const shots = process.env.PLAIN_SHOTS;
if (shots) mkdirSync(shots, { recursive: true });

// What a page may show on purpose: the person's own words and names
// (footage file names, a folder they chose), and the version tags in badges.
const allow = [
  { kind: 'path', test: (t) => /^~\//.test(t), why: 'a folder the person chose (the watched folder), in its short form' },
];

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
  await page.waitForFunction(() => document.querySelectorAll('.skeleton, [aria-busy="true"]').length === 0, null, { timeout: 8_000 }).catch(() => {});
  await page.waitForTimeout(600);
}

// The visible text, without the cut sheet's text views (the code form people
// edit by hand). People's own words (<Theirs>, data-user-content: titles, file
// names, names) are read inside curly quotes, the mark plain text uses for them
// too, so checkShown skips them except for IDs (D113).
async function visibleText(page) {
  return page.evaluate(([open, close]) => {
    const skip = document.querySelectorAll('.cm-editor, [data-plain-skip]');
    const hidden = [];
    for (const el of skip) {
      hidden.push([el, el.style.display]);
      el.style.display = 'none';
    }
    const added = [];
    for (const el of document.querySelectorAll('[data-user-content]')) {
      if (el.parentElement?.closest('[data-user-content]') || !el.textContent?.trim()) continue;
      const a = document.createTextNode(open);
      const b = document.createTextNode(close);
      el.prepend(a);
      el.append(b);
      added.push(a, b);
    }
    const text = document.body.innerText;
    for (const n of added) n.remove();
    for (const [el, d] of hidden) el.style.display = d;
    return text;
  }, [THEIRS_OPEN, THEIRS_CLOSE]);
}

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
const page = await ctx.newPage();
const u = new URL(signin);
await page.goto(`${base}/auth${u.search}`);
await settle(page);

const projects = (await api(page, 'project.list', {})).projects ?? [];
// The project with the most versions shows the most (cuts, narration, renders, quality checks).
const project = [...projects].sort((a, b) => (b.revisionSeq ?? 0) - (a.revisionSeq ?? 0))[0];
const revs = project ? ((await api(page, 'revision.list', { projectId: project.id })).revisions ?? []) : [];
const head = revs.find((r) => r.isHead) ?? revs[0];
const assets = project ? ((await api(page, 'asset.list', { projectId: project.id, limit: 50 })).assets ?? []) : [];
const video = assets.find((a) => a.kind === 'video');

const pages = [
  { name: 'home', path: '/' },
  project && { name: 'studio-cut', path: `/p/${project.id}` },
  project && { name: 'studio-conversation', path: `/p/${project.id}?tab=conversation` },
  project && { name: 'studio-sources', path: `/p/${project.id}?tab=sources` },
  video && { name: 'studio-source', path: `/p/${project.id}?tab=sources&asset=${video.id}` },
  project && { name: 'studio-script', path: `/p/${project.id}?tab=script` },
  project && { name: 'studio-sound', path: `/p/${project.id}?tab=sound` },
  project && { name: 'studio-voiceover', path: `/p/${project.id}?tab=voiceover` },
  project && { name: 'studio-resources', path: `/p/${project.id}?tab=resources` },
  project && { name: 'studio-renders', path: `/p/${project.id}?tab=renders` },
  project && { name: 'studio-history', path: `/p/${project.id}?tab=history` },
  project && { name: 'studio-settings', path: `/p/${project.id}?tab=settings` },
  head && { name: 'revision', path: `/p/${project.id}/r/${head.id}` },
  revs.length > 1 && { name: 'compare', path: `/p/${project.id}/compare?left=${revs[revs.length - 1].id}&right=${revs[0].id}` },
  { name: 'library', path: '/library' },
  { name: 'people', path: '/library/people' },
  { name: 'motion', path: '/library/motion' },
  { name: 'todos', path: '/todos' },
  { name: 'queue-jobs', path: '/renders?tab=jobs' },
  { name: 'queue-renders', path: '/renders?tab=renders' },
  { name: 'queue-machines', path: '/renders?tab=machines' },
  { name: 'improvements', path: '/improvements' },
  // Settings, one page per section (D184).
  { name: 'settings', path: '/settings' },
  { name: 'settings-people', path: '/settings/people' },
  { name: 'settings-preferences', path: '/settings/preferences' },
  { name: 'settings-claude', path: '/settings/claude' },
  { name: 'settings-footage', path: '/settings/footage' },
  { name: 'settings-extension', path: '/settings/extension' },
  { name: 'settings-storage', path: '/settings/storage' },
  { name: 'brand', path: '/settings/brand' },
  { name: 'integrations', path: '/settings/integrations' },
  { name: 'log', path: '/settings/log' },
  { name: 'settings-cli', path: '/settings/cli' },
  { name: 'not-found', path: '/nope' },
].filter(Boolean);

let failed = 0;
for (const p of pages) {
  await page.goto(base + p.path);
  await settle(page);
  if (shots) await page.screenshot({ path: path.join(shots, `sweep-${p.name}.png`), fullPage: true });
  const text = await visibleText(page);
  const found = [];
  for (const line of text.split('\n')) {
    for (const f of checkShown(line)) {
      if (allow.some((a) => a.kind === f.kind && a.test(f.text))) continue;
      found.push(`${f.kind} ${JSON.stringify(f.text)} in ${JSON.stringify(line.trim().slice(0, 140))}`);
    }
  }
  if (found.length) {
    failed++;
    console.error(`FAIL ${p.name} (${p.path}):\n  ${[...new Set(found)].join('\n  ')}`);
  } else {
    console.log(`ok   ${p.name}`);
  }
}
// The Claude Code panel on a conversation (PLAIN_CONVERSATION, in its project
// PLAIN_CONVERSATION_PROJECT): its step lines, cards and notes are held to the
// panel's stricter rules too (checkPanel, D140: no file names, version codes,
// millisecond times or tool words).
let checked = pages.length;
if (process.env.PLAIN_CONVERSATION) {
  checked++;
  const conv = process.env.PLAIN_CONVERSATION;
  const where = process.env.PLAIN_CONVERSATION_PROJECT ? `/p/${process.env.PLAIN_CONVERSATION_PROJECT}?ask=${conv}` : `/?ask=${conv}`;
  await page.goto(base + where);
  await page.locator('[aria-label="Conversation"]').waitFor({ timeout: 30_000 });
  await settle(page);
  if (shots) await page.screenshot({ path: path.join(shots, 'sweep-panel.png') });
  const text = await page.evaluate(([open, close]) => {
    const panel = document.querySelector('aside[role="dialog"]');
    if (!panel) return '';
    const added = [];
    for (const el of panel.querySelectorAll('[data-user-content]')) {
      if (el.parentElement?.closest('[data-user-content]') || !el.textContent?.trim()) continue;
      const a = document.createTextNode(open);
      const b = document.createTextNode(close);
      el.prepend(a);
      el.append(b);
      added.push(a, b);
    }
    const t = panel.innerText;
    for (const n of added) n.remove();
    return t;
  }, [THEIRS_OPEN, THEIRS_CLOSE]);
  const found = [];
  for (const line of text.split('\n')) {
    for (const f of checkPanel(line)) found.push(`${f.kind} ${JSON.stringify(f.text)} in ${JSON.stringify(line.trim().slice(0, 140))}`);
  }
  if (!text.trim()) found.push('the panel did not open');
  if (found.length) {
    failed++;
    console.error(`FAIL the Claude Code panel (${where}):\n  ${[...new Set(found)].join('\n  ')}`);
  } else {
    console.log('ok   the Claude Code panel');
  }
}
await browser.close();
if (failed) {
  console.error(`${failed} of ${checked} pages show words a person should not have to read`);
  process.exit(1);
}
console.log(`all ${checked} pages speak plainly ✓`);
