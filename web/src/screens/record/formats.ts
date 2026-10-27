/*
 * Which file format the browser records the screen and the camera in (D258):
 * H.264 with AAC sound in MP4 where the browser can (current Chrome, Edge and
 * Safari: plays everywhere), else VP9 or VP8 with Opus in WebM (Firefox,
 * older Chrome). MetaRush copies the file into a clean one without
 * re-encoding, and makes its own preview copies like for any footage.
 *
 * Pure functions, tested by web/scripts/recorder-test.mjs.
 */

const WITH_SOUND = [
  'video/mp4;codecs=avc1.64001F,mp4a.40.2',
  'video/mp4;codecs=avc1.42E01F,mp4a.40.2',
  'video/mp4;codecs=avc1,mp4a.40.2',
  'video/mp4;codecs=avc1,opus',
  'video/webm;codecs=vp9,opus',
  'video/webm;codecs=vp8,opus',
  'video/webm',
  'video/mp4',
];

const PICTURE_ONLY = ['video/mp4;codecs=avc1.64001F', 'video/mp4;codecs=avc1.42E01F', 'video/mp4;codecs=avc1', 'video/webm;codecs=vp9', 'video/webm;codecs=vp8', 'video/webm', 'video/mp4'];

/** The best format the browser records, or '' to let it choose (then read recorder.mimeType). */
export function pickFormat(supported: (type: string) => boolean, sound: boolean): string {
  for (const t of sound ? WITH_SOUND : PICTURE_ONLY) {
    try {
      if (supported(t)) return t;
    } catch {
      /* a browser that throws for a type doesn't record it */
    }
  }
  return '';
}

/**
 * Bits per second for a picture: enough that a screen's text stays sharp
 * (about 0.1 bit per pixel per frame), between 1.5 and 12 Mbit/s.
 */
export function videoBits(width: number, height: number, fps: number): number {
  const w = width > 0 ? width : 1280;
  const h = height > 0 ? height : 720;
  const f = fps > 0 ? Math.min(fps, 60) : 30;
  return Math.round(Math.min(12e6, Math.max(1.5e6, w * h * f * 0.1)));
}

/** The file format as the host keeps it: the media type without spaces, lower case. */
export function formatOf(mime: string): string {
  return mime.replace(/\s+/g, '').toLowerCase();
}
