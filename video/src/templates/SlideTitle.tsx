import type { templateSlideTitle } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { pick } from '../core/layout';
import type { TextStyle } from '../core/text';
import { rgba } from '../theme/brand';
import { ResourceImage, fitInside, usePicture } from './resources';
import { strParam, trackingFor, typeTune, warnOnce, type TemplateProps } from './shared';
import {
  RisingLines,
  SlideBackdrop,
  SlideContent,
  TextLines,
  fitLine,
  fitText,
  riseStyle,
  slideArea,
  slideMotion,
  slidePalette,
} from './slides';

type SlideTitleParams = templateSlideTitle.SlideTitleParams;

export const parseSlideTitle = (raw: unknown): SlideTitleParams | null => {
  const title = strParam(raw, 'title', 90);
  if (!title) return null;
  return {
    title,
    subtitle: strParam(raw, 'subtitle', 140),
    kicker: strParam(raw, 'kicker', 40),
    logo: strParam(raw, 'logo', 52),
    image: strParam(raw, 'image', 52),
  };
};

/**
 * slide.title v1: a section or opening slide. The title is set large and
 * left-aligned on the brand background, with an optional label above it in
 * the accent (Part 1, How it works), a subtitle beneath, a logo in the top
 * corner and, optionally, a picture behind it all, darkened toward the text.
 * The label (or, without one, a short accent rule) leads in, the title lines
 * rise from their baselines, the subtitle settles last.
 */
export const SlideTitle: React.FC<TemplateProps> = (props) => {
  const { params, brand, layout } = props;
  const frame = useCurrentFrame();
  const p = parseSlideTitle(params);
  const logo = usePicture(p?.logo, 'slide.title logo');
  const image = usePicture(p?.image, 'slide.title image');
  const { unit: u, orientation: o } = layout;

  const L = useMemo(() => {
    if (!p) return null;
    const tune = typeTune(brand.font);
    const area = slideArea(layout);
    const textW = pick(o, { landscape: Math.min(area.width, 1400 * u), square: area.width, vertical: area.width });
    const logoBox = logo ? fitInside(logo, Math.min(area.width * 0.34, 440 * u), Math.round(pick(o, { landscape: 60, square: 60, vertical: 66 }) * u)) : null;
    const logoGap = logoBox ? Math.round(pick(o, { landscape: 48, square: 44, vertical: 56 }) * u) : 0;
    const region = { top: area.top + (logoBox ? logoBox.height + logoGap : 0), bottom: area.top + area.height };

    // The label above the title: small, tracked capitals. Without one, a short accent rule leads in.
    const kickerStyle: TextStyle = { fontFamily: brand.fontFamily, fontWeight: tune.strong - 20, letterSpacing: '0.16em', uppercase: true };
    const kickerBase = Math.round(pick(o, { landscape: 25, square: 25, vertical: 28 }) * u);
    const kickerSize = p.kicker ? fitLine(p.kicker, kickerStyle, textW, kickerBase, Math.round(14 * u)) : 0;
    const leadH = p.kicker ? Math.round(kickerSize * 1.2) : Math.max(2, Math.round(4 * u));
    const leadGap = Math.round((p.kicker ? 24 : 34) * u);

    const maxSize = pick(o, { landscape: 136, square: 116, vertical: 112 }) * u;
    const lineHeight = Math.max(tune.lineDisplay, 1.04);
    const titleStyle: TextStyle = { fontFamily: brand.fontFamily, fontWeight: tune.display, letterSpacing: trackingFor(brand.font, maxSize, u) };
    const subStyle: TextStyle = { fontFamily: brand.fontFamily, fontWeight: tune.body, letterSpacing: '0.002em' };
    const subMax = Math.round(pick(o, { landscape: 40, square: 38, vertical: 42 }) * u);
    const roomH = region.bottom - region.top;
    const title = fitText(p.title, titleStyle, {
      maxWidth: textW,
      maxLines: pick(o, { landscape: 3, square: 3, vertical: 4 }),
      maxHeight: Math.max(maxSize, roomH - leadH - leadGap - (p.subtitle ? subMax * 1.36 * 2 + 30 * u : 0)),
      lineHeight,
      maxSize,
      minSize: Math.round(48 * u),
    });
    if (!title) return null;
    const subGap = p.subtitle ? Math.max(Math.round(30 * u), Math.round(title.fontSize * 0.4)) : 0;
    const sub = fitText(p.subtitle, subStyle, {
      maxWidth: Math.min(textW, Math.round(pick(o, { landscape: 1160, square: 900, vertical: 900 }) * u)),
      maxLines: 3,
      lineHeight: 1.36,
      maxSize: subMax,
      minSize: Math.round(26 * u),
    });
    const textH = leadH + leadGap + title.height + (sub ? subGap + sub.height : 0);
    const centred = area.top + (area.height - textH) * 0.5;
    const top = Math.max(region.top, Math.min(centred, region.bottom - textH));
    return { area, textW, logoBox, kickerStyle, kickerSize, leadH, leadGap, title, sub, subGap, top };
  }, [p?.title, p?.subtitle, p?.kicker, logo, brand, layout]);

  if (!p || !L) {
    warnOnce('slide.title: missing required param "title"; slide skipped');
    return null;
  }

  const pal = slidePalette(brand);
  const m = slideMotion(frame, props, 1.35);
  const c = m.clock;
  const logoP = c.enter(0.02, 0.75);
  const leadP = c.enter(0.08, 0.7);
  const lineP = (i: number) => c.enter(0.16 + i * 0.08, 0.9);
  const subP = c.enter(0.5, 0.8);
  const imageIn = c.enter(0, 1.3);
  // When another card follows, the picture goes with the content so the next slide starts on the plain background.
  const imageOut = props.joins?.after ? m.exit : 1;

  const veil = pick(o, {
    landscape: `linear-gradient(90deg, ${rgba(brand.background, 0.92)} 0%, ${rgba(brand.background, 0.8)} 42%, ${rgba(brand.background, 0.52)} 100%)`,
    square: `linear-gradient(180deg, ${rgba(brand.background, 0.62)} 0%, ${rgba(brand.background, 0.86)} 55%, ${rgba(brand.background, 0.9)} 100%)`,
    vertical: `linear-gradient(180deg, ${rgba(brand.background, 0.6)} 0%, ${rgba(brand.background, 0.86)} 48%, ${rgba(brand.background, 0.74)} 100%)`,
  });

  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <SlideBackdrop brand={brand} layout={layout} opacity={m.backdrop}>
        {image ? (
          <AbsoluteFill style={{ opacity: Math.min(1, imageIn * 1.4) * imageOut }}>
            <AbsoluteFill style={{ transform: `scale(${(1.045 - 0.045 * imageIn).toFixed(4)})` }}>
              <ResourceImage pic={image} fit="cover" style={{ width: '100%', height: '100%' }} />
            </AbsoluteFill>
            <AbsoluteFill style={{ background: veil }} />
          </AbsoluteFill>
        ) : null}
      </SlideBackdrop>
      <SlideContent motion={m}>
        {logo && L.logoBox ? (
          <div
            style={{
              position: 'absolute',
              left: L.area.left,
              top: L.area.top,
              width: L.logoBox.width,
              height: L.logoBox.height,
              ...riseStyle(logoP, 10 * u),
            }}
          >
            <ResourceImage pic={logo} style={{ width: L.logoBox.width, height: L.logoBox.height }} />
          </div>
        ) : null}
        <div style={{ position: 'absolute', left: L.area.left, top: L.top, width: L.textW, color: pal.text }}>
          {p.kicker ? (
            <div
              style={{
                height: L.leadH,
                marginBottom: L.leadGap,
                fontFamily: L.kickerStyle.fontFamily,
                fontWeight: L.kickerStyle.fontWeight,
                fontSize: L.kickerSize,
                lineHeight: `${L.leadH}px`,
                letterSpacing: L.kickerStyle.letterSpacing,
                textTransform: 'uppercase',
                whiteSpace: 'nowrap',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                color: pal.accentInk,
                ...riseStyle(leadP, 8 * u),
              }}
            >
              {p.kicker}
            </div>
          ) : (
            <div
              style={{
                width: Math.round(56 * u),
                height: L.leadH,
                borderRadius: L.leadH,
                marginBottom: L.leadGap,
                background: pal.accent,
                transform: `scaleX(${leadP.toFixed(4)})`,
                transformOrigin: '0% 50%',
              }}
            />
          )}
          <RisingLines t={L.title} color={pal.text} progress={lineP} />
          {L.sub ? (
            <div style={{ marginTop: L.subGap, ...riseStyle(subP, 12 * u) }}>
              <TextLines t={L.sub} color={rgba(brand.text, 0.74)} />
            </div>
          ) : null}
        </div>
      </SlideContent>
    </AbsoluteFill>
  );
};
