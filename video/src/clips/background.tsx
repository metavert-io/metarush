import type { PlanBackground, PlanClip, RenderPlan } from '@metarush/schema';
import React, { useState, type CSSProperties } from 'react';
import { AbsoluteFill, Img, OffthreadVideo, getRemotionEnvironment } from 'remotion';
import { safeColor } from '../theme/brand';
import { framingStyle } from './framing';
import { seeThroughHere } from './seethrough';

/**
 * A clip's background replaced (D510). Go resolved what goes behind the
 * people and the clip's cut-out — the clip's own picture with its background
 * see-through, a WebM with VP9's alpha channel — so the layers are, bottom to
 * top: the clip itself (its sound; blurred when the background is a blur of
 * it), what goes behind (a colour, a picture, another clip), and the
 * cut-out, placed exactly like the clip and played from the same moment
 * (`cutOutTrimBefore`, a fraction of a frame when it starts between the
 * cut's frames), its edges softened (`feather`).
 */

/**
 * Whether this browser shows a video's see-through parts (seethrough.ts:
 * not Safari, nor any browser on an iPhone or iPad); where it can't, the
 * preview plays the clip as recorded (renders draw in Chrome, the review copy
 * shows the new background).
 */
export const seeThroughVideo = (): boolean => getRemotionEnvironment().isRendering || seeThroughHere();

/** The clip's cut-out file, when the plan has its URL (the preview may not yet). */
export const cutOutSrc = (clip: PlanClip, plan: RenderPlan): string | undefined => {
  const bg = clip.background;
  if (!bg?.cutOut || !seeThroughVideo()) return undefined;
  const url = plan.cutOutSources?.[bg.cutOut];
  return typeof url === 'string' && url.length > 0 ? url : undefined;
};

/**
 * The clip's own picture blurred (with blur), drawn a little larger than the
 * frame so the blur's soft rim falls outside it.
 */
export const blurredStyle = (
  bg: PlanBackground,
  clip: PlanClip,
  width: number,
  height: number,
  frame: number,
): CSSProperties | undefined => {
  const blur = Number(bg.blur);
  if (bg.with !== 'blur' || !(blur > 0)) return undefined;
  const m = Math.ceil(2 * blur);
  const s = framingStyle(clip.framing, clip.sourceWidth, clip.sourceHeight, width + 2 * m, height + 2 * m, frame);
  const placed =
    typeof s.left === 'number' && typeof s.top === 'number'
      ? { ...s, left: s.left - m, top: s.top - m }
      : { ...s, transform: `scale(${1 + (2 * m) / Math.max(1, Math.min(width, height))})` };
  return { ...placed, filter: `blur(${blur}px)` };
};

/** What shows behind the people: a colour, a picture, or another clip (a blur is the clip itself). */
export const BehindThePeople: React.FC<{ bg: PlanBackground; plan: RenderPlan; width: number; height: number }> = ({
  bg,
  plan,
  width,
  height,
}) => {
  const [failed, setFailed] = useState(false);
  const rendering = getRemotionEnvironment().isRendering;
  if (bg.with === 'colour') {
    return <AbsoluteFill style={{ backgroundColor: safeColor(bg.colour, '#000000') }} />;
  }
  if (bg.with === 'picture') {
    const url = bg.picture ? plan.resourceSources?.[bg.picture] : undefined;
    return (
      <AbsoluteFill style={{ backgroundColor: '#000000' }}>
        {url && !failed ? (
          <Img
            src={url}
            alt=""
            style={{ width: '100%', height: '100%', objectFit: 'cover', display: 'block' }}
            onError={rendering ? undefined : () => setFailed(true)}
          />
        ) : null}
      </AbsoluteFill>
    );
  }
  if (bg.with === 'footage' && bg.footage) {
    const f = bg.footage;
    const src = plan.sources?.[f.assetId];
    return (
      <AbsoluteFill style={{ backgroundColor: '#000000', overflow: 'hidden' }}>
        {src ? (
          <OffthreadVideo
            src={src}
            muted
            trimBefore={f.trimBefore > 0 ? f.trimBefore : undefined}
            playbackRate={f.playbackRate > 0 ? f.playbackRate : 1}
            pauseWhenBuffering
            style={framingStyle({ mode: 'fill', focusX: 0.5, focusY: 0.5, zoom: 1 }, f.sourceWidth, f.sourceHeight, width, height)}
          />
        ) : null}
      </AbsoluteFill>
    );
  }
  return null;
};

/** An id for the clip's edge filter, unique in the page (nested cuts reuse clip ids). */
export const featherId = (plan: RenderPlan, clip: PlanClip): string =>
  `mr-feather-${(plan.revisionId || plan.specHash || 'plan').replace(/[^A-Za-z0-9_-]/g, '')}-${String(clip.id).replace(/[^A-Za-z0-9_-]/g, '')}`;

/**
 * The SVG filter that softens the people's edges: the cut-out's own alpha
 * blurred, then the cut-out kept only where that blurred alpha is (so the
 * edge fades inward and never shows a dark rim).
 */
export const FeatherFilter: React.FC<{ id: string; feather: number }> = ({ id, feather }) => (
  <svg width={0} height={0} style={{ position: 'absolute' }} aria-hidden>
    <defs>
      <filter id={id} x="-5%" y="-5%" width="110%" height="110%" colorInterpolationFilters="sRGB">
        <feGaussianBlur in="SourceAlpha" stdDeviation={Math.max(0, feather) / 2} result="soft" />
        <feComposite in="SourceGraphic" in2="soft" operator="in" />
      </filter>
    </defs>
  </svg>
);

/** The people: the clip's cut-out, placed and timed exactly like the clip. */
export const CutOut: React.FC<{
  clip: PlanClip;
  plan: RenderPlan;
  src: string;
  width: number;
  height: number;
  frame: number;
  rate: number;
}> = ({ clip, plan, src, width, height, frame, rate }) => {
  const bg = clip.background as PlanBackground;
  const feather = Number(bg.feather) > 0 ? Number(bg.feather) : 0;
  const id = featherId(plan, clip);
  const trim = Number(bg.cutOutTrimBefore);
  return (
    <>
      {feather > 0 ? <FeatherFilter id={id} feather={feather} /> : null}
      <OffthreadVideo
        src={src}
        transparent
        muted
        trimBefore={trim > 0 ? trim : undefined}
        playbackRate={rate}
        pauseWhenBuffering
        style={{
          ...framingStyle(clip.framing, clip.sourceWidth, clip.sourceHeight, width, height, frame),
          filter: feather > 0 ? `url(#${id})` : undefined,
        }}
      />
    </>
  );
};
