import { useState } from 'react';
import { useSearchParams } from 'react-router';
import {
  ArrowRight,
  ArrowsClockwise,
  CheckCircle,
  Code,
  FilmSlate,
  GitBranch,
  GitPullRequest,
  ImageSquare,
  Package,
  Play,
  Plus,
  Prohibit,
  Shapes,
  ShieldCheck,
  TextAa,
  WarningCircle,
  XCircle,
} from '@phosphor-icons/react';
import { media } from '../../api/client';
import { useOp, useOpMutation } from '../../api/queries';
import type { AssetSource, ProjectShowOut, RemotionCompositionRow, RemotionShowOut, RemotionSourceRow, RemotionStillRow, RemotionVersionRow } from '../../api/types';
import { bytes, clock, plural, relative } from '../../lib/format';
import { useActivityOpen } from '../../lib/workbench';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, cx, Details, ErrorPanel, SectionTitle, Skeleton, Theirs, Thumb, type Tone } from '../../ui/primitives';
import { RemotionMapPanel, RemotionWordsPanel } from './RemotionMap';
import { RemotionPicturesPanel } from './RemotionPictures';
import { RemotionPushPanel } from './RemotionPush';
import { RemotionRefreshPanel } from './RemotionRefresh';

/*
 * Remotion sources (D131–D138): Remotion projects brought into a project. Each
 * composition (a video the project's code can make) becomes a clip once a
 * person approves the version it comes from. Everything here is pictures and
 * words: the project's code never runs in the dashboard (its pictures were
 * drawn in the render jail with no network).
 */

const stateTone: Record<string, Tone> = { waiting: 'accent', approved: 'ok', rejected: 'danger', revoked: 'danger' };

function StateBadge({ state, label, number }: { state: string; label: string; number?: number }) {
  const icon = state === 'approved' ? <CheckCircle size={12} weight="fill" /> : state === 'waiting' ? <ShieldCheck size={12} /> : <XCircle size={12} weight="fill" />;
  return (
    <Badge tone={stateTone[state] ?? 'neutral'} icon={icon}>
      {number ? `Version ${number}: ${label}` : label}
    </Badge>
  );
}

function compositionFacts(c: RemotionCompositionRow): string {
  if (c.still) return `${c.width}×${c.height}, a still picture`;
  const fps = Number.isInteger(c.fps) ? c.fps : c.fps.toFixed(2);
  return `${c.width}×${c.height} · ${fps} frames a second · ${clock(c.duration)}${c.computed ? ' (worked out when it plays)' : ''}`;
}

/** The project's Remotion sources, under Sources (nothing when it has none). */
export function RemotionSources({ data, onOpenClip }: { data: ProjectShowOut; onOpenClip: (assetId: string) => void }) {
  const projectId = data.project.id;
  const list = useOp('remotion.list', { projectId });
  const [params, setParams] = useSearchParams();
  const opened = params.get('remotion') ?? '';
  const openedVersion = params.get('rv') ?? '';
  const setOpen = (id: string, version?: string) => {
    const next = new URLSearchParams(params);
    if (id) next.set('remotion', id);
    else next.delete('remotion');
    if (version) next.set('rv', version);
    else next.delete('rv');
    setParams(next, { replace: true });
  };
  const sources = list.data?.sources ?? [];
  if (list.isLoading) return opened ? <Skeleton className="h-40 rounded-[16px]" /> : null;
  if (list.error) return <ErrorPanel error={list.error} onRetry={() => void list.refetch()} />;
  if (!sources.length) return null;
  return (
    <section aria-labelledby="remotion-sources" data-remotion-sources>
      <SectionTitle id="remotion-sources" meta={list.data?.waiting ? `${plural(list.data.waiting, 'version')} waiting for you` : undefined}>
        Remotion projects
      </SectionTitle>
      <p className="-mt-1.5 mb-3 max-w-[72ch] text-[12.5px] leading-relaxed text-muted">
        Videos made from code. Each composition becomes a clip once a person has looked at the version it comes from and approved it.
      </p>
      <div className="space-y-4">
        {sources.map((s) => (
          <SourceCard
            key={s.id}
            row={s}
            projectId={projectId}
            open={opened === s.id}
            version={opened === s.id ? openedVersion : ''}
            onOpen={(v) => setOpen(s.id, v)}
            onClose={() => setOpen('')}
            onOpenClip={onOpenClip}
          />
        ))}
      </div>
    </section>
  );
}

function SourceCard({ row, projectId, open, version, onOpen, onClose, onOpenClip }: {
  row: RemotionSourceRow;
  projectId: string;
  open: boolean;
  version: string;
  onOpen: (version?: string) => void;
  onClose: () => void;
  onOpenClip: (assetId: string) => void;
}) {
  const [all, setAll] = useState(false);
  const [panel, setPanel] = useState<'' | 'scenes' | 'words' | 'pictures' | 'refresh' | 'push'>('');
  const comps = row.compositions;
  const shown = all ? comps : comps.slice(0, 6);
  const existing = row.clips.filter((c) => c.kind === 'existing');
  const made = row.clips.filter((c) => c.kind === 'render').length;
  const approved = !!row.approved;
  const newer = row.headNumber > (row.approvedNumber ?? 0) && row.head !== row.version;
  return (
    <article aria-label={`Remotion project ${row.title}`} data-remotion-source={row.name} className="panel p-4 sm:p-5">
      <header className="flex flex-wrap items-start gap-x-3 gap-y-2">
        <div className="grid h-9 w-9 shrink-0 place-items-center rounded-[11px] bg-accent-soft text-accent-text">
          <Code size={18} weight="bold" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="min-w-0 text-[15.5px] font-semibold tracking-[-0.01em] text-fg [overflow-wrap:anywhere]">
              <Theirs>{row.title}</Theirs>
            </h3>
            {row.versionNumber ? <StateBadge state={row.state} label={row.stateLabel} number={row.versionNumber} /> : null}
          </div>
          <p className="mt-0.5 min-w-0 text-[12px] text-subtle [overflow-wrap:anywhere]">
            <GitBranch size={12} aria-hidden className="mr-1 inline-block align-[-2px]" />
            {row.origin.kind === 'github' ? (
              <>
                From GitHub, <Theirs>{row.origin.repo || row.origin.url}</Theirs>
              </>
            ) : (
              <>
                From the folder <Theirs>{row.origin.folder}</Theirs>
                {row.origin.machine ? (
                  <>
                    {' '}
                    on <Theirs>{row.origin.machine}</Theirs>
                  </>
                ) : null}
              </>
            )}
            {made ? ` · ${plural(made, 'clip')} made from it` : null}
          </p>
          {row.description ? (
            <p className="mt-1 max-w-[72ch] text-[13px] leading-relaxed text-muted">
              <Theirs>{row.description}</Theirs>
            </p>
          ) : null}
        </div>
        <div className="w-full pl-12 sm:w-auto sm:pl-0">
          {open ? (
            <Button variant="ghost" size="sm" onClick={onClose}>
              Close the review
            </Button>
          ) : (
            <Button variant={row.waiting ? 'primary' : 'secondary'} size="sm" icon={<ShieldCheck size={13} />} onClick={() => onOpen(row.waiting ? row.head : undefined)}>
              {row.waiting ? `Review version ${row.headNumber}` : 'Versions and review'}
            </Button>
          )}
        </div>
      </header>

      {row.problem ? (
        <p role="status" className="mt-3 flex items-start gap-2 rounded-[12px] bg-danger-soft px-3.5 py-2.5 text-[13px] leading-relaxed text-fg">
          <WarningCircle size={15} weight="fill" className="mt-[2px] shrink-0 text-danger" />
          <span>MetaRush couldn't read its compositions: {row.problem}</span>
        </p>
      ) : null}
      {row.waiting && !open ? (
        <p className="mt-3 flex items-start gap-2 rounded-[12px] bg-accent-soft px-3.5 py-2.5 text-[13px] leading-relaxed text-fg">
          <ShieldCheck size={15} className="mt-[2px] shrink-0 text-accent-text" />
          <span>
            Version {row.headNumber} waits for you{row.headState === 'waiting' && newer && approved ? ` (cuts keep using version ${row.approvedNumber} until you approve it)` : ''}. Look at it,
            then approve it or turn it down.
          </span>
        </p>
      ) : null}
      {!approved && !row.waiting && !row.problem ? (
        <p className="mt-3 text-[12.5px] text-muted">No version is approved, so its compositions can't go into cuts yet.</p>
      ) : null}

      {open ? <Review row={row} projectId={projectId} version={version || row.head} onVersion={(v) => onOpen(v)} /> : null}

      {comps.length ? (
        <div className="mt-4">
          <h4 className="mb-2 text-[13px] font-semibold text-fg">
            Compositions{' '}
            <span className="font-normal text-subtle">
              · version {row.versionNumber}
              {approved ? '' : ', not approved yet'}
            </span>
          </h4>
          <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {shown.map((c) => (
              <li key={c.id}>
                <CompositionTile row={row} comp={c} projectId={projectId} approved={approved} onOpenClip={onOpenClip} />
              </li>
            ))}
          </ul>
          {comps.length > shown.length || all ? (
            <button type="button" onClick={() => setAll((v) => !v)} className="mt-2 text-[12.5px] font-medium text-accent-text hover:underline">
              {all ? 'Show fewer' : `Show all ${comps.length}`}
            </button>
          ) : null}
        </div>
      ) : null}

      {comps.length && row.versionNumber ? (
        <div className="mt-4">
          <div className="flex flex-wrap gap-2" role="group" aria-label="What is in it">
            <Button
              size="sm"
              variant={panel === 'scenes' ? 'primary' : 'secondary'}
              aria-pressed={panel === 'scenes'}
              icon={<Shapes size={13} />}
              onClick={() => setPanel((p) => (p === 'scenes' ? '' : 'scenes'))}
              data-remotion-panel="scenes"
            >
              Scenes and sounds
            </Button>
            <Button
              size="sm"
              variant={panel === 'words' ? 'primary' : 'secondary'}
              aria-pressed={panel === 'words'}
              icon={<TextAa size={13} />}
              onClick={() => setPanel((p) => (p === 'words' ? '' : 'words'))}
              data-remotion-panel="words"
            >
              Words on screen
            </Button>
            <Button
              size="sm"
              variant={panel === 'pictures' ? 'primary' : 'secondary'}
              aria-pressed={panel === 'pictures'}
              icon={<ImageSquare size={13} />}
              onClick={() => setPanel((p) => (p === 'pictures' ? '' : 'pictures'))}
              data-remotion-panel="pictures"
            >
              Pictures
            </Button>
            <Button
              size="sm"
              variant={panel === 'refresh' ? 'primary' : 'secondary'}
              aria-pressed={panel === 'refresh'}
              icon={<ArrowsClockwise size={13} />}
              onClick={() => setPanel((p) => (p === 'refresh' ? '' : 'refresh'))}
              data-remotion-panel="refresh"
            >
              Refresh from its own steps
            </Button>
            {row.origin.commit ? (
              <Button
                size="sm"
                variant={panel === 'push' ? 'primary' : 'secondary'}
                aria-pressed={panel === 'push'}
                icon={<GitPullRequest size={13} />}
                onClick={() => setPanel((p) => (p === 'push' ? '' : 'push'))}
                data-remotion-panel="push"
              >
                Send changes back
              </Button>
            ) : null}
          </div>
          {panel === 'scenes' ? <RemotionMapPanel row={row} projectId={projectId} /> : null}
          {panel === 'words' ? <RemotionWordsPanel row={row} projectId={projectId} onReview={(v) => onOpen(v)} /> : null}
          {panel === 'pictures' ? <RemotionPicturesPanel row={row} projectId={projectId} onReview={(v) => onOpen(v)} /> : null}
          {panel === 'refresh' ? <RemotionRefreshPanel row={row} projectId={projectId} onReview={(v) => onOpen(v)} /> : null}
          {panel === 'push' ? <RemotionPushPanel row={row} projectId={projectId} /> : null}
        </div>
      ) : null}

      {existing.length ? (
        <div className="mt-4">
          <h4 className="mb-1.5 text-[13px] font-semibold text-fg">Videos it already had</h4>
          <p className="mb-2 text-[12px] text-subtle">Renders from its own folder, added as footage when it was brought in.</p>
          <ul className="flex flex-wrap gap-2">
            {existing.map((c) => (
              <li key={c.assetId}>
                <button
                  type="button"
                  onClick={() => onOpenClip(c.assetId)}
                  className="inline-flex h-8 max-w-full items-center gap-1.5 rounded-full px-3 text-[12.5px] text-fg hairline hover:bg-hover"
                >
                  <FilmSlate size={13} className="shrink-0 text-subtle" />
                  <Theirs className="truncate">{c.title}</Theirs>
                  <span className="shrink-0 text-subtle">{clock(c.duration)}</span>
                </button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {row.extras.length ? (
        <p className="mt-4 flex flex-wrap items-center gap-1.5 text-[12.5px] text-muted">
          <Package size={13} className="text-subtle" aria-hidden />
          Also uses
          {row.extras.map((p) => (
            <Badge key={p.name} tone="neutral" title={`version ${p.version}`}>
              <Theirs>{p.name}</Theirs>
            </Badge>
          ))}
        </p>
      ) : null}

      <Details compact className="mt-3">
        <div>Short name for cut sheets and the terminal: {row.name}</div>
        <div>Source: {row.id}</div>
        <div>
          Where: {row.where}
          {row.origin.branch ? `, branch ${row.origin.branch}` : ''}
          {row.origin.commit ? `, commit ${row.origin.commit}` : ''}
          {row.origin.dirty ? ' (with changes not committed)' : ''}
        </div>
        <div>
          Shown: version {row.versionNumber} ({row.version}); newest {row.headNumber}
          {row.approvedNumber ? `; approved ${row.approvedNumber}` : ''}
        </div>
        <div>Remotion {row.remotion}</div>
        {row.extras.length ? <div>Other packages: {row.extras.map((p) => `${p.name}@${p.version}`).join(', ')}</div> : null}
        <div>In a cut sheet: remotion {row.name} {comps[0]?.id ?? 'Composition'}</div>
      </Details>
    </article>
  );
}

function CompositionTile({ row, comp, projectId, approved, onOpenClip }: {
  row: RemotionSourceRow;
  comp: RemotionCompositionRow;
  projectId: string;
  approved: boolean;
  onOpenClip: (assetId: string) => void;
}) {
  const toast = useToast();
  const [, setActivity] = useActivityOpen();
  const use = useOpMutation('remotion.use', { invalidate: ['remotion.list', 'project.show', 'cut.show', 'revision.list', 'activity.summary'] });
  const render = useOpMutation('remotion.render', { invalidate: ['remotion.list', 'activity.summary', 'job.list'] });
  const failed = (e: { message: string; hint?: string }) => toast({ tone: 'error', title: 'That did not work', body: [e.message, e.hint].filter(Boolean).join(' ') });
  const input = { source: row.name, projectId, composition: comp.id };
  const ratio = comp.width && comp.height ? `${comp.width} / ${comp.height}` : '16 / 9';
  // A still picture can't be a clip, and a clip is at most 15 minutes (a part of a longer one: in the terminal).
  const usable = !comp.still && comp.durationInFrames > 1 && comp.duration <= 900;
  return (
    <div className="flex h-full flex-col overflow-hidden rounded-[14px] bg-raised hairline">
      <div className="grid place-items-center bg-inset" style={{ aspectRatio: '16 / 9' }}>
        <div className="max-h-full max-w-full" style={{ aspectRatio: ratio, height: comp.height >= comp.width * 0.5625 ? '100%' : undefined, width: comp.height < comp.width * 0.5625 ? '100%' : undefined }}>
          <Thumb
            src={comp.poster ? media.remotionPoster(row.version, comp.id) : undefined}
            alt={`A picture of ${comp.id} from the middle of it`}
            className="h-full w-full"
            fit="contain"
            fallback={<FilmSlate size={20} className="text-subtle" />}
          />
        </div>
      </div>
      <div className="flex flex-1 flex-col gap-1 p-3">
        <div className="min-w-0 text-[13.5px] font-medium text-fg [overflow-wrap:anywhere]">
          <Theirs>{comp.id}</Theirs>
          {comp.folder ? (
            <span className="font-normal text-subtle">
              {' '}
              in <Theirs>{comp.folder}</Theirs>
            </span>
          ) : null}
        </div>
        <div className="text-[12px] text-subtle">{compositionFacts(comp)}</div>
        {approved && !usable ? (
          <div className="mt-auto pt-2 text-[12px] text-muted">{comp.still ? 'A still picture, so it can’t be a clip.' : 'Longer than 15 minutes: make a clip of a part of it.'}</div>
        ) : null}
        {approved && usable ? (
          <div className="mt-auto flex flex-wrap gap-1.5 pt-2">
            <Button
              size="sm"
              variant="secondary"
              icon={<Plus size={13} />}
              loading={use.isPending}
              onClick={() =>
                use.mutate(input, {
                  onSuccess: (o) =>
                    toast({
                      tone: o.inCut ? 'success' : undefined,
                      title: o.message,
                      action: o.inCut ? undefined : { label: 'Show progress', onClick: () => setActivity(true) },
                    }),
                  onError: failed,
                })
              }
            >
              Use in the cut
            </Button>
            {comp.clip ? (
              <Button size="sm" variant="ghost" icon={<Play size={13} />} onClick={() => onOpenClip(comp.clip!.assetId)}>
                Open the clip
              </Button>
            ) : (
              <Button
                size="sm"
                variant="ghost"
                icon={<FilmSlate size={13} />}
                loading={render.isPending}
                onClick={() =>
                  render.mutate(input, {
                    onSuccess: (o) =>
                      toast({ tone: o.state === 'done' ? 'success' : o.state === 'failed' ? 'error' : undefined, title: o.message, action: o.state === 'done' ? undefined : { label: 'Show progress', onClick: () => setActivity(true) } }),
                    onError: failed,
                  })
                }
              >
                Make the clip
              </Button>
            )}
          </div>
        ) : null}
      </div>
    </div>
  );
}

/* ---------- the review of one version ---------- */

const kindWords: Record<string, string> = { import: 'brought in', edit: 'a change', refresh: 'made by a refresh' };

function Review({ row, projectId, version, onVersion }: { row: RemotionSourceRow; projectId: string; version: string; onVersion: (v: string) => void }) {
  const show = useOp('remotion.show', { ref: `${row.name}@${version}`, projectId });
  if (show.isLoading) return <Skeleton className="mt-4 h-56 rounded-[14px]" />;
  if (show.error || !show.data) return <ErrorPanel className="mt-4" error={show.error} onRetry={() => void show.refetch()} />;
  return <ReviewBody data={show.data} projectId={projectId} onVersion={onVersion} />;
}

function ReviewBody({ data, projectId, onVersion }: { data: RemotionShowOut; projectId: string; onVersion: (v: string) => void }) {
  const v = data.version;
  const s = data.source;
  const check = v.check;
  const row = data.versions.find((x) => x.hash === v.hash);
  const previous = v.parent ? data.versions.find((x) => x.hash === v.parent) : undefined;
  const words = check?.words ?? [];
  const problems = check?.problems ?? [];
  const blocked = [...(check?.blocked ?? []), ...(v.read?.blocked ?? [])];
  return (
    <div data-remotion-review className="fade-in mt-4 rounded-[14px] bg-panel p-4 hairline">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h4 className="text-[15px] font-semibold text-fg">Version {v.number}</h4>
            {row ? <StateBadge state={row.state} label={row.stateLabel} /> : null}
          </div>
          <p className="mt-0.5 text-[12.5px] text-subtle">
            {upperFirst(kindWords[v.kind] ?? v.kind)} {relative(v.at)}
            {v.by?.userName ? ` by ${v.by.userName}` : ''}
            {previous ? `, made from version ${previous.number}` : ''}
          </p>
          {v.message && (v.kind === 'edit' || v.kind === 'refresh') ? (
            <p className="mt-1 text-[13px] text-fg">
              <Theirs>“{v.message}”</Theirs>
            </p>
          ) : null}
        </div>
        <Decide data={data} projectId={projectId} />
      </div>

      {check?.summary ? <p className="mt-3 max-w-[80ch] text-[13.5px] leading-relaxed text-fg">{check.summary}</p> : null}
      {!check && v.kind === 'import' ? (
        <p className="mt-3 max-w-[80ch] text-[13.5px] leading-relaxed text-fg">
          The project as it was brought in: {plural(v.compositions.length, 'composition')}, {plural(v.files, 'file')} of code and {plural(v.publicFiles, 'picture or sound file', 'pictures and sound files')}.
          Look at the pictures of its compositions below before you approve it.
        </p>
      ) : null}

      {words.length ? (
        <div className="mt-4">
          <h5 className="mb-1.5 text-[13px] font-semibold text-fg">Words that changed</h5>
          <ul className="space-y-1.5">
            {words.map((w, i) => (
              <li key={i} className="rounded-[10px] bg-raised px-3 py-2 text-[13px] leading-relaxed hairline">
                <span className="block text-[11.5px] text-subtle">
                  <Theirs>{w.file}</Theirs>, line {w.line}
                </span>
                {w.before ? (
                  <span className="text-muted line-through decoration-[rgb(239_141_128/0.7)]">
                    <Theirs>“{w.before}”</Theirs>
                  </span>
                ) : null}
                {w.before && w.after ? <ArrowRight size={12} className="mx-1.5 inline text-subtle" aria-label="became" /> : null}
                {w.after ? (
                  <span className="text-fg">
                    <Theirs>“{w.after}”</Theirs>
                  </span>
                ) : (
                  <span className="text-subtle"> (removed)</span>
                )}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {v.packagesChanged?.length ? (
        <div className="mt-4">
          <h5 className="mb-1.5 text-[13px] font-semibold text-fg">Packages it adds or changes</h5>
          <p className="mb-2 text-[12.5px] text-muted">
            Code from other people that MetaRush would install for this version. {v.packagesAllowed ? 'A person allowed them.' : 'Nothing is installed until a person allows them.'}
          </p>
          <ul className="flex flex-wrap gap-1.5">
            {v.packagesChanged.map((p) => (
              <li key={p.name}>
                <Badge tone={v.packagesAllowed ? 'neutral' : 'accent'}>
                  <Theirs>{p.name}</Theirs> {p.from && p.to ? `${p.from} → ${p.to}` : p.to ? `${p.to}, new` : 'removed'}
                </Badge>
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {problems.length ? (
        <div className="mt-4">
          <h5 className="mb-1.5 text-[13px] font-semibold text-fg">What the check found</h5>
          <ul className="space-y-1.5">
            {problems.map((p, i) => (
              <li key={i} className="flex gap-2 text-[13px] leading-relaxed text-fg">
                {p.severity === 'error' ? (
                  <XCircle size={15} weight="fill" className="mt-[2px] shrink-0 text-danger" />
                ) : (
                  <WarningCircle size={15} weight="fill" className="mt-[2px] shrink-0 text-accent-text" />
                )}
                <span>
                  {p.file ? (
                    <span className="text-subtle">
                      <Theirs>{p.file}</Theirs>
                      {p.line ? ` line ${p.line}` : ''}:{' '}
                    </span>
                  ) : null}
                  {p.message}
                  {p.hint ? <span className="block text-[12.5px] text-muted">{p.hint}</span> : null}
                </span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {blocked.length ? (
        <p className="mt-4 flex items-start gap-2 text-[13px] leading-relaxed text-fg">
          <Prohibit size={15} className="mt-[2px] shrink-0 text-danger" />
          <span>Its code tried to reach the internet while MetaRush drew it, and MetaRush stopped it. A version that needs the internet can't be used.</span>
        </p>
      ) : null}

      {data.stills.length ? <Stills stills={data.stills} previous={previous?.number} /> : null}

      {check?.moments?.length ? (
        <p className="mt-3 text-[12.5px] text-muted">
          Where it looks different:{' '}
          {check.moments.map((m, i) => (
            <span key={i}>
              {i ? ', ' : ''}
              <Theirs>{m.composition}</Theirs> {clock(m.from)} to {clock(m.to)}
            </span>
          ))}
          .
        </p>
      ) : null}

      {v.kind === 'import' && data.leftOut.length ? (
        <div className="mt-4">
          <h5 className="mb-1.5 text-[13px] font-semibold text-fg">What MetaRush left out</h5>
          <ul className="space-y-1 text-[12.5px] leading-relaxed text-muted">
            {data.leftOut.map((l) => (
              <li key={l.why}>
                {plural(l.count, 'file')}: {l.why}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {v.config?.length ? (
        <div className="mt-4">
          <h5 className="mb-1.5 text-[13px] font-semibold text-fg">Its own render settings</h5>
          <p className="mb-1.5 text-[12.5px] text-muted">MetaRush renders with its own settings; where the project asks for something, this is what MetaRush does about it.</p>
          <ul className="space-y-1 text-[12.5px] leading-relaxed text-fg">
            {v.config.map((c, i) => (
              <li key={i} className="flex gap-1.5">
                {c.applied ? <CheckCircle size={13} className="mt-[3px] shrink-0 text-ok" /> : <span className="mt-[7px] h-1.5 w-1.5 shrink-0 rounded-full bg-subtle" />}
                <span>{c.note}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {data.versions.length > 1 ? (
        <div className="mt-4">
          <h5 className="mb-1.5 text-[13px] font-semibold text-fg">Versions</h5>
          <ul className="divide-y divide-line overflow-hidden rounded-[12px] bg-raised hairline">
            {data.versions.map((x) => (
              <li key={x.hash}>
                <VersionLine row={x} current={x.hash === v.hash} onOpen={() => onVersion(x.hash)} />
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      <Details compact className="mt-3">
        <div>Version {v.number}: {v.hash}</div>
        {v.parent ? <div>Made from: {v.parent}</div> : null}
        <div>
          Files: {v.files} ({bytes(v.bytes)}), public files {v.publicFiles} ({bytes(v.publicBytes)})
        </div>
        <div>Remotion {v.remotion}; package lock {v.lock}</div>
        {v.fonts?.length ? <div>Fonts kept with it: {v.fonts.map((f) => f.family).join(', ')}</div> : null}
        {v.read?.detail ? <div>What the renderer said: {v.read.detail}</div> : null}
        {v.read ? (
          <div>
            Read {relative(v.read.at)}
            {v.read.machine ? ` on ${v.read.machine}` : ''}
            {v.read.seconds ? ` in ${Math.round(v.read.seconds)} s` : ''}
          </div>
        ) : null}
        {check ? (
          <div>
            Checked {relative(check.at)}
            {check.machine ? ` on ${check.machine}` : ''}
            {check.seconds ? ` in ${Math.round(check.seconds)} s` : ''}
            {check.stage ? `, stopped at ${check.stage}` : ''}
          </div>
        ) : null}
        {blocked.length ? <div>Blocked: {blocked.join(', ')}</div> : null}
        {check?.changes?.length ? <div>Files changed: {check.changes.map((c) => `${c.path} (${c.change})`).join(', ')}</div> : null}
        <div>In the terminal: metarush remotion show {s.name}@{v.number}</div>
      </Details>
    </div>
  );
}

function VersionLine({ row, current, onOpen }: { row: RemotionVersionRow; current: boolean; onOpen: () => void }) {
  return (
    <button type="button" onClick={onOpen} aria-current={current || undefined} className={cx('flex w-full flex-wrap items-center gap-2 px-3 py-2 text-left text-[12.5px] hover:bg-hover', current && 'bg-hover')}>
      <span className="font-medium text-fg">Version {row.number}</span>
      <StateBadge state={row.state} label={row.stateLabel} />
      <span className="text-subtle">
        {kindWords[row.kind] ?? row.kind} {relative(row.at)}
      </span>
      {row.summary ? <span className="min-w-0 basis-full truncate text-muted sm:basis-auto sm:flex-1">{row.summary}</span> : null}
      {current ? <span className="text-subtle">· this one</span> : null}
    </button>
  );
}

function Stills({ stills, previous }: { stills: RemotionStillRow[]; previous?: number }) {
  return (
    <div className="mt-4">
      <h5 className="mb-1.5 text-[13px] font-semibold text-fg">Before and after</h5>
      <p className="mb-2 text-[12.5px] text-muted">The same moments drawn from {previous ? `version ${previous}` : 'the version before'} and from this one, with no network.</p>
      <ul className="space-y-3">
        {stills.map((s, i) => (
          <li key={i} data-remotion-still>
            <div className="mb-1 text-[12px] text-subtle">
              <Theirs>{s.composition}</Theirs> at {clock(s.at)}
              {s.difference > 0 ? '' : ', looks the same'}
            </div>
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
              {s.beforeUrl ? (
                <figure className="min-w-0">
                  <img src={s.beforeUrl} alt={`${s.composition} at ${clock(s.at)}, before`} className="block h-auto w-full rounded-[10px] bg-inset hairline" loading="lazy" />
                  <figcaption className="mt-0.5 text-[11.5px] text-subtle">Before</figcaption>
                </figure>
              ) : null}
              <figure className="min-w-0">
                <img src={s.afterUrl} alt={`${s.composition} at ${clock(s.at)}, after`} className="block h-auto w-full rounded-[10px] bg-inset hairline" loading="lazy" />
                <figcaption className="mt-0.5 text-[11.5px] text-subtle">After</figcaption>
              </figure>
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}

/* ---------- the decision (people only) ---------- */

function Decide({ data, projectId }: { data: RemotionShowOut; projectId: string }) {
  const dialog = useDialog();
  const toast = useToast();
  const v = data.version;
  const title = data.source.title;
  const ref = `${data.source.name}@${v.hash}`;
  const invalidate = ['remotion.list', 'remotion.show', 'project.show', 'cut.show', 'revision.list', 'activity.summary'] as const;
  const approve = useOpMutation('remotion.approve', { invalidate: [...invalidate] });
  const revoke = useOpMutation('remotion.revoke', { invalidate: [...invalidate] });
  const allow = useOpMutation('remotion.allow', { invalidate: [...invalidate] });
  const busy = approve.isPending || revoke.isPending || allow.isPending;
  const done = (o: { summary: string; updates?: { message: string }[] }) =>
    toast({ tone: 'success', title: o.summary, body: o.updates?.length ? o.updates.map((u) => u.message).join(' ') : undefined, durationMs: 9000 });
  const failed = (e: { message: string; hint?: string }) => toast({ tone: 'error', title: 'That did not work', body: [e.message, e.hint].filter(Boolean).join(' ') });
  const checkFailed = (v.check && !v.check.ok) || (v.read && !v.read.ok);

  const onAllow = async () => {
    const ok = await dialog.confirm({
      title: `Allow the packages of version ${v.number}?`,
      body: (
        <div className="space-y-2">
          <p>MetaRush then installs them for this project (their install programs never run) and checks the version again, with no network while it draws.</p>
          <ul className="list-disc pl-5">
            {(v.packagesChanged ?? []).map((p) => (
              <li key={p.name}>
                <Theirs>{p.name}</Theirs> {p.to ? p.to : '(removed)'}
              </li>
            ))}
          </ul>
        </div>
      ),
      confirmLabel: 'Allow them',
    });
    if (ok) allow.mutate({ ref, projectId }, { onSuccess: done, onError: failed });
  };
  const onApprove = async () => {
    const ok = await dialog.confirm({
      title: `Approve version ${v.number} of “${title}”?`,
      body: (
        <p>
          Cuts can then use clips made from it. Clips in the latest cuts that come from an older version are made again from this one and move to it in a new version of each cut;
          earlier versions of the cuts keep what they had.
        </p>
      ),
      confirmLabel: 'Approve',
    });
    if (ok) approve.mutate({ ref, projectId }, { onSuccess: done, onError: failed });
  };
  const onReject = async () => {
    const note = await dialog.prompt({
      title: `Turn down version ${v.number}?`,
      body: <p>Its compositions can't be used in a cut. Claude can make a new version from your note.</p>,
      label: 'What should change (optional)',
      multiline: true,
      maxLength: 2000,
      confirmLabel: 'Turn down',
      tone: 'danger',
    });
    if (note !== null) approve.mutate({ ref, projectId, reject: true, note: note.trim() || undefined }, { onSuccess: done, onError: failed });
  };
  const onRevoke = async () => {
    const note = await dialog.prompt({
      title: `Withdraw approval of version ${v.number}?`,
      body: <p>New clips from it, and renders of cuts that use its clips, are refused until someone approves it again. Videos already made are kept.</p>,
      label: 'Why (optional)',
      multiline: true,
      maxLength: 2000,
      confirmLabel: 'Withdraw approval',
      tone: 'danger',
    });
    if (note !== null) revoke.mutate({ ref, projectId, note: note.trim() || undefined }, { onSuccess: done, onError: failed });
  };

  if (!v.packagesAllowed)
    return (
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="primary" icon={<Package size={14} />} loading={allow.isPending} disabled={busy} onClick={() => void onAllow()}>
          Allow the packages
        </Button>
      </div>
    );
  return (
    <div className="flex flex-wrap items-center gap-2">
      {v.state === 'approved' ? (
        <Button variant="secondary" icon={<Prohibit size={14} />} loading={revoke.isPending} disabled={busy} onClick={() => void onRevoke()}>
          Withdraw approval
        </Button>
      ) : (
        <>
          {v.state === 'waiting' ? (
            <Button variant="ghost" icon={<XCircle size={14} />} loading={approve.isPending && !!approve.variables?.reject} disabled={busy} onClick={() => void onReject()}>
              Turn down
            </Button>
          ) : null}
          <Button
            variant="primary"
            icon={<CheckCircle size={14} />}
            loading={approve.isPending && !approve.variables?.reject}
            disabled={busy || !!checkFailed}
            title={checkFailed ? "It didn't pass its check, so it can't be approved" : undefined}
            onClick={() => void onApprove()}
          >
            Approve
          </Button>
        </>
      )}
    </div>
  );
}

function upperFirst(s: string): string {
  return s ? s[0]!.toUpperCase() + s.slice(1) : s;
}

/* ---------- a clip's provenance ---------- */

/** "Made from the Remotion project “Teaser”: Intro, version 3" in a clip's header (a link to the project's review). */
export function RemotionCredit({ source, className }: { source?: AssetSource | null; className?: string }) {
  const [, setParams] = useSearchParams();
  const rc = source?.remotion;
  if (!rc) return null;
  const open = () => setParams(new URLSearchParams({ tab: 'sources', remotion: rc.sourceId, rv: rc.version }));
  return (
    <div className={cx('flex min-w-0 flex-wrap items-center gap-x-1.5 text-[12px] text-muted', className)}>
      <Code size={12} className="shrink-0 text-subtle" aria-hidden />
      <span className="min-w-0">
        Made from the Remotion project <Theirs>“{rc.sourceTitle || rc.sourceName}”</Theirs>
        {rc.kind === 'existing' ? (
          ': a video it already had'
        ) : rc.kind === 'sound' ? (
          <>
            : a sound <Theirs>{rc.composition}</Theirs> plays, version {rc.versionNumber}
          </>
        ) : (
          <>
            : <Theirs>{rc.composition}</Theirs>, version {rc.versionNumber}
          </>
        )}
      </span>
      <span aria-hidden>·</span>
      <button type="button" onClick={open} className="text-accent-text hover:underline">
        see the project
      </button>
    </div>
  );
}
