import { useEffect, useRef, useState } from 'react';
import { ArrowsClockwise, MagicWand, MusicNotes, Shapes, SpeakerHigh, TextAa, WarningCircle } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { media } from '../../api/client';
import { useOp, useOpMutation } from '../../api/queries';
import type { RemotionMapComposition, RemotionMapScene, RemotionMapSound, RemotionSourceRow, RemotionStillRow, RemotionTaskOut, RemotionWordRow, RemotionWordsCheckOut, RemotionWordsOut } from '../../api/types';
import { clock, plural } from '../../lib/format';
import { useActivityOpen } from '../../lib/workbench';
import { useToast } from '../../ui/Toast';
import { Badge, Button, cx, Details, ErrorPanel, Skeleton, Spinner, Theirs, Thumb } from '../../ui/primitives';

/*
 * What a Remotion project holds (D167–D171), as pictures and words: its
 * scenes with a still each, the words on screen, its sounds; its words as
 * settings a person changes here; a scene lifted into a motion graphic; its
 * sounds brought in as the cut's own tracks. Nothing of the project runs in
 * the dashboard — every picture was drawn in the render jail.
 */

type JobView = { id: string; state: string; title: string; progress: { pct: number; message?: string }; result?: Record<string, unknown>; error?: string };

const running = (state?: string) => state === 'queued' || state === 'running';

/** Follows the work a Remotion task started until it is done, then says how it went. */
export function useTask(invalidate: string[]) {
  const toast = useToast();
  const qc = useQueryClient();
  const [, setActivity] = useActivityOpen();
  const [jobId, setJobId] = useState<string | null>(null);
  const q = useOp('job.show', jobId ? { id: jobId } : null, { refetchInterval: (s) => (s.state.data && running(s.state.data.state) ? 1500 : false) });
  const job = q.data as unknown as JobView | undefined;
  const told = useRef<string | null>(null);
  useEffect(() => {
    if (!job || running(job.state) || told.current === job.id) return;
    told.current = job.id;
    for (const k of invalidate) void qc.invalidateQueries({ queryKey: [k] });
    const message = typeof job.result?.message === 'string' ? job.result.message : '';
    if (job.state === 'succeeded') toast({ tone: 'success', title: message || 'Done', durationMs: 9000 });
    else toast({ tone: 'error', title: 'That did not work', body: job.error || undefined, durationMs: 12000 });
  }, [job, qc, toast, invalidate]);
  const start = (out: RemotionTaskOut) => {
    told.current = null;
    setJobId(out.jobId);
    toast({ title: out.message, action: { label: 'Show progress', onClick: () => setActivity(true) } });
  };
  const busy = !!job && running(job.state);
  return { start, busy, line: busy ? job?.progress?.message || job?.title || '' : '', pct: job?.progress?.pct ?? 0 };
}

export function Working({ line, pct }: { line: string; pct: number }) {
  return (
    <p role="status" className="flex items-center gap-2 text-[12.5px] text-muted">
      <Spinner size={12} />
      <span className="min-w-0 truncate">{line ? line.charAt(0).toUpperCase() + line.slice(1) : 'Working on it'}</span>
      {pct > 0 && pct < 1 ? <span className="shrink-0 text-subtle">{Math.round(pct * 100)}%</span> : null}
    </p>
  );
}

export const failedToast = (toast: ReturnType<typeof useToast>) => (e: { message: string; hint?: string }) =>
  toast({ tone: 'error', title: 'That did not work', body: [e.message, e.hint].filter(Boolean).join(' ') });

/* ---------- the map: scenes, words on screen, sounds ---------- */

const roleWords: Record<RemotionMapSound['role'], [string, string]> = { music: ['piece of music', 'pieces of music'], voiceover: ['voice line', 'voice lines'], effect: ['sound effect', 'sound effects'] };

function soundsSummary(sounds: RemotionMapSound[]): string {
  const parts: string[] = [];
  for (const role of ['music', 'voiceover', 'effect'] as const) {
    const n = sounds.filter((s) => s.role === role).length;
    if (n) parts.push(plural(n, roleWords[role][0], roleWords[role][1]));
  }
  return parts.join(', ');
}

/** The project map of the version shown (made in the render jail the first time it is opened). */
export function RemotionMapPanel({ row, projectId }: { row: RemotionSourceRow; projectId: string }) {
  const toast = useToast();
  const ref = `${row.name}@${row.version}`;
  const q = useOp('remotion.map', { ref, projectId }, { refetchInterval: (s) => (s.state.data?.state === 'making' ? 3000 : false) });
  const again = useOpMutation('remotion.map', { invalidate: ['remotion.map'] });
  const [pick, setPick] = useState('');
  if (q.isLoading) return <Skeleton className="mt-3 h-40 rounded-[14px]" />;
  if (q.error || !q.data) return <ErrorPanel className="mt-3" error={q.error} onRetry={() => void q.refetch()} />;
  const d = q.data;
  if (d.state !== 'done' || !d.map) {
    return (
      <div className="mt-3 rounded-[12px] bg-panel px-3.5 py-3 text-[13px] leading-relaxed text-muted hairline" data-remotion-map={d.state}>
        {d.state === 'making' ? (
          <span className="flex items-center gap-2">
            <Spinner size={13} /> {d.message}
          </span>
        ) : (
          <>
            <p>{d.message}</p>
            {d.state === 'failed' ? (
              <Button className="mt-2" size="sm" variant="secondary" icon={<ArrowsClockwise size={13} />} loading={again.isPending} onClick={() => again.mutate({ ref, projectId, again: true }, { onError: failedToast(toast) })}>
                Try again
              </Button>
            ) : null}
          </>
        )}
      </div>
    );
  }
  const comps = d.map.compositions;
  const comp = comps.find((c) => c.id === pick) ?? comps[0];
  if (!comp) return <p className="mt-3 text-[13px] text-muted">{d.message}</p>;
  return (
    <div className="mt-3" data-remotion-map="done">
      <p className="mb-2 text-[12.5px] text-muted">{d.message}</p>
      {comps.length > 1 ? (
        <div className="mb-3 flex flex-wrap gap-1.5" role="group" aria-label="Compositions">
          {comps.map((c) => (
            <button
              key={c.id}
              type="button"
              aria-pressed={c.id === comp.id}
              onClick={() => setPick(c.id)}
              className={cx('h-7 rounded-full px-3 text-[12.5px] hairline', c.id === comp.id ? 'bg-accent-soft font-medium text-fg' : 'text-muted hover:bg-hover')}
            >
              <Theirs>{c.id}</Theirs>
            </button>
          ))}
        </div>
      ) : null}
      <CompositionMap row={row} projectId={projectId} version={d.version} comp={comp} />
    </div>
  );
}

function CompositionMap({ row, projectId, version, comp }: { row: RemotionSourceRow; projectId: string; version: string; comp: RemotionMapComposition }) {
  const toast = useToast();
  const layers = useTask(['remotion.list', 'project.show', 'cut.show', 'revision.list']);
  const use = useOpMutation('remotion.use', { invalidate: ['remotion.list', 'project.show', 'cut.show', 'revision.list', 'activity.summary'] });
  const approved = !!row.approved && row.approved === version;
  const sounds = soundsSummary(comp.sounds);
  return (
    <div data-remotion-map-composition={comp.id}>
      <div className="mb-2 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-[12.5px] text-subtle">
        <span>
          {plural(comp.scenes.length, 'scene')} · {clock(comp.duration)}
          {comp.length.computed ? ', its length worked out from its data' : ''}
        </span>
        {sounds ? (
          <span className="inline-flex items-center gap-1">
            <SpeakerHigh size={12} aria-hidden /> {sounds}
          </span>
        ) : null}
      </div>
      {comp.problem ? <p className="mb-2 text-[12.5px] text-danger">{comp.problem}</p> : null}
      {comp.sounds.length && approved ? (
        <div className="mb-3 rounded-[12px] bg-panel px-3.5 py-2.5 hairline">
          <p className="text-[12.5px] leading-relaxed text-muted">
            Its sounds can come in as the cut&apos;s own tracks: the picture alone, with its music, voice and effects on the Sound tab, where you can change their levels, or a new voice can
            take over.
          </p>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              variant="secondary"
              icon={<MusicNotes size={13} />}
              loading={use.isPending}
              disabled={layers.busy}
              onClick={() =>
                use.mutate({ source: row.name, projectId, composition: comp.id, sound: 'layers' }, {
                  onSuccess: (o) => (o.jobId ? layers.start({ message: o.message, jobId: o.jobId, state: 'queued', name: row.name }) : toast({ tone: 'success', title: o.message })),
                  onError: failedToast(toast),
                })
              }
            >
              Use in the cut, its sounds as tracks
            </Button>
            {layers.busy ? <Working line={layers.line} pct={layers.pct} /> : null}
          </div>
        </div>
      ) : null}
      <ol className="space-y-2.5">
        {comp.scenes.map((s) => (
          <li key={s.n}>
            <SceneRow row={row} projectId={projectId} version={version} comp={comp} scene={s} />
          </li>
        ))}
      </ol>
      <Details compact className="mt-3">
        <div>
          Registered in {comp.file}:{comp.line}
          {comp.component ? `, drawn by ${comp.component}${comp.componentFile ? ` (${comp.componentFile})` : ''}` : ''}
        </div>
        {comp.length.file ? (
          <div>
            Its length is set in {comp.length.file}:{comp.length.line}
            {comp.length.code ? ` — ${comp.length.code}` : ''}
          </div>
        ) : null}
        {comp.sounds.map((s) => (
          <div key={s.n}>
            Sound {s.n}: {s.file || 'not a project file'}, {s.role}, from {clock(s.from)} to {clock(s.to)}, level {Math.round(s.volume * 100)}%{s.code ? ` (${s.code})` : ''}
          </div>
        ))}
      </Details>
    </div>
  );
}

function SceneRow({ row, projectId, version, comp, scene: s }: { row: RemotionSourceRow; projectId: string; version: string; comp: RemotionMapComposition; scene: RemotionMapScene }) {
  const toast = useToast();
  const lift = useOpMutation('remotion.lift');
  const task = useTask(['motion.list', 'resources.list']);
  const [all, setAll] = useState(false);
  const words = s.words.filter((w) => w.text.trim());
  const shown = all ? words : words.slice(0, 3);
  const ratio = comp.width && comp.height ? `${comp.width} / ${comp.height}` : '16 / 9';
  return (
    <div className="flex flex-col gap-3 rounded-[12px] bg-raised p-2.5 hairline sm:flex-row" data-remotion-scene={s.n}>
      <div className="w-full shrink-0 overflow-hidden rounded-[9px] bg-inset sm:w-[176px]" style={{ aspectRatio: ratio }}>
        <Thumb src={s.still ? media.remotionScene(version, comp.id, s.n) : undefined} alt={`Scene ${s.n}, ${s.name}`} className="h-full w-full" fit="contain" fallback={<Shapes size={18} className="text-subtle" />} />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span className="text-[13px] font-medium text-fg [overflow-wrap:anywhere]">
            {s.n}. <Theirs>{s.name || 'Scene'}</Theirs>
          </span>
          <span className="text-[12px] text-subtle">
            {clock(s.from)} – {clock(s.to)}
            {s.length.computed ? ', timed by its data' : ''}
          </span>
          {s.fromSettings ? <Badge tone="info">{plural(s.fromSettings, 'word setting')}</Badge> : null}
        </div>
        {shown.length ? (
          <ul className="mt-1 space-y-0.5 text-[12.5px] text-muted">
            {shown.map((w, i) => (
              <li key={i} className="flex min-w-0 items-start gap-1.5">
                <TextAa size={12} className="mt-[3px] shrink-0 text-subtle" aria-hidden />
                <Theirs className="min-w-0 [overflow-wrap:anywhere]">“{w.text}”</Theirs>
              </li>
            ))}
            {words.length > 3 ? (
              <li>
                <button type="button" className="text-[12px] font-medium text-accent-text hover:underline" onClick={() => setAll((v) => !v)}>
                  {all ? 'Show fewer words' : `${words.length - 3} more on screen`}
                </button>
              </li>
            ) : null}
          </ul>
        ) : null}
        {s.overflowing?.length ? (
          <p className="mt-1 flex items-start gap-1.5 text-[12px] text-danger">
            <WarningCircle size={13} weight="fill" className="mt-[2px] shrink-0" />
            <span>
              Words spill out of their box here: <Theirs>{s.overflowing.slice(0, 2).map((t) => `“${t}”`).join(', ')}</Theirs>
            </span>
          </p>
        ) : null}
        <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[12px] text-subtle">
          {s.pictures.length ? <span>{plural(s.pictures.length, 'picture')}</span> : null}
          {s.sounds.length ? <span>{plural(s.sounds.length, 'sound')} {s.sounds.length === 1 ? 'starts' : 'start'} here</span> : null}
          {s.component ? (
            <Button
              size="sm"
              variant="ghost"
              icon={<MagicWand size={13} />}
              loading={lift.isPending}
              disabled={task.busy}
              title="Its own code becomes a motion graphic any cut can use; the project's colours, fonts and components become one shared library"
              onClick={() => lift.mutate({ source: `${row.name}@${version}`, projectId, composition: comp.id, scene: String(s.n) }, { onSuccess: task.start, onError: failedToast(toast) })}
            >
              Make it a motion graphic
            </Button>
          ) : null}
        </div>
        {task.busy ? (
          <div className="mt-1">
            <Working line={task.line} pct={task.pct} />
          </div>
        ) : null}
        <Details compact className="mt-1.5">
          {s.component ? (
            <div>
              Drawn by {s.component}
              {s.file ? ` in ${s.file}${s.line ? `:${s.line}` : ''}` : ''}
            </div>
          ) : null}
          {s.placed ? <div>Placed in {s.placed}</div> : null}
          <div>
            Frames {s.fromFrame}–{s.fromFrame + s.frames}
            {s.length.file ? `; its length is set in ${s.length.file}:${s.length.line}` : ''}
          </div>
          {words.map((w, i) => (
            <div key={i}>
              “{w.text}”{w.key ? ` — the setting ${w.key}` : ''}
              {w.file ? ` (${w.file}:${w.line})` : w.data ? ` (from ${w.data})` : ''}
              {w.flags?.length ? ` [${w.flags.join(', ')}]` : ''}
            </div>
          ))}
          {s.pictures.map((p, i) => (
            <div key={`p${i}`}>Picture: {p.file}</div>
          ))}
        </Details>
      </div>
    </div>
  );
}

/* ---------- words as settings ---------- */

/** A source's on-screen words: settings to change, or the offer to make them settings. */
export function RemotionWordsPanel({ row, projectId, onReview }: { row: RemotionSourceRow; projectId: string; onReview: (version: string) => void }) {
  const toast = useToast();
  const q = useOp('remotion.words', { source: row.name, projectId });
  const setup = useOpMutation('remotion.words.setup');
  const task = useTask(['remotion.words', 'remotion.list', 'remotion.show', 'remotion.map']);
  if (q.isLoading) return <Skeleton className="mt-3 h-32 rounded-[14px]" />;
  if (q.error || !q.data) return <ErrorPanel className="mt-3" error={q.error} onRetry={() => void q.refetch()} />;
  const d = q.data;
  if (d.hasSettings) return <WordsForm data={d} row={row} projectId={projectId} />;
  return (
    <div className="mt-3 rounded-[12px] bg-panel px-3.5 py-3 hairline" data-remotion-words="offer">
      <p className="max-w-[72ch] text-[13px] leading-relaxed text-muted">{d.message}</p>
      {d.waiting ? (
        <Button className="mt-2" size="sm" variant="primary" onClick={() => onReview(d.waiting!.hash)}>
          Review version {d.waiting.number}
        </Button>
      ) : d.copy.length ? (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant="primary"
            icon={<TextAa size={13} />}
            loading={setup.isPending}
            disabled={task.busy}
            onClick={() => setup.mutate({ source: row.name, projectId }, { onSuccess: task.start, onError: failedToast(toast) })}
          >
            Make its words settings
          </Button>
          {task.busy ? <Working line={task.line} pct={task.pct} /> : null}
        </div>
      ) : null}
      {d.copy.length ? (
        <Details compact className="mt-2">
          {d.copy.map((c) => (
            <div key={c.file}>
              {c.file}: {plural(c.words, 'word')} in {c.consts.join(', ')}
            </div>
          ))}
        </Details>
      ) : null}
    </div>
  );
}

function WordsForm({ data: d, row, projectId }: { data: RemotionWordsOut; row: RemotionSourceRow; projectId: string }) {
  const toast = useToast();
  const set = useOpMutation('remotion.words.set');
  const task = useTask(['remotion.words', 'remotion.list', 'project.show', 'cut.show', 'revision.list', 'remotion.map']);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [all, setAll] = useState(false);
  const approved = !!row.approved && row.approved === d.version;
  const rows = d.settings.filter((s) => all || s.onScreen || s.changed);
  const edits = Object.entries(draft).filter(([k, t]) => d.settings.find((s) => s.key === k)?.text !== t);
  const send = (checkOnly: boolean) =>
    set.mutate({ source: row.name, projectId, set: edits.map(([key, text]) => ({ key, text })), checkOnly }, {
      onSuccess: (o) => {
        task.start(o);
        if (!checkOnly) setDraft({});
      },
      onError: failedToast(toast),
    });
  return (
    <div className="mt-3" data-remotion-words="settings">
      <p className="mb-2 max-w-[72ch] text-[12.5px] leading-relaxed text-muted">
        {d.message} Changed words are drawn with no network and checked (does the video get longer, do they still fit), then the latest cuts&apos; clips are made again with them.
      </p>
      {!approved ? <p className="mb-2 text-[12.5px] text-muted">Version {d.versionNumber} isn&apos;t approved yet, so its words can&apos;t change for cuts until a person approves it.</p> : null}
      <ul className="space-y-2">
        {rows.map((s) => (
          <WordField key={s.key} s={s} value={draft[s.key] ?? s.text} disabled={!approved || task.busy} onChange={(t) => setDraft((x) => ({ ...x, [s.key]: t }))} />
        ))}
      </ul>
      {d.settings.length > rows.length || all ? (
        <button type="button" onClick={() => setAll((v) => !v)} className="mt-2 text-[12.5px] font-medium text-accent-text hover:underline">
          {all ? 'Only the words on screen' : `Show all ${d.settings.length} settings`}
        </button>
      ) : null}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button size="sm" variant="secondary" disabled={!approved || !edits.length || task.busy} loading={set.isPending && !!set.variables?.checkOnly} onClick={() => send(true)}>
          Check the new words
        </Button>
        <Button size="sm" variant="primary" disabled={!approved || !edits.length || task.busy} loading={set.isPending && !set.variables?.checkOnly} onClick={() => send(false)}>
          Use the new words
        </Button>
        {edits.length ? (
          <Button size="sm" variant="ghost" disabled={task.busy} onClick={() => setDraft({})}>
            Undo my edits
          </Button>
        ) : null}
        {task.busy ? <Working line={task.line} pct={task.pct} /> : null}
      </div>
      {d.pending ? <WordsCheck title="The last check (nothing changed yet)" c={d.pending} /> : null}
      {d.last ? <WordsCheck title={`The last change (${d.number})`} c={d.last} /> : null}
      <Details compact className="mt-3">
        {d.settings.map((s) => (
          <div key={s.key}>
            {s.key}: {s.file}:{s.line}
            {s.used.length ? ` — read by ${s.used.join(', ')}` : ''}
          </div>
        ))}
      </Details>
    </div>
  );
}

function WordField({ s, value, disabled, onChange }: { s: RemotionWordRow; value: string; disabled: boolean; onChange: (t: string) => void }) {
  const id = `w-${s.key.replace(/[^A-Za-z0-9_-]/g, '-')}`;
  const where = s.seen[0] ?? (s.used.length ? `read by ${s.used.slice(0, 2).join(', ')}` : 'not seen on screen');
  return (
    <li data-word-setting={s.key}>
      <label htmlFor={id} className="mb-0.5 flex flex-wrap items-baseline gap-x-2 text-[12px] text-subtle" title={s.key}>
        <Theirs>{where}</Theirs>
        {s.changed ? (
          <span>
            · the code says <Theirs>“{s.code}”</Theirs>
          </span>
        ) : null}
      </label>
      <textarea
        id={id}
        value={value}
        disabled={disabled}
        maxLength={400}
        rows={value.length > 70 ? 2 : 1}
        onChange={(e) => onChange(e.target.value)}
        className="block w-full resize-y rounded-[10px] bg-panel px-3 py-2 text-[13px] leading-snug text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0 disabled:opacity-60"
        data-user-content
      />
    </li>
  );
}

function WordsCheck({ title, c }: { title: string; c: RemotionWordsCheckOut }) {
  return (
    <div className="mt-4" data-words-check>
      <h5 className="text-[13px] font-semibold text-fg">{title}</h5>
      {c.summary ? <p className="mt-0.5 max-w-[72ch] text-[12.5px] leading-relaxed text-muted">{c.summary}</p> : null}
      {c.stills.length ? <StillPairs stills={c.stills} /> : null}
    </div>
  );
}

/** Before/after stills of a check (afterLabel: what changed, "With the new words"). */
export function StillPairs({ stills, afterLabel = 'With the new words' }: { stills: RemotionStillRow[]; afterLabel?: string }) {
  return (
    <ul className="mt-2 space-y-3">
      {stills.slice(0, 4).map((s, i) => (
        <li key={i}>
          <div className="mb-1 text-[12px] text-subtle">
            <Theirs>{s.composition}</Theirs> at {clock(s.at)}
          </div>
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            {s.beforeUrl ? (
              <figure className="min-w-0">
                <img src={s.beforeUrl} alt={`${s.composition} at ${clock(s.at)}, before`} className="block h-auto w-full rounded-[10px] bg-inset hairline" loading="lazy" />
                <figcaption className="mt-0.5 text-[11.5px] text-subtle">Before</figcaption>
              </figure>
            ) : null}
            <figure className="min-w-0">
              <img src={s.afterUrl} alt={`${s.composition} at ${clock(s.at)}, ${afterLabel.toLowerCase()}`} className="block h-auto w-full rounded-[10px] bg-inset hairline" loading="lazy" />
              <figcaption className="mt-0.5 text-[11.5px] text-subtle">{afterLabel}</figcaption>
            </figure>
          </div>
        </li>
      ))}
    </ul>
  );
}
