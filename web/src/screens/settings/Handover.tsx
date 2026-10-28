import { useId, useState } from 'react';
import { ArrowsLeftRight, Desktop, WarningCircle } from '@phosphor-icons/react';
import { useOp, useOpMutation } from '../../api/queries';
import type { OpOutput } from '../../api/types';
import { OwnerOnly } from '../../components/OwnerOnly';
import { capFirst, plural, stamp } from '../../lib/format';
import { useHost } from '../../lib/host';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, CopyBlock, Details, ErrorPanel, Spinner, Theirs } from '../../ui/primitives';

/*
 * Settings → Storage → "Hand over this computer's role" (D344–D348). A dry run
 * first: pick the computer that takes it over and the card lists, in plain
 * words, everything tied to this computer and what happens to each — the
 * panel's conversations (and whether each one's Claude Code session is here),
 * recipe runs and planning by itself, pinned work, folders, remote access,
 * extension pairings, keys by name, the background role — with the steps only
 * a person takes. The handover itself happens in a terminal: the file carries
 * the conversations and asks for a passphrase there, never here. The card
 * follows a handover in progress (Abandon… before it is received), says when
 * this computer is stepping down or works as a client, and offers clean only
 * after the other computer received it. Owners, people only.
 */

type Plan = OpOutput<'machine.handover.plan'>;
type Status = OpOutput<'machine.handover.status'>;
type Record = Status['recent'][number];

const STATE_WORD: { [k: string]: string } = {
  exporting: 'File being made',
  exported: 'Waiting to be received',
  receiving: 'Being received',
  received: 'Received',
  done: 'Done',
  abandoned: 'Abandoned',
};

export function useHandoverStatus() {
  return useOp(
    'machine.handover.status',
    {},
    {
      refetchInterval: (q) => (q.state.data?.inProgress.length || q.state.data?.role.role === 'stepping-down' ? 5_000 : 60_000),
      staleTime: 2_000,
      retry: 3,
    },
  );
}

export function Handover() {
  const host = useHost();
  const st = useHandoverStatus();
  const s = st.data;
  const [open, setOpen] = useState(false);
  const here = host.remote ? <Theirs>{host.machine}</Theirs> : 'this computer';
  return (
    <section id="handover" aria-labelledby="handover-h" className="panel scroll-mt-28 p-5" data-handover>
      <h2 id="handover-h" className="mb-1 flex flex-wrap items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <ArrowsLeftRight size={17} />
        </span>
        <span>Hand over this computer’s role</span>
        {s && s.role.role !== 'host' ? (
          <Badge tone="accent" className="max-sm:hidden">
            {s.role.role === 'client' ? 'a client now' : 'stepping down'}
          </Badge>
        ) : null}
      </h2>
      <p className="mb-3 max-w-[78ch] text-[13px] leading-relaxed text-muted">
        Moving to another computer? Everything tied to {here} goes to it: the dashboard and background work, the conversations in Claude’s panel, recipe runs, planning by itself,
        the work waiting here and the watched and linked folders. Keys never travel: each computer keeps its own. Nothing is deleted here unless you ask.
      </p>
      {st.error && !s ? <ErrorPanel compact error={st.error} onRetry={() => void st.refetch()} /> : null}
      {!s && st.isLoading ? (
        <div className="flex items-center gap-2 text-[13px] text-muted">
          <Spinner size={13} /> Looking at this computer’s role…
        </div>
      ) : null}
      {s ? <StatusView s={s} /> : null}
      {s && s.role.role === 'host' && !s.inProgress.some((r) => r.from === s.computer.id) ? (
        <OwnerOnly className="mt-3">
          {open ? (
            <PlanView onClose={() => setOpen(false)} />
          ) : (
            <Button variant="secondary" icon={<ArrowsLeftRight size={15} />} onClick={() => setOpen(true)} data-handover-open>
              Hand over this computer’s role…
            </Button>
          )}
        </OwnerOnly>
      ) : null}
      <Details label="A computer that’s gone for good" className="mt-4">
        <div className="space-y-2 text-[12.5px] leading-relaxed text-muted">
          <p>
            If a computer can’t hand its role over any more (it broke, or it was wiped), take over from it on the computer that goes on. That works once it hasn’t been seen for 30
            minutes and you confirm it is off — a computer still running would do the same work twice. Its conversations come from a plain copy of its folders; any without Claude
            Code’s own record go on from a summary.
          </p>
          <CopyBlock
            text={'metarush machine take-over "<computer>" --from <copy of its folders> --plan'}
            caption="In a terminal on the computer that takes over: say what it would do, then run it again without --plan."
          />
        </div>
      </Details>
    </section>
  );
}

function StatusView({ s }: { s: Status }) {
  const dialog = useDialog();
  const toast = useToast();
  const abandon = useOpMutation('machine.handover.abandon', {
    invalidate: ['machine.handover.status'],
  });
  const me = s.computer.id;
  const mine = s.inProgress.filter((r) => r.from === me || r.to === me);
  const giveUp = async (r: Record) => {
    const ok = await dialog.confirm({
      title: 'Abandon the handover?',
      body:
        r.state === 'receiving' ? (
          <p>
            Only when <Theirs>{r.toName}</Theirs> stopped part-way and has been quiet for 30 minutes: what it already took stays there, the rest goes on on{' '}
            <Theirs>{r.fromName}</Theirs>, and the handover file can’t be received any more.
          </p>
        ) : (
          <p>
            The conversations go on on <Theirs>{r.fromName}</Theirs> as before, and the handover file can’t be received any more. Delete the file if you made one.
          </p>
        ),
      confirmLabel: 'Abandon',
      tone: 'danger',
    });
    if (!ok) return;
    abandon.mutate(
      { id: r.id },
      {
        onSuccess: (o) => toast({ tone: 'success', title: o.summary }),
        onError: (e) =>
          toast({
            tone: 'error',
            title: 'Couldn’t abandon it',
            body: [e.message, e.hint].filter(Boolean).join(' '),
          }),
      },
    );
  };
  return (
    <div className="space-y-3" data-handover-status={s.role.role}>
      {s.role.role !== 'host' || mine.length ? (
        <p className="max-w-[78ch] rounded-[10px] bg-accent-soft px-3 py-2 text-[12.5px] leading-relaxed text-accent-text" data-handover-says>
          {s.summary}
        </p>
      ) : null}
      {mine.map((r) => (
        <div key={r.id} className="flex flex-col gap-2 rounded-[12px] px-3 py-2.5 hairline sm:flex-row sm:items-center sm:justify-between" data-handover-row={r.state}>
          <div className="min-w-0 text-[13px]">
            <div className="flex flex-wrap items-center gap-2">
              <Desktop size={14} className="text-subtle" aria-hidden />
              <Theirs className="font-medium">{r.fromName}</Theirs>
              <span className="text-subtle">to</span>
              <Theirs className="font-medium">{r.toName}</Theirs>
              <Badge tone={r.state === 'received' ? 'ok' : 'accent'}>{STATE_WORD[r.state] ?? r.state}</Badge>
            </div>
            <p className="mt-0.5 text-[12px] text-muted">
              {plural(r.conversations?.length ?? 0, 'conversation')}, started {stamp(r.startedAt)}
            </p>
          </div>
          {r.from === me && (r.state === 'exported' || r.state === 'exporting' || r.state === 'receiving') ? (
            <OwnerOnly>
              <Button size="sm" variant="secondary" onClick={() => void giveUp(r)} loading={abandon.isPending} data-handover-abandon>
                Abandon…
              </Button>
            </OwnerOnly>
          ) : null}
        </div>
      ))}
      {s.waiting.length ? (
        <div className="text-[12.5px] text-muted" data-handover-waiting>
          <p className="font-medium text-fg">Still to finish here before this computer steps down:</p>
          <ul className="mt-1 list-disc pl-5">
            {s.waiting.map((w) => (
              <li key={w.id}>
                {capFirst(w.words)}: {w.why}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {s.clean && s.role.role !== 'host' ? (
        <div className="space-y-1.5 text-[12.5px] text-muted" data-handover-clean>
          <p>
            This computer still has its copies of the conversations handed to <Theirs>{s.clean.toName}</Theirs>. Once you’re sure all is well there, remove them in a terminal here
            — never by themselves.
          </p>
          <Details label="The command for a terminal">
            <CopyBlock text="metarush machine handover clean" caption="Says what it removes and asks before it does." />
          </Details>
        </div>
      ) : null}
    </div>
  );
}

function PlanView({ onClose }: { onClose: () => void }) {
  const [to, setTo] = useState('');
  const sel = useId();
  const plan = useOp('machine.handover.plan', { to: to || undefined });
  const p = plan.data;
  return (
    <div className="min-w-0 space-y-4 rounded-[12px] p-3 hairline sm:p-4" data-handover-plan>
      <div>
        <label htmlFor={sel} className="text-[13px] font-medium text-fg">
          Which computer takes it over?
        </label>
        <select
          id={sel}
          value={to}
          onChange={(e) => setTo(e.target.value)}
          className="mt-2 h-9 w-full max-w-[360px] rounded-[10px] bg-panel px-3 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent focus:outline-offset-0"
          data-handover-to
        >
          <option value="">Choose a computer…</option>
          {(p?.computers ?? []).map((c) => (
            <option key={c.id} value={c.name}>
              {c.name}
            </option>
          ))}
        </select>
        {p && !p.computers.length ? <p className="mt-2 text-[12.5px] text-muted">No other computer has joined this workspace yet: invite it first, and join it there.</p> : null}
      </div>
      {plan.error ? <ErrorPanel compact error={plan.error} onRetry={() => void plan.refetch()} /> : null}
      {!p && plan.isLoading ? (
        <div className="flex items-center gap-2 text-[13px] text-muted">
          <Spinner size={13} /> Looking at what is tied to this computer…
        </div>
      ) : null}
      {p ? <PlanBody p={p} /> : null}
      <div>
        <Button variant="ghost" size="sm" onClick={onClose}>
          Close
        </Button>
      </div>
    </div>
  );
}

function PlanBody({ p }: { p: Plan }) {
  const to = p.to?.name;
  return (
    <div className="min-w-0 space-y-4 text-[12.5px] leading-relaxed [overflow-wrap:anywhere]" data-handover-ready={p.ready ? 'yes' : 'no'}>
      <p className="text-[13px] text-fg" data-handover-summary>
        {p.summary}
      </p>
      {p.blocking?.length ? (
        <div className="flex gap-2 rounded-[10px] bg-danger-soft px-3 py-2 text-danger" data-handover-blocking>
          <WarningCircle size={15} className="mt-0.5 shrink-0" aria-hidden />
          <ul className="space-y-1">
            {p.blocking.map((b) => (
              <li key={b}>{b}</li>
            ))}
          </ul>
        </div>
      ) : null}
      <Group title="Conversations in Claude’s panel" empty="None here." data-handover-group="conversations">
        {p.conversations.map((c) => (
          <li key={c.id} data-handover-conversation={c.session}>
            <Theirs className="font-medium text-fg">{c.title}</Theirs> — {c.words}
          </li>
        ))}
      </Group>
      <Group title="Work tied to this computer" empty="Nothing waiting here.">
        {p.work.map((w) => (
          <li key={w.id}>
            {capFirst(w.words)}: {w.why}
          </li>
        ))}
      </Group>
      <Group title="Folders" empty="No watched or linked folders here.">
        {p.folders.map((f) => (
          <li key={f.kind + f.path}>
            {f.words}
            <Details compact>
              <div className="break-all">
                Here <span className="tc text-muted">{f.path}</span>
              </div>
            </Details>
          </li>
        ))}
      </Group>
      <Group title="Remote access">
        <li>{p.remote.words}</li>
      </Group>
      {p.extensions.length ? (
        <Group title="Browser extensions paired here">
          {p.extensions.map((e) => (
            <li key={e.name}>
              <Theirs>{e.name}</Theirs>: {e.words}
            </li>
          ))}
        </Group>
      ) : null}
      <Group title="Keys on this computer (names only)">
        {p.keys.map((k) => (
          <li key={k.name}>
            {k.name}: {k.words}
          </li>
        ))}
      </Group>
      {p.abilities.length ? (
        <Group title="Abilities set up here">
          {p.abilities.map((a) => (
            <li key={a.name}>
              <Theirs>{a.name}</Theirs>: {a.words}
            </li>
          ))}
        </Group>
      ) : null}
      <Group title={p.background.name}>
        <li>{p.background.words}</li>
      </Group>
      <div data-handover-steps>
        <p className="mb-1.5 text-[13px] font-medium text-fg">Steps only a person takes</p>
        <ol className="list-decimal space-y-2 pl-5">
          {p.steps.map((st) => (
            <li key={st.words}>
              <span className="text-fg">{st.where === 'old' ? 'Here: ' : to ? `On ${to}: ` : 'On the new computer: '}</span>
              {st.words}
              {st.click ? <span className="text-subtle"> ({st.click})</span> : null}
            </li>
          ))}
        </ol>
        <Details label="The commands for a terminal" className="mt-2">
          <div className="space-y-2">
            {p.steps
              .filter((st) => st.command)
              .map((st) => (
                <CopyBlock key={st.words} text={st.command ?? ''} caption={st.words} />
              ))}
          </div>
        </Details>
      </div>
      {p.ready && to ? (
        <div className="space-y-2" data-handover-next>
          <p className="text-[13px] font-medium text-fg">Then, in a terminal</p>
          <p className="text-muted">
            Here, make the handover file: it asks for a passphrase. Carry the file yourself (a drive, AirDrop or scp), never through a shared or synced folder. Then on {to},
            receive it: it asks for the passphrase and where each folder is there.
          </p>
          <Details label="The commands for a terminal">
            <div className="space-y-2">
              <CopyBlock text={p.next[0] ?? ''} caption="Here: makes the handover file." />
              <CopyBlock text={'metarush machine handover receive <file>'} caption={`On ${to}: receives it.`} />
            </div>
          </Details>
        </div>
      ) : null}
    </div>
  );
}

function Group({
  title,
  empty,
  children,
  ...rest
}: { title: string; empty?: string; children: React.ReactNode } & {
  [k: `data-${string}`]: string;
}) {
  const items = Array.isArray(children) ? children.filter(Boolean) : children ? [children] : [];
  return (
    <div {...rest}>
      <p className="mb-1 text-[13px] font-medium text-fg">{title}</p>
      {items.length ? <ul className="list-disc space-y-1 pl-5 text-muted">{children}</ul> : <p className="text-muted">{empty}</p>}
    </div>
  );
}
