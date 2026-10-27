// Shared plumbing for the MetaRush render CLIs (render.mjs, still.mjs).
// Contract: stdout carries JSON lines ONLY; every log goes to stderr.
import crypto from 'node:crypto';
import fs from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { format } from 'node:util';
import { RENDER_CSP } from './motion/jail.mjs';

export const VIDEO_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
export const COMPOSITION_ID = 'MetaRushEdit';
const require = createRequire(import.meta.url);

// --- stdout discipline -------------------------------------------------------
// Remotion and its dependencies log through console.*; route all of it to
// stderr so the Go worker can parse stdout line by line.
const toStderr = (...args) => process.stderr.write(`${format(...args)}\n`);
console.log = toStderr;
console.info = toStderr;
console.warn = toStderr;
console.debug = toStderr;
console.error = toStderr;

export const emit = (obj) => {
  process.stdout.write(`${JSON.stringify(obj)}\n`);
};

export const log = (...args) => toStderr('[metarush-render]', ...args);

export class UsageError extends Error {}

/** Emits {"type":"error"} and exits 1. Never throws. */
export const fail = (err) => {
  const message = err instanceof Error ? err.message : String(err);
  if (err instanceof Error && err.stack && !(err instanceof UsageError)) log(err.stack);
  emit({ type: 'error', message });
  process.exit(1);
};

// --- versions ----------------------------------------------------------------
export const remotionVersion = () => require('remotion/package.json').version;

/** The generated template catalog (schema/templates.json, from the Go catalog). */
export const templateCatalogPath = () => require.resolve('@metarush/schema/schemas/templates.json');

export const templateManifest = () => JSON.parse(fs.readFileSync(templateCatalogPath(), 'utf8')).templates;

export const versionsLine = () => ({
  type: 'versions',
  remotion: remotionVersion(),
  templates: Object.fromEntries(templateManifest().map((t) => [t.id, t.version])),
});

// --- props validation --------------------------------------------------------
const isPosInt = (v) => Number.isInteger(v) && v > 0;

/** Minimal structural checks; the plan itself is trusted output of Go. */
export const loadProps = (file) => {
  let raw;
  try {
    raw = fs.readFileSync(file, 'utf8');
  } catch (e) {
    throw new UsageError(`cannot read --props file ${file}: ${e.message}`);
  }
  let props;
  try {
    props = JSON.parse(raw);
  } catch (e) {
    throw new UsageError(`--props file is not valid JSON: ${e.message}`);
  }
  const plan = props?.plan;
  if (!plan || typeof plan !== 'object') throw new UsageError('props must be {"plan": RenderPlan}; "plan" is missing');
  for (const k of ['width', 'height', 'fps', 'durationInFrames']) {
    if (!isPosInt(plan[k])) throw new UsageError(`plan.${k} must be a positive integer (got ${JSON.stringify(plan[k])})`);
  }
  if (plan.width % 2 || plan.height % 2) {
    throw new UsageError(`plan.width/height must be even for yuv420p H.264 (got ${plan.width}x${plan.height})`);
  }
  checkMedia(plan, 0);
  return props;
};

/** A nested cut's clip (D84): `cut:<project>/<revision>`, drawn from plan.nested. */
const isNestedKey = (id) => typeof id === 'string' && id.startsWith('cut:');

/** A conversation clip (D99): `conv:<name>`, drawn from its shots' tiles. */
const isConversationKey = (id) => typeof id === 'string' && id.startsWith('conv:');

/**
 * Every clip and sound has an http(s) source, every nested cut its plan (with
 * the same checks, up to the nesting limit), and every resource file a URL.
 */
const checkMedia = (plan, depth) => {
  for (const k of ['clips', 'audio', 'overlays']) {
    if (!Array.isArray(plan[k])) throw new UsageError(`plan.${k} must be an array`);
  }
  const sources = plan.sources && typeof plan.sources === 'object' ? plan.sources : null;
  if (!sources) throw new UsageError('plan.sources must be an object mapping assetId -> URL');
  const missing = [];
  const badScheme = [];
  // Clearer copies of recordings (variant) render from their own files: a render
  // never falls back to the original voice silently (the preview may).
  const variants = [...plan.clips.map((c) => c?.audio?.variant), ...plan.audio.map((a) => a?.variant)].filter(Boolean);
  // A conversation clip (D99) draws its members' recordings (its tiles); its
  // sound is the members' elements in plan.audio.
  const tiles = plan.clips.flatMap((c) => (c?.conversation?.shots ?? []).flatMap((s) => s?.tiles ?? []));
  for (const item of [...plan.clips, ...plan.audio, ...variants, ...tiles]) {
    if (isConversationKey(item?.assetId)) {
      if (!item?.conversation || !Array.isArray(item.conversation.shots)) throw new UsageError(`clip ${item?.id} plays a conversation but the plan has no shots for it`);
      continue;
    }
    if (isNestedKey(item?.assetId)) {
      const inner = plan.nested?.[item.assetId]?.plan;
      if (!inner || typeof inner !== 'object') throw new UsageError(`plan.nested has no plan for ${item.assetId}`);
      if (depth >= 3) throw new UsageError(`cuts are nested more than 3 deep (${item.assetId})`);
      continue;
    }
    const url = sources[item?.assetId];
    if (typeof url !== 'string' || url.length === 0) {
      missing.push(item?.assetId ?? '(no assetId)');
      continue;
    }
    if (!/^https?:\/\//i.test(url)) badScheme.push(`${item.assetId} -> ${url}`);
  }
  for (const [sha, url] of Object.entries(plan.resourceSources ?? {})) {
    if (typeof url !== 'string' || url.length === 0) missing.push(`resource file ${sha.slice(0, 12)}`);
    else if (!/^https?:\/\//i.test(url)) badScheme.push(`resource file ${sha.slice(0, 12)} -> ${url}`);
  }
  if (missing.length) {
    throw new UsageError(`plan.sources has no URL for asset(s): ${[...new Set(missing)].join(', ')}`);
  }
  if (badScheme.length) {
    throw new UsageError(`plan.sources must be http(s) URLs; got: ${[...new Set(badScheme)].join(', ')}`);
  }
  for (const n of Object.values(plan.nested ?? {})) {
    if (n?.plan && typeof n.plan === 'object') checkMedia(n.plan, depth + 1);
  }
};

// --- bundle cache ------------------------------------------------------------
// Format 2: the page carries the render Content-Security-Policy (D70).
const BUNDLE_FORMAT = 2;

/** Puts the render page's Content-Security-Policy first in its <head>. */
const injectPolicy = (dir) => {
  const p = path.join(dir, 'index.html');
  const html = fs.readFileSync(p, 'utf8');
  const meta = `<meta http-equiv="Content-Security-Policy" content="${RENDER_CSP}"><meta http-equiv="x-dns-prefetch-control" content="off">`;
  if (!/<head[^>]*>/i.test(html)) throw new Error('the Remotion bundle has no <head> to put the security policy in');
  fs.writeFileSync(p, html.replace(/<head[^>]*>/i, (m) => m + meta));
};

const listFiles = (dir) => {
  const out = [];
  if (!fs.existsSync(dir)) return out;
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, entry.name);
    if (entry.isDirectory()) out.push(...listFiles(p));
    else if (entry.isFile()) out.push(p);
  }
  return out;
};

/** Content hash of everything that can change the bundle output. */
export const bundleHash = () => {
  const schemaTs = path.dirname(require.resolve('@metarush/schema'));
  const files = [
    ...listFiles(path.join(VIDEO_DIR, 'src')),
    path.join(VIDEO_DIR, 'package.json'),
    path.join(VIDEO_DIR, 'tsconfig.json'),
    ...listFiles(schemaTs),
    templateCatalogPath(),
  ]
    .filter((f) => fs.existsSync(f))
    .sort();
  const h = crypto.createHash('sha256');
  h.update(`format:${BUNDLE_FORMAT}\nremotion:${remotionVersion()}\ncsp:${RENDER_CSP}\n`);
  for (const f of files) {
    h.update(path.relative(VIDEO_DIR, f));
    h.update('\0');
    h.update(fs.readFileSync(f));
    h.update('\0');
  }
  return h.digest('hex').slice(0, 16);
};

const pruneOldBundles = (cacheDir, keep) => {
  try {
    const dirs = fs
      .readdirSync(cacheDir, { withFileTypes: true })
      .filter((d) => d.isDirectory() && d.name.startsWith('bundle-') && d.name !== keep)
      .map((d) => ({ p: path.join(cacheDir, d.name), m: fs.statSync(path.join(cacheDir, d.name)).mtimeMs }))
      .sort((a, b) => b.m - a.m);
    const hour = 60 * 60 * 1000;
    // Keep the two most recent older bundles (a concurrent render may use one).
    for (const d of dirs.slice(2)) if (Date.now() - d.m > hour) fs.rmSync(d.p, { recursive: true, force: true });
  } catch (e) {
    log('bundle cache prune skipped:', e.message);
  }
};

/**
 * Returns a serveUrl for the bundle of src/index.ts, building it once per
 * content hash. Builds go to a temp dir and are renamed into place atomically,
 * so concurrent renders never see a half-written bundle.
 */
export const getBundle = async ({ cacheDir, onProgress }) => {
  const hash = bundleHash();
  const name = `bundle-${hash}`;
  const finalDir = path.join(cacheDir, name);
  if (fs.existsSync(path.join(finalDir, 'index.html'))) {
    const now = new Date();
    fs.utimesSync(finalDir, now, now);
    log(`using cached bundle ${finalDir}`);
    return { serveUrl: finalDir, cached: true, hash };
  }
  fs.mkdirSync(cacheDir, { recursive: true });
  const tmpDir = path.join(cacheDir, `.tmp-${hash}-${process.pid}-${crypto.randomBytes(4).toString('hex')}`);
  const { bundle } = await import('@remotion/bundler');
  log(`bundling src/index.ts (hash ${hash}) ...`);
  const t0 = Date.now();
  await bundle({
    entryPoint: path.join(VIDEO_DIR, 'src', 'index.ts'),
    outDir: tmpDir,
    rootDir: VIDEO_DIR,
    enableCaching: true,
    onProgress: (p) => onProgress?.(p / 100),
  });
  injectPolicy(tmpDir);
  try {
    fs.renameSync(tmpDir, finalDir);
  } catch (e) {
    // Another process won the race; use its bundle.
    fs.rmSync(tmpDir, { recursive: true, force: true });
    if (!fs.existsSync(path.join(finalDir, 'index.html'))) throw e;
  }
  log(`bundled in ${((Date.now() - t0) / 1000).toFixed(1)}s -> ${finalDir}`);
  pruneOldBundles(cacheDir, name);
  return { serveUrl: finalDir, cached: false, hash };
};

// --- project motion graphics -----------------------------------------------------
/** --motion <hash>=<archive file> (repeatable) → [{hash, file}]. */
export const parseMotionArgs = (values) =>
  (values ?? []).map((v) => {
    const m = /^([0-9a-f]{64})=(.+)$/.exec(v);
    if (!m) throw new UsageError(`--motion must be <sha256>=<archive file>, got ${v}`);
    return { hash: m[1], file: path.resolve(m[2]) };
  });

/** A positive integer flag with a default. */
export const intFlag = (v, def, name, min = 1) => {
  if (v === undefined) return def;
  const n = Number(v);
  if (!Number.isInteger(n) || n < min) throw new UsageError(`--${name} must be an integer of at least ${min}, got ${v}`);
  return n;
};

/**
 * Exits with a plain error when the work runs past its total time limit, or
 * when no frame was finished for frameMs (a component stuck in a loop).
 * Chrome is killed on exit (session.mjs).
 */
export const watchdog = ({ totalMs, frameMs, what = 'the render' }) => {
  let last = Date.now();
  const started = Date.now();
  const t = setInterval(() => {
    const now = Date.now();
    if (totalMs && now - started > totalMs) {
      emit({ type: 'error', code: 'time_limit', message: `${what} ran past its time limit (${Math.round(totalMs / 1000)} s) and was stopped` });
      process.exit(1);
    }
    if (frameMs && now - last > frameMs) {
      emit({ type: 'error', code: 'frame_time_limit', message: `${what} stopped: no frame was finished for ${Math.round(frameMs / 1000)} s (a motion graphic may be stuck)` });
      process.exit(1);
    }
  }, 500);
  t.unref();
  return { tick: () => (last = Date.now()), stop: () => clearInterval(t) };
};

// --- browser -----------------------------------------------------------------
/**
 * Remotion keeps its Chrome Headless Shell under <nearest package.json>/node_modules/.remotion,
 * resolved from the process cwd. We pin cwd to the video package so the
 * worker can spawn us from anywhere and still reuse one browser install.
 */
export const pinCwd = () => process.chdir(VIDEO_DIR);

export const ensureBrowserInstalled = async () => {
  const { ensureBrowser } = await import('@remotion/renderer');
  await ensureBrowser({
    logLevel: 'warn',
    onBrowserDownload: () => {
      log('Chrome Headless Shell missing; downloading (one time) ...');
      let lastDecile = -1;
      return {
        version: null,
        onProgress: ({ percent }) => {
          const decile = Math.floor(percent * 10);
          if (decile > lastDecile) {
            lastDecile = decile;
            log(`browser download ${decile * 10}%`);
          }
        },
      };
    },
  });
};

// ANGLE (GPU) is Remotion v5's default and is markedly faster for blur/filters
// on macOS. Override with METARUSH_REMOTION_GL=swangle on GPU-less hosts.
export const chromiumOptions = {
  gl: process.env.METARUSH_REMOTION_GL || 'angle',
  enableMultiProcessOnLinux: true,
};

/** Throttle to ~4 emits/second; `force` always emits. */
export const throttle = (fn, ms = 250) => {
  let last = 0;
  return (value, force = false) => {
    const now = Date.now();
    if (force || now - last >= ms) {
      last = now;
      fn(value);
    }
  };
};

export const resolveCacheDir = (arg) => path.resolve(arg ?? path.join(VIDEO_DIR, '.bundle-cache'));
