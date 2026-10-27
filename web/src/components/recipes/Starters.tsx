import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowRight, CookingPot, Plus } from '@phosphor-icons/react';
import type { ops } from '@metarush/schema';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import { useToast } from '../../ui/Toast';
import { Badge, Button, cx, Details } from '../../ui/primitives';
import { RECIPE_QUERIES, recipesLink } from './api';

/*
 * The starter recipes MetaRush comes with (D293): "a call becomes an episode
 * with shorts" and "a release tag becomes a release video". Those this
 * workspace doesn't have yet are offered on the Library's Recipes tab and on
 * Home's Start from a recipe: "Add the starter recipes" brings them all in,
 * "Use this one" brings one in and opens it. Each comes in as a draft — never
 * current by itself — that a person looks at and makes current (Make
 * current… on its page) before it runs.
 */

type StarterRow = ops.RecipeStarterRow;

export function useStarters() {
  return useOp('recipe.starters', {}, { staleTime: 30_000 });
}

/** Adds starters (all, or one) as drafts; opens the one added when it was one. */
export function useAddStarters() {
  const qc = useQueryClient();
  const toast = useToast();
  const navigate = useNavigate();
  const [busy, setBusy] = useState<string | null>(null);
  const add = async (only?: string) => {
    setBusy(only ?? 'all');
    try {
      const out = await call('recipe.starters.add', only ? { only: [only] } : {});
      for (const k of [...RECIPE_QUERIES, 'recipe.starters' as const]) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: out.message, body: out.next || undefined, durationMs: 7000 });
      const one = only ? out.added.find((a) => a.name === only) : undefined;
      if (one?.recipe) navigate(recipesLink({ recipe: one.recipe.name }));
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'The starter recipes didn’t come in', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(null);
    }
  };
  return { add, busy };
}

/** The starters not added yet, as cards with Use this one and Add the starter recipes. */
export function StarterRecipes({ compact, className }: { compact?: boolean; className?: string }) {
  const q = useStarters();
  const { add, busy } = useAddStarters();
  const missing = (q.data?.starters ?? []).filter((s) => s.state !== 'here');
  if (!missing.length) return null;
  return (
    <section aria-labelledby="starters-h" className={cx('rounded-[16px] bg-raised p-4 hairline', className)} data-starter-recipes>
      <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-2">
        <h3 id="starters-h" className="min-w-0 flex-1 text-[14.5px] font-semibold text-fg">
          Starter recipes
          <span className="ml-2 text-[12.5px] font-normal text-muted">Ready-made ways to start; each comes in as a draft you make current.</span>
        </h3>
        {missing.length > 1 ? (
          <Button size="sm" variant="primary" icon={<Plus size={12} weight="bold" />} loading={busy === 'all'} disabled={!!busy} onClick={() => void add()} data-add-starters>
            Add the starter recipes
          </Button>
        ) : null}
      </div>
      <ul className={cx('grid grid-cols-1 gap-3', !compact && 'md:grid-cols-2')}>
        {missing.map((s) => (
          <li key={s.name} className="min-w-0">
            <StarterCard s={s} busy={busy} onUse={() => void add(s.name)} compact={compact} />
          </li>
        ))}
      </ul>
    </section>
  );
}

function StarterCard({ s, busy, onUse, compact }: { s: StarterRow; busy: string | null; onUse: () => void; compact?: boolean }) {
  return (
    <article className="flex h-full min-w-0 flex-col rounded-[12px] bg-panel p-3 hairline" data-starter={s.name} data-starter-state={s.state}>
      <div className="flex min-w-0 items-start gap-2.5">
        <CookingPot size={18} className="mt-[1px] shrink-0 text-accent-text" aria-hidden />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h4 className="text-[13.5px] font-semibold text-fg">{s.title}</h4>
            {s.state === 'newer' ? <Badge tone="info">A newer version</Badge> : null}
          </div>
          <p className="mt-0.5 text-[12px] text-muted">Makes {s.makes}.</p>
          {!compact ? <p className="mt-1.5 text-[12.5px] leading-relaxed text-fg">{s.summary}</p> : null}
          <p className="mt-1.5 text-[12px] text-muted">A run gives it {s.gives.join(', ')}.</p>
          {!compact && s.uses.length ? (
            <Details compact label="What it uses" className="mt-1.5">
              <ul className="list-disc pl-5 text-[12px] text-muted" data-starter-uses>
                {s.uses.map((u) => (
                  <li key={u}>{u}</li>
                ))}
              </ul>
            </Details>
          ) : null}
        </div>
      </div>
      <div className="mt-3 flex justify-end">
        <Button size="sm" variant="secondary" icon={<ArrowRight size={12} />} loading={busy === s.name} disabled={!!busy} onClick={onUse} data-use-starter={s.name}>
          {s.state === 'newer' ? 'Add the newer version' : 'Use this one'}
        </Button>
      </div>
    </article>
  );
}
