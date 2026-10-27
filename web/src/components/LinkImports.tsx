import { useCallback, useId, useState, type ReactNode } from 'react';
import { ArrowSquareOut, Browser as BrowserIcon, LinkSimple, PuzzlePiece } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import type { AssetSource, ExtensionBrowser, ImportIn, ImportOut } from '../api/types';
import { absolute, relative } from '../lib/format';
import { useActivityOpen } from '../lib/workbench';
import { useHost } from '../lib/host';
import { useDialog } from '../ui/Dialog';
import { Badge, Button, CopyButton, cx, Details, ErrorPanel, Skeleton, Theirs } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { AIBadge, isGenerated } from './generate/ai';
import { ownerTitle, useOwnership } from './OwnerOnly';
import { looksLikeFolder, useRemotionImport } from './remotion/useRemotionImport';

/* ---------- words ---------- */

export function siteName(platform: string | undefined): string {
  return ({ youtube: 'YouTube', tiktok: 'TikTok', x: 'X' } as Record<string, string>)[platform ?? ''] ?? 'the web';
}

function who(s: AssetSource): string {
  if (s.handle) return s.handle.startsWith('@') ? s.handle : `@${s.handle}`;
  return s.uploader ?? '';
}

function uploadedOn(s: AssetSource): string {
  if (!s.uploadDate || !/^\d{4}-\d{2}-\d{2}$/.test(s.uploadDate)) return '';
  return new Date(`${s.uploadDate}T00:00:00Z`).toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' });
}

/** Web links in pasted or dropped text (one per line, or among other words). */
export function findLinks(text: string): string[] {
  const out: string[] = [];
  for (let w of text.split(/\s+/)) {
    w = w.replace(/^[<([{"',;]+|[>)\]}"',;]+$/g, '');
    if (/^https?:\/\/\S+$/i.test(w) && !out.includes(w)) out.push(w);
  }
  return out;
}

/* ---------- importing ---------- */

export interface ImportTarget {
  projectId?: string;
  label?: string;
}

interface ImportExtra {
  browserSignIn?: ImportIn['browserSignIn'];
  playlist?: number;
  /** Called once an import starts, including one started later from the toast ("Import the first 5"). */
  onStarted?: () => void;
  /** A project's Sources: ask whether a GitHub repository is a Remotion project (D131). */
  remotion?: boolean;
}

type ImportRow = ImportOut['imports'][number];

function importingTitle(r: ImportRow, n: number | undefined): string {
  if (r.status === 'importing') return 'Already being imported';
  if (r.kind === 'playlist') return `Importing the first ${n ?? ''} videos of the playlist`.replace('  ', ' ');
  if (r.kind === 'channel') return `Importing the latest ${n ?? ''} videos from ${siteName(r.platform)}`.replace('  ', ' ');
  return `Importing the video from ${siteName(r.platform)}`;
}

/**
 * Imports links (ingest.url) and says what happens in a toast: importing (with
 * "Show progress"), already in MetaRush, or why nothing was imported (a
 * playlist offers "Import the first 5").
 */
export function useImportLinks() {
  const toast = useToast();
  const qc = useQueryClient();
  const [, setActivity] = useActivityOpen();
  const run = useCallback(
    async (urls: string[], target: ImportTarget = {}, extra: ImportExtra = {}): Promise<ImportOut | null> => {
      let out: ImportOut;
      try {
        out = await call('ingest.url', { urls, projectId: target.projectId, browserSignIn: extra.browserSignIn, playlist: extra.playlist });
      } catch (e) {
        const err = toApiError(e);
        toast({ tone: 'error', title: 'Could not import that link', body: err.hint ? `${err.message} ${err.hint}` : err.message });
        return null;
      }
      for (const k of ['activity.summary', 'asset.list', 'project.show', 'job.list']) void qc.invalidateQueries({ queryKey: [k] });
      const into = target.label ?? 'the inbox';
      const going = out.imports.filter((r) => r.status === 'queued' || r.status === 'importing');
      if (going.length) {
        const first = going[0]!;
        extra.onStarted?.();
        toast({
          title: going.length === 1 ? importingTitle(first, extra.playlist) : `Importing ${going.length} videos`,
          body: `Into ${into}. ${out.running ? 'It shows in the activity bar while it downloads.' : (out.note ?? '')}`,
          action: { label: 'Show progress', onClick: () => setActivity(true) },
        });
      }
      for (const r of out.imports.filter((x) => x.status === 'already')) toast({ tone: 'success', title: 'Already in MetaRush', body: r.message });
      for (const r of out.imports.filter((x) => x.status === 'refused')) {
        if (r.code === 'playlist') {
          toast({
            title: r.kind === 'channel' ? 'That link is a channel, so nothing was imported' : 'That link is a playlist, so nothing was imported',
            body: 'MetaRush imports one video at a time unless you ask for more.',
            action: { label: 'Import the first 5', onClick: () => void run([r.input], target, { ...extra, playlist: 5 }) },
            durationMs: 15000,
          });
        } else {
          toast({ tone: 'error', title: r.message, body: r.next });
        }
      }
      return out;
    },
    [toast, qc, setActivity],
  );
  return run;
}

/**
 * Links through the Add box's classifier (add.route, the same as the CLI's
 * `metarush add`): videos are imported (ingest.url, with its toasts); GitHub
 * repositories (pinned at a commit) and web pages (dated snapshots) are
 * brought in as resources by a fetch job on this computer (add.links); a link
 * that could be either asks once, in plain words.
 */
export function useAddLinks() {
  const toast = useToast();
  const dialog = useDialog();
  const qc = useQueryClient();
  const [, setActivity] = useActivityOpen();
  const importLinks = useImportLinks();
  const importRemotion = useRemotionImport();
  return useCallback(
    async (urls: string[], target: ImportTarget = {}, extra: ImportExtra = {}): Promise<{ started: number; handled: number }> => {
      let routes;
      try {
        routes = await call('add.route', { text: urls.join('\n') });
      } catch (e) {
        const err = toApiError(e);
        toast({ tone: 'error', title: 'Could not read that link', body: err.hint ? `${err.message} ${err.hint}` : err.message });
        return { started: 0, handled: 0 };
      }
      const videos: string[] = [];
      const fetches: string[] = [];
      const pages: string[] = [];
      let handled = 0;
      let started = 0;
      for (const l of routes.links) {
        if (l.route === 'video') {
          videos.push(l.input);
        } else if (l.route === 'repo' && extra.remotion && target.projectId) {
          // In a project's Sources a repository may be a Remotion project: its videos become clips.
          const as = await dialog.open<'remotion' | 'resource'>(
            (close) => (
              <div>
                <p className="text-[13.5px] leading-relaxed text-muted">
                  If <span className="break-all font-medium text-fg">{l.input}</span> is a Remotion project (videos made from code), MetaRush can bring it in so its compositions become
                  clips in {target.label ?? 'this project'}. Otherwise it is kept for code slides and diagrams.
                </p>
                <div className="mt-5 flex flex-wrap justify-end gap-2 border-t border-line pt-4">
                  <Button variant="ghost" onClick={() => close('resource')}>
                    Keep it for code slides
                  </Button>
                  <Button variant="primary" onClick={() => close('remotion')}>
                    It's a Remotion project
                  </Button>
                </div>
              </div>
            ),
            { title: 'Is this a Remotion project?', size: 'sm' },
          );
          if (as === 'remotion') {
            handled++;
            if (await importRemotion(l.input, target.projectId)) {
              started++;
              extra.onStarted?.();
            }
          } else if (as === 'resource') {
            if (l.ready) fetches.push(l.input);
            else {
              handled++;
              toast({ title: l.message, durationMs: 9000 });
            }
          }
        } else if (l.route === 'repo' || l.route === 'page') {
          if (l.ready) fetches.push(l.input);
          else {
            handled++;
            toast({ title: l.message, durationMs: 9000 });
          }
        } else if (l.route === 'ambiguous') {
          const as = await dialog.open<'video' | 'page'>(
            (close) => (
              <div>
                <p className="text-[13.5px] leading-relaxed text-muted">
                  <span className="break-all font-medium text-fg">{l.input}</span> could be a video to import, or a web page to use in graphics.
                </p>
                <div className="mt-5 flex flex-wrap justify-end gap-2 border-t border-line pt-4">
                  <Button variant="ghost" onClick={() => close('page')}>
                    It's a page
                  </Button>
                  <Button variant="primary" onClick={() => close('video')}>
                    Import the video
                  </Button>
                </div>
              </div>
            ),
            { title: 'Is this a video?', size: 'sm' },
          );
          if (as === 'video') videos.push(l.input);
          else if (as === 'page') pages.push(l.input);
        } else {
          handled++;
          toast({ tone: 'error', title: l.message || "That isn't a link MetaRush can use" });
        }
      }
      // Repositories and pages: a fetch job on this computer brings them into Resources.
      for (const [urls, as] of [
        [fetches, undefined],
        [pages, 'page'],
      ] as const) {
        if (!urls.length) continue;
        try {
          const out = await call('add.links', { urls: [...urls], projectId: target.projectId, as });
          for (const o of out.links) {
            handled++;
            if (o.status === 'fetching') {
              started++;
              toast({
                title: o.message,
                body: `It shows up in ${target.label ? `${target.label}'s resources` : 'the workspace library'} when it's done.`,
                action: { label: 'Show progress', onClick: () => setActivity(true) },
                durationMs: 9000,
              });
            } else {
              toast({ tone: o.status === 'refused' ? 'error' : undefined, title: o.message });
            }
          }
          for (const k of ['resources.list', 'job.list', 'activity.summary']) void qc.invalidateQueries({ queryKey: [k] });
        } catch (e) {
          const err = toApiError(e);
          toast({ tone: 'error', title: 'Could not bring that in', body: err.hint ? `${err.message} ${err.hint}` : err.message });
        }
      }
      if (started) extra.onStarted?.();
      if (!videos.length) return { started, handled };
      const out = await importLinks(videos, target, extra);
      return { started: started + (out ? out.imports.filter((r) => r.status === 'queued' || r.status === 'importing').length : 0), handled: handled + (out ? out.imports.length - out.refused : 0) };
    },
    [toast, dialog, importLinks, importRemotion, qc, setActivity],
  );
}

/** The Add box's "Paste a link" field. */
export function PasteLinkField({ target, className, hint, compact, remotion }: {
  target?: ImportTarget;
  className?: string;
  hint?: string;
  compact?: boolean;
  /** A project's Sources: also takes a Remotion project's folder, and asks whether a GitHub repository is one. */
  remotion?: boolean;
}) {
  const [value, setValue] = useState('');
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState('');
  const addLinks = useAddLinks();
  const importRemotion = useRemotionImport();
  const folders = !!remotion && !!target?.projectId;
  const host = useHost();
  const id = useId();
  const submit = async () => {
    if (folders && looksLikeFolder(value)) {
      setProblem('');
      setBusy(true);
      const ok = await importRemotion(value, target!.projectId!);
      setBusy(false);
      if (ok) setValue('');
      return;
    }
    const links = findLinks(value.includes('://') ? value : value ? `https://${value.trim()}` : '');
    if (!links.length) {
      setProblem(folders ? "Paste a link (a video, a GitHub repository or a web page) or the full path of a Remotion project's folder." : 'Paste a link: a video, a GitHub repository or a web page.');
      return;
    }
    setProblem('');
    setBusy(true);
    // The field empties once the import starts, also when that is later, from the toast ("Import the first 5").
    const out = await addLinks(links, target, { onStarted: () => setValue(''), remotion: folders });
    setBusy(false);
    if (out.handled > 0 || out.started > 0) setValue('');
  };
  if (compact)
    return (
      <form
        className={cx('flex min-w-0 gap-1.5', className)}
        aria-label="Import from a link"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <input
          id={id}
          type="text"
          inputMode="url"
          autoComplete="off"
          spellCheck={false}
          value={value}
          onChange={(e) => {
            setValue(e.target.value);
            setProblem('');
          }}
          aria-label={folders ? 'Paste a link or a folder' : 'Paste a link'}
          aria-invalid={!!problem || undefined}
          title={problem || undefined}
          placeholder={folders ? 'Paste a link or a folder' : 'Paste a link'}
          className={cx('h-9 min-w-0 flex-1 rounded-[10px] bg-panel px-3 text-[13px] text-fg outline-none placeholder:text-subtle hairline focus:ring-2 focus:ring-accent', problem && 'ring-2 ring-danger')}
        />
        <Button type="submit" variant="secondary" size="md" loading={busy} disabled={!value.trim()}>
          Import
        </Button>
      </form>
    );
  return (
    <form
      className={cx('w-full', className)}
      aria-label="Import from a link"
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <label htmlFor={id} className="mb-1.5 flex items-center gap-1.5 text-[12.5px] font-medium text-muted">
        <LinkSimple size={13} /> Or paste a link
      </label>
      <div className="flex gap-2">
        <input
          id={id}
          type="text"
          inputMode="url"
          autoComplete="off"
          spellCheck={false}
          value={value}
          onChange={(e) => {
            setValue(e.target.value);
            setProblem('');
          }}
          placeholder={folders ? "A video, a GitHub repository, a web page or a Remotion project's folder" : 'A video, a GitHub repository or a web page'}
          className="h-10 min-w-0 flex-1 rounded-[10px] bg-panel px-3 text-[13.5px] text-fg outline-none placeholder:text-subtle hairline focus:ring-2 focus:ring-accent"
        />
        <Button type="submit" variant="secondary" size="lg" loading={busy} disabled={!value.trim()}>
          Import
        </Button>
      </div>
      {problem ? (
        <p role="alert" className="mt-1.5 text-[12px] text-danger">
          {problem}
        </p>
      ) : (
        <p className="mt-1.5 text-[11.5px] text-subtle">{hint ?? `Videos are downloaded ${host.onHere} in the best quality; a repository is kept at its current commit and a web page as a dated snapshot. Where each came from stays with it.`}</p>
      )}
    </form>
  );
}

/* ---------- the browser sign-in ---------- */

const BROWSERS = [
  { id: 'chrome', label: 'Chrome' },
  { id: 'safari', label: 'Safari' },
  { id: 'firefox', label: 'Firefox' },
  { id: 'brave', label: 'Brave' },
  { id: 'edge', label: 'Edge' },
];

function SignInChoice({ initial, site, onDone }: { initial: string; site: string; onDone: (browser?: string) => void }) {
  const [browser, setBrowser] = useState(initial || 'chrome');
  const name = BROWSERS.find((b) => b.id === browser)?.label ?? 'your browser';
  return (
    <div>
      <p className="text-[13.5px] leading-relaxed text-muted">
        {site} asked MetaRush to prove it isn't a bot. MetaRush can download this video once with the sign-in from one of your browsers, as if you were watching it there.
      </p>
      <p className="mt-2 text-[12.5px] leading-relaxed text-subtle">
        Your sign-in stays in the browser: the downloader reads it for this download only, and MetaRush never copies or keeps your cookies. Make sure you're signed in to {site} in that browser.
      </p>
      <fieldset className="mt-4">
        <legend className="mb-2 text-[12.5px] font-medium text-fg">Which browser?</legend>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-5" role="radiogroup">
          {BROWSERS.map((b) => (
            <button
              key={b.id}
              type="button"
              role="radio"
              aria-checked={browser === b.id}
              onClick={() => setBrowser(b.id)}
              className={cx('flex h-10 items-center justify-center rounded-[10px] text-[13px] transition-colors hairline', browser === b.id ? 'bg-accent-soft text-accent-text ring-1 ring-accent-line' : 'text-muted hover:bg-hover')}
            >
              {b.label}
            </button>
          ))}
        </div>
      </fieldset>
      <p className="mt-3 text-[12px] leading-relaxed text-subtle">
        {browser === 'safari'
          ? 'Safari keeps its sign-ins private: the program running MetaRush needs Full Disk Access (System Settings, Privacy & Security).'
          : browser === 'firefox'
            ? 'Firefox can stay open.'
            : `macOS may ask to allow access to “${name} Safe Storage”: choose Allow.`}
      </p>
      <div className="mt-5 flex flex-wrap justify-end gap-2 border-t border-line pt-4">
        <Button variant="ghost" onClick={() => onDone()}>
          Cancel
        </Button>
        <Button variant="primary" icon={<BrowserIcon size={15} />} onClick={() => onDone(browser)}>
          Download with {name}
        </Button>
      </div>
    </div>
  );
}

/** "Use my browser's sign-in" for a failed import: choose the browser, then import again. */
export function useBrowserSignIn() {
  const dialog = useDialog();
  const importLinks = useImportLinks();
  const own = useOwnership();
  const toast = useToast();
  return useCallback(
    async (url: string, platform: string | undefined, target: ImportTarget) => {
      // The browser's sign-in is this computer's person's own (D191).
      if (!own.owner) {
        toast({ tone: 'info', title: "Only an owner can use this computer's browser sign-in", body: ownerTitle(own, 'lend it to an import') });
        return;
      }
      let remembered = '';
      try {
        remembered = (await call('ingest.url.tools', {})).browser ?? '';
      } catch {
        // the choice starts at Chrome
      }
      const browser = await dialog.open<string>((close) => <SignInChoice initial={remembered} site={siteName(platform)} onDone={(b) => close(b)} />, {
        title: "Use your browser's sign-in",
        size: 'md',
      });
      if (browser) await importLinks([url], target, { browserSignIn: browser as ImportIn['browserSignIn'] });
    },
    [dialog, importLinks, own, toast],
  );
}

/* ---------- where a clip came from ---------- */

/** "From YouTube · @handle · 12 Mar 2024 · open original". */
export function SourceCredit({ source, className }: { source?: AssetSource | null; className?: string }) {
  if (!source) return null;
  if (isGenerated(source))
    return (
      <div className={cx('flex min-w-0 flex-wrap items-center gap-x-1.5 text-[12px] text-muted', className)}>
        <AIBadge source={source} />
        <span className="min-w-0">Made with {source.generated?.providerTitle ?? 'generative video'}</span>
      </div>
    );
  const parts = [`From ${siteName(source.platform)}`, who(source), uploadedOn(source)].filter(Boolean);
  return (
    <div className={cx('flex min-w-0 flex-wrap items-center gap-x-1 text-[12px] text-muted', className)}>
      <LinkSimple size={12} className="shrink-0 text-subtle" aria-hidden />
      <span className="min-w-0">{parts.join(' · ')}</span>
      {source.url ? (
        <>
          <span aria-hidden>·</span>
          <a href={source.url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-0.5 text-accent-text hover:underline">
            open original <ArrowSquareOut size={11} aria-hidden />
          </a>
        </>
      ) : null}
    </div>
  );
}

/** Details rows about the original post (the clip's Details tab). */
export function sourceRows(s: AssetSource | null | undefined): [string, ReactNode][] {
  if (!s) return [];
  if (isGenerated(s)) {
    const g = s.generated;
    return [
      ['Came from', <SourceCredit source={s} />],
      ['Model', `${g?.modelTitle ?? s.toolVersion ?? ''} (${g?.model ?? ''})`],
      ...(g?.prompt ? ([['Prompt', <span className="break-words">“{g.prompt}”</span>]] as [string, ReactNode][]) : []),
      ...(g?.look ? ([['Look', g.look]] as [string, ReactNode][]) : []),
      ['Watermark', g?.watermark ? `${g.watermark} (invisible, added by the provider)` : 'none stated'],
      ['Made', <span title={absolute(s.downloadedAt)}>{relative(s.downloadedAt)}</span>],
    ];
  }
  const how =
    s.tool === 'yt-dlp'
      ? `downloaded by MetaRush (yt-dlp ${s.toolVersion ?? ''})`
      : `saved from your browser (MetaRush extension ${s.toolVersion ?? ''})${s.method === 'recording' ? ', recorded while it played' : ''}`;
  return [
    ['Came from', <SourceCredit source={s} />],
    ...(s.title ? ([['Original title', <span className="break-words">{s.title}</span>]] as [string, ReactNode][]) : []),
    ...(s.uploader || s.channelUrl
      ? ([[
          'Account',
          s.channelUrl ? (
            <a href={s.channelUrl} target="_blank" rel="noopener noreferrer" className="text-accent-text hover:underline">
              {[s.uploader, s.handle].filter(Boolean).join(' ')}
            </a>
          ) : (
            [s.uploader, s.handle].filter(Boolean).join(' ')
          ),
        ]] as [string, ReactNode][])
      : []),
    ['License', s.license || 'none stated'],
    ['Fetched', <span title={absolute(s.downloadedAt)}>{`${relative(s.downloadedAt)}, ${how.trim()}${s.signedIn && s.tool === 'yt-dlp' ? ", with your browser's sign-in" : ''}`}</span>],
  ];
}

/* ---------- Settings ---------- */

/** Settings → Footage → Link imports: the downloader on this machine and its updates. */
export function LinkImportSettings() {
  const st = useOp('ingest.url.tools', {}, { staleTime: 60_000 });
  const host = useHost();
  const qc = useQueryClient();
  const toast = useToast();
  const [busy, setBusy] = useState('');
  const d = st.data;
  const refresh = () => void qc.invalidateQueries({ queryKey: ['ingest.url.tools'] });
  const update = async () => {
    setBusy('update');
    try {
      const r = await call('ingest.url.update', {});
      toast({ tone: 'success', title: r.updated ? 'Downloader updated' : 'Already up to date', body: r.message });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'Could not update the downloader', body: err.hint ? `${err.message} (${err.hint})` : err.message });
    } finally {
      setBusy('');
      refresh();
    }
  };
  const configure = async (input: { autoUpdate?: boolean; forgetBrowser?: boolean }) => {
    setBusy('config');
    try {
      await call('ingest.url.configure', input);
    } catch (e) {
      toast({ tone: 'error', title: 'Could not change it', body: toApiError(e).message });
    } finally {
      setBusy('');
      refresh();
    }
  };
  return (
    <section id="link-imports" className="panel scroll-mt-28 p-5" aria-labelledby="li-h">
      <h2 id="li-h" className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <LinkSimple size={17} />
        </span>
        Link imports
      </h2>
      <p className="mb-4 text-[12.5px] leading-relaxed text-muted">Paste a YouTube, TikTok or X link in the inbox and MetaRush downloads the video {host.onHere}.</p>
      {st.isLoading ? (
        <Skeleton className="h-16" />
      ) : st.error ? (
        <ErrorPanel compact error={st.error} onRetry={() => void st.refetch()} />
      ) : d ? (
        <div className="space-y-3 text-[13px]">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-fg">{d.installed ? `Downloader: ${d.summary}` : `The downloader (yt-dlp) is not installed ${host.onHere}.`}</span>
            {d.installed ? <Badge tone={d.problems.length ? 'accent' : 'ok'}>{d.problems.length ? 'needs attention' : 'ready'}</Badge> : null}
          </div>
          {d.problems.map((p) => (
            <p key={p} className="rounded-[10px] bg-accent-soft px-3 py-2 text-[12.5px] leading-relaxed text-accent-text">
              {p}
            </p>
          ))}
          {d.installed ? (
            <>
              <label className="flex items-center gap-2 text-[13px] text-muted">
                <input type="checkbox" checked={d.autoUpdate} disabled={busy !== ''} onChange={(e) => void configure({ autoUpdate: e.target.checked })} className="h-4 w-4 accent-[var(--accent)]" />
                Keep the downloader up to date by itself (about once a week, when nothing is importing)
              </label>
              <p className="text-[12px] leading-relaxed text-subtle">
                It only ever updates the downloader itself. Other programs it uses, like Python, change only when you choose Update now.
              </p>
              {d.lastResult ? <p className="text-[12px] text-subtle">Last check {d.lastCheck ? relative(d.lastCheck) : ''}: {d.lastResult}</p> : null}
              <div className="flex flex-wrap items-center gap-2">
                <Button size="sm" variant="secondary" loading={busy === 'update'} disabled={busy !== '' && busy !== 'update'} onClick={() => void update()}>
                  Update now
                </Button>
                {d.browser ? (
                  <Button size="sm" variant="ghost" disabled={busy !== ''} onClick={() => void configure({ forgetBrowser: true })}>
                    Forget the {BROWSERS.find((b) => b.id === d.browser)?.label ?? d.browser} sign-in choice
                  </Button>
                ) : null}
              </div>
              {d.managedBy === 'homebrew' ? <p className="text-[12px] text-subtle">Update now uses Homebrew, which may also update Python and Deno for it.</p> : null}
            </>
          ) : (
            <div className="text-[12.5px] text-muted">
              <p>Install the downloader with Homebrew (Claude Code can do it for you), or save videos with the browser extension below.</p>
              <Details compact className="mt-1.5">
                <span className="tc">brew install yt-dlp</span>
              </Details>
            </div>
          )}
        </div>
      ) : null}
    </section>
  );
}

/**
 * Settings → Browser extension (its own section, D191): where it is, and
 * browsers people paired (D197): everyone pairs and removes their own (Your
 * browsers); owners see and remove everyone's (Everyone's browsers).
 */
/** One paired browser: whose (in Everyone's browsers), when paired and used, and Remove. */
function BrowserRow({ b, person, stopped, onRemove }: { b: ExtensionBrowser; person?: string; stopped?: string; onRemove: () => void }) {
  return (
    <li className="flex items-center gap-3 px-3 py-2.5" data-browser={b.name}>
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] text-fg">
          {b.name}
          {person ? (
            <span className="text-muted">
              {' '}
              · <Theirs>{person}</Theirs>
            </span>
          ) : null}
        </div>
        <div className="text-[11.5px] text-subtle">
          Paired {relative(b.pairedAt)}, {b.lastUsedAt ? `last used ${relative(b.lastUsedAt)}` : 'not used yet'}, {b.sent} sent
        </div>
        {stopped ? <div className="text-[11.5px] text-danger">{stopped}</div> : null}
      </div>
      <Button size="sm" variant="ghost" onClick={onRemove} aria-label={person ? `Remove ${person}'s ${b.name}` : `Remove ${b.name}`}>
        Remove
      </Button>
    </li>
  );
}

export function BrowserExtensionSettings() {
  const st = useOp('extension.status', {}, { staleTime: 30_000 });
  const host = useHost();
  const qc = useQueryClient();
  const dialog = useDialog();
  const toast = useToast();
  const [busy, setBusy] = useState('');
  const d = st.data;
  const refresh = () => void qc.invalidateQueries({ queryKey: ['extension.status'] });
  const pair = async () => {
    setBusy('pair');
    try {
      const p = await call('extension.pair', {});
      await dialog.open<void>(
        (close) => (
          <div>
            <p className="text-[13.5px] text-muted">
              Type this code into the “Send to MetaRush” extension and choose Pair. It works once, for 10 minutes. What that browser sends is yours{p.for ? <> (<Theirs>{p.for}</Theirs>)</> : null}.
            </p>
            <div className="tc my-5 select-all text-center text-[32px] font-semibold tracking-[0.12em] text-fg" aria-label="Pairing code">
              {p.code}
            </div>
            <ol className="list-decimal space-y-1 pl-5 text-[12.5px] leading-relaxed text-muted">
              {p.steps.map((s) => (
                <li key={s}>{s}</li>
              ))}
            </ol>
            <div className="mt-5 flex justify-end border-t border-line pt-4">
              <Button variant="primary" onClick={() => close()}>
                Done
              </Button>
            </div>
          </div>
        ),
        { title: 'Pair a browser', size: 'md' },
      );
    } catch (e) {
      toast({ tone: 'error', title: 'Could not make a pairing code', body: toApiError(e).message });
    } finally {
      setBusy('');
      refresh();
    }
  };
  const unpair = async (b: ExtensionBrowser, whose?: string) => {
    const ok = await dialog.confirm({
      title: whose ? `Remove ${whose}'s ${b.name}?` : `Remove ${b.name}?`,
      body: 'It stops being able to send videos and pages to MetaRush right away. It can be paired again with a new code.',
      confirmLabel: 'Remove',
      tone: 'danger',
    });
    if (!ok) return;
    try {
      await call('extension.revoke', { id: b.id });
      toast({ tone: 'success', title: `${b.name} is removed` });
    } catch (e) {
      toast({ tone: 'error', title: 'Could not remove it', body: toApiError(e).message });
    } finally {
      refresh();
    }
  };
  const others = (d?.everyone ?? []).filter((b) => !b.mine);
  return (
    <section id="browser-extension" className="panel scroll-mt-28 p-5" aria-labelledby="ext-h">
      <h2 id="ext-h" className="mb-1 flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <PuzzlePiece size={17} />
        </span>
        Send to MetaRush
      </h2>
      <p className="mb-4 text-[12.5px] leading-relaxed text-muted">
        “Send to MetaRush” adds a button to YouTube, TikTok and X video pages. It saves the video in your own browser, signed in as you, and sends it here. A paired browser can only send videos and pages; it can't see your projects or clips.
      </p>
      {st.isLoading ? (
        <Skeleton className="h-16" />
      ) : st.error ? (
        <ErrorPanel compact error={st.error} onRetry={() => void st.refetch()} />
      ) : d ? (
        <div className="space-y-4 text-[13px]">
          {host.remote ? (
            <div data-extension-remote>
              <div className="mb-1 text-[12px] font-medium text-muted">Install it in Chrome, Brave or Edge on this computer</div>
              <ol className="list-decimal space-y-1 pl-5 text-[12.5px] leading-relaxed text-muted">
                <li>Download the extension and unzip it (its folder on {host.machine} isn't on this computer).</li>
                <li>Open chrome://extensions, turn on Developer mode, choose Load unpacked and pick the unzipped folder.</li>
                <li>In the extension, open “MetaRush address” and use the address below, then pair it with a code.</li>
              </ol>
              <div className="mt-1.5 flex flex-wrap items-center gap-2 rounded-[10px] bg-panel px-3 py-2 hairline">
                <span className="min-w-0 flex-1 truncate text-[12.5px] text-muted" title={d.address}>
                  {d.address}
                </span>
                <CopyButton text={d.address} label="Copy the address" />
                <a href="/api/extension/download" download className="inline-flex h-7 items-center gap-1.5 rounded-[8px] bg-panel px-2.5 text-[12.5px] text-fg hairline hover:bg-hover">
                  Download the extension
                </a>
              </div>
            </div>
          ) : (
            <div>
              <div className="mb-1 text-[12px] font-medium text-muted">Install it in Chrome, Brave or Edge</div>
              <ol className="list-decimal space-y-1 pl-5 text-[12.5px] leading-relaxed text-muted">
                <li>Open chrome://extensions and turn on Developer mode.</li>
                <li>Choose Load unpacked, then pick MetaRush's extension folder: copy its location below and paste it into the folder window (on a Mac, press ⌘⇧G first).</li>
              </ol>
              <div className="mt-1.5 flex items-center gap-2 rounded-[10px] bg-panel px-3 py-2 hairline">
                <span className="min-w-0 flex-1 text-[12.5px] text-muted">The extension's folder</span>
                <CopyButton text={d.folder} label="Copy its location" />
              </div>
              <Details compact className="mt-1.5">
                <span className="tc break-all">{d.folder}</span>
              </Details>
            </div>
          )}
          {/* A pairing is its person's (D197): everyone pairs and removes their own; owners remove anyone's. */}
          <div data-your-browsers>
            <div className="mb-1.5 flex items-center gap-2">
              <span className="text-[12px] font-medium text-muted">Your browsers</span>
              <Button size="sm" variant="primary" className="ml-auto" loading={busy === 'pair'} onClick={() => void pair()}>
                Pair a browser
              </Button>
            </div>
            <p className="mb-2 text-[12px] leading-relaxed text-subtle">What a browser you pair sends is yours, like anything else you add.</p>
            {d.browsers.length === 0 ? (
              <p className="text-[12.5px] text-subtle">None yet.{d.codeWaiting && d.codeExpiresAt ? ` Your pairing code is waiting until ${new Date(d.codeExpiresAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}.` : ''}</p>
            ) : (
              <ul className="divide-y divide-line overflow-hidden rounded-[12px] hairline" aria-label="Your browsers">
                {d.browsers.map((b) => (
                  <BrowserRow key={b.id} b={b} onRemove={() => void unpair(b)} />
                ))}
              </ul>
            )}
          </div>
          {d.owner && others.length > 0 ? (
            <div data-everyones-browsers>
              <div className="mb-1.5 text-[12px] font-medium text-muted">Everyone's browsers</div>
              <ul className="divide-y divide-line overflow-hidden rounded-[12px] hairline" aria-label="Everyone's browsers">
                {others.map((b) => (
                  <BrowserRow key={b.id} b={b} person={b.person} stopped={b.stopped} onRemove={() => void unpair(b, b.person)} />
                ))}
              </ul>
            </div>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
