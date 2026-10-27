#!/usr/bin/env node
// Layout packs in a real browser (D263–D267). Run it through
// scripts/layout-packs-e2e.sh: a throwaway installation where "Acme" (the
// default's settings, with a logo and colours) and "Client" (uses the default;
// its first cut names a title card, a lower third and an end screen) exist, a
// motion graphic acme-title waits for a person, and a fake `claude` proposes
// Acme's pack when asked. It checks that:
//   1. Settings → Layout pack shows every role previewed in the house style
//      (graphics in the player, call layouts as boxes), MetaRush's own pack;
//   2. choosing the cinematic lower third makes a pack of Acme's own, offers
//      Client's latest cut, and taking the offer saves a version drawn with it;
//   3. a round inset for picture in picture shows in its preview;
//   4. in the Claude panel, the pack Claude proposes is a card a person decides
//      in a styled dialog; while its graphic waits it is kept as the proposed
//      pack, not made current;
//   5. Settings → Layout pack shows the proposed pack, what it waits for and a
//      link to review the graphic; approving it under Motion (a styled dialog)
//      lets a person accept the pack, which becomes current and is offered to
//      Client's latest cut: its title card becomes Acme's motion graphic, its
//      end screen the end.screen template;
//   6. previews follow the shape asked for;
//   7. no native dialogs, page errors or horizontal overflow; a phone (390 px)
//      and the light theme fit.
// Screenshots in PACKS_SHOTS. It CHANGES the projects: point it only at a
// throwaway installation.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { checkPanel } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const base = (process.env.METARUSH_DASHBOARD ?? '').replace(/\/$/, '');
const need = ['METARUSH_DASHBOARD', 'METARUSH_MINT', 'PACKS_ACME', 'PACKS_CLIENT', 'PACKS_MOTION', 'PACKS_SHOTS'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/layout-packs-e2e.sh does)`);
    process.exit(2);
  }
}
const acme = process.env.PACKS_ACME;
const client = process.env.PACKS_CLIENT;
const motionHash = process.env.PACKS_MOTION;
const shotsDir = process.env.PACKS_SHOTS;
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

let shotN = 0;
async function shot(page, name) {
  await page.waitForTimeout(500);
  await page.screenshot({ path: path.join(shotsDir, `${String(++shotN).padStart(2, '0')}-${name}.png`) });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 0) problems.push(`horizontal overflow of ${overflow}px on ${name}`);
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

/** The latest version of a project's cut. */
async function head(page, projectId) {
  const list = (await api(page, 'revision.list', { projectId })).revisions;
  return list.find((r) => r.isHead) ?? list[0];
}

async function headSpec(page, projectId) {
  const h = await head(page, projectId);
  const r = await api(page, 'revision.show', { id: h.id });
  return { number: h.number, spec: r.revision?.spec ?? r.spec };
}

const overlay = (spec, id) => (spec.overlays ?? []).find((o) => o.id === id);
const settings = (id) => `${base}/p/${id}?tab=settings`;
const packPart = (page) => page.locator('[data-layout-pack]');

async function openPack(page, id) {
  await page.goto(settings(id));
  const part = packPart(page);
  await part.waitFor({ timeout: 30_000 });
  // The Claude panel stays open across pages once opened: close it.
  await page.waitForTimeout(600);
  const close = page.getByRole('button', { name: 'Close (Esc)' });
  if (await close.count()) {
    await close.first().click();
    await page.waitForTimeout(400);
  }
  await part.scrollIntoViewIfNeeded();
  return part;
}

/** Clicks the styled dialog's confirm button. */
async function confirmDialog(page) {
  const dlg = page.locator('[data-mr-dialog="open"]');
  await dlg.waitFor({ timeout: 10_000 });
  const text = (await dlg.innerText()).replace(/\s+/g, ' ');
  await dlg.locator('button[type="submit"]').click();
  await dlg.waitFor({ state: 'detached', timeout: 15_000 });
  return text;
}

async function main() {
  const browser = await chromium.launch();
  const { ctx, page } = await newPage(browser);
  await page.goto(mintLink());
  await page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 20_000 });
  const state = await ctx.storageState();

  // 1. MetaRush's own pack, every role previewed in Acme's brand.
  const first = await headSpec(page, client);
  check(
    "Client's first cut: its roles drawn with MetaRush's own pack",
    overlay(first.spec, 'intro')?.template === 'title.card' && overlay(first.spec, 'ada')?.template === 'lower-third.clean' && overlay(first.spec, 'end')?.template === 'endcard.cta',
    (first.spec.overlays ?? []).map((o) => `${o.id}:${o.template}`).join(' '),
  );
  let part = await openPack(page, acme);
  const roles = await part.locator('[data-pack-role]').count();
  check('the part lists every role', roles === 7, `${roles} roles`);
  check("it is MetaRush's own pack until Acme has one", (await part.getByText('Starter pack').count()) === 1);
  const players = await until(async () => {
    const n = await part.locator('[data-pack-role] [data-pack-preview] .__remotion-player').count();
    return n >= 5 ? n : 0;
  }, 30_000);
  check('each graphic plays in the player', players === 5, `${players} players`);
  const boxes = await part.locator('[data-pack-role="picture-in-picture"] svg rect').count();
  check("picture in picture is previewed as Go places the pictures", boxes >= 3, `${boxes} boxes`);
  const lines = (await part.innerText()).split('\n').filter((l) => l.trim());
  const unplain = lines.flatMap((l) => checkPanel(l).map((f) => `${f.kind} ${f.text} in ${JSON.stringify(l)}`));
  check('the part reads plainly', unplain.length === 0, unplain.join(' | '));
  await shot(page, 'pack-starter-desktop-dark');

  // 2. A cinematic lower third: Acme's own pack, offered to Client's latest cut.
  await part.locator('[data-pack-field="role:lower-third"]').selectOption('lower-third.cinematic');
  const own = await until(async () => {
    const p = await api(page, 'content.pack.show', { projectId: acme });
    return p.own && p.pack.pieces?.['lower-third']?.template === 'lower-third.cinematic' ? p : null;
  });
  check('choosing a template makes a pack of Acme\'s own', !!own && own.pack.version === 1, own ? `version ${own.pack.version}` : 'not saved');
  const offer = part.locator(`[data-pack-offer="${client}"]`);
  await offer.waitFor({ timeout: 30_000 });
  await shot(page, 'pack-offer-desktop-dark');
  await offer.click();
  const second = await until(async () => {
    const s = await headSpec(page, client);
    return s.number > first.number ? s : null;
  });
  check("taking the offer saves Client's cut with the pack's lower third", overlay(second?.spec ?? {}, 'ada')?.template === 'lower-third.cinematic', overlay(second?.spec ?? {}, 'ada')?.template);

  // 3. A round inset.
  await part.locator('[data-pack-field="pipShape"]').selectOption('circle');
  const round = await until(async () => {
    const p = await api(page, 'content.pack.show', { projectId: acme });
    return p.pack.layouts?.pip?.shape === 'circle' ? p : null;
  });
  check('picture in picture: a round inset', !!round);
  const inset = await until(async () => {
    const rx = await part.locator('[data-pack-role="picture-in-picture"] svg rect').nth(2).getAttribute('rx');
    return Number(rx) > 50 ? Number(rx) : 0;
  }, 15_000);
  check('the preview shows the circle', inset > 50, `rx ${inset}`);

  // 4. Claude proposes a pack: a card only a person decides.
  const asked = await api(page, 'assistant.ask', { prompt: 'Draw a layout pack for Acme', projectId: acme });
  const convId = asked.conversation.id;
  const cv = await until(async () => {
    const c = await api(page, 'assistant.show', { id: convId, events: false });
    return c.state === 'idle' && c.actions.length >= 1 ? c : null;
  }, 60_000);
  const card = cv?.actions?.[0];
  check("Claude's pack is a card a person decides", card?.op === 'content.pack.accept' && card.humanOnly === true && /^Use “Acme spring” as the layout pack of /.test(card.title ?? ''), card?.title);
  check('the card says what waits', (card?.details ?? []).some((d) => d.includes('Waits for a person to approve acme-title')), (card?.details ?? []).join(' | '));
  await page.goto(`${base}/p/${acme}?ask=${convId}`);
  const cardEl = page.locator(`[aria-label="Claude asks: ${card.title.replace(/"/g, '\\"')}"]`).last();
  await cardEl.waitFor({ timeout: 30_000 });
  await cardEl.scrollIntoViewIfNeeded();
  await shot(page, 'pack-card-desktop-dark');
  await cardEl.getByRole('button', { name: 'Use this pack', exact: true }).click();
  const dlgText = await confirmDialog(page);
  check('deciding asks in a styled dialog', /Use “Acme spring” as the layout pack/.test(dlgText), dlgText.slice(0, 160));
  await cardEl.getByText('Done', { exact: true }).or(cardEl.getByText('Did not work', { exact: true })).waitFor({ timeout: 60_000 });
  const kept = await api(page, 'content.pack.show', { projectId: acme });
  check('while its graphic waits, the pack is kept as the proposed pack', kept.draft && !kept.draft.ready && kept.pack.name !== 'Acme spring' && kept.draft.waiting?.[0]?.name === 'acme-title', JSON.stringify(kept.draft?.waiting ?? []));

  // 5. Settings shows it; approve the graphic; accept the pack.
  part = await openPack(page, acme);
  const draft = part.locator('[data-pack-draft]');
  await draft.waitFor({ timeout: 15_000 });
  check('Settings → Layout pack shows the proposed pack', (await draft.innerText()).includes('Acme spring'));
  check('it can\'t be used before its graphic is approved', await draft.locator('[data-pack-accept]').isDisabled());
  await shot(page, 'pack-draft-waiting-desktop-dark');
  await draft.getByRole('link', { name: 'review and approve it' }).click();
  await page.waitForURL((u) => u.pathname === `/motion/${motionHash}`, { timeout: 15_000 });
  await page.getByRole('button', { name: 'Approve' }).first().click();
  const appr = page.getByRole('dialog');
  await appr.waitFor();
  await appr.getByRole('button', { name: 'Approve' }).click();
  await page.getByText('Approved acme-title@').first().waitFor({ timeout: 15_000 });
  check('a person approved the graphic', (await api(page, 'motion.show', { ref: motionHash })).row.state === 'approved');
  part = await openPack(page, acme);
  const ready = part.locator('[data-pack-draft-ready]');
  await ready.waitFor({ timeout: 15_000 });
  await ready.locator('[data-pack-accept]').click();
  const acceptText = await confirmDialog(page);
  check('accepting asks in a styled dialog', /Make the proposed pack the layout pack\?/.test(acceptText), acceptText.slice(0, 120));
  const current = await until(async () => {
    const p = await api(page, 'content.pack.show', { projectId: acme });
    return p.pack.name === 'Acme spring' && !p.draft ? p : null;
  });
  check('the proposed pack is the layout pack now', !!current && current.pack.version === 3 && current.pack.pieces?.['title-card']?.motion?.name === 'acme-title', current ? `version ${current.pack.version}` : 'not current');
  const offer2 = part.locator(`[data-pack-offer="${client}"]`);
  await offer2.waitFor({ timeout: 30_000 });
  await offer2.click();
  const third = await until(async () => {
    const s = await headSpec(page, client);
    return s.number > second.number ? s : null;
  });
  const intro = overlay(third?.spec ?? {}, 'intro');
  const end = overlay(third?.spec ?? {}, 'end');
  check("Client's title card is Acme's motion graphic, with its words", intro?.motion?.hash === motionHash && intro?.params?.title === 'Spring launch' && intro?.params?.wipe === 'left', JSON.stringify(intro));
  check("Client's end screen leaves room for the next videos", end?.template === 'end.screen' && end.duration === 5 && end.params?.headline === 'Thanks for watching' && end.params?.logo, JSON.stringify(end));
  await part.scrollIntoViewIfNeeded();
  await shot(page, 'pack-accepted-desktop-dark');

  // A person picks drawings for a role in the dashboard: a template, then the approved motion graphic again.
  const titleSel = part.locator('[data-pack-field="role:title-card"]');
  await titleSel.selectOption('slide.title');
  const slide = await until(async () => ((await api(page, 'content.pack.show', { projectId: acme })).pack.pieces?.['title-card']?.template === 'slide.title' ? true : null));
  await titleSel.selectOption(`motion:acme-title@${motionHash.slice(0, 8)}`);
  const back = await until(async () => ((await api(page, 'content.pack.show', { projectId: acme })).pack.pieces?.['title-card']?.motion?.hash === motionHash ? true : null));
  check('a person chooses the title slide, then the approved motion graphic, in the dashboard', !!slide && !!back);

  // 6. Previews follow the shape.
  await part.locator('[data-pack-field="shape"]').selectOption('vertical');
  const vertical = await until(async () => {
    const r = await part.locator('[data-pack-role="title-card"] [data-pack-preview]').evaluate((el) => el.getBoundingClientRect());
    return r.height > r.width ? r : null;
  }, 20_000);
  check('previews follow the shape asked for', !!vertical);
  check('no native dialogs, page errors or horizontal overflow (desktop)', problems.length === 0, problems.join(' | '));
  await ctx.close();

  // 7. Light and phone.
  for (const [theme, mobile] of [['light', false], ['dark', true]]) {
    const tag = `${mobile ? 'phone' : 'desktop'}-${theme}`;
    const v = await newPage(browser, { theme, mobile, state });
    const p = await openPack(v.page, acme);
    await p.locator('[data-pack-role]').first().waitFor();
    await shot(v.page, `pack-${tag}`);
    await v.ctx.close();
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
