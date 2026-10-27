/*
 * The screen recorder's clock (D259). Everything is timed on the page's clock
 * (performance.now(), milliseconds): time 0 is when the recorders start. The
 * screen and camera files start then; the voice is the microphone's samples,
 * which start when its first sample was heard and run on the microphone's own
 * clock. Both are measured, as live takes are (D85: capture.ts takeStart):
 * each block of samples arrives a little after its last sample was heard, so
 * the earliest arrivals say when sample 0 was heard, and a line through them
 * over the whole recording says how fast the microphone's clock runs.
 *
 * Pausing stops every file: nothing is recorded or sent while paused. The
 * voice keeps arriving, so the samples heard during a pause are dropped by
 * their index (VoiceGate), and all files leave out the same stretch.
 *
 * Pure functions (no browser APIs), tested by web/scripts/recorder-test.mjs.
 */

/** An arrival: [page time (ms), samples received so far, including that block]. */
export type Arrival = [number, number];

/** The 10th percentile (undefined for none). */
export function low(xs: number[], q = 0.1): number | undefined {
  if (!xs.length) return undefined;
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.floor((s.length - 1) * q)];
}

export interface MicFit {
  /** When sample 0 was heard (page time, ms). */
  first: number;
  /** How much faster the microphone's clock runs than the page's (parts per million). */
  ppm: number;
  /** How far apart the start estimates were (ms): how sure `first` is. */
  spread: number;
}

/** Drift is measured only over this much sound (ms); shorter, it stays 0. */
export const DRIFT_MIN_MS = 60_000;

/**
 * Fits the microphone's arrivals at a nominal sample rate: when its first
 * sample was heard, and its drift. Each arrival's estimate of sample 0 is its
 * time minus the sound received so far; a busy page only delays arrivals, so
 * the lowest estimates are the truth. Drift: the start estimates of the first
 * and last fifth of the recording drift apart by the clock difference.
 */
export function fitMic(arrivals: Arrival[], rate: number): MicFit | undefined {
  if (!arrivals.length || rate <= 0) return undefined;
  const audioMs = (arrivals[arrivals.length - 1]![1] / rate) * 1000;
  let ppm = 0;
  if (audioMs >= DRIFT_MIN_MS) {
    const k = Math.max(8, Math.floor(arrivals.length / 5));
    const a = arrivals.slice(0, k);
    const b = arrivals.slice(-k);
    const at = (xs: Arrival[]) => low(xs.map(([t, n]) => t - (n / rate) * 1000))!;
    const mid = (xs: Arrival[]) => (xs.reduce((s, [, n]) => s + n, 0) / xs.length / rate) * 1000;
    const slope = (at(b) - at(a)) / (mid(b) - mid(a));
    // est = first − audio·p: the estimates fall as a fast microphone gets ahead.
    ppm = Math.round(-slope * 1e6 * 10) / 10;
    if (!Number.isFinite(ppm) || Math.abs(ppm) > 1000) ppm = 0; // not a clock: a stall or a gap
  }
  // The start: the early arrivals, corrected to sample 0 for the drift.
  const head = arrivals.slice(0, Math.max(8, Math.floor(arrivals.length / 5)));
  const headEst = head.map(([t, n]) => t - (n / rate) * 1000 + (n / rate) * 1000 * (ppm / 1e6));
  const first = low(headEst)!;
  const median = low(headEst, 0.5)!;
  return { first: round3(first), ppm, spread: round3(Math.max(0, median - first)) };
}

/** A stretch of the page's clock the person paused (ms; `to` undefined while paused). */
export interface Pause {
  from: number;
  to?: number;
}

/**
 * Which of the microphone's samples are kept: those heard outside the
 * pauses. Sample i was heard at first + i / rate (at the microphone's actual
 * rate); a pause drops the samples heard while it lasted.
 */
export class VoiceGate {
  private pauses: Pause[] = [];
  private first: number;
  private rate: number;
  constructor(first: number, rate: number) {
    this.first = first;
    this.rate = rate;
  }

  /** The current fit (the start and actual rate get surer as samples arrive). */
  refit(first: number, rate: number) {
    this.first = first;
    this.rate = rate;
  }

  pause(at: number) {
    this.pauses.push({ from: at });
  }

  resume(at: number) {
    const p = this.pauses[this.pauses.length - 1];
    if (p && p.to === undefined) p.to = at;
  }

  get paused(): boolean {
    const p = this.pauses[this.pauses.length - 1];
    return !!p && p.to === undefined;
  }

  /** The sample index heard at page time t. */
  index(t: number): number {
    return Math.max(0, Math.round(((t - this.first) / 1000) * this.rate));
  }

  /**
   * The kept parts of a block of samples starting at index `start`: [from,
   * to) offsets within the block.
   */
  keep(start: number, length: number): [number, number][] {
    let parts: [number, number][] = [[start, start + length]];
    for (const p of this.pauses) {
      const a = this.index(p.from);
      const b = p.to === undefined ? Infinity : this.index(p.to);
      parts = parts.flatMap(([x, y]) => {
        const out: [number, number][] = [];
        if (x < Math.min(y, a)) out.push([x, Math.min(y, a)]);
        if (Math.max(x, b) < y) out.push([Math.max(x, b), y]);
        return out;
      });
    }
    return parts.map(([x, y]) => [x - start, y - start]);
  }
}

/**
 * The pauses on the recording's clock (seconds, with pauses left out: where
 * in the files each happened) and how long each lasted.
 */
export function pausesOnClock(t0: number, pauses: Pause[]): { at: number; length: number }[] {
  const out: { at: number; length: number }[] = [];
  let before = 0;
  for (const p of pauses) {
    if (p.to === undefined) continue;
    const len = Math.max(0, p.to - p.from);
    out.push({ at: round3((p.from - t0 - before) / 1000), length: round3(len / 1000) });
    before += len;
  }
  return out;
}

/** How long has been recorded at page time t (seconds), pauses left out. */
export function recordedAt(t0: number, pauses: Pause[], t: number): number {
  let paused = 0;
  for (const p of pauses) {
    const to = p.to ?? t;
    if (p.from < t) paused += Math.max(0, Math.min(to, t) - p.from);
  }
  return Math.max(0, (t - t0 - paused) / 1000);
}

/** The voice's place on the recording's clock (seconds) from its fit. */
export function voiceStart(t0: number, fit: MicFit | undefined): number {
  if (!fit) return 0;
  return round3((fit.first - t0) / 1000);
}

/** 16-bit little-endian samples from float samples (−1…1). */
export function toPCM16(samples: Float32Array, parts: [number, number][] = [[0, samples.length]]): Uint8Array {
  const n = parts.reduce((s, [a, b]) => s + (b - a), 0);
  const out = new Uint8Array(n * 2);
  const v = new DataView(out.buffer);
  let o = 0;
  for (const [a, b] of parts) {
    for (let i = a; i < b; i++) {
      const x = Math.max(-1, Math.min(1, samples[i]!));
      v.setInt16(o, x < 0 ? x * 0x8000 : x * 0x7fff, true);
      o += 2;
    }
  }
  return out;
}

function round3(v: number): number {
  return Math.round(v * 1000) / 1000;
}
