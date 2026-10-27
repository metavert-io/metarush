import { useMemo } from 'react';
import {
  QueryClient,
  QueryCache,
  MutationCache,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryKey,
  type UseQueryOptions,
} from '@tanstack/react-query';
import { ApiError, call, getSession, getStatus, toApiError } from './client';
import type { OpInput, OpOutput, Peaks, RemoteOp } from './types';

export function createQueryClient(): QueryClient {
  return new QueryClient({
    queryCache: new QueryCache(),
    mutationCache: new MutationCache(),
    defaultOptions: {
      queries: {
        staleTime: 15_000,
        gcTime: 5 * 60_000,
        refetchOnWindowFocus: true,
        retry: (count, err) => {
          const e = toApiError(err);
          if (e.isSignedOut || e.code === 'E_NOT_FOUND' || e.code === 'E_INVALID_INPUT' || e.code === 'E_UNAUTHORIZED') return false;
          return count < 2;
        },
        retryDelay: (n) => Math.min(800 * 2 ** n, 6000),
      },
      mutations: { retry: false },
    },
  });
}

/** Query keys are always [opName, input], so SSE can invalidate by op prefix. */
export const opKey = <K extends RemoteOp>(name: K, input?: OpInput<K>): QueryKey =>
  input === undefined ? [name] : [name, input];

type OpQueryOptions<K extends RemoteOp, T> = Omit<UseQueryOptions<OpOutput<K>, ApiError, T>, 'queryKey' | 'queryFn'>;

/** `useOp('project.show', { id })`; pass `null` input to disable. */
export function useOp<K extends RemoteOp, T = OpOutput<K>>(name: K, input: OpInput<K> | null, options?: OpQueryOptions<K, T>) {
  return useQuery<OpOutput<K>, ApiError, T>({
    queryKey: [name, input ?? null],
    queryFn: ({ signal }) => call(name, input as OpInput<K>, signal),
    ...options,
    enabled: input !== null && (options?.enabled ?? true),
  });
}

export function useOpMutation<K extends RemoteOp>(
  name: K,
  opts: { invalidate?: RemoteOp[]; onSuccess?: (out: OpOutput<K>, input: OpInput<K>) => void } = {},
) {
  const qc = useQueryClient();
  return useMutation<OpOutput<K>, ApiError, OpInput<K>>({
    mutationFn: (input) => call(name, input),
    onSuccess: (out, input) => {
      for (const op of opts.invalidate ?? []) void qc.invalidateQueries({ queryKey: [op] });
      opts.onSuccess?.(out, input);
    },
  });
}

export function useStatus() {
  return useQuery({ queryKey: ['status'], queryFn: ({ signal }) => getStatus(signal), refetchInterval: 20_000, staleTime: 5_000 });
}

export function useSession() {
  return useQuery({
    queryKey: ['session'],
    queryFn: ({ signal }) => getSession(signal),
    staleTime: 5 * 60_000,
    // Signed out or daemon down: show the gate at once (it re-checks on its own).
    retry: (n, err) => {
      const e = toApiError(err);
      return !e.isSignedOut && !e.isUnreachable && n < 1;
    },
  });
}

/** Waveform peaks for an asset (static once produced). */
export function usePeaks(assetId: string | undefined, enabled = true) {
  return useQuery<Peaks, ApiError>({
    queryKey: ['peaks', assetId],
    // Plain fetch (not request()): media may redirect to a presigned object-store URL,
    // and a CORS failure there must not flag the daemon as offline.
    queryFn: async ({ signal }) => {
      const res = await fetch(`/media/assets/${encodeURIComponent(assetId ?? '')}/peaks`, { credentials: 'same-origin', signal });
      const body: unknown = await res.json().catch(() => null);
      if (!res.ok || !body || typeof body !== 'object' || !Array.isArray((body as Peaks).peaks)) {
        const err = (body as { error?: { code: string; message: string; hint?: string } } | null)?.error;
        throw new ApiError(err ?? { code: 'E_NOT_FOUND', message: 'No waveform yet' }, res.status);
      }
      return body as Peaks;
    },
    enabled: !!assetId && enabled,
    staleTime: Infinity,
    retry: false,
  });
}

/** installationId → machine name, for "made on <machine>" labels. */
export function useInstallationNames(): Map<string, string> {
  const ws = useQuery({
    queryKey: ['workspace.show', {}],
    queryFn: ({ signal }) => call('workspace.show', {}, signal),
    staleTime: 5 * 60_000,
  });
  const list = ws.data?.installations ?? [];
  const key = list.map((i) => i.id + i.name).join('|');
  return useMemo(() => new Map(list.map((i) => [i.id, i.name || i.hostname])), [key]); // eslint-disable-line react-hooks/exhaustive-deps
}
