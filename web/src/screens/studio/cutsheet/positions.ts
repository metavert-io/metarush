/**
 * Cut sheet problems carry 1-based line numbers and 1-based character columns
 * (Unicode code points, as Go counts runes). Editors index text in UTF-16 code
 * units, so a column has to be walked, not added.
 */

/** UTF-16 offset of the code point at `col` (1-based) within a line. */
export function columnOffset(lineText: string, col: number): number {
  if (col <= 1) return 0;
  let units = 0;
  let points = 0;
  for (const ch of lineText) {
    if (points === col - 1) return units;
    units += ch.length;
    points++;
  }
  return lineText.length;
}

export interface LineInfo {
  from: number;
  text: string;
}

/** Document range for a problem; the whole line when there are no columns. */
export function problemRange(line: LineInfo, col?: number, endCol?: number): { from: number; to: number } {
  const end = line.from + line.text.length;
  if (!col) return { from: line.from, to: end };
  const from = line.from + columnOffset(line.text, col);
  const to = Math.min(end, endCol && endCol > col ? line.from + columnOffset(line.text, endCol) : from + 1);
  if (to > from) return { from, to };
  // Past the end of the line (e.g. "the end time is missing"): mark its last character.
  return end > line.from ? { from: end - 1, to: end } : { from: line.from, to: line.from };
}
