import { Component, forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState, type ReactNode } from 'react';
import { Player, type PlayerRef } from '@remotion/player';
import { keepPreviousData } from '@tanstack/react-query';
import { MetaRushEdit, planMetadata } from '@metarush/video/lib';
import { FilmStrip, WarningCircle } from '@phosphor-icons/react';
import { media } from '../api/client';
import { useOp } from '../api/queries';
import type { Render, RenderPlan } from '../api/types';
import { usePlayerHotkey } from '../lib/players';
import { cx, Details, ErrorPanel, Spinner } from '../ui/primitives';
import { compensateMono } from './compensate';
import { CaptionFix } from '../components/transcripts/CaptionFix';
import { RecoveringVideo, TroubleLine, TroublePanel } from './Trouble';
import { usePlayerRecovery } from './usePlayerRecovery';

/*
 * The ONE place the dashboard touches the Remotion Player. It previews a revision
 * by rendering its Go-resolved RenderPlan (proxy sources) with @metarush/video's
 * MetaRushEdit, exactly as the worker renders it. If the plan or the player
 * fails, it falls back to a finished render MP4, clearly labeled as such, and
 * never fakes a preview.
 */

export interface CutPreviewHandle {
  seekToFrame(frame: number): void;
  seekToSeconds(s: number): void;
  play(): void;
  pause(): void;
  toggle(): void;
  /** The player's own volume (0..1, on top of the mix); the recorder turns it down to a quiet guide. */
  setVolume(v: number): void;
  mute(): void;
  unmute(): void;
  /** Where the playhead is (frames), and whether it plays. */
  getFrame(): number;
  isPlaying(): boolean;
}

export const CutPreview = forwardRef<CutPreviewHandle, {
  revisionId: string;
  renders: Render[];
  onFrame?: (frame: number, fps: number) => void;
  onPlan?: (plan: RenderPlan) => void;
  className?: string;
  maxHeight?: string;
  /**
   * Play this plan instead of the revision's: the timeline's unsaved changes, or
   * a mix being tried before it is saved (both resolved by Go).
   */
  plan?: RenderPlan;
  /** Shuttle speed (negative plays backwards). */
  playbackRate?: number;
  /** Called when playback starts, pauses or ends. */
  onPlayingChange?: (playing: boolean) => void;
  /**
   * Clicking a caption fixes its words (D145): the version's own plan only
   * (a version keeps its words; the fix goes to the transcript).
   */
  fixCaptions?: { projectId?: string };
  /**
   * Play this finished render (a review copy or the final render) instead of
   * the live preview, because the person chose it (D187): the real thing,
   * motion graphics moving. Same-origin, with the D180 recovery.
   */
  playRender?: Render;
}>(function CutPreview({ revisionId, renders, onFrame, onPlan, className, maxHeight = 'min(70dvh, 720px)', plan: planOverride, playbackRate = 1, onPlayingChange, fixCaptions, playRender }, ref) {
  // Keep playing the previous version's plan while a new version's loads (no flash).
  const fetched = useOp('revision.plan', planOverride ? null : { id: revisionId }, { staleTime: 60_000, placeholderData: keepPreviousData });
  const plan = planOverride ? { data: planOverride, isLoading: false, error: null as null | typeof fetched.error, refetch: fetched.refetch } : fetched;
  // Local first: copy the proxies this cut plays into this machine's cache so the
  // player reads them from disk (no buffering re-seeks heard as repeated words).
  const prep = useOp('preview.prepare', { revisionId }, { refetchInterval: (q) => (q.state.data && q.state.data.ready < q.state.data.total ? 1200 : false), staleTime: 5_000 });
  const [playerFailed, setPlayerFailed] = useState<string | null>(null);
  const playerRef = useRef<PlayerRef>(null);
  const videoRef = useRef<HTMLVideoElement>(null);
  const [buffering, setBuffering] = useState(false);
  // The caption being fixed (the frame it was clicked at).
  const [captionAt, setCaptionAt] = useState<number | null>(null);
  const captionsFixable = !!fixCaptions && !planOverride;
  const stalls = useRef<{ n: number; since: number }>({ n: 0, since: 0 });
  const [stutters, setStutters] = useState(false);
  // A new object every render would make the Player re-render the whole composition.
  // Mono files are compensated for the preview only (see compensate.ts).
  const inputProps = useMemo(() => (plan.data ? { plan: compensateMono(plan.data) } : null), [plan.data]);

  useEffect(() => {
    setPlayerFailed(null);
    setStutters(false);
    stalls.current = { n: 0, since: 0 };
  }, [revisionId]);

  const chosen = playRender && playRender.state === 'available' && playRender.output ? playRender : undefined;
  const fallback = useMemo(() => chosen ?? pickFallback(renders, revisionId), [chosen, renders, revisionId]);
  const usePlayer = !chosen && !!plan.data && !playerFailed && !!MetaRushEdit && !!planMetadata;
  const meta = useMemo(() => (plan.data && planMetadata ? planMetadata(plan.data) : null), [plan.data]);
  const fps = meta?.fps ?? fallback?.output?.fps ?? 30;
  // Through a short outage (D180): a plain line instead of an endless
  // "Buffering…", tries with growing pauses, back where it was.
  const recovery = usePlayerRecovery(playerRef, plan.data, usePlayer, revisionId);
  const troubled = useRef(false);
  troubled.current = !!recovery.trouble;

  // Buffering: show it instead of skipping, and suggest a render if it keeps happening.
  useEffect(() => {
    setBuffering(false);
    const p = playerRef.current;
    if (!p) return;
    const onWaiting = () => {
      setBuffering(true);
      if (troubled.current) return; // an outage isn't this computer stuttering
      const now = Date.now();
      const s = stalls.current;
      if (now - s.since > 60_000) stalls.current = { n: 1, since: now };
      else s.n += 1;
      if (stalls.current.n >= 3) setStutters(true);
    };
    const onResume = () => setBuffering(false);
    p.addEventListener('waiting', onWaiting);
    p.addEventListener('resume', onResume);
    return () => {
      p.removeEventListener('waiting', onWaiting);
      p.removeEventListener('resume', onResume);
    };
  }, [plan.data, playerFailed, recovery.mountKey, recovery.down]);
  useEffect(() => {
    if (plan.data) onPlan?.(plan.data);
  }, [plan.data, onPlan]);
  useEffect(() => {
    const p = playerRef.current;
    if (!p || !onPlayingChange) return;
    const on = () => onPlayingChange(true);
    const off = () => onPlayingChange(false);
    p.addEventListener('play', on);
    p.addEventListener('pause', off);
    p.addEventListener('ended', off);
    return () => {
      p.removeEventListener('play', on);
      p.removeEventListener('pause', off);
      p.removeEventListener('ended', off);
    };
  }, [onPlayingChange, plan.data, playerFailed, recovery.mountKey, recovery.down]);
  // Taken down while playing: it isn't playing now (it plays on when it comes back).
  useEffect(() => {
    if (recovery.down && recovery.place().playing) onPlayingChange?.(false);
  }, [recovery.down, recovery, onPlayingChange]);

  const toggle = useCallback(() => {
    if (usePlayer && recovery.down) recovery.remember({ playing: !recovery.place().playing });
    else if (usePlayer) playerRef.current?.toggle();
    else {
      const v = videoRef.current;
      if (v) {
        if (v.paused) void v.play().catch(() => undefined);
        else v.pause();
      }
    }
  }, [usePlayer, recovery]);
  const activate = usePlayerHotkey({ toggle }, true);

  // While the preview is down (D180), seeks and play/pause are kept for when it comes back.
  const down = usePlayer && recovery.down;
  useImperativeHandle(
    ref,
    () => ({
      seekToFrame: (f) => {
        activate();
        if (down) recovery.remember({ frame: Math.max(0, Math.round(f)) });
        else if (usePlayer) playerRef.current?.seekTo(Math.max(0, Math.round(f)));
        else if (videoRef.current) videoRef.current.currentTime = f / fps;
      },
      seekToSeconds: (s) => {
        activate();
        if (down) recovery.remember({ frame: Math.max(0, Math.round(s * fps)) });
        else if (usePlayer) playerRef.current?.seekTo(Math.max(0, Math.round(s * fps)));
        else if (videoRef.current) videoRef.current.currentTime = s;
      },
      play: () => (down ? recovery.remember({ playing: true }) : usePlayer ? playerRef.current?.play() : void videoRef.current?.play().catch(() => undefined)),
      pause: () => (down ? recovery.remember({ playing: false }) : usePlayer ? playerRef.current?.pause() : videoRef.current?.pause()),
      toggle,
      setVolume: (v) => {
        const x = Math.max(0, Math.min(1, v));
        if (usePlayer) playerRef.current?.setVolume(x);
        else if (videoRef.current) videoRef.current.volume = x;
      },
      mute: () => (usePlayer ? playerRef.current?.mute() : videoRef.current && (videoRef.current.muted = true)),
      unmute: () => (usePlayer ? playerRef.current?.unmute() : videoRef.current && (videoRef.current.muted = false)),
      getFrame: () => (down ? recovery.place().frame : usePlayer ? (playerRef.current?.getCurrentFrame() ?? 0) : Math.round((videoRef.current?.currentTime ?? 0) * fps)),
      isPlaying: () => (down ? recovery.place().playing : usePlayer ? !!playerRef.current?.isPlaying() : !!videoRef.current && !videoRef.current.paused),
    }),
    [usePlayer, fps, toggle, activate, down, recovery],
  );

  // Frame updates from the Remotion player, throttled to ~12 Hz for the UI.
  useEffect(() => {
    const p = playerRef.current;
    if (!usePlayer || !p || !onFrame) return;
    let last = 0;
    const onUpdate = (e: { detail: { frame: number } }) => {
      const now = performance.now();
      if (now - last < 80) return;
      last = now;
      onFrame(e.detail.frame, fps);
    };
    const onSeeked = (e: { detail: { frame: number } }) => onFrame(e.detail.frame, fps);
    p.addEventListener('frameupdate', onUpdate);
    p.addEventListener('seeked', onSeeked);
    return () => {
      p.removeEventListener('frameupdate', onUpdate);
      p.removeEventListener('seeked', onSeeked);
    };
  }, [usePlayer, onFrame, fps, plan.data, recovery.mountKey, recovery.down]);

  const aspect = chosen?.output ? chosen.output.width / chosen.output.height : meta ? meta.compositionWidth / meta.compositionHeight : fallback?.output ? fallback.output.width / fallback.output.height : 9 / 16;

  return (
    <div
      className={cx('min-w-0', captionsFixable && '[&_[data-caption-line]]:cursor-text [&_[data-caption-line]:hover]:outline [&_[data-caption-line]:hover]:outline-2 [&_[data-caption-line]:hover]:outline-offset-4 [&_[data-caption-line]:hover]:outline-white/60', className)}
      data-player-surface
      data-fix-captions={captionsFixable || undefined}
      onPointerDown={() => activate()}
      onPointerDownCapture={(e) => {
        if (!captionsFixable || !usePlayer) return;
        const t = e.target as HTMLElement | null;
        if (!t?.closest?.('[data-caption-line]')) return;
        // A click on a caption fixes its words instead of playing or pausing.
        e.stopPropagation();
        e.preventDefault();
        playerRef.current?.pause();
        setCaptionAt(playerRef.current?.getCurrentFrame() ?? 0);
      }}
      onClickCapture={(e) => {
        if (captionsFixable && (e.target as HTMLElement | null)?.closest?.('[data-caption-line]')) e.stopPropagation();
      }}
    >
      <div className="relative mx-auto overflow-hidden rounded-[14px] bg-inset" style={{ aspectRatio: String(aspect), maxHeight, maxWidth: `calc(${maxHeight} * ${aspect})` }}>
        {plan.isLoading && !chosen ? (
          <div className="absolute inset-0 grid place-items-center">
            <div className="flex items-center gap-2 text-[12.5px] text-[#a69d93]">
              <Spinner size={13} /> Getting the preview ready…
            </div>
          </div>
        ) : usePlayer && meta && recovery.down && recovery.trouble ? (
          <TroublePanel trouble={recovery.trouble} />
        ) : usePlayer && meta ? (
          <PlayerBoundary onError={(m) => setPlayerFailed(m)}>
            <Player
              key={recovery.mountKey}
              ref={playerRef}
              component={MetaRushEdit!}
              inputProps={inputProps!}
              bufferStateDelayInMilliseconds={300}
              durationInFrames={meta.durationInFrames}
              fps={meta.fps}
              compositionWidth={meta.compositionWidth}
              compositionHeight={meta.compositionHeight}
              controls
              clickToPlay
              doubleClickToFullscreen
              allowFullscreen
              spaceKeyToPlayOrPause={false}
              playbackRate={playbackRate}
              showVolumeControls
              errorFallback={({ error }) => <PlayerError message={error.message} />}
              renderLoading={() => (
                <div className="grid h-full w-full place-items-center">
                  <Spinner size={16} className="text-[#a69d93]" />
                </div>
              )}
              style={{ width: '100%', height: '100%' }}
            />
            {recovery.trouble ? (
              <TroubleLine trouble={recovery.trouble} />
            ) : prep.data && prep.data.ready < prep.data.total ? (
              <div className="pointer-events-none absolute left-2 top-2 flex items-center gap-2 rounded-full bg-black/70 px-2.5 py-1 text-[11.5px] text-white" role="status">
                <Spinner size={10} />
                Preparing preview… {prep.data.ready} of {prep.data.total}
                {prep.data.bytesTotal ? <span className="tc text-white/70">{Math.floor((prep.data.bytesDone / prep.data.bytesTotal) * 100)}%</span> : null}
              </div>
            ) : buffering ? (
              <div className="pointer-events-none absolute left-2 top-2 flex items-center gap-2 rounded-full bg-black/70 px-2.5 py-1 text-[11.5px] text-white" role="status">
                <Spinner size={10} /> Buffering…
              </div>
            ) : null}
            {captionAt !== null ? (
              <div className="absolute inset-x-0 bottom-14 z-20 flex justify-center px-2">
                <CaptionFix revisionId={revisionId} projectId={fixCaptions?.projectId} frame={captionAt} onClose={() => setCaptionAt(null)} />
              </div>
            ) : null}
          </PlayerBoundary>
        ) : fallback?.output ? (
          <RecoveringVideo
            ref={videoRef}
            key={fallback.id}
            src={media.render(fallback.id)}
            poster={fallback.output.thumbnailKey ? media.renderThumb(fallback.id) : undefined}
            controls
            playsInline
            wrapClassName="h-full w-full"
            className="h-full w-full bg-black object-contain"
            onTimeUpdate={(e) => onFrame?.(Math.round(e.currentTarget.currentTime * fps), fps)}
          />
        ) : (
          <div className="absolute inset-0 grid place-items-center p-6 text-center">
            <div className="max-w-[300px]">
              <div className="mx-auto mb-3 grid h-11 w-11 place-items-center rounded-[12px] bg-white/5 text-[#a69d93]">
                <FilmStrip size={20} />
              </div>
              <div className="text-[14px] font-medium text-[#efe8de]">Preview unavailable</div>
              <div className="mt-1 text-[12.5px] leading-relaxed text-[#a69d93]">
                {plan.error ? 'MetaRush could not get this cut ready to play.' : playerFailed ? 'The preview stopped working.' : 'Nothing to show yet.'} Make a review copy to watch this cut.
              </div>
            </div>
          </div>
        )}
      </div>
      {!chosen && !usePlayer && fallback?.output && !plan.isLoading ? (
        <div className="mt-2 flex items-center justify-center gap-1.5 text-[12px] text-accent-text">
          <WarningCircle size={13} />
          Playing the {fallback.quality === 'review' ? 'review copy' : 'final render'} of version {fallback.revisionNumber}{fallback.revisionId !== revisionId ? ', not this version' : ''}, because the live preview {playerFailed ? 'stopped working' : "isn't available"}.
        </div>
      ) : null}
      {captionsFixable && usePlayer ? <p className="mt-1.5 text-center text-[11.5px] text-subtle">Click a caption to fix its words.</p> : null}
      {stutters && usePlayer ? (
        <div className="mt-2 flex items-center justify-center gap-1.5 text-[12px] text-muted">
          <WarningCircle size={13} className="text-accent-text" />
          The preview may stutter on this computer. Make a review copy to see it exactly.
        </div>
      ) : null}
      {plan.error && !fallback ? <ErrorPanel className="mt-3" compact error={plan.error} onRetry={() => void plan.refetch()} /> : null}
      {playerFailed ? (
        <Details compact label="What went wrong in the preview" className="mt-2 text-center">
          {playerFailed}
        </Details>
      ) : null}
    </div>
  );
});

function pickFallback(renders: Render[], revisionId: string): Render | undefined {
  const ok = renders.filter((r) => r.state === 'available' && r.output);
  const rank = (r: Render) => (r.revisionId === revisionId ? 2 : 0) + (r.quality === 'final' ? 1 : 0);
  return ok.sort((a, b) => rank(b) - rank(a) || b.createdAt.localeCompare(a.createdAt))[0];
}

function PlayerError({ message }: { message: string }) {
  return (
    <div className="grid h-full w-full place-items-center p-6 text-center">
      <div>
        <div className="text-[13.5px] font-medium text-[#efe8de]">The preview stopped working</div>
        <div className="mt-1 text-[12px] text-[#a69d93]">Try reloading the page, or make a review copy to watch this cut.</div>
        <Details compact className="mt-2 text-[#a69d93]">
          {message}
        </Details>
      </div>
    </div>
  );
}

class PlayerBoundary extends Component<{ children: ReactNode; onError: (m: string) => void }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  componentDidCatch(error: unknown) {
    this.props.onError(error instanceof Error ? error.message : String(error));
  }
  render() {
    return this.state.failed ? null : this.props.children;
  }
}
