import { useEffect, useSyncExternalStore } from 'react';
import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query';
import { ApiError, call, request, toApiError } from './client';
import type {
  AssistantEvent,
  AssistantModelsOut,
  AssistantPartial,
  AssistantSettingsOut,
  AssistantStatus,
  Conversation,
  ConversationListIn,
  ConversationProposals,
  OpInput,
  OpOutput,
  RemoteOp,
} from './types';

/*
 * The Claude Code panel's data: availability (GET /api/assistant), the
 * assistant.* ops (conversations, settings, suggestions, proposals) and one
 * live stream per open conversation (SSE). Streams live in a module store
 * outside React, so switching views, closing the drawer or going full screen
 * never drops one; after a reload the stream reconnects from the last event.
 * Query keys all start with 'assistant' so server events can refresh them.
 */

const base = '/api/assistant';

export function useAssistantStatus(enabled = true) {
  return useQuery<AssistantStatus, ApiError>({
    queryKey: ['assistant', 'status'],
    queryFn: ({ signal }) => request<AssistantStatus>('GET', base, { signal }),
    staleTime: 30_000,
    refetchInterval: 60_000,
    enabled,
  });
}

/** Re-runs the availability checks (no prompt is sent). */
export async function refreshAssistantStatus(): Promise<AssistantStatus> {
  return request<AssistantStatus>('GET', `${base}?refresh=1`);
}

export function useConversations(input: ConversationListIn, enabled = true) {
  return useQuery({
    queryKey: ['assistant', 'list', input],
    queryFn: ({ signal }) => call('assistant.list', input, signal),
    staleTime: 10_000,
    enabled,
  });
}

export function useSuggestions(projectId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ['assistant', 'suggestions', projectId ?? ''],
    queryFn: ({ signal }) => call('assistant.suggestions', projectId ? { projectId } : {}, signal),
    staleTime: 60_000,
    enabled,
  });
}

export function useModels(enabled = true) {
  return useQuery<AssistantModelsOut, ApiError>({
    queryKey: ['assistant', 'models'],
    queryFn: ({ signal }) => call('assistant.models', {}, signal),
    staleTime: 10 * 60_000,
    enabled,
  });
}

export function useAssistantSettings(enabled = true) {
  return useQuery<AssistantSettingsOut, ApiError>({
    queryKey: ['assistant', 'settings'],
    queryFn: ({ signal }) => call('assistant.settings', {}, signal),
    staleTime: 30_000,
    enabled,
  });
}

export function useConversationProposals(id: string | undefined, enabled = true) {
  return useQuery<ConversationProposals, ApiError>({
    queryKey: ['assistant', 'proposals', id],
    queryFn: ({ signal }) => call('assistant.proposals', { id: id ?? '' }, signal),
    enabled: !!id && enabled,
    staleTime: 15_000,
  });
}

/** A mutation for one assistant op; every assistant query refreshes afterwards. */
export function useAssistantOp<K extends RemoteOp>(name: K) {
  const qc = useQueryClient();
  return useMutation<OpOutput<K>, ApiError, OpInput<K>>({
    mutationFn: (input) => call(name, input),
    onSuccess: (out) => {
      const conv = conversationOf(out);
      if (conv) primeConversation(conv);
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: ['assistant'] }),
  });
}

function conversationOf(out: unknown): Conversation | undefined {
  if (!out || typeof out !== 'object') return undefined;
  const o = out as { conversation?: Conversation; id?: string; turns?: unknown };
  if (o.conversation && typeof o.conversation === 'object') return o.conversation;
  if (typeof o.id === 'string' && 'state' in o && 'installationId' in o) return out as Conversation;
  return undefined;
}

/* ---------- Live conversation streams ---------- */

export interface StreamState {
  conversation?: Conversation;
  events: AssistantEvent[];
  partial?: AssistantPartial;
  loading: boolean;
  /** The SSE connection is open. */
  connected: boolean;
  error?: ApiError;
}

const empty: StreamState = { events: [], loading: false, connected: false };

class Stream {
  state: StreamState = { events: [], loading: true, connected: false };
  private subs = new Set<() => void>();
  private es: EventSource | null = null;
  private refs = 0;
  private closeTimer: ReturnType<typeof setTimeout> | null = null;
  private started = false;
  private lastSeq = 0;

  constructor(
    readonly id: string,
    readonly raw: boolean,
  ) {}

  subscribe = (fn: () => void) => {
    this.subs.add(fn);
    return () => {
      this.subs.delete(fn);
    };
  };

  get = () => this.state;

  private set(patch: Partial<StreamState>) {
    this.state = { ...this.state, ...patch };
    this.subs.forEach((fn) => fn());
  }

  /** Keeps the stream open while someone shows it (and for a minute after). */
  retain() {
    this.refs++;
    if (this.closeTimer) {
      clearTimeout(this.closeTimer);
      this.closeTimer = null;
    }
    if (!this.started) {
      this.started = true;
      void this.load();
    } else if (!this.es) {
      this.follow();
    }
    return () => {
      this.refs--;
      if (this.refs <= 0) {
        this.closeTimer = setTimeout(() => this.close(), 60_000);
      }
    };
  }

  private async load() {
    try {
      const conv = await call('assistant.show', { id: this.id, raw: this.raw });
      const events = conv.events ?? [];
      this.lastSeq = events.length ? events[events.length - 1]!.seq : 0;
      this.set({ conversation: { ...conv, events: undefined }, events, loading: false, error: undefined });
      this.follow();
    } catch (err) {
      this.set({ loading: false, error: toApiError(err) });
    }
  }

  private follow() {
    if (this.es || typeof EventSource === 'undefined') return;
    const url = `${base}/conversations/${encodeURIComponent(this.id)}/events?after=${this.lastSeq}${this.raw ? '&raw=1' : ''}`;
    const es = new EventSource(url, { withCredentials: true });
    this.es = es;
    es.onopen = () => this.set({ connected: true });
    es.onmessage = (m: MessageEvent<string>) => {
      let e: AssistantEvent;
      try {
        e = JSON.parse(m.data) as AssistantEvent;
      } catch {
        return;
      }
      if (e.seq <= this.lastSeq) return;
      this.lastSeq = e.seq;
      const partial = e.kind === 'text' || e.kind === 'turn_end' ? undefined : this.state.partial;
      this.set({ events: [...this.state.events, e], partial });
    };
    es.addEventListener('meta', (m) => {
      try {
        this.applyMeta(JSON.parse((m as MessageEvent<string>).data) as Conversation);
      } catch {
        /* ignore a malformed message */
      }
    });
    es.addEventListener('partial', (m) => {
      let p: AssistantPartial;
      try {
        p = JSON.parse((m as MessageEvent<string>).data) as AssistantPartial;
      } catch {
        return;
      }
      const cur = this.state.partial;
      const text = p.from > 0 && cur && cur.block === p.block ? cur.text + p.text : p.text;
      this.set({ partial: { ...p, text } });
    });
    es.onerror = () => {
      // EventSource reconnects by itself (sending Last-Event-ID); a lost session ends it.
      if (es.readyState === EventSource.CLOSED) {
        this.es = null;
        this.set({ connected: false });
        if (this.refs > 0) setTimeout(() => this.follow(), 3000);
      } else {
        this.set({ connected: false });
      }
    };
  }

  applyMeta(conv: Conversation) {
    if (conv.id !== this.id) return;
    this.set({ conversation: { ...conv, events: undefined } });
  }

  close() {
    this.es?.close();
    this.es = null;
    this.set({ connected: false });
  }
}

const streams = new Map<string, Stream>();

function streamFor(id: string, raw = false): Stream {
  const key = id + (raw ? ':raw' : '');
  let s = streams.get(key);
  if (!s) {
    s = new Stream(id, raw);
    streams.set(key, s);
  }
  return s;
}

/** Applies a conversation snapshot (from an op's answer) to its open streams. */
export function primeConversation(conv: Conversation) {
  for (const raw of [false, true]) {
    const s = streams.get(`${conv.id}${raw ? ':raw' : ''}`);
    s?.applyMeta(conv);
  }
}

/** The live state of one conversation (loads it, then follows its SSE stream). */
export function useConversationStream(id: string | undefined, raw = false): StreamState {
  const s = id ? streamFor(id, raw) : undefined;
  const state = useSyncExternalStore(s ? s.subscribe : noopSubscribe, s ? s.get : getEmpty);
  useEffect(() => {
    if (!s) return;
    return s.retain();
  }, [s]);
  return state;
}

const noopSubscribe = () => () => {};
const getEmpty = () => empty;

/** Server events for the assistant: refresh lists and open conversations' proposals. */
export function invalidateAssistant(qc: QueryClient) {
  void qc.invalidateQueries({ queryKey: ['assistant'] });
}
