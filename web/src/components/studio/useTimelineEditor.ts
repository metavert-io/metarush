import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../../api/client';
import type { CutProblem, EditOp, RenderPlan, Spec } from '../../api/types';
import type { ops } from '@metarush/schema';
import { applyOps, cutDiffOps, keepMine as keepMineOps, OpError, plainRefusal, sameCut } from '../../lib/editops';
import { loadPref, savePref } from '../../lib/theme';

export type EditPreviewOut = ops.EditPreviewOut;
export type PreviewConflict = ops.PreviewConflict;

/*
 * The studio timeline's editing state (D58). Every gesture is a normal edit:
 * its ops join a batch of unsaved ops written against the version they started
 * from; `edit.preview` answers with the cut Go makes of them (validated, its
 * frame-accurate render plan, a conflict when someone else changed the same
 * parts) and `edit.apply` saves the batch in the background as one revision
 * (about 2 s after the last gesture, or when the page is left), origin
 * "timeline:studio", with a plain summary. Undo and redo walk back through
 * the gestures, also once they are saved: undoing a saved one is a new edit
 * that puts its clips back. Unsaved ops are also kept in this browser, so a
 * change that can't be saved yet survives a reload.
 */

const ORIGIN = 'timeline:studio';
const SAVE_AFTER_MS = 2000;
const PREVIEW_AFTER_MS = 120;

interface Batch {
  id: number;
  ops: EditOp[];
}

interface Group {
  id: number;
  label: string;
  ops: EditOp[];
  before: Spec;
  after: Spec;
  /** The unsaved batch that last moved this gesture (its ops, or its undo). */
  batch?: number;
}

interface Draft {
  baseId: string;
  ops: EditOp[];
  at: number;
}

export interface BaseRevision {
  id: string;
  number: number;
  spec: Spec;
}

export type SaveState = 'saved' | 'unsaved' | 'saving' | 'blocked' | 'conflict' | 'error';

export interface TimelineEditor {
  base?: BaseRevision;
  /** The cut as it stands with unsaved changes (the server's answer when it is current). */
  spec?: Spec;
  /** The render plan to play: the current preview's, else the saved version's. */
  plan?: RenderPlan;
  dirty: boolean;
  saveState: SaveState;
  /** Problems that stop saving, in plain words. */
  problems: CutProblem[];
  warnings: CutProblem[];
  conflict?: PreviewConflict;
  saveError?: string;
  canUndo: boolean;
  canRedo: boolean;
  undoLabel?: string;
  redoLabel?: string;
  /** A gesture: returns an error message when it can't be done (nothing changes). */
  apply(label: string, ops: EditOp[]): string | null;
  undo(): string | null;
  redo(): string | null;
  /** Save now (resolves true when nothing is left unsaved). */
  flush(): Promise<boolean>;
  keepMine(): Promise<void>;
  keepTheirs(): Promise<void>;
  /** For changes that save themselves (a transition, words): save first, run, then follow the new version. */
  runSaved(label: string, run: () => Promise<string | undefined>): Promise<boolean>;
  /** Preview the unsaved changes plus extra ops (the transition picker's live preview). */
  previewWith(extra: EditOp[]): Promise<EditPreviewOut>;
  /** True while a pointer gesture is in progress (saving waits for it). */
  setBusy(busy: boolean): void;
  clearError(): void;
}

export function useTimelineEditor(projectId: string, head: { id: string; number: number } | undefined): TimelineEditor {
  const qc = useQueryClient();
  const draftKey = `timeline.draft.${projectId}`;
  const [base, setBase] = useState<BaseRevision>();
  const [batches, setBatches] = useState<Batch[]>([]);
  const [history, setHistory] = useState<Group[]>([]);
  const [future, setFuture] = useState<Group[]>([]);
  const [preview, setPreview] = useState<{ key: string; out: EditPreviewOut }>();
  const [basePlan, setBasePlan] = useState<{ id: string; plan: RenderPlan }>();
  const [lastPlan, setLastPlan] = useState<RenderPlan>();
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string>();
  const [busy, setBusyState] = useState(false);
  const batchSeq = useRef(0);
  const previewSeq = useRef(0);
  const savingBatches = useRef(new Set<number>());
  const restored = useRef(false);
  const state = useRef({ base, batches, saving });
  state.current = { base, batches, saving };

  const pending = useMemo(() => batches.flatMap((b) => b.ops), [batches]);
  const headKey = head?.id ?? '';
  const previewKey = base ? `${base.id}@${headKey}:${JSON.stringify(pending)}` : '';

  const loadBase = useCallback(
    async (id: string) => {
      const rev = await qc.fetchQuery({ queryKey: ['revision.show', { id }], queryFn: ({ signal }) => call('revision.show', { id }, signal), staleTime: 60_000 });
      return { id: rev.summary.id, number: rev.summary.number, spec: rev.spec } satisfies BaseRevision;
    },
    [qc],
  );

  // First load: unsaved changes kept in this browser win (written against their own version).
  useEffect(() => {
    if (restored.current || !head) return;
    restored.current = true;
    const draft = loadPref<Draft | null>(draftKey, null);
    const start = draft?.ops.length ? draft.baseId : head.id;
    void loadBase(start)
      .then((b) => {
        setBase(b);
        if (draft?.ops.length && b.id === draft.baseId) setBatches([{ id: ++batchSeq.current, ops: draft.ops }]);
      })
      .catch(() => loadBase(head.id).then(setBase));
  }, [head, draftKey, loadBase]);

  // Afterwards the edits follow the head whenever nothing is unsaved (a newer version only).
  useEffect(() => {
    if (!restored.current || !base || !head || head.id === base.id || head.number < base.number) return;
    if (batches.length || saving) return; // unsaved work: the preview rebases it onto the head
    let gone = false;
    void loadBase(head.id).then((b) => !gone && setBase(b));
    return () => {
      gone = true;
    };
  }, [head, base, loadBase, batches.length, saving]);

  // Unsaved ops survive a reload (cleared once saved). Clearing is immediate: a
  // draft left behind by leaving the page right after a save would be applied
  // again on the next visit, against the version it was written for.
  useEffect(() => {
    if (!base) return;
    if (!pending.length) {
      savePref(draftKey, null);
      return;
    }
    const t = setTimeout(() => savePref(draftKey, { baseId: base.id, ops: pending, at: Date.now() } satisfies Draft), 250);
    return () => clearTimeout(t);
  }, [base, pending, draftKey]);

  // The saved version's plan, for playing when nothing is unsaved.
  useEffect(() => {
    if (!base || basePlan?.id === base.id) return;
    let gone = false;
    void qc
      .fetchQuery({ queryKey: ['revision.plan', { id: base.id }], queryFn: ({ signal }) => call('revision.plan', { id: base.id }, signal), staleTime: 60_000 })
      .then((plan) => !gone && setBasePlan({ id: base.id, plan }))
      .catch(() => undefined);
    return () => {
      gone = true;
    };
  }, [base, basePlan?.id, qc]);

  const working = useMemo(() => {
    if (!base) return undefined;
    try {
      return applyOps(base.spec, pending);
    } catch {
      return base.spec;
    }
  }, [base, pending]);

  // Ask Go what the unsaved changes make (debounced, latest wins). The head is
  // in the key, so a newer version someone saved is noticed (rebase or conflict).
  useEffect(() => {
    if (!base || !pending.length || preview?.key === previewKey) return;
    const my = ++previewSeq.current;
    const ctrl = new AbortController();
    const t = setTimeout(() => {
      call('edit.preview', { plan: { baseRevisionId: base.id, origin: ORIGIN, ops: pending } }, ctrl.signal)
        .then((out) => {
          if (my !== previewSeq.current) return;
          setPreview({ key: previewKey, out });
          if (out.valid && out.renderPlan) setLastPlan(out.renderPlan);
        })
        .catch(() => undefined);
    }, PREVIEW_AFTER_MS);
    return () => {
      clearTimeout(t);
      ctrl.abort();
    };
  }, [base, pending, previewKey, preview?.key]);

  const current = pending.length && preview?.key === previewKey ? preview.out : undefined;
  const conflict = current?.conflict ?? undefined;
  const problems = current?.errors ?? [];
  const blocked = problems.length > 0;

  const save = useCallback(async (): Promise<boolean> => {
    const { base: b, batches: bs, saving: inFlight } = state.current;
    if (!b || !bs.length) return true;
    if (inFlight) return false;
    const taken = bs.map((x) => x.id);
    const ops = bs.flatMap((x) => x.ops);
    setSaving(true);
    setSaveError(undefined);
    taken.forEach((id) => savingBatches.current.add(id));
    try {
      const res = await call('edit.apply', { plan: { baseRevisionId: b.id, origin: ORIGIN, ops } });
      const next = await loadBase(res.revision.id);
      setBase(next);
      setBatches((cur) => cur.filter((x) => !taken.includes(x.id)));
      for (const key of ['project.show', 'project.list', 'revision.list', 'cut.show', 'audit.list']) void qc.invalidateQueries({ queryKey: [key] });
      return true;
    } catch (err) {
      const e = toApiError(err);
      if (e.code === 'E_REVISION_CONFLICT') {
        void qc.invalidateQueries({ queryKey: ['project.show'] }); // the preview then explains it (Keep mine / Keep theirs)
        setPreview(undefined);
      } else setSaveError([e.message, e.hint].filter(Boolean).join(' '));
      return false;
    } finally {
      taken.forEach((id) => savingBatches.current.delete(id));
      setSaving(false);
    }
  }, [loadBase, qc]);

  // Background saves: a while after the last gesture, never during one, never with problems.
  useEffect(() => {
    if (!batches.length || busy || saving || blocked || conflict || saveError || !current) return;
    const t = setTimeout(() => void save(), SAVE_AFTER_MS);
    return () => clearTimeout(t);
  }, [batches, busy, saving, blocked, conflict, saveError, current, save]);

  // Hiding the page saves at once; closing it sends the save on its way (it survives the unload).
  const canSave = !blocked && !conflict;
  const saveRef = useRef(save);
  saveRef.current = save;
  useEffect(() => {
    const onHide = () => {
      if (document.visibilityState === 'hidden' && canSave) void saveRef.current();
    };
    const onLeave = () => {
      const { base: b, batches: bs, saving: inFlight } = state.current;
      if (!b || !bs.length || !canSave || inFlight) return;
      void fetch('/api/ops/edit.apply', {
        method: 'POST',
        keepalive: true,
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-MetaRush-Client': 'dashboard' },
        body: JSON.stringify({ plan: { baseRevisionId: b.id, origin: ORIGIN, ops: bs.flatMap((x) => x.ops) } }),
      })
        .then((r) => r.ok && savePref(draftKey, null))
        .catch(() => undefined);
    };
    document.addEventListener('visibilitychange', onHide);
    window.addEventListener('pagehide', onLeave);
    return () => {
      document.removeEventListener('visibilitychange', onHide);
      window.removeEventListener('pagehide', onLeave);
    };
  }, [canSave, draftKey]);
  // Leaving the editor inside the app (another tab of the project, another page).
  useEffect(() => () => void saveRef.current(), []);

  const lastBatchId = batches[batches.length - 1]?.id;
  const droppable = (id: number | undefined) => id !== undefined && id === lastBatchId && !savingBatches.current.has(id);
  const push = (ops: EditOp[]) => {
    const id = ++batchSeq.current;
    setBatches((cur) => [...cur, { id, ops }]);
    return id;
  };

  const apply = (label: string, ops: EditOp[]): string | null => {
    if (!working || !ops.length) return null;
    let after: Spec;
    try {
      after = applyOps(working, ops);
    } catch (e) {
      return e instanceof OpError ? plainRefusal(e.message) : 'That change could not be made.';
    }
    if (sameCut(after, working)) return null;
    const id = push(ops);
    setHistory((h) => [...h.slice(-199), { id, label, ops, before: working, after, batch: id }]);
    setFuture([]);
    setSaveError(undefined);
    return null;
  };

  const undo = (): string | null => {
    const g = history[history.length - 1];
    if (!g || !working) return null;
    if (droppable(g.batch)) {
      setBatches((cur) => cur.slice(0, -1));
      setHistory((h) => h.slice(0, -1));
      setFuture((f) => [...f, { ...g, batch: undefined }]);
      return null;
    }
    const ops = cutDiffOps(g.after, g.before);
    try {
      applyOps(working, ops);
    } catch {
      return "That can't be undone any more: what it changed was changed again since.";
    }
    const id = push(ops);
    setHistory((h) => h.slice(0, -1));
    setFuture((f) => [...f, { ...g, batch: id }]);
    return null;
  };

  const redo = (): string | null => {
    const g = future[future.length - 1];
    if (!g || !working) return null;
    if (droppable(g.batch)) {
      setBatches((cur) => cur.slice(0, -1));
      setFuture((f) => f.slice(0, -1));
      setHistory((h) => [...h, { ...g, batch: undefined }]);
      return null;
    }
    // The gesture's own ops when the cut is where it was; else the clips (and line places) it made.
    const ops = sameCut(working, g.before) ? g.ops : cutDiffOps(g.before, g.after);
    try {
      applyOps(working, ops);
    } catch {
      return "That can't be redone: what it changed was changed since.";
    }
    const id = push(ops);
    setFuture((f) => f.slice(0, -1));
    setHistory((h) => [...h, { ...g, batch: id }]);
    return null;
  };

  const flush = useCallback(async () => {
    if (!state.current.batches.length) return true;
    if (blocked || conflict) return false;
    return save();
  }, [blocked, conflict, save]);

  const followHead = useCallback(async () => {
    const p = await call('project.show', { id: projectId });
    if (!p.headRevisionId) return undefined;
    const next = await loadBase(p.headRevisionId);
    setBase(next);
    return next;
  }, [loadBase, projectId]);

  const keepTheirs = useCallback(async () => {
    setBatches([]);
    setHistory([]);
    setFuture([]);
    setPreview(undefined);
    setSaveError(undefined);
    savePref(draftKey, null);
    await followHead();
  }, [followHead, draftKey]);

  const keepMine = useCallback(async () => {
    const b = state.current.base;
    if (!b || !working) return;
    const mine = working;
    const headRev = await followHead();
    if (!headRev) return;
    const ops = keepMineOps(b.spec, headRev.spec, mine);
    setBatches(ops.length ? [{ id: ++batchSeq.current, ops }] : []);
    setHistory([]);
    setFuture([]);
    setPreview(undefined);
    setSaveError(undefined);
  }, [followHead, working]);

  const runSaved = useCallback(
    async (label: string, run: () => Promise<string | undefined>) => {
      if (!(await flush())) return false;
      const before = state.current.base?.spec;
      const revId = await run();
      if (!revId) return false;
      const next = await loadBase(revId);
      setBase(next);
      if (before) {
        setHistory((h) => [...h.slice(-199), { id: ++batchSeq.current, label, ops: cutDiffOps(before, next.spec), before, after: next.spec }]);
        setFuture([]);
      }
      for (const key of ['project.show', 'project.list', 'revision.list', 'cut.show']) void qc.invalidateQueries({ queryKey: [key] });
      return true;
    },
    [flush, loadBase, qc],
  );

  const previewWith = useCallback((extra: EditOp[]) => {
    const b = state.current.base;
    if (!b) return Promise.reject(new Error('no cut yet'));
    return call('edit.preview', { plan: { baseRevisionId: b.id, origin: ORIGIN, ops: [...state.current.batches.flatMap((x) => x.ops), ...extra] } });
  }, []);

  const setBusy = useCallback((v: boolean) => setBusyState(v), []);
  const clearError = useCallback(() => setSaveError(undefined), []);

  const plan = pending.length
    ? current?.valid && current.renderPlan
      ? current.renderPlan
      : (lastPlan ?? (basePlan?.id === base?.id ? basePlan?.plan : undefined))
    : basePlan?.id === base?.id
      ? basePlan?.plan
      : lastPlan;
  const spec = current?.spec ?? working;
  const saveState: SaveState = conflict ? 'conflict' : saving ? 'saving' : saveError ? 'error' : blocked ? 'blocked' : batches.length ? 'unsaved' : 'saved';

  return {
    base,
    spec,
    plan,
    dirty: batches.length > 0,
    saveState,
    problems,
    warnings: current?.warnings ?? [],
    conflict,
    saveError,
    canUndo: history.length > 0,
    canRedo: future.length > 0,
    undoLabel: history[history.length - 1]?.label,
    redoLabel: future[future.length - 1]?.label,
    apply,
    undo,
    redo,
    flush,
    keepMine,
    keepTheirs,
    runSaved,
    previewWith,
    setBusy,
    clearError,
  };
}
