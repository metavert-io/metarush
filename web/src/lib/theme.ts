import { useSyncExternalStore } from 'react';

export type Theme = 'dark' | 'light';
const KEY = 'metarush.theme';
const subs = new Set<() => void>();

function read(): Theme {
  const attr = typeof document !== 'undefined' ? document.documentElement.getAttribute('data-theme') : null;
  return attr === 'light' ? 'light' : 'dark';
}

export function setTheme(t: Theme) {
  document.documentElement.setAttribute('data-theme', t);
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', t === 'light' ? '#f2f1ee' : '#0d0c0b');
  try {
    localStorage.setItem(KEY, t);
  } catch {
    // storage unavailable (private mode): the choice lasts for this tab only
  }
  subs.forEach((fn) => fn());
}

export function useTheme(): [Theme, (t: Theme) => void] {
  const theme = useSyncExternalStore(
    (fn) => {
      subs.add(fn);
      return () => subs.delete(fn);
    },
    read,
    () => 'dark' as Theme,
  );
  return [theme, setTheme];
}

/** Small persisted UI preferences (wrapped: storage may be unavailable). */
export function loadPref<T>(key: string, fallback: T): T {
  try {
    const v = localStorage.getItem(`metarush.${key}`);
    return v === null ? fallback : (JSON.parse(v) as T);
  } catch {
    return fallback;
  }
}
export function savePref(key: string, value: unknown) {
  try {
    localStorage.setItem(`metarush.${key}`, JSON.stringify(value));
  } catch {
    // ignore
  }
}
