// On-screen words as settings (D168): which files hold a project's words
// (its "copy files": exported constants full of text, like src/story.ts), the
// one-time change that makes them settings (each such constant wrapped in
// words("NAME", …) from a small helper MetaRush writes, src/metarush-words.ts),
// and, once a version has that, the list of its settings (END.tagline → the
// words as written, with file and line). Nothing here runs project code.
import path from 'node:path';
import ts from 'typescript';
import { bare, lineOf, propName, wordy } from './project.mjs';

export const HELPER_NAME = 'metarush-words';
// Written without a label since the open-source release; helpers made before it say "… (D168)" and still match.
export const HELPER_MARK = 'MetaRush words as settings';

export const HELPER_TEXT = `// ${HELPER_MARK}: the words this project puts on screen can be changed
// as settings in MetaRush. words("END", {…}) gives back the value as written, with
// the words a person changed in MetaRush in their place: MetaRush passes them to
// the render as input props (metarushWords: {"END.tagline": "…"}). Nothing else
// changes; without settings every word is the one written in the code.
import { getInputProps } from "remotion";

type Settings = Record<string, string>;

const settings: Settings = (() => {
  try {
    const props = getInputProps() as { metarushWords?: unknown };
    const w = props && typeof props === "object" ? props.metarushWords : undefined;
    return w && typeof w === "object" ? (w as Settings) : {};
  } catch {
    return {};
  }
})();

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

/** The string leaves of a literal with their key paths: [{key, text, line, wordy}]. */
const leaves = (sf, node, key, out, depth = 0) => {
  const n = bare(node);
  if (!n || depth > 16 || out.length > 3000) return out;
  if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n)) {
    out.push({ key, text: n.text, line: lineOf(sf, n), wordy: wordy(n.text) });
  } else if (ts.isArrayLiteralExpression(n)) {
    n.elements.forEach((e, i) => leaves(sf, e, `${key}.${i}`, out, depth + 1));
  } else if (ts.isObjectLiteralExpression(n)) {
    for (const p of n.properties) {
      if (!ts.isPropertyAssignment(p)) continue;
      const k = propName(p);
      if (k !== undefined) leaves(sf, p.initializer, `${key}.${k}`, out, depth + 1);
    }
  }
  return out;
};

/** Is this import the words helper (./metarush-words, ../metarush-words …)? */
const isHelperSpec = (spec) => /(^|\/)metarush-words(\.ts|\.js)?$/.test(spec) && spec.startsWith('.');

/** The settings a version has: words("NAME", …) calls with the helper imported. */
export const settingsOf = (P) => {
  const out = [];
  let helper = '';
  for (const [f, sf] of P.sources) {
    if (sf.text.includes(HELPER_MARK) && /export function words|export const words/.test(sf.text)) helper = f;
    const local = new Set();
    for (const st of sf.statements) {
      if (!ts.isImportDeclaration(st) || !ts.isStringLiteral(st.moduleSpecifier) || !isHelperSpec(st.moduleSpecifier.text)) continue;
      const nb = st.importClause?.namedBindings;
      if (nb && ts.isNamedImports(nb)) for (const el of nb.elements) if ((el.propertyName ?? el.name).text === 'words') local.add(el.name.text);
    }
    if (!local.size) continue;
    const visit = (node) => {
      if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && local.has(node.expression.text) && node.arguments.length === 2 && ts.isStringLiteral(node.arguments[0])) {
        const name = node.arguments[0].text;
        for (const l of leaves(sf, node.arguments[1], name, [])) out.push({ ...l, file: f, name });
      }
      ts.forEachChild(node, visit);
    };
    visit(sf);
  }
  // A string directly in words("X", "…") is the key X itself.
  for (const s of out) if (s.key.endsWith('.')) s.key = s.key.slice(0, -1);
  return { helper, settings: out.slice(0, 3000) };
};

/** The exported constants of a file that hold words, with their initializers. */
const wordConsts = (sf) => {
  const out = [];
  for (const st of sf.statements) {
    if (!ts.isVariableStatement(st)) continue;
    const exported = (ts.getModifiers(st) ?? []).some((m) => m.kind === ts.SyntaxKind.ExportKeyword);
    if (!exported || !(st.declarationList.flags & ts.NodeFlags.Const)) continue;
    for (const d of st.declarationList.declarations) {
      if (!ts.isIdentifier(d.name) || !d.initializer) continue;
      const init = d.initializer;
      const b = bare(init);
      const wrapped = ts.isCallExpression(b) && ts.isIdentifier(b.expression) && b.expression.text === 'words';
      if (!wrapped && !(ts.isObjectLiteralExpression(b) || ts.isArrayLiteralExpression(b) || ts.isStringLiteral(b) || ts.isNoSubstitutionTemplateLiteral(b))) continue;
      const ls = leaves(sf, wrapped ? b.arguments[1] : init, d.name.text, []);
      const n = ls.filter((l) => l.wordy).length;
      if (n === 0) continue;
      out.push({ name: d.name.text, line: lineOf(sf, d), words: n, wrapped, init });
    }
  }
  return out;
};

/** Files that hold a project's on-screen words: exported constants of text, no components. */
export const copyFiles = (P) => {
  const out = [];
  for (const [f, sf] of P.sources) {
    if (sf.text.includes(HELPER_MARK)) continue;
    const consts = wordConsts(sf);
    const words = consts.reduce((a, c) => a + c.words, 0);
    if (words < 2) continue;
    let jsx = false;
    const find = (n) => {
      if (jsx) return;
      if (ts.isJsxElement(n) || ts.isJsxSelfClosingElement(n) || ts.isJsxFragment(n)) jsx = true;
      else ts.forEachChild(n, find);
    };
    find(sf);
    if (jsx) continue;
    out.push({ file: f, words, consts: consts.map(({ init, ...c }) => c), wrapped: consts.every((c) => c.wrapped) });
  }
  return out.sort((a, b) => b.words - a.words || a.file.localeCompare(b.file));
};

/**
 * Which words each file uses: the paths into the copy files' constants its
 * code reads (END.tagline, CONNECT.sources — a path that stops early uses
 * every word under it). {file: [paths]}.
 */
export const usesOf = (P, copySet) => {
  const out = {};
  for (const [f, sf] of P.sources) {
    const roots = new Map(); // local name → the constant's name in its copy file
    const ns = new Set(); // import * as S from './story'
    for (const [local, b] of P.bindings.get(f) ?? []) {
      if (!b.from || !copySet.has(b.from)) continue;
      if (b.name === '*') ns.add(local);
      else if (b.name !== 'default') roots.set(local, P.exportedLocal(b.from, b.name));
    }
    if (copySet.has(f)) for (const name of P.declared.get(f)?.keys() ?? []) roots.set(name, name);
    if (!roots.size && !ns.size) continue;
    const paths = new Set();
    const visit = (node) => {
      if (ts.isIdentifier(node) && (roots.has(node.text) || ns.has(node.text))) {
        const p = node.parent;
        const declName = p && (ts.isImportSpecifier(p) || ts.isImportClause(p) || ts.isNamespaceImport(p) || (ts.isVariableDeclaration(p) && p.name === node) || (ts.isPropertyAccessExpression(p) && p.name === node) || (ts.isPropertyAssignment(p) && p.name === node));
        if (!declName) {
          const path = roots.has(node.text) ? [roots.get(node.text)] : [];
          let n = node;
          for (;;) {
            const q = n.parent;
            if (q && ts.isPropertyAccessExpression(q) && q.expression === n) path.push(q.name.text);
            else if (q && ts.isElementAccessExpression(q) && q.expression === n && (ts.isNumericLiteral(q.argumentExpression) || ts.isStringLiteral(q.argumentExpression))) path.push(q.argumentExpression.text);
            else break;
            n = q;
          }
          if (path.length) paths.add(path.join('.'));
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(sf);
    out[f] = [...paths].sort();
  }
  return out;
};

/**
 * Taking the helper out again (D176, sending changes back): every
 * words("NAME", value) call becomes its value — with the words of `values`
 * ({"END.tagline": "…"}) written into the strings at those keys when given —
 * the helper's import goes, and so does the helper. Returns {files: [{path,
 * text}], removed: [helper], problems: []}. Spans are replaced in the text, so
 * formatting and comments stay as they were (the import lines setupWords
 * added go with their line breaks).
 */
export const bakeWords = (P, values = {}) => {
  const { helper } = settingsOf(P);
  const files = [];
  const problems = [];
  for (const [f, sf] of P.sources) {
    if (f === helper) continue;
    const imports = [];
    const local = new Set();
    sf.statements.forEach((st, i) => {
      if (!ts.isImportDeclaration(st) || !ts.isStringLiteral(st.moduleSpecifier) || !isHelperSpec(st.moduleSpecifier.text)) return;
      const nb = st.importClause?.namedBindings;
      const names = nb && ts.isNamedImports(nb) ? nb.elements : [];
      for (const el of names) if ((el.propertyName ?? el.name).text === 'words') local.add(el.name.text);
      imports.push({ st, i, only: names.length === 1 && !st.importClause?.name });
    });
    if (!local.size) continue;
    const edits = [];
    const visit = (node) => {
      if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && local.has(node.expression.text) && node.arguments.length === 2 && ts.isStringLiteral(node.arguments[0])) {
        const name = node.arguments[0].text;
        const arg = node.arguments[1];
        const start = arg.getStart(sf);
        let text = sf.text.slice(start, arg.getEnd());
        // The words of the settings, into the strings at their keys (from the end, so offsets hold).
        const inner = [];
        const walk = (n, key, depth = 0) => {
          const b = bare(n);
          if (!b || depth > 16) return;
          if (ts.isStringLiteral(b) || ts.isNoSubstitutionTemplateLiteral(b)) {
            const k = key.endsWith('.') ? key.slice(0, -1) : key;
            if (Object.prototype.hasOwnProperty.call(values, k)) inner.push({ start: b.getStart(sf) - start, end: b.getEnd() - start, text: JSON.stringify(String(values[k])) });
          } else if (ts.isArrayLiteralExpression(b)) {
            b.elements.forEach((e, i) => walk(e, `${key}.${i}`, depth + 1));
          } else if (ts.isObjectLiteralExpression(b)) {
            for (const p of b.properties) {
              if (!ts.isPropertyAssignment(p)) continue;
              const pk = propName(p);
              if (pk !== undefined) walk(p.initializer, `${key}.${pk}`, depth + 1);
            }
          }
        };
        walk(arg, name);
        for (const e of inner.sort((a, b) => b.start - a.start)) text = text.slice(0, e.start) + e.text + text.slice(e.end);
        edits.push({ start: node.getStart(sf), end: node.getEnd(), text });
        return; // a words() call inside another one is unwrapped with it
      }
      ts.forEachChild(node, visit);
    };
    visit(sf);
    for (const im of imports) {
      if (!im.only) {
        problems.push({ file: f, message: 'its import of the words helper brings more than words(); MetaRush left it as it is' });
        continue;
      }
      let start = im.st.getStart(sf);
      let end = im.st.getEnd();
      const prev = sf.statements[im.i - 1];
      if (prev && ts.isImportDeclaration(prev) && sf.text[start - 1] === '\n') start -= 1; // added after the last import, on its own line
      else if (sf.text.slice(end, end + 2) === '\n\n') end += 2; // added first, with a blank line after it
      else if (sf.text[end] === '\n') end += 1;
      edits.push({ start, end, text: '' });
    }
    let text = sf.text;
    for (const e of edits.sort((a, b) => b.start - a.start)) text = text.slice(0, e.start) + e.text + text.slice(e.end);
    files.push({ path: f, text });
  }
  return { files, removed: helper ? [helper] : [], problems };
};

/** Where the helper goes: next to the copy files (their common folder), as metarush-words.ts. */
const helperPath = (files) => {
  const dirs = files.map((f) => path.posix.dirname(f));
  let common = dirs[0] ?? 'src';
  for (const d of dirs) while (!(d === common || d.startsWith(common + '/')) && common !== '.') common = path.posix.dirname(common);
  return (common === '.' ? '' : common + '/') + HELPER_NAME + '.ts';
};

/**
 * The one-time change (D168): every exported constant of text in the copy
 * files wrapped in words("NAME", …), the helper imported, the helper written.
 * Returns {files: [{path, text}], helper, consts}. Spans are replaced in the
 * original text, so formatting and comments stay as they were.
 */
export const setupWords = (P, want) => {
  const copies = copyFiles(P).filter((c) => !want?.length || want.includes(c.file));
  if (!copies.length) return { files: [], helper: '', consts: [] };
  const existing = settingsOf(P).helper;
  const helper = existing || helperPath(copies.map((c) => c.file));
  const files = [];
  const consts = [];
  for (const c of copies) {
    const sf = P.sources.get(c.file);
    const edits = [];
    for (const k of wordConsts(sf)) {
      if (k.wrapped) continue;
      edits.push({ start: k.init.getStart(sf), end: k.init.getEnd(), text: `words(${JSON.stringify(k.name)}, ${k.init.getText(sf)})` });
      consts.push({ file: c.file, name: k.name, line: k.line, words: k.words });
    }
    if (!edits.length) continue;
    let text = sf.text;
    for (const e of edits.sort((a, b) => b.start - a.start)) text = text.slice(0, e.start) + e.text + text.slice(e.end);
    let rel = path.posix.relative(path.posix.dirname(c.file), helper.replace(/\.ts$/, ''));
    if (!rel.startsWith('.')) rel = './' + rel;
    const importLine = `import { words } from ${JSON.stringify(rel)};\n`;
    // After the last import at the top, else first (after leading comments).
    const imports = sf.statements.filter(ts.isImportDeclaration);
    if (imports.length) {
      const end = imports[imports.length - 1].getEnd();
      // edits above never touch imports, so the original offsets hold
      text = text.slice(0, end) + '\n' + importLine.trimEnd() + text.slice(end);
    } else {
      const first = sf.statements[0];
      const at = first ? first.getStart(sf) : 0;
      text = text.slice(0, at) + importLine + '\n' + text.slice(at);
    }
    files.push({ path: c.file, text });
  }
  if (!existing && files.length) files.push({ path: helper, text: HELPER_TEXT });
  return { files, helper, consts };
};
