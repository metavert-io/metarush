import { useEffect, useId, useMemo, useState } from 'react';
import { Link } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { CheckCircle, FilmSlate, Plus, ShieldCheck, Sparkle, UserCircle, WarningCircle } from '@phosphor-icons/react';
import { call, media, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import { resourceMedia } from '../api/resources';
import type { GenEstimate, LookCreateIn, LookView, PersonSummary } from '../api/types';
import { estimateWords } from '../lib/costs';
import { EstimateLines } from '../components/generate/EstimateLines';
import { ResourcePicture } from '../components/Resources';
import { absolute } from '../lib/format';
import { resetSelection } from '../lib/workbench';
import { useDialog } from '../ui/Dialog';
import { useToast } from '../ui/Toast';
import { Badge, Button, cx, Details, EmptyState, ErrorPanel, Segmented, Skeleton, Spinner, Thumb, type Tone } from '../ui/primitives';

/*
 * Looks and consent (D126): how a person (with their consent) or a fictional
 * character appears in generated video. A person's look is 2 or 3 stills and
 * up to three short clips of them from your footage, a fixed description and
 * a test sheet; only people make, approve or withdraw one. Consent: your own
 * likeness takes a statement in your words; anyone else's, a recording of
 * them saying they agree, with what it covers and until when. MetaRush never
 * records consent for a child or a public figure.
 */

const LIKENESS_QUERIES = ['looks.list', 'looks.show', 'consent.list', 'people.show', 'people.list', 'generate.estimate'] as const;

const stateBadge: Record<LookView['state'], { label: string; tone: Tone }> = {
  draft: { label: 'Not approved yet', tone: 'accent' },
  approved: { label: 'Approved', tone: 'ok' },
  revoked: { label: 'Withdrawn', tone: 'neutral' },
};

function useRefresh() {
  const qc = useQueryClient();
  return () => LIKENESS_QUERIES.forEach((q) => void qc.invalidateQueries({ queryKey: [q] }));
}

/** Looks and consent: every look, and how to make one (the last part of Settings → Integrations, id="looks", D184). */
export function LooksPage() {
  useEffect(() => resetSelection({}), []);
  const looks = useOp('looks.list', { all: true });
  const consents = useOp('consent.list', {});
  const dialog = useDialog();
  const toast = useToast();
  const refresh = useRefresh();
  const newCharacter = async () => {
    const out = await dialog.open<string>((close) => <CreateLookForm onClose={close} />, { title: 'A new character look', size: 'lg' });
    if (out) {
      refresh();
      toast({ tone: 'success', title: 'Look made', body: out });
    }
  };
  const list = looks.data?.looks ?? [];
  return (
    <section id="looks" aria-labelledby="looks-h" className="scroll-mt-28" data-looks-section>
      <div className="mb-6 flex flex-wrap items-end gap-3">
        <div className="min-w-0">
          <h2 id="looks-h" className="text-[18px] font-semibold tracking-[-0.02em]">
            Looks and consent
          </h2>
          <p className="mt-1 max-w-[72ch] text-[13px] leading-relaxed text-muted">
            How people and characters appear in generated video. A person's look needs their consent first: open them in Library → People. A character's look is made from
            pictures, never from a real person's face.
          </p>
        </div>
        <div className="ml-auto flex gap-2">
          <Link to="/library/people" className="inline-flex h-9 items-center gap-1.5 rounded-[10px] px-3 text-[13px] font-medium text-muted hairline hover:bg-hover hover:text-fg">
            <UserCircle size={15} /> People
          </Link>
          <Button variant="primary" icon={<Plus size={14} />} onClick={() => void newCharacter()}>
            New character look
          </Button>
        </div>
      </div>
      {looks.error ? <ErrorPanel error={looks.error} onRetry={() => void looks.refetch()} /> : null}
      {looks.isLoading ? (
        <Skeleton className="h-48 rounded-[16px]" />
      ) : list.length === 0 ? (
        <div className="panel">
          <EmptyState
            icon={<Sparkle size={22} />}
            title="No looks yet"
            body="Open someone in Library → People to record their consent and make their look, or make a character look from pictures."
          />
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2" data-looks>
          {list.map((l) => (
            <LookCard key={l.id} look={l} />
          ))}
        </div>
      )}
      <section className="mt-10" aria-labelledby="consents-h">
        <h2 id="consents-h" className="mb-2 text-[16px] font-semibold">
          Consent on record
        </h2>
        {(consents.data?.consents ?? []).length === 0 ? (
          <p className="text-[13px] text-muted">No consent recorded yet.</p>
        ) : (
          <ul className="space-y-2" data-consents>
            {consents.data!.consents.map((c) => (
              <li key={c.id} className="flex flex-wrap items-center gap-2 rounded-[12px] px-3.5 py-2.5 hairline">
                <ShieldCheck size={15} className={c.status === 'active' ? 'text-ok' : 'text-subtle'} />
                <span className="min-w-0 flex-1 text-[13px]">
                  {c.summary}
                  {c.voiceCopies?.map((v) => (
                    <span key={v} className="block text-[12px] text-muted" data-consent-voice-copy>
                      {v}
                    </span>
                  ))}
                  {c.presenters?.map((v) => (
                    <span key={v} className="block text-[12px] text-muted" data-consent-presenter>
                      Presenter {v}
                    </span>
                  ))}
                </span>
                <Link to={c.link} className="text-[12.5px] font-medium text-accent-text hover:underline">
                  {c.personName}
                </Link>
              </li>
            ))}
          </ul>
        )}
      </section>
    </section>
  );
}

/** One look: its stills, where it stands, and what a person can do with it. */
export function LookCard({ look: l }: { look: LookView }) {
  const dialog = useDialog();
  const toast = useToast();
  const refresh = useRefresh();
  const [open, setOpen] = useState(false);
  const show = useOp('looks.show', open ? { look: l.id } : null);
  const st = stateBadge[l.state];
  const decide = async (approve: boolean) => {
    const ok = await dialog.confirm(
      approve
        ? {
            title: `Approve the look “${l.title}”?`,
            body: 'New shots can use it once it is approved. Look at its test shots first: they show how the video model sees it.',
            confirmLabel: 'Approve look',
          }
        : {
            title: `Withdraw the look “${l.title}”?`,
            body: 'Nothing new is made with it. Clips already made with it stay in your projects.',
            confirmLabel: 'Withdraw look',
            tone: 'danger',
          },
    );
    if (!ok) return;
    try {
      const out = await call(approve ? 'looks.approve' : 'looks.revoke', { look: l.id });
      refresh();
      toast({ tone: 'success', title: out.summary });
    } catch (e) {
      toast({ tone: 'error', title: 'Not saved', body: toApiError(e).message });
    }
  };
  const test = async () => {
    const out = await dialog.open<string>((close) => <TestForm look={l} onClose={close} />, { title: `Test shots of ${l.title}`, size: 'md' });
    if (out) {
      refresh();
      setOpen(true);
      toast({ tone: 'success', title: 'Test shots asked for', body: out });
    }
  };
  return (
    <article className="panel p-4" aria-label={`Look ${l.title}`} data-look={l.name}>
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-[15px] font-semibold">{l.title}</h3>
        <Badge tone={st.tone}>{st.label}</Badge>
        <span className="text-[12px] text-subtle">{l.subject === 'person' ? l.personName : 'a character'}</span>
        {l.style === 'stylized' ? <Badge tone="info">{l.styleNote}</Badge> : null}
      </div>
      <div className="mt-3 flex flex-wrap gap-2" aria-label="Stills and clips">
        {l.stills.map((s) => (
          <figure key={s.ref} className="w-[112px]">
            {s.assetId ? (
              <Thumb src={media.frame(s.assetId, s.at ?? 0, 240)} alt={`${l.title}, ${s.ref}`} className="aspect-video w-full rounded-[8px]" />
            ) : s.resourceId ? (
              <Thumb src={resourceMedia.thumb(s.resourceId)} alt={`${l.title}, ${s.ref}`} className="aspect-video w-full rounded-[8px]" />
            ) : (
              <div className="grid aspect-video w-full place-items-center rounded-[8px] bg-inset text-[11px] text-subtle">a picture</div>
            )}
          </figure>
        ))}
        {l.clips.map((c) => (
          <figure key={c.ref} className="relative w-[112px]">
            <Thumb src={media.frame(c.assetId, c.from + (c.to - c.from) / 2, 240)} alt={`${l.title}, ${c.ref}`} className="aspect-video w-full rounded-[8px]" />
            <span className="absolute bottom-1 right-1 rounded-[5px] bg-black/70 px-1 text-[10px] text-white">{(c.to - c.from).toFixed(1)} s</span>
          </figure>
        ))}
      </div>
      <p className="mt-2 text-[12.5px] leading-relaxed text-muted">{l.description}</p>
      {l.problems?.map((p) => (
        // A look waiting for approval isn't in trouble: only an approved look that can't be used is.
        <p key={p} className={cx('mt-1 flex items-start gap-1.5 text-[12.5px]', l.state === 'approved' ? 'text-danger' : 'text-muted')}>
          <WarningCircle size={14} weight="fill" className="mt-[2px] shrink-0" /> {p}
        </p>
      ))}
      {l.consent ? <p className="mt-1 text-[12px] text-subtle">{l.consent.summary}</p> : null}
      {l.state !== 'revoked' ? (
        <div className="mt-3 flex flex-wrap gap-1.5">
          <Button size="sm" variant="secondary" icon={<FilmSlate size={13} />} onClick={() => void test()}>
            Make test shots
          </Button>
          {l.state === 'draft' ? (
            <Button size="sm" variant="primary" icon={<CheckCircle size={13} />} onClick={() => void decide(true)}>
              Approve
            </Button>
          ) : null}
          <Button size="sm" variant="ghost" onClick={() => void decide(false)}>
            Withdraw
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
            {open ? 'Hide test sheet' : 'Test sheet'}
          </Button>
        </div>
      ) : null}
      {open ? (
        <div className="mt-3" data-test-sheet>
          {show.isLoading ? (
            <Skeleton className="h-20" />
          ) : (show.data?.testShots ?? []).length === 0 ? (
            <p className="text-[12.5px] text-subtle">No test shots yet. Make a few short drafts to see how the video model shows {l.title}.</p>
          ) : (
            <ul className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              {show.data!.testShots!.map((t) => (
                <li key={t.id} className="rounded-[10px] p-1.5 hairline">
                  {t.clips[0] ? (
                    <Link to={t.clips[0].link}>
                      <Thumb src={media.frame(t.clips[0].assetId, 1, 240)} alt={t.prompt} className="aspect-video w-full rounded-[7px]" />
                    </Link>
                  ) : (
                    <div className="grid aspect-video w-full place-items-center rounded-[7px] bg-inset text-subtle">
                      {t.state === 'making' || t.state === 'waiting' || t.state === 'sending' ? <Spinner size={14} /> : <FilmSlate size={16} />}
                    </div>
                  )}
                  <p className="mt-1 line-clamp-2 text-[11.5px] text-muted">{t.summary}</p>
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : null}
      <Details compact>
        <span className="tc">
          {l.ref} · made {absolute(l.createdAt)}
          {l.approvedAt ? ` · approved ${absolute(l.approvedAt)}` : ''}
        </span>
      </Details>
    </article>
  );
}

/** Test shots of a look: priced first. */
function TestForm({ look: l, onClose }: { look: LookView; onClose: (summary?: string) => void }) {
  const [est, setEst] = useState<GenEstimate | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    call('looks.test', { look: l.id, dryRun: true })
      .then((o) => setEst(o.estimate))
      .catch(setError);
  }, [l.id]);
  const go = async () => {
    if (!est) return;
    setBusy(true);
    try {
      const out = await call('looks.test', { look: l.id, maxUsd: est.usd });
      onClose(out.summary);
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };
  return (
    <div className="space-y-4">
      <p className="text-[13.5px] leading-relaxed text-muted">
        Two short drafts of {l.title} (3 seconds, 360p): one looking at the camera, one walking across a room. They go on the look's test sheet, not into a project.
      </p>
      <div className="min-h-[60px] rounded-[12px] bg-hover/60 px-3.5 py-3">{est ? <EstimateLines estimate={est} /> : error ? null : <Spinner size={13} />}</div>
      {error ? <ErrorPanel compact error={error} /> : null}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={() => onClose()} disabled={busy}>
          Cancel
        </Button>
        <Button variant="primary" loading={busy} disabled={!est || !est.allowed} onClick={() => void go()}>
          {est ? `Make them (${estimateWords(est.usd)})` : 'Make them'}
        </Button>
      </div>
    </div>
  );
}

/**
 * A person's likeness on the People page: their consent and their looks,
 * with the two things only a person does here — record consent, make a look.
 */
export function LikenessSection({ person: p }: { person: PersonSummary }) {
  const dialog = useDialog();
  const toast = useToast();
  const refresh = useRefresh();
  const consents = useOp('consent.list', { person: p.id });
  const looks = useOp('looks.list', { person: p.id, all: true });
  const show = useOp('people.show', { id: p.id });
  const active = (consents.data?.consents ?? []).find((c) => c.status === 'active');
  const faces = show.data?.faces ?? [];
  const record = async () => {
    const out = await dialog.open<string>((close) => <ConsentForm person={p} onClose={close} />, { title: `Consent for ${p.name}'s likeness`, size: 'md' });
    if (out) {
      refresh();
      toast({ tone: 'success', title: 'Consent recorded', body: out });
    }
  };
  const withdraw = async () => {
    if (!active) return;
    const ok = await dialog.confirm({
      title: `Withdraw ${p.name}'s consent?`,
      body: `Nothing new is made with ${p.name}'s likeness or voice: the looks and voice copies that rely on it are withdrawn too (voice copies are deleted at the provider). Clips already made stay, marked as made with consent that was withdrawn.`,
      confirmLabel: 'Withdraw consent',
      tone: 'danger',
    });
    if (!ok) return;
    try {
      const out = await call('consent.revoke', { id: active.id });
      refresh();
      toast({ tone: 'success', title: out.summary });
    } catch (e) {
      toast({ tone: 'error', title: 'Not withdrawn', body: toApiError(e).message });
    }
  };
  const makeLook = async () => {
    const out = await dialog.open<string>((close) => <CreateLookForm person={p} faces={faces} onClose={close} />, { title: `A look of ${p.name}`, size: 'lg' });
    if (out) {
      refresh();
      toast({ tone: 'success', title: 'Look made', body: out });
    }
  };
  return (
    <section aria-label={`${p.name} in generated video`} data-likeness>
      <h3 className="mb-2 mt-6 text-[13.5px] font-semibold">{p.name} in generated video</h3>
      {active ? (
        <div className="flex flex-wrap items-center gap-2 rounded-[12px] bg-ok-soft/60 px-3 py-2.5 text-[13px]">
          <ShieldCheck size={15} className="text-ok" />
          <span className="min-w-0 flex-1">{active.summary}</span>
          <Button size="sm" variant="ghost" onClick={() => void withdraw()}>
            Withdraw consent
          </Button>
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-2 rounded-[12px] px-3 py-2.5 text-[13px] hairline">
          <span className="min-w-0 flex-1 text-muted">MetaRush makes {p.name}'s likeness only with their consent. None is recorded yet.</span>
          <Button size="sm" variant="primary" icon={<ShieldCheck size={13} />} onClick={() => void record()} data-record-consent>
            Record consent
          </Button>
        </div>
      )}
      <div className="mt-3 space-y-3">
        {(looks.data?.looks ?? []).map((l) => (
          <LookCard key={l.id} look={l} />
        ))}
      </div>
      {active ? (
        <div className="mt-3">
          <Button size="sm" variant="secondary" icon={<Plus size={13} />} onClick={() => void makeLook()} disabled={faces.length === 0} data-make-look>
            Make a look of {p.name}
          </Button>
          {faces.length === 0 ? <p className="mt-1 text-[12px] text-subtle">Confirm a few of {p.name}'s faces first (above): a look is made from them.</p> : null}
        </div>
      ) : null}
    </section>
  );
}

/** Recording consent: your own statement, or their recording. */
function ConsentForm({ person: p, onClose }: { person: PersonSummary; onClose: (summary?: string) => void }) {
  const uid = useId();
  const [who, setWho] = useState<'self' | 'other'>('self');
  const [statement, setStatement] = useState('');
  const [recording, setRecording] = useState('');
  const [expires, setExpires] = useState('');
  const [adult, setAdult] = useState(false);
  const [publicFigure, setPublicFigure] = useState(false);
  // A presenter (D274): always a recording, the person's own too, naming the services that may keep it.
  const [presenter, setPresenter] = useState(false);
  const [services, setServices] = useState<string[]>([]);
  // Lip sync and new words (D279, D309): each named with the service it may go to.
  const [newWords, setNewWords] = useState(false);
  const [wordsService, setWordsService] = useState('elevenlabs');
  const [lipSync, setLipSync] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const recordings = p.appearances ?? [];
  const presenterServices = useOp('presenter.stock', {}, { enabled: presenter, staleTime: 60_000 });
  const needsRecording = who === 'other' || presenter;
  // A refusal is about what was chosen: it goes once the choice changes.
  useEffect(() => setError(null), [who, adult, publicFigure, recording, presenter, services, newWords, lipSync, wordsService]);
  const example =
    who === 'self'
      ? `I, ${p.name}, agree to MetaRush making videos with my likeness for my own projects.`
      : `I, ${p.name}, agree to my likeness being used in generated video for this project.`;
  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      const out = await call('consent.record', {
        person: p.id,
        self: who === 'self',
        statement: statement.trim(),
        adult,
        ...(publicFigure ? { publicFigure } : {}),
        ...(needsRecording && recording ? { recording } : {}),
        ...(expires ? { expiresOn: expires, timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone } : {}),
        // Named uses replace the defaults (shots and voice), so those stay listed (D274).
        ...(presenter || lipSync || newWords
          ? {
              uses: ['shots', 'voice', ...(presenter ? ['presenters'] : []), ...(lipSync ? ['lip-sync'] : []), ...(newWords ? ['new-words'] : [])],
              providers: [...new Set([...(presenter ? services : []), ...(lipSync ? ['sync'] : []), ...(newWords ? [wordsService] : [])])],
            }
          : {}),
      });
      onClose(out.summary);
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };
  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
      data-consent-form
    >
      <Segmented
        label="Whose likeness"
        value={who}
        onChange={setWho}
        items={[
          { value: 'self', label: `This is me (${p.name})` },
          { value: 'other', label: 'Someone else' },
        ]}
      />
      <div>
        <label htmlFor={`${uid}-st`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
          {who === 'self' ? 'Your consent, in your own words' : `${p.name}'s consent, in their words`}
        </label>
        <textarea
          id={`${uid}-st`}
          rows={3}
          value={statement}
          onChange={(e) => setStatement(e.target.value)}
          placeholder={example}
          maxLength={2000}
          className="w-full resize-y rounded-[10px] bg-panel px-3 py-2 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
        />
      </div>
      <label className="flex items-start gap-2 text-[13px]">
        <input type="checkbox" checked={presenter} onChange={(e) => setPresenter(e.target.checked)} className="mt-[3px]" data-consent-presenter />
        <span>
          It covers a presenter of {p.name} too
          <span className="block text-[12px] text-muted">
            A presenter reads lines on camera and is kept at an AI service: it needs a recording of {p.name} agreeing on camera, naming that service — their own likeness
            too.
          </span>
        </span>
      </label>
      {presenter ? (
        <fieldset className="space-y-1.5" data-consent-services>
          <legend className="mb-1 text-[12.5px] font-medium text-muted">Services the recording names, which may keep it and the footage</legend>
          {(presenterServices.data?.services ?? []).map((s) => (
            <label key={s.provider} className="flex items-center gap-2 text-[13px]">
              <input
                type="checkbox"
                checked={services.includes(s.provider)}
                onChange={(e) => setServices((list) => (e.target.checked ? [...list, s.provider] : list.filter((x) => x !== s.provider)))}
              />
              {s.providerTitle}
            </label>
          ))}
        </fieldset>
      ) : null}
      {needsRecording ? (
        <div>
          <label htmlFor={`${uid}-rec`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
            A recording of {p.name} saying they agree
          </label>
          <select id={`${uid}-rec`} value={recording} onChange={(e) => setRecording(e.target.value)} className="h-9 w-full rounded-[10px] bg-panel px-2.5 text-[13px] text-fg hairline">
            <option value="">Choose a clip where {p.name} says so</option>
            {recordings.map((r) => (
              <option key={r.assetId + r.label} value={r.assetId}>
                {r.assetTitle}
              </option>
            ))}
          </select>
          <p className="mt-1 text-[12px] text-subtle">Record a short clip of them saying it and add it to MetaRush first if you don't have one.</p>
        </div>
      ) : null}
      <div>
        <label htmlFor={`${uid}-exp`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
          Last day it covers (optional{who === 'other' ? '; a year if you leave it empty' : ''})
        </label>
        <input id={`${uid}-exp`} type="date" value={expires} onChange={(e) => setExpires(e.target.value)} className="h-9 rounded-[10px] bg-panel px-2.5 text-[13px] text-fg hairline" />
      </div>
      <fieldset className="space-y-2" data-consent-uses>
        <legend className="mb-1 text-[12.5px] font-medium text-muted">It also covers (only if the statement or recording says so)</legend>
        <label className="flex items-start gap-2 text-[13px]">
          <input type="checkbox" checked={newWords} onChange={(e) => setNewWords(e.target.checked)} className="mt-[3px]" data-consent-new-words />
          <span>
            A word or short phrase {p.name} said may be changed in their own voice copy, kept at{' '}
            <select value={wordsService} onChange={(e) => setWordsService(e.target.value)} className="h-7 rounded-[8px] bg-panel px-1.5 text-[12.5px] text-fg hairline" aria-label="The service that keeps the voice copy">
              <option value="elevenlabs">ElevenLabs</option>
              <option value="gemini">Google Gemini</option>
            </select>
            . Each change is checked and marked as made with AI.
          </span>
        </label>
        <label className="flex items-start gap-2 text-[13px]">
          <input type="checkbox" checked={lipSync} onChange={(e) => setLipSync(e.target.checked)} className="mt-[3px]" data-consent-lip-sync />
          <span>{p.name}'s lips may be matched to new sound (with sync.).</span>
        </label>
      </fieldset>
      <label className="flex items-start gap-2 text-[13px]">
        <input type="checkbox" checked={adult} onChange={(e) => setAdult(e.target.checked)} className="mt-[3px]" data-adult />
        <span>{p.name} is 18 or older. MetaRush never makes a child's likeness.</span>
      </label>
      <label className="flex items-start gap-2 text-[13px]">
        <input type="checkbox" checked={publicFigure} onChange={(e) => setPublicFigure(e.target.checked)} className="mt-[3px]" />
        <span>{p.name} is a public figure (a politician, a celebrity). MetaRush doesn't make their likeness.</span>
      </label>
      <p className="rounded-[10px] bg-accent-soft px-3 py-2 text-[12.5px] leading-relaxed text-accent-text">
        It is kept with your name, this computer and the time, and can be withdrawn at any time. Google's own filters may still decline some likenesses.
      </p>
      {error ? <ErrorPanel compact error={error} /> : null}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={() => onClose()} disabled={busy}>
          Cancel
        </Button>
        <Button
          type="submit"
          variant="primary"
          loading={busy}
          disabled={statement.trim().length < 20 || !adult || (presenter && (!recording || services.length === 0))}
        >
          Record consent
        </Button>
      </div>
    </form>
  );
}

type Face = { id: string; assetId: string; at: number; assetTitle?: string; from: number; to: number };

/** Making a look: a person's (from their confirmed faces) or a character's (from pictures). */
function CreateLookForm({ person: p, faces = [], onClose }: { person?: PersonSummary; faces?: Face[]; onClose: (summary?: string) => void }) {
  const uid = useId();
  const [title, setTitle] = useState(p?.name ?? '');
  const [description, setDescription] = useState('');
  const [style, setStyle] = useState<'realistic' | 'stylized'>('realistic');
  const [styleNote, setStyleNote] = useState('');
  const [stills, setStills] = useState<string[]>(() => faces.slice(0, 2).map((f) => f.id));
  const [clips, setClips] = useState<string[]>([]);
  const [pictures, setPictures] = useState<string[]>([]);
  const [noReal, setNoReal] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const resources = useOp('resources.list', p ? null : {});
  const pics = useMemo(() => (resources.data?.resources ?? []).filter((r) => r.kind === 'image'), [resources.data]);
  const byId = new Map(faces.map((f) => [f.id, f]));
  const toggle = (list: string[], set: (v: string[]) => void, id: string, max: number) => set(list.includes(id) ? list.filter((x) => x !== id) : list.length < max ? [...list, id] : list);
  const save = async () => {
    setBusy(true);
    setError(null);
    const input: LookCreateIn = {
      title: title.trim(),
      description: description.trim(),
      style,
      ...(style === 'stylized' ? { styleNote: styleNote.trim() } : {}),
      ...(p ? { person: p.id } : {}),
      stills: p ? stills.map((id) => ({ assetId: byId.get(id)!.assetId, at: byId.get(id)!.at })) : pictures.map((name) => ({ resource: name })),
      clips: clips.map((id) => {
        const f = byId.get(id)!;
        const from = Math.max(f.from, f.at - 1.5);
        return { assetId: f.assetId, from, to: Math.min(f.to, from + 3) };
      }),
      ...(!p && noReal ? { noRealPeople: true } : {}),
    };
    try {
      const out = await call('looks.create', input);
      onClose(out.summary);
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };
  const nStills = p ? stills.length : pictures.length;
  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
      data-look-form
    >
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <label htmlFor={`${uid}-t`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
            Name of the look
          </label>
          <input id={`${uid}-t`} value={title} onChange={(e) => setTitle(e.target.value)} maxLength={80} className="h-9 w-full rounded-[10px] bg-panel px-3 text-[13.5px] text-fg hairline" />
        </div>
        <div>
          <div className="mb-1.5 text-[12.5px] font-medium text-muted">Style</div>
          <Segmented
            label="Style"
            value={style}
            onChange={setStyle}
            items={[
              { value: 'realistic', label: 'Realistic' },
              { value: 'stylized', label: 'Stylized' },
            ]}
          />
        </div>
      </div>
      {style === 'stylized' ? (
        <input
          aria-label="The style"
          value={styleNote}
          onChange={(e) => setStyleNote(e.target.value)}
          placeholder="a watercolor illustration"
          className="h-9 w-full rounded-[10px] bg-panel px-3 text-[13.5px] text-fg hairline placeholder:text-subtle"
        />
      ) : null}
      <div>
        <label htmlFor={`${uid}-d`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
          Who they are and how they look (sent with every shot)
        </label>
        <textarea
          id={`${uid}-d`}
          rows={3}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          maxLength={1500}
          placeholder={p ? `${p.name}, in their thirties, short dark hair, round glasses, usually in a green jacket` : 'A cartoon sea captain with a blue coat and a white beard'}
          className="w-full resize-y rounded-[10px] bg-panel px-3 py-2 text-[13.5px] text-fg hairline placeholder:text-subtle"
        />
      </div>
      {p ? (
        <>
          <div>
            <div className="mb-1.5 text-[12.5px] font-medium text-muted">Stills: pick 2 or 3 of {p.name}'s faces</div>
            <div className="flex flex-wrap gap-2" role="group" aria-label="Stills">
              {faces.map((f) => (
                <button
                  key={f.id}
                  type="button"
                  aria-pressed={stills.includes(f.id)}
                  onClick={() => toggle(stills, setStills, f.id, 3)}
                  className={cx('w-[104px] rounded-[10px] p-1 text-left', stills.includes(f.id) ? 'bg-accent-soft ring-1 ring-accent-line' : 'hairline hover:bg-hover')}
                >
                  <Thumb src={media.frame(f.assetId, f.at, 240)} alt="" className="aspect-video w-full rounded-[7px]" />
                  <span className="mt-1 block truncate text-[11px] text-muted">{f.assetTitle}</span>
                </button>
              ))}
            </div>
          </div>
          <div>
            <div className="mb-1.5 text-[12.5px] font-medium text-muted">Short clips (optional, up to 3): 3 seconds around a face; likenesses work best with them</div>
            <div className="flex flex-wrap gap-2" role="group" aria-label="Clips">
              {faces.map((f) => (
                <button
                  key={f.id}
                  type="button"
                  aria-pressed={clips.includes(f.id)}
                  onClick={() => toggle(clips, setClips, f.id, 3)}
                  className={cx('rounded-[8px] px-2 py-1 text-[11.5px]', clips.includes(f.id) ? 'bg-accent-soft text-accent-text ring-1 ring-accent-line' : 'text-muted hairline hover:bg-hover')}
                >
                  {f.assetTitle ?? 'clip'} at {Math.floor(f.at / 60)}:{String(Math.floor(f.at % 60)).padStart(2, '0')}
                </button>
              ))}
            </div>
          </div>
        </>
      ) : (
        <>
          <div>
            <div className="mb-1.5 text-[12.5px] font-medium text-muted">Stills: pick 2 or 3 pictures of the character</div>
            {pics.length === 0 ? (
              <p className="text-[12.5px] text-subtle">Add pictures of the character to the resources library first (drop them on any page).</p>
            ) : (
              <div className="flex flex-wrap gap-2" role="group" aria-label="Pictures">
                {pics.map((r) => (
                  <button
                    key={r.id}
                    type="button"
                    aria-pressed={pictures.includes(r.name)}
                    onClick={() => toggle(pictures, setPictures, r.name, 3)}
                    className={cx('w-[104px] rounded-[10px] p-1 text-left', pictures.includes(r.name) ? 'bg-accent-soft ring-1 ring-accent-line' : 'hairline hover:bg-hover')}
                  >
                    <ResourcePicture r={r} className="aspect-video w-full rounded-[7px]" />
                    <span className="mt-1 block truncate text-[11px] text-muted">{r.title}</span>
                  </button>
                ))}
              </div>
            )}
          </div>
          <label className="flex items-start gap-2 text-[13px]">
            <input type="checkbox" checked={noReal} onChange={(e) => setNoReal(e.target.checked)} className="mt-[3px]" />
            <span>These pictures show no real person.</span>
          </label>
        </>
      )}
      <p className="text-[12px] text-subtle">The look starts as not approved: make a few test shots, then approve it. Only people make, approve or withdraw a look.</p>
      {error ? <ErrorPanel compact error={error} /> : null}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={() => onClose()} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" loading={busy} disabled={!title.trim() || description.trim().length < 10 || nStills < 1 || (!p && !noReal)}>
          Make the look
        </Button>
      </div>
    </form>
  );
}
