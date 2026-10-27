import type { LowerThirdParams } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { pick } from '../core/layout';
import { makeOverlayClock } from '../core/motion';
import { fitSingleLine, fitWords, splitWords, type TextStyle } from '../core/text';
import type { FrameLayout } from '../core/layout';
import { rgba, type ResolvedBrand } from '../theme/brand';
import { ResourceImage, fitInside, usePicture } from './resources';
import { enumParam, MaskLine, strParam, trackingFor, typeTune, warnOnce, type TemplateProps } from './shared';

export const parseLowerThird = (raw: unknown): LowerThirdParams | null => {
  const name = strParam(raw, 'name', 60);
  if (!name) return null;
  return {
    name,
    title: strParam(raw, 'title', 80),
    align: enumParam(raw, 'align', ['left', 'center', 'right'] as const),
    logo: strParam(raw, 'logo', 52),
  };
};

/** How wide a strap's logo box may be, as a multiple of its height. */
export const STRAP_LOGO_ASPECT = 2.5;

/**
 * Layout for lower-third.clean, shared by the component and by captions (which
 * lift above an on-screen lower third). Pure; needs the bundled fonts loaded.
 */
export const lowerThirdCleanMetrics = (p: LowerThirdParams, brand: ResolvedBrand, layout: FrameLayout) => {
  const { unit: u, orientation: o, content } = layout;
  const tune = typeTune(brand.font);
  const align = p.align ?? 'left';
  const hairW = Math.max(2, Math.round(4 * u));
  const hairGap = Math.round(22 * u);
  const centerRule = align === 'center' ? hairW + Math.round(18 * u) : 0;
  const nameBase = pick(o, { vertical: 58, landscape: 48, square: 52 }) * u;
  // A logo (optional) starts the strap: beside it, or above it when centered.
  // Its box is fixed by the type size so captions can reserve the same space.
  const logoH = p.logo ? Math.round(nameBase * 1.5) : 0;
  const logoMaxW = Math.round(logoH * STRAP_LOGO_ASPECT);
  const logoGap = p.logo ? Math.round(18 * u) : 0;
  const textMax =
    pick(o, { vertical: content.width, landscape: Math.min(content.width, 1100 * u), square: content.width }) -
    (align === 'center' ? 0 : hairW + hairGap + (p.logo ? logoMaxW + logoGap : 0));
  const nameStyle: TextStyle = {
    fontFamily: brand.fontFamily,
    fontWeight: tune.display - 20,
    letterSpacing: trackingFor(brand.font, nameBase, u),
  };
  const titleStyle: TextStyle = { fontFamily: brand.fontFamily, fontWeight: tune.body, letterSpacing: '0.004em' };
  const nameSize = Math.max(Math.round(nameBase * 0.6), fitSingleLine(p.name, nameStyle, textMax, nameBase));
  const titleWords = splitWords(p.title ?? '');
  const titleFit = fitWords({
    words: titleWords,
    style: titleStyle,
    maxWidth: textMax,
    maxLines: 2,
    lineHeight: 1.3,
    maxSize: Math.round(nameBase * 0.6),
    minSize: Math.round(24 * u),
  });
  const titleGap = Math.round(10 * u);
  const textH = nameSize * 1.12 + (titleWords.length ? titleGap + titleFit.lines.length * titleFit.fontSize * 1.3 : 0);
  const blockH = centerRule + textH + (align === 'center' && p.logo ? logoH + logoGap : 0);
  return {
    align,
    hairW,
    hairGap,
    centerRule,
    nameStyle,
    titleStyle,
    nameSize,
    titleWords,
    titleFit,
    titleGap,
    blockH,
    logoH: align === 'center' ? logoH : Math.min(logoH, Math.round(textH)),
    logoMaxW,
    logoGap,
  };
};

/**
 * lower-third.clean v1: name + title beside a hairline accent that draws
 * downward while the lines rise into place. A soft corner scrim keeps it
 * legible over bright footage without boxing it in.
 */
export const LowerThirdClean: React.FC<TemplateProps> = ({ params, durationInFrames, brand, layout }) => {
  const frame = useCurrentFrame();
  const p = parseLowerThird(params);
  const logo = usePicture(p?.logo, 'lower-third.clean logo');
  const { unit: u, content } = layout;
  const m = useMemo(
    () => (p ? lowerThirdCleanMetrics(p, brand, layout) : null),
    [p?.name, p?.title, p?.align, p?.logo, brand, layout],
  );

  if (!p || !m) {
    warnOnce('lower-third.clean: missing required param "name"; overlay skipped');
    return null;
  }
  const { align, hairW, hairGap, nameStyle, titleStyle, nameSize, titleWords, titleFit, titleGap, blockH, logoH, logoMaxW, logoGap } = m;
  const logoBox = logo && logoH > 0 ? fitInside(logo, logoMaxW, logoH) : null;

  const c = makeOverlayClock(frame, durationInFrames, layout.fps, 1.0);
  const hair = c.enter(0, 0.55);
  const nameP = c.enter(0.08, 0.75);
  const titleP = c.enter(0.2, 0.75);
  const scrimP = c.enter(0, 0.6);
  const exit = c.exit;

  const bottomEdge = content.top + content.height;
  const scrimOrigin = align === 'right' ? '100% 100%' : align === 'center' ? '50% 100%' : '0% 100%';

  const logoView =
    logo && logoBox ? (
      <div
        style={{
          flex: '0 0 auto',
          alignSelf: 'center',
          width: logoBox.width,
          height: logoBox.height,
          margin: align === 'center' ? `0 auto ${logoGap}px` : undefined,
          opacity: nameP,
          transform: `translateY(${(1 - nameP) * 8 * u}px)`,
        }}
      >
        <ResourceImage pic={logo} style={{ width: logoBox.width, height: logoBox.height }} />
      </div>
    ) : null;

  const textBlock = (
    <div style={{ textAlign: align, minWidth: 0 }}>
      {align === 'center' ? logoView : null}
      {align === 'center' ? (
        <div
          style={{
            width: Math.round(44 * u),
            height: hairW,
            margin: `0 auto ${Math.round(18 * u)}px`,
            background: brand.accent,
            borderRadius: hairW,
            transform: `scaleX(${hair})`,
          }}
        />
      ) : null}
      <MaskLine progress={nameP} fontSize={nameSize}>
        <div
          style={{
            fontFamily: nameStyle.fontFamily,
            fontWeight: nameStyle.fontWeight,
            fontSize: nameSize,
            letterSpacing: nameStyle.letterSpacing,
            lineHeight: 1.12,
            whiteSpace: 'nowrap',
            color: brand.text,
          }}
        >
          {p.name}
        </div>
      </MaskLine>
      {titleWords.length ? (
        <div style={{ marginTop: titleGap }}>
          {titleFit.lines.map((ln, i) => (
            <MaskLine key={i} progress={titleP} fontSize={titleFit.fontSize}>
              <div
                style={{
                  fontFamily: titleStyle.fontFamily,
                  fontWeight: titleStyle.fontWeight,
                  fontSize: titleFit.fontSize,
                  letterSpacing: titleStyle.letterSpacing,
                  lineHeight: 1.3,
                  whiteSpace: 'nowrap',
                  color: rgba(brand.text, 0.8),
                }}
              >
                {ln.map((j) => titleWords[j]).join(' ')}
              </div>
            </MaskLine>
          ))}
        </div>
      ) : null}
    </div>
  );

  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <AbsoluteFill
        style={{
          opacity: scrimP * exit,
          background: `radial-gradient(${align === 'center' ? '80% 55%' : '75% 50%'} at ${scrimOrigin}, ${rgba(
            brand.background,
            0.55,
          )}, ${rgba(brand.background, 0.25)} 45%, ${rgba(brand.background, 0)} 100%)`,
        }}
      />
      <div
        style={{
          position: 'absolute',
          left: content.left,
          width: content.width,
          top: bottomEdge - blockH,
          display: 'flex',
          flexDirection: align === 'right' ? 'row-reverse' : 'row',
          justifyContent: align === 'center' ? 'center' : 'flex-start',
          alignItems: 'stretch',
          gap: align === 'center' ? 0 : hairGap,
          opacity: exit,
          transform: `translateY(${(1 - exit) * 10 * u}px)`,
          textShadow: `0 ${Math.max(1, u)}px ${8 * u}px ${rgba(brand.background, 0.35)}`,
        }}
      >
        {align !== 'center' && logoView ? <div style={{ display: 'flex', marginRight: align === 'right' ? 0 : logoGap - hairGap, marginLeft: align === 'right' ? logoGap - hairGap : 0 }}>{logoView}</div> : null}
        {align !== 'center' ? (
          <div
            style={{
              flex: '0 0 auto',
              width: hairW,
              margin: `${Math.round(nameSize * 0.12)}px 0 ${Math.round(4 * u)}px`,
              background: brand.accent,
              borderRadius: hairW,
              transform: `scaleY(${hair})`,
              transformOrigin: 'top',
            }}
          />
        ) : null}
        {textBlock}
      </div>
    </AbsoluteFill>
  );
};
