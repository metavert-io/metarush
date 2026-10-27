// Go embeds web/dist via `//go:embed all:dist`, so the directory must never be
// empty in git. Vite empties dist on every build; put the placeholder back.
import { writeFileSync, existsSync, mkdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const dist = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'dist');
if (!existsSync(dist)) mkdirSync(dist, { recursive: true });
writeFileSync(path.join(dist, '.gitkeep'), '');
console.log('postbuild: restored web/dist/.gitkeep');
