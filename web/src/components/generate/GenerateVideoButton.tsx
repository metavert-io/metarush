import { useNavigate, useSearchParams } from 'react-router';
import { FolderSimplePlus, Sparkle } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import type { ProjectSummary } from '../../api/types';
import { useDialog } from '../../ui/Dialog';
import { Button, Skeleton } from '../../ui/primitives';
import { useCreateProject } from '../Library';
import { ProjectPicker } from '../ProjectPicker';

/*
 * "Generate video" in the drop box (D154), under Choose files: in a project's
 * Sources it opens the Generate panel there; on Home it first asks which
 * project the shot is for (every generated shot belongs to a project, which
 * pays for it from its budget), the one used last already chosen.
 */

export function GenerateVideoButton({ target, size }: { target: { projectId?: string; label: string }; size: 'md' | 'lg' }) {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const dialog = useDialog();
  const create = useCreateProject();

  const open = async () => {
    if (target.projectId) {
      const next = new URLSearchParams(params);
      next.set('tab', 'sources');
      next.set('generate', '1');
      setParams(next);
      return;
    }
    const picked = await dialog.open<{ id: string }>(
      (close) => (
        <WhichProject
          onPick={(id) => close({ id })}
          onNew={async () => {
            const id = await create();
            if (id) close({ id });
          }}
          onCancel={() => close()}
        />
      ),
      { title: 'Generate a video for which project?', size: 'sm' },
    );
    if (picked?.id) navigate(`/p/${picked.id}?tab=sources&generate=1`);
  };

  return (
    <Button size={size} variant="secondary" icon={<Sparkle size={size === 'lg' ? 16 : 14} weight="fill" />} onClick={() => void open()} data-generate-video title="Describe a shot and MetaRush makes it with AI; you see the price first">
      Generate video
    </Button>
  );
}

/** The project the shot is for: the one of the last generated shot is offered first, else the newest project. */
function WhichProject({ onPick, onNew, onCancel }: { onPick: (id: string) => void; onNew: () => void; onCancel: () => void }) {
  const last = useOp('generate.list', { limit: 1 }, { staleTime: 60_000 });
  const projects = useOp('project.list', {}, { staleTime: 30_000 });
  const live = (projects.data?.projects ?? []).filter((p) => !p.archived);
  const lastId = last.data?.generations[0]?.projectId;
  const usual: ProjectSummary | undefined = live.find((p) => p.id === lastId) ?? [...live].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))[0];
  const loading = last.isLoading || projects.isLoading;
  return (
    <div className="space-y-4" data-which-project>
      <p className="text-[13px] leading-relaxed text-muted">The shot is added to that project&apos;s Sources and paid from its budget for generated video. You see the price before anything is sent.</p>
      {loading ? (
        <Skeleton className="h-10" />
      ) : usual ? (
        <Button variant="primary" className="w-full" icon={<Sparkle size={14} weight="fill" />} onClick={() => onPick(usual.id)} data-autofocus data-usual-project>
          {`For “${usual.name}”`}
        </Button>
      ) : null}
      <div>
        <div className="mb-1.5 text-[12.5px] font-medium text-muted">{usual ? 'Or another project' : 'Choose a project'}</div>
        <ProjectPicker label="Projects" autoFocus={!usual && !loading} onPick={(p) => onPick(p.id)} />
      </div>
      <div className="flex items-center justify-between gap-2 border-t border-line pt-4">
        <Button variant="quiet" icon={<FolderSimplePlus size={15} />} onClick={onNew}>
          New project
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  );
}
