import { useEffect, useMemo } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { useQueries } from '@tanstack/react-query';
import { Archive, ArrowRight, BookmarkSimple, Books, Cpu, Eye, FilmSlate, FolderOpen, FolderSimplePlus, HardDrives, Plus, Robot, SlidersHorizontal, Tray, TrashSimple, Queue, X } from '@phosphor-icons/react';
import { call, media } from '../api/client';
import { useOp, useStatus } from '../api/queries';
import type { AssetSummary, DraftView, ProjectSummary, RevisionSummary } from '../api/types';
import { absolute, capFirst, duration as fmtDuration, folderLabel, plural, relative, stamp } from '../lib/format';
import { useHost } from '../lib/host';
import { resetSelection, useActivityOpen } from '../lib/workbench';
import { useActivity } from '../api/activity';
import { AssetGridSkeleton, ProjectCard, ProjectGridSkeleton, useCreateProject, usePickProject } from '../components/Library';
import { AssetGallery, useAssetSelection, useLibraryView, ViewToggle } from '../components/AssetGallery';
import { EditableTitle } from '../components/AssetMeta';
import { panelParam, SourceInspector, type Panel, type SeekRequest } from '../components/SourceInspector';
import { useConfigureWatch, WatchStrip } from '../components/WatchFolder';
import { SourceCredit } from '../components/LinkImports';
import { AddBox } from '../components/AddBox';
import { SourceMenu, useSourceActions } from '../components/SourceActions';
import { MenuButton } from '../layout/assistant/Menu';
import { libraryLink } from '../api/library';
import { ARCHIVED_PROJECTS } from '../components/project/ProjectDelete';
import { StartFromRecipe } from '../components/recipes/StartFrom';
import { GettingStarted } from '../components/GettingStarted';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, cx, EmptyState, ErrorPanel, Freshness, Progress, SectionTitle, Skeleton, Theirs, Thumb } from '../ui/primitives';

export function Home() {
  const projects = useOp('project.list', {});
  // Deleted projects wait in Archived projects (D196); the projects' heading links there.
  const archived = useOp('project.list', { archivedOnly: true }, { staleTime: 30_000 });
  const archivedCount = archived.data?.projects.length ?? 0;
  const inbox = useOp('asset.list', { unassigned: true, limit: 500 });
  const navigate = useNavigate();
  const createProject = useCreateProject();
  const [params, setParams] = useSearchParams();
  const openAsset = useOpenInboxAsset();

  useEffect(() => resetSelection({}), []);

  // Deep link (search, notifications): /?asset=ast_…&t=12.3&hl=word opens the inbox viewer there.
  const deepAsset = params.get('asset');
  useEffect(() => {
    if (!deepAsset) return;
    const a = inbox.data?.assets.find((x) => x.id === deepAsset);
    if (a) {
      const t = params.get('t');
      const hl = params.get('hl');
      const panel = panelParam(params.get('panel'));
      setParams({}, { replace: true });
      void openAsset(a, t ? Number(t) : undefined, hl ? hl.split('|') : undefined, panel);
    } else if (inbox.data) {
      // Not in the inbox (it was added to a project meanwhile): find its project.
      void call('asset.show', { id: deepAsset })
        .then((s) => {
          const pid = s.asset.projectIds[0];
          const keep = Object.fromEntries(['t', 'hl', 'panel'].flatMap((k) => (params.get(k) ? [[k, params.get(k)!]] : [])));
          if (pid) navigate(`/p/${pid}?${new URLSearchParams({ tab: 'sources', asset: deepAsset, ...keep })}`, { replace: true });
          // Filed away (a collection, the Archive) or in the Trash: the Library shows it.
          else navigate(libraryLink({ asset: deepAsset }), { replace: true });
        })
        .catch(() => setParams({}, { replace: true }));
    }
  }, [deepAsset, inbox.data, openAsset, setParams, params, navigate]);

  const list = projects.data?.projects ?? [];
  const heads = useHeadRevisions(list);
  const inboxNames = useMemo(() => new Map((inbox.data?.assets ?? []).map((a) => [a.id, a.title])), [inbox.data]);

  return (
    <div className="space-y-10">
      <div className="grid grid-cols-1 gap-5 lg:grid-cols-12">
        <GettingStarted className="lg:col-span-12" /> {/* the first run (D256) */}
        <IntakeZone className="lg:col-span-8" />
        <SystemCard className="lg:col-span-4" />
      </div>

      <div className="grid grid-cols-1 gap-8 lg:grid-cols-12">
        <section className="min-w-0 lg:col-span-8" aria-labelledby="projects-h">
          <SectionTitle
            id="projects-h"
            meta={projects.data ? plural(list.length, 'project') : undefined}
            action={
              <>
                <Freshness at={projects.dataUpdatedAt || undefined} fetching={projects.isFetching} onRefresh={() => void projects.refetch()} />
                {archivedCount > 0 ? (
                  <Link to={ARCHIVED_PROJECTS} className="inline-flex h-7 items-center gap-1.5 rounded-[8px] px-2 text-[12.5px] text-accent-text hover:bg-accent-soft" data-archived-link>
                    <Archive size={13} /> Archived projects <span className="tc text-subtle">{archivedCount}</span>
                  </Link>
                ) : null}
                <Button size="sm" variant="primary" icon={<Plus size={13} weight="bold" />} onClick={async () => {
                  const id = await createProject();
                  if (id) navigate(`/p/${id}`);
                }}>
                  New project
                </Button>
              </>
            }
          >
            Projects
          </SectionTitle>
          {projects.isLoading ? (
            <ProjectGridSkeleton />
          ) : projects.error ? (
            <ErrorPanel error={projects.error} onRetry={() => void projects.refetch()} />
          ) : list.length === 0 ? (
            <div className="panel">
              <EmptyState
                icon={<FilmSlate size={22} />}
                title="No projects yet"
                body="A project holds a brief, its footage and every version of the cut. Create one, then drop footage onto it, or ask Claude Code to start one for you."
                action={
                  <Button variant="primary" icon={<FolderSimplePlus size={15} />} onClick={async () => {
                    const id = await createProject();
                    if (id) navigate(`/p/${id}`);
                  }}>
                    Create a project
                  </Button>
                }
              />
            </div>
          ) : (
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {list.map((p) => {
                const h = heads.get(p.id);
                return <ProjectCard key={p.id} project={p} names={inboxNames} review={h ? { number: h.number, state: h.approval.state } : undefined} />;
              })}
            </div>
          )}
        </section>

        <aside className="min-w-0 space-y-8 lg:col-span-4" aria-label="Review and activity">
          <ReviewRequests />
          <StartFromRecipe /> {/* Recipes: start a video from one (D212) */}
          <ProcessingNow />
        </aside>
      </div>

      <InboxSection loading={inbox.isLoading} error={inbox.error} assets={(inbox.data?.assets ?? []).filter((a) => a.kind !== 'image')} updatedAt={inbox.dataUpdatedAt} fetching={inbox.isFetching} onRetry={() => void inbox.refetch()} />
    </div>
  );
}

/** Latest head revision per project (for review requests). One small query per project. */
function useHeadRevisions(projects: ProjectSummary[]): Map<string, RevisionSummary> {
  const withHeads = projects.filter((p) => p.headRevisionId).slice(0, 24);
  const results = useQueries({
    queries: withHeads.map((p) => ({
      queryKey: ['revision.list', { projectId: p.id }],
      queryFn: ({ signal }: { signal: AbortSignal }) => call('revision.list', { projectId: p.id }, signal),
      staleTime: 15_000,
    })),
  });
  return useMemo(() => {
    const m = new Map<string, RevisionSummary>();
    results.forEach((r, i) => {
      const p = withHeads[i];
      const head = r.data?.revisions.find((x) => x.isHead) ?? r.data?.revisions.find((x) => x.id === p?.headRevisionId);
      if (p && head) m.set(p.id, head);
    });
    return m;
  }, [results.map((r) => r.dataUpdatedAt).join(','), withHeads.map((p) => p.id).join(',')]);
}

/** Home's Add box: everything lands in the Inbox (footage) or the workspace library (pictures, logos, fonts). */
function IntakeZone({ className }: { className?: string }) {
  return <AddBox target={{ label: 'the Inbox' }} variant="hero" className={className} />;
}

function SystemCard({ className }: { className?: string }) {
  const status = useStatus();
  const host = useHost();
  const workers = useOp('worker.list', {}, { refetchInterval: 30_000 });
  const online = (workers.data?.workers ?? []).filter((w) => w.online);
  const w = status.data?.watch;
  return (
    <section aria-label="Machines and storage" className={cx('panel flex flex-col p-5', className)}>
      <div className="flex items-center gap-2">
        <h2 className="text-[14px] font-semibold text-fg">This workspace</h2>
        <Freshness className="ml-auto" at={status.dataUpdatedAt || undefined} fetching={status.isFetching} />
      </div>
      {status.isLoading ? (
        <div className="mt-4 space-y-2">
          <Skeleton className="h-10" />
          <Skeleton className="h-10" />
          <Skeleton className="h-10" />
        </div>
      ) : status.error ? (
        <ErrorPanel className="mt-3" compact error={status.error} onRetry={() => void status.refetch()} />
      ) : (
        <ul className="mt-3 divide-y divide-line">
          <SysRow icon={<Cpu size={15} />} label="Machines working" value={workers.isLoading ? '…' : `${online.length} online`} detail={online.map((x) => x.name).join(', ') || 'None right now. Open MetaRush on a machine to process footage.'} tone={online.length ? 'ok' : 'danger'} to="/renders?tab=machines" />
          <SysRow
            icon={<FolderOpen size={15} />}
            label="Watched folder"
            value={w ? (w.missing ? 'not found' : w.enabled ? (w.pending?.length ? `${w.pending.length} copying` : 'watching') : 'off') : 'off'}
            detail={w ? `${folderLabel(w.dir)}${w.lastScan ? `, checked ${relative(w.lastScan)}` : ''}` : 'Not set up. Add footage automatically from a folder.'}
            tone={w?.errors?.length || w?.missing ? 'danger' : w?.enabled ? 'ok' : undefined}
            to="/settings/footage#watched-folder"
          />
          <SysRow icon={<HardDrives size={15} />} label="Footage storage" value={status.data?.mediaShared ? 'shared' : host.remote ? host.machine : 'this machine'} detail={status.data?.mediaShared ? 'Every machine in the workspace can open it' : host.remote ? `Only ${host.machine} can open the footage` : 'Only this machine can open the footage'} to="/settings/storage" />
          <SysRow icon={<Eye size={15} />} label="Transcripts" value={status.data?.transcriber ? (host.remote ? `on ${host.machine}` : 'on this machine') : 'off'} detail={status.data?.transcriber ? (host.remote ? `Made privately on ${host.machine}, nothing is uploaded` : 'Made privately on your computer, nothing is uploaded') : `Transcripts can't be made ${host.onHere} yet`} />
        </ul>
      )}
      {w?.errors?.length ? <div className="mt-2 rounded-[10px] bg-danger-soft px-3 py-2 text-[12px] text-danger">{w.errors[0]}</div> : null}
    </section>
  );
}

function SysRow({ icon, label, value, detail, tone, to }: { icon: React.ReactNode; label: string; value: string; detail?: string; tone?: 'ok' | 'danger'; to?: string }) {
  const body = (
    <div className="flex items-start gap-3 py-2.5">
      <div className="mt-0.5 text-subtle">{icon}</div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="text-[13px] text-muted">{label}</span>
          <span className={cx('ml-auto text-[12.5px] font-medium', tone === 'ok' ? 'text-ok' : tone === 'danger' ? 'text-danger' : 'text-fg')}>{value}</span>
        </div>
        {detail ? (
          <div className="tc mt-0.5 truncate text-[11px] text-subtle" title={detail}>
            {detail.replace(/`/g, '')}
          </div>
        ) : null}
      </div>
    </div>
  );
  return <li>{to ? <Link to={to} className="-mx-2 block rounded-[10px] px-2 hover:bg-hover">{body}</Link> : body}</li>;
}

/**
 * Awaiting your review (D188, D249): each cut's latest version that an AI app
 * (Claude, Codex, Cursor…) made or that someone asked to have reviewed, the
 * drafts AI apps proposed (versions not in the cut yet, other changes), all
 * worked out by the server (review.waiting); hand edits saved in the
 * background don't wait here. A draft opens its review page (/review/…).
 */
function ReviewRequests() {
  const q = useOp('review.waiting', {}, { staleTime: 10_000 });
  const waiting = q.data?.versions ?? [];
  const changes = q.data?.changes ?? [];
  const count = waiting.length + changes.length;
  return (
    <section aria-labelledby="review-h" data-awaiting-review>
      <SectionTitle id="review-h" meta={count ? `${count} waiting` : undefined}>
        Awaiting your review
      </SectionTitle>
      {q.isLoading ? (
        <Skeleton className="h-20 rounded-[14px]" />
      ) : q.error ? (
        <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />
      ) : count === 0 ? (
        <div className="rounded-[14px] px-4 py-5 text-[13px] text-muted hairline">
          Nothing waiting. Versions Claude Code and other AI apps make, the drafts they propose, and ones someone asks to have reviewed wait here for you to approve.
        </div>
      ) : (
        <ul className="space-y-1.5">
          {waiting.map(({ projectId, projectName, thumbAssetId, cutTitle, revision: rev, said, draft }) => (
            <li key={rev.id} data-waiting-version={rev.id} data-waiting-draft={draft?.id}>
              <Link to={draft ? `/review/${draft.id}` : `/p/${projectId}/r/${rev.id}`} className="group flex items-center gap-3 rounded-[14px] p-2 transition-colors hover:bg-hover">
                <Thumb src={thumbAssetId ? media.asset(thumbAssetId, 'thumbnail') : undefined} alt="" className="h-12 w-[72px] shrink-0 rounded-[9px]" fallback={<FilmSlate size={14} />} />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="tc text-[12.5px] font-semibold text-fg">r{rev.number}</span>
                    {draft ? (
                      <Badge tone="info" icon={<Robot size={10} />}>
                        {draft.agentName}'s draft
                      </Badge>
                    ) : null}
                    <span className="truncate text-[13px] text-fg">{draft ? draft.title : <Theirs>{rev.title || cutTitle || projectName}</Theirs>}</span>
                  </div>
                  <div className="truncate text-[11.5px] text-subtle" title={absolute(rev.createdAt)}>
                    <span className="text-muted" data-stamp>
                      {stamp(rev.createdAt)}
                    </span>
                    , <Theirs>{projectName}</Theirs>, {fmtDuration(rev.duration)}, {draft ? draftLine(draft) : said.startsWith('Someone') ? 'someone' + said.slice(7) : said}
                  </div>
                </div>
                <ArrowRight size={14} className="shrink-0 text-subtle transition-transform group-hover:translate-x-0.5" />
              </Link>
            </li>
          ))}
          {changes.map((d) => (
            <li key={d.id} data-waiting-draft={d.id}>
              <Link to={`/review/${d.id}`} className="group flex items-center gap-3 rounded-[14px] p-2 transition-colors hover:bg-hover">
                <div className="flex h-12 w-[72px] shrink-0 items-center justify-center rounded-[9px] bg-raised text-subtle hairline">
                  <SlidersHorizontal size={16} />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <Badge tone="info" icon={<Robot size={10} />}>
                      {d.agentName}'s draft
                    </Badge>
                    <span className="truncate text-[13px] text-fg">{d.title}</span>
                  </div>
                  <div className="truncate text-[11.5px] text-subtle" title={absolute(d.createdAt)}>
                    <span className="text-muted" data-stamp>
                      {stamp(d.createdAt)}
                    </span>
                    {d.projectName ? (
                      <>
                        , <Theirs>{d.projectName}</Theirs>
                      </>
                    ) : null}
                    , {d.changes[0] ?? 'a change waiting for your OK'}
                  </div>
                </div>
                <ArrowRight size={14} className="shrink-0 text-subtle transition-transform group-hover:translate-x-0.5" />
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** A draft version's second line: not in the cut yet, and its review copy. */
function draftLine(d: DraftView): string {
  const copy = d.reviewCopy ? `review copy: ${d.reviewCopy.said.charAt(0).toLowerCase()}${d.reviewCopy.said.slice(1)}` : 'review copy coming';
  return `not in the cut yet, ${copy}`;
}

function ProcessingNow() {
  const act = useActivity();
  const [, setOpen] = useActivityOpen();
  const navigate = useNavigate();
  const items = (act.data?.items ?? []).filter((i) => i.state === 'active' || i.state === 'waiting' || i.state === 'interrupted');
  return (
    <section aria-labelledby="proc-h">
      <SectionTitle
        id="proc-h"
        meta={items.length ? plural(items.length, 'item') : undefined}
        action={
          <button type="button" onClick={() => setOpen(true)} className="inline-flex items-center gap-1 text-[12.5px] text-accent-text hover:underline">
            <Queue size={13} /> All activity
          </button>
        }
      >
        In progress
      </SectionTitle>
      {act.isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-12 rounded-[12px]" />
          <Skeleton className="h-12 rounded-[12px]" />
        </div>
      ) : act.error ? (
        <ErrorPanel compact error={act.error} onRetry={() => void act.refetch()} />
      ) : items.length === 0 ? (
        <div className="rounded-[14px] px-4 py-5 text-[13px] text-muted hairline">All quiet. Uploads, footage being prepared and renders show up here while they run.</div>
      ) : (
        <ul className="space-y-1">
          {items.slice(0, 6).map((it) => (
            <li key={it.key}>
              <button type="button" onClick={() => (it.link ? navigate(it.link) : setOpen(true))} className="block w-full rounded-[12px] px-3 py-2.5 text-left hairline hover:bg-hover">
                <div className="flex items-center gap-2">
                  <span className="min-w-0 truncate text-[13px] font-medium text-fg">{it.title}</span>
                  {it.pct != null && it.state === 'active' ? <span className="tc ml-auto shrink-0 text-[11.5px] text-muted">{Math.round(it.pct * 100)}%</span> : null}
                </div>
                <div className={cx('mt-0.5 truncate text-[11.5px]', it.state === 'waiting' || it.state === 'interrupted' ? 'text-accent-text' : 'text-subtle')}>
                  {it.waiting?.text && (it.state === 'waiting' || it.steps?.some((s) => s.state === 'paused')) ? capFirst(it.waiting.text) : it.stageLabel}
                </div>
                {it.state === 'active' ? <Progress className="mt-2" value={it.pct ?? 0} indeterminate={it.pct == null} label={`${it.title} progress`} /> : null}
              </button>
            </li>
          ))}
          {items.length > 6 ? (
            <li>
              <button type="button" onClick={() => setOpen(true)} className="w-full rounded-[12px] px-3 py-2 text-left text-[12.5px] text-accent-text hover:bg-hover">
                {plural(items.length - 6, 'more')}…
              </button>
            </li>
          ) : null}
        </ul>
      )}
    </section>
  );
}

/** Opens an inbox clip in a large viewer (optionally at a time, with words highlighted) with "Add to project". */
function useOpenInboxAsset() {
  const dialog = useDialog();
  const pick = usePickProject();
  const navigate = useNavigate();
  const actions = useSourceActions();
  return useMemo(
    () => async (asset: AssetSummary, t?: number, highlight?: string[], panel?: Panel) => {
      const seek: SeekRequest | undefined = t !== undefined && Number.isFinite(t) ? { t, nonce: Date.now() } : undefined;
      const r = await dialog.open<'add'>(
        (close) => (
          <div>
            <div className="-mt-1 mb-4">
              <EditableTitle asset={asset} heading />
              <div className="tc mt-0.5 truncate text-[11.5px] text-subtle">{asset.name}</div>
              <SourceCredit source={asset.source} className="mt-1" />
            </div>
            <SourceInspector asset={asset} compact seek={seek} highlight={highlight} panel={panel} />
            <div className="mt-5 flex flex-wrap items-center justify-end gap-2 border-t border-line pt-4">
              <MenuButton
                label={`File “${asset.title}” away`}
                items={actions.menuItems(asset).map((it) => (it === 'separator' ? it : { ...it, onSelect: () => (close(), it.onSelect()) }))}
              />
              <span className="flex-1" />
              <Button variant="ghost" onClick={() => close()}>
                Close
              </Button>
              <Button variant="primary" icon={<FolderSimplePlus size={15} />} onClick={() => close('add')}>
                Add to project
              </Button>
            </div>
          </div>
        ),
        { label: asset.title, size: 'lg' },
      );
      if (r === 'add') {
        const id = await pick(asset);
        if (id) navigate(`/p/${id}?tab=sources&asset=${asset.id}`);
      }
    },
    [dialog, pick, navigate, actions],
  );
}

function InboxSection({ assets, loading, error, onRetry, updatedAt, fetching }: { assets: AssetSummary[]; loading: boolean; error: unknown; onRetry: () => void; updatedAt: number; fetching: boolean }) {
  const pick = usePickProject();
  const open = useOpenInboxAsset();
  const createProject = useCreateProject();
  const configure = useConfigureWatch();
  const actions = useSourceActions();
  const navigate = useNavigate();
  const [view, setView] = useLibraryView('inbox');
  const selection = useAssetSelection(useMemo(() => assets.map((a) => a.id), [assets]));
  const chosen = assets.filter((a) => selection.isSelected(a.id));
  return (
    <section aria-labelledby="inbox-h" id="inbox">
      <SectionTitle
        id="inbox-h"
        meta={assets.length ? `${plural(assets.length, 'clip')} not in a project yet` : undefined}
        action={
          <>
            <Freshness at={updatedAt || undefined} fetching={fetching} onRefresh={onRetry} />
            <Link to={libraryLink()} className="inline-flex h-7 items-center gap-1.5 rounded-[8px] px-2 text-[12.5px] text-accent-text hover:bg-accent-soft">
              <Books size={13} /> Library
            </Link>
            <ViewToggle value={view} onChange={setView} label="Inbox layout" />
            {assets.length > 1 && chosen.length === 0 ? (
              <Button
                size="sm"
                variant="secondary"
                icon={<FolderSimplePlus size={13} />}
                onClick={async () => {
                  const id = await createProject(assets.map((a) => a.id));
                  if (id) navigate(`/p/${id}`);
                }}
              >
                New project from inbox
              </Button>
            ) : null}
          </>
        }
      >
        Inbox
      </SectionTitle>
      <div className="mb-4">
        <WatchStrip onConfigure={() => void configure()} />
      </div>
      {chosen.length > 0 ? (
        <div role="region" aria-label="Selected clips" className="fade-in sticky top-[calc(var(--mr-header-h)+7px)] z-20 mb-3 flex flex-wrap items-center gap-2 rounded-[12px] bg-raised px-3 py-2 shadow-[var(--shadow-soft)] hairline">
          <span className="text-[13px] font-medium text-fg">{plural(chosen.length, 'clip')} selected</span>
          <span className="hidden text-[12px] text-subtle sm:inline">Drag them onto a project, or</span>
          <Button size="sm" variant="primary" icon={<FolderSimplePlus size={13} />} onClick={() => void pick(chosen)}>
            Add to project…
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={async () => {
              const id = await createProject(chosen.map((a) => a.id));
              if (id) navigate(`/p/${id}`);
            }}
          >
            New project with these
          </Button>
          <Button size="sm" variant="ghost" icon={<BookmarkSimple size={13} />} onClick={() => void actions.library.addToCollection(chosen)}>
            Add to collection…
          </Button>
          <Button size="sm" variant="ghost" icon={<Archive size={13} />} onClick={() => void actions.library.archive(chosen)}>
            Archive
          </Button>
          <Button size="sm" variant="ghost" icon={<TrashSimple size={13} />} onClick={() => void actions.library.trash(chosen)}>
            Move to Trash
          </Button>
          <button type="button" onClick={selection.clear} className="ml-auto inline-flex items-center gap-1 rounded-[8px] px-2 py-1 text-[12px] text-muted hover:bg-hover hover:text-fg">
            <X size={12} /> Clear <span className="hidden sm:inline">(Esc)</span>
          </button>
        </div>
      ) : null}
      {loading ? (
        <AssetGridSkeleton count={4} />
      ) : error ? (
        <ErrorPanel error={error} onRetry={onRetry} />
      ) : assets.length === 0 ? (
        <div className="panel">
          <EmptyState
            compact
            icon={<Tray size={20} />}
            title="The inbox is empty"
            body="Clips you drop outside a project, or that arrive through the watched folder, wait here until you add them to a project, put them on a collection, archive them or move them to the Trash."
            action={
              <Link to={libraryLink()} className="inline-flex items-center gap-1.5 text-[13px] text-accent-text hover:underline">
                <Books size={14} /> Everything filed away is in the Library
              </Link>
            }
          />
        </div>
      ) : (
        <>
          <AssetGallery
            assets={assets}
            view={view}
            selection={selection}
            label="Inbox clips"
            onOpen={(a) => void open(a)}
            action={(a) => (
              <span className="flex items-center gap-1">
                <Button size="sm" variant="secondary" className="bg-raised/90 backdrop-blur" icon={<FolderSimplePlus size={13} />} onClick={() => void pick(a)}>
                  Add
                </Button>
                <SourceMenu asset={a} actions={actions} />
              </span>
            )}
          />
          <p className="mt-3 px-1 text-[11.5px] text-subtle">
            Drag clips onto a project to add them, or file them away from their “…” menu. <span className="tc">j</span>/<span className="tc">k</span> move, <span className="tc">x</span> selects, Enter opens.
          </p>
        </>
      )}
    </section>
  );
}

