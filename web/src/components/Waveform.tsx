import { useEffect, useMemo, useRef, useState } from 'react';
import { usePeaks } from '../api/queries';
import type { TechnicalEvidence } from '../api/types';
import { timecode } from '../lib/format';
import { useTheme } from '../lib/theme';
import { cx, Skeleton } from '../ui/primitives';

export interface WaveLayers {
  pauses: boolean;
  scenes: boolean;
  black: boolean;
  freezes: boolean;
}

/**
 * Peaks waveform with detector overlays (pauses, scene changes, black and frozen
 * frames). Click or arrow keys to seek; it is a slider for assistive tech.
 */
export function Waveform({ assetId, duration, time, onSeek, evidence, layers, height = 72, range, enabled = true }: {
  assetId: string;
  duration: number;
  time: number;
  onSeek: (t: number) => void;
  evidence?: TechnicalEvidence;
  layers: WaveLayers;
  height?: number;
  range?: { from: number; to: number };
  enabled?: boolean;
}) {
  const peaks = usePeaks(assetId, enabled);
  const wrap = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const [width, setWidth] = useState(0);
  const [hover, setHover] = useState<number | null>(null);
  const [theme] = useTheme();
  const total = peaks.data?.duration || duration || 1;

  useEffect(() => {
    const el = wrap.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setWidth(Math.floor(e?.contentRect.width ?? 0)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Downsample to one value per 3px column (max of the bucket).
  const columns = useMemo(() => {
    const p = peaks.data?.peaks;
    if (!p || p.length === 0 || width <= 0) return null;
    const n = Math.max(1, Math.floor(width / 3));
    const out = new Float32Array(n);
    for (let i = 0; i < n; i++) {
      const a = Math.floor((i / n) * p.length);
      const b = Math.max(a + 1, Math.floor(((i + 1) / n) * p.length));
      let m = 0;
      for (let j = a; j < b && j < p.length; j++) m = Math.max(m, p[j] ?? 0);
      out[i] = m;
    }
    return out;
  }, [peaks.data, width]);

  useEffect(() => {
    const c = canvas.current;
    if (!c || !columns || width <= 0) return;
    const dpr = window.devicePixelRatio || 1;
    c.width = width * dpr;
    c.height = height * dpr;
    const ctx = c.getContext('2d');
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, width, height);
    const css = getComputedStyle(document.documentElement);
    const base = css.getPropertyValue('--wave').trim() || 'rgba(255,255,255,0.4)';
    const played = css.getPropertyValue('--wave-played').trim() || '#e0a458';
    const mid = height / 2;
    const playedX = (time / total) * width;
    const n = columns.length;
    const colW = width / n;
    for (let i = 0; i < n; i++) {
      const v = Math.max(0.025, Math.pow(columns[i] ?? 0, 0.8));
      const h = v * (height - 6);
      const x = i * colW;
      ctx.fillStyle = x + colW / 2 <= playedX ? played : base;
      const w = Math.max(1, colW - 1.2);
      const r = Math.min(w / 2, 1.2);
      roundRect(ctx, x, mid - h / 2, w, h, r);
    }
  }, [columns, width, height, time, total, theme]);

  const toTime = (clientX: number) => {
    const el = wrap.current;
    if (!el) return 0;
    const r = el.getBoundingClientRect();
    return Math.max(0, Math.min(total, ((clientX - r.left) / r.width) * total));
  };
  const pct = (t: number) => `${(Math.max(0, Math.min(total, t)) / total) * 100}%`;

  const ev = evidence;
  return (
    <div className="select-none">
      <div
        ref={wrap}
        role="slider"
        tabIndex={0}
        aria-label="Jump to a moment in the clip (waveform)"
        aria-valuemin={0}
        aria-valuemax={Math.round(total * 1000) / 1000}
        aria-valuenow={Math.round(time * 1000) / 1000}
        aria-valuetext={timecode(time)}
        data-player-surface
        onKeyDown={(e) => {
          const step = e.shiftKey ? 5 : 1;
          if (e.key === 'ArrowRight') {
            e.preventDefault();
            onSeek(Math.min(total, time + step));
          } else if (e.key === 'ArrowLeft') {
            e.preventDefault();
            onSeek(Math.max(0, time - step));
          } else if (e.key === 'Home') {
            e.preventDefault();
            onSeek(0);
          } else if (e.key === 'End') {
            e.preventDefault();
            onSeek(total);
          }
        }}
        onPointerDown={(e) => {
          (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
          onSeek(toTime(e.clientX));
        }}
        onPointerMove={(e) => {
          setHover(toTime(e.clientX));
          if (e.buttons === 1) onSeek(toTime(e.clientX));
        }}
        onPointerLeave={() => setHover(null)}
        className="relative cursor-pointer overflow-hidden rounded-[10px] bg-inset"
        style={{ height }}
      >
        {range ? <div className="absolute inset-y-0 bg-accent-soft" style={{ left: pct(range.from), width: `calc(${pct(range.to)} - ${pct(range.from)})` }} /> : null}
        {layers.pauses &&
          ev?.silences?.map((s, i) => (
            <div
              key={`s${i}`}
              title={`Pause ${(s.to - s.from).toFixed(2)}s at ${timecode(s.from)}`}
              className="absolute inset-y-0 bg-[repeating-linear-gradient(135deg,rgb(169_185_204/0.16)_0_4px,transparent_4px_8px)]"
              style={{ left: pct(s.from), width: `max(2px, calc(${pct(s.to)} - ${pct(s.from)}))` }}
            />
          ))}
        {layers.black &&
          ev?.black?.map((s, i) => (
            <div key={`b${i}`} title={`Black frames ${timecode(s.from)} to ${timecode(s.to)}`} className="absolute inset-y-0 bg-black/70 ring-1 ring-inset ring-white/10" style={{ left: pct(s.from), width: `max(2px, calc(${pct(s.to)} - ${pct(s.from)}))` }} />
          ))}
        {layers.freezes &&
          ev?.freezes?.map((s, i) => (
            <div key={`f${i}`} title={`Frozen picture ${timecode(s.from)} to ${timecode(s.to)}`} className="absolute bottom-0 h-1.5 bg-danger/70" style={{ left: pct(s.from), width: `max(2px, calc(${pct(s.to)} - ${pct(s.from)}))` }} />
          ))}
        {peaks.isLoading ? <Skeleton className="absolute inset-2" /> : null}
        {peaks.isError ? <div className="absolute inset-0 grid place-items-center text-[12px] text-[#8f877e]">No waveform for this clip yet</div> : null}
        <canvas ref={canvas} className="pointer-events-none absolute inset-0 h-full w-full" style={{ width, height }} aria-hidden />
        {layers.scenes &&
          ev?.scenes?.map((s, i) => (
            <div key={`c${i}`} title={`Scene change at ${timecode(s.at)}`} className="absolute inset-y-0 w-[2px] -translate-x-1/2 bg-accent/90" style={{ left: pct(s.at) }}>
              <div className="absolute -left-[3px] top-0 h-2 w-2 rotate-45 bg-accent" />
            </div>
          ))}
        <div className="pointer-events-none absolute inset-y-0 w-[2px] -translate-x-1/2 bg-fg/90 shadow-[0_0_0_1px_rgb(0_0_0/0.3)]" style={{ left: pct(time) }} />
        {hover !== null ? (
          <>
            <div className="pointer-events-none absolute inset-y-0 w-px bg-white/30" style={{ left: pct(hover) }} />
            <div
              className="tc pointer-events-none absolute top-1 -translate-x-1/2 rounded-[5px] bg-black/75 px-1.5 py-0.5 text-[10.5px] text-white"
              style={{ left: `clamp(32px, ${pct(hover)}, calc(100% - 32px))` }}
            >
              {timecode(hover)}
            </div>
          </>
        ) : null}
      </div>
    </div>
  );
}

function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  ctx.beginPath();
  if (typeof ctx.roundRect === 'function') ctx.roundRect(x, y, w, h, r);
  else ctx.rect(x, y, w, h);
  ctx.fill();
}

export function WaveLegend({ layers, onChange, evidence, onJump }: {
  layers: WaveLayers;
  onChange: (l: WaveLayers) => void;
  evidence?: TechnicalEvidence;
  onJump?: (t: number) => void;
}) {
  const items: { key: keyof WaveLayers; label: string; count: number; swatch: string; first?: number }[] = [
    { key: 'pauses', label: 'Pauses', count: evidence?.silences?.length ?? 0, swatch: 'bg-[repeating-linear-gradient(135deg,var(--info)_0_2px,transparent_2px_4px)]', first: evidence?.silences?.[0]?.from },
    { key: 'scenes', label: 'Scene changes', count: evidence?.scenes?.length ?? 0, swatch: 'bg-accent', first: evidence?.scenes?.[0]?.at },
    { key: 'black', label: 'Black frames', count: evidence?.black?.length ?? 0, swatch: 'bg-black ring-1 ring-white/30', first: evidence?.black?.[0]?.from },
    { key: 'freezes', label: 'Frozen', count: evidence?.freezes?.length ?? 0, swatch: 'bg-danger', first: evidence?.freezes?.[0]?.from },
  ];
  return (
    <div className="mt-2.5 flex flex-wrap items-center gap-1.5" role="group" aria-label="What to show on the waveform">
      {items.map((it) => (
        <button
          key={it.key}
          type="button"
          aria-pressed={layers[it.key]}
          onClick={() => onChange({ ...layers, [it.key]: !layers[it.key] })}
          onDoubleClick={() => it.first !== undefined && onJump?.(it.first)}
          className={cx(
            'inline-flex h-7 items-center gap-2 rounded-full px-2.5 text-[12px] transition-colors hairline',
            layers[it.key] ? 'bg-panel text-fg' : 'text-subtle hover:text-muted',
            it.count === 0 && 'opacity-60',
          )}
        >
          <span className={cx('h-2.5 w-2.5 rounded-[3px]', it.swatch, !layers[it.key] && 'opacity-40')} />
          {it.label}
          <span className="tc text-[11px] text-subtle">{it.count}</span>
        </button>
      ))}
    </div>
  );
}
