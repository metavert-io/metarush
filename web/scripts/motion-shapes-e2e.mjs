#!/usr/bin/env node
// A motion graphic drawn in another shape after a person approved it (D377,
// D381), run by scripts/motion-shapes-e2e.sh: a person approves the vertical
// version in the dashboard (styled dialog) · a non-person draws it again
// widescreen from a terminal · the frames the person approved stay as they
// were, the widescreen frames are added and marked as not approved · a
// widescreen cut is noted with the approval as the way out · the person
// approves it in that shape on the graphic's page · the note clears. No
// native dialogs, no console errors.
import { execSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const { MS_MR: mr, MS_PROJECT: projectId, MS_VERSION: version } = process.env;
const shots = process.env.SHOTS;
if (!base || !process.env.METARUSH_MINT || !mr || !projectId || !version) {
  console.error('run through scripts/motion-shapes-e2e.sh');
  process.exit(2);
}
if (shots) fs.mkdirSync(shots, { recursive: true });

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

async function shot(page, name) {
  if (shots) await page.screenshot({ path: path.join(shots, `${name}.png`), fullPage: true });
}

// The note a widescreen cut using this version gets (D379, D381).
async function wideNote(page, hash) {
  const spec = {
    schemaVersion: 1,
    output: { width: 1920, height: 1080, fps: 30 },
    clips: [{ id: 'a', assetId: 'ast_01jabcdefghjkmnpqrstvwxyz0', in: 0, out: 2 }],
    overlays: [{ id: 'route', motion: { name: 'route-map', hash }, at: 0.2, duration: 3, params: { from: 'Lisbon', to: 'Porto' } }],
  };
  const out = await api(page, 'edit.check', { plan: { projectId, spec } });
  return (out.warnings ?? []).find((w) => w.code === 'motion_shape_undrawn');
}

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
const page = await ctx.newPage();
const problems = [];
page.on('dialog', async (d) => {
  problems.push(`native ${d.type()} dialog: ${d.message()}`);
  await d.dismiss();
});
page.on('console', (m) => {
  if (m.type() === 'error' && !/favicon|Failed to load resource/.test(m.text())) problems.push(`console error: ${m.text()}`);
});
page.on('pageerror', (e) => problems.push(`page error: ${e.message}`));
try {
  await page.goto(mintLink());
  const first = await api(page, 'motion.show', { ref: version });
  const hash = first.row.hash;
  const name = first.row.name;

  // A person approves it, vertical, in the dashboard.
  await page.goto(`${base}/motion/${hash}`);
  await page.getByRole('button', { name: 'Approve' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.waitFor();
  await dialog.getByRole('button', { name: 'Approve' }).click();
  await page.getByText(`Approved ${name}@`).first().waitFor({ timeout: 15_000 });
  const approved = await api(page, 'motion.show', { ref: hash });
  check('a person approved it in the shape they saw', approved.row.state === 'approved' && JSON.stringify(approved.component.approvedShapes) === '["1080x1920"]', JSON.stringify(approved.component.approvedShapes));
  const seen = (approved.component.frames ?? []).map((f) => f.object.sha256);

  // A non-person draws it again widescreen, from a terminal.
  const redraw = JSON.parse(execSync(`${mr} motion check --version ${version} --shape landscape --no-preview --json`, { cwd: repo, encoding: 'utf8' }));
  check('drawing again keeps the approval and says it covers only vertical', redraw.data.ok && redraw.data.redrawn && redraw.data.state === 'approved' && /approval stands for the shapes a person approved it in/.test(redraw.data.summary), redraw.data.summary);
  const after = await api(page, 'motion.show', { ref: hash });
  const kept = seen.every((s, i) => after.component.frames[i]?.object.sha256 === s);
  check('the frames the person approved stay as they were', kept && after.component.frames.length > seen.length, `${seen.length} → ${after.component.frames.length}`);
  const note = await wideNote(page, hash);
  check('a widescreen cut is noted, with a person approving it as the way out', !!note && /motion approve/.test(note.command ?? ''), note?.command ?? 'no note');

  // The page shows the new shape as not approved yet; the person approves it there.
  await page.reload();
  const approveShape = page.locator('[data-approve-shape="1920x1080"]');
  await approveShape.waitFor({ timeout: 15_000 });
  check('the widescreen frames are marked as not approved yet', (await page.getByText('Not approved in this shape yet').count()) >= 1);
  await shot(page, 'motion-shape-not-approved');
  await approveShape.getByRole('button', { name: 'Approve in this shape' }).click();
  const confirm = page.getByRole('dialog');
  await confirm.waitFor();
  check('approving a shape asks in a styled dialog', await confirm.getByText(`Approve ${name} widescreen (16:9)?`).isVisible());
  await confirm.getByRole('button', { name: 'Approve in this shape' }).click();
  await page.getByText('in widescreen (16:9) too').first().waitFor({ timeout: 15_000 });
  await approveShape.waitFor({ state: 'detached', timeout: 15_000 });
  await shot(page, 'motion-shape-approved');
  const done = await api(page, 'motion.show', { ref: hash });
  check('approved in both shapes', JSON.stringify(done.component.approvedShapes) === '["1080x1920","1920x1080"]', JSON.stringify(done.component.approvedShapes));
  check('the widescreen cut has no note now', !(await wideNote(page, hash)));
} catch (e) {
  check('the run finished', false, e.message);
  await shot(page, 'motion-shapes-failure').catch(() => {});
} finally {
  check('no native dialogs or console errors', problems.length === 0, problems.join(' | '));
  await browser.close();
}
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
process.exit(failed.length ? 1 : 0);
