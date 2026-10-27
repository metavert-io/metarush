import { useId, useState } from 'react';
import { Lightbulb, Plus, X } from '@phosphor-icons/react';
import { call } from '../../api/client';
import { useOp } from '../../api/queries';
import type { SpellingEntry } from '../../api/types';
import { Button, EmptyState, Skeleton, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { plainError, useFixes } from './useFixes';

/**
 * A spelling list (D147): how names and terms are always written. New
 * transcripts are corrected by it (the same letters written differently, and
 * the ways a word is heard) and the transcriber is told to expect the words.
 * It is part of a project's Content Settings (D193): the list of the settings
 * the project uses — its own (changed here), or another project's (shown,
 * read-only: it is changed there). Without a project, the default's.
 */
export function SpellingList({ projectId, compact }: { projectId?: string; compact?: boolean }) {
  const lists = useOp('transcript.spelling.list', projectId ? { projectId } : {});
  const sugs = useOp('transcript.spelling.suggestions', projectId ? { projectId } : null);
  const fixes = useFixes();
  const toast = useToast();
  const [word, setWord] = useState('');
  const [heard, setHeard] = useState('');
  const [fixExisting, setFixExisting] = useState(true);
  const [busy, setBusy] = useState(false);
  const wordId = useId();
  const heardId = useId();
  const mine = lists.data?.entries;
  const readOnly = !!lists.data?.readOnly;
  const add = async () => {
    const w = word.trim();
    if (!w) return;
    setBusy(true);
    const out = await fixes.addSpelling({ projectId, word: w, heardAs: heard.trim() ? heard.split(',').map((h) => h.trim()).filter(Boolean) : undefined, fixExisting });
    setBusy(false);
    if (out) {
      setWord('');
      setHeard('');
    }
  };
  const remove = async (e: SpellingEntry) => {
    try {
      const out = await call('transcript.spelling.remove', { projectId: e.projectId || undefined, word: e.word });
      fixes.refresh();
      toast({ tone: 'success', title: out.summary, action: { label: 'Undo', onClick: () => void fixes.addSpelling({ projectId: e.projectId || undefined, word: e.word, heardAs: e.heardAs ?? undefined, note: e.note || undefined }) } });
    } catch (err) {
      toast({ tone: 'error', title: 'Still on the list', body: plainError(err) });
    }
  };
  return (
    <div className="space-y-3" data-spelling-list data-read-only={readOnly || undefined}>
      {readOnly ? null : (
      <form
        className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] sm:items-end"
        onSubmit={(e) => {
          e.preventDefault();
          void add();
        }}
      >
        <div>
          <label htmlFor={wordId} className="mb-1 block text-[12px] font-medium text-fg">
            Always write
          </label>
          <input
            id={wordId}
            value={word}
            onChange={(e) => setWord(e.target.value)}
            placeholder="FernBrook"
            maxLength={80}
            className="h-9 w-full rounded-[9px] bg-panel px-2.5 text-[13px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
            data-spelling-word
          />
        </div>
        <div>
          <label htmlFor={heardId} className="mb-1 block text-[12px] font-medium text-fg">
            Also heard as <span className="font-normal text-subtle">(optional)</span>
          </label>
          <input
            id={heardId}
            value={heard}
            onChange={(e) => setHeard(e.target.value)}
            placeholder="Fernbrooke, Fernbruk"
            className="h-9 w-full rounded-[9px] bg-panel px-2.5 text-[13px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
          />
        </div>
        <Button type="submit" variant="primary" icon={<Plus size={13} />} disabled={!word.trim()} loading={busy} data-spelling-add>
          Add
        </Button>
        <label className="inline-flex items-center gap-1.5 text-[12px] text-muted sm:col-span-3">
          <input type="checkbox" checked={fixExisting} onChange={(e) => setFixExisting(e.target.checked)} className="accent-[var(--accent)]" />
          Fix existing transcripts too (every clip using this list)
        </label>
      </form>
      )}
      {!compact && !readOnly ? (
        <p className="text-[11.5px] leading-relaxed text-subtle">
          Spellings that differ only in spaces, dashes or capitals are found by themselves (“Fern Brook”, “fern-brook”). Add the other ways it is heard when the transcriber gets it wrong differently.
        </p>
      ) : null}
      {lists.isLoading ? (
        <Skeleton className="h-16" />
      ) : mine && mine.length ? (
        <ul className="divide-y divide-line rounded-[12px] hairline" aria-label="The spelling list">
          {mine.map((e) => (
            <EntryRow key={e.id} e={e} onRemove={readOnly ? undefined : () => void remove(e)} />
          ))}
        </ul>
      ) : (
        <EmptyState
          compact
          title="Nothing on the list yet"
          body={readOnly ? 'Words added to it where it is changed show up here.' : 'Add the names and terms the transcripts get wrong; new transcripts are corrected by them.'}
        />
      )}
      {sugs.data?.suggestions.length && !readOnly ? (
        <div className="rounded-[12px] bg-accent-soft/60 p-2.5" data-spelling-suggestions>
          <div className="mb-1.5 flex items-center gap-1.5 text-[12px] font-medium text-fg">
            <Lightbulb size={13} /> You keep fixing these the same way
          </div>
          <ul className="space-y-1.5">
            {sugs.data.suggestions.map((s) => (
              <li key={s.key} className="flex flex-wrap items-center gap-2 text-[12.5px] text-muted">
                <span className="min-w-0 flex-1">
                  <Theirs className="font-medium text-fg">“{s.word}”</Theirs> — fixed {s.times > 1 ? `${s.times} times` : `in ${s.places} places`}
                  {s.heard.length ? (
                    <>
                      {' '}
                      (it was <Theirs>{s.heard.map((h) => `“${h}”`).join(', ')}</Theirs>)
                    </>
                  ) : null}
                </span>
                <Button size="sm" variant="secondary" onClick={() => void fixes.addSpelling({ projectId, word: s.word, heardAs: s.heard })}>
                  Add to the list
                </Button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}

function EntryRow({ e, onRemove }: { e: SpellingEntry; onRemove?: () => void }) {
  return (
    <li className="flex items-center gap-2 px-3 py-2" data-spelling-entry={e.word}>
      <div className="min-w-0 flex-1">
        <Theirs className="text-[13.5px] font-medium text-fg">{e.word}</Theirs>
        {e.heardAs?.length ? (
          <span className="ml-2 text-[12px] text-subtle">
            also heard as <Theirs>{e.heardAs.map((h) => `“${h}”`).join(', ')}</Theirs>
          </span>
        ) : null}
        {e.note ? <Theirs className="block text-[12px] text-subtle">{e.note}</Theirs> : null}
      </div>
      {onRemove ? (
        <button type="button" onClick={onRemove} aria-label={`Take “${e.word}” off the list`} className="grid h-7 w-7 place-items-center rounded-[8px] text-subtle hover:bg-hover hover:text-fg">
          <X size={13} />
        </button>
      ) : null}
    </li>
  );
}
