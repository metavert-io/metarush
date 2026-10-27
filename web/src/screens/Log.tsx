import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, useSearchParams } from 'react-router';
import {
  ArrowRight,
  ChartBar,
  ChatCircleText,
  ClockCounterClockwise,
  Cloud,
  DownloadSimple,
  HandPointing,
  Lightning,
  ListMagnifyingGlass,
  MagnifyingGlass,
  PencilSimpleLine,
  Queue,
  Trash,
  Warning,
  WarningCircle,
  Wrench,
  X,
} from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import type { LogListIn, LogRow, LogSettingsOut, LogShowOut } from '../api/types';
import { resetSelection } from '../lib/workbench';
import { Markdown } from '../layout/assistant/Markdown';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, CopyButton, cx, Details, EmptyState, ErrorPanel, Freshness, Skeleton, Spinner, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';

type View = 'all' | 'problems' | 'claude' | 'jobs' | 'programs';
const VIEWS: { value: View; label: string }[] = [
  { value: 'all', label: 'Everything' },
  { value: 'problems', label: 'Problems' },
  { value: 'claude', label: 'Claude' },
  { value: 'jobs', label: 'Jobs' },
  { value: 'programs', label: 'Programs & services' },
];

type Range = 'hour' | 'day' | 'week' | 'month' | 'all';
const RANGES: { value: Range; label: string; since: string }[] = [
  { value: 'hour', label: 'Last hour', since: '1h' },
  { value: 'day', label: 'Last 24 hours', since: '1d' },
  { value: 'week', label: 'Last 7 days', since: '7d' },
  { value: 'month', label: 'Last 30 days', since: '30d' },
  { value: 'all', label: 'All time', since: '3650d' },
];

/** Object filters a link can carry (?job=…, ?render=…), with how the pill names them. */
const SCOPES: { key: 'job' | 'render' | 'asset' | 'project' | 'conversation'; label: string }[] = [
  { key: 'job', label: 'Only this job' },
  { key: 'render', label: 'Only this render' },
  { key: 'asset', label: 'Only this clip' },
  { key: 'project', label: 'Only this project' },
  { key: 'conversation', label: 'Only this conversation' },
];

function dayLabel(iso: string): string {
  const d = new Date(iso);
  const today = new Date();
  const yesterday = new Date(Date.now() - 86_400_000);
  const same = (a: Date, b: Date) => a.toDateString() === b.toDateString();
  if (same(d, today)) return 'Today';
  if (same(d, yesterday)) return 'Yesterday';
  return d.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short', year: d.getFullYear() === today.getFullYear() ? undefined : 'numeric' });
}

function clock(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
}

/** What kind of entry it is, at a glance. Problems use the problem icon. */
function EntryIcon({ row, size = 16 }: { row: LogRow; size?: number }) {
  if (row.level === 'error') return <WarningCircle size={size} weight="fill" className="text-danger" aria-label="Problem" />;
  if (row.level === 'warn') return <Warning size={size} weight="fill" className="text-accent-text" aria-label="Warning" />;
  const cls = 'text-subtle';
  switch (row.kind) {
    case 'claude':
      return <ChatCircleText size={size} className="text-accent-text" aria-hidden />;
    case 'action':
      return <HandPointing size={size} className="text-accent-text" aria-hidden />;
    case 'job':
      return <Queue size={size} className={cls} aria-hidden />;
    case 'tool':
      return <Wrench size={size} className={cls} aria-hidden />;
    case 'provider':
      return <Cloud size={size} className={cls} aria-hidden />;
    case 'op_summary':
      return <ChartBar size={size} className={cls} aria-hidden />;
    case 'op':
      return row.op?.mutating ? <PencilSimpleLine size={size} className="text-fg" aria-hidden /> : <ListMagnifyingGlass size={size} className={cls} aria-hidden />;
  }
  return <Lightning size={size} className={cls} aria-hidden />;
}

function who(row: LogRow): string {
  return row.machine ? `${row.channelLabel} on ${row.machine}` : row.channelLabel;
}

/** Who did it (the person, D183) and where: "Maya, dashboard on Studio Mac". */
function Who({ row }: { row: LogRow }) {
  return (
    <>
      {row.userName ? (
        <>
          <span className="text-muted" data-entry-person={row.userId ?? ''}>
            <Theirs>{row.userName}</Theirs>
          </span>
          {', '}
        </>
      ) : null}
      {who(row)}
    </>
  );
}

/** The activity log: everything MetaRush did, newest first, in plain words (Settings → Log, D184, which gives it its heading). */
export function LogPage() {
  const [params, setParams] = useSearchParams();
  const view = (params.get('view') as View | null) ?? 'all';
  const range = (params.get('range') as Range | null) ?? 'week';
  const q = params.get('q') ?? '';
  const entry = params.get('entry') ?? undefined;
  const ids = params.get('ids') ?? undefined;
  // By person (D183): the log lists entries of everyone; this narrows what is shown.
  const person = params.get('person') ?? '';
  const scopes = SCOPES.filter((s) => params.get(s.key));
  const [live, setLive] = useState(true);
  const [draft, setDraft] = useState(q);
  const [open, setOpen] = useState<string | null>(entry ?? null);
  useEffect(() => resetSelection({}), []);
  useEffect(() => setDraft(q), [q]);

  const set = (k: string, v: string | undefined) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v);
    else next.delete(k);
    setParams(next, { replace: true });
  };
  // Typing searches after a short pause.
  useEffect(() => {
    if (draft === q) return;
    const t = setTimeout(() => set('q', draft.trim() || undefined), 350);
    return () => clearTimeout(t);
  }, [draft]); // eslint-disable-line react-hooks/exhaustive-deps

  const input: LogListIn = useMemo(() => {
    const scoped = scopes.length > 0 || !!ids;
    const i: LogListIn = { limit: 100, since: scoped && !params.get('range') ? '3650d' : RANGES.find((r) => r.value === range)?.since };
    if (view === 'problems') i.problems = true;
    if (view === 'claude') i.kind = ['claude', 'action'];
    if (view === 'jobs') i.kind = ['job'];
    if (view === 'programs') i.kind = ['tool', 'provider'];
    if (q) i.query = q;
    if (ids) i.ids = ids.split(',').filter(Boolean);
    for (const s of scopes) i[s.key] = params.get(s.key) ?? undefined;
    return i;
  }, [view, range, q, ids, params]); // eslint-disable-line react-hooks/exhaustive-deps
  const key = JSON.stringify(input);

  const first = useOp('log.list', input, { refetchInterval: live ? 5000 : false, placeholderData: (prev) => prev });
  const projects = useOp('project.list', {}, { staleTime: 60_000 });
  const [older, setOlder] = useState<LogRow[]>([]);
  const [olderNext, setOlderNext] = useState<string | undefined>();
  const [loadingOlder, setLoadingOlder] = useState(false);
  useEffect(() => {
    setOlder([]);
    setOlderNext(undefined);
  }, [key]);
  const nextCursor = older.length ? olderNext : first.data?.next;
  const toast = useToast();
  const loadOlder = async () => {
    if (!nextCursor) return;
    setLoadingOlder(true);
    try {
      const out = await call('log.list', { ...input, before: nextCursor });
      setOlder((o) => [...o, ...out.entries]);
      setOlderNext(out.next ?? '');
    } catch (e) {
      toast({ tone: 'error', title: 'Could not load older entries', body: toApiError(e).message });
    } finally {
      setLoadingOlder(false);
    }
  };
  const users = useOp('users.list', {}, { staleTime: 60_000 });
  const allRows = useMemo(() => {
    const seen = new Set<string>();
    return [...(first.data?.entries ?? []), ...older].filter((r) => (seen.has(r.id) ? false : (seen.add(r.id), true)));
  }, [first.data, older]);
  const rows = useMemo(() => (person ? allRows.filter((r) => r.userId === person) : allRows), [allRows, person]);
  // Everyone the filter offers: the workspace's people, and anyone else the entries name.
  const people = useMemo(() => {
    const m = new Map<string, string>();
    for (const u of users.data?.people ?? []) if (u.state !== 'earlier' || u.work > 0) m.set(u.id, u.name || u.email || 'Someone');
    for (const r of allRows) if (r.userId && r.userName && !m.has(r.userId)) m.set(r.userId, r.userName);
    if (person && !m.has(person)) m.set(person, 'Someone');
    return [...m.entries()].sort((a, b) => a[1].localeCompare(b[1]));
  }, [users.data, allRows, person]);
  const filtered = view !== 'all' || !!q || scopes.length > 0 || !!ids || !!person;

  return (
    <div data-log-page>
      <div className="mb-5 flex flex-wrap items-center gap-3">
        <LogTabs current="log" />
      </div>

      <div className="mb-4 flex flex-wrap items-center gap-2">
        <label className="relative flex min-w-[220px] flex-1 items-center">
          <span className="sr-only">Search the log</span>
          <MagnifyingGlass size={15} className="pointer-events-none absolute left-3 text-subtle" aria-hidden />
          <input
            type="search"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') set('q', draft.trim() || undefined);
              if (e.key === 'Escape') setDraft('');
            }}
            placeholder="Search what happened, errors and conversations"
            className="h-9 w-full rounded-[10px] bg-panel pl-9 pr-3 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-offset-0 focus:outline-accent"
          />
        </label>
        <label className="flex items-center gap-2 text-[12.5px] text-muted">
          <span className="max-sm:sr-only">When</span>
          <select value={range} onChange={(e) => set('range', e.target.value === 'week' ? undefined : e.target.value)} className="h-9 rounded-[10px] bg-panel px-2 text-[12.5px] text-fg hairline">
            {RANGES.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </select>
        </label>
        <label className="flex items-center gap-2 text-[12.5px] text-muted">
          <span className="max-sm:sr-only">Project</span>
          <select
            value={params.get('project') ?? ''}
            onChange={(e) => set('project', e.target.value || undefined)}
            className="h-9 max-w-[200px] rounded-[10px] bg-panel px-2 text-[12.5px] text-fg hairline"
          >
            <option value="">All projects</option>
            {(projects.data?.projects ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </label>
        {people.length > 1 || person ? (
          <label className="flex items-center gap-2 text-[12.5px] text-muted">
            <span className="max-sm:sr-only">Person</span>
            <select value={person} onChange={(e) => set('person', e.target.value || undefined)} className="h-9 max-w-[180px] rounded-[10px] bg-panel px-2 text-[12.5px] text-fg hairline" data-log-person>
              <option value="">Everyone</option>
              {people.map(([id, name]) => (
                <option key={id} value={id}>
                  {name}
                </option>
              ))}
            </select>
          </label>
        ) : null}
      </div>

      <div className="mb-5 flex flex-wrap items-center gap-2">
        <div className="no-scrollbar flex gap-1 overflow-x-auto" role="group" aria-label="What to show">
          {VIEWS.map((v) => (
            <button
              key={v.value}
              type="button"
              aria-pressed={view === v.value}
              onClick={() => set('view', v.value === 'all' ? undefined : v.value)}
              className={cx('inline-flex h-8 shrink-0 items-center rounded-full px-3 text-[12.5px] transition-colors hairline', view === v.value ? 'bg-panel text-fg' : 'text-muted hover:text-fg')}
            >
              {v.label}
            </button>
          ))}
        </div>
        {scopes.map((s) => (
          <button key={s.key} type="button" onClick={() => set(s.key, undefined)} className="inline-flex h-8 items-center gap-1.5 rounded-full bg-accent-soft px-3 text-[12.5px] text-accent-text">
            {s.key === 'project' ? projects.data?.projects.find((p) => p.id === params.get('project'))?.name ?? s.label : s.label} <X size={12} aria-label="Show everything" />
          </button>
        ))}
        {ids ? (
          <button type="button" onClick={() => set('ids', undefined)} className="inline-flex h-8 items-center gap-1.5 rounded-full bg-accent-soft px-3 text-[12.5px] text-accent-text">
            Only the entries an idea points to <X size={12} aria-label="Show everything" />
          </button>
        ) : null}
        <div className="ml-auto flex items-center gap-3">
          <label className="inline-flex items-center gap-1.5 text-[12px] text-muted" title="Check for new entries every few seconds">
            <input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} className="h-3.5 w-3.5 accent-[var(--accent)]" />
            Keep up to date
          </label>
          <Freshness at={first.dataUpdatedAt || undefined} fetching={first.isFetching} onRefresh={() => void first.refetch()} />
        </div>
      </div>

      {entry && !rows.some((r) => r.id === entry) ? <PinnedEntry id={entry} onClose={() => set('entry', undefined)} /> : null}

      {first.isLoading ? (
        <div className="space-y-2" aria-label="Loading the log">
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <Skeleton key={i} className="h-[52px] rounded-[12px]" />
          ))}
        </div>
      ) : first.error ? (
        <ErrorPanel error={first.error} onRetry={() => void first.refetch()} />
      ) : rows.length === 0 ? (
        <div className="panel">
          <EmptyState
            icon={<ClockCounterClockwise size={22} />}
            title={filtered ? 'Nothing matches' : 'Nothing in the log yet'}
            body={
              person && allRows.length > 0
                ? `Nothing ${people.find(([id]) => id === person)?.[1] ?? 'this person'} did is in these entries. Show older entries, or try a longer time range.`
                : filtered
                  ? 'Try a longer time range, another view, or different words.'
                  : 'As you import footage, make cuts, render and talk to Claude, every step shows up here, from every machine in the workspace.'
            }
          />
        </div>
      ) : (
        <LogList rows={rows} open={open} onToggle={(id) => setOpen((o) => (o === id ? null : id))} />
      )}

      {(rows.length > 0 || (person && allRows.length > 0)) && nextCursor ? (
        <div className="mt-4 flex justify-center">
          <Button variant="secondary" loading={loadingOlder} onClick={() => void loadOlder()}>
            Show older entries
          </Button>
        </div>
      ) : null}

      <LogFooter input={input} count={rows.length} person={person} />
    </div>
  );
}

/** Log and Improvements are two views of the same thing. */
export function LogTabs({ current }: { current: 'log' | 'improvements' }) {
  const item = (to: string, label: string, on: boolean) => (
    <Link
      to={to}
      aria-current={on ? 'page' : undefined}
      className={cx('inline-flex h-8 items-center rounded-[9px] px-3 text-[13px] font-medium transition-colors', on ? 'bg-panel text-fg hairline' : 'text-muted hover:bg-hover hover:text-fg')}
    >
      {label}
    </Link>
  );
  return (
    <nav aria-label="Log sections" className="flex items-center gap-1">
      {item('/settings/log', 'Log', current === 'log')}
      {item('/improvements', 'Improvements', current === 'improvements')}
    </nav>
  );
}

function LogList({ rows, open, onToggle }: { rows: LogRow[]; open: string | null; onToggle: (id: string) => void }) {
  const groups: { day: string; rows: LogRow[] }[] = [];
  for (const r of rows) {
    const day = dayLabel(r.at);
    const g = groups[groups.length - 1];
    if (g && g.day === day) g.rows.push(r);
    else groups.push({ day, rows: [r] });
  }
  return (
    <div className="space-y-6">
      {groups.map((g) => (
        <section key={g.day} aria-label={g.day}>
          <h2 className="mb-2 px-1 text-[12.5px] font-semibold text-muted">{g.day}</h2>
          <ul className="overflow-hidden rounded-[16px] hairline">
            {g.rows.map((r, i) => (
              <EntryRow key={r.id} row={r} open={open === r.id} onToggle={() => onToggle(r.id)} first={i === 0} />
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}

function EntryRow({ row, open, onToggle, first }: { row: LogRow; open: boolean; onToggle: () => void; first: boolean }) {
  const ref = useRef<HTMLLIElement>(null);
  useEffect(() => {
    if (open) ref.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }, [open]);
  return (
    <li ref={ref} data-entry={row.id} className={cx(!first && 'border-t border-line', open ? 'bg-raised' : 'bg-bg')}>
      <button
        type="button"
        aria-expanded={open}
        onClick={onToggle}
        className="grid w-full grid-cols-[56px_18px_minmax(0,1fr)] items-start gap-x-3 px-4 py-2.5 text-left outline-none transition-colors hover:bg-raised focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent sm:grid-cols-[64px_18px_minmax(0,1fr)_auto]"
      >
        <time dateTime={row.at} className="tc whitespace-nowrap pt-[2px] text-[12px] text-subtle" title={new Date(row.at).toLocaleString()}>
          {clock(row.at)}
        </time>
        <span className="pt-[2px]">
          <EntryIcon row={row} />
        </span>
        <span className="min-w-0">
          <span className={cx('block break-words text-[13.5px] leading-snug', row.level === 'error' ? 'font-medium text-fg' : 'text-fg')}>{row.text}</span>
          <span className="mt-0.5 block truncate text-[12px] text-subtle">
            <Who row={row} />
            {row.duration ? <span className="sm:hidden">, {row.duration}</span> : null}
          </span>
          {row.error?.advice && !open ? <span className="mt-1 block text-[12.5px] leading-relaxed text-muted">{row.error.advice}</span> : null}
        </span>
        <span className="tc hidden pt-[2px] text-right text-[12px] text-subtle sm:block">{row.duration}</span>
      </button>
      {open ? <EntryDetail id={row.id} row={row} /> : null}
    </li>
  );
}

/** An entry a link points to that is not on the current page. */
function PinnedEntry({ id, onClose }: { id: string; onClose: () => void }) {
  const s = useOp('log.show', { id });
  return (
    <div className="mb-5 overflow-hidden rounded-[16px] ring-1 ring-accent-line">
      <div className="flex items-center gap-2 bg-raised px-4 py-2 text-[12.5px] text-muted">
        <span className="flex-1">The entry you opened</span>
        <button type="button" onClick={onClose} className="rounded-[7px] p-1 text-subtle hover:bg-hover hover:text-fg" aria-label="Close">
          <X size={13} />
        </button>
      </div>
      {s.isLoading ? (
        <div className="p-4">
          <Skeleton className="h-16" />
        </div>
      ) : s.error ? (
        <div className="p-4">
          <ErrorPanel error={s.error} compact />
        </div>
      ) : s.data ? (
        <ul>
          <EntryRow row={s.data.entry} open onToggle={() => undefined} first />
        </ul>
      ) : null}
    </div>
  );
}

function Block({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="mt-4">
      <h3 className="mb-1.5 text-[12px] font-semibold text-muted">{title}</h3>
      {children}
    </div>
  );
}

/** The whole entry: full prompt and reply, error output, what it involved. */
function EntryDetail({ id, row }: { id: string; row: LogRow }) {
  const s = useOp('log.show', { id }, { staleTime: 60_000 });
  const d: LogShowOut | undefined = s.data;
  const c = row.claude;
  return (
    <div className="border-t border-line px-4 pb-4 pt-1 sm:pl-[102px]">
      {s.isLoading ? (
        <div className="space-y-2 pt-3">
          <Skeleton className="h-4 w-2/3" />
          <Skeleton className="h-4 w-1/2" />
        </div>
      ) : s.error ? (
        <div className="pt-3">
          <ErrorPanel error={s.error} compact onRetry={() => void s.refetch()} />
        </div>
      ) : d ? (
        <>
          {row.error ? (
            <div className="mt-3 rounded-[12px] bg-danger-soft px-4 py-3">
              <div className="text-[13px] font-medium text-fg">What went wrong</div>
              <div className="mt-1 break-words text-[12.5px] leading-relaxed text-muted">{row.error.message}</div>
              {row.error.advice ? <div className="mt-1.5 text-[12.5px] leading-relaxed text-fg">{row.error.advice}</div> : null}
            </div>
          ) : null}
          {d.prompt ? (
            <Block title={c?.source === 'skill-eval' ? 'The test asked' : 'Asked'}>
              <div className="whitespace-pre-wrap break-words rounded-[12px] bg-panel px-4 py-3 text-[13.5px] leading-relaxed text-fg hairline">{d.prompt}</div>
            </Block>
          ) : null}
          {d.response ? (
            <Block title="Claude answered">
              <div className="rounded-[12px] bg-panel px-4 py-3 text-[13.5px] leading-relaxed hairline">
                <Markdown text={d.response} />
              </div>
            </Block>
          ) : null}
          {c && c.tools && c.tools.length > 0 ? (
            <Block title={`What Claude did (${c.toolCount} ${c.toolCount === 1 ? 'step' : 'steps'})`}>
              <ul className="space-y-1">
                {c.tools.map((t, i) => (
                  <li key={i} className="flex flex-wrap items-baseline gap-x-2 text-[12.5px]">
                    <span className={cx('shrink-0', t.denied || t.isError ? 'text-danger' : 'text-subtle')}>{t.denied ? 'Not allowed' : t.isError ? 'Failed' : t.name}</span>
                    <span className="tc min-w-0 flex-1 basis-[12rem] break-words text-muted">{t.summary}</span>
                    {t.result && !t.denied ? <span className="min-w-0 max-w-full break-words text-subtle">{t.result}</span> : null}
                  </li>
                ))}
              </ul>
            </Block>
          ) : null}
          {c && c.actions && c.actions.length > 0 ? (
            <Block title="Changes Claude asked for">
              <ul className="space-y-1.5">
                {c.actions.map((a) => (
                  <li key={a.id} className="flex flex-wrap items-center gap-2 text-[13px]">
                    <span className="text-fg">{a.title}</span>
                    <Badge tone={a.decision === 'applied' ? 'ok' : a.decision === 'failed' ? 'danger' : a.decision === 'proposed' ? 'info' : 'neutral'}>
                      {{ proposed: 'waiting', applied: 'done', dismissed: 'not now', failed: 'failed', unavailable: 'not possible here' }[a.decision]}
                    </Badge>
                  </li>
                ))}
              </ul>
            </Block>
          ) : null}
          {d.stderr ? (
            <Block title="Error output (the end)">
              <pre className="tc max-h-[40dvh] overflow-auto whitespace-pre-wrap break-words rounded-[12px] bg-inset px-4 py-3 text-[11.5px] leading-relaxed text-[#efe8de]">{d.stderr}</pre>
            </Block>
          ) : null}
          {d.note ? <p className="mt-3 text-[12.5px] text-muted">{d.note}</p> : null}
          {d.parent ? (
            <Block title="Part of">
              <CompactRow row={d.parent} />
            </Block>
          ) : null}
          {d.children.length > 0 ? (
            <Block title="What it involved">
              <ul className="space-y-1">
                {d.children.map((k) => (
                  <li key={k.id}>
                    <CompactRow row={k} />
                  </li>
                ))}
              </ul>
            </Block>
          ) : null}
          {d.related.length > 0 ? (
            <Block title={row.refs.conversationId ? 'Also in this conversation' : 'Also in this job'}>
              <ul className="space-y-1">
                {d.related.slice(0, 12).map((k) => (
                  <li key={k.id}>
                    <CompactRow row={k} withDay />
                  </li>
                ))}
              </ul>
            </Block>
          ) : null}
          <div className="mt-4 flex flex-wrap items-center gap-2">
            {row.target ? (
              <Link to={row.target.link} className="inline-flex h-8 items-center gap-1.5 rounded-[9px] bg-accent px-3 text-[12.5px] font-medium text-accent-ink hover:bg-accent-hover">
                {row.target.label} <ArrowRight size={12} />
              </Link>
            ) : null}
            <details className="group ml-auto min-w-0 max-w-full">
              <summary className="cursor-pointer list-none text-right text-[12px] font-medium text-muted hover:text-fg">Details</summary>
              <div className="mt-2 w-[min(640px,calc(100vw-4rem))] max-w-full">
                <div className="mb-2 flex justify-end">
                  <CopyButton text={JSON.stringify(d.entry, null, 2)} label="Copy entry" />
                </div>
                <pre className="tc max-h-[40dvh] overflow-auto whitespace-pre-wrap break-words rounded-[12px] bg-inset px-4 py-3 text-[11px] leading-relaxed text-[#efe8de]">
                  {JSON.stringify(d.entry, null, 2)}
                </pre>
              </div>
            </details>
          </div>
        </>
      ) : null}
    </div>
  );
}

function CompactRow({ row, withDay }: { row: LogRow; withDay?: boolean }) {
  return (
    <div className="flex items-start gap-2 text-[12.5px]">
      <span className="tc w-[60px] shrink-0 whitespace-nowrap pt-[1px] text-subtle">{withDay ? new Date(row.at).toLocaleDateString(undefined, { day: 'numeric', month: 'short' }) : clock(row.at)}</span>
      <span className="pt-[1px]">
        <EntryIcon row={row} size={13} />
      </span>
      <span className="min-w-0 flex-1 break-words text-muted">{row.text}</span>
      {row.duration ? <span className="tc shrink-0 text-subtle">{row.duration}</span> : null}
    </div>
  );
}

/** Save the entries shown as a file, and remove old ones (people only). */
function LogFooter({ input, count, person }: { input: LogListIn; count: number; person?: string }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const download = async () => {
    setBusy(true);
    try {
      const lines: string[] = [];
      let before: string | undefined;
      for (let page = 0; page < 10; page++) {
        const out = await call('log.export', { ...input, limit: 1000, before, bodies: true });
        for (const e of out.entries) if (!person || e.userId === person) lines.push(JSON.stringify(e));
        if (!out.next) break;
        before = out.next;
      }
      const blob = new Blob([lines.join('\n') + '\n'], { type: 'application/x-ndjson' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `metarush-log-${new Date().toISOString().slice(0, 10)}.jsonl`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 5000);
      toast({ tone: 'success', title: `Saved ${lines.length} ${lines.length === 1 ? 'entry' : 'entries'}` });
    } catch (e) {
      toast({ tone: 'error', title: 'Could not save the log', body: toApiError(e).message });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mt-8 flex flex-wrap items-center gap-2 border-t border-line pt-4">
      <Button variant="ghost" size="sm" icon={<DownloadSimple size={13} />} loading={busy} disabled={count === 0} onClick={() => void download()}>
        Save as a file
      </Button>
      <Details compact label="Making a bug report?" className="px-2 text-[12px]">
        <p className="max-w-[60ch]">
          Ask Claude Code to make a MetaRush log bundle: one file with the recent log, failed jobs, the setup check and versions, with passwords and keys removed. In a terminal:{' '}
          <code className="tc rounded-[5px] bg-hover px-1 text-fg">metarush log bundle</code>
        </p>
      </Details>
      <Link to="/settings/log#log" className="ml-auto text-[12px] font-medium text-accent-text hover:underline">
        What the log keeps
      </Link>
    </div>
  );
}

const PRIVACY: { value: 'workspace' | 'local' | 'off'; label: string; help: string }[] = [
  { value: 'workspace', label: 'With the workspace', help: 'Anyone who can open this workspace can read what you asked Claude.' },
  { value: 'local', label: 'On this machine only', help: 'Other machines see that a conversation happened, not the words.' },
  { value: 'off', label: 'Not kept', help: 'The log says that a conversation happened, without the words.' },
];
const RETENTION = [
  { v: 0, label: 'Until I delete them' },
  { v: 30, label: '30 days' },
  { v: 90, label: '90 days' },
  { v: 365, label: 'A year' },
];

/** Settings: where the words of Claude conversations are kept, and for how long entries stay. */
export function LogSettingsCard() {
  const s = useOp('log.settings', {}, { staleTime: 30_000 });
  const qc = useQueryClient();
  const toast = useToast();
  const dialog = useDialog();
  const [saving, setSaving] = useState<string | null>(null);
  const d: LogSettingsOut | undefined = s.data;
  const save = async (what: string, input: { prompts?: 'workspace' | 'local' | 'off'; retentionDays?: number }) => {
    setSaving(what);
    try {
      await call('log.configure', input);
      void qc.invalidateQueries({ queryKey: ['log.settings'] });
      toast({ tone: 'success', title: 'Saved' });
    } catch (e) {
      toast({ tone: 'error', title: 'Could not change it', body: toApiError(e).message });
    } finally {
      setSaving(null);
    }
  };
  const purge = async () => {
    const choice = await dialog.open<{ days: number; mine: boolean }>((close) => <PurgeForm onDone={close} />, { title: 'Delete old log entries', size: 'sm' });
    if (!choice) return;
    const before = new Date(Date.now() - choice.days * 86_400_000).toISOString();
    try {
      const out = await call('log.purge', { before, thisMachine: choice.mine });
      void qc.invalidateQueries({ queryKey: ['log.list'] });
      toast({ tone: 'success', title: `Deleted ${out.deleted} ${out.deleted === 1 ? 'entry' : 'entries'}` });
    } catch (e) {
      toast({ tone: 'error', title: 'Could not delete them', body: toApiError(e).message });
    }
  };
  return (
    <section id="log" className="panel scroll-mt-28 p-5" aria-labelledby="log-h">
      <h2 id="log-h" className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <ClockCounterClockwise size={17} />
        </span>
        What the log keeps
      </h2>
      <p className="mb-4 max-w-[70ch] text-[12.5px] leading-relaxed text-muted">
        MetaRush keeps a log of everything it does, so you can see what happened and report problems. Passwords and keys are removed before anything is saved.
      </p>
      {s.isLoading ? (
        <Skeleton className="h-24" />
      ) : s.error ? (
        <ErrorPanel error={s.error} compact onRetry={() => void s.refetch()} />
      ) : d ? (
        <div className="space-y-4">
          <fieldset>
            <legend className="mb-1.5 text-[12.5px] font-medium text-fg">What you ask Claude and its answers are kept</legend>
            <div className="flex flex-wrap gap-1" role="radiogroup">
              {PRIVACY.map((p) => (
                <button
                  key={p.value}
                  type="button"
                  role="radio"
                  aria-checked={d.prompts === p.value}
                  disabled={!!saving}
                  onClick={() => d.prompts !== p.value && void save('prompts', { prompts: p.value })}
                  className={cx('inline-flex h-8 items-center gap-1.5 rounded-full px-3 text-[12.5px] transition-colors hairline', d.prompts === p.value ? 'bg-panel text-fg' : 'text-muted hover:text-fg')}
                >
                  {p.label}
                </button>
              ))}
              {saving === 'prompts' ? <Spinner size={12} className="ml-1 self-center text-subtle" /> : null}
            </div>
            <p className="mt-1.5 text-[12px] text-subtle">{PRIVACY.find((p) => p.value === d.prompts)?.help}</p>
            {d.problem ? (
              <p className="mt-1.5 flex items-start gap-1.5 text-[12px] leading-relaxed text-fg">
                <Warning size={13} weight="fill" className="mt-[2px] shrink-0 text-accent-text" aria-hidden />
                <span>{d.problem.charAt(0).toUpperCase() + d.problem.slice(1)}. To keep them, move MetaRush's folder on this computer out of synced folders and code repositories.</span>
              </p>
            ) : null}
          </fieldset>
          <fieldset>
            <legend className="mb-1.5 text-[12.5px] font-medium text-fg">Entries from this machine stay</legend>
            <div className="flex flex-wrap gap-1" role="radiogroup">
              {RETENTION.map((r) => (
                <button
                  key={r.v}
                  type="button"
                  role="radio"
                  aria-checked={d.retentionDays === r.v}
                  disabled={!!saving}
                  onClick={() => d.retentionDays !== r.v && void save('retention', { retentionDays: r.v })}
                  className={cx('inline-flex h-8 items-center rounded-full px-3 text-[12.5px] transition-colors hairline', d.retentionDays === r.v ? 'bg-panel text-fg' : 'text-muted hover:text-fg')}
                >
                  {r.label}
                </button>
              ))}
            </div>
          </fieldset>
          <p className="text-[12.5px] leading-relaxed text-muted">
            {d.hooks.enabled && !d.hooks.otherHome
              ? 'Claude Code sessions in a terminal that use MetaRush are recorded too.'
              : 'Claude Code sessions in a terminal are not recorded. You can turn that on from a terminal.'}
            {d.spool.entries > 0 ? ` ${d.spool.entries} entries are waiting to be saved because the database could not be reached.` : ''}
          </p>
          <div className="flex flex-wrap items-center gap-2 border-t border-line pt-4">
            <Link to="/improvements" className="inline-flex h-8 items-center gap-1.5 rounded-[9px] px-3 text-[12.5px] font-medium text-fg hairline hover:bg-hover">
              Ideas from the log
            </Link>
            <Button variant="ghost" size="sm" className="ml-auto" icon={<Trash size={13} />} onClick={() => void purge()}>
              Delete old entries
            </Button>
          </div>
          <Details compact className="text-[12px]">
            <div className="space-y-1">
              <p>
                log.prompts: <span className="tc text-fg">{d.prompts}</span>, log.retentionDays: <span className="tc text-fg">{d.retentionDays}</span>
              </p>
              <p>
                Record terminal sessions: <code className="tc rounded-[5px] bg-hover px-1 text-fg">metarush log claude-hooks enable</code> (status:{' '}
                <span className="tc text-fg">{d.hooks.enabled ? 'on' : 'off'}</span>)
              </p>
            </div>
          </Details>
        </div>
      ) : null}
    </section>
  );
}

function PurgeForm({ onDone }: { onDone: (v?: { days: number; mine: boolean }) => void }) {
  const [days, setDays] = useState(90);
  const [mine, setMine] = useState(false);
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        onDone({ days, mine });
      }}
    >
      <p className="text-[13px] leading-relaxed text-muted">This removes log entries for good, with the texts only they used. Your footage, cuts and renders are not touched.</p>
      <label className="mt-4 block text-[12.5px] font-medium text-fg" htmlFor="purge-age">
        Delete entries older than
      </label>
      <select id="purge-age" value={days} onChange={(e) => setDays(Number(e.target.value))} className="mt-1.5 h-9 w-full rounded-[10px] bg-panel px-2 text-[13px] text-fg hairline">
        <option value={7}>7 days</option>
        <option value={30}>30 days</option>
        <option value={90}>90 days</option>
        <option value={365}>A year</option>
      </select>
      <label className="mt-3 flex items-center gap-2 text-[12.5px] text-muted">
        <input type="checkbox" checked={mine} onChange={(e) => setMine(e.target.checked)} className="h-4 w-4 accent-[var(--accent)]" />
        Only this machine's entries
      </label>
      <p className="mt-2 text-[12px] text-subtle">{mine ? 'Entries from other machines stay.' : 'This includes entries from every machine in the workspace.'}</p>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="ghost" onClick={() => onDone(undefined)}>
          Cancel
        </Button>
        <Button type="submit" variant="danger" icon={<Trash size={13} />}>
          Delete
        </Button>
      </div>
    </form>
  );
}
