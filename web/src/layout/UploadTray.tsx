import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router';
import { ArrowClockwise, CaretDown, CaretUp, CheckCircle, CloudArrowUp, CopySimple, WarningCircle, X } from '@phosphor-icons/react';
import { useActivity } from '../api/activity';
import { isActive, isFinished, STALL_MS, uploadFraction, uploads, useUploads, type UploadItem } from '../api/uploads';
import type { ActivityItem } from '../api/types';
import { bytes, plural } from '../lib/format';
import { useNow } from '../lib/hooks';
import { cx, IconButton, Progress, Spinner } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { etaLabel, rateLabel } from './ActivityBar';

const STAGES = ['sending', 'checking', 'fingerprinting', 'storing', 'processing', 'ready'] as const;
type Stage = (typeof STAGES)[number];
const STAGE_LABEL: Record<Stage, string> = {
  sending: 'Sending',
  checking: 'Checking',
  fingerprinting: 'Fingerprinting',
  storing: 'Storing',
  processing: 'Processing',
  ready: 'Ready',
};
const STAGE_HINT: Record<Stage, string> = {
  sending: 'copying the file to MetaRush',
  checking: 'making sure every byte arrived',
  fingerprinting: 'recognizing the video so it is never stored twice',
  storing: 'saving it to your footage storage',
  processing: 'making a preview, a transcript and a scene analysis',
  ready: 'ready to use',
};

const baseName = (p: string) => p.slice(Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\')) + 1);

/** Where an upload is in its journey, combining this tab's state, the server record and the activity summary. */
function stageOf(it: UploadItem, act?: ActivityItem): Stage | null {
  if (['preparing', 'queued', 'starting', 'sending', 'stalled', 'retrying', 'offline', 'interrupted'].includes(it.state)) return 'sending';
  if (it.state === 'server') return (STAGES as readonly string[]).includes(it.stage ?? '') ? (it.stage as Stage) : 'checking';
  if (it.state === 'done') {
    if (act?.stage === 'processing') return 'processing';
    return 'ready';
  }
  return null;
}

function Stepper({ stage, failed }: { stage: Stage; failed?: boolean }) {
  const at = STAGES.indexOf(stage);
  return (
    <ol className="mt-1.5 flex items-center gap-[3px]" aria-label={`Stage ${at + 1} of ${STAGES.length}: ${STAGE_LABEL[stage]}`}>
      {STAGES.map((s, i) => (
        <li
          key={s}
          title={`${STAGE_LABEL[s]}: ${STAGE_HINT[s]}`}
          className={cx('h-[3px] flex-1 rounded-full transition-colors duration-500', failed && i === at ? 'bg-danger' : i < at ? 'bg-accent' : i === at ? 'bg-accent/60' : 'bg-line-strong')}
        />
      ))}
    </ol>
  );
}

function detail(it: UploadItem, act: ActivityItem | undefined, now: number): { text: string; tone?: 'danger' | 'warn' | 'ok' } {
  switch (it.state) {
    case 'preparing':
      return { text: 'Checking whether it is already stored…' };
    case 'queued':
      return { text: it.resumedFrom ? `Resuming from ${bytes(it.resumedFrom)}` : 'Waiting for a free upload slot' };
    case 'starting':
      return { text: 'Starting' };
    case 'sending': {
      const done = it.sent + it.inflight;
      const pct = it.size ? Math.floor((done / it.size) * 100) : 0;
      const parts = [`Sending ${pct}%`, `${bytes(done)} of ${bytes(it.size)}`];
      const r = rateLabel(it.rate);
      if (r) parts.push(r);
      const eta = it.rate ? etaLabel((it.size - done) / it.rate) : null;
      if (eta) parts.push(eta);
      return { text: parts.join(', ') };
    }
    case 'stalled':
      return { text: `No progress for ${Math.round(Math.max(STALL_MS, now - it.lastProgressAt) / 1000)}s. Reconnecting and resuming from ${bytes(it.sent)}…`, tone: 'warn' };
    case 'retrying':
      return { text: `Reconnecting (attempt ${it.attempts})…`, tone: 'warn' };
    case 'offline':
      return { text: it.error ?? 'Cannot reach MetaRush on this machine.', tone: 'danger' };
    case 'interrupted':
      return { text: `Interrupted at ${it.size ? Math.floor((it.sent / it.size) * 100) : 0}%. Drop the same file again to resume.`, tone: 'warn' };
    case 'server': {
      const label = STAGE_LABEL[(stageOf(it) ?? 'checking') as Stage];
      const parts = [label];
      if (it.stageTotal && it.stage !== 'checking') parts[0] = `${label} ${Math.floor(((it.stageDone ?? 0) / it.stageTotal) * 100)}%`;
      if (it.stage === 'checking' || it.stage === 'fingerprinting') parts.push(STAGE_HINT[it.stage]);
      const r = rateLabel(it.rate);
      if (r && it.stage !== 'checking') parts.push(r);
      const eta = it.rate && it.stageTotal ? etaLabel((it.stageTotal - (it.stageDone ?? 0)) / it.rate) : null;
      if (eta) parts.push(eta);
      if (now - it.lastProgressAt > STALL_MS && it.stage !== 'checking') parts.push('no progress for a while');
      return { text: parts.join(', ') };
    }
    case 'waiting':
      return { text: 'Identical to another file in this drop. Keeps both names once that one is stored.' };
    case 'done': {
      if (act?.stage === 'processing') {
        const paused = act.steps?.find((s) => s.state === 'paused');
        if (paused?.waiting) return { text: `${paused.label} ${paused.waiting.text}`, tone: 'warn' };
        return { text: act.stageLabel };
      }
      if (act?.state === 'failed') return { text: act.error ?? 'Processing failed', tone: 'danger' };
      const res = it.result?.resource;
      if (res) {
        const where = res.scope === 'workspace' ? 'the workspace library' : 'Resources';
        return {
          text: res.status === 'same' ? `Already in ${where}` : res.status === 'version' ? `A new version of “${res.title}” in ${where}` : `Added to ${where} as “${res.name}”`,
          tone: 'ok',
        };
      }
      const st = it.result?.status;
      return { text: st === 'deduped' ? 'Already in MetaRush, added here' : st === 'resumed' ? 'Resumed and stored' : 'Stored', tone: 'ok' };
    }
    case 'skipped':
      return { text: it.duplicate ? `Skipped: identical to “${it.duplicate.title}” (${it.duplicate.where})` : 'Skipped: identical to another file in this drop' };
    case 'aliased':
      return { text: it.duplicate ? `Kept both names, stored once as “${it.duplicate.title}”` : 'Kept both names, stored once', tone: 'ok' };
    case 'failed':
      return { text: it.error || 'Failed', tone: 'danger' };
    case 'canceled':
      return { text: 'Canceled' };
  }
}

export function UploadTray() {
  const items = useUploads();
  const act = useActivity();
  const toast = useToast();
  const navigate = useNavigate();
  // Phones start with the tray folded so it does not cover the page.
  const phone = useMemo(() => !!window.matchMedia?.('(max-width: 639px)').matches, []);
  const [open, setOpen] = useState(!phone);
  const now = useNow(1000);

  useEffect(() => {
    void uploads.hydrate(); // rebuild from server state after a reload
  }, []);

  const byUpload = useMemo(() => {
    const m = new Map<string, ActivityItem>();
    for (const a of act.data?.items ?? []) if (a.uploadId) m.set(a.uploadId, a);
    return m;
  }, [act.data]);

  useEffect(
    () =>
      uploads.onFinished((it) => {
        if (it.state === 'failed') toast({ tone: 'error', title: `${baseName(it.name)} failed`, body: it.error });
      }),
    [toast],
  );

  // Once every upload is stored (nothing sending, interrupted or failed), the
  // tray folds away after a moment and clears itself a minute later, so it never
  // sits on top of the library. Processing carries on in the activity bar and
  // on each clip; new uploads open the tray again.
  const activeCount = items.filter(isActive).length;
  const troubled = items.some((x) => x.state === 'interrupted' || x.state === 'failed' || x.state === 'offline' || x.state === 'stalled');
  const settled = items.length > 0 && activeCount === 0 && !troubled;
  useEffect(() => {
    if (troubled || (activeCount > 0 && !phone)) setOpen(true);
  }, [activeCount, troubled, phone]);
  useEffect(() => {
    if (!settled) return;
    const fold = setTimeout(() => setOpen(false), 4000);
    const clear = setTimeout(() => uploads.clearFinished(), 60_000);
    return () => {
      clearTimeout(fold);
      clearTimeout(clear);
    };
  }, [settled, items.length]);

  if (items.length === 0) return null;
  const active = items.filter(isActive);
  const sendingNow = items.filter((x) => ['sending', 'stalled', 'retrying', 'starting', 'queued'].includes(x.state));
  const total = sendingNow.reduce((s, x) => s + x.size, 0);
  const sent = sendingNow.reduce((s, x) => s + Math.min(x.size, x.sent + x.inflight), 0);
  const rate = sendingNow.reduce((s, x) => s + (x.state === 'sending' ? x.rate ?? 0 : 0), 0);
  const overall = active.length ? active.reduce((s, x) => s + uploadFraction(x), 0) / active.length : 1;
  const interrupted = items.filter((x) => x.state === 'interrupted').length;
  const processing = items.filter((x) => x.state === 'done' && x.serverId && byUpload.get(x.serverId)?.stage === 'processing').length;
  const finished = items.filter(isFinished).length;
  const headline = active.length
    ? `Uploading ${plural(active.length, 'file')}`
    : interrupted
      ? `${plural(interrupted, 'upload')} interrupted`
      : processing
        ? `Processing ${plural(processing, 'file')}`
        : `${plural(finished, 'upload')} finished`;
  const sub = sendingNow.length
    ? [`${bytes(sent)} of ${bytes(total)}`, rateLabel(rate), rate ? etaLabel((total - sent) / rate) : null].filter(Boolean).join(', ')
    : active.length
      ? 'Checking and storing on this machine'
      : interrupted
        ? 'Drop the same files again to resume where they stopped.'
        : processing
          ? 'Making previews, transcripts and scene analysis.'
          : 'Everything you added is stored.';

  return (
    <section
      aria-label="Uploads"
      className="fade-up fixed bottom-3 left-3 right-3 z-30 overflow-hidden rounded-[16px] bg-raised shadow-[var(--shadow-float)] hairline sm:bottom-5 sm:left-5 sm:right-auto sm:w-[420px]"
    >
      <div className="flex items-center gap-3 px-3.5 py-3">
        <div className={cx('grid h-8 w-8 shrink-0 place-items-center rounded-[10px]', active.length ? 'bg-accent-soft text-accent-text' : interrupted ? 'bg-accent-soft text-accent-text' : 'bg-ok-soft text-ok')}>
          {active.length ? <CloudArrowUp size={17} /> : interrupted ? <WarningCircle size={17} /> : processing ? <Spinner size={14} /> : <CheckCircle size={17} weight="fill" />}
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            <span className="truncate text-[13px] font-medium text-fg">{headline}</span>
            {active.length ? <span className="tc ml-auto text-[11.5px] text-muted">{Math.floor(overall * 100)}%</span> : null}
          </div>
          {active.length ? <Progress className="mt-1.5" value={overall} label="Total upload progress" /> : null}
          <div className="mt-1 truncate text-[11.5px] text-subtle" aria-live="polite">
            {sub}
          </div>
        </div>
        {finished > 0 ? (
          <button type="button" onClick={() => uploads.clearFinished()} className="rounded-[7px] px-1.5 py-1 text-[12px] text-subtle hover:bg-hover hover:text-fg">
            Clear
          </button>
        ) : null}
        <IconButton size="sm" label={open ? 'Collapse uploads' : 'Expand uploads'} onClick={() => setOpen(!open)}>
          {open ? <CaretDown size={14} /> : <CaretUp size={14} />}
        </IconButton>
      </div>
      {open ? (
        <ul className="max-h-[42dvh] overflow-y-auto border-t border-line px-1.5 py-1.5">
          {items.map((it) => {
            const a = it.serverId ? byUpload.get(it.serverId) : undefined;
            const st = stageOf(it, a);
            const d = detail(it, a, now);
            const title = a?.title && it.state === 'done' ? a.title : baseName(it.name);
            const failedStage = it.state === 'failed' || a?.state === 'failed';
            return (
              <li key={it.key} className="group flex items-start gap-3 rounded-[10px] px-2 py-2 hover:bg-hover">
                <div className="mt-[3px] w-4 shrink-0 text-center">
                  {it.state === 'done' && a?.stage !== 'processing' && a?.state !== 'failed' ? (
                    <CheckCircle size={15} weight="fill" className="text-ok" />
                  ) : it.state === 'aliased' ? (
                    <CheckCircle size={15} weight="fill" className="text-ok" />
                  ) : it.state === 'skipped' ? (
                    <CopySimple size={14} className="text-info" />
                  ) : failedStage || it.state === 'offline' ? (
                    <WarningCircle size={15} weight="fill" className="text-danger" />
                  ) : it.state === 'interrupted' || it.state === 'stalled' ? (
                    <WarningCircle size={15} weight="fill" className="text-accent-text" />
                  ) : it.state === 'canceled' ? (
                    <X size={14} className="text-subtle" />
                  ) : (
                    <Spinner size={12} className="text-accent" />
                  )}
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex items-baseline gap-2">
                    <span className="min-w-0 truncate text-[12.5px] font-medium text-fg" title={it.name}>
                      {title}
                    </span>
                    <span className="ml-auto max-w-[40%] shrink-0 truncate text-[11px] text-subtle">{it.restored ? a?.subtitle ?? it.targetLabel : it.targetLabel}</span>
                  </div>
                  <div className={cx('mt-0.5 text-[11.5px] leading-snug', d.tone === 'danger' ? 'text-danger' : d.tone === 'warn' ? 'text-accent-text' : 'text-muted')} title={d.text}>
                    {d.text}
                  </div>
                  {st && st !== 'ready' && !['skipped', 'aliased', 'canceled'].includes(it.state) ? <Stepper stage={st} failed={failedStage} /> : null}
                  {a?.steps?.length && it.state === 'done' && a.stage === 'processing' ? (
                    <div className="mt-1 flex flex-wrap items-center gap-1">
                      {a.steps
                        .filter((s) => s.state === 'running' || s.state === 'paused' || s.state === 'failed')
                        .map((s) => (
                          <span
                            key={s.kind}
                            className={cx(
                              'inline-flex h-[18px] items-center gap-1 rounded-full px-1.5 text-[10px] font-medium',
                              s.state === 'failed' ? 'bg-danger-soft text-danger' : s.state === 'paused' ? 'bg-accent-soft text-accent-text' : 'bg-info-soft text-info',
                            )}
                          >
                            {s.state === 'running' ? <Spinner size={7} /> : null}
                            {s.label}
                            {s.state === 'paused' ? ' paused' : s.state === 'failed' ? ' failed' : ''}
                          </span>
                        ))}
                      <span className="text-[10.5px] text-subtle">
                        {a.steps.filter((s) => s.state === 'done').length} of {a.steps.length} ready
                      </span>
                    </div>
                  ) : null}
                  {it.state === 'done' && it.result?.resource && it.projectId ? (
                    <button type="button" onClick={() => navigate(`/p/${it.projectId}?tab=resources&resource=${it.result!.resource!.id}`)} className="mt-1 text-[11.5px] font-medium text-accent-text hover:underline">
                      Open in Resources
                    </button>
                  ) : null}
                  {it.state === 'done' && a?.link && a.stage === 'ready' ? (
                    <button type="button" onClick={() => navigate(a.link!)} className="mt-1 text-[11.5px] font-medium text-accent-text hover:underline">
                      Open
                    </button>
                  ) : null}
                </div>
                <div className="flex shrink-0 items-center">
                  {it.file && (it.state === 'offline' || (it.state === 'failed' && it.retriable !== false)) ? (
                    <IconButton size="sm" label="Resume upload" onClick={() => void uploads.resume(it.key)}>
                      <ArrowClockwise size={13} />
                    </IconButton>
                  ) : null}
                  {['preparing', 'queued', 'starting', 'sending', 'stalled', 'retrying', 'offline', 'waiting'].includes(it.state) ? (
                    <IconButton size="sm" label="Cancel upload" onClick={() => void uploads.cancel(it.key)}>
                      <X size={13} />
                    </IconButton>
                  ) : isFinished(it) || it.state === 'interrupted' ? (
                    <IconButton size="sm" label={it.state === 'interrupted' ? 'Discard interrupted upload' : 'Dismiss'} className="opacity-0 focus:opacity-100 group-hover:opacity-100 [@media(hover:none)]:opacity-100" onClick={() => uploads.dismiss(it.key)}>
                      <X size={13} />
                    </IconButton>
                  ) : null}
                </div>
              </li>
            );
          })}
        </ul>
      ) : null}
    </section>
  );
}
