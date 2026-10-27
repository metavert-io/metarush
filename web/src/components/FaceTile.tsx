import { useEffect, useState } from 'react';
import { UserCircle } from '@phosphor-icons/react';
import { media } from '../api/client';
import { cx } from '../ui/primitives';

/** Where a face is in a still: [x, y, width, height] from the top-left, 0 to 1. */
export type FaceBox = number[];

/**
 * A face, cut out of the still that shows it best: the frame at `at` of the
 * clip, zoomed onto `box` with some room around it. Nothing is stored for it:
 * the daemon cuts the still on demand (and caches it).
 */
export function FaceTile({ assetId, at, box, size = 72, label, className, round }: {
  assetId: string;
  at: number;
  box: FaceBox;
  size?: number;
  /** What the picture shows, for screen readers ("Maya's face in Kitchen chat"). */
  label: string;
  className?: string;
  round?: boolean;
}) {
  const src = media.frame(assetId, at, 960);
  const [aspect, setAspect] = useState<number | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    setAspect(null);
    setFailed(false);
    const img = new Image();
    img.onload = () => setAspect(img.naturalWidth / Math.max(1, img.naturalHeight));
    img.onerror = () => setFailed(true);
    img.src = src;
    return () => {
      img.onload = null;
      img.onerror = null;
    };
  }, [src]);
  const style: React.CSSProperties = { width: size, height: size };
  if (aspect && box.length === 4) {
    const [x = 0, y = 0, w = 0, h = 0] = box;
    // A square around the face, 1.8× its size, in the picture's own units.
    const side = Math.max(w * aspect, h) * 1.8; // in units of the picture height
    const cw = Math.min(1, side / aspect);
    const ch = Math.min(1, side);
    const x0 = Math.max(0, Math.min(1 - cw, x + w / 2 - cw / 2));
    const y0 = Math.max(0, Math.min(1 - ch, y + h / 2 - ch / 2));
    style.backgroundImage = `url("${src}")`;
    style.backgroundSize = `${100 / cw}% ${100 / ch}%`;
    style.backgroundPosition = `${cw >= 1 ? 50 : (x0 / (1 - cw)) * 100}% ${ch >= 1 ? 50 : (y0 / (1 - ch)) * 100}%`;
  }
  return (
    <span
      role="img"
      aria-label={label}
      data-face-tile
      className={cx('relative inline-grid shrink-0 place-items-center overflow-hidden bg-inset bg-no-repeat text-subtle hairline', round ? 'rounded-full' : 'rounded-[12px]', !aspect && !failed && 'animate-pulse', className)}
      style={style}
    >
      {failed ? <UserCircle size={Math.round(size / 2.4)} aria-hidden /> : null}
    </span>
  );
}
