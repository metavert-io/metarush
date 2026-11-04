import { useCallback, useState } from 'react';
import { Certificate, CheckCircle, DownloadSimple, FolderOpen, PaperPlaneTilt } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { request, toApiError } from '../api/client';
import type { DeliverHereIn, DeliverHereOut } from '../api/types';
import { useHost } from '../lib/host';
import { useDialog } from '../ui/Dialog';
import { Button, Spinner } from '../ui/primitives';
import { useToast } from '../ui/Toast';

/*
 * The dashboard's Deliver (D422): a version's final renders go to a new
 * folder under Movies/MetaRush exports, each with its record (.json), notes
 * and captions — and Content Credentials where they are on (D418). The same
 * delivery as `metarush deliver`; MetaRush chooses the folder, never the
 * browser, and never writes over anything.
 */

function DeliverForm({ revisionId, label, close }: { revisionId: string; label: string; close: (v?: DeliverHereOut) => void }) {
  const toast = useToast();
  const host = useHost();
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<DeliverHereOut | null>(null);
  const deliver = async () => {
    setBusy(true);
    setError(null);
    try {
      const out = await request<DeliverHereOut>('POST', '/api/deliver', { json: { revisionId } satisfies DeliverHereIn });
      setDone(out);
      for (const k of ['delivery.ready', 'project.receipt', 'project.show'] as const) void qc.invalidateQueries({ queryKey: [k] });
    } catch (e) {
      const err = toApiError(e);
      setError([err.message, err.hint].filter(Boolean).join(' '));
      // A signing that failed is remembered: the version's page now says what holds it.
      for (const k of ['delivery.ready', 'credentials.status'] as const) void qc.invalidateQueries({ queryKey: [k] });
    } finally {
      setBusy(false);
    }
  };
  const reveal = async (folder: string) => {
    try {
      await request('POST', '/api/export/reveal', { json: { folder } });
    } catch (e) {
      toast({ tone: 'error', title: 'Could not open the folder', body: e instanceof Error ? e.message : String(e) });
    }
  };
  if (done) {
    const d = done.deliver;
    return (
      <div className="space-y-4" data-deliver-done>
        <div className="flex items-start gap-2.5">
          <CheckCircle size={20} weight="fill" className="mt-0.5 shrink-0 text-ok" />
          <div className="min-w-0 text-[13.5px] leading-relaxed text-fg">
            <p>{d.note}</p>
            {host.remote ? <p className="mt-1 text-muted">The folder is on {host.machine}. Download it to have the videos on this computer.</p> : null}
          </div>
        </div>
        <ul className="space-y-1.5 text-[13px]">
          {d.files.map((f) => (
            <li key={f.path} className="rounded-[10px] bg-panel px-3 py-2" data-deliver-file>
              <div className="break-all font-medium">{f.path.split('/').pop()}</div>
              {f.contentCredentials ? (
                <div className="mt-0.5 flex items-start gap-1.5 text-[12.5px] text-muted" data-deliver-credentials={f.contentCredentials.signed ? 'signed' : 'none'}>
                  <Certificate size={14} className="mt-0.5 shrink-0" aria-hidden />
                  <span>{f.contentCredentials.says}</span>
                </div>
              ) : null}
            </li>
          ))}
        </ul>
        {d.warnings.length > 0 ? (
          <ul className="list-disc space-y-1 pl-5 text-[12.5px] text-info">
            {d.warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        ) : null}
        <div className="flex flex-wrap justify-end gap-2">
          {host.remote ? (
            <a
              href={`/api/export/download?folder=${encodeURIComponent(done.folder)}`}
              download
              className="inline-flex h-9 items-center gap-2 rounded-[10px] bg-panel px-3.5 text-[13.5px] text-fg hairline hover:bg-hover"
              data-deliver-download
            >
              <DownloadSimple size={15} /> Download the folder
            </a>
          ) : null}
          <Button variant={host.remote ? 'ghost' : 'secondary'} onClick={() => void reveal(done.folder)} icon={<FolderOpen size={15} />}>
            {host.remote ? `Show in Finder on ${host.machine}` : 'Show in Finder'}
          </Button>
          <Button variant="primary" onClick={() => close(done)}>
            Done
          </Button>
        </div>
      </div>
    );
  }
  return (
    <div className="space-y-4">
      <p className="text-[13.5px] text-muted">
        Delivers the final video of {label} to a new folder in MetaRush exports{host.remote ? ` on ${host.machine}` : ''}, checked against the render, with its record,
        notes and captions next to it, and Content Credentials where they are on. Nothing is uploaded and nothing is written over.
      </p>
      {error ? (
        <p className="text-[13px] text-danger" data-deliver-error>
          {error}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={() => close()} disabled={busy}>
          Cancel
        </Button>
        <Button variant="primary" onClick={() => void deliver()} disabled={busy} icon={busy ? <Spinner size={14} /> : <PaperPlaneTilt size={15} />} data-deliver-go>
          {busy ? 'Delivering…' : 'Deliver'}
        </Button>
      </div>
    </div>
  );
}

/** Opens the Deliver dialog for a version. */
export function useDeliver() {
  const dialog = useDialog();
  return useCallback(
    (revisionId: string, label: string) => dialog.open<DeliverHereOut>((close) => <DeliverForm revisionId={revisionId} label={label} close={close} />, { title: 'Deliver', size: 'md' }),
    [dialog],
  );
}
