import { useMemo } from 'react';
import { Eye, FilmReel, Lightning, Play, Warning } from '@phosphor-icons/react';
import { useActivity } from '../../api/activity';
import type { ActivityItem, Render, RevisionSummary } from '../../api/types';
import { absolute, capFirst, stamp } from '../../lib/format';
import { Button, cx, Progress } from '../../ui/primitives';

/*
 * What the person watches above the timeline (D187): the live preview (drawn
 * in the browser from the cut, unsaved changes included, motion graphics as
 * stills) or a finished render (the real thing). A review copy follows each
 * cut's latest version by itself, so it is usually there a minute after the
 * last change.
 */

const ACTIVE = ['queued', 'rendering', 'uploading'];

/** A render's name in words (D188): "Review copy", "Final render". */
export function renderName(r: Pick<Render, 'quality'>): string {
  return r.quality === 'final' ? 'Final render' : 'Review copy';
}

/** "Review copy (version 7, Today 2:34 PM)". */
export function renderChoiceLabel(r: Render, withVersion = true): string {
  const when = stamp(r.completedAt ?? r.createdAt);
  return `${renderName(r)} (${withVersion ? `version ${r.revisionNumber}, ` : ''}${when})`;
}

const newest = (a: Render, b: Render) => (b.completedAt ?? b.createdAt).localeCompare(a.completedAt ?? a.createdAt);

export interface CutCopies {
  /** The newest finished review copy of the latest version. */
  latest?: Render;
  /** The newest finished review copy of an older version of this cut (when the latest has none). */
  older?: Render;
  /** A review copy of the latest version on its way. */
  making?: Render;
  /** Its activity: progress, why it waits. */
  activity?: ActivityItem;
}

/** The review copies of a cut: the latest version's (ready or on its way) and the newest older one. */
export function useCutCopies(renders: Render[], revisions: RevisionSummary[], headId: string | undefined): CutCopies {
  const act = useActivity();
  return useMemo(() => {
    if (!headId) return {};
    const head = revisions.find((r) => r.id === headId);
    const branch = head?.branch;
    const reviewCopies = renders.filter((r) => r.quality === 'review');
    const ready = reviewCopies.filter((r) => r.state === 'available' && r.output).sort(newest);
    const latest = ready.find((r) => r.revisionId === headId);
    const sameCut = (r: Render) => !branch || revisions.find((v) => v.id === r.revisionId)?.branch === branch;
    const older = latest ? undefined : ready.find((r) => r.revisionId !== headId && sameCut(r) && (!head || r.revisionNumber < head.number));
    const making = latest ? undefined : reviewCopies.filter((r) => r.revisionId === headId && ACTIVE.includes(r.state)).sort(newest)[0];
    const activity = making ? act.data?.items.find((it) => it.renderId === making.id) : undefined;
    return { latest, older, making, activity };
  }, [renders, revisions, headId, act.data]);
}

/**
 * The switch above the player: "Live preview" or a finished render. Options
 * past the first are renders; value is "live" or a render's ID.
 */
export function PreviewSwitch({ renders, value, onChange, withVersion = true, className }: {
  renders: Render[];
  value: string;
  onChange: (v: string) => void;
  withVersion?: boolean;
  className?: string;
}) {
  const items = [{ key: 'live', label: 'Live preview', title: 'Drawn in your browser from the cut, with any unsaved changes; motion graphics show as stills' }].concat(
    renders.map((r) => ({ key: r.id, label: renderChoiceLabel(r, withVersion), title: `The ${renderName(r).toLowerCase()} MetaRush made, ${absolute(r.completedAt ?? r.createdAt)}: exactly what the video looks like` })),
  );
  return (
    <div role="radiogroup" aria-label="What the player shows" className={cx('flex max-w-full flex-wrap justify-center gap-1', className)} data-preview-switch>
      {items.map((it) => (
        <button
          key={it.key}
          type="button"
          role="radio"
          aria-checked={value === it.key}
          title={it.title}
          onClick={() => onChange(it.key)}
          data-preview-choice={it.key === 'live' ? 'live' : 'render'}
          className={cx(
            'inline-flex min-h-8 max-w-full items-center gap-1.5 truncate rounded-full px-3 text-[12.5px] font-medium hairline transition-colors',
            value === it.key ? 'bg-accent-soft text-accent-text ring-1 ring-accent-line' : 'text-muted hover:bg-hover hover:text-fg',
          )}
        >
          {it.key === 'live' ? <Eye size={13} /> : <FilmReel size={13} />}
          <span className="truncate">{it.label}</span>
        </button>
      ))}
    </div>
  );
}

/**
 * The line under the switch in the Edit tab: a review copy on its way (with
 * progress, or why it waits), one of an older version (with a button to
 * watch it), what the live preview shows.
 */
export function CopyStatus({ copies, head, showing, dirty, onWatch, className }: {
  copies: CutCopies;
  head?: RevisionSummary;
  /** The render the player shows, if any. */
  showing?: Render;
  dirty: boolean;
  onWatch: (r: Render) => void;
  className?: string;
}) {
  const { making, activity, older, latest } = copies;
  const lines: React.ReactNode[] = [];
  if (making) {
    const waiting = activity?.state === 'waiting' || making.state === 'queued';
    const why = activity?.waiting?.text;
    lines.push(
      <div key="making" className="w-full max-w-[420px]" data-copy-making>
        <div className="flex items-center justify-center gap-1.5 text-[12px] text-muted">
          <Lightning size={13} className="text-accent-text" />
          <span>
            Making a review copy of version {making.revisionNumber}…
            {why && waiting ? <span className="text-accent-text"> {capFirst(why)}</span> : null}
          </span>
          {activity?.pct != null && !waiting ? <span className="tc text-subtle">{Math.round(activity.pct * 100)}%</span> : null}
        </div>
        <Progress className="mt-1.5" value={activity?.pct ?? 0} indeterminate={activity?.pct == null || waiting} label={`Review copy of version ${making.revisionNumber}`} />
      </div>,
    );
  }
  if (showing && head && showing.revisionId !== head.id) {
    lines.push(
      <p key="old" className="flex items-center justify-center gap-1.5 text-[12px] text-accent-text">
        <Warning size={13} /> This is version {showing.revisionNumber}'s {renderName(showing).toLowerCase()}; the latest is version {head.number}.
      </p>,
    );
  } else if (!showing && !latest && older) {
    lines.push(
      <p key="older" className="flex flex-wrap items-center justify-center gap-x-2 gap-y-1 text-[12px] text-muted">
        <span>The review copy is of version {older.revisionNumber}, not the latest.</span>
        <Button size="sm" variant="ghost" icon={<Play size={12} weight="fill" />} onClick={() => onWatch(older)} data-watch-older>
          Watch it
        </Button>
      </p>,
    );
  } else if (!showing && latest && dirty) {
    lines.push(
      <p key="dirty" className="text-center text-[12px] text-muted">
        The live preview shows your unsaved changes; the review copy is of version {latest.revisionNumber} as saved.
      </p>,
    );
  } else if (!showing && latest) {
    lines.push(
      <p key="ready" className="flex flex-wrap items-center justify-center gap-x-2 gap-y-1 text-[12px] text-muted" data-copy-ready>
        <span>The review copy of version {latest.revisionNumber} is ready.</span>
        <Button size="sm" variant="ghost" icon={<Play size={12} weight="fill" />} onClick={() => onWatch(latest)}>
          Watch it
        </Button>
      </p>,
    );
  }
  if (!lines.length) return null;
  return <div className={cx('mt-2 flex flex-col items-center gap-1.5', className)}>{lines}</div>;
}
