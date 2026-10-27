import { useSyncExternalStore } from 'react';
import type { AssetSummary } from '../api/types';

/*
 * In and out marks on a piece of footage (the source viewer's I and O keys or
 * handles): the part that goes into the cut when it is dragged onto the
 * timeline or added with a button. No marks = the whole footage. Kept for the
 * session, per footage.
 */

export interface Marks {
  in?: number;
  out?: number;
}

const marks = new Map<string, Marks>();
const subs = new Set<() => void>();
let version = 0;

function changed() {
  version++;
  subs.forEach((fn) => fn());
}

export function useMarksVersion(): number {
  return useSyncExternalStore(
    (fn) => {
      subs.add(fn);
      return () => subs.delete(fn);
    },
    () => version,
  );
}

export function useMarks(assetId: string | undefined): Marks {
  useMarksVersion();
  return (assetId && marks.get(assetId)) || {};
}

export function setMark(assetId: string, edge: 'in' | 'out', t: number | undefined) {
  const cur = { ...(marks.get(assetId) ?? {}) };
  if (t === undefined) delete cur[edge];
  else cur[edge] = Math.round(t * 1000) / 1000;
  // An out before the in (or the other way round) moves the other mark away.
  if (cur.in !== undefined && cur.out !== undefined && cur.out <= cur.in) {
    if (edge === 'in') delete cur.out;
    else delete cur.in;
  }
  marks.set(assetId, cur);
  changed();
}

export function clearMarks(assetId: string) {
  marks.delete(assetId);
  changed();
}

/*
 * I and O mark a stretch on the timeline and in/out points in the source
 * viewer; when both are on the page, the keys go to the one used last.
 */
let surface: 'timeline' | 'source' | undefined;
let timelines = 0;
export function setKeySurface(s: 'timeline' | 'source') {
  surface = s;
}
export function timelineMounted(delta: 1 | -1) {
  timelines += delta;
}
export const sourceKeysActive = () => timelines === 0 || surface === 'source';
export const timelineKeysActive = () => surface !== 'source';

/** The part of the footage that goes into the cut. */
export function markedRange(asset: AssetSummary): { in: number; out: number; marked: boolean } {
  const m = marks.get(asset.id) ?? {};
  const start = m.in ?? 0;
  const end = m.out ?? asset.duration ?? 0;
  return { in: start, out: end > start ? end : (asset.duration ?? 0), marked: m.in !== undefined || m.out !== undefined };
}
