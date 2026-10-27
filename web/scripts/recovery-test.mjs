#!/usr/bin/env node
// Players that keep going through a short outage (web/src/player/recovery.ts,
// D180): what the server's answer to one byte means, the pauses between
// tries, when no progress is a stall, the way back (ask, say, wait, try,
// healed), and which files a preview asks about.
//
//   node --experimental-strip-types web/scripts/recovery-test.mjs
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const R = await import(path.join(here, '..', 'src', 'player', 'recovery.ts'));

let failed = 0;
let checked = 0;
const eq = (name, got, want) => {
  checked++;
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    failed++;
    console.error(`FAIL ${name}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
};
const until = async (ok, what) => {
  for (let i = 0; i < 200 && !ok(); i++) await new Promise((r) => setTimeout(r, 0));
  if (!ok()) {
    failed++;
    console.error(`FAIL ${what}: never happened`);
  }
};

// The lines people read.
eq('database line', R.troubleLine({ kind: 'database' }), "MetaRush can't reach its database right now; it keeps trying.");
eq('reconnecting line', R.troubleLine({ kind: 'reconnecting' }), 'Reconnecting…');
eq('failed line: the server\'s words', R.troubleLine({ kind: 'failed', message: "MetaRush can't find that clip." }), "MetaRush can't find that clip.");

// Growing pauses, the last one repeating, ±15 %.
eq('pauses', [0, 1, 2, 3, 4, 5, 9].map((n) => R.retryDelay(n, () => 0.5)), [1000, 2000, 4000, 8000, 15000, 15000, 15000]);
eq('shortest and longest of the first pause', [R.retryDelay(0, () => 0), R.retryDelay(0, () => 1)], [850, 1150]);

// What one byte of the file says.
const json = (status, code, message) =>
  new Response(JSON.stringify({ ok: false, error: { code, message } }), { status, headers: { 'Content-Type': 'application/json' } });
const seen = [];
const answering = (res) => async (url, init) => {
  seen.push({ url, range: init.headers.Range, redirect: init.redirect, credentials: init.credentials });
  if (res instanceof Error) throw res;
  return typeof res === 'function' ? res() : res;
};
const probe = (res) => R.probeMedia('/media/assets/ast_1/proxy', undefined, answering(res));
eq('a part of the file', await probe(new Response('x', { status: 206 })), { kind: 'ok' });
eq('one byte, same origin, redirects not followed', seen[0], { url: '/media/assets/ast_1/proxy', range: 'bytes=0-0', redirect: 'manual', credentials: 'same-origin' });
eq('a redirect to the media store', await probe({ ok: false, type: 'opaqueredirect', status: 0, headers: new Headers(), body: null }), { kind: 'ok' });
eq('the database is down', await probe(json(503, 'E_DATABASE', "MetaRush can't reach its database right now, so it can't start playing this yet.")), { kind: 'database' });
eq('the clip is gone', await probe(json(404, 'E_NOT_FOUND', "MetaRush can't find that clip.")), { kind: 'gone', message: "MetaRush can't find that clip." });
eq('signed out', await probe(json(401, 'E_UNAUTHORIZED', 'This browser is not signed in to MetaRush.')), { kind: 'gone', message: 'This browser is not signed in to MetaRush.' });
eq('not ready yet: worth trying again', await probe(json(409, 'E_CONFLICT', '“Harbour walk” isn\'t ready to play yet.')), { kind: 'trouble', message: '“Harbour walk” isn\'t ready to play yet.' });
eq('storage trouble: worth trying again', (await probe(json(502, 'E_STORAGE', "MetaRush couldn't read the file from its storage."))).kind, 'trouble');
eq('the dev server answering for MetaRush', await probe(new Response('Bad Gateway', { status: 502 })), { kind: 'offline' });
eq('MetaRush not answering at all', await probe(new TypeError('Failed to fetch')), { kind: 'offline' });
{
  const ctrl = new AbortController();
  ctrl.abort();
  let threw = false;
  try {
    await R.probeMedia('/m', ctrl.signal, async () => {
      throw new DOMException('Aborted', 'AbortError');
    });
  } catch {
    threw = true;
  }
  eq('a probe that was called off throws', threw, true);
}
{
  const by = { '/a': new Response('x', { status: 206 }), '/b': json(503, 'E_DATABASE', 'down'), '/c': new TypeError('x') };
  const fetcher = async (url) => {
    const r = by[url];
    if (r instanceof Error) throw r;
    return r;
  };
  eq('the worst answer counts', await R.probeAll(['/a', '/b', '/c'], undefined, fetcher), { kind: 'database' });
  eq('nothing to ask about', await R.probeAll([], undefined, fetcher), { kind: 'ok' });
}

// A stall: playing, with no new position and no new bytes for STALL_MS.
{
  let t = 0;
  const w = R.stallWatch(() => t, 6000);
  w.position(1);
  t = 5999;
  eq('5.999 s without progress is not a stall', w.stalled(true), false);
  t = 6000;
  eq('6 s without progress while playing is a stall', w.stalled(true), true);
  eq('paused is never a stall', w.stalled(false), false);
  w.data();
  t = 9000;
  eq('bytes still arriving: slow, not stalled', w.stalled(true), false);
  w.position(2);
  t = 14999;
  eq('moving again', w.stalled(true), false);
  t = 15000;
  eq('stopped again', w.stalled(true), true);
}

// The way back.
const run = (answers, opts = {}) => {
  const log = { shown: [], waits: [], probes: 0, retries: 0, idles: 0 };
  const rec = new R.Recovery({
    probe: async () => {
      const a = answers[Math.min(log.probes, answers.length - 1)];
      log.probes++;
      return a;
    },
    retry: () => log.retries++,
    show: (t) => log.shown.push(t ? R.troubleLine(t) : null),
    idle: () => log.idles++,
    wait: async (ms) => {
      log.waits.push(ms);
    },
    random: () => 0.5,
    failedLine: opts.failedLine,
  });
  return { rec, log };
};
{
  const { rec, log } = run([{ kind: 'database' }, { kind: 'database' }, { kind: 'ok' }]);
  rec.begin('stall');
  await until(() => log.retries === 1, 'a try once the server answers');
  eq('says reconnecting, then what is wrong', log.shown, ['Reconnecting…', R.DATABASE_LINE, R.DATABASE_LINE]);
  eq('waits 1 s, then 2 s', log.waits, [1000, 2000]);
  eq('stops asking the server while it waits (the preview comes down)', log.idles, 2);
  eq('trying', [rec.active, rec.retrying], [true, true]);
  rec.begin('stall'); // a second notice while trying again: the try didn't work
  await until(() => log.retries === 2, 'another try');
  eq('the next pause is longer', log.waits, [1000, 2000, 4000]);
  rec.healed();
  eq('playing again: the line goes', log.shown.at(-1), null);
  eq('done', [rec.active, rec.retrying, rec.failed], [false, false, false]);
  rec.begin('error', 2);
  await until(() => log.retries === 3, 'a new outage starts over');
  eq('a new outage waits from the start', log.waits.length, 3);
}
{
  const { rec, log } = run([{ kind: 'offline' }, { kind: 'ok' }]);
  rec.begin('error', 2);
  await until(() => log.retries === 1, 'MetaRush back');
  eq('MetaRush itself not answering: reconnecting', log.shown, ['Reconnecting…', 'Reconnecting…']);
}
{
  const { rec, log } = run([{ kind: 'gone', message: "MetaRush can't find that clip." }]);
  rec.begin('error', 4);
  await until(() => rec.failed, 'giving up on a gone clip');
  eq('a gone clip: said once, never tried again', [log.shown, log.retries, log.probes], [['Reconnecting…', "MetaRush can't find that clip."], 0, 1]);
  rec.begin('stall');
  eq('nothing more after giving up', log.probes, 1);
}
{
  const { rec, log } = run([{ kind: 'ok' }], { failedLine: 'The preview copy could not be loaded.' });
  rec.begin('error', 4);
  await until(() => log.retries === 1, 'first try');
  rec.begin('error', 4); // the server sends it, but it still won't play
  await until(() => log.retries === 2, 'second try');
  rec.begin('error', 4);
  await until(() => rec.failed, 'giving up on a file that won\'t play here');
  eq('a file that won\'t play here: said plainly after two tries', log.shown.at(-1), 'The preview copy could not be loaded.');
}
{
  const { rec, log } = run([{ kind: 'database' }]);
  rec.begin('stall');
  rec.begin('error', 2);
  rec.begin('stall');
  await until(() => log.probes >= 1, 'first question');
  eq('more trouble while it asks is the same trouble', log.shown[0], 'Reconnecting…');
  rec.dispose();
  const n = log.probes;
  await new Promise((r) => setTimeout(r, 5));
  eq('a player that went away stops asking', log.probes <= n + 1, true);
}

{
  // Playing again by itself (the stalled request came after all) while a try
  // waits: the pending try doesn't run, so it isn't sent back to where it stalled.
  let open;
  const gate = new Promise((r) => (open = r));
  const log = { probes: 0, retries: 0, shown: [] };
  const rec = new R.Recovery({
    probe: async () => {
      log.probes++;
      return { kind: 'database' };
    },
    retry: () => log.retries++,
    show: (t) => log.shown.push(t ? R.troubleLine(t) : null),
    wait: () => gate,
    random: () => 0.5,
  });
  rec.begin('stall');
  await until(() => log.probes === 1, 'asked once');
  rec.healed();
  open();
  await new Promise((r) => setTimeout(r, 5));
  eq('healed by itself while waiting: no more asking, no try', [log.probes, log.retries, log.shown.at(-1)], [1, 0, null]);
}

// Which files a preview asks about around a frame.
{
  const plan = {
    fps: 30,
    sources: { a: '/media/assets/a/proxy', b: '/media/assets/b/proxy', m: '/media/assets/m/original?preview=1', v: '/media/assets/v/original?preview=1' },
    clips: [
      { assetId: 'a', from: 0, durationInFrames: 90 },
      { assetId: 'b', from: 90, durationInFrames: 90 },
    ],
    audio: [{ assetId: 'm', from: 0, durationInFrames: 400, variant: { assetId: 'v' } }],
  };
  const base = 'http://127.0.0.1:4841/p/x';
  eq('at the start: the first clip and the music', R.sourcesNear(plan, 0, undefined, base), ['/media/assets/a/proxy', '/media/assets/m/original?preview=1', '/media/assets/v/original?preview=1']);
  eq('near the cut: both clips', R.sourcesNear(plan, 80, undefined, base).slice(0, 2), ['/media/assets/a/proxy', '/media/assets/b/proxy']);
  eq('the failing file first, without its #t= part', R.sourcesNear(plan, 150, 'http://127.0.0.1:4841/media/assets/b/proxy#t=3,6', base)[0], '/media/assets/b/proxy');
  eq('nothing there: every file', R.sourcesNear({ ...plan, clips: [], audio: [] }, 5000, undefined, base).length, 4);
  eq('same file, written two ways', R.mediaPath('http://127.0.0.1:4841/media/renders/r1#t=1', base), R.mediaPath('/media/renders/r1', base));
}

console.log(`recovery: ${checked - failed} of ${checked} checks pass`);
if (failed) process.exit(1);
