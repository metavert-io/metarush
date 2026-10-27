import { Link, useSearchParams } from 'react-router';
import { CheckCircle, Circle, Database, Key, Package, Sparkle, X } from '@phosphor-icons/react';
import { useOp } from '../api/queries';
import type { ComponentState, WorksItem } from '../api/types';
import { Button, cx, ErrorPanel, Skeleton } from '../ui/primitives';
import { AddComponentButton, useAddComponent } from './Components';

/**
 * The dashboard's first run (D256): what works now, free and on this computer,
 * what a key adds, Claude Code's sign-in (D255) and where the projects are
 * kept — the same words `metarush setup` ends with (op setup.status). Shown
 * while the workspace has no projects and no footage, or when setup opened
 * the dashboard (`/?welcome=1`). What isn't ready offers Add it for the
 * component that makes it work, and the optional components not here yet
 * are listed with Add it, the suggested ones first (D324).
 */
export function GettingStarted({ className }: { className?: string }) {
  const [params, setParams] = useSearchParams();
  const asked = params.get('welcome') === '1';
  const q = useOp('setup.status', {}, { staleTime: 60_000 });
  const { add, busy } = useAddComponent();
  if (!asked && !q.data?.firstRun) return null;
  const missing = q.data?.components ?? [];
  const byName = new Map(missing.map((c) => [c.name, c]));
  const hide = () => {
    const next = new URLSearchParams(params);
    next.delete('welcome');
    setParams(next, { replace: true });
  };
  return (
    <section aria-labelledby="getting-started-h" className={cx('panel p-5', className)} data-getting-started>
      <div className="flex items-start gap-3">
        <div className="mt-0.5 text-accent-text">
          <Sparkle size={18} weight="fill" />
        </div>
        <div className="min-w-0 flex-1">
          <h2 id="getting-started-h" className="text-[15px] font-semibold text-fg">
            MetaRush is ready
          </h2>
          <p className="mt-1 text-[13px] text-muted">{q.data?.summary ?? 'Checking what works on this computer…'}</p>
        </div>
        {asked && !q.data?.firstRun ? (
          <Button size="sm" variant="ghost" icon={<X size={13} />} onClick={hide} aria-label="Close getting started">
            Close
          </Button>
        ) : null}
      </div>
      {q.isLoading ? (
        <div className="mt-4 space-y-2">
          <Skeleton className="h-10" />
          <Skeleton className="h-10" />
        </div>
      ) : q.error ? (
        <ErrorPanel className="mt-3" compact error={q.error} onRetry={() => void q.refetch()} />
      ) : q.data ? (
        <div className="mt-4 grid grid-cols-1 gap-5 lg:grid-cols-2">
          <div>
            <h3 className="text-[12px] font-medium uppercase tracking-wide text-subtle">Works now, free and on this computer</h3>
            <ul className="mt-2 space-y-2.5" data-no-keys>
              {q.data.noKeys.map((x) => (
                <Item key={x.key} item={x} component={x.component ? byName.get(x.component) : undefined} add={add} busy={busy} />
              ))}
            </ul>
            {missing.length ? <MoreComponents components={missing} add={add} busy={busy} shown={new Set(q.data.noKeys.filter((x) => !x.ready).map((x) => x.component ?? ''))} /> : null}
          </div>
          <div className="space-y-5">
            {q.data.withKeys.length ? (
              <div>
                <h3 className="text-[12px] font-medium uppercase tracking-wide text-subtle">What a key adds</h3>
                <ul className="mt-2 space-y-2.5" data-with-keys>
                  {q.data.withKeys.map((x) => (
                    <Item key={x.key} item={x} keyed />
                  ))}
                </ul>
                <p className="mt-2 text-[12px] text-subtle">You always see the price before anything paid runs, and it&rsquo;s paid to the service directly.</p>
              </div>
            ) : null}
            <div>
              <h3 className="text-[12px] font-medium uppercase tracking-wide text-subtle">Claude Code</h3>
              <p className="mt-2 flex items-start gap-2 text-[13px] text-fg" data-claude-code>
                <Mark ready={q.data.claude.ready} />
                <span className="min-w-0">
                  {q.data.claude.says}
                  {!q.data.claude.ready && q.data.claude.next ? <span className="mt-0.5 block text-[12px] text-muted">{q.data.claude.next}</span> : null}
                  {!q.data.claude.ready && q.data.claude.command ? <CommandDetails command={q.data.claude.command} /> : null}
                </span>
              </p>
            </div>
            <p className="flex items-start gap-2 text-[12.5px] text-muted" data-database>
              <Database size={15} className="mt-0.5 shrink-0 text-subtle" />
              <span className="min-w-0">{q.data.database.says}</span>
            </p>
            <p className="text-[12.5px] text-muted">Start by dropping footage into the box below, or ask Claude Code to start a project for you.</p>
          </div>
        </div>
      ) : null}
    </section>
  );
}

function Mark({ ready }: { ready: boolean }) {
  return ready ? <CheckCircle size={16} weight="fill" className="mt-0.5 shrink-0 text-ok" /> : <Circle size={16} className="mt-0.5 shrink-0 text-subtle" />;
}

/** The optional components not here yet (besides those above), the suggested ones first. */
function MoreComponents({ components, add, busy, shown }: { components: ComponentState[]; add: (c: ComponentState) => Promise<void>; busy: string | null; shown: Set<string> }) {
  const rest = components.filter((c) => !shown.has(c.name));
  const suggested = rest.filter((c) => c.suggested);
  if (!rest.length) return null;
  return (
    <div className="mt-5" data-more-components>
      <h3 className="flex items-center gap-1.5 text-[12px] font-medium uppercase tracking-wide text-subtle">
        <Package size={13} /> More you can add
      </h3>
      <ul className="mt-2 space-y-2.5">
        {suggested.slice(0, 6).map((c) => (
          <li key={c.name} className="flex items-start gap-2" data-more-component={c.name}>
            <Circle size={16} className="mt-0.5 shrink-0 text-subtle" />
            <div className="min-w-0 flex-1">
              <div className="text-[13px] font-medium text-fg">{c.title}</div>
              <div className="text-[12.5px] text-muted">
                {c.unlocks} <span className="text-subtle">({c.size})</span>
              </div>
            </div>
            <AddComponentButton c={c} add={add} busy={busy} />
          </li>
        ))}
      </ul>
      <Link to="/settings/components" className="mt-2 inline-block text-[12.5px] text-accent-text hover:underline">
        {rest.length > suggested.length ? `All components, ${rest.length - Math.min(suggested.length, 6)} more (Settings → Components)` : 'Settings → Components'}
      </Link>
    </div>
  );
}

function Item({ item, keyed, component, add, busy }: { item: WorksItem; keyed?: boolean; component?: ComponentState; add?: (c: ComponentState) => Promise<void>; busy?: string | null }) {
  return (
    <li className="flex items-start gap-2" data-works={item.key} data-ready={item.ready ? 'yes' : 'no'}>
      {keyed && !item.ready ? <Key size={16} className="mt-0.5 shrink-0 text-subtle" /> : <Mark ready={item.ready} />}
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-medium text-fg">{item.name}</div>
        <div className="text-[12.5px] text-muted">{item.says}</div>
        {!item.ready && item.next ? (
          <div className="mt-0.5 text-[12px] text-subtle">
            {item.place ? (
              <Link to={item.place} className="text-accent-text hover:underline">
                {item.next}
              </Link>
            ) : (
              item.next
            )}
          </div>
        ) : null}
        {!item.ready && item.command && (!item.place || !keyed) ? <CommandDetails command={item.command} /> : null}
      </div>
      {!item.ready && component && add ? <AddComponentButton c={component} add={add} busy={busy ?? null} /> : null}
    </li>
  );
}

/** The command that sets something up, behind Details (D109: commands stay out of plain copy). */
function CommandDetails({ command }: { command: string }) {
  return (
    <details className="mt-1 text-[12px]" data-command-details>
      <summary className="cursor-pointer text-subtle hover:text-muted">Details</summary>
      <code className="tc mt-1 block whitespace-pre-wrap break-all rounded-[8px] bg-inset px-2 py-1.5 text-[11.5px] text-muted">{command}</code>
    </details>
  );
}
