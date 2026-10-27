#!/usr/bin/env node
// Reads a Remotion source without running it (D132): the TypeScript compiler
// parses every code file (MetaRush's own copy, never the project's), and this
// script walks the trees. Nothing here imports, requires or evaluates project
// code.
//
//   node render/source/analyze.mjs read --work <dir>
//   node render/source/analyze.mjs typecheck --work <dir>   (needs <dir>/node_modules: the source's packages)
//   node render/source/analyze.mjs map --work <dir>
//   node render/source/analyze.mjs words-setup --work <dir> [--file src/story.ts]...
//   node render/source/analyze.mjs words-bake --work <dir> [--options <file.json>]   ({values: {"END.tagline": "…"}})
//   node render/source/analyze.mjs pictures-setup --work <dir> [--name HERO]...
//   node render/source/analyze.mjs bake --work <dir> [--options <file.json>]   (both helpers out: words, then pictures)
//   node render/source/analyze.mjs lift --work <dir> --options <file.json> (or --component <Name> [--from <file>])
//
// read: entry point, compositions as written (ids, sizes, frame rates and
// lengths written as numbers or constants; calculateMetadata and other
// computed values marked), each code file's imports, the Google Fonts it
// loads, the public/ files it names, remotion.config.ts settings (as text),
// problems (inline webpack loaders, imports from web addresses), the files
// that hold its on-screen words and, once it has them, its word settings; the
// pictures it draws (public/ files through staticFile, imported picture files)
// and, once they are settings, their names (D209).
// typecheck: TypeScript's problems in the project's own files, with the
// project's compiler settings (plugins dropped) and its packages' types.
// map: the project map's facts from the code (map.mjs, D167).
// words-setup: the one-time change that makes its words settings (words.mjs, D168).
// words-bake: that change taken out again, the settings' words written in when given (words.mjs, D176).
// pictures-setup: the one-time change that makes its pictures settings (pictures.mjs, D209).
// bake: both changes taken out again, for sending changes back (D176, D209).
// lift: a scene's code as a motion graphic and a shared library (lift.mjs, D171).
// stdout: one JSON line ({type: "read" | "types" | "map" | "words-setup" | "words-bake" | "pictures-setup" | "bake" | "lift", ...} or {type: "error"}).
import fs from 'node:fs';
import path from 'node:path';
import { parseArgs } from 'node:util';
import ts from 'typescript';
import { liftScene } from './lift.mjs';
import { mapFacts } from './map.mjs';
import { bakePictures, pictureNamesByFile, scanPictures, setupPictures } from './pictures.mjs';
import { listCode, literalOf, lineOf, parseProject, stringOf } from './project.mjs';
import { bakeWords, copyFiles, settingsOf, setupWords, usesOf } from './words.mjs';

const emit = (o) => process.stdout.write(JSON.stringify(o) + '\n');

/** The compositions as written, the config, the entry point and the problems (D132). */
const readProject = (root, P) => {
  const { files, sources, problems, compositions } = P;
  const out = compositions.map((c) => {
    const id = stringOf(c.attrs.id);
    const num = (k) => (c.attrs[k] === undefined ? undefined : P.numberOf(c.file, c.attrs[k]));
    const width = num('width');
    const height = num('height');
    const fps = num('fps');
    const duration = c.still ? 1 : num('durationInFrames');
    let defaultProps;
    if (c.attrs.defaultProps) {
      const lit = literalOf(ts.isJsxExpression(c.attrs.defaultProps) ? c.attrs.defaultProps.expression : c.attrs.defaultProps);
      if (lit !== undefined) defaultProps = lit;
    }
    const compNode = c.attrs.component ?? c.attrs.lazyComponent;
    const comp = P.componentFile(c.file, compNode);
    let componentName = '';
    let n = compNode;
    while (n && (ts.isJsxExpression(n) || ts.isParenthesizedExpression(n))) n = n.expression;
    if (n && ts.isIdentifier(n)) componentName = n.text;
    const computed = !!c.attrs.calculateMetadata || c.spread || [width, height, c.still ? 1 : fps, duration].some((v) => v === undefined);
    // Where its length is written (the map's "length set by", D167).
    const dAttr = c.attrs.durationInFrames;
    const dWhere = dAttr ? P.numberWhere(c.file, dAttr) : undefined;
    const dPlace = dAttr && !dWhere ? P.placeOf(c.file, dAttr) : null;
    let durationAt = dAttr ? { text: dAttr.getText(c.sf).replace(/^\{|\}$/g, '').slice(0, 200), frames: dWhere?.value ?? null, file: dWhere?.file ?? dPlace?.file ?? c.file, line: dWhere?.line ?? dPlace?.line ?? lineOf(c.sf, dAttr) } : null;
    if (c.attrs.calculateMetadata) {
      // Its props work its length out (calculateMetadata): that is what sets it.
      const cm = c.attrs.calculateMetadata;
      durationAt = { text: 'calculateMetadata', frames: null, file: c.file, line: lineOf(c.sf, cm) };
    }
    return {
      id: id ?? '',
      width: width ?? 0,
      height: height ?? 0,
      fps: c.still ? 0 : (fps ?? 0),
      durationInFrames: duration ?? 0,
      folder: c.folder,
      still: c.still,
      computed,
      defaultProps: defaultProps === undefined ? '' : JSON.stringify(defaultProps),
      file: c.file,
      line: c.line,
      component: comp,
      componentName,
      files: P.closure(comp),
      durationAt,
      calculateMetadata: !!c.attrs.calculateMetadata,
    };
  });
  for (const c of out) {
    if (!c.id) problems.push({ severity: 'warning', code: 'composition_id', file: c.file, line: c.line, message: "A composition's id is computed in code; MetaRush learns it when it reads the project in its sandbox." });
  }

  // remotion.config.ts (read as text; never run).
  const config = [];
  for (const name of ['remotion.config.ts', 'remotion.config.js', 'remotion.config.mjs', 'remotion.config.mts']) {
    const p = path.join(root, name);
    if (!fs.existsSync(p)) continue;
    const kind = name.endsWith('.ts') || name.endsWith('.mts') ? ts.ScriptKind.TS : ts.ScriptKind.JS;
    const sf = ts.createSourceFile(name, fs.readFileSync(p, 'utf8'), ts.ScriptTarget.Latest, true, kind);
    const walk = (node) => {
      if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression) && ts.isIdentifier(node.expression.expression) && node.expression.expression.text === 'Config') {
        const setting = node.expression.name.text;
        const lit = node.arguments.length === 1 ? literalOf(node.arguments[0]) : undefined;
        config.push({ setting, value: lit === undefined ? (node.arguments.length ? '(code)' : '') : JSON.stringify(lit), line: lineOf(sf, node), literal: lit !== undefined });
      }
      ts.forEachChild(node, walk);
    };
    walk(sf);
    break;
  }

  // The entry point: the conventional places, then any file calling registerRoot.
  const calls = (f) => /\bregisterRoot\s*\(/.test(sources.get(f)?.text ?? '');
  const entryFromConfig = config.find((c) => c.setting === 'setEntryPoint' && c.literal);
  const conventional = ['src/index.ts', 'src/index.tsx', 'src/index.js', 'src/index.jsx', 'remotion/index.ts', 'remotion/index.tsx', 'src/remotion/index.ts', 'src/remotion/index.tsx', 'index.ts', 'index.tsx'];
  let entry = '';
  if (entryFromConfig) {
    const e = JSON.parse(entryFromConfig.value).replace(/^\.\//, '');
    if (files.has(e) && calls(e)) entry = e;
  }
  if (!entry) entry = conventional.find((f) => files.has(f) && calls(f)) ?? P.list.find(calls) ?? '';
  if (!entry) problems.push({ severity: 'error', code: 'no_entry', message: "MetaRush couldn't find the file that registers the project's compositions (registerRoot).", hint: 'Remotion projects usually have it in src/index.ts.' });
  return { entry, compositions: out, config: config.map(({ literal, ...c }) => c) };
};

const read = (root) => {
  const P = parseProject(root);
  const r = readProject(root, P);
  const { helper, settings } = settingsOf(P);
  const copy = copyFiles(P);
  // The words each composition reads (paths into the copy files' constants, D169).
  const uses = usesOf(P, new Set(copy.map((c) => c.file)));
  for (const c of r.compositions) c.words = [...new Set(c.files.flatMap((f) => uses[f] ?? []))].sort();
  // The pictures it draws (D209), and which of them each composition draws (by name).
  const pics = scanPictures(P);
  const picsOf = pictureNamesByFile(pics);
  for (const c of r.compositions) c.pictures = [...new Set(c.files.flatMap((f) => picsOf[f] ?? []))].sort();
  return {
    type: 'read',
    entry: r.entry,
    compositions: r.compositions,
    imports: P.imports,
    fonts: [...P.fonts.values()].sort((a, b) => a.module.localeCompare(b.module)),
    staticFiles: [...P.staticFiles].sort(),
    config: r.config,
    problems: P.problems,
    // Words as settings (D168): the files that hold its words, and its settings once it has them.
    copy: copy.map((c) => ({ file: c.file, words: c.words, consts: c.consts.map((k) => k.name), wrapped: c.wrapped })),
    wordsHelper: helper,
    settings: settings.map(({ wordy, ...s }) => ({ ...s, onScreen: wordy })),
    // Pictures as settings (D209).
    pictures: pics.pictures,
    picturesLeft: pics.left,
    picturesHelper: pics.helper,
  };
};

/** Both helpers taken out (D176, D209): words first, then pictures on what that left. */
const bakeAll = (root, values) => {
  const P = parseProject(root);
  const w = bakeWords(P, values);
  const texts = new Map(w.files.map((f) => [f.path, f.text]));
  const p = bakePictures(parseProject(root, { texts, removed: new Set(w.removed) }));
  for (const f of p.files) texts.set(f.path, f.text);
  return { files: [...texts].map(([path, text]) => ({ path, text })), removed: [...w.removed, ...p.removed], problems: [...w.problems, ...p.problems] };
};

const typecheck = (root) => {
  const cfgPath = ['tsconfig.json'].map((n) => path.join(root, n)).find((p) => fs.existsSync(p));
  let options = { noEmit: true, jsx: ts.JsxEmit.ReactJSX, strict: true, skipLibCheck: true, module: ts.ModuleKind.ESNext, moduleResolution: ts.ModuleResolutionKind.Bundler, target: ts.ScriptTarget.ES2022, esModuleInterop: true, resolveJsonModule: true };
  let rootNames = listCode(root).filter((f) => /\.(tsx?|mts|cts)$/.test(f)).map((f) => path.join(root, f));
  if (cfgPath) {
    const raw = ts.readConfigFile(cfgPath, ts.sys.readFile);
    const cfg = raw.config ?? {};
    if (cfg.compilerOptions) delete cfg.compilerOptions.plugins; // language-service plugins: never loaded
    const parsed = ts.parseJsonConfigFileContent(cfg, ts.sys, root);
    options = { ...parsed.options, noEmit: true, skipLibCheck: true, incremental: false, composite: false, tsBuildInfoFile: undefined };
    if (parsed.fileNames.length) rootNames = parsed.fileNames.filter((f) => !f.includes(`${path.sep}node_modules${path.sep}`) && !f.includes(`${path.sep}.metarush-probe${path.sep}`));
  }
  delete options.plugins;
  const program = ts.createProgram({ rootNames, options });
  const real = fs.realpathSync(root);
  const problems = [];
  for (const d of ts.getPreEmitDiagnostics(program)) {
    if (!d.file) continue;
    let fn = d.file.fileName;
    try {
      fn = fs.realpathSync(fn);
    } catch {
      // generated by TypeScript
    }
    if (!fn.startsWith(real + path.sep) || fn.includes(`${path.sep}node_modules${path.sep}`)) continue;
    const { line } = d.file.getLineAndCharacterOfPosition(d.start ?? 0);
    problems.push({
      severity: d.category === ts.DiagnosticCategory.Error ? 'error' : 'warning',
      code: 'type',
      file: path.relative(real, fn).split(path.sep).join('/'),
      line: line + 1,
      message: ts.flattenDiagnosticMessageText(d.messageText, ' ').slice(0, 500),
    });
    if (problems.length >= 200) break;
  }
  return { type: 'types', problems, typescript: ts.version };
};

const map = (root) => {
  const P = parseProject(root);
  const r = readProject(root, P);
  return mapFacts(P, r.compositions);
};

const main = () => {
  const [cmd, ...rest] = process.argv.slice(2);
  const { values } = parseArgs({
    args: rest,
    options: { work: { type: 'string' }, file: { type: 'string', multiple: true }, name: { type: 'string', multiple: true }, component: { type: 'string' }, from: { type: 'string' }, options: { type: 'string' } },
    strict: true,
  });
  if (!values.work) throw new Error('--work <dir> is required');
  const root = path.resolve(values.work);
  if (cmd === 'read') emit(read(root));
  else if (cmd === 'typecheck') emit(typecheck(root));
  else if (cmd === 'map') emit(map(root));
  else if (cmd === 'words-setup') emit({ type: 'words-setup', ...setupWords(parseProject(root), values.file ?? []) });
  else if (cmd === 'words-bake') {
    const o = values.options ? JSON.parse(fs.readFileSync(path.resolve(values.options), 'utf8')) : {};
    emit({ type: 'words-bake', ...bakeWords(parseProject(root), o.values ?? {}) });
  } else if (cmd === 'pictures-setup') emit({ type: 'pictures-setup', ...setupPictures(parseProject(root), values.name ?? []) });
  else if (cmd === 'bake') {
    const o = values.options ? JSON.parse(fs.readFileSync(path.resolve(values.options), 'utf8')) : {};
    emit({ type: 'bake', ...bakeAll(root, o.values ?? {}) });
  }
  else if (cmd === 'lift') {
    const o = values.options ? JSON.parse(fs.readFileSync(path.resolve(values.options), 'utf8')) : {};
    if (values.component) o.component = values.component;
    if (values.from) o.from = values.from;
    emit({ type: 'lift', ...liftScene(parseProject(root), o) });
  } else throw new Error('usage: analyze.mjs read|typecheck|map|words-setup|words-bake|pictures-setup|bake|lift --work <dir>');
};

try {
  main();
} catch (e) {
  emit({ type: 'error', message: e instanceof Error ? e.message : String(e) });
  process.exit(1);
}
