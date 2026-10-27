import { useCallback, useEffect, useRef, useState } from 'react';
import { Check, Play, Sparkle, Stop } from '@phosphor-icons/react';
import { call, media } from '../../../api/client';
import { useOp } from '../../../api/queries';
import type { NarrationLine, TakeView } from '../../../api/types';
import { relative } from '../../../lib/format';
import { markJobSeen } from '../../../lib/workbench';
import { Badge, Button, cx, ErrorPanel, Skeleton } from '../../../ui/primitives';
import { applyPlan, checkApplied, useChange } from './api';

/*
 * A line's takes, newest first: listen to each, choose the one that plays,
 * and compare a recording with its cleaned-up version (A/B). Every take is
 * kept; choosing one never deletes another.
 */

/** Plays one stretch of an audio asset at a time (a take is a range of its file). */
export function useRangePlayer() {
  const ref = useRef<HTMLAudioElement | null>(null);
  const [playing, setPlaying] = useState<string | null>(null);
  const stop = useCallback(() => {
    const a = ref.current;
    if (a) {
      a.ontimeupdate = a.onended = a.onerror = null;
      a.pause();
    }
    setPlaying(null);
  }, []);
  useEffect(() => stop, [stop]);
  const play = useCallback(
    (key: string, src: string, from: number, to: number) => {
      if (playing === key) {
        stop();
        return;
      }
      stop();
      const a = ref.current ?? (ref.current = new Audio());
      a.preload = 'auto';
      if (a.src !== new URL(src, window.location.href).href) a.src = src;
      const start = () => {
        a.currentTime = Math.max(0, from);
        void a.play().catch(() => setPlaying(null));
      };
      a.ontimeupdate = () => {
        if (a.currentTime >= to) stop();
      };
      a.onended = stop;
      a.onerror = stop;
      setPlaying(key);
      if (a.readyState >= 1) start();
      else a.onloadedmetadata = () => ((a.onloadedmetadata = null), start());
    },
    [playing, stop],
  );
  return { play, stop, playing };
}

const SOURCE: Record<string, string> = { recorded: 'Your recording', imported: 'Imported recording', tts: 'AI voice', scratch: 'Computer voice' };

/**
 * The panel follows the cut as it changes (choosing a take makes a new
 * version): it reads the project's latest version and the line in it.
 */
export function TakesDialog({ projectId, lineId }: { projectId: string; lineId: string }) {
  const project = useOp('project.show', { id: projectId });
  const revisionId = project.data?.headRevisionId;
  const show = useOp('voiceover.show', revisionId ? { revisionId } : null);
  const line = show.data?.beats.find((b) => b.id === lineId);
  if (project.error || show.error) return <ErrorPanel error={project.error ?? show.error} />;
  if (!revisionId || !show.data) return <Skeleton className="h-40" />;
  if (!line) return <p className="text-[13px] text-muted">This line is no longer in the cut.</p>;
  return <TakesPanel revisionId={revisionId} line={line} />;
}

export function TakesPanel({ revisionId, line }: { revisionId: string; line: NarrationLine }) {
  const takes = useOp('voiceover.takes', { revisionId, beat: line.id });
  const player = useRangePlayer();
  const change = useChange();
  const [busy, setBusy] = useState<string | null>(null);
  const cur = line.take;
  const run = async (key: string, fn: () => Promise<unknown>, done: string, failed?: string) => {
    setBusy(key);
    await change(fn, done, failed);
    setBusy(null);
  };
  const choose = (t: TakeView, n: number) =>
    run(
      t.id,
      async () => applyPlan(await call('voiceover.choose', { revisionId, beat: line.id, takeId: t.id })),
      `Line ${line.line} now plays take ${n}`,
      'That take was not chosen',
    );
  const version = (v: 'original' | 'processed') =>
    run(
      v,
      async () => applyPlan(await call('voiceover.choose', { revisionId, beat: line.id, version: v })),
      v === 'processed' ? `Line ${line.line} plays the cleaned-up version` : `Line ${line.line} plays the original recording`,
    );
  const clean = () =>
    run(
      'clean',
      async () => {
        const out = await call('voiceover.cleanup', { revisionId, beats: [line.id], apply: true });
        markJobSeen(out.jobId);
        return checkApplied(out.applied);
      },
      'Cleaned up: less background noise and an even level. The original is kept.',
      'The recording was not cleaned up',
    );
  const list = takes.data?.takes ?? [];
  const recorded = cur && (cur.source === 'recorded' || cur.source === 'imported');
  const src = (assetId: string) => `${media.asset(assetId, 'original')}?preview=1`;

  return (
    <div className="space-y-5">
      <p className="text-[14px] leading-relaxed text-fg">“{line.text}”</p>

      {cur && recorded ? (
        <section aria-label="Compare the original and the cleaned-up sound" className="rounded-[14px] bg-hover/50 p-3.5">
          <h3 className="text-[13px] font-semibold">The take that plays now</h3>
          {cur.processed ? (
            <>
              <p className="mt-1 text-[12.5px] text-muted">Listen to both, then choose which one the video uses.</p>
              <div className="mt-2.5 grid grid-cols-1 gap-2 sm:grid-cols-2">
                <ABChoice
                  label="Original"
                  active={!cur.useProcessed}
                  playing={player.playing === 'A'}
                  onPlay={() => player.play('A', src(cur.assetId), cur.in, cur.out)}
                  onUse={() => void version('original')}
                  busy={busy === 'original'}
                />
                <ABChoice
                  label="Cleaned up"
                  active={!!cur.useProcessed}
                  playing={player.playing === 'B'}
                  onPlay={() => player.play('B', src(cur.processed!.assetId), cur.in + (cur.processed!.offset ?? 0), cur.out + (cur.processed!.offset ?? 0))}
                  onUse={() => void version('processed')}
                  busy={busy === 'processed'}
                />
              </div>
            </>
          ) : (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <p className="min-w-0 flex-1 text-[12.5px] text-muted">Clean up the sound: less hiss and rumble, softer harsh “s” sounds and an even level. You can switch back any time.</p>
              <Button size="sm" variant="secondary" icon={<Sparkle size={13} />} loading={busy === 'clean'} onClick={() => void clean()}>
                Clean up the sound
              </Button>
            </div>
          )}
        </section>
      ) : null}

      <section aria-label={`Takes for line ${line.line}`}>
        <h3 className="mb-2 text-[13px] font-semibold">Every take</h3>
        {takes.error ? (
          <ErrorPanel compact error={takes.error} onRetry={() => void takes.refetch()} />
        ) : takes.isLoading ? (
          <Skeleton className="h-24" />
        ) : list.length === 0 ? (
          <p className="text-[12.5px] text-subtle">No takes yet. Record the line, use a recording or make an AI voice.</p>
        ) : (
          <ol className="divide-y divide-line overflow-hidden rounded-[12px] hairline" aria-label={`Takes for line ${line.line}`}>
            {list.map((t, i) => {
              const n = list.length - i;
              const playingIt = player.playing === t.id;
              return (
                <li key={t.id} className={cx('flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 py-2.5', t.chosen && 'bg-accent-soft')}>
                  <button
                    type="button"
                    onClick={() => player.play(t.id, src(t.assetId), t.in, t.out)}
                    aria-label={playingIt ? `Stop take ${n}` : `Play take ${n}`}
                    className={cx('grid h-8 w-8 shrink-0 place-items-center rounded-full transition-colors', playingIt ? 'bg-accent text-accent-ink' : 'bg-panel text-fg hairline hover:bg-hover')}
                  >
                    {playingIt ? <Stop size={12} weight="fill" /> : <Play size={12} weight="fill" />}
                  </button>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-1.5 text-[13px]">
                      <span className="font-medium">Take {n}</span>
                      <span className="text-muted">
                        {SOURCE[t.source] ?? 'Sound'}
                        {t.voiceName ? ` (${t.voiceName})` : ''}, {t.duration.toFixed(1)} s
                      </span>
                      {t.chosen ? <Badge tone="accent">plays now</Badge> : null}
                      {t.cleanups?.length ? <Badge>cleaned-up version too</Badge> : null}
                    </div>
                    <div className="text-[11.5px] text-subtle">{relative(t.createdAt)}</div>
                  </div>
                  {!t.chosen ? (
                    <Button size="sm" variant="secondary" loading={busy === t.id} onClick={() => void choose(t, n)}>
                      Use this take
                    </Button>
                  ) : null}
                </li>
              );
            })}
          </ol>
        )}
      </section>
    </div>
  );
}

function ABChoice({ label, active, playing, onPlay, onUse, busy }: { label: string; active: boolean; playing: boolean; onPlay: () => void; onUse: () => void; busy: boolean }) {
  return (
    <div className={cx('flex items-center gap-2.5 rounded-[12px] bg-panel p-2.5 hairline', active && 'ring-2 ring-accent')}>
      <button
        type="button"
        onClick={onPlay}
        aria-label={playing ? `Stop the ${label.toLowerCase()} version` : `Play the ${label.toLowerCase()} version`}
        className={cx('grid h-9 w-9 shrink-0 place-items-center rounded-full transition-colors', playing ? 'bg-accent text-accent-ink' : 'bg-hover text-fg hover:bg-active')}
      >
        {playing ? <Stop size={13} weight="fill" /> : <Play size={13} weight="fill" />}
      </button>
      <div className="min-w-0 flex-1 text-[13px] font-medium">{label}</div>
      {active ? (
        <span className="inline-flex items-center gap-1 text-[12px] font-medium text-accent-text">
          <Check size={13} weight="bold" /> In the video
        </span>
      ) : (
        <Button size="sm" variant="ghost" loading={busy} onClick={onUse} aria-label={`Use the ${label.toLowerCase()} version`}>
          Use this
        </Button>
      )}
    </div>
  );
}
