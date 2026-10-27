#!/usr/bin/env node
// Measures where the dashboard's preview plays two people's recordings of one
// conversation (D119), in Chromium, with the very player the dashboard uses
// (@remotion/player + @metarush/video's MetaRushEdit) and the plans Go
// resolved for the render self-test (video/render/fixtures/transitions.json):
// Ada's microphone starts with the call, Bo's 0.713 s in.
//
//   - Paused: after a seek, where each recording's sound sits (the media
//     element's currentTime) against where the plan puts it.
//   - Playing: both recordings sampled at the same moment, many times over a
//     few seconds, as moments of the conversation: how far apart they are.
//
// Plans: sync-frame (placed on frames, as cuts made before D119), sync-sample
// (placed to the sample) and sync-drift (Bo's clock 208.3 ppm fast; the
// preview resamples him in the browser). Prints a table and fails when the
// sample-aligned plans are not where they belong when paused.
//
//   node web/scripts/sync-preview-check.mjs      (needs ffmpeg; port SYNC_CHECK_PORT, default 4967)
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sync-preview-'));
const PORT = Number(process.env.SYNC_CHECK_PORT ?? 4967);
const fixtures = JSON.parse(fs.readFileSync(path.join(repo, 'video/render/fixtures/transitions.json'), 'utf8'));

// The two microphones, as the render self-test makes them.
const VOICES = {
  'voice-a.wav': '0.45*(2*random(0)-1)*lt(mod(t\\,0.37)\\,0.03)',
  'voice-b.wav': '0.45*(2*random(1)-1)*lt(mod(t+0.11\\,0.43)\\,0.04)',
};
for (const [name, expr] of Object.entries(VOICES)) {
  execFileSync('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y', '-f', 'lavfi', '-i', `aevalsrc=${expr}:s=48000:c=mono`, '-t', '8', '-c:a', 'pcm_s16le', path.join(dir, name)]);
}

// The page: the dashboard's player with one plan.
const entry = path.join(dir, 'entry.tsx');
fs.writeFileSync(
  entry,
  `import React from 'react';
import { createRoot } from 'react-dom/client';
import { Player } from '@remotion/player';
import { MetaRushEdit, planMetadata } from '@metarush/video/lib';
const plan = (window as any).__plan;
const meta = planMetadata(plan);
const App = () => {
  const ref = React.useRef<any>(null);
  React.useEffect(() => { (window as any).__player = ref.current; }, []);
  return <Player ref={ref} component={MetaRushEdit} inputProps={{ plan }} durationInFrames={meta.durationInFrames} fps={meta.fps}
    compositionWidth={meta.compositionWidth} compositionHeight={meta.compositionHeight} style={{ width: 640, height: 360 }} />;
};
createRoot(document.getElementById('root')!).render(<App />);
`,
);
await build({
  entryPoints: [entry],
  bundle: true,
  format: 'esm',
  platform: 'browser',
  outfile: path.join(dir, 'page.js'),
  logLevel: 'error',
  jsx: 'automatic',
  loader: { '.woff2': 'dataurl', '.woff': 'dataurl', '.ttf': 'dataurl', '.png': 'dataurl', '.svg': 'dataurl' },
  define: { 'process.env.NODE_ENV': '"production"' },
  nodePaths: [path.join(repo, 'node_modules')],
});

const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://x');
  const name = path.basename(u.pathname);
  if (name === 'page.html') {
    const plan = u.searchParams.get('plan');
    res.writeHead(200, { 'Content-Type': 'text/html' }).end(
      `<!doctype html><meta charset="utf-8"><title>sync</title><div id="root"></div><script>window.__plan=${JSON.stringify(planFor(plan))}</script><script type="module" src="page.js"></script>`,
    );
    return;
  }
  const file = path.join(dir, name);
  if (!fs.existsSync(file)) {
    res.writeHead(404).end();
    return;
  }
  const size = fs.statSync(file).size;
  const m = /^bytes=(\d+)-(\d*)$/.exec(req.headers.range ?? '');
  const start = m ? Number(m[1]) : 0;
  const end = m && m[2] ? Number(m[2]) : size - 1;
  res.writeHead(m ? 206 : 200, {
    'Content-Type': name.endsWith('.js') ? 'text/javascript' : 'audio/wav',
    'Accept-Ranges': 'bytes',
    'Content-Length': String(end - start + 1),
    ...(m ? { 'Content-Range': `bytes ${start}-${end}/${size}` } : {}),
  });
  fs.createReadStream(file, { start, end }).pipe(res);
});
await new Promise((r) => server.listen(PORT, '127.0.0.1', r));
const base = `http://127.0.0.1:${PORT}`;

const planFor = (name) => {
  const plan = structuredClone(fixtures.plans[`${name}-preview`]);
  plan.sources = { 'voice-a': `${base}/voice-a.wav`, 'voice-b': `${base}/voice-b.wav` };
  return plan;
};

// Where a member's recording should be at timeline frame f (seconds into it).
const expected = (a, f, fps) => (a.trimBefore + (a.trimFraction ?? 0) + (f - a.from) * (a.playbackRate || 1)) / fps;
// The conversation's moment a member's recording position is (Ada starts at 0, Bo at 0.713 s).
const clockOf = (member, t, rate) => (member === 'bo' ? 0.713 + t / rate : t);

const browser = await chromium.launch({ args: ['--autoplay-policy=no-user-gesture-required'] });
const rows = [];
let failures = 0;
try {
  for (const name of ['sync-frame', 'sync-sample', 'sync-drift']) {
    const plan = planFor(name);
    const fps = plan.fps;
    const page = await browser.newPage();
    await page.goto(`${base}/page.html?plan=${name}`);
    await page.waitForFunction(() => !!window.__player, null, { timeout: 30000 });
    const els = Object.fromEntries(plan.audio.map((a) => [a.member, a]));
    const srcOf = { ada: 'voice-a.wav', bo: 'voice-b.wav' };
    const read = () =>
      page.evaluate(() =>
        [...document.querySelectorAll('audio')].map((el) => ({ src: el.currentSrc || el.src, t: el.currentTime, paused: el.paused, ready: el.readyState })),
      );
    // Paused, at a few frames: each recording where the plan puts it.
    let paused = 0;
    for (const f of [45, 90, 140]) {
      await page.evaluate((frame) => window.__player.seekTo(frame), f);
      await page.waitForTimeout(700);
      for (const el of await read()) {
        const who = Object.keys(srcOf).find((k) => el.src.endsWith(srcOf[k]));
        if (!who || el.ready < 1) continue;
        const e = el.t - expected(els[who], f, fps);
        paused = Math.max(paused, who === 'bo' ? Math.abs(e) : 0);
      }
    }
    // The planned position itself, against the pin: where Bo's recording is at frame 45.
    const bo = els.bo;
    const rate = bo.playbackRate || 1;
    const pinErr = clockOf('bo', expected(bo, 45, fps), rate) - 45 / fps;
    // Playing: both sampled at once, as moments of the conversation.
    await page.evaluate((frame) => window.__player.seekTo(frame), 30);
    await page.waitForTimeout(500);
    await page.evaluate(() => window.__player.play());
    await page.waitForTimeout(1200);
    const gaps = [];
    for (let i = 0; i < 25; i++) {
      const now = await read();
      const ada = now.find((e) => e.src.endsWith('voice-a.wav') && !e.paused);
      const b = now.find((e) => e.src.endsWith('voice-b.wav') && !e.paused);
      if (ada && b) gaps.push(clockOf('bo', b.t, rate) - clockOf('ada', ada.t, 1));
      await page.waitForTimeout(80);
    }
    await page.evaluate(() => window.__player.pause());
    gaps.sort((x, y) => x - y);
    const med = gaps.length ? gaps[Math.floor(gaps.length / 2)] : NaN;
    const spread = gaps.length ? gaps[gaps.length - 1] - gaps[0] : NaN;
    rows.push({ name, pinErr, paused, med, spread, n: gaps.length });
    await page.close();
  }
} finally {
  await browser.close();
  server.close();
  fs.rmSync(dir, { recursive: true, force: true });
}

const ms = (v) => `${(v * 1e3).toFixed(3)} ms`;
console.log('plan          planned vs pin   paused: seek error   playing: Bo − Ada (median, spread, samples)');
for (const r of rows) {
  console.log(`${r.name.padEnd(13)} ${ms(r.pinErr).padStart(12)}   ${ms(r.paused).padStart(14)}       ${ms(r.med).padStart(10)}, ${ms(r.spread)}, ${r.n}`);
}
for (const r of rows) {
  if (r.name !== 'sync-frame' && Math.abs(r.pinErr) > 1e-6) {
    console.error(`FAIL ${r.name}: the preview's plan puts Bo ${ms(r.pinErr)} from his pin`);
    failures++;
  }
  if (r.name !== 'sync-frame' && r.paused > 1e-5) {
    console.error(`FAIL ${r.name}: paused, Bo's recording sits ${ms(r.paused)} from where the plan puts it`);
    failures++;
  }
}
process.exit(failures ? 1 : 0);
