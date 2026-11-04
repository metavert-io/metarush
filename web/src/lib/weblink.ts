/**
 * webLink is a link someone may open: an http or https address with a host and
 * no sign-in in it (`https://youtube.com@evil.example/` is evil.example's), else
 * nothing. Links come from files people import too, so "Open the original" and
 * the panel's cards show only what this says (D444).
 */
export function webLink(u?: string | null): string | undefined {
  const h = (u ?? '').trim();
  if (!h) return undefined;
  let url: URL;
  try {
    url = new URL(h);
  } catch {
    return undefined;
  }
  if ((url.protocol !== 'http:' && url.protocol !== 'https:') || !url.hostname || url.username || url.password) return undefined;
  return url.href;
}
