import { measureText } from '@remotion/layout-utils';

/**
 * Deterministic text layout. We measure with the same bundled font the DOM
 * renders with and emit explicit lines, so browser line-breaking can never
 * disagree with our fit math (and text never overflows the safe area).
 * Only call these after `useBundledFonts()` reports ready.
 */
export type TextStyle = {
  fontFamily: string;
  fontWeight: number;
  /** CSS length, typically in em (e.g. "-0.02em"). */
  letterSpacing?: string;
  uppercase?: boolean;
  tabularNums?: boolean;
};

export const measureWidth = (text: string, style: TextStyle, fontSize: number): number => {
  if (!text) return 0;
  return measureText({
    text,
    fontFamily: style.fontFamily,
    fontWeight: style.fontWeight,
    fontSize,
    letterSpacing: style.letterSpacing,
    textTransform: style.uppercase ? 'uppercase' : undefined,
    fontVariantNumeric: style.tabularNums ? 'tabular-nums' : undefined,
  }).width;
};

export const splitWords = (text: string): string[] =>
  text
    .replace(/\s+/g, ' ')
    .trim()
    .split(' ')
    .filter((w) => w.length > 0);

/**
 * Scripts written without spaces between words (Chinese characters, Japanese
 * kana, Thai, Lao, Khmer, Burmese; Go: textalign.Unspaced, D244): their
 * transcript words are pieces of a line, joined with nothing between them.
 */
const UNSPACED = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Thai}\p{Script=Lao}\p{Script=Khmer}\p{Script=Myanmar}ー々\u3000-\u303F\uFF00-\uFFEF]/u;

/** Whether two words read with a space between them (Go: textalign.SpaceBetween). */
export const spaceBetween = (left: string | undefined, right: string | undefined): boolean => {
  const l = left ? Array.from(left).pop() : undefined;
  const r = right ? Array.from(right)[0] : undefined;
  if (!l || !r) return true;
  return !UNSPACED.test(l) && !UNSPACED.test(r);
};

/** A line's words joined the way its language writes them (Go: textalign.JoinWords). */
export const joinWords = (words: (string | undefined)[]): string => {
  let out = '';
  let prev = '';
  for (const w of words) {
    if (!w) continue;
    if (prev && spaceBetween(prev, w)) out += ' ';
    out += w;
    prev = w;
  }
  return out;
};

/** One-character words ("I", "a", "&") read badly at the end of a line. */
const isShortWord = (w: string | undefined) => !!w && /^[\p{L}\p{N}&]$/u.test(w);

/**
 * Greedy word wrap. Returns lines as arrays of word indices, or null when a
 * single word is wider than `maxWidth` (caller should shrink the font).
 * A one-character word is carried to the next line instead of ending one.
 */
export const wrapWords = (
  words: string[],
  style: TextStyle,
  fontSize: number,
  maxWidth: number,
): number[][] | null => {
  const lines: number[][] = [];
  let current: number[] = [];
  for (let i = 0; i < words.length; i++) {
    const candidate = [...current, i];
    const text = joinWords(candidate.map((j) => words[j]));
    if (measureWidth(text, style, fontSize) <= maxWidth) {
      current = candidate;
      continue;
    }
    if (current.length === 0) return null;
    if (measureWidth(words[i] ?? '', style, fontSize) > maxWidth) return null;
    const last = current[current.length - 1]!;
    const carried = [last, i];
    if (
      current.length > 1 &&
      isShortWord(words[last]) &&
      measureWidth(joinWords(carried.map((j) => words[j])), style, fontSize) <= maxWidth
    ) {
      lines.push(current.slice(0, -1));
      current = carried;
    } else {
      lines.push(current);
      current = [i];
    }
  }
  if (current.length) lines.push(current);
  return lines;
};

export const lineWidth = (words: string[], line: number[], style: TextStyle, fontSize: number) =>
  measureWidth(joinWords(line.map((j) => words[j])), style, fontSize);

/**
 * Narrowest width that keeps the same line count: avoids a lonely last word
 * (typographic "balance"), like CSS text-wrap: balance but deterministic.
 */
export const balanceLines = (
  words: string[],
  style: TextStyle,
  fontSize: number,
  maxWidth: number,
  lines: number[][],
): number[][] => {
  if (lines.length < 2) return lines;
  let lo = maxWidth * 0.5;
  let hi = maxWidth;
  let best = lines;
  for (let iter = 0; iter < 10; iter++) {
    const mid = (lo + hi) / 2;
    const trial = wrapWords(words, style, fontSize, mid);
    if (trial && trial.length <= lines.length) {
      best = trial;
      hi = mid;
    } else {
      lo = mid;
    }
  }
  return best;
};

export type FitResult = { fontSize: number; lines: number[][]; widest: number };

/**
 * Largest font size (stepping down from maxSize) at which `words` wrap into at
 * most `maxLines` lines that fit `maxWidth`, and whose block height fits
 * `maxHeight`. Falls back to minSize (still wrapped, possibly more lines).
 */
export const fitWords = ({
  words,
  style,
  maxWidth,
  maxLines,
  maxHeight = Infinity,
  lineHeight,
  maxSize,
  minSize,
  balance = true,
}: {
  words: string[];
  style: TextStyle;
  maxWidth: number;
  maxLines: number;
  maxHeight?: number;
  lineHeight: number;
  maxSize: number;
  minSize: number;
  balance?: boolean;
}): FitResult => {
  const finish = (size: number, lines: number[][]): FitResult => {
    const out = balance ? balanceLines(words, style, size, maxWidth, lines) : lines;
    const widest = Math.max(0, ...out.map((l) => lineWidth(words, l, style, size)));
    return { fontSize: size, lines: out, widest };
  };
  const step = Math.max(1, Math.round(maxSize / 48));
  for (let size = Math.round(maxSize); size >= minSize; size -= step) {
    const lines = wrapWords(words, style, size, maxWidth);
    if (!lines) continue;
    if (lines.length <= maxLines && lines.length * size * lineHeight <= maxHeight) {
      return finish(size, lines);
    }
  }
  // Even the smallest size needs more lines: shrink a long single word to fit.
  let size = minSize;
  let lines = wrapWords(words, style, size, maxWidth);
  while (!lines && size > 8) {
    size = Math.floor(size * 0.9);
    lines = wrapWords(words, style, size, maxWidth);
  }
  return finish(size, lines ?? [words.map((_, i) => i)]);
};

/** Font size at which a single line of `text` fits `maxWidth`, capped at maxSize. */
export const fitSingleLine = (text: string, style: TextStyle, maxWidth: number, maxSize: number): number => {
  const w = measureWidth(text, style, maxSize);
  if (w <= maxWidth || w === 0) return maxSize;
  return Math.floor(maxSize * (maxWidth / w) * 0.995);
};

/** Normalizes a word for emphasis matching (mirrors Go's normWord). */
export const normWord = (w: string): string =>
  w.toLowerCase().replace(/^[\s.,!?;:"'“”‘’()\-]+|[\s.,!?;:"'“”‘’()\-]+$/g, '');
