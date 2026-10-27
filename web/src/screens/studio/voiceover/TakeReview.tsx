import { useMemo } from 'react';
import { ArrowCounterClockwise, CheckCircle, MagnetStraight, Play, Record, Stop, WarningCircle } from '@phosphor-icons/react';
import type { LineFit, NarrationOut, PlaceOut, RenderPlan } from '../../../api/types';
import { Button, cx } from '../../../ui/primitives';
import { clock } from './api';

/*
 * Review after a live take (D85): where each line landed on the narration lane
 * (and where it was planned), lines not heard, and — once it is in the cut —
 * lines that run into the next shot or past the end, each with one-click fixes.
 * "Snap lines to their planned spots" is a button, never automatic.
 */

export function TakeReview({ out, kept, busy, show, plan, duration, playing, onKeep, onSnap, onAgain, onDone, onFix, onPlay }: {
  out: PlaceOut;
  /** It is in the cut (a new version). */
  kept: boolean;
  /** What is being done: keep, snap, back, fix:<line>:<strategy>. */
  busy: string | null;
  /** The narration of the cut now (after keeping: the flags). */
  show?: NarrationOut;
  plan?: RenderPlan;
  duration: number;
  playing: boolean;
  onKeep: () => void;
  /** Each line of the take to its planned spot (planned) or back where it was said. */
  onSnap: (where: 'planned' | 'recorded') => void;
  onAgain: () => void;
  onDone: () => void;
  onFix: (beatId: string, strategy: string, summary: string) => void;
  onPlay: () => void;
}) {
  const placed = out.lines.filter((l) => l.state === 'placed');
  const snapped = out.where === 'planned';
  const canSnap = placed.some((l) => l.moved != null && Math.abs(l.moved) >= 0.1);
  const readLines = new Set(out.lines.map((l) => l.beatId));
  const flags = useMemo<LineFit[]>(
    () => (kept && show ? show.fit.filter((f) => readLines.has(f.beatId) && (f.status === 'over' || f.status === 'unresolved' || (f.intoNextShot ?? 0) > 0)) : []),
    [kept, show, out], // eslint-disable-line react-hooks/exhaustive-deps
  );
  const summary = out.summary.replace(/ Apply the change to put it in the cut\.$/, '').replace(/ It is in the cut now as version \d+\.$/, '');
  return (
    <div className="flex min-w-0 flex-1 flex-col gap-4 rounded-[18px] bg-white/[0.035] p-5 ring-1 ring-inset ring-white/[0.06] sm:p-6" data-take-review={kept ? 'kept' : 'new'}>
      <div>
        <h3 className="flex items-center gap-2 text-[16px] font-semibold text-white">
          {kept ? <CheckCircle size={18} weight="fill" className="text-[#92c79c]" /> : null}
          {kept ? (snapped ? 'Your lines are at their planned spots' : 'Your take is in the cut') : 'Your take'}
        </h3>
        <p className="mt-1 text-[13.5px] leading-relaxed text-[#b5aca2]">{summary}</p>
      </div>

      <PlacedLane out={out} plan={plan} duration={duration} />

      <ol className="space-y-1.5" aria-label="Where each line landed">
        {out.lines.map((l) => {
          const bad = l.state === 'not-heard' || l.state === 'trimmed' || !!l.message;
          return (
            <li key={l.beatId} className="flex items-start gap-2.5 rounded-[12px] bg-white/[0.04] px-3 py-2 text-[12.5px]" data-placed-line={l.beatId} data-state={l.state}>
              <span className="tc mt-[1px] grid h-5 min-w-5 place-items-center rounded-full bg-white/[0.08] px-1 text-[11px] font-semibold text-[#d9d1c7]">{l.line}</span>
              <span className="min-w-0 flex-1">
                <span className="line-clamp-1 text-white">{l.text}</span>
                <span className={cx('mt-0.5 block', bad ? 'text-[#e8b169]' : 'text-[#a69d93]')}>
                  {l.state === 'placed' ? (
                    <>
                      <span className="tc">{clock(l.at)}</span> · starts {l.placement}
                      {l.moved != null && Math.abs(l.moved) >= 0.1 && !snapped ? ` · ${Math.abs(l.moved).toFixed(1)} s ${l.moved > 0 ? 'later' : 'earlier'} than planned` : ''}
                      {l.message ? ` · ${l.message}` : ''}
                    </>
                  ) : l.state === 'kept' ? (
                    'Not part of this take: unchanged'
                  ) : (
                    l.message
                  )}
                </span>
              </span>
            </li>
          );
        })}
      </ol>

      {flags.length ? (
        <div className="space-y-2" aria-label="Lines to look at">
          {flags.map((f) => (
            <div key={f.beatId} className="rounded-[12px] bg-[#ef8d80]/10 px-3 py-2.5" data-flag={f.beatId}>
              <p className="flex items-start gap-1.5 text-[12.5px] leading-relaxed text-[#f2ece4]">
                <WarningCircle size={14} weight="fill" className="mt-[2px] shrink-0 text-[#ef8d80]" aria-hidden />
                <span>
                  Line {f.line}: {(f.short || f.message).replace(/^This line /, '')}.
                </span>
              </p>
              {f.fixes?.filter((x) => x.strategy !== 'shorten').length ? (
                <div className="mt-2 flex flex-wrap gap-1.5">
                  {f.fixes
                    .filter((x) => x.strategy !== 'shorten')
                    .map((x) => (
                      <Button
                        key={x.strategy}
                        size="sm"
                        variant="secondary"
                        className="min-h-7 max-w-full !h-auto py-1 text-left leading-snug !whitespace-normal"
                        loading={busy === `fix:${f.beatId}:${x.strategy}`}
                        onClick={() => onFix(f.beatId, x.strategy, x.summary)}
                      >
                        {x.summary}
                      </Button>
                    ))}
                </div>
              ) : null}
            </div>
          ))}
        </div>
      ) : null}

      <div className="mt-auto flex flex-wrap items-center gap-2 pt-1">
        {!kept ? (
          <>
            <Button variant="primary" size="lg" icon={<CheckCircle size={17} />} loading={busy === 'keep'} disabled={!out.result || !!busy} onClick={onKeep} data-autofocus>
              Keep it
            </Button>
            <Button variant="secondary" size="lg" icon={playing ? <Stop size={15} weight="fill" /> : <Play size={15} weight="fill" />} disabled={!!busy && busy !== 'play'} onClick={onPlay}>
              {playing ? 'Stop' : 'Play it back'}
            </Button>
            <button type="button" onClick={onAgain} disabled={!!busy} className="inline-flex h-11 items-center gap-2 rounded-full px-4 text-[13.5px] font-medium text-[#d9d1c7] hover:bg-white/[0.06] disabled:opacity-40">
              <Record size={15} weight="fill" className="text-[#ef8d80]" /> Record again
            </button>
          </>
        ) : (
          <>
            {snapped ? (
              <Button variant="secondary" size="sm" icon={<ArrowCounterClockwise size={14} />} loading={busy === 'back'} disabled={!!busy} onClick={() => onSnap('recorded')}>
                Back to where I said them
              </Button>
            ) : (
              <Button
                variant="secondary"
                size="sm"
                icon={<MagnetStraight size={14} />}
                loading={busy === 'snap'}
                disabled={!!busy || !canSnap}
                title={canSnap ? undefined : 'Every line is already where it was planned (or the script had no planned spots yet)'}
                onClick={() => onSnap('planned')}
              >
                Snap lines to their planned spots
              </Button>
            )}
            <Button variant="secondary" size="sm" icon={playing ? <Stop size={13} weight="fill" /> : <Play size={13} weight="fill" />} onClick={onPlay}>
              {playing ? 'Stop' : 'Play it back'}
            </Button>
            <button type="button" onClick={onAgain} disabled={!!busy} className="inline-flex h-8 items-center gap-1.5 rounded-full px-3 text-[12.5px] font-medium text-[#d9d1c7] hover:bg-white/[0.06] disabled:opacity-40">
              <Record size={13} weight="fill" className="text-[#ef8d80]" /> Record again
            </button>
            <Button variant="primary" size="sm" className="ml-auto" onClick={onDone}>
              Done
            </Button>
          </>
        )}
      </div>
    </div>
  );
}

/** The narration lane for this take: each line where it landed, its planned spot outlined. */
function PlacedLane({ out, plan, duration }: { out: PlaceOut; plan?: RenderPlan; duration: number }) {
  const total = Math.max(0.1, duration);
  const pct = (t: number) => `${Math.max(0, Math.min(100, (t / total) * 100))}%`;
  const shots = plan ? plan.clips.map((c) => ({ id: c.id, from: c.from / plan.fps, to: (c.from + c.durationInFrames) / plan.fps })) : [];
  return (
    <div aria-label="The narration lane" data-placed-lane>
      <div className="relative h-14 rounded-[10px] bg-black/40">
        {shots.map((s, i) => (
          <div key={s.id} className={cx('absolute top-1 h-4 rounded-[3px]', i % 2 ? 'bg-white/[0.10]' : 'bg-white/[0.16]')} style={{ left: pct(s.from), width: `calc(${pct(s.to)} - ${pct(s.from)} - 1px)` }} aria-hidden />
        ))}
        <div className="absolute bottom-1 top-6 rounded-[4px] bg-[#ef8d80]/10" style={{ left: pct(out.from), width: `calc(${pct(out.to)} - ${pct(out.from)})` }} aria-hidden />
        {out.lines.map((l) =>
          l.state === 'placed' && l.plannedAt != null && l.moved != null && Math.abs(l.moved) >= 0.1 ? (
            <div key={`p-${l.beatId}`} className="absolute bottom-1.5 top-[26px] rounded-[5px] border border-dashed border-white/40" style={{ left: pct(l.plannedAt), width: `max(10px, calc(${pct(l.plannedAt + ((l.out ?? 0) - (l.in ?? 0)))} - ${pct(l.plannedAt)}))` }} title={`Line ${l.line} was planned to start ${l.planned}`} aria-hidden />
          ) : null,
        )}
        {out.lines
          .filter((l) => l.state === 'placed')
          .map((l) => (
            <div
              key={l.beatId}
              className={cx('absolute bottom-1.5 top-[26px] flex items-center overflow-hidden rounded-[5px] px-1 text-[10.5px] font-semibold', l.message ? 'bg-[#e8b169] text-[#1c1307]' : 'bg-[#e0a458] text-[#1c1307]')}
              style={{ left: pct(l.at), width: `max(12px, calc(${pct(l.at + ((l.out ?? 0) - (l.in ?? 0)))} - ${pct(l.at)} - 1px))` }}
              title={`Line ${l.line}: ${l.text}`}
              data-lane-line={l.beatId}
              data-at={l.at}
            >
              {l.line}
            </div>
          ))}
      </div>
      <div className="mt-1 flex justify-between text-[11px] text-[#8f877e]">
        <span>Where each line landed{out.lines.some((l) => l.moved != null && Math.abs(l.moved) >= 0.1) ? ' (outlined: where it was planned)' : ''}</span>
        <span className="tc">{clock(total)}</span>
      </div>
    </div>
  );
}
