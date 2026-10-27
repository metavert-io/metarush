import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { ArrowsLeftRight, CaretRight, DownloadSimple, Equals, Minus, PencilSimpleLine, Plus as PlusIcon } from '@phosphor-icons/react';
import { media } from '../api/client';
import { useInstallationNames, useOp } from '../api/queries';
import type { AssetSummary, Diff, ProjectShowOut, RenderPlan, RevisionSummary, Spec } from '../api/types';
import { duration as fmtDuration, signedDuration } from '../lib/format';
import { updateSelection, resetSelection, useProvideDropTarget } from '../lib/workbench';
import { CutReview } from '../components/CutReview';
import { ApprovalBadge, ApprovalControls, RenderButtons, RevisionRow } from '../components/Review';
import { RenderDownload } from '../components/SignoffReminder';
import { ClipStrip, type ClipMark } from '../components/Timeline';
import { CutPreview } from '../player/LazyCutPreview';
import { templateName } from './studio/cutsheet/script';
import { Badge, Button, cx, ErrorPanel, Freshness, Skeleton } from '../ui/primitives';
import { PendingFixesBanner } from '../components/transcripts/PendingFixes';
import { ArchivedProject } from '../components/project/ProjectDelete';
import { MadeWith } from '../components/recipes/MadeWith';
import { DeliveryNotes } from '../components/DeliveryNotes';
import { PostToYouTube } from '../components/PostToYouTube';
import { CaptionLanguages } from '../components/transcripts/CaptionLanguages';
import { VersionCost } from '../components/project/Receipt';
import { ShipGate } from '../components/ShipGate';
import { ShareForReview } from '../components/ShareForReview';

function useProject(projectId: string) {
  const show = useOp('project.show', { id: projectId });
  useProvideDropTarget(projectId, show.data?.project.name);
  useEffect(() => {
    if (!show.data) return;
    const head = show.data.revisions.find((r) => r.id === show.data!.headRevisionId);
    updateSelection({ projectId, projectName: show.data.project.name, headRevisionId: head?.id, headRevisionNumber: head?.number });
  }, [show.data, projectId]);
  return show;
}

function Crumbs({ data, tail, updatedAt, fetching, onRefresh }: { data: ProjectShowOut; tail: React.ReactNode; updatedAt?: number; fetching?: boolean; onRefresh?: () => void }) {
  return (
    <nav aria-label="Breadcrumb" className="mb-4 flex items-center gap-1.5 text-[12.5px] text-subtle">
      <Link to="/" className="hover:text-fg">
        Studio
      </Link>
      <CaretRight size={11} />
      <Link to={`/p/${data.project.id}`} className="truncate hover:text-fg">
        {data.project.name}
      </Link>
      <CaretRight size={11} />
      <span className="truncate text-muted">{tail}</span>
      {updatedAt ? <Freshness className="ml-auto" at={updatedAt} fetching={fetching} onRefresh={onRefresh} /> : null}
    </nav>
  );
}

/* ---------- /p/:projectId/r/:revisionId ---------- */

export function RevisionPage() {
  const { projectId = '', revisionId = '' } = useParams();
  const navigate = useNavigate();
  const show = useProject(projectId);
  const machines = useInstallationNames();
  const [picked, setPicked] = useState<string[]>([]);
  useEffect(() => () => resetSelection({}), []);
  useEffect(() => setPicked([revisionId]), [revisionId]);

  if (show.isLoading) return <PageSkeleton />;
  if (show.error || !show.data) return <ErrorPanel error={show.error} onRetry={() => void show.refetch()} />;
  const data = show.data;
  if (data.project.archived) return <ArchivedProject project={data.project} />; // deleted (D196)
  const rev = data.revisions.find((r) => r.id === revisionId);
  const openSource = (assetId: string, t: number) => navigate(`/p/${projectId}?tab=sources&asset=${assetId}&t=${t.toFixed(3)}`);
  const compare = () => {
    const [a, b] = picked.map((id) => data.revisions.find((r) => r.id === id)!).sort((x, y) => x.number - y.number);
    if (a && b) navigate(`/p/${projectId}/compare?left=${a.id}&right=${b.id}`);
  };

  return (
    <div>
      <Crumbs data={data} tail={rev ? `Version ${rev.number}` : 'Version'} updatedAt={show.dataUpdatedAt} fetching={show.isFetching} onRefresh={() => void show.refetch()} />
      <div className="grid grid-cols-1 gap-8 xl:grid-cols-[280px_minmax(0,1fr)]">
        <aside aria-label="Versions" className="order-2 xl:order-1">
          <div className="mb-2 flex items-center gap-2">
            <h2 className="text-[14px] font-semibold">Versions</h2>
            <Button size="sm" variant={picked.length === 2 ? 'primary' : 'ghost'} className="ml-auto" disabled={picked.length !== 2} icon={<ArrowsLeftRight size={13} />} onClick={compare}>
              Compare
            </Button>
          </div>
          <p className="mb-2 text-[11.5px] text-subtle">Tick two versions to compare them.</p>
          <div className="space-y-1 xl:sticky xl:top-[calc(var(--mr-header-h)+23px)] xl:max-h-[calc(100dvh-140px)] xl:overflow-y-auto">
            {data.revisions.map((r) => (
              <RevisionRow
                key={r.id}
                rev={r}
                to={`/p/${projectId}/r/${r.id}`}
                active={r.id === revisionId}
                compareChecked={picked.includes(r.id)}
                onCompareToggle={(on) => setPicked((p) => (on ? [...p.filter((x) => x !== r.id), r.id].slice(-2) : p.filter((x) => x !== r.id)))}
                installationName={(id) => machines.get(id)}
              />
            ))}
          </div>
        </aside>
        <div className="order-1 min-w-0 xl:order-2">
          {rev?.draftId && !rev.isHead && rev.approval.state === 'draft' ? (
            // An AI app's draft (D249): not in the cut until a person approves it on its review page.
            <div className="mb-4 flex flex-wrap items-center gap-2 rounded-[12px] bg-info-soft px-4 py-2.5 text-[13px] text-info" data-draft-banner>
              Version {rev.number} is {rev.agentName ?? 'an AI app'}'s draft: it isn't in the cut until someone approves it.
              <Link to={`/review/${rev.draftId}`} className="font-medium underline-offset-2 hover:underline">
                Review the draft
              </Link>
            </div>
          ) : rev && !rev.isHead ? (
            <div className="mb-4 flex flex-wrap items-center gap-2 rounded-[12px] bg-info-soft px-4 py-2.5 text-[13px] text-info">
              You are looking at version {rev.number}, not the latest one.
              {data.headRevisionId ? (
                <Link to={`/p/${projectId}/compare?left=${rev.id}&right=${data.headRevisionId}`} className="font-medium underline-offset-2 hover:underline">
                  Compare with the latest
                </Link>
              ) : null}
            </div>
          ) : null}
          {rev?.isHead ? <PendingFixesBanner projectId={projectId} className="mb-4" /> : null}
          {rev ? <MadeWith projectId={projectId} revision={rev} className="mb-4" /> : null}
          {rev ? <ShipGate projectId={projectId} revisionId={rev.id} className="mb-4" /> : null}
          <CutReview data={data} revisionId={revisionId} onOpenSource={openSource} workspaceInstallations={machines} />
          {rev ? <VersionCost projectId={projectId} revisionId={rev.id} number={rev.number} /> : null}
          {rev ? <ShareForReview projectId={projectId} revisionId={rev.id} number={rev.number} /> : null}
          {rev ? <DeliveryNotes projectId={projectId} revisionId={rev.id} number={rev.number} /> : null}
          {rev ? <CaptionLanguages projectId={projectId} revisionId={rev.id} number={rev.number} /> : null}
          {rev ? <PostToYouTube projectId={projectId} revisionId={rev.id} number={rev.number} /> : null}
        </div>
      </div>
    </div>
  );
}

/* ---------- /p/:projectId/compare?left&right ---------- */

export function ComparePage() {
  const { projectId = '' } = useParams();
  const [params, setParams] = useSearchParams();
  const show = useProject(projectId);
  const revs = show.data?.revisions ?? [];
  const left = params.get('left') ?? revs[1]?.id ?? '';
  const right = params.get('right') ?? revs[0]?.id ?? '';
  const valid = !!left && !!right && left !== right;
  const cmp = useOp('revision.compare', valid ? { left, right } : null);
  const leftShow = useOp('revision.show', left ? { id: left } : null);
  const rightShow = useOp('revision.show', right ? { id: right } : null);
  const [leftPlan, setLeftPlan] = useState<RenderPlan>();
  const [rightPlan, setRightPlan] = useState<RenderPlan>();
  useEffect(() => () => resetSelection({}), []);

  const lRev = revs.find((r) => r.id === left);
  const rRev = revs.find((r) => r.id === right);
  useEffect(() => {
    if (lRev && rRev) updateSelection({ compare: { left: lRev.id, right: rRev.id, leftNumber: lRev.number, rightNumber: rRev.number }, revisionId: rRev.id, revisionNumber: rRev.number });
    return () => updateSelection({ compare: undefined });
  }, [lRev, rRev]);

  const assets = useMemo(() => new Map<string, AssetSummary>((show.data?.assets ?? []).map((a) => [a.id, a])), [show.data]);
  const diff = cmp.data?.diff;
  const { leftMarks, rightMarks } = useMemo(() => marksFor(diff), [diff]);

  if (show.isLoading) return <PageSkeleton />;
  if (show.error || !show.data) return <ErrorPanel error={show.error} onRetry={() => void show.refetch()} />;
  const data = show.data;
  if (data.project.archived) return <ArchivedProject project={data.project} />; // deleted (D196)
  const set = (k: 'left' | 'right', v: string) => {
    const next = new URLSearchParams(params);
    next.set(k, v);
    if (!next.get(k === 'left' ? 'right' : 'left')) next.set(k === 'left' ? 'right' : 'left', k === 'left' ? right : left);
    setParams(next, { replace: true });
  };

  return (
    <div>
      <Crumbs data={data} tail="Compare" updatedAt={cmp.dataUpdatedAt || undefined} fetching={cmp.isFetching} onRefresh={() => void cmp.refetch()} />
      <div className="mb-6 flex flex-wrap items-end gap-3">
        <h1 className="mr-2 text-[24px] font-semibold tracking-[-0.025em]">Compare versions</h1>
        <RevSelect label="Before" value={left} revs={revs} onChange={(v) => set('left', v)} />
        <Button
          variant="ghost"
          size="md"
          aria-label="Swap sides"
          icon={<ArrowsLeftRight size={15} />}
          onClick={() => {
            const next = new URLSearchParams(params);
            next.set('left', right);
            next.set('right', left);
            setParams(next, { replace: true });
          }}
        >
          Swap
        </Button>
        <RevSelect label="After" value={right} revs={revs} onChange={(v) => set('right', v)} />
      </div>

      {revs.length < 2 ? (
        <div className="rounded-[14px] px-4 py-6 text-[13px] text-muted hairline">This project needs at least two versions to compare.</div>
      ) : !valid ? (
        <div className="rounded-[14px] px-4 py-6 text-[13px] text-muted hairline">Pick two different versions.</div>
      ) : cmp.error ? (
        <ErrorPanel error={cmp.error} onRetry={() => void cmp.refetch()} />
      ) : (
        <>
          <DiffSummary diff={diff} loading={cmp.isLoading} />
          <div className="mt-6 grid grid-cols-1 gap-6 xl:grid-cols-2">
            {[
              { side: 'Before', rev: lRev, spec: leftShow.data?.spec, plan: leftPlan, setPlan: setLeftPlan, marks: leftMarks },
              { side: 'After', rev: rRev, spec: rightShow.data?.spec, plan: rightPlan, setPlan: setRightPlan, marks: rightMarks },
            ].map((s) => (
              <section key={s.side} className="panel min-w-0 p-4 sm:p-5" aria-label={`${s.side}: version ${s.rev?.number ?? ''}`}>
                <div className="mb-3 flex flex-wrap items-center gap-2">
                  <span className="text-[11.5px] font-medium text-subtle">{s.side}</span>
                  <span className="tc text-[14px] font-semibold">r{s.rev?.number}</span>
                  {s.rev ? <ApprovalBadge rev={s.rev} /> : null}
                  {s.rev?.isHead ? <Badge tone="accent">latest</Badge> : null}
                  <span className="ml-auto text-[12px] text-subtle">{s.rev ? fmtDuration(s.rev.duration) : ''}</span>
                </div>
                <div className="truncate text-[14px] font-medium text-fg">{s.rev?.title || 'Untitled'}</div>
                {s.rev?.instruction ? <div className="clamp-2 mt-0.5 text-[12.5px] text-muted">“{s.rev.instruction}”</div> : null}
                <div className="mt-4">{s.rev ? <CutPreview revisionId={s.rev.id} renders={data.renders} onPlan={s.setPlan} maxHeight="min(46dvh, 460px)" /> : null}</div>
                <div className="mt-4">{s.spec ? <ClipStrip spec={s.spec} plan={s.plan} assets={assets} marks={s.marks} compact /> : <Skeleton className="h-24" />}</div>
                {s.rev ? <SideActions rev={s.rev} data={data} /> : null}
              </section>
            ))}
          </div>
          <ChangedFields diff={diff} names={namesIn([leftShow.data?.spec, rightShow.data?.spec], assets)} />
        </>
      )}
    </div>
  );
}

function marksFor(diff: Diff | undefined) {
  const leftMarks = new Map<string, ClipMark>();
  const rightMarks = new Map<string, ClipMark>();
  if (!diff) return { leftMarks, rightMarks };
  for (const id of diff.clipsRemoved) leftMarks.set(id, 'removed');
  for (const id of diff.clipsAdded) rightMarks.set(id, 'added');
  for (const f of diff.clipsChanged) {
    leftMarks.set(f.id, 'changed');
    rightMarks.set(f.id, 'changed');
  }
  for (const id of diff.overlaysAdded) rightMarks.set(`overlay:${id}`, 'added');
  return { leftMarks, rightMarks };
}

function RevSelect({ label, value, revs, onChange }: { label: string; value: string; revs: RevisionSummary[]; onChange: (v: string) => void }) {
  return (
    <label className="flex flex-col gap-1 text-[11.5px] font-medium text-subtle">
      {label}
      <select value={value} onChange={(e) => onChange(e.target.value)} className="h-9 min-w-[200px] max-w-[280px] rounded-[10px] bg-panel px-3 text-[13px] text-fg hairline">
        {revs.map((r) => (
          <option key={r.id} value={r.id}>
            r{r.number} {r.title ? `· ${r.title}` : ''} {r.isHead ? '(latest)' : ''}
          </option>
        ))}
      </select>
    </label>
  );
}

function DiffSummary({ diff, loading }: { diff: Diff | undefined; loading: boolean }) {
  if (loading || !diff)
    return (
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-20 rounded-[14px]" />
        ))}
      </div>
    );
  const delta = diff.durationAfter - diff.durationBefore;
  const flags = [
    diff.reordered && 'clips reordered',
    diff.captionsChanged && 'captions changed',
    diff.outputChanged && 'video size or format changed',
    diff.brandChanged && 'brand kit changed',
    diff.notesChanged && 'notes changed',
    (diff.audioAdded.length || diff.audioRemoved.length || diff.audioChanged.length) && 'sound changed',
    (diff.overlaysAdded.length || diff.overlaysRemoved.length || diff.overlaysChanged.length) && 'graphics changed',
  ]
    .filter(Boolean)
    .filter((f) => !diff.summary.some((s) => s.toLowerCase().includes(String(f)))) as string[];
  return (
    <section aria-label="Summary of changes">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Duration" value={`${fmtDuration(diff.durationBefore)} → ${fmtDuration(diff.durationAfter)}`} sub={signedDuration(delta)} tone={Math.abs(delta) < 0.05 ? undefined : delta < 0 ? 'ok' : 'accent'} />
        <Stat label="Clips added" value={String(diff.clipsAdded.length)} icon={<PlusIcon size={13} weight="bold" />} tone={diff.clipsAdded.length ? 'ok' : undefined} />
        <Stat label="Clips removed" value={String(diff.clipsRemoved.length)} icon={<Minus size={13} weight="bold" />} tone={diff.clipsRemoved.length ? 'danger' : undefined} />
        <Stat label="Clips changed" value={String(diff.clipsChanged.length)} icon={<PencilSimpleLine size={13} />} tone={diff.clipsChanged.length ? 'info' : undefined} sub={diff.reordered ? 'and reordered' : undefined} />
      </div>
      {diff.summary.length || flags.length ? (
        <div className="mt-4 rounded-[14px] p-4 hairline">
          {diff.summary.length ? (
            <ul className="space-y-1">
              {diff.summary.map((s, i) => (
                <li key={i} className="flex gap-2 text-[13.5px] leading-relaxed text-fg">
                  <span className="mt-[9px] h-1 w-1 shrink-0 rounded-full bg-accent" aria-hidden />
                  {s}
                </li>
              ))}
            </ul>
          ) : null}
          {flags.length ? (
            <div className={cx('flex flex-wrap gap-1.5', diff.summary.length > 0 && 'mt-3')}>
              {flags.map((f) => (
                <Badge key={f} tone="info">
                  {f}
                </Badge>
              ))}
            </div>
          ) : null}
        </div>
      ) : (
        <div className="mt-4 flex items-center gap-2 rounded-[14px] p-4 text-[13px] text-muted hairline">
          <Equals size={14} /> These versions are the same.
        </div>
      )}
    </section>
  );
}

function Stat({ label, value, sub, icon, tone }: { label: string; value: string; sub?: string; icon?: React.ReactNode; tone?: 'ok' | 'danger' | 'info' | 'accent' }) {
  const color = tone === 'ok' ? 'text-ok' : tone === 'danger' ? 'text-danger' : tone === 'info' ? 'text-info' : tone === 'accent' ? 'text-accent-text' : 'text-fg';
  return (
    <div className="rounded-[14px] bg-raised px-4 py-3 hairline">
      <div className="flex items-center gap-1.5 text-[11.5px] text-subtle">
        {icon}
        {label}
      </div>
      <div className={cx('tc mt-1 truncate text-[17px] font-semibold', color)}>{value}</div>
      {sub ? <div className={cx('tc text-[12px]', color === 'text-fg' ? 'text-subtle' : color)}>{sub}</div> : null}
    </div>
  );
}

/** A clip's, graphic's or sound's changed setting in words (the cut's own field names stay in the cut). */
const FIELD_WORDS: Record<string, string> = {
  in: 'start',
  out: 'end',
  at: 'when it starts',
  duration: 'length',
  speed: 'speed',
  volume: 'volume',
  muted: 'sound on or off',
  framing: 'framing',
  transitionIn: 'transition',
  fadeIn: 'fade in',
  fadeOut: 'fade out',
  label: 'label',
  rationale: 'why it is there',
  evidence: 'reasons',
  locked: 'lock',
  notes: 'notes',
  assetId: 'footage',
  role: 'kind of sound',
  gainDb: 'level',
  duck: 'dipping under voices',
  duckUnderDialogue: 'dipping under voices',
  duckDb: 'how far it dips',
  rights: 'rights',
  fit: 'fit to the video',
  loop: 'loop',
  template: 'kind of graphic',
  motion: 'motion graphic',
  params: 'text and settings',
};

/** What people call each clip, graphic and sound in these cuts: its label, kind of graphic or file title (else its short name). */
function namesIn(specs: (Spec | undefined)[], assets: Map<string, AssetSummary>): Map<string, string> {
  const out = new Map<string, string>();
  for (const s of specs) {
    if (!s) continue;
    for (const c of s.clips) out.set(c.id, c.label || assets.get(c.assetId)?.title || c.id);
    for (const o of s.overlays ?? []) out.set(o.id, `${o.motion ? o.motion.name : templateName(o.template ?? '')} (${o.id})`);
    for (const a of s.audio ?? []) out.set(a.id, assets.get(a.assetId)?.title || a.id);
  }
  return out;
}

function ChangedFields({ diff, names }: { diff: Diff | undefined; names: Map<string, string> }) {
  const name = (id: string) => names.get(id) ?? id;
  if (!diff) return null;
  const groups: { title: string; added: string[]; removed: string[]; changed: { id: string; fields: string[] }[] }[] = [
    { title: 'Clips', added: diff.clipsAdded, removed: diff.clipsRemoved, changed: diff.clipsChanged },
    { title: 'Graphics', added: diff.overlaysAdded, removed: diff.overlaysRemoved, changed: diff.overlaysChanged },
    { title: 'Sound', added: diff.audioAdded, removed: diff.audioRemoved, changed: diff.audioChanged },
  ].filter((g) => g.added.length || g.removed.length || g.changed.length);
  if (groups.length === 0) return null;
  return (
    <section className="mt-6" aria-labelledby="fields-h">
      <h2 id="fields-h" className="mb-3 text-[15px] font-semibold">
        What changed
      </h2>
      <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
        {groups.map((g) => (
          <div key={g.title} className="rounded-[16px] p-4 hairline">
            <div className="mb-2 text-[13px] font-semibold">{g.title}</div>
            <ul className="space-y-1.5 text-[12.5px]">
              {g.added.map((id) => (
                <li key={`a-${id}`} className="flex items-center gap-2">
                  <Badge tone="ok">added</Badge>
                  <span className="text-fg">{name(id)}</span>
                </li>
              ))}
              {g.removed.map((id) => (
                <li key={`r-${id}`} className="flex items-center gap-2">
                  <Badge tone="danger">removed</Badge>
                  <span className="text-fg">{name(id)}</span>
                </li>
              ))}
              {g.changed.map((c) => (
                <li key={`c-${c.id}`} className="flex flex-wrap items-center gap-2">
                  <Badge tone="info">changed</Badge>
                  <span className="text-fg">{name(c.id)}</span>
                  <span className="text-muted">{c.fields.map((f) => FIELD_WORDS[f] ?? f).join(', ')}</span>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
    </section>
  );
}

function SideActions({ rev, data }: { rev: RevisionSummary; data: ProjectShowOut }) {
  const latest = data.renders.find((r) => r.revisionId === rev.id && r.state === 'available' && r.output);
  return (
    <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-line pt-4">
      <ApprovalControls rev={rev} size="sm" />
      <div className="flex-1" />
      <RenderButtons revisionId={rev.id} size="sm" brief={data.project.brief} output={rev.output} />
      {latest ? (
        <RenderDownload
          href={media.render(latest.id, true)}
          render={latest}
          branch={rev.branch}
          className="inline-flex h-7 items-center gap-1.5 rounded-[8px] px-2.5 text-[12.5px] text-muted hairline hover:bg-hover hover:text-fg"
        >
          <DownloadSimple size={13} /> MP4
        </RenderDownload>
      ) : null}
      <Link to={`/p/${data.project.id}/r/${rev.id}`} className="inline-flex h-7 items-center rounded-[8px] px-2.5 text-[12.5px] text-accent-text hover:bg-accent-soft">
        Open version {rev.number}
      </Link>
    </div>
  );
}

function PageSkeleton() {
  return (
    <div aria-busy="true">
      <Skeleton className="h-4 w-60" />
      <Skeleton className="mt-6 h-8 w-80 max-w-full" />
      <div className="mt-6 grid grid-cols-1 gap-6 xl:grid-cols-2">
        <Skeleton className="h-[420px] rounded-[16px]" />
        <Skeleton className="h-[420px] rounded-[16px]" />
      </div>
    </div>
  );
}
