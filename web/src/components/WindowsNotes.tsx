import { CheckCircle, Circle, Desktop } from '@phosphor-icons/react';
import type { WindowsNote, WindowsReport } from '../api/types';
import { cx } from '../ui/primitives';

/**
 * MetaRush inside WSL on Windows (D515–D521): the headline ("MetaRush runs in
 * Ubuntu on Windows (WSL 2).") and what to know — the background service,
 * Ubuntu staying up after its last window closes, the Windows browser, the
 * network, folders on the Windows side — each ready, or with what to do. The
 * words are the server's (internal/wsl), the same as `metarush setup` and
 * doctor; each step's command stays behind Details. Getting started shows
 * every note, Settings → General only the ones that need something.
 */
export function WindowsNotes({ report, onlyToDo, className }: { report: WindowsReport; onlyToDo?: boolean; className?: string }) {
  const notes = onlyToDo ? report.notes.filter((n) => !n.ready) : report.notes;
  return (
    <div className={cx('min-w-0', className)} data-windows-notes data-wsl={report.version}>
      <p className="flex items-start gap-2 text-[13px] font-medium text-fg" data-windows-headline>
        <Desktop size={15} className="mt-0.5 shrink-0 text-subtle" aria-hidden />
        <span className="min-w-0">{report.says}</span>
      </p>
      {notes.length ? (
        <ul className="mt-2 space-y-2">
          {notes.map((n, i) => (
            <NoteRow key={`${n.key}-${i}`} n={n} />
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function NoteRow({ n }: { n: WindowsNote }) {
  return (
    <li className="flex items-start gap-2" data-windows-note={n.key} data-ready={n.ready ? 'yes' : 'no'}>
      {n.ready ? <CheckCircle size={15} weight="fill" className="mt-0.5 shrink-0 text-ok" /> : <Circle size={15} className="mt-0.5 shrink-0 text-subtle" />}
      <div className="min-w-0 flex-1 text-[12.5px]">
        <div className="text-muted">{n.says}</div>
        {!n.ready && n.next ? <div className="mt-0.5 text-subtle">{n.next}</div> : null}
        {!n.ready && n.command ? (
          <details className="mt-1 text-[12px]" data-command-details>
            <summary className="cursor-pointer text-subtle hover:text-muted">Details</summary>
            <code className="tc mt-1 block whitespace-pre-wrap break-all rounded-[8px] bg-inset px-2 py-1.5 text-[11.5px] text-muted">{n.command}</code>
          </details>
        ) : null}
      </div>
    </li>
  );
}
