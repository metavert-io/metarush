import { useSyncExternalStore } from 'react';
import type { QueryClient } from '@tanstack/react-query';
import { identify } from '../lib/fingerprint';
import { ApiError, call, request } from './client';
import { onServerEvent } from './events';
import type { DuplicateOf, IngestResult, PrecheckResult, UploadList, UploadRecord } from './types';

/*
 * Resumable browser uploads (internal/api/uploads.go): create → PATCH 8 MB chunks at
 * Upload-Offset → HEAD to learn the server's offset after a hiccup. When the last
 * chunk lands the server runs the same ingest pipeline as the CLI and reports its
 * stages (checking, fingerprinting, storing) with byte progress on the upload
 * record, which survives reloads.
 *
 * Before anything is sent, each file gets a cheap identity (first/last MiB + a
 * sampled hash) and the server is asked which files are already stored, which are
 * duplicated within the drop, and which can resume an interrupted upload.
 */

const CHUNK = 8 * 1024 * 1024;
const CONCURRENCY = 2;
const MAX_RETRIES = 6;
/** No byte progress for this long while sending: reconnect and resume from the server's offset. */
export const STALL_MS = 15_000;
/** Finished uploads reappear after a reload only when they finished this recently. */
const RESTORE_FINISHED_MS = 3 * 60_000;

export type UploadState =
  | 'preparing'
  | 'queued'
  | 'starting'
  | 'sending'
  | 'stalled'
  | 'retrying'
  | 'offline'
  | 'server'
  | 'done'
  | 'skipped'
  | 'aliased'
  | 'waiting'
  | 'interrupted'
  | 'failed'
  | 'canceled';

export interface UploadItem {
  key: string;
  file?: File;
  name: string;
  size: number;
  projectId?: string;
  targetLabel: string;
  state: UploadState;
  /** Bytes the server has confirmed. */
  sent: number;
  /** Bytes of the in-flight chunk already on the wire. */
  inflight: number;
  serverId?: string;
  /** Server-side stage after sending: checking, fingerprinting, storing, done. */
  stage?: string;
  stageDone?: number;
  stageTotal?: number;
  /** Bytes per second: measured here while sending, reported by the server after. */
  rate?: number;
  lastProgressAt: number;
  result?: IngestResult;
  duplicate?: DuplicateOf;
  /** The earlier file in the same drop this one is identical to. */
  sameAsKey?: string;
  error?: string;
  /** False when retrying cannot help (e.g. the file is not media). */
  retriable?: boolean;
  attempts: number;
  fingerprint?: string;
  sample?: string;
  /** This item was rebuilt from server state after a reload. */
  restored?: boolean;
  resumedFrom?: number;
  createdAt: number;
  finishedAt?: number;
}

export const ACTIVE: UploadState[] = ['preparing', 'queued', 'starting', 'sending', 'stalled', 'retrying', 'server', 'waiting'];
export const isActive = (it: UploadItem) => ACTIVE.includes(it.state);
export const isFinished = (it: UploadItem) => ['done', 'skipped', 'aliased', 'failed', 'canceled'].includes(it.state);

/** One duplicate the person must decide about. */
export interface DuplicateQuestion {
  key: string;
  name: string;
  size: number;
  /** A stored asset it is identical to, or… */
  existing?: DuplicateOf & { sameName: boolean; inTarget: boolean };
  /** …an earlier file in the same drop. */
  sameAs?: string;
  targetLabel: string;
}
export type DuplicateChoice = 'skip' | 'keep';
type Resolver = (qs: DuplicateQuestion[]) => Promise<Map<string, DuplicateChoice>>;

type FinishListener = (item: UploadItem) => void;

class UploadManager {
  private items: UploadItem[] = [];
  private listeners = new Set<() => void>();
  private finishListeners = new Set<FinishListener>();
  private xhrs = new Map<string, XMLHttpRequest>();
  private pollers = new Map<string, ReturnType<typeof setTimeout>>();
  private polling = new Set<string>();
  private watchdog: ReturnType<typeof setInterval> | null = null;
  private qc: QueryClient | null = null;
  private resolver: Resolver | null = null;
  private seq = 0;
  private hydrated = false;

  constructor() {
    onServerEvent((e) => {
      if (e.type !== 'upload' || !e.id) return;
      const it = this.items.find((x) => x.serverId === e.id);
      if (it && (it.state === 'server' || it.restored)) void this.pollOnce(it.key);
      if (!it && this.hydrated) void this.hydrate(); // started in another tab
    });
  }

  attach(qc: QueryClient) {
    this.qc = qc;
  }

  /** The duplicate dialog registers itself here (the manager is not a React component). */
  setDuplicateResolver(fn: Resolver | null) {
    this.resolver = fn;
  }

  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };
  getSnapshot = () => this.items;

  onFinished(fn: FinishListener): () => void {
    this.finishListeners.add(fn);
    return () => this.finishListeners.delete(fn);
  }

  private emit() {
    this.items = [...this.items];
    this.listeners.forEach((fn) => fn());
  }

  private patch(key: string, next: Partial<UploadItem>) {
    const i = this.items.findIndex((x) => x.key === key);
    if (i < 0) return;
    this.items[i] = { ...this.items[i]!, ...next };
    this.emit();
  }

  private get(key: string): UploadItem | undefined {
    return this.items.find((x) => x.key === key);
  }

  /* ---------- Rebuild from server state (reload-safe) ---------- */

  /** Loads the daemon's upload records: interrupted uploads, server stages, recent results. */
  async hydrate() {
    let list: UploadList;
    try {
      list = await request<UploadList>('GET', '/api/uploads');
    } catch {
      return;
    }
    this.hydrated = true;
    let changed = false;
    const recentlyDone = Date.now() - RESTORE_FINISHED_MS;
    for (const rec of list.uploads) {
      const existing = this.items.find((x) => x.serverId === rec.id);
      if (existing) continue;
      // Interrupted, in-progress and failed uploads always come back after a
      // reload; ones that simply finished come back only if they just did.
      if ((rec.state === 'done' || rec.state === 'canceled') && new Date(rec.updatedAt).getTime() < recentlyDone) continue;
      changed = true;
      this.items.push(this.fromRecord(rec));
    }
    if (changed) {
      this.items.sort((a, b) => b.createdAt - a.createdAt);
      this.emit();
      for (const it of this.items) if (it.state === 'server') this.schedulePoll(it.key, 400);
    }
  }

  private fromRecord(rec: UploadRecord): UploadItem {
    const base: UploadItem = {
      key: `srv-${rec.id}`,
      name: rec.name,
      size: rec.size,
      projectId: rec.projectId,
      targetLabel: rec.projectId ? 'Project' : 'Inbox',
      state: 'server',
      sent: rec.offset,
      inflight: 0,
      serverId: rec.id,
      stage: rec.stage,
      stageDone: rec.stageDone,
      stageTotal: rec.stageTotal,
      rate: rec.rate,
      lastProgressAt: new Date(rec.lastProgressAt).getTime() || Date.now(),
      attempts: 0,
      fingerprint: rec.fingerprint,
      restored: true,
      createdAt: new Date(rec.createdAt).getTime(),
      result: rec.result,
    };
    switch (rec.state) {
      case 'receiving':
        return { ...base, state: 'interrupted', rate: undefined };
      case 'ingesting':
        return base;
      case 'done':
        return rec.result?.status === 'duplicate'
          ? { ...base, state: 'skipped', duplicate: rec.result.duplicate, finishedAt: new Date(rec.updatedAt).getTime() }
          : { ...base, state: 'done', finishedAt: new Date(rec.updatedAt).getTime() };
      case 'failed':
        return { ...base, state: 'failed', error: cleanError(rec.error || rec.result?.error, rec.name), retriable: false, finishedAt: new Date(rec.updatedAt).getTime() };
      default:
        return { ...base, state: 'canceled', finishedAt: new Date(rec.updatedAt).getTime() };
    }
  }

  /* ---------- Adding files: identify, precheck, resolve duplicates ---------- */

  async add(files: { file: File; path?: string }[], target: { projectId?: string; label: string }) {
    const now = Date.now();
    const fresh: UploadItem[] = [];
    for (const { file, path } of files) {
      const it: UploadItem = {
        key: `u${++this.seq}-${now}`,
        file,
        name: path || file.name,
        size: file.size,
        projectId: target.projectId,
        targetLabel: target.label,
        state: 'preparing',
        sent: 0,
        inflight: 0,
        attempts: 0,
        lastProgressAt: now,
        createdAt: now,
      };
      this.items.unshift(it);
      fresh.push(it);
    }
    this.emit();
    // Identify every file (reads ~3 MiB each), a few at a time.
    const queue = [...fresh];
    await Promise.all(
      Array.from({ length: Math.min(4, queue.length) }, async () => {
        for (let it = queue.shift(); it; it = queue.shift()) {
          if (!it.file || it.size === 0) continue;
          try {
            const id = await identify(it.file);
            this.patch(it.key, id);
          } catch {
            // no fingerprint: the upload proceeds and the server's SHA-256 decides
          }
        }
      }),
    );
    const ready = fresh.map((x) => this.get(x.key)!).filter((x) => x && x.state === 'preparing');
    let results: PrecheckResult[] = [];
    const withId = ready.filter((x) => x.fingerprint && x.size > 0);
    if (withId.length > 0) {
      try {
        const out = await call('asset.precheck', {
          files: withId.map((x) => ({ name: baseName(x.name), size: x.size, fingerprint: x.fingerprint!, sample: x.sample })),
          ...(target.projectId ? { projectId: target.projectId } : {}),
        });
        results = out.results;
      } catch {
        results = []; // precheck is a convenience; uploads still dedupe by SHA-256
      }
    }
    const questions: DuplicateQuestion[] = [];
    results.forEach((r, i) => {
      const it = withId[i];
      if (!it) return;
      if (r.duplicate) {
        questions.push({ key: it.key, name: it.name, size: it.size, existing: r.duplicate, targetLabel: target.label });
      } else if (r.batchDuplicateOf !== undefined && withId[r.batchDuplicateOf]) {
        const orig = withId[r.batchDuplicateOf]!;
        this.patch(it.key, { sameAsKey: orig.key });
        questions.push({ key: it.key, name: it.name, size: it.size, sameAs: orig.name, targetLabel: target.label });
      } else if (r.resume) {
        // Same file as an interrupted upload: continue it instead of starting over.
        const prior = this.items.find((x) => x.serverId === r.resume!.uploadId && x.key !== it.key);
        if (prior) this.items = this.items.filter((x) => x.key !== prior.key);
        this.patch(it.key, { serverId: r.resume.uploadId, sent: r.resume.offset, resumedFrom: r.resume.offset });
      }
    });
    let choices = new Map<string, DuplicateChoice>();
    if (questions.length > 0) {
      choices = this.resolver ? await this.resolver(questions) : new Map(questions.map((q) => [q.key, 'skip' as DuplicateChoice]));
    }
    for (const q of questions) {
      const choice = choices.get(q.key) ?? 'skip';
      const it = this.get(q.key);
      if (!it) continue;
      if (q.existing) {
        if (choice === 'keep') void this.keepBoth(it.key, q.existing.assetId);
        else void this.skipDuplicate(it.key, q.existing);
      } else if (choice === 'keep') {
        this.patch(it.key, { state: 'waiting' }); // aliased once the identical file finishes
      } else {
        this.finish(it.key, { state: 'skipped' });
      }
    }
    for (const it of ready) {
      const cur = this.get(it.key);
      if (cur?.state === 'preparing') this.patch(it.key, { state: 'queued' });
    }
    this.pump();
  }

  private async keepBoth(key: string, assetId: string) {
    const it = this.get(key);
    if (!it) return;
    try {
      const out = await call('asset.duplicate', { assetId, name: baseName(it.name), action: 'keep', ...(it.projectId ? { projectId: it.projectId } : {}) });
      this.finish(key, { state: 'aliased', duplicate: out.duplicate });
      this.invalidate();
    } catch (err) {
      const e = err instanceof ApiError ? err : null;
      this.finish(key, { state: 'failed', error: e ? e.message : String(err), retriable: false });
    }
  }

  private async skipDuplicate(key: string, dup: DuplicateOf) {
    const it = this.get(key);
    if (!it) return;
    this.finish(key, { state: 'skipped', duplicate: dup });
    try {
      await call('asset.duplicate', { assetId: dup.assetId, name: baseName(it.name), action: 'skip', ...(it.projectId ? { projectId: it.projectId } : {}) });
    } catch {
      // the skip already happened locally; the notification is best effort
    }
  }

  /** Files waiting on an identical earlier file in the same drop. */
  private settleWaiters(original: UploadItem) {
    for (const w of this.items.filter((x) => x.state === 'waiting' && x.sameAsKey === original.key)) {
      const assetId = original.result?.assetId ?? original.duplicate?.assetId;
      if (assetId && (original.state === 'done' || original.state === 'aliased' || original.state === 'skipped')) void this.keepBoth(w.key, assetId);
      else this.patch(w.key, { state: 'queued', sameAsKey: undefined }); // the original did not make it: upload this one
    }
    this.pump();
  }

  /* ---------- Sending ---------- */

  private pump() {
    const active = this.items.filter((x) => x.state === 'starting' || x.state === 'sending' || x.state === 'retrying' || x.state === 'stalled').length;
    let slots = CONCURRENCY - active;
    for (const it of [...this.items].reverse()) {
      if (slots <= 0) break;
      if (it.state === 'queued') {
        slots -= 1;
        void this.run(it.key);
      }
    }
    this.ensureWatchdog();
  }

  /** Stall detection: no byte progress for STALL_MS → abort, ask the server for its offset, resume. */
  private ensureWatchdog() {
    const sending = this.items.some((x) => x.state === 'sending' || x.state === 'stalled');
    if (sending && !this.watchdog) {
      this.watchdog = setInterval(() => {
        const now = Date.now();
        for (const it of this.items) {
          if (it.state === 'sending' && now - it.lastProgressAt > STALL_MS) {
            this.patch(it.key, { state: 'stalled', rate: 0 });
            this.xhrs.get(it.key)?.abort(); // the send loop sees the abort and resumes from HEAD
          }
        }
        if (!this.items.some((x) => x.state === 'sending' || x.state === 'stalled') && this.watchdog) {
          clearInterval(this.watchdog);
          this.watchdog = null;
        }
      }, 1000);
    }
  }

  private async run(key: string) {
    const start = this.get(key);
    if (!start?.file) return;
    if (start.size === 0) {
      this.finish(key, { state: 'failed', error: 'This file is empty.', retriable: false });
      return;
    }
    this.patch(key, { state: start.serverId ? 'sending' : 'starting', error: undefined, lastProgressAt: Date.now() });
    try {
      if (!start.serverId) {
        const rec = await request<UploadRecord>('POST', '/api/uploads', {
          json: { name: start.file.name, size: start.size, ...(start.projectId ? { projectId: start.projectId } : {}), ...(start.fingerprint ? { fingerprint: start.fingerprint } : {}) },
        });
        this.patch(key, { serverId: rec.id, sent: rec.offset, state: 'sending', lastProgressAt: Date.now() });
      } else {
        const off = await this.headOffset(start.serverId);
        if (off === null) this.patch(key, { serverId: undefined, sent: 0, resumedFrom: undefined }); // record vanished: start over
        else this.patch(key, { sent: off });
        if (off === null) return void this.run(key);
      }
      await this.sendChunks(key);
    } catch (err) {
      const e = err instanceof ApiError ? err : null;
      const it = this.get(key);
      if (!it || it.state === 'canceled') return;
      if (e?.isUnreachable || e?.status === 0) {
        this.patch(key, { state: 'offline', inflight: 0, error: 'Cannot reach MetaRush on this machine. Press resume once it is running again.' });
        return;
      }
      this.finish(key, { state: 'failed', error: e ? [e.message, e.hint].filter(Boolean).join(' ') : String(err), retriable: !e || e.status >= 500 });
    } finally {
      this.pump();
    }
  }

  private async sendChunks(key: string) {
    let markAt = performance.now();
    let markBytes = this.get(key)?.sent ?? 0;
    for (;;) {
      const it = this.get(key);
      if (!it || !it.file || it.state === 'canceled' || it.state === 'offline') return;
      if (it.sent >= it.size) break;
      const end = Math.min(it.sent + CHUNK, it.size);
      try {
        const rec = await this.patchChunk(key, it.serverId!, it.sent, it.file.slice(it.sent, end), (loaded) => {
          const now = performance.now();
          const cur = this.get(key);
          if (!cur) return;
          const live = cur.sent + loaded;
          let rate = cur.rate;
          if (now - markAt >= 500) {
            const inst = ((live - markBytes) / (now - markAt)) * 1000;
            rate = rate ? rate * 0.6 + inst * 0.4 : inst;
            markAt = now;
            markBytes = live;
          }
          this.patch(key, { inflight: loaded, rate, lastProgressAt: Date.now(), state: cur.state === 'stalled' ? 'sending' : cur.state });
        });
        markBytes = Math.min(markBytes, rec.offset);
        this.patch(key, { sent: rec.offset, inflight: 0, attempts: 0, state: 'sending', lastProgressAt: Date.now() });
        if (rec.state === 'ingesting' || rec.state === 'done' || rec.state === 'failed') break;
      } catch (err) {
        const cur = this.get(key);
        if (!cur || cur.state === 'canceled') return;
        if (err instanceof ApiError && err.status === 409) {
          const off = (err.details as { offset?: number } | undefined)?.offset;
          if (typeof off === 'number') {
            this.patch(key, { sent: off, inflight: 0 });
            markBytes = off;
            continue;
          }
          throw err;
        }
        const stalled = cur.state === 'stalled';
        const retriable = stalled || !(err instanceof ApiError) || err.status === 0 || err.status >= 500 || err.isUnreachable;
        if (!retriable) throw err;
        const attempts = cur.attempts + 1;
        if (attempts > MAX_RETRIES) {
          this.patch(key, { state: 'offline', inflight: 0, attempts, error: 'Connection lost. Press resume once MetaRush is running again.' });
          return;
        }
        this.patch(key, { state: stalled ? 'stalled' : 'retrying', inflight: 0, attempts, error: undefined });
        if (!stalled) await sleep(Math.min(1000 * 2 ** (attempts - 1), 15_000));
        const off = await this.headOffset(cur.serverId!);
        if (off !== null) {
          this.patch(key, { sent: off, lastProgressAt: Date.now() });
          markBytes = off;
          markAt = performance.now();
        }
      }
    }
    const done = this.get(key);
    if (!done || done.state === 'canceled') return;
    this.patch(key, { state: 'server', inflight: 0, sent: done.size, stage: 'checking', stageDone: 0, stageTotal: done.size, rate: undefined, lastProgressAt: Date.now() });
    this.schedulePoll(key, 500);
  }

  private patchChunk(key: string, id: string, offset: number, blob: Blob, onProgress: (loaded: number) => void): Promise<UploadRecord> {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      this.xhrs.set(key, xhr);
      xhr.open('PATCH', `/api/uploads/${encodeURIComponent(id)}`);
      xhr.withCredentials = true;
      xhr.setRequestHeader('Upload-Offset', String(offset));
      xhr.setRequestHeader('X-MetaRush-Client', 'dashboard');
      xhr.setRequestHeader('Content-Type', 'application/offset+octet-stream');
      xhr.setRequestHeader('Accept', 'application/json');
      xhr.upload.onprogress = (ev) => {
        if (ev.lengthComputable) onProgress(Math.min(ev.loaded, blob.size));
      };
      xhr.onerror = () => {
        this.xhrs.delete(key);
        reject(new ApiError({ code: 'E_DAEMON_UNREACHABLE', message: 'Upload interrupted' }, 0));
      };
      xhr.onabort = () => {
        this.xhrs.delete(key);
        reject(new ApiError({ code: 'E_CANCELED', message: 'Upload paused' }, 499));
      };
      xhr.onload = () => {
        this.xhrs.delete(key);
        let env: { ok?: boolean; data?: UploadRecord; error?: { code: string; message: string; hint?: string; details?: unknown } } | null = null;
        try {
          env = JSON.parse(xhr.responseText);
        } catch {
          env = null;
        }
        if (env?.ok && env.data) resolve(env.data);
        else if (env?.error) reject(new ApiError(env.error, xhr.status));
        else reject(new ApiError({ code: 'E_DAEMON_UNREACHABLE', message: `Upload chunk failed (HTTP ${xhr.status})` }, xhr.status >= 500 ? xhr.status : 0));
      };
      xhr.send(blob);
    });
  }

  private async headOffset(id: string): Promise<number | null> {
    try {
      const res = await fetch(`/api/uploads/${encodeURIComponent(id)}`, { method: 'HEAD', credentials: 'same-origin' });
      const v = res.headers.get('Upload-Offset');
      return v !== null && res.ok ? Number(v) : null;
    } catch {
      return null;
    }
  }

  /* ---------- Server stages ---------- */

  private schedulePoll(key: string, ms: number) {
    const t = this.pollers.get(key);
    if (t) clearTimeout(t);
    this.pollers.set(
      key,
      setTimeout(() => void this.pollOnce(key), ms),
    );
  }

  private async pollOnce(key: string) {
    const it = this.get(key);
    if (!it?.serverId || this.polling.has(key)) return;
    if (it.state !== 'server' && it.state !== 'interrupted') return;
    this.polling.add(key);
    try {
      const rec = await request<UploadRecord>('GET', `/api/uploads/${encodeURIComponent(it.serverId)}`);
      if (rec.state === 'done') {
        const dup = rec.result?.status === 'duplicate';
        this.finish(key, { state: dup ? 'skipped' : 'done', stage: 'done', result: rec.result, duplicate: rec.result?.duplicate });
        return;
      }
      if (rec.state === 'failed' || rec.state === 'canceled') {
        this.finish(key, { state: rec.state === 'failed' ? 'failed' : 'canceled', result: rec.result, error: cleanError(rec.error || rec.result?.error, it.name), retriable: false });
        return;
      }
      if (rec.state === 'receiving' && it.state === 'interrupted') {
        this.patch(key, { sent: rec.offset });
        return;
      }
      this.patch(key, { stage: rec.stage, stageDone: rec.stageDone, stageTotal: rec.stageTotal, rate: rec.rate, lastProgressAt: new Date(rec.lastProgressAt).getTime() });
    } catch {
      // keep polling; the daemon may be momentarily busy
    } finally {
      this.polling.delete(key);
    }
    if (this.get(key)?.state === 'server') this.schedulePoll(key, 1500);
  }

  private invalidate() {
    if (!this.qc) return;
    for (const k of ['asset.list', 'project.show', 'project.list', 'job.list', 'status', 'activity.summary']) void this.qc.invalidateQueries({ queryKey: [k] });
  }

  private finish(key: string, next: Partial<UploadItem>) {
    if (this.get(key)?.finishedAt) return; // already reported (SSE and polling can race)
    const t = this.pollers.get(key);
    if (t) clearTimeout(t);
    this.pollers.delete(key);
    this.patch(key, { ...next, inflight: 0, finishedAt: Date.now() });
    const it = this.get(key);
    if (!it) return;
    if (it.state === 'done' || it.state === 'aliased') this.invalidate();
    this.finishListeners.forEach((fn) => fn(it));
    this.settleWaiters(it);
  }

  /* ---------- Controls ---------- */

  async cancel(key: string) {
    const it = this.get(key);
    if (!it) return;
    const wasActive = !isFinished(it);
    this.patch(key, { state: 'canceled', inflight: 0, finishedAt: Date.now() });
    this.xhrs.get(key)?.abort();
    const t = this.pollers.get(key);
    if (t) clearTimeout(t);
    if (wasActive && it.serverId && it.state !== 'server') {
      try {
        await request('DELETE', `/api/uploads/${encodeURIComponent(it.serverId)}`);
      } catch {
        // already gone
      }
    }
    this.settleWaiters({ ...it, state: 'canceled' });
    this.pump();
  }

  /** Resume a stalled, offline or failed upload from the server's current offset. */
  async resume(key: string) {
    const it = this.get(key);
    if (!it?.file) return;
    let sent = it.sent;
    if (it.serverId) {
      const off = await this.headOffset(it.serverId);
      if (off === null) {
        this.patch(key, { serverId: undefined, sent: 0 });
        sent = 0;
      } else sent = off;
    }
    this.patch(key, { state: 'queued', attempts: 0, error: undefined, sent, finishedAt: undefined });
    this.pump();
  }

  /** Remove finished uploads from every dashboard's tray (server records are dismissed). */
  clearFinished() {
    const gone = this.items.filter(isFinished);
    this.items = this.items.filter((x) => !isFinished(x));
    this.emit();
    for (const it of gone) if (it.serverId) void request('DELETE', `/api/uploads/${encodeURIComponent(it.serverId)}`).catch(() => undefined);
  }

  dismiss(key: string) {
    const it = this.get(key);
    this.items = this.items.filter((x) => x.key !== key);
    this.emit();
    if (it?.serverId && (isFinished(it) || it.state === 'interrupted')) void request('DELETE', `/api/uploads/${encodeURIComponent(it.serverId)}`).catch(() => undefined);
  }
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function baseName(p: string): string {
  const i = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'));
  return i >= 0 ? p.slice(i + 1) : p;
}

/** The server reports ingest errors against its temporary upload file; show the user's filename instead. */
function cleanError(msg: string | undefined, name: string): string | undefined {
  if (!msg) return msg;
  return msg.replace(/\/\S*?\/upl_[A-Za-z0-9]+\.part/g, name);
}

export const uploads = new UploadManager();

export function useUploads(): UploadItem[] {
  return useSyncExternalStore(uploads.subscribe, uploads.getSnapshot);
}

/** Overall fraction for one item across its upload stages (sending dominates; server stages after). */
export function uploadFraction(it: UploadItem): number {
  if (isFinished(it)) return 1;
  const sending = it.size ? (it.sent + it.inflight) / it.size : 0;
  if (it.state !== 'server') return 0.85 * Math.min(1, sending);
  const sp = it.stageTotal ? Math.min(1, (it.stageDone ?? 0) / it.stageTotal) : 0;
  switch (it.stage) {
    case 'checking':
      return 0.85 + 0.02 * sp;
    case 'fingerprinting':
      return 0.87 + 0.05 * sp;
    case 'storing':
      return 0.92 + 0.08 * sp;
    default:
      return 1;
  }
}

/* ---------- Drag-and-drop helpers (folders via webkitGetAsEntry) ---------- */

/**
 * Files no one means to add (D247): hidden ones (.DS_Store, ._clip.mov), what
 * Windows keeps in folders (Thumbs.db, desktop.ini, any case), Office's
 * lock files (~$notes.docx) and anything inside a drive's Recycle Bin or
 * System Volume Information. Takes a name or a path with / or \.
 */
export const isSystemFile = (p: string): boolean => {
  const parts = p.split(/[\\/]/).filter(Boolean);
  const name = (parts[parts.length - 1] ?? '').toLowerCase();
  if (!name || name.startsWith('.') || name.startsWith('~$') || ['thumbs.db', 'ehthumbs.db', 'desktop.ini'].includes(name)) return true;
  return parts.some((s) => ['$recycle.bin', 'system volume information'].includes(s.toLowerCase()));
};

const isHidden = isSystemFile;

function readAllEntries(dir: FileSystemDirectoryEntry): Promise<FileSystemEntry[]> {
  const reader = dir.createReader();
  const out: FileSystemEntry[] = [];
  return new Promise((resolve) => {
    const next = () =>
      reader.readEntries(
        (batch) => {
          if (batch.length === 0) resolve(out);
          else {
            out.push(...batch);
            next();
          }
        },
        () => resolve(out),
      );
    next();
  });
}

async function walk(entry: FileSystemEntry, prefix: string, out: { file: File; path: string }[]) {
  if (isHidden(entry.name)) return;
  if (entry.isFile) {
    const file = await new Promise<File | null>((res) => (entry as FileSystemFileEntry).file(res, () => res(null)));
    if (file) out.push({ file, path: prefix + file.name });
  } else if (entry.isDirectory) {
    const children = await readAllEntries(entry as FileSystemDirectoryEntry);
    for (const c of children) await walk(c, `${prefix}${entry.name}/`, out);
  }
}

/** Files from a drop, expanding folders where the browser supports it. */
export async function filesFromDataTransfer(dt: DataTransfer): Promise<{ file: File; path: string }[]> {
  const out: { file: File; path: string }[] = [];
  const items = Array.from(dt.items ?? []);
  const entries = items
    .filter((i) => i.kind === 'file')
    .map((i) => (typeof i.webkitGetAsEntry === 'function' ? i.webkitGetAsEntry() : null));
  if (entries.length > 0 && entries.every((e) => e !== null)) {
    for (const e of entries) await walk(e!, '', out);
    return out;
  }
  for (const f of Array.from(dt.files ?? [])) if (!isHidden(f.name)) out.push({ file: f, path: f.name });
  return out;
}

/** OS file drags (not MetaRush's own item drags). */
export function hasFiles(dt: DataTransfer | null): boolean {
  if (!dt) return false;
  const types = Array.from(dt.types ?? []);
  return types.includes('Files') && !types.includes(ASSET_DRAG_TYPE);
}

/** MIME type of an internal drag of library items onto projects. */
export const ASSET_DRAG_TYPE = 'application/x-metarush-assets';
