import { useCallback, useEffect, useMemo, useRef, useState, type RefObject } from 'react';
import { probeMedia, Recovery, stallWatch, type Trouble } from './recovery';

export interface MediaRecovery {
  /** The line to show (null: playing as it should). */
  trouble: Trouble | null;
  /** While it recovers: whether it plays once it is back (null when it isn't recovering). */
  willPlay: boolean | null;
  /**
   * Play or pause pressed while it recovers (the element itself can't play
   * then): kept for when it is back. False when it isn't recovering: play or
   * pause the element as usual.
   */
  toggle: () => boolean;
  /** A seek while it recovers: it comes back there. False when it isn't recovering. */
  seek: (t: number) => boolean;
}

/**
 * Keeps a <video> or <audio> element playing through a short outage (D180):
 * on a stall while it plays, or a media error, it says what is wrong and tries
 * again with growing pauses (recovery.ts), then loads the file again where it
 * was and plays on if it was playing (or if Play was pressed meanwhile).
 * failedLine is said when the server sends the file but it won't play here.
 */
export function useMediaRecovery(ref: RefObject<HTMLMediaElement | null>, src: string | undefined, failedLine?: string): MediaRecovery {
  const [trouble, setTrouble] = useState<Trouble | null>(null);
  const [willPlay, setWillPlay] = useState<boolean | null>(null);
  const toggleRef = useRef<() => boolean>(() => false);
  const seekRef = useRef<(t: number) => boolean>(() => false);
  useEffect(() => {
    setTrouble(null);
    setWillPlay(null);
    const el = ref.current;
    if (!el || !src) return;
    const watch = stallWatch();
    // Where it was and whether it played when the trouble began (pressing
    // play, pause or seeking meanwhile changes it).
    let resume = { at: 0, play: false };
    let healFrom = Number.NaN; // healed once it plays past here (after a try, or by itself)
    let ownLoad = 0;
    const intend = (play: boolean) => {
      resume.play = play;
      setWillPlay(play);
    };
    const rec = new Recovery({
      probe: (signal) => probeMedia(el.currentSrc || src, signal),
      show: (t) => {
        setTrouble(t);
        if (!t || t.kind === 'failed') setWillPlay(null);
      },
      failedLine,
      retry: () => {
        const { at, play } = resume;
        const onMeta = () => {
          if (at > 0 && Number.isFinite(at)) {
            try {
              el.currentTime = Math.min(at, Number.isFinite(el.duration) ? el.duration : at);
            } catch {
              // a file without a known length: it starts from the beginning
            }
          }
          if (play) {
            healFrom = el.currentTime;
            // Refused (the browser wants a click first): it loaded, the person presses play.
            void el.play().catch(() => rec.healed());
          } else {
            rec.healed(); // paused: being loaded again was all it needed
          }
        };
        el.addEventListener('loadedmetadata', onMeta, { once: true });
        watch.reset();
        ownLoad = performance.now();
        el.load();
      },
    });
    const recovering = () => rec.active && !rec.failed;
    toggleRef.current = () => {
      if (!recovering()) return false;
      intend(!resume.play);
      return true;
    };
    seekRef.current = (t) => {
      if (!recovering()) return false;
      resume.at = healFrom = t;
      return true;
    };
    const begin = (why: 'stall' | 'error', code?: number) => {
      if (!rec.active) {
        resume = { at: el.currentTime, play: !el.paused };
        healFrom = el.currentTime;
        setWillPlay(resume.play);
      }
      rec.begin(why, code);
    };
    const onError = () => {
      if (!el.getAttribute('src') && !el.currentSrc) return;
      begin('error', el.error?.code);
    };
    const onTime = () => {
      watch.position(el.currentTime);
      if (rec.active && !el.paused && el.currentTime > healFrom + 0.05) rec.healed();
    };
    const onData = () => watch.data();
    const onPause = () => {
      if (recovering() && performance.now() - ownLoad > 500) intend(false);
    };
    const onPlay = () => {
      if (recovering()) intend(true);
      watch.reset(); // starting to play is not a stall
    };
    el.addEventListener('error', onError);
    el.addEventListener('timeupdate', onTime);
    el.addEventListener('progress', onData);
    el.addEventListener('pause', onPause);
    el.addEventListener('play', onPlay);
    const tick = window.setInterval(() => {
      if (rec.failed || (rec.active && !rec.retrying)) return;
      if (watch.stalled(!el.paused && !el.ended)) begin('stall');
    }, 1000);
    return () => {
      rec.dispose();
      toggleRef.current = () => false;
      seekRef.current = () => false;
      window.clearInterval(tick);
      el.removeEventListener('error', onError);
      el.removeEventListener('timeupdate', onTime);
      el.removeEventListener('progress', onData);
      el.removeEventListener('pause', onPause);
      el.removeEventListener('play', onPlay);
    };
  }, [ref, src, failedLine]);
  const toggle = useCallback(() => toggleRef.current(), []);
  const seek = useCallback((t: number) => seekRef.current(t), []);
  return useMemo(() => ({ trouble, willPlay, toggle, seek }), [trouble, willPlay, toggle, seek]);
}
