/*
 * A deadline for opening a camera or a microphone. Some devices never answer:
 * the browser's getUserMedia then never resolves (a virtual microphone whose
 * app isn't connected, as on a computer reached through a remote-desktop
 * app). The recorders give up DEVICE_DEADLINE_MS after the person allowed the
 * device (the browser's question isn't counted) and say which device didn't
 * answer, so the person can choose another; a device that answers late is
 * closed at once.
 *
 * No browser APIs: tested by web/scripts/recorder-test.mjs with a fake that
 * never resolves.
 */

/** How long a device may take to open. */
export const DEVICE_DEADLINE_MS = 9000;

/** The device didn't answer in time. */
export class NoAnswer extends Error {
  readonly device: string;
  constructor(device: string) {
    super(`The ${device} didn't answer.`);
    this.device = device;
  }
}

/**
 * Resolves with p, or rejects with NoAnswer ms after `armed` resolves (at
 * once by default): the browser's permission question is the person's time,
 * not the device's, so the deadline starts only once they allowed it. When p
 * resolves after the deadline, onLate gets its value (to close the device).
 */
export function withDeadline<T>(p: Promise<T>, ms: number, device: string, onLate?: (v: T) => void, armed: Promise<unknown> = Promise.resolve()): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    let late = false;
    let settled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    void armed.then(() => {
      if (settled) return;
      timer = setTimeout(() => {
        late = true;
        reject(new NoAnswer(device));
      }, ms);
    });
    p.then(
      (v) => {
        if (late) {
          onLate?.(v);
          return;
        }
        settled = true;
        clearTimeout(timer);
        resolve(v);
      },
      (e: unknown) => {
        if (late) return;
        settled = true;
        clearTimeout(timer);
        reject(e);
      },
    );
  });
}

/** A permission's state, as the browser can say it. */
export interface PermissionLike {
  state: string;
  addEventListener(type: 'change', fn: () => void): void;
}

/**
 * When the deadline for opening a device starts: at once when the browser
 * already allows it, once the person allows it when it asks first; never (no
 * deadline this time) when it can't say (some browsers) or it is blocked (the
 * browser refuses at once).
 */
export function armedBy(status: PermissionLike | null): Promise<void> | null {
  if (!status) return null;
  if (status.state === 'granted') return Promise.resolve();
  if (status.state !== 'prompt') return null;
  return new Promise<void>((resolve) => status.addEventListener('change', () => status.state === 'granted' && resolve()));
}

/** The browser's permission for a camera or microphone (null when it can't say). */
export async function permissionFor(name: 'camera' | 'microphone'): Promise<PermissionLike | null> {
  try {
    return navigator.permissions ? await navigator.permissions.query({ name: name as PermissionName }) : null;
  } catch {
    return null;
  }
}

/**
 * Opens a device with the deadline counted from the person's permission (see
 * armedBy); without one (the browser can't say), no deadline this time.
 */
export async function openWithDeadline<T>(open: () => Promise<T>, permission: 'camera' | 'microphone', device: string, onLate?: (v: T) => void): Promise<T> {
  const armed = armedBy(await permissionFor(permission));
  const p = open();
  return armed ? withDeadline(p, DEVICE_DEADLINE_MS, device, onLate, armed) : p;
}
