import { forwardRef, useImperativeHandle, useRef, type VideoHTMLAttributes } from 'react';
import { WarningCircle } from '@phosphor-icons/react';
import { cx, Spinner } from '../ui/primitives';
import { troubleLine, type Trouble } from './recovery';
import { useMediaRecovery } from './useMediaRecovery';

/** A player's plain line while it can't play (D180): over the picture, at the top. */
export function TroubleLine({ trouble, className }: { trouble: Trouble; className?: string }) {
  return (
    <div
      className={cx(
        'pointer-events-none absolute top-2 z-10 flex max-w-[calc(100%-1rem)] items-center gap-2 rounded-[10px] bg-black/75 px-2.5 py-1 text-[11.5px] leading-snug text-white',
        className ?? 'left-2',
      )}
      role="status"
      aria-live="polite"
      data-player-trouble={trouble.kind}
    >
      {trouble.kind === 'failed' ? <WarningCircle size={13} className="shrink-0 text-[#e8b169]" /> : <Spinner size={10} />}
      <span>{troubleLine(trouble)}</span>
    </div>
  );
}

/** Instead of the picture while the preview is down: the same line, in the middle. */
export function TroublePanel({ trouble }: { trouble: Trouble }) {
  return (
    <div className="absolute inset-0 grid place-items-center p-6 text-center" role="status" aria-live="polite" data-player-trouble={trouble.kind} data-player-down="">
      <div className="flex max-w-[340px] items-center gap-2 text-[12.5px] leading-relaxed text-[#d9d1c7]">
        {trouble.kind === 'failed' ? <WarningCircle size={15} className="shrink-0 text-[#e8b169]" /> : <Spinner size={13} />}
        <span>{troubleLine(trouble)}</span>
      </div>
    </div>
  );
}

/**
 * A <video> that keeps going through a short outage (useMediaRecovery), with
 * its line over the picture. wrapClassName styles the box around it.
 */
export const RecoveringVideo = forwardRef<HTMLVideoElement, VideoHTMLAttributes<HTMLVideoElement> & { wrapClassName?: string; failedLine?: string }>(
  function RecoveringVideo({ wrapClassName, failedLine, src, ...props }, ref) {
    const el = useRef<HTMLVideoElement>(null);
    useImperativeHandle(ref, () => el.current as HTMLVideoElement, []);
    const { trouble } = useMediaRecovery(el, src, failedLine ?? "This video can't be played here.");
    return (
      <div className={cx('relative', wrapClassName)}>
        <video ref={el} src={src} {...props} />
        {trouble ? <TroubleLine trouble={trouble} className="left-1/2 -translate-x-1/2" /> : null}
      </div>
    );
  },
);
