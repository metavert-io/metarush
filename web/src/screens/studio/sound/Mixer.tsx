import { useEffect, useId, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import { Headphones, SpeakerHigh, SpeakerSlash } from '@phosphor-icons/react';
import { call } from '../../../api/client';
import type { MixBusChange, ProjectShowOut, RenderPlan, SoundLevelsIn, SoundShowOut, Spec } from '../../../api/types';
import { Button, cx, ErrorPanel, Skeleton, Spinner } from '../../../ui/primitives';
import { checkApplied, errorText, useChange } from '../voiceover/api';
import { Music } from './Music';
import { Voices } from './Voices';

/*
 * The Sound tab's panel, in plain words: how loud the camera sound, each
 * person (over their own speech), the narration, music and effects are, what
 * is off, clearer voices, the music, music getting quieter while someone
 * talks, voices evened out between clips, smooth cuts and the overall level
 * for where the video goes. Level changes are heard at once in the preview
 * (sound.preview: the plan's gain envelopes, nothing saved) and saved as one
 * new version (sound.levels); clearer voices and music changes are their own
 * small saved steps (sound.enhance, sound.music.*). The same ops back
 * `metarush sound …`.
 */

type BusName = 'source' | 'voiceover' | 'music' | 'sfx';

const PARTS: { bus: BusName; label: string; help: string }[] = [
  { bus: 'source', label: 'Camera sound', help: 'The sound recorded with the video: people talking on camera, the room.' },
  { bus: 'voiceover', label: 'Narration', help: 'Your voiceover lines.' },
  { bus: 'music', label: 'Music', help: 'Music tracks under the video.' },
  { bus: 'sfx', label: 'Effects', help: 'Sound effects.' },
];

const TARGETS: { value: string; label: string }[] = [
  { value: 'auto', label: 'Automatic: what this video’s platform expects' },
  { value: '-14', label: 'About right for YouTube, TikTok and Instagram' },
  { value: '-16', label: 'A little quieter, for websites and presentations' },
  { value: '-23', label: 'Quieter, like TV' },
];

export interface MusicDraft {
  gainDb?: number;
  duck?: 'on' | 'off' | 'auto';
  duckDb?: number;
}

export interface Draft {
  buses: Partial<Record<BusName, { gainDb?: number; mute?: boolean; solo?: boolean }>>;
  clips: Record<string, boolean>;
  people: Record<string, number>;
  music: Record<string, MusicDraft>;
  muteSource?: boolean;
  ducking?: boolean;
  depth?: number;
  levelSpeech?: boolean;
  smoothCuts?: boolean;
  target?: string;
}

const EMPTY: Draft = { buses: {}, clips: {}, people: {}, music: {} };

export function isEmpty(d: Draft): boolean {
  return (
    Object.keys(d.buses).length === 0 &&
    Object.keys(d.clips).length === 0 &&
    Object.keys(d.people).length === 0 &&
    Object.keys(d.music).length === 0 &&
    d.muteSource === undefined &&
    d.ducking === undefined &&
    d.depth === undefined &&
    d.levelSpeech === undefined &&
    d.smoothCuts === undefined &&
    d.target === undefined
  );
}

function toInput(revisionId: string, d: Draft): SoundLevelsIn {
  const parts: MixBusChange[] = Object.entries(d.buses).map(([bus, b]) => ({ bus: bus as BusName, ...b }));
  const input: SoundLevelsIn = { revisionId };
  if (parts.length) input.parts = parts;
  const clips = Object.entries(d.clips).map(([clip, muted]) => ({ clip, muted }));
  if (clips.length) input.clips = clips;
  const people = Object.entries(d.people).map(([name, gainDb]) => ({ name, gainDb }));
  if (people.length) input.people = people;
  const music = Object.entries(d.music).map(([track, m]) => ({ track, ...m }));
  if (music.length) input.music = music;
  if (d.muteSource !== undefined) input.muteSource = d.muteSource;
  if (d.ducking !== undefined) input.ducking = d.ducking;
  if (d.depth !== undefined) input.depth = d.depth;
  if (d.levelSpeech !== undefined) input.levelSpeech = d.levelSpeech;
  if (d.smoothCuts !== undefined) input.smoothCuts = d.smoothCuts;
  if (d.target !== undefined) input.loudness = d.target;
  return input;
}

export function levelText(db: number): string {
  if (Math.abs(db) < 0.25) return 'As recorded';
  const amt = `${Math.abs(db).toFixed(Math.abs(db) < 10 ? 1 : 0).replace(/\.0$/, '')} dB`;
  if (db <= -18) return `Much quieter (${amt})`;
  if (db < -6) return `Quieter (${amt})`;
  if (db < 0) return `A little quieter (${amt})`;
  if (db <= 4) return `A little louder (${amt})`;
  return `Louder (${amt})`;
}

export function depthText(d: number): string {
  if (d <= 8) return 'A little';
  if (d <= 15) return 'Clearly';
  return 'A lot';
}

export function Mixer({ revisionId, spec, plan, show, data, onPreview }: {
  revisionId: string;
  spec: Spec;
  plan?: RenderPlan;
  /** sound.show for this version. */
  show?: SoundShowOut;
  data: ProjectShowOut;
  /** The plan to play while trying a change (undefined: back to the saved cut). */
  onPreview: (p: RenderPlan | undefined) => void;
}) {
  const change = useChange();
  const [draft, setDraft] = useState<Draft>(EMPTY);
  // A/B: every voice as recorded in the preview (nothing is saved).
  const [original, setOriginal] = useState(false);
  const [previewing, setPreviewing] = useState(false);
  const [previewMix, setPreviewMix] = useState<{ explanations: string[]; summary: string } | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const seq = useRef(0);
  const dirty = !isEmpty(draft);
  const trying = dirty || original;

  // Leaving the tab plays the saved cut again. (A new version made meanwhile —
  // a line moved — keeps the draft: it is tried again on the new version.)
  useEffect(() => () => onPreview(undefined), []); // eslint-disable-line react-hooks/exhaustive-deps

  // Every change is heard at once: resolve the changed cut (debounced; the
  // newest answer wins).
  useEffect(() => {
    if (!trying) {
      onPreview(undefined);
      setPreviewMix(null);
      setProblem(null);
      return;
    }
    const n = ++seq.current;
    const ctrl = new AbortController();
    const t = setTimeout(() => {
      setPreviewing(true);
      call('sound.preview', { ...toInput(revisionId, draft), ...(original ? { original: true } : {}) }, ctrl.signal)
        .then((out) => {
          if (n !== seq.current) return;
          onPreview(out.plan);
          setPreviewMix({ explanations: out.mix.explanations, summary: out.summary });
          setProblem(null);
        })
        .catch((e: unknown) => {
          if (n !== seq.current || (e instanceof DOMException && e.name === 'AbortError')) return;
          setProblem(errorText(e));
        })
        .finally(() => n === seq.current && setPreviewing(false));
    }, 140);
    return () => {
      clearTimeout(t);
      ctrl.abort();
    };
  }, [draft, trying, original, revisionId]); // eslint-disable-line react-hooks/exhaustive-deps

  const m = show?.mix;
  const eff = m?.effective;
  const saved = m?.mix;
  const busView = (b: BusName) => m?.buses.find((x) => x.name === b);
  /** A part as it plays with the draft: level, muted, played alone. */
  const part = (b: BusName) => {
    const bv = busView(b);
    const d = draft.buses[b];
    return { gainDb: d?.gainDb ?? bv?.gainDb ?? 0, mute: d?.mute ?? !!bv?.mute, solo: d?.solo ?? !!bv?.solo };
  };
  const setBus = (b: BusName, patch: { gainDb?: number; mute?: boolean; solo?: boolean }) =>
    setDraft((d) => ({ ...d, buses: { ...d.buses, [b]: { ...d.buses[b], ...patch } } }));

  // Only the parts this cut has.
  const present = useMemo(() => {
    const has = new Set<BusName>();
    if (plan?.clips.some((c) => c.hasAudio)) has.add('source');
    if (spec.voiceover?.beats.some((b) => b.take) || spec.audio?.some((a) => a.role === 'voiceover')) has.add('voiceover');
    if (spec.audio?.some((a) => a.role === 'music')) has.add('music');
    if (spec.audio?.some((a) => a.role === 'sfx')) has.add('sfx');
    return has;
  }, [plan, spec]);

  const save = async () => {
    setSaving(true);
    const out = await change(
      async () => checkApplied((await call('sound.levels', { ...toInput(revisionId, draft), apply: true })).applied),
      'Sound saved in a new version of the cut',
      'The sound was not saved',
    );
    setSaving(false);
    if (out) {
      setDraft(EMPTY);
      if (!original) onPreview(undefined);
    }
  };

  if (!show || !m || !eff) return <Skeleton className="h-[420px]" />;

  const muteSource = draft.muteSource ?? !!saved?.muteSource;
  const ducking = draft.ducking ?? eff.ducking.auto;
  const depth = draft.depth ?? Math.abs(eff.ducking.musicUnderVoiceoverDb);
  const levelSpeech = draft.levelSpeech ?? !!eff.levelSpeech;
  const smooth = draft.smoothCuts ?? (saved?.cutBlend ?? 0) > 0;
  const target = draft.target ?? (saved?.loudness?.targetLufs !== undefined ? String(saved.loudness.targetLufs) : 'auto');
  const anySolo = PARTS.some((p) => present.has(p.bus) && part(p.bus).solo);
  const explanations = previewMix?.explanations ?? show.explanations;
  const people = show.people.filter((p) => p.seconds > 0);
  const sourceOff = part('source').mute || muteSource || (anySolo && !part('source').solo);

  return (
    <div className="space-y-5">
      {dirty ? (
        <div className="sticky top-[calc(var(--mr-header-h)+47px)] z-10 flex flex-wrap items-center gap-2 rounded-[12px] bg-accent-soft px-3 py-2.5 shadow-[var(--shadow-soft)] backdrop-blur" role="status">
          <span className="flex min-w-0 flex-1 items-center gap-2 text-[12.5px] text-fg">
            {previewing ? <Spinner size={12} /> : <Headphones size={15} className="shrink-0 text-accent-text" />}
            You are hearing your changes in the preview. They are not saved yet.
          </span>
          <Button size="sm" variant="ghost" onClick={() => setDraft(EMPTY)}>
            Undo changes
          </Button>
          <Button size="sm" variant="primary" loading={saving} onClick={() => void save()}>
            Save sound
          </Button>
        </div>
      ) : null}
      {problem ? <ErrorPanel compact error={new Error(problem)} /> : null}

      <section aria-labelledby="mix-levels-h">
        <h3 id="mix-levels-h" className="text-[13.5px] font-semibold">
          How loud each part is
        </h3>
        <div className="mt-2 space-y-1.5">
          {PARTS.filter((p) => present.has(p.bus)).map((p) => {
            const pt = part(p.bus);
            const gain = pt.gainDb;
            const mute = pt.mute || (p.bus === 'source' && muteSource);
            const solo = pt.solo;
            const silenced = mute || (anySolo && !solo);
            return (
              <div key={p.bus} className="space-y-1.5">
                <LevelRow
                  label={p.label}
                  help={p.help}
                  gain={gain}
                  mute={mute}
                  solo={solo}
                  silenced={silenced}
                  onGain={(g) => setBus(p.bus, { gainDb: g })}
                  onMute={(v) => (p.bus === 'source' && muteSource && !v ? setDraft((d) => ({ ...d, muteSource: false, buses: { ...d.buses, source: { ...d.buses.source, mute: false } } })) : setBus(p.bus, { mute: v }))}
                  onSolo={(v) => setBus(p.bus, { solo: v })}
                />
                {p.bus === 'source' && people.length > 0 ? (
                  <ul className="ml-4 space-y-1.5 border-l border-line pl-3" aria-label="Each person, over their own speech">
                    {people.map((pp) => {
                      const g = draft.people[pp.name] ?? pp.gainDb;
                      return (
                        <li key={pp.name}>
                          <PersonRow
                            name={pp.name}
                            gain={g}
                            seconds={pp.seconds}
                            silenced={sourceOff}
                            onGain={(v) => setDraft((d) => ({ ...d, people: { ...d.people, [pp.name]: Math.round(v * 10) / 10 } }))}
                          />
                        </li>
                      );
                    })}
                  </ul>
                ) : null}
              </div>
            );
          })}
          {present.size === 0 ? <p className="text-[12.5px] text-subtle">This cut has no sound yet.</p> : null}
        </div>
      </section>

      <Voices revisionId={revisionId} show={show} original={original} onOriginal={setOriginal} />

      <Music revisionId={revisionId} show={show} data={data} spec={spec} draft={draft.music} onDraft={(id, patch) => setDraft((d) => ({ ...d, music: { ...d.music, [id]: { ...d.music[id], ...patch } } }))} />

      {present.has('source') ? (
        <section aria-labelledby="mix-camera-h" className="rounded-[14px] p-3.5 hairline">
          <h3 id="mix-camera-h" className="text-[13.5px] font-semibold">
            Camera sound
          </h3>
          <Toggle
            checked={muteSource}
            onChange={(v) => setDraft((d) => ({ ...d, muteSource: v }))}
            label="Turn off the camera sound for the whole video"
            help="Only the narration, music and effects are heard."
          />
          {!muteSource ? (
            <details className="mt-2 group">
              <summary className="cursor-pointer text-[12.5px] font-medium text-accent-text">Turn it off for single clips</summary>
              <ul className="mt-2 space-y-1">
                {spec.clips.map((c, i) => {
                  const off = draft.clips[c.id] ?? !!c.muted;
                  const pc = plan?.clips.find((x) => x.id === c.id);
                  if (pc && !pc.hasAudio) return null;
                  return (
                    <li key={c.id}>
                      <Toggle
                        small
                        checked={off}
                        onChange={(v) => setDraft((d) => ({ ...d, clips: { ...d.clips, [c.id]: v } }))}
                        label={`${i + 1}. ${c.label || `Shot ${i + 1}`}: no camera sound`}
                      />
                    </li>
                  );
                })}
              </ul>
            </details>
          ) : null}
        </section>
      ) : null}

      <section aria-labelledby="mix-auto-h" className="space-y-3 rounded-[14px] p-3.5 hairline">
        <h3 id="mix-auto-h" className="text-[13.5px] font-semibold">
          Automatic help
        </h3>
        <div>
          <Toggle
            checked={ducking}
            onChange={(v) => setDraft((d) => ({ ...d, ducking: v }))}
            label="Music gets quieter when someone talks"
            help="While the narration or someone on camera speaks, the music steps back so words are easy to hear."
          />
          {ducking ? (
            <MiniSlider
              label="How much quieter"
              min={4}
              max={24}
              step={1}
              value={depth}
              text={depthText(depth)}
              onChange={(v) => setDraft((d) => ({ ...d, depth: v }))}
              left="A little"
              right="A lot"
            />
          ) : null}
        </div>
        <Toggle
          checked={levelSpeech}
          onChange={(v) => setDraft((d) => ({ ...d, levelSpeech: v }))}
          label="Even out voices"
          help="People recorded on different cameras or microphones sound about equally loud."
        />
        <Toggle
          checked={smooth}
          onChange={(v) => setDraft((d) => ({ ...d, smoothCuts: v }))}
          label="Smooth cuts"
          help="The sound blends for a moment at each cut, so it never clicks."
        />
      </section>

      <section aria-labelledby="mix-loud-h" className="rounded-[14px] p-3.5 hairline">
        <h3 id="mix-loud-h" className="text-[13.5px] font-semibold">
          Overall loudness
        </h3>
        <LoudnessSelect value={target} onChange={(v) => setDraft((d) => ({ ...d, target: v }))} />
      </section>

      <section aria-labelledby="mix-what-h">
        <h3 id="mix-what-h" className="text-[13.5px] font-semibold">
          What you hear{dirty ? ' with your changes' : original ? ' with every voice as recorded' : ''}
        </h3>
        {explanations.length ? (
          <ul className="mt-2 space-y-1.5">
            {explanations.map((e) => (
              <li key={e} className="flex gap-2 text-[12.5px] leading-relaxed text-muted">
                <span aria-hidden className="mt-[7px] h-1 w-1 shrink-0 rounded-full bg-subtle" />
                {e}
              </li>
            ))}
          </ul>
        ) : (
          <p className="mt-1 text-[12.5px] text-subtle">Everything plays as it was recorded.</p>
        )}
      </section>
    </div>
  );
}

/** A person's level over their own speech (their voice in every clip, nobody else's). */
function PersonRow({ name, gain, seconds, silenced, onGain }: { name: string; gain: number; seconds: number; silenced: boolean; onGain: (g: number) => void }) {
  const id = useId();
  const min = -30;
  const max = 12;
  const pct = ((Math.max(min, gain) - min) / (max - min)) * 100;
  return (
    <div className={cx('grid grid-cols-[minmax(0,1fr)] items-center gap-x-3 gap-y-1 rounded-[12px] px-3 py-2 hairline sm:grid-cols-[118px_minmax(0,1fr)]', silenced && 'opacity-60')} data-person={name}>
      <div className="min-w-0">
        <label htmlFor={id} className="block truncate text-[13px] font-medium" title={name}>
          {name}
        </label>
        <div className="truncate text-[11.5px] text-subtle">{levelText(gain)}</div>
      </div>
      <input
        id={id}
        type="range"
        min={min}
        max={max}
        step={0.5}
        value={Math.max(min, gain)}
        aria-valuetext={`${levelText(gain)} over ${name}'s speech`}
        aria-describedby={`${id}-help`}
        onChange={(e) => onGain(Number(e.target.value))}
        onDoubleClick={() => onGain(0)}
        style={{ '--pct': `${pct}%` } as CSSProperties}
        className={rangeClass}
      />
      <span id={`${id}-help`} className="sr-only">
        Only while {name} speaks ({Math.round(seconds)} seconds in this cut); everyone else stays as they are.
      </span>
    </div>
  );
}

export const rangeClass = cx(
  'block h-5 w-full cursor-pointer appearance-none bg-transparent',
  '[&::-webkit-slider-runnable-track]:h-1.5 [&::-webkit-slider-runnable-track]:rounded-full [&::-webkit-slider-runnable-track]:bg-[linear-gradient(to_right,var(--accent)_var(--pct),var(--line-strong)_var(--pct))]',
  '[&::-moz-range-track]:h-1.5 [&::-moz-range-track]:rounded-full [&::-moz-range-track]:bg-[linear-gradient(to_right,var(--accent)_var(--pct),var(--line-strong)_var(--pct))]',
  '[&::-webkit-slider-thumb]:-mt-[5px] [&::-webkit-slider-thumb]:h-4 [&::-webkit-slider-thumb]:w-4 [&::-webkit-slider-thumb]:appearance-none [&::-webkit-slider-thumb]:rounded-full [&::-webkit-slider-thumb]:border-2 [&::-webkit-slider-thumb]:border-[var(--bg-panel)] [&::-webkit-slider-thumb]:bg-[var(--accent)]',
  '[&::-moz-range-thumb]:h-4 [&::-moz-range-thumb]:w-4 [&::-moz-range-thumb]:rounded-full [&::-moz-range-thumb]:border-2 [&::-moz-range-thumb]:border-[var(--bg-panel)] [&::-moz-range-thumb]:bg-[var(--accent)]',
);

function LevelRow({ label, help, gain, mute, solo, silenced, onGain, onMute, onSolo }: {
  label: string;
  help: string;
  gain: number;
  mute: boolean;
  solo: boolean;
  silenced: boolean;
  onGain: (g: number) => void;
  onMute: (v: boolean) => void;
  onSolo: (v: boolean) => void;
}) {
  const id = useId();
  const min = -30;
  const max = 12;
  const pct = ((Math.max(min, gain) - min) / (max - min)) * 100;
  return (
    <div className={cx('grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 rounded-[12px] px-3 py-2 hairline sm:grid-cols-[130px_minmax(0,1fr)_auto]', silenced && 'opacity-60')}>
      <div className="min-w-0">
        <label htmlFor={id} className="text-[13px] font-medium">
          {label}
        </label>
        <div className="truncate text-[11.5px] text-subtle" title={help}>
          {mute ? 'Off' : silenced ? 'Silent while another part plays alone' : levelText(gain)}
        </div>
      </div>
      <input
        id={id}
        type="range"
        min={min}
        max={max}
        step={0.5}
        value={Math.max(min, gain)}
        aria-valuetext={mute ? 'off' : levelText(gain)}
        aria-describedby={`${id}-help`}
        onChange={(e) => onGain(Number(e.target.value))}
        onDoubleClick={() => onGain(0)}
        style={{ '--pct': `${pct}%` } as CSSProperties}
        className={cx('order-3 col-span-2 sm:order-none sm:col-span-1', rangeClass)}
      />
      <span id={`${id}-help`} className="sr-only">
        {help}
      </span>
      <div className="flex items-center gap-1">
        <button
          type="button"
          aria-pressed={mute}
          aria-label={mute ? `Turn ${label.toLowerCase()} back on` : `Mute ${label.toLowerCase()}`}
          title={mute ? 'Turn back on' : 'Mute'}
          onClick={() => onMute(!mute)}
          className={cx('grid h-8 w-8 place-items-center rounded-[9px] transition-colors', mute ? 'bg-danger-soft text-danger' : 'text-muted hover:bg-hover hover:text-fg')}
        >
          {mute ? <SpeakerSlash size={15} /> : <SpeakerHigh size={15} />}
        </button>
        <button
          type="button"
          aria-pressed={solo}
          aria-label={solo ? `Hear everything again` : `Hear only ${label.toLowerCase()}`}
          title={solo ? 'Hear everything again' : 'Hear only this'}
          onClick={() => onSolo(!solo)}
          className={cx('h-8 rounded-[9px] px-2 text-[11.5px] font-semibold transition-colors', solo ? 'bg-accent text-accent-ink' : 'text-muted hover:bg-hover hover:text-fg')}
        >
          Only this
        </button>
      </div>
    </div>
  );
}

export function Toggle({ checked, onChange, label, help, small, disabled }: { checked: boolean; onChange: (v: boolean) => void; label: string; help?: ReactNode; small?: boolean; disabled?: boolean }) {
  const id = useId();
  return (
    <div className={cx('flex items-start gap-3', small ? 'py-0.5' : '', disabled && 'opacity-60')}>
      <button
        id={id}
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={cx(
          'relative mt-[2px] h-5 w-9 shrink-0 rounded-full transition-colors duration-200 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent',
          checked ? 'bg-accent' : 'bg-line-strong',
        )}
      >
        <span
          aria-hidden
          className={cx(
            'absolute left-0 top-0.5 h-4 w-4 rounded-full bg-white shadow-[0_1px_2px_rgb(0_0_0/0.35)] transition-transform duration-200 ease-[var(--ease-out-expo)]',
            checked ? 'translate-x-[18px]' : 'translate-x-0.5',
          )}
        />
      </button>
      <label htmlFor={id} className="min-w-0 cursor-pointer">
        <span className={cx('block', small ? 'text-[12.5px]' : 'text-[13px] font-medium')}>{label}</span>
        {help ? <span className="block text-[12px] leading-relaxed text-subtle">{help}</span> : null}
      </label>
    </div>
  );
}

export function MiniSlider({ label, min, max, step, value, text, onChange, left, right, className, onCommit }: {
  label: string;
  min: number;
  max: number;
  step: number;
  value: number;
  text: string;
  onChange: (v: number) => void;
  left: string;
  right: string;
  className?: string;
  /** Called when the person lets go (pointer up, or a key). */
  onCommit?: () => void;
}) {
  const id = useId();
  const pct = ((value - min) / (max - min)) * 100;
  return (
    <div className={className ?? 'ml-12 mt-2 max-w-[360px]'}>
      <div className="flex items-baseline justify-between">
        <label htmlFor={id} className="text-[12px] text-muted">
          {label}
        </label>
        <span className="text-[12px] text-fg">{text}</span>
      </div>
      <input
        id={id}
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        aria-valuetext={text}
        onChange={(e) => onChange(Number(e.target.value))}
        onPointerUp={onCommit}
        onKeyUp={onCommit}
        style={{ '--pct': `${pct}%` } as CSSProperties}
        className={cx('mt-1', rangeClass)}
      />
      <div className="flex justify-between text-[11px] text-subtle" aria-hidden>
        <span>{left}</span>
        <span>{right}</span>
      </div>
    </div>
  );
}

function LoudnessSelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const id = useId();
  const options = TARGETS.some((t) => t.value === value) ? TARGETS : [...TARGETS, { value, label: 'A level set for this cut' }];
  return (
    <div className="mt-2">
      <label htmlFor={id} className="text-[12.5px] text-muted">
        How loud the finished video is overall
      </label>
      <select
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="mt-1.5 h-9 w-full rounded-[10px] bg-panel px-3 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent focus:outline-offset-0"
      >
        {options.map((t) => (
          <option key={t.value} value={t.value}>
            {t.label}
          </option>
        ))}
      </select>
    </div>
  );
}
