/*
 * Hidden characters: the ones that change what a person sees without showing
 * themselves — characters that reorder text (bidi overrides and isolates),
 * invisible ones (zero-width characters, fillers, the blank braille pattern, a
 * run of variation selectors that can carry a hidden message) and control
 * codes. Words from outside MetaRush (an AI app's, a reviewer's, a catalogue's)
 * lose them before a person reads them. The same rule as Go's
 * (plainwords.Clean, internal/plainwords/hidden.go), checked against the same
 * cases (internal/plainwords/testdata/hidden.json) by web/scripts/hidden-test.mjs.
 */

/** Letters and symbols that draw nothing. */
const FILLERS = new Set([0x034f, 0x115f, 0x1160, 0x17b4, 0x17b5, 0x180b, 0x180c, 0x180d, 0x180f, 0x2800, 0x3164, 0xffa0]);
const CONTROL_OR_FORMAT = /^[\p{Cc}\p{Cf}]$/u;
const SPACE = /^\s$/u;

const ZWNJ = 0x200c;
const ZWJ = 0x200d;
const SYMBOL = /^[\p{So}\p{Sk}]$/u;
const EMOJI_AFTER = /^\p{So}$/u;
const LETTER_OR_MARK = /^[\p{L}\p{Mn}]$/u;

/**
 * A joiner (or non-joiner) doing a joiner's work: between two emoji (a joiner
 * only), or two letters of a script that isn't ASCII (Go: joins).
 */
function joins(cp: number, prev: number | undefined, next: string | undefined): boolean {
  if (prev === undefined || next === undefined) return false;
  const p = String.fromCodePoint(prev);
  const n = next.codePointAt(0)!;
  if (cp === ZWJ && (SYMBOL.test(p) || prev === 0xfe0f) && EMOJI_AFTER.test(next)) return true;
  const letter = (c: number, ch: string) => c > 0x7f && LETTER_OR_MARK.test(ch) && !FILLERS.has(c);
  return letter(prev, p) && letter(n, next);
}

/** A variation selector: VS1–VS16 and the supplement. */
function selector(cp: number): boolean {
  return (cp >= 0xfe00 && cp <= 0xfe0f) || (cp >= 0xe0100 && cp <= 0xe01ef);
}

/** A character that always goes (variation selectors depend on what comes before them). */
function hiddenChar(ch: string, cp: number): boolean {
  if (ch === '\n' || ch === '\t') return false;
  return (
    CONTROL_OR_FORMAT.test(ch) ||
    cp === 0x2028 ||
    cp === 0x2029 ||
    FILLERS.has(cp) ||
    (cp >= 0xfdd0 && cp <= 0xfdef) ||
    (cp & 0xfffe) === 0xfffe ||
    (cp >= 0xd800 && cp <= 0xdfff) // half of a pair on its own: not text
  );
}

/**
 * Words without their hidden characters, and how many went. Newlines and tabs
 * stay; an emoji keeps its one text-or-picture selector (U+FE0E, U+FE0F); a
 * zero-width joiner stays between two emoji, and a joiner or non-joiner
 * between two letters of a script that isn't ASCII.
 */
export function cleanCount(s: string): [string, number] {
  let out = '';
  let n = 0;
  let prev: number | undefined; // the last character kept
  const chars = Array.from(s);
  for (let i = 0; i < chars.length; i++) {
    const ch = chars[i]!;
    const cp = ch.codePointAt(0)!;
    if (cp === ZWJ || cp === ZWNJ) {
      if (joins(cp, prev, chars[i + 1])) {
        out += ch;
        prev = cp;
      } else {
        n++;
      }
    } else if (hiddenChar(ch, cp)) {
      n++;
    } else if (selector(cp)) {
      if ((cp === 0xfe0e || cp === 0xfe0f) && prev !== undefined && !selector(prev) && !SPACE.test(String.fromCodePoint(prev))) {
        out += ch;
        prev = cp;
      } else {
        n++;
      }
    } else {
      out += ch;
      prev = cp;
    }
  }
  return n === 0 ? [s, 0] : [out, n];
}

/** Words as a person should see them: without hidden characters. */
export function shownText(s: string): string {
  return cleanCount(s)[0];
}

/** What a hover over words that lost hidden characters says. */
export function hiddenTitle(n: number): string {
  const what = n === 1 ? 'a hidden character' : `${n} hidden characters`;
  return `MetaRush took ${what} out of these words: characters that hide or reorder words. Whoever wrote them may want you to see something other than what's there.`;
}
