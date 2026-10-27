import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { CheckCircle, Sparkle, UserCircle, Warning } from '@phosphor-icons/react';
import { call } from '../../api/client';
import { useOp } from '../../api/queries';
import type { AIEstimate, AIMakeIn, PresenterMakeIn, PresenterView, ProjectShowOut } from '../../api/types';
import { useToast } from '../../ui/Toast';
import { useDialog } from '../../ui/Dialog';
import { useEstimatesFirst } from '../../lib/preferences';
import { estimateWords } from '../../lib/costs';
import { Badge, Button, ErrorPanel, Spinner, Theirs, type Tone } from '../../ui/primitives';
import { AIEstimateLines, AIWorkList } from './AIWork';

/*
 * Presenters in the Generate panel (D273–D277): a presenter reads your lines
 * on camera from the project's own sound — a person's own presenter (made at
 * a service from their approved look, a training take and their consent
 * recorded on camera, naming that service), or a stock presenter whose
 * licence covers commercial use and client work. Each clip is priced first
 * and lands in Sources marked as an AI presenter. Beginning, making,
 * testing, approving and retiring a person's presenter are a person's steps.
 */

const stateWords: Record<PresenterView['state'], { label: string; tone: Tone }> = {
  waiting: { label: 'Waiting for the take', tone: 'neutral' },
  training: { label: 'Being made', tone: 'accent' },
  draft: { label: 'Test it', tone: 'info' },
  approved: { label: 'Approved', tone: 'ok' },
  failed: { label: "Couldn't be made", tone: 'danger' },
  retired: { label: 'Retired', tone: 'neutral' },
};

const inputCls = 'h-9 w-full rounded-[10px] bg-panel px-2.5 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent';
const labelCls = 'mb-1.5 block text-[12.5px] font-medium text-muted';

function num(v: string): number | undefined {
  const n = Number(v);
  return v.trim() !== '' && Number.isFinite(n) ? n : undefined;
}

/** The presenter kind: who reads which sound, priced first; then the presenters themselves. */
export function PresenterWork({ data, onOpenClip }: { data: ProjectShowOut; onOpenClip: (assetId: string) => void }) {
  const uid = useId();
  const toast = useToast();
  const qc = useQueryClient();
  const projectId = data.project.id;
  const estimatesFirst = useEstimatesFirst();
  const presenters = useOp('presenter.list', {}, { refetchInterval: 5000 });
  const stock = useOp('presenter.stock', {}, { staleTime: 60_000 });
  const models = useOp('ai.models', { kind: 'presenter', projectId });
  const [who, setWho] = useState('');
  const [clip, setClip] = useState('');
  const [line, setLine] = useState('');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [background, setBackground] = useState('');
  const [est, setEst] = useState<AIEstimate | null>(null);
  const [estError, setEstError] = useState<unknown>(null);
  const [pricing, setPricing] = useState(false);
  const [sending, setSending] = useState(false);

  const own = (presenters.data?.presenters ?? []).filter((p) => p.state === 'approved' || p.state === 'draft');
  const stockOptions = (stock.data?.services ?? []).flatMap((s) =>
    s.offered
      ? s.presenters.map((p) => ({
          ...p,
          provider: s.provider,
          providerTitle: s.providerTitle,
        }))
      : [],
  );
  const chosen = own.find((p) => p.name === who);
  const chosenStock = stockOptions.find((p) => `stock:${p.id}` === who);
  const stockModel = chosenStock ? (models.data?.kinds?.[0]?.models ?? []).find((m) => m.provider === chosenStock.provider) : undefined;
  const test = chosen?.state === 'draft';
  const sounds = data.assets.filter((a) => a.hasAudio && a.state === 'available');
  const lineNo = num(line);
  const ready0 = who !== '' && (clip !== '' || (lineNo !== undefined && lineNo > 0));

  const makeIn = useMemo<AIMakeIn>(
    () => ({
      kind: 'presenter',
      projectId,
      presenter: who,
      ...(stockModel ? { model: `${stockModel.provider}/${stockModel.id}` } : {}),
      ...(lineNo && lineNo > 0 ? { line: lineNo } : { clip }),
      ...(!(lineNo && lineNo > 0) && num(from) !== undefined ? { from: num(from) } : {}),
      ...(!(lineNo && lineNo > 0) && num(to) !== undefined ? { to: num(to) } : {}),
      options: background ? { background } : {},
    }),
    [projectId, who, stockModel, lineNo, clip, from, to, background],
  );
  const inputKey = JSON.stringify(makeIn);
  const [estKey, setEstKey] = useState('');
  const latest = useRef('');
  useEffect(() => {
    latest.current = inputKey;
    if (!ready0) {
      setEst(null);
      setEstKey('');
      setEstError(null);
      return;
    }
    const t = setTimeout(async () => {
      setPricing(true);
      try {
        const e = test
          ? (
              await call('presenter.test', {
                id: who,
                clip,
                projectId,
                ...(num(from) !== undefined ? { from: num(from) } : {}),
                ...(num(to) !== undefined ? { to: num(to) } : {}),
                dryRun: true,
              })
            ).estimate
          : await call('ai.estimate', makeIn);
        if (latest.current !== inputKey) return;
        setEst(e);
        setEstKey(inputKey);
        setEstError(null);
      } catch (err) {
        if (latest.current !== inputKey) return;
        setEst(null);
        setEstError(err);
      } finally {
        if (latest.current === inputKey) setPricing(false);
      }
    }, 450);
    return () => clearTimeout(t);
  }, [inputKey, ready0, test, who, clip, from, to, projectId, makeIn]);
  const priced = !!est && estKey === inputKey;
  const ready = estimatesFirst ? priced && !!est?.allowed && !pricing : ready0 && !(priced && est && !est.allowed);

  const go = async () => {
    if (!ready) return;
    setSending(true);
    try {
      const maxUsd = priced && est ? { maxUsd: est.usd } : {};
      const out = test
        ? await call('presenter.test', {
            id: who,
            clip,
            projectId,
            ...(num(from) !== undefined ? { from: num(from) } : {}),
            ...(num(to) !== undefined ? { to: num(to) } : {}),
            ...maxUsd,
          })
        : await call('ai.make', { ...makeIn, ...maxUsd });
      ['ai.list', 'presenter.list', 'activity.summary', 'project.show'].forEach((q) => void qc.invalidateQueries({ queryKey: [q] }));
      toast({ tone: 'success', title: 'Asked for it', body: out.summary });
    } catch (e) {
      setEstError(e);
    } finally {
      setSending(false);
    }
  };

  return (
    <div data-ai-work="presenter">
      <div className="grid grid-cols-1 gap-5 px-5 py-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(300px,1fr)]">
        <div className="space-y-4">
          <div>
            <label htmlFor={`${uid}-who`} className={labelCls}>
              Who reads it
            </label>
            <select id={`${uid}-who`} value={who} onChange={(e) => setWho(e.target.value)} className={inputCls} data-presenter-who>
              <option value="">Choose a presenter</option>
              {own.map((p) => (
                <option key={p.id} value={p.name}>
                  {p.title} ({p.providerTitle}){p.state === 'draft' ? ' — a test clip' : ''}
                </option>
              ))}
              {stockOptions.map((p) => (
                <option key={p.id} value={`stock:${p.id}`}>
                  {p.name} ({p.providerTitle}’s stock presenter)
                </option>
              ))}
            </select>
            {own.length === 0 && stockOptions.length === 0 && !presenters.isLoading ? (
              <p className="mt-1 text-[12px] text-subtle">No presenters yet: begin a person’s own presenter below, with their consent.</p>
            ) : null}
          </div>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-[minmax(0,1fr)_90px_90px]">
            <div>
              <label htmlFor={`${uid}-clip`} className={labelCls}>
                The sound it reads
              </label>
              <select id={`${uid}-clip`} value={clip} onChange={(e) => setClip(e.target.value)} className={inputCls} data-presenter-clip disabled={!!lineNo}>
                <option value="">Choose a clip with sound</option>
                {sounds.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.title}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label htmlFor={`${uid}-f`} className={labelCls}>
                From (s)
              </label>
              <input
                id={`${uid}-f`}
                inputMode="decimal"
                value={from}
                onChange={(e) => setFrom(e.target.value)}
                placeholder="start"
                className={inputCls}
                disabled={!!lineNo}
              />
            </div>
            <div>
              <label htmlFor={`${uid}-t`} className={labelCls}>
                To (s)
              </label>
              <input
                id={`${uid}-t`}
                inputMode="decimal"
                value={to}
                onChange={(e) => setTo(e.target.value)}
                placeholder="end"
                className={inputCls}
                disabled={!!lineNo}
              />
            </div>
          </div>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div>
              <label htmlFor={`${uid}-line`} className={labelCls}>
                Or a narration line of the cut
              </label>
              <input
                id={`${uid}-line`}
                inputMode="numeric"
                value={line}
                onChange={(e) => setLine(e.target.value)}
                placeholder="line number, e.g. 3"
                className={inputCls}
                disabled={test}
              />
            </div>
            {chosen?.greenScreen ? (
              <div>
                <label htmlFor={`${uid}-bg`} className={labelCls}>
                  Behind the presenter
                </label>
                <select id={`${uid}-bg`} value={background} onChange={(e) => setBackground(e.target.value)} className={inputCls}>
                  <option value="">As it was filmed</option>
                  <option value="green">Green, to key out later</option>
                  <option value="#ffffff">White</option>
                  <option value="#000000">Black</option>
                </select>
              </div>
            ) : null}
          </div>
        </div>
        <div className="flex flex-col gap-3">
          <div className="min-h-[120px] rounded-[14px] bg-hover/60 px-4 py-3.5" aria-live="polite">
            {!ready0 ? (
              <p className="text-[13px] text-subtle">A presenter reads your lines on camera. Choose who reads it and the sound, to see what it costs.</p>
            ) : pricing && !est ? (
              <span className="inline-flex items-center gap-2 text-[13px] text-muted">
                <Spinner size={12} /> Pricing it…
              </span>
            ) : est ? (
              <AIEstimateLines estimate={est} />
            ) : null}
            {estError ? <ErrorPanel compact error={estError} className="mt-2" /> : null}
          </div>
          <Button
            variant="primary"
            icon={<Sparkle size={14} weight="fill" />}
            disabled={!ready}
            loading={sending}
            onClick={() => void go()}
            data-presenter-go
            data-estimates-first={estimatesFirst ? 'on' : 'off'}
          >
            {est && priced ? `${test ? 'Make a test clip' : 'Make it'} (${estimateWords(est.usd)})` : test ? 'Make a test clip' : 'Make it'}
          </Button>
          <p className="text-[12px] leading-relaxed text-subtle">
            Nothing is sent until you press it. The clip lands in Sources marked as an AI presenter, with your own sound under it.
          </p>
        </div>
      </div>
      <PresenterList data={data} presenters={presenters.data?.presenters ?? []} />
      <AIWorkList projectId={projectId} kind="presenter" onOpenClip={onOpenClip} />
    </div>
  );
}

/** The presenters: where each stands, the words to read, and each person's next step. */
function PresenterList({ data, presenters }: { data: ProjectShowOut; presenters: PresenterView[] }) {
  const toast = useToast();
  const dialog = useDialog();
  const qc = useQueryClient();
  const refresh = () => ['presenter.list', 'ai.list', 'activity.summary'].forEach((q) => void qc.invalidateQueries({ queryKey: [q] }));
  const decide = async (p: PresenterView, approve: boolean) => {
    if (!approve) {
      const ok = await dialog.confirm({
        title: `Retire “${p.title}”?`,
        body: `Nothing more is made with it, and it is deleted at ${p.providerTitle}. Clips already made stay in your cuts, marked as made with consent since withdrawn.`,
        confirmLabel: 'Retire it',
        tone: 'danger',
      });
      if (!ok) return;
    }
    try {
      const out = await call(approve ? 'presenter.approve' : 'presenter.retire', { id: p.id });
      refresh();
      toast({
        tone: 'success',
        title: approve ? 'Approved' : 'Retired',
        body: out.summary,
      });
    } catch (e) {
      toast({
        tone: 'error',
        title: approve ? 'Not approved' : 'Not retired',
        body: e instanceof Error ? e.message : String(e),
      });
    }
  };
  const words = async (p: PresenterView) => {
    try {
      const out = await call('presenter.start', { again: p.id });
      refresh();
      toast({ tone: 'success', title: 'New words', body: out.summary });
    } catch (e) {
      toast({
        tone: 'error',
        title: 'No new words',
        body: e instanceof Error ? e.message : String(e),
      });
    }
  };
  return (
    <div className="space-y-3 border-t border-line px-5 py-4" data-presenters>
      <div className="flex items-center gap-2">
        <h3 className="text-[13.5px] font-semibold">People’s own presenters</h3>
        <span className="text-[12px] text-subtle">Made only with the person’s consent, recorded on camera.</span>
      </div>
      {presenters.length > 0 ? (
        <ul className="grid grid-cols-1 gap-3 lg:grid-cols-2" aria-label="Presenters">
          {presenters.map((p) => {
            const st = stateWords[p.state];
            return (
              <li key={p.id} className="flex flex-col gap-1.5 rounded-[12px] p-3 hairline" data-presenter={p.state} data-presenter-name={p.name}>
                <div className="flex flex-wrap items-center gap-1.5">
                  <UserCircle size={16} className="text-muted" />
                  <span className="text-[13px] font-medium text-fg">
                    <Theirs>{p.title}</Theirs>
                  </span>
                  <Badge tone={st.tone}>{st.label}</Badge>
                  <span className="text-[11.5px] text-subtle">{p.providerTitle}</span>
                </div>
                <p className="text-[12.5px] leading-snug text-muted">{p.summary}</p>
                {p.words ? (
                  <p className="rounded-[10px] bg-accent-soft px-2.5 py-2 text-[12.5px] text-accent-text" data-presenter-words>
                    {p.providerTitle}’s words to read on camera: “<Theirs>{p.words}</Theirs>”
                  </p>
                ) : null}
                {p.next ? <p className="text-[12px] text-subtle">{p.next}</p> : null}
                {p.state === 'waiting' || p.state === 'failed' ? <MakeForm data={data} p={p} onDone={refresh} /> : null}
                <div className="flex flex-wrap gap-2">
                  {p.state === 'draft' ? (
                    <Button size="sm" variant="ghost" icon={<CheckCircle size={13} />} onClick={() => void decide(p, true)} data-presenter-approve>
                      Approve
                    </Button>
                  ) : null}
                  {(p.state === 'waiting' || p.state === 'failed') && p.challenge ? (
                    <Button size="sm" variant="ghost" onClick={() => void words(p)}>
                      New words
                    </Button>
                  ) : null}
                  {p.state !== 'retired' ? (
                    <Button size="sm" variant="ghost" icon={<Warning size={13} />} onClick={() => void decide(p, false)} data-presenter-retire>
                      Retire
                    </Button>
                  ) : null}
                  <Link to={p.link} className="self-center text-[12px] font-medium text-accent-text hover:underline">
                    {p.personName} on the People page
                  </Link>
                </div>
                {p.erasure ? <p className="text-[11.5px] text-subtle">{p.erasure}</p> : null}
              </li>
            );
          })}
        </ul>
      ) : null}
      <BeginForm onDone={refresh} />
    </div>
  );
}

/** Begin a person's presenter: the service is asked for the words they read on camera. */
function BeginForm({ onDone }: { onDone: () => void }) {
  const uid = useId();
  const toast = useToast();
  const people = useOp('people.list', {}, { staleTime: 60_000 });
  const [person, setPerson] = useState('');
  const [green, setGreen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const begin = async () => {
    setBusy(true);
    try {
      const out = await call('presenter.start', {
        person,
        greenScreen: green || undefined,
      });
      setError(null);
      setPerson('');
      onDone();
      toast({ tone: 'success', title: 'Begun', body: out.summary });
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="rounded-[12px] bg-hover/40 p-3" data-presenter-begin>
      <div className="flex flex-wrap items-end gap-3">
        <div className="min-w-[200px] flex-1">
          <label htmlFor={`${uid}-person`} className={labelCls}>
            Begin a person’s own presenter
          </label>
          <select id={`${uid}-person`} value={person} onChange={(e) => setPerson(e.target.value)} className={inputCls} data-presenter-person>
            <option value="">Choose a person with an approved look</option>
            {(people.data?.people ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </div>
        <label className="flex items-center gap-2 pb-2 text-[12.5px] text-fg">
          <input type="checkbox" checked={green} onChange={(e) => setGreen(e.target.checked)} /> Filmed against a green screen
        </label>
        <Button size="sm" disabled={!person} loading={busy} onClick={() => void begin()} data-presenter-start>
          Begin
        </Button>
      </div>
      <p className="mt-1.5 text-[12px] text-subtle">
        The service gives the words they read on camera. Their consent, recorded on camera and naming the service, goes on the{' '}
        <Link to="/library/people" className="font-medium text-accent-text hover:underline">
          People page
        </Link>
        .
      </p>
      {error ? <ErrorPanel compact error={error} className="mt-2" /> : null}
    </div>
  );
}

/** Send a waiting presenter's training take and the reading of the service's words: priced first. */
function MakeForm({ data, p, onDone }: { data: ProjectShowOut; p: PresenterView; onDone: () => void }) {
  const uid = useId();
  const toast = useToast();
  const estimatesFirst = useEstimatesFirst();
  const videos = data.assets.filter((a) => a.kind === 'video' && a.hasAudio && a.state === 'available');
  const [take, setTake] = useState('');
  const [reading, setReading] = useState('');
  const [rFrom, setRFrom] = useState('');
  const [rTo, setRTo] = useState('');
  const [shows, setShows] = useState(false);
  const [est, setEst] = useState<AIEstimate | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  // A person's word counts only where MetaRush hasn't looked for faces in the take;
  // once it has, the faces must be named as the person.
  const unlooked = !!take && videos.find((a) => a.id === take)?.analysis?.visual !== 'available';
  const input = useMemo<PresenterMakeIn>(
    () => ({
      id: p.id,
      take,
      projectId: data.project.id,
      ...(reading ? { reading } : {}),
      ...(num(rFrom) !== undefined ? { readingFrom: num(rFrom) } : {}),
      ...(num(rTo) !== undefined ? { readingTo: num(rTo) } : {}),
      ...(shows && unlooked ? { showsPerson: true } : {}),
    }),
    [p.id, take, reading, rFrom, rTo, shows, unlooked, data.project.id],
  );
  const key = JSON.stringify(input);
  useEffect(() => {
    if (!take) {
      setEst(null);
      return;
    }
    let live = true;
    const t = setTimeout(async () => {
      try {
        const out = await call('presenter.make', { ...input, dryRun: true });
        if (live) {
          setEst(out.estimate ?? null);
          setError(null);
        }
      } catch (e) {
        if (live) {
          setEst(null);
          setError(e);
        }
      }
    }, 450);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [key, take, input]);
  const make = async () => {
    setBusy(true);
    try {
      const out = await call('presenter.make', {
        ...input,
        ...(est ? { maxUsd: est.usd } : {}),
      });
      onDone();
      toast({ tone: 'success', title: 'Sent', body: out.summary });
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const can = estimatesFirst ? !!est?.allowed : !!take && !(est && !est.allowed);
  return (
    <div className="space-y-2 rounded-[10px] bg-hover/40 p-2.5" data-presenter-make>
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <div>
          <label htmlFor={`${uid}-take`} className={labelCls}>
            The training take
          </label>
          <select id={`${uid}-take`} value={take} onChange={(e) => setTake(e.target.value)} className={inputCls} data-presenter-take>
            <option value="">Choose a video of them</option>
            {videos.map((a) => (
              <option key={a.id} value={a.id}>
                {a.title}
              </option>
            ))}
          </select>
        </div>
        {p.challenge ? (
          <div>
            <label htmlFor={`${uid}-reading`} className={labelCls}>
              Them reading the words, in their consent recording
            </label>
            <select id={`${uid}-reading`} value={reading} onChange={(e) => setReading(e.target.value)} className={inputCls} data-presenter-reading>
              <option value="">Their consent recording</option>
              {videos.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.title}
                </option>
              ))}
            </select>
            <div className="mt-1.5 grid grid-cols-2 gap-2">
              <input
                aria-label="Reading from (s)"
                inputMode="decimal"
                value={rFrom}
                onChange={(e) => setRFrom(e.target.value)}
                placeholder="from (s)"
                className={inputCls}
              />
              <input
                aria-label="Reading to (s)"
                inputMode="decimal"
                value={rTo}
                onChange={(e) => setRTo(e.target.value)}
                placeholder="to (s)"
                className={inputCls}
              />
            </div>
          </div>
        ) : null}
      </div>
      {unlooked ? (
        <label className="flex items-start gap-2 text-[12.5px] text-fg">
          <input type="checkbox" checked={shows} onChange={(e) => setShows(e.target.checked)} className="mt-[3px]" data-presenter-shows />
          <span>The take shows {p.personName}, on their own (MetaRush hasn't looked for faces in it yet)</span>
        </label>
      ) : null}
      {est ? <AIEstimateLines estimate={est} /> : null}
      {error ? <ErrorPanel compact error={error} /> : null}
      <Button size="sm" variant="primary" disabled={!can} loading={busy} onClick={() => void make()} data-presenter-send>
        {est ? `Make the presenter (${estimateWords(est.usd)})` : 'Make the presenter'}
      </Button>
    </div>
  );
}
