import { useState } from 'react';
import { ChatCircleText, PushPin } from '@phosphor-icons/react';
import { useOp, useOpMutation } from '../api/queries';
import { absolute, relative, timecode, viaLabel } from '../lib/format';
import { Button, cx, ErrorPanel, Freshness, Skeleton, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';

/** Review comments, optionally pinned to a time in the cut (or in a source). */
export function Comments({ projectId, revisionId, assetId, time, timeLabel = 'cut', onSeek, revisionNumberFor, className }: {
  projectId: string;
  revisionId?: string;
  assetId?: string;
  time?: number;
  timeLabel?: string;
  onSeek?: (t: number) => void;
  revisionNumberFor?: (id: string) => number | undefined;
  className?: string;
}) {
  const list = useOp('comment.list', { projectId });
  const toast = useToast();
  const [body, setBody] = useState('');
  const [pin, setPin] = useState(true);
  const add = useOpMutation('comment.add', { invalidate: ['comment.list'] });

  const comments = (list.data?.comments ?? []).filter((c) => (assetId ? c.assetId === assetId : !c.assetId));
  const scoped = revisionId ? comments.filter((c) => !c.revisionId || c.revisionId === revisionId) : comments;
  const other = revisionId ? comments.length - scoped.length : 0;

  const submit = () => {
    const text = body.trim();
    if (!text) return;
    add.mutate(
      {
        projectId,
        body: text,
        ...(revisionId ? { revisionId } : {}),
        ...(assetId ? { assetId } : {}),
        ...(pin && time !== undefined ? { at: Math.round(time * 1000) / 1000 } : {}),
      },
      {
        onSuccess: () => {
          setBody('');
          toast({ tone: 'success', title: 'Comment added' });
        },
        onError: (e) => toast({ tone: 'error', title: 'Could not add the comment', body: e.message }),
      },
    );
  };

  return (
    <div className={className}>
      <div className="mb-2 flex items-center gap-2">
        <h3 className="text-[13.5px] font-semibold text-fg">Comments</h3>
        <span className="tc text-[11.5px] text-subtle">{scoped.length}</span>
        <Freshness className="ml-auto" at={list.dataUpdatedAt || undefined} fetching={list.isFetching} />
      </div>
      {list.isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-12" />
          <Skeleton className="h-12" />
        </div>
      ) : list.error ? (
        <ErrorPanel compact error={list.error} onRetry={() => void list.refetch()} />
      ) : scoped.length === 0 ? (
        <div className="flex items-start gap-2.5 rounded-[10px] px-3 py-3 text-[12.5px] leading-relaxed text-muted hairline">
          <ChatCircleText size={16} className="mt-[1px] shrink-0 text-subtle" />
          <span>No comments yet. Notes pinned to a moment in the {timeLabel} help Claude Code find exactly what you mean.</span>
        </div>
      ) : (
        <ul className="max-h-[320px] space-y-1 overflow-y-auto pr-1">
          {scoped.map((c) => (
            <li key={c.id} className="rounded-[10px] px-2.5 py-2 hover:bg-hover">
              <div className="flex items-center gap-2 text-[11.5px] text-subtle" data-comment-outside={c.outside ? '' : undefined}>
                <span className="font-medium text-muted" title={c.outside ? 'Left through a review link by someone outside the workspace' : undefined}>
                  {c.author.userName ?? 'Someone'}
                </span>
                <span title={absolute(c.createdAt)}>{relative(c.createdAt)}</span>
                {c.revisionId && revisionNumberFor?.(c.revisionId) ? <span className="tc">r{revisionNumberFor(c.revisionId)}</span> : null}
                <span className="sr-only">via {viaLabel(c.author.via)}</span>
                {c.at !== undefined ? (
                  <button
                    type="button"
                    disabled={!onSeek || (!!revisionId && !!c.revisionId && c.revisionId !== revisionId)}
                    onClick={() => onSeek?.(c.at!)}
                    className="tc ml-auto inline-flex items-center gap-1 rounded-full bg-accent-soft px-2 py-0.5 text-[11px] text-accent-text hover:brightness-110 disabled:opacity-60"
                  >
                    <PushPin size={10} weight="fill" /> {timecode(c.at)}
                  </button>
                ) : null}
              </div>
              <p className="mt-1 whitespace-pre-wrap break-words text-[13px] leading-relaxed text-fg">{c.outside ? <Theirs>{c.body}</Theirs> : c.body}</p>
            </li>
          ))}
        </ul>
      )}
      {other > 0 ? <div className="mt-1 px-2.5 text-[11.5px] text-subtle">{other} more on other versions</div> : null}
      <form
        className="mt-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <label htmlFor={`comment-${projectId}-${assetId ?? 'cut'}`} className="sr-only">
          Add a comment
        </label>
        <textarea
          id={`comment-${projectId}-${assetId ?? 'cut'}`}
          rows={2}
          value={body}
          maxLength={4000}
          onChange={(e) => setBody(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              submit();
            }
          }}
          placeholder="Add a note for the next pass…"
          className="w-full resize-y rounded-[12px] bg-panel px-3 py-2.5 text-[13px] leading-relaxed text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
        />
        <div className="mt-2 flex items-center gap-2">
          {time !== undefined ? (
            <label className={cx('inline-flex cursor-pointer items-center gap-1.5 text-[12px]', pin ? 'text-accent-text' : 'text-subtle')}>
              <input type="checkbox" checked={pin} onChange={(e) => setPin(e.target.checked)} className="h-3.5 w-3.5 accent-[var(--accent)]" />
              Pin to <span className="tc">{timecode(time)}</span>
            </label>
          ) : null}
          <Button type="submit" size="sm" variant="primary" className="ml-auto" disabled={!body.trim()} loading={add.isPending}>
            Comment
          </Button>
        </div>
      </form>
    </div>
  );
}
