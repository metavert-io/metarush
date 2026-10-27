import type { PlanCaptionLine, PlanCaptions, PlanOverlay } from '@metarush/schema';
import React, { useMemo } from 'react';
import { interpolateColors, useCurrentFrame } from 'remotion';
import { pick, type FrameLayout } from '../core/layout';
import { EASE_IN, EASE_OUT, ramp } from '../core/motion';
import { fitWords, spaceBetween, splitWords, type TextStyle } from '../core/text';
import { readableOn, rgba, type ResolvedBrand } from '../theme/brand';
import lookCatalog from '@metarush/schema/schemas/caption-looks.json';
import { lowerRegionReserve } from '../templates/reserve';
import { typeTune } from '../templates/shared';

type Word = { text: string; from: number; to: number; emphasis: boolean };

/**
 * A caption look (D245): one entry of schema/caption-looks.json, which Go
 * (editspec.CaptionLooks) writes; the quality check measures captions with
 * the same line heights and scales.
 */
export type CaptionLook = {
  id: string;
  name: string;
  wordTimed?: boolean;
  fallback?: string;
  weight: 'display' | 'strong' | 'label' | 'text';
  lineHeight: number;
  scale: number;
  uppercase?: boolean;
  backdrop: 'scrim' | 'box' | 'none';
  outline?: boolean;
  spoken?: '' | 'color' | 'box';
  pop?: boolean;
};

const LOOKS = (lookCatalog as { looks: CaptionLook[] }).looks;

/** A look by its ID; an unknown one is Clean (the first). */
export const captionLook = (id: string | undefined): CaptionLook => LOOKS.find((l) => l.id === id) ?? LOOKS[0]!;

const MAX_LINES = 2;

/** How much larger a caption size draws than medium (Go: editspec.CaptionSizeScale, D199). */
export const captionSizeScale = (size?: string) => (size === 'small' ? 0.82 : size === 'large' ? 1.2 : 1);

/** Caption type size before any shrink-to-fit, per orientation (and the captions' size). */
export const captionBaseSize = (layout: FrameLayout, size?: string) =>
  pick(layout.orientation, { vertical: 64, landscape: 52, square: 58 }) * layout.unit * captionSizeScale(size);

/** Top of a caption block of height `blockH` for a position, clamped inside the content box. */
export const captionTop = (layout: FrameLayout, position: string, blockH: number): number => {
  const { content } = layout;
  const top0 = content.top;
  const bottom0 = content.top + content.height;
  let top: number;
  switch (position) {
    case 'top':
      top = top0;
      break;
    case 'middle':
      top = top0 + content.height / 2 - blockH / 2;
      break;
    case 'bottom':
      top = bottom0 - blockH;
      break;
    default: // lower
      top = top0 + content.height * 0.8 - blockH / 2;
  }
  return Math.max(top0, Math.min(bottom0 - blockH, top));
};

/**
 * The band captions can occupy (worst case: two lines at full size). Center
 * templates are laid out outside it when they share screen time with captions,
 * matching Go's region model (captions lower/bottom = "lower", top = "top").
 */
export const captionZone = (layout: FrameLayout, captions: PlanCaptions): { top: number; bottom: number } => {
  const look = captionLook(captions.style);
  const lh = look.lineHeight;
  const base = captionBaseSize(layout, captions.size) * look.scale;
  const blockH = MAX_LINES * base * lh + (captions.showSpeakers ? speakerLabelHeight(base) : 0);
  const top = captionTop(layout, captions.position, blockH);
  return { top, bottom: top + blockH };
};

/** Speaker name above a caption line (captions.showSpeakers): size and the height it adds. */
const speakerLabelSize = (fontSize: number) => Math.round(fontSize * 0.42);
const speakerLabelHeight = (fontSize: number) => speakerLabelSize(fontSize) * 1.3 + 0.14 * fontSize;

/** Index of the caption line on screen at `frame`, or -1. */
const activeLineIndex = (lines: PlanCaptionLine[], frame: number): number => {
  let found = -1;
  for (let i = 0; i < lines.length; i++) {
    const l = lines[i]!;
    if (l.from > frame) break;
    if (frame >= l.from && frame < l.to) found = i;
  }
  return found;
};

const wordsOf = (line: PlanCaptionLine, wordTimed: boolean): Word[] => {
  if (wordTimed && line.words && line.words.length > 0) {
    return line.words
      .map((w) => ({ text: (w.text ?? '').trim(), from: w.from, to: w.to, emphasis: Boolean(w.emphasis) }))
      .filter((w) => w.text.length > 0);
  }
  // Segment precision (or manual captions): static words, emphasis flags kept if present.
  const flags = new Map<string, boolean>();
  for (const w of line.words ?? []) if (w.emphasis) flags.set(w.text.trim(), true);
  return splitWords(line.text ?? '').map((t) => ({
    text: t,
    from: line.from,
    to: line.to,
    emphasis: flags.get(t) ?? false,
  }));
};

const CaptionLine: React.FC<{
  line: PlanCaptionLine;
  prev: PlanCaptionLine | undefined;
  next: PlanCaptionLine | undefined;
  captions: PlanCaptions;
  overlays: PlanOverlay[] | undefined;
  brand: ResolvedBrand;
  layout: FrameLayout;
}> = ({ line, prev, next, captions, overlays, brand, layout }) => {
  const frame = useCurrentFrame();
  const { unit: u, orientation: o, content, fps } = layout;
  const wordTimed = captions.precision === 'word';
  // A look that shows the spoken word needs word timings (Go falls back too).
  let look = captionLook(captions.style);
  if (look.wordTimed && !wordTimed) look = captionLook(look.fallback);
  const boxed = look.backdrop === 'box';
  const upper = Boolean(captions.uppercase || look.uppercase);
  const tune = typeTune(brand.font);
  const weight = look.weight === 'strong' ? tune.strong : look.weight === 'label' ? tune.label : look.weight === 'text' ? tune.body : tune.display - 20;

  const words = useMemo(() => wordsOf(line, wordTimed), [line, wordTimed]);
  const textStyle: TextStyle = useMemo(
    () => ({
      fontFamily: brand.fontFamily,
      fontWeight: weight,
      letterSpacing: upper ? '0.025em' : '-0.006em',
      uppercase: upper,
    }),
    [brand.fontFamily, weight, upper],
  );

  const lineHeight = look.lineHeight;
  const baseSize = captionBaseSize(layout, captions.size) * look.scale;
  const padX = boxed ? 0.42 : 0;
  const maxWidth = pick(o, {
    vertical: content.width,
    landscape: Math.min(content.width, 1240 * u),
    square: content.width * 0.94,
  });

  const fit = useMemo(
    () =>
      fitWords({
        words: words.map((w) => w.text),
        style: textStyle,
        // Boxed pads each line by padX em on both sides.
        maxWidth: maxWidth - 2 * padX * baseSize,
        maxLines: MAX_LINES,
        lineHeight,
        maxSize: baseSize,
        minSize: Math.round(baseSize * 0.62),
      }),
    [words, textStyle, maxWidth, padX, baseSize, lineHeight],
  );

  // Lower thirds on screen during this line push it up (decided once per line).
  const reserve = useMemo(
    () =>
      captions.position === 'lower' || captions.position === 'bottom'
        ? lowerRegionReserve(overlays, line.from, line.to, brand, layout)
        : 0,
    [captions.position, overlays, line.from, line.to, brand, layout],
  );

  if (words.length === 0) return null;
  const { fontSize, lines } = fit;
  const who = captions.showSpeakers && line.speaker ? line.speaker : '';
  const blockH = lines.length * fontSize * lineHeight + (who ? speakerLabelHeight(fontSize) : 0);

  // Vertical placement inside the content box, clamped so the block never leaves it.
  let top = captionTop(layout, captions.position, blockH);
  if (reserve > 0) {
    // Keep about one line of air so caption and strap read as separate blocks.
    const ceiling = content.top + content.height - reserve - Math.round(Math.max(44 * u, fontSize)) - blockH;
    top = Math.max(content.top, Math.min(top, ceiling));
  }

  // Line-to-line: continuous speech swaps without a dip; after silence, a short rise.
  const local = frame - line.from;
  const dur = line.to - line.from;
  const continuous = prev !== undefined && line.from - prev.to <= 2;
  const handoff = next !== undefined && next.from - line.to <= 2;
  const inFrames = Math.max(2, Math.round(fps * (continuous ? 0.1 : 0.18)));
  const outFrames = Math.max(2, Math.round(fps * 0.13));
  const enter = ramp(local, 0, Math.min(inFrames, Math.max(1, dur - 1)), EASE_OUT);
  const exit = handoff ? 1 : 1 - ramp(local, dur - outFrames, outFrames, EASE_IN);
  const rise = continuous ? 0 : (1 - enter) * 0.14 * fontSize;
  const pop = look.pop && !continuous ? 0.9 + 0.1 * enter : 1;

  // A tight halo defines glyph edges on bright footage; the wide shadow lifts the line.
  const halo = [
    `0 0 ${Math.max(1, 1.5 * u)}px ${rgba(brand.background, 0.6)}`,
    `0 ${Math.max(1, u)}px ${Math.max(1, 3 * u)}px ${rgba(brand.background, 0.45)}`,
    `0 ${Math.max(1, 2 * u)}px ${14 * u}px ${rgba(brand.background, 0.5)}`,
  ];
  // An outline (bold, highlight, subtitle): a ring of hard shadows in the
  // brand's background colour, so no box or shade is needed behind the words.
  const ring = (() => {
    if (!look.outline) return [];
    const w = Math.max(1, fontSize * (look.weight === 'text' ? 0.035 : 0.055));
    const out: string[] = [];
    for (let k = 0; k < 16; k++) {
      const a = (k / 16) * Math.PI * 2;
      out.push(`${(Math.cos(a) * w).toFixed(2)}px ${(Math.sin(a) * w).toFixed(2)}px 0 ${rgba(brand.background, 0.92)}`);
    }
    return out;
  })();
  const shadow = [...ring, ...halo].join(', ');
  const spokenInk = readableOn(brand.accent, [brand.background, brand.text, '#ffffff', '#111111']);
  const highlightFrames = Math.max(1, Math.round(fps * 0.08));

  // The spoken word stays highlighted until the next word starts.
  const activeIdx = (() => {
    if (!wordTimed || !look.spoken) return -1;
    let idx = -1;
    words.forEach((w, i) => {
      if (w.from <= frame) idx = i;
    });
    return idx;
  })();

  const wordColor = (w: Word, i: number): string => {
    if (look.spoken === 'box' && i === activeIdx) return spokenInk;
    if (w.emphasis) return brand.accent;
    if (activeIdx < 0 || look.spoken !== 'color') return brand.text;
    const on = ramp(frame, w.from, highlightFrames, EASE_OUT);
    const nextFrom = words[i + 1]?.from ?? line.to;
    const off = i === activeIdx ? 0 : ramp(frame, nextFrom, highlightFrames * 2, EASE_OUT);
    const a = Math.max(0, on - off);
    return interpolateColors(a, [0, 1], [brand.text, brand.accent]);
  };

  return (
    <div
      data-caption-line=""
      style={{
        position: 'absolute',
        left: content.left + (content.width - maxWidth) / 2,
        width: maxWidth,
        top,
        textAlign: 'center',
        opacity: enter * exit,
        transform: `translateY(${rise}px) scale(${pop})`,
      }}
    >
      {look.backdrop === 'scrim' ? (
        <div
          aria-hidden
          style={{
            // Soft-edged scrim under the whole block: an even tone behind every
            // word with a long falloff, so it reads as shade, not as a box.
            position: 'absolute',
            left: '50%',
            top: '50%',
            width: fit.widest + 0.9 * fontSize,
            height: blockH + 0.1 * fontSize,
            transform: 'translate(-50%, -50%)',
            borderRadius: 0.6 * fontSize,
            background: rgba(brand.background, 0.36),
            // Blurring a flat shape gives a continuous falloff with no visible edge.
            filter: `blur(${(0.55 * fontSize).toFixed(1)}px)`,
          }}
        />
      ) : null}
      {who ? (
        <div
          style={{
            position: 'relative',
            fontFamily: textStyle.fontFamily,
            fontWeight: tune.label,
            fontSize: speakerLabelSize(fontSize),
            lineHeight: 1.3,
            letterSpacing: '0.06em',
            textTransform: 'uppercase',
            color: brand.accent,
            textShadow: boxed ? undefined : shadow,
            marginBottom: 0.14 * fontSize,
          }}
        >
          <span style={boxed ? { background: rgba(brand.background, 0.8), padding: '0.12em 0.45em', borderRadius: '0.3em' } : undefined}>{who}</span>
        </div>
      ) : null}
      {lines.map((ln, li) => (
        <div
          key={li}
          style={{
            position: 'relative',
            whiteSpace: 'nowrap',
            fontFamily: textStyle.fontFamily,
            fontWeight: textStyle.fontWeight,
            fontSize,
            lineHeight,
            letterSpacing: textStyle.letterSpacing,
            textTransform: upper ? 'uppercase' : undefined,
            color: brand.text,
            textShadow: boxed ? undefined : shadow,
          }}
        >
          <span
            style={
              boxed
                ? {
                    background: rgba(brand.background, 0.8),
                    padding: `0.1em ${padX}em 0.12em`,
                    borderRadius: '0.22em',
                    WebkitBoxDecorationBreak: 'clone',
                    boxDecorationBreak: 'clone',
                  }
                : undefined
            }
          >
            {ln.map((wi, k) => {
              const w = words[wi]!;
              return (
                <React.Fragment key={wi}>
                  {k > 0 && spaceBetween(words[ln[k - 1]!]?.text, w.text) ? ' ' : null}
                  <span
                    style={
                      look.spoken === 'box' && wi === activeIdx
                        ? {
                            color: wordColor(w, wi),
                            background: brand.accent,
                            borderRadius: '0.16em',
                            // A spread shadow pads the box without moving the words.
                            boxShadow: `0 0 0 ${(0.12 * fontSize).toFixed(1)}px ${brand.accent}`,
                            textShadow: 'none',
                          }
                        : { color: wordColor(w, wi) }
                    }
                  >
                    {w.text}
                  </span>
                </React.Fragment>
              );
            })}
          </span>
        </div>
      ))}
    </div>
  );
};

export const CaptionLayer: React.FC<{
  captions: PlanCaptions;
  overlays: PlanOverlay[] | undefined;
  brand: ResolvedBrand;
  layout: FrameLayout;
}> = ({ captions, overlays, brand, layout }) => {
  const frame = useCurrentFrame();
  const lines = captions.lines ?? [];
  const idx = activeLineIndex(lines, frame);
  if (idx < 0) return null;
  const line = lines[idx]!;
  return (
    <CaptionLine
      key={idx}
      line={line}
      prev={lines[idx - 1]}
      next={lines[idx + 1]}
      captions={captions}
      overlays={overlays}
      brand={brand}
      layout={layout}
    />
  );
};
