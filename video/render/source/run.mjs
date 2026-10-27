#!/usr/bin/env node
// Runs a Remotion source's code in the render jail (D133, D134): reads its
// compositions, draws stills and low-resolution frame passes, and renders a
// composition, with the source's OWN Remotion packages (its lockfile's
// @remotion/bundler and @remotion/renderer, from the environment MetaRush
// installed with install scripts off) and MetaRush's bundler settings.
//
//   node render/source/run.mjs <task.json>
//   node render/source/run.mjs setup --env <dir>     (outside the jail: the headless browser, versions)
//
// The project's own Node code never runs: remotion.config.ts is not loaded,
// no webpack override of the project's is used, and nothing from package.json
// scripts runs. Pictures a person put in place of the project's own (D209) are
// served with the bundle, by their hash (serve-pictures.mjs). Its browser code runs only in Chrome behind the jail (the
// refusing proxy, host-resolver rules, the page's Content-Security-Policy and
// the Node download guard); a browser that can't be jailed stops the run.
//
// stdout: JSON lines only (versions, bundle, compositions, still, frames,
// progress, rendered, blocked, done | error). Logs: stderr.
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { format } from 'node:util';
import { RENDER_CSP, chromeWasJailed, guardDownloads, jailChrome, killChromes, reservePort, startRefusingProxy } from '../motion/jail.mjs';
import { placePictures } from './serve-pictures.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const FONTS_LOADER = path.join(HERE, 'fonts-loader.cjs');
const PROBE_DIR = path.join(HERE, 'probe');
const PROBE_FOLDER = '.metarush-probe';
const FONT_DIR = '__metarush_fonts';
const BUNDLE_FORMAT = 'metarush-source-bundle/1';

// stdout carries JSON lines only.
const toStderr = (...args) => process.stderr.write(`${format(...args)}\n`);
console.log = toStderr;
console.info = toStderr;
console.warn = toStderr;
console.debug = toStderr;
console.error = toStderr;
const emit = (o) => process.stdout.write(`${JSON.stringify(o)}\n`);
const log = (...a) => toStderr('[metarush-source]', ...a);
const fail = (e) => {
  emit({ type: 'error', message: e instanceof Error ? e.message : String(e), code: e?.code });
  if (e instanceof Error && e.stack) log(e.stack);
  killChromes();
  process.exit(1);
};

/** The source's own Remotion packages. */
const loadRemotion = (envDir) => {
  const req = createRequire(path.join(envDir, 'package.json'));
  const version = req('remotion/package.json').version;
  const bundler = req('@remotion/bundler');
  const renderer = req('@remotion/renderer');
  const bv = req('@remotion/bundler/package.json').version;
  const rv = req('@remotion/renderer/package.json').version;
  if (bv !== version || rv !== version) throw new Error(`the project's Remotion packages don't match (remotion ${version}, bundler ${bv}, renderer ${rv})`);
  return { version, bundler, renderer };
};

/** The page's Content-Security-Policy and a reporter of what it blocked. */
const injectPolicy = (dir) => {
  const p = path.join(dir, 'index.html');
  const html = fs.readFileSync(p, 'utf8');
  if (!/<head[^>]*>/i.test(html)) throw new Error('the bundle has no page head to put the security policy in');
  const meta = `<meta http-equiv="Content-Security-Policy" content="${RENDER_CSP}"><meta http-equiv="x-dns-prefetch-control" content="off">` +
    `<script>document.addEventListener('securitypolicyviolation',function(e){console.error('[metarush] blocked: '+e.violatedDirective+' '+(e.blockedURI||''))});</script>`;
  fs.writeFileSync(p, html.replace(/<head[^>]*>/i, (m) => m + meta));
};

/** Builds (or reuses) the bundle of one version with MetaRush's settings. */
const getBundle = async ({ bundler, b, onProgress }) => {
  const final = path.resolve(b.bundle);
  const marker = path.join(final, '.metarush-bundle.json');
  if (fs.existsSync(path.join(final, 'index.html')) && fs.existsSync(marker)) {
    const now = new Date();
    try {
      fs.utimesSync(final, now, now);
    } catch {
      // read-only: fine
    }
    return { serveUrl: final, cached: true };
  }
  const work = path.resolve(b.work);
  const tmp = `${final}.tmp-${process.pid}-${crypto.randomBytes(4).toString('hex')}`;
  const t0 = Date.now();
  const rules = [{ test: /[\\/]@remotion[\\/]google-fonts[\\/]dist[\\/]/, use: [{ loader: FONTS_LOADER }] }];
  if (b.probe) rules.push(probeRule(work));
  await bundler.bundle({
    entryPoint: path.join(work, b.entry),
    rootDir: work,
    outDir: tmp,
    publicDir: path.join(work, 'public'),
    symlinkPublicDir: process.platform !== 'win32' && fs.existsSync(path.join(work, 'public')),
    enableCaching: false,
    ignoreRegisterRootWarning: false,
    onProgress: (p) => onProgress?.(p / 100),
    // MetaRush's settings, never the project's: only the font loader is added
    // (and, for a map, the probe around compositions and scenes).
    webpackOverride: (config) => ({
      ...config,
      module: {
        ...config.module,
        rules: [...(config.module?.rules ?? []), ...rules],
      },
    }),
  });
  injectPolicy(tmp);
  // Google Fonts the project loads, from MetaRush's copies.
  for (const [rel, file] of Object.entries(b.fonts ?? {})) {
    if (!/^[A-Za-z0-9._/-]+$/.test(rel) || rel.includes('..')) continue;
    const dst = path.join(tmp, FONT_DIR, rel);
    fs.mkdirSync(path.dirname(dst), { recursive: true });
    fs.copyFileSync(file, dst);
  }
  fs.writeFileSync(path.join(tmp, '.metarush-bundle.json'), JSON.stringify({ format: BUNDLE_FORMAT, at: new Date().toISOString() }));
  try {
    fs.renameSync(tmp, final);
  } catch (e) {
    fs.rmSync(tmp, { recursive: true, force: true });
    if (!fs.existsSync(path.join(final, 'index.html'))) throw e;
  }
  log(`bundled ${b.name ?? ''} in ${((Date.now() - t0) / 1000).toFixed(1)}s`);
  return { serveUrl: final, cached: false };
};

/**
 * The map's probe (D167): MetaRush's probe files go next to the project's
 * files (so their imports find the project's own React and Remotion), and the
 * project's files — only those, never Remotion's own packages — get the probe's
 * "remotion" and "@remotion/transitions" when they import them.
 */
const probeRule = (work) => {
  const dir = path.join(work, PROBE_FOLDER);
  fs.mkdirSync(dir, { recursive: true });
  for (const f of fs.readdirSync(PROBE_DIR)) {
    if (!f.endsWith('.mjs')) continue;
    const dst = path.join(dir, f);
    const tmp = `${dst}.${process.pid}`;
    fs.copyFileSync(path.join(PROBE_DIR, f), tmp);
    fs.renameSync(tmp, dst);
  }
  // The key replaces Remotion's own "remotion" entry (its bundler points the
  // name at its ESM build; "remotion/no-react" and friends keep their entries).
  const alias = { remotion: path.join(dir, 'remotion.mjs') };
  if (fs.existsSync(path.join(work, 'node_modules', '@remotion', 'transitions', 'package.json'))) alias['@remotion/transitions$'] = path.join(dir, 'transitions.mjs');
  else fs.rmSync(path.join(dir, 'transitions.mjs'), { force: true });
  return { test: /\.(tsx?|jsx?|mjs|cjs)$/, exclude: [/[\\/]node_modules[\\/]/, /[\\/]\.metarush-probe[\\/]/], resolve: { alias } };
};

/** The probe's lines, by kind (scene, text), from the browser's console. */
const probeLines = { scene: [], text: [] };
const takeProbe = (text) => {
  const m = /^\[metarush-(scene|text)\] (.*)$/s.exec(text ?? '');
  if (!m) return false;
  try {
    probeLines[m[1]].push(JSON.parse(m[2]));
  } catch {
    // not ours
  }
  return true;
};

/** A sound's file in the project's public folder, from the address the page used. */
const publicPath = (src) => {
  let p = String(src ?? '');
  try {
    p = new URL(p, 'http://x').pathname;
  } catch {
    return '';
  }
  p = decodeURIComponent(p).replace(/^\/+/, '');
  const m = /^(?:static-[^/]+|public)\/(.+)$/.exec(p);
  return m ? m[1] : '';
};

/**
 * Where each sound plays (D170): Remotion's own record of the sounds on every
 * frame (src, the frame of the media, volume, speed), folded into placements
 * the way its renderer folds them (one per element id while it plays without
 * a gap).
 */
const soundPlacements = (frames, fps) => {
  const srcOf = new Map();
  for (const f of frames) for (const a of f.audioAndVideoAssets ?? []) if (!String(a.src).startsWith('same-as')) srcOf.set(`same-as-${a.id}-${a.frame}`, a.src);
  const out = [];
  const open = new Map();
  let prevFrame = null;
  for (const f of frames) {
    if (prevFrame !== null && f.frame !== prevFrame + 1) open.clear();
    prevFrame = f.frame;
    const seen = new Set();
    for (const a of f.audioAndVideoAssets ?? []) {
      if (a.type !== 'audio' && a.type !== 'video') continue;
      if (seen.has(a.id)) continue;
      seen.add(a.id);
      let cur = open.get(a.id);
      if (!cur || cur.last !== f.frame - 1) {
        let src = String(a.src);
        if (src.startsWith('same-as')) src = srcOf.get(src) ?? src;
        cur = { id: a.id, type: a.type, src, file: publicPath(src), from: f.frame, frames: 0, mediaFrame: a.mediaFrame, audioStartFrame: a.audioStartFrame ?? 0, playbackRate: a.playbackRate ?? 1, volumes: [], last: f.frame - 1 };
        out.push(cur);
        open.set(a.id, cur);
      }
      cur.frames++;
      cur.last = f.frame;
      cur.volumes.push(Math.round(Number(a.volume) * 10000) / 10000);
    }
  }
  return out.map(({ last, ...x }) => {
    const v = x.volumes;
    const same = v.every((y) => y === v[0]);
    // Seconds into the file where it starts (Remotion's own arithmetic).
    const start = x.audioStartFrame / fps + ((x.mediaFrame - x.audioStartFrame) / fps) * x.playbackRate;
    return { ...x, start: Math.round(start * 1000) / 1000, volumes: same ? [v[0] ?? 1] : v };
  });
};

const setup = async (envDir) => {
  process.chdir(envDir);
  const { version, renderer } = loadRemotion(envDir);
  emit({ type: 'versions', remotion: version });
  let downloaded = false;
  await renderer.ensureBrowser({
    logLevel: 'warn',
    onBrowserDownload: () => {
      downloaded = true;
      log('Chrome Headless Shell missing; downloading (one time) ...');
      return { version: null, onProgress: ({ percent }) => emit({ type: 'progress', stage: 'browser', progress: Math.round(percent * 1000) / 1000 }) };
    },
  });
  emit({ type: 'done', downloaded });
};

const main = async () => {
  const args = process.argv.slice(2);
  if (args[0] === 'setup') {
    const i = args.indexOf('--env');
    if (i < 0 || !args[i + 1]) throw new Error('usage: run.mjs setup --env <dir>');
    return setup(path.resolve(args[i + 1]));
  }
  if (args.length !== 1) throw new Error('usage: run.mjs <task.json> | setup --env <dir>');
  const task = JSON.parse(fs.readFileSync(path.resolve(args[0]), 'utf8'));
  const envDir = path.resolve(task.env);
  const { version, bundler, renderer } = loadRemotion(envDir);
  emit({ type: 'versions', remotion: version });
  const started = Date.now();
  const maxMs = (task.maxSeconds ?? 600) * 1000;
  const total = setInterval(() => {
    if (Date.now() - started > maxMs) {
      emit({ type: 'error', code: 'time_limit', message: `it ran past its time limit (${Math.round(maxMs / 1000)} s) and was stopped` });
      killChromes();
      process.exit(1);
    }
  }, 500);
  total.unref();

  // 1. Bundles (outside the browser; the bundler parses the code as text).
  const bundles = {};
  for (const b of task.bundles ?? []) {
    emit({ type: 'progress', stage: 'bundling', bundle: b.name });
    bundles[b.name] = await getBundle({ bundler, b });
    // Pictures a person put in place of the project's own (D209), by their hash.
    placePictures(bundles[b.name].serveUrl, b.pictures);
    emit({ type: 'bundle', name: b.name, cached: bundles[b.name].cached });
  }

  // 2. The jail around the browser.
  const { port, release } = await reservePort();
  const loopback = [`localhost:${port}`, `127.0.0.1:${port}`];
  const proxy = await startRefusingProxy();
  jailChrome({ proxyPort: proxy.port, bypass: loopback });
  const blockedNode = new Set();
  guardDownloads(loopback, (h) => blockedNode.add(h));
  const blockedPage = new Set();
  const onBrowserLog = (l) => {
    if (takeProbe(l.text)) return;
    const m = /\[metarush\] blocked: (\S+) (\S*)/.exec(l.text ?? '');
    if (m) {
      // Only addresses count as reaching out; the policy also refuses eval and
      // inline code (blockedURI "eval", "inline"), which isn't the network.
      let host = '';
      try {
        const u = new URL(m[2]);
        if (/^(https?|wss?|ftp):$/.test(u.protocol)) host = u.host;
      } catch {
        // not an address
      }
      if (host) blockedPage.add(host);
      else log(`[browser] the page's policy refused ${m[1]} ${m[2] || ''}`.trim());
      return;
    }
    if (l.type === 'error') log(`[browser ${l.type}] ${String(l.text).slice(0, 500)}`);
  };
  process.chdir(envDir); // Remotion finds its browser next to the nearest package.json
  const chromiumOptions = { gl: process.env.METARUSH_REMOTION_GL || (process.platform === 'linux' ? 'swangle' : 'angle'), enableMultiProcessOnLinux: true };
  const browser = await renderer.openBrowser('chrome', { chromiumOptions, logLevel: 'warn' });
  if (!chromeWasJailed()) {
    await browser.close({ silent: true }).catch(() => {});
    throw new Error("MetaRush couldn't lock the browser off the network, so it won't run this project's code. Update MetaRush (Remotion changed how it starts Chrome).");
  }
  await release(); // Remotion binds it from here on
  const blocked = () => [...new Set([...proxy.tried, ...blockedNode, ...blockedPage])].sort();
  const common = { puppeteerInstance: browser, port, logLevel: 'warn', onBrowserLog, chromiumOptions, timeoutInMilliseconds: task.frameTimeoutMs ?? 30000 };
  const closeAll = async () => {
    await release();
    await browser.close({ silent: true }).catch(() => {});
    killChromes();
    await proxy.close().catch(() => {});
  };

  try {
    for (const t of task.tasks ?? []) {
      const b = bundles[t.bundle ?? 'main'];
      if (!b) throw new Error(`no bundle named ${t.bundle}`);
      const inputProps = t.props ?? {};
      const select = (id) => renderer.selectComposition({ serveUrl: b.serveUrl, id, inputProps, ...common });
      if (t.type === 'compositions') {
        const comps = await renderer.getCompositions(b.serveUrl, { inputProps, ...common });
        emit({
          type: 'compositions',
          bundle: t.bundle ?? 'main',
          compositions: comps.map((c) => {
            let props = '';
            try {
              const s = JSON.stringify(c.defaultProps ?? {});
              props = s.length <= 20000 ? s : '';
            } catch {
              props = '';
            }
            return { id: c.id, width: c.width, height: c.height, fps: c.fps, durationInFrames: c.durationInFrames, defaultProps: props };
          }),
        });
      } else if (t.type === 'posters') {
        // One small picture per composition (its middle frame), for the dashboard.
        const comps = await renderer.getCompositions(b.serveUrl, { inputProps, ...common });
        fs.mkdirSync(t.outDir, { recursive: true });
        for (const comp of comps.slice(0, t.max ?? 24)) {
          const frame = Math.max(0, Math.floor((comp.durationInFrames - 1) / 2));
          const out = path.join(t.outDir, `${comp.id.replace(/[^A-Za-z0-9_-]/g, '_')}.jpg`);
          // About t.width pixels wide (never larger than the composition), else a fixed scale.
          const scale = t.width ? Math.round(Math.max(0.1, Math.min(1, t.width / comp.width)) * 1000) / 1000 : (t.scale ?? 0.25);
          try {
            await renderer.renderStill({ composition: comp, serveUrl: b.serveUrl, inputProps, frame, output: out, imageFormat: 'jpeg', jpegQuality: 80, scale, overwrite: true, ...common });
            emit({ type: 'still', bundle: t.bundle ?? 'main', composition: comp.id, frame, out, width: Math.round(comp.width * scale), height: Math.round(comp.height * scale), poster: true });
          } catch (e) {
            log(`poster of ${comp.id} failed: ${e.message}`);
          }
        }
      } else if (t.type === 'stills') {
        const comps = new Map();
        // With the probe asked to read the words, each still says what is on screen and whether it fits.
        const envVariables = t.probe ? { METARUSH_PROBE: t.probe } : {};
        for (const s of t.items ?? []) {
          if (!comps.has(s.composition)) comps.set(s.composition, await select(s.composition));
          const comp = comps.get(s.composition);
          const frame = Math.max(0, Math.min(comp.durationInFrames - 1, s.frame));
          fs.mkdirSync(path.dirname(s.out), { recursive: true });
          const t0 = Date.now();
          probeLines.text.length = 0;
          await renderer.renderStill({ composition: comp, serveUrl: b.serveUrl, inputProps, frame, output: s.out, imageFormat: s.format ?? 'jpeg', jpegQuality: 88, scale: s.scale ?? 1, overwrite: true, envVariables, ...common });
          const text = t.probe === 'text' ? (probeLines.text.find((x) => x.frame === frame) ?? probeLines.text[probeLines.text.length - 1] ?? null) : undefined;
          emit({ type: 'still', bundle: t.bundle ?? 'main', composition: s.composition, frame, out: s.out, ms: Date.now() - t0, width: Math.round(comp.width * (s.scale ?? 1)), height: Math.round(comp.height * (s.scale ?? 1)), text });
        }
      } else if (t.type === 'scenes') {
        // Every frame evaluated, none drawn (D167, D170): the probe says where each
        // scene is, and Remotion's own record of the sounds says where each sound plays.
        const comp = await select(t.composition);
        probeLines.scene.length = 0;
        const res = await renderer.renderFrames({
          composition: comp,
          serveUrl: b.serveUrl,
          inputProps,
          outputDir: null,
          imageFormat: 'none',
          concurrency: task.concurrency ?? null,
          envVariables: { METARUSH_PROBE: 'scenes' },
          onStart: () => {},
          onFrameUpdate: (n) => {
            if (n % 30 === 0) emit({ type: 'progress', stage: 'scenes', progress: Math.round((n / Math.max(1, comp.durationInFrames)) * 1000) / 1000 });
          },
          ...common,
        });
        const frames = [...(res.assetsInfo?.assets ?? [])].sort((x, y) => x.frame - y.frame);
        const seen = new Set();
        const scenes = [];
        for (const sc of probeLines.scene) {
          if (seen.has(sc.key)) continue;
          seen.add(sc.key);
          scenes.push(sc);
        }
        emit({ type: 'scenes', bundle: t.bundle ?? 'main', composition: t.composition, fps: comp.fps, durationInFrames: comp.durationInFrames, width: comp.width, height: comp.height, scenes, sounds: soundPlacements(frames, comp.fps) });
      } else if (t.type === 'frames') {
        const comp = await select(t.composition);
        fs.mkdirSync(t.outDir, { recursive: true });
        const res = await renderer.renderFrames({
          composition: comp,
          serveUrl: b.serveUrl,
          inputProps,
          outputDir: t.outDir,
          imageFormat: 'jpeg',
          jpegQuality: 70,
          everyNthFrame: Math.max(1, t.every ?? Math.round(comp.fps)),
          scale: t.scale ?? 0.2,
          concurrency: task.concurrency ?? null,
          onStart: () => {},
          onFrameUpdate: () => {},
          imageSequencePattern: 'frame-[frame].[ext]',
          ...common,
        });
        emit({ type: 'frames', bundle: t.bundle ?? 'main', composition: t.composition, outDir: t.outDir, every: Math.max(1, t.every ?? Math.round(comp.fps)), fps: comp.fps, durationInFrames: comp.durationInFrames, count: res.frameCount ?? null });
      } else if (t.type === 'render') {
        const comp = await select(t.composition);
        const range = Array.isArray(t.frames) && t.frames.length === 2 ? [Math.max(0, t.frames[0]), Math.min(comp.durationInFrames - 1, t.frames[1])] : null;
        // Its props can change its length (calculateMetadata): the cap holds for what is actually rendered.
        const frames = range ? range[1] - range[0] + 1 : comp.durationInFrames;
        if (t.maxLength && frames / comp.fps > t.maxLength + 1e-6) {
          throw new Error(`with these props it is ${Math.round(frames / comp.fps)} seconds long; MetaRush makes clips of up to ${Math.round(t.maxLength / 60)} minutes (render a part of it)`);
        }
        let last = -1;
        let lastTick = Date.now();
        const stuck = setInterval(() => {
          if (Date.now() - lastTick > (task.frameTimeoutMs ?? 30000) + 15000) {
            emit({ type: 'error', code: 'frame_time_limit', message: 'no frame was finished for a long time (is something stuck?)' });
            killChromes();
            process.exit(1);
          }
        }, 1000);
        stuck.unref();
        await renderer.renderMedia({
          composition: comp,
          serveUrl: b.serveUrl,
          inputProps,
          codec: 'h264',
          audioCodec: 'aac',
          pixelFormat: 'yuv420p',
          crf: t.crf ?? 18,
          imageFormat: task.imageFormat ?? 'jpeg',
          jpegQuality: task.jpegQuality ?? 90,
          outputLocation: t.out,
          overwrite: true,
          // Sound in layers (D170): the picture alone; its sounds become the cut's own tracks.
          muted: !!t.muted,
          enforceAudioTrack: !t.muted,
          concurrency: task.concurrency ?? null,
          frameRange: range,
          hardwareAcceleration: 'disable',
          ...common,
          onProgress: ({ progress, renderedFrames }) => {
            if (renderedFrames !== last) {
              last = renderedFrames;
              lastTick = Date.now();
            }
            emit({ type: 'progress', stage: 'rendering', progress: Math.round(progress * 1000) / 1000, renderedFrames });
          },
        });
        clearInterval(stuck);
        emit({ type: 'rendered', composition: t.composition, out: t.out, width: comp.width, height: comp.height, fps: comp.fps, durationInFrames: frames });
      } else {
        throw new Error(`unknown task ${t.type}`);
      }
    }
  } catch (e) {
    const bl = blocked();
    if (bl.length) emit({ type: 'blocked', hosts: bl });
    await closeAll();
    throw e;
  }
  const bl = blocked();
  if (bl.length) emit({ type: 'blocked', hosts: bl });
  await closeAll();
  clearInterval(total);
  emit({ type: 'done', seconds: Math.round((Date.now() - started) / 100) / 10 });
};

process.on('exit', () => killChromes());
main().then(
  () => process.exit(0),
  (e) => fail(e),
);
