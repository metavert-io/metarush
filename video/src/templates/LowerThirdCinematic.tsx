import React, { useMemo } from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { pick } from '../core/layout';
import { EASE_IN_OUT, makeOverlayClock, mix } from '../core/motion';
import { fitSingleLine, type TextStyle } from '../core/text';
import { SERIF_STACK } from '../theme/fonts';
import type { LowerThirdParams } from '@metarush/schema';
import type { FrameLayout } from '../core/layout';
import { rgba, type ResolvedBrand } from '../theme/brand';
import { parseLowerThird, STRAP_LOGO_ASPECT } from './LowerThirdClean';
import { ResourceImage, fitInside, usePicture } from './resources';
import { typeTune, warnOnce, type TemplateProps } from './shared';

const NAME_TRACK = 0.2; // em, resting
const TITLE_TRACK = 0.3; // em, resting

/**
 * Layout for lower-third.cinematic, shared with captions (which lift above an
 * on-screen lower third). Pure; needs the bundled fonts loaded. Sizes are fit
 * at the widest tracking the animation reaches, so nothing ever overflows.
 */
export const lowerThirdCinematicMetrics = (p: LowerThirdParams, brand: ResolvedBrand, layout: FrameLayout) => {
  const { unit: u, orientation: o, content } = layout;
  const tune = typeTune(brand.font);
  const nameBase = pick(o, { vertical: 46, landscape: 40, square: 42 }) * u;
  const titleBase = Math.round(pick(o, { vertical: 25, landscape: 21, square: 22 }) * u);
  const maxW = pick(o, { vertical: content.width, landscape: Math.min(content.width, 1200 * u), square: content.width });
  const nameStyle: TextStyle = { fontFamily: SERIF_STACK, fontWeight: 400, letterSpacing: `${NAME_TRACK + 0.14}em`, uppercase: true };
  const titleStyle: TextStyle = {
    fontFamily: brand.fontFamily,
    fontWeight: tune.label,
    letterSpacing: `${TITLE_TRACK + 0.16}em`,
    uppercase: true,
  };
  const nameSize = Math.max(Math.round(nameBase * 0.55), fitSingleLine(p.name, nameStyle, maxW, nameBase));
  const titleSize = p.title ? Math.max(Math.round(16 * u), fitSingleLine(p.title, titleStyle, maxW, titleBase)) : titleBase;
  const gap = Math.round(16 * u);
  const ruleH = Math.max(1, 1.5 * u);
  // A logo (optional) sits above the name; its box is fixed by the type size.
  const logoH = p.logo ? Math.round(nameBase * 1.6) : 0;
  const logoGap = p.logo ? Math.round(gap * 1.25) : 0;
  const blockH = logoH + logoGap + nameSize * 1.15 + (p.title ? gap * 2 + ruleH + titleSize * 1.4 : 0);
  return { nameSize, titleSize, gap, ruleH, blockH, tune, logoH, logoGap };
};

/**
 * lower-third.cinematic v1: letter-spaced serif small caps over a low
 * gradient. The name resolves with a slow soft-edged wipe while its tracking
 * settles inward; a hairline draws, then the title tracks in beneath it.
 */
export const LowerThirdCinematic: React.FC<TemplateProps> = ({ params, durationInFrames, brand, layout }) => {
  const frame = useCurrentFrame();
  const p = parseLowerThird(params);
  const logo = usePicture(p?.logo, 'lower-third.cinematic logo');
  const { unit: u, content } = layout;
  const align = p?.align ?? 'left';
  const m = useMemo(
    () => (p ? lowerThirdCinematicMetrics(p, brand, layout) : null),
    [p?.name, p?.title, p?.logo, brand, layout],
  );

  if (!p || !m) {
    warnOnce('lower-third.cinematic: missing required param "name"; overlay skipped');
    return null;
  }
  const { nameSize, titleSize: titleFitSize, gap, ruleH, blockH, tune, logoH, logoGap } = m;
  const logoBox = logo && logoH > 0 ? fitInside(logo, Math.round(logoH * STRAP_LOGO_ASPECT), logoH) : null;

  const c = makeOverlayClock(frame, durationInFrames, layout.fps, 2.0);
  const wipe = c.enter(0, 1.3, EASE_IN_OUT);
  const nameTrack = mix(NAME_TRACK + 0.14, NAME_TRACK, c.enter(0, 1.8));
  const nameOpacity = c.enter(0, 1.0);
  const rule = c.enter(0.55, 0.9);
  const titleP = c.enter(0.8, 1.2);
  const titleTrack = mix(TITLE_TRACK + 0.16, TITLE_TRACK, titleP);
  const shade = c.enter(0, 1.2);
  const exit = c.exit;
  const exitTrack = (1 - exit) * 0.04;

  // Soft-edged wipe in reading direction (mirrored for right, from the middle for center).
  const edge = 18; // % feather
  let mask: string;
  if (align === 'center') {
    const half = mix(0, 50 + edge, wipe);
    mask = `linear-gradient(to right, transparent ${50 - half - edge}%, #000 ${50 - half}%, #000 ${50 + half}%, transparent ${50 + half + edge}%)`;
  } else {
    const pos = mix(-edge, 100 + edge, wipe);
    mask = `linear-gradient(${align === 'right' ? 'to left' : 'to right'}, #000 ${pos - edge}%, transparent ${pos}%)`;
  }

  const ruleW = Math.round(64 * u);
  const bottomEdge = content.top + content.height;
  const justify = align === 'center' ? 'center' : align === 'right' ? 'flex-end' : 'flex-start';
  // Trailing tracking would push centered/right text off-axis; pull it back.
  const trailing = (em: number) => (align === 'left' ? 0 : -em);

  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <AbsoluteFill
        style={{
          opacity: shade * exit,
          background: `linear-gradient(to top, ${rgba(brand.background, 0.62)} 0%, ${rgba(brand.background, 0.3)} 22%, ${rgba(
            brand.background,
            0,
          )} 48%)`,
        }}
      />
      <div
        style={{
          position: 'absolute',
          left: content.left,
          width: content.width,
          top: bottomEdge - blockH,
          display: 'flex',
          flexDirection: 'column',
          alignItems: justify,
          opacity: exit,
          textShadow: `0 ${Math.max(1, u)}px ${10 * u}px ${rgba(brand.background, 0.45)}`,
        }}
      >
        {logo && logoBox ? (
          <div
            style={{
              height: logoH,
              marginBottom: logoGap,
              display: 'flex',
              alignItems: 'flex-end',
              opacity: nameOpacity,
              WebkitMaskImage: mask,
              maskImage: mask,
            }}
          >
            <ResourceImage pic={logo} style={{ width: logoBox.width, height: logoBox.height }} />
          </div>
        ) : null}
        <div
          style={{
            fontFamily: SERIF_STACK,
            fontWeight: 400,
            fontSize: nameSize,
            lineHeight: 1.15,
            letterSpacing: `${nameTrack + exitTrack}em`,
            marginRight: `${trailing(nameTrack + exitTrack)}em`,
            textTransform: 'uppercase',
            whiteSpace: 'nowrap',
            color: brand.text,
            opacity: nameOpacity,
            WebkitMaskImage: mask,
            maskImage: mask,
          }}
        >
          {p.name}
        </div>
        {p.title ? (
          <>
            <div
              style={{
                width: ruleW,
                height: ruleH,
                margin: `${gap}px 0`,
                background: brand.accent,
                transform: `scaleX(${rule})`,
                transformOrigin: align === 'right' ? 'right' : align === 'center' ? 'center' : 'left',
              }}
            />
            <div
              style={{
                fontFamily: brand.fontFamily,
                fontWeight: tune.label,
                fontSize: titleFitSize,
                lineHeight: 1.4,
                letterSpacing: `${titleTrack}em`,
                marginRight: `${trailing(titleTrack)}em`,
                textTransform: 'uppercase',
                whiteSpace: 'nowrap',
                color: rgba(brand.text, 0.82),
                opacity: titleP,
              }}
            >
              {p.title}
            </div>
          </>
        ) : null}
      </div>
    </AbsoluteFill>
  );
};
