// Pictures as settings (D209): the pictures a project draws — its public/
// files named with staticFile("…") and the picture files its code imports —
// found without running anything; the one-time change that makes them
// settings (each reference wrapped in picture("NAME", …) from a small helper
// MetaRush writes, src/metarush-pictures.ts); and that change taken out again
// when changes go back to the project's repository (D176). After the change a
// person replaces a picture in MetaRush as data: the render gets the new
// picture's address in the input prop metarushPictures and MetaRush serves the
// file, by its SHA-256, with the render's own files. Nothing here runs project
// code.
import fs from 'node:fs';
import path from 'node:path';
import ts from 'typescript';
import { bare, lineOf } from './project.mjs';

export const PICTURES_HELPER_NAME = 'metarush-pictures';
// Written without a label since the open-source release; helpers made before it say "… (D209)" and still match.
export const PICTURES_HELPER_MARK = 'MetaRush pictures as settings';
/** Files a browser draws as a picture (what staticFile and imports name). */
export const PICTURE_FILE = /\.(png|jpe?g|webp|gif|svg|avif|bmp)$/i;
/** Where MetaRush serves replaced pictures in a render (the bundle's own server). */
export const SERVED_PREFIX = '/__metarush_pictures/';

export const PICTURES_HELPER_TEXT = `// ${PICTURES_HELPER_MARK}: the pictures this project draws can be
// replaced in MetaRush without changing its code. picture("HERO", staticFile("hero.png"))
// gives back the picture as written, or the one a person chose in MetaRush in its
// place: MetaRush passes its address to the render as an input prop
// (metarushPictures: {"HERO": "${SERVED_PREFIX}<sha256>.png"}) and serves the file with
// the render's own files. Without settings every picture is the code's own.
import { getInputProps } from "remotion";

const settings: Record<string, string> = (() => {
  try {
    const props = getInputProps() as { metarushPictures?: unknown };
    const p = props && typeof props === "object" ? props.metarushPictures : undefined;
    return p && typeof p === "object" ? (p as Record<string, string>) : {};
  } catch {
    return {};
  }
})();

export function picture<T>(name: string, value: T): T {
  const s = Object.prototype.hasOwnProperty.call(settings, name) ? settings[name] : undefined;
  return (typeof s === "string" && s.startsWith("${SERVED_PREFIX}") ? s : value) as T;
}
`;

/** Is this import the pictures helper (./metarush-pictures, ../metarush-pictures …)? */
export const isPicturesHelperSpec = (spec) => spec.startsWith('.') && /(^|\/)metarush-pictures(\.ts|\.js)?$/.test(spec);

const trimSlashes = (p) => String(p).replace(/^\/+/, '');

/**
 * What a file binds that matters here: remotion's staticFile (and namespace
 * imports of remotion), the helper's picture, the helper's import lines, and
 * default imports of picture files (local name → the file, its statement).
 */
const fileFacts = (f, sf) => {
  const staticFns = new Set();
  const remotionNs = new Set();
  const helperFns = new Set();
  const helperImports = [];
  const pictureImports = new Map();
  sf.statements.forEach((st, i) => {
    if (!ts.isImportDeclaration(st) || !ts.isStringLiteral(st.moduleSpecifier)) return;
    const spec = st.moduleSpecifier.text;
    const ic = st.importClause;
    if (!ic || ic.isTypeOnly) return;
    const nb = ic.namedBindings;
    if (spec === 'remotion') {
      if (nb && ts.isNamedImports(nb)) for (const el of nb.elements) if (!el.isTypeOnly && (el.propertyName ?? el.name).text === 'staticFile') staticFns.add(el.name.text);
      if (nb && ts.isNamespaceImport(nb)) remotionNs.add(nb.name.text);
    } else if (isPicturesHelperSpec(spec)) {
      const names = nb && ts.isNamedImports(nb) ? nb.elements : [];
      for (const el of names) if ((el.propertyName ?? el.name).text === 'picture') helperFns.add(el.name.text);
      helperImports.push({ st, i, only: names.length === 1 && !ic.name });
    } else if (spec.startsWith('.') && !/[?!#]/.test(spec) && PICTURE_FILE.test(spec) && ic.name) {
      const file = path.posix.normalize(path.posix.join(path.posix.dirname(f), spec));
      pictureImports.set(ic.name.text, { file, st, i, spec, alone: !nb });
    }
  });
  return { staticFns, remotionNs, helperFns, helperImports, pictureImports };
};

/** staticFile(…) from remotion (also R.staticFile(…) through a namespace import). */
const isStaticFileCall = (node, facts) => {
  if (!node || !ts.isCallExpression(node) || node.arguments.length !== 1) return false;
  const c = node.expression;
  if (ts.isIdentifier(c)) return facts.staticFns.has(c.text);
  return ts.isPropertyAccessExpression(c) && ts.isIdentifier(c.expression) && facts.remotionNs.has(c.expression.text) && c.name.text === 'staticFile';
};

/** The literal path a staticFile call names, or undefined. */
const literalPath = (call) => {
  const arg = call.arguments[0];
  return arg && (ts.isStringLiteral(arg) || ts.isNoSubstitutionTemplateLiteral(arg)) ? trimSlashes(arg.text) : undefined;
};

/** picture("NAME", X) through a local name bound to the helper. */
const isPictureCall = (node, facts) =>
  ts.isCallExpression(node) && ts.isIdentifier(node.expression) && facts.helperFns.has(node.expression.text) && node.arguments.length === 2 && ts.isStringLiteral(node.arguments[0]);

/** A JSX picture element around a node (<Img src={…}>, <img src={…}>). */
const inPictureTag = (node) => {
  for (let n = node.parent, depth = 0; n && depth < 4; n = n.parent, depth++) {
    if (ts.isJsxAttribute(n)) {
      const el = n.parent?.parent;
      const tag = el && (ts.isJsxOpeningElement(el) || ts.isJsxSelfClosingElement(el)) ? el.tagName.getText() : '';
      return /^(Img|img|Image|image)$/.test(tag.split('.').pop() ?? '');
    }
  }
  return false;
};

/** Every identifier written in a file (to pick names that can't clash). */
const identifiers = (sf) => {
  const out = new Set();
  const walk = (n) => {
    if (ts.isIdentifier(n)) out.add(n.text);
    ts.forEachChild(n, walk);
  };
  walk(sf);
  return out;
};

const unique = (want, taken, sep = '') => {
  let n = want;
  for (let i = 2; taken.has(n); i++) n = `${want}${sep}${i}`;
  taken.add(n);
  return n;
};

/** HERO from hero.png, HERO_DARK from hero-dark.svg; a letter first. */
export const pictureName = (part) => {
  let n = String(part)
    .replace(/\.[^.]+$/, '')
    .normalize('NFKD')
    .replace(/[^A-Za-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
    .toUpperCase();
  if (!n) n = 'PICTURE';
  if (!/^[A-Z]/.test(n)) n = `PICTURE_${n}`;
  return n.slice(0, 40).replace(/_+$/, '');
};

/** A name for a file no other picture has: its own, then with its folder, then numbered. */
const nameFor = (file, taken) => {
  const parts = file.split('/');
  const base = pictureName(parts[parts.length - 1]);
  const folder = parts.length > 2 ? pictureName(parts[parts.length - 2]) : '';
  for (const n of [base, folder && `${folder}_${base}`]) {
    if (n && !taken.has(n)) {
      taken.add(n);
      return n;
    }
  }
  return unique(base, taken, '_');
};

/**
 * The pictures a project draws (D209). Returns {pictures, left, helper,
 * byFile} — pictures: [{name, file, setting, uses: [{file, line, how,
 * wrapped, why?}]}] (how: static | import; why: when a use can't be wrapped);
 * left: references worked out as the video plays [{file, line, code, why}];
 * helper: the helper's path when the version has it; internal: the nodes
 * setup needs.
 */
export const scanPictures = (P) => {
  const { root, sources } = P;
  let helper = '';
  const named = new Map(); // name → {name, file, uses}
  const candidates = new Map(); // file → uses (not wrapped yet)
  const left = [];
  const internal = new Map(); // file → [{use, node | st, local}]
  // A file inside the project only (staticFile refuses "..", and an import outside it isn't the project's).
  const outside = (rel) => rel.split('/').includes('..') || path.posix.isAbsolute(rel);
  const missing = (rel) => (outside(rel) ? 'it names a file outside the project' : `the project has no file ${rel}`);
  const exists = (rel) => {
    if (outside(rel)) return false;
    try {
      return fs.statSync(path.join(root, rel)).isFile();
    } catch {
      return false;
    }
  };
  for (const [f, sf] of sources) {
    if (sf.text.includes(PICTURES_HELPER_MARK) && /export function picture\b/.test(sf.text)) {
      helper = f;
      continue;
    }
    const facts = fileFacts(f, sf);
    if (!facts.staticFns.size && !facts.remotionNs.size && !facts.helperFns.size && !facts.pictureImports.size) continue;
    const wrappedImports = new Set();
    const mine = [];
    const addNamed = (name, file, use) => {
      const e = named.get(name) ?? { name, file: '', uses: [] };
      if (!e.file && file) e.file = file;
      e.uses.push(use);
      named.set(name, e);
    };
    const addCandidate = (file, use, extra) => {
      if (!candidates.has(file)) candidates.set(file, []);
      candidates.get(file).push(use);
      mine.push({ use, file, ...extra });
    };
    const visit = (node) => {
      if (isPictureCall(node, facts)) {
        const name = node.arguments[0].text;
        const target = bare(node.arguments[1]);
        let file = '';
        let how = 'code';
        if (isStaticFileCall(target, facts) && literalPath(target) !== undefined) {
          file = `public/${literalPath(target)}`;
          how = 'static';
        } else if (ts.isIdentifier(target) && facts.pictureImports.has(target.text)) {
          file = facts.pictureImports.get(target.text).file;
          how = 'import';
          wrappedImports.add(target.text);
        }
        addNamed(name, file, { file: f, line: lineOf(sf, node), how, wrapped: true });
        return; // its staticFile is wrapped already
      }
      if (isStaticFileCall(node, facts)) {
        const p = literalPath(node);
        if (p !== undefined) {
          if (PICTURE_FILE.test(p)) {
            const file = `public/${p}`;
            if (exists(file)) addCandidate(file, { file: f, line: lineOf(sf, node), how: 'static', wrapped: false }, { node });
            else left.push({ file: f, line: lineOf(sf, node), code: node.getText(sf).slice(0, 160), why: missing(file) });
          }
        } else {
          const text = node.arguments[0].getText(sf);
          if (inPictureTag(node) || /\.(png|jpe?g|webp|gif|svg|avif|bmp)\b/i.test(text)) {
            left.push({ file: f, line: lineOf(sf, node), code: node.getText(sf).slice(0, 160), why: 'its file is worked out as the video plays' });
          }
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(sf);
    // Imported picture files not wrapped yet.
    const imports = sf.statements.filter(ts.isImportDeclaration);
    const lastImport = imports[imports.length - 1];
    for (const [local, im] of facts.pictureImports) {
      if (wrappedImports.has(local)) continue;
      const use = { file: f, line: lineOf(sf, im.st), how: 'import', wrapped: false };
      if (!exists(im.file)) {
        left.push({ file: f, line: use.line, code: im.st.getText(sf).slice(0, 160), why: missing(im.file) });
        continue;
      }
      if (!im.alone) use.why = 'its import brings more than the picture';
      else if (usedBefore(sf, local, lastImport)) use.why = 'the code uses it before its imports end';
      addCandidate(im.file, use, { st: im.st, local });
    }
    if (mine.length) internal.set(f, mine);
  }
  // Names: a file a setting already names keeps that name; new ones get theirs
  // (files nearer the top first: public/hero.png is HERO, public/art/hero.png ART_HERO).
  const taken = new Set(named.keys());
  const fileName = new Map();
  for (const e of named.values()) if (e.file && !fileName.has(e.file)) fileName.set(e.file, e.name);
  const depth = (f) => f.split('/').length;
  for (const file of [...candidates.keys()].sort((a, b) => depth(a) - depth(b) || a.localeCompare(b))) {
    if (!fileName.has(file)) fileName.set(file, nameFor(file, taken));
    const name = fileName.get(file);
    const e = named.get(name) ?? { name, file, uses: [] };
    e.uses.push(...candidates.get(file));
    named.set(name, e);
  }
  for (const xs of internal.values()) for (const x of xs) x.name = fileName.get(x.file);
  const pictures = [...named.values()]
    .map((e) => ({ name: e.name, file: e.file, setting: e.uses.some((u) => u.wrapped), uses: e.uses.sort((a, b) => a.file.localeCompare(b.file) || a.line - b.line) }))
    .sort((a, b) => (a.file || '~').localeCompare(b.file || '~') || a.name.localeCompare(b.name));
  return { pictures: pictures.slice(0, 500), left: left.slice(0, 200), helper, internal };
};

/** Is a name used by a statement before the last import (where a const added after the imports would not exist yet)? */
const usedBefore = (sf, local, lastImport) => {
  if (!lastImport) return false;
  let found = false;
  for (const st of sf.statements) {
    if (st === lastImport) break;
    if (ts.isImportDeclaration(st)) continue;
    const walk = (n) => {
      if (found) return;
      if (ts.isIdentifier(n) && n.text === local) found = true;
      else ts.forEachChild(n, walk);
    };
    walk(st);
  }
  return found;
};

/** The pictures each code file draws, by name ({file: [names]}), for the compositions that reach those files. */
export const pictureNamesByFile = (scan) => {
  const out = {};
  for (const p of scan.pictures) {
    for (const u of p.uses) {
      if (!out[u.file]) out[u.file] = new Set();
      out[u.file].add(p.name);
    }
  }
  return Object.fromEntries(Object.entries(out).map(([f, s]) => [f, [...s].sort()]));
};

/** Where the helper goes: next to the files it is used in (their common folder). */
const helperPathFor = (files) => {
  const dirs = files.map((f) => path.posix.dirname(f));
  let common = dirs[0] ?? 'src';
  for (const d of dirs) while (!(d === common || d.startsWith(common + '/')) && common !== '.') common = path.posix.dirname(common);
  return (common === '.' ? '' : common + '/') + PICTURES_HELPER_NAME + '.ts';
};

/**
 * The one-time change (D209): each picture reference that isn't a setting yet
 * wrapped — staticFile("hero.png") becomes picture("HERO", staticFile("hero.png"));
 * an imported picture's import is renamed (hero → heroAsWritten) and
 * `const hero = picture("HERO", heroAsWritten);` follows the imports — the
 * helper imported, the helper written. want limits it to some names. Returns
 * {files: [{path, text}], helper, pictures: [{name, file, uses}], skipped:
 * [{file, line, why}]}. Spans are replaced in the original text, so
 * formatting and comments stay as they were.
 */
export const setupPictures = (P, want = []) => {
  const scan = scanPictures(P);
  const pick = (name) => !want?.length || want.includes(name);
  const skipped = [];
  const work = []; // [file, uses]
  for (const [f, xs] of scan.internal) {
    const todo = [];
    for (const x of xs) {
      if (!pick(x.name)) continue;
      if (x.use.why) skipped.push({ file: f, line: x.use.line, name: x.name, why: x.use.why });
      else todo.push(x);
    }
    if (todo.length) work.push([f, todo]);
  }
  if (!work.length) return { files: [], helper: scan.helper, pictures: [], skipped };
  const helper = scan.helper || helperPathFor(work.map(([f]) => f));
  const files = [];
  const done = new Map();
  for (const [f, todo] of work) {
    const sf = P.sources.get(f);
    const facts = fileFacts(f, sf);
    const taken = identifiers(sf);
    let fn = [...facts.helperFns][0];
    const addImport = !fn;
    if (!fn) fn = taken.has('picture') ? unique('metarushPicture', taken) : 'picture';
    const edits = [];
    const consts = [];
    for (const x of todo) {
      if (x.node) {
        edits.push({ start: x.node.getStart(sf), end: x.node.getEnd(), text: `${fn}(${JSON.stringify(x.name)}, ${x.node.getText(sf)})` });
      } else {
        const asWritten = unique(`${x.local}AsWritten`, taken);
        const id = x.st.importClause.name;
        edits.push({ start: id.getStart(sf), end: id.getEnd(), text: asWritten });
        consts.push(`const ${x.local} = ${fn}(${JSON.stringify(x.name)}, ${asWritten});`);
      }
      const d = done.get(x.name) ?? { name: x.name, file: x.file, uses: 0 };
      d.uses++;
      done.set(x.name, d);
    }
    let rel = path.posix.relative(path.posix.dirname(f), helper.replace(/\.ts$/, ''));
    if (!rel.startsWith('.')) rel = './' + rel;
    let insert = '';
    if (addImport) insert += `\nimport { ${fn === 'picture' ? 'picture' : `picture as ${fn}`} } from ${JSON.stringify(rel)};`;
    for (const c of consts) insert += `\n${c}`;
    const imports = sf.statements.filter(ts.isImportDeclaration);
    const end = imports.length ? imports[imports.length - 1].getEnd() : 0;
    // Every file with a picture reference imports remotion or the picture, so it has imports.
    if (insert) edits.push({ start: end, end, text: insert });
    let text = sf.text;
    for (const e of edits.sort((a, b) => b.start - a.start)) text = text.slice(0, e.start) + e.text + text.slice(e.end);
    files.push({ path: f, text });
  }
  if (!scan.helper) files.push({ path: helper, text: PICTURES_HELPER_TEXT });
  return { files, helper, pictures: [...done.values()], skipped };
};

/** How many times an identifier is written in a file. */
const countIdent = (sf, name) => {
  let n = 0;
  const walk = (x) => {
    if (ts.isIdentifier(x) && x.text === name) n++;
    ts.forEachChild(x, walk);
  };
  walk(sf);
  return n;
};

/**
 * Taking the helper out again (D176, sending changes back): every
 * picture("NAME", value) call becomes its value, the consts setup added after
 * the imports go and their imports get their names back, the helper's import
 * goes, and so does the helper. Returns {files: [{path, text}], removed:
 * [helper], problems: []}: the files exactly as they were before the change
 * when nobody edited those lines since.
 */
export const bakePictures = (P) => {
  const { helper } = scanPictures(P);
  const files = [];
  const problems = [];
  for (const [f, sf] of P.sources) {
    if (f === helper) continue;
    const facts = fileFacts(f, sf);
    if (!facts.helperFns.size && !facts.helperImports.length) continue;
    const edits = [];
    const handled = new Set();
    // const hero = picture("HERO", heroAsWritten): the import gets its name back.
    for (const st of sf.statements) {
      if (!ts.isVariableStatement(st) || (ts.getModifiers(st) ?? []).length || !(st.declarationList.flags & ts.NodeFlags.Const)) continue;
      const ds = st.declarationList.declarations;
      if (ds.length !== 1 || !ts.isIdentifier(ds[0].name) || !ds[0].initializer) continue;
      const init = ds[0].initializer;
      if (!isPictureCall(init, facts) || !ts.isIdentifier(init.arguments[1])) continue;
      const m = init.arguments[1].text;
      const im = facts.pictureImports.get(m);
      if (!im || countIdent(sf, m) !== 2) continue; // used elsewhere too: unwrap the call only
      let start = st.getStart(sf);
      if (sf.text[start - 1] === '\n') start -= 1;
      edits.push({ start, end: st.getEnd(), text: '' });
      const id = im.st.importClause.name;
      edits.push({ start: id.getStart(sf), end: id.getEnd(), text: ds[0].name.text });
      handled.add(init);
    }
    const visit = (node) => {
      if (isPictureCall(node, facts)) {
        if (!handled.has(node)) edits.push({ start: node.getStart(sf), end: node.getEnd(), text: node.arguments[1].getText(sf) });
        return;
      }
      ts.forEachChild(node, visit);
    };
    visit(sf);
    for (const im of facts.helperImports) {
      if (!im.only) {
        problems.push({ file: f, message: 'its import of the pictures helper brings more than picture(); MetaRush left it as it is' });
        continue;
      }
      let start = im.st.getStart(sf);
      let end = im.st.getEnd();
      const prev = sf.statements[im.i - 1];
      if (prev && ts.isImportDeclaration(prev) && sf.text[start - 1] === '\n') start -= 1;
      else if (sf.text.slice(end, end + 2) === '\n\n') end += 2;
      else if (sf.text[end] === '\n') end += 1;
      edits.push({ start, end, text: '' });
    }
    let text = sf.text;
    for (const e of edits.sort((a, b) => b.start - a.start)) text = text.slice(0, e.start) + e.text + text.slice(e.end);
    files.push({ path: f, text });
  }
  return { files, removed: helper ? [helper] : [], problems };
};
