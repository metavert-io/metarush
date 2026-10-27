import { useEffect, useRef, useState, type ReactNode } from 'react';
import { BatteryWarning, Check, PencilSimple, Warning } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { assetChips, type StatusChip } from '../api/activity';
import { call } from '../api/client';
import type { ActivityItem, AssetSummary } from '../api/types';
import { cx, Spinner, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';

/* ---------- Status chips: proxy ✓ · transcript paused (battery 18%) · evidence ✓ ---------- */

function chipTone(s: StatusChip): string {
  switch (s.state) {
    case 'done':
      return 'bg-ok-soft text-ok';
    case 'failed':
      return 'bg-danger-soft text-danger';
    case 'paused':
      return 'bg-accent-soft text-accent-text';
    case 'running':
      return 'bg-info-soft text-info';
    default:
      return 'bg-hover text-subtle';
  }
}

function ChipIcon({ s }: { s: StatusChip }) {
  switch (s.state) {
    case 'done':
      return <Check size={9} weight="bold" />;
    case 'failed':
      return <Warning size={9} weight="bold" />;
    case 'paused':
      return <BatteryWarning size={10} weight="bold" />;
    case 'running':
      return <Spinner size={8} />;
    default:
      return <span aria-hidden className="h-[5px] w-[5px] rounded-full bg-current opacity-60" />;
  }
}

/** Per-asset processing state; live (running, paused for battery, failed) when the asset is being processed. */
export function StatusChips({ asset, item, className, compact }: { asset: AssetSummary; item?: ActivityItem; className?: string; compact?: boolean }) {
  const chips = assetChips(asset, item);
  return (
    <div className={cx('flex flex-wrap gap-1', className)}>
      {chips.map((c) => {
        const paused = c.state === 'paused';
        const text = paused && !compact ? `${c.label} ${c.detail}` : c.label;
        return (
          <span
            key={c.key}
            title={`${c.label}: ${c.detail}`}
            className={cx('inline-flex h-5 max-w-full items-center gap-1 rounded-full px-1.5 text-[10.5px] font-medium', chipTone(c))}
          >
            <ChipIcon s={c} />
            <span className="truncate">{text}</span>
            {c.state === 'running' && c.pct ? <span className="tc opacity-80">{Math.round(c.pct * 100)}%</span> : null}
            <span className="sr-only">: {c.detail}</span>
          </span>
        );
      })}
    </div>
  );
}

/* ---------- Inline title editing ---------- */

/**
 * An asset title that can be renamed in place (asset.update). The original
 * filename never changes; it is shown separately as file info.
 */
export function EditableTitle({
  asset,
  className,
  inputClassName,
  onOpen,
  heading,
  children,
}: {
  asset: Pick<AssetSummary, 'id' | 'title' | 'name'>;
  className?: string;
  inputClassName?: string;
  /** When set, clicking the text opens the asset; renaming is the pencil button (or F2). */
  onOpen?: () => void;
  heading?: boolean;
  children?: ReactNode;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(asset.title);
  const [saving, setSaving] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const qc = useQueryClient();
  const toast = useToast();
  const title = asset.title || asset.name;

  useEffect(() => {
    if (!editing) setValue(asset.title);
  }, [asset.title, editing]);
  useEffect(() => {
    if (editing) {
      input.current?.focus();
      input.current?.select();
    }
  }, [editing]);

  const busy = useRef(false);
  const save = async () => {
    if (busy.current) return;
    const next = value.replace(/\s+/g, ' ').trim();
    if (!next || next === asset.title) {
      setEditing(false);
      setValue(asset.title);
      return;
    }
    busy.current = true;
    setSaving(true);
    try {
      await call('asset.update', { id: asset.id, title: next });
      for (const k of ['asset.list', 'project.show', 'asset.show', 'asset.card', 'asset.fixes', 'asset.names', 'activity.summary', 'search', 'footage.moments']) void qc.invalidateQueries({ queryKey: [k] });
      setEditing(false);
    } catch (e) {
      toast({ tone: 'error', title: 'Could not rename it', body: e instanceof Error ? e.message : String(e) });
    } finally {
      busy.current = false;
      setSaving(false);
    }
  };

  if (editing)
    return (
      <form
        className={cx('flex min-w-0 items-center gap-1.5', className)}
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <input
          ref={input}
          value={value}
          maxLength={200}
          disabled={saving}
          aria-label={`Title for ${asset.name}`}
          onChange={(e) => setValue(e.target.value)}
          onBlur={() => void save()}
          onKeyDown={(e) => {
            if (e.key === 'Escape') {
              e.preventDefault();
              e.stopPropagation();
              setValue(asset.title);
              setEditing(false);
            }
          }}
          onClick={(e) => e.stopPropagation()}
          className={cx(
            'min-w-0 flex-1 rounded-[7px] bg-panel px-1.5 py-0.5 text-fg outline-none ring-1 ring-accent-line focus:ring-2 focus:ring-accent',
            heading ? 'text-[18px] font-semibold tracking-[-0.02em]' : 'text-[13px] font-medium',
            inputClassName,
          )}
        />
        {saving ? <Spinner size={11} className="text-subtle" label="Saving title" /> : null}
      </form>
    );

  const Text = heading ? 'h2' : 'span';
  return (
    <div className={cx('group/title flex min-w-0 items-center gap-1', className)}>
      {onOpen ? (
        <button type="button" onClick={onOpen} onKeyDown={(e) => e.key === 'F2' && (e.preventDefault(), setEditing(true))} className="min-w-0 truncate text-left outline-none focus-visible:rounded-[6px] focus-visible:outline-2 focus-visible:outline-accent" title={title}>
          <Text className={cx('truncate', heading ? 'text-[18px] font-semibold tracking-[-0.02em] text-fg' : 'text-[13px] font-medium text-fg')}>{children ?? <Theirs>{title}</Theirs>}</Text>
        </button>
      ) : (
        <Text
          className={cx('min-w-0 cursor-text truncate', heading ? 'text-[18px] font-semibold tracking-[-0.02em] text-fg' : 'text-[13px] font-medium text-fg')}
          title={`${title} (click to rename)`}
          onClick={() => setEditing(true)}
        >
          {children ?? <Theirs>{title}</Theirs>}
        </Text>
      )}
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          setEditing(true);
        }}
        aria-label={`Rename ${title}`}
        title="Rename"
        className="shrink-0 rounded-[6px] p-1 text-subtle opacity-60 transition-opacity hover:bg-hover hover:text-fg focus-visible:opacity-100 group-hover/title:opacity-100 [@media(hover:none)]:opacity-100"
      >
        <PencilSimple size={heading ? 14 : 12} />
      </button>
    </div>
  );
}

/** The original filename (and aliases) as secondary file info. */
export function FileInfo({ asset, className }: { asset: Pick<AssetSummary, 'name' | 'title' | 'aliases'>; className?: string }) {
  const showName = asset.name !== asset.title;
  const aliases = asset.aliases ?? [];
  if (!showName && aliases.length === 0) return null;
  return (
    <div className={cx('min-w-0 truncate text-[11px] text-subtle', className)} title={[asset.name, ...aliases].join('\n')}>
      <Theirs className="tc">{asset.name}</Theirs>
      {aliases.length ? <span> + {aliases.length === 1 ? <Theirs>{aliases[0]}</Theirs> : `${aliases.length} other names`}</span> : null}
    </div>
  );
}
