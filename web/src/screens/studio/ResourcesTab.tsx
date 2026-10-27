import { useEffect, useMemo, useState } from 'react';
import { MagnifyingGlass } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import type { ProjectShowOut, ResourceRow } from '../../api/types';
import { AddBox } from '../../components/AddBox';
import { LinkedProjects, useUseFromProject } from '../../components/ProjectLinks';
import { LinkedFolders, ResourceGrid, useResourceDetails } from '../../components/Resources';
import { cx, ErrorPanel, Skeleton } from '../../ui/primitives';

type KindFilter = 'all' | 'image' | 'svg' | 'pdf' | 'font' | 'repo' | 'website';
type OriginFilter = 'all' | 'project' | 'linked' | 'workspace';

const KINDS: [KindFilter, string][] = [
  ['all', 'All'],
  ['image', 'Pictures'],
  ['svg', 'SVG'],
  ['pdf', 'PDF'],
  ['font', 'Fonts'],
  ['website', 'Web pages'],
  ['repo', 'Code'],
];

/**
 * A project's resources (D79–D83, D104–D106): its own logos, pictures, PDFs
 * and fonts, the GitHub repositories and web pages it keeps (and the code
 * slides, diagrams and cards made from them), what the projects it uses bring
 * in ("from Brand"), and the workspace library, with the Add box, linked
 * folders and project links.
 */
export function ResourcesTab({ data, resourceId, onSelect }: { data: ProjectShowOut; resourceId?: string; onSelect: (id: string | undefined) => void }) {
  const projectId = data.project.id;
  const list = useOp('resources.list', { projectId });
  const [q, setQ] = useState('');
  const [kind, setKind] = useState<KindFilter>('all');
  const [origin, setOrigin] = useState<OriginFilter>('all');
  const details = useResourceDetails(projectId);
  const project = { id: projectId, name: data.project.name };
  const fromProject = useUseFromProject(project, data.links ?? []);
  const rows = list.data?.resources ?? [];

  const shown = useMemo(() => {
    const words = q.trim().toLowerCase().split(/\s+/).filter(Boolean);
    return rows.filter((r) => {
      if (kind !== 'all' && r.kind !== kind) return false;
      if (origin !== 'all' && r.origin !== origin) return false;
      if (!words.length) return true;
      const hay = [r.title, r.name, ...(r.tags ?? []), ...(r.autoTags ?? []).map((t) => t.tag), r.description ?? '', r.provenance ?? '', r.site?.url ?? ''].join(' ').toLowerCase();
      return words.every((w) => hay.includes(w));
    });
  }, [rows, q, kind, origin]);

  // Deep link (?resource=…): open its details once.
  useEffect(() => {
    if (!resourceId) return;
    const r = rows.find((x) => x.id === resourceId);
    if (!r) return;
    onSelect(undefined);
    void details(r);
  }, [resourceId, rows]); // eslint-disable-line react-hooks/exhaustive-deps

  if (list.isLoading) return <Skeleton className="h-64" />;
  if (list.error) return <ErrorPanel error={list.error} onRetry={() => void list.refetch()} />;

  const open = (r: ResourceRow) => void details(r);
  const target = { projectId, label: data.project.name };

  if (rows.length === 0)
    return (
      <div className="space-y-4">
        <AddBox target={target} variant="empty" focus="resources" onFromProject={() => void fromProject()} />
        <LinkedProjects data={data} />
        <LinkedFolders projectId={projectId} />
      </div>
    );

  const chip = (on: boolean) => cx('h-8 rounded-full px-3 text-[12.5px] transition-colors hairline', on ? 'bg-panel text-fg' : 'text-muted hover:text-fg');
  return (
    <div className="space-y-5">
      <AddBox target={target} variant="compact" focus="resources" onFromProject={() => void fromProject()} />
      <LinkedProjects data={data} />
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-[200px] flex-1 sm:max-w-[320px]">
          <MagnifyingGlass size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-subtle" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Find by name, tag or description"
            aria-label="Find resources"
            className="h-9 w-full rounded-[10px] bg-panel pl-8 pr-3 text-[13px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
          />
        </div>
        <div className="flex flex-wrap gap-1" role="group" aria-label="Kind">
          {KINDS.map(([k, label]) => (
            <button key={k} type="button" aria-pressed={kind === k} onClick={() => setKind(k)} className={chip(kind === k)}>
              {label}
            </button>
          ))}
        </div>
        <div className="flex flex-wrap gap-1" role="group" aria-label="Where from">
          {(
            [
              ['all', 'Everywhere'],
              ['project', 'This project'],
              ['linked', 'Other projects'],
              ['workspace', 'Workspace'],
            ] as [OriginFilter, string][]
          ).map(([o, label]) => (
            <button key={o} type="button" aria-pressed={origin === o} onClick={() => setOrigin(o)} className={chip(origin === o)}>
              {label}
            </button>
          ))}
        </div>
      </div>
      <ResourceGrid rows={shown} onOpen={open} empty="No resources match." />
      <p className="px-1 text-[11.5px] text-subtle">{list.data?.summary}</p>
      <LinkedFolders projectId={projectId} />
    </div>
  );
}
