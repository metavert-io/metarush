import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { CheckCircle, ClipboardText, CookingPot, HandPalm, MusicNotes, SealCheck, TextAa, UserCircle, UserFocus, UsersThree, X } from '@phosphor-icons/react';
import { toApiError } from '../api/client';
import { useOp, useOpMutation } from '../api/queries';
import type { OpInput, Todo, TodoKind } from '../api/types';
import { useHotkeys } from '../lib/hooks';
import { pct } from '../lib/speakers';
import { resetSelection } from '../lib/workbench';
import { stopSnippet } from '../components/SnippetPlayer';
import { PersonPicker, type PersonChoice } from '../components/PersonPicker';
import { SampleTile, stopSampleTiles } from '../components/SampleTile';
import { FaceTile } from '../components/FaceTile';
import { confirmSamePerson, useSpeakerActions } from '../components/speakerActions';
import { FaceBulkBar, FaceGridKeys, FaceGroupTile, ShownNamesBar } from '../components/FaceGroups';
import { RunTodoActions } from '../components/recipes/TodoActions';
import { sourceLink } from '../layout/SearchPalette';
import { useDialog } from '../ui/Dialog';
import { Button, cx, EmptyState, ErrorPanel, Freshness, Kbd, Skeleton, Spinner, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';

type KindFilter = 'all' | 'speakers' | 'faces' | 'music' | 'brief' | 'people' | 'approval' | 'spelling' | 'runs' | 'gate';
const KIND_GROUPS: Record<Exclude<KindFilter, 'all'>, { label: string; kinds: TodoKind[] }> = {
  speakers: { label: 'Who is speaking', kinds: ['speaker_unnamed', 'speaker_match'] },
  faces: { label: "Who's on screen", kinds: ['face_group', 'face_person'] },
  music: { label: 'Music', kinds: ['music_rights'] },
  brief: { label: 'Project brief', kinds: ['brief_missing'] },
  people: { label: 'Same person?', kinds: ['duplicate_person'] },
  approval: { label: 'Ready to approve', kinds: ['revision_approval', 'remotion_approval', 'ability_approval'] },
  spelling: { label: 'Spelling', kinds: ['spelling'] },
  runs: { label: 'Runs that wait', kinds: ['recipe_run'] },
  gate: { label: 'Before it goes out', kinds: ['ship_gate'] },
};

function kindIcon(kind: TodoKind) {
  switch (kind) {
    case 'speaker_unnamed':
    case 'speaker_match':
      return <UserCircle size={22} />;
    case 'music_rights':
      return <MusicNotes size={22} />;
    case 'brief_missing':
      return <ClipboardText size={22} />;
    case 'duplicate_person':
      return <UsersThree size={22} />;
    case 'face_person':
    case 'face_group':
      return <UserFocus size={22} />;
    case 'spelling':
      return <TextAa size={22} />;
    case 'recipe_run':
      return <CookingPot size={22} />;
    case 'ship_gate':
      return <HandPalm size={22} />;
    default:
      return <SealCheck size={22} />;
  }
}

/**
 * Questions MetaRush can't answer by itself. Each answer is applied right away
 * (names a voice everywhere, records music rights, fills the brief…).
 * Keyboard: j/k move, 1–9 pick an answer, Enter type one, s skip, x select.
 */
export function TodosPage() {
  const [params, setParams] = useSearchParams();
  const projectId = params.get('project') ?? undefined;
  const assetId = params.get('asset') ?? undefined;
  const kind = (params.get('kind') as KindFilter | null) ?? 'all';
  useEffect(() => resetSelection({}), []);
  const list = useOp('todos.list', { ...(projectId ? { projectId } : {}), ...(assetId ? { assetId } : {}) });
  // Who is on screen: one question per person per clip, as a grid (D155–D157).
  const groups = useOp('todos.groups', { ...(projectId ? { projectId } : {}), ...(assetId ? { assetId } : {}) });
  const projects = useOp('project.list', {});
  const all = list.data?.todos ?? [];
  const faceQs = useMemo(() => (kind === 'all' || kind === 'faces' ? (groups.data?.groups ?? []) : []), [groups.data, kind]);
  const others = useMemo(() => all.filter((t) => t.kind !== 'face_group' && (kind === 'all' || KIND_GROUPS[kind].kinds.includes(t.kind))), [all, kind]);
  const shown = useMemo(() => [...faceQs, ...others], [faceQs, others]);
  const [active, setActive] = useState(0);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const cards = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (active >= shown.length) setActive(Math.max(0, shown.length - 1));
  }, [shown.length, active]);
  useEffect(() => {
    cards.current?.querySelector<HTMLElement>(`[data-todo-index="${active}"]`)?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }, [active]);
  useEffect(
    () => () => {
      stopSnippet();
      stopSampleTiles();
    },
    [],
  );

  const setFilter = (k: string, v: string | undefined) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v);
    else next.delete(k);
    setParams(next, { replace: true });
  };

  const next = (e?: KeyboardEvent) => {
    e?.preventDefault();
    setActive((i) => Math.min(shown.length - 1, i + 1));
  };
  const prev = (e?: KeyboardEvent) => {
    e?.preventDefault();
    setActive((i) => Math.max(0, i - 1));
  };
  useHotkeys({
    j: () => next(),
    k: () => prev(),
    ArrowDown: next,
    ArrowRight: next,
    ArrowUp: prev,
    ArrowLeft: prev,
    x: () => {
      const t = shown[active];
      if (!t) return;
      setSelected((s) => {
        const n = new Set(s);
        if (n.has(t.id)) n.delete(t.id);
        else n.add(t.id);
        return n;
      });
    },
  });

  const projectName = new Map((projects.data?.projects ?? []).map((p) => [p.id, p.name]));
  const counts = list.data?.counts;
  const assetCtx = assetId ? all.find((t) => t.assetId === assetId)?.context : undefined;
  const assetName = assetCtx ? assetCtx.assetTitle || assetCtx.assetName : undefined;

  return (
    <div className="mx-auto max-w-[1100px]">
      <div className="mb-6 flex flex-wrap items-end gap-3">
        <div className="min-w-0">
          <h1 className="text-[28px] font-semibold tracking-[-0.03em]">Questions for you</h1>
          <p className="mt-1 max-w-[62ch] text-[14px] text-muted">Things we can't work out on our own. Each answer is used right away, in every video it applies to.</p>
        </div>
        <div className="ml-auto flex items-center gap-3">
          <Link to="/library/people" className="inline-flex items-center gap-1.5 rounded-[8px] px-2 py-1 text-[12.5px] font-medium text-accent-text hover:bg-accent-soft">
            <UsersThree size={14} /> People you named
          </Link>
          <Freshness at={list.dataUpdatedAt || undefined} fetching={list.isFetching} onRefresh={() => void list.refetch()} />
        </div>
      </div>

      <div className="mb-5 flex flex-wrap items-center gap-2">
        <div className="no-scrollbar flex gap-1 overflow-x-auto" role="group" aria-label="Kind of question">
          <Chip active={kind === 'all'} onClick={() => setFilter('kind', undefined)} count={counts?.total}>
            All
          </Chip>
          {(Object.keys(KIND_GROUPS) as Exclude<KindFilter, 'all'>[]).map((k) => {
            const n = KIND_GROUPS[k].kinds.reduce((s, x) => s + (counts?.byKind[x] ?? 0), 0);
            if (!n && kind !== k) return null;
            return (
              <Chip key={k} active={kind === k} onClick={() => setFilter('kind', k)} count={n}>
                {KIND_GROUPS[k].label}
              </Chip>
            );
          })}
        </div>
        <label className="ml-auto flex items-center gap-2 text-[12.5px] text-muted">
          <span>Project</span>
          <select
            value={projectId ?? ''}
            onChange={(e) => setFilter('project', e.target.value || undefined)}
            className="h-8 max-w-[220px] rounded-[8px] bg-panel px-2 text-[12.5px] text-fg hairline"
          >
            <option value="">All projects</option>
            {(projects.data?.projects ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
                {counts?.byProject[p.id] ? ` (${counts.byProject[p.id]})` : ''}
              </option>
            ))}
          </select>
        </label>
        {assetId ? (
          <button type="button" onClick={() => setFilter('asset', undefined)} className="inline-flex h-8 items-center gap-1.5 rounded-full bg-accent-soft px-3 text-[12.5px] text-accent-text">
            Only “{assetName ?? 'this video'}” <X size={12} />
          </button>
        ) : null}
      </div>

      {selected.size > 0 ? (
        <BulkBar todos={shown.filter((t) => selected.has(t.id))} total={shown.length} onSelectAll={() => setSelected(new Set(shown.map((t) => t.id)))} onDone={() => setSelected(new Set())} />
      ) : null}

      {list.isLoading ? (
        <div className="space-y-3">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-36 rounded-[16px]" />
          ))}
        </div>
      ) : list.error ? (
        <ErrorPanel error={list.error} onRetry={() => void list.refetch()} />
      ) : shown.length === 0 ? (
        <div className="panel">
          <EmptyState
            icon={<CheckCircle size={22} />}
            title="Nothing to answer right now"
            body={kind !== 'all' || projectId || assetId ? 'Nothing here with these filters.' : 'When MetaRush hears a voice it does not know, finds music without a license, or needs to know what a project is for, it asks here.'}
          />
        </div>
      ) : (
        <div ref={cards}>
          {faceQs.length > 0 ? (
            <section aria-label="Who's on screen" className="mb-6" data-face-grid>
              <div className="mb-1 flex flex-wrap items-baseline gap-x-3">
                <h2 className="text-[15px] font-semibold tracking-[-0.01em]">Who's on screen</h2>
                {faceQs.length > 1 ? (
                  <button
                    type="button"
                    className="text-[12.5px] font-medium text-accent-text hover:underline"
                    onClick={() => setSelected((sel) => new Set([...sel, ...faceQs.map((t) => t.id)]))}
                    data-select-all-faces
                  >
                    Select all {faceQs.length}
                  </button>
                ) : null}
              </div>
              <p className="mb-3 max-w-[70ch] text-[12.5px] text-muted">One question per person in each clip. One answer names all of their faces there, and MetaRush learns what they look like.</p>
              <ShownNamesBar clips={groups.data?.shown ?? []} />
              <FaceBulkBar questions={faceQs} projectId={projectId} assetId={assetId} />
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {faceQs.map((t, i) => (
                  <FaceGroupTile
                    key={t.id}
                    todo={t}
                    index={i}
                    active={i === active}
                    onActivate={() => setActive(i)}
                    others={faceQs.filter((o) => o.id !== t.id && o.assetId === t.assetId)}
                    selected={selected.has(t.id)}
                    onSelect={(on) =>
                      setSelected((sel) => {
                        const n = new Set(sel);
                        if (on) n.add(t.id);
                        else n.delete(t.id);
                        return n;
                      })
                    }
                  />
                ))}
              </div>
              <FaceGridKeys />
            </section>
          ) : null}
          {others.length > 0 && faceQs.length > 0 ? <h2 className="mb-3 text-[15px] font-semibold tracking-[-0.01em]">Other questions</h2> : null}
          <div className="space-y-3">
            {others.map((t, k) => {
              const i = faceQs.length + k;
              return (
                <TodoCard
                  key={t.id}
                  todo={t}
                  index={i}
                  active={i === active}
                  onActivate={() => setActive(i)}
                  selected={selected.has(t.id)}
                  onSelect={(on) =>
                    setSelected((s) => {
                      const n = new Set(s);
                      if (on) n.add(t.id);
                      else n.delete(t.id);
                      return n;
                    })
                  }
                  projectName={t.projectIds[0] ? projectName.get(t.projectIds[0]) : undefined}
                />
              );
            })}
          </div>
        </div>
      )}
      {others.length > 0 ? (
        <p className="mt-5 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-[12px] text-subtle">
          <span className="inline-flex items-center gap-1">
            <Kbd>j</Kbd>
            <Kbd>k</Kbd> move
          </span>
          <span className="inline-flex items-center gap-1">
            <Kbd>1</Kbd>…<Kbd>9</Kbd> pick an answer
          </span>
          <span className="inline-flex items-center gap-1">
            <Kbd>↵</Kbd> type an answer
          </span>
          <span className="inline-flex items-center gap-1">
            <Kbd>p</Kbd> listen
          </span>
          <span className="inline-flex items-center gap-1">
            <Kbd>s</Kbd> skip
          </span>
          <span className="inline-flex items-center gap-1">
            <Kbd>x</Kbd> select for answering together
          </span>
        </p>
      ) : null}
    </div>
  );
}

function Chip({ active, onClick, count, children }: { active: boolean; onClick: () => void; count?: number; children: React.ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cx('inline-flex h-8 shrink-0 items-center gap-1.5 rounded-full px-3 text-[12.5px] transition-colors hairline', active ? 'bg-panel text-fg' : 'text-muted hover:text-fg')}
    >
      {children}
      {count ? <span className="tc text-[11px] text-subtle">{count}</span> : null}
    </button>
  );
}

type AnswerInput = Omit<OpInput<'todos.answer'>, 'id'>;

function useAnswer() {
  const toast = useToast();
  const dialog = useDialog();
  const speakers = useSpeakerActions();
  const answer = useOpMutation('todos.answer', { invalidate: ['todos.list', 'todos.counts', 'todos.groups', 'footage.speakers', 'footage.transcript', 'people.list', 'people.show', 'notification.list', 'project.show', 'revision.list'] });
  const dismiss = useOpMutation('todos.dismiss', { invalidate: ['todos.list', 'todos.counts', 'todos.groups'] });
  return {
    /**
     * Answers a question. Two voices of one clip only become one person after
     * the person confirms it (quiet bulk answers never ask: they report it).
     * Resolves false when nothing was saved.
     */
    answer: async (t: Todo, v: AnswerInput, quiet = false): Promise<boolean> => {
      try {
        let out;
        let samePerson = false;
        for (let round = 0; !out; round++) {
          try {
            out = await answer.mutateAsync({ id: t.id, ...v, ...(samePerson ? { samePerson: true } : {}) });
          } catch (err) {
            const e = toApiError(err);
            if (e.code !== 'E_CONFIRM_REQUIRED' || quiet || round >= 2) throw err;
            // Hear what it is compared with, and fix an earlier mistake in place (D158).
            const ok = await confirmSamePerson(dialog, (e.details ?? {}) as Parameters<typeof confirmSamePerson>[1]);
            if (!ok) return false;
            samePerson = ok === 'same';
          }
        }
        if (!quiet) speakers.announce(t.assetId, out.done, out.separate, out.autoMerged);
        return true;
      } catch (err) {
        const e = toApiError(err);
        toast({ tone: 'error', title: 'Could not save that answer', body: [e.message, e.hint].filter(Boolean).join(' ') });
        return false;
      }
    },
    dismiss: (id: string, quiet = false) =>
      dismiss.mutateAsync({ id }).then(
        () => {
          if (!quiet) toast({ tone: 'info', title: "Skipped. We won't ask again." });
          return true;
        },
        (e: Error) => {
          toast({ tone: 'error', title: 'Could not skip', body: e.message });
          return false;
        },
      ),
    busy: answer.isPending || dismiss.isPending,
  };
}

function TodoCard({ todo: t, index, active, onActivate, selected, onSelect, projectName }: {
  todo: Todo;
  index: number;
  active: boolean;
  onActivate: () => void;
  selected: boolean;
  onSelect: (on: boolean) => void;
  projectName?: string;
}) {
  const { answer, dismiss, busy } = useAnswer();
  const actions = useSpeakerActions();
  const [text, setText] = useState('');
  const input = useRef<HTMLInputElement>(null);
  const root = useRef<HTMLDivElement>(null);
  const speaker = t.kind === 'speaker_unnamed' || t.kind === 'speaker_match';
  // A run of Recipes that waits: Go on and Stop are the run's own buttons (they say what it costs).
  const run = t.kind === 'recipe_run';
  // "Is this Maya?" about a face: the face itself, and a name picker to say who it is instead.
  const face = t.kind === 'face_person' && t.assetId && t.context.frameAt != null && (t.context.box?.length ?? 0) === 4;
  const picksPerson = speaker || t.kind === 'face_person';
  const people = useOp('people.list', {}, { enabled: speaker, staleTime: 60_000 });
  const samples = t.context.samples ?? [];
  const submit = (v: AnswerInput) => void answer(t, v);
  const pickPerson = (c: PersonChoice) => {
    if (c.kind === 'person') submit({ answer: c.person.name, personId: c.person.id });
    else if (c.kind === 'new') submit({ answer: c.name, newPerson: true });
  };
  const answerId = `ans-${t.id}`;
  const hasVideo = t.context.frameAt !== undefined;
  const voiceName = t.context.speaker ?? 'This voice';
  const title = t.context.assetTitle || t.context.assetName;
  // "Is this Maya?": a moment you named as Maya elsewhere, to compare.
  const known = t.kind === 'speaker_match' && t.context.personId ? (people.data?.people ?? []).find((p) => p.id === t.context.personId) : undefined;
  const reference = known ? (known.samples.find((s) => s.source !== 'auto' && s.assetId !== t.assetId) ?? known.samples.find((s) => s.source !== 'auto')) : undefined;

  useHotkeys(
    {
      Enter: (e) => {
        // Enter on a focused button or link keeps its normal meaning.
        if (!t.freeText || (e.target instanceof HTMLElement && e.target.closest('button,a,summary,label'))) return;
        e.preventDefault();
        (input.current ?? document.getElementById(answerId))?.focus();
      },
      s: () => void dismiss(t.id),
      p: () => root.current?.querySelector<HTMLButtonElement>('[data-sample-tile]')?.click(),
      ...(run ? {} : Object.fromEntries(t.suggestions.slice(0, 9).map((_, i) => [String(i + 1), () => submit({ suggestion: i + 1 })]))),
    },
    active && !busy,
  );

  return (
    <div
      ref={root}
      data-todo-index={index}
      data-todo-kind={t.kind}
      onClick={onActivate}
      onFocusCapture={onActivate}
      className={cx(
        'fade-in grid grid-cols-1 gap-4 rounded-[16px] bg-raised p-4 transition-shadow sm:grid-cols-[auto_1fr]',
        active ? 'ring-1 ring-accent-line shadow-[var(--shadow-soft)]' : 'hairline',
      )}
      aria-current={active || undefined}
    >
      <div className="flex items-start gap-3 sm:flex-col">
        <label className="flex items-center gap-2 text-[12px] text-subtle sm:order-last">
          <input type="checkbox" checked={selected} onChange={(e) => onSelect(e.target.checked)} className="h-4 w-4 accent-[var(--accent)]" />
          <span className="sm:sr-only">Select</span>
        </label>
        {face ? (
          <Link to={sourceLink(t.assetId!, t.projectIds[0], t.context.frameAt ?? undefined, undefined, 'onscreen')} onClick={(e) => e.stopPropagation()} aria-label="Open the clip at this face">
            <FaceTile assetId={t.assetId!} at={t.context.frameAt!} box={t.context.box!} size={120} label={`The face in ${title ?? 'this clip'}`} />
          </Link>
        ) : speaker && t.assetId && samples[0] ? (
          <SampleTile assetId={t.assetId} from={samples[0].from} to={samples[0].to} hasVideo={hasVideo} label={`${voiceName} in ${title ?? 'this video'}`} size="md" />
        ) : speaker ? (
          <div className="grid h-[90px] w-[160px] place-items-center rounded-[12px] bg-panel text-accent-text hairline" aria-hidden>
            <UserCircle size={26} />
          </div>
        ) : (
          <div className="grid h-16 w-16 place-items-center rounded-[12px] bg-panel text-accent-text hairline" aria-hidden>
            {kindIcon(t.kind)}
          </div>
        )}
      </div>
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px] text-subtle">
          {projectName || t.context.projectName ? <Theirs>{projectName ?? t.context.projectName}</Theirs> : null}
          {t.context.assetTitle || t.context.assetName ? (
            <>
              {projectName || t.context.projectName ? <span aria-hidden>·</span> : null}
              {t.assetId ? (
                <Link to={sourceLink(t.assetId, t.projectIds[0], samples[0]?.from)} className="truncate hover:text-fg hover:underline" onClick={(e) => e.stopPropagation()}>
                  <Theirs>{t.context.assetTitle || t.context.assetName}</Theirs>
                </Link>
              ) : (
                <Theirs className="truncate">{t.context.assetTitle || t.context.assetName}</Theirs>
              )}
            </>
          ) : null}
        </div>
        <h2 className="mt-1 text-[16px] font-semibold tracking-[-0.01em] text-fg">{t.question}</h2>
        {t.detail ? <p className="mt-1 max-w-[70ch] text-[13px] leading-relaxed text-muted">{t.detail}</p> : null}
        {run ? <RunTodoActions todo={t} /> : null}
        {t.context.link && !run ? (
          <Link to={t.context.link} className="mt-1.5 inline-block text-[13px] font-medium text-accent-text hover:underline" onClick={(e) => e.stopPropagation()} data-todo-look>
            Look at it first
          </Link>
        ) : null}
        {speaker && t.assetId && (samples.length > 1 || reference) ? (
          <div className="mt-3 flex flex-wrap items-start gap-x-5 gap-y-3">
            {samples.length > 1 ? (
              <div role="group" aria-label={`More of ${voiceName}`}>
                <p className="mb-1.5 text-[11.5px] font-medium text-subtle">More of this voice</p>
                <div className="flex flex-wrap gap-2">
                  {samples.slice(1, 3).map((s, i) => (
                    <SampleTile key={i} assetId={t.assetId!} from={s.from} to={s.to} hasVideo={hasVideo} label={`${voiceName}, clip ${i + 2}`} size="sm" />
                  ))}
                </div>
              </div>
            ) : null}
            {reference && known ? (
              <div role="group" aria-label={`${known.name}, to compare`} data-compare>
                <p className="mb-1.5 text-[11.5px] font-medium text-subtle">{known.name}, as you named them</p>
                <SampleTile assetId={reference.assetId} from={reference.from} to={reference.to} title={reference.assetTitle} hasVideo={reference.hasVideo} label={`${known.name} in ${reference.assetTitle}`} size="sm" />
              </div>
            ) : null}
          </div>
        ) : null}
        {t.suggestions.length > 0 && !run ? (
          <div className="mt-3 flex flex-wrap gap-2" role="group" aria-label="Suggested answers">
            {t.suggestions.map((s, i) => (
              <Button
                key={i}
                size="md"
                variant={i === 0 && t.kind !== 'speaker_unnamed' ? 'primary' : 'secondary'}
                disabled={busy}
                onClick={(e) => {
                  e.stopPropagation();
                  submit({ suggestion: i + 1 });
                }}
                trailing={i < 9 ? <span className={cx('tc ml-1 rounded-[4px] px-1 text-[10.5px]', i === 0 && t.kind !== 'speaker_unnamed' ? 'bg-black/15' : 'bg-hover text-subtle')}>{i + 1}</span> : undefined}
              >
                {s.label}
              </Button>
            ))}
          </div>
        ) : null}
        <div className="mt-3 flex flex-wrap items-start gap-2">
          {picksPerson && t.freeText ? (
            <div className="min-w-[240px] flex-1" onClick={(e) => e.stopPropagation()}>
              <PersonPicker
                id={answerId}
                label={t.question}
                value={text}
                onValueChange={setText}
                onPick={pickPerson}
                placeholder={t.kind === 'speaker_match' || t.kind === 'face_person' ? 'Someone else? Type their name' : 'Type their name'}
                onEscape={() => document.getElementById(answerId)?.blur()}
              />
            </div>
          ) : t.freeText ? (
            <form
              className="flex min-w-[240px] flex-1 items-center gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                if (text.trim()) submit({ answer: text.trim() });
              }}
            >
              <label htmlFor={`ans-${t.id}`} className="sr-only">
                Your answer
              </label>
              <input
                ref={input}
                id={answerId}
                value={text}
                onChange={(e) => setText(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Escape') (e.target as HTMLInputElement).blur();
                }}
                maxLength={500}
                placeholder={t.kind === 'music_rights' ? 'Where is it from, or what license?' : 'Your answer'}
                className="h-9 min-w-0 flex-1 rounded-[10px] bg-panel px-3 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
              />
              <Button type="submit" variant={t.suggestions.length ? 'secondary' : 'primary'} disabled={busy || !text.trim()}>
                Save
              </Button>
            </form>
          ) : null}
          {t.kind === 'speaker_match' && t.assetId && t.context.personName ? (
            <Button
              variant="ghost"
              disabled={busy}
              onClick={(e) => {
                e.stopPropagation();
                void actions.notThem(t.assetId!, t.context.label ?? '', { id: t.context.personId, name: t.context.personName! });
              }}
            >
              Not {t.context.personName}
            </Button>
          ) : null}
          {run ? null : (
            <Button variant="ghost" disabled={busy} onClick={() => void dismiss(t.id)} className="ml-auto">
              Skip
            </Button>
          )}
          {busy ? <Spinner size={14} className="text-subtle" label="Saving" /> : null}
        </div>
        {t.kind === 'speaker_match' && t.context.score ? (
          <p className="mt-2 text-[11.5px] text-subtle">Voice match {pct(t.context.score)}%: how alike the voices sound, not a guarantee.</p>
        ) : null}
      </div>
    </div>
  );
}

function BulkBar({ todos, total, onSelectAll, onDone }: { todos: Todo[]; total: number; onSelectAll: () => void; onDone: () => void }) {
  const { answer, dismiss, busy } = useAnswer();
  const toast = useToast();
  // A face question's first answer counts only when it suggests someone (a
  // quick pick on "Who is this?" is not a suggestion, D189).
  const withSuggestion = todos.filter(
    (t) => t.suggestions.length > 0 && t.kind !== 'revision_approval' && t.kind !== 'remotion_approval' && t.kind !== 'ability_approval' && t.kind !== 'recipe_run' && t.kind !== 'ship_gate' && (t.kind !== 'face_group' || !!t.context.personName),
  );
  const run = async (fn: (t: Todo) => Promise<boolean>, list: Todo[], verb: string) => {
    let ok = 0;
    for (const t of list) if (await fn(t)) ok++;
    toast({ tone: ok === list.length ? 'success' : 'error', title: `${verb} ${ok} of ${list.length}` });
    onDone();
  };
  return (
    <div className="sticky top-[calc(var(--mr-header-h)+7px)] z-20 mb-4 flex flex-wrap items-center gap-2 rounded-[14px] bg-panel px-4 py-2.5 shadow-[var(--shadow-soft)] hairline" role="region" aria-label="Answer selected questions together">
      <span className="text-[13px] font-medium">{todos.length} selected</span>
      {todos.length < total ? (
        <button type="button" className="text-[12.5px] font-medium text-accent-text hover:underline" onClick={onSelectAll} data-select-all>
          Select all {total}
        </button>
      ) : null}
      <div className="ml-auto flex flex-wrap gap-2">
        <Button size="sm" variant="primary" disabled={busy || withSuggestion.length === 0} onClick={() => void run((t) => answer(t, { suggestion: 1 }, true), withSuggestion, 'Answered')}>
          Use the first answer ({withSuggestion.length})
        </Button>
        <Button size="sm" variant="ghost" disabled={busy} onClick={() => void run((t) => dismiss(t.id, true), todos, 'Skipped')}>
          Skip all
        </Button>
        <Button size="sm" variant="ghost" onClick={onDone} aria-label="Clear selection">
          <X size={13} />
        </Button>
      </div>
    </div>
  );
}
