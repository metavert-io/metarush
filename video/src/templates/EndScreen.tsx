import type { EndScreenParams } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import type { Box, FrameLayout } from '../core/layout';
import { makeOverlayClock } from '../core/motion';
import { fitWords, splitWords, type TextStyle } from '../core/text';
import { rgba } from '../theme/brand';
import { ResourceImage, fitInside, usePicture } from './resources';
import { MaskLine, enumParam, strParam, trackingFor, typeTune, warnOnce, type TemplateProps } from './shared';

const SLOTS = ['two-videos', 'one-video', 'videos-only'] as const;
type Slots = (typeof SLOTS)[number];

export const parseEndScreen = (raw: unknown): (EndScreenParams & { slots: Slots }) | null => {
  const headline = strParam(raw, 'headline', 80);
  if (!headline) return null;
  const outline = !!raw && typeof raw === 'object' && (raw as Record<string, unknown>).outline === true;
  return {
    headline,
    subline: strParam(raw, 'subline', 120),
    slots: enumParam(raw, 'slots', SLOTS) ?? 'two-videos',
    outline,
    logo: strParam(raw, 'logo', 52),
    image: strParam(raw, 'image', 52),
  };
};

/** Where the words go and the room left for the platform's elements. */
export type EndScreenLayout = { text: Box; videos: Box[]; subscribe: Box | null };

/**
 * end.screen geometry: the words on one side, empty room for the elements a
 * video platform places over a video's last seconds — 16:9 frames for the
 * next videos and a circle for the subscribe button — on the other (stacked
 * below the words on a vertical or square frame). Pure, so the self-test can
 * measure the same boxes.
 */
export const endScreenLayout = (layout: FrameLayout, slots: Slots): EndScreenLayout => {
  const { content: c, orientation: o, unit: u } = layout;
  const videoCount = slots === 'one-video' ? 1 : 2;
  const circle = slots !== 'videos-only';
  const gap = Math.round(36 * u);
  if (o === 'landscape') {
    const textW = Math.round(c.width * 0.4);
    const roomX = c.left + textW + gap * 2;
    const roomW = c.left + c.width - roomX;
    // Videos stacked in the room on the right, as large as the height allows.
    let vw = videoCount === 1 ? roomW : Math.min(roomW, ((c.height - gap) / 2) * (16 / 9));
    let vh = (vw * 9) / 16;
    if (videoCount === 1 && vh > c.height * 0.7) {
      vh = c.height * 0.7;
      vw = (vh * 16) / 9;
    }
    const total = videoCount * vh + (videoCount - 1) * gap;
    const top = c.top + (c.height - total) / 2;
    const videos: Box[] = [];
    for (let i = 0; i < videoCount; i++) videos.push({ left: roomX + (roomW - vw) / 2, top: top + i * (vh + gap), width: vw, height: vh });
    const d = Math.round(Math.min(textW * 0.42, c.height * 0.24));
    const textH = circle ? c.height - d - gap : c.height;
    return {
      text: { left: c.left, top: c.top, width: textW, height: textH },
      videos,
      subscribe: circle ? { left: c.left, top: c.top + c.height - d, width: d, height: d } : null,
    };
  }
  // Vertical and square: the words on top, the room below.
  const textH = Math.round(c.height * (o === 'vertical' ? 0.34 : 0.3));
  const roomTop = c.top + textH + gap;
  const roomH = c.top + c.height - roomTop;
  const d = circle ? Math.round(Math.min(c.width * 0.22, roomH * 0.22)) : 0;
  const forVideos = roomH - (circle ? d + gap : 0);
  let vw = Math.min(c.width * (o === 'vertical' ? 0.9 : 0.62), ((forVideos - (videoCount - 1) * gap) / videoCount) * (16 / 9));
  if (videoCount === 1) vw = Math.min(c.width * 0.9, forVideos * (16 / 9));
  const vh = (vw * 9) / 16;
  const videos: Box[] = [];
  for (let i = 0; i < videoCount; i++) videos.push({ left: c.left + (c.width - vw) / 2, top: roomTop + i * (vh + gap), width: vw, height: vh });
  const below = roomTop + videoCount * vh + (videoCount - 1) * gap + gap;
  return {
    text: { left: c.left, top: c.top, width: c.width, height: textH },
    videos,
    subscribe: circle ? { left: c.left + (c.width - d) / 2, top: Math.min(below, c.top + c.height - d), width: d, height: d } : null,
  };
};

/**
 * end.screen v1 (D263): a full brand-background end screen for a video's last
 * 5–20 seconds. The headline (and a line under it) and the logo sit on one
 * side; the rest is left empty for the platform's own elements, the next
 * videos and the subscribe button, which it places over the video. A faint
 * panel marks each place (the platform's elements cover it), with a dashed
 * outline when `outline` asks for a guide. Optional `image` behind, under a
 * brand-coloured veil. When the screen ends with the video the background
 * holds through the exit.
 */
export const EndScreen: React.FC<TemplateProps> = ({ params, durationInFrames, from, planDurationInFrames, brand, layout }) => {
  const frame = useCurrentFrame();
  const p = parseEndScreen(params);
  const logo = usePicture(p?.logo, 'end.screen logo');
  const image = usePicture(p?.image, 'end.screen image');
  const { unit: u, fps } = layout;
  const tune = typeTune(brand.font);
  const geo = useMemo(() => endScreenLayout(layout, p?.slots ?? 'two-videos'), [layout, p?.slots]);
  const maxSize = Math.round((layout.orientation === 'landscape' ? 92 : 84) * u);
  const headStyle: TextStyle = useMemo(
    () => ({ fontFamily: brand.fontFamily, fontWeight: tune.display, letterSpacing: trackingFor(brand.font, maxSize, u) }),
    [brand.fontFamily, brand.font, tune, maxSize, u],
  );
  const headWords = useMemo(() => splitWords(p?.headline ?? ''), [p?.headline]);
  const logoH = logo ? Math.max(48 * u, Math.min(geo.text.height * 0.16, 140 * u)) : 0;
  const head = useMemo(
    () =>
      fitWords({
        words: headWords,
        style: headStyle,
        maxWidth: geo.text.width,
        maxLines: 3,
        maxHeight: geo.text.height * 0.5,
        lineHeight: tune.lineDisplay,
        maxSize,
        minSize: Math.round(36 * u),
      }),
    [headWords, headStyle, geo.text.width, geo.text.height, tune.lineDisplay, maxSize, u],
  );
  const subStyle: TextStyle = useMemo(() => ({ fontFamily: brand.fontFamily, fontWeight: tune.body, letterSpacing: '0em' }), [brand.fontFamily, tune]);
  const subWords = useMemo(() => splitWords(p?.subline ?? ''), [p?.subline]);
  const sub = useMemo(
    () =>
      fitWords({
        words: subWords,
        style: subStyle,
        maxWidth: geo.text.width,
        maxLines: 3,
        lineHeight: 1.36,
        maxSize: Math.max(Math.round(26 * u), Math.round(head.fontSize * 0.36)),
        minSize: Math.round(22 * u),
      }),
    [subWords, subStyle, geo.text.width, head.fontSize, u],
  );

  if (!p) {
    warnOnce('end.screen: missing required param "headline"; overlay skipped');
    return null;
  }

  const c = makeOverlayClock(frame, durationInFrames, fps, 1.2);
  const bg = c.enter(0, 0.45);
  const lineP = (i: number) => c.enter(0.12 + i * 0.07, 0.8);
  const subP = c.enter(0.4, 0.7);
  const roomP = c.enter(0.5, 0.7);
  const logoP = c.enter(0.05, 0.6);
  const exit = c.exit;
  const endsWithVideo = from + durationInFrames >= planDurationInFrames - 1;
  const bgOpacity = bg * (endsWithVideo ? 1 : exit);
  const logoBox = logo ? fitInside(logo, geo.text.width * 0.6, logoH) : null;
  const vertical = layout.orientation !== 'landscape';
  const panel = rgba(brand.primary, 0.07);
  const line = rgba(brand.text, 0.42);
  const lineW = Math.max(2, Math.round(3 * u));
  const radius = Math.round(18 * u);

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
          backgroundColor: image ? rgba(brand.background, 0.78) : brand.background,
          backgroundImage: `radial-gradient(110% 80% at 100% 100%, ${rgba(brand.accent, 0.08)}, ${rgba(brand.accent, 0)} 60%)`,
        }}
      />
      {[...geo.videos, ...(geo.subscribe ? [geo.subscribe] : [])].map((b, i) => {
        const round = geo.subscribe && i === geo.videos.length;
        return (
          <div
            key={`slot-${i}`}
            data-end-screen-slot={round ? 'subscribe' : 'video'}
            style={{
              position: 'absolute',
              left: b.left,
              top: b.top,
              width: b.width,
              height: b.height,
              borderRadius: round ? '50%' : radius,
              backgroundColor: panel,
              border: p.outline ? `${lineW}px dashed ${line}` : undefined,
              boxSizing: 'border-box',
              opacity: roomP * exit,
            }}
          />
        );
      })}
      <div
        style={{
          position: 'absolute',
          left: geo.text.left,
          top: geo.text.top,
          width: geo.text.width,
          height: geo.text.height,
          display: 'flex',
          flexDirection: 'column',
          justifyContent: vertical ? 'flex-end' : 'center',
          alignItems: vertical ? 'center' : 'flex-start',
          textAlign: vertical ? 'center' : 'left',
          color: brand.text,
          opacity: exit,
        }}
      >
        {logo && logoBox ? (
          <div style={{ width: logoBox.width, height: logoBox.height, marginBottom: Math.round(head.fontSize * 0.45), opacity: logoP }}>
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
          <div style={{ marginTop: Math.round(head.fontSize * 0.4), opacity: subP }}>
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
      </div>
    </AbsoluteFill>
  );
};
