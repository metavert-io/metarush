import type { QuoteParams } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { pick } from '../core/layout';
import { makeOverlayClock, ramp, EASE_OUT } from '../core/motion';
import { fitWords, normWord, splitWords, type TextStyle } from '../core/text';
import { SERIF_STACK } from '../theme/fonts';
import { rgba } from '../theme/brand';
import { enumParam, strArrayParam, strParam, trackingFor, typeTune, warnOnce, type TemplateProps } from './shared';

export const parseQuote = (raw: unknown): QuoteParams | null => {
  const text = strParam(raw, 'text', 240);
  if (!text) return null;
  return {
    text,
    attribution: strParam(raw, 'attribution', 80),
    emphasis: strArrayParam(raw, 'emphasis', 8) as QuoteParams['emphasis'],
    motion: enumParam(raw, 'motion', ['restrained', 'none'] as const),
  };
};

/** Strip wrapping straight/curly quotes; we set our own typographic marks. */
const unquote = (t: string) => t.replace(/^["“”'‘’]+/, '').replace(/["“”'‘’]+$/, '').trim();

/** Marks words covered by any emphasis entry (single words or multi-word phrases). */
const emphasisMask = (words: string[], emphasis: string[]): boolean[] => {
  const norm = words.map(normWord);
  const mask = words.map(() => false);
  for (const phrase of emphasis) {
    const parts = splitWords(phrase).map(normWord).filter(Boolean);
    if (!parts.length) continue;
    for (let i = 0; i + parts.length <= norm.length; i++) {
      if (parts.every((p, k) => norm[i + k] === p)) for (let k = 0; k < parts.length; k++) mask[i + k] = true;
    }
  }
  return mask;
};

/**
 * quote.kinetic v1: a pull quote set large and left-aligned over a dimmed
 * frame. Words surface one after another (fade + short rise, no blur, no
 * scale); emphasized words carry the accent. motion "none" is a single fade.
 */
export const QuoteKinetic: React.FC<TemplateProps> = ({ params, durationInFrames, brand, layout }) => {
  const frame = useCurrentFrame();
  const p = parseQuote(params);
  const { unit: u, orientation: o, content, fps } = layout;
  const tune = typeTune(brand.font);

  const words = useMemo(() => splitWords(unquote(p?.text ?? '')), [p?.text]);
  const emph = useMemo(() => emphasisMask(words, (p?.emphasis ?? []) as string[]), [words, p?.emphasis]);

  const maxSize = pick(o, { vertical: 80, landscape: 84, square: 72 }) * u;
  const maxWidth = pick(o, {
    vertical: content.width,
    landscape: Math.min(content.width, 1360 * u),
    square: content.width * 0.94,
  });
  const lineHeight = brand.font === 'Fraunces' ? 1.14 : 1.16;
  const style: TextStyle = useMemo(
    () => ({ fontFamily: brand.fontFamily, fontWeight: brand.font === 'Fraunces' ? 440 : 520, letterSpacing: trackingFor(brand.font, maxSize, u) }),
    [brand.fontFamily, brand.font, maxSize, u],
  );
  const fit = useMemo(
    () =>
      fitWords({
        words,
        style,
        maxWidth,
        maxLines: pick(o, { vertical: 8, landscape: 4, square: 6 }),
        maxHeight: content.height * 0.6,
        lineHeight,
        maxSize,
        minSize: Math.round(34 * u),
      }),
    [words, style, maxWidth, o, content.height, lineHeight, maxSize, u],
  );

  if (!p || words.length === 0) {
    warnOnce('quote.kinetic: missing required param "text"; overlay skipped');
    return null;
  }

  const restrained = p.motion !== 'none';
  const { fontSize, lines } = fit;
  // Reveal window: the whole quote is readable well before the midpoint.
  const usable = Math.max(1, durationInFrames - Math.round(0.42 * fps));
  const revealWindow = Math.min(usable * 0.45, 2.4 * fps);
  const stagger = Math.max(0.035 * fps, Math.min(0.12 * fps, revealWindow / Math.max(1, words.length)));
  const wordDur = Math.round(0.55 * fps);
  const c = makeOverlayClock(frame, durationInFrames, fps, restrained ? 0.5 : 0.6);
  const dim = c.enter(0, 0.5);
  const staticIn = c.enter(0, 0.6);
  const markP = c.enter(0, 0.6);
  const lastStart = Math.round(0.15 * fps) + stagger * (words.length - 1);
  const attribP = restrained ? ramp(frame, lastStart + wordDur * 0.4, Math.round(0.6 * fps), EASE_OUT) : staticIn;
  const exit = c.exit;

  const markSize = Math.round(fontSize * 2.2);
  const attribSize = Math.max(Math.round(28 * u), Math.round(fontSize * 0.4));
  const blockW = Math.max(fit.widest, Math.min(maxWidth, 520 * u));
  const markBox = markSize * 0.4;
  const blockH =
    markBox + fontSize * 0.18 + lines.length * fontSize * lineHeight + (p.attribution ? attribSize * 1.4 + fontSize * 0.62 : 0);
  const top = content.top + Math.max(0, (content.height - blockH) / 2);
  const left = content.left + (content.width - blockW) / 2;

  let wi = 0;
  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <AbsoluteFill style={{ background: rgba(brand.background, 0.64), opacity: dim * exit }} />
      <div
        style={{
          position: 'absolute',
          left,
          top,
          width: blockW,
          opacity: exit * (restrained ? 1 : staticIn),
          transform: `translateY(${(1 - exit) * -8 * u}px)`,
          color: brand.text,
        }}
      >
        <div
          aria-hidden
          style={{
            fontFamily: SERIF_STACK,
            fontWeight: 500,
            fontSize: markSize,
            lineHeight: 1,
            height: markBox,
            color: brand.accent,
            opacity: markP,
            transform: `translateY(${(1 - markP) * 0.06 * markSize}px)`,
            marginLeft: -markSize * 0.03,
          }}
        >
          {'“'}
        </div>
        <div style={{ marginTop: fontSize * 0.18 }}>
          {lines.map((ln, li) => (
            <div
              key={li}
              style={{
                fontFamily: style.fontFamily,
                fontWeight: style.fontWeight,
                fontSize,
                lineHeight,
                letterSpacing: style.letterSpacing,
                whiteSpace: 'nowrap',
              }}
            >
              {ln.map((j, k) => {
                const index = wi++;
                const start = Math.round(0.15 * fps + stagger * index);
                const t = restrained ? ramp(frame, start, wordDur, EASE_OUT) : 1;
                return (
                  <React.Fragment key={j}>
                    {k > 0 ? ' ' : null}
                    <span
                      style={{
                        display: 'inline-block',
                        opacity: t,
                        transform: `translateY(${(1 - t) * 0.22}em)`,
                        color: emph[j] ? brand.accent : undefined,
                      }}
                    >
                      {words[j]}
                    </span>
                  </React.Fragment>
                );
              })}
            </div>
          ))}
        </div>
        {p.attribution ? (
          <div
            style={{
              marginTop: fontSize * 0.62,
              opacity: attribP,
              transform: `translateY(${(1 - attribP) * 8 * u}px)`,
              fontFamily: brand.fontFamily,
              fontWeight: tune.label,
              fontSize: attribSize,
              letterSpacing: '0.01em',
              color: rgba(brand.text, 0.72),
              whiteSpace: 'nowrap',
            }}
          >
            {p.attribution}
          </div>
        ) : null}
      </div>
    </AbsoluteFill>
  );
};
