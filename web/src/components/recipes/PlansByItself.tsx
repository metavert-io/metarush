import { useEffect, useId, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { CalendarBlank, CookingPot, FolderOpen, Play } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import { absolute, stamp } from '../../lib/format';
import { useToast } from '../../ui/Toast';
import { Button, ErrorPanel, Skeleton, Theirs, cx } from '../../ui/primitives';
import { RECIPE_QUERIES, type RecipeTriggerRow } from './api';

/*
 * Planning by itself (D218): when new footage or pictures arrive where an
 * input takes them from, or every week at a time, the recipe plans a run and
 * rings the bell of the person who turned it on — it never starts one,
 * whatever Auto or the estimate preference say. The settings say whom they
 * tell and which computer looks; while they are on only that person or an
 * owner changes them. A plan it made waits at the top of the recipe's page
 * with its estimate: Run… opens the run dialog (the person agrees there),
 * Set aside drops it.
 */

const DAYS = ['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday'];
const upper = (s: string) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : s);

function useTriggerChange(name: string) {
  const qc = useQueryClient();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const change = async (input: { newFiles?: boolean; weekly?: string; dismiss?: boolean }): Promise<boolean> => {
    setBusy(true);
    try {
      const out = await call('recipe.triggers.set', { recipe: name, ...input });
      for (const k of [...RECIPE_QUERIES, 'recipe.triggers' as const]) void qc.invalidateQueries({ queryKey: [k] });
      if (out.message) toast({ tone: 'success', title: out.message });
      return true;
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'That didn’t change', body: [err.message, err.hint].filter(Boolean).join(' ') });
      return false;
    } finally {
      setBusy(false);
    }
  };
  return { change, busy };
}

/** The plan the recipe made by itself, at the top of its page: Run… or Set aside. */
export function PlannedBanner({ name, canRun, onRun }: { name: string; canRun: boolean; onRun: () => void }) {
  const q = useOp('recipe.triggers', { recipe: name });
  const { change, busy } = useTriggerChange(name);
  const row = q.data?.recipes[0];
  const p = row?.prepared;
  if (!p) return null;
  const price = p.estimate.usd > 0 ? p.estimate.about : 'nothing to pay';
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[12px] bg-accent-soft px-4 py-3 text-[13px] text-fg" data-planned-run data-planned-why={p.why}>
      <CookingPot size={16} className="shrink-0 text-accent-text" aria-hidden />
      <span className="min-w-0 flex-1 leading-relaxed">
        <span className="font-medium">{p.words}</span>
        {p.ready ? `: the run is planned, ${price}.` : `: the run can’t start yet${p.problems?.length ? ` — ${p.problems[0]}` : ''}.`}{' '}
        <span className="text-muted">
          Planned{' '}
          <span title={absolute(p.at)}>{stamp(p.at)}</span> for <Theirs>{p.for.userName || 'someone'}</Theirs>; nothing starts until a person runs it.
        </span>
      </span>
      <span className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="primary" icon={<Play size={12} weight="fill" />} disabled={!canRun || !p.ready} onClick={onRun} data-run-planned>
          Run…
        </Button>
        <Button size="sm" variant="ghost" loading={busy} onClick={() => void change({ dismiss: true })} data-set-aside>
          Set aside
        </Button>
      </span>
    </div>
  );
}

/** When the recipe plans a run by itself, whom it tells, and the switches. */
export function PlansByItself({ name }: { name: string }) {
  const q = useOp('recipe.triggers', { recipe: name });
  if (q.isLoading) return <Skeleton className="h-28 rounded-[14px]" />;
  if (q.error || !q.data?.recipes[0]) return <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />;
  return <TriggerSettings key={JSON.stringify(q.data.recipes[0].weekly ?? null)} name={name} row={q.data.recipes[0]} />;
}

function TriggerSettings({ name, row }: { name: string; row: RecipeTriggerRow }) {
  const { change, busy } = useTriggerChange(name);
  const ids = { files: useId(), weekly: useId(), day: useId(), time: useId() };
  const [day, setDay] = useState<string>(row.weekly?.day ?? 'monday');
  const [time, setTime] = useState(row.weekly?.time ?? '09:00');
  const locked = !row.canChange;
  const noSource = row.sources.length === 0;
  // The switches move at once; a refusal puts them back.
  const [files, setFiles] = useState(row.newFiles);
  const [weeklyOn, setWeeklyOn] = useState(!!row.weekly);
  useEffect(() => setFiles(row.newFiles), [row.newFiles]);
  useEffect(() => setWeeklyOn(!!row.weekly), [row.weekly]);
  const setWeekly = (d: string, t: string) => void change({ weekly: `${d} ${t}` });
  const flipFiles = async (on: boolean) => {
    setFiles(on);
    if (!(await change({ newFiles: on }))) setFiles(!on);
  };
  const flipWeekly = async (on: boolean) => {
    setWeeklyOn(on);
    if (!(await change({ weekly: on ? `${day} ${time}` : 'off' }))) setWeeklyOn(!on);
  };
  return (
    <section aria-labelledby="rcp-triggers" data-recipe-triggers data-triggers-on={row.on ? 'yes' : 'no'}>
      <h3 id="rcp-triggers" className="mb-1 text-[15px] font-semibold text-fg">
        Planning by itself
      </h3>
      <p className="mb-2 max-w-[80ch] text-[12.5px] leading-relaxed text-muted">
        It can plan a run and ring your bell when new footage or pictures arrive, or every week. It never starts one: you run the plan when it suits you.
      </p>
      <fieldset disabled={locked} aria-busy={busy} className="m-0 min-w-0 space-y-2 border-0 p-0">
        <label htmlFor={ids.files} className={cx('flex items-start gap-3 rounded-[12px] bg-panel px-3.5 py-3 hairline', noSource || locked ? 'cursor-not-allowed' : 'cursor-pointer')}>
          <input
            id={ids.files}
            type="checkbox"
            checked={files}
            disabled={noSource && !files}
            onChange={(e) => void flipFiles(e.target.checked)}
            className="mt-[3px] h-4 w-4 shrink-0 accent-[var(--accent)]"
            data-trigger-new-files={row.newFiles ? 'on' : 'off'}
          />
          <span className="min-w-0">
            <span className={cx('flex items-center gap-1.5 text-[13.5px] font-medium', noSource ? 'text-muted' : 'text-fg')}>
              <FolderOpen size={14} className="text-subtle" aria-hidden /> When new footage or pictures arrive
            </span>
            <span className="mt-0.5 block text-[12.5px] leading-relaxed text-muted">
              {noSource
                ? 'No input takes its footage or pictures from a linked folder, a collection, a project or a repository, so nothing new can arrive for it.'
                : `In ${row.sources.join(' and ')}: once they stop arriving, it plans a run with the newest and tells you.`}
            </span>
          </span>
        </label>
        <div className={cx('rounded-[12px] bg-panel px-3.5 py-3 hairline')}>
          <label htmlFor={ids.weekly} className={cx('flex items-start gap-3', locked ? 'cursor-not-allowed' : 'cursor-pointer')}>
            <input
              id={ids.weekly}
              type="checkbox"
              checked={weeklyOn}
              onChange={(e) => void flipWeekly(e.target.checked)}
              className="mt-[3px] h-4 w-4 shrink-0 accent-[var(--accent)]"
              data-trigger-weekly={row.weekly ? 'on' : 'off'}
            />
            <span className="min-w-0">
              <span className="flex items-center gap-1.5 text-[13.5px] font-medium text-fg">
                <CalendarBlank size={14} className="text-subtle" aria-hidden /> Every week
              </span>
              <span className="mt-0.5 block text-[12.5px] leading-relaxed text-muted">
                {row.weekly && row.next ? (
                  <>
                    {upper(row.weeklyWords ?? '')}; the next plan <span title={absolute(row.next)}>{stamp(row.next)}</span>.
                  </>
                ) : (
                  'On a day and at a time of the computer that looks.'
                )}
              </span>
            </span>
          </label>
          <div className="mt-2 flex flex-wrap items-center gap-2 pl-7">
            <label htmlFor={ids.day} className="sr-only">
              Day
            </label>
            <select
              id={ids.day}
              value={day}
              onChange={(e) => {
                setDay(e.target.value);
                if (weeklyOn) setWeekly(e.target.value, time);
              }}
              className="h-8 rounded-[8px] bg-raised px-2 text-[12.5px] text-fg hairline"
              data-trigger-day
            >
              {DAYS.map((d) => (
                <option key={d} value={d}>
                  {upper(d)}
                </option>
              ))}
            </select>
            <label htmlFor={ids.time} className="sr-only">
              Time
            </label>
            <input
              id={ids.time}
              type="time"
              value={time}
              onChange={(e) => setTime(e.target.value)}
              onBlur={() => {
                if (weeklyOn && time !== row.weekly?.time) setWeekly(day, time);
              }}
              className="h-8 rounded-[8px] bg-raised px-2 text-[12.5px] text-fg hairline"
              data-trigger-time
            />
          </div>
        </div>
      </fieldset>
      <p className="mt-2 text-[12px] leading-relaxed text-subtle" data-triggers-who>
        {row.on ? (
          <>
            Tells <Theirs>{row.tells || 'someone'}</Theirs>
            {row.computer ? (
              <>
                {' '}
                · the computer <Theirs>{row.computer}</Theirs> looks
              </>
            ) : null}
            {row.at ? (
              <>
                {' '}
                · turned on by <Theirs>{row.by || 'someone'}</Theirs> <span title={absolute(row.at)}>{stamp(row.at)}</span>
              </>
            ) : null}
            .{locked ? ` Only ${row.tells || 'they'} or an owner changes it while it is on.` : ''}
          </>
        ) : (
          'Off. Turned on, it tells you, and this computer looks for new files and the weekly time.'
        )}
      </p>
    </section>
  );
}
