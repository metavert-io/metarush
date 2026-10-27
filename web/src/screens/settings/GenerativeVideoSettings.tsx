import { useEffect, useId, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { GenSettingsOut, OpInput } from '../../api/types';
import { money } from '../../components/generate/ai';
import { absolute, stamp } from '../../lib/format';
import { OwnerOnly } from '../../components/OwnerOnly';
import { useToast } from '../../ui/Toast';
import { Badge, Button, ErrorPanel, Progress, Segmented, Skeleton } from '../../ui/primitives';
import { Details } from './Details';

/*
 * Settings → Integrations → Generative video (D124): what new shots use
 * (the model, draft and final quality) and what may be spent — today's limit
 * for the whole team and each project's budget. Spending decisions are
 * people-only on the server; the key itself is on the provider card above.
 */

const resLabel = (r: string) => (r === '4k' ? '4K' : r);

/** "Gemini Omni 1.1 Flash: shots of 3 to 10 seconds…" under its own title reads "Shots of 3 to 10 seconds…". */
function withoutTitle(describe: string, title: string): string {
  const rest = describe.startsWith(`${title}: `) ? describe.slice(title.length + 2) : describe;
  return rest.charAt(0).toUpperCase() + rest.slice(1);
}

export function GenerativeVideoSettings() {
  const uid = useId();
  const toast = useToast();
  const qc = useQueryClient();
  const [projectId, setProjectId] = useState('');
  const settings = useOp('generate.settings', projectId ? { projectId } : {});
  const projects = useOp('project.list', {});
  const s = settings.data;
  const [cap, setCap] = useState('');
  const [budget, setBudget] = useState('');
  const [busy, setBusy] = useState('');
  useEffect(() => {
    if (s) setCap(String(s.dailyCapUsd));
  }, [s?.dailyCapUsd]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (s && projectId) setBudget(s.projectBudgetUsd !== undefined && s.projectBudgetUsd !== null ? String(s.projectBudgetUsd) : '');
  }, [s?.projectBudgetUsd, projectId]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = async (what: string, input: OpInput<'generate.configure'>, done: (o: GenSettingsOut) => string) => {
    setBusy(what);
    try {
      const out = await call('generate.configure', input);
      void qc.invalidateQueries({ queryKey: ['generate.settings'] });
      void qc.invalidateQueries({ queryKey: ['generate.estimate'] });
      toast({ tone: 'success', title: done(out) });
    } catch (e) {
      toast({ tone: 'error', title: 'Not saved', body: toApiError(e).message });
    } finally {
      setBusy('');
    }
  };

  if (settings.error) return <ErrorPanel error={settings.error} onRetry={() => void settings.refetch()} />;
  if (!s) return <Skeleton className="h-48 rounded-[16px]" />;
  const today = s.budget.today;
  const used = today.spentUsd + today.heldUsd;

  return (
    <section className="panel p-5" aria-labelledby={`${uid}-h`} data-generative-settings>
      <h2 id={`${uid}-h`} className="text-[15px] font-semibold">
        What new shots use, and what they may cost
      </h2>
      <p className="mt-1 max-w-[72ch] text-[13px] leading-relaxed text-muted">
        Every shot is priced before it is made and paid from the project's budget and today's limit. Only people change these.
      </p>

      <div className="mt-4 space-y-2" role="list" aria-label="Video models">
        {s.models.map((m) => (
          <div key={m.id} role="listitem" className="rounded-[12px] px-3.5 py-3 hairline">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-[14px] font-medium">{m.title}</span>
              {m.default ? <Badge tone="ok">Used for new shots</Badge> : null}
              {m.stage !== 'ga' ? <Badge tone="info">{m.stage === 'preview' ? 'Preview' : 'Experimental'}</Badge> : null}
              {m.retiredOn ? <Badge tone="danger">Retired {m.retiredOn}</Badge> : null}
            </div>
            <p className="mt-0.5 text-[12.5px] text-muted">{withoutTitle(m.describe, m.title)}.</p>
            <p className="mt-1 text-[12.5px] text-fg">
              {m.resolutions.map((r) => `${resLabel(r)} ${money(m.perSecondUsd[r])} a second${m.upscaled?.includes(r) ? ' (upscaled)' : ''}`).join(' · ')}
            </p>
            <p className="mt-0.5 text-[11.5px] text-subtle">
              Prices of {m.priceTable}. Google adds an invisible watermark ({m.watermark ?? 'SynthID'}) to what it makes.
            </p>
          </div>
        ))}
      </div>

      {/* The team's quality and daily limit are the owners' (D191); a project's budget stays open. */}
      <OwnerOnly>
      <div className="mt-5 grid grid-cols-1 gap-5 md:grid-cols-2">
        <div>
          <div className="mb-1.5 text-[12.5px] font-medium text-muted">Drafts</div>
          <Segmented
            label="Draft quality"
            value={s.draftResolution}
            onChange={(v) => void save('draft', { draftResolution: v as '360p' | '720p' }, () => `Drafts are ${resLabel(v)} now`)}
            items={['360p', '720p'].map((r) => ({ value: r, label: r === '360p' ? '360p, cheapest' : r }))}
          />
        </div>
        <div>
          <div className="mb-1.5 text-[12.5px] font-medium text-muted">Final quality</div>
          <Segmented
            label="Final quality"
            value={s.finalResolution}
            onChange={(v) => void save('final', { finalResolution: v as '1080p' | '4k' }, () => `Final quality is ${resLabel(v)} now`)}
            items={['1080p', '4k'].map((r) => ({ value: r, label: resLabel(r) }))}
          />
        </div>
      </div>

      <form
        className="mt-5"
        onSubmit={(e) => {
          e.preventDefault();
          const v = Number(cap);
          void save('cap', { dailyCapUsd: v }, (o) => `The team may spend up to ${money(o.dailyCapUsd)} a day on generated video`);
        }}
      >
        <label htmlFor={`${uid}-cap`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
          Today's limit for the whole team (US dollars a day)
        </label>
        <div className="flex flex-wrap items-center gap-2">
          <input
            id={`${uid}-cap`}
            type="number"
            min={1}
            max={1000}
            step={1}
            value={cap}
            onChange={(e) => setCap(e.target.value)}
            className="h-9 w-[120px] rounded-[10px] bg-panel px-3 text-[13.5px] text-fg hairline focus:outline-2 focus:outline-accent"
          />
          <Button type="submit" size="sm" loading={busy === 'cap'} disabled={!cap || Number(cap) === s.dailyCapUsd}>
            Save limit
          </Button>
          {s.dailyCapDefault ? (
            <span className="text-[12px] text-subtle">The usual limit: Auto doesn’t spend money by itself until a person sets one.</span>
          ) : s.dailyCapBy ? (
            <span className="text-[12px] text-subtle" title={absolute(s.dailyCapBy.at)} data-cap-by>
              Set by {s.dailyCapBy.userName || 'someone'}, {stamp(s.dailyCapBy.at)}.
            </span>
          ) : null}
        </div>
        <div className="mt-2 max-w-[460px]">
          <div className="text-[12.5px] text-muted">
            Today: {money(used)} of {money(s.dailyCapUsd)}
            {today.heldUsd > 0 ? `, ${money(today.heldUsd)} of it set aside for shots being made` : ''}
          </div>
          <Progress className="mt-1.5" value={Math.min(1, used / Math.max(0.01, s.dailyCapUsd))} tone={used / s.dailyCapUsd > 0.9 ? 'danger' : 'accent'} label="Spent today" />
        </div>
      </form>
      </OwnerOnly>

      <form
        className="mt-5"
        onSubmit={(e) => {
          e.preventDefault();
          if (!projectId) return;
          void save('budget', { projectId, budgetUsd: Number(budget || 0) }, (o) =>
            o.projectBudgetUsd ? `This project may spend up to ${money(o.projectBudgetUsd)} on generated video` : 'This project has no budget of its own now (today’s limit still applies)',
          );
        }}
      >
        <label htmlFor={`${uid}-prj`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
          A project's budget
        </label>
        <div className="flex flex-wrap items-center gap-2">
          <select
            id={`${uid}-prj`}
            value={projectId}
            onChange={(e) => setProjectId(e.target.value)}
            className="h-9 min-w-[200px] rounded-[10px] bg-panel px-2.5 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent"
          >
            <option value="">Choose a project</option>
            {(projects.data?.projects ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
          {projectId ? (
            <>
              <input
                aria-label="Budget in US dollars (0 for no limit)"
                type="number"
                min={0}
                step={1}
                value={budget}
                onChange={(e) => setBudget(e.target.value)}
                className="h-9 w-[120px] rounded-[10px] bg-panel px-3 text-[13.5px] text-fg hairline focus:outline-2 focus:outline-accent"
              />
              <Button type="submit" size="sm" loading={busy === 'budget'}>
                Save budget
              </Button>
              {!s.projectBudgetDefault ? (
                <Button size="sm" variant="ghost" loading={busy === 'clear'} onClick={() => void save('clear', { projectId, clearBudget: true }, () => 'Back to the usual budget')}>
                  Use the usual budget
                </Button>
              ) : null}
            </>
          ) : null}
        </div>
        {projectId && s.budget.project ? (
          <p className="mt-2 text-[12.5px] text-muted">
            Spent so far: {money(s.budget.project.spentUsd + s.budget.project.heldUsd)}
            {s.projectBudgetUsd !== undefined && s.projectBudgetUsd !== null ? ` of ${money(s.projectBudgetUsd)}` : ', no project limit'}
            {s.projectBudgetDefault ? ' (the usual budget)' : ''}
            {!s.projectBudgetDefault && s.projectBudgetBy ? `, set by ${s.projectBudgetBy.userName || 'someone'} ${stamp(s.projectBudgetBy.at)}` : ''}. 0 means no project limit. Auto
            spends money by itself only where a person set this project’s budget and the team’s daily limit.
          </p>
        ) : null}
      </form>

      <Details>
        <dl className="grid grid-cols-1 gap-x-3 sm:grid-cols-[170px_minmax(0,1fr)] sm:gap-y-1">
          <dt>Google's own limits</dt>
          <dd className="text-muted">{s.spendLimitsPer10Minutes?.map((v) => `$${v}`).join(', ')} per 10 minutes, by your Google usage tier</dd>
          <dt>From a terminal</dt>
          <dd className="tc text-muted">metarush generate settings · metarush generate configure --daily-cap 20</dd>
        </dl>
      </Details>
    </section>
  );
}
