#!/usr/bin/env node
// MetaRush motion graphic check (spawned by `metarush motion check`, D68).
//
//   node render/motion.mjs check --archive <file> --hash <sha256> --out <dir> --plan-base <file.json>
//        [--name route-map] [--duration <s>] [--no-preview] [--bundle-cache <dir>]
//        [--frame-timeout-ms N] [--max-seconds N] [--max-bundle-bytes N] [--resources <file.json>]
//   node render/motion.mjs scan --archive <file> --hash <sha256>
//
// scan only reads the archive (the import scan and the resources its code
// names, D107), so Go can find those resources before the check draws it.
// --resources gives the check the pictures the component draws, by name
// ({resources: {name: PlanResource}, files: {sha256: path}}): they are
// served from loopback like a render's, inside the same jail.
//
// The archive is verified against its hash, unpacked, scanned (parsed
// imports), type-checked, bundled with MetaRush's own settings and drawn in a
// network-jailed headless browser: test frames and a short preview video.
// stdout: JSON lines only (versions, files, scan, types, bundle, info,
// frames, preview, blocked, done | error). Logs: stderr. Exit 0 when the
// check ran (done.ok says whether the component passed), 1 when it could not.
import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { parseArgs } from 'node:util';
import {
  COMPOSITION_ID,
  UsageError,
  chromiumOptions,
  emit,
  ensureBrowserInstalled,
  fail,
  getBundle,
  intFlag,
  log,
  pinCwd,
  remotionVersion,
  resolveCacheDir,
  watchdog,
} from './shared.mjs';
import { extractArchive, readArchive } from './motion/archive.mjs';
import { bundleComponent, DEFAULT_MAX_BUNDLE_BYTES } from './motion/bundle.mjs';
import { scanFiles } from './motion/scan.mjs';
import { openSession } from './motion/session.mjs';
import { typecheck } from './motion/typecheck.mjs';
import { tsSettingsOf } from './motion/policy.mjs';

const INFO_ID = 'MetaRushMotionInfo';
const REGIONS = new Set(['lower', 'center', 'full']);

const finish = (ok, extra = {}) => {
  emit({ type: 'done', ok, ...extra });
  process.exit(0);
};

/** Checks meta as the person and Go will rely on it. */
const metaProblems = (meta) => {
  const out = [];
  const p = (message, hint = '') => out.push({ severity: 'error', code: 'bad_meta', file: 'index.tsx', message, hint });
  if (!meta || typeof meta !== 'object') {
    p('meta must be an object: { summary, region, minDuration, maxDuration }.', "Example: export const meta = { summary: 'An animated route between two cities.', region: 'center', minDuration: 2, maxDuration: 8 };");
    return out;
  }
  if (typeof meta.summary !== 'string' || meta.summary.trim().length < 3 || meta.summary.length > 200) p('meta.summary should be one plain sentence (3 to 200 characters) for the person who approves it.');
  if (!REGIONS.has(meta.region)) p(`meta.region must be lower, center or full (got ${JSON.stringify(meta.region)}).`, 'lower: the band where name titles go; center: the middle; full: covers the whole frame.');
  const okNum = (v) => typeof v === 'number' && Number.isFinite(v);
  if (meta.persistent !== undefined && typeof meta.persistent !== 'boolean') p('meta.persistent is true for a graphic made to stay on screen for a section or the whole video, or left out.');
  if (meta.persistent === true) {
    // Made to stay up for a section or the whole video (D163): no upper limit.
    if (!okNum(meta.minDuration) || meta.minDuration < 0.2 || (meta.maxDuration !== undefined && (!okNum(meta.maxDuration) || meta.maxDuration < meta.minDuration))) {
      p('meta.minDuration is seconds, at least 0.2; a graphic that stays up (meta.persistent) may leave meta.maxDuration out.');
    }
  } else if (!okNum(meta.minDuration) || !okNum(meta.maxDuration) || meta.minDuration < 0.2 || meta.maxDuration > 60 || meta.minDuration > meta.maxDuration) {
    p('meta.minDuration and meta.maxDuration are seconds between 0.2 and 60, the first not above the second.', 'A graphic made to stay on screen for a section or the whole video says meta.persistent: true instead.');
  }
  if (meta.checkAt !== undefined && (!Array.isArray(meta.checkAt) || meta.checkAt.length > 6 || !meta.checkAt.every((t) => okNum(t) && t >= 0))) {
    p('meta.checkAt is a list of up to 6 moments, in seconds from the start of the graphic.');
  }
  return out;
};

const schemaProblems = (info) => {
  const out = [];
  const s = info.jsonSchema;
  if (info.schemaError || !s || typeof s !== 'object') {
    out.push({ severity: 'error', code: 'bad_schema', file: 'index.tsx', message: `schema can't be turned into a parameter description: ${info.schemaError ?? 'it is not a zod schema'}.`, hint: 'Export a zod object: export const schema = z.object({ … }).' });
  } else if (s.type !== 'object') {
    out.push({ severity: 'error', code: 'bad_schema', file: 'index.tsx', message: 'schema must be a zod object (z.object({…})): parameters are named fields.', hint: '' });
  }
  return out;
};

/** Serves resource files at http://127.0.0.1:<port>/<token>/<sha256> (nothing else). */
const serveResources = (files, types) =>
  new Promise((resolve) => {
    const token = crypto.randomBytes(16).toString('hex');
    const server = http.createServer((req, res) => {
      const m = /^\/([0-9a-f]{32})\/([0-9a-f]{64})$/.exec(new URL(req.url, 'http://x').pathname);
      const file = m && m[1] === token ? files[m[2]] : undefined;
      if (!file || (req.method !== 'GET' && req.method !== 'HEAD')) {
        res.writeHead(404).end();
        return;
      }
      const svg = types[m[2]] === 'image/svg+xml';
      res.writeHead(200, { 'Content-Type': types[m[2]] ?? 'application/octet-stream', 'Cache-Control': 'no-store', 'Access-Control-Allow-Origin': '*', 'X-Content-Type-Options': 'nosniff',
        ...(svg ? { 'Content-Security-Policy': "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox" } : {}) });
      if (req.method === 'HEAD') res.end();
      else fs.createReadStream(file).pipe(res);
    });
    server.listen(0, '127.0.0.1', () => resolve({ base: `http://127.0.0.1:${server.address().port}/${token}`, close: () => new Promise((r) => server.close(() => r())) }));
  });

/** The resource names and parameters a component's code draws (useResource). */
const scanOnly = (args) => {
  for (const k of ['archive', 'hash']) if (!args[k]) throw new UsageError(`--${k} is required`);
  const archive = readArchive(path.resolve(args.archive), String(args.hash));
  const scan = scanFiles(archive.files);
  emit({ type: 'scan', imports: scan.imports, problems: scan.problems, resources: scan.resources, libraries: scan.libraries, library: scan.library });
  emit({ type: 'done', ok: !scan.problems.some((p) => p.severity === 'error') });
  process.exit(0);
};

/** Problems with how the code names resources, once the schema is known. */
const resourceProblems = (refs, info, given) => {
  const out = [];
  const props = info?.jsonSchema?.properties ?? {};
  for (const r of refs) {
    if (r.param !== undefined && props[r.param]?.format !== 'metarush-resource') {
      out.push({ severity: 'error', code: 'resource_param', file: r.file, line: r.line,
        message: `useResource(params.${r.param}): ${r.param} isn't a parameter marked as a resource.`, hint: `In the schema: ${r.param}: z.string().meta({ format: 'metarush-resource' }).` });
    }
    if (r.name !== undefined && !given[r.name.split('#')[0]]) {
      out.push({ severity: 'error', code: 'resource_unknown', file: r.file, line: r.line, message: `useResource("${r.name}"): this project has no picture called ${r.name.split('#')[0]}.`, hint: 'Add it first (metarush resources add …), or use a name from metarush resources list.' });
    }
  }
  return out;
};

/** --lib name=sha256=archive: a shared library a graphic is checked with (D171). */
const parseLibs = (values) =>
  (values ?? []).map((v) => {
    const m = /^([a-z][a-z0-9-]{0,39})=([0-9a-f]{64})=(.+)$/.exec(v);
    if (!m) throw new UsageError(`--lib and --draw take <name>=<sha256>=<archive file>, got ${v}`);
    return { name: m[1], hash: m[2], file: path.resolve(m[3]) };
  });

/** Unpacks shared libraries (verified, scanned) under dir: {name: folder}. */
const unpackLibs = (libs, dir) => {
  const out = {};
  for (const l of libs) {
    const la = readArchive(l.file, l.hash);
    const ls = scanFiles(la.files);
    if (!ls.library) throw new UsageError(`${l.name} (${l.hash.slice(0, 8)}) isn't a shared library`);
    out[l.name] = extractArchive(la, path.join(dir, 'lib', l.name));
  }
  return out;
};

const check = async (args) => {
  for (const k of ['archive', 'hash', 'out', 'plan-base']) if (!args[k]) throw new UsageError(`--${k} is required`);
  const hash = String(args.hash);
  if (!/^[0-9a-f]{64}$/.test(hash)) throw new UsageError('--hash must be a full sha256');
  const outDir = path.resolve(args.out);
  const cacheDir = resolveCacheDir(args['bundle-cache']);
  const frameTimeoutMs = intFlag(args['frame-timeout-ms'], 20000, 'frame-timeout-ms', 7000);
  const maxSeconds = intFlag(args['max-seconds'], 240, 'max-seconds');
  const maxBundleBytes = intFlag(args['max-bundle-bytes'], DEFAULT_MAX_BUNDLE_BYTES, 'max-bundle-bytes');
  const base = JSON.parse(fs.readFileSync(path.resolve(args['plan-base']), 'utf8'));
  const name = args.name && /^[a-z][a-z0-9-]*$/.test(args.name) ? args.name : 'component';
  fs.mkdirSync(outDir, { recursive: true });
  const dog = watchdog({ totalMs: maxSeconds * 1000, what: 'the check' });

  // 1. The archive, exactly as it will be stored and rendered.
  const archive = readArchive(path.resolve(args.archive), hash);
  emit({ type: 'files', files: archive.files.map((f) => ({ path: f.path, bytes: Buffer.byteLength(f.text), lines: f.text.split('\n').length })) });

  // 2. The import scan on the parsed source (and the resources its code draws).
  const scan = scanFiles(archive.files);
  emit({ type: 'scan', imports: scan.imports, problems: scan.problems, resources: scan.resources, libraries: scan.libraries, library: scan.library });
  if (scan.problems.some((p) => p.severity === 'error')) finish(false, { stage: 'scan' });
  const libs = parseLibs(args.lib);
  for (const n of scan.libraries ?? []) {
    if (!libs.some((l) => l.name === n)) {
      emit({ type: 'scan', imports: [], resources: [], problems: [{ severity: 'error', code: 'library_unknown', file: 'index.tsx', line: 1, message: `It uses the shared library ${n}, which this workspace has no checked version of.`, hint: `Check the library first (metarush motion check motion/${n}).` }] });
      finish(false, { stage: 'scan' });
    }
  }
  if (scan.library) return checkLibrary(args, archive, hash, name, outDir, cacheDir, frameTimeoutMs, maxBundleBytes, base, dog);

  // 3. Types, with MetaRush's settings, then 4. the bundle (size limit included).
  const stage = await (async () => {
    const work = fs.mkdtempSync(path.join(os.tmpdir(), 'metarush-motion-check-'));
    try {
      const root = extractArchive(archive, path.join(work, name));
      const libDirs = unpackLibs(libs, work);
      const types = typecheck(root, libDirs, tsSettingsOf(archive.files).settings);
      emit({ type: 'types', problems: types });
      if (types.some((p) => p.severity === 'error')) return 'types';
      try {
        const bundled = await bundleComponent({ dir: root, hash, maxBytes: maxBundleBytes, libs: libDirs });
        emit({ type: 'bundle', bytes: bundled.bytes, problems: [] });
      } catch (e) {
        emit({ type: 'bundle', problems: [{ severity: 'error', code: e.code ?? 'bundle', file: 'index.tsx', message: e.message }] });
        return 'bundle';
      }
      return '';
    } finally {
      fs.rmSync(work, { recursive: true, force: true });
    }
  })();
  if (stage) finish(false, { stage });

  // 5. Load it in the jailed browser: exports, meta, schema, sample parameters.
  let preview = {};
  // A graphic that changes while it stays up (D163) can show a sample change:
  // preview.json's "changes": [{"at": seconds, "params": {…}}] (not parameters).
  let sampleChanges = [];
  const pv = archive.files.find((f) => f.path === 'preview.json');
  if (pv) {
    try {
      preview = JSON.parse(pv.text);
    } catch (e) {
      emit({ type: 'info', problems: [{ severity: 'error', code: 'bad_preview', file: 'preview.json', message: `preview.json isn't valid JSON: ${e.message}` }] });
      finish(false, { stage: 'info' });
    }
    if (preview && typeof preview === 'object' && Array.isArray(preview.changes)) {
      sampleChanges = preview.changes
        .filter((c) => c && typeof c.at === 'number' && Number.isFinite(c.at) && c.at > 0 && c.params && typeof c.params === 'object' && !Array.isArray(c.params))
        .slice(0, 6)
        .sort((a, b) => a.at - b.at);
      const { changes: _drop, ...rest } = preview;
      preview = rest;
    }
  }
  pinCwd();
  await ensureBrowserInstalled();
  const { serveUrl } = await getBundle({ cacheDir });
  const fps = base.fps ?? 30;
  const plan = {
    schemaVersion: 1,
    revisionId: 'motion-check',
    projectId: '',
    specHash: hash,
    media: 'original',
    width: base.width,
    height: base.height,
    fps,
    durationInFrames: fps * 4,
    background: base.background ?? '#101014',
    brand: base.brand,
    safeArea: base.safeArea,
    clips: [],
    audio: [],
    dialogueRanges: [],
    overlays: [{ id: 'check', template: '', templateVersion: '', from: 0, durationInFrames: fps * 4, params: preview, motion: { name, hash, region: 'center', libs: Object.fromEntries(libs.map((l) => [l.name, l.hash])) } }],
    sources: {},
  };
  // The pictures its code draws (useResource), served like a render's.
  let resourceServer = null;
  const given = {};
  if (args.resources) {
    const r = JSON.parse(fs.readFileSync(path.resolve(args.resources), 'utf8'));
    Object.assign(given, r.resources ?? {});
    const files = r.files ?? {};
    if (Object.keys(files).length) {
      resourceServer = await serveResources(files, r.types ?? {});
      plan.resources = given;
      plan.resourceSources = Object.fromEntries(Object.keys(files).map((sha) => [sha, `${resourceServer.base}/${sha}`]));
    }
  }
  const session = await openSession({ plan, motion: [{ hash, file: path.resolve(args.archive) }, ...libs.map((l) => ({ hash: l.hash, file: l.file }))], maxBundleBytes, chromiumOptions, log });
  const closeAll = async () => {
    await session.close();
    await resourceServer?.close();
  };
  try {
    const { renderMedia, renderStill, selectComposition } = await import('@remotion/renderer');
    const infoComp = await selectComposition({
      serveUrl,
      id: INFO_ID,
      inputProps: { hash, url: plan.motionSources[hash], preview },
      chromiumOptions,
      logLevel: 'warn',
      timeoutInMilliseconds: frameTimeoutMs,
      ...session.remotion,
    });
    const info = infoComp.props.info;
    const problems = [];
    if (!info?.component) problems.push({ severity: 'error', code: 'missing_export', file: 'index.tsx', message: "index.tsx doesn't export the component (export default)." });
    problems.push(...metaProblems(info?.meta), ...schemaProblems(info ?? {}), ...resourceProblems(scan.resources ?? [], info, given));
    if (info && !info.preview.ok) {
      problems.push({ severity: 'error', code: 'bad_preview', file: pv ? 'preview.json' : 'index.tsx', message: `The sample parameters don't match the schema: ${info.preview.error}`, hint: pv ? '' : 'Add a preview.json with sample parameters.' });
    }
    emit({ type: 'info', meta: info?.meta ?? null, jsonSchema: info?.jsonSchema ?? null, exports: info?.exports ?? [], problems });
    if (problems.some((p) => p.severity === 'error')) {
      await closeAll();
      finish(false, { stage: 'info', blocked: session.blocked() });
    }

    // 6. Test frames and a short preview, as a plan with only this graphic, in
    // every shape of video it is for (D164: the cuts that use it).
    const meta = info.meta;
    plan.overlays[0].motion.region = meta.region;
    const persistent = meta.persistent === true;
    const fallback = persistent ? 20 : 4; // a graphic that stays up is drawn over 20 s (D163)
    const want = args.duration !== undefined ? Number(args.duration) : fallback;
    const longest = persistent ? Math.max(meta.minDuration, meta.maxDuration ?? 600) : meta.maxDuration;
    const seconds = Math.min(longest, Math.max(meta.minDuration, Number.isFinite(want) && want > 0 ? want : fallback));
    const total = Math.max(2, Math.round(seconds * fps));
    plan.durationInFrames = total;
    plan.overlays[0].durationInFrames = total;
    const at = (s) => Math.max(0, Math.min(total - 1, Math.round(s * fps)));
    // A sample change (preview.json "changes"): the graphic's fields from that frame on.
    if (sampleChanges.length) {
      let cur = { ...preview };
      plan.overlays[0].segments = [{ from: 0, params: cur }];
      for (const c of sampleChanges) {
        cur = { ...cur, ...c.params };
        const from = at(c.at);
        if (from > 0) plan.overlays[0].segments.push({ from, params: cur });
      }
    }
    const picks = [
      ['first', 0],
      ['start', at(Math.min(0.4, seconds / 4))],
      ['middle', Math.floor(total / 2)],
      ['end', at(seconds - Math.min(0.4, seconds / 4))],
      ['last', total - 1],
      ...(meta.checkAt ?? []).map((t, i) => [`moment ${i + 1}`, at(t)]),
      ...sampleChanges.map((c, i) => [`change ${i + 1}`, at(c.at + 0.3)]),
    ];
    const shapes = Array.isArray(base.shapes) && base.shapes.length ? base.shapes.slice(0, 3) : [{ width: base.width, height: base.height, safeArea: base.safeArea, label: '' }];
    const previewFrames = persistent ? Math.min(total, Math.round(6 * fps)) : total;
    for (const [si, shape] of shapes.entries()) {
      const tag = si === 0 ? '' : `${String(shape.label || si).replace(/[^a-z0-9-]/gi, '') || si}-`;
      const shapePlan = { ...plan, width: shape.width, height: shape.height, safeArea: shape.safeArea ?? plan.safeArea };
      const inputProps = { plan: shapePlan };
      const comp = await selectComposition({ serveUrl, id: COMPOSITION_ID, inputProps, chromiumOptions, logLevel: 'warn', timeoutInMilliseconds: frameTimeoutMs, ...session.remotion });
      const seen = new Set();
      const frames = [];
      for (const [label, frame] of picks) {
        if (seen.has(frame)) continue;
        seen.add(frame);
        const file = path.join(outDir, `frame-${tag}${String(frame).padStart(4, '0')}.png`);
        const t0 = Date.now();
        await renderStill({ composition: comp, serveUrl, inputProps, frame, output: file, imageFormat: 'png', overwrite: true, chromiumOptions, timeoutInMilliseconds: frameTimeoutMs, logLevel: 'warn', ...session.remotion });
        frames.push({ label, frame, at: frame / fps, file, ms: Date.now() - t0 });
      }
      // The middle again: a component must draw a frame the same way every time.
      let again = '';
      if (si === 0) {
        const mid = frames.find((f) => f.label === 'middle');
        again = path.join(outDir, 'middle-again.png');
        await renderStill({ composition: comp, serveUrl, inputProps, frame: mid.frame, output: again, imageFormat: 'png', overwrite: true, chromiumOptions, timeoutInMilliseconds: frameTimeoutMs, logLevel: 'warn', ...session.remotion });
      }
      emit({ type: 'frames', durationInFrames: total, fps, frames, again, shape: shape.label ?? '', width: shape.width, height: shape.height });

      if (!args['no-preview']) {
        const file = path.join(outDir, `preview${tag ? '-' + tag.slice(0, -1) : ''}.mp4`);
        const frameDog = watchdog({ frameMs: frameTimeoutMs + 10000, what: 'the preview' });
        await renderMedia({
          composition: comp,
          serveUrl,
          inputProps,
          codec: 'h264',
          crf: 28,
          pixelFormat: 'yuv420p',
          scale: 0.5,
          outputLocation: file,
          overwrite: true,
          chromiumOptions,
          timeoutInMilliseconds: frameTimeoutMs,
          logLevel: 'warn',
          ...(previewFrames < total ? { frameRange: [0, previewFrames - 1] } : {}),
          ...session.remotion,
          onProgress: () => frameDog.tick(),
        });
        frameDog.stop();
        emit({ type: 'preview', file, shape: shape.label ?? '' });
      }
    }
    const blocked = session.blocked();
    if (blocked.length) emit({ type: 'blocked', hosts: blocked });
    dog.stop();
    await closeAll();
    finish(true, { renderer: remotionVersion(), bundleBytes: session.bundles[0]?.bytes ?? 0 });
  } catch (e) {
    const blocked = session.blocked();
    if (blocked.length) emit({ type: 'blocked', hosts: blocked });
    await closeAll();
    throw e;
  }
};

/**
 * A shared library's check (D171): its type check, a bundle that imports it,
 * then each graphic that uses it (--draw name=sha256=archive, their latest
 * checked versions) drawn with this version, so a person sees what one theme
 * change does to every graphic before approving it once.
 */
const checkLibrary = async (args, archive, hash, name, outDir, cacheDir, frameTimeoutMs, maxBundleBytes, base, dog) => {
  const work = fs.mkdtempSync(path.join(os.tmpdir(), 'metarush-motion-lib-'));
  // finish() ends the process at once (process.exit skips finally blocks):
  // the work folder goes on the way out, however the check ends.
  const removeWork = () => fs.rmSync(work, { recursive: true, force: true });
  process.once('exit', removeWork);
  try {
    const root = extractArchive(archive, path.join(work, 'lib', name));
    const types = typecheck(root, {}, tsSettingsOf(archive.files).settings);
    emit({ type: 'types', problems: types });
    if (types.some((p) => p.severity === 'error')) finish(false, { stage: 'types' });
    // A graphic that imports everything it exports must bundle.
    const probeDir = path.join(work, 'probe');
    fs.mkdirSync(probeDir, { recursive: true });
    fs.writeFileSync(path.join(probeDir, 'index.tsx'), `import * as L from '@metarush/lib/${name}';\nimport { z } from 'zod';\nexport const schema = z.object({});\nexport const meta = { summary: 'library check', region: 'full' as const, minDuration: 1, maxDuration: 2 };\nexport default function Probe() {\n  void L;\n  return null;\n}\n`);
    try {
      const b = await bundleComponent({ dir: probeDir, hash: '0'.repeat(64), maxBytes: maxBundleBytes, libs: { [name]: root } });
      emit({ type: 'bundle', bytes: b.bytes, problems: [] });
    } catch (e) {
      emit({ type: 'bundle', problems: [{ severity: 'error', code: e.code ?? 'bundle', file: 'index.tsx', message: e.message }] });
      finish(false, { stage: 'bundle' });
    }
    emit({ type: 'info', meta: { summary: (() => { try { return JSON.parse(archive.files.find((f) => f.path === 'library.json').text).summary; } catch { return ''; } })(), region: 'full', minDuration: 0, maxDuration: 0, library: true }, jsonSchema: { type: 'object', properties: {} }, exports: [], problems: [] });
    // The graphics that use it, drawn with this version.
    const draws = parseLibs(args.draw);
    const frames = [];
    const fps = base.fps ?? 30;
    if (draws.length) {
      pinCwd();
      await ensureBrowserInstalled();
      const { serveUrl } = await getBundle({ cacheDir });
      const { renderStill, selectComposition } = await import('@remotion/renderer');
      for (const d of draws) {
        const ga = readArchive(d.file, d.hash);
        let preview = {};
        const pv = ga.files.find((f) => f.path === 'preview.json');
        if (pv) {
          try {
            preview = JSON.parse(pv.text);
          } catch {
            preview = {};
          }
        }
        const total = fps * 4;
        const plan = {
          schemaVersion: 1, revisionId: 'motion-library-check', projectId: '', specHash: hash, media: 'original', width: base.width, height: base.height, fps,
          durationInFrames: total, background: base.background ?? '#101014', brand: base.brand, safeArea: base.safeArea, clips: [], audio: [], dialogueRanges: [], sources: {},
          overlays: [{ id: 'check', template: '', templateVersion: '', from: 0, durationInFrames: total, params: preview, motion: { name: d.name, hash: d.hash, region: 'full', libs: { [name]: hash } } }],
        };
        // The pictures and fonts they draw (useResource, useFont), served like a render's.
        let resourceServer = null;
        if (args.resources) {
          const r = JSON.parse(fs.readFileSync(path.resolve(args.resources), 'utf8'));
          const files = r.files ?? {};
          if (Object.keys(files).length) {
            resourceServer = await serveResources(files, r.types ?? {});
            plan.resources = r.resources ?? {};
            plan.resourceSources = Object.fromEntries(Object.keys(files).map((sha) => [sha, `${resourceServer.base}/${sha}`]));
          }
        }
        const session = await openSession({ plan, motion: [{ hash: d.hash, file: d.file }, { hash, file: path.resolve(args.archive) }], maxBundleBytes, chromiumOptions, log });
        try {
          const inputProps = { plan };
          const comp = await selectComposition({ serveUrl, id: COMPOSITION_ID, inputProps, chromiumOptions, logLevel: 'warn', timeoutInMilliseconds: frameTimeoutMs, ...session.remotion });
          for (const [label, frame] of [[`${d.name}: start`, Math.round(total * 0.25)], [`${d.name}: middle`, Math.floor(total / 2)]]) {
            const file = path.join(outDir, `frame-${d.name}-${String(frame).padStart(4, '0')}.png`);
            const t0 = Date.now();
            await renderStill({ composition: comp, serveUrl, inputProps, frame, output: file, imageFormat: 'png', overwrite: true, chromiumOptions, timeoutInMilliseconds: frameTimeoutMs, logLevel: 'warn', ...session.remotion });
            frames.push({ label, frame, at: frame / fps, file, ms: Date.now() - t0 });
          }
          const blocked = session.blocked();
          if (blocked.length) emit({ type: 'blocked', hosts: blocked });
        } finally {
          await session.close();
          await resourceServer?.close();
        }
      }
    }
    emit({ type: 'frames', durationInFrames: fps * 4, fps, frames, again: '' });
    dog.stop();
    finish(true, { renderer: remotionVersion(), bundleBytes: 0, library: true });
  } finally {
    removeWork();
    process.removeListener('exit', removeWork);
  }
};

const main = async () => {
  emit({ type: 'versions', remotion: remotionVersion() });
  const [cmd, ...rest] = process.argv.slice(2);
  let args;
  try {
    ({ values: args } = parseArgs({
      args: rest,
      options: {
        archive: { type: 'string' },
        hash: { type: 'string' },
        out: { type: 'string' },
        'plan-base': { type: 'string' },
        name: { type: 'string' },
        duration: { type: 'string' },
        'no-preview': { type: 'boolean' },
        'bundle-cache': { type: 'string' },
        'frame-timeout-ms': { type: 'string' },
        'max-seconds': { type: 'string' },
        'max-bundle-bytes': { type: 'string' },
        resources: { type: 'string' },
        lib: { type: 'string', multiple: true },
        draw: { type: 'string', multiple: true },
      },
      strict: true,
    }));
  } catch (e) {
    throw new UsageError(e.message);
  }
  if (cmd === 'scan') return scanOnly(args);
  if (cmd !== 'check') throw new UsageError('usage: motion.mjs check --archive <file> --hash <sha256> --out <dir> --plan-base <file.json> | scan --archive <file> --hash <sha256>');
  await check(args);
};

main().catch((err) => fail(err));
