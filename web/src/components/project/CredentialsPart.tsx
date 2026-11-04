import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import { useDialog } from '../../ui/Dialog';
import { ErrorPanel, Segmented, Skeleton } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { OwnerOnly } from '../OwnerOnly';

/*
 * Content Credentials (D418), a Content Settings part: whether the delivered
 * videos of every project using these settings carry them (always), always
 * and wait until they can be signed (required), never, or as the workspace
 * says. Owners change it (content.credentials.set; `metarush content
 * credentials`); asking less is a person's.
 */

const rank: Record<string, number> = { off: 0, on: 1, required: 2 };

export function CredentialsPart({ projectId, readOnly }: { projectId: string; readOnly: boolean }) {
  const q = useOp('credentials.status', { projectId });
  const qc = useQueryClient();
  const dialog = useDialog();
  const toast = useToast();
  const mode = q.data?.settings?.mode ?? 'workspace';
  const choose = async (v: string) => {
    if (v === mode) return;
    const after = v === 'workspace' ? (q.data?.workspace?.mode ?? 'off') : v;
    if ((rank[after] ?? 0) < (rank[q.data?.mode ?? 'off'] ?? 0)) {
      const ok = await dialog.confirm({
        title: 'Ask less of these videos’ Content Credentials?',
        body: after === 'off' ? 'Their delivered videos will go out without Content Credentials.' : 'A version that can’t be signed will go out without them instead of waiting.',
        confirmLabel: 'Change it',
        tone: 'danger',
      });
      if (!ok) return;
    }
    try {
      const out = await call('content.credentials.set', { projectId, mode: v as 'on' | 'off' | 'required' | 'workspace' });
      for (const k of ['credentials.status', 'delivery.ready', 'content.show'] as const) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: out.summary });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: "Couldn't change it", body: [err.message, err.hint].filter(Boolean).join(' ') });
    }
  };
  if (q.isLoading) return <Skeleton className="h-12" />;
  if (q.error) return <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />;
  return (
    <div data-credentials-part>
      <p className="mb-3 text-[12.5px] leading-relaxed text-muted">
        Whether the final videos of a project using these settings carry Content Credentials when they are delivered: a signed record of how each was made, which parts AI made or
        changed, and who signed it. Required: a version this computer can’t sign waits.
      </p>
      <OwnerOnly note>
        <fieldset disabled={readOnly} className="m-0 border-0 p-0">
          <Segmented
            label="Content Credentials"
            className="max-w-full flex-wrap"
            value={mode}
            onChange={(v) => void choose(v)}
            items={[
              { value: 'workspace', label: 'As the workspace says' },
              { value: 'on', label: 'On' },
              { value: 'required', label: 'Required' },
              { value: 'off', label: 'Off' },
            ]}
          />
        </fieldset>
      </OwnerOnly>
      <p className="mt-2 text-[12.5px] text-fg">{q.data?.summary}</p>
    </div>
  );
}
