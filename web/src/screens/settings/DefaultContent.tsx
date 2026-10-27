import { Link, Navigate, useLocation, useSearchParams } from 'react-router';
import { Sliders } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import { ErrorPanel, Skeleton } from '../../ui/primitives';

/*
 * /settings/brand and /settings/content (D193): the brand and the spelling
 * list are Content Settings now, in each project's Settings tab. Old links
 * (Settings → Brand, Settings → Footage → Spelling, /brand) open the default's
 * — the project whose settings new projects use — or, when no project's are
 * the default yet, a short page to choose a project.
 */
export function DefaultContentPage() {
  const loc = useLocation();
  const [params] = useSearchParams();
  const part = params.get('part') ?? (loc.pathname.endsWith('/brand') ? 'brand' : '');
  const q = useOp('content.show', {});
  const projects = useOp('project.list', {});
  if (q.isLoading) return <Skeleton className="mx-auto h-40 max-w-[640px]" />;
  if (q.error) return <ErrorPanel error={q.error} onRetry={() => void q.refetch()} />;
  const def = q.data?.default ?? q.data?.from;
  if (def) return <Navigate to={`/p/${def.id}?tab=settings${part ? `&part=${encodeURIComponent(part)}` : ''}`} replace />;
  const list = (projects.data?.projects ?? []).filter((p) => !p.archived);
  return (
    <div className="mx-auto max-w-[640px] py-6" data-choose-settings>
      <h1 className="flex items-center gap-2 text-[20px] font-semibold tracking-[-0.02em] text-fg">
        <Sliders size={18} className="text-subtle" aria-hidden /> Content Settings
      </h1>
      <p className="mt-2 text-[13.5px] leading-relaxed text-muted">
        The brand, spelling list, narration voice and house rules are in each project's Settings tab. No project's settings are used for new projects yet: open a project and tick
        “Use for new projects”.
      </p>
      {projects.isLoading ? (
        <Skeleton className="mt-4 h-24" />
      ) : list.length ? (
        <ul className="mt-4 divide-y divide-line overflow-hidden rounded-[12px] hairline">
          {list.map((p) => (
            <li key={p.id}>
              <Link to={`/p/${p.id}?tab=settings${part ? `&part=${encodeURIComponent(part)}` : ''}`} className="block px-4 py-3 text-[13.5px] text-fg hover:bg-hover">
                {p.name}
              </Link>
            </li>
          ))}
        </ul>
      ) : (
        <p className="mt-4 text-[13px] text-subtle">Create a project first: its settings are used for new projects.</p>
      )}
    </div>
  );
}
