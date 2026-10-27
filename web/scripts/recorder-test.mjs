#!/usr/bin/env node
// The screen recorder's logic (web/src/screens/record/): the clock that lines
// the tracks up (when the microphone's first sample was heard, its drift, the
// samples a pause drops), the queue that sends each track's pieces at the
// host's offset (a piece cut off on its way, a dropped connection, a reload),
// the teleprompter's lines from a pasted script, and the file format chosen.
//
//   node --experimental-strip-types web/scripts/recorder-test.mjs
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const src = (f) => path.join(here, '..', 'src', 'screens', 'record', f);
const { fitMic, VoiceGate, pausesOnClock, recordedAt, voiceStart, toPCM16, DRIFT_MIN_MS } = await import(src('clock.ts'));
const { TrackQueue, OffsetMismatch, Refused, BACKOFF } = await import(src('pieces.ts'));
const { splitScript, sentences, estimateSeconds, promptLines } = await import(src('script.ts'));
const { pickFormat, videoBits, formatOf } = await import(src('formats.ts'));
const { withDeadline, NoAnswer, DEVICE_DEADLINE_MS, armedBy } = await import(path.join(here, '..', 'src', 'lib', 'deadline.ts'));

let failed = 0;
let checked = 0;
const eq = (name, got, want) => {
  checked++;
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    failed++;
    console.error(`FAIL ${name}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
};
const near = (name, got, want, tol) => {
  checked++;
  if (!(Math.abs(got - want) <= tol)) {
    failed++;
    console.error(`FAIL ${name}: got ${got}, want ${want} ± ${tol}`);
  }
};

// ----- the clock -----
// A microphone at 48 kHz whose first sample was heard at 1000.0 ms, delivering
// 128-sample blocks, each arriving 2–12 ms after its last sample (a busy page
// only delays), its clock running `ppm` fast against the page's.
function arrivals({ first = 1000, rate = 48000, seconds = 10, ppm = 0, seed = 7 }) {
  let s = seed;
  const rnd = () => ((s = (s * 1103515245 + 12345) % 2147483648) / 2147483648);
  const out = [];
  const actual = rate * (1 + ppm / 1e6);
  for (let n = 128; n <= rate * seconds; n += 128) {
    const heard = first + (n / actual) * 1000;
    out.push([heard + 2 + rnd() * 10 + (rnd() < 0.05 ? 40 : 0), n]);
  }
  return out;
}
{
  const fit = fitMic(arrivals({}), 48000);
  near('sample 0 heard at 1000 ms (within 3 ms of the fastest arrival delay)', fit.first, 1002, 3);
  eq('a short take has no drift measured', fit.ppm, 0);
  near('the spread is a few ms', fit.spread, 4, 4);
  eq('no arrivals: no fit', fitMic([], 48000), undefined);
}
{
  const fit = fitMic(arrivals({ seconds: 120, ppm: 40 }), 48000);
  near('a microphone 40 ppm fast over two minutes', fit.ppm, 40, 8);
  near('its first sample still at 1000 ms', fit.first, 1002, 3);
  const slow = fitMic(arrivals({ seconds: 120, ppm: -25, seed: 3 }), 48000);
  near('a microphone 25 ppm slow', slow.ppm, -25, 8);
  eq('drift needs a minute of sound', DRIFT_MIN_MS, 60000);
}
{
  // The voice's place: the recorders started at 980 ms, its first sample at 1002.
  eq('the voice starts 0.022 s after the recorders', voiceStart(980, { first: 1002, ppm: 0, spread: 1 }), 0.022);
  eq('without a fit the voice starts with them', voiceStart(980, undefined), 0);
}
{
  // Pauses: 10 s of samples at 1 kHz heard from 0 ms; paused from 2000 to 5000 ms.
  const g = new VoiceGate(0, 1000);
  g.pause(2000);
  eq('while paused', g.paused, true);
  eq('samples after the pause starts are dropped', g.keep(1500, 1000), [[0, 500]]);
  g.resume(5000);
  eq('resumed', g.paused, false);
  eq('a block across the pause keeps both ends', g.keep(1000, 5000), [[0, 1000], [4000, 5000]]);
  eq('a block inside the pause keeps nothing', g.keep(2500, 1000), []);
  eq('after it, everything', g.keep(6000, 128), [[0, 128]]);
  g.pause(8000);
  g.resume(8500);
  eq('two pauses', g.keep(0, 10000), [[0, 2000], [5000, 8000], [8500, 10000]]);
  eq('the pauses on the recording clock (pauses left out)', pausesOnClock(0, [{ from: 2000, to: 5000 }, { from: 8000, to: 8500 }]), [
    { at: 2, length: 3 },
    { at: 5, length: 0.5 },
  ]);
  eq('recorded so far leaves pauses out', recordedAt(0, [{ from: 2000, to: 5000 }, { from: 8000 }], 9000), 5);
}
{
  const pcm = toPCM16(new Float32Array([0, 1, -1, 0.5, 2]), [[1, 3], [4, 5]]);
  eq('16-bit samples, only the kept parts', Array.from(new Int16Array(pcm.buffer)), [32767, -32768, 32767]);
}

// ----- the piece queue -----
const bytes = (n, from = 0) => new Blob([Uint8Array.from({ length: n }, (_, i) => (from + i) % 251)]);
const read = async (b) => Array.from(new Uint8Array(await b.arrayBuffer()));
function host({ dropAt = new Set(), cutAt = new Set(), refuseAt = -1 } = {}) {
  const h = { data: [], calls: 0, sends: [] };
  h.sender = {
    head: async () => h.data.length,
    send: async (offset, blob) => {
      h.calls++;
      h.sends.push(offset);
      if (offset === refuseAt) throw new Refused('This recording has ended.');
      if (dropAt.has(h.calls)) throw new Error('The connection to MetaRush dropped.');
      if (offset !== h.data.length) throw new OffsetMismatch(h.data.length);
      let b = await read(blob);
      if (cutAt.has(h.calls)) b = b.slice(0, Math.floor(b.length / 2)); // cut off on its way: half arrived
      h.data.push(...b);
      if (cutAt.has(h.calls)) throw new Error('A piece of the recording was cut off on its way.');
      return h.data.length;
    },
  };
  return h;
}
const noWait = () => Promise.resolve();
{
  const h = host();
  const kept = new Map();
  const store = { put: async (o, b) => void kept.set(o, b), drop: async (upTo) => { for (const [o, b] of kept) if (o + b.size <= upTo) kept.delete(o); } };
  const q = new TrackQueue(h.sender, store, noWait);
  for (let i = 0; i < 5; i++) await q.add(bytes(100, i * 100));
  await q.drained();
  eq('every piece arrives, in order', h.data, (await read(bytes(500))));
  eq('sent = recorded', [q.state.sent, q.state.bytes], [500, 500]);
  eq('pieces the host has are forgotten in browser storage', kept.size, 0);
}
{
  // The 2nd send is cut off half way, the 4th drops: the queue carries on from the host's offset.
  const h = host({ cutAt: new Set([2]), dropAt: new Set([4]) });
  const states = [];
  const q = new TrackQueue(h.sender, undefined, noWait);
  q.subscribe((s) => s.problem && states.push(s.problem));
  for (let i = 0; i < 4; i++) await q.add(bytes(64, i * 64));
  await q.drained();
  eq('a cut-off piece and a dropped connection lose nothing', h.data, await read(bytes(256)));
  eq('it said what was wrong while retrying', states.includes('The connection to MetaRush dropped.'), true);
  eq('the problem clears once sending works', q.state.problem, null);
  eq('a cut-off piece is sent again from the host offset (half of the second piece)', h.sends.includes(96), true);
}
{
  // After a reload: the host already has 150 of the 300 bytes kept in browser storage.
  const h = host();
  h.data.push(...(await read(bytes(150))));
  const q = new TrackQueue(h.sender, undefined, noWait);
  q.restore([{ offset: 0, blob: bytes(100) }, { offset: 100, blob: bytes(100, 100) }, { offset: 200, blob: bytes(100, 200) }]);
  await q.drained();
  eq('a reload carries on where the host stopped', h.data, await read(bytes(300)));
  eq('the first send learnt the offset from the host', h.sends[0], 0);
}
{
  const h = host({ refuseAt: 100 });
  const q = new TrackQueue(h.sender, undefined, noWait);
  await q.add(bytes(100));
  await q.add(bytes(100, 100));
  let refused = '';
  await q.drained().catch((e) => (refused = e instanceof Refused ? 'refused' : String(e)));
  eq('a refusal for good stops the queue', [refused, q.state.refused, q.state.problem], ['refused', true, 'This recording has ended.']);
}
{
  // Backoff waits grow; Try again wakes the queue at once.
  const waits = [];
  let fail = 3;
  const sender = { head: async () => 0, send: async (o, b) => { if (fail-- > 0) throw new Error('offline'); return o + b.size; } };
  const q = new TrackQueue(sender, undefined, (ms) => (waits.push(ms), Promise.resolve()));
  await q.add(bytes(10));
  await q.drained();
  eq('waits grow after each failure', waits, BACKOFF.slice(0, 3));
}

// ----- the teleprompter's lines -----
eq('paragraphs are lines', splitScript('First idea here.\nStill the first.\n\nSecond idea.\n\n# a note\nThird.'), ['First idea here. Still the first.', 'Second idea.', 'Third.']);
eq('rows of sentences are lines', splitScript('Open the dashboard.\nClick Record.\nPick a window.'), ['Open the dashboard.', 'Click Record.', 'Pick a window.']);
eq('a wrapped paragraph becomes its sentences', splitScript('This is a long paragraph that\nwraps at a width. It has two\nsentences in it.'), ['This is a long paragraph that wraps at a width.', 'It has two sentences in it.']);
eq('abbreviations and initials keep a sentence whole', sentences('Dr. Smith met J. R. Doe at 5 p.m. today. Then 3 more came.'), ['Dr. Smith met J. R. Doe at 5 p.m. today.', 'Then 3 more came.']);
eq('nothing: no lines', splitScript('  \n# only a note\n'), []);
near('ten words at 155 a minute', estimateSeconds('one two three four five six seven eight nine ten'), 3.871, 0.001);
near('a pause inside the line adds a quarter second', estimateSeconds('Hello there. How are you?'), 5 * 60 / 155 + 0.25, 0.001);
{
  const pl = promptLines(['(warmly) Welcome to the demo.', '', 'Click Record.']);
  eq('lines numbered, how to say it kept apart', pl.map((l) => [l.id, l.n, l.text, l.delivery ?? null, l.pinned]), [
    ['line-1', 1, 'Welcome to the demo.', 'warmly', false],
    ['line-2', 2, 'Click Record.', null, false],
  ]);
  eq('a short line still gets time to read it', pl[1].dur, 0.8);
}

// ----- formats -----
eq('H.264 in MP4 first where the browser records it', pickFormat((t) => t.startsWith('video/mp4') || t.startsWith('video/webm'), true), 'video/mp4;codecs=avc1.64001F,mp4a.40.2');
eq('WebM with VP9 and Opus otherwise (Firefox)', pickFormat((t) => t.startsWith('video/webm'), true), 'video/webm;codecs=vp9,opus');
eq('picture only: no sound in the type', pickFormat((t) => t === 'video/webm;codecs=vp8', false), 'video/webm;codecs=vp8');
eq('nothing known: let the browser choose', pickFormat(() => false, true), '');
eq('a browser that throws is skipped', pickFormat((t) => { if (t.includes('mp4')) throw new Error('no'); return t === 'video/webm'; }, true), 'video/webm');
eq('1080p at 30 fps: about 6 Mbit/s', videoBits(1920, 1080, 30), 6220800);
eq('small pictures get at least 1.5 Mbit/s', videoBits(320, 180, 15), 1500000);
eq('a 5K screen at 60 is capped at 12 Mbit/s', videoBits(5120, 2880, 60), 12000000);
eq('the format as kept', formatOf('video/webm; codecs="vp8, opus"'), 'video/webm;codecs="vp8,opus"');

// ----- a device that never answers -----
{
  const never = new Promise(() => {});
  const t = Date.now();
  let err;
  await withDeadline(never, 40, 'microphone').catch((e) => (err = e));
  eq('a microphone that never answers is given up on', [err instanceof NoAnswer, err?.device, err?.message], [true, 'microphone', "The microphone didn't answer."]);
  near('after the deadline', Date.now() - t, 40, 200);
  eq('the recorders wait about nine seconds', DEVICE_DEADLINE_MS, 9000);
  // One that answers late is closed at once.
  let resolveLate;
  const late = new Promise((r) => (resolveLate = r));
  let closed = null;
  await withDeadline(late, 10, 'camera', (v) => (closed = v)).catch(() => undefined);
  resolveLate('stream');
  await new Promise((r) => setTimeout(r, 5));
  eq('a device that answers late is closed', closed, 'stream');
  eq('one that answers in time is used', await withDeadline(Promise.resolve('mic'), 1000, 'microphone'), 'mic');
  let refused;
  await withDeadline(Promise.reject(new Error('NotAllowedError')), 1000, 'camera').catch((e) => (refused = e.message));
  eq('a refusal is passed on as it is', refused, 'NotAllowedError');
}
// ----- the browser's permission question isn't the device's time -----
{
  const after = (ms) => new Promise((r) => setTimeout(r, ms));
  // The person answers the browser's question at 80 ms; the device answers 30 ms after: in time (deadline 50 ms).
  const t0 = Date.now();
  const v = await withDeadline(after(110).then(() => 'stream'), 50, 'microphone', undefined, after(80));
  eq('a device opened after a slow permission answer is used', v, 'stream');
  near('the deadline counted from the answer', Date.now() - t0, 110, 60);
  // Allowed at 60 ms and never answering: given up 50 ms after the answer, not 50 ms after asking.
  const t1 = Date.now();
  let err;
  await withDeadline(new Promise(() => {}), 50, 'camera', undefined, after(60)).catch((e) => (err = e));
  eq('a device that never answers after the person allowed it is given up on', err instanceof NoAnswer, true);
  near('50 ms after the answer', Date.now() - t1, 110, 80);
  // What the browser says decides when the deadline starts.
  eq('allowed already: at once', (await Promise.race([armedBy({ state: 'granted', addEventListener() {} }).then(() => 'armed'), after(20).then(() => 'not')])), 'armed');
  eq('blocked: no deadline (the browser refuses at once)', armedBy({ state: 'denied', addEventListener() {} }), null);
  eq("a browser that can't say: no deadline this time", armedBy(null), null);
  const listeners = [];
  const status = { state: 'prompt', addEventListener: (_t, fn) => listeners.push(fn) };
  const armed = armedBy(status);
  eq('asking: not yet', await Promise.race([armed.then(() => 'armed'), after(20).then(() => 'not')]), 'not');
  status.state = 'granted';
  listeners.forEach((fn) => fn());
  eq('once allowed: armed', await Promise.race([armed.then(() => 'armed'), after(20).then(() => 'not')]), 'armed');
}

console.log(`recorder-test: ${checked - failed}/${checked} checks passed`);
if (failed) process.exit(1);
