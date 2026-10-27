import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react';
import { ArrowUp } from '@phosphor-icons/react';
import type { ApiError } from '../../api/client';
import { Button, ErrorPanel } from '../../ui/primitives';

/*
 * "Not quite…" (D186): what should change, written right on the card (never a
 * native dialog). Sending marks the card "not quite" and gives Claude the note
 * at once, as the person's message, to make a new version with that change.
 */
export function NotQuiteBox({ onSend, onCancel, busy, error, what }: { onSend: (note: string) => void; onCancel: () => void; busy: boolean; error?: ApiError | null; what?: string }) {
  const [text, setText] = useState('');
  const id = useId();
  const ta = useRef<HTMLTextAreaElement>(null);
  useEffect(() => ta.current?.focus(), []);
  const send = () => {
    const t = text.trim();
    if (t && !busy) onSend(t);
  };
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      send();
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      onCancel();
    }
  };
  return (
    <div className="mt-2.5 rounded-[12px] bg-panel p-2.5 hairline" data-not-quite-box>
      <label htmlFor={id} className="mb-1 block text-[12px] font-medium text-muted">
        What should change?
      </label>
      <textarea
        id={id}
        ref={ta}
        rows={2}
        maxLength={2000}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={onKey}
        placeholder={what ? `For example: ${what}` : 'For example: keep the first line, and make it shorter.'}
        className="block w-full resize-y rounded-[9px] bg-raised px-2.5 py-2 text-[13px] leading-relaxed text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-offset-0 focus:outline-accent"
      />
      {error ? <ErrorPanel error={error} compact className="mt-2" title="Could not send that" /> : null}
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <Button size="sm" variant="primary" icon={<ArrowUp size={12} weight="bold" />} loading={busy} disabled={!text.trim() || busy} onClick={send} data-not-quite-send>
          Send to Claude
        </Button>
        <Button size="sm" variant="ghost" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
        <span className="text-[11.5px] text-subtle">Claude makes a new version with your change.</span>
      </div>
    </div>
  );
}
