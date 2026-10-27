import { useEffect, useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { ArrowRight, Check, DownloadSimple, FileText, Lightbulb, X } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import type { ImprovementItem, ImprovementReportSummary } from '../api/types';
import { relative } from '../lib/format';
import { resetSelection } from '../lib/workbench';
import { Markdown } from '../layout/assistant/Markdown';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, CopyBlock, cx, EmptyState, ErrorPanel, Freshness, Skeleton, Tabs, type Tone } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { LogTabs } from './Log';

type State = 'new' | 'kept' | 'dismissed';

const KIND: Record<string, { label: string; tone: Tone }> = {
  'feature-gap': { label: 'Missing feature', tone: 'accent' },
  friction: { label: 'Friction', tone: 'info' },
  bug: { label: 'Problem', tone: 'danger' },
};
const IMPACT: Record<string, string> = { high: 'Big impact', medium: 'Some impact', low: 'Small impact' };

const ASK = 'Look at the MetaRush log and tell me what MetaRush should add or fix.';

/** Ideas for what MetaRush could do better, found in the log by Claude Code. */
export function ImprovementsPage() {
  const [params, setParams] = useSearchParams();
  const state = (params.get('state') as State | null) ?? 'new';
  useEffect(() => resetSelection({}), []);
  const list = useOp('improve.list', { state: 'all', limit: 500 });
  const items = (list.data?.items ?? []).filter((i) => i.state === state);
  const counts = list.data?.counts;
  const reports = list.data?.reports ?? [];
  const toast = useToast();
  const [exporting, setExporting] = useState(false);

  const exportMd = async () => {
    setExporting(true);
    try {
      const out = await call('improve.export', {});
      const url = URL.createObjectURL(new Blob([out.markdown], { type: 'text/markdown' }));
      const a = document.createElement('a');
      a.href = url;
      a.download = out.filename;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 5000);
    } catch (e) {
      toast({ tone: 'error', title: 'Could not export the ideas', body: toApiError(e).message });
    } finally {
      setExporting(false);
    }
  };

  return (
    <div className="mx-auto max-w-[1100px]">
      <div className="mb-6 flex flex-wrap items-end gap-3">
        <div className="min-w-0">
          <h1 className="text-[28px] font-semibold tracking-[-0.03em]">Improvements</h1>
          <p className="mt-1 max-w-[64ch] text-[14px] text-muted">Ideas for what MetaRush could do better, found by Claude Code in the log: things it had to work around, and what kept going wrong.</p>
        </div>
        <div className="ml-auto flex flex-wrap items-center gap-3">
          <LogTabs current="improvements" />
        </div>
      </div>

      <div className="mb-5 flex flex-wrap items-center gap-3">
        <Tabs<State>
          label="Which ideas"
          value={state}
          onChange={(s) => setParams(s === 'new' ? {} : { state: s }, { replace: true })}
          items={[
            { value: 'new', label: 'New', count: counts?.new },
            { value: 'kept', label: 'Kept', count: counts?.kept },
            { value: 'dismissed', label: 'Dismissed', count: counts?.dismissed },
          ]}
        />
        <div className="ml-auto flex items-center gap-3">
          <Freshness at={list.dataUpdatedAt || undefined} fetching={list.isFetching} onRefresh={() => void list.refetch()} />
          <Button size="sm" variant="secondary" icon={<DownloadSimple size={13} />} loading={exporting} disabled={!counts || counts.new + counts.kept === 0} onClick={() => void exportMd()}>
            Export
          </Button>
        </div>
      </div>

      {list.isLoading ? (
        <div className="space-y-3">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-32 rounded-[16px]" />
          ))}
        </div>
      ) : list.error ? (
        <ErrorPanel error={list.error} onRetry={() => void list.refetch()} />
      ) : items.length === 0 ? (
        <div className="panel">
          {state === 'new' && (counts?.kept ?? 0) + (counts?.dismissed ?? 0) === 0 ? (
            <EmptyState
              icon={<Lightbulb size={22} />}
              title="No ideas yet"
              body="Ask Claude Code to look through the log. It groups what went wrong or what MetaRush could not do, ranks it, and saves the ideas here for you to keep or dismiss."
              action={<CopyBlock className="w-full max-w-[560px] text-left" text={ASK} caption="Paste this into Claude Code in a terminal." />}
            />
          ) : (
            <EmptyState icon={<Lightbulb size={22} />} title={state === 'new' ? 'Nothing new' : state === 'kept' ? 'Nothing kept yet' : 'Nothing dismissed'} compact />
          )}
        </div>
      ) : (
        <ul className="space-y-3">
          {items.map((it) => (
            <IdeaCard key={it.id} item={it} />
          ))}
        </ul>
      )}

      {reports.length > 0 ? <Reports reports={reports} /> : null}
    </div>
  );
}

function IdeaCard({ item }: { item: ImprovementItem }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [busy, setBusy] = useState<'keep' | 'dismiss' | null>(null);
  const kind = KIND[item.kind] ?? { label: item.kind, tone: 'neutral' as Tone };
  const decide = async (to: 'keep' | 'dismiss') => {
    setBusy(to);
    try {
      await call(to === 'keep' ? 'improve.keep' : 'improve.dismiss', { ids: [item.id] });
      void qc.invalidateQueries({ queryKey: ['improve.list'] });
      toast({ tone: 'success', title: to === 'keep' ? 'Kept' : 'Dismissed', body: item.title });
    } catch (e) {
      toast({ tone: 'error', title: 'Could not change it', body: toApiError(e).message });
    } finally {
      setBusy(null);
    }
  };
  return (
    <li className="fade-in rounded-[16px] bg-raised p-5 hairline">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={kind.tone}>{kind.label}</Badge>
        <span className="text-[12px] text-subtle">
          {IMPACT[item.impact] ?? item.impact}, seen {item.frequency === 1 ? 'once' : `${item.frequency} times`}
          {item.seen > 1 ? `, in ${item.seen} reports` : ''}
        </span>
        <span className="ml-auto text-[12px] text-subtle" title={new Date(item.lastSeenAt).toLocaleString()}>
          {relative(item.lastSeenAt)}
        </span>
      </div>
      <h2 className="mt-2 text-[16px] font-semibold tracking-[-0.01em] text-fg">{item.title}</h2>
      {item.description ? (
        <div className="mt-1.5 max-w-[78ch] text-[13.5px] leading-relaxed text-muted">
          <Markdown text={item.description} />
        </div>
      ) : null}
      <div className="mt-4 flex flex-wrap items-center gap-2">
        {item.evidence.length > 0 ? (
          <Link to={`/settings/log?ids=${item.evidence.join(',')}`} className="inline-flex h-8 items-center gap-1.5 rounded-[9px] px-3 text-[12.5px] font-medium text-accent-text hover:bg-accent-soft">
            See what shows it ({item.evidence.length}) <ArrowRight size={12} />
          </Link>
        ) : null}
        <div className="ml-auto flex gap-2">
          {item.state !== 'dismissed' ? (
            <Button size="sm" variant="ghost" icon={<X size={13} />} loading={busy === 'dismiss'} disabled={!!busy} onClick={() => void decide('dismiss')}>
              Dismiss
            </Button>
          ) : null}
          {item.state !== 'kept' ? (
            <Button size="sm" variant={item.state === 'dismissed' ? 'secondary' : 'primary'} icon={<Check size={13} />} loading={busy === 'keep'} disabled={!!busy} onClick={() => void decide('keep')}>
              {item.state === 'dismissed' ? 'Keep after all' : 'Keep'}
            </Button>
          ) : null}
        </div>
      </div>
    </li>
  );
}

function Reports({ reports }: { reports: ImprovementReportSummary[] }) {
  const dialog = useDialog();
  const open = (r: ImprovementReportSummary) =>
    void dialog.open(() => <ReportView id={r.id} />, { title: r.title, size: 'lg' });
  return (
    <section className="mt-10" aria-labelledby="reports-h">
      <h2 id="reports-h" className="mb-2 px-1 text-[12.5px] font-semibold text-muted">
        Reports these ideas came from
      </h2>
      <ul className="overflow-hidden rounded-[16px] hairline">
        {reports.map((r, i) => (
          <li key={r.id} className={cx(i > 0 && 'border-t border-line')}>
            <button type="button" onClick={() => open(r)} className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-raised">
              <FileText size={16} className="shrink-0 text-subtle" aria-hidden />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[13.5px] text-fg">{r.title}</span>
                <span className="block text-[12px] text-subtle">
                  {r.itemCount === 1 ? '1 idea' : `${r.itemCount} ideas`}
                  {r.by ? `, saved by ${r.by}` : ''}
                </span>
              </span>
              <span className="shrink-0 text-[12px] text-subtle">{relative(r.createdAt)}</span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}

function ReportView({ id }: { id: string }) {
  const s = useOp('improve.show', { id });
  if (s.isLoading) return <Skeleton className="h-40" />;
  if (s.error) return <ErrorPanel error={s.error} compact />;
  return (
    <div className="max-h-[70dvh] overflow-y-auto text-[13.5px] leading-relaxed">
      <Markdown text={s.data?.report?.markdown ?? ''} />
    </div>
  );
}
