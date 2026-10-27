import { useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, CheckCircle, Globe, Key, Play, Prohibit, PuzzlePiece, ShieldCheck, WarningCircle, Wrench, XCircle } from '@phosphor-icons/react';
import { call } from '../api/client';
import { useOp, useOpMutation } from '../api/queries';
import type { AbilityFileLook, AbilityInput, HereLocal, AbilityPlanOut, AbilityRow, AbilityRunOut, AbilityShowOut, AbilityVersion, CatalogueShowOut, MenuItem } from '../api/types';
import { CostEstimate } from '../components/CostEstimate';
import { ExportAbilityButton, ImportAbilityButton, ImportedSection } from '../components/abilities/Files';
import { OwnerOnly, OwnersNote, useOwnership } from '../components/OwnerOnly';
import { bytes, plural, relative } from '../lib/format';
import { money } from '../lib/costs';
import { useDialog } from '../ui/Dialog';
import { useToast } from '../ui/Toast';
import { Badge, Button, cx, Details, EmptyState, ErrorPanel, SectionTitle, Skeleton, Theirs, type Tone } from '../ui/primitives';

/*
 * Library → Abilities (D230–D235): capabilities added without a new
 * MetaRush. An owner reads what each version may do — what it installs, the
 * places it reaches and the keys MetaRush adds there, what leaves this
 * computer, what it costs and its licence — and approves it; a person sets
 * it up on each computer; then members run it, in the sandbox. The dashboard
 * shows what the server says: it never runs an ability's code.
 *
 * The page opens on the menu (D272): what MetaRush does built in (the
 * first-party catalogue, D269) and the abilities this workspace added, side
 * by side, each saying whether it works on this computer now or what it
 * needs first (catalogue.list).
 */

const QUERIES = ['ability.list', 'ability.show', 'ability.runs', 'ability.find', 'catalogue.list', 'catalogue.show'] as const;

export function abilitiesLink(o: { ability?: string; version?: string; run?: string; builtin?: string } = {}): string {
  const p = new URLSearchParams();
  if (o.builtin) p.set('builtin', o.builtin);
  if (o.ability) p.set('ability', o.ability);
  if (o.version) p.set('version', o.version);
  if (o.run) p.set('run', o.run);
  const q = p.toString();
  return '/library/abilities' + (q ? `?${q}` : '');
}

const stateTone: Record<string, Tone> = { waiting: 'accent', approved: 'ok', turned_down: 'danger', revoked: 'danger', failed: 'danger', imported: 'info' };

function StateBadge({ state, label }: { state: string; label: string }) {
  const icon = state === 'approved' ? <CheckCircle size={12} weight="fill" /> : state === 'waiting' ? <ShieldCheck size={12} /> : <XCircle size={12} weight="fill" />;
  return (
    <Badge tone={stateTone[state] ?? 'neutral'} icon={icon}>
      {label}
    </Badge>
  );
}

/** Library → Abilities: the menu, one built-in ability (?builtin=), one added ability (?ability=), one run (&run=). */
export function AbilitiesPage() {
  const [params] = useSearchParams();
  const builtin = params.get('builtin');
  if (builtin) return <BuiltInPage id={builtin} />;
  const name = params.get('ability');
  if (name) return <AbilityPage name={name} version={params.get('version') ?? undefined} run={params.get('run') ?? undefined} />;
  return <AbilityList />;
}

/* ---------- the menu: built in and added, side by side (D272) ---------- */

type Filter = 'all' | 'built-in' | 'added' | 'waiting' | 'works';

const FILTERS: [Filter, string][] = [
  ['all', 'Everything'],
  ['works', 'Works here'],
  ['built-in', 'Built in'],
  ['added', 'Added'],
  ['waiting', 'Waiting for an owner'],
];

function AbilityList() {
  const [filter, setFilter] = useState<Filter>('all');
  const menu = useOp('catalogue.list', {});
  const list = useOp('ability.list', {});
  const items = menu.data?.items ?? [];
  const byName = new Map(items.map((it) => [it.name, it]));
  const builtIn = items.filter((it) => it.kind === 'first-party' && (filter !== 'works' || it.worksHere));
  const rows = (list.data?.abilities ?? []).filter((r) => {
    switch (filter) {
      case 'waiting':
        return r.state === 'waiting' || !!r.waitingVersion;
      case 'works':
        return !!byName.get(r.name)?.worksHere;
      default:
        return true;
    }
  });
  const showBuiltIn = filter === 'all' || filter === 'built-in' || filter === 'works';
  const showAdded = filter !== 'built-in';
  const error = menu.error ?? list.error;
  return (
    <div data-abilities-page>
      <h2 className="sr-only">Abilities</h2>
      <div className="mb-3 flex flex-wrap items-center gap-2" role="group" aria-label="Show">
        {FILTERS.map(([k, label]) => (
          <button
            key={k}
            type="button"
            aria-pressed={filter === k}
            onClick={() => setFilter(k)}
            className={cx('inline-flex h-8 shrink-0 items-center gap-1.5 rounded-full px-3 text-[12.5px] transition-colors hairline', filter === k ? 'bg-panel text-fg' : 'text-muted hover:text-fg')}
          >
            {label}
            {k === 'waiting' && list.data?.waiting ? <span className="tc text-[11px] text-subtle">{list.data.waiting}</span> : null}
          </button>
        ))}
        <ImportAbilityButton />
      </div>
      {menu.data ? (
        <p className="mb-5 max-w-[75ch] text-[13px] text-muted" data-menu-summary>
          {menu.data.message}
        </p>
      ) : null}
      {error ? (
        <ErrorPanel error={error} onRetry={() => void (menu.isError ? menu.refetch() : list.refetch())} />
      ) : menu.isLoading || list.isLoading ? (
        <div className="grid gap-3 sm:grid-cols-2">
          <Skeleton className="h-28" />
          <Skeleton className="h-28" />
        </div>
      ) : (
        <div className="space-y-8">
          {showBuiltIn
            ? (menu.data?.groups ?? [])
                .filter((g) => g.key !== 'added')
                .map((g) => {
                  const its = builtIn.filter((it) => it.group === g.key);
                  if (its.length === 0) return null;
                  return (
                    <section key={g.key} aria-labelledby={`menu-${g.key}`} data-menu-group={g.key}>
                      <SectionTitle id={`menu-${g.key}`} meta={`${g.worksNow} of ${g.count} work here`}>
                        {g.title}
                      </SectionTitle>
                      <ul className="grid gap-2.5 sm:grid-cols-2 xl:grid-cols-3">
                        {its.map((it) => (
                          <BuiltInCard key={it.name} item={it} />
                        ))}
                      </ul>
                    </section>
                  );
                })
            : null}
          {showAdded ? (
            <section aria-labelledby="menu-added" data-menu-group="added">
              {filter === 'all' || filter === 'works' ? (
                <SectionTitle id="menu-added" meta={rows.length ? plural(rows.length, 'ability', 'abilities') : undefined}>
                  Added to this workspace
                </SectionTitle>
              ) : (
                <h3 id="menu-added" className="sr-only">
                  Added to this workspace
                </h3>
              )}
              {rows.length === 0 ? (
                <EmptyState
                  compact={filter === 'all' || filter === 'works'}
                  icon={<PuzzlePiece size={22} />}
                  title={filter === 'waiting' ? 'Nothing waits for an owner' : filter === 'works' ? 'No added ability works here yet' : 'No abilities added yet'}
                  body="An ability adds something MetaRush can do, like finding stock footage or making a new kind of picture, without a new MetaRush. Claude or a person writes one and checks it; it shows up here for an owner to approve."
                  action={
                    filter === 'added' || filter === 'all' ? (
                      <Details label="How to add one">
                        <p className="text-[12.5px] text-muted">Check its folder in a terminal on this computer:</p>
                        <code className="mt-1 block text-[12px]">metarush ability check ./my-ability</code>
                      </Details>
                    ) : undefined
                  }
                />
              ) : (
                <ul className="grid gap-3 sm:grid-cols-2" data-ability-list>
                  {rows.map((r) => (
                    <AbilityCard key={r.name} row={r} item={byName.get(r.name)} />
                  ))}
                </ul>
              )}
            </section>
          ) : null}
        </div>
      )}
    </div>
  );
}

/** Works here, or what it needs first, in a few words. */
function WorksHere({ item }: { item: MenuItem }) {
  if (item.worksHere) {
    return (
      <Badge tone="ok" icon={<CheckCircle size={12} weight="fill" />}>
        Works here
      </Badge>
    );
  }
  const needs = item.missing ?? 'needs something first';
  return (
    <span className="inline-flex min-w-0 max-w-full items-start gap-1 rounded-[11px] bg-hover px-2 py-[3px] text-[11.5px] font-medium leading-snug text-muted" data-needs>
      <WarningCircle size={12} className="mt-[2px] shrink-0" aria-hidden />
      <span className="min-w-0 break-words">{needs.charAt(0).toUpperCase() + needs.slice(1)}</span>
    </span>
  );
}

function costWords(item: MenuItem): string | null {
  switch (item.cost) {
    case 'priced':
      return 'Priced first';
    case 'keyed':
      return 'The service may charge';
    case 'plan':
      return 'Uses your Claude plan';
    default:
      return null;
  }
}

/** What it costs, in a sentence; "It costs nothing." only with no price and no key (D268). */
function costSentence(cost: MenuItem['cost']): string | null {
  switch (cost) {
    case 'free':
      return 'It costs nothing.';
    case 'priced':
      return 'Each run is priced first and paid to the service that does it.';
    case 'keyed':
      return 'It uses a key to a service, which may charge for what it does.';
    case 'plan':
      return 'It uses your Claude Code, which counts toward your Claude plan.';
    default:
      return null;
  }
}

function BuiltInCard({ item: it }: { item: MenuItem }) {
  const cost = costWords(it);
  return (
    <li>
      <Link
        to={abilitiesLink({ builtin: it.id })}
        className="flex h-full flex-col gap-2 rounded-[12px] bg-panel p-3.5 transition-colors hairline hover:bg-hover"
        data-builtin={it.id}
        data-works={it.worksHere ? 'yes' : 'no'}
      >
        <div className="flex items-start justify-between gap-2">
          <h4 className="text-[14px] font-semibold tracking-[-0.01em]">{it.title}</h4>
        </div>
        <p className="text-[12.5px] leading-relaxed text-muted">{it.does}</p>
        <div className="mt-auto flex flex-wrap items-center gap-2 pt-1">
          <WorksHere item={it} />
          {it.worksHere && it.partly ? <span className="text-[11.5px] text-subtle">Here: {it.partly}</span> : null}
          {cost ? <span className="text-[11.5px] text-subtle">{cost}</span> : null}
          {it.peopleOnly ? <span className="text-[11.5px] text-subtle">A person starts it</span> : null}
        </div>
      </Link>
    </li>
  );
}

function AbilityCard({ row: r, item }: { row: AbilityRow; item?: MenuItem }) {
  const shown = r.approved ? { state: r.approved.state, label: r.approved.stateLabel } : { state: r.state, label: r.stateLabel };
  return (
    <li>
      <Link
        to={abilitiesLink({ ability: r.name })}
        className="block h-full rounded-[14px] bg-panel p-4 transition-colors hairline hover:bg-hover"
        data-ability={r.name}
        data-works={item?.worksHere ? 'yes' : 'no'}
      >
        <div className="flex items-start gap-3">
          <span className="mt-0.5 text-accent-text" aria-hidden>
            <PuzzlePiece size={18} weight="duotone" />
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="text-[15px] font-semibold tracking-[-0.01em]">
                <Theirs>{r.title}</Theirs>
              </h3>
              <StateBadge state={shown.state} label={shown.label} />
            </div>
            <p className="mt-1 text-[13px] leading-relaxed text-muted">
              <Theirs>{r.does}</Theirs>
            </p>
            <p className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-[12px] text-subtle">
              <span>{r.cost === 'free' ? 'Free' : r.costWords}</span>
              <span>{r.sendsWords}</span>
              <span>{r.setUpHere ? 'Set up on this computer' : 'Not set up on this computer'}</span>
            </p>
            {item ? (
              <div className="mt-2">
                <WorksHere item={item} />
              </div>
            ) : null}
            {r.waitingVersion ? <p className="mt-1.5 text-[12px] text-accent-text">Version {r.waitingVersion} waits for an owner.</p> : null}
          </div>
        </div>
      </Link>
    </li>
  );
}

/* ---------- one built-in ability (D269) ---------- */

function BuiltInPage({ id }: { id: string }) {
  const show = useOp('catalogue.show', { name: id });
  const d = show.data;
  return (
    <div data-builtin-page={id}>
      <Link to={abilitiesLink()} className="mb-4 inline-flex items-center gap-1.5 text-[12.5px] text-muted hover:text-fg">
        <ArrowLeft size={13} /> All abilities
      </Link>
      {show.isError ? (
        <ErrorPanel error={show.error} onRetry={() => void show.refetch()} />
      ) : !d ? (
        <Skeleton className="h-48" />
      ) : (
        <BuiltInBody d={d} />
      )}
    </div>
  );
}

function BuiltInBody({ d }: { d: CatalogueShowOut }) {
  const it = d.item;
  const cost = costSentence(it.cost);
  return (
    <div className="max-w-[72ch] space-y-6">
      <header>
        <p className="text-[12px] font-medium uppercase tracking-[0.04em] text-subtle">{it.groupTitle}</p>
        <div className="mt-1 flex flex-wrap items-center gap-2">
          <h2 className="text-[22px] font-semibold tracking-[-0.02em]">{it.title}</h2>
          <WorksHere item={it} />
        </div>
        <p className="mt-1.5 text-[13.5px] leading-relaxed text-muted">{it.does}</p>
      </header>
      <section aria-labelledby="builtin-needs" className="rounded-[14px] bg-panel p-4 hairline">
        <SectionTitle id="builtin-needs">What it needs</SectionTitle>
        <dl className="divide-y divide-line">
          <Row label="On a computer" icon={<Wrench size={13} />}>
            {it.requires.length ? it.requires.join(', ') : 'Nothing more than MetaRush.'}
          </Row>
          <Row label="Here">
            {it.worksHere ? (it.partly ? `It works on this computer, ${it.partly}.` : 'It works on this computer now.') : `It ${it.missing ?? 'needs something first'}.`}
          </Row>
          {cost ? (
            <Row label="What it costs" icon={<Key size={13} />}>
              {cost}
            </Row>
          ) : null}
          {it.leaves ? <Row label="What leaves">{it.leaves.charAt(0).toUpperCase() + it.leaves.slice(1)}.</Row> : null}
          <Row label="Who starts it">{it.peopleOnly ? 'Only a person, in the dashboard or their own terminal.' : it.changes ? 'Anyone, Claude included; it changes something, so a change is asked about first.' : 'Anyone, Claude included; it only looks.'}</Row>
        </dl>
      </section>
      <Details label="Details">
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[12px]">
          <dt className="text-subtle">Name</dt>
          <dd className="font-mono">{it.name}</dd>
          <dt className="text-subtle">It runs</dt>
          <dd className="font-mono">
            {it.runs}
            {it.fixed ? ` ${JSON.stringify(it.fixed)}` : ''}
          </dd>
          {it.command ? (
            <>
              <dt className="text-subtle">Command</dt>
              <dd className="font-mono">{it.command}</dd>
            </>
          ) : null}
          <dt className="text-subtle">Receipts name it</dt>
          <dd className="font-mono">{it.madeWith}</dd>
        </dl>
      </Details>
    </div>
  );
}

/* ---------- one ability ---------- */

function AbilityPage({ name, version, run }: { name: string; version?: string; run?: string }) {
  const show = useOp('ability.show', { ref: version ? `${name}@${version}` : name });
  const d = show.data;
  return (
    <div data-ability-page={name}>
      <Link to={abilitiesLink()} className="mb-4 inline-flex items-center gap-1.5 text-[12.5px] text-muted hover:text-fg">
        <ArrowLeft size={13} /> All abilities
      </Link>
      {show.isError ? (
        <ErrorPanel error={show.error} onRetry={() => void show.refetch()} />
      ) : !d ? (
        <Skeleton className="h-64" />
      ) : (
        <AbilityBody d={d} run={run} />
      )}
    </div>
  );
}

function AbilityBody({ d, run }: { d: AbilityShowOut; run?: string }) {
  const v = d.ability;
  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-[22px] font-semibold tracking-[-0.02em]">
              <Theirs>{v.title}</Theirs>
            </h2>
            <StateBadge state={v.state} label={d.stateLabel} />
            <span className="text-[12.5px] text-subtle">Version {v.number}</span>
          </div>
          <p className="mt-1.5 max-w-[70ch] text-[13.5px] leading-relaxed text-muted">
            <Theirs>{v.does}</Theirs>
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <ExportAbilityButton v={v} />
          <SetUpButton d={d} />
          {v.state === 'approved' ? <RunButton d={d} /> : null}
        </div>
      </header>
      {run ? <RunPanel name={v.name} id={run} /> : null}
      <div className="grid gap-6 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
        <div className="space-y-6">
          <section aria-labelledby="ability-may-do" className="rounded-[14px] bg-panel p-4 hairline" data-approval-page>
            <SectionTitle id="ability-may-do">What it may do</SectionTitle>
            <MayDo d={d} />
            <div className="mt-4 border-t border-line pt-4">
              <Decide d={d} />
            </div>
          </section>
          {v.state === 'imported' ? <ImportedSection v={v} /> : <CheckSection v={v} />}
        </div>
        <div className="space-y-6">
          <HereSection d={d} />
          <InputsSection inputs={v.facts.inputs} />
          <VersionsSection d={d} />
          <RunsSection d={d} />
          <Details label="Details">
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[12px]">
              <dt className="text-subtle">Version</dt>
              <dd className="break-all font-mono">{v.hash}</dd>
              <dt className="text-subtle">Name</dt>
              <dd className="font-mono">{v.name}</dd>
              <dt className="text-subtle">It runs</dt>
              <dd className="font-mono">{v.facts.runs}</dd>
            </dl>
            <pre className="mt-3 max-h-80 overflow-auto rounded-[10px] bg-hover/60 p-3 text-[11.5px] leading-relaxed" data-manifest>
              {v.manifest}
            </pre>
            {v.notes ? <pre className="mt-3 max-h-60 overflow-auto whitespace-pre-wrap rounded-[10px] bg-hover/60 p-3 text-[11.5px] leading-relaxed">{v.notes}</pre> : null}
          </Details>
        </div>
      </div>
    </div>
  );
}

function Row({ label, icon, children }: { label: string; icon?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[9.5rem_1fr] gap-3 py-2 text-[13px] max-sm:grid-cols-1 max-sm:gap-1">
      <dt className="flex items-center gap-1.5 text-subtle">
        {icon}
        {label}
      </dt>
      <dd className="min-w-0 leading-relaxed">{children}</dd>
    </div>
  );
}

/** The page an owner approves a version from (D231). */
function MayDo({ d }: { d: AbilityShowOut }) {
  const p = d.page;
  return (
    <dl className="mt-2 divide-y divide-line">
      <Row label="What it installs" icon={<Wrench size={13} />}>
        <ul className="space-y-0.5">
          {p.installs.map((l, i) => (
            <li key={i}>{l}</li>
          ))}
        </ul>
      </Row>
      <Row label="What it reaches" icon={<Globe size={13} />}>
        {p.network}
      </Row>
      <Row label="Keys" icon={<Key size={13} />}>
        {p.keys.length === 0 ? (
          'None.'
        ) : (
          <ul className="space-y-0.5" data-keys>
            {p.keys.map((k) => (
              <li key={k.name}>
                The <Theirs>{k.name}</Theirs> key, in {k.place}, only for {k.hosts.join(', ')}.{' '}
                <span className="text-subtle">{k.configured ? `On this computer (${k.masked}).` : 'Not on this computer yet.'}</span>
              </li>
            ))}
            <li className="text-[12px] text-subtle">MetaRush adds keys on the way out; the ability's code only ever sees a stand-in.</li>
          </ul>
        )}
      </Row>
      <Row label="What leaves">{p.leaves}</Row>
      <Row label="What it brings back">{p.makes}</Row>
      <Row label="What it costs">{p.cost}</Row>
      <Row label="Licence">
        <Theirs>{p.licence}</Theirs>
      </Row>
      {p.terms ? (
        <Row label="The service's terms">
          <span data-terms>
            <Theirs>{p.terms}</Theirs>
          </span>
        </Row>
      ) : null}
      <Row label="Who starts a run">{p.decides}</Row>
      {p.outside ? <Row label="Outside MetaRush">{p.outside}</Row> : null}
      {p.local ? <Row label="This computer">{p.local}</Row> : null}
      <Row label="Limits">{p.limits}</Row>
    </dl>
  );
}

function Decide({ d }: { d: AbilityShowOut }) {
  const v = d.ability;
  const dialog = useDialog();
  const toast = useToast();
  const approve = useOpMutation('ability.approve', { invalidate: [...QUERIES] });
  const revoke = useOpMutation('ability.revoke', { invalidate: [...QUERIES] });
  const busy = approve.isPending || revoke.isPending;
  const ref = `${v.name}@${v.hash}`;
  const open = v.facts.network === 'open';
  const local = v.facts.local ?? null;
  const done = (o: { message: string }) => toast({ tone: 'success', title: o.message });
  const failed = (e: Error) => toast({ tone: 'error', title: 'That did not work', body: e.message });

  const onApprove = async () => {
    const ok = await dialog.confirm({
      title: `Approve “${v.title}”?`,
      body: (
        <div className="space-y-2 text-[13px]">
          <p>Members can then run this exact version on every computer a person sets it up on.</p>
          <p>{d.page.network}</p>
          <p>{d.page.leaves}</p>
          <p>
            {d.page.cost}. Licence: <Theirs>{d.page.licence}</Theirs>.
          </p>
          {d.page.terms ? (
            <p className="font-medium" data-terms>
              The service's terms: <Theirs>{d.page.terms}</Theirs>
            </p>
          ) : null}
          {open ? <p className="font-medium text-danger">It also asks to reach any public place on the internet, not only the ones it names; approving it agrees to that too.</p> : null}
          {local ? (
            <>
              <p className="font-medium text-danger" data-local-service>
                It also asks to reach <Theirs>{local.service}</Theirs> on the computer it runs on (port {local.port}), a program outside MetaRush&apos;s sandbox; approving it agrees to
                the program listening there now, and only that one.
              </p>
              <ListeningNow l={d.local} />
            </>
          ) : null}
          <p className="text-muted">If anyone changes its files, the new version needs an owner again.</p>
        </div>
      ),
      confirmLabel: open ? 'Approve, with the whole internet' : local ? `Approve, with ${local.service}` : 'Approve',
    });
    if (ok)
      approve.mutate(
        { ref, openNetwork: open || undefined, localService: local ? true : undefined, localProgram: local && d.local?.now ? d.local.now : undefined },
        { onSuccess: done, onError: failed },
      );
  };
  const onReject = async () => {
    const note = await dialog.prompt({
      title: `Turn down “${v.title}”?`,
      body: <p>Nobody can run this version.</p>,
      label: 'Why (optional)',
      multiline: true,
      maxLength: 1000,
      confirmLabel: 'Turn down',
      tone: 'danger',
    });
    if (note !== null) approve.mutate({ ref, reject: true, note: note.trim() || undefined }, { onSuccess: done, onError: failed });
  };
  const onRevoke = async () => {
    const note = await dialog.prompt({
      title: `Take back the approval of “${v.title}”?`,
      body: <p>Nothing runs this version until an owner approves it again. What earlier runs brought back stays.</p>,
      label: 'Why (optional)',
      multiline: true,
      maxLength: 1000,
      confirmLabel: 'Take back',
      tone: 'danger',
    });
    if (note !== null) revoke.mutate({ ref, note: note.trim() || undefined }, { onSuccess: done, onError: failed });
  };
  if (v.state === 'failed') {
    return <p className="text-[12.5px] text-muted">Its check didn't pass, so nobody can approve this version.</p>;
  }
  if (v.state === 'imported') {
    return <p className="text-[12.5px] text-muted">It came from another workspace: it is checked on this computer first, then an owner here can approve it.</p>;
  }
  return (
    <OwnerOnly note>
      <div className="flex flex-wrap items-center gap-2" data-decide>
        {v.state === 'approved' ? (
          <Button variant="secondary" icon={<Prohibit size={14} />} loading={revoke.isPending} disabled={busy} onClick={() => void onRevoke()}>
            Take back the approval
          </Button>
        ) : (
          <>
            {v.state === 'waiting' ? (
              <Button variant="ghost" icon={<XCircle size={14} />} loading={approve.isPending && !!approve.variables?.reject} disabled={busy} onClick={() => void onReject()}>
                Turn down
              </Button>
            ) : null}
            <Button variant="primary" icon={<CheckCircle size={14} />} loading={approve.isPending && !approve.variables?.reject} disabled={busy || !d.canApprove} onClick={() => void onApprove()}>
              Approve this version
            </Button>
          </>
        )}
      </div>
    </OwnerOnly>
  );
}

function CheckSection({ v }: { v: AbilityVersion }) {
  const c = v.check;
  const t = c.test;
  return (
    <section aria-labelledby="ability-check" className="rounded-[14px] bg-panel p-4 hairline" data-check>
      <SectionTitle id="ability-check" meta={`${relative(c.at)} on ${c.machine}`}>
        Its check
      </SectionTitle>
      <p className={cx('mt-2 flex items-center gap-1.5 text-[13px]', c.ok ? 'text-ok' : 'text-danger')}>
        {c.ok ? <CheckCircle size={14} weight="fill" /> : <WarningCircle size={14} weight="fill" />}
        {c.ok ? 'It passed: it ran once in the sandbox with the network off.' : "It didn't pass."}
      </p>
      {c.problems.length ? (
        <ul className="mt-2 list-disc space-y-0.5 pl-5 text-[12.5px] text-danger">
          {c.problems.map((p, i) => (
            <li key={i}>{p}</li>
          ))}
        </ul>
      ) : null}
      {c.warnings.length ? (
        <ul className="mt-2 space-y-0.5 text-[12.5px] text-muted">
          {c.warnings.map((w, i) => (
            <li key={i}>{w}</li>
          ))}
        </ul>
      ) : null}
      {t ? (
        <p className="mt-2 text-[12.5px] text-muted">
          Its test took {t.seconds.toFixed(1)} s{t.outputs.length ? ` and made ${plural(t.outputs.length, 'file')}` : ''}.
        </p>
      ) : null}
      <p className="mt-2 text-[12px] text-subtle">
        {plural(v.files.length, 'file')}, {bytes(v.files.reduce((n, f) => n + f.size, 0))}
        {v.ignored?.length ? `; ${plural(v.ignored.length, 'hidden file')} left out` : ''}.
      </p>
      {t?.log ? (
        <Details label="What its test printed">
          <pre className="max-h-72 overflow-auto whitespace-pre-wrap text-[11.5px] leading-relaxed">{t.log}</pre>
        </Details>
      ) : null}
    </section>
  );
}

function HereSection({ d }: { d: AbilityShowOut }) {
  const h = d.here;
  return (
    <section aria-labelledby="ability-here" className="rounded-[14px] bg-panel p-4 hairline" data-here>
      <SectionTitle id="ability-here">On this computer</SectionTitle>
      <p className={cx('mt-2 text-[13px]', h.ready ? 'text-ok' : 'text-muted')}>{h.ready ? 'Everything a run needs is here.' : h.problems.join(' ')}</p>
      {h.programs.length || h.files.length ? (
        <ul className="mt-2 space-y-0.5 text-[12.5px] text-muted">
          {h.programs.map((p) => (
            <li key={p.name}>{p.says}</li>
          ))}
          {h.files.map((f) => (
            <li key={`file-${f.name}`} data-here-file={f.name}>
              {f.state}
            </li>
          ))}
        </ul>
      ) : null}
      {h.apart ? <p className="mt-2 text-[12px] text-subtle">{h.apart}</p> : null}
      {d.setUpOn.length ? <p className="mt-2 text-[12px] text-subtle">Set up on {d.setUpOn.join(', ')}.</p> : null}
    </section>
  );
}

function InputsSection({ inputs }: { inputs: AbilityInput[] }) {
  if (!inputs.length) return null;
  return (
    <section aria-labelledby="ability-inputs" className="rounded-[14px] bg-panel p-4 hairline">
      <SectionTitle id="ability-inputs">What a run takes</SectionTitle>
      <ul className="mt-2 space-y-1 text-[13px]">
        {inputs.map((i) => (
          <li key={i.name}>
            <span className="font-medium">{i.name}</span> <span className="text-subtle">({kindWord(i.kind)}{i.optional ? ', optional' : ''})</span>
            {i.says ? (
              <>
                : <Theirs>{i.says}</Theirs>
              </>
            ) : null}
          </li>
        ))}
      </ul>
    </section>
  );
}

function kindWord(k: string): string {
  return ({ words: 'words', number: 'a number', choice: 'a choice', 'yes-no': 'yes or no', footage: 'a clip', picture: 'a picture', sound: 'a sound clip' } as Record<string, string>)[k] ?? k;
}

function VersionsSection({ d }: { d: AbilityShowOut }) {
  if (d.versions.length < 2) return null;
  return (
    <section aria-labelledby="ability-versions" className="rounded-[14px] bg-panel p-4 hairline">
      <SectionTitle id="ability-versions">Versions</SectionTitle>
      <ul className="mt-2 space-y-1 text-[13px]">
        {d.versions.map((r) => (
          <li key={r.version} className="flex items-center gap-2">
            <Link to={abilitiesLink({ ability: d.ability.name, version: r.version })} className={cx('hover:underline', r.version === d.ability.hash && 'font-medium')}>
              Version {r.number}
            </Link>
            <StateBadge state={r.state} label={r.stateLabel} />
            <span className="text-[12px] text-subtle">checked {relative(r.checkedAt)}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function RunsSection({ d }: { d: AbilityShowOut }) {
  if (!d.runs.length) return null;
  return (
    <section aria-labelledby="ability-runs" className="rounded-[14px] bg-panel p-4 hairline" data-runs>
      <SectionTitle id="ability-runs">Latest runs</SectionTitle>
      <ul className="mt-2 space-y-1 text-[13px]">
        {d.runs.map((r) => (
          <li key={r.id}>
            <Link to={abilitiesLink({ ability: d.ability.name, run: r.id })} className="hover:underline">
              Run {r.number}
            </Link>{' '}
            <span className="text-muted">
              {r.stateLabel} on {r.machine}, {relative(r.createdAt)}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function RunPanel({ name, id }: { name: string; id: string }) {
  const runs = useOp('ability.runs', { id }, { refetchInterval: (q) => (q.state.data?.run && ['queued', 'running'].includes(q.state.data.run.state) ? 2000 : false) });
  const r = runs.data?.run;
  return (
    <section aria-labelledby="ability-run" className="rounded-[14px] bg-panel p-4 hairline" data-run={id}>
      {runs.isError ? (
        <ErrorPanel error={runs.error} compact />
      ) : !r ? (
        <Skeleton className="h-20" />
      ) : (
        <>
          <SectionTitle id="ability-run" meta={`on ${r.machine}, ${relative(r.createdAt)}`} action={<Link to={abilitiesLink({ ability: name })} className="text-[12px] text-muted hover:text-fg">Close</Link>}>
            Run {r.number}: {r.stateLabel}
          </SectionTitle>
          {r.summary || r.error ? <p className={cx('mt-2 text-[13px]', r.error ? 'text-danger' : '')}>{r.summary || r.error}</p> : null}
          {r.outputs.length ? (
            <ul className="mt-2 space-y-0.5 text-[12.5px]" data-outputs>
              {r.outputs.map((o) => (
                <li key={o.path}>
                  <Theirs>{o.title || o.path}</Theirs> <span className="text-subtle">— {o.status}{o.why ? `: ${o.why}` : ''}</span>
                  {o.credit || o.licence ? (
                    <span className="block text-[12px] text-subtle" data-output-credit>
                      <Theirs>{o.credit || o.licence}</Theirs>
                    </span>
                  ) : null}
                </li>
              ))}
            </ul>
          ) : null}
          {runs.data?.requests?.length ? (
            <Details label={`The places it reached (${plural(r.requests, 'request')}${r.refused ? `, ${r.refused} refused` : ''})`}>
              <ul className="space-y-0.5 text-[12px]">
                {runs.data.requests.map((q, i) => (
                  <li key={i} className={q.refused ? 'text-danger' : ''}>
                    {q.method} {q.host}
                    {q.port && q.port !== 443 && q.port !== 80 ? `:${q.port}` : ''}
                    {q.path} {q.refused ? `— refused: ${q.refused}` : q.status ? `(${q.status})` : ''}
                    {q.keys?.length ? ` · with the ${q.keys.join(', ')} key` : ''}
                  </li>
                ))}
              </ul>
            </Details>
          ) : null}
          {runs.data?.log ? (
            <Details label="What it printed">
              <pre className="max-h-72 overflow-auto whitespace-pre-wrap text-[11.5px] leading-relaxed">{runs.data.log}</pre>
            </Details>
          ) : null}
        </>
      )}
    </section>
  );
}

/* ---------- setting up and running ---------- */

function SetUpButton({ d }: { d: AbilityShowOut }) {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const v = d.ability;
  if (!v.facts.programs.length && !v.facts.keys.length && !(v.facts.files ?? []).length && !v.facts.local) return null;
  const onClick = async () => {
    const out = await dialog.open<string>((close) => <SetUpForm d={d} onDone={close} />, { title: `Set up “${v.title}” on this computer`, size: 'md' });
    if (!out) return;
    for (const k of QUERIES) void qc.invalidateQueries({ queryKey: [k] });
    toast({ tone: 'success', title: out });
  };
  return (
    <Button variant={d.here.ready ? 'ghost' : 'secondary'} icon={<Wrench size={14} />} onClick={() => void onClick()} data-setup>
      {d.here.setUp ? 'Set up again' : 'Set up on this computer'}
    </Button>
  );
}

const field =
  'w-full rounded-[10px] bg-panel px-3 text-[13.5px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0 disabled:opacity-60';

/** The program listening on an ability's local service now (D284), in full: what an owner agrees to. */
function ListeningNow({ l }: { l?: HereLocal | null }) {
  if (!l) return null;
  const p = l.now;
  return (
    <div className="space-y-1 text-[13px]" data-listening-now>
      {p ? (
        <p>
          Listening on port {l.port} now: <Theirs>{p.name}</Theirs> (<Theirs>{p.path}</Theirs>)
          {p.interpreter ? (
            <>
              , started in <Theirs>{p.cwd || 'a folder MetaRush can’t name'}</Theirs>, running <Theirs>{p.script || 'code MetaRush can’t name'}</Theirs>
            </>
          ) : null}
          .
        </p>
      ) : null}
      {l.agreed && !l.same ? (
        <p className="text-muted" data-agreed>
          An owner agreed to <Theirs>{l.agreed.name}</Theirs> (<Theirs>{l.agreed.path}</Theirs>)
          {l.agreed.interpreter ? (
            <>
              , started in <Theirs>{l.agreed.cwd || '—'}</Theirs>, running <Theirs>{l.agreed.script || '—'}</Theirs>
            </>
          ) : null}
          .
        </p>
      ) : null}
      {l.problem ? <p className="text-danger">{l.problem}</p> : null}
    </div>
  );
}

function SetUpForm({ d, onDone }: { d: AbilityShowOut; onDone: (message?: string) => void }) {
  const owner = useOwnership().owner;
  const [keys, setKeys] = useState<Record<string, string>>({});
  const [files, setFiles] = useState<Record<string, string>>({});
  // Agree on this computer to the program listening on its local service now (owners, D284).
  const [agreeLocal, setAgreeLocal] = useState(false);
  // What each file given really is, from the server (D285): looked at before it is set up.
  const [looked, setLooked] = useState<{ for: string; files: AbilityFileLook[] } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const given = Object.fromEntries(Object.entries(keys).filter(([, v]) => v.trim()));
      const paths = Object.fromEntries(Object.entries(files).filter(([, v]) => v.trim()));
      const sig = JSON.stringify(paths);
      if (Object.keys(paths).length && looked?.for !== sig) {
        const look = await call('ability.setup', { ref: d.ability.name, files: paths, look: true });
        setLooked({ for: sig, files: look.files ?? [] });
        return;
      }
      const out = await call('ability.setup', {
        ref: d.ability.name,
        ...(Object.keys(given).length ? { keys: given } : {}),
        ...(Object.keys(paths).length ? { files: paths } : {}),
        ...(agreeLocal && d.local?.now ? { agreeLocal: true, localProgram: d.local.now } : {}),
      });
      onDone(out.message);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="space-y-4 text-[13px]" data-setup-form>
      {d.here.programs.length ? (
        <div>
          <p className="font-medium">Programs it runs</p>
          <p className="mt-0.5 text-muted">MetaRush notes each program's fingerprint now; if a program changes later (an update), a person sets the ability up again.</p>
          <ul className="mt-1.5 space-y-0.5">
            {d.here.programs.map((p) => (
              <li key={p.name}>{p.says}</li>
            ))}
          </ul>
        </div>
      ) : null}
      {d.local ? (
        <div data-setup-local>
          <p className="font-medium">
            <Theirs>{d.local.service}</Theirs> on this computer
          </p>
          <ListeningNow l={d.local} />
          {d.local.now && !d.local.problem && !d.local.same ? (
            <label className="mt-1.5 flex items-start gap-2">
              <input
                type="checkbox"
                checked={agreeLocal}
                disabled={!owner || busy}
                onChange={(e) => setAgreeLocal(e.target.checked)}
                className="mt-0.5 accent-[var(--accent)]"
                data-agree-local
              />
              <span>Agree, on this computer, to the program listening now (owners). Setting it up otherwise keeps what was agreed.</span>
            </label>
          ) : null}
          {!owner && d.local.now && !d.local.same ? <OwnersNote className="mt-2" /> : null}
        </div>
      ) : null}
      {d.here.files.length ? (
        <div>
          <p className="font-medium">Files it reads</p>
          <p className="mt-0.5 text-muted">Give each file's full path on this computer. MetaRush notes its fingerprint now and copies it into each run, read-only; the ability never reads it in place.</p>
          {!owner ? <OwnersNote className="mt-2" /> : null}
          <div className="mt-2 space-y-2">
            {d.here.files.map((f) => (
              <label key={f.name} className="block">
                <span className="text-[12.5px] text-muted">
                  It asks for: <Theirs>{f.says}</Theirs>
                </span>
                {f.setUp ? (
                  <span className="block text-[12px] text-subtle" data-file-now={f.name}>
                    Set up now: {f.path} — {f.about || 'a file'}. Type another path to replace it.
                  </span>
                ) : null}
                <input
                  type="text"
                  autoComplete="off"
                  spellCheck={false}
                  disabled={!owner || busy}
                  placeholder="/full/path/to/the/file"
                  value={files[f.name] ?? ''}
                  onChange={(e) => setFiles({ ...files, [f.name]: e.target.value })}
                  className={cx(field, 'mt-1 h-9')}
                  data-file={f.name}
                />
              </label>
            ))}
          </div>
          {looked?.files.length ? (
            <div className="mt-3 rounded-[10px] bg-hover/50 p-3 hairline" data-files-looked>
              <p className="font-medium">What you are giving it</p>
              <ul className="mt-1 space-y-1">
                {looked.files.map((f) => (
                  <li key={f.name}>
                    {f.path}: {f.about}, {bytes(f.size)}.
                  </li>
                ))}
              </ul>
              <p className="mt-1 text-subtle">These go into every run of this ability on this computer.</p>
            </div>
          ) : null}
        </div>
      ) : null}
      {d.here.keys.length ? (
        <div>
          <p className="font-medium">Keys</p>
          <p className="mt-0.5 text-muted">Kept in this computer's keychain. The ability's code never sees them: MetaRush adds them on the way out, only for the places named.</p>
          {!owner ? <OwnersNote className="mt-2" /> : null}
          <div className="mt-2 space-y-2">
            {d.here.keys.map((k) => (
              <label key={k.name} className="block">
                <span className="text-[12.5px] text-muted">
                  The <Theirs>{k.name}</Theirs> key, for {k.hosts.join(', ')}
                  {k.configured ? ` (on this computer: ${k.masked}; type a new one to replace it)` : ''}
                </span>
                <input
                  type="password"
                  autoComplete="off"
                  disabled={!owner || busy}
                  value={keys[k.name] ?? ''}
                  onChange={(e) => setKeys({ ...keys, [k.name]: e.target.value })}
                  className={cx(field, 'mt-1 h-9')}
                  data-key={k.name}
                />
              </label>
            ))}
          </div>
        </div>
      ) : null}
      {error ? <ErrorPanel error={error} compact /> : null}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={() => onDone()} disabled={busy}>
          Cancel
        </Button>
        <Button variant="primary" onClick={() => void submit()} loading={busy} data-setup-submit>
          {Object.values(files).some((v) => v.trim()) && looked?.for !== JSON.stringify(Object.fromEntries(Object.entries(files).filter(([, v]) => v.trim())))
            ? 'Look at the files'
            : 'Set it up'}
        </Button>
      </div>
    </div>
  );
}

function RunButton({ d }: { d: AbilityShowOut }) {
  const dialog = useDialog();
  const toast = useToast();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const v = d.ability;
  const onClick = async () => {
    const out = await dialog.open<AbilityRunOut>((close) => <RunForm d={d} onDone={close} />, { title: `Run “${v.title}”`, size: 'lg' });
    if (!out) return;
    for (const k of QUERIES) void qc.invalidateQueries({ queryKey: [k] });
    toast({ tone: 'success', title: out.message });
    navigate(abilitiesLink({ ability: v.name, run: out.id }));
  };
  return (
    <Button variant="primary" icon={<Play size={14} weight="fill" />} disabled={!d.here.ready} title={d.here.ready ? undefined : d.here.problems.join(' ')} onClick={() => void onClick()} data-run-button>
      Run…
    </Button>
  );
}

function InputField({ input, value, onChange, disabled }: { input: AbilityInput; value: string; onChange: (v: string) => void; disabled: boolean }) {
  const id = `ability-input-${input.name}`;
  const label = (
    <label htmlFor={id} className="text-[12.5px] text-muted">
      {input.says ? <Theirs>{input.says}</Theirs> : input.name} <span className="text-subtle">({kindWord(input.kind)}{input.optional ? ', optional' : ''})</span>
    </label>
  );
  if (input.kind === 'choice') {
    return (
      <div>
        {label}
        <select id={id} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)} className={cx(field, 'mt-1 h-9')}>
          <option value="">Choose…</option>
          {(input.options ?? []).map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </select>
      </div>
    );
  }
  if (input.kind === 'yes-no') {
    return (
      <label className="flex items-center gap-2 text-[13px]">
        <input type="checkbox" checked={value === 'yes'} disabled={disabled} onChange={(e) => onChange(e.target.checked ? 'yes' : 'no')} className="accent-[var(--accent)]" />
        {input.says ? <Theirs>{input.says}</Theirs> : input.name}
      </label>
    );
  }
  const placeholder = input.kind === 'footage' || input.kind === 'picture' || input.kind === 'sound' ? "The clip's or picture's title" : input.default !== undefined ? String(input.default) : '';
  return (
    <div>
      {label}
      <input
        id={id}
        type={input.kind === 'number' ? 'number' : 'text'}
        value={value}
        disabled={disabled}
        min={input.min ?? undefined}
        max={input.kind === 'number' ? (input.max ?? undefined) : undefined}
        maxLength={input.kind === 'words' && input.max ? input.max : undefined}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        className={cx(field, 'mt-1 h-9')}
        data-input={input.name}
      />
    </div>
  );
}

function RunForm({ d, onDone }: { d: AbilityShowOut; onDone: (out?: AbilityRunOut) => void }) {
  const v = d.ability;
  const [values, setValues] = useState<Record<string, string>>({});
  const [plan, setPlan] = useState<AbilityPlanOut | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const inputs = Object.fromEntries(Object.entries(values).filter(([, x]) => x !== ''));
  const ref = `${v.name}@${v.hash}`;
  const look = async () => {
    setBusy(true);
    setError(null);
    try {
      setPlan(await call('ability.plan', { ref, inputs }));
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const start = async () => {
    if (!plan) return;
    setBusy(true);
    setError(null);
    try {
      onDone(await call('ability.run', { ref, inputs, plan: plan.fingerprint, ...(plan.estimate.usd > 0 ? { maxUsd: plan.estimate.usd } : {}) }));
    } catch (e) {
      setError(e);
      setPlan(null); // the plan changed: look at it again
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="space-y-4" data-run-form>
      <div className="space-y-3">
        {v.facts.inputs.map((i) => (
          <InputField
            key={i.name}
            input={i}
            value={values[i.name] ?? ''}
            disabled={busy}
            onChange={(x) => {
              setValues({ ...values, [i.name]: x });
              setPlan(null);
            }}
          />
        ))}
        {v.facts.inputs.length === 0 ? <p className="text-[13px] text-muted">It takes nothing.</p> : null}
      </div>
      {plan ? (
        <div className="space-y-2 rounded-[12px] bg-hover/50 p-3 text-[13px] hairline" data-plan>
          <p className="font-medium">{plan.message}</p>
          {plan.localWords ? (
            <div data-local-program>
              <p>{plan.localWords}</p>
              {plan.localProgram?.path ? (
                <Details label="Where the program is">
                  <p className="font-mono text-[12px]">{plan.localProgram.path}</p>
                </Details>
              ) : null}
            </div>
          ) : null}
          <p>{plan.leaves}</p>
          {plan.terms ? (
            <p className="font-medium" data-terms>
              The service's terms: <Theirs>{plan.terms}</Theirs>
            </p>
          ) : null}
          <p className="text-muted">
            {plan.makes} {plan.where}
          </p>
          {plan.estimate.usd > 0 ? <CostEstimate estimate={plan.estimate} defaultOpen /> : <p className="text-muted">It costs nothing.</p>}
          {plan.problems.length ? (
            <ul className="list-disc pl-5 text-danger">
              {plan.problems.map((p, i) => (
                <li key={i}>{p}</li>
              ))}
            </ul>
          ) : (
            <p className="text-muted">{plan.decidesWords}</p>
          )}
        </div>
      ) : null}
      {error ? <ErrorPanel error={error} compact /> : null}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={() => onDone()} disabled={busy}>
          Cancel
        </Button>
        {plan && plan.ready ? (
          <Button variant="primary" icon={<Play size={14} weight="fill" />} onClick={() => void start()} loading={busy} data-start>
            {plan.estimate.usd > 0 ? `Run, for about ${money(plan.estimate.usd)}` : 'Run'}
          </Button>
        ) : (
          <Button variant="primary" onClick={() => void look()} loading={busy} data-look>
            Look at what it will do
          </Button>
        )}
      </div>
    </div>
  );
}
