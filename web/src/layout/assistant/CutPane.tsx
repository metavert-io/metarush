import { useNavigate } from 'react-router';
import { ArrowSquareOut, FilmSlate } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import { duration, relative } from '../../lib/format';
import { CutPreview } from '../../player/LazyCutPreview';
import { Badge, Button, ErrorPanel, Skeleton } from '../../ui/primitives';

/** The conversation's project: its current cut, playable, and what version it is. */
export function CutPane({ projectId, onNavigate }: { projectId?: string; onNavigate: () => void }) {
  const navigate = useNavigate();
  const q = useOp('project.show', projectId ? { id: projectId } : null, { staleTime: 15_000 });
  if (!projectId) {
    return (
      <div className="grid h-full place-items-center px-6 text-center">
        <div>
          <FilmSlate size={22} className="mx-auto text-subtle" aria-hidden />
          <p className="mt-2 text-[13px] text-muted">Open a project to see its cut here.</p>
        </div>
      </div>
    );
  }
  if (q.isLoading) {
    return (
      <div className="space-y-3 p-4" aria-busy="true">
        <Skeleton className="mx-auto aspect-[9/16] w-full max-w-[300px] rounded-[14px]" />
        <Skeleton className="h-5 w-2/3" />
      </div>
    );
  }
  if (q.error || !q.data) return <div className="p-4">{q.error ? <ErrorPanel error={q.error} compact onRetry={() => void q.refetch()} /> : null}</div>;
  const d = q.data;
  const head = d.revisions.find((r) => r.id === d.headRevisionId);
  const go = (to: string) => {
    onNavigate();
    navigate(to);
  };
  return (
    <div className="flex h-full min-h-0 flex-col gap-3 overflow-y-auto p-4">
      <div className="min-w-0">
        <div className="truncate text-[13.5px] font-semibold text-fg">{d.project.name}</div>
        <div className="mt-0.5 text-[12px] text-subtle">{head ? 'The current cut' : 'No cut yet'}</div>
      </div>
      {head ? (
        <>
          <CutPreview revisionId={head.id} renders={d.renders} maxHeight="min(56dvh, 560px)" />
          <div className="space-y-1.5">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-[13px] text-fg">Version {head.number}</span>
              <span className="tc text-[12px] text-subtle">{duration(head.duration)}</span>
              <Badge tone={head.approval?.state === 'approved' ? 'ok' : head.approval?.state === 'rejected' ? 'danger' : 'neutral'}>
                {head.approval?.state === 'approved' ? 'Approved' : head.approval?.state === 'rejected' ? 'Needs changes' : 'Draft'}
              </Badge>
            </div>
            {head.instruction ? <p className="clamp-3 text-[12.5px] leading-relaxed text-muted">{head.instruction}</p> : null}
            <p className="text-[11.5px] text-subtle">
              Made {relative(head.createdAt)}
              {head.createdBy?.userName ? ` by ${head.createdBy.userName}` : ''}
            </p>
          </div>
          <Button size="sm" variant="secondary" icon={<ArrowSquareOut size={13} />} onClick={() => go(`/p/${d.project.id}/r/${head.id}`)} className="self-start">
            Open in the studio
          </Button>
        </>
      ) : (
        <p className="rounded-[12px] px-3.5 py-3 text-[12.5px] leading-relaxed text-muted hairline">Ask Claude for a first cut and it shows up here as soon as you apply it.</p>
      )}
    </div>
  );
}
