import { useId, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { CheckCircle, Crown, FilmReel, GitBranch, Lightning, XCircle } from '@phosphor-icons/react';
import { useOpMutation } from '../api/queries';
import type { Brief, OpInput, RevisionSummary } from '../api/types';
import { absolute, duration as fmtDuration, stamp, viaLabel } from '../lib/format';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, cx, stateTone, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { SignoffsWaiting, useSignoffReminder } from './SignoffReminder';
import { askInProject } from '../layout/assistant/store';
import { VersionNote } from './sources/CutNames';

/** A version's state in words (D188): Draft, Approved, Needs changes. */
export function approvalWord(state: string | undefined): string {
  return state === 'approved' ? 'Approved' : state === 'rejected' ? 'Needs changes' : 'Draft';
}

export function ApprovalBadge({ rev }: { rev: Pick<RevisionSummary, 'approval'> }) {
  const s = rev.approval.state;
  return (
    <Badge tone={stateTone(s)} icon={s === 'approved' ? <CheckCircle size={11} weight="fill" /> : s === 'rejected' ? <XCircle size={11} weight="fill" /> : undefined}>
      {approvalWord(s)}
    </Badge>
  );
}

/**
 * Approve / reject: a human decision recorded with an optional note (styled
 * dialogs only). While the cut still waits for sign-offs, the same dialog
 * says so first and approving reads "Approve anyway" (a reminder, D166).
 */
export function ApprovalControls({ rev, size = 'md', className }: { rev: RevisionSummary; size?: 'sm' | 'md'; className?: string }) {
  const dialog = useDialog();
  const toast = useToast();
  const navigate = useNavigate();
  const signoffs = useSignoffReminder();
  const approve = useOpMutation('revision.approve', {
    invalidate: ['project.show', 'revision.list', 'revision.show', 'project.list', 'revision.compare', 'cut.signoffs.list', 'review.waiting', 'review.packet', 'draft.list'],
  });
  // An AI app's draft (D249): approving puts it in the cut; a note goes back to the app.
  const draftBy = rev.draftId ? rev.agentName ?? 'the AI app' : undefined;
  const decide = async (reject: boolean) => {
    const waiting = reject ? [] : await signoffs.waiting(rev.projectId, rev.branch);
    const what = draftBy
      ? `Version ${rev.number} is ${draftBy}'s draft: approving puts it in the cut as the latest version, approved by you (keeping newer changes where they don't overlap).`
      : `This marks version ${rev.number}${rev.title ? ` (“${rev.title}”)` : ''} as the cut you checked and like. Nothing in it changes, and the latest version stays the latest.`;
    const note = await dialog.prompt({
      title: reject ? `Reject version ${rev.number}?` : `Approve version ${rev.number}?`,
      body: reject ? (
        `This records your decision on version ${rev.number}. Say what should change, so ${draftBy ?? 'Claude Code'} can work on it.`
      ) : waiting.length ? (
        <div className="space-y-2">
          <p className="text-fg">
            <SignoffsWaiting waiting={waiting} question="Approve anyway?" />
          </p>
          <p>{what}</p>
        </div>
      ) : (
        what
      ),
      label: reject ? 'What should change?' : 'Note (optional)',
      placeholder: reject ? 'The hook lands too late; start on the question.' : 'Ready for final render.',
      multiline: true,
      required: reject,
      maxLength: 2000,
      confirmLabel: reject ? 'Reject this version' : waiting.length ? 'Approve anyway' : 'Approve this version',
      cancelLabel: !reject && waiting.length ? 'Not yet' : undefined,
      tone: reject ? 'danger' : 'primary',
    });
    if (note === null) return;
    approve.mutate(
      { id: rev.id, reject, ...(note ? { note } : {}) },
      {
        onSuccess: (r) => {
          // A rejection's note goes to the conversation that made the version (D186);
          // when none did, "Send to Claude" starts one in the project with it.
          const told = r.toldClaude;
          const sendable = reject && !!note?.trim() && !told;
          toast({
            tone: 'success',
            title: r.approval.state === 'approved' ? `Version ${r.number} approved` : `Version ${r.number} rejected`,
            body:
              [reject && told ? `Your note went to Claude in “${told.title || 'the conversation that made it'}”.` : note, r.reminder].filter(Boolean).join(' ') || undefined,
            action: sendable
              ? {
                  label: 'Send to Claude',
                  onClick: () => {
                    // The project's page, so the new conversation is about it.
                    if (!window.location.pathname.startsWith(`/p/${rev.projectId}`)) navigate(`/p/${rev.projectId}`);
                    askInProject(rev.projectId, `I turned down version ${r.number}: ${note!.trim()}${/[.!?…]$/.test(note!.trim()) ? '' : '.'} Make a new version with that change.`);
                  },
                }
              : undefined,
          });
        },
        onError: (e) => toast({ tone: 'error', title: 'Could not save the decision', body: [e.message, e.hint].filter(Boolean).join(' ') }),
      },
    );
  };
  const state = rev.approval.state;
  return (
    <div className={cx('flex flex-wrap items-center gap-2', className)}>
      <Button size={size} variant={state === 'approved' ? 'secondary' : 'primary'} icon={<CheckCircle size={15} weight={state === 'approved' ? 'fill' : 'regular'} />} loading={approve.isPending && !approve.variables?.reject} onClick={() => void decide(false)}>
        {state === 'approved' ? 'Approved' : 'Approve'}
      </Button>
      <Button size={size} variant="ghost" icon={<XCircle size={15} />} loading={approve.isPending && !!approve.variables?.reject} onClick={() => void decide(true)}>
        {state === 'rejected' ? 'Rejected' : 'Reject'}
      </Button>
    </div>
  );
}

/** How a render's state reads in a sentence. */
const RENDER_STATE_WORDS: Record<string, string> = { queued: 'waiting to start', rendering: 'rendering now', available: 'ready', failed: 'failed', canceled: 'canceled' };

type RenderPreset = NonNullable<OpInput<'render.submit'>['preset']>;
const PRESETS: RenderPreset[] = ['landscape-1080p', 'vertical-1080p', 'square-1080'];
const SHAPE_WORDS: Record<RenderPreset, string> = { 'landscape-1080p': 'Widescreen', 'vertical-1080p': 'Vertical', 'square-1080': 'Square' };
const isPreset = (p: string | undefined): p is RenderPreset => !!p && (PRESETS as string[]).includes(p);

/**
 * The shapes a render can take when the brief says where the video goes (D166):
 * the places' shapes first ("Vertical — for “social shorts”"), then the cut's
 * own shape and the others. None without places: the render keeps the cut's shape.
 */
export function renderShapes(brief: Brief | undefined, own: string | undefined): { value: RenderPreset | ''; label: string }[] {
  const places = (brief?.deliverables ?? []).filter((d) => isPreset(d.preset));
  if (!places.length) return [];
  const out: { value: RenderPreset | ''; label: string }[] = [];
  for (const p of PRESETS.filter((x) => places.some((d) => d.preset === x)).sort((a, b) => places.findIndex((d) => d.preset === a) - places.findIndex((d) => d.preset === b))) {
    const names = places.filter((d) => d.preset === p).map((d) => `“${d.name}”`);
    const joined = names.length > 1 ? `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}` : names[0];
    out.push({ value: p, label: `${SHAPE_WORDS[p]} — for ${joined}` });
  }
  if (!isPreset(own)) out.push({ value: '', label: "The cut's own shape" });
  for (const p of isPreset(own) ? [own, ...PRESETS] : PRESETS) {
    if (!out.some((o) => o.value === p)) out.push({ value: p, label: p === own ? `${SHAPE_WORDS[p]} — the cut's own shape` : SHAPE_WORDS[p] });
  }
  return out;
}

/**
 * Queue a review (fast, half resolution) or final (full quality) render. When
 * the brief says where the video goes, a shape choice comes first (the cut's own
 * shape stays the default).
 */
export function RenderButtons({
  revisionId,
  size = 'md',
  className,
  brief,
  output,
}: {
  revisionId: string | undefined;
  size?: 'sm' | 'md';
  className?: string;
  /** The project's brief: its places' shapes are offered first. */
  brief?: Brief;
  /** The version's own output (its shape is the default). */
  output?: RevisionSummary['output'];
}) {
  const toast = useToast();
  const id = useId();
  const submit = useOpMutation('render.submit', { invalidate: ['render.list', 'project.show', 'job.list', 'revision.show'] });
  const own = output?.preset;
  const shapes = renderShapes(brief, own);
  const [picked, setPicked] = useState<RenderPreset | '' | undefined>(undefined);
  const shape = picked ?? (isPreset(own) ? own : '');
  const go = (quality: 'review' | 'final') => {
    if (!revisionId) return;
    const preset = shapes.length && shape && shape !== own ? shape : undefined;
    submit.mutate(
      { revisionId, quality, ...(preset ? { preset } : {}) },
      {
        onSuccess: (out) => {
          if (!out.created) toast({ title: 'That render already exists', body: out.note ?? `The ${quality === 'final' ? 'final render' : 'review copy'} of version ${out.render.revisionNumber} is ${RENDER_STATE_WORDS[out.render.state] ?? out.render.state}.` });
          else if (out.onlineWorkers === 0) toast({ tone: 'error', title: `The ${quality === 'final' ? 'final render' : 'review copy'} is waiting: no machine running MetaRush can take it right now`, body: out.note });
          else
            toast({
              tone: 'success',
              title: `${quality === 'final' ? 'Final render' : 'Review copy'} on its way`,
              body: `Version ${out.render.revisionNumber}${preset ? `, ${SHAPE_WORDS[preset].toLowerCase()}` : ''}. You'll see progress here.`,
            });
        },
        onError: (e) => toast({ tone: 'error', title: 'Could not start the render', body: [e.message, e.hint].filter(Boolean).join(' ') }),
      },
    );
  };
  return (
    <div className={cx('flex flex-wrap items-center gap-2', className)}>
      {shapes.length ? (
        <>
          <label htmlFor={id} className="sr-only">
            The shape to render
          </label>
          <select
            id={id}
            value={shape}
            onChange={(e) => setPicked(e.target.value as RenderPreset | '')}
            disabled={!revisionId}
            title="The shape to render: where the video goes first"
            className={cx('max-w-[260px] min-w-0 truncate rounded-[9px] bg-panel px-2 text-fg hairline focus:outline-2 focus:outline-accent', size === 'sm' ? 'h-7 text-[12px]' : 'h-9 text-[13px]')}
            data-render-shape
          >
            {shapes.map((s) => (
              <option key={s.value || 'own'} value={s.value}>
                {s.label}
              </option>
            ))}
          </select>
        </>
      ) : null}
      <Button size={size} variant="secondary" disabled={!revisionId} icon={<Lightning size={15} />} loading={submit.isPending && submit.variables?.quality === 'review'} onClick={() => go('review')} title="Quick and half size, for checking the cut">
        Review copy
      </Button>
      <Button size={size} variant="secondary" disabled={!revisionId} icon={<FilmReel size={15} />} loading={submit.isPending && submit.variables?.quality === 'final'} onClick={() => go('final')} title="Your original footage at full quality">
        Final render
      </Button>
    </div>
  );
}

/** Lineage row: number, title, author and machine, time, approval, latest marker. */
export function RevisionRow({ rev, to, scriptTo, active, compareChecked, onCompareToggle, installationName }: {
  rev: RevisionSummary;
  to: string;
  /** Where this version's script opens (a "View script" link on the row). */
  scriptTo?: string;
  active?: boolean;
  compareChecked?: boolean;
  onCompareToggle?: (checked: boolean) => void;
  installationName?: (id: string) => string | undefined;
}) {
  const machine = installationName?.(rev.createdBy.installationId);
  return (
    <div
      data-active={active || undefined}
      className={cx('group relative flex items-start gap-3 rounded-[12px] px-3 py-3 transition-colors', active ? 'bg-panel hairline' : 'hover:bg-hover')}
    >
      {onCompareToggle ? (
        <input
          type="checkbox"
          checked={!!compareChecked}
          onChange={(e) => onCompareToggle(e.target.checked)}
          aria-label={`Select version ${rev.number} to compare`}
          className="mt-1 h-4 w-4 shrink-0 cursor-pointer accent-[var(--accent)]"
        />
      ) : null}
      <Link to={to} className="min-w-0 flex-1 outline-none after:absolute after:inset-0 after:rounded-[12px] focus-visible:after:outline-2 focus-visible:after:outline-accent">
        <div className="flex items-center gap-2">
          <span className="tc text-[13px] font-semibold text-fg">r{rev.number}</span>
          {rev.isHead ? (
            <Badge tone="accent" icon={<Crown size={10} weight="fill" />}>
              latest
            </Badge>
          ) : null}
          {rev.draftId && !rev.isHead && rev.approval.state === 'draft' ? (
            <Badge tone="info" title="Not in the cut until someone approves it">
              {rev.agentName ?? 'AI app'}'s draft
            </Badge>
          ) : null}
          {rev.rebasedFrom ? (
            <span title="Someone saved a newer version first; this change didn't overlap it, so both were kept" className="text-subtle">
              <GitBranch size={12} />
            </span>
          ) : null}
          <span className="ml-auto">
            <ApprovalBadge rev={rev} />
          </span>
        </div>
        <div className="mt-1 truncate text-[13px] text-fg">{rev.note ? <Theirs>{rev.note}</Theirs> : rev.title || rev.instruction || 'Untitled version'}</div>
        {rev.instruction && (rev.title || rev.note) && rev.instruction !== rev.note ? (
          <div className="clamp-2 mt-0.5 text-[12px] leading-relaxed text-muted">{rev.origin?.startsWith('timeline:') ? rev.instruction : `“${rev.instruction}”`}</div>
        ) : null}
        <div className="mt-1.5 truncate text-[11.5px] text-subtle" title={absolute(rev.createdAt)}>
          <span className="text-muted" data-stamp>
            {stamp(rev.createdAt)}
          </span>
          , {rev.createdBy.userName ?? 'Someone'} via {viaLabel(rev.createdBy.via, rev.origin, rev.createdBy.agent ? rev.agentName : undefined)}
          {machine ? ` on ${machine}` : ''} · {fmtDuration(rev.duration)}
        </div>
      </Link>
      <VersionNote rev={rev} />
      {scriptTo ? (
        <Link
          to={scriptTo}
          className="relative z-10 shrink-0 self-center rounded-[8px] px-2 py-1 text-[12px] font-medium text-accent-text hover:bg-accent-soft hover:underline"
          aria-label={`View the script of version ${rev.number}`}
          data-view-script
        >
          View script
        </Link>
      ) : null}
    </div>
  );
}
