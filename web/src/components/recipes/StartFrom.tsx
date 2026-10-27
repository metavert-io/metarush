import { Link } from 'react-router';
import { ArrowRight, CookingPot, Play } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import { Button, ErrorPanel, SectionTitle, Skeleton, Theirs, Thumb } from '../../ui/primitives';
import { recipesLink } from './api';
import { useRunDialog } from './RunDialog';
import { StarterRecipes } from './Starters';

/*
 * "Start from a recipe" on Studio's Home (D212): the recipes that can run —
 * a still of what each makes, what it makes and where its runs go — each with
 * Run with… (the run dialog: the inputs, the plan and its estimate, then Run);
 * all of them are on the Library's Recipes tab. With none yet, one line says
 * how to save one, and the starter recipes not added yet are offered (D293).
 */

/** How many recipes Home shows (the newest first). */
const SHOWN = 4;

export function StartFromRecipe({ className }: { className?: string }) {
  const list = useOp('recipe.list', {}, { staleTime: 30_000 });
  const run = useRunDialog();
  const rows = (list.data?.recipes ?? []).filter((r) => !r.archived);
  const runnable = rows.filter((r) => r.current > 0);
  return (
    <section aria-labelledby="start-recipe-h" className={className} data-start-from-recipe>
      <SectionTitle
        id="start-recipe-h"
        meta={runnable.length ? `${runnable.length} ready to run` : undefined}
        action={
          rows.length ? (
            <Link to={recipesLink()} className="inline-flex items-center gap-1 text-[12.5px] text-accent-text hover:underline" data-all-recipes>
              All recipes <ArrowRight size={12} />
            </Link>
          ) : null
        }
      >
        Start from a recipe
      </SectionTitle>
      {list.isLoading ? (
        <Skeleton className="h-20 rounded-[14px]" />
      ) : list.error ? (
        <ErrorPanel compact error={list.error} onRetry={() => void list.refetch()} />
      ) : runnable.length === 0 ? (
        <>
          <div className="flex gap-2.5 rounded-[14px] px-4 py-4 text-[13px] leading-relaxed text-muted hairline" data-start-from-recipe-empty>
            <CookingPot size={17} className="mt-[2px] shrink-0 text-subtle" aria-hidden />
            <span>
              {rows.length
                ? 'A recipe waits for a person to make a version current before it runs. '
                : 'A recipe is a video that worked, written down to make again with new footage, pictures or words. Save one from a project’s “…” menu. '}
              <Link to={recipesLink()} className="font-medium text-accent-text hover:underline">
                Recipes in the Library
              </Link>
            </span>
          </div>
          <StarterRecipes compact className="mt-2" />
        </>
      ) : (
        <ul className="space-y-1.5" aria-label="Recipes you can run">
          {runnable.slice(0, SHOWN).map((r) => (
            <li key={r.id} className="flex items-center gap-3 rounded-[14px] p-2 hairline" data-start-recipe={r.name}>
              <Link to={recipesLink({ recipe: r.name })} className="shrink-0" tabIndex={-1} aria-hidden>
                <Thumb src={r.example?.thumbUrl} alt="" className="h-12 w-[72px] rounded-[9px]" fallback={<CookingPot size={15} />} />
              </Link>
              <div className="min-w-0 flex-1">
                <Link to={recipesLink({ recipe: r.name })} className="block truncate text-[13px] font-medium text-fg hover:underline">
                  <Theirs>{r.title}</Theirs>
                </Link>
                <div className="truncate text-[11.5px] text-subtle" title={[r.makes, r.lands].filter(Boolean).join(', ')}>
                  {[r.makes, r.lands ? `each run makes ${r.lands}` : ''].filter(Boolean).join('; ')}
                </div>
              </div>
              <Button size="sm" variant="secondary" icon={<Play size={12} weight="fill" />} onClick={() => void run({ recipe: r.name, title: r.title })} data-start-run-with={r.name}>
                Run with…
              </Button>
            </li>
          ))}
          {runnable.length > SHOWN ? (
            <li>
              <Link to={recipesLink()} className="block rounded-[12px] px-3 py-2 text-[12.5px] text-accent-text hover:bg-hover">
                {runnable.length - SHOWN} more in the Library
              </Link>
            </li>
          ) : null}
        </ul>
      )}
    </section>
  );
}
