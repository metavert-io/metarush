import { useSearchParams } from 'react-router';
import { RecipesGallery } from '../components/recipes/Gallery';
import { RecipePage } from '../components/recipes/RecipePage';
import { RunPage } from '../components/recipes/RunPage';
import { SideBySide } from '../components/recipes/SideBySide';
import { LibraryShell } from './Library';

/*
 * Library → Recipes (/library/recipes, D208): the gallery of recipes;
 * ?recipe=<name> (and &version=<n>) opens one (&planned=1: with Run with… on
 * the run it planned by itself, D218), ?run=<id> one of its runs,
 * ?compare=<run>&with=<run> two runs side by side (D212) (the links
 * internal/links makes: links.Recipe, links.RecipeRun, links.RecipeCompare).
 * The run dialog, "Save as a recipe…" in a project's menu and "Made with…" on
 * a run's project and versions live in components/recipes.
 */
export function RecipesPage() {
  const [params] = useSearchParams();
  const run = params.get('run');
  const recipe = params.get('recipe');
  const version = Number(params.get('version')) || undefined;
  const planned = params.get('planned') === '1';
  const compare = params.get('compare');
  const compareWith = params.get('with');
  return (
    <LibraryShell section="recipes">
      {compare && compareWith ? (
        <SideBySide key={`${compare}/${compareWith}`} left={compare} right={compareWith} />
      ) : run ? (
        <RunPage key={run} id={run} />
      ) : recipe ? (
        <RecipePage key={`${recipe}@${version ?? ''}`} name={recipe} version={version} planned={planned} />
      ) : (
        <RecipesGallery />
      )}
    </LibraryShell>
  );
}
