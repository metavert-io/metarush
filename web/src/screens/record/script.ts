import type { PromptLine } from '../studio/voiceover/prompter';

/*
 * The teleprompter's lines for a screen recording (D261): a pasted script is
 * cut into lines as the person wrote it — the browser twin of Go's
 * editspec.SplitScript (paragraphs; without blank lines, each row unless the
 * rows are one wrapped paragraph; a single block, each sentence; # lines left
 * out) — and each line lasts as long as it takes to say at the narrator's
 * pace (editspec.EstimateSeconds: 155 words a minute, a quarter second per
 * sentence pause inside the line). A leading "(…)" is how to say it.
 *
 * Pure functions, tested by web/scripts/recorder-test.mjs.
 */

/** The narrator's pace (editspec.DefaultWPM). */
export const WPM = 155;

const ABBREV = new Set(['mr', 'mrs', 'ms', 'dr', 'st', 'jr', 'sr', 'prof', 'vs', 'etc', 'e.g', 'i.e', 'no', 'approx', 'fig', 'mt']);

function isUpper(c: string): boolean {
  return c.toUpperCase() === c && c.toLowerCase() !== c;
}

/** Splits text at sentence ends, keeping abbreviations and initials whole (editspec.Sentences). */
export function sentences(text: string): string[] {
  const r = Array.from(text.split(/\s+/).filter(Boolean).join(' '));
  const out: string[] = [];
  let start = 0;
  for (let i = 0; i < r.length; i++) {
    const c = r[i]!;
    if (c !== '.' && c !== '!' && c !== '?' && c !== '…') continue;
    let j = i + 1;
    while (j < r.length && '.!?"”’\')'.includes(r[j]!)) j++;
    if (j >= r.length || r[j] !== ' ' || j + 1 >= r.length) continue;
    const next = r[j + 1]!;
    if (!(isUpper(next) || /[0-9"“(\[‘]/.test(next))) continue;
    if (c === '.') {
      let k = i;
      while (k > start && r[k - 1] !== ' ') k--;
      const w = r
        .slice(k, i)
        .join('')
        .replace(/^["“(‘']+|["“(‘']+$/g, '')
        .toLowerCase();
      if (ABBREV.has(w) || (Array.from(w).length === 1 && isUpper(r[k]!))) continue;
    }
    out.push(r.slice(start, j).join('').trim());
    start = j + 1;
    i = j;
  }
  const rest = r.slice(start).join('').trim();
  if (rest) out.push(rest);
  return out;
}

/** Cuts a script into lines as written (editspec.SplitScript). */
export function splitScript(text: string): string[] {
  const lines = text.replace(/\r\n/g, '\n').split('\n').filter((l) => !l.trim().startsWith('#'));
  const paras: string[] = [];
  let cur: string[] = [];
  for (const l of lines) {
    const t = l.trim();
    if (!t) {
      if (cur.length) paras.push(cur.join(' '));
      cur = [];
    } else cur.push(t);
  }
  if (cur.length) paras.push(cur.join(' '));
  if (paras.length !== 1) return paras;
  const rows: string[] = [];
  let ends = 0;
  for (const l of lines) {
    const t = l.split(/\s+/).filter(Boolean).join(' ');
    if (!t) continue;
    rows.push(t);
    const last = Array.from(t.replace(/["”’')\]]+$/, '')).pop() ?? '';
    if ('.!?…:;'.includes(last) && last) ends++;
  }
  if (rows.length > 1 && ends * 2 >= rows.length) return rows;
  return sentences(paras[0]!);
}

/** How long a line takes to say at the pace, with a quarter second per pause inside it (editspec.EstimateSeconds). */
export function estimateSeconds(text: string, wpm = WPM): number {
  const words = text.split(/\s+/).filter((w) => /[\p{L}\p{N}]/u.test(w) && !/^(um+|uh+|erm*|hmm+)[.,!?]*$/i.test(w)).length;
  let pauses = (text.match(/[.?!;:]/g) ?? []).length;
  if (pauses > 0) pauses--;
  return Math.round((words * 60 / wpm + 0.25 * pauses) * 1000) / 1000;
}

/** The teleprompter's lines: none pinned, each as long as it takes to say. */
export function promptLines(lines: string[]): PromptLine[] {
  return lines
    .map((raw) => raw.trim())
    .filter(Boolean)
    .map((raw, i) => {
      const m = /^\(([^)]{1,80})\)\s*(.+)$/.exec(raw);
      const text = m ? m[2]! : raw;
      return { id: `line-${i + 1}`, n: i + 1, text, delivery: m ? m[1] : undefined, pinned: false, start: 0, dur: Math.max(0.8, estimateSeconds(text)) };
    });
}
