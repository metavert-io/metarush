import { lazy, Suspense, useEffect, useMemo, useRef, useState } from 'react';
import { Check, Info, Sparkle, X } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import type { ops } from '@metarush/schema';
import type { AssetSummary, RenderPlan } from '../../api/types';
import { clock, plural } from '../../lib/format';
import { cutPoint, OpError, removalOps, shorter } from '../../lib/words';
import { Badge, Button, cx, EmptyState, Spinner } from '../../ui/primitives';
import { plainRefusal } from '../../lib/editops';
import type { TimelineEditor as Editor } from './useTimelineEditor';

const LoopPreview = lazy(() => import('../../player/LoopPreview'));

export interface CleanupResult {
  count: number;
  seconds: number;
}

type WordRemoval = ops.WordRemoval;

const KIND: Record<string, string> = { filler: 'Filler sound', repeat: 'Said twice', 'false-start': 'False start', pause: 'Long pause' };

/** What the review looks for: ums and restarts, or long pauses (D236). */
export type CleanupKind = 'ums' | 'pauses';

type Decision = 'take' | 'keep' | undefined;

/**
 * "Remove ums and restarts": MetaRush suggests filler sounds (um, uh), words
 * said twice in a row and false starts in what the cut says; you listen to each
 * one with it taken out (or as it is now) and take it out or keep it, or take
 * out all the rest. What you take out goes on the timeline as one change.
 * "Tighten pauses" (kind pauses, D236) is the same review for the long pauses
 * between words: each one heard shortened first, longer than and shortened to
 * lengths you choose.
 */
export function CleanupReview({
  editor,
  projectId,
  assets,
  onDone,
  kind = 'ums',
}: {
  editor: Editor;
  projectId: string;
  assets: Map<string, AssetSummary>;
  onDone: (r?: CleanupResult) => void;
  kind?: CleanupKind;
}) {
  const pauses = kind === 'pauses';
  const [lengths, setLengths] = useState({ longer: 1, to: 0.5 });
  const [ask, setAsk] = useState(0);
  const [phase, setPhase] = useState<'loading' | 'none' | 'review' | 'error'>('loading');
  const [error, setError] = useState<string>();
  const [items, setItems] = useState<WordRemoval[]>([]);
  const [decisions, setDecisions] = useState<Decision[]>([]);
  const [cur, setCur] = useState(0);
  const [hear, setHear] = useState<'without' | 'now'>('without');
  const [without, setWithout] = useState<{ index: number; plan?: RenderPlan }>();
  const [busy, setBusy] = useState(false);
  const baseId = useRef<string | undefined>(undefined);
  const applyRef = useRef<HTMLButtonElement>(null);

  // Save what is unsaved, then ask for suggestions on that version.
  useEffect(() => {
    let gone = false;
    setPhase('loading');
    void (async () => {
      if (!(await editor.flush())) {
        if (gone) return;
        setPhase('error');
        setError('Your latest changes on the timeline need attention first (see the message under the timeline). Fix or undo them, then try again.');
        return;
      }
      try {
        const out = pauses
          ? await call('cut.pauses', { projectId, longerThan: lengths.longer, to: lengths.to, dryRun: true })
          : await call('cut.removeWords', { projectId, cleanup: true, dryRun: true });
        if (gone) return;
        const planBase = (out.plan as { baseRevisionId?: string }).baseRevisionId;
        if (planBase && editor.base && planBase !== editor.base.id) {
          setPhase('error');
          setError('The cut just changed. Close this and try again in a moment.');
          return;
        }
        baseId.current = planBase ?? editor.base?.id;
        const rs = out.removals ?? [];
        setItems(rs);
        setDecisions(rs.map(() => undefined));
        setCur(0);
        setPhase(rs.length ? 'review' : 'none');
      } catch (e) {
        if (gone) return;
        const ae = toApiError(e);
        if (ae.code === 'E_NOT_FOUND') setPhase('none');
        else {
          setPhase('error');
          setError([ae.message, ae.hint].filter(Boolean).join(' '));
        }
      }
    })();
    return () => {
      gone = true;
    };
  }, [ask]); // eslint-disable-line react-hooks/exhaustive-deps

  const r = items[cur];
  const spec = editor.spec;

  // The current suggestion taken out, as Go plays it.
  useEffect(() => {
    if (phase !== 'review' || !r || !spec) return;
    let gone = false;
    let ops;
    try {
      ops = removalOps(spec, [r]);
    } catch {
      setWithout({ index: cur });
      return;
    }
    setBusy(true);
    const t = setTimeout(() => {
      editor
        .previewWith(ops)
        .then((out) => !gone && setWithout({ index: cur, plan: out.renderPlan ?? undefined }))
        .catch(() => !gone && setWithout({ index: cur }))
        .finally(() => !gone && setBusy(false));
    }, 120);
    return () => {
      gone = true;
      clearTimeout(t);
    };
  }, [cur, phase]); // eslint-disable-line react-hooks/exhaustive-deps

  const counts = useMemo(() => {
    const by: Record<string, number> = {};
    for (const x of items) by[x.kind] = (by[x.kind] ?? 0) + 1;
    return by;
  }, [items]);
  const taken = items.filter((_, i) => decisions[i] === 'take');
  const takenSecs = taken.reduce((n, x) => n + x.seconds, 0);
  const undecided = decisions.findIndex((d) => d === undefined);

  const decide = (d: Decision, i = cur) => {
    const next = decisions.slice();
    next[i] = d;
    setDecisions(next);
    // On to the next one still to decide (after this one, then from the start).
    const after = next.findIndex((x, k) => k > i && x === undefined);
    const any = next.findIndex((x) => x === undefined);
    const to = after >= 0 ? after : any;
    if (to >= 0) {
      setCur(to);
      setHear('without');
    } else requestAnimationFrame(() => applyRef.current?.focus());
  };

  const takeRest = () => {
    setDecisions(decisions.map((d) => d ?? 'take'));
    requestAnimationFrame(() => applyRef.current?.focus());
  };

  const apply = () => {
    if (!taken.length) return;
    if (editor.base?.id !== baseId.current || editor.dirty) {
      setError('The cut changed while you were choosing. Close this and look again.');
      return;
    }
    const s = editor.spec;
    if (!s) return;
    let ops;
    try {
      ops = removalOps(s, taken);
    } catch (e) {
      setError(e instanceof OpError ? plainRefusal(e.message) : 'Those could not be taken out.');
      return;
    }
    const err = editor.apply(pauses ? `Shortened ${plural(taken.length, 'long pause')}` : `Took out ${plural(taken.length, 'um and restart', 'ums and restarts')}`, ops);
    if (err) {
      setError(err);
      return;
    }
    onDone({ count: taken.length, seconds: takenSecs });
  };

  const lengthsRow = pauses ? (
    <PauseLengths
      value={lengths}
      busy={phase === 'loading'}
      onLook={(v) => {
        setLengths(v);
        setAsk((n) => n + 1);
      }}
    />
  ) : null;
  if (phase === 'loading')
    return (
      <div className="space-y-4" data-no-timeline-keys>
        {lengthsRow}
        <div className="grid min-h-[200px] place-items-center">
          <span className="flex items-center gap-2 text-[13px] text-muted">
            <Spinner size={14} /> {pauses ? 'Listening for long pauses between words…' : 'Listening for ums, repeated words and false starts…'}
          </span>
        </div>
      </div>
    );
  if (phase === 'none')
    return (
      <div className="space-y-4" data-no-timeline-keys data-cleanup-review="none">
        {lengthsRow}
        {pauses ? (
          <EmptyState compact icon={<Sparkle size={20} />} title="Nothing to tighten" body={`No pauses longer than ${secs(lengths.longer)} between words in what the cut says.`} />
        ) : (
          <EmptyState compact icon={<Sparkle size={20} />} title="Nothing to clean up" body="No ums, words said twice or false starts were found in what the cut says." />
        )}
        <div className="flex justify-end">
          <Button variant="primary" onClick={() => onDone()}>
            Done
          </Button>
        </div>
      </div>
    );
  if (phase === 'error')
    return (
      <div className="space-y-4" data-no-timeline-keys data-cleanup-review="error">
        <p className="rounded-[12px] bg-danger-soft px-4 py-3 text-[13px] leading-relaxed text-fg">{error}</p>
        <div className="flex justify-end">
          <Button variant="secondary" onClick={() => onDone()}>
            Close
          </Button>
        </div>
      </div>
    );

  const fps = spec?.output.fps || 30;
  const t0 = r && spec ? cutPoint(spec, r) : 0;
  const plan = hear === 'without' ? (without?.index === cur ? without.plan : undefined) : (editor.plan ?? undefined);
  const inFrame = Math.round((t0 - 2) * fps);
  const outFrame = Math.round((t0 + (hear === 'without' ? 0 : (r?.seconds ?? 0)) + 1.5) * fps);
  const vertical = spec ? (spec.output.height ?? 1920) > (spec.output.width ?? 1080) : false;
  const clip = r && spec ? spec.clips[r.clip - 1] : undefined;
  const where = clip ? clip.label || assets.get(clip.assetId)?.title || clip.id : '';
  const kinds = Object.entries(counts).map(([k, n]) => {
    const name =
      k === 'filler' ? plural(n, 'filler sound') : k === 'repeat' ? plural(n, 'word said twice', 'words said twice') : k === 'pause' ? plural(n, 'long pause') : plural(n, 'false start');
    return name;
  });
  const allSecs = items.reduce((n, x) => n + x.seconds, 0);
  const verb = pauses ? 'Shorten' : 'Take out';

  return (
    <div className="space-y-4" data-no-timeline-keys data-cleanup-review="review" data-cleanup-kind={kind}>
      {lengthsRow}
      <p className="text-[13px] leading-relaxed text-muted">
        Found {kinds.length > 1 ? `${kinds.slice(0, -1).join(', ')} and ${kinds[kinds.length - 1]}` : kinds[0]}
        {pauses ? ` (${shorter(allSecs)} in all)` : ''}. Listen to each one and choose; nothing changes until you {pauses ? 'shorten them' : 'take them out'}.
      </p>
      {r ? (
        <div className={cx('grid gap-4', vertical ? 'sm:grid-cols-[200px_minmax(0,1fr)]' : 'sm:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)]')}>
          <div className={cx('relative overflow-hidden rounded-[12px] bg-inset', vertical ? 'mx-auto aspect-[9/16] w-[180px] sm:w-full' : 'aspect-video w-full')} data-cleanup-preview={hear}>
            <Suspense
              fallback={
                <div className="grid h-full place-items-center">
                  <Spinner size={14} />
                </div>
              }
            >
              <LoopPreview key={`${cur}:${hear}`} plan={plan} inFrame={inFrame} outFrame={outFrame} busy={busy && hear === 'without'} />
            </Suspense>
          </div>
          <div className="min-w-0 space-y-3">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-[12px] font-medium text-subtle">
                {cur + 1} of {items.length}
              </span>
              <Badge tone="accent">{KIND[r.kind] ?? 'Suggestion'}</Badge>
              {decisions[cur] ? (
                <span className="text-[12px] text-subtle">{decisions[cur] === 'take' ? (pauses ? 'Shortening it' : 'Taking it out') : pauses ? 'Keeping it as it is' : 'Keeping it'}</span>
              ) : null}
            </div>
            {r.kind === 'pause' ? (
              <p className="text-[15px] leading-relaxed text-fg" data-cleanup-words>
                {r.before ? <span className="text-muted">…{r.before} </span> : null}
                <span className="mx-0.5 inline-flex items-center rounded-full bg-accent-soft px-2 align-middle text-[11.5px] font-medium text-accent-text">{secs(r.wordsTo - r.wordsFrom)} of quiet</span>
                {r.after ? <span className="text-muted"> {r.after}…</span> : null}
              </p>
            ) : (
              <p className="text-[15px] leading-relaxed text-fg" data-cleanup-words>
                {r.before ? <span className="text-muted">…{r.before} </span> : null}
                <del className="rounded-[4px] bg-danger-soft px-1 text-fg decoration-danger decoration-2">{r.words}</del>
                {r.after ? <span className="text-muted"> {r.after}…</span> : null}
              </p>
            )}
            <p className="text-[12px] leading-relaxed text-subtle">
              {r.reason.charAt(0).toUpperCase() + r.reason.slice(1)}
              {r.speaker ? `, ${r.speaker}` : ''}, in {where} at {clock(r.at)}.{' '}
              {pauses ? `Shortening it to ${secs(lengths.to)} makes the video ${shorter(r.seconds)} shorter.` : `Taking it out makes the video ${shorter(r.seconds)} shorter.`}
            </p>
            <div role="group" aria-label="What to hear" className="inline-flex rounded-full p-0.5 hairline">
              {(
                [
                  ['without', pauses ? 'Shortened' : 'Without it'],
                  ['now', 'As it is now'],
                ] as const
              ).map(([k, label]) => (
                <button
                  key={k}
                  type="button"
                  aria-pressed={hear === k}
                  onClick={() => setHear(k)}
                  className={cx('h-7 rounded-full px-3 text-[12px] transition-colors', hear === k ? 'bg-panel font-medium text-fg shadow-sm' : 'text-muted hover:text-fg')}
                >
                  {label}
                </button>
              ))}
            </div>
            <div className="flex flex-wrap gap-2">
              <Button variant="primary" icon={<Check size={14} />} onClick={() => decide('take')} data-cleanup-take>
                {pauses ? 'Shorten it' : 'Take it out'}
              </Button>
              <Button variant="secondary" onClick={() => decide('keep')} data-cleanup-keep>
                {pauses ? 'Keep it as it is' : 'Keep it'}
              </Button>
            </div>
            {undecided >= 0 ? (
              <button type="button" onClick={takeRest} className="text-[12.5px] font-medium text-accent-text hover:underline" data-cleanup-all>
                {decisions.some(Boolean) ? `${verb} all the rest (${decisions.filter((d) => d === undefined).length})` : `${verb} all ${items.length}`}
              </button>
            ) : null}
          </div>
        </div>
      ) : null}

      <ol className="flex flex-wrap gap-1" aria-label="All suggestions">
        {items.map((x, i) => (
          <li key={`${x.clipId}:${x.from}`}>
            <button
              type="button"
              onClick={() => {
                setCur(i);
                setHear('without');
              }}
              aria-current={i === cur ? 'step' : undefined}
              aria-label={`Suggestion ${i + 1}, ${x.kind === 'pause' ? x.reason : `“${x.words}”`}${decisions[i] === 'take' ? (pauses ? ', shortening it' : ', taking it out') : decisions[i] === 'keep' ? ', keeping it' : ''}`}
              title={x.kind === 'pause' ? x.reason : `“${x.words}”`}
              className={cx(
                'inline-flex h-7 min-w-7 items-center justify-center gap-1 rounded-full px-2 text-[11.5px] transition-colors hairline',
                i === cur ? 'bg-accent-soft font-semibold text-fg ring-1 ring-accent-line' : 'text-muted hover:bg-hover',
              )}
            >
              {decisions[i] === 'take' ? <Check size={11} className="text-accent-text" /> : decisions[i] === 'keep' ? <X size={11} className="text-subtle" /> : null}
              {i + 1}
            </button>
          </li>
        ))}
      </ol>

      {error ? (
        <p className="flex items-start gap-2 rounded-[12px] bg-danger-soft px-3 py-2 text-[12.5px] text-fg">
          <Info size={14} className="mt-[2px] shrink-0" /> {error}
        </p>
      ) : null}

      <div className="sticky bottom-0 -mx-1 flex flex-wrap items-center justify-end gap-2 border-t border-line bg-panel px-1 pt-3">
        <span className="mr-auto text-[12px] text-subtle">
          {taken.length ? `${plural(taken.length, 'one')} to ${pauses ? 'shorten' : 'take out'}, ${shorter(takenSecs)} in all` : 'Nothing chosen yet'}
        </span>
        <Button variant="ghost" onClick={() => onDone()}>
          Cancel
        </Button>
        <Button ref={applyRef} variant="primary" disabled={!taken.length} onClick={apply} data-cleanup-apply>
          {taken.length ? `${verb} ${taken.length}` : verb}
        </Button>
      </div>
    </div>
  );
}

/** Seconds the way the review says them: 0.5 s, 1 s, 1.25 s. */
function secs(v: number): string {
  return `${Math.round(v * 100) / 100} s`;
}

/**
 * The two lengths "Tighten pauses" uses: pauses longer than one are shortened
 * to the other. Look again asks with the new lengths.
 */
function PauseLengths({ value, busy, onLook }: { value: { longer: number; to: number }; busy: boolean; onLook: (v: { longer: number; to: number }) => void }) {
  const [longer, setLonger] = useState(String(value.longer));
  const [to, setTo] = useState(String(value.to));
  const l = Number(longer);
  const t = Number(to);
  const ok = Number.isFinite(l) && Number.isFinite(t) && l >= 0.2 && l <= 30 && t >= 0.1 && t < l;
  const changed = l !== value.longer || t !== value.to;
  const field = 'h-8 w-16 rounded-[9px] bg-panel px-2 text-[13px] text-fg hairline';
  return (
    <form
      className="flex flex-wrap items-center gap-x-2 gap-y-1.5 text-[12.5px] text-muted"
      data-pause-lengths
      onSubmit={(e) => {
        e.preventDefault();
        if (ok && changed) onLook({ longer: l, to: t });
      }}
    >
      <label className="flex items-center gap-1.5">
        Pauses longer than
        <input className={field} inputMode="decimal" value={longer} onChange={(e) => setLonger(e.target.value)} aria-label="Pauses longer than, in seconds" data-pause-longer />s
      </label>
      <label className="flex items-center gap-1.5">
        get shortened to
        <input className={field} inputMode="decimal" value={to} onChange={(e) => setTo(e.target.value)} aria-label="Shortened to, in seconds" data-pause-to />s
      </label>
      <Button size="sm" variant="ghost" type="submit" disabled={!ok || !changed || busy} data-pause-look>
        Look again
      </Button>
      {!ok ? <span className="basis-full text-[11.5px] text-danger">Choose a pause length between 0.2 and 30 s, shortened to at least 0.1 s and less than that.</span> : null}
    </form>
  );
}
