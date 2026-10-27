import { useOp } from '../api/queries';

/** How many motion graphic versions wait for a person (D68): on the Library link and its Motion tab. */
export function MotionBadge() {
  const list = useOp('motion.list', { state: 'waiting' }, { staleTime: 60_000 });
  const n = list.data?.waiting ?? 0;
  if (!n) return null;
  return (
    <span
      className="tc inline-grid h-[18px] min-w-[18px] place-items-center rounded-full bg-accent px-1 text-[10.5px] font-semibold text-accent-ink"
      aria-label={`${n} motion ${n === 1 ? 'graphic waits' : 'graphics wait'} for you`}
      data-motion-badge
    >
      {n}
    </span>
  );
}
