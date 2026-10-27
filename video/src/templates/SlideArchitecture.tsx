import type { templateSlideArchitecture } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { pick } from '../core/layout';
import { luminance } from '../theme/brand';
import { ResourceImage, usePicture } from './resources';
import { strParam, warnOnce, type TemplateProps } from './shared';
import {
  FigureText,
  SlideBackdrop,
  SlideContent,
  figureEnter,
  fitScale,
  layoutFigureSlide,
  riseStyle,
  slideMotion,
  slidePalette,
  type FigureSize,
} from './slides';

type SlideArchitectureParams = templateSlideArchitecture.SlideArchitectureParams;

export const parseSlideArchitecture = (raw: unknown): SlideArchitectureParams | null => {
  const diagram = strParam(raw, 'diagram', 52);
  if (!diagram) return null;
  return { diagram, title: strParam(raw, 'title', 80), caption: strParam(raw, 'caption', 140) };
};

/** The card's size in a box, with the diagram's size inside it. */
type CardSize = FigureSize & { pad: number; innerW: number; innerH: number };

/**
 * slide.architecture v1: an architecture diagram (an SVG MetaRush drew, or
 * any picture) on a soft, light card under a heading, so a dark-on-light
 * diagram reads on a dark brand background; an optional caption beneath. The
 * card rises in and the diagram settles onto it a beat later. The diagram is
 * always shown whole, as large as the slide allows.
 */
export const SlideArchitecture: React.FC<TemplateProps> = (props) => {
  const { params, brand, layout } = props;
  const frame = useCurrentFrame();
  const p = parseSlideArchitecture(params);
  const diagram = usePicture(p?.diagram, 'slide.architecture diagram');
  const { unit: u, orientation: o } = layout;

  const fl = useMemo(() => {
    if (!p) return null;
    const w = diagram?.width && diagram.height ? diagram.width : 1600;
    const h = diagram?.width && diagram.height ? diagram.height : 900;
    const pad = Math.round(pick(o, { landscape: 40, square: 34, vertical: 30 }) * u);
    const sizer = (maxW: number, maxH: number): CardSize | null => {
      const s = fitScale(w, h, maxW - 2 * pad, maxH - 2 * pad);
      if (!(s > 0)) return null;
      const innerW = Math.round(w * s);
      const innerH = Math.round(h * s);
      return { width: innerW + 2 * pad, height: innerH + 2 * pad, pad, innerW, innerH };
    };
    return layoutFigureSlide<CardSize>({
      layout,
      brand,
      title: p.title,
      caption: p.caption,
      sizer: diagram ? sizer : () => null,
      allowSide: o === 'landscape',
      sideBias: 1.3,
    });
  }, [p?.title, p?.caption, diagram, brand, layout]);

  if (!p || !fl) {
    warnOnce('slide.architecture: missing required param "diagram"; slide skipped');
    return null;
  }

  const pal = slidePalette(brand);
  const m = slideMotion(frame, props, 1.25);
  const c = m.clock;
  const cardP = figureEnter(c);
  const drawP = c.enter(0.36, 0.8);
  const size = fl.size;
  // A light card on dark brands; on a light background, white with a hairline.
  const card = pal.dark ? (luminance(brand.text) > 0.78 ? brand.text : '#F6F6F4') : '#FFFFFF';

  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <SlideBackdrop brand={brand} layout={layout} opacity={m.backdrop} />
      <SlideContent motion={m}>
        <FigureText fl={fl} pal={pal} clock={c} u={u} />
        {diagram && size ? (
          <div
            style={{
              position: 'absolute',
              left: fl.figure.left,
              top: fl.figure.top,
              width: size.width,
              height: size.height,
              boxSizing: 'border-box',
              padding: size.pad,
              borderRadius: Math.round(26 * u),
              background: card,
              boxShadow: `0 ${Math.round(28 * u)}px ${Math.round(80 * u)}px ${pal.shadow(1.05)}, 0 0 0 1px ${pal.dark ? 'rgba(255, 255, 255, 0.14)' : 'rgba(0, 0, 0, 0.08)'}`,
              ...riseStyle(cardP, 24 * u),
            }}
          >
            <div style={{ width: size.innerW, height: size.innerH, ...riseStyle(drawP, 8 * u) }}>
              <ResourceImage pic={diagram} style={{ width: size.innerW, height: size.innerH }} />
            </div>
          </div>
        ) : null}
      </SlideContent>
    </AbsoluteFill>
  );
};
