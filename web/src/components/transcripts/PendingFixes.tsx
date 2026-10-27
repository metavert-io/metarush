import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { CaretDown, TextAa } from '@phosphor-icons/react';
import { call } from '../../api/client';
import { useOp } from '../../api/queries';
import { timecode } from '../../lib/format';
import { Button, cx, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { SnippetButton } from '../SnippetPlayer';
import { plainError } from './useFixes';

/**
 * Fixes queue up; renders don't pile up (D148). A saved version keeps the
 * words it was made with; when text fixes since touch its words, this says
 * so and makes one new version (and one review copy) for all of them with one
 * click. Nothing renders by itself.
 */
export function PendingFixesBanner({ projectId, className }: { projectId: string; className?: string }) {
  const pend = useOp('transcript.pending', { projectId }, { staleTime: 10_000 });
  const qc = useQueryClient();
  const toast = useToast();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const d = pend.data;
  if (!d || !d.revisionId || (d.count === 0 && !d.transcribed.length)) return null;
  const make = async () => {
    setBusy(true);
    try {
      const out = await call('transcript.pending.apply', { projectId });
      for (const k of ['transcript.pending', 'revision.list', 'project.show', 'render.list', 'captions.check']) void qc.invalidateQueries({ queryKey: [k] });
      toast({
        tone: 'success',
        title: out.summary,
        action: { label: 'Open it', onClick: () => void navigate(`/p/${projectId}/r/${out.revision.id}`) },
      });
    } catch (err) {
      toast({ tone: 'error', title: 'No new version was made', body: plainError(err) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className={cx('rounded-[12px] bg-accent-soft px-3 py-2.5', className)} role="status" data-pending-fixes={d.count}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <TextAa size={16} className="shrink-0 text-accent-text" aria-hidden />
        <p className="min-w-0 flex-1 text-[13px] text-fg">
          {d.summary}
          {d.latest ? <span className="text-muted"> Make a new version and a review copy to use them.</span> : null}
        </p>
        <button type="button" onClick={() => setOpen(!open)} aria-expanded={open} className="inline-flex items-center gap-1 text-[12px] font-medium text-accent-text hover:underline">
          {open ? 'Hide' : 'See them'} <CaretDown size={10} className={cx('transition-transform', open && 'rotate-180')} />
        </button>
        {d.latest ? (
          <Button size="sm" variant="primary" loading={busy} onClick={() => void make()} data-pending-apply>
            Make the new version
          </Button>
        ) : null}
      </div>
      {open ? (
        <ul className="mt-2 max-h-56 space-y-1 overflow-y-auto border-t border-line pt-2" data-pending-places>
          {d.places.map((p) => (
            <li key={p.id + (p.takenBack ? ':back' : '')} className="flex items-center gap-2 text-[12.5px] text-muted">
              {p.kind === 'words' ? <SnippetButton assetId={p.assetId} from={Math.max(0, p.from - 1)} to={p.to + 1} label={`what is said at ${timecode(p.from, { ms: false })}`} size="sm" /> : null}
              <span className="tc w-12 shrink-0 text-subtle">{p.at >= 0 ? timecode(p.at, { ms: false }) : ''}</span>
              <span className="min-w-0 flex-1 truncate">
                <Theirs className="text-muted">{p.clip}</Theirs>:{' '}
                {p.kind === 'transcribed' ? (
                  'transcribed again'
                ) : p.kind === 'speaker' ? (
                  <>
                    who speaks, <Theirs>“{p.beforeText}”</Theirs> → <Theirs className="font-medium text-fg">“{p.afterText}”</Theirs>
                  </>
                ) : p.takenBack ? (
                  <>
                    <Theirs>“{p.afterText}”</Theirs> goes back to <Theirs className="font-medium text-fg">“{p.beforeText}”</Theirs> (undone)
                  </>
                ) : (
                  <>
                    <Theirs>“{p.beforeText}”</Theirs> → <Theirs className="font-medium text-fg">“{p.afterText}”</Theirs>
                  </>
                )}
              </span>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
