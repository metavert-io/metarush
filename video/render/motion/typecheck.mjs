// Type-checks a motion graphic with MetaRush's TypeScript settings
// (video/tsconfig.json). Allowed packages resolve to MetaRush's own pinned
// copies (as if imported from inside the video package) and @metarush/motion
// to its helper module; everything else was refused by the scan already.
import fs from 'node:fs';
import path from 'node:path';
import ts from 'typescript';
import { fileURLToPath } from 'node:url';
import { classify, TS_SETTINGS } from './policy.mjs';

const VIDEO_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

const API = path.join(VIDEO_DIR, 'src', 'motion', 'api.ts');
const FROM = path.join(VIDEO_DIR, 'src', 'index.ts');

const settings = () => {
  const cfgPath = path.join(VIDEO_DIR, 'tsconfig.json');
  const raw = ts.readConfigFile(cfgPath, ts.sys.readFile);
  const parsed = ts.parseJsonConfigFileContent(raw.config ?? {}, ts.sys, VIDEO_DIR);
  return { ...parsed.options, noEmit: true, skipLibCheck: true, types: [], allowJs: false };
};

/**
 * Returns diagnostics in the component's own files: [{file, line, col, message}].
 * libs: the shared libraries it imports (D171), {name: folder} — their files
 * resolve like the component's own, their problems are theirs (checked apart).
 * relax: TypeScript settings the component declares (policy.mjs tsSettingsOf;
 * only the relaxations TS_SETTINGS allows).
 */
export const typecheck = (dir, libs = {}, relax = {}) => {
  const root = fs.realpathSync(dir);
  const libRoots = Object.fromEntries(Object.entries(libs).map(([n, d]) => [n, fs.realpathSync(d)]));
  const files = [];
  const walk = (d) => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const p = path.join(d, e.name);
      if (e.isDirectory()) walk(p);
      else if (/\.(tsx|ts)$/.test(e.name)) files.push(p);
    }
  };
  walk(root);
  const options = settings();
  for (const [k, v] of Object.entries(relax ?? {})) if (TS_SETTINGS[k]?.includes(v)) options[k] = v;
  const host = ts.createCompilerHost(options, true);
  const inComponent = (f) => f.startsWith(root + path.sep);
  const inLib = (f) => Object.values(libRoots).some((r) => f.startsWith(r + path.sep));
  host.resolveModuleNameLiterals = (literals, containingFile, redirected, opts) =>
    literals.map((lit) => {
      const name = lit.text;
      if (!inComponent(containingFile) && !inLib(containingFile)) return ts.resolveModuleName(name, containingFile, opts, host, undefined, redirected);
      if (name === '@metarush/motion') return { resolvedModule: { resolvedFileName: API, extension: ts.Extension.Ts, isExternalLibraryImport: false } };
      const c = classify(name);
      if (c.kind === 'library') {
        const r = libRoots[c.name];
        return r ? { resolvedModule: { resolvedFileName: path.join(r, 'index.tsx'), extension: ts.Extension.Tsx, isExternalLibraryImport: false } } : { resolvedModule: undefined };
      }
      if (c.kind === 'relative') return ts.resolveModuleName(name, containingFile, opts, host, undefined, redirected);
      if (c.kind) return ts.resolveModuleName(name, FROM, opts, host, undefined, redirected);
      return { resolvedModule: undefined };
    });
  const program = ts.createProgram({ rootNames: files, options, host });
  const out = [];
  for (const d of ts.getPreEmitDiagnostics(program)) {
    if (!d.file || !inComponent(d.file.fileName)) continue;
    const { line, character } = d.file.getLineAndCharacterOfPosition(d.start ?? 0);
    out.push({
      severity: d.category === ts.DiagnosticCategory.Error ? 'error' : 'warning',
      code: 'type',
      file: path.relative(root, d.file.fileName).split(path.sep).join('/'),
      line: line + 1,
      col: character + 1,
      message: ts.flattenDiagnosticMessageText(d.messageText, ' '),
    });
  }
  return out;
};
