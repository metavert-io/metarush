import { useCallback } from 'react';
import { useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { Trash, WarningCircle } from '@phosphor-icons/react';
import { call, toApiError } from '../api/client';
import { LIBRARY_QUERIES, libraryLink } from '../api/library';
import type { AssetSummary, LibraryActionOut, TrashEmptyOut, UseRow } from '../api/types';
import { bytes, plural } from '../lib/format';
import { useDialog } from '../ui/Dialog';
import { Button } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { useChooseCollection } from './CollectionPicker';
import { UsesList } from './SourceActions';

/*
 * Filing footage away (D96–D98): Archive, Add to collection…, Move to Trash,
 * Restore, and deleting for good from the Trash. Every action is the same op
 * the CLI runs, says what happened in a toast, and offers Undo where it can be
 * undone. Deleting for good asks first, with how much goes, in a styled
 * warning (never a browser dialog).
 */

type Clip = Pick<AssetSummary, 'id' | 'title'>;

const what = (list: Clip[]) => (list.length === 1 ? `“${list[0]!.title}”` : plural(list.length, 'clip'));

/** The ones an action changed (Undo puts back only those). */
function doneIDs(out: LibraryActionOut): string[] {
  return out.items.filter((r) => r.outcome === 'done').map((r) => r.assetId);
}

/** The styled warning before deleting for good: what goes, how big, what stays and why. */
function PermanentDelete({ check, close }: { check: TrashEmptyOut; close: (ok?: boolean) => void }) {
  const shown = check.items.slice(0, 6);
  return (
    <div>
      <div className="flex items-start gap-3">
        <WarningCircle size={22} weight="fill" className="mt-0.5 shrink-0 text-danger" />
        <div className="min-w-0 text-[13.5px] leading-relaxed">
          <p className="text-fg">Their previews, transcripts and speaker data go too, for every machine.</p>
          <p className="mt-1 font-medium text-danger">This can't be undone.</p>
        </div>
      </div>
      {shown.length > 1 ? (
        <ul className="mt-4 divide-y divide-line overflow-hidden rounded-[12px] text-[13px] hairline" aria-label="What is deleted">
          {shown.map((it) => (
            <li key={it.assetId} className="flex items-center gap-3 px-3 py-2">
              <span className="min-w-0 flex-1 truncate text-fg">{it.title}</span>
              <span className="tc shrink-0 text-[12px] text-muted">{bytes(it.size)}</span>
            </li>
          ))}
          {check.items.length > shown.length ? <li className="px-3 py-2 text-[12.5px] text-muted">and {plural(check.items.length - shown.length, 'more clip')}</li> : null}
        </ul>
      ) : null}
      {check.kept.length ? (
        <div className="mt-4 rounded-[12px] bg-hover/60 p-3">
          <div className="text-[12.5px] font-medium text-muted">{check.kept.length === 1 ? 'Stays in the Trash' : `${check.kept.length} stay in the Trash`}</div>
          <ul className="mt-1.5 space-y-2">
            {check.kept.slice(0, 4).map((k) => (
              <li key={k.assetId} className="text-[12.5px] leading-relaxed">
                <span className="font-medium text-fg">“{k.title}”</span> <span className="text-muted">{k.reason}</span>
                {k.uses?.length ? <UsesList uses={k.uses as UseRow[]} onNavigate={() => close()} /> : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <div className="mt-6 flex flex-wrap justify-end gap-2 border-t border-line pt-4">
        <Button variant="ghost" data-autofocus onClick={() => close()}>
          Cancel
        </Button>
        <Button variant="danger" icon={<Trash size={14} />} onClick={() => close(true)}>
          Delete forever
        </Button>
      </div>
    </div>
  );
}

export function useLibraryActions(project?: { id: string; name: string }) {
  const qc = useQueryClient();
  const toast = useToast();
  const dialog = useDialog();
  const navigate = useNavigate();
  const chooseCollection = useChooseCollection();
  const refresh = useCallback(() => {
    for (const k of LIBRARY_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  }, [qc]);
  const fail = (title: string, e: unknown) => {
    const err = toApiError(e);
    const rows = (err.details as { items?: { outcome: string; reason?: string }[] } | undefined)?.items ?? [];
    const one = rows.length === 1 && rows[0]!.outcome === 'refused' && rows[0]!.reason ? `${rows[0]!.reason.charAt(0).toUpperCase()}${rows[0]!.reason.slice(1)}.` : null;
    toast({ tone: 'error', title, body: one ?? [err.message, err.hint].filter(Boolean).join(' ') });
  };
  const ids = (list: Clip[]) => list.map((a) => a.id);

  /** Files clips in the Archive (out of this project too, when there is one), with Undo. */
  const archive = async (list: Clip[]) => {
    if (!list.length) return;
    let out: LibraryActionOut;
    try {
      out = await call('asset.archive', { assetIds: ids(list), ...(project ? { projectId: project.id } : {}) });
    } catch (e) {
      fail(`Didn't archive ${what(list)}`, e);
      return;
    }
    refresh();
    const done = doneIDs(out);
    toast({
      tone: 'success',
      title: out.message,
      body: project ? `Out of ${project.name}, kept in the Archive.` : 'Kept in the Archive; nothing was deleted.',
      action: {
        label: 'Undo',
        onClick: async () => {
          try {
            await call('asset.unarchive', { assetIds: done });
            if (project) for (const id of done) await call('asset.link', { assetId: id, projectId: project.id });
            refresh();
            toast({ title: project ? `Put ${what(list)} back in ${project.name}` : `Put ${what(list)} back in the Inbox` });
          } catch (e) {
            fail("Couldn't undo it", e);
          }
        },
      },
      actions: [{ label: 'Open the Archive', onClick: () => navigate(libraryLink({ view: 'archived' })) }],
      durationMs: 9000,
    });
  };

  /** Takes clips out of the Archive, with Undo. */
  const unarchive = async (list: Clip[]) => {
    if (!list.length) return;
    try {
      const out = await call('asset.unarchive', { assetIds: ids(list) });
      refresh();
      const done = doneIDs(out);
      toast({
        tone: 'success',
        title: out.message,
        action: {
          label: 'Undo',
          onClick: async () => {
            await call('asset.archive', { assetIds: done }).catch((e) => fail("Couldn't undo it", e));
            refresh();
          },
        },
      });
    } catch (e) {
      fail(`Didn't take ${what(list)} out of the Archive`, e);
    }
  };

  /** Puts clips on a collection chosen by typing (or a new one), with Undo. */
  const addToCollection = async (list: Clip[], already: string[] = []) => {
    if (!list.length) return;
    const disabled = Object.fromEntries(already.map((id) => [id, list.length === 1 ? 'It is on it already' : 'They are on it already']));
    const choice = await chooseCollection({ title: list.length === 1 ? `Add “${list[0]!.title}” to a collection` : `Add ${plural(list.length, 'clip')} to a collection`, disabled });
    if (!choice) return;
    const create = 'create' in choice;
    try {
      const out = await call('collection.add', { collection: create ? choice.create : choice.id, assetIds: ids(list), ...(create ? { create: true } : {}) });
      refresh();
      const col = out.collection;
      const done = out.items.filter((r) => r.outcome === 'done').map((r) => r.assetId);
      toast({
        tone: 'success',
        title: out.message,
        action: done.length
          ? {
              label: 'Undo',
              onClick: async () => {
                await call('collection.remove', { collection: col?.id ?? '', assetIds: done }).catch((e) => fail("Couldn't undo it", e));
                refresh();
              },
            }
          : undefined,
        actions: col ? [{ label: `Open ${col.name}`, onClick: () => navigate(libraryLink({ view: 'collections', collection: col.id })) }] : undefined,
        durationMs: 9000,
      });
    } catch (e) {
      fail(`Didn't add ${what(list)} to the collection`, e);
    }
  };

  /** Takes clips off a collection (nothing is deleted), with Undo. */
  const removeFromCollection = async (list: Clip[], collection: { id: string; name: string }) => {
    if (!list.length) return;
    try {
      const out = await call('collection.remove', { collection: collection.id, assetIds: ids(list) });
      refresh();
      const done = out.items.filter((r) => r.outcome === 'done').map((r) => r.assetId);
      toast({
        tone: 'success',
        title: out.message,
        body: 'Nothing was deleted.',
        action: {
          label: 'Undo',
          onClick: async () => {
            await call('collection.add', { collection: collection.id, assetIds: done }).catch((e) => fail("Couldn't undo it", e));
            refresh();
          },
        },
      });
    } catch (e) {
      fail(`Didn't take ${what(list)} off ${collection.name}`, e);
    }
  };

  /** Moves clips to the Trash at once (nothing is deleted), with Undo. */
  const trash = async (list: Clip[]) => {
    if (!list.length) return;
    let out: LibraryActionOut;
    try {
      out = await call('asset.trash', { assetIds: ids(list), ...(project ? { projectId: project.id } : {}) });
    } catch (e) {
      fail(`Didn't move ${what(list)} to the Trash`, e);
      return;
    }
    refresh();
    const done = out.items.filter((r) => r.outcome === 'done').map((r) => r.assetId);
    toast({
      tone: 'success',
      title: out.message,
      body: 'Nothing is deleted until you empty the Trash.',
      action: done.length
        ? {
            label: 'Undo',
            onClick: async () => {
              try {
                const back = await call('asset.restore', { assetIds: done });
                refresh();
                toast({ title: back.message });
              } catch (e) {
                fail("Couldn't undo it", e);
              }
            },
          }
        : undefined,
      actions: [{ label: 'Open the Trash', onClick: () => navigate(libraryLink({ view: 'trash' })) }],
      durationMs: 9000,
    });
  };

  /** Puts clips in the Trash back where they were. */
  const restore = async (list: Clip[]) => {
    if (!list.length) return;
    try {
      const out = await call('asset.restore', { assetIds: ids(list) });
      refresh();
      toast({ tone: 'success', title: out.message });
    } catch (e) {
      fail(`Didn't restore ${what(list)}`, e);
    }
  };

  /** Deletes clips in the Trash for good (all of it without a list), after the styled warning with sizes. */
  const deleteForever = async (list?: Clip[]) => {
    const input = list?.length ? { assetIds: ids(list) } : {};
    let check: TrashEmptyOut;
    try {
      check = await call('trash.empty', { ...input, dryRun: true });
    } catch (e) {
      fail(list ? `Can't delete ${what(list)}` : "Can't empty the Trash", e);
      return;
    }
    if (check.count === 0) {
      await dialog.open<void>(
        (close) => (
          <div>
            <p className="text-[13.5px] leading-relaxed text-fg">{check.message}</p>
            {check.kept.map((k) =>
              k.uses?.length ? (
                <div key={k.assetId} className="mt-3">
                  <UsesList uses={k.uses as UseRow[]} onNavigate={() => close()} />
                </div>
              ) : null,
            )}
            <div className="mt-5 flex justify-end border-t border-line pt-4">
              <Button variant="primary" onClick={() => close()}>
                OK
              </Button>
            </div>
          </div>
        ),
        { title: list?.length === 1 ? `“${list[0]!.title}” can't be deleted yet` : 'Nothing can be deleted yet', size: 'md' },
      );
      return;
    }
    const ok = await dialog.open<boolean>((close) => <PermanentDelete check={check} close={close} />, { title: check.question, size: 'md' });
    if (!ok) return;
    try {
      const out = await call('trash.empty', input);
      refresh();
      toast({ tone: 'success', title: out.message });
    } catch (e) {
      fail("Didn't delete them", e);
    }
  };

  return { archive, unarchive, addToCollection, removeFromCollection, trash, restore, deleteForever, emptyTrash: () => deleteForever() };
}
