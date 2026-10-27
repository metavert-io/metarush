import { lazy, Suspense } from 'react';
import type { ProjectShowOut } from '../../api/types';
import { useInstallationNames } from '../../api/queries';
import { Skeleton } from '../../ui/primitives';

const CutEditor = lazy(() => import('../../components/studio/CutEditor').then((m) => ({ default: m.CutEditor })));

/** The current cut: watch it and edit it on its timeline (older versions are reviewed on their own pages). */
export function CutTab({ data, onOpenSource }: { data: ProjectShowOut; revisionId: string; onOpenSource: (assetId: string, t: number) => void }) {
  const machines = useInstallationNames();
  return (
    <Suspense
      fallback={
        <div className="grid grid-cols-1 gap-5 xl:grid-cols-[minmax(0,1.3fr)_minmax(360px,1fr)]">
          <Skeleton className="aspect-video w-full rounded-[14px]" />
          <Skeleton className="h-64 rounded-[14px]" />
          <Skeleton className="h-40 rounded-[14px] xl:col-span-2" />
        </div>
      }
    >
      <CutEditor data={data} onOpenSource={onOpenSource} workspaceInstallations={machines} />
    </Suspense>
  );
}
