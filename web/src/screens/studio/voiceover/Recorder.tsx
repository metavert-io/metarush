import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ArrowLeft, ArrowRight, CheckCircle, Headphones, Microphone, Play, Record, Stop, WarningCircle, X } from '@phosphor-icons/react';
import { call, toApiError } from '../../../api/client';
import { uploadFraction } from '../../../api/uploads';
import type { NarrationLine, NarrationOut, PlaceOut, Render, RenderPlan } from '../../../api/types';
import { useHost } from '../../../lib/host';
import { markJobSeen } from '../../../lib/workbench';
import { CutPreview, type CutPreviewHandle } from '../../../player/LazyCutPreview';
import { useDialog } from '../../../ui/Dialog';
import { Button, cx, Kbd, Progress, Spinner } from '../../../ui/primitives';
import { applyPlan, checkApplied, clock, errorText, useRefreshCut } from './api';
import { clipName, type Scope } from './AddVoiceover';
import { encodeWav, listMics, Mic, MicError, micProblem, takeStart, voicedRange, type MicDevice } from './capture';
import { LevelMeter } from './LevelMeter';
import { LivePrompter, promptLines } from './LivePrompter';
import { advance, nextToBring, schedule } from './prompter';
import { uploadRecording, waitForWords } from './recordings';
import { TakeReview } from './TakeReview';
import type { Clip } from '../../../api/types';

/*
 * Recorder mode. Live (the default, D85): the person watches the cut and speaks;
 * the take starts where the playhead was and lands exactly where it was spoken —
 * the whole recording is kept, each line gets its piece of it (cut only in the
 * pauses) and starts where it was said. The teleprompter follows the video
 * (each line about two seconds before its moment; lines without timing at the
 * narrator's pace, Space or a clicker to bring the next one in). Recording from
 * a line again (a punch-in) replaces only that stretch; every take is kept.
 * One line at a time (D63) stays as the second mode: each take is placed at its
 * line's start point and trimmed only in silence. Takes upload like any file,
 * so they show up in the project and survive a reload. Recording is never
 * offered to the assistant.
 */

type Mode = 'live' | 'lines';
type Phase = 'setup' | 'countin' | 'recording' | 'placing' | 'review' | 'finished';

interface LocalTake {
  key: string;
  lineId?: string;
  n: number;
  url: string;
  samples: Float32Array;
  rate: number;
  duration: number;
  range: { in: number; out: number };
  /** The loudest moment is under −34 dBFS: the microphone is probably off or far away. */
  quiet: boolean;
  state: 'saving' | 'saved' | 'failed' | 'kept';
  takeId?: string;
  error?: string;
  saved: Promise<string | null>;
}

interface Placing {
  step: 'upload' | 'listen' | 'place';
  progress: number;
  waiting: boolean;
}

export function Recorder({ projectId, projectName, revisionId, headRevisionId, lines, plan, clips, scope, renders, startLine, startAt, onClose }: {
  projectId: string;
  projectName: string;
  revisionId: string;
  /** The newest version of the cut, read at each save. */
  headRevisionId: () => string | undefined;
  lines: NarrationLine[];
  plan?: RenderPlan;
  clips: Clip[];
  scope: Scope;
  renders: Render[];
  /** Record this line (one line at a time). */
  startLine?: string;
  /** Where the playhead was (live takes start there). */
  startAt?: number;
  onClose: () => void;
}) {
  const dialog = useDialog();
  const refresh = useRefreshCut();
  const preview = useRef<CutPreviewHandle>(null);
  // The picture stays on the version the recorder opened with while takes are kept.
  const [pictureRev] = useState(revisionId);
  const [mic, setMic] = useState<Mic | null>(null);
  const [micError, setMicError] = useState<MicError | null>(null);
  const [opening, setOpening] = useState(true);
  const [devices, setDevices] = useState<MicDevice[]>([]);
  const [deviceId, setDeviceId] = useState<string | undefined>();
  const [mode, setMode] = useState<Mode>(startLine ? 'lines' : 'live');
  const [guide, setGuide] = useState(false);
  const [phase, setPhase] = useState<Phase>('setup');
  const [count, setCount] = useState(3);
  const [elapsed, setElapsed] = useState(0);
  const [time, setTime] = useState(startAt ?? 0);
  const [takes, setTakes] = useState<LocalTake[]>([]);
  const takesRef = useRef(takes);
  takesRef.current = takes;
  const inScope = useMemo(() => (scope.clipId ? linesOverClip(lines, plan, scope.clipId) : lines), [lines, plan, scope.clipId]);
  const [idx, setIdx] = useState(() => Math.max(0, inScope.findIndex((l) => l.id === startLine)));
  const line = inScope[idx];
  const chain = useRef<Promise<unknown>>(Promise.resolve());
  const startedAt = useRef(0);
  const root = useRef<HTMLDivElement>(null);
  const fps = plan?.fps || 30;
  const duration = plan ? plan.durationInFrames / plan.fps : 0;
  const scopeStart = useMemo(() => {
    const c = scope.clipId ? plan?.clips.find((x) => x.id === scope.clipId) : undefined;
    return c && plan ? c.from / plan.fps : 0;
  }, [plan, scope.clipId]);

  /* ----- live takes ----- */
  const prompt = useMemo(() => promptLines(lines), [lines]);
  const timed = prompt.some((l) => l.pinned);
  const [from, setFrom] = useState(() => startAt ?? scopeStart);
  const [until, setUntil] = useState<number | undefined>();
  // Re-recording one line: playback starts a moment before it, and only that
  // line is placed again (the stretch it plays in is replaced).
  const reLine = until !== undefined ? lines.find((l) => Math.abs(reStartOf(l) - from) < 0.05) : undefined;
  const rangeEnd = reLine ? until : undefined;
  const [due, setDue] = useState<number[]>(() => schedule(prompt, from));
  const framesLog = useRef<[number, number][]>([]);
  const recFrom = useRef(0);
  const [placing, setPlacing] = useState<Placing | null>(null);
  const [placed, setPlaced] = useState<PlaceOut | null>(null);
  const [kept, setKept] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [override, setOverride] = useState<RenderPlan | undefined>();
  const [playing, setPlaying] = useState(false);
  const [after, setAfter] = useState<NarrationOut | undefined>();
  const latestRev = useRef<string>(revisionId);
  const abort = useRef<AbortController | null>(null);
  // Between takes the schedule follows the playhead (it is where the take will start).
  useEffect(() => {
    if (phase === 'setup' && mode === 'live') setDue(schedule(prompt, from));
  }, [phase, mode, prompt, from]);

  /* ----- microphone ----- */
  const open = useCallback(async (id?: string) => {
    setOpening(true);
    setMicError(null);
    try {
      const m = await Mic.open(id);
      setMic((old) => {
        old?.close();
        return m;
      });
      setDeviceId(m.deviceId);
      setDevices(await listMics());
    } catch (e) {
      setMicError(micProblem(e));
      // Another microphone can be chosen when this one didn't answer.
      setDevices(await listMics().catch(() => []));
    } finally {
      setOpening(false);
    }
  }, []);
  useEffect(() => {
    void open();
  }, [open]);
  const micRef = useRef<Mic | null>(null);
  micRef.current = mic;
  useEffect(
    () => () => {
      micRef.current?.close();
      abort.current?.abort();
    },
    [],
  );

  /* ----- the player: silent, or a quiet guide (full sound while playing a take back) ----- */
  useEffect(() => {
    const p = preview.current;
    if (!p) return;
    if (playing) {
      p.unmute();
      p.setVolume(1);
    } else if (guide) {
      p.unmute();
      p.setVolume(0.18);
    } else p.mute();
  }, [guide, phase, playing]);

  /* ----- recording ----- */
  const startAtLine = mode === 'lines' ? (line?.start ?? scopeStart) : from;
  const startCounts = useRef(new Map(lines.map((l) => [l.id, l.takeCount])));
  useEffect(() => {
    if (phase === 'setup') preview.current?.seekToSeconds(startAtLine);
  }, [phase, startAtLine]);
  const record = () => {
    if (!mic || phase === 'countin' || phase === 'recording' || phase === 'placing') return;
    mic.resume();
    setProblem(null);
    setOverride(undefined);
    setPlaying(false);
    const p = preview.current;
    p?.pause();
    p?.seekToSeconds(startAtLine);
    setCount(3);
    setPhase('countin');
  };
  const recordRef = useRef<() => void>(() => undefined);
  recordRef.current = record;
  useEffect(() => {
    if (phase !== 'countin') return;
    if (count === 0) {
      if (mode === 'live') {
        // The take starts on the frame the player shows.
        recFrom.current = Math.round(from * fps) / fps;
        framesLog.current = [];
        setDue(schedule(prompt, recFrom.current));
      }
      mic?.start();
      startedAt.current = performance.now();
      setElapsed(0);
      setPhase('recording');
      preview.current?.play();
      return;
    }
    const t = setTimeout(() => setCount((c) => c - 1), 1000);
    return () => clearTimeout(t);
  }, [phase, count, mic]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (phase !== 'recording') return;
    let raf = 0;
    const tick = () => {
      setElapsed((performance.now() - startedAt.current) / 1000);
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [phase]);

  const saveTake = (t: Omit<LocalTake, 'saved' | 'state'>, forLine: NarrationLine | undefined): Promise<string | null> => {
    if (!forLine) return Promise.resolve(null);
    const file = new File([encodeWav(t.samples, t.rate)], `Narration line ${forLine.line}, take ${t.n}.wav`, { type: 'audio/wav' });
    return (async () => {
      try {
        const assetId = await uploadRecording(file, projectId, projectName);
        const tv = await call('voiceover.addTake', { projectId, beatId: forLine.id, text: forLine.text, assetId, in: t.range.in, out: t.range.out, source: 'recorded' });
        setTakes((ts) => ts.map((x) => (x.key === t.key ? { ...x, state: x.state === 'kept' ? 'kept' : 'saved', takeId: tv.id } : x)));
        return tv.id;
      } catch (e) {
        setTakes((ts) => ts.map((x) => (x.key === t.key ? { ...x, state: 'failed', error: errorText(e) } : x)));
        return null;
      }
    })();
  };

  /** A live take: uploaded, listened to, then placed where it was said (checked, not yet in the cut). */
  const placeTake = async (samples: Float32Array, rate: number, t0: number) => {
    setPhase('placing');
    setPlacing({ step: 'upload', progress: 0, waiting: false });
    const ctrl = new AbortController();
    abort.current = ctrl;
    const file = new File([encodeWav(samples, rate)], `Live take from ${clock(t0)}.wav`, { type: 'audio/wav' });
    try {
      const assetId = await uploadRecording(file, projectId, projectName, (it) => setPlacing((p) => (p ? { ...p, progress: uploadFraction(it) } : p)));
      setPlacing({ step: 'listen', progress: 1, waiting: false });
      latestRev.current = headRevisionId() ?? latestRev.current;
      const only = rangeEnd !== undefined && reLine ? { until: rangeEnd, lines: [reLine.id] } : {};
      const placeIt = () => call('voiceover.place', { revisionId: latestRev.current, assetId, from: t0, ...only, source: 'recorded' });
      // Asked at once, the take is marked as narration while MetaRush listens to it
      // (no "file ready" notice); it is placed as soon as its words are there.
      let out = await placeIt().catch((e: unknown) => {
        if (toApiError(e).code === 'E_CONFLICT') return null;
        throw e;
      });
      if (!out) {
        await waitForWords(assetId, ctrl.signal, (w) => setPlacing((p) => (p ? { ...p, waiting: w } : p)));
        setPlacing({ step: 'place', progress: 1, waiting: false });
        out = await placeIt();
      }
      markJobSeen(out.jobId);
      setPlaced(out);
      setKept(false);
      setAfter(undefined);
      setPhase('review');
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return;
      setProblem(`${errorText(e)} The take is kept in the project.`);
      setPhase('setup');
    } finally {
      setPlacing(null);
    }
  };

  const stop = async () => {
    if (!mic || phase !== 'recording') return;
    preview.current?.pause();
    const samples = await mic.stop();
    const rate = mic.sampleRate;
    const dur = samples.length / rate;
    if (dur < 0.3) {
      setPhase('setup');
      return;
    }
    if (mode === 'live') {
      const t0 = takeStart(recFrom.current, fps, framesLog.current, mic.firstSampleAt());
      await placeTake(samples, rate, t0);
      return;
    }
    const voiced = voicedRange(samples, rate);
    const lineId = line?.id;
    const n = takesRef.current.filter((x) => x.lineId === lineId).length + 1 + (lineId ? (startCounts.current.get(lineId) ?? 0) : 0);
    const base = {
      key: `t${Date.now()}`,
      lineId,
      n,
      url: URL.createObjectURL(encodeWav(samples, rate)),
      samples,
      rate,
      duration: dur,
      range: { in: voiced.in, out: voiced.out },
      quiet: voiced.peak < 0.02,
    };
    const saved = saveTake(base, line);
    setTakes((ts) => [...ts, { ...base, state: 'saving', saved }]);
    setPhase('review');
  };
  const stopRef = useRef<() => void>(() => undefined);
  stopRef.current = () => void stop();

  // A re-recorded stretch stops on its own a little after its end; any take stops at the end of the video.
  useEffect(() => {
    if (phase !== 'recording' || mode !== 'live') return;
    if ((rangeEnd !== undefined && time >= rangeEnd + 0.8) || (duration > 0 && time >= duration - 1 / fps && elapsed > 1)) stopRef.current();
  }, [time, phase, mode, rangeEnd, duration, fps, elapsed]);

  /* ----- one line at a time: keep a take ----- */
  const current = takes.filter((t) => t.lineId === line?.id).at(-1);
  const keepLine = async () => {
    if (!current) return;
    const forLine = line;
    if (!forLine) return;
    setTakes((ts) => ts.map((x) => (x.key === current.key ? { ...x, state: x.state === 'failed' ? 'failed' : 'kept' } : x)));
    chain.current = chain.current.then(async () => {
      const takeId = await current.saved;
      if (!takeId) return;
      try {
        const rev = headRevisionId() ?? revisionId;
        await applyPlan(await call('voiceover.choose', { revisionId: rev, beat: forLine.id, takeId }));
        refresh();
      } catch (e) {
        setTakes((ts) => ts.map((x) => (x.key === current.key ? { ...x, state: 'failed', error: errorText(e) } : x)));
      }
    });
    if (idx < inScope.length - 1) {
      setIdx(idx + 1);
      setPhase('setup');
    } else setPhase('finished');
  };

  /* ----- a live take: keep it, snap it, fix it, play it back ----- */
  const loadAfter = async (rev: string) => {
    try {
      setAfter(await call('voiceover.show', { revisionId: rev }));
    } catch {
      /* the flags are a convenience */
    }
  };
  const keepLive = async () => {
    if (!placed?.result) return;
    setBusy('keep');
    try {
      const res = await applyPlan(placed.result);
      latestRev.current = res.revision.id;
      setKept(true);
      refresh();
      await loadAfter(res.revision.id);
    } catch (e) {
      setProblem(errorText(e));
    } finally {
      setBusy(null);
    }
  };
  const snap = async (where: 'planned' | 'recorded') => {
    if (!placed) return;
    setBusy(where === 'planned' ? 'snap' : 'back');
    try {
      const out = await call('voiceover.place', { revisionId: latestRev.current, read: placed.readId, where, apply: true });
      const ap = checkApplied(out.applied);
      latestRev.current = ap.revisionId ?? latestRev.current;
      setPlaced(out);
      refresh();
      await loadAfter(latestRev.current);
    } catch (e) {
      setProblem(errorText(e));
    } finally {
      setBusy(null);
    }
  };
  const fix = async (beatId: string, strategy: string) => {
    setBusy(`fix:${beatId}:${strategy}`);
    try {
      const out = await call('voiceover.fit', { revisionId: latestRev.current, strategy: strategy as 'extend-shot', beats: [beatId] });
      if (!out.result) throw new Error(out.summary);
      const res = await applyPlan(out.result);
      latestRev.current = res.revision.id;
      refresh();
      await loadAfter(res.revision.id);
    } catch (e) {
      setProblem(errorText(e));
    } finally {
      setBusy(null);
    }
  };
  const playBack = async () => {
    const p = preview.current;
    if (playing) {
      p?.pause();
      setPlaying(false);
      return;
    }
    if (!placed) return;
    setBusy('play');
    try {
      // Before keeping: the cut with this take, resolved by Go; after: the version it made.
      const pl = kept
        ? await call('revision.plan', { id: latestRev.current })
        : (await call('edit.preview', { plan: placed.result!.plan as unknown as Record<string, unknown> })).renderPlan;
      if (!pl) throw new Error('The take could not be played back.');
      setOverride(pl);
      setPlaying(true);
      setTimeout(() => {
        preview.current?.seekToSeconds(Math.max(0, placed.from - 0.5));
        preview.current?.play();
      }, 150);
    } catch (e) {
      setProblem(errorText(e));
    } finally {
      setBusy(null);
    }
  };
  const again = () => {
    preview.current?.pause();
    setPlaying(false);
    setOverride(undefined);
    setPlaced(null);
    setKept(false);
    setPhase('setup');
  };

  const finish = async () => {
    await chain.current;
    refresh();
    mic?.close();
    for (const t of takesRef.current) URL.revokeObjectURL(t.url);
    onClose();
  };
  const close = async () => {
    if (phase === 'recording') {
      const ok = await dialog.confirm({ title: 'Stop recording and close?', body: 'This take is thrown away. Takes you already kept stay.', confirmLabel: 'Close the recorder', tone: 'danger' });
      if (!ok) return;
      await mic?.stop();
    }
    if (phase === 'placing') abort.current?.abort();
    if (takesRef.current.some((t) => t.state === 'saving')) await Promise.all(takesRef.current.map((t) => t.saved));
    await finish();
  };

  /* ----- keyboard ----- */
  const keepRef = useRef<() => void>(() => undefined);
  keepRef.current = () => void (mode === 'live' ? (placed && !kept ? keepLive() : undefined) : keepLine());
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (document.querySelector('[data-mr-dialog="open"]')) return;
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === 'SELECT' || t.tagName === 'INPUT' || t.tagName === 'TEXTAREA')) return;
      if (mode === 'live' && phase === 'recording') {
        // Space or a clicker brings the next line in; Enter or Escape stops.
        if (e.key === ' ' || e.key === 'ArrowRight' || e.key === 'ArrowDown' || e.key === 'PageDown') {
          e.preventDefault();
          const k = nextToBring(due, time);
          if (k >= 0) setDue((d) => advance(d, prompt, k, time));
        } else if (e.key === 'Enter' || e.key === 'Escape') {
          e.preventDefault();
          stopRef.current();
        }
        return;
      }
      if (e.key === ' ') {
        e.preventDefault();
        if (phase === 'recording') stopRef.current();
        else if (phase === 'setup' || (phase === 'review' && mode === 'lines')) recordRef.current();
      } else if (e.key === 'Enter' && phase === 'review') {
        e.preventDefault();
        keepRef.current();
      } else if (e.key === 'Escape') {
        e.preventDefault();
        void close();
      } else if ((e.key === 'ArrowRight' || e.key === 'ArrowLeft') && (phase === 'setup' || phase === 'review') && mode === 'lines') {
        e.preventDefault();
        setIdx((i) => Math.max(0, Math.min(inScope.length - 1, i + (e.key === 'ArrowRight' ? 1 : -1))));
        setPhase('setup');
      }
    };
    window.addEventListener('keydown', onKey, true);
    return () => window.removeEventListener('keydown', onKey, true);
  });
  useEffect(() => {
    root.current?.querySelector<HTMLElement>('[data-autofocus]')?.focus();
  }, []);

  /* ----- the playhead ----- */
  const onFrame = useCallback(
    (f: number, r: number) => {
      setTime(f / r);
      if (phase === 'recording' && mode === 'live') framesLog.current.push([performance.now(), f]);
      if (phase === 'setup' && mode === 'live' && !preview.current?.isPlaying()) setFrom(f / r);
    },
    [phase, mode],
  );
  const reRecord = (id: string) => {
    if (!id) {
      setUntil(undefined);
      return;
    }
    const l = lines.find((x) => x.id === id);
    if (!l) return;
    const next = lines.filter((x) => x.start > l.start + 0.01).sort((a, b) => a.start - b.start)[0];
    const start = reStartOf(l);
    setFrom(start);
    setUntil(Math.min(next ? next.start - 0.05 : duration, l.end + 0.3));
    preview.current?.seekToSeconds(start);
  };
  const windowSecs = line ? Math.max(0.5, line.window) : 0;
  const nextLine = line ? inScope[inScope.indexOf(line) + 1] : undefined;

  return createPortal(
    <div
      ref={root}
      role="dialog"
      aria-modal="true"
      aria-label="Record narration"
      className="fixed inset-0 z-[75] flex flex-col bg-[#0b0a09] text-[#f2ece4]"
      data-mr-recorder
      data-phase={phase}
      data-mode={mode}
      onPointerDown={() => mic?.resume()}
    >
      {/* top bar */}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-white/[0.07] px-4 py-3 sm:px-6">
        <div className="flex min-w-0 flex-1 items-center gap-2 sm:flex-none">
          <Microphone size={18} className="text-[#e0a458]" aria-hidden />
          <h2 className="truncate text-[15px] font-semibold">Recording narration</h2>
          <span className="truncate text-[12.5px] text-[#a69d93]">{scope.clipId ? clipName(clips, scope.clipId) : 'the whole video'}</span>
        </div>
        {/* On a phone the close button stays beside the title and the controls take a row of their own. */}
        <div className="order-3 flex w-full flex-wrap items-center gap-3 sm:order-2 sm:ml-auto sm:w-auto">
          {devices.length > 1 ? (
            <label className="flex items-center gap-2 text-[12.5px] text-[#a69d93]">
              Microphone
              <select
                value={deviceId ?? ''}
                disabled={phase === 'recording' || phase === 'countin'}
                onChange={(e) => {
                  setDeviceId(e.target.value);
                  void open(e.target.value);
                }}
                className="h-8 max-w-[200px] rounded-[9px] bg-white/[0.06] px-2 text-[12.5px] text-[#f2ece4]"
              >
                {devices.map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.label}
                  </option>
                ))}
              </select>
            </label>
          ) : null}
          {mic ? <LevelMeter mic={mic} /> : null}
          <label className="flex items-center gap-2 text-[12.5px] text-[#a69d93]" title="Hear the video quietly while you record. Use headphones, or the microphone picks it up.">
            <input type="checkbox" checked={guide} onChange={(e) => setGuide(e.target.checked)} className="h-4 w-4 accent-[#e0a458]" />
            <Headphones size={14} aria-hidden /> Quiet guide sound
          </label>
        </div>
        <button
          type="button"
          onClick={() => void close()}
          aria-label="Close the recorder"
          className="order-2 grid h-9 w-9 shrink-0 place-items-center rounded-[10px] text-[#a69d93] hover:bg-white/[0.06] hover:text-white sm:order-3"
        >
          <X size={17} />
        </button>
      </div>

      {micError ? (
        <MicHelp
          error={micError}
          busy={opening}
          devices={devices}
          deviceId={deviceId}
          onPick={setDeviceId}
          onRetry={() => void open(deviceId)}
          onClose={() => void finish()}
        />
      ) : (
        <div className="grid min-h-0 flex-1 grid-cols-1 gap-4 overflow-y-auto p-4 sm:p-6 lg:grid-cols-[minmax(0,1.1fr)_minmax(340px,1fr)] lg:gap-8">
          {/* the picture */}
          <div className="min-w-0">
            <CutPreview
              ref={preview}
              revisionId={pictureRev}
              renders={renders}
              onFrame={onFrame}
              plan={override}
              onPlayingChange={(p) => {
                if (!p && playing) setPlaying(false);
                // Watching before recording: the take starts where the video was paused.
                if (!p && phase === 'setup' && mode === 'live') {
                  const f = preview.current?.getFrame();
                  if (f !== undefined) setFrom(f / fps);
                }
              }}
              maxHeight="min(46dvh, 560px)"
            />
            <div className="mt-2 text-center text-[12px] text-[#8f877e]">
              {playing ? 'Playing the cut with your take.' : guide ? 'The video plays quietly as a guide.' : 'The video plays without sound while you record.'}{' '}
              <span className="tc">{clock(time)}</span>
            </div>
          </div>

          {/* the teleprompter, or the review of a live take */}
          <div className="flex min-w-0 flex-col">
            <div className="mb-3 flex flex-wrap items-center gap-2" role="radiogroup" aria-label="How to record">
              {(['live', 'lines'] as const).map((m) => (
                <button
                  key={m}
                  type="button"
                  role="radio"
                  aria-checked={mode === m}
                  disabled={phase === 'recording' || phase === 'countin' || phase === 'placing' || (m === 'lines' && inScope.length === 0)}
                  onClick={() => (setMode(m), setPhase('setup'), setPlaced(null), setUntil(undefined))}
                  className={cx(
                    'h-8 rounded-full px-3 text-[12.5px] font-medium transition-colors disabled:opacity-40',
                    mode === m ? 'bg-[#e0a458] text-[#1c1307]' : 'bg-white/[0.06] text-[#b5aca2] hover:text-white',
                  )}
                >
                  {m === 'live' ? 'Live, while the video plays' : 'One line at a time'}
                </button>
              ))}
            </div>
            {problem ? (
              <p className="mb-3 flex items-start gap-2 rounded-[12px] bg-[#ef8d80]/10 px-3 py-2.5 text-[12.5px] leading-relaxed text-[#f2ece4]" role="alert">
                <WarningCircle size={14} weight="fill" className="mt-[2px] shrink-0 text-[#ef8d80]" />
                {problem}
              </p>
            ) : null}

            {mode === 'live' && phase === 'review' && placed ? (
              <TakeReview
                out={placed}
                kept={kept}
                busy={busy}
                show={after}
                plan={plan}
                duration={duration}
                playing={playing}
                onKeep={() => void keepLive()}
                onSnap={(w) => void snap(w)}
                onAgain={again}
                onDone={() => void finish()}
                onFix={(b, s) => void fix(b, s)}
                onPlay={() => void playBack()}
              />
            ) : mode === 'live' && phase === 'placing' && placing ? (
              <PlacingTake placing={placing} />
            ) : mode === 'live' ? (
              <LivePrompter
                lines={prompt}
                due={due}
                time={time}
                from={phase === 'setup' ? from : recFrom.current}
                until={rangeEnd}
                recording={phase === 'recording'}
                counting={phase === 'countin'}
                count={count}
                plan={plan}
                duration={duration}
                timed={timed}
              />
            ) : (
              <LinesPrompter phase={phase} count={count} line={line} next={nextLine} lines={inScope} index={idx} elapsed={elapsed} windowSecs={windowSecs} />
            )}

            {/* controls */}
            {!(mode === 'live' && (phase === 'review' || phase === 'placing')) ? (
              <div className="mt-4 flex flex-wrap items-center gap-3">
                {phase === 'recording' ? (
                  <button
                    type="button"
                    onClick={() => void stop()}
                    data-autofocus
                    className="inline-flex h-12 items-center gap-2.5 rounded-full bg-[#ef8d80] px-5 text-[14.5px] font-semibold text-[#1a0d0a] hover:brightness-110"
                  >
                    <Stop size={16} weight="fill" /> Stop
                    <span className="tc text-[13px] font-medium opacity-80">{elapsed.toFixed(1)} s</span>
                  </button>
                ) : phase === 'finished' ? (
                  <Button variant="primary" size="lg" icon={<CheckCircle size={17} />} onClick={() => void finish()}>
                    Done
                  </Button>
                ) : (
                  <button
                    type="button"
                    data-autofocus
                    disabled={!mic || phase === 'countin' || (mode === 'lines' && !line)}
                    onClick={record}
                    className={cx(
                      'inline-flex h-12 items-center gap-2.5 rounded-full px-5 text-[14.5px] font-semibold transition-[filter,background-color] disabled:opacity-40',
                      phase === 'review' ? 'bg-white/[0.08] text-white hover:bg-white/[0.12]' : 'bg-[#e0a458] text-[#1c1307] hover:brightness-110',
                    )}
                  >
                    {opening ? <Spinner size={15} /> : <Record size={17} weight="fill" className={phase === 'review' ? 'text-[#ef8d80]' : 'text-[#b03b2b]'} />}
                    {phase === 'review'
                      ? 'Try again'
                      : mode === 'lines'
                        ? `Record line ${line?.line ?? ''}`
                        : reLine
                          ? `Record line ${reLine.line} again`
                          : `Record from ${clock(from)}`}
                  </button>
                )}
                {mode === 'lines' && phase === 'review' && current ? (
                  <Button variant="primary" size="lg" onClick={() => void keepLine()}>
                    {idx < inScope.length - 1 ? 'Keep it, next line' : 'Keep it'}
                  </Button>
                ) : null}
                {mode === 'live' && phase === 'setup' && lines.length ? (
                  <label className="flex items-center gap-2 text-[12.5px] text-[#a69d93]">
                    Re-record
                    <select
                      value={reLine?.id ?? ''}
                      onChange={(e) => reRecord(e.target.value)}
                      className="h-8 max-w-[220px] rounded-[9px] bg-white/[0.06] px-2 text-[12.5px] text-[#f2ece4]"
                      aria-label="Re-record one line (only its stretch is replaced)"
                    >
                      <option value="">from the playhead on</option>
                      {lines.map((l) => (
                        <option key={l.id} value={l.id}>
                          only line {l.line}
                        </option>
                      ))}
                    </select>
                  </label>
                ) : null}
                {mode === 'lines' && (phase === 'setup' || phase === 'review') ? (
                  <div className="ml-auto flex items-center gap-1">
                    <button
                      type="button"
                      disabled={idx === 0}
                      onClick={() => (setIdx(idx - 1), setPhase('setup'))}
                      aria-label="Previous line"
                      className="grid h-9 w-9 place-items-center rounded-[10px] text-[#b5aca2] hover:bg-white/[0.06] disabled:opacity-30"
                    >
                      <ArrowLeft size={16} />
                    </button>
                    <span className="tc text-[12.5px] text-[#a69d93]">
                      {idx + 1} / {inScope.length}
                    </span>
                    <button
                      type="button"
                      disabled={idx >= inScope.length - 1}
                      onClick={() => (setIdx(idx + 1), setPhase('setup'))}
                      aria-label="Next line"
                      className="grid h-9 w-9 place-items-center rounded-[10px] text-[#b5aca2] hover:bg-white/[0.06] disabled:opacity-30"
                    >
                      <ArrowRight size={16} />
                    </button>
                  </div>
                ) : null}
              </div>
            ) : null}
            <p className="mt-2 hidden text-[11.5px] text-[#8f877e] sm:block">
              {mode === 'live' ? (
                phase === 'recording' ? (
                  <>
                    <Kbd>Space</Kbd> or a clicker: next line now · <Kbd>Enter</Kbd> stop
                  </>
                ) : (
                  <>
                    Move the video to where you want to start, then <Kbd>Space</Kbd>. Your take lands exactly where you speak.
                  </>
                )
              ) : (
                <>
                  <Kbd>Space</Kbd> record or stop · <Kbd>Enter</Kbd> keep the take · ← → change line
                </>
              )}
            </p>
            {mode === 'lines' ? <TakeList takes={takes.filter((t) => t.lineId === line?.id)} earlier={line?.takeCount ?? 0} /> : null}
            {mode === 'lines' && phase === 'finished' ? <p className="mt-3 text-[13px] text-[#b5aca2]">Every line has a take. Closing puts the last ones in the cut.</p> : null}
          </div>
        </div>
      )}
    </div>,
    document.body,
  );
}

/** Where playback starts to re-record one line: a second before it (the line before keeps its words; only the new take's sound replaces it). */
function reStartOf(l: { start: number }): number {
  return Math.max(0, l.start - 1);
}

function linesOverClip(lines: NarrationLine[], plan: RenderPlan | undefined, clipId: string): NarrationLine[] {
  const c = plan?.clips.find((x) => x.id === clipId);
  if (!c || !plan) return lines.filter((l) => l.anchor.clipId === clipId);
  return lines.filter((l) => {
    const f = Math.round(l.start * plan.fps);
    return f >= c.from && f < c.from + c.durationInFrames;
  });
}

/** While a live take is saved, listened to and placed. */
function PlacingTake({ placing }: { placing: Placing }) {
  const steps: { key: Placing['step']; label: string }[] = [
    { key: 'upload', label: 'Saving your take in the project' },
    { key: 'listen', label: placing.waiting ? 'Waiting for this computer to listen to it (it is busy)' : 'Listening to it' },
    { key: 'place', label: 'Putting each line where you said it' },
  ];
  const at = steps.findIndex((s) => s.key === placing.step);
  return (
    <div className="flex flex-1 flex-col justify-center rounded-[18px] bg-white/[0.035] p-6 ring-1 ring-inset ring-white/[0.06]" role="status" aria-live="polite" data-placing={placing.step}>
      <ol className="space-y-2.5">
        {steps.map((s, i) => (
          <li key={s.key} className={cx('flex items-center gap-2.5 text-[14px]', i <= at ? 'text-white' : 'text-white/35')}>
            {i < at ? <CheckCircle size={16} weight="fill" className="text-[#92c79c]" /> : i === at ? <Spinner size={14} /> : <span className="h-4 w-4 rounded-full border border-white/25" />}
            {s.label}
          </li>
        ))}
      </ol>
      {placing.step === 'upload' ? <Progress className="mt-4" value={placing.progress} label="Saving" /> : null}
    </div>
  );
}

/** One line at a time: the line to say large, the next one dimmed, how long its spot is. */
function LinesPrompter({ phase, count, line, next, lines, index, elapsed, windowSecs }: {
  phase: Phase;
  count: number;
  line?: NarrationLine;
  next?: NarrationLine;
  lines: NarrationLine[];
  index: number;
  elapsed: number;
  windowSecs: number;
}) {
  return (
    <div className="relative flex min-h-[240px] flex-1 flex-col justify-center overflow-hidden rounded-[18px] bg-white/[0.035] p-5 ring-1 ring-inset ring-white/[0.06] sm:p-7" aria-live="polite">
      {phase === 'countin' ? (
        <div className="absolute inset-0 z-10 grid place-items-center bg-black/55 backdrop-blur-[2px]" role="status" aria-label={`Recording in ${count}`}>
          <span key={count} className="fade-up tc text-[96px] font-semibold leading-none text-white">
            {count}
          </span>
        </div>
      ) : null}
      {phase === 'recording' ? (
        <div className="absolute right-4 top-4 flex items-center gap-1.5 text-[12px] font-medium text-[#ef8d80]">
          <span className="h-2.5 w-2.5 animate-pulse rounded-full bg-[#ef8d80]" /> Recording
        </div>
      ) : null}
      {line ? (
        <>
          <div className="mb-2 text-[12px] font-medium uppercase tracking-[0.08em] text-[#8f877e]">
            Line {line.line}
            {lines.length > 1 && (lines[0]?.line !== 1 || lines.length !== lines.at(-1)?.line) ? ` · ${index + 1} of ${lines.length} here` : lines.length > 1 ? ` of ${lines.length}` : ''}
            {windowSecs ? ` · about ${windowSecs.toFixed(1)} seconds to read it` : ''}
          </div>
          {line.delivery ? <div className="mb-2 text-[14px] italic text-[#e8b169]">({line.delivery})</div> : null}
          <p className="text-[clamp(22px,3.2vw,36px)] font-medium leading-[1.3] tracking-[-0.015em] text-white">{line.text}</p>
          {phase === 'recording' && windowSecs ? (
            <div className="mt-5 h-1.5 overflow-hidden rounded-full bg-white/10" aria-hidden>
              <div
                className={cx('h-full rounded-full transition-[width] duration-100', elapsed > windowSecs ? 'bg-[#ef8d80]' : 'bg-[#e0a458]')}
                style={{ width: `${Math.min(100, (elapsed / windowSecs) * 100)}%` }}
              />
            </div>
          ) : null}
          {phase === 'recording' && elapsed > windowSecs + 0.3 ? <p className="mt-2 text-[12.5px] text-[#ef8d80]">Longer than its spot; that is fine, MetaRush suggests a fix after.</p> : null}
          {next ? <p className="mt-6 text-[17px] leading-relaxed text-white/35">Next: {next.text}</p> : null}
        </>
      ) : (
        <p className="text-[15px] text-[#b5aca2]">No line to record here.</p>
      )}
    </div>
  );
}

function TakeList({ takes, earlier }: { takes: LocalTake[]; earlier: number }) {
  const [playing, setPlaying] = useState<string | null>(null);
  const audio = useRef<HTMLAudioElement | null>(null);
  useEffect(
    () => () => {
      audio.current?.pause();
    },
    [],
  );
  if (!takes.length) return earlier ? <p className="mt-4 text-[12.5px] text-[#8f877e]">{earlier === 1 ? 'An earlier take of this line is' : `${earlier} earlier takes of this line are`} kept in the project.</p> : null;
  const play = (t: LocalTake) => {
    const a = audio.current ?? (audio.current = new Audio());
    if (playing === t.key) {
      a.pause();
      setPlaying(null);
      return;
    }
    a.src = t.url;
    a.currentTime = t.range.in;
    a.ontimeupdate = () => a.currentTime >= t.range.out && (a.pause(), setPlaying(null));
    a.onended = () => setPlaying(null);
    void a.play().catch(() => setPlaying(null));
    setPlaying(t.key);
  };
  return (
    <ul className="mt-4 space-y-1.5" aria-label="Takes recorded now">
      {takes.map((t) => (
        <li key={t.key} className="flex flex-wrap items-center gap-2.5 rounded-[12px] bg-white/[0.04] px-3 py-2 text-[12.5px]">
          <button
            type="button"
            onClick={() => play(t)}
            aria-label={playing === t.key ? `Stop take ${t.n}` : `Play take ${t.n}`}
            className={cx('grid h-8 w-8 place-items-center rounded-full', playing === t.key ? 'bg-[#e0a458] text-[#1c1307]' : 'bg-white/[0.08] text-white hover:bg-white/[0.14]')}
          >
            {playing === t.key ? <Stop size={11} weight="fill" /> : <Play size={11} weight="fill" />}
          </button>
          <span className="font-medium text-white">Take {t.n}</span>
          <span className="tc text-[#a69d93]">{(t.range.out - t.range.in).toFixed(1)} s</span>
          {t.quiet ? <span className="text-[#e8b169]">Very quiet: is the right microphone chosen?</span> : null}
          <span className="ml-auto flex items-center gap-1.5 text-[#a69d93]">
            {t.state === 'saving' ? (
              <>
                <Spinner size={11} /> Saving
              </>
            ) : t.state === 'failed' ? (
              <span className="flex items-center gap-1 text-[#ef8d80]" title={t.error}>
                <WarningCircle size={13} weight="fill" /> Not saved: {t.error}
              </span>
            ) : t.state === 'kept' ? (
              <span className="flex items-center gap-1 text-[#92c79c]">
                <CheckCircle size={13} weight="fill" /> In the cut
              </span>
            ) : (
              'Saved'
            )}
          </span>
        </li>
      ))}
    </ul>
  );
}

function MicHelp({ error, busy, devices, deviceId, onPick, onRetry, onClose }: {
  error: MicError;
  busy: boolean;
  devices: MicDevice[];
  deviceId?: string;
  onPick: (id: string) => void;
  onRetry: () => void;
  onClose: () => void;
}) {
  const mac = /Mac/i.test(navigator.platform || navigator.userAgent);
  const host = useHost();
  const body: Record<string, string[]> = {
    denied: [
      'MetaRush is not allowed to use your microphone yet.',
      'Click the icon at the left of the address bar (a lock or sliders), set Microphone to Allow for this page, then press Try again.',
      ...(mac ? ['If it is still blocked, open System Settings → Privacy & Security → Microphone and turn it on for your browser.'] : []),
    ],
    missing: ['No microphone was found.', 'Plug one in or turn it on, then press Try again.'],
    busy: ['Another app is using the microphone.', 'Close it (video calls often keep it), then press Try again.'],
    insecure: host.remote
      ? [
          'The microphone only works on a secure (https) page.',
          host.publicUrl?.startsWith('https://')
            ? `Open MetaRush at ${host.publicUrl} and try again.`
            : `Ask the person who runs MetaRush on ${host.machine} to turn on HTTPS certificates for your Tailscale network (Settings → People → Remote access says how).`,
        ]
      : ['The microphone only works when MetaRush is opened from this computer.', 'Open the dashboard with its own link from this computer and try again.'],
    unsupported: ['This browser cannot record from the microphone.', 'Use a current version of Chrome, Edge, Firefox or Safari.'],
    noanswer: [
      "The microphone didn't answer.",
      "It may be a virtual microphone whose app isn't running (a remote-desktop or video-call app's). Choose another microphone, or open that app, then press Try again.",
    ],
    other: ['The microphone could not be started.', error.message],
  };
  const [first, ...rest] = body[error.kind] ?? body.other!;
  return (
    <div className="grid flex-1 place-items-center p-6">
      <div className="max-w-[520px] rounded-[18px] bg-white/[0.04] p-6 ring-1 ring-inset ring-white/[0.07]" role="alert">
        <div className="flex items-start gap-3">
          <WarningCircle size={22} weight="fill" className="mt-[1px] shrink-0 text-[#e0a458]" />
          <div>
            <h3 className="text-[16px] font-semibold text-white">{first}</h3>
            {rest.map((r) => (
              <p key={r} className="mt-2 text-[13.5px] leading-relaxed text-[#b5aca2]">
                {r}
              </p>
            ))}
            {devices.length > 1 && (error.kind === 'noanswer' || error.kind === 'busy' || error.kind === 'missing') ? (
              <label className="mt-4 flex items-center gap-2 text-[12.5px] text-[#a69d93]">
                Microphone
                <select
                  value={deviceId ?? ''}
                  onChange={(e) => onPick(e.target.value)}
                  className="h-8 max-w-[260px] rounded-[9px] bg-white/[0.06] px-2 text-[12.5px] text-[#f2ece4]"
                  data-mic-picker
                >
                  <option value="">The computer's default</option>
                  {devices.map((d) => (
                    <option key={d.id} value={d.id}>
                      {d.label}
                    </option>
                  ))}
                </select>
              </label>
            ) : null}
            <p className="mt-3 text-[13px] text-[#8f877e]">You can also add a recording you made elsewhere, or use an AI voice.</p>
            <div className="mt-5 flex flex-wrap gap-2">
              <Button variant="primary" loading={busy} onClick={onRetry} data-autofocus>
                Try again
              </Button>
              <Button variant="ghost" onClick={onClose} className="text-[#b5aca2] hover:text-white">
                Close
              </Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
