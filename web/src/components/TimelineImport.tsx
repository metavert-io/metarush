import { useEffect, useMemo, useRef, useState } from 'react';
import { CheckCircle, FilmStrip, Question, UploadSimple, XCircle } from '@phosphor-icons/react';
import { useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import type { ops } from '@metarush/schema';
import { call } from '../api/client';
import { uploads, useUploads } from '../api/uploads';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, cx, ErrorPanel, Spinner, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { ProjectPicker } from './ProjectPicker';

/*
 * Timelines from other editors (D303, D304): a dropped .otio, .fcpxml,
 * .fcpxmld, .xml or .edl file opens this dialog instead of uploading. It
 * reads the timeline (timeline.read), shows the footage it found and asks
 * about the rest — use a clip from the Library, add the file, or leave it
 * out — lists what can't be carried over, and makes the cut
 * (timeline.import, the normal edit check) in the chosen project.
 */

/** A timeline file another editing app exported, by its name or place in a dropped folder. */
export function isTimelineFile(path: string): boolean {
  const p = path.toLowerCase();
  if (/\.fcpxmld\/info\.fcpxml$/.test(p)) return true;
  if (p.includes('.fcpxmld/')) return false;
  return /\.(otio|fcpxml|edl|xml)$/.test(p);
}

/** Splits timeline files out of a drop: the rest uploads as usual. */
export function splitTimelines<T extends { file: File; path?: string }>(files: T[]): { timelines: T[]; rest: T[] } {
  const timelines: T[] = [];
  const rest: T[] = [];
  for (const f of files) {
    const p = f.path || f.file.name;
    if (isTimelineFile(p)) timelines.push(f);
    else if (p.toLowerCase().includes('.fcpxmld/')) continue; // the bundle's other files
    else rest.push(f);
  }
  return { timelines, rest };
}

type Answer = ops.TimelineAnswer;

/** Opens the import dialog for each timeline file. */
export function useTimelineImport() {
  const dialog = useDialog();
  return (file: File, path: string, target: { projectId?: string; label: string }) =>
    dialog.open<boolean>((close) => <TimelineImport file={file} path={path} target={target} onClose={close} />, { title: 'Make a cut from a timeline', size: 'lg', label: 'Make a cut from a timeline' });
}

function bundleName(path: string): string {
  const m = /([^/]+\.fcpxmld)\/info\.fcpxml$/i.exec(path);
  return m?.[1] ?? (path.split('/').pop() || path);
}

export function TimelineImport({ file, path, target, onClose }: {
  file: File;
  path: string;
  target: { projectId?: string; label: string };
  onClose: (made?: boolean) => void;
}) {
  const toast = useToast();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [content, setContent] = useState<string>();
  const [projectId, setProjectId] = useState(target.projectId);
  const [projectName, setProjectName] = useState(target.label);
  const [answers, setAnswers] = useState<Record<string, Answer>>({});
  const [read, setRead] = useState<ops.TimelineReadOut>();
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const [making, setMaking] = useState(false);
  const name = bundleName(path);

  useEffect(() => {
    file.text().then(setContent, (e) => setError(e));
  }, [file]);

  const answerList = useMemo(() => Object.values(answers), [answers]);
  useEffect(() => {
    if (content === undefined) return;
    let stop = false;
    setBusy(true);
    setError(undefined);
    call('timeline.read', { content, name, projectId, answers: answerList.filter((a) => !a.add) })
      .then((r) => !stop && setRead(r))
      .catch((e) => !stop && setError(e))
      .finally(() => !stop && setBusy(false));
    return () => {
      stop = true;
    };
  }, [content, name, projectId, answerList]);

  const answer = (key: string, a: Omit<Answer, 'footage'> | undefined) =>
    setAnswers((s) => {
      const next = { ...s };
      if (a) next[key] = { footage: key, ...a };
      else delete next[key];
      return next;
    });

  const make = async () => {
    if (!projectId || content === undefined) return;
    setMaking(true);
    setError(undefined);
    try {
      const out = await call('timeline.import', { content, name, projectId, answers: answerList });
      void qc.invalidateQueries();
      toast({ tone: 'success', title: `Made the cut “${out.cutName}”`, body: 'Watch it, then approve it like any cut.' });
      onClose(true);
      if (out.link) navigate(out.link);
    } catch (e) {
      setError(e);
    } finally {
      setMaking(false);
    }
  };

  const open = read?.questions ?? 0;
  return (
    <div className="flex flex-col gap-4" data-testid="timeline-import">
      <div className="flex items-start gap-3">
        <span className="grid h-9 w-9 shrink-0 place-items-center rounded-[11px] bg-accent-soft text-accent-text">
          <FilmStrip size={18} weight="bold" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="text-[14px] font-medium text-fg">
            <Theirs>{read?.name || name}</Theirs>
          </div>
          <div className="mt-0.5 text-[13px] text-muted">
            {read ? `${read.formatName}: ${read.shots} clips, ${clock(read.seconds)}` : busy ? 'Reading the timeline…' : 'A timeline from another editing app'}
          </div>
        </div>
        {busy ? <Spinner label="Reading" /> : null}
      </div>

      <div className="rounded-[12px] bg-panel p-3 hairline">
        <div className="text-[12.5px] font-medium text-muted">The cut goes into</div>
        {projectId ? (
          <div className="mt-1 flex items-center justify-between gap-2 text-[13.5px]">
            <Theirs>{projectName}</Theirs>
            {!target.projectId ? (
              <Button size="sm" variant="ghost" onClick={() => setProjectId(undefined)}>
                Change
              </Button>
            ) : null}
          </div>
        ) : (
          <div className="mt-2">
            <ProjectPicker
              label="Project"
              autoFocus={false}
              onPick={(p) => {
                setProjectId(p.id);
                setProjectName(p.name);
              }}
            />
          </div>
        )}
      </div>

      {error ? <ErrorPanel error={error} compact /> : null}

      {read ? (
        <>
          <section aria-label="Footage">
            <h3 className="text-[12.5px] font-medium text-muted">Footage</h3>
            <ul className="mt-1.5 flex flex-col gap-1.5">
              {read.footage.map((ft) => (
                <FootageRow key={ft.key} ft={ft} answer={answers[ft.key]} onAnswer={(a) => answer(ft.key, a)} target={{ projectId, label: projectName }} />
              ))}
            </ul>
          </section>
          {read.chapters.length ? (
            <section aria-label="Chapters">
              <h3 className="text-[12.5px] font-medium text-muted">Chapters (they go into the version's delivery notes)</h3>
              <ul className="mt-1 text-[13px]">
                {read.chapters.map((c, i) => (
                  <li key={i}>
                    {clock(c.at)} <Theirs>{c.title}</Theirs>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}
          {read.notes ? <p className="text-[13px] text-muted">{read.notes === 1 ? 'One marker becomes a note on its clip.' : `${read.notes} markers become notes on their clips.`}</p> : null}
          {read.warnings.length ? (
            <ul className="text-[13px] text-muted">
              {read.warnings.map((w, i) => (
                <li key={i}>{w}</li>
              ))}
            </ul>
          ) : null}
          {read.lost.length ? (
            <details className="rounded-[12px] bg-panel p-3 hairline" open={read.lost.length <= 4}>
              <summary className="cursor-pointer text-[13px] font-medium">What can't be carried over ({read.lost.length})</summary>
              <ul className="mt-2 list-disc pl-5 text-[13px] text-muted">
                {read.lost.map((l, i) => (
                  <li key={i}>{l.charAt(0).toUpperCase() + l.slice(1)}</li>
                ))}
              </ul>
            </details>
          ) : null}
        </>
      ) : null}

      <div className="flex flex-wrap items-center justify-end gap-2">
        {open ? <span className="mr-auto text-[13px] text-muted">{open === 1 ? 'Answer for the footage above first.' : `Answer for the ${open} files above first.`}</span> : null}
        <Button variant="ghost" onClick={() => onClose(false)}>
          Cancel
        </Button>
        <Button variant="primary" loading={making} disabled={!read || !projectId || open > 0 || busy || read.shots === 0} onClick={make} data-testid="timeline-import-make">
          Make the cut
        </Button>
      </div>
    </div>
  );
}

function FootageRow({ ft, answer, onAnswer, target }: {
  ft: ops.TimelineFootage;
  answer?: Answer;
  onAnswer: (a: Omit<Answer, 'footage'> | undefined) => void;
  target: { projectId?: string; label: string };
}) {
  const dialog = useDialog();
  const picker = useRef<HTMLInputElement>(null);
  const [sent, setSent] = useState<File>();
  const items = useUploads();
  const upload = sent ? items.find((u) => u.file === sent) : undefined;
  useEffect(() => {
    const id = upload?.result?.assetId ?? upload?.duplicate?.assetId;
    if (id && answer?.relink !== id) onAnswer({ relink: id });
  }, [upload, answer, onAnswer]);

  const found = !!ft.assetId && ft.how !== 'skipped';
  const relink = async () => {
    const options = ft.candidates ?? [];
    const v = await dialog.prompt({
      title: `Use another clip for “${ft.name}”`,
      label: 'Clip name',
      defaultValue: options[0]?.title ?? '',
      helper: options.length ? `Maybe: ${options.map((c) => `“${c.title}” (${c.why})`).join(', ')}` : 'Type the name of a clip in the Library.',
      confirmLabel: 'Use it',
    });
    if (v) onAnswer({ relink: v });
  };
  return (
    <li className={cx('rounded-[12px] p-2.5 hairline', !found && !answer ? 'bg-accent-soft' : 'bg-panel')}>
      <div className="flex items-start gap-2">
        {ft.how === 'skipped' ? (
          <XCircle size={16} className="mt-0.5 shrink-0 text-muted" />
        ) : found ? (
          <CheckCircle size={16} weight="fill" className="mt-0.5 shrink-0 text-ok" />
        ) : (
          <Question size={16} weight="fill" className="mt-0.5 shrink-0 text-accent-text" />
        )}
        <div className="min-w-0 flex-1 text-[13px]">
          <div className="font-medium">
            <Theirs>{ft.name}</Theirs>
            <span className="ml-2 font-normal text-muted">
              {ft.shots ? `${ft.shots} shot${ft.shots === 1 ? '' : 's'}` : ''}
              {ft.shots && ft.sounds ? ', ' : ''}
              {ft.sounds ? `${ft.sounds} sound${ft.sounds === 1 ? '' : 's'}` : ''}
            </span>
          </div>
          {found ? (
            <div className="text-muted">
              Is “<Theirs>{ft.title}</Theirs>” ({ft.why})
            </div>
          ) : ft.how === 'skipped' ? (
            <div className="text-muted">Left out of the cut</div>
          ) : (
            <div className="text-muted">{ft.question}</div>
          )}
          {upload && !found ? (
            <div className="mt-1 flex items-center gap-1.5 text-muted">
              {upload.state === 'failed' ? <Badge tone="danger">Couldn't add it</Badge> : <Spinner size={12} />}
              {upload.state === 'failed' ? upload.error : `Adding ${upload.name}…`}
            </div>
          ) : null}
        </div>
        <div className="flex shrink-0 flex-wrap justify-end gap-1">
          {!found || answer ? (
            <>
              <Button size="sm" variant="quiet" onClick={relink}>
                Use a clip
              </Button>
              <Button size="sm" variant="quiet" icon={<UploadSimple size={13} />} onClick={() => picker.current?.click()} disabled={!target.projectId}>
                Add the file
              </Button>
              {ft.how === 'skipped' ? (
                <Button size="sm" variant="ghost" onClick={() => onAnswer(undefined)}>
                  Keep it
                </Button>
              ) : (
                <Button size="sm" variant="ghost" onClick={() => onAnswer({ skip: true })}>
                  Leave it out
                </Button>
              )}
            </>
          ) : null}
        </div>
      </div>
      <input
        ref={picker}
        type="file"
        className="hidden"
        aria-hidden
        tabIndex={-1}
        onChange={(e) => {
          const f = e.target.files?.[0];
          e.target.value = '';
          if (!f || !target.projectId) return;
          setSent(f);
          void uploads.add([{ file: f }], { projectId: target.projectId, label: target.label });
        }}
      />
    </li>
  );
}

function clock(sec: number): string {
  const s = Math.max(0, Math.floor(sec + 1e-6));
  return s >= 3600 ? `${Math.floor(s / 3600)}:${String(Math.floor(s / 60) % 60).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}` : `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}
