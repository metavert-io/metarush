import { useEffect, useId, useMemo, useState } from 'react';
import { MagnifyingGlass } from '@phosphor-icons/react';
import { useOp } from '../../../api/queries';
import type { Anchor, AssetSummary, NarrationLine, RenderPlan, Spec } from '../../../api/types';
import { Button, cx, Skeleton } from '../../../ui/primitives';
import { clock, parseClock } from './api';

/*
 * Where a line starts, the way an editor says it: with a shot ("when Kitchen
 * starts, +0.5 s"), right after the line before it, at the moment someone
 * says something, or at an exact time. The first three follow the edit when
 * shots move; the exact time stays put.
 */

type Kind = 'clip' | 'after' | 'moment' | 'time';

const KINDS: { kind: Kind; label: string }[] = [
  { kind: 'clip', label: 'With a shot' },
  { kind: 'after', label: 'After the line before' },
  { kind: 'moment', label: 'When someone says…' },
  { kind: 'time', label: 'At an exact time' },
];

export function shotName(spec: Spec, id: string | undefined): string {
  const i = spec.clips.findIndex((c) => c.id === id);
  if (i < 0) return 'a shot';
  const c = spec.clips[i]!;
  return c.label ? `“${c.label}”` : `shot ${i + 1}`;
}

function offsetWords(o: number): string {
  if (Math.abs(o) < 0.05) return '';
  return o > 0 ? `, +${o.toFixed(1)} s` : `, ${o.toFixed(1)} s`;
}

/** An anchor in plain words (what the editor shows while choosing). */
export function anchorWords(spec: Spec, a: Anchor): string {
  switch (a.kind) {
    case 'clip':
      return `when ${shotName(spec, a.clipId)} ${a.edge === 'end' ? 'ends' : 'starts'}${offsetWords(a.offset ?? 0)}`;
    case 'after':
      return `${(a.gap ?? 0.3).toFixed(1)} s after the line before`;
    case 'moment':
      return `${a.quote ? `when ${a.quote}` : 'at a moment in the footage'}${offsetWords(a.offset ?? 0)}`;
    case 'time':
      return `at exactly ${clock((a.at ?? 0) + (a.offset ?? 0))}`;
  }
  return 'not placed';
}

export function PlacementEditor({ line, spec, plan, assets, onSave, onCancel, busy }: {
  line: NarrationLine;
  spec: Spec;
  plan?: RenderPlan;
  assets: Map<string, AssetSummary>;
  onSave: (a: Anchor) => void;
  onCancel: () => void;
  busy?: boolean;
}) {
  const cur = line.anchor;
  const [kind, setKind] = useState<Kind>((cur.kind as Kind) ?? 'clip');
  const firstClip = spec.clips[0]?.id ?? '';
  const [clipId, setClipId] = useState(cur.kind === 'clip' || cur.kind === 'moment' ? (cur.clipId ?? firstClip) : clipAt(plan, line.start) ?? firstClip);
  const [edge, setEdge] = useState<'start' | 'end'>(cur.edge === 'end' ? 'end' : 'start');
  const [offset, setOffset] = useState(String(cur.kind === 'clip' || cur.kind === 'moment' ? (cur.offset ?? 0) : 0));
  const [gap, setGap] = useState(String(cur.kind === 'after' ? (cur.gap ?? 0.3) : 0.3));
  const [at, setAt] = useState(clock(cur.kind === 'time' ? (cur.at ?? 0) + (cur.offset ?? 0) : line.start));
  const [moment, setMoment] = useState<{ assetId: string; at: number; quote: string } | null>(
    cur.kind === 'moment' && cur.assetId && cur.at !== undefined ? { assetId: cur.assetId, at: cur.at, quote: cur.quote ?? '' } : null,
  );
  const ids = { clip: useId(), edge: useId(), off: useId(), gap: useId(), at: useId() };

  const anchor = useMemo((): Anchor | null => {
    const off = Number(offset);
    switch (kind) {
      case 'clip':
        if (!clipId || !Number.isFinite(off)) return null;
        return { kind: 'clip', clipId, ...(edge === 'end' ? { edge: 'end' } : {}), ...(Math.abs(off) >= 0.05 ? { offset: Math.round(off * 10) / 10 } : {}) };
      case 'after': {
        const g = Number(gap);
        if (!Number.isFinite(g) || g < 0 || g > 10) return null;
        return { kind: 'after', gap: Math.round(g * 10) / 10 };
      }
      case 'moment':
        if (!moment || !Number.isFinite(off)) return null;
        return { kind: 'moment', assetId: moment.assetId, at: moment.at, clipId, quote: moment.quote, ...(Math.abs(off) >= 0.05 ? { offset: Math.round(off * 10) / 10 } : {}) };
      case 'time': {
        const t = parseClock(at);
        return t === null ? null : { kind: 'time', at: t };
      }
    }
  }, [kind, clipId, edge, offset, gap, at, moment]);

  return (
    <form
      className="mt-3 rounded-[12px] bg-hover/50 p-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (anchor) onSave(anchor);
      }}
      aria-label={`Where line ${line.line} starts`}
    >
      <div role="radiogroup" aria-label="Start it" className="flex flex-wrap gap-1.5">
        {KINDS.map((k) => (
          <button
            key={k.kind}
            type="button"
            role="radio"
            aria-checked={kind === k.kind}
            onClick={() => {
              // A spoken moment starts right on its word unless asked otherwise.
              if (k.kind === 'moment' && kind !== 'moment') setOffset('0');
              setKind(k.kind);
            }}
            className={cx(
              'h-7 rounded-full px-2.5 text-[12.5px] font-medium transition-colors',
              kind === k.kind ? 'bg-accent text-accent-ink' : 'bg-panel text-muted hairline hover:text-fg',
            )}
          >
            {k.label}
          </button>
        ))}
      </div>

      <div className="mt-3 flex flex-wrap items-end gap-3">
        {kind === 'clip' || kind === 'moment' ? (
          <div>
            <label htmlFor={ids.clip} className="block text-[11.5px] text-subtle">
              Shot
            </label>
            <select
              id={ids.clip}
              value={clipId}
              onChange={(e) => {
                setClipId(e.target.value);
                setMoment(null);
              }}
              className="mt-1 h-8 max-w-[220px] rounded-[9px] bg-panel px-2 text-[13px] text-fg hairline"
            >
              {spec.clips.map((c, i) => (
                <option key={c.id} value={c.id}>
                  {i + 1}. {c.label || `Shot ${i + 1}`}
                </option>
              ))}
            </select>
          </div>
        ) : null}
        {kind === 'clip' ? (
          <div>
            <label htmlFor={ids.edge} className="block text-[11.5px] text-subtle">
              When it
            </label>
            <select id={ids.edge} value={edge} onChange={(e) => setEdge(e.target.value as 'start' | 'end')} className="mt-1 h-8 rounded-[9px] bg-panel px-2 text-[13px] text-fg hairline">
              <option value="start">starts</option>
              <option value="end">ends</option>
            </select>
          </div>
        ) : null}
        {kind === 'clip' || kind === 'moment' ? (
          <div>
            <label htmlFor={ids.off} className="block text-[11.5px] text-subtle">
              Plus seconds
            </label>
            <input
              id={ids.off}
              type="number"
              step={0.1}
              min={-10}
              max={30}
              inputMode="decimal"
              value={offset}
              onChange={(e) => setOffset(e.target.value)}
              className="tc mt-1 h-8 w-[84px] rounded-[9px] bg-panel px-2 text-[13px] text-fg hairline"
            />
          </div>
        ) : null}
        {kind === 'after' ? (
          <div>
            <label htmlFor={ids.gap} className="block text-[11.5px] text-subtle">
              Pause before it (seconds)
            </label>
            <input
              id={ids.gap}
              type="number"
              step={0.1}
              min={0}
              max={10}
              inputMode="decimal"
              value={gap}
              onChange={(e) => setGap(e.target.value)}
              className="tc mt-1 h-8 w-[84px] rounded-[9px] bg-panel px-2 text-[13px] text-fg hairline"
            />
          </div>
        ) : null}
        {kind === 'time' ? (
          <div>
            <label htmlFor={ids.at} className="block text-[11.5px] text-subtle">
              Time in the video
            </label>
            <input
              id={ids.at}
              value={at}
              onChange={(e) => setAt(e.target.value)}
              placeholder="0:12.4"
              className="tc mt-1 h-8 w-[100px] rounded-[9px] bg-panel px-2 text-[13px] text-fg hairline"
            />
          </div>
        ) : null}
      </div>

      {kind === 'moment' ? <MomentPicker spec={spec} clipId={clipId} assets={assets} value={moment} onPick={setMoment} /> : null}

      <p className="mt-3 text-[12.5px] text-muted" aria-live="polite">
        {anchor ? (
          <>
            Starts <span className="font-medium text-fg">{anchorWords(spec, anchor)}</span>
            {kind === 'time' ? <span className="text-subtle"> (it stays there when shots move)</span> : <span className="text-subtle"> (it moves with the edit)</span>}
          </>
        ) : kind === 'moment' ? (
          'Pick the word the line should start on.'
        ) : (
          'Fill in where it starts.'
        )}
      </p>
      <div className="mt-3 flex justify-end gap-2">
        <Button size="sm" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button size="sm" variant="primary" type="submit" disabled={!anchor} loading={busy}>
          Save start point
        </Button>
      </div>
    </form>
  );
}

function clipAt(plan: RenderPlan | undefined, t: number): string | undefined {
  if (!plan) return undefined;
  const f = Math.round(t * plan.fps);
  let hit: RenderPlan['clips'][number] | undefined;
  for (const c of plan.clips) if (f >= c.from && f < c.from + c.durationInFrames && (!hit || c.from >= hit.from)) hit = c;
  return hit?.id;
}

/** The words spoken in one shot; picking one starts the line there. */
function MomentPicker({ spec, clipId, assets, value, onPick }: {
  spec: Spec;
  clipId: string;
  assets: Map<string, AssetSummary>;
  value: { assetId: string; at: number; quote: string } | null;
  onPick: (m: { assetId: string; at: number; quote: string }) => void;
}) {
  const clip = spec.clips.find((c) => c.id === clipId);
  const tx = useOp('footage.transcript', clip ? { assetId: clip.assetId, from: clip.in, to: clip.out, words: true } : null, { staleTime: 60_000 });
  const [q, setQ] = useState('');
  const searchId = useId();
  useEffect(() => setQ(''), [clipId]);
  if (!clip) return null;
  const segs = tx.data?.segments ?? [];
  const words = segs.flatMap((s) =>
    (s.words ?? []).filter((w) => w.f >= clip.in - 0.05 && w.f < clip.out).map((w) => ({ ...w, who: s.speakerName ?? '' })),
  );
  const shown = q.trim() ? words.filter((w) => w.w.toLowerCase().includes(q.trim().toLowerCase())) : words;
  const quoteFor = (i: number) => {
    const w = words[i]!;
    const phrase = words
      .slice(i, i + 4)
      .map((x) => x.w)
      .join(' ')
      .replace(/[.,!?;:]+$/, '');
    return `${w.who ? `${w.who} says` : 'someone says'} “${phrase}…”`;
  };
  return (
    <div className="mt-3">
      {tx.isLoading ? (
        <Skeleton className="h-16" />
      ) : words.length === 0 ? (
        <p className="text-[12.5px] text-subtle">
          Nobody speaks in {shotName(spec, clipId)} (or {assets.get(clip.assetId)?.title ?? 'its footage'} has no transcript yet). Pick another shot.
        </p>
      ) : (
        <>
          <div className="relative w-full sm:w-[260px]">
            <label htmlFor={searchId} className="sr-only">
              Find a word
            </label>
            <MagnifyingGlass size={13} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-subtle" aria-hidden />
            <input
              id={searchId}
              type="search"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="Find a word"
              className="h-8 w-full rounded-[9px] bg-panel pl-7 pr-2 text-[12.5px] text-fg hairline placeholder:text-subtle"
            />
          </div>
          <div className="mt-2 max-h-[150px] overflow-y-auto rounded-[10px] bg-panel p-2 text-[13px] leading-[1.9] hairline" role="listbox" aria-label="Words in this shot">
            {shown.map((w) => {
              const i = words.indexOf(w);
              const on = value?.assetId === clip.assetId && Math.abs(value.at - w.f) < 0.001;
              return (
                <button
                  key={`${w.f}-${i}`}
                  type="button"
                  role="option"
                  aria-selected={on}
                  onClick={() => onPick({ assetId: clip.assetId, at: Math.round(w.f * 1000) / 1000, quote: quoteFor(i) })}
                  className={cx('mr-1 rounded-[5px] px-1 transition-colors', on ? 'bg-accent text-accent-ink' : 'text-fg hover:bg-hover')}
                  title={`at ${clock(w.f - clip.in)} into the shot`}
                >
                  {w.w}
                </button>
              );
            })}
          </div>
        </>
      )}
    </div>
  );
}
