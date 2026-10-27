import { useEffect, useState } from 'react';
import { ArrowClockwise, ArrowsLeftRight, CheckCircle, MagicWand, Warning } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import type { ops } from '@metarush/schema';
import { call, media, toApiError } from '../../api/client';
import { useDialog } from '../../ui/Dialog';
import { Badge, Button, cx, Segmented, Spinner, Theirs } from '../../ui/primitives';
import { useToast } from '../../ui/Toast';

/*
 * A source's fixes (D152): two or three per kind of source, as checkboxes;
 * MetaRush marks the ones that fit and says why ("The camera shakes a lot").
 * Ticking makes a fixed copy on a computer that's on (the original is always
 * kept, unticking goes back to it); everything after uses the copy, and a
 * saved cut gets it only as a new version, one click per cut.
 */

/** Queries that show a source's name, fixes or what plays. */
export const SOURCE_QUERIES = ['asset.card', 'asset.fixes', 'asset.names', 'asset.list', 'asset.show', 'project.show', 'activity.summary', 'resources.show', 'resources.list', 'revision.list'];

export function useRefreshSources() {
  const qc = useQueryClient();
  return () => SOURCE_QUERIES.forEach((k) => void qc.invalidateQueries({ queryKey: [k] }));
}

const busyStates = new Set(['preparing']);

export function fixesBusy(v: ops.FixesView | undefined): boolean {
  return !!v && (busyStates.has(v.state ?? '') || !!v.measuring);
}

export function FixesPanel({ view, kindOf = 'clip', className }: {
  view: ops.FixesView;
  /** "clip" or "picture": the words used for it. */
  kindOf?: 'clip' | 'picture';
  className?: string;
}) {
  const toast = useToast();
  const dialog = useDialog();
  const refresh = useRefreshSources();
  const [busy, setBusy] = useState<string | null>(null);
  // A tick shows at once; the server's answer replaces it.
  const [optimistic, setOptimistic] = useState<Record<string, boolean>>({});
  const tickedKey = view.ticked.join(',');
  useEffect(() => setOptimistic({}), [tickedKey, view.state]);
  const preparing = view.state === 'preparing';

  const set = async (key: string, input: ops.FixesSetIn, failTitle: string) => {
    setBusy(key);
    try {
      const out = await call('asset.fixes.set', input);
      refresh();
      toast({ tone: 'success', title: out.summary });
    } catch (e) {
      const err = toApiError(e);
      setOptimistic({});
      toast({ tone: 'error', title: failTitle, body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(null);
    }
  };
  const look = async () => {
    setBusy('look');
    try {
      await call('asset.fixes.look', { id: view.id });
      refresh();
      toast({ title: 'Measuring it again', body: 'The fixes that fit show here once it is done.' });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: "Couldn't measure it again", body: err.message });
    } finally {
      setBusy(null);
    }
  };
  const useIn = async (cu: ops.FixCutUse) => {
    setBusy(`cut:${cu.projectId}:${cu.branch}`);
    try {
      const out = await call('cut.useFixed', { projectId: cu.projectId, branch: cu.branch, footage: view.id, ...(cu.original ? { original: true } : {}) });
      refresh();
      toast({ tone: 'success', title: out.applied ? `Saved as version ${out.applied.revision.number} of “${cu.cutName}”` : out.summary, body: out.applied ? out.summary : undefined });
    } catch (e) {
      const err = toApiError(e);
      toast({ tone: 'error', title: 'The cut stays as it is', body: [err.message, err.hint].filter(Boolean).join(' ') });
    } finally {
      setBusy(null);
    }
  };
  const compare = () =>
    void dialog.open<void>(() => <BeforeAfter view={view} />, { title: `Before and after: ${view.title}`, size: 'lg' });

  return (
    <section className={cx('space-y-3', className)} aria-label={`Fixes for ${view.title}`} data-fixes-panel data-fix-state={view.state ?? ''}>
      <div className="flex flex-wrap items-center gap-2">
        <MagicWand size={15} className="text-accent-text" aria-hidden />
        <h3 className="text-[13.5px] font-semibold text-fg">Fixes</h3>
        <span className="text-[12px] text-subtle">for {view.kindWords}</span>
        {view.kinds.length > 1 && !view.picture ? (
          <Segmented
            label={`What kind of ${kindOf} it is`}
            className="ml-auto"
            value={view.kind}
            onChange={(k) => void set('kind', { id: view.id, kind: k as ops.FixesSetIn['kind'] }, "Couldn't change that")}
            items={view.kinds.map((k) => ({ value: k.value, label: k.label }))}
          />
        ) : null}
      </div>
      {view.measuring ? (
        <p className="flex items-center gap-2 text-[12.5px] text-muted" role="status">
          <Spinner size={11} /> {view.measuring}
        </p>
      ) : null}
      <ul className="space-y-1.5">
        {view.fixes.map((f) => {
          const id = `fix-${view.id}-${f.id}`;
          return (
            <li key={f.id} className={cx('flex items-start gap-3 rounded-[12px] px-3 py-2.5 hairline', f.fits && !f.ticked && 'bg-accent-soft/40')} data-fix={f.id} data-fits={f.fits || undefined}>
              <input
                id={id}
                type="checkbox"
                checked={optimistic[f.id] ?? f.ticked}
                disabled={busy !== null}
                onChange={(e) => {
                  const on = e.target.checked;
                  setOptimistic((o) => ({ ...o, [f.id]: on }));
                  void set(f.id, on ? { id: view.id, tick: [f.id] } : { id: view.id, untick: [f.id] }, on ? `Couldn't ${f.label.toLowerCase()}` : "Couldn't take that fix off");
                }}
                className="mt-[3px] h-4 w-4 shrink-0 cursor-pointer accent-[var(--accent)]"
              />
              <label htmlFor={id} className="min-w-0 flex-1 cursor-pointer">
                <span className="flex flex-wrap items-center gap-1.5">
                  <span className="text-[13px] font-medium text-fg">{f.label}</span>
                  {f.fits ? <Badge tone="accent">Suggested</Badge> : null}
                  {busy === f.id ? <Spinner size={10} /> : null}
                </span>
                {f.why ? <span className="mt-0.5 block text-[12.5px] text-muted">{f.why}</span> : null}
                {f.after ? <span className="mt-0.5 block text-[12px] text-ok">{f.after}</span> : f.measured ? <span className="mt-0.5 block text-[12px] text-subtle">{f.measured}</span> : null}
                {f.pending ? <span className="mt-0.5 block text-[12px] text-subtle">{f.pending}</span> : null}
              </label>
            </li>
          );
        })}
      </ul>

      <div className="rounded-[12px] bg-hover/60 px-3.5 py-3 text-[12.5px] leading-relaxed" aria-live="polite" data-fix-summary>
        {preparing ? (
          <span className="flex items-center gap-2 text-muted">
            <Spinner size={11} /> {view.summary}
          </span>
        ) : view.state === 'failed' ? (
          <span className="flex flex-wrap items-center gap-2 text-danger">
            <Warning size={13} /> {view.error ?? view.summary}
            <Button size="sm" variant="ghost" loading={busy === 'again'} onClick={() => void set('again', { id: view.id, again: true }, "Couldn't start it again")}>
              Try again
            </Button>
          </span>
        ) : (
          <span className="flex flex-wrap items-center gap-2 text-fg">
            {view.copy ? <CheckCircle size={13} weight="fill" className="text-ok" /> : null}
            {view.usingWords}
          </span>
        )}
        {view.copy?.notes?.length ? (
          <ul className="mt-1.5 space-y-0.5 text-[12px] text-subtle">
            {view.copy.notes.map((n) => (
              <li key={n}>{n}</li>
            ))}
          </ul>
        ) : null}
        {view.trimWords ? <p className="mt-1 text-[12px] text-muted">{view.trimWords}</p> : null}
      </div>

      {view.cuts.length ? (
        <div className="space-y-1.5" data-fix-cuts>
          {view.cuts.map((cu) => (
            <div key={`${cu.projectId}:${cu.branch}`} className="flex flex-col items-start gap-2 rounded-[12px] px-3 py-2 hairline sm:flex-row sm:items-center">
              <div className="min-w-0 text-[12.5px] sm:flex-1">
                <Theirs className="font-medium text-fg">{cu.cutName}</Theirs>
                <span className="text-subtle">
                  {' '}
                  in <Theirs>{cu.projectName}</Theirs>, version {cu.number}: {cu.playsWords}
                </span>
              </div>
              {cu.offer ? (
                <Button size="sm" variant={cu.original ? 'ghost' : 'secondary'} loading={busy === `cut:${cu.projectId}:${cu.branch}`} disabled={busy !== null && busy !== `cut:${cu.projectId}:${cu.branch}`} onClick={() => void useIn(cu)} data-use-fixed>
                  {cu.offer}
                </Button>
              ) : null}
            </div>
          ))}
          <p className="px-1 text-[11.5px] text-subtle">Saved versions never change: using the fixed copy saves a new version of that cut.</p>
        </div>
      ) : null}

      <div className="flex flex-wrap gap-2">
        {view.copy && !view.picture ? (
          <Button size="sm" variant="ghost" icon={<ArrowsLeftRight size={13} />} onClick={compare} data-before-after>
            Before and after
          </Button>
        ) : null}
        <Button size="sm" variant="ghost" icon={<ArrowClockwise size={13} />} loading={busy === 'look'} disabled={busy !== null || fixesBusy(view)} onClick={() => void look()}>
          Measure again
        </Button>
      </div>
    </section>
  );
}

/** The original and the fixed copy side by side, with what changed. */
function BeforeAfter({ view }: { view: ops.FixesView }) {
  const sound = (view.copy?.fixes ?? []).every((f) => ['voices', 'volumes', 'loudness', 'hum', 'ends'].includes(f));
  const original = `${media.asset(view.id, 'proxy')}?media=original`;
  const fixed = `${media.asset(view.id, 'proxy')}?media=${encodeURIComponent(view.copy?.sha256 ?? '')}`;
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        {[
          { label: 'Original', src: original },
          { label: `Fixed: ${view.copy?.words ?? ''}`, src: fixed },
        ].map((s) =>
          sound ? (
            <figure key={s.label} className="space-y-1.5">
              <figcaption className="text-[12.5px] font-medium text-fg">{s.label}</figcaption>
              <audio controls preload="metadata" src={s.src} className="w-full" />
            </figure>
          ) : (
            <figure key={s.label} className="space-y-1.5">
              <figcaption className="text-[12.5px] font-medium text-fg">{s.label}</figcaption>
              <video controls muted playsInline preload="metadata" src={s.src} className="aspect-video w-full rounded-[10px] bg-inset" />
            </figure>
          ),
        )}
      </div>
      {view.copy?.measures?.length ? (
        <ul className="space-y-1 text-[12.5px] text-muted" aria-label="What changed">
          {view.copy.measures.map((m) => (
            <li key={m.fix}>{m.words}</li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
