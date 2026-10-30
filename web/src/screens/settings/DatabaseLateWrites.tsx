import { useState } from 'react';
import { ArrowsClockwise, MagnifyingGlass } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import { useOp, useOpMutation } from '../../api/queries';
import type { DatabaseLateShowOut, LateLookView, LateWriteView } from '../../api/types';
import { absolute, stamp } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, cx, Details, Spinner, Theirs } from '../../ui/primitives';

/*
 * Settings → Storage → Move the database, after a move into this database
 * (D352–D358): MetaRush looks at the old database again — right after the
 * move, then about 15 minutes, an hour and a day later — for anything a
 * computer that was asleep wrote there late. What it finds is listed here in
 * plain words; an owner carries a record over (only while this database's
 * own record is still the one the move copied: otherwise both versions are
 * shown and nothing changes) or leaves it, after a styled dialog. A record
 * removed from the old database is never removed here.
 */

const KIND: Record<string, string> = { new: 'added there', changed: 'changed there', gone: 'removed there' };

export function LateWrites({ late, onChanged }: { late: LateLookView; onChanged: () => void }) {
  const toast = useToast();
  const list = useOp('database.late.writes', {}, { enabled: late.found > 0, staleTime: 5_000 });
  const look = useOpMutation('database.late.writes', { invalidate: ['database.move.status'] });
  const found = list.data?.found ?? [];
  const lastLook = late.looks.at(-1)?.at;
  const lookAgain = () =>
    look.mutate(
      { look: true },
      {
        onSuccess: (o) => {
          toast(o.problem ? { tone: 'error', title: o.problem } : { tone: 'success', title: o.summary });
          void list.refetch();
          onChanged();
        },
        onError: (e) => toast({ tone: 'error', title: 'MetaRush couldn’t look again', body: [e.message, e.hint].filter(Boolean).join(' ') }),
      },
    );
  const tone = late.waiting > 0 ? 'bg-accent-soft' : 'bg-hover';
  return (
    <div className={cx('mt-4 rounded-[12px] px-3.5 py-3', tone)} data-database-late={late.waiting > 0 ? 'waiting' : late.ended ? 'ended' : 'looking'}>
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
        <div className="min-w-0 flex-1 text-[13px] leading-relaxed">
          <div className="font-medium text-fg">Anything written into the old database late</div>
          <div className="text-muted" data-database-late-summary role="status">
            {late.summary}
          </div>
          {late.problem && !late.ended ? <div className="mt-0.5 text-[12.5px] text-muted">The last look couldn’t be made: {late.problem}</div> : null}
          {lastLook ? (
            <div className="tc mt-0.5 text-[11.5px] text-subtle">
              Last looked <span title={absolute(lastLook)}>{stamp(lastLook)}</span>
              {late.next && !late.ended ? (
                <>
                  {' · '}next <span title={absolute(late.next)}>{stamp(late.next)}</span>
                </>
              ) : null}
            </div>
          ) : null}
        </div>
        {late.here && !late.ended ? (
          <Button variant="secondary" size="sm" icon={<ArrowsClockwise size={14} />} onClick={lookAgain} loading={look.isPending} data-database-late-look>
            Look again now
          </Button>
        ) : null}
      </div>
      {late.found > 0 ? (
        list.isLoading ? (
          <div className="mt-2 flex items-center gap-2 text-[12.5px] text-muted">
            <Spinner size={12} /> Reading what was found…
          </div>
        ) : (
          <ol className="mt-2.5 space-y-2" data-database-late-list>
            {found.map((f) => (
              <LateItem
                key={f.id}
                f={f}
                onDecided={() => {
                  void list.refetch();
                  onChanged();
                }}
              />
            ))}
          </ol>
        )
      ) : null}
    </div>
  );
}

function LateItem({ f, onDecided }: { f: LateWriteView; onDecided: () => void }) {
  const dialog = useDialog();
  const toast = useToast();
  const [both, setBoth] = useState<DatabaseLateShowOut | null>(null);
  const [showing, setShowing] = useState(false);
  const carry = useOpMutation('database.late.carry', { invalidate: ['database.move.status'] });
  const leave = useOpMutation('database.late.leave', { invalidate: ['database.move.status'] });
  const show = async () => {
    if (both) {
      setBoth(null);
      return;
    }
    setShowing(true);
    try {
      setBoth(await call('database.late.show', { id: f.id }));
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'MetaRush couldn’t show both versions', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setShowing(false);
    }
  };
  const doCarry = async () => {
    const ok = await dialog.confirm({
      title: 'Carry it over?',
      body: (
        <div className="space-y-2">
          <p>{f.says}</p>
          <p>The old database’s version goes into this database, in place of the one the move copied. If this database’s own changed meanwhile, nothing changes and both versions are shown.</p>
        </div>
      ),
      confirmLabel: 'Carry it over',
    });
    if (!ok) return;
    carry.mutate(
      { ids: [f.id] },
      {
        onSuccess: (o) => {
          toast({ tone: 'success', title: o.summary });
          setBoth(null);
          onDecided();
        },
        onError: (e) => {
          toast({ tone: 'error', title: 'Not carried over', body: [e.message, e.hint].filter(Boolean).join(' ') });
          void call('database.late.show', { id: f.id }).then(setBoth, () => undefined);
          onDecided();
        },
      },
    );
  };
  const doLeave = async () => {
    const ok = await dialog.confirm({
      title: 'Leave it as it is here?',
      body: (
        <div className="space-y-2">
          <p>{f.says}</p>
          <p>This database keeps its own record. What was found stays in this list as decided.</p>
        </div>
      ),
      confirmLabel: 'Leave it',
    });
    if (!ok) return;
    leave.mutate(
      { ids: [f.id] },
      {
        onSuccess: (o) => {
          toast({ tone: 'success', title: o.summary });
          onDecided();
        },
        onError: (e) => toast({ tone: 'error', title: 'Not left', body: [e.message, e.hint].filter(Boolean).join(' ') }),
      },
    );
  };
  const waiting = f.state === 'waiting';
  return (
    <li className="rounded-[10px] bg-panel px-3 py-2.5 hairline" data-database-late-item={f.state} data-database-late-kind={f.kind}>
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
        <div className="min-w-0 flex-1 text-[12.5px] leading-relaxed">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="font-medium text-fg">{f.n}.</span>
            <Badge tone={f.kind === 'gone' ? 'danger' : 'accent'}>{KIND[f.kind] ?? f.kind}</Badge>
            {waiting ? null : <Badge tone="ok">{f.state === 'carried' ? 'carried over' : 'left as it is'}</Badge>}
            {waiting && f.copyChanged ? <Badge tone="info">changed here too</Badge> : null}
          </div>
          <p className="mt-0.5 text-muted">{f.says}</p>
          <Details compact label="Details">
            <span className="font-mono">
              {f.collection} {f.record}
            </span>
          </Details>
        </div>
        <div className="flex flex-wrap gap-1.5">
          {f.kind !== 'gone' ? (
            <Button variant="ghost" size="sm" icon={<MagnifyingGlass size={13} />} onClick={() => void show()} loading={showing} data-database-late-show>
              {both ? 'Hide both versions' : 'Compare'}
            </Button>
          ) : null}
          {waiting && f.canCarry ? (
            <Button variant="primary" size="sm" onClick={() => void doCarry()} loading={carry.isPending} data-database-late-carry>
              Carry it over
            </Button>
          ) : null}
          {waiting ? (
            <Button variant="secondary" size="sm" onClick={() => void doLeave()} loading={leave.isPending} data-database-late-leave>
              Leave it
            </Button>
          ) : null}
        </div>
      </div>
      {both ? <BothVersions both={both} /> : null}
    </li>
  );
}

function BothVersions({ both }: { both: DatabaseLateShowOut }) {
  const pretty = (v: unknown) => (v == null ? '' : JSON.stringify(v, null, 2));
  return (
    <div className="mt-2 grid gap-2 sm:grid-cols-2" data-database-late-both>
      {[
        { title: 'In the old database', v: both.old },
        { title: 'In this database', v: both.here },
      ].map((x) => (
        <div key={x.title} className="min-w-0">
          <div className="text-[11.5px] font-medium text-muted">{x.title}</div>
          {x.v == null ? (
            <div className="text-[12px] text-subtle">Not there.</div>
          ) : (
            <pre className="mt-1 max-h-64 overflow-auto rounded-[8px] bg-hover p-2 text-[11px] leading-snug">
              <Theirs>{pretty(x.v)}</Theirs>
            </pre>
          )}
        </div>
      ))}
      {both.differs.length ? <div className="text-[11.5px] text-subtle sm:col-span-2">They differ in: {both.differs.join(', ')}.</div> : null}
    </div>
  );
}
