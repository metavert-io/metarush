// A scene lifted into a reusable motion graphic (D171): the scene's component
// becomes a graphic (its file, and an index.tsx MetaRush writes that draws it
// at its own size fitted to the frame, with its props as the graphic's
// parameters), and the files it shares with the rest of the project (theme,
// components, words) become a shared library (@metarush/lib/<library>) that
// every graphic lifted from the project imports — one approval per version of
// the theme. Google Fonts the code loads become the project's font resources,
// registered by the library's useThemeFonts(). Nothing here runs project
// code: it is read and rewritten as text with MetaRush's TypeScript parser.
import fs from 'node:fs';
import path from 'node:path';
import ts from 'typescript';
import { bare, lineOf, literalOf, propName, stringOf } from './project.mjs';
import { resolveTag } from './map.mjs';
import { HELPER_MARK } from './words.mjs';

/** Packages a motion graphic may import (video/render/motion/policy.mjs); Google Fonts are rewritten. */
const ALLOWED = new Set(['react', 'react/jsx-runtime', 'remotion', 'zod', '@remotion/zod-types', '@remotion/shapes', '@remotion/paths', '@remotion/layout-utils', '@remotion/transitions']);
const GOOGLE_FONT = /^@remotion\/google-fonts\/([A-Za-z0-9]+)$/;
const allowedSpec = (spec) => ALLOWED.has(spec) || spec.startsWith('@remotion/transitions/') || GOOGLE_FONT.test(spec);

const NAME = /^[a-z][a-z0-9-]{0,39}$/;
const slug = (s) =>
  String(s)
    .replace(/([a-z0-9])([A-Z])/g, '$1-$2')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .replace(/^[^a-z]+/, '')
    .slice(0, 40)
    .replace(/-+$/, '') || 'scene';

/**
 * The words helper as a shared library has it: the words as they were when it
 * was made — the code's, with the settings a person had changed in their place
 * (fixed; a motion graphic's own words are its parameters).
 */
export const libraryWordsHelper = (settings) => `// ${HELPER_MARK}, in a shared library: the words are as they were when
// MetaRush made it — the project's own, with the settings a person had changed
// in their place. A motion graphic's own words are its parameters.
type Settings = Record<string, string>;

const settings: Settings = ${JSON.stringify(settings ?? {}, null, 2)};

const put = (value: unknown, key: string): unknown => {
  if (typeof value === "string") return Object.prototype.hasOwnProperty.call(settings, key) ? settings[key] : value;
  if (Array.isArray(value)) return value.map((v, i) => put(v, \`\${key}.\${i}\`));
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value)) out[k] = put(v, \`\${key}.\${k}\`);
    return out;
  }
  return value;
};

export function words<T>(name: string, value: T): T {
  return put(value, name) as T;
}
`;

/** A literal value an expression resolves to without running anything (strings, numbers, booleans). */
const valueOf = (P, f, node, depth = 0) => {
  if (!node || depth > 10) return undefined;
  const n = bare(node.kind === ts.SyntaxKind.JsxExpression ? node.expression : node);
  if (!n) return undefined;
  const lit = literalOf(n);
  if (lit !== undefined && (typeof lit !== 'object' || lit === null)) return lit;
  if (ts.isIdentifier(n)) {
    const init = P.constInit(f, n.text);
    return init ? valueOf(P, init.file, init.node, depth + 1) : undefined;
  }
  if (ts.isPropertyAccessExpression(n)) {
    // A.b.c through constant objects (also inside words("A", {…}) wrappers).
    const chain = [];
    let e = n;
    while (ts.isPropertyAccessExpression(e)) {
      chain.unshift(e.name.text);
      e = bare(e.expression);
    }
    if (!ts.isIdentifier(e)) return undefined;
    const init = P.constInit(f, e.text);
    if (!init) return undefined;
    let obj = bare(init.node);
    if (ts.isCallExpression(obj) && ts.isIdentifier(obj.expression) && obj.expression.text === 'words' && obj.arguments[1]) {
      // A setting a person changed counts (words("END_V3", {…}) → END_V3.tagline).
      const key = [stringOf(obj.arguments[0]), ...chain].join('.');
      if (P.wordValues && Object.prototype.hasOwnProperty.call(P.wordValues, key)) return P.wordValues[key];
      obj = bare(obj.arguments[1]);
    }
    for (const k of chain) {
      if (!obj || !ts.isObjectLiteralExpression(obj)) return undefined;
      const p = obj.properties.find((x) => ts.isPropertyAssignment(x) && propName(x) === k);
      if (!p) return undefined;
      obj = bare(p.initializer);
    }
    return valueOf(P, init.file, obj, depth + 1);
  }
  return undefined;
};

/** The JSX a simple wrapper component returns (const X = () => <Y a="…" />), or null. */
const wrapperJSX = (node) => {
  let fn = node;
  if (ts.isVariableDeclaration(fn)) fn = fn.initializer ? bare(fn.initializer) : null;
  if (!fn || !(ts.isArrowFunction(fn) || ts.isFunctionExpression(fn) || ts.isFunctionDeclaration(fn))) return null;
  if (fn.parameters.length > 0) return null; // it takes props: it is the scene itself
  let body = fn.body;
  if (body && ts.isBlock(body)) {
    if (body.statements.length !== 1 || !ts.isReturnStatement(body.statements[0])) return null;
    body = body.statements[0].expression;
  }
  body = body ? bare(body) : null;
  return body && ts.isJsxSelfClosingElement(body) ? body : null;
};

/** The props a component declares: [{name, optional, kind, values?}], their defaults, and a zod schema it is typed with. */
const propsOf = (sf, decl) => {
  let typeNode = null;
  let fn = decl;
  if (ts.isVariableDeclaration(decl)) {
    if (decl.type && ts.isTypeReferenceNode(decl.type) && decl.type.typeArguments?.[0]) typeNode = decl.type.typeArguments[0];
    fn = decl.initializer ? bare(decl.initializer) : null;
    if (fn && ts.isCallExpression(fn) && fn.arguments[0]) fn = bare(fn.arguments[0]); // memo(…), forwardRef(…)
  }
  const param = fn && (ts.isArrowFunction(fn) || ts.isFunctionExpression(fn) || ts.isFunctionDeclaration(fn)) ? fn.parameters[0] : null;
  if (!typeNode && param?.type) typeNode = param.type;
  const defaults = {};
  if (param && ts.isObjectBindingPattern(param.name)) {
    for (const el of param.name.elements) {
      const k = el.propertyName ? (propName(el) ?? el.propertyName.getText(sf)) : el.name.getText(sf);
      if (el.initializer) defaults[k] = el.initializer;
    }
  }
  // z.infer<typeof schema>: the component's own zod schema.
  if (typeNode && ts.isTypeReferenceNode(typeNode) && /(^|\.)(infer|input|output)$/.test(typeNode.typeName.getText(sf)) && typeNode.typeArguments?.[0] && ts.isTypeQueryNode(typeNode.typeArguments[0])) {
    return { schemaName: typeNode.typeArguments[0].exprName.getText(sf), props: [], defaults };
  }
  // An interface or type alias in the same file.
  if (typeNode && ts.isTypeReferenceNode(typeNode)) {
    const n = typeNode.typeName.getText(sf);
    for (const st of sf.statements) {
      if ((ts.isInterfaceDeclaration(st) || ts.isTypeAliasDeclaration(st)) && st.name.text === n) {
        typeNode = ts.isInterfaceDeclaration(st) ? st : st.type;
        break;
      }
    }
  }
  const members = typeNode && (ts.isTypeLiteralNode(typeNode) || ts.isInterfaceDeclaration(typeNode)) ? typeNode.members : [];
  const props = [];
  for (const m of members) {
    if (!ts.isPropertySignature(m) || !m.type) continue;
    const name = propName(m);
    if (!name) continue;
    const t = m.type;
    let kind = '';
    let values;
    if (t.kind === ts.SyntaxKind.StringKeyword) kind = 'string';
    else if (t.kind === ts.SyntaxKind.NumberKeyword) kind = 'number';
    else if (t.kind === ts.SyntaxKind.BooleanKeyword) kind = 'boolean';
    else if (ts.isUnionTypeNode(t) && t.types.every((x) => ts.isLiteralTypeNode(x) && ts.isStringLiteral(x.literal))) {
      kind = 'enum';
      values = t.types.map((x) => x.literal.text);
    }
    props.push({ name, optional: !!m.questionToken, kind, values });
  }
  return { schemaName: '', props, defaults };
};

/** A Google Font module's own data (getInfo, read as text from the project's packages). */
const fontInfo = (root, family) => {
  const p = path.join(root, 'node_modules', '@remotion', 'google-fonts', 'dist', 'esm', `${family}.mjs`);
  let text;
  try {
    text = fs.readFileSync(p, 'utf8');
  } catch {
    return null;
  }
  const sf = ts.createSourceFile(p, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.JS);
  let info = null;
  const visit = (n) => {
    if (info) return;
    if (ts.isVariableDeclaration(n) && ts.isIdentifier(n.name) && n.name.text === 'getInfo' && n.initializer && ts.isArrowFunction(n.initializer)) {
      info = literalOf(bare(n.initializer.body)) ?? null;
      return;
    }
    ts.forEachChild(n, visit);
  };
  visit(sf);
  return info && typeof info === 'object' ? info : null;
};

/** The files a loadFont(style, {weights, subsets}) call loads. */
const fontFaces = (info, style, weights, subsets) => {
  const byStyle = info.fonts?.[style] ?? {};
  const ws = weights?.length ? weights.map(String) : Object.keys(byStyle);
  const faces = [];
  for (const w of ws) {
    const bySubset = byStyle[w] ?? {};
    const ss = subsets?.length ? subsets : Object.keys(bySubset);
    for (const s of ss) {
      const url = bySubset[s];
      if (typeof url !== 'string') continue;
      const m = /^https:\/\/fonts\.gstatic\.com\/(.+)$/.exec(url);
      if (!m) continue;
      faces.push({ weight: Number(w), style, subset: s, path: m[1], unicodeRange: info.unicodeRanges?.[s] ?? '' });
    }
  }
  return faces;
};

/** The text of a file with spans replaced ([{start, end, text}]). */
const applyEdits = (text, edits) => {
  let out = text;
  for (const e of [...edits].sort((a, b) => b.start - a.start)) out = out.slice(0, e.start) + e.text + out.slice(e.end);
  return out;
};

const stripExt = (p) => p.replace(/\.(tsx?)$/, '');

/**
 * The TypeScript settings lifted code is checked with: the project's own where
 * MetaRush's are stricter (motion/policy.mjs TS_SETTINGS) — a project without
 * noUncheckedIndexedAccess was written without it.
 */
const projectTypeScript = (root) => {
  let text;
  try {
    text = fs.readFileSync(path.join(root, 'tsconfig.json'), 'utf8');
  } catch {
    return { noUncheckedIndexedAccess: false };
  }
  const { config } = ts.parseConfigFileTextToJson('tsconfig.json', text);
  return config?.compilerOptions?.noUncheckedIndexedAccess === true ? {} : { noUncheckedIndexedAccess: false };
};

/** The component a composition draws (component={X}). */
const compositionComponent = (P, id) => {
  for (const c of P.compositions) {
    if (stringOf(c.attrs.id) !== id) continue;
    let n = c.attrs.component;
    while (n && (ts.isJsxExpression(n) || ts.isParenthesizedExpression(n))) n = n.expression;
    if (n && ts.isIdentifier(n)) {
      const d = resolveTag(P, c.file, n.text);
      if (d) return d;
    }
    return { problem: `The composition ${id} doesn't name its component plainly (component={…}), so MetaRush can't tell which code to lift.` };
  }
  return { problem: `MetaRush couldn't find the composition ${id} in the project's code.` };
};

/**
 * o: {component, from, composition, fonts: {path → resource name}, library,
 * name, source, sourceId, title, version, width, height, seconds, keep: [project paths]}.
 */
export const liftScene = (P, o) => {
  P.wordValues = o.words ?? {};
  const problems = [];
  const fail = (code, message, file, line) => {
    problems.push({ severity: 'error', code, message, file, line });
    return { ok: false, problems };
  };
  // 1. The component, through simple wrappers (EndV3 = () => <End tagline={…} />).
  let decl = null;
  if (o.component) {
    if (o.from) decl = P.declOf(o.from, o.component);
    if (!decl) {
      for (const [f, d] of P.declared) {
        const x = d.get(o.component);
        if (x && (!decl || (x.exported && !decl.exported))) decl = { file: f, name: o.component, ...x };
      }
    }
    if (!decl) return fail('no_component', `MetaRush couldn't find the component ${o.component} in the project's code.`);
  } else if (o.composition) {
    const d = compositionComponent(P, o.composition);
    if (d.problem) return fail('no_component', d.problem);
    decl = d;
  } else return fail('no_component', 'Say which composition or scene to lift.');
  let given = {};
  for (let depth = 0; depth < 3; depth++) {
    const jsx = wrapperJSX(decl.node);
    if (!jsx) break;
    const sfw = P.sources.get(decl.file);
    const inner = resolveTag(P, decl.file, jsx.tagName.getText(sfw));
    if (!inner) break;
    const vals = {};
    let all = true;
    for (const a of jsx.attributes.properties) {
      if (!ts.isJsxAttribute(a)) {
        all = false;
        break;
      }
      const v = a.initializer ? valueOf(P, decl.file, a.initializer) : true;
      if (v === undefined) {
        all = false;
        break;
      }
      vals[a.name.getText(sfw)] = v;
    }
    if (!all) break;
    given = vals; // the innermost wrapper's values are what the scene gets
    decl = inner;
  }
  const F = decl.file;
  const sfF = P.sources.get(F);
  const sceneName = decl.name;
  const { schemaName, props, defaults } = propsOf(sfF, decl.node);
  // 2. Its files: the scene's own file for the graphic; everything it shares
  // (and whatever the library already holds) for the library.
  const closure = P.closure(F);
  const libSet = new Set(closure.filter((f) => f !== F));
  for (const k of o.keep ?? []) if (P.files.has(k) && k !== F) for (const g of P.closure(k)) if (g !== F) libSet.add(g);
  const libFiles = [...libSet].sort();
  const jsonOf = (f) => (P.json.get(f) ?? []).filter(Boolean);
  const all = [F, ...libFiles];
  for (const f of all) {
    const sf = P.sources.get(f);
    if (!/\.(tsx|ts)$/.test(f)) return fail('not_typescript', `${f} isn't TypeScript; motion graphics are written in TypeScript (.tsx or .ts).`, f);
    for (const st of sf.statements) {
      if ((ts.isImportDeclaration(st) || ts.isExportDeclaration(st)) && st.moduleSpecifier && ts.isStringLiteral(st.moduleSpecifier)) {
        const spec = st.moduleSpecifier.text;
        if (!spec.startsWith('.') && !allowedSpec(spec)) problems.push({ severity: 'error', code: 'package', file: f, line: lineOf(sf, st), message: `It uses ${spec}, which motion graphics can't use.` });
      }
    }
    const visit = (n) => {
      if (ts.isCallExpression(n) && ts.isIdentifier(n.expression)) {
        const callee = n.expression.text;
        if (callee === 'staticFile') problems.push({ severity: 'error', code: 'project_files', file: f, line: lineOf(sf, n), message: "It draws pictures or plays sounds from the project's own files; a motion graphic draws with code and the project's resources." });
        if (callee === 'registerRoot') problems.push({ severity: 'error', code: 'root_file', file: f, line: lineOf(sf, n), message: "It is the project's starting file; lift a scene, not the whole project." });
      }
      if (ts.isJsxSelfClosingElement(n) || ts.isJsxOpeningElement(n)) {
        const tag = n.tagName.getText(sf);
        if (/^(Audio|Html5Audio|Video|OffthreadVideo|Html5Video|IFrame|Img)$/.test(tag)) problems.push({ severity: 'error', code: 'media', file: f, line: lineOf(sf, n), message: tag === 'Img' || tag === 'IFrame' ? 'It shows a picture or a page from a file; motion graphics draw pictures through the project’s resources.' : 'It plays a sound or a video; a motion graphic is a picture (its sound belongs to the cut).' });
        if (tag === 'Composition' || tag === 'Still') problems.push({ severity: 'error', code: 'root_file', file: f, line: lineOf(sf, n), message: "It lists the project's compositions; lift a scene, not the whole project." });
      }
      ts.forEachChild(n, visit);
    };
    visit(sf);
  }
  if (problems.length) return { ok: false, problems: problems.slice(0, 20) };
  // 3. Where the library's files go: their layout under their common folder.
  const allLib = [...libFiles, ...libFiles.flatMap(jsonOf)];
  let base = allLib.length ? path.posix.dirname(allLib[0]) : '';
  if (base === '.') base = '';
  for (const f of allLib) while (base && !f.startsWith(base + '/')) base = base.includes('/') ? path.posix.dirname(base) : '';
  const libPath = (f) => (base ? f.slice(base.length + 1) : f);
  for (const f of allLib) {
    const p = libPath(f);
    if (p.split('/').length > 4 || p.length > 128) return fail('too_deep', `${f} is nested too deeply to go into a shared library (at most four folders).`, f);
    if (p === 'index.tsx' || p === 'library.json' || p === 'metarush-fonts.tsx') return fail('name_taken', `${f} has a name the shared library needs for itself (${p}).`, f);
  }
  const libName = o.library && NAME.test(o.library) ? o.library : slug((o.source || 'project') + '-theme');
  const graphicName = o.name && NAME.test(o.name) ? o.name : slug(o.title || sceneName);
  const typescript = projectTypeScript(P.root);
  if (graphicName === libName) return fail('name_taken', `The graphic and the shared library can't both be called ${libName}.`);
  // 4. Google Fonts become the project's font resources.
  const fontFilesWanted = new Map(); // gstatic path → {family, style, weights, unicodeRange, subset}
  const edits = new Map(); // file → edits
  const editsOf = (f) => {
    if (!edits.has(f)) edits.set(f, []);
    return edits.get(f);
  };
  for (const f of all) {
    const sf = P.sources.get(f);
    const loaders = new Map(); // local loader name → font module family
    for (const st of sf.statements) {
      if (!ts.isImportDeclaration(st) || !ts.isStringLiteral(st.moduleSpecifier)) continue;
      const m = GOOGLE_FONT.exec(st.moduleSpecifier.text);
      if (!m) continue;
      const info = fontInfo(P.root, m[1]);
      if (!info) return fail('font', `MetaRush couldn't read the Google Font ${m[1]} the code loads.`, f, lineOf(sf, st));
      const family = info.fontFamily ?? m[1];
      const consts = [];
      const ic = st.importClause;
      if (ic?.name || (ic?.namedBindings && ts.isNamespaceImport(ic.namedBindings))) return fail('font', `It imports the Google Font ${m[1]} as a whole; MetaRush can turn only loadFont(…) and fontFamily into the project's fonts.`, f, lineOf(sf, st));
      const nb = ic?.namedBindings;
      if (nb && ts.isNamedImports(nb)) {
        for (const el of nb.elements) {
          const imported = (el.propertyName ?? el.name).text;
          if (imported === 'loadFont') loaders.set(el.name.text, { module: m[1], info, family });
          else if (imported === 'fontFamily') consts.push(`const ${el.name.text} = ${JSON.stringify(family)};`);
          else return fail('font', `It uses ${imported} from the Google Font ${m[1]}; MetaRush can turn only loadFont(…) and fontFamily into the project's fonts.`, f, lineOf(sf, st));
        }
      }
      editsOf(f).push({ start: st.getStart(sf), end: st.getEnd(), text: consts.join('\n') });
    }
    if (!loaders.size) continue;
    let bad = null;
    const visit = (n) => {
      if (bad) return;
      if (ts.isCallExpression(n) && ts.isIdentifier(n.expression) && loaders.has(n.expression.text)) {
        const L = loaders.get(n.expression.text);
        const style = n.arguments[0] ? stringOf(n.arguments[0]) : 'normal';
        const opts = n.arguments[1] ? literalOf(n.arguments[1]) : {};
        if (style === undefined || opts === undefined) {
          bad = { file: f, line: lineOf(sf, n), message: `It loads the Google Font ${L.module} with settings MetaRush can't read as written.` };
          return;
        }
        const faces = fontFaces(L.info, style, opts?.weights, opts?.subsets);
        if (!faces.length) {
          bad = { file: f, line: lineOf(sf, n), message: `The Google Font ${L.module} has no files for what the code asks for.` };
          return;
        }
        for (const fc of faces) {
          const cur = fontFilesWanted.get(fc.path) ?? { family: L.family, style: fc.style, weights: [], unicodeRange: fc.unicodeRange, subset: fc.subset };
          cur.weights.push(fc.weight);
          fontFilesWanted.set(fc.path, cur);
        }
        editsOf(f).push({ start: n.getStart(sf), end: n.getEnd(), text: `{ fontFamily: ${JSON.stringify(L.family)} }` });
        return;
      }
      ts.forEachChild(n, visit);
    };
    visit(sf);
    if (bad) return fail('font', bad.message, bad.file, bad.line);
  }
  const fonts = [...fontFilesWanted.entries()]
    .map(([p, v]) => ({ path: p, family: v.family, style: v.style, weights: [...new Set(v.weights)].sort((a, b) => a - b), unicodeRange: v.unicodeRange, subset: v.subset }))
    .sort((a, b) => a.path.localeCompare(b.path));
  const resourceOf = o.fonts ?? {}; // gstatic path → resource name (Go made them)
  const missingFonts = fonts.filter((ff) => !resourceOf[ff.path]);
  // 5. The library: its files as they are (fonts rewritten), and an index that exports them.
  const library = libFiles.length ? { name: libName, files: [], map: {} } : null;
  const indexLines = [];
  if (library) {
    const exportedBy = new Map(); // name → first library file that exports it
    for (const f of libFiles) {
      const sf = P.sources.get(f);
      const text = sf.text.includes(HELPER_MARK) ? libraryWordsHelper(o.words) : applyEdits(sf.text, edits.get(f) ?? []);
      library.files.push({ path: libPath(f), text });
      library.map[libPath(f)] = f;
      indexLines.push(`export * from ${JSON.stringify('./' + stripExt(libPath(f)))};`);
      for (const n of P.exported.get(f)?.keys() ?? []) {
        if (n === 'default') continue;
        if (exportedBy.has(n)) indexLines.push(`export { ${n} } from ${JSON.stringify('./' + stripExt(libPath(exportedBy.get(n))))};`);
        else exportedBy.set(n, f);
      }
    }
    for (const j of libFiles.flatMap(jsonOf)) {
      if (library.map[libPath(j)]) continue;
      library.files.push({ path: libPath(j), text: fs.readFileSync(path.join(P.root, j), 'utf8') });
      library.map[libPath(j)] = j;
    }
  }
  // 6. The graphic: the scene's file (imports of the library rewritten) and its index.
  const sceneFile = path.posix.basename(F);
  if (sceneFile === 'index.tsx' || sceneFile === 'preview.json' || sceneFile === 'motion.json') return fail('name_taken', `${F} is called ${sceneFile}; lift a scene from a file with a name of its own.`, F);
  const graphic = { name: graphicName, files: [] };
  {
    const ed = [...(edits.get(F) ?? [])];
    const defaultsFromLib = [];
    for (const st of sfF.statements) {
      if (!(ts.isImportDeclaration(st) || ts.isExportDeclaration(st)) || !st.moduleSpecifier || !ts.isStringLiteral(st.moduleSpecifier)) continue;
      const spec = st.moduleSpecifier.text;
      if (!spec.startsWith('.')) continue;
      const json = path.posix.normalize(path.posix.join(path.posix.dirname(F), spec));
      if (json.endsWith('.json')) continue; // data next to it comes along below
      const target = [...(P.bindings.get(F)?.values() ?? [])].find((b) => b.spec === spec)?.from ?? P.graph.get(F)?.find((g) => stripExt(g) === stripExt(json) || stripExt(g) === json + '/index');
      if (!target || !libSet.has(target)) continue;
      const lib = JSON.stringify('@metarush/lib/' + libName);
      if (ts.isExportDeclaration(st)) {
        ed.push({ start: st.getStart(sfF), end: st.getEnd(), text: st.getText(sfF).replace(st.moduleSpecifier.getText(sfF), lib) });
        continue;
      }
      const ic = st.importClause;
      if (!ic) continue; // a side-effect import: the library runs it
      let clause = ic.getText(sfF);
      if (ic.name) {
        // A default import from a library file: the library exports it under a name of its own.
        const alias = `default_${stripExt(libPath(target)).replace(/[^A-Za-z0-9]/g, '_')}`;
        defaultsFromLib.push(`export { default as ${alias} } from ${JSON.stringify('./' + stripExt(libPath(target)))};`);
        const rest = ic.namedBindings && ts.isNamedImports(ic.namedBindings) ? ic.namedBindings.elements.map((e) => e.getText(sfF)).join(', ') : '';
        if (ic.namedBindings && ts.isNamespaceImport(ic.namedBindings)) return fail('imports', `${F} imports ${spec} in a way MetaRush can't move into a shared library.`, F, lineOf(sfF, st));
        clause = `{ ${alias} as ${ic.name.text}${rest ? ', ' + rest : ''} }`;
      }
      ed.push({ start: st.getStart(sfF), end: st.getEnd(), text: `import ${clause} from ${lib};` });
    }
    if (defaultsFromLib.length && library) indexLines.push(...defaultsFromLib);
    graphic.files.push({ path: sceneFile, text: applyEdits(sfF.text, ed) });
    for (const j of jsonOf(F)) {
      const rel = path.posix.relative(path.posix.dirname(F), j);
      if (rel.startsWith('..')) return fail('outside', `${F} reads ${j}, outside its folder; lift a scene whose data sits next to it.`, F);
      graphic.files.push({ path: rel, text: fs.readFileSync(path.join(P.root, j), 'utf8') });
    }
  }
  if (library) {
    const uniq = [...new Set(indexLines)];
    if (fonts.length) uniq.push("export { useThemeFonts } from './metarush-fonts';");
    library.files.push({ path: 'index.tsx', text: `// The shared library of “${o.source || 'the project'}”: its theme, components and words,\n// made by MetaRush from the project's own files. Graphics import it as\n// @metarush/lib/${libName}; one approval per version covers every graphic that uses it.\n${uniq.join('\n')}\n` });
    library.files.push({
      path: 'library.json',
      text: JSON.stringify({ summary: `The theme of “${o.source || 'the project'}”: its colours, fonts, components and words, shared by the motion graphics lifted from it.`, ...(Object.keys(typescript).length ? { typescript } : {}), source: { id: o.sourceId || '', name: o.source || '' }, files: library.map }, null, 2) + '\n',
    });
    if (fonts.length) {
      const calls = fonts.map((ff) => {
        const desc = { style: ff.style, weight: ff.weights.length > 1 ? `${ff.weights[0]} ${ff.weights[ff.weights.length - 1]}` : String(ff.weights[0] ?? 400) };
        if (ff.unicodeRange) desc.unicodeRange = ff.unicodeRange;
        return `  useFont(${JSON.stringify(resourceOf[ff.path] || 'font')}, ${JSON.stringify(ff.family)}, ${JSON.stringify(desc)});`;
      });
      library.files.push({ path: 'metarush-fonts.tsx', text: `// The fonts this theme draws with: the project's font resources, made by MetaRush\n// from the Google Fonts its code loaded. A graphic calls useThemeFonts().\nimport { useFont } from '@metarush/motion';\n\nexport const useThemeFonts = (): void => {\n${calls.join('\n')}\n};\n` });
    }
    delete library.map;
  }
  // Its parameters: the scene's own props (with the values the composition gives them).
  const params = [];
  const preview = {};
  const zodOf = (p) => {
    const z = p.kind === 'string' ? 'z.string().max(400)' : p.kind === 'number' ? 'z.number()' : p.kind === 'boolean' ? 'z.boolean()' : p.kind === 'enum' ? `z.enum(${JSON.stringify(p.values)})` : '';
    return z ? z + (p.optional ? '.optional()' : '') : '';
  };
  for (const p of props) {
    const z = zodOf(p);
    if (!z) {
      if (!p.optional && given[p.name] === undefined) return fail('props', `${sceneName} needs ${p.name}, which a motion graphic can't take as a setting.`, F);
      continue;
    }
    params.push({ name: p.name, zod: z });
    let v = given[p.name];
    if (v === undefined && defaults[p.name]) v = valueOf(P, F, defaults[p.name]);
    if (v !== undefined) preview[p.name] = v;
  }
  const exp = P.exported.get(F);
  const byName = exp?.get(sceneName)?.local === sceneName || (exp?.has(sceneName) && !exp.get(sceneName).from);
  const byDefault = !byName && exp?.get('default')?.local === sceneName;
  if (!byName && !byDefault) return fail('not_exported', `${sceneName} isn't exported from ${F}, so a graphic can't draw it.`, F);
  if (schemaName && !exp?.has(schemaName)) return fail('not_exported', `${schemaName} isn't exported from ${F}.`, F);
  const W = Math.max(16, Math.round(o.width || 1920));
  const H = Math.max(16, Math.round(o.height || 1080));
  const seconds = Math.max(0.5, Number(o.seconds) || 5);
  const minD = Math.round(Math.max(0.5, seconds * 0.6) * 10) / 10;
  const maxD = Math.round(Math.min(60, Math.max(seconds * 2, minD + 1)) * 10) / 10;
  const schemaSrc = schemaName ? `export const schema = ${schemaName};` : `export const schema = z.object({\n${params.map((p) => `  ${JSON.stringify(p.name)}: ${p.zod},`).join('\n')}\n});`;
  const withFonts = !!(library && fonts.length);
  const local = stripExt('./' + sceneFile);
  const sceneImport = byName ? `import { ${sceneName}${schemaName ? `, ${schemaName}` : ''} } from ${JSON.stringify(local)};` : `import ${sceneName}${schemaName ? `, { ${schemaName} }` : ''} from ${JSON.stringify(local)};`;
  const what = `${o.title ? `the scene “${o.title}” of ` : ''}${o.composition ? `${o.composition} in ` : ''}the Remotion project “${o.source || ''}”`;
  const index = `// ${sceneName}, lifted from ${what} by MetaRush.
// It draws the scene at its own size (${W}×${H}), fitted to the frame;
// its parameters are the scene's own props.
import React from 'react';
import { AbsoluteFill, Sequence, useVideoConfig } from 'remotion';
import { z } from 'zod';
import type { MotionMeta, MotionProps } from '@metarush/motion';
${withFonts ? `import { useThemeFonts } from ${JSON.stringify('@metarush/lib/' + libName)};\n` : ''}${sceneImport}

${schemaSrc}

export const meta: MotionMeta = {
  summary: ${JSON.stringify(`${o.title || sceneName} from the Remotion project “${o.source || ''}”, drawn as it was made.`.slice(0, 200))},
  region: 'full',
  minDuration: ${minD},
  maxDuration: ${maxD},
};

const W = ${W};
const H = ${H};

export default function Lifted({ params }: MotionProps<typeof schema>) {
${withFonts ? '  useThemeFonts();\n' : ''}  const { width, height } = useVideoConfig();
  const k = Math.min(width / W, height / H);
  return (
    <AbsoluteFill style={{ alignItems: 'center', justifyContent: 'center', overflow: 'hidden' }}>
      <div style={{ position: 'relative', width: W, height: H, flexShrink: 0, transform: \`scale(\${k})\` }}>
        <Sequence width={W} height={H} layout="none" name=${JSON.stringify(sceneName)}>
          <${sceneName} {...(params as React.ComponentProps<typeof ${sceneName}>)} />
        </Sequence>
      </div>
    </AbsoluteFill>
  );
}
`;
  graphic.files.push({ path: 'index.tsx', text: index });
  graphic.files.push({ path: 'preview.json', text: JSON.stringify(preview, null, 2) + '\n' });
  if (Object.keys(typescript).length) graphic.files.push({ path: 'motion.json', text: JSON.stringify({ typescript }, null, 2) + '\n' });
  return {
    ok: true,
    problems,
    scene: { component: sceneName, file: F, line: lineOf(sfF, decl.node) },
    graphic,
    library,
    fonts,
    missingFonts: missingFonts.map((f) => f.path),
    params: params.map((p) => p.name),
    preview,
    sharedFiles: libFiles,
  };
};
