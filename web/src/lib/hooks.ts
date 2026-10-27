import { useEffect, useRef, useState } from 'react';

/** Re-render every `ms` (for relative timestamps). */
export function useNow(ms = 5000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms]);
  return now;
}

export function isTypingTarget(el: EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false;
  if (el.isContentEditable) return true;
  const tag = el.tagName;
  if (tag === 'TEXTAREA' || tag === 'SELECT') return true;
  if (tag === 'INPUT') {
    const type = (el as HTMLInputElement).type;
    return !['checkbox', 'radio', 'button', 'submit', 'range'].includes(type);
  }
  return false;
}

/** True while a modal dialog is open (dialogs own the keyboard then). */
export function isDialogOpen(): boolean {
  return !!document.querySelector('[data-mr-dialog="open"]');
}

/**
 * Single-key shortcuts ignored while typing or while a dialog is open.
 * `keys` maps KeyboardEvent.key (e.g. "/", "j", " ") to handlers.
 */
export function useHotkeys(keys: Record<string, (e: KeyboardEvent) => void>, enabled = true) {
  const ref = useRef(keys);
  ref.current = keys;
  useEffect(() => {
    if (!enabled) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      if (isTypingTarget(e.target) || isDialogOpen()) return;
      const fn = ref.current[e.key];
      if (fn) fn(e);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [enabled]);
}

/**
 * j/k list navigation. Returns the active index and a setter; Enter calls onOpen.
 * Items should render `data-active` and be scrolled into view by the caller.
 */
export function useListNav(count: number, onOpen?: (index: number) => void, enabled = true) {
  const [active, setActive] = useState(-1);
  useEffect(() => {
    if (active >= count) setActive(count - 1);
  }, [count, active]);
  useHotkeys(
    {
      j: (e) => {
        e.preventDefault();
        setActive((i) => Math.min(count - 1, i + 1));
      },
      k: (e) => {
        e.preventDefault();
        setActive((i) => Math.max(0, i - 1));
      },
      Enter: (e) => {
        if (active >= 0 && onOpen && !(e.target instanceof HTMLButtonElement) && !(e.target instanceof HTMLAnchorElement)) {
          e.preventDefault();
          onOpen(active);
        }
      },
    },
    enabled && count > 0,
  );
  return [active, setActive] as const;
}

/** Scroll an element into view when it becomes active. */
export function useScrollIntoView<T extends HTMLElement>(active: boolean) {
  const ref = useRef<T>(null);
  useEffect(() => {
    if (active) ref.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }, [active]);
  return ref;
}

export function useMediaQuery(q: string): boolean {
  const [m, setM] = useState(() => typeof window !== 'undefined' && window.matchMedia(q).matches);
  useEffect(() => {
    const mq = window.matchMedia(q);
    const on = () => setM(mq.matches);
    mq.addEventListener('change', on);
    on();
    return () => mq.removeEventListener('change', on);
  }, [q]);
  return m;
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    // Fallback for non-secure contexts
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try {
      ok = document.execCommand('copy');
    } catch {
      ok = false;
    }
    ta.remove();
    return ok;
  }
}
