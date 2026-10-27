import { useEffect, useId, useRef, useState } from 'react';
import { useLocation } from 'react-router';
import { Check, Gauge } from '@phosphor-icons/react';
import { useAssistantOp, useAssistantSettings, useAssistantStatus } from '../../api/assistant';
import type { OpOutput } from '../../api/types';
import { Button, ErrorPanel, Skeleton, cx } from '../../ui/primitives';

type Settings = OpOutput<'assistant.settings'>;
type Plan = NonNullable<Settings['planUsage']>;

/*
 * Settings → Claude Code (Usage and memory) (D181). A conversation is limited in
 * tokens (everything Claude reads and writes for it, mostly the conversation
 * read again at each step), long conversations are summarized so each message
 * reads less, the prompt cache can last an hour, and clicks on Claude's cards
 * can wait for the person's next message. The Claude plan's own usage, as
 * Claude Code last reported it, is shown at the top.
 */
export function UsageSettings() {
  const status = useAssistantStatus();
  const settings = useAssistantSettings();
  const save = useAssistantOp('assistant.settings.set');
  const loc = useLocation();
  const ref = useRef<HTMLElement>(null);
  const ids = { limit: useId(), at: useId(), auto: useId() };
  const s = settings.data;
  const [limit, setLimit] = useState('');
  const [at, setAt] = useState('');
  const [auto, setAuto] = useState('');
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    if (!s) return;
    setLimit(String(s.tokenLimitMillions));
    setAt(String(Math.round(s.compactAtTokens / 1000)));
    setAuto(String(Math.round(s.autoCompactTokens / 1000)));
  }, [s]);
  useEffect(() => {
    if (loc.hash === '#claude-usage') ref.current?.scrollIntoView({ block: 'start' });
  }, [loc.hash]);
  useEffect(() => {
    if (!saved) return;
    const t = setTimeout(() => setSaved(false), 2500);
    return () => clearTimeout(t);
  }, [saved]);

  const num = (v: string) => (v.trim() === '' ? NaN : Number(v.replace(/,/g, '')));
  const limitN = num(limit);
  const atN = num(at);
  const autoN = num(auto);
  const limitOk = Number.isFinite(limitN) && limitN >= 1 && limitN <= 1000;
  const atOk = Number.isFinite(atN) && atN >= 50 && atN <= 900;
  const autoOk = Number.isFinite(autoN) && autoN >= 100 && autoN <= 1000;
  const dirty =
    !!s &&
    (limitN !== s.tokenLimitMillions || Math.round(atN * 1000) !== s.compactAtTokens || Math.round(autoN * 1000) !== s.autoCompactTokens);
  const unavailable = status.data && !status.data.available;
  const submit = () =>
    save.mutate(
      { tokenLimitMillions: limitN, compactAtTokens: Math.round(atN * 1000), autoCompactTokens: Math.round(autoN * 1000) },
      { onSuccess: () => setSaved(true) },
    );

  return (
    <section ref={ref} id="claude-usage" className="panel scroll-mt-24 p-5" aria-labelledby="claude-usage-h" data-usage-settings>
      <h2 id="claude-usage-h" className="flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <Gauge size={17} />
        </span>
        Claude Code: usage and memory
      </h2>
      <p className="mt-1 text-[13px] leading-relaxed text-muted">
        Every step Claude takes reads the whole conversation again, so long conversations use the most. These settings keep that in check. Counted in tokens,
        the pieces of text Claude reads and writes.
      </p>
      {settings.isLoading ? (
        <div className="mt-4 space-y-2">
          <Skeleton className="h-9" />
          <Skeleton className="h-9" />
        </div>
      ) : settings.error ? (
        <ErrorPanel error={settings.error} compact className="mt-4" onRetry={() => void settings.refetch()} />
      ) : s ? (
        <>
          {s.planUsage ? <PlanBars plan={s.planUsage} /> : s.billing === 'plan' ? (
            <p className="mt-3 text-[12.5px] text-subtle">Your Claude plan's usage shows here after Claude's next message.</p>
          ) : null}
          <div className="mt-4 grid grid-cols-1 gap-4 md:grid-cols-3">
            <NumberField
              id={ids.limit}
              label="Limit per conversation"
              value={limit}
              onChange={setLimit}
              unit="million tokens"
              ok={limitOk}
              help={limitOk ? 'Claude stops a conversation here; /limit in the message box changes one conversation.' : 'Between 1 and 1,000.'}
              data="limit"
            />
            <NumberField
              id={ids.at}
              label="Summarize when its memory passes"
              value={at}
              onChange={setAt}
              unit="thousand tokens"
              ok={atOk}
              disabled={!s.compact}
              help={atOk ? 'Right after a reply, so the next message starts small.' : 'Between 50 and 900.'}
              data="compact-at"
            />
            <NumberField
              id={ids.auto}
              label="Claude Code's own summary point"
              value={auto}
              onChange={setAuto}
              unit="thousand tokens"
              ok={autoOk}
              help={autoOk ? 'A backstop inside one long message.' : 'Between 100 and 1,000.'}
              data="autocompact"
            />
          </div>
          <div className="mt-4 flex items-center gap-3">
            <Button size="sm" variant="primary" disabled={!dirty || !limitOk || !atOk || !autoOk || save.isPending || !!unavailable} loading={save.isPending} onClick={submit}>
              Save
            </Button>
            {saved ? (
              <span role="status" className="flex items-center gap-1 text-[12.5px] text-ok">
                <Check size={13} /> Saved. New messages use it.
              </span>
            ) : null}
          </div>
          {save.error ? <ErrorPanel error={save.error} compact className="mt-3" /> : null}
          <div className="mt-4 space-y-2">
            <Toggle
              setting="compact"
              on={s.compact}
              disabled={!!unavailable}
              title="Summarize long conversations"
              body="Claude writes a short summary of the conversation and carries on from it. The project, its cuts, versions and notes stay in MetaRush, so nothing you made is lost; the conversation shows in full here."
            />
            <Toggle
              setting="cacheHour"
              on={s.cacheHour}
              disabled={!!unavailable}
              title="Keep Claude's memory ready for an hour"
              body="Without it, a pause of five minutes (watching a render) makes the next message read everything again at full cost. Keeping it an hour costs a little more each time it is saved."
            />
            <Toggle
              setting="holdNews"
              on={s.holdNews}
              disabled={!!unavailable}
              title="Tell Claude about your clicks with your next message"
              body="Saving, rendering or setting aside one of Claude's cards waits for your next message instead of starting a message of its own. Safe steps that ran by themselves and anything that failed still reach Claude at once."
            />
          </div>
        </>
      ) : null}
    </section>
  );
}

function NumberField(props: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
  unit: string;
  ok: boolean;
  help: string;
  disabled?: boolean;
  data: string;
}) {
  return (
    <div>
      <label htmlFor={props.id} className="mb-1.5 block text-[12.5px] font-medium text-muted">
        {props.label}
      </label>
      <div className={cx('flex h-9 items-center rounded-[10px] bg-panel px-2.5 hairline', !props.ok && 'shadow-[inset_0_0_0_1px_var(--danger)]', props.disabled && 'opacity-60')}>
        <input
          id={props.id}
          inputMode="decimal"
          value={props.value}
          disabled={props.disabled}
          onChange={(e) => props.onChange(e.target.value)}
          aria-invalid={!props.ok}
          aria-describedby={`${props.id}-help`}
          className="h-full min-w-0 flex-1 bg-transparent text-[13px] text-fg outline-none"
          data-usage-field={props.data}
        />
        <span className="shrink-0 pl-1 text-[12px] text-subtle">{props.unit}</span>
      </div>
      <p id={`${props.id}-help`} className={cx('mt-1 text-[11.5px]', props.ok ? 'text-subtle' : 'text-danger')}>
        {props.help}
      </p>
    </div>
  );
}

/* A setting saved at once, like Safe steps. */
function Toggle({ setting, on, disabled, title, body }: { setting: 'compact' | 'cacheHour' | 'holdNews'; on: boolean; disabled: boolean; title: string; body: string }) {
  const save = useAssistantOp('assistant.settings.set');
  const id = useId();
  // What was clicked shows until the settings come back saying so (they
  // reload a moment after the save finishes).
  const [local, setLocal] = useState<boolean | null>(null);
  useEffect(() => {
    if (local !== null && local === on) setLocal(null);
  }, [on, local]);
  const value = local ?? on;
  return (
    <div className="rounded-[12px] bg-panel px-3.5 py-3 hairline">
      <label htmlFor={id} className="flex cursor-pointer items-start gap-3">
        <input
          id={id}
          type="checkbox"
          checked={value}
          disabled={disabled || save.isPending}
          onChange={(e) => {
            const v = e.target.checked;
            setLocal(v);
            save.mutate({ [setting]: v }, { onError: () => setLocal(null) });
          }}
          className="mt-[3px] h-4 w-4 shrink-0 accent-[var(--accent)]"
          data-usage-toggle={setting}
        />
        <span className="min-w-0">
          <span className="block text-[13px] font-medium text-fg">{title}</span>
          <span className="mt-0.5 block text-[12.5px] leading-relaxed text-muted">{body}</span>
        </span>
      </label>
      {save.error ? <ErrorPanel error={save.error} compact className="mt-2" /> : null}
    </div>
  );
}

/* The Claude plan's own usage: how much of each window is used. */
function PlanBars({ plan }: { plan: Plan }) {
  const rows: { label: string; used: number; resets: string }[] = [];
  const when = (iso: string) => new Date(iso).toLocaleString(undefined, { weekday: 'short', hour: 'numeric', minute: '2-digit' });
  if (plan.sevenDay) rows.push({ label: 'This week', used: plan.sevenDay.used, resets: when(plan.sevenDay.resetsAt) });
  if (plan.fiveHour) rows.push({ label: 'Last five hours', used: plan.fiveHour.used, resets: when(plan.fiveHour.resetsAt) });
  if (rows.length === 0) return null;
  return (
    <div className="mt-4 rounded-[12px] bg-panel px-3.5 py-3 hairline" data-plan-usage>
      <div className="text-[12.5px] font-medium text-muted">Your Claude plan</div>
      <div className="mt-2 space-y-2">
        {rows.map((r) => {
          const pct = Math.max(0, Math.min(100, Math.round(r.used * 100)));
          return (
            <div key={r.label}>
              <div className="flex items-baseline justify-between text-[12.5px]">
                <span className="text-fg">{r.label}</span>
                <span className="text-muted">
                  {pct}% used · starts again {r.resets}
                </span>
              </div>
              <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-[var(--hairline)]" role="meter" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label={`${r.label}: ${pct}% used`}>
                <div className={cx('h-full rounded-full', pct >= 90 ? 'bg-[var(--danger)]' : pct >= 75 ? 'bg-[var(--warn,var(--accent))]' : 'bg-[var(--accent)]')} style={{ width: `${pct}%` }} />
              </div>
            </div>
          );
        })}
      </div>
      <p className="mt-2 text-[11.5px] text-subtle">As Claude Code last reported it, after Claude's latest message on this machine.</p>
    </div>
  );
}
