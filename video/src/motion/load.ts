import { motionHost, type MotionEntry } from './contract';

const loading = new Map<string, Promise<MotionEntry>>();

/**
 * Loads a motion graphic's bundle (served by the renderer next to the page)
 * once per page and resolves with what it registered.
 */
export const loadMotionBundle = (hash: string, url: string): Promise<MotionEntry> => {
  const cached = loading.get(hash);
  if (cached) return cached;
  const p = new Promise<MotionEntry>((resolve, reject) => {
    const host = motionHost();
    if (!host) {
      reject(new Error('[metarush] this page has no motion graphic host'));
      return;
    }
    if (host.entries[hash]) {
      resolve(host.entries[hash]!);
      return;
    }
    if (!/^https?:\/\/(127\.0\.0\.1|localhost):\d+\//.test(url)) {
      reject(new Error(`[metarush] motion graphic ${hash.slice(0, 8)} has no bundle served by the renderer`));
      return;
    }
    const s = document.createElement('script');
    s.src = url;
    s.async = true;
    s.onload = () => {
      const e = host.entries[hash];
      if (e) resolve(e);
      else reject(new Error(`[metarush] motion graphic ${hash.slice(0, 8)} did not register itself`));
    };
    s.onerror = () => reject(new Error(`[metarush] motion graphic ${hash.slice(0, 8)} could not be loaded`));
    document.head.appendChild(s);
  });
  loading.set(hash, p);
  return p;
};
