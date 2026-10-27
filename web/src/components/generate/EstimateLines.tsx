import { CheckCircle, Info, WarningCircle } from '@phosphor-icons/react';
import type { GenEstimate, GenLimit } from '../../api/types';
import { Details } from '../../ui/primitives';
import { CostEstimate } from '../CostEstimate';
import { money } from './ai';
import { describe } from './describe';

function limitLine(label: string, l: GenLimit | undefined | null): string | null {
  if (!l) return null;
  const used = l.spentUsd + l.heldUsd;
  if (l.limitUsd === undefined || l.limitUsd === null) return `${label}: ${money(used)} spent, no limit of its own`;
  return `${label}: ${money(used)} of ${money(l.limitUsd)} spent${l.default ? ' (the usual limit)' : ''}`;
}

/**
 * The price of a request with what may stop it, in plain words: an estimate
 * that opens to what it draws on (D208), the technical numbers behind Details.
 */
export function EstimateLines({ estimate: e }: { estimate: GenEstimate }) {
  const lines = [limitLine('This project', e.budget.project), limitLine('Today, for the whole team', e.budget.today)].filter(Boolean) as string[];
  return (
    <div className="space-y-1.5 text-[13px] leading-relaxed" data-estimate>
      <p className="font-medium text-fg" data-estimate-summary>
        About {money(e.usd)}
        <span className="font-normal text-muted"> for {describe(e)}</span>
      </p>
      <CostEstimate estimate={e.cost} bare />
      {e.references?.map((r) => (
        <p key={r} className="text-[12.5px] text-muted">
          {r}
        </p>
      ))}
      {e.consent.ok && e.consent.message ? (
        <p className="flex items-start gap-1.5 text-[12.5px] text-ok">
          <CheckCircle size={14} weight="fill" className="mt-[2px] shrink-0" /> {e.consent.message}
        </p>
      ) : null}
      {e.problems?.map((p) => (
        <p key={p} className="flex items-start gap-1.5 text-[12.5px] text-danger" data-estimate-problem>
          <WarningCircle size={14} weight="fill" className="mt-[2px] shrink-0" /> {p}
        </p>
      ))}
      {e.notes?.map((n) => (
        <p key={n} className="flex items-start gap-1.5 text-[12.5px] text-muted">
          <Info size={14} className="mt-[2px] shrink-0" /> {n}
        </p>
      ))}
      {lines.map((l) => (
        <p key={l} className="text-[12px] text-subtle">
          {l}
        </p>
      ))}
      <Details compact>
        <dl className="grid grid-cols-[130px_minmax(0,1fr)] gap-y-0.5 text-[12px]">
          <dt>Model</dt>
          <dd className="text-muted">
            {e.modelTitle} ({e.model}, {e.stage})
          </dd>
          <dt>Per take</dt>
          <dd className="text-muted">
            ${e.perTakeUsd.toFixed(3)}, ${e.perSecondUsd.toFixed(3)} a second of video
          </dd>
          <dt>Tokens per take</dt>
          <dd className="text-muted">
            video {e.videoTokens.toLocaleString()}, input {e.inputTokens.toLocaleString()}, thinking allowance {e.thinkingTokens.toLocaleString()}
          </dd>
          <dt>Price table</dt>
          <dd className="text-muted">{e.priceTable}</dd>
          <dt>Kind of request</dt>
          <dd className="text-muted">{e.task}</dd>
        </dl>
      </Details>
    </div>
  );
}
