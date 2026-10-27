import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import {
  Archive,
  ArrowCounterClockwise,
  ArrowLeft,
  BookmarkSimple,
  Books,
  CookingPot,
  PuzzlePiece,
  FilmSlate,
  FolderSimplePlus,
  MagnifyingGlass,
  PencilSimple,
  Plus,
  Stack,
  Trash,
  TrashSimple,
  Tray,
  Sparkle,
  UsersThree,
  X,
} from '@phosphor-icons/react';
import { call, media, toApiError } from '../api/client';
import { LIBRARY_QUERIES, LIBRARY_VIEWS, libraryLink, type LibraryView } from '../api/library';
import { useOp } from '../api/queries';
import type { AssetSummary, CollectionRow, LibraryItem, LibraryOut } from '../api/types';
import { plural, relative } from '../lib/format';
import { resetSelection } from '../lib/workbench';
import { AssetGallery, useAssetSelection, useLibraryView, ViewToggle } from '../components/AssetGallery';
import { EditableTitle } from '../components/AssetMeta';
import { AssetGridSkeleton, usePickProject } from '../components/Library';
import { SourceCredit } from '../components/LinkImports';
import { useChooseProject } from '../components/ProjectPicker';
import { SourceInspector } from '../components/SourceInspector';
import { inInbox, SourceMenu, useSourceActions } from '../components/SourceActions';
import { MenuButton } from '../layout/assistant/Menu';
import { useDialog } from '../ui/Dialog';
import { Button, cx, EmptyState, ErrorPanel, Freshness, Tabs, Thumb } from '../ui/primitives';
import { MotionBadge } from '../components/MotionBadge';
import { ownerTitle, useOwnership } from '../components/OwnerOnly';
import { PeoplePage } from './People';
import { MotionListPage } from './Motion';
import { AbilitiesPage } from './Abilities';
import { useToast } from '../ui/Toast';

/*
 * The Library (D96–D98): every clip, wherever it is filed. The Inbox is a
 * triage pile; each clip leaves it by being added to a project, put on a
 * collection (a named shelf), archived (keep for later) or moved to the Trash.
 * The tabs count each view, and a search looks through all of them (the Trash
 * included), so nothing filed away is ever hard to find.
 */

const EMPTY: Record<LibraryView, { icon: ReactNode; title: string; body: string }> = {
  inbox: {
    icon: <Tray size={20} />,
    title: 'The Inbox is empty',
    body: 'New footage waits here until you add it to a project, put it on a collection, archive it or move it to the Trash.',
  },
  collections: {
    icon: <BookmarkSimple size={20} />,
    title: 'No collections yet',
    body: 'A collection is a shelf you name, like Extra shots, Family or Stock. A clip can be on several, and a project can use everything on one.',
  },
  archived: { icon: <Archive size={20} />, title: 'Nothing archived', body: 'Archive keeps clips you want to keep but not in a project, out of the Inbox and out of the way.' },
  projects: { icon: <FilmSlate size={20} />, title: 'No footage in projects yet', body: 'Clips you add to a project show up here too.' },
  trash: { icon: <TrashSimple size={20} />, title: 'The Trash is empty', body: 'Clips you move to the Trash wait here until you delete them for good. You can restore them until then.' },
  all: { icon: <Books size={20} />, title: 'No footage yet', body: 'Drop footage anywhere to add it.' },
};

function isView(v: string | null): v is LibraryView {
  return !!v && ['inbox', 'collections', 'archived', 'projects', 'trash', 'all'].includes(v);
}

/** Where a clip is, as small chips (projects, collections, the Archive) or its stay in the Trash. */
function Where({ it, showProjects, hideCollection }: { it: LibraryItem; showProjects: boolean; hideCollection?: string }) {
  if (it.trash) {
    const parts = [it.trash.from, `moved ${relative(it.trash.at)}`];
    return (
      <div className="mt-0.5 min-w-0 text-[11.5px] leading-snug text-subtle" data-where>
        <span className="block truncate" title={parts.join(' · ')}>
          {parts.join(' · ')}
        </span>
        {it.trash.deletesOn ? <span className="block truncate text-danger">Deleted for good {relative(it.trash.deletesOn)}</span> : null}
      </div>
    );
  }
  const chips: { key: string; label: string; icon: ReactNode; to: string }[] = [];
  if (showProjects) for (const p of it.projects) chips.push({ key: p.id, label: p.name, icon: <FilmSlate size={10} />, to: `/p/${p.id}?tab=sources&asset=${it.id}` });
  for (const c of it.collections) if (c.id !== hideCollection) chips.push({ key: c.id, label: c.name, icon: <BookmarkSimple size={10} />, to: libraryLink({ view: 'collections', collection: c.id }) });
  if (it.archived) chips.push({ key: 'archive', label: 'Archive', icon: <Archive size={10} />, to: libraryLink({ view: 'archived' }) });
  if (!chips.length) return null;
  return (
    <div className="mt-1 flex min-w-0 flex-wrap gap-1" data-where>
      {chips.slice(0, 4).map((c) => (
        <Link
          key={c.key}
          to={c.to}
          onClick={(e) => e.stopPropagation()}
          className="inline-flex max-w-[150px] items-center gap-1 truncate rounded-full bg-hover px-2 py-[1px] text-[11px] text-muted hover:text-fg"
          title={c.label}
        >
          {c.icon}
          <span className="truncate">{c.label}</span>
        </Link>
      ))}
      {chips.length > 4 ? <span className="text-[11px] text-subtle">+{chips.length - 4}</span> : null}
    </div>
  );
}

/** Opens a clip in a large viewer, with what can be done to it where it is. */
function useOpenLibraryClip(actions: ReturnType<typeof useSourceActions>, collection?: { id: string; name: string }) {
  const dialog = useDialog();
  const pick = usePickProject();
  const own = useOwnership();
  return async (asset: AssetSummary) => {
    const r = await dialog.open<'add' | 'restore' | 'delete'>(
      (close) => (
        <div>
          <div className="-mt-1 mb-4">
            <EditableTitle asset={asset} heading />
            <div className="tc mt-0.5 truncate text-[11.5px] text-subtle">{asset.name}</div>
            <SourceCredit source={asset.source} className="mt-1" />
          </div>
          <SourceInspector asset={asset} compact />
          <div className="mt-5 flex flex-wrap items-center justify-end gap-2 border-t border-line pt-4">
            {asset.trashedAt ? (
              <>
                <Button variant="ghost" onClick={() => close()}>
                  Close
                </Button>
                <Button variant="danger" icon={<Trash size={14} />} onClick={() => close('delete')} disabled={!own.owner} title={ownerTitle(own, 'delete clips for good')}>
                  Delete forever…
                </Button>
                <Button variant="primary" icon={<ArrowCounterClockwise size={15} />} onClick={() => close('restore')}>
                  Restore
                </Button>
              </>
            ) : (
              <>
                <MenuButton items={actions.menuItems(asset, { collection }).filter((i) => i === 'separator' || i.label !== "Where it's used")} label={`More for ${asset.title}`} />
                <span className="flex-1" />
                <Button variant="ghost" onClick={() => close()}>
                  Close
                </Button>
                <Button variant="primary" icon={<FolderSimplePlus size={15} />} onClick={() => close('add')}>
                  Add to project
                </Button>
              </>
            )}
          </div>
        </div>
      ),
      { label: asset.title, size: 'lg' },
    );
    if (r === 'add') await pick(asset);
    if (r === 'restore') await actions.library.restore([asset]);
    if (r === 'delete') await actions.library.deleteForever([asset]);
  };
}

export function LibraryPage() {
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const qParam = params.get('q') ?? '';
  const [text, setText] = useState(qParam);
  const searching = qParam.trim() !== '';
  const collectionId = params.get('collection') ?? '';
  const viewParam = params.get('view');
  const view: LibraryView = isView(viewParam) ? viewParam : collectionId ? 'collections' : searching ? 'all' : 'inbox';
  useEffect(() => resetSelection({}), []);
  // The search box updates the address a moment after typing stops.
  useEffect(() => {
    const t = setTimeout(() => {
      if (text.trim() === qParam.trim()) return;
      const next = new URLSearchParams(params);
      if (text.trim()) next.set('q', text.trim());
      else next.delete('q');
      next.delete('view');
      next.delete('collection');
      setParams(next, { replace: true });
    }, 250);
    return () => clearTimeout(t);
  }, [text]); // eslint-disable-line react-hooks/exhaustive-deps
  // The address changed from elsewhere (a link, Back): show its search, but never trim what is being typed.
  useEffect(() => {
    setText((t) => (t.trim() === qParam.trim() ? t : qParam));
  }, [qParam]);

  const input = useMemo(
    () => ({ view, ...(collectionId ? { collection: collectionId } : {}), ...(searching ? { query: qParam.trim() } : {}) }),
    [view, collectionId, searching, qParam],
  );
  // While typing a search the last answer stays up; another view never shows the one before it.
  const lib = useOp('library.list', input, {
    placeholderData: (prev, prevQuery) => {
      const was = prevQuery?.queryKey[1] as { view?: string; collection?: string } | null | undefined;
      return was && was.view === input.view && (was.collection ?? '') === (input.collection ?? '') ? prev : undefined;
    },
  });
  const data = lib.data;
  const collection = data?.collection ? { id: data.collection.id, name: data.collection.name } : undefined;
  const actions = useSourceActions();
  const open = useOpenLibraryClip(actions, collection);

  // Deep link: /library?asset=… opens that clip, in the view where it is filed.
  const deepAsset = params.get('asset');
  useEffect(() => {
    if (!deepAsset || !data || lib.isPlaceholderData) return;
    const found = data.items.find((x) => x.id === deepAsset);
    const next = new URLSearchParams(params);
    next.delete('asset');
    if (found) {
      setParams(next, { replace: true });
      void open(found);
      return;
    }
    void call('asset.show', { id: deepAsset })
      .then(({ asset }) => {
        const home = asset.trash ? libraryLink({ view: 'trash', asset: asset.id }) : asset.archivedAt ? libraryLink({ view: 'archived', asset: asset.id })
          : asset.collectionIds?.length ? libraryLink({ view: 'collections', collection: asset.collectionIds[0], asset: asset.id })
          : asset.projectIds.length ? libraryLink({ view: 'projects', asset: asset.id }) : libraryLink({ asset: asset.id });
        const target = new URL(home, window.location.origin);
        const sameView = (target.searchParams.get('view') ?? 'inbox') === view && (target.searchParams.get('collection') ?? '') === collectionId;
        if (sameView) setParams(next, { replace: true });
        else navigate(home, { replace: true });
      })
      .catch(() => setParams(next, { replace: true }));
  }, [deepAsset, data]); // eslint-disable-line react-hooks/exhaustive-deps

  const go = (v: LibraryView) => {
    const next = new URLSearchParams();
    if (v !== 'inbox' || searching) next.set('view', v);
    if (searching) next.set('q', qParam);
    setParams(next);
  };

  const counts = data?.counts;
  const tabs = [
    ...(searching ? [{ value: 'all' as LibraryView, label: 'All', count: counts?.all }] : []),
    ...LIBRARY_VIEWS.map((v) => ({ value: v.value, label: v.label, count: counts ? counts[v.value as keyof typeof counts] : undefined })),
  ];
  const showShelves = view === 'collections' && !collectionId && !searching;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-end gap-x-6 gap-y-3">
        <LibraryHeading section="footage" />
        <form
          role="search"
          onSubmit={(e) => e.preventDefault()}
          className="flex h-10 w-full items-center gap-2 rounded-[10px] bg-panel px-3 hairline focus-within:outline-2 focus-within:outline-accent sm:w-[min(380px,40vw)]"
        >
          <MagnifyingGlass size={15} className="shrink-0 text-subtle" />
          <label htmlFor="library-search" className="sr-only">
            Search all footage, the Trash included
          </label>
          <input
            id="library-search"
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="Search all footage"
            title="Titles, file names, tags, collections and what was said, the Trash included"
            className="min-w-0 flex-1 bg-transparent text-[13.5px] text-fg outline-none placeholder:text-subtle"
          />
          {text ? (
            <button type="button" onClick={() => setText('')} aria-label="Clear the search" className="rounded-[6px] p-1 text-subtle hover:bg-hover hover:text-fg">
              <X size={13} />
            </button>
          ) : null}
        </form>
      </div>

      <div className="flex flex-wrap items-center gap-x-1 gap-y-2 border-b border-line pb-3">
        <Tabs label="Library views" value={view} onChange={go} items={tabs} className="min-w-0 max-sm:flex-wrap" />
        <LibrarySectionLinks section="footage" />
        <Freshness at={lib.dataUpdatedAt || undefined} fetching={lib.isFetching} onRefresh={() => void lib.refetch()} className="ml-auto max-sm:hidden" />
      </div>

      {searching && data ? <p className="text-[13px] text-muted" data-summary>{data.summary}</p> : null}

      {lib.error ? (
        <ErrorPanel error={lib.error} onRetry={() => void lib.refetch()} />
      ) : !data ? (
        <AssetGridSkeleton count={8} />
      ) : showShelves ? (
        <Shelves rows={data.collections} />
      ) : (
        <ViewBody key={`${view}-${collectionId}`} data={data} view={view} actions={actions} open={open} searching={searching} onBack={() => navigate(libraryLink({ view: 'collections' }))} />
      )}
    </div>
  );
}

type LibrarySection = 'footage' | 'people' | 'motion' | 'recipes' | 'abilities';

const SECTION_WORDS: Record<LibrarySection, string> = {
  footage: 'Every clip, wherever it is. New footage waits in the Inbox until you add it to a project, put it on a collection, archive it or move it to the Trash.',
  people: "Everyone you've named. Once you name someone, we recognize their voice and face in your other videos, and you can check each one.",
  motion:
    "Graphics your projects carry as their own code, for the moments the ready-made templates don't cover. A person looks at each version and approves it before it can be used in a cut.",
  recipes: // Recipes (D201–D208)
    'Videos that worked, written down so they can be made again: new art, a new recording or a new headline, with the same sections, look and sound. Before a run starts you see what it makes and what it costs.',
  abilities: // Abilities (D230–D235) and the menu of what MetaRush does built in (D268–D272)
    "Everything MetaRush can do, built in or added to this workspace, and whether each works on this computer now. An owner approves each added ability before it runs, in this computer's sandbox, where its keys never reach its code.",
};

function LibraryHeading({ section }: { section: LibrarySection }) {
  return (
    <div className="min-w-0 flex-1">
      <h1 className="text-[26px] font-semibold tracking-[-0.03em] text-fg sm:text-[28px]">Library</h1>
      <p className="mt-1 max-w-[70ch] text-[13.5px] leading-relaxed text-muted">{SECTION_WORDS[section]}</p>
    </div>
  );
}

/** People, Motion and Recipes, after the footage views in the Library's tab bar (D184). */
function LibrarySectionLinks({ section }: { section: LibrarySection }) {
  const item = (to: string, label: string, icon: ReactNode, on: boolean, badge?: ReactNode) => (
    <Link
      to={to}
      aria-current={on ? 'page' : undefined}
      className={cx(
        'inline-flex h-8 shrink-0 items-center gap-1.5 rounded-[9px] px-3 text-[13px] font-medium transition-colors duration-200 focus-visible:outline-2 focus-visible:outline-accent',
        on ? 'bg-panel text-fg hairline' : 'text-muted hover:bg-hover hover:text-fg',
      )}
      data-library-tab={label.toLowerCase()}
    >
      <span className={on ? 'text-accent-text' : 'text-subtle'} aria-hidden>
        {icon}
      </span>
      {label}
      {badge}
    </Link>
  );
  return (
    <nav aria-label="More in the Library" className="flex shrink-0 items-center gap-1">
      <span className="mx-1.5 h-5 w-px bg-line max-sm:hidden" aria-hidden />
      {item('/library/people', 'People', <UsersThree size={14} />, section === 'people')}
      {item('/library/motion', 'Motion', <Sparkle size={14} />, section === 'motion', <MotionBadge />)}
      {item('/library/recipes', 'Recipes', <CookingPot size={14} />, section === 'recipes')}
      {item('/library/abilities', 'Abilities', <PuzzlePiece size={14} />, section === 'abilities')}
    </nav>
  );
}

/** People, Motion or Recipes inside the Library: the same heading and tab bar, the footage views a click away. */
export function LibraryShell({ section, children }: { section: Exclude<LibrarySection, 'footage'>; children: ReactNode }) {
  const navigate = useNavigate();
  useEffect(() => resetSelection({}), []);
  // The same answer the Inbox shows (cached), for the views' counts.
  const lib = useOp('library.list', { view: 'inbox' }, { staleTime: 60_000 });
  const counts = lib.data?.counts;
  const tabs = LIBRARY_VIEWS.map((v) => ({ value: v.value, label: v.label, count: counts ? counts[v.value as keyof typeof counts] : undefined }));
  return (
    <div className="space-y-6" data-library-section={section}>
      <div className="flex flex-wrap items-end gap-x-6 gap-y-3">
        <LibraryHeading section={section} />
      </div>
      <div className="flex flex-wrap items-center gap-x-1 gap-y-2 border-b border-line pb-3">
        <Tabs label="Library views" value={null} onChange={(v: LibraryView) => navigate(libraryLink({ view: v }))} items={tabs} className="min-w-0 max-sm:flex-wrap" />
        <LibrarySectionLinks section={section} />
      </div>
      {children}
    </div>
  );
}

/** Library → People (/library/people): everyone you've named. */
export function LibraryPeoplePage() {
  return (
    <LibraryShell section="people">
      <PeoplePage />
    </LibraryShell>
  );
}

/** Library → Abilities (/library/abilities, D230–D235): capabilities added without a new MetaRush. */
export function LibraryAbilitiesPage() {
  return (
    <LibraryShell section="abilities">
      <AbilitiesPage />
    </LibraryShell>
  );
}

/** Library → Motion (/library/motion): the projects' motion graphics. */
export function LibraryMotionPage() {
  return (
    <LibraryShell section="motion">
      <MotionListPage />
    </LibraryShell>
  );
}

/** The collections as shelves (cards), with "New collection". */
function Shelves({ rows }: { rows: CollectionRow[] }) {
  const create = useCreateCollection();
  const navigate = useNavigate();
  return (
    <section aria-label="Collections">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <p className="min-w-0 flex-1 text-[13px] text-muted">Shelves you name for footage you want to keep together. A clip can be on several, and a project can use everything on one.</p>
        <Button
          size="sm"
          variant="primary"
          icon={<Plus size={13} weight="bold" />}
          onClick={async () => {
            const c = await create();
            if (c) navigate(libraryLink({ view: 'collections', collection: c.id }));
          }}
        >
          New collection
        </Button>
      </div>
      {rows.length === 0 ? (
        <div className="panel">
          <EmptyState compact icon={EMPTY.collections.icon} title={EMPTY.collections.title} body={`${EMPTY.collections.body} Put clips on one from their “…” menu, or make one here.`} />
        </div>
      ) : (
        <ul className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 2xl:grid-cols-5" aria-label="Collections">
          {rows.map((c) => (
            <li key={c.id}>
              <Link to={libraryLink({ view: 'collections', collection: c.id })} className="group block rounded-[16px] p-2 transition-colors hover:bg-raised hover:shadow-[var(--shadow-soft)]" data-collection={c.name}>
                <Thumb src={c.coverAssetId ? media.asset(c.coverAssetId, 'thumbnail') : undefined} alt="" className="aspect-[16/10] w-full rounded-[11px]" fallback={<BookmarkSimple size={22} />} />
                <div className="px-1 pb-1 pt-2.5">
                  <div className="truncate text-[14px] font-semibold tracking-[-0.01em] text-fg">{c.name}</div>
                  <div className="mt-0.5 truncate text-[12px] text-muted" title={c.text}>
                    {c.text}
                  </div>
                </div>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function useCreateCollection() {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  return async (): Promise<CollectionRow | undefined> => {
    const name = await dialog.prompt({
      title: 'New collection',
      body: 'A shelf for footage you want to keep together, like Extra shots, Family or Stock.',
      label: 'Name',
      placeholder: 'Extra shots',
      required: true,
      maxLength: 80,
      confirmLabel: 'Create',
    });
    if (!name) return undefined;
    try {
      const out = await call('collection.create', { name });
      for (const k of LIBRARY_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: out.message });
      return out.collection;
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: "Couldn't create it", body: [err.message, err.hint].filter(Boolean).join(' ') });
      return undefined;
    }
  };
}

/** One collection's header: what is on it, who uses it, and what can be done with it. */
function CollectionHeader({ c, onBack }: { c: CollectionRow; onBack: () => void }) {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const choose = useChooseProject();
  const refresh = () => {
    for (const k of LIBRARY_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  };
  const fail = (title: string, e: unknown) => {
    const err = toApiError(e);
    toast({ tone: 'error', title, body: [err.message, err.hint].filter(Boolean).join(' ') });
  };
  const useIn = async () => {
    const used = Object.fromEntries(c.usedBy.map((p) => [p.id, 'It uses this collection already']));
    const p = await choose({ title: `Use ${c.name} in a project`, body: `The project sees every clip on ${c.name}, and clips you put on it later. Nothing is copied.`, disabled: used, allowNew: true });
    if (!p) return;
    try {
      const out = await call('project.link', { projectId: p.id, from: c.id });
      refresh();
      toast({ tone: 'success', title: out.message, actions: [{ label: `Open ${out.link?.projectName ?? 'the project'}`, onClick: () => navigate(`/p/${p.id}?tab=sources`) }], durationMs: 9000 });
    } catch (e) {
      fail(`Couldn't use ${c.name} there`, e);
    }
  };
  const rename = async () => {
    const name = await dialog.prompt({ title: `Rename ${c.name}`, label: 'Name', defaultValue: c.name, required: true, maxLength: 80, confirmLabel: 'Rename' });
    if (!name || name === c.name) return;
    try {
      const out = await call('collection.rename', { collection: c.id, name });
      refresh();
      toast({ tone: 'success', title: out.message });
    } catch (e) {
      fail("Couldn't rename it", e);
    }
  };
  const remove = async () => {
    const ok = await dialog.confirm({
      title: `Delete the ${c.name} collection?`,
      body: `No clip is deleted: the ${plural(c.count, 'clip')} on it stay in MetaRush, back in the Inbox unless something else has them.${
        c.usedBy.length ? ` ${c.usedBy.map((p) => p.name).join(', ')} stop${c.usedBy.length === 1 ? 's' : ''} using it; clips their cuts use stay in those projects.` : ''
      }`,
      confirmLabel: 'Delete collection',
      tone: 'danger',
    });
    if (!ok) return;
    try {
      const out = await call('collection.delete', { collection: c.id });
      refresh();
      onBack();
      toast({ tone: 'success', title: out.message, durationMs: 9000 });
    } catch (e) {
      fail("Couldn't delete it", e);
    }
  };
  return (
    <div className="mb-4 rounded-[14px] p-3 hairline" data-collection-header>
      <div className="flex items-center gap-2">
        <button type="button" onClick={onBack} className="-ml-1 inline-flex items-center gap-1 rounded-[8px] px-1.5 py-1 text-[12.5px] text-muted hover:bg-hover hover:text-fg">
          <ArrowLeft size={13} /> All collections
        </button>
        <span className="flex-1" />
        <Button size="sm" variant="secondary" icon={<Stack size={13} />} onClick={() => void useIn()}>
          Use in a project…
        </Button>
        <MenuButton
          label={`More for ${c.name}`}
          items={[
            { label: 'Rename…', icon: <PencilSimple size={14} />, onSelect: () => void rename() },
            'separator',
            { label: 'Delete collection…', icon: <Trash size={14} />, danger: true, onSelect: () => void remove(), hint: 'No clip is deleted' },
          ]}
        />
      </div>
      <div className="mt-1.5 min-w-0 px-0.5">
        <h2 className="flex items-center gap-2 text-[17px] font-semibold tracking-[-0.015em] text-fg">
          <BookmarkSimple size={16} className="shrink-0 text-accent-text" />
          <span className="truncate">{c.name}</span>
        </h2>
        <div className="mt-0.5 text-[12.5px] text-muted">{c.text}</div>
      </div>
    </div>
  );
}

/** The clips of one view, with the actions that make sense there (one at a time, or selected together). */
function ViewBody({ data, view, actions, open, searching, onBack }: {
  data: LibraryOut;
  view: LibraryView;
  actions: ReturnType<typeof useSourceActions>;
  open: (a: AssetSummary) => Promise<void>;
  searching: boolean;
  onBack: () => void;
}) {
  const items = data.items;
  const [layout, setLayout] = useLibraryView(`library.${view}`);
  const selection = useAssetSelection(useMemo(() => items.map((a) => a.id), [items]));
  const chosen = items.filter((a) => selection.isSelected(a.id));
  const pick = usePickProject();
  const lib = actions.library;
  const own = useOwnership();
  const collection = data.collection ? { id: data.collection.id, name: data.collection.name } : undefined;
  const trashView = view === 'trash';
  const empty = EMPTY[view];
  const inProjects = chosen.filter((a) => a.projectIds.length > 0);

  return (
    <section aria-label={trashView ? 'The Trash' : 'Clips'}>
      {data.collection ? <CollectionHeader c={data.collection} onBack={onBack} /> : null}
      {trashView && !searching ? <TrashHeader data={data} onEmpty={() => void lib.emptyTrash()} /> : null}
      <div className="mb-3 flex flex-wrap items-center gap-2">
        {!searching && !trashView ? <p className="min-w-0 flex-1 text-[13px] text-muted">{data.summary}</p> : <span className="flex-1" />}
        <ViewToggle value={layout} onChange={setLayout} label="Library layout" />
      </div>
      {chosen.length > 0 ? (
        <div role="region" aria-label="Selected clips" className="fade-in sticky top-[calc(var(--mr-header-h)+7px)] z-20 mb-3 flex flex-wrap items-center gap-2 rounded-[12px] bg-raised px-3 py-2 shadow-[var(--shadow-soft)] hairline">
          <span className="text-[13px] font-medium text-fg">{plural(chosen.length, 'clip')} selected</span>
          {trashView ? (
            <>
              <Button size="sm" variant="primary" icon={<ArrowCounterClockwise size={13} />} onClick={() => void lib.restore(chosen)}>
                Restore
              </Button>
              <Button size="sm" variant="danger" icon={<Trash size={13} />} onClick={() => void lib.deleteForever(chosen)} disabled={!own.owner} title={ownerTitle(own, 'delete clips for good')}>
                Delete forever…
              </Button>
            </>
          ) : (
            <>
              <Button size="sm" variant="primary" icon={<FolderSimplePlus size={13} />} onClick={() => void pick(chosen)}>
                Add to project…
              </Button>
              <Button size="sm" variant="secondary" icon={<BookmarkSimple size={13} />} onClick={() => void lib.addToCollection(chosen)}>
                Add to collection…
              </Button>
              {collection ? (
                <Button size="sm" variant="ghost" icon={<X size={13} />} onClick={() => void lib.removeFromCollection(chosen, collection)}>
                  Remove from {collection.name}
                </Button>
              ) : null}
              {view === 'archived' ? (
                <Button size="sm" variant="ghost" icon={<Archive size={13} />} onClick={() => void lib.unarchive(chosen)}>
                  Take out of the Archive
                </Button>
              ) : inProjects.length < chosen.length ? (
                <Button size="sm" variant="ghost" icon={<Archive size={13} />} onClick={() => void lib.archive(chosen.filter((a) => a.projectIds.length === 0))}>
                  Archive
                </Button>
              ) : null}
              <Button
                size="sm"
                variant="ghost"
                icon={<TrashSimple size={13} />}
                disabled={inProjects.length === chosen.length}
                title={inProjects.length ? `${plural(inProjects.length, 'clip')} in projects stay${inProjects.length === 1 ? 's' : ''}: remove ${inProjects.length === 1 ? 'it' : 'them'} from the project first` : undefined}
                onClick={() => void lib.trash(chosen.filter((a) => a.projectIds.length === 0))}
              >
                Move to Trash
              </Button>
            </>
          )}
          <button type="button" onClick={selection.clear} className="ml-auto inline-flex items-center gap-1 rounded-[8px] px-2 py-1 text-[12px] text-muted hover:bg-hover hover:text-fg">
            <X size={12} /> Clear <span className="hidden sm:inline">(Esc)</span>
          </button>
        </div>
      ) : null}
      {items.length === 0 ? (
        <div className="panel">
          {searching ? (
            <EmptyState compact icon={<MagnifyingGlass size={20} />} title="Nothing here matches" body={data.summary} />
          ) : (
            <EmptyState compact icon={empty.icon} title={data.collection ? `Nothing on ${data.collection.name} yet` : empty.title} body={data.collection ? 'Put clips on it from their “…” menu, or select several in the Inbox.' : empty.body} />
          )}
        </div>
      ) : (
        <>
          <AssetGallery
            assets={items}
            view={layout}
            selection={selection}
            label={trashView ? 'Clips in the Trash' : 'Clips'}
            draggable={!trashView}
            onOpen={(a) => void open(a)}
            meta={(a) => <Where it={a as LibraryItem} showProjects={view !== 'inbox'} hideCollection={collection?.id} />}
            action={(a) => (
              <span className="flex items-center gap-1">
                {trashView || a.trashedAt ? (
                  <Button size="sm" variant="secondary" className="bg-raised/90 backdrop-blur" icon={<ArrowCounterClockwise size={13} />} aria-label={`Restore ${a.title}`} onClick={() => void lib.restore([a])}>
                    <span className="max-sm:hidden">Restore</span>
                  </Button>
                ) : inInbox(a) ? (
                  <Button size="sm" variant="secondary" className="bg-raised/90 backdrop-blur" icon={<FolderSimplePlus size={13} />} aria-label={`Add ${a.title} to a project`} onClick={() => void pick(a)}>
                    <span className="max-sm:hidden">Add</span>
                  </Button>
                ) : null}
                <SourceMenu asset={a} actions={actions} collection={collection} />
              </span>
            )}
          />
          {data.total > items.length ? <p className="mt-3 text-[12px] text-subtle">Showing {items.length} of {data.total}. Search to narrow it down.</p> : null}
        </>
      )}
    </section>
  );
}

/** The Trash's header: how much is there, whether it empties itself, and Empty Trash. */
function TrashHeader({ data, onEmpty }: { data: LibraryOut; onEmpty: () => void }) {
  const own = useOwnership(); // deleting for good is the owners' (D191)
  return (
    <div className="mb-4 flex flex-wrap items-center gap-x-4 gap-y-2 rounded-[14px] bg-hover/50 p-3.5 hairline" data-trash-header>
      <TrashSimple size={20} className="shrink-0 text-muted max-sm:hidden" />
      <div className="min-w-[15rem] flex-1">
        <div className="text-[13.5px] font-medium text-fg">{data.summary}</div>
        <div className="mt-0.5 text-[12px] text-muted">
          {data.emptyAfterDays > 0 ? (
            <>
              Emptied automatically after {plural(data.emptyAfterDays, 'day')}.{' '}
            </>
          ) : (
            <>Never emptied on its own. </>
          )}
          <Link to="/settings/storage#trash" className="text-accent-text hover:underline">
            Change in Settings → Storage
          </Link>
        </div>
      </div>
      {data.total > 0 ? (
        <Button size="sm" variant="danger" icon={<Trash size={13} />} onClick={onEmpty} disabled={!own.owner} title={ownerTitle(own, 'empty the Trash')}>
          Empty Trash…
        </Button>
      ) : null}
    </div>
  );
}
