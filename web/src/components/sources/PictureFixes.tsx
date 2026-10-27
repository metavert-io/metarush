import { useEffect, useState } from 'react';
import { useOp } from '../../api/queries';
import { ErrorPanel, Skeleton } from '../../ui/primitives';
import { FixesPanel, fixesBusy } from './FixesPanel';

/*
 * A picture's or logo's fixes (D152): remove the background, trim empty
 * edges, make it larger and sharper. A fix makes a new version of the
 * picture (the original version stays); cuts that pin the picture get it
 * as a new version of the cut, one click each.
 */
export function PictureFixes({ id, className }: { id: string; className?: string }) {
  const [live, setLive] = useState(false);
  const q = useOp('asset.fixes', { id }, { refetchInterval: live ? 2500 : false });
  useEffect(() => setLive(fixesBusy(q.data)), [q.data]);
  if (q.isLoading) return <Skeleton className="h-32" />;
  if (q.error || !q.data) return <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />;
  return <FixesPanel view={q.data} kindOf="picture" className={className} />;
}
