import { useState } from 'react';
import { X } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import { Skeleton, Spinner, Theirs } from '../../ui/primitives';
import { FixWordsEditor } from './FixWords';
import { useFixes } from './useFixes';

/**
 * Fixing a caption from the review player (D145): the caption that was
 * clicked, the words of the footage it shows, and an editor for them. The fix
 * goes to the transcript; this version keeps its words until a new version.
 */
export function CaptionFix({ revisionId, projectId, frame, onClose }: { revisionId: string; projectId?: string; frame: number; onClose: () => void }) {
  const at = useOp('captions.at', { revisionId, frame }, { staleTime: 0 });
  const fixes = useFixes();
  const [busy, setBusy] = useState(false);
  const d = at.data;
  return (
    <div className="w-[min(92vw,460px)] space-y-2 rounded-[14px] bg-raised p-3 shadow-[var(--shadow-float)] hairline" data-caption-fix onPointerDown={(e) => e.stopPropagation()}>
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1 text-[12.5px] font-medium text-fg">Fix this caption</div>
        <button type="button" onClick={onClose} aria-label="Close" className="rounded-[6px] p-0.5 text-subtle hover:text-fg">
          <X size={12} />
        </button>
      </div>
      {at.isLoading ? (
        <Skeleton className="h-16" />
      ) : !d?.found || !d.fixable ? (
        <p className="text-[12.5px] text-muted">{d?.why || 'No caption shows there.'}</p>
      ) : (
        <>
          <p className="text-[12.5px] text-muted">
            This version says <Theirs className="text-fg">“{d.text}”</Theirs>
            {d.now !== d.text ? (
              <>
                ; the transcript says <Theirs className="text-fg">“{d.now}”</Theirs> now (fixed since)
              </>
            ) : null}
            .
          </p>
          <FixWordsEditor
            words={d.now}
            busy={busy}
            note="This version keeps its words; the fix waits for a new version."
            onCancel={onClose}
            onSave={async (text) => {
              setBusy(true);
              const out = await fixes.fix({ assetId: d.assetId!, from: d.sourceFrom, to: d.sourceTo, text, expect: d.now, projectId });
              setBusy(false);
              if (out) onClose();
            }}
          />
          {busy ? <Spinner size={11} className="text-subtle" label="Saving" /> : null}
        </>
      )}
    </div>
  );
}
