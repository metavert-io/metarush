#!/usr/bin/env node
// Render one frame of a plan to PNG (QA frame inspection).
//
//   node render/still.mjs --props <file.json> --frame <n> --out <file.png>
//        [--scale 1] [--bundle-cache <dir>] [--motion <sha256>=<archive>]… [--frame-timeout-ms N]
//
// stdout: JSON lines only: versions, then done {output, frame} | error.
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
  versionsLine,
} from './shared.mjs';
import { openSession } from './motion/session.mjs';

const main = async () => {
  emit(versionsLine());
  let args;
  try {
    ({ values: args } = parseArgs({
      options: {
        props: { type: 'string' },
        frame: { type: 'string' },
        out: { type: 'string' },
        scale: { type: 'string' },
        'bundle-cache': { type: 'string' },
        motion: { type: 'string', multiple: true },
        'frame-timeout-ms': { type: 'string' },
      },
      strict: true,
    }));
  } catch (e) {
    throw new UsageError(e.message);
  }
  if (!args.props) throw new UsageError('--props <file.json> is required');
  if (!args.out) throw new UsageError('--out <file.png> is required');
  if (args.frame === undefined) throw new UsageError('--frame <n> is required');

  const propsFile = path.resolve(args.props);
  const outFile = path.resolve(args.out);
  const cacheDir = resolveCacheDir(args['bundle-cache']);
  const frame = Number(args.frame);
  const scale = args.scale === undefined ? 1 : Number(args.scale);
  if (!(scale > 0 && scale <= 4)) throw new UsageError(`--scale must be in (0, 4], got ${args.scale}`);

  const motion = parseMotionArgs(args.motion);
  const frameTimeoutMs = intFlag(args['frame-timeout-ms'], 120000, 'frame-timeout-ms', 7000);
  const props = loadProps(propsFile);
  if (!Number.isInteger(frame) || frame < 0 || frame >= props.plan.durationInFrames) {
    throw new UsageError(`--frame must be an integer in [0, ${props.plan.durationInFrames - 1}], got ${args.frame}`);
  }
  fs.mkdirSync(path.dirname(outFile), { recursive: true });
  pinCwd();

  await ensureBrowserInstalled();
  const { serveUrl } = await getBundle({ cacheDir });
  const { selectComposition, renderStill } = await import('@remotion/renderer');
  const session = await openSession({ plan: props.plan, motion, chromiumOptions, log });
  const composition = await selectComposition({
    serveUrl,
    id: COMPOSITION_ID,
    inputProps: props,
    chromiumOptions,
    logLevel: 'warn',
    timeoutInMilliseconds: frameTimeoutMs,
    ...session.remotion,
  });
  await renderStill({
    composition,
    serveUrl,
    inputProps: props,
    frame,
    output: outFile,
    imageFormat: 'png',
    scale,
    overwrite: true,
    chromiumOptions,
    timeoutInMilliseconds: frameTimeoutMs,
    logLevel: 'warn',
    ...session.remotion,
  });
  const blocked = session.blocked();
  await session.close();
  if (blocked.length) emit({ type: 'blocked', hosts: blocked });
  emit({ type: 'done', output: outFile, frame });
};

main().then(
  () => process.exit(0),
  (err) => fail(err),
);
