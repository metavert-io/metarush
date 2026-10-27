import { Fragment, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { Plus, Stack } from '@phosphor-icons/react';
import { media } from '../api/client';
import { useOp } from '../api/queries';
import type { CollectionRow } from '../api/types';
import { plural } from '../lib/format';
import { fold, rank, type Hit } from '../lib/names';
import { loadPref, savePref } from '../lib/theme';
import { useDialog } from '../ui/Dialog';
import { Button, cx, Skeleton, Thumb } from '../ui/primitives';

/*
 * Choosing a collection by typing (D96): a combobox over the collections,
 * matched like project names (case and accents ignored, prefixes, small
 * typos), recent choices first. When nothing is called what was typed, the
 * last option creates it ("Create “Stock”"), so filing never needs a detour.
 */

const RECENT_KEY = 'collections.recent';

function rememberRecent(id: string) {
  const list = loadPref<string[]>(RECENT_KEY, []).filter((x) => x !== id);
  savePref(RECENT_KEY, [id, ...list].slice(0, 6));
}

interface Cand {
  id: string;
  name: string;
  weight: number;
  row: CollectionRow;
}

/** What was picked: an existing collection, or a name to create. */
export type CollectionChoice = { id: string; name: string } | { create: string };

function Marked({ text, marks }: { text: string; marks: [number, number][] }) {
  if (!marks.length) return <>{text}</>;
  const out: ReactNode[] = [];
  let at = 0;
  marks.forEach(([a, b], i) => {
    if (a > at) out.push(<Fragment key={`t${i}`}>{text.slice(at, a)}</Fragment>);
    out.push(
      <mark key={`m${i}`} className="rounded-[3px] bg-accent-soft text-fg">
        {text.slice(a, b)}
      </mark>,
    );
    at = b;
  });
  if (at < text.length) out.push(<Fragment key="end">{text.slice(at)}</Fragment>);
  return <>{out}</>;
}

export function CollectionPicker({ label, onPick, disabled = {}, autoFocus = true }: {
  label: string;
  onPick: (c: CollectionChoice) => void;
  /** Collections shown but not choosable, with the reason ("It is on it already"). */
  disabled?: Record<string, string>;
  autoFocus?: boolean;
}) {
  const auto = useId();
  const listId = `${auto}-list`;
  const list = useOp('collection.list', {}, { staleTime: 15_000 });
  const [value, setValue] = useState('');
  const [active, setActive] = useState(0);
  const listRef = useRef<HTMLUListElement>(null);
  const cands = useMemo<Cand[]>(() => (list.data?.collections ?? []).map((c) => ({ id: c.id, name: c.name, weight: c.count, row: c })), [list.data]);
  const q = value.trim();
  const exact = q ? cands.find((c) => fold(c.name) === fold(q)) : undefined;
  const options = useMemo<{ row?: CollectionRow; hit?: Hit<Cand>; create?: string }[]>(() => {
    let out: { row?: CollectionRow; hit?: Hit<Cand>; create?: string }[];
    if (!q) {
      const recent = loadPref<string[]>(RECENT_KEY, []);
      const byId = new Map(cands.map((c) => [c.id, c]));
      const seen = new Set<string>();
      out = [];
      for (const id of recent) {
        const c = byId.get(id);
        if (c && !seen.has(id)) {
          seen.add(id);
          out.push({ row: c.row });
        }
      }
      for (const c of [...cands].sort((a, b) => a.name.localeCompare(b.name))) if (!seen.has(c.id)) out.push({ row: c.row });
    } else {
      out = rank(q, cands)
        .slice(0, 50)
        .map((h) => ({ row: h.candidate.row, hit: h }));
      if (!exact) out.push({ create: q.replace(/\s+/g, ' ') });
    }
    return out.slice(0, 51);
  }, [q, cands, exact]);

  useEffect(() => setActive(0), [q, options.length]);
  useEffect(() => {
    listRef.current?.querySelector<HTMLElement>(`[data-index="${active}"]`)?.scrollIntoView({ block: 'nearest' });
  }, [active]);

  const pick = (o: (typeof options)[number] | undefined) => {
    if (!o) return;
    if (o.create) {
      onPick({ create: o.create });
      return;
    }
    if (!o.row || disabled[o.row.id]) return;
    rememberRecent(o.row.id);
    onPick({ id: o.row.id, name: o.row.name });
  };
  const optionId = (i: number) => `${auto}-opt-${i}`;

  return (
    <div className="min-w-0">
      <label htmlFor={`${auto}-input`} className="sr-only">
        {label}
      </label>
      <input
        id={`${auto}-input`}
        role="combobox"
        aria-expanded
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={options[active] ? optionId(active) : undefined}
        autoComplete="off"
        spellCheck={false}
        autoFocus={autoFocus}
        data-autofocus={autoFocus || undefined}
        value={value}
        maxLength={80}
        placeholder={cands.length ? 'Type a collection, or a new name' : 'Name a new collection: Extra shots, Family, Stock…'}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown') {
            e.preventDefault();
            setActive((i) => (options.length ? (i + 1) % options.length : 0));
          } else if (e.key === 'ArrowUp') {
            e.preventDefault();
            setActive((i) => (options.length ? (i - 1 + options.length) % options.length : 0));
          } else if (e.key === 'Enter') {
            e.preventDefault();
            pick(options[active]);
          }
        }}
        className="h-11 w-full rounded-[10px] bg-panel px-3 text-[14px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0 sm:h-10 sm:text-[13.5px]"
      />
      <span className="sr-only" aria-live="polite">
        {q ? `${plural(options.filter((o) => !o.create).length, 'collection')} match.` : ''}
      </span>
      <ul id={listId} ref={listRef} role="listbox" aria-label={label} className="mt-2 max-h-[min(46dvh,340px)] space-y-0.5 overflow-y-auto overscroll-contain">
        {list.isLoading ? (
          <>
            <Skeleton className="h-12" />
            <Skeleton className="h-12" />
          </>
        ) : null}
        {!list.isLoading && options.length === 0 ? <li className="px-2 py-4 text-center text-[13px] text-muted">No collections yet. Type a name to make the first one.</li> : null}
        {options.map((o, i) => {
          if (o.create)
            return (
              <li
                key="create"
                id={optionId(i)}
                role="option"
                aria-selected={i === active}
                data-index={i}
                data-create
                onMouseDown={(e) => e.preventDefault()}
                onMouseEnter={() => setActive(i)}
                onClick={() => pick(o)}
                className={cx('flex min-h-12 cursor-pointer items-center gap-3 rounded-[10px] px-2 py-1.5', i === active ? 'bg-accent-soft' : 'hover:bg-hover')}
              >
                <span className="grid h-9 w-14 shrink-0 place-items-center rounded-[7px] bg-accent-soft text-accent-text">
                  <Plus size={15} weight="bold" />
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13.5px] font-medium text-fg">Create “{o.create}”</span>
                  <span className="block truncate text-[11.5px] text-subtle">A new collection</span>
                </span>
              </li>
            );
          const row = o.row!;
          const why = disabled[row.id];
          return (
            <li
              key={row.id}
              id={optionId(i)}
              role="option"
              aria-selected={i === active}
              aria-disabled={why ? true : undefined}
              data-index={i}
              title={why}
              onMouseDown={(e) => e.preventDefault()}
              onMouseEnter={() => setActive(i)}
              onClick={() => pick(o)}
              className={cx('flex min-h-12 cursor-pointer items-center gap-3 rounded-[10px] px-2 py-1.5', i === active ? 'bg-accent-soft' : 'hover:bg-hover', why && 'cursor-not-allowed opacity-60')}
            >
              <Thumb src={row.coverAssetId ? media.asset(row.coverAssetId, 'thumbnail') : undefined} alt="" className="h-9 w-14 shrink-0 rounded-[7px]" fallback={<Stack size={14} />} />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[13.5px] font-medium text-fg">
                  <Marked text={row.name} marks={o.hit?.marks ?? []} />
                </span>
                <span className="block truncate text-[11.5px] text-subtle">{why ?? row.text}</span>
              </span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/**
 * A dialog that asks which collection; resolves with the choice (an existing
 * collection or a new name), or undefined when canceled.
 */
export function useChooseCollection() {
  const dialog = useDialog();
  return (o: { title: string; body?: ReactNode; disabled?: Record<string, string> }) =>
    dialog.open<CollectionChoice>(
      (close) => (
        <div>
          {o.body ? <div className="mb-3 text-[13px] leading-relaxed text-muted">{o.body}</div> : null}
          <CollectionPicker label={o.title} disabled={o.disabled} onPick={(c) => close(c)} />
          <div className="mt-4 flex justify-end border-t border-line pt-4">
            <Button variant="ghost" onClick={() => close()}>
              Cancel
            </Button>
          </div>
        </div>
      ),
      { title: o.title, size: 'sm' },
    );
}
