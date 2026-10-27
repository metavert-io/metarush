import { useState } from 'react';
import { ArrowRight, CheckCircle, GitBranch, GitPullRequest, WarningCircle, XCircle } from '@phosphor-icons/react';
import { useOp, useOpMutation } from '../../api/queries';
import type { RemotionPushIn, RemotionPushPlanOut, RemotionSourceRow } from '../../api/types';
import { relative } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, cx, Details, ErrorPanel, Skeleton, Theirs } from '../../ui/primitives';
import { useFollowJob, Working } from './RemotionRefresh';

/*
 * Sending changes back (D176): an approved version's changes, in plain words,
 * to the repository the project came from — a new branch, and a pull request
 * on GitHub. A person sends it; nothing is ever forced or sent to the main
 * branch, and a branch MetaRush didn't make is never changed.
 */

const field =
  'block w-full rounded-[10px] bg-panel px-3 py-1.5 text-[13px] leading-snug text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0 disabled:opacity-60';

export function RemotionPushPanel({ row, projectId }: { row: RemotionSourceRow; projectId: string }) {
  const toast = useToast();
  const dialog = useDialog();
  const show = useOp('remotion.show', { ref: `${row.name}@head`, projectId });
  const approved = (show.data?.versions ?? []).filter((v) => v.state === 'approved');
  const [version, setVersion] = useState<number | null>(null);
  const [branch, setBranch] = useState('');
  const [noPr, setNoPr] = useState(false);
  const [words, setWords] = useState(false);
  const chosen = version ?? row.approvedNumber ?? 0;
  const input: RemotionPushIn = { source: `${row.name}@${chosen || 'approved'}`, projectId, ...(branch ? { branch } : {}), noPr, words };
  const plan = useOp('remotion.push.plan', chosen ? input : null, { placeholderData: (prev) => prev });
  const list = useOp('remotion.push.list', { source: row.name, projectId });
  const start = useOpMutation('remotion.push.start');
  const job = useFollowJob(['remotion.push.list', 'remotion.push.plan', 'activity.summary']);
  if (!row.approved)
    return (
      <div className="mt-3 rounded-[12px] bg-panel px-3.5 py-3 text-[13px] text-muted hairline" data-remotion-push>
        Changes go back only from a version a person approved, and none is approved yet.
      </div>
    );
  if (plan.isLoading && !plan.data) return <Skeleton className="mt-3 h-40 rounded-[14px]" />;
  if (plan.error && !plan.data) return <ErrorPanel className="mt-3" error={plan.error} onRetry={() => void plan.refetch()} />;
  const p = plan.data;
  const busy = job.busy || start.isPending;

  const onSend = async (x: RemotionPushPlanOut) => {
    const ok = await dialog.confirm({
      title: `Send version ${x.versionNumber} of “${x.title}” back?`,
      body: (
        <div className="space-y-2 text-[13px] leading-relaxed">
          <p>
            To <Theirs>{x.target.where}</Theirs>, as the new branch <Theirs>{x.branch}</Theirs>
            {x.pr ? `, with a pull request into ${x.target.default || 'the main branch'}` : ''}.
          </p>
          <p>{x.summary}</p>
          <p className="text-muted">Nothing is forced, and the main branch isn&apos;t touched: you look at the branch there and merge it if you want.</p>
        </div>
      ),
      confirmLabel: 'Send them back',
    });
    if (!ok) return;
    start.mutate({ ...input, plan: x.fingerprint }, { onSuccess: job.start, onError: (e) => toast({ tone: 'error', title: 'It was not sent', body: [e.message, e.hint].filter(Boolean).join(' ') }) });
  };

  return (
    <div className="mt-3 rounded-[12px] bg-panel px-3.5 py-3 hairline" data-remotion-push>
      <div className="flex flex-wrap items-end gap-3">
        <label className="text-[12.5px] text-muted">
          <span className="mb-0.5 block">Version</span>
          <select
            className={cx(field, 'w-auto pr-8')}
            value={chosen}
            disabled={busy}
            onChange={(e) => setVersion(Number(e.target.value))}
            data-push-version
          >
            {approved.map((v) => (
              <option key={v.hash} value={v.number}>
                Version {v.number}
              </option>
            ))}
            {!approved.length && chosen ? <option value={chosen}>Version {chosen}</option> : null}
          </select>
        </label>
        <label className="min-w-[240px] flex-1 text-[12.5px] text-muted">
          <span className="mb-0.5 block">New branch</span>
          <input
            className={cx(field, 'font-mono text-[12px]')}
            defaultValue={branch}
            placeholder={p?.branch || `metarush/${row.name}-v${chosen}`}
            disabled={busy}
            onBlur={(e) => setBranch(e.target.value.trim())}
            aria-label="New branch"
            data-user-content
          />
        </label>
      </div>
      <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[12.5px] text-fg">
        {p?.target.kind === 'github' ? (
          <label className="flex cursor-pointer items-center gap-1.5">
            <input type="checkbox" checked={!noPr} disabled={busy} onChange={(e) => setNoPr(!e.target.checked)} />
            Open a pull request
          </label>
        ) : null}
        {p?.helper ? (
          <label className="flex cursor-pointer items-center gap-1.5">
            <input type="checkbox" checked={words} disabled={busy} onChange={(e) => setWords(e.target.checked)} data-push-words />
            Write today&apos;s word settings into the code
          </label>
        ) : null}
      </div>

      {p ? (
        <>
          <p className="mt-3 max-w-[80ch] text-[13px] leading-relaxed text-fg">{p.message}</p>
          {p.wordChanges.length ? (
            <ul className="mt-2 space-y-1">
              {p.wordChanges.slice(0, 8).map((w, i) => (
                <li key={i} className="rounded-[10px] bg-raised px-3 py-1.5 text-[12.5px] leading-relaxed hairline">
                  {w.before ? (
                    <span className="text-muted line-through decoration-[rgb(239_141_128/0.7)]">
                      <Theirs>“{w.before}”</Theirs>
                    </span>
                  ) : null}
                  {w.before && w.after ? <ArrowRight size={12} className="mx-1.5 inline text-subtle" aria-label="became" /> : null}
                  {w.after ? (
                    <span className="text-fg">
                      <Theirs>“{w.after}”</Theirs>
                    </span>
                  ) : null}
                </li>
              ))}
            </ul>
          ) : null}
          {p.changes.length ? (
            <ul className="mt-2 space-y-0.5 text-[12.5px] text-fg" data-push-changes>
              {p.changes.slice(0, 30).map((c) => (
                <li key={c.path}>
                  <span className="text-subtle">{c.change}</span> <Theirs>{c.path}</Theirs>
                  {c.lines ? <span className="text-subtle"> ({c.lines} lines)</span> : null}
                </li>
              ))}
              {p.changes.length > 30 ? <li className="text-subtle">and {p.changes.length - 30} more</li> : null}
            </ul>
          ) : null}
          {p.helper ? <p className="mt-2 text-[12.5px] text-muted">{p.helper}</p> : null}
          {p.problems.length ? (
            <ul className="mt-3 space-y-1">
              {p.problems.map((x) => (
                <li key={x} className="flex gap-2 text-[13px] leading-relaxed text-fg">
                  <WarningCircle size={15} weight="fill" className="mt-[2px] shrink-0 text-accent-text" />
                  <span>{x}</span>
                </li>
              ))}
            </ul>
          ) : null}
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <Button variant="primary" size="sm" icon={<GitPullRequest size={13} />} disabled={!p.ready || busy || plan.isFetching} loading={start.isPending} onClick={() => void onSend(p)} data-push-send>
              Send them back
            </Button>
            {job.busy ? <Working line={job.line} pct={job.pct} /> : null}
          </div>
          <Details compact className="mt-3">
            <div>To: {p.target.url}</div>
            <div>Original commit: {p.base}</div>
            {p.parent && p.parent !== p.base ? <div>On top of: {p.parent}</div> : null}
            <div>Plan: {p.fingerprint}</div>
            <div>
              In the terminal: metarush remotion push start {row.name}@{p.versionNumber}
              {branch ? ` --branch ${branch}` : ''}
              {noPr ? ' --no-pr' : ''}
              {words ? ' --words' : ''}
            </div>
          </Details>
        </>
      ) : null}

      {list.data?.pushes.length ? (
        <div className="mt-4" data-push-history>
          <h5 className="mb-1.5 text-[13px] font-semibold text-fg">Sent back</h5>
          <ul className="divide-y divide-line overflow-hidden rounded-[12px] bg-raised hairline">
            {list.data.pushes.map((x) => (
              <li key={x.id} className="flex flex-wrap items-center gap-2 px-3 py-2 text-[12.5px]">
                <GitBranch size={13} className="text-subtle" />
                <span className="font-medium text-fg">Version {x.versionNumber}</span>
                <span className="text-muted">
                  → <Theirs>{x.target.where}</Theirs>, <Theirs>{x.branch}</Theirs>
                </span>
                <Badge tone={x.state === 'done' ? 'ok' : x.state === 'failed' ? 'danger' : 'accent'} icon={x.state === 'done' ? <CheckCircle size={12} weight="fill" /> : x.state === 'failed' ? <XCircle size={12} weight="fill" /> : undefined}>
                  {x.stateLabel}
                </Badge>
                <span className="text-subtle">{relative(x.createdAt)}</span>
                {x.prUrl ? (
                  <a className="text-accent-text hover:underline" href={x.prUrl} target="_blank" rel="noreferrer noopener">
                    The pull request
                  </a>
                ) : x.branchUrl ? (
                  <a className="text-accent-text hover:underline" href={x.branchUrl} target="_blank" rel="noreferrer noopener">
                    The branch
                  </a>
                ) : null}
                {x.error ? <span className="basis-full text-fg">{x.error}</span> : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
