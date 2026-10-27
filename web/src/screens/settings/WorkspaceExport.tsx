import { useState } from 'react';
import { Archive, Export } from '@phosphor-icons/react';
import { useOp, useOpMutation } from '../../api/queries';
import type { OpOutput } from '../../api/types';
import { bytes, plural, relative } from '../../lib/format';
import { useHost } from '../../lib/host';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, CopyBlock, Details, ErrorPanel, Segmented, Skeleton, Theirs } from '../../ui/primitives';

/*
 * Settings → Storage → Export the workspace (D329–D333): one archive of
 * everything about the work, for a backup or a move. Owners only, people
 * only. Check what it would hold first (workspace.export plan), then Export…
 * asks in a styled dialog and makes the archive in MetaRush's own archives
 * folder on the computer it runs on, in the background; the list follows it
 * (workspace.exports). A restore is never done here: it is a terminal step
 * (metarush workspace restore) or part of setting up a new computer
 * (metarush setup --restore), and the card says so.
 */

type Media = 'referenced' | 'included';
type Plan = NonNullable<OpOutput<'workspace.export'>['plan']>;
type Record = OpOutput<'workspace.exports'>['exports'][number];

const STATE_TONE: { [k: string]: 'ok' | 'accent' | 'danger' | 'neutral' } = { running: 'accent', done: 'ok', failed: 'danger', stopped: 'neutral' };
const STATE_WORD: { [k: string]: string } = { running: 'Being made', done: 'Ready', failed: 'Didn’t finish', stopped: 'Stopped' };

export function WorkspaceExport() {
  const host = useHost();
  const [media, setMedia] = useState<Media>('referenced');
  const [plan, setPlan] = useState<Plan | null>(null);
  const exports = useOp('workspace.exports', {}, {
    refetchInterval: (q) => (q.state.data?.exports.some((e) => e.state === 'running') ? 2000 : false),
  });
  const planner = useOpMutation('workspace.export');
  const start = useOpMutation('workspace.export', { invalidate: ['workspace.exports'] });
  const dialog = useDialog();
  const toast = useToast();
  const running = exports.data?.exports.some((e) => e.state === 'running') ?? false;

  const check = (m: Media = media) => {
    setPlan(null);
    planner.mutate(
      { plan: true, media: m },
      {
        onSuccess: (o) => setPlan(o.plan ?? null),
        onError: (e) => toast({ tone: 'error', title: 'Couldn’t check the export', body: [e.message, e.hint].filter(Boolean).join(' ') }),
      },
    );
  };

  const exportNow = async () => {
    const ok = await dialog.confirm({
      title: 'Export the workspace?',
      body: (
        <div className="space-y-2">
          <p>{exports.data?.holds}</p>
          <p>
            The archive goes into MetaRush’s own folder {host.remote ? `on ${host.machine}` : 'on this computer'}, as one file
            {media === 'included' ? ', with every file of footage copied in and checked' : ' that lists the footage by where it is kept'}. Nothing in the workspace changes.
          </p>
        </div>
      ),
      confirmLabel: 'Export',
    });
    if (!ok) return;
    start.mutate(
      { background: true, media },
      {
        onSuccess: (o) => toast({ tone: 'success', title: o.summary }),
        onError: (e) => toast({ tone: 'error', title: 'Couldn’t start the export', body: [e.message, e.hint].filter(Boolean).join(' ') }),
      },
    );
  };

  return (
    <section id="export" className="panel scroll-mt-28 p-5" aria-labelledby="export-h" data-workspace-export>
      <h2 id="export-h" className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <Archive size={17} />
        </span>
        Export the workspace
      </h2>
      <p className="mb-4 max-w-[78ch] text-[12.5px] leading-relaxed text-muted" data-export-holds>
        {exports.data?.holds ??
          'An export holds everything about the work in this workspace, people and their consent records included. Passwords, keys and each computer’s own settings are never in it.'}
      </p>
      {exports.error ? <ErrorPanel compact error={exports.error} onRetry={() => void exports.refetch()} /> : null}

      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-3">
          <span className="text-[12.5px] text-muted">The footage</span>
          <Segmented
            label="The footage"
            className="max-w-full flex-wrap"
            value={media}
            onChange={(v) => {
              setMedia(v);
              if (plan) check(v);
            }}
            items={[
              { value: 'referenced', label: 'Listed by where it’s kept' },
              { value: 'included', label: 'Copied into the archive' },
            ]}
          />
        </div>
        <p className="max-w-[78ch] text-[12px] leading-relaxed text-subtle">
          {media === 'referenced'
            ? 'A small archive: the copy expects the footage in the same storage (or it can be moved there later).'
            : 'Every file of footage goes into the archive and is checked: as big as the footage, and the copy can use any storage.'}
        </p>
        <div className="flex flex-wrap gap-2">
          <Button variant="secondary" onClick={() => check()} loading={planner.isPending} data-export-check>
            Check what it would hold
          </Button>
          <Button variant="primary" icon={<Export size={15} />} onClick={() => void exportNow()} disabled={running || start.isPending} data-export-start>
            Export…
          </Button>
        </div>

        {plan ? <PlanView plan={plan} /> : null}

        {exports.isLoading ? <Skeleton className="h-12" /> : null}
        {exports.data?.exports.length ? (
          <ul className="divide-y divide-line rounded-[12px] hairline" aria-label="Exports made on this computer" data-export-list>
            {exports.data.exports.map((e) => (
              <ExportRow key={e.id} rec={e} />
            ))}
          </ul>
        ) : null}

        <div className="rounded-[10px] bg-inset px-3 py-2.5 text-[12.5px] leading-relaxed text-muted" data-restore-note>
          <p>Restoring an export happens in a terminal on the computer the copy goes to, never here. It checks every file first, writes only into an empty database and never deletes anything.</p>
          <Details label="The commands for a terminal">
            <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
              <CopyBlock text="metarush setup --restore <archive>" caption="On a new computer: set MetaRush up from the export." />
              <CopyBlock text="metarush workspace restore <archive> --plan" caption="Into an empty database: check first, then run it again without --plan." />
            </div>
          </Details>
        </div>
      </div>
    </section>
  );
}

function PlanView({ plan }: { plan: Plan }) {
  const records = plan.collections.reduce((n, c) => n + c.count, 0);
  const shown = plan.collections.filter((c) => c.count > 0);
  const out = plan.leftOut.filter((l) => l.count > 0 || l.unknown);
  return (
    <div className="space-y-2 rounded-[12px] px-3 py-3 hairline" data-export-plan>
      <p className="text-[13px] text-fg">
        The archive of <Theirs>{plan.workspace.name}</Theirs> would hold {plural(records, 'record')} and{' '}
        {plan.media.mode === 'included' ? `${plural(plan.media.files, 'file')} of footage (${bytes(plan.media.bytes)})` : `a list of ${plural(plan.media.files, 'file')} of footage`}: about{' '}
        {bytes(plan.bytes)}
        {plan.free ? `, with ${bytes(plan.free)} free where it goes` : ''}.
      </p>
      {plan.notes?.map((n) => (
        <p key={n} className="text-[12.5px] text-accent-text">
          {n}
        </p>
      ))}
      {out.length ? (
        <div className="text-[12.5px] text-muted">
          <span className="font-medium text-fg">Stays out on purpose: </span>
          {out.map((l) => l.why).join('; ')}.
        </div>
      ) : null}
      <Details label="What goes in">
        <ul className="grid grid-cols-1 gap-x-6 gap-y-1 sm:grid-cols-2">
          {shown.map((c) => (
            <li key={c.name} className="flex justify-between gap-3 text-[12px]">
              <span className="truncate" title={c.why}>
                {c.why}
              </span>
              <span className="tc shrink-0 text-subtle">{c.count.toLocaleString()}</span>
            </li>
          ))}
        </ul>
      </Details>
    </div>
  );
}

function ExportRow({ rec }: { rec: Record }) {
  return (
    <li className="flex flex-col gap-1 px-3 py-2.5 sm:flex-row sm:items-center sm:justify-between" data-export-row={rec.state}>
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2 text-[13px]">
          <Theirs className="truncate font-medium">{rec.name}</Theirs>
          <Badge tone={STATE_TONE[rec.state] ?? 'neutral'}>{STATE_WORD[rec.state] ?? rec.state}</Badge>
        </div>
        <p className="text-[12px] text-muted" data-export-says>
          {rec.says}
        </p>
        <Details compact>
          <div className="break-all">
            Archive <span className="tc text-muted">{rec.path}</span>
          </div>
        </Details>
      </div>
      <span className="shrink-0 text-[11.5px] text-subtle">{relative(rec.startedAt)}</span>
    </li>
  );
}
