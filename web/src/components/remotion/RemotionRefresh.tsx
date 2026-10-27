import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router';
import { ArrowsClockwise, CheckCircle, Globe, Key, LockKey, Plus, ShieldCheck, Terminal, WarningCircle, X, XCircle } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { useOp, useOpMutation } from '../../api/queries';
import type { RemotionKey, RemotionRefreshIn, RemotionRefreshPlanOut, RemotionRefreshRow, RemotionSourceRow } from '../../api/types';
import { relative } from '../../lib/format';
import { useActivityOpen } from '../../lib/workbench';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, cx, Details, ErrorPanel, Skeleton, Spinner, Theirs } from '../../ui/primitives';

/*
 * Refreshing a Remotion project from its own steps (D173–D175): the plan in
 * plain words — every command, the keys passed and the names they get, where
 * it may write, the hosts it may reach, how it is kept apart — and a button a
 * person presses (never Claude). What the steps write becomes a new version
 * that waits for a person, like any other.
 */

type JobView = { id: string; state: string; title: string; progress: { pct: number; message?: string }; result?: Record<string, unknown>; error?: string };
const running = (state?: string) => state === 'queued' || state === 'running';

/** Follows a job a panel started until it ends, then says how it went. */
export function useFollowJob(invalidate: string[]) {
  const toast = useToast();
  const qc = useQueryClient();
  const [, setActivity] = useActivityOpen();
  const [jobId, setJobId] = useState<string | null>(null);
  const q = useOp('job.show', jobId ? { id: jobId } : null, { refetchInterval: (s) => (s.state.data && running(s.state.data.state) ? 1500 : false) });
  const job = q.data as unknown as JobView | undefined;
  const told = useRef<string | null>(null);
  useEffect(() => {
    if (!job || running(job.state) || told.current === job.id) return;
    told.current = job.id;
    for (const k of invalidate) void qc.invalidateQueries({ queryKey: [k] });
    const message = typeof job.result?.message === 'string' ? job.result.message : '';
    if (job.state === 'succeeded') toast({ tone: 'success', title: message || 'Done', durationMs: 12000 });
    else toast({ tone: 'error', title: 'That did not finish', body: job.error || undefined, durationMs: 15000 });
  }, [job, qc, toast, invalidate]);
  const start = (out: { message: string; jobId: string }) => {
    told.current = null;
    setJobId(out.jobId);
    for (const k of invalidate) void qc.invalidateQueries({ queryKey: [k] });
    toast({ title: out.message, action: { label: 'Show progress', onClick: () => setActivity(true) }, durationMs: 9000 });
  };
  const busy = !!job && running(job.state);
  return { start, busy, line: busy ? job?.progress?.message || job?.title || '' : '', pct: job?.progress?.pct ?? 0 };
}

export function Working({ line, pct }: { line: string; pct: number }) {
  return (
    <p role="status" className="flex items-center gap-2 text-[12.5px] text-muted">
      <Spinner size={12} />
      <span className="min-w-0 truncate">{line ? line.charAt(0).toUpperCase() + line.slice(1) : 'Working on it'}</span>
      {pct > 0 && pct < 1 ? <span className="shrink-0 text-subtle">{Math.round(pct * 100)}%</span> : null}
    </p>
  );
}

const field =
  'block w-full rounded-[10px] bg-panel px-3 py-1.5 text-[13px] leading-snug text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0 disabled:opacity-60';

const stateTone: Record<string, 'ok' | 'danger' | 'accent' | 'neutral'> = { done: 'ok', failed: 'danger', canceled: 'danger', running: 'accent', queued: 'accent' };

/** The refresh panel of a source's card. */
export function RemotionRefreshPanel({ row, projectId, onReview }: { row: RemotionSourceRow; projectId: string; onReview: (version: string) => void }) {
  const toast = useToast();
  const dialog = useDialog();
  // The refresh steps being looked at; null = the ones last started (or none).
  const [picked, setPicked] = useState<{ steps: string[]; keys: RemotionKey[]; outputs: string[] } | null>(null);
  // Once the person changes anything, the plan is exactly what they picked (fresh: not the one last started).
  const input: RemotionRefreshIn = { source: row.name, projectId, ...(picked ? { steps: picked.steps, keys: picked.keys, outputs: picked.outputs, fresh: true } : {}) };
  const plan = useOp('remotion.refresh.plan', input, { placeholderData: (prev) => prev });
  const runs = useOp('remotion.refresh.runs', { source: row.name, projectId, limit: 8 });
  const start = useOpMutation('remotion.refresh.start');
  const job = useFollowJob(['remotion.refresh.runs', 'remotion.refresh.plan', 'remotion.list', 'remotion.show', 'todos.list', 'activity.summary']);
  if (plan.isLoading && !plan.data) return <Skeleton className="mt-3 h-40 rounded-[14px]" />;
  if (plan.error && !plan.data) return <ErrorPanel className="mt-3" error={plan.error} onRetry={() => void plan.refetch()} />;
  const p = plan.data!;
  const current = picked ?? {
    steps: p.refreshSteps.steps.map((s) => s.run),
    keys: p.refreshSteps.keys,
    outputs: p.refreshSteps.outputs.length ? p.refreshSteps.outputs : p.outputs,
  };
  const update = (next: Partial<typeof current>) => setPicked({ ...current, ...next });
  const busy = job.busy || start.isPending || !!p.running;

  const onStart = async () => {
    const ok = await dialog.confirm({
      title: `Refresh “${p.title}” from its own steps?`,
      body: <PlanSummary p={p} />,
      confirmLabel: 'Start the refresh',
    });
    if (!ok) return;
    start.mutate(
      { ...input, steps: current.steps, keys: current.keys, outputs: current.outputs, plan: p.fingerprint },
      { onSuccess: job.start, onError: (e) => toast({ tone: 'error', title: 'It did not start', body: [e.message, e.hint].filter(Boolean).join(' ') }) },
    );
  };

  return (
    <div className="mt-3 rounded-[12px] bg-panel px-3.5 py-3 hairline" data-remotion-refresh>
      <p className="max-w-[78ch] text-[13px] leading-relaxed text-fg">{p.message}</p>
      <p className="mt-1 max-w-[78ch] text-[12.5px] leading-relaxed text-muted">
        A refresh runs the project&apos;s own steps on {p.machine}, with the network on, away from MetaRush&apos;s files and keys. Only a person starts one; what it writes waits for you as a new
        version.
      </p>

      <Section icon={<Terminal size={14} />} title="Its steps, in order">
        <StepsPicker p={p} steps={current.steps} disabled={busy} onChange={(steps) => update({ steps })} />
      </Section>

      <Section icon={<Key size={14} />} title="Keys it gets (nothing else)">
        <KeysPicker p={p} keys={current.keys} disabled={busy} onChange={(keys) => update({ keys })} />
      </Section>

      <Section icon={<LockKey size={14} />} title="Where it may write">
        <OutputsPicker outputs={current.outputs} disabled={busy} onChange={(outputs) => update({ outputs })} />
      </Section>

      {p.hosts.length ? (
        <Section icon={<Globe size={14} />} title="Places it may reach">
          <ul className="space-y-0.5 text-[12.5px] text-fg" data-refresh-hosts>
            {p.hosts.map((h) => (
              <li key={h.host}>
                <Theirs>{h.host}</Theirs> <span className="text-subtle">({h.why})</span>
              </li>
            ))}
          </ul>
        </Section>
      ) : null}

      <Section icon={<ShieldCheck size={14} />} title="Kept apart">
        {p.apart ? <p className="text-[12.5px] leading-relaxed text-fg">{p.apart}</p> : null}
        <ul className="mt-1 space-y-0.5 text-[12.5px] leading-relaxed text-muted">
          {p.never.map((n) => (
            <li key={n} className="flex gap-1.5">
              <CheckCircle size={13} className="mt-[3px] shrink-0 text-ok" />
              <span>{n}</span>
            </li>
          ))}
        </ul>
      </Section>

      {p.problems.length ? (
        <ul className="mt-3 space-y-1" data-refresh-problems>
          {p.problems.map((x) => (
            <li key={x} className="flex gap-2 text-[13px] leading-relaxed text-fg">
              <WarningCircle size={15} weight="fill" className="mt-[2px] shrink-0 text-accent-text" />
              <span>{x}</span>
            </li>
          ))}
        </ul>
      ) : null}

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button variant="primary" size="sm" icon={<ArrowsClockwise size={13} />} disabled={!p.ready || busy || plan.isFetching} loading={start.isPending} onClick={() => void onStart()} data-refresh-start>
          Start the refresh
        </Button>
        {picked ? (
          <Button variant="ghost" size="sm" disabled={busy} onClick={() => setPicked(null)}>
            Back to the last one started
          </Button>
        ) : null}
        {job.busy ? <Working line={job.line} pct={job.pct} /> : p.running ? <Working line={`refresh ${p.running.number} is ${p.running.stateLabel}`} pct={0} /> : null}
      </div>

      <Runs runs={runs.data?.refreshes ?? []} row={row} projectId={projectId} onReview={onReview} />

      <Details compact className="mt-3">
        <div>Starts from version {p.versionNumber} ({p.version})</div>
        <div>Kept apart with: {p.isolation || 'nothing on this computer'}</div>
        {p.steps.map((s) => (
          <div key={s.n}>
            Step {s.n}: {s.run}
            {s.command ? ` → ${s.command}` : ''}
          </div>
        ))}
        <div>Plan: {p.fingerprint}</div>
        <div>
          In the terminal: metarush remotion refresh start {row.name} {current.steps.map((s) => `--step "${s}"`).join(' ')} {current.keys.map((k) => `--key ${k.provider}=${k.env}`).join(' ')}{' '}
          {current.outputs.map((o) => `--output ${o}`).join(' ')}
        </div>
      </Details>
    </div>
  );
}

function Section({ icon, title, children }: { icon: React.ReactNode; title: string; children: React.ReactNode }) {
  return (
    <div className="mt-3">
      <h5 className="mb-1 flex items-center gap-1.5 text-[13px] font-semibold text-fg">
        <span className="text-subtle">{icon}</span>
        {title}
      </h5>
      {children}
    </div>
  );
}

/** What the person is about to start, again, in the confirm dialog. */
function PlanSummary({ p }: { p: RemotionRefreshPlanOut }) {
  return (
    <div className="space-y-2 text-[13px] leading-relaxed">
      <p>On {p.machine}, in this order:</p>
      <ol className="list-decimal space-y-0.5 pl-5">
        {p.steps.map((s) => (
          <li key={s.n}>
            <Theirs>{s.run}</Theirs>
            {s.command ? (
              <span className="text-muted">
                {' '}
                (<Theirs>{s.command}</Theirs>)
              </span>
            ) : null}
          </li>
        ))}
      </ol>
      <p>{p.keys.length ? `It gets ${p.keys.map((k) => `the ${k.title} key as ${k.env}`).join(' and ')}, and nothing else.` : 'It gets no keys.'}</p>
      <p>Only what it writes in {p.outputs.join(', ')} becomes the new version, which waits for you before any cut uses it.</p>
    </div>
  );
}

function StepsPicker({ p, steps, disabled, onChange }: { p: RemotionRefreshPlanOut; steps: string[]; disabled: boolean; onChange: (s: string[]) => void }) {
  const [other, setOther] = useState('');
  const scriptRun = (name: string) => `npm run ${name}`;
  const scripts = new Set(p.scripts.map((s) => scriptRun(s.name)));
  const extra = steps.filter((s) => !scripts.has(s));
  return (
    <div>
      {p.scripts.length ? (
        <ul className="grid grid-cols-1 gap-1 sm:grid-cols-2" data-refresh-scripts>
          {p.scripts.map((s) => {
            const run = scriptRun(s.name);
            const at = steps.indexOf(run);
            return (
              <li key={s.name}>
                <label className={cx('flex cursor-pointer items-start gap-2 rounded-[10px] px-2.5 py-1.5 text-[12.5px] hover:bg-hover', at >= 0 && 'bg-hover')}>
                  <input
                    type="checkbox"
                    className="mt-[3px]"
                    checked={at >= 0}
                    disabled={disabled}
                    onChange={(e) => onChange(e.target.checked ? [...steps, run] : steps.filter((x) => x !== run))}
                    data-refresh-script={s.name}
                  />
                  <span className="min-w-0">
                    <span className="font-medium text-fg">
                      {at >= 0 ? `${at + 1}. ` : ''}
                      <Theirs>{s.name}</Theirs>
                    </span>
                    <span className="block truncate text-subtle" title={s.command}>
                      <Theirs>{s.command}</Theirs>
                    </span>
                    {s.makesVideos ? <span className="block text-muted">It renders videos; MetaRush makes clips itself.</span> : null}
                  </span>
                </label>
              </li>
            );
          })}
        </ul>
      ) : (
        <p className="text-[12.5px] text-muted">Its package.json has no scripts.</p>
      )}
      {extra.length ? (
        <ul className="mt-1.5 flex flex-wrap gap-1.5">
          {extra.map((s) => (
            <li key={s}>
              <Badge tone="neutral">
                <Theirs>{s}</Theirs>
                <button type="button" aria-label={`Remove ${s}`} className="ml-1 text-subtle hover:text-fg" disabled={disabled} onClick={() => onChange(steps.filter((x) => x !== s))}>
                  <X size={11} />
                </button>
              </Badge>
            </li>
          ))}
        </ul>
      ) : null}
      <form
        className="mt-2 flex max-w-[560px] gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (other.trim()) onChange([...steps, other.trim()]);
          setOther('');
        }}
      >
        <input
          className={field}
          value={other}
          disabled={disabled}
          placeholder="Another step, like node scripts/make.mjs"
          aria-label="Another step"
          onChange={(e) => setOther(e.target.value)}
          data-user-content
        />
        <Button type="submit" size="sm" variant="secondary" icon={<Plus size={13} />} disabled={disabled || !other.trim()}>
          Add
        </Button>
      </form>
    </div>
  );
}

function KeysPicker({ p, keys, disabled, onChange }: { p: RemotionRefreshPlanOut; keys: RemotionKey[]; disabled: boolean; onChange: (k: RemotionKey[]) => void }) {
  return (
    <ul className="space-y-1.5" data-refresh-keys>
      {p.integrations.map((it) => {
        const picked = keys.find((k) => k.provider === it.name);
        return (
          <li key={it.name} className="flex flex-wrap items-center gap-2 text-[12.5px]">
            <label className="flex min-w-[220px] cursor-pointer items-center gap-2">
              <input
                type="checkbox"
                checked={!!picked}
                disabled={disabled}
                onChange={(e) => onChange(e.target.checked ? [...keys, { provider: it.name as RemotionKey['provider'], env: it.env }] : keys.filter((k) => k.provider !== it.name))}
                data-refresh-key={it.name}
              />
              <span className="font-medium text-fg">The {it.title} key</span>
              <span className="text-subtle">{it.configured ? it.masked : 'not on this computer'}</span>
            </label>
            {picked ? (
              <label className="flex items-center gap-1.5 text-muted">
                as
                <input
                  className={cx(field, 'w-[230px] font-mono text-[12px]')}
                  defaultValue={picked.env}
                  disabled={disabled}
                  aria-label={`The name the ${it.title} key goes under`}
                  onBlur={(e) => onChange(keys.map((k) => (k.provider === it.name ? { ...k, env: e.target.value.trim().toUpperCase() } : k)))}
                />
              </label>
            ) : it.read ? (
              <span className="text-subtle">its code reads {it.env}</span>
            ) : null}
            {!it.configured ? (
              <Link className="text-accent-text hover:underline" to="/settings/integrations">
                Add it in Settings → Integrations
              </Link>
            ) : null}
          </li>
        );
      })}
      {p.reads.filter((r) => !r.set).length ? (
        <li className="text-[12px] leading-relaxed text-muted">
          It also reads {p.reads.filter((r) => !r.set).map((r) => r.name).join(', ')}, which MetaRush doesn&apos;t set (the code keeps its own defaults).
        </li>
      ) : null}
    </ul>
  );
}

function OutputsPicker({ outputs, disabled, onChange }: { outputs: string[]; disabled: boolean; onChange: (o: string[]) => void }) {
  const [next, setNext] = useState('');
  return (
    <div>
      <ul className="flex flex-wrap gap-1.5" data-refresh-outputs>
        {outputs.map((o) => (
          <li key={o}>
            <Badge tone="neutral">
              <Theirs>{o}</Theirs>
              <button type="button" aria-label={`Don't let it write ${o}`} className="ml-1 text-subtle hover:text-fg" disabled={disabled} onClick={() => onChange(outputs.filter((x) => x !== o))}>
                <X size={11} />
              </button>
            </Badge>
          </li>
        ))}
      </ul>
      <form
        className="mt-2 flex max-w-[560px] gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (next.trim() && !outputs.includes(next.trim())) onChange([...outputs, next.trim()]);
          setNext('');
        }}
      >
        <input className={field} value={next} disabled={disabled} placeholder="A folder or file, like src/generated/" aria-label="Another place it may write" onChange={(e) => setNext(e.target.value)} data-user-content />
        <Button type="submit" size="sm" variant="secondary" icon={<Plus size={13} />} disabled={disabled || !next.trim()}>
          Add
        </Button>
      </form>
      <p className="mt-1 text-[12px] text-muted">What it writes anywhere else is left out of the new version (and listed).</p>
    </div>
  );
}

function Runs({ runs, row, projectId, onReview }: { runs: RemotionRefreshRow[]; row: RemotionSourceRow; projectId: string; onReview: (v: string) => void }) {
  const [open, setOpen] = useState<number | null>(null);
  if (!runs.length) return null;
  return (
    <div className="mt-4" data-refresh-runs>
      <h5 className="mb-1.5 text-[13px] font-semibold text-fg">Refreshes</h5>
      <ul className="divide-y divide-line overflow-hidden rounded-[12px] bg-raised hairline">
        {runs.map((r) => (
          <li key={r.id} className="px-3 py-2 text-[12.5px]" data-refresh-run={r.number}>
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-medium text-fg">Refresh {r.number}</span>
              <Badge tone={stateTone[r.state] ?? 'neutral'} icon={r.state === 'done' ? <CheckCircle size={12} weight="fill" /> : r.state === 'failed' ? <XCircle size={12} weight="fill" /> : undefined}>
                {r.stateLabel}
              </Badge>
              <span className="text-subtle">
                {relative(r.createdAt)}
                {r.by?.userName ? ` by ${r.by.userName}` : ''} on {r.machine}
              </span>
              {r.versionNumber && r.version ? (
                <Button size="sm" variant="ghost" onClick={() => onReview(r.version!)}>
                  Review version {r.versionNumber}
                </Button>
              ) : null}
              {r.hasLog ? (
                <button type="button" className="text-accent-text hover:underline" onClick={() => setOpen(open === r.number ? null : r.number)}>
                  {open === r.number ? 'Hide what it printed' : 'What it printed'}
                </button>
              ) : null}
            </div>
            <p className="mt-0.5 text-muted">
              <Theirs>{r.steps.join(', ')}</Theirs>
            </p>
            {r.summary ? <p className="mt-0.5 text-fg">{r.summary}</p> : r.error ? <p className="mt-0.5 text-fg">{r.error}</p> : null}
            {r.leftOut.length ? (
              <p className="mt-0.5 text-muted">
                Left out: {r.leftOut.slice(0, 4).map((l) => `${l.path} (${l.why})`).join('; ')}
                {r.leftOut.length > 4 ? ` and ${r.leftOut.length - 4} more` : ''}
              </p>
            ) : null}
            {open === r.number ? <RunLog source={row.name} projectId={projectId} number={r.number} /> : null}
          </li>
        ))}
      </ul>
    </div>
  );
}

function RunLog({ source, projectId, number }: { source: string; projectId: string; number: number }) {
  const q = useOp('remotion.refresh.log', { source, projectId, number });
  if (q.isLoading) return <Skeleton className="mt-2 h-24 rounded-[10px]" />;
  if (q.error || !q.data) return <ErrorPanel className="mt-2" error={q.error} onRetry={() => void q.refetch()} />;
  return (
    <pre className="mt-2 max-h-[320px] overflow-auto whitespace-pre-wrap break-words rounded-[10px] bg-inset px-3 py-2 font-mono text-[11.5px] leading-snug text-fg hairline" data-user-content data-refresh-log>
      {q.data.text || 'Nothing printed.'}
    </pre>
  );
}
