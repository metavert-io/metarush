import type { templateSlideCode } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { pick } from '../core/layout';
import { ResourceImage, usePicture } from './resources';
import { strParam, warnOnce, type TemplateProps } from './shared';
import {
  FigureText,
  SlideBackdrop,
  SlideContent,
  figureEnter,
  layoutFigureSlide,
  riseStyle,
  slideMotion,
  slidePalette,
  travelProgress,
  type FigureSize,
} from './slides';

type SlideCodeParams = templateSlideCode.SlideCodeParams;

export const parseSlideCode = (raw: unknown): SlideCodeParams | null => {
  const code = strParam(raw, 'code', 52);
  if (!code) return null;
  return { code, title: strParam(raw, 'title', 80), caption: strParam(raw, 'caption', 140) };
};

/** How a code picture is shown in a box: its scale, the height on screen and how far it pans. */
type CodeSize = FigureSize & { scale: number; full: number; pan: number };

/**
 * Sizes a code picture for a box. It is shown whole when that keeps it
 * readable (MetaRush draws code at a fixed size, so the scale says how big
 * the letters are); otherwise it is shown at a comfortable reading size,
 * as wide as the box allows, in a window as tall as the box, and pans.
 */
const codeSizer =
  (w: number, h: number, u: number, maxShownW: number) =>
  (maxW: number, maxH: number): CodeSize | null => {
    if (!(w > 0 && h > 0) || maxW < 2 || maxH < 2) return null;
    const boxW = Math.min(maxW, maxShownW);
    const sWidth = boxW / w;
    const sFit = Math.min(sWidth, maxH / h);
    const sRead = 0.42 * u;
    const sComfort = 0.54 * u;
    let s = sFit;
    let view = h * sFit;
    let pan = 0;
    if (sFit < Math.min(sRead, sWidth) - 1e-9) {
      s = Math.min(sWidth, Math.max(sComfort, sFit));
      view = Math.min(maxH, h * s);
      pan = Math.max(0, h * s - view);
    }
    const visible = view / (h * s);
    return {
      width: Math.round(w * s),
      height: Math.round(view),
      score: Math.min(1, s / sRead) * Math.sqrt(visible) * Math.sqrt(s),
      scale: s,
      full: h * s,
      pan,
    };
  };

/**
 * slide.code v1: a code picture (a highlighted panel MetaRush made from a
 * repository, transparent around the panel) under a heading, with an optional
 * caption. Code too long to read whole is shown at a reading size and pans
 * slowly from top to bottom over the slide's time, its clipped edges softly
 * faded. In landscape a tall panel goes beside the heading.
 */
export const SlideCode: React.FC<TemplateProps> = (props) => {
  const { params, brand, layout } = props;
  const frame = useCurrentFrame();
  const p = parseSlideCode(params);
  const code = usePicture(p?.code, 'slide.code code');
  const { unit: u, orientation: o } = layout;

  const fl = useMemo(() => {
    if (!p) return null;
    // A picture without a recorded size is taken to be 16:10.
    const w = code?.width && code.height ? code.width : 1600;
    const h = code?.width && code.height ? code.height : 1000;
    const maxShownW = pick(o, { landscape: 1320, square: 4000, vertical: 4000 }) * u;
    return layoutFigureSlide<CodeSize>({
      layout,
      brand,
      title: p.title,
      caption: p.caption,
      sizer: code ? codeSizer(w, h, u, maxShownW) : () => null,
      allowSide: o === 'landscape',
      sideBias: 1.15,
    });
  }, [p?.title, p?.caption, code, brand, layout]);

  if (!p || !fl) {
    warnOnce('slide.code: missing required param "code"; slide skipped');
    return null;
  }

  const pal = slidePalette(brand);
  const m = slideMotion(frame, props, 1.1);
  const c = m.clock;
  const figP = figureEnter(c);
  const size = fl.size;
  const pan = size?.pan ?? 0;
  const t = pan > 0 ? travelProgress(frame, c, 1.1) : 0;
  const fade = size ? Math.round(Math.min(size.height * 0.09, 56 * u)) : 0;
  const topA = pan > 0 ? 1 - Math.min(1, t * 8) : 1;
  const bottomA = pan > 0 ? 1 - Math.min(1, (1 - t) * 8) : 1;
  const mask =
    pan > 0
      ? `linear-gradient(180deg, rgba(0, 0, 0, ${topA.toFixed(3)}) 0px, #000 ${fade}px, #000 calc(100% - ${fade}px), rgba(0, 0, 0, ${bottomA.toFixed(3)}) 100%)`
      : undefined;

  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <SlideBackdrop brand={brand} layout={layout} opacity={m.backdrop} />
      <SlideContent motion={m}>
        <FigureText fl={fl} pal={pal} clock={c} u={u} />
        {code && size ? (
          <div
            style={{
              position: 'absolute',
              left: fl.figure.left,
              top: fl.figure.top,
              width: size.width,
              height: size.height,
              ...riseStyle(figP, 24 * u),
            }}
          >
            <div style={{ width: '100%', height: '100%', overflow: 'hidden', WebkitMaskImage: mask, maskImage: mask }}>
              <div style={{ transform: `translateY(${(-pan * t).toFixed(2)}px)` }}>
                <ResourceImage pic={code} style={{ width: size.width, height: Math.round(size.full) }} />
              </div>
            </div>
          </div>
        ) : null}
      </SlideContent>
    </AbsoluteFill>
  );
};
