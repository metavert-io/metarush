import { useCallback, useEffect, useRef, useState } from 'react';
import { useSearchParams } from 'react-router';
import { keepPreviousData } from '@tanstack/react-query';
import { useOp } from '../../api/queries';
import type { ProjectShowOut, RenderPlan } from '../../api/types';
import { updateSelection } from '../../lib/workbench';
import { CutPreview, type CutPreviewHandle } from '../../player/LazyCutPreview';
import { cx, ErrorPanel, Skeleton } from '../../ui/primitives';
import { Mixer } from './sound/Mixer';

/*
 * The Sound tab: the preview, and everything about how the video sounds — how
 * loud the camera sound, each person, the narration, music and effects are, the
 * automatic help (voices evened out, music stepping back while someone talks,
 * smooth cuts), the overall loudness, clearer voices and the music. A change is
 * heard in the preview at once and saved as one new version of the cut. The
 * same ops back `metarush sound …` and `metarush mix …`.
 */

export function SoundTab({ data, revisionId, onSettled }: {
  data: ProjectShowOut;
  revisionId: string;
  /** The tab consumed its start-up instructions (clear them from the address). */
  onSettled?: () => void;
}) {
  // A change makes a new version: keep showing this one until the new one is in.
  const rev = useOp('revision.show', { id: revisionId }, { placeholderData: keepPreviousData });
  const show = useOp('sound.show', { revisionId }, { placeholderData: keepPreviousData });
  const preview = useRef<CutPreviewHandle>(null);
  const [plan, setPlan] = useState<RenderPlan | undefined>();
  const [override, setOverride] = useState<RenderPlan | undefined>();
  const [time, setTime] = useState(0);
  const [, setParams] = useSearchParams();
  const spec = rev.data?.spec;

  useEffect(() => {
    onSettled?.();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    updateSelection({ revisionId, time, timeBase: 'timeline', assetId: undefined, assetName: undefined, clipId: undefined });
  }, [revisionId, time]);
  const onFrame = useCallback((f: number, fps: number) => setTime(f / fps), []);

  if (rev.error) return <ErrorPanel error={rev.error} onRetry={() => void rev.refetch()} />;
  if (show.error) return <ErrorPanel error={show.error} onRetry={() => void show.refetch()} />;
  const vertical = spec ? (spec.output.height ?? 1920) > (spec.output.width ?? 1080) : true;

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-3">
        <div className="min-w-0">
          <h2 className="text-[19px] font-semibold tracking-[-0.02em]">Sound</h2>
          <p className="text-[12.5px] text-muted">How loud each part and each person is, clearer voices, and the music.</p>
        </div>
      </div>

      {/* The same two columns as the Voiceover tab: side by side from lg (vertical cut) or xl
          (landscape), the preview sticky below the top bar and the section tabs; stacked above the
          panel it scrolls away with the page. */}
      <div className={cx('grid grid-cols-1 gap-6', vertical ? 'lg:grid-cols-[minmax(280px,400px)_minmax(0,1fr)]' : 'xl:grid-cols-[minmax(0,1.3fr)_minmax(360px,1fr)]')}>
        <div
          data-vo-preview
          data-sound-preview
          className={cx('min-w-0 space-y-3', vertical ? 'lg:sticky lg:top-[calc(var(--mr-header-h)+63px)] lg:z-10 lg:self-start lg:bg-bg' : 'xl:sticky xl:top-[calc(var(--mr-header-h)+63px)] xl:z-10 xl:self-start xl:bg-bg')}
        >
          <CutPreview
            ref={preview}
            revisionId={revisionId}
            renders={data.renders}
            onFrame={onFrame}
            onPlan={setPlan}
            plan={override}
            maxHeight={vertical ? 'min(60dvh, 640px)' : 'min(50dvh, 520px)'}
          />
          {override ? (
            <p className="text-center text-[12px] font-medium text-accent-text" role="status">
              Playing your sound changes (not saved yet)
            </p>
          ) : null}
          <p className="text-center text-[12px] leading-relaxed text-subtle">
            The narration's lines and recordings are on the{' '}
            <button type="button" onClick={() => setParams(new URLSearchParams({ tab: 'voiceover' }))} className="font-medium text-accent-text hover:underline">
              Voiceover tab
            </button>
            .
          </p>
        </div>

        <div className="min-w-0">
          {!spec ? <Skeleton className="h-[420px] rounded-[16px]" /> : <Mixer revisionId={revisionId} spec={spec} plan={plan} show={show.data} data={data} onPreview={setOverride} />}
        </div>
      </div>
    </div>
  );
}
