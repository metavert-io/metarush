import { useNavigate } from 'react-router';
import { ArrowsLeftRight } from '@phosphor-icons/react';
import { useOp } from '../api/queries';

/**
 * A computer that handed its role to another one says so on every page
 * (D346): it works as a client — its background work and remote access for
 * the workspace are off — or it is about to step down once its own running
 * work is done. Nothing shows on a computer that is the host.
 */
export function HandoverStrip() {
  const st = useOp(
    'machine.handover.status',
    {},
    {
      refetchInterval: (q) => (q.state.data?.role.role === 'stepping-down' ? 5_000 : 120_000),
      staleTime: 5_000,
      retry: 2,
    },
  );
  const go = useNavigate();
  const s = st.data;
  if (!s || s.role.role === 'host') return null;
  return (
    <div data-handover-strip={s.role.role} role="status" className="border-t border-line bg-accent-soft">
      <div className="mx-auto flex min-h-10 w-full max-w-[1480px] items-center gap-2 px-4 py-1.5 text-[12.5px] sm:px-6 lg:px-8">
        <span className="grid h-5 w-5 shrink-0 place-items-center text-accent-text">
          <ArrowsLeftRight size={15} />
        </span>
        <span className="min-w-0 flex-1 text-fg">{s.role.says}</span>
        <button
          type="button"
          onClick={() => go('/settings/storage#handover')}
          className="shrink-0 rounded-[8px] px-2 py-1 font-medium text-accent-text hover:bg-hover"
          data-handover-strip-open
        >
          See more
        </button>
      </div>
    </div>
  );
}
