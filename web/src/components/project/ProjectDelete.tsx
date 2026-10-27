import { Link, useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { Archive, ArrowCounterClockwise, CaretRight, Trash, WarningCircle } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import type { Project, ProjectPurgeOut, ProjectPurgeRow } from '../../api/types';
import { absolute, capFirst, stamp } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { Button, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { ownerTitle, useOwnership } from '../OwnerOnly';

/*
 * Deleting a project archives it (D196): it leaves Home, the lists and search
 * and waits in Archived projects, where anyone can restore it. An owner clears
 * archived projects out for good, after a styled warning that lists what goes
 * (and what stays: footage is never deleted here). The same ops as the CLI
 * (project.delete, project.restore, project.purge).
 */

/** The dashboard's page of archived projects. */
export const ARCHIVED_PROJECTS = '/projects/archived';

/** Queries that show projects, refreshed after one is deleted, restored or cleared out. */
const PROJECT_QUERIES = ['project.list', 'project.show', 'search', 'library.list', 'review.waiting', 'asset.list', 'todo.list', 'assistant.list', 'assistant.get'];

type Ref = { id: string; name: string };

export function useProjectActions() {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const refresh = () => {
    for (const k of PROJECT_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
    void qc.invalidateQueries({ queryKey: ['assistant'] });
  };
  const fail = (title: string, e: unknown) => {
    const err = toApiError(e);
    toast({ tone: 'error', title, body: [err.message, err.hint].filter(Boolean).join(' ') });
  };

  const restore = async (p: Ref): Promise<boolean> => {
    try {
      const out = await call('project.restore', { id: p.id });
      refresh();
      toast({ tone: 'success', title: out.message, action: { label: 'Open it', onClick: () => navigate(`/p/${p.id}`) } });
      return true;
    } catch (e) {
      fail(`Didn't restore “${p.name}”`, e);
      return false;
    }
  };

  /** Deletes (archives) a project after a styled confirm; true when it did. */
  const deleteProject = async (p: Ref): Promise<boolean> => {
    const ok = await dialog.confirm({
      title: `Delete “${p.name}”?`,
      body: 'It moves to Archived projects; you can restore it. Its footage stays in the Library.',
      confirmLabel: 'Delete project',
      tone: 'danger',
    });
    if (!ok) return false;
    try {
      const out = await call('project.delete', { id: p.id });
      refresh();
      toast({
        tone: 'success',
        title: `Deleted “${p.name}”`,
        body: 'It is in Archived projects, where you can restore it.',
        action: { label: 'Undo', onClick: () => void restore(p) },
        actions: [{ label: 'Archived projects', onClick: () => navigate(ARCHIVED_PROJECTS) }],
        durationMs: 9000,
      });
      return !!out;
    } catch (e) {
      fail(`Didn't delete “${p.name}”`, e);
      return false;
    }
  };

  /** Clears an archived project (or all of them) out for good, after the warning that lists what goes. */
  const purge = async (target: Ref | 'all'): Promise<boolean> => {
    const input = target === 'all' ? { allArchived: true } : { id: target.id };
    let check: ProjectPurgeOut;
    try {
      check = await call('project.purge', { ...input, dryRun: true });
    } catch (e) {
      fail(target === 'all' ? "Can't clear out the archived projects" : `Can't delete “${target.name}” for good`, e);
      return false;
    }
    const ready = check.projects.filter((r) => r.status !== 'refused');
    if (ready.length === 0) {
      await dialog.open<void>((close) => <NothingToClear check={check} close={() => close()} />, { title: check.question, size: 'md' });
      return false;
    }
    const ok = await dialog.open<boolean>((close) => <PurgeConfirm check={check} close={close} />, { title: check.question, size: 'md' });
    if (!ok) return false;
    try {
      const out = await call('project.purge', { ...input, forever: true });
      refresh();
      toast({ tone: 'success', title: out.message, durationMs: 9000 });
      return true;
    } catch (e) {
      refresh();
      fail('Clearing out stopped', e);
      return false;
    }
  };

  return { deleteProject, restore, purge };
}

/** What goes and moves for one project, in a list. */
function PurgeRows({ rows }: { rows: ProjectPurgeRow[] }) {
  return (
    <ul className="mt-4 max-h-[44dvh] divide-y divide-line overflow-y-auto rounded-[12px] text-[13px] hairline" aria-label="What goes">
      {rows.map((r) => (
        <li key={r.projectId} className="px-3 py-2.5" data-purge-row={r.name}>
          <div className="font-medium text-fg">
            <Theirs>{r.name}</Theirs>
          </div>
          <div className="mt-0.5 text-[12.5px] leading-relaxed text-muted" data-goes>
            {r.goes.length ? `${capFirst(r.goes.join(', '))}.` : 'Just the project: nothing else is in it.'}
          </div>
          {r.moves.map((m) => (
            <div key={m} className="mt-0.5 text-[12.5px] leading-relaxed text-subtle">
              {capFirst(m)}.
            </div>
          ))}
        </li>
      ))}
    </ul>
  );
}

/** The styled warning before clearing out for good: what goes, what moves and stays, Cancel focused. */
function PurgeConfirm({ check, close }: { check: ProjectPurgeOut; close: (ok?: boolean) => void }) {
  const ready = check.projects.filter((r) => r.status !== 'refused');
  const refused = check.projects.filter((r) => r.status === 'refused');
  return (
    <div data-purge-confirm>
      <div className="flex items-start gap-3">
        <WarningCircle size={22} weight="fill" className="mt-0.5 shrink-0 text-danger" />
        <div className="min-w-0 text-[13.5px] leading-relaxed">
          <p className="text-fg">{ready.length === 1 ? 'Everything that is the project’s goes, for every machine.' : 'Everything that is these projects’ goes, for every machine.'}</p>
          <p className="mt-1 font-medium text-danger">This can't be undone.</p>
        </div>
      </div>
      <PurgeRows rows={ready} />
      {refused.length ? (
        <div className="mt-4 rounded-[12px] bg-hover/60 p-3">
          <div className="text-[12.5px] font-medium text-muted">{refused.length === 1 ? 'Stays for now' : `${refused.length} stay for now`}</div>
          <ul className="mt-1.5 space-y-1.5">
            {refused.map((r) => (
              <li key={r.projectId} className="text-[12.5px] leading-relaxed">
                <span className="font-medium text-fg">
                  “<Theirs>{r.name}</Theirs>”
                </span>{' '}
                <span className="text-muted">{r.blockers[0]}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <p className="mt-4 text-[12.5px] leading-relaxed text-subtle">Footage is never deleted here. Spending, the audit trail and the activity log stay.</p>
      <div className="mt-5 flex flex-wrap justify-end gap-2 border-t border-line pt-4">
        <Button variant="ghost" data-autofocus onClick={() => close()}>
          Cancel
        </Button>
        <Button variant="danger" icon={<Trash size={14} />} onClick={() => close(true)}>
          Delete for good
        </Button>
      </div>
    </div>
  );
}

/** Nothing can be cleared out yet: why, for each. */
function NothingToClear({ check, close }: { check: ProjectPurgeOut; close: () => void }) {
  return (
    <div>
      {check.projects.length ? (
        <ul className="space-y-2 text-[13px] leading-relaxed">
          {check.projects.map((r) => (
            <li key={r.projectId}>
              <span className="font-medium text-fg">
                “<Theirs>{r.name}</Theirs>”
              </span>{' '}
              <span className="text-muted">{r.blockers[0] ?? ''}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-[13.5px] text-muted">{check.message}</p>
      )}
      <div className="mt-5 flex justify-end border-t border-line pt-4">
        <Button variant="primary" onClick={close}>
          OK
        </Button>
      </div>
    </div>
  );
}

/** Who deleted it and when, in words ("Deleted yesterday 4:12 PM by Theo"). */
export function deletedWords(p: Pick<Project, 'archivedAt' | 'archivedBy' | 'updatedAt'>): string {
  const when = p.archivedAt ? stamp(p.archivedAt) : '';
  const by = p.archivedBy?.userName;
  return ['Deleted', when, by ? `by ${by}` : ''].filter(Boolean).join(' ');
}

/**
 * A link or a notification that leads to an archived project lands here: it
 * says the project is archived, with Restore (and Delete for good for owners).
 */
export function ArchivedProject({ project }: { project: Project }) {
  const { restore, purge } = useProjectActions();
  const own = useOwnership();
  const navigate = useNavigate();
  const purging = !!project.purging;
  return (
    <div className="mx-auto max-w-[680px] py-6" data-archived-project>
      <nav aria-label="Breadcrumb" className="mb-4 flex items-center gap-1.5 text-[12.5px] text-subtle">
        <Link to="/" className="hover:text-fg">
          Studio
        </Link>
        <CaretRight size={11} />
        <Link to={ARCHIVED_PROJECTS} className="hover:text-fg">
          Archived projects
        </Link>
        <CaretRight size={11} />
        <span className="truncate text-muted">
          <Theirs>{project.name}</Theirs>
        </span>
      </nav>
      <section className="panel p-6" aria-labelledby="archived-h">
        <div className="flex items-start gap-3">
          <Archive size={22} className="mt-1 shrink-0 text-subtle" aria-hidden />
          <div className="min-w-0 flex-1">
            <h1 id="archived-h" className="text-[22px] font-semibold leading-tight tracking-[-0.02em] text-fg">
              “<Theirs>{project.name}</Theirs>” is archived
            </h1>
            <p className="mt-2 text-[13.5px] leading-relaxed text-muted" title={project.archivedAt ? absolute(project.archivedAt) : undefined}>
              {deletedWords(project)}. It is in Archived projects: restore it to work on it again. Its footage is in the Library, and its conversations with Claude Code can still be read.
            </p>
            {purging ? (
              <p className="mt-2 rounded-[10px] bg-danger-soft px-3 py-2 text-[12.5px] text-danger">
                An owner started clearing it out for good, so it can't be restored. Delete for good finishes it.
              </p>
            ) : null}
          </div>
        </div>
        <div className="mt-5 flex flex-wrap items-center gap-2 border-t border-line pt-4">
          <Button variant="primary" icon={<ArrowCounterClockwise size={15} />} disabled={purging} onClick={() => void restore(project)}>
            Restore
          </Button>
          <Button variant="ghost" onClick={() => navigate(ARCHIVED_PROJECTS)}>
            Archived projects
          </Button>
          <span className="flex-1" />
          <Button
            variant="danger"
            icon={<Trash size={14} />}
            disabled={!own.owner}
            title={ownerTitle(own, 'delete projects for good')}
            onClick={async () => {
              if (await purge(project)) navigate(ARCHIVED_PROJECTS);
            }}
          >
            Delete for good…
          </Button>
        </div>
      </section>
    </div>
  );
}
