import { useState } from 'react';
import { Link } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { CheckCircle, HandPalm, Seal, ShieldWarning, Wrench } from '@phosphor-icons/react';
import type { ops } from '@metarush/schema';
import { call, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import { askInProject } from '../layout/assistant/store';
import { absolute, stamp } from '../lib/format';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, ErrorPanel, Skeleton, Theirs, cx } from '../ui/primitives';
import { useToast } from '../ui/Toast';

/*
 * Ready to deliver, or Held by… (D298, D302): one answer from delivery.ready —
 * the same words `metarush deliver ready` prints and delivery refuses with —
 * and a step for each hold: fix a problem (its own change, saved as a new
 * version), check the render (again), tick a sign-off, make the final render,
 * or deliver anyway with a note (people only, D299; kept and shown on the
 * receipt). The promises made to the client show as the check measured them
 * (D300).
 */

type Gate = ops.ShipGateOut;
type Hold = ops.GateHold;

const QUERIES = ['delivery.ready', 'project.show', 'cut.signoffs.list', 'project.receipt'] as const;

const promiseTone: Record<string, 'ok' | 'danger' | 'info' | 'neutral'> = { kept: 'ok', broken: 'danger', unmeasured: 'info', notApplicable: 'neutral' };
const promiseWord: Record<string, string> = { kept: 'Kept', broken: 'Not kept', unmeasured: "Couldn't measure", notApplicable: "Doesn't apply" };

export function ShipGate({ projectId, revisionId, className }: { projectId: string; revisionId: string; className?: string }) {
  const gate = useOp('delivery.ready', { projectId, revision: revisionId }, { staleTime: 5_000 });
  const qc = useQueryClient();
  const dialog = useDialog();
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const refresh = () => {
    for (const k of QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  };

  if (gate.isLoading) return <Skeleton className={cx('h-20 rounded-[14px]', className)} />;
  if (gate.error || !gate.data) return <ErrorPanel error={gate.error} compact onRetry={() => void gate.refetch()} />;
  const g: Gate = gate.data;
  const open = g.holds.filter((h) => !h.covered);
  const overridable = open.filter((h) => h.overridable);
  const held = g.state === 'held';
  const tone = held ? 'danger' : g.state === 'sent-anyway' || g.state === 'reminders' ? 'info' : 'ok';

  const run = async (key: string, fn: () => Promise<string>) => {
    setBusy(key);
    try {
      const said = await fn();
      refresh();
      toast({ tone: 'success', title: said });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'Nothing changed', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(null);
    }
  };

  const takePath = (h: Hold, p: ops.GatePath) => {
    if (!p.op) return;
    void run(h.key + p.label, async () => {
      if (p.op === 'revision.approve') {
        const ok = await dialog.confirm({ title: `Approve version ${g.number}?`, body: 'Only people approve versions.', confirmLabel: 'Approve' });
        if (!ok) throw new Error('Not approved.');
      }
      const out = (await call(p.op as 'qa.run', p.input as ops.QARunIn)) as { summary?: string; note?: string } | undefined;
      return out?.summary ?? out?.note ?? `${p.label}: done.`;
    });
  };

  const fix = (h: Hold, i: number, pr: ops.GateProblem) =>
    void run(`${h.key}#${i}`, async () => {
      const out = await call('edit.apply', {
        plan: { baseRevisionId: revisionId, origin: 'qa-fix:ship-gate', instruction: pr.fix || 'Fix what the quality check found', ops: pr.ops as never },
      });
      return `Saved as version ${out.revision.number}: render and check it, then deliver that one.`;
    });

  const anyway = async () => {
    const note = await dialog.prompt({
      title: `Deliver version ${g.number} anyway?`,
      body: (
        <div className="space-y-2 text-[13px]">
          <p>It is held by:</p>
          <ul className="list-disc space-y-0.5 pl-5">
            {overridable.map((h) => (
              <li key={h.key}>
                {h.says}
                {(h.problems ?? []).length > 0 ? (
                  <ul className="mt-0.5 list-[circle] pl-5 text-muted">
                    {(h.problems ?? []).slice(0, 3).map((pr, i) => (
                      <li key={i}>{pr.message}</li>
                    ))}
                  </ul>
                ) : null}
              </li>
            ))}
          </ul>
          <p className="text-muted">Your note is kept with the version, with your name and the time, and shows on the receipt and in what delivery writes next to the video.</p>
        </div>
      ),
      label: 'Why it goes out anyway',
      placeholder: 'The client needs it today; captions come in the next version.',
      multiline: true,
      required: true,
      maxLength: 500,
      confirmLabel: 'Deliver anyway',
      validate: (v) => (v.trim().length < 3 ? 'Say why in a few words.' : null),
    });
    if (!note) return;
    // The note covers exactly the holds shown in the dialog (D299): if they changed meanwhile, it is refused.
    void run('anyway', async () => (await call('delivery.override', { projectId, revision: revisionId, note: note.trim(), holds: overridable.map((h) => h.key) })).summary);
  };

  return (
    <section aria-label="Ready to deliver" className={cx('rounded-[14px] px-4 py-3 hairline', held ? 'bg-danger-soft/40' : 'bg-panel', className)} data-ship-gate data-state={g.state}>
      <div className="flex flex-wrap items-center gap-2">
        {held ? <HandPalm size={18} className="text-danger" aria-hidden /> : <CheckCircle size={18} className={tone === 'ok' ? 'text-ok' : 'text-info'} aria-hidden />}
        <h2 className="text-[14px] font-semibold" data-ship-headline>
          {g.headline}
        </h2>
        {!g.on ? <Badge tone="neutral">Reminders only</Badge> : null}
        <div className="ml-auto flex flex-wrap gap-1.5">
          {held && overridable.length > 0 ? (
            <Button size="sm" variant="secondary" icon={<ShieldWarning size={13} />} onClick={() => void anyway()} disabled={busy !== null} data-ship-anyway>
              Deliver anyway…
            </Button>
          ) : null}
        </div>
      </div>
      {open.length > 0 ? (
        <ul className="mt-2 space-y-2 text-[13px]" data-ship-holds>
          {open.map((h) => (
            <li key={h.key} data-hold={h.kind}>
              <div className="flex flex-wrap items-center gap-2">
                <span className={cx(!g.on && 'text-muted')}>{h.says}</span>
                {h.paths.map((p) =>
                  p.op ? (
                    <Button key={p.label} size="sm" variant="ghost" onClick={() => takePath(h, p)} disabled={busy !== null} data-hold-path={p.op}>
                      {p.label}
                    </Button>
                  ) : p.link ? (
                    <Link key={p.label} to={p.link} className="text-[12.5px] font-medium text-accent-text hover:underline">
                      {p.label}
                    </Link>
                  ) : null,
                )}
              </div>
              {(h.problems ?? []).length > 0 ? (
                <ul className="mt-1 space-y-1 border-l border-line pl-3">
                  {(h.problems ?? []).map((pr, i) => (
                    <li key={i} className="text-[12.5px]" data-problem>
                      <span className="mr-2 tabular-nums text-subtle">{pr.at}</span>
                      {pr.promise ? (
                        <Badge tone="danger" className="mr-1.5">
                          Promise
                        </Badge>
                      ) : null}
                      {pr.message}
                      {pr.fix ? <span className="text-muted"> Fix: {pr.fix}</span> : null}
                      <span className="ml-2 inline-flex gap-1">
                        {(pr.ops ?? []).length > 0 ? (
                          <Button size="sm" variant="ghost" icon={<Wrench size={12} />} onClick={() => fix(h, i, pr)} disabled={busy !== null} data-problem-fix>
                            Fix it
                          </Button>
                        ) : (
                          <Button size="sm" variant="ghost" onClick={() => askInProject(projectId, `Fix this in version ${g.number}: ${pr.message}`)}>
                            Ask Claude to fix it
                          </Button>
                        )}
                      </span>
                    </li>
                  ))}
                </ul>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}
      {!g.on ? <p className="mt-2 text-[12.5px] text-muted">{g.why}</p> : null}
      {(g.sentAnyway ?? []).map((o) => (
        <p key={o.id} className="mt-2 text-[12.5px] text-muted" data-ship-override>
          {o.by.userName || 'A person'}'s note, <span title={absolute(o.at)}>{stamp(o.at)}</span>: <Theirs>“{o.note}”</Theirs>
        </p>
      ))}
      {(g.warnings ?? []).map((w) => (
        <p key={w} className="mt-2 text-[12.5px] text-info" data-ship-warning>
          {w}
        </p>
      ))}
      {g.promises.length > 0 ? (
        <div className="mt-3" data-ship-promises>
          <div className="mb-1 flex items-center gap-1.5 text-[11.5px] font-medium uppercase tracking-wide text-subtle">
            <Seal size={12} aria-hidden /> Promises made to the client
          </div>
          <ul className="space-y-1 text-[12.5px]">
            {g.promises.map((p) => (
              <li key={p.ruleId} className="flex flex-wrap items-baseline gap-2" data-promise={p.status}>
                <Badge tone={promiseTone[p.status] ?? 'neutral'}>{promiseWord[p.status] ?? p.status}</Badge>
                <Theirs className="font-medium">“{p.text}”</Theirs>
                <span className="text-muted">{p.measured}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </section>
  );
}
