import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { CaretLeft, CaretRight, X } from '@phosphor-icons/react';
import { cx } from './primitives';

/*
 * Pictures and videos, large (the Claude Code panel's stills and a motion
 * graphic's test frames and preview). ‹ › or the arrow keys step through
 * them, Esc or a click outside closes, a swipe steps on a phone. Sources are
 * same-origin media URLs, so it works over remote access too. It marks itself
 * as an open dialog, so Esc closes it and not the panel under it.
 */

export interface LightboxItem {
  src: string;
  kind?: 'image' | 'video';
  label?: string;
}

export function Lightbox({ items, index, onClose, title }: { items: LightboxItem[]; index: number; onClose: () => void; title?: string }) {
  const [i, setI] = useState(() => Math.min(Math.max(0, index), Math.max(0, items.length - 1)));
  const closeRef = useRef<HTMLButtonElement>(null);
  const restore = useRef<Element | null>(null);
  const touch = useRef<number | null>(null);
  const n = items.length;
  const item = items[i];
  const step = (d: number) => setI((v) => (n ? (v + d + n) % n : 0));

  useEffect(() => {
    restore.current = document.activeElement;
    closeRef.current?.focus();
    const prev = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.body.style.overflow = prev;
      const r = restore.current;
      if (r instanceof HTMLElement && document.contains(r)) r.focus();
    };
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        onClose();
      } else if (e.key === 'ArrowLeft' && n > 1) {
        e.preventDefault();
        e.stopPropagation();
        step(-1);
      } else if (e.key === 'ArrowRight' && n > 1) {
        e.preventDefault();
        e.stopPropagation();
        step(1);
      } else if (e.key === 'Tab') {
        // Keep focus on the lightbox's own buttons.
        const nodes = Array.from(document.querySelectorAll<HTMLElement>('[data-mr-lightbox] button, [data-mr-lightbox] video'));
        if (nodes.length === 0) return;
        const at = nodes.indexOf(document.activeElement as HTMLElement);
        e.preventDefault();
        nodes[(at + (e.shiftKey ? -1 : 1) + nodes.length) % nodes.length]?.focus();
      }
    };
    document.addEventListener('keydown', onKey, true);
    return () => document.removeEventListener('keydown', onKey, true);
  }, [n, onClose]);

  if (!item) return null;
  const label = item.label || (item.kind === 'video' ? 'Preview' : '');
  const alt = label || title || 'Picture';
  return createPortal(
    <div
      className="fixed inset-0 z-[75] flex flex-col bg-[rgb(8_8_10/0.92)] text-white"
      role="dialog"
      aria-modal="true"
      aria-label={title ?? 'Pictures, large'}
      data-mr-dialog="open"
      data-mr-lightbox
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
      onTouchStart={(e) => {
        touch.current = e.touches[0]?.clientX ?? null;
      }}
      onTouchEnd={(e) => {
        const start = touch.current;
        const end = e.changedTouches[0]?.clientX;
        touch.current = null;
        if (start == null || end == null || n < 2) return;
        if (end - start > 40) step(-1);
        else if (start - end > 40) step(1);
      }}
      style={{ animation: 'mr-fade-in 0.2s var(--ease-out-expo) both' }}
    >
      <div className="flex items-center gap-3 px-4 py-3 sm:px-6">
        <p className="min-w-0 flex-1 truncate text-[13px] text-white/80">
          {title ? <span className="font-medium text-white">{title}</span> : null}
          {title && label ? ' · ' : null}
          {label}
        </p>
        {n > 1 ? (
          <span className="tc shrink-0 text-[12px] text-white/60" aria-live="polite">
            {i + 1} of {n}
          </span>
        ) : null}
        <button
          ref={closeRef}
          type="button"
          onClick={onClose}
          aria-label="Close (Esc)"
          title="Close (Esc)"
          className="grid h-9 w-9 shrink-0 place-items-center rounded-[10px] text-white/80 hover:bg-white/10 hover:text-white focus-visible:outline-2 focus-visible:outline-white"
        >
          <X size={18} />
        </button>
      </div>
      <div
        className="relative flex min-h-0 flex-1 items-center justify-center px-2 pb-4 sm:px-16"
        onMouseDown={(e) => {
          if (e.target === e.currentTarget) onClose();
        }}
      >
        {item.kind === 'video' ? (
          <video key={item.src} src={item.src} controls autoPlay playsInline className="max-h-full max-w-full rounded-[10px] bg-black" aria-label={alt} />
        ) : (
          <img key={item.src} src={item.src} alt={alt} className="max-h-full max-w-full rounded-[10px] object-contain" draggable={false} />
        )}
        {n > 1 ? (
          <>
            <NavButton side="left" onClick={() => step(-1)} />
            <NavButton side="right" onClick={() => step(1)} />
          </>
        ) : null}
      </div>
    </div>,
    document.body,
  );
}

function NavButton({ side, onClick }: { side: 'left' | 'right'; onClick: () => void }) {
  const label = side === 'left' ? 'Previous (←)' : 'Next (→)';
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      title={label}
      className={cx(
        'absolute top-1/2 grid h-11 w-11 -translate-y-1/2 place-items-center rounded-full bg-black/45 text-white/90 backdrop-blur-sm hover:bg-black/65 hover:text-white focus-visible:outline-2 focus-visible:outline-white',
        side === 'left' ? 'left-2 sm:left-4' : 'right-2 sm:right-4',
      )}
    >
      {side === 'left' ? <CaretLeft size={20} weight="bold" /> : <CaretRight size={20} weight="bold" />}
    </button>
  );
}
