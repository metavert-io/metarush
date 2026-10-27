import { WarningCircle } from '@phosphor-icons/react';
import { cx } from '../../ui/primitives';

/*
 * What changed between two versions of a recipe (D212), part by part — what
 * changes each run, its sections, rules, sign-offs and hints, the title, the
 * video, sound, graphics and pictures — each change a sentence from the server
 * (recipe.diff, and Claude's cards for a draft or a version made current).
 * Rules the newer version no longer keeps come first, in the danger tone: a
 * person reads them before making a version current.
 */

export interface ChangePart {
  part: string;
  label: string;
  changes: string[];
}

export function Differences({ parts, removedRules, className, compact }: { parts: ChangePart[]; removedRules?: string[]; className?: string; compact?: boolean }) {
  if (!parts.length && !removedRules?.length) return null;
  return (
    <div className={cx('space-y-2', className)} data-recipe-differences>
      {removedRules?.length ? (
        <p className="flex gap-1.5 text-[12.5px] leading-relaxed text-danger" data-removed-rules>
          <WarningCircle size={14} weight="fill" className="mt-[2px] shrink-0" aria-hidden />
          <span>
            It no longer keeps {removedRules.length === 1 ? 'this rule' : 'these rules'}: {removedRules.map((x) => `“${x}”`).join(', ')}.
          </span>
        </p>
      ) : null}
      {parts.map((p) => (
        <section key={p.part} aria-label={p.label} data-difference-part={p.part}>
          <h4 className={cx('font-medium text-subtle', compact ? 'text-[11.5px]' : 'text-[12px]')}>{p.label}</h4>
          <ul className={cx('mt-0.5 space-y-0.5', compact ? 'text-[12px]' : 'text-[12.5px]')}>
            {p.changes.map((c, i) => (
              <li key={i} className="flex gap-2 leading-relaxed text-fg">
                <span className="mt-[8px] h-1 w-1 shrink-0 rounded-full bg-accent" aria-hidden />
                <span className="min-w-0 break-words">{c}</span>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}
