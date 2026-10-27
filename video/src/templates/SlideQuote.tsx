import type { templateSlideQuote } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { pick } from '../core/layout';
import type { TextStyle } from '../core/text';
import { SERIF_STACK } from '../theme/fonts';
import { ResourceImage, fitInside, usePicture } from './resources';
import { strParam, trackingFor, typeTune, warnOnce, type TemplateProps } from './shared';
import { RisingLines, SlideBackdrop, SlideContent, TextLines, fitText, riseStyle, slideArea, slideMotion, slidePalette } from './slides';

type SlideQuoteParams = templateSlideQuote.SlideQuoteParams;

export const parseSlideQuote = (raw: unknown): SlideQuoteParams | null => {
  const text = strParam(raw, 'text', 280);
  if (!text) return null;
  return { text, attribution: strParam(raw, 'attribution', 80), role: strParam(raw, 'role', 80), image: strParam(raw, 'image', 52) };
};

/** Strip wrapping straight/curly quotes; the slide sets its own mark. */
const unquote = (t: string) => t.replace(/^["“”'‘’]+/, '').replace(/["“”'‘’]+$/, '').trim();

/**
 * slide.quote v1: a quotation set large and left-aligned under an accent
 * quotation mark, with who said it (name, then role, muted) beneath. A
 * portrait or logo, when given, stands beside the quote in landscape and
 * above it in vertical and square frames. Lines rise in one after another;
 * the attribution follows.
 */
export const SlideQuote: React.FC<TemplateProps> = (props) => {
  const { params, brand, layout } = props;
  const frame = useCurrentFrame();
  const p = parseSlideQuote(params);
  const image = usePicture(p?.image, 'slide.quote image');
  const { unit: u, orientation: o } = layout;

  const L = useMemo(() => {
    if (!p) return null;
    const text = unquote(p.text);
    if (!text) return null;
    const tune = typeTune(brand.font);
    const area = slideArea(layout);
    const side = !!image && o === 'landscape';
    const imgColW = side ? Math.round(area.width * 0.3) : 0;
    const gap = side ? Math.round(88 * u) : 0;
    const colW = side ? area.width - imgColW - gap : Math.min(area.width, Math.round(1440 * u));
    const imgSide = side && image ? fitInside(image, imgColW, Math.round(area.height * 0.8)) : null;
    const imgStack = image && !side ? fitInside(image, Math.min(area.width * 0.62, 560 * u), Math.round(area.height * 0.24)) : null;
    const imgGap = imgStack ? Math.round(52 * u) : 0;

    const maxSize = pick(o, { landscape: 72, square: 64, vertical: 64 }) * u;
    const serif = brand.font === 'Fraunces';
    const lineHeight = serif ? 1.16 : 1.22;
    const style: TextStyle = {
      fontFamily: brand.fontFamily,
      fontWeight: serif ? 440 : brand.font === 'JetBrains Mono' ? 480 : 500,
      letterSpacing: trackingFor(brand.font, maxSize, u),
    };
    // Who said it: the name, then the role (muted), each wrapping to a second line when long.
    const nameBase = Math.round(pick(o, { landscape: 32, square: 32, vertical: 34 }) * u);
    const nameStyle: TextStyle = { fontFamily: brand.fontFamily, fontWeight: tune.strong - 20, letterSpacing: '0.002em' };
    const roleStyle: TextStyle = { fontFamily: brand.fontFamily, fontWeight: tune.body, letterSpacing: '0.004em' };
    const attrW = Math.min(colW, Math.round(pick(o, { landscape: 1000, square: 860, vertical: 900 }) * u));
    const name = fitText(p.attribution, nameStyle, { maxWidth: attrW, maxLines: 2, lineHeight: 1.3, maxSize: nameBase, minSize: Math.round(24 * u) });
    const role = fitText(p.role, roleStyle, { maxWidth: attrW, maxLines: 2, lineHeight: 1.36, maxSize: Math.round(nameBase * 0.86), minSize: Math.round(22 * u) });
    const roleGap = name && role ? Math.round(4 * u) : 0;
    const attrH = (name?.height ?? 0) + roleGap + (role?.height ?? 0);
    const reserve = (imgStack ? imgStack.height + imgGap : 0) + maxSize * 1.1 + (attrH ? attrH + 40 * u : 0);
    const quote = fitText(text, style, {
      maxWidth: colW,
      maxLines: pick(o, { landscape: side ? 6 : 5, square: 6, vertical: 8 }),
      maxHeight: Math.max(maxSize * 2, area.height - reserve),
      lineHeight,
      maxSize,
      minSize: Math.round(34 * u),
    });
    if (!quote) return null;
    const markSize = Math.round(quote.fontSize * 2.6);
    const markBox = Math.round(markSize * 0.42);
    const markGap = Math.round(quote.fontSize * 0.22);
    const attrGap = attrH ? Math.max(Math.round(36 * u), Math.round(quote.fontSize * 0.72)) : 0;
    const textH = markBox + markGap + quote.height + attrGap + attrH;
    const blockH = (imgStack ? imgStack.height + imgGap : 0) + textH;
    const top = area.top + Math.max(0, (area.height - blockH) * 0.44);
    const groupW = side && imgSide ? imgSide.width + gap + colW : colW;
    const left = area.left + (side ? Math.max(0, (area.width - groupW) / 2) : 0);
    return {
      area,
      side,
      gap,
      colW,
      imgSide,
      imgStack,
      imgGap,
      quote,
      markSize,
      markBox,
      markGap,
      attrGap,
      name,
      role,
      roleGap,
      top: side ? area.top + Math.max(0, (area.height - textH) * 0.46) : top,
      left,
    };
  }, [p?.text, p?.attribution, p?.role, image, brand, layout]);

  if (!p || !L) {
    warnOnce('slide.quote: missing required param "text"; slide skipped');
    return null;
  }

  const pal = slidePalette(brand);
  const m = slideMotion(frame, props, 1.3);
  const c = m.clock;
  const imgP = c.enter(0.04, 0.9);
  const markP = c.enter(0.06, 0.7);
  const lineP = (i: number) => c.enter(0.14 + i * 0.07, 0.85);
  const attrP = c.enter(Math.min(0.62, 0.3 + L.quote.lines.length * 0.07), 0.7);
  const radius = Math.round(22 * u);
  const pictureStyle: React.CSSProperties = {
    borderRadius: radius,
    overflow: 'hidden',
    boxShadow: `0 ${Math.round(24 * u)}px ${Math.round(64 * u)}px ${pal.shadow(1)}, 0 0 0 1px ${pal.hairline}`,
  };
  const textLeft = L.side && L.imgSide ? L.left + L.imgSide.width + L.gap : L.area.left;
  const textTop = L.top + (L.imgStack ? L.imgStack.height + L.imgGap : 0);

  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <SlideBackdrop brand={brand} layout={layout} opacity={m.backdrop} />
      <SlideContent motion={m}>
        {image && L.imgSide ? (
          <div
            style={{
              position: 'absolute',
              left: L.left,
              top: L.area.top + (L.area.height - L.imgSide.height) / 2,
              width: L.imgSide.width,
              height: L.imgSide.height,
              ...pictureStyle,
              ...riseStyle(imgP, 20 * u),
            }}
          >
            <ResourceImage pic={image} style={{ width: L.imgSide.width, height: L.imgSide.height }} />
          </div>
        ) : null}
        {image && L.imgStack ? (
          <div
            style={{
              position: 'absolute',
              left: L.area.left,
              top: L.top,
              width: L.imgStack.width,
              height: L.imgStack.height,
              ...pictureStyle,
              ...riseStyle(imgP, 16 * u),
            }}
          >
            <ResourceImage pic={image} style={{ width: L.imgStack.width, height: L.imgStack.height }} />
          </div>
        ) : null}
        <div style={{ position: 'absolute', left: textLeft, top: textTop, width: L.colW }}>
          <div
            aria-hidden
            style={{
              fontFamily: SERIF_STACK,
              fontWeight: 500,
              fontSize: L.markSize,
              lineHeight: 1,
              height: L.markBox,
              marginBottom: L.markGap,
              marginLeft: -L.markSize * 0.04,
              color: pal.accent,
              ...riseStyle(markP, 0.05 * L.markSize),
            }}
          >
            {'“'}
          </div>
          <RisingLines t={L.quote} color={pal.text} progress={lineP} />
          {L.name || L.role ? (
            <div style={{ marginTop: L.attrGap, ...riseStyle(attrP, 10 * u) }}>
              {L.name ? <TextLines t={L.name} color={pal.text} /> : null}
              {L.role ? <TextLines t={L.role} color={pal.muted} style={{ marginTop: L.roleGap }} /> : null}
            </div>
          ) : null}
        </div>
      </SlideContent>
    </AbsoluteFill>
  );
};
