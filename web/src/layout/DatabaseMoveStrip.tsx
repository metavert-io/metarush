import { useNavigate } from 'react-router';
import { Database } from '@phosphor-icons/react';
import { useOp } from '../api/queries';
import { Spinner } from '../ui/primitives';

/*
 * While the workspace's database is being moved, or after it moved and this
 * computer still uses the old one (D335), every page says so under the top
 * bar: changes wait (or are refused here for good) and reading goes on.
 * Settings → Storage → Move the database has the rest.
 */
export function DatabaseMoveStrip() {
  const st = useOp('database.move.status', {}, {
    refetchInterval: (q) => (q.state.data?.frozen ? 3_000 : 60_000),
    staleTime: 2_000,
    retry: 6,
  });
  const go = useNavigate();
  const s = st.data;
  if (!s?.frozen) return null;
  const moving = s.state === 'moving';
  return (
    <div data-database-strip={s.state} role="status" className="border-t border-line bg-accent-soft">
      <div className="mx-auto flex min-h-10 w-full max-w-[1480px] items-center gap-2 px-4 py-1.5 text-[12.5px] sm:px-6 lg:px-8">
        <span className="grid h-5 w-5 shrink-0 place-items-center text-accent-text">{moving ? <Spinner size={12} /> : <Database size={15} />}</span>
        <span className="min-w-0 flex-1 text-fg">
          {moving
            ? 'The workspace is being moved to another database; changes wait until it’s done.'
            : s.canSwitch
              ? 'The workspace moved to another database: switch this computer too.'
              : s.summary}
        </span>
        <button
          type="button"
          onClick={() => go('/settings/storage#database')}
          className="shrink-0 rounded-[8px] px-2 py-1 font-medium text-accent-text hover:bg-hover"
          data-database-strip-open
        >
          {moving ? 'See how far it is' : 'Switch this computer'}
        </button>
      </div>
    </div>
  );
}
