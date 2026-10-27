import { useEffect, useId, useRef, useState } from 'react';
import { HandPalm, Lightning } from '@phosphor-icons/react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useAssistantOp } from '../../api/assistant';
import { call } from '../../api/client';
import { ErrorPanel, cx } from '../../ui/primitives';

/*
 * Ask first or Auto (D186), the person's own choice (kept on their account, so
 * it follows them to every browser and the command line). Ask first: changes
 * wait for an OK; safe steps run by themselves. Auto: every change that can be
 * undone runs by itself too, as them. Money, final renders, approvals,
 * deleting and setup always ask. The computer's own setting is the owner's
 * master switch: off, nothing runs by itself and Auto isn't offered.
 */

const OPEN_EVENT = 'mr:assistant-mode';

/** Opens the switch (a card's "Change" link). */
export function openModeSwitch() {
  window.dispatchEvent(new Event(OPEN_EVENT));
}

/**
 * What Auto does, in plain words. With the person's estimates off (D208),
 * changes that cost money run by themselves too — only where a person set the
 * project's budget and the team's daily limit with that service (D241).
 */
export function autoWords(estimatesOff?: boolean): string {
  return estimatesOff
    ? 'Changes that can be undone also run by themselves, as you: saving a version with its review copy, sound, transcript and footage fixes, links. With your estimates off, so do changes that cost money — only where a person set the project’s budget and the team’s daily limit with that service, and within them. Final renders, approvals and deleting still ask.'
    : 'Changes that can be undone also run by themselves, as you: saving a version with its review copy, sound, transcript and footage fixes, links. Anything that costs money, final renders, approvals and deleting still ask.';
}

export function useAssistantMode() {
  return useQuery({
    queryKey: ['assistant', 'mode'],
    queryFn: ({ signal }) => call('assistant.mode', {}, signal),
    staleTime: 30_000,
  });
}

export function ModeSwitch() {
  const mode = useAssistantMode();
  const set = useAssistantOp('assistant.mode.set');
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  // The choice shows at once; the saved answer replaces it (or it goes back if
  // saving fails), so the switch never flips back while the save is under way.
  const [chosen, setChosen] = useState<'ask' | 'auto' | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const id = useId();
  useEffect(() => {
    const show = () => {
      setOpen(true);
      button.current?.focus();
    };
    window.addEventListener(OPEN_EVENT, show);
    return () => window.removeEventListener(OPEN_EVENT, show);
  }, []);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey, true);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey, true);
    };
  }, [open]);
  if (!mode.data) return null;
  const offered = mode.data.offered;
  const current = chosen ?? mode.data.mode;
  const auto = current === 'auto' && offered;
  const choose = (m: 'ask' | 'auto') => {
    if (m === current) return;
    setChosen(m);
    set.mutate(
      { mode: m },
      {
        onSuccess: (out) => qc.setQueryData(['assistant', 'mode'], out),
        onSettled: () => setChosen(null),
      },
    );
  };
  return (
    <div ref={ref} className="relative shrink-0">
      <button
        ref={button}
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-controls={open ? id : undefined}
        aria-label={auto ? 'Auto is on: changes that can be undone run by themselves. Change' : 'Ask first: changes wait for your OK. Change'}
        title={auto ? 'Auto: changes that can be undone run by themselves' : 'Ask first: changes wait for your OK'}
        className={cx(
          'inline-flex h-7 items-center gap-1 rounded-[8px] px-2 text-[12px] font-medium transition-colors',
          auto ? 'bg-accent-soft text-accent-text hover:brightness-110' : 'text-subtle hover:bg-hover hover:text-fg',
        )}
        data-auto-mode={auto ? 'auto' : 'ask'}
      >
        {auto ? <Lightning size={13} weight="fill" aria-hidden /> : <HandPalm size={13} aria-hidden />}
        {auto ? 'Auto' : 'Ask first'}
      </button>
      {open ? (
        <div
          id={id}
          role="dialog"
          aria-label="How Claude's changes run"
          className="absolute bottom-full right-0 z-20 mb-2 w-[min(340px,86vw)] rounded-[12px] bg-raised p-2 shadow-[var(--shadow-float)] hairline"
          data-auto-menu
        >
          <p className="px-1.5 pb-1.5 text-[11.5px] text-subtle">How Claude's changes for you run</p>
          <Option
            name={`${id}-mode`}
            checked={!auto}
            onChange={() => choose('ask')}
            title="Ask first"
            body="Changes wait for your OK. Safe steps (checking a graphic, a review copy, a link you typed) run by themselves."
            value="ask"
          />
          <Option
            name={`${id}-mode`}
            checked={auto}
            disabled={!offered}
            onChange={() => choose('auto')}
            title="Auto"
            body={autoWords(mode.data.estimatesOff)}
            value="auto"
          />
          {!offered ? (
            <p className="px-1.5 pt-1.5 text-[12px] leading-relaxed text-muted" data-auto-off>
              Nothing runs by itself on this computer, so Auto isn't offered. An owner can change that in Settings → Claude Code.
            </p>
          ) : null}
          {set.error ? <ErrorPanel error={set.error} compact className="mt-2" /> : null}
        </div>
      ) : null}
    </div>
  );
}

function Option({
  name,
  checked,
  disabled,
  onChange,
  title,
  body,
  value,
}: {
  name: string;
  checked: boolean;
  disabled?: boolean;
  onChange: () => void;
  title: string;
  body: string;
  value: string;
}) {
  return (
    <label className={cx('flex items-start gap-2.5 rounded-[8px] px-1.5 py-1.5', disabled ? 'cursor-not-allowed opacity-60' : 'cursor-pointer hover:bg-hover')}>
      <input type="radio" name={name} checked={checked} disabled={disabled} onChange={onChange} className="mt-[3px] h-3.5 w-3.5 accent-[var(--accent)]" data-auto-option={value} />
      <span className="min-w-0">
        <span className="block text-[13px] font-medium text-fg">{title}</span>
        <span className="mt-0.5 block text-[12px] leading-relaxed text-muted">{body}</span>
      </span>
    </label>
  );
}
