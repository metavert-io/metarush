import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowsClockwise, Info, Play, WarningCircle } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import { money } from '../../lib/costs';
import { CostEstimate } from '../CostEstimate';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Button, cx, ErrorPanel, Skeleton, Spinner } from '../../ui/primitives';
import { RECIPE_QUERIES, recipesLink, type RecipePlanOut, type RecipeRunOut } from './api';
import { draftsToInputs, InputsForm, type InputDraft } from './InputsForm';
import { RunSteps } from './RunSteps';

/*
 * Run with… and Run again… (D204, D208): the recipe's inputs, then the plan
 * MetaRush makes from them — where the video goes, each step and who does it
 * (MetaRush, Claude or you), one estimate that opens to the services, models
 * and sources it draws on — then Run. Paid runs start only after the person
 * agrees to the estimate in a styled question, which covers the whole run;
 * with "Estimate costs of outside AI services before edits" off (Settings →
 * Preferences) there is no money question, only the line that budgets and
 * limits still hold. Nothing starts but what the person saw: if the plan
 * changed meanwhile, it is planned again and shown. A run with an expensive
 * kind of work makes a sample first (D294): Run starts the sample, agreeing to
 * its price only; the rest is planned once the person approves it on the run's
 * page. Skipping the sample asks why, and the reason is kept with the run.
 */

export type RunTarget = { recipe: string; title: string } | { again: string; title: string; number: number };

export function useRunDialog() {
  const dialog = useDialog();
  const navigate = useNavigate();
  const toast = useToast();
  const qc = useQueryClient();
  return async (target: RunTarget) => {
    const title = 'again' in target ? `Make run ${target.number} of “${target.title}” again` : `Run “${target.title}”`;
    const out = await dialog.open<RecipeRunOut>((close) => <RunForm target={target} onClose={close} />, { title, size: 'lg' });
    if (!out) return;
    for (const k of RECIPE_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
    toast({ tone: 'success', title: out.message, durationMs: 7000 });
    navigate(recipesLink({ run: out.run.id }));
  };
}

function RunForm({ target, onClose }: { target: RunTarget; onClose: (out?: RecipeRunOut) => void }) {
  const again = 'again' in target ? target.again : undefined;
  const dialog = useDialog();
  // Making a run again starts from what that run used.
  const prev = useOp('recipe.runs', again ? { run: again } : null);
  const recipeName = again ? prev.data?.run?.recipeName : 'recipe' in target ? target.recipe : undefined;
  const show = useOp('recipe.show', recipeName ? { recipe: recipeName } : null);
  const [drafts, setDrafts] = useState<Record<string, InputDraft>>({});
  const [note, setNote] = useState('');
  const [plan, setPlan] = useState<RecipePlanOut | null>(null);
  const [planError, setPlanError] = useState<unknown>(null);
  const [planning, setPlanning] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const seq = useRef(0);
  const inputs = useMemo(() => draftsToInputs(drafts), [drafts]);
  const ready = !!show.data && (!again || !!prev.data?.run);

  const planNow = async (opts: { quiet?: boolean } = {}) => {
    const n = ++seq.current;
    if (!opts.quiet) setPlanning(true);
    try {
      const out = await call('recipe.plan', again ? { repeats: again, inputs, ...(note.trim() ? { note: note.trim() } : {}) } : { recipe: recipeName!, inputs, ...(note.trim() ? { note: note.trim() } : {}) });
      if (n !== seq.current) return null;
      setPlan(out);
      setPlanError(null);
      return out;
    } catch (e) {
      if (n === seq.current) {
        setPlan(null);
        setPlanError(e);
      }
      return null;
    } finally {
      if (n === seq.current) setPlanning(false);
    }
  };

  // The plan follows the form (a moment after typing stops).
  useEffect(() => {
    if (!ready) return;
    const t = setTimeout(() => void planNow(), plan ? 450 : 0);
    return () => clearTimeout(t);
  }, [ready, inputs, note]); // eslint-disable-line react-hooks/exhaustive-deps

  const sample = plan?.sample ?? undefined;
  // What starting agrees to: the sample's price when the run starts with one (D294).
  const agreedNow = (p: RecipePlanOut) => (p.sample ? p.sample.estimate : p.estimate);
  const askMoney = !!plan && plan.askForEstimate && agreedNow(plan).usd > 0;
  const canRun = !!plan && plan.ready && !planning && !busy;

  const agree = (p: RecipePlanOut) => {
    if (p.sample) {
      return dialog.confirm({
        title: `Spend ${p.sample.estimate.about} on a sample first?`,
        body: (
          <>
            <p>{p.sample.summary}</p>
            <p className="mt-2">You agree now only to the sample. Once you approve it, the rest of the run is planned and you agree to its price then.</p>
          </>
        ),
        confirmLabel: 'Make the sample',
      });
    }
    // "make the AI voice (less than a cent)": the step's own words, only its first letter lowered.
    const priced = p.steps.filter((s) => (s.estimate ?? 0) > 0).map((s) => `${s.label.charAt(0).toLowerCase()}${s.label.slice(1)} (${money(s.estimate ?? 0)})`);
    return dialog.confirm({
      title: `Spend ${p.estimate.about} on this run?`,
      body: (
        <>
          <p>
            One agreement covers the whole run{priced.length ? `: ${priced.join(', ')}` : ''}. If a step would cost more than that, the run stops and asks you first.
          </p>
          <p className="mt-2">Budgets, today’s limits and project spending limits still hold.</p>
        </>
      ),
      confirmLabel: `Agree and run`,
    });
  };

  const start = async (skipSample?: string) => {
    if (!plan || !canRun) return;
    let p = skipSample ? { ...plan, sample: undefined } : plan;
    if (p.askForEstimate && agreedNow(p).usd > 0 && !(await agree(p))) return;
    setBusy(true);
    setError(null);
    setNotice('');
    try {
      for (let round = 0; ; round++) {
        const maxUsd = p.askForEstimate && agreedNow(p).usd > 0 ? agreedNow(p).usd : undefined;
        try {
          const body = { inputs, ...(note.trim() ? { note: note.trim() } : {}), fingerprint: p.fingerprint, ...(maxUsd !== undefined ? { maxUsd } : {}) };
          const skipped = skipSample ? { skipSample } : {};
          const out = again ? await call('recipe.again', { run: again, ...body, ...skipped }) : await call('recipe.run', { recipe: recipeName!, ...body, ...skipped });
          onClose(out);
          return;
        } catch (e) {
          const err = toApiError(e);
          if (err.code === 'E_CONFLICT' && round === 0) {
            // The plan changed since it was shown (an input, the recipe or a price): show the new one; nothing started.
            const fresh = await planNow({ quiet: true });
            setNotice('The plan changed since you looked (an input, the recipe or a price), so nothing started. Here it is again: look it over, then run it.');
            if (!fresh) throw e;
            return;
          }
          if (err.code === 'E_CONFIRM_REQUIRED' && round === 0) {
            // An amount is missing: the person agrees to the estimate the server named.
            const d = err.details as { estimate?: RecipePlanOut['estimate']; sample?: boolean } | undefined;
            const est = d?.estimate;
            if (!est) throw e;
            p = d?.sample && p.sample ? { ...p, askForEstimate: true, sample: { ...p.sample, estimate: est } } : { ...p, askForEstimate: true, estimate: est };
            if (!(await agree(p))) return;
            continue;
          }
          throw e;
        }
      }
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  const skip = async () => {
    const why = await dialog.prompt({
      title: 'Run it without the sample?',
      body: 'The whole run starts at once, and you agree to its whole price. Say why you go without the sample: the reason is kept with the run and on its receipt.',
      label: 'Why',
      placeholder: 'We used this voice last week',
      required: true,
      maxLength: 500,
      confirmLabel: 'Go without the sample',
      validate: (v) => (v.trim().length < 8 ? 'Say why in a few words.' : null),
    });
    if (why) void start(why.trim());
  };

  if (show.error || prev.error) return <ErrorPanel error={show.error ?? prev.error} onRetry={() => void (show.error ? show.refetch() : prev.refetch())} />;
  if (!ready) {
    return (
      <div className="space-y-3" aria-busy="true">
        <Skeleton className="h-16" />
        <Skeleton className="h-16" />
        <Skeleton className="h-28" />
      </div>
    );
  }
  const recipe = show.data!;
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void start();
      }}
      data-run-dialog
      data-plan-ready={plan?.ready ? 'yes' : 'no'}
    >
      {recipe.recipe.makes || recipe.lands ? (
        <p className="-mt-1 mb-4 text-[13px] leading-relaxed text-muted">
          {[recipe.recipe.makes ? `Makes ${recipe.recipe.makes}` : '', again ? 'a new version of the same cut' : recipe.lands].filter(Boolean).join(', ')}.
        </p>
      ) : null}

      <section aria-label="What changes this run">
        <h3 className="mb-2 text-[13.5px] font-semibold text-fg">{again ? 'What changes this time' : 'What changes each run'}</h3>
        <InputsForm
          inputs={recipe.inputs}
          drafts={drafts}
          onChange={(name, d) =>
            setDrafts((all) => {
              const next = { ...all };
              if (d) next[name] = d;
              else delete next[name];
              return next;
            })
          }
          planned={plan?.inputs}
          base={again ? prev.data?.run?.inputs : undefined}
          projectId={recipe.recipe.projectId}
          disabled={busy}
        />
        <label className="mt-3 block">
          <span className="text-[12.5px] font-medium text-muted">What this run is for (optional)</span>
          <input
            value={note}
            onChange={(e) => setNote(e.target.value)}
            maxLength={500}
            placeholder={again ? 'A warmer voice' : 'The spring episode'}
            disabled={busy}
            className="mt-1 h-9 w-full rounded-[10px] bg-panel px-3 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
          />
        </label>
      </section>

      <section aria-label="The plan" className="mt-5 border-t border-line pt-4" aria-busy={planning} data-run-plan>
        <div className="mb-2 flex items-center gap-2">
          <h3 className="text-[13.5px] font-semibold text-fg">The plan</h3>
          {planning ? (
            <span className="inline-flex items-center gap-1.5 text-[12px] text-subtle">
              <Spinner size={11} /> Planning…
            </span>
          ) : null}
        </div>
        {notice ? (
          <div className="mb-3 flex gap-2 rounded-[10px] bg-info-soft px-3 py-2 text-[12.5px] text-info" role="status" data-plan-changed>
            <ArrowsClockwise size={14} className="mt-[2px] shrink-0" aria-hidden />
            {notice}
          </div>
        ) : null}
        {planError ? (
          <ErrorPanel compact error={planError} onRetry={() => void planNow()} />
        ) : !plan ? (
          <Skeleton className="h-36" />
        ) : (
          <PlanView plan={plan} again={!!again} sample={sample} />
        )}
      </section>

      {error ? <ErrorPanel compact error={error} className="mt-4" /> : null}
      <div className="mt-5 flex flex-wrap items-center justify-end gap-2 border-t border-line pt-4">
        <Button variant="ghost" onClick={() => onClose()} disabled={busy}>
          Cancel
        </Button>
        {sample ? (
          <Button variant="secondary" onClick={() => void skip()} disabled={!canRun} data-run-skip-sample>
            Skip the sample…
          </Button>
        ) : null}
        <Button
          type="submit"
          variant="primary"
          icon={<Play size={14} weight="fill" />}
          loading={busy}
          disabled={!canRun}
          data-run-start
          data-asks-money={askMoney ? 'yes' : 'no'}
          data-sample-first={sample ? 'yes' : 'no'}
        >
          {sample ? (askMoney ? `Make the sample first (${sample.estimate.about})` : 'Make the sample first') : askMoney ? `Run it (${plan!.estimate.about})` : 'Run it'}
        </Button>
      </div>
    </form>
  );
}

/** The plan: where the video goes, problems first, the steps and who does them, what it costs. */
function PlanView({ plan, again, sample }: { plan: RecipePlanOut; again: boolean; sample?: NonNullable<RecipePlanOut['sample']> }) {
  return (
    <div className="space-y-3">
      <p className="text-[13px] leading-relaxed text-fg" data-plan-summary>
        {plan.summary}
      </p>
      {plan.problems.length ? (
        <div role="alert" className="rounded-[10px] bg-danger-soft px-3 py-2.5" data-plan-problems>
          <div className="mb-1 flex items-center gap-1.5 text-[12.5px] font-medium text-danger">
            <WarningCircle size={14} weight="fill" aria-hidden /> It can’t run yet
          </div>
          <ul className="list-disc space-y-0.5 pl-5 text-[12.5px] leading-relaxed text-fg">
            {plan.problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        </div>
      ) : null}
      {again && (plan.changes.length || plan.reused.length) ? (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2" data-plan-changes>
          {plan.changes.length ? (
            <div>
              <div className="mb-1 text-[11.5px] font-medium text-subtle">What changes</div>
              <ul className="list-disc space-y-0.5 pl-5 text-[12.5px] text-fg">
                {plan.changes.map((c) => (
                  <li key={c}>{c}</li>
                ))}
              </ul>
            </div>
          ) : null}
          {plan.reused.length ? (
            <div>
              <div className="mb-1 text-[11.5px] font-medium text-subtle">What is used again</div>
              <ul className="list-disc space-y-0.5 pl-5 text-[12.5px] text-muted">
                {plan.reused.map((c) => (
                  <li key={c}>{c}</li>
                ))}
              </ul>
            </div>
          ) : null}
        </div>
      ) : null}
      <RunSteps steps={plan.steps} planned className="-mx-2" />
      {sample ? (
        <div className="rounded-[10px] bg-accent-soft px-3 py-2.5" data-plan-sample>
          <div className="mb-1 text-[12.5px] font-semibold text-fg">A sample first</div>
          <ul className="list-disc space-y-0.5 pl-5 text-[12.5px] leading-relaxed text-fg">
            {sample.kinds.map((k) => (
              <li key={k.kind}>
                {k.what}
                {k.source ? <span className="text-muted"> — {k.source}</span> : null}
                <span className="text-subtle">
                  {' '}
                  ({money(k.usd)}; {money(k.runUsd)} in the whole run)
                </span>
              </li>
            ))}
          </ul>
          <p className="mt-1.5 text-[11.5px] leading-relaxed text-subtle">
            You watch and listen to it, then approve it on the run’s page; the rest is planned then, and you agree to its price.
          </p>
          {plan.askForEstimate && sample.estimate.parts.length ? <CostEstimate estimate={sample.estimate} label="The sample" className="mt-2" /> : null}
        </div>
      ) : null}
      {plan.askForEstimate ? (
        <div className="rounded-[10px] bg-hover/50 px-3 py-2.5" data-plan-estimate>
          {plan.estimate.parts.length ? (
            <CostEstimate estimate={plan.estimate} label="Estimate" />
          ) : (
            <p className="text-[12.5px] text-muted">
              Estimate: <span className="font-medium text-fg">{plan.estimate.about}</span>. No outside AI service is paid for this run.
            </p>
          )}
          {plan.estimate.usd > 0 && !sample ? (
            <p className="mt-1.5 text-[11.5px] leading-relaxed text-subtle">You agree to it once, before the run starts; a step that would cost more stops and asks.</p>
          ) : null}
          {plan.estimate.usd > 0 && sample ? (
            <p className="mt-1.5 text-[11.5px] leading-relaxed text-subtle">This is the whole run. You agree to the sample now and to the rest after you approve the sample.</p>
          ) : null}
        </div>
      ) : (
        <p className="flex gap-2 rounded-[10px] bg-hover/50 px-3 py-2.5 text-[12.5px] leading-relaxed text-muted" data-plan-no-estimate>
          <Info size={14} className="mt-[2px] shrink-0 text-subtle" aria-hidden />
          <span>
            Estimates are off for you, so there is no price to agree to: paid steps run within your budgets, today’s limits and project spending limits, which still
            apply.{' '}
            <Link to="/settings/preferences" className="font-medium text-accent-text hover:underline">
              Settings → Preferences
            </Link>
          </span>
        </p>
      )}
      {plan.budget.length ? (
        <ul className={cx('space-y-0.5 text-[11.5px] text-subtle')} data-plan-budget>
          {plan.budget.map((b) => (
            <li key={b}>{b}</li>
          ))}
        </ul>
      ) : null}
      {plan.warnings.length ? (
        <ul className="space-y-0.5 text-[12px] text-muted" data-plan-warnings>
          {plan.warnings.map((w) => (
            <li key={w} className="flex gap-1.5">
              <Info size={13} className="mt-[2px] shrink-0 text-subtle" aria-hidden />
              {w}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
