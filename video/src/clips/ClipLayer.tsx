import type { NestedPlan, PlanClip, RenderPlan } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, OffthreadVideo, Sequence, useCurrentFrame, useVideoConfig } from 'remotion';
import { CaptionLayer } from '../captions/CaptionLayer';
import { makeLayout } from '../core/layout';
import { EASE_IN_OUT, ramp } from '../core/motion';
import { resolveBrand, safeColor, withFontFile } from '../theme/brand';
import { useBundledFonts } from '../theme/fonts';
import { OverlayLayer } from '../templates/registry';
import { ResourcesProvider, useBrandFontFile } from '../templates/resources';
import { framingStyle } from './framing';
import { envelopeGain } from '../audio/AudioLayer';
import { TransitionFrame, easingFor, isDip, isMoving, isOverlap } from './transitions';
import { ConversationPicture } from './Conversation';

/**
 * Transition semantics (Go resolved all timing; we only ramp and move):
 * - cut:          no ramp.
 * - fade:         incoming fades in from plan.background over transitionIn.durationInFrames.
 * - crossfade:    (shown as "Dissolve") Go already overlapped the clips (incoming `from` is
 *                 earlier). The incoming clip sits above and ramps opacity 0→1 while the
 *                 outgoing keeps playing beneath.
 * - dip-to-black / dip-to-white: Go set the previous clip's fadeOutFrames (≈ half the dip).
 *                 The incoming clip fades in over the remaining half; a black (or white) matte
 *                 sits under both so the dip reads in that color whatever plan.background is.
 * - wipe, slide:  overlapped like a dissolve; the incoming clip is revealed by a moving edge
 *                 or slides in over the outgoing one, which stays still.
 * - push, whip:   both move: the outgoing clip carries a transitionOut with the same frames.
 * - zoom:         the incoming clip settles from close up while it fades in; the outgoing one
 *                 zooms in (transitionOut).
 * The sound is in the clip's gain envelope (Go). When a clip's sound has its own window
 * (`clip.audio`: a split edit or a blend at a cut), its picture plays muted here and the
 * audio layer plays its sound over that window.
 */
export const dipFadeInFrames = (clip: PlanClip): number => {
  const d = Math.max(0, Math.round(clip.transitionIn?.durationInFrames ?? 0));
  return d - Math.floor(d / 2);
};

const transitionType = (clip: PlanClip | undefined) => clip?.transitionIn?.type ?? 'cut';

/** Frames the picture fades in over (moving transitions never fade). */
const inFrames = (clip: PlanClip): number => {
  const t = transitionType(clip);
  if (t === 'cut' || isMoving(t)) return 0;
  if (isDip(t)) return dipFadeInFrames(clip);
  return Math.max(0, Math.round(clip.transitionIn?.durationInFrames ?? 0));
};

/** Frames the sound fades in over (legacy fallback for plans without envelopes). */
const soundInFrames = (clip: PlanClip): number => {
  const t = transitionType(clip);
  if (t === 'cut') return 0;
  if (isDip(t)) return dipFadeInFrames(clip);
  return Math.max(0, Math.round(clip.transitionIn?.durationInFrames ?? 0));
};

/** Visual opacity at clip-relative frame `f`. */
const clipOpacity = (clip: PlanClip, f: number): number => {
  const fadeIn = inFrames(clip);
  const inV = fadeIn > 0 ? ramp(f, 0, fadeIn, easingFor(clip.transitionIn?.easing, EASE_IN_OUT)) : 1;
  const out = Math.max(0, Math.round(clip.fadeOutFrames ?? 0));
  const outV = out > 0 ? 1 - ramp(f, clip.durationInFrames - out, out, EASE_IN_OUT) : 1;
  return Math.min(inV, outV);
};

const equalPowerIn = (p: number) => Math.sin((Math.max(0, Math.min(1, p)) * Math.PI) / 2);

/** Linear gain at clip-relative frame `f` (volume × transitions × overlap-out); fallback without an envelope. */
const clipGain = (clip: PlanClip, next: PlanClip | undefined, f: number): number => {
  const volume = Number.isFinite(clip.volume) ? Math.max(0, clip.volume) : 1;
  let g = volume;
  const t = transitionType(clip);
  const fadeIn = soundInFrames(clip);
  if (fadeIn > 0) {
    const p = Math.min(1, Math.max(0, f / fadeIn));
    g *= isOverlap(t) ? equalPowerIn(p) : p;
  }
  const out = Math.max(0, Math.round(clip.fadeOutFrames ?? 0));
  if (out > 0) {
    const p = Math.min(1, Math.max(0, (clip.durationInFrames - f) / out));
    g *= p;
  }
  if (next && isOverlap(transitionType(next))) {
    const d = Math.max(0, Math.round(next.transitionIn?.durationInFrames ?? 0));
    const start = next.from - clip.from;
    if (d > 0 && f >= start) {
      const p = Math.min(1, (f - start) / d);
      g *= equalPowerIn(1 - p);
    }
  }
  return g;
};

/**
 * A cut played inside this one (D84): its own resolved plan, drawn at this
 * size from the clip's start frame in that cut (`trimBefore`). Its sound is
 * already in this plan's audio list (Go flattened it with the mix), so its
 * picture plays silent. Its overlays, captions (unless this cut has its own),
 * brand and resources are its own.
 */
const NestedPicture: React.FC<{ nested: NestedPlan }> = ({ nested }) => {
  const inner = nested.plan;
  const fontsReady = useBundledFonts();
  const fontFile = useBrandFontFile(inner);
  const { width, height, fps } = useVideoConfig();
  const brand = useMemo(() => withFontFile(resolveBrand(inner?.brand), fontFile), [inner?.brand, fontFile]);
  const layout = useMemo(() => makeLayout(width, height, fps, inner?.safeArea), [width, height, fps, inner?.safeArea]);
  if (!inner) return null;
  return (
    <ResourcesProvider plan={inner}>
      <AbsoluteFill style={{ backgroundColor: safeColor(inner.background, '#000000'), fontFamily: brand.fontFamily }}>
        <ClipLayer plan={inner} silent />
        {fontsReady ? <OverlayLayer plan={inner} brand={brand} layout={layout} layer="base" /> : null}
        {fontsReady && inner.captions ? (
          <CaptionLayer captions={inner.captions} overlays={inner.overlays} brand={brand} layout={layout} />
        ) : null}
        {fontsReady ? <OverlayLayer plan={inner} brand={brand} layout={layout} layer="full" /> : null}
      </AbsoluteFill>
    </ResourcesProvider>
  );
};

const ClipView: React.FC<{
  clip: PlanClip;
  next: PlanClip | undefined;
  src: string | undefined;
  background: string;
  silent?: boolean;
  nested?: NestedPlan;
  plan: RenderPlan;
}> = ({ clip, next, src, background, silent, nested, plan }) => {
  const frame = useCurrentFrame();
  const { width, height } = useVideoConfig();
  const opacity = clipOpacity(clip, frame);
  if (clip.conversation) {
    // A conversation (D99): its shots; the sound is the members' own tracks in the audio layer.
    return (
      <TransitionFrame clip={clip} frame={frame} unit={Math.min(width, height) / 1080}>
        <AbsoluteFill style={{ opacity, overflow: 'hidden', backgroundColor: background }}>
          <ConversationPicture clip={clip} plan={plan} />
        </AbsoluteFill>
      </TransitionFrame>
    );
  }
  const rate = Number.isFinite(clip.playbackRate) && clip.playbackRate > 0 ? clip.playbackRate : 1;
  // A clip whose sound has its own window is heard through the audio layer
  // (and so is everything inside a nested cut).
  const muted = Boolean(silent) || clip.muted || !clip.hasAudio || Boolean(clip.audio);
  // Gain is computed from our own clip-relative frame and passed as a number,
  // so it does not depend on how Remotion offsets volume-callback frames. Go's
  // envelope (mix, ducking, master) wins; clipGain is the legacy fallback.
  const gain = muted ? 0 : Math.max(0, envelopeGain(clip.gain, frame) ?? clipGain(clip, next, frame));
  if (nested) {
    return (
      <TransitionFrame clip={clip} frame={frame} unit={Math.min(width, height) / 1080}>
        <AbsoluteFill style={{ opacity, overflow: 'hidden', backgroundColor: background }}>
          <Sequence from={-Math.max(0, Math.round(clip.trimBefore))} layout="none" name="nested cut">
            <NestedPicture nested={nested} />
          </Sequence>
        </AbsoluteFill>
      </TransitionFrame>
    );
  }
  if (!src) return null;
  return (
    <TransitionFrame clip={clip} frame={frame} unit={Math.min(width, height) / 1080}>
      {/* The layer is opaque (plan.background behind the picture) so a fit-mode
          letterbox fades and moves with the clip instead of revealing the outgoing clip. */}
      <AbsoluteFill style={{ opacity, overflow: 'hidden', backgroundColor: background }}>
        <OffthreadVideo
          src={src}
          trimBefore={clip.trimBefore > 0 ? clip.trimBefore : undefined}
          playbackRate={rate}
          muted={muted}
          volume={gain}
          // Preview gains above 1 are heard (see AudioLayer); renders ignore it.
          useWebAudioApi
          pauseWhenBuffering
          style={framingStyle(clip.framing, clip.sourceWidth, clip.sourceHeight, width, height, frame)}
        />
      </AbsoluteFill>
    </TransitionFrame>
  );
};

/** Black (or white) under-layer for dip windows (outgoing fade-out + incoming fade-in). */
const DipMattes: React.FC<{ clips: PlanClip[] }> = ({ clips }) => (
  <>
    {clips.map((clip, i) => {
      const t = transitionType(clip);
      if (!isDip(t)) return null;
      const white = t === 'dip-to-white';
      // The first clip fades in over the background (a white dip: over white).
      if (i === 0 && !white) return null;
      const prev = i > 0 ? clips[i - 1] : undefined;
      const before = Math.max(0, Math.round(prev?.fadeOutFrames ?? 0));
      const after = dipFadeInFrames(clip);
      const from = Math.max(0, clip.from - before);
      const dur = clip.from + after - from;
      if (dur <= 0) return null;
      return (
        <Sequence key={`dip-${clip.id}`} from={from} durationInFrames={dur} name={`dip ${clip.id}`}>
          <AbsoluteFill style={{ backgroundColor: white ? '#fff' : '#000' }} />
        </Sequence>
      );
    })}
  </>
);

/** The plan's clips. `silent`: a nested cut's picture (its sound is in the outer plan). */
export const ClipLayer: React.FC<{ plan: RenderPlan; silent?: boolean }> = ({ plan, silent }) => {
  const clips = plan.clips ?? [];
  const premount = Math.max(1, Math.round(plan.fps));
  return (
    <>
      <DipMattes clips={clips} />
      {clips.map((clip, i) => {
        if (!(clip.durationInFrames > 0)) return null;
        return (
          <Sequence
            key={clip.id || `clip-${i}`}
            from={clip.from}
            durationInFrames={clip.durationInFrames}
            premountFor={premount}
            name={clip.label ? `clip ${clip.label}` : `clip ${clip.id}`}
          >
            <ClipView
              clip={clip}
              next={clips[i + 1]}
              src={plan.sources?.[clip.assetId]}
              background={safeColor(plan.background, '#000000')}
              silent={silent}
              nested={plan.nested?.[clip.assetId]}
              plan={plan}
            />
          </Sequence>
        );
      })}
    </>
  );
};
