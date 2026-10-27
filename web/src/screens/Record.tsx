import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useNavigate, useSearchParams } from 'react-router';
import { Camera, CheckCircle, Desktop, Microphone, Pause, Play, PictureInPicture, Record as RecordIcon, Stop, WarningCircle, X } from '@phosphor-icons/react';
import { call, toApiError } from '../api/client';
import { useOp, useSession } from '../api/queries';
import type { RecordingInfo } from '../api/types';
import { useHost } from '../lib/host';
import { useDialog } from '../ui/Dialog';
import { Button, cx, Kbd, Spinner } from '../ui/primitives';
import { ProjectPicker } from '../components/ProjectPicker';
import { listMics, Mic, micProblem, type MicDevice } from './studio/voiceover/capture';
import { LevelMeter } from './studio/voiceover/LevelMeter';
import { advance, nextToBring, schedule } from './studio/voiceover/prompter';
import { canFloat, openFloat } from './record/float';
import { ScreenPrompter } from './record/Prompter';
import { LAYOUT_WORDS, layoutsFor, RecordingsList, useStartCut, type CutLayout } from './record/Recordings';
import { promptLines, splitScript } from './record/script';
import { Refused } from './record/pieces';
import { finishOnHost, isGone, RecordingSession, restoreQueues } from './record/session';
import { listCameras, openCamera, openScreen, problemWords, screenProblem, SourceError, sourceProblem, surfaceOf, type CameraDevice } from './record/sources';
import { available, forget, pending as pendingRecordings, type Pending } from './record/store';

/*
 * Record your screen (D258–D262): the screen (a window, a tab or a whole
 * screen), the camera and the microphone, recorded in this browser as
 * separate files on one clock and sent to the computer that runs MetaRush
 * while recording, with the teleprompter showing the script. Stopped, it
 * becomes footage (one conversation in the project), and a cut can start
 * from it with the camera in a corner or side by side.
 */

type Phase = 'setup' | 'countin' | 'recording' | 'sending' | 'finishing' | 'done' | 'failed';
type ScriptMode = 'none' | 'project' | 'paste';

const defaultName = () =>
  `Screen recording, ${new Date().toLocaleString(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })}`;

export function RecordPage() {
  const [params] = useSearchParams();
  const projectId = params.get('project') ?? undefined;
  const navigate = useNavigate();
  const host = useHost();
  const userId = useSession().data?.user.id ?? '';
  const mac = /Mac/i.test(navigator.platform || navigator.userAgent);
  const project = useOp('project.show', projectId ? { id: projectId } : null);
  const head = project.data?.headRevisionId;
  const narration = useOp('voiceover.show', head ? { revisionId: head } : null, { staleTime: 60_000 });
  const projectLines = useMemo(() => (narration.data?.beats ?? []).map((b) => (b.delivery ? `(${b.delivery}) ${b.text}` : b.text)), [narration.data]);

  /* ----- sources ----- */
  const [screen, setScreen] = useState<MediaStream | null>(null);
  const [camera, setCamera] = useState<MediaStream | null>(null);
  const [cameraId, setCameraId] = useState('');
  const [cameras, setCameras] = useState<CameraDevice[]>([]);
  const [mic, setMic] = useState<Mic | null>(null);
  const [micId, setMicId] = useState('');
  const [mics, setMics] = useState<MicDevice[]>([]);
  const [micOff, setMicOff] = useState(false);
  const [opening, setOpening] = useState<string | null>(null);
  const [problems, setProblems] = useState<Partial<Record<'screen' | 'camera' | 'microphone', SourceError>>>({});
  const [layout, setLayout] = useState<CutLayout>('pip');
  const [scriptMode, setScriptMode] = useState<ScriptMode>('none');
  const [pasted, setPasted] = useState('');
  const [floatIt, setFloatIt] = useState(canFloat());
  const [name, setName] = useState(defaultName);
  const [storage, setStorage] = useState(true);
  const live = useRef({ screen, camera, mic });
  live.current = { screen, camera, mic };

  const problem = (source: 'screen' | 'camera' | 'microphone', e: SourceError | null) => setProblems((p) => ({ ...p, [source]: e ?? undefined }));
  const ctx = { mac, remote: host.remote, machine: host.machine, publicUrl: host.publicUrl };

  const openMic = useCallback(async (id?: string) => {
    setOpening('microphone');
    problem('microphone', null);
    try {
      const m = await Mic.open(id || undefined);
      setMic((old) => {
        old?.close();
        return m;
      });
      setMicId(m.deviceId ?? '');
    } catch (e) {
      const me = micProblem(e);
      problem('microphone', new SourceError('microphone', me.kind === 'noanswer' ? 'noanswer' : me.kind, me.message));
    } finally {
      setMics(await listMics().catch(() => []));
      setOpening(null);
    }
  }, []);
  useEffect(() => {
    void openMic();
    void available().then(setStorage);
    void listCameras()
      .then(setCameras)
      .catch(() => undefined);
    return () => {
      const { screen: s, camera: c, mic: m } = live.current;
      s?.getTracks().forEach((t) => t.stop());
      c?.getTracks().forEach((t) => t.stop());
      m?.close();
    };
  }, [openMic]);

  const chooseScreen = async () => {
    setOpening('screen');
    problem('screen', null);
    try {
      const s = await openScreen();
      screen?.getTracks().forEach((t) => t.stop());
      setScreen(s);
      s.getVideoTracks()[0]?.addEventListener('ended', () => setScreen((cur) => (cur === s ? null : cur)));
    } catch (e) {
      problem('screen', sourceProblem('screen', e));
    } finally {
      setOpening(null);
    }
  };
  const chooseCamera = async (id: string) => {
    setCameraId(id);
    camera?.getTracks().forEach((t) => t.stop());
    setCamera(null);
    problem('camera', null);
    if (!id) return;
    setOpening('camera');
    try {
      const s = await openCamera(id === 'default' ? undefined : id);
      setCamera(s);
      setCameras(await listCameras().catch(() => cameras));
    } catch (e) {
      problem('camera', sourceProblem('camera', e));
    } finally {
      setOpening(null);
    }
  };

  /* ----- the teleprompter ----- */
  useEffect(() => {
    if (projectLines.length && scriptMode === 'none' && !pasted) setScriptMode('project');
  }, [projectLines.length]); // eslint-disable-line react-hooks/exhaustive-deps
  const scriptText = scriptMode === 'paste' ? splitScript(pasted) : scriptMode === 'project' ? projectLines : [];
  const lines = useMemo(() => promptLines(scriptText), [scriptText.join('\n')]); // eslint-disable-line react-hooks/exhaustive-deps
  const [due, setDue] = useState<number[]>([]);
  const [pip, setPip] = useState<Window | null>(null);

  /* ----- recording ----- */
  const [phase, setPhase] = useState<Phase>('setup');
  const [count, setCount] = useState(3);
  const session = useRef<RecordingSession | null>(null);
  const [, setTick] = useState(0);
  const [notice, setNotice] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [result, setResult] = useState<RecordingInfo | null>(null);
  const release = useRef<(() => void) | null>(null);
  const s = session.current;
  const time = s ? s.elapsed() : 0;
  const surface = surfaceOf(screen);
  const hasPicture = !!screen || !!camera;
  const canRecord = (!!screen || !!camera || (!!mic && !micOff)) && !opening;

  const record = async () => {
    if (!canRecord || phase !== 'setup') return;
    setFailure(null);
    setNotice(null);
    mic?.resume();
    if (floatIt && canFloat() && lines.length && !pip) {
      try {
        const w = await openFloat();
        if (w) {
          w.addEventListener('pagehide', () => setPip(null));
          setPip(w);
        }
      } catch {
        /* the teleprompter stays here */
      }
    }
    setCount(3);
    setPhase('countin');
  };
  const start = async () => {
    try {
      const rec = await RecordingSession.start({
        userId,
        name: name.trim() || defaultName(),
        projectId,
        layout: screen && camera ? layout : screen ? 'screen' : camera ? 'camera' : undefined,
        script: lines.length ? { source: scriptMode === 'project' ? 'project' : 'pasted', lines: lines.map((l) => l.text), ...(scriptMode === 'project' && head ? { revisionId: head } : {}) } : undefined,
        sources: { screen: screen ?? undefined, camera: camera ?? undefined, mic: mic && !micOff ? mic : undefined },
        onSourceEnded: (t) => {
          setNotice(
            t === 'screen'
              ? 'Screen sharing ended, so the recording stopped. Everything recorded until then is kept.'
              : t === 'camera'
                ? 'The camera went away, so the recording stopped. Everything recorded until then is kept.'
                : 'The microphone went away, so the recording stopped. Everything recorded until then is kept.',
          );
          void stop('track');
        },
        onProblem: (m) => setNotice(m),
        onRefused: (m) => {
          // MetaRush takes no more (the recording reached its limit, or was ended elsewhere): stop and keep what arrived.
          setNotice(`${m.replace(/\.$/, '')}. The recording stopped; what arrived is kept.`);
          void stop('track');
        },
      });
      session.current = rec;
      void navigator.locks?.request(`metarush-recording-${rec.id}`, () => new Promise<void>((r) => (release.current = r)));
      setDue(schedule(lines, 0));
      setPhase('recording');
    } catch (e) {
      setFailure(toApiError(e).message);
      setPhase('setup');
      pip?.close();
    }
  };
  useEffect(() => {
    if (phase !== 'countin') return;
    if (count === 0) {
      void start();
      return;
    }
    const t = setTimeout(() => setCount((c) => c - 1), 1000);
    return () => clearTimeout(t);
  }, [phase, count]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (phase !== 'recording' && phase !== 'sending') return;
    const t = setInterval(() => setTick((n) => n + 1), 200);
    return () => clearInterval(t);
  }, [phase]);

  const next = useCallback(() => {
    const cur = session.current;
    if (!cur || cur.paused) return;
    const t = cur.elapsed();
    setDue((d) => {
      const k = nextToBring(d, t);
      return k >= 0 ? advance(d, lines, k, t) : d;
    });
  }, [lines]);
  const togglePause = () => {
    const cur = session.current;
    if (!cur) return;
    if (cur.paused) {
      // The teleprompter waits too: its lines move by the pause.
      cur.resume();
    } else cur.pause();
    setTick((n) => n + 1);
  };
  const finishing = useRef(false);
  const stop = async (ended: 'stopped' | 'track' = 'stopped') => {
    const cur = session.current;
    if (!cur || finishing.current) return;
    finishing.current = true;
    setPhase('sending');
    try {
      await cur.stop(ended);
      let keep = false;
      await cur.sent().catch((e: unknown) => {
        if (!(e instanceof Refused)) throw e;
        keep = true; // MetaRush takes no more of it: keep what arrived
      });
      setPhase('finishing');
      setResult(await cur.finish(keep));
      setPhase('done');
      release.current?.();
    } catch (e) {
      setFailure(toApiError(e).message);
      setPhase('failed');
    } finally {
      finishing.current = false;
      pip?.close();
    }
  };
  const tryAgain = async () => {
    const cur = session.current;
    if (!cur) return;
    setFailure(null);
    for (const q of cur.queues.values()) q.retryNow();
    await stop(); // it carries on: sending what's left, then making it footage
  };
  const pipRef = useRef<Window | null>(null);
  pipRef.current = pip;
  useEffect(
    () => () => {
      session.current?.close();
      pipRef.current?.close(); // the floating teleprompter goes with the page
    },
    [],
  );

  // Space, an arrow or a clicker: the next line; P pauses; Enter stops (when this window has focus).
  useEffect(() => {
    if (phase !== 'recording') return;
    const onKey = (e: KeyboardEvent) => {
      if (document.querySelector('[data-mr-dialog="open"]')) return;
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT')) return;
      if (e.key === ' ' || e.key === 'ArrowRight' || e.key === 'ArrowDown' || e.key === 'PageDown') {
        e.preventDefault();
        next();
      } else if (e.key === 'p' || e.key === 'P') {
        e.preventDefault();
        togglePause();
      } else if (e.key === 'Enter') {
        e.preventDefault();
        void stop();
      }
    };
    window.addEventListener('keydown', onKey, true);
    pip?.addEventListener('keydown', onKey, true);
    return () => {
      window.removeEventListener('keydown', onKey, true);
      pip?.removeEventListener('keydown', onKey, true);
    };
  });

  const close = () => navigate(projectId ? `/p/${projectId}?tab=sources` : '/library');
  const again = () => {
    session.current = null;
    setResult(null);
    setFailure(null);
    setNotice(null);
    setName(defaultName());
    setPhase('setup');
  };

  const recording = phase === 'recording' || phase === 'countin';
  const prompter = (compact: boolean) => (
    <ScreenPrompter lines={lines} due={due} time={time} recording={phase === 'recording'} paused={!!s?.paused} onNext={next} compact={compact} />
  );

  return createPortal(
    <div role="dialog" aria-modal="true" aria-label="Record your screen" className="fixed inset-0 z-[75] flex flex-col bg-[#0b0a09] text-[#f2ece4]" data-mr-screen-recorder data-phase={phase}>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-white/[0.07] px-4 py-3 sm:px-6">
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <Desktop size={18} className="text-[#e0a458]" aria-hidden />
          <h2 className="truncate text-[15px] font-semibold">Record your screen</h2>
          <span className="truncate text-[12.5px] text-[#a69d93]">{projectId ? `for ${project.data?.project.name ?? 'the project'}` : 'into the Inbox'}</span>
        </div>
        {phase === 'recording' && s ? (
          <div className="flex items-center gap-3" data-recording-status>
            <span className={cx('flex items-center gap-1.5 text-[12.5px] font-medium', s.paused ? 'text-[#e8b169]' : 'text-[#ef8d80]')}>
              <span className={cx('h-2.5 w-2.5 rounded-full', s.paused ? 'bg-[#e8b169]' : 'animate-pulse bg-[#ef8d80]')} /> {s.paused ? 'Paused' : 'Recording'}
            </span>
            <span className="tc text-[13px] text-white" data-elapsed={time.toFixed(1)}>
              {clockText(time)}
            </span>
          </div>
        ) : null}
        <button
          type="button"
          onClick={close}
          disabled={recording || phase === 'sending' || phase === 'finishing'}
          aria-label="Close the recorder"
          className="grid h-9 w-9 shrink-0 place-items-center rounded-[10px] text-[#a69d93] hover:bg-white/[0.06] hover:text-white disabled:opacity-30"
        >
          <X size={17} />
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6">
        {phase === 'setup' || phase === 'countin' ? (
          <>
            <Unfinished exclude={s?.id} userId={userId} />
            <div className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(320px,0.9fr)]">
              <div className="min-w-0 space-y-4">
                <SourceRow icon={<Desktop size={16} />} title="Screen" problem={problems.screen} ctx={ctx}>
                  {screen ? (
                    <div className="flex flex-wrap items-center gap-3">
                      <Preview stream={screen} className="h-[90px] w-[160px]" />
                      <div className="min-w-0 flex-1 text-[13px] text-[#e9e2d9]">
                        <div data-surface={surface ?? ''}>{surface === 'monitor' ? 'A whole screen' : surface === 'browser' ? 'A browser tab' : surface === 'window' ? 'A window' : 'Shared'}</div>
                        <div className="truncate text-[12px] text-[#a69d93]">{screen.getVideoTracks()[0]?.label}</div>
                        {screen.getAudioTracks().length ? <div className="text-[12px] text-[#92c79c]">Its sound is recorded too.</div> : null}
                      </div>
                      <Button size="sm" variant="ghost" onClick={() => void chooseScreen()} disabled={!!opening}>
                        Change
                      </Button>
                      <Button size="sm" variant="ghost" onClick={() => (screen.getTracks().forEach((t) => t.stop()), setScreen(null))}>
                        Don’t record it
                      </Button>
                    </div>
                  ) : (
                    <Button variant="secondary" onClick={() => void chooseScreen()} loading={opening === 'screen'} data-choose-screen>
                      Choose a window, tab or screen…
                    </Button>
                  )}
                  {surface === 'monitor' ? (
                    <Note tone="warn">
                      You’re recording a whole screen, so the teleprompter shows in the recording if this window is on that screen. Record a window instead, or move this window (or the floating
                      teleprompter) to another screen.
                    </Note>
                  ) : screen ? (
                    <Note>Only what you shared is recorded: this window and the teleprompter stay out of it.</Note>
                  ) : screenProblem() ? null : (
                    <Note>A window keeps the teleprompter out of the recording; this tab is never offered.</Note>
                  )}
                </SourceRow>
                <SourceRow icon={<Camera size={16} />} title="Camera" problem={problems.camera} ctx={ctx}>
                  <div className="flex flex-wrap items-center gap-3">
                    <select
                      value={cameraId}
                      onChange={(e) => void chooseCamera(e.target.value)}
                      disabled={!!opening}
                      className="h-9 max-w-[260px] rounded-[10px] bg-white/[0.06] px-2 text-[13px] text-[#f2ece4]"
                      aria-label="Camera"
                      data-camera-picker
                    >
                      <option value="">Off</option>
                      {cameras.length ? cameras.map((c) => <option key={c.id} value={c.id || 'default'}>{c.label}</option>) : <option value="default">The computer’s camera</option>}
                    </select>
                    {opening === 'camera' ? <Spinner size={14} /> : null}
                    {camera ? <Preview stream={camera} className="h-[72px] w-[96px] rounded-full" mirror /> : null}
                  </div>
                </SourceRow>
                <SourceRow icon={<Microphone size={16} />} title="Microphone" problem={problems.microphone} ctx={ctx} onRetry={() => void openMic(micId)}>
                  <div className="flex flex-wrap items-center gap-3">
                    <select
                      value={micOff ? 'off' : micId}
                      onChange={(e) => {
                        if (e.target.value === 'off') {
                          setMicOff(true);
                          return;
                        }
                        setMicOff(false);
                        setMicId(e.target.value);
                        void openMic(e.target.value);
                      }}
                      disabled={!!opening}
                      className="h-9 max-w-[260px] rounded-[10px] bg-white/[0.06] px-2 text-[13px] text-[#f2ece4]"
                      aria-label="Microphone"
                      data-mic-picker
                    >
                      <option value="">The computer’s default</option>
                      {mics.map((m) => (
                        <option key={m.id} value={m.id}>
                          {m.label}
                        </option>
                      ))}
                      <option value="off">No microphone</option>
                    </select>
                    {opening === 'microphone' ? <Spinner size={14} /> : null}
                    {mic && !micOff ? <LevelMeter mic={mic} /> : null}
                  </div>
                </SourceRow>
                {screen && camera ? (
                  <div className="rounded-[16px] bg-white/[0.03] p-4 ring-1 ring-inset ring-white/[0.06]">
                    <div className="mb-2 text-[13px] font-medium text-white">How the first cut shows them</div>
                    <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Layout">
                      {(['pip', 'side-by-side', 'screen'] as CutLayout[]).map((l) => (
                        <button
                          key={l}
                          type="button"
                          role="radio"
                          aria-checked={layout === l}
                          onClick={() => setLayout(l)}
                          className={cx('h-8 rounded-full px-3 text-[12.5px] font-medium', layout === l ? 'bg-[#e0a458] text-[#1c1307]' : 'bg-white/[0.06] text-[#b5aca2] hover:text-white')}
                        >
                          {LAYOUT_WORDS[l]}
                        </button>
                      ))}
                    </div>
                    <p className="mt-2 text-[12px] text-[#8f877e]">The screen, the camera and your voice are kept apart, so you can change this in any cut.</p>
                  </div>
                ) : null}
              </div>

              <div className="flex min-w-0 flex-col gap-4">
                <div className="rounded-[16px] bg-white/[0.03] p-4 ring-1 ring-inset ring-white/[0.06]">
                  <div className="mb-2 text-[13px] font-medium text-white">Teleprompter</div>
                  <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Script">
                    {(['none', 'project', 'paste'] as ScriptMode[]).map((m) => (
                      <button
                        key={m}
                        type="button"
                        role="radio"
                        aria-checked={scriptMode === m}
                        disabled={m === 'project' && !projectLines.length}
                        onClick={() => setScriptMode(m)}
                        className={cx('h-8 rounded-full px-3 text-[12.5px] font-medium disabled:opacity-35', scriptMode === m ? 'bg-[#e0a458] text-[#1c1307]' : 'bg-white/[0.06] text-[#b5aca2] hover:text-white')}
                        title={m === 'project' && !projectLines.length ? 'This project’s cut has no narration script yet.' : undefined}
                      >
                        {m === 'none' ? 'No script' : m === 'project' ? 'This project’s script' : 'Paste a script'}
                      </button>
                    ))}
                  </div>
                  {scriptMode === 'paste' ? (
                    <textarea
                      value={pasted}
                      onChange={(e) => setPasted(e.target.value)}
                      rows={6}
                      placeholder="Paste or type what you’ll say. Each paragraph is a line on the teleprompter."
                      className="mt-3 w-full rounded-[12px] bg-white/[0.05] p-3 text-[13.5px] leading-relaxed text-[#f2ece4] placeholder:text-[#8f877e]"
                      aria-label="Script"
                      data-script
                    />
                  ) : null}
                  {lines.length ? (
                    <p className="mt-2 text-[12px] text-[#a69d93]" data-script-lines={lines.length}>
                      {lines.length === 1 ? 'One line' : `${lines.length} lines`}, about {clockText(lines.reduce((a, l) => a + l.dur + 0.3, 1))} at an easy pace. Lines come one after another; Next line (or Space, an arrow key or a clicker here) brings the next one early.
                    </p>
                  ) : null}
                  {canFloat() && lines.length ? (
                    <label className="mt-3 flex items-center gap-2 text-[12.5px] text-[#b5aca2]">
                      <input type="checkbox" checked={floatIt} onChange={(e) => setFloatIt(e.target.checked)} className="h-4 w-4 accent-[#e0a458]" />
                      <PictureInPicture size={14} aria-hidden /> Float it above other windows while recording
                    </label>
                  ) : null}
                </div>
                <label className="block rounded-[16px] bg-white/[0.03] p-4 ring-1 ring-inset ring-white/[0.06]">
                  <span className="mb-1.5 block text-[13px] font-medium text-white">Name</span>
                  <input value={name} onChange={(e) => setName(e.target.value)} maxLength={120} className="h-9 w-full rounded-[10px] bg-white/[0.05] px-3 text-[13.5px] text-[#f2ece4]" data-recording-name />
                </label>
                {host.remote ? <Note>You record in this browser; it is sent to {host.machine} as you go.</Note> : null}
                {!storage ? <Note tone="warn">This browser can’t keep the recording while it is sent (a private window?): keep this tab open until it says it’s in the Library.</Note> : null}
                {failure ? <Note tone="error">{failure}</Note> : null}
                <div className="flex flex-wrap items-center gap-3">
                  <button
                    type="button"
                    onClick={() => void record()}
                    disabled={!canRecord || phase !== 'setup'}
                    className="inline-flex h-12 items-center gap-2.5 rounded-full bg-[#e0a458] px-6 text-[14.5px] font-semibold text-[#1c1307] hover:brightness-110 disabled:opacity-40"
                    data-record
                  >
                    <RecordIcon size={17} weight="fill" className="text-[#b03b2b]" /> Record
                  </button>
                  <span className="text-[12px] text-[#8f877e]">
                    {!hasPicture && !mic ? 'Choose what to record.' : !hasPicture ? 'Only your voice is recorded.' : `Recording ${[screen && 'the screen', camera && 'the camera', mic && !micOff && 'your voice'].filter(Boolean).join(', ')}.`}
                  </span>
                </div>
              </div>
            </div>
            <RecordingsList projectId={projectId} exclude={s?.id} />
            {phase === 'countin' ? (
              <div className="fixed inset-0 z-10 grid place-items-center bg-black/60 backdrop-blur-[2px]" role="status" aria-label={`Recording in ${count}`} data-countdown={count}>
                <span key={count} className="fade-up tc text-[120px] font-semibold leading-none text-white">
                  {count}
                </span>
              </div>
            ) : null}
          </>
        ) : phase === 'recording' && s ? (
          <div className="grid h-full min-h-[420px] grid-cols-1 gap-5 lg:grid-cols-[minmax(0,1.6fr)_minmax(260px,0.8fr)]">
            <div className="flex min-h-[320px] min-w-0 flex-col">
              {pip ? (
                <div className="flex flex-1 flex-col items-center justify-center gap-3 rounded-[18px] bg-white/[0.03] p-6 text-center ring-1 ring-inset ring-white/[0.06]">
                  <PictureInPicture size={22} className="text-[#e0a458]" />
                  <p className="text-[14px] text-[#e9e2d9]">The teleprompter floats in its own window.</p>
                  <Button size="sm" variant="ghost" onClick={() => pip.close()}>
                    Bring it back here
                  </Button>
                </div>
              ) : (
                prompter(false)
              )}
              {pip ? createPortal(<div className="flex h-screen flex-col p-2">{prompter(true)}</div>, pip.document.body) : null}
              {notice ? <Note tone="warn">{notice}</Note> : null}
              <div className="mt-4 flex flex-wrap items-center gap-3">
                <button
                  type="button"
                  onClick={() => void stop()}
                  className="inline-flex h-12 items-center gap-2.5 rounded-full bg-[#ef8d80] px-5 text-[14.5px] font-semibold text-[#1a0d0a] hover:brightness-110"
                  data-stop
                >
                  <Stop size={16} weight="fill" /> Stop
                </button>
                <button
                  type="button"
                  onClick={togglePause}
                  className="inline-flex h-12 items-center gap-2 rounded-full bg-white/[0.08] px-5 text-[14px] font-medium text-white hover:bg-white/[0.12]"
                  data-pause
                >
                  {s.paused ? <Play size={15} weight="fill" /> : <Pause size={15} weight="fill" />} {s.paused ? 'Resume' : 'Pause'}
                </button>
                <p className="hidden text-[11.5px] text-[#8f877e] sm:block">
                  <Kbd>Space</Kbd> next line · <Kbd>P</Kbd> pause · <Kbd>Enter</Kbd> stop
                </p>
              </div>
            </div>
            <div className="flex min-w-0 flex-col gap-3">
              {camera ? <Preview stream={camera} className="aspect-video w-full rounded-[14px]" mirror /> : null}
              {screen ? <Preview stream={screen} className="aspect-video w-full rounded-[14px]" /> : null}
              {mic && !micOff ? <LevelMeter mic={mic} /> : null}
              <Sending session={s} />
            </div>
          </div>
        ) : phase === 'sending' || phase === 'finishing' ? (
          <div className="mx-auto mt-10 max-w-[520px] rounded-[18px] bg-white/[0.04] p-6 ring-1 ring-inset ring-white/[0.07]" role="status" aria-live="polite" data-finishing={phase}>
            <div className="flex items-center gap-2.5 text-[15px] font-medium text-white">
              <Spinner size={15} /> {phase === 'sending' ? 'Sending the last of it…' : 'Adding it to the Library…'}
            </div>
            {notice ? <p className="mt-3 text-[13px] text-[#e8b169]">{notice}</p> : null}
            {s ? <Sending session={s} /> : null}
          </div>
        ) : phase === 'failed' ? (
          <div className="mx-auto mt-10 max-w-[560px] rounded-[18px] bg-white/[0.04] p-6 ring-1 ring-inset ring-white/[0.07]" role="alert" data-failed>
            <div className="flex items-start gap-3">
              <WarningCircle size={22} weight="fill" className="mt-[1px] shrink-0 text-[#e0a458]" />
              <div>
                <h3 className="text-[16px] font-semibold text-white">The recording isn’t in the Library yet.</h3>
                <p className="mt-2 text-[13.5px] leading-relaxed text-[#b5aca2]">{failure}</p>
                <p className="mt-2 text-[13px] text-[#8f877e]">Nothing recorded is lost: it waits in this browser and on {host.machine}. Try again, or come back to the recorder later.</p>
                {s ? <Sending session={s} /> : null}
                <div className="mt-5 flex flex-wrap gap-2">
                  <Button variant="primary" onClick={() => void tryAgain()} data-autofocus>
                    Try again
                  </Button>
                  <Button variant="ghost" onClick={close}>
                    Later
                  </Button>
                </div>
              </div>
            </div>
          </div>
        ) : phase === 'done' && result ? (
          <Done result={result} notice={notice} onAgain={again} onClose={close} onChanged={setResult} />
        ) : null}
      </div>
    </div>,
    document.body,
  );
}

type ProblemContext = Parameters<typeof problemWords>[1];

function SourceRow({ icon, title, problem: p, ctx: c, onRetry, children }: { icon: ReactNode; title: string; problem?: SourceError; ctx: ProblemContext; onRetry?: () => void; children: ReactNode }) {
  const words = p ? problemWords(p, c) : null;
  return (
    <div className="rounded-[16px] bg-white/[0.03] p-4 ring-1 ring-inset ring-white/[0.06]" data-source={title.toLowerCase()}>
      <div className="mb-2 flex items-center gap-2 text-[13px] font-medium text-white">
        <span className="text-[#e0a458]">{icon}</span> {title}
      </div>
      {children}
      {words ? (
        <div className="mt-3 rounded-[12px] bg-[#ef8d80]/10 px-3 py-2.5 text-[12.5px] leading-relaxed" role="alert" data-problem={p!.kind}>
          <div className="font-medium text-white">{words[0]}</div>
          {words.slice(1).map((w) => (
            <div key={w} className="text-[#d6cec5]">
              {w}
            </div>
          ))}
          {onRetry ? (
            <button type="button" onClick={onRetry} className="mt-2 rounded-full bg-white/[0.08] px-3 py-1 text-[12px] font-medium text-white hover:bg-white/[0.14]">
              Try again
            </button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/** Recordings this browser started that didn't finish (a reload, a closed tab). */
function Unfinished({ exclude, userId }: { exclude?: string; userId: string }) {
  const dialog = useDialog();
  const [list, setList] = useState<Pending[]>([]);
  const [busy, setBusy] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const load = useCallback(async () => {
    const all = userId ? (await pendingRecordings(userId)).filter((p) => p.id !== exclude) : [];
    const out: Pending[] = [];
    for (const p of all) {
      // Recording in another tab right now: leave it alone.
      const free = navigator.locks ? await navigator.locks.request(`metarush-recording-${p.id}`, { ifAvailable: true }, async (l) => !!l) : true;
      if (!free) continue;
      try {
        const st = await call('recording.show', { recording: p.id });
        if (st.recording.state === 'ready' || st.recording.state === 'discarded') {
          await forget(p.id);
          continue;
        }
      } catch (e) {
        if (isGone(e)) {
          await forget(p.id);
          continue;
        }
      }
      out.push(p);
    }
    setList(out);
  }, [exclude, userId]);
  useEffect(() => {
    void load();
  }, [load]);
  if (!list.length && !msg) return null;
  const keep = async (p: Pending) => {
    setBusy(p.id);
    setMsg(null);
    try {
      const qs = await restoreQueues(p);
      await Promise.all([...qs.values()].map((q) => q.drained()));
      const out = await finishOnHost(p.id, p.clock, true);
      setMsg(out.text);
    } catch (e) {
      setMsg(toApiError(e).message);
    } finally {
      setBusy(null);
      void load();
    }
  };
  const discard = async (p: Pending) => {
    const ok = await dialog.confirm({ title: 'Throw this recording away?', body: 'What was recorded of it is deleted. This can’t be undone.', confirmLabel: 'Throw it away', tone: 'danger' });
    if (!ok) return;
    setBusy(p.id);
    try {
      await call('recording.discard', { recording: p.id }).catch((e: unknown) => {
        if (!isGone(e)) throw e;
      });
      await forget(p.id);
    } catch (e) {
      setMsg(toApiError(e).message);
    } finally {
      setBusy(null);
      void load();
    }
  };
  return (
    <div className="mb-5 space-y-2" data-unfinished>
      {list.map((p) => (
        <div key={p.id} className="flex flex-wrap items-center gap-3 rounded-[14px] bg-[#e0a458]/10 px-4 py-3 ring-1 ring-inset ring-[#e0a458]/25" data-unfinished-recording={p.id}>
          <WarningCircle size={16} weight="fill" className="shrink-0 text-[#e0a458]" />
          <p className="min-w-0 flex-1 text-[13px] text-[#f2ece4]">
            “{p.name}” didn’t finish {p.stopped ? 'sending' : 'recording'} (the tab closed or the page reloaded). What this browser kept can still be sent.
          </p>
          <Button size="sm" variant="primary" loading={busy === p.id} disabled={!!busy} onClick={() => void keep(p)} data-keep-unfinished>
            Send the rest and keep it
          </Button>
          <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => void discard(p)}>
            Throw it away
          </Button>
        </div>
      ))}
      {msg ? <p className="text-[12.5px] text-[#b5aca2]">{msg}</p> : null}
    </div>
  );
}

/** A live picture of a source (muted). */
function Preview({ stream, className, mirror }: { stream: MediaStream; className?: string; mirror?: boolean }) {
  const ref = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    const v = ref.current;
    if (v && v.srcObject !== stream) {
      v.srcObject = stream;
      void v.play().catch(() => undefined);
    }
  }, [stream]);
  return <video ref={ref} muted playsInline autoPlay className={cx('bg-black object-cover', mirror && '-scale-x-100', className)} />;
}

function Note({ children, tone = 'info' }: { children: ReactNode; tone?: 'info' | 'warn' | 'error' }) {
  return (
    <p
      className={cx(
        'mt-3 flex items-start gap-2 rounded-[12px] px-3 py-2.5 text-[12.5px] leading-relaxed',
        tone === 'info' ? 'bg-white/[0.04] text-[#b5aca2]' : tone === 'warn' ? 'bg-[#e0a458]/10 text-[#f2ece4]' : 'bg-[#ef8d80]/10 text-[#f2ece4]',
      )}
      role={tone === 'info' ? undefined : 'alert'}
      data-note={tone}
    >
      {tone !== 'info' ? <WarningCircle size={14} weight="fill" className={cx('mt-[2px] shrink-0', tone === 'warn' ? 'text-[#e0a458]' : 'text-[#ef8d80]')} /> : null}
      <span>{children}</span>
    </p>
  );
}

/** How much each track has sent, and any trouble sending. */
function Sending({ session }: { session: RecordingSession }) {
  const tracks = session.tracks;
  const waiting = tracks.reduce((a, t) => a + Math.max(0, t.bytes - t.sent), 0);
  const total = tracks.reduce((a, t) => a + t.bytes, 0);
  const problem = tracks.find((t) => t.problem)?.problem;
  return (
    <div className="mt-3 text-[12.5px] text-[#a69d93]" data-sending={waiting}>
      <div>
        {waiting === 0 ? `Sent: ${size(total)} so far, all of it on the computer that runs MetaRush.` : `Sent ${size(total - waiting)}; ${size(waiting)} waiting to be sent.`}
      </div>
      {problem ? (
        <div className="mt-1.5 flex items-start gap-1.5 text-[#e8b169]" role="alert">
          <WarningCircle size={13} weight="fill" className="mt-[2px] shrink-0" /> {problem} It keeps trying; nothing is lost.
        </div>
      ) : null}
    </div>
  );
}

function Done({ result, notice, onAgain, onClose, onChanged }: { result: RecordingInfo; notice: string | null; onAgain: () => void; onClose: () => void; onChanged: (r: RecordingInfo) => void }) {
  const startCut = useStartCut();
  const dialog = useDialog();
  const link = async () => {
    const picked = await dialog.open<{ id: string }>((close) => <ProjectPicker label="Projects" onPick={(p) => close({ id: p.id })} />, { title: 'Link into which project?', size: 'sm' });
    if (!picked) return;
    setBusy('link');
    setProblem(null);
    try {
      onChanged(await call('recording.link', { recording: result.recording.id, project: picked.id }));
    } catch (e) {
      setProblem(toApiError(e).message);
    } finally {
      setBusy(null);
    }
  };
  const [busy, setBusy] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const r = result.recording;
  const layouts = r.state === 'ready' && result.links.length ? layoutsFor(result) : [];
  const preferred = (r.layout as CutLayout | undefined) ?? layouts[0];
  const ordered = [...layouts].sort((a, b) => (a === preferred ? -1 : b === preferred ? 1 : 0));
  return (
    <div className="mx-auto mt-6 max-w-[640px] rounded-[18px] bg-white/[0.04] p-6 ring-1 ring-inset ring-white/[0.07]" data-done={r.state}>
      <div className="flex items-start gap-3">
        <CheckCircle size={22} weight="fill" className="mt-[1px] shrink-0 text-[#92c79c]" />
        <div className="min-w-0 flex-1">
          <h3 className="text-[16px] font-semibold text-white">{r.state === 'ready' ? 'It’s in the Library.' : 'It’s saved, with a problem.'}</h3>
          <p className="mt-1.5 text-[13.5px] leading-relaxed text-[#b5aca2]">{result.text}</p>
          {notice ? <p className="mt-1.5 text-[13px] text-[#e8b169]">{notice}</p> : null}
          <ul className="mt-3 space-y-1 text-[13px] text-[#e9e2d9]">
            {result.tracks.map((t) => (
              <li key={t.id} data-track={t.id}>
                <span className="font-medium">{TRACK_WORDS[t.id] ?? 'Track'}</span>: {t.text}
              </li>
            ))}
          </ul>
          {result.next ? <p className="mt-3 text-[13px] text-[#a69d93]">{result.next}</p> : null}
          {problem ? <Note tone="error">{problem}</Note> : null}
          <div className="mt-5 flex flex-wrap gap-2">
            {ordered.map((l, i) => (
              <Button
                key={l}
                variant={i === 0 ? 'primary' : 'secondary'}
                loading={busy === l}
                disabled={!!busy}
                onClick={async () => {
                  setBusy(l);
                  setProblem(await startCut(result, l));
                  setBusy(null);
                }}
                data-start-cut={l}
              >
                {`Start a cut: ${LAYOUT_WORDS[l].toLowerCase()}`}
              </Button>
            ))}
            {r.state === 'ready' && !result.links.length ? (
              <Button variant="primary" loading={busy === 'link'} disabled={!!busy} onClick={() => void link()} data-link-recording>
                Link into a project…
              </Button>
            ) : null}
            <Button variant="ghost" onClick={onAgain} data-record-again>
              Record another
            </Button>
            <Button variant="ghost" onClick={onClose}>
              Close
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}

const TRACK_WORDS: Record<string, string> = { screen: 'Screen', camera: 'Camera', voice: 'Voice' };

function clockText(sec: number): string {
  const s = Math.max(0, Math.round(sec));
  return s >= 3600 ? `${Math.floor(s / 3600)}:${String(Math.floor(s / 60) % 60).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}` : `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

function size(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`;
  if (n >= 1 << 20) return `${Math.round(n / (1 << 20))} MB`;
  return `${Math.max(0, Math.round(n / 1024))} KB`;
}

