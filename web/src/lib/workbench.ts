import { useEffect, useSyncExternalStore } from 'react';

/*
 * Small app-wide stores:
 *  - selection: what the person is looking at, used to compose the Ask Claude Code prompt
 *  - drop target: where a global file drop goes (the open project, else the inbox)
 *  - ask panel open state
 */

export interface Selection {
  projectId?: string;
  projectName?: string;
  headRevisionId?: string;
  headRevisionNumber?: number;
  revisionId?: string;
  revisionNumber?: number;
  assetId?: string;
  assetName?: string;
  /** Source seconds within assetId, or timeline seconds within revisionId. */
  time?: number;
  timeBase?: 'source' | 'timeline';
  clipId?: string;
  compare?: { left: string; right: string; leftNumber?: number; rightNumber?: number };
}

function store<T>(initial: T) {
  let value = initial;
  const subs = new Set<() => void>();
  return {
    get: () => value,
    set(next: T) {
      value = next;
      subs.forEach((fn) => fn());
    },
    subscribe(fn: () => void) {
      subs.add(fn);
      return () => {
        subs.delete(fn);
      };
    },
  };
}

const selection = store<Selection>({});
export function useSelection(): Selection {
  return useSyncExternalStore(selection.subscribe, selection.get);
}
export function updateSelection(patch: Partial<Selection>) {
  const cur = selection.get();
  let changed = false;
  for (const k of Object.keys(patch) as (keyof Selection)[]) {
    if (JSON.stringify(cur[k]) !== JSON.stringify(patch[k])) changed = true;
  }
  if (changed) selection.set({ ...cur, ...patch });
}
export function resetSelection(next: Selection = {}) {
  selection.set(next);
}

export interface DropTarget {
  projectId?: string;
  label: string;
}
const dropTarget = store<DropTarget>({ label: 'Inbox' });
export function useDropTarget(): DropTarget {
  return useSyncExternalStore(dropTarget.subscribe, dropTarget.get);
}
/** Screens call this to route global drops into their project while mounted. */
export function useProvideDropTarget(projectId: string | undefined, label: string | undefined) {
  useEffect(() => {
    if (!projectId || !label) return;
    dropTarget.set({ projectId, label });
    return () => dropTarget.set({ label: 'Inbox' });
  }, [projectId, label]);
}
export const getDropTarget = dropTarget.get;

const askOpen = store(false);
export function useAskOpen(): [boolean, (v: boolean) => void] {
  const v = useSyncExternalStore(askOpen.subscribe, askOpen.get);
  return [v, askOpen.set];
}
export const setAskOpen = askOpen.set;
/** Whether the Claude Code panel is open (outside React: toasts, keyboard handlers). */
export const getAskOpen = askOpen.get;

/**
 * A prepared request for the Claude Code panel ("suggest a voiceover script for
 * this cut"): opens the panel with the text in its input, in a new conversation
 * when asked. The person reads it, edits it if they like, and sends it.
 */
const askPrefill = store<{ text: string; nonce: number; fresh: boolean }>({ text: '', nonce: 0, fresh: false });
export function openAskWith(text: string, opts: { newConversation?: boolean } = {}) {
  askPrefill.set({ text, nonce: askPrefill.get().nonce + 1, fresh: !!opts.newConversation });
  askOpen.set(true);
}
export function useAskPrefill(): { text: string; nonce: number; fresh: boolean } {
  return useSyncExternalStore(askPrefill.subscribe, askPrefill.get);
}

/** A specific assistant run to show when the panel opens (deep links from notifications). */
const askRun = store<{ id?: string; nonce: number }>({ nonce: 0 });
export function openAskRun(id: string) {
  askRun.set({ id, nonce: askRun.get().nonce + 1 });
  askOpen.set(true);
}
export function useAskRunRequest(): { id?: string; nonce: number } {
  return useSyncExternalStore(askRun.subscribe, askRun.get);
}

const activityOpen = store(false);
/** The activity drawer (top bar button, activity strip, ?activity=open). */
export function useActivityOpen(): [boolean, (v: boolean) => void] {
  const v = useSyncExternalStore(activityOpen.subscribe, activityOpen.get);
  return [v, activityOpen.set];
}

/** Library view (grid or list), remembered per view in this browser. */
export type LibraryView = 'grid' | 'list';

const filePicker = store<{ nonce: number; folder: boolean }>({ nonce: 0, folder: false });
/** Ask the global drop zone to open the file (or folder) picker. */
export function requestFilePicker(folder = false) {
  filePicker.set({ nonce: filePicker.get().nonce + 1, folder });
}
export function useFilePickerRequests(): { nonce: number; folder: boolean } {
  return useSyncExternalStore(filePicker.subscribe, filePicker.get);
}

/**
 * Jobs whose outcome the person already saw on the screen that ran them (the
 * Voiceover tab shows "Cleaned up: …" itself). The bell keeps their
 * notification but marks it read instead of announcing it a second time.
 */
const seenJobs = new Set<string>();
export function markJobSeen(jobId: string | undefined) {
  if (jobId) seenJobs.add(jobId);
}
export function jobSeen(jobId: string | undefined): boolean {
  return !!jobId && seenJobs.has(jobId);
}
