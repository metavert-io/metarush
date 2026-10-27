import { useCallback, useEffect, useId, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query';
import { Coins, FolderSimple, MagnifyingGlass, Play, SlidersHorizontal, SpeakerHigh, SpeakerSlash, Star, Stop } from '@phosphor-icons/react';
import { call, media, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { ProviderSettingsIn, ProviderStatusOut, ProjectSummary, VoiceModel, VoiceView } from '../../api/types';
import { loadPref, savePref } from '../../lib/theme';
import { useToast } from '../../ui/Toast';
import { Badge, Button, cx, ErrorPanel, IconButton, Progress, Skeleton, Spinner, Tabs } from '../../ui/primitives';
import { Link } from 'react-router';
import { Details } from './Details';

/*
 * ElevenLabs choices for the whole workspace (default voice, model, how the
 * voice sounds, favorites) and per project (its own voice, a spending limit).
 * Same ops as `metarush providers settings elevenlabs` and `metarush voiceover voices`.
 * Samples stream through the daemon: the browser never talks to ElevenLabs.
 */

const EL = 'elevenlabs' as const;
const PROJECT_PREF = 'integrations.elevenlabs.project';

/** ElevenLabs' own defaults, shown while a setting is left alone. */
const PROVIDER_DEFAULTS = { stability: 0.5, similarityBoost: 0.75, style: 0, speed: 1, speakerBoost: true } as const;

type Apply = (input: Omit<ProviderSettingsIn, 'name'>, done: string) => Promise<boolean>;

export function ElevenLabsSettings() {
  const toast = useToast();
  const qc = useQueryClient();
  const [projectId, setProjectIdState] = useState<string>(() => loadPref<string>(PROJECT_PREF, ''));
  const setProjectId = (id: string) => {
    setProjectIdState(id);
    savePref(PROJECT_PREF, id);
  };
  const projects = useOp('project.list', {});
  const projectList = projects.data?.projects ?? [];
  const project = projectList.find((p) => p.id === projectId);
  const st = useOp('provider.status', project ? { name: EL, projectId: project.id } : { name: EL });
  // The default's narration voice (Content Settings, D193): what new projects use.
  const def = useOp('provider.status', { name: EL });
  const models = useOp('voiceover.models', {});

  const apply = useCallback<Apply>(
    async (input, done) => {
      try {
        const out = await call('provider.settings', { name: EL, ...input });
        void qc.invalidateQueries({ queryKey: ['provider.status'] });
        void qc.invalidateQueries({ queryKey: ['voiceover.voices'] });
        toast({ title: done, tone: 'success', body: out.warnings?.length ? out.warnings.join(' ') : undefined });
        return true;
      } catch (e) {
        toast({ title: 'That change was not saved', body: toApiError(e).message, tone: 'error' });
        return false;
      }
    },
    [qc, toast],
  );

  return (
    <div className="space-y-6">
      <VoicePanel status={def.data} loading={def.isLoading} apply={apply} />
      <SoundPanel
        status={def.data}
        models={models.data?.models ?? []}
        defaultModel={models.data?.default}
        modelsError={models.error}
        loading={def.isLoading || models.isLoading}
        apply={apply}
      />
      <ProjectPanel
        projects={projectList}
        loadingProjects={projects.isLoading}
        project={project}
        setProjectId={setProjectId}
        status={st.data}
        loadingStatus={st.isLoading}
        apply={apply}
      />
    </div>
  );
}

function Panel({ icon, title, intro, children, id }: { icon: ReactNode; title: string; intro?: ReactNode; children: ReactNode; id: string }) {
  return (
    <section className="panel p-5" aria-labelledby={id}>
      <h2 id={id} className="flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle" aria-hidden>
          {icon}
        </span>
        {title}
      </h2>
      {intro ? <p className="mt-1 max-w-[70ch] text-[13px] leading-relaxed text-muted">{intro}</p> : null}
      <div className="mt-4">{children}</div>
    </section>
  );
}

/* ---------- Voice samples ---------- */

export interface Player {
  cur: { id: string; state: 'loading' | 'playing' } | null;
  toggle: (v: VoiceView) => void;
}

/** One sample at a time, streamed from the daemon. */
export function usePreview(): Player {
  const toast = useToast();
  const ref = useRef<HTMLAudioElement | null>(null);
  const [cur, setCur] = useState<Player['cur']>(null);
  const curRef = useRef(cur);
  curRef.current = cur;

  const stop = useCallback(() => {
    const a = ref.current;
    if (a) {
      a.onplaying = a.onended = a.onerror = null;
      a.pause();
      a.removeAttribute('src');
      a.load();
    }
    setCur(null);
  }, []);
  useEffect(() => stop, [stop]);

  const toggle = useCallback(
    (v: VoiceView) => {
      if (curRef.current?.id === v.id) {
        stop();
        return;
      }
      stop();
      const a = ref.current ?? (ref.current = new Audio());
      const url = media.voicePreview(v.provider || EL, v.id, !!v.library);
      const fail = (msg?: string) => {
        setCur((c) => (c?.id === v.id ? null : c));
        toast({ title: `Could not play the ${v.name} sample`, body: msg, tone: 'error' });
      };
      setCur({ id: v.id, state: 'loading' });
      a.onplaying = () => setCur({ id: v.id, state: 'playing' });
      a.onended = () => setCur((c) => (c?.id === v.id ? null : c));
      a.onerror = () => {
        // The daemon answers errors as JSON; read it for a plain message.
        void fetch(url, { credentials: 'same-origin' })
          .then((r) => r.json() as Promise<{ error?: { message?: string } }>)
          .then((j) => fail(j.error?.message))
          .catch(() => fail());
      };
      a.src = url;
      a.play().catch((e: unknown) => {
        if (e instanceof DOMException && e.name === 'NotAllowedError') fail('The browser blocked the sound. Press play again.');
      });
    },
    [stop, toast],
  );
  return { cur, toggle };
}

export function PlayButton({ v, player }: { v: VoiceView; player: Player }) {
  const state = player.cur?.id === v.id ? player.cur.state : null;
  if (!v.hasPreview) {
    return (
      <span className="grid h-8 w-8 place-items-center text-subtle" title="No sample for this voice">
        <SpeakerSlash size={15} aria-label="No sample for this voice" />
      </span>
    );
  }
  return (
    <button
      type="button"
      onClick={() => player.toggle(v)}
      aria-pressed={!!state}
      aria-label={state ? `Stop the sample of ${v.name}` : `Play a sample of ${v.name}`}
      title={state ? 'Stop' : 'Play a sample'}
      className={cx(
        'grid h-8 w-8 shrink-0 place-items-center rounded-full transition-colors duration-200 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent',
        state ? 'bg-accent text-accent-ink' : 'bg-panel text-fg hairline hover:bg-hover',
      )}
    >
      {state === 'loading' ? <Spinner size={13} /> : state === 'playing' ? <Stop size={12} weight="fill" /> : <Play size={12} weight="fill" />}
    </button>
  );
}

export function PlayingBars() {
  return (
    <span className="inline-flex items-center gap-1.5 text-[11.5px] font-medium text-accent-text">
      <span className="inline-flex h-3 items-end gap-[2px]" aria-hidden>
        {[0, 160, 320].map((d, i) => (
          <span key={d} className="w-[3px] rounded-full bg-accent motion-safe:animate-pulse" style={{ height: `${[60, 100, 75][i]}%`, animationDelay: `${d}ms` }} />
        ))}
      </span>
      Playing sample
    </span>
  );
}

/* ---------- Default voice + browser ---------- */

const CATEGORY: Record<string, string> = {
  premade: 'Ready-made voice',
  cloned: 'Copy of a real voice',
  generated: 'Designed voice',
  professional: 'Professional voice',
  famous: 'Well-known voice',
  high_quality: 'High-quality voice',
};

export function voiceLine(v: Pick<VoiceView, 'traits' | 'category' | 'description'>): string {
  return v.traits || (v.category ? (CATEGORY[v.category] ?? '') : '') || v.description || '';
}

function VoicePanel({ status, loading, apply }: { status: ProviderStatusOut | undefined; loading: boolean; apply: Apply }) {
  const player = usePreview();
  const [tab, setTab] = useState<'account' | 'library'>('account');
  const [q, setQ] = useState('');
  const [debounced, setDebounced] = useState('');
  const [busy, setBusy] = useState<string | null>(null);
  const searchId = useId();
  useEffect(() => {
    const t = setTimeout(() => setDebounced(q.trim()), 250);
    return () => clearTimeout(t);
  }, [q]);
  const library = tab === 'library';
  const voices = useInfiniteQuery({
    queryKey: ['voiceover.voices', { provider: EL, library, search: debounced, paged: true }],
    queryFn: ({ pageParam, signal }) =>
      call(
        'voiceover.voices',
        { provider: EL, ...(library ? { library: true } : {}), ...(debounced ? { search: debounced } : {}), ...(pageParam ? { pageToken: pageParam } : {}) },
        signal,
      ),
    initialPageParam: '',
    getNextPageParam: (last) => last.nextPageToken || undefined,
    staleTime: 60_000,
  });
  const list = voices.data?.pages.flatMap((p) => p.voices) ?? [];
  const note = voices.data?.pages[0]?.note;
  // The default's settings (D193): the voice new projects use.
  const ws = status?.settings;
  const current = list.find((v) => v.id === ws?.voiceId);

  const run = async (key: string, input: Omit<ProviderSettingsIn, 'name'>, done: string) => {
    setBusy(key);
    await apply(input, done);
    setBusy(null);
  };
  const libraryOwner = (v: VoiceView) => (v.library && v.ownerId ? { libraryOwnerId: v.ownerId } : {});

  return (
    <Panel
      id="el-voice-h"
      icon={<SpeakerHigh size={17} />}
      title="Narration voice"
      intro={`The voice new projects use for AI narration: part of ${status?.from ? `${status.from.name}'s settings, the default` : 'the default settings'}. Play a few samples, then choose one. A project with settings of its own chooses in its Settings tab.`}
    >
      <div className="mb-4 flex flex-wrap items-center gap-3 rounded-[12px] bg-hover/60 px-3.5 py-3">
        {loading ? (
          <Skeleton className="h-6 w-60" />
        ) : ws?.voiceId ? (
          <>
            {current ? <PlayButton v={current} player={player} /> : null}
            <div className="min-w-0">
              <div className="text-[13.5px]">
                Default voice: <span className="font-semibold">{ws.voiceName || 'Chosen voice'}</span>
              </div>
              {current ? <div className="text-[12px] text-subtle">{voiceLine(current)}</div> : null}
            </div>
            {player.cur?.id === ws.voiceId && player.cur.state === 'playing' ? <PlayingBars /> : null}
          </>
        ) : (
          <div className="text-[13.5px] text-muted">
            <span className="font-medium text-fg">No default voice yet.</span> Choose one below before making AI narration.
          </div>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <Tabs
          label="Which voices"
          value={tab}
          onChange={setTab}
          items={[
            { value: 'account', label: 'Your voices' },
            { value: 'library', label: 'Voice library' },
          ]}
        />
        <div className="relative ml-auto w-full sm:w-[300px]">
          <label htmlFor={searchId} className="sr-only">
            Search voices
          </label>
          <MagnifyingGlass size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-subtle" aria-hidden />
          <input
            id={searchId}
            type="search"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder={library ? 'Search the library: calm, british, documentary…' : 'Search by name, accent or style'}
            className="h-9 w-full rounded-[10px] bg-panel pl-8 pr-3 text-[13px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
          />
        </div>
      </div>
      {note ? <p className="mt-2 text-[12px] text-subtle">{note}</p> : null}

      <div className="mt-3 overflow-hidden rounded-[14px] hairline">
        {voices.error ? (
          <ErrorPanel compact className="m-3" error={voices.error} onRetry={() => void voices.refetch()} />
        ) : voices.isLoading ? (
          <div className="space-y-2 p-3">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-11" />
            ))}
          </div>
        ) : list.length === 0 ? (
          <p className="px-4 py-8 text-center text-[13px] text-muted">{debounced ? `No voices match “${debounced}”.` : 'No voices here yet.'}</p>
        ) : (
          <ul className="max-h-[420px] divide-y divide-line overflow-y-auto" aria-label={library ? 'Library voices' : 'Your voices'}>
            {list.map((v) => {
              const isDefault = !v.library && v.id === ws?.voiceId;
              const playing = player.cur?.id === v.id && player.cur.state === 'playing';
              return (
                <li key={v.id} className={cx('grid grid-cols-[auto_1fr] items-center gap-x-3 gap-y-2 px-3 py-2.5 sm:grid-cols-[auto_1fr_auto]', isDefault && 'bg-accent-soft')}>
                  <PlayButton v={v} player={player} />
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="truncate text-[13.5px] font-medium">{v.name}</span>
                      {isDefault ? <Badge tone="accent">Default</Badge> : null}
                      {v.cloned ? <Badge>Copy of a real voice</Badge> : null}
                      {playing ? <PlayingBars /> : null}
                    </div>
                    <div className="truncate text-[12px] text-subtle">{voiceLine(v)}</div>
                  </div>
                  <div className="col-span-2 flex flex-wrap items-center gap-1.5 sm:col-span-1 sm:justify-end">
                    {!v.library ? (
                      <IconButton
                        size="sm"
                        label={v.favorite ? `Remove ${v.name} from favorites` : `Add ${v.name} to favorites`}
                        aria-pressed={!!v.favorite}
                        disabled={busy === `fav:${v.id}`}
                        onClick={() =>
                          void run(
                            `fav:${v.id}`,
                            v.favorite ? { unfavorite: v.id } : { favorite: v.id },
                            v.favorite ? `${v.name} removed from favorites` : `${v.name} added to favorites`,
                          )
                        }
                      >
                        <Star size={15} weight={v.favorite ? 'fill' : 'regular'} className={v.favorite ? 'text-accent' : undefined} />
                      </IconButton>
                    ) : null}
                    {!isDefault ? (
                      <Button
                        size="sm"
                        loading={busy === `def:${v.id}`}
                        onClick={() => void run(`def:${v.id}`, { voice: v.id, ...libraryOwner(v) }, `${v.name} is now the default voice`)}
                      >
                        Use as default
                      </Button>
                    ) : null}
                  </div>
                </li>
              );
            })}
          </ul>
        )}
        {voices.hasNextPage ? (
          <div className="border-t border-line p-2 text-center">
            <Button size="sm" variant="ghost" loading={voices.isFetchingNextPage} onClick={() => void voices.fetchNextPage()}>
              Show more voices
            </Button>
          </div>
        ) : null}
      </div>
      <span className="sr-only" aria-live="polite">
        {player.cur?.state === 'playing' ? `Playing a sample of ${list.find((v) => v.id === player.cur?.id)?.name ?? 'the voice'}` : ''}
      </span>
    </Panel>
  );
}

/* ---------- Model and how the voice sounds ---------- */

interface Draft {
  modelId: string;
  stability?: number;
  similarityBoost?: number;
  style?: number;
  speed?: number;
  speakerBoost?: boolean;
  outputFormat?: string;
}

const FORMATS: { value: string; label: string }[] = [
  { value: '', label: 'Standard (MP3), right for most videos' },
  { value: 'mp3_44100_192', label: 'High (MP3), needs a paid ElevenLabs plan' },
  { value: 'wav_44100', label: 'Lossless (WAV), needs a higher ElevenLabs plan' },
];

export function costLine(m: VoiceModel): string {
  const c = m.costMultiplier;
  if (!c || c === 1) return 'About 1 credit per character';
  if (c === 0.5) return 'Half a credit per character: cheaper, a little less natural';
  return `About ${c} credits per character`;
}

function SoundPanel({
  status,
  models,
  defaultModel,
  modelsError,
  loading,
  apply,
}: {
  status: ProviderStatusOut | undefined;
  models: VoiceModel[];
  defaultModel: string | undefined;
  modelsError: unknown;
  loading: boolean;
  apply: Apply;
}) {
  const ws = status?.settings;
  const saved = useMemo<Draft>(
    () => ({
      modelId: ws?.modelId ?? '',
      stability: ws?.voice?.stability,
      similarityBoost: ws?.voice?.similarityBoost,
      style: ws?.voice?.style,
      speed: ws?.voice?.speed,
      speakerBoost: ws?.voice?.speakerBoost,
      outputFormat: ws?.voice?.outputFormat,
    }),
    [ws],
  );
  const savedKey = JSON.stringify(saved);
  const [draft, setDraft] = useState<Draft>(saved);
  const [saving, setSaving] = useState(false);
  useEffect(() => setDraft(saved), [savedKey]); // eslint-disable-line react-hooks/exhaustive-deps
  const dirty = JSON.stringify(draft) !== savedKey;
  const modelSel = useId();
  const formatSel = useId();
  const boostId = useId();
  const model = models.find((m) => m.modelId === (draft.modelId || defaultModel));
  const auto = models.find((m) => m.modelId === defaultModel);
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }));
  const formats = draft.outputFormat && !FORMATS.some((f) => f.value === draft.outputFormat) ? [...FORMATS, { value: draft.outputFormat, label: draft.outputFormat }] : FORMATS;
  const changedFromDefaults =
    saved.stability !== undefined ||
    saved.similarityBoost !== undefined ||
    saved.style !== undefined ||
    saved.speed !== undefined ||
    saved.speakerBoost !== undefined ||
    !!saved.outputFormat;

  const save = async () => {
    const input: Omit<ProviderSettingsIn, 'name'> = {};
    const clear: string[] = [];
    if (draft.modelId !== saved.modelId) {
      if (draft.modelId) input.modelId = draft.modelId;
      else clear.push('model');
    }
    for (const f of ['stability', 'similarityBoost', 'style', 'speed'] as const) {
      if (draft[f] === saved[f]) continue;
      const v = draft[f];
      if (v === undefined) clear.push(f);
      else input[f] = v;
    }
    if (draft.speakerBoost !== saved.speakerBoost) {
      if (draft.speakerBoost === undefined) clear.push('speakerBoost');
      else input.speakerBoost = draft.speakerBoost;
    }
    if ((draft.outputFormat ?? '') !== (saved.outputFormat ?? '')) {
      if (draft.outputFormat) input.outputFormat = draft.outputFormat;
      else clear.push('outputFormat');
    }
    if (clear.length) input.clear = clear;
    setSaving(true);
    await apply(input, 'Voice settings saved for new projects (the default settings)');
    setSaving(false);
  };

  const speedText = (v: number) => (Math.abs(v - 1) < 0.005 ? 'Normal speed' : v > 1 ? `${Math.round((v - 1) * 100)}% faster` : `${Math.round((1 - v) * 100)}% slower`);
  const stabilityText = (v: number) => (v < 0.35 ? 'More expressive' : v > 0.65 ? 'More steady' : 'Balanced');
  const closeText = (v: number) => (v < 0.4 ? 'Looser' : v > 0.8 ? 'Very close' : 'Close');
  const styleText = (v: number) => (v < 0.05 ? 'Natural' : v > 0.6 ? 'Strong' : 'Some');

  return (
    <Panel
      id="el-sound-h"
      icon={<SlidersHorizontal size={17} />}
      title="How the voice sounds"
      intro="Part of the default settings, with the voice above. Changes apply to narration made from now on; lines you already made keep their sound until you remake them."
    >
      {loading ? (
        <div className="space-y-3">
          <Skeleton className="h-10" />
          <Skeleton className="h-16" />
          <Skeleton className="h-16" />
        </div>
      ) : (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
          className="space-y-5"
        >
          <div className="grid grid-cols-1 gap-x-10 gap-y-6 md:grid-cols-2">
            <div>
              <label htmlFor={modelSel} className="text-[13px] font-medium">
                Model
              </label>
              <p className="mt-0.5 text-[12px] leading-relaxed text-subtle">The AI that turns your script into speech. They differ in naturalness, languages and price.</p>
              {modelsError ? (
                <ErrorPanel compact className="mt-2" error={modelsError} />
              ) : (
                <select
                  id={modelSel}
                  value={draft.modelId}
                  onChange={(e) => set({ modelId: e.target.value })}
                  className="mt-2 h-9 w-full rounded-[10px] bg-panel px-3 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent focus:outline-offset-0"
                >
                  <option value="">Automatic{auto ? `: ${auto.name}` : ''} (recommended)</option>
                  {models.map((m) => (
                    <option key={m.modelId} value={m.modelId}>
                      {m.name}
                    </option>
                  ))}
                </select>
              )}
              {model ? (
                <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[12px] text-muted">
                  <li>{costLine(model)}</li>
                  <li>{model.languages.length > 1 ? `${model.languages.length} languages` : (model.languages[0] ?? 'English')}</li>
                  <li>{model.timestamps ? 'Word timing for captions' : model.timestampsNote || 'No word timing; captions are estimated'}</li>
                </ul>
              ) : null}
            </div>

            <div>
              <label htmlFor={formatSel} className="text-[13px] font-medium">
                Audio quality
              </label>
              <p className="mt-0.5 text-[12px] leading-relaxed text-subtle">Higher quality makes bigger files; most viewers will not hear a difference.</p>
              <select
                id={formatSel}
                value={draft.outputFormat ?? ''}
                onChange={(e) => set({ outputFormat: e.target.value || undefined })}
                className="mt-2 h-9 w-full rounded-[10px] bg-panel px-3 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent focus:outline-offset-0"
              >
                {formats.map((f) => (
                  <option key={f.value} value={f.value}>
                    {f.label}
                  </option>
                ))}
              </select>
            </div>

            <Slider
              label="Delivery"
              help="Lower sounds livelier and more varied; higher sounds calmer and more consistent from line to line."
              left="More expressive"
              right="More steady"
              value={draft.stability}
              fallback={PROVIDER_DEFAULTS.stability}
              min={0}
              max={1}
              step={0.05}
              text={stabilityText}
              onChange={(v) => set({ stability: v })}
            />
            <Slider
              label="Closeness to the original voice"
              help="How closely the speech copies the voice's sample. Very close can also copy noise from that sample."
              left="Looser"
              right="Closer"
              value={draft.similarityBoost}
              fallback={PROVIDER_DEFAULTS.similarityBoost}
              min={0}
              max={1}
              step={0.05}
              text={closeText}
              onChange={(v) => set({ similarityBoost: v })}
            />
            <Slider
              label="Speaking style"
              help="Adds more of the voice's own way of speaking. Can make the delivery less even."
              left="Natural"
              right="Exaggerated"
              value={draft.style}
              fallback={PROVIDER_DEFAULTS.style}
              min={0}
              max={1}
              step={0.05}
              text={styleText}
              disabled={!!model && !model.canUseStyle}
              note={model && !model.canUseStyle ? 'The chosen model does not use this.' : undefined}
              onChange={(v) => set({ style: v })}
            />
            <Slider
              label="Speed"
              help="How fast the narrator talks. Small changes sound most natural."
              left="Slower"
              right="Faster"
              value={draft.speed}
              fallback={PROVIDER_DEFAULTS.speed}
              min={0.7}
              max={1.2}
              step={0.01}
              text={speedText}
              onChange={(v) => set({ speed: v })}
            />

            <div className={cx('flex items-start gap-3 md:col-span-2', model && !model.canUseSpeakerBoost && 'opacity-60')}>
              <input
                id={boostId}
                type="checkbox"
                checked={draft.speakerBoost ?? PROVIDER_DEFAULTS.speakerBoost}
                disabled={!!model && !model.canUseSpeakerBoost}
                onChange={(e) => set({ speakerBoost: e.target.checked })}
                className="mt-[3px] h-4 w-4 accent-[var(--accent)]"
              />
              <label htmlFor={boostId} className="text-[13px]">
                <span className="font-medium">Make it sound more like the original voice</span>
                <span className="block text-[12px] leading-relaxed text-subtle">
                  {model && !model.canUseSpeakerBoost ? 'The chosen model does not use this.' : 'Slightly slower to make. On by default.'}
                </span>
              </label>
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-2 border-t border-line pt-4">
            <Button type="submit" variant="primary" disabled={!dirty} loading={saving}>
              Save changes
            </Button>
            {dirty ? (
              <Button variant="ghost" onClick={() => setDraft(saved)}>
                Undo changes
              </Button>
            ) : null}
            {changedFromDefaults || draft.modelId ? (
              <Button
                variant="ghost"
                className="ml-auto"
                onClick={() =>
                  setDraft({ modelId: '', stability: undefined, similarityBoost: undefined, style: undefined, speed: undefined, speakerBoost: undefined, outputFormat: undefined })
                }
              >
                Back to ElevenLabs defaults
              </Button>
            ) : null}
          </div>

          <SettingsDetails status={status} className="mt-0" />
        </form>
      )}
    </Panel>
  );
}

/** The exact values narration uses (ElevenLabs' own names), behind Details. */
function SettingsDetails({ status, className }: { status: ProviderStatusOut | undefined; className?: string }) {
  const e = status?.effective;
  return (
    <Details className={className}>
      {e ? <SettingsRows status={status} e={e} /> : <span>Nothing chosen yet.</span>}
      {e ? <p className="tc mt-2 [overflow-wrap:anywhere]">metarush providers settings elevenlabs --help</p> : null}
    </Details>
  );
}

function SettingsRows({ status, e }: { status: ProviderStatusOut | undefined; e: NonNullable<ProviderStatusOut['effective']> }) {
  const s = e.settings;
  const rows: [string, string | undefined, string | undefined][] = [
    ['voice', e.voiceId ? `${e.voiceName ?? ''} (${e.voiceId})` : 'not chosen', e.from.voice],
    ['model', e.modelId ? `${e.modelName ?? ''} (${e.modelId})` : 'automatic', e.from.model],
    ['stability', s.stability?.toFixed(2) ?? 'provider default (0.50)', e.from.stability],
    ['similarity boost', s.similarityBoost?.toFixed(2) ?? 'provider default (0.75)', e.from.similarityBoost],
    ['style', s.style?.toFixed(2) ?? 'provider default (0)', e.from.style],
    ['speed', s.speed?.toFixed(2) ?? 'provider default (1.00)', e.from.speed],
    ['speaker boost', s.speakerBoost === undefined ? 'provider default (on)' : s.speakerBoost ? 'on' : 'off', e.from.speakerBoost],
    ['output format', e.outputFormat, e.from.outputFormat],
  ];
  return (
    <div className="space-y-2">
      <p>What narration uses right now{status?.project ? ' for the selected project' : ''}, with ElevenLabs' names for each setting and where it comes from.</p>
      <dl className="grid grid-cols-1 gap-x-3 sm:grid-cols-[130px_minmax(0,1fr)] sm:gap-y-0.5">
        {rows.map(([k, v, from]) => (
          <div key={k} className="contents">
            <dt>{k}</dt>
            <dd className="tc min-w-0 text-muted [overflow-wrap:anywhere] max-sm:mb-1.5">
              {v}
              {from ? <span className="text-subtle"> · {from === 'workspace' ? 'workspace' : from}</span> : null}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

export function Slider({
  label,
  help,
  left,
  right,
  value,
  fallback,
  min,
  max,
  step,
  text,
  onChange,
  disabled,
  note,
}: {
  label: string;
  help: string;
  left: string;
  right: string;
  value: number | undefined;
  fallback: number;
  min: number;
  max: number;
  step: number;
  text: (v: number) => string;
  onChange: (v: number) => void;
  disabled?: boolean;
  note?: string;
}) {
  const id = useId();
  const v = value ?? fallback;
  const pct = ((v - min) / (max - min)) * 100;
  return (
    <div className={cx(disabled && 'opacity-60')}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        <label htmlFor={id} className="text-[13px] font-medium">
          {label}
        </label>
        <span className="text-[12px] text-muted">
          {text(v)}
          <span className="text-subtle">{value === undefined ? ' · ElevenLabs default' : ''}</span>
        </span>
      </div>
      <p id={`${id}-help`} className="mt-0.5 text-[12px] leading-relaxed text-subtle">
        {help}
      </p>
      <input
        id={id}
        type="range"
        min={min}
        max={max}
        step={step}
        value={v}
        disabled={disabled}
        aria-describedby={`${id}-help`}
        aria-valuetext={text(v)}
        onChange={(e) => onChange(Number(e.target.value))}
        style={{ '--pct': `${pct}%` } as CSSProperties}
        className={cx(
          'mt-2 block h-5 w-full cursor-pointer appearance-none bg-transparent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed',
          // Track: filled up to the value in the accent color (the native track is near-black on light themes).
          '[&::-webkit-slider-runnable-track]:h-1.5 [&::-webkit-slider-runnable-track]:rounded-full [&::-webkit-slider-runnable-track]:bg-[linear-gradient(to_right,var(--accent)_var(--pct),var(--line-strong)_var(--pct))]',
          '[&::-moz-range-track]:h-1.5 [&::-moz-range-track]:rounded-full [&::-moz-range-track]:bg-[linear-gradient(to_right,var(--accent)_var(--pct),var(--line-strong)_var(--pct))]',
          '[&::-webkit-slider-thumb]:-mt-[5px] [&::-webkit-slider-thumb]:h-4 [&::-webkit-slider-thumb]:w-4 [&::-webkit-slider-thumb]:appearance-none [&::-webkit-slider-thumb]:rounded-full [&::-webkit-slider-thumb]:border-2 [&::-webkit-slider-thumb]:border-[var(--bg-panel)] [&::-webkit-slider-thumb]:bg-[var(--accent)] [&::-webkit-slider-thumb]:shadow-[0_0_0_1px_var(--accent-line)]',
          '[&::-moz-range-thumb]:h-4 [&::-moz-range-thumb]:w-4 [&::-moz-range-thumb]:rounded-full [&::-moz-range-thumb]:border-2 [&::-moz-range-thumb]:border-[var(--bg-panel)] [&::-moz-range-thumb]:bg-[var(--accent)]',
        )}
      />
      <div className="mt-0.5 flex justify-between text-[11.5px] text-muted" aria-hidden>
        <span>{left}</span>
        <span>{right}</span>
      </div>
      {note ? <p className="mt-1 text-[12px] text-subtle">{note}</p> : null}
    </div>
  );
}

/* ---------- One project: its own voice and a spending limit ---------- */

function ProjectPanel({
  projects,
  loadingProjects,
  project,
  setProjectId,
  status,
  loadingStatus,
  apply,
}: {
  projects: ProjectSummary[];
  loadingProjects: boolean;
  project: ProjectSummary | undefined;
  setProjectId: (id: string) => void;
  status: ProviderStatusOut | undefined;
  loadingStatus: boolean;
  apply: Apply;
}) {
  const selId = useId();
  const limitId = useId();
  const budget = status?.budget;
  // The field shows the saved limit until the user edits it; an edit survives the
  // project's numbers arriving late and is dropped on save or when the project changes.
  const [edited, setEdited] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const savedLimit = budget?.limitCredits ? String(Math.round(budget.limitCredits)) : '';
  useEffect(() => setEdited(null), [project?.id]);
  const limit = edited ?? savedLimit;
  const used = budget?.usedCredits ?? 0;
  const limitNum = Number(limit);
  const run = async (key: string, input: Omit<ProviderSettingsIn, 'name'>, done: string) => {
    if (!project) return false;
    setBusy(key);
    const ok = await apply({ scope: 'project', projectId: project.id, ...input }, done);
    setBusy(null);
    return ok;
  };

  return (
    <Panel id="el-project-h" icon={<FolderSimple size={17} />} title="For one project" intro="A project's voice (from the settings it uses) and a limit on what its narration may cost.">
      <div className="grid grid-cols-1 gap-x-10 gap-y-6 md:grid-cols-[1fr_1fr_1.3fr]">
        <div>
          <label htmlFor={selId} className="text-[13px] font-medium">
            Project
          </label>
          {loadingProjects ? (
            <Skeleton className="mt-2 h-9" />
          ) : (
            <select
              id={selId}
              value={project?.id ?? ''}
              onChange={(e) => setProjectId(e.target.value)}
              className="mt-2 h-9 w-full rounded-[10px] bg-panel px-3 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent focus:outline-offset-0"
            >
              <option value="">Choose a project…</option>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          )}

          {!project ? (
            <p className="mt-3 text-[12.5px] leading-relaxed text-subtle">
              {projects.length ? 'Pick a project to see its voice and spending.' : 'Projects you create show up here.'}
            </p>
          ) : null}
        </div>
        {project && loadingStatus ? (
          <>
            <Skeleton className="h-16" />
            <Skeleton className="h-24" />
          </>
        ) : null}
        {project && !loadingStatus ? (
          <>
            <div>
              <div className="text-[13px] font-medium">Voice</div>
              <div className="mt-2 text-[13px] text-muted" data-project-voice>
                {status?.settings?.voiceName ?? 'No voice chosen yet'}
                {status?.from ? <span className="text-subtle"> · from {status.from.id === project.id ? 'its own settings' : `${status.from.name}'s settings`}</span> : null}
              </div>
              <Link to={`/p/${project.id}?tab=settings&part=voice`} className="mt-1 inline-block text-[12.5px] font-medium text-accent-text hover:underline">
                Change it in the project's Settings tab
              </Link>
            </div>

            <form
              onSubmit={(e) => {
                e.preventDefault();
                const n = limit.trim() ? Number(limit) : 0;
                if (!Number.isFinite(n) || n < 0) return;
                void run('budget', { budgetCredits: n }, n > 0 ? `${project.name} may spend up to ${n.toLocaleString()} credits` : `${project.name} has no spending limit`).then(
                  (ok) => ok && setEdited(null),
                );
              }}
            >
              <label htmlFor={limitId} className="flex items-center gap-1.5 text-[13px] font-medium">
                <Coins size={14} className="text-subtle" aria-hidden /> Spending limit
              </label>
              <p id={`${limitId}-help`} className="mt-0.5 text-[12px] leading-relaxed text-subtle">
                Credits are how ElevenLabs charges: about one per character of narration with most models, half with the faster ones. MetaRush stops before this project goes over
                the limit. Leave it empty for no limit.
              </p>
              <div className="mt-2 flex items-center gap-2">
                <input
                  id={limitId}
                  inputMode="numeric"
                  value={limit}
                  onChange={(e) => setEdited(e.target.value.replace(/[^\d]/g, ''))}
                  placeholder="No limit"
                  aria-describedby={`${limitId}-help`}
                  className="tc h-9 w-[140px] rounded-[10px] bg-panel px-3 text-[13px] text-fg hairline placeholder:font-sans placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
                />
                <span className="text-[12.5px] text-muted">credits</span>
                <Button type="submit" size="sm" disabled={limit === savedLimit || loadingStatus} loading={busy === 'budget'} className="ml-auto">
                  Save limit
                </Button>
              </div>
              <div className="mt-3 text-[12.5px] text-muted">
                Used so far: <span className="text-fg">{used.toLocaleString()} credits</span>
                {budget?.remainingCredits !== undefined ? <> · {budget.remainingCredits.toLocaleString()} left</> : null}
              </div>
              {budget?.limitCredits ? (
                <Progress className="mt-2" value={used / budget.limitCredits} tone={used / budget.limitCredits > 0.9 ? 'danger' : 'accent'} label={`${project.name} spending`} />
              ) : null}
              {limit && limitNum > 0 && limitNum < used ? (
                <p className="mt-2 text-[12px] text-danger">That is less than this project has already used, so no more narration could be made.</p>
              ) : null}
            </form>
          </>
        ) : null}
      </div>
    </Panel>
  );
}
