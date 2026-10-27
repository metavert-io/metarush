import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { ArrowsLeftRight, ChatCircleDots, CheckCircle, GitDiff, Sparkle, WarningCircle, X } from '@phosphor-icons/react';
import { call } from '../../../api/client';
import type { AssistantAction, FitLineView, FittedOut, NarrationOut } from '../../../api/types';
import { openAskRun } from '../../../lib/workbench';
import { Button, cx, Spinner } from '../../../ui/primitives';
import { useToast } from '../../../ui/Toast';
import { checkApplied, errorText, useChange, useRefreshCut } from './api';

/*
 * Script versions (D86). A pasted script is used as-is, or fitted to the video
 * by the person's own Claude Code (read-only, through the Claude Code panel):
 * Claude proposes lines tied to the right moments and trimmed to fit, every
 * change explained. The person sees both versions side by side, takes Claude's
 * lines one by one, and can switch versions at any time; the original script
 * is kept exactly as it was given.
 */

export type ScriptUse = 'as-is' | 'fit';

/** "How should MetaRush use it?" after a script is pasted. */
export function ScriptChoice({ value, onChange }: { value: ScriptUse; onChange: (v: ScriptUse) => void }) {
  const opts: { v: ScriptUse; title: string; body: string; icon: ReactNode }[] = [
    {
      v: 'as-is',
      title: 'Use my script as-is',
      body: 'Each paragraph is a line. No timing is guessed: record it while the video plays and your reading sets where each line goes.',
      icon: <CheckCircle size={16} />,
    },
    {
      v: 'fit',
      title: 'Fit it to the video with Claude',
      body: 'Claude watches the cut and suggests where each line starts and how to trim it to fit, keeping your words and style. You review every change; your original is kept.',
      icon: <Sparkle size={16} />,
    },
  ];
  return (
    <fieldset className="mt-3" data-script-choice>
      <legend className="mb-2 text-[12.5px] font-medium text-muted">How should MetaRush use it?</legend>
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2" role="radiogroup">
        {opts.map((o) => (
          <button
            key={o.v}
            type="button"
            role="radio"
            aria-checked={value === o.v}
            onClick={() => onChange(o.v)}
            className={cx(
              'flex items-start gap-2.5 rounded-[12px] p-3 text-left transition-colors duration-200',
              value === o.v ? 'bg-accent-soft shadow-[inset_0_0_0_1.5px_var(--accent-line)]' : 'bg-raised hairline hover:bg-hover',
            )}
          >
            <span className={cx('mt-[1px] grid h-6 w-6 shrink-0 place-items-center rounded-full', value === o.v ? 'bg-accent text-accent-ink' : 'bg-hover text-subtle')} aria-hidden>
              {o.icon}
            </span>
            <span className="min-w-0">
              <span className="block text-[13.5px] font-semibold text-fg">{o.title}</span>
              <span className="mt-0.5 block text-[12px] leading-relaxed text-muted">{o.body}</span>
            </span>
          </button>
        ))}
      </div>
    </fieldset>
  );
}

/**
 * Starts the fitting: the person's Claude Code reads the cut and proposes a
 * version (read-only); the Claude Code panel opens on that conversation.
 */
export async function askFit(revisionId: string): Promise<string> {
  const out = await call('voiceover.askFit', { revisionId });
  openAskRun(out.conversation.id);
  return out.conversation.id;
}

/** Waits for Claude's fitted script in a conversation: its card, once there. */
export function useFitCard(convId: string | undefined, actionId?: string): { card?: AssistantAction; working: boolean; problem?: string } {
  const [state, setState] = useState<{ card?: AssistantAction; working: boolean; problem?: string }>({ working: !!convId });
  useEffect(() => {
    if (!convId) {
      setState({ working: false });
      return;
    }
    let stop = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      try {
        const cv = await call('assistant.show', { id: convId, events: false });
        if (stop) return;
        const cards = (cv.actions ?? []).filter((a) => a.kind === 'voiceover script fit' && a.fit && (!actionId || a.id === actionId));
        const card = cards.at(-1);
        const working = cv.state === 'working';
        setState({ card, working, problem: !card && !working ? 'Claude finished without a fitted script. Open the conversation to see what it said.' : undefined });
        if (!card && working) timer = setTimeout(() => void poll(), 2000);
        else if (card && card.state === 'pending') timer = setTimeout(() => void poll(), 4000);
      } catch (e) {
        if (!stop) setState({ working: false, problem: errorText(e) });
      }
    };
    void poll();
    return () => {
      stop = true;
      if (timer) clearTimeout(timer);
    };
  }, [convId, actionId]);
  return state;
}

/** While Claude fits the script: a quiet line with the way to the conversation. */
export function FitWaiting({ convId, problem, onClose }: { convId: string; problem?: string; onClose: () => void }) {
  return (
    <div className="mb-4 flex flex-wrap items-start gap-3 rounded-[14px] bg-accent-soft px-4 py-3" role="status" data-fit-waiting>
      {problem ? <WarningCircle size={18} className="mt-[1px] shrink-0 text-accent-text" aria-hidden /> : <Spinner size={16} className="mt-[2px] shrink-0 text-accent-text" />}
      <p className="min-w-0 flex-1 text-[13px] leading-relaxed text-fg">
        {problem ?? 'Claude is watching your cut and fitting your script to it (it only reads; nothing changes until you choose). Your original script is in use meanwhile.'}
      </p>
      <Button size="sm" variant="ghost" icon={<ChatCircleDots size={14} />} onClick={() => openAskRun(convId)}>
        Open the conversation
      </Button>
      <button type="button" onClick={onClose} aria-label="Hide" className="grid h-7 w-7 place-items-center rounded-[8px] text-subtle hover:bg-hover hover:text-fg">
        <X size={14} />
      </button>
    </div>
  );
}

/**
 * Both versions side by side, line by line: the person's words and Claude's
 * (what changed highlighted), where each starts, and Claude's reason. For a
 * proposal (a card) the choices are made here and used at once; for the
 * fitted version already in the cut each choice is its own small change.
 */
export function FitReview({ fit, card, convId, revisionId, onDone }: {
  fit: FittedOut;
  /** A proposal waiting on a card in a conversation. */
  card?: AssistantAction;
  convId?: string;
  revisionId: string;
  onDone: () => void;
}) {
  const toast = useToast();
  const refresh = useRefreshCut();
  const change = useChange();
  const [keep, setKeep] = useState<Set<string>>(() => new Set(fit.lines.filter((l) => l.kept).map((l) => l.beatId)));
  const [busy, setBusy] = useState<string | null>(null);
  const proposal = !!card && card.state === 'pending';
  const changed = fit.lines.filter((l) => l.change !== 'none');
  const taking = changed.filter((l) => !keep.has(l.beatId) && !l.locked).length;
  const toggle = async (l: FitLineView, mine: boolean) => {
    if (proposal) {
      setKeep((k) => {
        const n = new Set(k);
        if (mine) n.add(l.beatId);
        else n.delete(l.beatId);
        return n;
      });
      return;
    }
    setBusy(l.beatId);
    const ok = await change(
      async () => checkApplied((await call('voiceover.fitted', { revisionId, ...(mine ? { keep: [l.beatId] } : { use: [l.beatId] }), apply: true })).applied),
      mine ? `Line ${l.line} keeps your words` : `Line ${l.line} uses Claude's version`,
      'That line was not changed',
    );
    setBusy(null);
    if (ok !== null)
      setKeep((k) => {
        const n = new Set(k);
        if (mine) n.add(l.beatId);
        else n.delete(l.beatId);
        return n;
      });
  };
  const use = async (dismiss: boolean) => {
    if (!card || !convId) return;
    setBusy(dismiss ? 'dismiss' : 'use');
    try {
      const out = await call('assistant.act', { id: convId, actionId: card.id, dismiss, keep: [...keep] });
      if (out.action.state === 'failed') throw new Error(out.action.error || 'That did not work.');
      refresh();
      toast(
        dismiss
          ? { tone: 'success', title: 'Kept your own script', body: "Claude's version stays in the conversation if you change your mind." }
          : { tone: 'success', title: "Claude's fitted script is in use", body: `${taking} of ${changed.length} changed lines from Claude; your original is kept and you can switch back any time.` },
      );
      onDone();
    } catch (e) {
      toast({ tone: 'error', title: 'That did not work', body: errorText(e) });
    } finally {
      setBusy(null);
    }
  };
  return (
    <section className="rounded-[14px] bg-panel p-4 hairline sm:p-5" aria-label="Your script and Claude's fitted version" data-fit-review={proposal ? 'proposal' : 'cut'}>
      <div className="flex flex-wrap items-start gap-3">
        <span className="grid h-9 w-9 shrink-0 place-items-center rounded-[10px] bg-accent-soft text-accent-text" aria-hidden>
          <GitDiff size={18} />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="text-[15px] font-semibold tracking-[-0.01em]">{proposal ? 'Claude fitted your script to the video' : 'Your script and Claude’s fitted version'}</h3>
          <p className="mt-0.5 text-[12.5px] leading-relaxed text-muted">
            {changed.length === 0
              ? 'Claude kept every line as you wrote it.'
              : `${changed.length} of ${fit.lines.length} lines changed. Take Claude's version line by line; your original script is kept either way.`}
          </p>
        </div>
        <button type="button" onClick={onDone} aria-label="Close the comparison" className="grid h-8 w-8 place-items-center rounded-[9px] text-subtle hover:bg-hover hover:text-fg">
          <X size={15} />
        </button>
      </div>
      {fit.note ? <p className="mt-3 rounded-[12px] bg-hover/60 px-3 py-2 text-[12.5px] leading-relaxed text-muted">Claude: {fit.note}</p> : null}
      {fit.warnings?.length ? (
        <ul className="mt-2 space-y-1">
          {fit.warnings.map((w) => (
            <li key={w} className="flex gap-1.5 text-[12px] text-accent-text">
              <WarningCircle size={13} className="mt-[2px] shrink-0" /> {w}
            </li>
          ))}
        </ul>
      ) : null}
      <ol className="mt-4 space-y-2.5" aria-label="Line by line">
        {fit.lines.map((l) => (
          <FitLine key={l.beatId} l={l} mine={keep.has(l.beatId) || !!l.locked} busy={busy === l.beatId} onToggle={(m) => void toggle(l, m)} />
        ))}
      </ol>
      {proposal ? (
        <div className="mt-4 flex flex-wrap items-center gap-2">
          <Button variant="primary" loading={busy === 'use'} disabled={!!busy} onClick={() => void use(false)} data-fit-use>
            {taking === changed.length ? "Use Claude's version" : `Use it (${taking} of ${changed.length} changes)`}
          </Button>
          <Button variant="ghost" loading={busy === 'dismiss'} disabled={!!busy} onClick={() => void use(true)}>
            Keep my script
          </Button>
          <span className="ml-auto text-[12px] text-subtle">You can switch between the two versions any time.</span>
        </div>
      ) : null}
    </section>
  );
}

function FitLine({ l, mine, busy, onToggle }: { l: FitLineView; mine: boolean; busy: boolean; onToggle: (mine: boolean) => void }) {
  if (l.change === 'none')
    return (
      <li className="flex items-start gap-2.5 rounded-[12px] px-3 py-2 text-[13px] text-muted hairline" data-fit-line={l.beatId} data-change="none">
        <span className="tc mt-[1px] grid h-5 min-w-5 place-items-center rounded-full bg-hover px-1 text-[11px] font-semibold">{l.line}</span>
        <span className="min-w-0 flex-1">
          {l.original} <span className="text-subtle">· unchanged</span>
        </span>
      </li>
    );
  return (
    <li className={cx('rounded-[12px] p-3 hairline', mine ? 'bg-raised' : 'bg-accent-soft/40')} data-fit-line={l.beatId} data-change={l.change} data-kept={mine ? 'mine' : 'claude'}>
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <span className="tc grid h-5 min-w-5 place-items-center rounded-full bg-hover px-1 text-[11px] font-semibold text-muted">{l.line}</span>
        <span className="text-[12px] font-medium text-subtle">{l.change === 'timing' ? 'Starts somewhere else' : l.change === 'words' ? 'Different words' : 'Different words and start'}</span>
        <div className="ml-auto inline-flex rounded-full bg-hover p-0.5" role="radiogroup" aria-label={`Line ${l.line}: which version`}>
          {[
            { v: false, label: "Claude's" },
            { v: true, label: 'Mine' },
          ].map((o) => (
            <button
              key={o.label}
              type="button"
              role="radio"
              aria-checked={mine === o.v}
              disabled={busy || l.locked}
              onClick={() => onToggle(o.v)}
              className={cx('h-7 rounded-full px-3 text-[12px] font-medium transition-colors disabled:opacity-60', mine === o.v ? 'bg-panel text-fg shadow-[var(--shadow-soft)]' : 'text-muted hover:text-fg')}
            >
              {o.label}
            </button>
          ))}
        </div>
        {busy ? <Spinner size={12} /> : null}
      </div>
      <div className="grid grid-cols-1 gap-2 md:grid-cols-2">
        <Version title="Yours" words={l.words} side="del" placement={l.originalPlacement} dim={!mine} />
        <Version title="Claude's" words={l.words} side="add" placement={l.placement} dim={mine} />
      </div>
      {l.reason ? <p className="mt-2 text-[12px] leading-relaxed text-muted">Why: {l.reason}</p> : null}
      {l.locked ? <p className="mt-1 text-[12px] text-subtle">This line is locked, so it stays as it is.</p> : null}
    </li>
  );
}

/** One side of a line: its words with what only this side has highlighted, and where it starts. */
function Version({ title, words, side, placement, dim }: { title: string; words: FitLineView['words']; side: 'del' | 'add'; placement: string; dim: boolean }) {
  const shown = useMemo(() => words.filter((w) => w.op === 'same' || w.op === side), [words, side]);
  return (
    <div className={cx('rounded-[10px] bg-panel px-3 py-2 hairline transition-opacity duration-200', dim && 'opacity-60')} data-side={side === 'del' ? 'mine' : 'claude'}>
      <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.06em] text-subtle">{title}</div>
      <p className="text-[14px] leading-relaxed text-fg">
        {shown.map((w, i) => (
          <span key={i}>
            {i ? ' ' : ''}
            {w.op === 'same' ? (
              side === 'del' && w.was ? w.was : w.text
            ) : (
              <mark className={cx('rounded-[4px] px-0.5', side === 'del' ? 'bg-danger-soft text-danger line-through decoration-1' : 'bg-ok-soft text-ok')} data-word={w.op}>
                {w.text}
              </mark>
            )}
          </span>
        ))}
      </p>
      <p className="mt-1 text-[11.5px] text-subtle">Starts {placement}</p>
    </div>
  );
}

/** The script version in use, and the way to switch or compare (when Claude fitted the script). */
export function VersionSwitch({ show, revisionId, onCompare }: { show: NarrationOut; revisionId: string; onCompare: () => void }) {
  const change = useChange();
  const [busy, setBusy] = useState(false);
  const sv = show.script;
  if (!sv.fitted) return null;
  const set = async (version: 'original' | 'fitted') => {
    if (version === sv.active) return;
    setBusy(true);
    await change(
      async () => {
        const out = await call('voiceover.use', { revisionId, version, apply: true });
        checkApplied(out.applied);
        return out;
      },
      (out) =>
        (version === 'fitted' ? "Claude's fitted version is in use" : 'Your original script is in use') +
        (out.audioKept ? '. The audio of the other version stays with it; switch back to hear it.' : ''),
      'The script version was not switched',
    );
    setBusy(false);
  };
  return (
    <div className="mb-3 flex flex-wrap items-center gap-2 rounded-[12px] bg-panel px-3 py-2 hairline" data-version-switch={sv.active}>
      <ArrowsLeftRight size={14} className="text-subtle" aria-hidden />
      <span className="text-[12.5px] text-muted">Script:</span>
      <div className="inline-flex rounded-full bg-hover p-0.5" role="radiogroup" aria-label="Which version of the script">
        {(
          [
            ['original', 'Yours, as written'],
            ['fitted', 'Fitted by Claude'],
          ] as const
        ).map(([v, label]) => (
          <button
            key={v}
            type="button"
            role="radio"
            aria-checked={sv.active === v}
            disabled={busy}
            onClick={() => void set(v)}
            className={cx('h-7 rounded-full px-3 text-[12px] font-medium transition-colors', sv.active === v ? 'bg-panel text-fg shadow-[var(--shadow-soft)]' : 'text-muted hover:text-fg')}
          >
            {label}
          </button>
        ))}
      </div>
      {busy ? <Spinner size={12} /> : null}
      <span className="text-[12px] text-subtle">
        {sv.fitted.changed} of {sv.fitted.lines} lines changed{sv.fitted.kept ? `, ${sv.fitted.kept} kept as you wrote them` : ''}
      </span>
      <Button size="sm" variant="ghost" icon={<GitDiff size={13} />} className="ml-auto" onClick={onCompare}>
        Compare
      </Button>
    </div>
  );
}
