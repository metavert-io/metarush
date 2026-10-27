// What a project motion graphic may import (D68). The scan (scan.mjs) applies
// this to the parsed source before anything is bundled; the bundler plugin
// (bundle.mjs) applies it again while resolving.
import { builtinModules, createRequire } from 'node:module';

const require = createRequire(import.meta.url);

/** Modules the render page provides itself, so a component shares its React and Remotion. */
export const SHARED = ['react', 'react/jsx-runtime', 'react/jsx-dev-runtime', 'react-dom', 'remotion', 'remotion/no-react', '@metarush/motion'];

/** The curated packages a component may import (MetaRush's own pinned versions). */
export const PACKAGES = ['zod', '@remotion/zod-types', '@remotion/shapes', '@remotion/paths', '@remotion/layout-utils', '@remotion/transitions'];

/** @remotion/transitions presentations (from the package's own exports). */
export const TRANSITION_SUBPATHS = Object.keys(require('@remotion/transitions/package.json').exports ?? {})
  .filter((k) => k.startsWith('./') && k !== './package.json')
  .map((k) => '@remotion/transitions/' + k.slice(2));

const NODE = new Set(builtinModules.flatMap((m) => [m, 'node:' + m]));

export const ALLOWED_LIST = 'remotion, react, zod, @remotion/zod-types, @remotion/shapes, @remotion/paths, @remotion/layout-utils, @remotion/transitions, @metarush/motion and the project\'s shared libraries (@metarush/lib/<name>)';

/**
 * TypeScript settings a component may declare (D171): code lifted from a
 * Remotion project is checked the way its project checked it, so a graphic's
 * motion.json or a library's library.json may say { "typescript": {
 * "noUncheckedIndexedAccess": false } } — only relaxations listed here.
 */
export const TS_SETTINGS = { noUncheckedIndexedAccess: [false] };
export const SETTINGS_FILE = 'motion.json';

/** The TypeScript settings files declare: {settings, problems}. */
export const tsSettingsOf = (files) => {
  const problems = [];
  const settings = {};
  for (const name of [SETTINGS_FILE, 'library.json']) {
    const f = files.find((x) => x.path === name);
    if (!f) continue;
    let doc;
    try {
      doc = JSON.parse(f.text);
    } catch {
      if (name === SETTINGS_FILE) problems.push({ file: name, message: `${name} isn't valid JSON.` });
      continue;
    }
    if (name === SETTINGS_FILE) {
      for (const k of Object.keys(doc ?? {})) if (k !== 'typescript') problems.push({ file: name, message: `${name} can only hold TypeScript settings ("typescript"), not "${k}".` });
    }
    const t = doc?.typescript;
    if (t === undefined) continue;
    if (!t || typeof t !== 'object' || Array.isArray(t)) {
      problems.push({ file: name, message: `"typescript" in ${name} lists settings: { "noUncheckedIndexedAccess": false }.` });
      continue;
    }
    for (const [k, v] of Object.entries(t)) {
      if (!Object.prototype.hasOwnProperty.call(TS_SETTINGS, k) || !TS_SETTINGS[k].includes(v)) {
        problems.push({ file: name, message: `${name} can't set the TypeScript setting ${k} to ${JSON.stringify(v)}; a component can only relax noUncheckedIndexedAccess (false), as projects it is lifted from often do.` });
        continue;
      }
      settings[k] = v;
    }
  }
  return { settings, problems };
};

/** A shared library a graphic imports (D171): @metarush/lib/<name>. */
export const LIB_PREFIX = '@metarush/lib/';
export const LIB_NAME = /^[a-z][a-z0-9-]{0,39}$/;

/**
 * Classifies an import a component wrote. Returns {kind} for an allowed one
 * (remotion, react, package, metarush, relative) or {problem, hint} for one
 * that is refused.
 */
export const classify = (spec) => {
  if (typeof spec !== 'string' || spec === '') return { problem: 'This import has no module name.' };
  if (spec === 'remotion') return { kind: 'remotion' };
  if (spec === 'react' || spec === 'react/jsx-runtime') return { kind: 'react' };
  if (spec === '@metarush/motion') return { kind: 'metarush' };
  if (spec.startsWith(LIB_PREFIX)) {
    const name = spec.slice(LIB_PREFIX.length);
    if (LIB_NAME.test(name)) return { kind: 'library', name };
    return { problem: `${spec} isn't a shared library's name.`, hint: 'A shared library is imported as @metarush/lib/<name>, its name in lowercase letters, digits and dashes.' };
  }
  if (PACKAGES.includes(spec) || TRANSITION_SUBPATHS.includes(spec)) return { kind: 'package' };
  if (spec.startsWith('./') || spec.startsWith('../')) return { kind: 'relative' };
  if (NODE.has(spec) || NODE.has(spec.split('/')[0])) {
    return {
      problem: `${spec} is part of Node.js. A motion graphic runs inside the renderer's browser, where files, programs and the network can't be reached.`,
      hint: 'Draw with remotion and react; pass anything the graphic needs as its parameters.',
    };
  }
  if (/^[a-z][a-z0-9+.-]*:/i.test(spec) || spec.startsWith('/') || spec.startsWith('\\')) {
    return { problem: `${spec} isn't a module a motion graphic may import (no URLs or absolute paths).`, hint: 'Import from ' + ALLOWED_LIST + ', or a file in the component folder.' };
  }
  if (spec === 'react-dom' || spec.startsWith('react-dom/') || spec.startsWith('remotion/')) {
    return { problem: `${spec} is part of the renderer itself; a motion graphic imports remotion and react only.`, hint: 'Import from ' + ALLOWED_LIST + '.' };
  }
  return {
    problem: `${spec} isn't one of the packages a motion graphic may use, and a component can't add packages.`,
    hint: "Allowed: ' + ALLOWED_LIST + ', and files in the component folder. Vetted templates cover the common cases.",
  };
};

/**
 * Globals a component may not use (by name). The scan refuses them in value
 * positions; the render page is network-jailed anyway (jail.mjs), so a
 * computed lookup that slips past the scan still reaches nothing.
 */
export const FORBIDDEN_GLOBALS = {
  eval: 'eval runs text as code; a motion graphic can only run its own reviewed source.',
  Function: 'new Function runs text as code; a motion graphic can only run its own reviewed source.',
  require: 'require loads modules at run time; import what you need at the top of the file (from the allowed packages).',
  importScripts: 'importScripts loads code from elsewhere; a motion graphic can only run its own reviewed source.',
  Worker: 'Workers run code outside the frame being drawn; keep everything in the component.',
  SharedWorker: 'Workers run code outside the frame being drawn; keep everything in the component.',
  WebAssembly: "WebAssembly runs compiled code a person can't review; write the graphic in TypeScript.",
  fetch: "A motion graphic can't use the network; pass what it needs as parameters.",
  XMLHttpRequest: "A motion graphic can't use the network; pass what it needs as parameters.",
  WebSocket: "A motion graphic can't use the network; pass what it needs as parameters.",
  WebTransport: "A motion graphic can't use the network; pass what it needs as parameters.",
  EventSource: "A motion graphic can't use the network; pass what it needs as parameters.",
  RTCPeerConnection: "A motion graphic can't open network connections.",
  webkitRTCPeerConnection: "A motion graphic can't open network connections.",
  RTCDataChannel: "A motion graphic can't open network connections.",
  sendBeacon: "A motion graphic can't use the network.",
};

/** Non-deterministic sources: allowed, but every frame must draw the same each time. */
export const NONDETERMINISTIC = {
  'Math.random': 'Math.random() draws something different every time a frame is rendered (frames render in parallel, in any order). Use random("some-seed") from remotion.',
  'Date.now': 'The clock differs every time a frame is rendered. Use useCurrentFrame() for anything that changes over time.',
  'performance.now': 'The clock differs every time a frame is rendered. Use useCurrentFrame() for anything that changes over time.',
  'new Date': 'The clock differs every time a frame is rendered. Use useCurrentFrame() for anything that changes over time.',
};
