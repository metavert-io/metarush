import * as React from 'react';
import * as ReactDOM from 'react-dom';
import * as JSXRuntime from 'react/jsx-runtime';
import * as Remotion from 'remotion';
import * as NoReact from 'remotion/no-react';
import * as Api from './api';
import type { MotionHost } from './contract';

/**
 * Installs the motion graphic host on the render page (imported only by the
 * render entry, src/index.ts → Root.tsx; never by the dashboard's lib.ts).
 */
export const installMotionHost = (): MotionHost => {
  if (globalThis.__metarushMotion) return globalThis.__metarushMotion;
  const host: MotionHost = {
    modules: {
      react: React,
      'react/jsx-runtime': JSXRuntime,
      'react/jsx-dev-runtime': JSXRuntime,
      'react-dom': ReactDOM,
      remotion: Remotion,
      'remotion/no-react': NoReact,
      '@metarush/motion': Api,
    },
    entries: {},
    register(hash, entry) {
      if (!/^[0-9a-f]{64}$/.test(hash)) throw new Error('[metarush] a motion graphic registered without a valid version');
      host.entries[hash] = entry;
    },
  };
  globalThis.__metarushMotion = host;
  // Network attempts the page's CSP blocked (the proxy jail blocks the rest):
  // logged so the renderer can report them, never sent anywhere.
  if (typeof document !== 'undefined') {
    document.addEventListener('securitypolicyviolation', (e) => {
      console.error(`[metarush] blocked: ${e.violatedDirective} ${String(e.blockedURI).slice(0, 200)}`);
    });
  }
  return host;
};
