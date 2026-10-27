// The project map's facts from the code (D167): where the words, pictures,
// sounds, fonts and data files are, file by file and line by line, and the
// scenes each composition lays out (Sequence, Series, TransitionSeries) with
// what sets their lengths. Nothing is run: the jailed probe (probe/*.mjs) says
// where the scenes really are and what is really on screen; Go joins the two.
import fs from 'node:fs';
import path from 'node:path';
import ts from 'typescript';
import { bare, lineOf, notWordsPlace, propName, stringOf, wordy } from './project.mjs';

const SOUND_TAGS = new Set(['Audio', 'Html5Audio']);
const PICTURE_TAGS = new Set(['Img', 'Image', 'img', 'IFrame']);
const VIDEO_TAGS = new Set(['Video', 'OffthreadVideo', 'Html5Video', 'video']);

/** The tag's last part (TransitionSeries.Sequence → Sequence, its owner TransitionSeries). */
const tagParts = (sf, open) => {
  const t = open.tagName.getText(sf);
  const i = t.lastIndexOf('.');
  return i < 0 ? { owner: '', tag: t } : { owner: t.slice(0, i), tag: t.slice(i + 1) };
};

/** The path to a string inside an exported constant (END.tagline, ACTS.0), or ''. */
export const keyPath = (node) => {
  const parts = [];
  let n = node;
  for (let depth = 0; n && depth < 24; depth++) {
    const p = n.parent;
    if (!p) return '';
    if (ts.isPropertyAssignment(p) && p.initializer === n) {
      const k = propName(p);
      if (k === undefined) return '';
      parts.unshift(k);
      n = p.parent; // the object literal
      continue;
    }
    if (ts.isArrayLiteralExpression(p)) {
      parts.unshift(String(p.elements.indexOf(n)));
      n = p;
      continue;
    }
    if (ts.isParenthesizedExpression(p) || ts.isAsExpression(p) || ts.isSatisfiesExpression?.(p) || ts.isTypeAssertionExpression?.(p)) {
      n = p;
      continue;
    }
    if (ts.isCallExpression(p) && ts.isIdentifier(p.expression) && p.expression.text === 'words' && p.arguments[1] === n) {
      n = p; // words("END", {…}): the constant's own name comes next
      continue;
    }
    if (ts.isVariableDeclaration(p) && p.initializer === n && ts.isIdentifier(p.name)) {
      const st = p.parent?.parent;
      if (!st || !ts.isVariableStatement(st) || st.parent !== node.getSourceFile()) return '';
      return [p.name.text, ...parts].join('.');
    }
    return '';
  }
  return '';
};

/** A JSON data file: how big, whether it holds timings, and the texts it names files with. */
const dataFacts = (root, file) => {
  let raw;
  try {
    raw = fs.readFileSync(path.join(root, file), 'utf8');
  } catch {
    return null;
  }
  let doc;
  try {
    doc = JSON.parse(raw);
  } catch {
    return { file, bytes: raw.length, kind: 'data', texts: [], strings: [] };
  }
  let timings = 0;
  const texts = [];
  const strings = [];
  const walk = (v, depth) => {
    if (depth > 12 || strings.length > 4000) return;
    if (Array.isArray(v)) {
      for (const x of v) walk(x, depth + 1);
      return;
    }
    if (v && typeof v === 'object') {
      const keys = Object.keys(v);
      if (keys.some((k) => /^(start|end|duration|from|to|frames?|seconds|time|at|offset)$/i.test(k) && typeof v[k] === 'number')) timings++;
      // An object that names a sound or picture file and says its words: {text, file}.
      const fileKey = keys.find((k) => typeof v[k] === 'string' && /\.(mp3|wav|m4a|aac|ogg|flac|png|jpe?g|webp|gif|svg|mp4|webm|mov)$/i.test(v[k]));
      const textKey = keys.find((k) => /^(text|line|words|caption|title|script|transcript)$/i.test(k) && typeof v[k] === 'string');
      if (fileKey && textKey) texts.push({ file: String(v[fileKey]).replace(/^\/+/, ''), text: v[textKey] });
      for (const k of keys) walk(v[k], depth + 1);
      return;
    }
    if (typeof v === 'string' && wordy(v)) strings.push(v);
  };
  walk(doc, 0);
  return { file, bytes: raw.length, kind: timings >= 3 ? 'timings' : 'data', timings, texts: texts.slice(0, 500), strings: [...new Set(strings)].slice(0, 2000) };
};

/** Everything the map needs to know from the code, per file and per composition. */
export const mapFacts = (P, compositions) => {
  const { root, sources } = P;
  const files = {};
  const sceneElems = [];
  for (const [f, sf] of sources) {
    const words = [];
    const pictures = [];
    const sounds = [];
    const videos = [];
    const objects = []; // object literals naming a scene ({ id: "create", lead: 0.3, tail: 1.3 })
    const visit = (node, inMap) => {
      // Inside .map(…) a JSX element is a template for many.
      let nextInMap = inMap;
      if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression) && ['map', 'flatMap', 'forEach'].includes(node.expression.name.text)) nextInMap = true;
      if ((ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) && wordy(node.text) && !notWordsPlace(node)) {
        words.push({ text: node.text, line: lineOf(sf, node), key: keyPath(node) });
      } else if (ts.isJsxText(node)) {
        const t = node.text.replace(/\s+/g, ' ').trim();
        if (wordy(t)) words.push({ text: t, line: lineOf(sf, node), key: '' });
      } else if (ts.isObjectLiteralExpression(node)) {
        const props = node.properties.filter(ts.isPropertyAssignment);
        const idp = props.find((p) => /^(id|name|scene|key|slug)$/.test(propName(p) ?? '') && stringOf(p.initializer) !== undefined);
        const nums = props.filter((p) => ts.isNumericLiteral(bare(p.initializer)) || (ts.isPrefixUnaryExpression(p.initializer) && ts.isNumericLiteral(p.initializer.operand)));
        if (idp && nums.length >= 1 && props.length <= 12) {
          objects.push({ id: stringOf(idp.initializer), line: lineOf(sf, node), text: node.getText(sf).replace(/\s+/g, ' ').slice(0, 240), owner: ownerName(node) });
        }
      }
      if (ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node)) {
        const open = ts.isJsxElement(node) ? node.openingElement : node;
        const { owner, tag } = tagParts(sf, open);
        const attr = (k) => open.attributes.properties.find((p) => ts.isJsxAttribute(p) && p.name.getText(sf) === k);
        const attrText = (k) => {
          const a = attr(k);
          return a && ts.isJsxAttribute(a) && a.initializer ? a.initializer.getText(sf).replace(/^\{|\}$/g, '').slice(0, 200) : '';
        };
        const srcFile = () => {
          const a = attr('src');
          let e = a && ts.isJsxAttribute(a) ? a.initializer : undefined;
          while (e && ts.isJsxExpression(e)) e = e.expression;
          if (e && ts.isCallExpression(e) && ts.isIdentifier(e.expression) && e.expression.text === 'staticFile' && e.arguments[0]) {
            const s = stringOf(e.arguments[0]);
            if (s !== undefined) return s.replace(/^\/+/, '');
            if (ts.isTemplateExpression(e.arguments[0])) return e.arguments[0].head.text.replace(/^\/+/, '') + '*';
          }
          return '';
        };
        if (SOUND_TAGS.has(tag)) sounds.push({ line: lineOf(sf, open), tag, src: attrText('src'), file: srcFile(), volume: attrText('volume'), start: attrText('trimBefore') || attrText('startFrom'), inMap });
        else if (PICTURE_TAGS.has(tag)) pictures.push({ line: lineOf(sf, open), src: attrText('src'), file: srcFile() });
        else if (VIDEO_TAGS.has(tag)) videos.push({ line: lineOf(sf, open), src: attrText('src'), file: srcFile() });
        const isSeries = (owner === 'TransitionSeries' || owner === 'Series') && tag === 'Sequence';
        if (isSeries || (owner === '' && tag === 'Sequence')) {
          const kind = owner === 'TransitionSeries' ? 'transition-series' : owner === 'Series' ? 'series' : 'sequence';
          const dur = attr('durationInFrames');
          const from = attr('from');
          const durWhere = dur && ts.isJsxAttribute(dur) && dur.initializer ? P.numberWhere(f, dur.initializer) : undefined;
          const fromWhere = from && ts.isJsxAttribute(from) && from.initializer ? P.numberWhere(f, from.initializer) : undefined;
          let child = '';
          let childFile = '';
          let childProps = [];
          if (ts.isJsxElement(node)) {
            for (const c of node.children) {
              if (ts.isJsxElement(c) || ts.isJsxSelfClosingElement(c)) {
                const co = ts.isJsxElement(c) ? c.openingElement : c;
                child = co.tagName.getText(sf);
                childFile = resolveTag(P, f, child)?.file ?? '';
                childProps = co.attributes.properties.filter(ts.isJsxAttribute).map((p) => ({ name: p.name.getText(sf), text: p.initializer ? p.initializer.getText(sf).replace(/^\{|\}$/g, '').slice(0, 200) : 'true' }));
                break;
              }
            }
          }
          sceneElems.push({
            file: f,
            line: lineOf(sf, open),
            kind,
            name: stringOf(attr('name')?.initializer) ?? '',
            child: child.includes('.') ? child.split('.').pop() : child,
            childFile,
            childProps,
            inMap,
            duration: dur ? { text: attrText('durationInFrames'), frames: durWhere?.value ?? null, file: durWhere?.file ?? f, line: durWhere?.line ?? lineOf(sf, dur) } : null,
            from: from ? { text: attrText('from'), frames: fromWhere?.value ?? null, file: fromWhere?.file ?? f, line: fromWhere?.line ?? lineOf(sf, from) } : null,
          });
        }
        if (owner === 'TransitionSeries' && tag === 'Transition') {
          const t = attr('timing');
          let frames = null;
          let where = null;
          let e = t && ts.isJsxAttribute(t) ? t.initializer : undefined;
          while (e && ts.isJsxExpression(e)) e = e.expression;
          if (e && ts.isCallExpression(e) && e.arguments[0] && ts.isObjectLiteralExpression(e.arguments[0])) {
            const dp = e.arguments[0].properties.find((p) => ts.isPropertyAssignment(p) && propName(p) === 'durationInFrames');
            if (dp) {
              const w = P.numberWhere(f, dp.initializer);
              frames = w?.value ?? null;
              where = w ? { file: w.file, line: w.line } : null;
            }
          }
          sceneElems.push({ file: f, line: lineOf(sf, open), kind: 'transition', name: '', child: '', childFile: '', childProps: [], inMap, duration: { text: attrText('timing'), frames, file: where?.file ?? f, line: where?.line ?? lineOf(sf, open) }, from: null });
        }
      }
      ts.forEachChild(node, (c) => visit(c, nextInMap));
    };
    visit(sf, false);
    const decl = P.declared.get(f) ?? new Map();
    files[f] = {
      words: dedupeWords(words).slice(0, 1500),
      pictures,
      sounds,
      videos,
      objects: objects.slice(0, 200),
      fonts: (P.fontFiles.get(f) ?? []).map((x) => x.family),
      data: P.json.get(f) ?? [],
      components: [...decl.entries()].filter(([, d]) => isComponentDecl(d.node)).map(([name, d]) => ({ name, line: d.line, exported: d.exported })),
    };
  }
  const dataFiles = [...new Set(Object.values(files).flatMap((x) => x.data))].sort();
  const data = dataFiles.map((d) => dataFacts(root, d)).filter(Boolean);
  const comps = compositions.map((c) => ({
    id: c.id,
    file: c.file,
    line: c.line,
    component: c.component,
    componentName: c.componentName ?? '',
    files: c.files,
    data: [...new Set(c.files.flatMap((f) => files[f]?.data ?? []))].sort(),
    durationAt: c.durationAt ?? null,
    calculateMetadata: !!c.calculateMetadata,
    defaultProps: c.defaultProps ?? '',
    // How far each of its files is from its component (scenes written closest to it are its own).
    distance: c.component ? P.distance(c.component) : {},
  }));
  return { type: 'map', files, scenes: sceneElems, data, compositions: comps };
};

const dedupeWords = (ws) => {
  const seen = new Set();
  const out = [];
  for (const w of ws) {
    const k = `${w.line}\u0000${w.text}`;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(w);
  }
  return out;
};

/** The declaration a JSX tag names in file f (<End/>, <T.Hook/> through a namespace import). */
export const resolveTag = (P, f, tag) => {
  const i = tag.indexOf('.');
  if (i < 0) return P.declOf(f, tag);
  const ns = tag.slice(0, i);
  const member = tag.slice(i + 1);
  const b = P.bindings.get(f)?.get(ns);
  if (b?.from && b.name === '*') return P.declOf(b.from, member);
  return null;
};

/** The name of the constant or function an object literal sits in (TEASER_PLAN, teaser). */
const ownerName = (node) => {
  for (let n = node.parent; n; n = n.parent) {
    if (ts.isVariableDeclaration(n) && ts.isIdentifier(n.name)) return n.name.text;
    if ((ts.isFunctionDeclaration(n) || ts.isMethodDeclaration(n)) && n.name && ts.isIdentifier(n.name)) return n.name.text;
  }
  return '';
};

/** A declaration that is a React component: a function (or arrow) whose name starts with a capital. */
const isComponentDecl = (node) => {
  if (!node) return false;
  if (ts.isFunctionDeclaration(node)) return !!node.name && /^[A-Z]/.test(node.name.text);
  if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && /^[A-Z]/.test(node.name.text) && node.initializer) {
    let i = bare(node.initializer);
    if (ts.isCallExpression(i) && i.arguments[0]) i = bare(i.arguments[0]); // forwardRef(…), memo(…)
    return ts.isArrowFunction(i) || ts.isFunctionExpression(i);
  }
  return false;
};
