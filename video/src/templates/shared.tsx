import React from 'react';
import type { FrameLayout } from '../core/layout';
import type { BrandFont } from '../theme/fonts';
import type { ResolvedBrand } from '../theme/brand';

/** Props every overlay template receives (frames are relative to the overlay). */
export type TemplateProps = {
  params: unknown;
  durationInFrames: number;
  /** Absolute timeline frame where the overlay starts. */
  from: number;
  planDurationInFrames: number;
  brand: ResolvedBrand;
  layout: FrameLayout;
  /**
   * Full-frame cards only: whether another full-frame card is on screen on the
   * frame before this one starts and on the frame after it ends, so slides
   * shown back to back keep one continuous background.
   */
  joins?: { before: boolean; after: boolean };
  /**
   * A graphic whose fields change while it stays on screen (D163): every
   * segment's whole fields from a frame (relative to the overlay) on. `params`
   * is then the current segment's, and `segmentFrom` the frame it started.
   */
  segments?: { from: number; params: unknown }[];
  segmentFrom?: number;
};

/** The segment of a changing graphic on screen at frame (relative to the overlay). */
export const segmentAt = (segments: { from: number; params: unknown }[] | undefined, frame: number): { index: number; from: number; params: unknown } | null => {
  if (!segments || segments.length === 0) return null;
  let k = 0;
  for (let i = 1; i < segments.length; i++) {
    if (segments[i]!.from <= frame) k = i;
    else break;
  }
  return { index: k, from: segments[k]!.from, params: segments[k]!.params };
};

const warned = new Set<string>();
/** console.warn once per message (templates re-render every frame). */
export const warnOnce = (message: string) => {
  if (warned.has(message)) return;
  warned.add(message);
  console.warn(`[metarush] ${message}`);
};

const record = (p: unknown): Record<string, unknown> =>
  p && typeof p === 'object' && !Array.isArray(p) ? (p as Record<string, unknown>) : {};

/** Trimmed non-empty string param, or undefined. Params arrive as raw JSON. */
export const strParam = (p: unknown, key: string, maxLength = 500): string | undefined => {
  const v = record(p)[key];
  if (typeof v !== 'string') return undefined;
  const t = v.replace(/\s+/g, ' ').trim();
  return t ? t.slice(0, maxLength) : undefined;
};

export const enumParam = <T extends string>(p: unknown, key: string, allowed: readonly T[]): T | undefined => {
  const v = record(p)[key];
  return typeof v === 'string' && (allowed as readonly string[]).includes(v) ? (v as T) : undefined;
};

export const strArrayParam = (p: unknown, key: string, maxItems = 8): string[] => {
  const v = record(p)[key];
  if (!Array.isArray(v)) return [];
  return v.filter((x): x is string => typeof x === 'string' && x.trim().length > 0).slice(0, maxItems);
};

/**
 * Per-font typographic tuning. Inter tightens as it grows (its opsz axis also
 * switches to the Display cut automatically); Fraunces needs less; the mono
 * stays near neutral so it keeps its rhythm.
 */
export const typeTune = (font: BrandFont) => {
  switch (font) {
    case 'Fraunces':
      return { display: 520, strong: 560, body: 420, label: 500, tightEm: -0.012, bodyEm: 0, lineDisplay: 1.06 };
    case 'JetBrains Mono':
      return { display: 600, strong: 620, body: 420, label: 500, tightEm: -0.02, bodyEm: 0, lineDisplay: 1.08 };
    default:
      return { display: 620, strong: 640, body: 440, label: 520, tightEm: -0.028, bodyEm: -0.003, lineDisplay: 1.02 };
  }
};

/** Tracking (em) interpolated from body (≤24px@1080) to display (≥96px@1080). */
export const trackingFor = (font: BrandFont, fontSize: number, unit: number): string => {
  const t = typeTune(font);
  const px = fontSize / unit;
  const k = Math.max(0, Math.min(1, (px - 24) / 72));
  const em = t.bodyEm + (t.tightEm - t.bodyEm) * k;
  return `${em.toFixed(4)}em`;
};

/**
 * Slot reveal: the child rises into a clipped line box. Padding keeps
 * descenders (g, j, p, q, y) inside the clip; a short opacity ramp hides any
 * sliver at the start.
 */
export const MaskLine: React.FC<{
  progress: number;
  /** Font size (px) of the line inside; sizes the descender allowance. */
  fontSize: number;
  children: React.ReactNode;
  style?: React.CSSProperties;
}> = ({ progress, fontSize, children, style }) => (
  <div
    style={{
      overflow: 'hidden',
      padding: `${fontSize * 0.08}px 0 ${fontSize * 0.34}px`,
      margin: `${-fontSize * 0.08}px 0 ${-fontSize * 0.34}px`,
      ...style,
    }}
  >
    <div
      style={{
        transform: `translateY(${(1 - progress) * 140}%)`,
        opacity: Math.min(1, progress * 2.5),
        willChange: 'transform',
      }}
    >
      {children}
    </div>
  </div>
);

/** Keeps a broken overlay from taking down the preview or the render. */
export class OverlayBoundary extends React.Component<
  { label: string; children: React.ReactNode },
  { failed: boolean }
> {
  override state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  override componentDidCatch(error: unknown) {
    warnOnce(`overlay ${this.props.label} failed to render and was skipped: ${String(error)}`);
  }
  override render() {
    return this.state.failed ? null : this.props.children;
  }
}
