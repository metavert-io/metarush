import { useCallback } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../../api/client';
import { useActivityOpen } from '../../lib/workbench';
import { useToast } from '../../ui/Toast';

/** A folder on the computer MetaRush runs on, as people type it (a full path or ~/…). */
export function looksLikeFolder(text: string): boolean {
  const t = text.trim();
  return /^(~(\/|$)|\/[^/])/.test(t) && !t.includes('://');
}

/**
 * Brings a Remotion project into a project (remotion.import, D131): a folder
 * on this computer or a GitHub repository. The import runs as a job; the
 * project's Remotion panel under Sources shows it when it is ready.
 */
export function useRemotionImport() {
  const toast = useToast();
  const qc = useQueryClient();
  const [, setActivity] = useActivityOpen();
  return useCallback(
    async (from: string, projectId: string): Promise<boolean> => {
      try {
        const out = await call('remotion.import', { from: from.trim(), projectId });
        for (const k of ['remotion.list', 'job.list', 'activity.summary']) void qc.invalidateQueries({ queryKey: [k] });
        toast({
          title: out.message,
          body: 'Its videos show up under Sources when MetaRush has read them.',
          action: { label: 'Show progress', onClick: () => setActivity(true) },
          durationMs: 9000,
        });
        return true;
      } catch (e) {
        const err = toApiError(e);
        toast({ tone: 'error', title: 'Could not bring in that Remotion project', body: err.hint ? `${err.message} ${err.hint}` : err.message });
        return false;
      }
    },
    [toast, qc, setActivity],
  );
}
