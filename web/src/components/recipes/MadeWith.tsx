import { Link } from 'react-router';
import { ArrowsClockwise, CookingPot } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import type { RevisionSummary } from '../../api/types';
import { Button, cx, Theirs } from '../../ui/primitives';
import { recipesLink, type RecipeRunBrief } from './api';
import { useRunDialog } from './RunDialog';

/*
 * "Made with the “App teaser” recipe, run 4" (D203): a run's project and the
 * versions it made say so, with a link to the run and Run again… (a new
 * version of the same cut, the plan saying what changes). Versions a run made
 * carry its origin (recipe:<run>) or were saved in the run's name.
 */

/** The run that made a version of a project's cut, if one did. */
export function runFor(runs: RecipeRunBrief[], rev: Pick<RevisionSummary, 'branch' | 'number' | 'origin' | 'createdBy'> | undefined): RecipeRunBrief | undefined {
  if (!rev) return undefined;
  const id = rev.origin?.startsWith('recipe:') ? rev.origin.slice('recipe:'.length) : '';
  if (id) {
    const r = runs.find((x) => x.id === id);
    if (r) return r;
  }
  if (rev.createdBy?.via !== 'recipe') return undefined; // saved by a run of Recipes (its AI voice, its clearer voices)
  // The run on the same cut whose newest version is the first at or after this one.
  return runs
    .filter((r) => r.branch === rev.branch && (r.version ?? 0) >= rev.number)
    .sort((a, b) => (a.version ?? 0) - (b.version ?? 0))[0];
}

/** The newest run that made a project's cut (its project header). */
export function runForCut(runs: RecipeRunBrief[], branch: string | undefined): RecipeRunBrief | undefined {
  if (!branch) return undefined;
  return runs.filter((r) => r.branch === branch && r.version).sort((a, b) => b.number - a.number)[0];
}

export function MadeWith({ projectId, revision, cut, className }: {
  projectId: string;
  /** A version: the run that made it. */
  revision?: Pick<RevisionSummary, 'branch' | 'number' | 'origin' | 'createdBy'>;
  /** A cut (its name in the project): the newest run that made it. */
  cut?: string;
  className?: string;
}) {
  const runs = useOp('recipe.runs', { projectId, limit: 50 }, { staleTime: 30_000 });
  const again = useRunDialog();
  const list = runs.data?.runs ?? [];
  const run = revision ? runFor(list, revision) : runForCut(list, cut);
  if (!run) return null;
  return (
    <div className={cx('flex flex-wrap items-center gap-x-2 gap-y-1.5 text-[12.5px] text-muted', className)} data-made-with={run.ref}>
      <CookingPot size={14} className="shrink-0 text-accent-text" aria-hidden />
      <span>
        Made with the{' '}
        <Link to={recipesLink({ recipe: run.recipe })} className="font-medium text-fg hover:underline">
          “<Theirs>{run.title}</Theirs>”
        </Link>{' '}
        recipe,{' '}
        <Link to={recipesLink({ run: run.id })} className="font-medium text-accent-text hover:underline" data-made-with-run>
          run {run.number}
        </Link>
      </span>
      {run.state !== 'running' && run.state !== 'waiting' && run.branch ? (
        <Button size="sm" variant="ghost" icon={<ArrowsClockwise size={12} />} onClick={() => void again({ again: run.id, title: run.title, number: run.number })} data-made-with-again>
          Run again…
        </Button>
      ) : null}
    </div>
  );
}
