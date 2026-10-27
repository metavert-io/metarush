import { useEffect, useState, useSyncExternalStore } from 'react';
import { getRemotionEnvironment, useDelayRender } from 'remotion';
import { BUNDLED_FACES } from './fontFaces';

/**
 * Brand font names accepted by the RenderPlan (`brand.font`).
 * Each maps to locally bundled OFL variable fonts registered under a private
 * family name ("MR …") so we never collide with fonts the host page loads.
 */
export type BrandFont = 'Inter' | 'Fraunces' | 'JetBrains Mono';

export const FONT_STACKS: Record<BrandFont, string> = {
  Inter: '"MR Inter", system-ui, sans-serif',
  Fraunces: '"MR Fraunces", Georgia, serif',
  'JetBrains Mono': '"MR JetBrains Mono", ui-monospace, monospace',
};

/** The serif used by `lower-third.cinematic` regardless of brand font. */
export const SERIF_STACK = FONT_STACKS.Fraunces;

export const fontStackFor = (font: string | undefined): string =>
  font && font in FONT_STACKS ? FONT_STACKS[font as BrandFont] : FONT_STACKS.Inter;

type LoadState = 'idle' | 'loading' | 'ready' | 'failed';

let state: LoadState = 'idle';
let loadError: Error | null = null;
let loadPromise: Promise<void> | null = null;
const listeners = new Set<() => void>();

const setState = (next: LoadState) => {
  state = next;
  for (const l of listeners) l();
};

/**
 * Registers and eagerly loads every bundled face (all subsets, ~620 KB).
 * Loading everything up front keeps renders deterministic: no glyph can fall
 * back to a system font on one frame and swap on the next.
 */
export const loadBundledFonts = (): Promise<void> => {
  if (loadPromise) return loadPromise;
  if (typeof document === 'undefined' || typeof FontFace === 'undefined') {
    state = 'ready';
    loadPromise = Promise.resolve();
    return loadPromise;
  }
  setState('loading');
  loadPromise = Promise.all(
    BUNDLED_FACES.map(async (f) => {
      const face = new FontFace(f.family, `url("${f.url}") format("woff2")`, {
        weight: f.weight,
        style: 'normal',
        unicodeRange: f.unicodeRange,
        display: 'block',
      });
      await face.load();
      document.fonts.add(face);
    }),
  ).then(
    () => setState('ready'),
    (err: unknown) => {
      loadError = err instanceof Error ? err : new Error(String(err));
      setState('failed');
      throw loadError;
    },
  );
  return loadPromise;
};

const subscribe = (cb: () => void) => {
  listeners.add(cb);
  return () => listeners.delete(cb);
};
const isSettled = () => state === 'ready' || state === 'failed';

/**
 * Returns true once the bundled fonts are usable. While loading, the current
 * frame is held with delayRender so the renderer never captures fallback text.
 * During a final render a font failure cancels the render; in the Player it
 * degrades to the fallback stack with a console warning.
 */
export const useBundledFonts = (): boolean => {
  const { delayRender, continueRender, cancelRender } = useDelayRender();
  const ready = useSyncExternalStore(subscribe, isSettled, () => false);
  const [handle] = useState<number | null>(() =>
    isSettled() ? null : delayRender('Loading bundled MetaRush fonts'),
  );

  useEffect(() => {
    loadBundledFonts().catch((err: unknown) => {
      if (getRemotionEnvironment().isRendering) {
        cancelRender(err instanceof Error ? err : new Error(String(err)));
      } else {
        console.warn('[metarush] bundled fonts failed to load; using fallbacks', err);
      }
    });
  }, [cancelRender]);

  useEffect(() => {
    if (ready && handle !== null) continueRender(handle);
  }, [ready, handle, continueRender]);

  return ready;
};
