import { useId, useState } from 'react';
import { CaretRight, Coins } from '@phosphor-icons/react';
import type { CostEstimate as Estimate } from '../api/types';
import { groupParts, partPrice } from '../lib/costs';
import { cx } from '../ui/primitives';

/*
 * What an edit that uses paid outside AI services is expected to cost (D208):
 * one total, "about $0.40", that opens to what it draws on — the service, its
 * model, the source (the clip, the script lines, the picture), the units and
 * each part's estimate. Generated shots, the AI voice, cloud voice clean-up
 * and Claude's cards for them all show it with this one component; the
 * numbers come from the server (internal/costs). It is always an estimate.
 */

function upperFirst(s: string): string {
  return s ? s[0]!.toUpperCase() + s.slice(1) : s;
}

/** How many parts of one kind show before "and N more". */
const SHOWN = 8;

export function CostEstimate({ estimate: e, defaultOpen = false, className, label = 'Estimate', bare }: {
  estimate: Estimate;
  /** Open at first (a dialog that asks about the price). */
  defaultOpen?: boolean;
  className?: string;
  /** The word before the total ("Estimate"). */
  label?: string;
  /** Only the "What it draws on" toggle: the total is said just above it. */
  bare?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const [all, setAll] = useState(false);
  const id = useId();
  if (!e.parts.length) return null;
  const groups = groupParts(e.parts);
  return (
    <div className={cx('text-[12.5px] leading-relaxed', className)} data-cost-estimate data-cost-usd={e.usd}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-controls={`${id}-parts`}
        className="group inline-flex max-w-full items-center gap-1.5 rounded-[7px] text-left text-muted hover:text-fg focus-visible:outline-2 focus-visible:outline-accent"
        data-cost-toggle
      >
        {bare ? null : (
          <>
            <Coins size={14} className="shrink-0 text-subtle" aria-hidden />
            <span>
              {label}: <span className="font-medium text-fg" data-cost-total>{e.about}</span>
            </span>
          </>
        )}
        <span className="inline-flex shrink-0 items-center gap-0.5 text-[12px] text-accent-text">
          <CaretRight size={10} className={cx('transition-transform duration-200', open && 'rotate-90')} aria-hidden />
          {open ? 'Hide what it draws on' : 'What it draws on'}
        </span>
      </button>
      {open ? (
        <div id={`${id}-parts`} className="mt-2 space-y-2.5 rounded-[10px] bg-panel px-3 py-2.5 hairline" data-cost-parts>
          {groups.map((g) => {
            const shown = all ? g.parts : g.parts.slice(0, SHOWN);
            const rest = g.parts.length - shown.length;
            // One price for the whole group goes under it; several go with their parts.
            const shared = g.rates.length === 1 ? g.rates[0] : undefined;
            return (
              <section key={g.key} aria-label={`${g.service}: ${g.what}`} data-cost-group={g.service}>
                {g.step ? <div className="text-[11.5px] font-medium text-subtle">{g.step}</div> : null}
                <div className="text-[12.5px] font-medium text-fg">
                  {upperFirst(g.what)}
                  <span className="font-normal text-muted">
                    {' '}
                    · {g.service}
                    {g.model ? `, ${g.model}` : ''}
                  </span>
                </div>
                <ul className="mt-1 space-y-1">
                  {shown.map((p, i) => (
                    <li key={i} className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-3" data-cost-part>
                      <span className="min-w-0 break-words">
                        {p.source ? <span className="text-fg">{p.source}</span> : null}
                        {p.source ? <span className="text-subtle"> · </span> : null}
                        <span className="text-muted">{p.units}</span>
                        {!shared && p.rate ? <span className="block text-[11.5px] text-subtle">At {p.rate}</span> : null}
                      </span>
                      <span className={cx('tc whitespace-nowrap text-right', p.free ? 'text-ok' : 'text-fg')} data-cost-part-price>
                        {partPrice(p)}
                      </span>
                    </li>
                  ))}
                </ul>
                {rest > 0 ? (
                  <button type="button" className="mt-1 text-[12px] font-medium text-accent-text hover:underline" onClick={() => setAll(true)}>
                    And {rest} more
                  </button>
                ) : null}
                {shared ? <p className="mt-1 text-[11.5px] text-subtle">At {shared}</p> : null}
              </section>
            );
          })}
          {e.notes?.length ? (
            <div className="space-y-0.5 border-t border-line pt-2 text-[11.5px] text-subtle">
              {e.notes.map((n) => (
                <p key={n}>{n}</p>
              ))}
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
