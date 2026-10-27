import { useQuery } from '@tanstack/react-query';
import { ApiError, request, toApiError } from './client';
import type { AuthMethod, AuthState, LinkAccept, LinkInfo, SignInRequest, SignInResult } from './types';

/*
 * Signing in (D183, docs/accounts.md). These endpoints work without a session,
 * so the sign-in and welcome pages use only them: nothing there asks for a
 * session that would answer "not signed in" and send the page in a circle.
 * Passwords go only in the request body: never in a URL, storage or a log.
 */

/** GET /api/auth/state: how this MetaRush signs people in, and whether this browser is signed in. */
export function getAuthState(signal?: AbortSignal): Promise<AuthState> {
  return request<AuthState>('GET', '/api/auth/state', { signal });
}

export function useAuthState(enabled = true) {
  return useQuery<AuthState, ApiError>({
    queryKey: ['auth.state'],
    queryFn: ({ signal }) => getAuthState(signal),
    staleTime: 30_000,
    enabled,
    retry: (n, err) => !toApiError(err).isUnreachable && n < 1,
  });
}

/** POST /api/auth/login with an email and password. */
export function signIn(input: SignInRequest): Promise<SignInResult> {
  return request<SignInResult>('POST', '/api/auth/login', { json: input });
}

/** POST /api/auth/logout: this browser only. */
export function signOut(): Promise<unknown> {
  return request<unknown>('POST', '/api/auth/logout', { json: {} });
}

/** GET /api/auth/link/{token}: what an invite or reset link is for. */
export function getLink(token: string, signal?: AbortSignal): Promise<LinkInfo> {
  return request<LinkInfo>('GET', `/api/auth/link/${encodeURIComponent(token)}`, { signal });
}

/** POST /api/auth/link/{token}: accept an invite (name and password) or a reset (password); signs this browser in. */
export function acceptLink(token: string, input: LinkAccept): Promise<SignInResult> {
  return request<SignInResult>('POST', `/api/auth/link/${encodeURIComponent(token)}`, { json: input });
}

/** The server's reason for a failed sign-in (`details.reason`): bad_credentials, throttled, link_gone, weak_password… */
export function reasonOf(err: unknown): string | undefined {
  const d = toApiError(err).details;
  if (d && typeof d === 'object' && 'reason' in d) {
    const r = (d as { reason?: unknown }).reason;
    if (typeof r === 'string') return r;
  }
  return undefined;
}

/** What a "not signed in" answer says about how to sign in (details.method, details.remote). */
export function signedOutWay(err: ApiError | null | undefined): { method?: AuthMethod; remote?: boolean } {
  const d = err?.details;
  if (!d || typeof d !== 'object') return {};
  const m = (d as { method?: unknown }).method;
  const r = (d as { remote?: unknown }).remote;
  return {
    method: m === 'single' || m === 'password' || m === 'tailscale' ? m : undefined,
    remote: typeof r === 'boolean' ? r : undefined,
  };
}

/** A failed sign-in in plain words: the server's message, then its hint. */
export function signInError(err: unknown): string {
  const e = toApiError(err);
  return [e.message, e.hint].filter(Boolean).join(' ');
}

/**
 * Where to go after signing in: `next` only when it is a path on this site
 * ("/settings/people#users"), never another site ("//elsewhere", "/\\elsewhere") or
 * the sign-in pages themselves.
 */
export function safeNext(next: string | null | undefined): string {
  if (!next || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return '/';
  try {
    const u = new URL(next, window.location.origin);
    if (u.origin !== window.location.origin) return '/';
    if (u.pathname === '/signin' || u.pathname.startsWith('/welcome/')) return '/';
    return u.pathname + u.search + u.hash;
  } catch {
    return '/';
  }
}

/** The current page as a `next` value for the sign-in page. */
export function signInPath(): string {
  const here = window.location.pathname + window.location.search + window.location.hash;
  return here && here !== '/' && !here.startsWith('/signin') ? `/signin?next=${encodeURIComponent(here)}` : '/signin';
}

/* ---------- Passwords: the rules people see (the server is the judge) ---------- */

export const PASSWORD_MIN = 12;
export const PASSWORD_MAX = 128;

/** How many characters a password has, counted as the server counts them (after NFKC). */
export function passwordLength(pw: string): number {
  return [...pw.normalize('NFKC')].length;
}

/** A plain line about a new password's length, or null when it is long enough. */
export function passwordLengthProblem(pw: string): string | null {
  const n = passwordLength(pw);
  if (n < PASSWORD_MIN) return `Use at least ${PASSWORD_MIN} characters (${n} so far). A few words together works well.`;
  if (n > PASSWORD_MAX) return `Use at most ${PASSWORD_MAX} characters.`;
  return null;
}

/** Labels for the sign-in methods, as people call them. */
export const METHOD_LABEL: Record<AuthMethod, string> = {
  single: 'Just me on this computer',
  password: 'Email and password',
  tailscale: 'Tailscale accounts',
};
