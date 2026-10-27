import { useEffect, useState } from 'react';
import { VideoCamera } from '@phosphor-icons/react';
import { media } from '../api/client';
import { useOp } from '../api/queries';
import type { AssetSummary } from '../api/types';
import { loadPref, savePref } from '../lib/theme';
import { cx, EmptyState, ErrorPanel, Segmented, Skeleton } from '../ui/primitives';

type Mode = 'smart' | 'even';

/**
 * The clip's frame grid: by default one frame per shot, then where the picture
 * changes and who starts speaking, then frames to fill long stretches (the same
 * choice `metarush footage sheet` makes); or evenly spaced. Each frame is
 * labeled with its time, who speaks and what they say, and jumps there.
 */
export function FrameGrid({ asset, onSeek }: { asset: AssetSummary; onSeek: (t: number) => void }) {
  const [mode, setMode] = useState<Mode>(() => loadPref<Mode>('frameGridMode', 'smart'));
  useEffect(() => savePref('frameGridMode', mode), [mode]);
  const kf = useOp('footage.keyframes', asset.kind === 'video' ? { assetId: asset.id, mode } : null, { staleTime: 60_000 });
  if (!asset.variants.includes('proxy') && !asset.variants.includes('thumbnail'))
    return <EmptyState compact icon={<VideoCamera size={20} />} title="No frames yet" body="They are ready right after the preview copy is made on your computer." />;
  const portrait = (asset.height ?? 0) > (asset.width ?? 0);
  return (
    <div data-frame-grid={mode}>
      <div className="mb-2 flex items-center gap-2">
        <Segmented<Mode>
          label="Which frames"
          value={mode}
          onChange={setMode}
          items={[
            { value: 'smart', label: 'Every shot' },
            { value: 'even', label: 'Evenly spaced' },
          ]}
        />
        {kf.data ? <span className="ml-auto text-[11.5px] text-subtle">{mode === 'smart' ? `${kf.data.shots} ${kf.data.shots === 1 ? 'shot' : 'shots'}` : `${kf.data.tiles.length} frames`}</span> : null}
      </div>
      {kf.isLoading ? (
        <div className={cx('grid gap-2', portrait ? 'grid-cols-3' : 'grid-cols-2')}>
          {Array.from({ length: 6 }, (_, i) => (
            <Skeleton key={i} className={portrait ? 'aspect-[9/16]' : 'aspect-video'} />
          ))}
        </div>
      ) : kf.error ? (
        <ErrorPanel compact error={kf.error} onRetry={() => void kf.refetch()} />
      ) : (
        <ol className={cx('grid gap-2', portrait ? 'grid-cols-3' : 'grid-cols-2')} aria-label="Frames">
          {(kf.data?.tiles ?? []).map((t) => (
            <li key={t.n}>
              <button type="button" onClick={() => onSeek(t.at)} className="group block w-full text-left" aria-label={`${t.caption} (${t.why})`} data-frame-tile={t.reason}>
                <span className={cx('relative block overflow-hidden rounded-[9px] bg-inset', portrait ? 'aspect-[9/16]' : 'aspect-video')}>
                  <img src={media.frame(asset.id, t.at, 320)} alt="" loading="lazy" className="h-full w-full object-cover transition-transform group-hover:scale-[1.02]" />
                  <span className="tc absolute left-1 top-1 rounded-[5px] bg-black/60 px-1 text-[10.5px] text-white">#{t.n}</span>
                </span>
                <span className="mt-1 block text-[11.5px] leading-snug text-muted">
                  <span className="tc text-accent-text">{t.timecode}</span>
                  {t.speaker ? <span className="text-fg"> · {t.speaker}</span> : null}
                  {t.words ? <span>: “{t.words}”</span> : <span className="text-subtle"> · {t.why}</span>}
                </span>
              </button>
            </li>
          ))}
        </ol>
      )}
      <p className="mt-2 text-[11.5px] text-subtle">{mode === 'smart' ? 'One frame per shot, plus where the picture changes and where someone starts speaking.' : 'Frames evenly spaced through the clip.'}</p>
    </div>
  );
}
