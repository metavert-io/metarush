import { memo, useEffect, useId, useMemo, useRef, useState } from 'react';
import { useParams } from 'react-router';
import { BracketsSquare, CaretDown, CaretUp, ChatText, ClockCounterClockwise, Crosshair, MagnifyingGlass, PencilSimple, Scissors, Sparkle, UserSwitch, X } from '@phosphor-icons/react';
import type { SpeakerRow, TranscriptOut, TranscriptSaid, TranscriptSegment } from '../api/types';
import { timecode } from '../lib/format';
import { useSpeakerColors } from '../lib/speakers';
import { useWordSelect } from '../lib/wordSelect';
import { joinWords, quoteWords, snapIn, snapOut, spaceBetween } from '../lib/words';
import { Button, cx, EmptyState, ErrorPanel, Skeleton, stateWord } from '../ui/primitives';
import { LineChip, LineFix, voiceMap } from './Speakers';
import { useDialog } from '../ui/Dialog';
import { FixWordsEditor, StretchSpeaker } from './transcripts/FixWords';
import { FindReplace } from './transcripts/FindReplace';
import { TranscriptHistory } from './transcripts/History';
import { useFixes } from './transcripts/useFixes';
import { SayDialog, WordMenu, type MenuEntry } from './transcripts/SayDifferently';

/** What a selection of words can do here (source seconds). */
export interface WordActions {
  /** Take the words out of the cut; `check` says why it can't (they aren't in the cut…). */
  cut?: { check: (from: number, to: number) => string | null; run: (sel: { from: number; to: number; words: string }) => void };
  /** Set the in and out marks around the words (to add them to the cut). */
  mark?: (inAt: number, outAt: number) => void;
}

const norm = (s: string) => s.toLowerCase().replace(/[^\p{L}\p{N}']+/gu, '');

/** Query → lowercase word stems to match the start of transcript words. */
function terms(q: string): string[] {
  return q
    .toLowerCase()
    .split(/[^\p{L}\p{N}']+/u)
    .map((t) => t.trim())
    .filter((t) => t.length > 0);
}

function wordMatches(word: string, ts: string[]): boolean {
  if (ts.length === 0) return false;
  const w = norm(word);
  return !!w && ts.some((t) => w.startsWith(norm(t)) || (t.length >= 3 && w.includes(norm(t))));
}

interface Hit {
  seg: number;
  word: number; // -1 for segments without word timing
  at: number;
}

/**
 * Timestamped transcript synced to playback: the current word lights up, the
 * view follows the playhead (until you scroll), and clicking any word or
 * timestamp seeks the source player there. "Find in transcript" highlights every
 * match and steps through them; `highlight` (from search) pre-fills it. With
 * several voices, each line shows who speaks (in that voice's color) and you
 * can show one person only (find then searches what that person says).
 */
export function Transcript({ data, loading, error, onRetry, time, onSeek, className, range, highlight, assetId, wordActions, duration }: {
  data: TranscriptOut | undefined;
  /** The clip: with it, every line shows who says it and can be given to someone else. */
  assetId?: string;
  /** With these, words can be selected (drag, Shift-click, Shift+arrows) and cut out or marked. */
  wordActions?: WordActions;
  /** The footage's length (marks never go past it). */
  duration?: number;
  loading: boolean;
  error: unknown;
  onRetry?: () => void;
  time: number;
  onSeek: (t: number) => void;
  className?: string;
  range?: { from: number; to: number };
  highlight?: string[];
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);
  const [only, setOnly] = useState<string | null>(null);
  const [fixing, setFixing] = useState<string | null>(null);
  const userScrollAt = useRef(0);
  const voices = useMemo(() => voiceMap(data), [data]);
  const colorOf = useSpeakerColors();
  const multi = voices.size > 1;
  const all = data?.segments ?? [];
  const segments = useMemo(() => (only ? all.filter((s) => s.speaker === only) : all), [all, only]);
  // Fixing the words (D145): any clip with a transcript, in any view.
  const { projectId } = useParams();
  const dialog = useDialog();
  const fixes = useFixes();
  const fixable = !!assetId && data?.status?.state === 'available' && !data?.original;
  const [editing, setEditing] = useState<'words' | 'speaker' | null>(null);
  const [saving, setSaving] = useState(false);
  // The right-click menu on words (D308: "Say it differently…" among the rest).
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  // Words in view, in order (the selection's units); lines without word times are not selectable.
  const flat = useMemo(() => {
    const out: { seg: number; f: number; t: number; w: string }[] = [];
    const base: number[] = [];
    segments.forEach((s, si) => {
      base[si] = out.length;
      for (const w of s.words ?? []) out.push({ seg: si, f: w.f, t: w.t, w: w.w.trim() });
    });
    return { out, base };
  }, [segments]);
  const selectable = !!(wordActions?.cut || wordActions?.mark || fixable);
  const span = (r: [number, number]) => {
    const ws = flat.out.map((w) => ({ from: w.f, to: w.t }));
    return {
      from: flat.out[r[0]]!.f,
      to: flat.out[r[1]]!.t,
      in: snapIn(ws, r[0]),
      out: snapOut(ws, r[1], duration ?? 0),
      words: joinWords(flat.out.slice(r[0], r[1] + 1).map((w) => w.w)),
    };
  };
  const cutWhy = (r: [number, number] | null) => {
    if (!r || !wordActions?.cut) return null;
    const s = span(r);
    return wordActions.cut.check(s.from, s.to);
  };
  const sel = useWordSelect(selectable ? flat.out.length : 0, {
    onActivate: (n) => {
      const w = flat.out[n];
      if (w) onSeek(w.f);
    },
    onDelete: (r) => {
      if (wordActions?.cut && !cutWhy(r)) {
        const s = span(r);
        wordActions.cut.run({ from: s.from, to: s.to, words: s.words });
        sel.clear();
      }
    },
    onPlay: (r) => {
      const w = flat.out[r[0]];
      if (w) onSeek(Math.max(0, w.f - 0.3));
    },
  });
  useEffect(() => sel.clear(), [only]); // eslint-disable-line react-hooks/exhaustive-deps
  // Change a spoken word (D308): in a project, on a clip whose words were heard here.
  const sayDifferently = (r: [number, number]) => {
    if (!assetId || !projectId) return;
    const sp = span(r);
    void dialog.open<string>((close) => <SayDialog projectId={projectId} assetId={assetId} from={sp.from} to={sp.to} before={sp.words} onClose={close} />, {
      title: 'Say it differently',
      size: 'md',
    });
  };
  const menuItems = (r: [number, number]): MenuEntry[] => {
    const out: MenuEntry[] = [];
    if (fixable) {
      out.push({
        label: 'Say it differently…',
        icon: <Sparkle size={14} />,
        onSelect: () => sayDifferently(r),
        disabled: !projectId,
        hint: projectId ? 'In the speaker’s own voice copy, with their consent' : 'Open the clip in a project to change its words in a cut',
      });
      out.push({ label: 'Fix the words', icon: <PencilSimple size={14} />, onSelect: () => setEditing('words') });
      out.push({ label: 'Who says this?', icon: <UserSwitch size={14} />, onSelect: () => setEditing('speaker') });
    }
    if (wordActions?.cut && !cutWhy(r)) {
      out.push({ label: 'Cut out of the video', icon: <Scissors size={14} />, onSelect: () => wordActions.cut!.run({ from: span(r).from, to: span(r).to, words: span(r).words }) });
    }
    return out;
  };
  useEffect(() => setEditing(null), [sel.range?.[0], sel.range?.[1]]); // eslint-disable-line react-hooks/exhaustive-deps
  const [query, setQuery] = useState(() => (highlight?.length ? highlight.join(' ') : ''));
  const [cur, setCur] = useState(0);
  const findId = useId();

  // Keyed by the words (callers build a new array on every render).
  const hl = highlight?.join(' ') ?? '';
  useEffect(() => {
    if (hl) setQuery(hl);
  }, [hl]);

  const ts = useMemo(() => terms(query), [query]);
  const hits: Hit[] = useMemo(() => {
    if (ts.length === 0) return [];
    const out: Hit[] = [];
    segments.forEach((s, si) => {
      if (s.words?.length) s.words.forEach((w, wi) => wordMatches(w.w, ts) && out.push({ seg: si, word: wi, at: w.f }));
      else if (s.text.split(/\s+/).some((w) => wordMatches(w, ts))) out.push({ seg: si, word: -1, at: s.from });
    });
    return out;
  }, [segments, ts]);

  // A search that arrived with a seek: start at the match nearest the playhead.
  useEffect(() => {
    if (hits.length === 0) return setCur(0);
    let best = 0;
    for (let i = 0; i < hits.length; i++) if (Math.abs(hits[i]!.at - time) < Math.abs(hits[best]!.at - time)) best = i;
    setCur(best);
    scrollTo(hits[best]!.seg);
  }, [hits]); // eslint-disable-line react-hooks/exhaustive-deps

  const activeSeg = useMemo(() => {
    let lo = 0;
    let hi = segments.length - 1;
    let ans = -1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if ((segments[mid]?.from ?? 0) <= time + 0.01) {
        ans = mid;
        lo = mid + 1;
      } else hi = mid - 1;
    }
    if (ans >= 0 && time > (segments[ans]?.to ?? 0) + 0.6) return -1;
    return ans;
  }, [segments, time]);

  const scrollTo = (seg: number) => {
    const el = scroller.current?.querySelector<HTMLElement>(`[data-seg="${seg}"]`);
    const box = scroller.current;
    if (!el || !box) return;
    const top = el.offsetTop - box.offsetTop;
    box.scrollTo({ top: Math.max(0, top - 48), behavior: 'smooth' });
  };

  useEffect(() => {
    if (!follow || activeSeg < 0) return;
    if (Date.now() - userScrollAt.current < 4000) return;
    const el = scroller.current?.querySelector<HTMLElement>(`[data-seg="${activeSeg}"]`);
    const box = scroller.current;
    if (!el || !box) return;
    const top = el.offsetTop - box.offsetTop;
    if (top < box.scrollTop + 24 || top > box.scrollTop + box.clientHeight - 80) {
      box.scrollTo({ top: Math.max(0, top - 48), behavior: 'smooth' });
    }
  }, [activeSeg, follow]);

  const step = (d: number) => {
    if (hits.length === 0) return;
    const n = (cur + d + hits.length) % hits.length;
    setCur(n);
    userScrollAt.current = Date.now(); // the search, not the playhead, decides where we look
    scrollTo(hits[n]!.seg);
    onSeek(hits[n]!.at);
  };

  if (loading)
    return (
      <div className={cx('space-y-4 p-1', className)}>
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="flex gap-4">
            <Skeleton className="h-4 w-14" />
            <div className="flex-1 space-y-2">
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-4 w-3/4" />
            </div>
          </div>
        ))}
      </div>
    );
  if (error) return <ErrorPanel error={error} onRetry={onRetry} compact className={className} />;
  if (!data) return null;
  const st = data.status?.state;
  if (st !== 'available')
    return (
      <EmptyState
        compact
        icon={<ChatText size={20} />}
        title={st === 'failed' ? 'Transcription failed' : st === 'running' ? 'Transcribing now' : st === 'queued' ? 'The transcript is waiting to start' : st ? `Transcript: ${stateWord(st)}` : 'No transcript yet'}
        body={data.status?.error || 'The transcript is made on your computer after the clip is added. It appears here when it is ready.'}
        className={className}
      />
    );
  if (all.length === 0)
    return <EmptyState compact icon={<ChatText size={20} />} title="No speech found" body="The transcriber listened to this clip and heard no words." className={className} />;

  const current = hits[cur];
  const voiceList = [...voices.values()].filter((v) => all.some((s) => s.speaker === v.label));
  return (
    <div className={cx('relative flex min-h-0 flex-col', className)}>
      <div className="mb-2 flex items-center gap-2">
        <div className="relative min-w-0 flex-1">
          <MagnifyingGlass size={13} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-subtle" />
          <label htmlFor={findId} className="sr-only">
            Find in transcript
          </label>
          <input
            id={findId}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                step(e.shiftKey ? -1 : 1);
              } else if (e.key === 'Escape' && query) {
                e.preventDefault();
                e.stopPropagation();
                setQuery('');
              }
            }}
            placeholder="Find in transcript"
            className="h-8 w-full rounded-[9px] bg-panel pl-7 pr-7 text-[12.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
          />
          {query ? (
            <button type="button" onClick={() => setQuery('')} aria-label="Clear search" className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded-[6px] p-0.5 text-subtle hover:text-fg">
              <X size={12} />
            </button>
          ) : null}
        </div>
        {query ? (
          <span className="flex shrink-0 items-center gap-0.5" aria-live="polite">
            <span className="tc mr-1 text-[11.5px] text-muted">{hits.length ? `${cur + 1} of ${hits.length}` : 'No matches'}</span>
            <button type="button" onClick={() => step(-1)} disabled={!hits.length} aria-label="Previous match" className="rounded-[6px] p-1 text-subtle hover:bg-hover hover:text-fg disabled:opacity-40">
              <CaretUp size={13} />
            </button>
            <button type="button" onClick={() => step(1)} disabled={!hits.length} aria-label="Next match" className="rounded-[6px] p-1 text-subtle hover:bg-hover hover:text-fg disabled:opacity-40">
              <CaretDown size={13} />
            </button>
          </span>
        ) : (
          <button
            type="button"
            aria-pressed={follow}
            onClick={() => {
              setFollow(!follow);
              userScrollAt.current = 0;
            }}
            className={cx('inline-flex shrink-0 items-center gap-1 rounded-full px-2 py-0.5 text-[11.5px] hairline', follow ? 'text-accent-text' : 'text-subtle')}
          >
            <Crosshair size={12} /> Follow
          </button>
        )}
        {fixable ? (
          <button
            type="button"
            onClick={() => void dialog.open((close) => <FindReplace assetId={assetId} clipTitle={data.title} projectId={projectId} initial={query} onDone={() => close()} />, { title: 'Find and replace in the transcripts', size: 'lg' })}
            className="inline-flex shrink-0 items-center gap-1 rounded-full px-2 py-0.5 text-[11.5px] text-subtle hairline hover:text-fg"
            data-find-replace-open
          >
            Replace
          </button>
        ) : null}
        {fixable && data.fixes?.rev ? (
          <button
            type="button"
            onClick={() => void dialog.open(() => <TranscriptHistory assetId={assetId!} />, { title: `Fixes in ${data.title}`, size: 'lg' })}
            className="inline-flex shrink-0 items-center gap-1 rounded-full px-2 py-0.5 text-[11.5px] text-subtle hairline hover:text-fg"
            data-history-open
          >
            <ClockCounterClockwise size={12} /> {data.fixes.fixes === 1 ? '1 fix' : `${data.fixes.fixes} fixes`}
          </button>
        ) : null}
      </div>
      {multi ? (
        <div className="no-scrollbar mb-2 flex gap-1.5 overflow-x-auto pb-0.5" role="group" aria-label="Show what one person says">
          <FilterChip active={only === null} onClick={() => setOnly(null)}>
            Everyone
          </FilterChip>
          {voiceList.map((v) => (
            <FilterChip key={v.label} active={only === v.label} color={colorOf(v.color)} onClick={() => setOnly(only === v.label ? null : v.label)}>
              {v.displayName}
            </FilterChip>
          ))}
        </div>
      ) : null}
      <div
        ref={scroller}
        onWheel={() => (userScrollAt.current = Date.now())}
        onTouchMove={() => (userScrollAt.current = Date.now())}
        onContextMenu={(e) => {
          if (!selectable) return;
          const n = Number((e.target as HTMLElement).closest<HTMLElement>('[data-wi]')?.dataset.wi);
          if (!Number.isFinite(n)) return;
          e.preventDefault();
          if (!sel.range || n < sel.range[0] || n > sel.range[1]) sel.select(n, n);
          setMenu({ x: e.clientX, y: e.clientY });
        }}
        onDoubleClick={(e) => {
          if (!fixable) return;
          // Double-click a word to fix how it is written.
          const n = Number((e.target as HTMLElement).closest<HTMLElement>('[data-wi]')?.dataset.wi);
          if (!Number.isFinite(n)) return;
          sel.select(n, n);
          setTimeout(() => setEditing('words'), 0);
        }}
        className={cx('relative min-h-0 flex-1 overflow-y-auto pr-1', selectable && 'select-none rounded-[10px] outline-none focus-visible:outline-2 focus-visible:outline-accent')}
        {...(selectable ? { tabIndex: 0, role: 'region', 'aria-label': 'Transcript. Select words with Shift and the arrow keys', ...sel.containerProps } : {})}
      >
        <ol className="space-y-1">
          {segments.map((s, i) => {
            const v = s.speaker ? voices.get(s.speaker) : undefined;
            const prev = segments[i - 1];
            return (
              <SegmentRow
                key={s.id || i}
                seg={s}
                index={i}
                active={i === activeSeg}
                time={i === activeSeg ? time : -1}
                onSeek={onSeek}
                inRange={!!range && s.to > range.from && s.from < range.to}
                said={data.said}
                terms={ts}
                currentWord={current && current.seg === i ? current.word : -2}
                voice={multi ? v : undefined}
                color={multi && v ? colorOf(v.color) : undefined}
                showName={multi && !!v && (!prev || prev.speaker !== s.speaker) && !assetId}
                chip={assetId && v ? { name: s.speakerName || v.displayName, color: colorOf(v.color) } : undefined}
                wordBase={selectable ? flat.base[i] : undefined}
                selRange={sel.range && s.words?.length && sel.range[1] >= flat.base[i]! && sel.range[0] < flat.base[i]! + s.words.length ? sel.range : null}
                fixing={fixing === (s.id || String(i))}
                onFix={() => setFixing(fixing === (s.id || String(i)) ? null : s.id || String(i))}
                fixPanel={assetId && fixing === (s.id || String(i)) ? <LineFix assetId={assetId} seg={s} current={v} onDone={() => setFixing(null)} /> : null}
              />
            );
          })}
        </ol>
        <p className="mt-4 text-[11.5px] leading-relaxed text-subtle">
          {selectable ? selectHint(wordActions ?? {}) : ''}
          {data.status?.provider ? 'Transcribed on your computer. ' : ''}Automatic transcripts can contain mistakes. Dotted words are uncertain; check quotes before putting them on screen.
        </p>
      </div>
      {menu && sel.range && menuItems(sel.range).length > 0 ? (
        <WordMenu x={menu.x} y={menu.y} items={menuItems(sel.range)} onClose={() => setMenu(null)} label="What to do with these words" />
      ) : null}
      {selectable && sel.range ? (
        <SelectionBar
          span={span(sel.range)}
          why={cutWhy(sel.range)}
          actions={wordActions ?? {}}
          onClear={sel.clear}
          hidden={sel.dragging}
          fix={fixable ? { editing, setEditing } : undefined}
          say={fixable && projectId ? () => sayDifferently(sel.range!) : undefined}
        >
          {editing === 'words' && assetId ? (
            <FixWordsEditor
              key={`${span(sel.range).from}`}
              words={span(sel.range).words}
              busy={saving}
              onCancel={() => setEditing(null)}
              onSave={async (text) => {
                const sp = span(sel.range!);
                setSaving(true);
                const out = await fixes.fix({ assetId, from: sp.from, to: sp.to, text, expect: sp.words, projectId });
                setSaving(false);
                if (out) {
                  setEditing(null);
                  sel.clear();
                }
              }}
            />
          ) : editing === 'speaker' && assetId ? (
            <StretchSpeaker
              assetId={assetId}
              from={span(sel.range).from}
              to={span(sel.range).to}
              projectId={projectId}
              onDone={() => {
                setEditing(null);
                sel.clear();
              }}
            />
          ) : null}
        </SelectionBar>
      ) : null}
    </div>
  );
}

function selectHint(a: WordActions): string {
  const what = [a.cut ? 'cut them out of the video' : '', a.mark ? 'mark them to add to the cut' : '', 'fix them'].filter(Boolean).join(' or ');
  return `Drag across words or Shift-click to select them, then ${what}. Double-click a word to fix how it is written. `;
}

/** The selected words and what to do with them. */
function SelectionBar({ span, why, actions, onClear, hidden, fix, say, children }: {
  span: { from: number; to: number; in: number; out: number; words: string };
  why: string | null;
  actions: WordActions;
  onClear: () => void;
  /** While words are being dragged across: out of the way. */
  hidden?: boolean;
  /** Fixing the words: retype them, or say who says them. */
  fix?: { editing: 'words' | 'speaker' | null; setEditing: (e: 'words' | 'speaker' | null) => void };
  /** Say the words differently, in the speaker's own voice copy (D308). */
  say?: () => void;
  children?: React.ReactNode;
}) {
  return (
    <div
      role="status"
      aria-live="polite"
      className={cx(
        'sticky bottom-3 z-10 mt-2 flex flex-wrap items-center gap-2 rounded-[12px] bg-raised px-3 py-2 shadow-[var(--shadow-float)] transition-opacity hairline',
        hidden && 'pointer-events-none opacity-0',
      )}
      data-word-selection
    >
      <span className="min-w-0 flex-1 truncate text-[12.5px] text-fg">
        “{quoteWords(span.words, 8)}”
        <span className="tc ml-2 text-[11.5px] text-subtle">
          {timecode(span.from, { ms: false })}–{timecode(span.to, { ms: false })}
        </span>
      </span>
      {actions.cut ? (
        <Button size="sm" variant="primary" icon={<Scissors size={13} />} disabled={!!why} onClick={() => actions.cut!.run({ from: span.from, to: span.to, words: span.words })} data-cut-words>
          Cut out of the video
        </Button>
      ) : null}
      {actions.mark ? (
        <Button size="sm" variant={actions.cut ? 'ghost' : 'secondary'} icon={<BracketsSquare size={13} />} onClick={() => actions.mark!(span.in, span.out)} data-mark-words>
          Mark in and out
        </Button>
      ) : null}
      {fix ? (
        <>
          <Button size="sm" variant={actions.cut || actions.mark ? 'ghost' : 'secondary'} icon={<PencilSimple size={13} />} onClick={() => fix.setEditing(fix.editing === 'words' ? null : 'words')} aria-pressed={fix.editing === 'words'} data-fix-words-open>
            Fix the words
          </Button>
          <Button size="sm" variant="ghost" icon={<UserSwitch size={13} />} onClick={() => fix.setEditing(fix.editing === 'speaker' ? null : 'speaker')} aria-pressed={fix.editing === 'speaker'} data-who-says-open>
            Who says this?
          </Button>
          {say ? (
            <Button size="sm" variant="ghost" icon={<Sparkle size={13} />} onClick={say} data-say-open>
              Say it differently…
            </Button>
          ) : null}
        </>
      ) : null}
      <button type="button" onClick={onClear} aria-label="Clear the selection" className="grid h-8 w-8 place-items-center rounded-[8px] text-subtle hover:bg-hover hover:text-fg">
        <X size={13} />
      </button>
      {why && actions.cut ? <span className="w-full text-[11.5px] text-subtle">{why}</span> : null}
      {children ? <div className="w-full">{children}</div> : null}
    </div>
  );
}

function FilterChip({ active, color, onClick, children }: { active: boolean; color?: string; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cx('inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full px-2.5 text-[12px] transition-colors hairline', active ? 'bg-panel text-fg' : 'text-muted hover:text-fg')}
    >
      {color ? <span className="h-2 w-2 rounded-full" style={{ background: color }} aria-hidden /> : null}
      <span className="max-w-[140px] truncate">{children}</span>
    </button>
  );
}

const SegmentRow = memo(function SegmentRow({ seg, index, active, time, onSeek, inRange, said, terms: ts, currentWord, voice, color, showName, chip, wordBase, selRange, fixing, onFix, fixPanel }: {
  seg: TranscriptSegment;
  index: number;
  active: boolean;
  time: number;
  onSeek: (t: number) => void;
  inRange: boolean;
  /** Stretches a cut says differently, in the speaker's own voice copy (D311). */
  said?: TranscriptSaid[];
  terms: string[];
  /** Word index of the current search match in this segment (-1: whole segment, -2: none). */
  currentWord: number;
  voice?: SpeakerRow;
  color?: string;
  showName: boolean;
  /** Who says the line, as a chip that opens "who says this?". */
  chip?: { name: string; color: string };
  /** Words are selectable: the index of this line's first word among the transcript's. */
  wordBase?: number;
  /** The selected words (transcript indices) when some are on this line. */
  selRange?: [number, number] | null;
  fixing?: boolean;
  onFix?: () => void;
  fixPanel?: React.ReactNode;
}) {
  // Exactly one lit word: the last one that has started, held briefly through gaps.
  let activeWord = -1;
  if (active && seg.words) {
    for (let i = 0; i < seg.words.length; i++) if ((seg.words[i]?.f ?? Infinity) <= time) activeWord = i;
    const w = seg.words[activeWord];
    if (w && time > w.t + 0.35) activeWord = -1;
  }
  const who = seg.speakerName || voice?.displayName;
  return (
    <li
      data-seg={index}
      className={cx(
        'group grid gap-3 rounded-[10px] px-2 py-2 transition-colors',
        chip ? 'grid-cols-[52px_1fr_auto] sm:grid-cols-[64px_1fr_auto]' : 'grid-cols-[64px_1fr]',
        active ? 'bg-accent-soft' : currentWord === -1 ? 'bg-accent-soft/60' : inRange ? 'bg-hover/60' : 'hover:bg-hover/60',
        showName && index > 0 && 'mt-2',
      )}
      style={color ? { boxShadow: `inset 2px 0 0 ${color}` } : undefined}
    >
      <button type="button" onClick={() => onSeek(seg.from)} className={cx('tc h-fit pt-[2px] text-left text-[11.5px]', active ? 'text-accent-text' : 'text-subtle hover:text-fg')} aria-label={`Jump to ${timecode(seg.from)}`}>
        {timecode(seg.from, { ms: false })}
      </button>
      <div className="min-w-0">
        {showName && who ? (
          <div className="mb-0.5 text-[12px] font-semibold" style={{ color }}>
            {who}
          </div>
        ) : null}
        <p className="text-[13.5px] leading-[1.65] text-muted">
          {color && who && !showName ? <span className="sr-only">{who}: </span> : null}
          {seg.words && seg.words.length > 0 ? (
            seg.words.map((w, i) => {
              const on = i === activeWord;
              const past = active && time >= w.t;
              const hit = ts.length > 0 && wordMatches(w.w, ts);
              if (wordBase !== undefined) {
                const n = wordBase + i;
                const picked = !!selRange && n >= selRange[0] && n <= selRange[1];
                const changed = said?.find((s) => (w.f + w.t) / 2 >= s.from && (w.f + w.t) / 2 <= s.to);
                return (
                  <span
                    key={i}
                    data-wi={n}
                    data-selected={picked || undefined}
                    title={
                      changed
                        ? `${timecode(w.f)}. ${changed.cutTitle} ${changed.inCut ? 'says' : 'can say'} “${changed.words}” here instead, in ${changed.who}’s own voice copy (changed with AI); this is what was said.${changed.state === 'withdrawn' ? ` ${changed.who}’s consent was withdrawn: take it out of the cut (Undo).` : ''}`
                        : `${timecode(w.f)}${w.p !== undefined ? `, confidence ${Math.round(w.p * 100)}%` : ''}`
                    }
                    data-fixed={w.x || undefined}
                    data-said={changed ? (changed.inCut ? 'in-cut' : 'ready') : undefined}
                    className={cx(
                      'cursor-text rounded-[3px] py-[1px]',
                      picked || on ? 'bg-accent text-accent-ink' : hit ? 'bg-accent-soft font-medium text-fg' : past ? 'text-fg hover:bg-hover' : active ? 'text-fg/80 hover:bg-hover' : 'hover:bg-hover hover:text-fg',
                      !picked && w.p !== undefined && w.p < 0.5 && 'underline decoration-dotted decoration-subtle underline-offset-4',
                      !picked && w.x && 'underline decoration-accent/60 decoration-[1.5px] underline-offset-[5px]',
                      !picked && changed && 'underline decoration-wavy decoration-accent decoration-[1.5px] underline-offset-[5px]',
                    )}
                  >
                    {w.w.trim()}
                    {i < seg.words!.length - 1 && spaceBetween(w.w.trim(), seg.words![i + 1]?.w.trim()) ? ' ' : ''}
                  </span>
                );
              }
              return (
                <span key={i}>
                  <button
                    type="button"
                    tabIndex={-1}
                    onClick={() => onSeek(w.f)}
                    data-hit={hit || undefined}
                    title={`${timecode(w.f)}${w.p !== undefined ? `, confidence ${Math.round(w.p * 100)}%` : ''}`}
                    className={cx(
                      'rounded-[4px] px-[1px] transition-colors duration-150 hover:bg-hover hover:text-fg',
                      on ? 'bg-accent text-accent-ink' : hit ? (i === currentWord ? 'bg-accent text-accent-ink ring-2 ring-accent-line' : 'bg-accent-soft font-medium text-fg') : past ? 'text-fg' : active ? 'text-fg/80' : '',
                      w.p !== undefined && w.p < 0.5 && 'underline decoration-dotted decoration-subtle underline-offset-4',
                    )}
                  >
                    {w.w.trim()}
                  </button>{' '}
                </span>
              );
            })
          ) : (
            <button type="button" tabIndex={-1} onClick={() => onSeek(seg.from)} className={cx('text-left', active && 'text-fg')}>
              {ts.length
                ? seg.text.split(/(\s+)/).map((part, i) =>
                    wordMatches(part, ts) ? (
                      <mark key={i} className="rounded-[3px] bg-accent-soft font-medium text-fg">
                        {part}
                      </mark>
                    ) : (
                      <span key={i}>{part}</span>
                    ),
                  )
                : seg.text}
            </button>
          )}
        </p>
        {fixPanel}
      </div>
      {chip ? <LineChip name={chip.name} color={chip.color} open={!!fixing} onClick={() => onFix?.()} className="mt-[1px]" /> : null}
    </li>
  );
});
