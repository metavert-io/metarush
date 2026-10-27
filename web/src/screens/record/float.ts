/*
 * The teleprompter in a small window that stays above other windows (D261):
 * the browser's document Picture-in-Picture (Chrome and Edge). Recording
 * another app's window, the person reads it there; it isn't part of that
 * window, so it never shows in the recording. A whole-screen recording does
 * show it (the setup says so).
 */

interface DocumentPiP {
  requestWindow(o: { width: number; height: number }): Promise<Window>;
  window: Window | null;
}

function api(): DocumentPiP | undefined {
  return (window as unknown as { documentPictureInPicture?: DocumentPiP }).documentPictureInPicture;
}

/** The browser can float the teleprompter. */
export function canFloat(): boolean {
  return typeof window !== 'undefined' && !!api();
}

/** Opens the floating window (from a click) with this page's look. */
export async function openFloat(): Promise<Window | null> {
  const pip = api();
  if (!pip) return null;
  const w = await pip.requestWindow({ width: 460, height: 300 });
  for (const sheet of Array.from(document.styleSheets)) {
    try {
      const css = Array.from(sheet.cssRules)
        .map((r) => r.cssText)
        .join('\n');
      const style = w.document.createElement('style');
      style.textContent = css;
      w.document.head.appendChild(style);
    } catch {
      if (sheet.href) {
        const link = w.document.createElement('link');
        link.rel = 'stylesheet';
        link.href = sheet.href;
        w.document.head.appendChild(link);
      }
    }
  }
  w.document.title = 'Teleprompter';
  for (const a of Array.from(document.documentElement.attributes)) w.document.documentElement.setAttribute(a.name, a.value);
  w.document.body.className = document.body.className;
  w.document.body.style.background = '#0b0a09';
  w.document.body.style.margin = '0';
  return w;
}
