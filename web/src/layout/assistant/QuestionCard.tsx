import { useId, useState } from 'react';
import { ChatCircleDots, CheckCircle } from '@phosphor-icons/react';
import { useAssistantOp } from '../../api/assistant';
import type { AssistantAction, Conversation } from '../../api/types';
import { absolute, stamp } from '../../lib/format';
import { Badge, Button, ErrorPanel, cx } from '../../ui/primitives';
import { decidedByOther, forSomeoneElse, nameOf, useViewerId } from './people';

/*
 * Claude asks the person something (D143): pick one answer, tick several, or
 * write one of their own. The answer goes to Claude as the reply of whoever
 * gives it: a question asked of someone else (D179) says so, and anyone in the
 * conversation may answer it.
 */
export function QuestionCard({ action, conversation }: { action: AssistantAction; conversation: Conversation }) {
  const q = action.question!;
  const answer = useAssistantOp('assistant.act');
  const id = useId();
  const me = useViewerId();
  const asked = forSomeoneElse(action, me) ? nameOf(action.forName) : '';
  const answeredBy = decidedByOther(action, me);
  const [picked, setPicked] = useState<number[]>([]);
  const [text, setText] = useState('');
  const pending = action.state === 'pending';
  const runnable = pending && conversation.here;
  const choices = q.choices ?? [];
  const free = choices.length === 0;
  const ready = picked.length > 0 || (text.trim() !== '' && (free || q.other));
  const toggle = (n: number) =>
    setPicked((p) => (q.multiple ? (p.includes(n) ? p.filter((x) => x !== n) : [...p, n].sort((a, b) => a - b)) : p.includes(n) ? [] : [n]));
  const send = (dismiss: boolean) =>
    answer.mutate(dismiss ? { id: conversation.id, actionId: action.id, dismiss: true } : { id: conversation.id, actionId: action.id, answer: { choices: picked, text: text.trim() } });

  return (
    <article
      className={cx('rounded-[14px] p-3.5 hairline', pending ? 'bg-raised shadow-[inset_0_0_0_1px_var(--accent-line)]' : 'bg-raised')}
      aria-label={asked ? `Claude asks ${asked}: ${q.text}` : `Claude asks you: ${q.text}`}
      data-waiting={runnable ? action.id : undefined}
      data-question
    >
      <div className="flex items-start gap-2.5">
        <span className={cx('mt-[1px] grid h-7 w-7 shrink-0 place-items-center rounded-[9px]', pending ? 'bg-accent-soft text-accent-text' : 'bg-hover text-subtle')} aria-hidden>
          <ChatCircleDots size={15} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <h4 className="min-w-0 text-[13.5px] font-semibold leading-snug text-fg" id={`${id}-q`}>
              {q.text}
            </h4>
            {pending && asked ? (
              <Badge tone="neutral">Waiting for {asked}</Badge>
            ) : pending ? (
              <Badge tone="accent">Needs you</Badge>
            ) : action.state === 'done' ? (
              <Badge tone="ok" icon={<CheckCircle size={12} weight="fill" />}>
                Answered
              </Badge>
            ) : (
              <Badge tone="neutral">Not now</Badge>
            )}
          </div>
          {q.why ? <p className="mt-0.5 text-[12.5px] leading-relaxed text-muted">{q.why}</p> : null}
          {!pending && action.decidedAt ? (
            <p className="mt-0.5 text-[12px] text-subtle" data-decided-state={action.state}>
              {action.state === 'done' ? `Answered by ${answeredBy || 'you'}` : `${answeredBy || 'You'} said not now`}
              {' · '}
              <time dateTime={action.decidedAt} title={absolute(action.decidedAt)} data-card-time>
                {stamp(action.decidedAt)}
              </time>
            </p>
          ) : null}
        </div>
      </div>

      {runnable ? (
        <fieldset className="mt-3 space-y-1.5" aria-labelledby={`${id}-q`}>
          <legend className="sr-only">{q.multiple ? 'Pick one or more' : 'Pick one'}</legend>
          {choices.map((c, i) => {
            const n = i + 1;
            const on = picked.includes(n);
            return (
              <label
                key={n}
                className={cx(
                  'flex cursor-pointer items-start gap-2.5 rounded-[10px] px-3 py-2 text-[13px] leading-snug hairline transition-colors',
                  on ? 'bg-accent-soft text-fg shadow-[inset_0_0_0_1px_var(--accent-line)]' : 'bg-panel text-fg hover:bg-hover',
                )}
              >
                <input
                  type={q.multiple ? 'checkbox' : 'radio'}
                  name={`${id}-choice`}
                  checked={on}
                  onChange={() => toggle(n)}
                  className="mt-[3px] h-3.5 w-3.5 shrink-0 accent-[var(--accent)]"
                />
                <span className="min-w-0">{c}</span>
              </label>
            );
          })}
          {free || q.other ? (
            <div>
              <label htmlFor={`${id}-text`} className="mb-1 block text-[12px] text-subtle">
                {free ? 'Your answer' : 'Or in your own words'}
              </label>
              <textarea
                id={`${id}-text`}
                rows={2}
                maxLength={2000}
                value={text}
                onChange={(e) => setText(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && ready) {
                    e.preventDefault();
                    send(false);
                  }
                }}
                className="w-full resize-y rounded-[10px] bg-panel px-3 py-2 text-[13px] leading-relaxed text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-offset-0 focus:outline-accent"
                placeholder={free ? 'Type your answer' : 'Something else'}
              />
            </div>
          ) : null}
        </fieldset>
      ) : action.state === 'done' && action.result ? (
        <p className="mt-2.5 text-[12.5px] leading-relaxed text-muted">
          <span className="text-subtle">{answeredBy ? `${answeredBy}’s answer: ` : 'Your answer: '}</span>
          {action.result}
        </p>
      ) : action.state === 'dismissed' && answeredBy ? (
        <p className="mt-2.5 text-[12px] text-subtle">{answeredBy} said not now.</p>
      ) : pending && !conversation.here ? (
        <p className="mt-2.5 text-[12px] text-subtle">You can answer this on {conversation.installationName}.</p>
      ) : null}

      {answer.error ? <ErrorPanel error={answer.error} compact className="mt-2.5" title="Could not send that" /> : null}
      {runnable ? (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <Button size="sm" variant="primary" loading={answer.isPending && !answer.variables?.dismiss} disabled={!ready || answer.isPending} onClick={() => send(false)}>
            Send answer
          </Button>
          <Button size="sm" variant="ghost" disabled={answer.isPending} onClick={() => send(true)}>
            Not now
          </Button>
        </div>
      ) : null}
    </article>
  );
}
