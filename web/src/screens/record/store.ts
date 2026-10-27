import type { RecordingClockIn } from '../../api/types';
import type { PieceStore } from './pieces';

/*
 * What the screen recorder keeps in this browser (D258): each piece until the
 * computer that runs MetaRush has it, and a note of each recording that
 * hasn't become footage yet, so a reload or a closed tab loses nothing that
 * was recorded: the next time the recorder opens it sends the rest. Notes and
 * pieces carry the person who recorded them: the recorder offers only that
 * person's, and signing out deletes them (after a warning when some weren't
 * sent). Browser storage can be missing or full (a private window): then
 * pieces are only sent from memory, and the recorder says so.
 */

const DB = 'metarush-recorder';
const PIECES = 'pieces';
const NOTES = 'recordings';

/** A recording this browser started that isn't footage yet. */
export interface Pending {
  id: string;
  /** The person who recorded it (the session's). */
  userId: string;
  name: string;
  projectId?: string;
  /** The tracks and how much of each was recorded. */
  tracks: { id: string; bytes: number }[];
  /** The last clock measured. */
  clock?: RecordingClockIn;
  /** It was stopped (only sending and finishing were left). */
  stopped: boolean;
  /** When it started (Date.now()). */
  startedAt: number;
}

let opening: Promise<IDBDatabase | null> | null = null;
let opened: IDBDatabase | null = null;

function db(): Promise<IDBDatabase | null> {
  if (opening) return opening;
  opening = new Promise((resolve) => {
    try {
      if (typeof indexedDB === 'undefined') return resolve(null);
      const req = indexedDB.open(DB, 1);
      req.onupgradeneeded = () => {
        const d = req.result;
        if (!d.objectStoreNames.contains(PIECES)) d.createObjectStore(PIECES, { keyPath: ['rec', 'track', 'offset'] });
        if (!d.objectStoreNames.contains(NOTES)) d.createObjectStore(NOTES, { keyPath: 'id' });
      };
      req.onsuccess = () => {
        opened = req.result;
        resolve(req.result);
      };
      req.onerror = () => resolve(null);
      req.onblocked = () => resolve(null);
    } catch {
      resolve(null);
    }
  });
  return opening;
}

function done(tx: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error ?? new Error('browser storage failed'));
    tx.onabort = () => reject(tx.error ?? new Error('browser storage stopped'));
  });
}

function all<T>(req: IDBRequest<T[]>): Promise<T[]> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

/** Browser storage works here. */
export async function available(): Promise<boolean> {
  return (await db()) !== null;
}

/** Where a track's pieces wait in this browser (the person's who recorded them). */
export function pieceStore(rec: string, track: string, user: string): PieceStore {
  return {
    async put(offset, blob) {
      const d = await db();
      if (!d) return;
      const tx = d.transaction(PIECES, 'readwrite');
      tx.objectStore(PIECES).put({ rec, track, offset, blob, user });
      await done(tx);
    },
    async drop(upTo) {
      const d = await db();
      if (!d) return;
      const tx = d.transaction(PIECES, 'readwrite');
      const st = tx.objectStore(PIECES);
      const range = IDBKeyRange.bound([rec, track, 0], [rec, track, Number.MAX_SAFE_INTEGER]);
      const req = st.openCursor(range);
      req.onsuccess = () => {
        const c = req.result;
        if (!c) return;
        const v = c.value as { offset: number; blob: Blob };
        if (v.offset + v.blob.size <= upTo) c.delete();
        c.continue();
      };
      await done(tx);
    },
  };
}

/** A track's pieces kept in this browser, in order. */
export async function piecesOf(rec: string, track: string): Promise<{ offset: number; blob: Blob }[]> {
  const d = await db();
  if (!d) return [];
  const tx = d.transaction(PIECES, 'readonly');
  const out = await all(tx.objectStore(PIECES).getAll(IDBKeyRange.bound([rec, track, 0], [rec, track, Number.MAX_SAFE_INTEGER])) as IDBRequest<{ offset: number; blob: Blob }[]>);
  return out.map((p) => ({ offset: p.offset, blob: p.blob })).sort((a, b) => a.offset - b.offset);
}

/** Notes a recording (and its latest clock) as not yet footage. */
export async function remember(p: Pending): Promise<void> {
  const d = await db();
  if (!d) return;
  try {
    const tx = d.transaction(NOTES, 'readwrite');
    tx.objectStore(NOTES).put(p);
    await done(tx);
  } catch {
    /* storage full: the host still has what arrived */
  }
}

/** The recordings this person started in this browser that aren't footage yet. */
export async function pending(userId: string): Promise<Pending[]> {
  const d = await db();
  if (!d) return [];
  try {
    const tx = d.transaction(NOTES, 'readonly');
    return (await all(tx.objectStore(NOTES).getAll() as IDBRequest<Pending[]>)).filter((p) => p.userId === userId).sort((a, b) => a.startedAt - b.startedAt);
  } catch {
    return [];
  }
}

/** How much of recordings (anyone's) this browser holds that MetaRush doesn't have yet (bytes). */
export async function unsentBytes(): Promise<number> {
  const d = await db();
  if (!d) return 0;
  try {
    const tx = d.transaction(PIECES, 'readonly');
    const list = await all(tx.objectStore(PIECES).getAll() as IDBRequest<{ blob: Blob }[]>);
    return list.reduce((n, p) => n + (p.blob?.size ?? 0), 0);
  } catch {
    return 0;
  }
}

/** Deletes everything the recorder keeps in this browser (signing out). */
export async function clearAll(): Promise<void> {
  if (typeof indexedDB === 'undefined') return;
  opened?.close();
  opened = null;
  opening = null;
  await new Promise<void>((resolve) => {
    try {
      const req = indexedDB.deleteDatabase(DB);
      req.onsuccess = req.onerror = req.onblocked = () => resolve();
    } catch {
      resolve();
    }
  });
}

/** Forgets a recording and its pieces (it is footage, or was thrown away). */
export async function forget(rec: string): Promise<void> {
  const d = await db();
  if (!d) return;
  try {
    const tx = d.transaction([PIECES, NOTES], 'readwrite');
    tx.objectStore(NOTES).delete(rec);
    tx.objectStore(PIECES).delete(IDBKeyRange.bound([rec, '', 0], [rec, '￿', Number.MAX_SAFE_INTEGER]));
    await done(tx);
  } catch {
    /* nothing to forget */
  }
}
