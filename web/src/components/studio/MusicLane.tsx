import { useMemo, useState, type PointerEvent as ReactPointerEvent } from 'react';
import { MusicNotes } from '@phosphor-icons/react';
import { call } from '../../api/client';
import { hasFiles } from '../../api/uploads';
import type { AssetSummary, Spec } from '../../api/types';
import { timecode } from '../../lib/format';
import { cx } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { addMusicFile, isAudioFile } from '../../screens/studio/sound/Music';
import { errorText } from '../../screens/studio/voiceover/api';
import type { LaneSpec } from './TimelineEditor';
import type { TimelineEditor as Editor } from './useTimelineEditor';

/*
 * The timeline's music lane: each music track where it plays (a looped track
 * as its repeats). Drag one to move it, or its ends to choose which part of
 * the track plays; from then on it is placed by hand (sound.music.set, saved
 * as a new version like every saved change on the timeline). Drop a music
 * file on the lane to put it under the video, fitted to it.
 */

interface Block {
  id: string; // the track
  title: string;
  from: number; // seconds on the timeline
  to: number;
  fit: boolean;
  /** The track's own timing (seconds). */
  at: number;
  in: number;
  out: number;
  /** Where the track starts in the video and where it stops in the track now (its first piece). */
  start: number;
  playedTo: number;
}

type Drag = { id: string; kind: 'move' | 'start' | 'end'; x0: number; dt: number; pointer: number };

/** The height of one track's row on the lane. */
const ROW = 26;

export function useMusicLane({ editor, projectId, assets }: { editor: Editor; projectId: string; assets: Map<string, AssetSummary> }): LaneSpec | undefined {
  const spec = editor.spec;
  const plan = editor.plan;
  const blocks = useMemo(() => musicBlocks(spec, plan, assets), [spec, plan, assets]);
  if (!spec) return undefined;
  // Each track has a row of its own, so tracks that overlap stay apart.
  const rows = Math.max(1, new Set(blocks.map((b) => b.id)).size);
  return {
    key: 'music',
    label: 'Music',
    height: rows * ROW + 4,
    render: ({ x, pps }) => <MusicLaneBody blocks={blocks} x={x} pps={pps} editor={editor} projectId={projectId} />,
  };
}

function musicBlocks(spec: Spec | undefined, plan: Editor['plan'], assets: Map<string, AssetSummary>): Block[] {
  if (!spec || !plan) return [];
  const fps = plan.fps || 30;
  const out: Block[] = [];
  const first = new Map<string, { start: number; playedTo: number }>();
  for (const a of plan.audio) {
    if (a.role !== 'music') continue;
    const id = a.id.replace(/~\d+$/, '');
    const t = spec.audio?.find((x) => x.id === id);
    if (!t) continue;
    const from = a.from / fps;
    const to = (a.from + a.durationInFrames) / fps;
    if (!first.has(id)) first.set(id, { start: from, playedTo: a.trimBefore / fps + (to - from) });
    const f = first.get(id)!;
    out.push({ id, title: assets.get(t.assetId)?.title ?? id, from, to, fit: t.fit === 'auto', at: t.at, in: t.in, out: t.out, start: f.start, playedTo: f.playedTo });
  }
  return out;
}

function MusicLaneBody({ blocks, x, pps, editor, projectId }: { blocks: Block[]; x: (t: number) => number; pps: number; editor: Editor; projectId: string }) {
  const toast = useToast();
  const [drag, setDrag] = useState<Drag | null>(null);
  const [over, setOver] = useState(false);
  const [adding, setAdding] = useState(false);

  // One saved step: the timeline saves its own changes first, then follows the new version.
  const save = async (label: string, run: (rev: string) => Promise<string | undefined>) => {
    try {
      const ok = await editor.runSaved(label, async () => {
        const rev = editor.base?.id;
        if (!rev) return undefined;
        return run(rev);
      });
      if (ok) toast({ tone: 'success', title: label });
    } catch (e) {
      toast({ tone: 'error', title: 'The music was not changed', body: errorText(e) });
    }
  };

  const place = (b: Block, kind: Drag['kind'], dt: number) => {
    if (Math.abs(dt) < 0.05) return;
    const r3 = (v: number) => Math.round(v * 1000) / 1000;
    // From now on it is placed by hand: where it starts in the video and which part of the track plays.
    let at = b.start;
    let from = b.in;
    let to = b.fit ? b.playedTo : b.out;
    if (kind === 'move') at = Math.max(0, b.start + dt);
    else if (kind === 'start') {
      const d = Math.max(-b.in, -b.start, dt);
      at = b.start + d;
      from = b.in + d;
    } else to = Math.max(b.in + 0.5, b.playedTo + dt);
    const words = kind === 'move' ? `Moved “${b.title}”` : `Changed where “${b.title}” ${kind === 'start' ? 'starts' : 'ends'}`;
    void save(words, async (rev) => {
      const out = await call('sound.music.set', { revisionId: rev, track: b.id, at: r3(at), from: r3(from), to: r3(to), apply: true });
      if (out.applied?.problem) throw new Error(out.applied.problem);
      return out.applied?.revisionId;
    });
  };

  const down = (e: ReactPointerEvent<HTMLElement>, b: Block, kind: Drag['kind']) => {
    if (e.button !== 0) return;
    e.stopPropagation();
    e.preventDefault();
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    editor.setBusy(true);
    setDrag({ id: b.id, kind, x0: e.clientX, dt: 0, pointer: e.pointerId });
  };
  const move = (e: ReactPointerEvent<HTMLElement>) => {
    if (!drag || e.pointerId !== drag.pointer) return;
    e.stopPropagation();
    setDrag({ ...drag, dt: (e.clientX - drag.x0) / pps });
  };
  const up = (e: ReactPointerEvent<HTMLElement>, b: Block) => {
    if (!drag || e.pointerId !== drag.pointer) return;
    e.stopPropagation();
    const d = drag;
    setDrag(null);
    editor.setBusy(false);
    place(b, d.kind, d.dt);
  };

  // Dropping a music file: added to the project and put under the video, fitted to it.
  const drop = async (f: File) => {
    if (!isAudioFile(f)) {
      toast({ tone: 'error', title: 'That is not a music file', body: `“${f.name}” has no sound MetaRush can use as music.` });
      return;
    }
    setAdding(true);
    await save(`Added “${f.name.replace(/\.[^.]+$/, '')}” as music`, async (rev) => {
      const out = await addMusicFile(f, projectId, () => rev);
      return out.applied?.revisionId;
    });
    setAdding(false);
  };

  return (
    <div
      className={cx('absolute inset-0 rounded-[7px] transition-colors', over && 'bg-accent-soft ring-1 ring-inset ring-accent')}
      data-music-lane
      role="list"
      aria-label="Music on the timeline"
      onDragOver={(e) => {
        if (!hasFiles(e.dataTransfer)) return;
        e.preventDefault();
        e.stopPropagation();
        e.dataTransfer.dropEffect = 'copy';
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        if (!hasFiles(e.dataTransfer)) return;
        e.preventDefault();
        e.stopPropagation();
        setOver(false);
        const f = Array.from(e.dataTransfer.files)[0];
        if (f) void drop(f);
      }}
    >
      {blocks.length === 0 && !adding ? (
        <div className="pointer-events-none absolute inset-y-0 left-2 flex items-center gap-1.5 text-[11px] text-subtle">
          <MusicNotes size={12} aria-hidden />
          {over ? 'Drop to put this music under the video' : 'Drop a music file here'}
        </div>
      ) : null}
      {adding ? <div className="pointer-events-none absolute inset-y-0 left-2 flex items-center text-[11px] text-subtle">Adding the music…</div> : null}
      {blocks.map((b, i) => {
        const row = [...new Set(blocks.map((v) => v.id))].indexOf(b.id);
        const dt = drag?.id === b.id ? drag.dt : 0;
        const kind = drag?.id === b.id ? drag.kind : null;
        const from = kind === 'move' || kind === 'start' ? Math.max(0, b.from + dt) : b.from;
        const to = kind === 'move' ? b.to + dt : kind === 'end' ? Math.max(b.from + 0.5, b.to + dt) : b.to;
        const left = x(from) + 1;
        const w = Math.max(16, x(to) - x(from) - 2);
        return (
          <div
            key={`${b.id}-${i}`}
            role="listitem"
            tabIndex={0}
            data-music-block={b.id}
            aria-label={`Music “${b.title}”, ${timecode(b.from)} to ${timecode(b.to)}${b.fit ? ', fits the video' : ', placed by hand'}. Drag it to move it, or use the arrow keys`}
            title={`${b.title}: ${timecode(b.from)}–${timecode(b.to)}`}
            onPointerDown={(e) => down(e, b, 'move')}
            onPointerMove={move}
            onPointerUp={(e) => up(e, b)}
            onPointerCancel={() => {
              setDrag(null);
              editor.setBusy(false);
            }}
            onKeyDown={(e) => {
              const step = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
              if (!step) return;
              e.preventDefault();
              e.stopPropagation();
              place(b, 'move', step * (e.shiftKey ? 1 : 0.1));
            }}
            className={cx(
              'absolute flex cursor-grab items-center gap-1 overflow-hidden rounded-[7px] px-2 text-[11px] font-medium outline-none active:cursor-grabbing',
              'bg-[color-mix(in_oklab,var(--info)_55%,#13161a)] text-white focus-visible:ring-2 focus-visible:ring-white',
              kind && 'z-10 shadow-[0_8px_24px_-6px_rgb(0_0_0/0.6)] ring-2 ring-white',
            )}
            style={{ left, width: w, top: 2 + row * ROW, height: ROW - 4, touchAction: 'none' }}
          >
            <span
              aria-hidden
              className="absolute inset-y-0 left-0 w-2 cursor-ew-resize"
              onPointerDown={(e) => down(e, b, 'start')}
              onPointerMove={move}
              onPointerUp={(e) => up(e, b)}
            />
            <MusicNotes size={11} className="shrink-0" aria-hidden />
            {w > 46 ? <span className="truncate">{b.title}</span> : null}
            <span
              aria-hidden
              className="absolute inset-y-0 right-0 w-2 cursor-ew-resize"
              onPointerDown={(e) => down(e, b, 'end')}
              onPointerMove={move}
              onPointerUp={(e) => up(e, b)}
            />
          </div>
        );
      })}
      {drag ? (
        <div className="pointer-events-none absolute bottom-full z-30 mb-1 whitespace-nowrap rounded-[7px] bg-black/85 px-2 py-0.5 text-[11px] text-white" style={{ left: 90 }} role="status">
          {(() => {
            const b = blocks.find((v) => v.id === drag.id);
            if (!b) return '';
            if (drag.kind === 'end') return `Ends at ${timecode(Math.max(b.from + 0.5, b.to + drag.dt))}`;
            return `Starts at ${timecode(Math.max(0, b.from + drag.dt))}`;
          })()}
        </div>
      ) : null}
    </div>
  );
}
