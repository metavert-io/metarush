#!/usr/bin/env node
// Remote access over Tailscale (D128, D129) in a real browser. Run it through
// scripts/remote-e2e.sh, which builds a throwaway installation, turns remote
// access on with the CLI and starts fake-serve (an HTTPS stand-in for
// `tailscale serve`). Chromium resolves the made-up tailnet name to this
// computer and trusts fake-serve's key, so the page is a real secure context.
//
// Which tailnet person a browser is: the X-Fake-Tailnet-User header, which
// fake-serve turns into Tailscale's identity headers (and removes), like
// serve does for people on the tailnet.
//
//   1. A listed account (Maya) opens the address and is signed in with no
//      link: a __Host- cookie, Secure, HttpOnly, SameSite=Strict; Settings says
//      who and from where; host-only actions say "on Sam's MacBook"; the folder
//      picker is never offered; the recorder records through HTTPS and the take
//      is hers; "Send to editing app" offers the folder as a download.
//   2. An account not on the list gets the plain page that says how to get in.
//   3. Headers pretending to be Tailscale's are refused: from a connection that
//      isn't loopback (a second MetaRush on fe80::1%lo0 behind a hop that keeps
//      the browser's headers) and on this computer's own address.
//   4. A computer without a listed account sees the plain page that says to
//      sign in to Tailscale with an account the owner let in (the sign-in
//      method is Tailscale accounts, D183); a one-time link made on this
//      computer but opened at the public address is refused (403), signs
//      nothing in, and is used up.
//   5. This computer's own browser still says "this computer".
//   6. A Claude Code conversation two people share (D179): Sam starts one in
//      the project; Maya finds it in the project's list, labelled "Started
//      by Sam Lee", with "Everyone / Just mine" (remembered across a
//      reload); she continues it, her message and the card it brings are
//      hers (her badge, her navigator), she decides it, and Sam's view says
//      who wrote and decided what.
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, request as pwRequest } from 'playwright';
import { useFakeMicByID } from './fake-mic.mjs';
import { checkPanel } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const need = ['REMOTE_URL', 'REMOTE_NAME', 'REMOTE_SPKI', 'REMOTE_LOCAL', 'REMOTE_PROJECT', 'REMOTE_MINT', 'REMOTE_SHOTS', 'REMOTE_FAKE_MIC'];
for (const k of need) {
  if (!process.env[k]) {
    console.error(`set ${need.join(', ')} (scripts/remote-e2e.sh does)`);
    process.exit(2);
  }
}
const URL0 = process.env.REMOTE_URL.replace(/\/$/, '');
const LOCAL = process.env.REMOTE_LOCAL.replace(/\/$/, '');
const SPOOF = (process.env.REMOTE_SPOOF ?? '').replace(/\/$/, '');
const SHOTS = process.env.REMOTE_SHOTS;
const MACHINE = "Sam's MacBook";
mkdirSync(SHOTS, { recursive: true });

const results = [];
const problems = [];
function check(name, ok, detail = '') {
  results.push({ name, ok: !!ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` - ${detail}` : ''}`);
}

function mint(cmd) {
  const out = execSync(cmd, { cwd: repo, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const m = out.match(/https?:\/\/\S+\/auth\?t=[a-f0-9]+/);
  if (!m) throw new Error(`no sign-in link in: ${out}`);
  return m[0];
}

const browser = await chromium.launch({
  args: [
    `--host-resolver-rules=MAP ${process.env.REMOTE_NAME} 127.0.0.1`,
    `--ignore-certificate-errors-spki-list=${process.env.REMOTE_SPKI}`,
    '--use-fake-ui-for-media-stream',
    '--use-fake-device-for-media-stream',
    `--use-file-for-fake-audio-capture=${process.env.REMOTE_FAKE_MIC}`,
    '--autoplay-policy=no-user-gesture-required',
  ],
});

async function newPage({ who, headers = {}, theme = 'dark' } = {}) {
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    colorScheme: theme,
    reducedMotion: 'reduce',
    permissions: ['microphone'],
    extraHTTPHeaders: { ...(who ? { 'X-Fake-Tailnet-User': who } : {}), ...headers },
  });
  await ctx.addInitScript((t) => {
    try {
      localStorage.setItem('metarush.theme', t);
    } catch {
      /* private mode */
    }
  }, theme);
  await useFakeMicByID(ctx);
  const page = await ctx.newPage();
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('dialog', (d) => {
    problems.push(`native ${d.type()} dialog: ${d.message()}`);
    void d.dismiss();
  });
  return { ctx, page };
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

const shot = (page, name) => page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: false });

async function until(what, f, { timeout = 60_000, every = 500 } = {}) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const v = await f();
    if (v) return v;
    await new Promise((r) => setTimeout(r, every));
  }
  throw new Error(`timed out waiting for ${what}`);
}

/** The Claude Code panel's visible text, checked like the panel run does (checkPanel). */
async function panelProblems(page) {
  const text = await page.evaluate(() => document.querySelector('aside[role="dialog"], [role="dialog"][aria-modal="true"]')?.innerText ?? '');
  return text.split('\n').flatMap((l) => checkPanel(l).map((f) => `${f.kind} ${JSON.stringify(f.text)} in ${JSON.stringify(l.trim().slice(0, 100))}`));
}

/** Opens the Claude Code panel's list of conversations. */
async function openList(page) {
  // The drawer remembers being open: after a reload it comes back by itself once
  // the page's first effects have run (restorePanel), a moment after the page has
  // loaded, so give it that moment before opening it (else the click lands on it).
  const drawer = page.locator('aside[role="dialog"]');
  await drawer.first().waitFor({ timeout: 5_000 }).catch(() => {});
  if (!(await drawer.count())) await page.getByRole('button', { name: /^Ask Claude Code/ }).click();
  const all = page.getByRole('button', { name: /^All conversations/ });
  await all.or(page.getByRole('button', { name: 'Back to the conversation' })).first().waitFor({ timeout: 30_000 });
  if (await all.count()) await all.click();
  await page.getByPlaceholder('Search conversations').waitFor({ timeout: 15_000 });
}

/** A conversation two people share (D179). */
async function shared() {
  const P = process.env.REMOTE_PROJECT;
  // Sam, at this computer, starts a conversation in the project.
  const sam = await newPage();
  await sam.page.goto(mint(process.env.REMOTE_MINT).replace(/^http:\/\/[^/]+/, LOCAL));
  await sam.page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
  const samId = (await sam.page.evaluate(async () => (await (await fetch('/api/session')).json()).data)).user.id;
  const asked = await api(sam.page, 'assistant.ask', { prompt: "Sam here: I'd like a slower opening", projectId: P });
  const convId = asked.conversation.id;
  const show = (page) => api(page, 'assistant.show', { id: convId, events: false });
  let cv = await until("Sam's card", async () => {
    const c = await show(sam.page);
    return c.state === 'idle' && c.actions?.length === 1 ? c : null;
  });
  check("Sam's conversation: his message, a card for him", cv.mine && cv.turns[0].user === 'Sam Lee' && cv.turns[0].userId === samId && cv.actions[0].forId === samId && cv.waitingForYou === 1,
    JSON.stringify({ mine: cv.mine, user: cv.turns[0].user, forName: cv.actions[0].forName, waitingForYou: cv.waitingForYou }));

  // Maya, signed in as herself, finds it in the project's list.
  const maya = await newPage({ who: 'maya' });
  const page = maya.page;
  await page.goto(`${URL0}/p/${P}`);
  await page.locator('[data-addbox]').first().or(page.getByRole('button', { name: /^Ask Claude Code/ })).first().waitFor({ timeout: 30_000 });
  const mayaId = (await page.evaluate(async () => (await (await fetch('/api/session')).json()).data)).user.id;
  await page.getByRole('button', { name: /^Ask Claude Code/ }).click();
  // The panel follows the project (D185): it opens the project's latest
  // conversation she can see — Sam's — rather than a list to pick from.
  const his = page.locator('[aria-label="Conversation"] [data-sender="Sam Lee"]');
  const opened = await his.first().waitFor({ timeout: 20_000 }).then(() => true).catch(() => false);
  check('the panel opens the project\'s conversation, Sam\'s, with his message under his name', opened);
  await openList(page);
  const row = page.locator('li', { has: page.locator('[data-started-by="Sam Lee"]') });
  await row.first().waitFor({ timeout: 20_000 });
  const rowText = (await row.first().innerText()).replace(/\s+/g, ' ');
  check("the project's list shows Sam's conversation, labelled with who started it", /Started by Sam Lee/.test(rowText) && /1 waiting for Sam Lee/.test(rowText), rowText);
  check('the list shows everyone\'s, with a "Just mine" choice', (await page.locator('[data-whose="everyone"]').count()) === 1 && (await page.getByRole('button', { name: 'Just mine' }).count()) === 1);
  check('the list says which project, with a way to every project', ((await page.locator('[data-list-scope="project"]').innerText()) ?? '').includes('Tailnet story'));
  check("someone else's row offers no quick archive", (await row.first().getByRole('button', { name: /^Archive/ }).count()) === 0);
  await shot(page, 'shared-list-maya');
  await page.getByRole('button', { name: 'Just mine' }).click();
  await page.getByText('None of yours yet', { exact: false }).waitFor({ timeout: 15_000 });
  check('"Just mine" leaves only hers (none yet), and says how to see the others', (await row.count()) === 0);
  await page.reload();
  await openList(page);
  await page.locator('[data-whose]').waitFor({ timeout: 15_000 });
  check('"Just mine" is remembered across a reload', (await page.locator('[data-whose="mine"]').count()) === 1, await page.locator('[data-whose]').getAttribute('data-whose'));
  await page.getByRole('button', { name: 'Everyone' }).click();
  await row.first().waitFor({ timeout: 15_000 });

  // She opens it: Sam's message says it's his, his card waits for him.
  await row.first().getByRole('button').first().click();
  await page.locator('[aria-label="Conversation"]').waitFor({ timeout: 30_000 });
  await page.locator('[data-sender="Sam Lee"]').first().waitFor({ timeout: 20_000 });
  check("his message has his picture beside it (his initials, he has none)", (await page.locator('[data-sender="Sam Lee"]').first().locator('[data-sender-avatar], [data-initials]').count()) === 1);
  const samCard = page.locator('article[aria-label^="Claude asks:"]', { hasText: 'A slower opening' });
  check("his card says whom it waits for", (await samCard.getByText('Waiting for Sam Lee').count()) === 1);
  check('nothing waits for her yet: no navigator, no badge', (await page.locator('[data-navigator]').count()) === 0 && (await page.locator('[data-assistant-waiting]').count()) === 0);
  await shot(page, 'shared-thread-maya-before');

  // She continues it: her message, and the card it brings, are hers.
  const box = page.getByLabel('Message to Claude');
  await box.click();
  await box.fill('Maya here: please keep the kitchen shot');
  await page.keyboard.press('ControlOrMeta+Enter');
  cv = await until('her card', async () => {
    const c = await show(page);
    return c.state === 'idle' && c.actions?.length === 2 ? c : null;
  });
  const hers = cv.actions.find((a) => a.turn === 2);
  check('her message is hers', cv.turns[1]?.user === 'Maya Lindqvist' && cv.turns[1]?.userId === mayaId, JSON.stringify(cv.turns[1] && { user: cv.turns[1].user, userId: cv.turns[1].userId === mayaId }));
  check('the card her message brought is for her', hers?.forId === mayaId && hers?.forName === 'Maya Lindqvist' && cv.waitingForYou === 1 && !cv.mine, JSON.stringify({ forName: hers?.forName, waitingForYou: cv.waitingForYou }));
  await page.locator('[data-sender="You"]').first().waitFor({ timeout: 20_000 });
  const navText = await page.locator('[data-navigator]').innerText().catch(() => '');
  check('her navigator counts only hers', /1 thing waits for you/.test(navText), navText.replace(/\s+/g, ' '));
  // The badge follows the status, which refreshes when the conversation changes.
  const badge = await until('her badge', async () => ((await page.locator('[data-assistant-waiting]').first().innerText().catch(() => '')).trim() === '1' ? '1' : null), {
    timeout: 70_000,
  }).catch(() => page.locator('[data-assistant-waiting]').first().innerText().catch(() => '(none)'));
  check('her top-bar badge counts only hers', badge.trim() === '1', badge);
  const samStatus = await sam.page.evaluate(async () => (await (await fetch('/api/assistant', { credentials: 'same-origin' })).json()).data);
  check("Sam's badge still counts only his", samStatus.waiting === 1, `${samStatus.waiting}`);

  // She decides her card.
  const herCard = page.locator('article[aria-label^="Claude asks:"]', { hasText: 'Keep the kitchen shot' });
  await herCard.getByRole('button', { name: 'Add note' }).click();
  cv = await until('her decision and Claude hearing it', async () => {
    const c = await show(page);
    const card = c.actions.find((a) => a.id === hers.id);
    return card?.state === 'done' && c.turns.length === 3 && c.state === 'idle' ? c : null;
  });
  const decided = cv.actions.find((a) => a.id === hers.id);
  check('the decision is hers', decided.by === 'Maya Lindqvist' && decided.byId === mayaId, JSON.stringify({ by: decided.by }));
  check('Claude heard it from her', cv.turns[2].note && cv.turns[2].userId === mayaId, JSON.stringify({ note: cv.turns[2].note, user: cv.turns[2].user }));
  const unplain = await panelProblems(page);
  check('her panel reads plainly (checkPanel)', unplain.length === 0, [...new Set(unplain)].slice(0, 6).join(' | '));
  await shot(page, 'shared-thread-maya');

  // Sam's view: who wrote and decided what.
  await sam.page.goto(`${LOCAL}/p/${P}?ask=${convId}`);
  await sam.page.locator('[aria-label="Conversation"]').waitFor({ timeout: 30_000 });
  await sam.page.locator('[data-sender="Maya Lindqvist"]').first().waitFor({ timeout: 20_000 });
  check('Sam sees his own message as "You" and hers with her name', (await sam.page.locator('[data-sender="You"]').count()) >= 1 && (await sam.page.locator('[data-sender="Maya Lindqvist"]').count()) === 1);
  const herCardForSam = sam.page.locator('article[aria-label^="Claude asks:"]', { hasText: 'Keep the kitchen shot' });
  check('Sam sees her decision as hers', (await herCardForSam.getByText(/^Done by Maya Lindqvist\b/).count()) === 1);
  const samNav = await sam.page.locator('[data-navigator]').innerText().catch(() => '');
  check("Sam's navigator still has his own card", /1 thing waits for you/.test(samNav), samNav.replace(/\s+/g, ' '));
  const samUnplain = await panelProblems(sam.page);
  check("Sam's panel reads plainly (checkPanel)", samUnplain.length === 0, [...new Set(samUnplain)].slice(0, 6).join(' | '));
  await shot(sam.page, 'shared-thread-sam');
  await sam.ctx.close();
  await maya.ctx.close();
}

async function main() {
  // 1. Maya: a listed Tailscale account.
  const { ctx, page } = await newPage({ who: 'maya' });
  const first = await page.goto(`${URL0}/`);
  check('the address opens over HTTPS', first?.status() === 200 && first.url().startsWith('https://'), first?.url());
  await page.locator('[data-addbox]').first().waitFor({ timeout: 30_000 });
  const sc = await page.evaluate(() => ({ secure: window.isSecureContext, mic: !!navigator.mediaDevices?.getUserMedia }));
  check('the page is a secure context (the microphone is offered)', sc.secure && sc.mic, JSON.stringify(sc));
  const cookies = await ctx.cookies(URL0);
  const c = cookies.find((x) => x.name === '__Host-mr_session');
  check('signed in by itself: a __Host- cookie, Secure, HttpOnly, SameSite=Strict', !!c && c.secure && c.httpOnly && c.sameSite === 'Strict' && c.path === '/', JSON.stringify(cookies.map((x) => [x.name, x.secure, x.httpOnly, x.sameSite])));
  const session = await page.evaluate(async () => (await (await fetch('/api/session')).json()).data);
  check('the session is Maya, from another computer, with Tailscale', session.user.name === 'Maya Lindqvist' && session.access.remote && session.access.signedInWith === 'tailscale' && session.access.machine === MACHINE, JSON.stringify(session.access));
  const choose = page.getByRole('button', { name: 'Choose a folder' }).first();
  const title = (await choose.count()) ? await choose.getAttribute('title') : null;
  check('the Add box sends a folder from this computer instead of linking one', title === null || /Send a folder's files from this computer/.test(title ?? ''), title ?? '(no title on Home: files are sent from the browser)');

  // D191: a member sees this computer's settings but can't change them…
  check("Maya's session: a member, Sam owns it", session.owner === false && (session.owners ?? []).includes('Sam Lee'), JSON.stringify({ owner: session.owner, owners: session.owners }));
  await page.goto(`${URL0}/settings/footage`);
  await page.locator('#watched-folder').waitFor({ timeout: 20_000 });
  const ownersLine = page.locator('[data-owners-line]').first();
  await ownersLine.waitFor({ timeout: 20_000 }).catch(() => undefined);
  const lineText = (await ownersLine.innerText().catch(() => '')).replace(/\s+/g, ' ');
  check('Settings → Footage for a member: "Only an owner can change this. Owner: Sam Lee."', lineText.includes('Only an owner can change this. Owner: Sam Lee.'), lineText);
  check('…and the watched folder can\'t be changed', await page.locator('#watched-folder').getByRole('button', { name: /Change…|Choose folder…/ }).first().isDisabled().catch(() => false));
  await shot(page, 'remote-settings-member');
  // …until Sam, at the host, makes her an owner: what follows is about where, not who.
  {
    const sam = await newPage();
    await sam.page.goto(mint(process.env.REMOTE_MINT).replace(/^http:\/\/[^/]+/, LOCAL));
    await sam.page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
    const people = (await api(sam.page, 'users.list')).people;
    const maya = people.find((p) => p.email === 'maya@example.com' || (p.logins ?? []).includes('maya@example.com'));
    check('Sam finds Maya among the people', !!maya, JSON.stringify(people.map((p) => [p.name, p.email])));
    if (maya) await api(sam.page, 'users.role', { user: maya.id, role: 'owner' });
    await sam.ctx.close();
  }

  // Settings → General (D184): who is signed in, and About.
  await page.goto(`${URL0}/settings`);
  await page.getByText(/Signed in as/).waitFor({ timeout: 20_000 });
  const signedIn = await page.getByText(/Signed in as/).locator('..').innerText();
  check('Settings: signed in as Maya, with Tailscale, from another computer', /Maya Lindqvist \(maya@example\.com\), with Tailscale, from another computer/.test(signedIn), signedIn.replace(/\s+/g, ' '));
  // Settings → General (About, D130): the version that answers is the host's, and says so.
  const about = page.locator('[data-about]');
  await about.locator('[data-about-version]').waitFor({ timeout: 20_000 });
  const aboutText = (await about.innerText()).replace(/\s+/g, ' ');
  check('About: the version runs on the host, named', /^About MetaRush .*MetaRush \d+\.\d+\.\d+/.test(aboutText) && aboutText.includes(`Running on ${MACHINE}, the computer this dashboard comes from.`), aboutText.slice(0, 160));
  check('About: the host is the computer that "runs this dashboard"', (await about.locator('[data-about-computers] li', { hasText: 'runs this dashboard' }).count()) === 1);
  await about.scrollIntoViewIfNeeded();
  await shot(page, 'remote-settings-about');

  // Settings → People → Remote access.
  await page.goto(`${URL0}/settings/people#remote-access`);
  await page.locator('[data-remote-access]').waitFor({ timeout: 20_000 });
  const card = page.locator('[data-remote-access]');
  await card.getByText(/People on your tailnet can open MetaRush on/).waitFor({ timeout: 20_000 });
  const cardText = await card.innerText();
  check('Remote access card: on, the address, the people', /People on your tailnet can open MetaRush on Sam's MacBook/.test(cardText) && cardText.includes(URL0) && /maya@example\.com/.test(cardText) && /sam@example\.com/.test(cardText), '');
  check('Remote access card: adding people only at the host', /Only someone at Sam's MacBook can let more people in/.test(cardText) && (await card.getByRole('button', { name: 'Add a person' }).count()) === 0);

  // Settings → Storage: this machine is the host.
  await page.goto(`${URL0}/settings/storage#machine`);
  await page.locator('#machine h2').waitFor({ timeout: 20_000 });
  const machineCard = (await page.locator('#machine h2').innerText()).replace(/\s+/g, ' ');
  check('the machine card names the host', machineCard.includes(MACHINE), machineCard);

  // Settings → Footage: the watched folder and the extension, on the host.
  await page.goto(`${URL0}/settings/footage`);
  await page.locator('#watched-folder').waitFor({ timeout: 20_000 });
  await page.locator('[data-host-action]').first().waitFor({ timeout: 20_000 }).catch(() => undefined);
  const hostActions = await page.locator('[data-host-action]').allInnerTexts();
  check('host-only actions are labelled "on Sam\'s MacBook"', hostActions.length > 0 && hostActions.every((t) => t.includes(`on ${MACHINE}`)), JSON.stringify(hostActions));
  const watchText = await page.locator('#watched-folder').innerText();
  check('the watched folder says where it is', watchText.includes(`on ${MACHINE}`), watchText.replace(/\s+/g, ' ').slice(0, 200));
  await shot(page, 'remote-settings-maya');
  // Change… opens the in-browser folder browser for the host (never the native picker).
  let pickerCalls = 0;
  page.on('request', (r) => {
    if (/\/api\/(watch\/choose|resources\/folders\/choose)/.test(r.url())) pickerCalls++;
  });
  await page.locator('#watched-folder').getByRole('button', { name: /Change…|Choose folder…/ }).click();
  const dlg = page.getByRole('dialog', { name: 'Watched folder' });
  await dlg.waitFor({ timeout: 10_000 });
  // The dialog opens before its contents arrive: read it once they have (or after 10 s).
  await dlg.getByText(`Browse folders on ${MACHINE}`).waitFor({ timeout: 10_000 }).catch(() => {});
  const dlgText = await dlg.innerText();
  check('the watched-folder dialog browses folders on the host', dlgText.includes(`Folder on ${MACHINE}`) && dlgText.includes(`Browse folders on ${MACHINE}`) && pickerCalls === 0, dlgText.replace(/\s+/g, ' ').slice(0, 160));
  await shot(page, 'remote-watch-dialog');
  await page.keyboard.press('Escape');
  // Settings → Browser extension (its own section, D191).
  await page.goto(`${URL0}/settings/extension`);
  const ext = page.locator('[data-extension-remote]');
  await ext.waitFor({ timeout: 20_000 }).catch(() => undefined);
  check('the browser extension is offered as a download with the tailnet address', (await ext.count()) === 1 && (await ext.innerText()).includes(URL0));

  // The recorder through the HTTPS address.
  const studio = `${URL0}/p/${process.env.REMOTE_PROJECT}`;
  await page.goto(`${studio}?tab=voiceover`);
  await page.getByRole('list', { name: 'Narration lines' }).waitFor({ timeout: 30_000 });
  await page.getByRole('button', { name: 'Record narration' }).click();
  const rec = page.getByRole('dialog', { name: 'Record narration' });
  await rec.getByRole('meter', { name: 'Microphone level' }).waitFor({ timeout: 20_000 });
  check('the microphone opens through the HTTPS address', true);
  await rec.getByRole('radio', { name: 'One line at a time' }).click();
  await rec.getByRole('button', { name: /^(Record line|Try again)/ }).click();
  await rec.getByRole('button', { name: /^Stop/ }).waitFor({ timeout: 15_000 });
  await page.waitForTimeout(3200);
  await shot(page, 'remote-recorder');
  await rec.getByRole('button', { name: /^Stop/ }).click();
  await rec.getByRole('button', { name: /^Keep it/ }).waitFor({ timeout: 20_000 });
  const saved = await rec
    .getByRole('list', { name: 'Takes recorded now' })
    .getByText('Saved')
    .first()
    .waitFor({ timeout: 60_000 })
    .then(() => true)
    .catch(() => false);
  check('the take is saved through the HTTPS address', saved);
  await rec.getByRole('button', { name: /^Keep it/ }).click();
  await rec.getByRole('button', { name: 'Close the recorder' }).click();
  await rec.waitFor({ state: 'detached', timeout: 60_000 });
  const proj = await api(page, 'project.show', { id: process.env.REMOTE_PROJECT });
  const narr = await api(page, 'voiceover.show', { revisionId: proj.headRevisionId });
  check('line 1 has the recorded take', narr.beats?.[0]?.take?.source === 'recorded', JSON.stringify(narr.beats?.map((b) => b.takeLabel)));
  const audit = await api(page, 'audit.list', { limit: 300 });
  const hers = audit.events.filter((e) => e.actor?.userName === 'Maya Lindqvist').map((e) => e.action);
  check('what she did is hers in the audit trail', hers.includes('remote.signin') && hers.some((a) => /voiceover|revision|edit/.test(a)), [...new Set(hers)].join(', '));

  // "Send to editing app": the folder is made on the host and offered as a download.
  await page.goto(`${studio}?tab=renders`);
  await page.getByRole('button', { name: 'Send to editing app' }).click();
  const send = page.getByRole('dialog', { name: 'Send to editing app' });
  await send.waitFor();
  check('Send to editing app says where the folder is made', (await send.innerText()).includes(`The folder is made on ${MACHINE}`));
  await send.getByRole('checkbox').uncheck();
  await send.getByRole('button', { name: /Make the timeline for/ }).click();
  await send.locator('[data-send-done]').waitFor({ timeout: 60_000 });
  const dl = send.locator('[data-export-download]');
  const href = (await dl.count()) ? await dl.getAttribute('href') : '';
  // Fetched by the page itself (Node can't resolve the made-up tailnet name; Chromium maps it).
  const zip = href
    ? await page.evaluate(async (h) => {
        const r = await fetch(h);
        const b = new Uint8Array(await r.arrayBuffer());
        return { status: r.status, type: r.headers.get('content-type'), disposition: r.headers.get('content-disposition'), size: b.length, magic: String.fromCharCode(b[0], b[1]) };
      }, href)
    : null;
  check('the export comes to this browser as a download (.zip)', !!zip && zip.status === 200 && zip.type === 'application/zip' && zip.magic === 'PK' && /attachment/.test(zip.disposition ?? ''), JSON.stringify(zip));
  check('"Show in Finder" says it opens on the host', (await send.getByRole('button', { name: `Show in Finder on ${MACHINE}` }).count()) === 1);
  await shot(page, 'remote-send-to-editor');
  await page.keyboard.press('Escape');
  await ctx.close();

  // 2. Bob: an account that isn't on the list.
  const bob = await newPage({ who: 'bob' });
  const r2 = await bob.page.goto(`${URL0}/`);
  const bobText = await bob.page.locator('body').innerText();
  check('an account not on the list is refused with a plain page', r2?.status() === 403 && /You can't open MetaRush here yet/.test(bobText) && bobText.includes('bob@example.com') && /Remote access/.test(bobText), bobText.replace(/\s+/g, ' ').slice(0, 220));
  await shot(bob.page, 'remote-refused-not-listed');
  // (The refusal page allows no requests of its own: its CSP is default-src 'none'.)
  const bobApi = await bob.page.goto(`${URL0}/api/session`);
  const bobJson = await bobApi?.json().catch(() => ({}));
  check('…and its requests are refused too', bobApi?.status() === 403 && bobJson?.error?.details?.reason === 'not_listed', `${bobApi?.status()} ${JSON.stringify(bobJson?.error?.details)}`);
  await bob.ctx.close();

  // 3. Spoofed headers.
  if (SPOOF) {
    const sp = await newPage({ headers: { 'Tailscale-User-Login': 'maya@example.com', 'Tailscale-User-Name': 'Maya Lindqvist' } });
    const r3 = await sp.page.goto(`${SPOOF}/`);
    const spText = await sp.page.locator('body').innerText();
    check('Tailscale headers from a connection that is not loopback are refused', r3?.status() === 403 && /MetaRush turned this visit away/.test(spText), spText.replace(/\s+/g, ' ').slice(0, 200));
    await shot(sp.page, 'remote-refused-spoofed');
    await sp.ctx.close();
  } else {
    console.log('skip non-loopback browser check: no fe80::1 on lo0 here');
  }
  const req = await pwRequest.newContext();
  const local = await req.get(`${LOCAL}/api/health`, { headers: { 'Tailscale-User-Login': 'maya@example.com' } });
  const localJson = await local.json().catch(() => ({}));
  check('Tailscale headers on this computer\'s own address are refused', local.status() === 403 && localJson?.error?.details?.reason === 'spoofed', `${local.status()} ${JSON.stringify(localJson?.error?.details)}`);
  await req.dispose();

  // 4. A computer without a listed account: the plain "sign in to Tailscale" page; a
  //    one-time link made here is refused at the public address (D183).
  const tag = await newPage({ theme: 'light' });
  await tag.page.goto(`${URL0}/`);
  const tsHeading = tag.page.getByRole('heading', { name: 'Sign in with your Tailscale account' });
  const sawPage = await tsHeading.waitFor({ timeout: 20_000 }).then(() => true).catch(() => false);
  const tagText = (await tag.page.locator('body').innerText()).replace(/\s+/g, ' ');
  check(
    'without a listed account: the plain page says to sign in to Tailscale with an account the owner let in',
    sawPage && /lets people in by their Tailscale account/.test(tagText) && /with the account the owner let in/.test(tagText),
    tagText.slice(0, 220),
  );
  check('…and names the computer', tagText.includes(`MetaRush on ${MACHINE} lets people in`), tagText.slice(0, 160));
  check('…and it offers no one-time link or terminal command', !/one-time|metarush dashboard/i.test(tagText));
  await shot(tag.page, 'remote-signin-needed');
  const localLink = mint(process.env.REMOTE_MINT);
  check('a one-time link is made for this computer only', localLink.startsWith(`${LOCAL}/auth?t=`), localLink.replace(/t=[a-f0-9]+/, 't=…'));
  const atPublic = localLink.replace(/^http:\/\/[^/]+/, URL0);
  const refused = await tag.page.goto(atPublic);
  const refusedText = (await tag.page.locator('body').innerText()).replace(/\s+/g, ' ');
  check('the same link opened at the public address is refused (403)', refused?.status() === 403 && /work only on the computer MetaRush runs on/.test(refusedText), `${refused?.status()} ${refusedText.slice(0, 160)}`);
  const tagCookies = (await tag.ctx.cookies(URL0)).filter((x) => /mr_session/.test(x.name));
  const tagSession = await tag.page.goto(`${URL0}/api/session`);
  const tagSessionJson = await tagSession?.json().catch(() => ({}));
  check('…and signs nothing in', tagCookies.length === 0 && tagSession?.status() === 401 && tagSessionJson?.error?.details?.reason === 'no_session', `${tagCookies.length} cookies; ${tagSession?.status()} ${JSON.stringify(tagSessionJson?.error?.details)}`);
  await tag.page.goto(`${URL0}/`);
  check('…the page still says to sign in to Tailscale', await tsHeading.waitFor({ timeout: 20_000 }).then(() => true).catch(() => false));
  await tag.ctx.close();
  // A link that reached another computer is used up: it doesn't work here afterwards either.
  const replay = await pwRequest.newContext({ maxRedirects: 0 });
  const again = await replay.get(localLink);
  check('…and the refused link is used up (it no longer works on this computer either)', again.status() === 401 && /already used or has expired/.test(await again.text()), `${again.status()}`);
  await replay.dispose();

  // 5. This computer's own browser.
  const me = await newPage();
  await me.page.goto(mint(process.env.REMOTE_MINT).replace(/^http:\/\/[^/]+/, LOCAL));
  await me.page.waitForURL((u) => !u.pathname.startsWith('/auth'), { timeout: 15_000 });
  await me.page.goto(`${LOCAL}/settings/people#remote-access`);
  await me.page.locator('[data-remote-access]').getByRole('button', { name: 'Add a person' }).waitFor({ timeout: 20_000 });
  await me.page.goto(`${LOCAL}/settings/footage`);
  await me.page.locator('[data-host-action]').first().waitFor({ timeout: 20_000 }).catch(() => undefined);
  const mine = await me.page.locator('[data-host-action]').allInnerTexts();
  check("this computer's browser says Open in Finder, and can add people", mine.length > 0 && mine.every((t) => !t.includes(' on ')), JSON.stringify(mine));
  await shot(me.page, 'remote-settings-local');
  await me.ctx.close();

  // 6. A conversation two people share (D179).
  await shared();

  for (const p of problems) console.log(`note: ${p}`);
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed${failed.length ? `; failed: ${failed.map((f) => f.name).join('; ')}` : ''}`);
  if (problems.some((p) => p.startsWith('native'))) {
    console.log('FAIL a native dialog appeared');
    process.exitCode = 1;
  }
  if (failed.length) process.exitCode = 1;
}

try {
  await main();
} catch (e) {
  console.error(e);
  process.exitCode = 1;
} finally {
  await browser.close();
}
