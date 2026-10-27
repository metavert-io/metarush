import { useEffect, useId, useMemo, useRef, useState, type ClipboardEvent, type DragEvent, type KeyboardEvent } from 'react';
import { ArrowUp, Check, Paperclip, StopCircle, WarningCircle } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { primeConversation, useAssistantOp, useModels } from '../../api/assistant';
import { call, toApiError, type ApiError } from '../../api/client';
import type { AssistantStatus, Conversation, OpInput } from '../../api/types';
import type { Selection } from '../../lib/workbench';
import { Button, ErrorPanel, IconButton, cx } from '../../ui/primitives';
import {
  ATTACH_ACCEPT,
  AttachmentChips,
  MAX_ATTACHMENTS,
  attachProblem,
  attachedPrompt,
  dragHasFiles,
  isPicture,
  readBase64,
  type PendingAttachment,
} from './Attachments';
import { contextItems, contextLine, messageFields } from './context';
import { ModeSwitch } from './ModeSwitch';
import { loadDraft, newConversation, openConversation, promptHistory, rememberPrompt, saveDraft, setPanel, getPanel } from './store';

/*
 * The input. It never blocks: while Claude works, messages queue and are sent
 * in order. Stop interrupts the current step. ↑/↓ recall earlier prompts, the
 * draft is kept per conversation, ⌘↵ (Ctrl+Enter) sends, and slash commands
 * change the conversation. Below the text there is only one quiet line: what
 * the message is about (with a way to change it) and the buttons. Pictures and
 * PDFs go with a message too (the paperclip, a drop or a paste, D166): each is
 * stored first and shows as a chip until the message is sent.
 */

export const COMMANDS: { name: string; args?: string; help: string }[] = [
  { name: 'fresh', help: 'Start a new conversation' },
  { name: 'model', args: '<name>', help: 'Use another model (for example sonnet, opus or default)' },
  { name: 'effort', args: '<level>', help: 'low, medium, high, xhigh, max or default' },
  { name: 'limit', args: '<millions>', help: 'Token limit for this conversation, in millions (for example /limit 80)' },
  { name: 'budget', args: '<dollars>', help: 'Spending limit for this conversation (API key only)' },
  { name: 'title', args: '<text>', help: 'Rename this conversation' },
  { name: 'raw', help: 'Show or hide the raw prompts and tool details' },
];

const EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max', 'default'];
type AskEffort = OpInput<'assistant.ask'>['effort'];

export interface PendingSettings {
  model?: string;
  effort?: string;
  budgetUsd?: number;
  tokenLimitMillions?: number;
}

export function Composer({
  conversation,
  status,
  sel,
  pending,
  setPending,
  onTyped,
  fill,
  autoFocus,
}: {
  conversation?: Conversation;
  status: AssistantStatus;
  sel: Selection;
  /** Settings chosen with slash commands before the first message. */
  pending: PendingSettings;
  setPending: (p: PendingSettings) => void;
  onTyped?: (hasText: boolean) => void;
  /** Text to put in the input (a suggestion that was picked); changes when the nonce changes. */
  fill?: { text: string; nonce: number };
  autoFocus?: boolean;
}) {
  const id = useId();
  const qc = useQueryClient();
  const convId = conversation?.id;
  const [draft, setDraftState] = useState(() => loadDraft(convId));
  const [excluded, setExcluded] = useState<Set<string>>(new Set());
  const [note, setNote] = useState<{ text: string; tone: 'ok' | 'danger' } | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [histIdx, setHistIdx] = useState(-1);
  const [showContext, setShowContext] = useState(false);
  const [files, setFiles] = useState<PendingAttachment[]>([]);
  const [fileNote, setFileNote] = useState<string | null>(null);
  const [dropping, setDropping] = useState(false);
  const dropDepth = useRef(0);
  const filesRef = useRef(files);
  filesRef.current = files;
  const fileInput = useRef<HTMLInputElement>(null);
  const ta = useRef<HTMLTextAreaElement>(null);
  const ask = useAssistantOp('assistant.ask');
  const cont = useAssistantOp('assistant.continue');
  const interrupt = useAssistantOp('assistant.interrupt');
  const configure = useAssistantOp('assistant.configure');
  const rename = useAssistantOp('assistant.rename');
  const models = useModels(draft.startsWith('/model'));
  // A conversation keeps its project (D185): the line says what this message carries.
  const convProjectId = conversation?.projectId;
  const convProjectName = conversation?.projectName;
  const hasConv = !!conversation;
  const items = useMemo(
    () => contextItems(sel, hasConv ? { projectId: convProjectId, projectName: convProjectName } : undefined),
    [sel, hasConv, convProjectId, convProjectName],
  );
  const line = contextLine(items, excluded);
  const working = conversation?.state === 'working';
  const queued = conversation?.queue?.length ?? 0;
  const sending = ask.isPending || cont.isPending;
  const attaching = files.some((f) => f.state === 'uploading');

  // Each conversation has its own draft.
  useEffect(() => {
    setDraftState(loadDraft(convId));
    setError(null);
    setNote(null);
    setHistIdx(-1);
  }, [convId]);
  // Previews made on this computer are let go when the box goes away.
  useEffect(() => () => filesRef.current.forEach((f) => f.preview && URL.revokeObjectURL(f.preview)), []);
  useEffect(() => {
    const t = setTimeout(() => saveDraft(convId, draft), 250);
    return () => clearTimeout(t);
  }, [convId, draft]);
  useEffect(() => onTyped?.(draft.trim().length > 0 || files.length > 0), [draft, files.length, onTyped]);
  useEffect(() => {
    if (!fill) return;
    setDraftState(fill.text);
    requestAnimationFrame(() => {
      const el = ta.current;
      if (!el) return;
      el.focus();
      el.setSelectionRange(fill.text.length, fill.text.length);
    });
  }, [fill]);
  useEffect(() => {
    if (autoFocus) ta.current?.focus();
  }, [autoFocus, convId]);
  useEffect(() => {
    if (!note) return;
    const t = setTimeout(() => setNote(null), 4000);
    return () => clearTimeout(t);
  }, [note]);
  // Grow with the text (up to a limit).
  useEffect(() => {
    const el = ta.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${Math.min(220, Math.max(44, el.scrollHeight))}px`;
  }, [draft]);

  const setDraft = (v: string) => {
    setDraftState(v);
    setHistIdx(-1);
  };

  const fail = (err: unknown) => setError(toApiError(err));

  /* ---------- Attachments: stored as soon as they are added, sent with the message ---------- */

  const dropFile = (key: string) =>
    setFiles((list) => {
      const gone = list.find((f) => f.key === key);
      if (gone?.preview) URL.revokeObjectURL(gone.preview);
      return list.filter((f) => f.key !== key);
    });

  const attach = (list: File[]) => {
    if (!list.length) return;
    const problems: string[] = [];
    const added: { item: PendingAttachment; file: File }[] = [];
    let room = MAX_ATTACHMENTS - filesRef.current.length;
    for (const f of list) {
      const why = attachProblem(f);
      if (why) {
        problems.push(why);
        continue;
      }
      if (room <= 0) {
        problems.push(`A message takes ${MAX_ATTACHMENTS} files at most.`);
        break;
      }
      room--;
      const item: PendingAttachment = {
        key: `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`,
        name: f.name || 'picture',
        size: f.size,
        mime: f.type,
        preview: isPicture(f.type) || /\.(png|jpe?g|webp|gif|svg)$/i.test(f.name) ? URL.createObjectURL(f) : undefined,
        state: 'uploading',
      };
      added.push({ item, file: f });
    }
    setFileNote(problems.length ? [...new Set(problems)].join(' ') : null);
    if (!added.length) return;
    setFiles((cur) => [...cur, ...added.map((a) => a.item)]);
    for (const { item, file } of added) {
      void readBase64(file)
        .then((data) => call('assistant.attach', { name: item.name, data }))
        .then((out) => setFiles((cur) => cur.map((x) => (x.key === item.key ? { ...x, state: 'ready', file: out.file, mime: out.file.mime } : x))))
        .catch((e: unknown) => {
          dropFile(item.key);
          setFileNote(`“${item.name}” was not attached: ${toApiError(e).message}`);
        });
    }
  };

  const onDrag = (e: DragEvent<HTMLDivElement>) => {
    if (!dragHasFiles(e.dataTransfer)) return;
    // The message box takes the files; the page's own drop (footage into the project) doesn't.
    e.preventDefault();
    e.stopPropagation();
    if (e.type === 'dragenter') {
      dropDepth.current += 1;
      setDropping(true);
    } else if (e.type === 'dragleave') {
      dropDepth.current = Math.max(0, dropDepth.current - 1);
      if (dropDepth.current === 0) setDropping(false);
    } else if (e.type === 'dragover') {
      e.dataTransfer.dropEffect = 'copy';
    } else if (e.type === 'drop') {
      dropDepth.current = 0;
      setDropping(false);
      attach(Array.from(e.dataTransfer.files ?? []));
      ta.current?.focus();
    }
  };

  const onPaste = (e: ClipboardEvent<HTMLTextAreaElement>) => {
    const pasted = Array.from(e.clipboardData?.files ?? []);
    if (!pasted.length) return;
    // A copied picture (a screenshot, a picture from a page): attach it instead of pasting its name.
    e.preventDefault();
    attach(pasted);
  };

  const runCommand = async (text: string): Promise<boolean> => {
    const [cmd, ...rest] = text.slice(1).split(/\s+/);
    const arg = rest.join(' ').trim();
    switch ((cmd ?? '').toLowerCase()) {
      case 'fresh':
      case 'new':
        newConversation();
        setPending({});
        setNote({ text: 'New conversation', tone: 'ok' });
        return true;
      case 'raw':
        setPanel({ raw: !getPanel().raw });
        setNote({ text: getPanel().raw ? 'Showing raw prompts and tool details' : 'Raw details hidden', tone: 'ok' });
        return true;
      case 'limit': {
        const v = Number(arg.replace(/m$/i, '').replace(/,/g, ''));
        if (!arg || !Number.isFinite(v) || v < 1 || v > 1000) {
          setNote({ text: 'Give a limit in millions of tokens, between 1 and 1,000, for example /limit 80', tone: 'danger' });
          return false;
        }
        if (!conversation) {
          setPending({ ...pending, tokenLimitMillions: v });
          setNote({ text: `Limit for the new conversation: ${v}M tokens`, tone: 'ok' });
          return true;
        }
        await configure.mutateAsync({ id: conversation.id, tokenLimitMillions: v });
        setNote({ text: `Limit set to ${v}M tokens`, tone: 'ok' });
        return true;
      }
      case 'budget': {
        if (status?.billing === 'plan') {
          setNote({ text: 'Your Claude plan has no spending limit: nothing is billed per message. Use /limit to change the token limit.', tone: 'danger' });
          return false;
        }
        const v = Number(arg.replace(/^\$/, ''));
        if (!arg || !Number.isFinite(v) || v < 0.05 || v > 100) {
          setNote({ text: 'Give a limit between $0.05 and $100, for example /budget 5', tone: 'danger' });
          return false;
        }
        if (!conversation) {
          setPending({ ...pending, budgetUsd: v });
          setNote({ text: `Limit for the new conversation: $${v.toFixed(2)}`, tone: 'ok' });
          return true;
        }
        await configure.mutateAsync({ id: conversation.id, budgetUsd: v });
        setNote({ text: `Limit set to $${v.toFixed(2)}`, tone: 'ok' });
        return true;
      }
      case 'model': {
        if (!arg) {
          setNote({ text: 'Say which model, for example /model sonnet (or /model default)', tone: 'danger' });
          return false;
        }
        const cat = models.data?.models ?? [];
        const known = arg === 'default' || cat.length === 0 || cat.some((m) => m.value === arg || m.resolvedModel === arg);
        if (!known) {
          setNote({ text: `Claude Code does not offer “${arg}”. Try ${cat.map((m) => m.value).slice(0, 5).join(', ')}.`, tone: 'danger' });
          return false;
        }
        if (!conversation) {
          setPending({ ...pending, model: arg });
          setNote({ text: `Model for the new conversation: ${arg}`, tone: 'ok' });
          return true;
        }
        await configure.mutateAsync({ id: conversation.id, model: arg });
        setNote({ text: arg === 'default' ? 'Using the default model' : `Model set to ${arg}`, tone: 'ok' });
        return true;
      }
      case 'effort': {
        const v = arg.toLowerCase();
        if (!EFFORTS.includes(v)) {
          setNote({ text: 'Effort is low, medium, high, xhigh, max or default', tone: 'danger' });
          return false;
        }
        if (!conversation) {
          setPending({ ...pending, effort: v });
          setNote({ text: `Effort for the new conversation: ${v}`, tone: 'ok' });
          return true;
        }
        await configure.mutateAsync({ id: conversation.id, effort: v });
        setNote({ text: v === 'default' ? 'Using the default effort' : `Effort set to ${v}`, tone: 'ok' });
        return true;
      }
      case 'title': {
        if (!conversation) {
          setNote({ text: 'Send a first message, then name the conversation', tone: 'danger' });
          return false;
        }
        if (!arg) {
          setNote({ text: 'Give the new name, for example /title Spring launch cut', tone: 'danger' });
          return false;
        }
        await rename.mutateAsync({ id: conversation.id, title: arg.slice(0, 120) });
        setNote({ text: 'Renamed', tone: 'ok' });
        return true;
      }
      case 'help':
      case '?':
        setNote({ text: COMMANDS.map((c) => '/' + c.name).join('  '), tone: 'ok' });
        return true;
    }
    setNote({ text: `There is no /${cmd} command. Try ${COMMANDS.map((c) => '/' + c.name).join(', ')}.`, tone: 'danger' });
    return false;
  };

  const send = async () => {
    const text = draft.trim();
    if ((!text && !files.length) || sending) return;
    if (attaching) {
      setFileNote('One moment: the files are still being attached.');
      return;
    }
    setError(null);
    try {
      if (text.startsWith('/')) {
        if (await runCommand(text)) setDraft('');
        return;
      }
      const attached = files.flatMap((f) => (f.file ? [f.file] : []));
      const fields = { ...messageFields(sel, text || attachedPrompt(attached), excluded), ...(attached.length ? { attachments: attached } : {}) };
      if (text) rememberPrompt(text);
      if (!conversation) {
        const out = await ask.mutateAsync({
          ...fields,
          model: pending.model,
          budgetUsd: pending.budgetUsd,
          tokenLimitMillions: pending.tokenLimitMillions,
          effort: pending.effort as AskEffort,
        });
        primeConversation(out.conversation);
        saveDraft(undefined, '');
        setDraftState('');
        setPending({});
        openConversation(out.conversation.id);
      } else {
        await cont.mutateAsync({ id: conversation.id, ...fields });
        setDraftState('');
        saveDraft(conversation.id, '');
      }
      // Sent: from now on the thread shows them.
      files.forEach((f) => f.preview && URL.revokeObjectURL(f.preview));
      setFiles([]);
      setFileNote(null);
    } catch (err) {
      fail(err);
      if ((err as ApiError)?.code === 'E_CONFLICT') void qc.invalidateQueries({ queryKey: ['assistant'] });
    }
  };

  const stop = async () => {
    if (!conversation) return;
    try {
      const out = await interrupt.mutateAsync({ id: conversation.id });
      // What the person had typed comes back to the box; news for Claude doesn't.
      const typed = (out.requeued ?? []).filter((q) => !q.note);
      if (typed.length && !draft.trim()) setDraftState(typed.map((q) => q.prompt).join('\n\n'));
    } catch (err) {
      fail(err);
    }
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      void send();
      return;
    }
    const el = e.currentTarget;
    if (e.key === 'ArrowUp' && !e.shiftKey && el.selectionStart === 0 && el.selectionEnd === 0) {
      const h = promptHistory();
      if (!h.length) return;
      e.preventDefault();
      const next = histIdx < 0 ? h.length - 1 : Math.max(0, histIdx - 1);
      setHistIdx(next);
      setDraftState(h[next]!);
    } else if (e.key === 'ArrowDown' && !e.shiftKey && histIdx >= 0 && el.selectionStart === el.value.length) {
      const h = promptHistory();
      e.preventDefault();
      const next = histIdx + 1;
      if (next >= h.length) {
        setHistIdx(-1);
        setDraftState('');
      } else {
        setHistIdx(next);
        setDraftState(h[next]!);
      }
    } else if (e.key === 'Tab' && draft.startsWith('/') && !draft.includes(' ')) {
      const m = COMMANDS.find((c) => c.name.startsWith(draft.slice(1).toLowerCase()));
      if (m) {
        e.preventDefault();
        setDraftState('/' + m.name + (m.args ? ' ' : ''));
      }
    } else if (e.key === 'Escape' && draft.startsWith('/')) {
      e.preventDefault();
      e.stopPropagation();
      setDraftState('');
    }
  };

  const slash = draft.startsWith('/') && !draft.includes(' ') ? COMMANDS.filter((c) => c.name.startsWith(draft.slice(1).toLowerCase())) : [];
  const pendingLine = [
    pending.model && `model ${pending.model}`,
    pending.effort && `${pending.effort} effort`,
    pending.tokenLimitMillions && `limit ${pending.tokenLimitMillions}M tokens`,
    pending.budgetUsd && `spending limit $${pending.budgetUsd.toFixed(2)}`,
  ]
    .filter(Boolean)
    .join(' · ');
  const settingsLine = conversation ? [conversation.model && conversation.model, conversation.effort && `${conversation.effort} effort`].filter(Boolean).join(' · ') : pendingLine;
  const workspaceWide = conversation ? !conversation.projectId : !sel.projectId;
  const quiet = [workspaceWide ? ['Whole workspace', line].filter(Boolean).join(' · ') : line, settingsLine].filter(Boolean).join(' · ');
  const placeholder = working
    ? 'Claude is working. Type your next message; it goes out when Claude is done.'
    : conversation
      ? 'Reply to Claude'
      : sel.projectName
        ? `Ask about ${sel.projectName}, or describe a change`
        : 'Ask about your footage, or describe a change';

  return (
    <div className="relative" onDragEnter={onDrag} onDragOver={onDrag} onDragLeave={onDrag} onDrop={onDrag} data-composer>
      {error ? <ErrorPanel error={error} compact className="mb-2" /> : null}
      {fileNote ? (
        <p role="status" className="mb-2 flex items-start gap-1.5 text-[12px] leading-relaxed text-danger" data-attach-note>
          <WarningCircle size={13} weight="fill" className="mt-[2px] shrink-0" aria-hidden />
          <span className="min-w-0">{fileNote}</span>
        </p>
      ) : null}
      {note ? (
        <p role="status" className={cx('mb-2 flex items-center gap-1.5 text-[12px]', note.tone === 'danger' ? 'text-danger' : 'text-muted')}>
          {note.tone === 'ok' ? <Check size={12} aria-hidden /> : null}
          {note.text}
        </p>
      ) : null}
      {slash.length ? (
        <ul role="listbox" aria-label="Commands" className="absolute bottom-full left-0 z-10 mb-2 w-[min(100%,360px)] overflow-hidden rounded-[12px] bg-raised py-1 shadow-[var(--shadow-float)] hairline">
          {slash.map((c) => (
            <li key={c.name} role="option" aria-selected={false}>
              <button
                type="button"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => {
                  setDraftState('/' + c.name + (c.args ? ' ' : ''));
                  ta.current?.focus();
                }}
                className="flex w-full items-baseline gap-2 px-3 py-1.5 text-left hover:bg-hover"
              >
                <span className="tc text-[12.5px] text-fg">/{c.name}</span>
                {c.args ? <span className="tc text-[11.5px] text-subtle">{c.args}</span> : null}
                <span className="ml-auto truncate pl-2 text-[11.5px] text-subtle">{c.help}</span>
              </button>
            </li>
          ))}
        </ul>
      ) : null}
      <div
        className={cx(
          'relative rounded-[14px] bg-panel hairline transition-shadow focus-within:shadow-[inset_0_0_0_1px_var(--accent-line)]',
          dropping && 'shadow-[inset_0_0_0_2px_var(--accent)]',
        )}
      >
        {dropping ? (
          <div
            className="pointer-events-none absolute inset-0 z-10 grid place-items-center rounded-[14px] bg-[color-mix(in_oklab,var(--bg-panel)_88%,transparent)] px-4 text-center text-[12.5px] font-medium text-accent-text"
            data-attach-drop
          >
            Drop pictures or PDFs to send them with your message
          </div>
        ) : null}
        <AttachmentChips items={files} onRemove={dropFile} />
        <label htmlFor={id} className="sr-only">
          Message to Claude
        </label>
        <textarea
          id={id}
          ref={ta}
          rows={1}
          value={draft}
          maxLength={8000}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={onKey}
          onPaste={onPaste}
          placeholder={placeholder}
          aria-describedby={`${id}-ctx`}
          className="block max-h-[220px] min-h-[44px] w-full resize-none bg-transparent px-3.5 pt-3 text-[13.5px] leading-relaxed text-fg outline-none placeholder:text-subtle"
        />
        <div className="flex items-center gap-2 px-2.5 pb-2 pt-1">
          <IconButton size="sm" label="Attach pictures or PDFs" onClick={() => fileInput.current?.click()} disabled={files.length >= MAX_ATTACHMENTS} className="-ml-1" data-attach-open>
            <Paperclip size={15} />
          </IconButton>
          <input
            ref={fileInput}
            type="file"
            multiple
            accept={ATTACH_ACCEPT}
            className="hidden"
            tabIndex={-1}
            aria-hidden
            data-attach-input
            onChange={(e) => {
              const list = Array.from(e.target.files ?? []);
              e.target.value = '';
              attach(list);
            }}
          />
          <div className="relative min-w-0 flex-1">
            <button
              type="button"
              id={`${id}-ctx`}
              onClick={() => setShowContext((v) => !v)}
              aria-expanded={showContext}
              aria-haspopup="dialog"
              className="block max-w-full truncate rounded-[6px] px-1 py-0.5 text-left text-[11.5px] text-subtle hover:text-muted"
              title="What this message is about (click to change)"
            >
              {quiet || 'Nothing selected'}
              {items.length ? <span className="ml-1.5 text-accent-text">change</span> : null}
            </button>
            {showContext && items.length ? (
              <ContextPicker
                items={items}
                excluded={excluded}
                onToggle={(k) =>
                  setExcluded((s) => {
                    const n = new Set(s);
                    if (n.has(k)) n.delete(k);
                    else n.add(k);
                    return n;
                  })
                }
                onClose={() => setShowContext(false)}
              />
            ) : null}
          </div>
          {queued ? <span className="tc shrink-0 text-[11.5px] text-subtle">{queued} waiting to send</span> : null}
          <ModeSwitch />
          {working ? (
            <Button size="sm" variant="secondary" icon={<StopCircle size={14} />} loading={interrupt.isPending} onClick={() => void stop()} title="Stop what Claude is doing now">
              Stop
            </Button>
          ) : null}
          <Button
            size="sm"
            variant="primary"
            icon={<ArrowUp size={13} weight="bold" />}
            loading={sending}
            disabled={(!draft.trim() && !files.length) || sending || attaching}
            onClick={() => void send()}
            title={attaching ? 'One moment: the files are still being attached' : working ? 'Send it when Claude is done (⌘↵)' : 'Send (⌘↵)'}
            aria-label={working ? 'Send it when Claude is done' : 'Send message'}
          >
            {working ? 'Send next' : 'Send'}
          </Button>
        </div>
      </div>
      <span className="sr-only" aria-live="polite">
        {status.available ? '' : status.reason}
      </span>
    </div>
  );
}

function ContextPicker({
  items,
  excluded,
  onToggle,
  onClose,
}: {
  items: ReturnType<typeof contextItems>;
  excluded: Set<string>;
  onToggle: (k: string) => void;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    ref.current?.querySelector<HTMLElement>('input')?.focus();
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose();
    };
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        onClose();
      }
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey, true);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey, true);
    };
  }, [onClose]);
  return (
    <div ref={ref} role="dialog" aria-label="What this message is about" className="absolute bottom-full left-0 z-10 mb-2 w-[min(320px,80vw)] rounded-[12px] bg-raised p-2 shadow-[var(--shadow-float)] hairline">
      <p className="px-1.5 pb-1.5 text-[11.5px] text-subtle">Sent with your message</p>
      {items.map((c) => {
        const on = c.fixed || !excluded.has(c.key);
        return (
          <label
            key={c.key}
            className={cx('flex items-center gap-2.5 rounded-[8px] px-1.5 py-1.5', c.fixed ? 'cursor-default' : 'cursor-pointer hover:bg-hover')}
            title={c.fixed ? 'A conversation stays about the project it started in. Start a new conversation for another one.' : undefined}
          >
            <input type="checkbox" checked={on} disabled={c.fixed} onChange={() => onToggle(c.key)} className="h-3.5 w-3.5 accent-[var(--accent)]" />
            <span className="min-w-0 flex-1">
              <span className="block text-[11.5px] text-subtle">{c.label}</span>
              <span className="block truncate text-[12.5px] text-fg">{c.value}</span>
            </span>
          </label>
        );
      })}
    </div>
  );
}
