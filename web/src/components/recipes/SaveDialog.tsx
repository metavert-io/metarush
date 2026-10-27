import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowDown, ArrowUp, CheckCircle, CookingPot, Info, Lock, WarningCircle } from '@phosphor-icons/react';
import { call } from '../../api/client';
import type { MenuItem } from '../../layout/assistant/Menu';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Button, cx, ErrorPanel, Segmented, Skeleton, Spinner } from '../../ui/primitives';
import { RECIPE_QUERIES, recipesLink, type RecipeProposalItem, type RecipeSaveIn, type RecipeSaveOut } from './api';

/*
 * "Save as a recipe…" (D201, D203): MetaRush reads the project's approved
 * version (else its newest) and proposes what changes each run — the
 * project's own footage, words in graphics, pictures that aren't the brand's,
 * the AI voice — and what stays, each with why. The person moves items
 * between the two lists (re-proposed each time, nothing saved), says where
 * runs go, adds hints for Claude, and saves. A person's save is current at
 * once; the approved render becomes the recipe's example.
 */

/** The project's "…" menu item that opens the proposal. */
export function useSaveAsMenuItem(project: { id: string; name: string }, hasCut: boolean): MenuItem {
  const open = useSaveDialog();
  return {
    label: 'Save as a recipe…',
    icon: <CookingPot size={14} />,
    disabled: !hasCut,
    hint: hasCut ? 'Make this video again with new inputs' : 'It needs a cut first',
    onSelect: () => void open(project),
  };
}

export function useSaveDialog() {
  const dialog = useDialog();
  const navigate = useNavigate();
  const toast = useToast();
  const qc = useQueryClient();
  return async (project: { id: string; name: string }) => {
    const out = await dialog.open<RecipeSaveOut>((close) => <SaveForm project={project} onClose={close} />, { title: `Save “${project.name}” as a recipe`, size: 'lg' });
    if (!out?.recipe) return;
    for (const k of RECIPE_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
    const name = out.recipe.name;
    toast({ tone: 'success', title: out.message, actions: [{ label: 'Open it', onClick: () => navigate(recipesLink({ recipe: name })) }], durationMs: 9000 });
    navigate(recipesLink({ recipe: name }));
  };
}

interface Moves {
  vary: string[];
  keep: string[];
  fixed: string[];
  chosen: string[];
}

const NONE: Moves = { vary: [], keep: [], fixed: [], chosen: [] };

function SaveForm({ project, onClose }: { project: { id: string; name: string }; onClose: (out?: RecipeSaveOut) => void }) {
  const [moves, setMoves] = useState<Moves>(NONE);
  const [newProjects, setNewProjects] = useState(false);
  const [title, setTitle] = useState('');
  const [hints, setHints] = useState('');
  const [proposal, setProposal] = useState<RecipeSaveOut | null>(null);
  const [first, setFirst] = useState<RecipeSaveOut | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [proposing, setProposing] = useState(false);
  const [saving, setSaving] = useState(false);
  const seq = useRef(0);

  const input = useMemo((): RecipeSaveIn => {
    const hintList = hints
      .split('\n')
      .map((h) => h.trim())
      .filter(Boolean);
    return {
      from: project.id,
      ...(title.trim() ? { title: title.trim() } : {}),
      ...(newProjects ? { newProjects: true } : {}),
      ...(moves.vary.length ? { vary: moves.vary } : {}),
      ...(moves.keep.length ? { keep: moves.keep } : {}),
      ...(moves.fixed.length ? { fixed: moves.fixed } : {}),
      ...(moves.chosen.length ? { chosen: moves.chosen } : {}),
      ...(hintList.length ? { hints: hintList } : {}),
    };
  }, [project.id, title, newProjects, moves, hints]);

  // The proposal follows the choices (nothing is saved).
  useEffect(() => {
    const n = ++seq.current;
    const t = setTimeout(
      async () => {
        setProposing(true);
        try {
          const out = await call('recipe.propose', input);
          if (n !== seq.current) return;
          setProposal(out);
          setFirst((f) => f ?? out);
          setError(null);
        } catch (e) {
          if (n === seq.current) setError(e);
        } finally {
          if (n === seq.current) setProposing(false);
        }
      },
      proposal ? 350 : 0,
    );
    return () => clearTimeout(t);
  }, [input]); // eslint-disable-line react-hooks/exhaustive-deps

  const move = (it: RecipeProposalItem) =>
    setMoves((m) => {
      const without = (xs: string[]) => xs.filter((k) => k !== it.key);
      const next: Moves = { vary: without(m.vary), keep: without(m.keep), fixed: without(m.fixed), chosen: without(m.chosen) };
      // Back to what MetaRush proposed, or across.
      const proposed = first?.proposal.find((p) => p.key === it.key)?.varies ?? it.varies;
      const toVary = !it.varies;
      if (toVary !== proposed) {
        if (it.kind === 'section') (toVary ? next.chosen : next.fixed).push(it.key);
        else (toVary ? next.vary : next.keep).push(it.key);
      }
      return next;
    });

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const out = await call('recipe.save', input);
      onClose(out);
    } catch (e) {
      setError(e);
      setSaving(false);
    }
  };

  if (!proposal && error) return <ErrorPanel error={error} onRetry={() => setMoves({ ...moves })} />;
  if (!proposal) {
    return (
      <div className="space-y-3" aria-busy="true" aria-label="Reading the cut">
        <Skeleton className="h-10" />
        <Skeleton className="h-40" />
        <Skeleton className="h-28" />
      </div>
    );
  }
  const items = proposal.proposal;
  const varies = items.filter((i) => i.varies);
  const stays = items.filter((i) => !i.varies);
  const problems = proposal.check?.problems ?? [];
  const warnings = proposal.check?.warnings ?? [];
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
      data-save-dialog
    >
      <p className="-mt-1 mb-4 text-[13px] leading-relaxed text-muted">{proposal.message}</p>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end">
        <label className="block min-w-0">
          <span className="text-[12.5px] font-medium text-muted">Its name</span>
          <input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            maxLength={120}
            placeholder={first?.title || project.name}
            className="mt-1 h-9 w-full rounded-[10px] bg-panel px-3 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
            data-save-title
          />
        </label>
        <div>
          <span className="mb-1 block text-[12.5px] font-medium text-muted">Each run makes</span>
          <Segmented
            label="Where runs go"
            value={newProjects ? 'new' : 'here'}
            onChange={(v) => setNewProjects(v === 'new')}
            items={[
              { value: 'here', label: 'A new cut here' },
              { value: 'new', label: 'A new project' },
            ]}
          />
        </div>
      </div>
      <p className="mt-1.5 text-[12px] text-subtle">
        {proposal.makes ? `It makes ${proposal.makes}; ` : ''}runs go to {proposal.lands}.
      </p>

      <div className="mt-4 grid grid-cols-1 gap-3 lg:grid-cols-2" aria-busy={proposing}>
        <ItemList title="Changes each run" tone="vary" items={varies} onMove={move} empty="Nothing changes yet: every run would make the same video. Move something here." />
        <ItemList title="Stays the same" tone="stay" items={stays} onMove={move} empty="Everything changes each run." />
      </div>
      {proposing ? (
        <p className="mt-2 inline-flex items-center gap-1.5 text-[12px] text-subtle">
          <Spinner size={11} /> Updating the proposal…
        </p>
      ) : null}

      <label className="mt-4 block">
        <span className="text-[12.5px] font-medium text-muted">Hints for Claude (optional, one per line)</span>
        <textarea
          value={hints}
          onChange={(e) => setHints(e.target.value)}
          rows={2}
          maxLength={2000}
          placeholder="Name the app in the first two seconds."
          className="mt-1 w-full resize-y rounded-[10px] bg-panel px-3 py-2 text-[13.5px] leading-relaxed text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
        />
      </label>

      {problems.length ? (
        <div role="alert" className="mt-4 rounded-[10px] bg-danger-soft px-3 py-2.5" data-save-problems>
          <div className="mb-1 flex items-center gap-1.5 text-[12.5px] font-medium text-danger">
            <WarningCircle size={14} weight="fill" aria-hidden /> Its check found something to fix
          </div>
          <ul className="list-disc space-y-0.5 pl-5 text-[12.5px] text-fg">
            {problems.map((p, i) => (
              <li key={i}>{p.message}</li>
            ))}
          </ul>
          <p className="mt-1 text-[12px] text-muted">You can still save it: it stays a draft until the problems are fixed and a person makes it current.</p>
        </div>
      ) : proposal.check?.ok ? (
        <p className="mt-4 flex items-center gap-1.5 text-[12.5px] text-ok" data-save-checked>
          <CheckCircle size={14} weight="fill" aria-hidden /> It checks out: with its own inputs it makes the video again.
        </p>
      ) : null}
      {warnings.length ? (
        <ul className="mt-2 space-y-0.5 text-[12px] text-muted">
          {warnings.map((w, i) => (
            <li key={i} className="flex gap-1.5">
              <Info size={13} className="mt-[2px] shrink-0 text-subtle" aria-hidden />
              {w.message}
            </li>
          ))}
        </ul>
      ) : null}
      {error ? <ErrorPanel compact error={error} className="mt-4" /> : null}

      <div className="mt-5 flex flex-wrap items-center justify-end gap-2 border-t border-line pt-4">
        <Button variant="ghost" onClick={() => onClose()} disabled={saving}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" icon={<CookingPot size={14} />} loading={saving} disabled={proposing} data-save-recipe>
          Save as a recipe
        </Button>
      </div>
    </form>
  );
}

function ItemList({ title, tone, items, onMove, empty }: { title: string; tone: 'vary' | 'stay'; items: RecipeProposalItem[]; onMove: (it: RecipeProposalItem) => void; empty: string }) {
  return (
    <section aria-label={title} className={cx('rounded-[12px] p-3 hairline', tone === 'vary' ? 'bg-accent-soft/40' : 'bg-hover/40')} data-proposal-list={tone}>
      <h3 className="mb-2 text-[13px] font-semibold text-fg">{title}</h3>
      {items.length === 0 ? (
        <p className="text-[12.5px] text-muted">{empty}</p>
      ) : (
        <ul className="space-y-1.5">
          {items.map((it) => (
            <li key={it.key} className="flex items-start gap-2 rounded-[10px] bg-panel px-2.5 py-2 hairline" data-proposal-item={it.key}>
              <span className="min-w-0 flex-1">
                <span className="block text-[12.5px] leading-snug text-fg">{it.what}</span>
                {it.why ? <span className="mt-0.5 block text-[11.5px] leading-snug text-subtle">{it.why}</span> : null}
              </span>
              {it.movable ? (
                <Button
                  size="sm"
                  variant="ghost"
                  className="-mr-1 shrink-0"
                  icon={tone === 'vary' ? <ArrowDown size={12} /> : <ArrowUp size={12} />}
                  onClick={() => onMove(it)}
                  aria-label={tone === 'vary' ? `Keep the same: ${it.what}` : `Change each run: ${it.what}`}
                  title={tone === 'vary' ? 'Keep it the same every run' : 'Change it each run'}
                  data-move={it.key}
                >
                  <span className="max-sm:hidden">{tone === 'vary' ? 'Keep' : 'Vary'}</span>
                </Button>
              ) : (
                <Lock size={13} className="mt-[3px] shrink-0 text-subtle" aria-label="Stays with the recipe" />
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
