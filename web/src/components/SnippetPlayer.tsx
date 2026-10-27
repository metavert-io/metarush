import { useEffect, useSyncExternalStore } from 'react';
import { Pause, Play, Warning } from '@phosphor-icons/react';
import { media } from '../api/client';
import { cx, Spinner } from '../ui/primitives';

/*
 * One shared <audio> element plays voice snippets, so starting one stops the
 * others. Snippets come from /media/assets/{id}/snippet (a few seconds cut on
 * the daemon from the cached proxy or original).
 */
type State = { key: string | null; status: 'idle' | 'loading' | 'playing' | 'error'; progress: number };
let state: State = { key: null, status: 'idle', progress: 0 };
const subs = new Set<() => void>();
let audio: HTMLAudioElement | null = null;
let raf = 0;

function set(next: Partial<State>) {
  state = { ...state, ...next };
  subs.forEach((fn) => fn());
}

function el(): HTMLAudioElement {
  if (audio) return audio;
  audio = new Audio();
  audio.preload = 'auto';
  audio.addEventListener('playing', () => {
    set({ status: 'playing' });
    const tick = () => {
      if (audio && audio.duration > 0) set({ progress: Math.min(1, audio.currentTime / audio.duration) });
      raf = requestAnimationFrame(tick);
    };
    cancelAnimationFrame(raf);
    raf = requestAnimationFrame(tick);
  });
  audio.addEventListener('waiting', () => set({ status: 'loading' }));
  audio.addEventListener('ended', () => {
    cancelAnimationFrame(raf);
    set({ key: null, status: 'idle', progress: 0 });
  });
  audio.addEventListener('pause', () => {
    cancelAnimationFrame(raf);
    if (state.status === 'playing') set({ key: null, status: 'idle', progress: 0 });
  });
  audio.addEventListener('error', () => {
    cancelAnimationFrame(raf);
    set({ status: 'error' });
  });
  return audio;
}

export function stopSnippet() {
  audio?.pause();
  set({ key: null, status: 'idle', progress: 0 });
}

function toggle(key: string, url: string) {
  const a = el();
  if (state.key === key && (state.status === 'playing' || state.status === 'loading')) {
    stopSnippet();
    return;
  }
  a.pause();
  set({ key, status: 'loading', progress: 0 });
  a.src = url;
  void a.play().catch(() => set({ status: 'error' }));
}

function useSnippetState(): State {
  return useSyncExternalStore(
    (fn) => {
      subs.add(fn);
      return () => subs.delete(fn);
    },
    () => state,
  );
}

/** Play a few seconds of a voice. Space/Enter toggle when focused. */
export function SnippetButton({ assetId, from, to, label, color, size = 'md', className, autoFocus }: {
  assetId: string;
  from: number;
  to: number;
  label: string;
  color?: string;
  size?: 'sm' | 'md';
  className?: string;
  autoFocus?: boolean;
}) {
  const key = `${assetId}:${from.toFixed(2)}:${to.toFixed(2)}`;
  const s = useSnippetState();
  const mine = s.key === key;
  const playing = mine && s.status === 'playing';
  const loading = mine && s.status === 'loading';
  const failed = mine && s.status === 'error';
  useEffect(() => () => {
    if (state.key === key) stopSnippet();
  }, [key]);
  const dim = size === 'sm' ? 28 : 34;
  return (
    <button
      type="button"
      autoFocus={autoFocus}
      onClick={() => toggle(key, media.snippet(assetId, from, to))}
      aria-label={playing ? `Stop ${label}` : `Listen: ${label}`}
      aria-pressed={playing}
      title={failed ? 'This clip could not be played' : `${label} (${Math.round(to - from)} s)`}
      className={cx(
        'relative inline-flex shrink-0 items-center justify-center rounded-full bg-panel text-fg hairline transition-[transform,background-color] duration-200 hover:bg-hover active:scale-[0.96]',
        className,
      )}
      style={{ width: dim, height: dim }}
    >
      <svg className="pointer-events-none absolute inset-0 -rotate-90" viewBox="0 0 36 36" aria-hidden>
        <circle cx="18" cy="18" r="16.5" fill="none" stroke={color ?? 'var(--accent)'} strokeOpacity={0.22} strokeWidth="2" />
        {mine ? (
          <circle
            cx="18"
            cy="18"
            r="16.5"
            fill="none"
            stroke={color ?? 'var(--accent)'}
            strokeWidth="2"
            strokeLinecap="round"
            strokeDasharray={`${(s.progress * 103.7).toFixed(1)} 103.7`}
          />
        ) : null}
      </svg>
      {loading ? <Spinner size={12} /> : failed ? <Warning size={14} className="text-danger" /> : playing ? <Pause size={13} weight="fill" /> : <Play size={13} weight="fill" className="translate-x-[1px]" />}
    </button>
  );
}
