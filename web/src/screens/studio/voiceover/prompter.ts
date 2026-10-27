/*
 * The live teleprompter's timing (D85): it follows the video. A line with a
 * start point of its own (tied to a shot, a moment or a clock time) is due when
 * the cut reaches it; a line without one ("as-is" scripts, or a line that
 * follows the one before) is due once the line before has been said, at the
 * narrator's pace. Space or a clicker brings the next line in early; the lines
 * that follow it at their pace move with it, pinned lines never do.
 *
 * Pure functions (no React), tested by web/scripts/prompter-test.mjs.
 */

export interface PromptLine {
  id: string;
  /** The line's number in the script (1-based). */
  n: number;
  text: string;
  delivery?: string;
  /** It has a start point of its own (not "after the previous line"). */
  pinned: boolean;
  /** Where the cut places it now (timeline seconds). */
  start: number;
  /** How long it takes to say (its recording, or an estimate at the narrator's pace). */
  dur: number;
}

/** A line appears this long before its moment, with a countdown. */
export const CUE_LEAD = 2;

/** The first line of an as-is script is due this long after recording starts. */
export const FIRST_LEAD = 1;

/**
 * When each line is due, for a recording that starts at `from` (timeline
 * seconds): pinned lines at their start, the others after the line before (its
 * length plus `gap`). Lines before the recording's start that follow a line
 * are moved to the start of the recording.
 */
export function schedule(lines: PromptLine[], from: number, gap = 0.3): number[] {
  const due: number[] = [];
  let prevEnd: number | undefined;
  for (const l of lines) {
    let t: number;
    if (l.pinned) t = l.start;
    else if (prevEnd === undefined) t = Math.max(l.start, from + FIRST_LEAD);
    else t = prevEnd + gap;
    if (!l.pinned && t < from + FIRST_LEAD && (prevEnd === undefined || prevEnd < from)) t = from + FIRST_LEAD;
    due.push(round3(t));
    prevEnd = t + l.dur;
  }
  return due;
}

/**
 * Brings line k in now (time t): it and the lines after it that follow at their
 * pace (up to the next pinned line) move by the same amount. Pinned lines keep
 * their moment.
 */
export function advance(due: number[], lines: PromptLine[], k: number, t: number): number[] {
  if (k < 0 || k >= due.length) return due;
  const out = [...due];
  const shift = t - due[k]!;
  if (lines[k]!.pinned) {
    out[k] = round3(t);
    return out;
  }
  for (let j = k; j < out.length; j++) {
    if (j > k && lines[j]!.pinned) break;
    out[j] = round3(out[j]! + shift);
  }
  return out;
}

export interface Prompt {
  /** The line being said now (-1 before the first is due). */
  current: number;
  /** The line after it (-1 at the end). */
  next: number;
  /** Seconds until the next line is due, when it is CUE_LEAD or less away. */
  countdown: number | null;
}

/** What the teleprompter shows at time t: the line due last, the next one and its countdown. */
export function promptAt(due: number[], t: number): Prompt {
  let current = -1;
  for (let i = 0; i < due.length; i++) if (due[i]! <= t + 1e-6) current = i;
  const next = current + 1 < due.length ? current + 1 : -1;
  const left = next >= 0 ? due[next]! - t : Infinity;
  return { current, next, countdown: left <= CUE_LEAD + 1e-6 ? Math.max(0, round3(left)) : null };
}

/** The line a manual advance brings in at time t (the next one not yet due), or -1. */
export function nextToBring(due: number[], t: number): number {
  const p = promptAt(due, t);
  return p.next;
}

function round3(v: number): number {
  return Math.round(v * 1000) / 1000;
}
