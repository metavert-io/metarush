import { Link } from 'react-router';
import { ArrowLeft, ArrowsClockwise, CheckCircle, CookingPot, FilmSlate, FilmStrip, Hourglass, Play, Sparkle, Stop, WarningCircle } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import { absolute, stamp } from '../../lib/format';
import { openAskWith } from '../../lib/workbench';
import { CostEstimate } from '../CostEstimate';
import { Badge, Button, CopyButton, cx, Details, ErrorPanel, Freshness, Skeleton, Theirs } from '../../ui/primitives';
import { inApp, recipesLink, runLinks, runTone, stateShort, type RecipeRunView } from './api';
import { runName, useRunActions } from './actions';
import { useRunDialog } from './RunDialog';
import { RunSteps } from './RunSteps';
import { money } from '../../lib/costs';

/*
 * One run (?run=<id>, D204): its steps as they go (live), what it waits for —
 * a person (Go on, people only, saying how much more it asks to spend when it
 * does; or Stop) or Claude (what Claude is asked to do) — and, once it made
 * them, links to its project, the version and the review copy. What was
 * agreed and spent, the estimate it started with and the inputs as they were.
 * A run that makes a sample first (D294) shows it — its review copy to watch
 * and listen to, Approve the sample and Not quite… — and says where it stands.
 */

export function RunPage({ id }: { id: string }) {
  const q = useOp('recipe.runs', { run: id }, {
    // Live events say when it moves; this is the safety net while it goes on.
    refetchInterval: (query) => {
      const s = query.state.data?.run?.state;
      return s === 'running' ? 4_000 : s === 'waiting' ? 12_000 : false;
    },
  });
  const actions = useRunActions();
  const again = useRunDialog();
  if (q.isLoading) {
    return (
      <div className="space-y-4" aria-busy="true">
        <Skeleton className="h-10 w-80" />
        <Skeleton className="h-72 rounded-[16px]" />
      </div>
    );
  }
  const run = q.data?.run;
  if (q.error || !run) {
    return (
      <div className="space-y-4">
        <Link to={recipesLink()} className="-ml-1 inline-flex items-center gap-1 rounded-[8px] px-1.5 py-1 text-[12.5px] text-muted hover:bg-hover hover:text-fg">
          <ArrowLeft size={13} /> All recipes
        </Link>
        <ErrorPanel error={q.error} onRetry={() => void q.refetch()} title={q.error?.code === 'E_NOT_FOUND' ? 'There is no such run' : undefined} />
      </div>
    );
  }
  const r: RecipeRunView = run;
  const name = runName(r.number, r.recipeTitle);
  const links = runLinks(r);
  const going = r.state === 'running' || r.state === 'waiting';
  const sampleWaits = !!r.sampleWaits;
  const forPerson = r.state === 'waiting' && r.waitingFor === 'person' && !sampleWaits;
  const forClaude = r.state === 'waiting' && r.waitingFor === 'claude';
  const asks = forPerson && (r.waitingUsd ?? 0) > 0 ? r.waitingUsd : undefined;
  const ref = { id: r.id, name, waitingUsd: asks };
  return (
    <div className="space-y-6" data-run-page={r.id} data-run-state={r.state} data-waiting-for={r.waitingFor || undefined}>
      <div>
        <div className="mb-3 flex flex-wrap items-center gap-x-1 gap-y-1 text-[12.5px] text-muted">
          <Link to={recipesLink()} className="-ml-1 inline-flex items-center gap-1 rounded-[8px] px-1.5 py-1 hover:bg-hover hover:text-fg">
            <ArrowLeft size={13} /> All recipes
          </Link>
          <span aria-hidden>·</span>
          <Link to={recipesLink({ recipe: r.recipeName })} className="rounded-[8px] px-1.5 py-1 hover:bg-hover hover:text-fg" data-run-recipe-link>
            “<Theirs>{r.recipeTitle}</Theirs>”
          </Link>
          <Freshness className="ml-auto" at={q.dataUpdatedAt || undefined} fetching={q.isFetching} onRefresh={() => void q.refetch()} />
        </div>
        <div className="flex flex-col gap-3 lg:flex-row lg:items-start">
          <div className="min-w-0 flex-1">
            <h2 className="flex flex-wrap items-center gap-2 text-[24px] font-semibold leading-tight tracking-[-0.025em] text-fg sm:text-[28px]">
              <CookingPot size={22} className="shrink-0 text-accent-text" aria-hidden />
              <span className="min-w-0 break-words">
                Run {r.number} of “<Theirs>{r.recipeTitle}</Theirs>”
              </span>
            </h2>
            <div className="mt-2 flex flex-wrap items-center gap-2 text-[12.5px] text-muted">
              <Badge tone={runTone(r.state, r.waitingFor)} title={r.stateLabel}>
                {stateShort(r)}
              </Badge>
              <span>
                Started by {r.createdBy.userName || 'someone'}, <span title={absolute(r.createdAt)}>{stamp(r.createdAt)}</span>, from version {r.versionNumber} of the
                recipe{r.repeats ? ', making an earlier run again' : ''}.
              </span>
            </div>
            {r.note ? (
              <p className="mt-1.5 text-[13px] text-muted">
                For: “<Theirs>{r.note}</Theirs>”
              </p>
            ) : null}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {going && !forPerson ? (
              <Button variant="ghost" icon={<Stop size={14} weight="fill" />} loading={actions.busy === 'stop'} onClick={() => void actions.stop(ref)} data-run-stop>
                Stop this run
              </Button>
            ) : null}
            {!going && r.branch && !r.projectGone ? (
              <Button variant="secondary" icon={<ArrowsClockwise size={14} />} onClick={() => void again({ again: r.id, title: r.recipeTitle, number: r.number })} data-run-again>
                Run again…
              </Button>
            ) : null}
          </div>
        </div>
      </div>

      {sampleWaits ? (
        <SampleWaits run={r} onApprove={() => void actions.approveSample(ref)} onNotQuite={() => void actions.notQuite(ref)} onStop={() => void actions.stop(ref)} busy={actions.busy} />
      ) : forPerson ? (
        <div role="alert" className="rounded-[14px] bg-accent-soft p-4 ring-1 ring-accent-line" data-run-waits="person">
          <div className="flex items-start gap-3">
            <Hourglass size={20} weight="fill" className="mt-[1px] shrink-0 text-accent-text" aria-hidden />
            <div className="min-w-0 flex-1">
              <p className="text-[14px] font-medium text-fg">{r.waiting}</p>
              <p className="mt-1 text-[12.5px] leading-relaxed text-muted">
                {asks
                  ? `Going on agrees to ${money(asks)} more than you agreed to before; if it needs still more, it asks again.`
                  : 'Going on lets it carry on from here; it spends only what it still needs, and budgets and today’s limits still hold.'}
              </p>
              <div className="mt-3 flex flex-wrap gap-2">
                <Button variant="primary" icon={<Play size={14} weight="fill" />} loading={actions.busy === 'go'} onClick={() => void actions.goOn(ref)} data-run-go-on>
                  {asks ? `Go on (${money(asks)} more)` : 'Go on'}
                </Button>
                <Button variant="ghost" icon={<Stop size={14} weight="fill" />} loading={actions.busy === 'stop'} onClick={() => void actions.stop(ref)} data-run-stop>
                  Stop this run
                </Button>
              </div>
            </div>
          </div>
        </div>
      ) : forClaude ? (
        <div className="rounded-[14px] bg-info-soft p-4" data-run-waits="claude">
          <div className="flex items-start gap-3">
            <Sparkle size={20} weight="fill" className="mt-[1px] shrink-0 text-info" aria-hidden />
            <div className="min-w-0 flex-1">
              <p className="text-[14px] font-medium text-fg">{r.waiting}</p>
              <p className="mt-1 text-[12.5px] leading-relaxed text-muted">
                Ask Claude to choose them, or choose them yourself in the cut. The run goes on by itself once the stand-in clips are replaced.
              </p>
              <div className="mt-3 flex flex-wrap items-center gap-2">
                {r.claude && links.project ? (
                  <Button
                    variant="secondary"
                    icon={<Sparkle size={14} weight="fill" />}
                    onClick={() => openAskWith(`${capFirst(name)} waits for you to choose the moments. ${r.claude}`, { newConversation: true })}
                    data-run-ask-claude
                  >
                    Ask Claude to choose them
                  </Button>
                ) : null}
                {links.version ? (
                  <Link to={`${links.project}?tab=cut`} className="inline-flex h-9 items-center gap-1.5 rounded-[10px] px-3 text-[13px] font-medium text-fg hover:bg-hover">
                    <FilmSlate size={14} /> Open the cut
                  </Link>
                ) : null}
              </div>
              {r.claude ? (
                <Details compact label="What Claude is asked to do" className="mt-2">
                  <p className="whitespace-pre-wrap">{r.claude}</p>
                  <CopyButton text={r.claude} label="Copy" className="mt-1 h-6 px-2 text-[11.5px]" />
                </Details>
              ) : null}
            </div>
          </div>
        </div>
      ) : r.state === 'done' ? (
        <div className="rounded-[14px] bg-ok-soft p-4" data-run-done>
          <div className="flex items-start gap-3">
            <CheckCircle size={20} weight="fill" className="mt-[1px] shrink-0 text-ok" aria-hidden />
            <div className="min-w-0 flex-1">
              <p className="text-[14px] font-medium text-fg">
                {r.approved ? `Version ${r.revisionNumber} is approved.` : `Version ${r.revisionNumber} is in Awaiting your review.`}
                {r.qa ? ` The quality check ${r.qa}.` : ''}
              </p>
              <p className="mt-1 text-[12.5px] text-muted">It spent {r.spentWords}.</p>
            </div>
          </div>
        </div>
      ) : r.state === 'failed' || r.state === 'stopped' ? (
        <div className="rounded-[14px] bg-danger-soft p-4" data-run-ended>
          <div className="flex items-start gap-3">
            <WarningCircle size={20} weight="fill" className="mt-[1px] shrink-0 text-danger" aria-hidden />
            <div className="min-w-0 flex-1">
              <p className="text-[14px] font-medium text-fg">{r.state === 'stopped' ? 'This run was stopped.' : 'This run didn’t finish.'}</p>
              {r.problem ? <p className="mt-1 text-[13px] text-muted">{r.problem}</p> : null}
              <p className="mt-1 text-[12.5px] text-subtle">What it made stays.</p>
            </div>
          </div>
        </div>
      ) : null}

      {links.project ? (
        <nav aria-label="Where its video is" className="flex flex-wrap gap-2" data-run-links>
          <Link to={links.project} className="inline-flex h-9 min-w-0 max-w-full items-center gap-1.5 rounded-[10px] bg-panel px-3 text-[13px] font-medium text-fg hairline hover:bg-hover" data-run-project>
            <FilmSlate size={14} className="shrink-0 text-subtle" />
            <span className="truncate">
              {r.newProject ? 'Its project' : 'The project'}: “<Theirs>{r.projectName}</Theirs>”
            </span>
          </Link>
          {links.version ? (
            <Link to={links.version} className="inline-flex h-9 items-center gap-1.5 rounded-[10px] bg-panel px-3 text-[13px] font-medium text-fg hairline hover:bg-hover" data-run-version>
              Version {r.revisionNumber} of the cut
            </Link>
          ) : null}
          {links.review ? (
            <Link to={links.review} className="inline-flex h-9 items-center gap-1.5 rounded-[10px] bg-panel px-3 text-[13px] font-medium text-fg hairline hover:bg-hover" data-run-review>
              <FilmStrip size={14} className="text-subtle" /> The review copy
            </Link>
          ) : null}
        </nav>
      ) : r.projectGone ? (
        <p className="text-[12.5px] text-muted">Its project was cleared out; the run&rsquo;s record stays.</p>
      ) : null}

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,360px)]">
        <section aria-labelledby="run-steps" className="min-w-0">
          <h3 id="run-steps" className="mb-2 text-[15px] font-semibold text-fg">
            Steps
          </h3>
          <RunSteps steps={r.steps} className="-mx-2" />
        </section>
        <aside className="min-w-0 space-y-5">
          <section aria-labelledby="run-money" className="rounded-[14px] bg-raised p-4 hairline" data-run-money>
            <h3 id="run-money" className="mb-2 text-[13.5px] font-semibold text-fg">
              What it costs
            </h3>
            <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-[12.5px]">
              <dt className="text-subtle">Agreed</dt>
              <dd className="text-fg" data-run-agreed>
                {r.agreedWords || '—'}
                {r.agreed?.more?.length ? <span className="block text-[11.5px] text-muted">{r.agreed.more.map((m) => `${money(m.usd)} more by ${m.by.userName || 'someone'}`).join('; ')}</span> : null}
              </dd>
              <dt className="text-subtle">Spent</dt>
              <dd className="text-fg" data-run-spent>
                {r.spentWords}
              </dd>
            </dl>
            {r.estimate.parts.length ? <CostEstimate estimate={r.estimate} label="The estimate" className="mt-2" /> : null}
            {r.sample ? (
              <div className="mt-3 border-t border-line pt-2 text-[12px] leading-relaxed text-muted" data-run-sample={r.sample.state}>
                <span className="font-medium text-fg">The sample: </span>
                {r.sampleWords}
                {r.sampleUrl && !sampleWaits ? (
                  <Link to={inApp(r.sampleUrl) ?? '#'} className="ml-1 font-medium text-accent-text hover:underline" data-run-sample-link>
                    Open it
                  </Link>
                ) : null}
              </div>
            ) : null}
          </section>
          <InputsWere run={r} />
          {r.settings?.ownerName || r.settings?.voiceName ? (
            <p className="text-[12px] leading-relaxed text-subtle">
              It followed {r.settings.ownerName ? `“${r.settings.ownerName}”’s Content Settings` : 'the Content Settings'}
              {r.settings.voiceName ? `, with the voice “${r.settings.voiceName}”` : ''}.
            </p>
          ) : null}
        </aside>
      </div>
    </div>
  );
}

/** The inputs as the run had them, named as the recipe names them. */
function InputsWere({ run: r }: { run: RecipeRunView }) {
  const show = useOp('recipe.show', { recipe: r.recipeName, version: String(r.versionNumber) }, { staleTime: 5 * 60_000, retry: false });
  const labels = new Map((show.data?.inputs ?? []).map((i) => [i.name, i.label]));
  if (!r.inputs.length) return null;
  return (
    <section aria-labelledby="run-inputs">
      <h3 id="run-inputs" className="mb-2 text-[13.5px] font-semibold text-fg">
        Its inputs, as they were
      </h3>
      <ul className="space-y-1 text-[12.5px]" data-run-inputs-were>
        {r.inputs.map((i) => (
          <li key={i.name} className={cx('rounded-[8px] px-2 py-1', i.changed && 'bg-accent-soft/50')}>
            <span className="text-muted">{labels.get(i.name) ?? i.name}: </span>
            <span className="text-fg">{i.words}</span>
            {i.changed ? <span className="text-[11.5px] text-accent-text"> (changed)</span> : null}
          </li>
        ))}
      </ul>
    </section>
  );
}

function capFirst(s: string): string {
  return s ? s[0]!.toUpperCase() + s.slice(1) : s;
}

/** A sample waiting for a person (D294): its review copy, then approve it or say what isn't right. */
function SampleWaits({ run: r, onApprove, onNotQuite, onStop, busy }: { run: RecipeRunView; onApprove: () => void; onNotQuite: () => void; onStop: () => void; busy: string | null }) {
  const sm = r.sample!;
  return (
    <div role="alert" className="rounded-[14px] bg-accent-soft p-4 ring-1 ring-accent-line" data-run-waits="sample">
      <div className="flex items-start gap-3">
        <Hourglass size={20} weight="fill" className="mt-[1px] shrink-0 text-accent-text" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="text-[14px] font-medium text-fg">The sample is ready: watch and listen to it, then approve it.</p>
          <ul className="mt-1 list-disc space-y-0.5 pl-5 text-[12.5px] leading-relaxed text-muted">
            {sm.kinds.map((k) => (
              <li key={k.kind}>
                {k.what}
                {k.source ? ` — ${k.source}` : ''}
              </li>
            ))}
          </ul>
          {sm.render ? (
            <video
              controls
              playsInline
              preload="metadata"
              src={`/media/renders/${sm.render}`}
              className="mt-3 aspect-video w-full max-w-[560px] rounded-[10px] bg-black"
              aria-label="The sample's review copy"
              data-run-sample-video
            />
          ) : null}
          <p className="mt-2 text-[12.5px] leading-relaxed text-muted">
            The sample spent {money(sm.spent.usd ?? 0)}. Approving it plans the rest of the run at today’s prices; you agree to that before it goes on.
          </p>
          <div className="mt-3 flex flex-wrap gap-2">
            <Button variant="primary" icon={<CheckCircle size={14} weight="fill" />} loading={busy === 'sample'} onClick={onApprove} data-run-sample-approve>
              Approve the sample
            </Button>
            <Button variant="secondary" onClick={onNotQuite} disabled={busy === 'sample'} data-run-sample-not-quite>
              Not quite…
            </Button>
            {r.sampleUrl ? (
              <Link to={inApp(r.sampleUrl) ?? '#'} className="inline-flex h-9 items-center gap-1.5 rounded-[10px] px-3 text-[13px] font-medium text-fg hover:bg-hover">
                <FilmSlate size={14} /> Open the sample’s cut
              </Link>
            ) : null}
            <Button variant="ghost" icon={<Stop size={14} weight="fill" />} loading={busy === 'stop'} onClick={onStop} data-run-stop>
              Stop this run
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
