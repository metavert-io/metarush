import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowRight, CheckCircle, Info, Key, Sparkle, StopCircle, WarningCircle } from '@phosphor-icons/react';
import { call } from '../../api/client';
import { useOp } from '../../api/queries';
import type { AIEstimate, AIMakeIn, AIVoiceCopyIn, AIWorkView, GenOptions, ProjectShowOut } from '../../api/types';
import { useToast } from '../../ui/Toast';
import { useEstimatesFirst } from '../../lib/preferences';
import { estimateWords } from '../../lib/costs';
import { Badge, Button, cx, Details, ErrorPanel, Skeleton, Spinner, type Tone } from '../../ui/primitives';
import { CostEstimate } from '../CostEstimate';
import { money } from './ai';

/*
 * AI services in the Generate panel (D224–D227): a picture, speech, a sound
 * effect, music, a recording in another voice, a dub, a cloud transcript or a
 * copy of someone's voice. The model and why come with the price, up to two
 * alternatives (cheaper, better) one click away, and the model's licence;
 * nothing is sent until the button with the price is pressed (or, with the
 * person's estimates off, D208, within the budget). Results land where they
 * belong — pictures in the resources library, sound and dubs in Sources, a
 * transcript as the clip's words — marked as AI-generated.
 */

export type AIKind =
  | 'picture'
  | 'voice'
  | 'sound-effect'
  | 'music'
  | 'voice-change'
  | 'dubbing'
  | 'transcription'
  | 'voice-copy'
  | 'background-removal'
  | 'upscale'
  | 'lip-sync'
  | 'presenter';

export const AI_KINDS: { value: AIKind; label: string; what: string }[] = [
  {
    value: 'picture',
    label: 'Picture',
    what: 'Make a picture, or change pictures from the resources library',
  },
  { value: 'voice', label: 'Speech', what: 'Say words in an AI voice' },
  {
    value: 'sound-effect',
    label: 'Sound effect',
    what: 'Make a sound effect from a few words',
  },
  {
    value: 'music',
    label: 'Music',
    what: 'Make a piece of music from a few words',
  },
  {
    value: 'voice-change',
    label: 'Voice change',
    what: 'Say a recording again in another voice',
  },
  {
    value: 'dubbing',
    label: 'Dub',
    what: 'Say a clip’s speech again in another language',
  },
  {
    value: 'transcription',
    label: 'Transcript',
    what: 'Write down a clip’s words with a service, or on this computer for free',
  },
  {
    value: 'background-removal',
    label: 'Cut-out',
    what: 'Take a picture’s background away (on this computer, free, on a Mac)',
  },
  {
    value: 'upscale',
    label: 'Larger picture',
    what: 'Make a picture 2 or 4 times larger',
  },
  {
    value: 'lip-sync',
    label: 'Lip sync',
    what: 'Match the lips in a clip to new sound, with everyone’s consent',
  },
  {
    value: 'voice-copy',
    label: 'Voice copy',
    what: 'Copy a person’s voice, with their consent',
  },
  {
    value: 'presenter',
    label: 'Presenter',
    what: 'A presenter reads your lines on camera',
  },
];

const LANGUAGES: { code: string; name: string }[] = [
  { code: 'es', name: 'Spanish' },
  { code: 'fr', name: 'French' },
  { code: 'de', name: 'German' },
  { code: 'it', name: 'Italian' },
  { code: 'pt', name: 'Portuguese' },
  { code: 'nl', name: 'Dutch' },
  { code: 'pl', name: 'Polish' },
  { code: 'ja', name: 'Japanese' },
  { code: 'ko', name: 'Korean' },
  { code: 'zh', name: 'Chinese' },
  { code: 'hi', name: 'Hindi' },
  { code: 'ar', name: 'Arabic' },
  { code: 'en', name: 'English' },
];

const stateWords: Record<AIWorkView['state'], { label: string; tone: Tone }> = {
  waiting: { label: 'Waiting to start', tone: 'neutral' },
  preparing: { label: 'Getting it ready', tone: 'accent' },
  sending: { label: 'Sending', tone: 'accent' },
  making: { label: 'Being made', tone: 'accent' },
  done: { label: 'Ready', tone: 'ok' },
  failed: { label: "Couldn't be made", tone: 'danger' },
  refused: { label: 'Declined', tone: 'danger' },
  canceled: { label: 'Stopped', tone: 'neutral' },
};

const inputCls = 'h-9 w-full rounded-[10px] bg-panel px-2.5 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent';
const labelCls = 'mb-1.5 block text-[12.5px] font-medium text-muted';

function num(v: string): number | undefined {
  const n = Number(v);
  return v.trim() !== '' && Number.isFinite(n) ? n : undefined;
}

/** The form for one kind of AI work, with its price, and the work made so far. */
export function AIWork({ kind, data, onOpenClip }: { kind: AIKind; data: ProjectShowOut; onOpenClip: (assetId: string) => void }) {
  const uid = useId();
  const toast = useToast();
  const qc = useQueryClient();
  const projectId = data.project.id;
  const estimatesFirst = useEstimatesFirst();
  const models = useOp('ai.models', { kind, projectId });
  const people = useOp('people.list', {}, { enabled: kind === 'voice-copy', staleTime: 60_000 });
  const [model, setModel] = useState('');
  const [prompt, setPrompt] = useState('');
  const [opts, setOpts] = useState<GenOptions>({});
  const [clip, setClip] = useState('');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [noPeople, setNoPeople] = useState(false);
  const [pictures, setPictures] = useState('');
  // Lip sync: the clip whose sound the lips are matched to.
  const [sound, setSound] = useState('');
  // Voice copies.
  const [person, setPerson] = useState('');
  const [voiceName, setVoiceName] = useState('');
  const [consentClip, setConsentClip] = useState('');
  const [est, setEst] = useState<AIEstimate | null>(null);
  const [estError, setEstError] = useState<unknown>(null);
  const [pricing, setPricing] = useState(false);
  const [sending, setSending] = useState(false);

  // A new kind starts afresh.
  useEffect(() => {
    setModel('');
    setOpts({});
    setEst(null);
    setEstError(null);
  }, [kind]);

  const kindModels = models.data?.kinds?.[0]?.models ?? [];
  const usable = kindModels.filter((m) => m.built && m.allowed && !m.retired);
  const chosenModel = kindModels.find((m) => `${m.provider}/${m.id}` === model);
  const voices = (chosenModel ?? kindModels.find((m) => m.default))?.limits?.voices ?? [];
  const sounds = data.assets.filter((a) => a.hasAudio && a.state === 'available');
  const set = (patch: Partial<GenOptions>) => setOpts((o) => ({ ...o, ...patch }));

  const needs = {
    words: kind === 'picture' || kind === 'voice' || kind === 'sound-effect' || kind === 'music',
    clip: kind === 'voice-change' || kind === 'dubbing' || kind === 'transcription' || kind === 'voice-copy' || kind === 'lip-sync',
    picture: kind === 'background-removal' || kind === 'upscale',
  };
  const ready0 =
    (needs.words ? prompt.trim() !== '' : true) &&
    (needs.clip ? clip !== '' : true) &&
    (needs.picture ? pictures.trim() !== '' : true) &&
    (kind !== 'lip-sync' || sound !== '') &&
    (kind !== 'dubbing' || !!opts.targetLanguage) &&
    (kind !== 'voice-copy' || (person !== '' && voiceName.trim() !== ''));

  const makeIn = useMemo<AIMakeIn>(
    () => ({
      kind: kind as AIMakeIn['kind'],
      projectId,
      ...(model ? { model } : {}),
      ...(prompt.trim() ? { prompt: prompt.trim() } : {}),
      ...(pictures.trim()
        ? {
            pictures: pictures
              .split(',')
              .map((p) => p.trim())
              .filter(Boolean),
          }
        : {}),
      ...(clip ? { clip } : {}),
      ...(kind === 'lip-sync' && sound ? { sound } : {}),
      ...(kind !== 'transcription' && num(from) !== undefined ? { from: num(from) } : {}),
      ...(kind !== 'transcription' && num(to) !== undefined ? { to: num(to) } : {}),
      options: opts,
      ...(noPeople ? { noPeople: true } : {}),
    }),
    [kind, projectId, model, prompt, pictures, clip, sound, from, to, opts, noPeople],
  );
  const copyIn = useMemo<AIVoiceCopyIn>(
    () => ({
      projectId,
      ...(model ? { model } : {}),
      person,
      name: voiceName.trim(),
      sample: clip,
      ...(num(from) !== undefined ? { from: num(from) } : {}),
      ...(num(to) !== undefined ? { to: num(to) } : {}),
      ...(consentClip ? { consentClip } : {}),
    }),
    [projectId, model, person, voiceName, clip, from, to, consentClip],
  );

  // The price is for exactly what would be sent: any change prices it again.
  const inputKey = useMemo(() => JSON.stringify(kind === 'voice-copy' ? copyIn : makeIn), [kind, copyIn, makeIn]);
  const [estKey, setEstKey] = useState('');
  const latest = useRef('');
  useEffect(() => {
    latest.current = inputKey;
    if (!ready0) {
      setEst(null);
      setEstKey('');
      setEstError(null);
      setPricing(false);
      return;
    }
    const t = setTimeout(async () => {
      setPricing(true);
      try {
        const e = kind === 'voice-copy' ? (await call('ai.voices.copy', { ...copyIn, dryRun: true })).estimate : await call('ai.estimate', makeIn);
        if (latest.current !== inputKey) return;
        setEst(e);
        setEstKey(inputKey);
        setEstError(null);
      } catch (e) {
        if (latest.current !== inputKey) return;
        setEst(null);
        setEstError(e);
      } finally {
        if (latest.current === inputKey) setPricing(false);
      }
    }, 450);
    return () => clearTimeout(t);
  }, [inputKey, ready0, kind, copyIn, makeIn]);
  const priced = !!est && estKey === inputKey;
  const ready = estimatesFirst ? priced && !!est?.allowed && !pricing : ready0 && !(priced && est && !est.allowed);

  const go = async () => {
    if (!ready) return;
    setSending(true);
    try {
      const maxUsd = priced && est ? { maxUsd: est.usd } : {};
      const out = kind === 'voice-copy' ? await call('ai.voices.copy', { ...copyIn, ...maxUsd }) : await call('ai.make', { ...makeIn, ...maxUsd });
      ['ai.list', 'ai.voices', 'activity.summary', 'project.show', 'resources.list'].forEach((q) => void qc.invalidateQueries({ queryKey: [q] }));
      toast({ tone: 'success', title: 'Asked for it', body: out.summary });
      setPrompt('');
    } catch (e) {
      setEstError(e);
    } finally {
      setSending(false);
    }
  };

  const keyMissing = est && est.keyNeeded && !est.keyReady;
  const clipSelect = (id: string, value: string, onChange: (v: string) => void, label: string) => (
    <div>
      <label htmlFor={id} className={labelCls}>
        {label}
      </label>
      <select id={id} value={value} onChange={(e) => onChange(e.target.value)} className={inputCls} data-ai-clip>
        <option value="">Choose a clip with sound</option>
        {sounds.map((a) => (
          <option key={a.id} value={a.id}>
            {a.title}
          </option>
        ))}
      </select>
    </div>
  );

  return (
    <div data-ai-work={kind}>
      <div className="grid grid-cols-1 gap-5 px-5 py-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(300px,1fr)]">
        <div className="space-y-4">
          {needs.words ? (
            <div>
              <label htmlFor={`${uid}-p`} className={labelCls}>
                {kind === 'voice' ? 'The words to say' : kind === 'picture' ? 'What the picture shows, or how to change the pictures' : 'What it sounds like'}
              </label>
              <textarea
                id={`${uid}-p`}
                rows={3}
                maxLength={kind === 'voice' ? 10000 : 4000}
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                placeholder={
                  kind === 'voice'
                    ? 'Welcome to the garden. Today we plant tomatoes.'
                    : kind === 'picture'
                      ? 'A watercolour kite over a green hill'
                      : kind === 'music'
                        ? 'A calm acoustic guitar piece for a cooking video'
                        : 'A wooden door creaks open slowly'
                }
                className="w-full resize-y rounded-[10px] bg-panel px-3 py-2 text-[14px] leading-relaxed text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent"
                data-ai-prompt
              />
            </div>
          ) : null}

          {kind === 'voice-copy' ? (
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div>
                <label htmlFor={`${uid}-person`} className={labelCls}>
                  Whose voice
                </label>
                <select id={`${uid}-person`} value={person} onChange={(e) => setPerson(e.target.value)} className={inputCls} data-ai-person>
                  <option value="">Choose a person</option>
                  {(people.data?.people ?? []).map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
                <p className="mt-1 text-[12px] text-subtle">
                  Their consent must be recorded first, on the{' '}
                  <Link to="/library/people" className="font-medium text-accent-text hover:underline">
                    People page
                  </Link>
                  .
                </p>
              </div>
              <div>
                <label htmlFor={`${uid}-vn`} className={labelCls}>
                  The new voice’s name
                </label>
                <input id={`${uid}-vn`} value={voiceName} onChange={(e) => setVoiceName(e.target.value)} maxLength={80} className={inputCls} data-ai-voice-name />
              </div>
            </div>
          ) : null}

          {needs.clip ? (
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-[minmax(0,1fr)_90px_90px]">
              {clipSelect(
                `${uid}-c`,
                clip,
                setClip,
                kind === 'voice-copy'
                  ? 'A clip of the voice, speaking alone'
                  : kind === 'transcription'
                    ? 'The clip to write down'
                    : kind === 'lip-sync'
                      ? 'The footage whose lips to match'
                      : 'The clip',
              )}
              {kind !== 'transcription' ? (
                <>
                  <div>
                    <label htmlFor={`${uid}-f`} className={labelCls}>
                      From (s)
                    </label>
                    <input id={`${uid}-f`} inputMode="decimal" value={from} onChange={(e) => setFrom(e.target.value)} placeholder="start" className={inputCls} />
                  </div>
                  <div>
                    <label htmlFor={`${uid}-t`} className={labelCls}>
                      To (s)
                    </label>
                    <input id={`${uid}-t`} inputMode="decimal" value={to} onChange={(e) => setTo(e.target.value)} placeholder="end" className={inputCls} />
                  </div>
                </>
              ) : null}
            </div>
          ) : null}

          {kind === 'voice-copy' ? clipSelect(`${uid}-cc`, consentClip, setConsentClip, 'The same person reading the service’s consent script (when it asks for one)') : null}
          {kind === 'lip-sync' ? clipSelect(`${uid}-snd`, sound, setSound, 'The new sound (no longer than the footage)') : null}
          {kind === 'lip-sync' ? (
            <p className="text-[12px] text-muted">
              Everyone seen or heard in that part, and the voice in the new sound, needs to be named and to have consented to lip sync with this service. Before sending,
              MetaRush looks at every quarter second of the part on this computer and stops if it shows a face nobody named there.
            </p>
          ) : null}

          {needs.picture ? (
            <div>
              <label htmlFor={`${uid}-pic`} className={labelCls}>
                The picture (its short name in the resources library)
              </label>
              <input id={`${uid}-pic`} value={pictures} onChange={(e) => setPictures(e.target.value)} placeholder="garden-photo" className={inputCls} data-ai-picture />
            </div>
          ) : null}

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div>
              <label htmlFor={`${uid}-m`} className={labelCls}>
                Model
              </label>
              <select id={`${uid}-m`} value={model} onChange={(e) => setModel(e.target.value)} className={inputCls} data-ai-model>
                <option value="">
                  The usual one
                  {kindModels.find((m) => m.pinned) ? ' (chosen in Content Settings)' : ''}
                </option>
                {usable.map((m) => (
                  <option key={`${m.provider}/${m.id}`} value={`${m.provider}/${m.id}`}>
                    {m.title} ({m.providerTitle}){m.rate ? `, ${m.rate.replace(/ \(.*\)$/, '')}` : ''}
                  </option>
                ))}
              </select>
            </div>

            {kind === 'picture' ? (
              <>
                <div>
                  <label htmlFor={`${uid}-size`} className={labelCls}>
                    Size and shape
                  </label>
                  <div className="flex gap-2">
                    <select id={`${uid}-size`} value={opts.size ?? ''} onChange={(e) => set({ size: e.target.value || undefined })} className={inputCls}>
                      <option value="">1K</option>
                      <option value="2K">2K</option>
                      <option value="4K">4K</option>
                    </select>
                    <select aria-label="Shape" value={opts.aspectRatio ?? ''} onChange={(e) => set({ aspectRatio: e.target.value || undefined })} className={inputCls}>
                      <option value="">Square or as the pictures</option>
                      <option value="16:9">Wide 16:9</option>
                      <option value="9:16">Tall 9:16</option>
                      <option value="4:3">4:3</option>
                      <option value="3:4">3:4</option>
                      <option value="1:1">Square 1:1</option>
                    </select>
                  </div>
                </div>
                <div className="sm:col-span-2">
                  <label htmlFor={`${uid}-pics`} className={labelCls}>
                    Pictures to change (their short names in the resources library, with commas)
                  </label>
                  <input id={`${uid}-pics`} value={pictures} onChange={(e) => setPictures(e.target.value)} placeholder="kite, garden-photo" className={inputCls} />
                </div>
              </>
            ) : null}

            {kind === 'upscale' ? (
              <div>
                <label htmlFor={`${uid}-scale`} className={labelCls}>
                  How much larger
                </label>
                <select id={`${uid}-scale`} value={opts.scale ?? ''} onChange={(e) => set({ scale: Number(e.target.value) || undefined })} className={inputCls} data-ai-scale>
                  <option value="">Twice as wide and tall</option>
                  <option value="4">Four times as wide and tall</option>
                </select>
              </div>
            ) : null}

            {kind === 'voice' || kind === 'voice-change' ? (
              <div>
                <label htmlFor={`${uid}-v`} className={labelCls}>
                  Voice
                </label>
                <input
                  id={`${uid}-v`}
                  list={`${uid}-vl`}
                  value={opts.voice ?? ''}
                  onChange={(e) => set({ voice: e.target.value || undefined })}
                  placeholder={voices[0] ?? 'A voice’s name or ID'}
                  className={inputCls}
                  data-ai-voice
                />
                <datalist id={`${uid}-vl`}>
                  {voices.map((v) => (
                    <option key={v} value={v} />
                  ))}
                </datalist>
              </div>
            ) : null}
            {kind === 'voice' ? (
              <div>
                <label htmlFor={`${uid}-st`} className={labelCls}>
                  How to say it (for voices that take it)
                </label>
                <input
                  id={`${uid}-st`}
                  value={opts.style ?? ''}
                  onChange={(e) => set({ style: e.target.value || undefined })}
                  placeholder="warm and unhurried"
                  className={inputCls}
                />
              </div>
            ) : null}

            {kind === 'sound-effect' || kind === 'music' ? (
              <div>
                <label htmlFor={`${uid}-secs`} className={labelCls}>
                  Length: {opts.seconds ? `${opts.seconds} seconds` : 'the service chooses'}
                </label>
                <input
                  id={`${uid}-secs`}
                  type="range"
                  min={0}
                  max={kind === 'music' ? 300 : 30}
                  step={kind === 'music' ? 5 : 0.5}
                  value={opts.seconds ?? 0}
                  onChange={(e) => set({ seconds: Number(e.target.value) || undefined })}
                  className="w-full accent-[var(--accent)]"
                  data-ai-seconds
                />
                <label className="mt-2 flex items-center gap-2 text-[12.5px] text-fg">
                  {kind === 'music' ? (
                    <input type="checkbox" checked={!!opts.instrumental} onChange={(e) => set({ instrumental: e.target.checked || undefined })} />
                  ) : (
                    <input type="checkbox" checked={!!opts.loop} onChange={(e) => set({ loop: e.target.checked || undefined })} />
                  )}
                  {kind === 'music' ? 'No singing' : 'Loops seamlessly'}
                </label>
              </div>
            ) : null}

            {kind === 'dubbing' ? (
              <div>
                <label htmlFor={`${uid}-into`} className={labelCls}>
                  Dub it into
                </label>
                <select
                  id={`${uid}-into`}
                  value={opts.targetLanguage ?? ''}
                  onChange={(e) => set({ targetLanguage: e.target.value || undefined })}
                  className={inputCls}
                  data-ai-into
                >
                  <option value="">Choose a language</option>
                  {LANGUAGES.map((l) => (
                    <option key={l.code} value={l.code}>
                      {l.name}
                    </option>
                  ))}
                </select>
                <label className="mt-2 flex items-start gap-2 text-[12.5px] text-fg">
                  <input type="checkbox" checked={!!opts.ownVoices} onChange={(e) => set({ ownVoices: e.target.checked || undefined })} className="mt-[3px]" />
                  <span>
                    In the speakers’ own voices
                    <span className="block text-[12px] text-muted">That copies their voices: everyone who speaks needs to be named and to have consented.</span>
                  </span>
                </label>
              </div>
            ) : null}
          </div>

          {kind === 'picture' && pictures.trim() ? (
            <label className="flex items-start gap-2 text-[13px] text-fg">
              <input type="checkbox" checked={noPeople} onChange={(e) => setNoPeople(e.target.checked)} className="mt-[3px]" data-no-people />
              <span>
                These pictures show no real people
                <span className="block text-[12px] text-muted">A picture of someone needs their consent.</span>
              </span>
            </label>
          ) : null}
        </div>

        <div className="flex flex-col gap-3">
          {keyMissing ? (
            <div className="flex flex-wrap items-center gap-2 rounded-[12px] bg-accent-soft px-3.5 py-3 text-[13px] text-accent-text" role="status">
              <Key size={15} className="shrink-0" />
              <span className="min-w-0 flex-1">Add your {est.providerTitle} key on this computer first. Each computer keeps its own key.</span>
              <Link to="/settings/integrations" className="inline-flex items-center gap-1 font-medium hover:underline">
                Settings → Integrations <ArrowRight size={12} />
              </Link>
            </div>
          ) : null}
          <div className="min-h-[120px] rounded-[14px] bg-hover/60 px-4 py-3.5" aria-live="polite">
            {!ready0 ? (
              <p className="text-[13px] text-subtle">{AI_KINDS.find((k) => k.value === kind)?.what}. Fill it in to see what it costs.</p>
            ) : pricing && !est ? (
              <span className="inline-flex items-center gap-2 text-[13px] text-muted">
                <Spinner size={12} /> Pricing it…
              </span>
            ) : est ? (
              <AIEstimateLines estimate={est} onPick={(m) => setModel(m)} />
            ) : null}
            {estError ? <ErrorPanel compact error={estError} className="mt-2" /> : null}
          </div>
          <Button
            variant="primary"
            icon={<Sparkle size={14} weight="fill" />}
            disabled={!ready}
            loading={sending}
            onClick={() => void go()}
            data-ai-go
            data-estimates-first={estimatesFirst ? 'on' : 'off'}
          >
            {est && priced ? `Make it (${estimateWords(est.usd)})` : 'Make it'}
          </Button>
          <p className="text-[12px] leading-relaxed text-subtle">Nothing is sent until you press it. What comes back is marked as AI-generated everywhere.</p>
        </div>
      </div>
      <AIWorkList projectId={projectId} kind={kind} onOpenClip={onOpenClip} />
    </div>
  );
}

/** An AI estimate: the price that opens to its parts, the model and why, the alternatives, the licence, what may stop it. */
export function AIEstimateLines({ estimate: e, onPick }: { estimate: AIEstimate; onPick?: (ref: string) => void }) {
  const why = {
    default: 'MetaRush’s usual model for this',
    asked: 'the model you chose',
    'content-settings': 'chosen in the project’s Content Settings',
  }[e.choice];
  const budget = e.budget.project?.limitUsd !== undefined && e.budget.project?.limitUsd !== null ? e.budget.project : null;
  return (
    <div className="space-y-1.5 text-[13px] leading-relaxed" data-ai-estimate>
      <p className="font-medium text-fg" data-estimate-summary>
        {e.cost.about.charAt(0).toUpperCase() + e.cost.about.slice(1)}
        <span className="font-normal text-muted">
          {' '}
          for {e.what} with {e.providerTitle} ({e.modelTitle}), {why}
        </span>
      </p>
      <CostEstimate estimate={e.cost} bare />
      {e.inputs?.map((s) => (
        <p key={s} className="text-[12.5px] text-muted">
          Sends {s}
        </p>
      ))}
      {e.alternatives.length > 0 ? (
        <ul className="space-y-0.5" aria-label="Other models" data-ai-alternatives>
          {e.alternatives.map((a) => (
            <li key={a.why} className="text-[12.5px] text-muted">
              {a.why === 'cheaper' ? 'Cheaper' : 'Better'}:{' '}
              {onPick ? (
                <button type="button" className="font-medium text-accent-text hover:underline" onClick={() => onPick(`${a.provider}/${a.model}`)}>
                  {a.summary}
                </button>
              ) : (
                a.summary
              )}
            </li>
          ))}
        </ul>
      ) : null}
      {e.consent.ok && e.consent.message ? (
        <p className="flex items-start gap-1.5 text-[12.5px] text-ok">
          <CheckCircle size={14} weight="fill" className="mt-[2px] shrink-0" /> {e.consent.message}
        </p>
      ) : null}
      {e.problems?.map((p) => (
        <p key={p} className="flex items-start gap-1.5 text-[12.5px] text-danger" data-estimate-problem>
          <WarningCircle size={14} weight="fill" className="mt-[2px] shrink-0" /> {p}
        </p>
      ))}
      {e.notes?.map((n) => (
        <p key={n} className="flex items-start gap-1.5 text-[12.5px] text-muted">
          <Info size={14} className="mt-[2px] shrink-0" /> {n}
        </p>
      ))}
      {budget ? (
        <p className="text-[12px] text-subtle">
          This project: {money(budget.spentUsd + budget.heldUsd)} of {money(budget.limitUsd)} spent
        </p>
      ) : null}
      <Details compact>
        <dl className="grid grid-cols-[130px_minmax(0,1fr)] gap-y-0.5 text-[12px]">
          <dt>Model</dt>
          <dd className="text-muted">
            {e.provider}/{e.model} ({e.stage})
          </dd>
          <dt>Price</dt>
          <dd className="text-muted">{e.rate}</dd>
          <dt>Counted</dt>
          <dd className="text-muted">{e.units}</dd>
          <dt>Licence</dt>
          <dd className="text-muted">
            {e.licence.summary} (commercial use: {e.licence.commercial}; broadcast: {e.licence.broadcast}; read {e.licence.checked})
          </dd>
          <dt>What the service keeps</dt>
          <dd className="text-muted">{e.data}</dd>
        </dl>
      </Details>
    </div>
  );
}

/** AI work made for this project, newest first: where it landed, what it cost, Stop while it runs. */
export function AIWorkList({ projectId, kind, onOpenClip }: { projectId: string; kind: AIKind; onOpenClip: (assetId: string) => void }) {
  const [live, setLive] = useState(false);
  const list = useOp('ai.list', { projectId, limit: 12 }, { refetchInterval: live ? 2500 : false });
  const work = list.data?.work ?? [];
  useEffect(() => setLive(work.some((w) => w.state === 'waiting' || w.state === 'sending' || w.state === 'making' || w.state === 'preparing')), [work]);
  const toast = useToast();
  const qc = useQueryClient();
  if (list.isLoading) return <Skeleton className="mx-5 mb-5 h-20" />;
  if (work.length === 0) return null;
  const stop = async (id: string) => {
    try {
      const out = await call('ai.cancel', { id });
      void qc.invalidateQueries({ queryKey: ['ai.list'] });
      toast({ tone: 'success', title: 'Stopping it', body: out.summary });
    } catch (e) {
      toast({
        tone: 'error',
        title: 'Not stopped',
        body: e instanceof Error ? e.message : String(e),
      });
    }
  };
  return (
    <div className="border-t border-line px-5 py-4">
      <div className="mb-3 flex items-center gap-2">
        <h3 className="text-[13.5px] font-semibold">AI work for this project</h3>
        <span className="text-[12px] text-subtle">{list.data?.summary}</span>
      </div>
      <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3" aria-label="AI work" data-ai-work-list>
        {work.map((w) => {
          const st = stateWords[w.state];
          const busy = w.state === 'waiting' || w.state === 'sending' || w.state === 'making' || w.state === 'preparing';
          return (
            <li key={w.id} className={cx('flex flex-col gap-1 rounded-[12px] p-2.5 hairline', w.kind === kind && 'bg-hover/40')} data-ai-work-item={w.state}>
              <div className="flex flex-wrap items-center gap-1.5">
                <Badge tone={st.tone}>{st.label}</Badge>
                <span className="text-[11.5px] text-subtle">
                  {w.kindTitle}, {money(w.costUsd)}
                </span>
              </div>
              {w.prompt ? (
                <p className="line-clamp-2 text-[12.5px] leading-snug text-fg" title={w.prompt}>
                  {w.prompt}
                </p>
              ) : null}
              {w.outputs.map((o) =>
                o.kind === 'clip' ? (
                  <button key={o.id} type="button" className="text-left text-[12.5px] font-medium text-accent-text hover:underline" onClick={() => onOpenClip(o.id)}>
                    {o.title}
                  </button>
                ) : o.link ? (
                  <Link key={o.id} to={o.link} className="text-[12.5px] font-medium text-accent-text hover:underline">
                    {o.title}
                  </Link>
                ) : (
                  <span key={o.id} className="text-[12.5px] text-fg">
                    {o.title}
                  </span>
                ),
              )}
              {w.state === 'failed' || w.state === 'refused' ? <p className="text-[12px] text-danger">{w.outcome?.message ?? w.summary}</p> : null}
              {w.forgotten?.kept ? <p className="text-[11.5px] text-subtle">{w.providerTitle} keeps it: it offers no way to delete it.</p> : null}
              {busy ? (
                <div>
                  <Button size="sm" variant="ghost" icon={<StopCircle size={13} />} onClick={() => void stop(w.id)}>
                    Stop
                  </Button>
                </div>
              ) : null}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
