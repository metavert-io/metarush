import { ApiError, call, request, toApiError } from '../../api/client';
import type { RecordingClockIn, RecordingCreateIn, RecordingInfo, RecordingPieceOut, RecordingRecord, RecordingScript } from '../../api/types';
import type { Mic } from '../studio/voiceover/capture';
import { fitMic, pausesOnClock, recordedAt, toPCM16, voiceStart, VoiceGate, type Arrival, type MicFit, type Pause } from './clock';
import { formatOf, pacing, pickFormat, PIECE_MS, videoBits } from './formats';
import { OffsetMismatch, Refused, TrackQueue, type QueueState, type Sender } from './pieces';
import { forget, pieceStore, piecesOf, remember, type Pending } from './store';
import { surfaceOf } from './sources';

/*
 * One screen recording while it records (D258, D259): the screen and the
 * camera each go to the browser's recorder in two-second pieces (with the
 * microphone's sound, or the screen's own), the microphone's samples to a
 * 16-bit voice track, every piece to its queue (kept in browser storage, sent
 * at once). The clock: the recorders start together (time 0); the voice's
 * start and drift come from when its blocks arrived; pauses stop every file.
 * The clock goes to the host every few seconds and with the finish, so a
 * closed tab keeps it.
 */

export type TrackID = 'screen' | 'camera' | 'voice';

export interface Sources {
  screen?: MediaStream;
  camera?: MediaStream;
  mic?: Mic;
}

export interface StartOptions {
  /** The person recording (the session's): browser storage is theirs. */
  userId: string;
  name: string;
  projectId?: string;
  layout?: RecordingCreateIn['layout'];
  script?: RecordingScript;
  sources: Sources;
  /** A source went away (a shared window closed, a camera unplugged). */
  onSourceEnded: (track: TrackID) => void;
  /** A recorder failed. */
  onProblem: (message: string) => void;
  /** The host refused a track for good (the recording reached its limit, or ended elsewhere). */
  onRefused: (message: string) => void;
}

/** The pieces one recording track has sent. */
export interface TrackProgress extends QueueState {
  id: TrackID;
}

const CLOCK_EVERY_MS = 5000;

/** Chrome, Edge and the other browsers built on Chromium (whose MP4 recorder formats.ts pacing works around). */
function isChromium(): boolean {
  const brands = (navigator as Navigator & { userAgentData?: { brands?: { brand: string }[] } }).userAgentData?.brands ?? [];
  return brands.some((b) => b.brand === 'Chromium');
}

/** Sends a track's pieces to the host. */
export function senderFor(rec: string, track: string): Sender {
  const path = `/api/recordings/${rec}/tracks/${track}`;
  return {
    async head() {
      const res = await fetch(path, { method: 'HEAD', credentials: 'same-origin' });
      if (!res.ok) throw new Refused('MetaRush no longer has this recording.');
      return Number(res.headers.get('Upload-Offset') ?? 0);
    },
    async send(offset, piece) {
      try {
        const out = await request<RecordingPieceOut>('PATCH', path, { headers: { 'Upload-Offset': String(offset), 'Content-Type': 'application/octet-stream' }, body: piece });
        return out.offset;
      } catch (e) {
        const err = toApiError(e);
        const d = (err.details ?? {}) as { offset?: number; state?: string };
        if (err.code === 'E_CONFLICT' && typeof d.offset === 'number') throw new OffsetMismatch(d.offset);
        if (err.code === 'E_CONFLICT' || err.code === 'E_NOT_FOUND' || (err.code === 'E_UNAUTHORIZED' && !err.isSignedOut)) throw new Refused(err.message);
        if (err.isUnreachable) throw new Error('The connection to MetaRush dropped; sending again as soon as it is back.');
        if (err.isSignedOut) throw new Error('You were signed out; sign in again and the recording carries on sending.');
        throw new Error(err.message);
      }
    },
  };
}

/** The microphone's arrivals, kept at their earliest per quarter second of sound (a long recording stays small). */
class Arrivals {
  readonly list: Arrival[] = [];
  private rate: number;
  private bucket = -1;
  constructor(rate: number) {
    this.rate = rate;
  }
  add(t: number, n: number) {
    const b = Math.floor((n / this.rate) * 4);
    const est = t - (n / this.rate) * 1000;
    if (b !== this.bucket) {
      this.bucket = b;
      this.list.push([t, n]);
      return;
    }
    const last = this.list[this.list.length - 1]!;
    if (est < last[0] - (last[1] / this.rate) * 1000) this.list[this.list.length - 1] = [t, n];
  }
}

export class RecordingSession {
  readonly id: string;
  readonly record: RecordingRecord;
  readonly queues = new Map<TrackID, TrackQueue>();
  private recorders = new Map<TrackID, MediaRecorder>();
  private t0 = 0;
  private pauses: Pause[] = [];
  private mic?: Mic;
  private arrivals?: Arrivals;
  private gate?: VoiceGate;
  private fit?: MicFit;
  private voiceSamples = 0;
  private timers: number[] = [];
  private stopping: Promise<void> | null = null;
  private ended: 'stopped' | 'track' = 'stopped';
  private lastClock = 0;
  private startedAt = Date.now();
  private userId: string;

  private constructor(rec: RecordingRecord, userId: string) {
    this.id = rec.id;
    this.record = rec;
    this.userId = userId;
  }

  /** Starts the recording on the host and every recorder at once (time 0). */
  static async start(opts: StartOptions): Promise<RecordingSession> {
    const { screen, camera, mic } = opts.sources;
    if (!screen && !camera && !mic) throw new Error('Choose something to record: the screen, the camera or the microphone.');
    const supported = (t: string) => typeof MediaRecorder !== 'undefined' && MediaRecorder.isTypeSupported(t);
    const micTrack = mic?.stream.getAudioTracks()[0];
    const tracks: RecordingCreateIn['tracks'] = [];
    const plan: { id: TrackID; stream: MediaStream; format: string; bits: number }[] = [];
    if (screen) {
      const v = screen.getVideoTracks()[0]!;
      const own = screen.getAudioTracks()[0];
      const s = v.getSettings();
      const sound = own ? 'own' : micTrack ? 'mic' : 'none';
      // Copies: stopping the recording ends them, never the shared screen's own sound or the microphone.
      const audio = own?.clone() ?? micTrack?.clone();
      const stream = new MediaStream(audio ? [v, audio] : [v]);
      const format = pickFormat(supported, !!audio);
      plan.push({ id: 'screen', stream, format, bits: videoBits(s.width ?? 0, s.height ?? 0, s.frameRate ?? 30) });
      tracks.push({ id: 'screen', label: v.label.slice(0, 200), surface: surfaceOf(screen), format: formatOf(format || 'video/webm'), sound, width: s.width, height: s.height });
    }
    if (camera) {
      const v = camera.getVideoTracks()[0]!;
      const s = v.getSettings();
      const audio = micTrack?.clone();
      const stream = new MediaStream(audio ? [v, audio] : [v]);
      const format = pickFormat(supported, !!audio);
      plan.push({ id: 'camera', stream, format, bits: Math.min(4e6, videoBits(s.width ?? 0, s.height ?? 0, s.frameRate ?? 30)) });
      tracks.push({ id: 'camera', label: v.label.slice(0, 200), format: formatOf(format || 'video/webm'), sound: audio ? 'mic' : 'none', width: s.width, height: s.height });
    }
    if (mic) tracks.push({ id: 'voice', label: mic.label.slice(0, 200), format: 'audio/pcm', rate: mic.sampleRate });
    const rec = await request<RecordingRecord>('POST', '/api/recordings', {
      json: { projectId: opts.projectId, name: opts.name, layout: opts.layout, tracks, script: opts.script } satisfies RecordingCreateIn,
    });
    const s = new RecordingSession(rec, opts.userId);
    await remember(s.note(false));
    for (const t of tracks) {
      const q = new TrackQueue(senderFor(rec.id, t.id), pieceStore(rec.id, t.id, opts.userId));
      let told = false;
      q.subscribe((st) => {
        if (st.refused && !told) {
          told = true;
          opts.onRefused(st.problem ?? 'MetaRush takes no more of this recording.');
        }
      });
      s.queues.set(t.id as TrackID, q);
    }
    // Every recorder made first, then all started together.
    for (const p of plan) {
      let r: MediaRecorder;
      try {
        // A keyframe every piece: an MP4 recorder writes a piece only from one
        // keyframe to the next, so a closed tab would lose everything since the last.
        const o = { ...(p.format ? { mimeType: p.format } : {}), videoBitsPerSecond: p.bits, audioBitsPerSecond: 128_000, videoKeyFrameIntervalDuration: PIECE_MS };
        r = new MediaRecorder(p.stream, o as MediaRecorderOptions);
      } catch {
        r = new MediaRecorder(p.stream);
      }
      const q = s.queues.get(p.id)!;
      r.ondataavailable = (e) => void q.add(e.data);
      r.onerror = () => opts.onProblem(`The ${p.id} stopped recording in the browser.`);
      s.recorders.set(p.id, r);
      p.stream.getVideoTracks()[0]!.addEventListener('ended', () => opts.onSourceEnded(p.id));
    }
    if (mic) {
      s.mic = mic;
      s.arrivals = new Arrivals(mic.sampleRate);
      mic.onBlock = (t, n) => s.arrivals!.add(t, n);
      micTrack?.addEventListener('ended', () => opts.onSourceEnded('voice'));
    }
    // How each recorder hands its pieces over (formats.ts pacing): worked out
    // first, so the recorders still start together.
    const chromium = isChromium();
    const paced = [...s.recorders.values()].map((r) => ({ r, ...pacing(r.mimeType, chromium) }));
    s.t0 = performance.now();
    for (const { r, timeslice } of paced) r.start(timeslice);
    for (const { r, handover } of paced) {
      if (handover) s.timers.push(window.setInterval(() => r.state === 'recording' && r.requestData(), handover));
    }
    mic?.start();
    if (mic) s.timers.push(window.setInterval(() => s.drainVoice(), 250));
    // The clock goes to the host soon after it starts (a tab closed in the first seconds keeps it) and then every few seconds.
    s.timers.push(window.setTimeout(() => void s.sendClock(), 1500));
    s.timers.push(window.setInterval(() => void s.sendClock(), CLOCK_EVERY_MS));
    return s;
  }

  /** Seconds recorded so far (pauses left out). */
  elapsed(now = performance.now()): number {
    return recordedAt(this.t0, this.pauses, now);
  }

  get paused(): boolean {
    const p = this.pauses[this.pauses.length - 1];
    return !!p && p.to === undefined;
  }

  get tracks(): TrackProgress[] {
    return [...this.queues.entries()].map(([id, q]) => ({ id, ...q.state }));
  }

  /** The fit of the microphone so far. */
  get micFit(): MicFit | undefined {
    return this.fit;
  }

  pause() {
    if (this.paused || this.stopping) return;
    const t = performance.now();
    for (const r of this.recorders.values()) if (r.state === 'recording') r.pause();
    this.gate?.pause(t);
    this.pauses.push({ from: t });
  }

  resume() {
    if (!this.paused || this.stopping) return;
    const t = performance.now();
    for (const r of this.recorders.values()) if (r.state === 'paused') r.resume();
    this.gate?.resume(t);
    this.pauses[this.pauses.length - 1]!.to = t;
  }

  /** The voice's samples since the last drain, without the paused stretches, to its queue. */
  private drainVoice(final?: Float32Array) {
    const mic = this.mic;
    if (!mic) return;
    const samples = final ?? mic.drain();
    this.fit = fitMic(this.arrivals?.list ?? [], mic.sampleRate) ?? this.fit;
    if (this.fit) {
      const rate = mic.sampleRate * (1 + this.fit.ppm / 1e6);
      if (!this.gate) {
        this.gate = new VoiceGate(this.fit.first, rate);
        for (const p of this.pauses) {
          this.gate.pause(p.from);
          if (p.to !== undefined) this.gate.resume(p.to);
        }
      } else this.gate.refit(this.fit.first, rate);
    }
    if (!samples.length) return;
    const parts = this.gate ? this.gate.keep(this.voiceSamples, samples.length) : ([[0, samples.length]] as [number, number][]);
    this.voiceSamples += samples.length;
    const pcm = toPCM16(samples, parts);
    if (pcm.length) void this.queues.get('voice')?.add(new Blob([pcm as BlobPart]));
  }

  /** The clock as measured now. */
  clock(ended?: 'stopped' | 'track'): RecordingClockIn {
    const now = performance.now();
    const tracks: RecordingClockIn['tracks'] = [];
    for (const [id, q] of this.queues) {
      const c = { id, start: 0, drift: 0, bytes: q.state.bytes };
      if (id === 'voice') {
        c.start = voiceStart(this.t0, this.fit);
        c.drift = this.fit?.ppm ?? 0;
      }
      tracks.push(c);
    }
    return { tracks, pauses: pausesOnClock(this.t0, this.pauses), length: Math.round(this.elapsed(now) * 1000) / 1000, spread: this.fit?.spread ?? 0, ...(ended ? { ended } : {}) };
  }

  private note(stopped: boolean): Pending {
    return {
      id: this.id,
      userId: this.userId,
      name: this.record.name,
      projectId: this.record.projectId,
      tracks: [...this.queues.entries()].map(([id, q]) => ({ id, bytes: q.state.bytes })),
      clock: this.queues.size ? this.clock() : undefined,
      stopped,
      startedAt: this.startedAt,
    };
  }

  private async sendClock() {
    if (Date.now() - this.lastClock < 1000) return;
    this.lastClock = Date.now();
    const c = this.clock();
    await remember({ ...this.note(!!this.stopping), clock: c });
    try {
      await request('POST', `/api/recordings/${this.id}/clock`, { json: c });
    } catch {
      /* sent again in a few seconds, and with the finish */
    }
  }

  /** Stops every recorder and waits for their last pieces. */
  stop(ended: 'stopped' | 'track' = 'stopped'): Promise<void> {
    if (this.stopping) return this.stopping;
    this.ended = ended;
    if (this.paused) this.resume();
    this.stopping = (async () => {
      const waits = [...this.recorders.values()].map(
        (r) =>
          new Promise<void>((resolve) => {
            if (r.state === 'inactive') return resolve();
            r.addEventListener('stop', () => resolve(), { once: true });
            try {
              r.stop();
            } catch {
              resolve();
            }
          }),
      );
      if (this.mic) {
        this.drainVoice();
        const rest = await this.mic.stop();
        this.drainVoice(rest);
        this.mic.onBlock = undefined;
      }
      await Promise.all(waits);
      for (const t of this.timers) (clearInterval(t), clearTimeout(t));
      this.timers = [];
      for (const r of this.recorders.values()) for (const tr of r.stream.getTracks()) if (tr.kind === 'audio') tr.stop(); // the sound copies
      await remember(this.note(true));
    })();
    return this.stopping;
  }

  /** Waits until the host has every piece. */
  async sent(): Promise<void> {
    await Promise.all([...this.queues.values()].map((q) => q.drained()));
  }

  /** Makes it footage on the host (after stop and sent; keep: what arrived, when the host refused the rest). */
  async finish(keep = false): Promise<RecordingInfo> {
    return finishOnHost(this.id, this.clock(this.ended), keep);
  }

  /** Stops sending (the page is going away); pieces stay in browser storage. */
  close() {
    for (const t of this.timers) clearInterval(t);
    for (const q of this.queues.values()) q.stop();
  }
}

/** Asks the host to make a recording footage, then forgets it in this browser. */
export async function finishOnHost(id: string, clock: RecordingClockIn | undefined, keep: boolean): Promise<RecordingInfo> {
  const out = await call('recording.finish', { recording: id, ...(clock ? { clock } : {}), keep });
  if (out.recording.state === 'ready' || out.recording.state === 'failed') await forget(id);
  return out;
}

/** A recording this browser didn't finish (a reload, a closed tab): its queues, restored from browser storage. */
export async function restoreQueues(p: Pending): Promise<Map<string, TrackQueue>> {
  const out = new Map<string, TrackQueue>();
  for (const t of p.tracks) {
    const q = new TrackQueue(senderFor(p.id, t.id), pieceStore(p.id, t.id, p.userId));
    const pieces = await piecesOf(p.id, t.id);
    if (pieces.length) {
      q.restore(pieces);
    } else {
      // Nothing waiting here: the host has all this browser recorded of it.
      q.restore([]);
    }
    out.set(t.id, q);
  }
  return out;
}

/** True when the error is the host saying the recording isn't there or has ended. */
export function isGone(e: unknown): boolean {
  const err = e instanceof ApiError ? e : toApiError(e);
  return err.code === 'E_NOT_FOUND' || (err.code === 'E_CONFLICT' && /thrown away/i.test(err.message));
}
