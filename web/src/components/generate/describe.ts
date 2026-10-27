import type { GenEstimate } from '../../api/types';
import { aOrAn } from '../../lib/format';

const resLabel = (r: string) => (r === '4k' ? '4K' : r);

/**
 * What a request makes, in a few words: "a 6-second shot, draft (720p), wide, made with Google Gemini"
 * ("an 8-second shot": the article follows how the number is said). Tested by web/scripts/article-test.mjs.
 */
export function describe(e: GenEstimate): string {
  let what = `${aOrAn(e.seconds)} ${e.seconds}-second shot`;
  if (e.operation === 'extend') what = `${e.seconds} more seconds`;
  else if (e.operation === 'edit') what = 'a changed copy of the clip';
  if (e.takes > 1) what = `${e.takes} takes of ${what}`;
  const q = e.quality === 'final' ? 'final quality' : 'draft';
  return `${what}, ${q} (${resLabel(e.resolution)}), ${e.aspectRatio === '9:16' ? 'tall' : 'wide'}, made with ${e.providerTitle}`;
}
