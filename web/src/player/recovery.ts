/*
 * Players that keep going when MetaRush's database, or MetaRush itself, stops
 * answering for a while (D180). A player that should be playing but shows no
 * new picture and receives no new bytes for STALL_MS, or that reports a media
 * error, asks the server what is wrong (one byte of the file it plays), says
 * so in a plain line instead of an endless "Buffering…", tries again with
 * growing pauses (RETRY_MS), and resumes where it was once the server answers.
 *
 * This file is plain logic (no React, no DOM types beyond fetch) so the rules
 * are tested in Node: web/scripts/recovery-test.mjs. The hooks that wire it to
 * players are useMediaRecovery.ts (video and audio elements) and
 * usePlayerRecovery.ts (the Remotion preview).
 */
import type { RenderPlan } from '../api/types';

/** No new picture and no new bytes for this long while playing is a stall. */
export const STALL_MS = 6000;

/** Pauses between tries (the last one repeats), each ±15 % so tabs don't try in step. */
export const RETRY_MS = [1000, 2000, 4000, 8000, 15000];

export type TroubleKind = 'reconnecting' | 'database' | 'failed';

/** What a player says while it can't play. */
export interface Trouble {
  kind: TroubleKind;
  /** For failed: what the server said, or the player's own words. */
  message?: string;
}

export const DATABASE_LINE = "MetaRush can't reach its database right now; it keeps trying.";
export const RECONNECTING_LINE = 'Reconnecting…';

/** The plain line a player shows for its trouble. */
export function troubleLine(t: Trouble): string {
  if (t.kind === 'database') return DATABASE_LINE;
  if (t.kind === 'failed') return t.message || "This can't be played right now.";
  return RECONNECTING_LINE;
}

/** The pause before try n (0-based). */
export function retryDelay(attempt: number, random: () => number = Math.random): number {
  const base = RETRY_MS[Math.min(Math.max(0, attempt), RETRY_MS.length - 1)] ?? 15000;
  return Math.round(base * (0.85 + 0.3 * random()));
}

/** What the server said when asked for one byte of a file a player plays. */
export type Probe =
  | { kind: 'ok' }
  | { kind: 'database' } // MetaRush answers, its database doesn't
  | { kind: 'offline' } // MetaRush itself isn't answering
  | { kind: 'gone'; message: string } // the clip is gone or this browser is signed out: trying again won't help
  | { kind: 'trouble'; message?: string }; // anything else: worth trying again

type Fetcher = (input: string, init?: RequestInit) => Promise<Response>;

/** Asks for the first byte of url, the way the player would, and says what that means. */
export async function probeMedia(url: string, signal?: AbortSignal, fetcher: Fetcher = (u, i) => fetch(u, i)): Promise<Probe> {
  let res: Response;
  try {
    res = await fetcher(url, { headers: { Range: 'bytes=0-0' }, credentials: 'same-origin', cache: 'no-store', redirect: 'manual', signal });
  } catch (err) {
    if (signal?.aborted || (err as { name?: string } | null)?.name === 'AbortError') throw err;
    return { kind: 'offline' };
  }
  // A redirect to the media store means MetaRush found the file (its records answered).
  if (res.ok || res.type === 'opaqueredirect' || res.status === 416) {
    void res.body?.cancel().catch(() => undefined);
    return { kind: 'ok' };
  }
  let body: { error?: { code?: string; message?: string } } | null = null;
  if ((res.headers.get('Content-Type') ?? '').includes('application/json')) {
    body = await res.json().catch(() => null);
  } else {
    void res.body?.cancel().catch(() => undefined);
  }
  const code = body?.error?.code ?? '';
  const message = body?.error?.message ?? '';
  if (code === 'E_DATABASE') return { kind: 'database' };
  if (!body && res.status >= 500) return { kind: 'offline' }; // a stand-in answered for MetaRush (the dev server's bad gateway)
  if (res.status === 404 || res.status === 401 || res.status === 403) return { kind: 'gone', message: message || "This can't be played right now." };
  return { kind: 'trouble', message: message || undefined };
}

const worst: Probe['kind'][] = ['ok', 'trouble', 'offline', 'database', 'gone'];

/** Asks for every url at once; the worst answer counts. */
export async function probeAll(urls: string[], signal?: AbortSignal, fetcher?: Fetcher): Promise<Probe> {
  if (!urls.length) return { kind: 'ok' };
  const all = await Promise.all(urls.map((u) => probeMedia(u, signal, fetcher)));
  return all.reduce((a, b) => (worst.indexOf(b.kind) > worst.indexOf(a.kind) ? b : a));
}

/** Notices a stall: no new position and no new bytes for `limit` while playing. */
export function stallWatch(now: () => number = Date.now, limit = STALL_MS) {
  let moved = now();
  let data = now();
  let at = Number.NaN;
  return {
    /** The playhead is at pos (seconds or frames). */
    position(pos: number) {
      if (pos !== at) {
        at = pos;
        moved = now();
      }
    },
    /** Bytes arrived. */
    data() {
      data = now();
    },
    /** Start counting again (after a try). */
    reset() {
      moved = data = now();
    },
    stalled(playing: boolean): boolean {
      return playing && now() - Math.max(moved, data) >= limit;
    },
  };
}

export interface RecoveryOptions {
  /** Asks the server about the files the player needs. */
  probe: (signal: AbortSignal) => Promise<Probe>;
  /** Plays again: reloads the media (or the player) where it was. */
  retry: () => void;
  /** Shows the line (null: playing again). */
  show: (t: Trouble | null) => void;
  /** The server didn't answer: the player may stop asking it meanwhile (the preview unmounts). */
  idle?: () => void;
  /** Said when the file plays nowhere, though the server sends it (it can't be decoded here). */
  failedLine?: string;
  wait?: (ms: number, signal: AbortSignal) => Promise<void>;
  random?: () => number;
}

const sleep = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const t = setTimeout(resolve, ms);
    signal.addEventListener('abort', () => {
      clearTimeout(t);
      reject(new DOMException('Aborted', 'AbortError'));
    }, { once: true });
  });

/**
 * One player's way back: begin() on a stall or a media error, healed() once it
 * plays again. While it recovers it asks the server, says what is wrong, waits
 * and tries again; a try (retry) ends in healed() or in another begin().
 */
export class Recovery {
  /** Recovering (from the first trouble until it plays again or gives up). */
  active = false;
  /** A try is under way: the next stall or error means it didn't work. */
  retrying = false;
  /** It gave up: the file is gone, or it can't be played here. */
  failed = false;
  private attempt = 0;
  private okButFailed = 0;
  // Each way back has its own number: a player that plays again by itself
  // while a try is pending ends it (the pending try doesn't run).
  private run = 0;
  private ctrl = new AbortController();
  private o: RecoveryOptions;

  constructor(o: RecoveryOptions) {
    this.o = o;
  }

  /** Something went wrong: a stall, or a media error (code: MediaError.code). */
  begin(why: 'stall' | 'error', code?: number) {
    if (this.failed || this.ctrl.signal.aborted) return;
    if (this.active && !this.retrying) return; // already asking or waiting
    if (this.retrying) {
      // The try didn't work. A file the server sends but that still won't
      // play (code 3: can't be decoded, 4: not a format this browser plays)
      // isn't a connection problem: after a second such try, say so.
      this.retrying = false;
      if (why === 'error' && (code === 3 || code === 4) && ++this.okButFailed >= 2) {
        this.giveUp(this.o.failedLine);
        return;
      }
      void this.later(this.run);
      return;
    }
    this.active = true;
    this.o.show({ kind: 'reconnecting' });
    void this.ask(++this.run);
  }

  /** It plays (or, paused, loaded) again, after a try or by itself. */
  healed() {
    if (!this.active || this.failed) return;
    this.active = this.retrying = false;
    this.attempt = this.okButFailed = 0;
    this.run++;
    this.o.show(null);
  }

  /** The player went away. */
  dispose() {
    this.ctrl.abort();
  }

  private giveUp(message?: string) {
    this.failed = true;
    this.retrying = false;
    this.o.show({ kind: 'failed', message });
  }

  private async later(run: number) {
    try {
      await (this.o.wait ?? sleep)(retryDelay(this.attempt++, this.o.random), this.ctrl.signal);
    } catch {
      return;
    }
    await this.ask(run);
  }

  private async ask(run: number) {
    if (run !== this.run) return;
    let p: Probe;
    try {
      p = await this.o.probe(this.ctrl.signal);
    } catch {
      return; // disposed
    }
    if (this.ctrl.signal.aborted || run !== this.run) return;
    if (p.kind === 'ok') {
      this.retrying = true;
      this.o.retry();
      return;
    }
    if (p.kind === 'gone') {
      this.giveUp(p.message);
      return;
    }
    this.o.show({ kind: p.kind === 'database' ? 'database' : 'reconnecting' });
    this.o.idle?.();
    await this.later(run);
  }
}

const here = () => (typeof location === 'undefined' ? 'http://localhost/' : location.href);

/** A media address as a same-origin path (no origin, no #t= fragment Remotion adds). */
export function mediaPath(src: string, base = here()): string {
  try {
    const u = new URL(src, base);
    return u.pathname + u.search;
  } catch {
    return src;
  }
}

/**
 * The files a preview needs around frame f (the clips and sounds within a
 * second before and two after; every file when none is there), the failing
 * one first, as same-origin paths.
 */
export function sourcesNear(plan: RenderPlan | undefined, f: number, failing?: string, base = here()): string[] {
  const out = new Set<string>();
  const place = (src: string) => mediaPath(src, base);
  if (failing) out.add(place(failing));
  if (plan) {
    const fps = plan.fps > 0 ? plan.fps : 30;
    const lo = f - fps;
    const hi = f + 2 * fps;
    const add = (id?: string) => {
      const s = id ? plan.sources?.[id] : undefined;
      if (s) out.add(place(s));
    };
    const near = (from: number, len: number) => from <= hi && from + len >= lo;
    for (const c of plan.clips ?? []) if (near(c.from, c.durationInFrames)) add(c.assetId);
    for (const a of plan.audio ?? []) {
      if (!near(a.from, a.durationInFrames)) continue;
      add(a.assetId);
      add(a.variant?.assetId);
    }
    if (out.size === (failing ? 1 : 0)) for (const s of Object.values(plan.sources ?? {})) out.add(place(s));
  }
  return [...out].slice(0, 6);
}
