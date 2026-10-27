import { useCallback, type AnchorHTMLAttributes, type ReactNode } from 'react';
import { Link } from 'react-router';
import { Signature } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call } from '../api/client';
import { useOp } from '../api/queries';
import type { Signoff } from '../api/types';
import { useDialog } from '../ui/Dialog';
import { cx, Theirs } from '../ui/primitives';

/*
 * Sign-offs are a reminder, never a lock (D166): before a person approves a
 * version or downloads the final video while the cut still waits for OKs
 * ("Lena OKs the final version"), the dashboard says so once, in a styled
 * dialog, and they can go ahead anyway. Saying "anyway" for a version or a
 * render is remembered on this page, so it doesn't ask again.
 */

const acknowledged = new Set<string>();

/** "Still waiting for 2 sign-offs: “…”; “…”." — the people's words marked as theirs. */
export function SignoffsWaiting({ waiting, question }: { waiting: Signoff[]; question?: string }) {
  return (
    <span data-signoffs-waiting>
      {waiting.length === 1 ? 'Still waiting for a sign-off: ' : `Still waiting for ${waiting.length} sign-offs: `}
      {waiting.map((s, i) => (
        <span key={s.id}>
          {i > 0 ? '; ' : ''}
          <Theirs className="text-fg">“{s.text}”</Theirs>
        </span>
      ))}
      .{question ? ` ${question}` : ''}
    </span>
  );
}

export interface SignoffCheck {
  /** The project's cut (a branch other than main when given). */
  projectId: string;
  branch?: string;
  what: 'approve' | 'download';
  /** What "anyway" is remembered for (a version or a render); asked once per page. */
  key?: string;
}

export function useSignoffReminder() {
  const qc = useQueryClient();
  const dialog = useDialog();

  /** The cut's sign-offs still waiting (none when they can't be read: it never blocks). */
  const waiting = useCallback(
    async (projectId: string, branch?: string): Promise<Signoff[]> => {
      const input = branch && branch !== 'main' ? { projectId, branch } : { projectId };
      try {
        const out = await qc.fetchQuery({ queryKey: ['cut.signoffs.list', input], queryFn: ({ signal }) => call('cut.signoffs.list', input, signal), staleTime: 5_000 });
        return out.signoffs.filter((s) => !s.done);
      } catch {
        return [];
      }
    },
    [qc],
  );

  /** Asks once when sign-offs are still waiting; true when the person goes ahead (or nothing waits). */
  const confirm = useCallback(
    async ({ projectId, branch, what, key }: SignoffCheck, body?: ReactNode): Promise<boolean> => {
      if (key && acknowledged.has(key)) return true;
      const open = await waiting(projectId, branch);
      if (!open.length) return true;
      const ok = await dialog.confirm({
        title: what === 'approve' ? 'Approve before everyone has OK’d it?' : 'Download before everyone has OK’d it?',
        body: (
          <div className="space-y-2">
            <p>
              <SignoffsWaiting waiting={open} question={what === 'approve' ? 'Approve anyway?' : 'Download anyway?'} />
            </p>
            {body}
          </div>
        ),
        confirmLabel: what === 'approve' ? 'Approve anyway' : 'Download anyway',
        cancelLabel: 'Not yet',
      });
      if (ok && key) acknowledged.add(key);
      return ok;
    },
    [dialog, waiting],
  );

  return { waiting, confirm, remember: (key: string) => acknowledged.add(key), remembered: (key: string) => acknowledged.has(key) };
}

/**
 * On the latest version: the sign-offs still waiting, in one line, with the way
 * to tick them (the project's Settings tab). Nothing when none wait.
 */
export function SignoffsWaitingLine({ projectId, branch, className }: { projectId: string; branch?: string; className?: string }) {
  const q = useOp('cut.signoffs.list', branch && branch !== 'main' ? { projectId, branch } : { projectId }, { staleTime: 15_000 });
  const open = (q.data?.signoffs ?? []).filter((s) => !s.done);
  if (!open.length) return null;
  return (
    <div className={cx('flex flex-wrap items-start gap-x-2 gap-y-1 rounded-[10px] bg-accent-soft/60 px-3 py-2 text-[12.5px] leading-relaxed text-muted', className)} data-signoffs-line>
      <Signature size={14} className="mt-[3px] shrink-0 text-accent-text" aria-hidden />
      <span className="min-w-0 flex-1">
        <SignoffsWaiting waiting={open} />{' '}
        <Link to={`/p/${projectId}?tab=settings`} className="whitespace-nowrap font-medium text-accent-text hover:underline">
          Tick them when they're given
        </Link>
      </span>
    </div>
  );
}

/** Starts a same-origin download (a render with ?download=1) without leaving the page. */
export function startDownload(href: string) {
  const a = document.createElement('a');
  a.href = href;
  a.rel = 'noopener';
  document.body.appendChild(a);
  a.click();
  a.remove();
}

/**
 * A render's download link. For the final video it first reminds of sign-offs
 * still waiting (once per render on this page); a review copy downloads at once,
 * since sending it round is how the OKs are asked for.
 */
export function RenderDownload({
  href,
  render,
  branch,
  children,
  ...rest
}: AnchorHTMLAttributes<HTMLAnchorElement> & { href: string; render: { id: string; projectId?: string; quality: string }; branch?: string; children: ReactNode }) {
  const { confirm, remembered } = useSignoffReminder();
  const key = `download:${render.id}`;
  return (
    <a
      href={href}
      {...rest}
      onClick={(e) => {
        rest.onClick?.(e);
        if (e.defaultPrevented || render.quality !== 'final' || !render.projectId || remembered(key)) return;
        e.preventDefault();
        void confirm({ projectId: render.projectId, branch, what: 'download', key }).then((ok) => {
          if (ok) startDownload(href);
        });
      }}
    >
      {children}
    </a>
  );
}
