import type { OutputShape } from '../api/types';
import { shapeOf } from '../lib/shape';
import { Skeleton, cx } from './primitives';

type Size = Pick<OutputShape, 'width' | 'height'> | { width?: number; height?: number } | null | undefined;

/** The CSS aspect-ratio of a cut's shape (D376); widescreen when nothing says. */
export function shapeRatio(s: Size): string {
  return s?.width && s?.height ? `${s.width} / ${s.height}` : '16 / 9';
}

/** Width ÷ height of a cut's shape; widescreen when nothing says. */
export function shapeAspect(s: Size): number {
  return s?.width && s?.height ? s.width / s.height : 16 / 9;
}

/** A loading box at a cut's served shape (never a guessed 9:16 or 16:9): tall and square ones narrower. */
export function ShapeSkeleton({ shape, className, tall, square, wide }: { shape: Size; className?: string; tall: string; square: string; wide?: string }) {
  const s = shapeOf(shape?.width, shape?.height);
  return <Skeleton className={cx('mx-auto w-full', s === 'vertical' ? tall : s === 'square' ? square : wide, className)} style={{ aspectRatio: shapeRatio(shape) }} />;
}
