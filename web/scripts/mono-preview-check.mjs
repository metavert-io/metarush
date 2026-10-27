#!/usr/bin/env node
// Measures how loud a mono file plays, against the same sound as a stereo file,
// in the two places that play a plan (D64):
//   - the renderer: video/render/render.mjs renders a plan with the mono and the
//     stereo tone one after the other; ffmpeg measures each channel of the MP4;
//   - the browser preview: Chromium plays both files through the Web Audio API the
//     way the preview player does (a media element source into the 2-channel
//     destination) and measures each channel.
// The difference between the two is the preview's mono compensation
// (web/src/player/compensate.ts, MONO_PREVIEW_GAIN = 1/√2, −3.01 dB).
//
//   node web/scripts/mono-preview-check.mjs        (needs ffmpeg; uses port 4986)
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'mono-check-'));
const PORT = Number(process.env.MONO_CHECK_PORT ?? 4986);
const ff = (args) => execFileSync('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y', ...args], { stdio: ['ignore', 'ignore', 'inherit'] });

// The same quiet 1 kHz tone (ffmpeg's sine is 1/8 full scale, then 0.251): once as a mono file, once as a
// stereo one made by ffmpeg's own mono-to-stereo step (−3 dB per side, like the renderer's).
ff(['-f', 'lavfi', '-i', 'sine=frequency=1000:sample_rate=48000:duration=3', '-af', 'volume=0.251', '-ac', '1', '-c:a', 'pcm_s16le', path.join(dir, 'mono.wav')]);
ff(['-f', 'lavfi', '-i', 'sine=frequency=1000:sample_rate=48000:duration=3', '-af', 'volume=0.251', '-ac', '2', '-c:a', 'pcm_s16le', path.join(dir, 'stereo.wav')]);

const server = http.createServer((req, res) => {
  const name = path.basename(new URL(req.url, 'http://x').pathname);
  const file = path.join(dir, name);
  if (name === 'page.html') {
    res.writeHead(200, { 'Content-Type': 'text/html' }).end('<!doctype html><title>mono</title>');
    return;
  }
  if (!fs.existsSync(file)) {
    res.writeHead(404).end();
    return;
  }
  const size = fs.statSync(file).size;
  const m = /^bytes=(\d+)-(\d*)$/.exec(req.headers.range ?? '');
  const start = m ? Number(m[1]) : 0;
  const end = m && m[2] ? Number(m[2]) : size - 1;
  res.writeHead(m ? 206 : 200, {
    'Content-Type': 'audio/wav',
    'Accept-Ranges': 'bytes',
    'Access-Control-Allow-Origin': '*',
    'Content-Length': String(end - start + 1),
    ...(m ? { 'Content-Range': `bytes ${start}-${end}/${size}` } : {}),
  });
  fs.createReadStream(file, { start, end }).pipe(res);
});
await new Promise((r) => server.listen(PORT, '127.0.0.1', r));
const url = (n) => `http://127.0.0.1:${PORT}/${n}`;

/* ---------- the renderer ---------- */
const fps = 30;
const el = (id, asset, from) => ({ id, assetId: asset, role: 'voiceover', bus: 'voiceover', from, durationInFrames: 75, trimBefore: 0, gainDb: 0, fadeInFrames: 0, fadeOutFrames: 0, duckUnderDialogue: false, duckDb: 0, playbackRate: 1, gain: [{ f: 0, g: 1 }, { f: 74, g: 1 }] });
const plan = {
  schemaVersion: 1, revisionId: 'rev_mono_check', projectId: 'prj_mono_check', specHash: 'mono', media: 'original',
  width: 320, height: 180, fps, durationInFrames: 180, background: '#000000',
  brand: { primary: '#F2EDE4', accent: '#E0A458', text: '#F7F5F0', background: '#0B0B0E', font: 'Inter' },
  safeArea: { top: 0, bottom: 0, left: 0, right: 0 },
  clips: [], overlays: [], dialogueRanges: [],
  audio: [el('mono', 'ast_mono', 0), el('stereo', 'ast_stereo', 90)],
  sources: { ast_mono: url('mono.wav'), ast_stereo: url('stereo.wav') },
};
fs.writeFileSync(path.join(dir, 'props.json'), JSON.stringify({ plan }));
const out = path.join(dir, 'render.mp4');
// Not spawnSync: the renderer fetches the tones from the server in this process.
const r = await new Promise((resolve) => {
  const child = spawn(process.execPath, ['render/render.mjs', '--props', path.join(dir, 'props.json'), '--out', out], { cwd: path.join(repo, 'video') });
  let log = '';
  child.stdout.on('data', (d) => (log += d));
  child.stderr.on('data', (d) => (log += d));
  child.on('close', (status) => resolve({ status, log }));
});
if (r.status !== 0) {
  console.error(r.log.slice(-3000));
  throw new Error('render failed');
}
/** RMS (dBFS) of each channel of a file between two times. */
const rms = (file, from, to) => {
  const res = spawnSync('ffmpeg', ['-hide_banner', '-nostats', '-i', file, '-af', `atrim=${from}:${to},astats=metadata=0:reset=0`, '-f', 'null', '-'], { encoding: 'utf8' });
  const chans = [...res.stderr.matchAll(/Channel: (\d+)[\s\S]*?RMS level dB: (-?[\d.]+|-inf)/g)].map((m) => Number(m[2]));
  return chans;
};
const renderMono = rms(out, 0.5, 2.0);
const renderStereo = rms(out, 3.5, 5.0);

/* ---------- the browser ---------- */
const browser = await chromium.launch({ args: ['--autoplay-policy=no-user-gesture-required'] });
const page = await browser.newPage();
await page.goto(url('page.html'));
const measure = (src) =>
  page.evaluate(async (src) => {
    const ctx = new AudioContext({ sampleRate: 48000 });
    const a = new Audio();
    a.crossOrigin = 'anonymous';
    a.src = src;
    a.loop = true;
    const node = ctx.createMediaElementSource(a);
    // What the destination does: a 2-channel "speakers" input (mono is up-mixed here).
    const toStereo = new GainNode(ctx, { channelCount: 2, channelCountMode: 'explicit', channelInterpretation: 'speakers' });
    const split = ctx.createChannelSplitter(2);
    const an = [ctx.createAnalyser(), ctx.createAnalyser()];
    node.connect(toStereo).connect(split);
    split.connect(an[0], 0);
    split.connect(an[1], 1);
    const sink = new GainNode(ctx, { gain: 0 });
    toStereo.connect(sink).connect(ctx.destination);
    await ctx.resume();
    await a.play();
    await new Promise((r) => setTimeout(r, 400));
    const buf = new Float32Array(2048);
    const acc = [0, 0];
    let n = 0;
    for (let i = 0; i < 40; i++) {
      for (let c = 0; c < 2; c++) {
        an[c].getFloatTimeDomainData(buf);
        for (const v of buf) acc[c] += v * v;
      }
      n += buf.length;
      await new Promise((r) => setTimeout(r, 25));
    }
    a.pause();
    await ctx.close();
    return acc.map((s) => 10 * Math.log10(s / n));
  }, src);
const browserMono = await measure(url('mono.wav'));
const browserStereo = await measure(url('stereo.wav'));
await browser.close();
server.close();

const fmt = (xs) => xs.map((x) => `${x.toFixed(2)} dB`).join(' / ');
const avg = (xs) => xs.reduce((a, b) => a + b, 0) / xs.length;
const renderDiff = avg(renderMono) - avg(renderStereo);
const browserDiff = avg(browserMono) - avg(browserStereo);
console.log('per channel RMS (left / right)        mono file              stereo file');
console.log(`renderer (MP4, ffmpeg astats)         ${fmt(renderMono)}    ${fmt(renderStereo)}`);
console.log(`browser preview (Web Audio)           ${fmt(browserMono)}    ${fmt(browserStereo)}`);
console.log(`mono against stereo: renderer ${renderDiff.toFixed(2)} dB, browser ${browserDiff.toFixed(2)} dB`);
console.log(`the preview needs ${(renderDiff - browserDiff).toFixed(2)} dB on mono files (compensate.ts applies -3.01 dB)`);
const ok = Math.abs(renderDiff - browserDiff + 3.0103) < 0.5;
console.log(ok ? 'ok: the preview compensation matches the measurement' : 'FAIL: the compensation does not match the measurement');
fs.rmSync(dir, { recursive: true, force: true });
process.exitCode = ok ? 0 : 1;
