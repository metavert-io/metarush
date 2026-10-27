import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, ArrowsLeftRight, FilmSlate, FolderSimplePlus, Lightning, MagnifyingGlass, MusicNotes, Sparkle, SquaresFour, VideoCamera, X, XCircle } from '@phosphor-icons/react';
import { GeneratePanel, type Frame } from '../../components/generate/GeneratePanel';
import { GeneratedInfo } from '../../components/generate/GeneratedInfo';
import { AIBadge, isGenerated } from '../../components/generate/ai';
import { useOp, useOpMutation } from '../../api/queries';
import type { AssetSummary, ProjectShowOut } from '../../api/types';
import { absolute, duration as fmtDuration, plural, stamp, timecode } from '../../lib/format';
import { updateSelection } from '../../lib/workbench';
import { processing, usePickProject } from '../../components/Library';
import { AddBox } from '../../components/AddBox';
import { LinkedProjects, originSummary, useUseFromProject } from '../../components/ProjectLinks';
import { SourceMenu, useSourceActions } from '../../components/SourceActions';
import { AssetGallery, useAssetSelection, useLibraryView, ViewToggle } from '../../components/AssetGallery';
import { EditableTitle, FileInfo, StatusChips } from '../../components/AssetMeta';
import { SourceCredit } from '../../components/LinkImports';
import { RemotionCredit, RemotionSources } from '../../components/remotion/RemotionSources';
import { useAssetActivity } from '../../api/activity';
import { Comments } from '../../components/Comments';
import { SourceInspector, type Panel, type SeekRequest } from '../../components/SourceInspector';
import type { WordActions } from '../../components/Transcript';
import { call, media, toApiError } from '../../api/client';
import { clipDiffOps, plainRefusal } from '../../lib/editops';
import { OpError, quoteWords, removalFor, removalOps, shorter, type Removal } from '../../lib/words';
import { Badge, Button, cx, ErrorPanel, Skeleton, Spinner, Theirs, Thumb } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { TodoBadge } from '../../components/TodoBadge';
import { askInProject } from '../../layout/assistant/store';
import { BetterNames, NameSuggestionChip, SourceInfo } from '../../components/sources/SourceInfo';

export function SourcesTab({ data, assetId, t, highlight, panel, onSelect }: {
  data: ProjectShowOut;
  assetId?: string;
  t?: number;
  highlight?: string[];
  /** Open this tab of the clip (deep links). */
  panel?: Panel;
  onSelect: (id: string | undefined) => void;
}) {
  const [filter, setFilter] = useState('');
  const [kind, setKind] = useState<'all' | 'video' | 'audio' | 'processing' | 'generated'>('all');
  // Generate a shot (D123): open from the toolbar, a clip's frame, or a link (?generate=1).
  const [params, setParams] = useSearchParams();
  const [genOpen, setGenOpen] = useState(params.get('generate') === '1');
  const [genStart, setGenStart] = useState<Frame | undefined>();
  const [genEnd, setGenEnd] = useState<Frame | undefined>();
  useEffect(() => {
    if (params.get('generate') === '1') setGenOpen(true);
  }, [params]);
  const closeGenerate = () => {
    setGenOpen(false);
    setGenStart(undefined);
    setGenEnd(undefined);
    if (params.get('generate')) {
      const next = new URLSearchParams(params);
      next.delete('generate');
      setParams(next, { replace: true });
    }
  };
  const [view, setView] = useLibraryView('sources');
  const pick = usePickProject();
  const activity = useAssetActivity();
  const assets = data.assets;
  const selected = assets.find((a) => a.id === assetId);
  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return assets.filter((a) => {
      if (q && ![a.title, a.name, ...(a.aliases ?? [])].some((s) => s.toLowerCase().includes(q))) return false;
      if (kind === 'video') return a.kind === 'video';
      if (kind === 'audio') return a.kind === 'audio';
      if (kind === 'processing') return processing(a).some((p) => p.state !== 'ok');
      if (kind === 'generated') return isGenerated(a.source);
      return true;
    });
  }, [assets, filter, kind]);
  const selection = useAssetSelection(useMemo(() => shown.map((a) => a.id), [shown]));
  const kinds: ('all' | 'video' | 'audio' | 'processing' | 'generated')[] = ['all', 'video', 'audio', 'processing', ...(assets.some((a) => isGenerated(a.source)) ? (['generated'] as const) : [])];
  const chosen = shown.filter((a) => selection.isSelected(a.id));
  const [seek, setSeek] = useState<SeekRequest | undefined>(t !== undefined && Number.isFinite(t) ? { t, nonce: 1 } : undefined);
  const toast = useToast();
  const add = useOpMutation('cut.add', { invalidate: ['project.show', 'project.list', 'revision.list', 'cut.show'] });
  // The marked part (or all of it) at the end of the current cut, saved as a new version.
  const addToCut = (r: { in: number; out: number }) => {
    if (!selected) return;
    add.mutate(
      { projectId: data.project.id, footage: selected.id, from: r.in.toFixed(3), to: r.out.toFixed(3) },
      {
        onSuccess: (o) => toast({ tone: 'success', title: `Added ${selected.title} to the cut`, body: o.applied ? `Saved as version ${o.applied.revision.number}.` : o.summary }),
        onError: (e) => toast({ tone: 'error', title: 'Not added', body: [e.message, e.hint].filter(Boolean).join(' ') }),
      },
    );
  };
  const [time, setTime] = useState(t ?? 0);
  const active = shown.findIndex((a) => a.id === assetId);
  const cutWords = useCutWords(data, selected);
  const project = useMemo(() => ({ id: data.project.id, name: data.project.name }), [data.project.id, data.project.name]);
  const actions = useSourceActions(project);
  const fromProject = useUseFromProject(project, data.links ?? []);
  const target = { projectId: data.project.id, label: data.project.name };
  const chosenOwn = chosen.filter((a) => !a.from);

  useEffect(() => {
    if (t !== undefined && Number.isFinite(t)) setSeek({ t, nonce: Date.now() });
  }, [t, assetId]);
  useEffect(() => {
    if (selected) updateSelection({ assetId: selected.id, assetName: selected.title, time, timeBase: 'source', clipId: undefined, revisionId: undefined });
    else updateSelection({ assetId: undefined, assetName: undefined, time: undefined, timeBase: undefined });
  }, [selected, time]);

  const generatePanel = genOpen ? (
    <GeneratePanel
      data={data}
      start={genStart}
      end={genEnd}
      onClearFrames={() => {
        setGenStart(undefined);
        setGenEnd(undefined);
      }}
      onOpenClip={(id) => onSelect(id)}
      onClose={closeGenerate}
    />
  ) : null;

  if (assets.length === 0)
    return (
      <div className="space-y-4">
        {generatePanel}
        <AddBox target={target} variant="empty" focus="footage" onFromProject={() => void fromProject()} />
        {!genOpen ? (
          <Button variant="secondary" icon={<Sparkle size={14} weight="fill" />} onClick={() => setGenOpen(true)}>
            Generate a shot
          </Button>
        ) : null}
        <LinkedProjects data={data} />
        <RemotionSources data={data} onOpenClip={(id) => onSelect(id)} />
      </div>
    );

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-2">
        {selected ? (
          <Button size="sm" variant="ghost" icon={<SquaresFour size={14} />} onClick={() => onSelect(undefined)}>
            All sources
          </Button>
        ) : null}
        <div className="relative min-w-[200px] flex-1 sm:max-w-[320px]">
          <MagnifyingGlass size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-subtle" />
          <input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Filter by title or filename"
            aria-label="Filter clips by title or filename"
            className="h-9 w-full rounded-[10px] bg-panel pl-8 pr-3 text-[13px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
          />
        </div>
        <div className="flex gap-1" role="group" aria-label="Filter by kind">
          {kinds.map((k) => (
            <button
              key={k}
              type="button"
              aria-pressed={kind === k}
              onClick={() => setKind(k)}
              className={cx('h-8 rounded-full px-3 text-[12.5px] capitalize transition-colors hairline', kind === k ? 'bg-panel text-fg' : 'text-muted hover:text-fg')}
            >
              {k === 'generated' ? 'AI-generated' : k}
            </button>
          ))}
        </div>
        <div className="ml-auto flex items-center gap-2">
          {!selected ? <ViewToggle value={view} onChange={setView} label="Clips layout" /> : null}
          {!genOpen ? (
            <Button size="sm" variant="secondary" icon={<Sparkle size={13} weight="fill" />} onClick={() => setGenOpen(true)} data-open-generate>
              Generate a shot
            </Button>
          ) : null}
          <AnalyzeButton projectId={data.project.id} />
        </div>
      </div>
      {generatePanel}
      {!selected ? (
        <>
          <BetterNames projectId={data.project.id} />
          <AddBox target={target} variant="compact" focus="footage" onFromProject={() => void fromProject()} />
          <LinkedProjects data={data} />
          <RemotionSources data={data} onOpenClip={(id) => onSelect(id)} />
        </>
      ) : null}

      {selected ? (
        <>
          <Filmstrip assets={shown} selectedId={selected.id} activeIndex={active} onSelect={(id) => onSelect(id)} />
          <section aria-label={`Clip ${selected.title}`} className="fade-in">
            <div className="mb-4 flex flex-wrap items-center gap-x-2 gap-y-1">
              <button type="button" onClick={() => onSelect(undefined)} className="rounded-[8px] p-1 text-subtle hover:bg-hover hover:text-fg lg:hidden" aria-label="Back to all clips">
                <ArrowLeft size={16} />
              </button>
              <EditableTitle asset={selected} heading className="min-w-0 max-w-full" />
              <span className="text-[12.5px] text-subtle">{fmtDuration(selected.duration)}</span>
              <span className="text-[12.5px] text-subtle" title={absolute(selected.createdAt)} data-stamp>
                added {stamp(selected.createdAt)}
              </span>
              {selected.from ? <Badge tone="info" title={`It comes from ${selected.from}; nothing was copied`}>from {selected.from}</Badge> : null}
              <AIBadge source={selected.source} />
              <SourceMenu asset={selected} actions={actions} />
              {selected.vfr ? <Badge tone="info" title="It changes its frame rate while it plays; MetaRush makes a steady copy for editing">frame rate changes</Badge> : null}
              {selected.hdr ? <Badge tone="info">HDR</Badge> : null}
              <TodoBadge assetId={selected.id} />
              <StatusChips asset={selected} item={activity.get(selected.id)} className="w-full sm:ml-auto sm:w-auto" />
              <FileInfo asset={selected} className="w-full" />
              <NameSuggestionChip asset={selected} className="w-full" />
              {isGenerated(selected.source) ? (
                <GeneratedInfo asset={selected} className="w-full" />
              ) : selected.source?.remotion ? (
                <RemotionCredit source={selected.source} className="w-full" />
              ) : (
                <SourceCredit source={selected.source} className="w-full" />
              )}
              {selected.kind === 'video' ? (
                <div className="flex w-full flex-wrap items-center gap-1.5" data-frame-actions>
                  <Button
                    size="sm"
                    variant="ghost"
                    icon={<Sparkle size={13} weight="fill" />}
                    onClick={() => {
                      setGenStart({ assetId: selected.id, at: time, title: selected.title });
                      setGenEnd(undefined);
                      setGenOpen(true);
                    }}
                    title="Generate a new shot that starts on the frame shown in the player"
                  >
                    Continue this shot
                  </Button>
                  {genStart ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<FilmSlate size={13} />}
                      onClick={() => {
                        setGenEnd({ assetId: selected.id, at: time, title: selected.title });
                        setGenOpen(true);
                      }}
                      title="Generate a shot that goes from the first frame you chose to this one"
                    >
                      End the shot on this frame
                    </Button>
                  ) : null}
                </div>
              ) : null}
            </div>
            <SourceInspector
              key={selected.id}
              asset={selected}
              seek={seek}
              onTime={setTime}
              highlight={highlight}
              panel={panel}
              markable={selected.kind === 'video'}
              onAdd={data.headRevisionId && selected.kind === 'video' ? [{ label: 'Add to the end of the cut', run: (r) => addToCut(r) }] : undefined}
              cutWords={cutWords}
            />
            <SourceInfo id={selected.id} className="mt-6" />
            <div className="mt-6 grid grid-cols-1 items-start gap-5 xl:grid-cols-[minmax(0,1.35fr)_minmax(340px,1fr)]">
              <UsedInCut data={data} asset={selected} onSeek={(s) => setSeek({ t: s, nonce: Date.now() })} />
              <section className="panel p-4">
                <Comments projectId={data.project.id} assetId={selected.id} time={time} timeLabel="source" onSeek={(s) => setSeek({ t: s, nonce: Date.now() })} />
              </section>
            </div>
          </section>
        </>
      ) : (
        <div className="grid grid-cols-1 gap-6 xl:grid-cols-[minmax(0,1fr)_380px]">
          <section aria-label="Clips in this project" className="min-w-0">
            {chosen.length > 0 ? (
              <div role="region" aria-label="Selected clips" className="fade-in mb-3 flex flex-wrap items-center gap-2 rounded-[12px] bg-raised px-3 py-2 hairline">
                <span className="text-[13px] font-medium text-fg">{plural(chosen.length, 'clip')} selected</span>
                {chosenOwn.length ? (
                  <Button size="sm" variant="primary" icon={<ArrowsLeftRight size={13} />} onClick={() => void actions.move(chosenOwn)}>
                    Move to another project…
                  </Button>
                ) : null}
                <Button size="sm" variant="secondary" icon={<FolderSimplePlus size={13} />} onClick={() => void pick(chosen)}>
                  Also add to another project…
                </Button>
                {chosenOwn.length ? (
                  <Button size="sm" variant="ghost" icon={<XCircle size={13} />} onClick={() => void actions.remove(chosenOwn)}>
                    Remove from this project
                  </Button>
                ) : null}
                <button type="button" onClick={selection.clear} className="ml-auto inline-flex items-center gap-1 rounded-[8px] px-2 py-1 text-[12px] text-muted hover:bg-hover hover:text-fg">
                  <X size={12} /> Clear
                </button>
              </div>
            ) : null}
            <AssetGallery
              assets={shown}
              view={view}
              selection={selection}
              label="Clips"
              onOpen={(a) => onSelect(a.id)}
              action={(a) => <SourceMenu asset={a} actions={actions} />}
              emptyText="No clips match this filter."
            />
            <p className="mt-3 px-1 text-[11.5px] text-subtle">
              {originSummary(shown, 'clip')}. <span className="tc">j</span>/<span className="tc">k</span> move, <span className="tc">x</span> selects, Enter opens.
            </p>
          </section>
          <Moments projectId={data.project.id} assets={assets} onOpen={(id, at) => {
            onSelect(id);
            setSeek({ t: at, nonce: Date.now(), play: false });
          }} />
        </div>
      )}
    </div>
  );
}

function Filmstrip({ assets, selectedId, activeIndex, onSelect }: { assets: AssetSummary[]; selectedId: string; activeIndex: number; onSelect: (id: string) => void }) {
  return (
    <div className="no-scrollbar -mx-1 flex gap-2 overflow-x-auto px-1 pb-1" role="listbox" aria-label="Sources">
      {assets.map((a, i) => {
        const sel = a.id === selectedId;
        return (
          <button
            key={a.id}
            type="button"
            role="option"
            aria-selected={sel}
            onClick={() => onSelect(a.id)}
            className={cx('group w-[148px] shrink-0 rounded-[12px] p-1 text-left transition-colors', sel ? 'bg-accent-soft ring-1 ring-accent-line' : i === activeIndex ? 'bg-hover' : 'hover:bg-hover')}
          >
            <div className="relative">
              <Thumb src={a.variants.includes('thumbnail') ? media.asset(a.id, 'thumbnail') : undefined} alt="" className="aspect-video w-full rounded-[9px]" fallback={a.kind === 'audio' ? <MusicNotes size={18} /> : <VideoCamera size={16} />} />
              <TodoBadge assetId={a.id} link={false} tone="overlay" className="absolute bottom-1 left-1" />
              <AIBadge source={a.source} overlay className="absolute right-1 top-1" />
            </div>
            <div className="mt-1.5 truncate px-0.5 text-[12px] font-medium text-fg" title={`${a.title} (${a.name})`}>
              <Theirs>{a.title}</Theirs>
            </div>
            <div className="tc px-0.5 text-[10.5px] text-subtle">{fmtDuration(a.duration)}</div>
            <div className="truncate px-0.5 text-[10.5px] text-subtle" title={`Added ${absolute(a.createdAt)}`} data-stamp>
              {stamp(a.createdAt)}
            </div>
          </button>
        );
      })}
    </div>
  );
}

/** Where this source appears in the cut's latest version. */
function UsedInCut({ data, asset, onSeek }: { data: ProjectShowOut; asset: AssetSummary; onSeek: (s: number) => void }) {
  const head = data.headRevisionId;
  const rev = useOp('revision.show', head ? { id: head } : null);
  const clips = (rev.data?.spec.clips ?? []).filter((c) => c.assetId === asset.id);
  return (
    <section className="panel p-5" aria-label="Used in the latest version">
      <div className="mb-3 text-[13.5px] font-semibold">In the latest version</div>
      {!head ? (
        <div className="text-[12.5px] text-subtle">There is no cut yet.</div>
      ) : rev.isLoading ? (
        <Skeleton className="h-16" />
      ) : rev.error ? (
        <ErrorPanel compact error={rev.error} />
      ) : clips.length === 0 ? (
        <div className="text-[12.5px] text-subtle">Not used in version {rev.data?.summary.number}.</div>
      ) : (
        <ul className="space-y-2">
          {clips.map((c) => (
            <li key={c.id}>
              <button type="button" onClick={() => onSeek(c.in)} className="w-full rounded-[12px] p-3 text-left hairline hover:bg-hover">
                <div className="flex items-center gap-2">
                  <span className="text-[13px] font-medium text-fg">{c.label || c.id}</span>
                  <span className="tc text-[11.5px] text-accent-text">
                    {timecode(c.in)} to {timecode(c.out)}
                  </span>
                </div>
                {c.rationale ? <div className="mt-1 text-[12.5px] text-muted">{c.rationale}</div> : null}
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function AnalyzeButton({ projectId }: { projectId: string }) {
  const toast = useToast();
  const analyze = useOpMutation('footage.analyze', { invalidate: ['job.list', 'project.show'] });
  return (
    <Button
      size="sm"
      variant="ghost"
      icon={<Lightning size={13} />}
      loading={analyze.isPending}
      onClick={() =>
        analyze.mutate(
          { projectId },
          {
            onSuccess: (o) =>
              toast({
                tone: 'success',
                title: o.jobs.length ? `Scheduled ${plural(o.jobs.length, 'analysis job')}` : 'Everything is already analyzed',
                body: o.jobs.length ? 'Transcripts and the scene analysis are made on your own machines running MetaRush.' : o.skipped.length ? `${plural(o.skipped.length, 'item')} already done.` : undefined,
              }),
            onError: (e) => toast({ tone: 'error', title: 'Could not schedule analysis', body: [e.message, e.hint].filter(Boolean).join(' ') }),
          },
        )
      }
    >
      Analyze missing
    </Button>
  );
}

/** Grounded candidate moments across the project (speech, pauses, scenes), searchable. */
/** What kind of moment it is, in words. */
const MOMENT_WORDS: Record<string, string> = { speech: 'said', pause: 'pause', visual: 'on screen', scene: 'scene change', black: 'black', freeze: 'frozen' };

function Moments({ projectId, assets, onOpen }: { projectId: string; assets: AssetSummary[]; onOpen: (assetId: string, t: number) => void }) {
  const [q, setQ] = useState('');
  const [debounced, setDebounced] = useState('');
  useEffect(() => {
    const t = setTimeout(() => setDebounced(q.trim()), 250);
    return () => clearTimeout(t);
  }, [q]);
  const moments = useOp('footage.moments', { projectId, ...(debounced ? { query: debounced } : {}), limit: 120 });
  const names = new Map(assets.map((a) => [a.id, a.title]));
  const list = moments.data?.moments ?? [];
  return (
    <aside className="panel flex max-h-[min(78dvh,820px)] flex-col p-4" aria-label="Moments">
      <div className="flex items-center gap-2">
        <h2 className="text-[13.5px] font-semibold">Moments</h2>
        {moments.isFetching ? <Spinner size={11} className="text-subtle" /> : null}
        <span className="ml-auto text-[11.5px] text-subtle">{list.length ? plural(list.length, 'moment') : ''}</span>
      </div>
      {/* Shorts from any footage (D246): Claude picks the moments on your own plan; each comes back as a card that makes a new cut. */}
      <Button
        size="sm"
        variant="ghost"
        icon={<Sparkle size={13} />}
        className="mt-2 self-start"
        onClick={() =>
          askInProject(
            projectId,
            'Find the best moments in this project\'s footage for shorts: complete stretches of 15 to 60 seconds that start and end cleanly. Propose each as a short for phones, with why it works.',
          )
        }
        data-ask-shorts
      >
        Ask Claude for shorts
      </Button>
      <div className="relative mt-3">
        <MagnifyingGlass size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-subtle" />
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search what was said"
          aria-label="Search transcripts across this project"
          className="h-9 w-full rounded-[10px] bg-panel pl-8 pr-3 text-[13px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
        />
      </div>
      <div className="mt-3 min-h-0 flex-1 overflow-y-auto">
        {moments.isLoading ? (
          <div className="space-y-2">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-12" />
            ))}
          </div>
        ) : moments.error ? (
          <ErrorPanel compact error={moments.error} onRetry={() => void moments.refetch()} />
        ) : list.length === 0 ? (
          <div className="px-1 py-6 text-center text-[12.5px] text-muted">{debounced ? `No speech matches “${debounced}”.` : 'No moments yet. They appear once the transcripts and the scene analysis are ready.'}</div>
        ) : (
          <ul className="space-y-0.5">
            {list.map((m, i) => (
              <li key={`${m.assetId}-${m.from}-${i}`}>
                <button type="button" onClick={() => onOpen(m.assetId, m.from)} className="w-full rounded-[10px] px-2.5 py-2 text-left hover:bg-hover">
                  <div className="flex items-center gap-2 text-[11.5px]">
                    <span className="tc text-accent-text">{timecode(m.from)}</span>
                    <Badge tone={m.kind === 'speech' ? 'neutral' : m.kind === 'scene' ? 'accent' : m.kind === 'pause' ? 'info' : 'danger'}>{MOMENT_WORDS[m.kind] ?? m.kind}</Badge>
                    <Theirs className="truncate text-subtle">{names.get(m.assetId) ?? m.asset}</Theirs>
                  </div>
                  {m.text ? (
                    <div className="clamp-2 mt-1 text-[12.5px] leading-relaxed text-fg">
                      {m.kind === 'speech' && m.detail ? <span className="font-medium text-muted">{m.detail}: </span> : null}
                      {m.text}
                    </div>
                  ) : m.detail ? (
                    <div className="mt-0.5 text-[12px] text-muted">{m.detail}</div>
                  ) : null}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
      {moments.data?.missing.length ? <div className="mt-2 border-t border-line pt-2 text-[11px] text-subtle">{plural(moments.data.missing.length, 'item')} still processing.</div> : null}
      <p className="mt-2 text-[11px] leading-relaxed text-subtle">{moments.data?.note}</p>
    </aside>
  );
}

/**
 * Words selected in a clip's transcript, taken out of the current cut: every
 * clip of the cut that plays them loses them, cut in the pauses around them,
 * saved as a new version (Undo puts them back).
 */
function useCutWords(data: ProjectShowOut, asset: AssetSummary | undefined): WordActions['cut'] | undefined {
  const qc = useQueryClient();
  const toast = useToast();
  const headId = data.headRevisionId;
  const head = useOp('revision.show', headId && asset?.hasAudio ? { id: headId } : null);
  const tx = useOp('footage.transcript', asset?.hasAudio ? { assetId: asset.id, words: true } : null);
  return useMemo(() => {
    if (!asset || !headId || !asset.hasAudio) return undefined;
    const spec = head.data?.spec;
    const segs = tx.data?.status?.state === 'available' ? tx.data.segments : undefined;
    const txf = (id: string) => (id === asset.id ? segs : undefined);
    const find = (from: number, to: number): Removal[] => {
      if (!spec || !segs) return [];
      const out: Removal[] = [];
      for (const c of spec.clips) {
        if (c.assetId !== asset.id) continue;
        const r = removalFor(spec, txf, c.id, from, to);
        if (r) out.push(r);
      }
      return out;
    };
    const refresh = () => {
      for (const key of ['project.show', 'project.list', 'revision.list', 'cut.show', 'audit.list']) void qc.invalidateQueries({ queryKey: [key] });
    };
    const fail = (title: string, e: unknown) => {
      const ae = toApiError(e);
      toast({ tone: 'error', title, body: [ae.message, ae.hint].filter(Boolean).join(' ') });
    };
    return {
      check: (from, to) => (!spec ? 'Loading the cut…' : !segs ? 'The transcript is not ready yet.' : find(from, to).length ? null : "These words aren't in the cut."),
      run: ({ from, to, words }) => {
        const removals = find(from, to);
        if (!spec || !head.data || !removals.length) return;
        let ops;
        try {
          ops = removalOps(spec, removals);
        } catch (e) {
          toast({ tone: 'error', title: 'Not cut out', body: e instanceof OpError ? plainRefusal(e.message) : 'Those words could not be cut out.' });
          return;
        }
        const said = `“${quoteWords(words)}”`;
        const before = spec;
        call('edit.apply', { plan: { baseRevisionId: head.data.summary.id, origin: 'cut-remove-words:studio', instruction: `Take out ${said}`, ops } })
          .then((res) => {
            refresh();
            const secs = removals.reduce((n, r) => n + r.seconds, 0);
            toast({
              tone: 'success',
              title: `Took ${said} out of the cut`,
              body: `It is ${shorter(secs)} shorter now (version ${res.revision.number}).`,
              action: {
                label: 'Undo',
                onClick: () =>
                  void call('revision.show', { id: res.revision.id })
                    .then((rev) => call('edit.apply', { plan: { baseRevisionId: res.revision.id, origin: 'cut-remove-words:studio', instruction: `Put back ${said}`, ops: clipDiffOps(rev.spec, before) } }))
                    .then(() => {
                      refresh();
                      toast({ title: `Put back ${said}` });
                    })
                    .catch((e: unknown) => fail('Not put back', e)),
              },
            });
          })
          .catch((e: unknown) => fail('Not cut out', e));
      },
    };
  }, [asset, headId, head.data, tx.data, qc, toast]);
}
