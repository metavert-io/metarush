import { memo, useMemo, useState, type ReactNode } from 'react';
import {
  ArrowsClockwise,
  BookOpenText,
  CaretDown,
  CaretRight,
  CheckCircle,
  Circle,
  CircleNotch,
  Eye,
  FileText,
  FilmStrip,
  ListChecks,
  MagnifyingGlass,
  PencilSimple,
  Prohibit,
  Sparkle,
  Stack,
  WarningCircle,
  X,
} from '@phosphor-icons/react';
import type { StreamState } from '../../api/assistant';
import type { AssistantAction, AssistantEvent, AssistantImage, AssistantToolCall, Conversation, ConversationTurn, TodoItem } from '../../api/types';
import { absolute, duration, stamp, tokenWords } from '../../lib/format';
import { useNow } from '../../lib/hooks';
import { Lightbox, type LightboxItem } from '../../ui/Lightbox';
import { cx, Spinner } from '../../ui/primitives';
import { ActionCard } from './ActionCard';
import { MessageFiles } from './Attachments';
import { CombinedCard } from './CombinedCard';
import { turnContextLine } from './context';
import { KeepCard } from './KeepCard';
import { Markdown } from './Markdown';
import { senderIdOf, senderOf, usePictures, useViewerId } from './people';
import { Avatar } from '../../ui/Avatar';
import { useSession } from '../../api/queries';
import { TurnProposalCards } from './Proposals';
import { QuestionCard } from './QuestionCard';

/**
 * A step, or several consecutive steps of one kind shown as one line
 * ("Looked at 19 stills from “Lena interview”"): the server gives each step
 * a group and the line for several (D140); the CLI groups the same way.
 */
type ToolRow = { call: AssistantToolCall; result?: AssistantToolCall; label: string; count: number; images: AssistantImage[] };

type Item =
  | { kind: 'text'; key: string; text: string }
  | { kind: 'tools'; key: string; rows: { call: AssistantToolCall; result?: AssistantToolCall }[] }
  | { kind: 'todo'; key: string; todos: TodoItem[] }
  | { kind: 'action'; key: string; actionId: string }
  | { kind: 'proposals'; key: string }
  | { kind: 'notice'; key: string; text: string; tone: 'muted' | 'danger' };

interface TurnView {
  n: number;
  head?: AssistantEvent;
  items: Item[];
  end?: AssistantEvent;
}

/** Groups the transcript by turn: prose, runs of activity rows, cards and notes. */
function buildTurns(events: AssistantEvent[], actionToolIds: Set<string>): TurnView[] {
  const results = new Map<string, AssistantToolCall>();
  for (const e of events) if (e.kind === 'tool_result' && e.tool) results.set(e.tool.id, e.tool);
  const turns = new Map<number, TurnView>();
  const get = (n: number) => {
    let t = turns.get(n);
    if (!t) {
      t = { n, items: [] };
      turns.set(n, t);
    }
    return t;
  };
  for (const e of events) {
    const t = get(e.turn);
    const key = `e${e.seq}`;
    switch (e.kind) {
      case 'turn':
        t.head = e;
        break;
      case 'text':
        if (e.text) t.items.push({ kind: 'text', key, text: e.text });
        break;
      case 'tool': {
        if (!e.tool || actionToolIds.has(e.tool.id)) break;
        if (e.tool.name === 'TodoWrite' && e.tool.todos?.length) {
          const prev = t.items.find((i): i is Extract<Item, { kind: 'todo' }> => i.kind === 'todo');
          if (prev) prev.todos = e.tool.todos;
          else t.items.push({ kind: 'todo', key, todos: e.tool.todos });
          break;
        }
        const row = { call: e.tool, result: results.get(e.tool.id) };
        const last = t.items[t.items.length - 1];
        if (last?.kind === 'tools') last.rows.push(row);
        else t.items.push({ kind: 'tools', key, rows: [row] });
        break;
      }
      case 'action':
        if (e.actionId) t.items.push({ kind: 'action', key, actionId: e.actionId });
        break;
      case 'proposals':
        if (!t.items.some((i) => i.kind === 'proposals')) t.items.push({ kind: 'proposals', key });
        break;
      case 'notice':
        if (e.text) t.items.push({ kind: 'notice', key, text: e.text, tone: 'muted' });
        break;
      case 'error':
        if (e.text) t.items.push({ kind: 'notice', key, text: e.text, tone: 'danger' });
        break;
      case 'turn_end':
        t.end = e;
        break;
    }
  }
  return [...turns.values()].sort((a, b) => a.n - b.n);
}

/** Consecutive steps with the same group become one line with a count. */
function groupRows(rows: { call: AssistantToolCall; result?: AssistantToolCall }[], raw: boolean): ToolRow[] {
  const out: ToolRow[] = [];
  for (const r of rows) {
    const last = out[out.length - 1];
    if (!raw && last && r.call.group && last.call.group === r.call.group) {
      last.count++;
      const many = r.call.many || last.call.many;
      if (many) last.label = many.replace('%d', String(last.count));
      last.images = [...last.images, ...(r.call.images ?? [])];
      last.result = r.result ?? last.result;
      continue;
    }
    out.push({ call: r.call, result: r.result, label: r.call.label || 'Worked on it', count: 1, images: r.call.images ?? [] });
  }
  return out;
}

/**
 * Cards a person decides with a click and can approve together (not questions,
 * not "Keep this?" cards, which are decided one by one, not ones only Claude's
 * machine can run, and not the Recipes cards: a run is agreed to on its own).
 */
export function decidable(a: AssistantAction, conv: Conversation): boolean {
  return a.state === 'pending' && !!a.op && !a.question && !a.keeping && !a.recipe /* Recipes (D211) */ && conv.here;
}

export interface ThreadProps {
  stream: StreamState;
  raw: boolean;
  billing: string;
  onNavigate: () => void;
  onDequeue?: (queueId: string) => void;
}

export function Thread({ stream, raw, billing, onNavigate, onDequeue }: ThreadProps) {
  const conv = stream.conversation;
  const me = useViewerId();
  const pictures = usePictures();
  const viewerName = useSession().data?.user.name || 'You';
  const actionToolIds = useMemo(() => new Set((conv?.actions ?? []).map((a) => a.toolUseId ?? '').filter(Boolean)), [conv?.actions]);
  const covered = useMemo(() => new Set((conv?.actions ?? []).map((a) => a.proposal ?? '').filter(Boolean)), [conv?.actions]);
  const turns = useMemo(() => buildTurns(stream.events, actionToolIds), [stream.events, actionToolIds]);
  if (!conv) return null;
  const turnInfo = new Map((conv.turns ?? []).map((t) => [t.n, t]));
  const running = conv.state === 'working' ? (conv.turns ?? []).find((t) => t.state === 'running') : undefined;
  // Who sent each message (D179): said on every message once someone other
  // than the viewer wrote in this conversation ("You", "Maya").
  const senders = new Map(turns.map((t) => [t.n, senderOf(t.head ?? turnInfo.get(t.n), conv, me)]));
  // Their pictures beside their messages while people sign in (D184; initials without one).
  const senderIds = new Map(turns.map((t) => [t.n, senderIdOf(t.head ?? turnInfo.get(t.n), conv)]));
  const shared = turns.some((t) => !(t.head?.state === 'note' || turnInfo.get(t.n)?.note) && senders.get(t.n) !== 'You');
  return (
    <ol className="space-y-6" aria-label="Conversation">
      {raw && conv.systemPrompt ? (
        <li>
          <RawBlock title="Rules added to Claude Code's system prompt" text={conv.systemPrompt} />
        </li>
      ) : null}
      {conv.compacted ? <li className="text-[12px] text-subtle">Older steps were trimmed from this saved conversation to keep it small.</li> : null}
      {turns.map((t) => (
        <li key={t.n}>
          <TurnBlock
            view={t}
            info={turnInfo.get(t.n)}
            conv={conv}
            raw={raw}
            billing={billing}
            live={running?.n === t.n}
            partial={running?.n === t.n ? stream.partial : undefined}
            covered={covered}
            onNavigate={onNavigate}
            sender={senders.get(t.n) ?? 'You'}
            showSender={shared}
            picture={pictures.on ? { src: pictures.of(senderIds.get(t.n) ?? ''), name: senders.get(t.n) === 'You' ? viewerName : senders.get(t.n) ?? '' } : undefined}
          />
        </li>
      ))}
      {(conv.queue ?? []).map((q) => (
        <li key={q.id} className="flex justify-end">
          <div className="max-w-[88%] rounded-[14px] bg-panel px-3.5 py-2.5 opacity-70 hairline">
            <div className="mb-1 flex items-center gap-2 text-[11.5px] text-subtle">
              <span>{q.note ? 'News for Claude, waiting to send' : shared ? `${senderOf(q, conv, me)}: waiting to send` : 'Waiting to send'}</span>
              {onDequeue && !q.note ? (
                <button type="button" onClick={() => onDequeue(q.id)} className="ml-auto rounded-[6px] p-0.5 hover:bg-hover hover:text-fg" aria-label="Remove this waiting message">
                  <X size={12} />
                </button>
              ) : null}
            </div>
            <p className="whitespace-pre-wrap break-words text-[13.5px] leading-relaxed text-fg">{q.note ? q.shown || plainNote(q.prompt) : q.prompt}</p>
            {q.attachments?.length ? <MessageFiles conversationId={conv.id} files={q.attachments} className="mt-2" /> : null}
          </div>
        </li>
      ))}
    </ol>
  );
}

const TurnBlock = memo(function TurnBlock({
  view,
  info,
  conv,
  raw,
  billing,
  live,
  partial,
  covered,
  onNavigate,
  sender,
  showSender,
  picture,
}: {
  view: TurnView;
  info?: ConversationTurn;
  conv: Conversation;
  raw: boolean;
  billing: string;
  live: boolean;
  partial?: StreamState['partial'];
  covered: Set<string>;
  onNavigate: () => void;
  /** Who sent this message: "You" or their name (D179). */
  sender: string;
  /** Say who sent each message (someone other than the viewer wrote in this conversation). */
  showSender: boolean;
  /** The sender's picture beside the message (people sign in; src absent: their initials). */
  picture?: { src?: string; name: string };
}) {
  const head = view.head;
  const note = head?.state === 'note' || info?.note;
  const ctx = turnContextLine(head?.context ?? info?.context);
  // When it was sent (D186): under every message, the full date on hover.
  const sentAt = head?.at || info?.startedAt;
  const answered = head?.actionId ? conv.actions?.find((a) => a.id === head.actionId && a.question) : undefined;
  // The pictures and PDFs the person sent with the message (D166).
  const files = head?.attachments?.length ? head.attachments : info?.attachments;
  const mine = sender === 'You';
  // Two or more cards of this turn waiting for a click show as one card (D144).
  const waiting = (conv.actions ?? []).filter((a) => a.turn === view.n && decidable(a, conv));
  const combined = waiting.length >= 2 ? new Set(waiting.map((a) => a.id)) : undefined;
  let combinedShown = false;
  return (
    <div className="space-y-3">
      {head ? (
        note ? (
          <div className="flex items-start gap-2 rounded-[12px] bg-ok-soft/60 px-3 py-2 text-[12.5px] leading-relaxed text-muted" data-note>
            <CheckCircle size={14} weight="fill" className="mt-[3px] shrink-0 text-ok" aria-hidden />
            <span className="min-w-0 flex-1 space-y-0.5 break-words">
              {(info?.shown || plainNote(head.text ?? '')).split('\n').map((l, i) => (
                <span key={i} className="block">
                  {l}
                </span>
              ))}
            </span>
            <When at={sentAt} className="mt-[1px] shrink-0" />
          </div>
        ) : (
          <div className="flex items-start justify-end gap-2" data-sender={showSender ? sender : undefined}>
            <div className="min-w-0 max-w-[88%]">
              {answered?.question ? (
                <div className="mb-1 truncate pr-1 text-right text-[11.5px] text-subtle">
                  {mine ? 'Your answer' : `${sender}’s answer`} to “{answered.question.text}”
                </div>
              ) : showSender ? (
                <div className={cx('mb-1 truncate pr-1 text-right text-[11.5px] font-medium', mine ? 'text-subtle' : 'text-muted')}>{sender}</div>
              ) : null}
              <div className={cx('rounded-[14px] px-3.5 py-2.5', mine ? 'bg-accent-soft' : 'bg-panel hairline')}>
                <p className="whitespace-pre-wrap break-words text-[13.5px] leading-relaxed text-fg">{head.text}</p>
                {files?.length ? <MessageFiles conversationId={conv.id} files={files} className="mt-2" /> : null}
              </div>
              <div className="mt-1 flex justify-end gap-1 truncate pr-1 text-right text-[11.5px] text-subtle" data-message-meta>
                {ctx ? <span className="min-w-0 truncate">{ctx}</span> : null}
                {ctx && sentAt ? <span aria-hidden>·</span> : null}
                <When at={sentAt} className="shrink-0" />
              </div>
            </div>
            {picture ? (
              <Avatar
                src={picture.src}
                name={picture.name}
                size={24}
                className={cx(showSender || answered?.question ? 'mt-[18px]' : 'mt-1')}
                marker="sender-avatar"
              />
            ) : null}
          </div>
        )
      ) : null}
      {raw && info?.sent ? <RawBlock title="Exact message sent to Claude Code" text={info.sent} /> : null}
      {view.items.map((it) => {
        switch (it.kind) {
          case 'text':
            return <Markdown key={it.key} text={it.text} />;
          case 'tools':
            return <ToolGroup key={it.key} rows={it.rows} live={live} raw={raw} />;
          case 'todo':
            return <TodoCard key={it.key} todos={it.todos} />;
          case 'action': {
            const a = conv.actions?.find((x) => x.id === it.actionId);
            if (!a) return null;
            if (a.keeping) return <KeepCard key={it.key} action={a} conversation={conv} raw={raw} onNavigate={onNavigate} />;
            if (combined?.has(a.id)) {
              if (combinedShown) return null;
              combinedShown = true;
              return <CombinedCard key={it.key} actions={waiting} conversation={conv} raw={raw} onNavigate={onNavigate} />;
            }
            if (a.question) return <QuestionCard key={it.key} action={a} conversation={conv} />;
            return <ActionCard key={it.key} action={a} conversation={conv} raw={raw} onNavigate={onNavigate} />;
          }
          case 'proposals':
            return <TurnProposalCards key={it.key} conversationId={conv.id} turn={view.n} here={conv.here} machine={conv.installationName} covered={covered} onNavigate={onNavigate} />;
          case 'notice':
            return (
              <p key={it.key} className={cx('flex items-start gap-2 text-[12.5px] leading-relaxed', it.tone === 'danger' ? 'text-danger' : 'text-subtle')}>
                {it.tone === 'danger' ? <WarningCircle size={14} className="mt-[2px] shrink-0" weight="fill" aria-hidden /> : null}
                <span>{it.text}</span>
              </p>
            );
        }
      })}
      {live ? <LiveTail partial={partial} since={info?.startedAt} /> : null}
      {view.end ? <TurnFooter end={view.end} info={info} billing={billing} raw={raw} /> : null}
    </div>
  );
});

/* ---------- While Claude works ---------- */

function LiveTail({ partial, since }: { partial?: StreamState['partial']; since?: string }) {
  if (partial?.phase === 'text' && partial.text) return <Markdown text={stripTitle(partial.text)} streaming />;
  const label = partial?.phase === 'thinking' ? 'Thinking' : 'Working';
  return <Working label={label} since={since} />;
}

/** Hides the [title: …] line Claude adds to its first reply while it streams. */
function stripTitle(s: string): string {
  return s.replace(/\n?\s*\[title:[^\]\n]*\]?\s*$/i, '');
}

function Working({ label, since }: { label: string; since?: string }) {
  const now = useNow(1000);
  const secs = since ? Math.max(0, (now - new Date(since).getTime()) / 1000) : 0;
  return (
    <p className="flex items-center gap-2 text-[12.5px] text-muted" aria-live="polite">
      <Spinner size={12} className="text-accent-text" />
      <span className="mr-shimmer-text">{label}</span>
      {since ? <span className="tc text-subtle">{duration(secs)}</span> : null}
    </p>
  );
}

/* ---------- Footer ---------- */

const stateLabel: Record<string, string> = {
  interrupted: 'Interrupted',
  stopped: 'Stopped',
  timed_out: 'Took too long',
  failed: 'Did not finish',
  budget: 'Reached the limit',
  lost: 'Cut off',
};

/** When something happened: "Today 3:10 PM", the full date on hover (D186). */
function When({ at, className }: { at?: string; className?: string }) {
  if (!at) return null;
  const s = stamp(at);
  if (!s) return null;
  return (
    <time dateTime={at} title={absolute(at)} className={cx('text-[11.5px] text-subtle', className)} data-message-time>
      {s}
    </time>
  );
}

function TurnFooter({ end, info, billing, raw }: { end: AssistantEvent; info?: ConversationTurn; billing: string; raw: boolean }) {
  const r = end.result;
  const parts: ReactNode[] = [];
  const at = end.at || info?.endedAt;
  if (at) parts.push(<When key="w" at={at} />);
  if (end.state && end.state !== 'succeeded') parts.push(<span key="s" className={end.state === 'interrupted' || end.state === 'stopped' ? '' : 'text-danger'}>{stateLabel[end.state] ?? 'Ended'}</span>);
  const ms = r?.durationMs ?? info?.durationMs ?? 0;
  if (ms > 0) parts.push(<span key="d">{duration(ms / 1000)}</span>);
  const steps = r?.steps ?? info?.steps ?? 0;
  if (steps > 0) parts.push(<span key="n">{steps === 1 ? '1 step' : `${steps} steps`}</span>);
  const tokens = r?.tokens?.total ?? info?.tokens?.total ?? 0;
  if (tokens > 0) parts.push(<span key="k">{tokenWords(tokens)} tokens</span>);
  const cost = r?.costUsd ?? info?.costUsd ?? 0;
  if (billing === 'api' && cost > 0) parts.push(<span key="c">${cost.toFixed(2)}</span>);
  if (raw && (r?.contextTokens ?? 0) > 0) parts.push(<span key="t">{Math.round((r!.contextTokens ?? 0) / 1000)}k context</span>);
  if (parts.length === 0) return null;
  return (
    <p className="tc flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11.5px] text-subtle">
      {parts.map((p, i) => (
        <span key={i} className="inline-flex items-center gap-2">
          {i > 0 ? <span aria-hidden>·</span> : null}
          {p}
        </span>
      ))}
    </p>
  );
}

/* ---------- Activity rows ---------- */

const icons: Record<string, ReactNode> = {
  Bash: <Sparkle size={13} />,
  Read: <FileText size={13} />,
  Write: <PencilSimple size={13} />,
  Edit: <PencilSimple size={13} />,
  MultiEdit: <PencilSimple size={13} />,
  Skill: <BookOpenText size={13} />,
  Glob: <MagnifyingGlass size={13} />,
  Grep: <MagnifyingGlass size={13} />,
  TodoWrite: <ListChecks size={13} />,
  Task: <Stack size={13} />,
  Agent: <Stack size={13} />,
};

function ToolGroup({ rows: steps, live, raw }: { rows: { call: AssistantToolCall; result?: AssistantToolCall }[]; live: boolean; raw: boolean }) {
  const [all, setAll] = useState(false);
  const rows = useMemo(() => groupRows(steps, raw), [steps, raw]);
  const images = rows.flatMap((r) => r.images);
  const hidden = rows.length > 5 && !all ? rows.length - 3 : 0;
  const shown = hidden ? rows.slice(hidden) : rows;
  return (
    <div className="space-y-2">
      <ul className="divide-y divide-line overflow-hidden rounded-[12px] bg-panel hairline" aria-label={rows.length === 1 ? '1 step' : `${rows.length} steps`}>
        {hidden ? (
          <li>
            <button type="button" onClick={() => setAll(true)} className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[12px] text-muted hover:bg-hover hover:text-fg">
              <CaretRight size={12} aria-hidden />
              {hidden} earlier {hidden === 1 ? 'step' : 'steps'}
            </button>
          </li>
        ) : null}
        {shown.map((r) => (
          <ToolRowView key={r.call.id} row={r} live={live} raw={raw} />
        ))}
      </ul>
      {images.length ? <Thumbnails images={images} /> : null}
    </div>
  );
}

function ToolRowView({ row, live, raw }: { row: ToolRow; live: boolean; raw: boolean }) {
  const { call, result } = row;
  const [open, setOpen] = useState(false);
  const status = result?.denied ? 'denied' : result?.isError ? 'error' : result ? 'ok' : live ? 'running' : 'none';
  const statusText = status === 'denied' ? 'not allowed here' : status === 'error' ? 'did not work' : status === 'running' ? 'working' : status === 'ok' ? 'done' : '';
  const label = row.label;
  // A step that did not work is usually one Claude corrects in the next step:
  // say so quietly instead of raising an alarm.
  const outcome =
    result?.outcome && status === 'ok' ? result.outcome : status === 'denied' && !/isn['’]t allowed/.test(label) ? 'not allowed here' : status === 'error' ? 'didn’t work' : '';
  const expandable = raw && !!(call.summary || call.input || result?.output);
  const head = (
    <span className="flex min-w-0 items-center gap-2 py-1.5 pl-3 pr-2.5">
      <span className="shrink-0 text-subtle" aria-hidden>
        {icons[call.name ?? ''] ?? <Sparkle size={13} />}
      </span>
      <span className="min-w-0 flex-1 truncate text-[12.5px] text-fg" title={label}>
        {label}
        {outcome ? <span className="text-subtle"> → {outcome}</span> : null}
      </span>
      <span className="ml-1 flex shrink-0 items-center gap-1" title={statusText}>
        {status === 'running' ? (
          <CircleNotch size={13} className="animate-spin text-accent-text" />
        ) : status === 'ok' ? (
          <CheckCircle size={14} className="text-subtle" />
        ) : status === 'denied' ? (
          <Prohibit size={14} className="text-subtle" />
        ) : status === 'error' ? (
          <WarningCircle size={14} className="text-subtle" />
        ) : null}
        {statusText ? <span className="sr-only">{statusText}</span> : null}
        {expandable ? open ? <CaretDown size={11} className="text-subtle" /> : <CaretRight size={11} className="text-subtle" /> : null}
      </span>
    </span>
  );
  if (!expandable) return <li>{head}</li>;
  return (
    <li>
      <button type="button" onClick={() => setOpen((v) => !v)} aria-expanded={open} className="block w-full text-left hover:bg-hover">
        {head}
      </button>
      {open ? (
        <div className="space-y-2 border-t border-line bg-inset px-3 py-2.5">
          {call.summary ? <pre className="tc whitespace-pre-wrap break-words text-[11px] leading-relaxed text-[#e9dcc8]">$ {call.summary}</pre> : null}
          {call.input && call.input !== '{}' ? <pre className="tc max-h-40 overflow-auto whitespace-pre-wrap break-words text-[11px] leading-relaxed text-[#a9c4e8]">{call.input}</pre> : null}
          {result?.output ? <pre className="tc max-h-56 overflow-auto whitespace-pre-wrap break-words text-[11px] leading-relaxed text-[#d9d1c6]">{result.output}</pre> : null}
        </div>
      ) : null}
    </li>
  );
}

/** Notes to Claude carry IDs ("created draft revision r3 (rev_…)"); people see the plain part. */
function withoutIds(s: string): string {
  return s
    .replace(/\s*\((?:rnd|rev|prj|ast|qar)_[0-9a-z]{26}\)/g, '')
    .replace(/\b(?:rnd|rev|prj|ast|qar)_[0-9a-z]{26}\b\s*/g, '')
    .replace(/\s*\(version [a-z0-9-]+@[0-9a-f]{6,64}\)/g, '')
    .replace(/\b([a-z][a-z0-9-]*)@[0-9a-f]{6,64}\b/g, '“$1”')
    .replace(/\s{2,}/g, ' ')
    .trim();
}

/** What the person did with a card, as Claude was told it, minus the parts written for Claude (older notes; newer ones carry their own plain line). */
function plainNote(s: string): string {
  return withoutIds(s)
    .replace(/^Done from the dashboard: (.*?)\. Result: ([\s\S]*?)\.?$/, '$2')
    .replace(/^Tried from the dashboard: (.*?)\. It failed: ([\s\S]*)$/, '$1 didn’t work: $2')
    .replace(/^Not now: (.*?)\. (?:Don't ask for it again unless I bring it up|Leave it for now; I may come back to it)\.$/, 'Not now: $1')
    .replace(/\b(\d{1,3}:\d{2})\.\d+\b/g, '$1');
}

export function imageUrl(img: AssistantImage): string {
  return `/api/assistant/image?root=${encodeURIComponent(img.root)}&path=${encodeURIComponent(img.path)}`;
}

/** The pictures Claude looked at: thumbnails that open large, ‹ › through all of them. */
function Thumbnails({ images }: { images: AssistantImage[] }) {
  const [open, setOpen] = useState<number | null>(null);
  const uniq = useMemo(() => images.filter((img, i) => images.findIndex((x) => x.root === img.root && x.path === img.path) === i), [images]);
  const items: LightboxItem[] = useMemo(() => uniq.map((img) => ({ src: imageUrl(img) })), [uniq]);
  const visible = uniq.slice(0, 6);
  const more = uniq.length - visible.length;
  return (
    <>
      <div className="grid grid-cols-3 gap-1.5 sm:grid-cols-4" role="list" aria-label="Pictures Claude looked at">
        {visible.map((img, i) => (
          <Thumb key={img.root + img.path} img={img} onOpen={() => setOpen(i)} more={i === visible.length - 1 && more > 0 ? more : 0} />
        ))}
      </div>
      {open != null ? <Lightbox items={items} index={open} title="What Claude looked at" onClose={() => setOpen(null)} /> : null}
    </>
  );
}

function Thumb({ img, onOpen, more }: { img: AssistantImage; onOpen: () => void; more: number }) {
  const [state, setState] = useState<'loading' | 'ok' | 'gone'>('loading');
  return (
    <button
      type="button"
      role="listitem"
      onClick={onOpen}
      disabled={state === 'gone'}
      className="group relative aspect-video overflow-hidden rounded-[8px] bg-inset hairline focus-visible:outline-2 focus-visible:outline-accent"
      aria-label={more ? `Open the pictures Claude looked at (${more} more)` : 'Open the picture Claude looked at, large'}
      title={state === 'gone' ? 'This picture is on the machine that holds the conversation' : 'Open large'}
      data-thumb
    >
      {state !== 'gone' ? (
        <img
          src={imageUrl(img)}
          alt=""
          loading="lazy"
          onLoad={() => setState('ok')}
          onError={() => setState('gone')}
          className={cx('h-full w-full object-cover transition-[opacity,transform] duration-300 group-hover:scale-[1.03]', state === 'ok' ? 'opacity-100' : 'opacity-0')}
        />
      ) : null}
      {state !== 'ok' ? (
        <span className="absolute inset-0 grid place-items-center text-subtle">{state === 'loading' ? <Spinner size={12} /> : <FilmStrip size={16} />}</span>
      ) : more ? (
        <span className="tc absolute inset-0 grid place-items-center bg-[rgb(0_0_0/0.5)] text-[13px] font-medium text-white">+{more}</span>
      ) : (
        <span className="absolute bottom-1 right-1 rounded-[5px] bg-[rgb(0_0_0/0.55)] p-0.5 text-white opacity-0 transition-opacity group-hover:opacity-100" aria-hidden>
          <Eye size={11} />
        </span>
      )}
    </button>
  );
}

/* ---------- Checklist ---------- */

function TodoCard({ todos }: { todos: TodoItem[] }) {
  const done = todos.filter((t) => t.status === 'completed').length;
  return (
    <section className="rounded-[12px] bg-panel px-3.5 py-3 hairline" aria-label="Claude's checklist">
      <div className="mb-2 flex items-center gap-2 text-[12px] text-muted">
        <ListChecks size={14} aria-hidden />
        <span>Plan</span>
        <span className="tc ml-auto text-subtle">
          {done}/{todos.length}
        </span>
      </div>
      <ul className="space-y-1.5">
        {todos.map((t, i) => (
          <li key={i} className="flex items-start gap-2 text-[13px] leading-snug">
            <span className="mt-[2px] shrink-0" aria-hidden>
              {t.status === 'completed' ? (
                <CheckCircle size={15} weight="fill" className="text-ok" />
              ) : t.status === 'in_progress' ? (
                <ArrowsClockwise size={15} className="text-accent-text" />
              ) : (
                <Circle size={15} className="text-subtle" />
              )}
            </span>
            <span className={cx('min-w-0', t.status === 'completed' ? 'text-subtle line-through decoration-line-strong' : 'text-fg')}>
              <span className="sr-only">{t.status === 'completed' ? 'Done: ' : t.status === 'in_progress' ? 'Doing now: ' : 'To do: '}</span>
              {t.status === 'in_progress' && t.activeForm ? t.activeForm : t.content}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

/* ---------- Raw view ---------- */

function RawBlock({ title, text }: { title: string; text: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded-[10px] hairline">
      <button type="button" onClick={() => setOpen((v) => !v)} aria-expanded={open} className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[11.5px] text-subtle hover:text-muted">
        {open ? <CaretDown size={11} /> : <CaretRight size={11} />}
        {title}
      </button>
      {open ? <pre className="tc max-h-72 overflow-auto whitespace-pre-wrap break-words border-t border-line bg-inset px-3 py-2.5 text-[11px] leading-relaxed text-[#d9d1c6]">{text}</pre> : null}
    </div>
  );
}
