import { useEffect, useState } from 'react';
import { Link } from 'react-router';
import { ArrowSquareOut, Info, MagicWand, Sparkle } from '@phosphor-icons/react';
import type { ops } from '@metarush/schema';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { AssetSummary } from '../../api/types';
import { plural } from '../../lib/format';
import { Badge, Button, cx, ErrorPanel, Skeleton, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';
import { FixesPanel, fixesBusy, useRefreshSources } from './FixesPanel';
import { SpokenLanguage } from './SpokenLanguage';

/*
 * The info card (D151): a source's descriptive name and its original file,
 * where it came from, what MetaRush found in it, where it's used and its
 * fixes (D152). One card per source, built in Go (asset.card) so the
 * dashboard, a terminal (metarush asset card) and Claude see the same.
 */

/** Takes MetaRush's better name for a clip (asset.update suggested). */
function useTakeName() {
  const toast = useToast();
  const refresh = useRefreshSources();
  return async (id: string, title: string) => {
    try {
      await call('asset.update', { id, suggested: true });
      refresh();
      toast({ tone: 'success', title: `It's called “${title}” now`, body: 'Its file name stays on its info card.' });
      return true;
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: "Couldn't rename it", body: err.message });
      return false;
    }
  };
}

/** "Better name: “Lena's call with Maya (32 min)” · Use it", next to a clip's title. */
export function NameSuggestionChip({ asset, className }: { asset: Pick<AssetSummary, 'id' | 'title' | 'nameSuggestion'>; className?: string }) {
  const take = useTakeName();
  const [busy, setBusy] = useState(false);
  const s = asset.nameSuggestion;
  if (!s?.title || s.title === asset.title) return null;
  return (
    <div className={cx('flex min-w-0 flex-wrap items-center gap-1.5 text-[12px]', className)} data-name-suggestion>
      <Sparkle size={12} weight="fill" className="shrink-0 text-accent-text" aria-hidden />
      <span className="text-muted">Better name:</span>
      <Theirs className="min-w-0 truncate font-medium text-fg" title={s.why}>
        {s.title}
      </Theirs>
      <Button
        size="sm"
        variant="ghost"
        loading={busy}
        onClick={async () => {
          setBusy(true);
          await take(asset.id, s.title);
          setBusy(false);
        }}
        data-use-name
      >
        Use it
      </Button>
    </div>
  );
}

/** "MetaRush has better names for 3 clips · Use all": names people typed are kept. */
export function BetterNames({ projectId, className }: { projectId?: string; className?: string }) {
  const toast = useToast();
  const refresh = useRefreshSources();
  const names = useOp('asset.names', projectId ? { projectId } : {}, { staleTime: 20_000 });
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(false);
  const rows = (names.data?.names ?? []).filter((r) => r.use);
  if (rows.length === 0) return null;
  const useAll = async () => {
    setBusy(true);
    try {
      const out = await call('asset.names.accept', { ...(projectId ? { projectId } : {}), assetIds: rows.map((r) => r.assetId) });
      refresh();
      toast({ tone: 'success', title: out.summary });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: "Couldn't use those names", body: err.message });
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className={cx('rounded-[14px] bg-accent-soft/50 px-4 py-3 hairline', className)} aria-label="Better names for clips" data-better-names>
      <div className="flex flex-wrap items-center gap-2">
        <Sparkle size={15} weight="fill" className="text-accent-text" aria-hidden />
        <span className="min-w-0 flex-1 text-[13px] text-fg">
          MetaRush has better names for {plural(rows.length, 'clip')}, from who&apos;s in them and what they&apos;re about. Names you typed stay.
        </span>
        <Button size="sm" variant="ghost" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
          {open ? 'Hide them' : 'See them'}
        </Button>
        <Button size="sm" variant="primary" loading={busy} onClick={() => void useAll()} data-use-all-names>
          Use all
        </Button>
      </div>
      {open ? (
        <ul className="mt-2 space-y-1 text-[12.5px]">
          {rows.map((r) => (
            <li key={r.assetId} className="flex flex-wrap items-center gap-1.5">
              <Theirs className="text-muted">{r.title}</Theirs>
              <span className="text-subtle">becomes</span>
              <Theirs className="font-medium text-fg">{r.suggestion}</Theirs>
              {r.why ? <span className="text-subtle">({r.why.replace(/\.$/, '')})</span> : null}
            </li>
          ))}
        </ul>
      ) : null}
    </section>
  );
}

/** A clip's (or a picture's) info card, with its fixes. */
export function SourceInfo({ id, className }: { id: string; className?: string }) {
  const [live, setLive] = useState(false);
  const q = useOp('asset.card', { id }, { refetchInterval: live ? 2500 : false });
  const c = q.data;
  // While a fixed copy is made (or it is measured), the card follows it.
  useEffect(() => setLive(fixesBusy(c?.fixes)), [c]);
  if (q.isLoading) return <Skeleton className={cx('h-48', className)} />;
  if (q.error || !c) return <ErrorPanel compact error={q.error} onRetry={() => void q.refetch()} className={className} />;
  return <InfoCard card={c} className={className} />;
}

function InfoCard({ card: c, className }: { card: ops.SourceCard; className?: string }) {
  const kindOf = c.picture ? 'picture' : 'clip';
  return (
    <section className={cx('panel grid grid-cols-1 gap-6 p-5 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]', className)} aria-label={`About ${c.title}`} data-info-card>
      <div className="min-w-0 space-y-4">
        <div>
          <div className="flex items-center gap-2">
            <Info size={15} className="text-accent-text" aria-hidden />
            <h3 className="text-[13.5px] font-semibold text-fg">About this {kindOf}</h3>
          </div>
          <p className="mt-1.5 text-[15px] font-semibold leading-snug text-fg">
            <Theirs>{c.title}</Theirs>
          </p>
          <p className="mt-0.5 text-[12px] text-subtle">{c.namedWords}</p>
          {c.suggestion?.title && c.suggestion.title !== c.title ? <NameSuggestionChip asset={{ id: c.id, title: c.title, nameSuggestion: c.suggestion }} className="mt-1.5" /> : null}
          <p className="mt-2 text-[12.5px] text-muted" data-file-name>
            Original file: <Theirs className="tc text-fg">{c.fileName}</Theirs>
            {c.aliases?.length ? (
              <span>
                {' '}
                (also added as {c.aliases.map((a, i) => (
                  <span key={a}>
                    {i ? ', ' : ''}
                    <Theirs>{a}</Theirs>
                  </span>
                ))}
                )
              </span>
            ) : null}
          </p>
        </div>
        <Lines title="Where it came from" lines={c.origin} />
        {c.link ? (
          <a href={c.link} target="_blank" rel="noreferrer noopener" className="inline-flex items-center gap-1 text-[12.5px] font-medium text-accent-text hover:underline">
            Open where it came from <ArrowSquareOut size={12} />
          </a>
        ) : null}
        <Lines title="What MetaRush found" lines={c.found} />
        {c.language ? <SpokenLanguage language={c.language} /> : null}
        {c.people?.length || c.onScreen?.length || c.topic ? (
          <div className="flex flex-wrap gap-1.5">
            {(c.people ?? []).map((p) => (
              <Badge key={`p-${p}`} tone="info">
                <Theirs>{p}</Theirs>
              </Badge>
            ))}
            {c.topic ? <Badge>about {c.topic}</Badge> : null}
            {(c.onScreen ?? []).slice(0, 6).map((w) => (
              <Badge key={`s-${w}`} title="Words on screen">
                <Theirs>{w}</Theirs>
              </Badge>
            ))}
          </div>
        ) : null}
        <div>
          <div className="mb-1 text-[12.5px] font-medium text-muted">Where it&apos;s used</div>
          {c.uses.length === 0 ? (
            <p className="text-[12.5px] text-subtle">Not in any project or cut yet.</p>
          ) : (
            <ul className="space-y-0.5 text-[12.5px]">
              {c.uses.map((u, i) => (
                <li key={`${u.kind}-${u.projectId ?? ''}-${i}`}>
                  {u.link ? (
                    <Link to={u.link} className="text-fg hover:underline">
                      {u.text}
                    </Link>
                  ) : (
                    <span className="text-fg">{u.text}</span>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
      <div className="min-w-0">
        {c.fixes ? (
          <FixesPanel view={c.fixes} kindOf={kindOf} />
        ) : (
          <div className="flex items-start gap-2 rounded-[12px] px-3.5 py-3 text-[12.5px] text-muted hairline">
            <MagicWand size={14} className="mt-0.5 shrink-0" aria-hidden /> MetaRush has no fixes for this kind of source.
          </div>
        )}
      </div>
    </section>
  );
}

function Lines({ title, lines }: { title: string; lines: string[] }) {
  if (!lines.length) return null;
  return (
    <div>
      <div className="mb-1 text-[12.5px] font-medium text-muted">{title}</div>
      <ul className="space-y-0.5 text-[12.5px] leading-relaxed text-fg">
        {lines.map((l) => (
          <li key={l}>{l}</li>
        ))}
      </ul>
    </div>
  );
}
