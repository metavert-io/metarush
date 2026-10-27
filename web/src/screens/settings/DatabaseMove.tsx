import { useId, useRef, useState, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowsLeftRight, CheckCircle, Database, Eye, EyeSlash, WarningCircle } from '@phosphor-icons/react';
import { request, toApiError, type ApiError } from '../../api/client';
import { useOp, useOpMutation } from '../../api/queries';
import { useHost } from '../../lib/host';
import { bytes } from '../../lib/format';
import type {
  DatabaseMoveForm,
  DatabaseMovePlanIn,
  DatabaseMovePlanOut,
  DatabaseMoveStartIn,
  DatabaseMoveStartOut,
  DatabaseMoveStatusOut,
  DatabasePlanOut,
  DatabaseSwitchFormIn,
  DatabaseSwitchOut,
} from '../../api/types';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, CopyBlock, cx, Details, ErrorPanel, Spinner, Theirs } from '../../ui/primitives';

/*
 * Settings → Storage → "Move the database" (D334–D338). The form says where the
 * workspace's database goes: the database on this computer, or another MongoDB
 * (Atlas or your own server) whose address is typed once — it goes to this
 * computer's keychain and never comes back. "Check" plans the move through
 * /api/database/plan (nothing changes); "Move" asks in a styled dialog, then
 * /api/database/move starts `metarush database move` in a process of its own,
 * like the media's move. While it moves, every computer's MetaRush pauses (it
 * starts again reading only) and database.move.status shows how far it is.
 * On another computer, afterwards, the card says the workspace moved and
 * offers the switch, with that computer's own sign-in (/api/database/switch).
 */

type Where = 'here' | 'elsewhere';

/** The workspace's database and any move, polled quickly while one runs or holds. */
export function useDatabaseMoveStatus() {
  return useOp('database.move.status', {}, {
    refetchInterval: (q) => (q.state.data?.moving || q.state.data?.state === 'moving' ? 2_000 : 60_000),
    staleTime: 1_000,
    retry: 6,
  });
}

export function DatabaseMove() {
  const st = useDatabaseMoveStatus();
  const s = st.data;
  const host = useHost();
  const [open, setOpen] = useState<null | { initial: DatabaseMoveForm }>(null);
  const sectionRef = useRef<HTMLElement>(null);
  const here = host.remote ? <Theirs>{host.machine}</Theirs> : 'this computer';
  return (
    <section id="database" ref={sectionRef} aria-labelledby="database-move-h" className="panel scroll-mt-28 p-5" data-database-move>
      <h2 id="database-move-h" className="mb-1 flex flex-wrap items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <Database size={17} />
        </span>
        <span className="whitespace-nowrap">Move the database</span>
        {s?.switchMeToo && !host.remote ? (
          <Badge tone="accent" className="max-sm:hidden">
            switch this computer too
          </Badge>
        ) : null}
      </h2>
      {st.error && !s ? <ErrorPanel compact className="mt-3" error={st.error} onRetry={() => void st.refetch()} /> : null}
      {!s ? (
        st.isLoading ? (
          <div className="mt-3 flex items-center gap-2 text-[13px] text-muted">
            <Spinner size={13} /> Looking at the workspace’s database…
          </div>
        ) : null
      ) : (
        <>
          <p className="max-w-[78ch] text-[13px] leading-relaxed text-muted">
            Your projects, versions, transcripts and notes are kept in a database. Move it to another one — the database on {here}, or MongoDB Atlas or your own server — and every record is
            copied and checked before anything switches. Changes pause on every computer while it moves; reading and playing keep working. The old database is kept, never deleted. Right now{' '}
            {here} uses <Theirs>{s.this.words}</Theirs>.
          </p>
          <MoveState
            s={s}
            here={here}
            onContinue={host.remote || !s.run ? undefined : () => setOpen({ initial: formOf(s) })}
            onChanged={() => void st.refetch()}
          />
          {host.remote ? (
            <p className="mt-3 max-w-[78ch] text-[13px] leading-relaxed text-muted" data-database-elsewhere="">
              Moving the database starts on <Theirs>{host.machine}</Theirs> itself, where MetaRush runs and keeps its sign-ins.
            </p>
          ) : s.switchMeToo ? (
            s.canSwitch ? (
              <SwitchForm s={s} onDone={() => void st.refetch()} />
            ) : null
          ) : open ? (
            <MoveForm
              key={JSON.stringify(open.initial)}
              initial={open.initial}
              onClose={() => setOpen(null)}
              onStarted={() => {
                setOpen(null);
                void st.refetch();
                sectionRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' });
              }}
            />
          ) : !s.moving && s.state !== 'moving' ? (
            <div className="mt-4">
              <Button variant="secondary" icon={<ArrowsLeftRight size={15} />} onClick={() => setOpen({ initial: { to: 'elsewhere', name: '' } })} data-database-open>
                Move the database to another place
              </Button>
            </div>
          ) : null}
        </>
      )}
    </section>
  );
}

/** The form for continuing the last move started here. */
function formOf(s: DatabaseMoveStatusOut): DatabaseMoveForm {
  const t = s.run?.target;
  return { to: t?.to === 'here' ? 'here' : 'elsewhere', name: t?.name ?? '', onlyThisComputer: t?.onlyThisComputer };
}

function Progress({ s }: { s: DatabaseMoveStatusOut }) {
  const p = s.run?.progress;
  if (!p || !p.documents) return null;
  const pct = Math.max(0, Math.min(100, Math.round((p.documentsDone / p.documents) * 100)));
  return (
    <div className="mt-2" aria-hidden>
      <div className="h-1.5 w-full max-w-[420px] overflow-hidden rounded-full bg-hover">
        <div className="h-full rounded-full bg-accent transition-[width] duration-500" style={{ width: `${pct}%` }} />
      </div>
      <div className="tc mt-1 text-[11.5px] text-subtle">
        {p.phase === 'checking' ? 'Checking' : 'Copying'} {p.collectionsDone} of {p.collections} kinds of records
      </div>
    </div>
  );
}

/** What is going on: a move running or waiting, the last one's outcome, or a move to follow. */
function MoveState({ s, here, onContinue, onChanged }: { s: DatabaseMoveStatusOut; here: ReactNode; onContinue?: () => void; onChanged: () => void }) {
  const dialog = useDialog();
  const toast = useToast();
  const abandon = useOpMutation('database.abandon', { invalidate: ['database.move.status'] });
  const run = s.run;
  if (s.moving) {
    return (
      <div className="mt-4 flex items-start gap-3 rounded-[12px] bg-accent-soft px-3.5 py-3" role="status" data-database-state="moving">
        <Spinner size={15} className="mt-[2px] shrink-0 text-accent-text" />
        <div className="min-w-0 text-[13px] leading-relaxed">
          <div className="font-medium text-fg">{run?.summary || s.summary}</div>
          {run?.says ? <div className="text-muted">{run.says}</div> : null}
          <Progress s={s} />
          <div className="mt-1 text-[12px] text-muted">
            Changes wait on every computer until it’s done. When the copy is checked, {here} switches to the new database and MetaRush starts again by itself; this page reconnects.
          </div>
        </div>
      </div>
    );
  }
  const doAbandon = async () => {
    const ok = await dialog.confirm({
      title: 'Abandon the move?',
      body: (
        <div className="space-y-2">
          <p>Changes go on in the old database, on every computer.</p>
          <p>The copy made so far stays locked, so nothing uses it, and nothing is deleted. You can move again later, into a new, empty database.</p>
        </div>
      ),
      confirmLabel: 'Abandon the move',
      tone: 'danger',
    });
    if (!ok) return;
    abandon.mutate(
      {},
      {
        onSuccess: (o) => {
          toast({ tone: 'success', title: o.summary });
          onChanged();
        },
        onError: (e) => toast({ tone: 'error', title: 'The move wasn’t abandoned', body: [e.message, e.hint].filter(Boolean).join(' ') }),
      },
    );
  };
  return (
    <>
      {s.state === 'moving' ? (
        <div className="mt-4 rounded-[12px] bg-accent-soft px-3.5 py-3" data-database-state="frozen" role="status">
          <div className="text-[13.5px] font-medium text-fg">{s.summary}</div>
          {s.steps.map((x) => (
            <p key={x} className="mt-0.5 text-[12.5px] leading-relaxed text-muted">
              {x}
            </p>
          ))}
          {s.paused?.length ? <p className="mt-0.5 text-[12px] text-subtle">Paused: {s.paused.join(', ')}.</p> : null}
          <div className="mt-2.5 flex flex-wrap gap-2">
            {onContinue && run && (run.state === 'failed' || run.state === 'stopped') ? (
              <Button variant="primary" size="sm" icon={<ArrowsLeftRight size={14} />} onClick={onContinue} data-database-continue>
                Continue the move
              </Button>
            ) : null}
            {!s.running || (run && (run.state === 'failed' || run.state === 'stopped')) ? (
              <Button variant="secondary" size="sm" onClick={() => void doAbandon()} loading={abandon.isPending} data-database-abandon>
                Abandon the move
              </Button>
            ) : null}
          </div>
        </div>
      ) : null}
      {run && s.state !== 'moving' && (run.state === 'done' || run.state === 'failed' || run.state === 'stopped') ? (
        <div
          className={cx('mt-4 flex items-start gap-3 rounded-[12px] px-3.5 py-3 text-[13px] leading-relaxed', run.state === 'done' ? 'bg-ok-soft' : 'bg-danger-soft')}
          data-database-state={run.state}
        >
          {run.state === 'done' ? <CheckCircle size={16} weight="fill" className="mt-[2px] shrink-0 text-ok" /> : <WarningCircle size={16} weight="fill" className="mt-[2px] shrink-0 text-danger" />}
          <div className="min-w-0">
            <div className="font-medium text-fg">{run.state === 'done' ? 'The last move finished' : 'The last move didn’t finish'}</div>
            <div className="text-muted" data-database-summary>
              {run.summary}
            </div>
            {run.next?.length ? (
              <ul className="mt-1 list-disc space-y-0.5 pl-5 text-[12.5px] text-muted">
                {run.next.map((n) => (
                  <li key={n}>{n}</li>
                ))}
              </ul>
            ) : null}
          </div>
        </div>
      ) : null}
      {s.switchMeToo ? (
        <div className="mt-4 rounded-[12px] bg-accent-soft px-3.5 py-3" data-database-switch-me-too="">
          <div className="text-[13.5px] font-medium text-fg">{s.summary}</div>
          <ol className="mt-1 list-decimal space-y-0.5 pl-5 text-[12.5px] leading-relaxed text-muted">
            {s.steps.map((x) => (
              <li key={x}>{x}</li>
            ))}
          </ol>
          {s.command ? (
            <Details label="The command for a terminal on this computer">
              <CopyBlock text={s.command} caption="Paste this computer's own address for the new database when it asks." />
            </Details>
          ) : null}
        </div>
      ) : null}
    </>
  );
}

function Field({ label, hint, children, id }: { label: string; hint?: ReactNode; children: ReactNode; id: string }) {
  return (
    <div>
      <label htmlFor={id} className="text-[12.5px] font-medium text-muted">
        {label}
      </label>
      <div className="mt-1.5">{children}</div>
      {hint ? <div className="mt-1 text-[11.5px] leading-relaxed text-subtle">{hint}</div> : null}
    </div>
  );
}

const inputCls =
  'h-10 w-full rounded-[10px] bg-panel px-3 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0 disabled:opacity-60';

/** The address field: typed once, hidden, never shown again. */
function AddressInput({ id, value, onChange, label }: { id: string; value: string; onChange: (v: string) => void; label: string }) {
  const [show, setShow] = useState(false);
  return (
    <div className="relative">
      <input
        id={id}
        type={show ? 'text' : 'password'}
        name="metarush-database-address"
        className={cx(inputCls, 'pr-11')}
        value={value}
        autoComplete="off"
        autoCorrect="off"
        autoCapitalize="off"
        spellCheck={false}
        data-1p-ignore
        data-lpignore="true"
        maxLength={2048}
        placeholder="mongodb+srv://…"
        onChange={(e) => onChange(e.target.value)}
        data-database-address
      />
      <button
        type="button"
        onClick={() => setShow((v) => !v)}
        aria-label={show ? `Hide the ${label}` : `Show the ${label} you typed`}
        aria-pressed={show}
        className="absolute right-1 top-1 grid h-8 w-8 place-items-center rounded-[8px] text-subtle hover:bg-hover hover:text-fg focus-visible:outline-2 focus-visible:outline-accent"
      >
        {show ? <EyeSlash size={15} /> : <Eye size={15} />}
      </button>
    </div>
  );
}

const WHERE: { v: Where; label: string; hint: string }[] = [
  { v: 'elsewhere', label: 'Another MongoDB', hint: 'MongoDB Atlas or your own server: every computer can reach it' },
  { v: 'here', label: 'The database on this computer', hint: 'Set up here the way setup does it: only this computer reaches it' },
];

function MoveForm({ initial, onClose, onStarted }: { initial: DatabaseMoveForm; onClose: () => void; onStarted: () => void }) {
  const id = useId();
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const [form, setForm] = useState<DatabaseMoveForm>(initial);
  const [address, setAddress] = useState('');
  const [checking, setChecking] = useState(false);
  const [starting, setStarting] = useState(false);
  const [plan, setPlan] = useState<{ id: string; plan: DatabasePlanOut } | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const set = (patch: Partial<DatabaseMoveForm>) => {
    setForm((cur) => ({ ...cur, ...patch }));
    setPlan(null);
    setError(null);
  };

  const check = async () => {
    if (checking) return;
    setChecking(true);
    setError(null);
    setPlan(null);
    try {
      const sent: DatabaseMoveForm = { to: form.to, name: form.name?.trim() || undefined, onlyThisComputer: form.onlyThisComputer || undefined };
      if (form.to === 'elsewhere' && address.trim()) sent.address = address.trim();
      const out = await request<DatabaseMovePlanOut>('POST', '/api/database/plan', { json: { form: sent } satisfies DatabaseMovePlanIn });
      setAddress(''); // kept in this computer's keychain now; never here again
      setPlan({ id: out.moveId, plan: out.plan });
    } catch (e) {
      setError(toApiError(e));
    } finally {
      setChecking(false);
    }
  };

  const move = async () => {
    if (!plan || !plan.plan.ready) return;
    const p = plan.plan;
    const ok = await dialog.confirm({
      title: `Move the database to ${p.to.words}?`,
      body: (
        <div className="space-y-2">
          <p>{p.summary}</p>
          <p>Changes pause on every computer while it moves ({p.takes}); reading and playing keep working. This computer switches only once every record is checked.</p>
          {p.computers.some((c) => !c.this) ? <p>Your other computers keep refusing changes until each one switches too, with its own sign-in for the new database.</p> : null}
          <p>{p.kept}</p>
        </div>
      ),
      confirmLabel: 'Move the database',
    });
    if (!ok) return;
    setStarting(true);
    try {
      const out = await request<DatabaseMoveStartOut>('POST', '/api/database/move', { json: { moveId: plan.id } satisfies DatabaseMoveStartIn });
      toast({ tone: 'success', title: out.summary });
      void qc.invalidateQueries({ queryKey: ['database.move.status'] });
      onStarted();
    } catch (e) {
      setError(toApiError(e));
    } finally {
      setStarting(false);
    }
  };

  return (
    <form
      className="mt-4 space-y-4 rounded-[14px] p-4 hairline"
      autoComplete="off"
      onSubmit={(e) => {
        e.preventDefault();
        void check();
      }}
      aria-label="The new database"
      data-database-form
    >
      <div role="radiogroup" aria-label="Where the database goes" className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {WHERE.map((k) => {
          const on = form.to === k.v;
          return (
            <button
              key={k.v}
              type="button"
              role="radio"
              aria-checked={on}
              onClick={() => !on && set({ to: k.v })}
              className={cx('rounded-[12px] px-3 py-2.5 text-left transition-colors hairline', on ? 'bg-accent-soft ring-1 ring-accent-line' : 'hover:bg-hover')}
              data-database-where={k.v}
            >
              <div className={cx('text-[13px] font-medium', on ? 'text-accent-text' : 'text-fg')}>{k.label}</div>
              <div className="text-[11.5px] text-subtle">{k.hint}</div>
            </button>
          );
        })}
      </div>
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
        {form.to === 'elsewhere' ? (
          <Field
            id={`${id}-addr`}
            label="Address"
            hint="With a database user of this computer’s own (in Atlas: Connect → Drivers). Kept in this computer’s keychain; you won’t see it again, here or anywhere in MetaRush. Leave it empty to use the one you gave before."
          >
            <AddressInput id={`${id}-addr`} value={address} onChange={(v) => setAddress(v)} label="address" />
          </Field>
        ) : null}
        <Field id={`${id}-name`} label="Database name" hint="Leave it empty for metarush. The new database must be empty.">
          <input id={`${id}-name`} className={inputCls} value={form.name ?? ''} spellCheck={false} placeholder="metarush" onChange={(e) => set({ name: e.target.value })} data-database-name />
        </Field>
      </div>
      {form.to === 'here' ? (
        <label className="flex items-start gap-2 text-[13px] text-muted">
          <input type="checkbox" className="mt-[3px]" checked={!!form.onlyThisComputer} onChange={(e) => set({ onlyThisComputer: e.target.checked })} data-database-only-here />
          <span>This computer will be the only one using the workspace: your other computers couldn’t reach a database kept here.</span>
        </label>
      ) : null}

      {error ? <ErrorPanel compact error={error} /> : null}

      {plan ? <PlanView plan={plan.plan} /> : null}

      <div className="flex flex-wrap items-center justify-end gap-2">
        <Button variant="ghost" onClick={onClose} disabled={starting}>
          Cancel
        </Button>
        {plan && plan.plan.ready ? (
          <>
            <Button variant="secondary" onClick={() => void check()} loading={checking} disabled={starting}>
              Check again
            </Button>
            <Button variant="primary" icon={<ArrowsLeftRight size={15} />} onClick={() => void move()} loading={starting} data-database-start>
              Move the database
            </Button>
          </>
        ) : (
          <Button type="submit" variant="primary" loading={checking} data-database-check>
            {checking ? 'Checking the new database…' : 'Check the move'}
          </Button>
        )}
      </div>
    </form>
  );
}

function PlanView({ plan }: { plan: DatabasePlanOut }) {
  const carried = plan.collections.filter((c) => c.carried && c.count > 0);
  const stays = plan.collections.filter((c) => !c.carried && c.count > 0);
  return (
    <div className="space-y-2 rounded-[12px] bg-hover px-3.5 py-3 text-[13px] leading-relaxed" data-database-plan={plan.ready ? 'ready' : 'blocked'} aria-live="polite">
      <div className="font-medium text-fg">{plan.summary}</div>
      {plan.counts.length ? (
        <p className="text-[12.5px] text-muted" data-database-counts>
          It holds {plan.counts.map((c) => `${c.count.toLocaleString()} ${c.what}`).join(', ')}.
        </p>
      ) : null}
      {plan.problems.length ? (
        <ul className="list-disc space-y-0.5 pl-5 text-[12.5px] text-danger" data-database-problems>
          {plan.problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      ) : null}
      <ul className="list-disc space-y-0.5 pl-5 text-[12.5px] text-muted">
        {plan.frozen.map((f) => (
          <li key={f}>{f}</li>
        ))}
        {plan.notes.map((n) => (
          <li key={n}>{n}</li>
        ))}
      </ul>
      <div className="text-[12.5px]">
        <div className="font-medium text-fg">Your computers</div>
        <ul className="mt-0.5 space-y-0.5" data-database-computers>
          {plan.computers.map((c) => (
            <li key={c.name} className="text-muted">
              <Theirs>{c.name}</Theirs>
              {c.this ? ' (this computer)' : ''}: {c.needs}
            </li>
          ))}
        </ul>
      </div>
      <p className="text-[12px] text-subtle">{plan.kept}</p>
      <Details compact>
        <div className="break-all">
          From <span className="tc text-muted">{plan.from.host || plan.from.words}</span>, database <span className="tc text-muted">{plan.from.name}</span>
        </div>
        <div className="break-all">
          To <span className="tc text-muted">{plan.to.host || plan.to.words}</span>, database <span className="tc text-muted">{plan.to.name}</span>
        </div>
        <ul className="mt-1 grid grid-cols-1 gap-x-6 gap-y-0.5 sm:grid-cols-2">
          {carried.map((c) => (
            <li key={c.name} className="flex justify-between gap-3 text-[12px]">
              <span className="tc truncate">{c.name}</span>
              <span className="tc shrink-0 text-subtle">
                {c.count.toLocaleString()} · {bytes(c.bytes)}
              </span>
            </li>
          ))}
        </ul>
        {stays.length ? <div className="mt-1 text-[12px]">Stays behind: {stays.map((c) => `${c.name} (${c.why})`).join('; ')}</div> : null}
      </Details>
    </div>
  );
}

/** Switching this computer after another one moved the workspace, with its own sign-in. */
function SwitchForm({ s, onDone }: { s: DatabaseMoveStatusOut; onDone: () => void }) {
  const id = useId();
  const toast = useToast();
  const qc = useQueryClient();
  const [address, setAddress] = useState('');
  const [name, setName] = useState(s.toName ?? '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const here = !!s.switchHere; // the database on this very computer: no address
  const go = async () => {
    if ((!here && !address.trim()) || busy) return;
    setBusy(true);
    setError(null);
    try {
      const out = await request<DatabaseSwitchOut>('POST', '/api/database/switch', { json: { address: here ? undefined : address.trim(), name: name.trim() || undefined } satisfies DatabaseSwitchFormIn });
      setAddress('');
      toast({ tone: 'success', title: out.summary });
      void qc.invalidateQueries();
      onDone();
    } catch (e) {
      setError(toApiError(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      className="mt-4 space-y-3 rounded-[14px] p-4 hairline"
      autoComplete="off"
      onSubmit={(e) => {
        e.preventDefault();
        void go();
      }}
      aria-label="Switch this computer to the new database"
      data-database-switch-form
    >
      {here ? null : (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          <Field id={`${id}-addr`} label="This computer’s address for the new database" hint="With this computer’s own database user. Kept in its keychain; you won’t see it again.">
            <AddressInput id={`${id}-addr`} value={address} onChange={setAddress} label="address" />
          </Field>
          <Field id={`${id}-name`} label="Database name">
            <input id={`${id}-name`} className={inputCls} value={name} spellCheck={false} onChange={(e) => setName(e.target.value)} />
          </Field>
        </div>
      )}
      {error ? <ErrorPanel compact error={error} /> : null}
      <div className="flex justify-end">
        <Button type="submit" variant="primary" icon={<ArrowsLeftRight size={15} />} loading={busy} disabled={!here && !address.trim()} data-database-switch>
          Switch this computer
        </Button>
      </div>
    </form>
  );
}
