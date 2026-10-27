import { useEffect, useRef, useState, useSyncExternalStore } from 'react';
import { Link } from 'react-router';
import { DownloadSimple, FilmSlate, FolderSimplePlus, Plus } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call, media, toApiError } from '../api/client';
import { useChooseProject } from './ProjectPicker';
import { useOp, useOpMutation } from '../api/queries';
import type { AssetSummary, Brief, ProjectSummary } from '../api/types';
import { draggedAssetIds, isAssetDrag, useAssetDrag } from '../lib/dragAssets';
import { plural, relative } from '../lib/format';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, cx, Skeleton, Spinner, Theirs, Thumb } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { TodoBadge } from './TodoBadge';

/* ---------- Processing state for a source (filters) ---------- */

type Proc = { key: string; label: string; state: 'ok' | 'pending' | 'failed' | 'na' };

export function processing(a: AssetSummary): Proc[] {
  const out: Proc[] = [];
  const failed = a.state === 'failed';
  if (a.kind === 'video') out.push({ key: 'proxy', label: 'Preview', state: a.variants.includes('proxy') ? 'ok' : failed ? 'failed' : 'pending' });
  if (a.hasAudio) {
    const t = a.transcript?.state;
    out.push({ key: 'transcript', label: 'Transcript', state: t === 'available' ? 'ok' : t === 'failed' ? 'failed' : 'pending' });
  }
  const ev = a.analysis?.evidence;
  out.push({ key: 'evidence', label: 'Analysis', state: ev === 'available' ? 'ok' : ev === 'failed' ? 'failed' : 'pending' });
  return out;
}

/* ---------- Adding clips to projects (drag and drop, picker), with Undo ---------- */

/**
 * Links clips to a project, then says so in a toast with Undo (unlink) and
 * Move… (to another project, when the drop landed on the wrong one).
 */
export function useAddToProject() {
  const qc = useQueryClient();
  const toast = useToast();
  const choose = useChooseProject();
  const refresh = () => {
    for (const k of ['asset.list', 'project.show', 'project.list', 'activity.summary', 'asset.where']) void qc.invalidateQueries({ queryKey: [k] });
  };
  return async (assetIds: string[], project: { id: string; name: string }, names?: Map<string, string>) => {
    const added: string[] = [];
    let failed = 0;
    for (const id of assetIds) {
      try {
        await call('asset.link', { assetId: id, projectId: project.id });
        added.push(id);
      } catch {
        failed++;
      }
    }
    for (const k of ['asset.list', 'project.show', 'project.list', 'activity.summary']) void qc.invalidateQueries({ queryKey: [k] });
    const what = assetIds.length === 1 ? `“${names?.get(assetIds[0]!) ?? 'the clip'}”` : plural(assetIds.length, 'clip');
    if (failed) {
      toast({ tone: 'error', title: `Could not add ${plural(failed, 'clip')} to ${project.name}` });
      if (!added.length) return;
    }
    toast({
      tone: 'success',
      title: `Added ${what} to ${project.name}`,
      action: {
        label: 'Undo',
        onClick: async () => {
          for (const id of added) {
            try {
              await call('asset.link', { assetId: id, projectId: project.id, unlink: true });
            } catch {
              // best effort
            }
          }
          refresh();
          toast({ title: `Removed ${what} from ${project.name}` });
        },
      },
      actions: [
        {
          label: 'Move…',
          onClick: async () => {
            const to = await choose({ title: added.length === 1 ? `Move ${what} to…` : `Move ${plural(added.length, 'clip')} to…`, exclude: [project.id], allowNew: true });
            if (!to) return;
            let moved = 0;
            for (const id of added) {
              try {
                await call('asset.move', { assetId: id, from: project.id, to: to.id });
                moved++;
              } catch (e) {
                const err = toApiError(e);
                toast({ tone: 'error', title: `Didn't move ${names?.get(id) ? `“${names.get(id)}”` : 'a clip'}`, body: [err.message, err.hint].filter(Boolean).join(' ') });
              }
            }
            refresh();
            const toName = to.name || (await call('project.show', { id: to.id }).then((p) => p.project.name).catch(() => 'the other project'));
            if (moved) toast({ tone: 'success', title: `Moved ${what} to ${toName}`, body: `${moved === 1 ? 'It is' : 'They are'} no longer in ${project.name}.` });
          },
        },
      ],
      durationMs: 9000,
    });
  };
}

/* ---------- Safe drop targets (B) ---------- */

/*
 * One project at a time is "armed" for a drop: after the pointer has stayed
 * over it for ARM_MS, or at once when the pointer is well inside it (INSET px
 * from every edge). Only the armed project lights up, the cursor label says
 * "Add to fernbrook", and a drop anywhere else does nothing, so a drop near the
 * edge between two cards can't land on the neighbour.
 */
const ARM_MS = 250;
const INSET = 18;
let armedTarget: { id: string; name: string } | null = null;
const armSubs = new Set<() => void>();
function setArmed(v: { id: string; name: string } | null) {
  if (armedTarget?.id === v?.id) return;
  armedTarget = v;
  armSubs.forEach((fn) => fn());
}
/** The project a drop would land on now (null: none). */
export function useArmedDrop(): { id: string; name: string } | null {
  return useSyncExternalStore(
    (fn) => {
      armSubs.add(fn);
      return () => armSubs.delete(fn);
    },
    () => armedTarget,
  );
}

/** Drop-target props for anything that represents a project (cards, lists). */
export function useProjectDropTarget(project: { id: string; name: string }, names?: Map<string, string>) {
  const drag = useAssetDrag();
  const armed = useArmedDrop();
  const [over, setOver] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  const add = useAddToProject();
  const isArmed = armed?.id === project.id;
  useEffect(() => {
    if (!drag) {
      window.clearTimeout(timer.current);
      setOver(false);
      setArmed(null);
    }
  }, [drag]);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  const arm = () => setArmed({ id: project.id, name: project.name });
  return {
    dragging: !!drag,
    /** This project would take the drop now. */
    over: over && isArmed,
    props: {
      onDragEnter: (e: React.DragEvent) => {
        if (!isAssetDrag(e.dataTransfer)) return;
        e.preventDefault();
        setOver(true);
        window.clearTimeout(timer.current);
        timer.current = window.setTimeout(arm, ARM_MS);
      },
      onDragOver: (e: React.DragEvent) => {
        if (!isAssetDrag(e.dataTransfer)) return;
        e.preventDefault();
        const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
        const inside = e.clientX > r.left + INSET && e.clientX < r.right - INSET && e.clientY > r.top + INSET && e.clientY < r.bottom - INSET;
        if (inside && armedTarget?.id !== project.id) arm();
        e.dataTransfer.dropEffect = armedTarget?.id === project.id ? 'link' : 'none';
        if (!over) setOver(true);
      },
      onDragLeave: (e: React.DragEvent) => {
        if ((e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) return;
        setOver(false);
        window.clearTimeout(timer.current);
        if (armedTarget?.id === project.id) setArmed(null);
      },
      onDrop: (e: React.DragEvent) => {
        const ids = draggedAssetIds(e.dataTransfer);
        setOver(false);
        window.clearTimeout(timer.current);
        if (!ids) return;
        e.preventDefault();
        e.stopPropagation();
        const ok = armedTarget?.id === project.id;
        setArmed(null);
        if (ok) void add(ids, project, names);
      },
    },
  };
}

export function AssetGridSkeleton({ count = 6 }: { count?: number }) {
  return (
    <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4 2xl:grid-cols-6">
      {Array.from({ length: count }, (_, i) => (
        <div key={i} className="p-1.5">
          <Skeleton className="aspect-video w-full" />
          <Skeleton className="mt-2.5 h-3.5 w-3/4" />
          <Skeleton className="mt-1.5 h-3 w-1/2" />
        </div>
      ))}
    </div>
  );
}

/* ---------- Projects ---------- */

export function ProjectCard({ project, active, review, names }: { project: ProjectSummary; active?: boolean; review?: { number: number; state: string }; names?: Map<string, string> }) {
  const drop = useProjectDropTarget({ id: project.id, name: project.name }, names);
  return (
    <Link
      to={`/p/${project.id}`}
      data-active={active || undefined}
      data-drop-project={project.id}
      {...drop.props}
      data-drop-armed={drop.over || undefined}
      className={cx(
        'group block rounded-[18px] p-2 transition-[background-color,box-shadow,transform] duration-300 ease-[var(--ease-out-expo)] hover:bg-raised hover:shadow-[var(--shadow-soft)]',
        // Nothing moves under the pointer while dragging (a moving card could put the neighbour under it).
        !drop.dragging && 'hover:-translate-y-0.5',
        active && 'bg-raised ring-1 ring-line-strong',
        drop.dragging && (drop.over ? 'mr-drop-over' : 'mr-drop-target'),
      )}
    >
      <div className="relative overflow-hidden rounded-[12px]">
        <Thumb
          src={project.thumbAssetId ? media.asset(project.thumbAssetId, 'thumbnail') : undefined}
          alt=""
          className="aspect-[16/10] w-full"
          imgClassName="group-hover:scale-[1.04] duration-700"
          fallback={<FilmSlate size={26} />}
        />
        <div className="pointer-events-none absolute inset-0 bg-gradient-to-t from-black/55 via-transparent to-transparent" />
        {drop.dragging ? (
          <div className={cx('pointer-events-none absolute inset-0 grid place-items-center transition-colors', drop.over ? 'bg-accent/30' : 'bg-black/25')}>
            <span className="inline-flex items-center gap-1.5 rounded-full bg-raised/95 px-3 py-1.5 text-[12.5px] font-medium text-fg shadow-[var(--shadow-soft)]">
              <DownloadSimple size={14} weight="bold" className="text-accent-text" /> {drop.over ? `Drop to add to ${project.name}` : 'Hold here to add'}
            </span>
          </div>
        ) : null}
        <div className="absolute bottom-2 left-2.5 right-2.5 flex items-center gap-1.5">
          {/* The cut's latest version (an AI app's draft beside the cut takes a number too, D248). */}
          {(project.headRevisionNumber || project.revisionSeq) > 0 ? (
            <span className="tc rounded-[6px] bg-black/60 px-1.5 py-0.5 text-[10.5px] text-white">r{project.headRevisionNumber || project.revisionSeq}</span>
          ) : null}
          {review?.state === 'draft' ? <span className="rounded-[6px] bg-accent px-1.5 py-0.5 text-[10.5px] font-medium text-accent-ink">Draft</span> : null}
          {review?.state === 'rejected' ? <span className="rounded-[6px] bg-danger px-1.5 py-0.5 text-[10.5px] font-medium text-white">Needs changes</span> : null}
          {review?.state === 'approved' ? <span className="rounded-[6px] bg-ok px-1.5 py-0.5 text-[10.5px] font-medium text-black/85">Approved</span> : null}
          <TodoBadge projectId={project.id} link={false} tone="overlay" className="ml-auto" />
        </div>
      </div>
      <div className="px-1.5 pb-1.5 pt-3">
        <div className="truncate text-[15px] font-semibold tracking-[-0.015em] text-fg"><Theirs>{project.name}</Theirs></div>
        <div className="clamp-2 mt-1 min-h-[2.6em] text-[12.5px] leading-[1.3rem] text-muted">{project.brief.goal || <span className="text-subtle">No brief yet</span>}</div>
        <div className="mt-2.5 flex items-center gap-2 text-[11.5px] text-subtle">
          <span>{plural(project.assetCount, 'source')}</span>
          <span aria-hidden>·</span>
          <span className="truncate">updated {relative(project.updatedAt)}</span>
        </div>
      </div>
    </Link>
  );
}

export function ProjectGridSkeleton() {
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
      {[0, 1, 2].map((i) => (
        <div key={i} className="p-2">
          <Skeleton className="aspect-[16/10] w-full rounded-[12px]" />
          <Skeleton className="mt-3 h-4 w-2/3" />
          <Skeleton className="mt-2 h-3 w-full" />
          <Skeleton className="mt-1.5 h-3 w-1/2" />
        </div>
      ))}
    </div>
  );
}

/* ---------- Project form (create / edit brief) ---------- */

const PLATFORMS = ['shorts', 'tiktok', 'reels', 'youtube', 'generic'];

export function ProjectForm({ initial, submitLabel, onSubmit, onCancel, busy, error }: {
  initial?: { name: string; brief: Brief };
  submitLabel: string;
  onSubmit: (v: { name: string; brief: Brief }) => void;
  onCancel: () => void;
  busy?: boolean;
  error?: string | null;
}) {
  const [name, setName] = useState(initial?.name ?? '');
  const [goal, setGoal] = useState(initial?.brief.goal ?? '');
  const [audience, setAudience] = useState(initial?.brief.audience ?? '');
  const [tone, setTone] = useState(initial?.brief.tone ?? '');
  const [target, setTarget] = useState(initial?.brief.targetSeconds ? String(initial.brief.targetSeconds) : '');
  const [platforms, setPlatforms] = useState<string[]>(initial?.brief.platforms ?? []);
  const [touched, setTouched] = useState(false);
  const nameErr = touched && !name.trim() ? 'A project needs a name.' : null;
  const field = 'mt-1.5 h-10 w-full rounded-[10px] bg-panel px-3 text-[14px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0';
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        setTouched(true);
        if (!name.trim()) return;
        const brief: Brief = {};
        if (goal.trim()) brief.goal = goal.trim();
        if (audience.trim()) brief.audience = audience.trim();
        if (tone.trim()) brief.tone = tone.trim();
        if (platforms.length) brief.platforms = platforms;
        const t = Number(target);
        if (target && Number.isFinite(t) && t > 0) brief.targetSeconds = t;
        if (initial?.brief.constraints) brief.constraints = initial.brief.constraints;
        onSubmit({ name: name.trim(), brief });
      }}
      className="space-y-4"
    >
      <div>
        <label htmlFor="pf-name" className="text-[12.5px] font-medium text-muted">
          Name
        </label>
        <input id="pf-name" data-autofocus value={name} maxLength={120} onChange={(e) => setName(e.target.value)} onBlur={() => setTouched(true)} placeholder="Spring launch teaser" aria-invalid={!!nameErr} className={field} />
        <div className="mt-1 min-h-[16px] text-[12px] text-danger">{nameErr}</div>
      </div>
      <div>
        <label htmlFor="pf-goal" className="text-[12.5px] font-medium text-muted">
          Goal
        </label>
        <textarea id="pf-goal" rows={2} value={goal} onChange={(e) => setGoal(e.target.value)} placeholder="A 30-second story about why we started" className={cx(field, 'h-auto resize-y py-2 leading-relaxed')} />
        <div className="mt-1 text-[12px] text-subtle">Claude Code reads the brief before every edit.</div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <div>
          <label htmlFor="pf-aud" className="text-[12.5px] font-medium text-muted">
            Audience
          </label>
          <input id="pf-aud" value={audience} onChange={(e) => setAudience(e.target.value)} placeholder="New customers" className={field} />
        </div>
        <div>
          <label htmlFor="pf-tone" className="text-[12.5px] font-medium text-muted">
            Tone
          </label>
          <input id="pf-tone" value={tone} onChange={(e) => setTone(e.target.value)} placeholder="Warm, direct" className={field} />
        </div>
        <div>
          <label htmlFor="pf-len" className="text-[12.5px] font-medium text-muted">
            Target length (s)
          </label>
          <input id="pf-len" inputMode="decimal" value={target} onChange={(e) => setTarget(e.target.value.replace(/[^\d.]/g, ''))} placeholder="30" className={cx(field, 'tc')} />
        </div>
      </div>
      <fieldset>
        <legend className="text-[12.5px] font-medium text-muted">Platforms</legend>
        <div className="mt-2 flex flex-wrap gap-1.5">
          {PLATFORMS.map((p) => {
            const on = platforms.includes(p);
            return (
              <button
                key={p}
                type="button"
                aria-pressed={on}
                onClick={() => setPlatforms(on ? platforms.filter((x) => x !== p) : [...platforms, p])}
                className={cx('h-8 rounded-full px-3 text-[12.5px] transition-colors hairline', on ? 'bg-accent-soft text-accent-text' : 'text-muted hover:bg-hover hover:text-fg')}
              >
                {p}
              </button>
            );
          })}
        </div>
      </fieldset>
      {error ? <div className="rounded-[10px] bg-danger-soft px-3 py-2 text-[12.5px] text-danger">{error}</div> : null}
      <div className="flex justify-end gap-2 pt-2">
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" loading={busy}>
          {submitLabel}
        </Button>
      </div>
    </form>
  );
}

function CreateProjectBody({ close, assetIds }: { close: (id?: string) => void; assetIds?: string[] }) {
  const toast = useToast();
  const create = useOpMutation('project.create', { invalidate: ['project.list'] });
  const link = useOpMutation('asset.link', { invalidate: ['asset.list', 'project.show', 'project.list'] });
  const [err, setErr] = useState<string | null>(null);
  return (
    <ProjectForm
      submitLabel="Create project"
      busy={create.isPending || link.isPending}
      error={err}
      onCancel={() => close()}
      onSubmit={(v) => {
        setErr(null);
        create.mutate(v, {
          onSuccess: async (out) => {
            for (const id of assetIds ?? []) {
              try {
                await link.mutateAsync({ assetId: id, projectId: out.project.id });
              } catch {
                // reported below
              }
            }
            toast({ tone: 'success', title: `Created ${out.project.name}`, body: assetIds?.length ? `With ${plural(assetIds.length, 'source')} from the inbox.` : 'Drop footage onto the project to start.' });
            close(out.project.id);
          },
          onError: (e) => setErr([e.message, e.hint].filter(Boolean).join(' ')),
        });
      }}
    />
  );
}

/** Opens the create-project dialog; resolves with the new project ID. */
export function useCreateProject() {
  const dialog = useDialog();
  return (assetIds?: string[]) => dialog.open<string>((close) => <CreateProjectBody close={close} assetIds={assetIds} />, { title: 'New project', size: 'md' });
}

function PickProjectBody({ close, assets }: { close: (id?: string) => void; assets: AssetSummary[] }) {
  const projects = useOp('project.list', {});
  const add = useAddToProject();
  const create = useCreateProject();
  const [busy, setBusy] = useState<string | null>(null);
  const list = projects.data?.projects ?? [];
  const names = new Map(assets.map((a) => [a.id, a.title]));
  const label = assets.length === 1 ? `“${assets[0]!.title}”` : plural(assets.length, 'clip');
  return (
    <div>
      <p className="text-[13px] text-muted">
        Add <span className="font-medium text-fg">{label}</span> to a project. Nothing is copied: the project points at the clips you already have.
      </p>
      <div className="mt-4 max-h-[46dvh] space-y-1 overflow-y-auto">
        {projects.isLoading ? (
          <>
            <Skeleton className="h-14" />
            <Skeleton className="h-14" />
          </>
        ) : null}
        {list.map((p) => {
          const already = assets.every((a) => a.projectIds.includes(p.id));
          return (
            <button
              key={p.id}
              type="button"
              disabled={already || !!busy}
              onClick={async () => {
                setBusy(p.id);
                await add(
                  assets.map((a) => a.id),
                  { id: p.id, name: p.name },
                  names,
                );
                setBusy(null);
                close(p.id);
              }}
              className="flex w-full items-center gap-3 rounded-[12px] p-2 text-left transition-colors hover:bg-hover disabled:opacity-60"
            >
              <Thumb src={p.thumbAssetId ? media.asset(p.thumbAssetId, 'thumbnail') : undefined} alt="" className="h-10 w-16 shrink-0 rounded-[8px]" fallback={<FilmSlate size={14} />} />
              <div className="min-w-0 flex-1">
                <div className="truncate text-[13.5px] font-medium text-fg"><Theirs>{p.name}</Theirs></div>
                <div className="text-[11.5px] text-subtle">{plural(p.assetCount, 'clip')}</div>
              </div>
              {already ? <Badge tone="ok">added</Badge> : busy === p.id ? <Spinner size={13} /> : <Plus size={15} className="text-subtle" />}
            </button>
          );
        })}
      </div>
      <div className="mt-4 flex items-center justify-between gap-2 border-t border-line pt-4">
        <Button
          variant="quiet"
          icon={<FolderSimplePlus size={15} />}
          onClick={async () => {
            const id = await create(assets.map((a) => a.id));
            if (id) close(id);
          }}
        >
          New project with {assets.length === 1 ? 'this' : 'these'}
        </Button>
        <Button variant="ghost" onClick={() => close()}>
          Done
        </Button>
      </div>
    </div>
  );
}

/** The keyboard-friendly way to add clips to a project (drag and drop does the same). */
export function usePickProject() {
  const dialog = useDialog();
  return (assets: AssetSummary | AssetSummary[]) => {
    const list = Array.isArray(assets) ? assets : [assets];
    return dialog.open<string>((close) => <PickProjectBody close={close} assets={list} />, { title: list.length === 1 ? 'Add to project' : `Add ${plural(list.length, 'clip')} to a project`, size: 'sm' });
  };
}

