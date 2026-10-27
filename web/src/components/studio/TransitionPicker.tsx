import { lazy, Suspense, useEffect, useMemo, useState } from 'react';
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, Sparkle, WarningCircle } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { Clip, CutProblem, EditOp, RenderPlan, Spec } from '../../api/types';
import { Badge, Button, cx, Spinner } from '../../ui/primitives';
import { sayDuration } from './TimelineEditor';
import type { TimelineEditor } from './useTimelineEditor';

const LoopPreview = lazy(() => import('../../player/LoopPreview'));

type SoundMode = 'cut' | 'lead' | 'carry' | 'auto';

interface Choice {
  type: string;
  duration: number;
  direction?: string;
  easing?: string;
  sound: SoundMode;
  soundSeconds: number;
}

const SOUND: { mode: SoundMode; label: string; hint: string }[] = [
  { mode: 'cut', label: 'Change the sound with the picture', hint: 'The usual straight change.' },
  { mode: 'lead', label: 'Hear the next clip a moment early', hint: 'Its sound starts before we see it (an editor calls this a J-cut).' },
  { mode: 'carry', label: "Let this clip's sound carry over", hint: 'It keeps going over the start of the next shot (an L-cut).' },
  { mode: 'auto', label: 'Change sound in a pause (auto)', hint: 'MetaRush moves the change into the nearest pause, so no word is cut off.' },
];

const DIRS: { id: string; label: string; icon: React.ReactNode }[] = [
  { id: 'left', label: 'Left', icon: <ArrowLeft size={14} /> },
  { id: 'right', label: 'Right', icon: <ArrowRight size={14} /> },
  { id: 'up', label: 'Up', icon: <ArrowUp size={14} /> },
  { id: 'down', label: 'Down', icon: <ArrowDown size={14} /> },
];

function initialChoice(clip: Clip): Choice {
  const t = clip.transitionIn;
  const a = t?.audio;
  return {
    type: t?.type ?? 'cut',
    duration: t?.duration ?? 0,
    direction: t?.direction,
    easing: t?.easing,
    sound: (a?.mode as SoundMode) ?? 'cut',
    soundSeconds: a?.duration ?? (a?.mode === 'carry' ? 0.3 : a?.mode === 'auto' ? 1 : 0.4),
  };
}

function transitionValue(c: Choice): EditOp['transition'] {
  const audio = c.sound === 'cut' ? undefined : { mode: c.sound, ...(c.soundSeconds ? { duration: c.soundSeconds } : {}) };
  if (c.type === 'cut' && !audio) return undefined;
  return {
    type: c.type as NonNullable<EditOp['transition']>['type'],
    ...(c.type !== 'cut' && c.duration ? { duration: c.duration } : {}),
    ...(c.direction ? { direction: c.direction as 'left' | 'right' | 'up' | 'down' } : {}),
    ...(c.easing ? { easing: c.easing as 'ease-in-out' | 'linear' | 'ease-in' | 'ease-out' } : {}),
    ...(audio ? { audio } : {}),
  };
}

/**
 * The marker between two clips opens this: the transition library as tiles, a
 * length that reads like speech, the way the sound changes in plain words, and
 * a live preview of that spot (about 1.5 s either side) with the choice applied,
 * resolved by Go. Applying it goes through cut.transition, like the command.
 */
export function TransitionPicker({
  editor,
  projectId,
  spec,
  clipId,
  prevName,
  nextName,
  onDone,
}: {
  editor: TimelineEditor;
  projectId: string;
  spec: Spec;
  clipId: string;
  prevName: string;
  nextName: string;
  onDone: (applied: string | undefined) => void;
}) {
  const catalog = useOp('edit.transitions', {}, { staleTime: Infinity });
  const project = useOp('project.show', { id: projectId });
  const clip = spec.clips.find((c) => c.id === clipId);
  const [choice, setChoice] = useState<Choice>(() => (clip ? initialChoice(clip) : { type: 'cut', duration: 0, sound: 'cut', soundSeconds: 0.4 }));
  const [preview, setPreview] = useState<{ plan?: RenderPlan; problems: CutProblem[]; warnings: CutProblem[] }>({ problems: [], warnings: [] });
  const [busy, setBusy] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const style = project.data?.project.brief.transitionStyle;
  const info = catalog.data?.transitions.find((t) => t.type === choice.type);

  // The live preview: the unsaved changes plus this choice, as Go resolves them.
  const key = JSON.stringify(transitionValue(choice) ?? null);
  useEffect(() => {
    let gone = false;
    setBusy(true);
    const t = setTimeout(() => {
      editor
        .previewWith([{ op: 'setTransition', id: clipId, transition: transitionValue(choice) }])
        .then((out) => {
          if (gone) return;
          setPreview({
            plan: out.renderPlan ?? undefined,
            problems: out.errors,
            warnings: out.warnings.filter((w) =>
              ['transition_clamped', 'split_limited', 'split_no_pause', 'split_cuts_word', 'split_drops_words', 'voices_overlap', 'flash_shot', 'direction_ignored'].includes(w.code),
            ),
          });
        })
        .catch(() => undefined)
        .finally(() => !gone && setBusy(false));
    }, 220);
    return () => {
      gone = true;
      clearTimeout(t);
    };
  }, [key, clipId]); // eslint-disable-line react-hooks/exhaustive-deps

  const pc = preview.plan?.clips.find((c) => c.id === clipId);
  const fps = preview.plan?.fps ?? 30;
  const cutFrame = pc ? (pc.transitionIn.audio?.cutFrame ?? pc.from + Math.floor((pc.transitionIn.durationInFrames ?? 0) / 2)) : 0;

  const pick = (type: string) => {
    const t = catalog.data?.transitions.find((x) => x.type === type);
    setChoice((c) => ({
      ...c,
      type,
      duration: c.type === type ? c.duration : (t?.defaultDuration ?? 0),
      direction: t?.directions?.length ? (c.direction ?? t.defaultDirection) : undefined,
      easing: t?.easing ? c.easing : undefined,
    }));
  };

  const apply = async () => {
    setSaving(true);
    setError(undefined);
    const words = info?.name ?? choice.type;
    let failed: string | undefined;
    const ok = await editor
      .runSaved(`Changed the transition into ${nextName}`, async () => {
        const out = await call('cut.transition', {
          projectId,
          clip: clipId,
          type: choice.type,
          ...(choice.type !== 'cut' ? { duration: choice.duration } : {}),
          ...(choice.direction ? { direction: choice.direction as 'left' | 'right' | 'up' | 'down' } : {}),
          ...(choice.easing ? { easing: choice.easing } : {}),
          sound: choice.sound,
          ...(choice.sound === 'lead' || choice.sound === 'carry' || choice.sound === 'auto' ? { soundSeconds: choice.soundSeconds } : {}),
        });
        if (out.noChange) return editor.base?.id;
        return out.applied?.revision.id;
      })
      .catch((e: unknown) => {
        const ae = toApiError(e);
        failed = [ae.message, ae.hint].filter(Boolean).join(' ');
        return false;
      });
    setSaving(false);
    if (ok) onDone(words);
    else setError(failed ?? 'It could not be saved yet: the timeline has changes that need attention first.');
  };

  if (!clip) return null;
  const vertical = (spec.output.height ?? 1920) > (spec.output.width ?? 1080);
  const range = info ? { min: Math.max(0.1, info.minDuration), max: Math.max(info.minDuration + 0.1, info.maxDuration) } : { min: 0, max: 0 };
  return (
    <div className="space-y-5" data-no-timeline-keys>
      <p className="text-[13px] leading-relaxed text-muted">
        How {prevName} turns into {nextName}. The preview plays that spot over and over with your choice.
      </p>
      <div className={cx('grid gap-4', vertical ? 'sm:grid-cols-[200px_1fr]' : 'sm:grid-cols-[minmax(0,1.2fr)_1fr]')}>
        <div className={cx('relative overflow-hidden rounded-[12px] bg-inset', vertical ? 'mx-auto aspect-[9/16] w-[180px] sm:w-full' : 'aspect-video w-full')}>
          <Suspense
            fallback={
              <div className="grid h-full place-items-center">
                <Spinner size={14} />
              </div>
            }
          >
            <LoopPreview plan={preview.plan} inFrame={Math.round(cutFrame - 1.5 * fps)} outFrame={Math.round(cutFrame + 1.5 * fps)} busy={busy} />
          </Suspense>
        </div>
        <div className="min-w-0 space-y-3">
          <div>
            <div className="text-[15px] font-semibold text-fg">{info?.name ?? 'Cut'}</div>
            <div className="mt-0.5 text-[12.5px] leading-relaxed text-muted">{info?.summary}</div>
            {info?.whenToUse ? <div className="mt-1 text-[12px] leading-relaxed text-subtle">{info.whenToUse}</div> : null}
          </div>
          {info && info.type !== 'cut' ? (
            <div>
              <label className="flex items-baseline justify-between text-[12px] font-medium text-muted" htmlFor="tp-length">
                <span>How long</span>
                <span className="text-fg">
                  {sayDuration(choice.duration)} <span className="tc text-subtle">· {choice.duration.toFixed(choice.duration < 1 ? 2 : 1)} s</span>
                </span>
              </label>
              <input
                id="tp-length"
                type="range"
                min={range.min}
                max={range.max}
                step={0.05}
                value={choice.duration || info.defaultDuration}
                onChange={(e) => setChoice((c) => ({ ...c, duration: Math.round(Number(e.target.value) * 100) / 100 }))}
                className="mt-1 w-full accent-[var(--accent)]"
              />
            </div>
          ) : null}
          {info?.directions?.length ? (
            <div role="group" aria-label="Which way it moves" className="flex flex-wrap items-center gap-1">
              <span className="mr-1 text-[12px] font-medium text-muted">Moves</span>
              {DIRS.map((d) => (
                <button
                  key={d.id}
                  type="button"
                  aria-pressed={(choice.direction ?? info.defaultDirection) === d.id}
                  onClick={() => setChoice((c) => ({ ...c, direction: d.id }))}
                  className={cx(
                    'inline-flex h-8 items-center gap-1 rounded-[8px] px-2.5 text-[12px] hairline',
                    (choice.direction ?? info.defaultDirection) === d.id ? 'bg-accent-soft text-accent-text' : 'text-muted hover:text-fg',
                  )}
                >
                  {d.icon}
                  {d.label}
                </button>
              ))}
            </div>
          ) : null}
          {info?.easing ? (
            <div role="group" aria-label="Pace" className="flex flex-wrap items-center gap-1">
              <span className="mr-1 text-[12px] font-medium text-muted">Pace</span>
              {(catalog.data?.easings ?? []).map((e) => (
                <button
                  key={e.id}
                  type="button"
                  title={e.summary}
                  aria-pressed={(choice.easing ?? catalog.data?.defaultEasing) === e.id}
                  onClick={() => setChoice((c) => ({ ...c, easing: e.id }))}
                  className={cx(
                    'h-8 rounded-[8px] px-2.5 text-[12px] hairline',
                    (choice.easing ?? catalog.data?.defaultEasing) === e.id ? 'bg-accent-soft text-accent-text' : 'text-muted hover:text-fg',
                  )}
                >
                  {e.name}
                </button>
              ))}
            </div>
          ) : null}
        </div>
      </div>

      <section aria-label="Transitions">
        {catalog.isLoading ? (
          <Spinner size={14} label="Loading the transitions" />
        ) : (
          <div role="radiogroup" aria-label="Picture" className="grid grid-cols-2 gap-2 sm:grid-cols-5">
            {(catalog.data?.transitions ?? []).map((t) => {
              const on = t.type === choice.type;
              const fits = style ? t.styles.includes(style) : !t.flashy;
              return (
                <button
                  key={t.type}
                  type="button"
                  role="radio"
                  aria-checked={on}
                  onClick={() => pick(t.type)}
                  title={t.summary}
                  className={cx('group rounded-[12px] p-1.5 text-left transition-colors hairline', on ? 'bg-accent-soft ring-2 ring-accent' : 'hover:bg-hover')}
                >
                  <TransitionTile type={t.type} live={on} />
                  <div className="mt-1.5 flex items-center gap-1 px-0.5">
                    <span className="truncate text-[12px] font-medium text-fg">{t.name}</span>
                    {t.flashy ? <Sparkle size={11} className="shrink-0 text-subtle" aria-label="Flashy" /> : null}
                  </div>
                  {style && fits ? <div className="px-0.5 text-[10.5px] text-ok">fits your style</div> : null}
                </button>
              );
            })}
          </div>
        )}
      </section>

      <section aria-label="Sound">
        <div className="mb-1.5 text-[12px] font-medium text-muted">The sound</div>
        <div role="radiogroup" aria-label="How the sound changes" className="grid gap-1.5 sm:grid-cols-2">
          {SOUND.map((s) => {
            const on = choice.sound === s.mode;
            return (
              <div key={s.mode} className={cx('rounded-[12px] p-2.5 hairline', on ? 'bg-accent-soft ring-1 ring-accent-line' : '')}>
                <button
                  type="button"
                  role="radio"
                  aria-checked={on}
                  onClick={() => setChoice((c) => ({ ...c, sound: s.mode, soundSeconds: s.mode === 'carry' ? 0.3 : s.mode === 'auto' ? 1 : 0.4 }))}
                  className="flex w-full items-start gap-2 text-left"
                >
                  <span className={cx('mt-[3px] grid h-3.5 w-3.5 shrink-0 place-items-center rounded-full border', on ? 'border-accent' : 'border-line-strong')}>
                    {on ? <span className="h-2 w-2 rounded-full bg-accent" /> : null}
                  </span>
                  <span>
                    <span className="block text-[12.5px] font-medium text-fg">{s.label}</span>
                    <span className="block text-[11.5px] leading-relaxed text-muted">{s.hint}</span>
                  </span>
                </button>
                {on && (s.mode === 'lead' || s.mode === 'carry') ? (
                  <div role="group" aria-label="How much" className="mt-2 flex flex-wrap gap-1 pl-5">
                    {[0.2, 0.4, 0.5, 1].map((v) => (
                      <button
                        key={v}
                        type="button"
                        aria-pressed={choice.soundSeconds === v}
                        onClick={() => setChoice((c) => ({ ...c, soundSeconds: v }))}
                        className={cx('h-7 rounded-full px-2.5 text-[11.5px] hairline', choice.soundSeconds === v ? 'bg-accent text-accent-ink' : 'text-muted hover:text-fg')}
                      >
                        {sayDuration(v)} <span className="tc opacity-70">{v} s</span>
                      </button>
                    ))}
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
      </section>

      {preview.problems.length || preview.warnings.length || error ? (
        <ul className="space-y-1">
          {[...preview.problems, ...preview.warnings].map((p, i) => (
            <li key={i} className={cx('flex items-start gap-2 rounded-[10px] px-3 py-2 text-[12.5px]', p.severity === 'error' ? 'bg-danger-soft text-fg' : 'bg-accent-soft text-accent-text')}>
              <WarningCircle size={14} className="mt-[2px] shrink-0" /> {p.message}
            </li>
          ))}
          {error ? <li className="rounded-[10px] bg-danger-soft px-3 py-2 text-[12.5px] text-fg">{error}</li> : null}
        </ul>
      ) : null}

      <div className="sticky bottom-[-20px] z-10 -mx-5 -mb-5 flex flex-wrap items-center justify-end gap-2 border-t border-line bg-raised px-5 py-3 sm:-mx-6 sm:-mb-6 sm:bottom-[-24px] sm:px-6">
        {style ? <Badge>{style} style</Badge> : null}
        <div className="flex-1" />
        <Button variant="ghost" onClick={() => onDone(undefined)}>
          Cancel
        </Button>
        <Button variant="primary" loading={saving} disabled={preview.problems.length > 0} onClick={() => void apply()}>
          Use {info?.name.toLowerCase() ?? 'this'}
        </Button>
      </div>
    </div>
  );
}

/** A small looping picture of what a transition does (two shots, A and B). */
function TransitionTile({ type, live }: { type: string; live: boolean }) {
  return (
    <div className={`mr-tt mr-tt-${type} relative aspect-video overflow-hidden rounded-[8px] bg-black`} data-live={live || undefined} aria-hidden>
      <div className="mr-tt-a absolute inset-0 grid place-items-center bg-[linear-gradient(135deg,#b56f2a,#e0a458)] text-[11px] font-semibold text-black/60">A</div>
      <div className="mr-tt-b absolute inset-0 grid place-items-center bg-[linear-gradient(135deg,#2f4a63,#7d9cb8)] text-[11px] font-semibold text-white/70">B</div>
    </div>
  );
}

/** Guarded hook for callers that only have a clip ID. */
export function useClipNames(spec: Spec | undefined, clipId: string | undefined, name: (c: Clip) => string) {
  return useMemo(() => {
    const i = spec?.clips.findIndex((c) => c.id === clipId) ?? -1;
    if (!spec || i < 0) return { prev: 'the clip before', next: 'this clip' };
    return { prev: i > 0 ? name(spec.clips[i - 1]!) : 'the start', next: name(spec.clips[i]!) };
  }, [spec, clipId, name]);
}
