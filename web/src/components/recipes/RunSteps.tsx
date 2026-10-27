import { CheckCircle, Circle, Hourglass, Sparkle, User, WarningCircle, XCircle } from '@phosphor-icons/react';
import { money } from '../../lib/costs';
import { cx, Spinner } from '../../ui/primitives';
import { WHO, type RecipeStep } from './api';

/*
 * A run's steps (D204), from its plan or as it goes: what each does, who does
 * it — MetaRush, Claude or you — and where it stands, with what it did or
 * waits for in plain words. The words come from the server.
 */

function WhoChip({ who }: { who: RecipeStep['who'] }) {
  const icon = who === 'claude' ? <Sparkle size={11} weight="fill" /> : who === 'person' ? <User size={11} weight="bold" /> : null;
  return (
    <span
      className={cx(
        'inline-flex h-[20px] shrink-0 items-center gap-1 rounded-full px-1.5 text-[11px] font-medium',
        who === 'claude' ? 'bg-accent-soft text-accent-text' : who === 'person' ? 'bg-info-soft text-info' : 'bg-hover text-muted',
      )}
      data-step-who={who}
    >
      {icon}
      {WHO[who]}
    </span>
  );
}

function StateIcon({ s }: { s: RecipeStep }) {
  switch (s.state) {
    case 'done':
      return <CheckCircle size={17} weight="fill" className="text-ok" aria-label="Done" />;
    case 'running':
      return <Spinner size={14} className="text-accent-text" label="Going on" />;
    case 'waiting':
      return <Hourglass size={17} weight="fill" className="text-accent-text" aria-label="Waiting" />;
    case 'failed':
      return <XCircle size={17} weight="fill" className="text-danger" aria-label="Didn't finish" />;
    case 'skipped':
      return <Circle size={17} className="text-subtle" aria-label="Not needed" />;
    default:
      return <Circle size={17} className="text-line-strong" aria-label="Not started" />;
  }
}

/**
 * The steps as a list. `planned` shows a plan (nothing started: no states);
 * otherwise each step's state, what it did and what it spent.
 */
export function RunSteps({ steps, planned, className }: { steps: RecipeStep[]; planned?: boolean; className?: string }) {
  return (
    <ol className={cx('space-y-1', className)} aria-label={planned ? 'What the run does' : 'Steps'} data-run-steps>
      {steps.map((s, i) => {
        const spent = s.spent?.usd ? money(s.spent.usd) : '';
        return (
          <li
            key={s.id}
            className={cx('grid grid-cols-[22px_minmax(0,1fr)] gap-x-2.5 rounded-[10px] px-2 py-1.5', !planned && s.state === 'waiting' && 'bg-accent-soft/60', !planned && s.state === 'failed' && 'bg-danger-soft/60')}
            data-step={s.id}
            data-step-state={planned ? 'planned' : s.state}
          >
            <span className="mt-[1px] grid h-[20px] place-items-center">
              {planned ? <span className="tc text-[11.5px] font-medium text-subtle">{i + 1}</span> : <StateIcon s={s} />}
            </span>
            <span className="min-w-0">
              <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                <span className={cx('text-[13px] font-medium', !planned && s.state === 'skipped' ? 'text-muted' : 'text-fg')}>{s.label}</span>
                <WhoChip who={s.who} />
                {planned && s.estimate ? <span className="tc text-[11.5px] text-muted">{money(s.estimate)}</span> : null}
                {!planned && spent ? <span className="tc text-[11.5px] text-muted">spent {spent}</span> : null}
              </span>
              {s.detail ? (
                <span className={cx('mt-0.5 block text-[12px] leading-relaxed', !planned && s.state === 'failed' ? 'text-danger' : 'text-muted')}>
                  {!planned && s.state === 'failed' ? <WarningCircle size={12} weight="fill" className="mr-1 inline align-[-1px]" aria-hidden /> : null}
                  {s.detail}
                </span>
              ) : null}
            </span>
          </li>
        );
      })}
    </ol>
  );
}

/** "1 step for Claude, 1 for you": who besides MetaRush the run needs. */
export function whoElse(steps: RecipeStep[]): string {
  const claude = steps.filter((s) => s.who === 'claude').length;
  const you = steps.filter((s) => s.who === 'person').length;
  const parts: string[] = [];
  if (claude) parts.push(claude === 1 ? 'one step is Claude’s' : `${claude} steps are Claude’s`);
  if (you) parts.push(you === 1 ? 'one is yours' : `${you} are yours`);
  return parts.join(', ');
}
