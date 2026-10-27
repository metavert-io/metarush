import { Sparkle } from '@phosphor-icons/react';
import type { AssetSource } from '../../api/types';
import { cx } from '../../ui/primitives';

/*
 * AI-generated clips are flagged everywhere (D127): a badge on tiles, rows,
 * the clip's header, the timeline and the Script. One helper decides.
 */

export function isGenerated(source: AssetSource | null | undefined): boolean {
  return !!source && (source.platform === 'generated' || !!source.generated);
}

/** The provider in a few words: "Google Gemini". */
export function madeWith(source: AssetSource | null | undefined): string {
  return source?.generated?.providerTitle || 'generative video';
}

/** A price as people read it: $0.61, less than a cent. */
export function money(usd: number | undefined | null): string {
  if (usd === undefined || usd === null) return '';
  if (usd > 0 && usd < 0.01) return 'less than a cent';
  return `$${usd.toFixed(2)}`;
}

/**
 * The AI badge. `overlay` sits on a thumbnail; the default is a chip in text.
 * The title says what made it.
 */
export function AIBadge({ source, overlay, className, label }: { source: AssetSource | null | undefined; overlay?: boolean; className?: string; label?: string }) {
  if (!isGenerated(source)) return null;
  const who = madeWith(source);
  const title = `AI-generated with ${who}. It is marked as AI-generated in exports, and the quality check notes it.`;
  if (overlay)
    return (
      <span
        className={cx('inline-flex items-center gap-0.5 rounded-[6px] bg-black/70 px-1.5 py-0.5 text-[10px] font-semibold tracking-[0.02em] text-white', className)}
        title={title}
        data-ai-badge
      >
        <Sparkle size={10} weight="fill" aria-hidden /> AI
      </span>
    );
  return (
    <span className={cx('inline-flex shrink-0 items-center gap-1 rounded-full bg-info-soft px-2 py-[1px] text-[11px] font-medium text-info', className)} title={title} data-ai-badge>
      <Sparkle size={11} weight="fill" aria-hidden />
      {label ?? 'AI-generated'}
    </span>
  );
}
