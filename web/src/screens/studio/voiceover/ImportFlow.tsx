import { useEffect, useRef, useState } from 'react';
import { CheckCircle, FileAudio, WarningCircle } from '@phosphor-icons/react';
import { call } from '../../../api/client';
import { uploadFraction } from '../../../api/uploads';
import { markJobSeen } from '../../../lib/workbench';
import { Button, cx, Progress, Spinner } from '../../../ui/primitives';
import { applyPlan, checkApplied, errorText, useRefreshCut } from './api';
import { clipName, type Scope } from './AddVoiceover';
import { uploadRecording, waitForWords, wordsAsScript } from './recordings';
import type { Clip } from '../../../api/types';

/*
 * Using a recording the person already has: it is added to the project,
 * MetaRush listens to it, and its words are matched to the lines (each piece
 * placed at its line, or the whole recording from the first line's spot).
 * Without a script yet, the script is written from what the recording says.
 */

type Step = 'upload' | 'listen' | 'script' | 'place' | 'done' | 'failed';

export interface ImportJob {
  file: File;
  scope: Scope;
  /** Recorded in the dashboard's recorder (rather than a file the person had). */
  recorded?: boolean;
}

/** The lines that start over one clip (a recording made for that clip covers only them). */
export async function linesOver(revisionId: string, clipId: string): Promise<string[]> {
  const [show, plan] = await Promise.all([call('voiceover.show', { revisionId }), call('revision.plan', { id: revisionId })]);
  const c = plan.clips.find((x) => x.id === clipId);
  if (!c) return [];
  return show.beats
    .filter((b) => {
      const f = Math.round(b.start * plan.fps);
      return f >= c.from && f < c.from + c.durationInFrames;
    })
    .map((b) => b.id);
}

export function ImportFlow({ job, projectId, projectName, headRevisionId, hasScript, clips, onDone }: {
  job: ImportJob;
  projectId: string;
  projectName: string;
  /** The latest version of the cut (read again at each step). */
  headRevisionId: () => string | undefined;
  hasScript: boolean;
  clips: Clip[];
  onDone: () => void;
}) {
  const [step, setStep] = useState<Step>('upload');
  const [progress, setProgress] = useState(0);
  const [waiting, setWaiting] = useState(false);
  const [whole, setWhole] = useState(false);
  const wholeRef = useRef(false);
  wholeRef.current = whole;
  const [result, setResult] = useState<{ summary: string; warnings: string[] } | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const refresh = useRefreshCut();
  const ran = useRef(false);

  useEffect(() => {
    if (ran.current) return;
    ran.current = true;
    const ctrl = new AbortController();
    void (async () => {
      try {
        const assetId = await uploadRecording(job.file, projectId, projectName, (it) => setProgress(uploadFraction(it)));
        setStep('listen');
        await waitForWords(assetId, ctrl.signal, setWaiting);
        let rev = headRevisionId();
        if (!rev) throw new Error('The project has no cut yet.');
        if (!hasScript) {
          setStep('script');
          const parsed = await call('voiceover.parse', { revisionId: rev, text: await wordsAsScript(assetId), ...(job.scope.clipId ? { clip: job.scope.clipId } : {}) });
          if (!parsed.result) throw new Error(parsed.issues[0]?.message ?? parsed.summary);
          rev = (await applyPlan(parsed.result)).revision.id;
          refresh();
        }
        setStep('place');
        const beats = job.scope.clipId ? await linesOver(rev, job.scope.clipId) : undefined;
        const out = await call('voiceover.align', {
          revisionId: rev,
          assetId,
          layout: wholeRef.current ? 'continuous' : 'beats',
          source: job.recorded ? 'recorded' : 'imported',
          ...(beats?.length ? { beats } : {}),
          apply: true,
        });
        markJobSeen(out.jobId);
        checkApplied(out.applied);
        setResult({ summary: out.summary.replace(/ It is in the cut now as version \d+\./, ''), warnings: out.warnings ?? [] });
        setStep('done');
      } catch (e) {
        if (e instanceof DOMException && e.name === 'AbortError') return;
        setProblem(errorText(e));
        setStep('failed');
      } finally {
        refresh();
      }
    })();
    return () => ctrl.abort();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const steps: { key: Step; label: string }[] = [
    { key: 'upload', label: 'Adding the recording to the project' },
    { key: 'listen', label: waiting ? 'Waiting for this computer to listen to it (it is busy or saving battery)' : 'Listening to it' },
    ...(hasScript ? [] : [{ key: 'script' as Step, label: 'Writing down what you said as lines' }]),
    { key: 'place', label: 'Lining it up with your lines' },
  ];
  const order: Step[] = ['upload', 'listen', 'script', 'place', 'done'];
  const at = order.indexOf(step);

  return (
    <section className="rounded-[14px] bg-panel p-4 hairline" aria-label="Adding your recording" aria-live="polite">
      <div className="flex items-start gap-3">
        <span className="grid h-10 w-10 shrink-0 place-items-center rounded-[11px] bg-accent-soft text-accent-text" aria-hidden>
          <FileAudio size={19} />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="truncate text-[14px] font-semibold">{job.recorded ? 'Your recording' : job.file.name}</h3>
          <p className="text-[12.5px] text-muted">
            {job.scope.clipId ? `For ${clipName(clips, job.scope.clipId)}` : 'For the whole video'}
            {step === 'done' ? '' : '. You can keep working; this finishes on its own.'}
          </p>
        </div>
      </div>
      <ol className="mt-3 space-y-1.5">
        {steps.map((s) => {
          const i = order.indexOf(s.key);
          const done = step === 'done' || (at > i && step !== 'failed');
          const now = s.key === step;
          return (
            <li key={s.key} className={cx('flex items-center gap-2 text-[12.5px]', done ? 'text-fg' : now ? 'text-fg' : 'text-subtle')}>
              {done ? <CheckCircle size={14} weight="fill" className="text-ok" /> : now ? <Spinner size={12} /> : <span className="h-3.5 w-3.5 rounded-full border border-line-strong" />}
              {s.label}
            </li>
          );
        })}
      </ol>
      {step === 'upload' ? <Progress className="mt-3" value={progress} label="Upload" /> : null}
      {step === 'upload' || step === 'listen' || step === 'script' ? (
        <fieldset className="mt-3">
          <legend className="text-[12px] text-subtle">When it is lined up</legend>
          <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-[12.5px]">
            <label className="flex items-center gap-1.5">
              <input type="radio" name={`whole-${job.file.name}`} checked={!whole} onChange={() => setWhole(false)} className="accent-[var(--accent)]" />
              Split it at each line (each piece follows its shot)
            </label>
            <label className="flex items-center gap-1.5">
              <input type="radio" name={`whole-${job.file.name}`} checked={whole} onChange={() => setWhole(true)} className="accent-[var(--accent)]" />
              Keep it in one piece
            </label>
          </div>
        </fieldset>
      ) : null}
      {step === 'done' && result ? (
        <div className="mt-3 rounded-[12px] bg-ok-soft px-3 py-2.5 text-[12.5px] text-fg">
          <p>{result.summary}</p>
          {result.warnings.map((w) => (
            <p key={w} className="mt-1 text-muted">
              {w}
            </p>
          ))}
        </div>
      ) : null}
      {step === 'failed' ? (
        <p className="mt-3 flex items-start gap-1.5 rounded-[12px] bg-danger-soft px-3 py-2.5 text-[12.5px] text-fg">
          <WarningCircle size={14} weight="fill" className="mt-[2px] shrink-0 text-danger" />
          {problem}
        </p>
      ) : null}
      {step === 'done' || step === 'failed' ? (
        <div className="mt-3 flex justify-end">
          <Button size="sm" variant={step === 'done' ? 'primary' : 'secondary'} onClick={onDone}>
            {step === 'done' ? 'Done' : 'Close'}
          </Button>
        </div>
      ) : null}
    </section>
  );
}
