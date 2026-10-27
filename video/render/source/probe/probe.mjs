// MetaRush's probe for the project map (D167): small React components a map
// bundle puts around a Remotion source's compositions and scenes. They draw
// nothing and change nothing; they report, as console lines the renderer
// collects, where each scene is on the composition's timeline (frames resolved
// while the code runs, so lengths computed in code are exact) and, when asked,
// which words are on screen and whether any of them spill out of their box.
//
// This file is MetaRush's code, copied next to the project's files only for a
// map run; it runs in the same jailed page as the project's code. Its imports
// resolve to the project's own React and Remotion.
import React from 'react';
import { useCurrentFrame, useVideoConfig, delayRender, continueRender } from 'remotion';

const h = React.createElement;
const RootFrame = React.createContext(null);
const ParentKey = React.createContext('');

/** What the renderer asked this run to report ("scenes", "text"), from its environment variables. */
const mode = () => {
  try {
    const env = (globalThis.process && globalThis.process.env) || {};
    return String(env.METARUSH_PROBE || '');
  } catch {
    return '';
  }
};

const said = new Set();
const say = (kind, o) => {
  let s;
  try {
    s = JSON.stringify(o);
  } catch {
    return;
  }
  const k = kind + s;
  if (said.has(k)) return;
  said.add(k);
  console.log(`[metarush-${kind}] ${s}`);
};

/** A component's name as written in the code (function or displayName). */
export const nameOf = (type) => {
  if (!type) return '';
  if (typeof type === 'string') return type;
  return String(type.displayName || type.name || (type.render && (type.render.displayName || type.render.name)) || (type.type && nameOf(type.type)) || '');
};

/** The first element child's component name ("Hook" for <Hook scene={s} />). */
export const childName = (children) => {
  let out = '';
  React.Children.forEach(children, (c) => {
    if (out || !React.isValidElement(c)) return;
    if (c.type === React.Fragment) out = childName(c.props.children);
    else out = nameOf(c.type);
  });
  return out;
};

/** Children that are only sound (<Audio>, <Html5Audio>): a sequence that places a sound, not a scene. */
const onlySound = (children, isSound) => {
  let any = false;
  let other = false;
  React.Children.forEach(children, (c) => {
    if (!React.isValidElement(c)) {
      if (c !== null && c !== undefined && c !== false && String(c).trim() !== '') other = true;
      return;
    }
    if (c.type === React.Fragment) {
      const r = onlySound(c.props.children, isSound);
      if (r === null) other = true;
      else any = any || r;
      return;
    }
    if (isSound(c.type)) any = true;
    else other = true;
  });
  return other ? null : any;
};

/** True when the children are only sound elements. */
export const onlySoundOf = (children, isSound) => onlySound(children, isSound) === true;

/** Words on screen and whether they fit, measured after the frame is drawn ("text" mode). */
const measure = (frame) => {
  const W = document.documentElement.clientWidth || window.innerWidth;
  const H = document.documentElement.clientHeight || window.innerHeight;
  const items = [];
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n && items.length < 400; n = walker.nextNode()) {
    const text = String(n.textContent || '').replace(/\s+/g, ' ').trim();
    if (text.length < 1 || !/\p{L}|\p{N}/u.test(text)) continue;
    const el = n.parentElement;
    if (!el || el.closest('script,style,noscript,title')) continue;
    let opacity = 1;
    let hidden = false;
    for (let e = el; e && e !== document.documentElement; e = e.parentElement) {
      const cs = getComputedStyle(e);
      if (cs.display === 'none' || cs.visibility === 'hidden') hidden = true;
      opacity *= Number(cs.opacity || 1);
    }
    if (hidden || opacity < 0.05) continue;
    let rects = [];
    try {
      const r = document.createRange();
      r.selectNodeContents(n);
      rects = [...r.getClientRects()].filter((x) => x.width > 0 && x.height > 0);
    } catch {
      rects = [];
    }
    if (!rects.length) {
      const b = el.getBoundingClientRect();
      if (b.width > 0 && b.height > 0) rects = [b];
    }
    if (!rects.length) continue;
    const x0 = Math.min(...rects.map((r) => r.left));
    const y0 = Math.min(...rects.map((r) => r.top));
    const x1 = Math.max(...rects.map((r) => r.right));
    const y1 = Math.max(...rects.map((r) => r.bottom));
    const lines = new Set(rects.map((r) => Math.round(r.top))).size;
    const flags = [];
    if (x0 < -1 || y0 < -1 || x1 > W + 1 || y1 > H + 1) flags.push('outside');
    for (let e = el; e && e !== document.body; e = e.parentElement) {
      const cs = getComputedStyle(e);
      const clips = /hidden|clip|auto|scroll/.test(cs.overflow + cs.overflowX + cs.overflowY);
      if (!clips) continue;
      const b = e.getBoundingClientRect();
      if (x0 < b.left - 1 || y0 < b.top - 1 || x1 > b.right + 1 || y1 > b.bottom + 1) {
        flags.push('clipped');
        break;
      }
    }
    const ecs = getComputedStyle(el);
    if (el.scrollWidth > el.clientWidth + 1 && /ellipsis/.test(ecs.textOverflow)) flags.push('cut short');
    const font = String(ecs.fontFamily || '').split(',')[0].replace(/["']/g, '').trim();
    items.push({ text: text.slice(0, 300), x: Math.round(x0), y: Math.round(y0), w: Math.round(x1 - x0), h: Math.round(y1 - y0), lines, opacity: Math.round(opacity * 100) / 100, flags, font, size: Math.round(parseFloat(ecs.fontSize) || 0) });
  }
  // The project's own pictures and videos on screen (public/ files).
  const pics = [];
  const seenPic = new Set();
  const media = [...document.querySelectorAll('img,video,image')];
  for (const m of media) {
    if (pics.length >= 60) break;
    const src = m.currentSrc || m.getAttribute('src') || m.getAttribute('href') || m.getAttribute('xlink:href') || '';
    const file = publicFile(src);
    if (!file || seenPic.has(file)) continue;
    const b = m.getBoundingClientRect();
    if (b.width < 2 || b.height < 2 || b.right < 0 || b.bottom < 0 || b.left > W || b.top > H) continue;
    let opacity = 1;
    for (let e = m; e && e !== document.documentElement; e = e.parentElement) opacity *= Number(getComputedStyle(e).opacity || 1);
    if (opacity < 0.05) continue;
    seenPic.add(file);
    pics.push({ file, video: m.tagName.toLowerCase() === 'video' });
  }
  return { frame, width: W, height: H, items, pics };
};

/**
 * A picture's file in the project's public folder, from the address the page
 * used; a picture a person put in place of the project's own (D209) keeps its
 * served name (__metarush_pictures/<sha256>.png), so a check can see it drawn.
 */
const publicFile = (src) => {
  try {
    const p = decodeURIComponent(new URL(src, location.href).pathname).replace(/^\/+/, '');
    if (/^__metarush_pictures\/[0-9a-f]{64}\.[a-z]+$/.test(p)) return p;
    const m = /^(?:static-[^/]+|public)\/(.+)$/.exec(p);
    return m ? m[1] : '';
  } catch {
    return '';
  }
};

/** Around a composition: the absolute frame for every scene probe inside, and the words when asked. */
export const RootProbe = ({ children }) => {
  const frame = useCurrentFrame();
  const m = mode();
  React.useLayoutEffect(() => {
    if (m !== 'text') return undefined;
    const handle = delayRender('MetaRush reads the words on screen');
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      try {
        say('text', measure(frame));
      } catch (e) {
        say('text', { frame, error: String(e && e.message ? e.message : e) });
      }
      continueRender(handle);
    };
    const ready = document.fonts && document.fonts.ready ? document.fonts.ready : Promise.resolve();
    ready.then(() => requestAnimationFrame(() => requestAnimationFrame(finish)), finish);
    const t = setTimeout(finish, 4000);
    return () => clearTimeout(t);
  }, [frame, m]);
  return h(RootFrame.Provider, { value: frame }, children);
};

/** Inside a scene (a sequence's content): where it is on the composition's timeline. */
export const SceneProbe = ({ meta, children }) => {
  const abs = React.useContext(RootFrame);
  const parent = React.useContext(ParentKey);
  const rel = useCurrentFrame();
  const { durationInFrames } = useVideoConfig();
  // Where it starts on the composition's timeline: the frame drawn minus the
  // frame inside it (exact while rendering: Remotion premounts nothing then).
  const from = abs === null ? null : abs - rel;
  const key = `${parent}/${meta.kind}:${meta.name || meta.component || ''}@${from}`;
  if (from !== null && mode() === 'scenes') {
    say('scene', { key, parent, kind: meta.kind, index: meta.index, name: meta.name || '', component: meta.component || '', sound: !!meta.sound, from, duration: durationInFrames });
  }
  return h(ParentKey.Provider, { value: key }, children);
};

/** Puts a probe inside every sequence-like child (Series.Sequence, TransitionSeries.Sequence), flattening fragments. */
export const probeChildren = (children, isItem, kind, isSound) => {
  let index = 0;
  const map = (cs) =>
    React.Children.map(cs, (c) => {
      if (!React.isValidElement(c)) return c;
      if (c.type === React.Fragment) return h(React.Fragment, { key: c.key }, map(c.props.children));
      if (!isItem(c.type)) return c;
      const i = index++;
      const meta = { kind, index: i, name: typeof c.props.name === 'string' ? c.props.name : '', component: childName(c.props.children), sound: onlySound(c.props.children, isSound) === true };
      return React.cloneElement(c, {}, h(SceneProbe, { meta }, c.props.children));
    });
  return map(children);
};

/** A component wrapped once (the same wrapper every time, so Remotion doesn't see a new composition). */
const wrapped = new WeakMap();
export const withRoot = (C) => {
  if (!C || (typeof C !== 'function' && typeof C !== 'object')) return C;
  let w = wrapped.get(C);
  if (!w) {
    w = (props) => h(RootProbe, null, h(C, props));
    w.displayName = nameOf(C) || 'Composition';
    wrapped.set(C, w);
  }
  return w;
};

const wrappedLazy = new WeakMap();
export const withRootLazy = (lazy) => {
  if (typeof lazy !== 'function') return lazy;
  let w = wrappedLazy.get(lazy);
  if (!w) {
    w = () => Promise.resolve(lazy()).then((m) => ({ ...m, default: withRoot(m && m.default) }));
    wrappedLazy.set(lazy, w);
  }
  return w;
};
