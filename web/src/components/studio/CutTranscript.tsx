import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router';
import { useQueries } from '@tanstack/react-query';
import { ArrowsInLineHorizontal, ChatText, ClockCounterClockwise, MagnifyingGlass, PencilSimple, Play, Scissors, Scroll, Sparkle, UserSwitch, X } from '@phosphor-icons/react';
import { call } from '../../api/client';
import type { AssetSummary, Clip, ProjectShowOut, SaidSound, TranscriptOut } from '../../api/types';
import { clock, plural, timecode } from '../../lib/format';
import { useMediaQuery } from '../../lib/hooks';
import { useSpeakerColors } from '../../lib/speakers';
import { useWordSelect } from '../../lib/wordSelect';
import { cutPoint, cutWords, OpError, quoteWords, removalOps, removalsFor, shorter, type CutClipWords, type CutWord, type Removal } from '../../lib/words';
import { useDialog } from '../../ui/Dialog';
import { Button, cx, EmptyState, Skeleton } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { LineChip, LineFix, voiceMap } from '../Speakers';
import { CleanupReview, type CleanupResult } from './CleanupReview';
import type { PlayerControls } from './TimelineEditor';
import { plainRefusal } from '../../lib/editops';
import type { TimelineEditor as Editor } from './useTimelineEditor';
import { FixWordsEditor, StretchSpeaker } from '../transcripts/FixWords';
import { FindReplace } from '../transcripts/FindReplace';
import { TranscriptHistory } from '../transcripts/History';
import { useFixes } from '../transcripts/useFixes';

function clipTitle(c: Clip, assets: Map<string, AssetSummary>): string {
  return c.label || assets.get(c.assetId)?.title || c.id;
}

/**
 * The cut's words, to edit by transcript: what every clip says, in the cut's
 * order, the word being played lit up. Select words (click and drag, Shift-click,
 * Shift+arrows; on a phone tap one word, then another) and press Delete or "Cut
 * out": the cut takes them out in the pauses around them, as one change on the
 * timeline (undo brings them back). The chip on a line says who speaks; click
 * it to give the line to someone else. "Remove ums and restarts" suggests
 * filler sounds, repeated words and false starts to listen to and take out.
 */
export function CutTranscript({ data, editor, live, assets, time, onSeek, player }: {
  data: ProjectShowOut;
  editor: Editor;
  /** The editor as it is when a dialog acts (dialogs keep the props they opened with). */
  live: Editor;
  assets: Map<string, AssetSummary>;
  time: number;
  onSeek: (s: number) => void;
  player: PlayerControls;
}) {
  const spec = editor.spec;
  const toast = useToast();
  const dialog = useDialog();
  const colorOf = useSpeakerColors();
  const coarse = useMediaQuery('(pointer: coarse)');
  const hintId = useId();
  const box = useRef<HTMLDivElement>(null);
  const userScrollAt = useRef(0);
  const [fixing, setFixing] = useState<string | null>(null);
  // Fixing the selected words (retype, or who says them).
  const [editing, setEditing] = useState<'words' | 'speaker' | null>(null);
  const [saving, setSaving] = useState(false);
  const fixes = useFixes();

  const assetIds = useMemo(() => [...new Set((spec?.clips ?? []).map((c) => c.assetId))].filter((id) => assets.get(id)?.hasAudio), [spec, assets]);
  const queries = useQueries({
    queries: assetIds.map((id) => ({
      queryKey: ['footage.transcript', { assetId: id, words: true }],
      queryFn: ({ signal }: { signal: AbortSignal }) => call('footage.transcript', { assetId: id, words: true }, signal),
      staleTime: 5 * 60_000,
    })),
  });
  const txKey = assetIds.map((id, i) => `${id}:${queries[i]?.dataUpdatedAt ?? 0}`).join('|');
  const txOut = useMemo(() => {
    const m = new Map<string, TranscriptOut>();
    assetIds.forEach((id, i) => {
      const d = queries[i]?.data as TranscriptOut | undefined;
      if (d) m.set(id, d);
    });
    return m;
  }, [txKey]); // eslint-disable-line react-hooks/exhaustive-deps
  // The words Go knows: transcripts that are ready.
  const tx = useCallback((id: string) => {
    const d = txOut.get(id);
    return d?.status?.state === 'available' ? d.segments : undefined;
  }, [txOut]);
  const model = useMemo(() => (spec ? cutWords(spec, tx) : { clips: [], words: [] }), [spec, tx]);
  const loading = queries.some((q) => q.isLoading);
  // Who speaks last before each clip (for the chips).
  const lastNames = useMemo(() => {
    const out: (string | undefined)[] = [];
    let last: string | undefined;
    for (const cc of model.clips) {
      out.push(last);
      const w = cc.words[cc.words.length - 1];
      const d = txOut.get(cc.clip.assetId);
      const seg = w ? d?.segments[w.seg] : undefined;
      if (seg) last = seg.speakerName || d?.speakers.find((v) => v.label === seg.speaker)?.displayName || last;
    }
    return out;
  }, [model, txOut]);

  // The word being played: the last one that has started, held briefly through pauses.
  const active = useMemo(() => {
    const ws = model.words;
    let lo = 0;
    let hi = ws.length - 1;
    let ans = -1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (ws[mid]!.t0 <= time + 0.01) {
        ans = mid;
        lo = mid + 1;
      } else hi = mid - 1;
    }
    if (ans < 0 || time > ws[ans]!.t1 + 0.35) return -1;
    return ans;
  }, [model, time]);

  const cutOut = (range: [number, number]) => {
    const s = live.spec;
    if (!s) return;
    let removals: Removal[];
    let ops;
    try {
      removals = removalsFor(s, tx, cutWords(s, tx), range[0], range[1]);
      ops = removalOps(s, removals);
    } catch (e) {
      toast({ tone: 'error', title: 'Not cut out', body: e instanceof OpError ? plainRefusal(e.message) : 'Those words could not be cut out.' });
      return;
    }
    if (!ops.length || !removals.length) return;
    const words = removals.map((r) => r.words).join(' ');
    const label = `Took out “${quoteWords(words)}”`;
    const err = live.apply(label, ops);
    if (err) {
      toast({ tone: 'error', title: 'Not cut out', body: err });
      return;
    }
    sel.clear();
    const secs = removals.reduce((n, r) => n + r.seconds, 0);
    // Just before the cut, so pressing play lets you hear it.
    onSeek(Math.max(0, cutPoint(s, removals[0]!) - 1.5));
    toast({ title: label, body: `The video is ${shorter(secs)} shorter. Press play to hear the cut.`, action: { label: 'Undo', onClick: () => live.undo() } });
  };

  const sel = useWordSelect(model.words.length, {
    onActivate: (n) => {
      const w = model.words[n];
      if (w) onSeek(w.t0 + 0.001);
    },
    onDelete: cutOut,
    onPlay: (r) => {
      const w = model.words[r[0]];
      if (!w) return;
      onSeek(Math.max(0, w.t0 - 0.3));
      player.play();
    },
    startAt: () => (active >= 0 ? active : 0),
    onOtherKey: (e) => {
      const cmd = e.metaKey || e.ctrlKey;
      if (cmd && (e.key === 'z' || e.key === 'Z')) {
        const m = e.shiftKey ? editor.redo() : editor.undo();
        if (m) toast({ title: m });
        return true;
      }
      if (!cmd && !e.altKey && e.key === ' ') {
        player.toggle();
        return true;
      }
      return false;
    },
  });

  // Follow the playback (until you scroll or select).
  useEffect(() => {
    if (active < 0 || sel.range || Date.now() - userScrollAt.current < 4000) return;
    const el = box.current?.querySelector<HTMLElement>(`[data-wi="${active}"]`);
    const b = box.current;
    if (!el || !b) return;
    const top = el.offsetTop - b.offsetTop;
    if (top < b.scrollTop + 16 || top > b.scrollTop + b.clientHeight - 48) b.scrollTo({ top: Math.max(0, top - 40), behavior: 'smooth' });
  }, [active, sel.range]);

  const openCleanup = () => {
    void dialog
      .open<CleanupResult | undefined>((close) => <CleanupReview editor={live} projectId={data.project.id} assets={assets} onDone={(r) => close(r)} />, { title: 'Remove ums and restarts', size: 'lg' })
      .then((r) => {
        if (r?.count) toast({ tone: 'success', title: `Took out ${plural(r.count, 'um and restart', 'ums and restarts')}`, body: `The video is ${shorter(r.seconds)} shorter.`, action: { label: 'Undo', onClick: () => live.undo() } });
      });
  };
  // Long pauses between words anywhere in the cut (D236), each heard shortened first.
  const openPauses = () => {
    void dialog
      .open<CleanupResult | undefined>((close) => <CleanupReview kind="pauses" editor={live} projectId={data.project.id} assets={assets} onDone={(r) => close(r)} />, { title: 'Tighten pauses', size: 'lg' })
      .then((r) => {
        if (r?.count) toast({ tone: 'success', title: `Shortened ${plural(r.count, 'long pause')}`, body: `The video is ${shorter(r.seconds)} shorter.`, action: { label: 'Undo', onClick: () => live.undo() } });
      });
  };

  const selection = useMemo(() => {
    if (!sel.range || !spec) return undefined;
    const [a, b] = sel.range;
    const ws = model.words.slice(a, b + 1);
    let secs = 0;
    try {
      secs = removalsFor(spec, tx, model, a, b).reduce((n, r) => n + r.seconds, 0);
    } catch {
      secs = 0;
    }
    // Words of one clip can be fixed (one transcript at a time).
    const one = ws.length > 0 && ws.every((w) => w.clip === ws[0]!.clip) ? model.clips[ws[0]!.clip] : undefined;
    const fix = one ? { assetId: one.clip.assetId, from: ws[0]!.from, to: ws[ws.length - 1]!.to } : undefined;
    return { text: ws.map((w) => w.text).join(' '), count: ws.length, secs, fix };
  }, [sel.range, spec, model, tx]);
  useEffect(() => setEditing(null), [sel.range?.[0], sel.range?.[1]]); // eslint-disable-line react-hooks/exhaustive-deps
  const saveWords = async (text: string) => {
    const f = selection?.fix;
    if (!f || !selection) return;
    setSaving(true);
    const out = await fixes.fix({ assetId: f.assetId, from: f.from, to: f.to, text, expect: selection.text, projectId: data.project.id });
    setSaving(false);
    if (out) {
      setEditing(null);
      sel.clear();
    }
  };
  const openFind = (initial = '') =>
    void dialog.open((close) => <FindReplace projectId={data.project.id} initial={initial} onDone={() => close()} />, { title: 'Find and replace in the transcripts', size: 'lg' });

  if (!spec) return <Skeleton className="h-56" />;
  const anyWords = model.words.length > 0;
  return (
    <div className="space-y-2.5" data-no-timeline-keys data-cut-transcript>
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="secondary" icon={<Sparkle size={13} />} onClick={openCleanup} disabled={!anyWords} data-cleanup-open>
          Remove ums and restarts
        </Button>
        <Button size="sm" variant="secondary" icon={<ArrowsInLineHorizontal size={13} />} onClick={openPauses} disabled={!anyWords} data-pauses-open>
          Tighten pauses
        </Button>
        <Button size="sm" variant="ghost" icon={<MagnifyingGlass size={13} />} onClick={() => openFind()} disabled={!anyWords} data-find-replace-open>
          Find and replace
        </Button>
        <Link to={`/p/${data.project.id}?tab=script`} className="ml-auto inline-flex items-center gap-1 text-[12px] font-medium text-accent-text hover:underline">
          <Scroll size={13} /> Read it as a script
        </Link>
      </div>
      <p id={hintId} className="text-[11.5px] leading-relaxed text-subtle">
        {coarse
          ? 'Tap a word, then tap another to select up to it. “Cut out” takes the selected words out, in the pauses around them; “Fix the words” retypes them.'
          : 'Click a word to go there; drag across words or Shift-click to select them. Delete cuts the selected words out, in the pauses around them; double-click a word to fix how it is written.'}
      </p>
      {loading && !anyWords ? (
        <div className="space-y-2 p-2">
          <Skeleton className="h-4 w-1/3" />
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-4 w-4/5" />
        </div>
      ) : !anyWords && !model.clips.some((c) => c.words.length) ? (
        <EmptyState compact icon={<ChatText size={20} />} title="Nothing is said in this cut" body="The words appear here once the footage in the cut has a transcript." />
      ) : null}
      <div
        ref={box}
        tabIndex={0}
        role="region"
        aria-label="What the cut says"
        aria-describedby={hintId}
        onWheel={() => (userScrollAt.current = Date.now())}
        onTouchMove={() => (userScrollAt.current = Date.now())}
        onDoubleClick={(e) => {
          // Double-click a word to fix how it is written.
          const n = Number((e.target as HTMLElement).closest<HTMLElement>('[data-wi]')?.dataset.wi);
          if (!Number.isFinite(n)) return;
          sel.select(n, n);
          setTimeout(() => setEditing('words'), 0);
        }}
        className={cx('relative max-h-[min(56dvh,560px)] select-none overflow-y-auto rounded-[12px] px-2 py-1 outline-none hairline focus-visible:outline-2 focus-visible:outline-accent', !anyWords && 'hidden')}
        {...sel.containerProps}
      >
        {model.clips.map((cc, i) => (
          <ClipWords
            key={cc.clip.id}
            cc={cc}
            prevName={lastNames[i]}
            title={clipTitle(cc.clip, assets)}
            hasAudio={!!assets.get(cc.clip.assetId)?.hasAudio}
            tx={txOut.get(cc.clip.assetId)}
            said={spec.mix?.said?.filter((s) => s.assetId === cc.clip.assetId)}
            range={sel.range}
            active={active}
            colorOf={colorOf}
            fixing={fixing}
            setFixing={setFixing}
            onSeek={onSeek}
            onHistory={() => void dialog.open(() => <TranscriptHistory assetId={cc.clip.assetId} />, { title: `Fixes in ${clipTitle(cc.clip, assets)}`, size: 'lg' })}
          />
        ))}
      </div>
      {sel.range && selection ? (
        <div
          role="status"
          aria-live="polite"
          className={cx(
            'sticky bottom-3 z-10 flex flex-wrap items-center gap-2 rounded-[12px] bg-raised px-3 py-2 shadow-[var(--shadow-float)] transition-opacity hairline',
            sel.dragging && 'pointer-events-none opacity-0',
          )}
          data-word-selection
        >
          <span className="min-w-0 flex-1 truncate text-[12.5px] text-fg">
            “{quoteWords(selection.text, 8)}”
            <span className="ml-2 text-[11.5px] text-subtle">
              {plural(selection.count, 'word')}
              {selection.secs ? `, ${shorter(selection.secs)}` : ''}
              {sel.extending ? '. Tap another word to select up to it' : ''}
            </span>
          </span>
          <Button size="sm" variant="primary" icon={<Scissors size={13} />} onClick={() => sel.range && cutOut(sel.range)} data-cut-words>
            Cut out
          </Button>
          {selection.fix ? (
            <>
              <Button size="sm" variant="ghost" icon={<PencilSimple size={13} />} onClick={() => setEditing(editing === 'words' ? null : 'words')} aria-pressed={editing === 'words'} data-fix-words-open>
                Fix the words
              </Button>
              <Button size="sm" variant="ghost" icon={<UserSwitch size={13} />} onClick={() => setEditing(editing === 'speaker' ? null : 'speaker')} aria-pressed={editing === 'speaker'} data-who-says-open>
                Who says this?
              </Button>
            </>
          ) : (
            <span className="text-[11.5px] text-subtle">Fix one clip's words at a time.</span>
          )}
          <Button
            size="sm"
            variant="ghost"
            icon={<Play size={13} />}
            onClick={() => {
              const w = sel.range ? model.words[sel.range[0]] : undefined;
              if (!w) return;
              onSeek(Math.max(0, w.t0 - 0.3));
              player.play();
            }}
          >
            Play
          </Button>
          <button type="button" onClick={sel.clear} aria-label="Clear the selection" className="grid h-8 w-8 place-items-center rounded-[8px] text-subtle hover:bg-hover hover:text-fg">
            <X size={13} />
          </button>
          {editing === 'words' && selection.fix ? (
            <div className="w-full">
              <FixWordsEditor key={`${selection.fix.assetId}:${selection.fix.from}`} words={selection.text} busy={saving} onCancel={() => setEditing(null)} onSave={(t) => void saveWords(t)} />
            </div>
          ) : null}
          {editing === 'speaker' && selection.fix ? (
            <div className="w-full">
              <StretchSpeaker
                assetId={selection.fix.assetId}
                from={selection.fix.from}
                to={selection.fix.to}
                projectId={data.project.id}
                onDone={() => {
                  setEditing(null);
                  sel.clear();
                }}
              />
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/** One clip's words, a line per transcript segment, with who says it. */
function ClipWords({ cc, title, hasAudio, tx, said, range, active, colorOf, fixing, setFixing, onSeek, prevName, onHistory }: {
  cc: CutClipWords;
  /** Words the cut says differently, in the speaker's own voice copy (D308, mix.said). */
  said?: SaidSound[];
  /** Who speaks last in the clips before (a chip only stands out where the voice changes). */
  prevName?: string;
  title: string;
  hasAudio: boolean;
  tx?: TranscriptOut;
  range: [number, number] | null;
  active: number;
  colorOf: (i: number) => string;
  fixing: string | null;
  setFixing: (k: string | null) => void;
  onSeek: (s: number) => void;
  /** Opens the clip's transcript history (its fixes, undo). */
  onHistory: () => void;
}) {
  const voices = useMemo(() => voiceMap(tx), [tx]);
  // Words a person wrote (fixed by hand, find and replace or the spelling list).
  const fixedAt = useMemo(() => {
    const out = new Set<string>();
    for (const sg of tx?.segments ?? []) for (const w of sg.words ?? []) if (w.x) out.add(`${w.f}:${w.t}`);
    return out;
  }, [tx]);
  const fixCount = tx?.fixes?.fixes ?? 0;
  const lines = useMemo(() => {
    const out: { seg: number; words: CutWord[] }[] = [];
    for (const w of cc.words) {
      const last = out[out.length - 1];
      if (last && last.seg === w.seg) last.words.push(w);
      else out.push({ seg: w.seg, words: [w] });
    }
    return out;
  }, [cc.words]);
  const why = cc.clip.muted
    ? 'Its own sound is off in the cut.'
    : !hasAudio
      ? 'No sound.'
      : !tx
        ? 'Loading what is said…'
        : tx.status?.state !== 'available'
          ? tx.status?.state === 'running'
            ? 'Being transcribed now.'
            : 'No transcript yet.'
          : 'No one speaks here.';
  let prevSpeaker = prevName;
  return (
    <section className="border-b border-line py-2 last:border-b-0" data-clip-words={cc.clip.id} aria-label={`Clip ${cc.index + 1}, ${title}`}>
      <div className="flex items-baseline gap-2 text-[11.5px] text-subtle">
        <span className="tc font-semibold text-muted">{cc.index + 1}</span>
        <button type="button" onClick={() => onSeek(cc.start + 0.001)} className="min-w-0 truncate font-medium text-muted hover:text-fg">
          {title}
        </button>
        <span className="tc shrink-0">{clock(cc.start)}</span>
        {fixCount ? (
          <button type="button" onClick={onHistory} className="ml-auto inline-flex shrink-0 items-center gap-1 rounded-[6px] px-1 text-[11.5px] text-subtle hover:bg-hover hover:text-fg" data-clip-history>
            <ClockCounterClockwise size={11} /> {plural(fixCount, 'fix', 'fixes')}
          </button>
        ) : null}
      </div>
      {lines.length === 0 ? <p className="pt-0.5 text-[12.5px] italic text-subtle">{why}</p> : null}
      {lines.map((line) => {
        const seg = tx?.segments[line.seg];
        const v = seg?.speaker ? voices.get(seg.speaker) : undefined;
        const name = seg?.speakerName || v?.displayName;
        const repeat = !!name && name === prevSpeaker;
        prevSpeaker = name;
        const key = `${cc.clip.id}:${line.seg}:${line.words[0]!.n}`;
        return (
          <div key={key} className="mt-1">
            <div className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-2">
              <p className="text-[14px] leading-[1.8] text-fg" style={v ? { boxShadow: `inset 2px 0 0 ${colorOf(v.color)}`, paddingLeft: 8 } : undefined}>
                {line.words.map((w, i) => {
                  const on = !!range && w.n >= range[0] && w.n <= range[1];
                  const fixed = fixedAt.has(`${w.from}:${w.to}`);
                  // Words the cut says differently: the new words in place of the old, marked (D311).
                  const s = saidOf(said, w);
                  if (s) {
                    if (i > 0 && saidOf(said, line.words[i - 1]!) === s) return <span key={w.n} data-wi={w.n} hidden />;
                    return (
                      <span
                        key={w.n}
                        data-wi={w.n}
                        data-selected={on || undefined}
                        data-said
                        title={`${timecode(w.t0)}. Changed with AI in ${s.who || 'the speaker'}’s own voice copy, with their consent; what was said: “${s.before}”.`}
                        className={cx(
                          'cursor-text rounded-[3px] py-[1px] underline decoration-wavy decoration-accent decoration-[1.5px] underline-offset-[5px]',
                          on ? 'bg-accent text-accent-ink' : w.n === active ? 'bg-accent-soft text-fg' : 'hover:bg-hover',
                        )}
                      >
                        {s.words}
                        {i < line.words.length - 1 ? ' ' : ''}
                      </span>
                    );
                  }
                  return (
                    <span
                      key={w.n}
                      data-wi={w.n}
                      data-selected={on || undefined}
                      data-fixed={fixed || undefined}
                      title={fixed ? `${timecode(w.t0)}, fixed by a person` : timecode(w.t0)}
                      className={cx(
                        'cursor-text rounded-[3px] py-[1px]',
                        on ? 'bg-accent text-accent-ink' : w.n === active ? 'bg-accent-soft text-fg' : 'hover:bg-hover',
                        w.approx && !on && 'text-muted',
                        fixed && !on && 'underline decoration-accent/60 decoration-[1.5px] underline-offset-[5px]',
                      )}
                    >
                      {w.text}
                      {i < line.words.length - 1 ? ' ' : ''}
                    </span>
                  );
                })}
              </p>
              {seg && name && v ? (
                <LineChip
                  name={name}
                  color={colorOf(v.color)}
                  open={fixing === key}
                  onClick={() => setFixing(fixing === key ? null : key)}
                  className={cx('mt-[3px]', repeat && fixing !== key && 'opacity-45 hover:opacity-100 focus-visible:opacity-100')}
                />
              ) : null}
            </div>
            {fixing === key && seg ? <LineFix assetId={cc.clip.assetId} seg={seg} current={v} onDone={() => setFixing(null)} /> : null}
          </div>
        );
      })}
    </section>
  );
}

/** The change a word of the cut is part of, when the cut says it differently (D311). */
function saidOf(said: SaidSound[] | undefined, w: CutWord): SaidSound | undefined {
  if (!said?.length) return undefined;
  const mid = (w.from + w.to) / 2;
  return said.find((s) => {
    const edge = Math.min(0.1, (s.to - s.from) / 2);
    return mid >= s.from + edge && mid <= s.to - edge;
  });
}
