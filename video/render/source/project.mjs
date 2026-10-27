// A Remotion source's code, parsed (D132): MetaRush's own TypeScript parses
// every code file and these helpers walk the trees. Nothing here imports,
// requires or evaluates project code. analyze.mjs (the static read), map.mjs
// (the project map's facts), words.mjs (words as settings) and lift.mjs (a
// scene lifted into a motion graphic) all start from parseProject.
import fs from 'node:fs';
import path from 'node:path';
import ts from 'typescript';

export const CODE = /\.(tsx|ts|jsx|js|mjs|cjs|mts|cts)$/;
const SKIP_DIRS = new Set(['node_modules', '.git', '.metarush', 'out', 'public', '.cache', 'build', 'dist']);
export const EXTS = ['.tsx', '.ts', '.jsx', '.js', '.mjs', '.cjs', '.mts', '.cts', '.json'];
const MAX_FILES = 4000;

export const listCode = (root) => {
  const out = [];
  const walk = (dir, rel) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      if (out.length >= MAX_FILES) return;
      const r = rel ? `${rel}/${e.name}` : e.name;
      if (e.isDirectory()) {
        if (!SKIP_DIRS.has(e.name) && !e.name.startsWith('.')) walk(path.join(dir, e.name), r);
      } else if (e.isFile() && CODE.test(e.name) && !e.name.endsWith('.d.ts')) {
        out.push(r);
      }
    }
  };
  walk(root, '');
  return out.sort();
};

export const kindOf = (f) => {
  if (f.endsWith('.tsx')) return ts.ScriptKind.TSX;
  if (f.endsWith('.jsx')) return ts.ScriptKind.JSX;
  if (/\.(js|mjs|cjs)$/.test(f)) return ts.ScriptKind.JS;
  return ts.ScriptKind.TS;
};

export const packageOf = (spec) => {
  if (spec.startsWith('@')) return spec.split('/').slice(0, 2).join('/');
  return spec.split('/')[0];
};

export const lineOf = (sf, node) => sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1;

/** Resolves a relative import to a project file (or null); JSON files resolve when they exist. */
export const resolveRel = (root, files, from, spec) => {
  const base = path.posix.normalize(path.posix.join(path.posix.dirname(from), spec));
  if (base.startsWith('..')) return null;
  const cands = [base, ...EXTS.map((e) => base + e), ...EXTS.map((e) => `${base}/index${e}`)];
  if (/\.(js|jsx|mjs|cjs)$/.test(base)) {
    const stem = base.replace(/\.(js|jsx|mjs|cjs)$/, '');
    cands.push(stem + '.ts', stem + '.tsx', stem + '.mts', stem + '.cts');
  }
  for (const c of cands) if (files.has(c)) return c;
  return null;
};

/** A JSON file a relative import names (or null). */
export const resolveJSON = (root, from, spec) => {
  const base = path.posix.normalize(path.posix.join(path.posix.dirname(from), spec));
  if (base.startsWith('..') || !base.endsWith('.json')) return null;
  try {
    return fs.statSync(path.join(root, base)).isFile() ? base : null;
  } catch {
    return null;
  }
};

export const stringOf = (node) => {
  if (!node) return undefined;
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text;
  if (ts.isJsxExpression(node)) return stringOf(node.expression);
  if (ts.isParenthesizedExpression(node)) return stringOf(node.expression);
  return undefined;
};

/** Strips parentheses, `as` and `satisfies` around an expression. */
export const bare = (node) => {
  let n = node;
  while (n && (ts.isParenthesizedExpression(n) || ts.isAsExpression(n) || ts.isSatisfiesExpression?.(n) || ts.isTypeAssertionExpression?.(n) || ts.isNonNullExpression(n))) n = n.expression;
  return n;
};

/** A literal as JSON-able data, or undefined when it isn't one. */
export const literalOf = (node, depth = 0) => {
  if (!node || depth > 12) return undefined;
  if (ts.isParenthesizedExpression(node) || ts.isAsExpression(node) || ts.isSatisfiesExpression?.(node)) return literalOf(node.expression, depth + 1);
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text;
  if (ts.isNumericLiteral(node)) return Number(node.text);
  if (ts.isPrefixUnaryExpression(node) && node.operator === ts.SyntaxKind.MinusToken && ts.isNumericLiteral(node.operand)) return -Number(node.operand.text);
  if (node.kind === ts.SyntaxKind.TrueKeyword) return true;
  if (node.kind === ts.SyntaxKind.FalseKeyword) return false;
  if (node.kind === ts.SyntaxKind.NullKeyword) return null;
  if (ts.isArrayLiteralExpression(node)) {
    const out = [];
    for (const e of node.elements) {
      const v = literalOf(e, depth + 1);
      if (v === undefined) return undefined;
      out.push(v);
    }
    return out;
  }
  if (ts.isObjectLiteralExpression(node)) {
    const out = {};
    for (const p of node.properties) {
      if (!ts.isPropertyAssignment(p)) return undefined;
      const k = ts.isIdentifier(p.name) || ts.isStringLiteral(p.name) || ts.isNumericLiteral(p.name) ? p.name.text : undefined;
      if (k === undefined) return undefined;
      const v = literalOf(p.initializer, depth + 1);
      if (v === undefined) return undefined;
      out[k] = v;
    }
    return out;
  }
  return undefined;
};

/** A property's name as written ({a: 1}, {"a b": 1}), or undefined. */
export const propName = (p) => {
  const n = p.name;
  if (!n) return undefined;
  if (ts.isIdentifier(n) || ts.isStringLiteral(n) || ts.isNumericLiteral(n) || ts.isPrivateIdentifier?.(n)) return n.text;
  return undefined;
};

/**
 * Words people read, not module names, colours, keys or paths: at least two
 * letters, and a single "word" with / # . @ in it is a path or an address.
 */
export const wordy = (s) => {
  if (typeof s !== 'string' || s.length < 2 || s.length > 400) return false;
  if (!/\s/.test(s) && /[/#.@\\]|^[a-z]+[A-Z][A-Za-z]*$|^[a-z]+(-[a-z]+)+$|^[a-z_]+$/.test(s) && !/^[A-Z]/.test(s)) return false;
  if (/^(#|rgba?\(|hsla?\(|linear-gradient|radial-gradient|url\(|var\(--|M\s?-?\d)/i.test(s)) return false;
  // CSS values (shadows, transforms, timings) are code, not words.
  if (/rgba?\(|hsla?\(|\d(px|em|rem|vh|vw|deg|ms)\b|cubic-bezier|translate[XYZ3]?\(|rotate\(|scale\(|#[0-9a-f]{3,8}\b/i.test(s)) return false;
  if (/^[\d\s.,:%+-]+(px|em|rem|vh|vw|deg|ms|s)?$/.test(s)) return false;
  let letters = 0;
  for (const ch of s) if (/\p{L}/u.test(ch)) letters++;
  return letters >= 2;
};

/** CSS and SVG attribute values are never on-screen words. */
const NOT_WORDS_ATTR = new Set(['className', 'class', 'style', 'key', 'id', 'src', 'href', 'd', 'fill', 'stroke', 'viewBox', 'strokeLinecap', 'strokeLinejoin', 'fontFamily', 'transform', 'points', 'path', 'xmlns', 'preserveAspectRatio', 'mode', 'layout', 'role', 'type', 'name', 'ref', 'loading', 'crossOrigin', 'textAnchor', 'dominantBaseline', 'mixBlendMode', 'objectFit']);
// Style and code keys (camelCase CSS properties, animation options): their values are never words.
const CSS_KEY = /^(position|display|color|fill|stroke|background\w*|border\w*|margin\w*|padding\w*|width|height|minWidth|maxWidth|minHeight|maxHeight|top|left|right|bottom|inset|transform\w*|transition\w*|animation\w*|filter|backdropFilter|opacity|zIndex|cursor|pointerEvents|visibility|overflow\w*|boxShadow|boxSizing|gap|rowGap|columnGap|justify\w*|align\w*|flex\w*|grid\w*|object\w*|clip\w*|mask\w*|outline\w*|mixBlendMode|easing|extrapolateLeft|extrapolateRight|(text|line|font|white|word|letter|vertical)[A-Z]\w*|fontFamily|kind|tone|variant|direction|unit|format|anchor|origin|placement|icon)$/;

/** True when a string literal sits where words can't be (keys, CSS values, attributes, imports). */
export const notWordsPlace = (node) => {
  const p = node.parent;
  if (!p) return true;
  if (ts.isImportDeclaration(p) || ts.isExportDeclaration(p) || ts.isExternalModuleReference(p) || ts.isLiteralTypeNode(p)) return true;
  if (ts.isPropertyAssignment(p) && p.name === node) return true;
  if (ts.isPropertyAssignment(p) && p.initializer === node) {
    const k = propName(p);
    if (k && CSS_KEY.test(k)) return true;
  }
  if (ts.isJsxAttribute(p) || (ts.isJsxExpression(p) && p.parent && ts.isJsxAttribute(p.parent))) {
    const attr = ts.isJsxAttribute(p) ? p : p.parent;
    const k = attr.name.getText();
    if (NOT_WORDS_ATTR.has(k) || /^(on[A-Z]|data-|aria-|stroke|fill|font)/.test(k)) return true;
  }
  if (ts.isCallExpression(p)) {
    const c = p.expression;
    const name = ts.isIdentifier(c) ? c.text : ts.isPropertyAccessExpression(c) ? c.name.text : '';
    if (['staticFile', 'require', 'import', 'random', 'delayRender', 'loadFont', 'useResource', 'useFont', 'interpolateColors', 'measureText', 'fitText', 'Error', 'TypeError', 'warn', 'log', 'error', 'getElementById', 'querySelector', 'cubicBezier', 'bezier', 'picture', 'metarushPicture'].includes(name)) return true;
  }
  if (ts.isNewExpression(p)) return true; // new Error("…")
  if (ts.isBinaryExpression(p) && /Equals/.test(ts.SyntaxKind[p.operatorToken.kind])) return true; // x === "warehouse"
  if (ts.isCaseClause(p) || ts.isElementAccessExpression(p) || ts.isComputedPropertyName(p)) return true;
  if (ts.isTemplateSpan(p)) return true;
  return false;
};

/**
 * Parses every code file of a work tree and indexes what the readers need:
 * imports (with names), top-level constants, the import graph, and helpers
 * that resolve numbers and constants without running anything. overlay
 * ({texts: Map path → text, removed: Set of paths}) reads some files as
 * changed in memory (one change taken out after another, D176).
 */
export const parseProject = (root, overlay = {}) => {
  const list = listCode(root).filter((f) => !overlay.removed?.has(f));
  const files = new Set(list);
  const sources = new Map();
  for (const f of list) {
    const text = overlay.texts?.get(f) ?? fs.readFileSync(path.join(root, f), 'utf8');
    sources.set(f, ts.createSourceFile(f, text, ts.ScriptTarget.Latest, true, kindOf(f)));
  }
  const problems = [];
  const imports = [];
  const fonts = new Map();
  const fontFiles = new Map(); // file → [{module, family, local name}]
  const staticFiles = new Set();
  const compositions = [];
  const json = new Map(); // file → [json files it imports]
  // Per file: name → {from file, imported name, spec} and top-level const initializers.
  const bindings = new Map();
  const consts = new Map();
  const declared = new Map(); // file → name → {node, exported, line}
  const exported = new Map(); // file → name → local name

  for (const [f, sf] of sources) {
    const fi = { file: f, files: new Set(), packages: new Set() };
    const names = new Map();
    const local = new Map();
    const decl = new Map();
    const exp = new Map();
    const jsonImports = new Set();
    const fontMods = [];
    const addSpec = (spec, node, how) => {
      if (spec.includes('!')) {
        problems.push({ severity: 'error', code: 'inline_loader', file: f, line: lineOf(sf, node), message: `“${spec}” asks the bundler to run a loader named in the import; MetaRush bundles the project its own way and refuses that.`, hint: 'Import the file plainly.' });
        return null;
      }
      if (/^[a-z][a-z0-9+.-]*:\/\//i.test(spec)) {
        problems.push({ severity: 'error', code: 'remote_import', file: f, line: lineOf(sf, node), message: `“${spec}” is imported from a web address; renders have no network, so it can't load.`, hint: 'Install the package or copy the file into the project.' });
        return null;
      }
      if (spec.startsWith('.') || spec.startsWith('/')) {
        const r = spec.startsWith('/') ? null : resolveRel(root, files, f, spec);
        if (r) fi.files.add(r);
        else {
          const j = spec.startsWith('/') ? null : resolveJSON(root, f, spec);
          if (j) jsonImports.add(j);
        }
        return r;
      }
      if (spec.startsWith('node:')) return null;
      fi.packages.add(packageOf(spec));
      const m = /^@remotion\/google-fonts\/([A-Za-z0-9]+)$/.exec(spec);
      if (m && m[1] !== 'index' && m[1] !== 'from-info') {
        fonts.set(m[1], { module: spec, family: m[1] });
        fontMods.push({ module: spec, family: m[1] });
      }
      if (how === 'dynamic' || how === 'require') {
        problems.push({ severity: 'warning', code: 'dynamic_import', file: f, line: lineOf(sf, node), message: `“${spec}” is loaded while the video runs, not imported at the top; it still has to be one of the project's packages.` });
      }
      return null;
    };
    const isExported = (node) => (ts.canHaveModifiers(node) ? ts.getModifiers(node) ?? [] : []).some((m) => m.kind === ts.SyntaxKind.ExportKeyword);
    const visit = (node, folders) => {
      if ((ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) && node.moduleSpecifier && ts.isStringLiteral(node.moduleSpecifier)) {
        const spec = node.moduleSpecifier.text;
        const target = addSpec(spec, node, 'static');
        if (ts.isImportDeclaration(node) && node.importClause) {
          const ic = node.importClause;
          if (ic.name) names.set(ic.name.text, { from: target, spec, name: 'default', json: resolveJSON(root, f, spec) });
          const nb = ic.namedBindings;
          if (nb && ts.isNamedImports(nb)) for (const el of nb.elements) names.set(el.name.text, { from: target, spec, name: (el.propertyName ?? el.name).text });
          if (nb && ts.isNamespaceImport(nb)) names.set(nb.name.text, { from: target, spec, name: '*' });
        }
        if (ts.isExportDeclaration(node) && node.exportClause && ts.isNamedExports(node.exportClause)) {
          for (const el of node.exportClause.elements) exp.set(el.name.text, { from: target, name: (el.propertyName ?? el.name).text });
        }
      } else if (ts.isExportDeclaration(node) && !node.moduleSpecifier && node.exportClause && ts.isNamedExports(node.exportClause)) {
        for (const el of node.exportClause.elements) exp.set(el.name.text, { local: (el.propertyName ?? el.name).text });
      } else if (ts.isImportEqualsDeclaration(node) && ts.isExternalModuleReference(node.moduleReference) && ts.isStringLiteral(node.moduleReference.expression)) {
        addSpec(node.moduleReference.expression.text, node, 'require');
      } else if (ts.isCallExpression(node)) {
        const callee = node.expression;
        const arg = node.arguments[0];
        if (callee.kind === ts.SyntaxKind.ImportKeyword && arg && ts.isStringLiteral(arg)) addSpec(arg.text, node, 'dynamic');
        else if (ts.isIdentifier(callee) && callee.text === 'require' && arg && ts.isStringLiteral(arg)) addSpec(arg.text, node, 'require');
        else if (ts.isIdentifier(callee) && callee.text === 'staticFile' && arg) {
          if (ts.isStringLiteral(arg) || ts.isNoSubstitutionTemplateLiteral(arg)) staticFiles.add(arg.text.replace(/^\/+/, ''));
          else if (ts.isTemplateExpression(arg) && arg.head.text) staticFiles.add(arg.head.text.replace(/^\/+/, '') + '*');
        }
      } else if (ts.isVariableStatement(node) && node.parent === sf) {
        const e = isExported(node);
        for (const d of node.declarationList.declarations) {
          if (!ts.isIdentifier(d.name)) continue;
          if (d.initializer) local.set(d.name.text, d.initializer);
          decl.set(d.name.text, { node: d, exported: e, line: lineOf(sf, d), statement: node });
          if (e) exp.set(d.name.text, { local: d.name.text });
        }
      } else if ((ts.isFunctionDeclaration(node) || ts.isClassDeclaration(node)) && node.parent === sf && node.name) {
        const e = isExported(node);
        decl.set(node.name.text, { node, exported: e, line: lineOf(sf, node), statement: node });
        if (e) exp.set(node.name.text, { local: node.name.text });
      } else if (ts.isExportAssignment(node) && node.parent === sf && ts.isIdentifier(node.expression)) {
        exp.set('default', { local: node.expression.text });
      }
      let nextFolders = folders;
      if (ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node)) {
        const open = ts.isJsxElement(node) ? node.openingElement : node;
        const tag = open.tagName.getText(sf);
        if (tag === 'Folder' || tag.endsWith('.Folder')) {
          const nameAttr = open.attributes.properties.find((p) => ts.isJsxAttribute(p) && p.name.getText(sf) === 'name');
          const n = nameAttr && ts.isJsxAttribute(nameAttr) ? stringOf(nameAttr.initializer) : undefined;
          nextFolders = [...folders, n ?? '?'];
        }
        if (tag === 'Composition' || tag === 'Still' || tag.endsWith('.Composition') || tag.endsWith('.Still')) {
          const attrs = {};
          let spread = false;
          for (const p of open.attributes.properties) {
            if (ts.isJsxSpreadAttribute(p)) {
              spread = true;
              continue;
            }
            attrs[p.name.getText(sf)] = p.initializer;
          }
          compositions.push({ file: f, line: lineOf(sf, open), still: tag.endsWith('Still'), attrs, spread, folder: folders.join('/'), sf });
        }
      }
      ts.forEachChild(node, (c) => visit(c, nextFolders));
    };
    visit(sf, []);
    bindings.set(f, names);
    consts.set(f, local);
    declared.set(f, decl);
    exported.set(f, exp);
    json.set(f, [...jsonImports].sort());
    fontFiles.set(f, fontMods);
    imports.push({ file: f, files: [...fi.files].sort(), packages: [...fi.packages].sort() });
  }

  const constInit = (f, name, depth = 0) => {
    if (depth > 16) return undefined;
    const local = consts.get(f)?.get(name);
    if (local) return { file: f, node: local, sf: sources.get(f) };
    const b = bindings.get(f)?.get(name);
    if (b?.from && b.name !== 'default' && b.name !== '*') return constInit(b.from, exportedLocal(b.from, b.name), depth + 1);
    return undefined;
  };
  const exportedLocal = (f, name) => {
    const e = exported.get(f)?.get(name);
    return e?.local ?? name;
  };

  /**
   * A number written in code, with where it is written: a literal, a constant
   * (local or imported), a property of a constant object (SCENES.open), or
   * arithmetic on those. {value, file, line} — line of the literal that sets
   * it when there is one literal, else of the expression.
   */
  const numberWhere = (f, node, depth = 0) => {
    if (!node || depth > 16) return undefined;
    const sf = sources.get(f);
    if (ts.isJsxExpression(node)) return numberWhere(f, node.expression, depth + 1);
    if (ts.isParenthesizedExpression(node) || ts.isAsExpression(node) || ts.isSatisfiesExpression?.(node)) return numberWhere(f, node.expression, depth + 1);
    if (ts.isNumericLiteral(node)) return { value: Number(node.text), file: f, line: lineOf(sf, node), literal: true };
    if (ts.isPrefixUnaryExpression(node) && node.operator === ts.SyntaxKind.MinusToken) {
      const v = numberWhere(f, node.operand, depth + 1);
      return v === undefined ? undefined : { ...v, value: -v.value };
    }
    if (ts.isBinaryExpression(node)) {
      const a = numberWhere(f, node.left, depth + 1);
      const b = numberWhere(f, node.right, depth + 1);
      if (a === undefined || b === undefined) return undefined;
      let value;
      switch (node.operatorToken.kind) {
        case ts.SyntaxKind.PlusToken: value = a.value + b.value; break;
        case ts.SyntaxKind.MinusToken: value = a.value - b.value; break;
        case ts.SyntaxKind.AsteriskToken: value = a.value * b.value; break;
        case ts.SyntaxKind.SlashToken: value = b.value === 0 ? undefined : a.value / b.value; break;
        default: value = undefined;
      }
      if (value === undefined) return undefined;
      // 2 * FPS: the literal that isn't the frame rate is the one people change.
      const main = a.literal && !b.literal ? a : b.literal && !a.literal ? b : { file: f, line: lineOf(sf, node) };
      return { value, file: main.file, line: main.line, literal: false };
    }
    if (ts.isIdentifier(node)) {
      const init = constInit(f, node.text, depth);
      return init ? numberWhere(init.file, init.node, depth + 1) : undefined;
    }
    if (ts.isPropertyAccessExpression(node) && ts.isIdentifier(node.expression)) {
      const init = constInit(f, node.expression.text, depth);
      if (!init) return undefined;
      const obj = bare(init.node);
      if (!obj || !ts.isObjectLiteralExpression(obj)) return undefined;
      const prop = obj.properties.find((p) => ts.isPropertyAssignment(p) && propName(p) === node.name.text);
      return prop ? numberWhere(init.file, prop.initializer, depth + 1) : undefined;
    }
    return undefined;
  };
  const numberOf = (f, node) => numberWhere(f, node)?.value;

  /**
   * Where the value of an expression is written, even when it isn't a plain
   * number (TOTAL = Object.values(SCENES).reduce(…)): the constant or the
   * property it names, else the expression itself.
   */
  const placeOf = (f, node, depth = 0) => {
    const w = numberWhere(f, node);
    if (w) return { file: w.file, line: w.line, value: w.value };
    const sf = sources.get(f);
    let n = node;
    while (n && (ts.isJsxExpression(n) || ts.isParenthesizedExpression(n) || ts.isAsExpression(n))) n = n.expression;
    if (n && depth < 8) {
      if (ts.isIdentifier(n)) {
        const init = constInit(f, n.text);
        if (init) return { file: init.file, line: lineOf(init.sf, init.node.parent ?? init.node), value: null };
      }
      if (ts.isPropertyAccessExpression(n) && ts.isIdentifier(n.expression)) {
        const init = constInit(f, n.expression.text);
        const obj = init ? bare(init.node) : null;
        if (obj && ts.isObjectLiteralExpression(obj)) {
          const prop = obj.properties.find((p) => ts.isPropertyAssignment(p) && propName(p) === n.name.text);
          if (prop) return { file: init.file, line: lineOf(init.sf, prop), value: null };
        }
      }
    }
    return n ? { file: f, line: lineOf(sf, n), value: null } : null;
  };

  // Each file's reach through project imports.
  const graph = new Map(imports.map((i) => [i.file, i.files]));
  const closure = (start) => {
    const seen = new Set();
    const stack = [start];
    while (stack.length) {
      const f = stack.pop();
      if (!f || seen.has(f)) continue;
      seen.add(f);
      for (const g of graph.get(f) ?? []) stack.push(g);
    }
    return [...seen].sort();
  };
  /** The file and name a JSX tag or identifier in file f refers to (following imports and re-exports). */
  const declOf = (f, name, depth = 0) => {
    if (depth > 12 || !name) return null;
    const d = declared.get(f)?.get(name);
    if (d) return { file: f, name, ...d };
    const b = bindings.get(f)?.get(name);
    if (b?.from) {
      if (b.name === '*' || b.name === 'default') {
        const target = b.name === 'default' ? exported.get(b.from)?.get('default')?.local : null;
        return target ? declOf(b.from, target, depth + 1) : null;
      }
      const e = exported.get(b.from)?.get(b.name);
      if (e?.from) return declOf(e.from, e.name, depth + 1);
      return declOf(b.from, e?.local ?? b.name, depth + 1);
    }
    return null;
  };
  const componentFile = (f, node) => {
    let n = node;
    while (n && (ts.isJsxExpression(n) || ts.isParenthesizedExpression(n))) n = n.expression;
    if (n && ts.isIdentifier(n)) {
      const b = bindings.get(f)?.get(n.text);
      if (b?.from) return b.from;
    }
    return f;
  };
  // How far each file is from another through imports (0: itself).
  const distance = (start) => {
    const d = new Map([[start, 0]]);
    const q = [start];
    while (q.length) {
      const f = q.shift();
      for (const g of graph.get(f) ?? []) {
        if (!d.has(g)) {
          d.set(g, d.get(f) + 1);
          q.push(g);
        }
      }
    }
    return Object.fromEntries(d);
  };
  return { root, list, files, sources, problems, imports, fonts, fontFiles, staticFiles, compositions, json, bindings, consts, declared, exported, graph, closure, distance, constInit, numberWhere, numberOf, placeOf, declOf, componentFile, exportedLocal };
};
