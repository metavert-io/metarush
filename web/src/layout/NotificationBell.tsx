import { useEffect, useId, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import { BatteryWarning, Bell, CheckCircle, Checks, CookingPot, CopySimple, FilmStrip, Info, MagnifyingGlass, Microphone, PuzzlePiece, Question, SealCheck, ShieldCheck, Sparkle, UserFocus, UsersThree, WarningCircle } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call } from '../api/client';
import { useOp } from '../api/queries';
import type { Notification } from '../api/types';
import { relative } from '../lib/format';
import { getAskOpen, jobSeen } from '../lib/workbench';
import { getPanel } from './assistant/store';
import { useNow } from '../lib/hooks';
import { cx, Freshness, Spinner } from '../ui/primitives';
import { useToast } from '../ui/Toast';

/** How long a notice about finished work waits for the screen that ran it to say it saw the answer. */
const JOB_NOTICE_GRACE_MS = 2500;

/** What a notification's button says ("Recognized Maya's voice in 2 more videos" asks you to check). */
function actionLabel(n: Notification) {
  if (n.kind === 'recipe_run') return n.tone === 'success' ? 'Open' : 'Look at the run';
  if (n.kind === 'recipe_planned') return n.tone === 'warning' ? 'Look at it' : 'Run…'; // a run planned by itself (D218)
  if (n.kind === 'ability_run') return n.tone === 'success' ? 'Open' : 'Look at the run';
  return n.kind === 'speakers_recognized' ? 'Check' : n.kind === 'remotion_waiting' || n.kind === 'ability_waiting' ? 'Look at it' : 'Open';
}

function KindIcon({ n }: { n: Notification }) {
  const cls = n.tone === 'error' ? 'text-danger' : n.tone === 'warning' ? 'text-accent-text' : n.tone === 'success' ? 'text-ok' : 'text-info';
  const size = 16;
  switch (n.kind) {
    case 'file_ready':
      return n.tone === 'error' ? <WarningCircle size={size} weight="fill" className={cls} /> : <CheckCircle size={size} weight="fill" className={cls} />;
    case 'render_finished':
    case 'render_failed':
      return <FilmStrip size={size} weight="fill" className={cls} />;
    case 'qa_done':
      return <ShieldCheck size={size} weight="fill" className={cls} />;
    case 'assistant_proposal':
      return <Sparkle size={size} weight="fill" className="text-accent-text" />;
    case 'duplicate_skipped':
      return <CopySimple size={size} className="text-info" />;
    case 'battery_paused':
      return <BatteryWarning size={size} weight="fill" className="text-accent-text" />;
    case 'speakers':
      return n.tone === 'error' ? <WarningCircle size={size} weight="fill" className={cls} /> : <UsersThree size={size} weight="fill" className={cls} />;
    case 'speakers_recognized':
      return <UserFocus size={size} weight="fill" className="text-info" />;
    case 'narration_ready':
    case 'narration_failed':
      return <Microphone size={size} weight="fill" className={cls} />;
    case 'remotion_waiting':
    case 'ability_waiting': // a version of an ability waits for an owner (D231)
      return <SealCheck size={size} weight="fill" className="text-accent-text" />;
    case 'ability_run':
      return <PuzzlePiece size={size} weight="fill" className={cls} />;
    case 'recipe_run':
    case 'recipe_planned':
      return <CookingPot size={size} weight="fill" className={cls} />;
    default:
      return <Info size={size} weight="fill" className={cls} />;
  }
}

/**
 * Notifications bell (top right): server-side, per person, the same on every
 * machine they use. New ones also toast; the unread badge survives reloads.
 */
export function NotificationBell() {
  const [open, setOpen] = useState(false);
  const list = useOp('notification.list', { limit: 40 }, { refetchInterval: 60_000 });
  const qc = useQueryClient();
  const navigate = useNavigate();
  const toast = useToast();
  const panelId = useId();
  const btn = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const [marking, setMarking] = useState(false);
  const unread = list.data?.unread ?? 0;
  useNow(30_000);

  // Toast notifications that arrive while this page is open (not the backlog).
  const seen = useRef<Set<string> | null>(null);
  useEffect(() => {
    const items = list.data?.notifications;
    if (!items) return;
    if (seen.current === null) {
      seen.current = new Set(items.map((n) => n.id + n.updatedAt));
      return;
    }
    const show = (n: Notification) =>
      toast({
        tone: n.tone === 'error' ? 'error' : n.tone === 'success' ? 'success' : 'info',
        title: n.title,
        body: n.body,
        action: n.link ? { label: actionLabel(n), onClick: () => void openOne(n) } : undefined,
      });
    const settle = (n: Notification) => {
      if (!jobSeen(n.jobId)) return false;
      // The screen that ran this work already said how it went.
      void call('notification.read', { ids: [n.id] }).then(() => qc.invalidateQueries({ queryKey: ['notification.list'] }), () => undefined);
      return true;
    };
    for (const n of [...items].reverse()) {
      const k = n.id + n.updatedAt;
      if (seen.current.has(k)) continue;
      seen.current.add(k);
      if (n.read) continue;
      // A conversation the person is looking at in the panel says it already
      // (its cards wait right there): no toast over the message box.
      const conv = (n.details as { conversationId?: string } | undefined)?.conversationId;
      if (n.kind === 'assistant_proposal' && conv && getAskOpen() && getPanel().conversationId === conv) {
        void call('notification.read', { ids: [n.id] }).then(() => qc.invalidateQueries({ queryKey: ['notification.list'] }), () => undefined);
        continue;
      }
      if (n.tone !== 'error' && n.jobId) {
        if (settle(n)) continue;
        // The screen that asked for this work may still be waiting for its
        // answer (the notice can arrive first on a busy machine): look again
        // in a moment before saying it a second time.
        window.setTimeout(() => {
          if (!settle(n)) show(n);
        }, JOB_NOTICE_GRACE_MS);
        continue;
      }
      show(n);
    }
  }, [list.data]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (!panel.current?.contains(t) && !btn.current?.contains(t)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('[data-mr-dialog="open"]')) {
        setOpen(false);
        btn.current?.focus();
      }
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    panel.current?.querySelector<HTMLElement>('[data-first]')?.focus();
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const refresh = () => void qc.invalidateQueries({ queryKey: ['notification.list'] });

  const markRead = async (ids: string[] | 'all') => {
    try {
      await call('notification.read', ids === 'all' ? { all: true } : { ids });
    } catch {
      // stays unread; the next refresh shows the truth
    }
    refresh();
  };

  const openOne = async (n: Notification, link = n.link) => {
    setOpen(false);
    if (!n.read) void markRead([n.id]);
    if (link) navigate(link);
  };

  const items = list.data?.notifications ?? [];
  return (
    <div className="relative">
      <button
        ref={btn}
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        aria-controls={panelId}
        aria-label={unread ? `Notifications, ${unread} unread` : 'Notifications'}
        title="Notifications"
        className={cx('relative inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-[10px] text-muted transition-colors hover:bg-hover hover:text-fg max-sm:w-8', open && 'bg-hover text-fg')}
      >
        <Bell size={18} weight={unread ? 'fill' : 'regular'} className={unread ? 'text-fg' : undefined} />
        {unread ? (
          <span className="tc absolute -right-0.5 -top-0.5 inline-flex h-[17px] min-w-[17px] items-center justify-center rounded-full bg-accent px-1 text-[10px] font-semibold leading-none text-accent-ink ring-2 ring-[var(--bg)]">
            {unread > 99 ? '99+' : unread}
          </span>
        ) : null}
      </button>
      {open ? (
        <div
          ref={panel}
          id={panelId}
          role="dialog"
          aria-label="Notifications"
          className="fade-up fixed inset-x-3 top-[60px] z-[60] max-h-[min(78dvh,620px)] overflow-hidden rounded-[16px] bg-raised shadow-[var(--shadow-float)] hairline sm:absolute sm:inset-x-auto sm:right-0 sm:top-[calc(100%+8px)] sm:w-[400px]"
        >
          <div className="flex items-center gap-2 border-b border-line px-4 py-3">
            <h2 className="text-[14px] font-semibold text-fg">Notifications</h2>
            {unread ? <span className="shrink-0 whitespace-nowrap rounded-full bg-accent-soft px-1.5 text-[11px] font-medium text-accent-text">{unread} new</span> : null}
            <Freshness className="ml-auto whitespace-nowrap max-sm:hidden" at={list.dataUpdatedAt || undefined} fetching={list.isFetching} />
            <button
              type="button"
              data-first
              disabled={!unread || marking}
              onClick={async () => {
                setMarking(true);
                await markRead('all');
                setMarking(false);
              }}
              className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap rounded-[8px] px-2 py-1 text-[12px] font-medium text-accent-text hover:bg-accent-soft disabled:text-subtle disabled:hover:bg-transparent max-sm:ml-auto"
            >
              {marking ? <Spinner size={11} /> : <Checks size={13} />} Mark all read
            </button>
          </div>
          <div className="max-h-[min(66dvh,540px)] overflow-y-auto p-1.5">
            {list.isLoading ? (
              <div className="flex items-center justify-center gap-2 py-10 text-[13px] text-muted">
                <Spinner size={13} /> Loading
              </div>
            ) : items.length === 0 ? (
              <div className="px-6 py-10 text-center">
                <div className="mx-auto mb-3 grid h-10 w-10 place-items-center rounded-[12px] bg-hover text-muted">
                  <Bell size={18} />
                </div>
                <div className="text-[13.5px] font-medium text-fg">Nothing yet</div>
                <p className="mx-auto mt-1 max-w-[30ch] text-[12.5px] leading-relaxed text-muted">Footage that is ready, finished renders, quality checks and anything that needs you show up here, on every machine you use.</p>
              </div>
            ) : (
              <ul>
                {items.map((n) => (
                  <li key={n.id} className={cx('group relative rounded-[11px]', !n.read && 'bg-accent-soft/40')}>
                    <button
                      type="button"
                      onClick={() => void openOne(n)}
                      className="flex w-full gap-3 rounded-[11px] px-3 py-2.5 text-left transition-colors hover:bg-hover"
                    >
                      <span className="mt-[2px] shrink-0">
                        <KindIcon n={n} />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className={cx('block break-words text-[13px] leading-snug', n.read ? 'text-muted' : 'font-medium text-fg')}>{n.title}</span>
                        {n.body ? <span className="clamp-2 mt-0.5 block text-[12px] leading-relaxed text-muted">{n.body}</span> : null}
                        <span className="mt-1 block text-[11px] text-subtle">
                          {relative(n.updatedAt)}
                          {n.machine ? `, ${n.machine}` : ''}
                        </span>
                      </span>
                      {!n.read ? <span className="sr-only">Unread</span> : null}
                    </button>
                    {n.kind === 'speakers_recognized' && n.link ? (
                      <button
                        type="button"
                        onClick={() => void openOne(n)}
                        data-check
                        className="-mt-1 mb-2.5 ml-[40px] inline-flex items-center gap-1.5 rounded-full bg-info-soft px-2 py-0.5 text-[11.5px] font-medium text-info hover:underline"
                      >
                        <MagnifyingGlass size={11} weight="bold" aria-hidden />
                        Check
                      </button>
                    ) : null}
                    {n.logLink ? (
                      <button
                        type="button"
                        onClick={() => void openOne(n, n.logLink)}
                        className="-mt-1 mb-2.5 ml-[40px] inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11.5px] font-medium text-accent-text hover:bg-accent-soft hover:underline"
                      >
                        See log
                      </button>
                    ) : null}
                    {n.questions ? (
                      <button
                        type="button"
                        onClick={() => void openOne(n, n.questions!.link)}
                        className="-mt-1 mb-2.5 ml-[40px] inline-flex items-center gap-1.5 rounded-full bg-accent-soft px-2 py-0.5 text-[11.5px] font-medium text-accent-text hover:underline"
                      >
                        <Question size={11} weight="bold" aria-hidden />
                        {n.questions.text[0]!.toUpperCase() + n.questions.text.slice(1)}
                      </button>
                    ) : null}
                    {!n.read ? (
                      <button
                        type="button"
                        onClick={() => void markRead([n.id])}
                        aria-label={`Mark “${n.title}” read`}
                        title="Mark read"
                        className="absolute right-2 top-2.5 rounded-[7px] p-1 text-subtle opacity-0 hover:bg-raised hover:text-fg focus-visible:opacity-100 group-hover:opacity-100 [@media(hover:none)]:opacity-100"
                      >
                        <Checks size={13} />
                      </button>
                    ) : null}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      ) : null}
    </div>
  );
}
