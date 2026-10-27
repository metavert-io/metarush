/*
 * Microphone capture for the recorder (D63). The page records the
 * microphone itself (getUserMedia works on 127.0.0.1, a secure context): an
 * AudioWorklet copies the raw samples, and a take becomes a 16-bit mono WAV at
 * the microphone's sample rate, like the CLI recorder's (no lossy codec, no
 * missing duration headers). Browser processing (echo cancellation, noise
 * suppression, automatic gain) is turned off: cleanup is MetaRush's own,
 * optional and reversible.
 */

import { NoAnswer, openWithDeadline } from '../../../lib/deadline';

const WORKLET = `
class MetaRushTap extends AudioWorkletProcessor {
  constructor() {
    super();
    this.on = false;
    this.port.onmessage = (e) => { this.on = !!e.data.record; };
  }
  process(inputs) {
    const input = inputs[0];
    if (this.on && input && input.length) {
      const n = input[0].length;
      const mono = new Float32Array(n);
      for (let c = 0; c < input.length; c++) {
        const ch = input[c];
        for (let i = 0; i < n; i++) mono[i] += ch[i] / input.length;
      }
      this.port.postMessage(mono, [mono.buffer]);
    }
    return true;
  }
}
registerProcessor('metarush-tap', MetaRushTap);
`;

export type MicProblem = 'denied' | 'missing' | 'busy' | 'insecure' | 'unsupported' | 'noanswer' | 'other';

export class MicError extends Error {
  constructor(
    readonly kind: MicProblem,
    message: string,
  ) {
    super(message);
  }
}

/** What went wrong with the microphone, from the browser's error. */
export function micProblem(e: unknown): MicError {
  if (e instanceof MicError) return e;
  if (e instanceof NoAnswer) return new MicError('noanswer', "The microphone didn't answer.");
  const name = e instanceof DOMException ? e.name : '';
  switch (name) {
    case 'NotAllowedError':
    case 'SecurityError':
      return new MicError('denied', 'The browser is not allowed to use the microphone.');
    case 'NotFoundError':
    case 'OverconstrainedError':
      return new MicError('missing', 'No microphone was found.');
    case 'NotReadableError':
    case 'AbortError':
      return new MicError('busy', 'The microphone is in use by another app or could not be started.');
  }
  return new MicError('other', e instanceof Error ? e.message : String(e));
}

export interface MicDevice {
  id: string;
  label: string;
}

/** Microphones (with names once the person has allowed the microphone). */
export async function listMics(): Promise<MicDevice[]> {
  if (!navigator.mediaDevices?.enumerateDevices) return [];
  const ds = await navigator.mediaDevices.enumerateDevices();
  let n = 0;
  return ds
    .filter((d) => d.kind === 'audioinput')
    .map((d) => ({ id: d.deviceId, label: d.label || `Microphone ${++n}` }))
    .filter((d, i, all) => all.findIndex((x) => x.id === d.id) === i);
}

/** A live microphone: level meter, and takes recorded as PCM. */
export class Mic {
  private chunks: Float32Array[] = [];
  private recording = false;
  private buf: Float32Array<ArrayBuffer>;
  private peakHold = 0;
  /** Samples received since start(), and when each block arrived: [performance.now(), samples so far]. */
  private received = 0;
  private arrivals: [number, number][] = [];

  private constructor(
    readonly stream: MediaStream,
    readonly ctx: AudioContext,
    private readonly source: MediaStreamAudioSourceNode,
    private readonly analyser: AnalyserNode,
    private readonly tap: AudioWorkletNode,
    private readonly sink: GainNode,
  ) {
    this.buf = new Float32Array(analyser.fftSize);
    tap.port.onmessage = (e: MessageEvent<Float32Array>) => {
      if (!this.recording) return;
      this.chunks.push(e.data);
      this.received += e.data.length;
      const now = performance.now();
      if (this.arrivals.length < 200_000) this.arrivals.push([now, this.received]);
      this.onBlock?.(now, this.received);
    };
  }

  /**
   * Called for every block of samples as it arrives while recording: when
   * (performance.now()) and how many samples have arrived so far, the
   * measurements a long recording keeps for its clock (the screen recorder,
   * D259).
   */
  onBlock?: (at: number, received: number) => void;

  /** The samples recorded since start() or the last drain (a recording sent while it records). */
  drain(): Float32Array {
    const n = this.chunks.reduce((a, c) => a + c.length, 0);
    const out = new Float32Array(n);
    let o = 0;
    for (const c of this.chunks) {
      out.set(c, o);
      o += c.length;
    }
    this.chunks = [];
    return out;
  }

  /**
   * When (performance.now()) the take's first sample was heard, from when its
   * blocks arrived: each block arrives a little after its last sample, so the
   * earliest arrivals (a low percentile, robust to a busy page) say it best.
   */
  firstSampleAt(): number | undefined {
    const rate = this.ctx.sampleRate;
    const est = this.arrivals.map(([t, n]) => t - (n / rate) * 1000);
    return lowPercentile(est);
  }

  static async open(deviceId?: string): Promise<Mic> {
    if (!window.isSecureContext) throw new MicError('insecure', 'The microphone only works on a secure page.');
    if (!navigator.mediaDevices?.getUserMedia || typeof AudioWorkletNode === 'undefined') {
      throw new MicError('unsupported', 'This browser cannot record from the microphone.');
    }
    let stream: MediaStream;
    try {
      // A microphone that never answers (a virtual one whose app isn't
      // connected) would leave the recorder waiting forever: give up after a
      // deadline counted from the person's permission and say so; one that
      // answers late is closed at once.
      stream = await openWithDeadline(
        () =>
          navigator.mediaDevices.getUserMedia({
            audio: {
              ...(deviceId ? { deviceId: { exact: deviceId } } : {}),
              echoCancellation: false,
              noiseSuppression: false,
              autoGainControl: false,
              channelCount: { ideal: 1 },
            },
          }),
        'microphone',
        'microphone',
        (late) => late.getTracks().forEach((t) => t.stop()),
      );
    } catch (e) {
      throw micProblem(e);
    }
    const ctx = new AudioContext({ latencyHint: 'interactive' });
    const url = URL.createObjectURL(new Blob([WORKLET], { type: 'text/javascript' }));
    try {
      await ctx.audioWorklet.addModule(url);
    } finally {
      URL.revokeObjectURL(url);
    }
    const source = ctx.createMediaStreamSource(stream);
    const analyser = ctx.createAnalyser();
    analyser.fftSize = 2048;
    const tap = new AudioWorkletNode(ctx, 'metarush-tap', { numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1] });
    // The worklet must be pulled by the graph to run; a silent gain keeps it
    // connected without playing the microphone back.
    const sink = ctx.createGain();
    sink.gain.value = 0;
    source.connect(analyser);
    source.connect(tap);
    tap.connect(sink);
    sink.connect(ctx.destination);
    // Browsers may keep a new audio context suspended until the person clicks;
    // never wait for it here (the Record button resumes it).
    void ctx.resume().catch(() => undefined);
    return new Mic(stream, ctx, source, analyser, tap, sink);
  }

  /** Wakes the audio context (call from a click). */
  resume() {
    if (this.ctx.state !== 'running') void this.ctx.resume().catch(() => undefined);
  }

  get sampleRate(): number {
    return this.ctx.sampleRate;
  }

  get label(): string {
    return this.stream.getAudioTracks()[0]?.label ?? '';
  }

  get deviceId(): string | undefined {
    return this.stream.getAudioTracks()[0]?.getSettings().deviceId;
  }

  /** Current level: rms and peak as 0..1, and a peak that falls back slowly (for meters). */
  level(): { rms: number; peak: number; hold: number } {
    this.analyser.getFloatTimeDomainData(this.buf);
    let s = 0;
    let p = 0;
    for (const v of this.buf) {
      s += v * v;
      const a = Math.abs(v);
      if (a > p) p = a;
    }
    const rms = Math.sqrt(s / this.buf.length);
    this.peakHold = Math.max(p, this.peakHold * 0.94);
    return { rms, peak: p, hold: this.peakHold };
  }

  start() {
    this.chunks = [];
    this.received = 0;
    this.arrivals = [];
    this.recording = true;
    this.tap.port.postMessage({ record: true });
  }

  /** Stops the take and returns its samples. */
  async stop(): Promise<Float32Array> {
    this.tap.port.postMessage({ record: false });
    // The worklet posts in 128-sample blocks: give the last ones a moment to arrive.
    await new Promise((r) => setTimeout(r, 60));
    this.recording = false;
    const n = this.chunks.reduce((a, c) => a + c.length, 0);
    const out = new Float32Array(n);
    let o = 0;
    for (const c of this.chunks) {
      out.set(c, o);
      o += c.length;
    }
    this.chunks = [];
    return out;
  }

  get isRecording(): boolean {
    return this.recording;
  }

  close() {
    this.recording = false;
    try {
      this.source.disconnect();
      this.tap.disconnect();
      this.sink.disconnect();
    } catch {
      /* already gone */
    }
    for (const t of this.stream.getTracks()) t.stop();
    void this.ctx.close().catch(() => undefined);
  }
}

/** The 10th percentile of some estimates (undefined for none). */
export function lowPercentile(xs: number[]): number | undefined {
  if (!xs.length) return undefined;
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.floor((s.length - 1) * 0.1)];
}

/**
 * Where a live take's first sample belongs on the timeline (D85): the cut
 * played from `from` while the take was recorded. The player's frame changes
 * (performance.now(), frame) say when the picture reached each moment; the
 * microphone says when the take's first sample was heard. Both clocks are
 * read at their earliest (a busy page only delays them).
 */
export function takeStart(from: number, fps: number, frames: [number, number][], firstSample: number | undefined): number {
  if (firstSample === undefined || !frames.length) return from;
  // When the picture was at `from`, from every frame change after it.
  const at = lowPercentile(frames.filter(([, f]) => f / fps >= from - 1e-6).map(([t, f]) => t - (f / fps - from) * 1000));
  if (at === undefined) return from;
  return Math.max(0, Math.round((from + (firstSample - at) / 1000) * 1000) / 1000);
}

/** A take as a 16-bit mono WAV file. */
export function encodeWav(samples: Float32Array, rate: number): Blob {
  const n = samples.length;
  const buf = new ArrayBuffer(44 + n * 2);
  const v = new DataView(buf);
  const text = (o: number, s: string) => {
    for (let i = 0; i < s.length; i++) v.setUint8(o + i, s.charCodeAt(i));
  };
  text(0, 'RIFF');
  v.setUint32(4, 36 + n * 2, true);
  text(8, 'WAVE');
  text(12, 'fmt ');
  v.setUint32(16, 16, true);
  v.setUint16(20, 1, true); // PCM
  v.setUint16(22, 1, true); // mono
  v.setUint32(24, rate, true);
  v.setUint32(28, rate * 2, true);
  v.setUint16(32, 2, true);
  v.setUint16(34, 16, true);
  text(36, 'data');
  v.setUint32(40, n * 2, true);
  for (let i = 0; i < n; i++) {
    const x = Math.max(-1, Math.min(1, samples[i]!));
    v.setInt16(44 + i * 2, x < 0 ? x * 0x8000 : x * 0x7fff, true);
  }
  return new Blob([buf], { type: 'audio/wav' });
}

/**
 * Where the voice is in a take: from the first to the last stretch of sound
 * clearly above the background, with a little air (0.15 s before, 0.3 s
 * after). The take keeps the whole file; only the part the line plays is set.
 * Silence or a take too quiet to tell returns the whole take.
 */
export function voicedRange(samples: Float32Array, rate: number): { in: number; out: number; peak: number } {
  const dur = samples.length / rate;
  const win = Math.max(1, Math.round(rate * 0.02));
  const levels: number[] = [];
  let peak = 0;
  for (let i = 0; i + win <= samples.length; i += win) {
    let s = 0;
    for (let j = i; j < i + win; j++) {
      const x = samples[j]!;
      s += x * x;
      if (Math.abs(x) > peak) peak = Math.abs(x);
    }
    levels.push(Math.sqrt(s / win));
  }
  // Too quiet to tell voice from background (the loudest moment under −34 dBFS):
  // keep the whole take; the recorder says it is very quiet.
  if (levels.length === 0 || peak < 0.02) return { in: 0, out: dur, peak };
  const sorted = [...levels].sort((a, b) => a - b);
  const floor = sorted[Math.floor(sorted.length * 0.1)] ?? 0;
  const loud = sorted[Math.floor(sorted.length * 0.95)] ?? peak;
  // Above the background and within 30 dB of the loud parts.
  const thr = Math.max(floor * 3, loud * 0.0316, 0.002);
  const first = levels.findIndex((l) => l > thr);
  let last = -1;
  for (let i = levels.length - 1; i >= 0; i--)
    if (levels[i]! > thr) {
      last = i;
      break;
    }
  if (first < 0 || last < first) return { in: 0, out: dur, peak };
  const a = Math.max(0, (first * win) / rate - 0.15);
  const b = Math.min(dur, ((last + 1) * win) / rate + 0.3);
  if (b - a < 0.3) return { in: 0, out: dur, peak };
  return { in: Math.round(a * 1000) / 1000, out: Math.round(b * 1000) / 1000, peak };
}
