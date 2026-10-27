import { useEffect, useId, useMemo, useState } from 'react';
import { Link } from 'react-router';
import { keepPreviousData, useInfiniteQuery } from '@tanstack/react-query';
import { ArrowRight, Coins, Key, MagnifyingGlass, Robot, Sparkle } from '@phosphor-icons/react';
import { call } from '../../../api/client';
import { useOp } from '../../../api/queries';
import type { NarrationOut, ProviderSettingsIn, TakeView, VoiceEstimate, VoiceView } from '../../../api/types';
import { markJobSeen } from '../../../lib/workbench';
import { costLine, PlayButton, PlayingBars, Slider, usePreview, voiceLine } from '../../settings/ElevenLabsSettings';
import { useDialog } from '../../../ui/Dialog';
import { Badge, Button, cx, ErrorPanel, Progress, Skeleton } from '../../../ui/primitives';
import { CostEstimate } from '../../../components/CostEstimate';
import { useEstimatesFirst } from '../../../lib/preferences';
import { checkApplied, errorText, useChange } from './api';

/*
 * The AI voice: which voice reads the lines and how it sounds (the project's
 * Content Settings, D193: read-only here when they are another project's),
 * what it costs before anything is made — an estimate that opens to each
 * line's voice, model, characters and credits (D208) — the project's spending
 * limit, and making each line (again). Lines made before with the same words,
 * voice and settings are reused for free. Making a line again asks about its
 * price first unless the person turned estimates off. The key itself is only
 * ever handled in Settings → Integrations.
 */

const EL = 'elevenlabs' as const;

function n(v: number): string {
  return Math.round(v).toLocaleString();
}

export function AIVoicePanel({ projectId, projectName, revisionId, show, only }: {
  projectId: string;
  projectName: string;
  revisionId: string;
  show: NarrationOut;
  /** Only these lines (the voiceover of one clip). */
  only?: string[];
}) {
  const list = useOp('provider.list', {});
  const el = list.data?.providers.find((p) => p.name === EL);
  if (list.isLoading) return <Skeleton className="h-64" />;
  if (list.error) return <ErrorPanel error={list.error} onRetry={() => void list.refetch()} />;
  if (!el?.configured) {
    return (
      <section className="rounded-[14px] bg-panel p-4 hairline" aria-label="AI voice is not set up">
        <div className="flex items-start gap-3">
          <span className="grid h-10 w-10 shrink-0 place-items-center rounded-[11px] bg-hover text-muted" aria-hidden>
            <Key size={18} />
          </span>
          <div className="min-w-0">
            <h3 className="text-[14.5px] font-semibold">AI voices come from ElevenLabs</h3>
            <p className="mt-1 max-w-[60ch] text-[13px] leading-relaxed text-muted">
              To use them, add your ElevenLabs key on this computer. It stays on this computer and MetaRush only sends it to ElevenLabs. Until then you can record the lines or try
              them with a free computer voice.
            </p>
            <Link
              to="/settings/integrations"
              className="mt-3 inline-flex h-9 items-center gap-2 rounded-[10px] bg-accent px-3.5 text-[13.5px] font-medium text-accent-ink hover:bg-accent-hover"
            >
              Open Settings → Integrations <ArrowRight size={14} />
            </Link>
          </div>
        </div>
      </section>
    );
  }
  if (!show.hasScript) {
    return <p className="rounded-[14px] p-4 text-[13px] text-muted hairline">Write the lines first (or ask Claude for them); then an AI voice can read them.</p>;
  }
  return <Configured projectId={projectId} projectName={projectName} revisionId={revisionId} show={show} only={only} />;
}

function Configured({ projectId, projectName, revisionId, show, only }: { projectId: string; projectName: string; revisionId: string; show: NarrationOut; only?: string[] }) {
  const st = useOp('provider.status', { name: EL, projectId }, { placeholderData: keepPreviousData });
  // One clip: its lines that have no voice yet (the whole cut: the server picks them).
  const wanted = only ? show.beats.filter((b) => only.includes(b.id) && (!b.take || b.take.source === 'scratch')).map((b) => b.id) : undefined;
  // Priced once a voice is chosen (without one there is nothing to price).
  const hasVoice = !!st.data?.effective.voiceId;
  const est = useOp('voiceover.estimate', wanted?.length === 0 || !hasVoice ? null : { revisionId, ...(wanted ? { beats: wanted } : {}) }, { retry: false, placeholderData: keepPreviousData });
  const takes = useOp('voiceover.takes', { revisionId }, { placeholderData: keepPreviousData });
  const change = useChange();
  const dialog = useDialog();
  const estimatesFirst = useEstimatesFirst();
  const [busy, setBusy] = useState<string | null>(null);
  const [choosing, setChoosing] = useState(false);
  // No voice yet: the voices are open right away.
  useEffect(() => {
    if (st.data && !st.data.effective.voiceId && !st.data.readOnly) setChoosing(true);
  }, [st.data]);
  const eff = st.data?.effective;
  const budget = st.data?.budget;
  const settingsOf = st.data?.from;
  const readOnly = !!st.data?.readOnly;
  const chosen = useMemo(() => new Map((takes.data?.takes ?? []).filter((t) => t.chosen).map((t) => [t.beatId ?? '', t])), [takes.data]);
  const priced = useMemo(() => new Map((est.data?.lines ?? []).map((l) => [l.beatId, l])), [est.data]);
  const e = est.data;
  const toMake = e ? e.lines.length - e.cachedLines : 0;

  const generate = (beats?: string[], regenerate = false, label?: string) =>
    (async () => {
      setBusy(beats?.join(',') ?? 'all');
      await change(
        async () => {
          const which = beats ?? wanted;
          const out = await call('voiceover.tts', { revisionId, ...(which ? { beats: which } : {}), ...(regenerate ? { regenerate: true } : {}), apply: true });
          markJobSeen(out.jobId);
          checkApplied(out.applied);
          return out;
        },
        (out) => out.summary.replace(/ It is in the cut now as version \d+\./, ''),
        label ?? 'The AI voice was not made',
      );
      setBusy(null);
    })();

  const remake = async (beatId: string, line: number) => {
    // Estimates off (D208): no price question; the project's budget still decides.
    if (!estimatesFirst) {
      await generate([beatId], true, `Line ${line} was not made again`);
      return;
    }
    let price = '';
    let cost: VoiceEstimate['cost'] | undefined;
    try {
      const p = await call('voiceover.estimate', { revisionId, beats: [beatId], regenerate: true });
      price = p.characters ? `${p.cost.about} (${n(p.credits)} credits)` : 'nothing (it was made before)';
      cost = p.cost;
      if (!p.allowed) {
        await dialog.alert({ title: `Line ${line} cannot be made again right now`, body: p.warnings?.join(' ') ?? 'It would go over the spending limit.' });
        return;
      }
    } catch (err) {
      await dialog.alert({ title: 'Could not price it', body: errorText(err) });
      return;
    }
    const ok = await dialog.confirm({
      title: `Make line ${line} again?`,
      body: (
        <div className="space-y-2">
          <p>A new AI take of this line costs {price}. The old take is kept.</p>
          {cost ? <CostEstimate estimate={cost} defaultOpen /> : null}
        </div>
      ),
      confirmLabel: 'Make it again',
    });
    if (ok) await generate([beatId], true, `Line ${line} was not made again`);
  };

  const saveSettings = async (input: Omit<ProviderSettingsIn, 'name'>, done: string) =>
    change(() => call('provider.settings', { name: EL, scope: 'project', projectId, ...input }), done, 'That change was not saved');

  return (
    <div className="space-y-5">
      <section aria-labelledby="ai-voice-h" className="rounded-[14px] bg-panel p-4 hairline">
        <div className="flex flex-wrap items-center gap-3">
          <span className="grid h-10 w-10 shrink-0 place-items-center rounded-[11px] bg-accent-soft text-accent-text" aria-hidden>
            <Robot size={19} />
          </span>
          <div className="min-w-0 flex-1">
            <h3 id="ai-voice-h" className="text-[14px] font-semibold">
              {eff?.voiceName ? `Voice: ${eff.voiceName}` : eff?.voiceId ? 'A chosen voice' : 'No voice chosen yet'}
            </h3>
            <p className="text-[12.5px] text-muted">
              {readOnly && settingsOf
                ? `From ${settingsOf.name}'s settings: change it there, or give ${projectName} settings of its own.`
                : eff?.voiceId
                  ? settingsOf && settingsOf.id !== projectId
                    ? `From ${settingsOf.name}'s settings.`
                    : `Part of ${projectName}'s settings${st.data?.usedBy ? ` (also used by ${st.data.usedBy} other project${st.data.usedBy === 1 ? '' : 's'})` : ''}.`
                  : 'Choose the voice that reads your lines.'}{' '}
              Lines can also have a voice of their own.
            </p>
          </div>
          {readOnly ? (
            <Link to={`/p/${projectId}?tab=settings&part=voice`} className="inline-flex h-8 items-center rounded-[9px] px-3 text-[12.5px] font-medium text-accent-text hairline hover:bg-hover" data-voice-settings-link>
              Settings
            </Link>
          ) : (
            <Button size="sm" variant={eff?.voiceId ? 'secondary' : 'primary'} onClick={() => setChoosing((v) => !v)} aria-expanded={choosing}>
              {choosing ? 'Done choosing' : eff?.voiceId ? 'Change voice' : 'Choose a voice'}
            </Button>
          )}
        </div>
        {choosing && !readOnly ? (
          <VoiceChooser
            current={eff?.voiceId}
            onPick={(v) => void saveSettings({ voice: v.id }, `${projectName} now uses ${v.name}`).then(() => setChoosing(false))}
          />
        ) : null}
        {eff && !readOnly ? (
          <HowItSounds
            speed={eff.settings.speed}
            stability={eff.settings.stability}
            model={eff.from.model === 'settings' ? eff.modelId : undefined}
            inherited={eff.from.model !== 'settings' ? eff.modelName : undefined}
            onSave={saveSettings}
          />
        ) : null}
      </section>

      <section aria-labelledby="ai-price-h" className="rounded-[14px] bg-panel p-4 hairline">
        <h3 id="ai-price-h" className="flex items-center gap-2 text-[14px] font-semibold">
          <Coins size={16} className="text-subtle" aria-hidden /> What it costs
        </h3>
        {st.data && !hasVoice ? (
          <p className="mt-1.5 text-[13px] text-muted">Choose a voice above to see what your lines cost.</p>
        ) : wanted?.length === 0 ? (
          <>
            <p className="mt-1.5 text-[13px] text-fg">Every line here already has a voice. Make single lines again below.</p>
            <Budget used={budget?.usedCredits ?? 0} limit={budget?.limitCredits} />
          </>
        ) : est.isLoading ? (
          <Skeleton className="mt-3 h-12" />
        ) : est.error ? (
          <p className="mt-2 text-[13px] text-muted">{errorText(est.error)}</p>
        ) : e ? (
          <>
            <p className="mt-1.5 text-[13px] text-fg" aria-live="polite">
              {e.lines.length === 0 ? 'Every line already has a voice. Make single lines again below.' : e.summary}
            </p>
            {e.cost.parts.length ? <CostEstimate estimate={e.cost} className="mt-1.5" /> : null}
            {e.warnings?.map((w) => (
              <p key={w} className="mt-1 text-[12.5px] text-danger">
                {w}
              </p>
            ))}
            <Budget used={budget?.usedCredits ?? 0} limit={budget?.limitCredits} remaining={e.accountRemaining} />
            {e.lines.length ? (
              <div className="mt-3 flex flex-wrap items-center gap-2">
                <Button variant="primary" icon={<Sparkle size={14} />} disabled={!e.allowed} loading={busy === 'all'} onClick={() => void generate()}>
                  {toMake === 0
                    ? `Use the AI voice already made (free)`
                    : `Make the AI voice for ${toMake === 1 ? '1 line' : `${toMake} lines`} (${e.cost.about})`}
                </Button>
                {!e.allowed ? <span className="text-[12px] text-subtle">Raise the limit in Settings → Integrations, or shorten the script.</span> : null}
              </div>
            ) : null}
          </>
        ) : null}
      </section>

      <section aria-labelledby="ai-lines-h">
        <h3 id="ai-lines-h" className="mb-2 text-[13.5px] font-semibold">
          Line by line
        </h3>
        <ol className="divide-y divide-line overflow-hidden rounded-[14px] hairline" aria-label="AI voice, line by line">
          {show.beats.filter((l) => !only || only.includes(l.id)).map((l) => {
            const t: TakeView | undefined = chosen.get(l.id);
            const ai = l.take?.source === 'tts';
            const stale = ai && t && t.text.trim() !== l.text.trim();
            const p = priced.get(l.id);
            return (
              <li key={l.id} className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 py-2.5">
                <span className="tc grid h-6 min-w-6 place-items-center rounded-full bg-hover px-1.5 text-[11.5px] font-semibold text-muted">{l.line}</span>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[13px] text-fg">{l.text}</div>
                  <div className="text-[11.5px] text-subtle">
                    {stale
                      ? 'The words changed since this voice was made'
                      : ai
                        ? `AI voice${t?.voiceName ? ` (${t.voiceName})` : ''}`
                        : l.take
                          ? l.take.source === 'scratch'
                            ? 'Free computer voice for now'
                            : 'Has a recording (an AI voice would replace it)'
                          : 'No sound yet'}
                    {p ? ` · ${p.cached ? 'free (made before)' : `about ${n(p.credits)} credits`}` : ''}
                  </div>
                </div>
                {stale ? <Badge tone="accent">words changed</Badge> : null}
                <Button
                  size="sm"
                  variant={ai && !stale ? 'ghost' : 'secondary'}
                  loading={busy === l.id}
                  onClick={() => void (ai || l.take ? remake(l.id, l.line) : generate([l.id]))}
                >
                  {ai ? 'Make again' : l.take && l.take.source !== 'scratch' ? 'Use AI voice' : 'Make'}
                </Button>
              </li>
            );
          })}
        </ol>
        <p className="mt-2 text-[12px] text-subtle">Everything made with an AI voice is marked as an AI voice in the finished files.</p>
      </section>
    </div>
  );
}

function Budget({ used, limit, remaining }: { used: number; limit?: number; remaining?: number }) {
  return (
    <div className="mt-3 rounded-[12px] bg-hover/50 px-3 py-2.5 text-[12.5px] text-muted">
      {limit ? (
        <>
          This project has used <span className="text-fg">{n(used)}</span> of its <span className="text-fg">{n(limit)}</span> credits.
          <Progress className="mt-2" value={used / limit} tone={used / limit > 0.9 ? 'danger' : 'accent'} label="Project spending" />
        </>
      ) : (
        <>
          This project has used <span className="text-fg">{n(used)}</span> credits so far and has no spending limit.{' '}
          <Link to="/settings/integrations" className="font-medium text-accent-text hover:underline">
            Set one
          </Link>
        </>
      )}
      {remaining !== undefined ? <div className="mt-1.5">Your ElevenLabs plan has {n(remaining)} characters left this period.</div> : null}
    </div>
  );
}

export function HowItSounds({ speed, stability, model, inherited, onSave, saveLabel }: {
  speed?: number;
  stability?: number;
  /** The model the settings chose, if they chose one. */
  model?: string;
  /** The model it uses otherwise (the automatic choice), by name. */
  inherited?: string;
  onSave: (input: Omit<ProviderSettingsIn, 'name'>, done: string) => Promise<unknown>;
  saveLabel?: string;
}) {
  const [sp, setSp] = useState(speed);
  const [stab, setStab] = useState(stability);
  const [mod, setMod] = useState(model ?? '');
  const [saving, setSaving] = useState(false);
  const models = useOp('voiceover.models', {}, { staleTime: 300_000 });
  const modelSel = useId();
  useEffect(() => setSp(speed), [speed]);
  useEffect(() => setStab(stability), [stability]);
  useEffect(() => setMod(model ?? ''), [model]);
  const dirty = sp !== speed || stab !== stability || mod !== (model ?? '');
  const picked = models.data?.models.find((m) => m.modelId === mod);
  const speedText = (v: number) => (Math.abs(v - 1) < 0.005 ? 'Normal speed' : v > 1 ? `${Math.round((v - 1) * 100)}% faster` : `${Math.round((1 - v) * 100)}% slower`);
  const stabilityText = (v: number) => (v < 0.35 ? 'More expressive' : v > 0.65 ? 'More steady' : 'Balanced');
  return (
    <details className="mt-4 border-t border-line pt-3">
      <summary className="cursor-pointer text-[12.5px] font-medium text-accent-text">How it sounds</summary>
      <div className="mt-3 grid grid-cols-1 gap-x-8 gap-y-5 md:grid-cols-2">
        <div className="md:col-span-2">
          <label htmlFor={modelSel} className="text-[13px] font-medium">
            Voice model
          </label>
          <p className="mt-0.5 text-[12px] leading-relaxed text-subtle">The AI that turns the lines into speech. Models differ in how natural they sound, their languages and their price.</p>
          {models.error ? (
            <ErrorPanel compact className="mt-2" error={models.error} />
          ) : (
            <select
              id={modelSel}
              value={mod}
              onChange={(e) => setMod(e.target.value)}
              className="mt-2 h-9 w-full max-w-[420px] rounded-[10px] bg-panel px-3 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent focus:outline-offset-0"
            >
              <option value="">Chosen automatically{inherited ? `: ${inherited}` : ''}</option>
              {(models.data?.models ?? []).map((m) => (
                <option key={m.modelId} value={m.modelId}>
                  {m.name}
                </option>
              ))}
            </select>
          )}
          {picked ? <p className="mt-1.5 text-[12px] text-muted">{costLine(picked)}</p> : null}
        </div>
        <Slider label="Speed" help="How fast the voice talks. Small changes sound most natural." left="Slower" right="Faster" value={sp} fallback={1} min={0.7} max={1.2} step={0.01} text={speedText} onChange={setSp} />
        <Slider
          label="Delivery"
          help="Lower sounds livelier; higher sounds calmer and more even from line to line."
          left="More expressive"
          right="More steady"
          value={stab}
          fallback={0.5}
          min={0}
          max={1}
          step={0.05}
          text={stabilityText}
          onChange={setStab}
        />
      </div>
      <div className="mt-3 flex items-center gap-2">
        <Button
          size="sm"
          variant="primary"
          disabled={!dirty}
          loading={saving}
          onClick={async () => {
            setSaving(true);
            const modelChange = mod === (model ?? '') ? {} : mod ? { modelId: mod } : { clear: ['model'] };
            await onSave({ ...(sp !== undefined ? { speed: sp } : {}), ...(stab !== undefined ? { stability: stab } : {}), ...modelChange }, 'Voice settings saved');
            setSaving(false);
          }}
        >
          {saveLabel ?? 'Save'}
        </Button>
        <span className="text-[12px] text-subtle">Lines made from now on sound like this; made lines keep their sound until made again.</span>
      </div>
    </details>
  );
}

/** The account's voices with samples (the same pieces as Settings → Integrations). */
export function VoiceChooser({ current, onPick }: { current?: string; onPick: (v: VoiceView) => void }) {
  const player = usePreview();
  const [q, setQ] = useState('');
  const [search, setSearch] = useState('');
  const id = useId();
  useEffect(() => {
    const t = setTimeout(() => setSearch(q.trim()), 250);
    return () => clearTimeout(t);
  }, [q]);
  const voices = useInfiniteQuery({
    queryKey: ['voiceover.voices', { provider: EL, search, paged: true }],
    queryFn: ({ pageParam, signal }) => call('voiceover.voices', { provider: EL, ...(search ? { search } : {}), ...(pageParam ? { pageToken: pageParam } : {}) }, signal),
    initialPageParam: '',
    getNextPageParam: (last) => last.nextPageToken || undefined,
    staleTime: 60_000,
  });
  const all = voices.data?.pages.flatMap((p) => p.voices) ?? [];
  return (
    <div className="mt-3">
      <div className="relative w-full sm:w-[300px]">
        <label htmlFor={id} className="sr-only">
          Search voices
        </label>
        <MagnifyingGlass size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-subtle" aria-hidden />
        <input
          id={id}
          type="search"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search by name, accent or style"
          className="h-9 w-full rounded-[10px] bg-raised pl-8 pr-3 text-[13px] text-fg hairline placeholder:text-subtle"
        />
      </div>
      <div className="mt-2 overflow-hidden rounded-[12px] hairline">
        {voices.error ? (
          <ErrorPanel compact className="m-3" error={voices.error} />
        ) : voices.isLoading ? (
          <Skeleton className="m-3 h-24" />
        ) : (
          <ul className="max-h-[300px] divide-y divide-line overflow-y-auto" aria-label="Voices to choose from">
            {all.map((v) => (
              <li key={v.id} className={cx('flex items-center gap-3 px-3 py-2', v.id === current && 'bg-accent-soft')}>
                <PlayButton v={v} player={player} />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5 text-[13px] font-medium">
                    {v.name}
                    {v.id === current ? <Badge tone="accent">in use</Badge> : null}
                    {player.cur?.id === v.id && player.cur.state === 'playing' ? <PlayingBars /> : null}
                  </div>
                  <div className="truncate text-[11.5px] text-subtle">{voiceLine(v)}</div>
                </div>
                {v.id !== current ? (
                  <Button size="sm" onClick={() => onPick(v)}>
                    Use this voice
                  </Button>
                ) : null}
              </li>
            ))}
            {all.length === 0 ? <li className="px-3 py-6 text-center text-[12.5px] text-muted">No voices match.</li> : null}
          </ul>
        )}
      </div>
    </div>
  );
}
