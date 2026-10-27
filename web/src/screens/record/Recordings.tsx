import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowSquareOut, FilmSlate, FolderSimplePlus, Trash, WarningCircle } from '@phosphor-icons/react';
import { call, toApiError } from '../../api/client';
import { useOp } from '../../api/queries';
import type { RecordingInfo } from '../../api/types';
import { useDialog } from '../../ui/Dialog';
import { cx, Spinner } from '../../ui/primitives';
import { ProjectPicker } from '../../components/ProjectPicker';
import { forget } from './store';

/*
 * Recordings made in the dashboard (D260), newest first: what each is and
 * what to do next — keep what arrived after a closed tab, link one into a
 * project, start a cut from it with the screen and camera laid out.
 */

export type CutLayout = 'pip' | 'side-by-side' | 'screen' | 'camera';

export const LAYOUT_WORDS: Record<CutLayout, string> = {
  pip: 'Camera in a corner',
  'side-by-side': 'Side by side',
  screen: 'Screen only',
  camera: 'Camera only',
};

/** The layouts a recording's tracks allow. */
export function layoutsFor(r: RecordingInfo): CutLayout[] {
  const has = (id: string) => r.recording.tracks.some((t) => t.id === id && t.assetId);
  const out: CutLayout[] = [];
  if (has('screen') && has('camera')) out.push('pip', 'side-by-side');
  if (has('screen')) out.push('screen');
  if (has('camera')) out.push('camera');
  return out;
}

/** Starts a cut from a recording (saved as a version) and opens it. */
export function useStartCut() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  return async (r: RecordingInfo, layout: CutLayout, projectId?: string): Promise<string | null> => {
    try {
      const out = await call('recording.cut', { recording: r.recording.id, layout, apply: true, ...(projectId ? { project: projectId } : {}) });
      if (!out.applied || out.applied.problem) return out.applied?.problem ?? out.summary;
      void qc.invalidateQueries();
      navigate(out.branch === 'main' ? `/p/${out.projectId}?tab=cut` : `/p/${out.projectId}/r/${out.applied.revisionId}`);
      return null;
    } catch (e) {
      return toApiError(e).message;
    }
  };
}

export function RecordingsList({ projectId, exclude }: { projectId?: string; exclude?: string }) {
  const list = useOp('recording.list', { ...(projectId ? { projectId } : {}), limit: 8 }, { refetchInterval: 5000 });
  const rows = (list.data?.recordings ?? []).filter((r) => r.recording.id !== exclude);
  if (!rows.length) return null;
  return (
    <section className="mt-8" aria-label="Recent recordings" data-recordings>
      <h3 className="mb-2 text-[12px] font-medium uppercase tracking-[0.08em] text-[#8f877e]">Recent recordings</h3>
      <ul className="space-y-2">
        {rows.map((r) => (
          <RecordingRow key={r.recording.id} r={r} onChanged={() => void list.refetch()} />
        ))}
      </ul>
    </section>
  );
}

function RecordingRow({ r, onChanged }: { r: RecordingInfo; onChanged: () => void }) {
  const dialog = useDialog();
  const startCut = useStartCut();
  const [busy, setBusy] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const rec = r.recording;
  const run = async (key: string, fn: () => Promise<string | null | void>) => {
    setBusy(key);
    setProblem(null);
    try {
      const p = await fn();
      if (p) setProblem(p);
    } catch (e) {
      setProblem(toApiError(e).message);
    } finally {
      setBusy(null);
      onChanged();
    }
  };
  const keep = () =>
    run('keep', async () => {
      await call('recording.finish', { recording: rec.id, keep: true });
      await forget(rec.id);
    });
  const discard = async () => {
    const ok = await dialog.confirm({
      title: 'Throw this recording away?',
      body: 'What arrived of it is deleted from the computer that runs MetaRush. This can’t be undone.',
      confirmLabel: 'Throw it away',
      tone: 'danger',
    });
    if (!ok) return;
    await run('discard', async () => {
      await call('recording.discard', { recording: rec.id });
      await forget(rec.id);
    });
  };
  const link = async () => {
    const picked = await dialog.open<{ id: string }>((close) => <ProjectPicker label="Projects" onPick={(p) => close({ id: p.id })} />, { title: 'Link into which project?', size: 'sm' });
    if (!picked) return;
    await run('link', async () => {
      await call('recording.link', { recording: rec.id, project: picked.id });
    });
  };
  const layouts = rec.state === 'ready' && r.links.length ? layoutsFor(r) : [];
  return (
    <li className="rounded-[14px] bg-white/[0.04] px-4 py-3 ring-1 ring-inset ring-white/[0.05]" data-recording={rec.id} data-state={r.interrupted ? 'interrupted' : rec.state}>
      <div className="flex flex-wrap items-start gap-x-3 gap-y-1">
        <FilmSlate size={16} className="mt-[2px] shrink-0 text-[#e0a458]" aria-hidden />
        <p className="min-w-0 flex-1 text-[13px] leading-relaxed text-[#e9e2d9]">{r.text}</p>
        {busy ? <Spinner size={13} /> : null}
      </div>
      {r.next ? <p className="ml-7 mt-1 text-[12.5px] text-[#a69d93]">{r.next}</p> : null}
      {problem ? (
        <p className="ml-7 mt-1.5 flex items-start gap-1.5 text-[12.5px] text-[#ef8d80]" role="alert">
          <WarningCircle size={13} weight="fill" className="mt-[2px] shrink-0" /> {problem}
        </p>
      ) : null}
      <div className="ml-7 mt-2 flex flex-wrap gap-2">
        {r.interrupted && r.here ? <RowButton onClick={() => void keep()} disabled={!!busy}>Keep what arrived</RowButton> : null}
        {(r.interrupted || rec.state === 'failed') && r.here && !rec.tracks.some((t) => t.assetId) ? (
          <RowButton onClick={() => void discard()} disabled={!!busy} icon={<Trash size={12} />}>
            Throw it away
          </RowButton>
        ) : null}
        {rec.state === 'failed' && r.here ? <RowButton onClick={() => void run('again', async () => void (await call('recording.finish', { recording: rec.id, keep: true })))} disabled={!!busy}>Try again</RowButton> : null}
        {rec.state === 'ready' && !r.links.length ? (
          <RowButton onClick={() => void link()} disabled={!!busy} icon={<FolderSimplePlus size={12} />}>
            Link into a project…
          </RowButton>
        ) : null}
        {layouts.map((l) => (
          <RowButton key={l} onClick={() => void run(`cut:${l}`, () => startCut(r, l))} disabled={!!busy} icon={<ArrowSquareOut size={12} />} data-start-cut={l}>
            {`Start a cut: ${LAYOUT_WORDS[l].toLowerCase()}`}
          </RowButton>
        ))}
      </div>
    </li>
  );
}

function RowButton({ children, onClick, disabled, icon, ...rest }: { children: React.ReactNode; onClick: () => void; disabled?: boolean; icon?: React.ReactNode } & Record<`data-${string}`, string>) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className={cx('inline-flex h-7 items-center gap-1.5 rounded-full bg-white/[0.07] px-3 text-[12px] font-medium text-[#e9e2d9] hover:bg-white/[0.12] disabled:opacity-40')}
      {...rest}
    >
      {icon}
      {children}
    </button>
  );
}
