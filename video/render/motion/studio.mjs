// `metarush motion studio`: preview a motion graphic in Remotion Studio, in
// the person's own browser on their own machine (D68: this runs the code
// locally, outside the render jail, so it is for the author's own components
// or ones a person has read). The component is scanned first and bundled with
// the same allowlist as renders; edits rebuild the bundle (reload the page).
import { spawn } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { bundleComponent } from './bundle.mjs';
import { scanFiles } from './scan.mjs';

const VIDEO_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

const readFolder = (dir) => {
  const out = [];
  const walk = (d) => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      if (e.name.startsWith('.')) continue;
      const p = path.join(d, e.name);
      if (e.isDirectory()) walk(p);
      else if (/\.(tsx|ts|json)$/.test(e.name)) out.push({ path: path.relative(dir, p).split(path.sep).join('/'), text: fs.readFileSync(p, 'utf8') });
    }
  };
  walk(dir);
  return out;
};

/**
 * Starts the bundle server and Remotion Studio; resolves when Studio exits.
 * emit receives JSON-line events (ready, rebuilt, problems).
 */
export const studio = async ({ dir, name, port, base, emit, log }) => {
  const root = fs.realpathSync(dir);
  const hash = crypto.createHash('sha256').update(`metarush-studio:${root}`).digest('hex');
  let code = '';
  const build = async () => {
    const files = readFolder(root);
    const scan = scanFiles(files);
    const errors = scan.problems.filter((p) => p.severity === 'error');
    if (errors.length) {
      emit({ type: 'problems', problems: errors });
      return false;
    }
    try {
      code = (await bundleComponent({ dir: root, hash })).code;
      return true;
    } catch (e) {
      emit({ type: 'problems', problems: [{ severity: 'error', code: 'bundle', file: 'index.tsx', message: e.message }] });
      return false;
    }
  };
  if (!(await build())) return 2;
  const token = crypto.randomBytes(16).toString('hex');
  const server = http.createServer((req, res) => {
    if (new URL(req.url, 'http://x').pathname !== `/${token}/${hash}.js`) {
      res.writeHead(404).end();
      return;
    }
    res.writeHead(200, { 'Content-Type': 'text/javascript; charset=utf-8', 'Cache-Control': 'no-store', 'Access-Control-Allow-Origin': '*' }).end(code);
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const url = `http://127.0.0.1:${server.address().port}/${token}/${hash}.js`;
  let preview = {};
  try {
    preview = JSON.parse(fs.readFileSync(path.join(root, 'preview.json'), 'utf8'));
  } catch {
    // no sample parameters: the component's own defaults
  }
  const fps = base.fps ?? 30;
  const total = fps * 5;
  const plan = {
    schemaVersion: 1, revisionId: 'motion-studio', projectId: '', specHash: hash, media: 'original', width: base.width, height: base.height, fps,
    durationInFrames: total, background: base.background ?? '#101014', brand: base.brand, safeArea: base.safeArea, clips: [], audio: [], dialogueRanges: [],
    overlays: [{ id: 'preview', template: '', templateVersion: '', from: Math.round(fps * 0.5), durationInFrames: total - fps, params: preview, motion: { name, hash, region: 'center' } }],
    sources: {}, motionSources: { [hash]: url },
  };
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'metarush-motion-studio-'));
  const props = path.join(tmp, 'props.json');
  fs.writeFileSync(props, JSON.stringify({ plan }));
  let timer = null;
  const watcher = fs.watch(root, { recursive: true }, () => {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      if (await build()) emit({ type: 'rebuilt' });
    }, 300);
  });
  const cli = path.join(path.dirname(fileURLToPath(import.meta.resolve('@remotion/cli/package.json'))), 'remotion-cli.js');
  const child = spawn(process.execPath, [cli, 'studio', 'src/index.ts', '--port', String(port), '--props', props], { cwd: VIDEO_DIR, stdio: ['ignore', 'inherit', 'inherit'] });
  emit({ type: 'ready', url: `http://localhost:${port}`, composition: 'MetaRushEdit' });
  log(`Remotion Studio: http://localhost:${port} (composition MetaRushEdit). Edit the files and reload the page; Ctrl-C stops it.`);
  const stop = () => child.kill('SIGTERM');
  process.on('SIGINT', stop);
  process.on('SIGTERM', stop);
  const code_ = await new Promise((r) => child.on('close', r));
  watcher.close();
  server.close();
  fs.rmSync(tmp, { recursive: true, force: true });
  return code_ ?? 0;
};
