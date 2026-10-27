#!/usr/bin/env node
// Motion graphic checks that need no browser (CI: npm run test:motion -w video):
// the import scan on parsed source, the archive verification, the type check
// and the bundler's allowlist and size limit. The browser side (network jail,
// time limits, rendering an approved component) is in render/selftest.mjs.
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { extractArchive, readArchive } from './archive.mjs';
import { bundleComponent } from './bundle.mjs';
import { scanFiles } from './scan.mjs';
import { component, makeArchive } from './testkit.mjs';
import { typecheck } from './typecheck.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const FIXTURE = path.join(HERE, '..', 'fixtures', 'motion', 'route-map');
let failures = 0;
const check = (ok, what) => {
  process.stderr.write(`[motion-test] ${ok ? 'PASS' : 'FAIL'}  ${what}\n`);
  if (!ok) failures++;
};
const files = (map) => Object.entries(map).map(([p, text]) => ({ path: p, text }));
const errorsOf = (r) => r.problems.filter((p) => p.severity === 'error');

// --- the scan refuses what a component may not do, with plain, located problems ---
const refused = {
  'node fs': [{ imports: "import fs from 'fs';" }, 'import_not_allowed', /part of Node\.js/],
  'node: prefix': [{ imports: "import { readFileSync } from 'node:fs';" }, 'import_not_allowed', /part of Node\.js/],
  child_process: [{ imports: "import { exec } from 'child_process';" }, 'import_not_allowed', /part of Node\.js/],
  'another package': [{ imports: "import _ from 'lodash';" }, 'import_not_allowed', /can't add packages/],
  'a URL': [{ imports: "import x from 'https://example.com/x.js';" }, 'import_not_allowed', /no URLs/],
  'outside the folder': [{ imports: "import x from '../../src/MetaRushEdit';" }, 'import_outside', /outside the component's folder/],
  'dynamic import': [{ body: "void import('./other'); return null;" }, 'dynamic_import', /import\(…\)/],
  eval: [{ body: "eval('1+1'); return null;" }, 'forbidden_global', /^eval:/],
  'window.eval': [{ body: "window.eval('1'); return null;" }, 'forbidden_global', /^eval:/],
  "globalThis['eval']": [{ body: "globalThis['eval']('1'); return null;" }, 'forbidden_global', /^eval:/],
  'new Function': [{ body: "new Function('return 1')(); return null;" }, 'forbidden_global', /^Function:/],
  require: [{ body: "const x = require('fs'); return null;" }, 'forbidden_global', /^require:/],
  'import = require': [{ top: "import fs = require('fs');" }, 'require', /require/],
  'constructor trick': [{ body: "[].constructor.constructor('return 1')(); return null;" }, 'code_from_text', /constructor/],
  'string timer': [{ body: "setTimeout('alert(1)', 1); return null;" }, 'code_from_text', /runs that text as code/],
  fetch: [{ body: "fetch('https://example.com'); return null;" }, 'forbidden_global', /network/],
  WebSocket: [{ body: "new WebSocket('ws://x'); return null;" }, 'forbidden_global', /network/],
  WebRTC: [{ body: 'new RTCPeerConnection(); return null;' }, 'forbidden_global', /network connections/],
};
for (const [name, [opts, code, msg]] of Object.entries(refused)) {
  const r = scanFiles(files(component(opts)));
  const p = errorsOf(r).find((x) => x.code === code);
  check(!!p && msg.test(p.message) && p.file === 'index.tsx' && p.line > 0, `scan refuses ${name} (${p ? `${p.file}:${p.line} ${p.message}` : JSON.stringify(errorsOf(r))})`);
}

// Triple-slash directives (at the top of a file, where they count) are refused.
{
  const f = files(component());
  f[0].text = '/// <reference types="node" />\n' + f[0].text;
  const p = errorsOf(scanFiles(f)).find((x) => x.code === 'reference_directive');
  check(!!p && p.line === 1, `scan refuses a reference directive (${p ? p.message : 'none'})`);
}
// navigator.sendBeacon and window.fetch are globals reached through the global object.
for (const body of ["navigator.sendBeacon('https://x', 'd'); return null;", "window.fetch('https://x'); return null;"]) {
  const p = errorsOf(scanFiles(files(component({ body })))).find((x) => x.code === 'forbidden_global');
  check(!!p, `scan refuses ${body.split('(')[0]}`);
}
// Types and property keys named like a forbidden global are fine.
{
  const r = scanFiles(files(component({ top: 'type Handler = Function; const opts = { fetch: 1 };', body: 'return <div data-x={opts.fetch} />;' })));
  check(errorsOf(r).length === 0, `types and object keys named fetch/Function are not uses (${JSON.stringify(errorsOf(r))})`);
}
// Non-deterministic sources are warnings.
{
  const r = scanFiles(files(component({ body: 'const x = Math.random(); return <AbsoluteFill>{x}</AbsoluteFill>;' })));
  check(r.problems.some((p) => p.severity === 'warning' && p.code === 'nondeterministic'), 'Math.random() is flagged as non-deterministic');
}
// Missing exports are named.
{
  const r = scanFiles([{ path: 'index.tsx', text: "export default function X() { return null; }\n" }]);
  const p = errorsOf(r).find((x) => x.code === 'missing_export');
  check(!!p && /schema/.test(p.message) && /meta/.test(p.message), 'missing schema and meta exports are named');
}
// The sample component passes, and lists its imports by kind.
const fixtureFiles = (() => {
  const out = [];
  for (const n of fs.readdirSync(FIXTURE)) out.push({ path: n, text: fs.readFileSync(path.join(FIXTURE, n), 'utf8') });
  return out;
})();
{
  const r = scanFiles(fixtureFiles);
  const kinds = new Set(r.imports.map((i) => i.kind));
  check(r.problems.length === 0 && ['remotion', 'package', 'metarush', 'relative'].every((k) => kinds.has(k)), `the sample component passes the scan (${r.imports.map((i) => i.specifier).join(', ')})`);
}

// --- resources (D107): useResource names a picture in quotes or through a parameter ---
{
  const res = (body, imports = "import { useResource, ResourceImage } from '@metarush/motion';") => scanFiles(files(component({ imports, body })));
  let r = res("const logo = useResource('logo'); const page = useResource('deck#3'); return logo ? <ResourceImage pic={logo} /> : null;");
  check(errorsOf(r).length === 0 && r.resources.map((x) => x.name).join(',') === 'logo,deck#3' && r.resources[0].line === 10, `a name in quotes is listed with its line (${JSON.stringify(r.resources)})`);
  r = res('const pic = useResource(params.label); return null;');
  check(errorsOf(r).length === 0 && r.resources[0]?.param === 'label', `a parameter is listed as one (${JSON.stringify(r.resources)})`);
  r = res("const x = M.useResource('logo'); return null;", "import * as M from '@metarush/motion';");
  check(errorsOf(r).length === 0 && r.resources[0]?.name === 'logo', 'through a namespace import too');
  r = res("const x = pick('logo'); return null;", "import { useResource as pick } from '@metarush/motion';");
  check(errorsOf(r).length === 0 && r.resources[0]?.name === 'logo', 'under another name too');
  for (const [what, body, msg] of [
    ['a computed name', "const n = 'lo' + 'go'; const x = useResource(n); return null;", /short name in quotes/],
    ['a name built from text', 'const x = useResource(`logo-${frame}`); return null;', /short name in quotes/],
    ['a name that is not a resource name', "const x = useResource('Logo!'); return null;", /isn't a resource's short name/],
    ['passing useResource around', "const f = useResource; const x = f('logo'); return null;", /Call useResource directly/],
  ]) {
    const p = errorsOf(res(body)).find((x) => x.code === 'resource_name');
    check(!!p && msg.test(p.message), `the scan refuses ${what} (${p ? p.message : 'none'})`);
  }
  // No new imports: a picture file can't be imported, only named.
  const p = errorsOf(res('return null;', "import logo from './logo.png';")).find((x) => x.code === 'import_missing' || x.code === 'import_not_allowed');
  check(!!p, `importing a picture file is still refused (${p ? p.message : 'none'})`);
}

// --- archives are verified against their version ---
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'metarush-motion-test-'));
try {
  const a = makeArchive(FIXTURE, path.join(tmp, 'a'));
  check(readArchive(a.file, a.hash).files.length === fixtureFiles.length, 'an archive reads back at its version');
  let threw = '';
  try {
    readArchive(a.file, 'f'.repeat(64));
  } catch (e) {
    threw = e.message;
  }
  check(/is not version/.test(threw), `a different version is refused (${threw})`);
  const evil = path.join(tmp, 'evil.json');
  fs.writeFileSync(evil, JSON.stringify({ format: 'metarush.motion/1', files: [{ path: '../../escape.ts', text: '' }, { path: 'index.tsx', text: '' }] }));
  threw = '';
  try {
    readArchive(evil);
  } catch (e) {
    threw = e.message;
  }
  check(/unsafe/.test(threw), 'an archive path outside the folder is refused');

  // --- the type check uses MetaRush's settings and locates problems ---
  const bad = makeArchive(component({ body: "const n: number = 'not a number'; return null;" }), path.join(tmp, 'b'));
  const root = extractArchive(readArchive(bad.file, bad.hash), path.join(tmp, 'typed'));
  const types = typecheck(root);
  check(types.some((t) => t.file === 'index.tsx' && t.line === 10 && /not assignable/.test(t.message)), `a type error is reported by line (${JSON.stringify(types[0])})`);
  const good = extractArchive(readArchive(a.file, a.hash), path.join(tmp, 'good'));
  const goodTypes = typecheck(good);
  check(goodTypes.length === 0, `the sample component type-checks (${JSON.stringify(goodTypes.slice(0, 2))})`);

  // --- the bundler: shared React/Remotion, curated packages, size limit ---
  const b = await bundleComponent({ dir: good, hash: a.hash });
  check(b.bytes > 1000 && /__metarushMotion/.test(b.code) && !/require\("fs"\)/.test(b.code), `the sample bundles (${b.bytes} bytes)`);
  check(!b.inputs.some((p) => /node_modules\/(react|react-dom|remotion)\//.test(p)), 'React and Remotion come from the page, not the bundle');
  check(b.inputs.some((p) => /node_modules\/zod\//.test(p)), 'zod is bundled from MetaRush’s pinned copy');
  threw = '';
  try {
    await bundleComponent({ dir: good, hash: a.hash, maxBytes: 50_000 });
  } catch (e) {
    threw = `${e.code}: ${e.message}`;
  }
  check(/bundle_too_big/.test(threw) && /limit/.test(threw), `the bundle size limit is enforced (${threw})`);
  // Even if a forbidden import slipped past the scan, the bundler's allowlist refuses it.
  const sneaky = extractArchive({ files: files(component({ imports: "import os from 'os';", body: 'return <AbsoluteFill>{os.platform()}</AbsoluteFill>;' })) }, path.join(tmp, 'sneaky'));
  threw = '';
  try {
    await bundleComponent({ dir: sneaky, hash: 'a'.repeat(64) });
  } catch (e) {
    threw = e.message;
  }
  check(/part of Node\.js/.test(threw), `the bundler refuses Node built-ins too (${threw.slice(0, 120)})`);

  // --- shared libraries (D171): a theme graphics import as @metarush/lib/<name> ---
  const libFiles = files({
    'library.json': JSON.stringify({ summary: 'The test theme: colours and a label.', typescript: { noUncheckedIndexedAccess: false } }),
    'index.tsx': "export * from './theme';\nexport { Label } from './Label';\nexport { useThemeFonts } from './fonts';\n",
    'theme.ts': "export const COLORS = { ink: '#F2F2F7', accent: '#E0A458' } as const;\nexport const SIZES = [12, 24, 48];\nexport const big = (): number => SIZES[2] * 2;\n",
    'Label.tsx': "import React from 'react';\nimport { COLORS } from './theme';\nexport const Label: React.FC<{ text: string }> = ({ text }) => <div style={{ color: COLORS.accent }}>{text}</div>;\n",
    'fonts.tsx': "import { useFont } from '@metarush/motion';\nexport const useThemeFonts = (): void => {\n  useFont('anton', 'Anton', { weight: '400' });\n};\n",
  });
  const ls = scanFiles(libFiles);
  check(ls.library && errorsOf(ls).length === 0, `a folder with library.json is a library and passes the scan (${JSON.stringify(errorsOf(ls).slice(0, 2))})`);
  check(ls.resources.some((x) => x.name === 'anton' && x.font), `useFont names a font resource (${JSON.stringify(ls.resources)})`);
  const graphic = files({
    'index.tsx': "import React from 'react';\nimport { AbsoluteFill } from 'remotion';\nimport { z } from 'zod';\nimport type { MotionMeta, MotionProps } from '@metarush/motion';\nimport { Label, big, useThemeFonts } from '@metarush/lib/test-theme';\nexport const schema = z.object({ text: z.string() });\nexport const meta: MotionMeta = { summary: 'A label in the theme.', region: 'center', minDuration: 1, maxDuration: 4 };\nexport default function L({ params }: MotionProps<typeof schema>) {\n  useThemeFonts();\n  return <AbsoluteFill style={{ fontSize: big() }}><Label text={params.text} /></AbsoluteFill>;\n}\n",
  });
  const gs = scanFiles(graphic);
  check(errorsOf(gs).length === 0 && gs.libraries.join() === 'test-theme', `a graphic that imports the library passes the scan and names it (${JSON.stringify(gs.libraries)})`);
  const nested = scanFiles(files({ 'library.json': '{"summary":"x"}', 'index.tsx': "export * from '@metarush/lib/other';\n" }));
  check(errorsOf(nested).some((p) => p.code === 'library_in_library' || p.code === 'bad_library'), `a library can't import a library, and says what it is (${errorsOf(nested).map((p) => p.code).join(',')})`);
  const settings = scanFiles(files({ ...Object.fromEntries(fixtureFiles.map((f) => [f.path, f.text])), 'motion.json': JSON.stringify({ typescript: { strict: false } }) }));
  check(errorsOf(settings).some((p) => p.code === 'bad_settings'), 'a component may only relax the one allowed TypeScript setting');
  const libDir = extractArchive({ files: libFiles }, path.join(tmp, 'lib', 'test-theme'));
  const strict = typecheck(libDir);
  const relaxed = typecheck(libDir, {}, { noUncheckedIndexedAccess: false });
  check(strict.some((t) => /possibly 'undefined'/.test(t.message)) && relaxed.length === 0, `lifted code is checked the way its project checked it (${strict.length} strict, ${relaxed.length} relaxed)`);
  const gDir = extractArchive({ files: graphic }, path.join(tmp, 'g', 'label'));
  const gTypes = typecheck(gDir, { 'test-theme': libDir });
  check(gTypes.length === 0, `a graphic type-checks against its library (${JSON.stringify(gTypes.slice(0, 2))})`);
  const gb = await bundleComponent({ dir: gDir, hash: 'c'.repeat(64), libs: { 'test-theme': libDir } });
  check(gb.bytes > 500 && /E0A458/.test(gb.code), `a graphic bundles with its library (${gb.bytes} bytes)`);
  threw = '';
  try {
    await bundleComponent({ dir: gDir, hash: 'c'.repeat(64) });
  } catch (e) {
    threw = e.message;
  }
  check(/test-theme/.test(threw), `without the library it doesn't bundle (${threw.slice(0, 120)})`);

  // --- a library's check leaves no work folder behind (it ends with process.exit) ---
  const libArchive = makeArchive(Object.fromEntries(libFiles.map((f) => [f.path, f.text])), path.join(tmp, 'lib-archive'));
  const planBase = path.join(tmp, 'plan-base.json');
  fs.writeFileSync(planBase, JSON.stringify({ width: 1280, height: 720, fps: 30 }));
  const ownTmp = fs.mkdtempSync(path.join(tmp, 'tmpdir-'));
  let lines = [];
  try {
    const out = execFileSync(process.execPath, [path.join(HERE, '..', 'motion.mjs'), 'check', '--archive', libArchive.file, '--hash', libArchive.hash, '--out', path.join(tmp, 'lib-out'), '--plan-base', planBase, '--name', 'test-theme'], {
      encoding: 'utf8',
      env: { ...process.env, TMPDIR: ownTmp, TMP: ownTmp, TEMP: ownTmp },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    lines = out.split('\n').filter(Boolean).map((l) => JSON.parse(l));
  } catch (e) {
    lines = [{ type: 'done', ok: false, error: String(e.stderr ?? e.message).slice(0, 300) }];
  }
  const done = lines.find((l) => l.type === 'done');
  check(done?.ok === true && done.library === true, `a library's check passes (${JSON.stringify(done)})`);
  const left = fs.readdirSync(ownTmp).filter((n) => n.startsWith('metarush-motion-lib-'));
  check(left.length === 0, `the check leaves no work folder behind (${left.join(', ') || 'none'})`);
} finally {
  fs.rmSync(tmp, { recursive: true, force: true });
}

process.stderr.write(failures === 0 ? '[motion-test] ALL CHECKS PASSED\n' : `[motion-test] ${failures} CHECK(S) FAILED\n`);
process.exit(failures === 0 ? 0 : 1);
