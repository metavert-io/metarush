import type { ops } from '@metarush/schema';
import type { RemoteOp } from '../../api/types';

/*
 * Recipes (D201–D208): videos that worked, written down so they can be made
 * again. The types come from @metarush/schema (generated from the Go structs);
 * this names them for the dashboard's Recipes tab, its links and the queries a
 * change makes stale. The word "recipe" is the feature's own (D210), so these
 * live with the feature's files.
 */

export type RecipeRow = ops.RecipeRow;
export type RecipeInputRow = ops.RecipeInputRow;
export type RecipeRunBrief = ops.RecipeRunBrief;
export type RecipeRunView = ops.RecipeRunView;
export type RecipeRunInput = ops.RecipeRunInput;
export type RecipeStep = ops.RecipeStep;
export type RecipeShowOut = ops.RecipeShowOut;
export type RecipeVersionRow = ops.RecipeVersionRow;
export type RecipeSectionRow = ops.RecipeSectionRow;
export type RecipePlanOut = ops.RecipePlanOut;
export type RecipeInputValue = ops.RecipeInputValue;
export type RecipeSaveIn = ops.RecipeSaveIn;
export type RecipeSaveOut = ops.RecipeSaveOut;
export type RecipeProposalItem = ops.RecipeProposalItem;
export type RecipeCompareOut = ops.RecipeCompareOut;
export type RecipeRunOut = ops.RecipeRunOut;
// What changed between two versions (D212), two runs' review copies side by
// side, and what a card about a recipe shows in Claude's panel (D211).
export type RecipeDiffOut = ops.RecipeDiffOut;
export type RecipeChangePart = ops.RecipeChangePart;
export type RecipeCopyView = ops.RecipeCopyView;
export type RecipeCardView = ops.RecipeCardView;
// What people kept changing in runs (D217) and planning by itself (D218).
export type RecipeSuggestion = ops.RecipeSuggestion;
export type RecipeSuggestionsOut = ops.RecipeSuggestionsOut;
export type RecipeTriggerRow = ops.RecipeTriggerRow;
// Recipe files between workspaces (D219): what an import brings and waits for.
export type RecipeImportOut = ops.RecipeImportOut;
export type RecipeImportView = ops.RecipeImportView;
export type RecipeImportCodeView = ops.RecipeImportCodeView;

/** What a change to recipes or their runs makes stale (the live events do the same, api/events.ts). */
export const RECIPE_QUERIES: RemoteOp[] = ['recipe.list', 'recipe.show', 'recipe.runs', 'recipe.diff', 'recipe.compare', 'recipe.suggestions', 'recipe.triggers', 'activity.summary', 'todos.list', 'todos.counts', 'notification.list'];

/** The Library's Recipes tab: the gallery, one recipe (and one of its versions), one run, or two runs side by side. */
export function recipesLink(o: { recipe?: string; version?: number; run?: string; compare?: [string, string] } = {}): string {
  const p = new URLSearchParams();
  if (o.compare) {
    p.set('compare', o.compare[0]);
    p.set('with', o.compare[1]);
  } else if (o.run) p.set('run', o.run);
  else if (o.recipe) {
    p.set('recipe', o.recipe);
    if (o.version) p.set('version', String(o.version));
  }
  const s = p.toString();
  return `/library/recipes${s ? `?${s}` : ''}`;
}

/** A page of this dashboard the server named by its full address: the same page here (so it opens in this tab). */
export function inApp(url: string | undefined): string | undefined {
  if (!url) return undefined;
  try {
    const u = new URL(url, window.location.origin);
    return `${u.pathname}${u.search}${u.hash}`;
  } catch {
    return undefined;
  }
}

/** Where a run's video is: its project, the version it made and its review copy. */
export function runLinks(r: { projectId?: string; revision?: string; render?: string; projectGone?: boolean }) {
  if (!r.projectId || r.projectGone) return {};
  return {
    project: `/p/${r.projectId}`,
    version: r.revision ? `/p/${r.projectId}/r/${r.revision}` : undefined,
    review: r.render ? `/p/${r.projectId}?tab=renders&render=${encodeURIComponent(r.render)}` : undefined,
  };
}

/** A run's state as a tone: going, waiting, ready or ended badly. */
export function runTone(state: string | undefined, waitingFor?: string): 'accent' | 'info' | 'ok' | 'danger' | 'neutral' {
  switch (state) {
    case 'running':
      return 'accent';
    case 'waiting':
      return waitingFor === 'claude' ? 'info' : 'accent';
    case 'done':
      return 'ok';
    case 'failed':
      return 'danger';
    default:
      return 'neutral';
  }
}

/** A run's state in a few words, for a badge (what a waiting run waits for is said in full where it is shown). */
export function stateShort(r: { state: string; stateLabel: string; waitingFor?: string }): string {
  if (r.state === 'waiting') return r.waitingFor === 'claude' ? 'Waiting for Claude' : 'Waiting for you';
  return r.stateLabel;
}

/** Who does a step, as people read it. */
export const WHO: Record<RecipeStep['who'], string> = { metarush: 'MetaRush', claude: 'Claude', person: 'You' };

/** The value an input has unless a run gives another: words or things in MetaRush. */
export function inputValue(row: RecipeInputRow): RecipeInputValue {
  return { name: row.name, ...(row.value.text ? { text: row.value.text } : {}), ...(row.value.items?.length ? { items: row.value.items } : {}) };
}
