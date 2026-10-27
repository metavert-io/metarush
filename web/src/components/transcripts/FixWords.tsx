import { useEffect, useId, useRef, useState } from 'react';
import { X } from '@phosphor-icons/react';
import { PersonPicker } from '../PersonPicker';
import { Button, Spinner } from '../../ui/primitives';
import { useFixes } from './useFixes';

/**
 * Retype what is said (D145): only the words that differ change, and each
 * keeps the time it was said in, so joining "Fern Brook" into "FernBrook" or splitting
 * "Maplegrove" into "Maple Grove" leaves the cut and its quoted words where
 * they are. Enter saves; Esc closes; Shift+Enter starts a new line of text.
 */
export function FixWordsEditor({ words, onSave, onCancel, busy, label = 'What is really said?', note }: {
  /** What the transcript says now. */
  words: string;
  onSave: (text: string) => void;
  onCancel: () => void;
  busy?: boolean;
  label?: string;
  note?: string;
}) {
  const [text, setText] = useState(words);
  const id = useId();
  const ref = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.focus();
    el.select();
  }, []);
  const changed = text.trim().replace(/\s+/g, ' ') !== words.trim().replace(/\s+/g, ' ');
  return (
    <div className="rounded-[12px] bg-raised p-2.5 hairline" data-fix-words onPointerDown={(e) => e.stopPropagation()}>
      <div className="mb-1.5 flex items-center gap-2">
        <label htmlFor={id} className="text-[12px] font-medium text-fg">
          {label}
        </label>
        {busy ? <Spinner size={11} className="text-subtle" label="Saving" /> : null}
        <button type="button" onClick={onCancel} className="ml-auto rounded-[6px] p-0.5 text-subtle hover:text-fg" aria-label="Close">
          <X size={12} />
        </button>
      </div>
      <textarea
        id={id}
        ref={ref}
        value={text}
        rows={Math.min(5, Math.max(1, Math.ceil(text.length / 60)))}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (e.key === 'Escape') {
            e.preventDefault();
            onCancel();
          } else if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            if (changed && !busy) onSave(text);
          }
        }}
        className="w-full resize-none rounded-[9px] bg-panel px-2.5 py-1.5 text-[13.5px] leading-relaxed text-fg hairline focus:outline-2 focus:outline-accent focus:outline-offset-0"
        data-fix-words-input
      />
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <Button size="sm" variant="primary" disabled={!changed} loading={busy} onClick={() => onSave(text)} data-fix-words-save>
          Save the fix
        </Button>
        <Button size="sm" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <span className="min-w-0 flex-1 text-[11.5px] leading-snug text-subtle">
          {note ?? 'The words keep their timing; joined or split words too. It can be undone.'}
        </span>
      </div>
    </div>
  );
}

/** "This part is Lena, not Maya": who says a stretch (recorded, so it can be undone). */
export function StretchSpeaker({ assetId, from, to, projectId, onDone }: { assetId: string; from: number; to: number; projectId?: string; onDone: () => void }) {
  const fixes = useFixes();
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  return (
    <div className="rounded-[12px] bg-raised p-2.5 hairline" data-stretch-speaker onPointerDown={(e) => e.stopPropagation()}>
      <div className="mb-1.5 flex items-center gap-2 text-[12px] font-medium text-fg">
        Who says these words?
        {busy ? <Spinner size={11} className="text-subtle" label="Saving" /> : null}
        <button type="button" onClick={onDone} className="ml-auto rounded-[6px] p-0.5 text-subtle hover:text-fg" aria-label="Close">
          <X size={12} />
        </button>
      </div>
      <PersonPicker
        label="Who says these words?"
        value={text}
        onValueChange={setText}
        allowUnsure
        newLabel="Someone new"
        placeholder="Type a name, or pick below"
        autoFocus
        onEscape={onDone}
        onPick={async (c) => {
          setBusy(true);
          const base = { assetId, from, to, projectId };
          const out = await fixes.speaker(
            c.kind === 'person' ? { ...base, personId: c.person.id } : c.kind === 'new' ? { ...base, person: c.name, newPerson: true } : { ...base, unsure: true },
          );
          setBusy(false);
          if (out) onDone();
        }}
      />
      <p className="mt-1.5 text-[11.5px] leading-relaxed text-subtle">Only these words change. Their voice is learned from what you confirm, and it can be undone.</p>
    </div>
  );
}
