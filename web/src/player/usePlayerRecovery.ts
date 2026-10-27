import { useCallback, useEffect, useMemo, useRef, useState, type RefObject } from 'react';
import type { PlayerRef } from '@remotion/player';
import type { RenderPlan } from '../api/types';
import { mediaPath, probeAll, Recovery, sourcesNear, stallWatch, type Trouble } from './recovery';

/** Where the preview was, to come back to. */
export interface PlayerPlace {
  frame: number;
  playing: boolean;
}

export interface PlayerRecovery {
  /** The line to show (null: playing as it should). */
  trouble: Trouble | null;
  /** The player is taken down while MetaRush can't send its files (Remotion would ask again and again). */
  down: boolean;
  /** Changes for each try: the player is mounted afresh. */
  mountKey: number;
  /** Where it was when the trouble began (and what a seek or play meanwhile changed). */
  place: () => PlayerPlace;
  /** A seek, play or pause while it is down: done when it comes back. */
  remember: (p: Partial<PlayerPlace>) => void;
}

const FAILED_LINE = "The preview can't play this cut right now.";

/**
 * Keeps the Remotion preview going through a short outage (D180). The files
 * the plan plays are <video>/<audio> elements inside the player; on a media
 * error from one of them, or no new frame and no new bytes for STALL_MS while
 * it plays, it notes the frame, asks MetaRush about the files around it,
 * shows a plain line and, while MetaRush can't send them, takes the player
 * down (its media elements would reload in a tight loop). Once MetaRush sends
 * them, the player is mounted again at that frame and plays on if it played.
 * resetKey (the version shown) starts over.
 */
export function usePlayerRecovery(playerRef: RefObject<PlayerRef | null>, plan: RenderPlan | undefined, enabled: boolean, resetKey: string): PlayerRecovery {
  const [trouble, setTrouble] = useState<Trouble | null>(null);
  const [down, setDown] = useState(false);
  const [mountKey, setMountKey] = useState(0);
  const place = useRef<PlayerPlace>({ frame: 0, playing: false });
  const pending = useRef<PlayerPlace | null>(null);
  const failing = useRef<string | undefined>(undefined);
  const planRef = useRef(plan);
  planRef.current = plan;
  const recRef = useRef<Recovery | null>(null);

  useEffect(() => {
    if (!enabled) return;
    const rec = new Recovery({
      probe: (signal) => probeAll(sourcesNear(planRef.current, place.current.frame, failing.current), signal),
      show: setTrouble,
      idle: () => setDown(true),
      retry: () => {
        pending.current = { ...place.current };
        setDown(false);
        setMountKey((k) => k + 1);
      },
      failedLine: FAILED_LINE,
    });
    recRef.current = rec;
    return () => {
      rec.dispose();
      recRef.current = null;
      pending.current = null;
      setTrouble(null);
      setDown(false);
    };
  }, [enabled, resetKey]);

  useEffect(() => {
    const rec = recRef.current;
    const p = playerRef.current;
    if (!enabled || down || !rec || !p) return;
    const watch = stallWatch();
    let healFrom = Number.NaN;
    let healTimer = 0;
    // A file that fails while another clip still plays (the next one, loading
    // ahead) is trouble too: playing on doesn't count as healed until no file
    // has failed for a while.
    let lastError = 0;
    const begin = (why: 'stall' | 'error', code?: number, src?: string) => {
      if (why === 'error') lastError = performance.now();
      if (!rec.active) {
        place.current = { frame: p.getCurrentFrame(), playing: p.isPlaying() };
        failing.current = src;
        healFrom = place.current.frame;
      }
      rec.begin(why, code);
    };
    const onFrame = (e: { detail: { frame: number } }) => {
      watch.position(e.detail.frame);
      if (rec.active && !rec.failed && p.isPlaying() && e.detail.frame > healFrom && performance.now() - lastError > 2000) rec.healed();
    };
    const onPlay = () => watch.reset(); // starting to play is not a stall
    p.addEventListener('frameupdate', onFrame);
    p.addEventListener('play', onPlay);
    const box = p.getContainerNode();
    // Media events don't bubble: listen while they go down to the element.
    const onMediaError = (e: Event) => {
      const t = e.target;
      if (t instanceof HTMLMediaElement) begin('error', t.error?.code, t.currentSrc || t.src);
    };
    const onData = (e: Event) => {
      if (e.target instanceof HTMLMediaElement) watch.data();
    };
    const onLoaded = (e: Event) => {
      if (e.target instanceof HTMLMediaElement && rec.retrying && !p.isPlaying() && performance.now() - lastError > 2000) rec.healed();
    };
    box?.addEventListener('error', onMediaError, true);
    box?.addEventListener('progress', onData, true);
    box?.addEventListener('loadeddata', onLoaded, true);
    // Remotion also throws for a file that failed to load (once per file); that is handled here.
    const ours = (src: string) => Object.values(planRef.current?.sources ?? {}).some((s) => mediaPath(s) === mediaPath(src));
    const onWindowError = (e: ErrorEvent) => {
      const err = e.error as { name?: string; src?: unknown } | null;
      if (err?.name !== 'MediaPlaybackError' || typeof err.src !== 'string' || !ours(err.src)) return;
      e.preventDefault();
      begin('error', undefined, err.src);
    };
    window.addEventListener('error', onWindowError);
    // Back after a try: where it was, playing if it played.
    const want = pending.current;
    if (want) {
      pending.current = null;
      healFrom = want.frame;
      p.seekTo(want.frame);
      if (want.playing) p.play();
      // Paused: loaded again is enough (a stretch with no footage loads nothing).
      else healTimer = window.setTimeout(() => rec.retrying && !p.isPlaying() && performance.now() - lastError > 2000 && rec.healed(), 3000);
    }
    const tick = window.setInterval(() => {
      if (rec.failed || (rec.active && !rec.retrying)) return;
      if (watch.stalled(p.isPlaying())) begin('stall');
    }, 1000);
    return () => {
      window.clearInterval(tick);
      window.clearTimeout(healTimer);
      p.removeEventListener('frameupdate', onFrame);
      p.removeEventListener('play', onPlay);
      box?.removeEventListener('error', onMediaError, true);
      box?.removeEventListener('progress', onData, true);
      box?.removeEventListener('loadeddata', onLoaded, true);
      window.removeEventListener('error', onWindowError);
    };
  }, [enabled, down, mountKey, playerRef]);

  const remember = useCallback((p: Partial<PlayerPlace>) => {
    place.current = { ...place.current, ...p };
  }, []);
  const getPlace = useCallback(() => place.current, []);
  return useMemo(() => ({ trouble, down, mountKey, place: getPlace, remember }), [trouble, down, mountKey, getPlace, remember]);
}
