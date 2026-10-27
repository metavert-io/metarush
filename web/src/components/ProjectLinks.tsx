import { useState } from 'react';
import { Link } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowRight, BookmarkSimple, Check, Copy, FilmSlate, LinkBreak, Stack, StackSimple } from '@phosphor-icons/react';
import { call, media, toApiError } from '../api/client';
import { libraryLink, LIBRARY_QUERIES } from '../api/library';
import { useOp } from '../api/queries';
import { RESOURCE_QUERIES } from '../api/resources';
import type { CollectionRow, ProjectLinkRow, ProjectShowOut } from '../api/types';
import { plural } from '../lib/format';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, cx, ErrorPanel, Skeleton, Thumb } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { ProjectPicker } from './ProjectPicker';

/*
 * Using another project's things (D83, item 10): link a whole project (the
 * default: its footage, resources, approved motion graphics and cut; new
 * things appear by themselves; nothing is copied), or only part of it (by
 * kind, tag, person or linked folder, or picked items). Its cut can also be
 * played inside this one (nested, pinned to a version) or copied in as a
 * starting point.
 */

const KINDS: { id: string; label: string }[] = [
  { id: 'footage', label: 'Footage' },
  { id: 'resources', label: 'Resources (logos, pictures, fonts)' },
  { id: 'motion', label: 'Approved motion graphics' },
  { id: 'cuts', label: 'Its cut' },
];

const REFRESH = [...RESOURCE_QUERIES, 'revision.list', 'revision.show', 'cut.show', 'cut.where'] as const;

function splitList(s: string): string[] {
  return s
    .split(/[,\n]/)
    .map((x) => x.trim())
    .filter(Boolean);
}

type Mode = 'all' | 'some' | 'pick';

function UseFromBody({ project, sourceId, existing, close }: { project: { id: string; name: string }; sourceId: string; existing?: ProjectLinkRow; close: (done?: boolean) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const src = useOp('project.show', { id: sourceId });
  const res = useOp('resources.list', { projectId: sourceId, only: 'project' });
  const folders = useOp('resources.folders', { projectId: sourceId });
  const [mode, setMode] = useState<Mode>(existing && !existing.whole ? (existing.items?.length ? 'pick' : 'some') : 'all');
  const [kinds, setKinds] = useState<string[]>(existing?.only?.kinds ?? ['footage', 'resources', 'motion', 'cuts']);
  const [tags, setTags] = useState((existing?.only?.tags ?? []).join(', '));
  const [people, setPeople] = useState((existing?.only?.people ?? []).join(', '));
  const [folderIds, setFolderIds] = useState<string[]>(existing?.only?.folders ?? []);
  const [items, setItems] = useState<string[]>(existing?.items?.map((i) => i.id) ?? []);
  const [busy, setBusy] = useState<string | null>(null);
  const name = src.data?.project.name ?? 'that project';
  const own = (src.data?.assets ?? []).filter((a) => !a.from);
  const resources = res.data?.resources ?? [];
  const head = src.data?.revisions.find((r) => r.id === src.data?.headRevisionId);

  const refresh = () => {
    for (const k of REFRESH) void qc.invalidateQueries({ queryKey: [k] });
  };
  const fail = (title: string, e: unknown) => {
    const err = toApiError(e);
    toast({ tone: 'error', title, body: [err.message, err.hint].filter(Boolean).join(' ') });
  };

  const link = async () => {
    setBusy('link');
    try {
      const input =
        mode === 'all'
          ? { projectId: project.id, from: sourceId }
          : mode === 'pick'
            ? { projectId: project.id, from: sourceId, items }
            : {
                projectId: project.id,
                from: sourceId,
                only: { kinds, tags: splitList(tags), people: splitList(people), folders: folderIds },
              };
      if (existing) await call('project.unlink', { projectId: project.id, from: sourceId });
      const out = await call('project.link', input);
      refresh();
      toast({ tone: 'success', title: out.message, body: out.link?.text });
      close(true);
    } catch (e) {
      fail(`Couldn't use ${name}'s things`, e);
    } finally {
      setBusy(null);
    }
  };

  const addCut = async (how: 'nest' | 'copy') => {
    setBusy(how);
    try {
      const out =
        how === 'nest'
          ? await call('cut.nest', { projectId: project.id, from: sourceId, instruction: `Play ${name}'s cut` })
          : await call('cut.copyIn', { projectId: project.id, from: sourceId, instruction: `Copy ${name}'s cut in` });
      refresh();
      const blocking = out.problems.filter((p) => p.severity === 'error');
      if (blocking.length) {
        toast({ tone: 'error', title: 'Not added', body: blocking.map((p) => p.message).join(' ') });
        return;
      }
      toast({
        tone: 'success',
        title: how === 'nest' ? `${name}'s cut plays in ${project.name} now` : `Copied ${name}'s cut into ${project.name}`,
        body: out.summary,
      });
      close(true);
    } catch (e) {
      fail(how === 'nest' ? "Couldn't add its cut" : "Couldn't copy its cut", e);
    } finally {
      setBusy(null);
    }
  };

  if (src.isLoading) return <Skeleton className="h-48" />;
  if (src.error) return <ErrorPanel compact error={src.error} />;

  const toggle = (list: string[], set: (v: string[]) => void, id: string) => set(list.includes(id) ? list.filter((x) => x !== id) : [...list, id]);
  const choice = (m: Mode, title: string, body: string) => (
    <button
      type="button"
      role="radio"
      aria-checked={mode === m}
      onClick={() => setMode(m)}
      className={cx('flex w-full items-start gap-3 rounded-[12px] p-3 text-left transition-colors hairline', mode === m ? 'bg-accent-soft ring-1 ring-accent-line' : 'hover:bg-hover')}
    >
      <span className={cx('mt-0.5 grid h-4 w-4 shrink-0 place-items-center rounded-full ring-1', mode === m ? 'bg-accent ring-accent text-accent-ink' : 'ring-line-strong')} aria-hidden>
        {mode === m ? <Check size={10} weight="bold" /> : null}
      </span>
      <span className="min-w-0">
        <span className="block text-[13.5px] font-medium text-fg">{title}</span>
        <span className="block text-[12.5px] leading-relaxed text-muted">{body}</span>
      </span>
    </button>
  );

  return (
    <div>
      <p className="text-[13px] leading-relaxed text-muted">
        {name} has {plural(own.length, 'clip')}, {plural(resources.length, 'resource')}
        {head ? `, and a cut (version ${head.number})` : ' and no cut yet'}. Nothing is copied: {project.name} uses the same files.
      </p>
      <div role="radiogroup" aria-label="What to use" className="mt-4 space-y-2">
        {choice('all', `Everything from ${name}`, 'Its footage, resources, approved motion graphics and cut. New things it gets appear here by themselves.')}
        {choice('some', 'Only some of it', 'By kind, tag, the people in the footage, or a linked folder.')}
        {choice('pick', 'Pick items', 'Choose specific clips and resources.')}
      </div>
      {mode === 'some' ? (
        <div className="mt-4 space-y-3 rounded-[12px] p-3 hairline">
          <fieldset>
            <legend className="mb-1.5 text-[12.5px] font-medium text-muted">Kinds</legend>
            <div className="flex flex-wrap gap-1.5">
              {KINDS.map((k) => (
                <button
                  key={k.id}
                  type="button"
                  aria-pressed={kinds.includes(k.id)}
                  onClick={() => toggle(kinds, setKinds, k.id)}
                  className={cx('h-8 rounded-full px-3 text-[12.5px] transition-colors hairline', kinds.includes(k.id) ? 'bg-accent-soft text-accent-text' : 'text-muted hover:bg-hover hover:text-fg')}
                >
                  {k.label}
                </button>
              ))}
            </div>
          </fieldset>
          <label className="block">
            <span className="text-[12.5px] font-medium text-muted">Tags (optional, comma separated)</span>
            <input value={tags} onChange={(e) => setTags(e.target.value)} placeholder="logo, product" className="mt-1 h-9 w-full rounded-[10px] bg-panel px-3 text-[13px] text-fg hairline placeholder:text-subtle" />
          </label>
          <label className="block">
            <span className="text-[12.5px] font-medium text-muted">People in the footage (optional)</span>
            <input value={people} onChange={(e) => setPeople(e.target.value)} placeholder="Maren Okafor" className="mt-1 h-9 w-full rounded-[10px] bg-panel px-3 text-[13px] text-fg hairline placeholder:text-subtle" />
          </label>
          {(folders.data?.folders ?? []).length ? (
            <fieldset>
              <legend className="mb-1.5 text-[12.5px] font-medium text-muted">Linked folders</legend>
              <div className="flex flex-wrap gap-1.5">
                {(folders.data?.folders ?? []).map((f) => (
                  <button
                    key={f.id}
                    type="button"
                    aria-pressed={folderIds.includes(f.id)}
                    onClick={() => toggle(folderIds, setFolderIds, f.id)}
                    className={cx('h-8 rounded-full px-3 text-[12.5px] hairline', folderIds.includes(f.id) ? 'bg-accent-soft text-accent-text' : 'text-muted hover:bg-hover')}
                  >
                    {f.name}
                  </button>
                ))}
              </div>
            </fieldset>
          ) : null}
        </div>
      ) : null}
      {mode === 'pick' ? (
        <div className="mt-4 max-h-[34dvh] space-y-0.5 overflow-y-auto rounded-[12px] p-1.5 hairline">
          {own.length + resources.length === 0 ? <p className="px-2 py-3 text-[12.5px] text-muted">{name} has nothing to pick yet.</p> : null}
          {[...own.map((a) => ({ id: a.id, label: a.title, kind: 'Clip' })), ...resources.map((r) => ({ id: r.id, label: r.title, kind: 'Resource' }))].map((it) => (
            <label key={it.id} className="flex cursor-pointer items-center gap-2.5 rounded-[9px] px-2 py-1.5 text-[13px] hover:bg-hover">
              <input type="checkbox" checked={items.includes(it.id)} onChange={() => toggle(items, setItems, it.id)} className="accent-[var(--accent)]" />
              <span className="min-w-0 flex-1 truncate text-fg">{it.label}</span>
              <span className="text-[11.5px] text-subtle">{it.kind}</span>
            </label>
          ))}
        </div>
      ) : null}
      {head ? (
        <section className="mt-5 rounded-[12px] p-3 hairline" aria-label={`${name}'s cut`}>
          <div className="text-[13px] font-medium text-fg">Its cut (version {head.number})</div>
          <p className="mt-0.5 text-[12.5px] leading-relaxed text-muted">Play it inside {project.name}'s cut as one block (it stays at version {head.number} until you update it), or start from a copy you can change.</p>
          <div className="mt-2.5 flex flex-wrap gap-2">
            <Button size="sm" variant="secondary" icon={<StackSimple size={13} />} loading={busy === 'nest'} disabled={!!busy} onClick={() => void addCut('nest')}>
              Play it at the end
            </Button>
            <Button size="sm" variant="ghost" icon={<Copy size={13} />} loading={busy === 'copy'} disabled={!!busy} onClick={() => void addCut('copy')}>
              Copy it in
            </Button>
          </div>
        </section>
      ) : null}
      <div className="mt-5 flex flex-wrap justify-end gap-2 border-t border-line pt-4">
        <Button variant="ghost" onClick={() => close()}>
          Cancel
        </Button>
        <Button variant="primary" icon={<Stack size={15} />} loading={busy === 'link'} disabled={!!busy || (mode === 'pick' && items.length === 0) || (mode === 'some' && kinds.length === 0)} onClick={() => void link()}>
          {existing ? 'Save' : mode === 'all' ? `Use everything from ${name}` : 'Use these'}
        </Button>
      </div>
    </div>
  );
}

/** A collection (D96) as the source: everything on it (the default), or picked clips. */
function UseFromCollectionBody({ project, collectionId, existing, close }: { project: { id: string; name: string }; collectionId: string; existing?: ProjectLinkRow; close: (done?: boolean) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const col = useOp('collection.show', { collection: collectionId });
  const [mode, setMode] = useState<'all' | 'pick'>(existing && !existing.whole ? 'pick' : 'all');
  const [items, setItems] = useState<string[]>(existing?.items?.map((i) => i.id) ?? []);
  const [busy, setBusy] = useState(false);
  if (col.isLoading) return <Skeleton className="h-40" />;
  if (col.error || !col.data) return <ErrorPanel compact error={col.error} />;
  const c = col.data.collection;
  const link = async () => {
    setBusy(true);
    try {
      if (existing) await call('project.unlink', { projectId: project.id, from: c.id });
      const out = await call('project.link', mode === 'all' ? { projectId: project.id, from: c.id } : { projectId: project.id, from: c.id, items });
      for (const k of [...REFRESH, ...LIBRARY_QUERIES]) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: out.message, body: out.link?.text });
      close(true);
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: `Couldn't use ${c.name}`, body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(false);
    }
  };
  const choice = (m: 'all' | 'pick', title: string, body: string) => (
    <button
      type="button"
      role="radio"
      aria-checked={mode === m}
      onClick={() => setMode(m)}
      className={cx('flex w-full items-start gap-3 rounded-[12px] p-3 text-left transition-colors hairline', mode === m ? 'bg-accent-soft ring-1 ring-accent-line' : 'hover:bg-hover')}
    >
      <span className={cx('mt-0.5 grid h-4 w-4 shrink-0 place-items-center rounded-full ring-1', mode === m ? 'bg-accent ring-accent text-accent-ink' : 'ring-line-strong')} aria-hidden>
        {mode === m ? <Check size={10} weight="bold" /> : null}
      </span>
      <span className="min-w-0">
        <span className="block text-[13.5px] font-medium text-fg">{title}</span>
        <span className="block text-[12.5px] leading-relaxed text-muted">{body}</span>
      </span>
    </button>
  );
  return (
    <div>
      <p className="text-[13px] leading-relaxed text-muted">
        The {c.name} collection has {plural(c.count, 'clip')}. Nothing is copied: {project.name} uses the same files.
      </p>
      <div role="radiogroup" aria-label="What to use" className="mt-4 space-y-2">
        {choice('all', `Everything on ${c.name}`, 'Its clips, and clips you put on it later.')}
        {choice('pick', 'Pick clips', 'Only the ones you choose.')}
      </div>
      {mode === 'pick' ? (
        <div className="mt-4 max-h-[34dvh] space-y-0.5 overflow-y-auto rounded-[12px] p-1.5 hairline">
          {col.data.items.length === 0 ? <p className="px-2 py-3 text-[12.5px] text-muted">Nothing is on it yet.</p> : null}
          {col.data.items.map((it) => (
            <label key={it.id} className="flex cursor-pointer items-center gap-2.5 rounded-[9px] px-2 py-1.5 text-[13px] hover:bg-hover">
              <input type="checkbox" checked={items.includes(it.id)} onChange={() => setItems(items.includes(it.id) ? items.filter((x) => x !== it.id) : [...items, it.id])} className="accent-[var(--accent)]" />
              <span className="min-w-0 flex-1 truncate text-fg">{it.title}</span>
            </label>
          ))}
        </div>
      ) : null}
      <div className="mt-5 flex flex-wrap justify-end gap-2 border-t border-line pt-4">
        <Button variant="ghost" onClick={() => close()}>
          Cancel
        </Button>
        <Button variant="primary" icon={<Stack size={15} />} loading={busy} disabled={busy || (mode === 'pick' && items.length === 0)} onClick={() => void link()}>
          {existing ? 'Save' : mode === 'all' ? `Use everything on ${c.name}` : 'Use these'}
        </Button>
      </div>
    </div>
  );
}

/** Collections to choose from in "From another project" (below the projects). */
function CollectionChoices({ used, onPick }: { used: Record<string, string>; onPick: (c: CollectionRow) => void }) {
  const list = useOp('collection.list', {}, { staleTime: 30_000 });
  const rows = list.data?.collections ?? [];
  if (!rows.length) return null;
  return (
    <div className="mt-4 border-t border-line pt-3">
      <div className="mb-1.5 text-[12.5px] font-medium text-muted">Or a collection</div>
      <ul className="max-h-[24dvh] space-y-0.5 overflow-y-auto" aria-label="Collections">
        {rows.map((c) => (
          <li key={c.id}>
            <button
              type="button"
              disabled={!!used[c.id]}
              title={used[c.id]}
              onClick={() => onPick(c)}
              className="flex min-h-11 w-full items-center gap-3 rounded-[10px] px-2 py-1.5 text-left hover:bg-hover disabled:opacity-60"
            >
              <Thumb src={c.coverAssetId ? media.asset(c.coverAssetId, 'thumbnail') : undefined} alt="" className="h-9 w-14 shrink-0 rounded-[7px]" fallback={<BookmarkSimple size={14} />} />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[13.5px] font-medium text-fg">{c.name}</span>
                <span className="block truncate text-[11.5px] text-subtle">{used[c.id] ?? c.text}</span>
              </span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** "From another project": choose the project (or a collection), then what to use from it. */
export function useUseFromProject(project: { id: string; name: string }, links: ProjectLinkRow[] = []) {
  const dialog = useDialog();
  const pickThenUse = async (sourceId?: string) => {
    let id = sourceId;
    if (!id) {
      const used = Object.fromEntries(links.map((l) => [l.sourceId, `${project.name} already uses it: change it from the list`]));
      id = await dialog.open<string>(
        (close) => (
          <div>
            <p className="mb-3 text-[13px] leading-relaxed text-muted">Use another project's footage, logos and pictures, motion graphics and cut in {project.name}, or everything on a collection. Nothing is copied.</p>
            <ProjectPicker label="Which project?" exclude={[project.id]} disabled={used} onPick={(p) => close(p.id)} />
            <CollectionChoices used={used} onPick={(c) => close(c.id)} />
          </div>
        ),
        { title: 'From another project', size: 'sm' },
      );
      if (!id) return;
    }
    const existing = links.find((l) => l.sourceId === id);
    if (id.startsWith('col_')) {
      await dialog.open<boolean>((close) => <UseFromCollectionBody project={project} collectionId={id!} existing={existing} close={close} />, {
        title: existing ? `What ${project.name} uses from ${existing.sourceName}` : 'What to use',
        size: 'md',
      });
      return;
    }
    await dialog.open<boolean>((close) => <UseFromBody project={project} sourceId={id!} existing={existing} close={close} />, {
      title: existing ? `What ${project.name} uses from ${existing.sourceName}` : 'What to use',
      size: 'md',
    });
  };
  return pickThenUse;
}

/** The projects this one uses, with what each brings in; change or stop using them. */
export function LinkedProjects({ data, className }: { data: ProjectShowOut; className?: string }) {
  const qc = useQueryClient();
  const toast = useToast();
  const dialog = useDialog();
  const links = data.links ?? [];
  const project = { id: data.project.id, name: data.project.name };
  const fromProject = useUseFromProject(project, links);
  if (links.length === 0) return null;
  const unlink = async (l: ProjectLinkRow) => {
    const ok = await dialog.confirm({
      title: `Stop using ${l.sourceName}?`,
      body: `Its things stop appearing in ${project.name}. Anything ${project.name}'s cut already uses stays, pinned to the version it uses. Nothing is deleted.`,
      confirmLabel: 'Stop using',
    });
    if (!ok) return;
    try {
      const out = await call('project.unlink', { projectId: project.id, from: l.sourceId });
      for (const k of REFRESH) void qc.invalidateQueries({ queryKey: [k] });
      toast({
        tone: 'success',
        title: out.message,
        body: out.kept?.length ? `Kept what the cut uses: ${out.kept.map((k) => k.name).join(', ')}.` : undefined,
        durationMs: 9000,
      });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: `Couldn't stop using ${l.sourceName}`, body: [err.message, err.hint].filter(Boolean).join(' ') });
    }
  };
  return (
    <section aria-label="Projects this one uses" className={cx('rounded-[14px] p-3 hairline', className)}>
      <div className="mb-2 flex items-center gap-2 px-1">
        <Stack size={15} className="text-accent-text" />
        <h3 className="text-[13px] font-semibold text-fg">Also from other projects and collections</h3>
      </div>
      <ul className="space-y-1">
        {links.map((l) => (
          <li key={l.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-[10px] px-2 py-1.5 hover:bg-hover">
            <Link
              to={l.sourceKind === 'collection' ? libraryLink({ view: 'collections', collection: l.sourceId }) : `/p/${l.sourceId}`}
              className="inline-flex min-w-0 items-center gap-1.5 text-[13px] font-medium text-fg hover:underline"
            >
              {l.sourceKind === 'collection' ? <BookmarkSimple size={13} className="shrink-0 text-subtle" /> : <FilmSlate size={13} className="shrink-0 text-subtle" />}
              <span className="truncate">{l.sourceName}</span>
            </Link>
            <span className="order-last w-full min-w-0 truncate text-[12px] text-muted sm:order-none sm:w-auto sm:flex-1" title={l.text}>
              {l.text}
            </span>
            {l.kept ? <Badge tone="info">kept for the cut</Badge> : null}
            <span className="ml-auto flex items-center gap-1">
              <Button size="sm" variant="ghost" icon={<ArrowRight size={12} />} onClick={() => void fromProject(l.sourceId)}>
                Change
              </Button>
              <Button size="sm" variant="ghost" icon={<LinkBreak size={12} />} onClick={() => void unlink(l)}>
                Stop using
              </Button>
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** "12 here, plus 40 from Brand" for a project's footage. */
export function originSummary(assets: { from?: string }[], noun: string): string {
  const here = assets.filter((a) => !a.from).length;
  const from = new Map<string, number>();
  for (const a of assets) if (a.from) from.set(a.from, (from.get(a.from) ?? 0) + 1);
  let s = plural(here, noun);
  for (const [n, c] of from) s += `, plus ${c} from ${n}`;
  return s;
}

