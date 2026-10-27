import { ShieldCheck } from '@phosphor-icons/react';
import type { api } from '@metarush/schema';
import { useAssistantStatus } from '../../api/assistant';
import { cx, Skeleton } from '../../ui/primitives';

/*
 * Whose Claude answers (D239): the panel runs the host's Claude Code, signed
 * in to the host's Claude account, so every conversation says whose account
 * answers and which terms apply to what Claude reads. A conversation shows
 * the account that answered its latest message; a new one the account
 * signed in now.
 */

export function WhoAnswers({ account, className, compact }: { account?: api.ClaudeAccount; className?: string; compact?: boolean }) {
  if (!account) return null;
  return (
    <details className={cx('group text-[12px] text-subtle', className)} data-who-answers={account.terms}>
      <summary className="flex cursor-pointer list-none items-center gap-1.5 hover:text-muted [&::-webkit-details-marker]:hidden">
        <ShieldCheck size={13} className="shrink-0" aria-hidden />
        <span className="min-w-0 truncate">
          {compact ? 'Answered by ' : 'Answers come from '}
          {account.who} · {account.termsName}
        </span>
      </summary>
      <p className="mt-1 pl-[19px] leading-relaxed text-muted">{account.dataUse}</p>
    </details>
  );
}

/** Settings → Claude Code: whose Claude account answers in the panel, and its terms (anyone can read it). */
export function WhoseClaudeSettings() {
  const status = useAssistantStatus();
  const a = status.data?.account;
  return (
    <section id="claude-account" className="panel scroll-mt-24 p-5" aria-labelledby="claude-account-h" data-claude-account>
      <h2 id="claude-account-h" className="flex items-center gap-2 text-[15px] font-semibold">
        <span className="text-subtle">
          <ShieldCheck size={17} />
        </span>
        Whose Claude answers
      </h2>
      {status.isLoading ? (
        <Skeleton className="mt-3 h-12" />
      ) : !a ? (
        <p className="mt-1 text-[13px] leading-relaxed text-muted">
          {status.data?.reason ?? 'Claude Code isn’t signed in on this computer, so nobody’s Claude account answers in the panel yet.'}
        </p>
      ) : (
        <div className="mt-2 space-y-2 text-[13px] leading-relaxed">
          <p>
            Every conversation in the Claude Code panel is answered by <span className="font-medium">{a.who}</span>. Everyone who uses the panel here is answered by
            that account: what they ask, and the transcripts and frames Claude reads, go to Anthropic under its terms.
          </p>
          <p className="text-muted">
            <span className="font-medium text-fg">{a.termsName}.</span> {a.dataUse}
          </p>
          <p className="text-[12px] text-subtle">To answer with another account, sign Claude Code in to it on {a.machine || 'this computer'}; each conversation says which account answered it.</p>
        </div>
      )}
    </section>
  );
}
