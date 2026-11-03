import { useCallback, useMemo, useSyncExternalStore } from 'react';
import { useQueryClient, type QueryClient } from '@tanstack/react-query';
import type { OutputShape, ProjectShowOut, ProjectSummary, RevisionShowOut } from '../api/types';

/**
 * The shape the server says a version or a project is (D376, D378), from what
 * this page has loaded (a project's page, the project list, a version's page):
 * what a box shows while the rest loads, instead of a guess. It follows the
 * query cache, so it answers as soon as any of those arrive. Undefined while
 * nothing loaded says.
 */
export function useServedShape({ revisionId, projectId }: { revisionId?: string; projectId?: string }): OutputShape | undefined {
  const qc = useQueryClient();
  const subscribe = useCallback((onChange: () => void) => qc.getQueryCache().subscribe(onChange), [qc]);
  // A string snapshot, so React re-renders only when the answer changes.
  const key = useSyncExternalStore(subscribe, () => {
    const s = servedShape(qc, revisionId, projectId);
    return s ? `${s.width}x${s.height}` : '';
  });
  return useMemo(() => (key ? servedShape(qc, revisionId, projectId) : undefined), [key, qc, revisionId, projectId]);
}

function servedShape(qc: QueryClient, revisionId?: string, projectId?: string): OutputShape | undefined {
  if (revisionId) {
    for (const [, d] of qc.getQueriesData<RevisionShowOut>({ queryKey: ['revision.show'] })) {
      if (d?.summary?.id === revisionId && d.summary.shape?.width) return d.summary.shape;
    }
    for (const [, d] of qc.getQueriesData<ProjectShowOut>({ queryKey: ['project.show'] })) {
      const r = d?.revisions?.find((x) => x.id === revisionId);
      if (r?.shape?.width) return r.shape;
    }
  }
  if (projectId) {
    for (const [, d] of qc.getQueriesData<ProjectShowOut>({ queryKey: ['project.show'] })) {
      if (d?.project?.id === projectId && d.shape?.width) return d.shape;
    }
    for (const [, d] of qc.getQueriesData<{ projects?: ProjectSummary[] }>({ queryKey: ['project.list'] })) {
      const p = d?.projects?.find((x) => x.id === projectId);
      if (p?.headShape?.width) return p.headShape;
    }
  }
  return undefined;
}
