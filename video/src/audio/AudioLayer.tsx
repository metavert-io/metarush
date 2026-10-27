import type { FrameRange, GainPoint, PlanAudio, PlanClip, PlanVariant, RenderPlan } from '@metarush/schema';
import React from 'react';
import { Html5Audio, Sequence, useCurrentFrame, useRemotionEnvironment, useVideoConfig } from 'remotion';

export const dbToLinear = (db: number) => (Number.isFinite(db) ? 10 ** (db / 20) : 1);

const smoothstep = (t: number) => {
  const x = Math.max(0, Math.min(1, t));
  return x * x * (3 - 2 * x);
};

/**
 * Gain from a Go-resolved envelope at element-relative frame `f`: linear
 * interpolation between keyframes, the last value held. Go computes every gain
 * (bus levels, fades, transitions, ducking under narration and dialogue, master
 * loudness) into these keyframes, so this is the only audio math here and the
 * dashboard preview and the final render apply identical gains. Returns null
 * when the element has no envelope (hand-built plans): callers fall back to
 * the legacy per-field computation below.
 */
export const envelopeGain = (env: GainPoint[] | undefined, f: number): number | null => {
  if (!env || env.length === 0) return null;
  const first = env[0]!;
  if (f <= first.f) return first.g;
  const last = env[env.length - 1]!;
  if (f >= last.f) return last.g;
  let lo = 0;
  let hi = env.length - 1;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if (env[mid]!.f <= f) lo = mid;
    else hi = mid;
  }
  const a = env[lo]!;
  const b = env[hi]!;
  return b.f === a.f ? b.g : a.g + ((b.g - a.g) * (f - a.f)) / (b.f - a.f);
};

/**
 * Legacy: 0..1 ducking depth at timeline frame `t`: fully ducked inside a
 * dialogue range [from, to), with smooth ramps of `rampFrames` before `from`
 * (so speech onsets are never masked) and after `to`.
 */
export const duckEnvelope = (t: number, ranges: FrameRange[], rampFrames: number): number => {
  let depth = 0;
  for (const r of ranges) {
    if (!(r.to > r.from)) continue;
    let d = 0;
    if (t >= r.from && t < r.to) d = 1;
    else if (t < r.from) d = smoothstep(1 - (r.from - t) / rampFrames);
    else d = smoothstep(1 - (t - r.to) / rampFrames);
    if (d > depth) depth = d;
    if (depth >= 1) break;
  }
  return depth;
};

/** Linear gain for an audio layer at its own relative frame `f`. */
export const audioGain = (a: PlanAudio, f: number, plan: Pick<RenderPlan, 'dialogueRanges' | 'fps'>): number => {
  const env = envelopeGain(a.gain, f);
  if (env !== null) return Math.max(0, env);
  let g = dbToLinear(a.gainDb);
  const fadeIn = Math.max(0, Math.round(a.fadeInFrames ?? 0));
  if (fadeIn > 0) g *= smoothstep(f / fadeIn);
  const fadeOut = Math.max(0, Math.round(a.fadeOutFrames ?? 0));
  if (fadeOut > 0) g *= smoothstep((a.durationInFrames - f) / fadeOut);
  if (a.duckUnderDialogue && a.duckDb) {
    const rampFrames = Math.max(2, Math.round((plan.fps || 30) * 0.2));
    const depth = duckEnvelope(a.from + f, plan.dialogueRanges ?? [], rampFrames);
    // Interpolate in dB so the duck sounds even; accept duckDb given as +12 or -12.
    g *= dbToLinear(-Math.abs(a.duckDb) * depth);
  }
  return Math.max(0, g);
};

/*
 * useWebAudioApi: in the dashboard preview, media plays through the Web Audio
 * API so gains above 1 (the master loudness gain, a part turned up in the
 * mixer) are heard as in the render; a plain media element caps volume at 1.
 * Renders ignore it (the renderer mixes the audio itself). Always on: once an
 * element is routed through Web Audio it cannot be routed back (D64).
 */
const AudioTrack: React.FC<{ a: PlanAudio; src: string; trimBefore: number; plan: RenderPlan }> = ({ a, src, trimBefore, plan }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const env = useRemotionEnvironment();
  const rate = Number.isFinite(a.playbackRate) && (a.playbackRate ?? 0) > 0 ? (a.playbackRate as number) : 1;
  // A recording placed to the sample (D119) carries trimFraction.
  const exact = a.trimFraction !== undefined && a.trimFraction !== null;
  let trim = trimBefore;
  if (exact && env.isRendering) trim = Math.max(0, trim + renderStartShift(a.from, fps) * fps * rate);
  return (
    <Html5Audio
      src={src}
      trimBefore={trim > 0 ? trim : undefined}
      playbackRate={rate}
      // Placed to the sample, a drifting recording is resampled (its pitch moves by
      // a fraction of a cent) so its time stays exactly linear; the default keeps
      // the pitch by stretching time, which wanders by milliseconds.
      preservePitch={exact ? false : undefined}
      volume={audioGain(a, frame, plan)}
      useWebAudioApi
      pauseWhenBuffering
    />
  );
};

/**
 * How late (seconds; negative: early) the renderer starts an element that
 * begins at frame `from`: Remotion 4 delays each sound by a whole number of
 * milliseconds (ffmpeg's adelay, `(start * 1000).toFixed(0)`), so a sound that
 * starts on a frame between whole milliseconds (22 at 30 fps is 733.33 ms)
 * plays up to half a millisecond off. A recording placed to the sample moves
 * its trim by the same amount, so what plays at every sample is exactly right
 * (measured by the render self-test's sync check).
 */
export const renderStartShift = (from: number, fps: number): number => {
  const start = from / fps;
  return Number((start * 1000).toFixed(0)) / 1000 - start;
};

/**
 * A clip's sound over its own window (split edits, blends at cuts): the same
 * source as its picture, from `audio.from` for `audio.durationInFrames`, starting
 * `audio.trimBefore` frames into the source, at the clip's playback rate, with
 * the window's Go-resolved envelope. The picture plays muted in the clip layer.
 */
const ClipSound: React.FC<{ clip: PlanClip; src: string; trimBefore: number }> = ({ clip, src, trimBefore }) => {
  const frame = useCurrentFrame();
  const a = clip.audio!;
  const rate = Number.isFinite(clip.playbackRate) && clip.playbackRate > 0 ? clip.playbackRate : 1;
  return (
    <Html5Audio
      src={src}
      trimBefore={trimBefore > 0 ? trimBefore : undefined}
      playbackRate={rate}
      volume={Math.max(0, envelopeGain(a.gain, frame) ?? 1)}
      useWebAudioApi
      pauseWhenBuffering
    />
  );
};

/**
 * The exact place in a file an element starts, in frames: `trimBefore` plus,
 * for a recording placed to the sample (a conversation member, D119), the rest
 * below a frame (`trimFraction`). Remotion's media trims take fractional
 * frames: the render cuts the sound to the microsecond and the preview seeks
 * there, so every person's track lines up to the sample.
 */
export const exactTrim = (t: { trimBefore: number; trimFraction?: number | null }): number =>
  Math.max(0, t.trimBefore + (Number.isFinite(t.trimFraction) ? (t.trimFraction as number) : 0));

/**
 * What an element's sound plays from: its clearer copy (`variant`: a processed
 * copy of the recording, time-aligned with it, that starts `variant.trimBefore`
 * frames before this element's sound) when the plan carries one and its file
 * is there, otherwise the recording itself. The recording is never changed, so
 * the original stays one setting away (D88).
 */
export const soundSource = (
  plan: Pick<RenderPlan, 'sources'>,
  assetId: string,
  trimBefore: number,
  variant?: PlanVariant | null,
): { src: string; trimBefore: number; assetId: string } | null => {
  if (variant) {
    const vs = plan.sources?.[variant.assetId];
    if (vs) return { src: vs, trimBefore: exactTrim(variant), assetId: variant.assetId };
  }
  const src = plan.sources?.[assetId];
  return src ? { src, trimBefore, assetId } : null;
};

export const AudioLayer: React.FC<{ plan: RenderPlan }> = ({ plan }) => (
  <>
    {(plan.clips ?? []).map((clip, i) => {
      const a = clip.audio;
      if (!a || clip.muted || !clip.hasAudio || !(a.durationInFrames > 0)) return null;
      const s = soundSource(plan, clip.assetId, a.trimBefore, a.variant);
      if (!s) return null;
      return (
        <Sequence
          key={`sound-${clip.id || i}`}
          from={a.from}
          durationInFrames={a.durationInFrames}
          layout="none"
          name={`sound ${clip.label || clip.id}`}
        >
          <ClipSound clip={clip} src={s.src} trimBefore={s.trimBefore} />
        </Sequence>
      );
    })}
    {(plan.audio ?? []).map((a, i) => {
      const s = soundSource(plan, a.assetId, exactTrim(a), a.variant);
      if (!s || !(a.durationInFrames > 0)) return null;
      return (
        <Sequence
          key={a.id || `audio-${i}`}
          from={a.from}
          durationInFrames={a.durationInFrames}
          layout="none"
          name={`audio ${a.role || ''} ${a.id}`.trim()}
        >
          <AudioTrack a={a} src={s.src} trimBefore={s.trimBefore} plan={plan} />
        </Sequence>
      );
    })}
  </>
);
