import { useEffect, useRef } from 'react';
import { BracketsSquare, Plus, X } from '@phosphor-icons/react';
import type { AssetSummary } from '../../api/types';
import { duration as fmtDuration, timecode } from '../../lib/format';
import { isDialogOpen, isTypingTarget } from '../../lib/hooks';
import { clearMarks, markedRange, setKeySurface, setMark, sourceKeysActive, useMarks } from '../../lib/marks';
import { Button, cx, Kbd } from '../../ui/primitives';

/**
 * In and out marks on a piece of footage: I and O at the playhead, or drag the
 * handles on the bar. What is marked is what goes into the cut (the whole
 * footage without marks). Optional buttons add it to the cut.
 */
export function MarkBar({
  asset,
  time,
  onSeek,
  onAdd,
}: {
  asset: AssetSummary;
  time: number;
  onSeek: (t: number) => void;
  onAdd?: { label: string; run: (range: { in: number; out: number }) => void }[];
}) {
  const marks = useMarks(asset.id);
  const dur = asset.duration || 0;
  const bar = useRef<HTMLDivElement>(null);
  const r = markedRange(asset);
  const timeRef = useRef(time);
  timeRef.current = time;

  // I / O at the playhead (when this viewer was used last, or no timeline is open).
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || isTypingTarget(e.target) || isDialogOpen() || !sourceKeysActive()) return;
      if (e.key === 'i' || e.key === 'I') {
        e.preventDefault();
        setMark(asset.id, 'in', timeRef.current);
      } else if (e.key === 'o' || e.key === 'O') {
        e.preventDefault();
        setMark(asset.id, 'out', timeRef.current);
      }
    };
    window.addEventListener('keydown', on);
    return () => window.removeEventListener('keydown', on);
  }, [asset.id]);

  const pct = (t: number) => `${(Math.max(0, Math.min(dur, t)) / Math.max(dur, 0.001)) * 100}%`;
  const drag = (edge: 'in' | 'out') => (e: React.PointerEvent) => {
    e.preventDefault();
    e.stopPropagation();
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    const move = (ev: PointerEvent) => {
      const b = bar.current?.getBoundingClientRect();
      if (!b) return;
      const t = Math.max(0, Math.min(dur, ((ev.clientX - b.left) / b.width) * dur));
      setMark(asset.id, edge, t);
      onSeek(t);
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };
  const keyNudge = (edge: 'in' | 'out') => (e: React.KeyboardEvent) => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
    e.preventDefault();
    const cur = edge === 'in' ? r.in : r.out;
    const step = (e.shiftKey ? 1 : 1 / 30) * (e.key === 'ArrowLeft' ? -1 : 1);
    setMark(asset.id, edge, Math.max(0, Math.min(dur, cur + step)));
  };

  return (
    <div className="mt-3 rounded-[12px] p-3 hairline" onPointerDown={() => setKeySurface('source')} onFocusCapture={() => setKeySurface('source')} data-no-timeline-keys>
      <div className="flex flex-wrap items-center gap-2 text-[12.5px]">
        <BracketsSquare size={15} className="text-subtle" />
        <span className="font-medium text-fg">{r.marked ? 'Marked part' : 'All of it'}</span>
        <span className="tc text-muted">
          {timecode(r.in)} to {timecode(r.out)} · {fmtDuration(r.out - r.in)}
        </span>
        <div className="ml-auto flex items-center gap-1">
          <Button size="sm" variant="ghost" onClick={() => setMark(asset.id, 'in', time)} title="Mark the start at the playhead (I)">
            Start here <Kbd>I</Kbd>
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setMark(asset.id, 'out', time)} title="Mark the end at the playhead (O)">
            End here <Kbd>O</Kbd>
          </Button>
          {r.marked ? (
            <Button size="sm" variant="ghost" icon={<X size={12} />} onClick={() => clearMarks(asset.id)}>
              Clear
            </Button>
          ) : null}
        </div>
      </div>
      <div ref={bar} className="relative mt-2.5 h-7 rounded-[8px] bg-inset" aria-label="The marked part of the footage">
        <div className="absolute inset-y-0 rounded-[8px] bg-accent/25 ring-1 ring-inset ring-accent/60" style={{ left: pct(r.in), width: `calc(${pct(r.out)} - ${pct(r.in)})` }} />
        <div className="pointer-events-none absolute inset-y-0 w-[2px] -translate-x-1/2 bg-white/80" style={{ left: pct(time) }} />
        {(['in', 'out'] as const).map((edge) => (
          <div
            key={edge}
            role="slider"
            tabIndex={0}
            aria-label={edge === 'in' ? 'Where the clip starts' : 'Where the clip ends'}
            aria-valuemin={0}
            aria-valuemax={dur}
            aria-valuenow={edge === 'in' ? r.in : r.out}
            aria-valuetext={timecode(edge === 'in' ? r.in : r.out)}
            onPointerDown={drag(edge)}
            onKeyDown={keyNudge(edge)}
            className={cx(
              'absolute inset-y-[-6px] z-10 flex w-11 -translate-x-1/2 cursor-ew-resize items-center justify-center outline-none',
              'focus-visible:[&>span]:ring-2 focus-visible:[&>span]:ring-accent',
            )}
            style={{ left: pct(edge === 'in' ? r.in : r.out), touchAction: 'none' }}
          >
            <span className={cx('block h-full w-[6px] rounded-full', marks[edge] !== undefined ? 'bg-accent' : 'bg-white/50')} />
          </div>
        ))}
      </div>
      {onAdd?.length ? (
        <div className="mt-3 flex flex-wrap gap-2">
          {onAdd.map((a, i) => (
            <Button key={a.label} size="sm" variant={i === 0 ? 'primary' : 'secondary'} icon={<Plus size={13} weight="bold" />} onClick={() => a.run({ in: r.in, out: r.out })}>
              {a.label}
            </Button>
          ))}
        </div>
      ) : null}
    </div>
  );
}
