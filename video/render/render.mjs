#!/usr/bin/env node
// MetaRush final-render CLI (spawned by the Go worker).
//
//   node render/render.mjs --props <file.json> --out <file.mp4>
//        [--scale 1] [--crf 18] [--concurrency N] [--bundle-cache <dir>]
//        [--hwaccel if-possible|disable]
//        [--motion <sha256>=<archive>]… [--frame-timeout-ms N] [--max-seconds N] [--max-bundle-bytes N]
//
// stdout: JSON lines only (versions, progress..., done | error). Logs: stderr.
// Every render runs in a network-jailed browser (motion/jail.mjs); project
// motion graphics come only as approved archives (--motion), are scanned and
// bundled here, and are served to the page from loopback.
import fs from 'node:fs';
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
  loadProps,
  log,
  parseMotionArgs,
  pinCwd,
  resolveCacheDir,
  throttle,
  versionsLine,
  watchdog,
} from './shared.mjs';
import { openSession } from './motion/session.mjs';

const main = async () => {
  emit(versionsLine());

  let args;
  try {
    ({ values: args } = parseArgs({
      options: {
        props: { type: 'string' },
        out: { type: 'string' },
        scale: { type: 'string' },
        crf: { type: 'string' },
        concurrency: { type: 'string' },
        'bundle-cache': { type: 'string' },
        hwaccel: { type: 'string' },
        motion: { type: 'string', multiple: true },
        'frame-timeout-ms': { type: 'string' },
        'max-seconds': { type: 'string' },
        'max-bundle-bytes': { type: 'string' },
      },
      strict: true,
    }));
  } catch (e) {
    throw new UsageError(e.message);
  }
  if (!args.props) throw new UsageError('--props <file.json> is required');
  if (!args.out) throw new UsageError('--out <file.mp4> is required');

  // Resolve every path before pinning cwd to the video package.
  const propsFile = path.resolve(args.props);
  const outFile = path.resolve(args.out);
  const cacheDir = resolveCacheDir(args['bundle-cache']);

  const scale = args.scale === undefined ? 1 : Number(args.scale);
  if (!(scale > 0 && scale <= 4)) throw new UsageError(`--scale must be in (0, 4], got ${args.scale}`);
  const crfGiven = args.crf !== undefined;
  const crf = crfGiven ? Number(args.crf) : 18;
  if (!Number.isInteger(crf) || crf < 1 || crf > 51) throw new UsageError(`--crf must be an integer 1..51, got ${args.crf}`);
  const concurrency = args.concurrency === undefined ? null : Number(args.concurrency);
  if (concurrency !== null && !(Number.isInteger(concurrency) && concurrency >= 1)) {
    throw new UsageError(`--concurrency must be a positive integer, got ${args.concurrency}`);
  }
  const hwaccel = args.hwaccel ?? 'disable';
  if (hwaccel !== 'disable' && hwaccel !== 'if-possible') {
    throw new UsageError(`--hwaccel must be "if-possible" or "disable", got ${args.hwaccel}`);
  }

  const motion = parseMotionArgs(args.motion);
  const frameTimeoutMs = intFlag(args['frame-timeout-ms'], 120000, 'frame-timeout-ms', 7000);
  const maxSeconds = intFlag(args['max-seconds'], 0, 'max-seconds');
  const maxBundleBytes = intFlag(args['max-bundle-bytes'], undefined, 'max-bundle-bytes');

  const props = loadProps(propsFile);
  const plan = props.plan;
  fs.mkdirSync(path.dirname(outFile), { recursive: true });
  pinCwd();
  const dog = watchdog({ totalMs: maxSeconds * 1000, what: 'the render' });

  const progress = { type: 'progress', progress: 0, renderedFrames: 0, encodedFrames: 0, stage: 'preparing' };
  const emitProgress = throttle((p) => emit(p));

  emitProgress({ ...progress, stage: 'browser' }, true);
  await ensureBrowserInstalled();

  const { serveUrl, cached } = await getBundle({
    cacheDir,
    onProgress: (p) => emitProgress({ ...progress, stage: 'bundling', stageProgress: Math.round(p * 1000) / 1000 }),
  });
  if (!cached) emitProgress({ ...progress, stage: 'bundling', stageProgress: 1 }, true);

  const { selectComposition, renderMedia, makeCancelSignal } = await import('@remotion/renderer');
  emitProgress({ ...progress, stage: 'preparing' }, true);
  const session = await openSession({ plan, motion, maxBundleBytes, chromiumOptions, log });
  const composition = await selectComposition({
    serveUrl,
    id: COMPOSITION_ID,
    inputProps: props,
    chromiumOptions,
    logLevel: 'warn',
    timeoutInMilliseconds: frameTimeoutMs,
    ...session.remotion,
  });
  if (
    composition.durationInFrames !== plan.durationInFrames ||
    composition.width !== plan.width ||
    composition.height !== plan.height ||
    composition.fps !== plan.fps
  ) {
    throw new Error(
      `composition metadata ${composition.width}x${composition.height}@${composition.fps} ${composition.durationInFrames}f does not match plan ${plan.width}x${plan.height}@${plan.fps} ${plan.durationInFrames}f`,
    );
  }

  // Hardware encoders cannot honor CRF. With an explicit --crf, Remotion falls
  // back to software (x264) even when --hwaccel if-possible is set; without one
  // we give VideoToolbox a bitrate scaled to the frame size instead.
  // Hardware means VideoToolbox, on macOS. On Linux Remotion's "if-possible"
  // picks NVENC without checking for an NVIDIA GPU, and FFmpeg then quits
  // ("Cannot load libcuda.so.1"), so Linux encodes in software unless
  // METARUSH_REMOTION_HWACCEL=1 says this host has one.
  const hwHere = process.platform === 'darwin' || process.env.METARUSH_REMOTION_HWACCEL === '1';
  const useHw = hwaccel === 'if-possible' && !crfGiven && hwHere;
  const bitrate = useHw
    ? `${Math.max(4, Math.round((plan.width * plan.height * scale * scale * plan.fps * 0.18) / 1e6))}M`
    : null;
  if (hwaccel === 'if-possible' && crfGiven) {
    log('--hwaccel if-possible with an explicit --crf: hardware encoders ignore CRF, encoding in software');
  }

  const { cancelSignal, cancel } = makeCancelSignal();
  let cancelled = false;
  for (const sig of ['SIGINT', 'SIGTERM']) {
    process.on(sig, () => {
      if (cancelled) process.exit(130);
      cancelled = true;
      log(`received ${sig}, cancelling render`);
      cancel();
    });
  }

  const total = plan.durationInFrames;
  const frames = watchdog({ frameMs: frameTimeoutMs + 15000, what: 'the render' });
  let lastRendered = -1;
  await renderMedia({
    composition,
    serveUrl,
    inputProps: props,
    codec: 'h264',
    audioCodec: 'aac',
    pixelFormat: 'yuv420p',
    ...(useHw ? { videoBitrate: bitrate } : { crf }),
    hardwareAcceleration: useHw ? 'if-possible' : 'disable',
    scale,
    concurrency,
    outputLocation: outFile,
    overwrite: true,
    enforceAudioTrack: true,
    chromiumOptions,
    timeoutInMilliseconds: frameTimeoutMs,
    logLevel: 'warn',
    cancelSignal,
    ...session.remotion,
    onProgress: ({ progress: p, renderedFrames, encodedFrames, stitchStage }) => {
      if (renderedFrames !== lastRendered) {
        lastRendered = renderedFrames;
        frames.tick();
      }
      const stage = renderedFrames < total ? 'rendering' : stitchStage === 'muxing' ? 'muxing' : 'encoding';
      emitProgress(
        { type: 'progress', progress: Math.round(p * 1000) / 1000, renderedFrames, encodedFrames, stage },
        p >= 1,
      );
    },
  });

  frames.stop();
  dog.stop();
  const blocked = session.blocked();
  await session.close();
  if (cancelled) throw new Error('render cancelled');
  if (blocked.length) emit({ type: 'blocked', hosts: blocked });
  emit({ type: 'done', output: outFile, durationInFrames: total, motion: session.bundles.map((b) => ({ hash: b.hash, bundleBytes: b.bytes })) });
};

main().then(
  () => process.exit(0),
  (err) => fail(err),
);
