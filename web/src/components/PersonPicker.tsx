import { Fragment, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { Question, UserPlus } from '@phosphor-icons/react';
import { media } from '../api/client';
import { useOp } from '../api/queries';
import type { PersonSummary } from '../api/types';
import { rank, type Hit } from '../lib/names';
import { loadPref, savePref } from '../lib/theme';
import { cx } from '../ui/primitives';
import { FaceTile } from './FaceTile';

/*
 * The one place a person's name is entered: a WAI-ARIA 1.2 combobox over the
 * people already named. Matching is the CLI's (internal/names, mirrored in
 * lib/names.ts): case and accents ignored, exact, then the start of the name,
 * the start of a word ("lind" finds "Maya Lindqvist"), anywhere in it, then one
 * or two typos. A typo close to someone offers "Did you mean …?" first; a new
 * person is only ever added through the explicit "Add someone new" option.
 * The people list loads once (TanStack Query cache) and is filtered here.
 */

export type PersonChoice = { kind: 'person'; person: PersonSummary } | { kind: 'new'; name: string } | { kind: 'unsure' };

type Option =
  | { kind: 'person'; person: PersonSummary; hit?: Hit<Cand> }
  | { kind: 'didyoumean'; person: PersonSummary }
  | { kind: 'new'; name: string }
  | { kind: 'unsure' };

interface Cand {
  id: string;
  name: string;
  weight: number;
  person: PersonSummary;
}

const RECENT_KEY = 'people.recent';
const MAX_OPTIONS = 8;

function rememberRecent(id: string) {
  const list = loadPref<string[]>(RECENT_KEY, []).filter((x) => x !== id);
  savePref(RECENT_KEY, [id, ...list].slice(0, 6));
}

export function initials(name: string) {
  const parts = name.trim().split(/\s+/);
  return ((parts[0]?.[0] ?? '') + (parts.length > 1 ? (parts[parts.length - 1]?.[0] ?? '') : '')).toUpperCase() || '?';
}

/** A person's face: a still from a video they speak in, or their initials. */
export function PersonFace({ person, size = 28, className }: { person: Pick<PersonSummary, 'name' | 'face'>; size?: number; className?: string }) {
  const [failed, setFailed] = useState(false);
  const f = person.face;
  if (f?.box?.length === 4) return <FaceTile assetId={f.assetId} at={f.from} box={f.box} size={size} round label="" className={className} />;
  return (
    <span className={cx('relative grid shrink-0 place-items-center overflow-hidden rounded-full bg-panel text-[11px] font-semibold text-fg hairline', className)} style={{ width: size, height: size }} aria-hidden>
      {f && !failed ? (
        <img src={media.frame(f.assetId, (f.from + f.to) / 2, size * 3)} alt="" loading="lazy" onError={() => setFailed(true)} className="h-full w-full object-cover" />
      ) : (
        initials(person.name)
      )}
    </span>
  );
}

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

function videos(n: number) {
  return n === 1 ? 'in 1 video' : `in ${n} videos`;
}

export function PersonPicker({ label, value, onValueChange, onPick, exclude = [], allowNew = true, allowUnsure = false, newLabel = 'Add someone new', placeholder = 'Type a name', autoFocus, className, inputClassName, onEscape, id }: {
  /** What is being chosen, for screen readers ("Who is Speaker 2?"). */
  label: string;
  value: string;
  onValueChange: (v: string) => void;
  onPick: (c: PersonChoice) => void;
  /** People who make no sense here (the person being rejected, for example). */
  exclude?: string[];
  allowNew?: boolean;
  allowUnsure?: boolean;
  newLabel?: string;
  placeholder?: string;
  autoFocus?: boolean;
  className?: string;
  inputClassName?: string;
  /** Esc with the list closed (e.g. leave editing). */
  onEscape?: () => void;
  id?: string;
}) {
  const auto = useId();
  const inputId = id ?? `${auto}-input`;
  const listId = `${auto}-list`;
  const people = useOp('people.list', {}, { staleTime: 60_000 });
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const typed = useRef(value);
  const listRef = useRef<HTMLUListElement>(null);
  const excludeKey = exclude.join('|');

  const cands = useMemo<Cand[]>(
    () => (people.data?.people ?? []).filter((p) => !exclude.includes(p.id)).map((p) => ({ id: p.id, name: p.name, weight: p.appearances.length || p.recordings, person: p })),
    [people.data, excludeKey], // eslint-disable-line react-hooks/exhaustive-deps
  );

  const q = value.trim();
  const options = useMemo<Option[]>(() => {
    const out: Option[] = [];
    if (!q) {
      // Nothing typed yet: recently picked people, then the ones heard most.
      const recent = loadPref<string[]>(RECENT_KEY, []);
      const byId = new Map(cands.map((c) => [c.id, c]));
      const seen = new Set<string>();
      for (const rid of recent) {
        const c = byId.get(rid);
        if (c && !seen.has(c.id)) {
          seen.add(c.id);
          out.push({ kind: 'person', person: c.person });
        }
      }
      for (const c of [...cands].sort((a, b) => b.weight - a.weight || a.name.localeCompare(b.name))) {
        if (out.length >= MAX_OPTIONS) break;
        if (!seen.has(c.id)) {
          seen.add(c.id);
          out.push({ kind: 'person', person: c.person });
        }
      }
    } else {
      const hits = rank(q, cands);
      const exact = hits.some((h) => h.kind === 'exact');
      const top = hits[0];
      let skip = '';
      if (top && top.kind === 'typo') {
        out.push({ kind: 'didyoumean', person: top.candidate.person });
        skip = top.candidate.id;
      }
      for (const h of hits) {
        if (out.length >= MAX_OPTIONS) break;
        if (h.candidate.id !== skip) out.push({ kind: 'person', person: h.candidate.person, hit: h });
      }
      if (allowNew && !exact) out.push({ kind: 'new', name: q.replace(/\s+/g, ' ') });
    }
    if (allowUnsure) out.push({ kind: 'unsure' });
    return out;
  }, [q, cands, allowNew, allowUnsure]);

  useEffect(() => {
    setActive(0);
  }, [q, options.length]);
  useEffect(() => {
    listRef.current?.querySelector<HTMLElement>(`[data-index="${active}"]`)?.scrollIntoView({ block: 'nearest' });
  }, [active]);

  const pick = (o: Option | undefined) => {
    if (!o) return;
    setOpen(false);
    switch (o.kind) {
      case 'person':
      case 'didyoumean':
        rememberRecent(o.person.id);
        onValueChange(o.person.name);
        onPick({ kind: 'person', person: o.person });
        return;
      case 'new':
        onPick({ kind: 'new', name: o.name });
        return;
      case 'unsure':
        onPick({ kind: 'unsure' });
    }
  };

  const optionId = (i: number) => `${auto}-opt-${i}`;
  const shown = open && options.length > 0;
  const personCount = options.filter((o) => o.kind === 'person' || o.kind === 'didyoumean').length;
  const announce = !open
    ? ''
    : !q
      ? personCount
        ? `${personCount} ${personCount === 1 ? 'person' : 'people'} you named. Use up and down arrows to choose.`
        : ''
      : personCount
        ? `${personCount} ${personCount === 1 ? 'match' : 'matches'}.`
        : allowNew
          ? `No one matches. ${newLabel}: ${q}?`
          : 'No one matches.';

  return (
    <div className={cx('min-w-0', className)}>
      <label htmlFor={inputId} className="sr-only">
        {label}
      </label>
      <input
        id={inputId}
        role="combobox"
        aria-expanded={shown}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={shown && options[active] ? optionId(active) : undefined}
        autoComplete="off"
        spellCheck={false}
        autoFocus={autoFocus}
        value={value}
        maxLength={80}
        placeholder={placeholder}
        onChange={(e) => {
          typed.current = e.target.value;
          onValueChange(e.target.value);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onClick={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onKeyDown={(e) => {
          switch (e.key) {
            case 'ArrowDown':
              e.preventDefault();
              if (!open) setOpen(true);
              else setActive((i) => (options.length ? (i + 1) % options.length : 0));
              return;
            case 'ArrowUp':
              e.preventDefault();
              if (!open) setOpen(true);
              else setActive((i) => (options.length ? (i - 1 + options.length) % options.length : 0));
              return;
            case 'Enter':
              if (shown && options[active]) {
                e.preventDefault();
                pick(options[active]);
              }
              return;
            case 'Tab':
              // Tab accepts the highlighted match (never "Add someone new": that stays an explicit choice).
              if (shown && q && options[active] && (options[active].kind === 'person' || options[active].kind === 'didyoumean')) pick(options[active]);
              return;
            case 'Escape':
              if (shown) {
                e.preventDefault();
                e.stopPropagation();
                setOpen(false);
                onValueChange(typed.current);
              } else onEscape?.();
          }
        }}
        className={cx(
          'h-11 w-full min-w-0 rounded-[10px] bg-panel px-3 text-[14px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0 sm:h-9 sm:text-[13.5px]',
          inputClassName,
        )}
      />
      <span className="sr-only" aria-live="polite">
        {announce}
      </span>
      <ul
        id={listId}
        ref={listRef}
        role="listbox"
        aria-label={label}
        hidden={!shown}
        className="mt-1.5 max-h-[272px] overflow-y-auto overscroll-contain rounded-[12px] bg-raised p-1 shadow-[var(--shadow-soft)] hairline"
      >
        {options.map((o, i) => {
          const on = i === active;
          const base = cx('flex min-h-11 cursor-pointer items-center gap-2.5 rounded-[9px] px-2.5 py-1.5 text-left text-[13.5px] sm:min-h-10', on ? 'bg-accent-soft text-fg' : 'text-fg hover:bg-hover');
          const events = {
            id: optionId(i),
            role: 'option' as const,
            'aria-selected': on,
            'data-index': i,
            onMouseDown: (e: React.MouseEvent) => e.preventDefault(), // keep focus in the field
            onMouseEnter: () => setActive(i),
            onClick: () => pick(o),
          };
          switch (o.kind) {
            case 'didyoumean':
              return (
                <li key={`d-${o.person.id}`} {...events} className={base}>
                  <PersonFace person={o.person} />
                  <span className="min-w-0 flex-1 truncate">
                    Did you mean <span className="font-semibold">{o.person.name}</span>?
                  </span>
                  <span className="shrink-0 text-[12px] text-subtle">{videos(o.person.appearances.length)}</span>
                </li>
              );
            case 'person':
              return (
                <li key={o.person.id} {...events} className={base}>
                  <PersonFace person={o.person} />
                  <span className="min-w-0 flex-1 truncate font-medium">
                    <Marked text={o.person.name} marks={o.hit?.marks ?? []} />
                  </span>
                  <span className="shrink-0 text-[12px] text-subtle">{videos(o.person.appearances.length)}</span>
                </li>
              );
            case 'new':
              return (
                <li key="new" {...events} className={cx(base, 'border-t border-line')}>
                  <span className="grid h-7 w-7 shrink-0 place-items-center rounded-full bg-hover text-muted" aria-hidden>
                    <UserPlus size={14} />
                  </span>
                  <span className="min-w-0 flex-1 truncate">
                    {newLabel}: <span className="font-semibold">“{o.name}”</span>
                  </span>
                </li>
              );
            case 'unsure':
              return (
                <li key="unsure" {...events} className={cx(base, 'text-muted')}>
                  <span className="grid h-7 w-7 shrink-0 place-items-center rounded-full bg-hover" aria-hidden>
                    <Question size={14} />
                  </span>
                  <span className="min-w-0 flex-1">Not sure who it is</span>
                </li>
              );
          }
        })}
      </ul>
    </div>
  );
}
