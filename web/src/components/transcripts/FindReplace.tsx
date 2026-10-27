import { useEffect, useId, useMemo, useState } from 'react';
import { keepPreviousData } from '@tanstack/react-query';
import { MagnifyingGlass } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import type { OpInput } from '../../api/types';
import { timecode, plural } from '../../lib/format';
import { Button, cx, Segmented, Spinner, Theirs } from '../../ui/primitives';
import { SnippetButton } from '../SnippetPlayer';
import { useFixes } from './useFixes';

type Scope = 'clip' | 'project' | 'everywhere';

function useDebounced<T>(v: T, ms: number): T {
  const [d, setD] = useState(v);
  useEffect(() => {
    const t = setTimeout(() => setD(v), ms);
    return () => clearTimeout(t);
  }, [v, ms]);
  return d;
}

/**
 * Find and replace in transcripts (D145): one clip, the project or every
 * clip. Every place is listed with the words around it and a play button
 * before anything changes; untick the places to leave alone. Whole words by
 * default; joining or splitting words keeps their timing. "Also put it on the
 * spelling list" makes new transcripts get it right.
 */
export function FindReplace({ projectId, assetId, clipTitle, initial = '', onDone }: {
  projectId?: string;
  /** The clip being looked at (offers "This clip"). */
  assetId?: string;
  clipTitle?: string;
  initial?: string;
  onDone: (changed: boolean) => void;
}) {
  const fixes = useFixes();
  const [query, setQuery] = useState(initial);
  const [withText, setWithText] = useState('');
  const [scope, setScope] = useState<Scope>(assetId ? 'clip' : projectId ? 'project' : 'everywhere');
  const [parts, setParts] = useState(false);
  const [matchCase, setMatchCase] = useState(false);
  const [spelling, setSpelling] = useState(false);
  const [unticked, setUnticked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const findId = useId();
  const withId = useId();
  const q = useDebounced(query.trim(), 250);
  const w = useDebounced(withText, 250);
  const where = scope === 'clip' ? { assetId } : scope === 'project' ? { projectId } : { everywhere: true };
  const input: OpInput<'transcript.find'> | null = q ? { query: q, with: w, ...where, partOfWords: parts, matchCase, limit: 500 } : null;
  const found = useOp('transcript.find', input, { placeholderData: keepPreviousData, staleTime: 0 });
  const hits = useMemo(() => found.data?.hits ?? [], [found.data]);
  useEffect(() => setUnticked(new Set()), [q, scope, parts, matchCase]);
  const ticked = hits.filter((h) => !h.same && !unticked.has(h.id));
  const clips = new Set(ticked.map((h) => h.assetId)).size;
  const looksLikeTerm = withText.trim().length > 0 && withText.trim().split(/\s+/).length <= 6;
  const apply = async () => {
    if (!q || !ticked.length) return;
    setBusy(true);
    const out = await fixes.replace({ query: q, with: withText.trim(), ...where, partOfWords: parts, matchCase, only: ticked.map((h) => h.id), spelling: spelling && looksLikeTerm, projectId: where.projectId ?? projectId });
    setBusy(false);
    if (out) onDone(out.changed > 0);
  };
  return (
    <div className="space-y-3" data-find-replace>
      <div className="grid gap-2 sm:grid-cols-2">
        <div>
          <label htmlFor={findId} className="mb-1 block text-[12px] font-medium text-fg">
            Find
          </label>
          <div className="relative">
            <MagnifyingGlass size={13} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-subtle" />
            <input
              id={findId}
              autoFocus
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Fern Brook"
              className="h-9 w-full rounded-[9px] bg-panel pl-7 pr-2 text-[13px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
              data-find-input
            />
          </div>
        </div>
        <div>
          <label htmlFor={withId} className="mb-1 block text-[12px] font-medium text-fg">
            Replace with
          </label>
          <input
            id={withId}
            value={withText}
            onChange={(e) => setWithText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                void apply();
              }
            }}
            placeholder="FernBrook"
            className="h-9 w-full rounded-[9px] bg-panel px-2.5 text-[13px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
            data-replace-input
          />
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <Segmented
          label="Where to look"
          value={scope}
          onChange={setScope}
          items={[
            ...(assetId ? [{ value: 'clip' as Scope, label: clipTitle ? <Theirs className="inline-block max-w-[180px] truncate align-bottom">{clipTitle}</Theirs> : 'This clip' }] : []),
            ...(projectId ? [{ value: 'project' as Scope, label: 'This project' }] : []),
            { value: 'everywhere' as Scope, label: 'Everything' },
          ]}
        />
        <label className="inline-flex items-center gap-1.5 text-[12.5px] text-muted">
          <input type="checkbox" checked={!parts} onChange={(e) => setParts(!e.target.checked)} className="accent-[var(--accent)]" data-whole-words />
          Whole words
        </label>
        <label className="inline-flex items-center gap-1.5 text-[12.5px] text-muted">
          <input type="checkbox" checked={matchCase} onChange={(e) => setMatchCase(e.target.checked)} className="accent-[var(--accent)]" data-match-case />
          Match case
        </label>
      </div>
      <div className="min-h-[120px] rounded-[12px] hairline" aria-live="polite">
        {!q ? (
          <p className="p-4 text-[12.5px] text-subtle">Type what the transcript says, as it was heard. Every place it is said is listed here, to listen to before anything changes.</p>
        ) : found.isLoading ? (
          <div className="flex items-center gap-2 p-4 text-[12.5px] text-subtle">
            <Spinner size={12} /> Looking…
          </div>
        ) : (
          <>
            <div className="flex items-center gap-2 border-b border-line px-3 py-2 text-[12px] text-muted">
              <span className="min-w-0 flex-1">
                {found.data?.summary}
                {unticked.size && ticked.length < hits.filter((h) => !h.same).length ? (
                  <span className="text-fg"> {ticked.length ? `Only the ${plural(ticked.length, 'ticked place')} will change.` : 'Nothing is ticked.'}</span>
                ) : null}
              </span>
              {hits.length > 1 ? (
                <button type="button" className="text-[12px] font-medium text-accent-text hover:underline" onClick={() => setUnticked(unticked.size ? new Set() : new Set(hits.map((h) => h.id)))}>
                  {unticked.size ? 'Tick all' : 'Untick all'}
                </button>
              ) : null}
            </div>
            <ul className="max-h-[min(46dvh,420px)] divide-y divide-line overflow-y-auto" data-find-results>
              {hits.map((h) => {
                const on = !h.same && !unticked.has(h.id);
                return (
                  <li key={h.id} className={cx('flex items-start gap-2.5 px-3 py-2', h.same && 'opacity-60')} data-find-hit={h.id}>
                    <input
                      type="checkbox"
                      checked={on}
                      disabled={h.same}
                      aria-label={`Change this place in ${h.clip} at ${timecode(h.from, { ms: false })}`}
                      onChange={() => {
                        const next = new Set(unticked);
                        if (next.has(h.id)) next.delete(h.id);
                        else next.add(h.id);
                        setUnticked(next);
                      }}
                      className="mt-1.5 accent-[var(--accent)]"
                    />
                    <SnippetButton assetId={h.assetId} from={Math.max(0, h.from - 1.2)} to={h.to + 1.2} label={`what is said at ${timecode(h.from, { ms: false })}`} size="sm" />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-baseline gap-2 text-[11.5px] text-subtle">
                        <Theirs className="min-w-0 truncate font-medium text-muted">{h.clip}</Theirs>
                        <span className="tc shrink-0">{timecode(h.from, { ms: false })}</span>
                        {h.speaker ? <Theirs className="truncate">{h.speaker}</Theirs> : null}
                      </div>
                      <p className="text-[13px] leading-relaxed text-muted">
                        <Theirs>
                          {h.before ? `…${h.before} ` : ''}
                          <mark className="rounded-[3px] bg-accent-soft px-0.5 font-medium text-fg">{h.text}</mark>
                          {h.after ? ` ${h.after}…` : ''}
                        </Theirs>
                        {withText && !h.same ? (
                          <span className="ml-1.5 text-fg">
                            → <Theirs className="font-medium">{h.new}</Theirs>
                          </span>
                        ) : null}
                        {h.same ? <span className="ml-1.5 text-[11.5px] text-subtle">already like that</span> : null}
                      </p>
                    </div>
                  </li>
                );
              })}
            </ul>
            {found.data?.note ? <p className="border-t border-line px-3 py-2 text-[11.5px] text-subtle">{found.data.note}</p> : null}
          </>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-3">
        {looksLikeTerm ? (
          <label className="inline-flex items-center gap-1.5 text-[12.5px] text-muted">
            <input type="checkbox" checked={spelling} onChange={(e) => setSpelling(e.target.checked)} className="accent-[var(--accent)]" data-add-spelling />
            Also put <Theirs className="font-medium text-fg">“{withText.trim()}”</Theirs> on the spelling list
          </label>
        ) : null}
        <span className="flex-1" />
        <Button variant="ghost" onClick={() => onDone(false)}>
          Cancel
        </Button>
        <Button variant="primary" disabled={!ticked.length || !q} loading={busy} onClick={() => void apply()} data-replace-apply>
          {ticked.length ? `Change ${plural(ticked.length, 'place')}${clips > 1 ? ` in ${clips} videos` : ''}` : 'Change'}
        </Button>
      </div>
      <p className="text-[11.5px] leading-relaxed text-subtle">Saved versions of a cut keep their words; the fixes wait until you make a new version. Every change can be undone.</p>
    </div>
  );
}
