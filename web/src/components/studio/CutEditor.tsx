import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ArrowsLeftRight, FilmStrip, Info, MagnifyingGlass, Scissors, Trash, VideoCamera } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call, media, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { AssetSummary, Clip, EditOp, ProjectShowOut, Render, RenderPlan, RevisionSummary, Spec } from '../../api/types';
import { endAssetDrag, startAssetDrag } from '../../lib/dragAssets';
import { newClipID, partID, place, round3, speedOf } from '../../lib/editops';
import { duration as fmtDuration, plural, timecode } from '../../lib/format';
import { markedRange, setKeySurface, timelineMounted, useMarksVersion } from '../../lib/marks';
import { updateSelection } from '../../lib/workbench';
import { CutPreview, type CutPreviewHandle } from '../../player/LazyCutPreview';
import { useDialog } from '../../ui/Dialog';
import { Badge, Button, cx, EmptyState, Skeleton, Tabs, Thumb } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { loadPref, savePref } from '../../lib/theme';
import { CropFraming, Layers, ReviewPanel } from '../CutReview';
import { SourceInspector } from '../SourceInspector';
import { ClipDetail, transitionWords } from '../Timeline';
import { CallFixes } from './CallFixes';
import { CutTranscript } from './CutTranscript';
import { CopyStatus, PreviewSwitch, useCutCopies } from './ReviewCopy';
import { useMusicLane } from './MusicLane';
import { clipName, TimelineEditor, type PlayerControls } from './TimelineEditor';
import { TransitionPicker } from './TransitionPicker';
import { VoiceoverButton } from '../../screens/studio/VoiceoverTab';
import { useTimelineEditor, type TimelineEditor as Editor } from './useTimelineEditor';

type Panel = 'clip' | 'words' | 'add' | 'review';
const PANELS: Panel[] = ['clip', 'words', 'add', 'review'];

/**
 * The Edit tab: the cut you edit by hand. The player plays what the
 * timeline shows (Go's preview of any unsaved change), the timeline is the
 * magnetic editor, and the panel beside the player shows the selected clip,
 * what the cut says (select words to cut them out), the footage to add (drag
 * it in, or mark a part and add it) and the cut's review (treatment,
 * approval, renders, comments).
 */
export function CutEditor({ data, onOpenSource, workspaceInstallations }: { data: ProjectShowOut; onOpenSource: (assetId: string, t: number) => void; workspaceInstallations?: Map<string, string> }) {
  const projectId = data.project.id;
  const headRev = data.revisions.find((r) => r.id === data.headRevisionId);
  const head = useMemo(() => (headRev ? { id: headRev.id, number: headRev.number } : undefined), [headRev?.id, headRev?.number]); // eslint-disable-line react-hooks/exhaustive-deps
  const editor = useTimelineEditor(projectId, head);
  const editorRef = useRef(editor);
  editorRef.current = editor;
  // A stable view of the editor for dialogs (they keep the props they opened with).
  const live = useMemo(() => new Proxy({} as Editor, { get: (_, k) => (editorRef.current as unknown as Record<string | symbol, unknown>)[k] }), []);
  const dialog = useDialog();
  const toast = useToast();
  const assets = useMemo(() => new Map<string, AssetSummary>(data.assets.map((a) => [a.id, a])), [data.assets]);
  const musicLane = useMusicLane({ editor, projectId, assets });
  const preview = useRef<CutPreviewHandle>(null);
  const [time, setTime] = useState(0);
  const [rate, setRate] = useState(1);
  const [playing, setPlaying] = useState(false);
  const [selectedId, setSelectedId] = useState<string>();
  const [panel, setPanelState] = useState<Panel>(() => {
    const p = loadPref<Panel>('cutPanel', 'clip');
    return PANELS.includes(p) ? p : 'clip';
  });
  const setPanel = useCallback((p: Panel | ((cur: Panel) => Panel)) => {
    setPanelState((cur) => {
      const next = typeof p === 'function' ? p(cur) : p;
      savePref('cutPanel', next);
      return next;
    });
  }, []);
  const spec = editor.spec;
  const baseId = editor.base?.id ?? head?.id;
  const summary = data.revisions.find((r) => r.id === baseId) ?? headRev;
  // Live preview or the review copy (D187): the copy of the latest version by
  // default when there are no unsaved changes; editing always shows the live one.
  const copies = useCutCopies(data.renders, data.revisions, head?.id);
  const [choice, setChoice] = useState<string>();
  useEffect(() => setChoice(undefined), [head?.id]);
  // The default is decided once per version: a copy that finishes while
  // someone watches the live preview is offered (Watch it), never switched to.
  useEffect(() => {
    if (choice === undefined && head?.id) setChoice(copies.latest?.id ?? 'live');
  }, [choice, head?.id, copies.latest?.id]);
  const view = editor.dirty ? 'live' : (choice ?? copies.latest?.id ?? 'live');
  const showing = view === 'live' ? undefined : data.renders.find((r) => r.id === view && r.state === 'available' && r.output);
  const offered = copies.latest ?? copies.older;
  const switchRenders = [offered, showing && showing.id !== offered?.id ? showing : undefined].filter((r): r is Render => !!r);

  useEffect(() => {
    timelineMounted(1);
    return () => timelineMounted(-1);
  }, []);

  const onFrame = useCallback((frame: number, fps: number) => setTime(frame / fps), []);
  const seek = useCallback((s: number) => {
    preview.current?.seekToSeconds(s);
    setTime(s);
  }, []);

  // Keep the playhead inside the cut as it changes.
  const total = useMemo(() => (spec ? place(spec).total / (spec.output.fps || 30) : 0), [spec]);
  useEffect(() => {
    if (total > 0 && time > total) seek(Math.max(0, total - 1 / 30));
  }, [total, time, seek]);

  const player: PlayerControls = {
    toggle: () => preview.current?.toggle(),
    play: () => preview.current?.play(),
    pause: () => preview.current?.pause(),
    isPlaying: () => !!preview.current?.isPlaying() || playing,
    rate,
    setRate,
  };

  useEffect(() => {
    updateSelection({ revisionId: baseId, revisionNumber: summary?.number, clipId: selectedId, time, timeBase: 'timeline', assetId: undefined, assetName: undefined });
  }, [baseId, summary?.number, selectedId, time]);

  const lineage = useMemo(() => {
    const byId = new Map(data.revisions.map((r) => [r.id, r]));
    const out: RevisionSummary[] = [];
    let cur = baseId ? byId.get(baseId) : undefined;
    const seen = new Set<string>();
    while (cur && !seen.has(cur.id)) {
      seen.add(cur.id);
      out.push(cur);
      cur = cur.parentId ? byId.get(cur.parentId) : undefined;
    }
    return out;
  }, [data.revisions, baseId]);

  const nameOf = useCallback((c: Clip) => clipName(c, assets), [assets]);

  const openTransition = (clipId: string) => {
    if (!spec) return;
    const i = spec.clips.findIndex((c) => c.id === clipId);
    if (i < 0) return;
    const prev = i > 0 ? nameOf(spec.clips[i - 1]!) : 'the start';
    const next = nameOf(spec.clips[i]!);
    void dialog
      .open<string>((close) => <TransitionPicker editor={live} projectId={projectId} spec={spec} clipId={clipId} prevName={prev} nextName={next} onDone={(w) => close(w)} />, {
        title: 'Transition',
        size: 'lg',
      })
      .then((applied) => {
        if (applied) toast({ tone: 'success', title: `${applied} into ${next}`, action: { label: 'Undo', onClick: () => live.undo() } });
      });
  };

  // Footage into the cut: at the end, or at the playhead (a clip under it is split there).
  // Footage from the inbox joins the project first (a cut only uses its own footage).
  const addFootage = async (asset: AssetSummary, range: { in: number; out: number }, where: 'end' | 'playhead') => {
    if (!spec) return;
    if (!assets.has(asset.id)) {
      try {
        await call('asset.link', { assetId: asset.id, projectId });
      } catch {
        toast({ tone: 'error', title: 'Not added', body: `${asset.title} couldn't be added to this project.` });
        return;
      }
    }
    const clip: Clip = { id: newClipID(spec, asset.title || asset.name), assetId: asset.id, in: round3(range.in), out: round3(range.out), speed: 1 };
    const ops: EditOp[] = [];
    if (where === 'end' || !spec.clips.length) ops.push({ op: 'insertClip', after: spec.clips[spec.clips.length - 1]?.id, clip });
    else {
      const { placed, fps } = place(spec);
      const f = Math.round(time * fps);
      const at = placed.find((p) => f >= p.ownFrom && f < p.ownTo) ?? placed[placed.length - 1]!;
      const c = spec.clips[at.index]!;
      if (f - at.ownFrom <= 1) ops.push(at.index === 0 ? { op: 'insertClip', before: c.id, clip } : { op: 'insertClip', after: spec.clips[at.index - 1]!.id, clip });
      else if (at.ownTo - f <= 1 || c.locked) ops.push({ op: 'insertClip', after: c.id, clip });
      else {
        const split = round3(c.in + ((f - at.from) / fps) * speedOf(c));
        ops.push({ op: 'splitClip', id: c.id, at: split, newId: partID(spec, c.id, new Set([clip.id])) }, { op: 'insertClip', after: c.id, clip });
      }
    }
    const err = editor.apply(`Added ${asset.title}`, ops);
    if (err) toast({ tone: 'error', title: 'Not added', body: err });
    else {
      setSelectedId(clip.id);
      toast({
        title: `Added ${asset.title}`,
        body: `${fmtDuration(range.out - range.in)} of it, ${where === 'end' ? 'at the end' : 'at the playhead'}.`,
        action: { label: 'Undo', onClick: () => live.undo() },
      });
    }
  };

  const selected =
    spec?.clips.find((c) => c.id === selectedId) ??
    (() => {
      if (!spec) return undefined;
      const { placed, fps } = place(spec);
      const f = Math.round(time * fps);
      const at = placed.find((p) => f >= p.ownFrom && f < p.ownTo);
      return at ? spec.clips[at.index] : spec.clips[0];
    })();
  const vertical = spec ? (spec.output.height ?? 1920) > (spec.output.width ?? 1080) : true;

  return (
    // Phones: player, timeline, then the panel; wider screens: player beside the panel, the timeline under both.
    <div
      className={cx(
        'grid grid-cols-1 gap-5 [grid-template-areas:"player"_"timeline"_"panel"_"layers"]',
        vertical
          ? 'lg:grid-cols-[minmax(260px,380px)_minmax(0,1fr)] lg:[grid-template-areas:"player_panel"_"timeline_timeline"_"layers_layers"]'
          : 'xl:grid-cols-[minmax(0,1.3fr)_minmax(360px,1fr)] xl:[grid-template-areas:"player_panel"_"timeline_timeline"_"layers_layers"]',
      )}
      onPointerDownCapture={(e) => (e.target as HTMLElement).closest('[data-timeline-editor]') && setKeySurface('timeline')}
    >
      <div className="min-w-0 [grid-area:player]">
        {switchRenders.length ? (
          <PreviewSwitch
            className="mb-2"
            renders={switchRenders}
            value={showing ? showing.id : 'live'}
            onChange={(v) => {
              if (v !== 'live' && editor.dirty) {
                toast({ title: 'Your changes are saving', body: 'The review copy shows the cut as saved; the live preview shows your changes now.' });
                return;
              }
              setChoice(v);
            }}
          />
        ) : null}
        {baseId ? (
          <CutPreview
            ref={preview}
            revisionId={baseId}
            renders={data.renders}
            plan={editor.plan}
            playbackRate={rate}
            onFrame={onFrame}
            onPlayingChange={setPlaying}
            maxHeight={vertical ? 'min(46dvh, 520px)' : 'min(42dvh, 480px)'}
            playRender={showing}
          />
        ) : (
          <Skeleton className="aspect-[9/16] w-full max-w-[380px] rounded-[14px]" />
        )}
        <CopyStatus copies={copies} head={headRev} showing={showing} dirty={editor.dirty} onWatch={(r) => setChoice(r.id)} />
        <div className="mt-2 flex flex-wrap items-center justify-center gap-x-3 gap-y-1 text-[12px] text-subtle">
          <span className="tc">{timecode(time)}</span>
          <span aria-hidden>/</span>
          <span className="tc">{timecode(total)}</span>
          {rate !== 1 ? <Badge tone="accent">{rate < 0 ? `◀ ${Math.abs(rate)}×` : `${rate}×`}</Badge> : null}
          {editor.dirty ? <span className="text-accent-text">with your unsaved changes</span> : null}
          {spec && baseId ? <CropFraming spec={spec} revisionId={baseId} disabled={editor.dirty} /> : null}
        </div>
        {/* Call recordings: split-second flashes at clip edges, names the call app printed (offered only when found). */}
        <CallFixes
          projectId={projectId}
          headId={head?.id}
          dirty={editor.dirty}
          editor={live}
          spec={spec}
          nameOf={nameOf}
          firstNamesRule={(data.project.rules ?? []).some((r) => r.measure === 'first-names')}
        />
      </div>
      <div className="min-w-0 [grid-area:panel]">
        <Tabs<Panel>
          label="Beside the player"
          value={panel}
          onChange={setPanel}
          items={[
            { value: 'clip', label: 'Clip' },
            { value: 'words', label: 'Transcript' },
            { value: 'add', label: 'Add footage', count: data.assets.filter((a) => a.kind === 'video').length },
            { value: 'review', label: 'Review' },
          ]}
        />
        <div className="mt-3">
          {panel === 'clip' ? (
            <ClipPanel
              spec={spec}
              clip={selected}
              assets={assets}
              plan={editor.plan}
              projectId={projectId}
              onOpenSource={onOpenSource}
              onPlayFrom={(s) => {
                seek(s);
                preview.current?.play();
              }}
              onTransition={openTransition}
              onRemove={(c) => {
                const err = editor.apply(`Removed ${nameOf(c)}`, [{ op: 'removeClip', id: c.id }]);
                if (err) toast({ tone: 'error', title: 'Not removed', body: err });
                else toast({ title: `Removed ${nameOf(c)}`, action: { label: 'Undo', onClick: () => live.undo() } });
              }}
            />
          ) : panel === 'words' ? (
            <CutTranscript data={data} editor={editor} live={live} assets={assets} time={time} onSeek={seek} player={player} />
          ) : panel === 'add' ? (
            <SourceBin projectAssets={data.assets} onAdd={(a, r, w) => void addFootage(a, r, w)} />
          ) : summary ? (
            <div className="space-y-5">
              <ReviewPanel
                data={data}
                revisionId={summary.id}
                summary={summary}
                spec={spec}
                loading={!spec}
                time={time}
                onSeek={seek}
                workspaceInstallations={workspaceInstallations}
                lineage={lineage}
                clipId={selected?.id}
              />
            </div>
          ) : null}
        </div>
      </div>

      <section aria-labelledby="studio-timeline-h" className="panel min-w-0 p-3 [grid-area:timeline] sm:p-4">
        <div className="mb-2 flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <h2 id="studio-timeline-h" className="text-[15px] font-semibold tracking-[-0.01em]">
            Timeline
          </h2>
          <span className="text-[12px] text-subtle">{spec ? `${plural(spec.clips.length, 'clip')}, ${fmtDuration(total)}` : ''}</span>
        </div>
        <TimelineEditor
          projectId={projectId}
          editor={editor}
          assets={assets}
          time={time}
          onSeek={seek}
          player={player}
          selectedId={selectedId}
          onSelectClip={(id) => {
            setSelectedId(id);
            if (id) setPanel((p) => (p === 'review' || p === 'words' ? p : 'clip'));
          }}
          onOpenTransition={openTransition}
          onAddRequest={() => setPanel('add')}
          extraLanes={musicLane ? [musicLane] : undefined}
          narrationEmpty={spec && baseId ? <VoiceoverButton spec={spec} revisionId={baseId} clipId={selectedId} compact /> : undefined}
        />
      </section>

      {spec ? (
        <div className="grid min-w-0 grid-cols-1 items-start gap-5 [grid-area:layers] xl:grid-cols-2">
          <Layers spec={spec} assets={assets} onOpenSource={onOpenSource} onSeek={seek} />
        </div>
      ) : null}
    </div>
  );
}

function ClipPanel({
  spec,
  clip,
  assets,
  plan,
  projectId,
  onOpenSource,
  onPlayFrom,
  onTransition,
  onRemove,
}: {
  spec?: Spec;
  clip?: Clip;
  assets: Map<string, AssetSummary>;
  plan?: RenderPlan;
  projectId: string;
  onOpenSource: (assetId: string, t: number) => void;
  onPlayFrom: (s: number) => void;
  onTransition: (clipId: string) => void;
  onRemove: (c: Clip) => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  if (!spec) return <Skeleton className="h-40" />;
  if (!clip) return <EmptyState compact icon={<FilmStrip size={20} />} title="No clip selected" body="Click a clip on the timeline to see why it is in the cut." />;
  const { placed, fps } = place(spec);
  const p = placed.find((x) => x.id === clip.id);
  const i = spec.clips.findIndex((c) => c.id === clip.id);
  const way = transitionWords(clip.transitionIn);
  return (
    <div className="panel p-5">
      <ClipDetail
        clip={clip}
        asset={assets.get(clip.assetId)}
        assets={assets}
        timelineStart={p ? p.from / fps : undefined}
        onOpenSource={onOpenSource}
        onPlayFrom={onPlayFrom}
        nested={clip.assetId.startsWith('cut:') ? (plan?.nested?.[clip.assetId] ?? null) : undefined}
        onUpdateNested={() =>
          void call('cut.update', { projectId, cuts: [clip.id], instruction: 'Update the cut from the other project to its latest version' })
            .then((out) => {
              for (const k of ['project.show', 'revision.list', 'revision.show', 'revision.plan', 'cut.show']) void qc.invalidateQueries({ queryKey: [k] });
              toast({ tone: 'success', title: out.noChange ? 'It already plays the latest version' : 'Updated to the latest version', body: out.summary });
            })
            .catch((e: unknown) => {
              const err = toApiError(e);
              toast({ tone: 'error', title: 'Not updated', body: [err.message, err.hint].filter(Boolean).join(' ') });
            })
        }
      />
      <div className="mt-5 flex flex-wrap gap-2 border-t border-line pt-4">
        {i > 0 ? (
          <Button size="sm" variant="secondary" icon={<ArrowsLeftRight size={13} />} onClick={() => onTransition(clip.id)}>
            {way ? `Transition: ${way}` : 'Choose a transition in'}
          </Button>
        ) : null}
        <Button size="sm" variant="ghost" icon={<Trash size={13} />} disabled={clip.locked || spec.clips.length < 2} onClick={() => onRemove(clip)}>
          Remove from the cut
        </Button>
        {clip.locked ? <span className="self-center text-[12px] text-subtle">Locked: it stays as it is unless someone unlocks it.</span> : null}
      </div>
    </div>
  );
}

/** Footage to put into the cut: this project's and the inbox's. Drag it onto the timeline, or open it to mark a part and add it. */
function SourceBin({ projectAssets, onAdd }: { projectAssets: AssetSummary[]; onAdd: (a: AssetSummary, range: { in: number; out: number }, where: 'end' | 'playhead') => void }) {
  const [from, setFrom] = useState<'project' | 'inbox'>('project');
  const [q, setQ] = useState('');
  const [open, setOpen] = useState<string>();
  useMarksVersion();
  const inbox = useOp('asset.list', from === 'inbox' ? { unassigned: true, limit: 200 } : null);
  const all = from === 'project' ? projectAssets : (inbox.data?.assets ?? []);
  const list = all.filter((a) => a.kind === 'video' && (!q.trim() || `${a.title} ${a.name}`.toLowerCase().includes(q.trim().toLowerCase())));
  const opened = list.find((a) => a.id === open) ?? all.find((a) => a.id === open);
  return (
    <div className="space-y-3" data-no-timeline-keys>
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex gap-1" role="group" aria-label="Where the footage is">
          {(['project', 'inbox'] as const).map((k) => (
            <button
              key={k}
              type="button"
              aria-pressed={from === k}
              onClick={() => setFrom(k)}
              className={cx('h-8 rounded-full px-3 text-[12.5px] hairline', from === k ? 'bg-panel text-fg' : 'text-muted hover:text-fg')}
            >
              {k === 'project' ? 'This project' : 'Inbox'}
            </button>
          ))}
        </div>
        <div className="relative min-w-[160px] flex-1">
          <MagnifyingGlass size={13} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-subtle" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Find footage"
            aria-label="Find footage"
            className="h-8 w-full rounded-[9px] bg-panel pl-7 pr-2 text-[12.5px] text-fg hairline placeholder:text-subtle"
          />
        </div>
      </div>
      <p className="flex items-center gap-1.5 text-[11.5px] text-subtle">
        <Info size={12} /> Drag footage onto the timeline, or open it to mark the part you want and add it.
      </p>
      {from === 'inbox' && inbox.isLoading ? (
        <Skeleton className="h-24" />
      ) : list.length === 0 ? (
        <EmptyState
          compact
          icon={<VideoCamera size={18} />}
          title={from === 'inbox' ? 'The inbox is empty' : 'No footage here'}
          body={from === 'inbox' ? 'Footage dropped outside a project waits in the inbox.' : 'Drop footage onto this page to add it to the project.'}
        />
      ) : (
        <ul className="grid max-h-[340px] grid-cols-2 gap-2 overflow-y-auto pr-1 sm:grid-cols-3">
          {list.map((a) => {
            const r = markedRange(a);
            const thumb = a.variants.includes('thumbnail') ? media.asset(a.id, 'thumbnail') : undefined;
            return (
              <li key={a.id}>
                <button
                  type="button"
                  draggable
                  onDragStart={(e) => startAssetDrag(e, [a.id], a.title, thumb)}
                  onDragEnd={endAssetDrag}
                  onClick={() => setOpen(open === a.id ? undefined : a.id)}
                  aria-pressed={open === a.id}
                  aria-label={`${a.title}, ${fmtDuration(a.duration)}${r.marked ? `, marked ${timecode(r.in)} to ${timecode(r.out)}` : ''}. Drag it onto the timeline or open it`}
                  className={cx('group w-full cursor-grab rounded-[10px] p-1 text-left hairline active:cursor-grabbing', open === a.id ? 'bg-accent-soft ring-1 ring-accent-line' : 'hover:bg-hover')}
                >
                  <Thumb src={thumb} alt="" className="aspect-video w-full rounded-[7px]" fallback={<VideoCamera size={16} />} />
                  <div className="mt-1 truncate px-0.5 text-[12px] font-medium text-fg">{a.title}</div>
                  <div className="tc px-0.5 text-[10.5px] text-subtle">{r.marked ? `${timecode(r.in, { ms: false })}–${timecode(r.out, { ms: false })} marked` : fmtDuration(a.duration)}</div>
                </button>
              </li>
            );
          })}
        </ul>
      )}
      {opened ? (
        <div className="panel p-3">
          <SourceInspector
            key={opened.id}
            asset={opened}
            compact
            mini
            markable
            onAdd={[
              { label: 'Add at the end', run: (range) => onAdd(opened, range, 'end') },
              { label: 'Add at the playhead', run: (range) => onAdd(opened, range, 'playhead') },
            ]}
          />
        </div>
      ) : null}
      <div className="flex items-center gap-1.5 text-[11.5px] text-subtle">
        <Scissors size={12} /> Adding at the playhead splits the clip there and puts the footage in between.
      </div>
    </div>
  );
}
