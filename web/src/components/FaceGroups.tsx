import { useMemo, useRef, useState } from 'react';
import { Link } from 'react-router';
import { ArrowsMerge, Check, Eye, EyeSlash, FilmSlate, Info, Microphone, Sparkle, TextT, UserFocus, UserMinus, X } from '@phosphor-icons/react';
import { toApiError } from '../api/client';
import { useOp, useOpMutation } from '../api/queries';
import type { RemoteOp, ShownClip, Todo, TodoEvidence } from '../api/types';
import { fold as foldName } from '../lib/names';
import { useHotkeys } from '../lib/hooks';
import { sourceLink } from '../layout/SearchPalette';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, cx, Kbd, Spinner, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { FaceTile } from './FaceTile';
import { PersonPicker, type PersonChoice } from './PersonPicker';

/*
 * Who is on screen, one question per person per clip (D155–D157): a grid of
 * tiles, each with a few sample faces, how often they are seen, the suggested
 * name and why (what the video shows or says, the voice, the looks), Yes, the
 * person picker, Same as… and Skip. One answer names all of that person's
 * faces in the clip. Answers for many at once (D189): "Not Lena in this
 * clip", "Skip all in this clip", and above the grid "None of these are
 * Lena" and "Skip all in this project". Keyboard: j/k (or the arrow keys)
 * move, 1 says yes, n says no, Enter types a name, s skips, c says the person
 * isn't anywhere in the clip, S skips the whole clip, x selects.
 */

const INVALIDATE: RemoteOp[] = [
  'todos.list',
  'todos.counts',
  'todos.groups',
  'footage.faces',
  'footage.visuals',
  'footage.speakers',
  'people.list',
  'people.show',
  'people.evidence',
  'notification.list',
];

function evidenceIcon(e: TodoEvidence) {
  switch (e.kind) {
    case 'shown':
      return <TextT size={12} />;
    case 'said':
    case 'voice':
      return <Microphone size={12} />;
    case 'looks':
      return <Eye size={12} />;
  }
  return <Sparkle size={12} />;
}

function seen(t: Todo): string {
  const n = t.context.appearances ?? 1;
  return n === 1 ? 'Seen once' : n === 2 ? 'Seen twice' : `Seen ${n} times`;
}

/**
 * Who a tile is about: the person it suggests, else its first quick pick that
 * some evidence points at (the voice heard, a name said) and is someone in People.
 */
export function tileWho(t: Todo): { id?: string; name: string } | undefined {
  if (t.context.personName) return { id: t.context.personId || undefined, name: t.context.personName };
  const pick = t.suggestions.find((s) => s.personId && (s.basis?.length ?? 0) > 0 && s.value !== 'no' && s.value !== 'yes');
  return pick ? { id: pick.personId, name: pick.value } : undefined;
}

/** Answers questions about faces and says what changed. */
export function useFaceAnswers() {
  const toast = useToast();
  const answer = useOpMutation('todos.answer', { invalidate: INVALIDATE });
  const dismiss = useOpMutation('todos.dismiss', { invalidate: INVALIDATE });
  const accept = useOpMutation('todos.acceptShown', { invalidate: INVALIDATE });
  const notPerson = useOpMutation('todos.notPerson', { invalidate: INVALIDATE });
  const notHere = useOpMutation('footage.faces.notHere', { invalidate: INVALIDATE });
  const skipFaces = useOpMutation('todos.skipFaces', { invalidate: INVALIDATE });
  const fail = (title: string) => (e: unknown) => {
    const x = toApiError(e);
    toast({ tone: 'error', title, body: [x.message, x.hint].filter(Boolean).join(' ') });
    return false;
  };
  const said = (tone: 'success' | 'info') => (o: { summary: string }) => {
    toast({ tone, title: o.summary });
    return true;
  };
  return {
    busy: answer.isPending || dismiss.isPending || accept.isPending || notPerson.isPending || notHere.isPending || skipFaces.isPending,
    answer: (t: Todo, v: { suggestion?: number; answer?: string; personId?: string; newPerson?: boolean; sameAs?: string }) =>
      answer.mutateAsync({ id: t.id, ...v }).then((o) => {
        toast({ tone: 'success', title: o.done });
        return true;
      }, fail('Could not save that answer')),
    skip: (t: Todo) =>
      dismiss.mutateAsync({ id: t.id }).then((o) => {
        toast({ tone: 'info', title: o.done });
        return true;
      }, fail('Could not skip')),
    acceptShown: (c: ShownClip) =>
      accept.mutateAsync({ assetId: c.assetId }).then((o) => {
        toast({ tone: o.accepted.length ? 'success' : 'info', title: o.summary });
        return true;
      }, fail('Could not use the names shown')),
    /** None of the questions suggesting this person (in a project or a clip, or everywhere) are them. */
    noneOf: (person: string, scope: { projectId?: string; assetId?: string }) =>
      notPerson.mutateAsync({ person, ...scope }).then(said('success'), fail('Could not save that answer')),
    /** The person appears nowhere on screen in the tile's clip. */
    notInClip: (t: Todo, who: { id?: string; name: string }) =>
      (who.id
        ? notHere.mutateAsync({ assetId: t.assetId!, person: who.id })
        : notPerson.mutateAsync({ person: who.name, assetId: t.assetId! })
      ).then(said('success'), fail('Could not save that answer')),
    /** Skip every question about faces in a clip or a project. */
    skipAll: (scope: { assetId?: string; projectId?: string }) => skipFaces.mutateAsync(scope).then(said('info'), fail('Could not skip')),
  };
}

/** The bar that accepts the names a clip shows, for everyone it asks about. */
export function ShownNamesBar({ clips }: { clips: ShownClip[] }) {
  const { acceptShown, busy } = useFaceAnswers();
  const [doing, setDoing] = useState<string | null>(null);
  if (clips.length === 0) return null;
  return (
    <div className="mb-3 space-y-2" data-shown-names>
      {clips.map((c) => (
        <div key={c.assetId} className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[14px] bg-accent-soft px-4 py-2.5">
          <TextT size={16} className="shrink-0 text-accent-text" />
          {/* At least 16rem for the sentence: on a phone the button goes below it. */}
          <p className="min-w-0 flex-[1_1_16rem] text-[13px] leading-snug text-fg">
            “<Theirs>{c.assetTitle}</Theirs>” shows the names of {c.names.length === 1 ? 'the person' : `the ${c.names.length} people`} under their pictures:{' '}
            <span className="font-medium">
              {c.names.map((n, i) => (
                <span key={n.todoId}>
                  {i > 0 ? ', ' : ''}
                  <Theirs>{n.name}</Theirs>
                </span>
              ))}
            </span>
            .
          </p>
          <Button
            size="sm"
            variant="primary"
            loading={busy && doing === c.assetId}
            disabled={busy}
            onClick={() => {
              setDoing(c.assetId);
              void acceptShown(c).finally(() => setDoing(null));
            }}
            data-accept-shown={c.assetId}
          >
            Use the names shown in the video
          </Button>
        </div>
      ))}
    </div>
  );
}

/**
 * Answers for many face questions at once (D189): "None of these are Lena"
 * for a person suggested by two questions or more, "Skip all in this project"
 * when the page shows one project, and which hints MetaRush no longer goes by.
 */
export function FaceBulkBar({ questions, projectId, assetId }: { questions: Todo[]; projectId?: string; assetId?: string }) {
  const { noneOf, skipAll, busy } = useFaceAnswers();
  const dialog = useDialog();
  const evidence = useOp('people.evidence', {}, { staleTime: 60_000 });
  const people = useMemo(() => {
    const by = new Map<string, { name: string; key: string; n: number }>();
    for (const t of questions) {
      const name = t.context.personName;
      if (!name) continue;
      const key = t.context.personId || `new:${foldName(name)}`;
      const p = by.get(key) ?? { name, key, n: 0 };
      p.n++;
      by.set(key, p);
    }
    return [...by.values()].filter((p) => p.n >= 2).sort((a, b) => b.n - a.n);
  }, [questions]);
  const muted = (evidence.data?.kinds ?? []).filter((k) => k.muted);
  const scope = projectId ? { projectId } : assetId ? { assetId } : {};
  if (people.length === 0 && !projectId && muted.length === 0) return null;
  return (
    <div className="mb-3 space-y-2" data-face-bulk>
      {people.length > 0 || projectId ? (
        <div className="flex flex-wrap items-center gap-2">
          {people.map((p) => (
            <Button
              key={p.key}
              size="sm"
              variant="secondary"
              icon={<UserMinus size={13} />}
              disabled={busy}
              data-none-of={p.name}
              onClick={async () => {
                const ok = await dialog.confirm({
                  title: `None of these are ${p.name}?`,
                  body: `The ${p.n} questions that suggest ${p.name} are answered “no”, and MetaRush won't suggest ${p.name} for those faces again. Each question can then be answered on its own.`,
                  confirmLabel: `None of them are ${p.name}`,
                });
                if (ok) void noneOf(p.key.startsWith('new:') ? p.name : p.key, scope);
              }}
            >
              None of these are <Theirs>{p.name}</Theirs> ({p.n})
            </Button>
          ))}
          {projectId ? (
            <Button
              size="sm"
              variant="ghost"
              className="ml-auto"
              disabled={busy}
              data-skip-project
              onClick={async () => {
                const ok = await dialog.confirm({
                  title: 'Skip every question about faces in this project?',
                  body: "Those faces won't be asked about again. You can still name any of them by hand on a clip's On screen tab.",
                  confirmLabel: 'Skip them all',
                });
                if (ok) void skipAll({ projectId });
              }}
            >
              Skip all in this project
            </Button>
          ) : null}
        </div>
      ) : null}
      {muted.length > 0 ? (
        <p className="flex items-start gap-1.5 text-[12px] leading-snug text-subtle" data-muted-evidence>
          <Info size={13} className="mt-[1px] shrink-0" />
          <span>
            {muted.map((k) => `${k.label}: right ${k.right} of ${k.answers} times here`).join('; ')}. MetaRush only offers those names as quick picks now.
          </span>
        </p>
      ) : null}
    </div>
  );
}

/**
 * One question about one person's faces in a clip: the faces, how often they
 * are seen, the suggestion and why, and the answers.
 */
export function FaceGroupTile({ todo: t, index, active, onActivate, others, selected, onSelect }: {
  todo: Todo;
  index: number;
  active: boolean;
  onActivate: () => void;
  /** The other open face questions of the same clip ("Same as…", "Skip all in this clip"). */
  others: Todo[];
  selected?: boolean;
  onSelect?: (on: boolean) => void;
}) {
  const { answer, skip, notInClip, skipAll, busy } = useFaceAnswers();
  const dialog = useDialog();
  const [text, setText] = useState('');
  const pickerId = `face-${t.id}`;
  const root = useRef<HTMLDivElement>(null);
  const faces = t.context.faces ?? [];
  const [first, ...rest] = faces;
  const title = t.context.assetTitle || t.context.assetName || 'this clip';
  const suggested = t.context.personName;
  const yes = t.suggestions.findIndex((s) => s.value === 'yes' || (suggested && s.label === `Yes, it's ${suggested}`));
  const hasNo = t.suggestions.some((s) => s.value === 'no');
  const quick = t.suggestions.map((s, i) => ({ s, i })).filter(({ s, i }) => i !== yes && s.value !== 'no');
  const who = tileWho(t);
  const pick = (c: PersonChoice) => {
    if (c.kind === 'person') void answer(t, { answer: c.person.name, personId: c.person.id });
    else if (c.kind === 'new') void answer(t, { answer: c.name, newPerson: true });
  };
  const notHere = () => {
    if (who && t.assetId) void notInClip(t, who);
  };
  const skipClip = async () => {
    if (!t.assetId) return;
    if (others.length > 0) {
      const ok = await dialog.confirm({
        title: `Skip all ${others.length + 1} questions about faces in “${title}”?`,
        body: "Those faces won't be asked about again. You can still name any of them by hand.",
        confirmLabel: 'Skip them all',
      });
      if (!ok) return;
    }
    void skipAll({ assetId: t.assetId });
  };
  const sameAs = async () => {
    const other = await dialog.open<Todo>(
      (close) => (
        <div>
          <p className="mb-3 text-[13px] leading-relaxed text-muted">Pick the other question about the same person in “{title}”. Their faces are asked about together then: one answer for both.</p>
          <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            {others.map((o) => {
              const f = o.context.faces?.[0];
              return (
                <li key={o.id}>
                  <button type="button" onClick={() => close(o)} className="flex w-full items-center gap-3 rounded-[12px] bg-panel p-2 text-left hairline hover:bg-hover" data-same-as={o.id}>
                    {f && o.assetId ? <FaceTile assetId={o.assetId} at={f.at} box={f.box} size={56} label="A face of the other question" /> : null}
                    <span className="min-w-0">
                      <span className="block truncate text-[13px] font-medium text-fg">{o.question}</span>
                      <span className="block text-[11.5px] text-muted">{seen(o)}</span>
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        </div>
      ),
      { title: 'Same person as…', size: 'md' },
    );
    if (other) void answer(t, { sameAs: other.id });
  };

  useHotkeys(
    {
      '1': () => {
        if (yes >= 0) void answer(t, { suggestion: yes + 1 });
      },
      n: () => {
        if (hasNo) void answer(t, { answer: 'no' });
      },
      s: () => void skip(t),
      c: () => notHere(),
      S: () => void skipClip(),
      Enter: (e) => {
        if (e.target instanceof HTMLElement && e.target.closest('button,a,summary,label')) return;
        e.preventDefault();
        document.getElementById(pickerId)?.focus();
      },
    },
    active && !busy,
  );

  return (
    <div
      ref={root}
      data-todo-index={index}
      data-todo-kind={t.kind}
      data-face-group={t.context.groupId}
      data-asset={t.assetId}
      onClick={onActivate}
      onFocusCapture={onActivate}
      aria-current={active || undefined}
      className={cx('fade-in relative flex min-w-0 flex-col rounded-[16px] bg-raised p-3 transition-shadow', active ? 'ring-1 ring-accent-line shadow-[var(--shadow-soft)]' : 'hairline')}
    >
      {onSelect ? (
        <label className="absolute right-2.5 top-2.5 flex h-6 w-6 items-center justify-center rounded-[6px] bg-panel/80 hairline" onClick={(e) => e.stopPropagation()}>
          <input type="checkbox" checked={!!selected} onChange={(e) => onSelect(e.target.checked)} className="h-4 w-4 accent-[var(--accent)]" data-select-face />
          <span className="sr-only">Select this question</span>
        </label>
      ) : null}
      <div className="flex gap-2">
        {first && t.assetId ? (
          <Link to={sourceLink(t.assetId, t.projectIds[0], first.at, undefined, 'onscreen')} onClick={(e) => e.stopPropagation()} aria-label={`Open “${title}” at this face`} className="shrink-0">
            <FaceTile assetId={t.assetId} at={first.at} box={first.box} size={104} label={`A face in ${title}`} />
          </Link>
        ) : (
          <div className="grid h-[104px] w-[104px] place-items-center rounded-[12px] bg-panel text-accent-text hairline" aria-hidden>
            <UserFocus size={26} />
          </div>
        )}
        {rest.length > 0 && t.assetId ? (
          <div className="grid grid-cols-3 content-start gap-1" role="group" aria-label="More of these faces">
            {rest.slice(0, 5).map((f) => (
              <FaceTile key={f.faceId} assetId={t.assetId!} at={f.at} box={f.box} size={40} label={`The same face at another moment of ${title}`} />
            ))}
          </div>
        ) : null}
      </div>
      <h2 className="mt-2.5 text-[15px] font-semibold tracking-[-0.01em] text-fg">{t.question}</h2>
      <p className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-[12px] text-muted">
        <span>
          {seen(t)} in “<Theirs>{title}</Theirs>”
        </span>
        {t.context.inCut ? (
          <Badge tone="accent" icon={<FilmSlate size={11} />}>
            In your cut
          </Badge>
        ) : null}
      </p>
      {(t.context.evidence ?? []).length > 0 ? (
        <ul className="mt-2 space-y-1" aria-label="Why">
          {(t.context.evidence ?? []).slice(0, 3).map((e, i) => (
            <li key={i} className="flex items-start gap-1.5 text-[12px] leading-snug text-muted" data-evidence={e.kind}>
              <span className="mt-[2px] shrink-0 text-subtle">{evidenceIcon(e)}</span>
              <span>{e.text}</span>
            </li>
          ))}
        </ul>
      ) : null}
      <div className="mt-auto pt-3">
        {yes >= 0 || hasNo ? (
          <div className="mb-2 flex flex-wrap gap-1.5" role="group" aria-label="Answers">
            {yes >= 0 ? (
              <Button
                size="sm"
                variant="primary"
                className="whitespace-normal text-left"
                icon={<Check size={12} weight="bold" />}
                disabled={busy}
                onClick={(e) => {
                  e.stopPropagation();
                  void answer(t, { suggestion: yes + 1 });
                }}
                trailing={<span className="tc ml-1 rounded-[4px] bg-black/15 px-1 text-[10.5px]">1</span>}
              >
                {t.suggestions[yes]?.label}
              </Button>
            ) : null}
            {hasNo ? (
              <Button
                size="sm"
                variant="secondary"
                icon={<X size={12} />}
                disabled={busy}
                onClick={(e) => {
                  e.stopPropagation();
                  void answer(t, { answer: 'no' });
                }}
                trailing={<span className="tc ml-1 rounded-[4px] bg-hover px-1 text-[10.5px] text-subtle">n</span>}
              >
                No
              </Button>
            ) : null}
          </div>
        ) : null}
        {quick.length > 0 ? (
          <div className="mb-2 flex flex-wrap gap-1.5" role="group" aria-label="Quick answers">
            {quick.map(({ s, i }) => (
              <Button key={i} size="sm" variant="ghost" className="whitespace-normal text-left" disabled={busy} onClick={(e) => {
                e.stopPropagation();
                void answer(t, { suggestion: i + 1 });
              }}>
                {s.label}
              </Button>
            ))}
          </div>
        ) : null}
        <div onClick={(e) => e.stopPropagation()}>
          <PersonPicker id={pickerId} label={t.question} value={text} onValueChange={setText} onPick={pick} placeholder={suggested ? 'Someone else? Type their name' : 'Type their name'} onEscape={() => document.getElementById(pickerId)?.blur()} />
        </div>
        <div className="mt-2 flex flex-wrap items-center gap-1" role="group" aria-label="More answers">
          {who ? (
            <Button
              size="sm"
              variant="ghost"
              icon={<EyeSlash size={12} />}
              disabled={busy}
              data-not-here={who.name}
              onClick={(e) => {
                e.stopPropagation();
                notHere();
              }}
              trailing={<span className="tc ml-1 rounded-[4px] bg-hover px-1 text-[10.5px] text-subtle">c</span>}
            >
              Not <Theirs>{who.name}</Theirs> in this clip
            </Button>
          ) : null}
          {others.length > 0 ? (
            <Button size="sm" variant="ghost" icon={<ArrowsMerge size={12} />} disabled={busy} onClick={(e) => {
              e.stopPropagation();
              void sameAs();
            }}>
              Same as…
            </Button>
          ) : null}
          {busy ? <Spinner size={12} className="text-subtle" label="Saving" /> : null}
          <span className="ml-auto flex items-center gap-1">
            {others.length > 0 ? (
              <Button size="sm" variant="ghost" disabled={busy} data-skip-clip={t.assetId} onClick={(e) => {
                e.stopPropagation();
                void skipClip();
              }} trailing={<span className="tc ml-1 rounded-[4px] bg-hover px-1 text-[10.5px] text-subtle">S</span>}>
                Skip all in this clip
              </Button>
            ) : null}
            <Button size="sm" variant="ghost" disabled={busy} onClick={(e) => {
              e.stopPropagation();
              void skip(t);
            }} trailing={<span className="tc ml-1 rounded-[4px] bg-hover px-1 text-[10.5px] text-subtle">s</span>}>
              Skip
            </Button>
          </span>
        </div>
      </div>
    </div>
  );
}

/** The keys of the grid, as a hint line. */
export function FaceGridKeys() {
  return (
    <p className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-[12px] text-subtle">
      <span className="inline-flex items-center gap-1">
        <Kbd>j</Kbd>
        <Kbd>k</Kbd> move
      </span>
      <span className="inline-flex items-center gap-1">
        <Kbd>1</Kbd> yes
      </span>
      <span className="inline-flex items-center gap-1">
        <Kbd>n</Kbd> no
      </span>
      <span className="inline-flex items-center gap-1">
        <Kbd>↵</Kbd> type a name
      </span>
      <span className="inline-flex items-center gap-1">
        <Kbd>c</Kbd> not them in this clip
      </span>
      <span className="inline-flex items-center gap-1">
        <Kbd>s</Kbd> skip
      </span>
      <span className="inline-flex items-center gap-1">
        <Kbd>S</Kbd> skip the whole clip
      </span>
      <span className="inline-flex items-center gap-1">
        <Kbd>x</Kbd> select
      </span>
    </p>
  );
}
