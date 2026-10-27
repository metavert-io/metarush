import { useState } from 'react';
import { ArrowCircleUp, CaretRight, Desktop, Info, Sparkle } from '@phosphor-icons/react';
import { useOp } from '../../api/queries';
import type { ComputerVersion, ReleaseNotes, VersionShowOut } from '../../api/types';
import { Markdown } from '../../layout/assistant/Markdown';
import { absolute, platformLabel, plural, relative } from '../../lib/format';
import { useHost, type Host } from '../../lib/host';
import { unwrapMarkdown, versionDay } from '../../lib/version';
import { Badge, Button, cx, Details, ErrorPanel, Freshness, Skeleton, Theirs } from '../../ui/primitives';

/*
 * Settings → General (About MetaRush) (D130): the version that answers (the computer
 * that runs MetaRush, named when the browser is elsewhere), what's new in it
 * from the changelog built into it, and the version every computer in the
 * workspace runs, older ones flagged. `metarush version --details` says the
 * same; the exact build (commits past the release, commit) is in Details.
 */

/** How many of a section's changes show before "Show all". */
const FIRST = 6;

export function AboutMetaRush() {
  const q = useOp('version.show', {}, { staleTime: 60_000, refetchInterval: 60_000 });
  const host = useHost();
  const d = q.data;
  return (
    <section id="about" className="panel scroll-mt-28 p-5" aria-labelledby="about-h" data-about>
      <h2 id="about-h" className="mb-4 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <Info size={17} />
        </span>
        About MetaRush
        <Freshness className="ml-auto" at={q.dataUpdatedAt || undefined} fetching={q.isFetching} />
      </h2>
      {q.isLoading ? (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.15fr)]">
          <div className="space-y-2">
            <Skeleton className="h-9 w-48" />
            <Skeleton className="h-5 w-40" />
            <Skeleton className="mt-4 h-24" />
          </div>
          <Skeleton className="h-40" />
        </div>
      ) : q.error ? (
        <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} />
      ) : d ? (
        <div className="grid grid-cols-1 gap-x-8 gap-y-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.15fr)]">
          <div className="min-w-0">
            <ThisVersion d={d} host={host} />
            <Computers d={d} host={host} />
          </div>
          <div className="min-w-0 lg:border-l lg:border-line lg:pl-8">
            {d.whatsNew.length ? (
              d.whatsNew.map((s, i) => <WhatsNew key={s.heading} s={s} d={d} className={i > 0 ? 'mt-6' : undefined} />)
            ) : (
              <p className="text-[13px] text-muted">This version came without notes about what's new.</p>
            )}
          </div>
        </div>
      ) : null}
    </section>
  );
}

function ThisVersion({ d, host }: { d: VersionShowOut; host: Host }) {
  const day = versionDay(d);
  return (
    <div>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="text-[26px] font-semibold leading-tight tracking-[-0.03em]" data-about-version>
          MetaRush {d.release}
        </span>
        {day ? <span className="text-[13.5px] text-muted">{day}</span> : null}
      </div>
      {host.remote ? <p className="mt-1 text-[12.5px] text-muted">Running on {host.machine}, the computer this dashboard comes from.</p> : null}
      {d.dev ? (
        <p className="mt-2 inline-flex items-center gap-1.5 rounded-full bg-accent-soft px-2.5 py-1 text-[12px] text-accent-text">
          <Sparkle size={13} weight="fill" aria-hidden /> Includes changes that aren't in a release yet.
        </p>
      ) : null}
      <Details compact className="mt-2" label="Details">
        <div>
          Version <span className="tc text-muted">{d.version}</span>
          {d.devCount !== undefined ? <span>, {plural(d.devCount, 'commit')} after {d.release}</span> : d.dev ? <span>, built from source</span> : null}
        </div>
        <div>
          Commit <span className="tc text-muted">{d.commit}</span>, built <span className="text-muted">{absolute(d.date) || d.date}</span>
        </div>
        <div>
          Cut format <span className="tc text-muted">{d.editSpec}</span> · in a terminal: <span className="tc text-muted">metarush version --details</span>
        </div>
      </Details>
    </div>
  );
}

function Computers({ d, host }: { d: VersionShowOut; host: Host }) {
  const older = d.computers.filter((c) => c.standing === 'older');
  const newer = d.computers.filter((c) => c.standing === 'newer');
  return (
    <div className="mt-6">
      <h3 className="mb-2 text-[12px] font-medium uppercase tracking-[0.06em] text-subtle">Your computers</h3>
      {d.computers.length ? (
        <ul className="divide-y divide-line overflow-hidden rounded-[12px] hairline" data-about-computers>
          {d.computers.map((c) => (
            <ComputerRow key={c.id} c={c} host={host} />
          ))}
        </ul>
      ) : (
        <p className="text-[12.5px] text-subtle">No computers have said which MetaRush they run yet.</p>
      )}
      {older.length ? (
        <div className="mt-3 rounded-[10px] bg-accent-soft px-3 py-2 text-[12.5px] leading-relaxed text-accent-text" data-about-older>
          <p className="flex items-start gap-2">
            <ArrowCircleUp size={16} weight="fill" className="mt-[1px] shrink-0" aria-hidden />
            <span>
              {names(older)} {older.length === 1 ? 'runs' : 'run'} an older MetaRush than {host.here}. Update MetaRush there to get what's new.
            </span>
          </p>
          <Details compact className="mt-1.5 pl-6 text-accent-text" label="How to update it">
            <div>
              With Homebrew, on that computer: <span className="tc">brew upgrade --cask metarush</span>, then <span className="tc">metarush service restart</span>
            </div>
            <div>
              From a checkout: <span className="tc">scripts/deploy.sh --host &lt;its name&gt;</span> (docs/deployment.md)
            </div>
          </Details>
        </div>
      ) : null}
      {newer.length ? (
        <p className="mt-3 rounded-[10px] bg-info-soft px-3 py-2 text-[12.5px] leading-relaxed text-info">
          {names(newer)} already {newer.length === 1 ? 'runs' : 'run'} a newer MetaRush. Update {host.remote ? host.machine : 'this computer'} to get its changes.
        </p>
      ) : null}
      {d.computersError ? <p className="mt-2 text-[12px] text-subtle">{d.computersError}</p> : null}
    </div>
  );
}

function names(cs: ComputerVersion[]) {
  const shown = cs.slice(0, 3);
  return (
    <>
      {shown.map((c, i) => (
        <span key={c.id}>
          {i > 0 ? (i === shown.length - 1 && cs.length <= 3 ? ' and ' : ', ') : null}
          <Theirs className="font-medium">{c.name}</Theirs>
        </span>
      ))}
      {cs.length > 3 ? ` and ${plural(cs.length - 3, 'other')}` : null}
    </>
  );
}

const STANDING: Record<string, { label: string; tone: 'ok' | 'accent' | 'info' | 'neutral' }> = {
  same: { label: 'Same version', tone: 'ok' },
  older: { label: 'Older', tone: 'accent' },
  newer: { label: 'Newer', tone: 'info' },
  unknown: { label: 'Not known yet', tone: 'neutral' },
};

function ComputerRow({ c, host }: { c: ComputerVersion; host: Host }) {
  const st = STANDING[c.standing] ?? STANDING.unknown!;
  const what = c.state === 'invited' ? 'Invited, not joined yet' : c.release ? `MetaRush ${c.release}` : "Hasn't said which MetaRush it runs yet";
  return (
    <li className="flex items-start gap-3 px-3.5 py-3" data-about-computer={c.standing}>
      <span className="mt-[2px] shrink-0 text-subtle">
        <Desktop size={16} />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <Theirs className="min-w-0 truncate text-[13.5px] font-medium">{c.name}</Theirs>
          {c.this ? <Badge tone="accent">{host.remote ? 'runs this dashboard' : 'this computer'}</Badge> : null}
        </div>
        {/* One line of text: a wrap ends a line with "·", never starts one with it. */}
        <div className="mt-0.5 text-[12px] leading-relaxed text-muted">
          <span className={cx(c.standing === 'older' && 'font-medium text-fg')}>{what}</span>
          {c.os ? <span className="text-subtle">{` · ${platformLabel(c.os, c.arch ?? '')}`}</span> : null}
          {c.lastSeenAt && !c.this ? (
            <span className="text-subtle" title={absolute(c.lastSeenAt)}>
              {` · seen ${relative(c.lastSeenAt)}`}
            </span>
          ) : null}
        </div>
        {c.version ? (
          <Details compact className="mt-1" label="Details">
            <div>
              Version <span className="tc text-muted">{c.version}</span>
              {c.commit ? (
                <>
                  , commit <span className="tc text-muted">{c.commit}</span>
                </>
              ) : null}
              {c.built ? <span>, built {absolute(c.built) || c.built}</span> : null}
            </div>
            {c.hostname ? (
              <div>
                Host name <span className="tc text-muted">{c.hostname}</span>
              </div>
            ) : null}
          </Details>
        ) : null}
      </div>
      {!c.this ? (
        <Badge className="mt-[1px] shrink-0" tone={st.tone}>
          {st.label}
        </Badge>
      ) : null}
    </li>
  );
}

function WhatsNew({ s, d, className }: { s: ReleaseNotes; d: VersionShowOut; className?: string }) {
  const [all, setAll] = useState(false);
  const plain = s.entries.filter((e) => e.headline);
  const technical = s.entries.filter((e) => !e.headline);
  const shown = all ? plain : plain.slice(0, FIRST);
  const title = !s.release ? 'Coming in the next version' : s.release === d.release && !d.dev ? "What's new in this version" : `What's new in ${s.release}`;
  const day = s.date ? versionDay({ dev: false, releaseDate: s.date, date: '' }) : '';
  return (
    <div className={className} data-whats-new={s.release || 'next'}>
      <h3 className="flex flex-wrap items-baseline gap-x-2 text-[14px] font-semibold">
        {title}
        {day ? <span className="text-[12px] font-normal text-subtle">{day}</span> : null}
      </h3>
      {!s.release ? <p className="mt-0.5 text-[12px] text-subtle">Already here, before a release includes them.</p> : null}
      {shown.length ? (
        <ul className="mt-2.5 space-y-0.5">
          {shown.map((e, i) => (
            <li key={i} data-change>
              <details className="group/change rounded-[8px]">
                <summary className="flex cursor-pointer list-none items-start gap-2 rounded-[8px] px-2 py-1.5 text-[13px] leading-snug text-fg hover:bg-hover focus-visible:outline-2 focus-visible:outline-accent [&::-webkit-details-marker]:hidden">
                  <CaretRight size={11} className="mt-[4px] shrink-0 text-subtle transition-transform duration-200 group-open/change:rotate-90" aria-hidden />
                  <span>{e.headline}</span>
                </summary>
                <div className="mb-2 ml-[26px] mr-2 mt-0.5 border-l border-line pl-3">
                  <Markdown text={unwrapMarkdown(e.text)} className="text-[12.5px] leading-[1.6] text-muted" />
                </div>
              </details>
            </li>
          ))}
        </ul>
      ) : null}
      {plain.length > FIRST ? (
        <Button variant="quiet" size="sm" className="mt-1.5" onClick={() => setAll((v) => !v)} aria-expanded={all}>
          {all ? 'Show fewer' : `Show all ${plain.length} changes`}
        </Button>
      ) : null}
      {technical.length ? (
        <Details compact className="mt-2 px-2" label={`${plural(technical.length, 'more change')}, mostly for developers`}>
          <div className="space-y-2 pt-1">
            {technical.map((e, i) => (
              <Markdown key={i} text={unwrapMarkdown(e.text)} className="text-[12px] leading-[1.55] text-muted" />
            ))}
          </div>
        </Details>
      ) : null}
    </div>
  );
}
