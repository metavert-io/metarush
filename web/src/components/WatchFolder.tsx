import { useEffect, useId, useState } from 'react';
import { Link } from 'react-router';
import { ArrowUp, Eye, EyeSlash, Folder, FolderOpen, FolderSimplePlus, FolderUser, MagnifyingGlass, WarningCircle } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call, request, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import type { BrowseOut, WatchChooseOut, WatchRevealOut, WatchStatusOut } from '../api/types';
import { plural, relative } from '../lib/format';
import { useNow } from '../lib/hooks';
import { useHost } from '../lib/host';
import { useDialog } from '../ui/Dialog';
import { Button, cx, ErrorPanel, Skeleton, Spinner } from '../ui/primitives';
import { useToast } from '../ui/Toast';
import { ownerTitle, useOwnership } from './OwnerOnly';

export function useWatchStatus() {
  return useOp('watch.status', {}, { refetchInterval: (q) => (q.state.data?.state === 'watching' ? 10_000 : 30_000) });
}

function useInvalidateWatch() {
  const qc = useQueryClient();
  return () => {
    for (const k of ['watch.status', 'status', 'asset.list', 'activity.summary']) void qc.invalidateQueries({ queryKey: [k] });
  };
}

/** Opens the folder in Finder through this machine's daemon. */
export function useRevealFolder() {
  const toast = useToast();
  return async (dir?: string) => {
    try {
      await request<WatchRevealOut>('POST', '/api/watch/reveal', { json: dir ? { dir } : {} });
    } catch (e) {
      toast({ tone: 'error', title: 'Could not open the folder', body: toApiError(e).message });
    }
  };
}

/* ---------- status line in the Inbox header ---------- */

export function WatchStrip({ onConfigure }: { onConfigure: () => void }) {
  const st = useWatchStatus();
  const host = useHost();
  const own = useOwnership(); // the watched folder is this computer's setting: owners change it (D191)
  const now = useNow(5000);
  const d = st.data;
  if (st.isLoading) return <Skeleton className="h-11 rounded-[12px]" />;
  if (!d) return null;
  const live = d.live;
  const shell = 'flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-[12px] px-3.5 py-2.5 text-[12.5px] hairline';
  if (d.state === 'not_configured')
    return (
      <div className={cx(shell, 'bg-raised')}>
        <FolderSimplePlus size={16} className="shrink-0 text-accent-text" />
        <span className="min-w-0 flex-1 text-muted">
          <span className="font-medium text-fg">Add footage automatically.</span> Pick a folder; anything you put in it shows up here once it has finished copying.
        </span>
        <Button size="sm" variant="primary" onClick={onConfigure} disabled={!own.owner} title={ownerTitle(own, 'set up the watched folder')}>
          Set up watched folder
        </Button>
      </div>
    );
  const icon =
    d.state === 'watching' ? <FolderOpen size={16} className="shrink-0 text-ok" /> : d.state === 'missing' ? <WarningCircle size={16} className="shrink-0 text-danger" /> : <Folder size={16} className="shrink-0 text-subtle" />;
  const headline =
    d.state === 'watching' ? (host.remote ? `Watching on ${host.machine}` : 'Watching') : d.state === 'off' ? 'Watched folder is off:' : d.state === 'missing' ? 'Folder not found:' : `Not watching ${host.onHere}:`;
  return (
    <div className={cx(shell, d.state === 'missing' ? 'bg-danger-soft' : 'bg-raised')}>
      {icon}
      <span className="min-w-0 flex-1">
        <span className="text-muted">{headline}</span>{' '}
        <button
          type="button"
          title={`${d.dir} (click to copy)`}
          onClick={() => void navigator.clipboard?.writeText(d.dir ?? '').catch(() => undefined)}
          className="tc max-w-full truncate align-bottom text-[12px] font-medium text-fg hover:underline"
        >
          {d.displayDir}
        </button>
        {d.projectName ? <span className="text-muted">, into {d.projectName}</span> : null}
        <span className="mt-0.5 block text-[11.5px] text-subtle">
          {d.state === 'watching' && live ? (
            <>
              {live.lastScan ? `Checked ${relative(live.lastScan, now)}` : 'Starting'}
              {live.ingesting ? `, adding ${live.ingesting}` : ''}
              {live.pending.length ? `, ${plural(live.pending.length, 'file')} still copying` : ''}
              {live.lastIngestName ? `, last added ${live.lastIngestTitle || live.lastIngestName} ${live.lastIngestAt ? relative(live.lastIngestAt, now) : ''}` : ''}
            </>
          ) : d.state === 'missing' ? (
            'If it is on an external drive, plug it in. Watching resumes on its own.'
          ) : d.state === 'off' ? (
            'Files you put there are not being added.'
          ) : (
            d.note
          )}
        </span>
        {live?.errors?.length ? <span className="mt-1 block truncate text-[11.5px] text-danger" title={live.errors.join('\n')}>Could not add {live.errors[0]}</span> : null}
      </span>
      <span className="flex shrink-0 items-center gap-1.5">
        <Link to="/settings/footage#watched-folder" className="rounded-[8px] px-2 py-1 text-[12px] font-medium text-muted hover:bg-hover hover:text-fg">
          Settings
        </Link>
        <Button size="sm" variant="secondary" onClick={onConfigure} disabled={!own.owner} title={ownerTitle(own, 'change the watched folder')}>
          {d.state === 'off' ? 'Turn on' : d.state === 'missing' ? 'Change folder' : 'Configure'}
        </Button>
      </span>
    </div>
  );
}

/* ---------- folder browser (home folder only) ---------- */

function FolderBrowser({ start, close }: { start?: string; close: (v?: { path: string; display: string }) => void }) {
  const [path, setPath] = useState<string | undefined>(start);
  const [hidden, setHidden] = useState(false);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');
  const [mkErr, setMkErr] = useState<string | null>(null);
  const qc = useQueryClient();
  const list = useOp('watch.browse', { ...(path ? { path } : {}), hidden });
  const d: BrowseOut | undefined = list.data;
  const inputId = useId();

  const make = async () => {
    if (!d || !name.trim()) return;
    setMkErr(null);
    try {
      const e = await call('watch.mkdir', { parent: d.path, name: name.trim() });
      setCreating(false);
      setName('');
      void qc.invalidateQueries({ queryKey: ['watch.browse'] });
      setPath(e.path);
    } catch (err) {
      setMkErr(toApiError(err).message);
    }
  };

  return (
    <div>
      <div className="flex items-center gap-2">
        <Button size="sm" variant="secondary" icon={<ArrowUp size={13} />} disabled={!d?.parent} onClick={() => d?.parent && setPath(d.parent)} aria-label="Up one folder">
          Up
        </Button>
        <div className="tc min-w-0 flex-1 truncate rounded-[8px] bg-panel px-2.5 py-1.5 text-[12px] text-fg hairline" title={d?.path}>
          {d?.display ?? '…'}
        </div>
        <button
          type="button"
          aria-pressed={hidden}
          onClick={() => setHidden(!hidden)}
          title={hidden ? 'Hide hidden folders' : 'Show hidden folders'}
          aria-label={hidden ? 'Hide hidden folders' : 'Show hidden folders'}
          className="rounded-[8px] p-1.5 text-subtle hover:bg-hover hover:text-fg"
        >
          {hidden ? <EyeSlash size={15} /> : <Eye size={15} />}
        </button>
      </div>
      <ul className="mt-3 h-[min(46dvh,340px)] overflow-y-auto rounded-[12px] p-1 hairline" aria-label="Folders">
        {list.isLoading ? (
          <li className="flex items-center justify-center gap-2 py-10 text-[13px] text-muted">
            <Spinner size={13} /> Loading folders
          </li>
        ) : list.error ? (
          <li className="p-2">
            <ErrorPanel compact error={list.error} onRetry={() => void list.refetch()} />
          </li>
        ) : d && d.entries.length === 0 ? (
          <li className="px-3 py-10 text-center text-[12.5px] text-muted">No folders in here. You can create one below, or choose this folder.</li>
        ) : (
          d?.entries.map((e) => (
            <li key={e.path}>
              <button
                type="button"
                onClick={() => setPath(e.path)}
                className={cx('flex w-full items-center gap-2.5 rounded-[9px] px-2.5 py-2 text-left text-[13px] hover:bg-hover', e.hidden && 'opacity-70')}
                title={e.blocked || e.display}
              >
                {e.name === 'Movies' || e.name === 'Videos' ? <FolderUser size={16} className="text-accent-text" /> : <Folder size={16} className="text-subtle" />}
                <span className="min-w-0 flex-1 truncate text-fg">{e.name}</span>
                {e.blocked ? <span className="shrink-0 text-[11px] text-subtle">MetaRush files</span> : null}
              </button>
            </li>
          ))
        )}
      </ul>
      {creating ? (
        <form
          className="mt-3 flex items-start gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            void make();
          }}
        >
          <div className="min-w-0 flex-1">
            <label htmlFor={inputId} className="sr-only">
              New folder name
            </label>
            <input
              id={inputId}
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="MetaRush Inbox"
              className="h-9 w-full rounded-[9px] bg-panel px-3 text-[13px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-accent focus:outline-offset-0"
            />
            {mkErr ? <div className="mt-1 text-[12px] text-danger">{mkErr}</div> : null}
          </div>
          <Button size="md" type="submit" variant="secondary" disabled={!name.trim()}>
            Create
          </Button>
          <Button size="md" variant="ghost" onClick={() => setCreating(false)}>
            Cancel
          </Button>
        </form>
      ) : null}
      {d?.blocked ? <div className="mt-3 rounded-[10px] bg-danger-soft px-3 py-2 text-[12.5px] text-danger">{d.blocked}</div> : null}
      <div className="mt-5 flex flex-wrap items-center justify-end gap-2">
        {!creating ? (
          <Button variant="ghost" className="mr-auto" icon={<FolderSimplePlus size={15} />} onClick={() => setCreating(true)} disabled={!d}>
            New folder
          </Button>
        ) : null}
        <Button variant="ghost" onClick={() => close()}>
          Cancel
        </Button>
        <Button variant="primary" disabled={!d || !!d.blocked} onClick={() => d && close({ path: d.path, display: d.display })}>
          Choose this folder
        </Button>
      </div>
    </div>
  );
}

/* ---------- configure dialog ---------- */

function ConfigureBody({ close }: { close: (v?: true) => void }) {
  const st = useWatchStatus();
  const host = useHost();
  const projects = useOp('project.list', {});
  const dialog = useDialog();
  const toast = useToast();
  const reveal = useRevealFolder();
  const invalidate = useInvalidateWatch();
  const d = st.data;
  const [dir, setDir] = useState<{ path: string; display: string } | null>(null);
  const [typed, setTyped] = useState('');
  const [enabled, setEnabled] = useState(true);
  const [projectId, setProjectId] = useState('');
  const [move, setMove] = useState(false);
  const [stable, setStable] = useState(6);
  const [existing, setExisting] = useState<'import' | 'skip'>('import');
  const [files, setFiles] = useState<number | null>(null);
  const [choosing, setChoosing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const ids = { enabled: useId(), project: useId(), move: useId(), stable: useId(), path: useId() };

  useEffect(() => {
    if (!d) return;
    setDir(d.dir ? { path: d.dir, display: d.displayDir ?? d.dir } : { path: d.suggested, display: d.suggestedDisplay });
    setTyped(d.dir ? d.displayDir ?? d.dir : d.suggestedDisplay);
    setEnabled(d.state === 'not_configured' ? true : d.enabled || d.state === 'off');
    setProjectId(d.projectId ?? '');
    setMove(d.moveProcessed);
    setStable(d.stableSeconds || 6);
  }, [d]);

  if (st.isLoading || !d || !dir)
    return (
      <div className="space-y-3">
        <Skeleton className="h-10" />
        <Skeleton className="h-10" />
        <Skeleton className="h-10" />
      </div>
    );

  const isNew = dir.path !== d.dir;
  // From another computer the native picker would open on the other screen: browse instead.
  const native = d.canChoose && !host.remote;
  const choose = async () => {
    setChoosing(true);
    setErr(null);
    try {
      const out = await request<WatchChooseOut>('POST', '/api/watch/choose', { json: { start: dir.path } });
      if (!out.canceled && out.path) {
        if (out.blocked) setErr(new Error(out.blocked));
        else {
          setDir({ path: out.path, display: out.display ?? out.path });
          setTyped(out.display ?? out.path);
          setFiles(out.files);
        }
      }
    } catch (e) {
      setErr(e);
    } finally {
      setChoosing(false);
    }
  };
  const browse = async () => {
    const out = await dialog.open<{ path: string; display: string }>((c) => <FolderBrowser start={dir.path} close={c} />, { title: host.remote ? `Choose a folder on ${host.machine}` : 'Choose a folder', size: 'md' });
    if (out) {
      setDir(out);
      setTyped(out.display);
      setFiles(null);
    }
  };
  const save = async () => {
    setSaving(true);
    setErr(null);
    const path = native ? dir.path : typed.trim() || dir.path;
    try {
      if (!enabled && !isNew && d.dir) {
        await call('watch.disable', {});
      } else {
        await call('watch.set', {
          dir: path,
          enabled,
          projectId,
          moveProcessed: move,
          stableSeconds: Math.max(2, Math.min(600, Math.round(stable) || 6)),
          create: path === d.suggested || dir.path === d.suggested,
          existing,
        });
      }
      invalidate();
      toast({ tone: 'success', title: enabled ? 'Watching the folder' : 'Watched folder turned off', body: enabled ? 'New files are added once they finish copying.' : undefined });
      close(true);
    } catch (e) {
      setErr(e);
    } finally {
      setSaving(false);
    }
  };

  const field = 'h-9 rounded-[9px] bg-panel px-3 text-[13px] text-fg hairline focus:outline-2 focus:outline-accent focus:outline-offset-0';
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
      className="space-y-5"
    >
      <div>
        <div className="text-[12.5px] font-medium text-muted">{host.remote ? `Folder on ${host.machine}` : 'Folder'}</div>
        {native ? (
          <div className="mt-1.5 flex flex-wrap items-center gap-2">
            <div className="tc min-w-0 flex-1 truncate rounded-[9px] bg-panel px-3 py-2 text-[12.5px] text-fg hairline" title={dir.path}>
              {dir.display}
            </div>
            <Button variant="secondary" loading={choosing} onClick={() => void choose()}>
              Change…
            </Button>
          </div>
        ) : (
          <div className="mt-1.5 flex flex-wrap items-center gap-2">
            <label htmlFor={ids.path} className="sr-only">
              Folder path
            </label>
            <input id={ids.path} value={typed} onChange={(e) => setTyped(e.target.value)} className={cx(field, 'tc min-w-0 flex-1')} placeholder="~/Videos/MetaRush Inbox" />
          </div>
        )}
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[12px]">
          <button type="button" onClick={() => void browse()} className="inline-flex items-center gap-1 font-medium text-accent-text hover:underline">
            <MagnifyingGlass size={12} /> {host.remote ? `Browse folders on ${host.machine}` : 'Browse folders'}
          </button>
          {dir.path !== d.suggested ? (
            <button
              type="button"
              onClick={() => {
                setDir({ path: d.suggested, display: d.suggestedDisplay });
                setTyped(d.suggestedDisplay);
                setFiles(null);
              }}
              className="font-medium text-accent-text hover:underline"
            >
              Use {d.suggestedDisplay}
            </button>
          ) : (
            <span className="text-subtle">Suggested folder. It is created when you save.</span>
          )}
          {d.canReveal && d.dir && !isNew ? (
            <button type="button" onClick={() => void reveal()} className="font-medium text-accent-text hover:underline" data-host-action>
              {host.remote ? `Open in Finder on ${host.machine}` : 'Open in Finder'}
            </button>
          ) : null}
        </div>
      </div>

      <label htmlFor={ids.enabled} className="flex items-center justify-between gap-4 rounded-[12px] px-3.5 py-3 hairline">
        <span>
          <span className="block text-[13.5px] font-medium text-fg">Watch this folder</span>
          <span className="block text-[12px] text-muted">Files you put in it are added once they stop changing.</span>
        </span>
        <Toggle id={ids.enabled} checked={enabled} onChange={setEnabled} />
      </label>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div>
          <label htmlFor={ids.project} className="text-[12.5px] font-medium text-muted">
            Add files to
          </label>
          <select id={ids.project} value={projectId} onChange={(e) => setProjectId(e.target.value)} className={cx(field, 'mt-1.5 w-full')}>
            <option value="">Inbox</option>
            {(projects.data?.projects ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor={ids.stable} className="text-[12.5px] font-medium text-muted">
            Wait until a file stops changing for
          </label>
          <div className="mt-1.5 flex items-center gap-2">
            <input id={ids.stable} type="number" min={2} max={600} value={stable} onChange={(e) => setStable(Number(e.target.value))} className={cx(field, 'tc w-24')} />
            <span className="text-[12.5px] text-muted">seconds</span>
          </div>
        </div>
      </div>

      <label htmlFor={ids.move} className="flex items-center justify-between gap-4 rounded-[12px] px-3.5 py-3 hairline">
        <span>
          <span className="block text-[13.5px] font-medium text-fg">Tidy up after adding</span>
          <span className="block text-[12px] text-muted">Move added files into an “Ingested” folder inside it. Nothing is ever deleted.</span>
        </span>
        <Toggle id={ids.move} checked={move} onChange={setMove} />
      </label>

      {enabled && (isNew || d.state === 'not_configured' || d.state === 'off') ? (
        <fieldset className="rounded-[12px] px-3.5 py-3 hairline">
          <legend className="px-1 text-[12.5px] font-medium text-muted">Files already in the folder{files != null ? ` (${files})` : ''}</legend>
          <div className="mt-1 space-y-2">
            {(
              [
                ['import', 'Add them too', 'Everything already there is added, then new files as they arrive.'],
                ['skip', 'Only new files', 'Leave what is there now; you can add it later from Settings → Footage.'],
              ] as const
            ).map(([v, title, body]) => (
              <label key={v} className="flex cursor-pointer items-start gap-2.5">
                <input type="radio" name="existing" value={v} checked={existing === v} onChange={() => setExisting(v)} className="mt-1 accent-[var(--accent)]" />
                <span>
                  <span className="block text-[13px] font-medium text-fg">{title}</span>
                  <span className="block text-[12px] text-muted">{body}</span>
                </span>
              </label>
            ))}
          </div>
        </fieldset>
      ) : null}

      {err ? <ErrorPanel compact error={err} /> : null}
      <div className="flex flex-wrap justify-end gap-2 border-t border-line pt-4">
        <Button variant="ghost" onClick={() => close()}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" loading={saving}>
          Save
        </Button>
      </div>
    </form>
  );
}

export function useConfigureWatch() {
  const dialog = useDialog();
  return () => dialog.open<true>((close) => <ConfigureBody close={close} />, { title: 'Watched folder', size: 'md' });
}

export function Toggle({ id, checked, onChange, disabled, label }: { id?: string; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; label?: string }) {
  return (
    <button
      id={id}
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={(e) => {
        e.preventDefault();
        onChange(!checked);
      }}
      className={cx('relative h-6 w-11 shrink-0 rounded-full transition-colors duration-200 disabled:opacity-50', checked ? 'bg-accent' : 'bg-line-strong')}
    >
      <span aria-hidden className={cx('absolute left-0 top-0.5 h-5 w-5 rounded-full bg-white shadow transition-transform duration-200 ease-[var(--ease-snap)]', checked ? 'translate-x-[22px]' : 'translate-x-0.5')} />
    </button>
  );
}

/* ---------- Settings section ---------- */

export function WatchSettings() {
  const st = useWatchStatus();
  const host = useHost();
  const configure = useConfigureWatch();
  const reveal = useRevealFolder();
  const invalidate = useInvalidateWatch();
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const d: WatchStatusOut | undefined = st.data;
  const run = async (key: string, fn: () => Promise<unknown>, ok?: string) => {
    setBusy(key);
    try {
      await fn();
      invalidate();
      if (ok) toast({ tone: 'success', title: ok });
    } catch (e) {
      toast({ tone: 'error', title: 'That did not work', body: toApiError(e).message });
    } finally {
      setBusy(null);
    }
  };
  if (st.isLoading || !d)
    return (
      <div className="space-y-2">
        <Skeleton className="h-6" />
        <Skeleton className="h-6" />
      </div>
    );
  const stateLabel = { watching: host.remote ? `Watching on ${host.machine}` : 'Watching', off: 'Off', missing: 'Folder not found', not_configured: 'Not set up', not_running: `Not watching ${host.onHere}` }[d.state] ?? d.state;
  const tone = d.state === 'watching' ? 'text-ok' : d.state === 'missing' ? 'text-danger' : 'text-muted';
  const choose = async () => {
    setBusy('choose');
    try {
      const out = await request<WatchChooseOut>('POST', '/api/watch/choose', { json: {} });
      if (!out.canceled && out.path) {
        if (out.blocked) toast({ tone: 'error', title: 'That folder cannot be watched', body: out.blocked });
        else {
          await call('watch.set', { dir: out.path });
          invalidate();
          toast({ tone: 'success', title: `Watching ${out.display}` });
        }
      }
    } catch (e) {
      const err = toApiError(e);
      if ((err.details as { reason?: string } | undefined)?.reason === 'unsupported') void configure();
      else toast({ tone: 'error', title: 'Could not change the folder', body: err.message });
    } finally {
      setBusy(null);
    }
  };
  return (
    <div>
      <div className="flex flex-wrap items-center gap-3">
        <div className="min-w-0 flex-1">
          <div className="tc truncate text-[13px] text-fg" title={d.dir || d.suggested}>
            {d.dir ? d.displayDir : <span className="text-subtle">No folder yet (suggested: {d.suggestedDisplay})</span>}
          </div>
          <div className={cx('mt-0.5 text-[12px] font-medium', tone)}>
            {stateLabel}
            {d.state === 'watching' && d.live?.lastScan ? <span className="font-normal text-subtle">, checked {relative(d.live.lastScan)}</span> : null}
            {d.projectName ? <span className="font-normal text-subtle">, adds to {d.projectName}</span> : null}
          </div>
        </div>
        {d.dir ? (
          <Toggle
            checked={d.enabled}
            label="Watch the folder"
            disabled={!!busy}
            onChange={(on) => void run('toggle', () => (on ? call('watch.set', { dir: d.dir! }) : call('watch.disable', {})), on ? 'Watching the folder' : 'Watched folder turned off')}
          />
        ) : null}
      </div>
      <div className="mt-4 flex flex-wrap gap-2">
        <Button size="sm" variant="secondary" loading={busy === 'choose'} onClick={() => void (d.canChoose && !host.remote ? choose() : configure())}>
          {d.dir ? 'Change…' : 'Choose folder…'}
        </Button>
        {d.canReveal && d.dir && d.state !== 'missing' ? (
          <Button size="sm" variant="ghost" onClick={() => void reveal()} data-host-action>
            {host.remote ? `Open in Finder on ${host.machine}` : 'Open in Finder'}
          </Button>
        ) : null}
        {d.dir !== d.suggested ? (
          <Button size="sm" variant="ghost" loading={busy === 'default'} onClick={() => void run('default', () => call('watch.set', { dir: d.suggested, create: true }), `Watching ${d.suggestedDisplay}`)}>
            Use default
          </Button>
        ) : null}
        <Button
          size="sm"
          variant="ghost"
          disabled={!d.dir || d.skipped === 0}
          title={d.skipped ? undefined : 'Nothing was left out: every file in the folder is added.'}
          loading={busy === 'import'}
          onClick={() => void run('import', () => call('watch.import', {}), `Adding ${plural(d.skipped, 'file')} that were already there`)}
        >
          Import what's already there{d.skipped ? ` (${d.skipped})` : ''}
        </Button>
        <Button size="sm" variant="quiet" className="ml-auto" onClick={() => void configure()}>
          More options
        </Button>
      </div>
      {d.live?.errors?.length ? <div className="mt-3 rounded-[10px] bg-danger-soft px-3 py-2 text-[12px] text-danger">Could not add {d.live.errors[0]}</div> : null}
    </div>
  );
}
