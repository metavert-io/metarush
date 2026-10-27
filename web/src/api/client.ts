import type { ApiErrorBody, OpInfo, OpInput, OpOutput, RemoteOp, SessionInfo, StatusOut } from './types';

/** Thrown for every failed request; mirrors the server's error envelope. */
export class ApiError extends Error {
  readonly code: string;
  readonly hint?: string;
  readonly details?: unknown;
  readonly status: number;

  constructor(body: ApiErrorBody, status: number) {
    super(body.message || body.code);
    this.name = 'ApiError';
    this.code = body.code;
    this.hint = body.hint;
    this.details = body.details;
    this.status = status;
  }

  /** The session cookie is missing or expired (as opposed to other E_UNAUTHORIZED uses). */
  get isSignedOut(): boolean {
    return this.code === 'E_UNAUTHORIZED' && /not signed in/i.test(this.message);
  }

  /** MetaRush on this computer (the daemon) could not be reached at all. */
  get isUnreachable(): boolean {
    return this.code === 'E_DAEMON_UNREACHABLE';
  }
}

export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  const message = err instanceof Error ? err.message : String(err);
  return new ApiError({ code: 'E_CLIENT', message }, 0);
}

type Listener = (e: ApiError) => void;
const signedOutListeners = new Set<Listener>();
const unreachableListeners = new Set<Listener>();
const reachableListeners = new Set<() => void>();

export function onSignedOut(fn: Listener): () => void {
  signedOutListeners.add(fn);
  return () => signedOutListeners.delete(fn);
}
export function onUnreachable(fn: Listener): () => void {
  unreachableListeners.add(fn);
  return () => unreachableListeners.delete(fn);
}
export function onReachable(fn: () => void): () => void {
  reachableListeners.add(fn);
  return () => reachableListeners.delete(fn);
}

const UNREACHABLE: ApiErrorBody = {
  code: 'E_DAEMON_UNREACHABLE',
  message: "MetaRush isn't answering on this computer.",
  hint: 'Ask Claude Code to open the MetaRush dashboard again (or start it from a terminal), then retry.',
  details: { command: 'metarush dashboard' },
};

export interface RequestOptions {
  headers?: Record<string, string>;
  body?: BodyInit | null;
  json?: unknown;
  signal?: AbortSignal;
}

/** Low-level request against the same-origin Go server. Returns the envelope's data. */
export async function request<T>(method: string, path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json', ...opts.headers };
  if (method !== 'GET' && method !== 'HEAD') headers['X-MetaRush-Client'] = 'dashboard'; // CSRF defense
  let body = opts.body ?? null;
  if (opts.json !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(opts.json);
  }
  let res: Response;
  try {
    res = await fetch(path, { method, headers, body, credentials: 'same-origin', signal: opts.signal });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    const e = new ApiError(UNREACHABLE, 0);
    unreachableListeners.forEach((fn) => fn(e));
    throw e;
  }
  let parsed: unknown = undefined;
  const text = await res.text();
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = undefined;
    }
  }
  const env = parsed as { ok?: boolean; data?: T; error?: ApiErrorBody } | undefined;
  if (!env || typeof env.ok !== 'boolean') {
    // Non-envelope answer: the dev proxy's "bad gateway" or a plain-text guard response.
    if (res.status >= 500 || res.status === 0) {
      const e = new ApiError(UNREACHABLE, res.status);
      unreachableListeners.forEach((fn) => fn(e));
      throw e;
    }
    if (res.ok) {
      reachableListeners.forEach((fn) => fn());
      return parsed as T;
    }
    throw new ApiError({ code: `E_HTTP_${res.status}`, message: text.trim() || `MetaRush couldn't do that (error ${res.status}).` }, res.status);
  }
  reachableListeners.forEach((fn) => fn());
  if (!env.ok) {
    const e = new ApiError(env.error ?? { code: 'E_INTERNAL', message: `MetaRush couldn't do that (error ${res.status}).` }, res.status);
    if (e.isSignedOut) signedOutListeners.forEach((fn) => fn(e));
    throw e;
  }
  return env.data as T;
}

/** Typed op call: `call('project.show', { id })` → ProjectShowOut. */
export function call<K extends RemoteOp>(name: K, input: OpInput<K>, signal?: AbortSignal): Promise<OpOutput<K>> {
  return request<OpOutput<K>>('POST', `/api/ops/${name}`, { json: input ?? {}, signal });
}

export function getStatus(signal?: AbortSignal): Promise<StatusOut> {
  return request<StatusOut>('GET', '/api/status', { signal });
}

export function listOps(signal?: AbortSignal): Promise<OpInfo[]> {
  return request<OpInfo[]>('GET', '/api/ops', { signal });
}

/** GET /api/session */
export function getSession(signal?: AbortSignal): Promise<SessionInfo> {
  return request<SessionInfo>('GET', '/api/session', { signal });
}

/** Media URLs (cookie-authenticated, Range-capable; may redirect to presigned storage). */
export const media = {
  asset: (id: string, variant: 'proxy' | 'thumbnail' | 'contact_sheet' | 'peaks' | 'original') =>
    `/media/assets/${encodeURIComponent(id)}/${variant}`,
  render: (id: string, download = false) => `/media/renders/${encodeURIComponent(id)}${download ? '?download=1' : ''}`,
  renderThumb: (id: string) => `/media/renders/${encodeURIComponent(id)}/thumbnail`,
  /** One still of a video at a source time (cut on the daemon, cached), w = width in pixels. */
  frame: (id: string, t: number, w = 320) => `/media/assets/${encodeURIComponent(id)}/frame?t=${Math.max(0, t).toFixed(1)}&w=${Math.round(w)}`,
  /** A few seconds of an asset's audio (at most 15 s), for listening to a voice. */
  snippet: (id: string, from: number, to: number) =>
    `/media/assets/${encodeURIComponent(id)}/snippet?from=${from.toFixed(3)}&to=${to.toFixed(3)}`,
  /** A motion graphic's contact sheet, test frame n or preview video: pictures only, never its code (D68). */
  motionSheet: (hash: string) => `/media/motion/${encodeURIComponent(hash)}/sheet`,
  motionFrame: (hash: string, n: number) => `/media/motion/${encodeURIComponent(hash)}/frame-${n}`,
  /** Remotion sources (D137): a check's still (before or after) and a picture of a composition. */
  remotionStill: (version: string, n: number, side: 'before' | 'after') => `/media/remotion/${encodeURIComponent(version)}/still/${n}/${side}`,
  remotionPoster: (version: string, composition: string) => `/media/remotion/${encodeURIComponent(version)}/poster/${encodeURIComponent(composition)}`,
  /** A scene's still in a version's map (D167). */
  remotionScene: (version: string, composition: string, n: number) => `/media/remotion/${encodeURIComponent(version)}/scene/${encodeURIComponent(composition)}/${n}`,
  motionPreview: (hash: string) => `/media/motion/${encodeURIComponent(hash)}/preview`,
  /** Its preview in another shape it was checked in (D164: the first shape's is motionPreview). */
  motionShapePreview: (hash: string, shape: string) => `/media/motion/${encodeURIComponent(hash)}/preview-${encodeURIComponent(shape)}`,
  /** A voice's sample, streamed through the daemon (the provider key never reaches the browser). */
  voicePreview: (provider: string, voice: string, library = false) =>
    `/api/providers/${encodeURIComponent(provider)}/voices/${encodeURIComponent(voice)}/preview${library ? '?library=1' : ''}`,
};
