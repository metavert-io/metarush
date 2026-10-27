import { useEffect, useSyncExternalStore } from 'react';
import type { QueryClient, QueryKey } from '@tanstack/react-query';
import type { EventsHello, ServerEvent } from './types';

/*
 * Live updates: /api/events streams IDs of changed documents (change streams, or
 * polling on databases without them). We never trust payloads for data; we only
 * invalidate the matching queries so edits made from Claude Code on any machine
 * appear here within a moment.
 */

export type LiveState = 'connecting' | 'live' | 'offline';
interface LiveSnapshot {
  state: LiveState;
  mode?: string;
  lastEventAt?: number;
  retryInMs?: number;
}

let snapshot: LiveSnapshot = { state: 'connecting' };
const subs = new Set<() => void>();
function setSnapshot(next: Partial<LiveSnapshot>) {
  snapshot = { ...snapshot, ...next };
  subs.forEach((fn) => fn());
}
export function useLiveState(): LiveSnapshot {
  return useSyncExternalStore(
    (fn) => {
      subs.add(fn);
      return () => subs.delete(fn);
    },
    () => snapshot,
  );
}

type EventListener = (e: ServerEvent) => void;
const eventListeners = new Set<EventListener>();
/** Subscribe to raw server events (e.g. the upload manager listens for `upload`). */
export function onServerEvent(fn: EventListener): () => void {
  eventListeners.add(fn);
  return () => eventListeners.delete(fn);
}

/** Which queries an event makes stale. Keys are [opName, input?] prefixes. */
export function keysFor(e: ServerEvent, mode?: string): QueryKey[] {
  const keys: QueryKey[] = [];
  const project = e.projectId ? [{ id: e.projectId }] : [];
  // The activity bar summarizes uploads, jobs, renders, QA, workers and the assistant.
  if (['jobs', 'renders', 'worker_devices', 'ingest_sessions', 'upload', 'assistant', 'qa_reports', 'assets', 'settings'].includes(e.type)) keys.push(['activity.summary']);
  switch (e.type) {
    case 'projects':
      keys.push(['project.list'], e.id ? ['project.show', { id: e.id }] : ['project.show'], ['revision.list'], ['search'], ['review.waiting']);
      // What a project keeps (D166): its rules and sign-offs live on it; keeping one is recorded too.
      keys.push(['project.rules.list'], ['cut.signoffs.list'], ['keep.list']);
      // Whose Content Settings it uses (D192) is on the project too.
      keys.push(['content.show'], ['content.rules.list'], ['brand.show'], ['transcript.spelling.list'], ['provider.status']);
      break;
    case 'assets':
      keys.push(['asset.list'], ['project.show'], ['project.list'], ['footage.moments'], ['search']);
      // The Library's views, collections and the Trash (D96–D98).
      keys.push(['library.list'], ['collection.list'], ['collection.show'], ['trash.list'], ['asset.where']);
      if (e.id) keys.push(['asset.show', { id: e.id }], ['footage.transcript'], ['footage.evidence', { assetId: e.id }], ['peaks', e.id], ['footage.speakers', { assetId: e.id }]);
      // What's on screen and scene descriptions land with the clip (analysis.visual / analysis.describe).
      if (e.id) keys.push(['footage.visuals', { assetId: e.id }], ['footage.keyframes', { assetId: e.id }]);
      // A transcript was fixed (D145): its history, what waits for new versions, captions and suggestions.
      keys.push(['transcript.history'], ['transcript.pending'], ['transcript.find'], ['captions.check'], ['captions.file'], ['transcript.spelling.suggestions']);
      break;
    case 'notifications':
      keys.push(['notification.list']);
      break;
    case 'collections':
      // A collection was made, renamed or deleted: its name shows in projects that use it.
      keys.push(['collection.list'], ['collection.show'], ['library.list'], ['project.show'], ['project.links']);
      break;
    case 'settings':
      // This daemon's watched folder, worker, storage or remote access settings changed (dashboard or CLI).
      keys.push(['watch.status'], ['worker.settings'], ['status'], ['remote.status'], ['session'], ['storage.status'], ['workspace.show'], ['render.follow.show']);
      break;
    case 'qa_reports':
      keys.push(['qa.show'], ['render.list'], ['render.status']);
      // A draft's review copy was checked (D249).
      keys.push(['review.waiting'], ['review.packet'], ['draft.list']);
      break;
    case 'edit_revisions':
      keys.push(['revision.list'], ['project.list'], ['revision.compare'], ['assistant', 'proposals'], ['review.waiting'], ['review.packet'], ['draft.list']);
      keys.push(project.length ? ['project.show', ...project] : ['project.show']);
      if (e.id) keys.push(['revision.show', { id: e.id }]);
      keys.push(['transcript.pending'], ['captions.check']);
      break;
    case 'renders':
      keys.push(['render.list'], ['render.status'], ['revision.show'], ['render.follow.show']);
      keys.push(['review.waiting'], ['review.packet'], ['draft.list']); // a draft's review copy (D249)
      keys.push(project.length ? ['project.show', ...project] : ['project.show']);
      break;
    case 'jobs':
      // Job documents change on every progress tick; keep this to cheap queries.
      // Results land through their own collections (assets, renders).
      keys.push(['job.list'], ['render.status']);
      if (e.assetId) keys.push(['asset.show', { id: e.assetId }]);
      // The polling fallback only sees renders when they are created, never their
      // state changes; job updates are the only signal that a render progressed.
      if (mode === 'polling') keys.push(['render.list'], e.projectId ? ['project.show', { id: e.projectId }] : ['project.show']);
      break;
    case 'comments':
      keys.push(['comment.list']);
      break;
    case 'review_links':
      // A review link was made, ended, or brought comments in (D314, D315).
      keys.push(['share.list']);
      break;
    case 'worker_devices':
      keys.push(['worker.list'], ['status'], ['worker.settings']);
      break;
    case 'ingest_sessions':
      keys.push(['ingest.sessions'], ['status']);
      break;
    case 'upload':
      keys.push(['ingest.sessions']);
      break;
    case 'assistant':
      // A dashboard-assistant request started or changed state (any tab); a "Keep this?" card was decided.
      keys.push(['assistant'], ['keep.list']);
      break;
    case 'todos':
      // Job details, activity rows and the bell say how many questions a clip has.
      keys.push(['todos.list'], ['todos.counts'], ['todos.show'], ['todos.groups'], ['job.show'], ['activity.summary'], ['notification.list']);
      // An answered "Is this …?" names a face: the readout and the person's faces change.
      keys.push(['footage.visuals'], ['footage.faces'], ['people.show'], ['people.learned']);
      break;
    case 'asset_speakers':
      keys.push(['footage.speakers'], ['footage.transcript'], ['people.list'], ['people.show'], ['people.learned'], ['search']);
      break;
    case 'people':
      keys.push(['people.list'], ['people.show'], ['people.learned'], ['footage.speakers'], ['search']);
      break;
    case 'generations':
      // Generated video (D123–D127): a shot was asked for, sent, made or ended; its money settled.
      keys.push(['generate.list'], ['generate.show'], ['generate.settings'], ['looks.show'], ['ai.list'], ['ai.show'], ['ai.voices']);
      break;
    case 'looks':
      keys.push(['looks.list'], ['looks.show']);
      break;
    case 'consents':
      keys.push(['consent.list'], ['looks.list'], ['looks.show']);
      break;
    case 'remotion_sources':
    case 'remotion_versions':
    case 'remotion_renders':
      // Remotion sources (D131–D138): brought in, a version checked or decided, a clip made.
      keys.push(['remotion.list'], ['remotion.show']);
      break;
    case 'spelling_entries':
      // Spelling lists (D147): the lists, what they'd change, and the caption check's names.
      keys.push(['transcript.spelling.list'], ['transcript.spelling.suggestions'], ['captions.check'], ['content.show']);
      break;
    case 'content_defaults':
    case 'content_settings':
      // Content Settings (D192, D193): the brand, the voice and house rules of every project using them.
      keys.push(['content.show'], ['content.rules.list'], ['brand.show'], ['provider.status'], ['project.show']);
      break;
    case 'recipes':
    case 'recipe_versions': // Recipes (D201): a recipe saved, changed, made current or archived
      keys.push(['recipe.list'], ['recipe.show'], ['recipe.diff']);
      break;
    case 'recipe_runs':
      // A run of Recipes moved (D204): its page, the cards' last runs, "Made with…", the To-dos it asks and two runs side by side (D212).
      keys.push(['recipe.runs'], ['recipe.list'], ['recipe.show'], ['recipe.compare'], ['activity.summary']);
      break;
    case 'drafts':
      // An AI app's draft was saved, or a person decided about one (D248, D249): the review desk and the draft's page.
      keys.push(['review.waiting'], ['draft.list'], ['review.packet'], ['revision.list'], ['revision.show']);
      keys.push(project.length ? ['project.show', ...project] : ['project.show']);
      break;
    default:
      break;
  }
  return keys;
}

/**
 * Connects once per app. Reconnects with exponential backoff; after any gap the
 * whole cache is refetched, because events missed while offline are gone.
 */
export function useLiveEvents(qc: QueryClient, enabled: boolean) {
  useEffect(() => {
    if (!enabled || typeof EventSource === 'undefined') return;
    let es: EventSource | null = null;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let flushTimer: ReturnType<typeof setTimeout> | null = null;
    let attempt = 0;
    let everConnected = false;
    let closed = false;
    const pending = new Map<string, QueryKey>();

    const flush = () => {
      flushTimer = null;
      const keys = [...pending.values()];
      pending.clear();
      for (const queryKey of keys) void qc.invalidateQueries({ queryKey });
    };

    const connect = () => {
      if (closed) return;
      setSnapshot({ state: 'connecting', retryInMs: undefined });
      es = new EventSource('/api/events', { withCredentials: true });
      es.addEventListener('hello', (ev) => {
        let mode: string | undefined;
        try {
          mode = (JSON.parse((ev as MessageEvent<string>).data) as EventsHello).mode || undefined;
        } catch {
          mode = undefined;
        }
        if (everConnected && attempt > 0) void qc.invalidateQueries(); // catch up on anything missed
        everConnected = true;
        attempt = 0;
        setSnapshot({ state: 'live', mode });
      });
      es.onmessage = (ev: MessageEvent<string>) => {
        let e: ServerEvent;
        try {
          e = JSON.parse(ev.data) as ServerEvent;
        } catch {
          return;
        }
        setSnapshot({ lastEventAt: Date.now() });
        eventListeners.forEach((fn) => fn(e));
        for (const k of keysFor(e, snapshot.mode)) pending.set(JSON.stringify(k), k);
        if (!flushTimer) flushTimer = setTimeout(flush, 250);
      };
      es.onerror = () => {
        es?.close();
        es = null;
        attempt += 1;
        const delay = Math.min(1000 * 2 ** (attempt - 1), 30_000);
        setSnapshot({ state: attempt > 1 || everConnected ? 'offline' : 'connecting', retryInMs: delay });
        timer = setTimeout(connect, delay);
      };
    };

    const onOnline = () => {
      if (!es) {
        if (timer) clearTimeout(timer);
        attempt = Math.max(attempt, 1);
        connect();
      }
    };
    window.addEventListener('online', onOnline);
    connect();
    return () => {
      closed = true;
      window.removeEventListener('online', onOnline);
      if (timer) clearTimeout(timer);
      if (flushTimer) clearTimeout(flushTimer);
      es?.close();
    };
  }, [qc, enabled]);
}
