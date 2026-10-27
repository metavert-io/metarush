import type { templateSlideBullets } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { pick } from '../core/layout';
import { EASE_OUT, ramp } from '../core/motion';
import type { TextStyle } from '../core/text';
import { ResourceImage, fitInside, usePicture } from './resources';
import { enumParam, strArrayParam, strParam, typeTune, warnOnce, type TemplateProps } from './shared';
import {
  SlideBackdrop,
  SlideContent,
  SlideHeader,
  TextLines,
  clamp,
  fitHeader,
  fitParagraphs,
  riseStyle,
  slideArea,
  slideMotion,
  slidePalette,
} from './slides';

type SlideBulletsParams = templateSlideBullets.SlideBulletsParams;

export const parseSlideBullets = (raw: unknown): SlideBulletsParams | null => {
  const title = strParam(raw, 'title', 80);
  const bullets = strArrayParam(raw, 'bullets', 6)
    .map((b) => b.replace(/\s+/g, ' ').trim().slice(0, 200))
    .filter((b) => b.length > 0);
  if (!title || bullets.length === 0) return null;
  return { title, bullets, image: strParam(raw, 'image', 52), reveal: enumParam(raw, 'reveal', ['one-by-one', 'all'] as const) };
};

/** Where each point's marker sits, in ems of the point's size (the middle of the first line's lowercase). */
const MARKER_EM = 0.3;
const MARKER_COL_EM = 1.2;
const LINE_HEIGHT = 1.34;
const GAP_EM = 0.72;

/**
 * slide.bullets v1: a heading and up to six points. Points are left-aligned
 * beside small accent markers, all at one size, and appear one by one across
 * the slide's time (or all together with `reveal: "all"`), so each lands as
 * it is talked about; nothing moves once placed. A picture, when given, sits
 * beside the points in landscape and between the heading and the points in
 * vertical and square frames.
 */
export const SlideBullets: React.FC<TemplateProps> = (props) => {
  const { params, brand, layout, durationInFrames } = props;
  const frame = useCurrentFrame();
  const p = parseSlideBullets(params);
  const image = usePicture(p?.image, 'slide.bullets image');
  const { unit: u, orientation: o, fps } = layout;

  const L = useMemo(() => {
    if (!p) return null;
    const tune = typeTune(brand.font);
    const area = slideArea(layout);
    const side = !!image && o === 'landscape';
    const gap = side ? Math.round(72 * u) : 0;
    const imgColW = side ? Math.round(area.width * 0.42) : 0;
    const colW = side ? area.width - imgColW - gap : Math.min(area.width, Math.round(1440 * u));
    const header = fitHeader(p.title, brand, layout, colW, side ? 3 : 2, area.height * 0.34);
    const headH = header ? header.height : 0;
    const headGap = Math.round(pick(o, { landscape: 48, square: 42, vertical: 52 }) * u);
    // Above the points, the picture gives up room as points are added.
    const imgShare = [0.32, 0.32, 0.3, 0.27, 0.24, 0.22, 0.2][p.bullets.length] ?? 0.2;
    const imgStack = image && !side ? fitInside(image, area.width, Math.round(area.height * imgShare)) : null;
    const imgGap = imgStack ? Math.round(pick(o, { landscape: 44, square: 40, vertical: 48 }) * u) : 0;
    const maxSize = Math.round(pick(o, { landscape: 48, square: 46, vertical: 50 }) * u);
    const style: TextStyle = { fontFamily: brand.fontFamily, fontWeight: tune.body + 20, letterSpacing: '-0.002em' };
    const markerCol = Math.round(maxSize * MARKER_COL_EM);
    const fit = fitParagraphs(p.bullets, style, {
      maxWidth: colW - markerCol,
      maxLines: pick(o, { landscape: 3, square: 3, vertical: 4 }),
      maxHeight: Math.max(maxSize * 2, area.height - headH - headGap - (imgStack ? imgStack.height + imgGap : 0)),
      lineHeight: LINE_HEIGHT,
      gapEm: GAP_EM,
      maxSize,
      minSize: Math.round(26 * u),
    });
    const blockH = headH + headGap + (imgStack ? imgStack.height + imgGap : 0) + fit.height;
    const top = area.top + Math.max(0, (area.height - blockH) * 0.44);
    const imgSide = side && image ? fitInside(image, imgColW, Math.round(area.height * 0.9)) : null;
    return { area, side, gap, imgColW, colW, header, headH, headGap, imgStack, imgGap, imgSide, fit, markerCol, top };
  }, [p?.title, p?.bullets.join('\n'), image, brand, layout]);

  if (!p || !L) {
    warnOnce('slide.bullets: requires "title" and at least one point in "bullets"; slide skipped');
    return null;
  }

  const pal = slidePalette(brand);
  const m = slideMotion(frame, props, 1.0);
  const c = m.clock;

  // Points: one by one across the first ~60% of the slide (all together on request).
  const n = L.fit.items.length;
  const exitStart = durationInFrames - c.exitFrames;
  const pointDur = Math.max(4, Math.min(Math.round(0.55 * fps), Math.round(exitStart * 0.3)));
  const firstAt = Math.min(Math.round(0.5 * fps), Math.round(exitStart * 0.28));
  const oneByOne = p.reveal !== 'all';
  const latest = Math.max(firstAt, Math.round(exitStart * 0.6));
  const room = Math.max(0, exitStart - pointDur - firstAt);
  const step = n > 1 ? (oneByOne ? Math.min(clamp((latest - firstAt) / (n - 1), 0.28 * fps, 4 * fps), room / (n - 1)) : Math.min(0.07 * fps, room / (n - 1))) : 0;
  const pointP = (i: number) => ramp(frame, firstAt + i * step, pointDur, EASE_OUT);
  const imgP = c.enter(0.22, 0.9);

  const bulletsTop = L.top + L.headH + L.headGap + (L.imgStack ? L.imgStack.height + L.imgGap : 0);
  const size = L.fit.fontSize;
  const markerCol = Math.round(size * MARKER_COL_EM);
  const radius = Math.round(22 * u);
  const pictureStyle: React.CSSProperties = {
    borderRadius: radius,
    overflow: 'hidden',
    boxShadow: `0 ${Math.round(24 * u)}px ${Math.round(64 * u)}px ${pal.shadow(1)}, 0 0 0 1px ${pal.hairline}`,
  };

  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <SlideBackdrop brand={brand} layout={layout} opacity={m.backdrop} />
      <SlideContent motion={m}>
        {L.header ? <SlideHeader h={L.header} pal={pal} clock={c} style={{ position: 'absolute', left: L.area.left, top: L.top, width: L.colW }} /> : null}
        {image && L.imgStack ? (
          <div
            style={{
              position: 'absolute',
              left: L.area.left,
              top: L.top + L.headH + L.headGap,
              width: L.imgStack.width,
              height: L.imgStack.height,
              ...pictureStyle,
              ...riseStyle(imgP, 18 * u),
            }}
          >
            <ResourceImage pic={image} style={{ width: L.imgStack.width, height: L.imgStack.height }} />
          </div>
        ) : null}
        {image && L.imgSide ? (
          <div
            style={{
              position: 'absolute',
              left: L.area.left + L.colW + L.gap + (L.imgColW - L.imgSide.width) / 2,
              top: L.area.top + (L.area.height - L.imgSide.height) / 2,
              width: L.imgSide.width,
              height: L.imgSide.height,
              ...pictureStyle,
              ...riseStyle(imgP, 22 * u),
            }}
          >
            <ResourceImage pic={image} style={{ width: L.imgSide.width, height: L.imgSide.height }} />
          </div>
        ) : null}
        <div style={{ position: 'absolute', left: L.area.left, top: bulletsTop, width: L.colW }}>
          {L.fit.items.map((it, i) => {
            const t = pointP(i);
            // A point set smaller than the rest (a long URL) gets a marker to match its own first line.
            const marker = Math.max(4, Math.round(it.fontSize * MARKER_EM));
            return (
              <div key={i} style={{ position: 'relative', paddingLeft: markerCol, marginTop: i ? Math.round(GAP_EM * size) : 0, ...riseStyle(t, 14 * u) }}>
                <div
                  style={{
                    position: 'absolute',
                    left: Math.round(size * 0.04),
                    top: Math.round(it.fontSize * 0.72 - marker / 2),
                    width: marker,
                    height: marker,
                    borderRadius: marker,
                    background: pal.accent,
                  }}
                />
                <TextLines t={it} color={pal.body} />
              </div>
            );
          })}
        </div>
      </SlideContent>
    </AbsoluteFill>
  );
};
