import type { ReactNode } from 'react';
import { Link, useSearchParams } from 'react-router';
import { CookingPot, DownloadSimple, FilmSlate, Image as ImageIcon, ListBullets, Microphone, Play, TextAa } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import { money } from '../../lib/costs';
import { absolute, plural, stamp } from '../../lib/format';
import { Badge, Button, cx, EmptyState, ErrorPanel, Freshness, Skeleton, Theirs, Thumb } from '../../ui/primitives';
import { recipesLink, runTone, stateShort, type RecipeInputRow, type RecipeRow, type RecipeRunBrief } from './api';
import { exportRecipeFile, ImportButton } from './Files';
import { StarterRecipes } from './Starters';
import { useRunDialog } from './RunDialog';

/*
 * The Library's Recipes tab (D208): a card per recipe with its example (a
 * still of the render it was saved from), what it makes and where runs go,
 * what changes each run, its last runs (when, who, where, whether a person
 * approved what it made, what it spent) and Run with…. The starter recipes
 * this workspace doesn't have yet are offered above them (D293).
 */

export const KIND_ICON: Record<RecipeInputRow['kind'], ReactNode> = {
  words: <TextAa size={12} />,
  choice: <ListBullets size={12} />,
  voice: <Microphone size={12} />,
  footage: <FilmSlate size={12} />,
  pictures: <ImageIcon size={12} />,
};

export function RecipesGallery() {
  const [params, setParams] = useSearchParams();
  const archived = params.get('archived') === '1';
  const list = useOp('recipe.list', archived ? { archived: true } : {});
  const rows = list.data?.recipes ?? [];
  return (
    <section aria-label="Recipes" data-recipes-gallery>
      <div className="mb-4 flex flex-wrap items-center gap-x-3 gap-y-2">
        <p className="min-w-0 flex-1 text-[13px] text-muted">{list.data ? (rows.length ? list.data.summary : '') : 'Loading the recipes…'}</p>
        <label className="inline-flex items-center gap-1.5 text-[12.5px] text-muted">
          <input
            type="checkbox"
            checked={archived}
            onChange={(e) => {
              const next = new URLSearchParams(params);
              if (e.target.checked) next.set('archived', '1');
              else next.delete('archived');
              setParams(next, { replace: true });
            }}
            className="h-3.5 w-3.5 accent-[var(--accent)]"
          />
          Archived ones too
        </label>
        <ImportButton />
        <Freshness at={list.dataUpdatedAt || undefined} fetching={list.isFetching} onRefresh={() => void list.refetch()} className="max-sm:hidden" />
      </div>
      {!archived ? <StarterRecipes className="mb-5" /> : null}
      {list.isLoading ? (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-[380px] rounded-[16px]" />
          ))}
        </div>
      ) : list.error ? (
        <ErrorPanel error={list.error} onRetry={() => void list.refetch()} />
      ) : rows.length === 0 ? (
        <div className="panel">
          <EmptyState
            icon={<CookingPot size={22} />}
            title="No recipes yet"
            body="A recipe is a video that worked, written down so it can be made again with new art, a new recording or a new headline. Start from a starter recipe above, open a project whose video worked and choose “Save as a recipe…” in its “…” menu, ask Claude to save one, or bring in a recipe another workspace exported (Import a recipe…)."
          />
        </div>
      ) : (
        <ul className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3" aria-label="Recipes">
          {rows.map((r) => (
            <li key={r.id} className="min-w-0">
              <RecipeCard r={r} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function RecipeCard({ r }: { r: RecipeRow }) {
  const run = useRunDialog();
  const inputs = r.inputs;
  const canRun = !r.archived && r.current > 0;
  return (
    <article className="fade-in flex h-full min-w-0 flex-col overflow-hidden rounded-[16px] bg-raised hairline transition-shadow hover:shadow-[var(--shadow-soft)]" data-recipe-card={r.name}>
      <Link to={recipesLink({ recipe: r.name })} className="group relative block" aria-label={`Open “${r.title}”`} tabIndex={-1}>
        <Thumb
          src={r.example?.thumbUrl}
          alt=""
          className="aspect-[16/9] w-full"
          fallback={
            <span className="flex flex-col items-center gap-1.5 text-[12px] text-subtle">
              <CookingPot size={24} />
              No example yet
            </span>
          }
        />
        {r.example ? (
          <span className="absolute bottom-2 left-2 inline-flex items-center gap-1 rounded-full bg-black/55 px-2 py-0.5 text-[11px] font-medium text-white backdrop-blur">
            <Play size={10} weight="fill" /> Example
          </span>
        ) : null}
      </Link>
      <div className="flex min-w-0 flex-1 flex-col p-4">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <h3 className="min-w-0 text-[16px] font-semibold tracking-[-0.01em] text-fg">
            <Link to={recipesLink({ recipe: r.name })} className="hover:underline" data-recipe-open={r.name}>
              <Theirs>{r.title}</Theirs>
            </Link>
          </h3>
          {r.archived ? <Badge>Archived</Badge> : null}
          {r.current === 0 ? <Badge tone="info">Draft only</Badge> : r.draft ? <Badge tone="info">A draft waits</Badge> : null}
          {r.planned ? (
            <Badge tone="accent" title="It planned a run by itself: it waits for you to run it">
              A run is planned
            </Badge>
          ) : r.plansByItself ? (
            <Badge title="It plans a run by itself and tells you; it never starts one">Plans by itself</Badge>
          ) : null}
        </div>
        <p className="mt-1 text-[12.5px] leading-relaxed text-muted">
          {[r.makes ? `Makes ${r.makes}` : '', r.lands].filter(Boolean).join(', ')}.
        </p>
        {inputs.length ? (
          <div className="mt-3">
            <div className="mb-1 text-[11.5px] font-medium text-subtle">Changes each run</div>
            <ul className="flex flex-wrap gap-1" aria-label="What changes each run">
              {inputs.slice(0, 5).map((i) => (
                <li key={i.name} className="inline-flex max-w-full items-center gap-1 truncate rounded-full bg-hover px-2 py-[2px] text-[11.5px] text-muted" title={i.default ? `${i.label}: now ${i.default}` : i.label}>
                  <span className="shrink-0 text-subtle">{KIND_ICON[i.kind]}</span>
                  <span className="truncate">{i.label}</span>
                </li>
              ))}
              {inputs.length > 5 ? <li className="text-[11.5px] text-subtle">and {inputs.length - 5} more</li> : null}
            </ul>
          </div>
        ) : null}
        <div className="mt-3 flex-1">
          <div className="mb-1 text-[11.5px] font-medium text-subtle">{r.runs ? `Last runs (${r.runs} in all)` : 'No runs yet'}</div>
          {r.lastRuns.length ? (
            <ul className="space-y-1">
              {r.lastRuns.map((x) => (
                <li key={x.id}>
                  <RunLine run={x} />
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-[12px] text-subtle">Run it with new inputs: you see what it makes and what it costs first.</p>
          )}
        </div>
        <div className="mt-4 flex flex-wrap items-center gap-2">
          <Button
            variant="primary"
            size="sm"
            icon={<Play size={12} weight="fill" />}
            disabled={!canRun}
            title={canRun ? undefined : r.archived ? 'Restore it to run it' : 'A person makes a version current first'}
            onClick={() => void run({ recipe: r.name, title: r.title })}
            data-run-with={r.name}
          >
            Run with…
          </Button>
          <Link to={recipesLink({ recipe: r.name })} className="inline-flex h-7 items-center rounded-[8px] px-2.5 text-[12.5px] font-medium text-muted hover:bg-hover hover:text-fg">
            Versions and runs
          </Link>
          <Button
            variant="ghost"
            size="sm"
            icon={<DownloadSimple size={12} />}
            className="ml-auto"
            title="A file another workspace can bring in: the recipe, its pictures and footage (its code waits for a person's approval there)"
            onClick={() => exportRecipeFile(r.name)}
            data-recipe-export={r.name}
          >
            Export
          </Button>
        </div>
      </div>
    </article>
  );
}

/** One run in a list: its number, state, who and when, where and what it spent. */
export function RunLine({ run: x, className }: { run: RecipeRunBrief; className?: string }) {
  const tone = runTone(x.state, x.waitingFor);
  return (
    <Link to={recipesLink({ run: x.id })} className={cx('flex min-w-0 items-center gap-2 rounded-[8px] px-1.5 py-1 text-[12px] hover:bg-hover', className)} data-run-line={x.number}>
      <span
        className={cx(
          'h-2 w-2 shrink-0 rounded-full',
          tone === 'ok' ? 'bg-ok' : tone === 'danger' ? 'bg-danger' : tone === 'accent' ? 'bg-accent' : tone === 'info' ? 'bg-info' : 'bg-subtle',
        )}
        aria-hidden
      />
      <span className="shrink-0 font-medium text-fg">Run {x.number}</span>
      <span className="min-w-0 truncate text-muted" title={`${x.stateLabel}; ${x.by}, ${absolute(x.at)}`}>
        {stateShort(x)}
        {x.approved ? ', approved' : ''} · {x.by} · {stamp(x.at)}
      </span>
      <span className="ml-auto shrink-0 text-[11.5px] text-subtle" title={x.spentWords === 'nothing' ? undefined : `Spent ${x.spentWords}`}>
        {x.spent.usd && x.spent.usd > 0 ? money(x.spent.usd) : ''}
      </span>
    </Link>
  );
}

/** "3 runs, 1 approved". */
export function runsWords(runs: RecipeRunBrief[]): string {
  const approved = runs.filter((r) => r.approved).length;
  return `${plural(runs.length, 'run')}${approved ? `, ${approved} approved` : ''}`;
}
