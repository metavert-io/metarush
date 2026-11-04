/**
 * Whether a browser shows a video's see-through parts — the alpha channel of
 * a WebM's VP9, which a clip's cut-out is (D509, D510). Chrome, Edge, Firefox
 * and Android's browsers do. Safari plays VP9 without its alpha, and every
 * browser on an iPhone or iPad is Safari's engine whatever it is called
 * (Chrome's CriOS, Firefox's FxiOS, Edge's EdgiOS), an iPad's also when it
 * says it is a Mac (a Macintosh with a touch screen). Where it can't, the
 * preview plays such clips as recorded; renders draw in Chrome.
 *
 * Nothing here imports anything: the dashboard reads it too
 * (`@metarush/video/seethrough`), without the renderer.
 */
export const seeThroughIn = (userAgent: string, maxTouchPoints = 0): boolean => {
  const ua = userAgent || '';
  if (/iPhone|iPad|iPod/i.test(ua) || (/Macintosh/i.test(ua) && maxTouchPoints > 1)) return false;
  return !/^((?!chrome|chromium|android|edg).)*safari/i.test(ua);
};

/** Whether this browser shows see-through video (true where there is no browser to ask). */
export const seeThroughHere = (): boolean =>
  typeof navigator === 'undefined' ? true : seeThroughIn(navigator.userAgent, navigator.maxTouchPoints ?? 0);
