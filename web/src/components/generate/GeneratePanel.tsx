import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowRight, Copy, FilmSlate, Key, Sparkle, StopCircle, X } from '@phosphor-icons/react';
import { call, media } from '../../api/client';
import { useOp } from '../../api/queries';
import type { GenEstimate, GenShotIn, GenView, ProjectShowOut } from '../../api/types';
import { useToast } from '../../ui/Toast';
import { useEstimatesFirst } from '../../lib/preferences';
import { Badge, Button, cx, ErrorPanel, Segmented, Skeleton, Spinner, Thumb, type Tone } from '../../ui/primitives';
import { money } from './ai';
import { estimateWords } from '../../lib/costs';
import { EstimateLines } from './EstimateLines';
import { GENERATE_QUERIES, useGenerateRuns, useStopGeneration } from './RunDialog';
import { frameThumb, StartPicker, type Frame } from './StartPicker';
import { AI_KINDS, AIWork, type AIKind } from './AIWork';
import { PresenterWork } from './Presenters';

/*
 * Generate a shot (D123–D127): a prompt, who appears (a look), the length,
 * the shape and the quality — or start from a frame of real footage
 * ("continue this shot"), a picture (a photo from this computer or from
 * Resources, D154), or go from one frame to another. The price comes first
 * and the button carries it; nothing is sent until it is pressed. With the
 * person's estimates off (D208) the button doesn't wait for the price: the
 * shot goes ahead within the project's budget and today's limit. Results
 * land here and in Sources as new clips, marked as AI-generated.
 */

export type { Frame };

const frameRef = (f: Frame) => (f.picture ? { picture: f.picture } : { assetId: f.assetId!, at: f.at ?? 0 });

const clockOf = (t: number) => `${Math.floor(t / 60)}:${(t % 60).toFixed(1).padStart(4, '0')}`;

const stateWords: Record<GenView['state'], { label: string; tone: Tone }> = {
  waiting: { label: 'Waiting to start', tone: 'neutral' },
  sending: { label: 'Sending', tone: 'accent' },
  making: { label: 'Being made', tone: 'accent' },
  done: { label: 'Ready', tone: 'ok' },
  failed: { label: "Couldn't be made", tone: 'danger' },
  refused: { label: 'Declined', tone: 'danger' },
  canceled: { label: 'Stopped', tone: 'neutral' },
};

export function GeneratePanel({ data, start: given, end, onClearFrames, onOpenClip, onClose }: {
  data: ProjectShowOut;
  start?: Frame;
  end?: Frame;
  onClearFrames: () => void;
  onOpenClip: (assetId: string) => void;
  onClose: () => void;
}) {
  const uid = useId();
  const toast = useToast();
  const qc = useQueryClient();
  const projectId = data.project.id;
  const estimatesFirst = useEstimatesFirst();
  const settings = useOp('generate.settings', { projectId });
  const looks = useOp('looks.list', {});
  const s = settings.data;
  const model = s?.models.find((m) => m.default) ?? s?.models[0];
  const [prompt, setPrompt] = useState('');
  const [look, setLook] = useState('');
  const [seconds, setSeconds] = useState(6);
  const [aspect, setAspect] = useState<'16:9' | '9:16'>('16:9');
  const [quality, setQuality] = useState<'draft' | 'final'>('draft');
  const [resolution, setResolution] = useState('');
  const [takes, setTakes] = useState('1');
  const [noPeople, setNoPeople] = useState(false);
  // A starting picture or clip frame chosen in the panel (the clip view's "Continue this shot" passes its own).
  const [chosenStart, setChosenStart] = useState<Frame | undefined>();
  const start = given ?? chosenStart;
  // What to make: a shot (generated video), or another kind of AI work (D226).
  const [kind, setKind] = useState<'video' | AIKind>('video');
  const [est, setEst] = useState<GenEstimate | null>(null);
  const [estError, setEstError] = useState<unknown>(null);
  const [pricing, setPricing] = useState(false);
  const [sending, setSending] = useState(false);
  const usableLooks = (looks.data?.looks ?? []).filter((l) => l.usable);
  const res = resolution || (quality === 'final' ? (s?.finalResolution ?? '1080p') : (s?.draftResolution ?? '720p'));
  const resChoices = quality === 'final' ? ['1080p', '4k'] : ['360p', '720p'];

  // The usual length and shape fill in once, unless the person already chose.
  const chosen = useRef(false);
  useEffect(() => {
    if (!s || chosen.current) return;
    chosen.current = true;
    if (s.seconds) setSeconds(s.seconds);
    if (s.aspectRatio === '9:16') setAspect('9:16');
  }, [s]);

  const input = useMemo<GenShotIn>(
    () => ({
      projectId,
      prompt: prompt.trim(),
      seconds,
      aspectRatio: aspect,
      quality,
      resolution: res as GenShotIn['resolution'],
      takes: Number(takes),
      ...(look && !start ? { look } : {}),
      ...(start ? { from: frameRef(start) } : {}),
      ...(start && end ? { to: frameRef(end) } : {}),
      ...(noPeople ? { noPeople: true } : {}),
    }),
    [projectId, prompt, seconds, aspect, quality, res, takes, look, start, end, noPeople],
  );

  // The price shown is for exactly what would be sent: a change (a picture
  // added, a box ticked) prices it again before Generate can be pressed.
  const inputKey = useMemo(() => JSON.stringify(input), [input]);
  const [estKey, setEstKey] = useState('');
  const latest = useRef('');
  useEffect(() => {
    latest.current = inputKey;
    if (!input.prompt) {
      setEst(null);
      setEstKey('');
      setEstError(null);
      setPricing(false);
      return;
    }
    const t = setTimeout(async () => {
      setPricing(true);
      try {
        const e = await call('generate.estimate', input);
        if (latest.current !== inputKey) return; // a newer change is being priced
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
  }, [input, inputKey]);
  const priced = !!est && estKey === inputKey;

  // Estimates off: no waiting for the price (one the server refuses still says why).
  const ready = estimatesFirst ? priced && !!est?.allowed && !pricing : !!input.prompt && !(priced && est && !est.allowed);
  const go = async () => {
    if (!ready) return;
    setSending(true);
    try {
      const out = await call('generate.shot', { ...input, ...(priced && est ? { maxUsd: est.usd } : {}) });
      GENERATE_QUERIES.forEach((q) => void qc.invalidateQueries({ queryKey: [q] }));
      toast({ tone: 'success', title: 'Asked for the shot', body: out.summary });
      setPrompt('');
    } catch (e) {
      setEstError(e);
    } finally {
      setSending(false);
    }
  };

  const needsNoPeople = !!start || (est?.problems ?? []).some((p) => p.includes("can't tell"));

  return (
    <section className="panel overflow-hidden" aria-labelledby={`${uid}-h`} data-generate-panel>
      <div className="flex items-start gap-3 border-b border-line px-5 py-4">
        <div className="grid h-9 w-9 shrink-0 place-items-center rounded-[11px] bg-info-soft text-info" aria-hidden>
          <Sparkle size={18} weight="fill" />
        </div>
        <div className="min-w-0 flex-1">
          <h2 id={`${uid}-h`} className="text-[16px] font-semibold tracking-[-0.01em]">
            {kind === 'video' ? 'Generate a shot' : 'Generate'}
          </h2>
          <p className="mt-0.5 max-w-[70ch] text-[12.5px] leading-relaxed text-muted">
            {kind === 'video'
              ? `Made with ${s?.providerTitle ?? 'Google Gemini'}. You see the price first; new shots land below and in Sources, marked as AI-generated.`
              : 'With the AI services your keys cover. You see the price, the model and other choices first; what comes back is marked as AI-generated.'}
          </p>
        </div>
        <button type="button" onClick={onClose} aria-label="Close Generate a shot" className="grid h-8 w-8 place-items-center rounded-[8px] text-subtle hover:bg-hover hover:text-fg">
          <X size={15} />
        </button>
      </div>

      <div className="overflow-x-auto px-5 pt-3" data-generate-kinds>
        <Segmented
          label="What to make"
          value={kind}
          onChange={setKind}
          items={[{ value: 'video' as const, label: 'Video' }, ...AI_KINDS.map((k) => ({ value: k.value, label: k.label }))]}
        />
      </div>

      {kind === 'presenter' ? (
        <PresenterWork data={data} onOpenClip={onOpenClip} />
      ) : kind !== 'video' ? (
        <AIWork kind={kind} data={data} onOpenClip={onOpenClip} />
      ) : null}

      {kind === 'video' && s && !s.keyReady ? (
        <div className="mx-5 mt-4 flex flex-wrap items-center gap-2 rounded-[12px] bg-accent-soft px-3.5 py-3 text-[13px] text-accent-text" role="status">
          <Key size={15} className="shrink-0" />
          <span className="min-w-0 flex-1">Add your Google Gemini key on this computer first. Each computer keeps its own key.</span>
          <Link to="/settings/integrations" className="inline-flex items-center gap-1 font-medium hover:underline">
            Settings → Integrations <ArrowRight size={12} />
          </Link>
        </div>
      ) : null}

      {kind === 'video' ? (
        <>
      <div className="grid grid-cols-1 gap-5 px-5 py-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(300px,1fr)]">
        <div className="space-y-4">
          <div>
            <label htmlFor={`${uid}-p`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
              What the shot shows
            </label>
            <textarea
              id={`${uid}-p`}
              rows={3}
              maxLength={4000}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              placeholder="A red paper boat drifting on a still pond at dawn, mist on the water"
              className="w-full resize-y rounded-[10px] bg-panel px-3 py-2 text-[14px] leading-relaxed text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
              data-generate-prompt
            />
          </div>

          {start ? (
            <div className="flex flex-wrap items-center gap-3 rounded-[12px] px-3 py-2.5 hairline" data-generate-frames>
              {[start, end].filter(Boolean).map((f, i) => (
                <figure key={i} className="flex items-center gap-2">
                  <Thumb src={frameThumb(f!)} alt="" className="h-[45px] w-[80px] rounded-[7px]" fit={f!.picture ? 'contain' : 'cover'} />
                  <figcaption className="text-[12.5px]">
                    <span className="block font-medium text-fg">{i === 0 ? (end || f!.picture || !given ? 'Starts on' : 'Continues from') : 'Ends on'}</span>
                    <span className="block text-muted">
                      {f!.title}
                      {f!.picture ? '' : ` at ${clockOf(f!.at ?? 0)}`}
                    </span>
                  </figcaption>
                </figure>
              ))}
              {!given && start.assetId && start.duration ? (
                <label className="flex min-w-[180px] flex-1 items-center gap-2 text-[12px] text-muted">
                  Frame
                  <input
                    type="range"
                    min={0}
                    max={Math.max(0, start.duration - 0.1)}
                    step={0.1}
                    value={start.at ?? 0}
                    onChange={(e) => setChosenStart({ ...start, at: Number(e.target.value) })}
                    className="flex-1 accent-[var(--accent)]"
                    aria-label="Which frame of the clip the shot starts on"
                  />
                </label>
              ) : null}
              <Button
                size="sm"
                variant="ghost"
                className="ml-auto"
                onClick={() => {
                  setChosenStart(undefined);
                  onClearFrames();
                }}
              >
                Start from words instead
              </Button>
            </div>
          ) : (
            <div>
              <label htmlFor={`${uid}-l`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
                Who appears
              </label>
              <div className="flex flex-wrap items-center gap-2">
                <select
                  id={`${uid}-l`}
                  value={look}
                  onChange={(e) => setLook(e.target.value)}
                  className="h-9 min-w-[220px] rounded-[10px] bg-panel px-2.5 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent"
                >
                  <option value="">Nobody in particular</option>
                  {usableLooks.map((l) => (
                    <option key={l.id} value={l.name}>
                      {l.title}
                      {l.subject === 'character' ? ' (a character)' : ''}
                    </option>
                  ))}
                </select>
                <Link to="/settings/integrations#looks" className="text-[12.5px] font-medium text-accent-text hover:underline">
                  Looks and consent
                </Link>
              </div>
              <p className="mt-1.5 text-[12px] text-subtle">To continue a shot from your footage, open a clip and choose Continue this shot.</p>
              <div className="mt-4">
                <StartPicker
                  projectId={projectId}
                  projectName={data.project.name}
                  clips={data.assets}
                  onPick={(f) => {
                    setLook('');
                    setChosenStart(f);
                  }}
                />
              </div>
            </div>
          )}

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div>
              <label htmlFor={`${uid}-s`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
                Length: {seconds} seconds
              </label>
              <input
                id={`${uid}-s`}
                type="range"
                min={model?.minSeconds ?? 3}
                max={model?.maxSeconds ?? 10}
                step={1}
                value={seconds}
                onChange={(e) => {
                  chosen.current = true;
                  setSeconds(Number(e.target.value));
                }}
                className="w-full accent-[var(--accent)]"
              />
            </div>
            <div>
              <div className="mb-1.5 text-[12.5px] font-medium text-muted">Shape</div>
              <Segmented
                label="Shape"
                value={aspect}
                onChange={(v) => {
                  chosen.current = true;
                  setAspect(v);
                }}
                items={[
                  { value: '16:9', label: 'Wide' },
                  { value: '9:16', label: 'Tall' },
                ]}
              />
            </div>
            <div>
              <div className="mb-1.5 text-[12.5px] font-medium text-muted">Quality</div>
              <Segmented
                label="Quality"
                value={quality}
                onChange={(v) => {
                  setQuality(v);
                  setResolution('');
                }}
                items={[
                  { value: 'draft', label: 'Draft' },
                  { value: 'final', label: 'Final' },
                ]}
              />
              <div className="mt-2">
                <Segmented
                  label="Resolution"
                  value={res}
                  onChange={setResolution}
                  items={resChoices.map((r) => ({
                    value: r,
                    label: `${r === '4k' ? '4K' : r}${model?.perSecondUsd?.[r] !== undefined ? `, ${money(model.perSecondUsd[r])} a second` : ''}`,
                  }))}
                />
              </div>
            </div>
            <div>
              <div className="mb-1.5 text-[12.5px] font-medium text-muted">Takes</div>
              <Segmented label="Takes" value={takes} onChange={setTakes} items={['1', '2', '3', '4'].map((v) => ({ value: v, label: v }))} />
              <p className="mt-1.5 text-[12px] text-subtle">Every take is different. Pick the one you like.</p>
            </div>
          </div>

          {needsNoPeople ? (
            <label className="flex items-start gap-2 text-[13px] text-fg">
              <input type="checkbox" checked={noPeople} onChange={(e) => setNoPeople(e.target.checked)} className="mt-[3px]" data-no-people />
              <span>
                {start?.picture ? 'This picture shows no real people' : 'These frames show no real people'}
                <span className="block text-[12px] text-muted">A shot that shows someone needs their look and their consent.</span>
              </span>
            </label>
          ) : null}
        </div>

        <div className="flex flex-col gap-3">
          <div className="min-h-[120px] rounded-[14px] bg-hover/60 px-4 py-3.5" aria-live="polite">
            {!prompt.trim() ? (
              <p className="text-[13px] text-subtle">Describe the shot to see what it costs.</p>
            ) : pricing && !est ? (
              <span className="inline-flex items-center gap-2 text-[13px] text-muted">
                <Spinner size={12} /> Pricing it…
              </span>
            ) : est ? (
              <EstimateLines estimate={est} />
            ) : null}
            {estError ? <ErrorPanel compact error={estError} className="mt-2" /> : null}
          </div>
          <Button variant="primary" icon={<Sparkle size={14} weight="fill" />} disabled={!ready} loading={sending} onClick={() => void go()} data-generate-go data-estimates-first={estimatesFirst ? 'on' : 'off'}>
            {est && priced ? `Generate (${estimateWords(est.usd)})` : 'Generate'}
          </Button>
          <p className="text-[12px] leading-relaxed text-subtle">Nothing is sent until you press it. Google adds an invisible watermark (SynthID) to everything it makes.</p>
        </div>
      </div>

      <Takes projectId={projectId} onOpenClip={onOpenClip} />
        </>
      ) : null}
    </section>
  );
}

/** The project's generated takes, newest first, with what to do next. */
function Takes({ projectId, onOpenClip }: { projectId: string; onOpenClip: (assetId: string) => void }) {
  const [live, setLive] = useState(false);
  const list = useOp('generate.list', { projectId, limit: 12 }, { refetchInterval: live ? 2500 : false });
  const gens = list.data?.generations ?? [];
  useEffect(() => setLive(gens.some((g) => g.state === 'waiting' || g.state === 'sending' || g.state === 'making')), [gens]);
  const run = useGenerateRuns();
  const stop = useStopGeneration();
  if (list.isLoading) return <Skeleton className="mx-5 mb-5 h-24" />;
  if (gens.length === 0) return null;
  return (
    <div className="border-t border-line px-5 py-4">
      <div className="mb-3 flex items-center gap-2">
        <h3 className="text-[13.5px] font-semibold">Generated for this project</h3>
        <span className="text-[12px] text-subtle">{list.data?.summary}</span>
      </div>
      <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3" aria-label="Generated shots" data-generated-takes>
        {gens.map((g) => {
          const st = stateWords[g.state];
          const clip = g.clips[0];
          const busy = g.state === 'waiting' || g.state === 'sending' || g.state === 'making';
          return (
            <li key={g.id} className="flex gap-3 rounded-[12px] p-2.5 hairline" data-generation={g.state}>
              <button
                type="button"
                className="relative h-[54px] w-[96px] shrink-0 overflow-hidden rounded-[8px] bg-inset"
                disabled={!clip}
                onClick={() => clip && onOpenClip(clip.assetId)}
                aria-label={clip ? `Open ${clip.title}` : 'Not ready yet'}
              >
                {clip ? (
                  <Thumb src={clip.hasThumb ? media.asset(clip.assetId, 'thumbnail') : media.frame(clip.assetId, 0.5, 240)} alt="" className="h-full w-full" />
                ) : (
                  <span className="grid h-full w-full place-items-center text-subtle">{busy ? <Spinner size={14} /> : <FilmSlate size={16} />}</span>
                )}
              </button>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-1.5">
                  <Badge tone={st.tone}>{st.label}</Badge>
                  <span className="text-[11.5px] text-subtle">
                    {money(g.costUsd)}
                    {g.take > 1 ? `, take ${g.take}` : ''}
                  </span>
                </div>
                <p className="mt-1 line-clamp-2 text-[12.5px] leading-snug text-fg" title={g.prompt}>
                  {g.prompt}
                </p>
                {g.state === 'failed' || g.state === 'refused' ? <p className="mt-1 text-[12px] text-danger">{g.outcome?.message ?? g.summary}</p> : null}
                <div className={cx('mt-1.5 flex flex-wrap gap-1')}>
                  {busy ? (
                    <Button size="sm" variant="ghost" icon={<StopCircle size={13} />} onClick={() => void stop(g.id)}>
                      Stop
                    </Button>
                  ) : g.operation !== 'look-test' ? (
                    <>
                      <Button size="sm" variant="ghost" icon={<Copy size={13} />} onClick={() => void run('again', g.id)}>
                        Again
                      </Button>
                      <Button size="sm" variant="ghost" onClick={() => void run('variations', g.id)}>
                        Variations
                      </Button>
                      {clip ? (
                        <>
                          <Button size="sm" variant="ghost" onClick={() => void run('extend', g.id)}>
                            Extend
                          </Button>
                          <Button size="sm" variant="ghost" onClick={() => void run('edit', g.id)}>
                            Edit
                          </Button>
                        </>
                      ) : null}
                    </>
                  ) : null}
                </div>
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
