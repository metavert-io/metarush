import { useEffect, useState } from 'react';
import { Archive, ArrowCounterClockwise, Desktop, MagnifyingGlass } from '@phosphor-icons/react';
import { useAssistantOp, useConversations } from '../../api/assistant';
import type { Conversation } from '../../api/types';
import { relative } from '../../lib/format';
import { ErrorPanel, Skeleton, Spinner, Theirs, cx } from '../../ui/primitives';
import { othersWaiting, startedBy, useViewerId } from './people';
import { setWhose, useWhose, whoseInput, type Whose } from './store';

/*
 * The conversations, newest first, from any machine. In a project it lists
 * that project's (a link switches to every project's); search covers titles
 * and messages; archived ones have their own view. Whose (D179): a project's
 * list is everyone's and the workspace-wide one yours until you pick
 * Everyone or Just mine (remembered); the choice shows once someone else has
 * a conversation here, and then each row says who started it. Only the
 * machine that holds a conversation can continue it, so others say where.
 */
export function ConversationList({
  activeId,
  onOpen,
  compact,
  className,
  projectId,
  projectName,
}: {
  activeId?: string;
  onOpen: (id: string) => void;
  compact?: boolean;
  className?: string;
  /** The project in view: its conversations are listed (with a way to every project's). */
  projectId?: string;
  projectName?: string;
}) {
  const [q, setQ] = useState('');
  const [search, setSearch] = useState('');
  const [archived, setArchived] = useState(false);
  const [everyProject, setEveryProject] = useState(false);
  const whose = useWhose();
  const me = useViewerId();
  useEffect(() => {
    const t = setTimeout(() => setSearch(q.trim()), 200);
    return () => clearTimeout(t);
  }, [q]);
  useEffect(() => setEveryProject(false), [projectId]);
  const inProject = !!projectId && !everyProject;
  const list = useConversations({
    search: search || undefined,
    archived: archived || undefined,
    projectId: inProject ? projectId : undefined,
    ...whoseInput(whose),
    limit: 60,
  });
  const archive = useAssistantOp('assistant.archive');
  const items = list.data?.conversations ?? [];
  const others = list.data?.others ?? 0;
  const everyone = !!list.data?.allUsers;
  // Rows say who started them once someone else's are among them.
  const labelled = everyone && others > 0;
  const empty = search
    ? `Nothing matches “${search}”.`
    : archived
      ? 'No archived conversations.'
      : !everyone && others > 0
        ? `None of yours yet. Choose Everyone to see ${others === 1 ? 'the one someone else started' : `the ${others} other people started`}.`
        : 'No conversations yet.';
  return (
    <div className={cx('flex min-h-0 flex-col', className)}>
      <div className="flex items-center gap-2 px-1 pb-2">
        <label className="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-[9px] bg-panel px-2.5 hairline focus-within:shadow-[inset_0_0_0_1px_var(--accent-line)]">
          <MagnifyingGlass size={13} className="shrink-0 text-subtle" aria-hidden />
          <span className="sr-only">Search conversations</span>
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search conversations"
            className="h-full min-w-0 flex-1 bg-transparent text-[12.5px] text-fg outline-none placeholder:text-subtle"
          />
          {list.isFetching ? <Spinner size={11} className="text-subtle" /> : null}
        </label>
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-1 pb-2">
        <div className="flex items-center gap-1" role="tablist" aria-label="Which conversations">
          {[
            { v: false, l: 'Recent' },
            { v: true, l: 'Archived' },
          ].map((t) => (
            <button
              key={t.l}
              type="button"
              role="tab"
              aria-selected={archived === t.v}
              onClick={() => setArchived(t.v)}
              className={cx('rounded-[7px] px-2 py-1 text-[12px] font-medium transition-colors', archived === t.v ? 'bg-hover text-fg' : 'text-subtle hover:text-muted')}
            >
              {t.l}
            </button>
          ))}
        </div>
        {others > 0 ? <WhoseChoice value={everyone ? 'everyone' : 'mine'} /> : null}
      </div>
      {projectId ? (
        <p className="flex min-w-0 items-center gap-1.5 px-2 pb-2 text-[11.5px] text-subtle" data-list-scope={inProject ? 'project' : 'all'}>
          <span className="min-w-0 truncate">{inProject ? <>In {projectName ? <Theirs>“{projectName}”</Theirs> : 'this project'}</> : 'In every project'}</span>
          <span aria-hidden>·</span>
          <button type="button" className="min-w-0 shrink truncate font-medium text-accent-text hover:underline" onClick={() => setEveryProject((v) => !v)}>
            {inProject ? 'Every project' : <>Just {projectName ? <Theirs>“{projectName}”</Theirs> : 'this project'}</>}
          </button>
        </p>
      ) : null}
      <div className="min-h-0 flex-1 overflow-y-auto">
        {list.isLoading ? (
          <div className="space-y-1.5 p-1" aria-busy="true">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-12" />
            ))}
          </div>
        ) : list.error ? (
          <ErrorPanel error={list.error} compact onRetry={() => void list.refetch()} />
        ) : items.length === 0 ? (
          <p className="px-2 py-6 text-center text-[12.5px] leading-relaxed text-subtle">{empty}</p>
        ) : (
          <ul className="space-y-0.5">
            {items.map((c) => (
              <Row
                key={c.id}
                c={c}
                me={me}
                active={c.id === activeId}
                compact={compact}
                labelled={labelled}
                showProject
                onOpen={() => onOpen(c.id)}
                onArchive={() => archive.mutate({ id: c.id, archived: !c.archived })}
                busy={archive.isPending && archive.variables?.id === c.id}
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

/** Everyone's conversations or just mine (remembered for this viewer). */
function WhoseChoice({ value }: { value: Whose }) {
  return (
    <div className="ml-auto flex items-center gap-0.5 rounded-[8px] bg-panel p-0.5 hairline" role="group" aria-label="Whose conversations" data-whose={value}>
      {[
        { v: 'everyone' as const, l: 'Everyone' },
        { v: 'mine' as const, l: 'Just mine' },
      ].map((o) => (
        <button
          key={o.v}
          type="button"
          aria-pressed={value === o.v}
          onClick={() => setWhose(o.v)}
          className={cx('rounded-[6px] px-2 py-0.5 text-[12px] font-medium transition-colors', value === o.v ? 'bg-hover text-fg' : 'text-subtle hover:text-muted')}
        >
          {o.l}
        </button>
      ))}
    </div>
  );
}

function Row({
  c,
  me,
  active,
  compact,
  labelled,
  showProject,
  onOpen,
  onArchive,
  busy,
}: {
  c: Conversation;
  me: string;
  active: boolean;
  compact?: boolean;
  labelled: boolean;
  showProject: boolean;
  onOpen: () => void;
  onArchive: () => void;
  busy: boolean;
}) {
  const working = c.state === 'working';
  const others = othersWaiting(c, me);
  // Archiving hides a conversation for everyone: offered on one's own here
  // (someone else's can still be archived from its menu); restoring on any.
  const canArchive = c.archived || c.mine;
  return (
    <li className="group relative">
      <button
        type="button"
        onClick={onOpen}
        aria-current={active ? 'true' : undefined}
        className={cx(
          'block w-full rounded-[10px] px-2.5 py-2 text-left transition-colors',
          canArchive ? 'pr-9' : 'pr-2.5',
          active ? 'bg-active' : 'hover:bg-hover focus-visible:bg-hover',
        )}
      >
        <span className="flex items-center gap-1.5">
          {working ? <Spinner size={10} className="shrink-0 text-accent-text" label="Working" /> : null}
          <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-fg">{c.title}</span>
        </span>
        <span className={cx('mt-0.5 flex min-w-0 items-center gap-1.5 text-[11.5px] text-subtle', compact && 'flex-wrap gap-y-0')}>
          <span className="shrink-0">{relative(c.updatedAt)}</span>
          {labelled ? (
            <span className={cx('min-w-0 truncate', !c.mine && 'text-muted')} data-started-by={c.mine ? 'you' : c.userName}>
              · {startedBy(c)}
            </span>
          ) : null}
          {showProject ? (
            // What each conversation is about, for good (D185).
            <span className="min-w-0 truncate" data-row-project={c.projectId ?? ''}>
              · {c.projectId ? <Theirs>{c.projectName || 'a project'}</Theirs> : 'Whole workspace'}
            </span>
          ) : null}
          {c.waitingForYou ? (
            <span className="shrink-0 font-medium text-accent-text" data-waiting-count={c.waitingForYou}>
              · {c.waitingForYou} waiting for you
            </span>
          ) : null}
          {others.count ? (
            <span className="min-w-0 truncate" data-waiting-others={others.count}>
              · {others.count} waiting for {others.whom}
            </span>
          ) : null}
          {!c.here ? (
            <span className="inline-flex min-w-0 shrink items-center gap-1 truncate" title={`Lives on ${c.installationName}`}>
              · <Desktop size={11} aria-hidden /> {c.installationName}
            </span>
          ) : null}
        </span>
      </button>
      {canArchive ? (
        <button
          type="button"
          onClick={onArchive}
          disabled={busy}
          aria-label={c.archived ? `Restore ${c.title}` : `Archive ${c.title}`}
          title={c.archived ? 'Restore' : 'Archive'}
          className="absolute right-1.5 top-1/2 grid h-7 w-7 -translate-y-1/2 place-items-center rounded-[8px] text-subtle opacity-0 transition-opacity hover:bg-hover hover:text-fg focus-visible:opacity-100 group-hover:opacity-100"
        >
          {busy ? <Spinner size={11} /> : c.archived ? <ArrowCounterClockwise size={13} /> : <Archive size={13} />}
        </button>
      ) : null}
    </li>
  );
}
