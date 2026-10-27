import type { RenderPlan } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, useVideoConfig } from 'remotion';
import { AudioLayer } from './audio/AudioLayer';
import { CaptionLayer } from './captions/CaptionLayer';
import { ClipLayer } from './clips/ClipLayer';
import { makeLayout } from './core/layout';
import { resolveBrand, safeColor, withFontFile } from './theme/brand';
import { useBundledFonts } from './theme/fonts';
import { OverlayLayer } from './templates/registry';
import { ResourcesProvider, useBrandFontFile } from './templates/resources';

export type MetaRushEditProps = { plan: RenderPlan };

/**
 * Renders a Go-resolved RenderPlan verbatim. All timing comes from the plan;
 * the only time math here is easing inside each template's own duration.
 * Used unchanged by the dashboard (@remotion/player, proxy URLs) and the
 * worker (render/render.mjs, original URLs).
 *
 * Layers, bottom to top: background, dip mattes, clips, lower/center/corner
 * overlays, captions, full-frame overlays (end cards cover captions, as Go's
 * validator assumes). Resources (logos, pictures, fonts) resolve by name
 * through ResourcesProvider; a nested cut draws inside its clip (ClipLayer).
 */
export const MetaRushEdit: React.FC<MetaRushEditProps> = ({ plan }) => {
  const fontsReady = useBundledFonts();
  const fontFile = useBrandFontFile(plan);
  const { width, height, fps } = useVideoConfig();
  const brand = useMemo(() => withFontFile(resolveBrand(plan?.brand), fontFile), [plan?.brand, fontFile]);
  const layout = useMemo(() => makeLayout(width, height, fps, plan?.safeArea), [width, height, fps, plan?.safeArea]);

  if (!plan) {
    return <AbsoluteFill style={{ backgroundColor: '#000' }} />;
  }

  return (
    <ResourcesProvider plan={plan}>
      <AbsoluteFill
        style={{
          backgroundColor: safeColor(plan.background, '#000000'),
          fontFamily: brand.fontFamily,
          WebkitFontSmoothing: 'antialiased',
          overflow: 'hidden',
        }}
      >
        <ClipLayer plan={plan} />
        <AudioLayer plan={plan} />
        {fontsReady ? <OverlayLayer plan={plan} brand={brand} layout={layout} layer="base" /> : null}
        {fontsReady && plan.captions ? <CaptionLayer captions={plan.captions} overlays={plan.overlays} brand={brand} layout={layout} /> : null}
        {fontsReady ? <OverlayLayer plan={plan} brand={brand} layout={layout} layer="full" /> : null}
      </AbsoluteFill>
    </ResourcesProvider>
  );
};
