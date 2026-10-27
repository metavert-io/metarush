import { useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router';
import { CaretRight, CheckCircle, DownloadSimple, FileText, Robot, XCircle } from '@phosphor-icons/react';
import { call, media } from '../api/client';
import { useOp, useOpMutation } from '../api/queries';
import type { Packet } from '../api/types';
import { absolute, stamp } from '../lib/format';
import { resetSelection } from '../lib/workbench';
import { RecoveringVideo } from '../player/Trouble';
import { CostEstimate } from '../components/CostEstimate';
import { useDialog } from '../ui/Dialog';
import { useToast } from '../ui/Toast';
import { Badge, Button, CopyButton, Details, ErrorPanel, Freshness, Skeleton, Theirs, cx } from '../ui/primitives';

/**
 * The review page of an AI app's draft, or of any version (D249, D250): what
 * changes and why, the review copy, what the checks found, what it costs,
 * who made it and what is still waiting — the one-page packet a person can
 * copy or download as Markdown to hand to a client or a colleague. A draft
 * that waits has the people-only decisions: Approve, Not quite… (an inline
 * note that goes back to the app) and Turn down.
 */
export function ReviewDraftPage() {
  const { id = '' } = useParams();
  const pk = useOp('review.packet', { id });
  useEffect(() => resetSelection({}), []);
  if (pk.isLoading) return <PageSkeleton />;
  if (pk.error || !pk.data) return <ErrorPanel error={pk.error} onRetry={() => void pk.refetch()} />;
  const p = pk.data;
  return (
    <article data-review-page data-draft-state={p.draft?.state ?? p.state} className="mx-auto max-w-[1100px]">
      <nav aria-label="Breadcrumb" className="mb-4 flex items-center gap-1.5 text-[12.5px] text-subtle">
        <Link to="/" className="hover:text-fg">
          Studio
        </Link>
        <CaretRight size={11} />
        <span>Awaiting your review</span>
        {p.projectId ? (
          <>
            <CaretRight size={11} />
            <Link to={`/p/${p.projectId}`} className="truncate hover:text-fg">
              <Theirs>{p.projectName}</Theirs>
            </Link>
          </>
        ) : null}
        <Freshness className="ml-auto" at={pk.dataUpdatedAt} fetching={pk.isFetching} onRefresh={() => void pk.refetch()} />
      </nav>
      <header className="mb-6">
        <div className="mb-2 flex flex-wrap items-center gap-2">
          {p.who.agentName ? (
            <Badge tone="info" icon={<Robot size={11} />}>
              {p.draft ? `${p.who.agentName}'s draft` : `Made by ${p.who.agentName}`}
            </Badge>
          ) : null}
          <Badge tone={statusTone(p)}>{p.status}</Badge>
          {p.inCut ? <Badge tone="accent">in the cut</Badge> : null}
        </div>
        <h1 className="text-[24px] font-semibold leading-tight tracking-[-0.02em]">{p.title}</h1>
        <p className="mt-1.5 text-[13px] text-muted" title={absolute(p.who.at)}>
          Made by {p.who.made}, <span data-stamp>{stamp(p.who.at)}</span>
          {p.cutTitle ? (
            <>
              {' '}
              · cut <Theirs>{p.cutTitle}</Theirs>
            </>
          ) : null}
        </p>
      </header>
      <div className="grid grid-cols-1 gap-8 lg:grid-cols-[minmax(0,1fr)_340px]">
        <div className="min-w-0 space-y-6">
          {p.kind === 'version' ? <ReviewCopy packet={p} /> : null}
          <Section title="What changes">
            <ul className="space-y-1" data-changes>
              {(p.changes.length ? p.changes : ['Nothing.']).map((c, i) => (
                <li key={i} className="flex gap-2 text-[13.5px] leading-relaxed text-fg">
                  <span className="mt-[9px] h-1 w-1 shrink-0 rounded-full bg-accent" aria-hidden />
                  {c}
                </li>
              ))}
            </ul>
          </Section>
          {p.why.length ? (
            <Section title="Why">
              {p.why.map((w, i) => (
                <blockquote key={i} className="border-l-2 border-[var(--line)] pl-3 text-[13.5px] leading-relaxed text-fg">
                  <Theirs>{w}</Theirs>
                </blockquote>
              ))}
            </Section>
          ) : null}
          <Section title="Checks">
            <p className="text-[13.5px] text-fg">{p.checks.said}</p>
            <Lines items={p.checks.problems} tone="danger" />
            <Lines items={[...p.checks.warnings, ...p.checks.worthChecking]} />
          </Section>
          <Section title="Cost">
            <p className="text-[13.5px] text-fg" data-receipt>
              {p.receipt}
            </p>
            {p.draft?.cost ? <CostEstimate estimate={p.draft.cost} label="MetaRush's estimate" className="mt-2" /> : null}
          </Section>
        </div>
        <aside className="space-y-6">
          {p.draft ? <Decide packet={p} onDone={() => void pk.refetch()} /> : null}
          <Section title="What's waiting">
            {p.waiting.length ? <Lines items={p.waiting} plain /> : <p className="text-[13px] text-muted">Nothing.</p>}
          </Section>
          {p.draft?.decision ? (
            <Section title="Decision">
              <DraftDecisionLine d={p.draft.decision} />
            </Section>
          ) : p.decision ? (
            <Section title="Decision">
              <p className="text-[13px] text-fg" data-decision>
                {p.decision}
              </p>
            </Section>
          ) : null}
          <ShareLinks packet={p} />
        </aside>
      </div>
    </article>
  );
}

function statusTone(p: Packet): 'ok' | 'danger' | 'accent' | 'neutral' | 'info' {
  switch (p.draft?.state ?? p.state) {
    case 'approved':
    case 'in_cut':
      return 'ok';
    case 'turned_down':
    case 'rejected':
      return 'danger';
    case 'waiting':
    case 'approving':
    case 'draft':
      return 'accent';
    default:
      return 'neutral';
  }
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section aria-label={title}>
      <h2 className="mb-2 text-[14px] font-semibold">{title}</h2>
      {children}
    </section>
  );
}

function Lines({ items, tone, plain }: { items: string[]; tone?: 'danger'; plain?: boolean }) {
  if (!items.length) return null;
  return (
    <ul className={cx('space-y-1', !plain && 'mt-2')}>
      {items.map((l, i) => (
        <li key={i} className={cx('flex gap-2 text-[13px] leading-relaxed', tone === 'danger' ? 'text-danger' : plain ? 'text-fg' : 'text-muted')}>
          <span className={cx('mt-[8px] h-1 w-1 shrink-0 rounded-full', tone === 'danger' ? 'bg-danger' : 'bg-current opacity-40')} aria-hidden />
          {l}
        </li>
      ))}
    </ul>
  );
}

/** The version's review copy (made by itself about a minute after the change, D187), or where it stands. */
function ReviewCopy({ packet: p }: { packet: Packet }) {
  const rc = p.reviewCopy;
  const vertical = (p.revision?.output.height ?? 0) > (p.revision?.output.width ?? 1);
  return (
    <section aria-label="Review copy" data-review-copy={rc?.state ?? 'none'}>
      {rc?.state === 'available' ? (
        <RecoveringVideo src={media.render(rc.renderId)} controls playsInline className={cx('mx-auto rounded-[12px] bg-black', vertical ? 'max-h-[60dvh]' : 'w-full')} />
      ) : (
        <div className="flex aspect-video items-center justify-center rounded-[12px] bg-raised px-6 text-center text-[13px] text-muted hairline">
          {rc ? `Review copy: ${rc.said.charAt(0).toLowerCase()}${rc.said.slice(1)}.` : 'MetaRush makes a review copy of it by itself about a minute after it was saved.'}
        </div>
      )}
      <div className="mt-2 flex flex-wrap items-center gap-3 text-[12.5px] text-muted">
        {rc?.state === 'available' ? <span>{rc.said}.</span> : null}
        {p.revision && p.baseRevisionId ? (
          <Link to={`/p/${p.revision.projectId}/compare?left=${p.baseRevisionId}&right=${p.revision.id}`} className="ml-auto font-medium text-accent-text hover:underline" data-compare>
            Compare side by side
          </Link>
        ) : null}
        {p.revision ? (
          <Link to={`/p/${p.revision.projectId}/r/${p.revision.id}`} className={cx('font-medium text-accent-text hover:underline', !p.baseRevisionId && 'ml-auto')}>
            Open version {p.revision.number}
          </Link>
        ) : null}
      </div>
    </section>
  );
}

/** Approve, Not quite… (with what should change) or Turn down: people only. */
function Decide({ packet: p, onDone }: { packet: Packet; onDone: () => void }) {
  const d = p.draft!;
  const dialog = useDialog();
  const toast = useToast();
  const [notQuite, setNotQuite] = useState(false);
  const [note, setNote] = useState('');
  const decide = useOpMutation('draft.decide', {
    invalidate: ['review.waiting', 'review.packet', 'draft.list', 'project.show', 'revision.list', 'revision.show', 'content.rules.list', 'brand.show'],
    onSuccess: () => onDone(),
  });
  if (d.state === 'approving')
    return (
      <section aria-label="Your decision" className="panel p-4 text-[13px] text-muted" data-decide>
        Someone is approving this draft right now.
      </section>
    );
  if (d.state !== 'waiting') return null;
  const run = (decision: 'approve' | 'notQuite' | 'turnDown', n?: string) =>
    decide.mutate(
      { id: d.id, decision, ...(n ? { note: n } : {}) },
      {
        onSuccess: (v) =>
          toast({
            tone: 'success',
            title: decision === 'approve' ? 'Approved' : decision === 'notQuite' ? `${v.agentName} hears what should change` : 'Turned down',
            body: decision === 'approve' ? v.decision?.result : decision === 'notQuite' ? 'Its next draft takes this one’s place.' : undefined,
          }),
        onError: (e) => toast({ tone: 'error', title: 'This draft could not be approved', body: [e.message, e.hint].filter(Boolean).join(' ') }),
      },
    );
  const approve = async () => {
    const ok = await dialog.confirm({
      title: d.kind === 'version' ? `Put ${d.agentName}'s draft in the cut?` : `Make ${d.agentName}'s change?`,
      body:
        d.kind === 'version'
          ? `Version ${d.revision?.number ?? ''} becomes the latest version of the cut, approved by you.${d.headMoved ? ' Someone saved a newer version since: both changes are kept where they don’t overlap.' : ''}`
          : `${d.title}. MetaRush makes it as you.`,
      confirmLabel: d.kind === 'version' ? 'Approve and use it' : 'Approve and make it',
    });
    if (ok) run('approve');
  };
  const turnDown = async () => {
    const n = await dialog.prompt({
      title: `Turn down ${d.agentName}'s draft?`,
      body: 'Nothing changes. The app can read why.',
      label: 'Why (optional)',
      multiline: true,
      maxLength: 2000,
      confirmLabel: 'Turn it down',
      tone: 'danger',
    });
    if (n !== null) run('turnDown', n.trim() || undefined);
  };
  return (
    <section aria-label="Your decision" className="panel space-y-3 p-4" data-decide>
      <h2 className="text-[14px] font-semibold">Your decision</h2>
      {d.tried ? <p className="rounded-[10px] bg-danger-soft px-3 py-2 text-[12.5px] text-danger">Approving didn’t work: {d.tried.message}</p> : null}
      <div className="flex flex-wrap gap-2">
        <Button variant="primary" icon={<CheckCircle size={15} />} loading={decide.isPending && decide.variables?.decision === 'approve'} onClick={() => void approve()} data-approve>
          Approve
        </Button>
        <Button variant="secondary" onClick={() => setNotQuite((v) => !v)} aria-expanded={notQuite} data-not-quite>
          Not quite…
        </Button>
        <Button variant="ghost" icon={<XCircle size={15} />} loading={decide.isPending && decide.variables?.decision === 'turnDown'} onClick={() => void turnDown()} data-turn-down>
          Turn down
        </Button>
      </div>
      {notQuite ? (
        <form
          className="space-y-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (note.trim()) run('notQuite', note.trim());
          }}
        >
          <label className="block text-[12.5px] font-medium text-muted" htmlFor="not-quite-note">
            What should change?
          </label>
          <textarea
            id="not-quite-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            maxLength={2000}
            rows={3}
            placeholder="Keep the first two seconds; cut the second sentence instead."
            className="w-full rounded-[10px] bg-panel px-3 py-2 text-[13px] text-fg hairline"
            data-not-quite-note
          />
          <Button type="submit" size="sm" variant="primary" disabled={!note.trim()} loading={decide.isPending && decide.variables?.decision === 'notQuite'} data-send-not-quite>
            Send to {d.agentName}
          </Button>
        </form>
      ) : null}
      <p className="text-[11.5px] text-subtle">Only people decide. {d.agentName} reads your decision the next time it looks at its drafts.</p>
    </section>
  );
}

/** A person's decision about a draft, with when (stamp(), the exact date on hover). */
function DraftDecisionLine({ d }: { d: NonNullable<NonNullable<Packet['draft']>['decision']> }) {
  const who = d.by.userName || 'Someone';
  const what = d.state === 'approved' ? 'approved it' : d.state === 'not_quite' ? 'said Not quite' : d.state === 'in_cut' ? 'put its change in the cut' : 'turned it down';
  return (
    <div className="space-y-1 text-[13px] text-fg" data-decision>
      <p title={absolute(d.at)}>
        {who} {what}, <span data-stamp>{stamp(d.at)}</span>.
      </p>
      {d.note ? (
        <p className="text-muted">
          <Theirs>{d.note}</Theirs>
        </p>
      ) : null}
      {d.result ? <p className="text-muted">{d.result}</p> : null}
    </div>
  );
}

/** Copy or download the page as Markdown, to hand it to a client or a colleague. */
function ShareLinks({ packet: p }: { packet: Packet }) {
  const [markdown, setMarkdown] = useState<string>();
  useEffect(() => {
    let live = true;
    void call('review.packet', { id: p.id, format: 'markdown' }).then((out) => live && setMarkdown(out.markdown));
    return () => {
      live = false;
    };
  }, [p.id, p.status, p.decision]);
  const href = useMemo(() => (markdown ? URL.createObjectURL(new Blob([markdown], { type: 'text/markdown' })) : undefined), [markdown]);
  useEffect(
    () => () => {
      if (href) URL.revokeObjectURL(href);
    },
    [href],
  );
  return (
    <Section title="Share this page">
      <div className="flex flex-wrap items-center gap-2">
        <CopyButton text={markdown ?? ''} label="Copy as Markdown" />
        {href ? (
          <a href={href} download={`review-${p.id}.md`} className="inline-flex items-center gap-1 text-[12.5px] font-medium text-accent-text hover:underline" data-download-page>
            <DownloadSimple size={14} /> Download
          </a>
        ) : null}
      </div>
      <Details className="mt-3" compact>
        <div className="space-y-1 text-[12px] text-muted">
          <div className="flex items-center gap-1.5">
            <FileText size={12} /> {p.draft ? `draft ${p.draft.id}, ${p.draft.op}` : `version ${p.id}`}
          </div>
          {p.draft?.input ? <pre className="tc max-h-48 overflow-auto whitespace-pre-wrap rounded-[8px] bg-raised p-2 text-[11px]">{p.draft.input}</pre> : null}
          {p.sheetDiff ? (
            <>
              <div className="pt-1">The cut sheet's lines that differ:</div>
              <pre className="tc max-h-64 overflow-auto whitespace-pre rounded-[8px] bg-raised p-2 text-[11px]" data-sheet-diff>
                {p.sheetDiff}
              </pre>
            </>
          ) : null}
        </div>
      </Details>
    </Section>
  );
}

function PageSkeleton() {
  return (
    <div className="mx-auto max-w-[1100px] space-y-4">
      <Skeleton className="h-6 w-64" />
      <Skeleton className="h-9 w-[520px] max-w-full" />
      <div className="grid grid-cols-1 gap-8 lg:grid-cols-[minmax(0,1fr)_340px]">
        <Skeleton className="aspect-video rounded-[12px]" />
        <Skeleton className="h-48 rounded-[12px]" />
      </div>
    </div>
  );
}
