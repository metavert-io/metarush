import React from 'react';
import { AbsoluteFill } from 'remotion';
import { pick, type Box, type FrameLayout } from '../core/layout';
import { EASE_IN_OUT, EASE_OUT, makeOverlayClock, ramp, type OverlayClock } from '../core/motion';
import { balanceLines, fitWords, lineWidth, measureWidth, splitWords, wrapWords, type TextStyle } from '../core/text';
import { contrastRatio, luminance, rgba, type ResolvedBrand } from '../theme/brand';
import { MaskLine, trackingFor, typeTune, type TemplateProps } from './shared';

/**
 * The slide family's shared chrome (slide.title, .bullets, .code,
 * .architecture, .screenshot, .quote): a full-frame brand background, one type
 * scale, one set of margins inside the platform-safe content box, and one
 * choreography (content rises in, staggered; a short, gentle exit). Slides
 * shown back to back keep one continuous background: only their content
 * changes. Everything is a function of the frame.
 */

export const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

// --- colour --------------------------------------------------------------------

export type SlidePalette = {
  /** The background is dark (light text on it). */
  dark: boolean;
  text: string;
  body: string;
  muted: string;
  /** Fills: rules, markers, the quote mark. */
  accent: string;
  /** The accent when it is set as text (falls back to the text colour when it would not read). */
  accentInk: string;
  hairline: string;
  /** A shadow colour of a given strength (1 = a card's usual shadow). */
  shadow: (k: number) => string;
};

export const slidePalette = (brand: ResolvedBrand): SlidePalette => {
  const dark = luminance(brand.background) < 0.4;
  return {
    dark,
    text: brand.text,
    body: rgba(brand.text, 0.88),
    muted: rgba(brand.text, 0.64),
    accent: brand.accent,
    accentInk: contrastRatio(brand.accent, brand.background) >= 3 ? brand.accent : brand.text,
    hairline: dark ? 'rgba(255, 255, 255, 0.10)' : 'rgba(0, 0, 0, 0.09)',
    shadow: (k) => `rgba(0, 0, 0, ${Math.min(0.9, (dark ? 0.46 : 0.15) * k).toFixed(3)})`,
  };
};

// --- geometry ------------------------------------------------------------------

/** Where slide content goes: the platform-safe content box with a slide's own margins. */
export const slideArea = (layout: FrameLayout): Box => {
  const { content: c, unit: u, orientation: o } = layout;
  const padX = Math.round(pick(o, { landscape: 40, square: 26, vertical: 6 }) * u);
  const padY = Math.round(pick(o, { landscape: 26, square: 22, vertical: 14 }) * u);
  return { left: c.left + padX, top: c.top + padY, width: Math.max(1, c.width - 2 * padX), height: Math.max(1, c.height - 2 * padY) };
};

/** Scale that fits w×h inside maxW×maxH (0 when the picture has no size). */
export const fitScale = (w: number, h: number, maxW: number, maxH: number) => (w > 0 && h > 0 ? Math.max(0, Math.min(maxW / w, maxH / h)) : 0);

// --- motion --------------------------------------------------------------------

export type SlideMotion = {
  clock: OverlayClock;
  /** Opacity of the slide's background. */
  backdrop: number;
  /** Content: 1 until the exit, then eases to 0. */
  exit: number;
  /** Vertical offset (px) of the content as it leaves. */
  exitLift: number;
};

/**
 * A slide's clock. Its background is on from the first frame when it opens
 * the video or follows another full-frame card (back-to-back slides share one
 * background; only the content changes), else it fades in over the footage;
 * it stays to the last frame when another card follows or the video ends,
 * else it leaves with the content.
 */
export const slideMotion = (frame: number, props: TemplateProps, enterSpanSec: number): SlideMotion => {
  const { durationInFrames, from, planDurationInFrames, layout, joins } = props;
  const clock = makeOverlayClock(frame, durationInFrames, layout.fps, enterSpanSec);
  const inFromCard = from <= 0 || !!joins?.before;
  const holds = !!joins?.after || from + durationInFrames >= planDurationInFrames - 1;
  const fadeIn = inFromCard ? 1 : clock.enter(0, 0.4, EASE_IN_OUT);
  return {
    clock,
    backdrop: fadeIn * (holds ? 1 : clock.exit),
    exit: clock.exit,
    exitLift: (1 - clock.exit) * -10 * layout.unit,
  };
};

/**
 * 0 → 1 over the time a slide shows a long picture (code that pans, a whole
 * web page that scrolls): after the entrance settles and a short hold at the
 * top, eased, ending with a short hold at the bottom before the exit.
 */
export const travelProgress = (frame: number, clock: OverlayClock, settleSec: number): number => {
  const { durationInFrames: d, exitFrames, fps } = clock;
  const exitStart = d - exitFrames;
  const settle = Math.min(Math.round(settleSec * fps), Math.round(exitStart * 0.35));
  const hold = Math.round(clamp((exitStart - settle) * 0.14, 0.2 * fps, 1.1 * fps));
  const start = settle + hold;
  const end = exitStart - hold;
  if (end - start < 2) return ramp(frame, settle, Math.max(1, exitStart - settle), EASE_IN_OUT);
  return ramp(frame, start, end - start, EASE_IN_OUT);
};

// --- background ----------------------------------------------------------------

/**
 * The slide background: the brand background lit by a broad, soft glow of the
 * accent from the top corner and a fainter one of the primary colour from the
 * opposite corner, a quiet dot grid that fades out across the frame, and a
 * gentle vignette. Static (identical on every slide and every frame), so slides
 * shown back to back cut between their content, never their backgrounds.
 */
export const SlideBackdrop: React.FC<{ brand: ResolvedBrand; layout: FrameLayout; opacity: number; children?: React.ReactNode }> = ({
  brand,
  layout,
  opacity,
  children,
}) => {
  const { orientation: o, unit: u } = layout;
  const pal = slidePalette(brand);
  if (opacity <= 0) return null;
  const glowAt = pick(o, { landscape: '4% -8%', square: '0% -6%', vertical: '-12% 2%' });
  const glowSize = pick(o, { landscape: '78% 100%', square: '100% 85%', vertical: '140% 58%' });
  const grid = Math.max(8, Math.round(34 * u));
  const dot = Math.max(0.75, 1.25 * u);
  const gridMask = pick(o, {
    landscape: 'radial-gradient(70% 95% at 100% 0%, #000 0%, rgba(0, 0, 0, 0.45) 40%, rgba(0, 0, 0, 0) 78%)',
    square: 'radial-gradient(90% 80% at 100% 0%, #000 0%, rgba(0, 0, 0, 0.45) 40%, rgba(0, 0, 0, 0) 78%)',
    vertical: 'radial-gradient(120% 55% at 100% 0%, #000 0%, rgba(0, 0, 0, 0.45) 40%, rgba(0, 0, 0, 0) 80%)',
  });
  return (
    <AbsoluteFill style={{ opacity, backgroundColor: brand.background }}>
      <AbsoluteFill
        style={{
          backgroundImage: [
            `radial-gradient(${glowSize} at ${glowAt}, ${rgba(brand.accent, pal.dark ? 0.13 : 0.11)}, ${rgba(brand.accent, pal.dark ? 0.045 : 0.035)} 42%, ${rgba(brand.accent, 0)} 74%)`,
            `radial-gradient(70% 75% at 104% 108%, ${rgba(brand.primary, pal.dark ? 0.07 : 0.1)}, ${rgba(brand.primary, 0)} 72%)`,
            `linear-gradient(180deg, ${rgba('#FFFFFF', pal.dark ? 0.012 : 0)}, ${rgba('#000000', pal.dark ? 0.16 : 0.02)})`,
          ].join(', '),
        }}
      />
      <AbsoluteFill
        style={{
          backgroundImage: `radial-gradient(circle, ${rgba(brand.text, pal.dark ? 0.085 : 0.11)} ${dot.toFixed(2)}px, ${rgba(brand.text, 0)} ${(dot + 0.8).toFixed(2)}px)`,
          backgroundSize: `${grid}px ${grid}px`,
          backgroundPosition: `${Math.round(layout.safe.left)}px ${Math.round(layout.safe.top)}px`,
          WebkitMaskImage: gridMask,
          maskImage: gridMask,
        }}
      />
      {children}
      <AbsoluteFill
        style={{
          background: `radial-gradient(135% 120% at 50% 42%, rgba(0, 0, 0, 0) 58%, rgba(0, 0, 0, ${pal.dark ? 0.3 : 0.04}) 100%)`,
        }}
      />
    </AbsoluteFill>
  );
};

/** The layer slide content sits in: it fades and lifts gently as the slide ends. */
export const SlideContent: React.FC<{ motion: SlideMotion; children: React.ReactNode }> = ({ motion, children }) => (
  <AbsoluteFill style={{ opacity: motion.exit, transform: `translateY(${motion.exitLift.toFixed(2)}px)` }}>{children}</AbsoluteFill>
);

// --- type ----------------------------------------------------------------------

/** Lines fitted to a box, ready to draw. */
export type FittedText = {
  words: string[];
  style: TextStyle;
  fontSize: number;
  lineHeight: number;
  lines: number[][];
  widest: number;
  /** Height of the lines (px). */
  height: number;
};

export const fitText = (
  text: string | undefined,
  style: TextStyle,
  opts: { maxWidth: number; maxLines: number; maxHeight?: number; lineHeight: number; maxSize: number; minSize: number; balance?: boolean },
): FittedText | null => {
  const words = splitWords(text ?? '');
  if (!words.length) return null;
  const fit = fitWords({ words, style, ...opts, maxWidth: Math.max(1, opts.maxWidth) });
  return {
    words,
    style,
    fontSize: fit.fontSize,
    lineHeight: opts.lineHeight,
    lines: fit.lines,
    widest: fit.widest,
    height: fit.lines.length * fit.fontSize * opts.lineHeight,
  };
};

/**
 * Several paragraphs (bullet points) at one shared font size: the largest at
 * which each wraps into at most `maxLines` lines of `maxWidth` and the whole
 * block (with `gapEm` between paragraphs) fits `maxHeight`. Falls back to the
 * smallest size, wrapped as needed. A paragraph holding a word that fits the
 * width only well below the usual size (a URL, a path) gets its own smaller
 * size, so the others keep theirs.
 */
export const fitParagraphs = (
  texts: string[],
  style: TextStyle,
  opts: { maxWidth: number; maxLines: number; maxHeight: number; lineHeight: number; gapEm: number; maxSize: number; minSize: number },
): { fontSize: number; items: FittedText[]; height: number } => {
  const paras = texts.map(splitWords).filter((w) => w.length > 0);
  const maxWidth = Math.max(1, opts.maxWidth);
  // Measured at the sizes themselves: variable fonts change shape with size, so widths don't scale linearly.
  const wordsFit = (words: string[], size: number) => words.every((w) => measureWidth(w, style, size) <= maxWidth);
  const usual = Math.floor(Math.max(opts.minSize, opts.maxSize * 0.7));
  const own = paras.map((words) => {
    if (wordsFit(words, usual)) return 0;
    let size = usual;
    while (size > 6 && !wordsFit(words, size)) size = Math.floor(size * 0.94);
    return Math.max(6, size);
  });
  const sizeOf = (i: number, shared: number) => (own[i] ? Math.min(own[i]!, shared) : shared);
  const build = (shared: number) => {
    const items = paras.map((words, i): FittedText => {
      let size = sizeOf(i, shared);
      let wrapped = wrapWords(words, style, size, maxWidth);
      while (!wrapped && size > 6) {
        size = Math.floor(size * 0.92);
        wrapped = wrapWords(words, style, size, maxWidth);
      }
      const lines = balanceLines(words, style, size, maxWidth, wrapped ?? [words.map((_, j) => j)]);
      const widest = Math.max(0, ...lines.map((l) => lineWidth(words, l, style, size)));
      return { words, style, fontSize: size, lineHeight: opts.lineHeight, lines, widest, height: lines.length * size * opts.lineHeight };
    });
    const height = items.reduce((h, it) => h + it.height, 0) + Math.max(0, items.length - 1) * opts.gapEm * shared;
    return { fontSize: shared, items, height };
  };
  const step = Math.max(1, Math.round(opts.maxSize / 48));
  for (let size = Math.round(opts.maxSize); size >= opts.minSize; size -= step) {
    let height = Math.max(0, paras.length - 1) * opts.gapEm * size;
    const fits = paras.every((words, i) => {
      const s = sizeOf(i, size);
      const lines = wrapWords(words, style, s, maxWidth);
      if (!lines || lines.length > opts.maxLines) return false;
      height += lines.length * s * opts.lineHeight;
      return true;
    });
    if (fits && height <= opts.maxHeight) return build(size);
  }
  // Even the smallest size needs more lines: wrap at it.
  return build(Math.round(opts.minSize));
};

/**
 * Largest size (≤ maxSize) at which one line of text fits maxWidth, measured
 * at the size itself (variable fonts change shape with size, so a linear
 * estimate can come out a little wide). Never below minSize.
 */
export const fitLine = (text: string, style: TextStyle, maxWidth: number, maxSize: number, minSize: number): number => {
  let size = Math.floor(maxSize);
  const w = measureWidth(text, style, size);
  if (w <= maxWidth || w === 0) return size;
  size = Math.min(size - 1, Math.floor(size * (maxWidth / w)));
  while (size > minSize && measureWidth(text, style, size) > maxWidth) size = Math.floor(size * 0.97);
  return Math.max(Math.floor(minSize), size);
};

/** A slide heading: a short accent rule above the title. */
export type SlideHeaderFit = { title: FittedText; ruleW: number; ruleH: number; ruleGap: number; height: number };

export const fitHeader = (
  text: string | undefined,
  brand: ResolvedBrand,
  layout: FrameLayout,
  maxWidth: number,
  maxLines: number,
  maxHeight?: number,
): SlideHeaderFit | null => {
  const { unit: u, orientation: o } = layout;
  const tune = typeTune(brand.font);
  const maxSize = pick(o, { landscape: 72, square: 66, vertical: 70 }) * u;
  const style: TextStyle = { fontFamily: brand.fontFamily, fontWeight: tune.display, letterSpacing: trackingFor(brand.font, maxSize, u) };
  const ruleH = Math.max(2, Math.round(4 * u));
  const ruleGap = Math.round(pick(o, { landscape: 26, square: 24, vertical: 26 }) * u);
  const title = fitText(text, style, {
    maxWidth,
    maxLines,
    maxHeight: maxHeight !== undefined ? Math.max(1, maxHeight - ruleH - ruleGap) : undefined,
    lineHeight: 1.08,
    maxSize,
    minSize: Math.round(40 * u),
  });
  if (!title) return null;
  return { title, ruleW: Math.round(44 * u), ruleH, ruleGap, height: ruleH + ruleGap + title.height };
};

/** A caption (body weight, muted), under a picture or beside it. */
export const fitCaption = (
  text: string | undefined,
  brand: ResolvedBrand,
  layout: FrameLayout,
  maxWidth: number,
  maxLines: number,
): FittedText | null => {
  const { unit: u, orientation: o } = layout;
  const tune = typeTune(brand.font);
  const style: TextStyle = { fontFamily: brand.fontFamily, fontWeight: tune.body, letterSpacing: '0.002em' };
  return fitText(text, style, {
    maxWidth,
    maxLines,
    lineHeight: 1.38,
    maxSize: Math.round(pick(o, { landscape: 31, square: 31, vertical: 34 }) * u),
    minSize: Math.round(22 * u),
  });
};

const lineStyle = (t: FittedText): React.CSSProperties => ({
  fontFamily: t.style.fontFamily,
  fontWeight: t.style.fontWeight,
  fontSize: t.fontSize,
  lineHeight: t.lineHeight,
  letterSpacing: t.style.letterSpacing,
  textTransform: t.style.uppercase ? 'uppercase' : undefined,
  whiteSpace: 'nowrap',
});

/** Fitted lines, drawn as measured (never re-wrapped by the browser). */
export const TextLines: React.FC<{ t: FittedText; color: string; align?: 'left' | 'center'; style?: React.CSSProperties }> = ({
  t,
  color,
  align = 'left',
  style,
}) => (
  <div style={{ color, textAlign: align, ...style }}>
    {t.lines.map((ln, i) => (
      <div key={i} style={lineStyle(t)}>
        {ln.map((j) => t.words[j]).join(' ')}
      </div>
    ))}
  </div>
);

/** Fitted lines that rise into place one after another (each from its own baseline). */
export const RisingLines: React.FC<{ t: FittedText; color: string; progress: (line: number) => number; align?: 'left' | 'center' }> = ({
  t,
  color,
  progress,
  align = 'left',
}) => (
  <div style={{ color, textAlign: align }}>
    {t.lines.map((ln, i) => (
      <MaskLine key={i} progress={progress(i)} fontSize={t.fontSize}>
        <div style={lineStyle(t)}>{ln.map((j) => t.words[j]).join(' ')}</div>
      </MaskLine>
    ))}
  </div>
);

/** The heading: its rule draws while the title lines rise. */
export const SlideHeader: React.FC<{
  h: SlideHeaderFit;
  pal: SlidePalette;
  clock: OverlayClock;
  align?: 'left' | 'center';
  delay?: number;
  style?: React.CSSProperties;
}> = ({ h, pal, clock, align = 'left', delay = 0, style }) => {
  const rule = clock.enter(delay, 0.7);
  return (
    <div style={style}>
      <div
        style={{
          width: h.ruleW,
          height: h.ruleH,
          borderRadius: h.ruleH,
          background: pal.accent,
          margin: align === 'center' ? `0 auto ${h.ruleGap}px` : `0 0 ${h.ruleGap}px`,
          transform: `scaleX(${rule.toFixed(4)})`,
          transformOrigin: align === 'center' ? '50% 50%' : '0% 50%',
        }}
      />
      <RisingLines t={h.title} color={pal.text} align={align} progress={(i) => clock.enter(delay + 0.05 + i * 0.08, 0.85)} />
    </div>
  );
};

/** Rise-and-fade for one element (0 → 1). */
export const riseStyle = (p: number, distance: number): React.CSSProperties => ({
  opacity: Math.min(1, p * 1.25),
  transform: `translateY(${((1 - p) * distance).toFixed(2)}px)`,
});

// --- figures (code, diagrams, screenshots) ---------------------------------------

/** A figure's size in a box; `score` says how well it shows there (default: its area). */
export type FigureSize = { width: number; height: number; score?: number };

export type FigureSlideLayout<S extends FigureSize> = {
  mode: 'stack' | 'side';
  header: SlideHeaderFit | null;
  headerAt: { left: number; top: number; width: number };
  caption: FittedText | null;
  captionAt: { left: number; top: number; width: number };
  /** Where the figure goes (its size is the sizer's answer for the chosen box). */
  figure: Box;
  size: S | null;
  align: 'left' | 'center';
};

/**
 * Lays out a slide built around one figure. Stacked: heading on top, the
 * figure centred below it, the caption beneath, centred as one block in the
 * slide area. Side by side (landscape, when it shows the figure clearly
 * better, e.g. a phone or a tall code panel): heading and caption in a column
 * on the left, the figure on the right. `sizer` answers how big the figure is
 * inside a box (null: there's no figure to draw).
 */
export const layoutFigureSlide = <S extends FigureSize>({
  layout,
  brand,
  title,
  caption,
  sizer,
  allowSide,
  sideBias = 1.2,
}: {
  layout: FrameLayout;
  brand: ResolvedBrand;
  title?: string;
  caption?: string;
  sizer: (maxW: number, maxH: number) => S | null;
  allowSide: boolean;
  sideBias?: number;
}): FigureSlideLayout<S> => {
  const { unit: u, orientation: o } = layout;
  const area = slideArea(layout);
  const headGap = Math.round(pick(o, { landscape: 40, square: 36, vertical: 44 }) * u);
  const capGap = Math.round(pick(o, { landscape: 26, square: 24, vertical: 28 }) * u);
  const capMaxW = Math.min(area.width, Math.round(pick(o, { landscape: 1180, square: 860, vertical: 900 }) * u));
  const scoreOf = (s: S | null) => (s ? (s.score ?? s.width * s.height) : 0);

  const header = fitHeader(title, brand, layout, area.width, 2, area.height * 0.3);
  const cap = fitCaption(caption, brand, layout, capMaxW, 2);
  const headH = header ? header.height + headGap : 0;
  const capH = cap ? cap.height + capGap : 0;
  const stackSize = sizer(area.width, Math.max(1, area.height - headH - capH));

  if (allowSide) {
    const colW = Math.round(clamp(area.width * 0.34, 360 * u, 600 * u));
    const gap = Math.round(64 * u);
    const sideSize = sizer(area.width - colW - gap, area.height);
    if (sideSize && scoreOf(sideSize) > scoreOf(stackSize) * sideBias) {
      const sHeader = fitHeader(title, brand, layout, colW, 4, area.height * 0.6);
      const sCap = fitCaption(caption, brand, layout, colW, 6);
      const between = sHeader && sCap ? Math.round(30 * u) : 0;
      const textH = (sHeader?.height ?? 0) + between + (sCap?.height ?? 0);
      // The column is as wide as its text, so a short heading stays close to its figure.
      const textW = Math.min(colW, Math.max(sHeader?.title.widest ?? 0, sCap?.widest ?? 0, sHeader?.ruleW ?? 0));
      const groupW = (textW > 0 ? textW + gap : 0) + sideSize.width;
      const left = area.left + Math.max(0, (area.width - groupW) / 2);
      const textTop = area.top + Math.max(0, (area.height - textH) * 0.46);
      return {
        mode: 'side',
        header: sHeader,
        headerAt: { left, top: textTop, width: textW },
        caption: sCap,
        captionAt: { left, top: textTop + (sHeader?.height ?? 0) + between, width: textW },
        figure: {
          left: left + (textW > 0 ? textW + gap : 0),
          top: area.top + (area.height - sideSize.height) / 2,
          width: sideSize.width,
          height: sideSize.height,
        },
        size: sideSize,
        align: 'left',
      };
    }
  }

  const figW = stackSize?.width ?? 0;
  const figH = stackSize?.height ?? 0;
  const blockH = (header ? header.height : 0) + (header && figH ? headGap : 0) + figH + (cap ? cap.height + (figH || header ? capGap : 0) : 0);
  const top = area.top + Math.max(0, (area.height - blockH) * 0.44);
  const figTop = top + (header ? header.height + (figH ? headGap : 0) : 0);
  return {
    mode: 'stack',
    header,
    headerAt: { left: area.left, top, width: area.width },
    caption: cap,
    captionAt: { left: area.left, top: figTop + figH + (figH || header ? capGap : 0), width: area.width },
    figure: { left: area.left + (area.width - figW) / 2, top: figTop, width: figW, height: figH },
    size: stackSize,
    align: 'center',
  };
};

/** Heading and caption of a figure slide, placed by layoutFigureSlide. */
export const FigureText: React.FC<{
  fl: FigureSlideLayout<FigureSize>;
  pal: SlidePalette;
  clock: OverlayClock;
  u: number;
  captionDelay?: number;
}> = ({ fl, pal, clock, u, captionDelay = 0.5 }) => {
  const capP = clock.enter(captionDelay, 0.75);
  return (
    <>
      {fl.header ? (
        <SlideHeader
          h={fl.header}
          pal={pal}
          clock={clock}
          align={fl.align}
          style={{ position: 'absolute', left: fl.headerAt.left, top: fl.headerAt.top, width: fl.headerAt.width }}
        />
      ) : null}
      {fl.caption ? (
        <div style={{ position: 'absolute', left: fl.captionAt.left, top: fl.captionAt.top, width: fl.captionAt.width, ...riseStyle(capP, 10 * u) }}>
          <TextLines t={fl.caption} color={pal.muted} align={fl.align} />
        </div>
      ) : null}
    </>
  );
};

/** Timing of the figure's own entrance (it rises after the heading). */
export const figureEnter = (clock: OverlayClock) => clock.enter(0.18, 0.9, EASE_OUT);
