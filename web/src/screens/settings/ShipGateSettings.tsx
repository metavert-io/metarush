import { HandPalm } from '@phosphor-icons/react';
import { useOp, useOpMutation } from '../../api/queries';
import { relative } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { ErrorPanel, Segmented, Skeleton } from '../../ui/primitives';
import { OwnerOnly } from '../../components/OwnerOnly';

/*
 * Settings → General → Before it goes out (D298): whether versions wait for
 * the quality check and their sign-offs before they go out to a client. The
 * workspace's choice (owners; on for new workspaces, asked once in older ones);
 * a client's Content Settings can say otherwise for their projects.
 */

export function ShipGateSettings() {
  const gate = useOp('delivery.gate', {});
  const set = useOpMutation('delivery.gate.set', { invalidate: ['delivery.gate', 'delivery.ready', 'todos.list'] });
  const dialog = useDialog();
  const toast = useToast();
  const ws = gate.data?.workspace;
  const value = !ws ? 'undecided' : ws.on ? 'on' : 'off';
  const choose = async (v: string) => {
    if (v === value || v === 'undecided') return;
    const on = v === 'on';
    if (!on) {
      const ok = await dialog.confirm({
        title: 'Only remind about the quality check and sign-offs?',
        body: 'Versions can then go out with problems the quality check found, or while a sign-off still waits. The delivery record still says what would have held them.',
        confirmLabel: 'Only remind',
        tone: 'danger',
      });
      if (!ok) return;
    }
    set.mutate(
      { on },
      {
        onSuccess: (o) => toast({ tone: 'success', title: o.summary }),
        onError: (e) => toast({ tone: 'error', title: "Couldn't change it", body: [e.message, e.hint].filter(Boolean).join(' ') }),
      },
    );
  };
  return (
    <section id="ship-gate" className="panel scroll-mt-28 p-5" aria-labelledby="ship-gate-h" data-ship-gate-settings>
      <h2 id="ship-gate-h" className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <HandPalm size={17} />
        </span>
        Before it goes out
      </h2>
      <p className="mb-4 max-w-[78ch] text-[12.5px] leading-relaxed text-muted">
        Hold a version back from going out to a client while the quality check found problems, hasn't checked it, or a sign-off is still waiting. Things worth checking
        never hold it, and a person can still deliver it anyway with a note, which is kept and shows on the receipt. A client's Content Settings can say otherwise for
        their projects.
      </p>
      {gate.error ? <ErrorPanel compact error={gate.error} onRetry={() => void gate.refetch()} /> : null}
      {gate.isLoading ? (
        <Skeleton className="h-12" />
      ) : (
        <OwnerOnly note className="space-y-3">
          <Segmented
            label="Before it goes out"
            className="max-w-full flex-wrap"
            value={value}
            onChange={(v) => void choose(v)}
            items={[
              { value: 'on', label: 'Hold versions back' },
              { value: 'off', label: 'Just remind me' },
            ]}
          />
          <p className="text-[12.5px] text-fg" data-ship-gate-setting>
            {gate.data?.summary}
            {ws ? (
              <span className="text-subtle">
                {' '}
                Chosen by {ws.updatedBy.userName || 'a person'} {relative(ws.updatedAt)}.
              </span>
            ) : null}
          </p>
          {(gate.data?.warnings ?? []).map((w) => (
            <p key={w} className="text-[12.5px] text-info" data-ship-gate-warning>
              {w}
            </p>
          ))}
        </OwnerOnly>
      )}
    </section>
  );
}
