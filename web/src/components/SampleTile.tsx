import { useEffect, useRef, useState, useSyncExternalStore } from 'react';
import { Pause, Play, SpeakerSimpleHigh, Warning } from '@phosphor-icons/react';
import { media } from '../api/client';
import { timecode } from '../lib/format';
import { cx, Spinner } from '../ui/primitives';
import { SnippetButton, stopSnippet } from './SnippetPlayer';

/*
 * A voice sample as a small video tile: a frame just after the sample starts
 * (up to 1.5 s in: a call's speaker view switches to whoever talks a moment
 * after they start; cut on the daemon, cached), and a click plays exactly that stretch of the
 * video with sound, inline, from the same-origin preview copy. One tile plays
 * at a time: starting one stops the others (and any audio-only snippet).
 */

type Playing = { key: string; stop: () => void } | null;
let playing: Playing = null;
const subs = new Set<() => void>();
const emit = () => subs.forEach((fn) => fn());

export function stopSampleTiles() {
  playing?.stop();
}

function usePlayingKey(): string | null {
  return useSyncExternalStore(
    (fn) => {
      subs.add(fn);
      return () => subs.delete(fn);
    },
    () => playing?.key ?? null,
  );
}

const SIZES = { sm: [112, 63], md: [160, 90], lg: [232, 130] } as const;

export function SampleTile({ assetId, from, to, title, hasVideo = true, label, size = 'md', color, className }: {
  assetId: string;
  from: number;
  to: number;
  /** The clip, shown under the tile with the time ("IMG_2392 · 0:12"). */
  title?: string;
  hasVideo?: boolean;
  /** What the tile plays, for screen readers ("Maya in IMG_2392"). */
  label: string;
  size?: keyof typeof SIZES;
  /** A voice's color: the tile's edge while it plays. */
  color?: string;
  className?: string;
}) {
  const [w, h] = SIZES[size];
  const key = `${assetId}:${from.toFixed(2)}:${to.toFixed(2)}`;
  const current = usePlayingKey();
  const mine = current === key;
  const video = useRef<HTMLVideoElement>(null);
  const [state, setState] = useState<'idle' | 'loading' | 'playing' | 'error'>('idle');
  const [progress, setProgress] = useState(0);
  const [poster, setPoster] = useState<'loading' | 'ok' | 'error'>('loading');

  const stop = () => {
    const v = video.current;
    if (v) v.pause();
    setState('idle');
    setProgress(0);
    if (playing?.key === key) {
      playing = null;
      emit();
    }
  };
  const stopRef = useRef(stop);
  stopRef.current = stop;
  useEffect(() => () => {
    if (playing?.key === key) stopRef.current();
  }, [key]);
  // Another tile started: this one stops.
  useEffect(() => {
    if (!mine && state !== 'idle') {
      video.current?.pause();
      setState('idle');
      setProgress(0);
    }
  }, [mine, state]);

  if (!hasVideo)
    return (
      <figure className={cx('m-0 shrink-0', className)} style={{ width: w }}>
        <div className="grid place-items-center rounded-[10px] bg-panel hairline" style={{ height: h }}>
          <div className="flex flex-col items-center gap-1.5 text-subtle">
            <SnippetButton assetId={assetId} from={from} to={to} label={label} color={color} size="sm" />
            <SpeakerSimpleHigh size={12} aria-hidden />
          </div>
        </div>
        <Caption title={title} from={from} />
      </figure>
    );

  const start = () => {
    if (mine && (state === 'playing' || state === 'loading')) {
      stop();
      return;
    }
    playing?.stop();
    stopSnippet();
    playing = { key, stop: () => stopRef.current() };
    emit();
    setState('loading');
    setProgress(0);
    const v = video.current;
    if (!v) return;
    const go = () => {
      v.currentTime = from;
      void v.play().then(
        () => setState('playing'),
        () => setState('error'),
      );
    };
    if (!v.getAttribute('src')) {
      // Only a tile someone plays loads the video (a page can show dozens).
      v.src = media.asset(assetId, 'proxy');
      v.addEventListener('loadedmetadata', go, { once: true });
      v.load();
    } else if (v.readyState >= 1) go();
    else v.addEventListener('loadedmetadata', go, { once: true });
  };

  return (
    <figure className={cx('group/tile m-0 shrink-0', className)} style={{ width: w }}>
      <button
        type="button"
        onClick={start}
        aria-pressed={mine && state === 'playing'}
        aria-label={mine && state !== 'idle' ? `Stop: ${label}` : `Play: ${label}`}
        title={state === 'error' ? 'This part of the video could not be played' : `${label} (${Math.max(1, Math.round(to - from))} s)`}
        data-sample-tile
        data-from={from}
        data-to={to}
        className={cx(
          'relative block overflow-hidden rounded-[10px] bg-inset hairline outline-none transition-[transform,box-shadow] duration-200 ease-[var(--ease-out-expo)]',
          'hover:scale-[1.03] hover:shadow-[var(--shadow-soft)] focus-visible:outline-2 focus-visible:outline-accent active:scale-[0.99] motion-reduce:hover:scale-100',
        )}
        style={{ width: w, height: h, boxShadow: mine && state === 'playing' && color ? `inset 0 0 0 2px ${color}` : undefined }}
      >
        {poster === 'loading' ? <span className="skeleton absolute inset-0" aria-hidden /> : null}
        {poster !== 'error' ? (
          <img
            src={media.frame(assetId, from + Math.min(1.5, Math.max(0, to - from) / 2), w * 2)}
            alt=""
            loading="lazy"
            decoding="async"
            draggable={false}
            onLoad={() => setPoster('ok')}
            onError={() => setPoster('error')}
            className={cx('absolute inset-0 h-full w-full object-cover transition-opacity duration-300', poster === 'ok' ? 'opacity-100' : 'opacity-0')}
          />
        ) : (
          <span className="absolute inset-0 grid place-items-center text-[#7d756c]" aria-hidden>
            <Play size={18} />
          </span>
        )}
        <video
          ref={video}
          preload="none"
          playsInline
          data-sample-video
          aria-hidden
          tabIndex={-1}
          onTimeUpdate={(e) => {
            const v = e.currentTarget;
            const p = (v.currentTime - from) / Math.max(0.1, to - from);
            setProgress(Math.max(0, Math.min(1, p)));
            if (v.currentTime >= to) {
              v.pause();
              setState('idle');
              setProgress(0);
              if (playing?.key === key) {
                playing = null;
                emit();
              }
            }
          }}
          onWaiting={() => setState((s) => (s === 'playing' ? 'loading' : s))}
          onPlaying={() => setState('playing')}
          onError={() => mine && setState('error')}
          className={cx('absolute inset-0 h-full w-full object-cover transition-opacity duration-200', mine && state !== 'idle' ? 'opacity-100' : 'opacity-0')}
        />
        <span
          className={cx(
            'absolute bottom-1.5 left-1.5 grid h-6 w-6 place-items-center rounded-full bg-black/55 text-white backdrop-blur-[2px] transition-opacity',
            mine && state === 'playing' ? 'opacity-0 group-hover/tile:opacity-100' : 'opacity-100',
          )}
          aria-hidden
        >
          {state === 'loading' && mine ? <Spinner size={10} /> : state === 'error' && mine ? <Warning size={12} /> : mine && state === 'playing' ? <Pause size={11} weight="fill" /> : <Play size={11} weight="fill" className="translate-x-[0.5px]" />}
        </span>
        {mine && state !== 'idle' ? (
          <span className="absolute inset-x-0 bottom-0 h-[3px] bg-black/30" aria-hidden>
            <span className="block h-full bg-white/90" style={{ width: `${progress * 100}%`, background: color }} />
          </span>
        ) : null}
      </button>
      <Caption title={title} from={from} />
    </figure>
  );
}

function Caption({ title, from }: { title?: string; from: number }) {
  return (
    <figcaption className="mt-1 flex min-w-0 items-baseline gap-1 text-[11.5px] leading-tight text-subtle">
      {title ? <span className="min-w-0 truncate text-muted">{title}</span> : null}
      {title ? <span aria-hidden>·</span> : null}
      <span className="tc shrink-0">{timecode(from, { ms: false })}</span>
    </figcaption>
  );
}
