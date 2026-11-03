#!/usr/bin/env node
// Slides drawn to fit the cut they are made for (D393, render/slideshape.mjs).
//
//   node render/slideshape.test.mjs
import assert from 'node:assert/strict';
import { SHAPE_WIDTH, fitShape, turnForShape, viewportWidth } from './slideshape.mjs';

// Wide, or for no cut: drawn exactly as always.
for (const kind of ['code', 'stats', 'diagram']) {
  assert.equal(fitShape(undefined, kind), '', `${kind}: nothing added for no cut`);
  assert.equal(fitShape('landscape', kind), '', `${kind}: nothing added for a wide cut`);
}
assert.equal(viewportWidth('code', undefined), 2600);
assert.equal(viewportWidth('stats', 'landscape'), 1300);
assert.equal(viewportWidth('diagram', 'vertical'), 2600, 'a diagram is drawn by Mermaid at its own size');

// Vertical and square: narrower, long lines wrapped.
assert.match(fitShape('vertical', 'code'), /max-width: 980px/);
assert.match(fitShape('vertical', 'code'), /white-space: pre-wrap/);
assert.match(fitShape('square', 'code'), /max-width: 1360px/);
assert.match(fitShape('vertical', 'stats'), /width: 720px/);
assert.match(fitShape('vertical', 'stats'), /repeat\(2,/);
assert.match(fitShape('square', 'stats'), /repeat\(3,/);
assert.ok(SHAPE_WIDTH.code.vertical < SHAPE_WIDTH.code.square && SHAPE_WIDTH.stats.vertical < SHAPE_WIDTH.stats.square);
assert.equal(viewportWidth('code', 'vertical'), 1100);
assert.equal(viewportWidth('stats', 'vertical'), 840);

// A flowchart that runs across runs down for a vertical cut; nothing else changes.
assert.equal(turnForShape('flowchart LR\n  A --> B', 'vertical'), 'flowchart TB\n  A --> B');
assert.equal(turnForShape('graph RL\n  A --> B', 'vertical'), 'graph BT\n  A --> B');
assert.equal(turnForShape('%% the queue\ngraph LR\n  A --> B', 'vertical'), '%% the queue\ngraph TB\n  A --> B');
assert.equal(turnForShape('flowchart LR\n  A --> B', 'square'), 'flowchart LR\n  A --> B');
assert.equal(turnForShape('flowchart LR\n  A --> B', undefined), 'flowchart LR\n  A --> B');
assert.equal(turnForShape('flowchart TD\n  A --> B', 'vertical'), 'flowchart TD\n  A --> B');
assert.equal(turnForShape('sequenceDiagram\n  A->>B: LR', 'vertical'), 'sequenceDiagram\n  A->>B: LR');
assert.equal(turnForShape('flowchart LR\n  subgraph s [LR]\n  direction LR\n  end', 'vertical'), 'flowchart TB\n  subgraph s [LR]\n  direction LR\n  end');

console.log('slideshape: all checks passed');
