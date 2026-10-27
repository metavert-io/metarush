import { Easing, interpolate } from 'remotion';

/**
 * One easing family for the whole package. Entrances decelerate hard and settle
 * without overshoot; exits accelerate away; crossfades are symmetric.
 */
export const EASE_OUT = Easing.bezier(0.16, 1, 0.3, 1);
export const EASE_IN = Easing.bezier(0.55, 0, 0.75, 0.2);
export const EASE_IN_OUT = Easing.bezier(0.65, 0, 0.35, 1);

export const clamp01 = (v: number) => Math.max(0, Math.min(1, v));

/** 0 → 1 between `start` and `start + duration` (frames), eased. */
export const ramp = (
  frame: number,
  start: number,
  duration: number,
  easing: (t: number) => number = EASE_OUT,
): number => {
  if (duration <= 0) return frame >= start ? 1 : 0;
  return interpolate(frame, [start, start + duration], [0, 1], {
    easing,
    extrapolateLeft: 'clamp',
    extrapolateRight: 'clamp',
  });
};

export const mix = (a: number, b: number, t: number) => a + (b - a) * t;

/**
 * Timing for an overlay that must enter and exit inside its own duration.
 * Choreography is authored in seconds; `enter()` compresses it proportionally
 * when an overlay is too short for the full entrance, so the exit (last ~0.42s,
 * 13 frames at 30fps) always has room and never truncates.
 */
export type OverlayClock = {
  frame: number;
  /** Eased 0..1 progress of an entrance beat authored at (delay, duration) seconds. */
  enter: (delaySec: number, durationSec: number, easing?: (t: number) => number) => number;
  /** 1 until the exit starts, then eases to 0 on the final frame. */
  exit: number;
  exitFrames: number;
  durationInFrames: number;
  fps: number;
};

export const makeOverlayClock = (
  frame: number,
  durationInFrames: number,
  fps: number,
  /** Seconds at which the template's last entrance beat finishes. */
  enterSpanSec: number,
): OverlayClock => {
  const exitFrames = Math.max(4, Math.min(Math.round(0.42 * fps), Math.floor(durationInFrames / 3)));
  const exitStart = durationInFrames - exitFrames;
  const available = Math.max(1, exitStart * 0.9);
  const span = Math.max(1, enterSpanSec * fps);
  const k = Math.min(1, available / span);
  const enter = (delaySec: number, durationSec: number, easing = EASE_OUT) =>
    ramp(frame, delaySec * fps * k, Math.max(1, durationSec * fps * k), easing);
  const exit = 1 - ramp(frame, exitStart, exitFrames - 1, EASE_IN);
  return { frame, enter, exit, exitFrames, durationInFrames, fps };
};
