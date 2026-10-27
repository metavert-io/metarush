import { useEffect, useRef, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router';
import { ArrowClockwise, ArrowRight, BatteryWarning, DownloadSimple, Lightning, MusicNotes, Question, ShieldCheck, VideoCamera, X } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call, media, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import type { JobShowOut, JobTarget } from '../api/types';
import { absolute, bytes, duration as fmtDuration, relative } from '../lib/format';
import { useNow } from '../lib/hooks';
import { useDialog } from '../ui/Dialog';
import { Button, cx, Details, ErrorPanel, Progress, Skeleton, StateBadge, Thumb } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { etaLabel } from '../layout/ActivityBar';
import { QAChip, useQAReportDialog } from './QAReport';
import { RenderDownload } from './SignoffReminder';
import { RecoveringVideo } from '../player/Trouble';

/**
 * "Go to …" for a job: where its result lives, with the object selected. The
 * server builds the link with the same helper notifications and activity rows
 * use (internal/links), so every entry point lands in the same place.
 */
export function jobTarget(job: Pick<JobShowOut, 'target'>): JobTarget | undefined {
  return job.target ?? undefined;
}

const stateWords: Record<string, string> = { queued: 'Waiting', running: 'Running', succeeded: 'Finished', failed: 'Failed', canceled: 'Canceled' };

function took(s?: number | null): string | null {
  if (s == null || !Number.isFinite(s)) return null;
  return fmtDuration(s);
}

function Attempts({ job }: { job: JobShowOut }) {
  if (!job.attempts.length) return null;
  return (
    <div className="mt-5">
      <div className="text-[12px] font-medium text-muted">Attempts</div>
      <ul className="mt-1.5 space-y-1">
        {job.attempts.map((a, i) => {
          const dur = a.endedAt ? (new Date(a.endedAt).getTime() - new Date(a.startedAt).getTime()) / 1000 : null;
          const outcome =
            a.outcome === 'succeeded'
              ? 'finished'
              : a.outcome === 'retry'
                ? 'failed, tried again'
                : a.outcome === 'released'
                  ? 'stopped when MetaRush on that machine stopped; waiting to start again (not counted)'
                  : a.outcome === 'postponed'
                    ? "something it needs wasn't there yet; waited a moment to look again (not counted)"
                    : a.outcome || (i === job.attempts.length - 1 && job.state === 'running' ? 'running' : 'stopped');
          return (
            <li key={i} className="flex flex-wrap items-center gap-x-2 text-[12px] text-muted">
              <span className="tc text-subtle">{i + 1}.</span>
              <span className="text-fg">{a.workerName || 'a machine'}</span>
              <span title={absolute(a.startedAt)}>{relative(a.startedAt)}</span>
              {dur != null ? <span>for {took(dur)}</span> : null}
              <span className={cx(a.outcome === 'failed' || a.outcome === 'retry' ? 'text-danger' : a.outcome === 'succeeded' ? 'text-ok' : 'text-subtle')}>{outcome}</span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function JobBody({ id, close }: { id: string; close: (how?: 'navigated') => void }) {
  const q = useOp('job.show', { id }, { refetchInterval: (s) => (s.state.data && ['queued', 'running'].includes(s.state.data.state) ? 2000 : false) });
  const qc = useQueryClient();
  const navigate = useNavigate();
  const toast = useToast();
  const openQA = useQAReportDialog();
  const [busy, setBusy] = useState<string | null>(null);
  const now = useNow(1000);
  const j = q.data;
  const act = async (key: string, fn: () => Promise<unknown>, ok: string) => {
    setBusy(key);
    try {
      await fn();
      toast({ tone: 'success', title: ok });
      for (const k of ['job.show', 'job.list', 'activity.summary', 'render.list', 'project.show']) void qc.invalidateQueries({ queryKey: [k] });
    } catch (e) {
      toast({ tone: 'error', title: 'That did not work', body: toApiError(e).message });
    } finally {
      setBusy(null);
    }
  };
  const go = (link?: string) => {
    if (!link) return;
    close('navigated');
    navigate(link);
  };

  if (q.isLoading)
    return (
      <div className="space-y-3">
        <Skeleton className="h-7 w-2/3" />
        <Skeleton className="h-4 w-1/2" />
        <Skeleton className="h-40" />
      </div>
    );
  if (q.error || !j) return <ErrorPanel error={q.error} onRetry={() => void q.refetch()} title={q.error?.code === 'E_NOT_FOUND' ? 'This job no longer exists' : undefined} />;

  const r = j.related;
  const target = jobTarget(j);
  const startedAt = j.attempts.length ? j.attempts[j.attempts.length - 1]!.startedAt : j.createdAt;
  const elapsed = (now - new Date(startedAt).getTime()) / 1000;
  const eta = j.state === 'running' && j.progress.pct > 0.03 ? etaLabel((elapsed * (1 - j.progress.pct)) / j.progress.pct) : null;
  const render = r.render;
  const vertical = render ? (render.height ?? 0) > (render.width ?? 0) : false;

  return (
    <div>
      <div className="pr-8">
        <h2 className="text-[17px] font-semibold leading-snug tracking-[-0.015em] text-fg">{j.title}</h2>
        <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[12.5px] text-muted">
          <StateBadge state={j.state} label={j.cancelRequested && j.state === 'running' ? 'Stopping' : stateWords[j.state] ?? j.state} />
          <span>{j.startedBy}</span>
          <span title={absolute(j.createdAt)}>{relative(j.createdAt)}</span>
          {j.machine ? <span>on {j.machine}</span> : null}
          {j.durationSeconds != null && j.state !== 'running' ? <span>took {took(j.durationSeconds)}</span> : null}
        </div>
      </div>

      {j.state === 'running' ? (
        <div className="mt-5 rounded-[12px] px-4 py-3.5 hairline">
          <div className="flex items-baseline gap-2 text-[13px]">
            <span className="font-medium text-fg">{j.progress.message ? j.progress.message[0]!.toUpperCase() + j.progress.message.slice(1) : 'Working'}</span>
            <span className="tc ml-auto text-muted">{Math.round(j.progress.pct * 100)}%</span>
          </div>
          <Progress className="mt-2" value={j.progress.pct} indeterminate={!j.progress.pct} label="Job progress" />
          <div className="mt-1.5 text-[12px] text-subtle">{eta ?? `Running for ${took(elapsed)}`}</div>
        </div>
      ) : null}

      {j.state === 'queued' && j.waiting ? (
        <div className={cx('mt-5 flex items-start gap-3 rounded-[12px] px-4 py-3.5', j.waiting.code === 'battery' ? 'bg-accent-soft' : 'hairline')}>
          {j.waiting.code === 'battery' ? <BatteryWarning size={18} weight="fill" className="mt-[1px] shrink-0 text-accent-text" /> : null}
          <div className="min-w-0 flex-1">
            <div className="text-[13px] font-medium text-fg">{j.waiting.text[0]!.toUpperCase() + j.waiting.text.slice(1)}</div>
            <div className="mt-0.5 text-[12px] text-muted">
              {j.waiting.code === 'battery'
                ? 'Big jobs wait while the laptop is unplugged and low. They start on their own when it charges.'
                : j.waiting.code === 'no_worker'
                  ? 'Open MetaRush on a machine that can do this, and it starts there.'
                  : j.waiting.code === 'busy'
                    ? 'It starts as soon as the machine finishes what it is doing.'
                    : j.waiting.code === 'retry_backoff'
                      ? 'It failed a moment ago and will try again by itself.'
                      : 'It will start in a moment.'}
            </div>
          </div>
          {j.canProcessNow ? (
            <Button size="sm" variant="primary" loading={busy === 'now'} icon={<Lightning size={12} weight="fill" />} onClick={() => void act('now', () => call('job.expedite', { ids: [j.id] }), 'Starting it now')}>
              Process now
            </Button>
          ) : null}
        </div>
      ) : null}

      {j.state === 'succeeded' && render?.mediaUrl ? (
        <div className="mt-5">
          <RecoveringVideo src={render.mediaUrl} controls playsInline preload="metadata" className={cx('mx-auto rounded-[12px] bg-black', vertical ? 'max-h-[52dvh]' : 'w-full')} />
          <div className="mt-2 flex flex-wrap items-center gap-2 text-[12px] text-muted">
            <span>
              {render.width}×{render.height}, {fmtDuration(render.duration)}, {bytes(render.size)}
            </span>
            {render.qa ? <QAChip verdict={render.qa.verdict} errors={render.qa.errors} warnings={render.qa.warnings} onOpen={() => void openQA(render.qa!.reportId)} /> : null}
          </div>
        </div>
      ) : null}

      {r.asset && j.type !== 'render' && j.type !== 'qa' ? (
        <div className="mt-5 flex items-center gap-3 rounded-[12px] p-2.5 hairline">
          <Thumb src={r.asset.hasThumb ? media.asset(r.asset.id, 'thumbnail') : undefined} alt="" className="h-12 w-[76px] shrink-0 rounded-[8px]" fallback={r.asset.kind === 'audio' ? <MusicNotes size={15} /> : <VideoCamera size={15} />} />
          <div className="min-w-0 flex-1">
            <div className="truncate text-[13.5px] font-medium text-fg">{r.asset.title}</div>
            <div className="truncate text-[12px] text-muted">{j.produced?.summary ?? `${fmtDuration(r.asset.duration)} ${r.asset.kind}`}</div>
          </div>
        </div>
      ) : j.produced && j.state === 'succeeded' && !render?.mediaUrl ? (
        <div className="mt-5 text-[13px] text-muted">{j.produced.summary}</div>
      ) : null}

      {j.questions ? (
        <button
          type="button"
          onClick={() => go(j.questions!.link)}
          className="mt-3 flex w-full items-center gap-2.5 rounded-[12px] bg-accent-soft px-3.5 py-2.5 text-left transition-colors hover:bg-[color-mix(in_oklab,var(--accent)_22%,transparent)]"
        >
          <Question size={16} weight="bold" className="shrink-0 text-accent-text" />
          <span className="min-w-0 flex-1 text-[13px] font-medium text-fg">{j.questions.text[0]!.toUpperCase() + j.questions.text.slice(1)}</span>
          <span className="inline-flex shrink-0 items-center gap-1 text-[12.5px] font-medium text-accent-text">
            Answer <ArrowRight size={12} />
          </span>
        </button>
      ) : null}

      {j.type === 'qa' && r.qaReport ? (
        <div className="mt-5 rounded-[12px] px-4 py-3 hairline">
          <div className="flex items-center gap-2 text-[13px] font-medium text-fg">
            <ShieldCheck size={16} className="text-subtle" /> {j.produced?.summary}
          </div>
          {r.qaReport.findings.length ? (
            <ul className="mt-2 space-y-1">
              {r.qaReport.findings.map((f, i) => (
                <li key={i} className="text-[12px] text-muted">
                  <span className="tc text-accent-text">{f.timecode}</span> {f.message}
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}

      {j.state === 'failed' || j.state === 'canceled' ? (
        <div className="mt-5 rounded-[12px] bg-danger-soft px-4 py-3.5">
          <div className="text-[13px] font-medium text-fg">{j.state === 'canceled' ? 'This job was canceled.' : 'This job did not finish.'}</div>
          {j.advice ? <div className="mt-1 text-[12.5px] leading-relaxed text-muted">{j.advice}</div> : null}
          {j.logLink ? (
            <button type="button" onClick={() => go(j.logLink)} className="mt-2 inline-flex items-center gap-1 text-[12.5px] font-medium text-accent-text hover:underline">
              See log <ArrowRight size={12} />
            </button>
          ) : null}
        </div>
      ) : null}

      <Attempts job={j} />

      {j.error || j.attempts.some((a) => a.error) ? (
        <Details>
          <pre className="tc max-h-[30dvh] overflow-auto whitespace-pre-wrap break-words text-[11.5px] leading-relaxed text-muted">
            {[j.error, ...j.attempts.filter((a) => a.error && a.error !== j.error).map((a, i) => `Attempt ${i + 1}: ${a.error}`)].filter(Boolean).join('\n\n')}
          </pre>
          <div className="tc mt-2 text-[11px] text-subtle">
            {j.type} job {j.id}
          </div>
        </Details>
      ) : null}

      <div className="mt-6 flex flex-wrap items-center justify-end gap-2 border-t border-line pt-4">
        {j.canCancel && !j.cancelRequested ? (
          <Button variant="ghost" className="mr-auto" loading={busy === 'cancel'} icon={<X size={13} />} onClick={() => void act('cancel', () => call('job.cancel', { id: j.id }), 'Stopping it')}>
            Cancel job
          </Button>
        ) : null}
        {j.canRetry ? (
          <Button variant={target ? 'secondary' : 'primary'} loading={busy === 'retry'} icon={<ArrowClockwise size={13} />} onClick={() => void act('retry', () => call('job.retry', { id: j.id }), 'Trying again')}>
            Retry
          </Button>
        ) : null}
        {render?.mediaUrl ? (
          <RenderDownload
            href={`${render.mediaUrl}?download=1`}
            render={{ id: render.id, quality: render.quality, projectId: r.project?.id ?? j.projectId }}
            className="inline-flex h-9 items-center gap-2 rounded-[10px] px-3.5 text-[13.5px] text-fg hairline hover:bg-hover"
          >
            <DownloadSimple size={14} /> Download
          </RenderDownload>
        ) : null}
        {render && r.revision && r.project ? (
          <Button variant="secondary" onClick={() => go(`/p/${r.project!.id}/r/${r.revision!.id}`)}>
            Open version {render.revisionNumber}
          </Button>
        ) : null}
        {j.type === 'render' && j.state === 'succeeded' && render && !render.qa ? (
          <Button variant="secondary" loading={busy === 'qa'} icon={<ShieldCheck size={13} />} onClick={() => void act('qa', () => call('qa.run', { id: render.id }), 'Quality check started')}>
            Check quality
          </Button>
        ) : null}
        {j.type === 'transcribe' && j.state === 'succeeded' && r.asset ? (
          <Button variant="secondary" onClick={() => go(r.asset!.link)}>
            Open transcript
          </Button>
        ) : null}
        {target ? (
          <Button variant="primary" trailing={<ArrowRight size={13} />} onClick={() => go(target.link)}>
            {target.label}
          </Button>
        ) : null}
      </div>
    </div>
  );
}

/**
 * Opens a job's details. The dialog is deep-linkable (?job=<id>): the URL keeps
 * it open across a reload, and closing it removes the parameter.
 */
export function useJobDetails() {
  const [, setParams] = useSearchParams();
  return (id: string) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p);
        n.set('job', id);
        return n;
      },
      { replace: false },
    );
}

/** Mounted once (Shell): shows the job dialog while ?job= is in the URL. */
export function JobDetailsHost() {
  const [params, setParams] = useSearchParams();
  const dialog = useDialog();
  const id = params.get('job');
  const openFor = useRef<string | null>(null);
  useEffect(() => {
    if (!id || openFor.current === id) return;
    openFor.current = id;
    void dialog
      .open<'navigated' | 'closed'>((close) => <JobBody id={id} close={(how) => close(how ?? 'closed')} />, { label: 'Job details', size: 'lg' })
      .then((how) => {
        openFor.current = null;
        // "Go to …" leaves this page: clearing ?job= with this (older) setter
        // would race it and land back here.
        if (how === 'navigated') return;
        setParams(
          (p) => {
            const n = new URLSearchParams(p);
            n.delete('job');
            return n;
          },
          { replace: true },
        );
      });
  }, [id, dialog, setParams]);
  return null;
}
