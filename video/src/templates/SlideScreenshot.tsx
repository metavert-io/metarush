import type { templateSlideScreenshot } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import type { TextStyle } from '../core/text';
import { ResourceImage, usePicture, type ResolvedResource } from './resources';
import { enumParam, strParam, typeTune, warnOnce, type TemplateProps } from './shared';
import {
  FigureText,
  SlideBackdrop,
  SlideContent,
  clamp,
  figureEnter,
  fitLine,
  layoutFigureSlide,
  riseStyle,
  slideMotion,
  slidePalette,
  travelProgress,
  type FigureSize,
  type SlidePalette,
} from './slides';

type SlideScreenshotParams = templateSlideScreenshot.SlideScreenshotParams;
type FrameKind = 'browser' | 'phone' | 'none';

export const parseSlideScreenshot = (raw: unknown): SlideScreenshotParams | null => {
  const image = strParam(raw, 'image', 52);
  if (!image) return null;
  const rec = raw && typeof raw === 'object' && !Array.isArray(raw) ? (raw as Record<string, unknown>) : {};
  return {
    image,
    title: strParam(raw, 'title', 80),
    frame: enumParam(raw, 'frame', ['browser', 'phone', 'none'] as const),
    url: strParam(raw, 'url', 120),
    caption: strParam(raw, 'caption', 140),
    scroll: typeof rec.scroll === 'boolean' ? rec.scroll : undefined,
  };
};

/** "https://www.acme.example/docs/?q=1" → "www.acme.example/docs": host and path, no scheme. */
export const shortUrl = (u: string): string =>
  u
    .trim()
    .replace(/^[a-z][a-z0-9+.-]*:\/\//i, '')
    .replace(/^[^/@]*@/, '')
    .replace(/[?#].*$/, '')
    .replace(/\/+$/, '');

/** A tall, narrow picture is a phone screenshot. */
export const isPhoneShaped = (pic: { width?: number; height?: number }) =>
  !!pic.width && !!pic.height && pic.height > 1.6 * pic.width && pic.width <= 600;

// Frame proportions (shares of the frame's width).
const BAR_K = 0.05;
const BEZEL_K = 0.034;
const PHONE_RADIUS_K = 0.155;
/** A phone's screen shape (width / height). */
const PHONE_SCREEN_A = 0.462;

/** How the picture shows in its frame: the frame's size, the window onto the picture and how far it scrolls. */
type ShotSize = FigureSize & {
  /** The window onto the picture (px). */
  viewW: number;
  viewH: number;
  /** The picture's height at the window's width. */
  full: number;
  /** How far the picture scrolls (0: it doesn't). */
  travel: number;
  /** Browser bar height or phone bezel (px). */
  chrome: number;
};

const shotSizer =
  (kind: FrameKind, w: number, h: number, scroll: boolean | undefined, u: number) =>
  (maxW: number, maxH: number): ShotSize | null => {
    if (!(w > 0 && h > 0) || maxW < 2 || maxH < 2) return null;
    const A = w / h;
    const decide = (windowA: number) => {
      const taller = A < windowA * 0.985;
      const muchTaller = A < windowA / 1.3;
      const scrolls = (scroll ?? muchTaller) && taller;
      // A much taller picture that doesn't scroll shows its top, like a page in a window.
      return { scrolls, contentA: scrolls || muchTaller ? windowA : A };
    };
    const done = (width: number, height: number, viewW: number, viewH: number, scrolls: boolean, chrome: number): ShotSize => {
      const full = viewW / A;
      return { width: Math.round(width), height: Math.round(height), viewW, viewH, full, travel: scrolls ? Math.max(0, full - viewH) : 0, chrome, score: viewW * viewH };
    };
    if (kind === 'browser') {
      const barMin = 30 * u;
      const boxA = maxW / Math.max(1, maxH - Math.max(barMin, BAR_K * maxW));
      const { scrolls, contentA } = decide(clamp(boxA, 0.72, 1.6));
      let W = Math.min(maxW, maxH / (1 / contentA + BAR_K));
      if (BAR_K * W < barMin) W = Math.min(maxW, Math.max(1, (maxH - barMin) * contentA));
      const bar = Math.max(barMin, BAR_K * W);
      return done(W, W / contentA + bar, W, W / contentA, scrolls, bar);
    }
    if (kind === 'phone') {
      const taller = A < PHONE_SCREEN_A * 0.985;
      const muchTaller = A < PHONE_SCREEN_A / 1.3;
      const scrolls = (scroll ?? muchTaller) && taller;
      // The device keeps a phone's shape; a wider picture sits in the middle of its screen.
      const screenA = taller ? PHONE_SCREEN_A : Math.min(A, 0.5);
      const kH = (1 - 2 * BEZEL_K) / screenA + 2 * BEZEL_K;
      const Wd = Math.min(maxW, maxH / kH);
      const bezel = BEZEL_K * Wd;
      const Ws = Wd - 2 * bezel;
      return done(Wd, Wd * kH, Ws, Ws / screenA, scrolls, bezel);
    }
    const { scrolls, contentA } = decide(clamp(maxW / maxH, 0.6, 1.6));
    const W = Math.min(maxW, maxH * contentA);
    return done(W, W / contentA, W, W / contentA, scrolls, 0);
  };

/** The picture inside its window, scrolled `y` px; a picture shorter than the window sits in its middle. */
const Viewport: React.FC<{ pic: ResolvedResource; size: ShotSize; y: number; background: string; children?: React.ReactNode; radius?: number }> = ({
  pic,
  size,
  y,
  background,
  children,
  radius,
}) => (
  <div style={{ position: 'relative', width: size.viewW, height: size.viewH, overflow: 'hidden', background, borderRadius: radius }}>
    <div style={{ transform: `translateY(${(size.full < size.viewH ? (size.viewH - size.full) / 2 : -y).toFixed(2)}px)` }}>
      <ResourceImage pic={pic} style={{ width: size.viewW, height: size.full }} />
    </div>
    {children}
  </div>
);

/** A thin overlay scrollbar, shown only while the page moves. */
const ScrollThumb: React.FC<{ size: ShotSize; t: number; u: number }> = ({ size, t, u }) => {
  if (size.travel <= 0) return null;
  const vis = Math.max(0, Math.min(1, t * 10, (1 - t) * 10));
  if (vis <= 0) return null;
  const inset = Math.max(2, Math.round(4 * u));
  const track = size.viewH - inset * 2;
  const thumb = Math.max(Math.round(24 * u), track * (size.viewH / size.full));
  return (
    <div
      style={{
        position: 'absolute',
        right: inset,
        top: inset + (track - thumb) * t,
        width: Math.max(3, Math.round(6 * u)),
        height: thumb,
        borderRadius: 999,
        background: 'rgba(0, 0, 0, 0.42)',
        boxShadow: '0 0 0 1px rgba(255, 255, 255, 0.45)',
        opacity: vis * 0.9,
      }}
    />
  );
};

const BrowserFrame: React.FC<{ pic: ResolvedResource; size: ShotSize; y: number; t: number; address: string; brand: TemplateProps['brand']; pal: SlidePalette; u: number }> = ({
  pic,
  size,
  y,
  t,
  address,
  brand,
  pal,
  u,
}) => {
  const bar = size.chrome;
  const dot = Math.max(3, Math.round(bar * 0.24));
  const pillW = Math.round(Math.min(size.width * 0.5, Math.max(size.width * 0.3, 760 * u)));
  const pillH = Math.round(bar * 0.6);
  const textStyle: TextStyle = { fontFamily: brand.fontFamily, fontWeight: typeTune(brand.font).label - 20, letterSpacing: '0.004em' };
  const base = Math.round(bar * 0.4);
  // Long addresses shrink a little, then end in an ellipsis.
  const fontSize = address ? fitLine(address, textStyle, pillW - pillH * 1.2, base, Math.round(base * 0.7)) : base;
  const chromeFill = pal.dark
    ? `linear-gradient(rgba(255, 255, 255, 0.075), rgba(255, 255, 255, 0.075)), ${brand.background}`
    : 'linear-gradient(#F1F2F4, #ECEDF0)';
  return (
    <div
      style={{
        width: size.width,
        height: size.height,
        borderRadius: Math.round(clamp(size.width * 0.014, 8 * u, 18 * u)),
        overflow: 'hidden',
        background: chromeFill,
        boxShadow: `0 ${Math.round(30 * u)}px ${Math.round(90 * u)}px ${pal.shadow(1.1)}, 0 0 0 1px ${pal.hairline}`,
      }}
    >
      <div
        style={{
          position: 'relative',
          height: bar,
          display: 'flex',
          alignItems: 'center',
          gap: Math.round(dot * 0.72),
          padding: `0 ${Math.round(bar * 0.45)}px`,
          boxSizing: 'border-box',
          borderBottom: `1px solid ${pal.dark ? 'rgba(255, 255, 255, 0.06)' : 'rgba(0, 0, 0, 0.07)'}`,
        }}
      >
        {[0, 1, 2].map((i) => (
          <div key={i} style={{ width: dot, height: dot, borderRadius: dot, background: pal.dark ? 'rgba(255, 255, 255, 0.22)' : 'rgba(0, 0, 0, 0.17)' }} />
        ))}
        <div
          style={{
            position: 'absolute',
            left: (size.width - pillW) / 2,
            top: (bar - pillH) / 2,
            width: pillW,
            height: pillH,
            borderRadius: pillH,
            boxSizing: 'border-box',
            padding: `0 ${Math.round(pillH * 0.6)}px`,
            background: pal.dark ? 'rgba(255, 255, 255, 0.075)' : 'rgba(0, 0, 0, 0.055)',
            color: pal.dark ? 'rgba(255, 255, 255, 0.62)' : 'rgba(0, 0, 0, 0.55)',
            fontFamily: textStyle.fontFamily,
            fontWeight: textStyle.fontWeight,
            letterSpacing: textStyle.letterSpacing,
            fontSize,
            lineHeight: `${pillH}px`,
            textAlign: 'center',
            whiteSpace: 'nowrap',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
          }}
        >
          {address}
        </div>
      </div>
      <Viewport pic={pic} size={size} y={y} background="#FFFFFF">
        <ScrollThumb size={size} t={t} u={u} />
      </Viewport>
    </div>
  );
};

const PhoneFrame: React.FC<{ pic: ResolvedResource; size: ShotSize; y: number; t: number; pal: SlidePalette; u: number }> = ({ pic, size, y, t, pal, u }) => {
  const radius = size.width * PHONE_RADIUS_K;
  const bezel = size.chrome;
  const island = { w: size.viewW * 0.3, h: size.viewW * 0.088 };
  const out = -Math.max(1.5, 2.6 * u);
  const button = (side: 'left' | 'right', top: number, height: number) => (
    <div
      style={{
        position: 'absolute',
        left: side === 'left' ? out : undefined,
        right: side === 'right' ? out : undefined,
        top: size.height * top,
        width: Math.max(2, 3.4 * u),
        height: size.height * height,
        borderRadius: 2 * u,
        background: '#26272C',
      }}
    />
  );
  return (
    <div style={{ position: 'relative', width: size.width, height: size.height }}>
      {button('left', 0.2, 0.055)}
      {button('left', 0.285, 0.085)}
      {button('right', 0.25, 0.11)}
      <div
        style={{
          position: 'absolute',
          inset: 0,
          borderRadius: radius,
          background: '#0B0B0D',
          boxSizing: 'border-box',
          padding: bezel,
          boxShadow: `inset 0 0 0 ${Math.max(1, 1.6 * u).toFixed(2)}px rgba(255, 255, 255, 0.16), 0 ${Math.round(30 * u)}px ${Math.round(80 * u)}px ${pal.shadow(1.15)}, 0 0 0 1px rgba(0, 0, 0, 0.55)`,
        }}
      >
        <Viewport pic={pic} size={size} y={y} background={size.full < size.viewH ? '#000000' : '#FFFFFF'} radius={Math.max(0, radius - bezel)}>
          <div
            style={{
              position: 'absolute',
              left: (size.viewW - island.w) / 2,
              top: size.viewW * 0.03,
              width: island.w,
              height: island.h,
              borderRadius: island.h,
              background: '#000000',
            }}
          />
          <ScrollThumb size={size} t={t} u={u} />
        </Viewport>
      </div>
    </div>
  );
};

/**
 * slide.screenshot v1: a web page snapshot (or any screenshot) in a clean
 * browser window (three dots, the address in a muted pill) or a phone, or on
 * its own with rounded corners, under a heading, with an optional caption. A
 * tall-and-narrow picture gets the phone; a whole-page snapshot scrolls slowly
 * inside its window from top to bottom with eased starts and short holds at
 * both ends (a thin scrollbar shows while it moves). In landscape a phone
 * stands beside the heading.
 */
export const SlideScreenshot: React.FC<TemplateProps> = (props) => {
  const { params, brand, layout } = props;
  const frame = useCurrentFrame();
  const p = parseSlideScreenshot(params);
  const image = usePicture(p?.image, 'slide.screenshot image');
  const { unit: u, orientation: o } = layout;

  const kind: FrameKind = p?.frame ?? (image && isPhoneShaped(image) ? 'phone' : 'browser');
  const fl = useMemo(() => {
    if (!p) return null;
    const w = image?.width && image.height ? image.width : 1600;
    const h = image?.width && image.height ? image.height : 1000;
    return layoutFigureSlide<ShotSize>({
      layout,
      brand,
      title: p.title,
      caption: p.caption,
      sizer: image ? shotSizer(kind, w, h, p.scroll, u) : () => null,
      allowSide: o === 'landscape',
      // A phone stands beside its heading; a browser window only when that shows it far larger.
      sideBias: kind === 'phone' ? 1.15 : kind === 'browser' ? 1.6 : 1.4,
    });
  }, [p?.title, p?.caption, p?.scroll, kind, image, brand, layout]);

  if (!p || !fl) {
    warnOnce('slide.screenshot: missing required param "image"; slide skipped');
    return null;
  }

  const pal = slidePalette(brand);
  const m = slideMotion(frame, props, 1.1);
  const c = m.clock;
  const figP = figureEnter(c);
  const size = fl.size;
  const t = size && size.travel > 0 ? travelProgress(frame, c, 1.05) : 0;
  const y = size ? size.travel * t : 0;
  const address = p.url ?? (image?.pageUrl ? shortUrl(image.pageUrl) : '');

  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <SlideBackdrop brand={brand} layout={layout} opacity={m.backdrop} />
      <SlideContent motion={m}>
        <FigureText fl={fl} pal={pal} clock={c} u={u} />
        {image && size ? (
          <div style={{ position: 'absolute', left: fl.figure.left, top: fl.figure.top, width: size.width, height: size.height, ...riseStyle(figP, 26 * u) }}>
            {kind === 'browser' ? (
              <BrowserFrame pic={image} size={size} y={y} t={t} address={address} brand={brand} pal={pal} u={u} />
            ) : kind === 'phone' ? (
              <PhoneFrame pic={image} size={size} y={y} t={t} pal={pal} u={u} />
            ) : (
              <div
                style={{
                  width: size.width,
                  height: size.height,
                  borderRadius: Math.round(18 * u),
                  overflow: 'hidden',
                  boxShadow: `0 ${Math.round(26 * u)}px ${Math.round(72 * u)}px ${pal.shadow(1)}, 0 0 0 1px ${pal.hairline}`,
                }}
              >
                <Viewport pic={image} size={size} y={y} background="transparent">
                  <ScrollThumb size={size} t={t} u={u} />
                </Viewport>
              </div>
            )}
          </div>
        ) : null}
      </SlideContent>
    </AbsoluteFill>
  );
};
