import type { PlanCaptions, PlanOverlay, RenderPlan } from '@metarush/schema';
import React from 'react';
import { Sequence, useCurrentFrame } from 'remotion';
import { captionZone } from '../captions/CaptionLayer';
import type { FrameLayout } from '../core/layout';
import type { ResolvedBrand } from '../theme/brand';
import { DataCardStat } from './DataCardStat';
import { EndCardCta } from './EndCardCta';
import { EndScreen } from './EndScreen';
import { LowerThirdCinematic } from './LowerThirdCinematic';
import { LowerThirdClean } from './LowerThirdClean';
import { LogoCorner } from './LogoCorner';
import catalog from '@metarush/schema/schemas/templates.json';
import { QuoteKinetic } from './QuoteKinetic';
import { OverlayBoundary, segmentAt, warnOnce, type TemplateProps } from './shared';
import { NameBar } from './NameBar';
import { SlideArchitecture } from './SlideArchitecture';
import { SlideBullets } from './SlideBullets';
import { SlideCode } from './SlideCode';
import { SlideQuote } from './SlideQuote';
import { SlideScreenshot } from './SlideScreenshot';
import { SlideTitle } from './SlideTitle';
import { TitleCard } from './TitleCard';
import { MotionOverlay } from '../motion/MotionOverlay';

export type TemplateManifestEntry = { readonly id: string; readonly version: string };

/** Screen region per template (Go uses the same regions for collision checks). */
type Region = 'lower' | 'center' | 'full' | 'corner';

/** One entry of schema/templates.json. */
export interface TemplateCatalogEntry {
  readonly id: string;
  readonly version: string;
  readonly region: string;
  readonly summary: string;
  readonly minDuration: number;
  readonly maxDuration: number;
}

/**
 * The template catalog, generated from the Go catalog (internal/editspec
 * Templates) by `metarush schema export` into schema/templates.json. IDs,
 * versions and regions come only from there; render/render.mjs reads the same
 * file for its versions line.
 */
export const TEMPLATE_CATALOG: readonly TemplateCatalogEntry[] = catalog.templates;

/** Template IDs and versions (what a render records). */
export const TEMPLATE_MANIFEST: readonly TemplateManifestEntry[] = TEMPLATE_CATALOG.map(({ id, version }) => ({ id, version }));

/** The vetted components. Every catalog entry must have one (checked below and by the Go drift test). */
const COMPONENTS: Record<string, React.FC<TemplateProps>> = {
  'lower-third.clean': LowerThirdClean,
  'lower-third.cinematic': LowerThirdCinematic,
  'quote.kinetic': QuoteKinetic,
  'endcard.cta': EndCardCta,
  'datacard.stat': DataCardStat,
  'title.card': TitleCard,
  'logo.corner': LogoCorner,
  'slide.title': SlideTitle,
  'slide.bullets': SlideBullets,
  'slide.code': SlideCode,
  'slide.architecture': SlideArchitecture,
  'slide.screenshot': SlideScreenshot,
  'slide.quote': SlideQuote,
  'name.bar': NameBar,
  'end.screen': EndScreen,
};

/**
 * Hands a template the fields of the segment on screen (D163): Go resolved the
 * graphic's changes into segments; the template only reads the current one.
 */
const Segmented: React.FC<{ Component: React.FC<TemplateProps>; props: TemplateProps }> = ({ Component, props }) => {
  const frame = useCurrentFrame();
  const seg = segmentAt(props.segments, frame);
  return <Component {...props} params={seg ? seg.params : props.params} segmentFrom={seg ? seg.from : 0} />;
};

const isRegion = (r: string): r is Region => r === 'lower' || r === 'center' || r === 'full' || r === 'corner';

const VERSIONS = new Map(TEMPLATE_CATALOG.map((t) => [t.id, t.version]));
const REGIONS = new Map<string, Region>();

for (const t of TEMPLATE_CATALOG) {
  if (!COMPONENTS[t.id]) throw new Error(`[metarush] the template catalog lists ${t.id} but no component is registered`);
  if (!isRegion(t.region)) throw new Error(`[metarush] template ${t.id} has unknown region "${t.region}"`);
  REGIONS.set(t.id, t.region);
}

/**
 * Center templates share the screen with captions in Go's region model
 * (captions lower/bottom occupy "lower", top occupies "top"). When caption
 * lines overlap an overlay in time, its content box excludes the caption band
 * so a stat card or quote never sits on top of the words being spoken.
 */
const centerLayout = (layout: FrameLayout, captions: PlanCaptions | undefined, from: number, to: number): FrameLayout => {
  if (!captions || captions.position === 'middle') return layout;
  const overlaps = (captions.lines ?? []).some((l) => l.from < to && l.to > from);
  if (!overlaps) return layout;
  const zone = captionZone(layout, captions);
  const gap = Math.round(32 * layout.unit);
  const { content } = layout;
  if (captions.position === 'top') {
    const top = Math.min(content.top + content.height * 0.5, zone.bottom + gap);
    return { ...layout, content: { ...content, top, height: content.top + content.height - top } };
  }
  const bottom = Math.max(content.top + content.height * 0.5, zone.top - gap);
  return { ...layout, content: { ...content, height: bottom - content.top } };
};

/** A full-frame card (it covers the footage and sits above captions). */
const isFullOverlay = (o: PlanOverlay): boolean => (o.motion ? o.motion.region === 'full' : REGIONS.get(o.template) === 'full');

/**
 * Whether another full-frame card is on screen on the frame before `overlay`
 * starts and on the frame after it ends. Slides shown back to back use it to
 * keep one continuous background instead of fading through the footage.
 */
const fullJoins = (overlays: PlanOverlay[] | undefined, overlay: PlanOverlay): { before: boolean; after: boolean } => {
  const on = (f: number) =>
    (overlays ?? []).some((o) => o && o !== overlay && o.durationInFrames > 0 && isFullOverlay(o) && o.from <= f && f < o.from + o.durationInFrames);
  return { before: overlay.from > 0 && on(overlay.from - 1), after: on(overlay.from + overlay.durationInFrames) };
};

const OverlayItem: React.FC<{
  overlay: PlanOverlay;
  plan: RenderPlan;
  brand: ResolvedBrand;
  layout: FrameLayout;
}> = ({ overlay, plan, brand, layout }) => {
  if (overlay.motion) {
    // A project motion graphic (D68): its region comes from its check.
    const motionLayout =
      overlay.motion.region === 'center' ? centerLayout(layout, plan.captions, overlay.from, overlay.from + overlay.durationInFrames) : layout;
    return <MotionOverlay overlay={overlay} plan={plan} brand={brand} layout={motionLayout} />;
  }
  const Component = COMPONENTS[overlay.template];
  if (!Component) {
    warnOnce(`unknown template "${overlay.template}" (overlay ${overlay.id}); rendering nothing`);
    return null;
  }
  const itemLayout =
    REGIONS.get(overlay.template) === 'center'
      ? centerLayout(layout, plan.captions, overlay.from, overlay.from + overlay.durationInFrames)
      : layout;
  const expected = VERSIONS.get(overlay.template);
  if (overlay.templateVersion && expected && overlay.templateVersion !== expected) {
    warnOnce(
      `overlay ${overlay.id} asks for ${overlay.template}@${overlay.templateVersion}, this package ships @${expected}; rendering @${expected}`,
    );
  }
  return (
    <Sequence from={overlay.from} durationInFrames={overlay.durationInFrames} name={`${overlay.template} ${overlay.id}`}>
      <OverlayBoundary label={`${overlay.template} ${overlay.id}`}>
        <Segmented
          Component={Component}
          props={{
            params: overlay.params,
            durationInFrames: overlay.durationInFrames,
            from: overlay.from,
            planDurationInFrames: plan.durationInFrames,
            brand,
            layout: itemLayout,
            joins: REGIONS.get(overlay.template) === 'full' ? fullJoins(plan.overlays, overlay) : undefined,
            segments: overlay.segments ?? undefined,
          }}
        />
      </OverlayBoundary>
    </Sequence>
  );
};

/**
 * `layer="base"` renders lower, center and corner templates (below captions);
 * `layer="full"` renders full-frame cards, which sit above captions
 * (Go: "captions hide behind full-frame cards"). Plan order is kept in each.
 */
export const OverlayLayer: React.FC<{
  plan: RenderPlan;
  brand: ResolvedBrand;
  layout: FrameLayout;
  layer: 'base' | 'full';
}> = ({ plan, brand, layout, layer }) => (
  <>
    {(plan.overlays ?? []).map((o, i) => {
      if (!o || !(o.durationInFrames > 0)) return null;
      if ((layer === 'full') !== isFullOverlay(o)) return null;
      return <OverlayItem key={o.id || `overlay-${i}`} overlay={o} plan={plan} brand={brand} layout={layout} />;
    })}
  </>
);
