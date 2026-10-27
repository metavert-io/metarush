// Regenerates src/theme/fontFaces.ts from the installed @fontsource-variable packages.
// Usage: node scripts/gen-fontfaces.mjs
import fs from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const here = path.dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);
const pkgDir = (name) => path.dirname(require.resolve(`@fontsource-variable/${name}/package.json`));
const sets = [
  { pkg: 'inter', css: 'opsz.css', family: 'MR Inter', key: 'Inter' },
  { pkg: 'fraunces', css: 'opsz.css', family: 'MR Fraunces', key: 'Fraunces' },
  { pkg: 'jetbrains-mono', css: 'wght.css', family: 'MR JetBrains Mono', key: 'JetBrains Mono' },
];
let imports = [];
let entries = [];
let i = 0;
for (const s of sets) {
  const css = fs.readFileSync(path.join(pkgDir(s.pkg), s.css), 'utf8');
  const re = /@font-face\s*{([^}]*)}/g;
  let m;
  while ((m = re.exec(css))) {
    const body = m[1];
    const src = /url\(\.\/files\/([^)]+)\)/.exec(body)[1];
    const weight = /font-weight:\s*([^;]+);/.exec(body)[1].trim();
    const range = /unicode-range:\s*([^;]+);/.exec(body)[1].trim();
    const v = `f${i++}`;
    imports.push(`import ${v} from '@fontsource-variable/${s.pkg}/files/${src}';`);
    entries.push(`  { family: '${s.family}', brand: '${s.key}', url: ${v}, weight: '${weight}', unicodeRange: '${range}' },`);
  }
}
const out = `// GENERATED from @fontsource-variable/{inter,fraunces,jetbrains-mono} CSS (OFL-1.1).
// Regenerate if the font packages are upgraded. Every face is bundled locally:
// no network fetches at render time.
${imports.join('\n')}

export type BundledFace = {
  family: string;
  brand: 'Inter' | 'Fraunces' | 'JetBrains Mono';
  url: string;
  weight: string;
  unicodeRange: string;
};

export const BUNDLED_FACES: BundledFace[] = [
${entries.join('\n')}
];
`;
fs.writeFileSync(path.join(here, '..', 'src', 'theme', 'fontFaces.ts'), out);
console.log(entries.length, 'faces');
