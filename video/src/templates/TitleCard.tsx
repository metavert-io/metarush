import type { TitleCardParams } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { pick } from '../core/layout';
import { makeOverlayClock } from '../core/motion';
import { fitWords, splitWords, type TextStyle } from '../core/text';
import { rgba } from '../theme/brand';
import { ResourceImage, fitInside, usePicture } from './resources';
import { MaskLine, strParam, trackingFor, typeTune, warnOnce, type TemplateProps } from './shared';

export const parseTitleCard = (raw: unknown): TitleCardParams | null => {
  const title = strParam(raw, 'title', 80);
  if (!title) return null;
  return { title, subtitle: strParam(raw, 'subtitle', 120), logo: strParam(raw, 'logo', 52), image: strParam(raw, 'image', 52) };
};

/**
 * title.card v1: an opening title over a softened frame. The footage is dimmed
 * with a gentle vignette and a light defocus (it recedes, it does not vanish);
 * title lines rise from their baselines, a short accent rule draws, and the
 * subtitle settles beneath. At the very start of a video the frame begins
 * already softened so the first frame is never a bare shot.
 * Optional resources: `logo` sits above the title; `image` (a picture or a
 * PDF page) fills the frame behind the card in place of the softened footage.
 */
export const TitleCard: React.FC<TemplateProps> = ({ params, durationInFrames, from, brand, layout }) => {
  const frame = useCurrentFrame();
  const p = parseTitleCard(params);
  const logo = usePicture(p?.logo, 'title.card logo');
  const image = usePicture(p?.image, 'title.card image');
  const { unit: u, orientation: o, content, fps } = layout;
  const tune = typeTune(brand.font);

  const maxSize = pick(o, { vertical: 124, landscape: 132, square: 112 }) * u;
  const maxWidth = pick(o, {
    vertical: content.width,
    landscape: Math.min(content.width, 1500 * u),
    square: content.width,
  });
  const style: TextStyle = useMemo(
    () => ({ fontFamily: brand.fontFamily, fontWeight: tune.display, letterSpacing: trackingFor(brand.font, maxSize, u) }),
    [brand.fontFamily, brand.font, tune, maxSize, u],
  );
  const words = useMemo(() => splitWords(p?.title ?? ''), [p?.title]);
  const fit = useMemo(
    () =>
      fitWords({
        words,
        style,
        maxWidth,
        maxLines: 3,
        maxHeight: content.height * (logo ? 0.42 : 0.55),
        lineHeight: tune.lineDisplay,
        maxSize,
        minSize: Math.round(48 * u),
      }),
    [words, style, maxWidth, content.height, logo, tune.lineDisplay, maxSize, u],
  );
  const subSize = Math.max(Math.round(30 * u), Math.round(fit.fontSize * 0.3));
  const subStyle: TextStyle = useMemo(
    () => ({ fontFamily: brand.fontFamily, fontWeight: tune.body, letterSpacing: '0.002em' }),
    [brand.fontFamily, tune],
  );
  const subWords = useMemo(() => splitWords(p?.subtitle ?? ''), [p?.subtitle]);
  const sub = useMemo(
    () =>
      fitWords({
        words: subWords,
        style: subStyle,
        maxWidth: Math.min(maxWidth, Math.max(fit.widest * 1.1, 620 * u)),
        maxLines: 3,
        lineHeight: 1.34,
        maxSize: subSize,
        minSize: Math.round(24 * u),
      }),
    [subWords, subStyle, maxWidth, fit.widest, subSize, u],
  );

  if (!p) {
    warnOnce('title.card: missing required param "title"; overlay skipped');
    return null;
  }

  const c = makeOverlayClock(frame, durationInFrames, fps, 1.3);
  const atStart = from <= 0;
  const soften = atStart ? 1 : c.enter(0, 0.7);
  const lineP = (i: number) => c.enter(0.12 + i * 0.08, 0.9);
  const ruleP = c.enter(0.45, 0.6);
  const subP = c.enter(0.55, 0.75);
  const exit = c.exit;
  const veil = soften * exit;

  const ruleW = Math.round(56 * u);
  const ruleH = Math.max(2, Math.round(3 * u));
  const blur = 7 * u * veil;
  const logoP = c.enter(0.04, 0.7);
  const logoBox = logo
    ? fitInside(logo, Math.min(content.width * 0.42, 560 * u), Math.max(64 * u, Math.min(fit.fontSize * 1.25, content.height * 0.16)))
    : null;
  // Over a picture the scrim is lighter: the picture is the backdrop, not the footage.
  const scrim = image ? 0.55 : 1;

  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      {image ? (
        <AbsoluteFill style={{ opacity: veil, transform: `scale(${1.04 - 0.04 * c.enter(0, 1.2)})` }}>
          <ResourceImage pic={image} fit="cover" style={{ width: '100%', height: '100%' }} />
        </AbsoluteFill>
      ) : null}
      <AbsoluteFill
        style={{
          backdropFilter: !image && blur > 0.3 ? `blur(${blur.toFixed(2)}px)` : undefined,
          WebkitBackdropFilter: !image && blur > 0.3 ? `blur(${blur.toFixed(2)}px)` : undefined,
          background: `radial-gradient(120% 90% at 50% 50%, ${rgba(brand.background, 0.38 * veil * scrim)} 0%, ${rgba(
            brand.background,
            0.62 * veil * scrim,
          )} 100%)`,
        }}
      />
      <div
        style={{
          position: 'absolute',
          left: content.left,
          width: content.width,
          top: content.top,
          height: content.height,
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          justifyContent: 'center',
          textAlign: 'center',
          color: brand.text,
          opacity: exit,
          transform: `translateY(${(1 - exit) * -12 * u}px)`,
          textShadow: `0 ${2 * u}px ${18 * u}px ${rgba(brand.background, 0.35)}`,
        }}
      >
        {logo && logoBox ? (
          <div
            style={{
              width: logoBox.width,
              height: logoBox.height,
              marginBottom: Math.round(fit.fontSize * 0.42),
              opacity: logoP,
              transform: `translateY(${(1 - logoP) * 10 * u}px)`,
            }}
          >
            <ResourceImage pic={logo} style={{ width: logoBox.width, height: logoBox.height }} />
          </div>
        ) : null}
        <div>
          {fit.lines.map((ln, i) => (
            <MaskLine key={i} progress={lineP(i)} fontSize={fit.fontSize}>
              <div
                style={{
                  fontFamily: style.fontFamily,
                  fontWeight: style.fontWeight,
                  fontSize: fit.fontSize,
                  lineHeight: tune.lineDisplay,
                  letterSpacing: style.letterSpacing,
                  whiteSpace: 'nowrap',
                }}
              >
                {ln.map((j) => words[j]).join(' ')}
              </div>
            </MaskLine>
          ))}
        </div>
        {subWords.length ? (
          <>
            <div
              style={{
                width: ruleW,
                height: ruleH,
                borderRadius: ruleH,
                margin: `${Math.round(fit.fontSize * 0.34)}px 0 ${Math.round(fit.fontSize * 0.26)}px`,
                background: brand.accent,
                transform: `scaleX(${ruleP})`,
              }}
            />
            <div style={{ opacity: subP, transform: `translateY(${(1 - subP) * 12 * u}px)` }}>
              {sub.lines.map((ln, i) => (
                <div
                  key={i}
                  style={{
                    fontFamily: subStyle.fontFamily,
                    fontWeight: subStyle.fontWeight,
                    fontSize: sub.fontSize,
                    letterSpacing: subStyle.letterSpacing,
                    lineHeight: 1.34,
                    whiteSpace: 'nowrap',
                    color: rgba(brand.text, 0.8),
                  }}
                >
                  {ln.map((j) => subWords[j]).join(' ')}
                </div>
              ))}
            </div>
          </>
        ) : null}
      </div>
    </AbsoluteFill>
  );
};
