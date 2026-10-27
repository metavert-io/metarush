import type { SafeArea } from '@metarush/schema';

export type Orientation = 'vertical' | 'landscape' | 'square';

export type Box = { left: number; top: number; width: number; height: number };

/**
 * Frame geometry shared by captions and templates. `unit` scales every size
 * authored at a 1080px short edge, so 1080x1920, 1920x1080 and 1080x1080 share
 * one type scale and 4K plans scale up proportionally.
 */
export type FrameLayout = {
  width: number;
  height: number;
  fps: number;
  orientation: Orientation;
  unit: number;
  /** Plan safe area as an absolute box (px). */
  safe: Box;
  /** Breathing room kept between text and the safe-area edge. */
  gutter: number;
  /** `safe` shrunk by `gutter` on every side: where text may live. */
  content: Box;
};

export const orientationOf = (width: number, height: number): Orientation => {
  const r = width / height;
  if (r > 1.15) return 'landscape';
  if (r < 0.87) return 'vertical';
  return 'square';
};

export const pick = <T,>(o: Orientation, v: { vertical: T; landscape: T; square: T }): T => v[o];

const clampMargin = (v: unknown, total: number) => {
  const n = typeof v === 'number' && Number.isFinite(v) ? v : 0;
  // Never let a malformed safe area eat more than 30% of a dimension per side.
  return Math.max(0, Math.min(n, total * 0.3));
};

export const makeLayout = (
  width: number,
  height: number,
  fps: number,
  safeArea: SafeArea | undefined,
): FrameLayout => {
  const unit = Math.min(width, height) / 1080;
  const top = clampMargin(safeArea?.top, height);
  const bottom = clampMargin(safeArea?.bottom, height);
  const left = clampMargin(safeArea?.left, width);
  const right = clampMargin(safeArea?.right, width);
  const safe: Box = { left, top, width: width - left - right, height: height - top - bottom };
  const gutter = Math.round(16 * unit);
  const content: Box = {
    left: safe.left + gutter,
    top: safe.top + gutter,
    width: safe.width - gutter * 2,
    height: safe.height - gutter * 2,
  };
  return { width, height, fps, orientation: orientationOf(width, height), unit, safe, gutter, content };
};
