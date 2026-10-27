import { useEffect, useMemo, useRef, useState } from 'react';
import { Player, type PlayerRef } from '@remotion/player';
import { MetaRushEdit, planMetadata } from '@metarush/video/lib';
import { Pause, Play, SpeakerHigh, SpeakerSlash } from '@phosphor-icons/react';
import type { RenderPlan } from '../api/types';
import { Spinner } from '../ui/primitives';

/**
 * A few seconds of a render plan, looping: the transition picker plays the
 * cut it is about (from inFrame to outFrame) with the transition being chosen,
 * exactly as Go resolved it.
 */
export default function LoopPreview({ plan, inFrame, outFrame, busy }: { plan?: RenderPlan; inFrame: number; outFrame: number; busy?: boolean }) {
  const ref = useRef<PlayerRef>(null);
  const [playing, setPlaying] = useState(true);
  const [muted, setMuted] = useState(false);
  const meta = useMemo(() => (plan && planMetadata ? planMetadata(plan) : null), [plan]);
  const inputProps = useMemo(() => (plan ? { plan } : null), [plan]);
  const lo = meta ? Math.max(0, Math.min(inFrame, meta.durationInFrames - 2)) : 0;
  const hi = meta ? Math.max(lo + 1, Math.min(outFrame, meta.durationInFrames - 1)) : 1;

  useEffect(() => {
    const p = ref.current;
    if (!p) return;
    p.seekTo(lo);
    if (playing) p.play();
  }, [plan, lo]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!meta || !inputProps || !MetaRushEdit) {
    return (
      <div className="grid h-full w-full place-items-center">
        <Spinner size={16} className="text-[#a69d93]" label="Preparing the preview" />
      </div>
    );
  }
  return (
    <div className="relative h-full w-full">
      <Player
        ref={ref}
        component={MetaRushEdit}
        inputProps={inputProps}
        durationInFrames={meta.durationInFrames}
        fps={meta.fps}
        compositionWidth={meta.compositionWidth}
        compositionHeight={meta.compositionHeight}
        inFrame={lo}
        outFrame={hi}
        loop
        autoPlay
        initiallyMuted={muted}
        controls={false}
        clickToPlay={false}
        spaceKeyToPlayOrPause={false}
        style={{ width: '100%', height: '100%' }}
        acknowledgeRemotionLicense
      />
      <div className="absolute bottom-2 left-2 flex gap-1">
        <button
          type="button"
          onClick={() => {
            const p = ref.current;
            if (!p) return;
            if (playing) p.pause();
            else p.play();
            setPlaying(!playing);
          }}
          className="grid h-8 w-8 place-items-center rounded-full bg-black/60 text-white hover:bg-black/80"
          aria-label={playing ? 'Pause the preview' : 'Play the preview'}
        >
          {playing ? <Pause size={14} weight="fill" /> : <Play size={14} weight="fill" />}
        </button>
        <button
          type="button"
          onClick={() => {
            const p = ref.current;
            if (!p) return;
            if (muted) p.unmute();
            else p.mute();
            setMuted(!muted);
          }}
          className="grid h-8 w-8 place-items-center rounded-full bg-black/60 text-white hover:bg-black/80"
          aria-label={muted ? 'Sound on' : 'Sound off'}
        >
          {muted ? <SpeakerSlash size={14} /> : <SpeakerHigh size={14} />}
        </button>
      </div>
      {busy ? (
        <div className="pointer-events-none absolute right-2 top-2 rounded-full bg-black/60 p-1.5 text-white">
          <Spinner size={12} />
        </div>
      ) : null}
    </div>
  );
}
