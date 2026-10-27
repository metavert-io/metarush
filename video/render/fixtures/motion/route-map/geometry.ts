/** A gentle arc from the bottom left to the bottom right of a w × h box. */
export const arcPath = (w: number, h: number): string => `M 0 ${h} C ${w * 0.25} ${-h * 0.35}, ${w * 0.75} ${-h * 0.35}, ${w} ${h}`;
