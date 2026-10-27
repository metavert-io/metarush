import { useId, useState } from 'react';
import { Coins, HandPalm, Lightning, Moon, Receipt, Sun } from '@phosphor-icons/react';
import { Link } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { useOp, useOpMutation } from '../../api/queries';
import { useAssistantOp } from '../../api/assistant';
import type { PreferencesSetIn } from '../../api/types';
import { absolute, stamp } from '../../lib/format';
import { usePreferences } from '../../lib/preferences';
import { useTheme } from '../../lib/theme';
import { OwnerOnly, useOwnership } from '../../components/OwnerOnly';
import { autoWords } from '../../layout/assistant/ModeSwitch';
import { ErrorPanel, Skeleton, Theirs, cx } from '../../ui/primitives';

/*
 * Settings → Preferences (D208): each person's own choices. "Estimate costs
 * of outside AI services before edits" (on by default; an owner can ask it of
 * everyone), Ask first or Auto for Claude's changes (D186, the same choice as
 * the switch by the message box) and light or dark (kept in this browser).
 * Changing them is the person's own (people only); everyone sees the owners'
 * word on estimates.
 */

export function PreferencesSection() {
  return (
    <div className="space-y-6" data-preferences>
      <EstimatesCard />
      <div className="grid grid-cols-1 items-start gap-6 lg:grid-cols-2">
        <AssistantModeCard />
        <AutoSpendingCard />
        <AppearanceCard />
      </div>
    </div>
  );
}

function Card({ id, icon, title, children }: { id: string; icon: React.ReactNode; title: string; children: React.ReactNode }) {
  return (
    <section id={id} className="panel scroll-mt-28 p-5" aria-labelledby={`${id}-h`}>
      <h2 id={`${id}-h`} className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">{icon}</span>
        {title}
      </h2>
      {children}
    </section>
  );
}

/** One choice: a tick box with what it means under it. */
function Choice({ id, checked, disabled, onChange, title, children, data }: {
  id: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
  title: string;
  children: React.ReactNode;
  data?: Record<string, string>;
}) {
  return (
    <label htmlFor={id} className={cx('flex items-start gap-3 rounded-[12px] bg-panel px-3.5 py-3 hairline', disabled ? 'cursor-not-allowed' : 'cursor-pointer')}>
      <input id={id} type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} className="mt-[3px] h-4 w-4 shrink-0 accent-[var(--accent)]" {...data} />
      <span className="min-w-0">
        <span className={cx('block text-[13.5px] font-medium', disabled ? 'text-muted' : 'text-fg')}>{title}</span>
        <span className="mt-0.5 block text-[12.5px] leading-relaxed text-muted">{children}</span>
      </span>
    </label>
  );
}

function EstimatesCard() {
  const prefs = usePreferences();
  const own = useOwnership();
  const qc = useQueryClient();
  const ids = { mine: useId(), all: useId() };
  const save = useOpMutation('account.preferences.set', {
    onSuccess: (out) => {
      qc.setQueryData(['account.preferences', {}], out);
      void qc.invalidateQueries({ queryKey: ['assistant', 'mode'] }); // Auto's words follow the estimates
    },
  });
  const p = prefs.data;
  // The choice shows at once (in the same click) while it saves; the saved
  // answer replaces it, or it goes back if saving fails.
  const [choice, setChoice] = useState<PreferencesSetIn | null>(null);
  const change = (input: PreferencesSetIn) => {
    setChoice(input);
    save.mutate(input, { onSettled: () => setChoice(null) });
  };
  const mine = choice?.estimates ?? p?.estimatesYours ?? true;
  const required = choice?.requireEstimates ?? p?.estimatesRequired ?? false;
  return (
    <Card id="estimates" icon={<Coins size={17} />} title="Costs of outside AI services">
      <p className="mb-4 max-w-[78ch] text-[13px] leading-relaxed text-muted">
        Generated shots, the AI voice and clearer voices in the cloud are paid to outside services. An estimate says what an edit is expected to cost, about $0.40 for
        example, and opens to what it draws on: the service, its model, the clip, script lines or picture, and each part’s price.
      </p>
      {prefs.isLoading ? (
        <Skeleton className="h-20" />
      ) : prefs.error ? (
        <ErrorPanel error={prefs.error} compact onRetry={() => void prefs.refetch()} />
      ) : (
        <div className="space-y-3">
          <Choice
            id={ids.mine}
            checked={mine || required}
            disabled={required || save.isPending}
            onChange={(v) => change({ estimates: v })}
            title="Estimate costs of outside AI services before edits"
            data={{ 'data-estimates-mine': mine ? 'on' : 'off' }}
          >
            On (the usual): the estimate comes first and nothing is sent until you say yes. Off: paid steps run within your budgets, daily limits and project
            spending limits without asking about the price first, and with Auto on Claude’s changes that cost money run by themselves too — only where a person set
            the project’s budget and the team’s daily limit with that service (what Auto spends is on the project’s receipt, and the bell says each day). Consent,
            approvals and people-only steps always ask.
          </Choice>
          {required ? (
            <p className="rounded-[10px] bg-accent-soft px-3 py-2 text-[12.5px] leading-relaxed text-accent-text" data-estimates-required>
              {p?.requiredBy ? (
                <>
                  <Theirs>{p.requiredBy}</Theirs> asks everyone for estimates
                </>
              ) : (
                'An owner asks everyone for estimates'
              )}
              {p?.requiredAt ? (
                <>
                  {' '}
                  (since{' '}
                  <time dateTime={p.requiredAt} title={absolute(p.requiredAt)}>
                    {stamp(p.requiredAt)}
                  </time>
                  )
                </>
              ) : null}
              , so they come first for you too.{!mine ? ' Your own choice (off) counts again when they stop.' : ''}
            </p>
          ) : null}
          <OwnerOnly note={!own.owner}>
            <Choice
              id={ids.all}
              checked={required}
              disabled={save.isPending}
              onChange={(v) => change({ requireEstimates: v })}
              title="Ask everyone for estimates"
              data={{ 'data-estimates-required-box': required ? 'on' : 'off' }}
            >
              For everyone on this MetaRush, whatever they chose. Only an owner changes this.
            </Choice>
          </OwnerOnly>
          {save.error ? <ErrorPanel error={save.error} compact /> : null}
        </div>
      )}
    </Card>
  );
}

function AssistantModeCard() {
  const prefs = usePreferences();
  const set = useAssistantOp('assistant.mode.set');
  const qc = useQueryClient();
  const name = useId();
  const p = prefs.data;
  const offered = p?.autoOffered ?? false;
  // The choice shows at once while it saves (as the switch by the message box does).
  const [chosen, setChosen] = useState<'ask' | 'auto' | null>(null);
  const current = chosen ?? p?.assistantMode ?? 'ask';
  const choose = (mode: 'ask' | 'auto') => {
    if (mode === current) return;
    setChosen(mode);
    set.mutate(
      { mode },
      {
        onSuccess: (out) => {
          qc.setQueryData(['assistant', 'mode'], out);
          void qc.invalidateQueries({ queryKey: ['account.preferences'] });
        },
        onSettled: () => setChosen(null),
      },
    );
  };
  const option = (mode: 'ask' | 'auto', icon: React.ReactNode, title: string, body: string, disabled = false) => (
    <label className={cx('flex items-start gap-3 rounded-[12px] px-3.5 py-3 hairline', current === mode ? 'bg-accent-soft ring-1 ring-accent-line' : 'bg-panel', disabled ? 'cursor-not-allowed opacity-60' : 'cursor-pointer')}>
      <input type="radio" name={name} checked={current === mode} disabled={disabled || set.isPending} onChange={() => choose(mode)} className="mt-[3px] h-3.5 w-3.5 accent-[var(--accent)]" data-pref-mode={mode} />
      <span className="min-w-0">
        <span className="flex items-center gap-1.5 text-[13.5px] font-medium text-fg">
          <span className="text-subtle" aria-hidden>
            {icon}
          </span>
          {title}
        </span>
        <span className="mt-0.5 block text-[12.5px] leading-relaxed text-muted">{body}</span>
      </span>
    </label>
  );
  return (
    <Card id="assistant-mode" icon={<Lightning size={17} />} title="Claude’s changes for you">
      <p className="mb-4 text-[13px] leading-relaxed text-muted">The same choice as the switch by the message box in the Claude Code panel. It follows you to every browser and the command line.</p>
      {prefs.isLoading ? (
        <Skeleton className="h-24" />
      ) : (
        <div role="radiogroup" aria-label="How Claude's changes for you run" className="space-y-2">
          {option('ask', <HandPalm size={14} />, 'Ask first', 'Changes wait for your OK. Safe steps (checking a graphic, a review copy, a link you typed) run by themselves.')}
          {option('auto', <Lightning size={14} />, 'Auto', autoWords(p ? !p.estimates : false), !offered)}
          {!offered ? (
            <p className="text-[12px] leading-relaxed text-muted" data-auto-off>
              Nothing runs by itself on this computer, so Auto isn’t offered. An owner can change that in Settings → Claude Code.
            </p>
          ) : null}
          {set.error ? <ErrorPanel error={set.error} compact /> : null}
        </div>
      )}
    </Card>
  );
}

/** What Auto spent by itself for you today (D241): the bell's daily note opens here. */
function AutoSpendingCard() {
  const sp = useOp('assistant.spending', {}, { staleTime: 30_000 });
  const d = sp.data;
  const money = (usd: number) => (usd <= 0 ? 'nothing' : usd < 0.01 ? 'less than a cent' : `$${usd.toFixed(2)}`);
  return (
    <Card id="auto-spending" icon={<Receipt size={17} />} title="What Auto spends">
      {sp.isLoading ? (
        <Skeleton className="h-12" />
      ) : sp.error ? (
        <ErrorPanel error={sp.error} compact />
      ) : d ? (
        <div className="space-y-2 text-[13px]" data-auto-spending>
          <p className="leading-relaxed text-muted">{d.summary}</p>
          {d.today.length ? (
            <ul className="divide-y divide-line rounded-[12px] hairline">
              {d.today.map((x) => (
                <li key={x.id} className="flex flex-wrap items-baseline gap-x-2 px-3 py-2">
                  <Theirs className="min-w-0 flex-1">{x.what}</Theirs>
                  {x.projectId ? (
                    <Link to={`/p/${x.projectId}?tab=receipt`} className="text-[12.5px] text-accent-text hover:underline">
                      {d.projects?.[x.projectId] ? <Theirs>{d.projects[x.projectId]}</Theirs> : 'its project'}’s receipt
                    </Link>
                  ) : null}
                  <span className="text-[12px] text-subtle" title={absolute(x.at)}>
                    {stamp(x.at)}
                  </span>
                </li>
              ))}
            </ul>
          ) : null}
          {d.today.length ? (
            <p className="text-[12.5px] text-muted">
              Today: {d.todayPending ? `up to ${money(d.todayUpToUsd ?? 0)} (some of it is still being made)` : money(d.todayUsd)}.
            </p>
          ) : null}
        </div>
      ) : null}
    </Card>
  );
}

function AppearanceCard() {
  const [theme, setTheme] = useTheme();
  return (
    <Card id="appearance" icon={theme === 'dark' ? <Moon size={17} /> : <Sun size={17} />} title="Appearance">
      <div className="mt-3 flex gap-2" role="radiogroup" aria-label="Theme">
        {(['dark', 'light'] as const).map((t) => (
          <button
            key={t}
            type="button"
            role="radio"
            aria-checked={theme === t}
            onClick={() => setTheme(t)}
            className={cx(
              'flex h-10 flex-1 items-center justify-center gap-2 rounded-[10px] text-[13px] capitalize transition-colors hairline',
              theme === t ? 'bg-accent-soft text-accent-text ring-1 ring-accent-line' : 'text-muted hover:bg-hover',
            )}
          >
            {t === 'dark' ? <Moon size={15} /> : <Sun size={15} />} {t}
          </button>
        ))}
      </div>
      <p className="mt-2 text-[12px] text-subtle">Saved in this browser. The menu at the top right switches it too.</p>
    </Card>
  );
}
