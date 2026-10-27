#!/usr/bin/env node
// The dashboard speaks plainly (skills/_shared/voice.md): it reads every
// string a person sees in web/src — JSX text, visible attributes (title,
// placeholder, aria-label, alt, label…), dialog and toast texts, error
// messages, label maps — and checks it with the rules Go uses
// (internal/plainwords/rules.json): no internal IDs, flags, commands, file
// paths, JSON, internal codes or unexplained jargon. It also flags JSX that
// shows an ID field ({job.id}). Text inside a <Details> disclosure is for
// people who asked for the technical detail and is not checked. Intentional
// exceptions are in plain-words-allow.txt, each with its reason.
//
//   node web/scripts/plain-words-test.mjs [--report file]
import { readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';
import { check, checkShown } from './plainwords.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const src = path.join(here, '..', 'src');

// People's own words, marked in what is shown, are theirs (D113): the same cases
// as the Go checker (internal/plainwords/testdata/theirs.json), so both agree.
{
  const cases = JSON.parse(readFileSync(path.join(here, '..', '..', 'internal', 'plainwords', 'testdata', 'theirs.json'), 'utf8'));
  const bad = [];
  for (const s of cases.shown) {
    const got = checkShown(s);
    if (got.length) bad.push(`${JSON.stringify(s)} flagged: ${got.map((f) => `${f.kind} ${f.text}`).join('; ')}`);
  }
  for (const [s, want] of Object.entries(cases.still)) {
    const got = checkShown(s).map((f) => `${f.kind} ${f.text}`);
    if (!got.includes(want)) bad.push(`${JSON.stringify(s)}: got ${JSON.stringify(got)}, want ${want}`);
  }
  for (const b of bad) console.error('FAIL theirs:', b);
  if (bad.length) process.exit(1);
  console.log(`people's own words: ${cases.shown.length + Object.keys(cases.still).length} cases agree with the Go checker`);
}

// Attributes and object keys whose string values people read.
const VISIBLE = new Set([
  'title', 'placeholder', 'aria-label', 'aria-description', 'aria-valuetext', 'alt', 'label', 'helper', 'hint', 'tooltip', 'body', 'message',
  'text', 'caption', 'confirmLabel', 'cancelLabel', 'okLabel', 'emptyLabel', 'emptyText', 'description', 'summary', 'subtitle', 'heading', 'blurb',
  'lead', 'advice', 'question', 'explanation', 'says', 'detail', 'note', 'meta', 'empty', 'action', 'cta', 'headline', 'sub', 'what', 'why', 'next',
]);
// Components whose children are technical on purpose (Details) or are people's
// own words (Theirs: titles, file names, names; D113).
const DISCLOSURES = new Set(['Details', 'Theirs']);

function files(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = path.join(dir, name);
    if (statSync(p).isDirectory()) out.push(...files(p));
    else if (/\.(tsx|ts)$/.test(name) && !/\.test\.tsx?$/.test(name) && !name.endsWith('.d.ts')) out.push(p);
  }
  return out;
}

function inDisclosure(node) {
  for (let n = node.parent; n; n = n.parent) {
    if (ts.isJsxElement(n)) {
      const tag = n.openingElement.tagName.getText();
      if (DISCLOSURES.has(tag)) return true;
    }
  }
  return false;
}

// A string expression's literal text, with "x" for each value put into it.
function literal(node) {
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text;
  if (ts.isTemplateExpression(node)) return node.head.text + node.templateSpans.map((s) => 'x' + s.literal.text).join('');
  return null;
}

function sentenceLike(s) {
  const t = s.trim();
  if (!t.includes(' ')) return false;
  // Tailwind classes and other code-ish strings: lowercase tokens with -, :, [, /.
  if (/^[a-z0-9!:\[\]\-\/.#%_()=,'"@>*&+ ]+$/.test(t) && /[-:\[]/.test(t) && !/[.?!…]$/.test(t)) return false;
  return /^[A-Z“"‘(¿0-9]/.test(t) || /[.?!…:]$/.test(t);
}

const idField = /(^|\.)(id|[a-z][A-Za-z]*Id|[A-Z][A-Za-z]*ID|ID)$/;

function scan(file) {
  const text = readFileSync(file, 'utf8');
  const sf = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, file.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
  const found = [];
  const rel = path.relative(path.join(here, '..'), file);
  const put = (node, s, where) => {
    handled.add(node);
    if (!s || !s.trim() || inDisclosure(node)) return;
    for (const f of check(s)) {
      const line = sf.getLineAndCharacterOfPosition(node.getStart()).line + 1;
      found.push({ file: rel, line, text: s.trim().replace(/\s+/g, ' '), where, ...f });
    }
  };
  const visit = (node) => {
    if (ts.isJsxText(node)) {
      put(node, node.text, 'text');
    } else if (ts.isJsxAttribute(node)) {
      const name = node.name.getText();
      const init = node.initializer;
      if (init && VISIBLE.has(name)) {
        if (ts.isStringLiteral(init)) put(init, init.text, name);
        else if (ts.isJsxExpression(init) && init.expression) visitValue(init.expression, name);
      }
    } else if (ts.isJsxExpression(node) && node.expression && (ts.isJsxElement(node.parent) || ts.isJsxFragment(node.parent))) {
      visitValue(node.expression, 'text');
      const e = node.expression;
      const shown = ts.isPropertyAccessExpression(e) || ts.isIdentifier(e) ? e.getText() : '';
      if (shown && idField.test(shown) && !inDisclosure(node)) {
        const line = sf.getLineAndCharacterOfPosition(node.getStart()).line + 1;
        found.push({ file: rel, line, text: `{${shown}}`, where: 'text', kind: 'id', plain: 'show its name or title; keep the ID in Details' });
      }
    } else if (ts.isPropertyAssignment(node) && (ts.isIdentifier(node.name) || ts.isStringLiteral(node.name)) && VISIBLE.has(node.name.text)) {
      visitValue(node.initializer, node.name.text);
    } else if (ts.isNewExpression(node) && node.expression.getText() === 'Error' && node.arguments?.length) {
      visitValue(node.arguments[0], 'error');
    } else if ((ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node) || ts.isTemplateExpression(node)) && !handled.has(node) && !codeContext(node)) {
      // Any other sentence: a label map, a returned phrase, a list of tips.
      const s = literal(node);
      if (s && sentenceLike(s)) put(node, s, labelMapValue(node) ? 'label map' : 'string');
    }
    ts.forEachChild(node, visit);
  };
  // Strings that are code, not words: imports, class names, keys, comparisons, URLs, query keys.
  const codeContext = (node) => {
    const p = node.parent;
    if (!p) return true;
    if (ts.isImportDeclaration(p) || ts.isExportDeclaration(p) || ts.isExternalModuleReference(p)) return true;
    if (ts.isJsxAttribute(p) || (ts.isJsxExpression(p) && p.parent && ts.isJsxAttribute(p.parent))) return true; // visible ones are handled above
    if (ts.isBinaryExpression(p) && [ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsEqualsToken, ts.SyntaxKind.EqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsToken].includes(p.operatorToken.kind)) return true;
    if (ts.isCaseClause(p) || ts.isElementAccessExpression(p) || ts.isLiteralTypeNode(p) || ts.isPropertyAssignment(p) && p.name === node) return true;
    if (ts.isCallExpression(p)) {
      const callee = p.expression.getText();
      if (/(^|\.)(cx|clsx|classNames|querySelector|querySelectorAll|getElementById|addEventListener|removeEventListener|setAttribute|getAttribute|startsWith|endsWith|includes|split|replace|replaceAll|match|matchAll|test|join|indexOf|fetch|get|set|has|delete|append|call|useOp|useQuery|invalidateQueries|navigate|setParam|encodeURIComponent|RegExp|Intl\.\w+|toLocaleString|toLocaleDateString|toLocaleTimeString|localStorage\.\w+|sessionStorage\.\w+|console\.\w+)$/.test(callee)) return true;
    }
    return false;
  };
  const handled = new Set();
  // Strings that are values of a const map of labels or blurbs (Record<…, string>).
  const labelMapValue = (node) => {
    const p = node.parent;
    if (!p || !ts.isPropertyAssignment(p) || p.initializer !== node) return false;
    if (ts.isIdentifier(p.name) && VISIBLE.has(p.name.text)) return false; // handled above
    const obj = p.parent;
    return ts.isObjectLiteralExpression(obj) && obj.parent && ts.isVariableDeclaration(obj.parent);
  };
  const visitValue = (e, where) => {
    if (!e) return;
    const s = literal(e);
    if (s !== null) return put(e, s, where);
    if (ts.isConditionalExpression(e)) {
      visitValue(e.whenTrue, where);
      visitValue(e.whenFalse, where);
    } else if (ts.isBinaryExpression(e) && [ts.SyntaxKind.BarBarToken, ts.SyntaxKind.QuestionQuestionToken, ts.SyntaxKind.AmpersandAmpersandToken, ts.SyntaxKind.PlusToken].includes(e.operatorToken.kind)) {
      visitValue(e.left, where);
      visitValue(e.right, where);
    } else if (ts.isParenthesizedExpression(e)) {
      visitValue(e.expression, where);
    }
  };
  visit(sf);
  return found;
}

function loadAllow() {
  const p = path.join(here, 'plain-words-allow.txt');
  const out = [];
  for (const raw of readFileSync(p, 'utf8').split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const parts = line.split(' | ');
    if (parts.length !== 3 || !parts[2].trim()) throw new Error(`plain-words-allow.txt: ${JSON.stringify(line)}: want "file | text | reason"`);
    out.push({ file: parts[0].trim(), text: parts[1].trim(), reason: parts[2].trim(), used: false });
  }
  return out;
}

const allow = loadAllow();
const bad = [];
for (const f of files(src)) {
  for (const x of scan(f)) {
    const a = allow.find((a) => (a.file === x.file || (a.file.endsWith('/') && x.file.startsWith(a.file))) && (a.text === '*' || x.text.includes(a.text)));
    if (a) {
      a.used = true;
      continue;
    }
    bad.push(`${x.file}:${x.line}: ${x.where}: ${x.kind} ${JSON.stringify(x.text.length > 160 ? x.text.slice(0, 157) + '…' : x.text)} → ${JSON.stringify(x.kind === 'jargon' || x.kind === 'id' ? x.plain : x.text)}`);
  }
}
const unused = allow.filter((a) => !a.used);
const report = process.argv.indexOf('--report');
if (report > 0) writeFileSync(process.argv[report + 1], bad.join('\n') + '\n');
for (const b of bad) console.error('FAIL', b);
for (const a of unused) console.error(`FAIL plain-words-allow.txt: ${a.file} | ${a.text} matches nothing any more; remove it`);
if (bad.length || unused.length) {
  console.error(`${bad.length} strings in the dashboard are not plain words (see skills/_shared/voice.md), ${unused.length} stale allowlist entries`);
  process.exit(1);
}
console.log('dashboard copy: plain words ✓');
