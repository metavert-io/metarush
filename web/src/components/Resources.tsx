import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowClockwise, ArrowUpRight, ChartBar, Code, DownloadSimple, FilePdf, FolderSimple, Globe, Image as ImageIcon, Palette, Pause, Play, Plus, Tag, TextAa, Trash, PenNib, Warning, X } from '@phosphor-icons/react';
import { call, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import { chooseFolder, RESOURCE_QUERIES, resourceKindLabel, resourceMedia, setFolder } from '../api/resources';
import type { LinkedFolderRow, ResourceRow } from '../api/types';
import { absolute, bytes, plural, stamp } from '../lib/format';
import { useHost } from '../lib/host';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, cx, ErrorPanel, Skeleton, Spinner } from '../ui/primitives';
import { useActivityOpen } from '../lib/workbench';
import { useToast } from '../ui/Toast';
import { RepoOverview, RepoPicturesPanel, RepoTile, SiteFacts, SnapshotPictures, sourceWords } from './ResourceSources';
import { UsesList } from './SourceActions';
import { PictureFixes } from './sources/PictureFixes';

/*
 * The resources library in the dashboard (D79–D82): logos, pictures, SVGs,
 * PDFs and fonts a project's graphics can use by name ("logo"), from the
 * project itself, the projects it uses, or the workspace library. Everything
 * here calls the same ops as `metarush resources …`.
 */

export function useRefreshResources() {
  const qc = useQueryClient();
  return () => {
    for (const k of RESOURCE_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  };
}

function failToast(toast: ReturnType<typeof useToast>, title: string, e: unknown) {
  const err = toApiError(e);
  toast({ tone: 'error', title, body: [err.message, err.hint].filter(Boolean).join(' ') });
}

/* ---------- a resource's picture ---------- */

const fontFaces = new Map<string, Promise<string | null>>();
/** Registers a font resource in this page (for its specimen) and returns its family name. */
function useResourceFont(r: Pick<ResourceRow, 'id' | 'kind' | 'current'>): string | null {
  const [family, setFamily] = useState<string | null>(null);
  useEffect(() => {
    if (r.kind !== 'font' || typeof FontFace === 'undefined') return;
    const key = `${r.id}:${r.current}`;
    let p = fontFaces.get(key);
    if (!p) {
      const fam = `MR Resource ${r.current.slice(0, 12)}`;
      p = new FontFace(fam, `url("${resourceMedia.file(r.id, { sha: r.current })}")`)
        .load()
        .then((f) => {
          document.fonts.add(f);
          return fam;
        })
        .catch(() => null);
      fontFaces.set(key, p);
    }
    let live = true;
    void p.then((f) => live && setFamily(f));
    return () => {
      live = false;
    };
  }, [r.id, r.kind, r.current]);
  return family;
}

const KIND_ICON: Record<string, ReactNode> = {
  image: <ImageIcon size={20} />,
  svg: <PenNib size={20} />,
  pdf: <FilePdf size={20} />,
  font: <TextAa size={20} />,
  repo: <Code size={20} />,
  website: <Globe size={20} />,
};

/** A resource's thumbnail (a font shows a specimen in itself). */
export function ResourcePicture({ r, className, big }: { r: ResourceRow; className?: string; big?: boolean }) {
  const family = useResourceFont(r);
  const [failed, setFailed] = useState(false);
  if (r.kind === 'repo') return <RepoTile r={r} className={className} />;
  if (r.kind === 'font')
    return (
      <div className={cx('grid place-items-center overflow-hidden bg-panel text-fg', className)} aria-hidden>
        <span style={{ fontFamily: family ? `"${family}", system-ui` : undefined }} className={cx('leading-none', big ? 'text-[64px]' : 'text-[34px]')}>
          Aa
        </span>
      </div>
    );
  // A web page snapshot is opaque: its whole width at the top, on a plain ground.
  const page = r.kind === 'website';
  return (
    <div
      className={cx(
        'relative grid place-items-center overflow-hidden',
        page ? 'bg-panel' : 'bg-[repeating-conic-gradient(var(--bg-inset)_0_25%,var(--bg-panel)_0_50%)] bg-[length:16px_16px]',
        className,
      )}
    >
      {failed ? (
        <span className="text-subtle" aria-hidden>
          {KIND_ICON[r.kind]}
        </span>
      ) : (
        <img
          src={big ? resourceMedia.file(r.id, { sha: r.current, page: r.kind === 'pdf' && r.pagePictures ? 1 : undefined }) : r.thumbUrl}
          alt=""
          loading="lazy"
          onError={() => setFailed(true)}
          // It fills the frame (never sizes it): a square logo in a wider frame is fitted, not cropped.
          className={cx('absolute inset-0 h-full w-full object-contain', page && 'object-top')}
        />
      )}
    </div>
  );
}

/* ---------- details ---------- */

function TagEditor({ r, projectId }: { r: ResourceRow; projectId?: string }) {
  const toast = useToast();
  const refresh = useRefreshResources();
  const [draft, setDraft] = useState('');
  const own = r.tags ?? [];
  const suggested = (r.autoTags ?? []).filter((t) => !own.includes(t.tag));
  const change = async (input: { addTags?: string[]; removeTags?: string[] }) => {
    try {
      await call('resources.update', { ref: r.id, projectId, ...input });
      refresh();
    } catch (e) {
      failToast(toast, "Tags weren't changed", e);
    }
  };
  return (
    <div>
      <div className="flex flex-wrap items-center gap-1.5">
        {own.map((t) => (
          <span key={t} className="inline-flex items-center gap-1 rounded-full bg-accent-soft py-0.5 pl-2.5 pr-1 text-[12px] text-accent-text">
            {t}
            <button type="button" aria-label={`Remove the tag ${t}`} onClick={() => void change({ removeTags: [t] })} className="rounded-full p-0.5 hover:bg-hover">
              <X size={10} />
            </button>
          </span>
        ))}
        {suggested.map((t) => (
          <span key={t.tag} className="inline-flex items-center gap-1 rounded-full py-0.5 pl-2.5 pr-1 text-[12px] text-muted hairline" title={t.why ? `Suggested: ${t.why}` : 'Suggested by MetaRush'}>
            {t.tag}
            <button type="button" aria-label={`Keep the tag ${t.tag}`} onClick={() => void change({ addTags: [t.tag] })} className="rounded-full p-0.5 hover:bg-hover">
              <Plus size={10} />
            </button>
            <button type="button" aria-label={`Not ${t.tag}`} onClick={() => void change({ removeTags: [t.tag] })} className="rounded-full p-0.5 hover:bg-hover">
              <X size={10} />
            </button>
          </span>
        ))}
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const tags = draft
              .split(',')
              .map((x) => x.trim())
              .filter(Boolean);
            if (!tags.length) return;
            setDraft('');
            void change({ addTags: tags });
          }}
        >
          <input value={draft} onChange={(e) => setDraft(e.target.value)} placeholder="Add a tag" aria-label="Add a tag" className="h-7 w-28 rounded-full bg-panel px-2.5 text-[12px] text-fg hairline placeholder:text-subtle" />
        </form>
      </div>
      {suggested.length ? <p className="mt-1.5 text-[11.5px] text-subtle">Outlined tags are MetaRush's suggestions: + keeps one, × means it's wrong.</p> : null}
    </div>
  );
}

function provenance(r: ResourceRow): string {
  const s = r.source;
  const exact = sourceWords(r);
  if (exact) return exact;
  switch (s.kind) {
    case 'folder':
      return `From the linked folder ${r.folder?.name ?? ''}${s.relPath ? ` (${s.relPath})` : ''}`;
    case 'url':
      return `From ${s.site || s.url || 'the web'}`;
    case 'repo':
      return `From the repository ${s.url ?? ''}`;
    case 'project':
      return 'From another project';
    case 'generated':
      return `Made by ${s.tool ?? 'MetaRush'}`;
    default:
      return s.name ? `Added from ${s.name}` : 'Added from a file';
  }
}

function DetailsBody({ id, projectId, close }: { id: string; projectId?: string; close: () => void }) {
  const toast = useToast();
  const dialog = useDialog();
  const refresh = useRefreshResources();
  const [, setActivity] = useActivityOpen();
  const q = useOp('resources.show', { ref: id, projectId });
  const [busy, setBusy] = useState<string | null>(null);
  if (q.isLoading) return <Skeleton className="h-72" />;
  if (q.error || !q.data) return <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />;
  const r = q.data.row;
  const own = r.origin === 'project' || (r.origin === 'workspace' && !projectId);
  const run = async (key: string, fn: () => Promise<{ message: string }>, title: string) => {
    setBusy(key);
    try {
      const out = await fn();
      refresh();
      toast({ tone: 'success', title: out.message });
      return true;
    } catch (e) {
      failToast(toast, title, e);
      return false;
    } finally {
      setBusy(null);
    }
  };
  const rename = async () => {
    const title = await dialog.prompt({ title: 'Rename', label: 'Title', defaultValue: r.title, maxLength: 120, required: true });
    if (title && title !== r.title) void run('rename', () => call('resources.update', { ref: r.id, projectId, title }), "Couldn't rename it");
  };
  const remove = async () => {
    const ok = await dialog.confirm({
      title: `Remove “${r.title}”?`,
      body: 'It leaves the library. Cuts that already use it keep it (they are pinned to its exact version), and you can put it back.',
      confirmLabel: 'Remove',
      tone: 'danger',
    });
    if (ok && (await run('remove', () => call('resources.remove', { ref: r.id, projectId }), "Couldn't remove it"))) close();
  };
  // A repository moves to its newest commit, a web page gets a new snapshot:
  // a fetch job on this computer (its progress shows in the activity bar).
  const refetch = async () => {
    setBusy('refresh');
    try {
      const out = await call('resources.refresh', { ref: r.id, projectId });
      refresh();
      toast({ title: out.message, action: out.jobId ? { label: 'Show progress', onClick: () => setActivity(true) } : undefined, durationMs: 9000 });
    } catch (e) {
      failToast(toast, r.kind === 'repo' ? "Couldn't get the newest commit" : "Couldn't take a new snapshot", e);
    } finally {
      setBusy(null);
    }
  };
  const source = r.kind === 'repo' || r.kind === 'website';
  return (
    <div className="grid grid-cols-1 gap-5 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
      <div>
        {r.kind === 'website' ? (
          <SnapshotPictures r={r} />
        ) : (
          <ResourcePicture r={r} big className="aspect-[4/3] w-full rounded-[14px] hairline" />
        )}
        <div className="mt-2 flex flex-wrap gap-1.5">
          <Badge>{resourceKindLabel(r.kind)}</Badge>
          {r.width && r.height ? <Badge>{`${r.width}×${r.height}`}</Badge> : null}
          {r.pages && r.kind === 'pdf' ? <Badge>{plural(r.pages, 'page')}</Badge> : null}
          {r.family ? <Badge>{r.family}</Badge> : null}
          <Badge>{bytes(r.size)}</Badge>
        </div>
        {r.colors?.length ? (
          <div className="mt-3 flex items-center gap-1.5" aria-label="Main colours">
            {r.colors.slice(0, 6).map((c) => (
              <span key={c} title={c} className="h-5 w-5 rounded-full ring-1 ring-line-strong" style={{ background: c }} />
            ))}
          </div>
        ) : null}
      </div>
      <div className="min-w-0 space-y-4">
        <div>
          <div className="flex items-start gap-2">
            <h3 className="min-w-0 flex-1 break-words text-[17px] font-semibold tracking-[-0.01em] text-fg">{r.title}</h3>
            {own ? (
              <Button size="sm" variant="ghost" onClick={() => void rename()}>
                Rename
              </Button>
            ) : null}
          </div>
          <p className="mt-1 text-[12.5px] text-muted">
            Graphics use it as <span className="tc rounded-[5px] bg-panel px-1.5 py-0.5 text-fg">{r.name}</span>
            {r.from ? ` · from ${r.from}` : r.scope === 'workspace' ? ' · from the workspace library' : ''}
          </p>
          <p className="mt-1 text-[12.5px] text-subtle">
            {provenance(r)}
            {r.versions > 1 ? ` · ${plural(r.versions, 'version')}` : ''}
          </p>
          <p className="mt-0.5 text-[12.5px] text-subtle" data-resource-dates>
            <span title={absolute(r.createdAt)}>
              Added <span data-stamp>{stamp(r.createdAt)}</span>
            </span>
            {r.updatedAt && Math.abs(new Date(r.updatedAt).getTime() - new Date(r.createdAt).getTime()) > 60_000 ? (
              <span title={absolute(r.updatedAt)}>
                {' '}
                · last changed <span className="tc">{stamp(r.updatedAt)}</span>
              </span>
            ) : null}
          </p>
          {r.missing ? (
            <p className="mt-2 flex items-center gap-1.5 text-[12.5px] text-danger">
              <Warning size={13} />{' '}
              {r.source.kind === 'repo'
                ? "No longer in the repository's newest commit: it stays here, and cuts that use it keep working."
                : 'Missing from its folder: it stays here, and cuts that use it keep working.'}
            </p>
          ) : null}
          {!r.stored ? <p className="mt-2 text-[12.5px] text-muted">Only on the computer with its folder; it uploads when a cut first uses it.</p> : null}
        </div>
        {r.kind === 'repo' ? <RepoOverview data={q.data} /> : null}
        {r.kind === 'repo' ? <RepoPicturesPanel data={q.data} projectId={projectId} own={own} /> : null}
        {r.kind === 'website' ? <SiteFacts r={r} projectId={projectId} /> : null}
        <div>
          <div className="mb-1.5 flex items-center gap-1.5 text-[12.5px] font-medium text-muted">
            <Tag size={13} /> Tags
          </div>
          {own ? <TagEditor r={r} projectId={projectId} /> : <div className="text-[12.5px] text-subtle">{(r.tags ?? []).join(', ') || 'No tags'}</div>}
        </div>
        <div>
          <div className="mb-1.5 text-[12.5px] font-medium text-muted">Used in</div>
          <UsesList
            uses={q.data.usedIn}
            empty={
              r.kind === 'repo'
                ? 'Cuts show what is made from it (code slides, diagrams, cards), not the repository itself.'
                : r.kind === 'website'
                  ? 'No cut shows it yet. Ask Claude to “show the homepage at the end”.'
                  : 'No cut uses it yet. Ask Claude to “put our logo on the title card”, or pick it in a graphic.'
            }
            onNavigate={close}
          />
        </div>
        {own && r.kind === 'image' ? <PictureFixes id={r.id} /> : null}
        <div className="flex flex-wrap gap-2 border-t border-line pt-4">
          <a href={resourceMedia.file(r.id, { sha: r.current, download: true })} className="inline-flex h-8 items-center gap-1.5 rounded-[9px] px-3 text-[12.5px] text-fg hairline hover:bg-hover">
            <DownloadSimple size={13} /> {r.kind === 'repo' ? 'Download the snapshot' : 'Download'}
          </a>
          {own && source ? (
            <Button size="sm" variant="secondary" icon={<ArrowClockwise size={13} />} loading={busy === 'refresh'} onClick={() => void refetch()}>
              {r.kind === 'repo' ? 'Get the newest commit' : 'Take a new snapshot'}
            </Button>
          ) : null}
          {r.kind === 'repo' ? (
            <Button
              size="sm"
              variant="ghost"
              icon={<ChartBar size={13} />}
              loading={busy === 'card'}
              onClick={() => void run('card', () => call('resources.make', { kind: 'stats', repo: r.id, projectId }), "Couldn't make the card")}
            >
              Make a repository card
            </Button>
          ) : null}
          {source ? null : r.kind !== 'font' ? (
            <Button size="sm" variant="ghost" icon={<Palette size={13} />} loading={busy === 'brand'} onClick={() => void run('brand', () => call('brand.set', { logo: r.id, ...(projectId ? { projectId } : {}) }), "Couldn't set the brand logo")}>
              Use as the brand logo
            </Button>
          ) : (
            <Button size="sm" variant="ghost" icon={<Palette size={13} />} loading={busy === 'brand'} onClick={() => void run('brand', () => call('brand.set', { fontResource: r.id, ...(projectId ? { projectId } : {}) }), "Couldn't set the brand font")}>
              Use as the brand font
            </Button>
          )}
          {r.origin === 'project' ? (
            <Button size="sm" variant="ghost" icon={<ArrowUpRight size={13} />} loading={busy === 'promote'} onClick={() => void run('promote', () => call('resources.promote', { ref: r.id, projectId }), "Couldn't move it to the workspace library")}>
              Move to the workspace library
            </Button>
          ) : null}
          {own && !source ? (
            <Button size="sm" variant="ghost" icon={<ArrowClockwise size={13} />} loading={busy === 'refresh'} onClick={() => void run('refresh', () => call('resources.refresh', { ref: r.id, projectId }), "Couldn't look at it again")}>
              Look again
            </Button>
          ) : null}
          {own ? (
            <Button size="sm" variant="ghost" icon={<Trash size={13} />} loading={busy === 'remove'} onClick={() => void remove()} className="text-danger">
              Remove
            </Button>
          ) : null}
        </div>
      </div>
    </div>
  );
}

export function useResourceDetails(projectId?: string) {
  const dialog = useDialog();
  return (r: { id: string; title: string }) => dialog.open<void>((close) => <DetailsBody id={r.id} projectId={projectId} close={() => close()} />, { title: r.title, size: 'lg' });
}

/* ---------- the grid ---------- */

export function ResourceGrid({ rows, onOpen, empty = 'Nothing matches.' }: { rows: ResourceRow[]; onOpen: (r: ResourceRow) => void; empty?: string }) {
  if (rows.length === 0) return <div className="rounded-[14px] px-4 py-8 text-center text-[13px] text-muted hairline">{empty}</div>;
  return (
    <ul className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4 2xl:grid-cols-6" aria-label="Resources">
      {rows.map((r) => (
        <li key={r.id}>
          <button type="button" onClick={() => onOpen(r)} className="group block w-full rounded-[14px] p-1.5 text-left transition-colors hover:bg-hover focus-visible:outline-2 focus-visible:outline-accent">
            <ResourcePicture r={r} className="aspect-[4/3] w-full rounded-[10px] hairline" />
            <div className="px-1 pb-1 pt-2">
              <div className="truncate text-[13px] font-medium text-fg" title={r.title}>
                {r.title}
              </div>
              <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[11.5px] text-subtle">
                <span className="tc truncate">{r.name}</span>
                <span aria-hidden>·</span>
                <span>{resourceKindLabel(r.kind)}</span>
              </div>
              <div className="mt-0.5 truncate text-[11.5px] text-subtle" title={`Added ${absolute(r.createdAt)}`} data-stamp>
                Added {stamp(r.createdAt)}
              </div>
              <div className="mt-1.5 flex flex-wrap gap-1">
                {r.from ? <Badge tone="info">from {r.from}</Badge> : r.origin === 'workspace' ? <Badge>workspace</Badge> : null}
                {r.missing ? <Badge tone="danger">{r.source.kind === 'repo' ? 'not in the repository' : 'missing from folder'}</Badge> : null}
                {!r.stored ? <Badge>on one computer</Badge> : null}
                {(r.tags ?? []).slice(0, 2).map((t) => (
                  <Badge key={t} tone="accent">
                    {t}
                  </Badge>
                ))}
              </div>
            </div>
          </button>
        </li>
      ))}
    </ul>
  );
}

/* ---------- linked folders (status like the watched folder) ---------- */

function FolderRow({ f }: { f: LinkedFolderRow }) {
  const toast = useToast();
  const host = useHost();
  const refresh = useRefreshResources();
  const dialog = useDialog();
  const [busy, setBusy] = useState<string | null>(null);
  const run = async (key: string, fn: () => Promise<{ message: string }>, title: string) => {
    setBusy(key);
    try {
      const out = await fn();
      refresh();
      toast({ tone: 'success', title: out.message });
    } catch (e) {
      failToast(toast, title, e);
    } finally {
      setBusy(null);
    }
  };
  const here = f.here;
  const state = f.unlinked ? 'off' : !f.herePath ? 'elsewhere' : f.paused ? 'paused' : here?.state === 'missing' ? 'missing' : 'watching';
  return (
    <li className="rounded-[12px] p-3 hairline" data-folder={f.id}>
      <div className="flex flex-wrap items-center gap-2">
        <FolderSimple size={16} className="shrink-0 text-accent-text" />
        <span className="min-w-0 truncate text-[13.5px] font-medium text-fg">{f.name}</span>
        {state === 'watching' ? (
          <Badge tone="ok">kept in step</Badge>
        ) : state === 'paused' ? (
          <Badge>{host.remote ? `paused on ${host.machine}` : 'paused here'}</Badge>
        ) : state === 'missing' ? (
          <Badge tone="danger">can't be found</Badge>
        ) : state === 'elsewhere' ? (
          <Badge tone="info">{host.remote ? `not on ${host.machine}` : 'on another computer'}</Badge>
        ) : (
          <Badge>no longer kept in step</Badge>
        )}
        {here?.pending?.length ? (
          <span className="inline-flex items-center gap-1 text-[12px] text-subtle">
            <Spinner size={10} /> {plural(here.pending.length, 'file')} arriving
          </span>
        ) : null}
      </div>
      <p className="mt-1 text-[12.5px] leading-relaxed text-muted">{f.text}</p>
      {f.herePath ? <p className="tc mt-0.5 truncate text-[11.5px] text-subtle" title={f.herePath}>{f.herePath}</p> : null}
      {here?.errors?.length ? <p className="mt-1 text-[12px] text-danger">{here.errors.slice(0, 2).join(' ')}</p> : null}
      {!f.unlinked ? (
        <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
          {state === 'elsewhere' && host.remote ? (
            <span className="text-[12px] text-subtle">To keep it in step on {host.machine}, link it there.</span>
          ) : state === 'elsewhere' ? (
            <Button size="sm" variant="secondary" loading={busy === 'here'} onClick={() => void run('here', async () => {
              const out = await chooseFolder({ folderId: f.id });
              return { message: out.canceled ? 'Nothing changed.' : (out.linked?.message ?? 'Linked here.') };
            }, "Couldn't link it here")}>
              Find it on this computer
            </Button>
          ) : (
            <>
              <Button size="sm" variant="ghost" icon={<ArrowClockwise size={12} />} loading={busy === 'rescan'} onClick={() => void run('rescan', () => setFolder({ folder: f.id, rescan: true }), "Couldn't look through it")}>
                Look again
              </Button>
              {f.paused ? (
                <Button size="sm" variant="ghost" icon={<Play size={12} />} loading={busy === 'resume'} onClick={() => void run('resume', () => setFolder({ folder: f.id, resume: true }), "Couldn't resume it")}>
                  Resume
                </Button>
              ) : (
                <Button size="sm" variant="ghost" icon={<Pause size={12} />} loading={busy === 'pause'} onClick={() => void run('pause', () => setFolder({ folder: f.id, pause: true }), "Couldn't pause it")} data-host-action>
                  {host.remote ? `Pause on ${host.machine}` : 'Pause here'}
                </Button>
              )}
            </>
          )}
          <label className="ml-auto inline-flex items-center gap-1.5 text-[12px] text-muted">
            Upload
            <select
              value={f.upload}
              onChange={(e) => void run('upload', () => setFolder({ folder: f.id, upload: e.target.value as LinkedFolderRow['upload'] }), "Couldn't change it")}
              className="h-8 rounded-[8px] bg-panel px-2 text-[12px] text-fg hairline"
              aria-label="When files upload"
            >
              <option value="small">small files now (up to {f.smallMb} MB), large ones when a cut uses them</option>
              <option value="all">everything now</option>
              <option value="on-use">only when a cut uses them</option>
            </select>
          </label>
          <Button
            size="sm"
            variant="ghost"
            loading={busy === 'unlink'}
            onClick={async () => {
              const ok = await dialog.confirm({ title: `Stop keeping ${f.name} in step?`, body: 'New and changed files stop coming in. What came in already stays, and nothing in the folder is touched.', confirmLabel: 'Stop' });
              if (ok) void run('unlink', () => call('resources.unlink', { folder: f.id }), "Couldn't stop it");
            }}
          >
            Stop
          </Button>
        </div>
      ) : null}
    </li>
  );
}

export function LinkedFolders({ projectId, className }: { projectId?: string; className?: string }) {
  const q = useOp('resources.folders', projectId ? { projectId } : {}, { refetchInterval: 15_000 });
  const folders = useMemo(() => (q.data?.folders ?? []).filter((f) => (projectId ? f.projectId === projectId : f.scope === 'workspace')), [q.data, projectId]);
  if (!folders.length) return null;
  return (
    <section aria-label="Linked folders" className={className}>
      <h3 className="mb-2 flex items-center gap-1.5 text-[13px] font-semibold text-fg">
        <FolderSimple size={14} /> Linked folders
      </h3>
      <ul className="space-y-2">
        {folders.map((f) => (
          <FolderRow key={f.id} f={f} />
        ))}
      </ul>
    </section>
  );
}
