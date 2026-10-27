import type { NameBarParams } from '@metarush/schema';
import React from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import type { FrameLayout } from '../core/layout';
import { ramp } from '../core/motion';
import { fitSingleLine, measureWidth, type TextStyle } from '../core/text';
import { rgba, type ResolvedBrand } from '../theme/brand';
import { ResourceImage, fitInside, usePicture } from './resources';
import { enumParam, strParam, typeTune, type TemplateProps } from './shared';

/** The area call apps print names in when nothing says otherwise (Go: editspec.DefaultNameCover). */
const DEFAULT_COVER = { x: 0, y: 0.9, w: 0.3, h: 0.1 };

const num = (v: unknown, lo: number, hi: number): number | undefined =>
  typeof v === 'number' && Number.isFinite(v) ? Math.max(lo, Math.min(hi, v)) : undefined;

export const parseNameBar = (raw: unknown): NameBarParams & { cover: { x: number; y: number; w: number; h: number } } => {
  const r = raw && typeof raw === 'object' && !Array.isArray(raw) ? (raw as Record<string, unknown>) : {};
  const c = r.cover && typeof r.cover === 'object' ? (r.cover as Record<string, unknown>) : null;
  const cover = c
    ? { x: num(c.x, 0, 1) ?? 0, y: num(c.y, 0, 1) ?? 0.9, w: num(c.w, 0, 1) ?? 0.3, h: num(c.h, 0, 1) ?? 0.1 }
    : DEFAULT_COVER;
  return {
    name: strParam(raw, 'name', 60),
    title: strParam(raw, 'title', 80),
    cover,
    style: enumParam(raw, 'style', ['band', 'tab'] as const),
    hide: r.hide === true,
    logo: strParam(raw, 'logo', 52),
    height: enumParam(raw, 'height', ['full', 'half'] as const),
    animate: enumParam(raw, 'animate', ['fade', 'slide', 'never'] as const),
  };
};

/**
 * Where name.bar v1 draws, shared by the component and by captions (which lift
 * above it). The bar covers the area completely (the call app's names never
 * show through), at the bottom of the picture when the area is in its lower
 * half, else at the top; a band runs across the picture, a tab only as wide as
 * the area and the name. Height half (D199) makes it about half as tall, never
 * shorter than the area it covers: anchored to the edge when the names reach
 * it, else centred on them. Go mirrors this in internal/qa/layout.go nameBarBox.
 */
export const nameBarMetrics = (p: ReturnType<typeof parseNameBar>, brand: ResolvedBrand, layout: FrameLayout) => {
  const { width: W, height: H, unit: u, content } = layout;
  const c = p.cover;
  const atBottom = c.y + c.h / 2 >= 0.5;
  const onRight = c.x + c.w / 2 > 0.5;
  const padX = Math.round(0.006 * W);
  const padY = Math.round(0.006 * H);
  const minBand = Math.round(64 * u);
  const coverTop = Math.max(0, Math.floor(c.y * H) - padY);
  const coverBottom = Math.min(H, Math.ceil((c.y + c.h) * H) + padY);
  let top: number;
  let height: number;
  if (atBottom) {
    top = Math.min(coverTop, H - minBand);
    height = H - top;
  } else {
    top = 0;
    height = Math.max(coverBottom, minBand);
  }
  if (p.height === 'half') {
    const need = coverBottom - coverTop;
    const half = Math.max(need, Math.round(height / 2));
    const mid = (coverTop + coverBottom) / 2;
    if (atBottom && coverBottom >= H - padY) top = H - half;
    else if (!atBottom && coverTop <= padY) top = 0;
    else top = Math.round(Math.max(0, Math.min(H - half, mid - half / 2)));
    // Always over the printed names, whatever the rounding.
    top = Math.min(top, coverTop);
    height = Math.max(half, coverBottom - top);
  }
  const tune = typeTune(brand.font);
  const nameStyle: TextStyle = { fontFamily: brand.fontFamily, fontWeight: tune.display - 40 };
  const titleStyle: TextStyle = { fontFamily: brand.fontFamily, fontWeight: tune.body };
  const textArea = atBottom ? Math.min(height, H - content.top) : height;
  const nameBase = Math.max(Math.round(24 * u), Math.min(Math.round(46 * u), Math.round(textArea * (p.title ? 0.34 : 0.44))));
  const textLeft = onRight ? content.left : Math.max(content.left, Math.round(28 * u));
  const maxText = Math.max(Math.round(200 * u), content.width * 0.6);
  const nameSize = p.name ? fitSingleLine(p.name, nameStyle, maxText, nameBase) : nameBase;
  const titleSize = Math.round(nameSize * 0.62);
  const logoH = p.logo ? Math.round(nameSize * 1.3) : 0;
  const logoGap = p.logo ? Math.round(16 * u) : 0;
  const textW = Math.max(
    p.name ? measureWidth(p.name, nameStyle, nameSize) : 0,
    p.title ? measureWidth(p.title, titleStyle, titleSize) : 0,
  );
  // The tab covers the area and holds the name.
  const innerPad = Math.round(28 * u);
  const coverLeft = Math.max(0, Math.floor(c.x * W) - padX);
  const coverRight = Math.min(W, Math.ceil((c.x + c.w) * W) + padX);
  let left = 0;
  let width = W;
  if (p.style === 'tab') {
    if (onRight) {
      const need = Math.max(W - coverLeft, textW + logoH * 2 + logoGap + innerPad * 2);
      left = Math.max(0, W - need);
      width = W - left;
    } else {
      width = Math.min(W, Math.max(coverRight, textW + logoH * 2 + logoGap + innerPad * 2));
    }
  }
  return { atBottom, onRight, top, height, left, width, nameStyle, titleStyle, nameSize, titleSize, textLeft, logoH, logoGap, innerPad };
};

/**
 * name.bar v1 (D162): an opaque bar over the names a call app printed in the
 * picture, with our own name. It is solid from its first frame, so nothing of
 * the printed name shows while it arrives; when the name changes (the graphic's
 * segments, D163) the new name fades in quickly (animate slide: slides in;
 * never: appears at once, nothing moves — D199) and the bar stays put. A
 * segment with hide takes the bar away until a later one brings it back.
 */
export const NameBar: React.FC<TemplateProps> = ({ params, brand, layout, segmentFrom }) => {
  const frame = useCurrentFrame();
  const p = parseNameBar(params);
  const logo = usePicture(p.logo, 'name.bar logo');
  if (p.hide) return null;
  const m = nameBarMetrics(p, brand, layout);
  const { unit: u } = layout;
  const since = frame - (segmentFrom ?? 0);
  // How a name comes in (D199): fades (the default), slides, or never moves.
  const textIn = p.animate === 'never' ? 1 : ramp(since, 0, Math.max(2, Math.round((p.animate === 'slide' ? 0.22 : 0.16) * layout.fps)));
  const textOpacity = p.animate === 'never' ? 1 : p.animate === 'slide' ? textIn : 0.35 + 0.65 * textIn;
  const slideX = p.animate === 'slide' ? Math.round((1 - textIn) * -28 * u) : 0;
  const hair = Math.max(2, Math.round(3 * u));
  const logoBox = logo && m.logoH > 0 ? fitInside(logo, m.logoH * 2, m.logoH) : null;
  const textX = p.style === 'tab' ? m.left + m.innerPad : m.textLeft;
  const centerY = m.top + m.height / 2;
  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <div
        style={{
          position: 'absolute',
          left: m.left,
          top: m.top,
          width: m.width,
          height: m.height,
          background: brand.background,
          borderTop: m.atBottom ? `${hair}px solid ${brand.accent}` : undefined,
          borderBottom: m.atBottom ? undefined : `${hair}px solid ${brand.accent}`,
          boxSizing: 'border-box',
        }}
      />
      <div
        style={{
          position: 'absolute',
          left: textX,
          top: centerY,
          transform: slideX ? `translate(${slideX}px, -50%)` : 'translateY(-50%)',
          display: 'flex',
          alignItems: 'center',
          gap: m.logoGap,
          opacity: textOpacity,
        }}
      >
        {logo && logoBox ? <ResourceImage pic={logo} style={{ width: logoBox.width, height: logoBox.height }} /> : null}
        <div>
          {p.name ? (
            <div
              style={{
                fontFamily: m.nameStyle.fontFamily,
                fontWeight: m.nameStyle.fontWeight,
                fontSize: m.nameSize,
                lineHeight: 1.1,
                whiteSpace: 'nowrap',
                color: brand.text,
              }}
            >
              {p.name}
            </div>
          ) : null}
          {p.title ? (
            <div
              style={{
                fontFamily: m.titleStyle.fontFamily,
                fontWeight: m.titleStyle.fontWeight,
                fontSize: m.titleSize,
                lineHeight: 1.25,
                whiteSpace: 'nowrap',
                color: rgba(brand.text, 0.78),
                marginTop: Math.round(4 * u),
              }}
            >
              {p.title}
            </div>
          ) : null}
        </div>
      </div>
    </AbsoluteFill>
  );
};
