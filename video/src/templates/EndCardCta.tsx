import type { EndCardParams } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { pick } from '../core/layout';
import { makeOverlayClock } from '../core/motion';
import { fitSingleLine, fitWords, splitWords, type TextStyle } from '../core/text';
import { rgba } from '../theme/brand';
import { ResourceImage, fitInside, usePicture } from './resources';
import { MaskLine, strParam, trackingFor, typeTune, warnOnce, type TemplateProps } from './shared';

export const parseEndCard = (raw: unknown): EndCardParams | null => {
  const headline = strParam(raw, 'headline', 80);
  if (!headline) return null;
  return {
    headline,
    subline: strParam(raw, 'subline', 120),
    cta: strParam(raw, 'cta', 40),
    handle: strParam(raw, 'handle', 40),
    logo: strParam(raw, 'logo', 52),
    image: strParam(raw, 'image', 52),
  };
};

/**
 * endcard.cta v1: a full brand-background card. Headline lines rise into
 * place, then subline, a pill CTA (contrast-checked label) and the handle
 * anchored to the bottom safe edge. When the card ends with the video, the
 * background holds through the exit so the last frame is a clean brand frame.
 * Optional resources: `logo` above the headline; `image` (a picture or a PDF
 * page) behind the card under a brand-coloured veil that keeps text readable.
 */
export const EndCardCta: React.FC<TemplateProps> = ({
  params,
  durationInFrames,
  from,
  planDurationInFrames,
  brand,
  layout,
}) => {
  const frame = useCurrentFrame();
  const p = parseEndCard(params);
  const logo = usePicture(p?.logo, 'endcard.cta logo');
  const image = usePicture(p?.image, 'endcard.cta image');
  const { unit: u, orientation: o, content, fps } = layout;
  const tune = typeTune(brand.font);

  const maxSize = pick(o, { vertical: 104, landscape: 116, square: 96 }) * u;
  const textW = pick(o, {
    vertical: content.width,
    landscape: Math.min(content.width, 1400 * u),
    square: content.width,
  });
  const headStyle: TextStyle = useMemo(
    () => ({ fontFamily: brand.fontFamily, fontWeight: tune.display, letterSpacing: trackingFor(brand.font, maxSize, u) }),
    [brand.fontFamily, brand.font, tune, maxSize, u],
  );
  const headWords = useMemo(() => splitWords(p?.headline ?? ''), [p?.headline]);
  const head = useMemo(
    () =>
      fitWords({
        words: headWords,
        style: headStyle,
        maxWidth: textW,
        maxLines: 3,
        maxHeight: logo ? content.height * 0.42 : undefined,
        lineHeight: tune.lineDisplay,
        maxSize,
        minSize: Math.round(44 * u),
      }),
    [headWords, headStyle, textW, logo, content.height, tune.lineDisplay, maxSize, u],
  );

  const subSize = Math.max(Math.round(30 * u), Math.round(head.fontSize * 0.34));
  const subStyle: TextStyle = useMemo(
    () => ({ fontFamily: brand.fontFamily, fontWeight: tune.body, letterSpacing: '0em' }),
    [brand.fontFamily, tune],
  );
  const subWords = useMemo(() => splitWords(p?.subline ?? ''), [p?.subline]);
  const sub = useMemo(
    () =>
      fitWords({
        words: subWords,
        style: subStyle,
        maxWidth: Math.min(textW, Math.max(head.widest, 640 * u)),
        maxLines: 3,
        lineHeight: 1.36,
        maxSize: subSize,
        minSize: Math.round(24 * u),
      }),
    [subWords, subStyle, textW, head.widest, subSize, u],
  );

  const ctaBase = Math.round(pick(o, { vertical: 38, landscape: 34, square: 34 }) * u);
  const ctaStyle: TextStyle = useMemo(
    () => ({ fontFamily: brand.fontFamily, fontWeight: tune.strong, letterSpacing: '0.005em' }),
    [brand.fontFamily, tune],
  );
  // The pill never wraps: shrink its label to fit (padding is 1.1em per side).
  const ctaSize = useMemo(
    () => (p?.cta ? Math.max(Math.round(22 * u), fitSingleLine(p.cta, ctaStyle, textW - 2.2 * ctaBase, ctaBase)) : ctaBase),
    [p?.cta, ctaStyle, textW, ctaBase, u],
  );

  if (!p) {
    warnOnce('endcard.cta: missing required param "headline"; overlay skipped');
    return null;
  }

  const c = makeOverlayClock(frame, durationInFrames, fps, 1.4);
  const bg = c.enter(0, 0.5);
  const lineP = (i: number) => c.enter(0.15 + i * 0.07, 0.85);
  const subP = c.enter(0.45, 0.8);
  const ctaP = c.enter(0.62, 0.8);
  const handleP = c.enter(0.78, 0.7);
  const exit = c.exit;
  const endsWithVideo = from + durationInFrames >= planDurationInFrames - 1;
  const bgOpacity = bg * (endsWithVideo ? 1 : exit);

  const handleSize = Math.round(pick(o, { vertical: 30, landscape: 26, square: 28 }) * u);
  const handle = p.handle ? (p.handle.startsWith('@') || p.handle.includes('.') ? p.handle : `@${p.handle}`) : undefined;
  const contentLeft = content.left + (content.width - textW) / 2;
  const logoP = c.enter(0.05, 0.7);
  const logoBox = logo ? fitInside(logo, Math.min(textW * 0.5, 520 * u), Math.max(56 * u, Math.min(head.fontSize * 1.1, content.height * 0.14))) : null;

  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      {image ? (
        <AbsoluteFill style={{ opacity: bgOpacity }}>
          <ResourceImage pic={image} fit="cover" style={{ width: '100%', height: '100%' }} />
        </AbsoluteFill>
      ) : null}
      <AbsoluteFill
        style={{
          opacity: bgOpacity,
          backgroundColor: image ? rgba(brand.background, 0.72) : brand.background,
          backgroundImage: `radial-gradient(120% 80% at 0% 0%, ${rgba(brand.primary, 0.07)}, ${rgba(brand.primary, 0)} 60%)`,
        }}
      />
      <div
        style={{
          position: 'absolute',
          left: contentLeft,
          width: textW,
          top: content.top,
          height: content.height,
          display: 'flex',
          flexDirection: 'column',
          justifyContent: 'center',
          opacity: exit,
          transform: `translateY(${(1 - exit) * -10 * u}px)`,
          color: brand.text,
        }}
      >
        {logo && logoBox ? (
          <div
            style={{
              width: logoBox.width,
              height: logoBox.height,
              marginBottom: Math.round(head.fontSize * 0.45),
              opacity: logoP,
              transform: `translateY(${(1 - logoP) * 10 * u}px)`,
            }}
          >
            <ResourceImage pic={logo} style={{ width: logoBox.width, height: logoBox.height }} />
          </div>
        ) : null}
        <div>
          {head.lines.map((ln, i) => (
            <MaskLine key={i} progress={lineP(i)} fontSize={head.fontSize}>
              <div
                style={{
                  fontFamily: headStyle.fontFamily,
                  fontWeight: headStyle.fontWeight,
                  fontSize: head.fontSize,
                  lineHeight: tune.lineDisplay,
                  letterSpacing: headStyle.letterSpacing,
                  whiteSpace: 'nowrap',
                }}
              >
                {ln.map((j) => headWords[j]).join(' ')}
              </div>
            </MaskLine>
          ))}
        </div>
        {subWords.length ? (
          <div
            style={{
              marginTop: Math.round(head.fontSize * 0.42),
              opacity: subP,
              transform: `translateY(${(1 - subP) * 14 * u}px)`,
            }}
          >
            {sub.lines.map((ln, i) => (
              <div
                key={i}
                style={{
                  fontFamily: subStyle.fontFamily,
                  fontWeight: subStyle.fontWeight,
                  fontSize: sub.fontSize,
                  lineHeight: 1.36,
                  whiteSpace: 'nowrap',
                  color: rgba(brand.text, 0.74),
                }}
              >
                {ln.map((j) => subWords[j]).join(' ')}
              </div>
            ))}
          </div>
        ) : null}
        {p.cta ? (
          <div
            style={{
              marginTop: Math.round(pick(o, { vertical: 64, landscape: 52, square: 52 }) * u),
              opacity: ctaP,
              transform: `translateY(${(1 - ctaP) * 14 * u}px)`,
            }}
          >
            <span
              style={{
                display: 'inline-block',
                fontFamily: ctaStyle.fontFamily,
                fontWeight: ctaStyle.fontWeight,
                fontSize: ctaSize,
                letterSpacing: ctaStyle.letterSpacing,
                lineHeight: 1,
                padding: '0.62em 1.1em 0.66em',
                borderRadius: 999,
                background: brand.accent,
                color: brand.onAccent,
                whiteSpace: 'nowrap',
              }}
            >
              {p.cta}
            </span>
          </div>
        ) : null}
      </div>
      {handle ? (
        <div
          style={{
            position: 'absolute',
            left: contentLeft,
            width: textW,
            top: content.top + content.height - handleSize * 1.3,
            fontFamily: brand.fontFamily,
            fontWeight: tune.label,
            fontSize: handleSize,
            lineHeight: 1.3,
            letterSpacing: '0.01em',
            color: rgba(brand.text, 0.62),
            whiteSpace: 'nowrap',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            opacity: handleP * exit,
          }}
        >
          {handle}
        </div>
      ) : null}
    </AbsoluteFill>
  );
};
