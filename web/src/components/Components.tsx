import { useEffect, useState } from 'react';
import { CheckCircle, Circle, DownloadSimple, HandPointing, Package, Trash, WarningCircle } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../api/client';
import { useOp } from '../api/queries';
import type { ComponentState } from '../api/types';
import { Badge, Button, cx, Details, ErrorPanel, Freshness, Skeleton } from '../ui/primitives';
import { useDialog } from '../ui/Dialog';
import { useToast } from '../ui/Toast';
import { ownerTitle, OwnersNote, useOwnership } from './OwnerOnly';

/*
 * MetaRush's components (D324–D327): what MetaRush needs to run (itself, a
 * place for projects, a storage folder) and the optional parts that unlock
 * features — FFmpeg, the Remotion renderer, transcription, the link
 * downloader… — each with what it adds, its size, whether it is here and
 * Add it. The same op (components.list) feeds `metarush components`, and
 * Home's Getting started offers Add it with useAddComponent. Adding or
 * removing installs software on this computer: people only and owners only,
 * never Claude or Auto (the op refuses them anyway).
 */

/** Queries the add and remove change. */
const AFTER = ['components.list', 'setup.status', 'worker.list', 'activity.summary'] as const;

/** Add a component: a confirm with its size, time and licence, then the add in the background, then a toast when it is ready. */
export function useAddComponent(): { add: (c: ComponentState) => Promise<void>; busy: string | null } {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const [busy, setBusy] = useState<string | null>(null);
  const refresh = () => {
    for (const k of AFTER) void qc.invalidateQueries({ queryKey: [k] });
  };
  const add = async (c: ComponentState) => {
    if (c.how === 'yourself') {
      await dialog.alert({
        title: `Adding ${c.title} is up to you on this computer`,
        body: (
          <div className="space-y-2 text-[13px]">
            <p>{c.yourself}</p>
            {c.command ? (
              <Details compact>
                <code className="tc block whitespace-pre-wrap break-all text-[12px]">{c.command}</code>
              </Details>
            ) : null}
          </div>
        ),
      });
      return;
    }
    const ok = await dialog.confirm({
      title: `Add ${c.title}?`,
      body: (
        <div className="space-y-2 text-[13px] leading-relaxed" data-component-confirm={c.name}>
          <p>{c.unlocks}</p>
          <p className="text-muted">
            It downloads {c.size.replace(/^about /, 'about ')} and usually takes {c.time}.{' '}
            {c.how === 'homebrew' ? 'Homebrew installs it.' : c.how === 'download' ? 'MetaRush downloads it and checks it before using it.' : 'MetaRush sets it up.'}
          </p>
          {c.needs?.length ? <p className="text-muted">What it needs first is added with it.</p> : null}
          {c.licence ? (
            <p className="rounded-[10px] bg-hover/60 px-3 py-2 text-[12.5px] text-fg" data-licence>
              {c.licence}
            </p>
          ) : null}
        </div>
      ),
      confirmLabel: 'Add it',
    });
    if (!ok) return;
    setBusy(c.name);
    try {
      const out = await call('components.add', { name: c.name, background: true });
      refresh();
      if (out.component.ready) {
        toast({ tone: 'success', title: `${c.title} is added` });
        return;
      }
      toast({ tone: 'info', title: `Adding ${c.title}`, body: 'It shows here when it is ready; you can leave this page.' });
      // Ask again until it is ready, or the add stops.
      for (let i = 0; i < 600; i++) {
        await new Promise((r) => setTimeout(r, 2000));
        const l = await call('components.list', { name: c.name });
        qc.setQueryData(['components.list', { name: c.name }], l);
        const st = l.components[0];
        if (!st) break;
        if (st.ready) {
          refresh();
          toast({ tone: 'success', title: `${c.title} is added`, body: st.licence ?? undefined });
          return;
        }
        if (st.status !== 'adding' && i > 1) {
          refresh();
          toast({ tone: 'error', title: `${c.title} couldn't be added`, body: st.lastError ?? st.says });
          return;
        }
      }
    } catch (e) {
      toast({ tone: 'error', title: `${c.title} couldn't be added`, body: toApiError(e).message });
    } finally {
      setBusy(null);
      refresh();
    }
  };
  return { add, busy };
}

function Mark({ s }: { s: ComponentState }) {
  if (s.ready) return <CheckCircle size={17} weight="fill" className="mt-0.5 shrink-0 text-ok" aria-label="On this computer" />;
  if (s.status === 'failed') return <WarningCircle size={17} className="mt-0.5 shrink-0 text-danger" aria-label="Didn't work" />;
  if (s.status === 'needs-you') return <HandPointing size={17} className="mt-0.5 shrink-0 text-subtle" aria-label="Up to you" />;
  return <Circle size={17} className="mt-0.5 shrink-0 text-subtle" aria-label="Not here yet" />;
}

/** The Add it button for one component (owners; the fieldset of OwnerOnly or the title says why not). */
export function AddComponentButton({ c, add, busy, size = 'sm' }: { c: ComponentState; add: (c: ComponentState) => Promise<void>; busy: string | null; size?: 'sm' | 'md' }) {
  const own = useOwnership();
  const adding = busy === c.name || c.status === 'adding';
  return (
    <Button
      size={size}
      variant={c.suggested ? 'primary' : 'secondary'}
      icon={<DownloadSimple size={13} />}
      loading={adding}
      disabled={!own.owner || (busy !== null && busy !== c.name)}
      title={ownerTitle(own, 'add programs to this computer')}
      onClick={() => void add(c)}
      data-add-component={c.name}
    >
      {adding ? 'Adding…' : c.how === 'yourself' ? 'How to add it' : 'Add it'}
    </Button>
  );
}

function Row({ s, add, busy, onRemove }: { s: ComponentState; add: (c: ComponentState) => Promise<void>; busy: string | null; onRemove: (c: ComponentState) => void }) {
  const own = useOwnership();
  return (
    <li className="flex items-start gap-3 py-3" data-component={s.name} data-ready={s.ready ? 'yes' : 'no'} data-status={s.status}>
      <Mark s={s} />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="text-[13.5px] font-medium text-fg">{s.title}</span>
          {s.suggested && !s.ready ? <Badge tone="accent">Suggested</Badge> : null}
          {!s.ready && s.status !== 'not-here' ? <span className="text-[12px] text-subtle">{s.size}</span> : null}
        </div>
        <p className="mt-0.5 text-[12.5px] leading-relaxed text-muted">{s.unlocks}</p>
        {!s.ready || s.says.startsWith('Ready. ') ? <p className={cx('mt-1 text-[12.5px]', s.status === 'failed' ? 'text-danger' : 'text-subtle')}>{s.says}</p> : null}
        {s.adding ? <p className="mt-1 text-[12px] text-accent-text">{s.adding.message}</p> : null}
        {s.licence && !s.ready ? <p className="mt-1 text-[12px] text-subtle">{s.licence}</p> : null}
        {s.version || s.path || s.command || s.removeSays ? (
          <Details compact className="mt-1">
            <div className="space-y-1 text-[12px] text-muted">
              {s.version ? <div>{s.version}</div> : null}
              {s.path ? <code className="tc block break-all">{s.path}</code> : null}
              {s.command && !s.ready ? <code className="tc block break-all">{s.command}</code> : null}
              {s.ready && s.removeSays ? <div>{s.removeSays}</div> : null}
            </div>
          </Details>
        ) : null}
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {!s.ready && s.status !== 'not-here' ? <AddComponentButton c={s} add={add} busy={busy} /> : null}
        {s.ready && s.canRemove ? (
          <Button size="sm" variant="ghost" icon={<Trash size={13} />} disabled={!own.owner || busy !== null} title={ownerTitle(own, 'remove programs from this computer')} onClick={() => onRemove(s)} data-remove-component={s.name}>
            Remove
          </Button>
        ) : null}
      </div>
    </li>
  );
}

/** Settings → Components. */
export function ComponentsSection() {
  const q = useOp('components.list', {}, { refetchInterval: (query) => (query.state.data?.components.some((c) => c.status === 'adding') ? 2000 : 60_000) });
  const { add, busy } = useAddComponent();
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const [removing, setRemoving] = useState<string | null>(null);
  useEffect(() => {
    if (location.hash) document.getElementById(location.hash.slice(1))?.scrollIntoView();
  }, [q.data]);
  const remove = async (c: ComponentState) => {
    const ok = await dialog.confirm({
      title: `Remove ${c.title}?`,
      body: <p className="text-[13px] leading-relaxed">What it unlocks says it is missing until it is added again, and MetaRush won't set it up by itself.</p>,
      confirmLabel: 'Remove',
      tone: 'danger',
    });
    if (!ok) return;
    setRemoving(c.name);
    try {
      const out = await call('components.remove', { name: c.name });
      toast({ tone: out.removed.removed ? 'success' : 'info', title: out.summary });
    } catch (e) {
      toast({ tone: 'error', title: `${c.title} couldn't be removed`, body: toApiError(e).message });
    } finally {
      setRemoving(null);
      for (const k of AFTER) void qc.invalidateQueries({ queryKey: [k] });
    }
  };
  const all = q.data?.components ?? [];
  const required = all.filter((c) => c.required);
  const optional = all.filter((c) => !c.required && c.status !== 'not-here');
  const missing = optional.filter((c) => !c.ready).sort((a, b) => Number(b.suggested) - Number(a.suggested));
  const here = optional.filter((c) => c.ready);
  const elsewhere = all.filter((c) => c.status === 'not-here');
  return (
    <div className="space-y-6" data-components-page>
      <section className="panel p-5" id="components">
        <div className="flex items-start gap-3">
          <Package size={18} className="mt-0.5 text-accent-text" />
          <div className="min-w-0 flex-1">
            <h2 className="text-[15px] font-semibold text-fg">Components on this computer</h2>
            <p className="mt-1 text-[13px] text-muted">{q.data?.summary ?? 'Looking at what is on this computer…'}</p>
          </div>
          <Freshness at={q.dataUpdatedAt || undefined} fetching={q.isFetching} onRefresh={() => void q.refetch()} />
        </div>
        <OwnersNote className="mt-4" />
        {q.isLoading ? (
          <div className="mt-4 space-y-2">
            <Skeleton className="h-12" />
            <Skeleton className="h-12" />
            <Skeleton className="h-12" />
          </div>
        ) : q.error ? (
          <ErrorPanel className="mt-4" error={q.error} onRetry={() => void q.refetch()} />
        ) : (
          <div className="mt-2 space-y-5">
            {missing.length ? (
              <div>
                <h3 className="mt-3 text-[12px] font-medium uppercase tracking-wide text-subtle">You can add</h3>
                <ul className="divide-y divide-line" data-components-missing>
                  {missing.map((c) => (
                    <Row key={c.name} s={c} add={add} busy={busy ?? removing} onRemove={(x) => void remove(x)} />
                  ))}
                </ul>
              </div>
            ) : null}
            {here.length ? (
              <div>
                <h3 className="mt-3 text-[12px] font-medium uppercase tracking-wide text-subtle">On this computer</h3>
                <ul className="divide-y divide-line" data-components-here>
                  {here.map((c) => (
                    <Row key={c.name} s={c} add={add} busy={busy ?? removing} onRemove={(x) => void remove(x)} />
                  ))}
                </ul>
              </div>
            ) : null}
            <div>
              <h3 className="mt-3 text-[12px] font-medium uppercase tracking-wide text-subtle">What MetaRush needs to run</h3>
              <ul className="divide-y divide-line" data-components-required>
                {required.map((c) => (
                  <Row key={c.name} s={c} add={add} busy={busy ?? removing} onRemove={(x) => void remove(x)} />
                ))}
              </ul>
            </div>
            {elsewhere.length ? (
              <Details label={`Not for this computer (${elsewhere.length})`}>
                <ul className="mt-1 space-y-1 text-[12.5px] text-muted">
                  {elsewhere.map((c) => (
                    <li key={c.name}>
                      {c.title}: {c.says}
                    </li>
                  ))}
                </ul>
              </Details>
            ) : null}
          </div>
        )}
      </section>
    </div>
  );
}
