import { Link } from 'react-router';
import { Archive, ArrowCounterClockwise, CaretRight, FilmSlate, Trash } from '@phosphor-icons/react';
import { media } from '../api/client';
import { useOp } from '../api/queries';
import { absolute, plural } from '../lib/format';
import { deletedWords, useProjectActions } from '../components/project/ProjectDelete';
import { OwnersNote, ownerTitle, useOwnership } from '../components/OwnerOnly';
import { Button, EmptyState, ErrorPanel, Freshness, Skeleton, Theirs, Thumb } from '../ui/primitives';

/*
 * Archived projects (D196): the projects people deleted, most recently first.
 * Anyone restores one as it was; an owner deletes one for good, or clears
 * them all out, after a warning that lists what goes. Their footage stays in
 * the Library meanwhile, and nothing here is in Home, the lists or search.
 */
export function ArchivedProjectsPage() {
  const list = useOp('project.list', { archivedOnly: true });
  const { restore, purge } = useProjectActions();
  const own = useOwnership();
  const projects = list.data?.projects ?? [];
  return (
    <div className="space-y-6" data-archived-projects>
      <nav aria-label="Breadcrumb" className="flex items-center gap-1.5 text-[12.5px] text-subtle">
        <Link to="/" className="hover:text-fg">
          Studio
        </Link>
        <CaretRight size={11} />
        <span className="text-muted">Archived projects</span>
        <Freshness className="ml-auto" at={list.dataUpdatedAt || undefined} fetching={list.isFetching} onRefresh={() => void list.refetch()} />
      </nav>
      <div className="flex flex-wrap items-end gap-x-6 gap-y-3">
        <div className="min-w-0 flex-1 max-sm:basis-full">
          <h1 className="text-[26px] font-semibold tracking-[-0.03em] text-fg sm:text-[28px]">Archived projects</h1>
          <p className="mt-1 max-w-[70ch] text-[13.5px] leading-relaxed text-muted">
            Projects people deleted wait here. Restore one to bring it back as it was, or delete it for good. Their footage stays in the Library either way.
          </p>
        </div>
        {projects.length > 0 ? (
          <Button variant="danger" size="sm" icon={<Trash size={13} />} disabled={!own.owner} title={ownerTitle(own, 'clear out archived projects')} onClick={() => void purge('all')}>
            Clear out archived projects…
          </Button>
        ) : null}
      </div>
      {projects.length > 0 ? <OwnersNote /> : null}
      {list.isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-20 rounded-[14px]" />
          <Skeleton className="h-20 rounded-[14px]" />
        </div>
      ) : list.error ? (
        <ErrorPanel error={list.error} onRetry={() => void list.refetch()} />
      ) : projects.length === 0 ? (
        <div className="panel">
          <EmptyState
            icon={<Archive size={22} />}
            title="No archived projects"
            body="Projects you delete wait here until someone restores them or an owner clears them out for good."
            action={
              <Link to="/" className="text-[13px] text-accent-text hover:underline">
                Back to the studio
              </Link>
            }
          />
        </div>
      ) : (
        <ul className="divide-y divide-line overflow-hidden rounded-[16px] hairline" aria-label="Archived projects">
          {projects.map((p) => (
            <li key={p.id} className="flex flex-wrap items-center gap-3 px-3 py-3 sm:flex-nowrap" data-archived-row={p.name}>
              <Thumb src={p.thumbAssetId ? media.asset(p.thumbAssetId, 'thumbnail') : undefined} alt="" className="h-14 w-[88px] shrink-0 rounded-[10px]" fallback={<FilmSlate size={16} />} />
              <div className="min-w-0 flex-1 basis-[12rem]">
                <Link to={`/p/${p.id}`} className="block truncate text-[14.5px] font-semibold tracking-[-0.01em] text-fg hover:underline">
                  <Theirs>{p.name}</Theirs>
                </Link>
                <div className="mt-0.5 truncate text-[12px] text-muted" title={p.archivedAt ? absolute(p.archivedAt) : undefined}>
                  {deletedWords(p)} · {plural(p.assetCount, 'source')} · {plural(p.revisionSeq, 'version')}
                </div>
                {p.purging ? <div className="mt-0.5 text-[12px] text-danger">Clearing out started: Delete for good finishes it.</div> : null}
              </div>
              <div className="flex shrink-0 items-center gap-2 max-sm:w-full max-sm:justify-end">
                <Button size="sm" variant="secondary" icon={<ArrowCounterClockwise size={13} />} disabled={!!p.purging} onClick={() => void restore(p)} aria-label={`Restore ${p.name}`}>
                  Restore
                </Button>
                <Button size="sm" variant="ghost" icon={<Trash size={13} />} disabled={!own.owner} title={ownerTitle(own, 'delete projects for good')} onClick={() => void purge(p)} aria-label={`Delete ${p.name} for good`}>
                  <span className="max-sm:hidden">Delete for good…</span>
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
