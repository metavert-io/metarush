import { NoAnswer, openWithDeadline } from '../../lib/deadline';

/*
 * The screen recorder's sources (D258, D262): the screen through the
 * browser's picker (a window, a tab or a whole screen, with its sound where
 * the browser offers it; this dashboard's own tab is never offered, so the
 * teleprompter can't end up in a tab recording), the camera and the
 * microphone (capture.ts Mic). Problems are said plainly: what happened and
 * what to do.
 */

export type Source = 'screen' | 'camera' | 'microphone';
export type SourceProblemKind = 'denied' | 'system' | 'missing' | 'busy' | 'insecure' | 'unsupported' | 'noanswer' | 'other';

export class SourceError extends Error {
  readonly kind: SourceProblemKind;
  readonly source: Source;
  constructor(source: Source, kind: SourceProblemKind, message: string) {
    super(message);
    this.source = source;
    this.kind = kind;
  }
}

/** What went wrong opening a source, from the browser's error. */
export function sourceProblem(source: Source, e: unknown): SourceError {
  if (e instanceof SourceError) return e;
  if (e instanceof NoAnswer) return new SourceError(source, 'noanswer', e.message);
  const name = e instanceof DOMException ? e.name : '';
  const msg = e instanceof Error ? e.message : String(e);
  switch (name) {
    case 'NotAllowedError':
    case 'SecurityError':
      // Chrome says "by system" when the computer itself doesn't let the browser record the screen.
      return new SourceError(source, /system/i.test(msg) ? 'system' : 'denied', msg);
    case 'NotFoundError':
    case 'OverconstrainedError':
      return new SourceError(source, 'missing', msg);
    case 'NotReadableError':
    case 'AbortError':
      return new SourceError(source, 'busy', msg);
    case 'TypeError':
    case 'NotSupportedError':
      return new SourceError(source, 'unsupported', msg);
  }
  return new SourceError(source, 'other', msg);
}

/** Can this page record the screen at all? */
export function screenProblem(): SourceError | null {
  if (typeof window !== 'undefined' && !window.isSecureContext) return new SourceError('screen', 'insecure', 'Screen recording only works on a secure page.');
  if (!navigator.mediaDevices?.getDisplayMedia) return new SourceError('screen', 'unsupported', 'This browser cannot record the screen.');
  if (typeof MediaRecorder === 'undefined') return new SourceError('screen', 'unsupported', 'This browser cannot record video.');
  return null;
}

/** Asks the person what to share (from a click). */
export async function openScreen(): Promise<MediaStream> {
  const p = screenProblem();
  if (p) throw p;
  try {
    return await navigator.mediaDevices.getDisplayMedia({
      video: { frameRate: { ideal: 30, max: 30 } },
      audio: true,
      // Chrome: never offer this tab (the teleprompter is in it); a tab's own
      // sound when sharing a tab; a whole screen's sound where the system allows.
      selfBrowserSurface: 'exclude',
      systemAudio: 'include',
      surfaceSwitching: 'exclude',
      monitorTypeSurfaces: 'include',
    } as DisplayMediaStreamOptions);
  } catch (e) {
    throw sourceProblem('screen', e);
  }
}

/** What was shared: a whole screen, a window or a tab (unknown in some browsers). */
export function surfaceOf(stream: MediaStream | null | undefined): 'monitor' | 'window' | 'browser' | undefined {
  const s = stream?.getVideoTracks()[0]?.getSettings() as (MediaTrackSettings & { displaySurface?: string }) | undefined;
  const v = s?.displaySurface;
  return v === 'monitor' || v === 'window' || v === 'browser' ? v : undefined;
}

export interface CameraDevice {
  id: string;
  label: string;
}

/** Cameras (with names once the person allowed one). */
export async function listCameras(): Promise<CameraDevice[]> {
  if (!navigator.mediaDevices?.enumerateDevices) return [];
  const ds = await navigator.mediaDevices.enumerateDevices();
  let n = 0;
  return ds
    .filter((d) => d.kind === 'videoinput')
    .map((d) => ({ id: d.deviceId, label: d.label || `Camera ${++n}` }))
    .filter((d, i, list) => list.findIndex((x) => x.id === d.id) === i);
}

/** Opens a camera (720p at 30 frames a second where it can). */
export async function openCamera(deviceId?: string): Promise<MediaStream> {
  if (!window.isSecureContext) throw new SourceError('camera', 'insecure', 'The camera only works on a secure page.');
  if (!navigator.mediaDevices?.getUserMedia) throw new SourceError('camera', 'unsupported', 'This browser cannot use the camera.');
  try {
    return await openWithDeadline(
      () =>
        navigator.mediaDevices.getUserMedia({
          video: { ...(deviceId ? { deviceId: { exact: deviceId } } : {}), width: { ideal: 1280 }, height: { ideal: 720 }, frameRate: { ideal: 30 } },
        }),
      'camera',
      'camera',
      (late) => late.getTracks().forEach((t) => t.stop()),
    );
  } catch (e) {
    throw sourceProblem('camera', e);
  }
}

/** The plain words for a problem: a headline and what to do. */
export function problemWords(e: SourceError, ctx: { mac: boolean; remote: boolean; machine: string; publicUrl?: string }): string[] {
  const what = e.source === 'screen' ? 'the screen' : e.source === 'camera' ? 'the camera' : 'the microphone';
  const setting = e.source === 'screen' ? 'Screen & System Audio Recording' : e.source === 'camera' ? 'Camera' : 'Microphone';
  switch (e.kind) {
    case 'denied':
      return e.source === 'screen'
        ? ['Nothing is shared yet.', 'Choose a window, a tab or a screen in the browser’s picker. If no picker opened, the browser’s settings for this page block screen sharing: allow it there and try again.']
        : [
            `MetaRush isn't allowed to use ${what} yet.`,
            `Click the icon at the left of the address bar, set ${e.source === 'camera' ? 'Camera' : 'Microphone'} to Allow for this page, then try again.`,
            ...(ctx.mac ? [`If it is still blocked, open System Settings → Privacy & Security → ${setting} and turn it on for your browser.`] : []),
          ];
    case 'system':
      return [
        `This computer doesn't let the browser record ${what}.`,
        ctx.mac ? `Open System Settings → Privacy & Security → ${setting}, turn it on for your browser, then quit and reopen the browser.` : 'Allow the browser to record the screen in the system’s privacy settings, then reopen the browser.',
      ];
    case 'missing':
      return [`No ${e.source === 'camera' ? 'camera' : e.source === 'microphone' ? 'microphone' : 'screen'} was found.`, 'Plug one in or turn it on, then try again.'];
    case 'busy':
      return [`Another app is using ${what}, or it couldn't start.`, 'Close it (video calls often keep it), then try again.'];
    case 'insecure':
      return ctx.remote
        ? [
            `${what[0]!.toUpperCase()}${what.slice(1)} can only be recorded on a secure (https) page.`,
            ctx.publicUrl?.startsWith('https://')
              ? `Open MetaRush at ${ctx.publicUrl} and try again.`
              : `Ask the person who runs MetaRush on ${ctx.machine} to turn on HTTPS certificates for your Tailscale network (Settings → People → Remote access says how).`,
          ]
        : ['Recording only works when MetaRush is opened from this computer or its secure address.', 'Open the dashboard with its own link and try again.'];
    case 'noanswer':
      return [
        `${what[0]!.toUpperCase()}${what.slice(1)} didn't answer.`,
        `It may be a virtual ${e.source === 'camera' ? 'camera' : 'microphone'} whose app isn't running (a remote-desktop or video-call app's). Choose another one, or open that app, then try again.`,
      ];
    case 'unsupported':
      return [`This browser can't record ${what}.`, 'Use a current version of Chrome, Edge, Firefox or Safari on a computer (phones and tablets can’t share their screen).'];
  }
  return [`${what[0]!.toUpperCase()}${what.slice(1)} couldn't be started.`, e.message];
}
