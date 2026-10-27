import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { Lightbulb } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import { useToast } from '../../ui/Toast';
import { Button, ErrorPanel, Skeleton, cx } from '../../ui/primitives';
import { RECIPE_QUERIES, recipesLink, type RecipeSuggestion } from './api';
import { Differences } from './Differences';

/*
 * Suggested changes (D217): what people kept changing in this recipe's runs —
 * each approved run's version compared with the one the run made — each with
 * why ("You shortened “intro” in 4 of 5 runs"), and what a draft with them
 * would change. Saving makes a draft (Claude may too, from a card); only a
 * person makes it current, as always (Make current… on the draft).
 */

/** "runs 2, 4 and 5" from refs like app-teaser#2. */
function runsWords(refs: string[]): string {
  const ns = refs.map((r) => r.split('#').pop() ?? r);
  if (ns.length === 1) return `run ${ns[0]}`;
  return `runs ${ns.slice(0, -1).join(', ')} and ${ns[ns.length - 1]}`;
}

export function Suggestions({ name }: { name: string }) {
  const [left, setLeft] = useState<string[]>([]); // suggestions left out of the draft
  const [saving, setSaving] = useState(false);
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const all = useOp('recipe.suggestions', { recipe: name }, { staleTime: 30_000 });
  const keys = all.data?.suggestions.map((s) => s.key) ?? [];
  const only = keys.filter((k) => !left.includes(k));
  const some = left.length > 0 && only.length > 0;
  // What the draft changes without the ones left out.
  const partial = useOp('recipe.suggestions', some ? { recipe: name, only } : null, { staleTime: 30_000 });
  if (all.isLoading) return <Skeleton className="h-24 rounded-[14px]" />;
  if (all.error || !all.data) return <ErrorPanel compact error={all.error} onRetry={() => void all.refetch()} />;
  const d = all.data;
  const view = some ? partial.data : d;
  const none = only.length === 0;
  const save = async () => {
    setSaving(true);
    try {
      const out = await call('recipe.suggestions.save', { recipe: name, ...(some ? { only } : {}) });
      for (const k of [...RECIPE_QUERIES, 'recipe.suggestions' as const]) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: out.message });
      navigate(recipesLink({ recipe: name, version: out.version.number }));
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'The draft wasn’t saved', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setSaving(false);
    }
  };
  return (
    <section aria-labelledby="rcp-suggestions" data-recipe-suggestions data-suggestions={d.suggestions.length}>
      <h3 id="rcp-suggestions" className="mb-1 flex items-center gap-1.5 text-[15px] font-semibold text-fg">
        <Lightbulb size={16} className="text-accent-text" aria-hidden /> Suggested changes
      </h3>
      <p className="mb-2 max-w-[80ch] text-[12.5px] leading-relaxed text-muted" data-suggestions-summary>
        {d.summary}
      </p>
      {d.suggestions.length ? (
        <div className="space-y-3 rounded-[14px] bg-raised px-4 py-3 hairline">
          <ul className="space-y-1.5" aria-label="What people kept changing">
            {d.suggestions.map((s) => (
              <SuggestionRow key={s.key} s={s} on={!left.includes(s.key)} onChange={(v) => setLeft((l) => (v ? l.filter((k) => k !== s.key) : [...l, s.key]))} disabled={saving} />
            ))}
          </ul>
          {none ? (
            <p className="text-[12.5px] text-muted">Tick at least one to make a draft.</p>
          ) : !view ? (
            <Skeleton className="h-16" />
          ) : view.changes.length ? (
            <div className="rounded-[10px] bg-panel px-3 py-2.5 hairline" data-suggestions-draft>
              <p className="mb-1.5 text-[12.5px] text-muted">A draft from version {view.base?.number} would change:</p>
              <Differences parts={view.changes} compact />
              {view.check && !view.check.ok ? (
                <p className="mt-1.5 text-[12px] text-danger">Its check finds something to fix: {view.check.problems?.[0]?.message}</p>
              ) : null}
            </div>
          ) : null}
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" variant="secondary" loading={saving} disabled={none || !view} onClick={() => void save()} data-save-suggestions>
              Save as a draft
            </Button>
            <span className="text-[12px] text-subtle">It is only a suggestion: a draft, which a person makes current.</span>
          </div>
        </div>
      ) : null}
    </section>
  );
}

function SuggestionRow({ s, on, onChange, disabled }: { s: RecipeSuggestion; on: boolean; onChange: (on: boolean) => void; disabled: boolean }) {
  return (
    <li data-suggestion={s.key} data-in-draft={on ? 'yes' : 'no'}>
      <label className={cx('flex items-start gap-2.5 text-[13px]', disabled ? 'cursor-not-allowed' : 'cursor-pointer')}>
        <input type="checkbox" checked={on} disabled={disabled} onChange={(e) => onChange(e.target.checked)} className="mt-[3px] h-4 w-4 shrink-0 accent-[var(--accent)]" />
        <span className="min-w-0">
          <span className="block leading-relaxed text-fg">{s.reason}</span>
          <span className="block text-[11.5px] text-subtle">Seen in {runsWords(s.runs)}</span>
        </span>
      </label>
    </li>
  );
}
