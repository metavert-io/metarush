import { useRef, useState } from 'react';
import { FilmSlate, Image as ImageIcon, Images } from '@phosphor-icons/react';
import { media } from '../../api/client';
import { useOp } from '../../api/queries';
import { resourceMedia } from '../../api/resources';
import type { AssetSummary } from '../../api/types';
import { uploads } from '../../api/uploads';
import { duration as fmtDuration } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { Button, Skeleton, Spinner, Theirs, Thumb } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';

/*
 * Where a generated shot starts (D154): from words only, a picture (a photo
 * from this computer, added to the project's resources first, or one already
 * there) or a frame of one of the project's clips. The picture is sent as the
 * shot's first frame; nothing is sent before the price is agreed.
 */

/** A shot's first (or last) frame: a moment of a clip, or a picture from the resources library. */
export interface Frame {
  assetId?: string;
  at?: number;
  /** A resources-library picture (its short name) instead of a clip's frame. */
  picture?: string;
  title: string;
  /** The picture's thumbnail (pictures only; a clip's frame is drawn from the clip). */
  thumb?: string;
  /** The clip's length, so the frame can be moved (clips chosen here). */
  duration?: number;
}

export const frameThumb = (f: Frame) => f.thumb ?? (f.assetId ? media.frame(f.assetId, f.at ?? 0, 240) : undefined);

const PICTURE_TYPES = 'image/png,image/jpeg,image/webp';

export function StartPicker({ projectId, projectName, clips, onPick }: {
  projectId: string;
  projectName: string;
  clips: AssetSummary[];
  onPick: (f: Frame) => void;
}) {
  const toast = useToast();
  const dialog = useDialog();
  const file = useRef<HTMLInputElement>(null);
  const [adding, setAdding] = useState(false);
  const videos = clips.filter((c) => c.kind === 'video' && c.state === 'available');

  // A photo from this computer: added to the project's resources, then used.
  const send = (f: File) => {
    setAdding(true);
    const before = new Set(uploads.getSnapshot().map((u) => u.key));
    void uploads.add([{ file: f }], { projectId, label: projectName });
    const key = uploads.getSnapshot().find((u) => !before.has(u.key))?.key;
    const off = uploads.onFinished((it) => {
      if (it.key !== key) return;
      off();
      setAdding(false);
      const r = it.result?.resource;
      if (r && r.kind === 'image') {
        onPick({ picture: r.name, title: r.title, thumb: resourceMedia.thumb(r.id) });
        return;
      }
      toast({
        tone: 'error',
        title: "That can't start a shot",
        body: it.result?.assetId ? 'It went to Sources as a clip. Choose a photo or picture (PNG, JPEG or WebP), or a frame of the clip.' : it.error ?? 'Choose a photo or picture (PNG, JPEG or WebP).',
      });
    });
  };

  const fromResources = async () => {
    const r = await dialog.open<{ name: string; title: string; id: string; current: string }>((close) => <PicturesList projectId={projectId} onPick={close} />, { title: 'Start from a picture', size: 'md' });
    if (r) onPick({ picture: r.name, title: r.title, thumb: resourceMedia.thumb(r.id, r.current) });
  };

  const fromClip = async () => {
    const c = await dialog.open<AssetSummary>(
      (close) => (
        <ul className="grid max-h-[min(60dvh,520px)] grid-cols-2 gap-2 overflow-y-auto sm:grid-cols-3" aria-label="Clips">
          {videos.map((v) => (
            <li key={v.id}>
              <button type="button" onClick={() => close(v)} className="w-full rounded-[10px] p-1.5 text-left hairline hover:bg-hover" data-pick-clip>
                <Thumb src={v.variants.includes('thumbnail') ? media.asset(v.id, 'thumbnail') : undefined} alt="" className="aspect-video w-full rounded-[7px]" fallback={<FilmSlate size={16} />} />
                <span className="mt-1 block truncate text-[12px] font-medium text-fg">
                  <Theirs>{v.title}</Theirs>
                </span>
                <span className="block text-[11px] text-subtle">{fmtDuration(v.duration)}</span>
              </button>
            </li>
          ))}
        </ul>
      ),
      { title: 'Start from a frame of a clip', size: 'lg' },
    );
    if (c) onPick({ assetId: c.id, at: 0, title: c.title, duration: c.duration });
  };

  return (
    <div data-start-picker>
      <div className="mb-1.5 text-[12.5px] font-medium text-muted">Start from a picture (optional)</div>
      <input
        ref={file}
        type="file"
        accept={PICTURE_TYPES}
        className="hidden"
        aria-hidden
        tabIndex={-1}
        data-start-file
        onChange={(e) => {
          const f = e.target.files?.[0];
          e.target.value = '';
          if (f) send(f);
        }}
      />
      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="secondary" icon={adding ? <Spinner size={12} /> : <ImageIcon size={13} />} disabled={adding} onClick={() => file.current?.click()} data-start-upload>
          {adding ? 'Adding the picture…' : 'A photo from this computer'}
        </Button>
        <Button size="sm" variant="ghost" icon={<Images size={13} />} onClick={() => void fromResources()}>
          A picture in Resources
        </Button>
        {videos.length ? (
          <Button size="sm" variant="ghost" icon={<FilmSlate size={13} />} onClick={() => void fromClip()} data-start-clip>
            A frame of a clip
          </Button>
        ) : null}
      </div>
      <p className="mt-1.5 text-[12px] text-subtle">The shot starts on it. A photo is added to this project&apos;s Resources first.</p>
    </div>
  );
}

function PicturesList({ projectId, onPick }: { projectId: string; onPick: (r: { name: string; title: string; id: string; current: string }) => void }) {
  const list = useOp('resources.list', { projectId, kind: 'image' });
  if (list.isLoading) return <Skeleton className="h-40" />;
  const rows = list.data?.resources ?? [];
  if (!rows.length) return <p className="py-6 text-center text-[13px] text-muted">This project has no photos or pictures yet. Use a photo from this computer instead.</p>;
  return (
    <ul className="grid max-h-[min(60dvh,520px)] grid-cols-2 gap-2 overflow-y-auto sm:grid-cols-3" aria-label="Pictures">
      {rows.map((r) => (
        <li key={r.id}>
          <button type="button" onClick={() => onPick(r)} className="w-full rounded-[10px] p-1.5 text-left hairline hover:bg-hover">
            <Thumb src={resourceMedia.thumb(r.id, r.current)} alt="" className="aspect-video w-full rounded-[7px]" fit="contain" />
            <span className="mt-1 block truncate text-[12px] font-medium text-fg">
              <Theirs>{r.title}</Theirs>
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
}
