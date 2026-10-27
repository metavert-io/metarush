import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import { useDialog } from '../../ui/Dialog';
import { ErrorPanel, Segmented, Skeleton } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { OwnerOnly } from '../OwnerOnly';

/*
 * Before it goes out (D298), a Content Settings part: whether the videos of
 * every project using these settings wait for the quality check and their
 * sign-offs (always), only remind (never), or follow the workspace's choice.
 * Owners change it (content.gate.set; `metarush content gate`).
 */

export function GatePart({ projectId, readOnly }: { projectId: string; readOnly: boolean }) {
  const q = useOp('delivery.gate', { projectId });
  const qc = useQueryClient();
  const dialog = useDialog();
  const toast = useToast();
  const mode = q.data?.settings?.mode ?? 'workspace';
  const choose = async (v: string) => {
    if (v === mode) return;
    if (v !== 'on' && q.data?.on) {
      const ok = await dialog.confirm({
        title: 'Let these videos go out without waiting?',
        body: 'With the quality check and sign-offs only reminding, a version can go out with problems found or a sign-off still waiting.',
        confirmLabel: 'Change it',
        tone: 'danger',
      });
      if (!ok) return;
    }
    try {
      const out = await call('content.gate.set', { projectId, mode: v as 'on' | 'off' | 'workspace' });
      for (const k of ['delivery.gate', 'delivery.ready', 'content.show'] as const) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: out.summary });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: "Couldn't change it", body: [err.message, err.hint].filter(Boolean).join(' ') });
    }
  };
  if (q.isLoading) return <Skeleton className="h-12" />;
  if (q.error) return <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />;
  return (
    <div data-gate-part>
      <p className="mb-3 text-[12.5px] leading-relaxed text-muted">
        Whether a version of a project using these settings waits before it goes out to the client while the quality check found problems, hasn't checked it, or a sign-off
        is still waiting. A person can still deliver it anyway with a note.
      </p>
      <OwnerOnly note>
        <fieldset disabled={readOnly} className="m-0 border-0 p-0">
          <Segmented
            label="Before it goes out"
            className="max-w-full flex-wrap"
            value={mode}
            onChange={(v) => void choose(v)}
            items={[
              { value: 'workspace', label: 'As the workspace says' },
              { value: 'on', label: 'Hold versions back' },
              { value: 'off', label: 'Just remind' },
            ]}
          />
        </fieldset>
      </OwnerOnly>
      <p className="mt-2 text-[12.5px] text-fg">{q.data?.summary}</p>
    </div>
  );
}
