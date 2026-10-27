import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router';
import { keepPreviousData } from '@tanstack/react-query';
import { ChatCircleDots, Microphone, Plus, Robot, SlidersHorizontal, TextAlignLeft } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import type { AssetSummary, NarrationLine, ProjectShowOut, RenderPlan, Spec, VoiceView } from '../../api/types';
import { openAskWith, updateSelection } from '../../lib/workbench';
import { CutPreview, type CutPreviewHandle } from '../../player/LazyCutPreview';
import { useDialog } from '../../ui/Dialog';
import { Button, cx, ErrorPanel, Skeleton, Tabs } from '../../ui/primitives';
import { AddVoiceover, clipName, type Scope, type VoiceoverMode } from './voiceover/AddVoiceover';
import { AIVoicePanel } from './voiceover/AIVoice';
import { ImportFlow, type ImportJob } from './voiceover/ImportFlow';
import { Recorder } from './voiceover/Recorder';
import { ScriptEditor } from './voiceover/ScriptEditor';
import { FitReview, FitWaiting, useFitCard } from './voiceover/ScriptVersions';
import { TakesDialog } from './voiceover/Takes';

/*
 * The Voiceover tab: the preview with the narration strip under it, and the
 * narration itself — its lines (the script) and the AI voice — for the whole
 * video or one clip. Recording opens the recorder over it. How loud the
 * narration is, a clearer narrator's voice and the rest of the mix are on the
 * Sound tab.
 */

export type VoiceoverPanel = 'lines' | 'ai';

/** A recording picked in the "Add voiceover" dialog on another tab, handed to this one. */
let handoff: ImportJob | null = null;
export function handOffImport(job: ImportJob) {
  handoff = job;
}

/** The request Claude gets for "Ask Claude to suggest one". */
export function suggestPrompt(spec: Spec, scope: Scope, hasScript: boolean): string {
  const target = scope.clipId
    ? `the shot ${clipName(spec.clips, scope.clipId)} only (keep the narration of the other shots as it is)`
    : hasScript
      ? 'this cut, in place of the narration it has now'
      : 'this cut';
  return `Suggest a voiceover script for ${target}. Look at what happens and what people say in it, keep each line short enough to fit before the next one starts, and start each line on the shot or moment it belongs to. Give me the lines to review.`;
}

export function VoiceoverTab({ data, revisionId, mode, clipId, fit, onSettled }: {
  data: ProjectShowOut;
  revisionId: string;
  /** How the person chose to start (Add voiceover), or the panel to open. */
  mode?: string;
  clipId?: string;
  /** Claude's fitted script to review: <conversation>~<card> (the card's "Review it first", D86). */
  fit?: string;
  /** The tab consumed its start-up instructions (clear them from the address). */
  onSettled: () => void;
}) {
  // A change makes a new version: keep showing this one until the new one is in (no flicker,
  // and the recorder stays open).
  const rev = useOp('revision.show', { id: revisionId }, { placeholderData: keepPreviousData });
  const show = useOp('voiceover.show', { revisionId }, { placeholderData: keepPreviousData });
  const providers = useOp('provider.list', {});
  const elReady = !!providers.data?.providers.find((p) => p.name === 'elevenlabs')?.configured;
  const voices = useOp('voiceover.voices', elReady ? { provider: 'elevenlabs' } : null, { staleTime: 5 * 60_000 });
  const dialog = useDialog();
  const preview = useRef<CutPreviewHandle>(null);
  const [plan, setPlan] = useState<RenderPlan | undefined>();
  const [time, setTime] = useState(0);
  const [panel, setPanel] = useState<VoiceoverPanel>(mode === 'ai' ? 'ai' : 'lines');
  const [scope, setScope] = useState<Scope>({ clipId });
  const [recording, setRecording] = useState<{ line?: string } | null>(mode === 'record' ? {} : null);
  const [imports, setImports] = useState<ImportJob[]>(() => {
    const h = handoff;
    handoff = null;
    return h ? [h] : [];
  });
  const [composing, setComposing] = useState(mode === 'write');
  // Claude fitting the script (D86): the conversation, and the card to review once it is there.
  const [fitting, setFitting] = useState<{ conv: string; action?: string } | null>(() => {
    const [conv, action] = (fit ?? '').split('~');
    return conv ? { conv, action: action || undefined } : null;
  });
  const fitCard = useFitCard(fitting?.conv, fitting?.action);
  const [asked, setAsked] = useState(mode === 'ask');
  const [, setParams] = useSearchParams();
  const headRef = useRef(data.headRevisionId);
  headRef.current = data.headRevisionId;
  const assets = useMemo(() => new Map<string, AssetSummary>(data.assets.map((a) => [a.id, a])), [data.assets]);
  const spec = rev.data?.spec;
  const narration = show.data;

  // Start-up instructions are used once.
  useEffect(() => {
    if (mode === 'ask' && spec) openAskWith(suggestPrompt(spec, { clipId }, !!narration?.hasScript), { newConversation: true });
    if (mode || clipId || fit) onSettled();
  }, [!!spec]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    updateSelection({ revisionId, clipId: scope.clipId, time, timeBase: 'timeline', assetId: undefined, assetName: undefined });
  }, [revisionId, scope.clipId, time]);

  const onFrame = useCallback((f: number, fps: number) => setTime(f / fps), []);
  const seek = (s: number) => {
    preview.current?.seekToSeconds(s);
    setTime(s);
  };
  const playLine = (l: NarrationLine) => {
    seek(Math.max(0, l.start - 0.5));
    preview.current?.play();
  };
  const choose = (m: VoiceoverMode, s: Scope, file?: File) => {
    setScope(s);
    switch (m) {
      case 'write':
        setPanel('lines');
        setComposing(true);
        break;
      case 'ask':
        if (spec) openAskWith(suggestPrompt(spec, s, !!narration?.hasScript), { newConversation: true });
        setAsked(true);
        setPanel('lines');
        break;
      case 'import':
        if (file) setImports((js) => [...js, { file, scope: s }]);
        setPanel('lines');
        break;
      case 'record':
        setRecording({});
        break;
      case 'ai':
        setPanel(narration?.hasScript ? 'ai' : 'lines');
        if (!narration?.hasScript) setComposing(true);
        break;
    }
  };
  const openChooser = () =>
    void dialog.open<true>(
      (close) => (
        <ChooserDialog
          spec={spec!}
          initial={scope}
          hasScript={!!narration?.hasScript}
          onChoose={(m, s, f) => {
            close(true);
            choose(m, s, f);
          }}
        />
      ),
      { title: 'Add voiceover', size: 'lg' },
    );
  const takes = (l: NarrationLine) => void dialog.open(() => <TakesDialog projectId={data.project.id} lineId={l.id} />, { title: `Takes for line ${l.line}`, size: 'lg' });

  if (rev.error || show.error) return <ErrorPanel error={rev.error ?? show.error} onRetry={() => void (rev.refetch(), show.refetch())} />;
  const vertical = spec ? (spec.output.height ?? 1920) > (spec.output.width ?? 1080) : true;
  const lines = narration?.beats ?? [];
  const scopeLines = scope.clipId && plan ? lines.filter((l) => overClip(plan, l, scope.clipId!)) : undefined;
  const voiceList: VoiceView[] = voices.data?.voices ?? [];

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-3">
        <div className="min-w-0">
          <h2 className="text-[19px] font-semibold tracking-[-0.02em]">Voiceover</h2>
          <p className="text-[12.5px] text-muted">A voice that talks over the video, placed line by line.</p>
        </div>
        {spec ? <ScopePicker spec={spec} scope={scope} onScope={setScope} /> : null}
        <div className="flex flex-wrap items-center gap-2 sm:ml-auto">
          {narration?.hasScript ? (
            <>
              <Button size="sm" variant="secondary" icon={<Microphone size={14} />} onClick={() => setRecording({})}>
                Record narration
              </Button>
              <Button size="sm" variant="ghost" icon={<Plus size={14} />} onClick={openChooser} disabled={!spec}>
                Add voiceover
              </Button>
            </>
          ) : null}
        </div>
      </div>

      {/* Side by side from lg for a vertical cut, from xl for a landscape one. Only then does the preview stay
          in view while the lines scroll: stacked above them it would stay pinned over them (a
          sticky grid item is held by the whole grid, not its own row). When it sticks, it sits below the top
          bar and the section tabs, opaque, under menus and dialogs. */}
      <div className={cx('grid grid-cols-1 gap-6', vertical ? 'lg:grid-cols-[minmax(280px,400px)_minmax(0,1fr)]' : 'xl:grid-cols-[minmax(0,1.3fr)_minmax(360px,1fr)]')}>
        <div
          data-vo-preview
          className={cx('min-w-0 space-y-3', vertical ? 'lg:sticky lg:top-[calc(var(--mr-header-h)+63px)] lg:z-10 lg:self-start lg:bg-bg' : 'xl:sticky xl:top-[calc(var(--mr-header-h)+63px)] xl:z-10 xl:self-start xl:bg-bg')}
        >
          <CutPreview
            ref={preview}
            revisionId={revisionId}
            renders={data.renders}
            onFrame={onFrame}
            onPlan={setPlan}
            maxHeight={vertical ? 'min(60dvh, 640px)' : 'min(50dvh, 520px)'}
          />
          {spec && lines.length ? (
            // Lines are moved by hand on the timeline's narration lane (Current cut tab).
            <p className="text-center text-[12px] leading-relaxed text-subtle">
              To move lines by hand, drag them along the narration lane of the timeline.{' '}
              <button type="button" onClick={() => setParams(new URLSearchParams({ tab: 'cut' }))} className="font-medium text-accent-text hover:underline">
                Open the timeline
              </button>
            </p>
          ) : null}
        </div>

        <div className="min-w-0">
          {!spec || !narration ? (
            <Skeleton className="h-[360px] rounded-[16px]" />
          ) : !narration.hasScript && !composing && imports.length === 0 && !asked ? (
            <section className="panel p-5" aria-labelledby="vo-start-h">
              <h3 id="vo-start-h" className="text-[16px] font-semibold tracking-[-0.01em]">
                Add a voiceover
              </h3>
              <p className="mb-4 mt-1 text-[13px] text-muted">A voice that talks over the video. How do you want to make it?</p>
              <AddVoiceover clips={spec.clips} scope={scope} onScope={setScope} onChoose={choose} hasScript={false} hideScope compact={!vertical} />
            </section>
          ) : (
            <>
              <Tabs<VoiceoverPanel>
                label="Voiceover sections"
                value={panel}
                onChange={setPanel}
                className="mb-4"
                items={[
                  { value: 'lines', label: <span className="inline-flex items-center gap-1.5"><TextAlignLeft size={14} /> Lines</span>, count: lines.length },
                  { value: 'ai', label: <span className="inline-flex items-center gap-1.5"><Robot size={14} /> AI voice</span> },
                ]}
              />
              <p className="-mt-2 mb-4 flex items-center gap-1.5 text-[12px] text-subtle">
                <SlidersHorizontal size={13} aria-hidden />
                How loud the narration is, a clearer narrator's voice and the music are on the{' '}
                <button type="button" onClick={() => setParams(new URLSearchParams({ tab: 'sound' }))} className="font-medium text-accent-text hover:underline">
                  Sound tab
                </button>
              </p>
              {imports.map((j, i) => (
                <div key={`${j.file.name}-${i}`} className="mb-4">
                  <ImportFlow
                    job={j}
                    projectId={data.project.id}
                    projectName={data.project.name}
                    headRevisionId={() => headRef.current}
                    hasScript={narration.hasScript}
                    clips={spec.clips}
                    onDone={() => setImports((js) => js.filter((x) => x !== j))}
                  />
                </div>
              ))}
              {asked && panel === 'lines' ? (
                <div className="mb-4 flex flex-wrap items-start gap-3 rounded-[14px] bg-accent-soft px-4 py-3">
                  <ChatCircleDots size={18} className="mt-[1px] shrink-0 text-accent-text" aria-hidden />
                  <p className="min-w-0 flex-1 text-[13px] leading-relaxed text-fg">
                    Your request is ready in the Claude Code panel: send it, and when Claude proposes the lines, confirm them there. They show up here.
                  </p>
                  <Button size="sm" variant="ghost" onClick={() => setAsked(false)}>
                    Hide
                  </Button>
                </div>
              ) : null}
              {panel === 'lines' && fitting ? (
                fitCard.card?.fit && fitCard.card.state === 'pending' ? (
                  <div className="mb-4">
                    <FitReview fit={fitCard.card.fit} card={fitCard.card} convId={fitting.conv} revisionId={revisionId} onDone={() => setFitting(null)} />
                  </div>
                ) : !fitCard.card ? (
                  <FitWaiting convId={fitting.conv} problem={fitCard.working ? undefined : fitCard.problem} onClose={() => setFitting(null)} />
                ) : null
              ) : null}
              {panel === 'lines' ? (
                narration.hasScript || composing ? (
                  <ScriptEditor
                    revisionId={revisionId}
                    spec={spec}
                    show={narration}
                    plan={plan}
                    assets={assets}
                    scope={scope}
                    voices={voiceList}
                    onSeek={seek}
                    onPlayLine={playLine}
                    onTakes={takes}
                    onRecord={(l) => setRecording({ line: l?.id })}
                    onAIVoice={() => setPanel('ai')}
                    composing={composing}
                    onComposed={() => setComposing(false)}
                    onFitStarted={(conv) => setFitting({ conv })}
                  />
                ) : (
                  <p className="text-[13px] text-subtle">The lines appear here as soon as they are in the cut.</p>
                )
              ) : panel === 'ai' ? (
                <AIVoicePanel projectId={data.project.id} projectName={data.project.name} revisionId={revisionId} show={narration} only={scopeLines?.map((l) => l.id)} />
              ) : null}
            </>
          )}
        </div>
      </div>

      {recording && spec ? (
        <Recorder
          projectId={data.project.id}
          projectName={data.project.name}
          revisionId={revisionId}
          headRevisionId={() => headRef.current}
          lines={lines}
          plan={plan}
          clips={spec.clips}
          scope={scope}
          renders={data.renders}
          startLine={recording.line}
          startAt={scope.clipId ? undefined : time}
          onClose={() => setRecording(null)}
        />
      ) : null}
    </div>
  );
}

function overClip(plan: RenderPlan, l: NarrationLine, clipId: string): boolean {
  const c = plan.clips.find((x) => x.id === clipId);
  if (!c) return false;
  const f = Math.round(l.start * plan.fps);
  return f >= c.from && f < c.from + c.durationInFrames;
}

function ScopePicker({ spec, scope, onScope }: { spec: Spec; scope: Scope; onScope: (s: Scope) => void }) {
  return (
    <label className="flex items-center gap-2 text-[12.5px] text-muted">
      For
      <select
        value={scope.clipId ?? ''}
        onChange={(e) => onScope(e.target.value ? { clipId: e.target.value } : {})}
        className="h-8 max-w-[220px] rounded-[9px] bg-panel px-2 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent focus:outline-offset-0"
      >
        <option value="">the whole video</option>
        {spec.clips.map((c, i) => (
          <option key={c.id} value={c.id}>
            {i + 1}. {c.label || `Shot ${i + 1}`}
          </option>
        ))}
      </select>
    </label>
  );
}

/**
 * "Add voiceover" on the Current cut panel: the same chooser in a dialog; the
 * choice opens the Voiceover tab ready for it.
 */
export function VoiceoverButton({ spec, revisionId, clipId, compact }: {
  spec: Spec;
  revisionId: string;
  clipId?: string;
  /** A small button for the timeline's empty narration lane. */
  compact?: boolean;
}) {
  const dialog = useDialog();
  const [, setParams] = useSearchParams();
  const show = useOp('voiceover.show', { revisionId });
  const n = show.data?.beats.length ?? 0;
  const open = () =>
    void dialog.open<true>(
      (close) => (
        <ChooserDialog
          spec={spec}
          initial={{ clipId }}
          hasScript={n > 0}
          onChoose={(m, s, file) => {
            close(true);
            if (m === 'import' && file) handOffImport({ file, scope: s });
            const next = new URLSearchParams({ tab: 'voiceover', ...(m === 'import' ? {} : { mode: m }), ...(s.clipId ? { clip: s.clipId } : {}) });
            setParams(next);
          }}
        />
      ),
      { title: 'Add voiceover', size: 'lg' },
    );
  if (compact)
    return (
      <button
        type="button"
        onClick={open}
        className="inline-flex h-6 items-center gap-1 rounded-[7px] bg-white/[0.08] px-2 text-[11px] font-medium text-white/80 transition-colors hover:bg-white/[0.16] hover:text-white"
      >
        <Microphone size={11} aria-hidden /> Add voiceover
      </button>
    );
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button size="sm" variant="secondary" icon={<Microphone size={13} />} onClick={open}>
        Add voiceover
      </Button>
      {n > 0 ? (
        <button type="button" onClick={() => setParams(new URLSearchParams({ tab: 'voiceover' }))} className="text-[12.5px] font-medium text-accent-text hover:underline">
          {n === 1 ? 'Edit the voiceover (1 line)' : `Edit the voiceover (${n} lines)`}
        </button>
      ) : null}
    </div>
  );
}

/** The "Add voiceover" chooser in a dialog (here and on the Current cut panel). */
export function ChooserDialog({ spec, initial, hasScript, onChoose }: {
  spec: Spec;
  initial: Scope;
  hasScript: boolean;
  onChoose: (m: VoiceoverMode, s: Scope, file?: File) => void;
}) {
  const [scope, setScope] = useState<Scope>(initial);
  return (
    <div>
      <p className="mb-4 text-[13.5px] leading-relaxed text-muted">A voice that talks over the video. How do you want to make it?</p>
      <AddVoiceover clips={spec.clips} scope={scope} onScope={setScope} onChoose={onChoose} hasScript={hasScript} />
    </div>
  );
}
