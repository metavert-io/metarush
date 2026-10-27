import { useEffect, useId, useRef, useState } from 'react';
import { useLocation } from 'react-router';
import { Check, TerminalWindow } from '@phosphor-icons/react';
import { useAssistantOp, useAssistantSettings, useAssistantStatus, useModels } from '../../api/assistant';
import { Button, ErrorPanel, Skeleton, cx } from '../../ui/primitives';

/*
 * Settings → Claude Code: the installation's default model and effort (and,
 * with an API key, the dollar cap per conversation; a Claude plan's limit is
 * tokens, in "usage and memory", D181). The choices come from the installed
 * Claude Code itself; a conversation can still pick its own (/model, /effort).
 */
export function ClaudeCodeSettings() {
  const status = useAssistantStatus();
  const settings = useAssistantSettings();
  const models = useModels(!!status.data?.available);
  const save = useAssistantOp('assistant.settings.set');
  const loc = useLocation();
  const ref = useRef<HTMLElement>(null);
  const ids = { model: useId(), effort: useId(), limit: useId() };
  const s = settings.data;
  const [model, setModel] = useState('default');
  const [effort, setEffort] = useState('default');
  const [limit, setLimit] = useState('');
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    if (!s) return;
    setModel(s.model || 'default');
    setEffort(s.effort || 'default');
    setLimit(s.budgetSet ? s.budgetUsd.toFixed(2) : '');
  }, [s]);
  useEffect(() => {
    if (loc.hash === '#claude-code') ref.current?.scrollIntoView({ block: 'start' });
  }, [loc.hash]);
  useEffect(() => {
    if (!saved) return;
    const t = setTimeout(() => setSaved(false), 2500);
    return () => clearTimeout(t);
  }, [saved]);

  const cat = models.data?.models ?? [];
  const chosen = cat.find((m) => m.value === model || m.resolvedModel === model) ?? cat.find((m) => m.value === 'default');
  const levels = chosen?.effortLevels ?? [];
  const effortOk = effort === 'default' || levels.includes(effort);
  const plan = s?.billing === 'plan';
  const limitNum = limit.trim() === '' ? 0 : Number(limit);
  const limitOk = limit.trim() === '' || (Number.isFinite(limitNum) && limitNum >= 0.05 && limitNum <= 100);
  const dirty = !!s && (model !== (s.model || 'default') || effort !== (s.effort || 'default') || (!plan && (s.budgetSet ? s.budgetUsd.toFixed(2) : '') !== limit.trim()));
  const unavailable = status.data && !status.data.available;

  const submit = () =>
    save.mutate(plan ? { model, effort } : { model, effort, safetyLimitUsd: limitNum }, { onSuccess: () => setSaved(true) });

  return (
    <section ref={ref} id="claude-code" className="panel scroll-mt-24 p-5" aria-labelledby="claude-code-h">
      <h2 id="claude-code-h" className="flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <TerminalWindow size={17} />
        </span>
        Claude Code
      </h2>
      <p className="mt-1 text-[13px] leading-relaxed text-muted">
        {status.data?.available
          ? `Claude Code ${status.data.claudeVersion ?? ''} on this machine. ${status.data.billingLabel}.`
          : status.data?.reason ?? 'Checking for Claude Code on this machine.'}
      </p>
      {settings.isLoading || status.isLoading ? (
        <div className="mt-4 space-y-2">
          <Skeleton className="h-9" />
          <Skeleton className="h-9" />
        </div>
      ) : settings.error ? (
        <ErrorPanel error={settings.error} compact className="mt-4" onRetry={() => void settings.refetch()} />
      ) : s ? (
        <div className={cx('mt-4 grid grid-cols-1 gap-4', plan ? 'md:grid-cols-2' : 'md:grid-cols-3')}>
          <div>
            <label htmlFor={ids.model} className="mb-1.5 block text-[12.5px] font-medium text-muted">
              Model
            </label>
            {models.isLoading ? (
              <Skeleton className="h-9" />
            ) : (
              <select id={ids.model} value={model} disabled={!!unavailable} onChange={(e) => setModel(e.target.value)} className="h-9 w-full rounded-[10px] bg-panel px-2.5 text-[13px] text-fg hairline disabled:opacity-60">
                {cat.length === 0 ? <option value="default">Default</option> : null}
                {cat.map((m) => (
                  <option key={m.value} value={m.value}>
                    {m.value === 'default' ? `Default (${m.displayName.replace(/^Default \(|\)$/g, '') || 'Claude Code picks'})` : m.displayName}
                  </option>
                ))}
                {(models.data?.unavailable ?? []).map((m) => (
                  <option key={m.value} value={m.value} disabled>
                    {m.displayName} (update Claude Code)
                  </option>
                ))}
              </select>
            )}
            {chosen?.description && model !== 'default' ? <p className="mt-1 text-[11.5px] text-subtle">{chosen.description}</p> : null}
            {models.data?.source === 'fallback' ? <p className="mt-1 text-[11.5px] text-subtle">{models.data.note}</p> : null}
          </div>
          <div>
            <label htmlFor={ids.effort} className="mb-1.5 block text-[12.5px] font-medium text-muted">
              Effort
            </label>
            <select
              id={ids.effort}
              value={effortOk ? effort : 'default'}
              disabled={!!unavailable || levels.length === 0}
              onChange={(e) => setEffort(e.target.value)}
              className="h-9 w-full rounded-[10px] bg-panel px-2.5 text-[13px] text-fg hairline disabled:opacity-60"
            >
              <option value="default">Default</option>
              {levels.map((l) => (
                <option key={l} value={l}>
                  {l[0]!.toUpperCase() + l.slice(1)}
                </option>
              ))}
            </select>
            <p className="mt-1 text-[11.5px] text-subtle">{levels.length === 0 ? 'This model has no effort setting.' : 'How hard Claude thinks before it answers.'}</p>
          </div>
          {plan ? null : (
          <div>
            <label htmlFor={ids.limit} className="mb-1.5 block text-[12.5px] font-medium text-muted">
              Spending limit per conversation
            </label>
            <div className={cx('flex h-9 items-center rounded-[10px] bg-panel px-2.5 hairline', !limitOk && 'shadow-[inset_0_0_0_1px_var(--danger)]')}>
              <span className="text-[13px] text-subtle">$</span>
              <input
                id={ids.limit}
                inputMode="decimal"
                value={limit}
                onChange={(e) => setLimit(e.target.value)}
                placeholder={s.budgetUsd.toFixed(2)}
                aria-invalid={!limitOk}
                aria-describedby={`${ids.limit}-help`}
                className="h-full min-w-0 flex-1 bg-transparent pl-1 text-[13px] text-fg outline-none placeholder:text-subtle"
              />
            </div>
            <p id={`${ids.limit}-help`} className={cx('mt-1 text-[11.5px]', limitOk ? 'text-subtle' : 'text-danger')}>
              {!limitOk ? 'Between $0.05 and $100.' : 'Spend on your API key before Claude stops.'}
            </p>
          </div>
          )}
        </div>
      ) : null}
      {s ? (
        <p className="mt-4 text-[12.5px] leading-relaxed text-muted">
          Each message can take up to {s.maxSteps} steps and {s.timeoutMinutes} minutes. Bigger models and higher effort{' '}
          {plan ? "use more of your Claude plan's usage." : 'cost more per message.'} A conversation can switch with /model and /effort. Limits and memory are
          under <a href="#claude-usage" className="text-accent underline-offset-2 hover:underline">usage and memory</a>.
        </p>
      ) : null}
      {s ? <SafeSteps on={s.safeSteps} disabled={!!unavailable} /> : null}
      {save.error ? <ErrorPanel error={save.error} compact className="mt-3" /> : null}
      {s ? (
        <div className="mt-4 flex items-center gap-3">
          <Button size="sm" variant="primary" disabled={!dirty || !limitOk || !effortOk || save.isPending || !!unavailable} loading={save.isPending} onClick={submit}>
            Save
          </Button>
          {saved ? (
            <span role="status" className="flex items-center gap-1 text-[12.5px] text-ok">
              <Check size={13} /> Saved. New messages use it.
            </span>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

/*
 * The computer's master switch for steps that run by themselves (D141, D186):
 * on, safe steps (checks of Claude's graphics, review copies, links the
 * person typed) start without a click and each person may turn on Auto in the
 * panel; off, nothing runs by itself and Auto isn't offered. Saved at once.
 */
function SafeSteps({ on, disabled }: { on: boolean; disabled: boolean }) {
  const save = useAssistantOp('assistant.settings.set');
  const id = useId();
  const value = save.isPending && save.variables?.safeSteps !== undefined ? !!save.variables.safeSteps : on;
  return (
    <div className="mt-4 rounded-[12px] bg-panel px-3.5 py-3 hairline">
      <label htmlFor={id} className="flex cursor-pointer items-start gap-3">
        <input
          id={id}
          type="checkbox"
          checked={value}
          disabled={disabled || save.isPending}
          onChange={(e) => save.mutate({ safeSteps: e.target.checked })}
          className="mt-[3px] h-4 w-4 shrink-0 accent-[var(--accent)]"
          data-safe-steps
        />
        <span className="min-w-0">
          <span className="block text-[13px] font-medium text-fg">Let Claude's steps run by themselves on this computer</span>
          <span className="mt-0.5 block text-[12.5px] leading-relaxed text-muted">
            The switch for everyone here. On: safe steps (checking a graphic Claude made, a review copy, a link someone typed in the conversation) start
            without a click, and each person can choose Ask first or Auto in the Claude Code panel. Auto also runs changes that can be undone, such as
            saving a version. Off: every step waits for a click and Auto isn't offered. Anything that costs money, final renders, approvals and deleting
            always wait for a person.
          </span>
        </span>
      </label>
      {save.error ? <ErrorPanel error={save.error} compact className="mt-2" /> : null}
    </div>
  );
}
