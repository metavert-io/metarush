import { useCallback, useEffect, useRef, useState } from 'react';
import { isTypingTarget } from './hooks';

/*
 * Selecting words in a transcript, the way text is selected: click a word (or
 * tap it), drag across words, Shift-click to extend; the arrow keys move and
 * Shift+arrows extend; Delete or Backspace acts on the selection; Escape
 * clears it. On a touch screen, tapping a second word selects up to it. Words
 * are elements with data-wi="<index>" inside the container; anything else in
 * it (speaker chips, buttons, the "who says this?" panel) keeps working as usual.
 */

export interface WordSelect {
  /** The selected words (inclusive), or null. */
  range: [number, number] | null;
  /** The word the arrow keys move from. */
  focus: number | null;
  select(anchor: number, focus?: number): void;
  clear(): void;
  containerProps: {
    onPointerDown: (e: React.PointerEvent<HTMLElement>) => void;
    onKeyDown: (e: React.KeyboardEvent<HTMLElement>) => void;
  };
  /** True right after a touch tap selected one word (the next tap extends). */
  extending: boolean;
  /** True while words are being dragged across (a bar over the words steps aside). */
  dragging: boolean;
}

export function useWordSelect(
  count: number,
  opts: {
    /** A plain click or tap on a word (seek there). */
    onActivate?: (n: number) => void;
    onDelete?: (range: [number, number]) => void;
    /** Enter: play the selection. */
    onPlay?: (range: [number, number]) => void;
    /** Where the arrow keys start when nothing is selected (e.g. the word at the playhead). */
    startAt?: () => number;
    /** Other keys (space to play, undo…), when the selection did not use them. */
    onOtherKey?: (e: React.KeyboardEvent<HTMLElement>) => boolean;
  },
): WordSelect {
  const [anchor, setAnchor] = useState<number | null>(null);
  const [focus, setFocus] = useState<number | null>(null);
  const [touchSingle, setTouchSingle] = useState(false);
  const [dragging, setDragging] = useState(false);
  const optsRef = useRef(opts);
  optsRef.current = opts;
  const state = useRef({ anchor, focus });
  state.current = { anchor, focus };

  // Words come and go as the cut changes: keep the selection inside them.
  useEffect(() => {
    if (anchor !== null && anchor >= count) {
      setAnchor(null);
      setFocus(null);
    } else if (focus !== null && focus >= count) setFocus(count - 1);
  }, [count, anchor, focus]);

  const select = useCallback((a: number, f?: number) => {
    setAnchor(a);
    setFocus(f ?? a);
  }, []);
  const clear = useCallback(() => {
    setAnchor(null);
    setFocus(null);
    setTouchSingle(false);
  }, []);

  const wordAt = (el: Element | null, root: HTMLElement): number | null => {
    const w = el?.closest<HTMLElement>('[data-wi]');
    if (!w || !root.contains(w)) return null;
    const n = Number(w.dataset.wi);
    return Number.isFinite(n) ? n : null;
  };

  const onPointerDown = (e: React.PointerEvent<HTMLElement>) => {
    if (e.button !== 0) return;
    const root = e.currentTarget;
    const n = wordAt(e.target as Element, root);
    if (n === null) return;
    const { anchor: a, focus: f } = state.current;
    if (e.pointerType === 'touch') {
      // A tap (not a scroll): select the word, or up to it after a one-word selection.
      const x0 = e.clientX;
      const y0 = e.clientY;
      const up = (ev: PointerEvent) => {
        window.removeEventListener('pointerup', up);
        window.removeEventListener('pointercancel', cancel);
        if (Math.hypot(ev.clientX - x0, ev.clientY - y0) > 10) return;
        if (touchSingle && a !== null && a === f && n !== a) {
          setFocus(n);
          setTouchSingle(false);
        } else {
          select(n);
          setTouchSingle(true);
          optsRef.current.onActivate?.(n);
        }
      };
      const cancel = () => {
        window.removeEventListener('pointerup', up);
        window.removeEventListener('pointercancel', cancel);
      };
      window.addEventListener('pointerup', up);
      window.addEventListener('pointercancel', cancel);
      return;
    }
    e.preventDefault(); // no native text selection while dragging
    root.focus({ preventScroll: true });
    setTouchSingle(false);
    const start = e.shiftKey && a !== null ? a : n;
    setAnchor(start);
    setFocus(n);
    let moved = false;
    let dragged = false;
    const x0 = e.clientX;
    const y0 = e.clientY;
    const move = (ev: PointerEvent) => {
      // A real drag (not a click): bars over the words step aside until it ends.
      if (!dragged && Math.hypot(ev.clientX - x0, ev.clientY - y0) > 3) {
        dragged = true;
        setDragging(true);
      }
      const m = wordAt(document.elementFromPoint(ev.clientX, ev.clientY), root);
      if (m !== null && m !== state.current.focus) {
        moved = true;
        setFocus(m);
      }
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      window.removeEventListener('pointercancel', up);
      setDragging(false);
      if (!moved && !e.shiftKey) optsRef.current.onActivate?.(n);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
    window.addEventListener('pointercancel', up);
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLElement>) => {
    if (isTypingTarget(e.target) || count === 0) return;
    const root = e.currentTarget;
    // A button or link inside (a speaker chip) keeps its own keys.
    if (e.target !== root && (e.target as HTMLElement).closest('button, a, [role="button"]')) return;
    const { anchor: a, focus: f } = state.current;
    const handled = () => {
      e.preventDefault();
      e.stopPropagation();
    };
    const range: [number, number] | null = a !== null && f !== null ? [Math.min(a, f), Math.max(a, f)] : null;
    const cmd = e.metaKey || e.ctrlKey;
    const moveTo = (n: number) => {
      const t = Math.max(0, Math.min(count - 1, n));
      if (e.shiftKey && a !== null) setFocus(t);
      else select(t);
      setTouchSingle(false);
      requestAnimationFrame(() => {
        root.querySelector<HTMLElement>(`[data-wi="${t}"]`)?.scrollIntoView({ block: 'nearest' });
      });
    };
    switch (e.key) {
      case 'ArrowLeft':
      case 'ArrowRight': {
        if (cmd || e.altKey) break;
        handled();
        const d = e.key === 'ArrowLeft' ? -1 : 1;
        moveTo(f === null ? (optsRef.current.startAt?.() ?? 0) : f + d);
        return;
      }
      case 'Home':
      case 'End':
        handled();
        moveTo(e.key === 'Home' ? 0 : count - 1);
        return;
      case 'Escape':
        if (!range) break;
        handled();
        clear();
        return;
      case 'Delete':
      case 'Backspace':
        if (!range || cmd) break;
        handled();
        optsRef.current.onDelete?.(range);
        return;
      case 'Enter':
        if (!range) break;
        handled();
        optsRef.current.onPlay?.(range);
        return;
      case 'a':
      case 'A':
        if (!cmd) break;
        handled();
        select(0, count - 1);
        return;
    }
    if (optsRef.current.onOtherKey?.(e)) handled();
  };

  const range: [number, number] | null = anchor !== null && focus !== null ? [Math.min(anchor, focus), Math.max(anchor, focus)] : null;
  return { range, focus, select, clear, containerProps: { onPointerDown, onKeyDown }, extending: touchSingle && !!range && range[0] === range[1], dragging };
}
