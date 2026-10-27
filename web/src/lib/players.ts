import { useCallback, useEffect, useRef } from 'react';
import { isDialogOpen, isTypingTarget } from './hooks';

/*
 * Space toggles play/pause on the most recently used (or most recently mounted)
 * player on the page: the source viewer, the cut preview or a render.
 */

export interface PlayerHandle {
  toggle(): void;
}

const stack: { id: number; handle: PlayerHandle }[] = [];
let seq = 0;
let listening = false;

function onKey(e: KeyboardEvent) {
  if (e.key !== ' ' || e.metaKey || e.ctrlKey || e.altKey) return;
  if (isTypingTarget(e.target) || isDialogOpen()) return;
  const t = e.target as HTMLElement | null;
  // Let focused buttons/links/tabs keep their native Space activation.
  if (t && (t.closest('button, a, [role="button"], [role="tab"], [role="slider"], summary') || t.isContentEditable)) {
    if (!t.closest('[data-player-surface]')) return;
  }
  const top = stack[stack.length - 1];
  if (!top) return;
  e.preventDefault();
  top.handle.toggle();
}

export function usePlayerHotkey(handle: PlayerHandle | null, enabled = true) {
  const id = useRef(0);
  const ref = useRef(handle);
  ref.current = handle;
  useEffect(() => {
    if (!enabled) return;
    if (!listening) {
      window.addEventListener('keydown', onKey);
      listening = true;
    }
    id.current = ++seq;
    const entry = { id: id.current, handle: { toggle: () => ref.current?.toggle() } };
    stack.push(entry);
    return () => {
      const i = stack.findIndex((x) => x.id === entry.id);
      if (i >= 0) stack.splice(i, 1);
    };
  }, [enabled]);
  /** Call on user interaction so this player becomes the Space target (stable identity). */
  return useCallback(() => {
    const i = stack.findIndex((x) => x.id === id.current);
    if (i >= 0 && i !== stack.length - 1) stack.push(...stack.splice(i, 1));
  }, []);
}
