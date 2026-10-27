import type { LogoCornerParams } from '@metarush/schema';
import React from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { makeOverlayClock } from '../core/motion';
import { rgba } from '../theme/brand';
import { ResourceImage, fitInside, usePicture } from './resources';
import { enumParam, strParam, warnOnce, type TemplateProps } from './shared';

const CORNERS = ['top-right', 'top-left', 'bottom-right', 'bottom-left'] as const;
const SIZES = ['small', 'medium', 'large'] as const;

/** Share of the frame's width the logo's box may take. */
export const LOGO_CORNER_SHARE: Record<(typeof SIZES)[number], number> = { small: 0.1, medium: 0.16, large: 0.24 };

export const parseLogoCorner = (raw: unknown): LogoCornerParams | null => {
  const logo = strParam(raw, 'logo', 52);
  if (!logo) return null;
  const rec = raw && typeof raw === 'object' ? (raw as Record<string, unknown>) : {};
  const op = typeof rec.opacity === 'number' && Number.isFinite(rec.opacity) ? Math.min(1, Math.max(0.2, rec.opacity)) : undefined;
  return {
    logo,
    corner: enumParam(raw, 'corner', CORNERS),
    size: enumParam(raw, 'size', SIZES),
    opacity: op,
  };
};

/**
 * Where logo.corner draws (px), shared with QA's layout check in spirit: a box
 * of `size` (a share of the frame's width; its height at most 60% of that)
 * inside the platform-safe content box, in the corner asked for.
 */
export const logoCornerBox = (p: LogoCornerParams, layout: TemplateProps['layout'], pic: { width?: number; height?: number }) => {
  const { width, content } = layout;
  const share = LOGO_CORNER_SHARE[p.size ?? 'small'];
  const maxW = Math.round(width * share);
  const maxH = Math.round(maxW * 0.6);
  const box = fitInside({ name: '', url: '', kind: 'image', ...pic }, maxW, maxH);
  const corner = p.corner ?? 'top-right';
  const left = corner.endsWith('left') ? content.left : content.left + content.width - box.width;
  const top = corner.startsWith('top') ? content.top : content.top + content.height - box.height;
  return { left, top, width: box.width, height: box.height };
};

/**
 * logo.corner v1: a logo kept in one corner of the frame (top-right unless
 * asked otherwise) for as long as the overlay lasts. It fades in and out
 * gently and carries a soft shadow so a light logo stays visible over light
 * footage. Draws nothing when the plan has no picture for it.
 */
export const LogoCorner: React.FC<TemplateProps> = ({ params, durationInFrames, brand, layout }) => {
  const frame = useCurrentFrame();
  const p = parseLogoCorner(params);
  const logo = usePicture(p?.logo, 'logo.corner');
  if (!p) {
    warnOnce('logo.corner: missing required param "logo"; overlay skipped');
    return null;
  }
  if (!logo) return null;
  const c = makeOverlayClock(frame, durationInFrames, layout.fps, 0.5);
  const opacity = (p.opacity ?? 0.9) * c.enter(0, 0.45) * c.exit;
  const box = logoCornerBox(p, layout, logo);
  const u = layout.unit;
  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <div
        style={{
          position: 'absolute',
          left: box.left,
          top: box.top,
          width: box.width,
          height: box.height,
          opacity,
          filter: `drop-shadow(0 ${Math.max(1, Math.round(2 * u))}px ${Math.round(8 * u)}px ${rgba(brand.background, 0.35)})`,
        }}
      >
        <ResourceImage pic={logo} style={{ width: box.width, height: box.height }} />
      </div>
    </AbsoluteFill>
  );
};
