import type { RenderPlan } from '../api/types';

/*
 * Preview loudness parity (D64). The renderer spreads a mono file over both
 * stereo channels with equal power (each channel at −3.01 dB, FFmpeg's
 * mono-to-stereo upmix, measured on renders); the browser copies a mono file
 * to both channels at full level. So, in the preview only, every element that
 * plays a mono file gets its gain envelope scaled by 1/√2. The plan says which
 * files are mono (sourceChannels, filled by the preview plan alongside
 * sources): footage plays from stereo proxies and needs nothing; narration,
 * music and effects play their originals. Renders never see this.
 */
export const MONO_PREVIEW_GAIN = Math.SQRT1_2;

export function compensateMono(plan: RenderPlan): RenderPlan {
  const ch = plan.sourceChannels;
  if (!ch) return plan;
  const mono = (assetId: string) => ch[assetId] === 1;
  // A clearer copy of a recording (variant) is its own file: what matters is
  // whether that file is mono (a mono camera recording plays from a stereo
  // proxy, its clearer copy from a mono original).
  const heard = (assetId: string, variant?: { assetId: string } | null) =>
    variant && plan.sources?.[variant.assetId] ? variant.assetId : assetId;
  const scale = <T extends { f: number; g: number }>(env: T[] | undefined): T[] | undefined =>
    env ? env.map((p) => ({ ...p, g: p.g * MONO_PREVIEW_GAIN })) : env;
  let changed = false;
  const clips = plan.clips.map((c) => {
    if (!mono(heard(c.assetId, c.audio?.variant))) return c;
    changed = true;
    return { ...c, gain: scale(c.gain), audio: c.audio ? { ...c.audio, gain: scale(c.audio.gain) } : c.audio, volume: c.volume * MONO_PREVIEW_GAIN };
  });
  const audio = plan.audio.map((a) => {
    if (!mono(heard(a.assetId, a.variant))) return a;
    changed = true;
    // Without an envelope (hand-built plans) the level comes from gainDb.
    return a.gain?.length ? { ...a, gain: scale(a.gain) } : { ...a, gainDb: a.gainDb - 3.0103 };
  });
  return changed ? { ...plan, clips, audio } : plan;
}
