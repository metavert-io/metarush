// Slides drawn to fit the cut they are made for (D393): render/generate.mjs
// draws a code slide, a repository card or a Mermaid diagram for a vertical
// or square cut narrower — long code lines wrapped under themselves, a card's
// numbers in rows, a flowchart that runs down — so the cut shows it large.
// Nothing changes for a wide cut, or a slide made for none: drawn as always.
// Tested by render/slideshape.test.mjs (npm run test:motion).

/** How wide each kind of slide is drawn for a vertical or a square cut (CSS pixels). */
export const SHAPE_WIDTH = { code: { vertical: 980, square: 1360 }, stats: { vertical: 720, square: 980 } };

/** The extra page style that fits a slide to the shape ('' for wide or none). */
export const fitShape = (shape, kind) => {
  const w = SHAPE_WIDTH[kind]?.[shape];
  if (!w) return '';
  if (kind === 'code') {
    return `
#panel { min-width: 0; max-width: ${w}px; }
.bar { flex-wrap: wrap; white-space: normal; row-gap: 6px; } .repo { margin-left: 0; flex-basis: 100%; }
.ln { white-space: pre-wrap; } .ln .t { flex: 1; min-width: 0; overflow-wrap: anywhere; }
`;
  }
  return `
#panel { width: ${w}px; }
.stats { display: grid; grid-template-columns: repeat(${shape === 'vertical' ? 2 : 3}, minmax(0, 1fr)); gap: 30px 36px; }
`;
};

/** The page's width for a slide: room for the widest it is drawn (the picture is cut to the panel). */
export const viewportWidth = (kind, shape) => {
  const w = SHAPE_WIDTH[kind]?.[shape];
  if (w) return w + 120;
  return kind === 'stats' ? 1300 : 2600;
};

/**
 * A Mermaid flowchart that runs across, turned to run down for a vertical
 * cut: the same words and boxes, laid out top to bottom. Other diagrams, and
 * every diagram for a wide or square cut, are drawn as written.
 */
export const turnForShape = (source, shape) => {
  if (shape !== 'vertical' || typeof source !== 'string') return source;
  return source.replace(/^(\s*(?:%%[^\n]*\n\s*)*(?:graph|flowchart))[ \t]+(LR|RL)\b/, (_m, head, dir) => `${head} ${dir === 'RL' ? 'BT' : 'TB'}`);
};
