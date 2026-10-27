/**
 * `@metarush/motion`: what a project motion graphic may import from MetaRush
 * (D68) — the types its props come in and the theme and layout helpers the
 * vetted templates use. The render page provides this module; a component's
 * bundle never contains a copy.
 *
 *   import type { MotionProps, MotionMeta } from '@metarush/motion';
 *   import { makeOverlayClock, fitWords, rgba } from '@metarush/motion';
 *
 * Resources (D107): a component draws the project's pictures by name —
 *
 *   const logo = useResource('logo');            // a name in quotes: checked by motion check, pinned by the cut
 *   const shot = useResource(params.screenshot); // a parameter marked as a resource in the schema:
 *   // export const schema = z.object({ screenshot: z.string().meta({ format: 'metarush-resource' }) });
 *   return logo ? <ResourceImage pic={logo} style={{ width: 240 }} /> : null;
 *
 * The files come by hash from the render's own loopback media server (the
 * network jail allows nothing else); a component never sees an address.
 */
import { useContext, useMemo } from 'react';
import type { z } from 'zod';
import type { FrameLayout } from '../core/layout';
import { ResourcesContext, resolvePicture, useResourceFont, type ResolvedResource } from '../templates/resources';
import { warnOnce } from '../templates/shared';
import type { ResolvedBrand } from '../theme/brand';

export { orientationOf, pick, type Box, type FrameLayout, type Orientation } from '../core/layout';
export { EASE_IN, EASE_IN_OUT, EASE_OUT, clamp01, makeOverlayClock, mix, ramp, type OverlayClock } from '../core/motion';
export { balanceLines, fitSingleLine, fitWords, measureWidth, splitWords, wrapWords, type FitResult, type TextStyle } from '../core/text';
export { contrastRatio, hexToRgb, luminance, readableOn, rgba, type ResolvedBrand } from '../theme/brand';
export { ResourceImage, fitInside, type ResolvedResource } from '../templates/resources';

/**
 * A picture from the cut's resources by its short name ("logo", "deck#3" for
 * page 3 of a PDF, "homepage#2" for a web page snapshot on a phone), or null
 * when the cut has no picture by that name. Draw it with <ResourceImage pic={…} />
 * (the frame waits until it has loaded).
 */
export const useResource = (name: string | undefined): ResolvedResource | null => {
  const scope = useContext(ResourcesContext);
  const pic = useMemo(() => resolvePicture(scope, name), [scope, name]);
  if (name && !pic) warnOnce(`a motion graphic asked for the resource \u201c${name}\u201d, which this cut has no picture for`);
  return pic;
};

/**
 * A font from the cut's resources by its short name in quotes, registered as
 * the CSS family the code names (D171): `useFont('inter', 'Inter', { weight:
 * '400 800' })`, then `fontFamily: 'Inter'`. The frame waits until it has
 * loaded. A cut that uses the graphic pins the font like any resource.
 */
export const useFont = (name: string, family: string, descriptors?: FontFaceDescriptors): string => useResourceFont(name, family, descriptors);

/** Where on screen the graphic draws (collision checks, captions, QA). */
export type MotionRegion = 'lower' | 'center' | 'full';

/** What a component says about itself: `export const meta: MotionMeta = {…}`. */
export type MotionMeta = {
  /** One plain sentence for the person who approves it. */
  summary: string;
  region: MotionRegion;
  /** Seconds it needs at least, and at most, to read well (at most 60, unless persistent). */
  minDuration: number;
  maxDuration?: number;
  /**
   * Made to stay on screen for a section or the whole video (a name bar, a
   * corner mark, D163): no time limit; a cut places it with a span and may
   * change its fields while it stays up (`segments`).
   */
  persistent?: boolean;
  /** Extra moments (seconds from its start) the check takes test frames at. */
  checkAt?: number[];
};

/**
 * The props a component receives — the same as a vetted template's, with its
 * parameters already checked against its schema:
 *
 *   export default function RouteMap({ params, layout, brand }: MotionProps<typeof schema>) { … }
 *
 * Frames are relative to the graphic (useCurrentFrame() is 0 when it appears).
 */
export type MotionProps<S = unknown> = {
  params: S extends z.ZodType ? z.output<S> : S;
  durationInFrames: number;
  /** Absolute timeline frame where the graphic starts. */
  from: number;
  planDurationInFrames: number;
  brand: ResolvedBrand;
  /** Frame geometry: `content` is where text may go (inside the platform's safe area). */
  layout: FrameLayout;
  /**
   * When the cut changes the graphic's fields while it stays on screen (D163):
   * every segment's fields from a frame (relative to the graphic) on; `params`
   * is the current one's and `segmentFrom` the frame it started, so a component
   * can animate a change (a name fading in) instead of starting over.
   */
  segments?: { from: number; params: unknown }[];
  segmentFrom?: number;
};
