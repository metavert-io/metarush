import { useMemo } from 'react';
import { useAuthState } from '../../api/auth';
import { useOp, useSession } from '../../api/queries';
import type { AssistantAction, AssistantEvent, Conversation, ConversationTurn } from '../../api/types';

/*
 * Whose conversations, messages and cards are (D179). Remote access signs
 * people in as themselves, so one conversation can hold several people. The
 * server says who started a conversation (userId; mine for the viewer), who
 * sent each message (a turn's userId), whom each card is for (forId: the
 * person whose message Claude was answering) and who decided it (byId). A card
 * counts only for its person (their badge, their navigator); everyone sees it
 * in the thread, marked with whom it waits for, and may decide it.
 */

/** The signed-in person ('' until the session is known). */
export function useViewerId(): string {
  return useSession().data?.user.id ?? '';
}

/** A person's name as shown ("someone else" when there is none). */
export const nameOf = (name?: string) => name?.trim() || 'someone else';

/** "You", or "Started by Maya". */
export function startedBy(c: Conversation): string {
  return c.mine ? 'You' : `Started by ${nameOf(c.userName)}`;
}

/** Whether a card is for someone other than the viewer. */
export function forSomeoneElse(a: AssistantAction, me: string): boolean {
  return !!me && !!a.forId && a.forId !== me;
}

/** Who decided a card when it was someone else ('' for the viewer, or when that isn't known). */
export function decidedByOther(a: AssistantAction, me: string): string {
  return me && a.byId && a.byId !== me ? nameOf(a.by) : '';
}

/** The cards waiting for other people than the viewer, and who they are ("others" for several). */
export function othersWaiting(c: Conversation, me: string): { count: number; whom: string } {
  const list = (c.waitingFor ?? []).filter((w) => w.count > 0 && w.userId !== me);
  const count = c.waitingFor ? list.reduce((n, w) => n + w.count, 0) : Math.max(0, (c.pendingActions ?? 0) - (c.waitingForYou ?? 0));
  const whom = list.length === 1 ? nameOf(list[0]!.userName) : 'others';
  return { count, whom };
}

/**
 * Who sent a message: "You", or their name. Messages from before D179 carry no
 * person: they are the person's who started the conversation, unless they name
 * someone else.
 */
export function senderOf(m: Pick<ConversationTurn | AssistantEvent, 'user' | 'userId'> | undefined, c: Conversation, me: string): string {
  if (m?.userId) return m.userId === me ? 'You' : nameOf(m.user);
  if (m?.user && c.userName && m.user !== c.userName) return nameOf(m.user);
  return c.userId === me ? 'You' : nameOf(m?.user || c.userName);
}

/** Whose message it is (their id; '' when that isn't known), by the same rules as senderOf. */
export function senderIdOf(m: Pick<ConversationTurn | AssistantEvent, 'user' | 'userId'> | undefined, c: Conversation): string {
  if (m?.userId) return m.userId;
  if (m?.user && c.userName && m.user !== c.userName) return '';
  return c.userId ?? '';
}

/**
 * People's pictures by id (D184), shown next to their messages while people
 * sign in (email and password, or Tailscale): from the workspace's people
 * list, cached; the viewer's own from the session. `on` is false with "just
 * me on this computer" (no pictures beside messages then).
 */
export function usePictures(): { on: boolean; of: (id: string) => string | undefined } {
  const state = useAuthState();
  const session = useSession();
  const on = state.data?.method === 'password' || state.data?.method === 'tailscale';
  const list = useOp('users.list', {}, { staleTime: 5 * 60_000, enabled: on });
  return useMemo(() => {
    const m = new Map<string, string>();
    for (const p of list.data?.people ?? []) if (p.avatar) m.set(p.id, p.avatar);
    const me = session.data?.user.id;
    if (me) {
      if (session.data?.avatar) m.set(me, session.data.avatar);
      else m.delete(me);
    }
    return { on, of: (id: string) => (id ? m.get(id) : undefined) };
  }, [on, list.data, session.data]);
}
