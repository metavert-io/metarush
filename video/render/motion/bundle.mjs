// Bundles a motion graphic for the render page (D68): MetaRush's own esbuild
// configuration, a browser target, no loaders or plugins from the component.
// React, Remotion and @metarush/motion come from the page (so the component
// shares the renderer's instances); the curated packages are bundled from
// MetaRush's pinned copies; nothing else resolves. The Node side never runs
// the result — it is served to the headless browser as an opaque file.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import * as esbuild from 'esbuild';
import { SHARED, classify } from './policy.mjs';

const VIDEO_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
export const DEFAULT_MAX_BUNDLE_BYTES = 2 * 1024 * 1024;

const inside = (root, p) => p === root || p.startsWith(root + path.sep);

// Registers the component with the page. Written by MetaRush, not the component.
const entrySource = (hash) => `
import 'metarush:setup';
import * as M from './index.tsx';
import { toJSONSchema } from 'zod';
const host = globalThis.__metarushMotion;
if (!host || typeof host.register !== 'function') throw new Error('the render page has no motion graphic host');
host.register(${JSON.stringify(hash)}, {
  component: M.default,
  schema: M.schema,
  meta: M.meta,
  exports: Object.keys(M),
  jsonSchema() {
    return toJSONSchema(M.schema, { target: 'draft-2020-12', unrepresentable: 'any', io: 'input' });
  },
  parse(params) {
    if (!M.schema || typeof M.schema.safeParse !== 'function') return { success: false, error: 'schema is not a zod schema' };
    const r = M.schema.safeParse(params);
    return r.success ? { success: true, data: r.data } : { success: false, error: r.error.issues.map((i) => (i.path.join('.') || 'params') + ': ' + i.message).join('; ') };
  },
});
`;

const allowlist = (root, libs = {}) => ({
  name: 'metarush-motion-allowlist',
  setup(build) {
    // zod without its eval-based fast path (the page allows no eval); set
    // before the component's module creates its schema.
    build.onResolve({ filter: /^metarush:setup$/ }, () => ({ path: 'setup', namespace: 'metarush-setup' }));
    build.onLoad({ filter: /.*/, namespace: 'metarush-setup' }, () => ({ contents: "import { config } from 'zod'; config({ jitless: true });", loader: 'js', resolveDir: VIDEO_DIR }));
    build.onResolve({ filter: /.*/ }, async (args) => {
      if (args.pluginData?.metarushInner) return undefined;
      if (args.namespace === 'metarush-setup') return undefined;
      if (SHARED.includes(args.path)) return { path: args.path, namespace: 'metarush-shared' };
      // The component's own files and its shared libraries' files (D171) are held to the allowlist.
      const libRoot = Object.values(libs).find((r) => args.importer && inside(r, args.importer));
      const fromComponent = args.importer === '' || args.importer === '<stdin>' || inside(root, args.importer) || !!libRoot;
      if (!fromComponent) return undefined; // MetaRush's pinned packages resolve normally
      if (args.kind !== 'import-statement' && args.kind !== 'entry-point') {
        return { errors: [{ text: `${args.path}: only static imports are allowed (${args.kind})` }] };
      }
      const c = classify(args.path);
      if (c.problem) return { errors: [{ text: c.problem }] };
      if (c.kind === 'library') {
        if (libRoot) return { errors: [{ text: `${args.path}: a shared library can't use another shared library` }] };
        const r = libs[c.name];
        if (!r) return { errors: [{ text: `${args.path}: the render was not given this shared library` }] };
        return { path: path.join(r, 'index.tsx') };
      }
      if (c.kind === 'relative') {
        const base = args.resolveDir || root;
        const r = await build.resolve(args.path, { resolveDir: base, kind: args.kind, importer: args.importer, pluginData: { metarushInner: true } });
        if (r.errors.length) return { errors: r.errors };
        const within = libRoot ?? root;
        if (!inside(within, r.path)) return { errors: [{ text: `${args.path} points outside the ${libRoot ? 'library' : "component"}'s folder` }] };
        return { path: r.path };
      }
      const r = await build.resolve(args.path, { resolveDir: VIDEO_DIR, kind: args.kind, pluginData: { metarushInner: true } });
      if (r.errors.length) return { errors: r.errors };
      return { path: r.path };
    });
    build.onLoad({ filter: /.*/, namespace: 'metarush-shared' }, (args) => ({
      contents: `const m = globalThis.__metarushMotion && globalThis.__metarushMotion.modules[${JSON.stringify(args.path)}];
if (!m) throw new Error(${JSON.stringify('the render page does not provide ' + args.path)});
module.exports = m;`,
      loader: 'js',
    }));
  },
});

/**
 * Bundles the extracted component in dir. Returns {code, bytes} or throws an
 * Error whose message lists the problems in plain words.
 */
export const bundleComponent = async ({ dir, hash, maxBytes = DEFAULT_MAX_BUNDLE_BYTES, libs = {} }) => {
  const root = fs.realpathSync(dir);
  const libRoots = Object.fromEntries(Object.entries(libs).map(([n, d]) => [n, fs.realpathSync(d)]));
  let result;
  try {
    result = await esbuild.build({
      stdin: { contents: entrySource(hash), resolveDir: root, sourcefile: 'metarush-motion-entry.tsx', loader: 'tsx' },
      absWorkingDir: root,
      bundle: true,
      write: false,
      format: 'iife',
      platform: 'browser',
      target: ['chrome120'],
      jsx: 'automatic',
      jsxImportSource: 'react',
      minify: true,
      legalComments: 'none',
      charset: 'utf8',
      sourcemap: false,
      define: { 'process.env.NODE_ENV': '"production"' },
      loader: { '.ts': 'ts', '.tsx': 'tsx', '.json': 'json', '.js': 'js', '.mjs': 'js', '.cjs': 'js' },
      resolveExtensions: ['.tsx', '.ts', '.mjs', '.js', '.cjs', '.json'],
      mainFields: ['browser', 'module', 'main'],
      conditions: ['browser', 'import', 'module', 'default'],
      tsconfigRaw: { compilerOptions: { jsx: 'react-jsx', jsxImportSource: 'react', useDefineForClassFields: true } },
      plugins: [allowlist(root, libRoots)],
      logLevel: 'silent',
      metafile: true,
    });
  } catch (e) {
    const msgs = (e.errors ?? []).map((x) => (x.location ? `${x.location.file}:${x.location.line}: ` : '') + x.text);
    throw new Error(msgs.length ? `the motion graphic can't be bundled: ${msgs.join('; ')}` : `the motion graphic can't be bundled: ${e.message}`);
  }
  const out = result.outputFiles?.[0];
  if (!out) throw new Error('the motion graphic bundled to nothing');
  const bytes = out.contents.length;
  if (bytes > maxBytes) {
    const size = (n) => (n >= 1048576 ? `${(n / 1048576).toFixed(1)} MB` : `${Math.round(n / 1024)} KB`);
    throw Object.assign(new Error(`the motion graphic's bundle is ${size(bytes)}; the limit is ${size(maxBytes)}`), { code: 'bundle_too_big' });
  }
  return { code: out.text, bytes, inputs: Object.keys(result.metafile?.inputs ?? {}) };
};
