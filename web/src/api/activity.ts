import { useMemo } from 'react';
import { useOp } from './queries';
import type { ActivityItem, ActivityStep, AssetSummary } from './types';

/**
 * The whole-workspace activity summary (activity.summary). One query shared by
 * the activity bar, the upload tray and per-asset status chips; server events
 * invalidate it, and it also refreshes on a slow timer while anything is in flight.
 */
export function useActivity() {
  return useOp('activity.summary', {}, {
    staleTime: 2_000,
    refetchInterval: (q) => {
      const d = q.state.data;
      if (!d) return 15_000;
      return d.counts.active > 0 ? 4_000 : 30_000;
    },
  });
}

/** Activity items by asset ID (processing steps, waits) for chips anywhere an asset is shown. */
export function useAssetActivity(): Map<string, ActivityItem> {
  const act = useActivity();
  return useMemo(() => {
    const m = new Map<string, ActivityItem>();
    for (const it of act.data?.items ?? []) if (it.assetId && it.steps?.length) m.set(it.assetId, it);
    return m;
  }, [act.data]);
}

export type ChipState = 'done' | 'running' | 'queued' | 'paused' | 'failed' | 'canceled' | 'pending';

export interface StatusChip {
  key: string;
  label: string;
  state: ChipState;
  detail: string;
  pct?: number;
}

const chipKinds: { kind: string; label: string }[] = [
  { kind: 'proxy', label: 'Preview' },
  { kind: 'transcribe', label: 'Transcript' },
  { kind: 'evidence', label: 'Scene analysis' },
];

/**
 * Per-asset status chips: live job state from the activity summary when the
 * asset is being processed (running, paused for battery with the percentage,
 * failed), otherwise the asset's stored results.
 */
export function assetChips(a: AssetSummary, item?: ActivityItem): StatusChip[] {
  const steps = new Map<string, ActivityStep>((item?.steps ?? []).map((s) => [s.kind, s]));
  const out: StatusChip[] = [];
  for (const { kind, label } of chipKinds) {
    if (kind === 'proxy' && a.kind !== 'video') continue;
    if (kind === 'transcribe' && !a.hasAudio) continue;
    const s = steps.get(kind);
    if (s && s.state !== 'done') {
      const w = s.waiting;
      let detail: string = s.state;
      if (s.state === 'paused' && w) detail = w.batteryPercent != null ? `paused (battery ${w.batteryPercent}%)` : 'paused';
      else if (s.state === 'queued' && w && w.code !== 'starting') detail = w.text;
      else if (s.state === 'running') detail = s.machine ? `running on ${s.machine}` : 'running';
      else if (s.state === 'failed') detail = s.error ? `failed: ${s.error}` : 'failed';
      out.push({ key: kind, label, state: s.state as ChipState, detail, pct: s.pct });
      continue;
    }
    let state: ChipState = 'pending';
    if (kind === 'proxy') state = a.variants.includes('proxy') ? 'done' : a.analysis?.proxy === 'failed' ? 'failed' : 'pending';
    if (kind === 'transcribe') state = a.transcript?.state === 'available' ? 'done' : a.transcript?.state === 'failed' ? 'failed' : 'pending';
    if (kind === 'evidence') state = a.analysis?.evidence === 'available' ? 'done' : a.analysis?.evidence === 'failed' ? 'failed' : 'pending';
    if (s?.state === 'done') state = 'done';
    out.push({ key: kind, label, state, detail: state === 'done' ? 'ready' : state === 'failed' ? 'failed' : 'waiting to start' });
  }
  return out;
}
