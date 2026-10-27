// Pictures a person put in place of a project's own (D209), served to the
// jailed render with the bundle's own files (the only server the page may
// reach): each by its SHA-256 under /__metarush_pictures/, copied from
// MetaRush's verified cache. The page never gets a path from the project or
// the computer: the input prop metarushPictures names each file by its hash.
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';

export const PICTURES_DIR = '__metarush_pictures';
const SERVED_NAME = /^([0-9a-f]{64})\.(png|jpg|webp|gif|svg)$/;

const sha256Of = (file) => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');

/**
 * Puts the pictures ({"<sha256>.<ext>": "<file in MetaRush's cache>"}) in the
 * bundle's folder, each checked against its name's hash before it is served.
 * A picture already there (same name, same size) is kept. Returns how many
 * were copied.
 */
export const placePictures = (bundleDir, pictures) => {
  const entries = Object.entries(pictures ?? {});
  if (!entries.length) return 0;
  const dir = path.join(bundleDir, PICTURES_DIR);
  fs.mkdirSync(dir, { recursive: true });
  let copied = 0;
  for (const [name, src] of entries) {
    const m = SERVED_NAME.exec(name);
    if (!m) throw new Error(`a replaced picture has an unusual name (${name})`);
    let st;
    try {
      st = fs.statSync(src);
    } catch {
      st = null;
    }
    if (!st?.isFile() || st.size === 0) throw new Error(`a replaced picture isn't on this computer (${name})`);
    const dst = path.join(dir, name);
    try {
      if (fs.statSync(dst).size === st.size) continue;
    } catch {
      // not there yet
    }
    if (sha256Of(src) !== m[1]) throw new Error(`a replaced picture isn't the file it should be (${name})`);
    const tmp = `${dst}.${process.pid}.${crypto.randomBytes(4).toString('hex')}.tmp`;
    fs.copyFileSync(src, tmp);
    fs.renameSync(tmp, dst);
    copied++;
  }
  return copied;
};
