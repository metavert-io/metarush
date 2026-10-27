import { useSyncExternalStore } from 'react';
import { openAskWith, setAskOpen } from '../../lib/workbench';

/*
 * The Claude Code panel's view state, outside the component tree: which
 * conversation is open, drawer or full screen, list or thread, and the raw
 * view. It survives closing the drawer and (per tab) reloads; the raw toggle,
 * prompt history and whose conversations the lists show are remembered per
 * viewer. Storage access is best effort.
 *
 * One project per conversation (D185): the panel follows the project in
 * view. Opening another project shows that project's conversation this tab
 * last had open, else its most recent one, else a fresh start there; a page
 * outside any project keeps what is open.
 */

export interface PanelState {
  conversationId?: string;
  /** The open conversation's project ('' the whole workspace), or where a fresh start is. */
  projectKey?: string;
  fullscreen: boolean;
  view: 'thread' | 'history';
  /** Show the exact prompts sent, the rules and raw tool input/output. */
  raw: boolean;
}

const SESSION_KEY = 'mr.assistant.panel';
const BY_PROJECT_KEY = 'mr.assistant.byProject';
const RAW_KEY = 'mr.assistant.raw';
const HISTORY_KEY = 'mr.assistant.history';
const DRAFT_PREFIX = 'mr.assistant.draft.';

function read<T>(storage: () => Storage, key: string, fallback: T): T {
  try {
    const v = storage().getItem(key);
    return v == null ? fallback : (JSON.parse(v) as T);
  } catch {
    return fallback;
  }
}

function write(storage: () => Storage, key: string, value: unknown) {
  try {
    if (value === undefined) storage().removeItem(key);
    else storage().setItem(key, JSON.stringify(value));
  } catch {
    /* private mode or storage disabled: the panel still works for this page */
  }
}

const session = () => window.sessionStorage;
const local = () => window.localStorage;

const saved = read<Partial<PanelState> & { open?: boolean }>(session, SESSION_KEY, {});
let state: PanelState = {
  conversationId: saved.conversationId,
  projectKey: typeof saved.projectKey === 'string' ? saved.projectKey : undefined,
  fullscreen: !!saved.fullscreen,
  view: saved.view === 'history' ? 'history' : 'thread',
  raw: read<boolean>(local, RAW_KEY, false),
};
const subs = new Set<() => void>();

function persist(open?: boolean) {
  const prev = read<{ open?: boolean }>(session, SESSION_KEY, {});
  write(session, SESSION_KEY, { conversationId: state.conversationId, projectKey: state.projectKey, fullscreen: state.fullscreen, view: state.view, open: open ?? prev.open });
  write(local, RAW_KEY, state.raw);
}

export function setPanel(patch: Partial<PanelState>) {
  state = { ...state, ...patch };
  persist();
  subs.forEach((fn) => fn());
}

export function usePanel(): PanelState {
  return useSyncExternalStore(
    (fn) => {
      subs.add(fn);
      return () => {
        subs.delete(fn);
      };
    },
    () => state,
  );
}

export const getPanel = () => state;

/** Remembers whether the drawer is open, so a reload brings it back. */
export function rememberOpen(open: boolean) {
  persist(open);
}

/** Reopens the panel after a reload when it was open. */
export function restorePanel() {
  if (read<{ open?: boolean }>(session, SESSION_KEY, {}).open) setAskOpen(true);
}

/** Opens a conversation (its project, when known, is remembered for this tab). */
export function openConversation(id: string, projectKey?: string) {
  if (projectKey !== undefined) rememberForProject(projectKey, id);
  // Without a project, it is known once the conversation loads (syncConversationProject).
  setPanel({ conversationId: id, view: 'thread', projectKey: projectKey ?? (state.conversationId === id ? state.projectKey : undefined) });
}

/** A fresh start, in the project in view unless another is named. */
export function newConversation(projectKey?: string) {
  setPanel({ conversationId: undefined, view: 'thread', projectKey: projectKey ?? pageProject });
}

/* ---------- Following the project in view (D185) ---------- */

/** The project of the page in view ('' outside any project). */
let pageProject = '';
export function setPageProject(key: string) {
  pageProject = key;
}

/** The conversation this tab last had open in each project ('' the whole workspace). */
export function byProject(): Record<string, string> {
  const m = read<unknown>(session, BY_PROJECT_KEY, {});
  return m && typeof m === 'object' && !Array.isArray(m) ? (m as Record<string, string>) : {};
}

export function rememberForProject(key: string, id: string) {
  const m = byProject();
  if (m[key] === id) return;
  write(session, BY_PROJECT_KEY, { ...m, [key]: id });
}

/** Forgets a conversation this tab remembered for a project (it is gone or archived). */
export function forgetForProject(key: string) {
  const m = byProject();
  if (!(key in m)) return;
  delete m[key];
  write(session, BY_PROJECT_KEY, m);
}

/** The open conversation loaded: remember its project (it never changes). */
export function syncConversationProject(id: string, key: string) {
  rememberForProject(key, id);
  if (state.conversationId === id && state.projectKey !== key) setPanel({ projectKey: key });
}

/**
 * Opens the panel on a project with a fresh conversation and the text in its
 * box ("Send to Claude" after turning something down, D186). The caller shows
 * the project's page, so the message carries it.
 */
export function askInProject(projectId: string | undefined, text: string) {
  setPanel({ conversationId: undefined, view: 'thread', projectKey: projectId ?? '' });
  openAskWith(text);
}

/* ---------- Whose conversations a list shows (D179) ---------- */

/**
 * Everyone's conversations, or just the viewer's: remembered per viewer once
 * they pick one (until then a project's list is everyone's and the
 * workspace-wide list theirs, the server's defaults, as on the command line).
 */
export type Whose = 'everyone' | 'mine';
const WHOSE_KEY = 'mr.assistant.whose';
const readWhose = (): Whose | undefined => {
  const v = read<unknown>(local, WHOSE_KEY, undefined);
  return v === 'everyone' || v === 'mine' ? v : undefined;
};
let whose: Whose | undefined = readWhose();
const whoseSubs = new Set<() => void>();

export function setWhose(v: Whose) {
  whose = v;
  write(local, WHOSE_KEY, v);
  whoseSubs.forEach((fn) => fn());
}

export function useWhose(): Whose | undefined {
  return useSyncExternalStore(
    (fn) => {
      whoseSubs.add(fn);
      return () => {
        whoseSubs.delete(fn);
      };
    },
    () => whose,
  );
}

/** The list's input for a choice (none: the server's default for the list). */
export function whoseInput(w: Whose | undefined): { allUsers?: boolean; mine?: boolean } {
  return w === 'everyone' ? { allUsers: true } : w === 'mine' ? { mine: true } : {};
}

/* ---------- Prompt history and drafts ---------- */

export function promptHistory(): string[] {
  const h = read<unknown>(local, HISTORY_KEY, []);
  return Array.isArray(h) ? h.filter((x): x is string => typeof x === 'string') : [];
}

export function rememberPrompt(p: string) {
  const t = p.trim();
  if (!t || t.startsWith('/')) return;
  const h = promptHistory().filter((x) => x !== t);
  h.push(t);
  write(local, HISTORY_KEY, h.slice(-50));
}

export function loadDraft(id: string | undefined): string {
  return read<string>(local, DRAFT_PREFIX + (id ?? 'new'), '');
}

export function saveDraft(id: string | undefined, text: string) {
  write(local, DRAFT_PREFIX + (id ?? 'new'), text ? text : undefined);
}
