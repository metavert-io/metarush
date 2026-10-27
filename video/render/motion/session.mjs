// A render session: the network jail around Remotion's browser, plus the
// project motion graphics a plan uses (scanned and bundled from their
// approved archives, served on loopback). render.mjs, still.mjs and
// motion.mjs (check) all render through one.
import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { extractArchive, readArchive } from './archive.mjs';
import { DEFAULT_MAX_BUNDLE_BYTES, bundleComponent } from './bundle.mjs';
import { chromeWasJailed, guardDownloads, hostPort, jailChrome, killChromes, reservePort, startRefusingProxy } from './jail.mjs';
import { scanFiles } from './scan.mjs';

/** A plain error a person can act on (the renderer prints its message). */
export class MotionError extends Error {
  constructor(message, details) {
    super(message);
    this.details = details;
  }
}

/**
 * Builds the bundle for one archive: verify → unpack → scan → bundle, with the
 * shared libraries it imports (D171: libs [{name, hash, file}], each an
 * archive verified and scanned the same way).
 */
export const buildMotion = async ({ hash, file, maxBundleBytes = DEFAULT_MAX_BUNDLE_BYTES, workDir, libs = [] }) => {
  const archive = readArchive(file, hash);
  const scan = scanFiles(archive.files);
  const errors = scan.problems.filter((p) => p.severity === 'error');
  if (errors.length) {
    const first = errors[0];
    throw new MotionError(`the motion graphic ${hash.slice(0, 8)} has ${errors.length} problem(s), first ${first.file}:${first.line}: ${first.message}`, { problems: scan.problems });
  }
  const dir = fs.mkdtempSync(path.join(workDir ?? os.tmpdir(), `motion-${hash.slice(0, 8)}-`));
  try {
    const root = extractArchive(archive, path.join(dir, 'src'));
    const libDirs = {};
    for (const l of libs) {
      const la = readArchive(l.file, l.hash);
      const ls = scanFiles(la.files);
      if (!ls.library) throw new MotionError(`${l.name} (${l.hash.slice(0, 8)}) isn't a shared library`);
      const le = ls.problems.filter((p) => p.severity === 'error');
      if (le.length) throw new MotionError(`the shared library ${l.name} (${l.hash.slice(0, 8)}) has ${le.length} problem(s), first ${le[0].file}:${le[0].line}: ${le[0].message}`, { problems: ls.problems });
      libDirs[l.name] = extractArchive(la, path.join(dir, 'lib', l.name));
    }
    for (const n of scan.libraries ?? []) {
      if (!libDirs[n]) throw new MotionError(`the motion graphic ${hash.slice(0, 8)} uses the shared library ${n}, but no version of it was given`);
    }
    const b = await bundleComponent({ dir: root, hash, maxBytes: maxBundleBytes, libs: libDirs });
    return { hash, archive, scan, code: b.code, bytes: b.bytes };
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
};

/** Serves bundles at http://127.0.0.1:<port>/<token>/<hash>.js (nothing else). */
const serveBundles = (bundles) =>
  new Promise((resolve) => {
    const token = crypto.randomBytes(16).toString('hex');
    const byHash = new Map(bundles.map((b) => [b.hash, b.code]));
    const server = http.createServer((req, res) => {
      const m = /^\/([0-9a-f]{32})\/([0-9a-f]{64})\.js$/.exec(new URL(req.url, 'http://x').pathname);
      const code = m && m[1] === token ? byHash.get(m[2]) : undefined;
      if (code === undefined || (req.method !== 'GET' && req.method !== 'HEAD')) {
        res.writeHead(404).end();
        return;
      }
      res.writeHead(200, { 'Content-Type': 'text/javascript; charset=utf-8', 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff', 'Access-Control-Allow-Origin': '*' });
      res.end(req.method === 'HEAD' ? undefined : code);
    });
    server.listen(0, '127.0.0.1', () => {
      const base = `http://127.0.0.1:${server.address().port}/${token}`;
      resolve({ base, close: () => new Promise((r) => server.close(() => r())) });
    });
  });

/** The plan and every plan nested in it (cuts played inside it), outermost first. */
const allPlans = (plan) => {
  const out = [];
  const walk = (p, depth) => {
    if (!p || typeof p !== 'object' || depth > 3) return;
    out.push(p);
    for (const n of Object.values(p.nested ?? {})) walk(n?.plan, depth + 1);
  };
  walk(plan, 0);
  return out;
};

/**
 * Opens a session. plan is mutated: plan.motionSources gets the bundle URLs.
 * motion is [{hash, file}] (archives); every motion graphic the plan uses
 * must be among them. Returns the Remotion options to pass (port,
 * puppeteerInstance) and what was blocked.
 */
export const openSession = async ({ plan, motion = [], maxBundleBytes, chromiumOptions, logLevel = 'warn', extraLoopback = [], log = () => {} }) => {
  // The plan's own motion graphics and those of cuts nested in it (D84).
  const plans = allPlans(plan);
  const used = [...new Set(plans.flatMap((p) => (p.overlays ?? []).filter((o) => o?.motion?.hash).map((o) => o.motion.hash)))];
  const files = new Map(motion.map((m) => [m.hash, m.file]));
  for (const h of used) if (!files.has(h)) throw new MotionError(`the plan uses the motion graphic ${h.slice(0, 8)} but the renderer was not given its approved archive`);
  // The shared libraries each graphic is pinned to (D171), from the plan's overlays.
  const libsOf = new Map();
  for (const p of plans) {
    for (const o of p.overlays ?? []) {
      if (!o?.motion?.hash || libsOf.has(o.motion.hash)) continue;
      const libs = Object.entries(o.motion.libs ?? {}).map(([name, h]) => {
        if (!files.has(h)) throw new MotionError(`the plan uses the shared library ${name} (${String(h).slice(0, 8)}) but the renderer was not given its approved archive`);
        return { name, hash: h, file: files.get(h) };
      });
      libsOf.set(o.motion.hash, libs);
    }
  }
  const bundles = [];
  for (const h of used) bundles.push(await buildMotion({ hash: h, file: files.get(h), maxBundleBytes, libs: libsOf.get(h) ?? [] }));
  const blockedNode = new Set();
  let bundleServer = null;
  const loopback = [...extraLoopback];
  if (bundles.length) {
    bundleServer = await serveBundles(bundles);
    const motionSources = Object.fromEntries(bundles.map((b) => [b.hash, `${bundleServer.base}/${b.hash}.js`]));
    for (const p of plans) p.motionSources = motionSources;
    loopback.push(hostPort(bundleServer.base));
  } else {
    for (const p of plans) delete p.motionSources;
  }
  // Footage and resource files (logos, pictures, fonts) come from loopback media servers.
  for (const p of plans) {
    for (const u of [...Object.values(p.sources ?? {}), ...Object.values(p.resourceSources ?? {})]) {
      try {
        loopback.push(hostPort(u));
      } catch {
        // loadProps already refused non-http sources
      }
    }
  }
  const { port, release } = await reservePort();
  loopback.push(`localhost:${port}`, `127.0.0.1:${port}`);
  const proxy = await startRefusingProxy();
  jailChrome({ proxyPort: proxy.port, bypass: loopback });
  guardDownloads(loopback, (h) => blockedNode.add(h));
  const { openBrowser } = await import('@remotion/renderer');
  const browser = await openBrowser('chrome', { chromiumOptions, logLevel });
  if (!chromeWasJailed()) {
    if (bundles.length) {
      await release();
      await browser.close({ silent: true }).catch(() => {});
      throw new MotionError("MetaRush couldn't lock the renderer's browser off the network, so it won't run project motion graphics. Update MetaRush (Remotion changed how it starts Chrome).");
    }
    log('warning: the render browser could not be network-jailed (vetted templates only in this plan)');
  }
  // The page reports what its Content-Security-Policy blocked (host.ts).
  const blockedPage = new Set();
  const onBrowserLog = (l) => {
    const m = /\[metarush\] blocked: (\S+) (\S*)/.exec(l.text ?? '');
    if (m) {
      try {
        blockedPage.add(new URL(m[2]).host || m[2]);
      } catch {
        blockedPage.add(m[2] || m[1]);
      }
      return;
    }
    if (l.type === 'error' || l.type === 'warning') log(`[browser ${l.type}] ${l.text}`);
  };
  await release(); // Remotion binds it from here on
  let closed = false;
  const close = async () => {
    if (closed) return;
    closed = true;
    await browser.close({ silent: true }).catch(() => {});
    killChromes();
    await proxy.close().catch(() => {});
    await bundleServer?.close().catch(() => {});
  };
  return {
    port,
    browser,
    bundles,
    remotion: { port, puppeteerInstance: browser, onBrowserLog },
    blocked: () => [...new Set([...proxy.tried, ...blockedNode, ...blockedPage])].sort(),
    close,
  };
};

/** Last-resort cleanup on exit: no Chrome survives its renderer. */
process.on('exit', () => killChromes());
