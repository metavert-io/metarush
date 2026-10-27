import { useEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from 'react';
import { CaretLeft, CaretRight } from '@phosphor-icons/react';
import type { Conversation } from '../../api/types';
import { cx } from '../../ui/primitives';
import { forSomeoneElse } from './people';

/*
 * By the message box: how many cards and questions in this conversation wait
 * for the person, and ‹ › that jump to each (scrolled into view and briefly
 * highlighted), so nothing has to be hunted for in a long thread. Only their
 * own (D179: the cards Claude raised answering their messages); cards for
 * someone else show in the thread, marked with whom they wait for.
 */

/**
 * The cards and questions waiting for the person (me), in the thread's order
 * ("Keep this?" cards count too: they run an op, and each marks itself with
 * data-waiting like a question card, D166).
 */
export function waitingIds(conv: Conversation | undefined, me: string): string[] {
  return (conv?.actions ?? []).filter((a) => a.state === 'pending' && (!!a.op || !!a.question) && !forSomeoneElse(a, me)).map((a) => a.id);
}

export function Navigator({ conv, me, scroller }: { conv?: Conversation; me: string; scroller: RefObject<HTMLDivElement | null> }) {
  const ids = useMemo(() => waitingIds(conv, me), [conv, me]);
  const [at, setAt] = useState(-1);
  const last = useRef<HTMLElement | null>(null);
  useEffect(() => {
    if (at >= ids.length) setAt(ids.length - 1);
  }, [ids.length, at]);
  if (!conv || ids.length === 0) return null;

  // The places in the thread, in order (a card can show twice: on its own
  // inside a combined card's open row, so each card counts once).
  const anchors = (): HTMLElement[] => {
    const root = scroller.current;
    if (!root) return [];
    const seen = new Set<string>();
    const out: HTMLElement[] = [];
    for (const el of Array.from(root.querySelectorAll<HTMLElement>('[data-waiting]'))) {
      const id = el.dataset.waiting ?? '';
      if (!id || seen.has(id) || !ids.includes(id)) continue;
      seen.add(id);
      out.push(el);
    }
    return out;
  };
  const go = (d: number) => {
    const list = anchors();
    if (!list.length) return;
    const next = at < 0 ? (d > 0 ? 0 : list.length - 1) : (at + d + list.length) % list.length;
    setAt(next);
    const el = list[next]!;
    const reduce = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
    el.scrollIntoView({ block: 'center', behavior: reduce ? 'auto' : 'smooth' });
    last.current?.classList.remove('mr-flash');
    // Restart the glow even when the same card is picked again.
    el.classList.remove('mr-flash');
    void el.offsetWidth;
    el.classList.add('mr-flash');
    last.current = el;
    window.setTimeout(() => el.classList.remove('mr-flash'), 1400);
    const focusable = el.querySelector<HTMLElement>('button:not([disabled]), input:not([disabled]), textarea');
    focusable?.focus({ preventScroll: true });
  };
  const n = ids.length;
  const label = n === 1 ? '1 thing waits for you' : `${n} things wait for you`;
  return (
    <div className="mb-2 flex items-center justify-between gap-2" role="navigation" aria-label="Cards and questions waiting for you" data-navigator>
      <button
        type="button"
        onClick={() => go(1)}
        className="inline-flex min-w-0 items-center gap-2 rounded-full bg-accent-soft py-1 pl-2 pr-3 text-[12px] font-medium text-accent-text transition-colors hover:bg-[color-mix(in_oklab,var(--accent)_22%,transparent)]"
        title="Go to the next one"
      >
        <span className="tc grid h-5 min-w-5 place-items-center rounded-full bg-accent px-1.5 text-[11px] font-semibold text-accent-ink" aria-hidden>
          {n}
        </span>
        <span className="truncate">{label}</span>
        {at >= 0 ? (
          <span className="tc text-[11px] font-normal text-subtle">
            {Math.min(at, n - 1) + 1} of {n}
          </span>
        ) : null}
      </button>
      <div className="flex shrink-0 items-center gap-0.5">
        <NavButton label="Previous one waiting for you" onClick={() => go(-1)}>
          <CaretLeft size={14} weight="bold" />
        </NavButton>
        <NavButton label="Next one waiting for you" onClick={() => go(1)}>
          <CaretRight size={14} weight="bold" />
        </NavButton>
      </div>
    </div>
  );
}

function NavButton({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      title={label}
      className={cx('grid h-7 w-7 place-items-center rounded-[8px] text-muted transition-colors hover:bg-hover hover:text-fg focus-visible:outline-2 focus-visible:outline-accent')}
    >
      {children}
    </button>
  );
}
