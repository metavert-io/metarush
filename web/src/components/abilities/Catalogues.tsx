import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, ArrowSquareOut, Books, CheckCircle, DownloadSimple, Info, MagnifyingGlass, Plus, Trash, Warning } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { AbilityCatalogueAddOut, AbilityFromCatalogue, AbilityImportOut, CatalogueRow, ListedAbility } from '../../api/types';
import { inputClass, TextField } from '../PasswordFields';
import { OwnerOnly, useOwnership } from '../OwnerOnly';
import { absolute, plural, stamp } from '../../lib/format';
import { useDialog } from '../../ui/Dialog';
import { useToast } from '../../ui/Toast';
import { Badge, Button, cx, Details, EmptyState, ErrorPanel, SectionTitle, Skeleton, Theirs } from '../../ui/primitives';

/*
 * Community catalogues of abilities (D481–D486). A catalogue is a public list
 * of abilities — an index served over HTTPS — that an owner trusts under
 * Settings → Abilities. Anyone browses what the trusted ones list in Library
 * → Abilities → Browse catalogues: what each does and needs, its publisher
 * and licence, whether it is here already. What a catalogue says is its own
 * word, shown as theirs (<Theirs>) and never checked by MetaRush; a person
 * brings an ability in (a look first, then a styled question), and it comes in
 * as a draft: checked on a computer here, then approved by an owner. The
 * dashboard shows what the server says; it reads no catalogue itself.
 */

const QUERIES = ['ability.list', 'ability.show', 'ability.browse', 'ability.catalogues', 'catalogue.list'];

/** Library → Abilities → Browse catalogues (one catalogue, one listing). */
export function browseLink(o: { catalogue?: string; listing?: string } = {}): string {
  const p = new URLSearchParams({ browse: o.catalogue || '1' });
  if (o.listing) p.set('listing', o.listing);
  return `/library/abilities?${p.toString()}`;
}

function abilityLink(name: string, version?: string): string {
  const p = new URLSearchParams({ ability: name });
  if (version) p.set('version', version);
  return `/library/abilities?${p.toString()}`;
}

/** The one note every listing carries: a catalogue's word, not MetaRush's check. */
export function ListedNote({ note, className }: { note: string; className?: string }) {
  return (
    <p className={cx('flex items-start gap-2 rounded-[10px] bg-hover/60 px-3 py-2 text-[12.5px] leading-relaxed text-muted hairline', className)} data-listed-note>
      <Info size={14} className="mt-[3px] shrink-0 text-subtle" aria-hidden />
      <span>{note}</span>
    </p>
  );
}

/** Library → Abilities → Browse catalogues. */
export function BrowseCatalogues({ catalogue, listing }: { catalogue?: string; listing?: string }) {
  const [search, setSearch] = useState('');
  const [typed, setTyped] = useState('');
  const [reading, setReading] = useState(false);
  const input = useMemo(() => ({ catalogue: catalogue || undefined, search: search || undefined }), [catalogue, search]);
  const b = useOp('ability.browse', input, { staleTime: 30_000 });
  const qc = useQueryClient();
  const toast = useToast();
  const d = b.data;
  const readAgain = async () => {
    setReading(true);
    try {
      await call('ability.browse', { catalogue: catalogue || undefined, refresh: true, limit: 1 });
      await qc.invalidateQueries({ queryKey: ['ability.browse'] });
      void qc.invalidateQueries({ queryKey: ['ability.catalogues'] });
    } catch (e) {
      toast({ tone: 'error', title: 'MetaRush couldn’t read the catalogues', body: toApiError(e).message });
    } finally {
      setReading(false);
    }
  };
  useEffect(() => {
    if (!listing || !d) return;
    const t = setTimeout(() => document.querySelector(`[data-listing="${CSS.escape(listing)}"]`)?.scrollIntoView({ behavior: 'smooth', block: 'center' }), 150);
    return () => clearTimeout(t);
  }, [listing, d]);
  const problems = (d?.catalogues ?? []).filter((c) => c.problem);
  return (
    <div data-browse-catalogues>
      <Link to="/library/abilities" className="mb-4 inline-flex items-center gap-1.5 text-[12.5px] text-muted hover:text-fg">
        <ArrowLeft size={13} /> All abilities
      </Link>
      <header className="mb-4 flex flex-col gap-2 sm:flex-row sm:items-end sm:gap-3">
        <div className="min-w-0 flex-1">
          <h2 className="text-[22px] font-semibold tracking-[-0.02em]">Browse catalogues</h2>
          <p className="mt-1 max-w-[70ch] text-[13.5px] leading-relaxed text-muted">
            Abilities the community catalogues this workspace trusts list: what each does and needs, who published it, and whether it is here already.
          </p>
        </div>
        <Link to="/settings/abilities" className="text-[12.5px] text-accent-text hover:underline">
          Catalogues this workspace trusts
        </Link>
      </header>
      {d ? <ListedNote note={d.note} className="mb-4" /> : null}
      <form
        className="mb-5 flex flex-wrap items-center gap-2"
        role="search"
        onSubmit={(e) => {
          e.preventDefault();
          setSearch(typed.trim());
        }}
      >
        <label className="relative min-w-[220px] flex-1 sm:max-w-[420px]">
          <span className="sr-only">Find an ability</span>
          <MagnifyingGlass size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-subtle" aria-hidden />
          <input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder="Find an ability: stock photos, captions…" className={cx(inputClass, 'pl-8')} data-browse-search />
        </label>
        <Button type="submit" size="md">
          Find
        </Button>
        <Button variant="ghost" size="md" loading={reading} onClick={() => void readAgain()} data-browse-refresh title="Read the catalogues again now">
          Read again
        </Button>
        {catalogue ? (
          <Link to={browseLink()} className="text-[12.5px] text-muted hover:text-fg">
            Every catalogue
          </Link>
        ) : null}
      </form>
      {b.isError ? (
        <ErrorPanel error={b.error} onRetry={() => void b.refetch()} />
      ) : !d ? (
        <div className="grid gap-3 md:grid-cols-2">
          <Skeleton className="h-48" />
          <Skeleton className="h-48" />
        </div>
      ) : d.catalogues.length === 0 ? (
        <div data-browse-empty>
          <EmptyState
            icon={<Books size={22} />}
            title="No catalogue yet"
            body="This workspace trusts no catalogue of abilities yet. An owner adds one under Settings → Abilities; then what it lists shows here."
            action={
              <Link to="/settings/abilities" className="text-[13px] font-medium text-accent-text hover:underline">
                Settings → Abilities
              </Link>
            }
          />
        </div>
      ) : (
        <>
          {problems.map((c) => (
            <p key={c.key} className="mb-3 flex items-start gap-2 text-[12.5px] text-muted" data-catalogue-problem={c.key}>
              <Warning size={14} className="mt-[3px] shrink-0 text-accent-text" aria-hidden />
              <span>
                <Theirs>{c.title || c.key}</Theirs>: {c.problem} Its copy before stays in use.
              </span>
            </p>
          ))}
          <p className="mb-3 text-[13px] text-muted" data-browse-summary>
            {d.message}
          </p>
          {d.abilities.length === 0 ? null : (
            <ul className="grid gap-3 md:grid-cols-2" data-listings>
              {d.abilities.map((l) => (
                <ListedCard key={l.ref} l={l} highlight={l.entry.name === listing} />
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  );
}

/** One ability a catalogue lists: its words (theirs) and what they mean, in MetaRush's words. */
function ListedCard({ l, highlight }: { l: ListedAbility; highlight?: boolean }) {
  const e = l.entry;
  return (
    <li
      className={cx('flex h-full flex-col gap-3 rounded-[14px] bg-panel p-4 hairline', highlight && 'ring-2 ring-accent-line')}
      data-listing={e.name}
      data-catalogue={l.catalogue}
      data-here={l.here ? 'yes' : 'no'}
    >
      <div className="flex items-start gap-3">
        <span className="mt-0.5 text-accent-text" aria-hidden>
          <Books size={18} weight="duotone" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-[15px] font-semibold tracking-[-0.01em]">
              <Theirs>{e.title}</Theirs>
            </h3>
            {l.here ? (
              <Link to={l.here.link} className="inline-flex" data-listing-here>
                <Badge tone="ok" icon={<CheckCircle size={12} weight="fill" />}>
                  Here: version {l.here.number}, {l.here.stateLabel}
                </Badge>
              </Link>
            ) : l.other ? (
              <Badge tone="info">Another version is here</Badge>
            ) : null}
            {l.revoked ? <Badge tone="danger">Taken back</Badge> : null}
          </div>
          <p className="mt-0.5 text-[12px] text-subtle">
            In <Theirs>{l.catalogueTitle}</Theirs>
            {l.publisher ? (
              <>
                {' '}
                · listed by <Theirs>{l.publisher}</Theirs>
              </>
            ) : null}
          </p>
          <p className="mt-1.5 text-[13px] leading-relaxed text-muted">
            <Theirs>{e.does}</Theirs>
          </p>
        </div>
      </div>
      <dl className="grid gap-1.5 text-[12.5px] leading-relaxed">
        <ListedRow label="What it reaches">{l.reaches}</ListedRow>
        <ListedRow label="What leaves">{l.leaves}</ListedRow>
        <ListedRow label="What it brings back">
          {l.makes} {l.aiWords}
        </ListedRow>
        <ListedRow label="Each computer needs">
          <ul className="space-y-0.5">
            {l.needs.map((n, i) => (
              <li key={i}>{n}</li>
            ))}
          </ul>
        </ListedRow>
        <ListedRow label="What it costs">{l.costWords}</ListedRow>
        <ListedRow label="Licence">
          <Theirs>{e.licence}</Theirs>
        </ListedRow>
        <ListedRow label="Who starts a run">{l.decides}</ListedRow>
      </dl>
      {l.warnings.map((w, i) => (
        <p key={i} className="flex items-start gap-1.5 text-[12px] text-accent-text">
          <Warning size={13} className="mt-[2px] shrink-0" aria-hidden />
          {w}
        </p>
      ))}
      {l.revoked ? (
        <p className="text-[12px] text-danger">
          The catalogue took this version back: <Theirs>{l.revoked}</Theirs>
        </p>
      ) : null}
      {e.notes ? (
        <details className="text-[12.5px] text-muted">
          <summary className="cursor-pointer select-none text-subtle hover:text-fg">Its notes</summary>
          <p className="mt-1.5 whitespace-pre-wrap leading-relaxed">
            <Theirs>{e.notes}</Theirs>
          </p>
        </details>
      ) : null}
      <div className="mt-auto flex flex-wrap items-center gap-2 pt-1">
        <BringInButton l={l} />
        {l.sourcePage ? (
          <a href={l.sourcePage} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-[12.5px] text-muted hover:text-fg" data-listing-source>
            Read its files <ArrowSquareOut size={12} aria-hidden />
          </a>
        ) : null}
        {(e.links ?? []).map((k) => (
          <a key={k.url} href={k.url} target="_blank" rel="noopener noreferrer nofollow" className="inline-flex items-center gap-1 text-[12.5px] text-muted hover:text-fg">
            <Theirs>{k.title}</Theirs> <ArrowSquareOut size={12} aria-hidden />
          </a>
        ))}
      </div>
      <Details label="Details" compact>
        <p className="break-all">Its files: {l.sourceWords}</p>
        <p className="break-all">Version: {e.archive}</p>
        <code className="mt-1 block break-all">metarush ability import --from-catalogue {l.ref}</code>
      </Details>
    </li>
  );
}

function ListedRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[8.5rem_1fr] gap-2 max-sm:grid-cols-1 max-sm:gap-0.5">
      <dt className="text-subtle">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}

/** "Bring it in…": a look first (MetaRush fetches exactly the version listed and checks it is that version), then a styled question. */
function BringInButton({ l }: { l: ListedAbility }) {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [busy, setBusy] = useState(false);
  const from = `${l.catalogue}:${l.entry.name}@${l.entry.archive}`;
  const bring = async () => {
    setBusy(true);
    try {
      let asVersion = false;
      let dry: AbilityImportOut;
      try {
        dry = await call('ability.catalogues.import', { from, dryRun: true });
      } catch (e) {
        const err = toApiError(e);
        if (err.code !== 'E_CONFLICT' || !err.message.includes('already has an ability')) throw e;
        const same = await dialog.confirm({
          title: 'An ability of that name is here already',
          body: (
            <p>
              {err.message} {err.hint}
            </p>
          ),
          confirmLabel: 'Bring it in as a new version',
          tone: 'danger',
        });
        if (!same) return;
        asVersion = true;
        dry = await call('ability.catalogues.import', { from, dryRun: true, asVersion });
      }
      if (!dry.new) {
        toast({ tone: 'info', title: dry.message });
        navigate(abilityLink(dry.name, dry.version));
        return;
      }
      const ok = await dialog.confirm({ title: `Bring in “${dry.title}”?`, body: <BringInPreview out={dry} l={l} />, confirmLabel: 'Bring it in' });
      if (!ok) return;
      const out = await call('ability.catalogues.import', { from, asVersion: asVersion || undefined });
      for (const k of QUERIES) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: out.message, body: 'Its page says how to check it on this computer; then an owner approves it.' });
      navigate(abilityLink(out.name, out.version));
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'It didn’t come in', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(false);
    }
  };
  if (!l.canBringIn) {
    return l.whyNot ? <span className="text-[12px] text-subtle">{l.whyNot}</span> : null;
  }
  return (
    <Button size="sm" variant="primary" icon={<DownloadSimple size={13} />} loading={busy} onClick={() => void bring()} data-bring-in={l.entry.name}
      title="MetaRush fetches exactly the version listed; it comes in as a draft, checked here and approved by an owner before anyone runs it">
      {busy ? 'Fetching it…' : 'Bring it in…'}
    </Button>
  );
}

/** What bringing it in does, from what MetaRush fetched and checked (the dry run). */
function BringInPreview({ out, l }: { out: AbilityImportOut; l: ListedAbility }) {
  return (
    <div className="space-y-2 text-[13px]" data-bring-in-preview>
      <p>
        MetaRush fetched exactly the version <Theirs>{l.catalogueTitle}</Theirs> lists and found it is that version, saying what the listing says. It comes in as{' '}
        {out.of ? 'a new version of the ability here' : 'a new ability'}: {plural(out.files.length, 'file')}, never approved or set up here.
      </p>
      <p className="text-muted">What leaves this computer when it runs: {out.leaves}</p>
      {out.lineage ? <p className="text-[12.5px] text-muted">{out.lineage}</p> : null}
      <p className="text-[12.5px] text-subtle">
        Listed, not checked by MetaRush: it is checked on a computer here in the sandbox with the network off, and an owner reads what it may do and approves it, before anyone runs it.
      </p>
    </div>
  );
}

/** Where a version brought in from a catalogue came from (the ability's page). */
export function CatalogueOrigin({ c, at }: { c: AbilityFromCatalogue; at?: string }) {
  return (
    <div className="mt-3 space-y-1 text-[12.5px] text-muted" data-catalogue-origin>
      <p>
        From the catalogue <Theirs>{c.title}</Theirs> (its version {c.version}
        {c.date ? `, ${c.date}` : ''})
        {c.publisher ? (
          <>
            , listed by <Theirs>{c.publisher}</Theirs>
          </>
        ) : null}
        {at ? (
          <>
            , brought in <span title={absolute(at)}>{stamp(at)}</span>
          </>
        ) : null}
        .
      </p>
      <p className="text-subtle">What a catalogue says is its own word: MetaRush fetched exactly the version it lists, and nothing there approves it here.</p>
      <Details label="Where its files came from">
        {c.repository ? (
          <p className="break-all">
            {c.repository} at {c.commit}
            {c.path ? `, folder ${c.path}` : ''}
          </p>
        ) : (
          <p className="break-all">
            {c.fileUrl} ({c.fileSha256})
          </p>
        )}
        <p className="break-all">{c.url}</p>
      </Details>
    </div>
  );
}

/* ---------- Settings → Abilities ---------- */

/** Settings → Abilities: the catalogues this workspace trusts (owners add and remove them). */
export function AbilityCataloguesSettings() {
  const list = useOp('ability.catalogues', {});
  const d = list.data;
  return (
    <div className="space-y-6" data-catalogues-settings>
      <section className="panel p-5" aria-labelledby="trusted-catalogues">
        <SectionTitle id="trusted-catalogues" meta={d ? plural(d.catalogues.length, 'catalogue') : undefined} action={<Link to={browseLink()} className="text-[12.5px] text-accent-text hover:underline">Browse them</Link>}>
          Catalogues this workspace trusts
        </SectionTitle>
        {list.isError ? (
          <ErrorPanel error={list.error} onRetry={() => void list.refetch()} />
        ) : !d ? (
          <Skeleton className="h-24" />
        ) : (
          <>
            <ListedNote note={d.note} className="mb-4" />
            {d.catalogues.length === 0 ? (
              <p className="text-[13px] text-muted" data-no-catalogues>
                None yet: MetaRush trusts no catalogue by itself. An owner adds one below.
              </p>
            ) : (
              <ul className="divide-y divide-line" data-catalogue-list>
                {d.catalogues.map((c) => (
                  <CatalogueItem key={c.key} c={c} />
                ))}
              </ul>
            )}
          </>
        )}
      </section>
      <section className="panel p-5" aria-labelledby="add-catalogue">
        <SectionTitle id="add-catalogue">Add a catalogue</SectionTitle>
        <OwnerOnly note>
          <AddCatalogue suggested={(d?.suggested ?? []).filter((s) => !s.trusted).map((s) => s.url)} />
        </OwnerOnly>
      </section>
    </div>
  );
}

function CatalogueItem({ c }: { c: CatalogueRow }) {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const o = useOwnership();
  const [busy, setBusy] = useState<'' | 'remove' | 'read'>('');
  const remove = async () => {
    const ok = await dialog.confirm({
      title: `Stop trusting “${c.title || c.key}”?`,
      body: <p>Nothing more comes in from it. Abilities already brought in from it stay, each with its own approval.</p>,
      confirmLabel: 'Stop trusting it',
      tone: 'danger',
    });
    if (!ok) return;
    setBusy('remove');
    try {
      const out = await call('ability.catalogues.remove', { catalogue: c.key });
      for (const k of QUERIES) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: out.message });
    } catch (e) {
      toast({ tone: 'error', title: 'That did not work', body: toApiError(e).message });
    } finally {
      setBusy('');
    }
  };
  const readAgain = async () => {
    setBusy('read');
    try {
      await call('ability.browse', { catalogue: c.key, refresh: true, limit: 1 });
      for (const k of ['ability.catalogues', 'ability.browse']) void qc.invalidateQueries({ queryKey: [k] });
    } catch (e) {
      toast({ tone: 'error', title: 'MetaRush couldn’t read it', body: toApiError(e).message });
    } finally {
      setBusy('');
    }
  };
  return (
    <li className="flex flex-col gap-2 py-3 sm:flex-row sm:items-start sm:gap-3" data-catalogue-item={c.key}>
      <div className="flex min-w-0 flex-1 items-start gap-3">
        <span className="mt-0.5 text-accent-text" aria-hidden>
          <Books size={18} weight="duotone" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="text-[14px] font-medium">
            <Theirs>{c.title || c.key}</Theirs> <span className="text-[12px] font-normal text-subtle">· called {c.key} here</span>
          </p>
          {c.about ? (
            <p className="text-[12.5px] text-muted">
              <Theirs>{c.about}</Theirs>
            </p>
          ) : null}
          <p className="mt-0.5 text-[12px] text-subtle">
            {plural(c.abilities, 'ability', 'abilities')} listed
            {c.maintainer ? (
              <>
                {' '}
                · kept by <Theirs>{c.maintainer}</Theirs>
              </>
            ) : null}
            {c.fetchedAt ? (
              <>
                {' '}
                · read <span title={absolute(c.fetchedAt)}>{stamp(c.fetchedAt)}</span>
              </>
            ) : null}{' '}
            · added by <Theirs>{c.addedBy}</Theirs>
          </p>
          {c.problem ? <p className="mt-1 text-[12px] text-accent-text">The last time MetaRush asked: {c.problem} Its copy before stays in use.</p> : null}
          {c.leftOut.length > 0 ? (
            <details className="mt-1 text-[12px] text-muted">
              <summary className="cursor-pointer select-none text-subtle hover:text-fg">MetaRush left {plural(c.leftOut.length, 'listing')} out</summary>
              <ul className="mt-1 space-y-0.5">
                {c.leftOut.map((x, i) => (
                  <li key={i}>
                    <Theirs>{x.name}</Theirs>: {x.why}
                  </li>
                ))}
              </ul>
            </details>
          ) : null}
          <Details label="Details" compact>
            <p className="break-all">{c.url}</p>
            <p>
              Its version {c.version}
              {c.date ? `, ${c.date}` : ''}
            </p>
          </Details>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2 pl-[30px] sm:pl-0">
        <Link to={browseLink({ catalogue: c.key })} className="text-[12.5px] text-accent-text hover:underline">
          Browse
        </Link>
        <Button size="sm" variant="ghost" loading={busy === 'read'} disabled={!!busy} onClick={() => void readAgain()} data-catalogue-read>
          Read again
        </Button>
        <Button size="sm" variant="ghost" icon={<Trash size={13} />} loading={busy === 'remove'} disabled={!!busy || !o.owner} onClick={() => void remove()} data-catalogue-remove
          title={o.owner ? 'Stop trusting it' : 'Only an owner can stop trusting a catalogue'}>
          Remove…
        </Button>
      </div>
    </li>
  );
}

/** Add a catalogue (owners): its address, a look at what it lists, then a styled question. */
function AddCatalogue({ suggested }: { suggested: string[] }) {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const [url, setUrl] = useState('');
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const add = async (address: string) => {
    setBusy(true);
    setError(null);
    try {
      const look: AbilityCatalogueAddOut = await call('ability.catalogues.add', { url: address.trim(), name: name.trim() || undefined, dryRun: true });
      const c = look.catalogue;
      const ok = await dialog.confirm({
        title: `Trust “${c.title || c.key}”?`,
        body: (
          <div className="space-y-2 text-[13px]" data-add-preview>
            <p>
              It lists {plural(c.abilities, 'ability', 'abilities')}
              {c.maintainer ? (
                <>
                  {' '}
                  and says it is kept by <Theirs>{c.maintainer}</Theirs>
                </>
              ) : null}
              . It is called {c.key} here.
            </p>
            {c.about ? (
              <p className="text-muted">
                <Theirs>{c.about}</Theirs>
              </p>
            ) : null}
            {c.leftOut.length ? <p className="text-muted">MetaRush leaves {plural(c.leftOut.length, 'listing')} of it out (it says why once it is added).</p> : null}
            <p className="text-subtle">{look.note}</p>
          </div>
        ),
        confirmLabel: 'Trust it',
      });
      if (!ok) return;
      const out = await call('ability.catalogues.add', { url: address.trim(), name: name.trim() || undefined });
      for (const k of QUERIES) void qc.invalidateQueries({ queryKey: [k] });
      toast({ tone: 'success', title: out.message });
      setUrl('');
      setName('');
    } catch (e) {
      const err = toApiError(e);
      setError([err.message, err.hint].filter(Boolean).join(' '));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="space-y-4">
      <p className="max-w-[72ch] text-[13px] leading-relaxed text-muted">
        A catalogue is a list of abilities someone publishes, read over a secure connection (an address that starts with https://). For a list kept on GitHub, give its raw address.
        Trusting one lets people here browse it and bring abilities in from it; each one is checked here and approved by an owner first.
      </p>
      <form
        className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_200px_auto] sm:items-end"
        onSubmit={(e) => {
          e.preventDefault();
          if (url.trim()) void add(url);
        }}
        data-add-catalogue
      >
        <TextField label="Its address" value={url} onChange={setUrl} placeholder="https://…" inputMode="url" autoComplete="off" spellCheck={false} data-catalogue-url />
        <TextField label="Short name here (optional)" value={name} onChange={setName} placeholder="its own" autoComplete="off" spellCheck={false} />
        <Button type="submit" variant="primary" icon={<Plus size={13} />} loading={busy} disabled={!url.trim()}>
          Add…
        </Button>
      </form>
      {error ? (
        <p className="text-[12.5px] text-danger" role="alert" data-add-error>
          {error}
        </p>
      ) : null}
      {suggested.length > 0 ? (
        <div data-suggested-catalogues>
          <p className="mb-1.5 text-[12.5px] font-medium text-muted">Suggested on this computer</p>
          <ul className="space-y-1.5">
            {suggested.map((s) => (
              <li key={s} className="flex flex-wrap items-center gap-2 text-[12.5px]">
                <span className="min-w-0 break-all text-muted">{s}</span>
                <Button size="sm" variant="secondary" disabled={busy} onClick={() => void add(s)}>
                  Add…
                </Button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
