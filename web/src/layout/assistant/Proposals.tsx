import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ArrowSquareOut, CaretDown, CaretRight, CheckCircle, FilmSlate, GitDiff, Play, WarningCircle } from '@phosphor-icons/react';
import { useAssistantOp, useConversationProposals } from '../../api/assistant';
import { NotQuiteBox } from './NotQuite';
import { ApiError, call } from '../../api/client';
import type { EditApplyOut, PlanProposal, RenderOut, RenderProposal } from '../../api/types';
import { duration, plural, signedDuration } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, ErrorPanel, InlineCode, Skeleton, cx } from '../../ui/primitives';

/** The plans and render requests one turn wrote, re-checked against the current head. */
export function TurnProposalCards({
  conversationId,
  turn,
  here,
  machine,
  covered,
  onNavigate,
}: {
  conversationId: string;
  turn: number;
  here: boolean;
  machine: string;
  /** Plans an action card already shows (Claude asked to apply them): one card per plan. */
  covered?: Set<string>;
  onNavigate: () => void;
}) {
  const q = useConversationProposals(conversationId, here);
  if (!here) return <p className="rounded-[12px] px-3.5 py-3 text-[12.5px] text-muted hairline">The proposals from this message are on {machine}.</p>;
  if (q.isLoading) return <Skeleton className="h-32 rounded-[14px]" />;
  if (q.error) return <ErrorPanel error={q.error} compact onRetry={() => void q.refetch()} />;
  const t = q.data?.turns.find((x) => x.turn === turn);
  const plans = t?.plans.filter((p) => !covered?.has(p.ref)) ?? [];
  if (!t || (plans.length === 0 && t.renders.length === 0 && !t.problems?.length)) return null;
  return (
    <section aria-label="Proposed changes" className="space-y-2.5">
      {plans.map((p) => (
        <PlanCard key={p.file} p={p} conversationId={conversationId} onNavigate={onNavigate} renderRequested={t.renders.some((r) => r.plan === p.file)} />
      ))}
      {t.renders.map((r) => (
        <RenderCard key={r.file} r={r} onNavigate={onNavigate} />
      ))}
      {t.problems?.length ? (
        <ul className="space-y-1 text-[11.5px] text-subtle">
          {t.problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      ) : null}
    </section>
  );
}

/** The change in plain words: "1.5s shorter (now 15.2s), 1 clip changed." */
function plainDiff(d: NonNullable<NonNullable<PlanProposal['check']>['diff']>): string {
  const parts: string[] = [];
  const delta = d.durationAfter - d.durationBefore;
  if (delta < -0.05) parts.push(`${Math.abs(delta).toFixed(1)}s shorter (now ${duration(d.durationAfter)})`);
  else if (delta > 0.05) parts.push(`${delta.toFixed(1)}s longer (now ${duration(d.durationAfter)})`);
  else parts.push(`Same length (${duration(d.durationAfter)})`);
  const clips = (d.clipsChanged?.length ?? 0) + (d.clipsAdded?.length ?? 0) + (d.clipsRemoved?.length ?? 0);
  if (clips) parts.push(plural(clips, 'clip') + ' changed');
  const overlays = (d.overlaysChanged?.length ?? 0) + (d.overlaysAdded?.length ?? 0) + (d.overlaysRemoved?.length ?? 0);
  if (overlays) parts.push(plural(overlays, 'graphic') + ' changed');
  if (d.reordered) parts.push('shots reordered');
  return parts.join(', ') + '.';
}

function planTitle(p: PlanProposal): string {
  const t = p.instruction?.trim() || p.file.replace(/\.plan\.json$/, '').replace(/[-_]+/g, ' ');
  return t.charAt(0).toUpperCase() + t.slice(1);
}

function PlanCard({ p, conversationId, onNavigate, renderRequested }: { p: PlanProposal; conversationId: string; onNavigate: () => void; renderRequested: boolean }) {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [expanded, setExpanded] = useState(false);
  const [details, setDetails] = useState(false);
  // Not quite (D186): what should change goes to Claude as the person's message.
  const [notQuite, setNotQuite] = useState(false);
  const cont = useAssistantOp('assistant.continue');
  const sendNotQuite = (note: string) =>
    cont.mutate(
      { id: conversationId, prompt: `Not quite: ${planTitle(p).replace(/\.$/, '')}. ${/[.!?…]$/.test(note) ? note : `${note}.`} Make a new version with that change.` },
      { onSuccess: () => setNotQuite(false) },
    );
  const check = p.check;
  const diff = check?.diff;
  const apply = useMutation<EditApplyOut, ApiError, void>({
    mutationFn: () => call('edit.apply', { plan: p.plan ?? {} }),
    onSuccess: (out) => {
      for (const k of ['revision.list', 'project.show', 'project.list', 'revision.show']) void qc.invalidateQueries({ queryKey: [k] });
      void qc.invalidateQueries({ queryKey: ['assistant', 'proposals'] });
      toast({
        tone: 'success',
        title: `Saved as version ${out.revision.number}`,
        body: out.rebased
          ? "Someone saved a newer version meanwhile; this change doesn't overlap it, so both are kept."
          : 'Saved as a new version of the cut. Nothing was overwritten.',
        action: { label: 'Open', onClick: () => go(`/p/${out.revision.projectId}/r/${out.revision.id}`) },
      });
    },
  });
  const go = (to: string) => {
    onNavigate();
    navigate(to);
  };
  // A plan written against a head that has since moved (e.g. a sibling alternative was applied).
  const stale = p.error?.code === 'E_REVISION_CONFLICT';
  const state = p.applied ? 'applied' : stale ? 'stale' : p.error ? 'blocked' : check?.valid ? 'valid' : 'invalid';
  // The duration change has its own line; Go's summary repeats it.
  const summary = diff?.summary.filter((s) => !/^duration /.test(s)) ?? [];
  const badge =
    state === 'applied' ? (
      <Badge tone="ok" icon={<CheckCircle size={12} weight="fill" />}>
        Saved as version {p.applied!.number}
      </Badge>
    ) : state === 'valid' ? (
      <Badge tone="accent">Ready to apply</Badge>
    ) : state === 'stale' ? (
      <Badge tone="neutral">Out of date</Badge>
    ) : state === 'blocked' ? (
      <Badge tone="danger">Cannot apply</Badge>
    ) : (
      <Badge tone="danger">Needs fixes</Badge>
    );

  const onApply = async () => {
    const ok = await dialog.confirm({
      title: 'Save this change as a new version?',
      body: (
        <>
          <p>
            Saves the change as a new version of the cut{diff ? ` (${duration(diff.durationBefore)} → ${duration(diff.durationAfter)})` : ''}. The current version stays in the
            history, nothing is overwritten, and you can approve or revise the new one later.
          </p>
          {check?.warnings.length ? <p className="mt-2">{check.warnings.length === 1 ? 'Its note stays' : `Its ${check.warnings.length} notes stay`} with the new version.</p> : null}
        </>
      ),
      confirmLabel: 'Apply plan',
    });
    if (ok) apply.mutate();
  };

  return (
    <article className="rounded-[14px] bg-raised p-3.5 hairline" aria-label={`Plan: ${planTitle(p)}`}>
      <div className="flex items-start gap-2.5">
        <span className="mt-[1px] grid h-7 w-7 shrink-0 place-items-center rounded-[9px] bg-accent-soft text-accent-text" aria-hidden>
          <GitDiff size={15} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <h4 className="min-w-0 text-[13.5px] font-semibold leading-snug text-fg">{planTitle(p)}</h4>
            {badge}
          </div>
          {/* The plain summary below says what changed; the count is for cards without it. */}
          {p.kind !== 'ops' || !diff || state === 'applied' || state === 'stale' ? (
            <div className="mt-0.5 truncate text-[11.5px] text-subtle">
              {p.kind === 'ops' ? plural(p.opCount, 'change') : p.kind === 'spec' ? 'A whole new cut' : 'Could not be read'}
            </div>
          ) : null}
        </div>
      </div>
      {p.rationale ? (
        <div className="mt-2.5">
          <p className={cx('text-[12.5px] leading-relaxed text-muted', !expanded && 'line-clamp-3')}>{p.rationale}</p>
          {p.rationale.length > 220 ? (
            <button
              type="button"
              className="mt-0.5 text-[12px] font-medium text-accent-text hover:underline"
              onClick={() => setExpanded((v) => !v)}
              aria-expanded={expanded}
            >
              {expanded ? 'Show less' : 'Show more'}
            </button>
          ) : null}
        </div>
      ) : null}
      {diff && state !== 'applied' && state !== 'stale' ? (
        <p className="mt-2.5 text-[12.5px] leading-relaxed text-fg">
          {plainDiff(diff)}
          {check && check.errors.length > 0 ? <span className="text-danger"> {plural(check.errors.length, 'problem')} to fix first.</span> : null}
        </p>
      ) : null}
      {(diff || (check && (check.errors.length > 0 || check.warnings.length > 0))) && state !== 'applied' && state !== 'stale' ? (
        <div className="mt-2">
          <button type="button" onClick={() => setDetails((v) => !v)} aria-expanded={details} className="flex items-center gap-1 text-[12px] font-medium text-muted hover:text-fg">
            {details ? <CaretDown size={11} /> : <CaretRight size={11} />}
            Details
            {check && check.warnings.length > 0 ? <span className="font-normal text-subtle">({plural(check.warnings.length, 'note')})</span> : null}
          </button>
          {details ? (
            <div className="mt-2 space-y-2">
              {diff ? (
                <div className="rounded-[10px] bg-panel px-3 py-2.5 hairline">
                  <div className="tc flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-[12px]">
                    <span className="text-fg">
                      {duration(diff.durationBefore)} → {duration(diff.durationAfter)}
                    </span>
                    <span className={cx(diff.durationAfter < diff.durationBefore - 0.05 ? 'text-ok' : 'text-subtle')}>{signedDuration(diff.durationAfter - diff.durationBefore)}</span>
                    {check?.rebased ? <span className="text-info">updated for the latest cut</span> : null}
                  </div>
                  {summary.length ? (
                    <ul className="mt-1.5 space-y-0.5">
                      {summary.map((s, i) => (
                        <li key={i} className="flex gap-2 text-[12.5px] leading-relaxed text-fg">
                          <span className="mt-[8px] h-1 w-1 shrink-0 rounded-full bg-accent" aria-hidden />
                          {s}
                        </li>
                      ))}
                    </ul>
                  ) : null}
                </div>
              ) : null}
              {check && (check.errors.length > 0 || check.warnings.length > 0) ? (
                <ul className="space-y-1">
                  {[...check.errors.map((i) => ({ ...i, tone: 'danger' as const })), ...check.warnings.map((i) => ({ ...i, tone: 'warn' as const }))].map((i, k) => (
                    <li key={k} className={cx('flex gap-2 text-[12px] leading-relaxed', i.tone === 'danger' ? 'text-danger' : 'text-muted')}>
                      <WarningCircle size={13} weight="fill" className={cx('mt-[2px] shrink-0', i.tone === 'warn' && 'text-accent-text')} aria-hidden />
                      <span className="min-w-0">
                        <span className="sr-only">{i.tone === 'danger' ? 'Problem: ' : 'Note: '}</span>
                        <InlineCode text={i.message} />
                        {i.path ? <span className="tc ml-1 text-[11px] text-subtle">{i.path}</span> : null}
                      </span>
                    </li>
                  ))}
                </ul>
              ) : null}
            </div>
          ) : null}
        </div>
      ) : null}
      {p.error && stale ? (
        <p className="mt-2.5 rounded-[10px] bg-panel px-3 py-2.5 text-[12.5px] leading-relaxed text-muted hairline">
          The cut changed after this plan was written, in the same places. Ask Claude for a fresh plan.
        </p>
      ) : p.error && state === 'blocked' ? (
        <div className="mt-2.5">
          <ErrorPanel error={new ApiError(p.error, 0)} compact />
        </div>
      ) : null}
      {apply.error ? (
        <div className="mt-2.5">
          <ErrorPanel error={apply.error} compact title="Could not apply the plan" />
        </div>
      ) : null}
      <div className={cx('mt-3 flex flex-wrap items-center gap-2', state === 'stale' && 'hidden')}>
        {state === 'applied' ? (
          <>
            <Button
              size="sm"
              variant="secondary"
              icon={<ArrowSquareOut size={13} />}
              onClick={() => go(`/p/${p.applied!.projectId}/r/${p.applied!.revisionId}`)}
            >
              Open version {p.applied!.number}
            </Button>
            {renderRequested ? null : <RenderButton revisionId={p.applied!.revisionId} number={p.applied!.number} quality="review" onNavigate={onNavigate} />}
          </>
        ) : (
          <>
            <Button size="sm" variant="primary" disabled={state !== 'valid'} loading={apply.isPending} onClick={() => void onApply()}>
              Apply plan
            </Button>
            {!notQuite ? (
              <Button size="sm" variant="ghost" disabled={apply.isPending} onClick={() => setNotQuite(true)} title="Say what should change: Claude makes a new version" data-not-quite>
                Not quite…
              </Button>
            ) : null}
          </>
        )}
        {state === 'invalid' ? <span className="text-[11.5px] text-subtle">Ask Claude to fix it.</span> : null}
      </div>
      {notQuite && state !== 'applied' ? <NotQuiteBox onSend={sendNotQuite} onCancel={() => setNotQuite(false)} busy={cont.isPending} error={cont.error} /> : null}
    </article>
  );
}

function RenderCard({ r, onNavigate }: { r: RenderProposal; onNavigate: () => void }) {
  const navigate = useNavigate();
  return (
    <article
      className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[14px] bg-raised p-3.5 hairline"
      aria-label={`Render request: ${r.quality === 'final' ? 'the final render' : 'a review copy'}${r.revisionNumber ? ` of version ${r.revisionNumber}` : ''}`}
    >
      <span className="grid h-7 w-7 shrink-0 place-items-center rounded-[9px] bg-info-soft text-info" aria-hidden>
        <FilmSlate size={15} />
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-semibold text-fg">
          {r.quality === 'final' ? 'Final render' : 'Review copy'}
          {r.revisionNumber ? ` of version ${r.revisionNumber}` : r.plan ? ' of the applied change' : ''}
        </div>
        <div className="text-[12px] leading-relaxed text-muted">
          {r.error ? <span className="text-danger">{r.error}</span> : r.waiting ? r.waiting : r.reason || 'Claude suggests rendering it so you can watch the result.'}
        </div>
      </div>
      {r.renderId ? (
        <Button
          size="sm"
          variant="secondary"
          icon={<Play size={13} weight="fill" />}
          onClick={() => {
            onNavigate();
            navigate(`/renders/${r.renderId}`);
          }}
        >
          {r.renderState === 'available' ? 'Watch' : 'Open render'}
        </Button>
      ) : r.ready && r.revisionId ? (
        <RenderButton revisionId={r.revisionId} number={r.revisionNumber} quality={r.quality} preset={r.preset} onNavigate={onNavigate} />
      ) : null}
    </article>
  );
}

function RenderButton({
  revisionId,
  number,
  quality,
  preset,
  onNavigate,
}: {
  revisionId: string;
  number?: number;
  quality: 'review' | 'final';
  preset?: string;
  onNavigate: () => void;
}) {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const submit = useMutation<RenderOut, ApiError, void>({
    mutationFn: () =>
      call('render.submit', { revisionId, quality, ...(preset ? { preset: preset as 'vertical-1080p' | 'landscape-1080p' | 'square-1080' } : {}) }),
    onSuccess: (out) => {
      for (const k of ['render.list', 'job.list', 'project.show', 'revision.show']) void qc.invalidateQueries({ queryKey: [k] });
      void qc.invalidateQueries({ queryKey: ['assistant', 'proposals'] });
      toast({
        tone: out.note && !out.created ? 'info' : 'success',
        title: out.created ? `Rendering version ${out.render.revisionNumber}` : 'Already rendered',
        body: out.note || (quality === 'review' ? 'Half resolution, fast. It shows up in Renders when it is done.' : 'Full quality from the originals.'),
        action: {
          label: 'Open',
          onClick: () => {
            onNavigate();
            navigate(`/renders/${out.render.id}`);
          },
        },
      });
    },
    onError: (e) => toast({ tone: 'error', title: 'Could not start the render', body: e.message }),
  });
  const label = quality === 'review' ? 'Render a review copy' : 'Make the final render';
  return (
    <Button
      size="sm"
      variant="secondary"
      icon={<FilmSlate size={13} />}
      loading={submit.isPending}
      onClick={async () => {
        const ok = await dialog.confirm({
          title: `${label}${number ? ` of version ${number}` : ''}?`,
          body:
            quality === 'review'
              ? 'A quick, half-size render on one of your machines running MetaRush. You can keep working meanwhile.'
              : 'A full-quality render from your original footage. It can take a while.',
          confirmLabel: label,
        });
        if (ok) submit.mutate();
      }}
    >
      {label}
    </Button>
  );
}
