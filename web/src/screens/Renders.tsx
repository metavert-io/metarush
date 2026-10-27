import { useEffect, useMemo, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router';
import { ArrowClockwise, BatteryWarning, Cpu, FilmReel, Lightning, Plug, Queue, X } from '@phosphor-icons/react';
import { useInstallationNames, useOp, useOpMutation } from '../api/queries';
import type { Job, WorkerRow } from '../api/types';
import { absolute, jobLabel, platformLabel, relative } from '../lib/format';
import { useListNav, useScrollIntoView } from '../lib/hooks';
import { releaseOf } from '../lib/version';
import { resetSelection } from '../lib/workbench';
import { RenderTile } from '../components/Renders';
import { useJobDetails } from '../components/JobDetails';
import { useDialog } from '../ui/Dialog';
import { ownerTitle, useOwnership } from '../components/OwnerOnly';
import { Badge, cx, Details, EmptyState, ErrorPanel, Freshness, IconButton, Progress, SectionTitle, Segmented, Skeleton, StateBadge, stateWord, Tabs, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';

type Tab = 'jobs' | 'renders' | 'machines';
type JobFilter = 'active' | 'failed' | 'all';

export function RendersPage() {
  const { renderId } = useParams();
  const [params, setParams] = useSearchParams();
  const tab = (params.get('tab') as Tab | null) ?? (renderId ? 'renders' : 'jobs');
  useEffect(() => resetSelection({}), []);
  const workers = useOp('worker.list', {}, { refetchInterval: 20_000 });
  const online = (workers.data?.workers ?? []).filter((w) => w.online).length;

  return (
    <div>
      <div className="mb-6 flex flex-wrap items-end gap-3">
        <div>
          <h1 className="text-[28px] font-semibold tracking-[-0.03em]">Renders</h1>
          <p className="mt-1 text-[14px] text-muted">What MetaRush is working on across your projects, the finished renders, and the machines doing the work.</p>
        </div>
        <div className="ml-auto flex items-center gap-2 text-[12.5px] text-muted">
          <Cpu size={15} />
          {workers.isLoading ? '…' : `${online} of ${workers.data?.workers.length ?? 0} machines online`}
        </div>
      </div>
      <Tabs<Tab>
        label="Sections"
        className="mb-6"
        value={tab}
        onChange={(t) => setParams({ tab: t }, { replace: true })}
        items={[
          { value: 'jobs', label: 'Jobs' },
          { value: 'renders', label: 'Renders' },
          { value: 'machines', label: 'Machines', count: workers.data?.workers.length },
        ]}
      />
      {tab === 'jobs' ? <JobsSection /> : tab === 'renders' ? <RendersSection highlight={renderId} /> : <MachinesSection workers={workers.data?.workers} loading={workers.isLoading} error={workers.error} onRetry={() => void workers.refetch()} updatedAt={workers.dataUpdatedAt} fetching={workers.isFetching} />}
    </div>
  );
}

function JobsSection() {
  const [filter, setFilter] = useState<JobFilter>('active');
  const running = useOp('job.list', { state: 'running', limit: 100 });
  const queued = useOp('job.list', { state: 'queued', limit: 100 });
  const failed = useOp('job.list', { state: 'failed', limit: 100 });
  const idle = filter === 'active' && !running.isLoading && !queued.isLoading && (running.data?.jobs.length ?? 0) + (queued.data?.jobs.length ?? 0) === 0;
  const all = useOp('job.list', filter === 'all' || idle ? { limit: filter === 'all' ? 200 : 25 } : null);
  const projects = useOp('project.list', { includeArchived: true });
  const names = new Map((projects.data?.projects ?? []).map((p) => [p.id, p.name]));
  const assetList = useOp('asset.list', { limit: 1000 }, { staleTime: 60_000 });
  const assetNames = useMemo(() => new Map((assetList.data?.assets ?? []).map((a) => [a.id, a.title])), [assetList.data]);
  const jobs: Job[] = useMemo(() => {
    if (filter === 'active') return idle ? all.data?.jobs ?? [] : [...(running.data?.jobs ?? []), ...(queued.data?.jobs ?? [])];
    if (filter === 'failed') return failed.data?.jobs ?? [];
    return all.data?.jobs ?? [];
  }, [filter, idle, running.data, queued.data, failed.data, all.data]);
  const loading = filter === 'active' ? running.isLoading || queued.isLoading || (idle && all.isLoading) : filter === 'failed' ? failed.isLoading : all.isLoading;
  const error = filter === 'active' ? running.error || queued.error : filter === 'failed' ? failed.error : all.error;
  const src = filter === 'active' ? running : filter === 'failed' ? failed : all;
  const openJob = useJobDetails();
  const [active] = useListNav(jobs.length, (i) => jobs[i] && openJob(jobs[i]!.id));

  return (
    <section aria-labelledby="jobs-h">
      <SectionTitle
        id="jobs-h"
        action={
          <>
            <Freshness at={src.dataUpdatedAt || undefined} fetching={src.isFetching} onRefresh={() => void src.refetch()} />
            <div className="flex gap-1" role="group" aria-label="Filter jobs">
              {(
                [
                  ['active', `Active ${(running.data?.jobs.length ?? 0) + (queued.data?.jobs.length ?? 0)}`],
                  ['failed', `Failed ${failed.data?.jobs.length ?? 0}`],
                  ['all', 'All'],
                ] as const
              ).map(([k, l]) => (
                <button key={k} type="button" aria-pressed={filter === k} onClick={() => setFilter(k)} className={cx('h-8 rounded-full px-3 text-[12.5px] transition-colors hairline', filter === k ? 'bg-panel text-fg' : 'text-muted hover:text-fg')}>
                  {l}
                </button>
              ))}
            </div>
          </>
        }
      >
        Jobs
      </SectionTitle>
      {loading ? (
        <div className="space-y-2">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-14 rounded-[12px]" />
          ))}
        </div>
      ) : error ? (
        <ErrorPanel error={error} onRetry={() => void src.refetch()} />
      ) : jobs.length === 0 ? (
        <div className="panel">
          <EmptyState icon={<Queue size={22} />} title={filter === 'failed' ? 'No failed jobs' : filter === 'active' ? 'Nothing running' : 'No jobs yet'} body="Imports, analysis and renders show up here with their progress, the machine doing them, and any retries." />
        </div>
      ) : (
        <>
        {idle ? <div className="mb-3 rounded-[12px] px-4 py-3 text-[13px] text-muted hairline">Nothing is running right now. Here are the most recent jobs.</div> : null}
        <div className="overflow-hidden rounded-[16px] hairline">
          <div className="hidden grid-cols-[minmax(130px,1fr)_110px_minmax(180px,2fr)_minmax(120px,1fr)_80px_88px] gap-4 border-b border-line bg-raised px-4 py-2.5 text-[11.5px] font-medium text-subtle lg:grid">
            <span>Job</span>
            <span>State</span>
            <span>Progress</span>
            <span>Machine</span>
            <span>Tries</span>
            <span className="text-right">Actions</span>
          </div>
          <ul className="divide-y divide-line">
            {jobs.map((j, i) => (
              <JobRow key={j.id} job={j} project={j.projectId ? names.get(j.projectId) : undefined} assetName={j.assetId ? assetNames.get(j.assetId) : undefined} active={i === active} />
            ))}
          </ul>
        </div>
        </>
      )}
    </section>
  );
}

function JobRow({ job, project, assetName, active }: { job: Job; project?: string; assetName?: string; active: boolean }) {
  const openJob = useJobDetails();
  const dialog = useDialog();
  const toast = useToast();
  const ref = useScrollIntoView<HTMLLIElement>(active);
  const cancel = useOpMutation('job.cancel', { invalidate: ['job.list', 'render.list', 'project.show'] });
  const retry = useOpMutation('job.retry', { invalidate: ['job.list', 'render.list', 'project.show'] });
  const machines = useInstallationNames();
  const live = job.state === 'running' || job.state === 'queued';
  const last = job.attempts[job.attempts.length - 1];
  // Attempts record the machine's name when they start; older jobs fall back to today's machine names.
  const worker = job.lease?.ownerName ?? last?.workerName ?? (last?.worker ? machines.get(last.worker) : undefined);
  const subject = job.renderId ? (
    <Link to={`/renders/${job.renderId}`} className="hover:text-fg">
      {project ? <Theirs>{project}</Theirs> : 'render'}
    </Link>
  ) : job.projectId && project ? (
    <Link to={`/p/${job.projectId}`} className="hover:text-fg">
      <Theirs>{project}</Theirs>
    </Link>
  ) : assetName ? (
    <Theirs>{assetName}</Theirs>
  ) : (
    <span>{job.assetId ? 'a clip' : 'MetaRush'}</span>
  );
  const showProgress = job.state === 'running' || !!job.error;
  return (
    <li
      ref={ref}
      data-active={active || undefined}
      data-job={job.id}
      tabIndex={0}
      aria-label={`${jobLabel(job.type)}, ${stateWord(job.state)}. Open details`}
      onClick={(e) => {
        if ((e.target as HTMLElement).closest('button, a')) return;
        openJob(job.id);
      }}
      onKeyDown={(e) => {
        if (e.key === 'Enter' && e.target === e.currentTarget) {
          e.preventDefault();
          openJob(job.id);
        }
      }}
      className={cx(
        'flex cursor-pointer flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3 outline-none focus-visible:bg-hover focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent lg:grid lg:grid-cols-[minmax(130px,1fr)_110px_minmax(180px,2fr)_minmax(120px,1fr)_80px_88px] lg:gap-4',
        active ? 'bg-hover' : 'bg-bg hover:bg-raised',
      )}
    >
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="text-[13px] font-medium text-fg">{jobLabel(job.type)}</span>
          {job.heavy ? <Badge tone="info" title="Big job: may wait while a laptop is low on battery">big</Badge> : null}
        </div>
        <div className="truncate text-[11.5px] text-subtle" title={absolute(job.createdAt)}>
          {subject}, {relative(job.createdAt)}
          <span className="lg:hidden">
            {worker ? `, ${worker}` : ''}, try {job.attemptCount}/{job.maxAttempts}
          </span>
        </div>
      </div>
      <div className="shrink-0">
        <StateBadge state={job.state} label={job.cancelRequested && live ? 'stopping' : undefined} />
      </div>
      <div className={cx('min-w-0 basis-full lg:basis-auto', !showProgress && 'hidden lg:block')}>
        {job.state === 'running' ? (
          <>
            <Progress value={job.progress.pct} indeterminate={!job.progress.pct} label={`${jobLabel(job.type)} progress`} />
            <div className="mt-1 truncate text-[11.5px] text-subtle">
              {Math.round(job.progress.pct * 100)}%{job.progress.message ? `, ${job.progress.message}` : ''}
            </div>
          </>
        ) : job.error ? (
          <div className="clamp-2 text-[12px] text-danger">
            {job.state === 'queued' ? 'It failed a moment ago and will try again by itself.' : job.state === 'canceled' ? 'Canceled.' : 'It did not finish. Open it to see why and what to do.'}
          </div>
        ) : (
          <div className="truncate text-[12px] text-subtle">
            {job.state === 'queued' ? `waiting${job.nextRunAt ? `, next try ${relative(job.nextRunAt)}` : ''}` : job.finishedAt ? `finished ${relative(job.finishedAt)}` : job.progress.message || ''}
          </div>
        )}
      </div>
      <div className="hidden truncate text-[12.5px] text-muted lg:block">{worker ?? <span className="text-subtle">no machine yet</span>}</div>
      <div className="tc hidden text-[12.5px] text-muted lg:block">
        {job.attemptCount}/{job.maxAttempts}
      </div>
      <div className="flex shrink-0 justify-end gap-1">
        {job.state === 'failed' || job.state === 'canceled' ? (
          <IconButton
            size="sm"
            label="Retry job"
            onClick={() => retry.mutate({ id: job.id }, { onSuccess: () => toast({ tone: 'success', title: `${jobLabel(job.type)}: trying again` }), onError: (e) => toast({ tone: 'error', title: 'Could not try again', body: e.message }) })}
          >
            <ArrowClockwise size={14} />
          </IconButton>
        ) : null}
        {live && !job.cancelRequested ? (
          <IconButton
            size="sm"
            label="Cancel job"
            onClick={async () => {
              const ok = await dialog.confirm({ title: `Cancel this ${jobLabel(job.type).toLowerCase()} job?`, body: 'If it is running, it stops in a moment. You can try it again later.', confirmLabel: 'Cancel job', cancelLabel: 'Keep it', tone: 'danger' });
              if (ok) cancel.mutate({ id: job.id }, { onSuccess: () => toast({ title: 'Stopping it' }), onError: (e) => toast({ tone: 'error', title: 'Could not cancel', body: e.message }) });
            }}
          >
            <X size={14} />
          </IconButton>
        ) : null}
      </div>
    </li>
  );
}

function RendersSection({ highlight }: { highlight?: string }) {
  const renders = useOp('render.list', { limit: 100 });
  const jobs = useOp('job.list', { type: 'render', limit: 100 });
  const projects = useOp('project.list', { includeArchived: true });
  const names = new Map((projects.data?.projects ?? []).map((p) => [p.id, p.name]));
  const byJob = new Map((jobs.data?.jobs ?? []).map((j) => [j.id, j]));
  const list = renders.data?.renders ?? [];
  useEffect(() => {
    if (!highlight || !renders.data) return;
    document.querySelector(`[data-render="${highlight}"]`)?.scrollIntoView({ block: 'center', behavior: 'smooth' });
  }, [highlight, renders.data]);
  return (
    <section aria-labelledby="renders-h">
      <SectionTitle id="renders-h" meta={list.length ? `${list.length} recent` : undefined} action={<Freshness at={renders.dataUpdatedAt || undefined} fetching={renders.isFetching} onRefresh={() => void renders.refetch()} />}>
        Renders
      </SectionTitle>
      <FollowSetting className="mb-4" />
      {renders.isLoading ? (
        <div className="grid grid-cols-1 gap-2 lg:grid-cols-2">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-20 rounded-[14px]" />
          ))}
        </div>
      ) : renders.error ? (
        <ErrorPanel error={renders.error} onRetry={() => void renders.refetch()} />
      ) : list.length === 0 ? (
        <div className="panel">
          <EmptyState icon={<FilmReel size={22} />} title="No renders yet" body="Start a review copy or a final render from a project. Any machine running MetaRush that can render picks it up, and the finished video is saved in MetaRush's storage for everyone in the workspace." />
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-1 lg:grid-cols-2">
          {list.map((r) => (
            <div key={r.id} data-render={r.id}>
              <RenderTile render={r} job={byJob.get(r.jobId)} projectName={names.get(r.projectId)} highlight={r.id === highlight} showProject />
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

/**
 * "A review copy follows the latest version" (D187), for the whole workspace:
 * on by default; `metarush render follow on|off` does the same.
 */
function FollowSetting({ className }: { className?: string }) {
  const toast = useToast();
  const follow = useOp('render.follow.show', {});
  const set = useOpMutation('render.follow.set', { invalidate: ['render.follow.show'] });
  const own = useOwnership(); // a workspace setting that spends this computer's time (D191)
  const on = follow.data?.on;
  return (
    <div className={cx('flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[14px] px-4 py-3 hairline', className)} data-follow-setting>
      <Lightning size={16} className="shrink-0 text-accent-text" />
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-medium text-fg">A review copy follows each cut's latest version</div>
        <div className="text-[12px] text-muted">
          {on === false
            ? 'Off: review copies are made only when someone asks for one.'
            : `About ${follow.data ? Math.round(follow.data.quietSeconds / 60) || 1 : 1} minute after the last change, MetaRush makes one by itself, so the Edit tab can show the real thing.`}
        </div>
      </div>
      <fieldset disabled={!own.owner} title={ownerTitle(own, 'change this')} className="m-0 min-w-0 border-0 p-0">
      <Segmented<'on' | 'off'>
        label="Review copies of the latest version"
        value={on === false ? 'off' : 'on'}
        onChange={(v) =>
          set.mutate(
            { on: v === 'on' },
            {
              onSuccess: (o) => toast({ tone: 'success', title: o.on ? 'Review copies follow the latest versions' : 'Review copies only when asked for' }),
              onError: (e) => toast({ tone: 'error', title: 'Could not change it', body: [e.message, e.hint].filter(Boolean).join(' ') }),
            },
          )
        }
        items={[
          { value: 'on', label: 'On' },
          { value: 'off', label: 'Off' },
        ]}
      />
      </fieldset>
    </div>
  );
}

function MachinesSection({ workers, loading, error, onRetry, updatedAt, fetching }: { workers?: WorkerRow[]; loading: boolean; error: unknown; onRetry: () => void; updatedAt: number; fetching: boolean }) {
  return (
    <section aria-labelledby="machines-h">
      <SectionTitle id="machines-h" action={<Freshness at={updatedAt || undefined} fetching={fetching} onRefresh={onRetry} />}>
        Machines
      </SectionTitle>
      {loading ? (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          <Skeleton className="h-48 rounded-[16px]" />
          <Skeleton className="h-48 rounded-[16px]" />
        </div>
      ) : error ? (
        <ErrorPanel error={error} onRetry={onRetry} />
      ) : !workers || workers.length === 0 ? (
        <div className="panel">
          <EmptyState
            icon={<Cpu size={22} />}
            title="No machines running MetaRush yet"
            body={
              <>
                Start MetaRush on any computer connected to this workspace, and it shows up here with the work it can do.
                <Details compact className="mt-2">
                  <span className="tc">metarush dashboard</span>, or <span className="tc">metarush worker start</span> on a computer that only does the work
                </Details>
              </>
            }
          />
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 2xl:grid-cols-3">
          {workers.map((w) => (
            <WorkerCard key={w.id} w={w} />
          ))}
        </div>
      )}
    </section>
  );
}

function WorkerCard({ w }: { w: WorkerRow }) {
  const load = w.concurrency ? w.running / w.concurrency : 0;
  return (
    <article className={cx('panel p-5', !w.online && 'opacity-70')}>
      <div className="flex items-start gap-3">
        <div className={cx('grid h-10 w-10 shrink-0 place-items-center rounded-[12px]', w.online ? 'bg-ok-soft text-ok' : 'bg-hover text-subtle')}>
          <Cpu size={19} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <h3 className="truncate text-[15px] font-semibold">{w.name}</h3>
            <Badge tone={w.online ? 'ok' : 'neutral'}>{w.online ? 'online' : 'offline'}</Badge>
          </div>
          <div className="truncate text-[12px] text-subtle" title={w.hostname}>
            {w.hostname}
          </div>
        </div>
        {w.onBattery ? (
          <span title="On battery: big jobs may wait until it's plugged in" className="text-accent-text">
            <BatteryWarning size={18} />
          </span>
        ) : (
          <span title="Plugged in" className="text-subtle">
            <Plug size={16} />
          </span>
        )}
      </div>
      <dl className="mt-4 grid grid-cols-3 gap-2 text-[12px]">
        <div className="rounded-[10px] bg-panel px-2.5 py-2 hairline">
          <dt className="text-subtle">Computer</dt>
          <dd className="mt-0.5 truncate text-fg">{platformLabel(w.os, w.arch)}</dd>
        </div>
        <div className="rounded-[10px] bg-panel px-2.5 py-2 hairline">
          <dt className="text-subtle">Hardware</dt>
          <dd className="tc mt-0.5 truncate text-fg">
            {w.cpus} cores, {w.memoryGB} GB
          </dd>
        </div>
        <div className="rounded-[10px] bg-panel px-2.5 py-2 hairline">
          <dt className="text-subtle">Doing now</dt>
          <dd className="tc mt-0.5 text-fg">
            {w.running} of {w.concurrency}
          </dd>
        </div>
      </dl>
      <Progress className="mt-3" value={load} tone={load >= 1 ? 'accent' : 'ok'} label={`How busy ${w.name} is`} />
      <div className="mt-4">
        <div className="text-[11.5px] font-medium text-subtle">What it can do</div>
        <div className="mt-1.5 flex flex-wrap gap-1">
          {w.jobTypes.map((t) => (
            <Badge key={t} tone={t === 'render' ? 'accent' : 'neutral'} icon={t === 'render' ? <Lightning size={10} weight="fill" /> : undefined}>
              {jobLabel(t)}
            </Badge>
          ))}
          {w.acceptsHeavy ? <Badge tone="info">takes big jobs</Badge> : null}
        </div>
        {!w.jobTypes.includes('render') ? <div className="mt-2 text-[11.5px] text-subtle">It can't render yet: the renderer isn't set up on this machine.</div> : null}
      </div>
      {Object.keys(w.tools).length ? (
        <Details compact label="Tools and versions" className="mt-4">
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[12px]">
            {Object.entries(w.tools).map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="text-muted">{k}</dt>
                <dd className="tc truncate text-fg" title={v}>
                  {v}
                </dd>
              </div>
            ))}
          </dl>
          <div className="mt-1">
            {w.os}/{w.arch} · MetaRush <span className="tc">{w.version}</span>
          </div>
        </Details>
      ) : null}
      <div className="mt-4 border-t border-line pt-3 text-[11.5px] text-subtle" title={absolute(w.lastHeartbeat)}>
        MetaRush {releaseOf(w.version)}, checked in {relative(w.lastHeartbeat)}, running since {relative(w.startedAt)}
      </div>
    </article>
  );
}

