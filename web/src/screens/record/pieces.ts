/*
 * Sending a track's pieces while recording (D258). Each piece the browser
 * records is kept (in browser storage, so a reload or a closed tab loses
 * nothing that was recorded) and sent at once, in order, at the byte offset
 * the computer that runs MetaRush has. When it answers with another offset
 * (a piece cut off on its way, a retry after a reload) the queue carries on
 * from there; a dropped connection is retried with a growing wait. A piece
 * is forgotten once the host has all of it.
 *
 * No browser APIs beyond Blob: the sender and the store are given, so
 * web/scripts/recorder-test.mjs drives it with fakes.
 */

/** The host answered with another offset than the one sent. */
export class OffsetMismatch extends Error {
  readonly offset: number;
  constructor(offset: number) {
    super(`the host has ${offset} bytes`);
    this.offset = offset;
  }
}

/** The host refused the piece for good (the recording ended, it isn't yours). */
export class Refused extends Error {}

export interface Sender {
  /** How much of the track the host has. */
  head(): Promise<number>;
  /** Sends bytes at offset; resolves with the host's new offset. Throws OffsetMismatch, Refused or anything else (retried). */
  send(offset: number, piece: Blob): Promise<number>;
}

/** Where pieces wait until the host has them. */
export interface PieceStore {
  put(offset: number, piece: Blob): Promise<void>;
  /** Forget pieces wholly below `upTo`. */
  drop(upTo: number): Promise<void>;
}

export interface QueueState {
  /** Bytes recorded so far. */
  bytes: number;
  /** Bytes the host has. */
  sent: number;
  /** Retrying after a problem (its message), or null. */
  problem: string | null;
  /** The host refused the track for good. */
  refused: boolean;
}

interface Piece {
  offset: number;
  blob: Blob;
}

/** Waits between tries after a problem (ms), growing to the last. */
export const BACKOFF = [1000, 2000, 4000, 8000, 15000];

export class TrackQueue {
  private pieces: Piece[] = [];
  private st: QueueState = { bytes: 0, sent: 0, problem: null, refused: false };
  private running = false;
  private tries = 0;
  private waiters: (() => void)[] = [];
  private listeners = new Set<(s: QueueState) => void>();
  private wake: (() => void) | null = null;
  private stopped = false;
  private readonly sender: Sender;
  private readonly store?: PieceStore;
  private readonly sleep: (ms: number) => Promise<void>;

  constructor(sender: Sender, store?: PieceStore, sleep: (ms: number) => Promise<void> = (ms) => new Promise((r) => setTimeout(r, ms))) {
    this.sender = sender;
    this.store = store;
    this.sleep = sleep;
  }

  /** Starts from pieces kept in browser storage (after a reload): what the host already has is forgotten. */
  restore(pieces: Piece[]) {
    this.pieces = [...pieces].sort((a, b) => a.offset - b.offset);
    const last = this.pieces[this.pieces.length - 1];
    this.st.bytes = last ? last.offset + last.blob.size : 0;
    this.st.sent = this.pieces[0]?.offset ?? 0;
    this.pump();
  }

  get state(): QueueState {
    return { ...this.st };
  }

  subscribe(fn: (s: QueueState) => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  /** Adds the next recorded piece. */
  async add(blob: Blob): Promise<void> {
    if (!blob.size) return;
    const p = { offset: this.st.bytes, blob };
    this.st.bytes += blob.size;
    this.pieces.push(p);
    this.emit();
    if (this.store) {
      try {
        await this.store.put(p.offset, blob);
      } catch {
        /* browser storage full or off: the piece is still sent from memory */
      }
    }
    this.pump();
  }

  /** Resolves once the host has every piece recorded so far (rejects when it refused the track). */
  drained(): Promise<void> {
    if (this.st.refused) return Promise.reject(new Refused(this.st.problem ?? 'The recording was refused.'));
    if (this.st.sent >= this.st.bytes) return Promise.resolve();
    return new Promise((resolve, reject) => {
      this.waiters.push(() => (this.st.refused ? reject(new Refused(this.st.problem ?? '')) : resolve()));
    });
  }

  /** Tries again now (a person pressed Try again, or the connection came back). */
  retryNow() {
    this.tries = 0;
    this.wake?.();
  }

  /** Stops sending (the page is closing; pieces stay in browser storage). */
  stop() {
    this.stopped = true;
    this.wake?.();
  }

  private emit() {
    const s = this.state;
    for (const fn of this.listeners) fn(s);
    if (this.st.sent >= this.st.bytes || this.st.refused) {
      const w = this.waiters;
      this.waiters = [];
      w.forEach((f) => f());
    }
  }

  private pump() {
    if (this.running || this.stopped) return;
    this.running = true;
    void this.loop().finally(() => {
      this.running = false;
      if (!this.stopped && !this.st.refused && this.st.sent < this.st.bytes) this.pump();
    });
  }

  private async loop() {
    while (!this.stopped && !this.st.refused && this.st.sent < this.st.bytes) {
      const at = this.st.sent;
      const p = this.pieces.find((x) => x.offset + x.blob.size > at);
      if (!p || p.offset > at) {
        // The host is behind a piece that was forgotten: it can't be sent again.
        this.st.problem = 'Part of the recording was lost before it could be sent.';
        this.st.refused = true;
        this.emit();
        return;
      }
      try {
        const n = await this.sender.send(at, at > p.offset ? p.blob.slice(at - p.offset) : p.blob);
        this.accept(n);
        this.tries = 0;
        if (this.st.problem) {
          this.st.problem = null;
          this.emit();
        }
      } catch (e) {
        if (e instanceof OffsetMismatch) {
          this.accept(e.offset);
          continue;
        }
        if (e instanceof Refused) {
          this.st.problem = e.message;
          this.st.refused = true;
          this.emit();
          return;
        }
        this.st.problem = e instanceof Error && e.message ? e.message : 'The connection to MetaRush dropped.';
        this.emit();
        const wait = BACKOFF[Math.min(this.tries, BACKOFF.length - 1)]!;
        this.tries++;
        await Promise.race([this.sleep(wait), new Promise<void>((r) => (this.wake = r))]);
        this.wake = null;
      }
    }
  }

  /** The host has n bytes: forget the pieces it has wholly. */
  private accept(n: number) {
    this.st.sent = Math.min(Math.max(0, n), this.st.bytes);
    const keep = this.pieces.filter((x) => x.offset + x.blob.size > this.st.sent);
    if (keep.length !== this.pieces.length) {
      this.pieces = keep;
      void this.store?.drop(this.st.sent).catch(() => undefined);
    }
    this.emit();
  }
}
