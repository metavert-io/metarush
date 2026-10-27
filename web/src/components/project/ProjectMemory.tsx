import { useEffect, useId, useState, type FormEvent, type ReactNode } from 'react';
import { ArrowSquareOut, BookmarkSimple, ListChecks, MapPin, Plus, Seal, ShieldCheck, Signature, X } from '@phosphor-icons/react';
import { useOp, useOpMutation } from '../../api/queries';
import type { Deliverable, KeepItem, KeepKind, ProjectRule, ProjectShowOut, RemoteOp, Signoff } from '../../api/types';
import { absolute, plural, relative } from '../../lib/format';
import { openAskRun } from '../../lib/workbench';
import { Badge, Button, cx, ErrorPanel, Skeleton, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';

/*
 * What this project keeps (D166, docs/keeping.md), "This project" in the
 * project's Settings tab (below its Content Settings, D193):
 * its rules (and the checks the quality check makes every time), the sign-offs
 * the cut needs before it goes out, where the video goes (the brief's
 * places, each with its shape) and what its conversations offered to keep.
 * Everything here is the same ops as `metarush project rules`, `metarush cut
 * signoffs`, `metarush project update` and `metarush keep`.
 */

/** What the quality check measures for a rule, in plain words. */
export function measureWords(m: string | undefined): string {
  if (!m) return '';
  switch (m) {
    case 'captions-top':
      return 'captions at the top';
    case 'captions-bottom':
      return 'captions at the bottom';
    case 'first-names':
      return 'names on screen are first names only';
    case 'no-flashes':
      return 'no split-second flashes at clip edges';
  }
  if (m.startsWith('max-length:')) {
    const [num, shape] = m.slice('max-length:'.length).split('@');
    const s = Math.round(Number(num));
    if (!Number.isFinite(s) || s <= 0) return '';
    const len = `at most ${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')} long`;
    return shape ? `${len} when it is ${SHAPE_OF_MEASURE[shape] ?? shape}` : len;
  }
  // The promises' measures (D300), as internal/measures says them.
  const [kind, arg = ''] = m.split(':');
  const n = Number(arg);
  switch (kind) {
    case 'captions':
      return 'captions on wherever someone speaks, large and slow enough to read';
    case 'logo-end':
      return `a logo or an end card for the last ${n === 1 ? 'second' : `${n} seconds`}`;
    case 'logo-start':
      return `a logo within the first ${n === 1 ? 'second' : `${n} seconds`}`;
    case 'speech-over-music':
      return n === 0 ? 'the music never louder than the voices' : `the voices at least ${n} loudness steps louder than the music`;
    case 'disclosed':
      return 'every AI-generated part disclosed, on screen or in the description';
    case 'faces-in-frame':
      return 'no face cut off by the edge of the picture';
    case 'min-size':
      return `at least ${n} pixels on the shorter side`;
    case 'min-fps':
      return `at least ${n} frames a second`;
  }
  if (m.startsWith('shape:')) return SHAPE_OF_MEASURE[m.slice('shape:'.length)] ?? '';
  return '';
}

const SHAPE_OF_MEASURE: Record<string, string> = { landscape: 'widescreen', vertical: 'vertical', square: 'square' };

/** A place's shape, from its preset. */
export function shapeWord(preset: string | undefined): string {
  if (!preset) return '';
  if (preset.startsWith('vertical')) return 'Vertical';
  if (preset.startsWith('square')) return 'Square';
  return 'Widescreen';
}

const KIND_WORD: Record<KeepKind, string> = {
  source: 'Source',
  picture: 'Picture',
  brief: 'Brief',
  rule: 'Rule',
  promise: 'Promise',
  check: 'Check',
  person: 'Note',
  signoff: 'Sign-off',
  improve: 'Idea',
  spelling: 'Spelling',
};

const field = 'h-9 w-full min-w-0 rounded-[9px] bg-panel px-2.5 text-[13px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0';

export function ProjectMemory({ data }: { data: ProjectShowOut }) {
  const projectId = data.project.id;
  return (
    <div className="space-y-5" data-project-memory>
      <header className="max-w-[78ch]">
        <h2 className="text-[17px] font-semibold tracking-[-0.015em] text-fg">This project</h2>
        <p className="mt-1 text-[13px] leading-relaxed text-muted">
          What this project keeps for itself: its own rules (Claude follows them in every conversation and the quality check measures them), the OKs the video needs before it goes
          out, where it goes, and what conversations offered to keep. Change any of it here.
        </p>
      </header>
      <div className="grid grid-cols-1 items-start gap-5 lg:grid-cols-2">
        <div className="min-w-0 space-y-5">
          <Rules projectId={projectId} />
          <Signoffs projectId={projectId} />
        </div>
        <div className="min-w-0 space-y-5">
          <WhereItGoes data={data} />
          <Offered projectId={projectId} />
        </div>
      </div>
    </div>
  );
}

function Section({ icon, title, meta, help, children, name }: { icon: ReactNode; title: string; meta?: ReactNode; help?: ReactNode; children: ReactNode; name: string }) {
  const id = useId();
  return (
    <section className="panel min-w-0 p-4 sm:p-5" aria-labelledby={id} data-memory={name}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="text-subtle" aria-hidden>
          {icon}
        </span>
        <h3 id={id} className="text-[14.5px] font-semibold tracking-[-0.01em] text-fg">
          {title}
        </h3>
        {meta ? <span className="text-[12px] text-subtle">{meta}</span> : null}
      </div>
      {help ? <p className="mt-1 text-[12.5px] leading-relaxed text-muted">{help}</p> : null}
      <div className="mt-3">{children}</div>
    </section>
  );
}

function RemoveButton({ label, onClick, busy }: { label: string; onClick: () => void; busy?: boolean }) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={busy}
      aria-label={label}
      title={label}
      className="grid h-7 w-7 shrink-0 place-items-center rounded-[8px] text-subtle hover:bg-hover hover:text-fg disabled:opacity-40"
    >
      <X size={13} />
    </button>
  );
}

/* ---------- Rules (and checks) ---------- */

const RULE_QUERIES: RemoteOp[] = ['project.rules.list', 'project.show', 'keep.list'];

function ruleLine(r: ProjectRule): string {
  const w = measureWords(r.measure);
  if (r.promise) return w ? `A promise to the client: every quality check measures it (${w}), and a version that breaks it waits before it goes out` : 'A promise to the client';
  if (r.check) return w ? `Checked in every quality check: ${w}` : 'Checked in every quality check';
  return w ? `Claude follows it; the quality check measures it: ${w}` : 'Claude follows it';
}

function Rules({ projectId }: { projectId: string }) {
  const q = useOp('project.rules.list', { projectId });
  const toast = useToast();
  const add = useOpMutation('project.rules.add', { invalidate: RULE_QUERIES });
  const remove = useOpMutation('project.rules.remove', { invalidate: RULE_QUERIES });
  const [text, setText] = useState('');
  const [check, setCheck] = useState(false);
  const [promise, setPromise] = useState(false);
  const rules = q.data?.rules ?? [];
  const checks = rules.filter((r) => r.check).length;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const t = text.trim();
    if (!t) return;
    add.mutate(
      { projectId, text: t, ...(check ? { check: true } : {}), ...(promise ? { promise: true } : {}) },
      {
        onSuccess: (out) => {
          setText('');
          setCheck(false);
          setPromise(false);
          if (!out.created) toast({ title: 'The project already has that rule' });
        },
      },
    );
  };
  const takeOut = (r: ProjectRule) =>
    remove.mutate(
      { projectId, rule: r.id },
      {
        onSuccess: () =>
          toast({
            title: 'Rule taken out',
            body: `“${r.text}”`,
            action: {
              label: 'Undo',
              onClick: () => add.mutate({ projectId, text: r.text, ...(r.check ? { check: true } : {}), ...(r.promise ? { promise: true } : {}), ...(r.measure ? { measure: r.measure } : {}) }),
            },
          }),
        onError: (e) => toast({ tone: 'error', title: 'The rule is still there', body: e.message }),
      },
    );

  return (
    <Section
      name="rules"
      icon={<ListChecks size={16} />}
      title="Rules"
      meta={rules.length ? `${plural(rules.length, 'rule')}${checks ? `, ${plural(checks, 'check')}` : ''}` : undefined}
      help="Claude follows them in everything it suggests; the quality check measures the ones it can."
    >
      {q.isLoading ? (
        <Skeleton className="h-16" />
      ) : q.error ? (
        <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />
      ) : rules.length ? (
        <ul className="divide-y divide-line overflow-hidden rounded-[12px] hairline" aria-label="This project's rules">
          {rules.map((r) => (
            <li key={r.id} className="flex items-start gap-2.5 px-3 py-2.5" data-rule={r.promise ? 'promise' : r.check ? 'check' : 'rule'}>
              <span className="mt-[3px] shrink-0 text-subtle" aria-hidden>
                {r.promise ? <Seal size={14} /> : r.check ? <ShieldCheck size={14} /> : <ListChecks size={14} />}
              </span>
              <div className="min-w-0 flex-1">
                <Theirs className="block break-words text-[13.5px] leading-snug text-fg">{r.text}</Theirs>
                <p className="mt-0.5 text-[12px] leading-relaxed text-subtle">
                  {ruleLine(r)}
                  <span className="whitespace-nowrap" title={absolute(r.addedAt)}>
                    {' '}
                    · added {relative(r.addedAt)}
                  </span>
                </p>
              </div>
              <RemoveButton label={`Take the rule “${r.text}” out`} onClick={() => takeOut(r)} busy={remove.isPending && remove.variables?.rule === r.id} />
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-[12.5px] leading-relaxed text-subtle">No rules yet. Add one in your own words, like “Captions at the top”.</p>
      )}
      <form onSubmit={submit} className="mt-3 flex flex-wrap items-center gap-2" data-rule-add>
        <input value={text} onChange={(e) => setText(e.target.value)} placeholder="Captions at the top" aria-label="A new rule, in your words" maxLength={300} className={cx(field, 'flex-1 basis-[180px]')} />
        <Button type="submit" variant="secondary" icon={<Plus size={13} />} disabled={!text.trim()} loading={add.isPending}>
          Add
        </Button>
        <label className="flex basis-full items-center gap-1.5 text-[12px] text-muted">
          <input type="checkbox" checked={check} onChange={(e) => setCheck(e.target.checked)} className="accent-[var(--accent)]" />
          It's a problem to check for in every quality check
        </label>
        <label className="flex basis-full items-center gap-1.5 text-[12px] text-muted">
          <input type="checkbox" checked={promise} onChange={(e) => setPromise(e.target.checked)} className="accent-[var(--accent)]" data-rule-promise />
          It's a promise to the client: a version that breaks it waits before it goes out
        </label>
      </form>
      {add.error ? <p className="mt-2 text-[12px] text-danger">{add.error.message}</p> : null}
    </Section>
  );
}

/* ---------- Sign-offs ---------- */

const SIGNOFF_QUERIES: RemoteOp[] = ['cut.signoffs.list', 'project.show', 'keep.list'];

function signoffLine(s: Signoff): string {
  if (!s.done) return 'Still waiting';
  const who = s.doneBy?.userName || 'someone';
  return `OK'd by ${who}${s.doneAt ? ` ${relative(s.doneAt)}` : ''}${s.version ? `, for version ${s.version}` : ''}`;
}

function Signoffs({ projectId }: { projectId: string }) {
  const q = useOp('cut.signoffs.list', { projectId });
  const toast = useToast();
  const add = useOpMutation('cut.signoffs.add', { invalidate: SIGNOFF_QUERIES });
  const done = useOpMutation('cut.signoffs.done', { invalidate: SIGNOFF_QUERIES });
  const undo = useOpMutation('cut.signoffs.undo', { invalidate: SIGNOFF_QUERIES });
  const remove = useOpMutation('cut.signoffs.remove', { invalidate: SIGNOFF_QUERIES });
  const [text, setText] = useState('');
  // A tick shows at once; the list catches up when the server has it.
  const [ticked, setTicked] = useState<Record<string, boolean>>({});
  const list = q.data?.signoffs ?? [];
  const isDone = (s: Signoff) => ticked[s.id] ?? !!s.done;
  const open = list.filter((s) => !isDone(s)).length;
  useEffect(() => {
    setTicked((t) => {
      // Kept until the server says the same (an open sign-off has no "done" at all).
      const left = Object.fromEntries(Object.entries(t).filter(([id, v]) => list.some((s) => s.id === id) && !!list.find((s) => s.id === id)?.done !== v));
      return Object.keys(left).length === Object.keys(t).length ? t : left;
    });
  }, [list]);
  const failed = (e: Error) => toast({ tone: 'error', title: 'The sign-off did not change', body: e.message });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const t = text.trim();
    if (!t) return;
    add.mutate({ projectId, text: t }, { onSuccess: () => setText('') });
  };
  const toggle = (s: Signoff) => {
    const next = !isDone(s);
    setTicked((t) => ({ ...t, [s.id]: next }));
    (next ? done : undo).mutate(
      { projectId, signoff: s.id },
      {
        onError: (e) => {
          setTicked((t) => Object.fromEntries(Object.entries(t).filter(([id]) => id !== s.id)));
          failed(e);
        },
      },
    );
  };
  const takeOff = (s: Signoff) =>
    remove.mutate(
      { projectId, signoff: s.id },
      {
        onSuccess: () => toast({ title: 'Sign-off taken off', body: `“${s.text}”`, action: { label: 'Undo', onClick: () => add.mutate({ projectId, text: s.text }) } }),
        onError: failed,
      },
    );

  return (
    <Section
      name="signoffs"
      icon={<Signature size={16} />}
      title="Sign-offs"
      meta={list.length ? (open ? `${open} still waiting` : "everyone has OK'd it") : undefined}
      help="The OKs the cut needs before it goes out — this project's own, and the house sign-offs of its Content Settings. MetaRush reminds you before approving or downloading the final video; it never stops you."
    >
      {q.isLoading ? (
        <Skeleton className="h-16" />
      ) : q.error ? (
        <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />
      ) : list.length ? (
        <ul className="divide-y divide-line overflow-hidden rounded-[12px] hairline" aria-label="Sign-offs">
          {list.map((s) => {
            const on = isDone(s);
            const catching = ticked[s.id] !== undefined;
            return (
              <li key={s.id} className="flex items-start gap-2.5 px-3 py-2.5" data-signoff={on ? 'done' : 'open'} data-signoff-house={s.house || undefined}>
                <input
                  type="checkbox"
                  checked={on}
                  onChange={() => toggle(s)}
                  className="mt-[3px] h-4 w-4 shrink-0 cursor-pointer accent-[var(--accent)]"
                  aria-label={on ? `Untick “${s.text}”: the OK is needed again` : `Tick “${s.text}”: that OK was given`}
                />
                <div className="min-w-0 flex-1">
                  <Theirs className={cx('block break-words text-[13.5px] leading-snug', on ? 'text-muted' : 'text-fg')}>{s.text}</Theirs>
                  <p className={cx('mt-0.5 text-[12px] leading-relaxed', on ? 'text-ok' : 'text-subtle')} title={s.doneAt ? absolute(s.doneAt) : undefined}>
                    {catching ? (on ? "OK'd" : 'Still waiting') : signoffLine(s)}
                  </p>
                  {on && !catching && s.note ? <Theirs className="mt-0.5 block text-[12px] text-muted">“{s.note}”</Theirs> : null}
                </div>
                {s.house ? (
                  <Badge className="mt-[2px] shrink-0" title={`A house sign-off of ${s.houseOf}'s settings: every project using them needs it. Change it in Content Settings.`}>
                    from {s.houseOf}
                  </Badge>
                ) : (
                  <RemoveButton label={`Take “${s.text}” off the list`} onClick={() => takeOff(s)} busy={remove.isPending && remove.variables?.signoff === s.id} />
                )}
              </li>
            );
          })}
        </ul>
      ) : (
        <p className="text-[12.5px] leading-relaxed text-subtle">No sign-offs. Add who must OK the video, like “Lena OKs the final version”.</p>
      )}
      <form onSubmit={submit} className="mt-3 flex flex-wrap items-center gap-2" data-signoff-add>
        <input value={text} onChange={(e) => setText(e.target.value)} placeholder="Sam OKs the music" aria-label="Who must OK what" maxLength={200} className={cx(field, 'flex-1 basis-[180px]')} />
        <Button type="submit" variant="secondary" icon={<Plus size={13} />} disabled={!text.trim()} loading={add.isPending}>
          Add
        </Button>
      </form>
      {add.error ? <p className="mt-2 text-[12px] text-danger">{add.error.message}</p> : null}
    </Section>
  );
}

/* ---------- Where it goes (the brief's places) ---------- */

function WhereItGoes({ data }: { data: ProjectShowOut }) {
  const p = data.project;
  const toast = useToast();
  const update = useOpMutation('project.update', { invalidate: ['project.show', 'project.list', 'keep.list'] });
  const [name, setName] = useState('');
  const list = p.brief.deliverables ?? [];
  // The brief is saved whole: the places change, everything else stays as it is.
  const save = (next: Deliverable[], done?: () => void) =>
    update.mutate(
      { id: p.id, brief: { ...p.brief, deliverables: next } },
      { onSuccess: () => done?.(), onError: (e) => toast({ tone: 'error', title: 'Where it goes did not change', body: e.message }) },
    );
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const n = name.trim();
    if (!n) return;
    if (list.some((d) => d.name.trim().toLowerCase() === n.toLowerCase())) {
      setName('');
      return;
    }
    save([...list, { name: n }], () => setName(''));
  };
  const takeOut = (d: Deliverable) =>
    save(
      list.filter((x) => x !== d),
      () => toast({ title: 'Taken out of where it goes', body: `“${d.name}”`, action: { label: 'Undo', onClick: () => save([...list.filter((x) => x !== d), d]) } }),
    );

  return (
    <Section
      name="deliverables"
      icon={<MapPin size={16} />}
      title="Where it goes"
      meta={list.length ? plural(list.length, 'place') : undefined}
      help="Each place has the shape it needs: by email or on a website widescreen, social shorts vertical. New cuts and graphics are checked in these shapes."
    >
      {list.length ? (
        <ul className="divide-y divide-line overflow-hidden rounded-[12px] hairline" aria-label="Where the video goes">
          {list.map((d, i) => (
            <li key={`${d.name}-${i}`} className="flex items-center gap-2.5 px-3 py-2" data-deliverable={d.preset ?? ''}>
              <Theirs className="min-w-0 flex-1 break-words text-[13.5px] leading-snug text-fg">{d.name}</Theirs>
              {d.preset ? <Badge tone="info">{shapeWord(d.preset)}</Badge> : null}
              <RemoveButton label={`Take “${d.name}” out of where it goes`} onClick={() => takeOut(d)} busy={update.isPending} />
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-[12.5px] leading-relaxed text-subtle">Nowhere yet. Say where the video goes, like “by email” or “social shorts”.</p>
      )}
      <form onSubmit={submit} className="mt-3 flex flex-wrap items-center gap-2" data-deliverable-add>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="By email" aria-label="Where the video goes" maxLength={80} className={cx(field, 'flex-1 basis-[180px]')} />
        <Button type="submit" variant="secondary" icon={<Plus size={13} />} disabled={!name.trim() || list.length >= 12} loading={update.isPending && !!name.trim()}>
          Add
        </Button>
      </form>
    </Section>
  );
}

/* ---------- What conversations offered to keep ---------- */

const KEEP_QUERIES: RemoteOp[] = ['keep.list', 'project.rules.list', 'cut.signoffs.list', 'project.show', 'resources.list', 'transcript.spelling.list'];

/** A link as a person reads it: its site and page, without the scheme. */
function linkWords(u: string): string {
  return u
    .trim()
    .replace(/^https?:\/\//i, '')
    .replace(/^www\./i, '')
    .replace(/\/$/, '');
}

/** The thing, in the person's words. */
function itemWords(it: KeepItem): string {
  const d = it.detail ?? {};
  switch (it.kind) {
    case 'brief':
      return [d.goal, d.audience, d.tone, (d.deliverables ?? []).map((x) => x.name).join(', ')].filter(Boolean).join('; ') || it.text;
    case 'source':
      return linkWords(d.url || it.text);
    case 'picture':
      return d.file?.name || it.text || 'A picture';
    case 'person':
      return d.person ? `${d.person}: ${it.text}` : it.text;
  }
  return it.text;
}

function kindWord(it: KeepItem): string {
  if (it.kind === 'picture' && (it.detail?.brandKit || it.detail?.fromSite)) return 'Logo';
  return KIND_WORD[it.kind] ?? 'Kept';
}

function Offered({ projectId }: { projectId: string }) {
  const q = useOp('keep.list', { projectId });
  const toast = useToast();
  const add = useOpMutation('keep.add', { invalidate: KEEP_QUERIES });
  const items = q.data?.items ?? [];
  const groups: { state: KeepItem['state']; title: string; items: KeepItem[] }[] = [
    { state: 'kept', title: 'Kept', items: items.filter((i) => i.state === 'kept') },
    { state: 'declined', title: 'Not kept', items: items.filter((i) => i.state === 'declined') },
    { state: 'asked', title: 'Waiting for you', items: items.filter((i) => i.state === 'asked') },
  ];
  const keepAfterAll = (it: KeepItem) =>
    add.mutate(
      { projectId, kind: it.kind, text: it.text, detail: it.detail, from: 'person', ...(it.conversationId ? { conversationId: it.conversationId } : {}) },
      {
        onSuccess: (out) => toast({ tone: 'success', title: 'Kept', body: out.summary }),
        onError: (e) => toast({ tone: 'error', title: 'Not kept', body: [e.message, e.hint].filter(Boolean).join(' ') }),
      },
    );

  return (
    <Section
      name="offered"
      icon={<BookmarkSimple size={16} />}
      title="Offered in conversations"
      meta={items.length ? `${plural(items.length, 'thing')}, ${groups[0]!.items.length} kept` : undefined}
      help="What Claude Code conversations offered to keep. MetaRush asks once about each; what you set aside can still be kept here."
    >
      {q.isLoading ? (
        <Skeleton className="h-16" />
      ) : q.error ? (
        <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />
      ) : items.length === 0 ? (
        <p className="text-[12.5px] leading-relaxed text-subtle">Nothing yet. When a conversation brings up a rule, a sign-off, a link or a logo, Claude Code asks whether to keep it, and it shows here.</p>
      ) : (
        <div className="space-y-4">
          {groups
            .filter((g) => g.items.length)
            .map((g) => (
              <div key={g.state} data-offered={g.state}>
                <div className="mb-1.5 text-[12px] font-medium text-muted">
                  {g.title} <span className="tc text-subtle">{g.items.length}</span>
                </div>
                <ul className="divide-y divide-line overflow-hidden rounded-[12px] hairline">
                  {g.items.map((it) => (
                    <li key={it.id} className="flex flex-wrap items-start gap-x-2.5 gap-y-1.5 px-3 py-2.5" data-offered-item={it.kind}>
                      <Badge tone={it.state === 'kept' ? 'ok' : it.state === 'asked' ? 'accent' : 'neutral'} className="mt-[1px]">
                        {kindWord(it)}
                      </Badge>
                      <div className="min-w-0 flex-1 basis-[160px]">
                        <Theirs className="block break-words text-[13px] leading-snug text-fg">{itemWords(it)}</Theirs>
                        <p className="mt-0.5 text-[12px] leading-relaxed text-subtle" title={it.decidedAt ? absolute(it.decidedAt) : absolute(it.askedAt)}>
                          {it.state === 'kept'
                            ? it.outcome || `Kept ${relative(it.decidedAt)}`
                            : it.state === 'declined'
                              ? `Set aside${it.decidedBy?.userName ? ` by ${it.decidedBy.userName}` : ''} ${relative(it.decidedAt)}`
                              : `Asked ${relative(it.askedAt)}`}
                        </p>
                      </div>
                      {it.state === 'declined' ? (
                        <Button size="sm" variant="secondary" loading={add.isPending && add.variables?.text === it.text && add.variables?.kind === it.kind} disabled={add.isPending} onClick={() => keepAfterAll(it)} data-keep-after-all>
                          Keep after all
                        </Button>
                      ) : it.state === 'asked' && it.conversationId ? (
                        <Button size="sm" variant="ghost" icon={<ArrowSquareOut size={13} />} onClick={() => openAskRun(it.conversationId!)}>
                          Answer it
                        </Button>
                      ) : null}
                    </li>
                  ))}
                </ul>
              </div>
            ))}
        </div>
      )}
    </Section>
  );
}
