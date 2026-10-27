import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { ArrowsClockwise, CaretLeft, CaretRight, Check, Eyedropper, FilmSlate, MagicWand, Scissors, Sparkle, UsersThree, X } from '@phosphor-icons/react';
import { keepPreviousData } from '@tanstack/react-query';
import { call } from '../../api/client';
import { useOp } from '../../api/queries';
import type { CallInfo, CallMemberInfo, CallMoment, CleanupInfo, ConversationPin, ProjectShowOut } from '../../api/types';
import { markJobSeen } from '../../lib/workbench';
import { useDialog } from '../../ui/Dialog';
import { Badge, Button, cx, EmptyState, ErrorPanel, Skeleton, Theirs, type Tone } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { checkApplied, useChange } from './voiceover/api';
import { Toggle } from './sound/Mixer';
import { MakeConversationButton } from '../../components/MakeConversation';

/*
 * The Conversation tab (D99–D103): a call's recordings lined up on one clock
 * (each with how sure that is, and a nudge a frame at a time), the automatic
 * cut in a style, how each microphone plays, the webcams matched, the call's
 * clean-up suggestions to accept or skip, and shorts from its best moments.
 * Every button is one op the CLI has too (metarush conversation …); changes to
 * the cut become new versions.
 */

const SURE_TONE: Record<string, Tone> = { 'very sure': 'ok', 'fairly sure': 'accent', unsure: 'danger' };
const USE_WORDS: Record<string, string> = { both: 'picture and sound', sound: 'sound only', picture: 'shared picture' };

export function ConversationTab({ data, revisionId }: { data: ProjectShowOut; revisionId?: string }) {
  const projectId = data.project.id;
  const list = useOp('conversation.list', { projectId }, { placeholderData: keepPreviousData });
  const [pick, setPick] = useState<string>();
  const convs = list.data?.conversations ?? [];
  const conv = convs.find((c) => c.id === pick) ?? convs[0];

  if (list.error) return <ErrorPanel error={list.error} onRetry={() => void list.refetch()} />;
  if (!list.data) return <Skeleton className="h-[420px] rounded-[16px]" />;
  if (!conv)
    return (
      <div className="panel">
        <EmptyState
          icon={<UsersThree size={22} />}
          title="No conversation yet"
          body="A conversation is the recordings of one call — one per person from Zoom, Riverside or SquadCast, or each person's sound and the meeting's video — lined up on one clock. Its cut switches between people and layouts while everyone's own microphone plays on."
          action={<MakeConversationButton data={data} />}
        />
      </div>
    );

  return (
    <div className="space-y-5" data-conversation-tab>
      <div className="flex flex-wrap items-end gap-x-4 gap-y-3">
        <div className="min-w-0 flex-1">
          <h2 className="text-[19px] font-semibold tracking-[-0.02em]"><Theirs>{conv.name}</Theirs></h2>
          <p className="text-[12.5px] text-muted">{conv.text.startsWith(`${conv.name}: `) ? conv.text.slice(conv.name.length + 2) : conv.text}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {convs.length > 1 ? (
            <label className="flex items-center gap-2 text-[12.5px] text-muted">
              <span>Conversation</span>
              <select
                className="h-9 max-w-[220px] rounded-[10px] bg-panel px-2 text-[13px] text-fg hairline"
                value={conv.id}
                onChange={(e) => setPick(e.target.value)}
              >
                {convs.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </label>
          ) : null}
          <MakeConversationButton data={data} variant="ghost" />
        </div>
      </div>
      <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <div className="min-w-0 space-y-5">
          <Recordings conv={conv} />
          <MakeTheCut conv={conv} projectId={projectId} hasCut={!!revisionId} />
          <SoundSettings conv={conv} projectId={projectId} revisionId={revisionId} />
        </div>
        <div className="min-w-0 space-y-5">
          <Cleanup conv={conv} projectId={projectId} hasCut={!!revisionId} />
          <Webcams conv={conv} projectId={projectId} hasCut={!!revisionId} />
          <Shorts conv={conv} />
        </div>
      </div>
    </div>
  );
}

function Card({ id, title, sub, action, children, icon }: { id: string; title: string; sub?: ReactNode; action?: ReactNode; children?: ReactNode; icon?: ReactNode }) {
  return (
    <section aria-labelledby={id} className="space-y-3 rounded-[14px] bg-raised p-3.5 hairline" data-conv-card={id}>
      <div className="flex flex-wrap items-start justify-between gap-x-3 gap-y-2">
        <div className="flex min-w-0 items-start gap-2.5">
          {icon ? <span className="mt-0.5 grid h-7 w-7 shrink-0 place-items-center rounded-[9px] bg-accent-soft text-accent-text">{icon}</span> : null}
          <div className="min-w-0">
            <h3 id={id} className="text-[13.5px] font-semibold">
              {title}
            </h3>
            {sub ? <p className="text-[12px] leading-relaxed text-subtle">{sub}</p> : null}
          </div>
        </div>
        {action ? <div className="flex shrink-0 flex-wrap gap-2">{action}</div> : null}
      </div>
      {children}
    </section>
  );
}

/* ---------- the recordings, lined up ---------- */

function Recordings({ conv }: { conv: CallInfo }) {
  const change = useChange();
  const dialog = useDialog();
  const [details, setDetails] = useState(false);
  const [busy, setBusy] = useState<string>();
  const ref = { conversation: conv.id };

  const nudge = (m: CallMemberInfo, frames: number) => {
    setBusy(`${m.id}${frames}`);
    void change(() => call('conversation.nudge', { ...ref, member: m.id, frames }), (o) => o.summary).finally(() => setBusy(undefined));
  };
  const resync = async () => {
    const how = await dialog.open<'audio' | 'together' | 'clap'>(
      (close) => (
        <div className="space-y-3">
          <p className="text-[13px] text-muted">How should the recordings be lined up?</p>
          <div className="grid gap-2">
            {(
              [
                ['audio', 'By their sound', 'Each microphone hears the others a little; MetaRush finds where they match, and whether one clock runs fast.'],
                ['together', 'They start together', 'The app recorded them together (Zoom, Riverside aligned tracks).'],
                ['clap', 'On a clap', 'Someone clapped (or used a slate) in the first minute.'],
              ] as const
            ).map(([v, label, help]) => (
              <button key={v} type="button" onClick={() => close(v)} className="rounded-[12px] p-3 text-left hairline hover:bg-hover">
                <span className="block text-[13.5px] font-medium">{label}</span>
                <span className="block text-[12px] text-subtle">{help}</span>
              </button>
            ))}
          </div>
        </div>
      ),
      { title: 'Line the recordings up again', size: 'sm' },
    );
    if (!how) return;
    setBusy('sync');
    await change(
      async () => {
        const out = await call('conversation.sync', { ...ref, method: how });
        markJobSeen(out.jobId);
        return out;
      },
      (o) => o.summary,
    );
    setBusy(undefined);
  };

  return (
    <Card
      id="conv-recordings"
      title="Recordings"
      icon={<UsersThree size={15} />}
      sub="Where each recording starts on the conversation's clock and how sure that is. If someone's lips and voice don't quite match, move their recording a frame."
      action={
        <>
          <Button size="sm" variant="ghost" onClick={() => setDetails((d) => !d)} aria-pressed={details}>
            Details
          </Button>
          <Button size="sm" icon={<ArrowsClockwise size={13} />} loading={busy === 'sync'} onClick={() => void resync()}>
            Line up again
          </Button>
        </>
      }
    >
      <ul className="space-y-2" aria-label="Recordings">
        {conv.members.map((m) => (
          <li key={m.id} className="rounded-[12px] bg-panel p-3 hairline" data-member={m.id}>
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <Theirs className="text-[13.5px] font-medium">{m.name}</Theirs>
              <span className="text-[12px] text-subtle">{USE_WORDS[m.use] ?? m.use}</span>
              {m.method && m.method !== 'reference' ? (
                <Badge tone={SURE_TONE[m.sure] ?? 'neutral'} className="ml-auto">
                  {m.sure}
                </Badge>
              ) : (
                <Badge className="ml-auto">reference</Badge>
              )}
            </div>
            <p className="mt-1 text-[12.5px] leading-relaxed text-muted">{m.text}</p>
            {details ? (
              <p className="mt-1 break-words font-mono text-[11px] text-subtle">
                <Theirs>{m.title}</Theirs>
                {m.details ? ` · ${m.details}` : ''}
              </p>
            ) : null}
            {m.method !== 'reference' ? (
              <div className="mt-2 flex flex-wrap items-center gap-1.5">
                <span className="text-[12px] text-subtle">Move it</span>
                <Button size="sm" variant="secondary" icon={<CaretLeft size={12} />} loading={busy === `${m.id}-1`} onClick={() => nudge(m, -1)} aria-label={`Move ${m.name}'s recording a frame earlier`}>
                  a frame earlier
                </Button>
                <Button size="sm" variant="secondary" trailing={<CaretRight size={12} />} loading={busy === `${m.id}1`} onClick={() => nudge(m, 1)} aria-label={`Move ${m.name}'s recording a frame later`}>
                  a frame later
                </Button>
              </div>
            ) : null}
          </li>
        ))}
      </ul>
      {conv.cuts.some((c) => !c.current) ? <UseInCut conv={conv} /> : null}
    </Card>
  );
}

function UseInCut({ conv }: { conv: CallInfo }) {
  const change = useChange();
  const [busy, setBusy] = useState(false);
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-[12px] bg-accent-soft px-3 py-2 text-[12.5px] text-accent-text" role="status">
      <span className="min-w-0 flex-1">The cut still plays an earlier alignment.</span>
      <Button
        size="sm"
        variant="primary"
        loading={busy}
        onClick={() => {
          setBusy(true);
          void change(async () => checkApplied((await call('conversation.pin', { conversation: conv.id, apply: true })).applied), (ap) => `The cut uses the new alignment (version ${ap.number})`).finally(() => setBusy(false));
        }}
      >
        Use it in the cut
      </Button>
    </div>
  );
}

/* ---------- the automatic cut ---------- */

function MakeTheCut({ conv, projectId, hasCut }: { conv: CallInfo; projectId: string; hasCut: boolean }) {
  const styles = useOp('conversation.styles', {}, { staleTime: Infinity });
  const change = useChange();
  const dialog = useDialog();
  const [style, setStyle] = useState(conv.style || 'podcast');
  const [shape, setShape] = useState<'landscape' | 'vertical'>('landscape');
  const people = conv.members.filter((m) => m.hasPicture).length;
  const layouts = (styles.data?.layouts ?? []).filter((l) => people >= l.min && people <= l.max);
  const [layout, setLayout] = useState(conv.layout || '');
  const [busy, setBusy] = useState(false);
  useEffect(() => setStyle(conv.style || 'podcast'), [conv.id, conv.style]);

  const make = async () => {
    if (hasCut) {
      const ok = await dialog.confirm({
        title: 'Make a new cut of the conversation?',
        body: 'It becomes the next version of the cut. The current one stays in Versions, so nothing is lost.',
        confirmLabel: 'Make the cut',
      });
      if (!ok) return;
    }
    setBusy(true);
    await change(
      async () => checkApplied((await call('conversation.cut', { conversation: conv.id, project: projectId, style: style as 'podcast', shape, ...(layout ? { layout: layout as 'side-by-side' } : {}), apply: true })).applied),
      (ap) => `Made the cut (version ${ap.number}): each shot says why it is there`,
    );
    setBusy(false);
  };

  return (
    <Card id="conv-cut" title="Make the cut" icon={<MagicWand size={15} />} sub="MetaRush follows whoever is talking, cuts in pauses (never inside a word), keeps listening reactions and shows everyone when people talk or laugh together. Change any shot on the timeline afterwards.">
      <div role="radiogroup" aria-label="Style" className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {(styles.data?.styles ?? []).map((s) => (
          <button
            key={s.name}
            type="button"
            role="radio"
            aria-checked={style === s.name}
            onClick={() => setStyle(s.name)}
            className={cx('rounded-[12px] p-2.5 text-left transition-colors hairline', style === s.name ? 'bg-accent-soft ring-1 ring-accent' : 'bg-panel hover:bg-hover')}
          >
            <span className="block text-[13px] font-medium">{s.label}</span>
            <span className="block text-[11.5px] leading-snug text-subtle">{s.about}</span>
          </button>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <label className="flex items-center gap-2 text-[12.5px] text-muted">
          <span>Shape</span>
          <select className="h-8 rounded-[9px] bg-panel px-2 text-[13px] text-fg hairline" value={shape} onChange={(e) => setShape(e.target.value as 'landscape')}>
            <option value="landscape">16:9 episode</option>
            <option value="vertical">Vertical</option>
          </select>
        </label>
        {layouts.length > 1 ? (
          <label className="flex items-center gap-2 text-[12.5px] text-muted">
            <span>When people talk together</span>
            <select className="h-8 rounded-[9px] bg-panel px-2 text-[13px] text-fg hairline" value={layout} onChange={(e) => setLayout(e.target.value)}>
              <option value="">The best fit</option>
              {layouts.map((l) => (
                <option key={l.id} value={l.id}>
                  {l.label}
                </option>
              ))}
            </select>
          </label>
        ) : null}
        <Button className="ml-auto" variant="primary" icon={<Sparkle size={14} />} loading={busy} onClick={() => void make()}>
          Make the cut
        </Button>
      </div>
    </Card>
  );
}

/* ---------- how it sounds ---------- */

function SoundSettings({ conv, projectId, revisionId }: { conv: CallInfo; projectId: string; revisionId?: string }) {
  const rev = useOp('revision.show', revisionId ? { id: revisionId } : null, { placeholderData: keepPreviousData });
  const change = useChange();
  const [busy, setBusy] = useState<string>();
  const pin: ConversationPin | undefined = useMemo(() => Object.values(rev.data?.spec.conversations ?? {}).find((p) => p.id === conv.id), [rev.data, conv.id]);
  const name = useMemo(() => Object.entries(rev.data?.spec.conversations ?? {}).find(([, p]) => p.id === conv.id)?.[0], [rev.data, conv.id]);
  if (!revisionId || !pin || !name) return null;
  const gated = pin.gate?.mode === 'speaking';
  const tags = pin.nameTags !== 'off';
  const set = (key: string, input: { quieter?: boolean; nameTags?: boolean }, done: string) => {
    setBusy(key);
    void change(async () => checkApplied((await call('conversation.settings', { project: projectId, conversation: name, ...input, apply: true })).applied), done).finally(() => setBusy(undefined));
  };
  return (
    <Card id="conv-sound" title="How it plays" icon={<FilmSlate size={15} />} sub="Everyone's own microphone plays all the time; the picture is what switches.">
      <Toggle
        checked={gated}
        disabled={!!busy}
        onChange={(v) => set('gate', { quieter: v }, v ? 'Each microphone is quieter while its person listens' : 'Every microphone plays at its level all the time')}
        label="Quieter while not speaking"
        help="Each microphone steps down while its person listens, so one person's room and the other's voice coming out of their speakers stay out."
      />
      <Toggle checked={tags} disabled={!!busy} onChange={(v) => set('tags', { nameTags: v }, v ? 'Names on the tiles of layouts' : 'No names on the tiles')} label="Names on the tiles" help="Side by side, stacked, picture in picture and grid shots show each person's name." />
      <p className="text-[12px] text-subtle">
        Each person's level and a clearer voice are on the{' '}
        <Link to={`/p/${projectId}?tab=sound`} className="font-medium text-accent-text hover:underline">
          Sound tab
        </Link>
        .
      </p>
    </Card>
  );
}

/* ---------- clean-up ---------- */

const KIND_WORDS: Record<string, string> = { 'call-talk': 'Call talk', 'dead-air': 'Silence', freeze: 'Frozen picture', dropout: 'Sound dropout', chrome: "The app's labels" };

function Cleanup({ conv, projectId, hasCut }: { conv: CallInfo; projectId: string; hasCut: boolean }) {
  const change = useChange();
  const toast = useToast();
  const [busy, setBusy] = useState<string>();
  const [notes, setNotes] = useState<string[]>([]);
  const list = conv.suggestions;
  const open = list.filter((s) => s.state === 'open');

  const check = async () => {
    setBusy('check');
    const out = await change(
      async () => {
        const o = await call('conversation.cleanup', { conversation: conv.id });
        markJobSeen(o.jobId);
        return o;
      },
      (o) => o.summary,
      "Couldn't check the call",
    );
    setNotes(out?.notes ?? []);
    setBusy(undefined);
  };
  const decide = async (s: CleanupInfo, decision: 'accept' | 'skip' | 'reopen') => {
    setBusy(s.id + decision);
    try {
      const out = await call('conversation.decide', { conversation: conv.id, ids: [s.id], decision, project: projectId, apply: decision === 'accept' });
      if (decision === 'accept' && out.applied) checkApplied(out.applied);
      await change(
        async () => out,
        () => (decision === 'accept' ? (out.applied ? `Done: ${s.does.replace(/\.$/, '').toLowerCase()} (version ${out.applied.number})` : out.summary) : decision === 'skip' ? 'Skipped: the call stays as it is there' : 'Back on the list'),
      );
    } catch (e) {
      toast({ tone: 'error', title: "That wasn't changed", body: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(undefined);
    }
  };

  return (
    <Card
      id="conv-cleanup"
      title="Clean up the call"
      icon={<Scissors size={15} />}
      sub={hasCut ? 'What calls leave behind — talk about the call itself, long silences, a frozen webcam, a sound dropout, the app’s labels on a picture. Nothing changes until you accept it.' : 'Make the cut first: accepting a suggestion changes the cut.'}
      action={
        <Button size="sm" icon={<ArrowsClockwise size={13} />} loading={busy === 'check'} onClick={() => void check()}>
          {list.length ? 'Check again' : 'Check the call'}
        </Button>
      }
    >
      {list.length === 0 ? (
        <p className="text-[12.5px] text-subtle">Not checked yet.</p>
      ) : (
        <ul className="space-y-2" aria-label="Clean-up suggestions">
          {list.map((s) => (
            <li key={s.id} className={cx('rounded-[12px] p-3 hairline', s.state === 'open' ? 'bg-panel' : 'bg-hover/50')} data-suggestion={s.id} data-state={s.state}>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <Badge tone={s.state === 'open' ? 'accent' : 'neutral'}>{KIND_WORDS[s.kind] ?? s.kind}</Badge>
                <span className="tc text-[12px] text-subtle">{s.when}</span>
                {s.state !== 'open' ? <span className="ml-auto text-[12px] text-subtle">{s.state === 'accepted' ? (s.fix === 'flag' ? 'Noted' : 'Accepted') : 'Skipped'}</span> : null}
              </div>
              <p className="mt-1 text-[12.5px] leading-relaxed">{s.text}</p>
              {s.quote ? <p className="mt-1 text-[12.5px] italic leading-relaxed text-muted">“{s.quote}”</p> : null}
              {s.state === 'open' ? (
                <div className="mt-2 flex flex-wrap items-center gap-2">
                  <span className="min-w-0 flex-1 text-[12px] text-subtle">{s.does}</span>
                  <Button size="sm" variant="ghost" icon={<X size={12} />} loading={busy === s.id + 'skip'} onClick={() => void decide(s, 'skip')}>
                    Skip
                  </Button>
                  <Button size="sm" variant="primary" icon={<Check size={12} />} disabled={!hasCut && s.fix !== 'flag'} loading={busy === s.id + 'accept'} onClick={() => void decide(s, 'accept')}>
                    {s.fix === 'flag' ? 'Noted' : 'Accept'}
                  </Button>
                </div>
              ) : s.state === 'skipped' ? (
                <div className="mt-1.5">
                  <Button size="sm" variant="ghost" loading={busy === s.id + 'reopen'} onClick={() => void decide(s, 'reopen')}>
                    Undo skip
                  </Button>
                </div>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      {open.length > 1 ? <p className="text-[12px] text-subtle">{open.length} still open.</p> : null}
      {notes.map((n) => (
        <p key={n} className="text-[12px] text-subtle">
          {n}
        </p>
      ))}
    </Card>
  );
}

/* ---------- webcams ---------- */

function Webcams({ conv, projectId, hasCut }: { conv: CallInfo; projectId: string; hasCut: boolean }) {
  const change = useChange();
  const [busy, setBusy] = useState<string>();
  const cams = conv.members.filter((m) => m.hasPicture);
  if (cams.length < 2) return null;
  const matched = cams.some((m) => m.matched);
  const run = (off: boolean) => {
    setBusy(off ? 'off' : 'match');
    void change(
      async () => {
        const out = await call('conversation.match', { conversation: conv.id, project: projectId, off, apply: hasCut });
        markJobSeen(out.jobId);
        if (out.applied) checkApplied(out.applied);
        return out;
      },
      (o) => o.summary,
    ).finally(() => setBusy(undefined));
  };
  return (
    <Card
      id="conv-webcams"
      title="Match the webcams"
      icon={<Eyedropper size={15} />}
      sub="Every webcam sees colour and light differently. MetaRush measures each face and corrects brightness, contrast and colour to match one person's picture — measured before and after."
      action={
        <>
          {matched ? (
            <Button size="sm" variant="ghost" loading={busy === 'off'} onClick={() => run(true)}>
              As recorded
            </Button>
          ) : null}
          <Button size="sm" variant="secondary" loading={busy === 'match'} onClick={() => run(false)}>
            {matched ? 'Match again' : 'Match them'}
          </Button>
        </>
      }
    >
      <ul className="space-y-1.5" aria-label="Webcams">
        {cams.map((m) => (
          <li key={m.id} className="flex flex-wrap items-baseline gap-x-2 text-[12.5px]">
            <Theirs className="font-medium">{m.name}</Theirs>
            <span className="text-subtle">{m.matched ? m.matched : matched ? 'the reference' : 'as recorded'}</span>
          </li>
        ))}
      </ul>
    </Card>
  );
}

/* ---------- shorts ---------- */

function Shorts({ conv }: { conv: CallInfo }) {
  const toast = useToast();
  const [moments, setMoments] = useState<CallMoment[]>();
  const [busy, setBusy] = useState<string>();
  const [made, setMade] = useState<Record<string, string>>({});
  const find = async () => {
    setBusy('find');
    try {
      const out = await call('conversation.moments', { conversation: conv.id, count: 5 });
      setMoments(out.moments);
    } catch (e) {
      toast({ tone: 'error', title: "Couldn't look for moments", body: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(undefined);
    }
  };
  const make = async (m: CallMoment, k: string) => {
    setBusy(k);
    try {
      const out = await call('conversation.short', { conversation: conv.id, from: m.from, to: m.to, apply: true });
      if (out.projectId) setMade((x) => ({ ...x, [k]: out.projectId! }));
      toast({ tone: out.applied && !out.applied.problem ? 'success' : 'error', title: out.summary });
    } catch (e) {
      toast({ tone: 'error', title: "The short wasn't made", body: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(undefined);
    }
  };
  return (
    <Card
      id="conv-shorts"
      title="Shorts"
      icon={<Sparkle size={15} />}
      sub="Stretches that stand on their own — a question and its answer, a laugh, a strong first line. Each short is a new project with a vertical cut of its own. Claude Code can choose with you too."
      action={
        <Button size="sm" variant="secondary" loading={busy === 'find'} onClick={() => void find()}>
          {moments ? 'Look again' : 'Find the best moments'}
        </Button>
      }
    >
      {moments === undefined ? null : moments.length === 0 ? (
        <p className="text-[12.5px] text-subtle">No stretch stands on its own yet.</p>
      ) : (
        <ol className="space-y-2" aria-label="Best moments">
          {moments.map((m, i) => {
            const k = `${m.from}-${m.to}`;
            return (
              <li key={k} className="rounded-[12px] bg-panel p-3 hairline">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span className="text-[12px] font-semibold text-subtle">{i + 1}</span>
                  <span className="tc text-[12px] text-subtle">
                    {m.when} · {Math.round(m.to - m.from)} s
                  </span>
                  <span className="ml-auto text-[11.5px] text-subtle">{m.why.join(' · ')}</span>
                </div>
                <p className="mt-1 line-clamp-3 text-[12.5px] leading-relaxed">“{m.text}”</p>
                <div className="mt-2 flex justify-end">
                  {made[k] ? (
                    <Link to={`/p/${made[k]}`} className="text-[12.5px] font-medium text-accent-text hover:underline">
                      Open the short
                    </Link>
                  ) : (
                    <Button size="sm" variant="primary" loading={busy === k} onClick={() => void make(m, k)}>
                      Make a short
                    </Button>
                  )}
                </div>
              </li>
            );
          })}
        </ol>
      )}
    </Card>
  );
}
