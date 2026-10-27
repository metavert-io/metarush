import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { ArrowsClockwise, ArrowsMerge, Check, NotePencil, PencilSimple, Sparkle, UsersThree, WarningCircle } from '@phosphor-icons/react';
import { call, toApiError } from '../api/client';
import { useOp, useOpMutation } from '../api/queries';
import type { Appearance, PersonSummary, ClipVoice, OpOutput } from '../api/types';
import { pct, speakingTime } from '../lib/speakers';
import { resetSelection } from '../lib/workbench';
import { stopSnippet } from '../components/SnippetPlayer';
import { PersonFace, PersonPicker, type PersonChoice } from '../components/PersonPicker';
import { SampleTile, stopSampleTiles } from '../components/SampleTile';
import { FaceTile } from '../components/FaceTile';
import { LikenessSection } from './Looks';
import { sourceLink } from '../layout/SearchPalette';
import { useSpeakerActions } from '../components/speakerActions';
import { notThemFace } from '../components/CompareDialogs';
import { useDialog, type DialogApi } from '../ui/Dialog';
import { Button, cx, EmptyState, ErrorPanel, Freshness, Skeleton, Spinner, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';

const PEOPLE_QUERIES = ['people.list', 'people.show', 'footage.speakers', 'footage.transcript', 'todos.list', 'todos.counts', 'notification.list', 'footage.visuals', 'footage.faces'] as const;

type FaceRow = OpOutput<'people.show'>['faces'][number];

/** Everyone you've named, where they speak, notes about them, and fixes (rename, combine, separate); Library → People (D184). */
export function PeoplePage() {
  const [params, setParams] = useSearchParams();
  useEffect(() => resetSelection({}), []);
  useEffect(
    () => () => {
      stopSnippet();
      stopSampleTiles();
    },
    [],
  );
  const list = useOp('people.list', {});
  const people = list.data?.people ?? [];
  const selectedId = params.get('person') ?? people[0]?.id;
  const selected = people.find((p) => p.id === selectedId);

  return (
    <div data-people-page>
      <div className="-mt-2 mb-4 flex flex-wrap items-center gap-3">
        <h2 className="sr-only">People</h2>
        {list.data ? <p className="text-[12.5px] text-subtle">{people.length === 1 ? '1 person named' : `${people.length} people named`}</p> : null}
        <Freshness className="ml-auto" at={list.dataUpdatedAt || undefined} fetching={list.isFetching} onRefresh={() => void list.refetch()} />
      </div>
      {list.isLoading ? (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-[300px_1fr]">
          <Skeleton className="h-64 rounded-[16px]" />
          <Skeleton className="h-96 rounded-[16px]" />
        </div>
      ) : list.error ? (
        <ErrorPanel error={list.error} onRetry={() => void list.refetch()} />
      ) : people.length === 0 ? (
        <div className="panel">
          <EmptyState
            icon={<UsersThree size={22} />}
            title="Nobody named yet"
            body="Open a video, go to Speakers, listen to a voice and type a name. Or answer the questions on the To-dos page."
            action={
              <Link to="/todos" className="text-[13px] font-medium text-accent-text hover:underline">
                Go to questions
              </Link>
            }
          />
        </div>
      ) : (
        <div className="grid grid-cols-1 items-start gap-5 lg:grid-cols-[300px_1fr]">
          <ul className="panel divide-y divide-line overflow-hidden" aria-label="People">
            {people.map((p) => {
              const unchecked = p.appearances.filter((a) => a.source === 'auto').length;
              return (
                <li key={p.id}>
                  <button
                    type="button"
                    aria-current={p.id === selected?.id || undefined}
                    onClick={() => setParams({ person: p.id }, { replace: true })}
                    className={cx('flex w-full items-center gap-3 px-4 py-3 text-left transition-colors', p.id === selected?.id ? 'bg-accent-soft' : 'hover:bg-hover')}
                  >
                    <PersonFace person={p} size={36} />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-[14px] font-medium text-fg"><Theirs>{p.name}</Theirs></span>
                      <span className="block text-[12px] text-subtle">
                        {p.appearances.length === 1 ? '1 video' : `${p.appearances.length} videos`}, speaks {speakingTime(p.seconds)}
                      </span>
                    </span>
                    {unchecked ? (
                      <span className="shrink-0 rounded-full bg-info-soft px-2 py-0.5 text-[11px] font-medium text-info" title="Recognized by voice, not confirmed yet">
                        {unchecked} to check
                      </span>
                    ) : null}
                  </button>
                </li>
              );
            })}
          </ul>
          {selected ? <PersonDetail key={selected.id} person={selected} people={people} /> : null}
        </div>
      )}
    </div>
  );
}

/** "Combine X and Y?" with the safe answer as the default, and a plain pitch warning. */
function confirmCombine(dialog: DialogApi, a: string, b: string, pitchDiffers: boolean): Promise<boolean> {
  return dialog
    .open<boolean>(
      (close) => (
        <div>
          {pitchDiffers ? (
            <p className="rounded-[10px] bg-accent-soft px-3 py-2 text-[13.5px] leading-relaxed text-accent-text" data-pitch-warning>
              These two voices sound quite different — one is much lower. Same person?
            </p>
          ) : null}
          <p className="mt-3 text-[13.5px] leading-relaxed text-muted">
            Every video with {a} will say {b}, and their voices are learned together. You can separate a video again later.
          </p>
          <div className="mt-6 flex flex-wrap justify-end gap-2">
            <Button variant="primary" data-autofocus onClick={() => close(false)}>
              No, not the same
            </Button>
            <Button variant="secondary" onClick={() => close(true)}>
              Yes, combine
            </Button>
          </div>
        </div>
      ),
      { title: `Are ${a} and ${b} the same person?`, size: 'sm' },
    )
    .then((v) => v === true);
}

function PersonDetail({ person: p, people }: { person: PersonSummary; people: PersonSummary[] }) {
  const dialog = useDialog();
  const toast = useToast();
  const show = useOp('people.show', { id: p.id });
  const invalidate = [...PEOPLE_QUERIES];
  const rename = useOpMutation('people.rename', { invalidate });
  const merge = useOpMutation('people.merge', { invalidate });
  const distinct = useOpMutation('people.distinct', { invalidate });
  const rebuild = useOpMutation('people.rebuild', { invalidate });
  const [conflicts, setConflicts] = useState<ClipVoice[]>([]);
  const apps = show.data?.person.appearances ?? p.appearances;
  const similar = show.data?.similar ?? [];
  const others = useMemo(() => people.filter((x) => x.id !== p.id), [people, p.id]);

  const doRename = async () => {
    const name = await dialog.prompt({ title: `Rename ${p.name}`, label: 'Name', defaultValue: p.name, required: true, maxLength: 80, confirmLabel: 'Rename', body: 'The new name is used in every video they speak in.' });
    if (!name || name.trim() === p.name) return;
    rename.mutate({ id: p.id, name: name.trim() }, { onSuccess: (o) => toast({ tone: 'success', title: o.summary }), onError: (e) => toast({ tone: 'error', title: 'Could not rename', body: e.message }) });
  };
  const combineWith = async (other: { id: string; name: string }) => {
    let differ = false;
    try {
      differ = (await call('people.compare', { a: p.id, b: other.id })).pitchDiffers;
    } catch {
      // the comparison is advice; the question still stands
    }
    if (!(await confirmCombine(dialog, p.name, other.name, differ))) {
      distinct.mutate({ a: p.id, b: other.id }, { onSuccess: (o) => toast({ tone: 'info', title: o.summary }) });
      return;
    }
    merge.mutate({ from: p.id, into: other.id }, { onSuccess: (o) => toast({ tone: 'success', title: o.summary }), onError: (e) => toast({ tone: 'error', title: 'Could not combine', body: e.message }) });
  };
  const pickOther = async () => {
    const target = await dialog.open<PersonSummary>(
      (close) => <PickDialog label={`Who is ${p.name} really?`} exclude={[p.id]} onPick={(c) => c.kind === 'person' && close(c.person)} hint={`Their videos and voice samples move over, and ${p.name} disappears from the list.`} />,
      { title: `${p.name} is the same as…`, size: 'sm' },
    );
    if (target) await combineWith(target);
  };
  const doRebuild = () =>
    rebuild.mutate(
      { id: p.id },
      {
        onSuccess: (o) => {
          setConflicts(o.report?.conflicts ?? []);
          toast({ tone: 'success', title: o.summary });
        },
        onError: (e) => toast({ tone: 'error', title: 'Could not rebuild the voice', body: e.message }),
      },
    );

  return (
    <section className="panel p-4 sm:p-5" aria-label={p.name}>
      <div className="flex flex-wrap items-center gap-3">
        <PersonFace person={p} size={48} />
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-[20px] font-semibold tracking-[-0.02em]"><Theirs>{p.name}</Theirs></h2>
          <p className="text-[12.5px] text-subtle">
            {apps.length === 1 ? '1 video' : `${apps.length} videos`}, speaks {speakingTime(p.seconds)}
            {p.voiceprint ? ', voice known' : ', no voice sample yet'}
          </p>
        </div>
        <div className="flex flex-wrap gap-1">
          <Button size="sm" variant="ghost" icon={<PencilSimple size={13} />} onClick={() => void doRename()} loading={rename.isPending}>
            Rename
          </Button>
          {others.length > 0 ? (
            <Button size="sm" variant="ghost" icon={<ArrowsMerge size={13} />} onClick={() => void pickOther()} loading={merge.isPending}>
              Same as someone else
            </Button>
          ) : null}
          <Button size="sm" variant="ghost" icon={<ArrowsClockwise size={13} />} onClick={doRebuild} loading={rebuild.isPending} title="Learn their voice again from the videos you confirmed">
            Rebuild voice
          </Button>
        </div>
      </div>

      <Notes person={p} />

      {conflicts.length ? (
        <div className="mt-4 space-y-1.5 rounded-[12px] bg-danger-soft px-3 py-2.5" role="status">
          {conflicts.map((c) => (
            <p key={c.assetId + c.label} className="flex items-start gap-2 text-[13px] text-fg">
              <WarningCircle size={15} weight="fill" className="mt-[2px] shrink-0 text-danger" />
              <span>
                {c.reason === 'combined' ? `In “${c.assetTitle}”, two voices that sound like different people are combined as ${p.name}.` : `In “${c.assetTitle}”, ${c.speaker} sounds like someone else.`}{' '}
                <ClipLink ap={apps.find((x) => x.assetId === c.assetId)} assetId={c.assetId}>
                  Fix who said what there
                </ClipLink>
              </span>
            </p>
          ))}
        </div>
      ) : null}

      {similar.length > 0 ? (
        <div className="mt-4 space-y-2">
          {similar.map((s) => (
            <div key={s.id} className="flex flex-wrap items-center gap-2 rounded-[12px] bg-accent-soft px-3 py-2" data-similar={s.name}>
              <span className="text-[13px]">
                {p.name} sounds a lot like <span className="font-semibold">{s.name}</span> <span className="text-muted">({pct(s.score)}% voice match)</span>.
                {s.pitchDiffers ? ' One voice is much lower, though.' : ''} Same person?
              </span>
              <div className="ml-auto flex gap-1.5">
                <Button size="sm" variant="primary" loading={distinct.isPending} onClick={() => distinct.mutate({ a: p.id, b: s.id }, { onSuccess: (o) => toast({ tone: 'info', title: o.summary }) })}>
                  No, not the same
                </Button>
                <Button size="sm" variant="secondary" onClick={() => void combineWith(s)}>
                  Yes, combine
                </Button>
              </div>
            </div>
          ))}
        </div>
      ) : null}

      {p.samples.length > 0 ? (
        <>
          <h3 className="mb-2 mt-6 text-[13.5px] font-semibold">How {p.name} sounds</h3>
          <p className="mb-3 text-[12.5px] text-subtle">A few seconds from different videos. A wrong label stands out here.</p>
          <div className="flex flex-wrap gap-3" role="group" aria-label={`Samples of ${p.name}`}>
            {p.samples.map((s) => (
              <div key={s.assetId + s.from} className="relative">
                <SampleTile assetId={s.assetId} from={s.from} to={s.to} title={s.assetTitle} hasVideo={s.hasVideo} label={`${p.name} in ${s.assetTitle}`} size="md" />
                {s.source === 'auto' ? (
                  <span className="pointer-events-none absolute right-1.5 top-1.5 inline-flex items-center gap-1 rounded-full bg-black/60 px-1.5 py-0.5 text-[10.5px] font-medium text-white" title="Recognized by voice, not confirmed">
                    <Sparkle size={10} /> {pct(s.score)}%
                  </span>
                ) : null}
              </div>
            ))}
          </div>
        </>
      ) : null}

      <PersonFaces person={p} faces={show.data?.faces ?? []} recognized={show.data?.recognized ?? []} asked={show.data?.asked ?? []} />

      <LikenessSection person={p} />

      <h3 className="mb-2 mt-6 text-[13.5px] font-semibold">Where {p.name} speaks</h3>
      {show.isLoading && apps.length === 0 ? (
        <Skeleton className="h-24" />
      ) : apps.length === 0 ? (
        <p className="text-[13px] text-muted">Not in any video right now.</p>
      ) : (
        <ul className="space-y-2">
          {[...apps]
            .sort((x, y) => (x.source === y.source ? 0 : x.source === 'auto' ? -1 : 1))
            .map((ap) => (
              <AppearanceRow key={ap.assetId + ap.label} person={p} ap={ap} />
            ))}
        </ul>
      )}
    </section>
  );
}

/**
 * A person's faces: the ones you confirmed (one clip each; one that looks
 * unlike the others is asked about again), the ones MetaRush recognized by
 * their looks (D114: dashed, "That's them" / "Not them?"), and "Is this …?"
 * about faces that were on screen alone while they spoke or look like them.
 * Only faces you confirm teach MetaRush what they look like.
 */
function PersonFaces({ person: p, faces, recognized, asked }: { person: PersonSummary; faces: FaceRow[]; recognized: FaceRow[]; asked: FaceRow[] }) {
  const toast = useToast();
  const dialog = useDialog();
  const invalidate = [...PEOPLE_QUERIES];
  const answer = useOpMutation('todos.answer', { invalidate });
  const reject = useOpMutation('footage.faces.reject', { invalidate });
  const name = useOpMutation('footage.faces.name', { invalidate });
  const busy = answer.isPending || reject.isPending || name.isPending;
  if (faces.length === 0 && asked.length === 0 && recognized.length === 0) return null;
  const recognizedClips = new Set(recognized.map((f) => f.assetId)).size;
  const done = (o: { done: string }) => toast({ tone: 'success', title: o.done });
  const failed = (e: Error) => toast({ tone: 'error', title: 'Could not save that', body: toApiError(e).message });
  return (
    <section aria-label={`${p.name}'s face`} data-person-faces>
      <h3 className="mb-2 mt-6 text-[13.5px] font-semibold">{p.name}'s face</h3>
      <p className="mb-3 text-[12.5px] text-subtle">
        {asked.length > 0 ? `Is this ${p.name}? These faces were on screen by themselves while ${p.name} spoke, look like ${p.name}, or the video shows or says the name. One answer names all of them in that clip. ` : ''}
        {recognized.length > 0 ? `MetaRush recognized ${p.name}'s face ${recognized.length === 1 ? '' : `${recognized.length} times `}in ${recognizedClips === 1 ? 'one clip' : `${recognizedClips} clips`}: check ${recognized.length === 1 ? 'it' : 'them'}. ` : ''}A face you confirm shows {p.name}'s name in that clip and teaches MetaRush what {p.name} looks like.
      </p>
      <div className="flex flex-wrap gap-3" role="group" aria-label={`Faces of ${p.name}`}>
        {asked.map((f) => (
          <figure key={f.id} className="w-[132px] rounded-[12px] bg-accent-soft p-2" data-face-question={f.question}>
            <Link to={sourceLink(f.assetId, f.projectId, f.at, undefined, 'onscreen')} aria-label={`Open ${f.assetTitle ?? 'the clip'} at that moment`}>
              <FaceTile assetId={f.assetId} at={f.at} box={f.box} size={116} label={`A face in ${f.assetTitle ?? 'a clip'}`} />
            </Link>
            <figcaption className="mt-1.5">
              <span className="block text-[12.5px] font-medium text-fg">Is this {p.name}?</span>
              <span className="block truncate text-[11.5px] text-muted"><Theirs>{f.assetTitle}</Theirs></span>
              {f.appearances && f.appearances > 1 ? <span className="block text-[11.5px] text-muted">Seen {f.appearances} times there</span> : null}
              <span className="mt-1.5 flex gap-1">
                <Button size="sm" variant="primary" disabled={busy || !f.question} onClick={() => answer.mutate({ id: f.question!, suggestion: 1 }, { onSuccess: done, onError: failed })}>
                  Yes
                </Button>
                <Button size="sm" variant="secondary" disabled={busy || !f.question} onClick={() => answer.mutate({ id: f.question!, answer: 'no' }, { onSuccess: done, onError: failed })}>
                  No
                </Button>
              </span>
            </figcaption>
          </figure>
        ))}
        {recognized.map((f) => (
          <figure key={f.id} className="w-[132px] rounded-[12px] border border-dashed border-line p-2" data-face-recognized>
            <Link to={sourceLink(f.assetId, f.projectId, f.at, undefined, 'onscreen')} aria-label={`Open ${f.assetTitle ?? 'the clip'} at that moment`}>
              <FaceTile assetId={f.assetId} at={f.at} box={f.box} size={116} label={`Looks like ${p.name}, in ${f.assetTitle ?? 'a clip'}`} />
            </Link>
            <figcaption className="mt-1.5">
              <span className="block text-[11.5px] text-muted">Recognized, {Math.round((f.recognized?.score ?? 0) * 100)}% alike</span>
              <span className="block truncate text-[11.5px] text-muted">{f.assetTitle}</span>
              <span className="mt-1.5 flex gap-1">
                <Button size="sm" variant="secondary" disabled={busy} onClick={() => name.mutate({ faceId: f.id, personId: p.id, name: p.name }, { onSuccess: done, onError: failed })}>
                  That's {p.name}
                </Button>
              </span>
              <Button
                size="sm"
                variant="ghost"
                className="mt-1 -ml-1.5"
                disabled={busy}
                onClick={() => void notThemFace(dialog, { faceId: f.id, assetId: f.assetId, at: f.at, box: f.box, personId: p.id, name: p.name, score: f.recognized?.score })}
              >
                Not {p.name}?
              </Button>
            </figcaption>
          </figure>
        ))}
        {faces.map((f) => (
          <figure key={f.id} className={cx('w-[132px] rounded-[12px] p-2', f.question ? 'bg-accent-soft' : 'hairline')} data-face-confirmed data-face-question={f.question || undefined}>
            <Link to={sourceLink(f.assetId, f.projectId, f.at, undefined, 'onscreen')} aria-label={`Open ${f.assetTitle ?? 'the clip'} at that moment`}>
              <FaceTile assetId={f.assetId} at={f.at} box={f.box} size={116} label={`${p.name}'s face in ${f.assetTitle ?? 'a clip'}`} />
            </Link>
            <figcaption className="mt-1.5">
              {f.question ? <span className="block text-[12.5px] font-medium text-fg">Is this really {p.name}?</span> : null}
              {f.question ? <span className="block text-[11.5px] text-muted">It doesn't look like {p.name}'s other faces.</span> : null}
              <span className="block truncate text-[11.5px] text-muted">{f.assetTitle}</span>
              {f.question ? (
                <span className="mt-1.5 flex gap-1">
                  <Button size="sm" variant="primary" disabled={busy} onClick={() => answer.mutate({ id: f.question!, suggestion: 1 }, { onSuccess: done, onError: failed })}>
                    Yes
                  </Button>
                  <Button size="sm" variant="secondary" disabled={busy} onClick={() => answer.mutate({ id: f.question!, suggestion: 2 }, { onSuccess: done, onError: failed })}>
                    No
                  </Button>
                </span>
              ) : (
                <Button size="sm" variant="ghost" className="mt-1 -ml-1.5" disabled={busy} onClick={() => reject.mutate({ faceId: f.id }, { onSuccess: done, onError: failed })}>
                  Not {p.name}?
                </Button>
              )}
            </figcaption>
          </figure>
        ))}
      </div>
    </section>
  );
}

/** The person-picking dialog body (who someone really is, or who to combine with). */
function PickDialog({ label, exclude, onPick, hint, allowNew = false }: { label: string; exclude: string[]; onPick: (c: PersonChoice) => void; hint?: string; allowNew?: boolean }) {
  const [text, setText] = useState('');
  return (
    <div>
      {hint ? <p className="mb-3 text-[13px] leading-relaxed text-muted">{hint}</p> : null}
      <PersonPicker label={label} value={text} onValueChange={setText} onPick={onPick} exclude={exclude} allowNew={allowNew} autoFocus placeholder="Start typing a name" />
    </div>
  );
}

function ClipLink({ ap, assetId, children }: { ap?: Appearance; assetId: string; children: React.ReactNode }) {
  const project = ap?.projectIds[0];
  const to = project ? `/p/${project}?tab=sources&asset=${assetId}&panel=speakers` : `/?asset=${assetId}&panel=speakers`;
  return (
    <Link to={to} className="font-medium text-accent-text hover:underline">
      {children}
    </Link>
  );
}

/** Notes about a person, saved as you type (Claude reads them for names, titles and what to avoid). */
function Notes({ person: p }: { person: PersonSummary }) {
  const id = useId();
  const [text, setText] = useState(p.notes ?? '');
  const [state, setState] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle');
  const saved = useRef(p.notes ?? '');
  const update = useOpMutation('people.update', { invalidate: ['people.list', 'people.show', 'footage.speakers'] });
  useEffect(() => {
    if (text === saved.current) return;
    setState('idle');
    const t = setTimeout(() => {
      setState('saving');
      const v = text;
      update.mutate(
        v.trim() ? { id: p.id, notes: v } : { id: p.id, clearNotes: true },
        {
          onSuccess: () => {
            saved.current = v;
            setState('saved');
          },
          onError: () => setState('error'),
        },
      );
    }, 800);
    return () => clearTimeout(t);
  }, [text]); // eslint-disable-line react-hooks/exhaustive-deps
  const left = 2000 - text.length;
  return (
    <div className="mt-5">
      <label htmlFor={id} className="flex items-center gap-1.5 text-[13.5px] font-semibold text-fg">
        <NotePencil size={14} className="text-subtle" /> About {p.name}
      </label>
      <p className="mt-0.5 text-[12px] text-subtle">Claude reads this before choosing shots, writing name tags and captions.</p>
      <textarea
        id={id}
        value={text}
        maxLength={2000}
        rows={3}
        onChange={(e) => setText(e.target.value)}
        placeholder="Who they are, how to refer to them, what to highlight or avoid…"
        className="mt-2 w-full resize-y rounded-[10px] bg-raised px-3 py-2 text-[13.5px] leading-relaxed text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
        data-person-notes
      />
      <div className="mt-1 flex min-h-[18px] items-center gap-2 text-[12px]" aria-live="polite">
        {state === 'saving' ? (
          <span className="inline-flex items-center gap-1 text-subtle">
            <Spinner size={10} /> Saving
          </span>
        ) : state === 'saved' ? (
          <span className="inline-flex items-center gap-1 text-ok">
            <Check size={12} weight="bold" /> Saved
          </span>
        ) : state === 'error' ? (
          <span className="text-danger">Not saved. Check the connection and type again.</span>
        ) : null}
        {left < 200 ? <span className="tc ml-auto text-subtle">{left} left</span> : null}
      </div>
    </div>
  );
}

function AppearanceRow({ person: p, ap }: { person: PersonSummary; ap: Appearance }) {
  const dialog = useDialog();
  const toast = useToast();
  const actions = useSpeakerActions();
  const [busy, setBusy] = useState(false);
  const split = useOpMutation('people.split', { invalidate: [...PEOPLE_QUERIES] });
  const auto = ap.source === 'auto';
  const notThem = async () => {
    if (auto) {
      // One click: the automatic label goes, and the voice is asked about again.
      setBusy(true);
      await actions.notThem(ap.assetId, ap.label, { id: p.id, name: p.name });
      setBusy(false);
      return;
    }
    const choice = await dialog.open<PersonChoice>(
      (close) => <PickDialog label="Who is it really?" exclude={[p.id]} allowNew onPick={(c) => close(c)} hint="Tell us who it really is. Their voice is then learned from this video instead." />,
      { title: `Not ${p.name} in “${ap.assetTitle || ap.assetName}”?`, size: 'sm' },
    );
    if (!choice || choice.kind === 'unsure') return;
    split.mutate(
      choice.kind === 'person' ? { id: p.id, assetId: ap.assetId, speaker: ap.label, otherId: choice.person.id } : { id: p.id, assetId: ap.assetId, speaker: ap.label, name: choice.name, newPerson: true },
      { onSuccess: (o) => toast({ tone: 'success', title: o.summary }), onError: (e) => toast({ tone: 'error', title: 'Could not change it', body: toApiError(e).message }) },
    );
  };
  const confirm = async () => {
    setBusy(true);
    await actions.nameVoice(ap.assetId, ap.label, { kind: 'person', person: p });
    setBusy(false);
  };
  return (
    <li className={cx('rounded-[12px] bg-panel p-3 hairline', auto && 'outline-1 outline-dashed outline-info/45 -outline-offset-1')} data-appearance={ap.assetTitle} data-source={ap.source}>
      <div className="flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-start">
        <div className="order-2 flex gap-2 sm:order-none">
          {ap.samples.slice(0, 2).map((s) => (
            <SampleTile key={s.from} assetId={ap.assetId} from={s.from} to={s.to} hasVideo={ap.hasVideo} label={`${p.name} in ${ap.assetTitle || ap.assetName}`} size="sm" />
          ))}
        </div>
        <div className="order-1 min-w-0 flex-1 sm:order-none">
          <ClipLink ap={ap} assetId={ap.assetId}>
            <span className="block truncate text-[13.5px] text-fg">{ap.assetTitle || ap.assetName}</span>
          </ClipLink>
          <span className="mt-0.5 block text-[12px] text-subtle">speaks {speakingTime(ap.seconds)}</span>
          {auto ? (
            <span className="mt-1.5 inline-flex items-center gap-1.5 rounded-full bg-info-soft px-2 py-0.5 text-[11.5px] font-medium text-info sm:whitespace-nowrap">
              <Sparkle size={11} /> Recognized by voice, {pct(ap.score)}% voice match
            </span>
          ) : (
            <span className="mt-1.5 inline-flex items-center gap-1 text-[11.5px] text-subtle">
              <Check size={11} weight="bold" /> Named by you
            </span>
          )}
        </div>
        <div className="order-3 flex shrink-0 flex-wrap gap-1.5 sm:order-none sm:self-center">
          {auto ? (
            <Button size="sm" variant="secondary" icon={<Check size={12} weight="bold" />} disabled={busy} onClick={() => void confirm()}>
              That's {p.name}
            </Button>
          ) : null}
          <Button size="sm" variant="ghost" onClick={() => void notThem()} loading={busy || split.isPending}>
            Not {p.name}?
          </Button>
        </div>
      </div>
    </li>
  );
}
