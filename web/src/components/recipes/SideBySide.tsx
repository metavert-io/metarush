import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router';
import { ArrowCounterClockwise, ArrowLeft, FilmSlate, Pause, Play, SpeakerHigh } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import { RecoveringVideo } from '../../player/Trouble';
import { clock } from '../../lib/format';
import { Button, cx, ErrorPanel, Freshness, Skeleton, Theirs } from '../../ui/primitives';
import { recipesLink, type RecipeCompareOut, type RecipeCopyView } from './api';
import { CompareDetails } from './Compare';

/*
 * Two runs side by side (?compare=<run>&with=<run>, D212): their review copies
 * playing next to each other — one Play for both, one place to seek, the sound
 * from one at a time — then what changed between them (recipe.compare: the
 * inputs, the notes, the recipe versions, the cost and the cut's changes). On a
 * phone the two copies stack.
 */

export function SideBySide({ left, right }: { left: string; right: string }) {
  const cmp = useOp('recipe.compare', { left, right });
  const recipe = cmp.data?.left.recipe;
  const show = useOp('recipe.show', recipe ? { recipe } : null, { staleTime: 60_000 });
  if (cmp.isLoading) {
    return (
      <div className="space-y-4" aria-busy="true">
        <Skeleton className="h-10 w-80" />
        <Skeleton className="h-72 rounded-[16px]" />
      </div>
    );
  }
  if (cmp.error || !cmp.data) {
    return (
      <div className="space-y-4">
        <Link to={recipesLink()} className="-ml-1 inline-flex items-center gap-1 rounded-[8px] px-1.5 py-1 text-[12.5px] text-muted hover:bg-hover hover:text-fg">
          <ArrowLeft size={13} /> All recipes
        </Link>
        <ErrorPanel error={cmp.error} onRetry={() => void cmp.refetch()} title={cmp.error?.code === 'E_NOT_FOUND' ? 'There is no such run' : undefined} />
      </div>
    );
  }
  const c: RecipeCompareOut = cmp.data;
  const labels = Object.fromEntries((show.data?.inputs ?? []).map((i) => [i.name, i.label]));
  return (
    <div className="space-y-6" data-side-by-side data-left={c.left.number} data-right={c.right.number}>
      <div>
        <div className="mb-3 flex flex-wrap items-center gap-x-1 gap-y-1 text-[12.5px] text-muted">
          <Link to={recipesLink({ recipe: c.left.recipe })} className="-ml-1 inline-flex items-center gap-1 rounded-[8px] px-1.5 py-1 hover:bg-hover hover:text-fg" data-back-to-recipe>
            <ArrowLeft size={13} />
            <span>
              “<Theirs>{c.left.title}</Theirs>”
            </span>
          </Link>
          <Freshness className="ml-auto" at={cmp.dataUpdatedAt || undefined} fetching={cmp.isFetching} onRefresh={() => void cmp.refetch()} />
        </div>
        <h2 className="text-[24px] font-semibold leading-tight tracking-[-0.025em] text-fg sm:text-[28px]">
          Runs {c.left.number} and {c.right.number}, side by side
        </h2>
        <p className="mt-1.5 max-w-[80ch] text-[13.5px] leading-relaxed text-muted">Their review copies play together; the sound comes from one at a time. What changed between them is below.</p>
      </div>
      <Players c={c} />
      <section aria-labelledby="sbs-changes">
        <h3 id="sbs-changes" className="mb-2 text-[15px] font-semibold text-fg">
          What changed between them
        </h3>
        <CompareDetails c={c} labels={labels} />
      </section>
    </div>
  );
}

/** The two review copies, played together. */
function Players({ c }: { c: RecipeCompareOut }) {
  const copies: (RecipeCopyView | null)[] = [c.copies?.[0] ?? null, c.copies?.[1] ?? null];
  const runs = [c.left, c.right];
  const a = useRef<HTMLVideoElement>(null);
  const b = useRef<HTMLVideoElement>(null);
  const refs = [a, b];
  const [playing, setPlaying] = useState(false);
  const [at, setAt] = useState(0);
  const [sound, setSound] = useState(copies[0]?.ready ? 0 : 1);
  const ready = copies.map((x) => !!x?.ready);
  const length = Math.max(0, ...copies.map((x) => (x?.ready ? (x.duration ?? 0) : 0)));
  const videos = () => [a.current, b.current].filter((v): v is HTMLVideoElement => !!v);
  const end = (v: HTMLVideoElement) => (Number.isFinite(v.duration) ? v.duration : Infinity);

  // One sound at a time (the muted property, not the attribute, follows a change).
  useEffect(() => {
    refs.forEach((r, i) => {
      if (r.current) r.current.muted = sound !== i;
    });
  });

  const play = async () => {
    const vs = videos();
    if (!vs.length) return;
    if (vs.every((v) => v.ended)) vs.forEach((v) => (v.currentTime = 0));
    const from = Math.min(...vs.map((v) => v.currentTime));
    vs.forEach((v) => (v.currentTime = Math.min(from, end(v))));
    await Promise.all(vs.map((v) => v.play().catch(() => undefined)));
    setPlaying(true);
  };
  const pause = () => {
    videos().forEach((v) => v.pause());
    setPlaying(false);
  };
  const seek = (t: number) => {
    videos().forEach((v) => (v.currentTime = Math.min(t, end(v))));
    setAt(t);
  };
  // The longer copy leads; the other keeps in step with it while both play.
  const onTime = (i: number) => {
    const me = refs[i]!.current;
    const other = refs[1 - i]!.current;
    if (!me) return;
    if (other && (other.duration || 0) > (me.duration || 0)) return;
    setAt(me.currentTime);
    if (other && !other.paused && other.currentTime < end(other) - 0.05 && Math.abs(other.currentTime - me.currentTime) > 0.3) other.currentTime = me.currentTime;
  };
  const onEnded = () => {
    if (videos().every((v) => v.ended || v.paused)) setPlaying(false);
  };

  if (!ready[0] && !ready[1]) {
    return (
      <div className="rounded-[14px] bg-panel px-4 py-5 text-[13px] text-muted hairline" data-side-by-side-none>
        Neither run has a review copy to play yet{copies.some(Boolean) ? `: ${copies.map((x, i) => (x ? `run ${runs[i]!.number}, ${x.words}` : '')).filter(Boolean).join('; ')}` : ''}.
      </div>
    );
  }
  return (
    <section aria-label="Their review copies" data-side-by-side-players>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        {copies.map((x, i) => {
          const run = runs[i]!;
          const ratio = x?.width && x?.height ? `${x.width} / ${x.height}` : '16 / 9';
          return (
            <figure key={i} className="min-w-0" data-side-copy={run.number} data-copy-ready={x?.ready ? 'yes' : 'no'}>
              <figcaption className="mb-1.5 flex flex-wrap items-center gap-x-2 text-[12.5px]">
                <Link to={recipesLink({ run: run.id })} className="font-semibold text-fg hover:underline">
                  Run {run.number}
                </Link>
                <span className="text-muted">
                  {x?.version ? `version ${x.version} of the cut, ` : ''}
                  {x ? x.words : 'no review copy yet'}
                </span>
                {x?.ready ? (
                  <button
                    type="button"
                    onClick={() => setSound(i)}
                    aria-pressed={sound === i}
                    className={cx(
                      'ml-auto inline-flex h-6 items-center gap-1 rounded-full px-2 text-[11.5px] font-medium',
                      sound === i ? 'bg-accent-soft text-accent-text' : 'text-muted hover:bg-hover hover:text-fg',
                    )}
                    data-sound-from={run.number}
                  >
                    <SpeakerHigh size={12} aria-hidden /> {sound === i ? 'Sound from this one' : 'Hear this one'}
                  </button>
                ) : null}
              </figcaption>
              {x?.ready && x.videoUrl ? (
                <div className="grid place-items-center overflow-hidden rounded-[12px] bg-black">
                  <RecoveringVideo
                    ref={refs[i]}
                    src={x.videoUrl}
                    poster={x.thumbUrl}
                    playsInline
                    preload="metadata"
                    muted={sound !== i}
                    onTimeUpdate={() => onTime(i)}
                    onEnded={onEnded}
                    onPause={() => {
                      if (videos().every((v) => v.paused)) setPlaying(false);
                    }}
                    className="max-h-[62vh] w-full object-contain"
                    style={{ aspectRatio: ratio }}
                    wrapClassName="w-full"
                    aria-label={`Run ${run.number}'s review copy`}
                  />
                </div>
              ) : (
                <div className="grid aspect-video place-items-center rounded-[12px] bg-inset text-[12.5px] text-subtle">
                  <span className="flex flex-col items-center gap-1.5 px-4 text-center">
                    <FilmSlate size={22} aria-hidden />
                    {x ? `${x.words.charAt(0).toUpperCase()}${x.words.slice(1)}.` : 'This run has no review copy yet.'}
                  </span>
                </div>
              )}
            </figure>
          );
        })}
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[12px] bg-panel px-3 py-2 hairline" data-side-by-side-controls>
        <Button
          size="sm"
          variant="primary"
          icon={playing ? <Pause size={13} weight="fill" /> : <Play size={13} weight="fill" />}
          onClick={() => void (playing ? pause() : play())}
          data-play-both={playing ? 'playing' : 'paused'}
        >
          {playing ? 'Pause both' : 'Play both'}
        </Button>
        <Button size="sm" variant="ghost" icon={<ArrowCounterClockwise size={13} />} onClick={() => seek(0)} data-both-from-start>
          From the start
        </Button>
        <input
          type="range"
          min={0}
          max={Math.max(length, 0.1)}
          step={0.1}
          value={Math.min(at, length)}
          onChange={(e) => seek(Number(e.target.value))}
          aria-label="Where both play"
          className="h-1 min-w-[120px] flex-1 accent-[var(--accent)]"
          data-both-seek
        />
        <span className="tc text-[12px] text-muted" aria-live="off">
          {clock(at)} / {clock(length)}
        </span>
      </div>
    </section>
  );
}
