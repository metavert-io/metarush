/*
 * Cheap file identities for duplicate checks before uploading (mirrors
 * internal/media/fingerprint.go exactly):
 *   fp1: SHA-256 of (the first MiB || the last MiB)
 *   sp1: SHA-256 of 16 evenly spaced 64 KiB chunks (the whole file when small),
 *        which confirms an fp1 match so look-alike files are never confused.
 * Both read at most ~3 MiB of the file, whatever its size.
 */

const SPAN = 1 << 20;
const SAMPLE_COUNT = 16;
const SAMPLE_SIZE = 64 << 10;

function hex(buf: ArrayBuffer): string {
  return Array.from(new Uint8Array(buf), (b) => b.toString(16).padStart(2, '0')).join('');
}

async function sha256(parts: ArrayBuffer[]): Promise<string> {
  const total = parts.reduce((n, p) => n + p.byteLength, 0);
  const all = new Uint8Array(total);
  let off = 0;
  for (const p of parts) {
    all.set(new Uint8Array(p), off);
    off += p.byteLength;
  }
  return hex(await crypto.subtle.digest('SHA-256', all));
}

/** The chunk offsets for a file of `size` bytes (null = hash the whole file). */
export function sampleOffsets(size: number): number[] | null {
  if (size <= SAMPLE_COUNT * SAMPLE_SIZE) return null;
  // floor(i*(size-SAMPLE_SIZE)/(SAMPLE_COUNT-1)) is exact: the numerator stays far below 2^53.
  return Array.from({ length: SAMPLE_COUNT }, (_, i) => Math.floor((i * (size - SAMPLE_SIZE)) / (SAMPLE_COUNT - 1)));
}

export interface FileIdentity {
  fingerprint: string;
  sample: string;
}

/** fp1 + sp1 for a browser File. Needs a secure context (localhost counts). */
export async function identify(file: File): Promise<FileIdentity> {
  const size = file.size;
  const headLen = Math.min(size, SPAN);
  const tailLen = Math.min(size, SPAN);
  const [head, tail] = await Promise.all([file.slice(0, headLen).arrayBuffer(), file.slice(size - tailLen, size).arrayBuffer()]);
  const fingerprint = 'fp1:' + (await sha256([head, tail]));
  const offs = sampleOffsets(size);
  const chunks = offs ? await Promise.all(offs.map((o) => file.slice(o, o + SAMPLE_SIZE).arrayBuffer())) : [await file.arrayBuffer()];
  const sample = 'sp1:' + (await sha256(chunks));
  return { fingerprint, sample };
}
