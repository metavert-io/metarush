import type React from 'react';

/**
 * Project motion graphics (D68) reach the render page as bundles MetaRush
 * built from approved archives (render/motion/bundle.mjs). Each bundle
 * registers itself with this host; the host lends it the page's own React,
 * Remotion and @metarush/motion so a component shares the renderer's
 * instances. The dashboard has no host: it never runs component code.
 */
export type MotionParse = { success: true; data: unknown } | { success: false; error: string };

export type MotionEntry = {
  component: React.ComponentType<Record<string, unknown>> | undefined;
  schema: unknown;
  meta: unknown;
  exports: string[];
  /** The parameter schema as JSON Schema (zod's own converter, in this page). */
  jsonSchema: () => unknown;
  parse: (params: unknown) => MotionParse;
};

export type MotionHost = {
  modules: Record<string, unknown>;
  entries: Record<string, MotionEntry>;
  register: (hash: string, entry: MotionEntry) => void;
};

declare global {
  // eslint-disable-next-line no-var
  var __metarushMotion: MotionHost | undefined;
}

export const motionHost = (): MotionHost | undefined => (typeof globalThis === 'undefined' ? undefined : globalThis.__metarushMotion);
