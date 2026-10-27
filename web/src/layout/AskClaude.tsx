import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useNavigate } from 'react-router';
import {
  Archive,
  ArrowClockwise,
  ArrowCounterClockwise,
  ArrowLeft,
  ArrowsIn,
  ArrowsOut,
  ChatsCircle,
  ClockCounterClockwise,
  Desktop,
  FilmSlate,
  GearSix,
  NotePencil,
  PencilSimple,
  ShieldCheck,
  SlidersHorizontal,
  StopCircle,
  TerminalWindow,
  WarningCircle,
  X,
} from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { refreshAssistantStatus, useAssistantOp, useAssistantStatus, useConversationStream, useModels, type StreamState } from '../api/assistant';
import { toApiError } from '../api/client';
import { useOp } from '../api/queries';
import { useProjectActions } from '../components/project/ProjectDelete';
import type { AssistantStatus, Conversation } from '../api/types';
import { timecode, tokenWords } from '../lib/format';
import { useAskOpen, useAskPrefill, useAskRunRequest, useSelection, type Selection } from '../lib/workbench';
import { useDialog } from '../ui/Dialog';
import { Button, CopyButton, Details, ErrorPanel, Freshness, InlineCode, Skeleton, Spinner, Theirs, cx } from '../ui/primitives';
import { Composer, type PendingSettings } from './assistant/Composer';
import { ConversationList } from './assistant/ConversationList';
import { CutPane } from './assistant/CutPane';
import { MenuButton, type MenuItem } from './assistant/Menu';
import { WhoAnswers } from './assistant/WhoAnswers';
import { forgetForProject, newConversation, openConversation, rememberOpen, restorePanel, setPanel, syncConversationProject, usePanel } from './assistant/store';
import { useFollowProject } from './assistant/follow';
import { Thread } from './assistant/Thread';
import { Navigator } from './assistant/Navigator';
import { othersWaiting, useViewerId } from './assistant/people';
import { Welcome } from './assistant/Welcome';

/** Builds a ready-to-paste prompt for your own Claude Code session (only when Claude Code cannot run here). */
export function composePrompt(sel: Selection, request: string): string {
  const lines: string[] = [];
  if (sel.projectId) lines.push(`MetaRush project "${sel.projectName ?? sel.projectId}" (${sel.projectId}).`);
  else lines.push('MetaRush workspace (no project selected).');
  if (sel.headRevisionId) lines.push(`Latest version: r${sel.headRevisionNumber ?? '?'} (${sel.headRevisionId}).`);
  if (sel.revisionId && sel.revisionId !== sel.headRevisionId) lines.push(`I'm looking at version r${sel.revisionNumber ?? '?'} (${sel.revisionId}).`);
  if (sel.clipId) lines.push(`Selected clip: "${sel.clipId}".`);
  if (sel.assetId) {
    const at = sel.time !== undefined && sel.timeBase === 'source' ? ` at ${timecode(sel.time)} source time` : '';
    lines.push(`Selected footage: ${sel.assetName ?? sel.assetId} (${sel.assetId})${at}.`);
  } else if (sel.time !== undefined && sel.time > 0.05 && sel.timeBase === 'timeline') {
    lines.push(`Playhead: ${timecode(sel.time)} into the cut.`);
  }
  lines.push('');
  lines.push(request.trim() || '<describe the change you want>');
  return lines.join('\n');
}

let restored = false;
/** The last prepared request the panel put in its input (each is used once). */
let usedPrefill = 0;

/**
 * The Claude Code panel: real conversations with the person's local Claude
 * Code, in a drawer or full screen. It streams what Claude does, shows the
 * plans and changes it proposes as cards the person applies, and keeps every
 * conversation in the workspace's history.
 */
export function AskClaude() {
  const [open, setOpen] = useAskOpen();
  const panel = usePanel();
  // One project per conversation (D185): opening another project shows its conversation.
  useFollowProject(useSelection().projectId);
  useEffect(() => {
    if (!restored) {
      restored = true;
      restorePanel();
    }
  }, []);
  useEffect(() => rememberOpen(open), [open]);
  // A notification or deep link (?ask=<conversation>) opens that conversation.
  // Imported single requests kept their run ID, so older links still work.
  const askRequest = useAskRunRequest();
  useEffect(() => {
    if (askRequest.id) openConversation(askRequest.id);
  }, [askRequest]);
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.defaultPrevented || document.querySelector('[data-mr-dialog="open"]')) return;
      e.preventDefault();
      if (panel.fullscreen) setPanel({ fullscreen: false });
      else setOpen(false);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open, panel.fullscreen, setOpen]);
  if (!open) return null;
  return createPortal(<Panel onClose={() => setOpen(false)} />, document.body);
}

function Panel({ onClose }: { onClose: () => void }) {
  const panel = usePanel();
  const sel = useSelection();
  const status = useAssistantStatus();
  const titleId = useId();
  const st = status.data;
  const stream = useConversationStream(st?.available ? panel.conversationId : undefined, panel.raw);
  const [pending, setPending] = useState<PendingSettings>({});
  const [typed, setTyped] = useState(false);
  const [fill, setFill] = useState<{ text: string; nonce: number }>();
  const [showList, setShowList] = useState(false);
  const [showCut, setShowCut] = useState(false);
  const prevFocus = useRef<HTMLElement | null>(null);
  useEffect(() => {
    prevFocus.current = document.activeElement as HTMLElement | null;
    return () => prevFocus.current?.focus?.();
  }, []);
  const pick = useCallback((text: string) => setFill({ text, nonce: Date.now() }), []);
  // A prepared request from elsewhere in the dashboard (e.g. "Ask Claude to suggest a voiceover").
  const prefill = useAskPrefill();
  useEffect(() => {
    if (!prefill.nonce || prefill.nonce <= usedPrefill) return;
    usedPrefill = prefill.nonce;
    if (prefill.fresh) newConversation();
    setFill({ text: prefill.text, nonce: Date.now() });
  }, [prefill]);
  const conv = panel.conversationId ? stream.conversation : undefined;
  const projectId = conv?.projectId ?? sel.projectId;
  const fs = panel.fullscreen;
  // The open conversation's project is remembered for this tab (D185); one that is gone is forgotten.
  useEffect(() => {
    if (conv && conv.id === panel.conversationId) syncConversationProject(conv.id, conv.projectId ?? '');
  }, [conv, panel.conversationId]);
  useEffect(() => {
    if (stream.error && panel.projectKey !== undefined) forgetForProject(panel.projectKey);
  }, [stream.error, panel.projectKey]);
  // Toasts keep clear of the message box while the panel is open (styles.css).
  useEffect(() => {
    document.body.dataset.askPanel = fs ? 'full' : 'docked';
    return () => {
      delete document.body.dataset.askPanel;
    };
  }, [fs]);

  const body = !st ? (
    status.isLoading ? (
      <div className="space-y-3 p-5" aria-busy="true">
        <Skeleton className="h-5 w-2/3" />
        <Skeleton className="h-24" />
        <p className="flex items-center gap-2 text-[12.5px] text-subtle">
          <Spinner size={12} /> Checking for Claude Code on this machine
        </p>
      </div>
    ) : (
      <div className="p-5">
        <ErrorPanel error={status.error} onRetry={() => void status.refetch()} />
      </div>
    )
  ) : !st.available ? (
    <Unavailable status={st} sel={sel} updatedAt={status.dataUpdatedAt} fill={fill} />
  ) : panel.view === 'history' && !fs ? (
    <div className="min-h-0 flex-1 px-4 py-4">
      <ConversationList activeId={panel.conversationId} onOpen={(id) => openConversation(id)} projectId={sel.projectId} projectName={sel.projectName} className="h-full" />
    </div>
  ) : (
    <ThreadArea
      stream={stream}
      status={st}
      sel={sel}
      raw={panel.raw}
      hasConversation={!!panel.conversationId}
      typed={typed}
      showRecent={!fs}
      onPick={pick}
      onNavigate={() => {
        if (fs) setPanel({ fullscreen: false });
        onClose();
      }}
      composer={
        <ComposerArea conv={conv} status={st} sel={sel} pending={pending} setPending={setPending} onTyped={setTyped} fill={fill} />
      }
      wide={fs}
    />
  );

  const header = (
    <PanelHeader
      titleId={titleId}
      conv={conv}
      sel={sel}
      stream={stream}
      status={st}
      fullscreen={fs}
      raw={panel.raw}
      view={panel.view}
      onClose={onClose}
      onToggleList={fs ? () => setShowList((v) => !v) : undefined}
      onToggleCut={fs ? () => setShowCut((v) => !v) : undefined}
      listShown={showList}
      cutShown={showCut}
    />
  );

  if (fs) {
    return (
      <div role="dialog" aria-modal="true" aria-labelledby={titleId} className="fixed inset-0 z-[66] flex flex-col bg-bg" style={{ animation: 'mr-fade-in 0.25s var(--ease-out-expo) both' }}>
        {header}
        <div className="relative flex min-h-0 flex-1">
          <aside
            aria-label="Conversations"
            className={cx(
              'min-h-0 w-[280px] shrink-0 flex-col border-r border-line bg-raised px-3 py-3',
              showList ? 'absolute inset-y-0 left-0 z-10 flex shadow-[var(--shadow-float)] lg:static lg:shadow-none' : 'hidden lg:flex',
            )}
          >
            <Button
              size="sm"
              variant="secondary"
              icon={<NotePencil size={14} />}
              className="mb-3 w-full justify-start"
              onClick={() => {
                newConversation();
                setShowList(false);
              }}
            >
              New conversation
            </Button>
            <ConversationList
              activeId={panel.conversationId}
              compact
              projectId={sel.projectId}
              projectName={sel.projectName}
              onOpen={(id) => {
                openConversation(id);
                setShowList(false);
              }}
              className="min-h-0 flex-1"
            />
          </aside>
          <div className="flex min-h-0 min-w-0 flex-1 flex-col">{body}</div>
          <aside
            aria-label="The current cut"
            className={cx(
              'min-h-0 w-[380px] shrink-0 border-l border-line bg-raised',
              showCut ? 'absolute inset-y-0 right-0 z-10 block max-w-[92vw] shadow-[var(--shadow-float)] xl:static xl:shadow-none' : 'hidden xl:block',
            )}
          >
            <CutPane
              projectId={projectId}
              onNavigate={() => {
                setPanel({ fullscreen: false });
                onClose();
              }}
            />
          </aside>
        </div>
      </div>
    );
  }

  return (
    <div className="fixed inset-0 z-[65] flex justify-end">
      <div className="fade-in absolute inset-0 bg-[var(--overlay)] lg:bg-transparent" onMouseDown={onClose} aria-hidden />
      <aside
        role="dialog"
        aria-modal="false"
        aria-labelledby={titleId}
        className="relative flex h-full w-full max-w-[560px] flex-col border-l border-line bg-raised shadow-[var(--shadow-float)]"
        style={{ animation: 'mr-slide-in 0.38s var(--ease-out-expo) both' }}
      >
        {header}
        {body}
      </aside>
    </div>
  );
}

/* ---------- Header ---------- */

function PanelHeader({
  titleId,
  conv,
  sel,
  stream,
  status,
  fullscreen,
  raw,
  view,
  onClose,
  onToggleList,
  onToggleCut,
  listShown,
  cutShown,
}: {
  titleId: string;
  conv?: Conversation;
  sel: Selection;
  stream: StreamState;
  status?: AssistantStatus;
  fullscreen: boolean;
  raw: boolean;
  view: 'thread' | 'history';
  onClose: () => void;
  onToggleList?: () => void;
  onToggleCut?: () => void;
  listShown: boolean;
  cutShown: boolean;
}) {
  const dialog = useDialog();
  const navigate = useNavigate();
  const rename = useAssistantOp('assistant.rename');
  const archive = useAssistantOp('assistant.archive');
  const stop = useAssistantOp('assistant.stop');
  const available = !!status?.available;
  const listing = view === 'history' && !fullscreen;
  const me = useViewerId();
  // Cards and questions waiting for the person in other conversations (the list button says how many; D179: theirs only).
  const elsewhere = Math.max(0, (status?.waiting ?? 0) - (conv?.here ? (conv.waitingForYou ?? 0) : 0));
  const forOthers = conv ? othersWaiting(conv, me) : { count: 0, whom: '' };
  const title = listing ? 'Conversations' : (conv?.title ?? 'Claude Code');
  const sub = !available
    ? 'Not available on this machine'
    : listing
      ? 'On every machine in this workspace'
      : conv
        ? conv.state === 'working'
          ? 'Working'
          : !conv.here
            ? `Lives on ${conv.installationName}`
            : conv.waitingForYou
              ? conv.waitingForYou === 1
                ? '1 thing waits for you'
                : `${conv.waitingForYou} things wait for you`
              : forOthers.count
                ? forOthers.count === 1
                  ? `1 thing waits for ${forOthers.whom}`
                  : `${forOthers.count} things wait for ${forOthers.whom}`
                : stream.connected || !conv.live
                  ? 'Ready'
                  : 'Connecting'
        : 'Runs on this machine. Changes wait for your OK.';
  // What the conversation is about (D185): its project for good, or the whole
  // workspace; a fresh start is about the project in view.
  const about: { id: string; name?: string } | null = conv
    ? conv.projectId
      ? { id: conv.projectId, name: conv.projectName }
      : null
    : sel.projectId
      ? { id: sel.projectId, name: sel.projectName }
      : null;
  const openProject = (id: string) => {
    setPanel({ fullscreen: false });
    onClose();
    navigate(`/p/${id}`);
  };

  const items: (MenuItem | 'separator')[] = [
    { label: 'Show raw prompts and tool details', checked: raw, onSelect: () => setPanel({ raw: !raw }) },
  ];
  if (conv) {
    items.push('separator');
    items.push({
      label: 'Rename',
      icon: <PencilSimple size={14} />,
      onSelect: async () => {
        const t = await dialog.prompt({ title: 'Rename this conversation', label: 'Name', defaultValue: conv.title, maxLength: 120, required: true, confirmLabel: 'Rename' });
        if (t && t.trim()) rename.mutate({ id: conv.id, title: t.trim() });
      },
    });
    if (conv.here && conv.resumable) {
      items.push({ label: 'Model and effort', icon: <SlidersHorizontal size={14} />, hint: [conv.model || 'Default model', conv.effort ? `${conv.effort} effort` : ''].filter(Boolean).join(', '), onSelect: () => void openModelDialog(dialog, conv) });
    }
    if (conv.live) {
      items.push({ label: 'Stop Claude Code', icon: <StopCircle size={14} />, hint: 'Ends it now. The next message picks up again.', onSelect: () => stop.mutate({ id: conv.id }) });
    }
    items.push({
      label: conv.archived ? 'Restore' : 'Archive',
      icon: conv.archived ? <ArrowCounterClockwise size={14} /> : <Archive size={14} />,
      onSelect: () => {
        archive.mutate({ id: conv.id, archived: !conv.archived });
        if (!conv.archived) newConversation();
      },
    });
  }
  items.push('separator');
  items.push({
    label: 'Claude Code settings',
    icon: <GearSix size={14} />,
    onSelect: () => {
      setPanel({ fullscreen: false });
      onClose();
      navigate('/settings/claude#claude-code');
    },
  });

  return (
    <header className={cx('flex items-center gap-2 border-b border-line px-4 py-3 sm:px-5', fullscreen && 'bg-raised')}>
      {fullscreen && onToggleList ? (
        <button type="button" onClick={onToggleList} aria-pressed={listShown} aria-label="Conversations" className="grid h-8 w-8 place-items-center rounded-[9px] text-muted hover:bg-hover hover:text-fg lg:hidden">
          <ChatsCircle size={17} />
        </button>
      ) : null}
      {view === 'history' && !fullscreen && available ? (
        <button type="button" onClick={() => setPanel({ view: 'thread' })} aria-label="Back to the conversation" className="grid h-8 w-8 place-items-center rounded-[9px] text-muted hover:bg-hover hover:text-fg">
          <ArrowLeft size={16} />
        </button>
      ) : (
        <div className="hidden h-8 w-8 shrink-0 place-items-center rounded-[10px] bg-accent-soft text-accent-text sm:grid" aria-hidden>
          <TerminalWindow size={16} />
        </div>
      )}
      <div className="min-w-0 flex-1">
        <h2 id={titleId} className="truncate text-[14.5px] font-semibold tracking-[-0.01em] text-fg" title={title}>
          {title}
        </h2>
        <p className="flex min-w-0 items-center gap-1.5 truncate text-[12px] text-muted">
          {!listing && available ? (
            <>
              {about ? (
                <button
                  type="button"
                  onClick={() => openProject(about.id)}
                  className="min-w-0 shrink truncate font-medium text-accent-text hover:underline"
                  title={conv ? 'This conversation is about this project. Open it' : 'A new conversation here is about this project. Open it'}
                  data-header-project={about.id}
                >
                  In {about.name ? <Theirs>“{about.name}”</Theirs> : 'this project'}
                </button>
              ) : (
                <span className="shrink-0 font-medium text-fg" data-header-project="" title="This conversation is about the whole workspace, not one project">
                  Whole workspace
                </span>
              )}
              <span aria-hidden>·</span>
            </>
          ) : null}
          {conv?.state === 'working' && !listing ? <Spinner size={10} className="text-accent-text" /> : null}
          {conv && !conv.here && !listing ? <Desktop size={12} aria-hidden /> : null}
          <span className="truncate">{sub}</span>
        </p>
      </div>
      {conv && !listing ? <ContextMeter conv={conv} /> : null}
      {available ? (
        <div className="flex items-center gap-0.5">
          {!fullscreen ? (
            <HeaderButton
              label={view === 'history' ? 'Back to the conversation' : elsewhere ? `All conversations (${elsewhere} waiting for you in others)` : 'All conversations'}
              active={view === 'history'}
              onClick={() => setPanel({ view: view === 'history' ? 'thread' : 'history' })}
              badge={view === 'history' ? 0 : elsewhere}
            >
              <ClockCounterClockwise size={16} />
            </HeaderButton>
          ) : null}
          <button
            type="button"
            onClick={() => newConversation()}
            aria-label="New conversation"
            title={sel.projectName ? `A new conversation about “${sel.projectName}” (or type /fresh)` : 'A new conversation (or type /fresh)'}
            className="inline-flex h-8 items-center gap-1.5 rounded-[9px] px-2 text-[12.5px] font-medium text-muted transition-colors hover:bg-hover hover:text-fg"
            data-new-conversation
          >
            <NotePencil size={16} aria-hidden />
            <span className="hidden sm:inline">New conversation</span>
          </button>
          {fullscreen && onToggleCut ? (
            <HeaderButton label="The current cut" active={cutShown} onClick={onToggleCut} className="xl:hidden">
              <FilmSlate size={16} />
            </HeaderButton>
          ) : null}
          <HeaderButton label={fullscreen ? 'Exit full screen (Esc)' : 'Full screen'} onClick={() => setPanel({ fullscreen: !fullscreen, view: 'thread' })}>
            {fullscreen ? <ArrowsIn size={16} /> : <ArrowsOut size={16} />}
          </HeaderButton>
          <MenuButton items={items} label="More" />
        </div>
      ) : null}
      <HeaderButton
        label="Close (Esc)"
        onClick={() => {
          setPanel({ fullscreen: false });
          onClose();
        }}
      >
        <X size={16} />
      </HeaderButton>
    </header>
  );
}

function HeaderButton({ label, onClick, children, active, className, badge }: { label: string; onClick: () => void; children: ReactNode; active?: boolean; className?: string; badge?: number }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      title={label}
      aria-pressed={active === undefined ? undefined : active}
      className={cx('relative grid h-8 w-8 place-items-center rounded-[9px] text-muted transition-colors hover:bg-hover hover:text-fg', active && 'bg-hover text-fg', className)}
    >
      {children}
      {badge ? (
        <span className="tc absolute -right-0.5 -top-0.5 grid h-4 min-w-4 place-items-center rounded-full bg-accent px-1 text-[10px] font-semibold text-accent-ink" aria-hidden>
          {badge > 9 ? '9+' : badge}
        </span>
      ) : null}
    </button>
  );
}

/**
 * How full Claude's memory of the conversation is (when the stream reports
 * it), and how much of its token limit the conversation used (D181).
 */
function ContextMeter({ conv }: { conv: Conversation }) {
  const used = conv.contextTokens ?? 0;
  const max = conv.contextWindow ?? 0;
  const spent = conv.tokens?.total ?? 0;
  const limit = conv.limitTokens ?? 0;
  if ((!used || !max) && !spent) return null;
  const pct = used && max ? Math.min(1, used / max) : 0;
  const share = limit ? Math.min(1, spent / limit) : 0;
  const label = [
    used && max ? `Claude's memory of this conversation is ${Math.round(pct * 100)}% full (${tokenWords(used)} tokens)` : '',
    limit ? `${tokenWords(spent)} of its ${tokenWords(limit)}-token limit used` : '',
    conv.compactions ? `summarized ${conv.compactions === 1 ? 'once' : `${conv.compactions} times`}` : '',
  ]
    .filter(Boolean)
    .join('; ');
  return (
    <div className="hidden items-center gap-1.5 pr-1 sm:flex" title={label} role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round((limit ? share : pct) * 100)} data-usage-meter>
      <div className="h-1 w-10 overflow-hidden rounded-full bg-line">
        <div className={cx('h-full rounded-full', pct > 0.85 || share > 0.85 ? 'bg-danger' : 'bg-subtle')} style={{ width: `${Math.max(4, pct * 100)}%` }} />
      </div>
      <span className="tc text-[11px] text-subtle">{limit && spent ? `${tokenWords(spent)} / ${tokenWords(limit)}` : `${Math.round(pct * 100)}%`}</span>
    </div>
  );
}

async function openModelDialog(dialog: ReturnType<typeof useDialog>, conv: Conversation) {
  await dialog.open<void>((close) => <ModelForm conv={conv} onDone={() => close()} />, { title: 'Model and effort', size: 'sm' });
}

function ModelForm({ conv, onDone }: { conv: Conversation; onDone: () => void }) {
  const models = useModels();
  const configure = useAssistantOp('assistant.configure');
  const [model, setModel] = useState(conv.model || 'default');
  const [effort, setEffort] = useState(conv.effort || 'default');
  const cat = models.data?.models ?? [];
  const chosen = cat.find((m) => m.value === model || m.resolvedModel === model) ?? cat.find((m) => m.value === 'default');
  const levels = chosen?.effortLevels ?? [];
  useEffect(() => {
    if (effort !== 'default' && models.data && !levels.includes(effort)) setEffort('default');
  }, [effort, levels, models.data]);
  return (
    <div className="space-y-4">
      <p className="text-[12.5px] leading-relaxed text-muted">For this conversation only. Bigger models and higher effort think harder and use more of your Claude plan (or cost more with an API key).</p>
      {models.isLoading ? (
        <Skeleton className="h-20" />
      ) : models.error ? (
        <ErrorPanel error={models.error} compact />
      ) : (
        <>
          <label className="block">
            <span className="mb-1 block text-[12px] font-medium text-muted">Model</span>
            <select value={model} onChange={(e) => setModel(e.target.value)} className="h-9 w-full rounded-[9px] bg-panel px-2.5 text-[13px] text-fg hairline">
              {cat.map((m) => (
                <option key={m.value} value={m.value}>
                  {m.value === 'default' ? 'Default (Claude Code picks)' : m.displayName}
                </option>
              ))}
            </select>
          </label>
          <label className="block">
            <span className="mb-1 block text-[12px] font-medium text-muted">Effort</span>
            <select value={effort} disabled={levels.length === 0} onChange={(e) => setEffort(e.target.value)} className="h-9 w-full rounded-[9px] bg-panel px-2.5 text-[13px] text-fg hairline disabled:opacity-60">
              <option value="default">Default</option>
              {levels.map((l) => (
                <option key={l} value={l}>
                  {l}
                </option>
              ))}
            </select>
            {levels.length === 0 ? <span className="mt-1 block text-[11.5px] text-subtle">This model has no effort setting.</span> : null}
          </label>
        </>
      )}
      {configure.error ? <ErrorPanel error={configure.error} compact /> : null}
      <div className="flex justify-end gap-2">
        <Button size="sm" variant="ghost" onClick={onDone}>
          Cancel
        </Button>
        <Button size="sm" variant="primary" loading={configure.isPending} disabled={!models.data} onClick={() => configure.mutate({ id: conv.id, model, effort }, { onSuccess: onDone })}>
          Save
        </Button>
      </div>
    </div>
  );
}

/* ---------- Thread area (scrolling thread + composer) ---------- */

function ThreadArea({
  stream,
  status,
  sel,
  raw,
  hasConversation,
  typed,
  showRecent,
  onPick,
  onNavigate,
  composer,
  wide,
}: {
  stream: StreamState;
  status: AssistantStatus;
  sel: Selection;
  raw: boolean;
  hasConversation: boolean;
  typed: boolean;
  showRecent: boolean;
  onPick: (t: string) => void;
  onNavigate: () => void;
  composer: ReactNode;
  wide: boolean;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const dequeue = useAssistantOp('assistant.dequeue');
  const conv = stream.conversation;
  const convId = conv?.id;
  useEffect(() => {
    stick.current = true;
    const el = scroller.current;
    const c = content.current;
    if (!el || !c || typeof ResizeObserver === 'undefined') return;
    el.scrollTop = el.scrollHeight;
    const ro = new ResizeObserver(() => {
      if (stick.current) el.scrollTop = el.scrollHeight;
    });
    ro.observe(c);
    return () => ro.disconnect();
  }, [convId]);
  const working = conv?.state === 'working';
  const me = useViewerId();
  const forOthers = conv ? othersWaiting(conv, me) : { count: 0, whom: '' };
  const announce = !conv
    ? ''
    : working
      ? 'Claude is working on the latest message.'
      : conv.waitingForYou
        ? 'Claude is waiting for you.'
        : forOthers.count
          ? `Claude is waiting for ${forOthers.whom}.`
          : '';
  return (
    <>
      <div
        ref={scroller}
        className="min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-5"
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
        }}
      >
        <p className="sr-only" aria-live="polite">
          {announce}
        </p>
        <div ref={content} className={cx(wide && 'mx-auto max-w-[760px]')}>
          {!hasConversation ? (
            <Welcome projectId={sel.projectId} projectName={sel.projectName} status={status} hideSuggestions={typed} showRecent={showRecent} onPick={onPick} onOpen={(id) => openConversation(id)} me={me} />
          ) : stream.error ? (
            <div className="space-y-3">
              <ErrorPanel error={stream.error} />
              <Button size="sm" variant="secondary" onClick={() => newConversation()}>
                Start a new conversation
              </Button>
            </div>
          ) : stream.loading || !conv ? (
            <div className="space-y-4" aria-busy="true">
              <Skeleton className="ml-auto h-12 w-2/3 rounded-[14px]" />
              <Skeleton className="h-20" />
              <Skeleton className="h-10 w-1/2" />
            </div>
          ) : (
            <>
              <WhoAnswers account={conv.account ?? status.account} compact className="mb-4" />
              {conv.keptHere ? (
                // What may leave (D290): what this client's Content Settings keep from Claude, said plainly.
                <div className="mb-4 flex items-start gap-2 rounded-[12px] bg-panel px-3.5 py-2.5 text-[12.5px] leading-relaxed text-muted hairline" data-conversation-kept-here>
                  <ShieldCheck size={15} className="mt-[2px] shrink-0 text-subtle" aria-hidden />
                  <p className="min-w-0 flex-1">{conv.keptHere}</p>
                </div>
              ) : null}
              <Thread stream={stream} raw={raw} billing={status.billing} onNavigate={onNavigate} onDequeue={(queueId) => dequeue.mutate({ id: conv.id, queueId })} />
            </>
          )}
        </div>
      </div>
      <div className={cx('border-t border-line bg-raised px-4 pb-4 pt-3 sm:px-5')}>
        <div className={cx(wide && 'mx-auto max-w-[760px]')}>
          {hasConversation && conv ? <Navigator conv={conv} me={me} scroller={scroller} /> : null}
          {composer}
        </div>
      </div>
    </>
  );
}

function ComposerArea({
  conv,
  status,
  sel,
  pending,
  setPending,
  onTyped,
  fill,
}: {
  conv?: Conversation;
  status: AssistantStatus;
  sel: Selection;
  pending: PendingSettings;
  setPending: (p: PendingSettings) => void;
  onTyped: (b: boolean) => void;
  fill?: { text: string; nonce: number };
}) {
  // Archived projects, kept fresh by deleting and restoring (the conversation's own flag until they load).
  const archivedList = useOp('project.list', conv?.projectId ? { archivedOnly: true } : null, { staleTime: 30_000 });
  const projectArchived = archivedList.data ? archivedList.data.projects.some((p) => p.id === conv?.projectId) : !!conv?.projectArchived;
  if (projectArchived && conv?.projectId) {
    // A deleted project's conversation (D196): it reads as before; Restore brings the project back.
    return <ArchivedConversationLine projectId={conv.projectId} projectName={conv.projectName} />;
  }
  if (conv && (!conv.here || !conv.resumable)) {
    const why = !conv.here
      ? `This conversation lives on ${conv.installationName}, where Claude Code has its session. Continue it there, or start a new one here.`
      : conv.legacy
        ? 'This was a single request from an older MetaRush, so it cannot be continued. Start a new conversation instead.'
        : "Claude Code no longer has this conversation's session on this machine. Start a new conversation instead.";
    return (
      <div className="flex flex-wrap items-center gap-3 rounded-[12px] bg-panel px-3.5 py-3 hairline">
        <WarningCircle size={16} className="shrink-0 text-subtle" aria-hidden />
        <p className="min-w-0 flex-1 text-[12.5px] leading-relaxed text-muted">{why}</p>
        <Button size="sm" variant="secondary" icon={<NotePencil size={14} />} onClick={() => newConversation()}>
          New conversation
        </Button>
      </div>
    );
  }
  return <Composer conversation={conv} status={status} sel={sel} pending={pending} setPending={setPending} onTyped={onTyped} fill={fill} autoFocus />;
}

/**
 * A deleted project's conversation (D196) reads as before; instead of the
 * message box, one line says the project is archived, with Restore.
 */
function ArchivedConversationLine({ projectId, projectName }: { projectId: string; projectName?: string }) {
  const { restore } = useProjectActions();
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-[12px] bg-panel px-3.5 py-3 hairline" data-conversation-archived>
      <Archive size={16} className="shrink-0 text-subtle" aria-hidden />
      <p className="min-w-0 flex-1 text-[12.5px] leading-relaxed text-muted">
        {projectName ? (
          <>
            “<Theirs>{projectName}</Theirs>” is archived
          </>
        ) : (
          'This project is archived'
        )}{' '}
        — restore it to carry on here.
      </p>
      <Button size="sm" variant="secondary" icon={<ArrowCounterClockwise size={14} />} onClick={() => void restore({ id: projectId, name: projectName ?? 'the project' })}>
        Restore
      </Button>
    </div>
  );
}

/* ---------- Not available here: say why, offer a prompt to copy ---------- */

function Unavailable({ status, sel, updatedAt, fill }: { status: AssistantStatus; sel: Selection; updatedAt: number; fill?: { text: string; nonce: number } }) {
  const qc = useQueryClient();
  const [checking, setChecking] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [draft, setDraft] = useState(fill?.text ?? '');
  useEffect(() => {
    if (fill) setDraft(fill.text);
  }, [fill]);
  const prompt = useMemo(() => composePrompt(sel, draft), [sel, draft]);
  const id = useId();
  const retry = async () => {
    setChecking(true);
    setErr(null);
    try {
      qc.setQueryData(['assistant', 'status'], await refreshAssistantStatus());
    } catch (e) {
      setErr(toApiError(e));
    } finally {
      setChecking(false);
    }
  };
  return (
    <div className="min-h-0 flex-1 overflow-y-auto px-5 py-5">
      <div role="status" className="rounded-[14px] bg-panel p-3.5 hairline">
        <div className="flex items-start gap-2.5">
          <WarningCircle size={17} weight="fill" className="mt-[1px] shrink-0 text-accent-text" />
          <div className="min-w-0 flex-1">
            <p className="text-[13.5px] font-medium text-fg">{status.reason ?? 'Claude Code cannot run here.'}</p>
            {status.hint ? (
              <p className="mt-1 text-[12.5px] leading-relaxed text-muted">
                <InlineCode text={status.hint} />
              </p>
            ) : null}
            <div className="mt-2.5 flex flex-wrap items-center gap-3">
              <Button size="sm" variant="secondary" icon={<ArrowClockwise size={13} />} loading={checking} onClick={() => void retry()}>
                Check again
              </Button>
              <Freshness at={updatedAt || undefined} fetching={checking} />
            </div>
            {err ? <ErrorPanel error={err} compact className="mt-2.5" /> : null}
          </div>
        </div>
      </div>
      <label htmlFor={id} className="mb-1.5 mt-6 block text-[12.5px] font-medium text-muted">
        What should change?
      </label>
      <textarea
        id={id}
        rows={4}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        placeholder="Start on the question, cut the first pause, end on the payoff line."
        className="w-full resize-y rounded-[12px] bg-panel px-3.5 py-3 text-[13.5px] leading-relaxed text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-offset-0 focus:outline-accent"
      />
      <div className="mb-1.5 mt-4 flex items-center justify-between gap-3">
        <span className="text-[12.5px] font-medium text-muted">Copy this, then paste it into Claude Code yourself. It says which project and version you mean.</span>
        <CopyButton text={prompt} label="Copy prompt" />
      </div>
      <Details label="See the prompt" className="mt-2">
        <pre className="tc whitespace-pre-wrap break-words rounded-[12px] bg-inset p-4 text-[12px] leading-relaxed text-[#ece4d9]">{prompt}</pre>
      </Details>
    </div>
  );
}
