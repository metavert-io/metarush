import { useMemo, useState } from 'react';
import { CaretDown, CaretRight, Checks, WarningCircle } from '@phosphor-icons/react';
import { useAssistantOp } from '../../api/assistant';
import type { AssistantAction, Conversation } from '../../api/types';
import { useDialog } from '../../ui/Dialog';
import { Lightbox } from '../../ui/Lightbox';
import { Badge, Button, ErrorPanel, cx } from '../../ui/primitives';
import { SignoffsWaiting, useSignoffReminder } from '../../components/SignoffReminder';
import { ActionCard, approvesVersion, blockedCard, CardPictures, cardPictures, labelFor } from './ActionCard';
import { NotQuiteBox } from './NotQuite';
import { forSomeoneElse, nameOf, useViewerId } from './people';

/*
 * When one reply leaves several things for the person to OK (two graphics to
 * approve and a website to bring in), they are one card (D144): see them all,
 * Approve all, or decide each. Approve all runs them in one go (approvals
 * first) and Claude hears about all of them in one message, once the work they
 * start has finished.
 */
export function CombinedCard({ actions, conversation, raw, onNavigate }: { actions: AssistantAction[]; conversation: Conversation; raw: boolean; onNavigate: () => void }) {
  const act = useAssistantOp('assistant.act');
  const dialog = useDialog();
  const signoffs = useSignoffReminder();
  const [open, setOpen] = useState<string | null>(null);
  // The row whose "Not quite…" note is being written (D186).
  const [notQuite, setNotQuite] = useState<string | null>(null);
  const ready = useMemo(() => actions.filter((a) => !blockedCard(a)), [actions]);
  // One reply's cards are for the person whose message it answered (D179).
  const me = useViewerId();
  const asked = actions[0] && forSomeoneElse(actions[0], me) ? nameOf(actions[0].forName) : '';

  const decide = async (cards: AssistantAction[], dismiss: boolean) => {
    if (!cards.length) return;
    if (!dismiss && cards.some((a) => a.humanOnly)) {
      const one = cards.length === 1 ? cards[0]! : undefined;
      // Approving a version while sign-offs are still waiting: a reminder in the same dialog (D166).
      const waiting = cards.some(approvesVersion) && conversation.projectId ? await signoffs.waiting(conversation.projectId) : [];
      const ok = await dialog.confirm({
        title: one ? one.title + '?' : `Do these ${cards.length} things?`,
        body: (
          <div className="space-y-2">
            {waiting.length ? (
              <p className="text-fg">
                <SignoffsWaiting waiting={waiting} question={one ? 'Approve anyway?' : 'Go ahead anyway?'} />
              </p>
            ) : null}
            {one ? (
              <p>{one.details?.[0] ?? 'This is your decision. Claude only suggested it.'}</p>
            ) : (
              <ul className="list-disc space-y-1 pl-5">
                {cards.map((a) => (
                  <li key={a.id}>{a.title}</li>
                ))}
              </ul>
            )}
          </div>
        ),
        confirmLabel: one ? (waiting.length ? 'Approve anyway' : labelFor(one)) : waiting.length ? 'Approve all anyway' : 'Approve all',
        cancelLabel: waiting.length ? 'Not yet' : undefined,
      });
      if (!ok) return;
    }
    // Each card then shows how it went, in place (no toast over the message box).
    act.mutate({ id: conversation.id, actionIds: cards.map((a) => a.id), dismiss: dismiss || undefined, reviewCopy: dismiss ? undefined : true });
  };

  return (
    <article className="rounded-[14px] bg-raised p-3.5 shadow-[inset_0_0_0_1px_var(--accent-line)] hairline" aria-label={`Claude asks for ${actions.length} things`} data-combined>
      <div className="flex items-start gap-2.5">
        <span className="mt-[1px] grid h-7 w-7 shrink-0 place-items-center rounded-[9px] bg-accent-soft text-accent-text" aria-hidden>
          <Checks size={15} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <h4 className="min-w-0 text-[13.5px] font-semibold leading-snug text-fg">
              {asked ? `Claude needs ${asked}’s OK on ${actions.length} things` : `Claude needs your OK on ${actions.length} things`}
            </h4>
            {asked ? <Badge tone="neutral">Waiting for {asked}</Badge> : <Badge tone="accent">Needs you</Badge>}
          </div>
          <p className="mt-0.5 text-[12.5px] leading-relaxed text-muted">Approve them all at once, or decide each. Claude hears about them in one message.</p>
        </div>
      </div>

      <ul className="mt-3 divide-y divide-line overflow-hidden rounded-[12px] bg-panel hairline">
        {actions.map((a) => {
          const expanded = open === a.id;
          const blocked = blockedCard(a);
          return (
            <li key={a.id} data-waiting={a.id} aria-label={`Claude asks: ${a.title}`} className="px-3 py-2.5">
              <div className="flex items-start gap-2">
                <button
                  type="button"
                  onClick={() => setOpen(expanded ? null : a.id)}
                  aria-expanded={expanded}
                  className="mt-[3px] shrink-0 rounded-[6px] p-0.5 text-subtle hover:bg-hover hover:text-fg"
                  aria-label={expanded ? `Hide the details of ${a.title}` : `Show the details of ${a.title}`}
                >
                  {expanded ? <CaretDown size={12} /> : <CaretRight size={12} />}
                </button>
                <div className="min-w-0 flex-1">
                  <p className="text-[13px] font-medium leading-snug text-fg">{a.title}</p>
                  {a.details?.[0] ? <p className="mt-0.5 line-clamp-2 text-[12px] leading-relaxed text-muted">{a.details[0]}</p> : null}
                  {a.setUp?.length ? <p className="mt-0.5 text-[12px] font-medium text-accent-text">One-time setup first</p> : null}
                  {blocked ? (
                    <p className="mt-0.5 flex items-center gap-1 text-[12px] text-danger">
                      <WarningCircle size={12} weight="fill" aria-hidden /> It can't be done yet: open it to see why.
                    </p>
                  ) : null}
                  {!expanded ? <RowPictures action={a} /> : null}
                </div>
                <div className="flex shrink-0 flex-wrap justify-end gap-1.5">
                  <Button size="sm" variant="secondary" disabled={act.isPending || blocked} onClick={() => void decide([a], false)}>
                    {a.savesVersion ? 'Save and render' : labelFor(a)}
                  </Button>
                  <Button size="sm" variant="ghost" disabled={act.isPending} onClick={() => setNotQuite(notQuite === a.id ? null : a.id)} aria-label={`Not quite: ${a.title}`} data-not-quite>
                    Not quite…
                  </Button>
                  <Button size="sm" variant="ghost" disabled={act.isPending} onClick={() => void decide([a], true)} aria-label={`Not now: ${a.title}`}>
                    Not now
                  </Button>
                </div>
              </div>
              {notQuite === a.id ? (
                <NotQuiteBox
                  busy={act.isPending}
                  error={act.variables?.notQuite ? act.error : null}
                  onCancel={() => setNotQuite(null)}
                  onSend={(note) => act.mutate({ id: conversation.id, actionId: a.id, notQuite: note }, { onSuccess: () => setNotQuite(null) })}
                />
              ) : null}
              {expanded ? (
                <div className="mt-2.5">
                  <ActionCard action={a} conversation={conversation} raw={raw} onNavigate={onNavigate} />
                </div>
              ) : null}
            </li>
          );
        })}
      </ul>

      {act.error && !act.variables?.notQuite ? <ErrorPanel error={act.error} compact className="mt-2.5" title="Could not do that" /> : null}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button size="sm" variant="primary" loading={act.isPending && !act.variables?.dismiss && !act.variables?.notQuite} disabled={act.isPending || ready.length === 0} onClick={() => void decide(ready, false)}>
          {ready.length === actions.length ? 'Approve all' : `Approve the ${ready.length} that can be done`}
        </Button>
        <Button size="sm" variant="ghost" disabled={act.isPending} onClick={() => void decide(actions, true)}>
          Not now for all
        </Button>
      </div>
    </article>
  );
}

/** A compact strip of a card's pictures (opens large), shown above its details. */
function RowPictures({ action }: { action: AssistantAction }) {
  const pictures = useMemo(() => cardPictures(action), [action]);
  const [box, setBox] = useState<number | null>(null);
  if (!pictures.length) return null;
  return (
    <div className={cx('max-w-[420px]')}>
      <CardPictures action={action} pictures={pictures} onOpen={setBox} />
      {box != null ? <Lightbox items={pictures} index={box} title={action.title} onClose={() => setBox(null)} /> : null}
    </div>
  );
}
