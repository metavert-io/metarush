import { Link } from 'react-router';
import { DownloadSimple, FilmReel, Play, X } from '@phosphor-icons/react';
import { media } from '../api/client';
import { useInstallationNames, useOpMutation } from '../api/queries';
import type { Job, Render } from '../api/types';
import { absolute, bytes, duration as fmtDuration, presetLabel, stamp } from '../lib/format';
import { useDialog } from '../ui/Dialog';
import { Badge, cx, IconButton, Progress, StateBadge, Theirs, Thumb } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { QAChip, useQAReportDialog } from './QAReport';
import { SendToEditorButton } from './SendToEditor';
import { RenderDownload } from './SignoffReminder';
import { RecoveringVideo } from '../player/Trouble';

const ACTIVE = ['queued', 'rendering', 'uploading'];

export function RenderTile({
  render,
  job,
  projectName,
  highlight,
  showProject,
  branch,
}: {
  render: Render;
  job?: Job;
  projectName?: string;
  highlight?: boolean;
  showProject?: boolean;
  /** The cut its version belongs to, for the sign-off reminder (default main). */
  branch?: string;
}) {
  const dialog = useDialog();
  const toast = useToast();
  const openQA = useQAReportDialog();
  const cancel = useOpMutation('render.cancel', { invalidate: ['render.list', 'project.show', 'job.list'] });
  const machines = useInstallationNames();
  // Names are stored at render time; older renders fall back to today's machine names.
  const renderedBy = render.renderedByName || (render.renderedBy ? machines.get(render.renderedBy) : undefined);
  // Who asked for it (D183): the person's name as it was when they asked.
  const askedBy = render.createdBy?.userName;
  const out = render.output;
  const active = ACTIVE.includes(render.state);
  // D188: a review render is a review copy; the final render keeps its name.
  const name = render.quality === 'final' ? 'Final render' : 'Review copy';
  const vertical = out ? out.height > out.width : render.preset.startsWith('vertical');
  const play = () =>
    void dialog.open<never>(
      () => (
        <div className="-mx-1">
          <RecoveringVideo src={media.render(render.id)} controls autoPlay playsInline className={cx('mx-auto rounded-[12px] bg-black', vertical ? 'max-h-[72dvh]' : 'w-full')} />
          <div className="mt-3 flex flex-wrap items-center gap-2 text-[12.5px] text-muted">
            <span>
              {name} of version {render.revisionNumber}, {presetLabel(render.preset)}, {stamp(render.completedAt ?? render.createdAt)}
            </span>
            {out ? <span className="tc text-subtle">{out.width}×{out.height}, {bytes(out.size)}</span> : null}
            <RenderDownload href={media.render(render.id, true)} render={render} branch={branch} className="ml-auto inline-flex items-center gap-1 font-medium text-accent-text hover:underline">
              <DownloadSimple size={14} /> Download MP4
            </RenderDownload>
          </div>
        </div>
      ),
      { title: `${projectName ? `${projectName}, ` : ''}version ${render.revisionNumber}, ${name.toLowerCase()}`, size: vertical ? 'md' : 'lg' },
    );
  const when = render.completedAt ?? render.createdAt;

  return (
    <div data-render={render.id} data-active={highlight || undefined} className={cx('group flex gap-3 rounded-[14px] p-2 transition-colors', highlight ? 'bg-accent-soft ring-1 ring-accent-line' : 'hover:bg-hover')}>
      <button type="button" disabled={!out} onClick={play} className="relative shrink-0 overflow-hidden rounded-[10px] disabled:cursor-default" aria-label={out ? `Play the ${name.toLowerCase()} of version ${render.revisionNumber}` : undefined}>
        <Thumb src={out?.thumbnailKey ? media.renderThumb(render.id) : undefined} alt="" className={cx('rounded-[10px]', vertical ? 'h-[84px] w-[48px]' : 'h-[54px] w-[96px]')} fallback={<FilmReel size={16} />} />
        {out ? (
          <span className="absolute inset-0 grid place-items-center bg-black/0 opacity-0 transition-all group-hover:bg-black/35 group-hover:opacity-100">
            <Play size={16} weight="fill" className="text-white" />
          </span>
        ) : null}
      </button>
      <div className="min-w-0 flex-1 py-0.5">
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="tc text-[13px] font-semibold text-fg">r{render.revisionNumber}</span>
          <Badge tone={render.quality === 'final' ? 'accent' : 'neutral'}>{render.quality === 'final' ? 'final' : 'review copy'}</Badge>
          <StateBadge state={render.state} />
          {render.qa ? <QAChip verdict={render.qa.verdict} errors={render.qa.errors} warnings={render.qa.warnings} onOpen={() => void openQA(render.qa!.reportId)} /> : null}
          {showProject && projectName ? (
            <Link to={`/p/${render.projectId}`} className="truncate text-[12px] text-muted hover:text-fg">
              {projectName}
            </Link>
          ) : null}
        </div>
        <div className="mt-1 text-[12px] text-muted" title={absolute(when)}>
          <span data-stamp>
            {stamp(when)}
          </span>
          {render.follow && render.state === 'canceled' ? <span className="text-subtle"> · not made: a newer version came first</span> : null}
        </div>
        <div className="mt-0.5 truncate text-[11.5px] text-subtle">
          {presetLabel(render.preset)}
          {out ? `, ${fmtDuration(out.duration)}, ${bytes(out.size)}` : ''}
          {render.follow ? (
            <span data-render-follow>, made by itself for the latest version</span>
          ) : askedBy ? (
            <>
              , asked for by{' '}
              <span data-render-asked-by>
                <Theirs>{askedBy}</Theirs>
              </span>
            </>
          ) : null}
          {renderedBy ? `, on ${renderedBy}` : ''}
        </div>
        {active ? (
          <div className="mt-2">
            <Progress value={job?.progress.pct} indeterminate={!job || (render.state === 'queued' && !job.progress.pct)} label={`Render progress for version ${render.revisionNumber}`} />
            <div className="mt-1 truncate text-[11px] text-subtle">{job?.progress.message || (render.state === 'queued' ? 'Waiting for a machine running MetaRush' : render.state === 'uploading' ? 'Saving the finished video' : 'Rendering')}</div>
          </div>
        ) : null}
        {render.error ? <div className="clamp-2 mt-1 text-[12px] text-danger">{render.error}</div> : null}
      </div>
      <div className="flex shrink-0 items-start gap-1">
        {out ? <SendToEditorButton compact revisionId={render.revisionId} label={`version ${render.revisionNumber}`} /> : null}
        {out ? (
          <RenderDownload
            href={media.render(render.id, true)}
            render={render}
            branch={branch}
            aria-label={`Download the ${name.toLowerCase()} of version ${render.revisionNumber}`}
            title="Download MP4"
            className="inline-flex h-8 w-8 items-center justify-center rounded-[9px] text-muted hover:bg-panel hover:text-fg"
          >
            <DownloadSimple size={16} />
          </RenderDownload>
        ) : null}
        {active ? (
          <IconButton
            size="sm"
            label={`Stop the ${name.toLowerCase()}`}
            onClick={async () => {
              const ok = await dialog.confirm({ title: `Stop the ${name.toLowerCase()} of version ${render.revisionNumber}?`, body: 'MetaRush stops it in a moment. You can start it again later.', confirmLabel: 'Stop it', cancelLabel: 'Keep going', tone: 'danger' });
              if (!ok) return;
              cancel.mutate({ id: render.id }, { onSuccess: () => toast({ title: 'Render canceled' }), onError: (e) => toast({ tone: 'error', title: 'Could not cancel', body: e.message }) });
            }}
          >
            <X size={14} />
          </IconButton>
        ) : null}
      </div>
    </div>
  );
}

