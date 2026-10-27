import type { CSSProperties } from 'react';

type FocusKey = { f: number; x: number; y: number };

type FramingInput = {
  mode: string;
  focusX: number;
  focusY: number;
  zoom: number;
  /** A moving focus (output.framing "follow", D115): keys in clip frames, straight lines between them. */
  path?: FocusKey[] | null;
};

const finite = (v: number, fallback: number) => (Number.isFinite(v) ? v : fallback);

/**
 * The focus at clip frame `frame` along a path Go resolved (editspec.FocusAt):
 * held before the first key and after the last, straight lines between keys;
 * two keys a frame apart are a jump (a cut in the footage).
 */
export const focusAt = (path: FocusKey[], frame: number): [number, number] => {
  const first = path[0];
  if (!first) return [0.5, 0.5];
  if (frame <= first.f) return [first.x, first.y];
  let a = first;
  for (const b of path.slice(1)) {
    if (frame <= b.f) {
      const u = (frame - a.f) / Math.max(1, b.f - a.f);
      return [a.x + u * (b.x - a.x), a.y + u * (b.y - a.y)];
    }
    a = b;
  }
  return [a.x, a.y];
};

/**
 * Places a source of (sw x sh) display pixels inside a (W x H) frame.
 * fill = cover, fit = contain; `zoom` scales further around the focal point.
 * The focal point (focusX/Y, 0..1 of the source) is centered when possible and
 * otherwise kept in frame: the crop is clamped so no frame edge is revealed on
 * an axis where the scaled source covers the frame. On an axis where it does
 * not (fit letterbox), the source is centered. With a path, the focal point at
 * clip frame `frame` comes from it.
 */
export const framingStyle = (
  framing: FramingInput,
  sw: number,
  sh: number,
  W: number,
  H: number,
  frame = 0,
): CSSProperties => {
  let fx0 = framing.focusX;
  let fy0 = framing.focusY;
  if (framing.path && framing.path.length > 0) {
    [fx0, fy0] = focusAt(framing.path, frame);
  }
  const fx = Math.max(0, Math.min(1, finite(fx0, 0.5)));
  const fy = Math.max(0, Math.min(1, finite(fy0, 0.5)));
  const zoom = Math.max(1, finite(framing.zoom, 1));
  const fit = framing.mode === 'fit';

  if (!(sw > 0 && sh > 0)) {
    // Unknown source dimensions: let the browser do cover/contain.
    return {
      position: 'absolute',
      inset: 0,
      width: '100%',
      height: '100%',
      objectFit: fit ? 'contain' : 'cover',
      objectPosition: `${fx * 100}% ${fy * 100}%`,
      transform: zoom > 1 ? `scale(${zoom})` : undefined,
      transformOrigin: `${fx * 100}% ${fy * 100}%`,
    };
  }

  const base = fit ? Math.min(W / sw, H / sh) : Math.max(W / sw, H / sh);
  const scale = base * zoom;
  const w = sw * scale;
  const h = sh * scale;
  const place = (frameSize: number, size: number, focus: number) => {
    if (size <= frameSize + 0.5) return (frameSize - size) / 2;
    const ideal = frameSize / 2 - focus * size;
    return Math.max(frameSize - size, Math.min(0, ideal));
  };
  return {
    position: 'absolute',
    left: place(W, w, fx),
    top: place(H, h, fy),
    width: w,
    height: h,
    objectFit: 'fill',
    maxWidth: 'none',
  };
};
