import { useEffect, useState } from 'react';
import { initials } from '../lib/format';
import { cx } from './primitives';

/**
 * A person's picture (D184), or their initials when they have none or it
 * doesn't load. `src` is the address the server gives (/media/avatars/<id>?v=…).
 * Pass `marker` to put a data attribute on the picture itself, for tests.
 */
export function Avatar({
  src,
  name,
  size = 28,
  muted,
  className,
  marker,
}: {
  src?: string | null;
  name: string;
  size?: number;
  /** Greyed initials (an account that can't sign in). */
  muted?: boolean;
  className?: string;
  marker?: string;
}) {
  const [failed, setFailed] = useState(false);
  useEffect(() => setFailed(false), [src]);
  const style = { width: size, height: size };
  if (src && !failed) {
    return (
      <img
        src={src}
        alt=""
        width={size}
        height={size}
        draggable={false}
        onError={() => setFailed(true)}
        className={cx('shrink-0 rounded-full bg-hover object-cover', muted && 'opacity-60', className)}
        style={style}
        {...(marker ? { [`data-${marker}`]: '' } : {})}
      />
    );
  }
  return (
    <span
      className={cx('grid shrink-0 place-items-center rounded-full font-semibold leading-none', muted ? 'bg-hover text-muted' : 'bg-accent-soft text-accent-text', className)}
      style={{ ...style, fontSize: Math.max(9, Math.round(size * 0.38)) }}
      aria-hidden
      data-initials=""
    >
      {initials(name)}
    </span>
  );
}
