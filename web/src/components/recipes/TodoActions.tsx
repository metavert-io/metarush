import { Link } from 'react-router';
import { Play, Stop } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import type { Todo } from '../../api/types';
import { money } from '../../lib/costs';
import { Button } from '../../ui/primitives';
import { recipesLink } from './api';
import { runName, useRunActions } from './actions';

/*
 * A run that waits, as a question on To-dos (the to-do kind recipe_run, D204):
 * Go on (people only; when it asks to spend more, the amount is on the button
 * and in the styled question) and Stop, through the same actions as the
 * run's page — never a quiet answer that spends money.
 */
export function RunTodoActions({ todo: t }: { todo: Todo }) {
  const rr = t.context.recipe;
  const run = useOp('recipe.runs', rr ? { run: rr.runId } : null, { staleTime: 5_000 });
  const actions = useRunActions();
  if (!rr) return null;
  const view = run.data?.run;
  const waiting = view ? view.state === 'waiting' : true;
  const asks = view && view.waitingFor === 'person' && (view.waitingUsd ?? 0) > 0 ? view.waitingUsd : undefined;
  // A sample waits to be watched and approved on the run's page (D294), never from here.
  const sample = !!view?.sampleWaits;
  const ref = { id: rr.runId, name: runName(rr.number, rr.title), waitingUsd: asks };
  return (
    <div className="mt-3 flex flex-wrap items-center gap-2" role="group" aria-label="What to do with the run" data-run-todo={rr.runId}>
      {rr.waitingFor === 'person' && !sample ? (
        <Button variant="primary" icon={<Play size={13} weight="fill" />} loading={actions.busy === 'go'} disabled={!waiting} onClick={() => void actions.goOn(ref)} data-run-go-on>
          {asks ? `Go on (${money(asks)} more)` : 'Go on'}
        </Button>
      ) : null}
      <Button variant={rr.waitingFor === 'person' ? 'ghost' : 'secondary'} icon={<Stop size={13} weight="fill" />} loading={actions.busy === 'stop'} disabled={!waiting} onClick={() => void actions.stop(ref)} data-run-stop>
        Stop this run
      </Button>
      <Link to={recipesLink({ run: rr.runId })} className="inline-flex h-9 items-center rounded-[10px] px-3 text-[13px] font-medium text-accent-text hover:bg-accent-soft" data-run-todo-open>
        {sample ? 'Watch the sample' : 'Open the run'}
      </Link>
    </div>
  );
}
