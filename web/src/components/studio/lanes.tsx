import { memo, useEffect, useMemo, useRef, useSyncExternalStore } from 'react';
import { media } from '../../api/client';
import { usePeaks } from '../../api/queries';
import type { NestedPlan } from '@metarush/schema';
import type { AssetSummary, Clip } from '../../api/types';
import { useTheme } from '../../lib/theme';
import { speedOf } from '../../lib/editops';

/*
 * Timeline lanes: filmstrip thumbnails and waveforms for each clip.
 *
 * Filmstrip frames are decoded in the browser from the clip's same-origin
 * proxy (one hidden video, one asset at a time, a frame every few seconds of
 * footage), kept for the session and shared by every clip of that footage.
 * Until they arrive, the footage's thumbnail stands in.
 */

interface Strip {
  step: number; // seconds between frames
  frames: (string | undefined)[];
  done: boolean;
}

const strips = new Map<string, Strip>();
const subs = new Set<() => void>();
const queue: { asset: AssetSummary }[] = [];
let running = false;
let version = 0;
const notify = () => {
  version++;
  subs.forEach((fn) => fn());
};

function stepFor(duration: number) {
  if (duration <= 20) return 1;
  if (duration <= 90) return 2;
  return Math.max(2, duration / 60);
}

async function decode(asset: AssetSummary) {
  const strip = strips.get(asset.id)!;
  const v = document.createElement('video');
  v.muted = true;
  v.preload = 'auto';
  v.playsInline = true;
  v.crossOrigin = 'use-credentials';
  v.src = media.asset(asset.id, 'proxy');
  await new Promise<void>((resolve, reject) => {
    v.addEventListener('loadeddata', () => resolve(), { once: true });
    v.addEventListener('error', () => reject(new Error('The preview copy could not be loaded.')), { once: true });
  });
  const h = 54;
  const w = Math.max(24, Math.round((h * (asset.width || 16)) / (asset.height || 9)));
  const canvas = document.createElement('canvas');
  canvas.width = w * 2;
  canvas.height = h * 2;
  const ctx = canvas.getContext('2d');
  if (!ctx) throw new Error('canvas');
  for (let i = 0; i < strip.frames.length; i++) {
    const t = Math.min((asset.duration || v.duration || 0) - 0.05, i * strip.step + strip.step / 2);
    await new Promise<void>((resolve) => {
      const done = () => resolve();
      v.addEventListener('seeked', done, { once: true });
      v.currentTime = Math.max(0, t);
      setTimeout(done, 1500);
    });
    ctx.drawImage(v, 0, 0, canvas.width, canvas.height);
    strip.frames[i] = canvas.toDataURL('image/jpeg', 0.62);
    if (i % 4 === 3) notify();
  }
  v.removeAttribute('src');
  v.load();
}

async function pump() {
  if (running) return;
  running = true;
  while (queue.length) {
    const { asset } = queue.shift()!;
    try {
      await decode(asset);
    } catch {
      // no preview copy yet (or it can't be decoded here): the thumbnail stays
    }
    const s = strips.get(asset.id);
    if (s) s.done = true;
    notify();
  }
  running = false;
}

function requestStrip(asset: AssetSummary) {
  if (strips.has(asset.id) || asset.kind !== 'video' || !asset.variants.includes('proxy') || !asset.duration) return;
  const step = stepFor(asset.duration);
  strips.set(asset.id, { step, frames: new Array(Math.max(1, Math.ceil(asset.duration / step))).fill(undefined), done: false });
  queue.push({ asset });
  void pump();
}

function useStrips(): number {
  return useSyncExternalStore(
    (fn) => {
      subs.add(fn);
      return () => subs.delete(fn);
    },
    () => version,
  );
}

/** A clip's picture as a row of frames from its footage. */
export const Filmstrip = memo(function Filmstrip({ clip, asset, width, height, pps }: { clip: Clip; asset?: AssetSummary; width: number; height: number; pps: number }) {
  useStrips();
  useEffect(() => {
    if (asset) requestStrip(asset);
  }, [asset]);
  if (!asset) return null;
  const strip = strips.get(asset.id);
  const aspect = (asset.width || 16) / (asset.height || 9);
  const tileW = Math.max(28, Math.round(height * aspect));
  const tiles: { x: number; src?: string }[] = [];
  const thumb = asset.variants.includes('thumbnail') ? media.asset(asset.id, 'thumbnail') : undefined;
  for (let x = 0; x < width && tiles.length < 200; x += tileW) {
    const t = clip.in + ((x + tileW / 2) / pps) * speedOf(clip);
    const f = strip ? strip.frames[Math.min(strip.frames.length - 1, Math.max(0, Math.floor(t / strip.step)))] : undefined;
    tiles.push({ x, src: f ?? thumb });
  }
  return (
    <div className="pointer-events-none absolute inset-0 overflow-hidden" aria-hidden>
      {tiles.map((tile, i) =>
        tile.src ? (
          <img key={i} src={tile.src} alt="" draggable={false} className="absolute top-0 h-full object-cover opacity-80" style={{ left: tile.x, width: tileW }} />
        ) : (
          <div key={i} className="absolute top-0 h-full bg-white/[0.03]" style={{ left: tile.x, width: tileW - 1 }} />
        ),
      )}
    </div>
  );
});

/**
 * A cut from another project played inside this one (D84): one block with
 * its shots' thumbnails and where it comes from ("Brand intro · Brand · v4").
 * It opens read-only from the clip panel; changing it means changing that
 * project's cut, then updating this one.
 */
export const NestedStrip = memo(function NestedStrip({ nested, width }: { nested: NestedPlan | null; width: number }) {
  const shots = useMemo(() => {
    const ids: string[] = [];
    for (const c of nested?.plan?.clips ?? []) if (!c.assetId.startsWith('cut:') && !ids.includes(c.assetId)) ids.push(c.assetId);
    return ids.slice(0, Math.max(1, Math.floor(width / 64)));
  }, [nested, width]);
  const tileW = shots.length ? width / shots.length : width;
  return (
    <div className="pointer-events-none absolute inset-0 overflow-hidden bg-[repeating-linear-gradient(90deg,rgb(255_255_255/0.06)_0_2px,transparent_2px_10px)]" aria-hidden>
      {shots.map((id, i) => (
        <img key={id} src={media.asset(id, 'thumbnail')} alt="" draggable={false} className="absolute top-0 h-full object-cover opacity-60" style={{ left: i * tileW, width: tileW - 1 }} onError={(e) => ((e.target as HTMLImageElement).style.display = 'none')} />
      ))}
      <div className="absolute inset-x-0 top-0 h-[3px] bg-[repeating-linear-gradient(90deg,var(--accent)_0_8px,transparent_8px_12px)]" />
      {width > 70 ? (
        <span className="absolute left-1.5 top-1.5 max-w-[calc(100%-12px)] truncate rounded-[6px] bg-black/70 px-1.5 py-0.5 text-[10.5px] font-medium text-white">
          {nested ? `${nested.title || 'Cut'} · ${nested.projectName ?? 'another project'} · v${nested.number}` : 'A cut from another project'}
          {nested?.newerNumber ? ` · v${nested.newerNumber} is newer` : ''}
        </span>
      ) : null}
    </div>
  );
});

/** A clip's sound as a waveform of the part of the footage it plays. */
export const WaveLane = memo(function WaveLane({ clip, asset, width, height, muted }: { clip: Clip; asset?: AssetSummary; width: number; height: number; muted?: boolean }) {
  const peaks = usePeaks(asset?.id, !!asset && asset.hasAudio && asset.variants.includes('peaks'));
  const canvas = useRef<HTMLCanvasElement>(null);
  const [theme] = useTheme();
  const cols = useMemo(() => {
    const p = peaks.data?.peaks;
    const dur = peaks.data?.duration || asset?.duration || 0;
    if (!p?.length || !dur || width <= 0) return null;
    const n = Math.max(1, Math.floor(width / 3));
    const out = new Float32Array(n);
    for (let i = 0; i < n; i++) {
      const t0 = clip.in + (i / n) * (clip.out - clip.in);
      const t1 = clip.in + ((i + 1) / n) * (clip.out - clip.in);
      const a = Math.floor((t0 / dur) * p.length);
      const b = Math.max(a + 1, Math.floor((t1 / dur) * p.length));
      let m = 0;
      for (let j = a; j < b && j < p.length; j++) m = Math.max(m, p[j] ?? 0);
      out[i] = m;
    }
    return out;
  }, [peaks.data, asset?.duration, clip.in, clip.out, width]);
  useEffect(() => {
    const c = canvas.current;
    if (!c || width <= 0) return;
    const dpr = window.devicePixelRatio || 1;
    c.width = Math.max(1, Math.floor(width * dpr));
    c.height = Math.floor(height * dpr);
    const ctx = c.getContext('2d');
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, width, height);
    if (!cols) return;
    const css = getComputedStyle(document.documentElement);
    ctx.fillStyle = muted ? css.getPropertyValue('--wave-muted').trim() || 'rgba(140,130,120,0.35)' : css.getPropertyValue('--wave-lane').trim() || 'rgba(242,236,228,0.55)';
    const mid = height / 2;
    const colW = width / cols.length;
    for (let i = 0; i < cols.length; i++) {
      const v = Math.max(0.04, Math.pow(cols[i] ?? 0, 0.8));
      const h = v * (height - 4);
      ctx.fillRect(i * colW, mid - h / 2, Math.max(1, colW - 1), h);
    }
  }, [cols, width, height, theme, muted]);
  if (!asset?.hasAudio) return null;
  return <canvas ref={canvas} aria-hidden className="pointer-events-none absolute inset-0" style={{ width, height }} />;
});
