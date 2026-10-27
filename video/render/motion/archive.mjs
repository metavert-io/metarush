// The canonical motion graphic archive (Go: internal/motion, format
// metarush.motion/1). Node never computes identities: it checks that the
// bytes it was given hash to the expected version and unpacks them.
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';

export const FORMAT = 'metarush.motion/1';
const SEGMENT = /^[A-Za-z0-9_][A-Za-z0-9_.-]*$/;
const EXT = new Set(['.tsx', '.ts', '.json']);

export const sha256 = (buf) => crypto.createHash('sha256').update(buf).digest('hex');

const safePath = (p) => {
  if (typeof p !== 'string' || p === '' || p.startsWith('/') || p.includes('\\')) return false;
  const parts = p.split('/');
  if (parts.length > 4 || p.length > 128) return false;
  return parts.every((s) => SEGMENT.test(s) && s !== '.' && s !== '..') && EXT.has(path.posix.extname(p).toLowerCase());
};

/** Reads an archive file, verifies its SHA-256 and its layout. */
export const readArchive = (file, expectedHash) => {
  const buf = fs.readFileSync(file);
  const hash = sha256(buf);
  if (expectedHash && hash !== expectedHash) {
    throw new Error(`the motion graphic archive ${path.basename(file)} is not version ${expectedHash.slice(0, 8)} (it hashes to ${hash.slice(0, 8)})`);
  }
  let doc;
  try {
    doc = JSON.parse(buf.toString('utf8'));
  } catch (e) {
    throw new Error(`the motion graphic archive is not valid JSON: ${e.message}`);
  }
  if (!doc || doc.format !== FORMAT || !Array.isArray(doc.files)) throw new Error(`the motion graphic archive is not in the ${FORMAT} format`);
  const seen = new Set();
  for (const f of doc.files) {
    if (!f || !safePath(f.path) || typeof f.text !== 'string' || seen.has(f.path)) throw new Error(`the motion graphic archive has an unsafe or repeated path: ${JSON.stringify(f?.path)}`);
    seen.add(f.path);
  }
  if (!seen.has('index.tsx')) throw new Error('the motion graphic archive has no index.tsx');
  return { hash, files: doc.files };
};

/** Writes the archive's files into dir (fresh), never outside it. */
export const extractArchive = (archive, dir) => {
  fs.mkdirSync(dir, { recursive: true });
  const root = fs.realpathSync(dir);
  for (const f of archive.files) {
    const dst = path.join(root, ...f.path.split('/'));
    if (!dst.startsWith(root + path.sep)) throw new Error(`unsafe path ${f.path}`);
    fs.mkdirSync(path.dirname(dst), { recursive: true });
    fs.writeFileSync(dst, f.text, { flag: 'wx' });
  }
  return root;
};
