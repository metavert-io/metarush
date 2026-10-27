import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowSquareOut, Code, DeviceMobile, Desktop, GitCommit, Images, MagicWand, Palette, Scroll, X } from '@phosphor-icons/react';
import { call, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import { RESOURCE_QUERIES, resourceMedia } from '../api/resources';
import type { OpOutput, ResourceRow, ResourceShowOut } from '../api/types';
import { plural, relative } from '../lib/format';
import { useActivityOpen } from '../lib/workbench';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, cx, ErrorPanel, Skeleton, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { useChooseProject } from './ProjectPicker';

/*
 * Repositories and web pages in the resources library (D104, D105): a
 * repository's tile and overview (commit, languages, README, history, what
 * was made from it), and a web page snapshot's pictures (on a computer, on a
 * phone, the whole page) with the brand kit it suggests (D165: its logo,
 * colours and font, and "Use for this project"). Code slides, diagrams and
 * cards made from a repository say exactly where they came from (provenance).
 */

// Language colours (GitHub's), for the bar.
const LANG_COLORS: Record<string, string> = {
  Go: '#00ADD8', TypeScript: '#3178c6', JavaScript: '#f1e05a', Python: '#3572A5', Rust: '#dea584', Java: '#b07219', Kotlin: '#A97BFF', Swift: '#F05138',
  C: '#555555', 'C++': '#f34b7d', 'C#': '#178600', Ruby: '#701516', PHP: '#4F5D95', Shell: '#89e051', HTML: '#e34c26', CSS: '#663399', Vue: '#41b883',
  Svelte: '#ff3e00', Dart: '#00B4AB', Elixir: '#6e4a7e', Haskell: '#5e5086', Lua: '#000080', Zig: '#ec915c', Scala: '#c22d40', SQL: '#e38c00',
};
const langColor = (name: string) => LANG_COLORS[name] ?? 'var(--accent)';

type Lang = { name: string; share: number };

export function LanguageBar({ langs, legend }: { langs: Lang[]; legend?: boolean }) {
  if (!langs.length) return null;
  return (
    <div>
      <div className="flex h-1.5 gap-px overflow-hidden rounded-full" aria-hidden>
        {langs.map((l) => (
          <span key={l.name} style={{ flex: Math.max(0.02, l.share), background: langColor(l.name) }} />
        ))}
      </div>
      {legend ? (
        <div className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-[12px] text-muted">
          {langs.map((l) => (
            <span key={l.name} className="inline-flex items-center gap-1.5">
              <span className="h-2 w-2 rounded-full" style={{ background: langColor(l.name) }} />
              {l.name} <span className="text-subtle">{Math.round(l.share * 1000) / 10}%</span>
            </span>
          ))}
        </div>
      ) : null}
    </div>
  );
}

/** A repository's tile (it has no picture of its own). */
export function RepoTile({ r, className }: { r: ResourceRow; className?: string }) {
  const repo = r.repo;
  return (
    <div className={cx('flex flex-col justify-between bg-[linear-gradient(160deg,var(--bg-panel),var(--bg-hover))] p-3 text-left', className)} data-repo-tile>
      <div className="flex items-center gap-1.5 text-[11.5px] text-subtle">
        <Code size={13} weight="bold" className="text-accent-text" />
        {repo ? <span className="tc">{repo.commit.slice(0, 8)}</span> : null}
      </div>
      <div className="min-w-0">
        <div className="truncate text-[14px] font-semibold tracking-[-0.01em] text-fg">{r.title}</div>
        {repo ? <div className="mt-0.5 text-[11.5px] text-subtle">{plural(repo.files, 'file')}</div> : null}
      </div>
      <LanguageBar langs={(repo?.languages ?? []).slice(0, 5)} />
    </div>
  );
}

/** Where a resource came from, in a few plain words. */
export function sourceWords(r: ResourceRow): string {
  const s = r.source;
  if (r.site) return `Snapshot of ${r.site.url.replace(/^https?:\/\//, '')}, ${new Date(r.site.takenAt).toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' })}${r.site.via === 'extension' ? ', from your browser' : ''}`;
  if (r.repo) return `${r.repo.url.replace(/^https?:\/\//, '')}, as it was on ${new Date(r.repo.committedAt).toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' })}`;
  if (s.kind === 'generated' && r.provenance) return `Made from ${r.provenance}`;
  // A picture kept from a repository's folder (D216): the file at its commit.
  if (s.kind === 'repo' && r.provenance) return `From ${r.provenance}`;
  return '';
}

const SHOTS = [
  { label: 'desktop', name: 'Computer', icon: <Desktop size={13} /> },
  { label: 'mobile', name: 'Phone', icon: <DeviceMobile size={13} /> },
  { label: 'full', name: 'Whole page', icon: <Scroll size={13} /> },
];

/** A web page snapshot's pictures, one at a time (the whole page scrolls). */
export function SnapshotPictures({ r }: { r: ResourceRow }) {
  const [page, setPage] = useState(1);
  const count = r.pagePictures || 1;
  const shown = SHOTS.slice(0, Math.max(1, count));
  const tall = shown[page - 1]?.label !== 'desktop';
  return (
    <div>
      <div className={cx('overflow-auto rounded-[14px] bg-inset hairline', tall ? 'h-[420px]' : 'aspect-[16/10]')} data-snapshot-picture={shown[page - 1]?.label}>
        <img src={resourceMedia.file(r.id, { sha: r.current, page: count > 1 ? page : undefined })} alt="" className={cx('block w-full', !tall && 'h-full object-contain object-top', shown[page - 1]?.label === 'mobile' && 'mx-auto max-w-[260px]')} />
      </div>
      {shown.length > 1 ? (
        <div className="mt-2 flex flex-wrap gap-1" role="group" aria-label="Which picture">
          {shown.map((s, i) => (
            <button
              key={s.label}
              type="button"
              aria-pressed={page === i + 1}
              onClick={() => setPage(i + 1)}
              className={cx('inline-flex h-7 items-center gap-1.5 rounded-full px-2.5 text-[12px] transition-colors hairline', page === i + 1 ? 'bg-panel text-fg' : 'text-muted hover:text-fg')}
            >
              {s.icon} {s.name}
              <span className="tc text-subtle">#{i + 1}</span>
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}

/** What a snapshot says about the page, and the brand kit it suggests (D165). */
export function SiteFacts({ r, projectId }: { r: ResourceRow; projectId?: string }) {
  const s = r.site;
  if (!s) return null;
  const branded = (s.brand?.logos?.length ?? 0) + (s.brand?.colors?.length ?? 0) > 0;
  return (
    <div className="space-y-2 text-[12.5px]">
      {s.description ? <p className="leading-relaxed text-muted">{s.description}</p> : null}
      <div className="flex flex-wrap items-center gap-1.5">
        {(s.themeColors ?? []).slice(0, 5).map((c) => (
          <span key={c} title={c} className="h-4 w-4 rounded-full ring-1 ring-line-strong" style={{ background: c }} />
        ))}
        {s.signedIn ? <Badge tone="info">as you saw it</Badge> : <Badge>signed out</Badge>}
      </div>
      <a href={s.url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-accent-text hover:underline">
        Open the page <ArrowSquareOut size={12} />
      </a>
      {branded ? <SiteBrandSuggestion from={r.id} projectId={projectId ?? r.projectId} className="mt-3" /> : null}
    </div>
  );
}

type Suggestion = OpOutput<'brand.suggest'>;

const KIT_COLOURS = [
  { key: 'primary', label: 'Primary' },
  { key: 'accent', label: 'Accent' },
  { key: 'text', label: 'Text' },
  { key: 'background', label: 'Background' },
] as const;

/** The queries a brand kit taken from a site changes. */
function refreshAfterBrand(qc: ReturnType<typeof useQueryClient>) {
  for (const k of [...RESOURCE_QUERIES, 'brand.suggest', 'revision.list', 'revision.show', 'cut.show']) void qc.invalidateQueries({ queryKey: [k] });
}

/**
 * "Suggested from <site>" (D165): the brand kit a website snapshot suggests —
 * its logo and icon pictures, four colours and a font, with why — and "Use for
 * this project": brand.set with the site (fromSite) and the latest cut
 * (apply). When that saves a new version of the cut, a styled dialog asks
 * first; the same as `metarush brand set --from-site … --apply`.
 */
export function SiteBrandSuggestion({ from, projectId, className }: { from: string; projectId?: string; className?: string }) {
  const q = useOp('brand.suggest', projectId ? { from, projectId } : { from });
  const qc = useQueryClient();
  const toast = useToast();
  const dialog = useDialog();
  const chooseProject = useChooseProject();
  const [busy, setBusy] = useState(false);
  if (q.isLoading) return <Skeleton className={cx('h-40', className)} />;
  if (q.error || !q.data) return q.error ? <ErrorPanel compact error={q.error} className={className} /> : null;
  const s: Suggestion = q.data;
  const kit = s.kit;
  const site = s.site.site?.site || s.site.title;
  const target = projectId ?? s.projectId;

  const use = async () => {
    let pid = target;
    if (!pid) {
      const p = await chooseProject({ title: 'Use this brand for a project', body: 'The project gets its own brand kit from this website: its logo, its colours and its font.' });
      if (!p) return;
      pid = p.id;
    }
    setBusy(true);
    try {
      const dry = await call('brand.set', { projectId: pid, fromSite: s.site.id, apply: true, dryRun: true });
      if (dry.cut?.check && !dry.cut.check.valid) {
        const problems = dry.cut.problems.filter((x) => x.severity === 'error').map((x) => x.message);
        toast({ tone: 'error', title: "The cut can't take this brand yet", body: problems.join(' ') || dry.message });
        return;
      }
      if (dry.cut && !dry.cut.noChange) {
        const ok = await dialog.confirm({
          title: 'Save a new version of the cut with this brand?',
          body: (
            <div className="space-y-2">
              <p>{dry.cut.summary}</p>
              {dry.cut.changes.length > 1 ? (
                <ul className="list-disc space-y-0.5 pl-5 text-muted">
                  {dry.cut.changes.slice(0, 6).map((c) => (
                    <li key={c}>{c}</li>
                  ))}
                </ul>
              ) : null}
              <p className="text-muted">The project keeps this brand kit for its new cuts too. Undo is in the cut's history.</p>
            </div>
          ),
          confirmLabel: 'Save a new version',
        });
        if (!ok) return;
      }
      const out = await call('brand.set', { projectId: pid, fromSite: s.site.id, apply: true });
      refreshAfterBrand(qc);
      toast({ tone: 'success', title: 'The project uses this brand now', body: out.message });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: "The brand kit didn't change", body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className={cx('rounded-[16px] p-4 hairline', className)} data-testid="brand-suggestion">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <MagicWand size={14} className="text-accent-text" aria-hidden />
        <h3 className="text-[13.5px] font-semibold text-fg">
          Suggested from <Theirs>{site}</Theirs>
        </h3>
        {s.inUse ? <Badge tone="ok">In use for this project</Badge> : null}
      </div>
      <div className="flex flex-wrap items-start gap-x-6 gap-y-4">
        {s.logos.length ? (
          <div className="flex flex-wrap gap-2" aria-label="Pictures kept from the site">
            {s.logos.map((l) => (
              <figure key={l.resource.id} className="w-[92px]">
                <div
                  className={cx(
                    'relative h-14 w-[92px] overflow-hidden rounded-[10px] bg-[repeating-conic-gradient(var(--bg-inset)_0_25%,var(--bg-panel)_0_50%)] bg-[length:12px_12px] hairline',
                    l.chosen && 'ring-2 ring-accent-line',
                  )}
                >
                  <img src={l.resource.thumbUrl} alt="" className="absolute inset-1.5 h-[calc(100%-0.75rem)] w-[calc(100%-0.75rem)] object-contain" />
                </div>
                <figcaption className="mt-1 truncate text-[11px] text-subtle" title={l.chosen ? 'The suggested logo' : undefined}>
                  <Theirs>{l.resource.title}</Theirs>
                </figcaption>
              </figure>
            ))}
          </div>
        ) : null}
        <div className="flex gap-3" aria-label="Suggested colours">
          {KIT_COLOURS.map((c) => (
            <div key={c.key} className="text-center">
              <span className="mx-auto block h-8 w-8 rounded-full ring-1 ring-line-strong" style={{ background: kit[c.key] }} title={kit[c.key]} />
              <span className="mt-1 block text-[11px] text-subtle">{c.label}</span>
            </div>
          ))}
        </div>
        <div className="text-[12.5px]">
          <span className="block text-[11px] text-subtle">Font</span>
          <span className="text-fg">{s.fontFile ? <Theirs>{s.fontFile.title}</Theirs> : kit.font || "MetaRush's own"}</span>
        </div>
      </div>
      {s.why.length ? (
        <details className="mt-3 text-[12.5px]">
          <summary className="cursor-pointer text-muted">Why these</summary>
          <ul className="mt-1.5 space-y-0.5 leading-relaxed text-muted">
            {s.why.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        </details>
      ) : null}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button size="sm" variant="primary" icon={<Palette size={13} />} loading={busy} disabled={s.inUse} onClick={() => void use()}>
          {target ? 'Use for this project' : 'Use for a project…'}
        </Button>
        <span className="text-[12px] text-subtle">{s.message}</span>
      </div>
    </section>
  );
}

/**
 * The pictures a repository keeps from its folders (D216): the folders, the
 * pictures kept at its commit, what was left out and why, and — for the
 * project that owns it — keeping another folder (its pictures come in at the
 * commit it shows, a fetch on this computer) or stopping one (its pictures
 * stay). The same as `metarush resources pictures <repo> --keep|--drop`.
 */
export function RepoPicturesPanel({ data, projectId, own }: { data: ResourceShowOut; projectId?: string; own: boolean }) {
  const p = data.repo?.pictures;
  const qc = useQueryClient();
  const toast = useToast();
  const [, setActivity] = useActivityOpen();
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState<string | null>(null);
  const paths = p?.paths ?? [];
  const change = async (input: { keep?: string[]; drop?: string[] }, key: string) => {
    setBusy(key);
    try {
      const out = await call('resources.pictures.set', { ref: data.row.id, ...(projectId ? { projectId } : {}), ...input });
      for (const k of RESOURCE_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
      toast({
        title: out.message ?? 'Saved.',
        body: out.notes?.length ? out.notes.join(' ') : undefined,
        action: out.jobId ? { label: 'Show progress', onClick: () => setActivity(true) } : undefined,
        durationMs: 9000,
      });
      if (input.keep) setDraft('');
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: input.keep ? "Couldn't keep that folder's pictures" : "Couldn't stop keeping that folder", body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(null);
    }
  };
  const more = p ? p.kept - p.pictures.length : 0;
  return (
    <section className="space-y-2" data-testid="repo-pictures">
      <div className="flex items-center gap-1.5 font-medium text-muted">
        <Images size={13} /> Pictures it keeps
      </div>
      <p className="text-subtle">
        {p ? p.summary : 'Only its text files. It can also keep the pictures in one of its folders (art kept in git): each becomes a picture graphics can show, and says which commit it came from.'}
      </p>
      {paths.length ? (
        <div className="flex flex-wrap gap-1.5" aria-label="Folders it keeps pictures from">
          {paths.map((path) => (
            <span key={path} className="inline-flex h-7 items-center gap-1 rounded-full bg-panel pl-2.5 pr-1 text-[12px] text-fg hairline" data-picture-path={path}>
              <Theirs>{path === '.' ? 'the whole repository' : path}</Theirs>
              {own ? (
                <button
                  type="button"
                  aria-label={`Stop keeping ${path === '.' ? 'the whole repository' : path} in step`}
                  title="Stop keeping it in step (its pictures stay)"
                  disabled={busy !== null}
                  onClick={() => void change({ drop: [path] }, `drop:${path}`)}
                  className="rounded-full p-1 text-subtle hover:bg-hover hover:text-fg"
                >
                  <X size={10} />
                </button>
              ) : null}
            </span>
          ))}
        </div>
      ) : null}
      {p?.pictures.length ? (
        <ul className="grid grid-cols-4 gap-2 sm:grid-cols-6" aria-label="Pictures kept from it">
          {p.pictures.map((x) => (
            <li key={x.path} className="min-w-0" title={x.path}>
              <div className="relative aspect-square overflow-hidden rounded-[10px] bg-[repeating-conic-gradient(var(--bg-inset)_0_25%,var(--bg-panel)_0_50%)] bg-[length:12px_12px] hairline">
                <img src={x.thumbUrl} alt="" loading="lazy" className="absolute inset-1 h-[calc(100%-0.5rem)] w-[calc(100%-0.5rem)] object-contain" />
              </div>
              <div className="mt-1 truncate text-[11px] text-subtle">
                <Theirs>{x.title}</Theirs>
              </div>
            </li>
          ))}
        </ul>
      ) : null}
      {more > 0 ? <p className="text-subtle">And {plural(more, 'more picture')}.</p> : null}
      {p?.leftOut?.length ? (
        <details>
          <summary className="cursor-pointer text-muted">{plural(p.skipped ?? p.leftOut.length, 'file')} left out, and why</summary>
          <ul className="mt-1.5 space-y-0.5 text-subtle">
            {p.leftOut.map((l) => (
              <li key={l.path} className="min-w-0 break-words">
                <Theirs>{l.path}</Theirs>: {l.why}
              </li>
            ))}
          </ul>
        </details>
      ) : null}
      {own ? (
        <form
          className="flex flex-wrap items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            const path = draft.trim();
            if (path) void change({ keep: [path] }, 'keep');
          }}
        >
          <input
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder="A folder, like art/characters"
            aria-label="A folder of the repository to keep the pictures of"
            className="h-8 min-w-0 flex-1 rounded-[9px] bg-panel px-2.5 text-[12.5px] text-fg hairline placeholder:text-subtle"
          />
          <Button size="sm" variant="secondary" icon={<Images size={13} />} type="submit" loading={busy === 'keep'} disabled={!draft.trim() || busy !== null}>
            Keep its pictures
          </Button>
        </form>
      ) : null}
    </section>
  );
}

/** A repository's overview: languages, size, README, history, what was made from it. */
export function RepoOverview({ data }: { data: ResourceShowOut }) {
  const d = data.repo;
  if (!d) return null;
  return (
    <div className="space-y-3 text-[12.5px]">
      {d.description ? <p className="leading-relaxed text-muted">{d.description}</p> : null}
      <LanguageBar langs={d.languages ?? []} legend />
      <p className="text-subtle">
        {plural(d.files, 'file')}
        {d.skipped ? ` · ${d.skipped} left out (pictures, large files)` : ''}
        {d.license ? ` · licence ${d.license}` : ' · licence not stated'}
        {d.stars ? ` · ${d.stars.toLocaleString()} stars` : ''}
      </p>
      {d.recent?.length ? (
        <div>
          <div className="mb-1 flex items-center gap-1.5 font-medium text-muted">
            <GitCommit size={13} /> Recent commits
          </div>
          <ul className="space-y-0.5">
            {d.recent.slice(0, 4).map((c) => (
              <li key={c.sha} className="flex min-w-0 gap-2 text-subtle">
                <span className="min-w-0 flex-1 truncate text-fg">{c.subject}</span>
                <span className="shrink-0">{c.author ? `${c.author}, ` : ''}{relative(c.at)}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {d.readme ? (
        <details>
          <summary className="cursor-pointer text-muted">From its README</summary>
          <p className="mt-1.5 max-h-48 overflow-auto whitespace-pre-wrap rounded-[10px] bg-inset p-2.5 text-[12px] leading-relaxed text-muted">{d.readme.slice(0, 1500)}</p>
        </details>
      ) : null}
      {data.made?.length ? (
        <div>
          <div className="mb-1 font-medium text-muted">Made from it</div>
          <ul className="space-y-0.5">
            {data.made.map((m) => (
              <li key={m.id} className="flex min-w-0 gap-2">
                <span className="min-w-0 flex-1 truncate text-fg">{m.title}</span>
                <span className="tc shrink-0 text-subtle">{m.name}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : (
        <p className="text-subtle">Ask Claude to “make an architecture slide from the repo” or “put a code snippet of the main loop on screen”.</p>
      )}
    </div>
  );
}
