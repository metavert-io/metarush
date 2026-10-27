import type { PlanResource, RenderPlan } from '@metarush/schema';
import React, { createContext, useContext, useEffect, useMemo, useRef, useState } from 'react';
import { Img, getRemotionEnvironment, useDelayRender } from 'remotion';
import { warnOnce } from './shared';

/**
 * Resources (D79–D81): logos, pictures, PDF pages and fonts a cut pins by
 * short name (`spec.resources`). Go resolves the pins into `plan.resources`
 * (name → pinned version) and lists every file the plan draws in
 * `plan.resourceSources` (SHA-256 → URL: the dashboard's same-origin media
 * route in previews, the render's loopback media server in renders). Templates
 * never see URLs or IDs, only names ("logo", "deck#3"); a nested cut's picture
 * gets its own scope (its plan's pins).
 *
 * Part 2 (project motion graphics drawing resources) reuses this: a motion
 * component's resource params resolve through the same scope.
 */
export type ResourceScope = {
  readonly resources: Readonly<Record<string, PlanResource>>;
  readonly files: Readonly<Record<string, string>>;
};

const EMPTY: ResourceScope = { resources: {}, files: {} };
/** The cut's resources; project motion graphics read it through useResource (@metarush/motion). */
export const ResourcesContext = createContext<ResourceScope>(EMPTY);

export const ResourcesProvider: React.FC<{ plan: RenderPlan | undefined; children: React.ReactNode }> = ({ plan, children }) => {
  const resources = plan?.resources;
  const files = plan?.resourceSources;
  const value = useMemo<ResourceScope>(() => ({ resources: resources ?? {}, files: files ?? {} }), [resources, files]);
  return <ResourcesContext.Provider value={value}>{children}</ResourcesContext.Provider>;
};

/** A resource file ready to draw. */
export type ResolvedResource = {
  readonly name: string;
  /** Where the drawable file is (a media URL). */
  readonly url: string;
  readonly kind: PlanResource['kind'];
  readonly width?: number;
  readonly height?: number;
  readonly title?: string;
  /** The address of the page a web page snapshot shows (the resource's `url`), if known. */
  readonly pageUrl?: string;
};

const REF = /^([a-z][a-z0-9_-]{0,47})(?:#([1-9][0-9]{0,2}))?$/;

/** Splits "name" or "name#3" (a PDF page, or one of a web page snapshot's pictures). */
export const parseResourceRef = (ref: string): { name: string; page: number } | null => {
  const m = REF.exec(ref.trim());
  const name = m?.[1];
  if (!m || !name) return null;
  return { name, page: m[2] ? Number(m[2]) : 0 };
};

/**
 * Resolves a picture reference against a scope: a picture or SVG is its pinned
 * file; a PDF draws the page asked for (page 1 when none); a web page snapshot
 * draws its own file (the desktop screenshot) for "name" and its Nth picture
 * for "name#N" (1 desktop, 2 phone, 3 the whole page). Fonts and repositories
 * are not pictures. Null when the plan has no file for it (Go's check refuses
 * that, so it only happens with hand-made plans).
 */
export const resolvePicture = (scope: ResourceScope, ref: string | undefined): ResolvedResource | null => {
  if (!ref) return null;
  const r = parseResourceRef(ref);
  if (!r) return null;
  const res = scope.resources[r.name];
  if (!res || res.kind === 'font' || res.kind === 'repo') return null;
  let sha = res.sha256;
  let width = res.width;
  let height = res.height;
  const pageNo = res.kind === 'pdf' ? Math.max(1, r.page) : res.kind === 'website' ? r.page : 0;
  if (pageNo > 0) {
    const page = res.pages?.[pageNo - 1];
    if (!page) return null;
    sha = page.sha256;
    width = page.width;
    height = page.height;
  }
  const url = scope.files[sha];
  if (typeof url !== 'string' || url === '') return null;
  return { name: r.name, url, kind: res.kind, width, height, title: res.title, pageUrl: res.url };
};

/** The picture a template param names, or null (with one warning) when the plan can't draw it. */
export const usePicture = (ref: string | undefined, what: string): ResolvedResource | null => {
  const scope = useContext(ResourcesContext);
  const pic = useMemo(() => resolvePicture(scope, ref), [scope, ref]);
  if (ref && !pic) warnOnce(`${what}: the plan has no picture for “${ref}”; drawing without it`);
  return pic;
};

/** Width and height that fit a picture inside a box, keeping its shape. */
export const fitInside = (pic: ResolvedResource, maxWidth: number, maxHeight: number): { width: number; height: number } => {
  const w = pic.width && pic.width > 0 ? pic.width : 0;
  const h = pic.height && pic.height > 0 ? pic.height : 0;
  if (!w || !h) return { width: maxWidth, height: maxHeight };
  const s = Math.min(maxWidth / w, maxHeight / h);
  return { width: Math.max(1, Math.round(w * s)), height: Math.max(1, Math.round(h * s)) };
};

/**
 * Draws a resolved picture. Renders wait for it (Remotion's Img holds the
 * frame) and fail plainly when it can't load; previews drop it and warn.
 */
export const ResourceImage: React.FC<{
  pic: ResolvedResource;
  fit?: 'contain' | 'cover';
  style?: React.CSSProperties;
}> = ({ pic, fit = 'contain', style }) => {
  const [failed, setFailed] = useState(false);
  const rendering = getRemotionEnvironment().isRendering;
  if (failed) return null;
  return (
    <Img
      src={pic.url}
      alt=""
      style={{ display: 'block', objectFit: fit, ...style }}
      onError={
        rendering
          ? undefined
          : () => {
              warnOnce(`couldn't load the picture “${pic.title ?? pic.name}”; previewing without it`);
              setFailed(true);
            }
      }
    />
  );
};

// --- brand font files ---------------------------------------------------------

const fontLoads = new Map<string, Promise<void>>();
const fontsReady = new Set<string>();

/**
 * Loads a font file as binary data and registers it. Fetching (allowed to
 * loopback by the render page's connect-src) instead of a CSS url() keeps the
 * render CSP's font-src unchanged.
 */
const loadFontFile = (family: string, url: string): Promise<void> => {
  let p = fontLoads.get(family);
  if (!p) {
    p = fetch(url)
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.arrayBuffer();
      })
      .then((buf) => new FontFace(family, buf).load())
      .then((face) => {
        document.fonts.add(face);
        fontsReady.add(family);
      });
    p.catch(() => fontLoads.delete(family));
    fontLoads.set(family, p);
  }
  return p;
};

/** The private family name a font resource is registered under. */
export const fontFamilyFor = (font: PlanResource): string => `MR Brand ${font.sha256.slice(0, 16)}`;

// --- fonts a motion graphic names (D171) ---------------------------------------

const faceLoads = new Map<string, Promise<void>>();
const facesReady = new Set<string>();

const loadFace = (key: string, family: string, url: string, desc?: FontFaceDescriptors): Promise<void> => {
  let p = faceLoads.get(key);
  if (!p) {
    p = fetch(url)
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.arrayBuffer();
      })
      .then((buf) => new FontFace(family, buf, desc).load())
      .then((face) => {
        document.fonts.add(face);
        facesReady.add(key);
      });
    p.catch(() => faceLoads.delete(key));
    faceLoads.set(key, p);
  }
  return p;
};

/**
 * Registers the cut's font resource `name` as the CSS family `family` (with
 * an optional weight range, style and unicode range) and holds the frame until
 * it has loaded: a shared library lifted from a Remotion project draws its
 * Google Fonts this way, from the project's own font resources. Returns the
 * family. Renders fail plainly when it can't load; previews keep a fallback.
 */
export const useResourceFont = (name: string, family: string, desc?: FontFaceDescriptors): string => {
  const scope = useContext(ResourcesContext);
  const res = scope.resources[name];
  const url = res && res.kind === 'font' ? scope.files[res.sha256] : undefined;
  const key = url && res ? `${family}|${res.sha256}|${JSON.stringify(desc ?? {})}` : '';
  const { delayRender, continueRender, cancelRender } = useDelayRender();
  const pending = useRef<number | null>(null);
  const [ready, setReady] = useState(() => !key || facesReady.has(key));
  if (key && !ready && pending.current === null && !facesReady.has(key)) {
    pending.current = delayRender(`Loading the font “${family}”`);
  }
  if (name && !url) warnOnce(`a motion graphic asked for the font “${name}”, which this cut has no font file for`);
  const release = () => {
    if (pending.current !== null) {
      continueRender(pending.current);
      pending.current = null;
    }
  };
  useEffect(() => {
    if (!key || !url) return;
    if (facesReady.has(key)) {
      setReady(true);
      return;
    }
    let live = true;
    loadFace(key, family, url, desc).then(
      () => (live ? setReady(true) : release()),
      (err: unknown) => {
        const msg = `MetaRush couldn't load the font “${family}” (${err instanceof Error ? err.message : String(err)})`;
        if (getRemotionEnvironment().isRendering) cancelRender(new Error(msg));
        else {
          warnOnce(`${msg}; previewing with another font`);
          release();
        }
      },
    );
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  useEffect(() => {
    if (ready) release();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready]);
  return family;
};

/**
 * The family of the plan's brand font file (`brand.fontFile`, one of its font
 * resources) once it is loaded, else null. Renders hold the frame until it is
 * ready and fail plainly when it can't load; previews keep the built-in font.
 */
export const useBrandFontFile = (plan: RenderPlan | undefined): string | null => {
  const name = plan?.brand?.fontFile;
  const font = name ? plan?.resources?.[name] : undefined;
  const url = font && font.kind === 'font' ? plan?.resourceSources?.[font.sha256] : undefined;
  const family = font && url ? fontFamilyFor(font) : null;
  const { delayRender, continueRender, cancelRender } = useDelayRender();
  const pending = useRef<number | null>(null);
  const [loaded, setLoaded] = useState<string | null>(() => (family && fontsReady.has(family) ? family : null));
  if (family && loaded !== family && pending.current === null && !fontsReady.has(family)) {
    pending.current = delayRender(`Loading the brand font “${font?.title ?? font?.family ?? name}”`);
  }
  const release = () => {
    if (pending.current !== null) {
      continueRender(pending.current);
      pending.current = null;
    }
  };

  useEffect(() => {
    if (!family || !url) return;
    if (fontsReady.has(family)) {
      setLoaded(family);
      return;
    }
    let live = true;
    loadFontFile(family, url).then(
      () => (live ? setLoaded(family) : release()),
      (err: unknown) => {
        const msg = `MetaRush couldn't load the brand font “${font?.title ?? name}” (${err instanceof Error ? err.message : String(err)})`;
        if (getRemotionEnvironment().isRendering) {
          cancelRender(new Error(msg));
        } else {
          warnOnce(`${msg}; previewing with the built-in font`);
          release();
        }
      },
    );
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [family, url]);

  useEffect(() => {
    if (loaded && loaded === family) release();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loaded, family]);

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => () => release(), []);

  return loaded && loaded === family ? family : null;
};
