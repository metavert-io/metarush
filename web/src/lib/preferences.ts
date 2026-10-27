import { useOp } from '../api/queries';

/*
 * The signed-in person's preferences (D208, Settings → Preferences): whether
 * costs of outside AI services are estimated before edits (on by default, and
 * always when an owner asks everyone), their Ask first or Auto choice. The
 * server decides; this only reads it.
 */

export function usePreferences() {
  return useOp('account.preferences', {}, { staleTime: 30_000 });
}

/**
 * Whether edits that use paid outside AI services show their estimate and
 * wait for an OK first. Until the answer is in, they do (the safe default).
 */
export function useEstimatesFirst(): boolean {
  return usePreferences().data?.estimates ?? true;
}
