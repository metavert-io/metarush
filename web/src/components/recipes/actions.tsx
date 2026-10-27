import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../../api/client';
import { money } from '../../lib/costs';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { RECIPE_QUERIES } from './api';

/*
 * What a person does with a run that waits or goes on (D204): Go on (people
 * only; when it asks to spend more, the styled question says how much and
 * going on agrees to exactly that) and Stop (work not started is called off;
 * what it made stays). The run's page, the To-dos card and the gallery use
 * these, so they all ask the same way. A run's sample (D294) is approved —
 * the rest of the run is planned then — or turned down with what isn't right.
 */

export interface RunRef {
  id: string;
  /** "Run 2 of “Garden story”" — how the question names it. */
  name: string;
  /** How much more it asks to spend (US dollars), when it does. */
  waitingUsd?: number;
}

export function runName(number: number, title: string): string {
  return `run ${number} of “${title}”`;
}

export function useRunActions() {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const [busy, setBusy] = useState<'go' | 'stop' | 'sample' | null>(null);
  const refresh = () => {
    for (const k of RECIPE_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  };
  const agree = (name: string, usd: number) =>
    dialog.confirm({
      title: `Spend ${money(usd)} more on ${name}?`,
      body: `It costs ${money(usd)} more than you agreed to. Going on agrees to that much more; if it needs still more later, it stops and asks you again. Budgets and today’s limits still hold.`,
      confirmLabel: `Agree to ${money(usd)} more`,
    });

  /** Lets a run that waits for a person go on; resolves true when it did. */
  const goOn = async (r: RunRef): Promise<boolean> => {
    let more = r.waitingUsd && r.waitingUsd > 0 ? r.waitingUsd : undefined;
    if (more && !(await agree(r.name, more))) return false;
    setBusy('go');
    try {
      for (let round = 0; ; round++) {
        try {
          const out = await call('recipe.continue', { run: r.id, ...(more ? { moreUsd: more } : {}) });
          refresh();
          toast({ tone: 'success', title: out.message });
          return true;
        } catch (e) {
          const err = toApiError(e);
          const asks = (err.details as { waitingUsd?: number } | undefined)?.waitingUsd;
          // It asks to spend more than the page said (it changed meanwhile): ask about that amount.
          if (err.code === 'E_CONFIRM_REQUIRED' && asks && round === 0) {
            if (!(await agree(r.name, asks))) return false;
            more = asks;
            continue;
          }
          throw e;
        }
      }
    } catch (e) {
      const err = toApiError(e);
      refresh();
      toast({ tone: 'error', title: `${capFirst(r.name)} didn’t go on`, body: [err.message, err.hint].filter(Boolean).join(' ') });
      return false;
    } finally {
      setBusy(null);
    }
  };

  /** Stops a run after asking; what it made stays. */
  const stop = async (r: RunRef): Promise<boolean> => {
    const ok = await dialog.confirm({
      title: `Stop ${r.name}?`,
      body: 'Work it asked for that hasn’t started is called off. What it made stays: its project, its versions and the clips it made.',
      confirmLabel: 'Stop the run',
      tone: 'danger',
    });
    if (!ok) return false;
    setBusy('stop');
    try {
      const out = await call('recipe.stop', { run: r.id });
      refresh();
      toast({ tone: 'info', title: out.message });
      return true;
    } catch (e) {
      const err = toApiError(e);
      refresh();
      toast({ tone: 'error', title: `${capFirst(r.name)} didn’t stop`, body: [err.message, err.hint].filter(Boolean).join(' ') });
      return false;
    } finally {
      setBusy(null);
    }
  };

  /** Approves a run's sample (people only): the rest is planned, and asks for its agreement. */
  const approveSample = async (r: RunRef): Promise<boolean> => {
    const ok = await dialog.confirm({
      title: `Approve the sample of ${r.name}?`,
      body: 'The rest of the run is planned then, at today’s prices, and you agree to what it costs before it goes on. The sample’s line and shot are used again, not paid twice.',
      confirmLabel: 'Approve the sample',
    });
    if (!ok) return false;
    return decideSample(r, { decision: 'approve' });
  };

  /** Says the sample isn't right: the run stops, with the person's words. */
  const notQuite = async (r: RunRef): Promise<boolean> => {
    const note = await dialog.prompt({
      title: 'What isn’t right about the sample?',
      body: 'The run stops; what it made stays. Plan it again with other inputs — another voice, other words.',
      label: 'What isn’t right',
      placeholder: 'The voice sounds too bright for this show',
      required: true,
      maxLength: 500,
      multiline: true,
      confirmLabel: 'Stop the run',
      tone: 'danger',
    });
    if (!note?.trim()) return false;
    return decideSample(r, { decision: 'not-quite', note: note.trim() });
  };

  const decideSample = async (r: RunRef, d: { decision: 'approve' | 'not-quite'; note?: string }): Promise<boolean> => {
    setBusy('sample');
    try {
      const out = await call('recipe.sample', { run: r.id, ...d });
      refresh();
      toast({ tone: d.decision === 'approve' ? 'success' : 'info', title: out.message, body: out.next || undefined, durationMs: 7000 });
      return true;
    } catch (e) {
      const err = toApiError(e);
      refresh();
      toast({ tone: 'error', title: `The sample of ${r.name} wasn’t decided`, body: [err.message, err.hint].filter(Boolean).join(' ') });
      return false;
    } finally {
      setBusy(null);
    }
  };

  return { goOn, stop, approveSample, notQuite, busy };
}

function capFirst(s: string): string {
  return s ? s[0]!.toUpperCase() + s.slice(1) : s;
}
