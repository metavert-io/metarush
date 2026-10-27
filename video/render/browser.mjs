#!/usr/bin/env node
// Finishes a renderer install (spawned by `metarush renderer install`, see
// internal/renderer): downloads Remotion's Chrome Headless Shell when it is
// missing and proves the package loads (Remotion, the template catalog).
//
//   node render/browser.mjs
//
// stdout: JSON lines only, in order: versions, progress (only while the
// browser downloads), then {"type":"done","downloaded":bool} or error. Logs go
// to stderr. The browser lives under video/node_modules/.remotion (Remotion
// resolves it from the nearest package.json to the cwd, so we pin the cwd).
import { emit, fail, log, pinCwd, throttle, versionsLine } from './shared.mjs';

const main = async () => {
  emit(versionsLine());
  pinCwd();
  const { ensureBrowser } = await import('@remotion/renderer');
  let downloaded = false;
  const progress = throttle((p) => emit(p), 500);
  await ensureBrowser({
    logLevel: 'warn',
    onBrowserDownload: () => {
      downloaded = true;
      log('Chrome Headless Shell missing; downloading (one time) ...');
      progress({ type: 'progress', stage: 'browser', progress: 0 }, true);
      return {
        version: null,
        onProgress: ({ percent, downloadedBytes, totalSizeInBytes }) => {
          progress(
            { type: 'progress', stage: 'browser', progress: Math.round(percent * 1000) / 1000, downloadedBytes, totalBytes: totalSizeInBytes },
            percent >= 1,
          );
        },
      };
    },
  });
  emit({ type: 'done', downloaded });
};

main().then(
  () => process.exit(0),
  (err) => fail(err),
);
