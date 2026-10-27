/*
 * MetaRush versions in plain words (D130). The server says which build is
 * which and how computers compare (version.show); these only word it.
 */

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

/** "20 Oct 2026", the way the CLI and the changelog write a day. */
export function dayLabel(d: Date, utc = false): string {
  if (!Number.isFinite(d.getTime())) return '';
  return utc ? `${d.getUTCDate()} ${MONTHS[d.getUTCMonth()]} ${d.getUTCFullYear()}` : `${d.getDate()} ${MONTHS[d.getMonth()]} ${d.getFullYear()}`;
}

/** The day a build is known by: a release's own day, else the day it was built. */
export function versionDay(v: { dev: boolean; releaseDate?: string; date: string }): string {
  if (!v.dev && v.releaseDate) return dayLabel(new Date(`${v.releaseDate}T00:00:00Z`), true);
  return dayLabel(new Date(v.date));
}

const BLOCK = /^\s*(?:[-*+]\s|\d{1,4}[.)]\s|#|>|\||```|~~~)/;

/**
 * A changelog entry for the screen: CHANGELOG.md wraps its lines at ~120
 * characters (the terminal keeps them), the dashboard joins each paragraph and
 * list item back into one line so it flows, and code split over two lines reads whole.
 */
export function unwrapMarkdown(md: string): string {
  const out: string[] = [];
  let fence = false;
  for (const line of md.split('\n')) {
    const t = line.trim();
    const isFence = /^(```|~~~)/.test(t);
    const prev = out.length ? out[out.length - 1]! : '';
    if (isFence) fence = !fence;
    if (isFence || fence || t === '' || BLOCK.test(line) || prev.trim() === '' || /^\s*(```|~~~)/.test(prev)) {
      out.push(line);
    } else {
      out[out.length - 1] = `${prev} ${t}`;
    }
  }
  return out.join('\n');
}

/** The release a version is or follows ("0.2.0-dev.3" → "0.2.0"); the exact build stays in Details. */
export function releaseOf(v: string | undefined): string {
  if (!v) return '';
  return v.replace(/^v/, '').replace(/(?:^|-)dev(?:\.\d+)?$/, '').replace(/-$/, '');
}
