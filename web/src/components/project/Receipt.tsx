import { Link } from 'react-router';
import { DownloadSimple, Receipt as ReceiptIcon, ShieldCheck } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import type { ReceiptLeft } from '../../api/types';
import { stamp, absolute } from '../../lib/format';
import { Badge, Button, EmptyState, ErrorPanel, Skeleton, Theirs, cx } from '../../ui/primitives';
import { CostEstimate } from '../CostEstimate';

/*
 * The receipt (D240): every request a project sent to an outside AI service —
 * the service and model, what kind of work, what was sent, what it cost,
 * whether MetaRush deleted it there and why it ran — Claude Code's
 * conversations and whose account answered them, the activity log's count of
 * requests, generated media, what Auto spent by itself, and the cost of the
 * video at cost (per version and project). A client can be handed it as is.
 */

function money(usd: number, basis?: string): string {
  if (basis === 'none' || usd <= 0) return 'nothing';
  if (usd < 0.01) return 'less than a cent';
  return (basis === 'estimate' ? 'about ' : '') + `$${usd.toFixed(2)}`;
}

const deletedWords: Record<string, string> = { deleted: 'Deleted there', kept: 'Kept there', failed: 'Not deleted', unknown: 'Not said' };

// generatedLabel names generated media: a shot, AI narration, an AI picture, or the AI sound it is (D226).
function generatedLabel(g: { kind: string; what?: string }): string {
  if (g.kind === 'shot') return 'Generated shot';
  if (g.kind === 'voice') return 'AI narration';
  if (g.kind === 'picture') return 'AI picture';
  const what = (g.what ?? 'AI sound').replace(/^(a|an) /, '');
  return what.charAt(0).toUpperCase() + what.slice(1) + ' (AI)';
}

export function ReceiptTab({ projectId, revisionId }: { projectId: string; revisionId?: string }) {
  const r = useOp('project.receipt', { projectId, ...(revisionId ? { revision: revisionId } : {}) });
  if (r.isLoading) return <Skeleton className="h-64" />;
  if (r.error || !r.data) return <ErrorPanel error={r.error} onRetry={() => void r.refetch()} />;
  const d = r.data;
  const download = () => {
    const url = URL.createObjectURL(new Blob([JSON.stringify(d, null, 2)], { type: 'application/json' }));
    const a = document.createElement('a');
    a.href = url;
    a.download = `${d.projectName.replace(/[^\p{L}\p{N}]+/gu, '-').replace(/^-|-$/g, '').toLowerCase() || 'project'}-receipt.json`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 2000);
  };
  return (
    <div className="space-y-8" data-receipt>
      <section aria-label="In short">
        <div className="mb-2 flex flex-wrap items-center gap-2">
          <h2 className="flex items-center gap-2 text-[15px] font-semibold">
            <ReceiptIcon size={17} className="text-subtle" aria-hidden />
            Receipt
          </h2>
          <span className="text-[12px] text-subtle" title={absolute(d.at)}>
            as of {stamp(d.at)}
          </span>
          <Button size="sm" variant="ghost" className="ml-auto" icon={<DownloadSimple size={13} />} onClick={download} data-receipt-download>
            Download it
          </Button>
        </div>
        <div className="space-y-1.5 text-[13.5px] leading-relaxed" data-receipt-lines>
          {d.lines.map((l) => (
            <p key={l}>{l}</p>
          ))}
        </div>
      </section>

      <section aria-label="Cost at cost" className="grid grid-cols-1 gap-4 md:grid-cols-2">
        <div className="rounded-[12px] px-4 py-3 hairline">
          <div className="text-[11.5px] font-medium uppercase tracking-wide text-subtle">The project, at cost</div>
          <div className="mt-0.5 text-[22px] font-semibold tabular-nums" data-receipt-project-cost>
            {d.cost.project}
          </div>
          {d.cost.byService.length ? (
            <ul className="mt-2 space-y-0.5 text-[12.5px]">
              {d.cost.byService.map((s) => (
                <li key={s.service} className="flex gap-2">
                  <span className="min-w-0 flex-1">{s.service}</span>
                  <span className="text-muted">{s.requests === 1 ? '1 request' : `${s.requests} requests`}</span>
                  <span className="w-20 text-right tabular-nums">{money(s.usd)}</span>
                </li>
              ))}
            </ul>
          ) : null}
          <p className="mt-2 text-[12px] text-subtle">{d.cost.claude}</p>
        </div>
        {d.cost.version ? (
          <div className="rounded-[12px] px-4 py-3 hairline" data-receipt-version-cost>
            <div className="text-[11.5px] font-medium uppercase tracking-wide text-subtle">Version {d.cost.number}, at cost</div>
            <div className="mt-0.5 text-[22px] font-semibold tabular-nums">{money(d.cost.version.usd)}</div>
            <p className="text-[12px] text-subtle">What its cut uses: generated clips, AI voice lines and clearer voices from the cloud.</p>
            <CostEstimate estimate={d.cost.version} bare className="mt-1" />
          </div>
        ) : null}
        <p className="text-[12px] text-subtle md:col-span-2">{d.cost.note}</p>
      </section>

      {d.left ? <LeftSection left={d.left} /> : null}

      <section aria-label="Requests to outside AI services">
        <h3 className="mb-2 text-[14px] font-semibold">Requests to outside AI services</h3>
        {d.requests.length === 0 ? (
          <EmptyState compact icon={<ReceiptIcon size={20} />} title="None" body="Nothing from this project was sent to an outside AI service." />
        ) : (
          <ul className="divide-y divide-line rounded-[12px] hairline">
            {d.requests.map((rq) => (
              <li key={rq.record} className="px-3 py-2.5 text-[13px]" data-receipt-request={rq.kind}>
                <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
                  <span className="font-medium">{rq.what}</span>
                  <span className="text-muted">
                    {rq.service}
                    {rq.model ? `, ${rq.model}` : ''}
                  </span>
                  {rq.outcome !== 'done' ? <Badge tone={rq.outcome === 'running' ? 'info' : 'danger'}>{rq.outcome === 'running' ? 'Running' : rq.outcome === 'refused' ? 'Turned down' : rq.outcome === 'canceled' ? 'Stopped' : 'Failed'}</Badge> : null}
                  <span className="ml-auto tabular-nums">{money(rq.usd, rq.basis)}</span>
                </div>
                <div className="mt-0.5 text-[12px] text-muted">
                  <span title={absolute(rq.at)}>{stamp(rq.at)}</span> · {rq.why}
                </div>
                {rq.sent.length ? <div className="mt-0.5 text-[12px] text-muted">Sent: {rq.sent.join('; ')}</div> : null}
                {rq.prompt ? (
                  <div className="mt-0.5 text-[12px] text-muted">
                    Words sent: <Theirs>“{rq.prompt.length > 160 ? rq.prompt.slice(0, 160) + '…' : rq.prompt}”</Theirs>
                  </div>
                ) : null}
                <div className={cx('mt-0.5 text-[12px]', rq.deleted === 'deleted' ? 'text-ok' : 'text-subtle')} title={rq.deletedSays}>
                  {deletedWords[rq.deleted] ?? rq.deleted}: {rq.deletedSays}
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>

      {d.claude.length ? (
        <section aria-label="Claude Code conversations">
          <h3 className="mb-2 text-[14px] font-semibold">Claude Code conversations</h3>
          <ul className="divide-y divide-line rounded-[12px] hairline">
            {d.claude.map((c) => (
              <li key={c.conversationId} className="px-3 py-2.5 text-[13px]" data-receipt-claude>
                <div className="flex flex-wrap items-baseline gap-x-2">
                  <Theirs className="font-medium">{c.title}</Theirs>
                  <span className="text-muted">
                    {c.messages === 1 ? '1 message' : `${c.messages} messages`}
                    {c.startedBy ? `, started by ${c.startedBy}` : ''}
                  </span>
                  <span className="ml-auto tabular-nums">{c.billed ? money(c.usd) : 'on the plan'}</span>
                </div>
                <div className="mt-0.5 text-[12px] text-muted">{c.who ? `${c.who} · ${c.terms}` : `Claude Code on ${c.machine}`}</div>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {d.generated.length ? (
        <section aria-label="Generated media">
          <h3 className="mb-2 text-[14px] font-semibold">Generated media in the project</h3>
          <ul className="divide-y divide-line rounded-[12px] hairline">
            {d.generated.map((g, i) => (
              <li key={(g.assetId ?? g.resourceId ?? '') + i} className="flex flex-wrap items-baseline gap-x-2 px-3 py-2 text-[13px]">
                <span>{generatedLabel(g)}</span>
                <Theirs className="font-medium">{g.title}</Theirs>
                <span className="text-muted">
                  {g.service}
                  {g.model ? `, ${g.model}` : ''}
                  {g.seconds > 0 ? ` · ${g.seconds} s` : ''}
                </span>
                {g.inVersion ? <Badge tone="accent">In version {d.cost.number}</Badge> : null}
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {d.auto.length ? (
        <section aria-label="What Auto spent">
          <h3 className="mb-2 text-[14px] font-semibold">What Auto spent by itself</h3>
          <ul className="divide-y divide-line rounded-[12px] hairline">
            {d.auto.map((x, i) => (
              <li key={i} className="flex flex-wrap items-baseline gap-x-2 px-3 py-2 text-[13px]">
                <span className="font-medium">{x.what}</span>
                <span className="text-muted">for {x.for}</span>
                <span className="text-muted" title={absolute(x.at)}>
                  {stamp(x.at)}
                </span>
                <span className="ml-auto tabular-nums">{x.pending ? `up to ${money(x.upToUsd ?? 0)}` : money(x.usd)}</span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {(d.sentAnyway ?? []).length ? (
        <section aria-label="Sent out anyway" data-receipt-sent-anyway>
          <h3 className="mb-2 text-[14px] font-semibold">Sent out anyway</h3>
          <ul className="divide-y divide-line rounded-[12px] hairline">
            {(d.sentAnyway ?? []).map((x, i) => (
              <li key={i} className="px-3 py-2 text-[13px]">
                <div className="flex flex-wrap items-baseline gap-x-2">
                  <span className="font-medium">Version {x.number}</span>
                  <span className="text-muted">{x.wentOut ? 'went out anyway' : 'allowed to go out anyway, not sent yet'}</span>
                  <span className="text-muted">by {x.by}</span>
                  <span className="text-muted" title={absolute(x.at)}>
                    {stamp(x.at)}
                  </span>
                </div>
                <p className="mt-0.5">
                  <Theirs>“{x.note}”</Theirs>
                </p>
                <ul className="mt-1 list-disc pl-5 text-[12.5px] text-muted">
                  {x.held.map((h) => (
                    <li key={h}>{h}</li>
                  ))}
                </ul>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {d.logged.length ? (
        <section aria-label="The activity log's count">
          <h3 className="mb-1 text-[14px] font-semibold">Requests the activity log recorded</h3>
          <p className="mb-2 text-[12px] text-subtle">
            Every request to an outside service is in the{' '}
            <Link to="/settings/log" className="text-accent-text hover:underline">
              activity log
            </Link>
            ; here they are counted for this project.
          </p>
          <ul className="space-y-0.5 text-[13px]">
            {d.logged.map((l) => (
              <li key={l.service + l.what} className="flex gap-2">
                <span className="min-w-0 flex-1">
                  {l.service}: {l.what}
                </span>
                <span className="text-muted">
                  {l.count}
                  {l.failed ? `, ${l.failed} failed` : ''}
                </span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </div>
  );
}

/** A version's cost at cost, on its page (D240): what its cut uses of outside AI services. */
export function VersionCost({ projectId, revisionId, number }: { projectId: string; revisionId: string; number: number }) {
  const r = useOp('project.receipt', { projectId, revision: revisionId }, { staleTime: 30_000 });
  const v = r.data?.cost.version;
  if (!v) return null;
  return (
    <div className="mt-4 flex flex-wrap items-baseline gap-x-2 text-[12.5px] text-muted" data-version-cost>
      {v.parts.length ? (
        <CostEstimate estimate={v} label={`Version ${number}, at cost`} />
      ) : (
        <span>Version {number} uses no outside AI services: it costs nothing at cost.</span>
      )}
      <Link to={`/p/${projectId}?tab=receipt`} className="text-accent-text hover:underline">
        The project's receipt
      </Link>
    </div>
  );
}

const kindWords: Record<string, string> = { footage: 'footage', voices: 'voices', faces: 'faces', words: 'words', pictures: 'pictures' };

function andList(xs: string[]): string {
  if (xs.length <= 1) return xs.join('');
  return `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`;
}

/** What of the project left this computer, and where (D291): proof for a client. */
function LeftSection({ left }: { left: ReceiptLeft }) {
  return (
    <section aria-label="What left this computer" data-receipt-left={left.nothing ? 'nothing' : 'some'}>
      <h3 className="mb-2 flex items-center gap-2 text-[14px] font-semibold">
        <ShieldCheck size={15} className="text-subtle" aria-hidden />
        What left this computer
      </h3>
      <p className="text-[13px]" data-receipt-left-summary>
        {left.summary}
      </p>
      {left.places.length ? (
        <ul className="mt-2 divide-y divide-line rounded-[12px] hairline">
          {left.places.map((p) => (
            <li key={`${p.place}:${p.to}`} className="px-3 py-2 text-[13px]" data-receipt-left-place={p.place}>
              <div className="flex flex-wrap items-baseline gap-x-2">
                <span className="font-medium">{p.to}</span>
                <span className="text-muted">{p.kinds.length ? andList(p.kinds.map((k) => kindWords[k] ?? k)) : 'its conversations'}</span>
                <span className="ml-auto text-[12px] text-muted">
                  {p.count === 1 ? 'once' : `${p.count} times`} ·{' '}
                  <span title={absolute(p.last)}>{stamp(p.last)}</span>
                </span>
              </div>
              {p.what.length ? <div className="mt-0.5 text-[12px] text-subtle">{p.what.join('; ')}</div> : null}
            </li>
          ))}
        </ul>
      ) : null}
      {left.stored.map((s) => (
        <p key={s} className="mt-2 text-[12.5px] text-muted">
          {s}
        </p>
      ))}
      {left.refused.length ? (
        <div className="mt-3">
          <div className="mb-1 text-[12.5px] font-medium">Kept here by What may leave</div>
          <ul className="space-y-1 text-[12.5px] text-muted" data-receipt-left-refused>
            {left.refused.map((r, i) => (
              <li key={`${r.at}-${i}`}>
                <span title={absolute(r.at)}>{stamp(r.at)}</span>: {r.message}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <p className="mt-2 text-[12px] text-subtle">{left.rules}</p>
    </section>
  );
}
