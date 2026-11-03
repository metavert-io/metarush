import { useEffect, useId, useRef, useState } from 'react';
import { Link } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowRight, FilmReel, Plus } from '@phosphor-icons/react';
import type { ops } from '@metarush/schema';
import { call, toApiError } from '../../api/client';
import { uploads } from '../../api/uploads';
import { Button, cx, Spinner, Theirs } from '../../ui/primitives';
import { findLinks } from '../LinkImports';
import { WhatItDoesView } from '../WhatItDoes';
import { recipesLink } from './api';

/*
 * "Start from a video you like" (D403): the Add box for a video someone else
 * made. A link (YouTube, TikTok, X) or a video file comes in as a reference —
 * kept to learn from, never played in a cut — and once MetaRush has looked at
 * it, recipe.fromVideo's draft is saved: a recipe shaped like the video, for
 * the person's own footage. The box follows it here: bringing it in (the
 * import's job), looking at it (the clip's card, filling in), then what the
 * video does and the draft, with Open the draft. Recipes tab and Home's Start
 * from show it; the CLI's `metarush recipe from-video` is the same op.
 */

type Out = ops.RecipeFromVideoOut;

type Run = {
  from: string;
  label: string;
  out?: Out;
  /** The clip, once it is in, and its title. */
  assetId?: string;
  title?: string;
  card?: ops.WhatItDoes;
  stage: 'adding' | 'importing' | 'looking' | 'drafted' | 'failed';
  /** The draft was made before this was asked (the same video, asked again). */
  earlier?: boolean;
  problem?: string;
  hint?: string;
};

const POLL_MS = 2500;

export function StartFromVideo({ className, compact }: { className?: string; compact?: boolean }) {
  const [value, setValue] = useState('');
  const [problem, setProblem] = useState('');
  const [run, setRun] = useState<Run>();
  const fileInput = useRef<HTMLInputElement>(null);
  const qc = useQueryClient();
  const id = useId();
  const busy = !!run && run.stage !== 'drafted' && run.stage !== 'failed';

  const start = async (from: string, label: string, reference = false) => {
    setRun({ from, label, stage: 'adding' });
    try {
      const out = await call('recipe.fromVideo', { from, reference });
      apply(from, label, out, !!out.existing);
    } catch (e) {
      const err = toApiError(e);
      setRun({ from, label, stage: 'failed', problem: err.message, hint: err.hint });
    }
  };

  const apply = (from: string, label: string, out: Out, earlier: boolean) => {
    const stage = out.state === 'drafted' ? 'drafted' : out.state === 'importing' ? 'importing' : 'looking';
    setRun({ from, label, out, stage, earlier, assetId: out.clip?.id, title: out.clip?.title, card: out.card ?? undefined });
    if (stage === 'drafted') for (const k of ['recipe.list', 'asset.list', 'library.list']) void qc.invalidateQueries({ queryKey: [k] });
  };

  // Follow it: the import's job until the clip is in, then the clip's card
  // until its draft is saved (both only read), then the draft once.
  useEffect(() => {
    if (!run || run.stage === 'drafted' || run.stage === 'failed' || run.stage === 'adding') return;
    let stopped = false;
    const timer = window.setTimeout(async () => {
      try {
        if (run.stage === 'importing' && !run.assetId) {
          const jobId = run.out?.import?.jobIds?.[0] ?? run.out?.jobIds?.[0];
          if (!jobId) return;
          const j = await call('job.show', { id: jobId });
          if (stopped) return;
          if (j.state === 'succeeded') {
            const res = (j.result ?? {}) as Record<string, unknown>;
            const assetId = typeof res.assetId === 'string' ? res.assetId : undefined;
            setRun({ ...run, assetId, stage: assetId ? 'looking' : 'failed', problem: assetId ? undefined : 'The video came in, but MetaRush can’t find its clip.' });
          } else if (j.state === 'failed' || j.state === 'canceled') {
            setRun({ ...run, stage: 'failed', problem: j.advice || 'The video couldn’t be brought in.' });
          } else setRun({ ...run });
          return;
        }
        if (!run.assetId) return;
        const c = await call('footage.whatItDoes', { assetId: run.assetId });
        if (stopped) return;
        const sf = c.clip.startFrom;
        if (sf?.state === 'made') {
          // The draft this box followed: made just now, whatever the answer calls it.
          const out = await call('recipe.fromVideo', { from: run.assetId });
          if (!stopped) apply(run.from, run.label, out, false);
        } else if (sf?.state === 'failed') {
          setRun({ ...run, title: c.clip.title, card: c.card, stage: 'failed', problem: sf.problem || 'The draft couldn’t be made.' });
        } else setRun({ ...run, title: c.clip.title, card: c.card, stage: 'looking' });
      } catch (e) {
        if (!stopped) {
          const err = toApiError(e);
          if (err.code === 'E_CONFLICT' && /isn’t ready|isn't ready/.test(err.message)) setRun({ ...run });
          else setRun({ ...run, stage: 'failed', problem: err.message, hint: err.hint });
        }
      }
    }, POLL_MS);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, [run]); // eslint-disable-line react-hooks/exhaustive-deps

  const submit = () => {
    const links = findLinks(value.includes('://') ? value : value ? `https://${value.trim()}` : '');
    if (!links.length) {
      setProblem('Paste the link to one video: YouTube, TikTok, X or another video site.');
      return;
    }
    setProblem('');
    setValue('');
    void start(links[0]!, links[0]!);
  };

  // A video file on this computer: sent like a drop, then started from as a reference.
  const sendFile = async (file: File) => {
    setRun({ from: file.name, label: file.name, stage: 'adding' });
    const before = new Set(uploads.getSnapshot().map((u) => u.key));
    void uploads.add([{ file }], { label: 'the Inbox' });
    const assetId = await new Promise<string>((resolve, reject) => {
      const check = () => {
        const it = uploads.getSnapshot().find((u) => !before.has(u.key) && u.name === file.name);
        if (!it) return;
        if (['done', 'skipped', 'aliased'].includes(it.state)) {
          const id = it.result?.assetId ?? it.duplicate?.assetId;
          stop();
          if (id) resolve(id);
          else reject(new Error('The file came in, but MetaRush can’t find its clip.'));
        } else if (['failed', 'canceled'].includes(it.state)) {
          stop();
          reject(new Error(it.error || 'The file couldn’t be added.'));
        }
      };
      const stop = uploads.subscribe(check);
      check();
    }).catch((e: Error) => {
      setRun({ from: file.name, label: file.name, stage: 'failed', problem: e.message });
      return '';
    });
    if (assetId) await start(assetId, file.name, true);
  };

  const field = (
    <form
      className="flex min-w-0 flex-1 gap-1.5"
      aria-label="Start from a video you like"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <input
        id={id}
        type="text"
        inputMode="url"
        autoComplete="off"
        spellCheck={false}
        value={value}
        disabled={busy}
        onChange={(e) => {
          setValue(e.target.value);
          setProblem('');
        }}
        aria-label="A link to a video you like"
        aria-invalid={!!problem || undefined}
        placeholder="Paste a link to a video you like"
        className={cx('h-9 min-w-0 flex-1 rounded-[10px] bg-panel px-3 text-[13px] text-fg outline-none placeholder:text-subtle hairline focus:ring-2 focus:ring-accent', problem && 'ring-2 ring-danger')}
        data-start-from-video-link
      />
      <Button type="submit" variant="primary" size="md" disabled={!value.trim() || busy} data-start-from-video-go>
        Start
      </Button>
    </form>
  );

  return (
    <section aria-labelledby={`${id}-h`} className={cx('rounded-[16px] border border-dashed border-line-strong bg-raised px-4 py-4', className)} data-start-from-video>
      <div className="flex items-start gap-3">
        <div className="grid h-9 w-9 shrink-0 place-items-center rounded-[11px] bg-accent-soft text-accent-text">
          <FilmReel size={18} weight="bold" />
        </div>
        <div className="min-w-0 flex-1">
          <h2 id={`${id}-h`} className="text-[14px] font-medium text-fg">
            Start from a video you like
          </h2>
          {!compact ? (
            <p className="mt-0.5 text-[12.5px] leading-relaxed text-muted">
              A link or a video file comes in as a reference: kept to learn from, never played in a cut. MetaRush looks at what it does and drafts a recipe shaped like it, for your own
              footage. Nothing of the video goes into the draft.
            </p>
          ) : null}
        </div>
      </div>
      <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-center">
        {field}
        <input
          ref={fileInput}
          type="file"
          accept="video/*"
          className="hidden"
          aria-hidden
          tabIndex={-1}
          data-start-from-video-file
          onChange={(e) => {
            const f = e.target.files?.[0];
            e.target.value = '';
            if (f) void sendFile(f);
          }}
        />
        <Button size="md" variant="secondary" icon={<Plus size={14} weight="bold" />} disabled={busy} onClick={() => fileInput.current?.click()}>
          Choose a video file
        </Button>
      </div>
      {problem ? (
        <p className="mt-2 text-[12.5px] text-danger" role="alert">
          {problem}
        </p>
      ) : null}
      {run ? <Progress run={run} /> : null}
    </section>
  );
}

function Progress({ run }: { run: Run }) {
  const what = run.title ? (
    <>
      “<Theirs>{run.title}</Theirs>”
    </>
  ) : (
    'the video'
  );
  return (
    <div className="mt-4 border-t border-line pt-4" data-start-from-video-result={run.stage} aria-live="polite">
      {run.stage === 'adding' ? (
        <div className="flex items-center gap-2 text-[13px] text-muted">
          <Spinner size={12} />
          <span className="min-w-0">
            Bringing in “<Theirs>{run.label}</Theirs>”…
          </span>
        </div>
      ) : run.stage === 'importing' ? (
        <div className="flex items-center gap-2 text-[13px] text-muted">
          <Spinner size={12} />
          <span className="min-w-0">Bringing the video in as a reference…</span>
        </div>
      ) : run.stage === 'looking' ? (
        <div className="space-y-3">
          <div className="flex items-center gap-2 text-[13px] text-muted">
            <Spinner size={12} />
            <span className="min-w-0">MetaRush is looking at {what}. The draft is saved by itself once it has; you can leave this page.</span>
          </div>
          {run.card && run.card.sections.length ? <WhatItDoesView card={run.card} /> : null}
        </div>
      ) : run.stage === 'failed' ? (
        <div className="text-[13px]" role="alert">
          <p className="text-danger">{run.problem}</p>
          {run.hint ? <p className="mt-1 text-muted">{run.hint}</p> : null}
        </div>
      ) : run.out ? (
        <Drafted out={run.out} earlier={!!run.earlier} />
      ) : null}
    </div>
  );
}

function Drafted({ out, earlier }: { out: Out; earlier: boolean }) {
  const r = out.recipe;
  return (
    <div className="space-y-4" data-start-from-video-drafted>
      {r ? (
        <div className="flex flex-wrap items-center gap-3 rounded-[12px] bg-accent-soft px-3 py-2.5">
          <div className="min-w-0 flex-1 text-[13px] leading-relaxed text-fg">
            <span className="font-medium">
              {earlier ? 'Drafted earlier: ' : 'Drafted: '}
              <Theirs>{r.title}</Theirs>
            </span>
            <span className="text-muted"> — {r.makes}. A draft: a person makes it current before it runs.</span>
          </div>
          <Link
            to={recipesLink({ recipe: r.name, version: out.version?.number })}
            className="inline-flex items-center gap-1 rounded-[10px] bg-accent px-3 py-1.5 text-[12.5px] font-medium text-accent-ink"
            data-open-draft
          >
            Open the draft <ArrowRight size={12} />
          </Link>
        </div>
      ) : null}
      {out.card ? (
        <div>
          <h3 className="mb-2 text-[12.5px] font-medium uppercase tracking-[0.04em] text-subtle">What the video does</h3>
          <WhatItDoesView card={out.card} />
        </div>
      ) : null}
    </div>
  );
}
