import { useCallback, type ReactNode } from 'react';
import { Link, useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { Archive, ArrowCounterClockwise, ArrowSquareOut, ArrowsLeftRight, BookmarkSimple, FilmSlate, MapPin, Palette, Stack, Trash, TrashSimple, XCircle } from '@phosphor-icons/react';
import { call, toApiError } from '../api/client';
import { libraryLink } from '../api/library';
import { useOp } from '../api/queries';
import { RESOURCE_QUERIES } from '../api/resources';
import type { AssetSummary, UseRow } from '../api/types';
import { plural } from '../lib/format';
import { MenuButton, type MenuItem } from '../layout/assistant/Menu';
import { useDialog } from '../ui/Dialog';
import { Button, ErrorPanel, Skeleton } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { useChooseProject } from './ProjectPicker';
import { useLibraryActions } from './LibraryActions';
import { ownerTitle, useOwnership } from './OwnerOnly';

/*
 * What can be done to a source from a project's Sources tab (C, D, E): move it
 * to another project, remove it from this one (it stays in the workspace, back
 * in the Inbox when no project has it), delete it for good (a person, after a
 * styled confirmation; refused while anything uses it), and see where it is
 * used. The "…" menu is built from a list; the Library's actions (D96–D98:
 * Add to collection…, Archive, Move to Trash, and Restore in the Trash) are
 * part of it on every surface that shows footage.
 */

const REFRESH = ['asset.list', 'asset.show', 'project.show', 'project.list', 'activity.summary', 'asset.where', 'search'];

function joinNames(names: string[]): string {
  if (names.length <= 1) return names[0] ?? '';
  return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
}

/* ---------- where it's used (E) ---------- */

const KIND_ICON: Record<string, ReactNode> = {
  project: <FilmSlate size={15} />,
  linked: <Stack size={15} />,
  cut: <ArrowSquareOut size={15} />,
  brand: <Palette size={15} />,
  nested: <Stack size={15} />,
  collection: <BookmarkSimple size={15} />,
  archive: <Archive size={15} />,
  trash: <TrashSimple size={15} />,
};

/** The places something is used, each a link (a project's Sources tab, or a cut's version). */
export function UsesList({ uses, empty = 'Not used anywhere yet.', onNavigate }: { uses: UseRow[]; empty?: string; onNavigate?: () => void }) {
  if (uses.length === 0) return <p className="rounded-[12px] px-3 py-4 text-center text-[13px] text-muted hairline">{empty}</p>;
  return (
    <ul className="space-y-1" aria-label="Where it's used">
      {uses.map((u, i) => (
        <li key={`${u.kind}-${u.projectId ?? ''}-${u.revisionId ?? ''}-${i}`}>
          <Link to={u.link} onClick={onNavigate} className="flex items-start gap-2.5 rounded-[10px] px-2.5 py-2 text-[13px] text-fg hover:bg-hover">
            <span className="mt-[1px] shrink-0 text-subtle" aria-hidden>
              {KIND_ICON[u.kind] ?? <MapPin size={15} />}
            </span>
            <span className="min-w-0 flex-1 leading-snug">{u.text}</span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function AssetWhereBody({ asset, close }: { asset: { id: string; title: string }; close: () => void }) {
  const q = useOp('asset.where', { assetId: asset.id });
  return (
    <div>
      {q.isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-10" />
          <Skeleton className="h-10" />
        </div>
      ) : q.error ? (
        <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <>
          <p className="mb-3 text-[13px] text-muted">{q.data?.summary}</p>
          <UsesList uses={q.data?.uses ?? []} onNavigate={close} />
        </>
      )}
      <div className="mt-4 flex justify-end border-t border-line pt-4">
        <Button variant="ghost" onClick={close}>
          Close
        </Button>
      </div>
    </div>
  );
}

/** Opens "Where it's used" for a clip. */
export function useAssetWhere() {
  const dialog = useDialog();
  return (asset: { id: string; title: string }) =>
    dialog.open<void>((close) => <AssetWhereBody asset={asset} close={() => close()} />, { title: `Where “${asset.title}” is used`, size: 'md' });
}

/* ---------- move, remove, delete ---------- */

/** Footage that is filed nowhere (in no project or collection, not archived, not in the Trash). */
export function inInbox(a: Pick<AssetSummary, 'projectIds' | 'collectionIds' | 'archived' | 'trashedAt'>): boolean {
  return a.projectIds.length === 0 && !a.collectionIds?.length && !a.archived && !a.trashedAt;
}

/**
 * Actions on sources of a project, or (no project) of the Inbox and the
 * Library: where it's used, filing it away (Add to collection…, Archive, Move
 * to Trash), Restore from the Trash, and deleting for good.
 */
export function useSourceActions(project?: { id: string; name: string }) {
  const qc = useQueryClient();
  const toast = useToast();
  const dialog = useDialog();
  const choose = useChooseProject();
  const where = useAssetWhere();
  const navigate = useNavigate();
  const library = useLibraryActions(project);
  const own = useOwnership(); // deleting for good is the owners' (D191)
  const projects = useOp('project.list', {}, { staleTime: 60_000 });
  const projectName = useCallback((id: string) => projects.data?.projects.find((p) => p.id === id)?.name ?? 'another project', [projects.data]);
  const refresh = useCallback(() => {
    for (const k of [...REFRESH, ...RESOURCE_QUERIES]) void qc.invalidateQueries({ queryKey: [k] });
  }, [qc]);
  const what = (list: AssetSummary[]) => (list.length === 1 ? `“${list[0]!.title}”` : plural(list.length, 'clip'));
  const fail = (title: string, e: unknown) => {
    const err = toApiError(e);
    toast({ tone: 'error', title, body: [err.message, err.hint].filter(Boolean).join(' ') });
  };

  /** Moves clips between projects, with Undo. */
  const moveBetween = async (list: AssetSummary[], from: { id: string; name: string }, to: { id: string; name: string }): Promise<boolean> => {
    const moved: AssetSummary[] = [];
    for (const a of list) {
      try {
        await call('asset.move', { assetId: a.id, from: from.id, to: to.id });
        moved.push(a);
      } catch (e) {
        fail(`Didn't move “${a.title}”`, e);
      }
    }
    refresh();
    if (!moved.length) return false;
    toast({
      tone: 'success',
      title: `Moved ${what(moved)} to ${to.name}`,
      body: `${moved.length === 1 ? 'It is' : 'They are'} no longer in ${from.name}.`,
      action: {
        label: 'Undo',
        onClick: async () => {
          for (const a of moved) {
            try {
              await call('asset.move', { assetId: a.id, from: to.id, to: from.id });
            } catch (e) {
              fail(`Didn't move “${a.title}” back`, e);
            }
          }
          refresh();
          toast({ title: `Moved ${what(moved)} back to ${from.name}` });
        },
      },
      durationMs: 9000,
    });
    return true;
  };

  const move = async (list: AssetSummary[]) => {
    const own = list.filter((a) => !a.from);
    if (!own.length || !project) return;
    const elsewhere: Record<string, string> = {};
    if (own.length === 1) for (const pid of own[0]!.projectIds) if (pid !== project.id) elsewhere[pid] = 'It is already there';
    const to = await choose({ title: own.length === 1 ? `Move “${own[0]!.title}” to…` : `Move ${plural(own.length, 'clip')} to…`, exclude: [project.id], disabled: elsewhere, allowNew: true });
    if (!to) return;
    const target = to.name ? to : { id: to.id, name: projectName(to.id) };
    await moveBetween(own, project, target);
  };

  const remove = async (list: AssetSummary[]) => {
    if (!project) return;
    const own = list.filter((a) => !a.from);
    const removed: AssetSummary[] = [];
    let inbox = 0;
    for (const a of own) {
      try {
        const out = await call('asset.link', { assetId: a.id, projectId: project.id, unlink: true });
        removed.push(a);
        if (inInbox(out.asset)) inbox++;
      } catch (e) {
        fail(`Didn't remove “${a.title}”`, e);
      }
    }
    refresh();
    if (!removed.length) return;
    const back = inbox === removed.length ? (removed.length === 1 ? " — it's back in the Inbox" : " — they're back in the Inbox") : '';
    toast({
      tone: 'success',
      title: `Removed ${what(removed)} from ${project.name}${back}`,
      body: 'Nothing was deleted.',
      action: {
        label: 'Undo',
        onClick: async () => {
          for (const a of removed) {
            try {
              await call('asset.link', { assetId: a.id, projectId: project.id });
            } catch (e) {
              fail(`Didn't put “${a.title}” back`, e);
            }
          }
          refresh();
          toast({ title: `Put ${what(removed)} back in ${project.name}` });
        },
      },
      durationMs: 9000,
    });
  };

  /** Permanently deletes one clip (a person, after confirming); refused while anything uses it. */
  const del = async (a: AssetSummary) => {
    let check;
    try {
      check = await call('asset.delete', { assetId: a.id, projectId: project?.id, forever: true, dryRun: true });
    } catch (e) {
      fail(`Can't delete “${a.title}”`, e);
      return;
    }
    if (!check.allowed) {
      await dialog.open<void>(
        (close) => (
          <div>
            <div className="flex items-start gap-3">
              <XCircle size={20} weight="fill" className="mt-0.5 shrink-0 text-danger" />
              <div className="min-w-0 text-[13.5px] leading-relaxed text-fg">
                {check.blockers.map((b) => (
                  <p key={b}>{b}</p>
                ))}
              </div>
            </div>
            {check.uses.length ? (
              <div className="mt-4">
                <div className="mb-1.5 text-[12.5px] font-medium text-muted">Where it's used</div>
                <UsesList uses={check.uses} onNavigate={() => close()} />
              </div>
            ) : null}
            <div className="mt-5 flex justify-end border-t border-line pt-4">
              <Button variant="primary" onClick={() => close()}>
                OK
              </Button>
            </div>
          </div>
        ),
        { title: `“${a.title}” can't be deleted yet`, size: 'md' },
      );
      return;
    }
    const ok = await dialog.confirm({
      title: `Permanently delete “${a.title}”?`,
      body: "This removes the video, its preview, transcript and speaker data from storage for every machine. It can't be undone.",
      confirmLabel: 'Delete forever',
      tone: 'danger',
    });
    if (!ok) return;
    try {
      const out = await call('asset.delete', { assetId: a.id, projectId: project?.id, forever: true });
      refresh();
      toast({ tone: 'success', title: `Deleted “${a.title}”`, body: out.message });
    } catch (e) {
      fail(`Didn't delete “${a.title}”`, e);
    }
  };

  /**
   * The "…" menu for one source. Linked footage (from another project or a
   * collection) can only be looked up. In the Library, `collection` is the
   * collection being shown (its clips can be taken off it).
   */
  const menuItems = (a: AssetSummary, opts: { collection?: { id: string; name: string } } = {}): (MenuItem | 'separator')[] => {
    if (a.from) {
      const open = a.fromCollection ? libraryLink({ view: 'collections', collection: a.fromProjectId, asset: a.id }) : `/p/${a.fromProjectId}?tab=sources&asset=${a.id}`;
      return [
        { label: "Where it's used", icon: <MapPin size={14} />, onSelect: () => void where(a) },
        { label: `Open in ${a.from}`, icon: <ArrowSquareOut size={14} />, onSelect: () => a.fromProjectId && navigate(open), hint: 'It comes from there; nothing was copied' },
      ];
    }
    if (a.trashedAt)
      return [
        { label: 'Restore', icon: <ArrowCounterClockwise size={14} />, onSelect: () => void library.restore([a]), hint: 'Back where it was' },
        { label: "Where it's used", icon: <MapPin size={14} />, onSelect: () => void where(a) },
        'separator',
        {
          label: 'Delete forever…',
          icon: <Trash size={14} />,
          danger: true,
          disabled: !own.owner,
          hint: ownerTitle(own, 'delete clips for good'),
          onSelect: () => void library.deleteForever([a]),
        },
      ];
    const others = a.projectIds.filter((p) => p !== project?.id).map(projectName);
    const file: MenuItem[] = [
      { label: 'Add to collection…', icon: <BookmarkSimple size={14} />, onSelect: () => void library.addToCollection([a], a.collectionIds ?? []) },
      ...(opts.collection
        ? [{ label: `Remove from ${opts.collection.name}`, icon: <XCircle size={14} />, onSelect: () => void library.removeFromCollection([a], opts.collection!), hint: 'Nothing is deleted' }]
        : []),
      ...(a.archived && !project
        ? [{ label: 'Take out of the Archive', icon: <Archive size={14} />, onSelect: () => void library.unarchive([a]), hint: inInbox({ ...a, archived: false }) ? 'Back to the Inbox' : undefined }]
        : project || a.projectIds.length === 0
          ? [{ label: 'Archive', icon: <Archive size={14} />, onSelect: () => void library.archive([a]), hint: project ? 'Out of this project, kept in the Archive' : 'Keep it for later, out of the Inbox' }]
          : []), // a clip in a project is archived from that project's Sources
    ];
    const toTrash: MenuItem = {
      label: 'Move to Trash',
      icon: <TrashSimple size={14} />,
      danger: true,
      disabled: others.length > 0,
      hint: others.length ? `${project ? 'Also in' : 'In'} ${joinNames(others)}` : 'You can restore it later',
      onSelect: () => void library.trash([a]),
    };
    if (!project)
      return [
        ...file,
        { label: "Where it's used", icon: <MapPin size={14} />, onSelect: () => void where(a) },
        'separator',
        toTrash,
        { label: 'Delete…', icon: <Trash size={14} />, danger: true, disabled: others.length > 0, hint: others.length ? `In ${joinNames(others)}` : undefined, onSelect: () => void del(a) },
      ];
    return [
      { label: 'Move to another project…', icon: <ArrowsLeftRight size={14} />, onSelect: () => void move([a]) },
      { label: 'Remove from this project', icon: <XCircle size={14} />, onSelect: () => void remove([a]), hint: others.length ? `It stays in ${joinNames(others)}` : 'It goes back to the Inbox' },
      ...file,
      { label: "Where it's used", icon: <MapPin size={14} />, onSelect: () => void where(a) },
      'separator',
      toTrash,
      {
        label: 'Delete…',
        icon: <Trash size={14} />,
        danger: true,
        disabled: others.length > 0,
        hint: others.length ? `Also in ${joinNames(others)}` : undefined,
        onSelect: () => void del(a),
      },
    ];
  };

  return { move, remove, del, where, menuItems, moveBetween, library };
}

/** The "…" button on a source. */
export function SourceMenu({ asset, actions, collection }: { asset: AssetSummary; actions: ReturnType<typeof useSourceActions>; collection?: { id: string; name: string } }) {
  return (
    <div onClick={(e) => e.stopPropagation()} onKeyDown={(e) => e.stopPropagation()} className="rounded-[9px] bg-raised/90 backdrop-blur">
      <MenuButton items={actions.menuItems(asset, { collection })} label={`More for ${asset.title}`} />
    </div>
  );
}
