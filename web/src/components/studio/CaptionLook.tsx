import { useId, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import lookCatalog from '@metarush/schema/schemas/caption-looks.json';
import { call, toApiError } from '../../api/client';
import type { ops } from '@metarush/schema';
import type { Spec } from '../../api/types';
import { useToast } from '../../ui/Toast';

/** One vetted caption look (D245; schema/caption-looks.json, written by Go). */
export type CaptionLookChoice = { id: string; name: string; summary: string; wordTimed?: boolean };

export const CAPTION_LOOKS: CaptionLookChoice[] = (lookCatalog as { looks: CaptionLookChoice[] }).looks;

/** A look's name ("Big and bold"); an unknown or missing one is the first, Clean. */
export const captionLookName = (id: string | undefined): string => (CAPTION_LOOKS.find((l) => l.id === id) ?? CAPTION_LOOKS[0]!).name;

/**
 * How the cut's captions look, under the player (D245): one of the vetted
 * looks, or off. Choosing one saves a new version with `cut.captions` (the
 * same change as `metarush cut captions <project> --look <look>` or a cut
 * sheet's `captions:` line); Undo puts the captions back as they were.
 */
export function CaptionLookPicker({ spec, projectId, disabled }: { spec: Spec; projectId: string; disabled?: boolean }) {
  const id = useId();
  const qc = useQueryClient();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const cap = spec.captions;
  const now = cap?.enabled ? (cap.style ?? 'clean') : 'off';
  const refresh = () => {
    for (const key of ['project.show', 'project.list', 'revision.list', 'revision.show', 'cut.show', 'revision.plan']) void qc.invalidateQueries({ queryKey: [key] });
  };
  const choose = async (value: string) => {
    if (value === now) return;
    setBusy(true);
    try {
      const out = await call('cut.captions', value === 'off' ? { projectId, on: false } : { projectId, look: value as ops.CutCaptionsIn['look'], on: true });
      refresh();
      toast({
        tone: 'success',
        title: value === 'off' ? 'Captions off' : `Captions: ${captionLookName(value)}`,
        body: out.applied ? `Saved as version ${out.applied.revision.number}.` : out.summary,
        action: out.applied
          ? {
              label: 'Undo',
              onClick: () =>
                void call('edit.apply', {
                  plan: { baseRevisionId: out.applied!.revision.id, origin: 'captions:studio', instruction: 'Undo the captions change', ops: [{ op: 'setCaptions', captions: cap ?? { enabled: false } }] },
                })
                  .then(() => {
                    refresh();
                    toast({ title: 'Captions put back' });
                  })
                  .catch((e: unknown) => toast({ tone: 'error', title: 'Not put back', body: toApiError(e).message })),
            }
          : undefined,
      });
    } catch (e) {
      const ae = toApiError(e);
      toast({ tone: 'error', title: 'Captions not changed', body: [ae.message, ae.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(false);
    }
  };
  const summary = CAPTION_LOOKS.find((l) => l.id === now)?.summary;
  return (
    <label htmlFor={id} className="flex basis-full flex-wrap items-center justify-center gap-1.5" data-caption-look={now}>
      <span>Captions</span>
      <select
        id={id}
        value={now}
        disabled={busy || disabled || cap?.locked}
        onChange={(e) => void choose(e.target.value)}
        title={cap?.locked ? 'The captions are locked' : summary}
        className="h-7 rounded-[8px] bg-panel px-1.5 text-[12px] text-fg hairline"
        data-caption-look-choice
      >
        {CAPTION_LOOKS.map((l) => (
          <option key={l.id} value={l.id}>
            {l.name}
          </option>
        ))}
        <option value="off">Off</option>
      </select>
    </label>
  );
}
