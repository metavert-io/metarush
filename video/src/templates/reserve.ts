import type { PlanOverlay } from '@metarush/schema';
import type { FrameLayout } from '../core/layout';
import type { ResolvedBrand } from '../theme/brand';
import { lowerThirdCinematicMetrics } from './LowerThirdCinematic';
import { lowerThirdCleanMetrics, parseLowerThird } from './LowerThirdClean';
import { nameBarMetrics, parseNameBar } from './NameBar';

/** The share of the content box a lower-region motion graphic may use (its band). */
export const MOTION_LOWER_BAND = 0.26;

/**
 * Height (px above the content box's bottom edge) taken by lower-region
 * templates on screen at any time within [from, to). Captions at
 * position lower/bottom stay above it, so a caption line never collides with a
 * name strap even when the edit asks for both (Go flags it as
 * caption_collision; we still render it cleanly).
 */
export const lowerRegionReserve = (
  overlays: PlanOverlay[] | undefined,
  from: number,
  to: number,
  brand: ResolvedBrand,
  layout: FrameLayout,
): number => {
  let reserve = 0;
  for (const o of overlays ?? []) {
    if (!o || !(o.from < to && o.from + o.durationInFrames > from)) continue;
    if (o.motion) {
      // A project motion graphic in the lower region keeps the lower band (Go's QA uses the same band).
      if (o.motion.region === 'lower') reserve = Math.max(reserve, layout.content.height * MOTION_LOWER_BAND);
      continue;
    }
    if (o.template === 'name.bar') {
      // The bar at the bottom of the picture (D162): captions stay above its top
      // edge, whichever of its segments is on screen (a hidden one takes none).
      const bottom = layout.content.top + layout.content.height;
      for (const fields of o.segments?.length ? o.segments.map((sg) => sg.params) : [o.params]) {
        const p = parseNameBar(fields);
        if (p.hide) continue;
        const m = nameBarMetrics(p, brand, layout);
        if (m.atBottom) reserve = Math.max(reserve, bottom - m.top + Math.round(12 * layout.unit));
      }
      continue;
    }
    if (o.template !== 'lower-third.clean' && o.template !== 'lower-third.cinematic') continue;
    const p = parseLowerThird(o.params);
    if (!p) continue;
    const m = o.template === 'lower-third.clean' ? lowerThirdCleanMetrics(p, brand, layout) : lowerThirdCinematicMetrics(p, brand, layout);
    reserve = Math.max(reserve, m.blockH);
  }
  return reserve;
};
