import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Link, useSearchParams } from 'react-router';
import { ChatTeardropText, ClockCounterClockwise, FilmReel, HandWaving, Lock, MusicNotes, Play, Quotes, Scroll, Subtitles, TextAa, Warning } from '@phosphor-icons/react';
import { call, toApiError } from '../api/client';
import { useActivity } from '../api/activity';
import { useOp, useOpMutation } from '../api/queries';
import type { AssetSummary, ProjectShowOut, Render, RenderPlan, RevisionSummary, Spec } from '../api/types';
import { absolute, capFirst, duration as fmtDuration, plural, presetLabel, stamp, timecode, viaLabel } from '../lib/format';
import { updateSelection } from '../lib/workbench';
import { CutPreview, type CutPreviewHandle } from '../player/LazyCutPreview';
import { VoiceoverButton } from '../screens/studio/VoiceoverTab';
import { templateName } from '../screens/studio/cutsheet/script';
import { Badge, Button, cx, Details, ErrorPanel, Progress, Skeleton } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { Comments } from './Comments';
import { ApprovalBadge, ApprovalControls, RenderButtons } from './Review';
import { SignoffsWaitingLine } from './SignoffReminder';
import { ClipDetail, ClipStrip, EvidenceRow, layoutClips, SOUND_ROLE_WORDS } from './Timeline';
import { CaptionTools } from './transcripts/CaptionCheck';
import { CaptionLookPicker, captionLookName } from './studio/CaptionLook';
import { QAChip, useQAReportDialog } from './QAReport';
import { PreviewSwitch, renderName } from './studio/ReviewCopy';

/**
 * Reviewing one revision: the Remotion preview, its editorial treatment (notes,
 * rationale, the prompt that produced it), approval, comments pinned to the
 * playhead, and a timeline whose clips explain themselves with evidence.
 */
export function CutReview({ data, revisionId, onOpenSource, workspaceInstallations }: {
  data: ProjectShowOut;
  revisionId: string;
  onOpenSource: (assetId: string, t: number) => void;
  workspaceInstallations?: Map<string, string>;
}) {
  const rev = useOp('revision.show', { id: revisionId });
  const [plan, setPlan] = useState<RenderPlan | undefined>();
  const [time, setTime] = useState(0);
  const [clipId, setClipId] = useState<string | undefined>();
  const preview = useRef<CutPreviewHandle>(null);
  const assets = useMemo(() => new Map<string, AssetSummary>(data.assets.map((a) => [a.id, a])), [data.assets]);
  const spec = rev.data?.spec;
  const summary = rev.data?.summary ?? data.revisions.find((r) => r.id === revisionId);
  const [params, setParams] = useSearchParams();
  const wantClip = params.get('clip');
  const wantPlay = params.get('play') === '1';

  useEffect(() => {
    setPlan(undefined);
    setClipId(undefined);
    setTime(0);
    setChoice(undefined);
  }, [revisionId]);

  // This version's renders (D187): listed under the player and playable in
  // it; the newest finished one plays by default (the final render first),
  // the live preview when there is none.
  const versionRenders = useMemo(() => data.renders.filter((r) => r.revisionId === revisionId).sort((a, b) => b.createdAt.localeCompare(a.createdAt)), [data.renders, revisionId]);
  const ready = useMemo(
    () => versionRenders.filter((r) => r.state === 'available' && r.output).sort((a, b) => (a.quality === b.quality ? 0 : a.quality === 'final' ? -1 : 1)),
    [versionRenders],
  );
  const [choice, setChoice] = useState<string>();
  // Decided once per page: a render that finishes while someone watches is
  // listed with Play, never switched to under them.
  useEffect(() => {
    if (choice === undefined && rev.data) setChoice(ready[0]?.id ?? 'live');
  }, [choice, rev.data, ready]);
  const view = choice ?? ready[0]?.id ?? 'live';
  const showing = view === 'live' ? undefined : ready.find((r) => r.id === view);
  const playerBox = useRef<HTMLDivElement>(null);
  const setView = (v: string) => setChoice(v);

  const onFrame = useCallback((frame: number, fps: number) => setTime(frame / fps), []);
  const layout = useMemo(() => (spec ? layoutClips(spec, plan) : null), [spec, plan]);

  // The clip under the playhead follows playback unless one was picked.
  const playingClip = useMemo(() => layout?.items.find((it) => time >= it.start && time < it.end)?.clip.id, [layout, time]);
  const shownClipId = clipId ?? playingClip ?? spec?.clips[0]?.id;
  const shownClip = spec?.clips.find((c) => c.id === shownClipId);

  useEffect(() => {
    updateSelection({ revisionId, revisionNumber: summary?.number, clipId, time, timeBase: 'timeline', assetId: undefined, assetName: undefined });
  }, [revisionId, summary?.number, clipId, time]);

  const lineage = useMemo(() => {
    // Instructions that led here: follow parent links back from this revision.
    const byId = new Map(data.revisions.map((r) => [r.id, r]));
    const out: RevisionSummary[] = [];
    let cur = byId.get(revisionId);
    const seen = new Set<string>();
    while (cur && !seen.has(cur.id)) {
      seen.add(cur.id);
      out.push(cur);
      cur = cur.parentId ? byId.get(cur.parentId) : undefined;
    }
    return out;
  }, [data.revisions, revisionId]);

  const vertical = spec ? (spec.output.height ?? 1920) > (spec.output.width ?? 1080) : true;
  const seek = (s: number) => {
    preview.current?.seekToSeconds(s);
    setTime(s);
  };

  // ?clip=<id>&play=1 (from the Script view): start the preview at that clip
  // (where the render plan puts it; from the spec alone if the plan is slow).
  useEffect(() => {
    if (!wantClip || !layout) return;
    const item = layout.items.find((it) => it.clip.id === wantClip);
    const t = setTimeout(() => {
      if (item) {
        setClipId(item.clip.id);
        preview.current?.seekToSeconds(item.start + 0.001);
        setTime(item.start);
        if (wantPlay) preview.current?.play();
      }
      const next = new URLSearchParams(params);
      next.delete('clip');
      next.delete('play');
      setParams(next, { replace: true });
    }, plan ? 250 : 2500);
    return () => clearTimeout(t);
  }, [wantClip, wantPlay, layout, plan]); // eslint-disable-line react-hooks/exhaustive-deps

  if (rev.error) return <ErrorPanel error={rev.error} onRetry={() => void rev.refetch()} />;

  return (
    <div className="space-y-8">
      <div className={cx('grid grid-cols-1 gap-6', vertical ? 'lg:grid-cols-[minmax(300px,440px)_minmax(0,1fr)]' : 'xl:grid-cols-[minmax(0,1.55fr)_minmax(340px,1fr)]')}>
        <div className="min-w-0" ref={playerBox}>
          {ready.length ? <PreviewSwitch className="mb-2" renders={ready} value={showing ? showing.id : 'live'} onChange={setView} withVersion={false} /> : null}
          <CutPreview
            ref={preview}
            revisionId={revisionId}
            renders={data.renders}
            onFrame={onFrame}
            onPlan={setPlan}
            maxHeight={vertical ? 'min(74dvh, 760px)' : 'min(62dvh, 640px)'}
            fixCaptions={{ projectId: data.project.id }}
            playRender={showing}
          />
          <div className="mt-2 flex flex-wrap items-center justify-center gap-x-3 gap-y-1 text-[12px] text-subtle">
            <span className="tc">{timecode(time)}</span>
            <span aria-hidden>/</span>
            <span className="tc">{timecode(summary?.duration)}</span>
            {spec ? <span>{spec.output.preset ? presetLabel(spec.output.preset) : `${spec.output.width}×${spec.output.height}`}</span> : null}
            {spec && summary?.isHead ? <CropFraming spec={spec} revisionId={revisionId} /> : null}
            {spec && summary?.isHead ? <CaptionLookPicker spec={spec} projectId={data.project.id} /> : null}
          </div>
          {spec ? <CaptionTools projectId={data.project.id} revisionId={revisionId} /> : null}
          <VersionRenders
            renders={versionRenders}
            number={summary?.number}
            showing={showing?.id}
            onPlay={(r) => {
              setView(r.id);
              playerBox.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
            }}
          />
        </div>

        <div className="min-w-0 space-y-5">
          <ReviewPanel data={data} revisionId={revisionId} summary={summary} spec={spec} loading={rev.isLoading} time={time} onSeek={seek} workspaceInstallations={workspaceInstallations} lineage={lineage} clipId={clipId} />
        </div>
      </div>

      <section aria-labelledby="timeline-h">
        <div className="mb-3 flex items-center gap-3">
          <h2 id="timeline-h" className="text-[15px] font-semibold tracking-[-0.01em]">
            Timeline
          </h2>
          <span className="text-[12px] text-subtle">Click a clip to see why it is in the cut</span>
        </div>
        <div className="panel p-4">
          {spec ? (
            <ClipStrip spec={spec} plan={plan} assets={assets} time={time} onSeek={seek} selectedClipId={shownClipId} onSelectClip={setClipId} />
          ) : (
            <Skeleton className="h-28" />
          )}
        </div>
        <div className="mt-5 grid grid-cols-1 items-start gap-5 xl:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)]">
          <div className="panel p-5">
            {shownClip ? (
              <ClipDetail
                clip={shownClip}
                asset={assets.get(shownClip.assetId)}
                assets={assets}
                timelineStart={layout?.items.find((i) => i.clip.id === shownClip.id)?.start}
                onOpenSource={onOpenSource}
                onPlayFrom={(s) => {
                  seek(s);
                  preview.current?.play();
                }}
              />
            ) : (
              <Skeleton className="h-40" />
            )}
          </div>
          <div className="space-y-5">
            {spec ? <Layers spec={spec} assets={assets} onOpenSource={onOpenSource} onSeek={seek} /> : <Skeleton className="h-40 rounded-[16px]" />}
          </div>
        </div>
      </section>
    </div>
  );
}

/**
 * A cut's editorial treatment (title, notes, why, approval, renders), the
 * things to check before it renders, the prompts that led to it and its comments: the right
 * column of a cut's review, also the Review panel of the studio editor.
 */
export function ReviewPanel({ data, revisionId, summary, spec, loading, time, onSeek, workspaceInstallations, lineage, clipId }: {
  data: ProjectShowOut;
  revisionId: string;
  summary?: RevisionSummary;
  spec?: Spec;
  loading: boolean;
  time: number;
  onSeek: (s: number) => void;
  workspaceInstallations?: Map<string, string>;
  lineage: RevisionSummary[];
  /** The clip in view, offered as "One clip" when adding a voiceover. */
  clipId?: string;
}) {
  const scriptLink = `/p/${data.project.id}?tab=script${summary && !summary.isHead ? `&rev=${summary.id}` : ''}`;
  return (
    <>
      <section className="panel p-5" aria-label="Editorial treatment">
        {summary ? (
          <>
            <div className="flex flex-wrap items-center gap-2">
              <span className="tc text-[13px] font-semibold text-fg">r{summary.number}</span>
              <ApprovalBadge rev={summary} />
              {summary.isHead ? <Badge tone="accent">latest</Badge> : null}
              <span className="ml-auto text-[11.5px] text-subtle" title={absolute(summary.createdAt)}>
                {summary.createdBy.userName ?? 'Someone'} via {viaLabel(summary.createdBy.via, summary.origin)}
                {workspaceInstallations?.get(summary.createdBy.installationId) ? ` on ${workspaceInstallations.get(summary.createdBy.installationId)}` : ''}, <span data-stamp>{stamp(summary.createdAt)}</span>
              </span>
            </div>
            <h2 className="mt-3 text-[21px] font-semibold leading-snug tracking-[-0.02em] text-fg">{spec?.title || summary.title || 'Untitled cut'}</h2>
            <div className="mt-1 text-[12.5px] text-subtle">
              {fmtDuration(summary.duration)}, {plural(summary.clips, 'clip')}, {plural(summary.overlays, 'graphic')}
              {spec?.captions?.enabled ? ', captions on' : ''}
            </div>
            {spec?.notes ? (
              <blockquote className="mt-4 rounded-[12px] bg-accent-soft px-4 py-3 text-[14px] leading-relaxed text-fg">
                <div className="mb-1 flex items-center gap-1.5 text-[11.5px] font-medium text-accent-text">
                  <TextAa size={12} /> Notes
                </div>
                {spec.notes}
              </blockquote>
            ) : loading ? (
              <Skeleton className="mt-4 h-16" />
            ) : null}
            {summary.rationale ? (
              <div className="mt-4">
                <div className="text-[11.5px] font-medium text-subtle">Why this version</div>
                <p className="mt-1 text-[13.5px] leading-relaxed text-fg">{summary.rationale}</p>
              </div>
            ) : null}
            {summary.isHead ? <SignoffsWaitingLine projectId={data.project.id} branch={summary.branch} className="mt-4" /> : null}
            <div className="mt-5 flex flex-wrap items-center gap-2 border-t border-line pt-4">
              <ApprovalControls rev={summary} />
              <AskForReview rev={summary} />
              <div className="flex-1" />
              <Link
                to={scriptLink}
                className="inline-flex h-8 items-center gap-1.5 rounded-[9px] px-2.5 text-[12.5px] font-medium text-muted transition-colors hover:bg-hover hover:text-fg"
                data-view-script
              >
                <Scroll size={14} /> View script
              </Link>
              <RenderButtons revisionId={summary.id} size="sm" brief={data.project.brief} output={summary.output} />
            </div>
            {spec ? (
              <div className="mt-3 border-t border-line pt-3">
                <VoiceoverButton spec={spec} revisionId={revisionId} clipId={clipId} />
              </div>
            ) : null}
            {summary.approval.note || summary.approval.by ? (
              <div className="mt-3 rounded-[10px] px-3 py-2 text-[12.5px] text-muted hairline">
                {summary.approval.state !== 'draft' ? (
                  <span>
                    {summary.approval.state === 'approved'
                      ? `Approved by ${summary.approval.by?.userName ?? 'someone'}`
                      : `${summary.approval.by?.userName ?? 'Someone'} said it needs changes`}
                    {summary.approval.at ? `, ${stamp(summary.approval.at)}` : ''}
                  </span>
                ) : null}
                {summary.approval.note ? <div className="mt-0.5 text-fg">“{summary.approval.note}”</div> : null}
              </div>
            ) : null}
          </>
        ) : (
          <div className="space-y-3">
            <Skeleton className="h-5 w-24" />
            <Skeleton className="h-7 w-2/3" />
            <Skeleton className="h-16" />
          </div>
        )}
      </section>

      {summary?.warnings?.length ? (
        <section className="rounded-[14px] p-4 hairline" aria-label="Things to check before it renders">
          <div className="mb-2 flex items-center gap-1.5 text-[12.5px] font-medium text-accent-text">
            <Warning size={14} weight="fill" /> {summary.warnings.length === 1 ? 'One thing to check before it renders' : `${summary.warnings.length} things to check before it renders`}
          </div>
          <ul className="space-y-1.5">
            {summary.warnings.map((w, i) => (
              <li key={i} className="text-[12.5px] leading-relaxed text-muted">
                {w.message}
              </li>
            ))}
          </ul>
          <Details compact className="mt-2.5">
            {summary.warnings.map((w, i) => (
              <div key={i} className="tc">
                {summary.warnings!.length > 1 ? `${i + 1}. ` : ''}
                {w.code}
                {w.path ? ` · ${w.path}` : ''}
              </div>
            ))}
          </Details>
        </section>
      ) : null}

      <section className="rounded-[14px] p-4 hairline" aria-label="What was asked">
        <div className="mb-2 flex items-center gap-1.5 text-[12.5px] font-medium text-muted">
          <ClockCounterClockwise size={14} /> What was asked, version by version
        </div>
        {lineage.length === 0 ? (
          <div className="text-[12.5px] text-subtle">No history.</div>
        ) : (
          <ol className="space-y-2.5">
            {lineage.map((r) => (
              <li key={r.id} className="flex gap-3">
                <Link to={`/p/${r.projectId}/r/${r.id}`} className={cx('tc w-8 shrink-0 pt-[1px] text-[12px] hover:underline', r.id === revisionId ? 'font-semibold text-accent-text' : 'text-subtle')}>
                  r{r.number}
                </Link>
                <div className="min-w-0 text-[13px] leading-relaxed">
                  {r.instruction && r.origin?.startsWith('timeline:') ? (
                    <span className="text-fg">{r.instruction}</span>
                  ) : r.instruction ? (
                    <span className="text-fg">“{r.instruction}”</span>
                  ) : (
                    <span className="text-subtle">{r.title || 'No instruction recorded'}</span>
                  )}
                  <div className="text-[11px] text-subtle" title={absolute(r.createdAt)}>
                    {capFirst(viaLabel(r.createdBy.via, r.origin))}, <span data-stamp>{stamp(r.createdAt)}</span>
                  </div>
                </div>
              </li>
            ))}
          </ol>
        )}
      </section>

      <section className="panel p-4" aria-label="Comments">
        <Comments projectId={data.project.id} revisionId={revisionId} time={time} onSeek={onSeek} revisionNumberFor={(id) => data.revisions.find((r) => r.id === id)?.number} />
      </section>
    </>
  );
}

/**
 * Ask for review (D188): a version someone edited by hand waits in "Awaiting
 * your review" only when someone asks (or asks for its review copy). Claude's
 * versions wait by themselves.
 */
function AskForReview({ rev }: { rev: RevisionSummary }) {
  const toast = useToast();
  const ask = useOpMutation('revision.askReview', { invalidate: ['project.show', 'revision.list', 'revision.show', 'review.waiting'] });
  if (rev.approval.state !== 'draft' || rev.byClaude) return null;
  if (rev.reviewAsked)
    return (
      <span className="text-[12px] text-subtle" title={absolute(rev.reviewAsked.at)} data-review-asked>
        Waiting for review since {stamp(rev.reviewAsked.at)}
      </span>
    );
  return (
    <Button
      size="md"
      variant="ghost"
      icon={<HandWaving size={15} />}
      loading={ask.isPending}
      onClick={() =>
        ask.mutate(
          { id: rev.id },
          {
            onSuccess: (r) => toast({ tone: 'success', title: `Version ${r.number} is waiting for review`, body: 'It shows in Awaiting your review on the Studio page.' }),
            onError: (e) => toast({ tone: 'error', title: 'Could not ask for review', body: [e.message, e.hint].filter(Boolean).join(' ') }),
          },
        )
      }
      data-ask-review
    >
      Ask for review
    </Button>
  );
}

/**
 * A version's renders (D187): its review copies and final renders, when they
 * were made, how they are doing, and Play to watch one in the player above.
 */
function VersionRenders({ renders, number, showing, onPlay }: { renders: Render[]; number?: number; showing?: string; onPlay: (r: Render) => void }) {
  const act = useActivity();
  const openQA = useQAReportDialog();
  if (!renders.length) return null;
  return (
    <section className="mt-4 rounded-[14px] p-3 hairline" aria-label={`Renders of version ${number ?? ''}`} data-version-renders>
      <div className="mb-1.5 flex items-center gap-1.5 px-1 text-[12.5px] font-medium text-muted">
        <FilmReel size={14} /> Renders of this version
      </div>
      <ul className="space-y-0.5">
        {renders.map((r) => {
          const it = act.data?.items.find((x) => x.renderId === r.id);
          const busy = r.state === 'queued' || r.state === 'rendering' || r.state === 'uploading';
          const when = r.completedAt ?? r.createdAt;
          const state =
            r.state === 'available'
              ? 'ready'
              : r.state === 'failed'
                ? "didn't finish"
                : r.state === 'canceled'
                  ? r.follow
                    ? 'not made: a newer version came first'
                    : 'stopped'
                  : it?.waiting?.text && it.state === 'waiting'
                    ? it.waiting.text
                    : r.state === 'queued'
                      ? 'waiting to start'
                      : it?.pct != null
                        ? `making it, ${Math.round(it.pct * 100)}%`
                        : 'making it';
          return (
            <li key={r.id} data-version-render={r.id} className={cx('flex flex-wrap items-center gap-x-2 gap-y-1 rounded-[10px] px-2 py-1.5', showing === r.id && 'bg-accent-soft')}>
              <span className="text-[13px] font-medium text-fg">{renderName(r)}</span>
              <span className="text-[12px] text-muted" title={absolute(when)} data-stamp>
                {stamp(when)}
              </span>
              <span className={cx('min-w-0 truncate text-[12px]', r.state === 'failed' ? 'text-danger' : busy ? 'text-accent-text' : 'text-subtle')}>
                {state}
                {r.follow && r.state !== 'canceled' ? ', made by itself' : ''}
              </span>
              {r.qa ? <QAChip verdict={r.qa.verdict} errors={r.qa.errors} warnings={r.qa.warnings} onOpen={() => void openQA(r.qa!.reportId)} /> : null}
              <span className="ml-auto flex items-center gap-1">
                {r.state === 'available' && r.output ? (
                  <Button size="sm" variant={showing === r.id ? 'secondary' : 'ghost'} icon={<Play size={12} weight="fill" />} onClick={() => onPlay(r)} disabled={showing === r.id} data-play-render>
                    {showing === r.id ? 'Playing' : 'Play'}
                  </Button>
                ) : null}
              </span>
              {busy ? <Progress className="w-full" value={it?.pct ?? 0} indeterminate={it?.pct == null || r.state === 'queued'} label={`${renderName(r)} progress`} /> : null}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

type CropFramingValue = 'faces' | 'follow' | 'center';

const FRAMING_SAID: Record<CropFramingValue, string> = {
  faces: 'Crops keep faces in frame',
  follow: 'Crops follow movement',
  center: 'Crops keep the middle of the picture',
};

/**
 * How the cut's crops are framed (output.framing): the middle, the main face
 * held in frame (new cuts say so), or following the subject as it moves
 * (D115, only ever chosen by a person). Older cuts keep their framing until a
 * person changes it here, which saves a new version (the same setOutput edit
 * as a cut sheet's `framing:` line, or `metarush cut framing`).
 */
export function CropFraming({ spec, revisionId, disabled }: { spec: Spec; revisionId: string; disabled?: boolean }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const now: CropFramingValue = spec.output.framing === 'faces' || spec.output.framing === 'follow' ? spec.output.framing : 'center';
  const save = (framing: CropFramingValue, base: string, undoOf?: Spec['output']) => {
    setBusy(true);
    const output = undoOf ?? { ...spec.output, framing };
    const instruction = undoOf ? 'Undo the framing change' : framing === 'faces' ? 'Keep faces in frame' : framing === 'follow' ? 'Follow movement' : 'Keep the middle of the picture';
    return call('edit.apply', { plan: { baseRevisionId: base, origin: 'framing:studio', instruction, ops: [{ op: 'setOutput', output }] } })
      .then((res) => {
        for (const key of ['project.show', 'project.list', 'revision.list', 'revision.show', 'cut.show', 'revision.plan']) void qc.invalidateQueries({ queryKey: [key] });
        return res;
      })
      .finally(() => setBusy(false));
  };
  const choose = (framing: CropFramingValue) => {
    const before = spec.output;
    save(framing, revisionId)
      .then((res) =>
        toast({
          tone: 'success',
          title: FRAMING_SAID[framing],
          body: `Saved as version ${res.revision.number}.${framing === 'center' ? '' : framing === 'follow' ? ' The crop moves when the subject moves and stays put when they stand still. Clips with a framing of their own keep it.' : ' Clips with a focus of their own keep it.'}`,
          action: {
            label: 'Undo',
            onClick: () =>
              void save(now, res.revision.id, before)
                .then(() => toast({ title: 'Framing put back' }))
                .catch((e: unknown) => toast({ tone: 'error', title: 'Not put back', body: toApiError(e).message })),
          },
        }),
      )
      .catch((e: unknown) => {
        const ae = toApiError(e);
        toast({ tone: 'error', title: 'Framing not changed', body: [ae.message, ae.hint].filter(Boolean).join(' ') });
      });
  };
  const link = (framing: CropFramingValue, label: string, title: string) => (
    <button
      key={framing}
      type="button"
      disabled={busy || disabled}
      onClick={() => choose(framing)}
      title={title}
      data-framing-choice={framing}
      className="rounded-[7px] px-1.5 py-0.5 font-medium text-accent-text hover:bg-hover disabled:opacity-50"
    >
      {label}
    </button>
  );
  return (
    <span className="flex basis-full flex-wrap items-center justify-center gap-1.5" data-faces-in-frame={now === 'center' ? 'off' : 'on'} data-crop-framing={now}>
      {now === 'faces' ? <span>Faces kept in frame</span> : now === 'follow' ? <span>Crops follow movement</span> : null}
      {now !== 'faces' ? link('faces', now === 'follow' ? 'Hold still' : 'Keep faces in frame', 'Crops keep the main face of each clip in frame, in one place (a new version)') : null}
      {now !== 'follow' ? link('follow', 'Follow movement', 'Crops move with the subject as it moves: the main face, or what stands out where no face is seen (a new version)') : null}
      {now !== 'center' ? link('center', 'Turn off', 'Crops keep the middle of the picture instead (a new version)') : null}
    </span>
  );
}

function paramSummary(params: Record<string, unknown>): string {
  for (const k of ['text', 'headline', 'title', 'name', 'value', 'label']) {
    const v = params[k];
    if (typeof v === 'string' && v) return v;
  }
  return '';
}

export function Layers({ spec, assets, onOpenSource, onSeek }: { spec: Spec; assets: Map<string, AssetSummary>; onOpenSource: (assetId: string, t: number) => void; onSeek: (s: number) => void }) {
  const overlays = spec.overlays ?? [];
  const audio = spec.audio ?? [];
  const cap = spec.captions;
  return (
    <>
      <section className="panel p-5" aria-label="Graphics">
        <div className="mb-3 flex items-center gap-2 text-[13.5px] font-semibold">
          <Quotes size={15} className="text-subtle" /> Graphics <span className="tc text-[11.5px] font-normal text-subtle">{overlays.length}</span>
        </div>
        {overlays.length === 0 ? (
          <div className="text-[12.5px] text-subtle">No graphics in this cut.</div>
        ) : (
          <ul className="space-y-2">
            {overlays.map((o) => {
              const text = paramSummary(o.params as Record<string, unknown>);
              return (
                <li key={o.id} className="rounded-[12px] p-3 hairline">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge tone="accent">{o.motion ? `Motion graphic ${o.motion.name}` : o.template ? templateName(o.template) : 'Graphic'}</Badge>
                    <button type="button" onClick={() => onSeek(o.at)} className="tc text-[11.5px] text-accent-text hover:underline">
                      {timecode(o.at)}
                    </button>
                    <span className="tc text-[11.5px] text-subtle">for {o.duration}s</span>
                    {o.locked ? <Lock size={11} weight="fill" className="text-subtle" /> : null}
                    <span className="tc ml-auto text-[11px] text-subtle">{o.id}</span>
                  </div>
                  {text ? <div className="mt-1.5 text-[13.5px] font-medium text-fg">{text}</div> : null}
                  {o.rationale ? <div className="mt-1 text-[12.5px] text-muted">{o.rationale}</div> : null}
                  {o.evidence?.length ? (
                    <ul className="mt-2 space-y-1.5">
                      {o.evidence.map((ev, i) => (
                        <EvidenceRow key={i} ev={ev} assetName={ev.assetId ? assets.get(ev.assetId)?.title : undefined} onOpen={onOpenSource} />
                      ))}
                    </ul>
                  ) : null}
                </li>
              );
            })}
          </ul>
        )}
      </section>
      <section className="grid grid-cols-1 gap-5 sm:grid-cols-2 xl:grid-cols-1 2xl:grid-cols-2">
        <div className="rounded-[16px] p-4 hairline" aria-label="Captions">
          <div className="mb-2 flex items-center gap-2 text-[13px] font-semibold">
            <Subtitles size={15} className="text-subtle" /> Captions
          </div>
          {cap?.enabled ? (
            <div className="space-y-1 text-[12.5px] text-muted">
              <div>
                “{captionLookName(cap.style)}”, {cap.size ?? 'medium'}, {cap.position === 'top' ? 'at the top' : cap.position === 'middle' ? 'in the middle' : cap.position === 'bottom' ? 'at the very bottom' : 'low on the screen'}
                {cap.uppercase ? ', uppercase' : ''}
                {cap.maxWordsPerLine ? `, ${cap.maxWordsPerLine} words per line` : ''}
              </div>
              <div>{cap.source === 'manual' ? 'Timing set by hand' : 'Timing from the transcripts'}</div>
              {cap.emphasis?.length ? (
                <div className="flex flex-wrap gap-1 pt-1">
                  {cap.emphasis.map((w) => (
                    <Badge key={w} tone="accent">
                      {w}
                    </Badge>
                  ))}
                </div>
              ) : null}
            </div>
          ) : (
            <div className="text-[12.5px] text-subtle">Off</div>
          )}
        </div>
        <div className="rounded-[16px] p-4 hairline" aria-label="Audio">
          <div className="mb-2 flex items-center gap-2 text-[13px] font-semibold">
            <MusicNotes size={15} className="text-subtle" /> Music and sound
          </div>
          {audio.length === 0 ? (
            <div className="text-[12.5px] text-subtle">Only the clips' own sound</div>
          ) : (
            <ul className="space-y-2">
              {audio.map((a) => (
                <li key={a.id} className="text-[12.5px] text-muted">
                  <div className="flex items-center gap-2">
                    <Badge tone="info">{SOUND_ROLE_WORDS[a.role] ?? a.role}</Badge>
                    <span className="truncate text-fg">{assets.get(a.assetId)?.title ?? 'A sound file'}</span>
                  </div>
                  <div className="tc mt-1 text-[11.5px] text-subtle">
                    at {timecode(a.at)}, {a.gainDb ?? 0} dB{a.duckUnderDialogue ? ', dips under voices' : ''}
                  </div>
                  {a.rights?.license ? <div className="mt-0.5 text-[11.5px] text-subtle">License: {a.rights.license}</div> : null}
                </li>
              ))}
            </ul>
          )}
        </div>
      </section>
      <div className="flex items-center gap-1.5 px-1 text-[11.5px] text-subtle">
        <ChatTeardropText size={12} /> Change the cut in the Edit tab, or ask Claude Code.
      </div>
    </>
  );
}
