import { useCallback, useState } from 'react';
import { ArrowSquareOut, CheckCircle, DownloadSimple, FolderOpen } from '@phosphor-icons/react';
import { request } from '../api/client';
import type { SendToEditorIn, SendToEditorOut } from '../api/types';
import { useHost } from '../lib/host';
import { useDialog } from '../ui/Dialog';
import { Button, cx, IconButton, Spinner } from '../ui/primitives';
import { useToast } from '../ui/Toast';

/*
 * "Send to editing app": the version becomes a timeline in a new folder under
 * Movies/MetaRush exports (OpenTimelineIO for DaVinci Resolve, FCPXML for Final
 * Cut Pro, Final Cut Pro 7 XML for Premiere Pro), with the original footage
 * next to it when asked. The CLI does the same with `metarush export timeline`.
 */

type App = SendToEditorIn['app'];

const APPS: { id: App; name: string; file: string; open: string }[] = [
  { id: 'resolve', name: 'DaVinci Resolve', file: 'OpenTimelineIO timeline', open: 'File > Import > Timeline…' },
  { id: 'finalcut', name: 'Final Cut Pro', file: 'Final Cut Pro XML', open: 'File > Import > XML…' },
  { id: 'premiere', name: 'Premiere Pro', file: 'Final Cut Pro 7 XML', open: 'File > Import…' },
];

function SendForm({ revisionId, label, close }: { revisionId: string; label: string; close: (v?: SendToEditorOut) => void }) {
  const toast = useToast();
  const host = useHost();
  const [app, setApp] = useState<App>('resolve');
  const [footage, setFootage] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<SendToEditorOut | null>(null);
  const chosen = APPS.find((a) => a.id === app)!;

  const send = async () => {
    setBusy(true);
    setError(null);
    try {
      const out = await request<SendToEditorOut>('POST', '/api/export/timeline', { json: { revisionId, app, footage } satisfies SendToEditorIn });
      setDone(out);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
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
    const ex = done.export;
    return (
      <div className="space-y-4" data-send-done>
        <div className="flex items-start gap-2.5">
          <CheckCircle size={20} weight="fill" className="mt-0.5 shrink-0 text-ok" />
          <div className="min-w-0 text-[13.5px] leading-relaxed text-fg">
            <p>{ex.summary}</p>
            <p className="mt-1 text-muted">
              In {chosen.name}: {chosen.open}
              {ex.captions ? ' The captions are also there as subtitles (.srt).' : ''}
            </p>
            {host.remote ? (
              <p className="mt-1 text-muted">
                The folder is on {host.machine}. Download it to open it on this computer: unzip it, and if {chosen.name} asks where the footage is, point it at the Footage folder inside.
              </p>
            ) : null}
          </div>
        </div>
        {ex.notes.length > 0 ? (
          <div className="rounded-[12px] bg-panel px-3.5 py-3">
            <div className="text-[12px] font-semibold uppercase tracking-wide text-subtle">What {chosen.name} won't show exactly</div>
            <ul className="mt-1.5 list-disc space-y-1 pl-4 text-[13px] text-muted">
              {ex.notes.map((n) => (
                <li key={n}>{n.charAt(0).toUpperCase() + n.slice(1)}</li>
              ))}
            </ul>
          </div>
        ) : null}
        <div className="flex flex-wrap justify-end gap-2">
          {host.remote ? (
            <a
              href={`/api/export/download?folder=${encodeURIComponent(done.folder)}`}
              download
              className="inline-flex h-9 items-center gap-2 rounded-[10px] bg-panel px-3.5 text-[13.5px] text-fg hairline hover:bg-hover"
              data-export-download
            >
              <DownloadSimple size={15} /> Download the folder
            </a>
          ) : null}
          <Button variant={host.remote ? 'ghost' : 'secondary'} onClick={() => void reveal(done.folder)} icon={<FolderOpen size={15} />} data-host-action>
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
        Make a timeline of {label} that another editing app opens, to finish it there.
        {host.remote ? ` The folder is made on ${host.machine} (in Movies, MetaRush exports); you can download it afterwards.` : ''}
      </p>
      <div role="radiogroup" aria-label="Editing app" className="grid gap-2 sm:grid-cols-3">
        {APPS.map((a) => (
          <button
            key={a.id}
            type="button"
            role="radio"
            aria-checked={app === a.id}
            onClick={() => setApp(a.id)}
            className={cx(
              'rounded-[12px] border px-3 py-2.5 text-left transition-colors',
              app === a.id ? 'border-accent-line bg-accent-soft' : 'border-line hover:bg-hover',
            )}
          >
            <div className="text-[13.5px] font-semibold text-fg">{a.name}</div>
            <div className="mt-0.5 text-[12px] text-subtle">{a.file}</div>
          </button>
        ))}
      </div>
      <label className="flex items-start gap-2.5 text-[13.5px] text-fg">
        <input type="checkbox" className="mt-1" checked={footage} onChange={(e) => setFootage(e.target.checked)} />
        <span>
          Put the original footage in the folder too
          <span className="block text-[12.5px] text-subtle">Instant copies {host.remote ? `on ${host.machine}` : 'on this Mac'}, under their own names, so the folder can go to another computer.</span>
        </span>
      </label>
      {error ? <p className="text-[13px] text-danger">{error}</p> : null}
      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={() => close()} disabled={busy}>
          Cancel
        </Button>
        <Button variant="primary" onClick={() => void send()} disabled={busy} icon={busy ? <Spinner size={14} /> : <ArrowSquareOut size={15} />}>
          {busy ? (footage ? 'Writing the timeline and footage…' : 'Writing the timeline…') : `Make the timeline for ${chosen.name}`}
        </Button>
      </div>
    </div>
  );
}

/** Opens the "Send to editing app" dialog for a version of a cut. */
export function useSendToEditor() {
  const dialog = useDialog();
  return useCallback(
    (revisionId: string, label: string) =>
      dialog.open<SendToEditorOut>((close) => <SendForm revisionId={revisionId} label={label} close={close} />, { title: 'Send to editing app', size: 'md' }),
    [dialog],
  );
}

/** The button: a small icon on a render, or a labelled button in a header. */
export function SendToEditorButton({ revisionId, label, compact }: { revisionId?: string; label: string; compact?: boolean }) {
  const open = useSendToEditor();
  if (!revisionId) return null;
  if (compact) {
    return (
      <IconButton size="sm" label={`Send ${label} to an editing app`} onClick={() => void open(revisionId, label)}>
        <ArrowSquareOut size={15} />
      </IconButton>
    );
  }
  return (
    <Button size="sm" variant="secondary" icon={<ArrowSquareOut size={14} />} onClick={() => void open(revisionId, label)}>
      Send to editing app
    </Button>
  );
}
