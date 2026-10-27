import { lazy, Suspense, useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowCounterClockwise, ArrowLeft, ArrowsLeftRight, CheckCircle, ClockCounterClockwise, Code, Info, LockSimple, NotePencil, PencilSimple, Quotes, Scroll, WarningCircle } from '@phosphor-icons/react';
import { ApiError, call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { CutCheckOut, CutProblem, ProjectShowOut, RevisionSummary } from '../../api/types';
import { relative } from '../../lib/format';
import { loadPref, savePref } from '../../lib/theme';
import { useDialog } from '../../ui/Dialog';
import { Badge, Button, CopyButton, cx, ErrorPanel, InlineCode, Segmented, Skeleton, Spinner } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { SheetDiff } from './cutsheet/SheetDiff';
import type { SheetEditorHandle } from './cutsheet/SheetEditor';
import { ScriptView } from './cutsheet/ScriptView';

// CodeMirror loads only when someone opens the Markdown or Edit view.
const SheetEditor = lazy(() => import('./cutsheet/SheetEditor'));

/** Formatted (read like a script), Markdown (the sheet as text, read only) or Edit (today's editor). */
export type ScriptViewMode = 'formatted' | 'markdown' | 'edit';

const VIEW_BLURB: Record<ScriptViewMode, string> = {
  formatted: 'The cut read like a script: who says what, clip by clip.',
  markdown: 'The same script as text, the way Claude Code and the command line read it.',
  edit: 'Change the text and apply it as a new version.',
};

interface Draft {
  text: string;
  base: string; // revision the draft was written from ('' for a first cut)
  at: number;
}

const CHECK_DELAY_MS = 450;

export function viewParam(v: string | null): ScriptViewMode {
  return v === 'markdown' || v === 'edit' ? v : 'formatted';
}

/**
 * The cut as a script (its cut sheet). Formatted reads like a script; Markdown
 * shows the sheet as text; Edit is the editor: changes are checked as you type
 * (problems show on their lines), and "Apply changes" saves a new revision
 * through the same check and confirm steps as every other edit. Any version
 * opens with ?rev= (older ones read only); Compare shows what changed between
 * two versions.
 */
export function CutSheetTab({ data }: { data: ProjectShowOut }) {
  const [params, setParams] = useSearchParams();
  const compare = params.get('mode') === 'compare';
  const view = viewParam(params.get('view'));
  const setParam = useCallback(
    (next: Record<string, string | undefined>) => {
      const p = new URLSearchParams(params);
      for (const [k, v] of Object.entries(next)) {
        if (v === undefined) p.delete(k);
        else p.set(k, v);
      }
      p.set('tab', 'script');
      setParams(p, { replace: true });
    },
    [params, setParams],
  );
  const rev = params.get('rev') ?? undefined;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        {compare ? (
          <Button size="sm" variant="ghost" icon={<ArrowLeft size={13} />} onClick={() => setParam({ mode: undefined, left: undefined, right: undefined })}>
            Back to the script
          </Button>
        ) : (
          <Segmented<ScriptViewMode>
            label="How to show the script"
            value={view}
            onChange={(v) => setParam({ view: v === 'formatted' ? undefined : v })}
            items={[
              { value: 'formatted', label: 'Formatted', icon: <Scroll size={14} /> },
              { value: 'markdown', label: 'Markdown', icon: <Code size={14} /> },
              { value: 'edit', label: 'Edit', icon: <PencilSimple size={14} /> },
            ]}
          />
        )}
        <p className="min-w-0 text-[12.5px] text-subtle max-sm:basis-full">{compare ? 'What changed between two versions of the cut.' : VIEW_BLURB[view]}</p>
        {!compare && data.revisions.length > 1 ? (
          <Button className="ml-auto" size="sm" variant="ghost" icon={<ArrowsLeftRight size={13} />} onClick={() => setParam({ mode: 'compare' })}>
            Compare versions
          </Button>
        ) : null}
      </div>
      {compare ? (
        <CompareSheets data={data} params={params} setParam={setParam} />
      ) : view === 'edit' ? (
        <EditSheet data={data} rev={rev} onPickRevision={(id) => setParam({ rev: id })} />
      ) : (
        <ReadSheet data={data} view={view} rev={rev} onPickRevision={(id) => setParam({ rev: id })} onEdit={() => setParam({ view: 'edit', rev: undefined })} />
      )}
    </div>
  );
}

/* ---------- reading (formatted or as Markdown) ---------- */

function ReadSheet({ data, view, rev, onPickRevision, onEdit }: {
  data: ProjectShowOut;
  view: Exclude<ScriptViewMode, 'edit'>;
  rev?: string;
  onPickRevision: (id: string | undefined) => void;
  onEdit: () => void;
}) {
  const projectId = data.project.id;
  const head = data.headRevisionId;
  const target = rev && rev !== head ? rev : head;
  const show = useOp('cut.show', target ? { projectId, revision: target, format: 'json' } : { projectId, format: 'json' });
  const navigate = useNavigate();
  const draft = loadPref<Draft | null>(`cutsheet.draft.${projectId}`, null);
  const headRev = data.revisions.find((r) => r.id === head);
  const older = !!show.data?.readOnly;
  const revision = show.data?.revision;

  const playClip = (clipId: string) => {
    const q = `clip=${encodeURIComponent(clipId)}&play=1`;
    if (!revision || revision.isHead) navigate(`/p/${projectId}?tab=cut&${q}`);
    else navigate(`/p/${projectId}/r/${revision.id}?${q}`);
  };
  const openSource = (assetId: string, t: number) => navigate(`/p/${projectId}?tab=sources&asset=${assetId}&t=${t.toFixed(3)}`);

  if (show.error) return <ErrorPanel error={show.error} onRetry={() => void show.refetch()} />;
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <RevisionPicker revisions={data.revisions} value={target} head={head} onChange={(id) => onPickRevision(id === head ? undefined : id)} />
        {older ? (
          <Badge tone="info" icon={<LockSimple size={11} weight="fill" />}>
            older version
          </Badge>
        ) : null}
        <div className="ml-auto flex items-center gap-2">
          {view === 'markdown' && show.data ? <CopyButton text={show.data.sheet} label="Copy the text" /> : null}
          {older ? (
            <Button size="sm" variant="secondary" onClick={() => onPickRevision(undefined)}>
              Show the latest
            </Button>
          ) : null}
        </div>
      </div>
      {older && revision ? (
        <div className="flex items-start gap-2 rounded-[12px] bg-info-soft px-4 py-2.5 text-[12.5px] leading-relaxed text-info">
          <ClockCounterClockwise size={14} className="mt-[3px] shrink-0" />
          <span>
            You are looking at version {revision.number}. The latest is version {headRev?.number ?? ''}; changes always start from it.
          </span>
        </div>
      ) : null}
      {draft && !older ? (
        <div className="flex flex-wrap items-center gap-2 rounded-[12px] bg-accent-soft px-4 py-2.5 text-[12.5px] leading-relaxed text-accent-text">
          <NotePencil size={14} className="shrink-0" />
          <span>You have changes that are not saved yet ({relative(draft.at)}).</span>
          <button type="button" onClick={onEdit} className="font-semibold underline-offset-2 hover:underline">
            Open them in Edit
          </button>
        </div>
      ) : null}
      {show.isLoading || !show.data ? (
        view === 'formatted' ? (
          <div className="mx-auto max-w-[900px] space-y-4" aria-busy="true" aria-label="Loading the script">
            <Skeleton className="h-44 rounded-[16px]" />
            {[0, 1, 2].map((i) => (
              <div key={i} className="flex gap-4 px-4">
                <Skeleton className="h-[86px] w-[132px] shrink-0 rounded-[10px]" />
                <div className="flex-1 space-y-2">
                  <Skeleton className="h-4 w-1/3" />
                  <Skeleton className="h-3.5 w-1/2" />
                  <Skeleton className="h-3.5 w-2/3" />
                </div>
              </div>
            ))}
          </div>
        ) : (
          <div className="panel">
            <EditorSkeleton />
          </div>
        )
      ) : view === 'formatted' ? (
        <ScriptView data={data} text={show.data.sheet} spec={show.data.spec} revision={revision} starter={show.data.starter} onPlayClip={playClip} onOpenSource={openSource} />
      ) : (
        <section className="panel overflow-hidden" aria-label="The script as text">
          <div className="h-[min(70dvh,820px)] min-h-[340px]">
            <Suspense fallback={<EditorSkeleton />}>
              <SheetEditor value={show.data.sheet} readOnly problems={[]} onChange={() => undefined} label={`The script of version ${revision?.number ?? ''} as text (read only)`} />
            </Suspense>
          </div>
        </section>
      )}
    </div>
  );
}

/* ---------- editing ---------- */

function EditSheet({ data, rev, onPickRevision }: { data: ProjectShowOut; rev?: string; onPickRevision: (id: string | undefined) => void }) {
  const projectId = data.project.id;
  const head = data.headRevisionId;
  const target = rev && rev !== head ? rev : head;
  // The same query as the Formatted view (with the spec), so switching views doesn't fetch again.
  const show = useOp('cut.show', target ? { projectId, revision: target, format: 'json' } : { projectId, format: 'json' });
  const qc = useQueryClient();
  const dialog = useDialog();
  const toast = useToast();
  const editor = useRef<SheetEditorHandle>(null);
  const draftKey = `cutsheet.draft.${projectId}`;

  const server = show.data?.sheet;
  const serverRev = show.data?.revision?.id ?? '';
  const readOnly = !!show.data?.readOnly;
  const [text, setText] = useState<string>();
  const [base, setBase] = useState<string>();
  const [startedFrom, setStartedFrom] = useState<string>(''); // revision the current edits started from
  const [restored, setRestored] = useState<number>();
  const dirty = text !== undefined && base !== undefined && text !== base;
  const needsLoad = text === undefined;

  // Load the server's sheet; keep the person's edits if they have any.
  useEffect(() => {
    if (server === undefined) return;
    if (needsLoad || !dirty || readOnly) {
      const draft = !readOnly && text === undefined ? loadPref<Draft | null>(draftKey, null) : null;
      if (draft && draft.text !== server) {
        setText(draft.text);
        setStartedFrom(draft.base);
        setRestored(draft.at);
      } else {
        setText(server);
        setStartedFrom(serverRev);
        setRestored(undefined);
      }
      setBase(server);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [server, serverRev, readOnly, needsLoad]);

  // Unsaved edits survive switching tabs or reloading (this browser only).
  useEffect(() => {
    if (readOnly || text === undefined) return;
    const t = setTimeout(() => savePref(draftKey, dirty ? ({ text, base: startedFrom, at: Date.now() } satisfies Draft) : null), 300);
    return () => clearTimeout(t);
  }, [text, dirty, readOnly, startedFrom, draftKey]);

  // Check as you type.
  const [check, setCheck] = useState<CutCheckOut>();
  const [checkErr, setCheckErr] = useState<ApiError>();
  const [checking, setChecking] = useState(false);
  const seq = useRef(0);
  useEffect(() => {
    if (text === undefined || readOnly) {
      setCheck(undefined);
      setCheckErr(undefined);
      return;
    }
    const my = ++seq.current;
    const ctrl = new AbortController();
    setChecking(true);
    const timer = setTimeout(() => {
      call('cut.check', { sheet: text }, ctrl.signal)
        .then((out) => {
          if (my !== seq.current) return;
          setCheck(out);
          setCheckErr(undefined);
        })
        .catch((err: unknown) => {
          if (my !== seq.current || ctrl.signal.aborted) return;
          setCheck(undefined);
          setCheckErr(toApiError(err));
        })
        .finally(() => {
          if (my === seq.current) setChecking(false);
        });
    }, CHECK_DELAY_MS);
    return () => {
      clearTimeout(timer);
      ctrl.abort();
    };
  }, [text, readOnly]);

  const starter = !!show.data?.starter;
  // A fresh starting sheet has no clips yet; that is not a problem until someone edits it.
  const problems = useMemo(() => (readOnly || (starter && !dirty) ? [] : (check?.problems ?? [])), [check, readOnly, starter, dirty]);
  const errors = problems.filter((p) => p.severity === 'error');
  const warnings = problems.filter((p) => p.severity !== 'error');
  const headRev = data.revisions.find((r) => r.id === head);
  const headMoved = dirty && !!head && startedFrom !== '' && startedFrom !== head;
  const canApply = !readOnly && dirty && !checking && !!check?.valid && !check.noChanges && !checkErr;
  const [saving, setSaving] = useState(false);

  const discard = async () => {
    const ok = await dialog.confirm({
      title: 'Discard your changes?',
      body: 'The script goes back to the saved version. Nothing has been saved yet, so nothing else changes.',
      confirmLabel: 'Discard changes',
      tone: 'danger',
    });
    if (!ok || server === undefined) return;
    setText(server);
    setBase(server);
    setStartedFrom(serverRev);
    setRestored(undefined);
    savePref(draftKey, null);
  };

  const pickRevision = async (id: string) => {
    if (dirty) {
      const ok = await dialog.confirm({
        title: 'Leave your changes?',
        body: 'Opening another version replaces what is in the editor. Your unsaved changes are kept in this browser until you discard or apply them.',
        confirmLabel: 'Open the other version',
      });
      if (!ok) return;
    }
    setText(undefined);
    setBase(undefined);
    onPickRevision(id === head ? undefined : id);
  };

  const apply = async () => {
    if (!check || text === undefined) return;
    const nextNumber = (headRev?.number ?? 0) + 1;
    const message = await dialog.open<string>((close) => <ApplyDialog check={check} nextNumber={nextNumber} onCancel={() => close()} onConfirm={(m) => close(m)} />, {
      title: `Save as version ${nextNumber}?`,
      size: 'md',
    });
    if (message === undefined) return;
    setSaving(true);
    const instruction = message.trim() ? { instruction: message.trim() } : {};
    try {
      const fresh = await call('cut.check', { sheet: text, ...instruction });
      if (!fresh.valid || !fresh.confirmToken) {
        setCheck(fresh);
        toast({ tone: 'error', title: 'Not saved', body: 'Something changed while you were confirming. Look at the problems and try again.' });
        return;
      }
      const res = await call('cut.apply', { sheet: text, confirmToken: fresh.confirmToken, ...instruction });
      savePref(draftKey, null);
      setText(res.sheet);
      setBase(res.sheet);
      setStartedFrom(res.revision.id);
      setRestored(undefined);
      onPickRevision(undefined);
      for (const key of ['project.show', 'project.list', 'revision.list', 'revision.show', 'cut.show', 'cut.compare']) void qc.invalidateQueries({ queryKey: [key] });
      toast({
        tone: 'success',
        title: `Saved as r${res.revision.number}`,
        body: res.rebased ? "Someone saved a newer version meanwhile; your changes don't overlap theirs, so both are kept." : res.changes[0],
      });
    } catch (err) {
      const e = toApiError(err);
      toast({ tone: 'error', title: 'Not saved', body: [e.message, e.hint].filter(Boolean).join(' ') });
    } finally {
      setSaving(false);
    }
  };

  if (show.error) return <ErrorPanel error={show.error} onRetry={() => void show.refetch()} />;
  const loading = show.isLoading || text === undefined;

  return (
    <div className="grid grid-cols-1 gap-5 xl:grid-cols-[minmax(0,1fr)_340px]">
      <section className="panel flex min-w-0 flex-col overflow-hidden" aria-label="Cut sheet">
        <div className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-2.5 sm:px-4">
          <RevisionPicker revisions={data.revisions} value={target} head={head} onChange={(id) => void pickRevision(id)} />
          <SheetStatus readOnly={readOnly} dirty={dirty} checking={checking} check={starter && !dirty ? undefined : check} error={starter && !dirty ? undefined : checkErr} starter={starter} />
          <div className="ml-auto flex items-center gap-2">
            {dirty && !readOnly ? (
              <Button size="sm" variant="ghost" icon={<ArrowCounterClockwise size={13} />} onClick={() => void discard()}>
                Discard
              </Button>
            ) : null}
            {readOnly ? (
              <Button size="sm" variant="secondary" onClick={() => void pickRevision(head ?? '')} disabled={!head}>
                Edit the latest
              </Button>
            ) : (
              <Button size="sm" variant="primary" icon={<CheckCircle size={14} weight="fill" />} disabled={!canApply} loading={saving} onClick={() => void apply()}>
                Apply changes
              </Button>
            )}
          </div>
        </div>
        <Banners readOnly={readOnly} revNumber={show.data?.revision?.number} headNumber={headRev?.number} starter={starter} headMoved={headMoved} restored={restored} rebased={!!check?.rebased} />
        <div className="h-[min(64dvh,720px)] min-h-[340px] xl:h-[min(72dvh,860px)]">
          {loading ? (
            <EditorSkeleton />
          ) : (
            <Suspense fallback={<EditorSkeleton />}>
              <SheetEditor
                value={text}
                readOnly={readOnly}
                problems={problems}
                onChange={setText}
                label={readOnly ? `The script of version ${show.data?.revision?.number ?? ''} (read only)` : 'Script editor'}
                handleRef={editor}
              />
            </Suspense>
          )}
        </div>
      </section>

      <aside className="min-w-0 space-y-4" aria-label="Checking the script">
        {checkErr ? <ErrorPanel compact error={checkErr} title={checkErr.code === 'E_REVISION_CONFLICT' ? 'These changes clash with a newer version' : undefined} /> : null}
        <ProblemList title="Problems" tone="danger" problems={errors} onJump={(p) => editor.current?.focusLine(p.line ?? 1, p.col)} />
        {!readOnly ? <ChangeList check={check} dirty={dirty} checking={checking} /> : null}
        <ProblemList title="Notes" tone="accent" problems={warnings} onJump={(p) => editor.current?.focusLine(p.line ?? 1, p.col)} />
        {check?.anchors.length ? <AnchorList check={check} /> : null}
        <CheatSheet />
      </aside>
    </div>
  );
}

function EditorSkeleton() {
  return (
    <div className="space-y-2.5 p-5" aria-busy="true" aria-label="Loading the script">
      {[38, 22, 30, 64, 52, 0, 70, 44, 58, 0, 66, 40].map((w, i) => (w ? <Skeleton key={i} className="h-3.5" style={{ width: `${w}%` }} /> : <div key={i} className="h-3.5" />))}
    </div>
  );
}

function RevisionPicker({ revisions, value, head, onChange }: { revisions: RevisionSummary[]; value?: string; head?: string; onChange: (id: string) => void }) {
  const id = useId();
  if (!revisions.length) return <span className="text-[12.5px] font-medium text-muted">First cut</span>;
  return (
    <>
      <label htmlFor={id} className="sr-only">
        Version
      </label>
      <select id={id} value={value ?? head ?? ''} onChange={(e) => onChange(e.target.value)} className="h-7 max-w-[220px] rounded-[8px] bg-panel px-2 text-[12.5px] text-fg hairline">
        {revisions.map((r) => (
          <option key={r.id} value={r.id}>
            r{r.number}
            {r.id === head ? ' (latest)' : ''}
            {r.title ? ` · ${r.title}` : ''}
          </option>
        ))}
      </select>
    </>
  );
}

function SheetStatus({ readOnly, dirty, checking, check, error, starter }: { readOnly: boolean; dirty: boolean; checking: boolean; check?: CutCheckOut; error?: ApiError; starter: boolean }) {
  let body: React.ReactNode;
  if (readOnly)
    body = (
      <Badge tone="info" icon={<LockSimple size={11} weight="fill" />}>
        older version, read only
      </Badge>
    );
  else if (checking && dirty) body = <Badge icon={<Spinner size={9} />}>checking</Badge>;
  else if (error) body = <Badge tone="danger">can't save as it is</Badge>;
  else if (!check) body = null;
  else if (!check.valid) {
    const n = check.problems.filter((p) => p.severity === 'error').length;
    body = <Badge tone="danger">{n === 1 ? '1 problem' : `${n} problems`}</Badge>;
  } else if (check.noChanges || !dirty) body = <Badge>{starter ? 'no clips yet' : 'no changes'}</Badge>;
  else {
    const n = check.changes.length;
    body = (
      <Badge tone="ok" icon={<CheckCircle size={11} weight="fill" />}>
        ready, {n === 1 ? '1 change' : `${n} changes`}
      </Badge>
    );
  }
  return (
    <span aria-live="polite" className="inline-flex">
      {body}
    </span>
  );
}

function Banners({ readOnly, revNumber, headNumber, starter, headMoved, restored, rebased }: {
  readOnly: boolean;
  revNumber?: number;
  headNumber?: number;
  starter: boolean;
  headMoved: boolean;
  restored?: number;
  rebased: boolean;
}) {
  const items: { tone: 'info' | 'accent'; icon: React.ReactNode; text: string }[] = [];
  if (readOnly) items.push({ tone: 'info', icon: <ClockCounterClockwise size={14} />, text: `You are looking at version ${revNumber}. Changes always start from the latest version${headNumber ? ` (version ${headNumber})` : ''}.` });
  if (starter && !readOnly)
    items.push({ tone: 'info', icon: <Info size={14} />, text: 'This project has no cut yet. Add numbered clips under ## Clips using the footage names in the settings, then apply to save the first version.' });
  if (restored) items.push({ tone: 'accent', icon: <NotePencil size={14} />, text: `Restored the unsaved changes you made ${relative(restored)}.` });
  if (headMoved)
    items.push({
      tone: 'accent',
      icon: <WarningCircle size={14} />,
      text: rebased
        ? `Someone saved version ${headNumber} while you were editing. Your changes don't overlap theirs, so applying keeps both.`
        : `Someone saved version ${headNumber} while you were editing. Applying checks whether your changes and theirs can be combined.`,
    });
  if (!items.length) return null;
  return (
    <div className="space-y-px border-b border-line">
      {items.map((b, i) => (
        <div key={i} className={cx('flex items-start gap-2 px-4 py-2 text-[12.5px] leading-relaxed', b.tone === 'info' ? 'bg-info-soft text-info' : 'bg-accent-soft text-accent-text')}>
          <span className="mt-[3px] shrink-0">{b.icon}</span>
          <span>{b.text}</span>
        </div>
      ))}
    </div>
  );
}

function ProblemList({ title, tone, problems, onJump }: { title: string; tone: 'danger' | 'accent'; problems: CutProblem[]; onJump: (p: CutProblem) => void }) {
  if (!problems.length) return null;
  return (
    <section className="panel p-4" aria-label={title}>
      <div className={cx('mb-2 flex items-center gap-1.5 text-[13px] font-semibold', tone === 'danger' ? 'text-danger' : 'text-accent-text')}>
        <WarningCircle size={14} weight="fill" /> {title} <span className="tc text-[11.5px] font-normal text-subtle">{problems.length}</span>
      </div>
      <ul className="space-y-1">
        {problems.map((p, i) => (
          <li key={`${p.code}-${p.line}-${p.col}-${i}`}>
            <button type="button" onClick={() => onJump(p)} className="w-full rounded-[10px] px-2.5 py-2 text-left transition-colors hover:bg-hover">
              <div className="flex items-baseline gap-2">
                {p.line ? <span className="tc shrink-0 text-[11px] text-subtle">line {p.line}</span> : null}
                <span className="text-[13px] leading-snug text-fg">{p.message}</span>
              </div>
              {p.hint ? (
                <div className="mt-0.5 text-[12px] leading-relaxed text-muted">
                  <InlineCode text={p.hint} />
                </div>
              ) : null}
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}

function ChangeList({ check, dirty, checking }: { check?: CutCheckOut; dirty: boolean; checking: boolean }) {
  const changes = dirty && check?.valid && !check.noChanges ? check.changes : [];
  return (
    <section className="panel p-4" aria-label="What changes">
      <div className="mb-2 flex items-center gap-2 text-[13px] font-semibold text-fg">
        What changes
        {checking && dirty ? <Spinner size={11} className="text-subtle" label="Checking" /> : null}
      </div>
      {changes.length ? (
        <ul className="space-y-1.5">
          {changes.map((c, i) => (
            <li key={i} className="flex gap-2 text-[13px] leading-relaxed text-muted">
              <span aria-hidden className="mt-[9px] h-1 w-1 shrink-0 rounded-full bg-line-strong" />
              <span>{c}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-[12.5px] leading-relaxed text-subtle">{dirty ? (checking ? 'Checking your edits…' : 'Fix the problems above to see what would change.') : 'Edit the script and the changes appear here in plain words before you apply them.'}</p>
      )}
    </section>
  );
}

function AnchorList({ check }: { check: CutCheckOut }) {
  return (
    <section className="rounded-[16px] p-4 hairline" aria-label="Quoted words">
      <div className="mb-2 flex items-center gap-1.5 text-[13px] font-semibold text-fg">
        <Quotes size={14} weight="fill" className="text-subtle" /> Quoted words
      </div>
      <ul className="space-y-1.5">
        {check.anchors.map((a, i) => (
          <li key={i} className="text-[12.5px] leading-relaxed text-muted">
            <span className="text-fg">“{a.phrase}”</span> {a.field === 'in' ? 'starts' : 'ends'} {a.clipId} at <span className="tc text-accent-text">{clock(a.time)}</span>
            {a.words.toLowerCase() !== a.phrase.toLowerCase() ? <span className="text-subtle"> (heard as “{a.words}”)</span> : null}
          </li>
        ))}
      </ul>
      <p className="mt-2 text-[11.5px] text-subtle">After you apply, the script shows these exact times.</p>
    </section>
  );
}

function clock(s: number): string {
  const m = Math.floor(s / 60);
  const rest = s - m * 60;
  const sec = rest.toFixed(3).replace(/\.?0+$/, '');
  return `${m}:${rest < 10 ? '0' : ''}${sec}`;
}

function CheatSheet() {
  const rows: [string, string][] = [
    ['1. hook: interview-a 0:11.6 → 0:18.4', 'A clip: its name, the footage, where it starts and ends.'],
    ['from "these words" to "those words"', 'Start and end on spoken words instead of times.'],
    ['↳ dissolve 0.4 s', 'How the next clip comes in: cut, fade in, dissolve, fade through black or white, or a wipe, slide, push, zoom or whip (slide left 0.4 s).'],
    ["↳ cut · Sam's voice starts 0.4 s early", 'The sound at the cut: a voice that starts early, one that carries over (voice carries 0.3 s), or a change in the nearest pause.'],
    ['· muted · volume 80% · speed 1.5×', 'Clip settings go after the times.'],
    ['Why: …', 'The reason for a clip. Plain lines under a clip are your notes.'],
    ['> Ada: “So what would you build?”', 'Written by MetaRush: the words spoken and who says them. Edits here are ignored.'],
    ['- intro: "When I started…" · 0.5 s after hook starts', 'A narration line under ## Voiceover: its words and where it starts (when a shot starts or ends, after the previous line, or at a time).'],
    ['mix:\n  music: -6 dB\n  camera sound: off', 'Sound levels, in the settings at the top: also clip sound, narration and sound effects, how far music dips under voices, evening out voices from clip to clip, and how loud the whole video is.'],
  ];
  return (
    <details className="group rounded-[16px] p-4 hairline">
      <summary className="cursor-pointer list-none text-[13px] font-semibold text-fg marker:hidden">
        How to write the script <span className="ml-1 text-[11.5px] font-normal text-subtle group-open:hidden">show</span>
      </summary>
      <dl className="mt-3 space-y-2.5">
        {rows.map(([code, what]) => (
          <div key={code}>
            <dt className="tc whitespace-pre-wrap break-words text-[12px] text-accent-text">{code}</dt>
            <dd className="text-[12px] leading-relaxed text-muted">{what}</dd>
          </div>
        ))}
      </dl>
    </details>
  );
}

function ApplyDialog({ check, nextNumber, onCancel, onConfirm }: { check: CutCheckOut; nextNumber: number; onCancel: () => void; onConfirm: (message: string) => void }) {
  const [message, setMessage] = useState('');
  const id = useId();
  const notes = check.problems.filter((p) => p.severity !== 'error');
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        onConfirm(message);
      }}
    >
      <p className="text-[13.5px] leading-relaxed text-muted">This saves version {nextNumber}. The current one stays in the history, so nothing is lost.</p>
      <ul className="mt-3 max-h-[34dvh] space-y-1.5 overflow-y-auto rounded-[12px] bg-raised p-3 hairline">
        {check.changes.map((c, i) => (
          <li key={i} className="text-[13px] leading-relaxed text-fg">
            {c}
          </li>
        ))}
      </ul>
      {notes.length ? (
        <p className="mt-3 text-[12.5px] leading-relaxed text-accent-text">
          {notes.length === 1 ? '1 note' : `${notes.length} notes`} to look at (see the list next to the script). You can still save.
        </p>
      ) : null}
      {check.rebased ? <p className="mt-2 text-[12.5px] leading-relaxed text-info">Someone saved a newer version meanwhile; your changes don't overlap theirs, so both are kept.</p> : null}
      <div className="mt-4">
        <label htmlFor={id} className="text-[12.5px] font-medium text-muted">
          What did you change? (optional)
        </label>
        <input
          id={id}
          data-autofocus
          value={message}
          maxLength={400}
          onChange={(e) => setMessage(e.target.value)}
          placeholder="Ended the opening on the garage line"
          className="mt-1.5 h-10 w-full rounded-[10px] bg-panel px-3 text-[14px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
        />
        <div className="mt-1.5 text-[12px] text-subtle">Shown in the version history next to version {nextNumber}.</div>
      </div>
      <div className="mt-5 flex flex-wrap justify-end gap-2">
        <Button variant="ghost" onClick={onCancel}>
          Keep editing
        </Button>
        <Button variant="primary" type="submit" icon={<CheckCircle size={15} weight="fill" />}>
          Save as r{nextNumber}
        </Button>
      </div>
    </form>
  );
}

/* ---------- comparing ---------- */

function CompareSheets({ data, params, setParam }: { data: ProjectShowOut; params: URLSearchParams; setParam: (p: Record<string, string | undefined>) => void }) {
  const revs = data.revisions;
  const left = params.get('left') ?? revs[1]?.id ?? '';
  const right = params.get('right') ?? revs[0]?.id ?? '';
  const valid = !!left && !!right && left !== right;
  const cmp = useOp('cut.compare', valid ? { projectId: data.project.id, left, right } : null);
  const leftId = useId();
  const rightId = useId();
  if (revs.length < 2)
    return <div className="rounded-[14px] px-4 py-6 text-[13px] text-muted hairline">Comparing needs at least two versions of the cut. Apply a change and come back.</div>;
  const select = (id: string, label: string, value: string, key: 'left' | 'right') => (
    <div className="flex min-w-0 flex-col gap-1">
      <label htmlFor={id} className="text-[11.5px] font-medium text-subtle">
        {label}
      </label>
      <select id={id} value={value} onChange={(e) => setParam({ [key]: e.target.value })} className="h-9 w-full min-w-0 rounded-[10px] bg-panel px-3 text-[13px] text-fg hairline sm:w-[240px]">
        {revs.map((r) => (
          <option key={r.id} value={r.id}>
            r{r.number}
            {r.isHead ? ' (latest)' : ''}
            {r.instruction ? ` · ${r.instruction.slice(0, 48)}` : ''}
          </option>
        ))}
      </select>
    </div>
  );
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-end gap-3">
        {select(leftId, 'Before', left, 'left')}
        <Button variant="ghost" size="md" icon={<ArrowsLeftRight size={15} />} onClick={() => setParam({ left: right, right: left })} aria-label="Swap before and after">
          Swap
        </Button>
        {select(rightId, 'After', right, 'right')}
      </div>
      {!valid ? (
        <div className="rounded-[14px] px-4 py-6 text-[13px] text-muted hairline">Pick two different versions.</div>
      ) : cmp.error ? (
        <ErrorPanel error={cmp.error} onRetry={() => void cmp.refetch()} />
      ) : !cmp.data ? (
        <div className="space-y-3">
          <Skeleton className="h-24 rounded-[14px]" />
          <Skeleton className="h-72 rounded-[14px]" />
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-5 xl:grid-cols-[340px_minmax(0,1fr)]">
          <section className="panel h-fit p-4" aria-label="What changed">
            <div className="mb-2 text-[13px] font-semibold text-fg">
              r{cmp.data.left.number} to r{cmp.data.right.number}
            </div>
            {cmp.data.right.instruction ? <p className="mb-3 text-[12.5px] leading-relaxed text-muted">Asked for: “{cmp.data.right.instruction}”</p> : null}
            <ul className="space-y-1.5">
              {cmp.data.changes.map((c, i) => (
                <li key={i} className="flex gap-2 text-[13px] leading-relaxed text-muted">
                  <span aria-hidden className="mt-[9px] h-1 w-1 shrink-0 rounded-full bg-line-strong" />
                  <span>{c}</span>
                </li>
              ))}
            </ul>
          </section>
          <SheetDiff lines={cmp.data.lines} />
        </div>
      )}
    </div>
  );
}
