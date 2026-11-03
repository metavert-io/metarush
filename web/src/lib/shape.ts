/**
 * The one shape rule (D376): the twin of Go's editspec.ShapeOf and
 * Output.Shape, held to the same cases (internal/editspec/testdata/shapes.json,
 * web/scripts/shape-test.mjs). The server sends a cut's and a project's shape
 * (RevisionSummary.shape, CutView.shape, ProjectShowOut.shape,
 * ProjectSummary.headShape): read those. This is for a spec the browser is
 * editing, and sizes it already holds.
 */
import type { OutputShape } from '../api/types';

export type Shape = OutputShape['shape'];

/**
 * The renderer's thresholds (video/src/core/layout.ts orientationOf, Go's
 * editspec.LandscapeAbove / VerticalBelow): width ÷ height above 1.15 is
 * landscape, below 0.87 vertical, else square.
 */
export const LANDSCAPE_ABOVE = 1.15;
export const VERTICAL_BELOW = 0.87;

const PRESETS: Record<string, [number, number]> = { 'vertical-1080p': [1080, 1920], 'landscape-1080p': [1920, 1080], 'square-1080': [1080, 1080] };

/** The shape of a width × height frame (undefined when either is missing). */
export function shapeOf(w?: number, h?: number): Shape | undefined {
  if (!w || !h || w <= 0 || h <= 0) return undefined;
  const r = w / h;
  if (r > LANDSCAPE_ABOVE) return 'landscape';
  if (r < VERTICAL_BELOW) return 'vertical';
  return 'square';
}

/** The shape as people say it: landscape is "widescreen". */
export function shapeWord(s: Shape | undefined): string {
  return s === 'landscape' ? 'widescreen' : (s ?? '');
}

function gcd(a: number, b: number): number {
  while (b) [a, b] = [b, a % b];
  return a > 0 ? a : 1;
}

/** A width × height frame described like Go's editspec.ShapeOfSize. */
export function sizeShape(w?: number, h?: number): OutputShape | undefined {
  const shape = shapeOf(w, h);
  if (!shape || !w || !h) return undefined;
  const g = gcd(w, h);
  const ratio = w / g > 64 || h / g > 64 ? `${w}×${h}` : `${w / g}:${h / g}`;
  return { shape, width: w, height: h, aspect: Math.round((w / h) * 10000) / 10000, ratio, word: shapeWord(shape) };
}

/** An output's shape: its width × height, else its preset's size (Go's Output.Shape). */
export function outputShape(o: { preset?: string; width?: number; height?: number } | undefined | null): OutputShape | undefined {
  if (!o) return undefined;
  let w = o.width ?? 0;
  let h = o.height ?? 0;
  if (w <= 0 || h <= 0) {
    const d = o.preset ? PRESETS[o.preset] : undefined;
    if (!d) return undefined;
    [w, h] = d;
  }
  return sizeShape(w, h);
}

/** The preset of a shape ("" for none). */
export function presetFor(s: Shape | '' | undefined): string {
  return s === 'vertical' ? 'vertical-1080p' : s === 'landscape' ? 'landscape-1080p' : s === 'square' ? 'square-1080' : '';
}

/**
 * A frame's CSS aspect-ratio: its own size when known, else the size the
 * picture turned out to be (natural, once loaded), else the fallback while it
 * loads. Frames are shown whole (object-contain), never cropped to a box.
 */
export function frameAspect(size?: { width?: number; height?: number } | null, natural?: { width: number; height: number } | null, fallback = '16 / 9'): string {
  if (size?.width && size?.height && size.width > 0 && size.height > 0) return `${size.width} / ${size.height}`;
  if (natural && natural.width > 0 && natural.height > 0) return `${natural.width} / ${natural.height}`;
  return fallback;
}

/** How wide a box of this shape may be, so tall ones don't fill the page (CSS max-width). */
export function shapeMaxWidth(s: { width?: number; height?: number } | undefined | null, wide: string, tall: string, square: string): string {
  const shape = shapeOf(s?.width, s?.height);
  return shape === 'vertical' ? tall : shape === 'square' ? square : wide;
}
