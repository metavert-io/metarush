import { useState } from 'react';
import { ArrowCounterClockwise, ClockCounterClockwise } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import type { TranscriptHistoryEntry } from '../../api/types';
import { relative, timecode } from '../../lib/format';
import { Badge, Button, Details, EmptyState, ErrorPanel, Skeleton } from '../../ui/primitives';
import { useFixes } from './useFixes';

/**
 * A clip's transcript history (D145): every fix with who made it and when,
 * newest first; undo one fix or a whole find and replace. The computer's own
 * transcript is kept and can be read here.
 */
export function TranscriptHistory({ assetId }: { assetId: string }) {
  const h = useOp('transcript.history', { assetId });
  const fixes = useFixes();
  const [showOriginal, setShowOriginal] = useState(false);
  const original = useOp('footage.transcript', showOriginal ? { assetId, original: true } : null);
  if (h.isLoading) return <Skeleton className="h-40" />;
  if (h.error) return <ErrorPanel error={h.error} onRetry={() => void h.refetch()} compact />;
  const data = h.data!;
  return (
    <div className="space-y-3" data-transcript-history>
      <p className="text-[12.5px] text-muted">{data.summary}</p>
      {data.carry && data.carry.missed.length ? (
        <div className="rounded-[12px] bg-info-soft p-2.5 text-[12.5px] text-fg">
          When it was transcribed again, {data.carry.missed.length === 1 ? '1 fix' : `${data.carry.missed.length} fixes`} didn't carry over:
          <ul className="mt-1 space-y-0.5 text-muted">
            {data.carry.missed.map((m) => (
              <li key={m.fixId}>
                {timecode(m.from, { ms: false })} — your “{m.afterText}”; it says “{m.heard}” there now
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {data.entries.length === 0 ? (
        <EmptyState compact icon={<ClockCounterClockwise size={20} />} title="No fixes yet" body="Fix a word in the transcript and it shows here, with who fixed it and when." />
      ) : (
        <ul className="max-h-[min(52dvh,460px)] divide-y divide-line overflow-y-auto rounded-[12px] hairline">
          {data.entries.map((e) => (
            <HistoryRow key={e.id} e={e} onUndo={(batch) => void fixes.undo(batch ? { batchId: e.batchId } : { fixId: e.id })} />
          ))}
        </ul>
      )}
      {data.original ? (
        <div>
          <Button size="sm" variant="ghost" onClick={() => setShowOriginal(!showOriginal)} data-show-original>
            {showOriginal ? 'Hide' : 'Show'} the computer's own transcript
          </Button>
          {showOriginal ? (
            <div className="mt-2 max-h-56 overflow-y-auto rounded-[12px] bg-panel p-3 text-[12.5px] leading-relaxed text-muted hairline" data-original-transcript>
              {original.isLoading ? <Skeleton className="h-10" /> : (original.data?.segments ?? []).map((s) => (
                <p key={s.id}>
                  <span className="tc mr-2 text-subtle">{timecode(s.from, { ms: false })}</span>
                  {s.text}
                </p>
              ))}
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function HistoryRow({ e, onUndo }: { e: TranscriptHistoryEntry; onUndo: (batch: boolean) => void }) {
  const state = e.undone ? (
    <Badge tone="neutral">Undone</Badge>
  ) : e.superseded ? (
    <Badge tone="danger">Didn't carry over</Badge>
  ) : null;
  return (
    <li className="flex flex-wrap items-center gap-2 px-3 py-2" data-history-entry={e.id}>
      <div className="min-w-0 flex-1">
        <div className="text-[13px] text-fg">{e.says}</div>
        <div className="text-[11.5px] text-subtle">
          {e.who}, {relative(e.at)}
          {e.batchSize > 1 ? ` · one of ${e.batchSize} changed together` : ''}
        </div>
        {!e.canUndo && !e.undone && e.why && e.kind !== 'transcribed' ? <div className="text-[11.5px] text-subtle">{e.why}</div> : null}
      </div>
      {state}
      {e.canUndo && !e.undone ? (
        <span className="flex gap-1">
          <Button size="sm" variant="ghost" icon={<ArrowCounterClockwise size={13} />} onClick={() => onUndo(false)} data-history-undo>
            Undo
          </Button>
          {e.batchSize > 1 ? (
            <Button size="sm" variant="ghost" onClick={() => onUndo(true)}>
              Undo all {e.batchSize}
            </Button>
          ) : null}
        </span>
      ) : null}
      <Details compact className="w-full">
        <span className="tc">
          {timecode(e.from)}–{timecode(e.to)} · {e.source} · {e.id}
        </span>
      </Details>
    </li>
  );
}
