import { useEffect, useRef } from 'react';
import { FilmReel } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import type { ProjectShowOut } from '../../api/types';
import { RenderTile } from '../../components/Renders';
import { RenderButtons } from '../../components/Review';
import { SendToEditorButton } from '../../components/SendToEditor';
import { useQAReportDialog } from '../../components/QAReport';
import { EmptyState, Freshness, SectionTitle } from '../../ui/primitives';

export function RendersTab({ data, highlight, qaReport }: { data: ProjectShowOut; highlight?: string; qaReport?: string }) {
  const jobs = useOp('job.list', { projectId: data.project.id, type: 'render', limit: 50 });
  const byJob = new Map((jobs.data?.jobs ?? []).map((j) => [j.id, j]));
  const head = data.revisions.find((r) => r.id === data.headRevisionId);
  const openQA = useQAReportDialog();
  const opened = useRef<string | null>(null);

  // Deep links: ?render=<id> highlights it, &qa=<report> opens its quality check.
  useEffect(() => {
    if (!highlight) return;
    document.querySelector(`[data-render="${highlight}"]`)?.scrollIntoView({ block: 'center', behavior: 'smooth' });
  }, [highlight, data.renders]);
  useEffect(() => {
    if (!qaReport || opened.current === qaReport) return;
    opened.current = qaReport;
    void openQA(qaReport);
  }, [qaReport, openQA]);

  return (
    <section aria-labelledby="renders-h">
      <SectionTitle
        id="renders-h"
        meta={head ? `Latest version is ${head.number}` : undefined}
        action={
          <>
            <Freshness at={jobs.dataUpdatedAt || undefined} fetching={jobs.isFetching} />
            <SendToEditorButton revisionId={data.headRevisionId} label={head ? `version ${head.number}` : 'this cut'} />
            <RenderButtons revisionId={data.headRevisionId} size="sm" brief={data.project.brief} output={head?.output} />
          </>
        }
      >
        Renders
      </SectionTitle>
      {data.renders.length === 0 ? (
        <div className="panel">
          <EmptyState
            icon={<FilmReel size={22} />}
            title="No renders yet"
            body="A review copy is quick and half size, for checking the cut; MetaRush makes one by itself a minute after the latest change. A final render uses your original footage at full quality. Either runs on any of your machines that can render."
          />
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-1 lg:grid-cols-2">
          {data.renders.map((r) => (
            <RenderTile
              key={r.id}
              render={r}
              job={byJob.get(r.jobId)}
              projectName={data.project.name}
              highlight={r.id === highlight}
              branch={data.revisions.find((v) => v.id === r.revisionId)?.branch}
            />
          ))}
        </div>
      )}
    </section>
  );
}
