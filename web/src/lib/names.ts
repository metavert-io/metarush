/*
 * Matching what a person types against the people they have named. This is
 * the dashboard's copy of internal/names (Go): the same folding (case and
 * accents ignored, punctuation dropped), the same ranking (exact, prefix, word
 * prefix, substring, then one or two typos) and the same tie-breaks. Both are
 * tested against internal/names/testdata/cases.json (web/scripts/names-test.mjs).
 * No imports, so Node can run it directly with --experimental-strip-types.
 */

export type MatchKind = 'exact' | 'prefix' | 'word' | 'substring' | 'typo';

const KIND_ORDER: Record<MatchKind, number> = { exact: 0, prefix: 1, word: 2, substring: 3, typo: 4 };

interface Folded {
  text: string;
  /** For each character of text: its start and end in the original string. */
  spans: [number, number][];
}

/** A name as compared: no accents, lower case, punctuation dropped, single spaces. */
export function fold(s: string): string {
  return foldWithMap(s).text;
}

function foldWithMap(s: string): Folded {
  let text = '';
  const spans: [number, number][] = [];
  let space = false;
  let i = 0;
  for (const ch of s) {
    const start = i;
    i += ch.length;
    const base = ch.normalize('NFD').replace(/\p{Mn}/gu, '');
    if (base === '') continue;
    if (/^[-_\s]$/u.test(base)) {
      space = true;
      continue;
    }
    for (const c of base) {
      if (!/[\p{L}\p{Nd}]/u.test(c)) continue;
      if (space && text.length > 0) {
        text += ' ';
        spans.push([start, start]);
      }
      space = false;
      for (const lower of c.toLowerCase()) {
        text += lower;
        spans.push([start, i]);
      }
    }
  }
  return { text, spans };
}

/** How many edits a query may be off by: none under 3 letters, one up to 6, two from 7. */
export function tolerance(q: string): number {
  const n = [...q].length;
  if (n < 3) return 0;
  if (n < 7) return 1;
  return 2;
}

/** Optimal-string-alignment edit distance (insert, delete, substitute, swap neighbours). */
export function distance(a: string, b: string): number {
  const x = [...a];
  const y = [...b];
  const n = x.length;
  const m = y.length;
  const d: number[][] = Array.from({ length: n + 1 }, (_, i) => {
    const row = new Array<number>(m + 1).fill(0);
    row[0] = i;
    return row;
  });
  for (let j = 0; j <= m; j++) d[0]![j] = j;
  for (let i = 1; i <= n; i++) {
    for (let j = 1; j <= m; j++) {
      const cost = x[i - 1] === y[j - 1] ? 0 : 1;
      let v = Math.min(d[i - 1]![j]! + 1, d[i]![j - 1]! + 1, d[i - 1]![j - 1]! + cost);
      if (i > 1 && j > 1 && x[i - 1] === y[j - 2] && x[i - 2] === y[j - 1]) v = Math.min(v, d[i - 2]![j - 2]! + 1);
      d[i]![j] = v;
    }
  }
  return d[n]![m]!;
}

function words(s: string): string[] {
  return s.split(' ').filter(Boolean);
}

function wordPrefix(q: string, name: string): boolean {
  const qs = words(q);
  let i = 0;
  for (const w of words(name)) if (i < qs.length && w.startsWith(qs[i]!)) i++;
  return qs.length > 0 && i === qs.length;
}

/** How a folded query matches a folded name (null when it does not), with the distance for typos. */
export function classify(q: string, name: string): { kind: MatchKind; distance: number } | null {
  if (!q) return null;
  if (q === name) return { kind: 'exact', distance: 0 };
  if (name.startsWith(q)) return { kind: 'prefix', distance: 0 };
  if (wordPrefix(q, name)) return { kind: 'word', distance: 0 };
  if ([...q].length >= 2 && name.includes(q)) return { kind: 'substring', distance: 0 };
  const tol = tolerance(q);
  if (tol > 0) {
    let best = distance(q, name);
    for (const w of words(name)) best = Math.min(best, distance(q, w));
    if (best <= tol) return { kind: 'typo', distance: best };
  }
  return null;
}

export interface Candidate {
  id: string;
  name: string;
  /** Breaks ties between equally good matches (how often they appear); higher first. */
  weight?: number;
}

export interface Hit<T extends Candidate = Candidate> {
  candidate: T;
  kind: MatchKind;
  distance: number;
  /** Ranges of the name to highlight ([start, end) in the original string). */
  marks: [number, number][];
}

function compareFolded(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/** The candidates that match q, best first (kind, distance, weight, name). */
export function rank<T extends Candidate>(q: string, cands: T[]): Hit<T>[] {
  const fq = fold(q);
  const out: Hit<T>[] = [];
  for (const c of cands) {
    const f = foldWithMap(c.name);
    const m = classify(fq, f.text);
    if (!m) continue;
    out.push({ candidate: c, kind: m.kind, distance: m.distance, marks: marksFor(fq, f, m.kind) });
  }
  out.sort((a, b) => {
    if (a.kind !== b.kind) return KIND_ORDER[a.kind] - KIND_ORDER[b.kind];
    if (a.distance !== b.distance) return a.distance - b.distance;
    const wa = a.candidate.weight ?? 0;
    const wb = b.candidate.weight ?? 0;
    if (wa !== wb) return wb - wa;
    return compareFolded(fold(a.candidate.name), fold(b.candidate.name));
  });
  return out;
}

function marksFor(q: string, f: Folded, kind: MatchKind): [number, number][] {
  const range = (from: number, to: number): [number, number] | null => {
    const a = f.spans[from];
    const b = f.spans[to - 1];
    return a && b ? [a[0], b[1]] : null;
  };
  const out: [number, number][] = [];
  const push = (r: [number, number] | null) => {
    if (r && r[1] > r[0]) out.push(r);
  };
  if (kind === 'exact' || kind === 'prefix') push(range(0, q.length));
  else if (kind === 'substring') {
    const at = f.text.indexOf(q);
    if (at >= 0) push(range(at, at + q.length));
  } else if (kind === 'word') {
    const qs = words(q);
    let i = 0;
    let pos = 0;
    for (const w of f.text.split(' ')) {
      if (i < qs.length && w.startsWith(qs[i]!)) {
        push(range(pos, pos + qs[i]!.length));
        i++;
      }
      pos += w.length + 1;
    }
  }
  return out;
}

export interface Resolution<T extends Candidate = Candidate> {
  found?: T;
  ambiguous?: T[];
  suggest: T[];
}

/** The one person a typed name means: an exact match, or the only (word) prefix match; else close names. */
export function resolve<T extends Candidate>(q: string, cands: T[]): Resolution<T> {
  const hits = rank(q, cands);
  const exact = hits.filter((h) => h.kind === 'exact').map((h) => h.candidate);
  if (exact.length === 1) return { found: exact[0], suggest: [] };
  if (exact.length > 1) return { ambiguous: exact, suggest: [] };
  const pre = hits.filter((h) => h.kind === 'prefix' || h.kind === 'word').map((h) => h.candidate);
  if (pre.length === 1) return { found: pre[0], suggest: [] };
  if (pre.length > 1) return { ambiguous: pre, suggest: [] };
  return { suggest: hits.slice(0, 3).map((h) => h.candidate) };
}

/** People whose names are close to a new name (a slip rather than someone new): prefix, word prefix or typo. */
export function close<T extends Candidate>(q: string, cands: T[]): T[] {
  const out: T[] = [];
  for (const h of rank(q, cands)) {
    if (h.kind === 'exact') return [];
    if (h.kind === 'prefix' || h.kind === 'word' || h.kind === 'typo') out.push(h.candidate);
    if (out.length === 3) break;
  }
  return out;
}
