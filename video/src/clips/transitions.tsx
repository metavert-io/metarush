import type { PlanClip, PlanTransition } from '@metarush/schema';
import type { TransitionPresentation, TransitionPresentationComponentProps } from '@remotion/transitions';
import { slide, type SlideDirection } from '@remotion/transitions/slide';
import { wipe, type WipeDirection } from '@remotion/transitions/wipe';
import React from 'react';
import { AbsoluteFill } from 'remotion';
import { EASE_IN, EASE_IN_OUT, EASE_OUT, clamp01 } from '../core/motion';

/**
 * Transitions as Go planned them. Go decided every frame (which clips overlap,
 * for how long, in which direction, with which easing); this file only turns a
 * planned progress into pixels. Wipes, slides and pushes reuse the
 * @remotion/transitions presentations, driven by our own progress instead of a
 * TransitionSeries; zoom and whip are small components of our own.
 *
 * Directions name the way the picture moves: "left" means the next shot comes
 * in from the right and travels left (Remotion's "from-right").
 */

/** Transitions that move or clip the picture instead of fading it. */
export const isMoving = (t: string | undefined) => t === 'wipe' || t === 'slide' || t === 'push' || t === 'whip';

/** Transitions where both shots share the screen (the incoming one starts early). */
export const isOverlap = (t: string | undefined) =>
  t === 'crossfade' || t === 'wipe' || t === 'slide' || t === 'push' || t === 'zoom' || t === 'whip';

export const isDip = (t: string | undefined) => t === 'dip-to-black' || t === 'dip-to-white';

/** The vetted easings (the plan's `easing`); undefined keeps a transition's default. */
export const easingFor = (e: string | undefined, fallback: (t: number) => number = EASE_IN_OUT): ((t: number) => number) => {
  switch (e) {
    case 'linear':
      return (t) => t;
    case 'ease-in':
      return EASE_IN;
    case 'ease-out':
      return EASE_OUT;
    case 'ease-in-out':
      return EASE_IN_OUT;
  }
  return fallback;
};

/** Eased 0..1 progress of a transition that starts at clip frame `start`. */
const progressOf = (frame: number, start: number, t: PlanTransition): number => {
  const d = Math.max(0, Math.round(t.durationInFrames));
  if (d <= 0) return 1;
  return easingFor(t.easing)(clamp01((frame - start) / d));
};

const slideDirection = (d: string | undefined): SlideDirection =>
  d === 'right' ? 'from-left' : d === 'up' ? 'from-bottom' : d === 'down' ? 'from-top' : 'from-right';

const wipeDirection = (d: string | undefined): WipeDirection => slideDirection(d) as WipeDirection;

const noop = () => undefined;

/** Draws a @remotion/transitions presentation at our planned progress. */
const Presented: React.FC<{
  presentation: TransitionPresentation<Record<string, unknown>>;
  progress: number;
  direction: 'entering' | 'exiting';
  durationInFrames: number;
  children: React.ReactNode;
}> = ({ presentation, progress, direction, durationInFrames, children }) => {
  const Component = presentation.component as React.ComponentType<TransitionPresentationComponentProps<Record<string, unknown>>>;
  return (
    <Component
      presentationProgress={progress}
      presentationDirection={direction}
      passedProps={presentation.props}
      presentationDurationInFrames={durationInFrames}
      onElementImage={noop}
      onUnmount={noop}
      bothEnteringAndExiting={false}
    >
      {children}
    </Component>
  );
};

/** Offset (percent of the frame) of a shot moving in `direction` at `amount` 0..1 off-screen. */
const offset = (direction: string | undefined, amount: number): string => {
  const pct = amount * 100;
  switch (direction) {
    case 'right':
      return `translateX(${-pct}%)`;
    case 'up':
      return `translateY(${pct}%)`;
    case 'down':
      return `translateY(${-pct}%)`;
    default:
      return `translateX(${pct}%)`;
  }
};

/** Blur that peaks in the middle of a transition (px at a 1080 px short edge × unit). */
const peakBlur = (p: number, px: number, unit: number) => Math.sin(clamp01(p) * Math.PI) * px * unit;

/** A blurred layer grows a little so its soft edges stay outside the frame. */
const blurCover = (p: number) => 1 + 0.06 * Math.sin(clamp01(p) * Math.PI);

const enter = (t: PlanTransition, p: number, unit: number, node: React.ReactNode): React.ReactNode => {
  const d = Math.max(1, t.durationInFrames);
  switch (t.type) {
    case 'wipe':
      return (
        <Presented presentation={wipe({ direction: wipeDirection(t.direction) }) as TransitionPresentation<Record<string, unknown>>} progress={p} direction="entering" durationInFrames={d}>
          {node}
        </Presented>
      );
    case 'slide':
    case 'push':
      return (
        <Presented presentation={slide({ direction: slideDirection(t.direction) }) as TransitionPresentation<Record<string, unknown>>} progress={p} direction="entering" durationInFrames={d}>
          {node}
        </Presented>
      );
    case 'zoom':
      // The next shot settles from close up while it fades in (its opacity ramp is
      // the clip layer's own, like a dissolve).
      return <AbsoluteFill style={{ transform: `scale(${1.35 - 0.35 * p})`, filter: `blur(${peakBlur(p, 10, unit).toFixed(2)}px)` }}>{node}</AbsoluteFill>;
    case 'whip':
      return (
        <AbsoluteFill style={{ transform: `${offset(t.direction, 1 - p)} scale(${blurCover(p)})`, filter: `blur(${peakBlur(p, 26, unit).toFixed(2)}px)` }}>{node}</AbsoluteFill>
      );
  }
  return node;
};

const exit = (t: PlanTransition, p: number, unit: number, node: React.ReactNode): React.ReactNode => {
  const d = Math.max(1, t.durationInFrames);
  switch (t.type) {
    case 'push':
      return (
        <Presented presentation={slide({ direction: slideDirection(t.direction) }) as TransitionPresentation<Record<string, unknown>>} progress={p} direction="exiting" durationInFrames={d}>
          {node}
        </Presented>
      );
    case 'zoom':
      return <AbsoluteFill style={{ transform: `scale(${1 + 0.5 * p})`, filter: `blur(${peakBlur(p, 10, unit).toFixed(2)}px)` }}>{node}</AbsoluteFill>;
    case 'whip':
      return (
        <AbsoluteFill style={{ transform: `${offset(t.direction, -p)} scale(${blurCover(p)})`, filter: `blur(${peakBlur(p, 26, unit).toFixed(2)}px)` }}>{node}</AbsoluteFill>
      );
  }
  return node;
};

/**
 * Wraps a clip's picture in the movement of its entering transition and, when
 * the next clip pushes, zooms or whips it out, of its leaving one.
 */
export const TransitionFrame: React.FC<{ clip: PlanClip; frame: number; unit: number; children: React.ReactNode }> = ({ clip, frame, unit, children }) => {
  let node: React.ReactNode = children;
  const tin = clip.transitionIn;
  if (tin && (isMoving(tin.type) || tin.type === 'zoom') && tin.durationInFrames > 0) {
    node = enter(tin, progressOf(frame, 0, tin), unit, node);
  }
  const tout = clip.transitionOut;
  if (tout && tout.durationInFrames > 0) {
    node = exit(tout, progressOf(frame, clip.durationInFrames - tout.durationInFrames, tout), unit, node);
  }
  return <>{node}</>;
};
