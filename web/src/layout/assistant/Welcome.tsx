import { ArrowRight } from '@phosphor-icons/react';
import { WhoAnswers } from './WhoAnswers';
import { useConversations, useSuggestions } from '../../api/assistant';
import type { AssistantStatus } from '../../api/types';
import { relative } from '../../lib/format';
import { Skeleton } from '../../ui/primitives';
import { nameOf } from './people';
import { useWhose, whoseInput } from './store';

/*
 * A new conversation: one calm sentence about what Claude can do here and a
 * short "Things to try" list that fits what is open. Picking one only fills the
 * input. The list goes away as soon as the person types. "Pick up where you
 * left off" lists the latest conversations like the conversation list does: in
 * a project, everyone's in it unless the person chose Just mine (D179), each
 * someone else started saying who.
 */
export function Welcome({
  projectId,
  projectName,
  status,
  hideSuggestions,
  showRecent,
  onPick,
  onOpen,
  me,
}: {
  projectId?: string;
  projectName?: string;
  status: AssistantStatus;
  hideSuggestions: boolean;
  showRecent: boolean;
  onPick: (prompt: string) => void;
  onOpen: (id: string) => void;
  me: string;
}) {
  const sug = useSuggestions(projectId, !hideSuggestions);
  const whose = useWhose();
  const recent = useConversations({ limit: 3, projectId, ...whoseInput(whose) }, showRecent && !hideSuggestions);
  const plan = status.billing === 'plan';
  return (
    <div className="mx-auto w-full max-w-[560px] py-2">
      <h3 className="text-[17px] font-semibold tracking-[-0.015em] text-fg">{projectName ? `What should we do with ${projectName}?` : 'What should we work on?'}</h3>
      <p className="mt-1.5 text-[13.5px] leading-relaxed text-muted">
        Claude can look through your footage, build and tighten cuts, and render and check them. Anything that changes your project waits for your OK.
      </p>
      <p className="mt-1 text-[12px] text-subtle">{plan ? 'Uses your Claude plan.' : status.billing === 'api' ? 'Billed to your Anthropic API key.' : 'Runs with your Claude Code sign-in.'}</p>
      <WhoAnswers account={status.account} className="mt-1" />
      {hideSuggestions ? null : (
        <div className="mt-6">
          <h4 className="text-[12.5px] font-medium text-muted">Things to try</h4>
          {sug.isLoading ? (
            <div className="mt-2 space-y-1.5" aria-busy="true">
              {[0, 1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-9" />
              ))}
            </div>
          ) : (
            <ul className="mt-2 divide-y divide-line overflow-hidden rounded-[12px] hairline">
              {(sug.data?.suggestions ?? []).slice(0, 6).map((s) => (
                <li key={s.label}>
                  <button
                    type="button"
                    onClick={() => onPick(s.prompt)}
                    className="group flex w-full items-center gap-3 px-3.5 py-2.5 text-left text-[13px] text-fg transition-colors hover:bg-hover focus-visible:bg-hover"
                    title={s.prompt}
                  >
                    <span className="min-w-0 flex-1 truncate">{s.label}</span>
                    <ArrowRight size={13} className="shrink-0 text-subtle opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100" aria-hidden />
                  </button>
                </li>
              ))}
            </ul>
          )}
          {showRecent && recent.data?.conversations.length ? (
            <div className="mt-6">
              <h4 className="text-[12.5px] font-medium text-muted">Pick up where you left off</h4>
              <ul className="mt-2 space-y-0.5">
                {recent.data.conversations.map((c) => (
                  <li key={c.id}>
                    <button type="button" onClick={() => onOpen(c.id)} className="flex w-full items-baseline gap-2 rounded-[8px] px-1.5 py-1 text-left hover:bg-hover">
                      <span className="min-w-0 flex-1 truncate text-[12.5px] text-fg">{c.title}</span>
                      {!c.mine && me ? <span className="min-w-0 shrink truncate text-[11.5px] text-muted" data-started-by={c.userName}>Started by {nameOf(c.userName)}</span> : null}
                      <span className="shrink-0 text-[11.5px] text-subtle">{relative(c.updatedAt)}</span>
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </div>
      )}
    </div>
  );
}
