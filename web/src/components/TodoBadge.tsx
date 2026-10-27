import { Link } from 'react-router';
import { Question } from '@phosphor-icons/react';
import { useOp } from '../api/queries';
import type { TodoCounts } from '../api/types';
import { cx } from '../ui/primitives';

/** Open to-do counts for badges (one shared query; live via SSE). */
export function useTodoCounts(): TodoCounts | undefined {
  return useOp('todos.counts', {}, { staleTime: 30_000 }).data;
}

function label(n: number) {
  return n === 1 ? '1 question to answer' : `${n} questions to answer`;
}

/**
 * A small "questions waiting" pill. Mount it anywhere with an asset, a project
 * or neither (global count). Renders nothing when there is nothing to answer.
 * With `link`, it opens the to-dos page filtered to that scope.
 */
export function TodoBadge({ assetId, projectId, link = true, tone = 'accent', className }: {
  assetId?: string;
  projectId?: string;
  link?: boolean;
  tone?: 'accent' | 'overlay';
  className?: string;
}) {
  const counts = useTodoCounts();
  const n = !counts ? 0 : assetId ? counts.byAsset[assetId] ?? 0 : projectId ? counts.byProject[projectId] ?? 0 : counts.total;
  if (!n) return null;
  const body = (
    <>
      <Question size={11} weight="bold" aria-hidden />
      <span className="tc">{n}</span>
    </>
  );
  const cls = cx(
    'inline-flex h-5 shrink-0 items-center gap-1 rounded-full px-1.5 text-[11px] font-semibold leading-none',
    tone === 'overlay' ? 'bg-accent text-accent-ink shadow-[0_1px_4px_rgb(0_0_0/0.35)]' : 'bg-accent-soft text-accent-text',
    className,
  );
  if (!link) {
    return (
      <span className={cls} title={label(n)} role="status" aria-label={label(n)}>
        {body}
      </span>
    );
  }
  const q = assetId ? `?asset=${encodeURIComponent(assetId)}` : projectId ? `?project=${encodeURIComponent(projectId)}` : '';
  return (
    <Link
      to={`/todos${q}`}
      className={cx(cls, 'transition-transform duration-200 hover:scale-[1.04] focus-visible:outline-2 focus-visible:outline-accent')}
      title={label(n)}
      aria-label={label(n)}
      onClick={(e) => e.stopPropagation()}
    >
      {body}
    </Link>
  );
}
