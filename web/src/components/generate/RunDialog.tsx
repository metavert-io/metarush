import { useEffect, useId, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../../api/client';
import type { GenEstimate, GenRunOut } from '../../api/types';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { useEstimatesFirst } from '../../lib/preferences';
import { Button, ErrorPanel, Segmented, Spinner } from '../../ui/primitives';
import { estimateWords } from '../../lib/costs';
import { EstimateLines } from './EstimateLines';

/*
 * Generate again, Variations, Extend and Edit (D125): each makes new clips
 * and never replaces one. The dialog prices the request first (a dry run of
 * the same op) and the button carries the price; nothing is sent until the
 * person presses it. With their estimates off (D208) the button doesn't wait
 * for the price: the server prices it and the budgets still apply.
 */

export type RunKind = 'again' | 'variations' | 'extend' | 'edit';

export const GENERATE_QUERIES = ['generate.list', 'generate.show', 'project.show', 'activity.summary', 'asset.show', 'library.list'] as const;

const titles: Record<RunKind, string> = {
  again: 'Generate again',
  variations: 'Variations',
  extend: 'Continue this clip',
  edit: 'Change this clip',
};

function opFor(kind: RunKind): 'generate.again' | 'generate.extend' | 'generate.edit' {
  return kind === 'extend' ? 'generate.extend' : kind === 'edit' ? 'generate.edit' : 'generate.again';
}

function RunForm({ kind, id, onClose }: { kind: RunKind; id: string; onClose: (out?: GenRunOut) => void }) {
  const uid = useId();
  const [takes, setTakes] = useState(kind === 'variations' ? '3' : '1');
  const [seconds, setSeconds] = useState(6);
  const [text, setText] = useState('');
  const [quality, setQuality] = useState<'' | 'draft' | 'final'>('');
  const [est, setEst] = useState<GenEstimate | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [pricing, setPricing] = useState(false);
  const [busy, setBusy] = useState(false);
  const estimatesFirst = useEstimatesFirst();
  const needsText = kind === 'edit';

  const input = (dry: boolean, maxUsd?: number) => {
    const base = { id, dryRun: dry, ...(maxUsd !== undefined ? { maxUsd } : {}), ...(quality ? { quality } : {}) };
    if (kind === 'extend') return { ...base, seconds, ...(text.trim() ? { prompt: text.trim() } : {}) };
    if (kind === 'edit') return { ...base, instruction: text.trim() };
    return { ...base, takes: Number(takes) };
  };

  useEffect(() => {
    if (needsText && !text.trim()) {
      setEst(null);
      return;
    }
    const t = setTimeout(async () => {
      setPricing(true);
      setError(null);
      try {
        const out = (await call(opFor(kind), input(true) as never)) as GenRunOut;
        setEst(out.estimate);
      } catch (e) {
        setEst(null);
        setError(e);
      } finally {
        setPricing(false);
      }
    }, 400);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind, id, takes, seconds, text, quality]);

  const ready = estimatesFirst ? !!est && est.allowed && !pricing : !(needsText && !text.trim()) && !(est && !est.allowed);
  const go = async () => {
    if (!ready) return;
    setBusy(true);
    setError(null);
    try {
      const out = (await call(opFor(kind), input(false, estimatesFirst ? est?.usd : undefined) as never)) as GenRunOut;
      onClose(out);
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };

  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        void go();
      }}
    >
      <p className="text-[13.5px] leading-relaxed text-muted">
        {kind === 'again'
          ? 'A new take of the same shot: the same words and pictures, made again. It comes out different every time.'
          : kind === 'variations'
            ? 'Several new takes of the same shot at once, to choose from. Each is its own clip.'
            : kind === 'extend'
              ? 'A new clip that carries on from the end of this one. This clip stays as it is.'
              : 'A changed copy of this clip, made by Google Gemini from your instruction. This clip stays as it is.'}
      </p>
      {kind === 'variations' || kind === 'again' ? (
        <div>
          <div className="mb-1.5 text-[12.5px] font-medium text-muted">How many takes</div>
          <Segmented label="How many takes" value={takes} onChange={setTakes} items={['1', '2', '3', '4'].map((v) => ({ value: v, label: v }))} />
        </div>
      ) : null}
      {kind === 'extend' ? (
        <div>
          <label htmlFor={`${uid}-s`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
            How many more seconds: {seconds}
          </label>
          <input id={`${uid}-s`} type="range" min={3} max={10} step={1} value={seconds} onChange={(e) => setSeconds(Number(e.target.value))} className="w-full accent-[var(--accent)]" />
        </div>
      ) : null}
      {kind === 'extend' || kind === 'edit' ? (
        <div>
          <label htmlFor={`${uid}-t`} className="mb-1.5 block text-[12.5px] font-medium text-muted">
            {kind === 'edit' ? 'What to change' : 'What happens next (optional)'}
          </label>
          <textarea
            id={`${uid}-t`}
            data-autofocus
            rows={3}
            maxLength={4000}
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={kind === 'edit' ? 'Make it night, with the street lights on' : 'The boat reaches the far shore'}
            className="w-full resize-y rounded-[10px] bg-panel px-3 py-2 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
          />
        </div>
      ) : null}
      <div>
        <div className="mb-1.5 text-[12.5px] font-medium text-muted">Quality</div>
        <Segmented
          label="Quality"
          value={quality || 'same'}
          onChange={(v) => setQuality(v === 'same' ? '' : (v as 'draft' | 'final'))}
          items={[
            { value: 'same', label: kind === 'again' || kind === 'variations' ? 'As before' : 'Usual' },
            { value: 'draft', label: 'Draft' },
            { value: 'final', label: 'Final' },
          ]}
        />
      </div>
      <div className="min-h-[64px] rounded-[12px] bg-hover/60 px-3.5 py-3" aria-live="polite">
        {pricing && !est ? (
          <span className="inline-flex items-center gap-2 text-[13px] text-muted">
            <Spinner size={12} /> Pricing it…
          </span>
        ) : est ? (
          <EstimateLines estimate={est} />
        ) : needsText ? (
          <span className="text-[13px] text-subtle">Say what to change to see the price.</span>
        ) : null}
      </div>
      {error ? <ErrorPanel compact error={error} /> : null}
      <div className="flex flex-wrap justify-end gap-2">
        <Button variant="ghost" onClick={() => onClose()} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" loading={busy} disabled={!ready}>
          {est ? `Make it (${estimateWords(est.usd)})` : 'Make it'}
        </Button>
      </div>
    </form>
  );
}

/** Opens the dialog for one of the four ways to make new takes, and says what happened. */
export function useGenerateRuns() {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  return async (kind: RunKind, id: string) => {
    const out = await dialog.open<GenRunOut>((close) => <RunForm kind={kind} id={id} onClose={close} />, { title: titles[kind], size: 'md' });
    if (!out) return;
    GENERATE_QUERIES.forEach((q) => void qc.invalidateQueries({ queryKey: [q] }));
    toast({ tone: 'success', title: titles[kind], body: out.summary });
  };
}

/** Stop a generated video that is waiting or being made. */
export function useStopGeneration() {
  const toast = useToast();
  const qc = useQueryClient();
  return async (id: string) => {
    try {
      const out = await call('generate.cancel', { id });
      GENERATE_QUERIES.forEach((q) => void qc.invalidateQueries({ queryKey: [q] }));
      toast({ tone: 'info', title: 'Stopping it', body: out.summary });
    } catch (e) {
      toast({ tone: 'error', title: 'Could not stop it', body: toApiError(e).message });
    }
  };
}
