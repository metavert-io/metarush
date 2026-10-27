import { Fragment, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { FilmSlate, FolderSimplePlus } from '@phosphor-icons/react';
import { media } from '../api/client';
import { useOp } from '../api/queries';
import type { ProjectSummary } from '../api/types';
import { plural } from '../lib/format';
import { rank, type Hit } from '../lib/names';
import { loadPref, savePref } from '../lib/theme';
import { useDialog } from '../ui/Dialog';
import { Button, cx, Skeleton, Thumb } from '../ui/primitives';
import { useCreateProject } from './Library';

/*
 * Choosing a project by typing (the PersonPicker pattern): a WAI-ARIA combobox
 * over the projects, matched like the CLI's names (case and accents ignored,
 * prefix, word start, anywhere, small typos), recent choices first. Used by
 * "Move to another project…", the toast's "Move…" and "From another project".
 */

interface Cand {
  id: string;
  name: string;
  weight: number;
  project: ProjectSummary;
}

const RECENT_KEY = 'projects.recent';

function rememberRecent(id: string) {
  const list = loadPref<string[]>(RECENT_KEY, []).filter((x) => x !== id);
  savePref(RECENT_KEY, [id, ...list].slice(0, 6));
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

export function ProjectPicker({ label, onPick, exclude = [], disabled = {}, autoFocus = true, placeholder = 'Type a project name' }: {
  label: string;
  onPick: (p: ProjectSummary) => void;
  /** Projects that make no sense here (the one it is in). */
  exclude?: string[];
  /** Projects shown but not choosable, with the reason. */
  disabled?: Record<string, string>;
  autoFocus?: boolean;
  placeholder?: string;
}) {
  const auto = useId();
  const listId = `${auto}-list`;
  const projects = useOp('project.list', {}, { staleTime: 30_000 });
  const [value, setValue] = useState('');
  const [active, setActive] = useState(0);
  const listRef = useRef<HTMLUListElement>(null);
  const excludeKey = exclude.join('|');
  const cands = useMemo<Cand[]>(
    () => (projects.data?.projects ?? []).filter((p) => !exclude.includes(p.id) && !p.archived).map((p) => ({ id: p.id, name: p.name, weight: p.assetCount, project: p })),
    [projects.data, excludeKey], // eslint-disable-line react-hooks/exhaustive-deps
  );
  const q = value.trim();
  const options = useMemo<{ project: ProjectSummary; hit?: Hit<Cand> }[]>(() => {
    if (!q) {
      const recent = loadPref<string[]>(RECENT_KEY, []);
      const byId = new Map(cands.map((c) => [c.id, c]));
      const seen = new Set<string>();
      const out: { project: ProjectSummary }[] = [];
      for (const id of recent) {
        const c = byId.get(id);
        if (c && !seen.has(id)) {
          seen.add(id);
          out.push({ project: c.project });
        }
      }
      for (const c of [...cands].sort((a, b) => b.project.updatedAt.localeCompare(a.project.updatedAt))) if (!seen.has(c.id)) out.push({ project: c.project });
      return out.slice(0, 50);
    }
    return rank(q, cands)
      .slice(0, 50)
      .map((h) => ({ project: h.candidate.project, hit: h }));
  }, [q, cands]);

  useEffect(() => setActive(0), [q, options.length]);
  useEffect(() => {
    listRef.current?.querySelector<HTMLElement>(`[data-index="${active}"]`)?.scrollIntoView({ block: 'nearest' });
  }, [active]);

  const pick = (p: ProjectSummary | undefined) => {
    if (!p || disabled[p.id]) return;
    rememberRecent(p.id);
    onPick(p);
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
        maxLength={120}
        placeholder={placeholder}
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
            pick(options[active]?.project);
          }
        }}
        className="h-11 w-full rounded-[10px] bg-panel px-3 text-[14px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0 sm:h-10 sm:text-[13.5px]"
      />
      <span className="sr-only" aria-live="polite">
        {q ? `${plural(options.length, 'project')} match.` : ''}
      </span>
      <ul id={listId} ref={listRef} role="listbox" aria-label={label} className="mt-2 max-h-[min(46dvh,340px)] space-y-0.5 overflow-y-auto overscroll-contain">
        {projects.isLoading ? (
          <>
            <Skeleton className="h-12" />
            <Skeleton className="h-12" />
          </>
        ) : null}
        {!projects.isLoading && options.length === 0 ? <li className="px-2 py-4 text-center text-[13px] text-muted">{q ? `No project matches “${q}”.` : 'There are no other projects yet.'}</li> : null}
        {options.map((o, i) => {
          const why = disabled[o.project.id];
          return (
            <li
              key={o.project.id}
              id={optionId(i)}
              role="option"
              aria-selected={i === active}
              aria-disabled={why ? true : undefined}
              data-index={i}
              title={why}
              onMouseDown={(e) => e.preventDefault()}
              onMouseEnter={() => setActive(i)}
              onClick={() => pick(o.project)}
              className={cx('flex min-h-12 cursor-pointer items-center gap-3 rounded-[10px] px-2 py-1.5', i === active ? 'bg-accent-soft' : 'hover:bg-hover', why && 'cursor-not-allowed opacity-60')}
            >
              <Thumb src={o.project.thumbAssetId ? media.asset(o.project.thumbAssetId, 'thumbnail') : undefined} alt="" className="h-9 w-14 shrink-0 rounded-[7px]" fallback={<FilmSlate size={14} />} />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[13.5px] font-medium text-fg">
                  <Marked text={o.project.name} marks={o.hit?.marks ?? []} />
                </span>
                <span className="block truncate text-[11.5px] text-subtle">{why ?? plural(o.project.assetCount, 'source')}</span>
              </span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/**
 * A dialog that asks which project; resolves with it (undefined when
 * canceled). `allowNew` adds "New project" (created, then returned).
 */
export function useChooseProject() {
  const dialog = useDialog();
  const create = useCreateProject();
  return (o: { title: string; body?: ReactNode; exclude?: string[]; disabled?: Record<string, string>; allowNew?: boolean }) =>
    dialog.open<{ id: string; name: string }>(
      (close) => (
        <div>
          {o.body ? <div className="mb-3 text-[13px] leading-relaxed text-muted">{o.body}</div> : null}
          <ProjectPicker label={o.title} exclude={o.exclude} disabled={o.disabled} onPick={(p) => close({ id: p.id, name: p.name })} />
          <div className="mt-4 flex items-center justify-between gap-2 border-t border-line pt-4">
            {o.allowNew ? (
              <Button
                variant="quiet"
                icon={<FolderSimplePlus size={15} />}
                onClick={async () => {
                  const id = await create();
                  if (id) close({ id, name: '' });
                }}
              >
                New project
              </Button>
            ) : (
              <span />
            )}
            <Button variant="ghost" onClick={() => close()}>
              Cancel
            </Button>
          </div>
        </div>
      ),
      { title: o.title, size: 'sm' },
    );
}
