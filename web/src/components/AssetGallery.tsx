import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Check, MusicNotes, Rows, SquaresFour, VideoCamera } from '@phosphor-icons/react';
import { useAssetActivity } from '../api/activity';
import { media } from '../api/client';
import type { AssetSummary } from '../api/types';
import { endAssetDrag, startAssetDrag } from '../lib/dragAssets';
import { absolute, bytes, duration as fmtDuration, orientation, plural, resolution, stamp } from '../lib/format';
import { isDialogOpen, isTypingTarget, useListNav } from '../lib/hooks';
import { loadPref, savePref } from '../lib/theme';
import type { LibraryView } from '../lib/workbench';
import { cx, Thumb } from '../ui/primitives';
import { EditableTitle, FileInfo, StatusChips } from './AssetMeta';
import { TodoBadge } from './TodoBadge';
import { AIBadge } from './generate/ai';

/* ---------- grid / list preference (per view, this browser) ---------- */

export function useLibraryView(key: string, fallback: LibraryView = 'grid'): [LibraryView, (v: LibraryView) => void] {
  const [v, setV] = useState<LibraryView>(() => loadPref<LibraryView>(`view.${key}`, fallback));
  const set = useCallback(
    (next: LibraryView) => {
      setV(next);
      savePref(`view.${key}`, next); // wrapped: storage may be unavailable
    },
    [key],
  );
  return [v, set];
}

export function ViewToggle({ value, onChange, label = 'Layout' }: { value: LibraryView; onChange: (v: LibraryView) => void; label?: string }) {
  return (
    <div role="radiogroup" aria-label={label} className="inline-flex shrink-0 rounded-[9px] bg-panel p-0.5 hairline">
      {(
        [
          ['grid', <SquaresFour key="g" size={14} />, 'Grid with large thumbnails'],
          ['list', <Rows key="l" size={14} />, 'Compact list'],
        ] as const
      ).map(([v, icon, name]) => (
        <button
          key={v}
          type="button"
          role="radio"
          aria-checked={value === v}
          aria-label={name}
          title={name}
          onClick={() => onChange(v)}
          className={cx('inline-flex h-7 w-8 items-center justify-center rounded-[7px] transition-colors', value === v ? 'bg-hover text-fg' : 'text-subtle hover:text-fg')}
        >
          {icon}
        </button>
      ))}
    </div>
  );
}

/* ---------- selection ---------- */

export interface Selection {
  selected: Set<string>;
  isSelected: (id: string) => boolean;
  toggle: (id: string) => void;
  range: (id: string) => void;
  clear: () => void;
  selectAll: () => void;
}

/** Multi-select over an ordered list: toggle, shift-range from the last anchor, clear. */
export function useAssetSelection(order: string[]): Selection {
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const anchor = useRef<string | null>(null);
  const key = order.join(',');
  // Drop IDs that left the list (moved to a project, removed).
  useEffect(() => {
    setSelected((s) => {
      const keep = new Set([...s].filter((id) => order.includes(id)));
      return keep.size === s.size ? s : keep;
    });
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  return useMemo<Selection>(
    () => ({
      selected,
      isSelected: (id) => selected.has(id),
      toggle: (id) => {
        anchor.current = id;
        setSelected((s) => {
          const n = new Set(s);
          if (n.has(id)) n.delete(id);
          else n.add(id);
          return n;
        });
      },
      range: (id) => {
        const from = anchor.current ? order.indexOf(anchor.current) : -1;
        const to = order.indexOf(id);
        if (from < 0 || to < 0) {
          anchor.current = id;
          setSelected((s) => new Set(s).add(id));
          return;
        }
        const [a, b] = from < to ? [from, to] : [to, from];
        setSelected((s) => {
          const n = new Set(s);
          for (let i = a; i <= b; i++) n.add(order[i]!);
          return n;
        });
      },
      clear: () => {
        anchor.current = null;
        setSelected(new Set());
      },
      selectAll: () => setSelected(new Set(order)),
    }),
    [selected, key], // eslint-disable-line react-hooks/exhaustive-deps
  );
}

/* ---------- the gallery ---------- */

function Checkbox({ checked, onToggle, label, className }: { checked: boolean; onToggle: (e: React.MouseEvent) => void; label: string; className?: string }) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={checked}
      aria-label={label}
      onClick={(e) => {
        e.stopPropagation();
        onToggle(e);
      }}
      className={cx(
        'grid h-[22px] w-[22px] shrink-0 place-items-center rounded-[7px] transition-colors',
        checked ? 'bg-accent text-accent-ink shadow-[inset_0_1px_0_rgb(255_255_255/0.25)]' : 'bg-black/45 text-transparent ring-1 ring-white/45 backdrop-blur hover:bg-black/60',
        className,
      )}
    >
      <Check size={13} weight="bold" />
    </button>
  );
}

function ListCheckbox({ checked, onToggle, label }: { checked: boolean; onToggle: (e: React.MouseEvent) => void; label: string }) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={checked}
      aria-label={label}
      onClick={(e) => {
        e.stopPropagation();
        onToggle(e);
      }}
      className={cx('grid h-[18px] w-[18px] shrink-0 place-items-center rounded-[5px] transition-colors', checked ? 'bg-accent text-accent-ink' : 'text-transparent ring-1 ring-line-strong hover:ring-subtle')}
    >
      <Check size={11} weight="bold" />
    </button>
  );
}

const dragLabel = (n: number, a: AssetSummary) => (n === 1 ? a.title : `${n} ${a.kind === 'audio' ? 'clips' : 'videos'}`);

/** Footage a project link brings in ("from Brand"): nothing was copied. */
function FromBadge({ from }: { from: string }) {
  return (
    <span className="mt-0.5 inline-flex max-w-full items-center gap-1 truncate rounded-full bg-info-soft px-2 py-[1px] text-[11px] text-info" title={`It comes from ${from}; nothing was copied`}>
      from {from}
    </span>
  );
}

export function AssetGallery({
  assets,
  view,
  selection,
  onOpen,
  action,
  meta,
  draggable = true,
  label,
  emptyText = 'Nothing here.',
}: {
  assets: AssetSummary[];
  view: LibraryView;
  selection: Selection;
  onOpen: (a: AssetSummary) => void;
  action?: (a: AssetSummary) => ReactNode;
  /** A line under the title (the Library says where a clip is filed). */
  meta?: (a: AssetSummary) => ReactNode;
  draggable?: boolean;
  label: string;
  emptyText?: string;
}) {
  const activity = useAssetActivity();
  const [active, setActive] = useListNav(assets.length, (i) => assets[i] && onOpen(assets[i]!));
  const box = useRef<HTMLDivElement>(null);
  const anySelected = selection.selected.size > 0;

  // x / space toggles the highlighted item; Esc clears the selection.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || isTypingTarget(e.target) || isDialogOpen()) return;
      if ((e.key === 'x' || e.key === ' ') && active >= 0 && assets[active]) {
        if (e.key === ' ' && e.target instanceof HTMLButtonElement) return; // a focused button handles space itself
        e.preventDefault();
        selection.toggle(assets[active]!.id);
      } else if (e.key === 'Escape' && anySelected) {
        selection.clear();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [active, assets, selection, anySelected]);

  useEffect(() => {
    if (active < 0) return;
    box.current?.querySelector<HTMLElement>(`[data-index="${active}"]`)?.scrollIntoView({ block: 'nearest' });
  }, [active]);

  const click = (a: AssetSummary, i: number, e: React.MouseEvent) => {
    setActive(i);
    if (e.shiftKey) selection.range(a.id);
    else if (e.metaKey || e.ctrlKey || anySelected) selection.toggle(a.id);
    else onOpen(a);
  };
  const dragIds = (a: AssetSummary) => (selection.isSelected(a.id) ? assets.filter((x) => selection.isSelected(x.id)).map((x) => x.id) : [a.id]);
  const onDragStart = (a: AssetSummary) => (e: React.DragEvent) => {
    const ids = dragIds(a);
    startAssetDrag(e, ids, dragLabel(ids.length, a), a.variants.includes('thumbnail') ? media.asset(a.id, 'thumbnail') : undefined);
  };

  if (assets.length === 0) return <div className="rounded-[14px] px-4 py-8 text-center text-[13px] text-muted hairline">{emptyText}</div>;

  if (view === 'list')
    return (
      <div ref={box} role="list" aria-label={label} className="overflow-hidden rounded-[14px] hairline">
        <div aria-hidden className="hidden grid-cols-[18px_64px_minmax(0,2.4fr)_64px_minmax(0,1fr)_72px_minmax(0,1.6fr)_88px_72px] items-center gap-3 border-b border-line bg-raised px-3 py-2 text-[11px] font-medium text-subtle lg:grid">
          <span />
          <span />
          <span>Title</span>
          <span className="text-right">Length</span>
          <span>Picture</span>
          <span className="text-right">Size</span>
          <span>Status</span>
          <span>Added</span>
          <span />
        </div>
        {assets.map((a, i) => {
          const sel = selection.isSelected(a.id);
          return (
            <div
              key={a.id}
              role="listitem"
              data-index={i}
              data-active={i === active || undefined}
              draggable={draggable}
              onDragStart={draggable ? onDragStart(a) : undefined}
              onDragEnd={endAssetDrag}
              onClick={(e) => click(a, i, e)}
              className={cx(
                'group grid cursor-default grid-cols-[18px_52px_minmax(0,1fr)_auto] items-center gap-3 border-b border-line px-3 py-2 last:border-b-0 lg:grid-cols-[18px_64px_minmax(0,2.4fr)_64px_minmax(0,1fr)_72px_minmax(0,1.6fr)_88px_72px]',
                sel ? 'bg-accent-soft' : i === active ? 'bg-hover' : 'hover:bg-hover/70',
              )}
            >
              <ListCheckbox checked={sel} onToggle={(e) => (e.shiftKey ? selection.range(a.id) : selection.toggle(a.id))} label={`Select ${a.title}`} />
              <Thumb
                src={a.variants.includes('thumbnail') ? media.asset(a.id, 'thumbnail') : undefined}
                alt=""
                className="aspect-video w-full rounded-[6px]"
                fit={orientation(a.width, a.height) === 'vertical' ? 'contain' : 'cover'}
                fallback={a.kind === 'audio' ? <MusicNotes size={13} /> : <VideoCamera size={13} />}
              />
              <div className="min-w-0" onClick={(e) => e.stopPropagation()}>
                <EditableTitle asset={a} onOpen={() => onOpen(a)} />
                {a.from ? <FromBadge from={a.from} /> : null}
                <AIBadge source={a.source} className="mt-0.5" />
                {meta?.(a)}
                <FileInfo asset={a} />
                {/* Narrow screens: when it was added sits under the title (D188); wide ones have a column. */}
                <div className="mt-0.5 truncate text-[11.5px] text-subtle lg:hidden" title={absolute(a.createdAt)} data-stamp>
                  Added {stamp(a.createdAt)}
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-1 lg:hidden">
                  <TodoBadge assetId={a.id} />
                  <StatusChips asset={a} item={activity.get(a.id)} compact />
                </div>
              </div>
              <span className="flex items-center justify-end gap-1 lg:block">
                <span className="tc text-right text-[12px] text-muted">{fmtDuration(a.duration)}</span>
                {/* On narrow screens the actions sit here (the wide layout has a column for them). */}
                {action ? (
                  <span className="lg:hidden" onClick={(e) => e.stopPropagation()}>
                    {action(a)}
                  </span>
                ) : null}
              </span>
              <span className="tc hidden truncate text-[12px] text-muted lg:block">{resolution(a.width, a.height, a.fps) || a.kind}</span>
              <span className="tc hidden text-right text-[12px] text-muted lg:block">{bytes(a.size)}</span>
              <div className="hidden min-w-0 flex-wrap items-center gap-1 lg:flex">
                <TodoBadge assetId={a.id} />
                <StatusChips asset={a} item={activity.get(a.id)} compact />
              </div>
              <span className="hidden truncate text-[12px] text-subtle lg:block" title={absolute(a.createdAt)} data-stamp>
                {stamp(a.createdAt)}
              </span>
              <div className="hidden justify-end lg:flex" onClick={(e) => e.stopPropagation()}>
                {action?.(a)}
              </div>
            </div>
          );
        })}
      </div>
    );

  return (
    <div ref={box} role="list" aria-label={label} className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4 2xl:grid-cols-6">
      {assets.map((a, i) => {
        const sel = selection.isSelected(a.id);
        const orient = orientation(a.width, a.height);
        return (
          <div
            key={a.id}
            role="listitem"
            data-index={i}
            data-active={i === active || undefined}
            className={cx('group relative rounded-[14px] p-1.5 transition-colors', sel ? 'bg-accent-soft ring-1 ring-accent-line' : i === active ? 'bg-hover ring-1 ring-line-strong' : 'hover:bg-hover')}
          >
            <div draggable={draggable} onDragStart={draggable ? onDragStart(a) : undefined} onDragEnd={endAssetDrag} className="relative">
              <button
                type="button"
                onClick={(e) => click(a, i, e)}
                aria-label={anySelected ? `${sel ? 'Deselect' : 'Select'} ${a.title}` : `Open ${a.title}`}
                className="block w-full rounded-[10px] text-left outline-none focus-visible:outline-2 focus-visible:outline-accent"
              >
                <Thumb
                  src={a.variants.includes('thumbnail') ? media.asset(a.id, 'thumbnail') : undefined}
                  alt=""
                  className="aspect-video w-full rounded-[10px]"
                  fit={orient === 'vertical' ? 'contain' : 'cover'}
                  imgClassName="group-hover:scale-[1.03]"
                  fallback={a.kind === 'audio' ? <MusicNotes size={22} /> : <VideoCamera size={22} />}
                />
                <span className="tc absolute bottom-1.5 right-1.5 rounded-[6px] bg-black/70 px-1.5 py-0.5 text-[10.5px] text-white">{fmtDuration(a.duration)}</span>
                <AIBadge source={a.source} overlay className="absolute right-1.5 top-1.5" />
                <span className="absolute bottom-1.5 left-1.5 flex items-center gap-1">
                  <TodoBadge assetId={a.id} link={false} tone="overlay" />
                  {orient === 'vertical' ? <span className="rounded-[6px] bg-black/60 px-1.5 py-0.5 text-[10px] text-white/90">9:16</span> : null}
                </span>
              </button>
              <Checkbox
                checked={sel}
                onToggle={(e) => (e.shiftKey ? selection.range(a.id) : selection.toggle(a.id))}
                label={`Select ${a.title}`}
                className={cx('absolute left-2 top-2', sel || anySelected ? 'opacity-100' : 'opacity-0 focus-visible:opacity-100 group-hover:opacity-100 [@media(hover:none)]:opacity-100')}
              />
            </div>
            <div className="px-1 pb-1 pt-2">
              <EditableTitle asset={a} onOpen={() => onOpen(a)} />
              {a.from ? <FromBadge from={a.from} /> : null}
              {meta?.(a)}
              <FileInfo asset={a} className="mt-0.5" />
              <div className="mt-0.5 flex items-center gap-1.5 text-[11.5px] text-subtle">
                {a.kind === 'audio' ? <MusicNotes size={11} /> : <VideoCamera size={11} />}
                <span className="truncate">
                  {a.width && a.height ? `${a.width}×${a.height}` : a.kind}
                  {a.transcript?.words ? `, ${plural(a.transcript.words, 'word')}` : ''}
                </span>
              </div>
              <div className="mt-0.5 truncate text-[11.5px] text-subtle" title={absolute(a.createdAt)} data-stamp>
                Added {stamp(a.createdAt)}
              </div>
              <StatusChips asset={a} item={activity.get(a.id)} className="mt-2" />
            </div>
            {action ? <div className="absolute right-3 top-3 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100 [@media(hover:none)]:opacity-100">{action(a)}</div> : null}
          </div>
        );
      })}
    </div>
  );
}
