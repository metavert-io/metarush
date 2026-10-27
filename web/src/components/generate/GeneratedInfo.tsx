import { Link } from 'react-router';
import { ArrowsClockwise, Copy, FastForward, MagicWand, WarningCircle } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import type { AssetSummary } from '../../api/types';
import { absolute } from '../../lib/format';
import { Button, cx, Details } from '../../ui/primitives';
import { AIBadge, isGenerated, money } from './ai';
import { useGenerateRuns } from './RunDialog';

const resLabel = (r: string) => (r === '4k' ? '4K' : r);

/**
 * How a generated clip was made, in its header (D125): what made it, what it cost,
 * the prompt and everything else behind Details, and the four ways to make
 * new takes (none of them replaces this clip).
 */
export function GeneratedInfo({ asset, className }: { asset: Pick<AssetSummary, 'id' | 'source' | 'title'>; className?: string }) {
  const gen = isGenerated(asset.source);
  const show = useOp('generate.show', gen ? { id: asset.id } : null, { staleTime: 30_000 });
  const run = useGenerateRuns();
  if (!gen) return null;
  const g = show.data;
  const who = asset.source?.generated?.providerTitle ?? 'Google Gemini';
  return (
    <div className={cx('rounded-[12px] bg-info-soft/40 px-3 py-2.5 text-[12.5px]', className)} data-generated-info>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <AIBadge source={asset.source} />
        <span className="text-muted">
          Made with {who}
          {g ? ` · ${g.params.seconds} seconds, ${resLabel(g.params.resolution)}` : ''}
          {g && g.take > 1 ? ` · take ${g.take}` : ''}
          {g ? ` · ${money(g.costUsd)}` : ''}
        </span>
        {g?.consentRevoked ? (
          <span className="inline-flex items-center gap-1 text-danger">
            <WarningCircle size={13} weight="fill" /> A consent it relied on was withdrawn since
          </span>
        ) : null}
        <span className="ml-auto flex flex-wrap gap-1">
          <Button size="sm" variant="ghost" icon={<Copy size={13} />} onClick={() => void run('again', asset.id)}>
            Generate again
          </Button>
          <Button size="sm" variant="ghost" icon={<ArrowsClockwise size={13} />} onClick={() => void run('variations', asset.id)}>
            Variations
          </Button>
          <Button size="sm" variant="ghost" icon={<FastForward size={13} />} onClick={() => void run('extend', asset.id)}>
            Extend
          </Button>
          <Button size="sm" variant="ghost" icon={<MagicWand size={13} />} onClick={() => void run('edit', asset.id)}>
            Edit
          </Button>
        </span>
      </div>
      {g ? (
        <>
          <p className="mt-1.5 max-w-[90ch] text-fg" data-generated-prompt>
            “{g.prompt}”
          </p>
          {g.look ? <p className="mt-0.5 text-muted">With the look “{g.look.title}”</p> : null}
          {g.usedIn?.length ? <p className="mt-0.5 text-muted">{g.usedIn.map((u) => u.text).join(' · ')}</p> : null}
          <Details compact label="How it was made">
            <dl className="grid grid-cols-1 gap-x-3 sm:grid-cols-[150px_minmax(0,1fr)] sm:gap-y-1">
              <dt>Model</dt>
              <dd className="text-muted">
                {g.modelTitle} ({g.model}, {g.stage}), prices of {g.priceTable}
              </dd>
              {g.sentPrompt ? (
                <>
                  <dt>Prompt as sent</dt>
                  <dd className="whitespace-pre-wrap text-muted">{g.sentPrompt}</dd>
                </>
              ) : null}
              {g.template ? (
                <>
                  <dt>Put together as</dt>
                  <dd className="text-muted">
                    {g.template.name}
                    {g.template.vars ? ` (${Object.keys(g.template.vars).join(', ')})` : ''}
                  </dd>
                </>
              ) : null}
              <dt>Settings</dt>
              <dd className="text-muted">
                {g.params.seconds} s, {resLabel(g.params.resolution)}, {g.params.aspectRatio}, {g.params.quality}, {g.params.task}
              </dd>
              {g.references?.map((r) => (
                <div key={r.role + r.sha256} className="contents">
                  <dt>{r.role}</dt>
                  <dd className="tc min-w-0 text-muted [overflow-wrap:anywhere]">
                    {r.ref} · sha256 {r.sha256.slice(0, 16)}…{r.people?.length ? ` · shows ${r.people.join(', ')}` : ''}
                  </dd>
                </div>
              ))}
              <dt>Cost</dt>
              <dd className="text-muted">
                estimate {money(g.estimate.usd)}
                {g.outcome?.actual ? `, charged ${money(g.outcome.actual.usd)} (${g.outcome.actual.basis})` : ''}
              </dd>
              {g.outcome?.safety ? (
                <>
                  <dt>Safety filters</dt>
                  <dd className="text-muted">
                    {g.outcome.safety.side} side, {g.outcome.safety.category}
                    {g.outcome.safety.supportCodes?.length ? `, codes ${g.outcome.safety.supportCodes.join(', ')}` : ''}
                  </dd>
                </>
              ) : null}
              <dt>Request</dt>
              <dd className="tc min-w-0 text-muted [overflow-wrap:anywhere]">
                {g.id} · sha256 {g.requestHash.slice(0, 16)}…{g.ticket ? ` · at Google ${g.ticket.id}` : ''}
              </dd>
              <dt>Deleted at Google</dt>
              <dd className="text-muted">{g.forgotten ? (g.forgotten.ok ? `yes, ${absolute(g.forgotten.at)}` : `not all: ${g.forgotten.detail ?? ''}`) : 'not yet'}</dd>
              <dt>Asked by</dt>
              <dd className="text-muted">
                {g.createdBy.userName || 'someone'}, {g.channel}, on {g.machine}, {absolute(g.createdAt)}
              </dd>
              {g.parent ? (
                <>
                  <dt>Came from</dt>
                  <dd className="text-muted">
                    {g.parent.operation} take {g.parent.take}: {g.parent.prompt}
                  </dd>
                </>
              ) : null}
              <dt>From a terminal</dt>
              <dd className="tc text-muted">metarush generate show {g.id}</dd>
            </dl>
          </Details>
          {g.projectId ? (
            <Link to={`/p/${g.projectId}?tab=sources&generate=1`} className="mt-1 inline-block text-[12px] font-medium text-accent-text hover:underline">
              All generated shots of this project
            </Link>
          ) : null}
        </>
      ) : null}
    </div>
  );
}
