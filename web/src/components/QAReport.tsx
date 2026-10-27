import { CheckCircle, Info, ShieldCheck, Warning, WarningCircle } from '@phosphor-icons/react';
import { useOp } from '../api/queries';
import { relative } from '../lib/format';
import { useDialog } from '../ui/Dialog';
import { cx, ErrorPanel, Skeleton } from '../ui/primitives';

const sevLabel: Record<string, string> = { error: 'Problem', warning: 'Worth checking', info: 'Note' };

/** A render's quality check, in plain words: what was found, where, and what it means. */
export function QAReportBody({ id }: { id: string }) {
  const q = useOp('qa.show', { id });
  if (q.isLoading)
    return (
      <div className="space-y-2">
        <Skeleton className="h-14" />
        <Skeleton className="h-10" />
        <Skeleton className="h-10" />
      </div>
    );
  if (q.error) return <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />;
  const r = q.data?.report;
  if (!r) return null;
  const pass = r.counts.errors === 0 && r.counts.warnings === 0;
  return (
    <div>
      <div className={cx('flex items-center gap-3 rounded-[12px] px-3.5 py-3', pass ? 'bg-ok-soft' : r.counts.errors ? 'bg-danger-soft' : 'bg-accent-soft')}>
        {pass ? <CheckCircle size={20} weight="fill" className="text-ok" /> : r.counts.errors ? <WarningCircle size={20} weight="fill" className="text-danger" /> : <Warning size={20} weight="fill" className="text-accent-text" />}
        <div className="min-w-0">
          <div className="text-[14px] font-semibold text-fg">{pass ? 'No problems found' : r.counts.errors ? `${r.counts.errors} ${r.counts.errors === 1 ? 'problem' : 'problems'} to fix` : `${r.counts.warnings} ${r.counts.warnings === 1 ? 'thing' : 'things'} worth checking`}</div>
          <div className="text-[12px] text-muted">
            Version {r.revisionNumber}, {r.quality} render, checked {relative(r.createdAt)}
            {r.runBy ? ` on ${r.runBy}` : ''}
          </div>
        </div>
      </div>
      {r.findings.length ? (
        <ul className="mt-4 space-y-1.5">
          {r.findings.map((f) => (
            <li key={f.id} className="flex gap-3 rounded-[10px] px-3 py-2 hairline">
              <span className={cx('mt-[3px] shrink-0', f.severity === 'error' ? 'text-danger' : f.severity === 'warning' ? 'text-accent-text' : 'text-info')}>
                {f.severity === 'error' ? <WarningCircle size={15} weight="fill" /> : f.severity === 'warning' ? <Warning size={15} weight="fill" /> : <Info size={15} weight="fill" />}
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-baseline gap-x-2">
                  <span className="text-[12px] font-medium text-fg">{sevLabel[f.severity] ?? f.severity}</span>
                  <span className="tc text-[11.5px] text-accent-text">at {f.timecode}</span>
                  {f.basis === 'heuristic' ? <span className="text-[11px] text-subtle">(estimated)</span> : null}
                </div>
                <div className="mt-0.5 text-[12.5px] leading-relaxed text-muted">{f.message}</div>
                {f.fix?.summary ? <div className="mt-1 text-[12px] text-subtle">Suggested fix: {f.fix.summary}</div> : null}
              </div>
            </li>
          ))}
        </ul>
      ) : null}
      <p className="mt-4 text-[11.5px] leading-relaxed text-subtle">
        The check plays the finished video and compares it with the cut: black or frozen frames, how loud it sounds, silence where someone should be speaking, and text that might be cut off. Ask Claude Code to fix anything listed here.
      </p>
    </div>
  );
}

export function useQAReportDialog() {
  const dialog = useDialog();
  return (id: string) =>
    dialog.open<never>(() => <QAReportBody id={id} />, { title: 'Quality check', size: 'md' }).then(() => undefined);
}

export function QAChip({ verdict, errors, warnings, onOpen }: { verdict: string; errors: number; warnings: number; onOpen?: () => void }) {
  const tone = errors ? 'bg-danger-soft text-danger' : warnings ? 'bg-accent-soft text-accent-text' : 'bg-ok-soft text-ok';
  const label = errors ? `${errors} ${errors === 1 ? 'problem' : 'problems'} found` : warnings ? `${warnings} to check` : 'Check passed';
  return (
    <button type="button" onClick={onOpen} title={`Quality check: ${label.toLowerCase()}${onOpen ? '. Open the report' : ''}`} data-verdict={verdict} className={cx('inline-flex h-[22px] items-center gap-1 rounded-full px-2 text-[11.5px] font-medium', tone, onOpen && 'hover:brightness-110')}>
      <ShieldCheck size={12} weight="fill" />
      {label}
    </button>
  );
}
