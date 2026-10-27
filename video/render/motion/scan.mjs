// The import scan (D68): every .ts/.tsx file of a motion graphic is parsed
// with the TypeScript compiler (never matched with regular expressions) and
// walked. It lists every import and refuses anything outside the allowlist,
// dynamic import(), require, eval, new Function, Node built-ins, network APIs
// and code-from-text tricks, with plain, line-numbered problems. Runs before
// bundling, in `motion check` and again in every render.
import path from 'node:path';
import ts from 'typescript';
import { FORBIDDEN_GLOBALS, NONDETERMINISTIC, classify, tsSettingsOf } from './policy.mjs';

const CODE = /\.(tsx|ts)$/;
const GLOBAL_OBJECTS = new Set(['window', 'globalThis', 'self', 'top', 'parent', 'frames', 'navigator', 'document']);
const EXTS = ['', '.tsx', '.ts', '.json', '/index.tsx', '/index.ts'];

/** Is this identifier used as a value (not a type, a declaration name or a property key)? */
const isValueUse = (id) => {
  const p = id.parent;
  if (!p) return true;
  if (ts.isTypeReferenceNode(p) || ts.isTypeQueryNode(p) || ts.isQualifiedName(p) || ts.isExpressionWithTypeArguments(p)) {
    // `typeof fetch` in a type, `Function` as a type: no code runs.
    return false;
  }
  for (let n = p; n; n = n.parent) if (ts.isTypeNode(n)) return false;
  if (ts.isPropertyAccessExpression(p) && p.name === id) {
    // window.fetch, globalThis.eval, navigator.sendBeacon: a global reached
    // through a global object; opts.fetch is just a property. eval and
    // Function are refused on any object (x.eval, x.Function).
    const obj = p.expression;
    return id.text === 'eval' || id.text === 'Function' || (ts.isIdentifier(obj) && GLOBAL_OBJECTS.has(obj.text));
  }
  if ((ts.isPropertyAssignment(p) || ts.isPropertyDeclaration(p) || ts.isMethodDeclaration(p) || ts.isPropertySignature(p)) && p.name === id) {
    return false; // {fetch: 1} is a key
  }
  if ((ts.isVariableDeclaration(p) || ts.isParameter(p) || ts.isFunctionDeclaration(p) || ts.isBindingElement(p) || ts.isImportSpecifier(p) || ts.isJsxAttribute(p)) && p.name === id) return false;
  return true;
};

const lineOf = (sf, node) => {
  const { line, character } = sf.getLineAndCharacterOfPosition(node.getStart(sf));
  return { line: line + 1, col: character + 1 };
};

/** A resource's short name, or a PDF page / web page picture (name#3). */
const RESOURCE_NAME = /^[a-z][a-z0-9_-]{0,47}(#[1-9][0-9]{0,2})?$/;

/**
 * Where useResource's argument comes from (D107): a name in quotes, or a
 * parameter (params.x, props.params.x). Returns {name} | {param} | null.
 */
const resourceArg = (arg) => {
  if (!arg) return null;
  if (ts.isStringLiteral(arg) || ts.isNoSubstitutionTemplateLiteral(arg)) return { name: arg.text };
  if (ts.isPropertyAccessExpression(arg)) {
    const obj = arg.expression;
    if ((ts.isIdentifier(obj) && obj.text === 'params') || (ts.isPropertyAccessExpression(obj) && obj.name.text === 'params')) return { param: arg.name.text };
  }
  return null;
};

/**
 * Scans the component sources: files is [{path, text}] (paths relative to the
 * component folder, "/"-separated). Returns {imports, problems, exports,
 * resources}: resources are the pictures its code draws through useResource,
 * by name ({name}) or through a parameter ({param}).
 */
export const scanFiles = (files) => {
  const names = new Set(files.map((f) => f.path));
  const imports = [];
  const problems = [];
  const resources = [];
  const libraries = new Set();
  const exports = { default: false, schema: false, meta: false };
  // A shared library (D171): a folder with library.json, imported by graphics as @metarush/lib/<name>.
  const library = names.has('library.json');
  const add = (sev, code, file, at, message, hint) => problems.push({ severity: sev, code, file, line: at?.line, col: at?.col, message, hint });

  const resolveRelative = (from, spec) => {
    const target = path.posix.normalize(path.posix.join(path.posix.dirname(from), spec));
    if (target.startsWith('../') || target === '..' || path.posix.isAbsolute(target)) return { outside: true };
    for (const ext of EXTS) {
      if (names.has(target + ext)) return { file: target + ext };
    }
    return { missing: target };
  };

  const checkSpecifier = (sf, file, node, spec, typeOnly) => {
    const at = lineOf(sf, node);
    const c = classify(spec);
    if (c.problem) {
      add('error', 'import_not_allowed', file, at, c.problem, c.hint);
      return;
    }
    if (c.kind === 'library') {
      if (library) {
        add('error', 'library_in_library', file, at, `${spec}: a shared library can't use another shared library.`, 'Put what both need into one library.');
        return;
      }
      if (!typeOnly) libraries.add(c.name);
    }
    if (c.kind === 'relative') {
      const r = resolveRelative(file, spec);
      if (r.outside) {
        add('error', 'import_outside', file, at, `${spec} points outside the component's folder.`, 'A motion graphic can only import files in its own folder (and the allowed packages).');
        return;
      }
      if (r.missing) {
        add('error', 'import_missing', file, at, `${spec}: there is no ${r.missing}(.tsx|.ts|.json) in the component's folder.`, '');
        return;
      }
    }
    if (!typeOnly) imports.push({ specifier: spec, file, line: at.line, kind: c.kind });
  };

  for (const f of files) {
    if (!CODE.test(f.path)) continue;
    const kind = f.path.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
    const sf = ts.createSourceFile(f.path, f.text, ts.ScriptTarget.ES2022, true, kind);
    // How this file can reach useResource: its own name for the import, or
    // the namespace @metarush/motion was imported as.
    const resAliases = new Set();
    const fontAliases = new Set();
    const nsAliases = new Set();
    for (const st of sf.statements) {
      if (!ts.isImportDeclaration(st) || !ts.isStringLiteral(st.moduleSpecifier) || st.moduleSpecifier.text !== '@metarush/motion' || !st.importClause) continue;
      const nb = st.importClause.namedBindings;
      if (nb && ts.isNamespaceImport(nb)) nsAliases.add(nb.name.text);
      if (nb && ts.isNamedImports(nb)) {
        for (const el of nb.elements) {
          if ((el.propertyName ?? el.name).text === 'useResource') resAliases.add(el.name.text);
          if ((el.propertyName ?? el.name).text === 'useFont') fontAliases.add(el.name.text);
        }
      }
    }
    const isUseResource = (callee) =>
      (ts.isIdentifier(callee) && resAliases.has(callee.text)) ||
      (ts.isPropertyAccessExpression(callee) && callee.name.text === 'useResource' && ts.isIdentifier(callee.expression) && nsAliases.has(callee.expression.text));
    // useFont("name", "Family", …): a font from the project's resources, by a name in quotes (D171).
    const isUseFont = (callee) =>
      (ts.isIdentifier(callee) && fontAliases.has(callee.text)) ||
      (ts.isPropertyAccessExpression(callee) && callee.name.text === 'useFont' && ts.isIdentifier(callee.expression) && nsAliases.has(callee.expression.text));
    for (const d of sf.parseDiagnostics ?? []) {
      const { line, character } = sf.getLineAndCharacterOfPosition(d.start ?? 0);
      add('error', 'syntax', f.path, { line: line + 1, col: character + 1 }, ts.flattenDiagnosticMessageText(d.messageText, ' '), '');
    }
    for (const r of [...sf.referencedFiles, ...sf.typeReferenceDirectives, ...(sf.libReferenceDirectives ?? [])]) {
      const { line, character } = sf.getLineAndCharacterOfPosition(r.pos);
      add('error', 'reference_directive', f.path, { line: line + 1, col: character + 1 }, `/// <reference> directives aren't allowed (${r.fileName}).`, 'MetaRush provides the types for what a component may import.');
    }
    const visit = (node) => {
      if (ts.isImportDeclaration(node) && ts.isStringLiteral(node.moduleSpecifier)) {
        checkSpecifier(sf, f.path, node, node.moduleSpecifier.text, !!node.importClause?.isTypeOnly);
      } else if (ts.isExportDeclaration(node) && node.moduleSpecifier && ts.isStringLiteral(node.moduleSpecifier)) {
        checkSpecifier(sf, f.path, node, node.moduleSpecifier.text, node.isTypeOnly);
      } else if (ts.isImportEqualsDeclaration(node)) {
        add('error', 'require', f.path, lineOf(sf, node), 'import … = require(…) loads modules at run time.', 'Use a normal import at the top of the file.');
      } else if (ts.isImportTypeNode(node)) {
        const arg = node.argument;
        if (ts.isLiteralTypeNode(arg) && ts.isStringLiteral(arg.literal)) checkSpecifier(sf, f.path, node, arg.literal.text, true);
      } else if (ts.isCallExpression(node) && isUseFont(node.expression)) {
        const at = lineOf(sf, node);
        const a0 = node.arguments[0];
        const name = a0 && (ts.isStringLiteral(a0) || ts.isNoSubstitutionTemplateLiteral(a0)) ? a0.text : undefined;
        if (name === undefined || !RESOURCE_NAME.test(name) || name.includes('#')) {
          add('error', 'resource_name', f.path, at, 'useFont needs a font resource\u2019s short name in quotes, then the family the code names it.', 'Write useFont("inter", "Inter").');
        } else {
          resources.push({ name, file: f.path, line: at.line, font: true });
        }
      } else if (ts.isIdentifier(node) && fontAliases.has(node.text) && !ts.isImportSpecifier(node.parent) && !(ts.isCallExpression(node.parent) && node.parent.expression === node)) {
        add('error', 'resource_name', f.path, lineOf(sf, node), 'Call useFont directly (useFont("inter", "Inter")), so MetaRush can check which fonts the graphic draws with.', '');
      } else if (ts.isCallExpression(node) && isUseResource(node.expression)) {
        const at = lineOf(sf, node);
        const a = resourceArg(node.arguments[0]);
        if (!a) {
          add('error', 'resource_name', f.path, at, 'useResource needs a resource\u2019s short name in quotes, or a parameter marked as a resource.', 'Write useResource("logo"), or useResource(params.image) with image: z.string().meta({ format: "metarush-resource" }) in the schema.');
        } else if (a.name !== undefined && !RESOURCE_NAME.test(a.name)) {
          add('error', 'resource_name', f.path, at, `"${a.name.slice(0, 60)}" isn't a resource's short name (lowercase letters, digits, - and _; a page as name#3).`, 'See the names with metarush resources list --project <project>.');
        } else {
          resources.push({ ...a, file: f.path, line: at.line });
        }
      } else if (ts.isIdentifier(node) && resAliases.has(node.text) && !ts.isImportSpecifier(node.parent) && !(ts.isCallExpression(node.parent) && node.parent.expression === node)) {
        add('error', 'resource_name', f.path, lineOf(sf, node), 'Call useResource directly (useResource("logo")), so MetaRush can check which resources the graphic draws.', '');
      } else if (ts.isCallExpression(node)) {
        const callee = node.expression;
        if (callee.kind === ts.SyntaxKind.ImportKeyword) {
          add('error', 'dynamic_import', f.path, lineOf(sf, node), 'import(…) loads code while the graphic runs.', 'Import what you need at the top of the file, from the allowed packages or files in the component folder.');
        } else if (ts.isPropertyAccessExpression(callee) && callee.name.text === 'constructor') {
          add('error', 'code_from_text', f.path, lineOf(sf, node), 'Calling .constructor(…) can turn text into code.', 'A motion graphic can only run its own reviewed source.');
        } else if (ts.isIdentifier(callee) && (callee.text === 'setTimeout' || callee.text === 'setInterval') && node.arguments[0] && (ts.isStringLiteralLike(node.arguments[0]) || ts.isTemplateExpression(node.arguments[0]))) {
          add('error', 'code_from_text', f.path, lineOf(sf, node), `${callee.text} with text runs that text as code.`, 'Pass a function, or better, drive changes with useCurrentFrame().');
        }
      } else if (ts.isElementAccessExpression(node) && ts.isStringLiteralLike(node.argumentExpression)) {
        const key = node.argumentExpression.text;
        if (FORBIDDEN_GLOBALS[key] || key === 'constructor') {
          add('error', key === 'constructor' ? 'code_from_text' : 'forbidden_global', f.path, lineOf(sf, node), key === 'constructor' ? "['constructor'] can turn text into code." : `${key}: ${FORBIDDEN_GLOBALS[key]}`, '');
        }
      } else if (ts.isIdentifier(node) && Object.hasOwn(FORBIDDEN_GLOBALS, node.text) && isValueUse(node)) {
        add('error', 'forbidden_global', f.path, lineOf(sf, node), `${node.text}: ${FORBIDDEN_GLOBALS[node.text]}`, '');
      } else if (ts.isPropertyAccessExpression(node) && ts.isIdentifier(node.expression)) {
        const k = `${node.expression.text}.${node.name.text}`;
        if (NONDETERMINISTIC[k]) add('warning', 'nondeterministic', f.path, lineOf(sf, node), NONDETERMINISTIC[k], '');
      } else if (ts.isNewExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === 'Date' && (!node.arguments || node.arguments.length === 0)) {
        add('warning', 'nondeterministic', f.path, lineOf(sf, node), NONDETERMINISTIC['new Date'], '');
      }
      if (f.path === 'index.tsx' && node.parent === sf) {
        if (ts.isExportAssignment(node)) exports.default = true;
        const mods = ts.canHaveModifiers(node) ? ts.getModifiers(node) ?? [] : [];
        const isExport = mods.some((m) => m.kind === ts.SyntaxKind.ExportKeyword);
        if (isExport && mods.some((m) => m.kind === ts.SyntaxKind.DefaultKeyword)) exports.default = true;
        if (isExport && ts.isVariableStatement(node)) {
          for (const d of node.declarationList.declarations) {
            if (ts.isIdentifier(d.name) && (d.name.text === 'schema' || d.name.text === 'meta')) exports[d.name.text] = true;
          }
        }
        if (ts.isExportDeclaration(node) && !node.moduleSpecifier && node.exportClause && ts.isNamedExports(node.exportClause)) {
          for (const s of node.exportClause.elements) {
            const n = s.name.text;
            if (n === 'default') exports.default = true;
            if (n === 'schema' || n === 'meta') exports[n] = true;
          }
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(sf);
  }
  // TypeScript settings it declares (motion.json, library.json): only the allowed relaxations.
  for (const p of tsSettingsOf(files).problems) add('error', 'bad_settings', p.file, { line: 1, col: 1 }, p.message, '');
  const idx = files.find((f) => f.path === 'index.tsx');
  if (library) {
    // A library exports what graphics share; library.json says what it is.
    let doc = null;
    try {
      doc = JSON.parse(files.find((f) => f.path === 'library.json').text);
    } catch {
      doc = null;
    }
    if (!doc || typeof doc.summary !== 'string' || doc.summary.trim().length < 3 || doc.summary.length > 200) {
      add('error', 'bad_library', 'library.json', { line: 1, col: 1 }, 'library.json says what the library is in one sentence: { "summary": "…" }.', '');
    }
  } else if (idx) {
    const missing = [];
    if (!exports.default) missing.push('the component (export default)');
    if (!exports.schema) missing.push('schema (a zod object describing its parameters)');
    if (!exports.meta) missing.push('meta (summary, region, minDuration, maxDuration)');
    if (missing.length) add('error', 'missing_export', 'index.tsx', { line: 1, col: 1 }, `index.tsx doesn't export ${missing.join(', ')}.`, 'See metarush motion new for a starting point.');
  }
  problems.sort((a, b) => (a.file === b.file ? (a.line ?? 0) - (b.line ?? 0) : a.file < b.file ? -1 : 1));
  return { imports, problems, exports, resources, libraries: [...libraries].sort(), library };
};
