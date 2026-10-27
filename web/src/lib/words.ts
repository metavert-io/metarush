/*
 * Words in a cut, for editing by transcript: which words each clip plays, the
 * stretch of footage that removes some of them so the cuts land in the pauses
 * around them, and the removeWords ops for a set of removals. The same rules
 * as internal/cutsheet/words.go (FindWords, RemovalFor, RemovalOps): a word is
 * heard when its middle is inside the clip, and a removal keeps up to
 * PAD_AFTER of air after the last word kept before the cut and PAD_BEFORE
 * before the first word kept after it. web/scripts/words-parity.mjs checks it
 * against the golden file Go writes (TestWordRemovalsGolden).
 */
import type { Clip, EditOp, Spec } from '../api/types';
import { applyOps, OpError, partID, place, speedOf } from './editops';

/**
 * Scripts written without spaces between words (Chinese characters, Japanese
 * kana, Thai, Lao, Khmer, Burmese; Go: textalign.Unspaced, D244): their
 * transcript words are pieces of a line, joined with nothing between them.
 */
const UNSPACED = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Thai}\p{Script=Lao}\p{Script=Khmer}\p{Script=Myanmar}ー々\u3000-\u303F\uFF00-\uFFEF]/u;

/** Whether two words read with a space between them (Go: textalign.SpaceBetween). */
export function spaceBetween(left: string | undefined, right: string | undefined): boolean {
  const l = left ? Array.from(left).pop() : undefined;
  const r = right ? Array.from(right)[0] : undefined;
  if (!l || !r) return true;
  return !UNSPACED.test(l) && !UNSPACED.test(r);
}

/** Words joined the way their language writes them (Go: textalign.JoinWords). */
export function joinWords(words: (string | undefined)[]): string {
  let out = '';
  let prev = '';
  for (const w of words) {
    if (!w) continue;
    if (prev && spaceBetween(prev, w)) out += ' ';
    out += w;
    prev = w;
  }
  return out;
}

const PAD_BEFORE = 0.1;
const PAD_AFTER = 0.15;

/** A transcript segment as the dashboard gets it (footage.transcript with words). */
export interface SegmentLike {
  from: number;
  to: number;
  text: string;
  speaker?: string;
  words?: { w: string; f: number; t: number }[];
}

export interface WordRef {
  text: string;
  norm: string;
  from: number;
  to: number;
  /** Timing spread evenly over a segment that has no word times. */
  approx: boolean;
  /** The segment it is in (index into the transcript's segments). */
  seg: number;
}

/** Lowercase letters and digits only: "I'm" → "im" (cutsheet.normWord). */
export function normWord(s: string): string {
  return s.toLowerCase().replace(/[^\p{L}\p{Nd}]/gu, '');
}

/** Every timed word in source order; segments without word times get evenly spread ones. */
export function flattenWords(segs: SegmentLike[]): WordRef[] {
  const out: WordRef[] = [];
  segs.forEach((s, si) => {
    if (s.words?.length) {
      for (const w of s.words) {
        const n = normWord(w.w);
        if (n) out.push({ text: w.w.trim(), norm: n, from: w.f, to: w.t, approx: false, seg: si });
      }
      return;
    }
    const parts = s.text.split(/\s+/).filter(Boolean);
    if (!parts.length || s.to <= s.from) return;
    const step = (s.to - s.from) / parts.length;
    parts.forEach((t, i) => {
      const n = normWord(t);
      if (n) out.push({ text: t, norm: n, from: s.from + i * step, to: s.from + (i + 1) * step, approx: true, seg: si });
    });
  });
  return out.sort((a, b) => a.from - b.from);
}

/** The words a clip plays (by their middle) and its footage's words around them. */
export interface ClipWords {
  all: WordRef[];
  /** Indices into all. */
  heard: number[];
}

export function wordsOfClip(c: Clip, segs: SegmentLike[] | undefined): ClipWords {
  const all = c.muted || !segs ? [] : flattenWords(segs);
  const heard: number[] = [];
  all.forEach((w, i) => {
    const mid = (w.from + w.to) / 2;
    if (mid >= c.in && mid < c.out) heard.push(i);
  });
  return { all, heard };
}

/** The stretch that removes heard words h0..h1, cut in the pauses around them. */
export function removalRange(c: Clip, cw: ClipWords, h0: number, h1: number): { from: number; to: number } {
  const first = cw.all[cw.heard[h0]!]!;
  const last = cw.all[cw.heard[h1]!]!;
  let from = c.in;
  let to = c.out;
  if (h0 > 0) {
    const prev = cw.all[cw.heard[h0 - 1]!]!;
    const gap = first.from - prev.to;
    from = gap >= 2 * PAD_AFTER ? prev.to + PAD_AFTER : gap > 0 ? prev.to + gap / 2 : prev.to;
  }
  if (h1 + 1 < cw.heard.length) {
    const next = cw.all[cw.heard[h1 + 1]!]!;
    const gap = next.from - last.to;
    to = gap >= 2 * PAD_BEFORE ? next.from - PAD_BEFORE : gap > 0 ? last.to + gap / 2 : next.from;
  }
  // To the millisecond, without float noise pushing a value over.
  from = Math.max(c.in, Math.floor(from * 1000 + 1e-6) / 1000);
  to = Math.min(c.out, Math.ceil(to * 1000 - 1e-6) / 1000);
  return { from, to };
}

/** A stretch of one clip's footage to remove (cutsheet.WordRemoval). */
export interface Removal {
  clipId: string;
  /** The clip's number in the cut (1 = the first). */
  clip: number;
  words: string;
  wordsFrom: number;
  wordsTo: number;
  from: number;
  to: number;
  /** Where the words are heard in the cut (timeline seconds). */
  at: number;
  /** How much shorter the cut gets. */
  seconds: number;
}

type Transcripts = (assetId: string) => SegmentLike[] | undefined;

/** Removal of heard words h0..h1 of clip i. */
export function removalOf(spec: Spec, tx: Transcripts, i: number, h0: number, h1: number, cw = wordsOfClip(spec.clips[i]!, tx(spec.clips[i]!.assetId))): Removal {
  const c = spec.clips[i]!;
  const { from, to } = removalRange(c, cw, h0, h1);
  const first = cw.all[cw.heard[h0]!]!;
  const last = cw.all[cw.heard[h1]!]!;
  const { placed, fps } = place(spec);
  const pc = placed[i]!;
  const at = (pc.from + ((first.from - c.in) / speedOf(c)) * fps) / fps;
  const words: string[] = [];
  for (let h = h0; h <= h1; h++) words.push(cw.all[cw.heard[h]!]!.text);
  return {
    clipId: c.id,
    clip: i + 1,
    words: joinWords(words),
    wordsFrom: first.from,
    wordsTo: last.to,
    from,
    to,
    at: Math.round(Math.max(0, at) * 1000) / 1000,
    seconds: Math.round(((to - from) / speedOf(c)) * 1000) / 1000,
  };
}

/** The removal of a clip's words heard between from and to (source seconds; a word counts by its middle). */
export function removalFor(spec: Spec, tx: Transcripts, clipId: string, from: number, to: number): Removal | undefined {
  const i = spec.clips.findIndex((c) => c.id === clipId);
  if (i < 0) return undefined;
  const cw = wordsOfClip(spec.clips[i]!, tx(spec.clips[i]!.assetId));
  let h0 = -1;
  let h1 = -1;
  cw.heard.forEach((h, k) => {
    const w = cw.all[h]!;
    const mid = (w.from + w.to) / 2;
    if (mid >= from && mid < to) {
      if (h0 < 0) h0 = k;
      h1 = k;
    }
  });
  return h0 < 0 ? undefined : removalOf(spec, tx, i, h0, h1, cw);
}

/**
 * Removals as removeWords ops, in the cut's order (cutsheet.RemovalOps). A clip
 * a removal cuts in two continues in its second part, so later removals in the
 * same footage name that part; the parts' names are explicit so the ops replay
 * exactly. Throws OpError when a clip can't be changed (locked).
 */
export function removalOps(spec: Spec, removals: Pick<Removal, 'clipId' | 'clip' | 'from' | 'to' | 'words'>[]): EditOp[] {
  let work = spec;
  const sorted = [...removals].sort((a, b) => (a.clip !== b.clip ? a.clip - b.clip : a.from - b.from));
  const family = new Map<string, string[]>();
  const ops: EditOp[] = [];
  for (const r of sorted) {
    const parts = family.get(r.clipId) ?? [r.clipId];
    let target = '';
    for (const id of parts) for (const c of work.clips) if (c.id === id && r.from < c.out && r.to > c.in) target = id;
    if (!target) continue; // already removed with an earlier stretch
    const op: EditOp = { op: 'removeWords', id: target, from: r.from, to: r.to, newId: partID(work, r.clipId), text: r.words };
    const next = applyOps(work, [op]);
    if (!next.clips.some((c) => c.id === op.newId)) delete op.newId;
    else parts.push(op.newId!);
    family.set(r.clipId, parts);
    ops.push(op);
    work = next;
  }
  return ops;
}

/* ---------- the cut's words, for the transcript view ---------- */

/** A word the cut plays, where it is heard. */
export interface CutWord {
  /** Index in the cut's words (the selection's unit). */
  n: number;
  clip: number;
  /** Index into the clip's heard words. */
  h: number;
  text: string;
  /** Source seconds. */
  from: number;
  to: number;
  /** Timeline seconds. */
  t0: number;
  t1: number;
  approx: boolean;
  seg: number;
}

export interface CutClipWords {
  clip: Clip;
  index: number;
  /** Where the clip starts on the timeline (seconds). */
  start: number;
  words: CutWord[];
  cw: ClipWords;
}

/** The words every clip of the cut plays, in the cut's order. */
export function cutWords(spec: Spec, tx: Transcripts): { clips: CutClipWords[]; words: CutWord[] } {
  const { placed, fps } = place(spec);
  const clips: CutClipWords[] = [];
  const words: CutWord[] = [];
  spec.clips.forEach((c, i) => {
    const cw = wordsOfClip(c, tx(c.assetId));
    const start = placed[i]!.from / fps;
    const end = start + placed[i]!.dur / fps;
    const sp = speedOf(c);
    const mine: CutWord[] = cw.heard.map((idx, h) => {
      const w = cw.all[idx]!;
      const cut: CutWord = {
        n: words.length + h,
        clip: i,
        h,
        text: w.text,
        from: w.from,
        to: w.to,
        t0: Math.max(start, start + (w.from - c.in) / sp),
        t1: Math.min(end, start + (w.to - c.in) / sp),
        approx: w.approx,
        seg: w.seg,
      };
      return cut;
    });
    words.push(...mine);
    clips.push({ clip: c, index: i, start, words: mine, cw });
  });
  return { clips, words };
}

/** The removals for the cut's words a..b (inclusive; across clips: one per clip). */
export function removalsFor(spec: Spec, tx: Transcripts, model: { clips: CutClipWords[]; words: CutWord[] }, a: number, b: number): Removal[] {
  const lo = Math.min(a, b);
  const hi = Math.max(a, b);
  const out: Removal[] = [];
  for (const cc of model.clips) {
    const inside = cc.words.filter((w) => w.n >= lo && w.n <= hi);
    if (!inside.length) continue;
    out.push(removalOf(spec, tx, cc.index, inside[0]!.h, inside[inside.length - 1]!.h, cc.cw));
  }
  return out;
}

/**
 * The in point for footage starting on word i: in the pause just before it,
 * keeping up to PAD_BEFORE of air (cutsheet.snapIn, as quoted words in a cut
 * sheet's "from" do).
 */
export function snapIn(ws: { from: number; to: number }[], i: number): number {
  const w = ws[i]!;
  let v = w.from - PAD_BEFORE;
  if (i > 0) {
    const gap = w.from - ws[i - 1]!.to;
    if (gap >= 2 * PAD_BEFORE) {
      // keep the padding
    } else if (gap > 0) v = ws[i - 1]!.to + gap / 2;
    else v = w.from;
  }
  return Math.max(0, Math.floor(v * 1000) / 1000);
}

/** The out point for footage ending on word j: just after it, keeping up to PAD_AFTER (cutsheet.snapOut). */
export function snapOut(ws: { from: number; to: number }[], j: number, duration: number): number {
  const w = ws[j]!;
  let v = w.to + PAD_AFTER;
  if (j + 1 < ws.length) {
    const gap = ws[j + 1]!.from - w.to;
    if (gap >= 2 * PAD_AFTER) {
      // keep the padding
    } else if (gap > 0) v = w.to + gap / 2;
    else v = w.to;
  }
  v = Math.ceil(v * 1000) / 1000;
  return duration > 0 && v > duration ? duration : v;
}

/** How much shorter: "1.2 s", or "a moment". */
export function shorter(seconds: number): string {
  if (seconds < 0.25) return 'a moment';
  return `${Math.round(seconds * 10) / 10} s`;
}

/** Where a removal's cut lands on the timeline (seconds): where the removed stretch began. */
export function cutPoint(spec: Spec, r: Pick<Removal, 'clip' | 'from'>): number {
  const { placed, fps } = place(spec);
  const c = spec.clips[r.clip - 1];
  const p = placed[r.clip - 1];
  if (!c || !p) return 0;
  return p.from / fps + (r.from - c.in) / speedOf(c);
}

/** "and honestly" or "and honestly … quit" for a long stretch. */
export function quoteWords(words: string, max = 6): string {
  const parts = words.split(/\s+/).filter(Boolean);
  if (parts.length <= max) return parts.join(' ');
  return `${parts.slice(0, 3).join(' ')} … ${parts.slice(-2).join(' ')}`;
}

export { OpError };
