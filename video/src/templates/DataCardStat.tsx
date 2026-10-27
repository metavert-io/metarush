import type { DataCardParams } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { pick } from '../core/layout';
import { EASE_OUT, makeOverlayClock } from '../core/motion';
import { fitSingleLine, fitWords, splitWords, type TextStyle } from '../core/text';
import { rgba } from '../theme/brand';
import { enumParam, strParam, trackingFor, typeTune, warnOnce, type TemplateProps } from './shared';

export const parseDataCard = (raw: unknown): DataCardParams | null => {
  const value = strParam(raw, 'value', 16);
  const label = strParam(raw, 'label', 80);
  const source = strParam(raw, 'source', 200);
  if (!value || !label || !source) return null;
  return {
    value,
    label,
    source,
    context: strParam(raw, 'context', 120),
    trend: enumParam(raw, 'trend', ['up', 'down', 'flat'] as const),
  };
};

type NumericValue = {
  prefix: string;
  sign: string;
  number: number;
  decimals: number;
  grouped: boolean;
  suffix: string;
};

/**
 * Parses values like "73%", "$1.2M", "3,400", "+12.5x", "€40k" for count-up.
 * Anything with a second number ("24/7", "1 in 4") or no digits stays static.
 */
export const parseNumeric = (value: string): NumericValue | null => {
  const m = /^([^\d+\-.]*?)([+\-−]?)(\d{1,3}(?:,\d{3})+|\d+)(?:\.(\d+))?([^\d]*)$/.exec(value.trim());
  if (!m) return null;
  const [, prefix = '', sign = '', int = '', frac = '', suffix = ''] = m;
  const number = Number(`${int.replace(/,/g, '')}${frac ? `.${frac}` : ''}`);
  if (!Number.isFinite(number)) return null;
  return { prefix, sign, number, decimals: frac.length, grouped: int.includes(','), suffix };
};

const formatNumeric = (v: NumericValue, n: number): string => {
  const fixed = n.toFixed(v.decimals);
  const [i = '0', f] = fixed.split('.');
  const int = v.grouped ? i.replace(/\B(?=(\d{3})+(?!\d))/g, ',') : i;
  return `${v.prefix}${v.sign}${int}${f !== undefined ? `.${f}` : ''}${v.suffix}`;
};

/** Minimal geometric trend mark (stroke arrow / level bar) in the accent color. */
const TrendMark: React.FC<{ trend: 'up' | 'down' | 'flat'; size: number; color: string; progress: number }> = ({
  trend,
  size,
  color,
  progress,
}) => {
  const sw = Math.max(1.5, size * 0.09);
  const rotate = trend === 'up' ? -45 : trend === 'down' ? 45 : 0;
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      style={{ opacity: progress, transform: `translateY(${(1 - progress) * size * 0.2}px)`, flex: '0 0 auto' }}
      aria-hidden
    >
      <g transform={`rotate(${rotate} 12 12)`} fill="none" stroke={color} strokeWidth={(sw / size) * 24} strokeLinecap="round" strokeLinejoin="round">
        {trend === 'flat' ? (
          <path d="M4 12 H20" />
        ) : (
          <>
            <path d="M4 12 H19" />
            <path d="M13 6 L19 12 L13 18" />
          </>
        )}
      </g>
    </svg>
  );
};

/**
 * datacard.stat v1: a single statistic on a quiet panel. Numeric values count
 * up (tabular figures, width reserved so nothing shifts); the source line is
 * part of the panel from its first frame and never animates separately, so a
 * stat is never on screen without its attribution.
 */
export const DataCardStat: React.FC<TemplateProps> = ({ params, durationInFrames, brand, layout }) => {
  const frame = useCurrentFrame();
  const p = parseDataCard(params);
  const { unit: u, orientation: o, content, fps } = layout;
  const tune = typeTune(brand.font);

  const panelW = pick(o, {
    vertical: content.width,
    landscape: Math.min(content.width, 900 * u),
    square: content.width * 0.88,
  });
  const padX = Math.round(pick(o, { vertical: 60, landscape: 56, square: 56 }) * u);
  const padY = Math.round(pick(o, { vertical: 60, landscape: 52, square: 52 }) * u);
  const innerW = panelW - 2 * padX;

  const valueBase = pick(o, { vertical: 210, landscape: 176, square: 180 }) * u;
  const trendSize = Math.round(valueBase * 0.34);
  const valueStyle: TextStyle = useMemo(
    () => ({
      fontFamily: brand.fontFamily,
      fontWeight: tune.display,
      letterSpacing: trackingFor(brand.font, valueBase, u),
      tabularNums: true,
    }),
    [brand.fontFamily, brand.font, tune, valueBase, u],
  );
  const valueSize = useMemo(
    () =>
      p
        ? Math.max(Math.round(64 * u), fitSingleLine(p.value, valueStyle, innerW - (p.trend ? trendSize * 1.3 : 0), valueBase))
        : valueBase,
    [p?.value, p?.trend, valueStyle, innerW, trendSize, valueBase, u],
  );

  const labelBase = Math.round(pick(o, { vertical: 40, landscape: 34, square: 36 }) * u);
  const labelStyle: TextStyle = useMemo(
    () => ({ fontFamily: brand.fontFamily, fontWeight: tune.label, letterSpacing: '-0.004em' }),
    [brand.fontFamily, tune],
  );
  const labelWords = useMemo(() => splitWords(p?.label ?? ''), [p?.label]);
  const label = useMemo(
    () =>
      fitWords({ words: labelWords, style: labelStyle, maxWidth: innerW, maxLines: 3, lineHeight: 1.24, maxSize: labelBase, minSize: Math.round(26 * u) }),
    [labelWords, labelStyle, innerW, labelBase, u],
  );
  const ctxStyle: TextStyle = useMemo(
    () => ({ fontFamily: brand.fontFamily, fontWeight: tune.body, letterSpacing: '0em' }),
    [brand.fontFamily, tune],
  );
  const ctxWords = useMemo(() => splitWords(p?.context ?? ''), [p?.context]);
  const ctx = useMemo(
    () =>
      fitWords({ words: ctxWords, style: ctxStyle, maxWidth: innerW, maxLines: 3, lineHeight: 1.34, maxSize: Math.round(label.fontSize * 0.8), minSize: Math.round(22 * u) }),
    [ctxWords, ctxStyle, innerW, label.fontSize, u],
  );
  // Source: never truncated, wraps as needed, floor of 22px@1080 for legibility.
  const srcSize = Math.round(pick(o, { vertical: 26, landscape: 22, square: 24 }) * u);
  const srcText = p ? (/^(source|sources|via)\b/i.test(p.source) ? p.source : `Source: ${p.source}`) : '';
  const srcStyle: TextStyle = useMemo(
    () => ({ fontFamily: brand.fontFamily, fontWeight: tune.body + 30, letterSpacing: '0.003em' }),
    [brand.fontFamily, tune],
  );
  const srcWords = useMemo(() => splitWords(srcText), [srcText]);
  const src = useMemo(
    () =>
      fitWords({ words: srcWords, style: srcStyle, maxWidth: innerW, maxLines: 4, lineHeight: 1.36, maxSize: srcSize, minSize: Math.round(20 * u), balance: false }),
    [srcWords, srcStyle, innerW, srcSize, u],
  );

  if (!p) {
    warnOnce('datacard.stat: requires "value", "label" and "source"; overlay skipped');
    return null;
  }

  const c = makeOverlayClock(frame, durationInFrames, fps, 1.4);
  const panel = c.enter(0, 0.6);
  const dim = c.enter(0, 0.6);
  const valueP = c.enter(0.1, 0.6);
  const count = c.enter(0.2, 1.2, EASE_OUT);
  const labelP = c.enter(0.28, 0.7);
  const ctxP = c.enter(0.38, 0.7);
  const trendP = c.enter(0.9, 0.6);
  const exit = c.exit;

  const numeric = parseNumeric(p.value);
  const shown = numeric ? formatNumeric(numeric, numeric.number * count) : p.value;

  const panelH =
    padY * 2 +
    valueSize * 0.98 +
    Math.round(14 * u) +
    label.lines.length * label.fontSize * 1.24 +
    (ctxWords.length ? Math.round(10 * u) + ctx.lines.length * ctx.fontSize * 1.34 : 0) +
    Math.round(30 * u) * 2 +
    1 +
    src.lines.length * src.fontSize * 1.36;
  const left = content.left + (content.width - panelW) / 2;
  const top = content.top + Math.max(0, (content.height - panelH) / 2);

  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <AbsoluteFill style={{ background: rgba(brand.background, 0.3), opacity: dim * exit }} />
      <div
        style={{
          position: 'absolute',
          left,
          top,
          width: panelW,
          boxSizing: 'border-box',
          padding: `${padY}px ${padX}px`,
          borderRadius: Math.round(28 * u),
          background: rgba(brand.background, 0.88),
          boxShadow: `inset 0 0 0 1px ${rgba(brand.text, 0.1)}, 0 ${24 * u}px ${64 * u}px ${rgba(brand.background, 0.35)}`,
          color: brand.text,
          opacity: panel * exit,
          transform: `translateY(${(1 - panel) * 18 * u + (1 - exit) * 8 * u}px)`,
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: Math.round(trendSize * 0.3), opacity: valueP }}>
          <div
            style={{
              position: 'relative',
              fontFamily: valueStyle.fontFamily,
              fontWeight: valueStyle.fontWeight,
              fontSize: valueSize,
              letterSpacing: valueStyle.letterSpacing,
              fontVariantNumeric: 'tabular-nums',
              lineHeight: 0.98,
              whiteSpace: 'nowrap',
              // Optical alignment: large figures carry side bearing; pull them
              // back so the value's edge lines up with the label below.
              marginLeft: '-0.045em',
            }}
          >
            {/* Reserve the final width so the count-up never shifts the layout. */}
            <span style={{ visibility: 'hidden' }}>{p.value}</span>
            <span style={{ position: 'absolute', left: 0, top: 0 }}>{shown}</span>
          </div>
          {p.trend ? <TrendMark trend={p.trend} size={trendSize} color={brand.accent} progress={trendP} /> : null}
        </div>
        <div style={{ marginTop: Math.round(14 * u), opacity: labelP, transform: `translateY(${(1 - labelP) * 10 * u}px)` }}>
          {label.lines.map((ln, i) => (
            <div
              key={i}
              style={{
                fontFamily: labelStyle.fontFamily,
                fontWeight: labelStyle.fontWeight,
                fontSize: label.fontSize,
                letterSpacing: labelStyle.letterSpacing,
                lineHeight: 1.24,
                whiteSpace: 'nowrap',
              }}
            >
              {ln.map((j) => labelWords[j]).join(' ')}
            </div>
          ))}
        </div>
        {ctxWords.length ? (
          <div style={{ marginTop: Math.round(10 * u), opacity: ctxP, color: rgba(brand.text, 0.72) }}>
            {ctx.lines.map((ln, i) => (
              <div
                key={i}
                style={{ fontFamily: ctxStyle.fontFamily, fontWeight: ctxStyle.fontWeight, fontSize: ctx.fontSize, lineHeight: 1.34, whiteSpace: 'nowrap' }}
              >
                {ln.map((j) => ctxWords[j]).join(' ')}
              </div>
            ))}
          </div>
        ) : null}
        <div style={{ height: 1, background: rgba(brand.text, 0.14), margin: `${Math.round(30 * u)}px 0` }} />
        <div style={{ color: rgba(brand.text, 0.74) }}>
          {src.lines.map((ln, i) => (
            <div
              key={i}
              style={{
                fontFamily: srcStyle.fontFamily,
                fontWeight: srcStyle.fontWeight,
                letterSpacing: srcStyle.letterSpacing,
                fontSize: src.fontSize,
                lineHeight: 1.36,
                whiteSpace: 'nowrap',
              }}
            >
              {ln.map((j) => srcWords[j]).join(' ')}
            </div>
          ))}
        </div>
      </div>
    </AbsoluteFill>
  );
};
