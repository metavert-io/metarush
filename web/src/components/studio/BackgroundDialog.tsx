import { useEffect, useMemo, useRef, useState } from 'react';
import { WarningCircle } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { AssetSummary, Clip } from '../../api/types';
import { timecode } from '../../lib/format';
import { Button, cx, Segmented, Spinner } from '../../ui/primitives';

type With = 'colour' | 'blur' | 'picture' | 'footage';

const KINDS: { value: With; label: string }[] = [
  { value: 'blur', label: 'Blur' },
  { value: 'colour', label: 'Colour' },
  { value: 'picture', label: 'Picture' },
  { value: 'footage', label: 'Another clip' },
];

/** The words a background is said in (as Go's mattes.Words says it). */
export function backgroundWords(clip: Clip, assets: Map<string, AssetSummary>): string {
  const b = clip.background;
  if (!b) return 'its own background';
  switch (b.with) {
    case 'colour':
      return `the colour ${b.colour ?? ''}`.trim();
    case 'blur':
      return 'a blur of the clip itself';
    case 'picture':
      return `the picture “${b.picture ?? ''}”`;
    case 'footage': {
      const a = b.footage ? assets.get(b.footage.assetId) : undefined;
      return a ? `“${a.title}”` : 'another clip';
    }
  }
  return 'something else';
}

/** A clip's background can be replaced when it is footage with a picture (never a call, a nested cut or someone else's video). */
export function canReplaceBackground(clip: Clip, asset: AssetSummary | undefined): boolean {
  if (clip.assetId.startsWith('cut:') || clip.assetId.startsWith('conv:')) return false;
  return !!asset && asset.kind === 'video' && !asset.reference;
}

const safari = () => typeof navigator !== 'undefined' && /^((?!chrome|chromium|android|crios|fxios|edg).)*safari/i.test(navigator.userAgent);

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

/**
 * Replace the background… (D510, D513): what goes behind the people in a clip — a
 * blur of the clip, a colour, a picture from the resources library or
 * another clip — with a still of one moment made on this computer, then the
 * change: the clip's cut-out is made first when it has none (its progress
 * here, with Stop), and the change is saved as a new version once it's ready.
 */
export function BackgroundDialog({
  projectId,
  clip,
  assets,
  onDone,
}: {
  projectId: string;
  clip: Clip;
  assets: Map<string, AssetSummary>;
  onDone: (result?: { summary: string; revisionId: string; number: number; before?: Clip['background'] }) => void;
}) {
  const cur = clip.background;
  const [kind, setKind] = useState<With>((cur?.with as With) ?? 'blur');
  const [colour, setColour] = useState(cur?.colour ?? '#00b140');
  const [blur, setBlur] = useState(cur?.blur ?? 28);
  const [picture, setPicture] = useState(cur?.picture ?? '');
  const [footage, setFootage] = useState(cur?.footage?.assetId ?? '');
  const [footageAt, setFootageAt] = useState(cur?.footage?.in ?? 0);
  const [edges, setEdges] = useState(cur?.feather ?? 2);
  const [error, setError] = useState<string>();
  const [phase, setPhase] = useState<'choosing' | 'saving' | 'making'>('choosing');
  const [job, setJob] = useState<{ id: string; words: string; pct: number }>();
  const stopped = useRef(false);
  const at = Math.round(((clip.in + clip.out) / 2) * 10) / 10;

  const pictures = useOp('resources.list', kind === 'picture' ? { projectId, kind: 'image' } : null);
  const others = useMemo(() => [...assets.values()].filter((a) => a.kind === 'video' && !a.reference && a.id !== clip.assetId), [assets, clip.assetId]);
  useEffect(() => {
    if (kind === 'footage' && !footage && others[0]) setFootage(others[0].id);
    if (kind === 'picture' && !picture && pictures.data?.resources[0]) setPicture(pictures.data.resources[0].name);
  }, [kind, footage, others, picture, pictures.data]);

  const params = useMemo(() => {
    const q = new URLSearchParams({ assetId: clip.assetId, projectId, at: String(at), with: kind, feather: String(edges), width: '640' });
    if (kind === 'colour') q.set('colour', colour);
    if (kind === 'blur') q.set('blur', String(blur));
    if (kind === 'picture') q.set('picture', picture);
    if (kind === 'footage') {
      q.set('footage', footage);
      q.set('footageAt', String(footageAt));
    }
    return q.toString();
  }, [clip.assetId, projectId, at, kind, colour, blur, picture, footage, footageAt, edges]);
  const ready = (kind !== 'picture' || !!picture) && (kind !== 'footage' || !!footage);
  const shown = useDebounced(params, 450);
  const [still, setStill] = useState<'loading' | 'shown' | 'failed'>('loading');
  useEffect(() => setStill('loading'), [shown]);

  const input = () => ({
    projectId,
    clip: clip.id,
    with: kind,
    ...(kind === 'colour' ? { colour } : {}),
    ...(kind === 'blur' ? { blur } : {}),
    ...(kind === 'picture' ? { picture } : {}),
    ...(kind === 'footage' ? { footage, footageAt } : {}),
    feather: edges,
  });

  const apply = async () => {
    setError(undefined);
    setPhase('saving');
    try {
      const out = await call('cut.background', input());
      if (out.waiting && out.cutout?.jobId) {
        setJob({ id: out.cutout.jobId, words: out.cutout.matte.words, pct: out.cutout.matte.pct ?? 0 });
        setPhase('making');
        return;
      }
      if (out.applied) {
        onDone({ summary: out.summary, revisionId: out.applied.revision.id, number: out.applied.revision.number, before: cur });
        return;
      }
      const problems = (out.problems ?? []).filter((p) => p.severity === 'error').map((p) => p.message);
      setError(problems.length ? problems.join(' ') : out.summary);
      setPhase('choosing');
    } catch (e) {
      const ae = toApiError(e);
      setError([ae.message, ae.hint].filter(Boolean).join(' '));
      setPhase('choosing');
    }
  };

  // While the cut-out is made: how far along it is; once ready, the change is saved.
  const cutouts = useOp('footage.cutouts', phase === 'making' ? { assetId: clip.assetId } : null, { refetchInterval: 1500 });
  useEffect(() => {
    if (phase !== 'making' || !job || stopped.current) return;
    const m = cutouts.data?.cutouts.find((c) => c.jobId === job.id);
    if (!m) return;
    if (m.state === 'ready') {
      stopped.current = true;
      void apply().finally(() => (stopped.current = false));
    } else if (m.state === 'failed' || m.state === 'stopped') {
      setError(m.words);
      setPhase('choosing');
    } else if (m.words !== job.words || (m.pct ?? 0) !== job.pct) setJob({ ...job, words: m.words, pct: m.pct ?? 0 });
  }, [cutouts.data, phase, job]); // eslint-disable-line react-hooks/exhaustive-deps

  const stop = () => {
    if (job) void call('job.cancel', { id: job.id }).catch(() => undefined);
    setPhase('choosing');
    setJob(undefined);
  };

  return (
    <div className="space-y-4" data-background-dialog>
      <p className="text-[13px] text-muted">
        MetaRush finds the people in the clip and puts something else behind them, in the preview and the render alike. It is made on a computer of
        this workspace: free, and nothing leaves it.
      </p>
      <Segmented<With> label="What goes behind the people" value={kind} onChange={setKind} items={KINDS} />
      {safari() ? (
        <p className="text-[12.5px] text-subtle" data-safari-note>
          Safari can't show a video's see-through parts, so its preview plays this clip as recorded; the review copy, the final render, Chrome, Edge
          and Firefox show the new background.
        </p>
      ) : null}

      <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_200px]">
        <div className="relative overflow-hidden rounded-[10px] bg-black hairline" style={{ aspectRatio: '16 / 9' }}>
          {ready ? (
            <img
              key={shown}
              src={`/media/cutouts/preview?${shown}`}
              alt={`The clip at ${timecode(at, { ms: false })} with ${kind === 'blur' ? 'its background blurred' : 'the new background'}`}
              className={cx('h-full w-full object-contain transition-opacity', still === 'shown' ? 'opacity-100' : 'opacity-40')}
              onLoad={() => setStill('shown')}
              onError={() => setStill('failed')}
              data-background-still={still}
            />
          ) : null}
          {still === 'loading' && ready ? (
            <div className="absolute inset-0 grid place-items-center">
              <Spinner />
            </div>
          ) : null}
          {still === 'failed' ? (
            <div className="absolute inset-0 grid place-items-center p-4 text-center text-[12.5px] text-white/80">
              This computer can't show a still of it. The change still works where a computer of the workspace can find the people.
            </div>
          ) : null}
        </div>

        <div className="space-y-3 text-[13px]">
          {kind === 'colour' ? (
            <label className="flex items-center gap-2">
              <input type="color" value={colour} onChange={(e) => setColour(e.target.value)} aria-label="The colour" className="h-8 w-10 rounded" />
              <span className="tc text-muted">{colour}</span>
            </label>
          ) : null}
          {kind === 'blur' ? (
            <label className="block">
              <span className="text-muted">How strong</span>
              <input type="range" min={4} max={100} value={blur} onChange={(e) => setBlur(Number(e.target.value))} className="w-full" aria-label="How strong the blur is" />
            </label>
          ) : null}
          {kind === 'picture' ? (
            pictures.data?.resources.length ? (
              <label className="block">
                <span className="text-muted">The picture</span>
                <select value={picture} onChange={(e) => setPicture(e.target.value)} className="mt-1 h-8 w-full rounded-[8px] bg-panel px-2 hairline" aria-label="The picture">
                  {pictures.data.resources.map((r) => (
                    <option key={r.id} value={r.name}>
                      {r.title}
                    </option>
                  ))}
                </select>
              </label>
            ) : (
              <p className="text-subtle">{pictures.isLoading ? 'Looking for pictures…' : 'Add a picture to the project first (drop it on the page).'}</p>
            )
          ) : null}
          {kind === 'footage' ? (
            others.length ? (
              <>
                <label className="block">
                  <span className="text-muted">The clip behind them</span>
                  <select value={footage} onChange={(e) => setFootage(e.target.value)} className="mt-1 h-8 w-full rounded-[8px] bg-panel px-2 hairline" aria-label="The clip behind them">
                    {others.map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.title}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="block">
                  <span className="text-muted">From</span>
                  <input
                    type="number"
                    min={0}
                    step={0.5}
                    value={footageAt}
                    onChange={(e) => setFootageAt(Math.max(0, Number(e.target.value)))}
                    className="mt-1 h-8 w-24 rounded-[8px] bg-panel px-2 hairline"
                    aria-label="Where that clip starts, in seconds"
                  />{' '}
                  <span className="text-subtle">seconds in</span>
                </label>
              </>
            ) : (
              <p className="text-subtle">This project has no other footage yet.</p>
            )
          ) : null}
          <label className="block">
            <span className="text-muted">Soft edges</span>
            <input type="range" min={0.5} max={12} step={0.5} value={edges} onChange={(e) => setEdges(Number(e.target.value))} className="w-full" aria-label="How soft the people's edges are" />
          </label>
        </div>
      </div>

      {phase === 'making' && job ? (
        <div className="flex items-center gap-3 rounded-[10px] bg-accent-soft px-3 py-2 text-[12.5px] text-accent-text" role="status" data-cutout-progress={job.pct}>
          <Spinner />
          <span className="flex-1">{job.words}</span>
          <Button size="sm" variant="ghost" onClick={stop}>
            Stop
          </Button>
        </div>
      ) : null}
      {error ? (
        <p className="flex items-start gap-2 rounded-[10px] bg-danger-soft px-3 py-2 text-[12.5px] text-fg">
          <WarningCircle size={14} className="mt-[2px] shrink-0" /> {error}
        </p>
      ) : null}

      <div className="sticky bottom-[-20px] z-10 -mx-5 -mb-5 flex flex-wrap items-center justify-end gap-2 border-t border-line bg-raised px-5 py-3 sm:-mx-6 sm:-mb-6 sm:bottom-[-24px] sm:px-6">
        <div className="flex-1" />
        <Button variant="ghost" onClick={() => onDone(undefined)}>
          {phase === 'making' ? 'Close' : 'Cancel'}
        </Button>
        <Button variant="primary" loading={phase !== 'choosing'} disabled={!ready} onClick={() => void apply()}>
          {cur ? 'Change the background' : 'Replace the background'}
        </Button>
      </div>
    </div>
  );
}
