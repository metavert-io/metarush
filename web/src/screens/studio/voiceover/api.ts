import { useCallback } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError, type ApiError } from '../../../api/client';
import type { AppliedChange, EditApplyOut, PlanResult, RemoteOp } from '../../../api/types';
import { useToast } from '../../../ui/Toast';

/*
 * Every change on the Voiceover tab is one small, checked edit plan that
 * becomes a new version of the cut (D62): the ops build and check it on the
 * server (the same ones the CLI and Claude's cards use), the tab applies it.
 * The mixer is the one place that tries a change first (mix.preview) and
 * saves it once.
 */

/** Answers that change when the cut changes. */
export const CUT_QUERIES: RemoteOp[] = [
  'project.show',
  'project.list',
  'revision.list',
  'revision.show',
  'revision.plan',
  'preview.prepare',
  'voiceover.show',
  'voiceover.takes',
  'voiceover.estimate',
  'mix.show',
  'sound.show',
  'sound.compare',
  'cut.show',
  'cut.compare',
  'conversation.list',
  'conversation.show',
  'activity.summary',
  'provider.status',
];

export function useRefreshCut() {
  const qc = useQueryClient();
  return useCallback(() => {
    for (const k of CUT_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  }, [qc]);
}

/** A change that did not make it into the cut, in plain words. */
export class ChangeRefused extends Error {}

/** Applies a checked plan (the dashboard confirms in its own UI, so no token is needed). */
export async function applyPlan(res: PlanResult | undefined | null): Promise<EditApplyOut> {
  if (!res) throw new ChangeRefused('Nothing to change.');
  if (res.check && !res.check.valid) {
    throw new ChangeRefused(res.check.errors?.[0]?.message ?? 'This change cannot be made yet.');
  }
  return call('edit.apply', { plan: res.plan as unknown as Record<string, unknown> });
}

/** The version an op with apply made, or why it could not be put in the cut. */
export function checkApplied(ap: AppliedChange | undefined): AppliedChange {
  if (!ap) throw new ChangeRefused('The change was not put in the cut.');
  if (ap.problem) throw new ChangeRefused(ap.problem);
  return ap;
}

export function errorText(e: unknown): string {
  if (e instanceof ChangeRefused) return e.message;
  const err: ApiError = toApiError(e);
  return err.message;
}

/**
 * Runs a change and reports it: a short success toast (or the problem in plain
 * words), then refreshes everything that shows the cut.
 */
export function useChange() {
  const toast = useToast();
  const refresh = useRefreshCut();
  return useCallback(
    async <T,>(run: () => Promise<T>, done: string | ((out: T) => string), failed = 'That change was not made'): Promise<T | null> => {
      try {
        const out = await run();
        refresh();
        const title = typeof done === 'function' ? done(out) : done;
        if (title) toast({ tone: 'success', title });
        return out;
      } catch (e) {
        refresh();
        toast({ tone: 'error', title: failed, body: errorText(e) });
        return null;
      }
    },
    [refresh, toast],
  );
}

/** Seconds as m:ss.s ("0:12.4"). */
export function clock(s: number): string {
  const v = Math.max(0, s);
  const m = Math.floor(v / 60);
  const rest = v - m * 60;
  return `${m}:${rest.toFixed(1).padStart(4, '0')}`;
}

/** Reads m:ss.s, ss.s or h:mm:ss into seconds (null when it is not a time). */
export function parseClock(v: string): number | null {
  const t = v.trim().replace(/s$/, '');
  if (!t) return null;
  const parts = t.split(':');
  if (parts.length > 3) return null;
  let total = 0;
  for (let i = 0; i < parts.length; i++) {
    const x = Number(parts[i]);
    if (!Number.isFinite(x) || x < 0 || (i > 0 && x >= 60)) return null;
    total = total * 60 + x;
  }
  return Math.round(total * 1000) / 1000;
}
