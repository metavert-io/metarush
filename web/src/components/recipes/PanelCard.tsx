import { useState } from 'react';
import { ArrowSquareOut, ArrowsClockwise, CaretDown, CaretRight, Info, Sparkle, WarningCircle } from '@phosphor-icons/react';
import { call } from '../../api/client';
import type { AssistantAction } from '../../api/types';
import { money } from '../../lib/costs';
import { useDialog } from '../../ui/Dialog';
import { cx, Theirs } from '../../ui/primitives';
import type { RecipeCardView } from './api';
import { Differences } from './Differences';
import { RunSteps, whoElse } from './RunSteps';

/*
 * A card about a recipe in Claude's panel (D211): Claude's refused `metarush
 * recipe run|again|save|update|publish` — and keeping or changing a run's
 * first picks (D215), saving a recipe sheet (D213) or the changes suggested
 * from runs (D217) as a draft — built by the server. A run's card
 * shows the plan — where the video goes, the inputs (what this run gives), each
 * step and who does it — and its one estimate (the card's CostEstimate); a
 * save's what would change each run and what would stay; a draft's and a
 * version made current's what they change, in plain words (D212). None runs by
 * itself: a run is agreed to in a styled question (one agreement covers it, a
 * step that would cost more asks again; with estimates off, no money question),
 * making a version current asks after naming what it changes and the rules it
 * no longer keeps, and a save or a change is a draft saved with one click.
 */

const lower = (s: string) => (s ? s.charAt(0).toLowerCase() + s.slice(1) : s);
const upper = (s: string) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : s);

export function RecipeCardBody({ action, onOpen }: { action: AssistantAction; onOpen: (to: string) => void }) {
  const card = action.recipe!;
  const pending = action.state === 'pending' || action.state === 'unavailable';
  return (
    <div className="mt-1.5 space-y-2.5" data-recipe-card-kind={card.kind}>
      {card.replanned && action.state === 'pending' ? (
        <p className="flex gap-2 rounded-[10px] bg-info-soft px-3 py-2 text-[12.5px] leading-relaxed text-info" role="status" data-card-replanned>
          <ArrowsClockwise size={14} className="mt-[2px] shrink-0" aria-hidden />
          The plan changed since Claude asked (another run started, an input or a price changed), so nothing started. Here it is again: look it over, then run it.
        </p>
      ) : null}
      {card.kind === 'run' || card.kind === 'again' ? <RunPlan card={card} /> : null}
      {card.kind === 'save' ? <SavePlan card={card} /> : null}
      {card.kind === 'update' || card.kind === 'publish' || card.kind === 'sheet' || card.kind === 'suggested' ? <Changes card={card} /> : null}
      {card.kind === 'picks' ? <PicksPlan card={card} /> : null}
      {card.problems?.length ? (
        <ul className="space-y-0.5" data-card-problems>
          {card.problems.map((p, i) => (
            <li key={i} className="flex gap-1.5 text-[12.5px] leading-relaxed text-danger">
              <WarningCircle size={13} weight="fill" className="mt-[3px] shrink-0" aria-hidden />
              {p}
            </li>
          ))}
        </ul>
      ) : null}
      {card.warnings?.length ? (
        <ul className="space-y-0.5" data-card-warnings>
          {card.warnings.map((w, i) => (
            <li key={i} className="flex gap-1.5 text-[12px] leading-relaxed text-muted">
              <Info size={13} className="mt-[2px] shrink-0 text-subtle" aria-hidden />
              {w}
            </li>
          ))}
        </ul>
      ) : null}
      {card.link && pending ? (
        <button type="button" onClick={() => onOpen(card.link!)} className="inline-flex items-center gap-1 text-[12px] font-medium text-accent-text hover:underline" data-card-open-recipe>
          <ArrowSquareOut size={12} aria-hidden />{' '}
          {card.kind === 'again' ? 'Open the run it makes again' : card.kind === 'picks' ? 'Open the run' : card.kind === 'publish' ? 'Look at this version first' : 'Open the recipe'}
        </button>
      ) : null}
    </div>
  );
}

/** A run's plan: where the video goes, its inputs, what changes (again), and its steps one click away. */
function RunPlan({ card }: { card: RecipeCardView }) {
  const [open, setOpen] = useState(false);
  const steps = card.steps ?? [];
  const others = whoElse(steps);
  return (
    <>
      <p className="text-[12.5px] leading-relaxed text-muted" data-card-lands>
        {upper(`makes ${card.lands}`)}.
        {card.note ? (
          <>
            {' '}
            For: “<Theirs>{card.note}</Theirs>”.
          </>
        ) : null}
      </p>
      {card.inputs?.length ? (
        <ul className="space-y-0.5 rounded-[10px] bg-panel px-3 py-2 hairline" aria-label="Its inputs" data-card-inputs>
          {card.inputs.map((i) => (
            <li key={i.name} className={cx('text-[12.5px] leading-relaxed', i.given ? 'text-fg' : 'text-muted')} data-card-input={i.name} data-given={i.given ? 'yes' : 'no'}>
              <span className={i.given ? 'font-medium' : ''}>{i.label}:</span> {i.words}
              {i.given ? <span className="ml-1 text-[11.5px] text-accent-text">{card.kind === 'again' ? '(changes)' : '(this run)'}</span> : null}
            </li>
          ))}
        </ul>
      ) : null}
      {card.kind === 'again' && (card.changes?.length || card.reused?.length) ? (
        <div className="grid grid-cols-1 gap-2 text-[12.5px] sm:grid-cols-2" data-card-changes>
          {card.changes?.length ? (
            <div>
              <div className="text-[11.5px] font-medium text-subtle">What changes</div>
              <ul className="mt-0.5 list-disc space-y-0.5 pl-4 text-fg">
                {card.changes.map((c) => (
                  <li key={c}>{c}</li>
                ))}
              </ul>
            </div>
          ) : null}
          {card.reused?.length ? (
            <div>
              <div className="text-[11.5px] font-medium text-subtle">What is used again</div>
              <ul className="mt-0.5 list-disc space-y-0.5 pl-4 text-muted">
                {card.reused.map((c) => (
                  <li key={c}>{c}</li>
                ))}
              </ul>
            </div>
          ) : null}
        </div>
      ) : null}
      {card.claude ? (
        <p className="flex gap-1.5 text-[12.5px] leading-relaxed text-accent-text" data-card-claude>
          <Sparkle size={13} weight="fill" className="mt-[3px] shrink-0" aria-hidden />
          It waits for Claude to choose the moments; Claude hears when it does.
        </p>
      ) : null}
      {steps.length ? (
        <div>
          <button type="button" onClick={() => setOpen((v) => !v)} aria-expanded={open} className="flex items-center gap-1 text-[12px] font-medium text-muted hover:text-fg" data-card-steps-toggle>
            {open ? <CaretDown size={11} /> : <CaretRight size={11} />}
            What the run does: {steps.length} steps{others ? `; ${others}` : ''}
          </button>
          {open ? <RunSteps steps={steps} planned className="-mx-2 mt-1" /> : null}
        </div>
      ) : null}
      {card.budget?.length ? (
        <ul className="space-y-0.5 text-[11.5px] text-subtle" data-card-budget>
          {card.budget.map((b) => (
            <li key={b}>{b}</li>
          ))}
        </ul>
      ) : null}
    </>
  );
}

function SavePlan({ card }: { card: RecipeCardView }) {
  return (
    <>
      <p className="text-[12.5px] leading-relaxed text-muted">
        {upper(`makes ${card.makes}`)}; each run makes {card.lands}. Saved as a draft: a person makes it current before it runs.
      </p>
      {card.note ? (
        <p className="text-[12.5px] text-muted">
          Claude’s note: “<Theirs>{card.note}</Theirs>”
        </p>
      ) : null}
      <div className="grid grid-cols-1 gap-2 rounded-[10px] bg-panel px-3 py-2 text-[12.5px] hairline" data-card-proposal>
        <div data-card-list="varies">
          <div className="text-[11.5px] font-medium text-subtle">Changes each run</div>
          {card.varies?.length ? (
            <ul className="mt-0.5 list-disc space-y-0.5 pl-4 text-fg">
              {card.varies.map((c) => (
                <li key={c}>{c}</li>
              ))}
            </ul>
          ) : (
            <p className="mt-0.5 text-muted">Nothing: every run makes the same video.</p>
          )}
        </div>
        {card.stays?.length ? (
          <div data-card-list="stays">
            <div className="text-[11.5px] font-medium text-subtle">Stays the same</div>
            <ul className="mt-0.5 list-disc space-y-0.5 pl-4 text-muted">
              {card.stays.map((c) => (
                <li key={c}>{c}</li>
              ))}
            </ul>
          </div>
        ) : null}
      </div>
    </>
  );
}

/** A run's picks (D215): what the card keeps or changes, then each pick as it stands. */
function PicksPlan({ card }: { card: RecipeCardView }) {
  const words: Record<string, string> = { first: 'first pick', standin: 'stand-in to replace', kept: 'kept', changed: 'changed', dropped: 'dropped' };
  return (
    <>
      <p className="text-[12.5px] leading-relaxed text-muted" data-card-picks-change>
        In {card.run}: {lower(card.change ?? '')}
      </p>
      {card.picks?.length ? (
        <ul className="space-y-0.5 rounded-[10px] bg-panel px-3 py-2 hairline" aria-label="Its picks" data-card-picks>
          {card.picks.map((p) => (
            <li key={`${p.part}-${p.n}`} className={cx('text-[12.5px] leading-relaxed', p.changes ? 'text-fg' : 'text-muted')} data-card-pick={`${p.part}-${p.n}`} data-changes={p.changes ? 'yes' : 'no'}>
              <span className={p.changes ? 'font-medium' : ''}>{p.kind === 'short' ? `Short ${p.n}` : `“${p.part}” ${p.n}`}:</span> {p.when}
              {p.of ? (
                <>
                  {' '}
                  of “<Theirs>{p.of}</Theirs>”
                </>
              ) : null}{' '}
              — {words[p.state] ?? p.state}
              {p.why ? ` (${p.why})` : ''}
            </li>
          ))}
        </ul>
      ) : null}
      <p className="text-[12px] text-subtle">The run goes on by itself once every pick is kept or changed.</p>
    </>
  );
}

function Changes({ card }: { card: RecipeCardView }) {
  const parts = card.differences ?? [];
  const n = parts.reduce((t, p) => t + p.changes.length, 0);
  const from = card.kind === 'publish' ? card.against : card.version;
  const counted = n === 1 ? '1 change' : `${n} changes`;
  return (
    <>
      {card.kind === 'suggested' && card.reasons?.length ? (
        <ul className="list-disc space-y-0.5 pl-4 text-[12.5px] leading-relaxed text-fg" aria-label="Why" data-card-reasons>
          {card.reasons.map((r) => (
            <li key={r}>{r}</li>
          ))}
        </ul>
      ) : null}
      <p className="text-[12.5px] leading-relaxed text-muted" data-card-change-count>
        {card.kind === 'publish'
          ? from
            ? `Compared with version ${from}, the one runs make now: ${counted}.`
            : 'No version is current yet: runs will make this one.'
          : card.kind === 'sheet' && card.new
            ? 'A new recipe, saved as a draft: a person makes it current before it runs.'
            : `A draft from version ${from}: ${counted}. Runs keep making the current version until a person makes it current.`}
      </p>
      {card.note ? (
        <p className="text-[12.5px] text-muted">
          Claude’s note: “<Theirs>{card.note}</Theirs>”
        </p>
      ) : null}
      <Differences parts={parts} removedRules={card.removedRules} compact className="rounded-[10px] bg-panel px-3 py-2 hairline" />
    </>
  );
}

/**
 * Asks before a card about a recipe runs: a run's agreement to its estimate
 * (or, with estimates off, only whether to start it), what making a version
 * current changes. A save or a change is a draft: no question.
 */
export function useRecipeCardQuestion() {
  const dialog = useDialog();
  return async (action: AssistantAction): Promise<boolean> => {
    const card = action.recipe;
    if (!card) return true;
    if (card.kind === 'run' || card.kind === 'again') {
      const est = action.estimate;
      let ask = !!est && est.usd > 0;
      if (ask) {
        try {
          ask = (await call('account.preferences', {})).estimates; // this person's "Estimate costs … before edits" (D208)
        } catch {
          ask = true;
        }
      }
      const name = action.title.replace(/ \((about|less than)[^)]*\)$/, '');
      if (ask && est) {
        const priced = (card.steps ?? []).filter((s) => (s.estimate ?? 0) > 0).map((s) => `${lower(s.label)} (${money(s.estimate ?? 0)})`);
        return dialog.confirm({
          title: `Spend ${est.about} on this run?`,
          body: (
            <div data-card-agree>
              <p>
                {name}: one agreement covers the whole run{priced.length ? `: ${priced.join(', ')}` : ''}. If a step would cost more than that, the run stops and asks you
                first.
              </p>
              <p className="mt-2">Budgets, today’s limits and project spending limits still hold. Claude asked; starting it is your decision.</p>
            </div>
          ),
          confirmLabel: 'Agree and run',
        });
      }
      return dialog.confirm({
        title: `${name}?`,
        body: (
          <div data-card-agree>
            <p>{upper(`it makes ${card.lands}`)}.</p>
            <p className="mt-2">
              {est && est.usd > 0
                ? 'Estimates are off for you, so there is no price to agree to: paid steps run within your budgets, today’s limits and project spending limits.'
                : 'No outside AI service is paid for in this run.'}{' '}
              Claude asked; starting it is your decision.
            </p>
          </div>
        ),
        confirmLabel: card.kind === 'again' ? 'Run it again' : 'Run it',
      });
    }
    if (card.kind === 'publish') {
      return dialog.confirm({
        title: `${action.title}?`,
        body: (
          <div className="space-y-2" data-card-agree>
            <p>Runs make it from now on. Runs already made keep the version they made.</p>
            <Differences parts={card.differences ?? []} removedRules={card.removedRules} compact />
          </div>
        ),
        confirmLabel: 'Make it current',
      });
    }
    return true;
  };
}
